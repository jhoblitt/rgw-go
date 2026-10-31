package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// AbortMultipartUpload is RGWAbortMultipart::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6614-L6650 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7538-L7574 at v20.2.4)
// over RadosMultipartUpload::abort
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3151-L3263 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3995-L4107 at v20.2.4).

// Abort implements op.MultipartStore as AbortMultipartUpload aborts an
// upload:
//   - the RGWCompleteMultipart lock on the meta object (lockMeta), under a
//     cookie of this abort's own (newLockCookie), whose errors FromRADOS
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
	cookie := newLockCookie()
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

// abortState is what abort carries from one round to the next: when it
// asked for its lock and the cookie it holds it under, and the parts'
// accounted size and the prefixes it was counted for, processed_prefixes,
// so a part counted in an earlier round is not counted again, as abort's
// `continue` skips it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3195-L3201,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3218).
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
// is NoSuchUpload, and then:
//   - when the meta object carries a completion record whose head
//     (recordedHead), the version the record names, current or not, carries
//     the recorded tag and is a completed object (recordedCompleted), the
//     upload was completed and its parts are that version's: the meta object
//     alone is removed, as ceph/ceph#72103's abort does
//     (https://github.com/ceph/ceph/blob/2c1db8239cc/src/rgw/driver/rados/rgw_sal_rados.cc#L4236-L4263
//     at its head 2c1db8239cc), and no part is listed or queued. A version
//     no link of which landed is removed first, and the abort goes on;
//   - otherwise, when a version of the key, current or not (uploadVersion),
//     names an object of the upload, an earlier completion left its meta
//     object behind without a record (tracker #80896): NoSuchUpload, with
//     nothing changed, where v19.2.6 and v20.2.4 queue the object's parts for
//     the GC;
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
	d := metaDelete{rec: up.Bucket, key: up.Key, ref: ref, version: ms.version, mtime: ms.mtime, abort: true, cookie: a.cookie}
	if s.mp.lockMaxTime > 0 {
		d.deadline = s.abortMargin()
	}
	if tag, ok := ms.attrs[attrMPCompletionTag]; ok {
		head, herr := s.recordedHead(ctx, up.Bucket, up.Key, ms.attrs)
		if herr != nil {
			return herr
		}
		if head.Exists && bytes.Equal(head.Attrs[meta.AttrIDTag], tag) {
			completed, cerr := s.recordedCompleted(ctx, up, ref, head, tag, a)
			if cerr != nil {
				return cerr
			}
			if completed {
				if lerr := s.lockTermLeft(up, a); lerr != nil {
					return lerr
				}
				slog.InfoContext(ctx, "removing the meta object of a completed upload; its parts are the object's", slog.String("upload", up.ID))
				d.accounted = ms.size
				return abortErr(up, s.deleteMeta(ctx, d))
			}
		}
	}
	parts, err := s.listAllParts(ctx, ref, up.ID)
	if err != nil {
		return err
	}
	named, err := s.uploadVersion(ctx, up, ref, parts)
	if err != nil {
		return err
	}
	if named != nil {
		slog.WarnContext(ctx, "not aborting an upload whose parts a version of the key names",
			slog.String("upload", up.ID), slog.String("key", up.Key.Name), slog.String("instance", named.Key.Instance))
		return fmt.Errorf("%w: %w: upload %s was completed as %s", op.ErrNoSuchUpload, errUploadStands, up.ID, versionName(up.Bucket, named.Key))
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

// recordedCompleted reports whether head, the head a completion record of up
// names, carrying the recorded tag, is a completed object, which keeps the
// upload's parts: an unversioned head, the null version, whose head is its
// key's OLH head and is left as a failed write leaves it, or a version a link
// of which landed (recordedLinkState). A version no link of which landed,
// which a retry answered 500 or 412 for, is removed, its instance entry
// before its head (removeUnlinkedRecorded), its parts to the GC, and false
// lets the abort retire the upload, but only once no link of it can still
// land (linkSettled), and only while abortMargin of the abort's lock term
// remains (lockTermLeft) and the OSD confirms the abort's hold
// (assertAbortLock), both checked just before the removal. Otherwise, or
// when the instance entry is another write's, or a link of the version or
// another op on it meets the removal, it is InternalError, or the term's
// ServiceUnavailable, with nothing changed.
func (s *Store) recordedCompleted(ctx context.Context, up *op.Upload, ref mpRef, head *op.ObjectState, tag []byte, a *abortState) (bool, error) {
	if isNullVersion(head.Key) {
		return true, nil
	}
	rec := up.Bucket
	settled, err := s.linkSettled(ctx, rec, head.Key)
	if err != nil {
		return false, err
	}
	rl, err := s.recordedLinkState(ctx, rec, head, tag, byTag)
	switch {
	case err != nil:
		return false, err
	case rl.linked:
		return true, nil
	case rl.found && !rl.ours:
		return false, fmt.Errorf("%w: aborting upload %s, whose recorded %s has another write's index entry",
			op.ErrInternalError, up.ID, versionName(rec, rl.key))
	case !settled:
		return false, fmt.Errorf("%w: aborting upload %s: a link of its recorded %s may still land",
			op.ErrInternalError, up.ID, versionName(rec, rl.key))
	}
	if terr := s.lockTermLeft(up, a); terr != nil {
		return false, terr
	}
	if aerr := s.assertAbortLock(ctx, up, ref, a.cookie); aerr != nil {
		return false, aerr
	}
	slog.InfoContext(ctx, "removing the version a recorded multipart completion wrote and never linked",
		slog.String("upload", up.ID), slog.String("instance", rl.key.Instance))
	if err := s.removeUnlinkedRecorded(ctx, rec, head, rl, tag); err != nil {
		if errors.Is(err, errNotRemovable) {
			return false, fmt.Errorf("%w: aborting upload %s: removing its unlinked %s: %w",
				op.ErrInternalError, up.ID, versionName(rec, rl.key), err)
		}
		return false, err
	}
	return false, nil
}

// errNotRemovable marks an abort's refusal to remove a recorded version it
// found unlinked, which a link of it or another op on it, landed since, makes
// unsafe.
var errNotRemovable = errors.New("the version cannot be removed safely")

// removeUnlinkedRecorded removes head, the head of a version with an
// instance that a recorded completion wrote under tag and never linked, as
// rl, its index entries as recordedLinkState read them, tells. Its index
// entry goes before its head, so that no link of it, from any gateway and at
// any moment, leaves it listed without its head:
//   - an entry the completion prepared and never completed is first
//     completed from head (completeRecordedEntry), so that the completion's
//     pending op leaves it: complete_op's DEL keeps an entry while another op
//     is pending on it
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1127-L1167 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1261-L1299 at v20.2.4),
//     and the completion's own index completion, landing late, would then
//     find it and make it exist again;
//   - the index prepare DEL of the version under a fresh tag;
//   - its instance entry, read after the prepare with the bytes bi_get
//     answers as stored (getInstanceEntryBI), and read again once an op on it
//     older than the shard's tag timeout was canceled (expireStaleOps),
//     which must be the completion's entry, completed under its tag, or none
//     but the one the prepare made, with no op pending on it but the
//     prepare's and no versioned epoch, which every link sets
//     (BIVerObjEntry::write,
//     https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554 at v20.2.4)
//     (removableEntry);
//   - in one op on the shard, an OMAP_CMP of those bytes, which fails the
//     whole op once a link rewrote the entry since the read
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7802-L7875 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7879-L7952 at v20.2.4),
//     and the DEL's completion, which removes the entry: a link sent after it
//     meets bucket_link_olh's ENOENT
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829 at v20.2.4),
//     and an index completion under any tag complete_op's EINVAL
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1066-L1073 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1201-L1208 at v20.2.4);
//   - only then the head (removeUnindexedHead).
//
// radosgw's delete_obj removes the head before the entry (docs/exclusions.md,
// "AbortMultipartUpload leaves a completed upload's parts alone"). A refusal
// before the entry's removal cancels the prepare and wraps errNotRemovable,
// with nothing removed. An abort that stops between the entry's removal and
// the head's leaves the head unindexed and the record naming it, and the
// next abort removes it this way, judging the entry its own prepare makes.
func (s *Store) removeUnlinkedRecorded(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState, rl recordedLink, tag []byte) error {
	key := rl.key
	if rl.found && !rl.completed {
		if err := s.completeRecordedEntry(ctx, rec, head, rl); err != nil {
			return fmt.Errorf("%w: completing its index entry under its completion's tag: %w", errNotRemovable, err)
		}
	}
	x := s.newIndexOp(rec, key, "")
	x.bilogFlags = rgw.BILogFlagVersionedOp
	if err := x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	bi, err := s.removableEntryRead(ctx, rec, key, x, rl.writeTag)
	if err == nil {
		err = x.completeDelAsRead(ctx, bi, head.Mtime)
	}
	if err != nil {
		x.cancel()
		return err
	}
	return s.removeUnindexedHead(ctx, rec, key, head, tag)
}

