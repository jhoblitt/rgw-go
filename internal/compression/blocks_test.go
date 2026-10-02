package compression_test

import (
	"bytes"
	"errors"
	"io"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("Range", func() {
	// three blocks of a 10 MiB object compressed 4 MiB at a time, as RGWPutObj_Compress lays them out
	blocks := func() []meta.CompressionBlock {
		return []meta.CompressionBlock{
			{OldOfs: 0, NewOfs: 0, Len: 100},
			{OldOfs: 4 << 20, NewOfs: 100, Len: 90},
			{OldOfs: 8 << 20, NewOfs: 190, Len: 80},
		}
	}

	DescribeTable("selects the blocks fixup_range selects",
		func(partial bool, ofs, end uint64, want compression.Window) {
			got, err := compression.Range(blocks(), partial, ofs, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("the whole object, not partial", false, uint64(0), uint64(10<<20-1),
			compression.Window{First: 0, Last: 2, QOfs: 0, QLen: 10 << 20, CompOfs: 0, CompEnd: 269}),
		Entry("a range inside the first block", true, uint64(0), uint64(10),
			compression.Window{First: 0, Last: 0, QOfs: 0, QLen: 11, CompOfs: 0, CompEnd: 99}),
		Entry("a range across the last two blocks", true, uint64(5<<20), uint64(9<<20),
			compression.Window{First: 1, Last: 2, QOfs: 1 << 20, QLen: 4<<20 + 1, CompOfs: 100, CompEnd: 269}),
		Entry("a range that ends on a block boundary", true, uint64(4<<20-1), uint64(4<<20),
			compression.Window{First: 0, Last: 1, QOfs: 4<<20 - 1, QLen: 2, CompOfs: 0, CompEnd: 189}),
		Entry("a suffix range in the last block", true, uint64(10<<20-7), uint64(10<<20-1),
			compression.Window{First: 2, Last: 2, QOfs: 2<<20 - 7, QLen: 7, CompOfs: 190, CompEnd: 269}),
	)

	It("selects the one block of a single-block object whatever the range", func() {
		got, err := compression.Range(blocks()[:1], true, 5, 9)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(compression.Window{First: 0, Last: 0, QOfs: 5, QLen: 5, CompOfs: 0, CompEnd: 99}))
	})

	It("refuses an empty block list, as rgw_compression_info_from_attr does (-EIO)", func() {
		_, err := compression.Range(nil, false, 0, 0)
		Expect(err).To(MatchError(compression.ErrCorrupt))
	})

	It("refuses a block list that starts after the range", func() {
		b := blocks()
		b[0].OldOfs = 1
		_, err := compression.Range(b, false, 0, 10)
		Expect(err).To(MatchError(compression.ErrCorrupt))
	})

	It("refuses a range that ends before it starts", func() {
		_, err := compression.Range(blocks(), true, 10, 9)
		Expect(err).To(MatchError(compression.ErrCorrupt))
	})
})

// failingWriter fails every write after the first n bytes.
type failingWriter struct {
	n   int
	err error
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if len(p) > f.n {
		k := f.n
		f.n = 0
		return k, f.err
	}
	f.n -= len(p)
	return len(p), nil
}

var _ = Describe("Stream", func() {
	var (
		src    []byte
		blocks []meta.CompressionBlock
		comp   []byte
		dec    compression.Decoder
	)

	BeforeEach(func() {
		src = plaintext(10 << 20)
		// compress in 4 MiB chunks, building the block list as RGWPutObj_Compress::process does
		blocks, comp = nil, nil
		for ofs := 0; ofs < len(src); ofs += 4 << 20 {
			chunk := src[ofs:min(ofs+4<<20, len(src))]
			b := cephSnappy(chunk)
			blocks = append(blocks, meta.CompressionBlock{OldOfs: uint64(ofs), NewOfs: uint64(len(comp)), Len: uint64(len(b))})
			comp = append(comp, b...)
		}
		var err error
		dec, err = compression.NewDecoder(compression.Snappy)
		Expect(err).NotTo(HaveOccurred())
	})

	feed := func(s *compression.Stream, data []byte, sizes ...int) {
		GinkgoHelper()
		for i, k := 0, 0; i < len(data); k++ {
			n := min(sizes[k%len(sizes)], len(data)-i)
			wrote, err := s.Write(data[i : i+n])
			Expect(err).NotTo(HaveOccurred())
			Expect(wrote).To(Equal(n))
			i += n
		}
		Expect(s.Close()).To(Succeed())
	}

	It("delivers the whole object from arbitrarily split compressed pieces", func() {
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), comp, 1, 7, 4097, 1<<20)
		Expect(out.Bytes()).To(Equal(src))
	})

	It("delivers the whole object from one write", func() {
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), comp, len(comp))
		Expect(out.Bytes()).To(Equal(src))
	})

	It("delivers exactly a range spanning two blocks, trimmed by QOfs and QLen", func() {
		ofs, end := uint64(5<<20-3), uint64(8<<20+5)
		win, err := compression.Range(blocks, true, ofs, end)
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), comp[win.CompOfs:win.CompEnd+1], 4096)
		Expect(out.Bytes()).To(Equal(src[ofs : end+1]))
	})

	It("skips whole blocks before an unranged read's offset", func() {
		ofs := uint64(9<<20 + 1)
		win, err := compression.Range(blocks, false, ofs, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), comp, 65536)
		Expect(out.Bytes()).To(Equal(src[ofs:]))
	})

	It("passes over bytes between blocks, as handle_data seeks to each new_ofs", func() {
		gapped := slices.Concat(comp[:blocks[1].NewOfs], []byte("gap"), comp[blocks[1].NewOfs:])
		blocks[1].NewOfs += 3
		blocks[2].NewOfs += 3
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), gapped, 1, 4096)
		Expect(out.Bytes()).To(Equal(src))
	})

	It("refuses blocks that overlap as soon as the overlap arrives", func() {
		blocks[2].NewOfs--
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		s := compression.NewStream(dec, blocks, win, nil, io.Discard)
		_, err = s.Write(comp[:blocks[2].NewOfs+10])
		Expect(err).To(MatchError(compression.ErrCorrupt))
	})

	It("reports a stream that ends before the last block is whole", func() {
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		s := compression.NewStream(dec, blocks, win, nil, io.Discard)
		_, err = s.Write(comp[:len(comp)-10])
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Close()).To(MatchError(io.ErrUnexpectedEOF))
	})

	It("refuses bytes past the last block", func() {
		win, err := compression.Range(blocks, true, 0, 10)
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		s := compression.NewStream(dec, blocks, win, nil, &out)
		n, err := s.Write(comp[:blocks[0].Len+1])
		Expect(err).To(MatchError(compression.ErrCorrupt))
		Expect(n).To(BeEquivalentTo(blocks[0].Len))
		Expect(out.Bytes()).To(Equal(src[:11]))
	})

	It("refuses blocks that decode short of the window", func() {
		win, err := compression.Range(blocks, false, 0, uint64(len(src)))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		s := compression.NewStream(dec, blocks, win, nil, &out)
		_, err = s.Write(comp)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Close()).To(MatchError(compression.ErrCorrupt))
		Expect(out.Bytes()).To(Equal(src))
	})

	It("fails on the first corrupt block, writes nothing after it, and stays failed", func() {
		bad := slices.Clone(comp)
		bad[blocks[1].NewOfs]++ // the second block's length header now lies
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		s := compression.NewStream(dec, blocks, win, nil, &out)
		_, err = s.Write(bad)
		Expect(err).To(MatchError(compression.ErrCorrupt))
		Expect(out.Len()).To(Equal(4<<20), "the first block was delivered before the second failed")
		_, again := s.Write(bad[blocks[2].NewOfs:])
		Expect(again).To(MatchError(err))
		Expect(s.Close()).To(MatchError(err))
		Expect(out.Len()).To(Equal(4 << 20))
	})

	It("returns the destination's error", func() {
		boom := errors.New("client went away")
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		s := compression.NewStream(dec, blocks, win, nil, &failingWriter{n: 100, err: boom})
		_, err = s.Write(comp)
		Expect(err).To(MatchError(boom))
		Expect(s.Close()).To(MatchError(boom))
	})
})
