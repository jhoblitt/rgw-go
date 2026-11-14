package integration_test

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // SHA1 is a checksum algorithm S3 defines, not a security control
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The write-path gate's oracle: what a request carries and what a gateway
// stores and answers for it, computed from the standard library and the
// formats radosgw defines rather than from rgw-go's own packages, so that
// the gate holds both gateways to a third party.

// cksumAlgorithm is a checksum algorithm rgw-go computes for a write, with
// its row of radosgw's table
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L41-L99 at v20.2.4).
type cksumAlgorithm struct {
	// name is the algorithm as x-amz-checksum-algorithm names it.
	name string
	// id is the rgw::cksum::Type a record stores.
	id uint16
	// aws is the table's FLAG_AWS_CKSUM: a v20.2.4 radosgw names a type
	// without it x-rgw-checksum-<name> on a read and answers no header for
	// it on a write.
	aws  bool
	hash func([]byte) []byte
}

// writableAlgorithms are the six algorithms rgw-go computes for a write that
// a v20.2.4 radosgw has a type for: the five its table flags as AWS's, and
// SHA512, which it does not.
var writableAlgorithms = []cksumAlgorithm{
	{name: "CRC32", id: 1, aws: true, hash: func(b []byte) []byte { return binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(b)) }},
	{name: "CRC32C", id: 2, aws: true, hash: func(b []byte) []byte {
		return binary.BigEndian.AppendUint32(nil, crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
	}},
	{name: "SHA1", id: 4, aws: true, hash: func(b []byte) []byte { s := sha1.Sum(b); return s[:] }}, //nolint:gosec // S3's SHA1 checksum
	{name: "SHA256", id: 5, aws: true, hash: func(b []byte) []byte { s := sha256.Sum256(b); return s[:] }},
	{name: "SHA512", id: 6, hash: func(b []byte) []byte { s := sha512.Sum512(b); return s[:] }},
	{name: "CRC64NVME", id: 8, aws: true, hash: func(b []byte) []byte {
		return binary.BigEndian.AppendUint64(nil, crc64.Checksum(b, crc64NVMETable))
	}},
}

// crc64NVMETable is CRC-64/NVME's polynomial, 0xAD93D23594C93659, reflected
// as hash/crc64 takes it.
var crc64NVMETable = crc64.MakeTable(0x9A6C9329AC4BC9B5)

// wpAlgorithm is the writable algorithm of name.
func wpAlgorithm(name string) cksumAlgorithm {
	for _, a := range writableAlgorithms {
		if a.name == name {
			return a
		}
	}
	panic("no writable checksum algorithm " + name)
}

// lower is the algorithm's name as a header spells it.
func (a cksumAlgorithm) lower() string { return strings.ToLower(a.name) }

// valueHeader is the header a request supplies a's value under.
func (a cksumAlgorithm) valueHeader() string { return "x-amz-checksum-" + a.lower() }

// readHeader is the header a v20.2.4 radosgw answers a's stored value under
// on a checksum-mode read: header_name, x-amz-checksum-<name> for an AWS
// type and x-rgw-checksum-<name> for any other
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L177-L187 at v20.2.4).
// rgw-go answers every writable type under x-amz-checksum-<name>.
func (a cksumAlgorithm) readHeader(radosgw bool) string {
	if radosgw && !a.aws {
		return "x-rgw-checksum-" + a.lower()
	}
	return a.valueHeader()
}

// element is the type's element in an XML document, Checksum<NAME>
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L189-L192 at v20.2.4).
func (a cksumAlgorithm) element() string { return "Checksum" + a.name }

// armor is a's digest of b as a response carries it: the base64 of the
// digest, a CRC's big-endian, as the record stores it.
func (a cksumAlgorithm) armor(b []byte) string { return base64.StdEncoding.EncodeToString(a.hash(b)) }

