package otptest_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

var _ = Describe("RedactDevices", func() {
	const seed = "5345454453454352"
	device := func() otp.OTPInfo {
		return otp.OTPInfo{Type: otp.TypeTOTP, ID: "phone", Seed: seed, SeedType: otp.SeedHex, SeedBin: []byte("SEEDSECR"), StepSize: 30}
	}
	type record struct {
		Devices []otp.OTPInfo
		Device  *otp.OTPInfo
	}
	printed := func() string {
		d := device()
		return format.Object(record{Devices: []otp.OTPInfo{d}, Device: &d}, 0)
	}
	leaks := func(s string) bool { return strings.Contains(s, seed) || strings.Contains(s, "SEEDSECR") }

	It("is needed: Gomega alone prints a device's seed and seed_bin", func() {
		Expect(leaks(printed())).To(BeTrue())
	})

	When("run", func() {
		BeforeEach(otptest.RedactDevices)

		It("prints each device as its GoString, wherever it sits, without its seed or seed_bin", func() {
			out := printed()
			Expect(leaks(out)).To(BeFalse(), "the printed value carries a seed") //nolint:ginkgolinter // ContainSubstring would print the seed on a failure
			Expect(strings.Count(out, device().GoString())).To(Equal(2))
		})
	})
})
