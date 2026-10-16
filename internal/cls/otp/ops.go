package otp

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class and the methods its CLS_INIT registers that this file's client
// sends, with their flags
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L557-L575 at v19.2.7 and v20.2.4).
const (
	Class                = "otp"
	MethodSet            = "otp_set"          // RD|WR
	MethodGet            = "otp_get"          // RD
	MethodRemove         = "otp_remove"       // RD|WR
	MethodGetCurrentTime = "get_current_time" // RD
)

// OTPObject is the name of uid's MFA object, RGWSI_Cls::MFA::get_mfa_oid,
// which radosgw keeps in the zone's otp_pool
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.h#L56-L58 at v19.2.7 and v20.2.4).
func OTPObject(uid meta.UserID) string { //nolint:revive // the name the MFA callers share, beside OTPInfo
	return "user:" + uid.String()
}

// Set mirrors OTP::set: otp_set stores each device under its id, over one
// already there but keeping that one's record of checks, and parses its seed
// into seed_bin, failing the op with EINVAL for a seed that does not parse
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L301-L348 at v19.2.7 and v20.2.4).
// It is a write method, so op is a write op. OTP::create is Set with one
// device.
func Set(op radosclient.Execer, devs []OTPInfo, r denc.Release) {
	op.Exec(Class, MethodSet, clsutil.Encode(SetOp{Entries: devs}, r))
}

// Remove mirrors OTP::remove, which sends one id: otp_remove removes the
// devices ids names, skipping an id the object does not hold. It is a write
// method, so op is a write op.
func Remove(op radosclient.Execer, ids []string, r denc.Release) {
	op.Exec(Class, MethodRemove, clsutil.Encode(RemoveOp{IDs: ids}, r))
}

// GetResult is a pending otp_get reply.
type GetResult struct {
	res *radosclient.ExecResult
}

// GetAll mirrors OTP::get_all on a read op: otp_get with get_all and no ids.
func GetAll(op *radosclient.ReadOp, r denc.Release) *GetResult {
	return &GetResult{res: op.Exec(Class, MethodGet, clsutil.Encode(GetOp{GetAll: true}, r))}
}

// Devices decodes the devices once the op has run, each with the seed_bin
// the class parsed; an object without devices has none.
func (res *GetResult) Devices() ([]OTPInfo, error) {
	ret, err := clsutil.DecodeReply(res.res, Class, MethodGet, DecodeGetReply)
	return ret.FoundEntries, err
}

// TimeResult is a pending get_current_time reply.
type TimeResult struct {
	res *radosclient.ExecResult
}

// GetCurrentTime mirrors OTP::get_current_time on a read op, sending the
// empty request frame the class decodes.
func GetCurrentTime(op *radosclient.ReadOp, r denc.Release) *TimeResult {
	return &TimeResult{res: op.Exec(Class, MethodGetCurrentTime, clsutil.Encode(GetCurrentTimeOp{}, r))}
}

// Time decodes the OSD's clock once the op has run.
func (res *TimeResult) Time() (time.Time, error) {
	ret, err := clsutil.DecodeReply(res.res, Class, MethodGetCurrentTime, DecodeGetCurrentTimeReply)
	return ret.Time, err
}
