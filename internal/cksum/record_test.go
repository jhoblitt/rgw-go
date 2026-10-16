package cksum_test

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// v2CRC32 is Cksum::encode of the crc32 digest DE AD BE EF with flags
// V2|FULL_OBJECT, built by hand: ENCODE_START(2, 1)'s struct_v, compat and
// u32 length, then the u16 type, the u16 digest size, the digest and the u16
// flags.
func v2CRC32() []byte {
	return []byte{2, 1, 10, 0, 0, 0, 1, 0, 4, 0, 0xde, 0xad, 0xbe, 0xef, 5, 0}
}

// v1CRC32 is the same digest as the first encoder wrote it, ENCODE_START(1, 1)
// with no flags; v20.0.0 shipped it.
func v1CRC32() []byte {
	return []byte{1, 1, 8, 0, 0, 0, 1, 0, 4, 0, 0xde, 0xad, 0xbe, 0xef}
}

func deadbeef() []byte { return []byte{0xde, 0xad, 0xbe, 0xef} }

// header is ENCODE_START's six bytes for struct_v v, compat 1 and length n.
func header(v uint8, n int) []byte {
	return binary.LittleEndian.AppendUint32([]byte{v, 1}, uint32(n))
}

// record hand-builds a record at struct_v v from its body's pieces.
func record(v uint8, body ...[]byte) []byte {
	b := slices.Concat(body...)
	return slices.Concat(header(v, len(b)), b)
}

func u16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }

func seq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

func encode(c cksum.Cksum, r denc.Release) []byte {
	e := denc.NewEncoder()
	c.Encode(e, r)
	return e.Bytes()
}

func decode(b []byte) (cksum.Cksum, *denc.Decoder) {
	d := denc.NewDecoder(b)
	return cksum.DecodeCksum(d), d
}