// removableEntryRead reads key's instance entry after x's prepare
// (getInstanceEntryBI), cancels the ops on it older than the tag timeout and
// reads it again (expireStaleOps), and judges it (removableEntry), answering
// the raw entry the removal's op compares.
func (s *Store) removableEntryRead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, x *indexOp, writeTag string) (rgw.BIEntry, error) {
	read := func() (rgw.DirEntry, rgw.BIEntry, error) {
		en, bi, err := s.getInstanceEntryBI(ctx, rec, key)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return en, bi, fmt.Errorf("%w: its index entry went after the prepare", errNotRemovable)
		case err != nil:
			return en, bi, op.FromRADOS(fmt.Errorf("reading the index entry of %s: %w", versionName(rec, key), err), op.ScopeObject)
		}
		return en, bi, nil
	}
	en, bi, err := read()
	if err != nil {
		return bi, err
	}
	expired, err := s.expireStaleOps(ctx, rec, key, x, en, writeTag)
	if err != nil {
		return bi, err
	}
	if expired {
		if en, bi, err = read(); err != nil {
			return bi, err
		}
	}
	return bi, removableEntry(en, writeTag, x.tag)
}

// pendingOpExpiryDefault is CEPH_RGW_DEFAULT_TAG_TIMEOUT, and the default of
// rgw_pending_bucket_index_op_expiration, 120 seconds
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L21 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L3902-L3914 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L24 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L4188-L4200 at v20.2.4).
const pendingOpExpiryDefault = 120 * time.Second

