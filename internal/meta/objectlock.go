package meta

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ObjectRetention is RGWObjectRetention (rgw_object_lock.h:151-197 at v19.2.6
// and v20.2.4): an object's retention mode and the date it is retained until.
type ObjectRetention struct {
	Mode        string
	RetainUntil time.Time
}

// Encode mirrors RGWObjectRetention::encode, ENCODE_START(2, 1): the mode,
// the date as a real_time, and the date again through round_trip_encode,
// which writes its u64 nanosecond count (include/encoding.h:393-420 at
// v19.2.6, :394-421 at v20.2.4) and so keeps the seconds past 2106 that the
// first form's u32 drops.
func (o ObjectRetention) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Mode)
	e.Time(o.RetainUntil)
	e.U64(realTimeNanos(o.RetainUntil))
	e.EndStruct(f)
}

// DecodeObjectRetention mirrors RGWObjectRetention::decode, DECODE_START(2):
// from version 2 the date is the nanosecond count's, whatever the first form
// said.
func DecodeObjectRetention(d *denc.Decoder) ObjectRetention {
	h := d.BeginStruct(2)
	var o ObjectRetention
	o.Mode = d.String()
	o.RetainUntil = d.Time()
	if h.Version >= 2 {
		o.RetainUntil = realTimeFromNanos(d.U64())
	}
	d.EndStruct(h)
	return o
}

// realTimeNanos is a real_time's count of nanoseconds since the epoch, which
// is a u64; the zero time.Time is the epoch, as denc writes it.
func realTimeNanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.Unix())*uint64(time.Second) + uint64(t.Nanosecond()) //nolint:gosec // a real_time's count is a u64, wrapping as C++ does
}

// realTimeFromNanos is the instant n nanoseconds after the epoch in UTC, the
// zero time.Time for the epoch itself, as denc reads an unset time.
func realTimeFromNanos(n uint64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	sec := n / uint64(time.Second)
	return time.Unix(int64(sec), int64(n-sec*uint64(time.Second))).UTC() //nolint:gosec // seconds of a u64 nanosecond count fit an int64
}

// ObjectLegalHold is RGWObjectLegalHold (rgw_object_lock.h:199-230 at
// v19.2.6 and v20.2.4): an object's legal hold status.
type ObjectLegalHold struct{ Status string }

// Encode mirrors RGWObjectLegalHold::encode, ENCODE_START(1, 1).
func (l ObjectLegalHold) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(l.Status)
	e.EndStruct(f)
}

// DecodeObjectLegalHold mirrors RGWObjectLegalHold::decode, DECODE_START(1).
func DecodeObjectLegalHold(d *denc.Decoder) ObjectLegalHold {
	h := d.BeginStruct(1)
	l := ObjectLegalHold{Status: d.String()}
	d.EndStruct(h)
	return l
}
