package compression_test

import (
	"bytes"
	stdflate "compress/flate"
	stdgzip "compress/gzip"
	stdzlib "compress/zlib"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"runtime"
	"sync"

	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pierrec/lz4/v4"

	"github.com/jhoblitt/rgw-go/internal/compression"
)

// radosgwDefaults are compressor_zlib_level, compressor_zlib_winsize and
// compressor_zstd_level as radosgw ships them.
var radosgwDefaults = compression.EncoderOptions{ZlibLevel: 5, ZlibWinsize: -15, ZstdLevel: 1}

// chunk is rgw_max_chunk_size's default, the most plaintext one block holds.
const chunk = 4 << 20

func zlibOptions(level, winsize int) compression.EncoderOptions {
	return compression.EncoderOptions{ZlibLevel: level, ZlibWinsize: winsize, ZstdLevel: 1}
}

func zstdOptions(level int) compression.EncoderOptions {
	return compression.EncoderOptions{ZlibLevel: 5, ZlibWinsize: -15, ZstdLevel: level}
}

// noise is incompressible and seeded.
func noise(n int) []byte {
	GinkgoHelper()
	b := make([]byte, n)
	_, err := rand.NewChaCha8([32]byte{1}).Read(b)
	Expect(err).NotTo(HaveOccurred())
	return b
}

func text() []byte    { return plaintext(1 << 20) }
func empty() []byte   { return []byte{} }
func oneByte() []byte { return plaintext(1) }

func mustEncoder(codec string, o compression.EncoderOptions) compression.Encoder {
	GinkgoHelper()
	enc, err := compression.NewEncoder(codec, o)
	Expect(err).NotTo(HaveOccurred())
	return enc
}

func mustEncode(codec string, o compression.EncoderOptions, src []byte) (block []byte, message *int32) {
	GinkgoHelper()
	block, message, err := mustEncoder(codec, o).Encode(nil, src)
	Expect(err).NotTo(HaveOccurred())
	return block, message
}

// expectDecodes decodes block through NewDecoder under message, as a GET
// reads it back.
func expectDecodes(codec string, block []byte, message *int32, src []byte) {
	GinkgoHelper()
	dec, err := compression.NewDecoder(codec)
	Expect(err).NotTo(HaveOccurred())
	got, err := dec.Decode(nil, block, len(src), message)
	Expect(err).NotTo(HaveOccurred())
	Expect(got).To(Equal(src))
}

// window is the framing each zlib message names.
type window int

const (
	rawWindow window = iota
	zlibWindow
	gzipWindow
)

// readStd inflates stream with the standard library's reader for w, a
// decoder independent of NewDecoder's, and fails on bytes left after it.
func readStd(w window, stream []byte) []byte {
	GinkgoHelper()
	r := bytes.NewReader(stream)
	var rc io.ReadCloser
	var err error
	switch w {
	case rawWindow:
		rc = stdflate.NewReader(r)
	case zlibWindow:
		rc, err = stdzlib.NewReader(r)
	case gzipWindow:
		rc, err = stdgzip.NewReader(r)
	}
	Expect(err).NotTo(HaveOccurred())
	got, err := io.ReadAll(rc)
	Expect(err).NotTo(HaveOccurred())
	Expect(rc.Close()).To(Succeed())
	Expect(r.Len()).To(BeZero(), "bytes after the stream")
	return got
}

