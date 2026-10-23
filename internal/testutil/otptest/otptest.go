// Package otptest keeps the seeds of otp devices out of spec failure output.
package otptest

import (
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega/format"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
)

// RedactDevices makes Gomega print every otp.OTPInfo as its GoString, which
// leaves out the seed and seed_bin, wherever the device sits in a value a
// failure prints, until the spec ends. Run it from a BeforeEach. Gomega's
// own printing reads a struct's fields, so a failure comparing a device, an
// otp_instance or a list of devices would print the seed otherwise.
func RedactDevices() {
	key := format.RegisterCustomFormatter(func(v any) (string, bool) {
		if d, ok := v.(otp.OTPInfo); ok {
			return d.GoString(), true
		}
		return "", false
	})
	DeferCleanup(format.UnregisterCustomFormatter, key)
}
