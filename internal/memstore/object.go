package memstore

import (
	"bytes"
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is the MD5 of the data
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// objKey is the key an object is stored under: the null instance is the
// plain object, as rgw_obj_key::need_to_encode_instance has it.
func objKey(k meta.ObjKey) meta.ObjKey {
	if k.Instance == "null" {
		k.Instance = ""
	}
	return k
}

// prefetchLen is rgw_max_chunk_size's default, the most PrefetchObject
// returns of an object's data.
const prefetchLen = 4 << 20

// StatObject implements op.ObjectStore.
func (s *Store) StatObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, _, err := s.stat(rec, key)
	return st, err
}

// PrefetchObject implements op.ObjectStore: StatObject plus a copy of the
// object's first prefetchLen bytes, memstore holding the object whole.
func (s *Store) PrefetchObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, o, err := s.stat(rec, key)
	if err != nil || o == nil {
		return st, err
	}
	st.Head = slices.Clone(o.data[:min(len(o.data), prefetchLen)])
	return st, nil
}

// stat returns key's state in rec's bucket and its object, nil when it does
// not exist; the caller holds the lock.
func (s *Store) stat(rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, *object, error) {
	b, err := s.instance(rec)
	if err != nil {
		return nil, nil, err
	}
	o, ok := b.objects[objKey(key)]
	if !ok {
		return &op.ObjectState{Bucket: rec, Key: key}, nil, nil
	}
	st := o.state
	st.Bucket, st.Key = rec, key
	st.Attrs = cloneAttrs(o.state.Attrs)
	st.Checksum = storedChecksum(st.Attrs)
	return &st, o, nil
}

// ReadObject implements op.ObjectStore, writing the range to sink in one
// Write; a range reaching past the object ends with it.
func (s *Store) ReadObject(_ context.Context, st *op.ObjectState, rng op.ByteRange, sink io.Writer) error {
	s.mu.RLock()
	o, err := s.object(st.Bucket, st.Key)
	var data []byte
	if err == nil {
		data = o.data
	}
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	size := uint64(len(data))
	if rng.Offset > size {
		return fmt.Errorf("range at %d of %d bytes: %w", rng.Offset, size, op.ErrInvalidRange)
	}
	if _, err := sink.Write(data[rng.Offset : rng.Offset+min(rng.Length, size-rng.Offset)]); err != nil {
		return fmt.Errorf("writing %s: %w", st.Key.Name, err)
	}
	return nil
}

// PutObject implements op.ObjectStore. p.Checksum's type is checked before
// the body is read, and its value once the body and its MD5 are.
func (s *Store) PutObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error) {
	if err := checkChecksumType(p.Checksum); err != nil {
		return nil, fmt.Errorf("object %s: %w", key.Name, err)
	}
	data, err := readBody(body, p.Size)
	if err != nil {
		return nil, err
	}
	sum := md5.Sum(data) //nolint:gosec // an S3 ETag is the MD5 of the data
	if p.ContentMD5 != nil && !bytes.Equal(sum[:], p.ContentMD5) {
		return nil, fmt.Errorf("object %s: %w", key.Name, op.ErrBadDigest)
	}
	ck, err := bodyChecksum(p.Checksum, data)
	if err != nil {
		return nil, fmt.Errorf("object %s: %w", key.Name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.instance(rec)
	if err != nil {
		return nil, err
	}
	k := objKey(key)
	if err := checkWrite(b.objects[k], p.IfMatch, p.IfNoneMatch); err != nil {
		return nil, err
	}
	attrs := withChecksum(cloneAttrs(p.Attrs), ck, s.cfg.Release)
	o := s.newObject(k, data, cmp.Or(p.ETag, hex.EncodeToString(sum[:])), attrs, p.Mtime, p.StorageClass)
	o.state.WriteTag = p.Tag
	if p.Tag == "" {
		// append_rand_alpha(cct, "", tag, 32),
		// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1621-L1628 at v19.2.6,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1623-L1630 at v20.2.4.
		o.state.WriteTag = "_" + randomAlphanumeric(31)
	}
	b.objects[k] = o
	return putResult(o, ck), nil
}

// checkChecksumType refuses a checksum request whose type rgw-go does not
// compute, before anything is read or written, so that no stored record
// carries a type a v20.2.4 radosgw cannot read.
func checkChecksumType(req *op.ChecksumRequest) error {
	if req != nil && !req.Type.Writable() {
		return fmt.Errorf("%w: rgw-go computes no checksum of type %d", op.ErrInvalidArgument, req.Type)
	}
	return nil
}

// bodyChecksum is req's checksum of data, with req's flags, or nil for no
// request. A value Expected supplies must be its armored form, compared as
// RGWPutObj_Cksum::verify compares the strings
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L202-L211 at v20.2.4),
// or it is ErrBadDigest.
func bodyChecksum(req *op.ChecksumRequest, data []byte) (*cksum.Cksum, error) {
	if req == nil {
		return nil, nil
	}
	h, err := cksum.NewHash(req.Type)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", op.ErrInvalidArgument, err)
	}
	h.Write(data)
	c := cksum.Finish(req.Type, h, req.Flags)
	if req.Expected != nil {
		if want, ok := req.Expected(); ok && want != c.Armor() {
			return nil, fmt.Errorf("%w: the %s checksum is %s, not %q", op.ErrBadDigest, req.Type.Info().Name, c.Armor(), want)
		}
	}
	return &c, nil
}

