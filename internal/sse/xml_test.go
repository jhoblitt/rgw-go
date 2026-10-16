package sse_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/sse"
)

const s3NS = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

// doc is a ServerSideEncryptionConfiguration document whose root start tag
// carries attrs and holds inner.
func doc(attrs, inner string) []byte {
	return []byte("<ServerSideEncryptionConfiguration" + attrs + ">" + inner + "</ServerSideEncryptionConfiguration>")
}

// element is name holding text.
func element(name, text string) string {
	return "<" + name + ">" + text + "</" + name + ">"
}

func rule(inner string) string  { return element("Rule", inner) }
func apply(inner string) string { return element("ApplyServerSideEncryptionByDefault", inner) }

// algorithm is a Rule naming alg alone.
func algorithm(alg string) string { return rule(apply(element("SSEAlgorithm", alg))) }

// withRule is a configuration whose rule names alg, keyID and bucketKey.
func withRule(alg, keyID string, bucketKey bool) sse.BucketEncryptionConfig {
	return sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{
		Apply:            sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: keyID, SSEAlgorithm: alg},
		BucketKeyEnabled: bucketKey,
	}}
}

func parse(body []byte) sse.BucketEncryptionConfig {
	GinkgoHelper()
	c, err := sse.ParseXML(body)
	Expect(err).NotTo(HaveOccurred(), "body %q", body)
	return c
}

func refused(body []byte) error {
	GinkgoHelper()
	c, err := sse.ParseXML(body)
	Expect(err).To(MatchError(sse.ErrMalformedXML), "body %.200q", body)
	Expect(c).To(Equal(sse.BucketEncryptionConfig{}), "body %.200q", body)
	return err
}

