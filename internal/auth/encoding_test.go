package auth

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("radosgw encodings", func() {
	DescribeTable("aws4URIEncode is aws4_uri_encode",
		func(in string, slash bool, want string) {
			Expect(aws4URIEncode(in, slash)).To(Equal(want), "%q", in)
		},
		Entry("unreserved kept", "AZaz09-_.~", true, "AZaz09-_.~"),
		Entry("slash encoded when asked", "/a/b", true, "%2Fa%2Fb"),
		Entry("slash kept for the uri", "/a/b", false, "/a/b"),
		Entry("space, plus, utf-8 are %XX uppercase", "a b+ü", true, "a%20b%2B%C3%BC"),
	)

	DescribeTable("aws4Recode decodes then encodes",
		func(in string, slash bool, want string) {
			Expect(aws4Recode(in, slash)).To(Equal(want), "%q", in)
		},
		Entry("double-encoded key", "a%2Fb", true, "a%2Fb"),
		Entry("encoded slash becomes a slash in the uri", "/k%2Fv", false, "/k/v"),
		Entry("lowercase hex normalised", "%2f", true, "%2F"),
		Entry("a plus is no space outside a query, so it is encoded", "a+b", true, "a%2Bb"),
		Entry("a plus after a literal ? is a space", "/p?a+b", false, "/p%3Fa%20b"),
		Entry("a truncated escape ends the decode", "/a%4", false, "/a"),
		Entry("a bad hex digit empties the decode", "/a%zz", false, ""),
		Entry("a byte above 0x7f in an escape empties the decode", "/a%\xc3\xbc", false, ""),
	)

	It("trims as rgw_trim_whitespace and collapses as boost::trim_all", func() {
		Expect(trimSpace(" \t a b  \r\n")).To(Equal("a b"))
		Expect(trimSpace("\v\fx\v")).To(Equal("x"), "vertical tab and form feed are isspace")
		Expect(collapseSpace("  a \t  b\r\n c ")).To(Equal("a b\rc"),
			"each inner run becomes its first character (head_finder(1)), not a space")
		Expect(collapseSpace("a\t b")).To(Equal("a\tb"))
	})

	It("checks the content-md5 charset", func() {
		Expect(isBase64Charset("rL0Y20zC+Fzt72VPzMSk2A==")).To(BeTrue(), "a base64 MD5")
		Expect(isBase64Charset("rL0Y20zC+Fzt72VPzMSk2A=!")).To(BeFalse(), "'!' is outside the charset")
		Expect(isBase64Charset("rL0Y 20zC\t+Fzt")).To(BeTrue(), "isspace is part of is_base64_for_content_md5")
	})

	DescribeTable("atoll",
		func(in string, want int64) { Expect(atoll(in)).To(Equal(want), "%q", in) },
		Entry("digits", "604800", int64(604800)),
		Entry("trailing junk", "300abc", int64(300)),
		Entry("leading space and sign", "  -5", int64(-5)),
		Entry("no digits", "abc", int64(0)),
		Entry("empty", "", int64(0)),
		Entry("overflow saturates as strtoll does", "99999999999999999999", int64(math.MaxInt64)),
		Entry("negative overflow saturates", "-99999999999999999999", int64(math.MinInt64)),
	)

	DescribeTable("parseISO8601Basic is parse_iso8601 without the extended format",
		func(in string, ok bool, want time.Time) {
			t, got := parseISO8601Basic(in)
			Expect(got).To(Equal(ok), "%q", in)
			if ok {
				Expect(t).To(BeTemporally("==", want), "%q", in)
			}
		},
		Entry("with Z", "20150830T123600Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("without Z", "20150830T123600", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a fraction is checked, then dropped as radosgw's auth paths drop it", "20150830T123600.123Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("trailing space tolerated", "20150830T123600Z ", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("extended format rejected", "2015-08-30T12:36:00Z", false, time.Time{}),
		Entry("rfc 1123 rejected", "Sun, 30 Aug 2015 12:36:00 GMT", false, time.Time{}),
		Entry("fraction without Z rejected", "20150830T123600.123", false, time.Time{}),
		Entry("an empty fraction, which stringtoul takes as 0", "20150830T123600.Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a fraction stringtoul refuses", "20150830T123600.1aZ", false, time.Time{}),
		Entry("a fraction of -1, which strtoul wraps to ULONG_MAX, refused", "20150830T123600.-1Z", false, time.Time{}),
		Entry("a fraction of -2, which strtoul wraps below ULONG_MAX, accepted", "20150830T123600.-2Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a fraction of ULONG_MAX refused", "20150830T123600.18446744073709551615Z", false, time.Time{}),
		Entry("a fraction past ULONG_MAX, which strtoul saturates to it, refused", "20150830T123600.18446744073709551616Z", false, time.Time{}),
		Entry("leading space, which strptime skips before a number", " 20150830T123600Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a one-digit month, which strptime's width rule reads", "2015830T123600Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("month 13 rejected", "20151330T123600Z", false, time.Time{}),
		Entry("the 31st of a 30-day month rolls over as internal_timegm does", "20150431T000000Z", true, time.Date(2015, 5, 1, 0, 0, 0, 0, time.UTC)),
		Entry("second 60 rolls into the next minute", "20150830T123660Z", true, time.Date(2015, 8, 30, 12, 37, 0, 0, time.UTC)),
		Entry("year 0 starts a day late, as days_from_0's truncating division places it", "00000101T000000Z", true, time.Unix(-62167132800, 0)),
		Entry("a NUL ends the string as it does in C", "20150830T123600Z\x00junk", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
	)

	DescribeTable("parseRFC2616 accepts the four HTTP date forms",
		func(in string, ok bool, want time.Time) {
			t, got := parseRFC2616(in)
			Expect(got).To(Equal(ok), "%q", in)
			if ok {
				Expect(t).To(BeTemporally("==", want), "%q", in)
			}
		},
		Entry("rfc 1123", "Sun, 30 Aug 2015 12:36:00 GMT", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("rfc 1123 numeric zone", "Sun, 30 Aug 2015 14:36:00 +0200", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("rfc 850", "Sunday, 30-Aug-15 12:36:00 GMT", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("asctime", "Sun Aug 30 12:36:00 2015", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("iso rejected", "20150830T123600Z", false, time.Time{}),
		Entry("UTC in place of GMT", "Sun, 30 Aug 2015 12:36:00 UTC", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("text after GMT is ignored, offset included", "Sun, 30 Aug 2015 12:36:00 GMT+0200", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("another zone name rejected", "Sun, 30 Aug 2015 12:36:00 PST", false, time.Time{}),
		Entry("a lower-case gmt rejected", "Sun, 30 Aug 2015 12:36:00 gmt", false, time.Time{}),
		Entry("a one-digit day", "Sun, 6 Sep 2015 12:36:00 GMT", true, time.Date(2015, 9, 6, 12, 36, 0, 0, time.UTC)),
		Entry("full day and month names in any case", "SUNDAY, 30 august 2015 12:36:00 GMT", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("asctime's space-padded day", "Sun Sep  6 12:36:00 2015", true, time.Date(2015, 9, 6, 12, 36, 0, 0, time.UTC)),
		Entry("a numeric zone with a colon", "Sun, 30 Aug 2015 14:36:00 +02:00", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("Z as the numeric zone", "Sun, 30 Aug 2015 12:36:00 Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a three-digit zone rejected", "Sun, 30 Aug 2015 12:36:00 +020", false, time.Time{}),
		Entry("zone minutes of 60 rejected", "Sun, 30 Aug 2015 12:36:00 +0260", false, time.Time{}),
		Entry("text after a numeric zone rejected", "Sun, 30 Aug 2015 14:36:00 +0200 x", false, time.Time{}),
		Entry("an rfc 850 year of 69 is 1969", "Saturday, 30-Aug-69 12:36:00 GMT", true, time.Date(1969, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("an rfc 850 year of 68 is 2068", "Thursday, 30-Aug-68 12:36:00 GMT", true, time.Date(2068, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("glibc's weekday match runs on past a matched abbreviation", "SunMon, 30 Aug 2015 12:36:00 GMT", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a NUL ends the string as it does in C", "Sun, 30 Aug 2015 12:36:00 GMT\x00x", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("a NUL before the zone leaves none", "Sun, 30 Aug 2015 12:36:00 \x00GMT", false, time.Time{}),
	)
})
