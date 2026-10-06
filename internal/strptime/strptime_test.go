package strptime_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/strptime"
)

var _ = Describe("Parse", func() {
	DescribeTable("reads as glibc's strptime does in the C locale",
		func(in, format string, want strptime.Tm, wantRest string) {
			got, rest, ok := strptime.Parse(strptime.Tm{}, in, format)
			Expect(ok).To(BeTrue(), "%q against %q", in, format)
			Expect(got).To(Equal(want))
			Expect(rest).To(Equal(wantRest))
		},
		Entry("a number stops before a digit that would pass its maximum", "2012-01-50", "%Y-%m-%d",
			strptime.Tm{Year: 2012, Mon: 0, Mday: 5}, "0"),
		Entry("a one-digit field leaves the next digit to the format", "16:7550", "%H:%M5%S",
			strptime.Tm{Hour: 16, Min: 7, Sec: 50}, ""),
		Entry("whitespace before a number is skipped", " 7", "%e", strptime.Tm{Mday: 7}, ""),
		Entry("%n, %t and a format space match any run of whitespace, none included", "1 \t2", "%H%n%M%t", strptime.Tm{Hour: 1, Min: 2}, ""),
		Entry("%% matches a percent sign", "%5", "%%%S", strptime.Tm{Sec: 5}, ""),
		Entry("%B and %h are %b", "march APR", "%B %h", strptime.Tm{Mon: 3}, ""),
		Entry("%z with a colon", "+02:30", "%z", strptime.Tm{Gmtoff: 9000}, ""),
		Entry("%z with two digits", "-05", "%z", strptime.Tm{Gmtoff: -18000}, ""),
		Entry("%y before 69 is 20xx", "68", "%y", strptime.Tm{Year: 2068}, ""),
	)
	It("starts from the fields it is given, as a second call on the same struct tm does", func() {
		got, _, ok := strptime.Parse(strptime.Tm{Year: 2012, Mday: 25}, "16:00", "%H:%M")
		Expect(ok).To(BeTrue())
		Expect(got).To(Equal(strptime.Tm{Year: 2012, Mday: 25, Hour: 16}))
	})
	DescribeTable("fails where glibc returns NULL",
		func(in, format string) {
			_, _, ok := strptime.Parse(strptime.Tm{}, in, format)
			Expect(ok).To(BeFalse(), "%q against %q", in, format)
		},
		Entry("a hour past 23", "24", "%H"),
		Entry("a literal that does not match", "16-00", "%H:%M"),
		Entry("input that ends early", "16", "%H:%M"),
		Entry("%z with three digits", "+023", "%z"),
		Entry("%z minutes past 59", "+0260", "%z"),
		Entry("a conversion glibc does not know either", "1", "%q"),
		Entry("a format ending in %", "1", "%H%"),
	)
	// docs/exclusions.md, "A date whose time names a conversion rgw-go does
	// not run is refused": glibc reads these.
	DescribeTable("refuses a conversion outside its set, which glibc runs",
		func(in, format string) {
			_, _, ok := strptime.Parse(strptime.Tm{}, in, format)
			Expect(ok).To(BeFalse(), "%q against %q", in, format)
		},
		Entry("a day of the year", "1", "%j"),
		Entry("a flag on a listed conversion", "1", "%-d"),
		Entry("a width on a listed conversion", "05", "%2S"),
		Entry("an O modifier on a listed conversion", "1", "%Od"),
	)
})

var _ = Describe("Timegm", func() {
	It("rolls a day past the month's end into the next, as internal_timegm does", func() {
		Expect(strptime.Tm{Year: 2015, Mon: 3, Mday: 31}.Timegm()).To(Equal(time.Date(2015, 5, 1, 0, 0, 0, 0, time.UTC)))
	})
	It("counts year 0 as 365 days, a day off the proleptic calendar", func() {
		Expect(strptime.Tm{Year: 0, Mon: 2, Mday: 1}.Timegm().Unix()).To(Equal(int64(-62161948800)))
		Expect(time.Date(0, 3, 1, 0, 0, 0, 0, time.UTC).Unix()).To(Equal(int64(-62161948800 - 86400)))
	})
	It("ignores the zone offset", func() {
		Expect(strptime.Tm{Year: 1970, Mday: 1, Gmtoff: 3600}.Timegm().Unix()).To(BeZero())
	})
})
