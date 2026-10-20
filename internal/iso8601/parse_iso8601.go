package iso8601

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
)

// The strptime formats parse_iso8601 tries in order, glibc's %T spelled out
// as "%H:%M:%S" for strptime.Parse.
var (
	extendedFormats = []string{"%Y-%m-%dT%H:%M:%S", "%Y-%m-%d %H:%M:%S"}
	basicFormats    = []string{"%Y%m%dT%H%M%S"}
)

// fracScale is parse_iso8601's mul_table: the multiplier that turns a
// fraction of n digits into nanoseconds.
var fracScale = [...]uint64{0, 100000000, 10000000, 1000000, 100000, 10000, 1000, 100, 10, 1}

// ParseExtended is radosgw's parse_iso8601 with extended_format true
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L599-L659 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L612-L672 at v20.2.4),
// which parse_time, a POST policy's expiration, a Keystone token's expiry and
// a Swift TempURL's expiry are read with: glibc strptime's
// "%Y-%m-%dT%T", or else "%Y-%m-%d %T", whose space matches any run of white
// space, none included. Then, once C white space is trimmed from both ends of
// what remains, nothing, "Z", or "." and a fraction stringtoul takes followed
// by "Z".
//
// s is read as a C string, up to its first NUL. It returns the fields as
// strptime leaves them, which internal_timegm turns into seconds
// (strptime.Tm.Timegm), and the fraction as *pns receives it: stringtoul's
// value of the whole fraction, cut to 32 bits, times mul_table's scale for its
// first nine bytes, the product cut to 32 bits, so nine digits or fewer are
// nanoseconds and more are not. A caller that passes no pns still has its
// fraction checked, and ignores the value.
func ParseExtended(s string) (strptime.Tm, uint32, bool) {
	return parseISO8601(s, extendedFormats)
}

// ParseBasic is radosgw's parse_iso8601 with extended_format false
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L599-L659 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L612-L672 at v20.2.4),
// which the S3 signature checks read their request dates with: glibc
// strptime's
// "%Y%m%dT%H%M%S", then what remains as ParseExtended reads it.
func ParseBasic(s string) (strptime.Tm, uint32, bool) {
	return parseISO8601(s, basicFormats)
}

// cSpaces is what isspace takes in the C locale.
const cSpaces = " \t\n\v\f\r"

func parseISO8601(s string, formats []string) (strptime.Tm, uint32, bool) {
	s = rgwtext.CString(s)
	for _, f := range formats {
		tm, rest, ok := strptime.Parse(strptime.Tm{}, s, f)
		if !ok {
			continue
		}
		str := strings.Trim(rest, cSpaces)
		if str == "" || str == "Z" {
			return tm, 0, true
		}
		if str[0] != '.' || str[len(str)-1] != 'Z' {
			return strptime.Tm{}, 0, false
		}
		frac := str[1 : len(str)-1]
		v, ok := rgwtext.StringToUL(frac)
		if !ok {
			return strptime.Tm{}, 0, false
		}
		return tm, uint32(uint64(v) * fracScale[min(len(frac), 9)]), true //nolint:gosec // *pns is a uint32_t, and C++ cuts the product to it
	}
	return strptime.Tm{}, 0, false
}