var _ = Describe("ParseXML", func() {
	DescribeTable("reads each document an SDK sends as radosgw reads it",
		func(body string, want sse.BucketEncryptionConfig) {
			Expect(parse([]byte(body))).To(Equal(want))
		},
		Entry("AES256, as s3-tests' _put_bucket_encryption_s3 sends it",
			`<?xml version="1.0" encoding="UTF-8"?>`+string(doc(" "+s3NS, algorithm("AES256"))),
			withRule("AES256", "", false)),
		Entry("aws:kms with a key id, which the AWS SDK for Go writes first",
			string(doc(" "+s3NS, rule(apply(element("KMSMasterKeyID", "fool-me-again")+element("SSEAlgorithm", "aws:kms"))))),
			withRule("aws:kms", "fool-me-again", false)),
		Entry("aws:kms:dsse with a key ARN and a bucket key",
			string(doc(" "+s3NS, rule(apply(element("SSEAlgorithm", "aws:kms:dsse")+
				element("KMSMasterKeyID", "arn:aws:kms:us-east-2:111122223333:key/1234abcd"))+
				element("BucketKeyEnabled", "true")))),
			withRule("aws:kms:dsse", "arn:aws:kms:us-east-2:111122223333:key/1234abcd", true)),
		Entry("aws:fsx", string(doc(" "+s3NS, algorithm("aws:fsx"))), withRule("aws:fsx", "", false)),
		Entry("aws:backup", string(doc(" "+s3NS, algorithm("aws:backup"))), withRule("aws:backup", "", false)),
		Entry("BucketKeyEnabled false",
			string(doc(" "+s3NS, rule(apply(element("SSEAlgorithm", "aws:kms"))+element("BucketKeyEnabled", "false")))),
			withRule("aws:kms", "", false)),
	)
	DescribeTable("reads an absent optional element as decode_xml does, its default in its place",
		func(body string, want sse.BucketEncryptionConfig) {
			Expect(parse([]byte(body))).To(Equal(want))
		},
		Entry("no Rule: a configuration without one", string(doc(" "+s3NS, "")), sse.BucketEncryptionConfig{}),
		Entry("a self-closed root", `<ServerSideEncryptionConfiguration `+s3NS+`/>`, sse.BucketEncryptionConfig{}),
		Entry("an empty Rule: a rule that names nothing", string(doc("", "<Rule/>")), withRule("", "", false)),
		Entry("a Rule holding BucketKeyEnabled alone", string(doc("", rule(element("BucketKeyEnabled", "true")))),
			withRule("", "", true)),
		Entry("an empty ApplyServerSideEncryptionByDefault", string(doc("", rule("<ApplyServerSideEncryptionByDefault/>"))),
			withRule("", "", false)),
		Entry("an empty SSEAlgorithm", string(doc("", algorithm(""))), withRule("", "", false)),
	)
	DescribeTable("reads the forms XML gives the same elements and text",
		func(body string, want sse.BucketEncryptionConfig) {
			Expect(parse([]byte(body))).To(Equal(want))
		},
		Entry("no namespace", string(doc("", algorithm("AES256"))), withRule("AES256", "", false)),
		Entry("an empty default namespace", string(doc(` xmlns=""`, algorithm("AES256"))), withRule("AES256", "", false)),
		Entry("the S3 namespace declared again on a child",
			string(doc(" "+s3NS, `<Rule `+s3NS+`>`+apply(element("SSEAlgorithm", "AES256"))+`</Rule>`)),
			withRule("AES256", "", false)),
		Entry("a UTF-8 byte order mark, which expat skips", "\xef\xbb\xbf"+string(doc("", algorithm("AES256"))),
			withRule("AES256", "", false)),
		Entry("whitespace, comments and processing instructions around the elements",
			"<!-- before -->\n"+string(doc(" "+s3NS, "\n  <!-- a -->\n  <Rule>\n    <?pi x?>\n    "+
				apply("\n      "+element("SSEAlgorithm", "AES256")+"\n    ")+"\n  </Rule>\n"))+"\n<!-- after -->\n",
			withRule("AES256", "", false)),
		Entry("character references spelling AES256", string(doc("", algorithm("&#65;&#x45;S256"))),
			withRule("AES256", "", false)),
		Entry("a CDATA section holding aws:kms", string(doc("", algorithm("<![CDATA[aws:kms]]>"))),
			withRule("aws:kms", "", false)),
		Entry("a comment inside a value, which both readers drop", string(doc("", algorithm("AES<!-- x -->256"))),
			withRule("AES256", "", false)),
		Entry("BucketKeyEnabled before the default, the elements in any order",
			string(doc("", rule(element("BucketKeyEnabled", "true")+apply(element("SSEAlgorithm", "aws:kms"))))),
			withRule("aws:kms", "", true)),
	)
	DescribeTable("reads a key id as get_data does, its references decoded and nothing trimmed",
		func(text, want string) {
			body := doc("", rule(apply(element("SSEAlgorithm", "aws:kms")+element("KMSMasterKeyID", text))))
			Expect(parse(body)).To(Equal(withRule("aws:kms", want, false)))
		},
		Entry("whitespace around it kept", " k\n", " k\n"),
		Entry("escaped markup decoded", "a&lt;b&amp;c&gt;&quot;&apos;", `a<b&c>"'`),
		Entry("UTF-8 kept", "ключ", "ключ"),
		Entry("a carriage return and line feed read as a line feed, as expat reads them", "a\r\nb", "a\nb"),
	)
	DescribeTable("reads each BucketKeyEnabled form every reference gives one meaning, as radosgw reads it",
		func(value string, want bool) {
			body := doc("", rule(apply(element("SSEAlgorithm", "aws:kms"))+element("BucketKeyEnabled", value)))
			Expect(parse(body)).To(Equal(withRule("aws:kms", "", want)), "value %q", value)
		},
		Entry("TRUE, as strncasecmp reads it", "TRUE", true),
		Entry("False", "False", false),
		Entry("1, true to xs:boolean and to radosgw's integer fallback", "1", true),
		Entry("0", "0", false),
		Entry("XML whitespace around 1, which xs:boolean collapses and strtol skips", " \t1\n", true),
		Entry("1 inside 63 bytes of whitespace, 64 bytes in all", strings.Repeat(" ", 31)+"1"+strings.Repeat(" ", 32), true),
	)
	DescribeTable("refuses a BucketKeyEnabled no reference reads as a boolean as MalformedXML",
		func(value string) {
			Expect(refused(doc("", rule(element("BucketKeyEnabled", value))))).
				To(MatchError(ContainSubstring("BucketKeyEnabled is")), "value %q", value)
		},
		Entry("whitespace before true, which radosgw refuses too", " true"),
		Entry("2, which radosgw's integer fallback reads as true", "2"),
		Entry("-7, which radosgw reads as true", "-7"),
		Entry("01, which radosgw reads as true", "01"),
		Entry("yes", "yes"),
		Entry("empty, which radosgw refuses too", ""),
	)

	// at is the offset of the first byte of what the parser must stop at.
	type hostile struct {
		body []byte
		at   int
	}
	refusedAt := func(body, marker string) hostile {
		i := strings.Index(body, marker)
		Expect(i).To(BeNumerically(">=", 0), "marker %q in %q", marker, body)
		return hostile{body: []byte(body), at: i}
	}
	lastAt := func(body, marker string) hostile {
		return hostile{body: []byte(body), at: strings.LastIndex(body, marker)}
	}
	// nested is a well-formed document whose prefix and suffix hold depth
	// nested elements, as deep as fits under MaxConfigXML.
	nested := func(prefix, suffix string, depth int) hostile {
		body := prefix + strings.Repeat("<a>", depth) + strings.Repeat("</a>", depth) + suffix
		Expect(len(body)).To(BeNumerically("<=", sse.MaxConfigXML))
		return hostile{body: []byte(body), at: len(prefix)}
	}
	DescribeTable("refuses a hostile or out-of-schema document as MalformedXML at its first offending token",
		func(h func() hostile) {
			c := h()
			err := refused(c.body)
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("at byte %d", c.at))), "body %.200q", c.body)
		},
		Entry("a DOCTYPE whose internal entity spells a value, which expat expands", func() hostile {
			return refusedAt(`<!DOCTYPE ServerSideEncryptionConfiguration [<!ENTITY a "AES256">]>`+
				string(doc("", algorithm("&a;"))), "<!DOCTYPE")
		}),
		Entry("an external entity", func() hostile {
			return refusedAt(`<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>`+
				string(doc("", rule(apply(element("KMSMasterKeyID", "&e;"))))), "<!DOCTYPE")
		}),
		Entry("a declaration after the root", func() hostile {
			return refusedAt(string(doc("", ""))+`<!DOCTYPE x>`, "<!DOCTYPE")
		}),
		Entry("an undeclared entity reference", func() hostile {
			return refusedAt(string(doc("", algorithm("&a;"))), "&a;")
		}),
		Entry("140000 nested unknown elements under the root", func() hostile {
			return nested(`<ServerSideEncryptionConfiguration>`, `</ServerSideEncryptionConfiguration>`, 140000)
		}),
		Entry("140000 nested elements inside SSEAlgorithm", func() hostile {
			return nested(`<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>`,
				`AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`, 140000)
		}),
		Entry("a misspelled SSEAlgorithm, which radosgw ignores and stores as no algorithm", func() hostile {
			return refusedAt(string(doc(" "+s3NS, rule(apply(element("SseAlgorithm", "AES256"))))), "<SseAlgorithm>")
		}),
		Entry("a misspelled Rule, which radosgw ignores and stores as no rule", func() hostile {
			return refusedAt(string(doc(" "+s3NS, element("Rules", apply(element("SSEAlgorithm", "AES256"))))), "<Rules>")
		}),
		Entry("BlockedEncryptionTypes, which AWS reads and the floors ignore", func() hostile {
			return refusedAt(string(doc(" "+s3NS, rule(element("BlockedEncryptionTypes",
				element("EncryptionType", "SSE-C"))))), "<BlockedEncryptionTypes>")
		}),
		Entry("an unknown element in the default", func() hostile {
			return refusedAt(string(doc("", rule(apply(element("SSEAlgorithm", "AES256")+"<Foo/>")))), "<Foo/>")
		}),
		Entry("SSEAlgorithm directly under Rule, which radosgw ignores", func() hostile {
			return refusedAt(string(doc("", rule(element("SSEAlgorithm", "AES256")))), "<SSEAlgorithm>")
		}),
		Entry("BucketKeyEnabled under the default, which radosgw ignores", func() hostile {
			return refusedAt(string(doc("", rule(apply(element("BucketKeyEnabled", "true"))))), "<BucketKeyEnabled>")
		}),
		Entry("ApplyServerSideEncryptionByDefault directly under the root", func() hostile {
			return refusedAt(string(doc("", apply(element("SSEAlgorithm", "AES256")))), "<ApplyServer")
		}),
		Entry("a second Rule, which radosgw drops", func() hostile {
			return lastAt(string(doc("", algorithm("AES256")+algorithm("aws:kms"))), "<Rule>")
		}),
		Entry("a second default, which radosgw drops", func() hostile {
			return lastAt(string(doc("", rule(apply(element("SSEAlgorithm", "AES256"))+apply("")))), "<ApplyServer")
		}),
		Entry("a repeated SSEAlgorithm, whose first copy radosgw reads", func() hostile {
			return lastAt(string(doc("", rule(apply(element("SSEAlgorithm", "AES256")+element("SSEAlgorithm", "aws:kms"))))),
				"<SSEAlgorithm>")
		}),
		Entry("a repeated KMSMasterKeyID", func() hostile {
			return lastAt(string(doc("", rule(apply(element("SSEAlgorithm", "aws:kms")+
				element("KMSMasterKeyID", "a")+element("KMSMasterKeyID", "b"))))), "<KMSMasterKeyID>")
		}),
		Entry("a repeated BucketKeyEnabled whose second copy radosgw never decodes", func() hostile {
			return lastAt(string(doc("", rule(element("BucketKeyEnabled", "false")+element("BucketKeyEnabled", "garbage")))),
				"<BucketKeyEnabled>")
		}),
		Entry("a prefixed element, even one bound to S3's namespace", func() hostile {
			return refusedAt(string(doc(" "+s3NS, `<s3:Rule>`+apply("")+`</s3:Rule>`)), "<s3:")
		}),
		Entry("a prefix declaration, even one naming S3's namespace", func() hostile {
			return refusedAt(string(doc(` xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"`, "")), "<Server")
		}),
		Entry("a prefixed root, which radosgw refuses too", func() hostile {
			return refusedAt(`<s3:ServerSideEncryptionConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/">`+
				`</s3:ServerSideEncryptionConfiguration>`, "<s3:")
		}),
		Entry("another root, which radosgw refuses too", func() hostile {
			return refusedAt(`<ServerSideEncryptionRule>`+apply("")+`</ServerSideEncryptionRule>`, "<ServerSideEncryptionRule>")
		}),
		Entry("a root whose name differs in case", func() hostile {
			return refusedAt(`<serverSideEncryptionConfiguration/>`, "<server")
		}),
		Entry("the root nested in itself", func() hostile {
			return lastAt(string(doc("", string(doc("", "")))), "<ServerSideEncryptionConfiguration>")
		}),
		Entry("a second root, which expat refuses too", func() hostile {
			return refusedAt(string(doc("", ""))+`<Second/>`, "<Second/>")
		}),
		Entry("an end tag after the root", func() hostile {
			return hostile{body: append(doc("", ""), "</Rule>"...), at: len(doc("", ""))}
		}),
		Entry("an end tag with no start", func() hostile {
			return refusedAt(`</ServerSideEncryptionConfiguration>`, "</Server")
		}),
		Entry("a mismatched end tag, which expat refuses too", func() hostile {
			return refusedAt(string(doc("", `<Rule></ApplyServerSideEncryptionByDefault>`)), "</Apply")
		}),
		Entry("an unclosed Rule, which the root's end tag meets", func() hostile {
			return refusedAt(`<ServerSideEncryptionConfiguration><Rule></ServerSideEncryptionConfiguration>`, "</Server")
		}),
		Entry("a foreign namespace on the root", func() hostile {
			return refusedAt(string(doc(` xmlns="urn:evil"`, algorithm("AES256"))), "<Server")
		}),
		Entry("a foreign namespace on a value", func() hostile {
			return refusedAt(string(doc(" "+s3NS, rule(apply(`<SSEAlgorithm xmlns="urn:evil">AES256</SSEAlgorithm>`)))),
				"<SSEAlgorithm")
		}),
		Entry("an attribute on a value, which radosgw ignores", func() hostile {
			return refusedAt(string(doc("", rule(apply(`<SSEAlgorithm foo="bar">AES256</SSEAlgorithm>`)))), "<SSEAlgorithm")
		}),
		Entry("text in the root", func() hostile {
			return refusedAt(string(doc("", "junk"+algorithm("AES256"))), "junk")
		}),
		Entry("text in Rule", func() hostile {
			return refusedAt(string(doc("", rule("junk"+apply(element("SSEAlgorithm", "AES256"))))), "junk")
		}),
		Entry("text in the default", func() hostile {
			return refusedAt(string(doc("", rule(apply(element("SSEAlgorithm", "AES256")+"junk")))), "junk")
		}),
		Entry("a no-break space between the elements, which is not XML whitespace", func() hostile {
			return refusedAt(string(doc("", " "+algorithm("AES256"))), " ")
		}),
		Entry("text after the root, which expat refuses too", func() hostile {
			return refusedAt(string(doc("", ""))+"junk", "junk")
		}),
		Entry("a BucketKeyEnabled far past 64 bytes, refused where its text starts", func() hostile {
			body := string(doc("", rule(element("BucketKeyEnabled", strings.Repeat("t", 1<<16)))))
			return hostile{body: []byte(body), at: strings.Index(body, "<BucketKeyEnabled>") + len("<BucketKeyEnabled>")}
		}),
		Entry("1 inside 64 bytes of whitespace, 65 in all, refused where its text starts", func() hostile {
			body := string(doc("", rule(element("BucketKeyEnabled", strings.Repeat(" ", 32)+"1"+strings.Repeat(" ", 32)))))
			return hostile{body: []byte(body), at: strings.Index(body, "<BucketKeyEnabled>") + len("<BucketKeyEnabled>")}
		}),
		Entry("an offset counted from the byte order mark", func() hostile {
			return refusedAt("\xef\xbb\xbf"+string(doc("", "<Foo/>")), "<Foo/>")
		}),
	)
	DescribeTable("refuses a document that is not well-formed as MalformedXML",
		func(body string) {
			Expect(refused([]byte(body))).To(MatchError(sse.ErrMalformedXML))
		},
		Entry("an empty body", ""),
		Entry("whitespace alone", " \n"),
		Entry("an unclosed root", `<ServerSideEncryptionConfiguration>`),
		Entry("an XML declaration not at the start", " "+`<?xml version="1.0"?>`+string(doc("", ""))),
		Entry("an XML declaration of version 2.0", `<?xml version="2.0"?>`+string(doc("", ""))),
		Entry("an encoding other than UTF-8", `<?xml version="1.0" encoding="ISO-8859-1"?>`+string(doc("", ""))),
		Entry("a duplicate xmlns attribute", string(doc(` xmlns="" xmlns=""`, ""))),
		Entry("a NUL byte in a value", string(doc("", algorithm("AES\x00256")))),
		Entry("invalid UTF-8 in a value", string(doc("", rule(apply(element("SSEAlgorithm", "aws:kms")+
			element("KMSMasterKeyID", "k\xff")))))),
	)
	DescribeTable("refuses, as Validate does, a value AWS does not take",
		func(inner string) {
			err := refused(doc(" "+s3NS, inner))
			Expect(err).NotTo(MatchError(ContainSubstring("at byte")), "a value refusal names no offset")
		},
		Entry("an algorithm outside AWS's list", algorithm("AES-256")),
		Entry("AES256 in another case", algorithm("aes256")),
		Entry("aws:kms with whitespace, which radosgw stores as written", algorithm(" aws:kms")),
		Entry("a key id with AES256", rule(apply(element("SSEAlgorithm", "AES256")+element("KMSMasterKeyID", "k")))),
		Entry("a key id with no algorithm", rule(apply(element("KMSMasterKeyID", "k")))),
	)
	DescribeTable("keeps a refusal's message short when the client's text in it is long",
		func(body []byte) {
			err := refused(body)
			Expect(len(err.Error())).To(BeNumerically("<", 512), "error %.200q", err)
		},
		Entry("a long root name", []byte("<"+strings.Repeat("a", 1<<19)+"/>")),
		Entry("a long unknown element", doc("", "<"+strings.Repeat("b", 1<<19)+"/>")),
		Entry("a long element inside a value", doc("", rule(apply("<SSEAlgorithm><"+strings.Repeat("c", 1<<19)+"/>")))),
		Entry("a long attribute name and value", doc(" "+strings.Repeat("n", 1<<18)+`="`+strings.Repeat("v", 1<<18)+`"`, "")),
		Entry("a long declaration", []byte("<!"+strings.Repeat("D", 1<<19)+">"+string(doc("", "")))),
		Entry("a long undeclared entity, which Go's decoder names", doc("", algorithm("&"+strings.Repeat("e", 1<<19)+";"))),
		Entry("a long processing instruction target, which xmltext names", doc("", "<?"+strings.Repeat("p", 1<<19)+"*?>")),
		Entry("a long algorithm", doc("", algorithm(strings.Repeat("x", 1<<19)))),
		Entry("a long BucketKeyEnabled", doc("", rule(element("BucketKeyEnabled", strings.Repeat("z", 1<<19))))),
	)
	It("refuses a body over MaxConfigXML before reading it", func() {
		pad := sse.MaxConfigXML + 1 - len(doc("", ""))
		body := doc("", strings.Repeat(" ", pad))
		Expect(body).To(HaveLen(sse.MaxConfigXML + 1))
		err := refused(body)
		Expect(err).To(MatchError(ContainSubstring("exceeds")))
	})
	It("reads a body of MaxConfigXML bytes, rgw_max_put_param_size's default", func() {
		Expect(sse.MaxConfigXML).To(Equal(1 << 20))
		inner := algorithm("AES256")
		pad := sse.MaxConfigXML - len(doc("", inner))
		body := doc("", strings.Repeat("\n", pad)+inner)
		Expect(body).To(HaveLen(sse.MaxConfigXML))
		Expect(parse(body)).To(Equal(withRule("AES256", "", false)))
	})
})