var _ = Describe("Encoder", func() {
	DescribeTable("writes a block of rgw_max_chunk_size plaintext that NewDecoder decodes",
		func(codec string, o compression.EncoderOptions) {
			src := plaintext(chunk)
			block, message := mustEncode(codec, o, src)
			Expect(len(block)).To(BeNumerically("<", len(src)/2), "compressible text compresses")
			expectDecodes(codec, block, message, src)
		},
		Entry("zlib at radosgw's defaults", compression.Zlib, radosgwDefaults),
		Entry("zlib, the zlib wrapper", compression.Zlib, zlibOptions(5, 15)),
		Entry("zlib, gzip", compression.Zlib, zlibOptions(5, 31)),
		Entry("zlib, level -1, zlib's default", compression.Zlib, zlibOptions(-1, -15)),
		Entry("zstd at radosgw's default level 1", compression.Zstd, radosgwDefaults),
		Entry("zstd, level 19", compression.Zstd, zstdOptions(19)),
		Entry("zstd, a negative level", compression.Zstd, zstdOptions(-5)),
		Entry("lz4", compression.LZ4, radosgwDefaults),
		Entry("snappy", compression.Snappy, radosgwDefaults),
	)

	// One byte takes each library's shortest path: an lz4 block of literals
	// alone, a zstd frame smaller than its minimum window, a lone snappy
	// literal. A byte either side of rgw_max_chunk_size brackets the table
	// above.
	DescribeTable("round-trips the input lengths at a block's edges through NewDecoder",
		func(codec string, n int) {
			src := plaintext(n)
			block, message := mustEncode(codec, radosgwDefaults, src)
			expectDecodes(codec, block, message, src)
		},
		Entry("zlib, 1 byte", compression.Zlib, 1),
		Entry("zlib, rgw_max_chunk_size - 1", compression.Zlib, chunk-1),
		Entry("zlib, rgw_max_chunk_size + 1", compression.Zlib, chunk+1),
		Entry("zstd, 1 byte", compression.Zstd, 1),
		Entry("zstd, rgw_max_chunk_size - 1", compression.Zstd, chunk-1),
		Entry("zstd, rgw_max_chunk_size + 1", compression.Zstd, chunk+1),
		Entry("lz4, 1 byte", compression.LZ4, 1),
		Entry("lz4, rgw_max_chunk_size - 1", compression.LZ4, chunk-1),
		Entry("lz4, rgw_max_chunk_size + 1", compression.LZ4, chunk+1),
		Entry("snappy, 1 byte", compression.Snappy, 1),
		Entry("snappy, rgw_max_chunk_size - 1", compression.Snappy, chunk-1),
		Entry("snappy, rgw_max_chunk_size + 1", compression.Snappy, chunk+1),
	)

	DescribeTable("writes a block of incompressible input that NewDecoder decodes",
		func(codec string, o compression.EncoderOptions) {
			src := noise(1 << 18)
			block, message := mustEncode(codec, o, src)
			expectDecodes(codec, block, message, src)
		},
		Entry("zlib, level 0, which stores", compression.Zlib, zlibOptions(0, -15)),
		Entry("zlib at radosgw's defaults", compression.Zlib, radosgwDefaults),
		Entry("zstd", compression.Zstd, radosgwDefaults),
		Entry("lz4", compression.LZ4, radosgwDefaults),
		Entry("snappy", compression.Snappy, radosgwDefaults),
	)

	// radosgw never compresses an empty buffer (RGWPutObj_Compress::process,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_compression.cc#L49 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_compression.cc#L49), so its
	// compressors' output for one is no format; the block keeps the framing.
	DescribeTable("encodes an empty input to a block in the codec's framing that decodes to nothing",
		func(codec string, o compression.EncoderOptions, minLen int) {
			block, message := mustEncode(codec, o, empty())
			Expect(len(block)).To(BeNumerically(">=", minLen), "the framing is all there")
			expectDecodes(codec, block, message, empty())
		},
		Entry("zlib, raw deflate: the prefix byte and an empty final block", compression.Zlib, radosgwDefaults, 3),
		Entry("zlib, the zlib wrapper: its header and checksum too", compression.Zlib, zlibOptions(5, 15), 9),
		Entry("zlib, gzip: its header and trailer too", compression.Zlib, zlibOptions(5, 31), 21),
		Entry("zstd: the length and an empty frame", compression.Zstd, radosgwDefaults, 4+6),
		Entry("lz4: the count, one pair and an empty block", compression.LZ4, radosgwDefaults, 12+1),
		Entry("snappy: the length alone", compression.Snappy, radosgwDefaults, 1),
	)

	// zlib_compress puts the byte 0 before the deflate stream
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zlib/ZlibCompressor.cc#L117-L121 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zlib/ZlibCompressor.cc#L129-L133 at v20.2.4).
	// The message is the window bits inflateInit2 takes back; Go's deflate
	// always reaches back 32 KiB, so the message and the zlib header always
	// state 15 bits, and any zlib, gzip or raw inflater takes the stream.
	DescribeTable("frames zlib as the prefix byte 0, then a stream whose window the message states, never narrower than 32 KiB",
		func(winsize int, want int32, w window) {
			for name, src := range map[string][]byte{"1 MiB of text": text(), "one byte": oneByte(), "an empty input": empty()} {
				block, message := mustEncode(compression.Zlib, zlibOptions(5, winsize), src)
				Expect(message).NotTo(BeNil(), name)
				Expect(*message).To(Equal(want), name)
				Expect(block[0]).To(BeZero(), "%s: the prefix byte", name)
				switch w {
				case zlibWindow:
					Expect(block[1]>>4).To(BeEquivalentTo(7), "%s: the zlib header's CINFO states a 32 KiB window", name)
				case gzipWindow:
					Expect(block[1:3]).To(Equal([]byte{0x1f, 0x8b}), "%s: the gzip magic", name)
				}
				Expect(readStd(w, block[1:])).To(Equal(src), name)
				expectDecodes(compression.Zlib, block, message, src)
			}
		},
		Entry("-15, radosgw's default: raw deflate", -15, int32(-15), rawWindow),
		Entry("-1, which deflateInit2 refuses: raw deflate at -15", -1, int32(-15), rawWindow),
		// docs/ceph-upstream-bugs.md, "compressor_zlib_winsize 8 writes zlib
		// blocks radosgw cannot read back": radosgw records 8 for a header
		// stating 9, which its inflateInit2(8) then refuses.
		Entry("8: the zlib wrapper at 15, not the 8 radosgw records", 8, int32(15), zlibWindow),
		Entry("15: the zlib wrapper", 15, int32(15), zlibWindow),
		Entry("16, which deflateInit2 refuses: gzip at 31", 16, int32(31), gzipWindow),
		Entry("31: gzip", 31, int32(31), gzipWindow),
		Entry("32, the option's maximum, which deflateInit2 refuses: gzip at 31", 32, int32(31), gzipWindow),
	)

	// Level 9's long match search makes this text slow; a quarter of the
	// usual input still tells every level apart.
	DescribeTable("deflates zlib at compressor_zlib_level",
		func(level int) {
			src := plaintext(256 << 10)
			block, _ := mustEncode(compression.Zlib, zlibOptions(level, -15), src)
			var want bytes.Buffer
			w, err := stdflate.NewWriter(&want, level)
			Expect(err).NotTo(HaveOccurred())
			_, err = w.Write(src)
			Expect(err).NotTo(HaveOccurred())
			Expect(w.Close()).To(Succeed())
			Expect(block[1:]).To(Equal(want.Bytes()))
		},
		Entry("0, which stores", 0),
		Entry("1", 1),
		Entry("5, radosgw's default", 5),
		Entry("9", 9),
		Entry("-1, zlib's default", -1),
	)

	It("hands out a message the caller may keep", func() {
		enc := mustEncoder(compression.Zlib, zlibOptions(5, 15))
		_, first, err := enc.Encode(nil, text())
		Expect(err).NotTo(HaveOccurred())
		*first = -15
		_, second, err := enc.Encode(nil, text())
		Expect(err).NotTo(HaveOccurred())
		Expect(*second).To(BeEquivalentTo(15))
	})

	// ZstdCompressor, LZ4Compressor and SnappyCompressor never set
	// compressor_message, so radosgw stores none for them.
	DescribeTable("returns no message for the other codecs",
		func(codec string) {
			_, message := mustEncode(codec, radosgwDefaults, text())
			Expect(message).To(BeNil())
		},
		Entry("zstd", compression.Zstd),
		Entry("lz4", compression.LZ4),
		Entry("snappy", compression.Snappy),
	)

	// ZstdCompressor::compress
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zstd/ZstdCompressor.h#L29-L62 at v19.2.7
	// and v20.2.4). libzstd states the size ZSTD_initCStream_srcSize pledges
	// in every frame; klauspost leaves it out of a frame under 256 bytes.
	// Ceph's decompress sizes its output by the length header and never
	// reads it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zstd/ZstdCompressor.h#L77-L84 at v19.2.7
	// and v20.2.4).
	DescribeTable("frames zstd as the little-endian plaintext length, then one frame",
		func(input func() []byte) {
			src := input()
			block, _ := mustEncode(compression.Zstd, radosgwDefaults, src)
			Expect(binary.LittleEndian.Uint32(block)).To(BeEquivalentTo(len(src)), "the length")
			Expect(block[4:8]).To(Equal([]byte{0x28, 0xb5, 0x2f, 0xfd}), "the frame's magic")
			var h zstd.Header
			Expect(h.Decode(block[4:])).To(Succeed())
			if h.HasFCS {
				Expect(h.FrameContentSize).To(BeEquivalentTo(len(src)), "one frame holds the whole plaintext")
			} else {
				Expect(len(src)).To(BeNumerically("<", 256), "only a frame under 256 bytes leaves out its content size")
			}
			Expect(h.HasCheckSum).To(BeFalse(), "libzstd's default writes no checksum")
		},
		Entry("1 MiB of text", text),
		Entry("a 1-byte input", oneByte),
		Entry("an empty input", empty),
	)

	// libzstd reads compressor_zstd_level 0 as its default, 3
	// (ZSTD_CLEVEL_DEFAULT in zstd v1.4.5, the submodule at v19.2.7 and
	// v20.2.4); klauspost's EncoderLevelFromZstd maps the rest.
	DescribeTable("compresses zstd at the level klauspost maps compressor_zstd_level to",
		func(level int, want zstd.EncoderLevel) {
			ref, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(want), zstd.WithEncoderCRC(false))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(ref.Close)
			for _, src := range [][]byte{text(), plaintext(chunk)} {
				block, _ := mustEncode(compression.Zstd, zstdOptions(level), src)
				Expect(block[4:]).To(Equal(ref.EncodeAll(src, nil)), "a block of %d bytes", len(src))
			}
		},
		Entry("1, radosgw's default: fastest", 1, zstd.SpeedFastest),
		Entry("a negative level: fastest", -5, zstd.SpeedFastest),
		Entry("0, libzstd's default 3: default", 0, zstd.SpeedDefault),
		Entry("6: better", 6, zstd.SpeedBetterCompression),
		Entry("19: best", 19, zstd.SpeedBestCompression),
	)

	// LZ4Compressor::compress makes its input contiguous, so it writes one
	// pair (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/lz4/LZ4Compressor.cc#L36-L85 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/lz4/LZ4Compressor.cc#L36-L85 at v20.2.4).
	DescribeTable("frames lz4 as a count of 1, one (origin_len, compressed_len) pair, then the block",
		func(input func() []byte) {
			src := input()
			block, _ := mustEncode(compression.LZ4, radosgwDefaults, src)
			Expect(binary.LittleEndian.Uint32(block[0:])).To(BeEquivalentTo(1), "the count")
			Expect(binary.LittleEndian.Uint32(block[4:])).To(BeEquivalentTo(len(src)), "origin_len")
			Expect(binary.LittleEndian.Uint32(block[8:])).To(BeEquivalentTo(len(block)-12), "compressed_len")
			got := make([]byte, len(src))
			n, err := lz4.UncompressBlock(block[12:], got)
			Expect(err).NotTo(HaveOccurred())
			Expect(got[:n]).To(Equal(src), "one LZ4 block, standing alone")
		},
		Entry("1 MiB of text", text),
		Entry("incompressible input", func() []byte { return noise(1 << 16) }),
		Entry("a 1-byte input", oneByte),
		Entry("an empty input", empty),
	)

	// SnappyCompressor::compress writes snappy::Compress's output and nothing
	// else (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/snappy/SnappyCompressor.h#L63-L71 at v19.2.7
	// and v20.2.4).
	DescribeTable("frames snappy as a raw snappy block",
		func(input func() []byte) {
			src := input()
			block, _ := mustEncode(compression.Snappy, radosgwDefaults, src)
			n, err := snappy.DecodedLen(block)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(len(src)), "the block's leading length")
			got, err := snappy.DecodeStrict(nil, block)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(src))
		},
		Entry("1 MiB of text", text),
		Entry("a 1-byte input", oneByte),
		Entry("an empty input", empty),
	)

	DescribeTable("encodes into dst's backing array when it is large enough, and grows it otherwise",
		func(codec string) {
			src := text()
			buf := make([]byte, 0, 2<<20)
			block, message, err := mustEncoder(codec, radosgwDefaults).Encode(buf, src)
			Expect(err).NotTo(HaveOccurred())
			Expect(&block[0]).To(BeIdenticalTo(&buf[:1][0]), "encoded into the caller's buffer")
			expectDecodes(codec, block, message, src)
			block, message, err = mustEncoder(codec, radosgwDefaults).Encode(make([]byte, 0, 16), src)
			Expect(err).NotTo(HaveOccurred())
			expectDecodes(codec, block, message, src)
		},
		Entry("zlib", compression.Zlib),
		Entry("zstd", compression.Zstd),
		Entry("lz4", compression.LZ4),
		Entry("snappy", compression.Snappy),
	)

	DescribeTable("is safe for concurrent use",
		func(codec string, o compression.EncoderOptions) {
			enc := mustEncoder(codec, o)
			const workers = 8
			srcs := make([][]byte, workers)
			blocks := make([][]byte, workers)
			messages := make([]*int32, workers)
			errs := make([]error, workers)
			var wg sync.WaitGroup
			for i := range workers {
				srcs[i] = plaintext(256<<10 + i)
				wg.Go(func() {
					for range 4 {
						blocks[i], messages[i], errs[i] = enc.Encode(nil, srcs[i])
						if errs[i] != nil {
							return
						}
					}
				})
			}
			wg.Wait()
			for i := range workers {
				Expect(errs[i]).NotTo(HaveOccurred(), "worker %d", i)
				expectDecodes(codec, blocks[i], messages[i], srcs[i])
			}
		},
		Entry("zlib", compression.Zlib, radosgwDefaults),
		Entry("zlib, gzip", compression.Zlib, zlibOptions(5, 31)),
		Entry("zstd", compression.Zstd, radosgwDefaults),
		Entry("lz4", compression.LZ4, radosgwDefaults),
		Entry("snappy", compression.Snappy, radosgwDefaults),
	)

	// deflateInit2 refuses these, so zlib_compress fails and radosgw stores
	// the object uncompressed (RGWPutObj_Compress::process,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_compression.cc#L54-L64 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_compression.cc#L54-L64).
	// A winsize it refuses whose sign or range still names a framing is
	// written at the full window instead, above.
	DescribeTable("refuses a zlib level deflateInit2 refuses, and a winsize that names no framing",
		func(o compression.EncoderOptions) {
			_, err := compression.NewEncoder(compression.Zlib, o)
			Expect(err).To(HaveOccurred())
			Expect(err).NotTo(MatchError(compression.ErrUnknownCodec))
		},
		Entry("a winsize of 0", zlibOptions(5, 0)),
		Entry("a winsize of 7", zlibOptions(5, 7)),
		Entry("level -2, which Go's deflate reads as Huffman only", zlibOptions(-2, -15)),
		Entry("level 10", zlibOptions(10, -15)),
	)

	It("reads the zlib options for zlib alone", func() {
		bad := compression.EncoderOptions{ZlibLevel: 10, ZlibWinsize: 0, ZstdLevel: 1}
		for _, codec := range []string{compression.Zstd, compression.LZ4, compression.Snappy} {
			_, err := compression.NewEncoder(codec, bad)
			Expect(err).NotTo(HaveOccurred(), codec)
		}
	})

	It("knows no other codec", func() {
		for _, codec := range []string{compression.None, "brotli", ""} {
			_, err := compression.NewEncoder(codec, radosgwDefaults)
			Expect(err).To(MatchError(compression.ErrUnknownCodec), "codec %q", codec)
		}
	})

	// At compressor_zstd_level 10 and up, klauspost's best level, each encoder
	// holds a 2 MiB and a 32 MiB match table, so one encoder left resident is
	// far above the bound. radosgw's ZstdCompressor::compress frees its stream
	// after each block
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zstd/ZstdCompressor.h#L29-L62 at v19.2.7
	// and v20.2.4).
	DescribeTable("holds a zstd encoder only while a block compresses",
		func(writers int) {
			src := text()
			enc := mustEncoder(compression.Zstd, zstdOptions(10))
			base := heapInUse()
			made := compression.ZstdEncodersMadeForTest()
			start := make(chan struct{})
			errs := make([]error, writers)
			var wg sync.WaitGroup
			for i := range writers {
				wg.Go(func() {
					<-start
					_, _, errs[i] = enc.Encode(nil, src)
				})
			}
			close(start)
			wg.Wait()
			for i := range writers {
				Expect(errs[i]).NotTo(HaveOccurred(), "writer %d", i)
			}
			Expect(compression.ZstdEncodersMadeForTest()-made).To(BeNumerically("<=", writers),
				"no more encoders than compressions at once")
			// One collection leaves the pool's idle encoders alive; each holds
			// the tables of one compression, about 50 MiB at level 10.
			runtime.GC()
			Expect(int64(heapNow())-int64(base)).To(BeNumerically("<", int64(writers)*(64<<20)),
				"no idle encoder holds the tables of more than one compression")
			Expect(int64(heapInUse())-int64(base)).To(BeNumerically("<", 8<<20), "nothing of them is kept once they are idle")
		},
		Entry("after one level-10 write", 1),
		Entry("after four level-10 writes at once", 4),
	)
})

// heapInUse is the heap in use after three collections: sync.Pool keeps an
// idle value through one, and frees it at the next.
func heapInUse() uint64 {
	for range 3 {
		runtime.GC()
	}
	return heapNow()
}

// heapNow is the heap in use.
func heapNow() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}
