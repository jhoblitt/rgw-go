package op_test

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// The expectations below were produced by radosgw's parse_range,
// parse_time and rgw_string_unquote, copied from v19.2.6 (the same at
// v20.2.4) and run against glibc 2.42.

var _ = Describe("ParseRange", func() {
	DescribeTable("is RGWGetObj::parse_range",
		func(value string, ofs, end int64, partial bool) {
			o, e, p, err := op.ParseRange(value)
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{o, e, p}).To(Equal([]any{ofs, end, partial}))
		},
		Entry("a closed range", "bytes=0-10", int64(0), int64(10), true),
		Entry("an open range", "bytes=4-", int64(4), int64(-1), true),
		Entry("a suffix range", "bytes=-7", int64(-7), int64(-1), true),
		Entry("a unit after whitespace is compared from the value's first byte, so it is no range",
			"  bytes = 0-1", int64(0), int64(-1), false),
		Entry("a unit that is not bytes is ignored", "items=0-1", int64(0), int64(-1), false),
		Entry("a unit longer than bytes is ignored", "bytesx=0-1", int64(0), int64(-1), false),
		Entry("whitespace other than spaces before the unit", "\t\nbytes\v=7-8", int64(0), int64(-1), false),
		Entry("no header value", "", int64(0), int64(-1), false),
		Entry("bytes= anywhere in the value", "x-bytes=2-3", int64(2), int64(3), true),
		Entry("an empty unit compares zero bytes and passes", "=0-1", int64(0), int64(1), true),
		Entry("a prefix of bytes passes", "byt=0-1", int64(0), int64(1), true),
		Entry("the unit in any case, spaces before the equals sign", "Bytes = 3-4", int64(3), int64(4), true),
		Entry("a second range is dropped by atoll", "bytes=0-1,5-6", int64(0), int64(1), true),
		Entry("atoll reads a non-number as 0", "bytes=abc-5", int64(0), int64(5), true),
		Entry("atoll skips whitespace and takes a plus sign", "bytes= +1 - 2", int64(1), int64(2), true),
		Entry("a suffix of nothing negates end's initial -1", "bytes=-", int64(1), int64(-1), true),
		Entry("a suffix reads the rest with atoll", "bytes=-5-10", int64(-5), int64(-1), true),
		Entry("atoll saturates", "bytes=99999999999999999999-", int64(math.MaxInt64), int64(-1), true),
		Entry("a saturated suffix", "bytes=-99999999999999999999", int64(-math.MaxInt64), int64(-1), true),
		Entry("a NUL ends the value", "bytes=4-\x009", int64(4), int64(-1), true),
	)
	DescribeTable("refuses with InvalidRange",
		func(value string) {
			_, _, _, err := op.ParseRange(value)
			Expect(err).To(MatchError(op.ErrInvalidRange))
		},
		Entry("end before start", "bytes=10-5"),
		Entry("an end atoll reads as 0, before the start", "bytes=1-x"),
		Entry("no dash", "bytes=5"),
		Entry("nothing after the unit", "bytes="),
		Entry("negative end", "bytes=0--1"),
	)
})

var _ = Describe("RangeToOfs", func() {
	DescribeTable("is RGWRados::Object::Read::range_to_ofs",
		func(size uint64, ofs, end int64, wantFirst, wantLast uint64) {
			first, last, err := op.RangeToOfs(size, ofs, end)
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{first, last}).To(Equal([]uint64{wantFirst, wantLast}))
		},
		Entry("open range", uint64(11), int64(4), int64(-1), uint64(4), uint64(10)),
		Entry("suffix", uint64(11), int64(-7), int64(-1), uint64(4), uint64(10)),
		Entry("suffix longer than the object", uint64(11), int64(-70), int64(-1), uint64(0), uint64(10)),
		Entry("end clamped", uint64(11), int64(0), int64(50), uint64(0), uint64(10)),
		Entry("the last byte alone", uint64(11), int64(10), int64(-1), uint64(10), uint64(10)),
		Entry("an empty object has no last byte", uint64(0), int64(0), int64(-1), uint64(0), uint64(0)),
		Entry("a suffix of an empty object", uint64(0), int64(-5), int64(-1), uint64(0), uint64(0)),
	)
	DescribeTable("refuses a start at or past the end with InvalidRange",
		func(size uint64, ofs, end int64) {
			_, _, err := op.RangeToOfs(size, ofs, end)
			Expect(err).To(MatchError(op.ErrInvalidRange))
		},
		Entry("start past the end", uint64(11), int64(40), int64(50)),
		Entry("start at the size", uint64(11), int64(11), int64(-1)),
	)
})

