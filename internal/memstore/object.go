package memstore

import (
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is the MD5 of the data
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

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

// StatObject implements op.ObjectStore.
func (s *Store) StatObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := s.instance(rec)
	if err != nil {
		return nil, err
	}
	o, ok := b.objects[objKey(key)]
	if !ok {
		return &op.ObjectState{Bucket: rec, Key: key}, nil
	}
	st := o.state
	st.Bucket, st.Key = rec, key
	st.Attrs = cloneAttrs(o.state.Attrs)
	return &st, nil
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

// PutObject implements op.ObjectStore.
func (s *Store) PutObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error) {
	data, err := readBody(body, p.Size)
	if err != nil {
		return nil, err
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
	o := s.newObject(k, data, cmp.Or(p.ETag, md5Hex(data)), cloneAttrs(p.Attrs), p.Mtime, p.StorageClass)
	b.objects[k] = o
	return putResult(o), nil
}

// DeleteObject implements op.ObjectStore. IfMatch is checked as Tentacle's
// check_preconditions checks a delete's (rgw_rados.cc:7287-7306 at v20.2.4):
// "*" passes any object, and an ETag must match the object's; Squid's
// delete takes no If-Match.
func (s *Store) DeleteObject(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
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
	if p.IfMatch != "" && p.IfMatch != "*" && !etagMatches(p.IfMatch, o.state.ETag) {
		return fmt.Errorf("object %s: %w", key.Name, op.ErrPreconditionFailed)
	}
	delete(b.objects, k)
	return nil
}

// CopyObject implements op.ObjectStore. IfMatch and IfNoneMatch are checked
// against the source as RGWRados::Object::Read::prepare checks a copy
// source (rgw_rados.cc:6970-6990 at v19.2.6).
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
	db.objects[k] = o
	return putResult(o), nil
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

func putResult(o *object) *op.PutResult {
	return &op.PutResult{ETag: o.state.ETag, Size: o.state.Size, Mtime: o.state.Mtime, Epoch: o.state.Epoch}
}

// readBody reads a write's body. A body longer than its declared size is
// EntityTooLarge; a shorter one is RequestTimeout, as RGWPutObj::execute
// answers when the bytes read fall short of the content length
// (rgw_op.cc:4430-4431 at v19.2.6).
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

// checkWrite is prepare_atomic_modification's check of a write's If-Match
// and If-None-Match against the object it replaces (rgw_rados.cc:6515-6543 at
// v19.2.6): "*" asks whether an object exists, and an ETag is compared as
// strncmp(cond, etag, len(etag)) compares it, which a missing object, having
// no etag attr, fails either way.
func checkWrite(o *object, ifMatch, ifNoneMatch string) error {
	matches := func(cond string) bool { return o != nil && strings.HasPrefix(cond, o.state.ETag) }
	switch {
	case ifMatch == "*" && o == nil,
		ifMatch != "" && ifMatch != "*" && !matches(ifMatch),
		ifNoneMatch == "*" && o != nil,
		ifNoneMatch != "" && ifNoneMatch != "*" && (o == nil || matches(ifNoneMatch)):
		return op.ErrPreconditionFailed
	}
	return nil
}

// etagMatches is how a read or a Tentacle delete compares an If-Match or
// If-None-Match value with an ETag: unquoted, it must start with the ETag,
// as if_match_str.compare(0, etag.length(), etag) tests it.
func etagMatches(cond, etag string) bool { return strings.HasPrefix(unquote(cond), etag) }

// unquote is rgw_string_unquote (rgw_common.cc:518-533 at v19.2.6): a value
// that opens with a quote loses it and, past any trailing spaces, its
// closing one.
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
