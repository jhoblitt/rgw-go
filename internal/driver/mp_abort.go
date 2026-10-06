package driver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// AbortMultipartUpload is RGWAbortMultipart::execute (rgw_op.cc:6614-6650 at
// v19.2.6, :7538-7574 at v20.2.4) over RadosMultipartUpload::abort
// (driver/rados/rgw_sal_rados.cc:3151-3263 at v19.2.6, :3995-4107 at
// v20.2.4); a bare line number below is v19.2.6's rgw_sal_rados.cc.

// Abort implements op.MultipartStore as AbortMultipartUpload aborts an
// upload:
//   - the RGWCompleteMultipart lock on the meta object (lockMeta), under a
//     cookie of this abort's own (newAbortCookie), whose errors FromRADOS
//     maps: ENOENT NoSuchUpload, any other holder (EBUSY) 503;
//   - then up to maxMetaDeleteRetries rounds, each reading the meta object
//     and the key's head (abortRound). A round whose meta object removal
//     meets a racing part's version, ECANCELED, starts again, and the last
//     ECANCELED answers 409 ConcurrentModification;
//   - the unlock RGWAbortMultipart::execute sends afterwards, under the same
//     cookie, which meets a removed object and is ignored.
//
// Unlike radosgw, which queues the parts for the GC before it removes the
// meta object, rgw-go queues them only once the meta object is gone, so an
// abort that fails or is canceled leaves an upload that can still be
// completed or aborted over intact parts, and a crash between the two leaks
// the parts instead (docs/exclusions.md, "AbortMultipartUpload removes the
// upload before it frees the parts"). Once the lock is taken the abort runs
// to its end whatever the client does.
func (s *Store) Abort(ctx context.Context, up *op.Upload) error {
	ref, err := s.metaRef(ctx, up.Bucket, up.Key, up.ID)
	if err != nil {
		return err
	}
	// Taken before the lock request, so the OSD starts the lock's term no
	// earlier than this.
	lockedAt := s.now()
	cookie := newAbortCookie()
	if err := s.lockMeta(ctx, ref, cookie); err != nil {
		return fmt.Errorf("locking upload %s: %w", up.ID, op.FromRADOS(err, op.ScopeUpload))
	}
	ctx = context.WithoutCancel(ctx)
	defer s.unlockMeta(ctx, ref, cookie)
	a := abortState{lockedAt: lockedAt, cookie: cookie}
	for attempt := range maxMetaDeleteRetries {
		err := s.abortRound(ctx, up, ref, &a)
		if !errors.Is(err, radosclient.ErrCanceled) {
			return err
		}
		if attempt < maxMetaDeleteRetries-1 {
			slog.DebugContext(ctx, "multipart meta object removal canceled by a racing part; retrying", slog.String("upload", up.ID))
		}
	}
	return fmt.Errorf("removing the meta object of upload %s: %w", up.ID, op.FromRADOS(radosclient.ErrCanceled, op.ScopeUpload))
}

// newAbortCookie is a lock cookie no other hold shares: 128 random bits in
// hex. A cls_lock locker is the request's entity and its cookie
// (cls_lock.cc:174-178, and locker_id_t's ordering, cls_lock_types.h:80-86,
// at v19.2.6 and v20.2.4), and every request of this gateway is one entity,
// so only the cookie tells the abort's hold from one this gateway's own
// completion takes, under cookie "", after the abort's lapsed. The exclusive
// lock still refuses the second holder either way (cls_lock.cc:201-212), and
// the removal's assert_locked fails on the OSD once the abort's hold is gone
// (:507-519). A radosgw gateway is another entity, so the cookie changes
// nothing in how its locks and this one exclude each other.
func newAbortCookie() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) //nolint:errcheck // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// abortState is what abort carries from one round to the next: when it
// asked for its lock and the cookie it holds it under, and the parts'
// accounted size and the prefixes it was counted for, processed_prefixes,
// so a part counted in an earlier round is not counted again, as abort's
// `continue` skips it (:3196-3201, :3218).
type abortState struct {
	lockedAt  time.Time
	cookie    string
	counted   partCleanup
	accounted uint64
}

// maxAbortMargin caps abortMargin.
const maxAbortMargin = 30 * time.Second

// abortMargin is how much of the lock's term an abort keeps in hand for the
// meta object's removal: the removal is sent only while at least this much
// of the term remains, and its reply is awaited no longer. Neither makes the
// removal safe, which is the cookie's assertion on the OSD: they answer 503
// early rather than send an op that is likely to be refused, and bound the
// wait. 30 seconds is far beyond a healthy write's latency; a quarter of the
// term keeps a short rgw_mp_lock_max_time usable.
func (s *Store) abortMargin() time.Duration {
	return min(maxAbortMargin, s.mp.lockMaxTime/4)
}

// lockTermLeft answers 503 when less than abortMargin of the lock's term,
// counted from a.lockedAt, remains. A term of zero never lapses.
func (s *Store) lockTermLeft(up *op.Upload, a *abortState) error {
	if s.mp.lockMaxTime <= 0 {
		return nil
	}
	if elapsed := s.now().Sub(a.lockedAt); elapsed >= s.mp.lockMaxTime-s.abortMargin() {
		return fmt.Errorf("%w: the lock on upload %s was taken %s ago and may lapse before the abort ends",
			op.ErrServiceUnavailable, up.ID, elapsed)
	}
	return nil
}

