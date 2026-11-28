package op_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// docs/differences.md, "CompleteMultipartUpload checks the part heads and
// keeps the parts until its head is written": a stored part ETag that is not
// 32 hex digits is 400 InvalidPart, where radosgw's hex_to_buf hashes
// whatever it decodes of it into the object's ETag.
var _ = Describe("MultipartETag, as docs/differences.md records it", func() {
	const md5A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	It("is the MD5 of the parts' MD5s and their count", func() {
		Expect(op.MultipartETag([]string{md5A, md5A})).To(Equal("baf72a5be99c3271cada410bd0e19bf6-2"))
	})
	DescribeTable("refuses a part ETag that is not 32 hex digits with InvalidPart",
		func(etag string) {
			_, err := op.MultipartETag([]string{md5A, etag})
			Expect(err).To(MatchError(op.ErrInvalidPart), "part ETag %q", etag)
		},
		Entry("31 hex digits", strings.Repeat("a", 31)),
		Entry("33 hex digits, which hex_to_buf reads up to its buffer", strings.Repeat("a", 33)),
		Entry("34 hex digits, a byte past an MD5", strings.Repeat("a", 34)),
		Entry("a byte that is no hex digit", strings.Repeat("a", 31)+"g"),
		Entry("an empty ETag", ""),
		Entry("a quoted ETag, which the stored part info never holds", `"`+strings.Repeat("a", 30)+`"`),
	)
})
