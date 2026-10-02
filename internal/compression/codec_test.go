package compression_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"strconv"

	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pierrec/lz4/v4"

	"github.com/jhoblitt/rgw-go/internal/compression"
)

// plaintext is compressible and seeded, like populate.sh's payload_compressible.
func plaintext(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	out := make([]byte, n)
	words := []string{"alpha ", "beta ", "gamma ", "delta "}
	for i := 0; i < n; {
		i += copy(out[i:], words[r.IntN(len(words))])
	}
	return out
}

// Encoders in Ceph's framing, src/compressor at v19.2.6 and v20.2.4.

// deflateMember is one stream of the kind the window bits select: raw deflate
// below 0, the zlib wrapper up to 15, gzip above.
func deflateMember(src []byte, winBits int) []byte {
	var b bytes.Buffer
	switch {
	case winBits < 0:
		w, err := flate.NewWriter(&b, 5)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.Write(src)
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())
	case winBits <= 15:
		w := zlib.NewWriter(&b)
		_, err := w.Write(src)
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())
	default:
		w := gzip.NewWriter(&b)
		// QatAccel writes QZ_DEFLATE_GZIP_EXT members, which carry an extra field.
		w.Extra = []byte{'Q', 'Z', 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		_, err := w.Write(src)
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())
	}
	return b.Bytes()
}

// cephZlib is ZlibCompressor's block: the prefix byte zlib_compress writes
// (ZlibCompressor.cc:119 at v19.2.6, :131 at v20.2.4), then the members back
// to back, as QatAccel writes one per input buffer.
func cephZlib(winBits int, members ...[]byte) []byte {
	out := []byte{0}
	for _, m := range members {
		out = append(out, deflateMember(m, winBits)...)
	}
	return out
}

func cephSnappy(src []byte) []byte { return snappy.Encode(nil, src) }

func cephZstd(src []byte) []byte {
	enc, err := zstd.NewWriter(nil)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(enc.Close()).To(Succeed()) }()
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(src)))
	return enc.EncodeAll(src, out)
}

// lz4Header is LZ4Compressor::compress's table: the count, then an
// (origin_len, compressed_len) pair per block.
func lz4Header(pairs ...[2]int) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(pairs)))
	for _, p := range pairs {
		out = binary.LittleEndian.AppendUint32(out, uint32(p[0]))
		out = binary.LittleEndian.AppendUint32(out, uint32(p[1]))
	}
	return out
}

func lz4Block(src []byte) []byte {
	var c lz4.Compressor
	dst := make([]byte, lz4.CompressBlockBound(len(src)))
	n, err := c.CompressBlock(src, dst)
	Expect(err).NotTo(HaveOccurred())
	Expect(n).NotTo(BeZero(), "incompressible fixture")
	return dst[:n]
}

func cephLZ4(chunks ...[]byte) []byte {
	var pairs [][2]int
	var body []byte
	for _, ch := range chunks {
		b := lz4Block(ch)
		pairs = append(pairs, [2]int{len(ch), len(b)})
		body = append(body, b...)
	}
	return append(lz4Header(pairs...), body...)
}

// lz4Chained is a count-2 block whose second LZ4 block copies 64 bytes from
// the start of the first block's output, as LZ4_compress_fast_continue may
// when it compresses several buffers on one stream. It returns the block and
// what it decodes to.
func lz4Chained() (block, want []byte) {
	first := plaintext(4096)
	tail := []byte("tail!")
	// A match of 64 (token 0x0f, then 64-4-15) at distance len(first), then
	// the literals that must end every LZ4 block.
	second := []byte{0x0f}
	second = binary.LittleEndian.AppendUint16(second, uint16(len(first)))
	second = append(second, 64-4-15, byte(len(tail)<<4))
	second = append(second, tail...)
	fb := lz4Block(first)
	block = lz4Header([2]int{len(first), len(fb)}, [2]int{64 + len(tail), len(second)})
	block = append(append(block, fb...), second...)
	want = append(append(append([]byte(nil), first...), first[:64]...), tail...)
	return block, want
}

