package s3_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = Describe("ValidBucketName", func() {
	DescribeTable("is valid_s3_bucket_name",
		func(name string, strict, relaxed bool) {
			check := func(relax, want bool) {
				err := s3.ValidBucketName(name, relax)
				if want {
					Expect(err).NotTo(HaveOccurred(), "%q relaxed=%t", name, relax)
				} else {
					Expect(err).To(MatchError(op.ErrInvalidBucketName), "%q relaxed=%t", name, relax)
				}
			}
			check(false, strict)
			check(true, relaxed)
		},
		Entry("two bytes are too short", "ab", false, false),
		Entry("three bytes are enough", "abc", true, true),
		Entry("64 bytes are too long strictly, fine relaxed", strings.Repeat("a", 64), false, true),
		Entry("256 bytes are too long relaxed", strings.Repeat("a", 256), false, false),
		Entry("an uppercase letter is relaxed only", "Bucket", false, true),
		Entry("a leading dash is relaxed only", "-abc", false, true),
		Entry("a trailing dash is relaxed only", "abc-", false, true),
		Entry("doubled dots are relaxed only", "a..b", false, true),
		Entry("a dot before a dash is relaxed only", "a.-b", false, true),
		Entry("a dash before a dot is relaxed only", "a-.b", false, true),
		Entry("a single dot between letters", "a.b", true, true),
		Entry("an underscore is relaxed only", "a_b", false, true),
		Entry("a leading underscore is relaxed only", "_abc", false, true),
		Entry("a leading star is never valid", "*abc", false, false),
		Entry("a slash is never valid", "a/b", false, false),
		Entry("a byte past ASCII is never valid", "ab\xc3\xa9", false, false),
		Entry("a dotted quad is never valid", "192.168.1.1", false, false),
		Entry("four dotted numbers are not an address", "1.2.3.4.5", true, true),
		Entry("the scan stops at a NUL, as the C string ends there", "abc\x00*z", true, true),
	)
})