// composite is a's composite of the parts, without its "-<parts>": a's
// digest over the parts' digests, concatenated in order, as Amazon S3
// defines it for a multipart upload and radosgw's DigestCombiner computes
// it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L90-L115 at v20.2.4).
func (a cksumAlgorithm) composite(parts ...[]byte) string {
	var cat []byte
	for _, p := range parts {
		cat = append(cat, a.hash(p)...)
	}
	return a.armor(cat)
}

// The flags of a checksum record
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L104-L108 at v20.2.4).
const (
	cksumFlagV2         = 0x1
	cksumFlagComposite  = 0x2
	cksumFlagFullObject = 0x4
	cksumFlagCombined   = 0x8
)

// cksumRecord is a user.rgw.cksum attr: Cksum::encode's ENCODE_START(2, 1)
// header, a u16 type, a u16 digest size, the digest and u16 flags, every
// integer little-endian
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L258-L281 at v20.2.4).
type cksumRecord struct {
	Type   uint16
	Digest []byte
	Flags  uint16
}

var errCksumRecord = errors.New("not a version 2 checksum record")

// parseCksumRecord decodes b, which must be one version 2 record whose
// length covers it exactly.
func parseCksumRecord(b []byte) (cksumRecord, error) {
	const header = 6
	if len(b) < header+4 || b[0] != 2 || b[1] != 1 {
		return cksumRecord{}, fmt.Errorf("%w: % x", errCksumRecord, b)
	}
	if int(binary.LittleEndian.Uint32(b[2:])) != len(b)-header {
		return cksumRecord{}, fmt.Errorf("%w: its length %d against %d bytes", errCksumRecord, binary.LittleEndian.Uint32(b[2:]), len(b)-header)
	}
	size := int(binary.LittleEndian.Uint16(b[8:]))
	if len(b) != header+4+size+2 {
		return cksumRecord{}, fmt.Errorf("%w: a %d-byte digest in %d bytes", errCksumRecord, size, len(b))
	}
	return cksumRecord{
		Type:   binary.LittleEndian.Uint16(b[6:]),
		Digest: b[10 : 10+size],
		Flags:  binary.LittleEndian.Uint16(b[10+size:]),
	}, nil
}

// cstring is b up to its first NUL, as radosgw-admin's object stat prints
// an attr it does not decode: dump_string formats bl.to_str().c_str()
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L1350-L1356 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L1409-L1415 at v20.2.4).
func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// awsChunked frames body as the unsigned aws-chunked payload of
// STREAMING-UNSIGNED-PAYLOAD-TRAILER: chunks of at most chunk bytes, each
// its hex length, CRLF, its data and CRLF; the final 0 chunk; then the
// trailer section, the one trailer as name:value and CRLF, ended by a CRLF.
func awsChunked(body []byte, chunk int, name, value string) []byte {
	var b bytes.Buffer
	for len(body) > 0 {
		n := min(chunk, len(body))
		fmt.Fprintf(&b, "%x\r\n%s\r\n", n, body[:n])
		body = body[n:]
	}
	fmt.Fprintf(&b, "0\r\n%s:%s\r\n\r\n", name, value)
	return b.Bytes()
}

// blockOffsets is the old_ofs of each block of an object of parts of the
// given plaintext sizes, a single object being one part: each part cut
// into blocks of chunk bytes from its own start, as each get_data read is
// one compressor block
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1073-L1106 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_compression.cc#L44-L88 at v20.2.4),
// and a completion shifting each part's blocks by the plaintext before it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4393-L4414 at v20.2.4).
func blockOffsets(chunk uint64, parts ...uint64) []uint64 {
	var out []uint64
	var start uint64
	for _, size := range parts {
		for ofs := uint64(0); ofs < size; ofs += chunk {
			out = append(out, start+ofs)
		}
		start += size
	}
	return out
}

