package driver

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// lcStep is a point in an action at which lcPass.hook runs.
type lcStep int

const (
	// lcStepDecided follows Decide's choice of a removal or a delete marker,
	// before the re-read.
	lcStepDecided lcStep = iota
	// lcStepReread follows the re-read and its checks of the object, before
	// the bucket's record is read again and the object removed or marked.
	lcStepReread
	// lcStepUpload precedes the abort of an expired multipart upload.
	lcStepUpload
	// lcStepListed precedes the decision of each entry a rule's walk hands
	// out, named by its key as listed.
	lcStepListed
)

func (p *lcPass) at(ctx context.Context, step lcStep, key meta.ObjKey) {
	if p.hook != nil {
		p.hook(ctx, step, key)
	}
}

// entry is LCOpRule::process for e, a listed entry of rule's walk over rec,
// with le its lifecycle form and st the lister's state
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1617-L1677 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1673-L1733 at v20.2.4):
// Decide, then the action it selected. A failure is logged and ends the
// entry alone, as the work pool's function logs it. Once an action has found
// the bucket changed, the entries still queued are dropped unread.
func (b *lcBucket) entry(ctx context.Context, rec *op.BucketRecord, rule *lifecycle.Op, e op.ObjectEntry, le lifecycle.Entry, st lifecycle.State) {
	b.p.at(ctx, lcStepListed, e.Key)
	if b.change() != nil {
		return
	}
	now := b.w.now()
	probe := &lcProbe{s: b.p.s, rec: rec, now: now}
	clock := lifecycle.Clock{Now: now, Debug: b.p.s.opts.lcDebugInterval, Loc: b.w.loc}
	d, err := lifecycle.Decide(ctx, rule, &rec.Info, le, st, clock, probe)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle skipped an object it could not decide", slog.String("bucket", b.c.entry.Bucket),
			slog.String("key", e.Key.Name), slog.String("rule", rule.ID), slog.Any("error", err))
		return
	}
	switch {
	case d.Effect == lifecycle.EffectNone:
	case !b.versioned && d.Action == lifecycle.ActionExpiration && d.Effect == lifecycle.EffectRemove && !e.DeleteMarker:
		b.expireCurrent(ctx, rec, probe, d.Key, e)
	case b.versioned && (d.Effect == lifecycle.EffectMarker || d.Effect == lifecycle.EffectNullMarker):
		b.markCurrent(ctx, rec, probe, e, d.Effect == lifecycle.EffectNullMarker)
	case b.versioned && d.Effect == lifecycle.EffectRemove && e.DeleteMarker && d.Action != lifecycle.ActionNoncurrentExpiration:
		b.removeMarker(ctx, rec, d.Key, e)
	default:
		slog.WarnContext(ctx, "lifecycle takes no such action", slog.String("bucket", b.c.entry.Bucket),
			slog.String("key", e.Key.Name), slog.String("instance", e.Key.Instance), slog.Bool("versioned", b.versioned),
			slog.Int("action", int(d.Action)), slog.Int("effect", int(d.Effect)))
	}
}