var _ = Describe("BucketEncryptionConfig.Validate", func() {
	DescribeTable("takes each algorithm AWS lists, and none",
		func(alg string) {
			Expect(withRule(alg, "", true).Validate()).To(Succeed())
		},
		Entry("none, as an empty Rule stores", ""),
		Entry("AES256", "AES256"),
		Entry("aws:kms", "aws:kms"),
		Entry("aws:kms:dsse", "aws:kms:dsse"),
		Entry("aws:fsx", "aws:fsx"),
		Entry("aws:backup", "aws:backup"),
	)
	DescribeTable("takes a KMSMasterKeyID with aws:kms or aws:kms:dsse",
		func(alg string) {
			Expect(withRule(alg, "arn:aws:kms:us-east-1:111122223333:key/k", false).Validate()).To(Succeed())
		},
		Entry("aws:kms", "aws:kms"),
		Entry("aws:kms:dsse", "aws:kms:dsse"),
	)
	It("takes a configuration without a rule", func() {
		Expect(sse.BucketEncryptionConfig{}.Validate()).To(Succeed())
	})
	DescribeTable("refuses an SSEAlgorithm outside AWS's list as MalformedXML",
		func(alg string) {
			Expect(withRule(alg, "", false).Validate()).To(MatchError(sse.ErrMalformedXML), "algorithm %q", alg)
		},
		Entry("a misspelling", "AES-256"),
		Entry("AES256 in lower case, compared as written", "aes256"),
		Entry("aws:kms in upper case", "AWS:KMS"),
		Entry("trailing whitespace, not trimmed", "AES256 "),
		Entry("a prefix of aws:kms:dsse", "aws:kms:"),
		Entry("an SSE-C name, which no configuration takes", "SSE-C"),
		Entry("a NUL after AES256", "AES256\x00"),
	)
	DescribeTable("refuses a KMSMasterKeyID with any other algorithm as MalformedXML",
		func(alg string) {
			Expect(withRule(alg, "k", false).Validate()).To(MatchError(sse.ErrMalformedXML), "algorithm %q", alg)
		},
		Entry("none", ""),
		Entry("AES256", "AES256"),
		Entry("aws:fsx", "aws:fsx"),
		Entry("aws:backup", "aws:backup"),
	)
	It("keeps the message short for a long algorithm", func() {
		err := withRule(strings.Repeat("x", 1<<20), "", false).Validate()
		Expect(err).To(MatchError(sse.ErrMalformedXML))
		Expect(len(err.Error())).To(BeNumerically("<", 512))
	})
})

