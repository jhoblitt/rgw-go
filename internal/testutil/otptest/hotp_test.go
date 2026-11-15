package otptest_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

// rfcKey is the secret of both RFCs' SHA-1 test vectors, "12345678901234567890".
var rfcKey = []byte("12345678901234567890")

var _ = Describe("HOTP", func() {
	// RFC 4226, Appendix D: the HOTP values of counts 0 to 9, six digits.
	DescribeTable("computes RFC 4226's test values",
		func(count uint64, want string) {
			Expect(otptest.HOTP(rfcKey, count, 6)).To(Equal(want))
		},
		Entry("count 0", uint64(0), "755224"),
		Entry("count 1", uint64(1), "287082"),
		Entry("count 2", uint64(2), "359152"),
		Entry("count 3", uint64(3), "969429"),
		Entry("count 4", uint64(4), "338314"),
		Entry("count 5", uint64(5), "254676"),
		Entry("count 6", uint64(6), "287922"),
		Entry("count 7", uint64(7), "162583"),
		Entry("count 8", uint64(8), "399871"),
		Entry("count 9", uint64(9), "520489"),
	)

	// RFC 6238, Appendix B, SHA-1: the TOTP of a time at a 30 second step from
	// 0, eight digits, whose counter is the time over 30.
	DescribeTable("computes RFC 6238's SHA-1 test values",
		func(unix uint64, want string) {
			Expect(otptest.HOTP(rfcKey, unix/30, 8)).To(Equal(want))
		},
		Entry("at 59 s", uint64(59), "94287082"),
		Entry("at 1111111109 s", uint64(1111111109), "07081804"),
		Entry("at 1111111111 s", uint64(1111111111), "14050471"),
		Entry("at 1234567890 s", uint64(1234567890), "89005924"),
		Entry("at 2000000000 s", uint64(2000000000), "69279037"),
		Entry("at 20000000000 s", uint64(20000000000), "65353130"),
	)

	It("keeps a leading zero", func() {
		Expect(otptest.HOTP(rfcKey, 1111111109/30, 6)).To(Equal("081804"))
	})
})
