package op

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Placement is a placement rule resolved through the zone's parameters to
// the pools and namespace a bucket's data, index and multipart pieces use.
type Placement struct {
	// Rule is the rule resolved: a placement name and a storage class,
	// STANDARD where the rule named none. Compare it with a stored rule,
	// which keeps an empty class, by String.
	Rule          meta.PlacementRule
	DataPool      meta.Pool // for the rule's storage class
	IndexPool     meta.Pool
	DataExtraPool meta.Pool
	// Compression is the storage class's compression type, "" for none.
	Compression string
	InlineData  bool
}

//counterfeiter:generate . ZoneInfo

// ZoneInfo is the zone this gateway serves, resolved once at startup.
type ZoneInfo interface {
	// Release is the encoding release the cluster's own radosgw writes.
	Release() denc.Release
	Zone() meta.Zone
	ZoneGroup() meta.ZoneGroup
	ZoneParams() meta.ZoneParams
	Realm() meta.Realm
	Period() meta.Period
	// Placement resolves rule; the zero rule means the zonegroup's default.
	// A rule without a name takes the default placement, and its storage
	// class when the rule has none either; an empty storage class is
	// STANDARD. A placement the zone lacks, or a storage class the rule's
	// placement lacks, is ErrInvalidLocationConstraint.
	Placement(rule meta.PlacementRule) (Placement, error)
}