// rangeBounds is the first and last byte the one range of a Range header
// names in an object of size bytes: bytes=a-b, bytes=a- to the end, or
// bytes=-n, the last n.
func rangeBounds(spec string, size int) (first, last int) {
	GinkgoHelper()
	r, ok := strings.CutPrefix(spec, "bytes=")
	Expect(ok).To(BeTrue(), "range %q", spec)
	a, b, ok := strings.Cut(r, "-")
	Expect(ok).To(BeTrue(), "range %q", spec)
	if a == "" {
		n, err := strconv.Atoi(b)
		Expect(err).NotTo(HaveOccurred(), "range %q", spec)
		return size - n, size - 1
	}
	first, err := strconv.Atoi(a)
	Expect(err).NotTo(HaveOccurred(), "range %q", spec)
	if b == "" {
		return first, size - 1
	}
	last, err = strconv.Atoi(b)
	Expect(err).NotTo(HaveOccurred(), "range %q", spec)
	return first, last
}

// compressibleBody is n bytes of words drawn from a small vocabulary by a
// generator seeded with seed: text every codec shrinks, and that no
// block of the size the gateways compress repeats.
func compressibleBody(n int, seed uint64) []byte {
	words := strings.Fields("ceph rados gateway bucket object index shard placement pool " +
		"stripe manifest head tail block codec zlib snappy zstd lz4 checksum part upload")
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // reproducible text, not a secret
	b := make([]byte, 0, n+16)
	for len(b) < n {
		b = append(b, words[r.IntN(len(words))]...)
		b = append(b, ' ')
	}
	return b[:n]
}

// xmlElement is the text of the first element of exactly name in doc, not
// one whose name it begins, its entities resolved; "" when there is none,
// or when the element holds elements rather than text. It fails the spec
// for text that is not well-formed, an entity it cannot resolve.
func xmlElement(doc []byte, name string) string {
	GinkgoHelper()
	q := regexp.QuoteMeta(name)
	m := regexp.MustCompile(`<` + q + `>([^<]*)</` + q + `>`).FindSubmatch(doc)
	if m == nil {
		return ""
	}
	var text string
	Expect(xml.Unmarshal([]byte("<x>"+string(m[1])+"</x>"), &text)).To(Succeed(), "the text of %s in %s", name, doc)
	return text
}

// xmlSection is the first element name in doc with everything it holds,
// tags included, and "" when there is none.
func xmlSection(doc []byte, name string) string {
	q := regexp.QuoteMeta(name)
	return string(regexp.MustCompile(`<` + q + `>.*?</` + q + `>|<` + q + `/>`).Find(doc))
}

// s3testsParts are the three 5 MiB parts of s3-tests'
// test_multipart_use_cksum_helper_*: As, Bs and Cs.
func s3testsParts() [][]byte {
	return [][]byte{bytes.Repeat([]byte("A"), 5<<20), bytes.Repeat([]byte("B"), 5<<20), bytes.Repeat([]byte("C"), 5<<20)}
}

// crc32Record is the user.rgw.cksum of a PutObject of the dolor text under
// CRC32: flags 0.
func crc32Record() []byte {
	return []byte{2, 1, 10, 0, 0, 0, 1, 0, 4, 0, 0x98, 0xb2, 0xc5, 0xbd, 0, 0}
}

