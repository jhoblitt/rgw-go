package rgwtext_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

var _ = DescribeTable("URLDecode is rgw_common.cc's url_decode",
	func(in string, inQuery bool, want string) {
		Expect(rgwtext.URLDecode(in, inQuery)).To(Equal(want), "%q, in query %v", in, inQuery)
	},
	Entry("plain", "/a/b", false, "/a/b"),
	Entry("empty", "", true, ""),
	Entry("percent escapes in either case", "/a%2Fb%41%6a%6A", false, "/a/bAjj"),
	Entry("a plus kept outside a query", "/a+b", false, "/a+b"),
	Entry("a plus is a space in a query", "a+b", true, "a b"),
	Entry("a literal ? switches to query mode", "/p+q?r+s", false, "/p+q?r s"),
	Entry("an escaped ? does not switch to query mode", "/p%3Fq+r", false, "/p?q+r"),
	Entry("an escaped plus stays a plus in a query", "a%2Bb", true, "a+b"),
	Entry("a bad hex digit empties the result", "/a%zzb", false, ""),
	Entry("a bad second hex digit empties the result", "/a%4zb", false, ""),
	Entry("a truncated escape stops decoding", "/a%4", false, "/a"),
	Entry("a lone percent sign stops decoding", "/a%", false, "/a"),
	Entry("a byte above 0x7f in an escape is a bad hex digit", "/a%\xc3\xbcb", false, ""),
	Entry("a byte above 0x7f outside an escape is copied", "/\xc3\xbc", false, "/\xc3\xbc"),
	Entry("an escaped NUL is kept", "a%00b", true, "a\x00b"),
)
