package xmltext_test

import (
	"encoding/xml"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

var _ = Describe("Escape", func() {
	DescribeTable("escapes as xml_stream_escaper does",
		func(in, want string) {
			Expect(xmltext.Escape(in)).To(Equal(want), "Escape(%q)", in)
		},
		Entry("text without a special byte, returned as is", "plain/key-1_2.txt", "plain/key-1_2.txt"),
		Entry("the empty string", "", ""),
		Entry("the five markup characters", `&<>'"`, "&amp;&lt;&gt;&apos;&quot;"),
		Entry("a quoted ETag", `"9b2cf535f27731c974343645a3985328"`, "&quot;9b2cf535f27731c974343645a3985328&quot;"),
		Entry("tab and newline, copied", "a\tb\nc", "a\tb\nc"),
		Entry("tab, newline and a byte from 0x80 up after an escaped byte, copied", "<\t\n\x80", "&lt;\t\n\x80"),
		Entry("carriage return and the other bytes below 0x20, those beside tab and newline included, as references",
			"\r\x01\x08\x0b\x1f", "&#x0d;&#x01;&#x08;&#x0b;&#x1f;"),
		Entry("NUL, as a reference", "a\x00b", "a&#x00;b"),
		Entry("DEL, as a reference", "\x7f", "&#x7f;"),
		Entry("space and tilde, copied", " ~", " ~"),
		Entry("UTF-8, copied", "é日本", "é日本"),
		Entry("bytes from 0x80 up, copied even when they are not UTF-8", "k\x80\xff\xc3", "k\x80\xff\xc3"),
	)
})

var _ = Describe("Text", func() {
	type contents struct {
		XMLName  xml.Name       `xml:"Contents"`
		Key      xmltext.Text   `xml:"Key"`
		ETag     xmltext.Text   `xml:"ETag"`
		Size     int64          `xml:"Size"`
		Owner    xmltext.Text   `xml:"Owner,omitempty"`
		Empty    xmltext.Text   `xml:"Empty"`
		Prefixes []xmltext.Text `xml:"Prefix"`
	}
	It("marshals through encoding/xml with the escaper's bytes, honoring omitempty", func() {
		b, err := xml.Marshal(contents{Key: "a\x01b\xffc\x7f", ETag: `"e1"`, Size: 1, Prefixes: []xmltext.Text{"p<", "q'"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("<Contents><Key>a&#x01;b\xffc&#x7f;</Key><ETag>&quot;e1&quot;</ETag><Size>1</Size>" +
			"<Empty></Empty><Prefix>p&lt;</Prefix><Prefix>q&apos;</Prefix></Contents>"))
	})
})
