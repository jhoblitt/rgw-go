package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// errNotOLH ends a follow whose second head read found the head no longer
// an OLH head.
var errNotOLH = errors.New("the head the olh update left is no olh head")

// StatObject implements op.ObjectStore: objectState without prefetch.
func (s *Store) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.objectState(ctx, rec, key, false)
}

// PrefetchObject implements op.ObjectStore: objectState with prefetch.
func (s *Store) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.objectState(ctx, rec, key, true)
}

// objectState is get_obj_state_impl with follow_olh set, as
// Read::prepare reads an object's state
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6260-L6437 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6874-L7051 at v20.2.4):
// headState, and for a key named without an instance whose head carries
// olh.ver, the OLH head of a versioned key, followHead. A named version
// whose head is missing is looked up in the index (namedVersion).
func (s *Store) objectState(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error) {
	st, err := s.headState(ctx, rec, key, prefetch)
	if err != nil {
		return nil, err
	}
	if _, olh := st.Attrs[meta.AttrOLHVer]; olh && key.Instance == "" {
		return s.followHead(ctx, rec, st, prefetch)
	}
	if !st.Exists && key.Instance != "" {
		return s.namedVersion(ctx, st)
	}
	return st, nil
}

// headState is get_obj_state_impl without following the OLH: statHead, but
// the null version named on an OLH head without a manifest does not exist,
// "read null version, and the head object only have olh info"
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6427-L6433 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7041-L7047 at v20.2.4).
func (s *Store) headState(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error) {
	st, err := s.statHead(ctx, rec, key, prefetch)
	if err != nil {
		return nil, err
	}
	if _, olh := st.Attrs[meta.AttrOLHVer]; olh && key.Instance == meta.NullInstance && st.Manifest == nil {
		return &op.ObjectState{Bucket: rec, Key: key}, nil
	}
	return st, nil
}

// followHead is get_olh_target_state
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6238-L6258 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6852-L6872 at v20.2.4):
// followOLH on head, a versioned key's OLH head as read, and the headState
// of the version it resolves, with the prefetch head was read with. A
// version whose key is head's, the null version, is head itself, as
// radosgw's object context hands back the state it already read for that
// key. A follow that answers not found, a current delete marker's among
// them, is a state that does not exist under head's key, with DeleteMarker
// set for the marker.
//
// When the follow runs the update for a pending modification, the head is
// read again once the update has run and the version resolved from that
// read, without a second update, where radosgw resolves it from the head as
// read before the update (docs/exclusions.md, "A read that replays a
// pending modification answers the version current after it"). A head that
// read finds gone, or no longer an OLH head, is the key's state as found.
func (s *Store) followHead(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState, prefetch bool) (*op.ObjectState, error) {
	var found *op.ObjectState
	update := func(ctx context.Context, h *op.ObjectState) error {
		if err := s.updateOLH(ctx, rec, h); err != nil {
			return err
		}
		fresh, err := s.statHead(ctx, rec, h.Key, prefetch)
		if err != nil {
			return err
		}
		if _, olh := fresh.Attrs[meta.AttrOLHVer]; !olh {
			found = fresh
			return errNotOLH
		}
		*h = *fresh
		return nil
	}
	target, deleteMarker, err := s.followOLH(ctx, head, update)
	switch {
	case errors.Is(err, errNotOLH):
		return found, nil
	case errors.Is(err, radosclient.ErrNotFound):
		return &op.ObjectState{Bucket: rec, Key: head.Key, DeleteMarker: deleteMarker}, nil
	case err != nil:
		return nil, err
	}
	if target == head.Key {
		return head, nil
	}
	return s.headState(ctx, rec, target, prefetch)
}

// namedVersion is the index lookup of st, the state of a version a request
// named whose head is missing, null included, on a bucket whose versioning
// was ever enabled: its entry as getInstanceEntry resolves it, which for the
// null version finds its delete marker behind the version marker. A delete
// marker's entry sets st's DeleteMarker when the marker is listed, which its
// list entry says: an unlink at an epoch below the OLH entry's takes a
// marker's list entry and leaves its instance entry
// (docs/ceph-upstream-bugs.md, "cls_rgw's bucket_unlink_instance at an older
// epoch leaves a delete marker's instance entry"). radosgw makes no such
// lookup, answering 404 for a marker as for a missing version. A listed
// marker's state carries its index entry's mtime and versioned_epoch, which
// tell it from any other marker, as a versioned delete's Expect compares them
// (expectRemoval): every marker's entry carries the tag "delete-marker"
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1350-L1358 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484 at v20.2.4).
func (s *Store) namedVersion(ctx context.Context, st *op.ObjectState) (*op.ObjectState, error) {
	rec := st.Bucket
	if !rec.Info.Versioned() {
		return st, nil
	}
	key := st.Key
	if key.Instance == meta.NullInstance {
		key.Instance = ""
	}
	en, err := s.getInstanceEntry(ctx, rec, key)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return st, nil
	case err != nil:
		return nil, fmt.Errorf("looking up %s: %w", versionName(rec, st.Key), err)
	}
	if en.Flags&rgw.FlagDeleteMarker == 0 {
		return st, nil
	}
	if st.DeleteMarker, err = s.listed(ctx, rec, key, en); err != nil {
		return nil, fmt.Errorf("looking up the list entry of %s: %w", versionName(rec, st.Key), err)
	}
	if st.DeleteMarker {
		st.Mtime, st.VersionedEpoch = en.Meta.Mtime, en.VersionedEpoch
	}
	return st, nil
}

