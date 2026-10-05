package driver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A multipart upload is RadosMultipartUpload (driver/rados/rgw_sal_rados.cc;
// a bare line number below is v19.2.6's, with v20.2.4's where it differs).

// mpRef locates one object of an upload in RADOS: the meta object in the
// bucket placement's data-extra pool, or a part head or stripe in its tail
// pool, as an objRef, whose handle carries the locator when there is one,
// and the object's key. A key in the multipart namespace has no locator
// (rgw_obj_key::get_loc), so loc stays empty for every name radosgw writes.
type mpRef struct {
	objRef
	key meta.ObjKey
}

// extraPool is rgw_get_obj_data_pool for an object with in_extra_data set
// (driver/rados/rgw_obj_manifest.cc:412-430 at v19.2.6 and v20.2.4), as
// dataPool is for one without: the bucket's explicit data-extra pool, or its
// explicit data pool when it names none (rgw_data_placement_target::
// get_data_extra_pool, rgw_pool_types.h:146-151); else the data-extra pool
// of the rule's placement, or that placement's STANDARD data pool when it
// names none (RGWZonePlacementInfo::get_data_extra_pool,
// rgw_zone_types.h:274-280); else the same of the zonegroup's default
// placement (each the same at both tags). The storage class plays no part,
// so a COLD upload's meta object sits beside a STANDARD one's. ok is false
// when the zone has neither placement.
func (s *Store) extraPool(rule meta.PlacementRule, bucket meta.BucketID) (pool meta.Pool, ok bool) {
	if ep := bucket.ExplicitPlacement; ep.DataPool.Name != "" {
		if ep.DataExtraPool.Name == "" {
			return ep.DataPool, true
		}
		return ep.DataExtraPool, true
	}
	if rule != (meta.PlacementRule{}) {
		if pi, found := s.zone.Params.PlacementPools[rule.Name]; found {
			return placementExtraPool(pi), true
		}
	}
	pi, found := s.zone.Params.PlacementPools[s.zone.ZoneGroup.DefaultPlacement.Name]
	if !found {
		return meta.Pool{}, false
	}
	return placementExtraPool(pi), true
}

// placementExtraPool is RGWZonePlacementInfo::get_data_extra_pool.
func placementExtraPool(pi meta.ZonePlacementInfo) meta.Pool {
	if pi.DataExtraPool.Name != "" {
		return pi.DataExtraPool
	}
	return deref(pi.StorageClasses[meta.StorageClassStandard].DataPool, meta.Pool{})
}

// metaRef is the meta object of upload uploadID of key in rec's bucket,
// RadosMultipartUpload::get_meta_obj with in_extra_data set (:3265-3268,
// :3288-3290): "<key>.<uploadID>.meta" in the multipart namespace, placed by
// the bucket's placement rule, with openRef's errors; a pool that does not
// exist wraps radosclient.ErrNotFound, which op.FromRADOS makes
// NoSuchUpload.
func (s *Store) metaRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (mpRef, error) {
	rule, bucket := rec.Info.PlacementRule, rec.Info.Bucket
	pool, ok := s.extraPool(rule, bucket)
	mk := meta.ObjKey{Name: meta.MultipartMetaName(key.Name, uploadID), NS: meta.NSMultipart}
	ref, err := s.openRef(ctx, resolvedPool{pool: pool, ok: ok, kind: "data-extra pool", rule: rule}, meta.Obj{Bucket: bucket, Key: mk}, op.ScopeUpload)
	if err != nil {
		return mpRef{}, err
	}
	return mpRef{objRef: ref, key: mk}, nil
}

// metaState is one read of an upload's meta object.
type metaState struct {
	ref     mpRef
	size    uint64
	mtime   time.Time
	attrs   map[string][]byte  // the user.rgw. xattrs, as rgw_filter_attrset leaves them
	version version.ObjVersion // its cls_version; zero when it has none
	info    meta.MultipartUploadInfo
}

// readMeta is RadosMultipartUpload::get_info with the attrs (:3642-3717;
// v20.2.4 :4492-4578): the meta object's prefetching stat, raw_obj_stat's
// one op of cls_version_read, getxattrs, stat2 and a read of
// rgw_max_chunk_size at 0 (driver/rados/rgw_rados.cc:8827-8870), whose bytes
// decode as the multipart_upload_info. A missing object or an empty one is
// NoSuchUpload (:3673-3676, :3700-3702); an info that does not decode is
// radosgw's -EIO (:3706-3711), which its error table lacks, so UnknownError.
// radosgw reads an empty head's first byte with a second op, which reads
// nothing; rgw-go answers from the first.
func (s *Store) readMeta(ctx context.Context, ref mpRef) (*metaState, error) {
	rop := radosclient.NewReadOp()
	objv := version.Read(rop, s.release)
	xattrs := rop.GetXattrs()
	stat := rop.Stat()
	buf := s.headBuf()
	rd := rop.ReadInto(0, *buf)
	_, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// An op abandoned to its context may still fill the buffer, so only
		// one that ended goes back to the pool.
		defer s.headBufs.Put(buf)
	}
	if err != nil {
		return nil, op.FromRADOS(err, op.ScopeUpload)
	}
	st := &metaState{ref: ref, size: stat.Size, mtime: stat.ModTime, attrs: filterAttrs(xattrs.Xattrs)}
	// cls_version_read's completion leaves the version alone when its reply
	// does not decode (cls_version_client.cc:66-77 at v19.2.6 and v20.2.4).
	if v, verr := objv.Version(); verr == nil {
		st.version = v
	}
	if rd.N == 0 {
		return nil, fmt.Errorf("%w: the meta object %s is empty", op.ErrNoSuchUpload, ref.oid)
	}
	d := denc.NewDecoder(rd.Data[:rd.N])
	st.info = meta.DecodeMultipartUploadInfo(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding the multipart upload info of %s: %w", op.ErrUnknown, ref.oid, err)
	}
	return st, nil
}

// uploadFromMeta is the upload a read of its meta object describes: the
// owner its ACL names, which ListParts renders; its mtime as the time the
// upload was initiated; the destination placement it stores; its attrs.
func uploadFromMeta(rec *op.BucketRecord, key meta.ObjKey, uploadID string, st *metaState) *op.Upload {
	ownerID, display := entryOwner(st.attrs)
	return &op.Upload{
		ID: uploadID, Bucket: rec, Key: key, Owner: meta.ParseOwner(ownerID), OwnerName: display,
		Initiated: st.mtime, Placement: st.info.DestPlacement, Attrs: st.attrs,
	}
}
