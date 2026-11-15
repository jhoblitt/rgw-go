package xmltext_test

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// flatDoc is a root holding empty elements, elements of them in all.
func flatDoc(elements int) []byte {
	return []byte("<r>" + strings.Repeat("<a/>", elements-1) + "</r>")
}

// nestedDoc is levels elements, each inside the last, all closed.
func nestedDoc(levels int) []byte {
	return []byte(strings.Repeat("<a>", levels) + strings.Repeat("</a>", levels))
}

// attrName is the i-th three-letter attribute name, 17,576 of them.
func attrName(i int) string {
	return string([]byte{'a' + byte(i/676%26), 'a' + byte(i/26%26), 'a' + byte(i%26)})
}

// unitsDoc is a root and empty elements, each carrying up to 2,000 distinct
// attributes in a start tag well under MaxStartTag, that start units elements
// and attributes in all.
func unitsDoc(units int) []byte {
	var b strings.Builder
	b.WriteString("<r>")
	for left := units - 1; left > 0; {
		n := min(left-1, 2000)
		b.WriteString("<a")
		for i := range n {
			b.WriteString(" " + attrName(i) + `=""`)
		}
		b.WriteString("/>")
		left -= 1 + n
	}
	b.WriteString("</r>")
	return []byte(b.String())
}

// allocNoise is the allocations other goroutines of the test process may
// make while parseAllocs counts.
const allocNoise = 64

// tagAllocs is the most allocations the decoder makes building one start tag
// of the specs' documents, which hold up to 2,000 attributes: it builds the tag
// before the count refuses the element.
const tagAllocs = 4096

// bytesNoise is the bytes other goroutines of the test process may allocate
// while parseBytes counts.
const bytesNoise = 256 << 10

// parseBytes is the bytes one Parse of doc allocates.
func parseBytes(doc []byte) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := xmltext.Parse(doc)
	runtime.ReadMemStats(&after)
	_ = err
	return after.TotalAlloc - before.TotalAlloc
}

// parseAllocs is the allocations one Parse of doc makes.
func parseAllocs(doc []byte) float64 {
	var err error
	allocs := testing.AllocsPerRun(1, func() { _, err = xmltext.Parse(doc) })
	_ = err
	return allocs
}

