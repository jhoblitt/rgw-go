package xmltext_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

const s3NS = "http://s3.amazonaws.com/doc/2006-03-01/"

// docSchema is a document whose root, Doc, holds a field Name and a field
// Flag of at most 8 bytes, each once, and a List holding Item fields, any
// number of them.
func docSchema(lenient bool, namespaces ...string) *xmltext.Schema {
	return &xmltext.Schema{
		Root: "Doc",
		Children: map[string][]xmltext.Child{
			"Doc":  {{Name: "Name"}, {Name: "List"}, {Name: "Flag", MaxText: 8}},
			"List": {{Name: "Item", Repeats: true}},
		},
		Namespaces: append([]string{s3NS}, namespaces...),
		Lenient:    lenient,
	}
}

// scanRefusal scans body, which s must refuse, and returns the refusal.
func scanRefusal(s *xmltext.Schema, body string, end func(xmltext.Closed) error) *xmltext.ScanError {
	GinkgoHelper()
	err := xmltext.Scan([]byte(body), s, end)
	Expect(err).To(BeAssignableToTypeOf(&xmltext.ScanError{}), "body %.120q", body)
	se, _ := errors.AsType[*xmltext.ScanError](err)
	return se
}

// verdict is what a table makes of a body: the refusal, and the marker the
// refused token starts at, or no refusal.
type verdict struct {
	refusal xmltext.Refusal
	marker  string
}

func at(r xmltext.Refusal, marker string) verdict { return verdict{r, marker} }

// endOfBody is the marker of a refusal at the body's end.
const endOfBody = "\x00the end"

// atEnd is the verdict on a body refused at its end.
func atEnd(r xmltext.Refusal) verdict { return verdict{r, endOfBody} }

// reads is the verdict on a body the table admits.
var reads = verdict{}

// attrs is n attributes with empty values, a0 to a(n-1).
func attrs(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(" a" + strconv.Itoa(i) + `=""`)
	}
	return b.String()
}

