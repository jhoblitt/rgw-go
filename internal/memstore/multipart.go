package memstore

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // a multipart ETag is an MD5 of MD5s
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// minPartSize is rgw_multipart_min_part_size's default: every part of a
// completion but the last must be at least this long.
const minPartSize = 5 << 20

func uploadKey(bucketID, key, id string) string {
	return bucketID + "\x00" + key + "\x00" + id
}

func (u *upload) metaName() string { return meta.MultipartMetaName(u.up.Key.Name, u.up.ID) }

// copyUpload returns the caller's view of u, its bucket rec.
func copyUpload(u *upload, rec *op.BucketRecord) *op.Upload {
	c := u.up
	c.Bucket = rec
	c.Owner = cloneOwner(u.up.Owner)
	c.Attrs = cloneAttrs(u.up.Attrs)
	return &c
}

// CreateUpload implements op.MultipartStore. The id is radosgw's v2 form,
// "2~" and 31 characters of gen_rand_alphanumeric (rgw_sal_rados.cc:3278-3283
// at v19.2.6).
func (s *Store) CreateUpload(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.instance(rec); err != nil {
		return nil, err
	}
	u := &upload{
		up: op.Upload{
			ID:        meta.MultipartUploadIDPrefix + randomAlphanumeric(31),
			Key:       key,
			Owner:     cloneOwner(p.Owner),
			OwnerName: p.OwnerName,
			Initiated: s.now(),
			Placement: p.Placement,
			Attrs:     cloneAttrs(p.Attrs),
		},
		bucketID: rec.Info.Bucket.ID,
		parts:    map[int]*part{},
	}
	s.uploads[uploadKey(u.bucketID, key.Name, u.up.ID)] = u
	return copyUpload(u, rec), nil
}

