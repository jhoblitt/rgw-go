package driver

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238's SHA-1 vectors, which the otp class's liboath computes
	"encoding/binary"
	"fmt"
)

// RFCSeed is RFC 6238 Appendix B's SHA-1 seed.
const RFCSeed = "12345678901234567890"

// RFCCode is the 8-digit RFC 6238 SHA-1 code of RFCSeed at TOTP step n:
// RFC 4226's dynamic truncation of the HMAC of the big-endian counter. It
// is written apart from hotpEqual, so that the specs check one with the
// other.
func RFCCode(n uint64) string { return codeOf([]byte(RFCSeed), n) }

// EmptyKeyCode is RFCCode's code under a key of no bytes, which anyone can
// make.
func EmptyKeyCode(n uint64) string { return codeOf(nil, n) }

func codeOf(key []byte, n uint64) string {
	mac := hmac.New(sha1.New, key)
	mac.Write(binary.BigEndian.AppendUint64(nil, n))
	h := mac.Sum(nil)
	o := h[len(h)-1] & 0x0f
	return fmt.Sprintf("%08d", (binary.BigEndian.Uint32(h[o:o+4])&0x7fffffff)%100_000_000)
}