var _ = Describe("ParseHTTPTime", func() {
	const (
		oct2100 = 4128522211 // 2100-10-29T19:43:31Z
		oct1994 = 783459811  // 1994-10-29T19:43:31Z
		sep2026 = 1790470923 // 2026-09-27T01:02:03Z
		iso2026 = 1790557323 // 2026-09-28T01:02:03Z
	)
	DescribeTable("accepts what parse_time accepts, at radosgw's value",
		func(s string, sec, nsec int64) {
			got, err := op.ParseHTTPTime(s)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeTemporally("==", time.Unix(sec, nsec)))
			Expect(got.Location()).To(Equal(time.UTC))
		},
		Entry("RFC 1123 GMT", "Sat, 29 Oct 2100 19:43:31 GMT", int64(oct2100), int64(0)),
		Entry("RFC 1123 UTC", "Sat, 29 Oct 2100 19:43:31 UTC", int64(oct2100), int64(0)),
		Entry("anything after GMT", "Sun, 27 Sep 2026 01:02:03 GMTx", int64(sep2026), int64(0)),
		Entry("no space before the zone", "Sun, 27 Sep 2026 01:02:03GMT", int64(sep2026), int64(0)),
		Entry("names in any case, the full weekday", "SUNDAY, 27 sEp 2026 01:02:03 GMT", int64(sep2026), int64(0)),
		Entry("a full month name", "Sun, 27 September 2026 01:02:03 GMT", int64(sep2026), int64(0)),
		Entry("a weekday the date does not fall on", "Fri, 27 Sep 2026 01:02:03 GMT", int64(sep2026), int64(0)),
		Entry("glibc's %a: an abbreviation moves where later weekdays match", "MonTue, 27 Sep 2026 01:02:03 GMT", int64(sep2026), int64(0)),
		Entry("glibc's %a: a full name after a moving abbreviation", "SunSaturday, 27 Sep 2026 01:02:03 GMT", int64(sep2026), int64(0)),
		Entry("glibc's %a in asctime", "SunMon Oct 29 19:43:31 1994", int64(oct1994), int64(0)),
		Entry("format whitespace matching none, numbers skipping it", "Sun,27 Sep 2026  1:2:3 GMT", int64(sep2026), int64(0)),
		Entry("a numeric offset is parsed and ignored", "Sat, 29 Oct 2100 19:43:31 +0100", int64(oct2100), int64(0)),
		Entry("a negative offset, trailing whitespace", "Sat, 29 Oct 2100 19:43:31 -0800 ", int64(oct2100), int64(0)),
		Entry("a two-digit offset", "Sat, 29 Oct 2100 19:43:31 +99", int64(oct2100), int64(0)),
		Entry("an offset with a colon", "Sat, 29 Oct 2100 19:43:31 +01:30", int64(oct2100), int64(0)),
		Entry("Z for the offset", "Sat, 29 Oct 2100 19:43:31 Z", int64(oct2100), int64(0)),
		Entry("a day past the month's end runs on", "Sun, 31 Feb 2026 01:02:03 GMT", int64(1772499723), int64(0)),
		Entry("a leap second runs into the next minute", "Sun, 27 Sep 2026 23:59:60 GMT", int64(1790553600), int64(0)),
		Entry("a three-digit year", "Sun, 27 Sep 226 01:02:03 GMT", int64(822488971), int64(0)),
		Entry("a date before 1970 wraps through 32 bits", "Thu, 01 Jan 1960 00:00:00 GMT", int64(3979348096), int64(0)),
		Entry("the last second 32 bits hold", "Sun, 07 Feb 2106 06:28:15 GMT", int64(math.MaxUint32), int64(0)),
		Entry("the second after wraps to the epoch", "Sun, 07 Feb 2106 06:28:16 GMT", int64(0), int64(0)),
		Entry("RFC 850", "Saturday, 29-Oct-94 19:43:31 GMT", int64(oct1994), int64(0)),
		Entry("RFC 850's year 68 is 2068", "Saturday, 29-Oct-68 19:43:31 GMT", int64(3118765411), int64(0)),
		Entry("RFC 850's year 69 is 1969, wrapped", "Saturday, 29-Oct-69 19:43:31 GMT", int64(4289508707), int64(0)),
		Entry("asctime", "Sat Oct 29 19:43:31 1994", int64(oct1994), int64(0)),
		Entry("asctime with a padded day", "Sat Oct  9 19:43:31 1994  ", int64(781731811), int64(0)),
		Entry("asctime's year 94 is the year 94", "Sat Oct 29 19:43:31 94", int64(954857955), int64(0)),
		Entry("ISO 8601 with Z", "2026-09-28T01:02:03Z", int64(iso2026), int64(0)),
		Entry("ISO 8601 with a fraction", "2026-09-28T01:02:03.250Z", int64(iso2026), int64(250_000_000)),
		Entry("ISO 8601 with a space", "2026-09-28 01:02:03", int64(iso2026), int64(0)),
		Entry("ISO 8601 with no separator", "2026-09-2801:02:03", int64(iso2026), int64(0)),
		Entry("ISO 8601 after whitespace, Z after a space", " 2026-09-28T01:02:03 Z", int64(iso2026), int64(0)),
		Entry("ISO 8601 with unpadded fields", "2026-9-8T1:2:3", int64(1788829323), int64(0)),
		Entry("a day stops before a digit that would pass 31", "2026-09-401:02:03", int64(1788483723), int64(0)),
		Entry("an empty fraction", "2026-09-28T01:02:03.Z", int64(iso2026), int64(0)),
		Entry("a fraction past a second carries", "2026-09-28T01:02:03.1234567891Z", int64(iso2026+1), int64(234567891)),
		Entry("a fraction cut to 32 bits", "2026-09-28T01:02:03.999999999999Z", int64(iso2026+3), int64(567587327)),
		Entry("a negative fraction, negated as unsigned", "2026-09-28T01:02:03.-5Z", int64(iso2026+4), int64(244967296)),
		Entry("a fraction carrying past 32 bits of seconds stops at their maximum", "2106-02-07T06:28:14.2000000000Z", int64(math.MaxUint32), int64(0)),
		Entry("ISO 8601's year 0", "0000-01-01T00:00:00Z", int64(2257376640), int64(0)),
		Entry("%T's numbers skipping white space", "2026-09-28T 1: 2: 3Z", int64(iso2026), int64(0)),
		Entry("a NUL ends an RFC date", "Sun, 27 Sep 2026 01:02:03 GMT\x00x", int64(sep2026), int64(0)),
		Entry("a NUL ends an ISO date", "2026-09-28T01:02:03Z\x00x", int64(iso2026), int64(0)),
		Entry("a fraction of white space, a sign and digits", "2026-09-28T01:02:03. +5Z", int64(iso2026), int64(5_000_000)),
	)
	DescribeTable("rejects the rest with InvalidArgument",
		func(s string) {
			_, err := op.ParseHTTPTime(s)
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		},
		Entry("garbage", "yesterday"),
		Entry("epoch seconds", "1700000000"),
		Entry("RFC 1123 without a zone", "Sat, 29 Oct 2100 19:43:31"),
		Entry("a lower-case zone", "Sun, 27 Sep 2026 01:02:03 gmt"),
		Entry("whitespace before an RFC date", " Sun, 27 Sep 2026 01:02:03 GMT"),
		Entry("a space before the comma", "Sun , 27 Sep 2026 01:02:03 GMT"),
		Entry("a name longer than its abbreviation but not the full name", "Sund, 27 Sep 2026 01:02:03 GMT"),
		Entry("glibc's %a: weekdays out of order", "MonSun, 27 Sep 2026 01:02:03 GMT"),
		Entry("glibc's %a: an abbreviation after a full name", "MondayTue, 27 Sep 2026 01:02:03 GMT"),
		Entry("Sept", "Sun, 27 Sept 2026 01:02:03 GMT"),
		Entry("day 0", "Mon, 00 Sep 2026 01:02:03 GMT"),
		Entry("day 32", "Sun, 32 Jan 2026 01:02:03 GMT"),
		Entry("hour 24", "Sun, 27 Sep 2026 24:02:03 GMT"),
		Entry("second 62", "Sun, 27 Sep 2026 23:59:62 GMT"),
		Entry("a five-digit year", "Sun, 27 Sep 02026 01:02:03 GMT"),
		Entry("an offset with minutes past 59", "Sat, 29 Oct 2100 19:43:31 +0160"),
		Entry("an offset of three digits", "Sat, 29 Oct 2100 19:43:31 +010"),
		Entry("an offset of five digits", "Sat, 29 Oct 2100 19:43:31 +01000"),
		Entry("an offset without a sign", "Sat, 29 Oct 2100 19:43:31 0100"),
		Entry("an offset with one hour digit", "Sat, 29 Oct 2100 19:43:31 +1:30"),
		Entry("RFC 850 without a zone", "Sat, 29-Oct-94 19:43:31"),
		Entry("RFC 850 with a four-digit year", "Thu, 01-Jan-1970 00:00:00 GMT"),
		Entry("asctime with a zone", "Sat Oct 29 19:43:31 1994 GMT"),
		Entry("ISO 8601 with an offset", "2026-09-28T01:02:03+01:00"),
		Entry("ISO 8601 with a fraction and no Z", "2026-09-28T01:02:03.250"),
		Entry("ISO 8601 with a lower-case t", "2026-09-28t01:02:03"),
		Entry("ISO 8601 month 13", "2026-13-01T00:00:00Z"),
		Entry("ISO 8601 without seconds", "2026-01-01T00:00"),
		Entry("ISO 8601 date alone", "2026-01-01"),
		Entry("a fraction that is not decimal", "2026-01-01T00:00:00.0x10Z"),
		Entry("a fraction with a space before Z", "2026-01-01T00:00:00.5 Z"),
		Entry("a fraction of ULONG_MAX, which stringtoul refuses", "2026-09-28T01:02:03.18446744073709551615Z"),
		Entry("a fraction past ULONG_MAX", "2026-09-28T01:02:03.18446744073709551616Z"),
	)
})

var _ = Describe("Unquote", func() {
	DescribeTable("is rgw_string_unquote",
		func(in, want string) { Expect(op.Unquote(in)).To(Equal(want)) },
		Entry("quoted", `"abc"`, "abc"),
		Entry("unquoted", `abc`, "abc"),
		Entry("trailing spaces after the closing quote", `"abc"   `, "abc"),
		Entry("no closing quote", `"abc`, `"abc`),
		Entry("a lone quote", `"`, `"`),
		Entry("a quote and spaces", `"   `, `"   `),
		Entry("two quotes", `""`, ""),
		Entry("quotes around a space", `" "  `, " "),
		Entry("a quote inside", `"a"b"`, `a"b`),
		Entry("a space before the opening quote", ` "abc"`, ` "abc"`),
		Entry("star", `*`, `*`),
	)
})
