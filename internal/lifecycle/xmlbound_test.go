package lifecycle_test

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// maxPutParamSize is rgw_max_put_param_size's default, the most a lifecycle
// body may hold.
const maxPutParamSize = 1 << 20

var startTag = regexp.MustCompile(`<[A-Za-z]`)

// units is how many elements and attributes the generated document doc
// starts, each attribute counting as one as the reader counts it. Every
// attribute the specs write is double-quoted.
func units(doc []byte) int {
	return len(startTag.FindAllIndex(doc, -1)) + strings.Count(string(doc), `="`)
}

// storageClasses are the six S3 moves a version through.
var storageClasses = []string{"STANDARD_IA", "ONEZONE_IA", "INTELLIGENT_TIERING", "GLACIER_IR", "GLACIER", "DEEP_ARCHIVE"}

// heavyRule is a rule with every part a rule may hold: a filter of a prefix,
// the two sizes and tags tags; an expiration; a noncurrent expiration; and a
// transition and a noncurrent transition to each of transitions storage
// classes. A rule that filters by tag may neither abort uploads nor expire
// delete markers.
func heavyRule(i, tags, transitions int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<Rule><ID>r%d</ID><Status>Enabled</Status><Filter><And><Prefix>p/</Prefix>"+
		"<ObjectSizeGreaterThan>1</ObjectSizeGreaterThan><ObjectSizeLessThan>9</ObjectSizeLessThan>", i)
	for t := range tags {
		fmt.Fprintf(&b, "<Tag><Key>k%d</Key><Value>v</Value></Tag>", t)
	}
	b.WriteString("</And></Filter><Expiration><Days>400</Days></Expiration>" +
		"<NoncurrentVersionExpiration><NoncurrentDays>9</NoncurrentDays><NewerNoncurrentVersions>3</NewerNoncurrentVersions></NoncurrentVersionExpiration>")
	for t := range transitions {
		fmt.Fprintf(&b, "<Transition><Days>%d</Days><StorageClass>%s</StorageClass></Transition>", t+1, storageClasses[t])
		fmt.Fprintf(&b, "<NoncurrentVersionTransition><NoncurrentDays>%d</NoncurrentDays><StorageClass>%s</StorageClass></NoncurrentVersionTransition>", t+1, storageClasses[t])
	}
	b.WriteString("</Rule>")
	return b.String()
}

// heavyDoc is a configuration of heavy rules, up to rules of them and as
// many as rgw_max_put_param_size holds.
func heavyDoc(rules, tags, transitions int) []byte {
	var parts []string
	size := len(lcDoc())
	for i := range rules {
		r := heavyRule(i, tags, transitions)
		if size+len(r) > maxPutParamSize {
			break
		}
		size += len(r)
		parts = append(parts, r)
	}
	return lcDoc(parts...)
}

var _ = Describe("Parse against the bounds on a document's elements and depth", func() {
	// S3 allows an object ten tags and moves a version through six storage
	// classes, so a rule of ten tags and six transitions of each kind is the
	// largest an S3 client writes. The most elements rgw_max_put_param_size
	// can hold is a rule of more tags, which radosgw takes though no object
	// can match it: a tag is three elements in 39 bytes, the densest of any
	// document.
	DescribeTable("reads the largest document radosgw's limits allow",
		func(rules, tags, transitions, minElements int) {
			doc := heavyDoc(rules, tags, transitions)
			Expect(len(doc)).To(BeNumerically("<=", maxPutParamSize))
			c, err := lifecycle.Parse(doc, lifecycle.DefaultMaxRules)
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Rules).To(HaveLen(strings.Count(string(doc), "<Rule>")))
			elements := units(doc)
			Expect(elements).To(BeNumerically(">=", minElements), "the document is as dense as it was measured")
			Expect(elements*3/2).To(BeNumerically("<=", xmltext.MaxElements), "the bound is at least half again the largest document")
		},
		Entry("rgw_lc_max_rules rules of S3's ten tags", lifecycle.DefaultMaxRules, 10, 0, 43000),
		Entry("S3's largest rules, as many as 1 MiB holds", lifecycle.DefaultMaxRules, 10, 6, 40000),
		Entry("one rule of as many tags as 1 MiB holds", 1, 23000, 0, 69000),
	)

	It("answers MalformedXML for a document past the bound, as it does for one that does not parse", func() {
		past := lcDoc(lcRule(enabled, noPfx, days1, strings.Repeat("<x/>", xmltext.MaxElements)))
		_, err := lifecycle.Parse(past, lifecycle.DefaultMaxRules)
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		Expect(err.Error()).To(ContainSubstring("too many elements"))

		nested := lcDoc(lcRule(enabled, noPfx, days1, strings.Repeat("<x>", xmltext.MaxDepth)+strings.Repeat("</x>", xmltext.MaxDepth)))
		_, err = lifecycle.Parse(nested, lifecycle.DefaultMaxRules)
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		Expect(err.Error()).To(ContainSubstring("nested too deeply"))
	})
})
