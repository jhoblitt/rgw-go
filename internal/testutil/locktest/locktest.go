// Package locktest writes out radosgw's object-lock attrs by hand, for the
// specs that check a store's bytes against radosgw's encoding rather than
// against the encoder that wrote them.
package locktest

import (
	"encoding/binary"
	"time"
)

// RetentionAttr is a user.rgw.object-retention as RGWObjectRetention::encode
// writes it: ENCODE_START(2, 1) and the u32 length of the rest, the mode as a
// u32 length and its bytes, the date as encode writes a real_time, u32
// seconds and u32 nanoseconds, then round_trip_encode's u64 nanoseconds
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L181-L187 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L176-L182 at v20.2.4).
func RetentionAttr(mode string, until time.Time) []byte {
	sec, nsec := until.Unix(), until.Nanosecond()
	body := binary.LittleEndian.AppendUint32(nil, uint32(len(mode))) //nolint:gosec // a short mode
	body = append(body, mode...)
	body = binary.LittleEndian.AppendUint32(body, uint32(sec))                                //nolint:gosec // the u32 cuts the seconds as radosgw's does
	body = binary.LittleEndian.AppendUint32(body, uint32(nsec))                               //nolint:gosec // nanoseconds of a second
	body = binary.LittleEndian.AppendUint64(body, uint64(sec)*1e9+uint64(nsec))               //nolint:gosec // real_time's u64 nanoseconds
	return append(binary.LittleEndian.AppendUint32([]byte{2, 1}, uint32(len(body))), body...) //nolint:gosec // a short body
}

// LegalHoldAttr is a user.rgw.object-legal-hold as RGWObjectLegalHold::encode
// writes it: ENCODE_START(1, 1) and the u32 length of the rest, then the
// status as a u32 length and its bytes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L219-L223 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L214-L218 at v20.2.4).
func LegalHoldAttr(status string) []byte {
	body := binary.LittleEndian.AppendUint32(nil, uint32(len(status))) //nolint:gosec // a short status
	body = append(body, status...)
	return append(binary.LittleEndian.AppendUint32([]byte{1, 1}, uint32(len(body))), body...) //nolint:gosec // a short body
}
