package driver

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP, as liboath computes it for the otp class, is HMAC-SHA1
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

var _ op.MFAVerifier = (*Store)(nil)

const (
	// otpTokenLen is TOKEN_LEN, the length of the token OTP::check draws
	// for each check
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L65-L66 at v19.2.7 and v20.2.4).
	otpTokenLen = 16
	// otpCheckTries bounds the checks one verification sends. A check is
	// sent again only when the device's record changed after it was read,
	// and the class records at most ATTEMPTS_PER_WINDOW, five, checks of a
	// device in step_size seconds
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L108-L127 at v19.2.7 and v20.2.4),
	// so one try more outlasts them.
	otpCheckTries = 6
)

// errOTPRecordChanged is a check the guard refused because the device's
// record changed after it was read; the class checked nothing.
var errOTPRecordChanged = errors.New("the otp device's record changed while it was checked")

// VerifyMFA is RadosUser::verify_mfa once it has split x-amz-mfa into serial
// and pin, with RGWSI_Cls::MFA::check_mfa
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L298-L310 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L315-L327 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L46-L63 at v19.2.7 and v20.2.4).
// A serial not in the user's mfa_ids, or no user, is not verified and
// nothing is sent.
// Otherwise the device serial in the object "user:<uid>" of the zone's
// otp_pool checks pin through otp_check, then otp_get_result on a read op,
// both sent one CheckOp under one fresh token, and only OTP_CHECK_SUCCESS
// verifies. Any other result, and a device or object that is not there, is
// false with a nil error; a failed otp_check ends the check before the
// lookup, as OTP::check returns. Any other failure is the error. Nothing is
// logged but the device's id and the check's result, as check_mfa logs them,
// since the pin verifies until a check uses it or its window passes.
//
// It differs from radosgw where radosgw lets a code through or harms the
// cluster: a pin holding a NUL is not verified, since the class hands
// liboath val.c_str() and would verify a good code followed by a NUL and
// junk; a device of step 0 is not checked, since a good code faults the OSD
// there (docs/ceph-upstream-bugs.md, "cls_otp divides by a stored step_size
// that is never validated"); and a code the device may have accepted before
// is refused, which the class verifies again in the later steps of its
// window (unusedCode).
func (s *Store) VerifyMFA(ctx context.Context, user *meta.UserInfo, serial, pin string) (bool, error) {
	if user == nil || !slices.Contains(user.MFAIDs, serial) || strings.IndexByte(pin, 0) >= 0 {
		return false, nil
	}
	pool, err := s.pools.get(ctx, s.zone.Params.OTPPool)
	if err != nil {
		return false, fmt.Errorf("otp check: %w", err)
	}
	oid := otp.OTPObject(user.UserID)
	for range otpCheckTries {
		ok, err := s.checkOTP(ctx, pool, oid, serial, pin)
		switch {
		case errors.Is(err, errOTPRecordChanged):
			continue
		case errors.Is(err, radosclient.ErrNotFound):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("otp check of device %q in %s: %w", serial, oid, err)
		}
		return ok, nil
	}
	return false, fmt.Errorf("otp check of device %q in %s, %d tries: %w", serial, oid, otpCheckTries, errOTPRecordChanged)
}

