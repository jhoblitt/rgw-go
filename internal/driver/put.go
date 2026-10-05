package driver

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is the MD5 of the data
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"math"
	"time"

	"github.com/jhoblitt/rgw-go/internal/compression"
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
	return s.putStream(ctx, rec, key, body, l, streamWrite{
		tag: tag, attrs: p.Attrs, size: p.Size, mtime: p.Mtime, ifMatch: p.IfMatch, ifNoneMatch: p.IfNoneMatch,
		client: true, etag: p.ETag, contentMD5: p.ContentMD5,
	})
}

// streamWrite is what putStream writes besides the body.
type streamWrite struct {
	tag   string
	attrs map[string][]byte
	// size is the body's declared length, -1 when unknown; a body that
	// ends at another length is RequestTimeout.
	size                 int64
	mtime                time.Time // zero means now
	ifMatch, ifNoneMatch string
	// client marks a client's body, which RGWPutObj::execute checks as it
	// arrives: rgw_max_put_size, the quota on the bytes received and
	// Content-MD5 apply, and the stored ETag is etag, or the body's MD5
	// when etag is empty. Without it the body is copy_obj_data's, which
	// checks none of them, and attrs' own user.rgw.etag is stored.
	client     bool
	etag       string
	contentMD5 []byte
}

// putStream writes body as AtomicObjectProcessor does, after its prepare,
// which planPut is: the tails streamed from body, the head through
// writeMeta. The index entry's accounted size is the orig_size of attrs'
// user.rgw.compression when its type is not none, as copy_obj_data passes
// it (rgw_rados.cc:5098-5108 at v19.2.6, :5368-5379 at v20.2.4), and the
// bytes streamed otherwise.
func (s *Store) putStream(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, l layout, w streamWrite) (*op.PutResult, error) {
	orig, compressed, err := origSize(w.attrs)
	if err != nil {
		return nil, fmt.Errorf("%w: the compression info for %s/%s: %w", op.ErrUnknown, rec.Info.Bucket.Name, key.Name, err)
	}
	// generator::create_begin names the tails "." and 31 random characters
	// and "_" (rgw_obj_manifest.cc).
	m := meta.NewTrivialManifest(meta.Obj{Bucket: rec.Info.Bucket, Key: key}, l.headRule, l.tailRule, "."+s.w.rand(31)+"_", l.maxHead, l.stripe)
	tw := s.newTailWriter(&m, l)
	var (
		h    hash.Hash
		sink = io.Discard
	)
	if !w.client {
		tw.maxSize = math.MaxUint64
	}
	if w.contentMD5 != nil || w.client && w.etag == "" {
		h = md5.New() //nolint:gosec // an S3 ETag is the MD5 of the data
		sink = h
	}
	data, size, err := tw.consume(ctx, body, w.size, sink)
	// radosgw finishes a PUT whose body it holds whether or not its client
	// stays: nothing after get_data's loop watches the client, the second
	// check_quota included. A head write abandoned to a canceled context
	// could also land after its tails were deleted.
	ctx = context.WithoutCancel(ctx)
	if err == nil && w.size >= 0 && uint64(w.size) != size {
		err = op.ErrRequestTimeout
	}
	if err == nil && w.client {
		err = s.CheckQuota(ctx, rec, rec.Info.Owner, int64(size), 1) //nolint:gosec // an object size is far below 2^63
	}
	if err == nil && w.contentMD5 != nil && !bytes.Equal(h.Sum(nil), w.contentMD5) {
		err = op.ErrBadDigest
	}
	if err == nil {
		err = tw.drain()
	}
	if err != nil {
		tw.discard()
		return nil, err
	}
	m.SetObjSize(size)
	attrs := maps.Clone(w.attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	etag := rgwBlStr(attrs[meta.AttrETag])
	if w.client {
		etag = w.etag
		if etag == "" {
			etag = hex.EncodeToString(h.Sum(nil))
		}
		attrs[meta.AttrETag] = []byte(etag)
	}
	accounted := size
	if compressed {
		accounted = orig
	}
	hw := &headWrite{
		rec: rec, key: key, tag: w.tag, data: data, manifest: &m, attrs: attrs, mtime: w.mtime,
		ifMatch: w.ifMatch, ifNoneMatch: w.ifNoneMatch, create: true, modifyTail: true, size: size, accountedSize: accounted,
	}
	res, err := s.writeMeta(ctx, hw, s.newIndexOp(rec, key, w.tag))
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

// origSize is rgw_compression_info_from_attrset (rgw_compression.cc:10-40 at
// v19.2.6 and v20.2.4) over attrs: the orig_size of a user.rgw.compression
// whose type is not none, and compressed false without one. An attr that does
// not decode, or that lists no blocks, is radosgw's -EIO.
func origSize(attrs map[string][]byte) (size uint64, compressed bool, err error) {
	b, ok := attrs[meta.AttrCompression]
	if !ok {
		return 0, false, nil
	}
	ci, err := decodeAttr(b, meta.DecodeCompressionInfo)
	if err != nil {
		return 0, false, err
	}
	if len(ci.Blocks) == 0 {
		return 0, false, errors.New("no compression blocks")
	}
	return ci.OrigSize, ci.Type != compression.None, nil
}