// expireCurrent removes key, the current object of rec, a bucket never
// versioned, as remove_expired_obj does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L601-L658 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L608-L679 at v20.2.4),
// once the object and the bucket are still the ones Decide saw:
//   - the key is read again, and an object gone, no longer at e's mtime and
//     ETag, or at another RADOS version than probe read for Decide, is
//     skipped, so a write since the decision, one that keeps the mtime and
//     the ETag included, is decided again at the next pass;
//   - the bucket's record is read again (recheck);
//   - on a bucket with object lock as that record has it, a retention or
//     legal hold on what the read found keeps the object (lcLockCheck), as
//     pass_object_lock_check keeps a noncurrent version;
//   - the removal carries e's mtime as UnmodifiedSince, which
//     remove_expired_obj sends, and IfMatch and IfMatchLastModified, and
//     asserts the version the read returned, so a write to the head since
//     the read, an attr write that leaves the ETag and the second of the
//     mtime as they were included, fails it and the object is decided again
//     at the next pass. A failed condition or assert, or a missing object,
//     is a skip.
//
// Then the notification of radosgw's ObjectExpirationCurrent and
// LifecycleExpirationDelete. radosgw sends only UnmodifiedSince, compared in
// whole seconds, and checks no lock on a current object
// (docs/exclusions.md, "Lifecycle removes an object only as it was
// listed" and "Every permanent removal by lifecycle runs the object-lock
// check").
func (b *lcBucket) expireCurrent(ctx context.Context, rec *op.BucketRecord, probe *lcProbe, key meta.ObjKey, e op.ObjectEntry) {
	s := b.p.s
	attrs := []any{slog.String("bucket", b.c.entry.Bucket), slog.String("key", key.Name)}
	b.p.at(ctx, lcStepDecided, key)
	st, err := s.StatObject(ctx, rec, key)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read an expired object again; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	if !st.Exists || !st.Mtime.Equal(e.Mtime) || st.ETag != e.ETag {
		slog.DebugContext(ctx, "lifecycle skips an object changed since its listing", attrs...)
		return
	}
	if epoch, read := probe.epoch(); read && epoch != st.Epoch {
		slog.DebugContext(ctx, "lifecycle skips an object written since its decision", attrs...)
		return
	}
	b.p.at(ctx, lcStepReread, key)
	cur := b.recheck(ctx, attrs)
	if cur == nil {
		return
	}
	if cur.Info.ObjLockEnabled() {
		if lerr := lcLockCheck(ctx, st, b.w.now()); lerr != nil {
			slog.DebugContext(ctx, "lifecycle keeps a locked object", append(attrs, slog.Any("error", lerr))...)
			return
		}
	}
	_, err = s.DeleteObject(ctx, cur, key, op.DeleteParams{
		UnmodifiedSince: e.Mtime, IfMatch: e.ETag, IfMatchLastModified: e.Mtime, AssertEpoch: st.Epoch,
		MarkerOwner: e.Owner, MarkerOwnerDisplayName: e.OwnerDisplayName,
	})
	switch {
	case errors.Is(err, op.ErrPreconditionFailed), errors.Is(err, op.ErrConcurrentModification), errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "lifecycle skips an object written since its read", append(attrs, slog.Any("error", err))...)
		return
	case err != nil:
		slog.WarnContext(ctx, "lifecycle could not remove an expired object", append(attrs, slog.Any("error", err))...)
		return
	}
	slog.InfoContext(ctx, "lifecycle removed an expired object", attrs...)
	b.p.notifier.Notify(ctx, lifecycle.Notification{
		Bucket: cur.Info.Bucket, Key: key, ETag: st.ETag, Size: st.Size, VersionID: e.Key.Instance,
		Events: []lifecycle.Event{lifecycle.EventExpirationCurrent, lifecycle.EventExpirationDelete},
	})
}

// recheck is an action's read of the bucket's record again (current), just
// before its delete, returning the record to delete with. A bucket changed
// since the page's record, or gone, skips the action and ends the pass once
// the rule's pool drains (noteChange), as does one whose record could not be
// read, which is logged as that read's failure; recheck then returns nil.
func (b *lcBucket) recheck(ctx context.Context, attrs []any) *op.BucketRecord {
	cur, err := b.current(ctx)
	switch {
	case err == nil:
		return cur
	case errors.Is(err, errLCChanged), errors.Is(err, errLCStaleEntry):
		slog.WarnContext(ctx, "lifecycle skips an expired object of a bucket changed during its pass", append(attrs, slog.Any("error", err))...)
	default:
		slog.WarnContext(ctx, "lifecycle could not read the bucket's record again; the expired object stays", append(attrs, slog.Any("error", err))...)
	}
	b.noteChange(err)
	return nil
}

// The events of the expirations of delete markers, named as to_string names
// them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_notify_event_type.cc#L31-L56 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_notify_event_type.cc#L31-L56 at v20.2.4).
const (
	lcEventExpirationDeleteMarker        lifecycle.Event = "s3:ObjectLifecycle:Expiration:DeleteMarker"
	lcEventExpirationDeleteMarkerCreated lifecycle.Event = "s3:LifecycleExpiration:DeleteMarkerCreated"
)

