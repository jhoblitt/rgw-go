package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// DeleteObject is the unversioned path of RGWRados::Object::Delete::delete_obj
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5845-L5986 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6584-L6735 at v20.2.4;
// a bare line number below is v19.2.6's): one head stat, the conditions, the
// index prepare DEL under a fresh tag, the head's guarded obj_remove under
// full-try, then complete_del, not awaited, the GC enqueue of the old tails,
// synchronous as send_split_chain is
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L68-L118),
// and the quota adjustment. The conditions are v20.2.4's on both releases, and
// the guard is v19.2.6's on both (docs/exclusions.md, "Delete conditions are
// Tentacle's, and a delete is guarded, on both releases"). A missing key is
// ErrNoSuchKey; a lost race is ErrConcurrentModification, which the op answers
// 204 as radosgw does (rgw_op.cc:5302-5304).
//
// A bucket whose versioning is or was enabled or that has object lock is
// answered NotImplemented before the store is touched, so that no caller
// takes this path there: radosgw takes the versioned one, which writes a
// delete marker for a key with no instance (:5774-5812), where this one
// would remove the object outright.
func (s *Store) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
	if f := rec.Info.Flags; f&(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled) != 0 {
		return fmt.Errorf("%w: deleting from bucket %s, which is versioned or has object lock",
			op.ErrNotImplemented, rec.Info.Bucket.Name)
	}
	if key.Instance == "null" {
		key.Instance = "" // https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5768-L5770
	}
	st, err := s.readHead(ctx, rec, key, false)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("object %s: %w", key.Name, op.ErrNoSuchKey) // https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5903-L5912
	}
	w := radosclient.NewWriteOp()
	if cerr := s.deleteConditions(w, st, p); cerr != nil {
		return cerr
	}
	// prepare_atomic_modification(removal_op) appends only the guard
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6493-L6513,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6555-L6558);
	// a head without a tag, or with the one get_obj_state_impl fakes for a
	// head with a manifest, is not guarded.
	if st.WriteTag != "" {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	return s.removeHead(ctx, rec, key, "", st, w, true)
}

// removeHead is what delete_obj's unversioned path does once its checks are
// in w
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5917-L5986;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6673-L6740):
// the index prepare DEL under a fresh tag on the shard hashName names, or
// the key's own when it is empty, the head's obj_remove after w's checks
// under full-try, then complete_del, not awaited, the GC enqueue of the old
// tails and, when counted, the quota adjustment; a removal that failed
// otherwise than by timing out cancels the prepare. st is the head as read.
// The removal runs to its end whatever the client does.
func (s *Store) removeHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, hashName string, st *op.ObjectState, w *radosclient.WriteOp, counted bool) error {
	ref, err := s.headRef(ctx, rec, key)
	if err != nil {
		return err
	}
	// A remove abandoned to a canceled context could land after its index
	// change was canceled, leaving an entry no listing checks again.
	ctx = context.WithoutCancel(ctx)
	// The removal never sets state->write_tag, so the prepare takes
	// UpdateIndex::prepare's random tag
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7087-L7093).
	x := s.newIndexOp(rec, key, "")
	x.hashName = hashName
	if err = x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release) // remove_rgw_head_obj, https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5722-L5727
	epoch, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		// "rgw can't determine whether or not the delete succeeded": the
		// pending entry stays for a listing's check_disk_state
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5951-L5954).
		return op.FromRADOS(err, op.ScopeObject)
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		x.completeDel(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, st.Mtime)
		if st.Manifest != nil {
			s.completeAtomicModification(ctx, rec, key, st)
		}
	default:
		x.cancel() // https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5968-L5972
		return op.FromRADOS(err, op.ScopeObject)
	}
	if !counted {
		return nil
	}
	// radosgw adjusts only once complete_del has also succeeded (r is
	// overwritten by it,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5958-L5986);
	// rgw-go's completion is not awaited, so the adjustment follows the
	// removal itself.
	accounted := st.Size
	if st.Compression != nil {
		accounted = st.Compression.OrigSize
	}
	if err := s.AdjustStats(ctx, rec, rec.Info.Owner, -1, 0, int64(accounted)); err != nil { //nolint:gosec // object sizes are far below 2^63
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// deleteObjIndex is RGWRados::delete_obj_index
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6033-L6050;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6787-L6804):
// complete_del on an UpdateIndex that was never prepared, so with an empty
// tag, pool -1 and epoch 0, on the shard the key's own name hashes to, and
// not awaited. The index op is built here because newIndexOp substitutes a
// random tag for an empty one, as UpdateIndex::prepare does, and cls_rgw
// refuses a tag the entry's pending map does not hold
// (cls_rgw.cc:1066-1072).
func (s *Store) deleteObjIndex(rec *op.BucketRecord, key meta.ObjKey, mtime time.Time) {
	x := &indexOp{
		s:     s,
		rec:   rec,
		obj:   meta.Obj{Bucket: rec.Info.Bucket, Key: key},
		blind: rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless,
	}
	x.completeDel(rgw.EntryVer{Pool: -1, Epoch: 0}, mtime)
}

// deleteConditions checks a delete's conditions against st and appends the
// checks the OSD repeats, in radosgw's order: x-amz-delete-if-unmodified-since
// first, on both releases (:5860-5875; v20.2.4 :6609-6624), then v20.2.4's
// check_preconditions and its obj_check_mtime for
// x-amz-if-match-last-modified-time (v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6663-L6671,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329).
// Times compare in whole seconds: high_precision_time is set only for a system
// request (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5283),
// which serves multisite.
func (s *Store) deleteConditions(w *radosclient.WriteOp, st *op.ObjectState, p op.DeleteParams) error {
	mtime := st.Mtime.Truncate(time.Second)
	if !p.UnmodifiedSince.IsZero() {
		if mtime.After(p.UnmodifiedSince.Truncate(time.Second)) {
			return op.ErrPreconditionFailed
		}
		rgw.ObjCheckMtime(w, p.UnmodifiedSince, rgw.MtimeLE, false, s.release)
	}
	if p.IfMatchSize != nil && *p.IfMatchSize != st.Size {
		return op.ErrPreconditionFailed
	}
	if !p.IfMatchLastModified.IsZero() {
		if !mtime.Equal(p.IfMatchLastModified.Truncate(time.Second)) {
			return op.ErrPreconditionFailed
		}
		rgw.ObjCheckMtime(w, p.IfMatchLastModified, rgw.MtimeEQ, false, s.release)
	}
	return s.checkPreconditions(st, p.IfMatch, "")
}
