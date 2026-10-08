package user

import (
	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The account methods the class registers (cls_user.cc:749-762 at v19.2.6
// and v20.2.4, which do not differ).
const (
	methodAccountResourceAdd  = "account_resource_add"
	methodAccountResourceGet  = "account_resource_get"
	methodAccountResourceRm   = "account_resource_rm"
	methodAccountResourceList = "account_resource_list"
)

// AccountResource is cls_user_account_resource (cls_user_types.h:239-264),
// one entry of an account's index: keyed by the name lower-cased, listed by
// the path, with metadata the resource type defines.
type AccountResource struct {
	Name, Path string
	Metadata   []byte
}

// Encode mirrors cls_user_account_resource::encode, ENCODE_START(1, 1).
func (a AccountResource) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(a.Name)
	e.String(a.Path)
	e.Bytes32(a.Metadata)
	e.EndStruct(f)
}

// DecodeAccountResource mirrors cls_user_account_resource::decode, DECODE_START(1).
func DecodeAccountResource(d *denc.Decoder) AccountResource {
	h := d.BeginStruct(1)
	a := AccountResource{Name: d.String(), Path: d.String(), Metadata: d.Bytes32()}
	d.EndStruct(h)
	return a
}

// AccountHeader is cls_user_account_header (cls_user_types.h:219-236), the
// omap header of an account index object: how many entries it holds.
type AccountHeader struct{ Count uint32 }

// Encode mirrors cls_user_account_header::encode, ENCODE_START(1, 1).
func (h AccountHeader) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U32(h.Count)
	e.EndStruct(f)
}

// DecodeAccountHeader mirrors cls_user_account_header::decode, DECODE_START(1).
func DecodeAccountHeader(d *denc.Decoder) AccountHeader {
	h := d.BeginStruct(1)
	a := AccountHeader{Count: d.U32()}
	d.EndStruct(h)
	return a
}

// ResourceMetadata is rgwrados::users::resource_metadata
// (driver/rados/users.h:67-85 at v19.2.6 and v20.2.4), the metadata of an
// account users index entry: the user's id without its tenant.
type ResourceMetadata struct{ UserID string }

// Encode mirrors resource_metadata::encode, ENCODE_START(1, 1).
func (m ResourceMetadata) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(m.UserID)
	e.EndStruct(f)
}

// DecodeResourceMetadata mirrors resource_metadata::decode, DECODE_START(1).
func DecodeResourceMetadata(d *denc.Decoder) ResourceMetadata {
	h := d.BeginStruct(1)
	m := ResourceMetadata{UserID: d.String()}
	d.EndStruct(h)
	return m
}

// AccountResourceAddOp is cls_user_account_resource_add_op (cls_user_ops.h:267-290).
type AccountResourceAddOp struct {
	Entry     AccountResource
	Exclusive bool
	Limit     uint32
}

// Encode mirrors cls_user_account_resource_add_op::encode, ENCODE_START(1, 1).
func (o AccountResourceAddOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Entry.Encode(e, r)
	e.Bool(o.Exclusive)
	e.U32(o.Limit)
	e.EndStruct(f)
}

// DecodeAccountResourceAddOp mirrors cls_user_account_resource_add_op::decode, DECODE_START(1).
func DecodeAccountResourceAddOp(d *denc.Decoder) AccountResourceAddOp {
	h := d.BeginStruct(1)
	o := AccountResourceAddOp{Entry: DecodeAccountResource(d), Exclusive: d.Bool(), Limit: d.U32()}
	d.EndStruct(h)
	return o
}

// AccountResourceGetOp is cls_user_account_resource_get_op (cls_user_ops.h:292-309).
type AccountResourceGetOp struct{ Name string }

// Encode mirrors cls_user_account_resource_get_op::encode, ENCODE_START(1, 1).
func (o AccountResourceGetOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.EndStruct(f)
}

// DecodeAccountResourceGetOp mirrors cls_user_account_resource_get_op::decode, DECODE_START(1).
func DecodeAccountResourceGetOp(d *denc.Decoder) AccountResourceGetOp {
	h := d.BeginStruct(1)
	o := AccountResourceGetOp{Name: d.String()}
	d.EndStruct(h)
	return o
}

// AccountResourceGetRet is cls_user_account_resource_get_ret (cls_user_ops.h:311-328).
type AccountResourceGetRet struct{ Entry AccountResource }

// Encode mirrors cls_user_account_resource_get_ret::encode, ENCODE_START(1, 1).
func (o AccountResourceGetRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Entry.Encode(e, r)
	e.EndStruct(f)
}

// DecodeAccountResourceGetRet mirrors cls_user_account_resource_get_ret::decode, DECODE_START(1).
func DecodeAccountResourceGetRet(d *denc.Decoder) AccountResourceGetRet {
	h := d.BeginStruct(1)
	o := AccountResourceGetRet{Entry: DecodeAccountResource(d)}
	d.EndStruct(h)
	return o
}

// AccountResourceRmOp is cls_user_account_resource_rm_op (cls_user_ops.h:330-347).
type AccountResourceRmOp struct{ Name string }

// Encode mirrors cls_user_account_resource_rm_op::encode, ENCODE_START(1, 1).
func (o AccountResourceRmOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.EndStruct(f)
}

