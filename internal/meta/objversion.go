package meta

import "github.com/jhoblitt/rgw-go/internal/denc"

// ObjVersion is cls_version's obj_version as meta stores it; it converts to
// and from version.ObjVersion by struct conversion.
type ObjVersion struct {
	Ver uint64 `json:"ver"`
	Tag string `json:"tag"`
}

// Encode mirrors obj_version::encode, ENCODE_START(1, 1).
func (v ObjVersion) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(v.Ver)
	e.String(v.Tag)
	e.EndStruct(f)
}

// DecodeObjVersion mirrors obj_version::decode, DECODE_START(1).
func DecodeObjVersion(d *denc.Decoder) ObjVersion {
	h := d.BeginStruct(1)
	v := ObjVersion{Ver: d.U64(), Tag: d.String()}
	d.EndStruct(h)
	return v
}
