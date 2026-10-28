package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// DeleteObject is RGWRados::Object::Delete::delete_obj
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5833-L6127 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6741 at v20.2.4).
// A bucket whose flags hold BUCKET_VERSIONED, its versioning enabled or
// suspended, takes the versioned branch (deleteVersioned): radosgw tests that
// flag alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5850 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6462 at v20.2.4).
//
// Any other takes the unversioned path: one head stat, the conditions, the
// index prepare DEL under a fresh tag, the head's guarded obj_remove under
// full-try, then complete_del, not awaited, the GC enqueue of the old tails,
// synchronous as send_split_chain is
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L68-L118),
// and the quota adjustment. The conditions are v20.2.4's on both releases,
// and the guard is v19.2.6's on both (docs/exclusions.md, "Delete conditions
// are Tentacle's, and a delete is guarded, on both releases"). A missing key
// is ErrNoSuchKey; a lost race is ErrConcurrentModification, which the op
// answers 204 as radosgw does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5302-L5304).
// That path refuses the OLH head of a versioned key (readHead), which a
// bucket holds only once its versioning was enabled; suspending versioning
// keeps BUCKET_VERSIONED set. A nonzero p.AssertEpoch goes first on its
// removal's op, librados's assert_version, so the OSD refuses the removal
// of a head written since the caller read it at that version
// (removeHeadFlagged).
//
// p.Expect is refused on that path before anything is read, and
// p.AssertEpoch on the versioned branch.
func (s *Store) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) (*op.DeleteObjectResult, error) {
	if p.Expect != nil && !rec.Info.Versioned() {
		return nil, fmt.Errorf("%w: a delete's expected state", op.ErrNotImplemented)
	}
	if rec.Info.Versioned() {
		if p.AssertEpoch != 0 {
			return nil, fmt.Errorf("%w: a versioned delete's asserted version", op.ErrNotImplemented)
		}
		return s.deleteVersioned(ctx, rec, key, p)
	}
	if key.Instance == meta.NullInstance {
		key.Instance = "" // https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5844-L5846
	}
	st, err := s.readHead(ctx, rec, key)
	if err != nil {
		return nil, err
	}
	if !st.Exists {
		return nil, fmt.Errorf("object %s: %w", key.Name, op.ErrNoSuchKey) // https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5983-L5992
	}
	w := radosclient.NewWriteOp()
	if p.AssertEpoch != 0 {
		w.AssertVersion(p.AssertEpoch)
	}
	if cerr := s.deleteConditions(w, st, p); cerr != nil {
		return nil, cerr
	}
	// prepare_atomic_modification(removal_op) appends only the guard
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6493-L6513,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6555-L6558);
	// a head without a tag, or with the one get_obj_state_impl fakes for a
	// head with a manifest, is not guarded.
	if st.WriteTag != "" {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	if err := s.removeHead(ctx, rec, key, "", st, w, true); err != nil {
		return nil, err
	}
	return &op.DeleteObjectResult{}, nil
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
	return s.removeHeadFlagged(ctx, rec, key, hashName, st, w, counted, headRemoval{})
}

// headRemoval is how removeHeadFlagged changes the index: with bilogFlags,
// the RGWBILogFlags delete_obj's caller gives it, and with the removal's
// completion awaited when await is set, or else sent without waiting.
type headRemoval struct {
	bilogFlags uint16
	await      bool
}

// headKeptAttrs is the attr prefixes a removal or a reset of key's head
// keeps: remove_rgw_head_obj's user.rgw.olh., the key's OLH, for the head of
// a key without an instance, which is its OLH head once the key is versioned
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5798-L5803 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6410-L6415 at v20.2.4),
// and none for a version's own head, which no OLH lives on: obj_remove keeps
// nothing then, where radosgw's keeps OLH attrs a copy of a null version left
// on it and the head stays behind as an empty version (docs/exclusions.md,
// "A version's head is removed whole").
func headKeptAttrs(key meta.ObjKey) []string {
	if key.Instance == "" || key.Instance == meta.NullInstance {
		return []string{meta.AttrOLHPrefix}
	}
	return nil
}

