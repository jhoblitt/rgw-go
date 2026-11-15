package cors_test

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

const (
	// maxPutParamSize is rgw_max_put_param_size's default, the most a CORS
	// body may hold, and maxRules rgw_cors_rules_max_num's, the most rules.
	maxPutParamSize = 1 << 20
	maxRules        = 100
)

var startTag = regexp.MustCompile(`<[A-Za-z]`)

// units is how many elements and attributes the generated document doc
// starts, each attribute counting as one as the reader counts it. Every
// attribute the specs write is double-quoted.
func units(doc string) int {
	return len(startTag.FindAllStringIndex(doc, -1)) + strings.Count(doc, `="`)
}

// corsDoc is a CORSConfiguration holding rules.
func corsDoc(rules ...string) string {
	return `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` + strings.Join(rules, "") + `</CORSConfiguration>`
}

// repeated is n elements named name, numbered when numbered.
func repeated(name, text string, n int, numbered bool) string {
	var b strings.Builder
	for i := range n {
		t := text
		if numbered {
			t += strconv.Itoa(i)
		}
		b.WriteString("<" + name + ">" + t + "</" + name + ">")
	}
	return b.String()
}

// fullRule is a rule with each part repeated: the five methods S3 names, and
// n origins, headers and exposed headers.
func fullRule(i, n int) string {
	return fmt.Sprintf("<CORSRule><ID>rule-%d</ID>", i) +
		"<AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod><AllowedMethod>POST</AllowedMethod>" +
		"<AllowedMethod>DELETE</AllowedMethod><AllowedMethod>HEAD</AllowedMethod>" +
		repeated("AllowedOrigin", "https://o.example.com/", n, true) +
		repeated("AllowedHeader", "x-amz-h", n, true) +
		repeated("ExposeHeader", "x-amz-e", n, true) +
		"<MaxAgeSeconds>3000</MaxAgeSeconds></CORSRule>"
}

var _ = Describe("ParseXML against the bounds on a document's elements and depth", func() {
	// A rule holds as many origins and headers as its bytes allow, so the
	// densest document spends rgw_max_put_param_size on one-byte headers in
	// the rgw_cors_rules_max_num rules radosgw takes.
	DescribeTable("reads the largest document radosgw's limits allow",
		func(rules []string, wantRules, minElements int) {
			doc := corsDoc(rules...)
			Expect(len(doc)).To(BeNumerically("<=", maxPutParamSize))
			c, err := cors.ParseXML([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Rules).To(HaveLen(wantRules))
			elements := units(doc)
			Expect(elements).To(BeNumerically(">=", minElements))
			Expect(elements*3/2).To(BeNumerically("<=", xmltext.MaxElements), "the bound is at least half again the largest document")
		},
		Entry("rules of ten of each name", func() []string {
			rules := make([]string, maxRules)
			for i := range rules {
				rules[i] = fullRule(i, 10)
			}
			return rules
		}(), maxRules, 0),
		Entry("rules of one-byte headers", func() []string {
			rules := make([]string, maxRules)
			for i := range rules {
				rules[i] = "<CORSRule><AllowedOrigin>*</AllowedOrigin>" + repeated("AllowedHeader", "a", 325, false) + "</CORSRule>"
			}
			return rules
		}(), maxRules, 0),
	)

	It("answers MalformedXML for a document past the bound, as it does for one that does not parse", func() {
		past := corsDoc("<CORSRule><AllowedOrigin>*</AllowedOrigin>" + strings.Repeat("<x/>", xmltext.MaxElements) + "</CORSRule>")
		_, err := cors.ParseXML([]byte(past))
		Expect(err).To(MatchError(cors.ErrMalformedXML))
		Expect(err).To(MatchError(xmltext.ErrTooManyElements))

		nested := corsDoc("<CORSRule><AllowedOrigin>*</AllowedOrigin>" + strings.Repeat("<x>", xmltext.MaxDepth) + strings.Repeat("</x>", xmltext.MaxDepth) + "</CORSRule>")
		_, err = cors.ParseXML([]byte(nested))
		Expect(err).To(MatchError(cors.ErrMalformedXML))
		Expect(err).To(MatchError(xmltext.ErrTooDeep))
	})
})