// expireStaleOps cancels each op pending on en, key's instance entry as read
// after x's prepare, that is older than the tag timeout of x's shard
// (indexTagTimeout), with complete_op CANCEL under its tag, sent and awaited,
// and reports whether it canceled any. That is the rule dir_suggest_changes
// applies before it changes an entry, which takes an op whose time is past
// the tag timeout for dead and drops it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2283-L2299 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2453-L2469 at v20.2.4),
// and an unlinked version, listed nowhere, never meets a listing's
// suggestion. The op's time is the OSD's at its prepare; the abort compares
// it with the gateway's clock, where the class compares the OSD's. The
// abort's own prepare is left, and so is an op under writeTag, the
// completion's, which removeUnlinkedRecorded completes from the head rather
// than cancel; a younger op is left too, and removableEntry refuses it. A
// cancel the class answers EINVAL, its op completed meanwhile, counts as one
// that changed the entry.
func (s *Store) expireStaleOps(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, x *indexOp, en rgw.DirEntry, writeTag string) (bool, error) {
	var others []rgw.PendingEntry
	for _, p := range en.PendingMap {
		if p.Tag != x.tag && p.Tag != writeTag {
			others = append(others, p)
		}
	}
	if len(others) == 0 {
		return false, nil
	}
	timeout, err := s.indexTagTimeout(ctx, x)
	if err != nil {
		return false, err
	}
	now, expired := s.now(), false
	for _, p := range others {
		if !now.After(p.Info.Timestamp.Add(timeout)) {
			continue
		}
		slog.WarnContext(ctx, "canceling an op pending on a version's index entry past the tag timeout",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.String("instance", key.Instance),
			slog.String("tag", p.Tag), slog.Time("since", p.Info.Timestamp))
		y := s.newIndexOp(rec, key, p.Tag)
		y.bilogFlags = x.bilogFlags
		c := y.completion(rgw.OpCancel, rgw.EntryVer{Pool: -1}, rgw.DirEntryMeta{Category: rgw.CategoryNone})
		err := y.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.BucketCompleteOp(w, c, s.release) })
		if err != nil && !errors.Is(err, radosclient.ErrInvalid) {
			return expired, op.FromRADOS(fmt.Errorf("canceling op %s on the index entry of %s: %w", p.Tag, versionName(rec, key), err), op.ScopeObject)
		}
		expired = true
	}
	return expired, nil
}