// removeHeadFlagged is removeHead whose index change goes as how says. An
// awaited completion that fails is logged and the removal goes on, as
// radosgw's handle_completion drops a failed one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1011-L1017 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1052-L1058 at v20.2.4).
func (s *Store) removeHeadFlagged(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, hashName string, st *op.ObjectState, w *radosclient.WriteOp, counted bool, how headRemoval) error {
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
	x.bilogFlags = how.bilogFlags
	if err = x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	rgw.ObjRemove(w, headKeptAttrs(key), s.release)
	epoch, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		// "rgw can't determine whether or not the delete succeeded": the
		// pending entry stays for a listing's check_disk_state
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5951-L5954).
		return op.FromRADOS(err, op.ScopeObject)
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		ver := rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}
		if !how.await {
			x.completeDel(ver, st.Mtime)
		} else if cerr := x.completeDelSync(ctx, ver, st.Mtime); cerr != nil {
			slog.WarnContext(ctx, "bucket index completion failed for a removed version; its index entry stays, counted in the bucket's stats, until an admin bucket check repairs them",
				slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name), slog.String("instance", key.Instance),
				slog.Int("op", int(rgw.OpDel)), slog.Any("error", cerr))
		}
		if st.Manifest != nil {
			s.completeAtomicModification(ctx, rec, key, st)
		}
	default:
		x.cancel() // https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5968-L5972
		if e := errno(err); versionAsserted(w) && (e == syscall.ERANGE || e == syscall.EOVERFLOW) {
			return fmt.Errorf("%w: the head of %s is not at the version asserted: %w", op.ErrConcurrentModification, key.Name, err)
		}
		return op.FromRADOS(err, op.ScopeObject)
	}
	if !counted {
		return nil
	}
	// radosgw adjusts only once complete_del has also succeeded (r is
	// overwritten by it,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5958-L5986);
	// rgw-go's completion is not awaited on every path, so the adjustment
	// follows the removal itself.
	accounted := st.Size
	if st.Compression != nil {
		accounted = st.Compression.OrigSize
	}
	if err := s.AdjustStats(ctx, rec, rec.Info.Owner, -1, 0, int64(accounted)); err != nil { //nolint:gosec // object sizes are far below 2^63
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// versionAsserted reports whether w asserts the head's version, whose
// mismatch the OSD answers ERANGE for a head written since and EOVERFLOW for
// one at a lower version, an absent head's 0 included
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6460-L6473 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6529-L6542 at v20.2.4).
func versionAsserted(w *radosclient.WriteOp) bool {
	return slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
		_, ok := st.(*radosclient.AssertVersionStep)
		return ok
	})
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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1066-L1072).
// A multipart part's, which a listing's sweep sends, joins no wait set
// (indexOp.unraced): no OLH links a namespaced entry, and the other index
// ops on a part's entry find its shard from the upload's name
// (indexOp.hashName), so the only OLH call that shares its name is one on an
// object so named, whose entries it never touches.
func (s *Store) deleteObjIndex(rec *op.BucketRecord, key meta.ObjKey, mtime time.Time) {
	x := &indexOp{
		s:       s,
		rec:     rec,
		obj:     meta.Obj{Bucket: rec.Info.Bucket, Key: key},
		blind:   rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless,
		unraced: key.NS == meta.NSMultipart,
	}
	x.completeDel(rgw.EntryVer{Pool: -1, Epoch: 0}, mtime)
}

// deleteConditions checks a delete's conditions against st and appends the
// checks the OSD repeats, in radosgw's order: x-amz-delete-if-unmodified-since
// first, on both releases
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5860-L5875 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6609-L6624 at v20.2.4),
// then v20.2.4's check_preconditions and its obj_check_mtime for
// x-amz-if-match-last-modified-time (v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6663-L6671,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329).
// Times compare in whole seconds: high_precision_time is set only for a system
// request (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5283),
// which serves multisite.
func (s *Store) deleteConditions(w *radosclient.WriteOp, st *op.ObjectState, p op.DeleteParams) error {
	if !p.UnmodifiedSince.IsZero() {
		if st.Mtime.Truncate(time.Second).After(p.UnmodifiedSince.Truncate(time.Second)) {
			return op.ErrPreconditionFailed
		}
		rgw.ObjCheckMtime(w, p.UnmodifiedSince, rgw.MtimeLE, false, s.release)
	}
	if err := s.checkDeletePreconditions(st, p); err != nil {
		return err
	}
	if !p.IfMatchLastModified.IsZero() {
		rgw.ObjCheckMtime(w, p.IfMatchLastModified, rgw.MtimeEQ, false, s.release)
	}
	return nil
}

// checkDeletePreconditions is check_preconditions as a delete calls it, on
// st: x-amz-if-match-size, then x-amz-if-match-last-modified-time in whole
// seconds, then If-Match
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6646-L6715 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329 at v20.2.4).
func (s *Store) checkDeletePreconditions(st *op.ObjectState, p op.DeleteParams) error {
	if p.IfMatchSize != nil && *p.IfMatchSize != st.Size {
		return op.ErrPreconditionFailed
	}
	if !p.IfMatchLastModified.IsZero() && !st.Mtime.Truncate(time.Second).Equal(p.IfMatchLastModified.Truncate(time.Second)) {
		return op.ErrPreconditionFailed
	}
	return s.checkPreconditions(st, p.IfMatch, "")
}
