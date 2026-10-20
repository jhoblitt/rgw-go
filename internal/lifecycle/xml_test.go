package lifecycle_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

const xmlnsS3 = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`

// lcDoc is a LifecycleConfiguration document holding rules.
func lcDoc(rules ...string) []byte {
	return []byte(`<LifecycleConfiguration ` + xmlnsS3 + `>` + strings.Join(rules, "") + `</LifecycleConfiguration>`)
}

// lcRule is a Rule element holding parts.
func lcRule(parts ...string) string { return "<Rule>" + strings.Join(parts, "") + "</Rule>" }

// lcDump is the body Marshal writes for rules already rendered.
func lcDump(rules ...string) string {
	return `<LifecycleConfiguration ` + xmlnsS3 + `>` + strings.Join(rules, "") + `</LifecycleConfiguration>`
}

const (
	enabled = "<Status>Enabled</Status>"
	noPfx   = "<Prefix></Prefix>"
	days1   = "<Expiration><Days>1</Days></Expiration>"
	tagged  = "<Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter>"
	dm      = "<Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration>"
)

func parse(body []byte) (lifecycle.Configuration, error) {
	return lifecycle.Parse(body, lifecycle.DefaultMaxRules)
}

func parseOne(parts ...string) lifecycle.Rule {
	GinkgoHelper()
	c, err := parse(lcDoc(lcRule(parts...)))
	Expect(err).NotTo(HaveOccurred())
	Expect(c.Rules).To(HaveLen(1))
	return c.Rules[0]
}

var _ = Describe("Parse", func() {
	DescribeTable("refuses what RGWLifecycleConfiguration_S3::decode_xml refuses, and what S3's schema refuses, as MalformedXML",
		func(body string) {
			_, err := parse([]byte(body))
			Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		},
		Entry("a body that is not well-formed", `<LifecycleConfiguration><Rule>`),
		Entry("a configuration without a Rule", string(lcDoc())),
		Entry("a Rule without Status", string(lcDoc(lcRule("<ID>a</ID>", noPfx, days1)))),
		Entry("a Status other than Enabled or Disabled", string(lcDoc(lcRule("<Status>enabled</Status>", noPfx, days1)))),
		Entry("a Filter whose ObjectSizeLessThan equals its ObjectSizeGreaterThan",
			string(lcDoc(lcRule(enabled, "<Filter><ObjectSizeGreaterThan>5</ObjectSizeGreaterThan><ObjectSizeLessThan>5</ObjectSizeLessThan></Filter>", days1)))),
		Entry("a Filter whose ObjectSizeLessThan is smaller as a number though it sorts after as text",
			string(lcDoc(lcRule(enabled, "<Filter><ObjectSizeGreaterThan>10</ObjectSizeGreaterThan><ObjectSizeLessThan>9</ObjectSizeLessThan></Filter>", days1)))),
		Entry("a Filter whose ObjectSizeLessThan equals its ObjectSizeGreaterThan as numbers, not as text",
			string(lcDoc(lcRule(enabled, "<Filter><ObjectSizeGreaterThan>05</ObjectSizeGreaterThan><ObjectSizeLessThan>5</ObjectSizeLessThan></Filter>", days1)))),
		Entry("a Filter whose sizes are inside And", string(lcDoc(lcRule(enabled,
			"<Filter><And><ObjectSizeGreaterThan>7</ObjectSizeGreaterThan><ObjectSizeLessThan>6</ObjectSizeLessThan></And></Filter>", days1)))),
		Entry("a Rule with neither Filter nor Prefix", string(lcDoc(lcRule(enabled, days1)))),
		Entry("an Expiration with none of Days, Date and ExpiredObjectDeleteMarker",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration></Expiration>")))),
		Entry("an Expiration with Days and Date",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Days>1</Days><Date>2026-01-01T00:00:00Z</Date></Expiration>")))),
		Entry("an Expiration with Days and ExpiredObjectDeleteMarker",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Days>1</Days><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration>")))),
		Entry("an Expiration Date from_iso_8601 refuses",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026/01/01</Date></Expiration>")))),
		Entry("an Expiration Date a second past midnight",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T00:00:01Z</Date></Expiration>")))),
		Entry("an Expiration Date half a second past midnight",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T00:00:00.5Z</Date></Expiration>")))),
		Entry("an Expiration Date with a time and no Z",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T00:00:00</Date></Expiration>")))),
		Entry("a bare date, which from_iso_8601 reads as its midnight and radosgw stores",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01</Date></Expiration>")))),
		Entry("a bare date with white space after it", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01\n </Date></Expiration>")))),
		Entry("a year alone", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026</Date></Expiration>")))),
		Entry("a year and month alone", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01</Date></Expiration>")))),
		Entry("a time to the hour, which from_iso_8601 reads", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T00Z</Date></Expiration>")))),
		Entry("a time to the minute, which from_iso_8601 reads", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T00:00Z</Date></Expiration>")))),
		Entry("a bare Transition Date", string(lcDoc(lcRule(enabled, noPfx,
			"<Transition><Date>2026-01-01</Date><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("an Expiration Date before 1970", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>1969-12-31T00:00:00Z</Date></Expiration>")))),
		Entry("an Expiration Date past the end of real_time, whose wrap radosgw refuses as not at midnight",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2600-01-01T00:00:00Z</Date></Expiration>")))),
		Entry("an Expiration Date crafted so that from_iso_8601's wrap lands at midnight, which radosgw stores",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2600-01-01T23:34:33.709551616Z</Date></Expiration>")))),
		Entry("a Transition Date crafted so that from_iso_8601's wrap lands at midnight", string(lcDoc(lcRule(enabled, noPfx,
			"<Transition><Date>2600-01-01T23:34:33.709551616Z</Date><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("an Expiration Date with text after white space, which check_date takes",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T00:00:00Z later</Date></Expiration>")))),
		Entry("an Expiration Date in month 13, which from_iso_8601 carries into the next year",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-13-01T00:00:00Z</Date></Expiration>")))),
		Entry("the 31st of April, which from_iso_8601 carries into May",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-04-31T00:00:00Z</Date></Expiration>")))),
		Entry("day 00", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-00T00:00:00Z</Date></Expiration>")))),
		Entry("month 00, which from_iso_8601 carries back a year", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-00-01T00:00:00Z</Date></Expiration>")))),
		Entry("the 29th of February in a common year", string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2027-02-29T00:00:00Z</Date></Expiration>")))),
		Entry("hour 24, which from_iso_8601 carries into the next midnight",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>2026-01-01T24:00:00Z</Date></Expiration>")))),
		Entry("a zone offset, which from_iso_8601 cannot read", string(lcDoc(lcRule(enabled, noPfx,
			"<Expiration><Date>2026-01-01T00:00:00+00:00</Date></Expiration>")))),
		Entry("a Transition Date that is not a calendar date", string(lcDoc(lcRule(enabled, noPfx,
			"<Transition><Date>2026-02-30T00:00:00Z</Date><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("an ExpiredObjectDeleteMarker of True, which radosgw takes as no action",
			string(lcDoc(lcRule(enabled, noPfx, "<Expiration><ExpiredObjectDeleteMarker>True</ExpiredObjectDeleteMarker></Expiration>")))),
		Entry("an ExpiredObjectDeleteMarker of yes", string(lcDoc(lcRule(enabled, noPfx,
			"<Expiration><ExpiredObjectDeleteMarker>yes</ExpiredObjectDeleteMarker></Expiration>",
			"<AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>")))),
		Entry("an empty ExpiredObjectDeleteMarker", string(lcDoc(lcRule(enabled, noPfx,
			"<Expiration><ExpiredObjectDeleteMarker></ExpiredObjectDeleteMarker></Expiration>")))),
		Entry("a NoncurrentVersionExpiration without NoncurrentDays",
			string(lcDoc(lcRule(enabled, noPfx, "<NoncurrentVersionExpiration><NewerNoncurrentVersions>1</NewerNoncurrentVersions></NoncurrentVersionExpiration>")))),
		Entry("an AbortIncompleteMultipartUpload without DaysAfterInitiation",
			string(lcDoc(lcRule(enabled, noPfx, "<AbortIncompleteMultipartUpload></AbortIncompleteMultipartUpload>")))),
		Entry("a Transition with Days and Date",
			string(lcDoc(lcRule(enabled, noPfx, "<Transition><Days>1</Days><Date>2026-01-01T00:00:00Z</Date><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("a Transition with neither Days nor Date",
			string(lcDoc(lcRule(enabled, noPfx, "<Transition><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("a Transition Date a second past midnight",
			string(lcDoc(lcRule(enabled, noPfx, "<Transition><Date>2026-01-01T00:00:01Z</Date><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("a Transition without StorageClass", string(lcDoc(lcRule(enabled, noPfx, "<Transition><Days>1</Days></Transition>")))),
		Entry("a refusal in a Transition after a good one", string(lcDoc(lcRule(enabled, noPfx,
			"<Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition><Transition><Days>2</Days></Transition>")))),
		Entry("a NoncurrentVersionTransition without NoncurrentDays",
			string(lcDoc(lcRule(enabled, noPfx, "<NoncurrentVersionTransition><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>")))),
		Entry("a NoncurrentVersionTransition without StorageClass",
			string(lcDoc(lcRule(enabled, noPfx, "<NoncurrentVersionTransition><NoncurrentDays>1</NoncurrentDays></NoncurrentVersionTransition>")))),
		Entry("a Rule with no action", string(lcDoc(lcRule("<ID>a</ID>", enabled, noPfx)))),
		Entry("two Transitions to one storage class", string(lcDoc(lcRule(enabled, noPfx,
			"<Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition><Transition><Days>2</Days><StorageClass>COLD</StorageClass></Transition>")))),
		Entry("two NoncurrentVersionTransitions to one storage class", string(lcDoc(lcRule(enabled, noPfx,
			"<NoncurrentVersionTransition><NoncurrentDays>1</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>",
			"<NoncurrentVersionTransition><NoncurrentDays>2</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>")))),
		Entry("a refusal in a later Rule", string(lcDoc(lcRule("<ID>a</ID>", enabled, noPfx, days1), lcRule("<ID>b</ID>", noPfx, days1)))),
		Entry("a root other than LifecycleConfiguration, which radosgw reads as the empty configuration",
			`<BucketLifecycleConfiguration><Rule><Status>Enabled</Status></Rule></BucketLifecycleConfiguration>`),
		Entry("a prefixed root, which expat without namespaces reads as another name",
			`<s3:LifecycleConfiguration xmlns:s3="http://s3.amazonaws.com/doc/2006-03-01/"><s3:Rule/></s3:LifecycleConfiguration>`),
	)

	DescribeTable("refuses a number S3's schema refuses as MalformedXML, where radosgw stores the text",
		func(parts ...string) {
			_, err := parse(lcDoc(lcRule(parts...)))
			Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
			Expect(err.Error()).To(ContainSubstring("is not an integer"))
		},
		Entry("Expiration Days in words, which atoi reads as 0", enabled, noPfx, "<Expiration><Days>thirty</Days></Expiration>"),
		Entry("Expiration Days with text after the digits, which atoi reads as 5", enabled, noPfx, "<Expiration><Days>5 days</Days></Expiration>"),
		Entry("an empty Expiration Days", enabled, noPfx, "<Expiration><Days></Days></Expiration>"),
		Entry("Expiration Days past xs:int, which atoi reads as -1", enabled, noPfx, "<Expiration><Days>99999999999999999999</Days></Expiration>"),
		Entry("Expiration Days one past xs:int", enabled, noPfx, "<Expiration><Days>2147483648</Days></Expiration>"),
		Entry("Expiration Days with two signs", enabled, noPfx, "<Expiration><Days>+-1</Days></Expiration>"),
		Entry("Expiration Days with whitespace inside", enabled, noPfx, "<Expiration><Days>1 0</Days></Expiration>"),
		Entry("an empty NoncurrentDays", enabled, noPfx, "<NoncurrentVersionExpiration><NoncurrentDays></NoncurrentDays></NoncurrentVersionExpiration>"),
		Entry("NewerNoncurrentVersions that is not a number", enabled, noPfx,
			"<NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays><NewerNoncurrentVersions>some</NewerNoncurrentVersions></NoncurrentVersionExpiration>"),
		Entry("a fractional DaysAfterInitiation", enabled, noPfx,
			"<AbortIncompleteMultipartUpload><DaysAfterInitiation>1.5</DaysAfterInitiation></AbortIncompleteMultipartUpload>"),
		Entry("Transition Days in hex", enabled, noPfx, "<Transition><Days>0x10</Days><StorageClass>COLD</StorageClass></Transition>"),
		Entry("NoncurrentVersionTransition NoncurrentDays with a unit", enabled, noPfx,
			"<NoncurrentVersionTransition><NoncurrentDays>5d</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>"),
		Entry("an ObjectSizeGreaterThan in exponent form", enabled, "<Filter><ObjectSizeGreaterThan>1e3</ObjectSizeGreaterThan></Filter>", days1),
		Entry("an ObjectSizeLessThan past xs:long, inside And", enabled,
			"<Filter><And><Prefix>p/</Prefix><ObjectSizeLessThan>9223372036854775808</ObjectSizeLessThan></And></Filter>", days1),
	)

	It("takes S3's integer forms and stores each in decimal", func() {
		r := parseOne("<ID>r</ID>", enabled,
			"<Filter><And><ObjectSizeGreaterThan> +0010 </ObjectSizeGreaterThan><ObjectSizeLessThan>9223372036854775807</ObjectSizeLessThan></And></Filter>",
			"<Expiration><Days>\n 007\t</Days></Expiration>",
			"<NoncurrentVersionExpiration><NoncurrentDays>2147483647</NoncurrentDays><NewerNoncurrentVersions>+03</NewerNoncurrentVersions></NoncurrentVersionExpiration>",
			"<AbortIncompleteMultipartUpload><DaysAfterInitiation>01</DaysAfterInitiation></AbortIncompleteMultipartUpload>",
			"<Transition><Days> 40</Days><StorageClass>GLACIER</StorageClass></Transition>",
			"<NoncurrentVersionTransition><NoncurrentDays>-0</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>")
		Expect(r.Filter.SizeGT).To(Equal("10"))
		Expect(r.Filter.SizeLT).To(Equal("9223372036854775807"))
		Expect(r.Expiration).To(Equal(lifecycle.Expiration{Days: "7"}))
		Expect(r.NoncurExpiration).To(Equal(lifecycle.Expiration{Days: "2147483647", NewerNoncurrent: "3"}))
		Expect(r.MPExpiration).To(Equal(lifecycle.Expiration{Days: "1"}))
		Expect(r.Transitions).To(Equal(map[string]lifecycle.Transition{"GLACIER": {Days: "40", StorageClass: "GLACIER"}}))
		Expect(r.NoncurTransitions).To(Equal(map[string]lifecycle.Transition{"COLD": {Days: "0", StorageClass: "COLD"}}))
	})

	DescribeTable("compares a Filter's sizes as numbers, where radosgw compares their text",
		func(gt, lt string) {
			r := parseOne(enabled, "<Filter><ObjectSizeGreaterThan>"+gt+"</ObjectSizeGreaterThan><ObjectSizeLessThan>"+lt+"</ObjectSizeLessThan></Filter>", days1)
			Expect(r.Filter.SizeGT).To(Equal(gt))
			Expect(r.Filter.SizeLT).To(Equal(lt))
		},
		Entry("more than 9 and less than 10, which radosgw refuses", "9", "10"),
		Entry("more than 99 and less than 1000", "99", "1000"),
	)

	DescribeTable("refuses a negative object size as InvalidArgument, after the grammar's refusals",
		func(filter string) {
			_, err := parse(lcDoc(lcRule("<ID>r</ID>", enabled, filter, days1)))
			Expect(err).To(MatchError(lifecycle.ErrInvalidArgument))
			Expect(err.Error()).To(ContainSubstring("is negative"))
		},
		Entry("ObjectSizeGreaterThan", "<Filter><ObjectSizeGreaterThan>-1</ObjectSizeGreaterThan></Filter>"),
		Entry("ObjectSizeLessThan, which radosgw's worker reads as 2^64 - 1", "<Filter><ObjectSizeLessThan>-1</ObjectSizeLessThan></Filter>"),
		Entry("both, in order as numbers", "<Filter><And><ObjectSizeGreaterThan>-10</ObjectSizeGreaterThan><ObjectSizeLessThan>-1</ObjectSizeLessThan></And></Filter>"),
	)

	It("takes an object size of 0", func() {
		r := parseOne(enabled, "<Filter><ObjectSizeGreaterThan>0</ObjectSizeGreaterThan></Filter>", days1)
		Expect(r.Filter.SizeGT).To(Equal("0"))
	})

	It("refuses sizes out of order as MalformedXML before a negative one as InvalidArgument", func() {
		_, err := parse(lcDoc(lcRule(enabled, "<Filter><ObjectSizeGreaterThan>5</ObjectSizeGreaterThan><ObjectSizeLessThan>-1</ObjectSizeLessThan></Filter>", days1)))
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
	})

	DescribeTable("refuses a NewerNoncurrentVersions outside 1 to 100 as InvalidArgument",
		func(newer string) {
			_, err := parse(lcDoc(lcRule("<ID>r</ID>", enabled, noPfx,
				"<NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays><NewerNoncurrentVersions>"+newer+"</NewerNoncurrentVersions></NoncurrentVersionExpiration>")))
			Expect(err).To(MatchError(lifecycle.ErrInvalidArgument))
			Expect(err.Error()).To(ContainSubstring("not 1 to 100"))
		},
		Entry("0", "0"),
		Entry("101", "101"),
		Entry("a negative count, which radosgw stores as one that never acts", "-5"),
	)

	DescribeTable("takes a NewerNoncurrentVersions from 1 to 100, without a Filter as radosgw takes it",
		func(newer string) {
			r := parseOne(enabled, noPfx,
				"<NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays><NewerNoncurrentVersions>"+newer+"</NewerNoncurrentVersions></NoncurrentVersionExpiration>")
			Expect(r.NoncurExpiration.NewerNoncurrent).To(Equal(newer))
		},
		Entry("1", "1"),
		Entry("100", "100"),
	)

	DescribeTable("refuses in decode_xml's order, each refusal naming the first it meets",
		func(body []byte, reason string) {
			_, err := parse(body)
			Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
			Expect(err.Error()).To(ContainSubstring(reason))
		},
		Entry("Status before the Filter's sizes", lcDoc(lcRule(
			"<Filter><ObjectSizeGreaterThan>5</ObjectSizeGreaterThan><ObjectSizeLessThan>5</ObjectSizeLessThan></Filter>", days1)), "missing Status"),
		Entry("the Filter before the Expiration", lcDoc(lcRule(enabled,
			"<Filter><ObjectSizeGreaterThan>5</ObjectSizeGreaterThan><ObjectSizeLessThan>5</ObjectSizeLessThan></Filter>",
			"<Expiration></Expiration>")), "ObjectSizeLessThan"),
		Entry("the Expiration before the Transitions", lcDoc(lcRule(enabled, noPfx,
			"<Transition><StorageClass>COLD</StorageClass></Transition>", "<Expiration></Expiration>")), "ExpiredObjectDeleteMarker"),
		Entry("every NoncurrentVersionTransition before a repeated storage class", lcDoc(lcRule(enabled, noPfx,
			"<Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition><Transition><Days>2</Days><StorageClass>COLD</StorageClass></Transition>",
			"<NoncurrentVersionTransition><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>")), "NoncurrentDays"),
		Entry("a repeated Transition storage class before a repeated noncurrent one", lcDoc(lcRule(enabled, noPfx,
			"<NoncurrentVersionTransition><NoncurrentDays>1</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>",
			"<NoncurrentVersionTransition><NoncurrentDays>2</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>",
			"<Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition><Transition><Days>2</Days><StorageClass>COLD</StorageClass></Transition>")),
			"a second Transition"),
	)

	It("reads every rule before it counts them", func() {
		_, err := lifecycle.Parse(lcDoc(lcRule(enabled, noPfx, days1), lcRule(enabled, noPfx, days1), lcRule(noPfx, days1)), 1)
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		Expect(err.Error()).To(ContainSubstring("missing Status"))
	})

	It("refuses more rules than maxRules, after reading every rule", func() {
		two := lcDoc(lcRule(enabled, noPfx, days1), lcRule(enabled, noPfx, days1))
		_, err := lifecycle.Parse(two, 1)
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		Expect(err.Error()).To(ContainSubstring("2 rules"))
		c, err := lifecycle.Parse(two, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules).To(HaveLen(2))
	})

	It("takes rgw_lc_max_rules' default, 1000 rules, and refuses 1001", func() {
		Expect(lifecycle.DefaultMaxRules).To(Equal(1000))
		rules := make([]string, 1001)
		for i := range rules {
			rules[i] = lcRule(fmt.Sprintf("<ID>r%04d</ID>", i), enabled, noPfx, days1)
		}
		c, err := parse(lcDoc(rules[:1000]...))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules).To(HaveLen(1000))
		_, err = parse(lcDoc(rules...))
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
	})

	It("counts a grammar refusal before an invalid rule and the rule count before both validations", func() {
		long := "<ID>" + strings.Repeat("x", 256) + "</ID>"
		_, err := parse(lcDoc(lcRule(long, enabled, noPfx, days1), lcRule("<ID>b</ID>", noPfx, days1)))
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		_, err = lifecycle.Parse(lcDoc(lcRule(long, enabled, noPfx, days1), lcRule("<ID>b</ID>", enabled, noPfx, days1)), 1)
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
	})

	It("reads a legacy Prefix outside a Filter", func() {
		Expect(parseOne("<ID>r</ID>", "<Prefix>logs/</Prefix>", enabled, "<Expiration><Days>30</Days></Expiration>")).To(Equal(lifecycle.Rule{
			ID: "r", Prefix: "logs/", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "30"},
		}))
	})

	It("reads a Filter's conditions inside And and ignores those beside it", func() {
		r := parseOne("<ID>r</ID>", enabled, "<Prefix>rule/</Prefix>", "<Filter><Prefix>out/</Prefix><ObjectSizeLessThan>1</ObjectSizeLessThan><And>",
			"<Prefix>in/</Prefix><Tag><Key>b</Key><Value>2</Value></Tag><Tag><Key>a</Key><Value>1</Value></Tag><Tag></Tag>",
			"<ArchiveZone/><ObjectSizeGreaterThan>100</ObjectSizeGreaterThan><ObjectSizeLessThan>200</ObjectSizeLessThan>",
			"</And></Filter>", days1)
		Expect(r.Prefix).To(BeEmpty(), "a rule with a Filter does not read its Prefix")
		Expect(r.Filter).To(Equal(lifecycle.Filter{
			Prefix: "in/",
			Tags:   tags.Set{Tags: []tags.Tag{{Key: "", Value: ""}, {Key: "a", Value: "1"}, {Key: "b", Value: "2"}}},
			Flags:  lifecycle.FlagArchiveZone,
			SizeGT: "100",
			SizeLT: "200",
		}))
	})

	It("takes a Filter's tags without the limits of a tag set", func() {
		var tagXML strings.Builder
		for i := range 11 {
			fmt.Fprintf(&tagXML, "<Tag><Key>%s</Key><Value>v</Value></Tag>", strings.Repeat("k", 129+i))
		}
		r := parseOne(enabled, "<Filter><And>"+tagXML.String()+"</And></Filter>", days1)
		Expect(r.Filter.Tags.Len()).To(Equal(11))
	})

	It("reads an empty Filter", func() {
		Expect(parseOne("<ID>r</ID>", enabled, "<Filter></Filter>", days1).Filter).To(Equal(lifecycle.Filter{}))
	})

	DescribeTable("takes a Date written as a full date-time at midnight UTC on a calendar date, and keeps it as written",
		func(date string) {
			Expect(parseOne(enabled, noPfx, "<Expiration><Date>"+date+"</Date></Expiration>").Expiration).To(Equal(lifecycle.Expiration{Date: date}))
			r := parseOne(enabled, noPfx, "<Transition><Date>"+date+"</Date><StorageClass>COLD</StorageClass></Transition>")
			Expect(r.Transitions).To(Equal(map[string]lifecycle.Transition{"COLD": {Date: date, StorageClass: "COLD"}}))
		},
		Entry("a time at midnight", "2026-01-01T00:00:00Z"),
		Entry("botocore's form for '2017-09-27'", "2017-09-27T00:00:00Z"),
		Entry("milliseconds, the AWS SDKs' form", "2026-01-01T00:00:00.000Z"),
		Entry("nine fractional zeros", "2026-01-01T00:00:00.000000000Z"),
		Entry("the 29th of February in a leap year", "2028-02-29T00:00:00Z"),
		Entry("XML white space after it, which from_iso_8601 ends the time at", "2026-01-01T00:00:00Z\n "),
		Entry("the last midnight real_time holds", "2554-07-21T00:00:00Z"),
	)

	It("refuses a date botocore sends for an all-digit string, which it reads as epoch seconds", func() {
		// botocore 1.43.90 sends '20200101' as 20200101 seconds after the
		// epoch, so test_lifecycle_set_invalid_date puts this on the wire.
		_, err := parse(lcDoc(lcRule(enabled, noPfx, "<Expiration><Date>1970-08-22T19:08:21Z</Date></Expiration>")))
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
	})

	DescribeTable("reads ExpiredObjectDeleteMarker as an xs:boolean",
		func(flag string, want bool) {
			r := parseOne(enabled, noPfx, "<Expiration><ExpiredObjectDeleteMarker>"+flag+"</ExpiredObjectDeleteMarker></Expiration>",
				"<AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>")
			Expect(r.DMExpiration).To(Equal(want))
			Expect(r.Expiration).To(Equal(lifecycle.Expiration{}))
		},
		Entry("true", "true", true),
		Entry("1, which radosgw drops", "1", true),
		Entry("true with white space, which radosgw drops", " true\n", true),
		Entry("false", "false", false),
		Entry("0", "0", false),
	)

	It("reads every action", func() {
		Expect(parseOne("<ID>r</ID>", "<Status>Disabled</Status>", noPfx,
			"<Expiration><Days>30</Days></Expiration>",
			"<NoncurrentVersionExpiration><NoncurrentDays>7</NoncurrentDays><NewerNoncurrentVersions>3</NewerNoncurrentVersions></NoncurrentVersionExpiration>",
			"<AbortIncompleteMultipartUpload><DaysAfterInitiation>2</DaysAfterInitiation></AbortIncompleteMultipartUpload>",
			"<Transition><Days>40</Days><StorageClass>GLACIER</StorageClass></Transition>",
			"<Transition><Days>35</Days><StorageClass></StorageClass></Transition>",
			"<NoncurrentVersionTransition><NoncurrentDays>5</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>",
		)).To(Equal(lifecycle.Rule{
			ID:               "r",
			Status:           "Disabled",
			Expiration:       lifecycle.Expiration{Days: "30"},
			NoncurExpiration: lifecycle.Expiration{Days: "7", NewerNoncurrent: "3"},
			MPExpiration:     lifecycle.Expiration{Days: "2"},
			Transitions: map[string]lifecycle.Transition{
				"GLACIER": {Days: "40", StorageClass: "GLACIER"},
				"":        {Days: "35"},
			},
			NoncurTransitions: map[string]lifecycle.Transition{"COLD": {Days: "5", StorageClass: "COLD"}},
		}))
	})

	It("reads the first of a repeated element, ignores what the grammar does not name and keeps text untrimmed", func() {
		Expect(parseOne("<ID> r </ID><ID>s</ID>", enabled, "<Unknown><Days>x</Days></Unknown>", "<Prefix> logs/ </Prefix>", "<Prefix>second/</Prefix>",
			"<Expiration><Days>30</Days></Expiration>", "<Expiration><Days>x</Days></Expiration>",
		)).To(Equal(lifecycle.Rule{ID: " r ", Prefix: " logs/ ", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "30"}}))
	})

	It("names a rule without an ID with 48 random lowercase alphanumerics, as gen_rand_alphanumeric_lower does", func() {
		c, err := parse(lcDoc(lcRule(enabled, noPfx, days1), lcRule("<ID></ID>", enabled, noPfx, days1)))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules).To(HaveLen(2))
		Expect(c.Rules[0].ID).To(MatchRegexp(`^[0-9a-z]{48}$`))
		Expect(c.Rules[1].ID).To(MatchRegexp(`^[0-9a-z]{48}$`))
		Expect(c.Rules[0].ID).NotTo(Equal(c.Rules[1].ID))
		Expect(c.Rules[0].ID < c.Rules[1].ID).To(BeTrue(), "the rules come out in ID order")
	})

	It("draws generated IDs from every character of the alphabet", func() {
		seen := map[rune]bool{}
		rules := make([]string, 40)
		for i := range rules {
			rules[i] = lcRule(enabled, noPfx, days1)
		}
		c, err := parse(lcDoc(rules...))
		Expect(err).NotTo(HaveOccurred())
		for _, r := range c.Rules {
			for _, ch := range r.ID {
				seen[ch] = true
			}
		}
		Expect(seen).To(HaveLen(36))
	})

	It("returns the rules in ID order", func() {
		c, err := parse(lcDoc(lcRule("<ID>b</ID>", enabled, noPfx, days1), lcRule("<ID>a</ID>", enabled, noPfx, days1)))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules).To(HaveLen(2))
		Expect([]string{c.Rules[0].ID, c.Rules[1].ID}).To(Equal([]string{"a", "b"}))
	})

	It("cuts the client's text it repeats in a refusal to 512 bytes", func() {
		status := strings.Repeat("é", 400)
		_, err := parse(lcDoc(lcRule("<Status>"+status+"</Status>", noPfx, days1)))
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML))
		Expect(err.Error()).To(ContainSubstring(`"` + strings.Repeat("é", 256) + `"...`))
		Expect(err.Error()).NotTo(ContainSubstring(strings.Repeat("é", 257)))
	})

	It("cuts a refusal's client text by bytes, even inside a character", func() {
		status := "x" + strings.Repeat("é", 400)
		_, err := parse(lcDoc(lcRule("<Status>"+status+"</Status>", noPfx, days1)))
		Expect(err.Error()).To(ContainSubstring(`"x` + strings.Repeat("é", 255) + `\xc3"...`))
	})

	It("repeats a short text whole, unmarked", func() {
		_, err := parse(lcDoc(lcRule("<Status>on</Status>", noPfx, days1)))
		Expect(err.Error()).To(HaveSuffix(`"on"`))
	})
})

var _ = Describe("Parse's validation, rebuild and check_and_add_rule", func() {
	DescribeTable("refuses a rule LCRule::valid refuses as InvalidArgument",
		func(parts ...string) {
			_, err := parse(lcDoc(lcRule(parts...)))
			Expect(err).To(MatchError(lifecycle.ErrInvalidArgument))
		},
		Entry("an ID over 255 bytes", "<ID>"+strings.Repeat("x", 256)+"</ID>", enabled, noPfx, days1),
		Entry("Expiration Days of 0", enabled, noPfx, "<Expiration><Days>0</Days></Expiration>"),
		Entry("Expiration Days below 0", enabled, noPfx, "<Expiration><Days>-1</Days></Expiration>"),
		Entry("Expiration Days of 0 written with a sign", enabled, noPfx, "<Expiration><Days>-0</Days></Expiration>"),
		Entry("NoncurrentDays of 0", enabled, noPfx, "<NoncurrentVersionExpiration><NoncurrentDays>0</NoncurrentDays></NoncurrentVersionExpiration>"),
		Entry("DaysAfterInitiation of 0", enabled, noPfx, "<AbortIncompleteMultipartUpload><DaysAfterInitiation>0</DaysAfterInitiation></AbortIncompleteMultipartUpload>"),
		Entry("ExpiredObjectDeleteMarker false, which leaves no action", enabled, noPfx,
			"<Expiration><ExpiredObjectDeleteMarker>false</ExpiredObjectDeleteMarker></Expiration>"),
		Entry("a Transition below 0 days", enabled, noPfx, "<Transition><Days>-1</Days><StorageClass>COLD</StorageClass></Transition>"),
		Entry("a NoncurrentVersionTransition below 0 days", enabled, noPfx,
			"<NoncurrentVersionTransition><NoncurrentDays>-1</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>"),
		Entry("Expiration Days with a Transition Date", enabled, noPfx, days1,
			"<Transition><Date>2026-01-01T00:00:00Z</Date><StorageClass>COLD</StorageClass></Transition>"),
		Entry("an Expiration Date with Transition Days", enabled, noPfx, "<Expiration><Date>2026-01-01T00:00:00Z</Date></Expiration>",
			"<Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition>"),
		Entry("Transition Days with a Transition Date", enabled, noPfx,
			"<Transition><Days>1</Days><StorageClass>COLD</StorageClass></Transition>",
			"<Transition><Date>2026-01-01T00:00:00Z</Date><StorageClass>WARM</StorageClass></Transition>"),
	)

	DescribeTable("takes what LCRule::valid takes",
		func(parts ...string) {
			_, err := parse(lcDoc(lcRule(parts...)))
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("an ID of 255 bytes", "<ID>"+strings.Repeat("x", 255)+"</ID>", enabled, noPfx, days1),
		Entry("a Transition at 0 days", enabled, noPfx, "<Transition><Days>0</Days><StorageClass>COLD</StorageClass></Transition>"),
		Entry("Expiration Days with Transition Days", enabled, noPfx, days1,
			"<Transition><Days>2</Days><StorageClass>COLD</StorageClass></Transition>"),
		Entry("noncurrent transitions, which are not held to one of days and dates", enabled, noPfx, "<Expiration><Date>2026-01-01T00:00:00Z</Date></Expiration>",
			"<NoncurrentVersionTransition><NoncurrentDays>1</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>"),
		Entry("a tag filter with Expiration Days", enabled, "<Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter>", days1),
		Entry("a prefix filter with AbortIncompleteMultipartUpload", enabled, "<Filter><Prefix>p/</Prefix></Filter>",
			"<AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>"),
	)

	It("refuses a repeated ID as InvalidArgument", func() {
		_, err := parse(lcDoc(lcRule("<ID>x</ID>", enabled, noPfx, days1), lcRule("<ID>x</ID>", enabled, noPfx, days1)))
		Expect(err).To(MatchError(lifecycle.ErrInvalidArgument))
	})

	DescribeTable("refuses a tag filter beside an action S3 does not filter by tag as InvalidRequest",
		func(action string) {
			_, err := parse(lcDoc(lcRule(enabled, "<Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter>", action)))
			Expect(err).To(MatchError(lifecycle.ErrInvalidRequest))
		},
		Entry("ExpiredObjectDeleteMarker", "<Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration>"),
		Entry("AbortIncompleteMultipartUpload", "<AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>"),
	)

	DescribeTable("checks the rules in ID order, not document order",
		func(want error, rules ...string) {
			_, err := parse(lcDoc(rules...))
			Expect(err).To(MatchError(want))
		},
		Entry("an InvalidRequest under a first ID wins over an InvalidArgument written before it", lifecycle.ErrInvalidRequest,
			lcRule("<ID>b</ID>", enabled, noPfx, "<Expiration><Days>0</Days></Expiration>"), lcRule("<ID>a</ID>", enabled, tagged, dm)),
		Entry("an InvalidArgument under a first ID wins over an InvalidRequest written before it", lifecycle.ErrInvalidArgument,
			lcRule("<ID>b</ID>", enabled, tagged, dm), lcRule("<ID>a</ID>", enabled, noPfx, "<Expiration><Days>0</Days></Expiration>")),
		Entry("a repeated ID before the second rule's tag check", lifecycle.ErrInvalidArgument,
			lcRule("<ID>x</ID>", enabled, noPfx, days1), lcRule("<ID>x</ID>", enabled, tagged, dm)),
	)
})

var _ = Describe("Marshal", func() {
	DescribeTable("writes GetBucketLifecycleConfiguration's body as dump_xml does, byte for byte",
		func(r lifecycle.Rule, want string) {
			Expect(string(lifecycle.Marshal(lifecycle.Configuration{Rules: []lifecycle.Rule{r}}))).To(Equal(lcDump(want)))
		},
		Entry("a legacy prefix and expiration days",
			lifecycle.Rule{ID: "r", Prefix: "logs/", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "30"}},
			"<Rule><ID>r</ID><Prefix>logs/</Prefix><Status>Enabled</Status><Expiration><Days>30</Days></Expiration></Rule>"),
		Entry("an empty filter and an empty prefix, written as Prefix",
			lifecycle.Rule{ID: "r", Status: "Enabled", Expiration: lifecycle.Expiration{Date: "2026-01-01"}},
			"<Rule><ID>r</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><Date>2026-01-01</Date></Expiration></Rule>"),
		Entry("a filter of one prefix, without And",
			lifecycle.Rule{ID: "r", Prefix: "ignored/", Status: "Enabled", Filter: lifecycle.Filter{Prefix: "p/"}, DMExpiration: true},
			"<Rule><ID>r</ID><Filter><Prefix>p/</Prefix></Filter><Status>Enabled</Status><Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration></Rule>"),
		Entry("a filter of one tag, without And",
			lifecycle.Rule{ID: "r", Status: "Enabled", Filter: lifecycle.Filter{Tags: tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}}, Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>r</ID><Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
		Entry("a filter of two tags, in And and multimap order",
			lifecycle.Rule{ID: "r", Status: "Enabled", Filter: lifecycle.Filter{Tags: tags.Set{Tags: []tags.Tag{{Key: "b", Value: "2"}, {Key: "a", Value: "1"}}}}, Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>r</ID><Filter><And><Tag><Key>a</Key><Value>1</Value></Tag><Tag><Key>b</Key><Value>2</Value></Tag></And></Filter><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
		Entry("every condition, in And",
			lifecycle.Rule{ID: "r", Status: "Enabled", Filter: lifecycle.Filter{
				Prefix: "p/", Tags: tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}, Flags: lifecycle.FlagArchiveZone, SizeGT: "1", SizeLT: "9",
			}, Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>r</ID><Filter><And><Prefix>p/</Prefix><Tag><Key>k</Key><Value>v</Value></Tag><ArchiveZone></ArchiveZone>"+
				"<ObjectSizeGreaterThan>1</ObjectSizeGreaterThan><ObjectSizeLessThan>9</ObjectSizeLessThan></And></Filter>"+
				"<Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
		Entry("both sizes, which has_multi_condition counts once, without And",
			lifecycle.Rule{ID: "r", Status: "Enabled", Filter: lifecycle.Filter{SizeGT: "1", SizeLT: "9"}, Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>r</ID><Filter><ObjectSizeGreaterThan>1</ObjectSizeGreaterThan><ObjectSizeLessThan>9</ObjectSizeLessThan></Filter>"+
				"<Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
		Entry("ArchiveZone alone",
			lifecycle.Rule{ID: "r", Status: "Enabled", Filter: lifecycle.Filter{Flags: lifecycle.FlagArchiveZone}, Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>r</ID><Filter><ArchiveZone></ArchiveZone></Filter><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
		Entry("a flag radosgw names no flag for, an empty Filter",
			lifecycle.Rule{ID: "r", Status: "Enabled", Filter: lifecycle.Filter{Flags: 2}, Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>r</ID><Filter></Filter><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
		Entry("every action, in dump_xml's order, transitions in key order", lifecycle.Rule{
			ID: "r", Status: "Disabled",
			Expiration:       lifecycle.Expiration{Days: "30"},
			NoncurExpiration: lifecycle.Expiration{Days: "7", NewerNoncurrent: "3"},
			MPExpiration:     lifecycle.Expiration{Days: "2"},
			Transitions: map[string]lifecycle.Transition{
				"GLACIER": {Days: "40", StorageClass: "GLACIER"}, "COLD": {Date: "2026-01-01", StorageClass: "COLD"},
			},
			NoncurTransitions: map[string]lifecycle.Transition{"WARM": {Days: "5", StorageClass: "WARM"}, "COLD": {Days: "6", StorageClass: "COLD"}},
		}, "<Rule><ID>r</ID><Prefix></Prefix><Status>Disabled</Status><Expiration><Days>30</Days></Expiration>"+
			"<NoncurrentVersionExpiration><NewerNoncurrentVersions>3</NewerNoncurrentVersions><NoncurrentDays>7</NoncurrentDays></NoncurrentVersionExpiration>"+
			"<AbortIncompleteMultipartUpload><DaysAfterInitiation>2</DaysAfterInitiation></AbortIncompleteMultipartUpload>"+
			"<Transition><Date>2026-01-01</Date><StorageClass>COLD</StorageClass></Transition>"+
			"<Transition><Days>40</Days><StorageClass>GLACIER</StorageClass></Transition>"+
			"<NoncurrentVersionTransition><NoncurrentDays>6</NoncurrentDays><StorageClass>COLD</StorageClass></NoncurrentVersionTransition>"+
			"<NoncurrentVersionTransition><NoncurrentDays>5</NoncurrentDays><StorageClass>WARM</StorageClass></NoncurrentVersionTransition></Rule>"),
		Entry("the delete marker over expiration days, as only a stored rule holds both",
			lifecycle.Rule{ID: "r", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "5"}, DMExpiration: true},
			"<Rule><ID>r</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration></Rule>"),
		Entry("a noncurrent expiration without a newer count, and with a date but no days, as only a stored rule holds",
			lifecycle.Rule{ID: "r", Status: "Enabled", NoncurExpiration: lifecycle.Expiration{Date: "2026-01-01"}},
			"<Rule><ID>r</ID><Prefix></Prefix><Status>Enabled</Status><NoncurrentVersionExpiration><NoncurrentDays></NoncurrentDays></NoncurrentVersionExpiration></Rule>"),
		Entry("text escaped as XMLFormatter escapes it",
			lifecycle.Rule{ID: "<&>\"'\x01", Prefix: "a&b", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}},
			"<Rule><ID>&lt;&amp;&gt;&quot;&apos;&#x01;</ID><Prefix>a&amp;b</Prefix><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>"),
	)

	It("writes the rules in ID order and an empty configuration as an empty element pair", func() {
		a := lifecycle.Rule{ID: "a", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}}
		b := lifecycle.Rule{ID: "b", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "2"}}
		Expect(string(lifecycle.Marshal(lifecycle.Configuration{Rules: []lifecycle.Rule{b, a}}))).To(Equal(lcDump(
			"<Rule><ID>a</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><Days>1</Days></Expiration></Rule>",
			"<Rule><ID>b</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><Days>2</Days></Expiration></Rule>")))
		Expect(string(lifecycle.Marshal(lifecycle.Configuration{}))).To(Equal(lcDump()))
	})

	It("writes a configuration radosgw stored back as it stored it, numbers S3 would refuse included", func() {
		stored := lifecycle.Configuration{Rules: []lifecycle.Rule{{
			ID: "r", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "5x"},
			Filter: lifecycle.Filter{SizeGT: "10", SizeLT: "9"},
		}}}
		c, err := lifecycle.Decode(encode(stored, lifecycle.Configuration.Encode, denc.Squid))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(lifecycle.Marshal(c))).To(Equal(lcDump("<Rule><ID>r</ID><Filter><ObjectSizeGreaterThan>10</ObjectSizeGreaterThan>" +
			"<ObjectSizeLessThan>9</ObjectSizeLessThan></Filter><Status>Enabled</Status><Expiration><Days>5x</Days></Expiration></Rule>")))
	})

	It("writes back what Parse read, through the stored form", func() {
		c, err := parse(lcDoc(lcRule("<ID>r</ID>", enabled, "<Filter><And><Prefix>p/</Prefix><Tag><Key>k</Key><Value>v</Value></Tag></And></Filter>", days1,
			"<NoncurrentVersionExpiration><NoncurrentDays>7</NoncurrentDays></NoncurrentVersionExpiration>")))
		Expect(err).NotTo(HaveOccurred())
		stored, err := lifecycle.Decode(encode(c, lifecycle.Configuration.Encode, denc.Squid))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(lifecycle.Marshal(stored))).To(Equal(lcDump("<Rule><ID>r</ID><Filter><And><Prefix>p/</Prefix><Tag><Key>k</Key><Value>v</Value></Tag></And></Filter>" +
			"<Status>Enabled</Status><Expiration><Days>1</Days></Expiration><NoncurrentVersionExpiration><NoncurrentDays>7</NoncurrentDays></NoncurrentVersionExpiration></Rule>")))
	})
})

// Rook's bucket/lifecycle integration test sets an OBC's bucketLifecycle to
// these documents and compares GetBucketLifecycleConfiguration's rules with
// them (rook tests/integration/object/bucket/lifecycle/lifecycle.go,
// lifecycle1 and lifecycle2). The bodies are what aws-sdk-go-v2's
// PutBucketLifecycleConfiguration writes for them, each rule's members in
// the SDK's alphabetical order.
const (
	rookAbort = "<Rule><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload>" +
		"<ID>AbortIncompleteMultipartUploads</ID><Prefix></Prefix><Status>Enabled</Status></Rule>"
	rookExpire    = "<Rule><Expiration><Days>30</Days></Expiration><ID>ExpireAfter30Days</ID><Prefix></Prefix><Status>Enabled</Status></Rule>"
	rookAbortDump = "<Rule><ID>AbortIncompleteMultipartUploads</ID><Prefix></Prefix><Status>Enabled</Status>" +
		"<AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>"
	rookExpireDump = "<Rule><ID>ExpireAfter30Days</ID><Prefix></Prefix><Status>Enabled</Status><Expiration><Days>30</Days></Expiration></Rule>"
)

var _ = Describe("Rook's bucket lifecycle documents", func() {
	DescribeTable("are parsed and written back with an empty Prefix, no Filter, and the rules in ID order",
		func(body []byte, want string) {
			c, err := parse(body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(lifecycle.Marshal(c))).To(Equal(want))
		},
		Entry("lifecycle1, the multipart abort", lcDoc(rookAbort), lcDump(rookAbortDump)),
		Entry("lifecycle2, the abort and a 30-day expiration", lcDoc(rookExpire, rookAbort), lcDump(rookAbortDump, rookExpireDump)),
	)
})