// indexTagTimeout is the tag timeout dir_suggest_changes takes for x's shard
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2215-L2227 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2385-L2397 at v20.2.4):
// the shard header's tag_timeout, read as radosgw reads a header, by a
// bucket_list of no entries, else rgw_pending_bucket_index_op_expiration,
// else CEPH_RGW_DEFAULT_TAG_TIMEOUT, a zero going down the list. The class
// reads the option from the OSD's configuration, and rgw-go from its own, an
// option of the rgw service as of the osd's.
func (s *Store) indexTagTimeout(ctx context.Context, x *indexOp) (time.Duration, error) {
	var h rgw.DirHeader
	err := x.readShard(ctx, func(sh indexShard) error {
		rop := radosclient.NewReadOp()
		res := rgw.GetDirHeader(rop, s.release)
		if _, rerr := sh.pool.Read(ctx, sh.oid, rop, radosclient.OpFlagNone); rerr != nil {
			return rerr
		}
		ret, rerr := res.Result()
		if rerr != nil {
			return fmt.Errorf("%w: the header of index shard %s: %w", op.ErrUnknown, sh.oid, rerr)
		}
		h = ret.Dir.Header
		return nil
	})
	if err != nil {
		return 0, op.FromRADOS(fmt.Errorf("reading the header of the index shard of %s: %w", x.obj.Key.Name, err), op.ScopeObject)
	}
	if h.TagTimeout != 0 {
		return time.Duration(h.TagTimeout) * time.Second, nil //nolint:gosec // a tag timeout in seconds fits a Duration
	}
	if s.conf != nil {
		if d, cerr := s.conf.Seconds("rgw_pending_bucket_index_op_expiration"); cerr == nil && d > 0 {
			return d, nil
		}
	}
	return pendingOpExpiryDefault, nil
}

// completeDelAsRead completes x's prepared DEL, stamped with mtime, the
// removed head's, in one op guarded as every index write is, with an
// OMAP_CMP that requires bi, an index entry as bi_get answered it, unchanged.
// An entry that changed since that read, or a prepare no longer pending,
// wraps errNotRemovable.
func (x *indexOp) completeDelAsRead(ctx context.Context, bi rgw.BIEntry, mtime time.Time) error {
	// complete_op cancels a completion at or below the entry's epoch on its
	// pool
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1080-L1087 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1215-L1222 at v20.2.4),
	// which the head's own epoch, the entry's, would be; pool -1 and epoch 0,
	// a cancel's, never are.
	c := x.completion(rgw.OpDel, rgw.EntryVer{Pool: -1}, rgw.DirEntryMeta{Category: rgw.CategoryNone, Mtime: mtime})
	err := x.guardedWrite(ctx, func(w *radosclient.WriteOp) {
		w.OmapCmp(bi.Idx, radosclient.CmpEQ, bi.Data)
		rgw.BucketCompleteOp(w, c, x.s.release)
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, radosclient.ErrCanceled), errors.Is(err, radosclient.ErrInvalid):
		// Not wrapped: the abort's round takes a wrapped ECANCELED for a
		// racing part and runs again.
		return fmt.Errorf("%w: its index entry changed since it was read", errNotRemovable)
	}
	return op.FromRADOS(err, op.ScopeObject)
}

// removableEntry checks en, a recorded version's instance entry as read
// after the abort's prepare under prepTag, for one the abort may remove: it
// carries no versioned epoch, has no op pending but the prepare's, an older
// one canceled already (expireStaleOps), and is the completion's entry,
// completed under writeTag, or none but the one the prepare made, which does
// not exist. Any other wraps errNotRemovable.
func removableEntry(en rgw.DirEntry, writeTag, prepTag string) error {
	switch {
	case en.VersionedEpoch != 0:
		return fmt.Errorf("%w: a link of it landed at epoch %d", errNotRemovable, en.VersionedEpoch)
	case len(en.PendingMap) != 1 || en.PendingMap[0].Tag != prepTag:
		return fmt.Errorf("%w: another op on its index entry is pending", errNotRemovable)
	case en.Exists && en.Tag != writeTag:
		return fmt.Errorf("%w: its index entry is another write's", errNotRemovable)
	}
	return nil
}

