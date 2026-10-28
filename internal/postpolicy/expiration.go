package postpolicy

import "github.com/jhoblitt/rgw-go/internal/iso8601"

// parseISO8601 is RGWPolicy::set_expires
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L132-L141 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L141-L150 at v20.2.4):
// parse_iso8601 in its extended format on the expiration as a C string,
// iso8601.ParseExtended, and the seconds internal_timegm makes of it, which
// ignore the fraction.
func parseISO8601(s string) (int64, bool) {
	tm, _, ok := iso8601.ParseExtended(s)
	if !ok {
		return 0, false
	}
	return tm.Timegm().Unix(), true
}