// markCurrent is the current expiration of e, the current version of rec, a
// bucket that was ever versioned: a delete marker over e's name, owned by e's
// owner as remove_expired_obj makes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L601-L658 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L608-L679 at v20.2.4),
// which keeps e as a noncurrent version, or with null, on a suspended
// bucket, the null delete marker, which replaces the null version wherever
// it sits, current or not
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875 at v20.2.4)
// and keeps any other. It writes the marker once e and the bucket are still
// the ones Decide saw:
//   - the name is read again through its OLH, and a current version that is
//     not e's instance, still as listed (listedAs), or is at another RADOS
//     version than probe read for Decide, is skipped, so a version written
//     since the listing is never hidden;
//   - for the null delete marker the key's OLH head is read too, which holds
//     the null version's data when there is one, and is e when e is the null
//     version;
//   - the bucket's record is read again (recheck);
//   - on a bucket with object lock as that record has it, a retention or
//     legal hold on that null version keeps it, and no marker is written
//     (lcLockCheck); a marker over any other version removes nothing and is
//     written;
//   - the marker carries e's ETag as IfMatch, and for a version that is not
//     the null version e's mtime as IfMatchLastModified, which DeleteObject
//     checks on the version current when it links, atomically on an enabled
//     bucket; it reads the null version's mtime from the key's OLH head, which
//     drifts from it (listedAs). On an enabled bucket the marker carries e's
//     mtime as UnmodifiedSince too, as remove_expired_obj sends it, which the
//     class compares with no new marker's entry
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1690-L1701 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1831-L1842 at v20.2.4),
//     and the version as read again as Expect, which the marker's condition
//     requires current, by its instance and write tag, when the marker links:
//     a version a PUT wrote since, with e's body and an mtime IfMatch and
//     IfMatchLastModified would pass, or none for the null version, keeps the
//     marker off, as does an attr write since.
//     The null delete marker carries the OLH head as read as Expect, whose
//     write tag, the null version's, or the one its OLH's first init gave a
//     key without one, its init checks on the OLH head and its replay before
//     it removes the null version's data, so a null write since refuses it;
//     and no UnmodifiedSince: the class compares that with the null
//     version's own entry and, finding it no older, would write nothing. A
//     failed condition or Expect, or a version gone, is a skip.
//
// Then the notification of radosgw's ObjectExpirationCurrent and
// LifecycleExpirationDelete (docs/exclusions.md, "Lifecycle removes an object
// only as it was listed" and "Lifecycle expiration on a versioning-suspended
// bucket writes the null delete marker").
func (b *lcBucket) markCurrent(ctx context.Context, rec *op.BucketRecord, probe *lcProbe, e op.ObjectEntry, null bool) {
	s := b.p.s
	key := meta.ObjKey{Name: e.Key.Name}
	attrs := []any{slog.String("bucket", b.c.entry.Bucket), slog.String("key", key.Name), slog.String("instance", e.Key.Instance)}
	b.p.at(ctx, lcStepDecided, key)
	st, err := s.StatObject(ctx, rec, key)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read an expired object again; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	same, err := b.listedAs(ctx, rec, &e, st)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read an expired object again; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	if st.Key.Instance != e.Key.Instance || !same {
		slog.DebugContext(ctx, "lifecycle skips an object changed since its listing", attrs...)
		return
	}
	if epoch, read := probe.epoch(); read && epoch != st.Epoch {
		slog.DebugContext(ctx, "lifecycle skips an object written since its decision", attrs...)
		return
	}
	head := st
	if null && e.Key.Instance != "" {
		if head, err = s.readOLHHead(ctx, rec, key); err != nil {
			slog.WarnContext(ctx, "lifecycle could not read the null version the null delete marker replaces; the object stays",
				append(attrs, slog.Any("error", err))...)
			return
		}
	}
	b.p.at(ctx, lcStepReread, key)
	cur := b.recheck(ctx, attrs)
	if cur == nil {
		return
	}
	if null && head.Exists && cur.Info.ObjLockEnabled() {
		if lerr := lcLockCheck(ctx, head, b.w.now()); lerr != nil {
			slog.DebugContext(ctx, "lifecycle keeps a locked null version and writes no null delete marker", append(attrs, slog.Any("error", lerr))...)
			return
		}
	}
	p := op.DeleteParams{IfMatch: e.ETag, MarkerOwner: e.Owner, MarkerOwnerDisplayName: e.OwnerDisplayName, Expect: st}
	if e.Key.Instance != "" {
		p.IfMatchLastModified = e.Mtime
	}
	if null {
		p.Expect = head
	} else {
		p.UnmodifiedSince = e.Mtime
	}
	_, err = s.DeleteObject(ctx, cur, key, p)
	switch {
	case errors.Is(err, op.ErrPreconditionFailed), errors.Is(err, op.ErrConcurrentModification), errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "lifecycle skips an object written since its read", append(attrs, slog.Any("error", err))...)
		return
	case err != nil:
		slog.WarnContext(ctx, "lifecycle could not write a delete marker over an expired object", append(attrs, slog.Any("error", err))...)
		return
	}
	slog.InfoContext(ctx, "lifecycle wrote a delete marker over an expired object", attrs...)
	b.p.notifier.Notify(ctx, lifecycle.Notification{
		Bucket: cur.Info.Bucket, Key: key, ETag: st.ETag, Size: st.Size, VersionID: e.Key.Instance,
		Events: []lifecycle.Event{lifecycle.EventExpirationCurrent, lifecycle.EventExpirationDelete},
	})
}

