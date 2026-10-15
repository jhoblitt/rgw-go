package meta

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// OLHInfo is RGWOLHInfo
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.h#L94-L116 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.h#L96-L118 at v20.2.4),
// which an OLH head holds in AttrOLHInfo: the version its key resolves to,
// and whether that version is a delete marker, which makes the key read as
// absent.
type OLHInfo struct {
	Target  Obj  `json:"target"`
	Removed bool `json:"removed"`
}

// Encode mirrors RGWOLHInfo::encode, ENCODE_START(1, 1).
func (o OLHInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Target.Encode(e, r)
	e.Bool(o.Removed)
	e.EndStruct(f)
}

// DecodeOLHInfo mirrors RGWOLHInfo::decode, DECODE_START(1).
func DecodeOLHInfo(d *denc.Decoder) OLHInfo {
	h := d.BeginStruct(1)
	o := OLHInfo{Target: DecodeObj(d), Removed: d.Bool()}
	d.EndStruct(h)
	return o
}

// OLHPendingInfo is RGWOLHPendingInfo
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.h#L118-L138 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.h#L120-L140 at v20.2.4),
// the value of an attr under AttrOLHPendingPrefix: when its modification
// began.
type OLHPendingInfo struct {
	Time time.Time
}

// Encode mirrors RGWOLHPendingInfo::encode, ENCODE_START(1, 1).
func (p OLHPendingInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Time(p.Time)
	e.EndStruct(f)
}

// DecodeOLHPendingInfo mirrors RGWOLHPendingInfo::decode, DECODE_START(1).
func DecodeOLHPendingInfo(d *denc.Decoder) OLHPendingInfo {
	h := d.BeginStruct(1)
	p := OLHPendingInfo{Time: d.Time()}
	d.EndStruct(h)
	return p
}

// MarshalJSON is RGWOLHPendingInfo::dump, the time as utime_t prints it.
func (p OLHPendingInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Time Time `json:"time"`
	}{Time{p.Time}})
}

const (
	// noUnderscoreAlnum is gen_rand_alphanumeric_no_underscore's table
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L96-L99 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L96-L99 at v20.2.4).
	noUnderscoreAlnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-."
	// lowerAlnum is gen_rand_alphanumeric_lower's table
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L62-L65 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L62-L65 at v20.2.4).
	lowerAlnum = "0123456789abcdefghijklmnopqrstuvwxyz"
)

// chooseFrom is choose_from
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L33-L42 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L33-L42 at v20.2.4):
// n random bytes, each reduced modulo the table's length. The byte is read
// unsigned; choose_from sign-extends a signed char first, which changes
// nothing for a table of 36 or 64, as both divide 2^32 - 256.
func chooseFrom(table string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	for i, c := range b {
		b[i] = table[int(c)%len(table)]
	}
	return string(b)
}

// instanceLen is OBJ_INSTANCE_LEN less the NUL: radosgw hands
// gen_rand_alphanumeric_no_underscore a buffer of OBJ_INSTANCE_LEN (32)
// bytes, and it draws one character fewer than its size
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L96-L99 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L96-L99 at v20.2.4),
// leaving the last byte for the NUL.
const instanceLen = 31

// NewInstance is RGWRados::gen_rand_obj_instance_name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8868-L8877 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9614-L9623 at v20.2.4),
// a new version's instance. Its table holds nothing a URL escapes, and no
// '_', which would end the instance early in its head's "_:<instance>_<name>".
func NewInstance() string { return chooseFrom(noUnderscoreAlnum, instanceLen) }

// olhTagLen is the length of every tag olh_init_modification_impl draws.
const olhTagLen = 32

// NewOLHTag is a tag olh_init_modification_impl
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7842-L7887 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8505-L8550 at v20.2.4)
// gives a head it makes an OLH: 32 characters of gen_rand_alphanumeric_lower,
// drawn alike for the AttrOLHIDTag and for the fresh AttrIDTag written with
// it.
func NewOLHTag() string { return chooseFrom(lowerAlnum, olhTagLen) }

// NewOLHOpTag is the op tag olh_init_modification_impl names a pending
// modification by, at NewOLHTag's lines: the second of now, as utime_t keeps
// it in 32 bits, in 16 hex digits, then gen_rand_alphanumeric_lower to 32
// characters. now must be the Time of the OLHPendingInfo stored under the
// name, as radosgw takes both from one clock read: check_pending_olh_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680 at v20.2.4)
// sweeps expired pending attrs in name order and stops at the first that has
// not expired, so the names must sort as the times they store do.
func NewOLHOpTag(now time.Time) string {
	prefix := fmt.Sprintf("%016x", uint32(now.Unix())) //nolint:gosec // utime_t truncates seconds to u32
	return prefix + chooseFrom(lowerAlnum, olhTagLen-len(prefix))
}