// encoders build a fixture's block from the plaintext the spec chose.
type encoder func(src []byte) []byte

// noLimit is a decode limit far above every fixture, for the specs about
// something other than the limit.
const noLimit = 1 << 30

// expectOwnRefusal asserts that err is a refusal radosgw's decompress does
// not share: an ErrCorrupt that is not a DecodeError.
func expectOwnRefusal(err error) {
	GinkgoHelper()
	Expect(err).To(MatchError(compression.ErrCorrupt))
	Expect(errors.As(err, new(*compression.DecodeError))).To(BeFalse(), "radosgw's decompress does not refuse it: %v", err)
}

func zlibOf(winBits int) encoder {
	return func(src []byte) []byte { return cephZlib(winBits, src) }
}

func split(at int, enc func(...[]byte) []byte) encoder {
	return func(src []byte) []byte { return enc(src[:at], src[at:]) }
}

func zlibSplit(winBits, at int) encoder {
	return split(at, func(m ...[]byte) []byte { return cephZlib(winBits, m...) })
}

// withHeader replaces the two-byte zlib header that follows the prefix byte.
func withHeader(cmf, flg byte) encoder {
	return func(src []byte) []byte {
		b := cephZlib(15, src)
		b[1], b[2] = cmf, flg
		return b
	}
}

