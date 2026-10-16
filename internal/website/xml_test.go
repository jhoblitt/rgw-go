package website_test

import (
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/website"
)

const s3NS = "http://s3.amazonaws.com/doc/2006-03-01/"

// doc is a WebsiteConfiguration in S3's namespace holding inner.
func doc(inner string) []byte {
	return []byte(`<WebsiteConfiguration xmlns="` + s3NS + `">` + inner + `</WebsiteConfiguration>`)
}

const (
	index    = "<IndexDocument><Suffix>index.html</Suffix></IndexDocument>"
	redirect = "<Redirect><HostName>h</HostName></Redirect>"
	redirAll = "<RedirectAllRequestsTo><HostName>h</HostName></RedirectAllRequestsTo>"
)

func routingRules(rules ...string) string {
	return "<RoutingRules>" + strings.Join(rules, "") + "</RoutingRules>"
}

func routingRule(inner string) string { return "<RoutingRule>" + inner + "</RoutingRule>" }

// nRules is n routing rules that each redirect a numbered prefix.
func nRules(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(routingRule("<Condition><KeyPrefixEquals>p" + strconv.Itoa(i) + "/</KeyPrefixEquals></Condition>" + redirect))
	}
	return routingRules(b.String())
}

// refusal matches a *website.ParseError of the sentinel err with message msg.
func refusal(err error, msg string) types.GomegaMatcher {
	return SatisfyAll(MatchError(err), BeAssignableToTypeOf(&website.ParseError{}), HaveField("Message", msg))
}

// long is text far past any refusal's quoting.
var long = strings.Repeat("x", 4096)

const (
	msgNoHost        = `A host name must be provided to redirect all requests (e.g. "example.com").`
	msgNoIndex       = "A value for IndexDocument Suffix must be provided if RedirectAllRequestsTo is empty"
	msgSuffixMissing = "The IndexDocument Suffix is not well formed"
)

