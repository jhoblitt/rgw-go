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
	// ErrCorrupt is a stored block or block map that does not decode. radosgw
	// fails the read where its decompress returns an error.
	ErrCorrupt = errors.New("compression: corrupt block")
)

// Decoder decodes one stored block — one compression_block of an object's
// RGWCompressionInfo, an independent unit of compression — into dst's backing
// array (grown when it does not fit) and returns the decoded bytes. message is
// the object's compressor_message, which only zlib reads (its window bits).
type Decoder interface {
	Decode(dst, block []byte, message *int32) ([]byte, error)
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

// corrupt reports a codec library's failure as ErrCorrupt. It keeps the
// library error's text but not its identity, so that a truncated block is not
// taken for a stream that ended early (io.ErrUnexpectedEOF).
func corrupt(codec string, err error) error {
	return fmt.Errorf("%w: %s: %s", ErrCorrupt, codec, err.Error())
}
