package op_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// completeDoc is a CompleteMultipartUpload document holding parts as written.
func completeDoc(parts ...string) string {
	return "<CompleteMultipartUpload>" + strings.Join(parts, "") + "</CompleteMultipartUpload>"
}

// xmlPart is a Part element with a number and an ETag.
func xmlPart(num, etag string) string {
	return "<Part><PartNumber>" + num + "</PartNumber><ETag>" + etag + "</ETag></Part>"
}

var _ = Describe("ParseCompleteMultipart", func() {
	It("reads the parts sorted by number, the last of a repeated number kept", func() {
		doc := `<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Part><PartNumber>2</PartNumber><ETag>"b"</ETag></Part>` +
			`<Part><ETag>"a"</ETag><PartNumber>1</PartNumber></Part><Part><PartNumber>1</PartNumber><ETag>"a2"</ETag></Part></CompleteMultipartUpload>`
		parts, err := op.ParseCompleteMultipart([]byte(doc))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: `"a2"`}, {Number: 2, ETag: `"b"`}}))
	})
	It("reads a CompletedMultipartUpload root, which some clients send", func() {
		parts, err := op.ParseCompleteMultipart([]byte("<CompletedMultipartUpload>" + xmlPart("1", "x") + "</CompletedMultipartUpload>"))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: "x"}}))
	})
	It("keeps an ETag's text as written, its quotes and white space, with references and CDATA decoded", func() {
		parts, err := op.ParseCompleteMultipart([]byte(completeDoc(
			xmlPart("1", ` "a" `), xmlPart("2", "&quot;b&quot;"), xmlPart("3", "<![CDATA[<c>]]>"), xmlPart("4", ""))))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: ` "a" `}, {Number: 2, ETag: `"b"`}, {Number: 3, ETag: "<c>"}, {Number: 4, ETag: ""}}))
	})
	It("takes each part's first PartNumber and first ETag, as find_first does", func() {
		parts, err := op.ParseCompleteMultipart([]byte(completeDoc(
			"<Part><PartNumber>1</PartNumber><ETag>a</ETag><PartNumber>2</PartNumber><ETag>b</ETag></Part>")))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: "a"}}))
	})
	It("reads only the parts directly under the root", func() {
		parts, err := op.ParseCompleteMultipart([]byte(completeDoc("<X>"+xmlPart("9", "z")+"</X>", xmlPart("1", "a"))))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: "a"}}))
	})
	DescribeTable("reads a part number as C's atoi reads it",
		func(num string, want int) {
			parts, err := op.ParseCompleteMultipart([]byte(completeDoc(xmlPart(num, "x"))))
			Expect(err).NotTo(HaveOccurred())
			Expect(parts).To(Equal([]op.CompletePart{{Number: want, ETag: "x"}}), "PartNumber %q", num)
		},
		Entry("the leading digits", "12abc", 12),
		Entry("no digits as 0", "zzz", 0),
		Entry("past leading white space", " \t7", 7),
		Entry("a plus sign", "+3", 3),
		Entry("a minus sign", "-2", -2),
		Entry("leading zeros", "007", 7),
		Entry("the low 32 bits of a long", "4294967297", 1),
		Entry("an overflow saturated at LONG_MAX, then cut to an int", "99999999999999999999", -1),
	)
	DescribeTable("is MalformedXML for",
		func(doc string) {
			_, err := op.ParseCompleteMultipart([]byte(doc))
			Expect(err).To(MatchError(op.ErrMalformedXML), "%q", doc)
		},
		Entry("an empty document", ""),
		Entry("white space alone", " \n"),
		Entry("not XML", "hello"),
		Entry("an unclosed root", "<CompleteMultipartUpload>"+xmlPart("1", "x")),
		Entry("a mismatched end tag", "<CompleteMultipartUpload>"+xmlPart("1", "x")+"</CompletedMultipartUpload>"),
		Entry("junk after the root", completeDoc(xmlPart("1", "x"))+"<a/>"),
		Entry("an entity a DTD declares, which rgw-go's reader refuses where expat expands it", `<!DOCTYPE a [<!ENTITY e "1">]>`+completeDoc(xmlPart("&e;", "x"))),
		Entry("another root", "<Delete>"+xmlPart("1", "x")+"</Delete>"),
		Entry("a MultipartUpload root, which radosgw allocates but never looks up", "<MultipartUpload>"+xmlPart("1", "x")+"</MultipartUpload>"),
		Entry("a Part root", xmlPart("1", "x")),
		Entry("a root with a namespace prefix, as names are read as written", "<s3:CompleteMultipartUpload>"+xmlPart("1", "x")+"</s3:CompleteMultipartUpload>"),
		Entry("parts with a namespace prefix", completeDoc("<s3:Part><PartNumber>1</PartNumber><ETag>x</ETag></s3:Part>")),
		Entry("no parts", completeDoc()),
		Entry("an empty root element", "<CompleteMultipartUpload/>"),
		Entry("a part without an ETag", completeDoc("<Part><PartNumber>1</PartNumber></Part>")),
		Entry("a part without a number", completeDoc("<Part><ETag>x</ETag></Part>")),
		Entry("an empty number", completeDoc(xmlPart("", "x"))),
		Entry("a number whose digits sit in a child, leaving its own text empty", completeDoc(xmlPart("<n>1</n>", "x"))),
		Entry("a bad part beside good ones", completeDoc(xmlPart("1", "x"), "<Part><ETag>y</ETag></Part>")),
		Entry("a bad part nested away from the root, as xml_end runs for every Part", completeDoc("<X><Part/></X>", xmlPart("1", "x"))),
		Entry("a bad part inside a part", completeDoc("<Part><PartNumber>1</PartNumber><ETag>x</ETag><Part/></Part>")),
		Entry("a part number in an attribute", completeDoc(`<Part PartNumber="1"><ETag>x</ETag></Part>`)),
	)
	It("refuses every corruption of a document as MalformedXML or reads sorted, distinct parts", func() {
		valid := completeDoc(xmlPart("3", `"c"`), xmlPart("1", `"a"`), xmlPart("2", "&quot;b&quot;"))
		rng := rand.New(rand.NewPCG(8, 2026)) //nolint:gosec // a reproducible corpus, not a secret
		alphabet := []byte("<>/&;\"' =!-?[]PartNumberETagComplete0123456789\x00\xff")
		for i := range 2000 {
			doc := []byte(valid)
			for range 1 + rng.IntN(4) {
				p := rng.IntN(len(doc) + 1)
				switch rng.IntN(3) {
				case 0:
					doc = slices.Insert(doc, p, alphabet[rng.IntN(len(alphabet))])
				case 1:
					if p < len(doc) {
						doc = slices.Delete(doc, p, p+1)
					}
				default:
					if p < len(doc) {
						doc[p] = alphabet[rng.IntN(len(alphabet))]
					}
				}
			}
			parts, err := op.ParseCompleteMultipart(doc)
			if err != nil {
				Expect(err).To(MatchError(op.ErrMalformedXML), "corruption %d: %q", i, doc)
				continue
			}
			Expect(parts).NotTo(BeEmpty(), "corruption %d: %q", i, doc)
			Expect(slices.IsSortedFunc(parts, func(a, b op.CompletePart) int { return a.Number - b.Number })).To(BeTrue(), "corruption %d: %q", i, doc)
			Expect(slices.CompactFunc(slices.Clone(parts), func(a, b op.CompletePart) bool { return a.Number == b.Number })).To(HaveLen(len(parts)), "corruption %d: %q", i, doc)
		}
	})
	It("reads ten thousand and one parts, the count the limit is checked against", func() {
		parts, err := op.ParseCompleteMultipart([]byte(tooManyParts(10001)))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(HaveLen(10001))
	})
})

