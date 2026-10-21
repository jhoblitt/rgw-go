package fakerados

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP, as liboath computes it for the otp class, is HMAC-SHA1
	"encoding/binary"
	"fmt"
	"slices"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// otpCheckMethods are the methods OTPClassAt emulates of those that check a
// device's code, otp_check and otp_get_result, which the class registers
// CLS_METHOD_RD|CLS_METHOD_WR and CLS_METHOD_RD
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L564-L569 at v19.2.7 and v20.2.4),
// on the class's clock now, which each call reads once, as the class reads
// real_clock::now() once in otp_instance::check and once in find.
func otpCheckMethods(now func() time.Time) methodTable {
	return methodTable{
		otp.MethodCheck: {fn: noOutput(func(call *ClassCall) int32 {
			return otpCheck(call, now())
		}), write: true},
		otp.MethodGetResult: {fn: func(call *ClassCall) ([]byte, int32) {
			return otpGetResult(call, now())
		}},
	}
}

// otpAttemptsPerWindow is ATTEMPTS_PER_WINDOW, the checks a device records in
// step_size seconds
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L43 at v19.2.7 and v20.2.4).
const otpAttemptsPerWindow = 5

// otpCheck is otp_check_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L447-L481 at v19.2.7 and v20.2.4):
// the device the request's id names checks its code at now and is written
// back, unless it declined the check, when the method writes nothing. Either
// way it answers 0, whatever the code; otp_get_result reads the outcome.
func otpCheck(call *ClassCall, now time.Time) int32 {
	op, rval := decodeRequest(call.In, otp.DecodeCheckOp)
	if rval < 0 {
		return rval
	}
	inst, rval := otpGetInstance(call, op.ID)
	if rval < 0 {
		return rval
	}
	update, rval := otpCheckCode(&inst, op.Token, op.Val, now)
	if rval < 0 {
		return rval
	}
	if update {
		otpWriteInstance(call, inst)
	}
	return 0
}

// otpGetResult is otp_get_result
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L483-L511 at v19.2.7 and v20.2.4):
// it decodes the request as the cls_otp_check_otp_op otp_check takes, never
// the cls_otp_get_result_op named for it, and answers what find finds at
// now. The method is RD, so find's trim is not written back.
func otpGetResult(call *ClassCall, now time.Time) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, otp.DecodeCheckOp)
	if rval < 0 {
		return nil, rval
	}
	inst, rval := otpGetInstance(call, op.ID)
	if rval < 0 {
		return nil, rval
	}
	return encodeSquid(otp.GetResultReply{Result: otpFind(&inst, op.Token, now)}), 0
}

// realNanos is t as a ceph::real_time counts it: unsigned nanoseconds since
// the epoch
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_time.h#L66-L74 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/ceph_time.h#L63-L71 at v20.2.4).
// The zero time.Time is the epoch, as denc decodes a stored epoch to it.
func realNanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano()) //nolint:gosec // real_time's rep is uint64_t, so a time before the epoch wraps there too
}

// otpTrimExpired is otp_instance::trim_expired
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L98-L106 at v19.2.7 and v20.2.4):
// it drops the checks from the front while they are older than step_size
// seconds before now. The window slides with now, whatever TOTP step now
// falls in. Its arithmetic is real_time's, in unsigned nanoseconds.
func otpTrimExpired(i *otpInstance, now time.Time) {
	start := realNanos(now) - uint64(i.OTP.StepSize)*uint64(time.Second)
	n := 0
	for n < len(i.LastChecks) && realNanos(i.LastChecks[n].Timestamp) < start {
		n++
	}
	i.LastChecks = i.LastChecks[n:]
}

// otpCheckCode is otp_instance::check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L108-L127 at v19.2.7 and v20.2.4):
// after the trim, a device already holding five checks declines this one,
// reporting no update and leaving last_success alone, since verify never
// runs; otherwise it records the check under token with verify's result at
// now. A negative rval is otpVerify's.
func otpCheckCode(i *otpInstance, token, val string, now time.Time) (update bool, rval int32) {
	otpTrimExpired(i, now)
	if len(i.LastChecks) >= otpAttemptsPerWindow {
		return false, 0
	}
	ok, rval := otpVerify(i, now, val)
	if rval < 0 {
		return false, rval
	}
	result := otp.CheckFail
	if ok {
		result = otp.CheckSuccess
	}
	i.LastChecks = append(i.LastChecks, otp.OTPCheck{Token: token, Timestamp: now, Result: result})
	return true, 0
}

