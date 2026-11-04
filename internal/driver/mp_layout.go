package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A multipart upload is RadosMultipartUpload (driver/rados/rgw_sal_rados.cc).

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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.cc#L412-L430 at v19.2.6
// and v20.2.4), as dataPool is for one without: the bucket's explicit
// data-extra pool, or its explicit data pool when it names none
// (rgw_data_placement_target:: get_data_extra_pool,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_pool_types.h#L146-L151);
// else the data-extra pool of the rule's placement, or that placement's
// STANDARD data pool when it names none
// (RGWZonePlacementInfo::get_data_extra_pool,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone_types.h#L274-L280);
// else the same of the zonegroup's default placement (each the same at both
// tags). The storage class plays no part, so a COLD upload's meta object
// sits beside a STANDARD one's. ok is false when the zone has neither
// placement.
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
// RadosMultipartUpload::get_meta_obj with in_extra_data set
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3265-L3268,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3288-L3290):
// "<key>.<uploadID>.meta" in the multipart namespace, placed by the bucket's
// placement rule, with openRef's errors; a pool that does not exist wraps
// radosclient.ErrNotFound, which op.FromRADOS makes NoSuchUpload. An upload
// id holding a "." is NoSuchUpload, as the ops answer it.
func (s *Store) metaRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (mpRef, error) {
	if strings.Contains(uploadID, ".") {
		// RGWMPObj::init would name another key's upload with it.
		return mpRef{}, fmt.Errorf("%w: upload id %q holds a dot", op.ErrNoSuchUpload, uploadID)
	}
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

// metaReadSize is the buffer readMeta reads a meta object into. The object
// holds one encoded multipart_upload_info
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1517-L1525 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1563-L1574 at v20.2.4),
// whose fields other than its destination placement rule take at most 61
// bytes on Squid and 65 on Tentacle, which adds the checksum's type and
// flags, with the object lock mode, at most 10 bytes, and legal hold status,
// at most 3, that S3 allows. The rule is a placement's name and a storage
// class's, which the zonegroup's configuration names
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_placement_types.h#L80-L84 at v19.2.7
// and v20.2.4): 4 KiB leaves them about 4000 bytes, far past the names
// zonegroups use. radosgw bounds neither name's length, so a longer meta
// object can exist, and readMeta reads it again.
const metaReadSize = 4 << 10

// readMeta is RadosMultipartUpload::get_info with the attrs
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3646-L3721;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4492-L4578):
// the meta object's prefetching stat, raw_obj_stat's one op of
// cls_version_read, getxattrs, stat2 and a read at 0
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8827-L8870),
// whose bytes decode as the multipart_upload_info. A missing object or an
// empty one is NoSuchUpload
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3673-L3676,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3700-L3702);
// an info that does not decode is radosgw's -EIO
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3706-L3711),
// which its error table lacks, so UnknownError. radosgw reads an empty
// head's first byte with a second op, which reads nothing; rgw-go answers
// from the first.
//
// raw_obj_stat reads rgw_max_chunk_size into a buffer of that size, where
// rgw-go reads metaReadSize into a buffer of its own, which charges the
// memory budget nothing, as no other metadata a request reads does: so
// ListParts, AbortMultipartUpload and CompleteMultipartUpload, which carry
// no data, do not wait for the budget. An object the stat finds longer than
// that, which only placement and storage-class names together past about
// 4000 bytes make, is read again by the same op, up to
// rgw_max_chunk_size as raw_obj_stat reads it, into a buffer that waits for
// the budget as a transfer's first draw does (docs/differences.md, "Object
// data buffers share one memory budget, sized from the Go memory limit").
func (s *Store) readMeta(ctx context.Context, ref mpRef) (*metaState, error) {
	st, data, err := s.readMetaOp(ctx, ref, metaReadSize)
	if err == nil && st.size > uint64(len(data)) {
		n := min(st.size, s.readCfg.chunk)
		mem := s.mem.newStage()
		share, perr := mem.piece(ctx, n)
		defer mem.give(share)
		if perr != nil {
			return nil, perr
		}
		st, data, err = s.readMetaOp(ctx, ref, n)
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: the meta object %s is empty", op.ErrNoSuchUpload, ref.oid)
	}
	d := denc.NewDecoder(data)
	st.info = meta.DecodeMultipartUploadInfo(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding the multipart upload info of %s: %w", op.ErrUnknown, ref.oid, err)
	}
	return st, nil
}

// readMetaOp is readMeta's op, its read of n bytes at 0 into a buffer of
// its own, which it returns with the bytes read.
func (s *Store) readMetaOp(ctx context.Context, ref mpRef, n uint64) (*metaState, []byte, error) {
	rop := radosclient.NewReadOp()
	objv := version.Read(rop, s.release)
	xattrs := rop.GetXattrs()
	stat := rop.Stat()
	rd := rop.ReadInto(0, make([]byte, n))
	if _, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone); err != nil {
		return nil, nil, op.FromRADOS(err, op.ScopeUpload)
	}
	st := &metaState{ref: ref, size: stat.Size, mtime: stat.ModTime, attrs: filterAttrs(xattrs.Xattrs)}
	// cls_version_read's completion leaves the version alone when its reply
	// does not decode
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/version/cls_version_client.cc#L66-L77 at v19.2.6
	// and v20.2.4).
	if v, verr := objv.Version(); verr == nil {
		st.version = v
	}
	return st, rd.Data[:rd.N], nil
}

// uploadFromMeta is the upload a read of its meta object describes: the
// owner its ACL names, which ListParts renders; its mtime as the time the
// upload was initiated; the destination placement it stores; its attrs. The
// owner is decoded alone, as entryOwner decodes it; radosgw's ListParts
// decodes the whole policy and answers -EIO for one that does not decode
// (docs/differences.md, "ListParts names the owner of an ACL whose grants do
// not decode").
func uploadFromMeta(rec *op.BucketRecord, key meta.ObjKey, uploadID string, st *metaState) *op.Upload {
	ownerID, display := entryOwner(st.attrs)
	return &op.Upload{
		ID: uploadID, Bucket: rec, Key: key, Owner: meta.ParseOwner(ownerID), OwnerName: display,
		Initiated: st.mtime, Placement: st.info.DestPlacement, Attrs: st.attrs,
	}
}
