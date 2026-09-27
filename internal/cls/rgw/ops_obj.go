package rgw

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// ObjRemoveOp is rgw_cls_obj_remove_op, the input of obj_remove.
type ObjRemoveOp struct {
	KeepAttrPrefixes []string
}

// Encode mirrors rgw_cls_obj_remove_op::encode, ENCODE_START(1, 1).
func (o ObjRemoveOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.KeepAttrPrefixes, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeObjRemoveOp mirrors rgw_cls_obj_remove_op::decode, DECODE_START(1).
func DecodeObjRemoveOp(d *denc.Decoder) ObjRemoveOp {
	h := d.BeginStruct(1)
	o := ObjRemoveOp{KeepAttrPrefixes: denc.DecodeSlice(d, (*denc.Decoder).String)}
	d.EndStruct(h)
	return o
}

// ObjRemove adds obj_remove, which removes a data object; when it has xattrs
// under any of keepAttrPrefixes, the object is recreated empty with only
// those xattrs.
func ObjRemove(op radosclient.Execer, keepAttrPrefixes []string, r denc.Release) {
	op.Exec(Class, methodObjRemove, clsutil.Encode(ObjRemoveOp{KeepAttrPrefixes: keepAttrPrefixes}, r))
}

// StorePGVerOp is rgw_cls_obj_store_pg_ver_op, the input of obj_store_pg_ver.
type StorePGVerOp struct {
	Attr string
}

// Encode mirrors rgw_cls_obj_store_pg_ver_op::encode, ENCODE_START(1, 1).
func (o StorePGVerOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Attr)
	e.EndStruct(f)
}

// DecodeStorePGVerOp mirrors rgw_cls_obj_store_pg_ver_op::decode, DECODE_START(1).
func DecodeStorePGVerOp(d *denc.Decoder) StorePGVerOp {
	h := d.BeginStruct(1)
	o := StorePGVerOp{Attr: d.String()}
	d.EndStruct(h)
	return o
}

// ObjStorePGVer adds obj_store_pg_ver, which stores the object's PG version
// in the named xattr.
func ObjStorePGVer(op radosclient.Execer, attr string, r denc.Release) {
	op.Exec(Class, methodObjStorePGVer, clsutil.Encode(StorePGVerOp{Attr: attr}, r))
}

// CheckAttrsPrefixOp is rgw_cls_obj_check_attrs_prefix, the input of
// obj_check_attrs_prefix.
type CheckAttrsPrefixOp struct {
	CheckPrefix string
	FailIfExist bool
}

// Encode mirrors rgw_cls_obj_check_attrs_prefix::encode, ENCODE_START(1, 1).
func (o CheckAttrsPrefixOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.CheckPrefix)
	e.Bool(o.FailIfExist)
	e.EndStruct(f)
}

// DecodeCheckAttrsPrefixOp mirrors rgw_cls_obj_check_attrs_prefix::decode, DECODE_START(1).
func DecodeCheckAttrsPrefixOp(d *denc.Decoder) CheckAttrsPrefixOp {
	h := d.BeginStruct(1)
	var o CheckAttrsPrefixOp
	o.CheckPrefix = d.String()
	o.FailIfExist = d.Bool()
	d.EndStruct(h)
	return o
}

// ObjCheckAttrsPrefix adds obj_check_attrs_prefix, which fails the op with
// ECANCELED when an xattr under prefix exists and failIfExist is set, or when
// none does and it is not.
func ObjCheckAttrsPrefix(op radosclient.Execer, prefix string, failIfExist bool, r denc.Release) {
	op.Exec(Class, methodObjCheckAttrsPrefix, clsutil.Encode(CheckAttrsPrefixOp{CheckPrefix: prefix, FailIfExist: failIfExist}, r))
}

// CheckMtimeOp is rgw_cls_obj_check_mtime, the input of obj_check_mtime.
// ceph-dencoder does not register it, so no golden covers it.
type CheckMtimeOp struct {
	Mtime             time.Time
	Type              MtimeCheck
	HighPrecisionTime bool
}

// Encode mirrors rgw_cls_obj_check_mtime::encode, ENCODE_START(2, 1).
func (o CheckMtimeOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.Time(o.Mtime)
	e.U8(uint8(o.Type))
	e.Bool(o.HighPrecisionTime)
	e.EndStruct(f)
}

// DecodeCheckMtimeOp mirrors rgw_cls_obj_check_mtime::decode, DECODE_START(2).
func DecodeCheckMtimeOp(d *denc.Decoder) CheckMtimeOp {
	h := d.BeginStruct(2)
	var o CheckMtimeOp
	o.Mtime = d.Time()
	o.Type = MtimeCheck(d.U8())
	if h.Version >= 2 {
		o.HighPrecisionTime = d.Bool()
	}
	d.EndStruct(h)
	return o
}

// ObjCheckMtime adds obj_check_mtime, which fails the op with ECANCELED
// unless the object's mtime compares to mtime under typ; without
// highPrecision the comparison is in whole seconds.
func ObjCheckMtime(op radosclient.Execer, mtime time.Time, typ MtimeCheck, highPrecision bool, r denc.Release) {
	op.Exec(Class, methodObjCheckMtime, clsutil.Encode(CheckMtimeOp{Mtime: mtime, Type: typ, HighPrecisionTime: highPrecision}, r))
}
