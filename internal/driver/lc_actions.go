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
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// lcStep is a point in an action at which lcPass.hook runs.
type lcStep int

const (
	// lcStepDecided follows Decide's choice of a removal, before the re-read.
	lcStepDecided lcStep = iota
	// lcStepReread follows the re-read and its checks of the object, before
	// the bucket's record is read again and the object removed.
	lcStepReread
	// lcStepUpload precedes the abort of an expired multipart upload.
	lcStepUpload
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
// entry alone, as the work pool's function logs it. Once a removal has found
// the bucket changed, the entries still queued are dropped unread.
func (b *lcBucket) entry(ctx context.Context, rec *op.BucketRecord, rule *lifecycle.Op, e op.ObjectEntry, le lifecycle.Entry, st lifecycle.State) {
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
	case d.Action == lifecycle.ActionExpiration && d.Effect == lifecycle.EffectRemove && !e.DeleteMarker:
		b.expireCurrent(ctx, rec, probe, d.Key, e)
	default:
		slog.WarnContext(ctx, "lifecycle takes no such action on a bucket never versioned", slog.String("bucket", b.c.entry.Bucket),
			slog.String("key", e.Key.Name), slog.Int("action", int(d.Action)), slog.Int("effect", int(d.Effect)))
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
//   - the bucket's record is read again (current), and a bucket changed
//     since the page's record is skipped and ends the pass once the rule's
//     pool drains (noteChange), as is one whose record could not be read,
//     which is logged as that read's failure;
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
	cur, err := b.current(ctx)
	switch {
	case errors.Is(err, errLCChanged), errors.Is(err, errLCStaleEntry):
		slog.WarnContext(ctx, "lifecycle skips an expired object of a bucket changed during its pass", append(attrs, slog.Any("error", err))...)
	case err != nil:
		slog.WarnContext(ctx, "lifecycle could not read the bucket's record again; the expired object stays", append(attrs, slog.Any("error", err))...)
	}
	if err != nil {
		b.noteChange(err)
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
