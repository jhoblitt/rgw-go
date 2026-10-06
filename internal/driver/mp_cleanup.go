package driver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// What a completion or an abort retires, as RadosMultipartUpload's
// complete, cleanup_orphaned_parts, cleanup_part_history and abort do
// (driver/rados/rgw_sal_rados.cc:3038-3263 and :3433-3640 at v19.2.6,
// :3882-4107 and :4279-4490 at v20.2.4; a bare line number below is
// v19.2.6's).

// completeLockName is the cls_lock lock RGWCompleteMultipart takes on an
// upload's meta object (rgw_op.cc:6434 at v19.2.6, :7248 at v20.2.4).
const completeLockName = "RGWCompleteMultipart"

// mpOptions are the multipart tunables, read once at Open. The defaults in
// the comments are rgw.yaml.in's, identical at v19.2.6 and v20.2.4.
type mpOptions struct {
	// lockMaxTime is rgw_mp_lock_max_time, 10 min, an int of seconds that
	// RGWCompleteMultipart narrows to an int and utime_t to a u32
	// (rgw_op.cc:6430-6432 at v19.2.6).
	lockMaxTime time.Duration
	minPartSize uint64 // rgw_multipart_min_part_size, 5 MiB
}

// readMPOptions reads the multipart options through conf, failing on the
// first option librados does not know or whose value does not parse.
func readMPOptions(conf *cephconf.Options) (mpOptions, error) {
	var r reads
	secs := readOption(&r, conf.Int64, "rgw_mp_lock_max_time")
	o := mpOptions{minPartSize: readOption(&r, conf.Size, "rgw_multipart_min_part_size")}
	if r.err != nil {
		return mpOptions{}, fmt.Errorf("reading the multipart options: %w", r.err)
	}
	o.lockMaxTime = time.Duration(uint32(secs)) * time.Second //nolint:gosec // narrowed as utime_t's seconds narrow it
	return o, nil
}

// lockMeta is MPRadosSerializer::try_lock (:3750-3760; v20.2.4
// :4613-4624): one write op on the meta object, assert_exists then an
// exclusive lock under cookie for rgw_mp_lock_max_time. radosgw's serializer
// sends cookie "", as a completion does. The seam's error comes back for
// the caller's mapping: ENOENT for a missing meta object, EBUSY for another
// holder, EEXIST for this client's own hold under the same cookie.
func (s *Store) lockMeta(ctx context.Context, ref mpRef, cookie string) error {
	w := radosclient.NewWriteOp()
	lock.LockExisting(w, completeLockName, cookie, "", s.mp.lockMaxTime, s.release)
	_, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone)
	return err
}

// The completion record, the attrs ceph/ceph#72103 (tracker #80896, in
// review after v19.2.6 and v20.2.4) writes on the meta object before the
// head write: the ID tag the completion's head carries, NUL included, and
// the version instance of that head, "null" for the key's own head. A
// later completion or abort that finds them checks whether that head still
// carries the tag before it touches the parts (docs/exclusions.md, "A
// CompleteMultipartUpload records itself on the meta object before its head
// write").
const (
	attrMPCompletionTag      = meta.AttrPrefix + "mp_completion_tag"
	attrMPCompletionInstance = meta.AttrPrefix + "mp_completion_instance"
	// nullInstance names the key's own head, read without following an olh.
	nullInstance = "null"
)

// renewAndRecord renews this client's lock for another
// rgw_mp_lock_max_time, failing unless it still holds it, and in the same
// op writes the completion record for a head written under tag:
// assert_exists, lock_exclusive with LOCK_FLAG_MUST_RENEW, as radosgw's
// renewal sends it since tracker #75375's fix (ceph/ceph#67696, after
// v19.2.6 and v20.2.4), and the record's two setxattrs.
func (s *Store) renewAndRecord(ctx context.Context, ref mpRef, tag string) error {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	lock.Lock(w, lock.LockOp{Name: completeLockName, Type: lock.TypeExclusive, Duration: s.mp.lockMaxTime, Flags: lock.FlagMustRenew}, s.release)
	w.SetXattr(attrMPCompletionTag, []byte(tag+"\x00"))
	w.SetXattr(attrMPCompletionInstance, []byte(nullInstance))
	_, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone)
	return err
}

