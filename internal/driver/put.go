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
// AtomicObjectProcessor write an object
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4237-L4565
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L268-L411 at v19.2.6,
// rgw_op.cc
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4469-L4800 at v20.2.4):
// the tails while the body streams, then the checks on what arrived (its
// length, the quota on the bytes received, the Content-MD5), then the head
// write. A write that fails or loses a race deletes the tails it wrote,
// unless the head write timed out and may yet land.
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
// which planPut is: the body's plaintext stage, the stored stage made from
// it, whose bytes the tails stream and the head keeps, and the head through
// writeMeta. The body's length, the quota and the Content-MD5 are checked on
// the plaintext. The index entry's accounted size is the orig_size of attrs'
// user.rgw.compression when its type is not none, as copy_obj_data passes
// it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5098-L5108 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5368-L5379 at v20.2.4),
// and the plaintext's length otherwise.
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
		h     hash.Hash
		sinks []io.Writer
	)
	if w.contentMD5 != nil || w.client && w.etag == "" {
		h = md5.New() //nolint:gosec // an S3 ETag is the MD5 of the data
		sinks = append(sinks, h)
	}
	limit := uint64(math.MaxUint64)
	if w.client {
		limit = s.w.opts.maxPutSize
	}
	plain := newPlaintext(body, limit, s.w.opts.chunkSize, sinks...)
	stored := storeAsIs(plain)
	tw.incompressible = stored.incompressible
	data, size, err := tw.consume(ctx, stored, w.size)
	// radosgw finishes a PUT whose body it holds whether or not its client
	// stays: nothing after get_data's loop watches the client, the second
	// check_quota included. A head write abandoned to a canceled context
	// could also land after its tails were deleted.
	ctx = context.WithoutCancel(ctx)
	if err == nil && w.size >= 0 && uint64(w.size) != plain.n {
		err = op.ErrRequestTimeout
	}
	if err == nil && w.client {
		err = s.CheckQuota(ctx, rec, rec.Info.Owner, int64(plain.n), 1) //nolint:gosec // an object size is far below 2^63
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
	accounted := plain.n
	if compressed {
		accounted = orig
	}
	hw := &headWrite{
		rec: rec, key: key, tag: w.tag, data: data, manifest: &m, attrs: attrs, mtime: w.mtime,
		ifMatch: w.ifMatch, ifNoneMatch: w.ifNoneMatch, create: true, modifyTail: true, size: size, accountedSize: accounted,
		incompressible: stored.incompressible,
	}
	res, err := s.writeMeta(ctx, hw, s.newIndexOp(rec, key, w.tag))
	if err != nil {
		// A head write that timed out may yet land on these tails, so they
		// stay, as writer.clear_written leaves them
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L395-L401).
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

// plaintext is a write's first stage: the body as RGWPutObj::execute's loop
// reads it, before any filter makes a stored form of it. Every byte goes to
// the sinks, which compute what is taken over the data as the client sent it
// (the MD5), and n counts them.
//
// A body past max is EntityTooLarge in get_data's order
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1073-L1106 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1073-L1106 at v20.2.4):
// get_data reads the body in chunks of rgw_max_chunk_size, each until it is
// whole or the body ends, returns recv_body's error before it checks the
// size, and refuses the chunk that carries the body past rgw_max_put_size
// once it is read. So the stage, once the body passes max, reads on to the
// end of that chunk, at most one chunk more, and a body error within it wins
// over EntityTooLarge. Only a body without a Content-Length gets that far, as
// verify_params refuses a declared length over the limit
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1054-L1064 at v19.2.7
// and v20.2.4).
type plaintext struct {
	r     io.Reader
	sink  io.Writer
	max   uint64
	chunk uint64
	n     uint64
	err   error // the refusal or body error that ended the stage
}

// newPlaintext is the plaintext stage of body, held to limit in get_data's
// reads of chunk bytes.
func newPlaintext(body io.Reader, limit, chunk uint64, sinks ...io.Writer) *plaintext {
	return &plaintext{r: body, sink: io.MultiWriter(sinks...), max: limit, chunk: chunk}
}

func (p *plaintext) Read(b []byte) (int, error) {
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.r.Read(b)
	if n > 0 {
		if _, werr := p.sink.Write(b[:n]); werr != nil {
			return n, werr
		}
		p.n += uint64(n)
	}
	if p.n <= p.max {
		return n, err
	}
	// The end of the chunk that holds the first byte past max.
	end := p.max - p.max%p.chunk + p.chunk
	if err == nil && p.n < end {
		err = p.readTo(end)
	}
	if err == nil || errors.Is(err, io.EOF) || p.n >= end {
		err = op.ErrEntityTooLarge
	}
	p.err = err
	return n, err
}

// readTo reads the body on, discarding it, until n reaches end or the body
// ends or fails, and returns the body's error.
func (p *plaintext) readTo(end uint64) error {
	buf := make([]byte, min(end-p.n, 64<<10))
	for p.n < end {
		k, err := p.r.Read(buf[:min(end-p.n, uint64(len(buf)))])
		p.n += uint64(k) //nolint:gosec // a read count is never negative
		if err != nil {
			return err
		}
	}
	return nil
}

// storedStage is a write's second stage, the bytes it stores made from its
// plaintext: the stream the tail writer cuts the head and the stripes from.
// incompressible marks bytes radosgw's compressor made, which every data
// write hints as such.
type storedStage struct {
	io.Reader
	incompressible bool
}

// storeAsIs is the stored stage of a write that stores its plaintext as it
// is.
func storeAsIs(p *plaintext) storedStage { return storedStage{Reader: p} }

// origSize is rgw_compression_info_from_attrset
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L10-L40 at v19.2.6
// and v20.2.4) over attrs: the orig_size of a user.rgw.compression whose type
// is not none, and compressed false without one. An attr that does not
// decode, or that lists no blocks, is radosgw's -EIO.
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