var _ = Describe("the write-path gate's oracle", func() {
	// The inputs and values of radosgw's checksum unit tests, which carry
	// aws-sdk-cpp's and awscli's values
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L58-L65,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L106-L143 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/test_rgw_cksum.cc#L440-L467 at v20.2.4),
	// and s3-tests' 1024 As.
	const dolor = "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum."

	DescribeTable("armors each algorithm's digest as radosgw stores and answers it",
		func(name, input, want string) {
			Expect(wpAlgorithm(name).armor([]byte(input))).To(Equal(want))
		},
		Entry("CRC32 of dolor, aws-sdk-cpp's value", "CRC32", dolor, "mLLFvQ=="),
		Entry("CRC32C of dolor, aws-sdk-cpp's value", "CRC32C", dolor, "ldwuSw=="),
		Entry("SHA1 of dolor, aws-sdk-cpp's value", "SHA1", dolor, "zTazcHWKJZs0hFCEpsw4Rzy5Xic="),
		Entry("SHA256 of dolor, aws-sdk-cpp's value", "SHA256", dolor, "LYwvbZeMohcStfbeNsnTH6jpak+l2P+LAYjfuefBcbs="),
		Entry("SHA512 of 1024 As, s3-tests' value", "SHA512", strings.Repeat("A", 1024),
			"bO7Eq5ubilg55mUGSAieJjtmRdS+PhkSv4Z8Dj4XT5dqOfVEbEvR1X2DfWMZsSMQP+L+4vWQOAqD/k0O2YCZ7w=="),
		Entry("CRC64NVME of dolor, awscli's value", "CRC64NVME", dolor, "wiBA+PSv41M="),
		Entry("CRC64NVME of 1024 As, s3-tests' value", "CRC64NVME", strings.Repeat("A", 1024), "Qeh8oXvGiSo="),
	)

	// s3-tests' test_multipart_use_cksum_helper_* upload three parts of
	// 5 MiB, of As, Bs and Cs, and expect these values.
	Describe("over s3-tests' three 5 MiB parts", func() {
		DescribeTable("gives a digest algorithm's composite",
			func(name, want string) { Expect(wpAlgorithm(name).composite(s3testsParts()...)).To(Equal(want)) },
			Entry("SHA1", "SHA1", "sizjvY4eud3MrcHdZM3cQ/ol39o="),
			Entry("SHA256", "SHA256", "uWBwpe1dxI4Vw8Gf0X9ynOdw/SS6VBzfWm9giiv1sf4="),
		)

		DescribeTable("gives a CRC's full-object value as the CRC of the parts' concatenation",
			func(name, want string) {
				Expect(wpAlgorithm(name).armor(bytes.Join(s3testsParts(), nil))).To(Equal(want))
			},
			Entry("CRC32", "CRC32", "WgDhBQ=="),
			Entry("CRC32C", "CRC32C", "xU+Krw=="),
			Entry("CRC64NVME", "CRC64NVME", "i+6LR0y3eFo="),
		)
	})

	It("names a type radosgw does not count as AWS's under x-rgw-checksum on radosgw's reads alone", func() {
		Expect(wpAlgorithm("SHA512").readHeader(true)).To(Equal("x-rgw-checksum-sha512"))
		Expect(wpAlgorithm("SHA512").readHeader(false)).To(Equal("x-amz-checksum-sha512"))
		Expect(wpAlgorithm("SHA256").readHeader(true)).To(Equal("x-amz-checksum-sha256"))
		Expect(wpAlgorithm("CRC64NVME").element()).To(Equal("ChecksumCRC64NVME"))
	})

	Describe("parseCksumRecord", func() {
		It("decodes a version 2 record", func() {
			record := crc32Record()
			Expect(parseCksumRecord(record)).To(Equal(cksumRecord{Type: 1, Digest: []byte{0x98, 0xb2, 0xc5, 0xbd}, Flags: 0}))
			combined := append(bytes.Clone(record[:len(record)-2]), 0x0d, 0)
			Expect(parseCksumRecord(combined)).To(HaveField("Flags", uint16(cksumFlagV2|cksumFlagCombined|cksumFlagFullObject)))
		})

		DescribeTable("refuses what is not one whole version 2 record",
			func(b []byte) {
				_, err := parseCksumRecord(b)
				Expect(err).To(MatchError(errCksumRecord))
			},
			Entry("a version 1 record", append([]byte{1, 1, 8, 0, 0, 0}, crc32Record()[6:14]...)),
			Entry("a length short of the bytes", append(crc32Record(), 0)),
			Entry("a length field short of the bytes it covers", func() []byte { b := crc32Record(); b[2] = 9; return b }()),
			Entry("a digest size past the record", func() []byte { b := crc32Record(); b[8] = 5; return b }()),
			Entry("a truncated header", crc32Record()[:5]),
		)
	})

	It("prints an attr up to its first NUL, as object stat does", func() {
		Expect(cstring([]byte{2, 1, 10, 0, 0, 0, 1})).To(Equal("\x02\x01\n"))
		Expect(cstring([]byte("no nul"))).To(Equal("no nul"))
	})

	It("frames a body as an unsigned aws-chunked payload with one trailer", func() {
		Expect(string(awsChunked([]byte("hello world"), 5, "x-amz-checksum-crc32", "DUoRhQ=="))).To(Equal(
			"5\r\nhello\r\n5\r\n worl\r\n1\r\nd\r\n0\r\nx-amz-checksum-crc32:DUoRhQ==\r\n\r\n"))
		Expect(string(awsChunked(nil, 5, "x-amz-checksum-sha1", "v"))).To(Equal("0\r\nx-amz-checksum-sha1:v\r\n\r\n"))
	})

	DescribeTable("lays an object's blocks out a chunk at a time from each part's start",
		func(parts, want []uint64) { Expect(blockOffsets(4<<20, parts...)).To(Equal(want)) },
		Entry("a 10 MiB object", []uint64{10 << 20}, []uint64{0, 4 << 20, 8 << 20}),
		Entry("a 4 MiB object, one chunk exactly", []uint64{4 << 20}, []uint64{0}),
		Entry("parts of 5, 5 and 1 MiB", []uint64{5 << 20, 5 << 20, 1 << 20},
			[]uint64{0, 4 << 20, 5 << 20, 9 << 20, 10 << 20}),
	)

	DescribeTable("reads the bytes a Range header names",
		func(spec string, first, last int) {
			f, l := rangeBounds(spec, 100)
			Expect([]int{f, l}).To(Equal([]int{first, last}))
		},
		Entry("both bounds", "bytes=10-19", 10, 19),
		Entry("an open end", "bytes=90-", 90, 99),
		Entry("a suffix", "bytes=-5", 95, 99),
	)

	It("makes a body of the length asked that every codec shrinks and that repeats no block", func() {
		body := compressibleBody(8<<20, 1)
		Expect(body).To(HaveLen(8 << 20))
		Expect(compressibleBody(8<<20, 1)).To(Equal(body), "the same seed's body")
		Expect(body[:4<<20]).NotTo(Equal(body[4<<20:]), "the body's two 4 MiB blocks")
		Expect(compressibleBody(1<<20, 2)).NotTo(Equal(body[:1<<20]), "another seed's body")
		Expect(bytes.Count(body, []byte(" "))).To(BeNumerically(">", len(body)/12), "words of a dozen bytes or fewer")
	})

	It("finds an element by its exact name, and a section with all it holds", func() {
		doc := []byte(`<R><Checksum><ChecksumSHA256>v</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType></Checksum><ETag>"e"</ETag></R>`)
		Expect(xmlElement(doc, "ChecksumSHA256")).To(Equal("v"))
		Expect(xmlElement(doc, "ETag")).To(Equal(`"e"`))
		Expect(xmlElement([]byte(`<ETag>&quot;e&quot;</ETag>`), "ETag")).To(Equal(`"e"`), "an escaped element's text")
		Expect(xmlElement(doc, "Checksum")).To(BeEmpty(), "Checksum holds elements, no text")
		Expect(xmlElement(doc, "ChecksumType")).To(Equal("COMPOSITE"))
		Expect(xmlSection(doc, "Checksum")).To(Equal(`<Checksum><ChecksumSHA256>v</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType></Checksum>`))
		Expect(xmlSection([]byte(`<R><Checksum></Checksum></R>`), "Checksum")).To(Equal(`<Checksum></Checksum>`))
		Expect(xmlSection([]byte(`<R><Checksum/></R>`), "Checksum")).To(Equal(`<Checksum/>`))
		Expect(xmlSection(doc, "Missing")).To(BeEmpty())
	})

	It("fails the spec for an element whose text it cannot unescape", func() {
		failures := InterceptGomegaFailures(func() { xmlElement([]byte(`<ETag>&bogus;</ETag>`), "ETag") })
		Expect(failures).To(HaveLen(1))
		Expect(failures[0]).To(ContainSubstring("the text of ETag in <ETag>&bogus;</ETag>"))
	})
})