// removeUnindexedHead removes head, the head of key's version, whose index
// entry is gone, whole and guarded on tag, its write tag as the head stores
// it, and then does what removeHeadFlagged does after its removal: the head's
// tails to the GC and its size off the quota cache, also for a head already
// gone. A head another write changed since keeps its data and wraps
// errNotRemovable.
func (s *Store) removeUnindexedHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, head *op.ObjectState, tag []byte) error {
	ref, err := s.headRef(ctx, rec, key)
	if err != nil {
		return err
	}
	w := radosclient.NewWriteOp()
	w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, tag)
	rgw.ObjRemove(w, headKeptAttrs(key), s.release)
	_, err = ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrCanceled):
		// Not wrapped, as in removeEntryAsRead.
		return fmt.Errorf("%w: its head changed once its index entry was removed", errNotRemovable)
	case err != nil && !errors.Is(err, radosclient.ErrNotFound):
		return op.FromRADOS(err, op.ScopeObject)
	}
	if head.Manifest != nil {
		s.completeAtomicModification(ctx, rec, key, head)
	}
	accounted := head.Size
	if head.Compression != nil {
		accounted = head.Compression.OrigSize
	}
	if err := s.AdjustStats(ctx, rec, rec.Info.Owner, -1, 0, int64(accounted)); err != nil { //nolint:gosec // object sizes are far below 2^63
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// assertAbortLock asserts on the OSD that the abort still holds the
// RGWCompleteMultipart lock on up's meta object under cookie, assert_locked
// alone in a write op, as the meta object's removal asserts it
// (metaDelete.abort), so a hold that lapsed or another holder took over is
// ServiceUnavailable, with nothing removed. It leaves one round trip between
// the confirmation and the removal it guards, in which a gateway that took
// the lock over can link the version; the removal's compare of the version's
// index entry then refuses it (removeUnlinkedRecorded).
func (s *Store) assertAbortLock(ctx context.Context, up *op.Upload, ref mpRef, cookie string) error {
	w := radosclient.NewWriteOp()
	lock.AssertLocked(w, completeLockName, lock.TypeExclusive, cookie, "", s.release)
	if _, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone); err != nil {
		return fmt.Errorf("%w: the abort of upload %s no longer holds its lock: %w", op.ErrServiceUnavailable, up.ID, err)
	}
	return nil
}

// linkSettled reports whether no link of key, a version of rec's bucket, can
// still land: its name's OLH head carries no pending modification younger
// than rgw_olh_pending_timeout_sec (checkPendingOLHEntries), and its name's
// OLH log names no op on key's instance. A versioned write inits its
// modification, a pending attr on the OLH head, before it sends its link,
// and sends no link when that init fails
// (set_olh, https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8689-L8737 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9435-L9483 at v20.2.4;
// setOLH). setOLH keeps the attr after a link whose outcome is unknown, and
// only the replay of the link's log entry takes it away
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8505-L8507 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9251-L9253 at v20.2.4),
// or its expiry, which check_pending_olh_entries takes for the
// modification's failure
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680 at v20.2.4).
// bucket_link_olh writes its log entry and the version's instance and list
// entries in one class call, and answers ENOENT for a version whose instance
// entry is gone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688
// and https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1827 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829
// and https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1968 at v20.2.4).
// So a link rgw-go has in flight leaves its pending attr, and one that
// landed its list entry, which the caller reads after this, and its log
// entry until it is replayed. radosgw's set_olh cancels the attr after any
// failed link, a timeout included (set_olh above), so a radosgw link still
// in flight after a timeout is not seen here; the removal refuses one that
// lands before the version's instance entry is gone, and one after it fails
// with ENOENT (removeUnlinkedRecorded). An OLH log whose tag is not the
// head's is not decided.
func (s *Store) linkSettled(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (bool, error) {
	olh, err := s.readOLHHead(ctx, rec, olhHeadKey(key))
	switch {
	case err != nil:
		return false, err
	case !olh.Exists:
		return true, nil
	}
	if _, pending := s.checkPendingOLHEntries(ctx, olh); len(pending) > 0 {
		return false, nil
	}
	idx := s.newOLHIndex(rec, key)
	for marker := uint64(0); ; {
		ret, err := idx.readLog(ctx, olh, marker)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return true, nil
		case errors.Is(err, radosclient.ErrCanceled):
			return false, nil
		case err != nil:
			return false, err
		}
		for ver, entries := range ret.Log {
			for _, e := range entries {
				if e.Key.Instance == key.Instance {
					return false, nil
				}
			}
			marker = max(marker, ver)
		}
		if !ret.IsTruncated || len(ret.Log) == 0 {
			return true, nil
		}
	}
}

