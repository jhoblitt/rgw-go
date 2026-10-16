package cksum_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cksum"
)

// The inputs of radosgw's checksum unit tests
// (https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L58-L65 at v20.2.4).
const (
	lorem = "Lorem ipsum dolor sit amet"
	dolor = "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum."
)

// partSize is s3-tests' part size, 5 MiB, the least S3 allows for any part
// but the last.
const partSize = 5 << 20

// part is one of s3-tests' part bodies: partSize bytes of letter.
func part(letter byte) []byte { return bytes.Repeat([]byte{letter}, partSize) }

func sum(t cksum.Type, data []byte) []byte {
	GinkgoHelper()
	h, err := cksum.NewHash(t)
	Expect(err).NotTo(HaveOccurred())
	h.Write(data)
	return h.Sum(nil)
}

func armor(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unarmor(s string) []byte {
	GinkgoHelper()
	b, err := base64.StdEncoding.DecodeString(s)
	Expect(err).NotTo(HaveOccurred())
	return b
}

// armorHex is a hex digest, as radosgw's tests write some, armored.
func armorHex(s string) string {
	GinkgoHelper()
	b, err := hex.DecodeString(s)
	Expect(err).NotTo(HaveOccurred())
	return armor(b)
}

// spdkPattern fills 4096 bytes from first, stepping by step and wrapping, as
// the SPDK vectors of radosgw's crc64nvme test do.
func spdkPattern(first byte, step int) []byte {
	b := make([]byte, 4096)
	v := first
	for i := range b {
		b[i] = v
		v = byte(int(v) + step)
	}
	return b
}

var _ = Describe("NewHash", func() {
	DescribeTable("computes the digest radosgw stores",
		func(t cksum.Type, input, want string) {
			Expect(armor(sum(t, []byte(input)))).To(Equal(want))
		},
		// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L106-L143 at v20.2.4
		Entry("CRC32 of dolor, aws-sdk-cpp's value", cksum.CRC32, dolor, "mLLFvQ=="),
		Entry("CRC32C of dolor, aws-sdk-cpp's value", cksum.CRC32C, dolor, "ldwuSw=="),
		// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L162-L276 at v20.2.4
		Entry("SHA1 of lorem", cksum.SHA1, lorem, armorHex("38f00f8738e241daea6f37f6f55ae8414d7b0219")),
		Entry("SHA1 of dolor, aws-sdk-cpp's value", cksum.SHA1, dolor, "zTazcHWKJZs0hFCEpsw4Rzy5Xic="),
		Entry("SHA256 of lorem", cksum.SHA256, lorem,
			armorHex("16aba5393ad72c0041f5600ad3c2c52ec437a2f0c7fc08fadfc3c0fe9641d7a3")),
		Entry("SHA256 of dolor, aws-sdk-cpp's value", cksum.SHA256, dolor, "LYwvbZeMohcStfbeNsnTH6jpak+l2P+LAYjfuefBcbs="),
		Entry("SHA512 of lorem", cksum.SHA512, lorem,
			armorHex("b1f4aaa6b51c19ffbe4b1b6fa107be09c8acafd7c768106a3faf475b1e27a940d3c075fda671eadf46c68f93d7eabcf604bcbf7055da0dc4eae6743607a2fc3f")),
		Entry("SHA512 of dolor", cksum.SHA512, dolor,
			armorHex("8ba760cac29cb2b2ce66858ead169174057aa1298ccd581514e6db6dee3285280ee6e3a54c9319071dc8165ff061d77783100d449c937ff1fb4cd1bb516a69b9")),
		// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L440-L467 at v20.2.4
		Entry("CRC64NVME of dolor, awscli's value", cksum.CRC64NVME, dolor, "wiBA+PSv41M="),
		Entry("CRC64NVME of dolor twice, awscli's value", cksum.CRC64NVME, dolor+dolor, "oa2U66pdPLk="),
		// s3-tests' test_object_checksum_sha256, _sha512 and _crc64nvme.
		Entry("SHA256 of 1024 As", cksum.SHA256, strings.Repeat("A", 1024), "arcu6553sHVAiX4MjW0j7I7vD4w6R+Gz9Ok0Q9lTa+0="),
		Entry("SHA512 of 1024 As", cksum.SHA512, strings.Repeat("A", 1024),
			"bO7Eq5ubilg55mUGSAieJjtmRdS+PhkSv4Z8Dj4XT5dqOfVEbEvR1X2DfWMZsSMQP+L+4vWQOAqD/k0O2YCZ7w=="),
		Entry("CRC64NVME of 1024 As", cksum.CRC64NVME, strings.Repeat("A", 1024), "Qeh8oXvGiSo="),
	)

	// The NVM Command Set's values, as SPDK's tests check them
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L370-L418 at v20.2.4).
	DescribeTable("computes CRC64NVME's NVMe check values",
		func(input []byte, want uint64) {
			Expect(binary.BigEndian.Uint64(sum(cksum.CRC64NVME, input))).To(Equal(want))
		},
		Entry("4096 zero bytes", make([]byte, 4096), uint64(0x6482D367EB22B64E)),
		Entry("4096 0xff bytes", bytes.Repeat([]byte{0xff}, 4096), uint64(0xC0DDBA7302ECA3AC)),
		Entry("0x00, 0x01, ... wrapping", spdkPattern(0x00, 1), uint64(0x3E729F5F6750449C)),
		Entry("0xff, 0xfe, ... wrapping", spdkPattern(0xff, -1), uint64(0x9A2DF64B8E9E517E)),
	)

	// s3-tests' test_multipart_use_cksum_helper_* parts.
	DescribeTable("computes s3-tests' 5 MiB part checksums",
		func(t cksum.Type, letter byte, want string) {
			Expect(armor(sum(t, part(letter)))).To(Equal(want))
		},
		Entry("CRC32 of the A part", cksum.CRC32, byte('A'), "JRTCyQ=="),
		Entry("CRC32 of the B part", cksum.CRC32, byte('B'), "QoZTGg=="),
		Entry("CRC32 of the C part", cksum.CRC32, byte('C'), "YAgjqw=="),
		Entry("CRC32C of the A part", cksum.CRC32C, byte('A'), "MDaLrw=="),
		Entry("CRC32C of the B part", cksum.CRC32C, byte('B'), "TH4EZg=="),
		Entry("CRC32C of the C part", cksum.CRC32C, byte('C'), "Z7mBIQ=="),
		Entry("CRC64NVME of the A part", cksum.CRC64NVME, byte('A'), "L/E4WYn8v98="),
		Entry("CRC64NVME of the B part", cksum.CRC64NVME, byte('B'), "xW1l19VobYM="),
		Entry("CRC64NVME of the C part", cksum.CRC64NVME, byte('C'), "cK5MnNaWrW4="),
		Entry("SHA1 of the A part", cksum.SHA1, byte('A'), "iIaTCGbm+vdVjNqIMF2S0T7ibMk="),
		Entry("SHA1 of the B part", cksum.SHA1, byte('B'), "LS/TJ32bAVKEwRu+sE3X7awh/lk="),
		Entry("SHA1 of the C part", cksum.SHA1, byte('C'), "6DDwovUaHwrKNXDMzOGbuvj9kxI="),
		Entry("SHA256 of the A part", cksum.SHA256, byte('A'), "275VF5loJr1YYawit0XSHREhkFXYkkPKGuoK0x9VKxI="),
		Entry("SHA256 of the B part", cksum.SHA256, byte('B'), "mrHwOfjTL5Zwfj74F05HOQGLdUb7E5szdCbxgUSq6NM="),
		Entry("SHA256 of the C part", cksum.SHA256, byte('C'), "Vw7oB/nKQ5xWb3hNgbyfkvDiivl+U+/Dft48nfJfDow="),
	)

	It("computes exactly the Writable types, each at its digest size", func() {
		for t := range cksum.Type(16) {
			h, err := cksum.NewHash(t)
			if !t.Writable() {
				Expect(err).To(HaveOccurred(), "type %d", t)
				Expect(h).To(BeNil(), "type %d", t)
				continue
			}
			Expect(err).NotTo(HaveOccurred(), "type %d", t)
			Expect(h.Size()).To(Equal(t.Info().DigestSize), "type %d", t)
		}
	})

	DescribeTable("names the type it refuses",
		func(t cksum.Type, name string) {
			_, err := cksum.NewHash(t)
			Expect(err).To(MatchError(ContainSubstring(name)))
		},
		Entry("none", cksum.None, "none"),
		Entry("xxhash3, which needs an XXH3 library", cksum.XXH3, "xxhash3"),
		Entry("blake3, which AWS lacks", cksum.BLAKE3, "blake3"),
		Entry("main's md5, which has no Tentacle id", cksum.Type(9), "9"),
	)
})

var _ = Describe("Finish", func() {
	It("makes the record of the hash's digest under the flags given", func() {
		h, err := cksum.NewHash(cksum.CRC32)
		Expect(err).NotTo(HaveOccurred())
		h.Write([]byte(dolor))

		c := cksum.Finish(cksum.CRC32, h, cksum.FlagFullObject)
		Expect(c).To(Equal(cksum.Cksum{Type: cksum.CRC32, Digest: unarmor("mLLFvQ=="), Flags: cksum.FlagFullObject}))
		Expect(c.Valid()).To(Succeed())
	})
})
