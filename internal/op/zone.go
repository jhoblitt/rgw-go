package op

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Placement is a placement rule resolved through the zone's parameters to
// the pools and namespace a bucket's data, index and multipart pieces use.
type Placement struct {
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
	Placement(rule meta.PlacementRule) (Placement, error)
}
