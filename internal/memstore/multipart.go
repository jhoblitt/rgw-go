package memstore

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // a multipart ETag is an MD5 of MD5s
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
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
// "2~" and 31 characters of gen_rand_alphanumeric
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3278-L3283 at v19.2.6).
// The checksum type is kept on Tentacle alone, whose multipart_upload_info
// has one (op.UploadChecksum); the retention and legal hold on both, for the
// completion's object.
func (s *Store) CreateUpload(_ context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.instance(rec); err != nil {
		return nil, err
	}
	var uc op.UploadChecksum
	if s.cfg.Release >= denc.Tentacle {
		uc = p.Checksum
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
			Checksum:  uc,
		},
		bucketID: rec.Info.Bucket.ID,
		parts:    map[int]*part{},
	}
	if p.Retention != nil {
		u.retention = new(*p.Retention)
	}
	if p.LegalHold != nil {
		u.legalHold = new(*p.LegalHold)
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
// As the driver does, it answers NoSuchUpload for an unknown upload, and
// then a checksum type it does not compute, before reading the body; a
// body whose MD5 is not p.ContentMD5, and then one whose checksum is not
// the value p.Checksum expects, is BadDigest.
func (s *Store) PutPart(_ context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	s.mu.RLock()
	_, err := s.upload(up)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if err = checkChecksumType(p.Checksum); err != nil {
		return nil, fmt.Errorf("part %d: %w", n, err)
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
	ck, err := bodyChecksum(p.Checksum, data)
	if err != nil {
		return nil, fmt.Errorf("part %d: %w", n, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.upload(up)
	if err != nil {
		return nil, err
	}
	return s.storePart(u, n, data, ck), nil
}

// CopyPart implements op.MultipartStore: rng of src, its end clamped to the
// object, becomes part n, with the upload's checksum type computed over it.
func (s *Store) CopyPart(_ context.Context, up *op.Upload, n int, src *op.ObjectState, rng op.ByteRange) (*op.PartResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.upload(up)
	if err != nil {
		return nil, err
	}
	req, err := uploadChecksumRequest(u.up.Checksum)
	if err != nil {
		return nil, fmt.Errorf("part %d: %w", n, err)
	}
	so, err := s.object(src.Bucket, src.Key)
	if err != nil {
		return nil, err
	}
	size := uint64(len(so.data))
	if rng.Offset > size {
		return nil, fmt.Errorf("copy range at %d of %d bytes: %w", rng.Offset, size, op.ErrInvalidRange)
	}
	data := so.data[rng.Offset : rng.Offset+min(rng.Length, size-rng.Offset)]
	ck, err := bodyChecksum(req, data)
	if err != nil {
		return nil, fmt.Errorf("part %d: %w", n, err)
	}
	return s.storePart(u, n, data, ck), nil
}

// uploadChecksumRequest is the request that computes uc over a part, nil
// for an upload without a type, and ErrNotImplemented for a type rgw-go
// does not compute.
func uploadChecksumRequest(uc op.UploadChecksum) (*op.ChecksumRequest, error) {
	if err := checkUploadChecksum(uc); err != nil {
		return nil, err
	}
	if uc.Type == cksum.None {
		return nil, nil
	}
	return &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}, nil
}

// checkUploadChecksum refuses an upload whose stored checksum type rgw-go
// does not compute: xxh3 or blake3, which a radosgw may have stored, or a
// type a newer radosgw added.
func checkUploadChecksum(uc op.UploadChecksum) error {
	if uc.Type != cksum.None && !uc.Type.Writable() {
		return fmt.Errorf("%w: the upload's checksum type %d is not one rgw-go computes", op.ErrNotImplemented, uc.Type)
	}
	return nil
}

// storePart stores data as part n, whose PutPart or CopyPart computed ck.
// The part's record keeps ck on Tentacle alone (op.Part).
func (s *Store) storePart(u *upload, n int, data []byte, ck *cksum.Cksum) *op.PartResult {
	pt := &part{Number: n, ETag: md5Hex(data), Size: uint64(len(data)), Mtime: s.now(), data: data}
	if s.cfg.Release >= denc.Tentacle {
		pt.Checksum = cloneCksum(ck)
	}
	u.parts[n] = pt
	return &op.PartResult{ETag: pt.ETag, Size: pt.Size, Mtime: pt.Mtime, Checksum: ck}
}

// ListParts implements op.MultipartStore: the parts numbered past marker, in
// order, at most maxParts of them; NextMarker is the last number returned,
// and 0 when none is, as ListParts renders cur_max
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4135-L4149 at v19.2.6).
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
	var res op.ListPartsResult
	for _, n := range numbers[:min(max(maxParts, 0), len(numbers))] {
		p := u.parts[n].Part
		p.Checksum = cloneCksum(p.Checksum)
		res.Parts = append(res.Parts, p)
		res.NextMarker = n
	}
	res.Truncated = len(numbers) > len(res.Parts)
	return res, nil
}

// ListUploads implements op.MultipartStore as the driver does: the listing
// op.UploadListing names, as op.UploadsFromListing pages it.
func (s *Store) ListUploads(_ context.Context, rec *op.BucketRecord, p op.ListUploadsParams) (op.ListUploadsResult, error) {
	s.mu.RLock()
	lr, err := s.listObjects(rec, op.UploadListing(p))
	s.mu.RUnlock()
	if err != nil {
		return op.ListUploadsResult{}, err
	}
	return op.UploadsFromListing(rec, lr), nil
}

// Complete implements op.MultipartStore as RadosMultipartUpload::complete
// checks and assembles a completion
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3433-L3640 at v19.2.6):
// the listed parts, sorted by op.SortCompleteParts, must be the
// uploaded ones, in lockstep, each with its ETag, every one but the last at
// least minPartSize; the object's ETag is the MD5 of the parts' binary MD5s,
// "-" and the part count. up's If-Match and If-None-Match are checked as a
// PUT's are, and a refused completion leaves the upload in place. The
// object is written at up.Key, whose instance is the version the caller
// chose, and takes the upload's attrs, its retention and legal hold, then
// the bucket's default retention (withDefaultRetention), and up's write
// tag; the upload is gone once it lands, and the result names that version
// as the driver's does (completeResult). A missing upload is
// op.ErrCompletionInProgress, as the driver answers it, unless the key's
// object carries the ETag the parts make:
// a completion retried after it succeeded is answered 200 as radosgw's
// check_previously_completed answers it (previouslyCompleted).
//
// An upload with a checksum type combines its matched parts' checksums once
// every listed part has passed those checks (combineParts), then checks
// up.SuppliedChecksum against the combination (checkSupplied), before the
// conditions.
func (s *Store) Complete(_ context.Context, up *op.Upload, parts []op.CompletePart) (*op.PutResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts = op.SortCompleteParts(parts)
	u, err := s.upload(up)
	if err != nil {
		// radosgw takes the completion lock before it reads the upload, and
		// the lock of a meta object that is gone fails, so a missing upload
		// reads as a completion in progress, never NoSuchUpload, unless the
		// key already carries the ETag the parts make: a completion retried
		// after it succeeded.
		if res, ok := s.previouslyCompleted(up, parts); ok {
			return res, nil
		}
		return nil, fmt.Errorf("%w: upload %s of %s is gone", op.ErrCompletionInProgress, up.ID, up.Key.Name)
	}
	if err = checkUploadChecksum(u.up.Checksum); err != nil {
		return nil, fmt.Errorf("completing upload %s: %w", u.up.ID, err)
	}
	uploaded := slices.Sorted(maps.Keys(u.parts))
	if len(uploaded) != len(parts) {
		return nil, fmt.Errorf("%d parts listed of %d uploaded: %w", len(parts), len(uploaded), op.ErrInvalidPart)
	}
	var data bytes.Buffer
	etags := make([]string, 0, len(parts))
	named := make([]*part, 0, len(parts))
	for i, cp := range parts {
		pt := u.parts[uploaded[i]]
		if i < len(parts)-1 && pt.Size < minPartSize {
			return nil, fmt.Errorf("part %d of %d bytes: %w", pt.Number, pt.Size, op.ErrEntityTooSmall)
		}
		if cp.Number != pt.Number || unquote(cp.ETag) != pt.ETag {
			return nil, fmt.Errorf("part %d: %w", cp.Number, op.ErrInvalidPart)
		}
		etags = append(etags, pt.ETag)
		named = append(named, pt)
		data.Write(pt.data)
	}
	etag, err := op.MultipartETag(etags)
	if err != nil {
		return nil, err
	}
	ck, err := combineParts(u.up.Checksum, named)
	if err != nil {
		return nil, err
	}
	if err := checkSupplied(ck, up.SuppliedChecksum, len(parts)); err != nil {
		return nil, err
	}
	b, ok := s.instances[u.bucketID]
	if !ok {
		return nil, fmt.Errorf("bucket of upload %s: %w", u.up.ID, op.ErrNoSuchBucket)
	}
	k := objKey(up.Key)
	if err := checkWrite(b.objects[k], up.IfMatch, up.IfNoneMatch); err != nil {
		return nil, err
	}
	attrs := withUploadLock(withChecksum(cloneAttrs(u.up.Attrs), ck, s.cfg.Release), u)
	o := s.newObject(k, data.Bytes(), etag, attrs, time.Time{}, u.up.Placement.StorageClass)
	if err := withDefaultRetention(up.Bucket, o); err != nil {
		return nil, err
	}
	if up.WriteTag != "" {
		o.state.WriteTag = up.WriteTag
	}
	b.objects[k] = o
	delete(s.uploads, uploadKey(u.bucketID, u.up.Key.Name, u.up.ID))
	return completeResult(o, ck), nil
}

