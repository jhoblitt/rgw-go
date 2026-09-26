package gc

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// QueueInitOp is cls_rgw_gc_queue_init_op, the rgw_gc_queue_init request:
// the queue's data capacity in bytes and how many tags may be deferred.
type QueueInitOp struct {
	Size               uint64
	NumDeferredEntries uint64
}

// Encode mirrors cls_rgw_gc_queue_init_op::encode, ENCODE_START(1, 1).
func (o QueueInitOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(o.Size)
	e.U64(o.NumDeferredEntries)
	e.EndStruct(f)
}

// DecodeQueueInitOp mirrors cls_rgw_gc_queue_init_op::decode, DECODE_START(1).
func DecodeQueueInitOp(d *denc.Decoder) QueueInitOp {
	h := d.BeginStruct(1)
	var o QueueInitOp
	o.Size = d.U64()
	o.NumDeferredEntries = d.U64()
	d.EndStruct(h)
	return o
}

// UrgentData is cls_rgw_gc_urgent_data, the blob rgw_gc keeps in the queue
// head's bl_urgent_data: the deferred tags with their new expiry, and the
// deferral limit and counts. C++ holds the map in a std::unordered_map, so its
// encoding order is unspecified; Encode writes the keys sorted.
type UrgentData struct {
	UrgentDataMap         map[string]time.Time
	NumUrgentDataEntries  uint32 // the limit QueueInit recorded
	NumHeadUrgentEntries  uint32 // tags held in UrgentDataMap
	NumXattrUrgentEntries uint32 // tags spilled to the cls_queue_urgent_data xattr
}

// Encode mirrors cls_rgw_gc_urgent_data::encode, ENCODE_START(1, 1).
func (u UrgentData) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeMap(e, u.UrgentDataMap, (*denc.Encoder).String, (*denc.Encoder).Time)
	e.U32(u.NumUrgentDataEntries)
	e.U32(u.NumHeadUrgentEntries)
	e.U32(u.NumXattrUrgentEntries)
	e.EndStruct(f)
}

// DecodeUrgentData mirrors cls_rgw_gc_urgent_data::decode, DECODE_START(1).
// A repeated tag keeps its first time, as the unordered_map decode's emplace does.
func DecodeUrgentData(d *denc.Decoder) UrgentData {
	h := d.BeginStruct(1)
	var u UrgentData
	u.UrgentDataMap = denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).Time)
	u.NumUrgentDataEntries = d.U32()
	u.NumHeadUrgentEntries = d.U32()
	u.NumXattrUrgentEntries = d.U32()
	d.EndStruct(h)
	return u
}

// QueueRemoveEntriesOp is cls_rgw_gc_queue_remove_entries_op. The C++ client
// takes a u32 count but the struct stores it as a u64.
type QueueRemoveEntriesOp struct {
	NumEntries uint64
}

// Encode mirrors cls_rgw_gc_queue_remove_entries_op::encode, ENCODE_START(1, 1).
func (o QueueRemoveEntriesOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(o.NumEntries)
	e.EndStruct(f)
}

// DecodeQueueRemoveEntriesOp mirrors cls_rgw_gc_queue_remove_entries_op::decode, DECODE_START(1).
func DecodeQueueRemoveEntriesOp(d *denc.Decoder) QueueRemoveEntriesOp {
	h := d.BeginStruct(1)
	o := QueueRemoveEntriesOp{NumEntries: d.U64()}
	d.EndStruct(h)
	return o
}

// QueueDeferEntryOp is cls_rgw_gc_queue_defer_entry_op, the
// rgw_gc_queue_update_entry request. rgw_gc_queue_enqueue takes
// cls_rgw_gc_set_entry_op, which has the same fields and encoding.
type QueueDeferEntryOp struct {
	ExpirationSecs uint32
	Info           rgw.GCObjInfo
}

// Encode mirrors cls_rgw_gc_queue_defer_entry_op::encode, ENCODE_START(1, 1).
func (o QueueDeferEntryOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U32(o.ExpirationSecs)
	o.Info.Encode(e, r)
	e.EndStruct(f)
}

// DecodeQueueDeferEntryOp mirrors cls_rgw_gc_queue_defer_entry_op::decode, DECODE_START(1).
func DecodeQueueDeferEntryOp(d *denc.Decoder) QueueDeferEntryOp {
	h := d.BeginStruct(1)
	var o QueueDeferEntryOp
	o.ExpirationSecs = d.U32()
	o.Info = rgw.DecodeGCObjInfo(d)
	d.EndStruct(h)
	return o
}

// ListOp is cls_rgw_gc_list_op, the rgw_gc_queue_list_entries request. The
// C++ default for ExpiredOnly is true, which a version 1 encoding implies.
type ListOp struct {
	Marker      string
	Max         uint32
	ExpiredOnly bool
}

// Encode mirrors cls_rgw_gc_list_op::encode, ENCODE_START(2, 1).
func (o ListOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Marker)
	e.U32(o.Max)
	e.Bool(o.ExpiredOnly)
	e.EndStruct(f)
}

// DecodeListOp mirrors cls_rgw_gc_list_op::decode, DECODE_START(2).
func DecodeListOp(d *denc.Decoder) ListOp {
	h := d.BeginStruct(2)
	o := ListOp{Marker: d.String(), Max: d.U32(), ExpiredOnly: true}
	if h.Version >= 2 {
		o.ExpiredOnly = d.Bool()
	}
	d.EndStruct(h)
	return o
}

// ListRet is cls_rgw_gc_list_ret, the rgw_gc_queue_list_entries reply.
// NextMarker is set only when Truncated is.
type ListRet struct {
	Entries    []rgw.GCObjInfo
	NextMarker string
	Truncated  bool
}

// Encode mirrors cls_rgw_gc_list_ret::encode, ENCODE_START(2, 1).
func (o ListRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	denc.EncodeSlice(e, o.Entries, func(e *denc.Encoder, i rgw.GCObjInfo) { i.Encode(e, r) })
	e.String(o.NextMarker)
	e.Bool(o.Truncated)
	e.EndStruct(f)
}

// DecodeListRet mirrors cls_rgw_gc_list_ret::decode, DECODE_START(2).
func DecodeListRet(d *denc.Decoder) ListRet {
	h := d.BeginStruct(2)
	var o ListRet
	o.Entries = denc.DecodeSlice(d, rgw.DecodeGCObjInfo)
	if h.Version >= 2 {
		o.NextMarker = d.String()
	}
	o.Truncated = d.Bool()
	d.EndStruct(h)
	return o
}
