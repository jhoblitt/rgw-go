// Package cksum holds radosgw's checksums as Tentacle stores them: the type
// ids, their names and properties, and the Cksum record that the
// user.rgw.cksum attr and a part record carry
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L41-L281 at v20.2.4).
// Squid's radosgw has no checksum code, so the record has one form on both
// releases. Nothing here computes a digest.
package cksum