var _ = Describe("Scan", func() {
	// An entry with one verdict holds it under both leniencies; one with two
	// holds the first under the strict table and the second under the
	// lenient one.
	DescribeTable("refuses the first token the table does not admit, once per leniency",
		func(body string, verdicts ...verdict) {
			if len(verdicts) == 1 {
				verdicts = append(verdicts, verdicts[0])
			}
			for i, v := range verdicts {
				lenient := i == 1
				s := docSchema(lenient)
				if v == reads {
					Expect(xmltext.Scan([]byte(body), s, nil)).To(Succeed(), "lenient %v, body %q", lenient, body)
					continue
				}
				off := strings.Index(body, v.marker)
				if v.marker == endOfBody {
					off = len(body)
				}
				Expect(off).To(BeNumerically(">=", 0), "marker %q in %q", v.marker, body)
				se := scanRefusal(s, body, nil)
				Expect(se.Refusal).To(Equal(v.refusal), "lenient %v, body %q: %v", lenient, body, se)
				Expect(se.Offset).To(Equal(int64(off)), "lenient %v, body %q: %v", lenient, body, se)
				Expect(se).To(MatchError(HaveSuffix(" at byte %d", off)))
			}
		},
		Entry("a document the table admits",
			`<Doc xmlns="`+s3NS+`"> <Name>n</Name> <List><Item>a</Item><Item/></List> <Flag>12345678</Flag> </Doc>`, reads),
		Entry("comments, processing instructions, CDATA and references",
			`<?xml version="1.0"?><!-- c --><Doc><?p x?><Name>a<![CDATA[<b>]]>&amp;<!-- d --></Name></Doc><!-- e -->`, reads),
		Entry("the table's namespace declared again on a child", `<Doc xmlns="`+s3NS+`"><Name xmlns="`+s3NS+`"/></Doc>`, reads),
		Entry("an element the table lets repeat, repeated", `<Doc><List><Item/><Item/><Item/></List></Doc>`, reads),
		Entry("XML whitespace around the root", " \t\r\n<Doc/> \n", reads),
		Entry("a well-formed processing instruction in each place", "<?p x?><Doc><?p?><Name>a<?p\ty?>b</Name></Doc><?p z?>", reads),
		Entry("a field's decoded text at its bound", `<Doc><Flag>&#49;234567&amp;</Flag></Doc>`, reads),

		Entry("a token Go's decoder cannot read", `<Doc><Name>&e;</Name></Doc>`, at(xmltext.RefusedSyntax, "&e;")),
		Entry("a document type declaration, whose internal subset can declare entities",
			`<!DOCTYPE Doc [<!ENTITY e "x">]><Doc><Name>&e;</Name></Doc>`, at(xmltext.RefusedDeclaration, "<!DOCTYPE")),
		Entry("a declaration after the root", `<Doc/><!DOCTYPE Doc>`, at(xmltext.RefusedDeclaration, "<!DOCTYPE")),
		Entry("another root", `<Other><Name/></Other>`, at(xmltext.RefusedRoot, "<Other>")),
		Entry("a root whose name differs in case", `<doc/>`, at(xmltext.RefusedRoot, "<doc/>")),
		Entry("a prefixed root, even one bound to the table's namespace", `<p:Doc xmlns:p="`+s3NS+`"/>`, at(xmltext.RefusedRoot, "<p:Doc")),
		Entry("another root, refused before its attributes", `<Other xmlns="urn:x" a="1"/>`, at(xmltext.RefusedRoot, "<Other")),
		Entry("a second root", `<Doc></Doc><Doc/>`, at(xmltext.RefusedSecondRoot, "<Doc/>")),
		Entry("a second root of another name, refused before its name", `<Doc/> <Other/>`, at(xmltext.RefusedSecondRoot, "<Other/>")),
		Entry("an end tag without a start tag", `</Doc>`, at(xmltext.RefusedEndTag, "</Doc>")),
		Entry("an end tag after the root", `<Doc/></Doc>`, at(xmltext.RefusedEndTag, "</Doc>")),
		Entry("an end tag closing another element", `<Doc><List></Doc>`, at(xmltext.RefusedEndTag, "</Doc>")),
		Entry("an end tag closing the same name under a prefix", `<Doc><Name></p:Name></Doc>`, at(xmltext.RefusedEndTag, "</p:Name>")),
		Entry("an element inside a field", `<Doc><Name>a<b/></Name></Doc>`, at(xmltext.RefusedNested, "<b/>")),
		Entry("an element inside a field, refused before its prefix", `<Doc><Name><p:b/></Name></Doc>`, at(xmltext.RefusedNested, "<p:b/>")),
		Entry("a table element inside a field", `<Doc><Name><Name/></Name></Doc>`, at(xmltext.RefusedNested, "<Name/>")),
		Entry("a prefixed element", `<Doc><p:Name/></Doc>`, at(xmltext.RefusedPrefix, "<p:Name/>")),
		Entry("a prefixed element the table does not name", `<Doc><p:Other/></Doc>`, at(xmltext.RefusedPrefix, "<p:Other/>")),
		Entry("a repeated field", `<Doc><Name/><Name>b</Name></Doc>`, at(xmltext.RefusedRepeated, "<Name>")),
		Entry("a repeated container", `<Doc><List/><List><Item/></List></Doc>`, at(xmltext.RefusedRepeated, "<List>")),
		Entry("a repeated element, refused before its attributes", `<Doc><Name/><Name a="1"/></Doc>`, at(xmltext.RefusedRepeated, "<Name a")),
		Entry("a repeated element after an empty one", `<Doc><Flag></Flag><Flag/></Doc>`, at(xmltext.RefusedRepeated, "<Flag/>")),
		Entry("a default namespace other than the table's", `<Doc xmlns="urn:x"/>`, at(xmltext.RefusedNamespace, "<Doc")),
		Entry("the table's namespace without its trailing slash", `<Doc xmlns="http://s3.amazonaws.com/doc/2006-03-01"/>`,
			at(xmltext.RefusedNamespace, "<Doc")),
		Entry("another default namespace on a child", `<Doc><Name xmlns="urn:x"/></Doc>`, at(xmltext.RefusedNamespace, "<Name")),
		Entry("an empty default namespace the table does not take", `<Doc xmlns=""/>`, at(xmltext.RefusedNamespace, "<Doc")),
		Entry("text before the root", `x<Doc/>`, at(xmltext.RefusedText, "x<")),
		Entry("text after the root", `<Doc/>x`, at(xmltext.RefusedText, "x")),
		Entry("a character reference to a letter after the root", `<Doc/>&#65;`, at(xmltext.RefusedText, "&#65;")),
		Entry("a field's text past its bound, refused where the text passes it", `<Doc><Flag>1234<!-- c -->56789</Flag></Doc>`,
			at(xmltext.RefusedTextLength, "56789")),
		Entry("a field's text past its bound in one token", `<Doc><Flag>123456789</Flag></Doc>`, at(xmltext.RefusedTextLength, "123456789")),
		Entry("an offset counted from the byte order mark", "\xef\xbb\xbf<Other/>", at(xmltext.RefusedRoot, "<Other/>")),

		// What Parse refuses, Scan refuses too, so no refused body builds a
		// tree. One met before the root is refused at the root's start tag,
		// which another root's refusal precedes, as the root's own name is
		// judged before what the decoder passed on the way to it.
		Entry("a reference to a space after the root, which decodes to whitespace", `<Doc/>&#32;`, at(xmltext.RefusedText, "&#32;")),
		Entry("a reference to a newline after the root", `<Doc/>&#10;`, at(xmltext.RefusedText, "&#10;")),
		Entry("CDATA holding whitespace after the root", `<Doc/><![CDATA[ ]]>`, at(xmltext.RefusedText, "<![CDATA")),
		Entry("a reference to a space before the root", `&#32;<Doc/>`, at(xmltext.RefusedText, "&#32;")),
		Entry("CDATA holding whitespace before the root", `<![CDATA[ ]]><Doc><Name/></Doc>`, at(xmltext.RefusedText, "<![CDATA")),
		Entry("a reference to a space before another root, whose refusal precedes it", `&#32;<Other/>`, at(xmltext.RefusedRoot, "<Other")),
		Entry("a reference to a space and no root", `&#32;`, at(xmltext.RefusedText, "&#32;")),
		Entry("a reference to a space before the root, refused at the root before what follows it", `&#32;<Doc><Other/></Doc>`,
			at(xmltext.RefusedText, "&#32;")),
		Entry("two of Parse's refusals before the root, the first refused", `&#32;<?p*?><Doc/>`, at(xmltext.RefusedText, "&#32;")),
		Entry("an XML declaration after whitespace", ` <?xml version="1.0"?><Doc/>`, at(xmltext.RefusedProcInst, "<?xml")),
		Entry("an XML declaration after a comment, before another root", `<!-- c --><?xml version="1.0"?><Other/>`, at(xmltext.RefusedRoot, "<Other")),
		Entry("an XML declaration inside the root", `<Doc><?xml version="1.0"?></Doc>`, at(xmltext.RefusedProcInst, "<?xml")),
		Entry("an XML declaration in a field", `<Doc><Name>a<?xml version="1.0"?></Name></Doc>`, at(xmltext.RefusedProcInst, "<?xml")),
		Entry("an XML declaration after the root", `<Doc/><?xml version="1.0"?>`, at(xmltext.RefusedProcInst, "<?xml")),
		Entry("an XML declaration whose target is in upper case", `<?XML version="1.0"?><Doc/>`, at(xmltext.RefusedProcInst, "<?XML")),
		Entry("an XML declaration with a pseudo-attribute expat does not take", `<?xml version="1.0" foo="bar"?><Doc/>`, at(xmltext.RefusedProcInst, "<?xml")),
		Entry("an XML declaration without its version", `<?xml encoding="UTF-8"?><Doc/>`, at(xmltext.RefusedProcInst, "<?xml")),
		Entry("a processing-instruction target followed by neither whitespace nor ?>", `<Doc><?p*?></Doc>`, at(xmltext.RefusedProcInst, "<?p*")),
		Entry("a bad processing-instruction target before the root", `<?p*?><Doc/>`, at(xmltext.RefusedProcInst, "<?p*")),
		Entry("a bad processing-instruction target before another root, whose refusal precedes it", `<?p*?><Other/>`, at(xmltext.RefusedRoot, "<Other")),
		Entry("a bad processing-instruction target, then a declaration before the root", `<?p*?><!DOCTYPE Doc><Doc/>`, at(xmltext.RefusedDeclaration, "<!DOCTYPE")),
		Entry("a repeated namespace declaration", `<Doc xmlns="`+s3NS+`" xmlns="`+s3NS+`"/>`, at(xmltext.RefusedRepeatedAttribute, "<Doc")),
		Entry("a repeated namespace declaration on a child", `<Doc><Name xmlns="`+s3NS+`" xmlns="`+s3NS+`"/></Doc>`,
			at(xmltext.RefusedRepeatedAttribute, "<Name")),
		Entry("a repeated attribute on another root, whose refusal precedes it", `<Other a="1" a="1"/>`, at(xmltext.RefusedRoot, "<Other")),
		Entry("an empty body", ``, atEnd(xmltext.RefusedIncomplete)),
		Entry("whitespace alone", " \n", atEnd(xmltext.RefusedIncomplete)),
		Entry("a comment and a processing instruction alone", `<!-- c --><?p x?>`, atEnd(xmltext.RefusedIncomplete)),
		Entry("an unclosed root", `<Doc>`, atEnd(xmltext.RefusedIncomplete)),
		Entry("a body that ends inside a field", `<Doc><Name>a`, atEnd(xmltext.RefusedIncomplete)),

		Entry("an element the table does not name", `<Doc><Other>x</Other></Doc>`, at(xmltext.RefusedUnknown, "<Other>"), reads),
		Entry("an element the table does not name, repeated", `<Doc><Other/><Other/></Doc>`, at(xmltext.RefusedUnknown, "<Other/>"), reads),
		Entry("an element the table places elsewhere", `<Doc><List><Name/></List></Doc>`, at(xmltext.RefusedUnknown, "<Name/>"), reads),
		Entry("a container the table places elsewhere, which the lenient table reads as a field",
			`<Doc><List><List><Item/></List></List></Doc>`, at(xmltext.RefusedUnknown, "<List><Item"), at(xmltext.RefusedNested, "<Item/>")),
		Entry("an element inside one the table does not name", `<Doc><Other><b/></Other></Doc>`,
			at(xmltext.RefusedUnknown, "<Other>"), at(xmltext.RefusedNested, "<b/>")),
		Entry("an element the table does not name, refused before its attributes", `<Doc><Other xmlns="urn:x"/></Doc>`,
			at(xmltext.RefusedUnknown, "<Other"), at(xmltext.RefusedNamespace, "<Other")),
		Entry("text in the root", `<Doc>x<Name/></Doc>`, at(xmltext.RefusedText, "x<"), reads),
		Entry("text in a container", `<Doc><List><Item/>x</List></Doc>`, at(xmltext.RefusedText, "x<"), reads),
		Entry("a no-break space in the root, which is not XML whitespace", "<Doc> </Doc>", at(xmltext.RefusedText, " "), reads),
		Entry("an attribute", `<Doc><Name a="1"/></Doc>`, at(xmltext.RefusedAttribute, "<Name"), reads),
		Entry("an xml: attribute", `<Doc xml:lang="en"/>`, at(xmltext.RefusedAttribute, "<Doc"), reads),
		Entry("a prefixed attribute named xmlns, which declares nothing", `<Doc p:xmlns="urn:x"/>`, at(xmltext.RefusedAttribute, "<Doc"), reads),
		Entry("a prefix declared for the table's namespace", `<Doc xmlns:p="`+s3NS+`"/>`, at(xmltext.RefusedAttribute, "<Doc"), reads),
		Entry("a prefix declared for another namespace", `<Doc xmlns:p="urn:x"/>`,
			at(xmltext.RefusedAttribute, "<Doc"), at(xmltext.RefusedNamespace, "<Doc")),
		Entry("a prefix declared for no namespace", `<Doc xmlns:p=""/>`, at(xmltext.RefusedAttribute, "<Doc"), at(xmltext.RefusedNamespace, "<Doc")),
		Entry("an attribute before another namespace", `<Doc a="1" xmlns="urn:x"/>`,
			at(xmltext.RefusedAttribute, "<Doc"), at(xmltext.RefusedNamespace, "<Doc")),
		Entry("a repeated attribute", `<Doc><Name a="1" a="2"/></Doc>`,
			at(xmltext.RefusedAttribute, "<Name"), at(xmltext.RefusedRepeatedAttribute, "<Name")),
		Entry("one local name under two prefixes, which repeats no attribute", `<Doc xmlns:p="`+s3NS+`" p:a="1" a="2"/>`,
			at(xmltext.RefusedAttribute, "<Doc"), reads),
	)

	It("takes an empty default namespace where the table lists it", func() {
		for _, lenient := range []bool{false, true} {
			Expect(xmltext.Scan([]byte(`<Doc xmlns=""><Name xmlns=""/></Doc>`), docSchema(lenient, ""), nil)).To(Succeed(), "lenient %v", lenient)
			Expect(scanRefusal(docSchema(lenient, ""), `<Doc xmlns="urn:x"/>`, nil).Refusal).To(Equal(xmltext.RefusedNamespace), "lenient %v", lenient)
		}
	})

	It("calls end for each element after its children, with a field's decoded text and the children a container holds", func() {
		type closed struct {
			name, text string
			has        []bool
		}
		names := []string{"Name", "List", "Item", "Flag"}
		for _, lenient := range []bool{false, true} {
			body := `<Doc><List><Item>a&amp;<![CDATA[<b>]]><!-- c --></Item><Item/></List><Flag>f</Flag></Doc>`
			want := []closed{
				{"Item", "a&<b>", []bool{false, false, false, false}},
				{"Item", "", []bool{false, false, false, false}},
				{"List", "", []bool{false, false, true, false}},
				{"Flag", "f", []bool{false, false, false, false}},
				{"Doc", "", []bool{false, true, false, true}},
			}
			if lenient {
				body = strings.Replace(body, "<Flag>", "<Other>o</Other><Flag>", 1)
				want = append(want[:3:3], closed{"Other", "o", []bool{false, false, false, false}}, want[3], want[4])
			}
			var got []closed
			Expect(xmltext.Scan([]byte(body), docSchema(lenient), func(c xmltext.Closed) error {
				has := make([]bool, len(names))
				for i, n := range names {
					has[i] = c.Has(n)
				}
				got = append(got, closed{c.Name, string(c.Text), has})
				return nil
			})).To(Succeed(), "lenient %v", lenient)
			Expect(got).To(Equal(want), "lenient %v", lenient)
		}
	})

	It("refuses with end's error at the end tag of the element end refuses", func() {
		refusal := errors.New("no Name")
		body := `<Doc><List><Item>x</Item></List></Doc>`
		se := scanRefusal(docSchema(false), body, func(c xmltext.Closed) error {
			if c.Name == "Doc" && !c.Has("Name") {
				return refusal
			}
			return nil
		})
		off := strings.Index(body, "</Doc>")
		Expect(se.Refusal).To(Equal(xmltext.RefusedEnd))
		Expect(se.Offset).To(Equal(int64(off)))
		Expect(se).To(MatchError(refusal))
		Expect(se).To(MatchError("no Name at byte " + strconv.Itoa(off)))
	})

	DescribeTable("keeps a refusal's text within 400 bytes, however long the client's text in it",
		func(body string) {
			for _, lenient := range []bool{false, true} {
				err := xmltext.Scan([]byte(body), docSchema(lenient), nil)
				Expect(err).To(HaveOccurred(), "lenient %v", lenient)
				Expect(len(err.Error())).To(BeNumerically("<=", 400), "lenient %v: %.200q", lenient, err)
				Expect(err.Error()).To(Satisfy(utf8.ValidString), "lenient %v", lenient)
			}
		},
		Entry("a long root name", "<"+strings.Repeat("a", 1<<19)+"/>"),
		Entry("a long second root", "<Doc/><"+strings.Repeat("a", 1<<19)+"/>"),
		Entry("a long declaration", "<!"+strings.Repeat("D", 1<<19)+"><Doc/>"),
		Entry("a declaration of astral tag runes", "<!DOCTYPE "+strings.Repeat("\U000E0001", 1<<10)+"><Doc/>"),
		Entry("a long undeclared entity, which Go's decoder names", "<Doc><Name>&"+strings.Repeat("e", 1<<19)+";</Name></Doc>"),
		Entry("a long undeclared entity of four-byte letters", "<Doc><Name>&"+strings.Repeat("\U00020000", 1<<16)+";</Name></Doc>"),
		Entry("an encoding of emoji, which Go's decoder names", `<?xml version="1.0" encoding="`+strings.Repeat("\U0001F600", 128)+`"?><Doc/>`),
		Entry("long names in an end tag closing another", "<Doc><"+strings.Repeat("a", 1<<19)+"></"+strings.Repeat("b", 1<<19)+"></Doc>"),
		Entry("a long element inside a long element the table does not name",
			"<Doc><"+strings.Repeat("a", 1<<19)+"><"+strings.Repeat("b", 1<<19)+"/></Doc>"),
		Entry("a long prefixed name", "<Doc><"+strings.Repeat("p", 1<<19)+":"+strings.Repeat("n", 1<<19)+"/></Doc>"),
		Entry("64 astral tag runes as the root's namespace", `<Doc xmlns="`+strings.Repeat("\U000E0001", 64)+`"/>`),
		Entry("a 64-byte attribute name holding 64 astral tag runes",
			`<Doc `+strings.Repeat("n", 64)+`="`+strings.Repeat("\U000E0001", 64)+`" xmlns="u`+strings.Repeat("\U000E0001", 64)+`"/>`),
		Entry("a long namespace on a long element the table does not name",
			"<Doc><"+strings.Repeat("a", 1<<19)+` xmlns="`+strings.Repeat("\U000E0001", 1<<16)+`"/></Doc>`),
		Entry("a long attribute name and value", `<Doc `+strings.Repeat("n", 1<<18)+`="`+strings.Repeat("\U000E0001", 1<<16)+`" xmlns="urn:x"/>`),
		Entry("a long field past its bound", "<Doc><Flag>"+strings.Repeat("\U000E0001", 1<<16)+"</Flag></Doc>"),
		Entry("text of astral tag runes after the root", "<Doc/>"+strings.Repeat("\U000E0001", 1<<16)),
	)

	It("stops at the refusal without reading what follows it", func() {
		// Each prefix is followed by two megabytes that are not well-formed: a
		// scan that read past the refusal would fail on them instead.
		rest := strings.Repeat("<a", 1<<20)
		for prefix, want := range map[string]xmltext.Refusal{
			`<!DOCTYPE Doc>`:     xmltext.RefusedDeclaration,
			`<Other>`:            xmltext.RefusedRoot,
			`<Doc><Name><b>`:     xmltext.RefusedNested,
			`<Doc><Name/><Name>`: xmltext.RefusedRepeated,
			`<Doc><p:Name>`:      xmltext.RefusedPrefix,
			`<Doc xmlns="u">`:    xmltext.RefusedNamespace,
			`<Doc></Doc></Doc>`:  xmltext.RefusedEndTag,
			`<Doc/>x`:            xmltext.RefusedText,
			`<Doc/>&#32;`:        xmltext.RefusedText,
			`<Doc><?p*?>`:        xmltext.RefusedProcInst,
		} {
			Expect(scanRefusal(docSchema(true), prefix+rest, nil).Refusal).To(Equal(want), "prefix %q", prefix)
		}
	})

	It("says where a body that ends early ends: inside the element still open, or before any root", func() {
		Expect(scanRefusal(docSchema(false), `<Doc><Name>a`, nil)).To(MatchError(`the body ends inside "Name" at byte 12`))
		Expect(scanRefusal(docSchema(false), `<!-- c -->`, nil)).To(MatchError(`the body holds no root at byte 10`))
	})

	It("reads a table whose element holds 64 children, and panics on one that holds more", func() {
		children := func(n int) []xmltext.Child {
			c := make([]xmltext.Child, n)
			for i := range c {
				c[i] = xmltext.Child{Name: "C" + strconv.Itoa(i)}
			}
			return c
		}
		table := func(n int) *xmltext.Schema {
			return &xmltext.Schema{Root: "Doc", Children: map[string][]xmltext.Child{"Doc": children(n)}, Namespaces: []string{s3NS}}
		}
		Expect(xmltext.Scan([]byte(`<Doc><C0/><C63/></Doc>`), table(64), nil)).To(Succeed())
		se := scanRefusal(table(64), `<Doc><C63/><C0/><C63/></Doc>`, nil)
		Expect(se.Refusal).To(Equal(xmltext.RefusedRepeated), "the 64th child's mark")
		Expect(se.Offset).To(Equal(int64(len(`<Doc><C63/><C0/>`))))
		Expect(func() { Expect(xmltext.Scan([]byte(`<Doc/>`), table(65), nil)).To(Succeed()) }).
			To(PanicWith(ContainSubstring("more than 64 children")))
	})

	It("refuses a repeat of any of a tag's attributes, naming it, however many come before it", func() {
		s := docSchema(true)
		for n := 1; n <= 24; n++ {
			Expect(xmltext.Scan([]byte(`<Doc><Name`+attrs(n)+`/></Doc>`), s, nil)).To(Succeed(), "%d attributes", n)
			for j := range n {
				a := "a" + strconv.Itoa(j)
				se := scanRefusal(s, `<Doc><Name`+attrs(n)+` `+a+`="x"/></Doc>`, nil)
				Expect(se.Refusal).To(Equal(xmltext.RefusedRepeatedAttribute), "%s after %d attributes", a, n)
				Expect(se).To(MatchError(`the attribute "`+a+`" twice on "Name" at byte 5`), "%s after %d attributes", a, n)
			}
		}
	})

	DescribeTable("judges a tag's attributes in order however many it holds, under a lenient table",
		func(body, marker string, want xmltext.Refusal, why string) {
			if want == 0 {
				Expect(xmltext.Scan([]byte(body), docSchema(true), nil)).To(Succeed())
				return
			}
			se := scanRefusal(docSchema(true), body, nil)
			Expect(se.Refusal).To(Equal(want))
			Expect(se).To(MatchError(why + " at byte " + strconv.Itoa(strings.Index(body, marker))))
		},
		Entry("ten thousand distinct attributes, one local name among them also under a prefix",
			`<Doc xmlns:p="`+s3NS+`"><Name`+attrs(10000)+` p:a0=""/></Doc>`, "", xmltext.Refusal(0), ""),
		Entry("the first of ten thousand attributes, repeated after them",
			`<Doc><Name`+attrs(10000)+` a0="x"/></Doc>`, "<Name", xmltext.RefusedRepeatedAttribute, `the attribute "a0" twice on "Name"`),
		Entry("one in the middle of ten thousand, repeated after them",
			`<Doc><Name`+attrs(10000)+` a5000="x"/></Doc>`, "<Name", xmltext.RefusedRepeatedAttribute, `the attribute "a5000" twice on "Name"`),
		Entry("the last of ten thousand, repeated at once",
			`<Doc><Name`+attrs(10000)+` a9999="x"/></Doc>`, "<Name", xmltext.RefusedRepeatedAttribute, `the attribute "a9999" twice on "Name"`),
		Entry("a prefixed attribute repeated after ten thousand",
			`<Doc><Name`+attrs(10000)+` p:a0="" p:a0=""/></Doc>`, "<Name", xmltext.RefusedRepeatedAttribute, `the attribute "p:a0" twice on "Name"`),
		Entry("the root's namespace declaration, repeated after ten thousand attributes",
			`<Doc xmlns="`+s3NS+`"`+attrs(10000)+` xmlns="`+s3NS+`"/>`, "<Doc", xmltext.RefusedRepeatedAttribute, `the attribute "xmlns" twice on "Doc"`),
		Entry("a namespace the table does not list, then a repeat, refused at the namespace",
			`<Doc><Name`+attrs(10000)+` xmlns="urn:x" a0=""/></Doc>`, "<Name", xmltext.RefusedNamespace, `the namespace "urn:x" on "Name"`),
		Entry("a repeat, then a namespace the table does not list, refused at the repeat",
			`<Doc><Name`+attrs(10000)+` a0="" xmlns="urn:x"/></Doc>`, "<Name", xmltext.RefusedRepeatedAttribute, `the attribute "a0" twice on "Name"`),
	)

	It("judges repeats among a tag of many distinct attributes in a few comparisons, under a lenient table", func() {
		// Comparing each of 32,768 attributes with every one before it takes
		// 536,854,528 comparisons; past the first eight a set takes none, so
		// only the 28 among those eight are left.
		body, s := []byte(`<Doc`+attrs(1<<15)+`/>`), docSchema(true)
		var err error
		n := xmltext.CountAttrComparisons(func() { err = xmltext.Scan(body, s, nil) })
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeNumerically(">", 0), "the count sees the comparisons")
		Expect(n).To(BeNumerically("<=", 28), "comparisons for 32,768 attributes")
	})

	It("refuses a body nested a hundred thousand deep, building no tree, in a few allocations", func() {
		deep := []byte(`<Doc><Other>` + strings.Repeat("<a>", 1<<17) + strings.Repeat("</a>", 1<<17) + `</Other></Doc>`)
		for _, lenient := range []bool{false, true} {
			s := docSchema(lenient)
			var err error
			allocs := testing.AllocsPerRun(1, func() { err = xmltext.Scan(deep, s, nil) })
			Expect(err).To(HaveOccurred())
			Expect(allocs).To(BeNumerically("<", 100), "lenient %v", lenient)
		}
	})
})