// listedAs reports whether st, a version that is no delete marker read again
// by its key or through its OLH, is still e, the version a listing gave, at
// e's mtime and ETag. The null version's data is the key's OLH head, whose
// RADOS mtime moves with any write to it that sets none, as the removal of a
// modification's pending attr does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7754-L7797 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8417-L8460 at v20.2.4;
// docs/ceph-upstream-bugs.md, "radosgw's removal of an OLH head's pending
// attr moves the null version's mtime"), so the null version is judged by
// its instance entry, read again, at e's mtime, ETag and versioned_epoch,
// and its head by the ETag.
func (b *lcBucket) listedAs(ctx context.Context, rec *op.BucketRecord, e *op.ObjectEntry, st *op.ObjectState) (bool, error) {
	if !st.Exists || st.ETag != e.ETag {
		return false, nil
	}
	if e.Key.Instance != "" {
		return st.Mtime.Equal(e.Mtime), nil
	}
	en, err := b.p.s.nullDataEntry(ctx, rec, meta.ObjKey{Name: e.Key.Name})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return en.Meta.Mtime.Equal(e.Mtime) && en.Meta.ETag == e.ETag && en.VersionedEpoch == e.VersionedEpoch, nil
}

// removeMarker removes key, the delete marker e lists of rec, a bucket that
// was ever versioned, by its instance, null for an empty one, as
// remove_expired_obj removes one for the current expiration of a marker no
// entry of its name follows and for the delete-marker expiration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1172-L1186 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1287-L1307 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1192-L1206 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1309-L1329 at v20.2.4),
// once it is still the marker listed:
//   - the marker is read again by its version, and one gone, or no longer a
//     marker at e's versioned_epoch, which tells a marker from any other, is
//     skipped. e's mtime is not compared: a listing reports a marker whose
//     index entry carries a pending op with its OLH head's state
//     (checkDiskState, internal/driver/list.go), and a null delete marker
//     takes the pending op of the null entry it replaces, which the
//     writer's completion then clears from the null data key alone, so such
//     a marker would never match its listing (docs/ceph-upstream-bugs.md,
//     "radosgw's listing repair of a null version's list entry overwrites or
//     removes the key's version marker");
//   - the bucket's record is read again (recheck);
//   - the removal carries the marker's mtime as read again as
//     IfMatchLastModified, which DeleteObject checks on the marker's index
//     entry, e's mtime as UnmodifiedSince, as remove_expired_obj sends it,
//     which the removal of a named version does not read
//     (docs/ceph-upstream-bugs.md, "radosgw's versioned delete of a named
//     version ignores unmod_since, which its lifecycle relies on"), and the
//     marker as read again as Expect, which the unlink checks again, by its
//     versioned_epoch and mtime. A failed condition or Expect, or a marker
//     gone, is a skip.
//
// A marker holds no lock. Then the notification of radosgw's
// ObjectExpirationDeleteMarker and LifecycleExpirationDeleteMarkerCreated.
func (b *lcBucket) removeMarker(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, e op.ObjectEntry) {
	s := b.p.s
	attrs := []any{slog.String("bucket", b.c.entry.Bucket), slog.String("key", key.Name), slog.String("instance", key.Instance)}
	b.p.at(ctx, lcStepDecided, key)
	st, err := s.StatObject(ctx, rec, key)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle could not read an expired delete marker again; it stays", append(attrs, slog.Any("error", err))...)
		return
	}
	if !st.DeleteMarker || st.VersionedEpoch != e.VersionedEpoch {
		slog.DebugContext(ctx, "lifecycle skips a delete marker changed since its listing", attrs...)
		return
	}
	b.p.at(ctx, lcStepReread, key)
	cur := b.recheck(ctx, attrs)
	if cur == nil {
		return
	}
	_, err = s.DeleteObject(ctx, cur, key, op.DeleteParams{UnmodifiedSince: e.Mtime, IfMatchLastModified: st.Mtime, Expect: st})
	switch {
	case errors.Is(err, op.ErrPreconditionFailed), errors.Is(err, op.ErrConcurrentModification), errors.Is(err, op.ErrNoSuchKey):
		slog.DebugContext(ctx, "lifecycle skips a delete marker changed since its read", append(attrs, slog.Any("error", err))...)
		return
	case err != nil:
		slog.WarnContext(ctx, "lifecycle could not remove an expired delete marker", append(attrs, slog.Any("error", err))...)
		return
	}
	slog.InfoContext(ctx, "lifecycle removed an expired delete marker", attrs...)
	b.p.notifier.Notify(ctx, lifecycle.Notification{
		Bucket: cur.Info.Bucket, Key: key, VersionID: e.Key.Instance,
		Events: []lifecycle.Event{lcEventExpirationDeleteMarker, lcEventExpirationDeleteMarkerCreated},
	})
}

