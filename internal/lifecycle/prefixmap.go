package lifecycle

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// TransitionAction is transition_action
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L467-L481 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L467-L481 at v20.2.4):
// a transition's days, -1 when it has a date instead, its date, nil when it
// has days or Op holds no date for the stored one, and its storage class,
// canonical, so an empty class is STANDARD.
type TransitionAction struct {
	Days         int
	Date         *time.Time
	StorageClass string
}

// Op is lc_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L484-L508 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L484-L508 at v20.2.4),
// a rule as the lifecycle worker applies it. The day counts are 0 when the
// rule sets none, read with atoi; NewerNoncurrent is atoi's int held in a
// uint64_t, so a negative count is a huge one, which never acts, and 0 is
// the count of a rule that sets none, which holds back no version. A nil
// pointer is an absent boost::optional. The transition maps are keyed by
// the storage class as the rule names it, and Tags is a copy of the rule's.
//
// SizeGT and SizeLT keep the sign a stored size was written with, and
// SizeMatches compares them signed. radosgw's stoull reads -n as 2^64 - n,
// so its LT -1 matches every object and its GT -1 none, the opposite of the
// sender's meaning (docs/exclusions.md, "The lifecycle prefix map reads a
// stored negative object size by its sign, and no Date past real_time"). A
// size stoull reads above math.MaxInt64 is held as math.MaxInt64, which no
// object reaches.
//
// A date is nil where from_iso_8601 refuses the stored text, and where it
// reads a time past real_time's last instant, which it wraps modulo 2^64
// nanoseconds into another instant. radosgw's PutBucketLifecycleConfiguration
// stores such a date when the wrap lands at midnight, and its worker acts at
// the wrapped instant (docs/ceph-upstream-bugs.md, "radosgw's lifecycle Date
// wraps past 2554 and expires at once"); a nil date names no instant to act
// at.
type Op struct {
	ID                string
	Enabled           bool
	DMExpiration      bool
	Expiration        int
	ExpirationDate    *time.Time
	NoncurExpiration  int
	NewerNoncurrent   uint64
	MPExpiration      int
	SizeGT            *int64
	SizeLT            *int64
	Tags              *tags.Set
	Transitions       map[string]TransitionAction
	NoncurTransitions map[string]TransitionAction
	Flags             uint32
}

// PrefixOp is one entry of the prefix map: an Op under the prefix its rule
// matches.
type PrefixOp struct {
	Prefix string
	Op     Op
}

// PrefixMap is the configuration's prefix_map, which radosgw builds as it
// decodes a stored configuration and as it adds each rule: one Op per rule,
// as _add_rule makes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L114-L171 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L114-L171 at v20.2.4),
// under the filter's prefix when it has one and else the rule's. The map is
// a std::multimap, so the ops are in prefix order, bytewise, and those
// under one prefix in rule_map order.
func (c Configuration) PrefixMap() []PrefixOp {
	rules := c.ordered()
	if len(rules) == 0 {
		return nil
	}
	m := make([]PrefixOp, len(rules))
	for i := range rules {
		r := &rules[i]
		prefix := r.Prefix
		if r.Filter.Prefix != "" {
			prefix = r.Filter.Prefix
		}
		m[i] = PrefixOp{Prefix: prefix, Op: newOp(r)}
	}
	slices.SortStableFunc(m, func(a, b PrefixOp) int { return cmp.Compare(a.Prefix, b.Prefix) })
	return m
}

func newOp(r *Rule) Op {
	op := Op{
		ID:           r.ID,
		Enabled:      r.Status == "Enabled",
		DMExpiration: r.DMExpiration,
		Flags:        r.Filter.Flags,
	}
	if r.Expiration.Days != "" {
		op.Expiration = atoi(r.Expiration.Days)
	}
	if r.Expiration.Date != "" {
		op.ExpirationDate = optionalISO8601(r.Expiration.Date)
	}
	if r.NoncurExpiration.Days != "" {
		op.NoncurExpiration = atoi(r.NoncurExpiration.Days)
		op.NewerNoncurrent = uint64(int64(atoi(r.NoncurExpiration.NewerNoncurrent))) //nolint:gosec // an int converted to uint64_t
	}
	if r.MPExpiration.Days != "" {
		op.MPExpiration = atoi(r.MPExpiration.Days)
	}
	if len(r.Transitions) > 0 {
		op.Transitions = make(map[string]TransitionAction, len(r.Transitions))
		for class, t := range r.Transitions {
			a := TransitionAction{Days: -1, StorageClass: canonicalClass(class)}
			if t.Days != "" {
				a.Days = atoi(t.Days)
			} else {
				a.Date = optionalISO8601(t.Date)
			}
			op.Transitions[class] = a
		}
	}
	if len(r.NoncurTransitions) > 0 {
		op.NoncurTransitions = make(map[string]TransitionAction, len(r.NoncurTransitions))
		for class, t := range r.NoncurTransitions {
			op.NoncurTransitions[class] = TransitionAction{
				Days:         atoi(t.Days),
				Date:         optionalISO8601(t.Date),
				StorageClass: canonicalClass(class),
			}
		}
	}
	if r.Filter.Tags.Len() > 0 {
		op.Tags = &tags.Set{Tags: slices.Clone(r.Filter.Tags.Tags)}
	}
	if r.Filter.SizeGT != "" {
		op.SizeGT = new(signedSize(r.Filter.SizeGT))
	}
	if r.Filter.SizeLT != "" {
		op.SizeLT = new(signedSize(r.Filter.SizeLT))
	}
	return op
}

// SizeMatches is pass_size_limit_checks
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L516-L545 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L524-L552 at v20.2.4)
// for an object of size bytes, each bound compared signed: larger than
// SizeGT and smaller than SizeLT, where set. So a negative SizeGT holds for
// every object and a SizeLT of 0 or less for none.
func (op Op) SizeMatches(size uint64) bool {
	if gt := op.SizeGT; gt != nil && *gt >= 0 && size <= uint64(*gt) {
		return false
	}
	if lt := op.SizeLT; lt != nil && (*lt <= 0 || size >= uint64(*lt)) {
		return false
	}
	return true
}

// canonicalClass is rgw_placement_rule::get_canonical_storage_class.
func canonicalClass(class string) string {
	return meta.PlacementRule{StorageClass: class}.CanonicalStorageClass()
}

// optionalISO8601 is from_iso_8601's boost::optional, carried fields rolled
// forward as internal_timegm rolls them: nil where it refuses s, and where
// it reads a time real_time cannot hold, which it wraps into another
// instant.
func optionalISO8601(s string) *time.Time {
	r, ok := readISO8601(s)
	if !ok || !realTime(r.t) {
		return nil
	}
	return &r.t
}

// cSpace is isspace in the C locale, which strtoull skips before a number.
const cSpace = " \t\n\v\f\r"

// signedSize is LCFilter::get_size_gt's read of a size
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L264-L278 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L264-L278 at v20.2.4),
// std::stoull, whose throw on no digits or ERANGE the getter turns into 0,
// but with the minus sign strtoull negates in unsigned arithmetic kept, and
// a value past math.MaxInt64 either way held at the bound.
func signedSize(s string) int64 {
	v, end, erange := rgwtext.Strtoull(s)
	switch {
	case erange || end == 0:
		return 0
	case strings.HasPrefix(strings.TrimLeft(s, cSpace), "-"):
		if m := -v; m < 1<<63 {
			return -int64(m) //nolint:gosec // m is below 2^63 here
		}
		return math.MinInt64
	case v > math.MaxInt64:
		return math.MaxInt64
	}
	return int64(v)
}