// withChecksum is attrs with c as user.rgw.cksum, attrs unchanged for no c.
func withChecksum(attrs map[string][]byte, c *cksum.Cksum, rel denc.Release) map[string][]byte {
	if c == nil {
		return attrs
	}
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	e := denc.NewEncoder()
	c.Encode(e, rel)
	attrs[meta.AttrCksum] = e.Bytes()
	return attrs
}

// storedChecksum decodes attrs' user.rgw.cksum: nil when there is none or
// it does not decode.
func storedChecksum(attrs map[string][]byte) *cksum.Cksum {
	b, ok := attrs[meta.AttrCksum]
	if !ok {
		return nil
	}
	d := denc.NewDecoder(b)
	c := cksum.DecodeCksum(d)
	if d.Err() != nil {
		return nil
	}
	return &c
}

// cloneCksum is a copy of c that shares no digest bytes with it.
func cloneCksum(c *cksum.Cksum) *cksum.Cksum {
	if c == nil {
		return nil
	}
	d := *c
	d.Digest = bytes.Clone(c.Digest)
	return &d
}

// DeleteObject implements op.ObjectStore. A bucket whose versioning is or
// was enabled or that has object lock is refused, as the RADOS store
// refuses it. The conditions are checked as Tentacle's Delete::delete_obj
// checks them, on both releases
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6609-L6671 at v20.2.4):
// the mtime must not be after UnmodifiedSince, the size must equal
// IfMatchSize, the mtime must equal IfMatchLastModified, both in whole
// seconds, and If-Match "*" passes any object while an ETag must match
// the object's.
func (s *Store) DeleteObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
	if f := rec.Info.Flags; f&(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled) != 0 {
		return fmt.Errorf("%w: deleting from bucket %s, which is versioned or has object lock",
			op.ErrNotImplemented, rec.Info.Bucket.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.instance(rec)
	if err != nil {
		return err
	}
	k := objKey(key)
	o, ok := b.objects[k]
	if !ok {
		return fmt.Errorf("object %s: %w", key.Name, op.ErrNoSuchKey)
	}
	if !deleteAllowed(&o.state, p) {
		return fmt.Errorf("object %s: %w", key.Name, op.ErrPreconditionFailed)
	}
	delete(b.objects, k)
	return nil
}

// deleteAllowed reports whether st passes a delete's conditions.
func deleteAllowed(st *op.ObjectState, p op.DeleteParams) bool {
	mtime := st.Mtime.Truncate(time.Second)
	switch {
	case !p.UnmodifiedSince.IsZero() && mtime.After(p.UnmodifiedSince.Truncate(time.Second)):
		return false
	case p.IfMatchSize != nil && *p.IfMatchSize != st.Size:
		return false
	case !p.IfMatchLastModified.IsZero() && !mtime.Equal(p.IfMatchLastModified.Truncate(time.Second)):
		return false
	case p.IfMatch != "" && p.IfMatch != "*" && !etagMatches(p.IfMatch, st.ETag):
		return false
	}
	return true
}

// CopyObject implements op.ObjectStore. IfMatch and IfNoneMatch are checked
// against the source as RGWRados::Object::Read::prepare checks a copy
// source
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6970-L6990 at v19.2.6).
func (s *Store) CopyObject(_ context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, p op.CopyParams) (*op.PutResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	so, err := s.object(src.Bucket, src.Key)
	if err != nil {
		return nil, err
	}
	etag := so.state.ETag
	if p.IfMatch != "" && !etagMatches(p.IfMatch, etag) {
		return nil, fmt.Errorf("copy source %s: %w", src.Key.Name, op.ErrPreconditionFailed)
	}
	if p.IfNoneMatch != "" && etagMatches(p.IfNoneMatch, etag) {
		return nil, fmt.Errorf("copy source %s: %w", src.Key.Name, op.ErrNotModified)
	}
	db, err := s.instance(dst)
	if err != nil {
		return nil, err
	}
	attrs := cloneAttrs(p.Attrs)
	if !p.ReplaceAttrs {
		attrs = applyAttrs(so.state.Attrs, p.Attrs, nil)
	}
	k := objKey(dstKey)
	o := s.newObject(k, so.data, etag, attrs, p.Mtime, cmp.Or(p.StorageClass, so.state.StorageClass))
	o.state.WriteTag = p.Tag
	if p.Tag == "" {
		o.state.WriteTag = "_" + randomAlphanumeric(31)
	}
	db.objects[k] = o
	return putResult(o, nil), nil
}