// GetUpload implements op.MultipartStore.
func (s *Store) GetUpload(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (*op.Upload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.uploads[uploadKey(rec.Info.Bucket.ID, key.Name, uploadID)]
	if !ok {
		return nil, fmt.Errorf("upload %s of %s: %w", uploadID, key.Name, op.ErrNoSuchUpload)
	}
	return copyUpload(u, rec), nil
}

// upload finds up; the caller holds the lock.
func (s *Store) upload(up *op.Upload) (*upload, error) {
	if up.Bucket != nil {
		if u, ok := s.uploads[uploadKey(up.Bucket.Info.Bucket.ID, up.Key.Name, up.ID)]; ok {
			return u, nil
		}
	}
	return nil, fmt.Errorf("upload %s of %s: %w", up.ID, up.Key.Name, op.ErrNoSuchUpload)
}

// PutPart implements op.MultipartStore; a part replaces one of its number.
// As the driver does, it answers NoSuchUpload for an unknown upload before
// reading the body, and refuses a body whose MD5 is not p.ContentMD5 with
// BadDigest.
func (s *Store) PutPart(_ context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	s.mu.RLock()
	_, err := s.upload(up)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	data, err := readBody(body, p.Size)
	if err != nil {
		return nil, err
	}
	if p.ContentMD5 != nil {
		if sum := md5.Sum(data); !bytes.Equal(sum[:], p.ContentMD5) { //nolint:gosec // Content-MD5 is an MD5
			return nil, fmt.Errorf("part %d: %w", n, op.ErrBadDigest)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.upload(up)
	if err != nil {
		return nil, err
	}
	return s.storePart(u, n, data), nil
}

// CopyPart implements op.MultipartStore: rng of src, its end clamped to the
// object, becomes part n.
func (s *Store) CopyPart(_ context.Context, up *op.Upload, n int, src *op.ObjectState, rng op.ByteRange) (*op.PartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.upload(up)
	if err != nil {
		return nil, err
	}
	so, err := s.object(src.Bucket, src.Key)
	if err != nil {
		return nil, err
	}
	size := uint64(len(so.data))
	if rng.Offset > size {
		return nil, fmt.Errorf("copy range at %d of %d bytes: %w", rng.Offset, size, op.ErrInvalidRange)
	}
	return s.storePart(u, n, so.data[rng.Offset:rng.Offset+min(rng.Length, size-rng.Offset)]), nil
}

func (s *Store) storePart(u *upload, n int, data []byte) *op.PartResult {
	pt := &part{Number: n, ETag: md5Hex(data), Size: uint64(len(data)), Mtime: s.now(), data: data}
	u.parts[n] = pt
	return &op.PartResult{ETag: pt.ETag, Size: pt.Size, Mtime: pt.Mtime}
}

// ListParts implements op.MultipartStore: the parts numbered past marker, in
// order, at most maxParts of them; NextMarker is the last number returned.
func (s *Store) ListParts(_ context.Context, up *op.Upload, marker, maxParts int) (op.ListPartsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, err := s.upload(up)
	if err != nil {
		return op.ListPartsResult{}, err
	}
	var numbers []int
	for n := range u.parts {
		if n > marker {
			numbers = append(numbers, n)
		}
	}
	slices.Sort(numbers)
	res := op.ListPartsResult{NextMarker: marker}
	for _, n := range numbers[:min(max(maxParts, 0), len(numbers))] {
		res.Parts = append(res.Parts, u.parts[n].Part)
		res.NextMarker = n
	}
	res.Truncated = len(numbers) > len(res.Parts)
	return res, nil
}

// ListUploads implements op.MultipartStore as RadosBucket::list_multiparts
// lists the multipart namespace (rgw_sal_rados.cc:915-956 at v19.2.6): in
// the order of the uploads' meta names, "<key>.<upload id>.meta", after the
// marker "<KeyMarker>.<UploadIDMarker>.meta" (rgw_rest.cc:1646-1654), with
// Prefix and Delimiter applied to the meta names as ListObjects applies
// them to key names. A key marker without an upload id marker thus sorts
// before that key's own uploads, and "a-b" lists before "a". The next
// markers name the last upload of the page, as RGWListBucketMultiparts
// sets them (rgw_op.cc:6741-6744), and stay empty when it holds none.
func (s *Store) ListUploads(_ context.Context, rec *op.BucketRecord, p op.ListUploadsParams) (op.ListUploadsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.instance(rec); err != nil {
		return op.ListUploadsResult{}, err
	}
	marker := ""
	if p.KeyMarker != "" {
		marker = meta.MultipartMetaName(p.KeyMarker, p.UploadIDMarker)
	}
	var ups []*upload
	for _, u := range s.uploads {
		if u.bucketID == rec.Info.Bucket.ID && u.metaName() > marker {
			ups = append(ups, u)
		}
	}
	slices.SortFunc(ups, func(a, b *upload) int { return strings.Compare(a.metaName(), b.metaName()) })

	var res op.ListUploadsResult
	pg := newPager(p.Prefix, p.Delimiter, marker, p.MaxUploads)
list:
	for _, u := range ups {
		switch pg.next(u.metaName()) {
		case pagePrefix:
			res.CommonPrefixes = append(res.CommonPrefixes, pg.last)
		case pageEntry:
			res.Uploads = append(res.Uploads, *copyUpload(u, rec))
		case pageFull:
			res.Truncated = true
			break list
		case pageSkip:
		}
	}
	if n := len(res.Uploads); n > 0 {
		res.NextKeyMarker, res.NextUploadIDMarker = res.Uploads[n-1].Key.Name, res.Uploads[n-1].ID
	}
	return res, nil
}

// Complete implements op.MultipartStore as RadosMultipartUpload::complete
// checks and assembles a completion (rgw_sal_rados.cc:3433-3640 at v19.2.6):
// the listed parts must be the uploaded ones, in lockstep, each with its
// ETag, every one but the last at least minPartSize; the object's ETag is
// the MD5 of the parts' binary MD5s, "-" and the part count. It takes the
// upload's attrs, and the upload is gone once it lands.
func (s *Store) Complete(_ context.Context, up *op.Upload, parts []op.CompletePart) (*op.PutResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.upload(up)
	if err != nil {
		return nil, err
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].Number <= parts[i-1].Number {
			return nil, fmt.Errorf("part %d after part %d: %w", parts[i].Number, parts[i-1].Number, op.ErrInvalidPartOrder)
		}
	}
	uploaded := slices.Sorted(maps.Keys(u.parts))
	if len(uploaded) != len(parts) {
		return nil, fmt.Errorf("%d parts listed of %d uploaded: %w", len(parts), len(uploaded), op.ErrInvalidPart)
	}
	var data bytes.Buffer
	digests := md5.New() //nolint:gosec // a multipart ETag is an MD5 of MD5s
	for i, cp := range parts {
		pt := u.parts[uploaded[i]]
		if i < len(parts)-1 && pt.Size < minPartSize {
			return nil, fmt.Errorf("part %d of %d bytes: %w", pt.Number, pt.Size, op.ErrEntityTooSmall)
		}
		if cp.Number != pt.Number || unquote(cp.ETag) != pt.ETag {
			return nil, fmt.Errorf("part %d: %w", cp.Number, op.ErrInvalidPart)
		}
		raw, hexErr := hex.DecodeString(pt.ETag)
		if hexErr != nil {
			return nil, fmt.Errorf("part %d's etag %q: %w", pt.Number, pt.ETag, op.ErrInvalidPart)
		}
		digests.Write(raw)
		data.Write(pt.data)
	}
	b, ok := s.instances[u.bucketID]
	if !ok {
		return nil, fmt.Errorf("bucket of upload %s: %w", u.up.ID, op.ErrNoSuchBucket)
	}
	etag := hex.EncodeToString(digests.Sum(nil)) + "-" + strconv.Itoa(len(parts))
	k := objKey(u.up.Key)
	o := s.newObject(k, data.Bytes(), etag, cloneAttrs(u.up.Attrs), time.Time{}, u.up.Placement.StorageClass)
	b.objects[k] = o
	delete(s.uploads, uploadKey(u.bucketID, u.up.Key.Name, u.up.ID))
	return putResult(o), nil
}

// Abort implements op.MultipartStore.
func (s *Store) Abort(_ context.Context, up *op.Upload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.upload(up)
	if err != nil {
		return err
	}
	delete(s.uploads, uploadKey(u.bucketID, u.up.Key.Name, u.up.ID))
	return nil
}
