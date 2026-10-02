package compression

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/pierrec/lz4/v4"
)

// lz4Decoder is LZ4Compressor::decompress: a uint32 count, count
// (origin_len, compressed_len) pairs, then the LZ4 blocks back to back.
// LZ4Compressor::compress puts every input buffer through one LZ4_stream_t,
// so a block may copy from the output of the blocks before it, which
// LZ4_decompress_safe_continue resolves; here that output is the dictionary.
// radosgw makes its input contiguous first, so it writes a count of 1.
type lz4Decoder struct{}

// lz4MaxDistance is the farthest an LZ4 match reaches back.
const lz4MaxDistance = 1 << 16

func (lz4Decoder) Decode(dst, block []byte, _ *int32) ([]byte, error) {
	if len(block) < 4 {
		return nil, fmt.Errorf("%w: lz4 block shorter than its count", ErrCorrupt)
	}
	count := uint64(binary.LittleEndian.Uint32(block))
	hdr := 4 + 8*count
	if uint64(len(block)) < hdr {
		return nil, fmt.Errorf("%w: lz4 pair table of %d entries truncated", ErrCorrupt, count)
	}
	pairs := block[4:hdr]
	var total uint64
	for i := 0; i < len(pairs); i += 8 {
		total += uint64(binary.LittleEndian.Uint32(pairs[i:]))
	}
	if total > math.MaxInt32 {
		return nil, fmt.Errorf("%w: lz4 origin length %d", ErrCorrupt, total)
	}
	out := grow(dst, int(total))[:total]
	in := block[hdr:]
	written := 0
	for i := 0; i < len(pairs); i += 8 {
		origin := int(binary.LittleEndian.Uint32(pairs[i:]))
		size := int(binary.LittleEndian.Uint32(pairs[i+4:]))
		// LZ4_decompress_safe refuses an empty source whatever it is to yield.
		if size == 0 || len(in) < size {
			return nil, fmt.Errorf("%w: lz4 block %d of %d compressed bytes, %d remain", ErrCorrupt, i/8, size, len(in))
		}
		dict := out[max(0, written-lz4MaxDistance):written]
		n, err := lz4.UncompressBlockWithDict(in[:size], out[written:written+origin], dict)
		if err != nil {
			return nil, corrupt("lz4", err)
		}
		if n != origin {
			return nil, fmt.Errorf("%w: lz4 block %d decoded %d bytes, its pair says %d", ErrCorrupt, i/8, n, origin)
		}
		written += n
		in = in[size:]
	}
	return out, nil
}
