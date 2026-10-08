// Package driver is the RADOS driver: one Store implementing every store
// interface in op over the radosclient seam, and the group its background
// workers run in.
//
// Open takes the encoding release from the cluster's require_osd_release,
// or from the caller's override, and the driver encodes every persistent
// type at that release, as the cluster's own radosgw writes it.
//
// Open then resolves the realm, period, zonegroup and zone the gateway
// serves from the root pools as radosgw's startup reads them, and only
// reads. radosgw creates a default zonegroup and zone it cannot find and
// writes a missing master zone back into a zonegroup; the driver does
// neither, so the zone must exist before the gateway starts, as it does
// under Rook, which names the realm, zonegroup and zone of every gateway
// and creates them first.
//
// The driver never creates a pool. radosgw's rgw_init_ioctx creates a
// missing pool and enables the rgw application on it
// (src/rgw/driver/rados/rgw_tools.cc:30-49 at v19.2.6, :31-50 at v20.2.4);
// under Rook every pool exists before the gateway starts, and the seam has
// no pool create, so here a missing pool is a configuration error.
//
// The driver shares its zone with radosgw and radosgw-admin, and carries
// the obligations that sharing makes (docs/exclusions.md, "Coexistence
// obligations independent of any exclusion"):
//
//   - The metadata cache is invalidated both ways. The control watches
//     take every notify on the notify.N objects for an invalidation of the
//     key it names, never for the payload it carries, and the driver sends
//     the record it wrote after each of its own metadata writes
//     (docs/ceph-upstream-bugs.md, "radosgw caches any control-pool
//     UPDATE_OBJ notify payload unchecked"). A lost watch is registered
//     again with a growing pause, for ever, the cache off meanwhile
//     ("radosgw aborts the process after 100 failed control-watch
//     re-registrations").
//   - A listing's index suggestions are sent as the cluster's radosgw
//     sends them: unguarded on Squid, behind the reshard guard on Tentacle
//     ("Squid does not guard listing-time index suggestions against
//     resharding").
//   - User stats are kept exact: bucket links, unlinks and the stats syncs
//     go through cls_user as radosgw's do, so radosgw-admin user stats and
//     both gateways' quota checks read the same totals.
//   - A shard count of zero or less is floored to one at Open, and no
//     modulo divides by a count unguarded ("radosgw faults on a zero
//     rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards").
//   - No pool, realm, zonegroup or zone is created, as above.
package driver
