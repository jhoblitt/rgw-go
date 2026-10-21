package lifecycle_test

import (
	"time"
	_ "time/tzdata" // the DST specs load zones a host may lack

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/lifecycle"
)

func utc(s string) time.Time {
	GinkgoHelper()
	t, err := time.Parse(time.RFC3339Nano, s)
	Expect(err).NotTo(HaveOccurred())
	return t
}

func zone(name string) *time.Location {
	GinkgoHelper()
	loc, err := time.LoadLocation(name)
	Expect(err).NotTo(HaveOccurred())
	return loc
}

// ist is a zone five and a half hours ahead of UTC, with no daylight saving.
var ist = time.FixedZone("IST", 5*3600+1800)

var _ = Describe("Expired", func() {
	DescribeTable("counts days from local midnight of now back to the mtime's second",
		func(mtime string, days int, now string, loc *time.Location, want bool) {
			Expect(lifecycle.Expired(utc(mtime), days, utc(now), 0, loc)).To(Equal(want))
		},
		Entry("a day old at local midnight, in a zone ahead of UTC",
			"2026-06-13T18:30:00Z", 1, "2026-06-14T18:40:00Z", ist, true),
		Entry("a second short of a day at local midnight",
			"2026-06-13T18:30:01Z", 1, "2026-06-14T18:40:00Z", ist, false),
		Entry("the mtime's fraction, which to_time_t drops",
			"2026-06-13T18:30:00.999Z", 1, "2026-06-14T18:40:00Z", ist, true),
		Entry("ten minutes before local midnight, the day not yet turned",
			"2026-06-13T18:30:00Z", 1, "2026-06-14T18:20:00Z", ist, false),
		Entry("due in UTC once its midnight passes",
			"2026-06-13T00:00:00Z", 1, "2026-06-14T00:10:00Z", time.UTC, true),
		Entry("not yet in a zone whose midnight lies 18.5 hours after the mtime",
			"2026-06-13T00:00:00Z", 1, "2026-06-14T00:10:00Z", ist, false),
		Entry("Days 0 for an object written before local midnight",
			"2026-06-14T18:29:59Z", 0, "2026-06-14T18:40:00Z", ist, true),
		Entry("Days 0 for an object written after it",
			"2026-06-14T18:30:01Z", 0, "2026-06-14T18:40:00Z", ist, false),
		Entry("the zero time, which is the epoch as an unset real_time, so 21000 days have not passed",
			"0001-01-01T00:00:00Z", 21000, "2026-06-14T18:40:00Z", time.UTC, false),
		Entry("the zero time, 20000 days old",
			"0001-01-01T00:00:00Z", 20000, "2026-06-14T18:40:00Z", time.UTC, true),
	)

	DescribeTable("reads local midnight as glibc's mktime does with localtime_r's tm_isdst kept",
		func(name, mtime, now string, want bool) {
			Expect(lifecycle.Expired(utc(mtime), 0, utc(now), 0, zone(name))).To(Equal(want))
		},
		Entry("New York's spring-forward day, whose midnight mktime puts at 04:00Z, not 05:00Z",
			"America/New_York", "2026-03-08T04:00:00Z", "2026-03-08T14:00:00Z", true),
		Entry("a second after that midnight",
			"America/New_York", "2026-03-08T04:00:01Z", "2026-03-08T14:00:00Z", false),
		Entry("New York's fall-back day, whose midnight mktime puts at 05:00Z, not 04:00Z",
			"America/New_York", "2026-11-01T04:30:00Z", "2026-11-01T15:00:00Z", true),
		Entry("past it",
			"America/New_York", "2026-11-01T05:00:01Z", "2026-11-01T15:00:00Z", false),
		Entry("Santiago, whose clocks spring forward at midnight, at 03:00Z",
			"America/Santiago", "2026-09-06T03:00:00Z", "2026-09-06T13:00:00Z", true),
		Entry("a second after it",
			"America/Santiago", "2026-09-06T03:00:01Z", "2026-09-06T13:00:00Z", false),
		Entry("Caracas on the day it moved from -04:30 to -04:00, its true midnight at 04:30Z, as both DST flags are clear",
			"America/Caracas", "2016-05-01T04:30:00Z", "2016-05-01T16:00:00Z", true),
		Entry("a second after it",
			"America/Caracas", "2016-05-01T04:30:01Z", "2016-05-01T16:00:00Z", false),
		Entry("Moscow on the day it moved from +04 to +03, its true midnight at 20:00Z the day before",
			"Europe/Moscow", "2014-10-25T20:00:00Z", "2014-10-26T12:00:00Z", true),
		Entry("within the hour after it, which the offset now has would put before midnight",
			"Europe/Moscow", "2014-10-25T20:59:59Z", "2014-10-26T12:00:00Z", false),
		Entry("Cairo, whose DST starts at a midnight that does not exist, read at now's offset, 21:00Z",
			"Africa/Cairo", "2026-04-23T21:00:00Z", "2026-04-24T10:00:00Z", true),
		Entry("a second after it, inside the hour the zone skips",
			"Africa/Cairo", "2026-04-23T21:00:01Z", "2026-04-24T10:00:00Z", false),
		Entry("Beirut, likewise, at 21:00Z the day before",
			"Asia/Beirut", "2026-03-28T21:00:00Z", "2026-03-29T10:00:00Z", true),
		Entry("a second after Beirut's",
			"Asia/Beirut", "2026-03-28T21:00:01Z", "2026-03-29T10:00:00Z", false),
		Entry("Magadan, whose midnight occurs twice with no flag set, at the later, 14:00Z, glibc's answer in a fresh process",
			"Asia/Magadan", "2014-10-25T14:00:00Z", "2014-10-26T05:00:00Z", true),
		Entry("a second after Magadan's",
			"Asia/Magadan", "2014-10-25T14:00:01Z", "2014-10-26T05:00:00Z", false),
	)

	DescribeTable("with a positive debug interval, counts days of that length from now itself",
		func(mtime string, days int, now string, want bool) {
			Expect(lifecycle.Expired(utc(mtime), days, utc(now), 10*time.Second, ist)).To(Equal(want))
		},
		Entry("three intervals old", "2026-06-14T12:00:00Z", 3, "2026-06-14T12:00:30Z", true),
		Entry("a nanosecond short", "2026-06-14T12:00:00Z", 3, "2026-06-14T12:00:29.999999999Z", false),
		Entry("the mtime's fraction dropped", "2026-06-14T12:00:00.5Z", 3, "2026-06-14T12:00:30Z", true),
		Entry("local midnight ignored", "2026-06-14T18:29:00Z", 1, "2026-06-14T18:29:10Z", true),
		Entry("days × interval past a Duration, whose wrap would be negative, never due", "2026-06-14T12:00:00Z", 1000000000, "2026-06-14T12:00:30Z", false),
	)

	It("reads a nil location as UTC", func() {
		Expect(lifecycle.Expired(utc("2026-06-13T00:00:00Z"), 1, utc("2026-06-14T00:10:00Z"), 0, nil)).To(BeTrue())
		Expect(lifecycle.Expired(utc("2026-06-13T00:00:01Z"), 1, utc("2026-06-14T00:10:00Z"), 0, nil)).To(BeFalse())
	})

	It("takes a debug interval of zero or less for none", func() {
		Expect(lifecycle.Expired(utc("2026-06-14T12:00:00Z"), 1, utc("2026-06-14T12:00:30Z"), -time.Second, time.UTC)).To(BeFalse())
		Expect(lifecycle.Expired(utc("2026-06-13T12:00:00Z"), 1, utc("2026-06-15T00:00:30Z"), -time.Second, time.UTC)).To(BeTrue())
	})
})
