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

func (zlibDecoder) Decode(dst, block []byte, limit int, message *int32) ([]byte, error) {
	windowBits := int32(zlibDefaultWinSize)
	if message != nil {
		windowBits = *message
	}
	w, wbits, ok := inflateMode(windowBits)
	if !ok {
		return nil, failed("zlib", -1, fmt.Errorf("window bits %d", windowBits))
	}
	// The prefix byte marks zlib (0) or ISA-L (1) as the compressor; both
	// write the same streams, and decompress never reads it. A block without
	// one decodes to nothing there too, as decompress inflates no input.
	in := block[min(1, len(block)):]
	limit = max(limit, 0)
	out := grow(dst, min(4*len(in), limit))
	for len(in) > 0 {
		r := bytes.NewReader(in)
		m, err := openMember(r, in, w, wbits)
		if err != nil {
			return nil, zlibFailure(err)
		}
		if out, err = readAll(out, m, limit); err != nil {
			return nil, zlibFailure(err)
		}
		in = in[len(in)-r.Len():]
	}
	return out, nil
}

// errPastLimit is a block that inflates past the limit.
var errPastLimit = errors.New("decodes past the limit")

// zlibFailure classifies an inflate failure. inflate takes a stream that
// ends early for one awaiting more input, so radosgw's decompress returns
// what it inflated as success (ZlibCompressor.cc:249-277 at v19.2.6,
// :269-297 at v20.2.4), and it has no limit; every other failure is its -1
// (:261-266 at v19.2.6, :281-286 at v20.2.4).
func zlibFailure(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, errPastLimit) {
		return corrupt("zlib", err)
	}
	return failed("zlib", -1, err)
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
// first and never holding more than limit bytes: it fails when r yields a
// byte past limit.
func readAll(b []byte, r io.Reader, limit int) ([]byte, error) {
	for {
		if len(b) >= limit {
			return b, atEOF(r, limit)
		}
		if len(b) == cap(b) {
			// Grow by doubling, but never past limit, which append's
			// growth would overshoot.
			nb := make([]byte, len(b), len(b)+min(max(cap(b), 512), limit-len(b)))
			copy(nb, b)
			b = nb
		}
		n, err := r.Read(b[len(b):min(cap(b), limit)])
		b = b[:len(b)+n]
		if errors.Is(err, io.EOF) {
			return b, nil
		}
		if err != nil {
			return b, err
		}
	}
}

// atEOF reports whether r, having yielded limit bytes, ends there.
func atEOF(r io.Reader, limit int) error {
	var probe [1]byte
	for {
		n, err := r.Read(probe[:])
		switch {
		case n > 0:
			return fmt.Errorf("%w of %d bytes", errPastLimit, limit)
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}
	}
}