// completeResult is putResult for a completion, which also names the
// version it wrote or found: o's instance, empty for the null version.
func completeResult(o *object, ck *cksum.Cksum) *op.PutResult {
	res := putResult(o, ck)
	res.Version = o.state.Key.Instance
	return res
}

// combineParts is the checksum of an upload of type uc over parts, the
// parts a completion names in number order, through cksum.NewCombiner;
// nil for an upload without a type. A part without a checksum, or with one
// of another type, is ErrInvalidRequest, as try_sum_part_cksums answers it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7104-L7131 at v20.2.4).
// That function combines the last page list_parts returns rather than the
// parts named
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7068-L7092 at v20.2.4).
func combineParts(uc op.UploadChecksum, parts []*part) (*cksum.Cksum, error) {
	if uc.Type == cksum.None {
		return nil, nil
	}
	c := cksum.NewCombiner(uc.Type, uc.Flags)
	for _, pt := range parts {
		if pt.Checksum == nil {
			return nil, fmt.Errorf("%w: part %d has no checksum", op.ErrInvalidRequest, pt.Number)
		}
		if err := c.Append(*pt.Checksum, pt.Size); err != nil {
			return nil, fmt.Errorf("%w: part %d: %w", op.ErrInvalidRequest, pt.Number, err)
		}
	}
	sum := c.Final()
	return &sum, nil
}