var _ = Describe("Decoder", func() {
	DescribeTable("decodes a block written in Ceph's framing",
		func(codec string, encode encoder, message *int32) {
			src := plaintext(1 << 20)
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			got, err := dec.Decode(nil, encode(src), len(src), message)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(src), "a limit of exactly the decoded length takes the block")
		},
		Entry("zlib, raw deflate, no message (the default -15)", compression.Zlib, zlibOf(-15), nil),
		Entry("zlib, raw deflate, message -15", compression.Zlib, zlibOf(-15), new(int32(-15))),
		Entry("zlib, raw deflate streams back to back", compression.Zlib, zlibSplit(-15, 300000), nil),
		Entry("zlib, zlib wrapper, message 15", compression.Zlib, zlibOf(15), new(int32(15))),
		Entry("zlib, zlib wrapper, message 0 takes the header's window", compression.Zlib, zlibOf(15), new(int32(0))),
		Entry("zlib, gzip wrapper, message 31", compression.Zlib, zlibOf(31), new(int32(31))),
		Entry("zlib, gzip members back to back as QatAccel writes them, message 31", compression.Zlib, zlibSplit(31, 65536), new(int32(31))),
		Entry("zlib, gzip wrapper, message 16 takes a 32 KiB window", compression.Zlib, zlibOf(31), new(int32(16))),
		Entry("zlib, automatic detection, message 47 finds gzip", compression.Zlib, zlibOf(31), new(int32(47))),
		Entry("zlib, automatic detection, message 47 finds zlib", compression.Zlib, zlibOf(15), new(int32(47))),
		Entry("zlib, automatic detection, members of both kinds", compression.Zlib, func(src []byte) []byte {
			return append(cephZlib(31, src[:1000]), deflateMember(src[1000:], 15)...)
		}, new(int32(32))),
		Entry("snappy", compression.Snappy, cephSnappy, nil),
		Entry("zstd", compression.Zstd, cephZstd, nil),
		Entry("lz4, one block", compression.LZ4, func(src []byte) []byte { return cephLZ4(src) }, nil),
		Entry("lz4, two blocks", compression.LZ4, split(600000, cephLZ4), nil),
	)

	It("decodes an lz4 block that refers back into an earlier block's output", func() {
		dec, err := compression.NewDecoder(compression.LZ4)
		Expect(err).NotTo(HaveOccurred())
		block, want := lz4Chained()
		got, err := dec.Decode(nil, block, len(want), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	})

	DescribeTable("decodes into dst's backing array when it is large enough, and grows it otherwise",
		func(codec string, encode encoder) {
			src := plaintext(1 << 20)
			block := encode(src)
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			buf := make([]byte, 0, 2<<20)
			got, err := dec.Decode(buf, block, len(src), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(src))
			Expect(&got[0]).To(BeIdenticalTo(&buf[:1][0]), "decoded into the caller's buffer")
			got, err = dec.Decode(make([]byte, 0, 16), block, len(src), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(src))
		},
		Entry("zlib", compression.Zlib, zlibOf(-15)),
		Entry("snappy", compression.Snappy, cephSnappy),
		Entry("zstd", compression.Zstd, cephZstd),
		Entry("lz4", compression.LZ4, func(src []byte) []byte { return cephLZ4(src) }),
	)

	DescribeTable("refuses what radosgw's decompress refuses",
		func(codec string, encode encoder, message *int32, ret int) {
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			_, err = dec.Decode(nil, encode(plaintext(1<<16)), noLimit, message)
			Expect(err).To(MatchError(compression.ErrCorrupt))
			de, ok := errors.AsType[*compression.DecodeError](err)
			Expect(ok).To(BeTrue(), "a DecodeError: %v", err)
			Expect(de.Ret).To(Equal(ret), "radosgw's decompress return")
		},
		Entry("zstd shorter than its length header", compression.Zstd, func([]byte) []byte { return []byte{1, 2, 3} }, nil, -1),
		Entry("lz4 whose block decodes short", compression.LZ4, func(src []byte) []byte {
			b := cephLZ4(src)
			binary.LittleEndian.PutUint32(b[4:], uint32(len(src)+1)) // origin_len lies
			return b
		}, nil, -2),
		Entry("lz4 whose block decodes long", compression.LZ4, func(src []byte) []byte {
			b := cephLZ4(src)
			binary.LittleEndian.PutUint32(b[4:], uint32(len(src)-1))
			return b
		}, nil, -1),
		Entry("lz4 with an empty compressed block, which LZ4_decompress_safe refuses", compression.LZ4, func([]byte) []byte {
			return lz4Header([2]int{0, 0})
		}, nil, -1),
		Entry("snappy garbage", compression.Snappy, func([]byte) []byte { return []byte{0xff, 0xff, 0xff} }, nil, -1),
		Entry("snappy whose length header lies", compression.Snappy, func(src []byte) []byte {
			b := cephSnappy(src)
			b[0]++
			return b
		}, nil, -2),
		Entry("snappy copying at offset 0, which only S2 reads as a repeat", compression.Snappy, func([]byte) []byte {
			// "ab", then a copy of 4 at offset 1, then a copy of 4 at offset 0.
			return []byte{10, 0x04, 'a', 'b', 0x01, 0x01, 0x01, 0x00}
		}, nil, -2),
		Entry("zlib garbage after the prefix", compression.Zlib, func([]byte) []byte { return []byte{0, 0xff, 0xff, 0xff, 0xff} }, nil, -1),
		Entry("zlib with garbage after the last member", compression.Zlib, func(src []byte) []byte {
			return append(cephZlib(31, src), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
		}, new(int32(31)), -1),
		Entry("zlib whose zlib checksum is wrong", compression.Zlib, func(src []byte) []byte {
			b := cephZlib(15, src)
			b[len(b)-1] ^= 0xff
			return b
		}, new(int32(15)), -1),
		Entry("zlib whose gzip checksum is wrong", compression.Zlib, func(src []byte) []byte {
			b := cephZlib(31, src)
			b[len(b)-8] ^= 0xff
			return b
		}, new(int32(31)), -1),
		Entry("zlib, a zlib stream where the message asks for gzip", compression.Zlib, zlibOf(15), new(int32(31)), -1),
		Entry("zlib, a gzip stream where the message asks for zlib", compression.Zlib, zlibOf(31), new(int32(15)), -1),
		Entry("zlib, a zlib header whose window exceeds the message's", compression.Zlib, zlibOf(15), new(int32(14)), -1),
		// compressor_zlib_winsize 8: deflateInit2 deflates with a 512-byte
		// window and says so in the header, which inflateInit2(8) then refuses.
		Entry("zlib, the header radosgw writes for message 8", compression.Zlib, withHeader(0x18, 0x19), new(int32(8)), -1),
		Entry("zlib, a header window the automatic mode's bits refuse", compression.Zlib, zlibOf(15), new(int32(46)), -1),
		Entry("zlib, a zlib header asking for a preset dictionary", compression.Zlib, func(src []byte) []byte {
			b := cephZlib(15, src)
			// FDICT and the DICTID of an empty dictionary, which inflate still answers Z_NEED_DICT.
			hdr := []byte{0, 0x78, 0x20, 0, 0, 0, 1}
			return append(hdr, b[3:]...)
		}, new(int32(15)), -1),
		Entry("zlib, message -16", compression.Zlib, zlibOf(-15), new(int32(-16)), -1),
		Entry("zlib, message -7", compression.Zlib, zlibOf(-15), new(int32(-7)), -1),
		Entry("zlib, message 7", compression.Zlib, zlibOf(15), new(int32(7)), -1),
		Entry("zlib, message 17", compression.Zlib, zlibOf(31), new(int32(17)), -1),
		Entry("zlib, message 33", compression.Zlib, zlibOf(31), new(int32(33)), -1),
		Entry("zlib, message 48", compression.Zlib, zlibOf(31), new(int32(48)), -1),
	)

	// radosgw's zstd decompress ignores ZSTD_decompressStream's result and
	// its zlib decompress stops quietly when the input runs out; both hand
	// back what they decoded as success.
	DescribeTable("refuses a damaged block that radosgw serves short",
		func(codec string, encode encoder, message *int32) {
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			_, err = dec.Decode(nil, encode(plaintext(1<<16)), noLimit, message)
			expectOwnRefusal(err)
		},
		Entry("zstd whose frame decodes to a different length", compression.Zstd, func(src []byte) []byte {
			return append(binary.LittleEndian.AppendUint32(nil, 5), cephZstd(src)[4:]...)
		}, nil),
		Entry("zstd whose frame decodes to fewer bytes than its header says", compression.Zstd, func(src []byte) []byte {
			b := cephZstd(src)
			binary.LittleEndian.PutUint32(b, uint32(len(src)+1))
			return b
		}, nil),
		Entry("zstd garbage after the length header", compression.Zstd, func([]byte) []byte {
			return []byte{5, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff}
		}, nil),
		Entry("zlib, a truncated raw deflate stream", compression.Zlib, func(src []byte) []byte {
			b := cephZlib(-15, src)
			return b[:len(b)/2]
		}, nil),
		Entry("zlib, a gzip member missing its trailer", compression.Zlib, func(src []byte) []byte {
			b := cephZlib(31, src)
			return b[:len(b)-4]
		}, new(int32(31))),
	)

	It("refuses an lz4 block whose compressed data is shorter than its pair says", func() {
		dec, err := compression.NewDecoder(compression.LZ4)
		Expect(err).NotTo(HaveOccurred())
		b := cephLZ4(plaintext(1 << 16))
		_, err = dec.Decode(nil, b[:len(b)-1], noLimit, nil)
		expectOwnRefusal(err)
	})

	// LZ4Compressor::decompress decodes the count and each pair with
	// ceph's decode, which throws end_of_buffer on a short block
	// (LZ4Compressor.cc:110-116 at v19.2.6 and v20.2.4). Nothing between it
	// and the beast frontend catches that, so the frontend rethrows it out of
	// the io_context thread and the process ends (docs/ceph-upstream-bugs.md,
	// "radosgw terminates on a stored lz4 block too short for its pair
	// table").
	DescribeTable("refuses an lz4 block too short for its own header, on which radosgw ends",
		func(block []byte) {
			dec, err := compression.NewDecoder(compression.LZ4)
			Expect(err).NotTo(HaveOccurred())
			_, err = dec.Decode(nil, block, noLimit, nil)
			expectOwnRefusal(err)
		},
		Entry("shorter than its count", []byte{1, 0, 0}),
		Entry("with a truncated pair table", []byte{2, 0, 0, 0, 1, 0, 0, 0}),
	)

	DescribeTable("decodes no more than the limit, refusing a block that would decode past it",
		func(codec string, encode encoder, message *int32) {
			src := plaintext(1 << 20)
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			_, err = dec.Decode(nil, encode(src), len(src)-1, message)
			expectOwnRefusal(err)
			Expect(err).To(MatchError(ContainSubstring(strconv.Itoa(len(src)-1))), "refused by the limit")
		},
		Entry("zlib", compression.Zlib, zlibOf(-15), nil),
		Entry("zlib, the byte past the limit in a later member", compression.Zlib, zlibSplit(31, 1<<20-1), new(int32(31))),
		Entry("snappy", compression.Snappy, cephSnappy, nil),
		Entry("zstd", compression.Zstd, cephZstd, nil),
		Entry("lz4", compression.LZ4, func(src []byte) []byte { return cephLZ4(src) }, nil),
	)

	// A block whose header claims far more than the limit is refused from
	// the header, so these return at once rather than allocating 4 GiB.
	DescribeTable("refuses a header that claims more than the limit before allocating for it",
		func(codec string, block []byte) {
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			_, err = dec.Decode(nil, block, 1<<20, nil)
			expectOwnRefusal(err)
			Expect(err).To(MatchError(ContainSubstring("more than 1048576")), "refused by the limit")
		},
		Entry("snappy", compression.Snappy, binary.AppendUvarint(nil, 0xffffffff)),
		Entry("zstd", compression.Zstd, binary.LittleEndian.AppendUint32(nil, 0xffffffff)),
		Entry("lz4", compression.LZ4, lz4Header([2]int{0x7fffffff, 1})),
	)

	It("stops inflating a zlib block at the limit", func() {
		dec, err := compression.NewDecoder(compression.Zlib)
		Expect(err).NotTo(HaveOccurred())
		bomb := cephZlib(-15, make([]byte, 32<<20))
		Expect(len(bomb)).To(BeNumerically("<", 1<<20), "32 MiB of zeros deflates small")
		out, err := dec.Decode(make([]byte, 0, 1<<20), bomb, 1<<20, nil)
		expectOwnRefusal(err)
		Expect(err).To(MatchError(ContainSubstring("decodes past the limit of 1048576 bytes")))
		Expect(out).To(BeNil())
	})

	// ZlibCompressor::decompress inflates nothing from a block of no bytes
	// or of the prefix byte alone, and returns 0 (ZlibCompressor.cc:247-277
	// at v19.2.6, :267-297 at v20.2.4).
	DescribeTable("decodes an empty zlib block to nothing, as radosgw does",
		func(block []byte) {
			dec, err := compression.NewDecoder(compression.Zlib)
			Expect(err).NotTo(HaveOccurred())
			got, err := dec.Decode(nil, block, 0, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeEmpty())
		},
		Entry("the prefix byte alone", []byte{0}),
		Entry("no prefix byte", []byte(nil)),
	)

	It("knows no other codec", func() {
		_, err := compression.NewDecoder(compression.None)
		Expect(err).To(MatchError(compression.ErrUnknownCodec))
		_, err = compression.NewDecoder("brotli")
		Expect(err).To(MatchError(compression.ErrUnknownCodec))
	})

	Describe("the framing the fixtures pin", func() {
		It("zlib starts with the prefix byte", func() { Expect(cephZlib(-15, []byte("x"))[0]).To(BeZero()) })
		It("zstd starts with the little-endian length", func() {
			src := plaintext(1 << 16)
			Expect(binary.LittleEndian.Uint32(cephZstd(src)[:4])).To(BeEquivalentTo(len(src)))
		})
		It("lz4 starts with count and one (origin, compressed) pair", func() {
			src := plaintext(1 << 16)
			b := cephLZ4(src)
			Expect(binary.LittleEndian.Uint32(b[0:4])).To(BeEquivalentTo(1))
			Expect(binary.LittleEndian.Uint32(b[4:8])).To(BeEquivalentTo(len(src)))
			Expect(binary.LittleEndian.Uint32(b[8:12])).To(BeEquivalentTo(len(b) - 12))
		})
	})
})
