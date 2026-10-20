package objectlock

import (
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/iso8601"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The lengths of get_lock_until_date's periods, std::chrono::days and
// std::chrono::years, the Gregorian year of 365.2425 days.
const (
	secondsPerDay  = 86400
	secondsPerYear = 31556952
)

// The messages PutObjectRetention sets in s->err.message
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8413-L8429 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9348-L9364 at v20.2.4).
const (
	msgShortens       = "proposed retain-until date shortens an existing retention period and governance bypass check failed"
	msgToGovernance   = "can't change retention mode from COMPLIANCE to GOVERNANCE"
	msgFromGovernance = "can't change retention mode from GOVERNANCE without governance bypass"
)

// realSecond is real_clock::to_time_t of t, its whole seconds since the
// epoch; the zero time.Time is the epoch, as denc reads an unset real_time.
func realSecond(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// heldAt reports whether a retain-until date holds at now: radosgw's
// `to_time_t(date) > ceph_clock_now()`, a whole second after now's. radosgw
// compares the clock through utime_t's operator double
// (https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L230-L232 at v19.2.7 and v20.2.4),
// which reads a clock within about 119 ns of a whole second as that second;
// heldAt compares exactly.
func heldAt(until, now time.Time) bool { return realSecond(until) > now.Unix() }

// RetentionPeriodValid is RGWObjectLock::retention_period_valid
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L116-L121 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L112-L117 at v20.2.4):
// exactly one of the rule's days and years is positive.
func RetentionPeriodValid(r meta.DefaultRetention) bool { return (r.Years > 0) != (r.Days > 0) }

// lockUntil is LockUntil, and whether the date lies in real_time's range
// before it is clamped there.
func lockUntil(r meta.DefaultRetention, mtime time.Time) (time.Time, bool) {
	period := int64(r.Years) * secondsPerYear
	if r.Days > 0 {
		period = int64(r.Days) * secondsPerDay
	}
	nsec := 0
	if !mtime.IsZero() {
		nsec = mtime.Nanosecond()
	}
	t := time.Unix(realSecond(mtime)+period, int64(nsec)).UTC()
	switch {
	case t.After(meta.MaxRealTime):
		return meta.MaxRealTime, false
	case t.Before(time.Unix(0, 0)):
		return time.Unix(0, 0).UTC(), false
	}
	return t, true
}

// LockUntil is RGWObjectLock::get_lock_until_date
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L103-L111 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L87-L95 at v20.2.4)
// for a rule of r: mtime plus r's days of 86400 seconds when they are
// positive, else plus its years of 31,556,952 seconds. radosgw adds them in
// real_time's u64 nanoseconds, which wrap past meta.MaxRealTime, often to a
// date already past; LockUntil saturates there instead, and at the epoch for
// a negative period that reaches before it.
func LockUntil(r meta.DefaultRetention, mtime time.Time) time.Time {
	t, _ := lockUntil(r, mtime)
	return t
}

// PeriodFits reports whether r's period from now ends inside real_time's
// range, so that LockUntil(r, now) is the date the period gives and not a
// clamp.
func PeriodFits(r meta.DefaultRetention, now time.Time) bool {
	_, ok := lockUntil(r, now)
	return ok
}

// DecodeRetention decodes a user.rgw.object-retention attr strictly, as
// radosgw's callers that use the decode of a whole buffer do: a delete's
// verify_object_lock, PutObjectRetention's read of the existing retention,
// lifecycle's pass_object_lock_check and the lock headers of GET and HEAD.
// That decode, decode(T&, const bufferlist&), asserts that it read every byte
// (https://github.com/ceph/ceph/blob/v19.2.7/src/include/encoding.h#L631-L637 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/include/encoding.h#L632-L638 at v20.2.4),
// which ends radosgw's process. An attr that is short, or holds bytes past its
// encoding, is an *Error of ErrUndecodable.
func DecodeRetention(b []byte) (meta.ObjectRetention, error) {
	return decodeRetention(b, true)
}

// DecodeRetentionLenient decodes a user.rgw.object-retention attr as
// GetObjectRetention does, obj_retention.decode(iter) through an iterator
// that leaves bytes past the encoding unread
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8475-L8482 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9423-L9430 at v20.2.4).
// Only a short attr is an *Error of ErrUndecodable.
func DecodeRetentionLenient(b []byte) (meta.ObjectRetention, error) {
	return decodeRetention(b, false)
}

func decodeRetention(b []byte, strict bool) (meta.ObjectRetention, error) {
	d := denc.NewDecoder(b)
	r := meta.DecodeObjectRetention(d)
	if err := decoded(d, strict); err != nil {
		return meta.ObjectRetention{}, &Error{Err: ErrUndecodable, Detail: "decoding RGWObjectRetention: " + err.Error()}
	}
	return r, nil
}

