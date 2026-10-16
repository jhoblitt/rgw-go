package compression

import (
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
)

// EncoderOptions are radosgw's compressor options compressor_zlib_level,
// compressor_zlib_winsize and compressor_zstd_level, whose defaults are 5,
// -15 and 1
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/global.yaml.in#L768-L789 at v19.2.7
// and v20.2.4). NewEncoder reads only its codec's.
type EncoderOptions struct {
	ZlibLevel, ZlibWinsize, ZstdLevel int
}

// Encoder compresses one block — one compression_block of an object's
// RGWCompressionInfo, which a GET decodes on its own — in the framing
// src/compressor gives its codec, so that radosgw decodes it. It writes the
// block into dst's backing array, grown when it does not fit, and returns it
// with the compressor_message to store beside it: zlib's window bits, nil for
// the other codecs, which set none. dst and src must not overlap.
type Encoder interface {
	Encode(dst, src []byte) (block []byte, message *int32, err error)
}

// NewEncoder returns the encoder for codec, or ErrUnknownCodec ("none"
// included). It refuses a compressor_zlib_level deflateInit2 refuses, and a
// compressor_zlib_winsize of 0 to 7, which names no framing; on both radosgw's
// compress fails and RGWPutObj_Compress stores the object uncompressed
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_compression.cc#L54-L64 at v19.2.7
// and v20.2.4). Encoders are safe for concurrent use.
func NewEncoder(codec string, o EncoderOptions) (Encoder, error) {
	switch codec {
	case Zlib:
		return newZlibEncoder(o.ZlibLevel, o.ZlibWinsize)
	case Snappy:
		return snappyEncoder{}, nil
	case Zstd:
		return newZstdEncoder(o.ZstdLevel)
	case LZ4:
		return lz4Encoder{}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownCodec, codec)
}

// tooLong is a block longer than its codec's framing records.
func tooLong(codec string, n int) error {
	return fmt.Errorf("compression: %s cannot frame a block of %d bytes", codec, n)
}

// zlibMaxWBits is zlib's MAX_WBITS: a 32 KiB window, the farthest any of Go's
// deflate streams reaches back.
const zlibMaxWBits = 15

// zlibEncoder is ZlibCompressor::zlib_compress: the prefix byte 0, then one
// deflate stream at compressor_zlib_level
// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zlib/ZlibCompressor.cc#L82-L141 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zlib/ZlibCompressor.cc#L94-L153 at v20.2.4).
// zlib_compress stores compressor_zlib_winsize as the message, which radosgw's
// inflateInit2 takes back. Go's deflate always uses a 32 KiB window, which a
// narrower window bits value cannot inflate, so the message states the
// option's framing at 15 bits: -15, 15 or 31.
type zlibEncoder struct {
	w       wrapper
	level   int
	message int32
	pool    *sync.Pool
}

// deflatePools hold idle deflate writers by wrapper and level+1, each with the
// match tables of its level, which a block would otherwise allocate.
var deflatePools [gzipWrapper + 1][flate.BestCompression + 2]sync.Pool

func newZlibEncoder(level, winsize int) (Encoder, error) {
	// deflateInit2 takes Z_DEFAULT_COMPRESSION (-1) and 0 to 9. Go's deflate
	// also takes -2, Huffman only, which zlib refuses.
	if level < flate.DefaultCompression || level > flate.BestCompression {
		return nil, fmt.Errorf("compression: compressor_zlib_level %d is not a zlib level", level)
	}
	e := zlibEncoder{level: level}
	switch {
	case winsize < 0:
		e.w, e.message = rawDeflate, -zlibMaxWBits
	case winsize >= 8 && winsize <= 15:
		e.w, e.message = zlibWrapper, zlibMaxWBits
	case winsize > 15:
		e.w, e.message = gzipWrapper, 16+zlibMaxWBits
	default:
		return nil, fmt.Errorf("compression: compressor_zlib_winsize %d is no window deflateInit2 takes", winsize)
	}
	e.pool = &deflatePools[e.w][level+1]
	return e, nil
}

// deflateWriter is the reuse Go's deflate, zlib and gzip writers offer.
type deflateWriter interface {
	io.WriteCloser
	Reset(w io.Writer)
}

// deflater is a pooled deflate writer and the block it appends to, which is
// released before the deflater returns to its pool.
type deflater struct {
	w   deflateWriter
	out []byte
}

func (d *deflater) Write(p []byte) (int, error) {
	d.out = append(d.out, p...)
	return len(p), nil
}

func (z zlibEncoder) Encode(dst, src []byte) (block []byte, message *int32, err error) {
	d, err := z.deflater()
	if err != nil {
		return nil, nil, err
	}
	d.out = dst[:0]
	d.out = append(d.out, 0)
	d.w.Reset(d)
	if _, err = d.w.Write(src); err != nil {
		return nil, nil, fmt.Errorf("deflating a zlib block: %w", err)
	}
	if err = d.w.Close(); err != nil {
		return nil, nil, fmt.Errorf("deflating a zlib block: %w", err)
	}
	block = d.out
	d.out = nil
	z.pool.Put(d)
	return block, new(z.message), nil
}

