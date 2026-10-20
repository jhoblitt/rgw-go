package acl_test

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// v21's messages for a PutBucketOwnershipControls body it refuses
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4570-L4574,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L36-L69).
const (
	msgNoOwnershipControls = "Missing required element OwnershipControls"
	msgNoRule              = "Missing required element Rule"
	msgNoObjectOwnership   = "Missing required element ObjectOwnership"
	msgBadObjectOwnership  = "ObjectOwnership must be one of BucketOwnerEnforced | BucketOwnerPreferred | ObjectWriter"
)

// ownershipBytes writes OwnershipControls as its encode body does
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L77-L82):
// ENCODE_START(v, compat) around payload, the setting's byte and whatever a
// later version appends.
func ownershipBytes(v, compat uint8, payload ...byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(v, compat)
	e.Raw(payload)
	e.EndStruct(f)
	return e.Bytes()
}

// ownershipDoc is an OwnershipControls document whose root start tag carries
// attrs and holds inner.
func ownershipDoc(attrs, inner string) []byte {
	return []byte("<OwnershipControls" + attrs + ">" + inner + "</OwnershipControls>")
}

// ownershipRule is a Rule holding an ObjectOwnership of value.
func ownershipRule(value string) string {
	return element("Rule", element("ObjectOwnership", value))
}

func parseOwnership(body []byte) acl.Ownership {
	GinkgoHelper()
	o, err := acl.ParseOwnershipXML(body)
	Expect(err).NotTo(HaveOccurred(), "body %q", body)
	return o
}

// ownershipRefusal parses body, which must be refused as MalformedXML with
// ObjectWriter, and returns the refusal.
func ownershipRefusal(body []byte) *acl.ParseError {
	GinkgoHelper()
	o, err := acl.ParseOwnershipXML(body)
	Expect(err).To(MatchError(acl.ErrMalformedXML), "body %.120q", body)
	Expect(o).To(Equal(acl.ObjectWriter), "the setting returned with a refusal of %.120q", body)
	Expect(err).To(BeAssignableToTypeOf(&acl.ParseError{}), "body %.120q", body)
	pe, _ := errors.AsType[*acl.ParseError](err)
	return pe
}

var _ = Describe("Ownership", func() {
	DescribeTable("writes and reads each setting as its byte inside ENCODE_START(1, 1)",
		func(o acl.Ownership, b []byte, name string) {
			for _, r := range releases {
				Expect(encodeAt(o, r)).To(Equal(b), "release %v", r)
			}
			Expect(decodeWhole(b, acl.DecodeOwnership)).To(Equal(o))
			Expect(o.String()).To(Equal(name))
		},
		Entry("BucketOwnerEnforced, 0", acl.BucketOwnerEnforced, []byte{1, 1, 1, 0, 0, 0, 0}, "BucketOwnerEnforced"),
		Entry("BucketOwnerPreferred, 1", acl.BucketOwnerPreferred, []byte{1, 1, 1, 0, 0, 0, 1}, "BucketOwnerPreferred"),
		Entry("ObjectWriter, 2", acl.ObjectWriter, []byte{1, 1, 1, 0, 0, 0, 2}, "ObjectWriter"),
	)
	It("skips what a later version appends, as DECODE_START(1) does", func() {
		b := ownershipBytes(2, 1, 1, 0xAA, 0xBB)
		Expect(decodeWhole(b, acl.DecodeOwnership)).To(Equal(acl.BucketOwnerPreferred))
	})
	DescribeTable("keeps a byte outside the three as stored, which to_string names invalid",
		func(v uint8) {
			b := ownershipBytes(1, 1, v)
			o := decodeWhole(b, acl.DecodeOwnership)
			Expect(o).To(Equal(acl.Ownership(v)))
			Expect(o.String()).To(Equal("invalid"))
			Expect(encodeAt(o, denc.Tentacle)).To(Equal(b))
		},
		Entry("3, the first byte past the three", uint8(3)),
		Entry("255", uint8(255)),
	)
	DescribeTable("reads a failed decode as ObjectWriter, OwnershipControls' default, never BucketOwnerEnforced",
		func(b []byte, want error) {
			d := denc.NewDecoder(b)
			Expect(acl.DecodeOwnership(d)).To(Equal(acl.ObjectWriter))
			Expect(d.Err()).To(MatchError(want))
		},
		Entry("a compat version above 1", ownershipBytes(2, 2, 0), denc.ErrIncompatible),
		Entry("no bytes", []byte{}, denc.ErrShortBuffer),
		Entry("a header whose struct_len runs past the end", []byte{1, 1, 1, 0, 0, 0}, denc.ErrShortBuffer),
		Entry("a struct_len of 0 before a 0 byte, which the read takes past the struct end",
			[]byte{1, 1, 0, 0, 0, 0, 0}, denc.ErrOverread),
	)
})

