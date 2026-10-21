package driver

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// lcWorkTime is rgw_lifecycle_work_time as should_work and
// schedule_next_start_time scan it: the local hour and minute the work
// window starts and ends at, as given, outside a day's range included.
type lcWorkTime struct{ startHour, startMinute, endHour, endMinute int }

// scanLCWorkTime scans s as sscanf(s, "%d:%d-%d:%d") does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2510-L2518 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2577-L2585 at v20.2.4),
// text after the fourth number ignored, and refuses a value radosgw would
// read undefined numbers from (docs/ceph-upstream-bugs.md). radosgw checks
// neither how many numbers sscanf assigned nor sets the others first, so a
// value of fewer than four numbers leaves the window to whatever the stack
// held. A number past int is undefined in sscanf itself, for which
// glibc stores a truncated value.
func scanLCWorkTime(s string) (lcWorkTime, error) {
	var f [4]int
	rest := s
	for i, sep := range [4]string{":", "-", ":", ""} {
		n, after, ok := scanInt(rest)
		if ok {
			after, ok = strings.CutPrefix(after, sep)
		}
		if !ok {
			return lcWorkTime{}, fmt.Errorf("option rgw_lifecycle_work_time: %q does not scan as %%d:%%d-%%d:%%d", s)
		}
		f[i], rest = n, after
	}
	return lcWorkTime{startHour: f[0], startMinute: f[1], endHour: f[2], endMinute: f[3]}, nil
}

// scanInt is scanf's %d: white space skipped, then an optional sign and at
// least one decimal digit, all the digits that follow taken. ok is false
// when no digit follows or the number does not fit an int.
func scanInt(s string) (n int, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	end := 0
	if end < len(s) && (s[end] == '+' || s[end] == '-') {
		end++
	}
	digits := end
	for end < len(s) && '0' <= s[end] && s[end] <= '9' {
		end++
	}
	if end == digits {
		return 0, s, false
	}
	v, err := strconv.ParseInt(s[:end], 10, 32)
	if err != nil {
		return 0, s, false
	}
	return int(v), s[end:], true
}

// lcWorkerCounts returns rgw_lc_max_worker and rgw_lc_max_wp_worker as the
// worker uses them, with an error-level log line naming the option and the
// value read for each it changes. A negative worker count is used as 0, no
// worker: radosgw reserves that many workers in a vector and throws at
// startup. A workpool size that is not positive is used as 1: radosgw
// narrows it to 16 bits, and a size that narrows to 0 divides by zero at its
// worker's first work item
// (RGWLC::start_processor and WorkPool,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2467-L2477 at v19.2.7
// and https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L832-L879 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2534-L2544 at v20.2.4
// and https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L853-L900 at v20.2.4;
// docs/ceph-upstream-bugs.md).
func lcWorkerCounts(workers, wpWorkers int64) (maxWorker, maxWPWorker int) {
	if workers < 0 {
		slog.Error("lifecycle worker count is negative; running no lifecycle worker",
			slog.String("option", "rgw_lc_max_worker"), slog.Int64("value", workers))
		workers = 0
	}
	if wpWorkers <= 0 {
		slog.Error("lifecycle workpool size is not positive; using 1",
			slog.String("option", "rgw_lc_max_wp_worker"), slog.Int64("value", wpWorkers))
		wpWorkers = 1
	}
	return int(workers), int(wpWorkers)
}