// count adds part p's accounted size unless an earlier round counted its
// current upload. A part without a manifest is counted every round, as
// radosgw's branch for it never reaches the `continue`.
func (a *abortState) count(p meta.UploadPartInfo) {
	if !partManifestEmpty(p.Manifest) && p.Manifest.Prefix != "" && a.counted.seen(p.Num, p.Manifest.Prefix) {
		return
	}
	a.accounted += p.AccountedSize
}

// abortRound is one round of abort. It reads the meta object, whose ENOENT
// is NoSuchUpload, and the key's head, and then:
//   - when the meta object carries a completion record whose head carries the
//     recorded tag, the upload was completed and its parts are that object's:
//     the meta object alone is removed, as ceph/ceph#72103's abort does
//     (rgw_sal_rados.cc:4236-4263 at its head 2c1db8239cc), and no part is
//     listed or queued. A record naming a version other than the key's own
//     head is 501, as rgw-go reads no version;
//   - otherwise, when the head names an object of the upload, an earlier
//     completion left its meta object behind without a record (tracker
//     #80896): NoSuchUpload, with nothing changed, where v19.2.6 and v20.2.4
//     queue the object's parts for the GC;
//   - otherwise every part's current and past uploads are retired: their
//     index entries with the meta object's removal, under the version read,
//     and once the meta object is gone their stripes, heads included, to the
//     GC under the upload id in one chain, synchronously. A part without a
//     manifest, which only a gateway older than Hammer wrote, has no objects
//     to name; it is logged and skipped, and its head's entry under the
//     upload's own prefix is retired.
//
// The removal asserts the abort's own hold of the lock, so the OSD refuses
// it once that hold lapsed, and keeps every entry when it fails
// (metaDelete.abort); it is sent only while abortMargin of the lock's term
// remains (lockTermLeft), and one whose reply is not in within abortMargin,
// or that timed out, may yet land, and queues nothing.
func (s *Store) abortRound(ctx context.Context, up *op.Upload, ref mpRef, a *abortState) error {
	ms, err := s.readMeta(ctx, ref)
	if err != nil {
		return err
	}
	cur, err := s.readHead(ctx, up.Bucket, up.Key, false)
	if err != nil {
		return err
	}
	d := metaDelete{rec: up.Bucket, key: up.Key, ref: ref, version: ms.version, mtime: ms.mtime, abort: true, cookie: a.cookie}
	if s.mp.lockMaxTime > 0 {
		d.deadline = s.abortMargin()
	}
	if tag, ok := ms.attrs[attrMPCompletionTag]; ok {
		if inst, ok := ms.attrs[attrMPCompletionInstance]; ok && string(inst) != nullInstance {
			return fmt.Errorf("%w: upload %s was completed into version %q of %s, which rgw-go does not read",
				op.ErrNotImplemented, up.ID, inst, up.Key.Name)
		}
		if cur.Exists && bytes.Equal(cur.Attrs[meta.AttrIDTag], tag) {
			if lerr := s.lockTermLeft(up, a); lerr != nil {
				return lerr
			}
			slog.InfoContext(ctx, "removing the meta object of a completed upload; its parts are the object's", slog.String("upload", up.ID))
			d.accounted = ms.size
			return abortErr(up, s.deleteMeta(ctx, d))
		}
	}
	parts, err := s.listAllParts(ctx, ref, up.ID)
	if err != nil {
		return err
	}
	if cur.Exists && namesUploadObjects(cur.Manifest, uploadPrefixes(up.Key.Name, up.ID, parts)) {
		slog.WarnContext(ctx, "not aborting an upload whose parts the key's object names", slog.String("upload", up.ID), slog.String("key", up.Key.Name))
		return fmt.Errorf("%w: %w: upload %s was completed as %s", op.ErrNoSuchUpload, errUploadStands, up.ID, up.Key.Name)
	}
	var cleanup partCleanup
	for i := range parts {
		p := &parts[i]
		a.count(*p)
		if partManifestEmpty(p.Manifest) {
			slog.WarnContext(ctx, "skipping a multipart part without a manifest on abort",
				slog.String("upload", up.ID), slog.Uint64("part", uint64(p.Num)))
			head := meta.ObjKey{Name: meta.MultipartPartName(meta.MultipartPrefix(up.Key.Name, up.ID), p.Num), NS: meta.NSMultipart}
			cleanup.removeObjs = append(cleanup.removeObjs, rgw.ObjKey{Name: head.IndexKeyName()})
			continue
		}
		if err := s.retireCurrentPart(*p, &cleanup); err != nil {
			return err
		}
		if err := s.retirePart(*p, &cleanup); err != nil {
			return err
		}
	}
	d.removeObjs, d.accounted = cleanup.removeObjs, a.accounted
	if err := s.lockTermLeft(up, a); err != nil {
		return err
	}
	if err := s.deleteMeta(ctx, d); err != nil {
		return abortErr(up, err)
	}
	if len(cleanup.chain) > 0 {
		s.enqueueGC(ctx, cleanup.chain, up.ID)
	}
	return nil
}

// errUploadStands marks the NoSuchUpload of an upload whose parts the key's
// object names, which a bucket delete skips without having aborted it.
var errUploadStands = errors.New("the upload's parts back the key's object")

// abortErr is abort's answer to a failed removal of the meta object: a meta
// object already gone is NoSuchUpload (:3262); ECANCELED stays as it is for
// the caller's retry.
func abortErr(up *op.Upload, err error) error {
	if errors.Is(err, radosclient.ErrNotFound) {
		return fmt.Errorf("%w: the meta object of upload %s is gone", op.ErrNoSuchUpload, up.ID)
	}
	return err
}