var _ = Describe("ParseXML", func() {
	Context("a document it accepts", func() {
		It("reads the index document, the error document and the routing rules in document order", func() {
			body := doc(index + "<ErrorDocument><Key>error.html</Key></ErrorDocument>" + routingRules(
				routingRule("<Condition><KeyPrefixEquals>docs/</KeyPrefixEquals></Condition><Redirect><ReplaceKeyPrefixWith>documents/</ReplaceKeyPrefixWith></Redirect>"),
				routingRule("<Condition><HttpErrorCodeReturnedEquals>404</HttpErrorCodeReturnedEquals></Condition>"+
					"<Redirect><Protocol>https</Protocol><HostName>example.com</HostName><HttpRedirectCode>302</HttpRedirectCode><ReplaceKeyWith>404.html</ReplaceKeyWith></Redirect>"),
			))
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{
				IndexDocSuffix: "index.html",
				ErrorDoc:       "error.html",
				RoutingRules: meta.RoutingRules{
					{
						Condition:    meta.RoutingCondition{KeyPrefixEquals: "docs/"},
						RedirectInfo: meta.RoutingRedirect{ReplaceKeyPrefixWith: "documents/"},
					},
					{
						Condition: meta.RoutingCondition{HTTPErrorCodeReturnedEquals: 404},
						RedirectInfo: meta.RoutingRedirect{
							Redirect:       meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 302},
							ReplaceKeyWith: "404.html",
						},
					},
				},
			}))
		})

		It("reads RedirectAllRequestsTo with and without a Protocol", func() {
			body := doc("<RedirectAllRequestsTo><HostName>example.com</HostName><Protocol>https</Protocol></RedirectAllRequestsTo>")
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com", Protocol: "https"}}))
			body = doc("<RedirectAllRequestsTo><HostName>example.com</HostName></RedirectAllRequestsTo>")
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com"}}))
		})

		It("reads an index document alone, with no routing rules", func() {
			Expect(website.ParseXML(doc(index), 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html"}))
			Expect(website.ParseXML(doc(index+"<RoutingRules></RoutingRules>"), 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html"}))
		})

		It("reads a document without a namespace", func() {
			body := []byte("<WebsiteConfiguration>" + index + "</WebsiteConfiguration>")
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html"}))
		})

		It("reads a body that starts with a byte order mark and an XML declaration", func() {
			body := append([]byte("\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?>"), doc(index)...)
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html"}))
		})

		It("reads a document laid out with whitespace, comments and processing instructions between its elements", func() {
			body := []byte("<WebsiteConfiguration xmlns=\"" + s3NS + "\">\n  <!-- site -->\n  <IndexDocument>\n\t<Suffix>i</Suffix>\r\n  </IndexDocument>\n" +
				"  <RoutingRules>\n    <RoutingRule> <?note a?> <Redirect> <HostName>h</HostName> </Redirect> </RoutingRule>\n  </RoutingRules>\n</WebsiteConfiguration>\n")
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{
				IndexDocSuffix: "i",
				RoutingRules:   meta.RoutingRules{{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "h"}}}},
			}))
		})

		It("reads a rule with a Redirect alone, even an empty one", func() {
			body := doc(index + routingRules(routingRule("<Redirect/>"), routingRule("<Redirect></Redirect>")))
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: meta.RoutingRules{{}, {}}}))
		})

		It("reads an empty Condition, as radosgw renders a rule without one, as no condition", func() {
			body := doc(index + routingRules(
				routingRule("<Condition></Condition>"+redirect),
				routingRule("<Condition/>"+redirect),
				routingRule("<Condition> <!-- none --> </Condition>"+redirect),
			))
			none := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "h"}}}
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: meta.RoutingRules{none, none, none}}))
		})

		It("reads a Condition that holds one of its fields, even an empty one", func() {
			body := doc(index + routingRules(routingRule("<Condition><KeyPrefixEquals/></Condition><Redirect/>")))
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: meta.RoutingRules{{}}}))
		})

		It("reads an empty RoutingRules as absent, beside RedirectAllRequestsTo too, as both SDKs write an empty list", func() {
			for _, body := range [][]byte{
				doc(redirAll + "<RoutingRules/>"),
				doc("<RoutingRules></RoutingRules>" + redirAll),
				doc(redirAll + "<RoutingRules> <!-- none --> </RoutingRules>"),
				[]byte(`<WebsiteConfiguration xmlns="` + s3NS + `"><RedirectAllRequestsTo><HostName>h</HostName></RedirectAllRequestsTo><RoutingRules /></WebsiteConfiguration>`),
			} {
				Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "h"}}), "body %s", body)
			}
		})

		It("reads a HostName of other bytes as written, non-ASCII ones included", func() {
			body := doc("<RedirectAllRequestsTo><HostName>b&#252;cher.example:8080</HostName></RedirectAllRequestsTo>")
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "b\xc3\xbccher.example:8080"}}))
		})

		It("reads an error document Key of one byte", func() {
			body := doc(index + "<ErrorDocument><Key> </Key></ErrorDocument>")
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html", ErrorDoc: " "}))
		})

		It("keeps an element's own text as written, references and CDATA decoded and comments dropped", func() {
			body := doc(`<IndexDocument><Suffix> a&amp;b<![CDATA[<c>]]><!-- x -->&#x41; </Suffix></IndexDocument>`)
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: " a&b<c>A "}))
		})

		It("reads S3's default namespace declared again on a child", func() {
			body := doc(`<IndexDocument xmlns="` + s3NS + `"><Suffix xmlns="` + s3NS + `">i</Suffix></IndexDocument>`)
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "i"}))
		})

		DescribeTable("reads an HttpRedirectCode as strtol in base 10 does",
			func(text string, want uint16) {
				conf, err := website.ParseXML(doc(index+routingRules(routingRule("<Redirect><HttpRedirectCode>"+text+"</HttpRedirectCode></Redirect>"))), 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(conf.RoutingRules[0].RedirectInfo.Redirect.HTTPRedirectCode).To(Equal(want), "HttpRedirectCode %q", text)
			},
			Entry("the lowest code", "301", uint16(301)),
			Entry("the highest code", "399", uint16(399)),
			Entry("XML whitespace before it", " \t\r\n302", uint16(302)),
			Entry("XML whitespace after it", "302 \t\n", uint16(302)),
			Entry("a plus sign", "+302", uint16(302)),
			Entry("leading zeros", "000302", uint16(302)),
		)

		DescribeTable("reads an HttpErrorCodeReturnedEquals as strtol in base 10 does",
			func(text string, want uint16) {
				conf, err := website.ParseXML(doc(index+routingRules(routingRule("<Condition><HttpErrorCodeReturnedEquals>"+text+"</HttpErrorCodeReturnedEquals></Condition>"+redirect))), 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(conf.RoutingRules[0].Condition.HTTPErrorCodeReturnedEquals).To(Equal(want), "HttpErrorCodeReturnedEquals %q", text)
			},
			Entry("the lowest code", "400", uint16(400)),
			Entry("the highest code", "599", uint16(599)),
			Entry("whitespace and a sign", " +0404\n", uint16(404)),
		)
	})

	Context("MalformedXML, where radosgw answers InvalidArgument or reads the document", func() {
		DescribeTable("refuses a document that does not parse, or whose structure S3's schema does not admit",
			func(body []byte) {
				_, err := website.ParseXML(body, 50)
				Expect(err).To(refusal(website.ErrMalformedXML, ""))
			},
			Entry("a body that does not parse", []byte("<WebsiteConfiguration>"+index)),
			Entry("an empty body", []byte("")),
			Entry("text that is not XML", []byte("index.html")),
			Entry("a second root", append(doc(index), doc(index)...)),
			Entry("a root other than WebsiteConfiguration", []byte("<WebsiteConfig>"+index+"</WebsiteConfig>")),
			Entry("an empty root other than WebsiteConfiguration", []byte("<WebsiteConfig/>")),
			Entry("a document type declaration", []byte(`<!DOCTYPE WebsiteConfiguration [<!ENTITY s "index.html">]>`+
				`<WebsiteConfiguration><IndexDocument><Suffix>&s;</Suffix></IndexDocument></WebsiteConfiguration>`)),
			Entry("a document type declaration that declares nothing", []byte("<!DOCTYPE WebsiteConfiguration>"+string(doc(index)))),
			Entry("a prefixed root", []byte(`<s3:WebsiteConfiguration>`+index+`</s3:WebsiteConfiguration>`)),
			Entry("a prefixed element", doc(`<s3:IndexDocument><s3:Suffix>i</s3:Suffix></s3:IndexDocument>`)),
			Entry("an unknown element under the root", doc(index+"<Other/>")),
			Entry("an unknown element in IndexDocument", doc("<IndexDocument><Suffix>i</Suffix><Other/></IndexDocument>")),
			Entry("an unknown element in ErrorDocument", doc(index+"<ErrorDocument><Key>e</Key><Other/></ErrorDocument>")),
			Entry("an unknown element in RedirectAllRequestsTo", doc("<RedirectAllRequestsTo><HostName>h</HostName><Port>80</Port></RedirectAllRequestsTo>")),
			Entry("an unknown element in RoutingRules", doc(index+routingRules("<Other/>"+routingRule(redirect)))),
			Entry("a misspelled Condition in a RoutingRule", doc(index+routingRules(routingRule("<Condtion><KeyPrefixEquals>old/</KeyPrefixEquals></Condtion>"+redirect)))),
			Entry("an unknown element in a Condition", doc(index+routingRules(routingRule("<Condition><KeyPrefixEquals>a</KeyPrefixEquals><Extra/></Condition>"+redirect)))),
			Entry("an unknown element in a Redirect", doc(index+routingRules(routingRule("<Redirect><HostName>h</HostName><Port>8080</Port></Redirect>")))),
			Entry("an element where another level holds it", doc(index+routingRules(routingRule("<Redirect><Condition/></Redirect>")))),
			Entry("an index Suffix holding an element", doc("<IndexDocument><Suffix>index.html<b/></Suffix></IndexDocument>")),
			Entry("an error document Key holding an element", doc(index+"<ErrorDocument><Key><b/>e</Key></ErrorDocument>")),
			Entry("a redirect-all HostName holding an element", doc("<RedirectAllRequestsTo><HostName>h<b/></HostName></RedirectAllRequestsTo>")),
			Entry("a KeyPrefixEquals holding an element", doc(index+routingRules(routingRule("<Condition><KeyPrefixEquals><b/></KeyPrefixEquals></Condition>"+redirect)))),
			Entry("a ReplaceKeyWith holding an element", doc(index+routingRules(routingRule("<Redirect><ReplaceKeyWith>k<b/></ReplaceKeyWith></Redirect>")))),
			Entry("an attribute on the root", []byte(`<WebsiteConfiguration xmlns="`+s3NS+`" a="1">`+index+`</WebsiteConfiguration>`)),
			Entry("an attribute on a field", doc(`<IndexDocument><Suffix c="3">i</Suffix></IndexDocument>`)),
			Entry("a prefixed attribute", doc(`<IndexDocument xsi:type="T"><Suffix>i</Suffix></IndexDocument>`)),
			Entry("a prefix declared for S3's namespace", []byte(`<WebsiteConfiguration xmlns="`+s3NS+`" xmlns:s3="`+s3NS+`">`+index+`</WebsiteConfiguration>`)),
			Entry("a default namespace other than S3's", []byte(`<WebsiteConfiguration xmlns="urn:other">`+index+`</WebsiteConfiguration>`)),
			Entry("an empty default namespace", []byte(`<WebsiteConfiguration xmlns="">`+index+`</WebsiteConfiguration>`)),
			Entry("another namespace declared on a child", doc(`<IndexDocument xmlns="urn:other"><Suffix>i</Suffix></IndexDocument>`)),
			Entry("text in the root", doc("text"+index)),
			Entry("text in IndexDocument", doc("<IndexDocument>x<Suffix>i</Suffix></IndexDocument>")),
			Entry("text in RoutingRules", doc(index+routingRules("x"+routingRule(redirect)))),
			Entry("text in a RoutingRule", doc(index+routingRules(routingRule(redirect+"x")))),
			Entry("text in a Condition", doc(index+routingRules(routingRule("<Condition>x<KeyPrefixEquals>a</KeyPrefixEquals></Condition>"+redirect)))),
			Entry("text in a Redirect", doc(index+routingRules(routingRule("<Redirect><HostName>h</HostName>x</Redirect>")))),
			Entry("a no-break space between elements", doc("\xc2\xa0"+index)),
			Entry("a body over 1 MiB", append(doc(index), []byte(strings.Repeat(" ", website.MaxXML+1-len(doc(index))))...)),
		)

		DescribeTable("refuses what S3's model rules out (aws-sdk-go v1.55.8 service/s3/api.go)",
			func(body []byte) {
				_, err := website.ParseXML(body, 50)
				Expect(err).To(refusal(website.ErrMalformedXML, ""))
			},
			Entry("RedirectAllRequestsTo beside an IndexDocument", doc(redirAll+index)),
			Entry("RedirectAllRequestsTo after an ErrorDocument", doc("<ErrorDocument><Key>e</Key></ErrorDocument>"+redirAll)),
			Entry("RedirectAllRequestsTo beside RoutingRules holding a rule", doc(redirAll+routingRules(routingRule(redirect)))),
			Entry("RedirectAllRequestsTo beside two empty RoutingRules", doc(redirAll+"<RoutingRules/><RoutingRules></RoutingRules>")),
			Entry("RedirectAllRequestsTo beside an empty RoutingRules and an IndexDocument", doc(redirAll+"<RoutingRules/>"+index)),
			Entry("an ErrorDocument without a Key", doc(index+"<ErrorDocument></ErrorDocument>")),
			Entry("an empty error document Key", doc(index+"<ErrorDocument><Key/></ErrorDocument>")),
			Entry("a Suffix holding a slash", doc("<IndexDocument><Suffix>docs/index.html</Suffix></IndexDocument>")),
			Entry("a Suffix holding a slash written as a reference", doc("<IndexDocument><Suffix>a&#47;b</Suffix></IndexDocument>")),
			Entry("a RoutingRule without a Redirect", doc(index+routingRules(routingRule("<Condition><KeyPrefixEquals>a</KeyPrefixEquals></Condition>")))),
			Entry("an empty RoutingRule", doc(index+routingRules(routingRule("")))),
		)

		DescribeTable("refuses a repeated element the schema allows once, which radosgw reads the first of",
			func(body []byte) {
				_, err := website.ParseXML(body, 50)
				Expect(err).To(refusal(website.ErrMalformedXML, ""))
			},
			Entry("RedirectAllRequestsTo", doc(redirAll+redirAll)),
			Entry("the redirect-all HostName", doc("<RedirectAllRequestsTo><HostName>h</HostName><HostName>i</HostName></RedirectAllRequestsTo>")),
			Entry("the redirect-all Protocol", doc("<RedirectAllRequestsTo><HostName>h</HostName><Protocol>http</Protocol><Protocol>http</Protocol></RedirectAllRequestsTo>")),
			Entry("IndexDocument", doc(index+index)),
			Entry("Suffix", doc("<IndexDocument><Suffix>a</Suffix><Suffix>b</Suffix></IndexDocument>")),
			Entry("ErrorDocument", doc(index+"<ErrorDocument><Key>e</Key></ErrorDocument><ErrorDocument><Key>e</Key></ErrorDocument>")),
			Entry("Key", doc(index+"<ErrorDocument><Key>e</Key><Key>f</Key></ErrorDocument>")),
			Entry("RoutingRules", doc(index+routingRules(routingRule(redirect))+routingRules(routingRule(redirect)))),
			Entry("Condition", doc(index+routingRules(routingRule(strings.Repeat("<Condition><KeyPrefixEquals>a</KeyPrefixEquals></Condition>", 2)+redirect)))),
			Entry("Redirect", doc(index+routingRules(routingRule(redirect+redirect)))),
			Entry("KeyPrefixEquals", doc(index+routingRules(routingRule("<Condition><KeyPrefixEquals>a</KeyPrefixEquals><KeyPrefixEquals>b</KeyPrefixEquals></Condition>"+redirect)))),
			Entry("HttpErrorCodeReturnedEquals", doc(index+routingRules(routingRule(
				"<Condition><HttpErrorCodeReturnedEquals>404</HttpErrorCodeReturnedEquals><HttpErrorCodeReturnedEquals>404</HttpErrorCodeReturnedEquals></Condition>"+redirect)))),
			Entry("a rule's Protocol", doc(index+routingRules(routingRule("<Redirect><Protocol>http</Protocol><Protocol>http</Protocol></Redirect>")))),
			Entry("a rule's HostName", doc(index+routingRules(routingRule("<Redirect><HostName>h</HostName><HostName>h</HostName></Redirect>")))),
			Entry("HttpRedirectCode", doc(index+routingRules(routingRule("<Redirect><HttpRedirectCode>301</HttpRedirectCode><HttpRedirectCode>301</HttpRedirectCode></Redirect>")))),
			Entry("ReplaceKeyPrefixWith", doc(index+routingRules(routingRule("<Redirect><ReplaceKeyPrefixWith>a</ReplaceKeyPrefixWith><ReplaceKeyPrefixWith>b</ReplaceKeyPrefixWith></Redirect>")))),
			Entry("ReplaceKeyWith", doc(index+routingRules(routingRule("<Redirect><ReplaceKeyWith>a</ReplaceKeyWith><ReplaceKeyWith>b</ReplaceKeyWith></Redirect>")))),
		)

		DescribeTable("refuses a Protocol other than http or https, which radosgw stores and writes into the Location",
			func(protocol string) {
				_, err := website.ParseXML(doc("<RedirectAllRequestsTo><HostName>h</HostName><Protocol>"+protocol+"</Protocol></RedirectAllRequestsTo>"), 50)
				Expect(err).To(refusal(website.ErrMalformedXML, ""), "redirect-all Protocol %q", protocol)
				_, err = website.ParseXML(doc(index+routingRules(routingRule("<Redirect><Protocol>"+protocol+"</Protocol></Redirect>"))), 50)
				Expect(err).To(refusal(website.ErrMalformedXML, ""), "rule Protocol %q", protocol)
			},
			Entry("another scheme", "ftp"),
			Entry("upper case", "HTTP"),
			Entry("an empty one", ""),
			Entry("whitespace before", " https"),
			Entry("whitespace after", "http\n"),
		)

		It("accepts http and https in both places", func() {
			for _, p := range []string{"http", "https"} {
				conf, err := website.ParseXML(doc("<RedirectAllRequestsTo><HostName>h</HostName><Protocol>"+p+"</Protocol></RedirectAllRequestsTo>"), 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(conf.RedirectAll.Protocol).To(Equal(p))
				conf, err = website.ParseXML(doc(index+routingRules(routingRule("<Redirect><Protocol>"+p+"</Protocol></Redirect>"))), 50)
				Expect(err).NotTo(HaveOccurred())
				Expect(conf.RoutingRules[0].RedirectInfo.Redirect.Protocol).To(Equal(p))
			}
		})

		It("answers MalformedXML before an InvalidArgument earlier in the document", func() {
			body := doc(index + routingRules(
				routingRule("<Redirect><HttpRedirectCode>200</HttpRedirectCode></Redirect>"),
				routingRule("<Redirect><Protocol>ftp</Protocol></Redirect>"),
			))
			_, err := website.ParseXML(body, 50)
			Expect(err).To(refusal(website.ErrMalformedXML, ""))
		})

		It("reads a body of exactly 1 MiB", func() {
			body := append(doc(index), []byte(strings.Repeat(" ", website.MaxXML-len(doc(index))))...)
			Expect(body).To(HaveLen(website.MaxXML))
			Expect(website.ParseXML(body, 50)).To(Equal(meta.WebsiteConf{IndexDocSuffix: "index.html"}))
		})
	})

	DescribeTable("keeps a refusal's text short when the document's own text is long",
		func(body []byte) {
			_, err := website.ParseXML(body, 50)
			Expect(err).To(HaveOccurred())
			Expect(len(err.Error())).To(BeNumerically("<", 512), "refusal text %.80q", err.Error())
		},
		Entry("a long Protocol", doc("<RedirectAllRequestsTo><HostName>h</HostName><Protocol>"+long+"</Protocol></RedirectAllRequestsTo>")),
		Entry("a long root name", []byte("<"+long+"/>")),
		Entry("a long unknown element name", doc("<"+long+"/>")),
		Entry("a long attribute name", doc(`<IndexDocument `+long+`="v"/>`)),
		Entry("a long attribute value", doc(`<IndexDocument xmlns="`+long+`"/>`)),
		Entry("a long processing-instruction target xmltext refuses", []byte("<?"+long+"\"x?><WebsiteConfiguration/>")),
		Entry("a long prefixed name", doc("<p:"+long+"/>")),
		Entry("a long undefined entity, which the scan's decoder refuses", doc("<IndexDocument><Suffix>&"+long+";</Suffix></IndexDocument>")),
		Entry("a long declaration", []byte("<!DOCTYPE "+long+"><WebsiteConfiguration/>")),
		Entry("long text between elements", doc(long)),
		Entry("a long HttpRedirectCode", doc(index+routingRules(routingRule("<Redirect><HttpRedirectCode>"+long+"</HttpRedirectCode></Redirect>")))),
	)

	Context("InvalidArgument with no message, as radosgw's decode refusals carry none", func() {
		DescribeTable("refuses",
			func(body []byte) {
				_, err := website.ParseXML(body, 50)
				Expect(err).To(refusal(website.ErrInvalidArgument, ""))
			},
			Entry("RedirectAllRequestsTo without a HostName", doc("<RedirectAllRequestsTo><Protocol>https</Protocol></RedirectAllRequestsTo>")),
			Entry("a redirect-all HostName holding CR and LF", doc("<RedirectAllRequestsTo><HostName>h&#13;&#10;Set-Cookie: a=b</HostName></RedirectAllRequestsTo>")),
			Entry("a redirect-all HostName holding DEL", doc("<RedirectAllRequestsTo><HostName>h&#127;</HostName></RedirectAllRequestsTo>")),
			Entry("a rule's HostName holding a space", doc(index+routingRules(routingRule("<Redirect><HostName>a b</HostName></Redirect>")))),
			Entry("a rule's HostName holding a tab", doc(index+routingRules(routingRule("<Redirect><HostName>a\tb</HostName></Redirect>")))),
			Entry("ReplaceKeyPrefixWith beside ReplaceKeyWith", doc(index+routingRules(routingRule(
				"<Redirect><ReplaceKeyPrefixWith>a</ReplaceKeyPrefixWith><ReplaceKeyWith>b</ReplaceKeyWith></Redirect>")))),
			Entry("an empty ReplaceKeyPrefixWith beside an empty ReplaceKeyWith", doc(index+routingRules(routingRule(
				"<Redirect><ReplaceKeyWith/><ReplaceKeyPrefixWith></ReplaceKeyPrefixWith></Redirect>")))),
			Entry("a decode refusal before a message refusal", doc(routingRules(routingRule("<Redirect><HttpRedirectCode>300</HttpRedirectCode></Redirect>")))),
		)

		DescribeTable("refuses an HttpRedirectCode that is not 3XX other than 300",
			func(text string) {
				_, err := website.ParseXML(doc(index+routingRules(routingRule("<Redirect><HttpRedirectCode>"+text+"</HttpRedirectCode></Redirect>"))), 50)
				Expect(err).To(refusal(website.ErrInvalidArgument, ""), "HttpRedirectCode %q", text)
			},
			Entry("300", "300"),
			Entry("400", "400"),
			Entry("zero", "0"),
			Entry("a negative code", "-301"),
			Entry("a word", "found"),
			Entry("an empty code", ""),
			Entry("whitespace alone", "  "),
			Entry("a sign alone", "+"),
			Entry("text after the digits", "301x"),
			Entry("a space inside", "3 01"),
			Entry("a hexadecimal code", "0x12D"),
			Entry("past int's range, which would wrap to 301", "4294967597"),
			Entry("past long's range", "99999999999999999999301"),
			Entry("past long's range, which would wrap to 301", "18446744073709551917"),
			Entry("a no-break space after the digits", "301\xc2\xa0"),
		)

		DescribeTable("refuses an HttpErrorCodeReturnedEquals that is not 4XX or 5XX",
			func(text string) {
				_, err := website.ParseXML(doc(index+routingRules(routingRule("<Condition><HttpErrorCodeReturnedEquals>"+text+"</HttpErrorCodeReturnedEquals></Condition>"+redirect))), 50)
				Expect(err).To(refusal(website.ErrInvalidArgument, ""), "HttpErrorCodeReturnedEquals %q", text)
			},
			Entry("399", "399"),
			Entry("600", "600"),
			Entry("an empty code", ""),
			Entry("a word", "missing"),
			Entry("past u16's range, which would wrap to 404", "65940"),
		)
	})

	Context("InvalidArgument with radosgw's message", func() {
		DescribeTable("refuses",
			func(body []byte, msg string) {
				_, err := website.ParseXML(body, 50)
				Expect(err).To(refusal(website.ErrInvalidArgument, msg))
			},
			Entry("an empty redirect-all HostName", doc("<RedirectAllRequestsTo><HostName></HostName></RedirectAllRequestsTo>"), msgNoHost),
			Entry("neither RedirectAllRequestsTo nor IndexDocument", doc("<ErrorDocument><Key>e</Key></ErrorDocument>"), msgNoIndex),
			Entry("an empty WebsiteConfiguration", doc(""), msgNoIndex),
			Entry("an IndexDocument without a Suffix", doc("<IndexDocument></IndexDocument>"), msgSuffixMissing),
			Entry("an empty Suffix", doc("<IndexDocument><Suffix/></IndexDocument>"), msgSuffixMissing),
		)
	})

	Context("the routing rule limit", func() {
		DescribeTable("allows maxRules rules, a negative maxRules meaning 50",
			func(n, maxRules int) {
				conf, err := website.ParseXML(doc(index+nRules(n)), maxRules)
				Expect(err).NotTo(HaveOccurred())
				Expect(conf.RoutingRules).To(HaveLen(n))
			},
			Entry("50 of 50", 50, 50),
			Entry("50 of a negative limit", 50, -1),
			Entry("3 of 3", 3, 3),
		)

		DescribeTable("refuses more than maxRules rules with InvalidRequest and radosgw's message",
			func(n, maxRules int, msg string) {
				_, err := website.ParseXML(doc(index+nRules(n)), maxRules)
				Expect(err).To(refusal(website.ErrTooManyRules, msg))
			},
			Entry("51 of 50", 51, 50, "51 routing rules provided, the number of routing rules in a website configuration is limited to 50."),
			Entry("51 of a negative limit", 51, -7, "51 routing rules provided, the number of routing rules in a website configuration is limited to 50."),
			Entry("4 of 3", 4, 3, "4 routing rules provided, the number of routing rules in a website configuration is limited to 3."),
			Entry("1 of 0", 1, 0, "1 routing rules provided, the number of routing rules in a website configuration is limited to 0."),
		)

		It("counts the rules after the message refusals", func() {
			_, err := website.ParseXML(doc(nRules(51)), 50)
			Expect(err).To(refusal(website.ErrInvalidArgument, msgNoIndex))
		})
	})
})

