package cksum_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math/rand/v2"
	"slices"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
)

const (
	fullObject = cksum.FlagV2 | cksum.FlagCombined | cksum.FlagFullObject
	composite  = cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite
)

// hugePart is a part length past 4 GiB, and hugeCRC32 and hugeCRC32C are
// the CRCs of hugePart As.
const (
	hugePart   = 1<<32 + 5
	hugeCRC32  = "qhzefg=="
	hugeCRC32C = "DJxTGw=="
)

// partRecord is a part's record as radosgw stores it: the upload's flags,
// without V2.
func partRecord(t cksum.Type, flags uint16, digest []byte) cksum.Cksum {
	return cksum.Cksum{Type: t, Digest: digest, Flags: flags}
}

// combine appends parts, each of size bytes, to a new combiner for t and
// flags, and returns its Final.
func combine(t cksum.Type, flags uint16, size uint64, parts ...[]byte) cksum.Cksum {
	GinkgoHelper()
	c := cksum.NewCombiner(t, flags)
	for i, p := range parts {
		Expect(c.Append(partRecord(t, flags, p), size)).To(Succeed(), "part %d", i+1)
	}
	return c.Final()
}

// rendered is the checksum as a GET renders a multipart object's: the
// armor, with -<parts> for a composite.
func rendered(c cksum.Cksum, parts int) string {
	if c.Composite() {
		return c.Armor() + "-" + strconv.Itoa(parts)
	}
	return c.Armor()
}

func crc32Of(data []byte) []byte { return binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(data)) }