// tooManyParts is a completion naming parts 1 through n.
func tooManyParts(n int) string {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for i := range n {
		fmt.Fprintf(&b, `<Part><PartNumber>%d</PartNumber><ETag>"x"</ETag></Part>`, i+1)
	}
	b.WriteString("</CompleteMultipartUpload>")
	return b.String()
}

var _ = Describe("ParseCopySourceRange", func() {
	DescribeTable("reads bytes=first-last as init_processing does",
		func(v string, first, last uint64) {
			f, l, err := op.ParseCopySourceRange(v)
			Expect(err).NotTo(HaveOccurred(), "%q", v)
			Expect([]uint64{f, l}).To(Equal([]uint64{first, last}), "%q", v)
		},
		Entry("a range", "bytes=5-10", uint64(5), uint64(10)),
		Entry("one byte", "bytes=7-7", uint64(7), uint64(7)),
		Entry("leading zeros", "bytes=007-010", uint64(7), uint64(10)),
		Entry("an empty first bound, which strtoull reads as 0", "bytes=-10", uint64(0), uint64(10)),
		Entry("both bounds empty", "bytes=-", uint64(0), uint64(0)),
		Entry("the largest off_t", "bytes=0-9223372036854775807", uint64(0), uint64(1<<63-1)),
	)
	DescribeTable("is InvalidArgument for a value that is not bytes= and two runs of digits",
		func(v string) {
			_, _, err := op.ParseCopySourceRange(v)
			Expect(err).To(MatchError(op.ErrInvalidArgument), "%q", v)
		},
		Entry("no unit", "5-10"),
		Entry("no dash", "bytes=5"),
		Entry("letters", "bytes=a-b"),
		Entry("a unit not at the start", " bytes=5-10"),
		Entry("a unit in another case", "Bytes=5-10"),
		Entry("a second dash", "bytes=5-10-"),
		Entry("a second range", "bytes=0-1,3-4"),
		Entry("white space in a bound", "bytes= 5-10"),
		Entry("a sign", "bytes=+5-10"),
		Entry("hex", "bytes=0-0x10"),
		Entry("nothing", ""),
	)
	DescribeTable("is InvalidRange for",
		func(v string) {
			_, _, err := op.ParseCopySourceRange(v)
			Expect(err).To(MatchError(op.ErrInvalidRange), "%q", v)
		},
		Entry("a first bound past the last, -ERANGE", "bytes=10-5"),
		Entry("an empty last bound after a first one, which reads as 0", "bytes=5-"),
		Entry("a last bound that turns negative in an off_t", "bytes=0-18446744073709551615"),
		Entry("a last bound past 2^64, which strtoull saturates", "bytes=0-99999999999999999999"),
		Entry("both bounds past 2^63, which radosgw's comparison passes", "bytes=9223372036854775808-18446744073709551615"),
		Entry("both bounds saturated", "bytes=99999999999999999999-99999999999999999999"),
		Entry("a first bound past 2^63 before a small last one, which off_t orders first", "bytes=9223372036854775813-3"),
	)
})
