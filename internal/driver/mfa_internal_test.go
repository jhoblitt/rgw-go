package driver

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

var _ = Describe("an MFA code's one-time use", func() {
	BeforeEach(otptest.RedactDevices)

	// c is the TOTP step of RFC 6238's vector at T = 1111111109, the
	// check's step in every row.
	const c = uint64(0x23523EC)
	checkedAt := func() time.Time { return time.Unix(int64(c)*30+10, 0).UTC() } //nolint:gosec // c is far from overflow
	device := func() otp.OTPInfo {
		return otp.OTPInfo{Type: otp.TypeTOTP, ID: "rfc", SeedBin: []byte(RFCSeed), StepSize: 30, Window: 2}
	}
	record := func(lastSuccess uint64) otp.Instance { return otp.Instance{OTP: device(), LastSuccess: lastSuccess} }

	DescribeTable("from the step the device's seed finds the code at",
		func(code string, lastSuccess, after uint64, want bool) {
			Expect(unusedCode(record(lastSuccess), code, checkedAt(), after)).To(Equal(want))
		},
		Entry("the check's own step's code", RFCCode(c), c-5, c, true),
		Entry("a code one step behind, above the last success", RFCCode(c-1), c-5, c+1, true),
		Entry("a code one step behind, at the last success, which may have been used", RFCCode(c-1), c-1, c+1, false),
		Entry("a code two steps behind, under a last success one step behind the check", RFCCode(c-2), c-1, c+2, false),
		Entry("a code one step ahead after a success in the check's step, which the index alone cannot tell from a used one",
			RFCCode(c+1), c, c+1, true),
		Entry("a code two steps ahead, after a success in the check's step", RFCCode(c+2), c, c+2, true),
		Entry("ceph/ceph#72253's index, a code one step late", RFCCode(c-1), c-5, c-1, true),
		Entry("ceph/ceph#72253's index, a code two steps late", RFCCode(c-2), c-5, c-2, true),
		Entry("ceph/ceph#72253's index, a used code one step late", RFCCode(c-1), c-1, c-1, false),
	)

	DescribeTable("from the index the check stored, for a code the seed does not find",
		func(lastSuccess, after uint64, want bool) {
			Expect(indexUnused(c, after, lastSuccess)).To(Equal(want))
			Expect(unusedCode(record(lastSuccess), "00000000", checkedAt(), after)).To(Equal(want), "unusedCode falls back to it")
		},
		Entry("the floors' index, on time", c-5, c, true),
		Entry("the floors' index, one step late", c-5, c+1, true),
		Entry("the floors' index, a used code one step later", c-1, c+1, false),
		Entry("the floors' index, a code ahead within its distance of the last success", c, c+1, false),
		Entry("the floors' index, past twice the check's step", uint64(0), 2*c+1, false),
		Entry("ceph/ceph#72253's index, on time", c-5, c, true),
		Entry("ceph/ceph#72253's index, one step late", c-5, c-1, true),
		Entry("ceph/ceph#72253's index, two steps late", c-5, c-2, true),
	)

	// A code found one step behind, at the last success the class's own step
	// gives, under the clock secs and the offset ofs: the rule refuses it
	// wherever the class's step is liboath's.
	DescribeTable("outside 0 <= secs - time_ofs < 2^32, gives the class's verdict",
		func(secs uint32, ofs int32, want bool) {
			d := device()
			d.TimeOfs = ofs
			cls := uint64((secs - uint32(ofs)) / d.StepSize)              //nolint:gosec // the class's unsigned int arithmetic
			nts := uint64((int64(secs) - int64(ofs)) / int64(d.StepSize)) //nolint:gosec // liboath's signed step in a uint64_t
			inst := otp.Instance{OTP: d, LastSuccess: cls}
			Expect(unusedCode(inst, RFCCode(nts-1), time.Unix(int64(secs), 0).UTC(), cls+1)).To(Equal(want))
		},
		Entry("inside, where the rule refuses it", uint32(c*30+10), int32(0), false),
		Entry("an offset past the clock", uint32(c*30+10), int32(c*30+10010), true),
		Entry("an offset so far behind the clock that the class's step wraps", uint32(4_000_000_000), int32(-400_000_000), true),
	)

	It("finds a code at the distance and on the side liboath does, no further than its limit", func() {
		secs := uint32(checkedAt().Unix()) //nolint:gosec // c is far from overflow
		for _, want := range []struct {
			step   uint64
			d      uint64
			behind bool
		}{{c, 0, false}, {c + 1, 1, false}, {c - 1, 1, true}, {c + 2, 2, false}, {c - 2, 2, true}} {
			d, behind, ok := totpDistance(device(), secs, RFCCode(want.step), 2)
			Expect(ok).To(BeTrue(), "step %d", want.step)
			Expect([]any{d, behind}).To(Equal([]any{want.d, want.behind}), "step %d", want.step)
		}
		_, _, ok := totpDistance(device(), secs, RFCCode(c+3), 2)
		Expect(ok).To(BeFalse(), "past the window")
		_, _, ok = totpDistance(device(), secs, RFCCode(c-2), 1)
		Expect(ok).To(BeFalse(), "past the limit")
		_, _, ok = totpDistance(device(), secs, RFCCode(c)[2:], 2)
		Expect(ok).To(BeTrue(), "six digits, as liboath counts strlen(otp)")
		_, _, ok = totpDistance(device(), secs, RFCCode(c)[3:], 2)
		Expect(ok).To(BeFalse(), "five digits")
	})
})
