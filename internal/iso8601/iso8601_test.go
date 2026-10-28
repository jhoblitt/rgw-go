package iso8601_test

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/iso8601"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/strptime"
)

func utc(y int, mo time.Month, d, h, mi, s, ns int) time.Time {
	return time.Date(y, mo, d, h, mi, s, ns, time.UTC)
}

var _ = Describe("Parse", func() {
	DescribeTable("reads what from_iso_8601 reads, with the nanoseconds",
		func(s string, want time.Time) {
			got, ok := iso8601.Parse(s)
			Expect(ok).To(BeTrue(), "%q", s)
			Expect(got).To(Equal(want), "%q", s)
		},
		Entry("a year alone, its first midnight", "2030", utc(2030, 1, 1, 0, 0, 0, 0)),
		Entry("a year and a month", "2030-06", utc(2030, 6, 1, 0, 0, 0, 0)),
		Entry("a date", "2030-06-15", utc(2030, 6, 15, 0, 0, 0, 0)),
		Entry("a date ended by a space, whatever follows", "2030-06-15 trailing text", utc(2030, 6, 15, 0, 0, 0, 0)),
		Entry("a year ended by a vertical tab, C white space", "2030\vx", utc(2030, 1, 1, 0, 0, 0, 0)),
		Entry("a month ended by a line feed", "2030-06\n", utc(2030, 6, 1, 0, 0, 0, 0)),
		Entry("an hour and Z", "2030-06-15T12Z", utc(2030, 6, 15, 12, 0, 0, 0)),
		Entry("an hour, minutes and Z", "2030-06-15T12:30Z", utc(2030, 6, 15, 12, 30, 0, 0)),
		Entry("a whole second and Z", "2030-06-15T12:30:45Z", utc(2030, 6, 15, 12, 30, 45, 0)),
		Entry("a Z ended by a tab, whatever follows", "2030-06-15T12:30:45Z\tx", utc(2030, 6, 15, 12, 30, 45, 0)),
		Entry("one fractional digit, tenths", "2030-06-15T12:30:45.1Z", utc(2030, 6, 15, 12, 30, 45, 100_000_000)),
		Entry("two fractional digits", "2030-06-15T12:30:45.12Z", utc(2030, 6, 15, 12, 30, 45, 120_000_000)),
		Entry("three fractional digits", "2030-06-15T12:30:45.123Z", utc(2030, 6, 15, 12, 30, 45, 123_000_000)),
		Entry("four fractional digits", "2030-06-15T12:30:45.1234Z", utc(2030, 6, 15, 12, 30, 45, 123_400_000)),
		Entry("five fractional digits", "2030-06-15T12:30:45.12345Z", utc(2030, 6, 15, 12, 30, 45, 123_450_000)),
		Entry("six fractional digits", "2030-06-15T12:30:45.123456Z", utc(2030, 6, 15, 12, 30, 45, 123_456_000)),
		Entry("seven fractional digits", "2030-06-15T12:30:45.1234567Z", utc(2030, 6, 15, 12, 30, 45, 123_456_700)),
		Entry("eight fractional digits", "2030-06-15T12:30:45.12345678Z", utc(2030, 6, 15, 12, 30, 45, 123_456_780)),
		Entry("nine fractional digits, nanoseconds", "2030-06-15T12:30:45.123456789Z", utc(2030, 6, 15, 12, 30, 45, 123_456_789)),
		Entry("a fraction ended by white space after its Z", "2030-06-15T12:30:45.5Z x", utc(2030, 6, 15, 12, 30, 45, 500_000_000)),
		Entry("a thirteenth month, carried into the next year", "2030-13-01", utc(2031, 1, 1, 0, 0, 0, 0)),
		Entry("the 31st of February, carried into March", "2030-02-31", utc(2030, 3, 3, 0, 0, 0, 0)),
		Entry("hour 24, the next day", "2030-06-15T24Z", utc(2030, 6, 16, 0, 0, 0, 0)),
		Entry("second 60, the next minute", "2030-06-15T12:30:60Z", utc(2030, 6, 15, 12, 31, 0, 0)),
		Entry("the epoch", "1970", time.Unix(0, 0).UTC()),
		Entry("the epoch's first nanosecond", "1970-01-01T00:00:00.000000001Z", time.Unix(0, 1).UTC()),
		Entry("real_time's last nanosecond", "2554-07-21T23:34:33.709551615Z", meta.MaxRealTime),
	)
	DescribeTable("refuses what from_iso_8601 refuses, and what real_time cannot hold",
		func(s string) {
			got, ok := iso8601.Parse(s)
			Expect(ok).To(BeFalse(), "%q read as %v", s, got)
			Expect(got).To(BeZero(), "%q", s)
		},
		Entry("an empty string", ""),
		Entry("a three-digit year", "203"),
		Entry("a five-digit year", "20301"),
		Entry("a dash and nothing after it", "2030-"),
		Entry("a one-digit month", "2030-6"),
		Entry("a one-digit day", "2030-06-1"),
		Entry("slashes", "2030/06/15"),
		Entry("leading white space", " 2030-06-15"),
		Entry("white space that is not C's, a no-break space", "2030-06-15 "),
		Entry("a T and nothing after it", "2030-06-15T"),
		Entry("an hour without Z", "2030-06-15T12"),
		Entry("a one-digit hour", "2030-06-15T1Z"),
		Entry("minutes without Z", "2030-06-15T12:30"),
		Entry("a whole second without Z", "2030-06-15T12:30:45"),
		Entry("a lower-case z", "2030-06-15T12:30:45z"),
		Entry("a lower-case t", "2030-06-15t12:30:45Z"),
		Entry("a zone offset", "2030-06-15T12:30:45+01:00"),
		Entry("a dot and nothing after it", "2030-06-15T12:30:45."),
		Entry("a dot without digits", "2030-06-15T12:30:45.Z"),
		Entry("a fraction without Z", "2030-06-15T12:30:45.5"),
		Entry("ten fractional digits", "2030-06-15T12:30:45.1234567890Z"),
		Entry("a Z followed by more than white space", "2030-06-15T12:30:45Zx"),
		Entry("a Z after a date", "2030-06-15Z"),
		Entry("a year before 1970", "1969-12-31T23:59:59Z"),
		Entry("a year before 1970 that its thirteenth month carries to the epoch", "1969-13"),
		Entry("year 0", "0000"),
		Entry("day 00 of 1970, before the epoch, which from_iso_8601 wraps to 2554", "1970-01-00"),
		Entry("month 00 of 1970, December 1969, which from_iso_8601 wraps to 2554", "1970-00"),
		Entry("the second before the epoch, which internal_timegm makes -1", "1970-01-00T23:59:59Z"),
		Entry("a nanosecond past real_time's range, which from_iso_8601 wraps to the epoch", "2554-07-21T23:34:33.709551616Z"),
		Entry("a second past real_time's range", "2554-07-21T23:34:34Z"),
		Entry("a year past real_time's range, which from_iso_8601 wraps to 2415", "3000"),
		Entry("the largest year from_iso_8601 reads", "9999-12-31T23:59:59.999999999Z"),
	)
	It("reads past a NUL, as the string_view it is given does", func() {
		_, ok := iso8601.Parse("2030-06-15T12:30:45Z\x00")
		Expect(ok).To(BeFalse(), "a NUL after the Z is neither the end nor white space")
		got, ok := iso8601.Parse("2030-06-15\x00")
		Expect(ok).To(BeFalse(), "a NUL after the date is no delimiter: %v", got)
	})
})

