package driver

import (
	"bytes"
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("scanLCWorkTime", func() {
	DescribeTable("scans rgw_lifecycle_work_time as should_work's sscanf does, \"%d:%d-%d:%d\"",
		func(value string, want lcWorkTime) {
			Expect(scanLCWorkTime(value)).To(Equal(want))
		},
		Entry("the default", "00:00-06:00", lcWorkTime{endHour: 6}),
		Entry("a window past midnight", "22:30-02:15", lcWorkTime{startHour: 22, startMinute: 30, endHour: 2, endMinute: 15}),
		Entry("white space before a number", " 1:\t2-\n3: 4", lcWorkTime{startHour: 1, startMinute: 2, endHour: 3, endMinute: 4}),
		Entry("signed numbers", "+1:-0-23:+59", lcWorkTime{startHour: 1, endHour: 23, endMinute: 59}),
		Entry("text after the last number", "01:00-02:00 and more", lcWorkTime{startHour: 1, endHour: 2}),
		Entry("hours and minutes outside a day, kept as scanned", "-1:00-25:99", lcWorkTime{startHour: -1, endHour: 25, endMinute: 99}),
		Entry("the largest int", "2147483647:0-0:0", lcWorkTime{startHour: math.MaxInt32}),
	)

	DescribeTable("refuses a value whose four numbers sscanf would not all assign, naming the option and the value",
		func(value string) {
			_, err := scanLCWorkTime(value)
			Expect(err).To(MatchError(And(ContainSubstring("rgw_lifecycle_work_time"), ContainSubstring("%q", value))))
		},
		Entry("empty", ""),
		Entry("two numbers", "01:00"),
		Entry("three numbers", "01:00-02"),
		Entry("a space before the dash, which sscanf's '-' does not skip", "01:00 -02:00"),
		Entry("dots for colons", "01.00-02.00"),
		Entry("a hexadecimal hour, which %d reads as 0 and then stops at the x", "0x1:00-02:00"),
		Entry("a sign without digits", "+:00-02:00"),
		Entry("a number beyond int", "2147483648:00-01:00"),
	)
})

// counts is lcWorkerCounts's two results as one value.
func counts(workers, wpWorkers int64) []int {
	w, wp := lcWorkerCounts(workers, wpWorkers)
	return []int{w, wp}
}

var _ = Describe("lcWorkerCounts", func() {
	var buf *bytes.Buffer
	BeforeEach(func() {
		buf = &bytes.Buffer{}
		DeferCleanup(captureLog(buf))
	})

	It("keeps counts that are usable without a word", func() {
		Expect(counts(3, 7)).To(Equal([]int{3, 7}))
		Expect(counts(0, 1)).To(Equal([]int{0, 1}), "no worker is a count radosgw takes")
		Expect(buf.String()).To(BeEmpty())
	})

	It("runs no worker for a negative count, saying so with the value read", func() {
		Expect(counts(-2, 3)).To(Equal([]int{0, 3}))
		Expect(logRecords(buf)).To(ConsistOf(And(HaveKeyWithValue("level", "ERROR"),
			HaveKeyWithValue("option", "rgw_lc_max_worker"), HaveKeyWithValue("value", BeNumerically("==", -2)))))
	})

	DescribeTable("floors a workpool size that is not positive to one, saying so with the value read",
		func(wp int64) {
			Expect(counts(3, wp)).To(Equal([]int{3, 1}))
			Expect(logRecords(buf)).To(ConsistOf(And(HaveKeyWithValue("level", "ERROR"),
				HaveKeyWithValue("option", "rgw_lc_max_wp_worker"), HaveKeyWithValue("value", BeNumerically("==", wp)))))
		},
		Entry("zero, where radosgw divides by zero", int64(0)),
		Entry("a multiple of 65536, which radosgw narrows to zero", int64(-65536)),
	)

	It("uses a workpool size above 65535 as given, where radosgw narrows it to 16 bits", func() {
		Expect(counts(3, 65536)).To(Equal([]int{3, 65536}))
		Expect(buf.String()).To(BeEmpty())
	})
})
