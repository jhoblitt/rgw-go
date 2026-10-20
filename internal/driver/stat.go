package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// StatObject implements op.ObjectStore.
func (s *Store) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.readHead(ctx, rec, key, false)
}

// PrefetchObject implements op.ObjectStore.
func (s *Store) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.readHead(ctx, rec, key, true)
}

// readHead is RGWRados::raw_obj_stat
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8827-L8870 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9771-L9814 at v20.2.4)
// followed by the decoding get_obj_state_impl does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6120-L6297 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6874-L7051 at v20.2.4):
// one RADOS op that reads the head's version, every xattr, the stat and,
// with prefetch, its first rgw_max_chunk_size bytes.
func (s *Store) readHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error) {
	ref, err := s.headRef(ctx, rec, key)
	if errors.Is(err, radosclient.ErrNotFound) {
		// radosgw's stat creates a missing data pool, opening it with create
		// set (rgw_get_rados_ref,
		// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L107-L108 at v19.2.6,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_tools.cc#L108-L109 at v20.2.4),
		// and then finds no object in it. rgw-go creates no pool
		// (docs/exclusions.md).
		return &op.ObjectState{Bucket: rec, Key: key}, nil
	}
	if err != nil {
		return nil, err
	}
	rop := radosclient.NewReadOp()
	objv := version.Read(rop, s.release)
	xattrs := rop.GetXattrs()
	stat := rop.Stat()
	var (
		buf *[]byte
		rd  *radosclient.ReadResult
	)
	if prefetch {
		buf = s.headBuf()
		rd = rop.ReadInto(0, *buf)
	}
	ver, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone)
	if buf != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// An op abandoned to its context may still fill the buffer, so only
		// one that ended goes back to the pool.
		defer s.headBufs.Put(buf)
	}
	st := &op.ObjectState{Bucket: rec, Key: key}
	if errors.Is(err, radosclient.ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return nil, op.FromRADOS(err, op.ScopeObject)
	}
	st.Exists = true
	st.Epoch = ver
	// cls_version_read's completion leaves the version alone when its reply
	// does not decode
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/version/cls_version_client.cc#L66-L77 at v19.2.6
	// and v20.2.4).
	if v, verr := objv.Version(); verr == nil {
		st.Version = meta.ObjVersion(v)
	}
	st.Size, st.Mtime = stat.Size, stat.ModTime
	st.Attrs = filterAttrs(xattrs.Xattrs)
	if rd != nil {
		st.Head = slices.Clone(rd.Data[:rd.N])
	}
	if err := decodeState(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}

// headBuf returns a buffer of rgw_max_chunk_size bytes for a prefetch. The
// buffers are pooled because a prefetch reads into a whole chunk however
// short the head is.
func (s *Store) headBuf() *[]byte {
	if b, ok := s.headBufs.Get().(*[]byte); ok {
		return b
	}
	b := make([]byte, s.readCfg.chunk)
	return &b
}

// filterAttrs is rgw_filter_attrset over RGW_ATTR_PREFIX: the user.rgw. names
// only.
func filterAttrs(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		if strings.HasPrefix(k, meta.AttrPrefix) {
			out[k] = v
		}
	}
	return out
}