// abortUpload is handle_multipart_expiration's work on e, the listed meta
// object of an upload of rec, under rule: an upload whose meta mtime is rule's
// DaysAfterInitiation old, as obj_has_expired counts it, is aborted as
// AbortMultipartUpload aborts one, through GetUpload and Abort, so under its
// RGWCompleteMultipart lock, which radosgw's worker does not take
// (docs/exclusions.md, "Lifecycle aborts an expired multipart upload under
// its completion lock"). An upload already gone is NoSuchUpload, logged as
// radosgw logs ERR_NO_SUCH_UPLOAD, and so is one whose parts back its
// object, which Abort leaves (docs/exclusions.md, "AbortMultipartUpload
// leaves a completed upload's parts alone"). Then the notification of
// radosgw's ObjectExpirationAbortMPU.
func (b *lcBucket) abortUpload(ctx context.Context, rec *op.BucketRecord, rule *lifecycle.Op, e op.ObjectEntry) {
	s := b.p.s
	if !lifecycle.Expired(e.Mtime, rule.MPExpiration, b.w.now(), s.opts.lcDebugInterval, b.w.loc) {
		return
	}
	name, id, ok := meta.ParseMultipartMeta(e.Key.Name)
	attrs := []any{slog.String("bucket", b.c.entry.Bucket), slog.String("meta", e.Key.Name)}
	if !ok || name == "" || id == "" {
		slog.DebugContext(ctx, "lifecycle skips a multipart meta object that names no upload", attrs...)
		return
	}
	key := meta.ObjKey{Name: name}
	b.p.at(ctx, lcStepUpload, key)
	up, err := s.GetUpload(ctx, rec, key, id)
	if err == nil {
		err = s.Abort(ctx, up)
	}
	switch {
	case errors.Is(err, op.ErrNoSuchUpload):
		slog.InfoContext(ctx, "lifecycle found no multipart upload to abort", append(attrs, slog.Any("error", err))...)
		return
	case err != nil:
		slog.WarnContext(ctx, "lifecycle could not abort an expired multipart upload", append(attrs, slog.Any("error", err))...)
		return
	}
	slog.InfoContext(ctx, "lifecycle aborted an expired multipart upload", attrs...)
	b.p.notifier.Notify(ctx, lifecycle.Notification{
		Bucket: rec.Info.Bucket, Key: e.Key, ETag: e.ETag, Size: e.Size, VersionID: e.Key.Instance,
		Events: []lifecycle.Event{lifecycle.EventExpirationAbortMPU},
	})
}

