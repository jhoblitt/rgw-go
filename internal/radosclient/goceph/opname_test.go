package goceph_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

var _ = Describe("an operation's name in the seam's errors", func() {
	DescribeTable("names an object of a credential index by the index's kind, never its id",
		func(namespace, oid, want string) {
			Expect(goceph.OpName(namespace, "read", oid)).To(Equal(want))
		},
		Entry("an access key's index", "users.keys", "AKHIDDEN", "read (user key index)"),
		Entry("an email's index", "users.email", "hidden@example.com", "read (user email index)"),
		Entry("a Swift key's index", "users.swift", "alice:hidden", "read (user swift index)"),
	)
	It("names any other object by its id", func() {
		Expect(goceph.OpName("users.uid", "read", "alice")).To(Equal(`read "alice"`))
	})
})
