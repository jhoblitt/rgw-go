package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

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

// readHead is RGWRados::raw_obj_stat (driver/rados/rgw_rados.cc:8827-8870 at
// v19.2.6, :9771-9814 at v20.2.4) followed by the decoding
// get_obj_state_impl does (:6120-6297 at v19.2.6, :6874-7051 at v20.2.4):
// one RADOS op that reads the head's version, every xattr, the stat and,
// with prefetch, its first rgw_max_chunk_size bytes.
func (s *Store) readHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error) {
	ref, err := s.headRef(ctx, rec, key)
	if errors.Is(err, radosclient.ErrNotFound) {
		// radosgw's stat creates a missing data pool, opening it with create
		// set (rgw_get_rados_ref, driver/rados/rgw_tools.cc:107-108 at
		// v19.2.6, :108-109 at v20.2.4), and then finds no object in it.
		// rgw-go creates no pool (docs/exclusions.md).
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
	// does not decode (cls_version_client.cc:66-77 at v19.2.6 and v20.2.4).
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
// answers UnknownError (set_req_state_err, rgw_common.cc:349-353 at v19.2.6,
// :362-366 at v20.2.4).
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

// decodeAttr decodes an attr as radosgw's decode does, which leaves any
// bytes past the value unread.
func decodeAttr[T any](b []byte, decode func(*denc.Decoder) T) (T, error) {
	d := denc.NewDecoder(b)
	v := decode(d)
	return v, d.Err()
}

// setHead is RGWObjManifest::set_head (driver/rados/rgw_obj_manifest.h:406-415
// at v19.2.6 and v20.2.4). Its objs[0] inserts an empty piece where there
// is none, which the assignment here reproduces.
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