func (z zlibEncoder) deflater() (*deflater, error) {
	if d, ok := z.pool.Get().(*deflater); ok {
		return d, nil
	}
	d := new(deflater)
	var err error
	switch z.w {
	case rawDeflate:
		d.w, err = flate.NewWriter(d, z.level)
	case zlibWrapper:
		d.w, err = zlib.NewWriterLevel(d, z.level)
	default:
		d.w, err = gzip.NewWriterLevel(d, z.level)
	}
	if err != nil {
		return nil, fmt.Errorf("creating a zlib encoder: %w", err)
	}
	return d, nil
}

// zstdDefaultLevel is ZSTD_CLEVEL_DEFAULT, the level libzstd compresses at
// when compressor_zstd_level is 0.
const zstdDefaultLevel = 3

// zstdEncoder is ZstdCompressor::compress: the plaintext length as a
// little-endian uint32, then one frame at compressor_zstd_level
// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zstd/ZstdCompressor.h#L29-L62 at v19.2.7
// and v20.2.4). Like libzstd's, the frame states its content size and carries
// no checksum.
type zstdEncoder struct{ e *zstd.Encoder }

// sharedZstdEncoders hold one encoder per klauspost level, made on first use.
// EncodeAll is safe for concurrent use, and an encoder keeps match tables for
// each call it runs at once, too many to build per object. WithZeroFrames
// writes a frame for an empty input too.
var sharedZstdEncoders = map[zstd.EncoderLevel]func() (*zstd.Encoder, error){
	zstd.SpeedFastest:           zstdEncoderAt(zstd.SpeedFastest),
	zstd.SpeedDefault:           zstdEncoderAt(zstd.SpeedDefault),
	zstd.SpeedBetterCompression: zstdEncoderAt(zstd.SpeedBetterCompression),
	zstd.SpeedBestCompression:   zstdEncoderAt(zstd.SpeedBestCompression),
}

func zstdEncoderAt(level zstd.EncoderLevel) func() (*zstd.Encoder, error) {
	return sync.OnceValues(func() (*zstd.Encoder, error) {
		return zstd.NewWriter(nil, zstd.WithEncoderLevel(level), zstd.WithEncoderCRC(false), zstd.WithZeroFrames(true))
	})
}

func newZstdEncoder(level int) (Encoder, error) {
	if level == 0 {
		level = zstdDefaultLevel
	}
	e, err := sharedZstdEncoders[zstd.EncoderLevelFromZstd(level)]()
	if err != nil {
		return nil, fmt.Errorf("creating the zstd encoder: %w", err)
	}
	return zstdEncoder{e: e}, nil
}

func (z zstdEncoder) Encode(dst, src []byte) (block []byte, message *int32, err error) {
	if uint64(len(src)) > math.MaxUint32 {
		return nil, nil, tooLong("zstd", len(src))
	}
	out := binary.LittleEndian.AppendUint32(dst[:0], uint32(len(src))) //nolint:gosec // at most MaxUint32, checked above
	return z.e.EncodeAll(src, out), nil, nil
}

// lz4Encoder is LZ4Compressor::compress, which makes its input contiguous
// first: a count of 1, the (origin_len, compressed_len) pair, then one LZ4
// block
// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/lz4/LZ4Compressor.cc#L36-L85 at v19.2.7
// and v20.2.4).
type lz4Encoder struct{}

// lz4MaxInput is LZ4_MAX_INPUT_SIZE, past which LZ4_compress_fast_continue,
// and so radosgw's compress, fails.
const lz4MaxInput = 0x7E000000

// lz4Header is the count and the one pair.
const lz4Header = 12

// lz4Compressors hold idle compressors, each with its 128 KiB hash table.
var lz4Compressors sync.Pool

func (lz4Encoder) Encode(dst, src []byte) (block []byte, message *int32, err error) {
	if len(src) > lz4MaxInput {
		return nil, nil, tooLong("lz4", len(src))
	}
	n := lz4Header + lz4.CompressBlockBound(len(src))
	out := grow(dst, n)[:n]
	c, ok := lz4Compressors.Get().(*lz4.Compressor)
	if !ok {
		c = new(lz4.Compressor)
	}
	// A destination of CompressBlockBound always takes the block.
	size, err := c.CompressBlock(src, out[lz4Header:])
	lz4Compressors.Put(c)
	if err != nil {
		return nil, nil, fmt.Errorf("compressing an lz4 block: %w", err)
	}
	binary.LittleEndian.PutUint32(out[0:], 1)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(src))) //nolint:gosec // at most lz4MaxInput, checked above
	binary.LittleEndian.PutUint32(out[8:], uint32(size))     //nolint:gosec // at most CompressBlockBound(lz4MaxInput)
	return out[:lz4Header+size], nil, nil
}

// snappyEncoder is SnappyCompressor::compress: snappy's raw block format, with
// no framing of Ceph's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/snappy/SnappyCompressor.h#L63-L71 at v19.2.7
// and v20.2.4).
type snappyEncoder struct{}

func (snappyEncoder) Encode(dst, src []byte) (block []byte, message *int32, err error) {
	// snappy.Encode panics on a block whose length its header cannot hold.
	if snappy.MaxEncodedLen(len(src)) < 0 {
		return nil, nil, tooLong("snappy", len(src))
	}
	return snappy.Encode(dst, src), nil, nil
}
