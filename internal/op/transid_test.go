package op_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("transaction ids", func() {
	It("formats tx, a 21-digit hex sequence, a dash, a 10-digit hex time and the suffix", func() {
		at := time.Unix(0x68d7a1b2, 0)
		Expect(op.TransID(1, at, "-4155-ceph-objectstore")).
			To(Equal("tx000000000000000000001-0068d7a1b2-4155-ceph-objectstore"))
	})
	It("builds the suffix as url-encoded -<instance>-<zone>", func() {
		Expect(op.TransIDSuffix(4155, "ceph-objectstore")).To(Equal("-4155-ceph-objectstore"))
		Expect(op.TransIDSuffix(7, "my zone")).To(Equal("-7-my%20zone"), "a space is percent-encoded")
	})
	It("builds the host id as <instance>-<zone>-<zonegroup>", func() {
		Expect(op.HostID(4155, "z", "zg")).To(Equal("4155-z-zg"))
	})
})

var _ = DescribeTable("URLEncode is radosgw's url_encode",
	func(in string, encodeSlash bool, want string) {
		Expect(op.URLEncode(in, encodeSlash)).To(Equal(want), "%q", in)
	},
	Entry("alphanumerics and the unreserved punctuation pass", "Az09!$'()*-._|~", true, "Az09!$'()*-._|~"),
	Entry("a space and a control byte", "a b\x01", true, "a%20b%01"),
	Entry("every character char_needs_url_encoding lists", "\"#%&+,/:;<=>?@[\\]^`{}", true,
		"%22%23%25%26%2B%2C%2F%3A%3B%3C%3D%3E%3F%40%5B%5C%5D%5E%60%7B%7D"),
	Entry("DEL and every byte above it, in upper-case hex", "\x7f\xc3\xa9", true, "%7F%C3%A9"),
	Entry("a slash passes when encodeSlash is false", "a/b c", false, "a/b%20c"),
)
