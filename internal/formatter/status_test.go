package formatter_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/formatter"
)

var _ = Describe("StatusName", func() {
	DescribeTable("is the name radosgw's http_codes gives a status",
		func(status int, name string) {
			Expect(formatter.StatusName(status)).To(Equal(name), "status %d", status)
		},
		Entry("a name Go gives too", 404, "Not Found"),
		Entry("a name Go spells otherwise", 503, "Slow Down"),
		Entry("radosgw's misspelling", 305, "User Proxy"),
		Entry("a status Go has no name for", 498, "Rate Limited"),
		Entry("a status the table lacks, Locked", 423, ""),
		Entry("a status below 100", -1, ""),
	)

	It("names the 42 statuses http_codes lists and no other", func() {
		named := 0
		for status := range 1000 {
			if formatter.StatusName(status) != "" {
				named++
			}
		}
		Expect(named).To(Equal(42), "rgw_rest.cc:45-86 at v19.2.7 and v20.2.4")
	})
})
