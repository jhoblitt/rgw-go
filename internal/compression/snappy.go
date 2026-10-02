package compression

import "github.com/klauspost/compress/snappy"

// snappyDecoder is SnappyCompressor::decompress: the raw snappy block format,
// GetUncompressedLength then RawUncompress, with no framing of Ceph's.
// DecodeStrict is the reference decoder's grammar; snappy.Decode would also
// take S2's repeat-offset copies, which RawUncompress refuses.
type snappyDecoder struct{}

func (snappyDecoder) Decode(dst, block []byte, limit int, _ *int32) ([]byte, error) {
	n, err := snappy.DecodedLen(block)
	if err != nil {
		return nil, failed("snappy", -1, err)
	}
	if n > limit {
		return nil, overLimit("snappy", uint64(max(n, 0)), limit)
	}
	out, err := snappy.DecodeStrict(dst, block)
	if err != nil {
		return nil, failed("snappy", -2, err)
	}
	return out, nil
}
