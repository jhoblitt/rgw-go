package objectlock_test

import (
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
)

const xmlnsS3 = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

// config is an ObjectLockConfiguration body holding inner.
func config(inner string) string {
	return `<ObjectLockConfiguration ` + xmlnsS3 + `>` + inner + `</ObjectLockConfiguration>`
}

const enabled = `<ObjectLockEnabled>Enabled</ObjectLockEnabled>`

// rule is an enabled configuration whose DefaultRetention holds inner.
func rule(inner string) string {
	return config(enabled + `<Rule><DefaultRetention>` + inner + `</DefaultRetention></Rule>`)
}

func retention(inner string) string { return `<Retention ` + xmlnsS3 + `>` + inner + `</Retention>` }

func legalHold(inner string) string { return `<LegalHold ` + xmlnsS3 + `>` + inner + `</LegalHold>` }

// expectMalformed asserts err is the package's malformed-XML refusal, with
// no message for the client and detail as radosgw's RGWXMLDecoder::err text.
func expectMalformed(err error, detail string) {
	GinkgoHelper()
	Expect(err).To(MatchError(objectlock.ErrMalformedXML))
	var e *objectlock.Error
	Expect(errors.As(err, &e)).To(BeTrue(), "%v is an *objectlock.Error", err)
	Expect(e.Message).To(BeEmpty())
	Expect(e.Detail).To(Equal(detail))
}

func governanceDays(n int32) *meta.ObjectLockRule {
	return &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "GOVERNANCE", Days: n}}
}

