package acl_test

import (
	"bytes"
	"fmt"
	"math/bits"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// publicAccessBytes writes PublicAccessBlockConfiguration as its encode body
// does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_public_access.h#L45-L52):
// ENCODE_START(v, compat) around the four flags in declaration order, then
// whatever a later version appends.
func publicAccessBytes(v, compat uint8, flags [4]bool, appended ...byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(v, compat)
	for _, b := range flags {
		e.Bool(b)
	}
	e.Raw(appended)
	e.EndStruct(f)
	return e.Bytes()
}

var _ = Describe("PublicAccessBlock", func() {
	DescribeTable("reads and writes each flag at its place in ENCODE_START(1, 1)",
		func(flags [4]bool, want acl.PublicAccessBlock) {
			b := publicAccessBytes(1, 1, flags)
			Expect(decodeWhole(b, acl.DecodePublicAccessBlock)).To(Equal(want))
			for _, r := range releases {
				Expect(encodeAt(want, r)).To(Equal(b), "release %v", r)
			}
		},
		Entry("BlockPublicAcls first", [4]bool{true, false, false, false},
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("IgnorePublicAcls second", [4]bool{false, true, false, false},
			acl.PublicAccessBlock{IgnorePublicACLs: true}),
		Entry("BlockPublicPolicy third", [4]bool{false, false, true, false},
			acl.PublicAccessBlock{BlockPublicPolicy: true}),
		Entry("RestrictPublicBuckets fourth", [4]bool{false, false, false, true},
			acl.PublicAccessBlock{RestrictPublicBuckets: true}),
		Entry("two flags together", [4]bool{true, false, true, false},
			acl.PublicAccessBlock{BlockPublicACLs: true, BlockPublicPolicy: true}),
		Entry("the zero value", [4]bool{}, acl.PublicAccessBlock{}),
	)
	It("skips what a later version appends, as DECODE_START(1) does", func() {
		b := publicAccessBytes(2, 1, [4]bool{false, true, false, false}, 0xAA)
		Expect(decodeWhole(b, acl.DecodePublicAccessBlock)).To(Equal(acl.PublicAccessBlock{IgnorePublicACLs: true}))
	})
	It("refuses a compat version above 1", func() {
		d := denc.NewDecoder(publicAccessBytes(2, 2, [4]bool{}))
		acl.DecodePublicAccessBlock(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})

// blockDoc is a PublicAccessBlockConfiguration document whose root start tag
// carries attrs and holds inner.
func blockDoc(attrs, inner string) []byte {
	return []byte("<PublicAccessBlockConfiguration" + attrs + ">" + inner + "</PublicAccessBlockConfiguration>")
}

// element is name holding text.
func element(name, text string) string {
	return "<" + name + ">" + text + "</" + name + ">"
}

// blockNames are the four booleans in dump_xml's order.
var blockNames = []string{"BlockPublicAcls", "IgnorePublicAcls", "BlockPublicPolicy", "RestrictPublicBuckets"}

func parseBlock(body []byte) acl.PublicAccessBlock {
	GinkgoHelper()
	b, err := acl.ParsePublicAccessBlockXML(body)
	Expect(err).NotTo(HaveOccurred(), "body %q", body)
	return b
}

var _ = Describe("ParsePublicAccessBlockXML", func() {
	DescribeTable("reads each boolean present as true and leaves the absent ones false",
		func(name string, want acl.PublicAccessBlock) {
			Expect(parseBlock(blockDoc(" "+s3NS, element(name, "true")))).To(Equal(want))
		},
		Entry("BlockPublicAcls", "BlockPublicAcls", acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("IgnorePublicAcls", "IgnorePublicAcls", acl.PublicAccessBlock{IgnorePublicACLs: true}),
		Entry("BlockPublicPolicy", "BlockPublicPolicy", acl.PublicAccessBlock{BlockPublicPolicy: true}),
		Entry("RestrictPublicBuckets", "RestrictPublicBuckets", acl.PublicAccessBlock{RestrictPublicBuckets: true}),
	)
	DescribeTable("reads each boolean present as false among three true ones",
		func(name string, want acl.PublicAccessBlock) {
			var inner strings.Builder
			for _, n := range blockNames {
				v := "true"
				if n == name {
					v = "false"
				}
				inner.WriteString(element(n, v))
			}
			Expect(parseBlock(blockDoc(" "+s3NS, inner.String()))).To(Equal(want))
		},
		Entry("BlockPublicAcls", "BlockPublicAcls",
			acl.PublicAccessBlock{IgnorePublicACLs: true, BlockPublicPolicy: true, RestrictPublicBuckets: true}),
		Entry("IgnorePublicAcls", "IgnorePublicAcls",
			acl.PublicAccessBlock{BlockPublicACLs: true, BlockPublicPolicy: true, RestrictPublicBuckets: true}),
		Entry("BlockPublicPolicy", "BlockPublicPolicy",
			acl.PublicAccessBlock{BlockPublicACLs: true, IgnorePublicACLs: true, RestrictPublicBuckets: true}),
		Entry("RestrictPublicBuckets", "RestrictPublicBuckets",
			acl.PublicAccessBlock{BlockPublicACLs: true, IgnorePublicACLs: true, BlockPublicPolicy: true}),
	)
	It("reads a root with no boolean as the zero block, as decode_xml leaves each absent one false", func() {
		Expect(parseBlock([]byte(`<PublicAccessBlockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))).
			To(Equal(acl.PublicAccessBlock{}))
	})
	It("reads the booleans in any order", func() {
		inner := element("RestrictPublicBuckets", "true") + element("BlockPublicAcls", "false") +
			element("BlockPublicPolicy", "true") + element("IgnorePublicAcls", "true")
		Expect(parseBlock(blockDoc("", inner))).To(Equal(acl.PublicAccessBlock{
			IgnorePublicACLs: true, BlockPublicPolicy: true, RestrictPublicBuckets: true,
		}))
	})
	DescribeTable("reads the forms XML gives the same elements and text",
		func(body string, want acl.PublicAccessBlock) {
			Expect(parseBlock([]byte(body))).To(Equal(want))
		},
		Entry("the document an SDK sends, with its declaration",
			`<?xml version="1.0" encoding="UTF-8"?>`+string(blockDoc(" "+s3NS,
				element("BlockPublicAcls", "true")+element("IgnorePublicAcls", "true")+
					element("BlockPublicPolicy", "true")+element("RestrictPublicBuckets", "false"))),
			acl.PublicAccessBlock{BlockPublicACLs: true, IgnorePublicACLs: true, BlockPublicPolicy: true}),
		Entry("no namespace", string(blockDoc("", element("BlockPublicAcls", "true"))),
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("an empty default namespace", string(blockDoc(` xmlns=""`, element("BlockPublicAcls", "true"))),
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("the S3 namespace declared again on a boolean",
			string(blockDoc(" "+s3NS, `<IgnorePublicAcls `+s3NS+`>true</IgnorePublicAcls>`)),
			acl.PublicAccessBlock{IgnorePublicACLs: true}),
		Entry("a UTF-8 byte order mark, which expat skips",
			"\xef\xbb\xbf"+string(blockDoc("", element("BlockPublicPolicy", "true"))),
			acl.PublicAccessBlock{BlockPublicPolicy: true}),
		Entry("whitespace, comments and processing instructions around the elements",
			"<!-- before -->\n"+string(blockDoc(" "+s3NS,
				"\n  <!-- a -->\n  "+element("BlockPublicAcls", "true")+"\n  <?pi x?>\n"))+"\n<!-- after -->\n",
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("an empty root written with an end tag", string(blockDoc("", "")),
			acl.PublicAccessBlock{}),
		Entry("character references spelling true", string(blockDoc("", element("RestrictPublicBuckets", "&#116;&#x72;ue"))),
			acl.PublicAccessBlock{RestrictPublicBuckets: true}),
		Entry("a CDATA section holding true", string(blockDoc("", element("BlockPublicAcls", "<![CDATA[true]]>"))),
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("a comment inside the value, which both readers drop", string(blockDoc("", element("BlockPublicAcls", "tr<!-- x -->ue"))),
			acl.PublicAccessBlock{BlockPublicACLs: true}),
		Entry("a character reference to a space between the elements",
			string(blockDoc("", "&#32;"+element("BlockPublicAcls", "true"))),
			acl.PublicAccessBlock{BlockPublicACLs: true}),
	)
	DescribeTable("reads each boolean form every reference gives one meaning, as radosgw reads it",
		func(value string, want bool) {
			got := parseBlock(blockDoc(" "+s3NS, element("BlockPublicAcls", value)))
			Expect(got).To(Equal(acl.PublicAccessBlock{BlockPublicACLs: want}), "value %q", value)
		},
		Entry("TRUE, as AWS's reference writes it and strncasecmp reads it", "TRUE", true),
		Entry("False", "False", false),
		Entry("tRuE", "tRuE", true),
		Entry("1, true to xs:boolean and to radosgw's integer fallback", "1", true),
		Entry("0", "0", false),
		Entry("XML whitespace around 1, which xs:boolean collapses and strtol skips", " \t1\n", true),
		Entry("XML whitespace around 0", "\r\n0 ", false),
		Entry("a carriage return written as a character reference after 1", "1&#13;", true),
		Entry("1 inside 63 bytes of whitespace, 64 bytes in all", strings.Repeat(" ", 31)+"1"+strings.Repeat(" ", 32), true),
	)
	DescribeTable("refuses a value no reference reads as a boolean as MalformedXML",
		func(value string) {
			_, err := acl.ParsePublicAccessBlockXML(blockDoc(" "+s3NS, element("BlockPublicAcls", value)))
			Expect(err).To(MatchError(acl.ErrMalformedXML), "value %q", value)
		},
		Entry("whitespace before true, which radosgw refuses too", " true"),
		Entry("a line break after FALSE, which radosgw refuses too", "FALSE\n"),
		Entry("2, which radosgw's integer fallback reads as true", "2"),
		Entry("-7, which radosgw reads as true", "-7"),
		Entry("+0, which radosgw reads as false", "+0"),
		Entry("-0", "-0"),
		Entry("01, which radosgw reads as true", "01"),
		Entry("00", "00"),
		Entry("two digits apart, which radosgw refuses too", "1 1"),
		Entry("a no-break space before 1, which strtol does not skip", " 1"),
		Entry("false with a Unicode long s, which only Unicode case folding takes for s", "fal\u017fe"),
		Entry("yes", "yes"),
		Entry("on", "on"),
		Entry("empty", ""),
		Entry("whitespace alone", " "),
		Entry("true with a suffix", "truex"),
		Entry("a hexadecimal 1, which strtol reads only up to the x", "0x1"),
		Entry("1.0", "1.0"),
		Entry("2^31, past radosgw's int", "2147483648"),
	)
	It("refuses a self-closed boolean, whose text is empty, as radosgw's integer fallback does", func() {
		_, err := acl.ParsePublicAccessBlockXML(blockDoc("", "<BlockPublicAcls/>"))
		Expect(err).To(MatchError(acl.ErrMalformedXML))
	})

	// at is the offset of the first byte of what the parser must stop at.
	type hostile struct {
		body []byte
		at   int
	}
	// nested is a well-formed document whose prefix and suffix hold depth
	// nested elements, as deep as fits under MaxPublicAccessBlockXML.
	nested := func(prefix, suffix string, depth int) hostile {
		body := prefix + strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth) + suffix
		Expect(len(body)).To(BeNumerically("<=", acl.MaxPublicAccessBlockXML))
		return hostile{body: []byte(body), at: len(prefix)}
	}
	refusedAt := func(body, marker string) hostile {
		i := strings.Index(body, marker)
		Expect(i).To(BeNumerically(">=", 0), "marker %q in %q", marker, body)
		return hostile{body: []byte(body), at: i}
	}
	DescribeTable("refuses a hostile or out-of-schema document as MalformedXML at its first offending token",
		func(h func() hostile) {
			c := h()
			_, err := acl.ParsePublicAccessBlockXML(c.body)
			Expect(err).To(MatchError(acl.ErrMalformedXML))
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("at byte %d", c.at))), "body %.120q", c.body)
		},
		Entry("a DOCTYPE whose internal entity spells a value, which expat expands", func() hostile {
			return refusedAt(`<!DOCTYPE PublicAccessBlockConfiguration [<!ENTITY t "true">]>`+
				string(blockDoc("", element("BlockPublicAcls", "&t;"))), "<!DOCTYPE")
		}),
		Entry("a DOCTYPE after the XML declaration", func() hostile {
			return refusedAt(`<?xml version="1.0"?><!DOCTYPE PublicAccessBlockConfiguration>`+
				string(blockDoc("", "")), "<!DOCTYPE")
		}),
		Entry("an external entity", func() hostile {
			return refusedAt(`<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>`+
				string(blockDoc("", element("BlockPublicAcls", "&e;"))), "<!DOCTYPE")
		}),
		Entry("nested entities, the billion laughs", func() hostile {
			var dtd strings.Builder
			dtd.WriteString(`<!DOCTYPE l [<!ENTITY l0 "lol">`)
			for i := 1; i < 10; i++ {
				fmt.Fprintf(&dtd, `<!ENTITY l%d "%s">`, i, strings.Repeat(fmt.Sprintf("&l%d;", i-1), 10))
			}
			dtd.WriteString(`]>`)
			return refusedAt(dtd.String()+string(blockDoc("", element("BlockPublicAcls", "&l9;"))), "<!DOCTYPE")
		}),
		Entry("a declaration after the root", func() hostile {
			return refusedAt(string(blockDoc("", ""))+`<!DOCTYPE x>`, "<!DOCTYPE")
		}),
		Entry("an undeclared entity reference", func() hostile {
			return refusedAt(string(blockDoc("", element("BlockPublicAcls", "&t;"))), "&t;")
		}),
		Entry("140000 nested unknown elements under the root, which radosgw reads and ignores", func() hostile {
			return nested(`<PublicAccessBlockConfiguration>`, `</PublicAccessBlockConfiguration>`, 140000)
		}),
		Entry("140000 nested elements inside a boolean", func() hostile {
			return nested(`<PublicAccessBlockConfiguration><BlockPublicAcls>`,
				`true</BlockPublicAcls></PublicAccessBlockConfiguration>`, 140000)
		}),
		Entry("the root nested in itself", func() hostile {
			return refusedAt(`<PublicAccessBlockConfiguration><PublicAccessBlockConfiguration>`+
				element("BlockPublicAcls", "true")+`</PublicAccessBlockConfiguration></PublicAccessBlockConfiguration>`,
				`<PublicAccessBlockConfiguration><B`)
		}),
		Entry("a repeated boolean, whose first copy radosgw reads", func() hostile {
			body := string(blockDoc("", element("BlockPublicAcls", "true")+element("BlockPublicAcls", "false")))
			return hostile{body: []byte(body), at: strings.LastIndex(body, "<BlockPublicAcls>")}
		}),
		Entry("a repeated boolean whose second copy radosgw never decodes", func() hostile {
			body := string(blockDoc("", element("RestrictPublicBuckets", "false")+element("RestrictPublicBuckets", "garbage")))
			return hostile{body: []byte(body), at: strings.LastIndex(body, "<RestrictPublicBuckets>")}
		}),
		Entry("an unknown element, which radosgw ignores", func() hostile {
			return refusedAt(string(blockDoc("", element("BlockPublicAcls", "true")+"<Foo/>")), "<Foo/>")
		}),
		Entry("a misspelled boolean, which radosgw ignores and stores false", func() hostile {
			return refusedAt(string(blockDoc(" "+s3NS, element("BlockPublicACLs", "true"))), "<BlockPublicACLs>")
		}),
		Entry("a prefixed boolean, which radosgw ignores and stores false", func() hostile {
			return refusedAt(string(blockDoc("", element("s3:BlockPublicAcls", "true"))), "<s3:")
		}),
		Entry("a prefix declaration, even one naming S3's namespace", func() hostile {
			return refusedAt(string(blockDoc(` xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"`, "")), "<Public")
		}),
		Entry("a prefixed root, which radosgw refuses too", func() hostile {
			return refusedAt(`<s3:PublicAccessBlockConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/">`+
				`</s3:PublicAccessBlockConfiguration>`, "<s3:")
		}),
		Entry("another root, which radosgw refuses too", func() hostile {
			return refusedAt(`<PublicAccessBlock>`+element("BlockPublicAcls", "true")+`</PublicAccessBlock>`, "<PublicAccessBlock>")
		}),
		Entry("a root whose name differs in case", func() hostile {
			return refusedAt(`<publicAccessBlockConfiguration/>`, "<public")
		}),
		Entry("a second root, which expat refuses too", func() hostile {
			return refusedAt(string(blockDoc("", ""))+`<Second/>`, "<Second/>")
		}),
		Entry("a second PublicAccessBlockConfiguration root", func() hostile {
			body := string(blockDoc("", "")) + string(blockDoc("", element("BlockPublicAcls", "true")))
			return hostile{body: []byte(body), at: len(blockDoc("", ""))}
		}),
		Entry("an end tag after the root", func() hostile {
			body := string(blockDoc("", "")) + `</PublicAccessBlockConfiguration>`
			return hostile{body: []byte(body), at: len(blockDoc("", ""))}
		}),
		Entry("a value far past 64 bytes, refused where its text starts", func() hostile {
			body := string(blockDoc("", element("BlockPublicAcls", strings.Repeat("t", 1<<16))))
			return hostile{body: []byte(body), at: strings.Index(body, "<BlockPublicAcls>") + len("<BlockPublicAcls>")}
		}),
		Entry("1 inside 64 bytes of whitespace, 65 in all, refused where its text starts", func() hostile {
			body := string(blockDoc("", element("BlockPublicAcls", strings.Repeat(" ", 32)+"1"+strings.Repeat(" ", 32))))
			return hostile{body: []byte(body), at: strings.Index(body, "<BlockPublicAcls>") + len("<BlockPublicAcls>")}
		}),
		Entry("a no-break space between the elements, which is not XML whitespace", func() hostile {
			return refusedAt(string(blockDoc("", "\u00a0"+element("BlockPublicAcls", "true"))), "\u00a0")
		}),
		Entry("a foreign namespace on the root, which radosgw ignores", func() hostile {
			return refusedAt(string(blockDoc(` xmlns="urn:evil"`, element("BlockPublicAcls", "true"))), "<Public")
		}),
		Entry("S3's namespace without its trailing slash", func() hostile {
			return refusedAt(string(blockDoc(` xmlns="http://s3.amazonaws.com/doc/2006-03-01"`, "")), "<Public")
		}),
		Entry("a foreign namespace on a boolean", func() hostile {
			return refusedAt(string(blockDoc(" "+s3NS, `<IgnorePublicAcls xmlns="urn:evil">true</IgnorePublicAcls>`)),
				"<IgnorePublicAcls")
		}),
		Entry("an attribute on a boolean, which radosgw ignores", func() hostile {
			return refusedAt(string(blockDoc("", `<BlockPublicAcls foo="bar">true</BlockPublicAcls>`)), "<BlockPublicAcls")
		}),
		Entry("an xml: attribute on the root", func() hostile {
			return refusedAt(string(blockDoc(` xml:lang="en"`, "")), "<Public")
		}),
		Entry("text in the root, which radosgw ignores", func() hostile {
			return refusedAt(string(blockDoc("", "junk"+element("BlockPublicAcls", "true"))), "junk")
		}),
		Entry("text after the root, which expat refuses too", func() hostile {
			return refusedAt(string(blockDoc("", ""))+"junk", "junk")
		}),
		Entry("an offset counted from the byte order mark", func() hostile {
			return refusedAt("\xef\xbb\xbf"+string(blockDoc("", "<Foo/>")), "<Foo/>")
		}),
	)
	DescribeTable("refuses a document that is not well-formed as MalformedXML",
		func(body string) {
			_, err := acl.ParsePublicAccessBlockXML([]byte(body))
			Expect(err).To(MatchError(acl.ErrMalformedXML), "body %q", body)
		},
		Entry("an empty body", ""),
		Entry("whitespace alone", " \n"),
		Entry("an unclosed root", `<PublicAccessBlockConfiguration>`),
		Entry("an unclosed boolean", `<PublicAccessBlockConfiguration><BlockPublicAcls>true</PublicAccessBlockConfiguration>`),
		Entry("a mismatched end tag", string(blockDoc("", `<BlockPublicAcls>true</IgnorePublicAcls>`))),
		Entry("an end tag with no start", `</PublicAccessBlockConfiguration>`),
		Entry("an XML declaration not at the start", " "+`<?xml version="1.0"?>`+string(blockDoc("", ""))),
		Entry("an XML declaration of version 2.0", `<?xml version="2.0"?>`+string(blockDoc("", ""))),
		Entry("an encoding other than UTF-8", `<?xml version="1.0" encoding="ISO-8859-1"?>`+string(blockDoc("", ""))),
		Entry("a duplicate xmlns attribute", string(blockDoc(` xmlns="" xmlns=""`, ""))),
		Entry("a NUL byte in a value", string(blockDoc("", element("BlockPublicAcls", "tr\x00ue")))),
		Entry("invalid UTF-8 in a value", string(blockDoc("", element("BlockPublicAcls", "tr\xffue")))),
	)
	DescribeTable("keeps a refusal's message short when the client's text in it is long",
		func(body []byte) {
			_, err := acl.ParsePublicAccessBlockXML(body)
			Expect(err).To(MatchError(acl.ErrMalformedXML))
			Expect(len(err.Error())).To(BeNumerically("<", 512), "error %.200q", err)
		},
		Entry("a long root name", []byte("<"+strings.Repeat("a", 1<<19)+"/>")),
		Entry("a long unknown element", blockDoc("", "<"+strings.Repeat("b", 1<<19)+"/>")),
		Entry("a long element inside a boolean", blockDoc("", "<BlockPublicAcls><"+strings.Repeat("c", 1<<19)+"/>")),
		Entry("a long attribute name and value", blockDoc(" "+strings.Repeat("n", 1<<18)+`="`+strings.Repeat("v", 1<<18)+`"`, "")),
		Entry("a long declaration", []byte("<!"+strings.Repeat("D", 1<<19)+">"+string(blockDoc("", "")))),
		Entry("a long undeclared entity, which Go's decoder names", blockDoc("", element("BlockPublicAcls", "&"+strings.Repeat("e", 1<<19)+";"))),
		Entry("a long processing instruction target, which xmltext names", blockDoc("", "<?"+strings.Repeat("p", 1<<19)+"*?>")),
	)
	It("refuses a body over MaxPublicAccessBlockXML before reading it", func() {
		pad := acl.MaxPublicAccessBlockXML + 1 - len(blockDoc("", ""))
		body := blockDoc("", strings.Repeat(" ", pad))
		Expect(body).To(HaveLen(acl.MaxPublicAccessBlockXML + 1))
		_, err := acl.ParsePublicAccessBlockXML(body)
		Expect(err).To(MatchError(acl.ErrMalformedXML))
		Expect(err).To(MatchError(ContainSubstring("exceeds")))
	})
	It("reads a body of MaxPublicAccessBlockXML bytes, rgw_max_put_param_size's default", func() {
		Expect(acl.MaxPublicAccessBlockXML).To(Equal(1 << 20))
		inner := element("BlockPublicAcls", "true")
		pad := acl.MaxPublicAccessBlockXML - len(blockDoc("", inner))
		body := blockDoc("", strings.Repeat("\n", pad)+inner)
		Expect(body).To(HaveLen(acl.MaxPublicAccessBlockXML))
		Expect(parseBlock(body)).To(Equal(acl.PublicAccessBlock{BlockPublicACLs: true}))
	})
})

var _ = Describe("PublicAccessBlock.MarshalS3XML", func() {
	It("writes dump_xml's bytes: no namespace, the four booleans in its order, lower-case", func() {
		b := acl.PublicAccessBlock{BlockPublicACLs: true, BlockPublicPolicy: true}
		Expect(string(b.MarshalS3XML())).To(Equal(`<PublicAccessBlockConfiguration>` +
			`<BlockPublicAcls>true</BlockPublicAcls>` +
			`<IgnorePublicAcls>false</IgnorePublicAcls>` +
			`<BlockPublicPolicy>true</BlockPublicPolicy>` +
			`<RestrictPublicBuckets>false</RestrictPublicBuckets>` +
			`</PublicAccessBlockConfiguration>`))
	})
	It("writes every boolean of the zero block as false", func() {
		Expect(string(acl.PublicAccessBlock{}.MarshalS3XML())).To(Equal(`<PublicAccessBlockConfiguration>` +
			`<BlockPublicAcls>false</BlockPublicAcls>` +
			`<IgnorePublicAcls>false</IgnorePublicAcls>` +
			`<BlockPublicPolicy>false</BlockPublicPolicy>` +
			`<RestrictPublicBuckets>false</RestrictPublicBuckets>` +
			`</PublicAccessBlockConfiguration>`))
	})
	It("reads back as the block it writes, for each of the sixteen blocks", func() {
		for i := range 16 {
			b := acl.PublicAccessBlock{
				BlockPublicACLs:       i&1 != 0,
				IgnorePublicACLs:      i&2 != 0,
				BlockPublicPolicy:     i&4 != 0,
				RestrictPublicBuckets: i&8 != 0,
			}
			doc := b.MarshalS3XML()
			Expect(bytes.Count(doc, []byte(">true<"))).To(Equal(bits.OnesCount(uint(i))), "block %+v", b)
			Expect(parseBlock(doc)).To(Equal(b), "block %+v", b)
		}
	})
})