// checkSupplied compares the value req supplies for a completion with ck,
// the combination of its n parts: it must be ck's armored form, with "-n"
// when ck is composite, or a composite's without it, which radosgw accepts
// for minio's clients
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7295-L7341 at v20.2.4).
// Any other value, or one of another type, is ErrBadDigest. Nothing is
// compared without ck.
func checkSupplied(ck *cksum.Cksum, req *op.ChecksumRequest, n int) error {
	if ck == nil || req == nil || req.Expected == nil {
		return nil
	}
	got, ok := req.Expected()
	if !ok {
		return nil
	}
	armor := ck.Armor()
	want := armor
	if ck.Composite() {
		want += "-" + strconv.Itoa(n)
	}
	if req.Type == ck.Type && (got == want || ck.Composite() && got == armor) {
		return nil
	}
	return fmt.Errorf("%w: the supplied %s checksum %q is not the combined %s %s", op.ErrBadDigest,
		req.Type.Info().Name, got, ck.Type.Info().Name, want)
}

// previouslyCompleted is RGWCompleteMultipart::check_previously_completed
// and the answer to its success
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6441-L6444
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6546-L6586 at v19.2.7;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7252-L7255
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7433-L7504 at v20.2.4):
// the key's current version (currentObject), read with no instance as the
// driver's currentVersion reads it and as radosgw's check reads the object
// before the completion gives it an instance, carries the ETag the parts
// make, and the retried
// completion is a 200 for that object with its stored ETag, which the check
// sets
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6584 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7472 at v20.2.4).
// The caller holds s.mu.
func (s *Store) previouslyCompleted(up *op.Upload, parts []op.CompletePart) (*op.PutResult, bool) {
	if up.Bucket == nil {
		return nil, false
	}
	b, ok := s.instances[up.Bucket.Info.Bucket.ID]
	if !ok {
		return nil, false
	}
	o := currentObject(b, up.Key.Name)
	if o == nil {
		return nil, false
	}
	want, err := op.CompleteETag(parts)
	if err != nil || o.state.ETag != want {
		return nil, false
	}
	return completeResult(o, storedChecksum(o.state.Attrs)), true
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