var _ = Describe("ParseConfiguration", func() {
	DescribeTable("reads what RGWObjectLock::decode_xml reads",
		func(body string, want meta.ObjectLock) {
			got, err := objectlock.ParseConfiguration([]byte(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("enabled with no rule", config(enabled), meta.ObjectLock{Enabled: true}),
		Entry("no namespace", `<ObjectLockConfiguration>`+enabled+`</ObjectLockConfiguration>`, meta.ObjectLock{Enabled: true}),
		Entry("a GOVERNANCE rule in days", rule(`<Mode>GOVERNANCE</Mode><Days>30</Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(30)}),
		Entry("a COMPLIANCE rule in years", rule(`<Mode>COMPLIANCE</Mode><Years>7</Years>`),
			meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "COMPLIANCE", Years: 7}}}),
		Entry("days present as 0, judged by presence", rule(`<Mode>GOVERNANCE</Mode><Days>0</Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(0)}),
		Entry("negative days, which strtol reads", rule(`<Mode>GOVERNANCE</Mode><Days>-3</Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(-3)}),
		Entry("a plus sign and leading zeros", rule(`<Mode>GOVERNANCE</Mode><Days>+007</Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(7)}),
		Entry("white space before and after the number", rule("<Mode>GOVERNANCE</Mode><Days>\t 7\n </Days>"), meta.ObjectLock{Enabled: true, Rule: governanceDays(7)}),
		Entry("int's largest", rule(`<Mode>GOVERNANCE</Mode><Days>2147483647</Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(2147483647)}),
		Entry("int's smallest", rule(`<Mode>GOVERNANCE</Mode><Days>-2147483648</Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(-2147483648)}),
		Entry("the first of repeated elements", config(enabled+`<ObjectLockEnabled>no</ObjectLockEnabled>`+
			`<Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Mode>x</Mode><Days>1</Days><Days>x</Days></DefaultRetention></Rule><Rule/>`),
			meta.ObjectLock{Enabled: true, Rule: governanceDays(1)}),
		Entry("elements and attributes it does not know", config(`<Token a="b"/>`+enabled+`<Rule x="y"><Other/><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule>`),
			meta.ObjectLock{Enabled: true, Rule: governanceDays(1)}),
		Entry("an element's own text, without its children's", config(`<ObjectLockEnabled>Enab<x>y</x>led</ObjectLockEnabled>`), meta.ObjectLock{Enabled: true}),
		Entry("references and CDATA decoded", rule(`<Mode>GOVERNANC&#69;</Mode><Days><![CDATA[30]]></Days>`), meta.ObjectLock{Enabled: true, Rule: governanceDays(30)}),
	)
	DescribeTable("refuses, as MalformedXML, what radosgw refuses, in its order",
		func(body, detail string) {
			_, err := objectlock.ParseConfiguration([]byte(body))
			expectMalformed(err, detail)
		},
		Entry("another root", `<Retention>`+enabled+`</Retention>`, "missing mandatory field ObjectLockConfiguration"),
		Entry("a prefixed root, another name with namespaces off",
			`<s3:ObjectLockConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/">`+enabled+`</s3:ObjectLockConfiguration>`,
			"missing mandatory field ObjectLockConfiguration"),
		Entry("no ObjectLockEnabled", config(`<Rule/>`), "ObjectLockConfiguration: missing mandatory field ObjectLockEnabled"),
		Entry("ObjectLockEnabled other than Enabled", config(`<ObjectLockEnabled>Disabled</ObjectLockEnabled>`), "ObjectLockConfiguration: invalid ObjectLockEnabled value"),
		Entry("ObjectLockEnabled in another case", config(`<ObjectLockEnabled>enabled</ObjectLockEnabled>`), "ObjectLockConfiguration: invalid ObjectLockEnabled value"),
		Entry("ObjectLockEnabled with white space", config(`<ObjectLockEnabled> Enabled</ObjectLockEnabled>`), "ObjectLockConfiguration: invalid ObjectLockEnabled value"),
		Entry("a first ObjectLockEnabled other than Enabled", config(`<ObjectLockEnabled>x</ObjectLockEnabled>`+enabled), "ObjectLockConfiguration: invalid ObjectLockEnabled value"),
		Entry("a Rule without DefaultRetention", config(enabled+`<Rule/>`), "ObjectLockConfiguration: Rule: missing mandatory field DefaultRetention"),
		Entry("a DefaultRetention without Mode", rule(`<Days>1</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: missing mandatory field Mode"),
		Entry("a Mode other than GOVERNANCE or COMPLIANCE", rule(`<Mode>governance</Mode><Days>1</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: bad Mode in lock rule"),
		Entry("a bad Mode before a bad Days", rule(`<Mode>x</Mode><Days>x</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: bad Mode in lock rule"),
		Entry("Days that are no number", rule(`<Mode>GOVERNANCE</Mode><Days>ten</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("empty Days", rule(`<Mode>GOVERNANCE</Mode><Days></Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("a sign alone", rule(`<Mode>GOVERNANCE</Mode><Days>-</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("bytes after the number", rule(`<Mode>GOVERNANCE</Mode><Days>5d</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("a decimal fraction", rule(`<Mode>GOVERNANCE</Mode><Days>1.5</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("hexadecimal, read in base 10", rule(`<Mode>GOVERNANCE</Mode><Days>0x10</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("one past int's largest", rule(`<Mode>GOVERNANCE</Mode><Days>2147483648</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: integer out of range"),
		Entry("one below int's smallest", rule(`<Mode>GOVERNANCE</Mode><Days>-2147483649</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: integer out of range"),
		Entry("past long's range, ERANGE", rule(`<Mode>GOVERNANCE</Mode><Days>99999999999999999999</Days>`), "ObjectLockConfiguration: Rule: DefaultRetention: Days: failed to parse number"),
		Entry("bad Years after good Days", rule(`<Mode>GOVERNANCE</Mode><Days>1</Days><Years>x</Years>`), "ObjectLockConfiguration: Rule: DefaultRetention: Years: failed to parse number"),
		Entry("both Days and Years", rule(`<Mode>GOVERNANCE</Mode><Days>1</Days><Years>1</Years>`), "ObjectLockConfiguration: Rule: DefaultRetention: either Days or Years must be specified, but not both"),
		Entry("both, each 0", rule(`<Mode>GOVERNANCE</Mode><Days>0</Days><Years>0</Years>`), "ObjectLockConfiguration: Rule: DefaultRetention: either Days or Years must be specified, but not both"),
		Entry("neither Days nor Years", rule(`<Mode>COMPLIANCE</Mode>`), "ObjectLockConfiguration: Rule: DefaultRetention: either Days or Years must be specified, but not both"),
	)
	It("refuses a body expat cannot parse as MalformedXML", func() {
		_, err := objectlock.ParseConfiguration([]byte(config(enabled) + `<extra/>`))
		Expect(err).To(MatchError(objectlock.ErrMalformedXML))
		_, err = objectlock.ParseConfiguration(nil)
		Expect(err).To(MatchError(objectlock.ErrMalformedXML))
	})
})

var _ = Describe("decode_xml_obj(int)", func() {
	DescribeTable("reads strtol's number over the text, then nothing but C white space, within int",
		func(s string, want int32, what string) {
			got, gotWhat := objectlock.DecodeInt(s)
			Expect(gotWhat).To(Equal(what), "%q", s)
			Expect(got).To(Equal(want), "%q", s)
		},
		Entry("C white space XML text cannot hold, around the number", "\v\f 12\v\f\r", int32(12), ""),
		Entry("a NUL, where the C string ends", "12\x00junk", int32(12), ""),
		Entry("a NUL before the digits", "\x0012", int32(0), "failed to parse number"),
		Entry("long's largest, which ERANGE does not flag", "9223372036854775807", int32(0), "integer out of range"),
		Entry("one past long's largest, ERANGE", "9223372036854775808", int32(0), "failed to parse number"),
		Entry("one below long's smallest, ERANGE", "-9223372036854775809", int32(0), "failed to parse number"),
		Entry("white space alone", " \t", int32(0), "failed to parse number"),
	)
})

var _ = Describe("ParseRetention", func() {
	DescribeTable("reads what RGWObjectRetention::decode_xml reads",
		func(body string, want meta.ObjectRetention) {
			got, err := objectlock.ParseRetention([]byte(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("GOVERNANCE, the date to the nanosecond", retention(`<Mode>GOVERNANCE</Mode><RetainUntilDate>2030-01-02T03:04:05.123456789Z</RetainUntilDate>`),
			meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: time.Date(2030, 1, 2, 3, 4, 5, 123456789, time.UTC)}),
		Entry("COMPLIANCE, the date as the SDKs write it", retention(`<Mode>COMPLIANCE</Mode><RetainUntilDate>2030-01-02T03:04:05.000Z</RetainUntilDate>`),
			meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}),
		Entry("a date alone, ended by white space", retention(`<RetainUntilDate>2030-01-02 noon</RetainUntilDate><Mode>COMPLIANCE</Mode>`),
			meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)}),
		Entry("real_time's last instant", retention(`<Mode>GOVERNANCE</Mode><RetainUntilDate>2554-07-21T23:34:33.709551615Z</RetainUntilDate>`),
			meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: meta.MaxRealTime}),
	)
	DescribeTable("refuses, as MalformedXML, what radosgw refuses, in its order",
		func(body, detail string) {
			_, err := objectlock.ParseRetention([]byte(body))
			expectMalformed(err, detail)
		},
		Entry("another root", legalHold(`<Mode>GOVERNANCE</Mode>`), "missing mandatory field Retention"),
		Entry("no Mode", retention(`<RetainUntilDate>2030-01-02T03:04:05Z</RetainUntilDate>`), "Retention: missing mandatory field Mode"),
		Entry("a Mode other than GOVERNANCE or COMPLIANCE", retention(`<Mode>Governance</Mode><RetainUntilDate>2030-01-02T03:04:05Z</RetainUntilDate>`), "Retention: bad Mode in retention"),
		Entry("a bad Mode before a missing date", retention(`<Mode>x</Mode>`), "Retention: bad Mode in retention"),
		Entry("no RetainUntilDate", retention(`<Mode>GOVERNANCE</Mode>`), "Retention: missing mandatory field RetainUntilDate"),
		Entry("a date from_iso_8601 refuses", retention(`<Mode>GOVERNANCE</Mode><RetainUntilDate>Jan 2, 2030</RetainUntilDate>`), "Retention: invalid RetainUntilDate value"),
		Entry("white space before the date", retention(`<Mode>GOVERNANCE</Mode><RetainUntilDate> 2030-01-02T03:04:05Z</RetainUntilDate>`), "Retention: invalid RetainUntilDate value"),
		Entry("a date past real_time's range, which radosgw wraps", retention(`<Mode>GOVERNANCE</Mode><RetainUntilDate>2554-07-21T23:34:34Z</RetainUntilDate>`), "Retention: invalid RetainUntilDate value"),
		Entry("a date before the epoch, which radosgw wraps to 2554", retention(`<Mode>COMPLIANCE</Mode><RetainUntilDate>1970-01-00</RetainUntilDate>`), "Retention: invalid RetainUntilDate value"),
	)
})

var _ = Describe("ParseLegalHold", func() {
	DescribeTable("reads ON and OFF",
		func(status string) {
			got, err := objectlock.ParseLegalHold([]byte(legalHold(`<Status>` + status + `</Status>`)))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(meta.ObjectLegalHold{Status: status}))
		},
		Entry("ON", "ON"),
		Entry("OFF", "OFF"),
	)
	DescribeTable("refuses, as MalformedXML, what radosgw refuses, in its order",
		func(body, detail string) {
			_, err := objectlock.ParseLegalHold([]byte(body))
			expectMalformed(err, detail)
		},
		Entry("another root", retention(`<Status>ON</Status>`), "missing mandatory field LegalHold"),
		Entry("no Status", legalHold(``), "LegalHold: missing mandatory field Status"),
		Entry("a Status other than ON or OFF", legalHold(`<Status>on</Status>`), "LegalHold: bad status in legal hold"),
		Entry("an empty Status", legalHold(`<Status></Status>`), "LegalHold: bad status in legal hold"),
	)
})

var _ = Describe("the response documents", func() {
	DescribeTable("MarshalConfiguration writes RGWObjectLock::dump_xml under an ObjectLockConfiguration without xmlns",
		func(l meta.ObjectLock, want string) {
			Expect(string(objectlock.MarshalConfiguration(l))).To(Equal(want))
		},
		Entry("enabled with no rule", meta.ObjectLock{Enabled: true},
			`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`),
		Entry("a rule in days", meta.ObjectLock{Enabled: true, Rule: governanceDays(30)},
			`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>30</Days></DefaultRetention></Rule></ObjectLockConfiguration>`),
		Entry("a rule in years", meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "COMPLIANCE", Years: 7}}},
			`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Years>7</Years></DefaultRetention></Rule></ObjectLockConfiguration>`),
		Entry("Years whenever Days is not positive", meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "GOVERNANCE", Days: -1, Years: -2}}},
			`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Years>-2</Years></DefaultRetention></Rule></ObjectLockConfiguration>`),
		Entry("disabled, without ObjectLockEnabled", meta.ObjectLock{Rule: governanceDays(1)},
			`<ObjectLockConfiguration><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`),
		Entry("disabled with no rule, an empty section", meta.ObjectLock{}, `<ObjectLockConfiguration></ObjectLockConfiguration>`),
		Entry("a stored mode escaped as XMLFormatter escapes it", meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: `<&'">`, Days: 1}}},
			`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>&lt;&amp;&apos;&quot;&gt;</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`),
	)
	DescribeTable("MarshalRetention writes RGWObjectRetention::dump_xml, the date in nine fractional digits",
		func(r meta.ObjectRetention, want string) {
			Expect(string(objectlock.MarshalRetention(r))).To(Equal(want))
		},
		Entry("a date to the nanosecond", meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)},
			`<Retention><Mode>GOVERNANCE</Mode><RetainUntilDate>2030-01-02T03:04:05.000000006Z</RetainUntilDate></Retention>`),
		Entry("the unset date, the epoch", meta.ObjectRetention{},
			`<Retention><Mode></Mode><RetainUntilDate>1970-01-01T00:00:00.000000000Z</RetainUntilDate></Retention>`),
	)
	It("MarshalLegalHold writes RGWObjectLegalHold::dump_xml", func() {
		Expect(string(objectlock.MarshalLegalHold(meta.ObjectLegalHold{Status: "ON"}))).To(Equal(`<LegalHold><Status>ON</Status></LegalHold>`))
	})
	It("writes what the parsers read back", func() {
		l := meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "COMPLIANCE", Years: 3}}}
		Expect(objectlock.ParseConfiguration(objectlock.MarshalConfiguration(l))).To(Equal(l))
		r := meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: time.Date(2031, 5, 6, 7, 8, 9, 10, time.UTC)}
		Expect(objectlock.ParseRetention(objectlock.MarshalRetention(r))).To(Equal(r))
		h := meta.ObjectLegalHold{Status: "OFF"}
		Expect(objectlock.ParseLegalHold(objectlock.MarshalLegalHold(h))).To(Equal(h))
	})
})
