// Package iso8601 reads and writes ISO 8601 dates as radosgw does: Parse and
// Format are ceph's from_iso_8601 and to_iso_8601, and ParseExtended and
// ParseBasic are radosgw's parse_iso8601 in its two formats, with its
// fractional seconds and its zone. It is the reader the packages that read
// these dates are to share: op, auth and policy still keep copies of their
// own, which are to switch to it.
package iso8601

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/strptime"
)

// isSpace is isspace in the C locale, which radosgw never leaves.
func isSpace(c byte) bool { return c == ' ' || '\t' <= c && c <= '\r' }

// Parse is ceph::from_iso_8601 with ws_terminates, its default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L51-L151 at v19.2.7 and v20.2.4),
// which reads object-lock dates. It reads a four-digit year of 1970 or later,
// then optionally "-MM", "-DD", "THH", ":MM", ":SS" and "." with one to nine
// fractional digits, each field two digits. A date may stop after any of its
// fields at the end of s or at C white space, whatever follows it; a time
// must end in "Z", which the end of s or C white space must follow. Out of
// range fields carry, as internal_timegm carries them, so month 13 is next
// January and second 60 the next minute.
//
// The time is UTC, its nanoseconds kept. A time that real_time's u64
// nanoseconds since the epoch cannot hold, one before the epoch or past
// meta.MaxRealTime, is refused, where from_iso_8601 wraps it modulo 2^64
// nanoseconds, 2600-01-01T00:00:00Z to 2015 and 1970-01-00 to 2554: its
// header documents that every time must be representable so
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.h#L14-L33 at v19.2.7 and v20.2.4).
func Parse(s string) (time.Time, bool) {
	p := 0
	digits := func(n int) (int, bool) {
		v := 0
		for range n {
			if p == len(s) || s[p] < '0' || s[p] > '9' {
				return 0, false
			}
			v = v*10 + int(s[p]-'0')
			p++
		}
		return v, true
	}
	dateEnd := func() bool { return p == len(s) || isSpace(s[p]) }
	timeEnd := func() bool { return p < len(s) && s[p] == 'Z' && (p+1 == len(s) || isSpace(s[p+1])) }
	year, ok := digits(4)
	if !ok || year < 1970 {
		return time.Time{}, false
	}
	month, day, hour, minute, sec := 1, 1, 0, 0, 0
	realTime := func(nsec int) (time.Time, bool) {
		t := strptime.Tm{Year: year, Mon: month - 1, Mday: day, Hour: hour, Min: minute, Sec: sec}.Timegm().Add(time.Duration(nsec))
		if t.Before(time.Unix(0, 0)) || t.After(meta.MaxRealTime) {
			return time.Time{}, false
		}
		return t, true
	}
	if dateEnd() {
		return realTime(0)
	}
	for _, field := range []struct {
		delim byte
		v     *int
		end   func() bool
	}{{'-', &month, dateEnd}, {'-', &day, dateEnd}, {'T', &hour, timeEnd}, {':', &minute, timeEnd}, {':', &sec, timeEnd}} {
		if p == len(s) || s[p] != field.delim {
			return time.Time{}, false
		}
		p++
		if *field.v, ok = digits(2); !ok {
			return time.Time{}, false
		}
		if field.end() {
			return realTime(0)
		}
	}
	if p == len(s) || s[p] != '.' {
		return time.Time{}, false
	}
	p++
	nsec, scale := 0, 100_000_000
	for range 9 {
		d, ok := digits(1)
		if !ok {
			return time.Time{}, false
		}
		nsec += d * scale
		scale /= 10
		if timeEnd() {
			return realTime(nsec)
		}
	}
	return time.Time{}, false
}

// Format is ceph::to_iso_8601 in its default form, YMDhmsn
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.h#L39-L43 at v19.2.7 and v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L153-L203 at v19.2.7 and v20.2.4):
// t in UTC as "YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ", nine fractional digits. The
// zero time.Time is the epoch, as denc reads an unset real_time. t is a
// real_time's instant, between the epoch and meta.MaxRealTime, whose years
// to_iso_8601 writes in four digits.
func Format(t time.Time) string {
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