var _ = Describe("Cksum", func() {
	Describe("the record", func() {
		crc := func() cksum.Cksum {
			return cksum.Cksum{Type: cksum.CRC32, Digest: deadbeef(), Flags: cksum.FlagV2 | cksum.FlagFullObject}
		}

		It("encodes Cksum::encode's version 2 on both releases", func() {
			Expect(encode(crc(), denc.Squid)).To(Equal(v2CRC32()), "Squid")
			Expect(encode(crc(), denc.Tentacle)).To(Equal(v2CRC32()), "Tentacle")
		})

		It("encodes a combined sha512's 64-byte digest", func() {
			c := cksum.Cksum{Type: cksum.SHA512, Digest: seq(64), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite}
			Expect(encode(c, denc.Tentacle)).To(Equal(record(2, u16(6), u16(64), seq(64), u16(0xb))))
		})

		It("encodes none with an empty digest", func() {
			Expect(encode(cksum.Cksum{Flags: cksum.FlagV2}, denc.Tentacle)).To(Equal(record(2, u16(0), u16(0), u16(1))))
		})

		It("decodes version 2", func() {
			c, d := decode(v2CRC32())
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(c).To(Equal(crc()))
			Expect(d.Remaining()).To(BeZero())
		})

		It("decodes version 1 with flags 0", func() {
			c, d := decode(v1CRC32())
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(c).To(Equal(cksum.Cksum{Type: cksum.CRC32, Digest: deadbeef()}))
			Expect(d.Remaining()).To(BeZero())
		})

		It("decodes a later version's record, skipping what follows the flags", func() {
			b := slices.Concat(record(3, u16(1), u16(4), deadbeef(), u16(5), []byte{0xaa, 0xbb, 0xcc, 0xdd}), []byte{0x99})
			c, d := decode(b)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(c).To(Equal(crc()))
			Expect(d.Remaining()).To(Equal(1), "the byte after the record")
		})

		DescribeTable("stores each type under radosgw's id",
			func(id uint16, want cksum.Type, size int) {
				b := record(2, u16(id), u16(uint16(size)), seq(size), u16(1))
				c, d := decode(b)
				Expect(d.Err()).NotTo(HaveOccurred())
				Expect(c.Type).To(Equal(want))
				Expect(encode(c, denc.Tentacle)).To(Equal(b))
			},
			Entry("none, 0", uint16(0), cksum.None, 0),
			Entry("crc32, 1", uint16(1), cksum.CRC32, 4),
			Entry("crc32c, 2", uint16(2), cksum.CRC32C, 4),
			Entry("xxh3, 3", uint16(3), cksum.XXH3, 8),
			Entry("sha1, 4", uint16(4), cksum.SHA1, 20),
			Entry("sha256, 5", uint16(5), cksum.SHA256, 32),
			Entry("sha512, 6", uint16(6), cksum.SHA512, 64),
			Entry("blake3, 7", uint16(7), cksum.BLAKE3, 32),
			Entry("crc64nvme, 8", uint16(8), cksum.CRC64NVME, 8),
		)

		It("round-trips every type", func() {
			for t := range cksum.CRC64NVME + 1 {
				in := cksum.Cksum{Type: t, Digest: seq(t.Info().DigestSize), Flags: cksum.FlagsOf(t)}
				if len(in.Digest) == 0 {
					in.Digest = nil
				}
				out, d := decode(encode(in, denc.Tentacle))
				Expect(d.Err()).NotTo(HaveOccurred(), "type %d", t)
				Expect(out).To(Equal(in), "type %d", t)
			}
		})

		DescribeTable("refuses a type or digest size radosgw's decoder takes unchecked",
			func(b []byte) {
				c, d := decode(b)
				Expect(d.Err()).To(MatchError(denc.ErrMalformed))
				Expect(c).To(BeZero())
			},
			Entry("type 9, main's md5", record(2, u16(9), u16(16), seq(16), u16(1))),
			Entry("type 65535", record(2, u16(65535), u16(0), u16(1))),
			Entry("a digest size of 64 on a crc32", record(2, u16(1), u16(64), seq(64), u16(1))),
			Entry("a digest size past the 64-byte buffer", record(2, u16(6), u16(65), seq(65), u16(1))),
			Entry("a digest size of 0 on a sha256", record(2, u16(5), u16(0), u16(1))),
			Entry("a digest on none", record(2, u16(0), u16(4), deadbeef(), u16(1))),
			Entry("a version 1 record of type 9", record(1, u16(9), u16(16), seq(16))),
		)

		DescribeTable("refuses a record that is not framed",
			func(b []byte, want error) {
				c, d := decode(b)
				Expect(d.Err()).To(MatchError(want))
				Expect(c).To(BeZero())
			},
			Entry("nothing", []byte{}, denc.ErrShortBuffer),
			Entry("a truncated record", v2CRC32()[:12], denc.ErrShortBuffer),
			Entry("a truncated version 1 record", v1CRC32()[:13], denc.ErrShortBuffer),
			Entry("a digest that runs past the struct",
				slices.Concat(header(2, 4), u16(1), u16(4), deadbeef(), u16(5)), denc.ErrOverread),
			Entry("a compat past version 2", slices.Concat([]byte{3, 3}, v2CRC32()[2:]), denc.ErrIncompatible),
		)

		DescribeTable("Valid refuses what DecodeCksum would",
			func(c cksum.Cksum, ok bool) {
				if ok {
					Expect(c.Valid()).To(Succeed())
				} else {
					Expect(c.Valid()).NotTo(Succeed())
				}
			},
			Entry("a sha256", cksum.Cksum{Type: cksum.SHA256, Digest: seq(32)}, true),
			Entry("none", cksum.Cksum{}, true),
			Entry("type 9", cksum.Cksum{Type: 9, Digest: seq(16)}, false),
			Entry("a crc32 of 64 bytes", cksum.Cksum{Type: cksum.CRC32, Digest: seq(64)}, false),
			Entry("a sha1 without a digest", cksum.Cksum{Type: cksum.SHA1}, false),
		)

		DescribeTable("Encode panics rather than write a record Valid refuses",
			func(c cksum.Cksum) {
				Expect(func() { encode(c, denc.Tentacle) }).To(PanicWith(ContainSubstring("cksum")))
			},
			Entry("type 9", cksum.Cksum{Type: 9, Digest: seq(16)}),
			Entry("a crc32 of 64 bytes", cksum.Cksum{Type: cksum.CRC32, Digest: seq(64)}),
		)
	})

	DescribeTable("Armor is the base64 of the raw digest",
		func(c cksum.Cksum, want string) {
			Expect(c.Armor()).To(Equal(want))
		},
		Entry("a crc32", cksum.Cksum{Type: cksum.CRC32, Digest: deadbeef()}, "3q2+7w=="),
		Entry("a crc32 of hello world, whose digest is big-endian as AWS's is",
			cksum.Cksum{Type: cksum.CRC32, Digest: binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE([]byte("hello world")))},
			"DUoRhQ=="),
		Entry("a sha1 of nothing", cksum.Cksum{Type: cksum.SHA1, Digest: sha1.New().Sum(nil)}, "2jmj7l5rSw0yVb/vlWAYkK/YBwk="),
		Entry("a sha256 of nothing", cksum.Cksum{Type: cksum.SHA256, Digest: sha256.New().Sum(nil)}, "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="),
		Entry("none", cksum.Cksum{}, ""),
	)

	DescribeTable("Composite needs both COMPOSITE and COMBINED",
		func(flags uint16, want bool) {
			Expect(cksum.Cksum{Flags: flags}.Composite()).To(Equal(want))
		},
		Entry("no flags", uint16(0), false),
		Entry("an upload's COMPOSITE", cksum.FlagComposite, false),
		Entry("COMBINED alone", cksum.FlagCombined, false),
		Entry("COMPOSITE and COMBINED", cksum.FlagComposite|cksum.FlagCombined, true),
		Entry("a combined digest, V2|COMBINED|COMPOSITE", cksum.FlagV2|cksum.FlagCombined|cksum.FlagComposite, true),
		Entry("a combined CRC, V2|COMBINED|FULL_OBJECT", cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject, false),
	)

	DescribeTable("ObjectType is get_checksum_type over the flags radosgw writes",
		func(flags uint16, multipart bool, want string) {
			Expect(cksum.Cksum{Type: cksum.CRC32, Flags: flags}.ObjectType(multipart)).To(Equal(want))
		},
		Entry("a PutObject's 0", uint16(0), false, "FULL_OBJECT"),
		Entry("0 on a multipart object", uint16(0), true, "COMPOSITE"),
		Entry("a CRC upload's part flags, without V2, read directly", cksum.FlagFullObject, false, "FULL_OBJECT"),
		Entry("a CRC upload's part flags, without V2, on a multipart object", cksum.FlagFullObject, true, "COMPOSITE"),
		Entry("a digest upload's part flags on a multipart object", cksum.FlagComposite, true, "COMPOSITE"),
		Entry("a combined CRC, V2|COMBINED|FULL_OBJECT", cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject, true, "FULL_OBJECT"),
		Entry("a combined CRC on an object without parts", cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject, false, "FULL_OBJECT"),
		Entry("a single part's combined CRC of a COMPOSITE upload, without V2",
			cksum.FlagComposite|cksum.FlagCombined|cksum.FlagFullObject, true, "COMPOSITE"),
		Entry("a single part's combined CRC, COMBINED|FULL_OBJECT without V2", cksum.FlagCombined|cksum.FlagFullObject, true, "COMPOSITE"),
		Entry("a combined digest, V2|COMBINED|COMPOSITE", cksum.FlagV2|cksum.FlagCombined|cksum.FlagComposite, true, "COMPOSITE"),
		Entry("a combined digest on an object without parts", cksum.FlagV2|cksum.FlagCombined|cksum.FlagComposite, false, "FULL_OBJECT"),
	)

	DescribeTable("PartType is COMPOSITE only when the flags say so",
		func(flags uint16, want string) {
			Expect(cksum.Cksum{Type: cksum.SHA256, Flags: flags}.PartType()).To(Equal(want))
		},
		Entry("a PutObject's 0", uint16(0), "FULL_OBJECT"),
		Entry("a CRC upload's FULL_OBJECT", cksum.FlagFullObject, "FULL_OBJECT"),
		Entry("a digest upload's COMPOSITE, without COMBINED", cksum.FlagComposite, "COMPOSITE"),
		Entry("COMBINED alone", cksum.FlagCombined, "FULL_OBJECT"),
	)
})
