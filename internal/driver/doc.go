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
package driver