var _ = Describe("Format", func() {
	DescribeTable("writes to_iso_8601's YMDhmsn: nine fractional digits and a Z",
		func(t time.Time, want string) {
			Expect(iso8601.Format(t)).To(Equal(want))
		},
		Entry("the zero time.Time, the epoch as denc reads an unset real_time", time.Time{}, "1970-01-01T00:00:00.000000000Z"),
		Entry("the epoch", time.Unix(0, 0), "1970-01-01T00:00:00.000000000Z"),
		Entry("a whole second, its fraction zeros", utc(2030, 6, 15, 12, 30, 45, 0), "2030-06-15T12:30:45.000000000Z"),
		Entry("one nanosecond, zero-padded", utc(2030, 6, 15, 12, 30, 45, 1), "2030-06-15T12:30:45.000000001Z"),
		Entry("milliseconds, zero-padded on the right", utc(2030, 1, 2, 3, 4, 5, 6_000_000), "2030-01-02T03:04:05.006000000Z"),
		Entry("a time in another zone, written in UTC", time.Date(2030, 6, 15, 14, 30, 45, 0, time.FixedZone("CEST", 2*3600)), "2030-06-15T12:30:45.000000000Z"),
		Entry("real_time's last nanosecond", meta.MaxRealTime, "2554-07-21T23:34:33.709551615Z"),
	)
	It("writes what Parse reads back", func() {
		for _, t := range []time.Time{time.Unix(0, 0).UTC(), utc(2030, 6, 15, 12, 30, 45, 123_456_789), meta.MaxRealTime} {
			got, ok := iso8601.Parse(iso8601.Format(t))
			Expect(ok).To(BeTrue(), "%v", t)
			Expect(got).To(Equal(t))
		}
	})
})

