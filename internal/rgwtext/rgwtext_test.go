package rgwtext_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

var _ = DescribeTable("CString reads s as a C string",
	func(in, want string) { Expect(rgwtext.CString(in)).To(Equal(want), "%q", in) },
	Entry("no NUL", "abc", "abc"),
	Entry("a NUL ends it", "ab\x00c\x00d", "ab"),
	Entry("a NUL first leaves it empty", "\x00abc", ""),
	Entry("empty", "", ""),
)
