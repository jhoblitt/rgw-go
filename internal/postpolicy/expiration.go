package postpolicy

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
)

// cSpaces is what C's isspace takes in the C locale.
const cSpaces = " \t\n\v\f\r"

// parseISO8601 is RGWPolicy::set_expires
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L132-L141 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L141-L150 at v20.2.4):
// parse_iso8601 in its extended format on the expiration as a C string
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L599-L659 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L612-L672 at v20.2.4),
// a date, "T" or white space, and a time, then, white space aside, nothing,
// "Z", or "." and a fraction stringtoul takes followed by "Z"; and the
// seconds internal_timegm makes of it, which ignore the fraction. glibc's %T
// is spelled out for strptime.Parse.
func parseISO8601(s string) (int64, bool) {
	s = rgwtext.CString(s)
	tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%Y-%m-%dT%H:%M:%S")
	if !ok {
		tm, rest, ok = strptime.Parse(strptime.Tm{}, s, "%Y-%m-%d %H:%M:%S")
	}
	if !ok {
		return 0, false
	}
	if zone := strings.Trim(rest, cSpaces); zone != "" && zone != "Z" {
		frac, dot := strings.CutPrefix(zone, ".")
		frac, z := strings.CutSuffix(frac, "Z")
		if !dot || !z {
			return 0, false
		}
		if _, ok := rgwtext.StringToUL(frac); !ok {
			return 0, false
		}
	}
	return tm.Timegm().Unix(), true
}
