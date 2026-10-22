package otp

import (
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// InstanceKey is the omap key of the record of the device id in a user's
// MFA object: otp_key_prefix, "otp/", then the id, kept whole
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L171-L193 at v19.2.7 and v20.2.4).
func InstanceKey(id string) string { return "otp/" + id }

// Instance is otp_instance, the record the class keeps of one device under
// InstanceKey: the device, the checks of its codes from the last step_size
// seconds or more, and the index of its last successful check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L66-L95 at v19.2.7 and v20.2.4).
// No method of the class returns it; a client reads it with an omap read.
//
// LastSuccess is verify's index, the check's TOTP step plus the window
// distance liboath found the code at, without its sign, so a code found
// behind the check's step stores an index ahead of its own step
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L129-L153 at v19.2.7 and v20.2.4).
//
// The record holds the device's seed. Its fields are exported, so fmt
// prints OTP through OTPInfo's Format, and LogValue leaves the seed out;
// OTPInfo names the paths that print it anyway.
type Instance struct {
	OTP         OTPInfo
	LastChecks  []OTPCheck
	LastSuccess uint64
}

// Encode mirrors otp_instance::encode, ENCODE_START(1, 1).
func (i Instance) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	i.OTP.Encode(e, r)
	denc.EncodeSlice(e, i.LastChecks, func(e *denc.Encoder, c OTPCheck) { c.Encode(e, r) })
	e.U64(i.LastSuccess)
	e.EndStruct(f)
}

// DecodeInstance mirrors otp_instance::decode, DECODE_START(1).
func DecodeInstance(d *denc.Decoder) Instance {
	h := d.BeginStruct(1)
	i := Instance{
		OTP:         DecodeOTPInfo(d),
		LastChecks:  denc.DecodeSlice(d, DecodeOTPCheck),
		LastSuccess: d.U64(),
	}
	d.EndStruct(h)
	return i
}

// LogValue is the record for slog, its device without the seed.
func (i Instance) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("otp", i.OTP),
		slog.Int("last_checks", len(i.LastChecks)),
		slog.Uint64("last_success", i.LastSuccess),
	)
}