func crc32cOf(data []byte) []byte {
	return binary.BigEndian.AppendUint32(nil, crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
}

func crc64NVMEOf(data []byte) []byte { return sum(cksum.CRC64NVME, data) }

// streamed is t's digest of n bytes of letter, written a MiB at a time so
// that a part past 4 GiB needs no 4 GiB buffer.
func streamed(t cksum.Type, letter byte, n uint64) []byte {
	GinkgoHelper()
	h, err := cksum.NewHash(t)
	Expect(err).NotTo(HaveOccurred())
	buf := bytes.Repeat([]byte{letter}, 1<<20)
	for ; n >= uint64(len(buf)); n -= uint64(len(buf)) {
		h.Write(buf)
	}
	h.Write(buf[:n])
	return h.Sum(nil)
}

var _ = Describe("NewCombiner", func() {
	// s3-tests' test_multipart_use_cksum_helper_*: multipart_checksum_3parts_helper
	// creates each upload with ChecksumType FULL_OBJECT for a CRC and COMPOSITE
	// for SHA1 and SHA256, the flags FlagsOf gives.
	DescribeTable("combines s3-tests' three 5 MiB parts as radosgw does",
		func(t cksum.Type, parts []string, want string, wantFlags uint16, wantType string) {
			digests := make([][]byte, len(parts))
			for i, p := range parts {
				digests[i] = unarmor(p)
			}

			got := combine(t, cksum.FlagsOf(t), partSize, digests...)
			Expect(rendered(got, len(parts))).To(Equal(want))
			Expect(got.Type).To(Equal(t))
			Expect(got.Flags).To(Equal(wantFlags))
			Expect(got.ObjectType(true)).To(Equal(wantType))
			Expect(got.Valid()).To(Succeed())
		},
		Entry("CRC32, a full-object CRC", cksum.CRC32, []string{"JRTCyQ==", "QoZTGg==", "YAgjqw=="},
			"WgDhBQ==", fullObject, "FULL_OBJECT"),
		Entry("CRC32C, a full-object CRC", cksum.CRC32C, []string{"MDaLrw==", "TH4EZg==", "Z7mBIQ=="},
			"xU+Krw==", fullObject, "FULL_OBJECT"),
		Entry("CRC64NVME, a full-object CRC", cksum.CRC64NVME, []string{"L/E4WYn8v98=", "xW1l19VobYM=", "cK5MnNaWrW4="},
			"i+6LR0y3eFo=", fullObject, "FULL_OBJECT"),
		Entry("SHA1, a composite", cksum.SHA1,
			[]string{"iIaTCGbm+vdVjNqIMF2S0T7ibMk=", "LS/TJ32bAVKEwRu+sE3X7awh/lk=", "6DDwovUaHwrKNXDMzOGbuvj9kxI="},
			"sizjvY4eud3MrcHdZM3cQ/ol39o=-3", composite, "COMPOSITE"),
		Entry("SHA256, a composite", cksum.SHA256,
			[]string{
				"275VF5loJr1YYawit0XSHREhkFXYkkPKGuoK0x9VKxI=",
				"mrHwOfjTL5Zwfj74F05HOQGLdUb7E5szdCbxgUSq6NM=",
				"Vw7oB/nKQ5xWb3hNgbyfkvDiivl+U+/Dft48nfJfDow=",
			},
			"uWBwpe1dxI4Vw8Gf0X9ynOdw/SS6VBzfWm9giiv1sf4=-3", composite, "COMPOSITE"),
	)

	It("combines a one-part SHA256 upload as s3-tests' test_multipart_checksum_sha256 expects", func() {
		got := combine(cksum.SHA256, cksum.FlagComposite, 1024, unarmor("arcu6553sHVAiX4MjW0j7I7vD4w6R+Gz9Ok0Q9lTa+0="))
		Expect(rendered(got, 1)).To(Equal("Ok6Cs5b96ux6+MWQkJO7UBT5sKPBeXBLwvj/hK89smg=-1"))
		Expect(got.Flags).To(Equal(composite))
	})

	It("digests a digest type's parts whatever the upload's flags say", func() {
		parts := [][]byte{sum(cksum.SHA256, []byte(lorem)), sum(cksum.SHA256, []byte(dolor))}
		want := combine(cksum.SHA256, cksum.FlagComposite, 1, parts...)
		Expect(want.Flags).To(Equal(composite))

		for _, flags := range []uint16{0, cksum.FlagFullObject} {
			Expect(combine(cksum.SHA256, flags, 1, parts...)).To(Equal(want), "flags %#x", flags)
		}
	})

	// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L469-L482 at v20.2.4,
	// with the value awscli gives for dolor twice
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L465-L466 at v20.2.4).
	It("combines CRC64NVME of dolor with itself to awscli's value for dolor twice", func() {
		d := sum(cksum.CRC64NVME, []byte(dolor))
		Expect(combine(cksum.CRC64NVME, cksum.FlagFullObject, uint64(len(dolor)), d, d).Armor()).To(Equal("oa2U66pdPLk="))
	})

	// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L484-L731 at v20.2.4
	DescribeTable("combines dolor and lorem to the CRC of dolorem, as radosgw's unit tests do",
		func(t cksum.Type, crc func([]byte) []byte) {
			c := cksum.NewCombiner(t, cksum.FlagFullObject)
			Expect(c.Append(partRecord(t, cksum.FlagFullObject, crc([]byte(dolor))), uint64(len(dolor)))).To(Succeed())
			Expect(c.Append(partRecord(t, cksum.FlagFullObject, crc([]byte(lorem))), uint64(len(lorem)))).To(Succeed())
			Expect(c.Final().Digest).To(Equal(crc([]byte(dolor + lorem))))
		},
		Entry("CRC64NVME", cksum.CRC64NVME, crc64NVMEOf),
		Entry("CRC32", cksum.CRC32, crc32Of),
		Entry("CRC32C", cksum.CRC32C, crc32cOf),
	)

	DescribeTable("holds crc(a‖b) = combine(crc(a), crc(b), len(b)) over random splits",
		func(t cksum.Type, n int) {
			r := rand.New(rand.NewPCG(uint64(GinkgoRandomSeed()), uint64(n)))
			data := make([]byte, n)
			for i := range data {
				data[i] = byte(r.Uint32())
			}
			whole := sum(t, data)

			splits := []int{0, n}
			for range 6 {
				splits = append(splits, r.IntN(n+1))
			}
			for _, s := range splits {
				c := cksum.NewCombiner(t, cksum.FlagFullObject)
				Expect(c.Append(partRecord(t, cksum.FlagFullObject, sum(t, data[:s])), uint64(s))).To(Succeed())
				Expect(c.Append(partRecord(t, cksum.FlagFullObject, sum(t, data[s:])), uint64(n-s))).To(Succeed())
				Expect(c.Final().Digest).To(Equal(whole), "split at %d of %d", s, n)
			}
		},
		Entry("CRC32, no bytes", cksum.CRC32, 0),
		Entry("CRC32, one byte", cksum.CRC32, 1),
		Entry("CRC32, 2^20+3 bytes", cksum.CRC32, 1<<20+3),
		Entry("CRC32C, no bytes", cksum.CRC32C, 0),
		Entry("CRC32C, one byte", cksum.CRC32C, 1),
		Entry("CRC32C, 2^20+3 bytes", cksum.CRC32C, 1<<20+3),
		Entry("CRC64NVME, no bytes", cksum.CRC64NVME, 0),
		Entry("CRC64NVME, one byte", cksum.CRC64NVME, 1),
		Entry("CRC64NVME, 2^20+3 bytes", cksum.CRC64NVME, 1<<20+3),
	)

	// S3 allows a part of up to 5 GiB, so a part's length sets bits that no
	// s3-tests part reaches: 2^32+5 sets bit 32. The values are crc(b) and
	// crc(lorem‖b) for b of 2^32+5 As, streamed through hash/crc32 and
	// hash/crc64, the CRC32 pair also through Python's zlib.
	DescribeTable("combines a part of 2^32+5 bytes to the CRC of the whole",
		func(t cksum.Type, part, whole string) {
			c := cksum.NewCombiner(t, cksum.FlagFullObject)
			Expect(c.Append(partRecord(t, cksum.FlagFullObject, sum(t, []byte(lorem))), uint64(len(lorem)))).To(Succeed())
			Expect(c.Append(partRecord(t, cksum.FlagFullObject, unarmor(part)), hugePart)).To(Succeed())
			Expect(c.Final().Armor()).To(Equal(whole))
		},
		Entry("CRC32", cksum.CRC32, hugeCRC32, "rxtSIQ=="),
		Entry("CRC32C", cksum.CRC32C, hugeCRC32C, "/u8eSw=="),
		Entry("CRC64NVME", cksum.CRC64NVME, "LFDjXyTVUmg=", "I6L+RQsOA/Q="),
	)

	// hash/crc64 is pure Go and streams 4 GiB in about 14 s under the race
	// detector, so only the CRCs hash/crc32 computes in assembly are streamed.
	DescribeTable("streams the 2^32+5 byte part to the CRC the combination takes",
		func(t cksum.Type, want string) {
			Expect(armor(streamed(t, 'A', hugePart))).To(Equal(want))
		},
		Entry("CRC32", cksum.CRC32, hugeCRC32),
		Entry("CRC32C", cksum.CRC32C, hugeCRC32C),
	)

	DescribeTable("combines parts of unequal sizes, an empty one among them",
		func(t cksum.Type) {
			var data []byte
			c := cksum.NewCombiner(t, cksum.FlagFullObject)
			for i, n := range []int{3, 0, 1000, 70001, 1} {
				p := seq(n + i)[i:]
				data = append(data, p...)
				Expect(c.Append(partRecord(t, cksum.FlagFullObject, sum(t, p)), uint64(n))).To(Succeed())
			}
			Expect(c.Final().Digest).To(Equal(sum(t, data)))
		},
		Entry("CRC32", cksum.CRC32),
		Entry("CRC32C", cksum.CRC32C),
		Entry("CRC64NVME", cksum.CRC64NVME),
	)

	// radosgw keeps a single part's record, whose flags lack V2, so it
	// reports such an upload COMPOSITE (docs/ceph-upstream-bugs.md).
	DescribeTable("stores a single-part CRC upload FULL_OBJECT with V2",
		func(t cksum.Type, flags uint16) {
			digest := sum(t, []byte(dolor))
			got := combine(t, flags, uint64(len(dolor)), digest)
			Expect(got).To(Equal(cksum.Cksum{Type: t, Digest: digest, Flags: fullObject}))
			Expect(got.ObjectType(true)).To(Equal("FULL_OBJECT"))
		},
		Entry("CRC32 created FULL_OBJECT", cksum.CRC32, cksum.FlagFullObject),
		Entry("CRC32C created with an x-amz-checksum-type naming neither type", cksum.CRC32C, uint16(0)),
		Entry("CRC64NVME created FULL_OBJECT", cksum.CRC64NVME, cksum.FlagFullObject),
	)

	// radosgw ignores the upload's flags and combines a full-object CRC
	// (docs/ceph-upstream-bugs.md).
	DescribeTable("gives a COMPOSITE CRC upload the CRC of its parts' CRCs",
		func(t cksum.Type, crc func([]byte) []byte, letters string) {
			var parts [][]byte
			for _, l := range []byte(letters) {
				parts = append(parts, crc(part(l)))
			}

			got := combine(t, cksum.FlagComposite, partSize, parts...)
			Expect(got.Digest).To(Equal(crc(slices.Concat(parts...))))
			Expect(got.Flags).To(Equal(composite))
			Expect(got.ObjectType(true)).To(Equal("COMPOSITE"))
			Expect(rendered(got, len(parts))).To(HaveSuffix("-%d", len(parts)))
		},
		Entry("CRC32, s3-tests' three parts", cksum.CRC32, crc32Of, "ABC"),
		Entry("CRC32, one part", cksum.CRC32, crc32Of, "A"),
		Entry("CRC32C, s3-tests' three parts", cksum.CRC32C, crc32cOf, "ABC"),
	)

	DescribeTable("refuses a part of another type, leaving the combination as it was",
		func(t cksum.Type, flags uint16, wrong cksum.Cksum) {
			c := cksum.NewCombiner(t, flags)
			Expect(c.Append(partRecord(t, flags, sum(t, []byte(lorem))), uint64(len(lorem)))).To(Succeed())
			before := c.Final()

			Expect(c.Append(wrong, 1)).To(MatchError(ContainSubstring("%s", t.Info().Name)))
			Expect(c.Final()).To(Equal(before))
		},
		Entry("a CRC32C part in a CRC32 upload", cksum.CRC32, cksum.FlagFullObject,
			cksum.Cksum{Type: cksum.CRC32C, Digest: crc32cOf([]byte(lorem))}),
		Entry("a CRC32 part in a COMPOSITE CRC32C upload", cksum.CRC32C, cksum.FlagComposite,
			cksum.Cksum{Type: cksum.CRC32, Digest: crc32Of([]byte(lorem))}),
		Entry("a SHA1 part in a SHA256 upload", cksum.SHA256, cksum.FlagComposite,
			cksum.Cksum{Type: cksum.SHA1, Digest: make([]byte, 20)}),
		Entry("a part with no checksum in a CRC64NVME upload", cksum.CRC64NVME, cksum.FlagFullObject, cksum.Cksum{}),
	)

	DescribeTable("refuses a part whose digest is not its type's size",
		func(t cksum.Type, flags uint16, size int) {
			c := cksum.NewCombiner(t, flags)
			Expect(c.Append(cksum.Cksum{Type: t, Digest: make([]byte, size)}, 1)).
				To(MatchError(ContainSubstring("digest of %d bytes", size)))
		},
		Entry("a 3-byte CRC32 in a full-object upload", cksum.CRC32, cksum.FlagFullObject, 3),
		Entry("a 20-byte SHA256", cksum.SHA256, cksum.FlagComposite, 20),
	)

	DescribeTable("refuses every part of a type it does not compute",
		func(t cksum.Type) {
			c := cksum.NewCombiner(t, cksum.FlagComposite)
			Expect(c.Append(cksum.Cksum{Type: t, Digest: make([]byte, t.Info().DigestSize)}, 1)).To(HaveOccurred())
			Expect(c.Final()).To(Equal(cksum.Cksum{}))
		},
		Entry("xxhash3", cksum.XXH3),
		Entry("blake3", cksum.BLAKE3),
		Entry("none", cksum.None),
		Entry("main's md5, which has no Tentacle id", cksum.Type(9)),
	)
})