// DecodeAccountResourceRmOp mirrors cls_user_account_resource_rm_op::decode, DECODE_START(1).
func DecodeAccountResourceRmOp(d *denc.Decoder) AccountResourceRmOp {
	h := d.BeginStruct(1)
	o := AccountResourceRmOp{Name: d.String()}
	d.EndStruct(h)
	return o
}

// AccountResourceListOp is cls_user_account_resource_list_op (cls_user_ops.h:349-372).
type AccountResourceListOp struct {
	Marker, PathPrefix string
	MaxEntries         uint32
}

// Encode mirrors cls_user_account_resource_list_op::encode, ENCODE_START(1, 1).
func (o AccountResourceListOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Marker)
	e.String(o.PathPrefix)
	e.U32(o.MaxEntries)
	e.EndStruct(f)
}

// DecodeAccountResourceListOp mirrors cls_user_account_resource_list_op::decode, DECODE_START(1).
func DecodeAccountResourceListOp(d *denc.Decoder) AccountResourceListOp {
	h := d.BeginStruct(1)
	o := AccountResourceListOp{Marker: d.String(), PathPrefix: d.String(), MaxEntries: d.U32()}
	d.EndStruct(h)
	return o
}

// AccountResourceListRet is cls_user_account_resource_list_ret (cls_user_ops.h:374-397).
type AccountResourceListRet struct {
	Entries   []AccountResource
	Truncated bool
	Marker    string
}

// Encode mirrors cls_user_account_resource_list_ret::encode, ENCODE_START(1, 1).
func (o AccountResourceListRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.Entries, func(e *denc.Encoder, a AccountResource) { a.Encode(e, r) })
	e.Bool(o.Truncated)
	e.String(o.Marker)
	e.EndStruct(f)
}

// DecodeAccountResourceListRet mirrors cls_user_account_resource_list_ret::decode, DECODE_START(1).
func DecodeAccountResourceListRet(d *denc.Decoder) AccountResourceListRet {
	h := d.BeginStruct(1)
	o := AccountResourceListRet{Entries: denc.DecodeSlice(d, DecodeAccountResource), Truncated: d.Bool(), Marker: d.String()}
	d.EndStruct(h)
	return o
}

// AccountResourceAdd mirrors cls_user_account_resource_add
// (cls_user.cc:520-579): the entry is keyed by its name lower-cased, so
// names that differ only in case share one. An existing entry is
// overwritten unless exclusive, which is EEXIST; a new one when the
// header's count has reached limit is EUSERS, and otherwise counts.
func AccountResourceAdd(op radosclient.Execer, entry AccountResource, exclusive bool, limit uint32, r denc.Release) {
	op.Exec(class, methodAccountResourceAdd, clsutil.Encode(AccountResourceAddOp{Entry: entry, Exclusive: exclusive, Limit: limit}, r))
}

// AccountResourceRm mirrors cls_user_account_resource_rm
// (cls_user.cc:616-661): a name the index lacks is ENOENT; a removed one
// leaves the count one lower, never below zero.
func AccountResourceRm(op radosclient.Execer, name string, r denc.Release) {
	op.Exec(class, methodAccountResourceRm, clsutil.Encode(AccountResourceRmOp{Name: name}, r))
}

// AccountResourceGetResult is a pending "account_resource_get" reply.
type AccountResourceGetResult struct {
	res *radosclient.ExecResult
}

// AccountResourceGet mirrors cls_user_account_resource_get
// (cls_user.cc:581-614): the entry under name lower-cased, ENOENT when the
// index lacks it.
func AccountResourceGet(op *radosclient.ReadOp, name string, r denc.Release) *AccountResourceGetResult {
	return &AccountResourceGetResult{res: op.Exec(class, methodAccountResourceGet, clsutil.Encode(AccountResourceGetOp{Name: name}, r))}
}

// Entry decodes the entry.
func (res *AccountResourceGetResult) Entry() (AccountResource, error) {
	ret, err := clsutil.DecodeReply(res.res, class, methodAccountResourceGet, DecodeAccountResourceGetRet)
	return ret.Entry, err
}

// AccountResourceListResult is a pending "account_resource_list" reply.
type AccountResourceListResult struct {
	res *radosclient.ExecResult
}

// AccountResourceList mirrors cls_user_account_resource_list
// (cls_user.cc:663-720): at most min(max, 1000) raw entries after marker,
// of which those whose path starts with pathPrefix are returned. The
// marker is the last raw key read, whether or not its entry matched, and
// the truncation the omap read's.
func AccountResourceList(op *radosclient.ReadOp, marker, pathPrefix string, maxEntries uint32, r denc.Release) *AccountResourceListResult {
	in := clsutil.Encode(AccountResourceListOp{Marker: marker, PathPrefix: pathPrefix, MaxEntries: maxEntries}, r)
	return &AccountResourceListResult{res: op.Exec(class, methodAccountResourceList, in)}
}

// Result decodes the entries, whether more remain, and the marker.
func (res *AccountResourceListResult) Result() (entries []AccountResource, truncated bool, marker string, err error) {
	ret, err := clsutil.DecodeReply(res.res, class, methodAccountResourceList, DecodeAccountResourceListRet)
	if err != nil {
		return nil, false, "", err
	}
	return ret.Entries, ret.Truncated, ret.Marker, nil
}