var _ = Describe("BucketEncryptionConfig.MarshalS3XML", func() {
	const open = `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`
	const end = `</ServerSideEncryptionConfiguration>`
	It("writes dump_xml's document in S3's namespace, a bucket key as true", func() {
		Expect(string(withRule("aws:kms", "k", true).MarshalS3XML())).To(Equal(open +
			`<Rule><ApplyServerSideEncryptionByDefault>` +
			`<SSEAlgorithm>aws:kms</SSEAlgorithm><KMSMasterKeyID>k</KMSMasterKeyID>` +
			`</ApplyServerSideEncryptionByDefault><BucketKeyEnabled>true</BucketKeyEnabled></Rule>` + end))
	})
	It("writes SSEAlgorithm even when it is empty, and no KMSMasterKeyID or BucketKeyEnabled when they are unset", func() {
		Expect(string(withRule("", "", false).MarshalS3XML())).To(Equal(open +
			`<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm></SSEAlgorithm>` +
			`</ApplyServerSideEncryptionByDefault></Rule>` + end))
	})
	It("writes no Rule for a configuration without one, closing the root with an end tag", func() {
		Expect(string(sse.BucketEncryptionConfig{}.MarshalS3XML())).To(Equal(open + end))
	})
	It("escapes values as XMLFormatter does", func() {
		Expect(string(withRule(`a<&>"'`, "\x01\t", false).MarshalS3XML())).To(Equal(open +
			`<Rule><ApplyServerSideEncryptionByDefault>` +
			`<SSEAlgorithm>a&lt;&amp;&gt;&quot;&apos;</SSEAlgorithm><KMSMasterKeyID>&#x01;` + "\t" + `</KMSMasterKeyID>` +
			`</ApplyServerSideEncryptionByDefault></Rule>` + end))
	})
	DescribeTable("reads back as the configuration it writes",
		func(c sse.BucketEncryptionConfig) {
			Expect(parse(c.MarshalS3XML())).To(Equal(c))
		},
		Entry("no rule", sse.BucketEncryptionConfig{}),
		Entry("a rule that names nothing", withRule("", "", false)),
		Entry("AES256", withRule("AES256", "", false)),
		Entry("aws:kms with a key id and a bucket key", withRule("aws:kms", "k", true)),
		Entry("aws:kms:dsse with a key id that needs escaping", withRule("aws:kms:dsse", `<&>"' ключ`, false)),
		Entry("a bucket key with no algorithm", withRule("", "", true)),
	)
})