// checkOTP is one try of VerifyMFA's check of pin against the device serial
// in the object oid. The check is guarded on the device's record as read, so
// that the class checks it against the last success unusedCode compares
// with; when the record changed in between, the guard cancels the check and
// checkOTP fails with errOTPRecordChanged.
func (s *Store) checkOTP(ctx context.Context, pool radosclient.Pool, oid, serial, pin string) (bool, error) {
	key := otp.InstanceKey(serial)
	before, raw, err := readOTPInstance(ctx, pool, oid, key)
	if err != nil {
		return false, err
	}
	if before.OTP.StepSize == 0 {
		slog.DebugContext(ctx, "otp check not sent: the device's step is 0", slog.String("otp_id", serial))
		return false, nil
	}
	req := otp.CheckOp{ID: serial, Val: pin, Token: randAlnum(otpTokenLen)}
	w := radosclient.NewWriteOp()
	w.OmapCmp(key, radosclient.CmpEQ, raw)
	otp.Check(w, req, s.release)
	if _, err = pool.Write(ctx, oid, w, radosclient.OpFlagNone); err != nil {
		if errors.Is(err, radosclient.ErrCanceled) {
			return false, errOTPRecordChanged
		}
		return false, err
	}
	r := radosclient.NewReadOp()
	lookup := otp.LookupCheck(r, req, s.release)
	afterRes := r.OmapGetValsByKeys([]string{key})
	if _, err = pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return false, err
	}
	check, err := lookup.Record()
	logged := []any{slog.String("otp_id", serial), slog.Int("result", int(check.Result))}
	if errors.Is(err, otp.ErrCheckRefused) {
		slog.DebugContext(ctx, "otp check", logged...)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	after, _, err := otpInstanceIn(afterRes, key)
	if err != nil {
		return false, err
	}
	if !unusedCode(before, pin, check.Timestamp, after.LastSuccess) {
		slog.DebugContext(ctx, "otp check refused: the device may have accepted the code before", logged...)
		return false, nil
	}
	slog.DebugContext(ctx, "otp check", logged...)
	return true, nil
}

// readOTPInstance reads the device's record under key in the object oid,
// with its bytes, which hold the device's seed and go nowhere but the guard
// of the check. A missing object or record is radosclient.ErrNotFound, as
// get_otp_instance answers otp_check for it.
func readOTPInstance(ctx context.Context, pool radosclient.Pool, oid, key string) (otp.Instance, []byte, error) {
	r := radosclient.NewReadOp()
	res := r.OmapGetValsByKeys([]string{key})
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return otp.Instance{}, nil, err
	}
	return otpInstanceIn(res, key)
}

func otpInstanceIn(res *radosclient.OmapResult, key string) (otp.Instance, []byte, error) {
	if res.Err != nil {
		return otp.Instance{}, nil, res.Err
	}
	raw, ok := res.Values[key]
	if !ok {
		return otp.Instance{}, nil, fmt.Errorf("otp device record: %w", radosclient.ErrNotFound)
	}
	d := denc.NewDecoder(raw)
	inst := otp.DecodeInstance(d)
	if err := d.Err(); err != nil {
		return otp.Instance{}, nil, fmt.Errorf("decoding otp device record: %w", err)
	}
	return inst, raw, nil
}

// unusedCode reports whether pin, which the class accepted at checked with
// before as the device's record, is a code the device has not accepted
// before. after is the device's last success once the check ran. before's
// step is not 0.
//
// verify stores the index d + c: c is the check's TOTP step, (secs -
// time_ofs) / step_size in unsigned int with secs the check's time cast to
// uint32_t, and d the window distance liboath found the code at, without its
// sign (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L129-L153 at v19.2.7 and v20.2.4).
// No code the device accepted is of a step above its index, nor an index
// above the last success, so a code of a step at or below the last success
// before the check may be one already used, which the class takes again in
// a later step of its window (docs/ceph-upstream-bugs.md, "cls_otp computes
// the replay index from an unsigned window distance"), and is refused.
//
// The code's step is c+d or c-d, and the device's seed, in before, tells
// which: a code found ahead is of step c+d, its index, which the class
// found above the last success, and verifies; one found behind is of step
// c-d. The record cannot narrow the last success: it keeps no codes, and
// its checks span step_size seconds, while a code can be used again up to
// twice the window in steps after an earlier success, so even the last
// success's own step would not rule out an earlier success of the code's
// step. So a fresh code found behind at or below the last success is
// refused too, such as each code of an authenticator a fraction of a step
// behind used at a cadence of two steps or less, which radosgw verifies and radosgw-admin
// mfa resync, fitting whole steps, does not cure (docs/exclusions.md, "An
// MFA code the device may have accepted before does not verify").
//
// All of it holds while 0 <= secs - time_ofs < 2^32, where the class's
// unsigned c is liboath's signed step. Only an admin writing time_ofs into
// the device's record leaves that range; the class's index then follows no
// code's step, the class verifies a used code itself, and its verdict
// stands.
//
// The walk goes no further than after allows: on the floors' class this
// check's own index is at or below it, and on a class with ceph/ceph#72253
// a walk a racing success cuts short falls back to the same answer. The
// success is taken as this check's own: the token's uniqueness keeps
// another check's record from answering the lookup. A code totpDistance
// does not find, where the class's liboath found it, falls back to the
// index the check stored (indexUnused).
func unusedCode(before otp.Instance, pin string, checked time.Time, after uint64) bool {
	var secs uint32
	if !checked.IsZero() {
		secs = uint32(checked.Unix()) //nolint:gosec // verify casts the check's time to uint32_t
	}
	dev := before.OTP
	if since := int64(secs) - int64(dev.TimeOfs); since < 0 || since >= 1<<32 {
		return true
	}
	c := uint64((secs - uint32(dev.TimeOfs)) / dev.StepSize) //nolint:gosec // the class's unsigned int arithmetic
	limit := uint64(dev.Window)
	if after >= c {
		limit = min(limit, after-c)
	}
	d, behind, ok := totpDistance(dev, secs, pin, limit)
	if !ok {
		return indexUnused(c, after, before.LastSuccess)
	}
	return !behind || d <= c && c-d > before.LastSuccess
}

