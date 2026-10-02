package driver

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// CaptureLog is captureLog for the external specs.
var CaptureLog = captureLog

// ReadAccount is readAccount for the external specs.
var ReadAccount = (*Store).readAccount

// OwnerBucketsObj is ownerBucketsObj for the external specs: the pool and
// oid of an owner's bucket list.
func OwnerBucketsObj(s *Store, owner meta.Owner) (meta.Pool, string) {
	o := s.ownerBucketsObj(owner)
	return o.pool, o.oid
}

// ZoneForTest is the zone a Store from NewStoreForTest serves.
type ZoneForTest struct {
	Params    meta.ZoneParams
	ZoneGroup meta.ZoneGroup
}

// ReadConfigForTest is the read path's options for NewStoreForTest:
// rgw_max_chunk_size, rgw_get_obj_max_req_size and rgw_get_obj_window_size.
type ReadConfigForTest struct{ Chunk, MaxReq, Window uint64 }

// NewStoreForTest builds a Squid Store over cluster that serves zone with
// cfg, skipping Open's zone resolution and control objects, so a spec can
// drive the data path over a counterfeiter cluster.
func NewStoreForTest(cluster radosclient.Cluster, zone ZoneForTest, cfg ReadConfigForTest) *Store {
	return &Store{
		cluster: cluster,
		release: denc.Squid,
		zone:    &zoneConfig{Params: zone.Params, ZoneGroup: zone.ZoneGroup},
		pools:   newPoolCache(cluster),
		readCfg: readConfig{chunk: cfg.Chunk, maxReq: cfg.MaxReq, window: cfg.Window},
	}
}

// DataPoolForTest is dataPool for the external specs.
func (s *Store) DataPoolForTest(rule meta.PlacementRule, bucket meta.BucketID) (meta.Pool, bool) {
	return s.dataPool(rule, bucket)
}
