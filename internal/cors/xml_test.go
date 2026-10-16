package cors_test

import (
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cors"
)

const xmlns = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

// doc wraps rules in a CORSConfiguration.
func doc(rules ...string) []byte {
	return []byte("<CORSConfiguration " + xmlns + ">" + strings.Join(rules, "") + "</CORSConfiguration>")
}

// rule wraps elements in a CORSRule.
func rule(elems ...string) string { return "<CORSRule>" + strings.Join(elems, "") + "</CORSRule>" }

func el(name, text string) string { return "<" + name + ">" + text + "</" + name + ">" }

// origin is the AllowedOrigin every rule needs.
var origin = el("AllowedOrigin", "*")

// parseOne parses a document of one rule and returns the rule.
func parseOne(elems ...string) cors.Rule {
	GinkgoHelper()
	c, err := cors.ParseXML(doc(rule(elems...)))
	Expect(err).NotTo(HaveOccurred())
	Expect(c.Rules).To(HaveLen(1))
	return c.Rules[0]
}

var _ = Describe("ParseXML", func() {
	It("reads the two-rule vector's document into its configuration", func() {
		body, err := os.ReadFile("testdata/vectors/two-rules.xml")
		Expect(err).NotTo(HaveOccurred())
		c, err := cors.ParseXML(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(c).To(Equal(twoRules()))
		Expect(c.Encode()).To(Equal(unhex(twoRulesBin)))
	})
	It("accepts a rule with no AllowedMethod", func() {
		r := parseOne(origin)
		Expect(r).To(Equal(cors.Rule{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}}))
	})
	It("reads each method name in any case, COPY included", func() {
		r := parseOne(origin, el("AllowedMethod", "gEt"), el("AllowedMethod", "put"), el("AllowedMethod", "Head"),
			el("AllowedMethod", "POST"), el("AllowedMethod", "delete"), el("AllowedMethod", "copy"), el("AllowedMethod", "GET"))
		Expect(r.Methods).To(Equal(cors.MethodGet | cors.MethodPut | cors.MethodHead | cors.MethodPost | cors.MethodDelete | cors.MethodCopy))
	})
	It("keeps an ID of 255 bytes, and only the first ID", func() {
		id := strings.Repeat("i", 255)
		Expect(parseOne(origin, el("ID", id), el("ID", strings.Repeat("j", 300))).ID).To(Equal(id))
	})
	It("keeps the origins and headers sorted with each once, and the expose headers as written", func() {
		r := parseOne(el("AllowedOrigin", "b"), el("AllowedOrigin", "a*"), el("AllowedOrigin", "b"),
			el("AllowedHeader", "Z"), el("AllowedHeader", "a"), el("AllowedHeader", "Z"),
			el("ExposeHeader", "z"), el("ExposeHeader", ""), el("ExposeHeader", "a"), el("ExposeHeader", "z"))
		Expect(r.AllowedOrigins).To(Equal([]string{"a*", "b"}))
		Expect(r.AllowedHeaders).To(Equal([]string{"Z", "a"}))
		Expect(r.ExposeHeaders).To(Equal([]string{"z", "", "a", "z"}))
	})
	It("keeps an expose header's newline", func() {
		Expect(parseOne(origin, el("ExposeHeader", "a\nb")).ExposeHeaders).To(Equal([]string{"a\nb"}))
	})
	It("reads a carriage return reference as expat reports it, and a literal CRLF as a newline", func() {
		Expect(parseOne(origin, el("ExposeHeader", "a&#13;b\r\nc")).ExposeHeaders).To(Equal([]string{"a\rb\nc"}))
	})
	It("takes an element's own text untrimmed", func() {
		r := parseOne(el("AllowedOrigin", " http://a "), el("ID", "\tid\n"))
		Expect(r.AllowedOrigins).To(Equal([]string{" http://a "}))
		Expect(r.ID).To(Equal("\tid\n"))
	})
	It("reads only the root's own CORSRule children as rules", func() {
		c, err := cors.ParseXML(doc(rule(el("Other", rule(el("AllowedOrigin", "inner"))), el("AllowedOrigin", "outer"))))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules).To(HaveLen(1))
		Expect(c.Rules[0].AllowedOrigins).To(Equal([]string{"outer"}))
	})
	DescribeTable("MaxAgeSeconds as strtoull reads it",
		func(text string, want uint32) {
			Expect(parseOne(origin, el("MaxAgeSeconds", text)).MaxAge).To(Equal(want))
		},
		Entry("a plain number", "3000", uint32(3000)),
		Entry("zero", "0", uint32(0)),
		Entry("an empty element, which strtoull reads as 0", "", uint32(0)),
		Entry("leading whitespace", " \t\n7", uint32(7)),
		Entry("a plus sign", "+7", uint32(7)),
		Entry("negative zero", "-0", uint32(0)),
		Entry("the largest u32 below the invalid marker", "4294967294", uint32(4294967294)),
		Entry("2^32 - 1, the invalid marker itself", "4294967295", cors.MaxAgeInvalid),
		Entry("2^32, stored as invalid", "4294967296", cors.MaxAgeInvalid),
		Entry("a negative number, which strtoull wraps", "-1", cors.MaxAgeInvalid),
		Entry("a number past 2^64, which strtoull saturates", "99999999999999999999999", cors.MaxAgeInvalid),
		Entry("a negative number past 2^64", "-99999999999999999999999", cors.MaxAgeInvalid),
		Entry("leading zeros", "007", uint32(7)),
		Entry("a leading carriage return reference, the one C space beyond space, tab and line feed XML carries", "&#13;5", uint32(5)),
	)
	It("takes only the first MaxAgeSeconds", func() {
		Expect(parseOne(origin, el("MaxAgeSeconds", "5"), el("MaxAgeSeconds", "junk")).MaxAge).To(Equal(uint32(5)))
	})
	It("leaves MaxAge invalid without a MaxAgeSeconds", func() {
		Expect(parseOne(origin).MaxAge).To(Equal(cors.MaxAgeInvalid))
	})
	It("ignores elements it does not know", func() {
		Expect(parseOne(origin, el("Unknown", "x"), el("s3:AllowedOrigin", "y"))).To(Equal(cors.Rule{
			MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"},
		}))
	})

	DescribeTable("refuses as MalformedXML",
		func(body []byte) {
			c, err := cors.ParseXML(body)
			Expect(err).To(MatchError(cors.ErrMalformedXML))
			Expect(c).To(BeNil())
		},
		Entry("an empty body", []byte{}),
		Entry("a document that is not well-formed", []byte("<CORSConfiguration><CORSRule>")),
		Entry("a root other than CORSConfiguration", []byte("<Foo>"+rule(origin)+"</Foo>")),
		Entry("a prefixed root, read by its name as written", []byte("<s3:CORSConfiguration xmlns:s3=\"x\">"+rule(origin)+"</s3:CORSConfiguration>")),
		Entry("a CORSConfiguration with no CORSRule", doc()),
		Entry("a CORSConfiguration whose only rules are prefixed", doc("<s3:CORSRule xmlns:s3=\"x\">"+origin+"</s3:CORSRule>")),
		Entry("an AllowedMethod radosgw has no flag for", doc(rule(origin, el("AllowedMethod", "PATCH")))),
		Entry("an AllowedMethod that matches only under Unicode case folding", doc(rule(origin, el("AllowedMethod", "POſT")))),
		Entry("an AllowedMethod with whitespace", doc(rule(origin, el("AllowedMethod", " GET")))),
		Entry("an empty AllowedMethod", doc(rule(origin, el("AllowedMethod", "")))),
		Entry("an ID over 255 bytes", doc(rule(origin, el("ID", strings.Repeat("i", 256))))),
		Entry("a rule with no AllowedOrigin", doc(rule(el("AllowedMethod", "GET")))),
		Entry("an empty AllowedOrigin", doc(rule(el("AllowedOrigin", "")))),
		Entry("an AllowedOrigin with two wildcards", doc(rule(el("AllowedOrigin", "*.example.*")))),
		Entry("an empty AllowedHeader", doc(rule(origin, el("AllowedHeader", "")))),
		Entry("an AllowedHeader with two wildcards", doc(rule(origin, el("AllowedHeader", "x-*-*")))),
		Entry("a MaxAgeSeconds with trailing junk", doc(rule(origin, el("MaxAgeSeconds", "10s")))),
		Entry("a MaxAgeSeconds with trailing whitespace", doc(rule(origin, el("MaxAgeSeconds", "10 ")))),
		Entry("a MaxAgeSeconds of whitespace alone", doc(rule(origin, el("MaxAgeSeconds", "  ")))),
		Entry("a MaxAgeSeconds of a sign alone", doc(rule(origin, el("MaxAgeSeconds", "-")))),
		Entry("a MaxAgeSeconds in hex", doc(rule(origin, el("MaxAgeSeconds", "0x10")))),
		Entry("a MaxAgeSeconds with a non-ASCII digit", doc(rule(origin, el("MaxAgeSeconds", "١")))),
		Entry("a bad rule after a good one", doc(rule(origin), rule(el("AllowedMethod", "GET")))),
		Entry("a CORSRule nested anywhere that is itself bad, as every CORSRule's xml_end runs",
			doc(rule(origin, el("Other", rule(el("AllowedMethod", "GET")))))),
		Entry("a CORSConfiguration nested anywhere with no CORSRule", doc(rule(origin, el("CORSConfiguration", "")))),
	)
})

