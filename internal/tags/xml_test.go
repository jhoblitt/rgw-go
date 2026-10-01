package tags_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/tags"
)

const ns = "http://s3.amazonaws.com/doc/2006-03-01/"

// tagging returns a Tagging document in the S3 namespace around tagSet.
func tagging(tagSet string) []byte {
	return []byte(`<Tagging xmlns="` + ns + `">` + tagSet + `</Tagging>`)
}

// utf16LE writes s, which must be ASCII apart from a leading U+FEFF, in
// UTF-16LE.
func utf16LE(s string) []byte {
	var b []byte
	for _, r := range s {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

// tagSetOf returns a TagSet of n distinct tags.
func tagSetOf(n int) string {
	var b strings.Builder
	b.WriteString("<TagSet>")
	for i := range n {
		fmt.Fprintf(&b, "<Tag><Key>k%02d</Key><Value>v</Value></Tag>", i)
	}
	b.WriteString("</TagSet>")
	return b.String()
}

var _ = Describe("ParseXML", func() {
	It("reads each Tag of the TagSet", func() {
		s, err := tags.ParseXML(tagging("<TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet>"), tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
	})
	It("reads a pretty-printed document with an XML declaration", func() {
		doc := `<?xml version="1.0" encoding="UTF-8"?>
<Tagging xmlns="` + ns + `">
  <TagSet>
    <Tag>
      <Key>k</Key>
      <Value>v</Value>
    </Tag>
  </TagSet>
</Tagging>
`
		s, err := tags.ParseXML([]byte(doc), tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
	})
	It("reads a document that starts with a UTF-8 byte order mark", func() {
		s, err := tags.ParseXML(append([]byte("\xef\xbb\xbf"), tagging("<TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet>")...),
			tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
	})
	DescribeTable("reads an empty TagSet as the empty set",
		func(tagSet string) {
			s, err := tags.ParseXML(tagging(tagSet), tags.MaxObjectTags)
			Expect(err).NotTo(HaveOccurred())
			Expect(s).To(Equal(tags.Set{}))
		},
		Entry("self-closing", "<TagSet/>"),
		Entry("with an end tag", "<TagSet></TagSet>"),
		Entry("holding only elements other than Tag", "<TagSet><Other><Key>k</Key><Value>v</Value></Other></TagSet>"),
	)
	DescribeTable("takes element text as expat delivers it",
		func(tag string, want tags.Tag) {
			s, err := tags.ParseXML(tagging("<TagSet><Tag>"+tag+"</Tag></TagSet>"), tags.MaxObjectTags)
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Tags).To(Equal([]tags.Tag{want}))
		},
		Entry("surrounding whitespace kept", "<Key> k </Key><Value>\tv\n</Value>", tags.Tag{Key: " k ", Value: "\tv\n"}),
		Entry("entities and character references decoded", "<Key>a&amp;b&lt;&#x41;</Key><Value>&quot;&apos;&#66;</Value>",
			tags.Tag{Key: "a&b<A", Value: `"'B`}),
		Entry("CDATA read as text", "<Key><![CDATA[<k>]]></Key><Value>v</Value>", tags.Tag{Key: "<k>", Value: "v"}),
		Entry("a child element's text left out", "<Key>a<b>x</b>c</Key><Value>v</Value>", tags.Tag{Key: "ac", Value: "v"}),
		Entry("an empty Value", "<Key>k</Key><Value/>", tags.Tag{Key: "k", Value: ""}),
		Entry("only the first Key and Value", "<Key>a</Key><Key>b</Key><Value>1</Value><Value>2</Value>", tags.Tag{Key: "a", Value: "1"}),
		Entry("elements other than Key and Value ignored", "<Other>x</Other><Key>k</Key><Value>v</Value>", tags.Tag{Key: "k", Value: "v"}),
	)
	It("reads only the first TagSet", func() {
		s, err := tags.ParseXML(tagging(
			"<TagSet><Tag><Key>a</Key><Value>1</Value></Tag></TagSet><TagSet><Tag><Key>b</Key><Value>2</Value></Tag></TagSet>"),
			tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal([]tags.Tag{{Key: "a", Value: "1"}}))
	})
	It("keeps equal keys in document order across a large TagSet", func() {
		in, want := interleaved(tags.MaxBucketTags)
		var b strings.Builder
		b.WriteString("<TagSet>")
		for _, t := range in {
			fmt.Fprintf(&b, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", t.Key, t.Value)
		}
		b.WriteString("</TagSet>")
		s, err := tags.ParseXML(tagging(b.String()), tags.MaxBucketTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal(want))
	})
	It("keeps duplicate keys in multimap order", func() {
		s, err := tags.ParseXML(tagging("<TagSet>"+
			"<Tag><Key>b</Key><Value>1</Value></Tag>"+
			"<Tag><Key>a</Key><Value>x</Value></Tag>"+
			"<Tag><Key>b</Key><Value>2</Value></Tag>"+
			"</TagSet>"), tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Tags).To(Equal([]tags.Tag{{Key: "a", Value: "x"}, {Key: "b", Value: "1"}, {Key: "b", Value: "2"}}))
	})
	DescribeTable("reads a document whose root is not Tagging as the empty set, as radosgw's optional root lookup does",
		func(doc string) {
			s, err := tags.ParseXML([]byte(doc), tags.MaxObjectTags)
			Expect(err).NotTo(HaveOccurred())
			Expect(s).To(Equal(tags.Set{}))
		},
		Entry("another name", "<Foo><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Foo>"),
		Entry("Tagging under a prefix, which expat does not resolve",
			`<s3:Tagging xmlns:s3="`+ns+`"><s3:TagSet><s3:Tag><s3:Key>k</s3:Key><s3:Value>v</s3:Value></s3:Tag></s3:TagSet></s3:Tagging>`),
	)
	DescribeTable("refuses a document missing a mandatory element with ErrMalformedXML",
		func(doc []byte) {
			s, err := tags.ParseXML(doc, tags.MaxBucketTags)
			Expect(err).To(MatchError(tags.ErrMalformedXML))
			Expect(s).To(Equal(tags.Set{}))
		},
		Entry("no TagSet", tagging("")),
		Entry("a Tag without Value", tagging("<TagSet><Tag><Key>k</Key></Tag></TagSet>")),
		Entry("a Tag without Key", tagging("<TagSet><Tag><Value>v</Value></Tag></TagSet>")),
		Entry("a TagSet under a prefix", tagging(`<s3:TagSet xmlns:s3="`+ns+`"></s3:TagSet>`)),
	)
	DescribeTable("refuses what expat does not parse with ErrMalformedXML",
		func(doc string) {
			_, err := tags.ParseXML([]byte(doc), tags.MaxObjectTags)
			Expect(err).To(MatchError(tags.ErrMalformedXML))
		},
		Entry("not XML", "not xml"),
		Entry("an empty body", ""),
		Entry("only whitespace and a comment", " <!-- c --> "),
		Entry("an unclosed root", "<Tagging><TagSet>"),
		Entry("mismatched end tags", "<Tagging><TagSet></Tagging></TagSet>"),
		Entry("a second root element", "<Tagging><TagSet/></Tagging><Tagging><TagSet/></Tagging>"),
		Entry("text after the root", "<Tagging><TagSet/></Tagging>x"),
		Entry("text before the root", "x<Tagging><TagSet/></Tagging>"),
		Entry("a repeated attribute", `<Tagging a="1" a="2"><TagSet/></Tagging>`),
		Entry("an undefined entity", "<Tagging><TagSet><Tag><Key>&k;</Key><Value>v</Value></Tag></TagSet></Tagging>"),
		Entry("whitespace before the XML declaration", ` <?xml version="1.0"?><Tagging><TagSet/></Tagging>`),
		Entry("an XML declaration inside the root", `<Tagging><?xml version="1.0"?><TagSet/></Tagging>`),
		Entry("an XML declaration without a version", `<?xml encoding="UTF-8"?><Tagging><TagSet/></Tagging>`),
		Entry("standalone neither yes nor no", `<?xml version="1.0" standalone="maybe"?><Tagging><TagSet/></Tagging>`),
		Entry("an XML declaration written XML", `<?XML version="1.0"?><Tagging><TagSet/></Tagging>`),
		Entry("a DOCTYPE inside the root", "<Tagging><!DOCTYPE Tagging><TagSet/></Tagging>"),
		Entry("a DOCTYPE after the root", "<Tagging><TagSet/></Tagging><!DOCTYPE Tagging>"),
		Entry("an empty CDATA section after the root", "<Tagging><TagSet/></Tagging><![CDATA[]]>"),
	)
	DescribeTable("reads some bodies otherwise than expat, as docs/exclusions.md records",
		func(doc []byte, refused bool) {
			s, err := tags.ParseXML(doc, tags.MaxObjectTags)
			if refused {
				Expect(err).To(MatchError(tags.ErrMalformedXML))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
		},
		Entry("refusing an ISO-8859-1 declaration, which expat reads",
			[]byte(`<?xml version="1.0" encoding="ISO-8859-1"?><Tagging><TagSet/></Tagging>`), true),
		Entry("refusing a US-ASCII declaration, which expat reads",
			[]byte(`<?xml version="1.0" encoding="US-ASCII"?><Tagging><TagSet/></Tagging>`), true),
		Entry("refusing UTF-16 with a byte order mark, which expat reads",
			utf16LE("\ufeff<Tagging><TagSet/></Tagging>"), true),
		Entry("refusing version 1.1, which expat reads",
			[]byte(`<?xml version="1.1"?><Tagging><TagSet/></Tagging>`), true),
		Entry("refusing a name with two colons, which expat reads",
			[]byte(`<Tagging><a:b:c/><TagSet/></Tagging>`), true),
		Entry("refusing an entity its DTD declares, which expat expands",
			[]byte(`<!DOCTYPE Tagging [<!ENTITY e "v">]><Tagging><TagSet><Tag><Key>k</Key><Value>&e;</Value></Tag></TagSet></Tagging>`), true),
		Entry("reading attributes not separated by whitespace, which expat refuses",
			[]byte(`<Tagging a="1"b="2"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`), false),
	)
	It("refuses more than limit tags with ErrInvalidTag", func() {
		_, err := tags.ParseXML(tagging(tagSetOf(11)), tags.MaxObjectTags)
		Expect(err).To(MatchError(tags.ErrInvalidTag))
		s, err := tags.ParseXML(tagging(tagSetOf(11)), tags.MaxBucketTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Len()).To(Equal(11))
	})
	DescribeTable("refuses a tag check_and_add_tag refuses with ErrInvalidTag",
		func(tag string) {
			_, err := tags.ParseXML(tagging("<TagSet><Tag>"+tag+"</Tag></TagSet>"), tags.MaxObjectTags)
			Expect(err).To(MatchError(tags.ErrInvalidTag))
		},
		Entry("an empty Key", "<Key></Key><Value>v</Value>"),
		Entry("a 129-byte Key", "<Key>"+strings.Repeat("k", 129)+"</Key><Value>v</Value>"),
		Entry("a 257-byte Value", "<Key>k</Key><Value>"+strings.Repeat("v", 257)+"</Value>"),
	)
	It("reports a missing element before a limit, as decode_xml runs before rebuild", func() {
		doc := tagging(strings.TrimSuffix(tagSetOf(11), "</TagSet>") + "<Tag><Key>z</Key></Tag></TagSet>")
		_, err := tags.ParseXML(doc, tags.MaxObjectTags)
		Expect(err).To(MatchError(tags.ErrMalformedXML))
	})
})

var _ = Describe("Set.MarshalS3XML", func() {
	It("writes one Tag per tag inside the namespaced Tagging and TagSet", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}
		Expect(string(s.MarshalS3XML())).To(Equal(
			`<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`))
	})
	It("writes the empty set as an empty TagSet", func() {
		Expect(string(tags.Set{}.MarshalS3XML())).To(Equal(
			`<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet></TagSet></Tagging>`))
	})
	It("escapes keys and values as XMLFormatter does", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: `"k">`, Value: "a<b&c'"}}}
		Expect(string(s.MarshalS3XML())).To(ContainSubstring(`<Key>&quot;k&quot;&gt;</Key><Value>a&lt;b&amp;c&apos;</Value>`))
	})
	It("writes an empty value as an empty element", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "k"}}}
		Expect(string(s.MarshalS3XML())).To(ContainSubstring(`<Tag><Key>k</Key><Value></Value></Tag>`))
	})
	It("writes multimap order whatever order Tags was built in", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "b", Value: "1"}, {Key: "a", Value: "2"}, {Key: "b", Value: "0"}}}
		Expect(string(s.MarshalS3XML())).To(ContainSubstring(
			`<TagSet><Tag><Key>a</Key><Value>2</Value></Tag><Tag><Key>b</Key><Value>1</Value></Tag><Tag><Key>b</Key><Value>0</Value></Tag></TagSet>`))
	})
	It("writes what ParseXML reads back", func() {
		s := tags.Set{Tags: []tags.Tag{{Key: "a&b", Value: "<x>"}, {Key: "k", Value: ""}, {Key: "q'\"", Value: "\t"}}}
		got, err := tags.ParseXML(s.MarshalS3XML(), tags.MaxObjectTags)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(s))
	})
})
