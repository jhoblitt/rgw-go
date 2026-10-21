package lifecycle

import (
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/tags"
)

// expiryDateLayout is s3_expiration_header's "{:%a, %d %b %Y %T %Z}" of
// fmt::gmtime, whose zone is GMT.
const expiryDateLayout = "Mon, 02 Jan 2006 15:04:05 GMT"

// ExpirationHeader is s3_expiration_header at v19.2.7
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2750-L2888 at v19.2.7),
// the x-amz-expiration value for a request for the object name, whose stored
// tags are objTags and whose mtime is mtime, or "" for none. versionID is
// the request's own versionId, never the instance a read resolves through the
// OLH: get_s3_expiration_header passes the requested one, since the resolved
// instance would make every response of a versioned bucket look like a
// request for a version
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L112-L126 at v19.2.7).
// Over the enabled rules in rule_map order, whose prefix, the filter's when
// it has one and else the rule's, begins name, and whose tags the object
// holds, each rule's
// Expiration gives a date: its Date as the prefix map reads it, so none for
// one it holds as nil, or else the first midnight UTC at least its Days after
// mtime's whole second, the pass at which a worker on UTC first finds the
// object due. The earliest date wins,
// the first rule on a tie, and the value is `expiry-date="<the date, RFC
// 1123 in GMT>", rule-id="<the rule's ID>"`. A request that names a version,
// "null" included, gets none.
//
// That is v19.2.7's form, on both releases. v20.2.4's s3_expiration_header
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2817-L2952 at v20.2.4)
// gives a request that names a version a header, and takes the date of a key
// with an instance, a current version's among them, from the rule's
// NoncurrentVersionExpiration (docs/exclusions.md, "x-amz-expiration names
// the Expiration rule alone on Tentacle").
func ExpirationHeader(c Configuration, name, versionID string, objTags tags.Set, mtime time.Time) string {
	if versionID != "" {
		return ""
	}
	var earliest time.Time
	var id string
	found := false
	rules := c.ordered()
	for i := range rules {
		r := &rules[i]
		if !r.matches(name) || r.Filter.Tags.Len() > 0 && !hasAllTags(r.Filter.Tags, objTags) {
			continue
		}
		var date time.Time
		switch {
		case r.Expiration.Date != "":
			d := optionalISO8601(r.Expiration.Date)
			if d == nil {
				continue
			}
			date = *d
		case r.Expiration.Days != "":
			date = dayAfter(mtime, atoi(r.Expiration.Days))
		default:
			continue
		}
		if !found || earliest.After(date) {
			earliest, id, found = date, r.ID, true
		}
	}
	if !found {
		return ""
	}
	return `expiry-date="` + time.Unix(earliest.Unix(), 0).UTC().Format(expiryDateLayout) + `", rule-id="` + id + `"`
}

// AbortHeader is s3_multipart_abort_header
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2890-L2954 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2954-L3018 at v20.2.4)
// for an upload of the object name begun at mtime: over the enabled rules in
// rule_map order whose prefix begins name, whatever their tags, the earliest
// date their DaysAfterInitiation give, counted as ExpirationHeader counts
// Days, and its rule's ID, the first rule on a tie; ok is false when no rule
// gives one. The date keeps mtime's fraction of a second, as radosgw's
// real_time does, which x-amz-abort-date's format drops.
func AbortHeader(c Configuration, name string, mtime time.Time) (date time.Time, ruleID string, ok bool) {
	rules := c.ordered()
	for i := range rules {
		r := &rules[i]
		if !r.matches(name) || r.MPExpiration.Days == "" {
			continue
		}
		d := dayAfter(mtime, atoi(r.MPExpiration.Days))
		if !ok || date.After(d) {
			date, ruleID, ok = d, r.ID, true
		}
	}
	return date, ruleID, ok
}

// matches reports whether r is enabled and its prefix, the filter's when it
// has one and else the rule's, begins name.
func (r Rule) matches(name string) bool {
	prefix := r.Prefix
	if r.Filter.Prefix != "" {
		prefix = r.Filter.Prefix
	}
	return r.Status == "Enabled" && strings.HasPrefix(name, prefix)
}

// dayAfter is the first midnight UTC at or after mtime's whole second plus
// days, with mtime's fraction of a second kept for the comparison between
// rules, as radosgw's real_time keeps it. Both of radosgw's headers compute
// `mtime + (days × 86400 - to_time_t(mtime) % 86400 + 86400)` seconds, the
// midnight that begins mtime's day plus days and one, which names the day
// after the worker acts for an mtime in a day's first second
// (docs/exclusions.md, "Lifecycle headers name the midnight the worker acts
// at"). radosgw computes it in a double for x-amz-expiration and in an int
// for x-amz-abort-date, so Days past 106751 overflow the one's nanoseconds
// and DaysAfterInitiation past 24855 the other, and a date past real_time's
// last instant wraps; rgw-go gives the date the days name
// (docs/exclusions.md, "Lifecycle header dates past 24855 days or real_time
// are not wrapped").
func dayAfter(mtime time.Time, days int) time.Time {
	sec := realSecond(mtime) + int64(days)*secondsPerDay
	return time.Unix(sec+(secondsPerDay-sec%secondsPerDay)%secondsPerDay, int64(realTimeOf(mtime).Nanosecond())).UTC()
}
