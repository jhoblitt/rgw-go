package meta

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Time is a time.Time whose JSON form is RGW's dump format, "2006-01-02T15:04:05.000000Z".
type Time struct{ time.Time }

const (
	dumpLayout = "2006-01-02T15:04:05.000000Z"
	// relativeBelow is the second count under which utime_t::gmtime prints a
	// time as raw seconds rather than a date, taking it for a duration.
	relativeBelow = 60 * 60 * 24 * 365 * 10
)

// Gmtime is utime_t::gmtime (src/include/utime.h:247-277 at v19.2.6), the
// text encode_json gives a time through dump_stream: "<sec>.<usec>" for a
// time within ten years of the epoch, the zero time included, and an ISO
// 8601 UTC date with microseconds otherwise.
func (t Time) Gmtime() string {
	var sec, nsec uint32
	if !t.IsZero() {
		sec = uint32(t.Unix())        //nolint:gosec // utime_t truncates seconds to u32
		nsec = uint32(t.Nanosecond()) //nolint:gosec // Nanosecond is within [0, 1e9)
	}
	if sec < relativeBelow {
		return fmt.Sprintf("%d.%06d", sec, nsec/1000)
	}
	return time.Unix(int64(sec), int64(nsec)).UTC().Format(dumpLayout)
}

// MarshalJSON renders t as Gmtime does.
func (t Time) MarshalJSON() ([]byte, error) { return json.Marshal(t.Gmtime()) }

// UnmarshalJSON accepts either form MarshalJSON writes. Zero seconds and
// microseconds give the zero time.
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("meta: time: %w", err)
	}
	if !strings.Contains(s, "-") {
		secStr, usecStr, _ := strings.Cut(s, ".")
		sec, err := strconv.ParseUint(secStr, 10, 32)
		if err != nil {
			return fmt.Errorf("meta: time %q: %w", s, err)
		}
		var usec uint64
		if usecStr != "" {
			if usec, err = strconv.ParseUint(usecStr, 10, 32); err != nil {
				return fmt.Errorf("meta: time %q: %w", s, err)
			}
		}
		if sec == 0 && usec == 0 {
			*t = Time{}
			return nil
		}
		t.Time = time.Unix(int64(sec), int64(usec)*1000).UTC() //nolint:gosec // both parsed as u32
		return nil
	}
	v, err := time.Parse(dumpLayout, s)
	if err != nil {
		return fmt.Errorf("meta: time: %w", err)
	}
	t.Time = v
	return nil
}

// Encode writes t as ceph::real_time, u32 seconds then u32 nanoseconds.
func (t Time) Encode(e *denc.Encoder) { e.Time(t.Time) }

// DecodeTime reads a ceph::real_time.
func DecodeTime(d *denc.Decoder) Time { return Time{d.Time()} }