var _ = Describe("Parse's bounds on a document's elements and depth", func() {
	It("holds fewer elements than a body of rgw_max_put_param_size bytes of empty ones, or it would bound nothing at the default", func() {
		Expect(xmltext.MaxElements).To(BeNumerically("<", (1<<20)/len("<a/>")))
		Expect(xmltext.MaxDepth).To(BeNumerically("<", (1<<20)/len("<a>")))
		Expect(xmltext.MaxStartTag).To(BeNumerically("<", 1<<20))
	})

	Describe("MaxElements", func() {
		It("reads a document of exactly that many elements", func() {
			root, err := xmltext.Parse(flatDoc(xmltext.MaxElements))
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Children).To(HaveLen(xmltext.MaxElements - 1))
		})

		It("refuses a document of one more with ErrTooManyElements, which is not a document expat refuses", func() {
			_, err := xmltext.Parse(flatDoc(xmltext.MaxElements + 1))
			Expect(err).To(MatchError(xmltext.ErrTooManyElements))
			Expect(err).NotTo(MatchError(xmltext.ErrNotWellFormed))
		})

		DescribeTable("counts each element started, whether it is empty, holds text or children, or is closed again",
			func(element string) {
				doc := "<r>" + strings.Repeat(element, xmltext.MaxElements-1) + "</r>"
				_, err := xmltext.Parse([]byte(doc))
				Expect(err).NotTo(HaveOccurred())
				_, err = xmltext.Parse([]byte(doc[:len(doc)-len("</r>")] + element + "</r>"))
				Expect(err).To(MatchError(xmltext.ErrTooManyElements))
			},
			Entry("an empty element", "<a/>"),
			Entry("an element with a start and an end tag", "<a></a>"),
			Entry("an element with text", "<a>x</a>"),
		)

		It("counts each attribute as one more, so an element with one is refused where one without is read", func() {
			doc := func(last string) []byte {
				return []byte("<r>" + strings.Repeat("<a/>", xmltext.MaxElements-2) + last + "</r>")
			}
			_, err := xmltext.Parse(doc("<b/>"))
			Expect(err).NotTo(HaveOccurred(), "the root, the elements and one more make MaxElements")
			_, err = xmltext.Parse(doc(`<b x=""/>`))
			Expect(err).To(MatchError(xmltext.ErrTooManyElements))
		})

		It("reads elements and attributes together up to MaxElements, and refuses the next", func() {
			root, err := xmltext.Parse(unitsDoc(xmltext.MaxElements))
			Expect(err).NotTo(HaveOccurred())
			Expect(root.Children).NotTo(BeEmpty())
			_, err = xmltext.Parse(unitsDoc(xmltext.MaxElements + 1))
			Expect(err).To(MatchError(xmltext.ErrTooManyElements))
		})

		It("refuses an element of MaxElements attributes as a start tag too long, before it can be counted", func() {
			var b strings.Builder
			b.WriteString("<a")
			for i := range xmltext.MaxElements {
				b.WriteString(" " + attrName(i/26) + string(rune('a'+i%26)) + `=""`)
			}
			b.WriteString("/>")
			_, err := xmltext.Parse([]byte(b.String()))
			Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
		})

		It("costs what a document at the bound costs, however many attributes run past it", func() {
			atBound := parseAllocs(unitsDoc(xmltext.MaxElements))
			fourTimes := parseAllocs(unitsDoc(4 * xmltext.MaxElements))
			Expect(atBound).To(BeNumerically(">", float64(xmltext.MaxElements)/2), "the measure counts the tree")
			Expect(fourTimes).To(BeNumerically("<=", atBound+tagAllocs), "four times the bound allocates what the bound does, and the start tag the decoder builds before the count refuses it")
		})

		It("counts the elements of every branch together, not the most open at once", func() {
			branch := strings.Repeat("<b>", 3) + strings.Repeat("</b>", 3)
			base := "<r>" + strings.Repeat(branch, (xmltext.MaxElements-1)/3)
			_, err := xmltext.Parse([]byte(base + "<c/></r>"))
			Expect(err).NotTo(HaveOccurred(), "the root, the branches and one element make MaxElements")
			_, err = xmltext.Parse([]byte(base + "<c/><c/></r>"))
			Expect(err).To(MatchError(xmltext.ErrTooManyElements))
		})

		It("stops building at the bound: end sees no element past it, and none of the elements still open", func() {
			ended := 0
			_, err := xmltext.ParseFunc(flatDoc(2*xmltext.MaxElements), func(*xmltext.Element) error {
				ended++
				return nil
			})
			Expect(err).To(MatchError(xmltext.ErrTooManyElements))
			Expect(ended).To(Equal(xmltext.MaxElements - 1))
		})

		It("costs what a document at the bound costs, however far past it the document runs", func() {
			atBound := parseAllocs(flatDoc(xmltext.MaxElements))
			pastBound := parseAllocs(flatDoc(xmltext.MaxElements + 1))
			fourTimes := parseAllocs(flatDoc(4 * xmltext.MaxElements))
			Expect(atBound).To(BeNumerically(">", float64(xmltext.MaxElements)), "the measure counts the tree")
			Expect(pastBound).To(BeNumerically("<=", atBound+allocNoise))
			Expect(fourTimes).To(BeNumerically("<=", atBound+allocNoise), "a document four times the bound allocates what one at it does")
		})
	})

	Describe("MaxDepth", func() {
		It("reads a document nested that many elements deep", func() {
			root, err := xmltext.Parse(nestedDoc(xmltext.MaxDepth))
			Expect(err).NotTo(HaveOccurred())
			depth := 1
			for e := root; len(e.Children) > 0; e = e.Children[0] {
				depth++
			}
			Expect(depth).To(Equal(xmltext.MaxDepth))
		})

		It("refuses a document one element deeper with ErrTooDeep, which is not a document expat refuses", func() {
			_, err := xmltext.Parse(nestedDoc(xmltext.MaxDepth + 1))
			Expect(err).To(MatchError(xmltext.ErrTooDeep))
			Expect(err).NotTo(MatchError(xmltext.ErrNotWellFormed))
		})

		It("refuses at the start tag that passes the bound, before it finds the document unclosed", func() {
			_, err := xmltext.Parse([]byte(strings.Repeat("<a>", xmltext.MaxDepth+1)))
			Expect(err).To(MatchError(xmltext.ErrTooDeep))
		})

		It("counts the elements open at once, so a root of many branches that each reach the bound reads", func() {
			branch := strings.Repeat("<b>", xmltext.MaxDepth-1) + strings.Repeat("</b>", xmltext.MaxDepth-1)
			_, err := xmltext.Parse([]byte("<r>" + strings.Repeat(branch, 20) + "</r>"))
			Expect(err).NotTo(HaveOccurred())
		})

		It("costs what a document at the bound costs, however deep the document runs", func() {
			atBound := parseAllocs(nestedDoc(xmltext.MaxDepth))
			pastBound := parseAllocs(nestedDoc(xmltext.MaxDepth + 1))
			deep := parseAllocs([]byte(strings.Repeat("<a>", 349525)))
			Expect(atBound).To(BeNumerically(">", float64(xmltext.MaxDepth)), "the measure counts the tree")
			Expect(pastBound).To(BeNumerically("<=", atBound+allocNoise))
			Expect(deep).To(BeNumerically("<=", atBound+allocNoise), "a body of 349,525 open elements, 1 MiB, allocates what one at the bound does")
		})
	})

	It("answers a document that is not well-formed before the bound with ErrNotWellFormed, whatever follows", func() {
		doc := "<r></b>" + strings.Repeat("<a/>", 2*xmltext.MaxElements)
		_, err := xmltext.Parse([]byte(doc))
		Expect(err).To(MatchError(xmltext.ErrNotWellFormed))
	})

	It("returns end's error, not the bound's, when end refuses an element before the bound", func() {
		refused := errors.New("refused")
		_, err := xmltext.ParseFunc(flatDoc(2*xmltext.MaxElements), func(*xmltext.Element) error { return refused })
		Expect(err).To(BeIdenticalTo(refused))
	})
})