var _ = Describe("MaxRealTime", func() {
	It("is real_time's u64 nanoseconds at their maximum", func() {
		Expect(meta.MaxRealTime.Unix()).To(Equal(int64(18446744073)))
		Expect(meta.MaxRealTime.Nanosecond()).To(Equal(709551615))
		Expect(meta.MaxRealTime.Location()).To(Equal(time.UTC))
	})
})

// tm is the struct tm strptime fills for a date and time.
func tm(y, mon, d, h, mi, s int) strptime.Tm {
	return strptime.Tm{Year: y, Mon: mon - 1, Mday: d, Hour: h, Min: mi, Sec: s}
}

var _ = Describe("ParseExtended", func() {
	DescribeTable("reads what parse_iso8601 reads in its extended format, the fields as strptime leaves them and pns",
		func(s string, want strptime.Tm, ns uint32) {
			got, gotNS, ok := iso8601.ParseExtended(s)
			Expect(ok).To(BeTrue(), "%q", s)
			Expect(got).To(Equal(want), "%q", s)
			Expect(gotNS).To(Equal(ns), "%q", s)
		},
		Entry("a T and Z", "2026-09-28T01:02:03Z", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("no Z", "2026-09-28T01:02:03", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("a space for the T", "2026-09-28 01:02:03", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("no separator, which the space form's white space matches", "2026-09-2801:02:03", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("white space before, and before a Z, trimmed", " 2026-09-28T01:02:03 Z ", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("unpadded fields, as strptime reads numbers", "2026-9-8T1:2:3", tm(2026, 9, 8, 1, 2, 3), uint32(0)),
		Entry("%T's numbers skipping white space", "2026-09-28T 1: 2: 3Z", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("a fraction, scaled by mul_table", "2026-09-28T01:02:03.25Z", tm(2026, 9, 28, 1, 2, 3), uint32(250_000_000)),
		Entry("nine fractional digits", "2026-09-28T01:02:03.123456789Z", tm(2026, 9, 28, 1, 2, 3), uint32(123_456_789)),
		Entry("an empty fraction, which stringtoul takes as 0", "2026-09-28T01:02:03.Z", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
		Entry("ten digits, the value scaled for nine, past a second", "2026-09-28T01:02:03.1234567891Z", tm(2026, 9, 28, 1, 2, 3), uint32(1_234_567_891)),
		Entry("twelve digits, the value cut to 32 bits", "2026-09-28T01:02:03.999999999999Z", tm(2026, 9, 28, 1, 2, 3), uint32(3_567_587_327)),
		Entry("a negative fraction, negated as unsigned", "2026-09-28T01:02:03.-5Z", tm(2026, 9, 28, 1, 2, 3), uint32(4_244_967_296)),
		Entry("a fraction of white space, a sign and a digit, three bytes", "2026-09-28T01:02:03. +5Z", tm(2026, 9, 28, 1, 2, 3), uint32(5_000_000)),
		Entry("the 31st of April, which strptime takes as a day", "2026-04-31T00:00:00Z", tm(2026, 4, 31, 0, 0, 0), uint32(0)),
		Entry("year 0", "0000-01-01T00:00:00Z", tm(0, 1, 1, 0, 0, 0), uint32(0)),
		Entry("a NUL ends the C string", "2026-09-28T01:02:03Z\x00x", tm(2026, 9, 28, 1, 2, 3), uint32(0)),
	)
	DescribeTable("refuses what parse_iso8601 refuses in its extended format",
		func(s string) {
			_, _, ok := iso8601.ParseExtended(s)
			Expect(ok).To(BeFalse(), "%q", s)
		},
		Entry("an empty string", ""),
		Entry("a zone offset", "2026-09-28T01:02:03+01:00"),
		Entry("a fraction without Z", "2026-09-28T01:02:03.250"),
		Entry("a lower-case t", "2026-09-28t01:02:03"),
		Entry("month 13", "2026-13-01T00:00:00Z"),
		Entry("no seconds", "2026-01-01T00:00"),
		Entry("a date alone", "2026-01-01"),
		Entry("the basic format", "20260928T010203Z"),
		Entry("a fraction stringtoul refuses", "2026-09-28T01:02:03.1aZ"),
		Entry("a fraction of -1, which strtoul wraps to ULONG_MAX", "2026-09-28T01:02:03.-1Z"),
		Entry("a lower-case z", "2026-09-28T01:02:03z"),
		Entry("a fraction that is a Z", "2026-09-28T01:02:03.ZZ"),
	)
})

var _ = Describe("ParseBasic", func() {
	DescribeTable("reads what parse_iso8601 reads without the extended format",
		func(s string, want strptime.Tm, ns uint32) {
			got, gotNS, ok := iso8601.ParseBasic(s)
			Expect(ok).To(BeTrue(), "%q", s)
			Expect(got).To(Equal(want), "%q", s)
			Expect(gotNS).To(Equal(ns), "%q", s)
		},
		Entry("with Z", "20150830T123600Z", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
		Entry("without Z", "20150830T123600", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
		Entry("a fraction", "20150830T123600.123Z", tm(2015, 8, 30, 12, 36, 0), uint32(123_000_000)),
		Entry("trailing white space", "20150830T123600Z ", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
		Entry("an empty fraction", "20150830T123600.Z", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
		Entry("a fraction of -2, which strtoul wraps below ULONG_MAX", "20150830T123600.-2Z", tm(2015, 8, 30, 12, 36, 0), uint32(4_274_967_296)),
		Entry("leading white space, which strptime skips before a number", " 20150830T123600Z", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
		Entry("a one-digit month, which strptime's width rule reads", "2015830T123600Z", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
		Entry("second 60, which strptime reads", "20150830T123660Z", tm(2015, 8, 30, 12, 36, 60), uint32(0)),
		Entry("a NUL ends the C string", "20150830T123600Z\x00junk", tm(2015, 8, 30, 12, 36, 0), uint32(0)),
	)
	DescribeTable("refuses what parse_iso8601 refuses without the extended format",
		func(s string) {
			_, _, ok := iso8601.ParseBasic(s)
			Expect(ok).To(BeFalse(), "%q", s)
		},
		Entry("the extended format", "2015-08-30T12:36:00Z"),
		Entry("an RFC 1123 date", "Sun, 30 Aug 2015 12:36:00 GMT"),
		Entry("a fraction without Z", "20150830T123600.123"),
		Entry("a fraction stringtoul refuses", "20150830T123600.1aZ"),
		Entry("a fraction of ULONG_MAX", "20150830T123600.18446744073709551615Z"),
		Entry("a fraction past ULONG_MAX, which strtoul saturates to it", "20150830T123600.18446744073709551616Z"),
		Entry("month 13", "20151330T123600Z"),
	)
})

// nanos is t's nanoseconds since the epoch as real_time holds them.
func nanos(t time.Time) uint64 { return uint64(t.UnixNano()) } //nolint:gosec // every t here is after the epoch

var _ = Describe("ParseWrapped", func() {
	DescribeTable("reads what from_iso_8601 reads without ws_terminates, real_time's nanoseconds wrapped modulo 2^64",
		func(s string, want uint64) {
			got, ok := iso8601.ParseWrapped(s)
			Expect(ok).To(BeTrue(), "%q", s)
			Expect(got).To(Equal(want), "%q", s)
		},
		Entry("the epoch", "1970", uint64(0)),
		Entry("a year and a month", "2030-06", nanos(utc(2030, 6, 1, 0, 0, 0, 0))),
		Entry("a date", "2030-06-15", nanos(utc(2030, 6, 15, 0, 0, 0, 0))),
		Entry("an hour and Z", "2030-06-15T12Z", nanos(utc(2030, 6, 15, 12, 0, 0, 0))),
		Entry("minutes and Z", "2030-06-15T12:30Z", nanos(utc(2030, 6, 15, 12, 30, 0, 0))),
		Entry("a whole second and Z", "2030-06-15T12:30:45Z", nanos(utc(2030, 6, 15, 12, 30, 45, 0))),
		Entry("one fractional digit", "2030-06-15T12:30:45.5Z", nanos(utc(2030, 6, 15, 12, 30, 45, 500_000_000))),
		Entry("nine fractional digits", "2030-06-15T12:30:45.123456789Z", nanos(utc(2030, 6, 15, 12, 30, 45, 123_456_789))),
		Entry("a thirteenth month, carried into the next year", "2030-13-01", nanos(utc(2031, 1, 1, 0, 0, 0, 0))),
		Entry("hour 25, minute 61 and second 61, carried", "2030-01-01T25:61:61Z", nanos(utc(2030, 1, 2, 2, 2, 1, 0))),
		Entry("real_time's last nanosecond", "2554-07-21T23:34:33.709551615Z", uint64(math.MaxUint64)),
		Entry("a nanosecond past real_time's range, wrapped to the epoch", "2554-07-21T23:34:33.709551616Z", uint64(0)),
		Entry("a date past 2554, wrapped into 2015", "2600-01-01", uint64(1434155126290448384)),
		Entry("the largest year from_iso_8601 reads, wrapped", "9999-12-31", uint64(13594541441775828992)),
		Entry("a day before the epoch, wrapped to 2554", "1970-01-00", uint64(18446657673709551616)),
		Entry("month 00 of 1970, December 1969, wrapped", "1970-00", uint64(18444065673709551616)),
	)
	DescribeTable("refuses what from_iso_8601 refuses without ws_terminates",
		func(s string) {
			got, ok := iso8601.ParseWrapped(s)
			Expect(ok).To(BeFalse(), "%q read as %d", s, got)
			Expect(got).To(BeZero(), "%q", s)
		},
		Entry("an empty string", ""),
		Entry("a year before 1970", "1969-12-31"),
		Entry("the second before the epoch, which internal_timegm makes -1", "1970-01-00T23:59:59Z"),
		Entry("a one-digit month", "2030-6"),
		Entry("an hour without Z", "2030-06-15T12"),
		Entry("a Z after a date", "2030-06-15Z"),
		Entry("a dot without digits", "2030-06-15T12:30:45.Z"),
		Entry("ten fractional digits", "2030-06-15T12:30:45.1234567890Z"),
		Entry("a zone offset", "2030-06-15T12:30:45+01:00"),
		Entry("a NUL after the Z", "2030-06-15T12:30:45Z\x00"),
	)
	DescribeTable("refuses a date or a time that white space follows, which Parse takes",
		func(s string) {
			_, ok := iso8601.ParseWrapped(s)
			Expect(ok).To(BeFalse(), "%q", s)
			_, ok = iso8601.Parse(s)
			Expect(ok).To(BeTrue(), "%q", s)
		},
		Entry("a year and a space", "2030 "),
		Entry("a date and a space", "2030-06-15 "),
		Entry("a date, a space and text", "2030-06-15 trailing text"),
		Entry("a month and a line feed", "2030-06\n"),
		Entry("a Z and a tab", "2030-06-15T12:30:45Z\t"),
		Entry("a fraction's Z and a space", "2030-06-15T12:30:45.5Z x"),
	)
})
