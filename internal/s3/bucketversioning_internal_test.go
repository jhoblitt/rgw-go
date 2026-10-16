package s3

import (
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("parseVersioningConfiguration", func() {
	const (
		head = `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`
		tail = `</VersioningConfiguration>`
	)
	doc := func(inner string) string { return head + inner + tail }

	DescribeTable("reads a document radosgw reads",
		func(body string, status op.VersioningStatus, mfaDelete *bool) {
			gotStatus, gotMFA, err := parseVersioningConfiguration([]byte(body))
			Expect(err).NotTo(HaveOccurred(), "%q", body)
			Expect(gotStatus).To(Equal(status))
			Expect(gotMFA).To(Equal(mfaDelete))
		},
		Entry("Enabled", doc(`<Status>Enabled</Status>`), op.VersioningEnabled, nil),
		Entry("Suspended", doc(`<Status>Suspended</Status>`), op.VersioningSuspended, nil),
		Entry("MfaDelete Enabled", doc(`<Status>Enabled</Status><MfaDelete>Enabled</MfaDelete>`), op.VersioningEnabled, new(true)),
		Entry("MfaDelete Disabled before Status", doc(`<MfaDelete>Disabled</MfaDelete><Status>Suspended</Status>`), op.VersioningSuspended, new(false)),
		Entry("no namespace, as radosgw reads names as written",
			`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, op.VersioningEnabled, nil),
		Entry("the XML declaration and whitespace between elements",
			"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+head+"\n  <Status>Enabled</Status>\n"+tail+"\n", op.VersioningEnabled, nil),
		Entry("a UTF-8 byte order mark before the declaration, which expat skips",
			"\xef\xbb\xbf<?xml version=\"1.0\"?>"+doc(`<Status>Enabled</Status>`), op.VersioningEnabled, nil),
		Entry("a character reference, which expat decodes", doc(`<Status>&#69;nabled</Status>`), op.VersioningEnabled, nil),
		Entry("CDATA", doc(`<Status><![CDATA[Suspended]]></Status>`), op.VersioningSuspended, nil),
		Entry("a comment inside the value, whose text expat joins", doc(`<Status>Ena<!-- c -->bled</Status>`), op.VersioningEnabled, nil),
		Entry("an element the schema does not name, which radosgw ignores", doc(`<Status>Enabled</Status><Foo>x</Foo>`), op.VersioningEnabled, nil),
		Entry("attributes, which radosgw ignores", doc(`<Status id="1">Enabled</Status>`), op.VersioningEnabled, nil),
		Entry("a prefix declared for S3's namespace", `<VersioningConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`,
			op.VersioningEnabled, nil),
		Entry("S3's namespace declared again on a child", doc(`<Status xmlns="http://s3.amazonaws.com/doc/2006-03-01/">Enabled</Status>`), op.VersioningEnabled, nil),
	)

	DescribeTable("refuses with 400 MalformedXML",
		func(body string) {
			_, _, err := parseVersioningConfiguration([]byte(body))
			Expect(err).To(MatchError(op.ErrMalformedXML), "%q", body)
			Expect(op.AsError(err).Message).To(BeEmpty(), "radosgw's MalformedXML carries no message")
		},
		// radosgw answers each of these 400 InvalidArgument
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2179-L2269 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2274-L2364 at v20.2.4).
		Entry("an empty body", ``),
		Entry("text", `Enabled`),
		Entry("an unclosed root", head+`<Status>Enabled</Status>`),
		Entry("another root", `<Versioning><Status>Enabled</Status></Versioning>`),
		Entry("a prefixed root, which radosgw reads as another name",
			`<s3:VersioningConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"><s3:Status>Enabled</s3:Status></s3:VersioningConfiguration>`),
		Entry("no Status", head+tail),
		Entry("MfaDelete without Status", doc(`<MfaDelete>Disabled</MfaDelete>`)),
		Entry("an empty Status", doc(`<Status></Status>`)),
		Entry("Status in lower case", doc(`<Status>enabled</Status>`)),
		Entry("Status in upper case", doc(`<Status>SUSPENDED</Status>`)),
		Entry("Status after a space", doc(`<Status> Enabled</Status>`)),
		Entry("Status before a line break", doc("<Status>Enabled\n</Status>")),
		Entry("Status after a tab", doc("<Status>\tSuspended</Status>")),
		Entry("Status as a character reference to a space and the value", doc(`<Status>&#32;Enabled</Status>`)),
		Entry("Status Disabled, MfaDelete's value", doc(`<Status>Disabled</Status>`)),
		Entry("MfaDelete in lower case", doc(`<Status>Enabled</Status><MfaDelete>enabled</MfaDelete>`)),
		Entry("MfaDelete true", doc(`<Status>Enabled</Status><MfaDelete>true</MfaDelete>`)),
		Entry("an empty MfaDelete", doc(`<Status>Enabled</Status><MfaDelete></MfaDelete>`)),
		Entry("MfaDelete after a space", doc(`<Status>Enabled</Status><MfaDelete> Disabled</MfaDelete>`)),
		Entry("an undeclared entity", doc(`<Status>&e;</Status>`)),
		// radosgw reads each of these.
		Entry("Status twice, the same", doc(`<Status>Enabled</Status><Status>Enabled</Status>`)),
		Entry("Status twice, which radosgw reads as the first", doc(`<Status>Suspended</Status><Status>Enabled</Status>`)),
		Entry("MfaDelete twice", doc(`<Status>Enabled</Status><MfaDelete>Disabled</MfaDelete><MfaDelete>Enabled</MfaDelete>`)),
		Entry("a DOCTYPE", `<!DOCTYPE VersioningConfiguration>`+doc(`<Status>Enabled</Status>`)),
		Entry("an entity the DOCTYPE declares", `<!DOCTYPE VersioningConfiguration [<!ENTITY e "Enabled">]>`+doc(`<Status>&e;</Status>`)),
		Entry("another default namespace", `<VersioningConfiguration xmlns="http://example.com/"><Status>Enabled</Status></VersioningConfiguration>`),
		Entry("an empty default namespace", `<VersioningConfiguration xmlns=""><Status>Enabled</Status></VersioningConfiguration>`),
		Entry("another namespace on a child", doc(`<Status xmlns="http://example.com/">Enabled</Status>`)),
		Entry("a prefix declared for another namespace", `<VersioningConfiguration xmlns:x="http://example.com/"><Status>Enabled</Status></VersioningConfiguration>`),
		Entry("a prefixed child beside Status, which radosgw ignores",
			`<VersioningConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status><s3:MfaDelete>Enabled</s3:MfaDelete></VersioningConfiguration>`),
		Entry("an element inside Status, whose text radosgw reads as the value", doc(`<Status><b/>Enabled</Status>`)),
		Entry("an element nested in one the schema does not name", doc(`<Status>Enabled</Status><Foo><Bar/></Foo>`)),
	)

	Describe("bounded work", func() {
		// Each prefix is followed by two megabytes that are not well-formed: a
		// parser that reads past the refusal fails on them instead.
		DescribeTable("stops at the refusal, before reading what follows it",
			func(prefix, reason string) {
				_, _, err := parseVersioningConfiguration([]byte(prefix + strings.Repeat("<a", 1<<20)))
				Expect(err).To(MatchError(op.ErrMalformedXML))
				Expect(err).To(MatchError(ContainSubstring(reason)))
			},
			Entry("a DOCTYPE", `<!DOCTYPE VersioningConfiguration [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]>`+head,
				"declaration"),
			Entry("an element nested too deep", head+`<Status><b>`, "nested"),
			Entry("Status twice", head+`<Status>Enabled</Status><Status>`, "repeated"),
			Entry("MfaDelete twice", head+`<MfaDelete>Enabled</MfaDelete><MfaDelete>`, "repeated"),
			Entry("another namespace", head+`<Status xmlns="http://example.com/">`, "namespace"),
			Entry("a prefixed element", head+`<x:Status>`, "prefix"),
			Entry("an end tag without a start tag, which would take the depth below zero", head+tail+`</a>`,
				"an end tag without a start tag"),
		)
		// allocsRefusing is the allocations parseVersioningConfiguration makes
		// refusing body, which it must refuse with MalformedXML.
		allocsRefusing := func(body []byte) float64 {
			GinkgoHelper()
			var err error
			allocs := testing.AllocsPerRun(1, func() { _, _, err = parseVersioningConfiguration(body) })
			Expect(err).To(MatchError(op.ErrMalformedXML))
			return allocs
		}
		It("refuses a body nested a hundred thousand deep in a few allocations", func() {
			deep := []byte(head + strings.Repeat("<a>", 1<<17) + strings.Repeat("</a>", 1<<17) + tail)
			Expect(allocsRefusing(deep)).To(BeNumerically("<", 100), "a tree of the document takes an element a level")
		})
		It("refuses a body of thirty thousand Status elements in a few allocations", func() {
			many := []byte(head + strings.Repeat(`<Status>Enabled</Status>`, 1<<15) + tail)
			Expect(allocsRefusing(many)).To(BeNumerically("<", 100), "a tree of the document takes an element a Status")
		})
	})
})
