package driver

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// readConfig is the read path's options, read once at Open. The defaults in
// the comments are rgw.yaml.in's at v19.2.6 and v20.2.4.
type readConfig struct {
	chunk  uint64 // rgw_max_chunk_size, 4 MiB: what a prefetch reads of the head
	maxReq uint64 // rgw_get_obj_max_req_size, 4 MiB: the most one data read asks for
	window uint64 // rgw_get_obj_window_size, 16 MiB: the most the data reads in flight ask for
}

// loadReadConfig reads the read path's options. It refuses a zero chunk or
// request size, with which no read makes progress, and a window smaller than
// one request, which radosgw takes but then fails every data read larger
// than the window with EDEADLK (the aio throttles' get,
// rgw_aio_throttle.cc:40-42 at v19.2.6 and v20.2.4).
func loadReadConfig(conf *cephconf.Options) (readConfig, error) {
	var r reads
	c := readConfig{
		chunk:  readOption(&r, conf.Size, "rgw_max_chunk_size"),
		maxReq: readOption(&r, conf.Size, "rgw_get_obj_max_req_size"),
		window: readOption(&r, conf.Size, "rgw_get_obj_window_size"),
	}
	if r.err != nil {
		return readConfig{}, fmt.Errorf("reading the read options: %w", r.err)
	}
	if c.chunk == 0 || c.maxReq == 0 || c.window < c.maxReq {
		return readConfig{}, fmt.Errorf("driver: rgw_max_chunk_size %d, rgw_get_obj_max_req_size %d, rgw_get_obj_window_size %d: "+
			"a zero chunk or request size, or a window below one request, cannot read", c.chunk, c.maxReq, c.window)
	}
	return c, nil
}

// dataPool is rgw_get_obj_data_pool for an object outside the extra-data
// pool (driver/rados/rgw_obj_manifest.cc:412-430 at v19.2.6 and v20.2.4): the
// bucket's explicit data pool when it has one, else the data pool of the
// rule's placement for the rule's storage class
// (RGWZoneParams::get_head_data_pool, driver/rados/rgw_zone.h:285-308 at
// v19.2.6, :307-330 at v20.2.4), else that of the zonegroup's default
// placement for its storage class. ok is false when the zone has neither
// placement. radosgw's head-object helpers answer -EIO there, logging
// "probably misconfiguration" (get_obj_head_ref,
// driver/rados/rgw_rados.cc:2508-2512 at v19.2.6, :2616-2620 at v20.2.4),
// while its object stat drops the failure (docs/ceph-upstream-bugs.md).
func (s *Store) dataPool(rule meta.PlacementRule, bucket meta.BucketID) (pool meta.Pool, ok bool) {
	if bucket.ExplicitPlacement.DataPool.Name != "" {
		return bucket.ExplicitPlacement.DataPool, true
	}
	if rule != (meta.PlacementRule{}) {
		if pi, found := s.zone.Params.PlacementPools[rule.Name]; found {
			return classPool(pi, rule.StorageClass), true
		}
	}
	def := s.zone.ZoneGroup.DefaultPlacement
	pi, found := s.zone.Params.PlacementPools[def.Name]
	if !found {
		return meta.Pool{}, false
	}
	return classPool(pi, def.StorageClass), true
}

// classPool is RGWZonePlacementInfo::get_data_pool (rgw_zone_types.h:281-290
// at v19.2.6 and v20.2.4): the storage class's data pool, or STANDARD's for a
// class the placement does not list, so an object of a since-removed class
// still reads. A listed class without a data pool also takes STANDARD's,
// where radosgw takes an empty pool (docs/exclusions.md).
func classPool(pi meta.ZonePlacementInfo, class string) meta.Pool {
	if c, ok := pi.StorageClasses[class]; ok && c.DataPool != nil {
		return *c.DataPool
	}
	return deref(pi.StorageClasses[meta.StorageClassStandard].DataPool, meta.Pool{})
}

// objRef is a RADOS object of a bucket: its pool handle, which carries the
// locator when there is one, its name and its locator.
type objRef struct {
	pool radosclient.Pool
	oid  string
	loc  string
}

// rawRef resolves obj through rule as rgw_obj_to_raw does: the pool from
// dataPool, the name and locator as get_obj_bucket_and_oid_loc derives them.
// A placement that resolves no pool is UnknownError, the -EIO of radosgw's
// head-object helpers. A resolved pool without a name is InvalidArgument:
// radosgw opens it with create set, which librados refuses with EINVAL
// (RadosClient::pool_create, librados/RadosClient.cc:686-687 at v19.2.6,
// :688-689 at v20.2.4). A pool that does not exist fails with an error that
// wraps radosclient.ErrNotFound and names it.
func (s *Store) rawRef(ctx context.Context, rule meta.PlacementRule, obj meta.Obj) (objRef, error) {
	pool, ok := s.dataPool(rule, obj.Bucket)
	return s.openRef(ctx, resolvedPool{pool: pool, ok: ok, kind: "data pool", rule: rule}, obj, op.ScopeObject)
}

// resolvedPool is a placement's answer for one object: the pool, whether any
// placement resolved it, and, for errors, which pool of which rule it is.
type resolvedPool struct {
	pool meta.Pool
	ok   bool
	kind string
	rule meta.PlacementRule
}

// openRef is the part of rgw_obj_to_raw and rgw_get_rados_ref that follows
// the pool's resolution: no pool is the head-object helpers' -EIO,
// UnknownError; a pool without a name is librados's EINVAL,
// InvalidArgument; a failed open is op.FromRADOS under scope. The name and
// locator are get_obj_bucket_and_oid_loc's, and the handle carries the
// locator.
func (s *Store) openRef(ctx context.Context, p resolvedPool, obj meta.Obj, scope op.Scope) (objRef, error) {
	if !p.ok {
		return objRef{}, fmt.Errorf("%w: no %s for placement %q of bucket %s", op.ErrUnknown, p.kind, p.rule, obj.Bucket.Name)
	}
	if p.pool.Name == "" {
		return objRef{}, fmt.Errorf("%w: the %s for placement %q of bucket %s has no name", op.ErrInvalidArgument, p.kind, p.rule, obj.Bucket.Name)
	}
	h, err := s.pools.get(ctx, p.pool)
	if err != nil {
		return objRef{}, op.FromRADOS(err, scope)
	}
	stripe := meta.Stripe{Obj: obj}
	ref := objRef{pool: h, oid: stripe.OID(), loc: stripe.Locator()}
	if ref.loc != "" {
		ref.pool = h.WithLocator(ref.loc)
	}
	return ref, nil
}

// headRef is rawRef for the head object of key in rec's bucket, which the
// bucket's placement rule places.
func (s *Store) headRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (objRef, error) {
	return s.rawRef(ctx, rec.Info.PlacementRule, meta.Obj{Bucket: rec.Info.Bucket, Key: key})
}
