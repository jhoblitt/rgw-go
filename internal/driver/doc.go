// Package driver is the RADOS driver: one Store implementing every store
// interface in op over the radosclient seam, and the group its background
// workers run in.
//
// Open takes the encoding release from the cluster's require_osd_release,
// or from the caller's override, and the driver encodes every persistent
// type at that release, as the cluster's own radosgw writes it.
//
// The driver never creates a pool. radosgw's rgw_init_ioctx creates a
// missing pool and enables the rgw application on it
// (src/rgw/driver/rados/rgw_tools.cc:30-49 at v19.2.6, :31-50 at v20.2.4);
// under Rook every pool exists before the gateway starts, and the seam has
// no pool create, so here a missing pool is a configuration error.
package driver
