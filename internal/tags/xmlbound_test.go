package tags_test

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

var startTag = regexp.MustCompile(`<[A-Za-z]`)

var _ = Describe("ParseXML against the bounds on a document's elements and depth", func() {
	It("reads the largest tag set S3 allows, a bucket's 50 tags of the longest key and value", func() {
		var b strings.Builder
		b.WriteString("<TagSet>")
		for i := range 50 {
			key := fmt.Sprintf("%0*d", tags.MaxKeyLen, i)
			fmt.Fprintf(&b, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", key, strings.Repeat("v", tags.MaxValueLen))
		}
		b.WriteString("</TagSet>")
		doc := tagging(b.String())
		s, err := tags.ParseXML(doc, 50)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(HaveLen(50))
		elements := len(startTag.FindAllIndex(doc, -1)) + strings.Count(string(doc), `="`)
		Expect(elements * 3 / 2).To(BeNumerically("<=", xmltext.MaxElements))
	})

	It("still answers InvalidTag for the most tags a 1 MiB body holds, which the bound admits", func() {
		doc := tagging(tagSetOf(23000))
		Expect(len(doc)).To(BeNumerically("<=", 1<<20))
		_, err := tags.ParseXML(doc, 50)
		Expect(err).To(MatchError(tags.ErrInvalidTag))
	})

	It("answers MalformedXML for a document past the bound, as it does for one that does not parse", func() {
		_, err := tags.ParseXML(tagging(tagSetOf(xmltext.MaxElements/3+1)), 50)
		Expect(err).To(MatchError(tags.ErrMalformedXML))
		Expect(err).To(MatchError(xmltext.ErrTooManyElements))

		nest := strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth)
		_, err = tags.ParseXML(tagging("<TagSet><Tag><Key>k</Key><Value>v</Value></Tag>"+nest+"</TagSet>"), 50)
		Expect(err).To(MatchError(tags.ErrMalformedXML))
		Expect(err).To(MatchError(xmltext.ErrTooDeep))
	})
})