// lcLockCheck is pass_object_lock_check on st, a version that exists
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L304-L349 at v19.2.7 and v20.2.4):
// objectlock.VerifyDelete at now with neither bypass. A lock attr that does
// not decode keeps the version too, and is logged, as radosgw logs it.
func lcLockCheck(ctx context.Context, st *op.ObjectState, now time.Time) error {
	err := objectlock.VerifyDelete(st.Attrs, now, false, false)
	if err != nil && !errors.Is(err, objectlock.ErrLocked) {
		slog.WarnContext(ctx, "lifecycle keeps a version whose lock attr does not decode",
			slog.String("key", st.Key.Name), slog.String("instance", st.Key.Instance), slog.Any("error", err))
	}
	return err
}

// lcProbe is Decide's Probe for one listed entry of rec: one StatObject of
// the version, made when Decide first asks and kept for its other
// questions, at now.
type lcProbe struct {
	s    *Store
	rec  *op.BucketRecord
	now  time.Time
	read bool
	key  meta.ObjKey
	st   *op.ObjectState
	err  error
}

// epoch is the RADOS version of the version probe read, and whether it read
// one: 0 for a version that did not exist.
func (p *lcProbe) epoch() (uint64, bool) {
	if !p.read || p.st == nil {
		return 0, false
	}
	return p.st.Epoch, true
}

func (p *lcProbe) stat(ctx context.Context, key meta.ObjKey) (*op.ObjectState, error) {
	if !p.read || p.key != key {
		p.read, p.key = true, key
		p.st, p.err = p.s.StatObject(ctx, p.rec, key)
	}
	return p.st, p.err
}

// Size implements lifecycle.Probe: the size uncompressed, OrigSize for a
// compressed object, whose Size is the size stored.
func (p *lcProbe) Size(ctx context.Context, key meta.ObjKey) (uint64, error) {
	st, err := p.stat(ctx, key)
	switch {
	case err != nil:
		return 0, err
	case !st.Exists:
		return 0, nil
	case st.Compression != nil:
		return st.Compression.OrigSize, nil
	}
	return st.Size, nil
}

// Tags implements lifecycle.Probe.
func (p *lcProbe) Tags(ctx context.Context, key meta.ObjKey) ([]byte, error) {
	st, err := p.stat(ctx, key)
	if err != nil || !st.Exists {
		return nil, err
	}
	return st.Attrs[tags.Attr], nil
}

// VerifyDelete implements lifecycle.Probe. A read that fails keeps the
// version, as pass_object_lock_check's failed prepare does, and is logged.
func (p *lcProbe) VerifyDelete(ctx context.Context, key meta.ObjKey) error {
	st, err := p.stat(ctx, key)
	if err != nil {
		slog.WarnContext(ctx, "lifecycle keeps a version whose lock it could not read",
			slog.String("key", key.Name), slog.String("instance", key.Instance), slog.Any("error", err))
		return err
	}
	if !st.Exists {
		return nil
	}
	return lcLockCheck(ctx, st, p.now)
}
