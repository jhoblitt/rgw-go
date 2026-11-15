package otptest

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // the otp class's codes, as liboath computes them, are HMAC-SHA1 (RFC 4226, RFC 6238)
	"encoding/binary"
	"fmt"
)

// HOTP is the code of counter under secret that liboath's
// _oath_hotp_generate2 makes for the otp class: RFC 4226's dynamic truncation
// of the HMAC-SHA1 of the big-endian counter, as digits decimal digits
// (https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/hotp.c#L75-L155).
// A TOTP is the HOTP of the counter (time - start) / step (RFC 6238).
func HOTP(secret []byte, counter uint64, digits int) string {
	mac := hmac.New(sha1.New, secret)
	mac.Write(binary.BigEndian.AppendUint64(nil, counter))
	hs := mac.Sum(nil)
	offset := hs[len(hs)-1] & 0x0f
	s := binary.BigEndian.Uint32(hs[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, s%mod)
}