// clearRecord removes the completion record after a head write that
// certainly did not land, so a retry may still complete the upload. A
// failure is logged: the record then stays, and a retry is refused rather
// than completed.
func (s *Store) clearRecord(ctx context.Context, ref mpRef) {
	ctx = context.WithoutCancel(ctx)
	w := radosclient.NewWriteOp()
	w.AssertExists()
	w.RmXattr(attrMPCompletionTag)
	w.RmXattr(attrMPCompletionInstance)
	if _, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone); err != nil {
		slog.WarnContext(ctx, "failed to remove the completion record; a retry of the upload will be refused",
			slog.String("oid", ref.oid), slog.Any("error", err))
	}
}

// unlockMeta is the serializer's unlock (Lock::unlock) of this client's
// hold under cookie, which RGWCompleteMultipart::complete sends whenever the
// lock was not released with the meta object (rgw_op.cc:6582-6593 at
// v19.2.6, :7506-7517 at v20.2.4). It runs to its end whatever the client
// does, and a failure is logged; a lock no longer held answers ENOENT, which
// is not.
func (s *Store) unlockMeta(ctx context.Context, ref mpRef, cookie string) {
	ctx = context.WithoutCancel(ctx)
	w := radosclient.NewWriteOp()
	lock.Unlock(w, completeLockName, cookie, s.release)
	if _, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "failed to unlock the multipart meta object", slog.String("oid", ref.oid), slog.Any("error", err))
	}
}

// listPartsChunk is the page complete, cleanup_orphaned_parts and abort list
// the parts by (:3046, :3177, :3456).
const listPartsChunk = 1000

// listAllParts drains listPartsPage in pages of listPartsChunk. A page that
// is truncated holds exactly listPartsChunk parts, so stored[i] is on page
// i / listPartsChunk.
func (s *Store) listAllParts(ctx context.Context, ref mpRef, uploadID string) ([]meta.UploadPartInfo, error) {
	var all []meta.UploadPartInfo
	for marker, more := 0, true; more; {
		page, next, truncated, err := s.listPartsPage(ctx, ref, uploadID, marker, listPartsChunk)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		marker, more = next, truncated && len(page) > 0
	}
	return all, nil
}

// partCleanup accumulates what a completion or an abort retires: the index
// entries the closing class call removes, the GC chain of the retired
// objects, and the prefixes already handled for each part number,
// processed_prefixes.
type partCleanup struct {
	removeObjs []rgw.ObjKey
	chain      []rgw.GCObj
	prefixes   map[uint32]map[string]bool
}

// seen reports that prefix was handled for part already, marking it handled.
func (c *partCleanup) seen(part uint32, prefix string) bool {
	if c.prefixes == nil {
		c.prefixes = map[uint32]map[string]bool{}
	}
	if c.prefixes[part] == nil {
		c.prefixes[part] = map[string]bool{}
	}
	if c.prefixes[part][prefix] {
		return true
	}
	c.prefixes[part][prefix] = true
	return false
}

// stripesOf is every stripe of the part manifest m under prefix, as GC
// chain entries: the pool as rgw_pool::to_str renders it, the oid and the
// locator, as cleanup_part_history pushes them (:3123-3130), and the index
// key of its first stripe, the part head. A rule's override prefix is
// dropped, so only prefix's objects are named: radosgw writes none on a
// part, and one would name another upload of the part.
func (s *Store) stripesOf(m meta.Manifest, prefix string) (chain []rgw.GCObj, headKey string, err error) {
	m.Prefix = prefix
	m.Rules = maps.Clone(m.Rules)
	for k, r := range m.Rules {
		r.OverridePrefix = ""
		m.Rules[k] = r
	}
	stripes, err := m.Stripes()
	if err != nil {
		return nil, "", fmt.Errorf("%w: walking the part manifest under %s: %w", op.ErrUnknown, prefix, err)
	}
	for i := range stripes {
		st := &stripes[i]
		pool, _ := s.dataPool(st.Placement, st.Obj.Bucket)
		chain = append(chain, rgw.GCObj{Pool: pool.String(), Key: rgw.ObjKey{Name: st.OID()}, Loc: st.Locator()})
	}
	if len(stripes) > 0 {
		headKey = stripes[0].Obj.Key.IndexKeyName()
	}
	return chain, headKey, nil
}

// retirePart is cleanup_part_history (:3103-3148; v20.2.4 :3947-3993) for
// one part: each past prefix not yet handled has its part head's entry,
// "<prefix>.<n>" in the multipart namespace, removed from the index, and
// every stripe of the part's manifest under it joins the chain. radosgw
// sends each part's chain to the GC on its own; the caller sends the
// whole chain once.
func (s *Store) retirePart(p meta.UploadPartInfo, c *partCleanup) error {
	for _, pp := range p.PastPrefixes {
		if c.seen(p.Num, pp) {
			continue
		}
		c.removeObjs = append(c.removeObjs, rgw.ObjKey{Name: meta.ObjKey{Name: meta.MultipartPartName(pp, p.Num), NS: meta.NSMultipart}.IndexKeyName()})
		chain, _, err := s.stripesOf(p.Manifest, pp)
		if err != nil {
			return err
		}
		c.chain = append(c.chain, chain...)
	}
	return nil
}

