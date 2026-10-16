package cksum_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
)

var _ = Describe("Type", func() {
	DescribeTable("Info is Tentacle's table under AWS's names",
		func(t cksum.Type, want cksum.Info) {
			Expect(t.Info()).To(Equal(want))
		},
		Entry("none", cksum.None, cksum.Info{
			Name: "none", Header: "x-rgw-checksum-none", Element: "ChecksumNONE",
		}),
		Entry("crc32", cksum.CRC32, cksum.Info{
			Name: "crc32", Header: "x-amz-checksum-crc32", Element: "ChecksumCRC32",
			DigestSize: 4, AWS: true, CRC: true,
		}),
		Entry("crc32c", cksum.CRC32C, cksum.Info{
			Name: "crc32c", Header: "x-amz-checksum-crc32c", Element: "ChecksumCRC32C",
			DigestSize: 4, AWS: true, CRC: true,
		}),
		Entry("type 3 under main's AWS name xxhash3", cksum.XXH3, cksum.Info{
			Name: "xxhash3", Header: "x-amz-checksum-xxhash3", Element: "ChecksumXXHASH3",
			DigestSize: 8, AWS: true,
		}),
		Entry("sha1", cksum.SHA1, cksum.Info{
			Name: "sha1", Header: "x-amz-checksum-sha1", Element: "ChecksumSHA1",
			DigestSize: 20, AWS: true,
		}),
		Entry("sha256", cksum.SHA256, cksum.Info{
			Name: "sha256", Header: "x-amz-checksum-sha256", Element: "ChecksumSHA256",
			DigestSize: 32, AWS: true,
		}),
		Entry("sha512, an AWS type as on main", cksum.SHA512, cksum.Info{
			Name: "sha512", Header: "x-amz-checksum-sha512", Element: "ChecksumSHA512",
			DigestSize: 64, AWS: true,
		}),
		Entry("blake3 under Tentacle's rgw header", cksum.BLAKE3, cksum.Info{
			Name: "blake3", Header: "x-rgw-checksum-blake3", Element: "ChecksumBLAKE3",
			DigestSize: 32,
		}),
		Entry("crc64nvme", cksum.CRC64NVME, cksum.Info{
			Name: "crc64nvme", Header: "x-amz-checksum-crc64nvme", Element: "ChecksumCRC64NVME",
			DigestSize: 8, AWS: true, CRC: true,
		}),
		Entry("main's md5, which has no Tentacle id", cksum.Type(9), cksum.Info{}),
		Entry("the largest id", cksum.Type(65535), cksum.Info{}),
	)

	DescribeTable("ParseName and ForeignName",
		func(name string, want cksum.Type, wantOK, wantForeign bool) {
			t, ok := cksum.ParseName(name)
			Expect([]any{t, ok}).To(Equal([]any{want, wantOK}), "ParseName(%q)", name)
			Expect(cksum.ForeignName(name)).To(Equal(wantForeign), "ForeignName(%q)", name)
		},
		Entry("crc32", "crc32", cksum.CRC32, true, false),
		Entry("crc32c in capitals", "CRC32C", cksum.CRC32C, true, false),
		Entry("Tentacle's xxh3", "xxh3", cksum.XXH3, true, false),
		Entry("Tentacle's xxh3 in capitals", "XXH3", cksum.XXH3, true, false),
		Entry("main's xxhash3", "xxhash3", cksum.XXH3, true, false),
		Entry("sha1 in mixed case", "Sha1", cksum.SHA1, true, false),
		Entry("sha256", "sha256", cksum.SHA256, true, false),
		Entry("sha512 in capitals", "SHA512", cksum.SHA512, true, false),
		Entry("blake3", "blake3", cksum.BLAKE3, true, false),
		Entry("crc64nvme in capitals", "CRC64NVME", cksum.CRC64NVME, true, false),
		Entry("none, which names no algorithm", "none", cksum.None, false, false),
		Entry("main's MD5", "MD5", cksum.None, false, true),
		Entry("main's xxhash64", "xxhash64", cksum.None, false, true),
		Entry("main's xxhash128 in mixed case", "XXHash128", cksum.None, false, true),
		Entry("an unknown name", "FOO", cksum.None, false, false),
		Entry("the empty name", "", cksum.None, false, false),
		Entry("a name with a trailing space", "crc32 ", cksum.None, false, false),
		Entry("a prefix of a name", "crc", cksum.None, false, false),
		Entry("a non-ASCII letter Unicode folds to s", "ſha1", cksum.None, false, false),
		Entry("a non-ASCII letter Unicode folds to s in a foreign name", "xxhaſh64", cksum.None, false, false),
	)

	DescribeTable("Writable is the AWS set rgw-go computes",
		func(t cksum.Type, want bool) {
			Expect(t.Writable()).To(Equal(want))
		},
		Entry("none", cksum.None, false),
		Entry("crc32", cksum.CRC32, true),
		Entry("crc32c", cksum.CRC32C, true),
		Entry("xxhash3", cksum.XXH3, false),
		Entry("sha1", cksum.SHA1, true),
		Entry("sha256", cksum.SHA256, true),
		Entry("sha512", cksum.SHA512, true),
		Entry("blake3", cksum.BLAKE3, false),
		Entry("crc64nvme", cksum.CRC64NVME, true),
		Entry("a type with no id", cksum.Type(9), false),
	)

	DescribeTable("the flags are the values radosgw stores",
		func(flag, want uint16) {
			Expect(flag).To(Equal(want))
		},
		Entry("V2", cksum.FlagV2, uint16(0x1)),
		Entry("COMPOSITE", cksum.FlagComposite, uint16(0x2)),
		Entry("FULL_OBJECT", cksum.FlagFullObject, uint16(0x4)),
		Entry("COMBINED", cksum.FlagCombined, uint16(0x8)),
	)

	DescribeTable("FlagsOf is cksum_flags_of",
		func(t cksum.Type, want uint16) {
			Expect(cksum.FlagsOf(t)).To(Equal(want))
		},
		Entry("none", cksum.None, uint16(0)),
		Entry("crc32", cksum.CRC32, cksum.FlagFullObject),
		Entry("crc32c", cksum.CRC32C, cksum.FlagFullObject),
		Entry("crc64nvme", cksum.CRC64NVME, cksum.FlagFullObject),
		Entry("xxhash3", cksum.XXH3, cksum.FlagComposite),
		Entry("sha1", cksum.SHA1, cksum.FlagComposite),
		Entry("sha256", cksum.SHA256, cksum.FlagComposite),
		Entry("sha512", cksum.SHA512, cksum.FlagComposite),
		Entry("blake3", cksum.BLAKE3, cksum.FlagComposite),
	)

	DescribeTable("Permitted is permitted_cksum_algo_and_type",
		func(t cksum.Type, flags uint16, want bool) {
			Expect(cksum.Permitted(t, flags)).To(Equal(want))
		},
		Entry("none with no flags", cksum.None, uint16(0), true),
		Entry("none under any flags", cksum.None, cksum.FlagFullObject|cksum.FlagComposite, true),
		Entry("crc32 COMPOSITE", cksum.CRC32, cksum.FlagComposite, true),
		Entry("sha256 COMPOSITE", cksum.SHA256, cksum.FlagComposite, true),
		Entry("crc64nvme COMPOSITE", cksum.CRC64NVME, cksum.FlagComposite, false),
		Entry("COMPOSITE wins over FULL_OBJECT", cksum.SHA1, cksum.FlagComposite|cksum.FlagFullObject, true),
		Entry("crc64nvme FULL_OBJECT", cksum.CRC64NVME, cksum.FlagFullObject, true),
		Entry("crc32c FULL_OBJECT", cksum.CRC32C, cksum.FlagFullObject, true),
		Entry("sha256 FULL_OBJECT", cksum.SHA256, cksum.FlagFullObject, false),
		Entry("crc32 with no type flag", cksum.CRC32, uint16(0), true),
		Entry("blake3 with no type flag", cksum.BLAKE3, uint16(0), false),
		Entry("a type with no id, COMPOSITE", cksum.Type(9), cksum.FlagComposite, false),
		Entry("a type with no id, FULL_OBJECT", cksum.Type(10), cksum.FlagFullObject, false),
	)
})
