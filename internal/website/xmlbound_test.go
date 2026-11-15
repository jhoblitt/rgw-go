package website_test

import (
	"fmt"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/website"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// maxRoutingRules is rgw_website_routing_rules_max_num's default.
const maxRoutingRules = 50

var startTag = regexp.MustCompile(`<[A-Za-z]`)

// units is how many elements and attributes the generated document body
// starts, each attribute counting as one as the reader counts it. Every
// attribute the specs write is double-quoted.
func units(body []byte) int {
	return len(startTag.FindAllIndex(body, -1)) + strings.Count(string(body), `="`)
}

// fullRule is a routing rule with every element S3 gives one.
func fullRule(i int) string {
	return routingRule(fmt.Sprintf("<Condition><KeyPrefixEquals>p%d/</KeyPrefixEquals>"+
		"<HttpErrorCodeReturnedEquals>404</HttpErrorCodeReturnedEquals></Condition>"+
		"<Redirect><Protocol>https</Protocol><HostName>h.example.com</HostName>"+
		"<ReplaceKeyPrefixWith>q%d/</ReplaceKeyPrefixWith><HttpRedirectCode>301</HttpRedirectCode></Redirect>", i, i))
}

var _ = Describe("ParseXML against the bounds on a document's elements and depth", func() {
	It("reads the largest document radosgw's limits allow, rgw_website_routing_rules_max_num rules with every element", func() {
		var rules []string
		for i := range maxRoutingRules {
			rules = append(rules, fullRule(i))
		}
		body := doc(index + "<ErrorDocument><Key>error.html</Key></ErrorDocument>" + routingRules(rules...))
		conf, err := website.ParseXML(body, maxRoutingRules)
		Expect(err).NotTo(HaveOccurred())
		Expect(conf.RoutingRules).To(HaveLen(maxRoutingRules))
		elements := units(body)
		Expect(elements * 3 / 2).To(BeNumerically("<=", xmltext.MaxElements))
	})

	// A body is held to MaxXML and to the elements S3 gives a configuration
	// before it is built into a tree, so the bound is not reachable with
	// one: the most elements MaxXML holds in elements the reader admits is
	// the shortest rule over and over, which has far fewer than the bound.
	It("answers a body of the most routing rules MaxXML holds with the limit on rules, which the bound admits", func() {
		var b strings.Builder
		rule := routingRule(redirect)
		for b.Len()+len(rule) < website.MaxXML-200 {
			b.WriteString(rule)
		}
		body := doc(index + routingRules(b.String()))
		Expect(len(body)).To(BeNumerically("<=", website.MaxXML))
		Expect(units(body)).To(BeNumerically(">=", 44000), "the densest body MaxXML holds")
		Expect(units(body)).To(BeNumerically("<", xmltext.MaxElements))
		_, err := website.ParseXML(body, maxRoutingRules)
		Expect(err).To(MatchError(website.ErrTooManyRules))
	})
})