var _ = Describe("OwnershipOf", func() {
	It("names the attr as v21 does", func() {
		Expect(acl.AttrOwnershipControls).To(Equal("user.rgw.ownership-controls"))
	})
	DescribeTable("reads the setting a bucket's attr holds",
		func(o acl.Ownership) {
			attrs := map[string][]byte{"user.rgw.acl": {9}, acl.AttrOwnershipControls: ownershipBytes(1, 1, uint8(o))}
			Expect(acl.OwnershipOf(attrs)).To(Equal(o))
		},
		Entry("BucketOwnerEnforced", acl.BucketOwnerEnforced),
		Entry("BucketOwnerPreferred", acl.BucketOwnerPreferred),
		Entry("ObjectWriter", acl.ObjectWriter),
	)
	DescribeTable("reads a bucket without the attr as ObjectWriter, as get_object_ownership defaults it",
		func(attrs map[string][]byte) {
			Expect(acl.OwnershipOf(attrs)).To(Equal(acl.ObjectWriter))
		},
		Entry("no attrs", map[string][]byte(nil)),
		Entry("other attrs only", map[string][]byte{"user.rgw.acl": {9}, "user.rgw.public-access": {1}}),
	)
	DescribeTable("reads an attr that does not decode as ObjectWriter, as get_object_ownership catches the throw",
		func(b []byte) {
			Expect(acl.OwnershipOf(map[string][]byte{acl.AttrOwnershipControls: b})).To(Equal(acl.ObjectWriter), "attr %x", b)
		},
		Entry("an empty attr", []byte{}),
		Entry("a header cut short", []byte{1, 1, 1, 0}),
		Entry("an attr truncated before its setting", []byte{1, 1, 1, 0, 0, 0}),
		Entry("a compat version above 1 around BucketOwnerEnforced", ownershipBytes(2, 2, 0)),
		Entry("a struct_len of 0 before a BucketOwnerEnforced byte", []byte{1, 1, 0, 0, 0, 0, 0}),
	)
	DescribeTable("reads a byte outside the three as ObjectWriter, as each of v21's readers treats it",
		func(v uint8) {
			Expect(acl.OwnershipOf(map[string][]byte{acl.AttrOwnershipControls: ownershipBytes(1, 1, v)})).To(Equal(acl.ObjectWriter))
		},
		Entry("3", uint8(3)),
		Entry("255", uint8(255)),
	)
	It("ignores bytes after the encoding, as decode does", func() {
		b := append(ownershipBytes(1, 1, 0), 0xFF, 0xFF)
		Expect(acl.OwnershipOf(map[string][]byte{acl.AttrOwnershipControls: b})).To(Equal(acl.BucketOwnerEnforced))
	})
	It("reads the setting of a later version past what it appends", func() {
		b := ownershipBytes(2, 1, 0, 9)
		Expect(acl.OwnershipOf(map[string][]byte{acl.AttrOwnershipControls: b})).To(Equal(acl.BucketOwnerEnforced))
	})
})

