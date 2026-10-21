package otp

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The methods an MFA check calls, with the flags CLS_INIT registers them
// with (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L564-L569 at v19.2.7 and v20.2.4).
// A check takes two because a write method cannot both record a failed
// attempt and return a failure, which would cancel the write, and a write op
// returns no output, as the comment on h_get_result_op says
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L543-L552 at v19.2.7 and v20.2.4).
const (
	MethodCheck     = "otp_check"      // RD|WR
	MethodGetResult = "otp_get_result" // RD
)

// ErrCheckRefused is a check whose result is not CheckSuccess, which
// check_mfa answers EACCES
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L46-L63 at v19.2.7 and v20.2.4).
var ErrCheckRefused = errors.New("otp: check refused")

// CheckOp is cls_otp_check_otp_op, the request of both otp_check and
// otp_get_result: the id of the device, the code to check against it, and
// the token naming the check.
//
// The code verifies until a check consumes it or its window passes, so
// Format, LogValue and MarshalJSON leave Val out. fmt still prints it from
// the fields, without calling a method, for a struct that holds a CheckOp in
// an unexported field, slog's text handler included, and for %p on a value
// that is not a pointer and %w on anything, which it reports as bad verbs;
// an encoder other than encoding/json, such as encoding/xml, writes it too.
// Callers keep a CheckOp off those paths.
type CheckOp struct {
	ID, Val, Token string
}

// MarshalJSON writes the check's id and token, never its code.
// cls_otp_check_otp_op has no dump, so no radosgw document fixes this form,
// and it is what slog's JSON handler writes for a CheckOp inside another
// value, where LogValue is not called.
func (o CheckOp) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}{o.ID, o.Token})
	if err != nil {
		return nil, fmt.Errorf("otp: encoding check: %w", err)
	}
	return b, nil
}

// Format prints the check's id and token, never its code, under every verb
// fmt hands it: a string verb keeps its flags, width and precision, %#v
// prints the Go form, and any other verb is reported as fmt reports a verb a
// type does not take.
func (o CheckOp) Format(s fmt.State, verb rune) {
	switch {
	case verb == 'v' && s.Flag('#'):
		_, _ = fmt.Fprintf(s, "otp.CheckOp{ID:%q, Token:%q}", o.ID, o.Token)
	case verb == 'v' || verb == 's' || verb == 'q' || verb == 'x' || verb == 'X':
		_, _ = fmt.Fprintf(s, fmt.FormatString(s, verb), o.redacted())
	default:
		_, _ = fmt.Fprintf(s, "%%!%c(otp.CheckOp=%s)", verb, o.redacted())
	}
}

// LogValue is the check for slog, its id and token without its code.
func (o CheckOp) LogValue() slog.Value {
	return slog.GroupValue(slog.String("id", o.ID), slog.String("token", o.Token))
}

func (o CheckOp) redacted() string {
	return fmt.Sprintf("{ID:%q Token:%q}", o.ID, o.Token)
}

// Encode mirrors cls_otp_check_otp_op::encode, ENCODE_START(1, 1).
func (o CheckOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.ID)
	e.String(o.Val)
	e.String(o.Token)
	e.EndStruct(f)
}

// DecodeCheckOp mirrors cls_otp_check_otp_op::decode, DECODE_START(1).
func DecodeCheckOp(d *denc.Decoder) CheckOp {
	h := d.BeginStruct(1)
	o := CheckOp{ID: d.String(), Val: d.String(), Token: d.String()}
	d.EndStruct(h)
	return o
}

// GetResultReply is cls_otp_get_result_reply, the otp_get_result reply.
type GetResultReply struct {
	Result OTPCheck
}

// Encode mirrors cls_otp_get_result_reply::encode, ENCODE_START(1, 1).
func (o GetResultReply) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Result.Encode(e, r)
	e.EndStruct(f)
}

// DecodeGetResultReply mirrors cls_otp_get_result_reply::decode,
// DECODE_START(1).
func DecodeGetResultReply(d *denc.Decoder) GetResultReply {
	h := d.BeginStruct(1)
	o := GetResultReply{Result: DecodeOTPCheck(d)}
	d.EndStruct(h)
	return o
}

// Check mirrors OTP::check's first call: otp_check checks req.Val against
// the device req.ID and records the outcome under req.Token, unless the
// device already holds five checks from the last step_size seconds, a window
// that slides with the OSD's clock rather than a TOTP step, when it records
// nothing (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L108-L127 at v19.2.7 and v20.2.4).
// It is a write method, so op is a write op, as Tentacle's OTP::check sends
// it (https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L71-L73 at v20.2.4).
// Squid's IoCtx::exec reaches the OSD as a write as well, since the OSD
// marks an op that calls a write method with set_class_write
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L197-L204 at v19.2.7 and v20.2.4).
// The method succeeds whether or not req.Val verifies, failing the op with
// ENOENT when the object holds no device with the id; LookupCheck reads the
// outcome.
//
// otp_get_result answers the newest check recorded under a token, so each
// check takes a token that no other check on the device shares, whether from
// this gateway, another one or radosgw: drawn from crypto/rand, as OTP::check
// draws its 16 characters from CryptoRandom
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L33-L51 at v19.2.7 and v20.2.4).
// Under a shared token, a check the class declined to record answers with
// the other check's result.
func Check(op radosclient.Execer, req CheckOp, r denc.Release) {
	op.Exec(Class, MethodCheck, clsutil.Encode(req, r))
}

// CheckLookup is a pending otp_get_result reply.
type CheckLookup struct {
	res *radosclient.ExecResult
}

// LookupCheck mirrors OTP::check's second call on a read op: it sends
// otp_get_result the same cls_otp_check_otp_op Check sent, as radosgw does
// and as the class decodes it, never the cls_otp_get_result_op named for the
// method, which holds no device id
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L76-L84 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L78-L86 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L483-L511 at v19.2.7 and v20.2.4).
func LookupCheck(op *radosclient.ReadOp, req CheckOp, r denc.Release) *CheckLookup {
	return &CheckLookup{res: op.Exec(Class, MethodGetResult, clsutil.Encode(req, r))}
}

// Record decodes the check once the op has run: the newest the device
// recorded under the token in the last step_size seconds, or, when there is
// none, the token with CheckUnknown. A check whose result is not
// CheckSuccess comes with ErrCheckRefused. The method's own failure is
// returned as it is, so an object holding no device with the id reads as
// radosclient.ErrNotFound.
func (l *CheckLookup) Record() (OTPCheck, error) {
	ret, err := clsutil.DecodeReply(l.res, Class, MethodGetResult, DecodeGetResultReply)
	if err != nil {
		return OTPCheck{}, err
	}
	if ret.Result.Result != CheckSuccess {
		return ret.Result, ErrCheckRefused
	}
	return ret.Result, nil
}