// DecodeLegalHold decodes a user.rgw.object-legal-hold attr strictly, as
// DecodeRetention decodes a retention, for the three of DecodeRetention's
// callers that read a legal hold: a delete's verify_object_lock, lifecycle's
// pass_object_lock_check and the lock headers of GET and HEAD.
func DecodeLegalHold(b []byte) (meta.ObjectLegalHold, error) {
	return decodeLegalHold(b, true)
}

// DecodeLegalHoldLenient decodes a user.rgw.object-legal-hold attr as
// GetObjectLegalHold does, obj_legal_hold.decode(iter) through an iterator
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8579-L8586 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9547-L9554 at v20.2.4).
// Only a short attr is an *Error of ErrUndecodable.
func DecodeLegalHoldLenient(b []byte) (meta.ObjectLegalHold, error) {
	return decodeLegalHold(b, false)
}

func decodeLegalHold(b []byte, strict bool) (meta.ObjectLegalHold, error) {
	d := denc.NewDecoder(b)
	h := meta.DecodeObjectLegalHold(d)
	if err := decoded(d, strict); err != nil {
		return meta.ObjectLegalHold{}, &Error{Err: ErrUndecodable, Detail: "decoding RGWObjectLegalHold: " + err.Error()}
	}
	return h, nil
}

// decoded is the decoder's error, or, when strict, one for bytes left past
// the encoding.
func decoded(d *denc.Decoder, strict bool) error {
	if err := d.Err(); err != nil {
		return err
	}
	if n := d.Remaining(); strict && n > 0 {
		return fmt.Errorf("%d bytes past the encoding", n)
	}
	return nil
}

// VerifyDelete is verify_object_lock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1639-L1667 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1702-L1730 at v20.2.4),
// a delete's check of the version's attrs at now, in its order: a retention
// attr that DecodeRetention refuses is ErrUndecodable; a retain-until date
// whose whole second is after now's is ErrLocked, unless the mode is
// GOVERNANCE and both bypassHeader, x-amz-bypass-governance-retention, and
// bypassAllowed, s3:BypassGovernanceRetention, are set; then a legal-hold
// attr that DecodeLegalHold refuses is ErrUndecodable, and a status of ON is
// ErrLocked. With both bypass flags false it is lifecycle's
// pass_object_lock_check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L304-L349 at v19.2.7 and v20.2.4),
// nil where that passes and an error, either one, where it does not.
func VerifyDelete(attrs map[string][]byte, now time.Time, bypassHeader, bypassAllowed bool) error {
	if b, ok := attrs[meta.AttrObjectRetention]; ok {
		r, err := DecodeRetention(b)
		if err != nil {
			return err
		}
		if heldAt(r.RetainUntil, now) && (r.Mode != modeGovernance || !bypassHeader || !bypassAllowed) {
			return &Error{Err: ErrLocked, Detail: fmt.Sprintf("%.32q retention until %s", r.Mode, iso8601.Format(r.RetainUntil))}
		}
	}
	if b, ok := attrs[meta.AttrObjectLegalHold]; ok {
		h, err := DecodeLegalHold(b)
		if err != nil {
			return err
		}
		if h.Status == holdOn {
			return &Error{Err: ErrLocked, Detail: "legal hold ON"}
		}
	}
	return nil
}

// RetentionChange is PutObjectRetention's check of the version's existing
// retention old against proposed
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8403-L8430 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9338-L9365 at v20.2.4),
// the dates compared in whole seconds, in its order. bypass is the
// x-amz-bypass-governance-retention header and s3:BypassGovernanceRetention
// both. A proposed date earlier than old's needs old's mode GOVERNANCE and
// bypass, and then nothing more; otherwise the same mode passes, a change
// to GOVERNANCE is refused, and a change to any other mode needs bypass.
// Each refusal is an *Error of ErrAccessDenied with radosgw's message.
//
// No old retention passes, and so does one whose retain-until second is not
// after now's: a lapsed retention protects nothing, so its mode and date
// bind no change, where radosgw applies both checks to it.
func RetentionChange(old *meta.ObjectRetention, proposed meta.ObjectRetention, now time.Time, bypass bool) error {
	if old == nil || !heldAt(old.RetainUntil, now) {
		return nil
	}
	denied := func(msg string) error { return &Error{Err: ErrAccessDenied, Message: msg} }
	switch {
	case realSecond(proposed.RetainUntil) < realSecond(old.RetainUntil):
		if old.Mode != modeGovernance || !bypass {
			return denied(msgShortens)
		}
	case old.Mode == proposed.Mode:
	case proposed.Mode == modeGovernance:
		return denied(msgToGovernance)
	case !bypass:
		return denied(msgFromGovernance)
	}
	return nil
}