var _ = Describe("ParseOwnershipXML", func() {
	DescribeTable("reads each of the three names",
		func(name string, want acl.Ownership) {
			Expect(parseOwnership(ownershipDoc(" "+s3NS, ownershipRule(name)))).To(Equal(want))
		},
		Entry("BucketOwnerEnforced", "BucketOwnerEnforced", acl.BucketOwnerEnforced),
		Entry("BucketOwnerPreferred", "BucketOwnerPreferred", acl.BucketOwnerPreferred),
		Entry("ObjectWriter", "ObjectWriter", acl.ObjectWriter),
	)
	DescribeTable("reads the forms XML gives the same elements and text",
		func(body string, want acl.Ownership) {
			Expect(parseOwnership([]byte(body))).To(Equal(want))
		},
		Entry("the document an SDK sends, with its declaration",
			`<?xml version="1.0" encoding="UTF-8"?>`+string(ownershipDoc(" "+s3NS, ownershipRule("BucketOwnerPreferred"))),
			acl.BucketOwnerPreferred),
		Entry("no namespace", string(ownershipDoc("", ownershipRule("BucketOwnerEnforced"))), acl.BucketOwnerEnforced),
		Entry("an empty default namespace", string(ownershipDoc(` xmlns=""`, ownershipRule("ObjectWriter"))), acl.ObjectWriter),
		Entry("the S3 namespace declared again on Rule",
			string(ownershipDoc(" "+s3NS, `<Rule `+s3NS+`>`+element("ObjectOwnership", "BucketOwnerEnforced")+`</Rule>`)),
			acl.BucketOwnerEnforced),
		Entry("a UTF-8 byte order mark, which expat skips",
			"\xef\xbb\xbf"+string(ownershipDoc("", ownershipRule("BucketOwnerPreferred"))), acl.BucketOwnerPreferred),
		Entry("whitespace, comments and processing instructions around the elements",
			"<!-- before -->\n"+string(ownershipDoc(" "+s3NS,
				"\n  <!-- a -->\n  <Rule>\n    "+element("ObjectOwnership", "BucketOwnerEnforced")+"\n  <?pi x?></Rule>\n"))+"\n",
			acl.BucketOwnerEnforced),
		Entry("character references spelling the name",
			string(ownershipDoc("", ownershipRule("&#79;bject&#x57;riter"))), acl.ObjectWriter),
		Entry("a CDATA section holding the name",
			string(ownershipDoc("", ownershipRule("<![CDATA[BucketOwnerEnforced]]>"))), acl.BucketOwnerEnforced),
		Entry("a comment inside the name, which both readers drop",
			string(ownershipDoc("", ownershipRule("Bucket<!-- x -->OwnerPreferred"))), acl.BucketOwnerPreferred),
		Entry("a character reference to a space between the elements",
			string(ownershipDoc("", "&#32;"+ownershipRule("ObjectWriter"))), acl.ObjectWriter),
	)
	DescribeTable("refuses what v21 refuses once expat has read the body, with v21's message",
		func(body, message string) {
			Expect(ownershipRefusal([]byte(body)).Message).To(Equal(message), "body %.120q", body)
		},
		Entry("another root", `<Ownership>`+ownershipRule("ObjectWriter")+`</Ownership>`, msgNoOwnershipControls),
		Entry("a root whose name differs in case", `<ownershipControls/>`, msgNoOwnershipControls),
		Entry("a prefixed root, which v21 reads as another name",
			`<s3:OwnershipControls xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/">`+ownershipRule("ObjectWriter")+
				`</s3:OwnershipControls>`, msgNoOwnershipControls),
		Entry("a root with no Rule", `<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`, msgNoRule),
		Entry("a root holding whitespace only", string(ownershipDoc("", "\n  \n")), msgNoRule),
		Entry("an empty Rule", string(ownershipDoc("", "<Rule/>")), msgNoObjectOwnership),
		Entry("a Rule holding whitespace and a comment only", string(ownershipDoc("", "<Rule>\n <!-- c --> </Rule>")), msgNoObjectOwnership),
		Entry("a name in lower case", string(ownershipDoc("", ownershipRule("objectwriter"))), msgBadObjectOwnership),
		Entry("whitespace around a name, which v21 does not trim",
			string(ownershipDoc("", ownershipRule(" BucketOwnerEnforced "))), msgBadObjectOwnership),
		Entry("a line break after a name", string(ownershipDoc("", ownershipRule("ObjectWriter\n"))), msgBadObjectOwnership),
		Entry("an empty name", string(ownershipDoc("", ownershipRule(""))), msgBadObjectOwnership),
		Entry("a self-closed ObjectOwnership", string(ownershipDoc("", "<Rule><ObjectOwnership/></Rule>")), msgBadObjectOwnership),
		Entry("invalid, to_string's name for a byte outside the three", string(ownershipDoc("", ownershipRule("invalid"))), msgBadObjectOwnership),
		Entry("a name with a suffix", string(ownershipDoc("", ownershipRule("ObjectWriters"))), msgBadObjectOwnership),
		Entry("a name of 64 KiB", string(ownershipDoc("", ownershipRule(strings.Repeat("B", 1<<16)))), msgBadObjectOwnership),
	)

	// at is the offset of the first byte of what the parser must stop at.
	type hostile struct {
		body []byte
		at   int
	}
	// nested is a well-formed document whose prefix and suffix hold depth
	// nested elements, as deep as fits under MaxOwnershipXML.
	nested := func(prefix, suffix string, depth int) hostile {
		body := prefix + strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth) + suffix
		Expect(len(body)).To(BeNumerically("<=", acl.MaxOwnershipXML))
		return hostile{body: []byte(body), at: len(prefix)}
	}
	refusedAt := func(body, marker string) hostile {
		i := strings.Index(body, marker)
		Expect(i).To(BeNumerically(">=", 0), "marker %q in %q", marker, body)
		return hostile{body: []byte(body), at: i}
	}
	lastAt := func(body, marker string) hostile {
		return hostile{body: []byte(body), at: strings.LastIndex(body, marker)}
	}
	DescribeTable("refuses a hostile or out-of-schema document at its first offending token, with no message",
		func(h func() hostile) {
			c := h()
			pe := ownershipRefusal(c.body)
			Expect(pe).To(MatchError(ContainSubstring(fmt.Sprintf("at byte %d", c.at))), "body %.120q", c.body)
			Expect(pe.Message).To(BeEmpty(), "body %.120q", c.body)
		},
		Entry("a DOCTYPE whose internal entity spells a name, which expat expands", func() hostile {
			return refusedAt(`<!DOCTYPE OwnershipControls [<!ENTITY o "ObjectWriter">]>`+
				string(ownershipDoc("", ownershipRule("&o;"))), "<!DOCTYPE")
		}),
		Entry("a DOCTYPE after the XML declaration", func() hostile {
			return refusedAt(`<?xml version="1.0"?><!DOCTYPE OwnershipControls>`+
				string(ownershipDoc("", ownershipRule("ObjectWriter"))), "<!DOCTYPE")
		}),
		Entry("an external entity", func() hostile {
			return refusedAt(`<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>`+
				string(ownershipDoc("", ownershipRule("&e;"))), "<!DOCTYPE")
		}),
		Entry("nested entities, the billion laughs", func() hostile {
			var dtd strings.Builder
			dtd.WriteString(`<!DOCTYPE l [<!ENTITY l0 "lol">`)
			for i := 1; i < 10; i++ {
				fmt.Fprintf(&dtd, `<!ENTITY l%d "%s">`, i, strings.Repeat(fmt.Sprintf("&l%d;", i-1), 10))
			}
			dtd.WriteString(`]>`)
			return refusedAt(dtd.String()+string(ownershipDoc("", ownershipRule("&l9;"))), "<!DOCTYPE")
		}),
		Entry("a declaration after the root", func() hostile {
			return refusedAt(string(ownershipDoc("", ownershipRule("ObjectWriter")))+`<!DOCTYPE x>`, "<!DOCTYPE")
		}),
		Entry("an undeclared entity reference", func() hostile {
			return refusedAt(string(ownershipDoc("", ownershipRule("&o;"))), "&o;")
		}),
		Entry("140000 nested unknown elements under the root, which v21 reads and ignores", func() hostile {
			return nested(`<OwnershipControls>`, ownershipRule("ObjectWriter")+`</OwnershipControls>`, 140000)
		}),
		Entry("140000 nested unknown elements under Rule", func() hostile {
			return nested(`<OwnershipControls><Rule>`, element("ObjectOwnership", "ObjectWriter")+`</Rule></OwnershipControls>`, 140000)
		}),
		Entry("140000 nested elements inside ObjectOwnership, whose text v21 still reads", func() hostile {
			return nested(`<OwnershipControls><Rule><ObjectOwnership>Object`, `Writer</ObjectOwnership></Rule></OwnershipControls>`, 140000)
		}),
		Entry("the root nested in itself", func() hostile {
			return refusedAt(`<OwnershipControls><OwnershipControls>`+ownershipRule("ObjectWriter")+
				`</OwnershipControls></OwnershipControls>`, `<OwnershipControls><Rule>`)
		}),
		Entry("an unknown element in the root, which v21 ignores", func() hostile {
			return refusedAt(string(ownershipDoc("", ownershipRule("ObjectWriter")+"<Foo/>")), "<Foo/>")
		}),
		Entry("Rules for Rule, which v21 ignores and so finds no Rule", func() hostile {
			return refusedAt(string(ownershipDoc(" "+s3NS, element("Rules", element("ObjectOwnership", "ObjectWriter")))), "<Rules>")
		}),
		Entry("an unknown element in Rule, which v21 ignores", func() hostile {
			return refusedAt(string(ownershipDoc("", `<Rule><ID>x</ID>`+element("ObjectOwnership", "ObjectWriter")+`</Rule>`)), "<ID>")
		}),
		Entry("ObjectOwnership directly in the root, which v21 does not look for there", func() hostile {
			return refusedAt(string(ownershipDoc("", element("ObjectOwnership", "ObjectWriter"))), "<ObjectOwnership>")
		}),
		Entry("a repeated Rule, whose first v21 reads", func() hostile {
			return lastAt(string(ownershipDoc("", ownershipRule("BucketOwnerEnforced")+ownershipRule("ObjectWriter"))), "<Rule>")
		}),
		Entry("a repeated Rule after an empty one, which v21 refuses for its missing ObjectOwnership", func() hostile {
			return lastAt(string(ownershipDoc("", "<Rule/>"+ownershipRule("ObjectWriter"))), "<Rule>")
		}),
		Entry("a repeated ObjectOwnership, whose first v21 reads", func() hostile {
			body := string(ownershipDoc("", element("Rule", element("ObjectOwnership", "ObjectWriter")+
				element("ObjectOwnership", "BucketOwnerEnforced"))))
			return lastAt(body, "<ObjectOwnership>")
		}),
		Entry("an element inside ObjectOwnership", func() hostile {
			return refusedAt(string(ownershipDoc("", ownershipRule("Object<b/>Writer"))), "<b/>")
		}),
		Entry("a prefixed Rule, which v21 reads as another name", func() hostile {
			return refusedAt(string(ownershipDoc("", `<s3:Rule>`+element("ObjectOwnership", "ObjectWriter")+`</s3:Rule>`)), "<s3:")
		}),
		Entry("a prefixed ObjectOwnership", func() hostile {
			return refusedAt(string(ownershipDoc("", element("Rule", element("s3:ObjectOwnership", "ObjectWriter")))), "<s3:")
		}),
		Entry("a prefix declaration, even one naming S3's namespace", func() hostile {
			return refusedAt(string(ownershipDoc(` xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"`, ownershipRule("ObjectWriter"))),
				"<OwnershipControls")
		}),
		Entry("a foreign namespace on the root, which v21 ignores", func() hostile {
			return refusedAt(string(ownershipDoc(` xmlns="urn:evil"`, ownershipRule("ObjectWriter"))), "<OwnershipControls")
		}),
		Entry("S3's namespace without its trailing slash", func() hostile {
			return refusedAt(string(ownershipDoc(` xmlns="http://s3.amazonaws.com/doc/2006-03-01"`, ownershipRule("ObjectWriter"))),
				"<OwnershipControls")
		}),
		Entry("a foreign namespace on Rule", func() hostile {
			return refusedAt(string(ownershipDoc(" "+s3NS, `<Rule xmlns="urn:evil">`+element("ObjectOwnership", "ObjectWriter")+`</Rule>`)),
				"<Rule")
		}),
		Entry("an attribute on ObjectOwnership, which v21 ignores", func() hostile {
			return refusedAt(string(ownershipDoc("", `<Rule><ObjectOwnership foo="bar">ObjectWriter</ObjectOwnership></Rule>`)),
				"<ObjectOwnership")
		}),
		Entry("an xml: attribute on the root", func() hostile {
			return refusedAt(string(ownershipDoc(` xml:lang="en"`, ownershipRule("ObjectWriter"))), "<OwnershipControls")
		}),
		Entry("text in the root, which v21 ignores", func() hostile {
			return refusedAt(string(ownershipDoc("", "junk"+ownershipRule("ObjectWriter"))), "junk")
		}),
		Entry("text in Rule, which v21 ignores", func() hostile {
			return refusedAt(string(ownershipDoc("", element("Rule", "junk"+element("ObjectOwnership", "ObjectWriter")))), "junk")
		}),
		Entry("a no-break space between the elements, which is not XML whitespace", func() hostile {
			return refusedAt(string(ownershipDoc("", " "+ownershipRule("ObjectWriter"))), " ")
		}),
		Entry("text after the root, which expat refuses too", func() hostile {
			return refusedAt(string(ownershipDoc("", ownershipRule("ObjectWriter")))+"junk", "junk")
		}),
		Entry("a second root, which expat refuses too", func() hostile {
			return refusedAt(string(ownershipDoc("", ownershipRule("ObjectWriter")))+`<Second/>`, "<Second/>")
		}),
		Entry("an end tag after the root", func() hostile {
			return lastAt(string(ownershipDoc("", ownershipRule("ObjectWriter")))+`</OwnershipControls>`, "</OwnershipControls>")
		}),
		Entry("an offset counted from the byte order mark", func() hostile {
			return refusedAt("\xef\xbb\xbf"+string(ownershipDoc("", "<Foo/>")), "<Foo/>")
		}),
	)
	DescribeTable("refuses a document that is not well-formed with no message, as v21's failed parse sets none",
		func(body string) {
			Expect(ownershipRefusal([]byte(body)).Message).To(BeEmpty(), "body %q", body)
		},
		Entry("an empty body", ""),
		Entry("whitespace alone", " \n"),
		Entry("an unclosed root holding no Rule, which v21 refuses before it looks for one", `<OwnershipControls>`),
		Entry("an unclosed root holding another name",
			`<OwnershipControls>`+ownershipRule("Nobody")),
		Entry("an unclosed Rule", `<OwnershipControls><Rule>`+element("ObjectOwnership", "ObjectWriter")+`</OwnershipControls>`),
		Entry("a mismatched end tag", string(ownershipDoc("", `<Rule><ObjectOwnership>ObjectWriter</Rule></ObjectOwnership>`))),
		Entry("an end tag with no start", `</OwnershipControls>`),
		Entry("an XML declaration not at the start", " "+`<?xml version="1.0"?>`+string(ownershipDoc("", ownershipRule("ObjectWriter")))),
		Entry("an encoding other than UTF-8",
			`<?xml version="1.0" encoding="ISO-8859-1"?>`+string(ownershipDoc("", ownershipRule("ObjectWriter")))),
		Entry("a duplicate xmlns attribute", string(ownershipDoc(` xmlns="" xmlns=""`, ownershipRule("ObjectWriter")))),
		Entry("a NUL byte in the name", string(ownershipDoc("", ownershipRule("Object\x00Writer")))),
		Entry("invalid UTF-8 in the name", string(ownershipDoc("", ownershipRule("Object\xffWriter")))),
	)
	DescribeTable("keeps a refusal's text short when the client's text in it is long",
		func(body []byte) {
			pe := ownershipRefusal(body)
			Expect(len(pe.Error())).To(BeNumerically("<", 512), "error %.200q", pe)
		},
		Entry("a long root name", []byte("<"+strings.Repeat("a", 1<<19)+"/>")),
		Entry("a long unknown element", ownershipDoc("", "<"+strings.Repeat("b", 1<<19)+"/>")),
		Entry("a long element inside ObjectOwnership", ownershipDoc("", "<Rule><ObjectOwnership><"+strings.Repeat("c", 1<<19)+"/>")),
		Entry("a long attribute name and value", ownershipDoc(" "+strings.Repeat("n", 1<<18)+`="`+strings.Repeat("v", 1<<18)+`"`, "")),
		Entry("a long declaration", []byte("<!"+strings.Repeat("D", 1<<19)+">"+string(ownershipDoc("", "")))),
		Entry("a long undeclared entity, which Go's decoder names", ownershipDoc("", ownershipRule("&"+strings.Repeat("e", 1<<19)+";"))),
		Entry("a long processing instruction target, which xmltext names", ownershipDoc("", "<?"+strings.Repeat("p", 1<<19)+"*?>")),
		Entry("a long name", ownershipDoc("", ownershipRule(strings.Repeat("O", 1<<19)))),
		Entry("64 next-line controls as the name, each quoted as six bytes",
			ownershipDoc("", ownershipRule(strings.Repeat("\u0085", 64)))),
		Entry("64 astral tag runes as the name, each quoted as ten bytes",
			ownershipDoc("", ownershipRule(strings.Repeat("\U000E0001", 64)))),
		Entry("a long undeclared entity of four-byte letters, which Go's decoder names",
			ownershipDoc("", ownershipRule("&"+strings.Repeat("\U00020000", 1<<16)+";"))),
		Entry("64 astral tag runes as the root's namespace",
			ownershipDoc(` xmlns="`+strings.Repeat("\U000E0001", 64)+`"`, ownershipRule("ObjectWriter"))),
	)
	DescribeTable("gives a root other than OwnershipControls v21's root message even when expat would refuse the body",
		func(body string) {
			_, perr := xmltext.Parse([]byte(body))
			Expect(perr).To(MatchError(xmltext.ErrNotWellFormed), "expat refuses %q", body)
			Expect(ownershipRefusal([]byte(body)).Message).To(Equal(msgNoOwnershipControls), "body %q", body)
		},
		Entry("an unclosed root", `<Foo>`),
		Entry("text after the root", `<Foo/>junk`),
		Entry("an XML declaration after whitespace, before the root", ` <?xml version="1.0"?><Foo/>`),
		Entry("a processing instruction with a bad target, before the root", `<?p*?><Foo/>`),
		Entry("a repeated attribute on the root tag", `<Foo a="1" a="1"/>`),
	)
	DescribeTable("refuses with no message, as v21 does, a malformation Go's decoder meets before or on a foreign root's start tag",
		func(body string) {
			_, perr := xmltext.Parse([]byte(body))
			Expect(perr).To(MatchError(xmltext.ErrNotWellFormed), "expat refuses %q", body)
			Expect(ownershipRefusal([]byte(body)).Message).To(BeEmpty(), "body %q", body)
		},
		Entry("a comment holding two hyphens, before the root", `<!-- a -- b --><Foo/>`),
		Entry("a NUL byte before the root", "\x00<Foo/>"),
		Entry("an unquoted attribute value on the root tag", `<Foo a=1/>`),
		Entry("a less-than sign in an attribute value on the root tag", `<Foo a="<"/>`),
		Entry("a root name that starts with a digit", `<1Foo/>`),
	)
	It("refuses a name in a body as MalformedXML, not as the header's InvalidArgument", func() {
		_, err := acl.ParseOwnershipXML(ownershipDoc("", ownershipRule("Nobody")))
		Expect(err).To(MatchError(acl.ErrMalformedXML))
		Expect(err).NotTo(MatchError(acl.ErrInvalid))
	})
	DescribeTable("cuts a long client text in a refusal at a rune boundary",
		func(body []byte) {
			pe := ownershipRefusal(body)
			Expect(pe).To(MatchError(ContainSubstring("...")), "the client's text is cut")
			Expect(pe.Error()).To(Satisfy(utf8.ValidString))
		},
		Entry("an encoding of emoji, which Go's decoder names as the scan reads it",
			[]byte(`<?xml version="1.0" encoding="`+strings.Repeat("\U0001F600", 128)+`"?><OwnershipControls/>`)),
		Entry("a processing instruction target of ideographs, which xmltext names",
			ownershipDoc("", "<?"+strings.Repeat("一", 1<<10)+"*?>"+ownershipRule("ObjectWriter"))),
	)
	It("refuses a body over MaxOwnershipXML before reading it", func() {
		inner := ownershipRule("ObjectWriter")
		pad := acl.MaxOwnershipXML + 1 - len(ownershipDoc("", inner))
		body := ownershipDoc("", strings.Repeat(" ", pad)+inner)
		Expect(body).To(HaveLen(acl.MaxOwnershipXML + 1))
		pe := ownershipRefusal(body)
		Expect(pe).To(MatchError(ContainSubstring("exceeds")))
		Expect(pe.Message).To(BeEmpty())
	})
	It("reads a body of MaxOwnershipXML bytes, rgw_max_put_param_size's default", func() {
		Expect(acl.MaxOwnershipXML).To(Equal(1 << 20))
		inner := ownershipRule("BucketOwnerEnforced")
		pad := acl.MaxOwnershipXML - len(ownershipDoc("", inner))
		body := ownershipDoc("", strings.Repeat("\n", pad)+inner)
		Expect(body).To(HaveLen(acl.MaxOwnershipXML))
		Expect(parseOwnership(body)).To(Equal(acl.BucketOwnerEnforced))
	})
})