// listed reports whether en, the instance entry of key's version, has its
// list entry, the key get_list_index_key gives it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L302-L315 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L352-L365 at v20.2.4),
// read through bi_get on the shard of key's name.
func (s *Store) listed(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, en rgw.DirEntry) (bool, error) {
	_, ok, err := s.listEntry(ctx, rec, key, en)
	return ok, err
}

// listEntry is listed's read, answering the list entry itself.
func (s *Store) listEntry(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, en rgw.DirEntry) (rgw.DirEntry, bool, error) {
	x := s.newOLHIndex(rec, key).x
	list := rgw.ObjKey{Name: rgw.ListIndexKey(en.Key, en.VersionedEpoch)}
	var le rgw.DirEntry
	err := x.readShard(ctx, func(sh indexShard) error {
		var err error
		le, err = x.dirEntry(ctx, sh, list, rgw.BIIndexPlain)
		return err
	})
	if errors.Is(err, radosclient.ErrNotFound) {
		return rgw.DirEntry{}, false, nil
	}
	return le, err == nil, err
}

// unfollowedHeads is the HeadStater a listing checks a pending entry's head
// with: readOLHHead, which reads an OLH head as any other head.
type unfollowedHeads struct{ s *Store }

// StatObject implements HeadStater.
func (h unfollowedHeads) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return h.s.readOLHHead(ctx, rec, key)
}

// readHead is statHead for the driver's own reads of a head it goes on to
// write or remove, which serve unversioned objects alone: it refuses the OLH
// head of a versioned object.
func (s *Store) readHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	st, err := s.statHead(ctx, rec, key, false)
	if err != nil {
		return nil, err
	}
	if _, ok := st.Attrs[meta.AttrOLHVer]; ok {
		slog.InfoContext(ctx, "refusing the olh head of a versioned object: versioning is not implemented",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", st.Key.Name))
		return nil, fmt.Errorf("%w: %s/%s is the olh head of a versioned object", op.ErrNotImplemented, rec.Info.Bucket.Name, st.Key.Name)
	}
	return st, nil
}

// readOLHHead is get_obj_state without following the OLH, as set_olh and
// unlink_obj_instance read the key's OLH head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8694 at v19.2.7,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8793 at v19.2.7;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9440 at v20.2.4,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9539 at v20.2.4):
// statHead with no prefetch, which reads an OLH head as any other head.
func (s *Store) readOLHHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.statHead(ctx, rec, key, false)
}

// statHead is RGWRados::raw_obj_stat
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8827-L8870 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9771-L9814 at v20.2.4)
// followed by the decoding get_obj_state_impl does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6120-L6297 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6874-L7051 at v20.2.4):
// one RADOS op that reads the head's version, every xattr, the stat and,
// with prefetch, its first rgw_max_chunk_size bytes.
func (s *Store) statHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error) {
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
	return nil
}

// storedChecksum is st's user.rgw.cksum, nil when there is none or
// cksum.DecodeAttr refuses it: radosgw reads the attr in the ops that show
// it, never in get_obj_state_impl, so a record it cannot read fails no read
// of the object. Such a record is logged at debug level, as radosgw's
// list_parts logs one it cannot decode
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2919-L2923 at v20.2.4).
// An attr with bytes past its record is one cksum.DecodeAttr refuses, where
// radosgw's checksum-mode GET aborts and its other readers take the record
// (docs/exclusions.md, "A stored checksum rgw-go cannot read whole is not
// shown").
func storedChecksum(ctx context.Context, st *op.ObjectState) *cksum.Cksum {
	b, ok := st.Attrs[meta.AttrCksum]
	if !ok {
		return nil
	}
	c, err := cksum.DecodeAttr(b)
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
