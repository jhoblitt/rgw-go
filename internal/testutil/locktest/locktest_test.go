package locktest_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// The goldens are the bytes internal/meta's hand-written goldens hold for
// the same values, written out from the encode functions at both floors.
var _ = Describe("the object-lock attrs written out by hand", func() {
	DescribeTable("RetentionAttr",
		func(mode string, until time.Time, want []byte) {
			Expect(locktest.RetentionAttr(mode, until)).To(Equal(want))
		},
		// 2030-01-02T03:04:05Z is 1893553445 seconds, 0x70dd5525; with 6 ns,
		// 1893553445000000006 ns, 0x1a4740d364713206.
		Entry("GOVERNANCE until 2030", "GOVERNANCE", time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC), []byte{
			2, 1, 30, 0, 0, 0,
			10, 0, 0, 0, 'G', 'O', 'V', 'E', 'R', 'N', 'A', 'N', 'C', 'E',
			0x25, 0x55, 0xdd, 0x70, 6, 0, 0, 0,
			0x06, 0x32, 0x71, 0x64, 0xd3, 0x40, 0x47, 0x1a,
		}),
		// real_time's last instant: 18446744073 seconds, cut to 1266874889,
		// 0x4b82fa09, and 709551615 ns, 0x2a4ae5ff; the u64 is all ones.
		Entry("COMPLIANCE until real_time's last instant", "COMPLIANCE", meta.MaxRealTime, []byte{
			2, 1, 30, 0, 0, 0,
			10, 0, 0, 0, 'C', 'O', 'M', 'P', 'L', 'I', 'A', 'N', 'C', 'E',
			0x09, 0xfa, 0x82, 0x4b, 0xff, 0xe5, 0x4a, 0x2a,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		}),
	)
	DescribeTable("LegalHoldAttr",
		func(status string, want []byte) {
			Expect(locktest.LegalHoldAttr(status)).To(Equal(want))
		},
		Entry("ON", "ON", []byte{1, 1, 6, 0, 0, 0, 2, 0, 0, 0, 'O', 'N'}),
		Entry("OFF", "OFF", []byte{1, 1, 7, 0, 0, 0, 3, 0, 0, 0, 'O', 'F', 'F'}),
	)
})
