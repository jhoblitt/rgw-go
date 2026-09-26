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
