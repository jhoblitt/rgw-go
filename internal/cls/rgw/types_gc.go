package rgw

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// GCObj is cls_rgw_obj, one RADOS object garbage collection deletes: its
// pool, its object name (Key.Name) with an optional instance, and its
// locator.
type GCObj struct {
	Pool string
	Key  ObjKey
	Loc  string
}

// Encode mirrors cls_rgw_obj::encode, ENCODE_START(2, 1): the name is
// written alone, then again inside the key.
func (o GCObj) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Pool)
	e.String(o.Key.Name)
	e.String(o.Loc)
	o.Key.Encode(e, r)
	e.EndStruct(f)
}

// DecodeGCObj mirrors cls_rgw_obj::decode, DECODE_START(2). From version 2
// the key replaces the name read before it.
func DecodeGCObj(d *denc.Decoder) GCObj {
	h := d.BeginStruct(2)
	var o GCObj
	o.Pool = d.String()
	o.Key.Name = d.String()
	o.Loc = d.String()
	if h.Version >= 2 {
		o.Key = DecodeObjKey(d)
	}
	d.EndStruct(h)
	return o
}

// EncodeGCObjChain writes cls_rgw_obj_chain, ENCODE_START(1, 1).
func EncodeGCObjChain(e *denc.Encoder, chain []GCObj, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, chain, func(e *denc.Encoder, o GCObj) { o.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeGCObjChain reads cls_rgw_obj_chain, DECODE_START(1).
func DecodeGCObjChain(d *denc.Decoder) []GCObj {
	h := d.BeginStruct(1)
	chain := denc.DecodeSlice(d, DecodeGCObj)
	d.EndStruct(h)
	return chain
}

// GCObjInfo is cls_rgw_gc_obj_info, one garbage collection entry: the tag
// that names it, the objects to delete, and when it becomes due. The omap-era
// gc_set_entry and the queue-era rgw_gc class both carry it.
type GCObjInfo struct {
	Tag   string
	Chain []GCObj
	Time  time.Time
}

// Encode mirrors cls_rgw_gc_obj_info::encode, ENCODE_START(1, 1).
func (g GCObjInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(g.Tag)
	EncodeGCObjChain(e, g.Chain, r)
	e.Time(g.Time)
	e.EndStruct(f)
}

// DecodeGCObjInfo mirrors cls_rgw_gc_obj_info::decode, DECODE_START(1).
func DecodeGCObjInfo(d *denc.Decoder) GCObjInfo {
	h := d.BeginStruct(1)
	var g GCObjInfo
	g.Tag = d.String()
	g.Chain = DecodeGCObjChain(d)
	g.Time = d.Time()
	d.EndStruct(h)
	return g
}

// GCSetEntryOp is cls_rgw_gc_set_entry_op, the input of gc_set_entry and of
// the rgw_gc class's enqueue and update methods.
type GCSetEntryOp struct {
	ExpirationSecs uint32
	Info           GCObjInfo
}

// Encode mirrors cls_rgw_gc_set_entry_op::encode, ENCODE_START(1, 1).
func (o GCSetEntryOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U32(o.ExpirationSecs)
	o.Info.Encode(e, r)
	e.EndStruct(f)
}

// DecodeGCSetEntryOp mirrors cls_rgw_gc_set_entry_op::decode, DECODE_START(1).
func DecodeGCSetEntryOp(d *denc.Decoder) GCSetEntryOp {
	h := d.BeginStruct(1)
	var o GCSetEntryOp
	o.ExpirationSecs = d.U32()
	o.Info = DecodeGCObjInfo(d)
	d.EndStruct(h)
	return o
}

// GCListOp is cls_rgw_gc_list_op, the input of gc_list and of the rgw_gc
// class's rgw_gc_queue_list_entries. The C++ default for ExpiredOnly is true,
// which a version 1 encoding implies.
type GCListOp struct {
	Marker      string
	Max         uint32
	ExpiredOnly bool
}

// Encode mirrors cls_rgw_gc_list_op::encode, ENCODE_START(2, 1).
func (o GCListOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Marker)
	e.U32(o.Max)
	e.Bool(o.ExpiredOnly)
	e.EndStruct(f)
}

// DecodeGCListOp mirrors cls_rgw_gc_list_op::decode, DECODE_START(2).
func DecodeGCListOp(d *denc.Decoder) GCListOp {
	h := d.BeginStruct(2)
	o := GCListOp{Marker: d.String(), Max: d.U32(), ExpiredOnly: true}
	if h.Version >= 2 {
		o.ExpiredOnly = d.Bool()
	}
	d.EndStruct(h)
	return o
}

// GCListRet is cls_rgw_gc_list_ret, the output of gc_list and of the rgw_gc
// class's rgw_gc_queue_list_entries. NextMarker is set only when Truncated is.
type GCListRet struct {
	Entries    []GCObjInfo
	NextMarker string
	Truncated  bool
}

// Encode mirrors cls_rgw_gc_list_ret::encode, ENCODE_START(2, 1).
func (o GCListRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	denc.EncodeSlice(e, o.Entries, func(e *denc.Encoder, i GCObjInfo) { i.Encode(e, r) })
	e.String(o.NextMarker)
	e.Bool(o.Truncated)
	e.EndStruct(f)
}

// DecodeGCListRet mirrors cls_rgw_gc_list_ret::decode, DECODE_START(2).
func DecodeGCListRet(d *denc.Decoder) GCListRet {
	h := d.BeginStruct(2)
	var o GCListRet
	o.Entries = denc.DecodeSlice(d, DecodeGCObjInfo)
	if h.Version >= 2 {
		o.NextMarker = d.String()
	}
	o.Truncated = d.Bool()
	d.EndStruct(h)
	return o
}

// GCRemoveOp is cls_rgw_gc_remove_op, the input of gc_remove.
type GCRemoveOp struct {
	Tags []string
}

// Encode mirrors cls_rgw_gc_remove_op::encode, ENCODE_START(1, 1).
func (o GCRemoveOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.Tags, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeGCRemoveOp mirrors cls_rgw_gc_remove_op::decode, DECODE_START(1).
func DecodeGCRemoveOp(d *denc.Decoder) GCRemoveOp {
	h := d.BeginStruct(1)
	o := GCRemoveOp{Tags: denc.DecodeSlice(d, (*denc.Decoder).String)}
	d.EndStruct(h)
	return o
}

// GCDeferEntryOp is cls_rgw_gc_defer_entry_op, the input of gc_defer_entry.
type GCDeferEntryOp struct {
	ExpirationSecs uint32
	Tag            string
}

// Encode mirrors cls_rgw_gc_defer_entry_op::encode, ENCODE_START(1, 1).
func (o GCDeferEntryOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U32(o.ExpirationSecs)
	e.String(o.Tag)
	e.EndStruct(f)
}

// DecodeGCDeferEntryOp mirrors cls_rgw_gc_defer_entry_op::decode, DECODE_START(1).
func DecodeGCDeferEntryOp(d *denc.Decoder) GCDeferEntryOp {
	h := d.BeginStruct(1)
	var o GCDeferEntryOp
	o.ExpirationSecs = d.U32()
	o.Tag = d.String()
	d.EndStruct(h)
	return o
}