var _ = Describe("ParseOwnership", func() {
	DescribeTable("reads each of the three names",
		func(name string, want acl.Ownership) {
			o, err := acl.ParseOwnership(name)
			Expect(err).NotTo(HaveOccurred())
			Expect(o).To(Equal(want))
		},
		Entry("BucketOwnerEnforced", "BucketOwnerEnforced", acl.BucketOwnerEnforced),
		Entry("BucketOwnerPreferred", "BucketOwnerPreferred", acl.BucketOwnerPreferred),
		Entry("ObjectWriter", "ObjectWriter", acl.ObjectWriter),
	)
	DescribeTable("refuses any other name as radosgw's -EINVAL with parse's message, and ObjectWriter",
		func(name string) {
			o, err := acl.ParseOwnership(name)
			Expect(o).To(Equal(acl.ObjectWriter), "name %q", name)
			Expect(err).To(MatchError(acl.ErrInvalid), "name %q", name)
			Expect(err).NotTo(MatchError(acl.ErrMalformedXML), "name %q", name)
			Expect(err).To(BeAssignableToTypeOf(&acl.ParseError{}), "name %q", name)
			pe, _ := errors.AsType[*acl.ParseError](err)
			Expect(pe.Message).To(Equal(msgBadObjectOwnership), "name %q", name)
		},
		Entry("a name in lower case", "bucketownerenforced"),
		Entry("whitespace after a name, which parse does not trim", "ObjectWriter "),
		Entry("an empty name", ""),
		Entry("invalid, to_string's name for a byte outside the three", "invalid"),
	)
	It("keeps its refusal short when the name is long", func() {
		_, err := acl.ParseOwnership(strings.Repeat("\U000E0001", 1<<16))
		Expect(len(err.Error())).To(BeNumerically("<", 512))
	})
})

