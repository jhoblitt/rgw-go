// Package cksum holds radosgw's checksums as Tentacle stores them: the type
// ids, their names and properties, the Cksum record that the user.rgw.cksum
// attr and a part record carry
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L41-L281 at v20.2.4),
// the digests, and the combination of a multipart upload's part checksums
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L31-L155 at v20.2.4).
// Squid's radosgw has no checksum code, so v20.2.4 is the reference on both
// releases: the record has one form, and the digests and combined values
// are Tentacle radosgw's, apart from the differences docs/exclusions.md
// records.
package cksum
