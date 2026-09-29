package gate_test

import (
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// compressedSize returns how many bytes radosgw stored for an object of size
// bytes that it compressed as info records: a block per chunk it compressed,
// the blocks in order in the original data and back to back in the stored
// data.
func compressedSize(info meta.CompressionInfo, size uint64) (uint64, error) {
	if info.OrigSize != size {
		return 0, fmt.Errorf("original size %d, want %d", info.OrigSize, size)
	}
	if len(info.Blocks) == 0 {
		return 0, errors.New("no blocks")
	}
	var stored uint64
	for i, b := range info.Blocks {
		switch {
		case i == 0 && b.OldOfs != 0:
			return 0, fmt.Errorf("block 0 starts at original offset %d", b.OldOfs)
		case i > 0 && b.OldOfs <= info.Blocks[i-1].OldOfs:
			return 0, fmt.Errorf("block %d starts at original offset %d, not after block %d's %d",
				i, b.OldOfs, i-1, info.Blocks[i-1].OldOfs)
		case b.OldOfs >= size:
			return 0, fmt.Errorf("block %d starts at original offset %d, past the object's %d bytes", i, b.OldOfs, size)
		case b.NewOfs != stored:
			return 0, fmt.Errorf("block %d starts at stored offset %d, want %d", i, b.NewOfs, stored)
		}
		stored += b.Len
	}
	return stored, nil
}

var _ = Describe("compressedSize", func() {
	blocks := func(bs ...meta.CompressionBlock) meta.CompressionInfo {
		return meta.CompressionInfo{Type: "zlib", OrigSize: 10 << 20, Blocks: bs}
	}

	It("sums the blocks of every chunk radosgw compressed", func() {
		size, err := compressedSize(blocks(
			meta.CompressionBlock{OldOfs: 0, NewOfs: 0, Len: 700},
			meta.CompressionBlock{OldOfs: 4 << 20, NewOfs: 700, Len: 500},
			meta.CompressionBlock{OldOfs: 8 << 20, NewOfs: 1200, Len: 300},
		), 10<<20)
		Expect(err).NotTo(HaveOccurred())
		Expect(size).To(Equal(uint64(1500)))
	})

	DescribeTable("refuses blocks that do not tile the object",
		func(info meta.CompressionInfo, want string) {
			_, err := compressedSize(info, 10<<20)
			Expect(err).To(MatchError(want))
		},
		Entry("an original size that is not the object's",
			meta.CompressionInfo{OrigSize: 1 << 20, Blocks: []meta.CompressionBlock{{Len: 1}}},
			"original size 1048576, want 10485760"),
		Entry("no blocks", blocks(), "no blocks"),
		Entry("a first block that does not start at the object's start",
			blocks(meta.CompressionBlock{OldOfs: 1, Len: 1}),
			"block 0 starts at original offset 1"),
		Entry("blocks out of order in the original data",
			blocks(meta.CompressionBlock{Len: 1}, meta.CompressionBlock{OldOfs: 0, NewOfs: 1, Len: 1}),
			"block 1 starts at original offset 0, not after block 0's 0"),
		Entry("a block past the object's end",
			blocks(meta.CompressionBlock{Len: 1}, meta.CompressionBlock{OldOfs: 10 << 20, NewOfs: 1, Len: 1}),
			"block 1 starts at original offset 10485760, past the object's 10485760 bytes"),
		Entry("a gap in the stored data",
			blocks(meta.CompressionBlock{Len: 1}, meta.CompressionBlock{OldOfs: 1, NewOfs: 2, Len: 1}),
			"block 1 starts at stored offset 2, want 1"),
	)
})