var _ = Describe("Ownership.MarshalS3XML", func() {
	DescribeTable("writes v21's document in the S3 namespace",
		func(o acl.Ownership, want string) {
			Expect(string(o.MarshalS3XML())).To(Equal(want))
		},
		Entry("BucketOwnerEnforced", acl.BucketOwnerEnforced, `<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Rule><ObjectOwnership>BucketOwnerEnforced</ObjectOwnership></Rule></OwnershipControls>`),
		Entry("BucketOwnerPreferred", acl.BucketOwnerPreferred, `<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Rule><ObjectOwnership>BucketOwnerPreferred</ObjectOwnership></Rule></OwnershipControls>`),
		Entry("ObjectWriter", acl.ObjectWriter, `<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Rule><ObjectOwnership>ObjectWriter</ObjectOwnership></Rule></OwnershipControls>`),
		Entry("a byte outside the three, named invalid as to_string names it", acl.Ownership(3),
			`<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
				`<Rule><ObjectOwnership>invalid</ObjectOwnership></Rule></OwnershipControls>`),
	)
	It("reads back as the setting it writes, for each of the three", func() {
		for _, o := range []acl.Ownership{acl.BucketOwnerEnforced, acl.BucketOwnerPreferred, acl.ObjectWriter} {
			Expect(parseOwnership(o.MarshalS3XML())).To(Equal(o), "setting %v", o)
		}
	})
})
