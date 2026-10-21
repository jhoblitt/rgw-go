package lifecycle

import (
	"math"
	"time"
)

// secondsPerDay is secs_in_a_day
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L46-L47 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L46-L47 at v20.2.4).
const secondsPerDay = 86400

// Expired is obj_has_expired
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L272-L302 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L272-L302 at v20.2.4):
// whether an object of mtime is days old at now. With debug at 0 or less,
// rgw_lc_debug_interval's default, it is once local midnight of now in loc,
// round_to_day, is at least days × 86400 seconds after mtime's whole second.
// With a positive debug, each day is debug long and the age counts from now
// itself. A days × debug past a Duration's range is never reached. A nil
// loc is read as UTC.
func Expired(mtime time.Time, days int, now time.Time, debug time.Duration, loc *time.Location) bool {
	if debug > 0 {
		return now.Sub(time.Unix(realSecond(mtime), 0)) >= mulDuration(days, debug)
	}
	return roundToDay(now, loc).Unix()-realSecond(mtime) >= int64(days)*secondsPerDay
}

// expiresAt is the due time obj_has_expired gives the action's selection,
// mtime plus the days it counts.
func expiresAt(mtime time.Time, days int, debug time.Duration) time.Time {
	mtime = realTimeOf(mtime)
	if debug > 0 {
		return mtime.Add(mulDuration(days, debug))
	}
	return time.Unix(mtime.Unix()+int64(days)*secondsPerDay, int64(mtime.Nanosecond())).UTC()
}

// dateReached is radosgw's `ceph_clock_now() >= to_time_t(date)`: whether
// now has reached date's whole second.
func dateReached(date, now time.Time) bool {
	return !now.Before(time.Unix(date.Unix(), 0))
}

// roundToDay is utime_t::round_to_day
// (https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L218-L227 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/include/utime.h#L218-L227 at v20.2.4):
// localtime_r's fields of now with the time of day cleared, back through
// mktime, which keeps the tm_isdst localtime_r set. glibc's mktime gives the
// zone's own midnight when the zone has one that day and its
// daylight-saving flag there is now's. Otherwise, where the flags differ or
// the zone skips midnight, it reads midnight at the offset now has: New
// York's midnight on the day it springs forward is 04:00Z, not 05:00Z, and
// Cairo's, which its DST start at midnight skips, is 21:00Z, while Moscow's
// on the day it moved from +04 to +03, no flag set either side, is 20:00Z
// the day before, its true midnight. Where a zone skips midnight with no
// flag change, glibc answers -1, which round_to_day does not check
// (docs/ceph-upstream-bugs.md, "radosgw's lifecycle finds nothing due on a
// day whose local midnight a zone skips without a DST change"); rgw-go reads
// that midnight at now's offset too. Where midnight occurs twice with the
// same flag, glibc's answer follows its previous mktime call; rgw-go takes
// time.Date's choice, the later, which is glibc's in a fresh process. A nil
// loc is UTC, the zone of a gateway with TZ unset, as Rook runs it.
func roundToDay(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	y, m, d := local.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	if h, mi, sec := midnight.Clock(); midnight.IsDST() != local.IsDST() || h != 0 || mi != 0 || sec != 0 {
		_, offset := local.Zone()
		midnight = time.Date(y, m, d, 0, 0, 0, 0, time.FixedZone("", offset))
	}
	return midnight
}

// realSecond is real_clock::to_time_t of t; the zero time.Time is the epoch,
// as denc reads an unset real_time.
func realSecond(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// realTimeOf is t, or the epoch for the zero time.Time.
func realTimeOf(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t
}

// mulDuration is n × d, saturated at a Duration's bounds.
func mulDuration(n int, d time.Duration) time.Duration {
	p := int64(n) * int64(d)
	if n != 0 && (p/int64(n) != int64(d) || (n == -1 && d == math.MinInt64)) {
		if (n < 0) != (d < 0) {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	return time.Duration(p)
}
