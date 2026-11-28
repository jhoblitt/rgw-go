package objectlock_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
)

// clockSecond is a whole second, and clockJustBefore 50 ns before it, which
// a double of the clock's seconds reads as clockSecond itself.
var (
	clockSecond     = time.Date(2026, 10, 20, 12, 0, 1, 0, time.UTC)
	clockJustBefore = clockSecond.Add(-50 * time.Nanosecond)
)

// docs/differences.md, "A retain-until second is compared with the clock
// exactly": radosgw compares a date's whole seconds with ceph_clock_now()
// through utime_t's operator double, which at today's clock holds the
// fraction of a second to 2^-22 s, so a clock within about 119 ns of a whole
// second reads as that second. rgw-go compares the clock's whole seconds.
var _ = Describe("the clock a retain-until second is compared with, as docs/differences.md records it", func() {
	retainedUntil := func(mode string, until time.Time) map[string][]byte {
		return map[string][]byte{meta.AttrETag: []byte("x"), meta.AttrObjectRetention: retentionAttr(mode, until)}
	}

	DescribeTable("VerifyDelete keeps a version whose retention ends at the next second, 50 ns before it",
		func(mode string, header, allowed bool) {
			Expect(objectlock.VerifyDelete(retainedUntil(mode, clockSecond), clockJustBefore, header, allowed)).To(MatchError(objectlock.ErrLocked))
		},
		Entry("COMPLIANCE, a delete", "COMPLIANCE", false, false),
		Entry("GOVERNANCE without the bypass, lifecycle's check", "GOVERNANCE", false, false),
		Entry("GOVERNANCE with the header alone", "GOVERNANCE", true, false),
	)
	It("VerifyDelete lets the delete through once the clock reaches that second", func() {
		Expect(objectlock.VerifyDelete(retainedUntil("COMPLIANCE", clockSecond), clockSecond, false, false)).To(Succeed())
	})
	It("RetentionChange holds a COMPLIANCE retention ending at the next second, 50 ns before it, refusing GOVERNANCE", func() {
		old := &meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: clockSecond}
		err := objectlock.RetentionChange(old, meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: clockSecond.Add(time.Hour)}, clockJustBefore, false)
		Expect(err).To(MatchError(objectlock.ErrAccessDenied))
	})
})