// retireCurrentPart adds what cleanup_orphaned_parts and abort retire of a
// part's current upload (:3061-3078, :3194-3214): every stripe of its
// manifest, the part head included, as update_gc_chain with the meta
// object as the head walks it, and the part head's index entry. A part
// without a manifest or a prefix, or whose prefix was handled, adds
// nothing.
func (s *Store) retireCurrentPart(p meta.UploadPartInfo, c *partCleanup) error {
	prefix := p.Manifest.Prefix
	if partManifestEmpty(p.Manifest) || prefix == "" || c.seen(p.Num, prefix) {
		return nil
	}
	chain, headKey, err := s.stripesOf(p.Manifest, prefix)
	if err != nil {
		return err
	}
	c.chain = append(c.chain, chain...)
	if headKey != "" {
		c.removeObjs = append(c.removeObjs, rgw.ObjKey{Name: headKey})
	}
	return nil
}

// partManifestEmpty is RGWObjManifest::empty
// (driver/rados/rgw_obj_manifest.h:384-388 at v19.2.6 and v20.2.4).
func partManifestEmpty(m meta.Manifest) bool {
	if m.ExplicitObjs {
		return len(m.Objs) == 0
	}
	return len(m.Rules) == 0
}

// uploadPrefixes is every prefix the parts of upload uploadID of key were
// written under: the upload's own, and each part's current and past ones.
func uploadPrefixes(key, uploadID string, parts []meta.UploadPartInfo) map[string]bool {
	out := map[string]bool{meta.MultipartPrefix(key, uploadID): true}
	for i := range parts {
		if p := parts[i].Manifest.Prefix; p != "" {
			out[p] = true
		}
		for _, pp := range parts[i].PastPrefixes {
			out[pp] = true
		}
	}
	return out
}

// manifestPrefixes is every prefix the manifest m names objects under: each
// rule's, or for an explicit manifest each piece's name up to its last dot,
// as "<prefix>.<part>" and "<prefix>.<part>_<stripe>" name a part's head and
// stripes. An object completed from an upload names its parts so.
func manifestPrefixes(m *meta.Manifest) map[string]bool {
	out := map[string]bool{}
	if m == nil {
		return out
	}
	if m.ExplicitObjs {
		for ofs := range m.Objs {
			name := m.Objs[ofs].Loc.Key.Name
			if i := strings.LastIndexByte(name, '.'); i > 0 {
				out[name[:i]] = true
			}
		}
		return out
	}
	for _, r := range m.Rules {
		out[cmp.Or(r.OverridePrefix, m.Prefix)] = true
	}
	return out
}

// namesUploadObjects reports that the manifest m names an object written
// under one of prefixes. No other writer uses a prefix of the form
// "<key>.<upload id or 32 random characters>".
func namesUploadObjects(m *meta.Manifest, prefixes map[string]bool) bool {
	for p := range manifestPrefixes(m) {
		if prefixes[p] {
			return true
		}
	}
	return false
}

// retainedCleanup is what finishing an upload whose object head already
// stands retires: of each part's current and past uploads, one whose
// prefix the head names keeps its objects and loses only its index entry;
// any other goes to the GC with its entry, as an orphan or a part's
// history does. A prefix is matched whatever the part number, so a part
// that shares the upload's prefix with the head's parts is kept and leaks
// rather than risk the head's data.
func (s *Store) retainedCleanup(parts []meta.UploadPartInfo, named map[string]bool) (partCleanup, error) {
	var c partCleanup
	for i := range parts {
		p := &parts[i]
		for _, prefix := range append([]string{p.Manifest.Prefix}, p.PastPrefixes...) {
			if prefix != "" && named[prefix] && !c.seen(p.Num, prefix) {
				key := meta.ObjKey{Name: meta.MultipartPartName(prefix, p.Num), NS: meta.NSMultipart}
				c.removeObjs = append(c.removeObjs, rgw.ObjKey{Name: key.IndexKeyName()})
			}
		}
		if err := s.retireCurrentPart(*p, &c); err != nil {
			return partCleanup{}, err
		}
		if err := s.retirePart(*p, &c); err != nil {
			return partCleanup{}, err
		}
	}
	return c, nil
}