// fullConf sets every field the S3 document carries but RedirectAllRequestsTo.
func fullConf() meta.WebsiteConf {
	return meta.WebsiteConf{
		IndexDocSuffix: "index.html",
		ErrorDoc:       "error.html",
		RoutingRules: meta.RoutingRules{
			{
				Condition:    meta.RoutingCondition{KeyPrefixEquals: "docs/", HTTPErrorCodeReturnedEquals: 404},
				RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 302}, ReplaceKeyPrefixWith: "documents/"},
			},
			{
				Condition:    meta.RoutingCondition{HTTPErrorCodeReturnedEquals: 410},
				RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "gone.html"},
			},
		},
	}
}

var _ = Describe("MarshalS3XML", func() {
	It("writes GetBucketWebsite's document: the index, the error document, then the rules, each field when set", func() {
		Expect(string(website.MarshalS3XML(fullConf()))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<IndexDocument><Suffix>index.html</Suffix></IndexDocument>" +
			"<ErrorDocument><Key>error.html</Key></ErrorDocument>" +
			"<RoutingRules>" +
			"<RoutingRule><Condition><KeyPrefixEquals>docs/</KeyPrefixEquals><HttpErrorCodeReturnedEquals>404</HttpErrorCodeReturnedEquals></Condition>" +
			"<Redirect><Protocol>https</Protocol><HostName>example.com</HostName><HttpRedirectCode>302</HttpRedirectCode><ReplaceKeyPrefixWith>documents/</ReplaceKeyPrefixWith></Redirect></RoutingRule>" +
			"<RoutingRule><Condition><HttpErrorCodeReturnedEquals>410</HttpErrorCodeReturnedEquals></Condition><Redirect><ReplaceKeyWith>gone.html</ReplaceKeyWith></Redirect></RoutingRule>" +
			"</RoutingRules></WebsiteConfiguration>"))
	})

	It("writes RedirectAllRequestsTo with HostName before Protocol, and never its code", func() {
		conf := meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 302}}
		Expect(string(website.MarshalS3XML(conf))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<RedirectAllRequestsTo><HostName>example.com</HostName><Protocol>https</Protocol></RedirectAllRequestsTo></WebsiteConfiguration>"))
		conf.RedirectAll.Protocol = ""
		Expect(string(website.MarshalS3XML(conf))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<RedirectAllRequestsTo><HostName>example.com</HostName></RedirectAllRequestsTo></WebsiteConfiguration>"))
	})

	It("writes a RedirectAllRequestsTo with no HostName as nothing", func() {
		conf := meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https"}, IndexDocSuffix: "i"}
		Expect(string(website.MarshalS3XML(conf))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<IndexDocument><Suffix>i</Suffix></IndexDocument></WebsiteConfiguration>"))
	})

	It("writes each section on its own, so a redirect-all stored beside an index document writes both", func() {
		conf := meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "h"}, ErrorDoc: "e"}
		Expect(string(website.MarshalS3XML(conf))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<RedirectAllRequestsTo><HostName>h</HostName></RedirectAllRequestsTo><ErrorDocument><Key>e</Key></ErrorDocument></WebsiteConfiguration>"))
	})

	It("closes an empty configuration and an empty rule with end tags", func() {
		Expect(string(website.MarshalS3XML(meta.WebsiteConf{}))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `"></WebsiteConfiguration>`))
		Expect(string(website.MarshalS3XML(meta.WebsiteConf{RoutingRules: meta.RoutingRules{{}}}))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<RoutingRules><RoutingRule><Condition></Condition><Redirect></Redirect></RoutingRule></RoutingRules></WebsiteConfiguration>"))
	})

	It("writes a rule without a condition as an empty Condition, as dump_xml does, which a get then put reads back", func() {
		conf := meta.WebsiteConf{IndexDocSuffix: "i", RoutingRules: meta.RoutingRules{{RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "k"}}}}
		got := website.MarshalS3XML(conf)
		Expect(string(got)).To(ContainSubstring("<RoutingRule><Condition></Condition><Redirect>"))
		Expect(website.ParseXML(got, 50)).To(Equal(conf))
	})

	It("escapes text as XMLFormatter's dump_string does", func() {
		conf := meta.WebsiteConf{IndexDocSuffix: "a&<>'\"\r\x7f\tb"}
		Expect(string(website.MarshalS3XML(conf))).To(Equal(`<WebsiteConfiguration xmlns="` + s3NS + `">` +
			"<IndexDocument><Suffix>a&amp;&lt;&gt;&apos;&quot;&#x0d;&#x7f;\tb</Suffix></IndexDocument></WebsiteConfiguration>"))
	})

	It("writes a document ParseXML reads back as the configuration", func() {
		Expect(website.ParseXML(website.MarshalS3XML(fullConf()), 50)).To(Equal(fullConf()))
	})
})
