package rgw

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ObjKey is cls_rgw_obj_key, rgw_obj_index_key: an object's name and version
// instance as the bucket index keys it. It is this package's own copy; the
// cls packages do not import meta.
type ObjKey struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
}

// Encode mirrors rgw_obj_index_key::encode, ENCODE_START(1, 1).
func (k ObjKey) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(k.Name)
	e.String(k.Instance)
	e.EndStruct(f)
}

// DecodeObjKey mirrors rgw_obj_index_key::decode, DECODE_START(1).
func DecodeObjKey(d *denc.Decoder) ObjKey {
	h := d.BeginStruct(1)
	var k ObjKey
	k.Name = d.String()
	k.Instance = d.String()
	d.EndStruct(h)
	return k
}

// EntryVer is rgw_bucket_entry_ver: the data pool and the object version the
// head write returned. Its C++ default has Pool -1, not the Go zero value;
// NewEntryVer returns it.
type EntryVer struct {
	Pool  int64  `json:"pool"`
	Epoch uint64 `json:"epoch"`
}

// NewEntryVer returns the rgw_bucket_entry_ver default: pool -1, epoch 0.
func NewEntryVer() EntryVer { return EntryVer{Pool: -1} }

// Encode mirrors rgw_bucket_entry_ver::encode, ENCODE_START(1, 1), whose
// fields are packed values.
func (v EntryVer) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	encodePacked(e, uint64(v.Pool)) //nolint:gosec // C++ casts the int64 to uint64 to size it
	encodePacked(e, v.Epoch)
	e.EndStruct(f)
}

// DecodeEntryVer mirrors rgw_bucket_entry_ver::decode, DECODE_START(1).
func DecodeEntryVer(d *denc.Decoder) EntryVer {
	h := d.BeginStruct(1)
	var v EntryVer
	v.Pool = int64(decodePacked(d)) //nolint:gosec // two's-complement reinterpretation is the wire format
	v.Epoch = decodePacked(d)
	d.EndStruct(h)
	return v
}

// encodePacked mirrors encode_packed_val. A value below 0x80 is one byte;
// otherwise a marker byte 0x80|n precedes the value as an n-byte integer.
// The bounds are the C++ ones, including its off-by-one: 0x10000 takes the
// two-byte form and is truncated to 0, and values from 0x1000001 up take
// eight bytes although four would hold them.
func encodePacked(e *denc.Encoder, v uint64) {
	switch {
	case v < 0x80:
		e.U8(uint8(v))
	case v < 0x100:
		e.U8(0x81)
		e.U8(uint8(v))
	case v <= 0x10000:
		e.U8(0x82)
		e.U16(uint16(v)) //nolint:gosec // C++ truncates 0x10000 to 0 here too
	case v <= 0x1000000:
		e.U8(0x84)
		e.U32(uint32(v))
	default:
		e.U8(0x88)
		e.U64(v)
	}
}

// decodePacked mirrors decode_packed_val. A marker other than 0x81, 0x82,
// 0x84 or 0x88 is malformed.
func decodePacked(d *denc.Decoder) uint64 {
	c := d.U8()
	if c < 0x80 {
		return uint64(c)
	}
	switch c &^ 0x80 {
	case 1:
		return uint64(d.U8())
	case 2:
		return uint64(d.U16())
	case 4:
		return uint64(d.U32())
	case 8:
		return d.U64()
	default:
		if d.Err() == nil {
			d.Fail(fmt.Errorf("%w: packed value marker %#x", denc.ErrMalformed, c))
		}
		return 0
	}
}

// relativeBelow is the second count under which utime_t::gmtime prints a
// time as raw seconds rather than a date, taking it for a duration.
const relativeBelow = 60 * 60 * 24 * 365 * 10

// dumpTime renders a ceph::real_time as encode_json of its utime_t does:
// "<sec>.<usec>" within ten years of the epoch, the zero time included, and
// an ISO 8601 UTC date with microseconds otherwise.
func dumpTime(t time.Time) json.RawMessage {
	var sec, nsec uint32
	if !t.IsZero() {
		sec = uint32(t.Unix())        //nolint:gosec // utime_t truncates seconds to u32
		nsec = uint32(t.Nanosecond()) //nolint:gosec // Nanosecond is within [0, 1e9)
	}
	var s string
	if sec < relativeBelow {
		s = fmt.Sprintf("%d.%06d", sec, nsec/1000)
	} else {
		s = time.Unix(int64(sec), int64(nsec)).UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(err) // a string always marshals
	}
	return b
}