// metaDelete is one removal of an upload's meta object, as
// RGWRados::Object::Delete::delete_obj makes it for the multipart callers.
type metaDelete struct {
	rec *op.BucketRecord
	// key is the upload's key, whose shard holds the meta object's entry.
	key     meta.ObjKey
	ref     mpRef
	version version.ObjVersion // as read; 0 sends no check
	mtime   time.Time          // the meta object's, for complete_del
	// removeObjs are the entries the completion or cancel retires with the
	// meta object's.
	removeObjs []rgw.ObjKey
	// accounted is what the quota cache drops with the object: the meta
	// object's own size for a completion, the parts' total for an abort.
	accounted uint64
	// abort makes the removal an abort's, which must leave the upload whole
	// whenever it does not remove it: the op first asserts the
	// RGWCompleteMultipart lock this client holds under cookie, so the OSD
	// fails it with EBUSY, however late it runs, once that hold lapsed or
	// was broken, whoever holds the lock since: a cls_lock locker is the
	// request's entity and its cookie (cls_lock.cc:174-178 and :507-519 at
	// v19.2.6 and v20.2.4), so a completion of this gateway, the same
	// entity under cookie "", does not pass for an abort's hold under its
	// own cookie; a cancel retires none of removeObjs, where
	// delete_obj's cancel retires them (:5969) from an upload that stands;
	// and a meta object already gone comes back as radosclient.ErrNotFound
	// without the quota cache change, as delete_obj returns -ENOENT before
	// it (:5979-5984).
	abort bool
	// cookie is the abort's lock cookie, which the assertion names.
	cookie string
	// deadline bounds the wait for the removal's reply; zero waits as long as
	// the op takes. A removal past it is answered as one that timed out.
	deadline time.Duration
}

// deleteMeta is Delete::delete_obj on the meta object (rgw_rados.cc:5757-5987;
// v20.2.4 :6445-6739) as delete_object(FLAG_PREVENT_VERSIONING) reaches it,
// so without a bucket index log entry (rgw_sal_rados.cc:2924-2945): the
// index prepare DEL under a random tag on the shard of the upload's key,
// obj_remove keeping the olh attrs, cls_version_check EQ when a version was
// read (RGWObjVersionTracker::version_for_check, rgw_common.h:950-955 at
// v19.2.6), under pool_full_try. A removal that worked, or found no object,
// completes the DEL with d.removeObjs and drops one object and d.accounted
// bytes from the quota cache; ETIMEDOUT leaves the pending entry for a
// listing to reconcile and answers ErrRequestTimedOut, as does a reply not in
// by d.deadline; anything else cancels
// with d.removeObjs, as radosgw does (:5969), or without them for an abort
// (metaDelete.abort). ECANCELED, a part registered since the version was
// read, comes back as radosclient.ErrCanceled for the caller's retry.
func (s *Store) deleteMeta(ctx context.Context, d metaDelete) error {
	ctx = context.WithoutCancel(ctx)
	x := s.newIndexOp(d.rec, d.ref.key, "")
	x.hashName = d.key.Name
	x.noLog = true
	x.removeObjs = d.removeObjs
	if err := x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeUpload)
	}
	w := radosclient.NewWriteOp()
	if d.abort {
		// First: past obj_remove the op's object has no lock to assert.
		lock.AssertLocked(w, completeLockName, lock.TypeExclusive, d.cookie, "", s.release)
	}
	rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release)
	if d.version.Ver != 0 {
		version.Check(w, d.version, version.CondEQ, s.release)
	}
	wctx := ctx
	if d.deadline > 0 {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(ctx, d.deadline)
		defer cancel()
	}
	epoch, err := d.ref.pool.Write(wctx, d.ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut), errors.Is(err, context.DeadlineExceeded):
		return op.FromRADOS(err, op.ScopeUpload)
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		x.completeDel(rgw.EntryVer{Pool: d.ref.pool.ID(), Epoch: epoch}, d.mtime)
		if err != nil && d.abort {
			return err
		}
		if aerr := s.AdjustStats(ctx, d.rec, d.rec.Info.Owner, -1, 0, int64(d.accounted)); aerr != nil { //nolint:gosec // object sizes are far below 2^63
			slog.WarnContext(ctx, "quota cache adjustment failed", slog.String("bucket", d.rec.Info.Bucket.Name), slog.Any("error", aerr))
		}
		return nil
	default:
		if d.abort {
			x.removeObjs = nil
		}
		x.cancel()
		if errors.Is(err, radosclient.ErrCanceled) {
			return err
		}
		return op.FromRADOS(err, op.ScopeUpload)
	}
}
