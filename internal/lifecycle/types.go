package lifecycle

import "github.com/jhoblitt/rgw-go/internal/tags"

// Expiration is LCExpiration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L41-L104 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L41-L104 at v20.2.4),
// the shape a rule's Expiration, NoncurrentVersionExpiration and
// AbortIncompleteMultipartUpload all take. radosgw keeps every field as the
// text the document held and reads a number out of it only where it uses
// one, so a field holds whatever text was stored.
type Expiration struct {
	Days            string
	Date            string
	NewerNoncurrent string
}

// Transition is LCTransition
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L107-L169 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L107-L169 at v20.2.4),
// a Transition or NoncurrentVersionTransition, its fields text as
// Expiration's are.
type Transition struct {
	Days         string
	Date         string
	StorageClass string
}

// FlagArchiveZone is the LCFilter flag of radosgw's ArchiveZone extension,
// make_flag(LCFlagType::ArchiveZone): bit 0, as make_flag shifts 1 by the
// flag's ordinal less one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L191-L199 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L191-L199 at v20.2.4).
const FlagArchiveZone uint32 = 1 << 0

// Filter is LCFilter
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L187-L313 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L187-L313 at v20.2.4).
// SizeGT and SizeLT are text, as Expiration's fields are. A stored filter
// may carry flag bits radosgw names no flag for; they are kept.
type Filter struct {
	Prefix string
	Tags   tags.Set
	Flags  uint32
	SizeGT string
	SizeLT string
}

// Rule is LCRule
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L316-L464 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L316-L464 at v20.2.4).
// Transitions and NoncurTransitions are std::maps keyed by the storage class
// as the document named it. Status is Enabled or Disabled in every rule
// radosgw parses, but a stored rule holds whatever text was stored.
type Rule struct {
	ID                string
	Prefix            string
	Status            string
	Expiration        Expiration
	NoncurExpiration  Expiration
	MPExpiration      Expiration
	DMExpiration      bool
	Filter            Filter
	Transitions       map[string]Transition
	NoncurTransitions map[string]Transition
}

// Configuration is RGWLifecycleConfiguration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L510-L562 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L510-L562 at v20.2.4):
// its rule_map, a std::multimap from rule ID to rule, as Rules in the
// multimap's order, by ID bytewise and rules with the same ID in the order
// they were added. Every function of this package that builds a
// Configuration keeps that order, and Encode writes it whatever order a
// caller built Rules in.
type Configuration struct {
	Rules []Rule
}
