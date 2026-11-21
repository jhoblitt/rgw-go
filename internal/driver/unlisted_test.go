package driver_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("an unlisted version's record key", func() {
	DescribeTable("holds no NUL and reads back as the version it names",
		func(name, instance string) {
			key := meta.ObjKey{Name: name, Instance: instance}
			k := driver.UnlistedKeyForTest(key)
			Expect(k).NotTo(ContainSubstring("\x00"))
			got, ok := driver.ParseUnlistedKeyForTest(k)
			Expect(ok).To(BeTrue())
			Expect(got).To(Equal(key))
		},
		Entry("a name and an instance", "k", "v1"),
		Entry("the null version", "k", ""),
		Entry("a NUL in the name", "a\x00b", "v1"),
		Entry("slashes and percent signs in the name", "dir/50%/x", "v1"),
		Entry("escapes in the name, taken as they are", "a%2Fb%00c%25", "v1"),
		Entry("a name with a leading underscore, which the index escapes", "_k", "v1"),
		Entry("an instance of radosgw's alphabet", "k", "aB3.-_xYz"),
	)

	DescribeTable("refuses a key that names no version",
		func(k string) {
			_, ok := driver.ParseUnlistedKeyForTest(k)
			Expect(ok).To(BeFalse())
		},
		Entry("no separator", "k"),
		Entry("no name", "/v1"),
		Entry("two separators", "k/v/1"),
		Entry("a bad escape", "k%zz/v1"),
	)
})
