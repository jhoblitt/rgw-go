// Package otp is the client of the RADOS object class "otp" (src/cls/otp),
// which keeps a user's MFA devices, each an otp_info_t with its TOTP seed,
// and the record of their recent checks in the omap of one object per user:
// OTPObject's "user:<uid>", in the zone's otp_pool. Request functions add one
// exec step to an op; a method that replies returns a result to decode once
// the op has run. Every request and reply is (1,1) at v19.2.7 and v20.2.4,
// where the class and its types are the same, so the release argument
// selects nothing yet.
//
// # Seeds are secrets
//
// A device's seed, and the seed_bin the class parses from it, let whoever
// holds them make the user's MFA codes. Set sends them to the OSD and
// Devices returns them, as radosgw's metadata section does, and OTPInfo's
// JSON carries the seed. Format, String, GoString and LogValue leave both
// out, but a struct holding devices in an unexported field, %p on a value
// that is not a pointer, %w, and slog's JSON handler still print them, so
// callers keep devices off those paths. The binary decoders' errors carry
// offsets and sizes alone, and UnmarshalJSON's name a member, never a value,
// when it is handed a document itself. Through json.Unmarshal or a
// json.Decoder, a malformed document fails with encoding/json's
// *json.SyntaxError, which can quote the seed, so a caller decoding devices
// that way replaces that error before it returns or logs it.
//
// # How radosgw composes the calls
//
// The otp metadata section's put, RGWSI_Cls::MFA::set_mfa with reset_obj
// set, is one write op on the object: a remove allowed to fail, a
// non-exclusive create, the version check, new version and mtime of
// prepare_mfa_write
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L65-L87 at v19.2.7 and v20.2.4),
// then Set with the document's devices. Its get, list_mfa, is one read op: a
// stat for the mtime, the version read, then GetAll (set_mfa and list_mfa,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L188-L245 at v19.2.7 and v20.2.4).
package otp