// tagOf is a start tag of n bytes holding one attribute, whose value is fill
// repeated, in quote.
func tagOf(n int, quote, fill string) string {
	head, tail := `<a b=`+quote, quote+`/>`
	room := n - len(head) - len(tail)
	return head + strings.Repeat(fill, room/len(fill)+1)[:room] + tail
}

// dupTag is a start tag of n bytes or just under, of one attribute name again
// and again.
func dupTag(n int) string {
	return "<a" + strings.Repeat(` a=""`, (n-len("<a/>"))/len(` a=""`)) + "/>"
}

// scanSchema is a lenient table, which reads an attribute on any element.
var scanSchema = &xmltext.Schema{
	Root:     "r",
	Children: map[string][]xmltext.Child{"r": {{Name: "a", Repeats: true}}},
	Lenient:  true,
}

var _ = Describe("The bound on a start tag's bytes", func() {
	// The decoder builds every attribute of a start tag before it returns the
	// tag, so the bound is made as the decoder reads, whatever Parse or Scan
	// would then find in the tag.
	DescribeTable("reads a start tag of exactly MaxStartTag bytes and refuses one byte more, the same of Parse and Scan",
		func(quote, fill string) {
			at, over := tagOf(xmltext.MaxStartTag, quote, fill), tagOf(xmltext.MaxStartTag+1, quote, fill)
			Expect(at).To(HaveLen(xmltext.MaxStartTag))
			_, err := xmltext.Parse([]byte("<r>" + at + "</r>"))
			Expect(err).NotTo(HaveOccurred())
			Expect(xmltext.Scan([]byte("<r>"+at+"</r>"), scanSchema, nil)).To(Succeed())

			_, err = xmltext.Parse([]byte("<r>" + over + "</r>"))
			Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
			Expect(err).NotTo(MatchError(xmltext.ErrNotWellFormed))
			err = xmltext.Scan([]byte("<r>"+over+"</r>"), scanSchema, nil)
			Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
			var se *xmltext.ScanError
			Expect(errors.As(err, &se)).To(BeTrue())
			Expect(se.Refusal).To(Equal(xmltext.RefusedStartTag))
			Expect(se.Offset).To(BeEquivalentTo(len("<r>")), "the offset of the tag")
		},
		Entry("a value of letters", `"`, "x"),
		Entry("a value of > in double quotes, which does not end the tag", `"`, ">"),
		Entry("a value of > in single quotes, which does not end the tag", `'`, ">"),
		Entry("a value of / and quotes of the other kind", `'`, `"/`),
	)

	It("counts a tag whose name begins with a byte over 0x7f, which the decoder reads as a name", func() {
		over := "<é" + tagOf(xmltext.MaxStartTag, `"`, "x")[len("<a"):]
		_, err := xmltext.Parse([]byte("<r>" + over + "</r>"))
		Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
	})

	// The decoder reads a quoted value to the refused byte and, when what it
	// holds ends inside a rune, reports invalid UTF-8 in place of the refusal
	// unless the guard's error is kept.
	DescribeTable("refuses with its own error a tag whose byte past the bound falls inside a rune of a quoted value",
		func(r string, held int) {
			head := `<a b="`
			tag := head + strings.Repeat("x", xmltext.MaxStartTag-len(head)-held) + r + strings.Repeat("x", 100) + `"/>`
			Expect(tag[:xmltext.MaxStartTag]).To(HaveSuffix(r[:held]), "the bound falls after the rune's first %d bytes", held)
			Expect(tag[xmltext.MaxStartTag]).To(Equal(r[held]), "the 16,385th byte is the rune's next")
			Expect(r[held]&0xc0).To(Equal(byte(0x80)), "a continuation byte")

			_, err := xmltext.Parse([]byte("<r>" + tag + "</r>"))
			Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
			Expect(err).NotTo(MatchError(xmltext.ErrNotWellFormed))

			err = xmltext.Scan([]byte("<r>"+tag+"</r>"), scanSchema, nil)
			Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
			var se *xmltext.ScanError
			Expect(errors.As(err, &se)).To(BeTrue())
			Expect(se.Refusal).To(Equal(xmltext.RefusedStartTag))
			Expect(se.Offset).To(BeEquivalentTo(len("<r>")), "the offset of the tag")
		},
		Entry("the second byte of a two-byte rune", "\u00e9", 1),
		Entry("the second byte of a three-byte rune", "\u20ac", 1),
		Entry("the third byte of a three-byte rune", "\u20ac", 2),
		Entry("the second byte of a four-byte rune", "\U0001F600", 1),
		Entry("the fourth byte of a four-byte rune", "\U0001F600", 3),
	)

	DescribeTable("does not count what is not a start tag, however long it is",
		func(doc string) {
			_, err := xmltext.Parse([]byte(doc))
			Expect(err).NotTo(HaveOccurred())
			Expect(xmltext.Scan([]byte(doc), scanSchema, nil)).To(Succeed())
		},
		Entry("text", "<r>"+strings.Repeat("x", 4*xmltext.MaxStartTag)+"</r>"),
		Entry("text of > and quotes", "<r>"+strings.Repeat(`>"'`, 4*xmltext.MaxStartTag/3)+"</r>"),
		Entry("CDATA", "<r><![CDATA["+strings.Repeat("x", 4*xmltext.MaxStartTag)+"]]></r>"),
		Entry("a comment", "<r><!--"+strings.Repeat("x", 4*xmltext.MaxStartTag)+"--></r>"),
		Entry("a processing instruction", "<r><?p "+strings.Repeat("x", 4*xmltext.MaxStartTag)+"?></r>"),
		Entry("a comment before the root", "<!--"+strings.Repeat("x", 4*xmltext.MaxStartTag)+"--><r/>"),
		Entry("an end tag", "<r></r"+strings.Repeat(" ", 4*xmltext.MaxStartTag)+">"),
		Entry("a start tag after many that are short", "<r>"+strings.Repeat(`<a b="x"/>`, 4*xmltext.MaxStartTag/len(`<a b="x"/>`))+"</r>"),
	)

	It("does not count a document type declaration, whose subset holds markup", func() {
		doc := "<!DOCTYPE r [" + strings.Repeat(`<!ENTITY e "x">`, 4*xmltext.MaxStartTag/len(`<!ENTITY e "x">`)) + "]><r/>"
		_, err := xmltext.Parse([]byte(doc))
		Expect(err).NotTo(HaveOccurred())
	})

	It("costs what a tag just past the bound costs, however far past it the tag runs", func() {
		just, megabyte := dupTag(xmltext.MaxStartTag+16), dupTag(1<<20)
		Expect(len(just)).To(BeNumerically(">", xmltext.MaxStartTag))
		Expect(len(megabyte)).To(BeNumerically(">", 1<<20-16))
		_, err := xmltext.Parse([]byte(megabyte))
		Expect(err).To(MatchError(xmltext.ErrStartTagTooLong))
		atBound := parseBytes([]byte(just))
		Expect(parseBytes([]byte(megabyte))).To(BeNumerically("<=", atBound+bytesNoise))
		Expect(atBound).To(BeNumerically("<", 4<<20), "an unbounded tag of 262,142 attributes allocates 86 MiB")
	})
})