// indexUnused is unusedCode from the index the check stored, after, for a
// code of step c-d or c+d where d is after-c: it refuses the code unless
// c-d is above lastSuccess, so it refuses a code found ahead within d steps
// of the last success too. A success recorded under another check's token
// before the record was read reads as used, as its index is at or below
// lastSuccess; one recorded later can pass, and only the token's
// uniqueness keeps it from answering this check's lookup.
//
// A class with ceph/ceph#72253 stores the code's own step as the index, so
// a code checked after its step leaves after below c. That class refuses a
// used code itself, and the code verifies. The floors' class leaves after
// below c only when its unsigned int sum wraps.
func indexUnused(c, after, lastSuccess uint64) bool {
	if after < c {
		return true
	}
	d := after - c
	return d <= c && c-d > lastSuccess
}

// totpDistance finds pin among the device's codes at secs as liboath's
// oath_totp_validate2 walks them: the TOTP step from the device's offset,
// then for each distance up to the window the step after it and, past
// distance 0, the step before, the first match winning, with as many digits
// as pin holds (liboath/totp.c:251-260 and :523-597 at oath-toolkit
// v2.6.15-2-g11a716b). It walks no further than limit, the distance the
// check's own index allows, and reports the distance and whether the code
// lies behind the step.
func totpDistance(dev otp.OTPInfo, secs uint32, pin string, limit uint64) (d uint64, behind, ok bool) {
	digits := len(pin)
	if digits < 6 || digits > 8 {
		return 0, false, false
	}
	nts := uint64((int64(secs) - int64(dev.TimeOfs)) / int64(dev.StepSize)) //nolint:gosec // liboath stores the signed quotient in a uint64_t
	for iter := range limit + 1 {
		if hotpEqual(dev.SeedBin, nts+iter, pin) {
			return iter, false, true
		}
		if iter > 0 && hotpEqual(dev.SeedBin, nts-iter, pin) {
			return iter, true, true
		}
	}
	return 0, false, false
}

// hotpEqual reports whether pin is the HOTP code of counter under secret, as
// liboath's _oath_hotp_generate2 makes it: RFC 4226's dynamic truncation of
// the HMAC-SHA1 of the big-endian counter, as len(pin) decimal digits
// (liboath/hotp.c:75-155 at oath-toolkit v2.6.15-2-g11a716b), compared in
// constant time.
func hotpEqual(secret []byte, counter uint64, pin string) bool {
	mac := hmac.New(sha1.New, secret)
	mac.Write(binary.BigEndian.AppendUint64(nil, counter))
	h := mac.Sum(nil)
	o := h[len(h)-1] & 0x0f
	v := binary.BigEndian.Uint32(h[o:o+4]) & 0x7fffffff
	mod := uint32(1)
	for range pin {
		mod *= 10
	}
	return hmac.Equal(fmt.Appendf(nil, "%0*d", len(pin), v%mod), []byte(pin))
}
