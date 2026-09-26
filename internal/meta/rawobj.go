package meta

import "github.com/jhoblitt/rgw-go/internal/denc"

// RawObj is rgw_raw_obj: pool, oid, locator.
type RawObj struct {
	Pool Pool   `json:"pool"`
	OID  string `json:"oid"`
	Loc  string `json:"loc"`
}

// Encode mirrors rgw_raw_obj::encode, ENCODE_START(6, 6).
func (o RawObj) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(6, 6)
	o.Pool.Encode(e, r)
	e.String(o.OID)
	e.String(o.Loc)
	e.EndStruct(f)
}

// DecodeRawObj mirrors rgw_raw_obj::decode. Below version 6 the bytes are an
// rgw_obj written before rgw_raw_obj split from it, and are converted as
// rgw_raw_obj::decode_from_rgw_obj does.
//
// The C++ reads a DECODE_START(6) header, then seeks back and decodes an
// rgw_obj with DECODE_START_LEGACY_COMPAT_LEN(6, 3, 3). The two headers agree
// from version 3 on, so reading the legacy header once is equivalent there;
// below version 3 the C++ also checks bytes of the first field as if they
// were compat and length, which this decoder does not.
func DecodeRawObj(d *denc.Decoder) RawObj {
	h := d.BeginStructLegacy(6, 3, 3, 0)
	var o RawObj
	if h.Version < 6 {
		o = rawFromObj(decodeObjBody(d, h.Version))
	} else {
		o.Pool = DecodePool(d)
		o.OID = d.String()
		o.Loc = d.String()
	}
	d.EndStruct(h)
	return o
}

// rawFromObj is decode_from_rgw_obj: get_obj_bucket_and_oid_loc for the oid
// and locator, and the bucket's explicit data pool.
func rawFromObj(obj Obj) RawObj {
	o := RawObj{
		Pool: obj.Bucket.ExplicitPlacement.DataPool,
		OID:  prependMarker(obj.Bucket.Marker, obj.Key.OID()),
	}
	if loc := obj.Key.Locator(); loc != "" {
		o.Loc = prependMarker(obj.Bucket.Marker, loc)
	}
	return o
}

// prependMarker is prepend_bucket_marker: "<marker>_<oid>", or oid alone when
// either is empty.
func prependMarker(marker, oid string) string {
	if marker == "" || oid == "" {
		return oid
	}
	return marker + "_" + oid
}
