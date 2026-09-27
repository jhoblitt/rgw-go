package version

import "github.com/jhoblitt/rgw-go/internal/denc"

// XattrName is VERSION_ATTR, the xattr the class stores the ObjVersion in.
const XattrName = "ceph.objclass.version"

// ObjVersion is obj_version; an object without one reads as the zero
// ObjVersion. Set stores whatever the caller sends, never initializing it.
// Inc on an unversioned object first writes Ver 1 with a random 24-character
// base64 tag, then increments, leaving 2. radosgw's metadata objects start at
// Ver 1 with a 24-character alphanumeric tag because
// RGWObjVersionTracker::generate_new_write_ver makes that version and sends it
// with set.
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

// Cond is VersionCond, the comparison a condition applies to the stored version.
type Cond uint32

// The VersionCond values. CondEQ compares both the counter and the tag; the
// ordering conditions compare only the counter.
const (
	CondNone  Cond = 0
	CondEQ    Cond = 1
	CondGT    Cond = 2
	CondGE    Cond = 3
	CondLT    Cond = 4
	CondLE    Cond = 5
	CondTagEQ Cond = 6
	CondTagNE Cond = 7
)

// Condition is obj_version_cond: the stored version must satisfy Cond against Ver.
type Condition struct {
	Ver  ObjVersion
	Cond Cond
}

// Encode mirrors obj_version_cond::encode, ENCODE_START(1, 1), the condition as a u32.
func (c Condition) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	c.Ver.Encode(e, r)
	e.U32(uint32(c.Cond))
	e.EndStruct(f)
}

// DecodeCondition mirrors obj_version_cond::decode, DECODE_START(1). Any
// condition value is kept, as the C++ cast to the enum keeps it.
func DecodeCondition(d *denc.Decoder) Condition {
	h := d.BeginStruct(1)
	c := Condition{Ver: DecodeObjVersion(d), Cond: Cond(d.U32())}
	d.EndStruct(h)
	return c
}

// SetOp is cls_version_set_op, the "set" request.
type SetOp struct {
	Objv ObjVersion
}

// Encode mirrors cls_version_set_op::encode, ENCODE_START(1, 1).
func (o SetOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Objv.Encode(e, r)
	e.EndStruct(f)
}

// DecodeSetOp mirrors cls_version_set_op::decode, DECODE_START(1).
func DecodeSetOp(d *denc.Decoder) SetOp {
	h := d.BeginStruct(1)
	o := SetOp{Objv: DecodeObjVersion(d)}
	d.EndStruct(h)
	return o
}

// IncOp is cls_version_inc_op, the "inc" and "inc_conds" request. The class
// ignores Objv and checks only Conds.
type IncOp struct {
	Objv  ObjVersion
	Conds []Condition
}

// Encode mirrors cls_version_inc_op::encode, ENCODE_START(1, 1).
func (o IncOp) Encode(e *denc.Encoder, r denc.Release) { encodeConds(e, r, o.Objv, o.Conds) }

// DecodeIncOp mirrors cls_version_inc_op::decode, DECODE_START(1).
func DecodeIncOp(d *denc.Decoder) IncOp {
	objv, conds := decodeConds(d)
	return IncOp{Objv: objv, Conds: conds}
}

// CheckOp is cls_version_check_op, the "check_conds" request, the same shape as IncOp.
type CheckOp struct {
	Objv  ObjVersion
	Conds []Condition
}

// Encode mirrors cls_version_check_op::encode, ENCODE_START(1, 1).
func (o CheckOp) Encode(e *denc.Encoder, r denc.Release) { encodeConds(e, r, o.Objv, o.Conds) }

// DecodeCheckOp mirrors cls_version_check_op::decode, DECODE_START(1).
func DecodeCheckOp(d *denc.Decoder) CheckOp {
	objv, conds := decodeConds(d)
	return CheckOp{Objv: objv, Conds: conds}
}

func encodeConds(e *denc.Encoder, r denc.Release, objv ObjVersion, conds []Condition) {
	f := e.BeginStruct(1, 1)
	objv.Encode(e, r)
	denc.EncodeSlice(e, conds, func(e *denc.Encoder, c Condition) { c.Encode(e, r) })
	e.EndStruct(f)
}

func decodeConds(d *denc.Decoder) (ObjVersion, []Condition) {
	h := d.BeginStruct(1)
	objv := DecodeObjVersion(d)
	conds := denc.DecodeSlice(d, DecodeCondition)
	d.EndStruct(h)
	return objv, conds
}

// ReadRet is cls_version_read_ret, the "read" reply.
type ReadRet struct {
	Objv ObjVersion
}

// Encode mirrors cls_version_read_ret::encode, ENCODE_START(1, 1).
func (o ReadRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Objv.Encode(e, r)
	e.EndStruct(f)
}

// DecodeReadRet mirrors cls_version_read_ret::decode, DECODE_START(1).
func DecodeReadRet(d *denc.Decoder) ReadRet {
	h := d.BeginStruct(1)
	o := ReadRet{Objv: DecodeObjVersion(d)}
	d.EndStruct(h)
	return o
}
