package driver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // an S3 ETag is the MD5 of the data
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
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
// the tails while the body streams, compressed as the destination
// placement's storage class says, then the checks on what arrived (its
// length, the quota on the bytes received, the Content-MD5, the checksum),
// then the head write. A write that fails or loses a race deletes the tails
// it wrote, unless the head write timed out and may yet land, or landed. A
// checksum request of a type rgw-go does not compute is refused before
// anything is read or written (newChecksumSink).
//
// The head write is a version's on a bucket whose versioning is enabled, the
// key's instance the version's id, and the null version's over a key's OLH
// head, linked under the key's OLH at the epoch the class assigns, as every
// S3 PUT's olh_epoch is 0
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1271 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2117-L2143 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1344 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2280-L2306 at v20.2.4;
// put_versioned.go).
func (s *Store) PutObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error) {
	return s.putObject(ctx, rec, key, body, p, 0)
}

// putObject is PutObject linking a versioned write at epoch: 0 lets the
// class assign it, and one below the key's OLH entry's links the version
// non-current.
func (s *Store) putObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams, epoch uint64) (*op.PutResult, error) {
	ck, err := newChecksumSink(p.Checksum, p.Size)
	if err != nil {
		return nil, fmt.Errorf("putting %s/%s: %w", rec.Info.Bucket.Name, key.Name, err)
	}
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
		client: true, etag: p.ETag, contentMD5: p.ContentMD5, checksum: ck, compression: s.compressionType(l.tailRule),
		olhEpoch: &epoch,
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
	// checksum, when set, computes the plaintext's checksum, which is
	// checked after the Content-MD5 and stored as user.rgw.cksum.
	checksum *checksumSink
	// compression is the destination's compression type, which compresses
	// the body unless it is empty or none (storeFor). encrypted marks a
	// body an encryption filter stores, which is compressed only where the
	// zonegroup supports compress-encrypted.
	compression string
	encrypted   bool
	// incompressible hints every data write INCOMPRESSIBLE though no stage
	// compresses the body: a copy of bytes another compressor made.
	incompressible bool
	// olhEpoch is the head write's (headWrite).
	olhEpoch *uint64
	// expect, when set, is the head the write may land on alone, as the
	// caller read it (headWrite.expect).
	expect *op.ObjectState
}

// putStream writes body as AtomicObjectProcessor does, after its prepare,
// which planPut is: the body's plaintext stage, the stored stage made from
// it, whose bytes the tails stream and the head keeps, and the head through
// writeMeta. The body's length, the quota, the Content-MD5 and the checksum
// are checked on the plaintext, in RGWPutObj::execute's order
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4662-L4790 at v20.2.4),
// and the checksum's record joins the head's attrs. The manifest and the
// index entry take the stored size. A write the stored stage compressed
// stores its user.rgw.compression. The index entry's accounted size is the
// orig_size of attrs' user.rgw.compression when its type is not none, as
// copy_obj_data passes it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5098-L5108 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5368-L5379 at v20.2.4),
// and the plaintext's length otherwise, as RGWPutObj::execute passes
// s->obj_size.
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
	if w.checksum != nil {
		sinks = append(sinks, w.checksum)
	}
	limit := uint64(math.MaxUint64)
	if w.client {
		limit = s.w.opts.maxPutSize
	}
	plain := newPlaintext(body, limit, s.w.opts.chunkSize, sinks...)
	stored := s.storeFor(ctx, plain, w.size, w.compression, w.encrypted)
	incompressible := stored.incompressible || w.incompressible
	tw.incompressible = incompressible
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
	var sum checksumResult
	if err == nil && w.checksum != nil {
		sum, err = w.checksum.finish(s.release)
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
	if ci, ok := stored.compression(); ok {
		attrs[meta.AttrCompression] = encodeAt(ci, s.release)
	}
	if sum.attr != nil {
		attrs[meta.AttrCksum] = sum.attr
	}
	accounted := plain.n
	if compressed {
		accounted = orig
	}
	hw := &headWrite{
		rec: rec, key: key, tag: w.tag, data: data, manifest: &m, attrs: attrs, mtime: w.mtime,
		ifMatch: w.ifMatch, ifNoneMatch: w.ifNoneMatch, create: true, modifyTail: true, size: size, accountedSize: accounted,
		incompressible: incompressible, olhEpoch: w.olhEpoch, expect: w.expect,
	}
	res, err := s.writeMeta(ctx, hw, s.newIndexOp(rec, key, w.tag))
	if err != nil {
		// A head write that timed out may yet land on these tails, so they
		// stay, as writer.clear_written leaves them
		// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L395-L401).
		// A versioned write's head write that landed names them whatever its
		// completion or its link answered, so they stay too, where radosgw
		// deletes them (docs/ceph-upstream-bugs.md, "radosgw's versioned
		// PutObject deletes its tails when its link fails"); a removal of
		// the version sends them to the GC.
		if !res.written && !errors.Is(err, radosclient.ErrTimedOut) {
			tw.discard()
		}
		return nil, err
	}
	if res.canceled {
		tw.discard()
	}
	return &op.PutResult{ETag: etag, Size: size, Mtime: res.mtime, Epoch: res.epoch, Checksum: sum.cksum}, nil
}

