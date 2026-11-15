package objectlock_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

var _ = Describe("the lock documents against the bounds on a document's elements and depth", func() {
	// The largest honest document of the three is a bucket's configuration,
	// six elements in four levels, so a document past either bound is no
	// client's. Each document here is valid but for the bound.
	DescribeTable("answer MalformedXML for a document past the bound, as for one that does not parse",
		func(root, inner string, parse func([]byte) error) {
			doc := func(junk string) []byte { return []byte("<" + root + ">" + inner + junk + "</" + root + ">") }
			Expect(parse(doc(""))).To(Succeed())

			err := parse(doc(strings.Repeat("<x/>", xmltext.MaxElements)))
			Expect(err).To(MatchError(objectlock.ErrMalformedXML))
			Expect(err.Error()).To(ContainSubstring("too many elements"))

			err = parse(doc(strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth)))
			Expect(err).To(MatchError(objectlock.ErrMalformedXML))
			Expect(err.Error()).To(ContainSubstring("nested too deeply"))
		},
		Entry("a bucket's configuration", "ObjectLockConfiguration", "<ObjectLockEnabled>Enabled</ObjectLockEnabled>",
			func(b []byte) error {
				_, err := objectlock.ParseConfiguration(b)
				return err
			}),
		Entry("an object's retention", "Retention", "<Mode>GOVERNANCE</Mode><RetainUntilDate>2030-01-01T00:00:00Z</RetainUntilDate>",
			func(b []byte) error {
				_, err := objectlock.ParseRetention(b)
				return err
			}),
		Entry("an object's legal hold", "LegalHold", "<Status>ON</Status>",
			func(b []byte) error {
				_, err := objectlock.ParseLegalHold(b)
				return err
			}),
	)
})
