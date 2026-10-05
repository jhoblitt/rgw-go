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
// (driver/rados/rgw_rados.cc:5845-5986 at v19.2.6, :6584-6735 at v20.2.4; a
// bare line number below is v19.2.6's): one head stat, the conditions, the
// index prepare DEL under a fresh tag, the head's guarded obj_remove under
// full-try, then complete_del, not awaited, the GC enqueue of the old tails,
// synchronous as send_split_chain is (rgw_gc.cc:68-118), and the quota
// adjustment. The conditions are v20.2.4's on both
// releases, and the guard is v19.2.6's on both (docs/exclusions.md, "Delete
// conditions are Tentacle's, and a delete is guarded, on both releases").
// A missing key is ErrNoSuchKey; a lost race is ErrConcurrentModification,
// which the op answers 204 as radosgw does (rgw_op.cc:5302-5304).
func (s *Store) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
	if key.Instance == "null" {
		key.Instance = "" // :5768-5770
	}
	st, err := s.readHead(ctx, rec, key, false)
	if err != nil {
		return err
	}
	if !st.Exists {
		return fmt.Errorf("object %s: %w", key.Name, op.ErrNoSuchKey) // :5903-5912
	}
	ref, err := s.headRef(ctx, rec, key)
	if err != nil {
		return err
	}
	w := radosclient.NewWriteOp()
	if cerr := s.deleteConditions(w, st, p); cerr != nil {
		return cerr
	}
	// prepare_atomic_modification(removal_op) appends only the guard
	// (:6493-6513, :6555-6558); a head without a tag, or with the one
	// get_obj_state_impl fakes for a head with a manifest, is not guarded.
	if st.WriteTag != "" {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	// radosgw finishes a delete whatever its client does, and a remove
	// abandoned to a canceled context could land after its index change was
	// canceled, leaving an entry no listing checks again.
	ctx = context.WithoutCancel(ctx)
	// The removal never sets state->write_tag, so the prepare takes
	// UpdateIndex::prepare's random tag (:7087-7093).
	x := s.newIndexOp(rec, key, "")
	if err = x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release) // remove_rgw_head_obj, :5722-5727
	epoch, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		// "rgw can't determine whether or not the delete succeeded": the
		// pending entry stays for a listing's check_disk_state (:5951-5954).
		return op.FromRADOS(err, op.ScopeObject)
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		x.completeDel(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, st.Mtime)
		if st.Manifest != nil {
			s.completeAtomicModification(ctx, rec, key, st)
		}
	default:
		x.cancel() // :5968-5972
		return op.FromRADOS(err, op.ScopeObject)
	}
	accounted := st.Size
	if st.Compression != nil {
		accounted = st.Compression.OrigSize
	}
	if err := s.AdjustStats(ctx, rec, rec.Info.Owner, -1, 0, int64(accounted)); err != nil { //nolint:gosec // object sizes are far below 2^63
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// deleteConditions checks a delete's conditions against st and appends the
// checks the OSD repeats, in radosgw's order: x-amz-delete-if-unmodified-since
// first, on both releases (:5860-5875; v20.2.4 :6609-6624), then v20.2.4's
// check_preconditions and its obj_check_mtime for
// x-amz-if-match-last-modified-time (v20.2.4 :6663-6671, :7260-7329). Times
// compare in whole seconds: high_precision_time is set only for a system
// request (rgw_op.cc:5283), which serves multisite.
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