// checksumSink is RGWPutObj_Cksum
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L157-L215 at v20.2.4)
// as a plaintext sink: the digest of the body as the client sent it, before
// compression, verified and recorded once the body is read. Squid's radosgw
// has no checksum filter; rgw-go computes one on both releases, the record
// being an attr of its own.
type checksumSink struct {
	req *op.ChecksumRequest
	h   hash.Hash
	// batchSize is the size of the buffer that gathers a CRC64NVME digest's
	// Writes (crc64Batch), 0 for none; batch is that buffer, made on the
	// first Write, so a body never written allocates none.
	batchSize int
	batch     *bufio.Writer
}

// crc64Batch is the least a CRC64NVME digest is written at a time, but for
// the body's last bytes. For a polynomial other than ISO and ECMA, hash/crc64
// builds its 16 KiB slicing-by-8 table anew on every Write of 2048 bytes or
// more, and runs a byte at a time below that (go1.27.3
// src/hash/crc64/crc64.go:141-178), while a request body arrives in TLS
// records of 16 KiB or less. Unbatched, a 4 MiB PUT read in 1 KiB pieces
// took 2.1 times as long as one without a checksum; batched, every piece
// size costs what 128 KiB pieces do, about 1.3 times
// (BenchmarkPutObjectChecksum). A body of a known length shorter than the
// batch takes a buffer of its length, and one shorter than crc64Slicing
// none, as it is hashed a byte at a time however it is written.
const crc64Batch = 128 << 10

// crc64Slicing is the least Write hash/crc64 slices for a custom polynomial.
const crc64Slicing = 2048

// newChecksumSink is the sink of req for a body of size bytes, -1 when
// unknown, and nil for no request. A type that is not cksum.Type.Writable,
// which cksum.NewHash refuses, is InvalidArgument, so no caller that skipped
// the op's parsing stores a type a v20.2.4 radosgw would look up past the
// end of its table
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L88-L99 at v20.2.4).
func newChecksumSink(req *op.ChecksumRequest, size int64) (*checksumSink, error) {
	if req == nil {
		return nil, nil
	}
	h, err := cksum.NewHash(req.Type)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", op.ErrInvalidArgument, err)
	}
	c := &checksumSink{req: req, h: h}
	switch {
	case req.Type != cksum.CRC64NVME:
	case size < 0 || size >= crc64Batch:
		c.batchSize = crc64Batch
	case size >= crc64Slicing:
		c.batchSize = int(size)
	}
	return c, nil
}

func (c *checksumSink) Write(p []byte) (int, error) {
	if c.batchSize == 0 {
		return c.h.Write(p)
	}
	if c.batch == nil {
		c.batch = bufio.NewWriterSize(c.h, c.batchSize)
	}
	return c.batch.Write(p)
}

// checksumResult is a write's checksum and the user.rgw.cksum that stores it,
// both nil for a write without one.
type checksumResult struct {
	cksum *cksum.Cksum
	attr  []byte
}

// finish is the checksum of the plaintext the sink took, with the request's
// flags, and its encoded record, once the body is read: Expected is asked
// for its value only then, as an aws-chunked trailer carries it after the
// body. A value that is not the checksum's armor, compared as the strings
// RGWPutObj_Cksum::verify compares
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L202-L211 at v20.2.4),
// is BadDigest. A request that supplies no value stores the checksum
// unverified, as radosgw's does
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4764-L4776 at v20.2.4).
// The record is checked with Cksum.Valid before it is encoded, so a record
// radosgw's decoder would take unchecked fails the write rather than panic.
func (c *checksumSink) finish(rel denc.Release) (checksumResult, error) {
	if c.batch != nil {
		if err := c.batch.Flush(); err != nil {
			return checksumResult{}, fmt.Errorf("%w: hashing the body: %w", op.ErrInternalError, err)
		}
	}
	sum := cksum.Finish(c.req.Type, c.h, c.req.Flags)
	if c.req.Expected != nil {
		if want, ok := c.req.Expected(); ok && want != sum.Armor() {
			return checksumResult{}, fmt.Errorf("%w: the %s checksum of the body is %s, not %q",
				op.ErrBadDigest, c.req.Type.Info().Name, sum.Armor(), want)
		}
	}
	if err := sum.Valid(); err != nil {
		return checksumResult{}, fmt.Errorf("%w: the computed checksum: %w", op.ErrInternalError, err)
	}
	return checksumResult{cksum: &sum, attr: encodeAt(sum, rel)}, nil
}

