package xmltext_test

import (
	"errors"
	"slices"
	"unicode/utf16"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

const bom = "\xef\xbb\xbf"

// utf16Doc is s in UTF-16LE behind its byte order mark.
func utf16Doc(s string) string {
	b := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return string(b)
}

var _ = Describe("Parse", func() {
	// The verdicts are radosgw's, from libexpat 2.8.3 driven as RGWXMLParser
	// drives it, except where an entry names a difference that
	// docs/exclusions.md records.
	DescribeTable("reads a document expat reads",
		func(doc string) {
			_, err := xmltext.Parse([]byte(doc))
			Expect(err).NotTo(HaveOccurred(), "%q", doc)
		},
		Entry("a bare root", `<a/>`),
		Entry("whitespace around the root", " \t\r\n<a/>\n "),
		Entry("comments and processing instructions around the root", `<!-- c --><?p x?><a/><!-- d --><?p y?>`),
		Entry("a UTF-8 byte order mark", bom+`<a/>`),
		Entry("a byte order mark before the declaration", bom+`<?xml version="1.0"?><a/>`),
		Entry("a declaration", `<?xml version="1.0"?><a/>`),
		Entry("a declaration in single quotes", `<?xml version='1.0'?><a/>`),
		Entry("a declaration with whitespace around =", `<?xml version = "1.0" ?><a/>`),
		Entry("a declaration with UTF-8", `<?xml version="1.0" encoding="UTF-8"?><a/>`),
		Entry("a declaration with utf-8 in lower case", `<?xml version="1.0" encoding="utf-8"?><a/>`),
		Entry("a declaration with all three pseudo-attributes", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><a/>`),
		Entry("a declaration with standalone no", `<?xml version="1.0" standalone="no"?><a/>`),
		Entry("a declaration with trailing whitespace", "<?xml version=\"1.0\" encoding=\"UTF-8\"  \n?><a/>"),
		Entry("a processing instruction with data", `<?p d?><a/>`),
		Entry("a processing instruction whose target starts with xml", `<?xml-stylesheet href='x'?><a/>`),
		Entry("a DOCTYPE", `<!DOCTYPE a><a/>`),
		Entry("a DOCTYPE with an internal subset", `<!DOCTYPE a [<!ELEMENT a EMPTY>]><a/>`),
		Entry("a DOCTYPE after the declaration and a comment", `<?xml version="1.0"?><!-- c --><!DOCTYPE a><a/>`),
		Entry("a DOCTYPE naming another root", `<!DOCTYPE b><a/>`),
		Entry("one local name under two prefixes", `<a xmlns:p="u" p:x="1" x="2"/>`),
		Entry("an undeclared prefix", `<p:a q:x="1"/>`),
		Entry("CDATA and references inside the root", `<a><![CDATA[ ]]>&#32;&amp;</a>`),
		// expat refuses these; docs/exclusions.md records that rgw-go reads them.
		Entry("attributes without whitespace between them (a recorded difference)", `<a x='1'y='2'/>`),
		Entry("a malformed DTD internal subset (a recorded difference)", `<!DOCTYPE a [ garbage ]><a/>`),
		Entry("trailing garbage in a DOCTYPE (a recorded difference)", `<!DOCTYPE a SYSTEM "x.dtd" garbage><a/>`),
	)

	DescribeTable("refuses a document expat refuses",
		func(doc string) {
			_, err := xmltext.Parse([]byte(doc))
			Expect(err).To(MatchError(xmltext.ErrNotWellFormed), "%q", doc)
		},
		Entry("an empty document", ``),
		Entry("whitespace alone", "  \n"),
		Entry("text before the root", `x<a/>`),
		Entry("text after the root", `<a/>x`),
		Entry("a second root", `<a/><b/>`),
		Entry("CDATA whitespace before the root", `<![CDATA[ ]]><a/>`),
		Entry("CDATA whitespace after the root", `<a/><![CDATA[ ]]>`),
		Entry("a character reference to a space before the root", `&#32;<a/>`),
		Entry("a character reference to a newline after the root", `<a/>&#10;`),
		Entry("an entity reference after the root", `<a/>&amp;`),
		Entry("a second byte order mark", bom+bom+`<a/>`),
		Entry("an unclosed root", `<a>`),
		Entry("an end tag without a start tag", `</a>`),
		Entry("a mismatched end tag", `<a></b>`),
		Entry("a declaration without a version", `<?xml encoding="UTF-8"?><a/>`),
		Entry("an empty declaration", `<?xml?><a/>`),
		Entry("a declaration with an unknown pseudo-attribute", `<?xml version="1.0" foo="bar"?><a/>`),
		Entry("a declaration with encoding before version", `<?xml encoding="UTF-8" version="1.0"?><a/>`),
		Entry("a declaration with standalone before encoding", `<?xml version="1.0" standalone="yes" encoding="UTF-8"?><a/>`),
		Entry("a declaration without whitespace between pseudo-attributes", `<?xml version="1.0"encoding="UTF-8"?><a/>`),
		Entry("a declaration with standalone maybe", `<?xml version="1.0" standalone="maybe"?><a/>`),
		Entry("a declaration naming Version", `<?xml Version="1.0"?><a/>`),
		Entry("a declaration with a space in the version", `<?xml version="1.0 "?><a/>`),
		Entry("a declaration with an empty version", `<?xml version=""?><a/>`),
		Entry("a declaration with an empty encoding", `<?xml version="1.0" encoding=""?><a/>`),
		Entry("a declaration with mismatched quotes", `<?xml version="1.0'?><a/>`),
		Entry("a declaration with an unquoted version", `<?xml version=1.0?><a/>`),
		Entry("a declaration with a repeated version", `<?xml version="1.0" version="1.0"?><a/>`),
		Entry("a second declaration", `<?xml version="1.0"?><?xml version="1.0"?><a/>`),
		Entry("a declaration after whitespace", ` <?xml version="1.0"?><a/>`),
		Entry("a declaration after a comment", `<!-- c --><?xml version="1.0"?><a/>`),
		Entry("a declaration after the root", `<a/><?xml version="1.0"?>`),
		Entry("a declaration inside the root", `<a><?xml version="1.0"?></a>`),
		Entry("the target XML", `<?XML version="1.0"?><a/>`),
		Entry("the target Xml", `<?Xml version="1.0"?><a/>`),
		Entry("an encoding expat does not know", `<?xml version="1.0" encoding="windows-1252"?><a/>`),
		Entry("a processing instruction target followed by ?", `<?p?d?><a/>`),
		Entry("a processing instruction target followed by =", `<?p="d"?><a/>`),
		Entry("a second DOCTYPE", `<!DOCTYPE a><!DOCTYPE a><a/>`),
		Entry("a DOCTYPE after the root", `<a/><!DOCTYPE a>`),
		Entry("a DOCTYPE inside the root", `<a><!DOCTYPE a></a>`),
		Entry("a DOCTYPE without whitespace", `<!DOCTYPEa><a/>`),
		Entry("another declaration at the top level", `<!ELEMENT a EMPTY><a/>`),
		Entry("a duplicate attribute", `<a x="1" x="2"/>`),
		Entry("]]> in text", `<a>]]></a>`),
		Entry("-- in a comment", `<a><!-- a -- b --></a>`),
		Entry("a control character", "<a>\x01</a>"),
		Entry("an undeclared entity", `<a>&e;</a>`),
		Entry("< in an attribute value", `<a x="<"/>`),
		Entry("invalid UTF-8", "<a>\xff</a>"),
	)

	// expat reads these; docs/exclusions.md records that rgw-go refuses them.
	DescribeTable("refuses a document expat reads, as recorded",
		func(doc string) {
			_, err := xmltext.Parse([]byte(doc))
			Expect(err).To(MatchError(xmltext.ErrNotWellFormed), "%q", doc)
		},
		Entry("XML 1.1", `<?xml version="1.1"?><a/>`),
		Entry("XML 2.0", `<?xml version="2.0"?><a/>`),
		Entry("XML 1.1 with whitespace around =", `<?xml version = "1.1"?><a/>`),
		Entry("ISO-8859-1", `<?xml version="1.0" encoding="ISO-8859-1"?><a/>`),
		Entry("ISO-8859-1 with whitespace around =", `<?xml version="1.0" encoding = "ISO-8859-1"?><a/>`),
		Entry("US-ASCII", `<?xml version="1.0" encoding="US-ASCII"?><a/>`),
		Entry("UTF-16 behind its byte order mark", utf16Doc(`<a/>`)),
		Entry("a name with two colons", `<a:b:c/>`),
		Entry("an entity the DTD declares", `<!DOCTYPE a [<!ENTITY e "x">]><a>&e;</a>`),
		Entry("an undeclared entity beside an external DTD subset", `<!DOCTYPE a SYSTEM "x.dtd"><a>&foo;</a>`),
		Entry("an undeclared entity beside a parameter-entity reference", `<!DOCTYPE a [<!ENTITY % p SYSTEM "p.dtd"> %p;]><a>&foo;</a>`),
	)

	It("keeps names and attribute names as written, with their prefixes", func() {
		root, err := xmltext.Parse([]byte(`<s3:Root xmlns:s3="u" xmlns="v" xsi:type="T" type="U"><s3:Child/><Child/></s3:Root>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Name).To(Equal("s3:Root"))
		Expect(root.Attrs).To(Equal(map[string]string{"xmlns:s3": "u", "xmlns": "v", "xsi:type": "T", "type": "U"}))
		Expect(root.Children).To(HaveLen(2))
		Expect(root.Children[0].Name).To(Equal("s3:Child"))
		Expect(root.Children[1].Name).To(Equal("Child"))
	})

	It("gives each element the text directly inside it, CDATA and references decoded, its children's excluded", func() {
		root, err := xmltext.Parse([]byte("<a>x<![CDATA[<y>]]>&amp;&#65;<!-- c -->z<b>inner</b>\r\nw</a>"))
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Text).To(Equal("x<y>&Az\nw"))
		Expect(root.FindFirst("b").Text).To(Equal("inner"))
	})

	It("reads a DTD attribute default without applying it, where expat applies it (a recorded difference)", func() {
		root, err := xmltext.Parse([]byte(`<!DOCTYPE a [<!ATTLIST a t CDATA "d">]><a/>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Attrs).To(BeEmpty())
	})

	It("gives an element without text or attributes empty ones", func() {
		root, err := xmltext.Parse([]byte(`<a/>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(root).To(Equal(&xmltext.Element{Name: "a", Attrs: map[string]string{}}))
	})

	It("finds children by name, as written, in document order", func() {
		root, err := xmltext.Parse([]byte(`<r><g>1</g><s3:g>2</s3:g><h/><g>3</g><x><g>4</g></x></r>`))
		Expect(err).NotTo(HaveOccurred())
		Expect(root.FindFirst("g").Text).To(Equal("1"))
		Expect(root.FindFirst("s3:g").Text).To(Equal("2"))
		Expect(root.FindFirst("none")).To(BeNil())
		var texts []string
		for g := range root.Find("g") {
			texts = append(texts, g.Text)
		}
		Expect(texts).To(Equal([]string{"1", "3"}))
		Expect(slices.Collect(root.Find("none"))).To(BeEmpty())
	})

	It("stops finding when the caller stops ranging", func() {
		root, err := xmltext.Parse([]byte(`<r><g>1</g><g>2</g></r>`))
		Expect(err).NotTo(HaveOccurred())
		var first []string
		for g := range root.Find("g") {
			first = append(first, g.Text)
			break
		}
		Expect(first).To(Equal([]string{"1"}))
	})
})

var _ = Describe("ParseFunc", func() {
	It("calls end as each element ends, children before parents, with its text and children complete", func() {
		var ended []string
		root, err := xmltext.ParseFunc([]byte(`<r>t<a><b>x</b></a><c/></r>`), func(e *xmltext.Element) error {
			ended = append(ended, e.Name+"("+e.Text+")"+string(rune('0'+len(e.Children))))
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(root.Name).To(Equal("r"))
		Expect(ended).To(Equal([]string{"b(x)0", "a()1", "c()0", "r(t)2"}))
	})

	It("fails the parse with end's error, as an xml_end returning false does", func() {
		refused := errors.New("refused")
		var ended []string
		_, err := xmltext.ParseFunc([]byte(`<r><a/><b/><c/></r>`), func(e *xmltext.Element) error {
			ended = append(ended, e.Name)
			if e.Name == "b" {
				return refused
			}
			return nil
		})
		Expect(err).To(BeIdenticalTo(refused))
		Expect(ended).To(Equal([]string{"a", "b"}))
	})

	It("refuses a document that is not well-formed before end sees the element that makes it so", func() {
		var ended []string
		_, err := xmltext.ParseFunc([]byte(`<r><a/></b>`), func(e *xmltext.Element) error {
			ended = append(ended, e.Name)
			return nil
		})
		Expect(err).To(MatchError(xmltext.ErrNotWellFormed))
		Expect(ended).To(Equal([]string{"a"}))
	})
})
