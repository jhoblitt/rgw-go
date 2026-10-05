package driver

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is the MD5 of the data
	"encoding/hex"
	"errors"
	"io"
	"maps"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// PutObject implements op.ObjectStore as RGWPutObj::execute and
// AtomicObjectProcessor write an object (rgw_op.cc:4237-4565 and
// driver/rados/rgw_putobj_processor.cc:268-411 at v19.2.6, rgw_op.cc
// :4469-4800 at v20.2.4): the tails while the body streams, then the checks
// on what arrived (its length, the quota on the bytes received, the
// Content-MD5), then the head write. A write that fails or loses a race
// deletes the tails it wrote, unless the head write timed out and may yet
// land.
func (s *Store) PutObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error) {
	tag := p.Tag
	if tag == "" {
		tag = s.w.randTag()
	}
	l, err := s.planPut(ctx, rec, key, p.StorageClass)
	if err != nil {
		return nil, err
	}
	// generator::create_begin names the tails "." and 31 random characters
	// and "_" (rgw_obj_manifest.cc).
	m := meta.NewTrivialManifest(meta.Obj{Bucket: rec.Info.Bucket, Key: key}, l.headRule, l.tailRule, "."+s.w.rand(31)+"_", l.maxHead, l.stripe)
	tw := s.newTailWriter(&m, l)
	h := md5.New() //nolint:gosec // an S3 ETag is the MD5 of the data
	data, size, err := tw.consume(ctx, body, p.Size, h)
	// radosgw finishes a PUT whose body it holds whether or not its client
	// stays: nothing after get_data's loop watches the client, the second
	// check_quota included. A head write abandoned to a canceled context
	// could also land after its tails were deleted.
	ctx = context.WithoutCancel(ctx)
	if err == nil && p.Size >= 0 && uint64(p.Size) != size {
		err = op.ErrRequestTimeout
	}
	if err == nil {
		err = s.CheckQuota(ctx, rec, rec.Info.Owner, int64(size), 1) //nolint:gosec // an object size is far below 2^63
	}
	sum := h.Sum(nil)
	if err == nil && p.ContentMD5 != nil && !bytes.Equal(sum, p.ContentMD5) {
		err = op.ErrBadDigest
	}
	if err == nil {
		err = tw.drain()
	}
	if err != nil {
		tw.discard()
		return nil, err
	}
	etag := p.ETag
	if etag == "" {
		etag = hex.EncodeToString(sum)
	}
	m.SetObjSize(size)
	attrs := maps.Clone(p.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrETag] = []byte(etag)
	hw := &headWrite{
		rec: rec, key: key, tag: tag, data: data, manifest: &m, attrs: attrs, mtime: p.Mtime,
		ifMatch: p.IfMatch, ifNoneMatch: p.IfNoneMatch, create: true, modifyTail: true, size: size, accountedSize: size,
	}
	res, err := s.writeMeta(ctx, hw, s.newIndexOp(rec, key, tag))
	if err != nil {
		// A head write that timed out may yet land on these tails, so they
		// stay, as writer.clear_written leaves them (rgw_putobj_processor.cc:395-401).
		if !errors.Is(err, radosclient.ErrTimedOut) {
			tw.discard()
		}
		return nil, err
	}
	if res.canceled {
		tw.discard()
	}
	return &op.PutResult{ETag: etag, Size: size, Mtime: res.mtime, Epoch: res.epoch}, nil
}