// plaintext is a write's first stage: the body as RGWPutObj::execute's loop
// reads it, before any filter makes a stored form of it. Every byte goes to
// the sinks, which compute what is taken over the data as the client sent it
// (the MD5 and the checksum), and n counts them.
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
// write hints as such. comp is the compressor that makes them, nil when the
// plaintext is stored as it is.
type storedStage struct {
	io.Reader
	incompressible bool
	comp           *compressor
}

// storeAsIs is the stored stage of a write that stores its plaintext as it
// is.
func storeAsIs(p *plaintext) storedStage { return storedStage{Reader: p} }

// compression is the user.rgw.compression the stage's write stores, once
// its plaintext is read, and false when it stores none.
func (st storedStage) compression() (meta.CompressionInfo, bool) {
	if st.comp == nil {
		return meta.CompressionInfo{}, false
	}
	return st.comp.info()
}

// featureCompressEncrypted is rgw::zone_features::compress_encrypted
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_zone_features.h#L17 at v19.2.7
// and v20.2.4).
const featureCompressEncrypted = "compress-encrypted"

// compressionType is RGWZoneParams::get_compression_type
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_zone.cc#L356-L365 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_zone.cc#L356-L365 at v20.2.4)
// for a write's destination placement rule: its storage class's
// compression_type, and none for a class without one, for a class or a
// placement the zone lacks, and for a rule without a name, which radosgw
// looks up by that empty name.
func (s *Store) compressionType(rule meta.PlacementRule) string {
	if rule.Name == "" {
		return compression.None
	}
	p, err := s.Placement(rule)
	if err != nil || p.Compression == "" {
		return compression.None
	}
	return p.Compression
}

// storeFor is the stored stage of a write of plain into a placement whose
// compression type is typ, as RGWPutObj::execute chooses its filters
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4341-L4371 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4553-L4587 at v20.2.4).
// None stores the plaintext as it is, and so does an encrypted write unless
// the zonegroup supports compress-encrypted. A type rgw-go has no encoder for,
// random among them, is stored as it is too and logged the first time,
// where radosgw compresses with the plugin it loads or, for random, picks
// (docs/exclusions.md, "random and a codec rgw-go has no encoder for store
// the object uncompressed"). Any other type compresses, its data hinted
// incompressible from the start, as set_compressed marks the object when the
// plugin loads: an object whose first block fails and is stored as it is
// keeps the hint. declared is the plaintext's declared length, -1 when
// unknown.
func (s *Store) storeFor(ctx context.Context, plain *plaintext, declared int64, typ string, encrypted bool) storedStage {
	if typ == "" || typ == compression.None {
		return storeAsIs(plain)
	}
	if encrypted && !slices.Contains(s.zone.ZoneGroup.EnabledFeatures, featureCompressEncrypted) {
		return storeAsIs(plain)
	}
	enc, err := s.w.newEncoder(typ, s.w.opts.codec)
	switch {
	case errors.Is(err, compression.ErrUnknownCodec):
		if _, logged := s.w.unloadable.LoadOrStore(typ, struct{}{}); !logged {
			slog.WarnContext(ctx, "no encoder for the compression type; its objects are stored uncompressed",
				slog.String("compression_type", typ))
		}
		return storeAsIs(plain)
	case err != nil:
		// The plugin loads and refuses every compress, so each object's first
		// block fails.
		enc = refusingEncoder{err: err}
	}
	c := &compressor{plain: plain, enc: enc, typ: typ, chunk: s.w.opts.chunkSize, declared: declared}
	return storedStage{Reader: c, incompressible: true, comp: c}
}

// refusingEncoder is the encoder of options its codec refuses: every block
// fails with err.
type refusingEncoder struct{ err error }

func (r refusingEncoder) Encode(_, _ []byte) (block []byte, message *int32, err error) {
	return nil, nil, r.err
}

