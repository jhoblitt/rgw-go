package denc

import "time"

// Time appends t as utime_t or ceph::real_time: u32 seconds since the Unix
// epoch then u32 nanoseconds. The zero time.Time encodes as 0 seconds and 0
// nanoseconds, the epoch radosgw writes for an unset time. Seconds are
// otherwise truncated to 32 bits as Ceph does, so times before 1970 or after
// 2106 do not round-trip.
func (e *Encoder) Time(t time.Time) {
	if t.IsZero() {
		e.U32(0)
		e.U32(0)
		return
	}
	e.U32(uint32(t.Unix()))       //nolint:gosec // Ceph truncates seconds to u32
	e.U32(uint32(t.Nanosecond())) //nolint:gosec // Nanosecond is within [0, 1e9)
}

// Time reads u32 seconds then u32 nanoseconds and returns the instant in UTC.
// The epoch, 0 seconds and 0 nanoseconds, decodes as the zero time.Time so
// that an unset time stays IsZero in Go.
func (d *Decoder) Time() time.Time {
	sec := d.U32()
	nsec := d.U32()
	if d.err != nil || (sec == 0 && nsec == 0) {
		return time.Time{}
	}
	return time.Unix(int64(sec), int64(nsec)).UTC()
}