// otpVerify is otp_instance::verify
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L129-L153 at v19.2.7 and v20.2.4).
// It validates val up to its first NUL, as the class hands liboath
// val.c_str(), at the clock's seconds cast to uint32_t. A code liboath finds
// in the window has the index of liboath's unsigned distance plus the
// current step, in the class's unsigned int arithmetic, whichever way the
// code lies; one at or below last_success is refused, and a greater one
// becomes it. The class divides by the stored step_size, which liboath
// replaced only for itself, so a code that verifies on a device of step 0
// faults the OSD; the emulator answers EIO there, before anything is written.
func otpVerify(i *otpInstance, now time.Time, val string) (ok bool, rval int32) {
	secs := uint32(realNanos(now) / uint64(time.Second)) //nolint:gosec // the class casts to uint32_t
	result := oathTOTPValidate2(i.OTP.SeedBin, secs, i.OTP.StepSize, i.OTP.TimeOfs, i.OTP.Window, rgwtext.CString(val))
	if result < 0 {
		return false, 0
	}
	if i.OTP.StepSize == 0 {
		return false, -int32(syscall.EIO)
	}
	index := uint64(uint32(result) + (secs-uint32(i.OTP.TimeOfs))/i.OTP.StepSize) //nolint:gosec // the class's unsigned int arithmetic
	if index <= i.LastSuccess {
		return false, 0
	}
	i.LastSuccess = index
	return true, 0
}

// otpFind is otp_instance::find
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L155-L169 at v19.2.7 and v20.2.4):
// after the trim, the newest check carrying token, and otherwise token with
// OTP_CHECK_UNKNOWN at now.
func otpFind(i *otpInstance, token string, now time.Time) otp.OTPCheck {
	otpTrimExpired(i, now)
	for _, c := range slices.Backward(i.LastChecks) {
		if c.Token == token {
			return c
		}
	}
	return otp.OTPCheck{Token: token, Timestamp: now, Result: otp.CheckUnknown}
}

// The liboath results the class reads
// (https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/oath.h.in#L127-L146).
const (
	oathInvalidDigits int32 = -2
	oathInvalidOTP    int32 = -6
)

// oathTOTPValidate2 is liboath's oath_totp_validate2 as otpVerify calls it, with
// no otp_pos: oath_totp_validate4_callback over HMAC-SHA1 with strlen(otp)
// digits
// (https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/totp.c#L251-L264,
// https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/totp.c#L524-L598,
// the same from oath-toolkit 2.6.7 to 2.6.12). A step of 0 is 30 seconds.
// The step counter is the time from the offset divided in signed 64-bit
// arithmetic, then held unsigned. Distance 0 tries the counter itself, and
// each distance up to window the counter after it and then the one before,
// in unsigned 64-bit arithmetic; the result is the distance of the first
// match, whichever way it lies, held as an int, or OATH_INVALID_OTP. A code
// of fewer than 6 or more than 8 digits is OATH_INVALID_DIGITS.
func oathTOTPValidate2(secret []byte, now, step uint32, startOffset int32, window uint32, code string) int32 {
	if len(code) < 6 || len(code) > 8 {
		return oathInvalidDigits
	}
	if step == 0 {
		step = 30
	}
	nts := uint64((int64(now) - int64(startOffset)) / int64(step)) //nolint:gosec // liboath stores the signed quotient in a uint64_t
	for iter := uint32(0); ; iter++ {
		if oathHOTP(secret, nts+uint64(iter), len(code)) == code {
			return int32(iter) //nolint:gosec // liboath returns its unsigned iter as an int
		}
		if iter > 0 && oathHOTP(secret, nts-uint64(iter), len(code)) == code {
			return int32(iter) //nolint:gosec // liboath returns its unsigned iter as an int
		}
		if iter == window {
			return oathInvalidOTP
		}
	}
}

// oathHOTP is liboath's _oath_hotp_generate2 for HMAC-SHA1: RFC 4226's
// dynamic truncation of the HMAC of the big-endian counter, as digits
// decimal digits
// (https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/hotp.c#L75-L155).
func oathHOTP(secret []byte, counter uint64, digits int) string {
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