var _ = Describe("Clip and Quote", func() {
	DescribeTable("Clip cuts at a rune boundary to at most n bytes, marking the cut",
		func(s string, n int, want string) {
			Expect(xmltext.Clip(s, n)).To(Equal(want))
		},
		Entry("a string at the bound, kept", "abcd", 4, "abcd"),
		Entry("a string past the bound, cut", "abcde", 4, "abcd..."),
		Entry("a cut inside a rune, moved before it", "abé", 3, "ab..."),
		Entry("a cut inside a four-byte rune", "a\U0001F600", 4, "a..."),
		Entry("a cut through bytes that start no rune", "\x80\x80\x80", 2, "..."),
	)
	DescribeTable("Quote cuts to 32 bytes and quotes, at most 133 bytes",
		func(s string) {
			q := xmltext.Quote(s)
			Expect(len(q)).To(BeNumerically("<=", 133), "%q", q)
			Expect(q).To(HavePrefix(`"`))
			Expect(q).To(HaveSuffix(`..."`))
			Expect(q).To(Satisfy(utf8.ValidString))
		},
		Entry("astral tag runes, each quoted as ten bytes", strings.Repeat("\U000E0001", 64)),
		Entry("next-line controls, each quoted as six bytes", strings.Repeat("\u0085", 64)),
		Entry("control bytes, each quoted as four", strings.Repeat("\x01", 64)),
		Entry("bytes that are not UTF-8, each quoted as four", strings.Repeat("\xff", 64)),
	)
	It("quotes a short string whole", func() {
		Expect(xmltext.Quote("a\"b")).To(Equal(`"a\"b"`))
	})
})