// compressor is RGWPutObj_Compress
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_compression.cc#L44-L88 at v19.2.7
// and v20.2.4) over a write's plaintext, which it reads as get_data does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1073-L1106 at v19.2.7
// and v20.2.4): blocks of rgw_max_chunk_size, or of the declared length's
// rest when that is shorter, each encoded in one call. A block records its
// plaintext offset, the end of the block before it in the stored bytes, and
// its stored length. A first block that fails to encode has the whole
// plaintext stored as it is; a later one fails the write with UnknownError,
// radosgw's -EIO. Its two block buffers, one of plaintext and one encoded,
// are held until the plaintext ends and take no share of the body budget, as
// the head takes none.
type compressor struct {
	plain    *plaintext
	enc      compression.Encoder
	typ      string
	chunk    uint64
	declared int64

	src, dst []byte // the plaintext block buffer, the encoded block buffer
	out      []byte // the stored bytes being read: an encoded block, or src
	pos      int    // how much of out was read
	ofs      uint64 // the plaintext offset of the next block
	stored   uint64 // the stored bytes so far
	blocks   []meta.CompressionBlock
	message  *int32
	raw      bool // the first block failed: the plaintext is stored as it is
	ended    bool // the plaintext has ended
	err      error
}

func (c *compressor) Read(p []byte) (int, error) {
	for c.pos == len(c.out) {
		if c.err != nil {
			return 0, c.err
		}
		c.err = c.next()
	}
	n := copy(p, c.out[c.pos:])
	c.pos += n
	return n, nil
}

// next makes out the stored form of the next block, or ends the stream with
// io.EOF.
func (c *compressor) next() error {
	if c.ended {
		// The last block is read: the drain and the head write that follow
		// need neither buffer.
		c.src, c.dst, c.out, c.pos = nil, nil, nil, 0
		return io.EOF
	}
	block, ended, err := c.readBlock()
	if err != nil {
		return err
	}
	c.ended = ended
	c.out, c.pos = block, 0
	if len(block) == 0 || c.raw {
		c.ofs += uint64(len(block))
		return nil
	}
	enc, message, err := c.enc.Encode(c.dst[:0], block)
	switch {
	case err == nil:
		c.blocks = append(c.blocks, meta.CompressionBlock{OldOfs: c.ofs, NewOfs: c.stored, Len: uint64(len(enc))})
		c.stored += uint64(len(enc))
		if message != nil {
			c.message = message
		}
		c.dst, c.out = enc, enc
	case c.ofs > 0:
		return fmt.Errorf("%w: compressing the %s block at %d: %w", op.ErrUnknown, c.typ, c.ofs, err)
	default:
		slog.Debug("the first block does not compress; storing the object uncompressed",
			slog.String("compression_type", c.typ), slog.Any("error", err))
		c.raw = true
	}
	c.ofs += uint64(len(block))
	return nil
}

// readBlock reads the next block of plaintext into src, reporting that the
// plaintext ended with it. Once the declared length is read, a one-byte
// probe tells the plaintext's end from a body that runs on past it, which is
// read on in chunks for the length check to refuse.
func (c *compressor) readBlock() (block []byte, ended bool, err error) {
	want := c.chunk
	if c.declared >= 0 {
		rest := uint64(c.declared) - min(c.ofs, uint64(c.declared))
		if rest == 0 {
			var probe [1]byte
			k, done, perr := readPiece(c.plain, probe[:])
			if perr != nil || k == 0 {
				return nil, done, perr
			}
			c.declared = -1
			buf := c.buffer(c.chunk)
			buf[0] = probe[0]
			n, done, perr := readPiece(c.plain, buf[1:])
			return buf[:1+n], done, perr
		}
		want = min(want, rest)
	}
	buf := c.buffer(want)
	n, ended, err := readPiece(c.plain, buf)
	return buf[:n], ended, err
}

// buffer is src with room for n bytes.
func (c *compressor) buffer(n uint64) []byte {
	if uint64(cap(c.src)) < n {
		c.src = make([]byte, n)
	}
	return c.src[:n]
}

// info is the RGWCompressionInfo RGWPutObj::execute stores once the
// plaintext is read
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4452-L4469 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4684-L4701 at v20.2.4):
// the type, the plaintext's size, the last message the encoder gave and the
// blocks; false when no block compressed, for an empty body or one whose
// first block failed.
func (c *compressor) info() (meta.CompressionInfo, bool) {
	if c.raw || len(c.blocks) == 0 {
		return meta.CompressionInfo{}, false
	}
	return meta.CompressionInfo{Type: c.typ, OrigSize: c.plain.n, CompressorMessage: c.message, Blocks: c.blocks}, true
}

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
