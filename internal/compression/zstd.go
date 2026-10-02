package compression

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// zstdDecoder is ZstdCompressor::decompress: a little-endian uint32 of the
// decoded length, then one frame.
type zstdDecoder struct{ d *zstd.Decoder }

// sharedZstd is one decoder for every object: DecodeAll is safe for concurrent
// use and runs up to GOMAXPROCS decodes at once. With a nil reader it starts no
// goroutines, so it is never closed. The cap limit bounds each decode by the
// capacity of the slice it is given, which Decode sets to the length header.
var sharedZstd = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecodeAllCapLimit(true))
})

func newZstdDecoder() (Decoder, error) {
	d, err := sharedZstd()
	if err != nil {
		return nil, fmt.Errorf("creating the zstd decoder: %w", err)
	}
	return zstdDecoder{d: d}, nil
}

// Decode refuses a frame that decodes to any length but the header's, where
// radosgw, which ignores ZSTD_decompressStream's result, returns what the
// frame yielded up to that length.
func (z zstdDecoder) Decode(dst, block []byte, _ *int32) ([]byte, error) {
	if len(block) < 4 {
		return nil, fmt.Errorf("%w: zstd block shorter than its length header", ErrCorrupt)
	}
	want := int(binary.LittleEndian.Uint32(block))
	out, err := z.d.DecodeAll(block[4:], grow(dst, want)[:0:want])
	if err != nil {
		return nil, corrupt("zstd", err)
	}
	if len(out) != want {
		return nil, fmt.Errorf("%w: zstd frame decoded %d bytes, its header says %d", ErrCorrupt, len(out), want)
	}
	return out, nil
}