// errUploadStands marks the NoSuchUpload of an upload whose parts the key's
// object names, which a bucket delete skips without having aborted it.
var errUploadStands = errors.New("the upload's parts back the key's object")

// abortErr is abort's answer to a failed removal of the meta object: a meta
// object already gone is NoSuchUpload
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3262);
// ECANCELED stays as it is for the caller's retry.
func abortErr(up *op.Upload, err error) error {
	if errors.Is(err, radosclient.ErrNotFound) {
		return fmt.Errorf("%w: the meta object of upload %s is gone", op.ErrNoSuchUpload, up.ID)
	}
	return err
}

// abortMultipartsPage is abort_multiparts' max, the uploads one listing
// returns.
const abortMultipartsPage = 1000

// abortMultiparts is RadosBucket::abort_multiparts
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L958-L1013 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L978-L1033 at v20.2.4),
// which RadosBucket::remove runs for its own zonegroup's bucket after the
// emptiness check
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L395-L400 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L412-L417 at v20.2.4):
// the multipart namespace listed by meta object name, MultipartMetaFilter, in
// pages of 1000 without a prefix or a delimiter, each upload aborted through
// Abort. NoSuchUpload, which radosgw's -ENOENT and -ERR_NO_SUCH_UPLOAD both
// reach here as, is logged and skipped; an upload refused because its parts
// back the key's object is skipped with a warning, and counted, as radosgw
// counts the abort it makes of it; any other error is returned, and the
// bucket delete fails with it. aborted is num_deleted, which counts the
// skipped uploads too. Each upload is aborted once, where radosgw, whose
// list_multiparts appends each page to the one vector, aborts every earlier
// page's uploads again on each later page (docs/ceph-upstream-bugs.md,
// "radosgw's bucket delete aborts each page of multipart uploads again on
// every later page"), and under the RGWCompleteMultipart lock, which
// abort_multiparts does not take (docs/exclusions.md, "A bucket delete aborts
// its multipart uploads under their completion lock").
func (s *Store) abortMultiparts(ctx context.Context, rec *op.BucketRecord) (aborted int, err error) {
	var marker string
	for {
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: meta.NSMultipart, NameFilter: meta.IsMultipartMeta, Marker: marker, MaxKeys: abortMultipartsPage})
		if err != nil {
			return aborted, err
		}
		for i := range res.Entries {
			e := &res.Entries[i]
			key, id, ok := meta.ParseMultipartMeta(e.Key.Name)
			if !ok {
				continue
			}
			err := s.Abort(ctx, &op.Upload{ID: id, Bucket: rec, Key: meta.ObjKey{Name: key}})
			switch {
			case errors.Is(err, errUploadStands):
				// radosgw's abort frees such an upload's parts and counts it.
				slog.WarnContext(ctx, "not aborting a multipart upload whose parts back an object; its meta object and parts stay",
					slog.String("bucket", rec.Info.Bucket.Name), slog.String("meta", e.Key.Name), slog.Any("error", err))
			case errors.Is(err, op.ErrNoSuchUpload):
				slog.InfoContext(ctx, "multipart upload not found for cleanup on bucket delete",
					slog.String("bucket", rec.Info.Bucket.Name), slog.String("meta", e.Key.Name), slog.Any("error", err))
			case err != nil:
				return aborted, fmt.Errorf("aborting multipart upload %s of bucket %s: %w", e.Key.Name, rec.Info.Bucket.Name, err)
			}
			aborted++
		}
		if !res.Truncated {
			return aborted, nil
		}
		if res.NextMarker == marker {
			return aborted, fmt.Errorf("%w: listing the multipart uploads of bucket %s made no progress past %q", op.ErrInternalError, rec.Info.Bucket.Name, marker)
		}
		marker = res.NextMarker
	}
}
