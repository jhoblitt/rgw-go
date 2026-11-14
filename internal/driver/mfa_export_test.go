package driver

import "github.com/jhoblitt/rgw-go/internal/testutil/otptest"

// RFCSeed is RFC 6238 Appendix B's SHA-1 seed.
const RFCSeed = "12345678901234567890"

// RFCCode is the 8-digit RFC 6238 SHA-1 code of RFCSeed at TOTP step n. It
// comes from otptest, apart from hotpEqual, so that the specs check one with
// the other.
func RFCCode(n uint64) string { return otptest.HOTP([]byte(RFCSeed), n, 8) }

// EmptyKeyCode is RFCCode's code under a key of no bytes, which anyone can
// make.
func EmptyKeyCode(n uint64) string { return otptest.HOTP(nil, n, 8) }