var _ = Describe("MarshalS3XML", func() {
	It("renders the two-rule vector as GetBucketCors does", func() {
		c, err := cors.Decode(unhex(twoRulesBin))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(c.MarshalS3XML())).To(Equal(`<CORSConfiguration ` + xmlns + `>` +
			`<CORSRule><ID>web</ID><AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod>` +
			`<AllowedOrigin>https://*.example.com</AllowedOrigin><AllowedOrigin>https://www.example.com</AllowedOrigin>` +
			`<AllowedHeader>Content-Type</AllowedHeader><AllowedHeader>X-Amz-*</AllowedHeader>` +
			`<MaxAgeSeconds>3000</MaxAgeSeconds>` +
			`<ExposeHeader>x-amz-request-id</ExposeHeader><ExposeHeader>ETag</ExposeHeader></CORSRule>` +
			`<CORSRule><AllowedMethod>GET</AllowedMethod><AllowedOrigin>*</AllowedOrigin></CORSRule>` +
			`</CORSConfiguration>`))
	})
	It("writes the methods in to_xml's order, whatever bits are set", func() {
		c := &cors.Configuration{Rules: []cors.Rule{{
			MaxAge: cors.MaxAgeInvalid, Methods: 0xff, AllowedOrigins: []string{"*"},
		}}}
		Expect(string(c.MarshalS3XML())).To(Equal(`<CORSConfiguration ` + xmlns + `><CORSRule>` +
			`<AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod><AllowedMethod>DELETE</AllowedMethod>` +
			`<AllowedMethod>HEAD</AllowedMethod><AllowedMethod>POST</AllowedMethod><AllowedMethod>COPY</AllowedMethod>` +
			`<AllowedOrigin>*</AllowedOrigin></CORSRule></CORSConfiguration>`))
	})
	It("writes a max age of 0, and none for the invalid marker", func() {
		c := &cors.Configuration{Rules: []cors.Rule{
			{MaxAge: 0, AllowedOrigins: []string{"a"}},
			{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"b"}},
		}}
		Expect(string(c.MarshalS3XML())).To(Equal(`<CORSConfiguration ` + xmlns + `>` +
			`<CORSRule><AllowedOrigin>a</AllowedOrigin><MaxAgeSeconds>0</MaxAgeSeconds></CORSRule>` +
			`<CORSRule><AllowedOrigin>b</AllowedOrigin></CORSRule></CORSConfiguration>`))
	})
	It("escapes text as XMLFormatter does and sorts the sets a caller built out of order", func() {
		c := &cors.Configuration{Rules: []cors.Rule{{
			ID: `<"&'>`, MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"b&", "a<", "b&"},
			AllowedHeaders: []string{"y", "x"}, ExposeHeaders: []string{"a\nb", "\r"},
		}}}
		Expect(string(c.MarshalS3XML())).To(Equal(`<CORSConfiguration ` + xmlns + `><CORSRule>` +
			`<ID>&lt;&quot;&amp;&apos;&gt;</ID><AllowedOrigin>a&lt;</AllowedOrigin><AllowedOrigin>b&amp;</AllowedOrigin>` +
			`<AllowedHeader>x</AllowedHeader><AllowedHeader>y</AllowedHeader>` +
			"<ExposeHeader>a\nb</ExposeHeader><ExposeHeader>&#x0d;</ExposeHeader></CORSRule></CORSConfiguration>"))
	})
	It("closes an empty configuration with an end tag", func() {
		Expect(string((&cors.Configuration{}).MarshalS3XML())).To(Equal(`<CORSConfiguration ` + xmlns + `></CORSConfiguration>`))
	})
	It("reads back what it writes", func() {
		c := twoRules()
		got, err := cors.ParseXML(c.MarshalS3XML())
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(c))
	})
})
