package compression

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zlib"
)

// zlibDefaultWinSize is ZLIB_DEFAULT_WIN_SIZE, the window bits
// ZlibCompressor::decompress takes when the object has no compressor_message:
// raw deflate with a 32 KiB window.
const zlibDefaultWinSize = -15

// wrapper is the framing around each deflate stream that inflateInit2's window
// bits select.
type wrapper int

const (
	rawDeflate wrapper = iota
	zlibWrapper
	gzipWrapper
	autoWrapper // zlib or gzip, told apart by each member's magic
)

// inflateMode reads window bits as zlib's inflateReset2 does: below 0 raw
// deflate, to 15 the zlib wrapper, to 31 gzip, to 47 either, the low four bits
// the window, where 0 takes the zlib header's. It reports false for the values
// inflateInit2 refuses with Z_STREAM_ERROR.
func inflateMode(windowBits int32) (w wrapper, wbits int, ok bool) {
	bits := int(windowBits)
	switch {
	case bits < 0:
		w, wbits = rawDeflate, -bits
	case bits < 16:
		w, wbits = zlibWrapper, bits
	case bits < 32:
		w, wbits = gzipWrapper, bits-16
	case bits < 48:
		w, wbits = autoWrapper, bits-32
	default:
		return 0, 0, false
	}
	return w, wbits, wbits == 0 || wbits >= 8 && wbits <= 15
}

// zlibDecoder is ZlibCompressor::decompress: skip the prefix byte, then
// inflate with the stored window bits to the end of the block, starting a new
// stream (inflateReset) wherever one ends before the input does. QatAccel
// writes one gzip member per input buffer.
type zlibDecoder struct{}

func (zlibDecoder) Decode(dst, block []byte, message *int32) ([]byte, error) {
	if len(block) < 1 {
		return nil, fmt.Errorf("%w: zlib block shorter than its prefix byte", ErrCorrupt)
	}
	windowBits := int32(zlibDefaultWinSize)
	if message != nil {
		windowBits = *message
	}
	w, wbits, ok := inflateMode(windowBits)
	if !ok {
		return nil, fmt.Errorf("%w: zlib window bits %d", ErrCorrupt, windowBits)
	}
	// The prefix byte marks zlib (0) or ISA-L (1) as the compressor; both
	// write the same streams, and decompress never reads it.
	in := block[1:]
	out := grow(dst, 4*len(in))
	for len(in) > 0 {
		r := bytes.NewReader(in)
		m, err := openMember(r, in, w, wbits)
		if err != nil {
			return nil, corrupt("zlib", err)
		}
		if out, err = readAll(out, m); err != nil {
			return nil, corrupt("zlib", err)
		}
		in = in[len(in)-r.Len():]
	}
	return out, nil
}

// openMember starts the stream at the head of in, which r reads. Go's readers
// consume exactly one member from an io.ByteReader, leaving r at the next. For
// a zlib header it adds two checks inflate makes and Go's reader does not: a
// window no wider than the window bits allow, and no preset dictionary, for
// which inflate answers Z_NEED_DICT.
func openMember(r *bytes.Reader, in []byte, w wrapper, wbits int) (io.Reader, error) {
	if w == autoWrapper {
		w = zlibWrapper
		if len(in) >= 2 && in[0] == 0x1f && in[1] == 0x8b {
			w = gzipWrapper
		}
	}
	switch w {
	case rawDeflate:
		return flate.NewReader(r), nil
	case gzipWrapper:
		z, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		z.Multistream(false)
		return z, nil
	}
	if len(in) >= 2 {
		if size := int(in[0]>>4) + 8; wbits != 0 && size > wbits {
			return nil, fmt.Errorf("header window of %d bits exceeds %d", size, wbits)
		}
		if in[1]&0x20 != 0 {
			return nil, errors.New("header asks for a preset dictionary")
		}
	}
	return zlib.NewReader(r)
}

// readAll appends what r yields to b until io.EOF, filling b's spare capacity
// first.
func readAll(b []byte, r io.Reader) ([]byte, error) {
	for {
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
		n, err := r.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if errors.Is(err, io.EOF) {
			return b, nil
		}
		if err != nil {
			return b, err
		}
	}
}
