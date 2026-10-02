package compression

import (
	"errors"
	"fmt"
)

// Codec names as Compressor::get_comp_alg_name spells them
// (src/compressor/Compressor.h); they are the compression_type strings
// RGWCompressionInfo stores.
const (
	None   = "none"
	Snappy = "snappy"
	Zlib   = "zlib"
	Zstd   = "zstd"
	LZ4    = "lz4"
)

var (
	// ErrUnknownCodec is a compression_type with no decoder here. radosgw
	// logs "Cannot load compressor of type" and fails the read with EIO.
	ErrUnknownCodec = errors.New("compression: unknown codec")
	// ErrCorrupt is a stored block or block map that does not decode. A block
	// that radosgw's decompress refuses as well is also a *DecodeError; one
	// it decodes, or never returns from, is ErrCorrupt alone.
	ErrCorrupt = errors.New("compression: corrupt block")
)

// DecodeError is a stored block its codec cannot decode where radosgw's
// decompress fails as well. Ret is what that decompress returns: -1, or -2
// where snappy's RawUncompress fails or an lz4 block decodes short of its
// pair's length (SnappyCompressor.h:85 and :94 at v19.2.6 and v20.2.4;
// LZ4Compressor.cc:142 and :144 at v19.2.6, :138 and :140 at v20.2.4).
// radosgw's GET takes Ret for an errno, EPERM or ENOENT, and answers 403
// AccessDenied or 404 NoSuchKey when no byte of the body has gone out
// (send_response_data, rgw_rest_s3.cc:391-404 at v19.2.6; rgw_common.cc:89
// and :97). A DecodeError is an ErrCorrupt.
type DecodeError struct {
	Codec string
	Ret   int
	cause string
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("%v: %s: %s (radosgw's decompress returns %d)", ErrCorrupt, e.Codec, e.cause, e.Ret)
}

// Unwrap makes a DecodeError an ErrCorrupt.
func (e *DecodeError) Unwrap() error { return ErrCorrupt }

// Decoder decodes one stored block — one compression_block of an object's
// RGWCompressionInfo, an independent unit of compression — into dst's backing
// array (grown when it does not fit) and returns the decoded bytes. message is
// the object's compressor_message, which only zlib reads (its window bits).
//
// limit bounds the decoded length: a block that would decode to more fails
// with ErrCorrupt, and no more than limit bytes are allocated for its output.
// radosgw's decompressors take the length from the block itself, so a block
// whose header claims 4 GiB has radosgw allocate 4 GiB.
type Decoder interface {
	Decode(dst, block []byte, limit int, message *int32) ([]byte, error)
}

// NewDecoder returns the decoder for codec, or ErrUnknownCodec ("none"
// included: the caller never decodes an uncompressed object). Decoders are
// safe for concurrent use.
func NewDecoder(codec string) (Decoder, error) {
	switch codec {
	case Zlib:
		return zlibDecoder{}, nil
	case Snappy:
		return snappyDecoder{}, nil
	case Zstd:
		return newZstdDecoder()
	case LZ4:
		return lz4Decoder{}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownCodec, codec)
}

// grow returns dst[:0] with room for at least n bytes.
func grow(dst []byte, n int) []byte {
	if cap(dst) >= n {
		return dst[:0]
	}
	return make([]byte, 0, n)
}

// overLimit is the refusal of a block whose decoded length passes limit.
func overLimit(codec string, n uint64, limit int) error {
	return fmt.Errorf("%w: %s block of %d decoded bytes, more than %d", ErrCorrupt, codec, n, limit)
}

// corrupt reports a codec library's failure that radosgw's decompress does
// not share as ErrCorrupt. It keeps the library error's text but not its
// identity, so that a truncated block is not taken for a stream that ended
// early (io.ErrUnexpectedEOF).
func corrupt(codec string, err error) error {
	return fmt.Errorf("%w: %s: %s", ErrCorrupt, codec, err.Error())
}

// failed reports a codec library's failure that radosgw's decompress shares,
// returning ret, as a *DecodeError.
func failed(codec string, ret int, err error) error {
	return &DecodeError{Codec: codec, Ret: ret, cause: err.Error()}
}