// decodeState fills st's typed fields from its attrs in get_obj_state_impl's
// order, so a head that fails two ways fails as radosgw's does. An attr that
// does not decode is radosgw's -EIO, which its error table lacks, so it
// answers UnknownError (set_req_state_err,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L349-L353 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L362-L366 at v20.2.4).
func decodeState(ctx context.Context, st *op.ObjectState) error {
	rec := st.Bucket
	if etag, ok := st.Attrs[meta.AttrETag]; ok {
		// "get rid of extra null character at the end of the etag, as we used
		// to store it like that": radosgw drops it from the attr itself.
		if n := len(etag); n > 0 && etag[n-1] == 0 {
			etag = etag[:n-1]
			st.Attrs[meta.AttrETag] = etag
		}
		st.ETag = string(etag)
	}
	if b, ok := st.Attrs[meta.AttrCompression]; ok {
		ci, err := decodeAttr(b, meta.DecodeCompressionInfo)
		if err != nil {
			return fmt.Errorf("%w: decoding the compression info of %s/%s: %w", op.ErrUnknown, rec.Info.Bucket.Name, st.Key.Name, err)
		}
		st.Compression = &ci
	}
	st.Checksum = storedChecksum(ctx, st)
	st.WriteTag = string(st.Attrs[meta.AttrIDTag])
	if b, ok := st.Attrs[meta.AttrManifest]; ok {
		m, err := decodeAttr(b, meta.DecodeManifest)
		if err != nil {
			return fmt.Errorf("%w: decoding the manifest of %s/%s: %w", op.ErrUnknown, rec.Info.Bucket.Name, st.Key.Name, err)
		}
		// "patch manifest to reflect the head we just read, some manifests
		// might be broken due to old bugs"
		setHead(&m, rec.Info.PlacementRule, meta.Obj{Bucket: rec.Info.Bucket, Key: st.Key}, st.Size)
		st.Size = m.ObjSize
		st.Manifest = &m
	}
	st.StorageClass = string(st.Attrs[meta.AttrStorageClass])
	st.ContentType = strings.TrimRight(string(st.Attrs[meta.AttrContentType]), "\x00")
	if _, ok := st.Attrs[meta.AttrOLHVer]; ok {
		slog.InfoContext(ctx, "refusing the olh head of a versioned object: versioning is not implemented",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", st.Key.Name))
		return fmt.Errorf("%w: %s/%s is the olh head of a versioned object", op.ErrNotImplemented, rec.Info.Bucket.Name, st.Key.Name)
	}
	return nil
}

// storedChecksum is st's user.rgw.cksum, nil when there is none or
// cksum.DecodeCksum refuses it: radosgw reads the attr in the ops that show
// it, never in get_obj_state_impl, so a record it cannot read fails no read
// of the object. Such a record is logged at debug level, as radosgw's
// list_parts logs one it cannot decode
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2919-L2923 at v20.2.4).
// Bytes past the record are left unread, as every radosgw reader but the
// checksum-mode GET leaves them (docs/ceph-upstream-bugs.md, "radosgw aborts
// on a checksum-mode GET of an object whose checksum attr has bytes past its
// record").
func storedChecksum(ctx context.Context, st *op.ObjectState) *cksum.Cksum {
	b, ok := st.Attrs[meta.AttrCksum]
	if !ok {
		return nil
	}
	c, err := decodeAttr(b, cksum.DecodeCksum)
	if err != nil {
		slog.DebugContext(ctx, "the stored checksum does not decode; the object shows none",
			slog.String("bucket", st.Bucket.Info.Bucket.Name), slog.String("key", st.Key.Name), slog.Any("error", err))
		return nil
	}
	return &c
}

// decodeAttr decodes an attr as radosgw's decode does, which leaves any
// bytes past the value unread.
func decodeAttr[T any](b []byte, decode func(*denc.Decoder) T) (T, error) {
	d := denc.NewDecoder(b)
	v := decode(d)
	return v, d.Err()
}

// setHead is RGWObjManifest::set_head
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.h#L406-L415 at v19.2.6
// and v20.2.4). Its objs[0] inserts an empty piece where there is none, which
// the assignment here reproduces.
func setHead(m *meta.Manifest, rule meta.PlacementRule, obj meta.Obj, size uint64) {
	m.HeadPlacementRule = rule
	m.Obj = obj
	m.HeadSize = size
	if m.ExplicitObjs && size > 0 {
		if m.Objs == nil {
			m.Objs = map[uint64]meta.ManifestPart{}
		}
		p := m.Objs[0]
		p.Loc, p.Size = obj, size
		m.Objs[0] = p
	}
}