// SetObjectAttrs implements op.ObjectStore: set, then rm.
func (s *Store) SetObjectAttrs(_ context.Context, st *op.ObjectState, set map[string][]byte, rm []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, err := s.object(st.Bucket, st.Key)
	if err != nil {
		return err
	}
	o.state.Attrs = applyAttrs(o.state.Attrs, set, rm)
	o.state.ContentType = contentType(o.state.Attrs)
	return nil
}

// object finds key in rec's bucket; the caller holds the lock.
func (s *Store) object(rec *op.BucketRecord, key meta.ObjKey) (*object, error) {
	b, err := s.instance(rec)
	if err != nil {
		return nil, err
	}
	o, ok := b.objects[objKey(key)]
	if !ok {
		return nil, fmt.Errorf("object %s: %w", key.Name, op.ErrNoSuchKey)
	}
	return o, nil
}

// newObject builds a head write's object: attrs with the etag attr added, a
// fresh epoch and write tag, and now for a zero mtime.
func (s *Store) newObject(key meta.ObjKey, data []byte, etag string, attrs map[string][]byte, mtime time.Time, storageClass string) *object {
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrETag] = []byte(etag)
	if mtime.IsZero() {
		mtime = s.now()
	}
	return &object{
		state: op.ObjectState{
			Key:          key,
			Exists:       true,
			Size:         uint64(len(data)),
			Mtime:        mtime,
			Epoch:        s.nextEpoch(),
			Attrs:        attrs,
			ETag:         etag,
			WriteTag:     s.newTag(),
			StorageClass: storageClass,
			ContentType:  contentType(attrs),
		},
		data: data,
	}
}

// putResult describes o, whose write computed ck, or nil.
func putResult(o *object, ck *cksum.Cksum) *op.PutResult {
	return &op.PutResult{ETag: o.state.ETag, Size: o.state.Size, Mtime: o.state.Mtime, Epoch: o.state.Epoch, Checksum: ck}
}

// readBody reads a write's body. A body longer than its declared size is
// EntityTooLarge; a shorter one is RequestTimeout, as RGWPutObj::execute
// answers when the bytes read fall short of the content length
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4430-L4431 at v19.2.6).
func readBody(body io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		data, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("reading the body: %w", err)
		}
		return data, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, size+1))
	if err != nil {
		return nil, fmt.Errorf("reading the body: %w", err)
	}
	switch n := int64(len(data)); {
	case n > size:
		return nil, fmt.Errorf("body past its %d bytes: %w", size, op.ErrEntityTooLarge)
	case n < size:
		return nil, fmt.Errorf("body of %d of its %d bytes: %w", n, size, op.ErrRequestTimeout)
	}
	return data, nil
}

// checkWrite is v20.2.4's check_preconditions for a write's If-Match and
// If-None-Match against the object it replaces
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7286-L7328 at v20.2.4),
// which rgw-go applies on Squid too: "*" asks whether an object exists, an
// ETag condition is unquoted and must start with the object's ETag,
// If-Match on no object is NoSuchKey, and If-None-Match with an ETag
// passes when there is no object.
func checkWrite(o *object, ifMatch, ifNoneMatch string) error {
	if ifMatch != "" {
		switch {
		case o == nil:
			return op.ErrNoSuchKey
		case ifMatch != "*" && !etagMatches(ifMatch, o.state.ETag):
			return op.ErrPreconditionFailed
		}
	}
	if ifNoneMatch != "" && o != nil && (ifNoneMatch == "*" || etagMatches(ifNoneMatch, o.state.ETag)) {
		return op.ErrPreconditionFailed
	}
	return nil
}

// etagMatches is how a read or a Tentacle delete compares an If-Match or
// If-None-Match value with an ETag: unquoted, it must start with the ETag,
// as if_match_str.compare(0, etag.length(), etag) tests it.
func etagMatches(cond, etag string) bool { return strings.HasPrefix(unquote(cond), etag) }

// unquote is rgw_string_unquote
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L518-L533 at v19.2.6):
// a value that opens with a quote loses it and, past any trailing spaces,
// its closing one.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	t := strings.TrimRight(s, " ")
	if len(t) < 2 || t[len(t)-1] != '"' {
		return s
	}
	return t[1 : len(t)-1]
}

// contentType is the user.rgw.content_type attr without the NUL radosgw
// stores it with.
func contentType(attrs map[string][]byte) string {
	return strings.TrimSuffix(string(attrs[meta.AttrContentType]), "\x00")
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // an S3 ETag is the MD5 of the data
	return hex.EncodeToString(sum[:])
}
