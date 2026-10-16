package rgw

import "github.com/jhoblitt/rgw-go/internal/denc"

// OLHLogOp is OLHLogOp, the change one pending OLH log entry records. It
// travels as a u8.
type OLHLogOp uint8

// The OLHLogOp values, CLS_RGW_OLH_OP_UNKNOWN to CLS_RGW_OLH_OP_REMOVE_INSTANCE
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L513-L518 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L512-L517 at v20.2.4).
// OLHOpUnlinkOLH records that the name has no current version left.
const (
	OLHOpUnknown        OLHLogOp = 0
	OLHOpLinkOLH        OLHLogOp = 1
	OLHOpUnlinkOLH      OLHLogOp = 2
	OLHOpRemoveInstance OLHLogOp = 3
)

// OLHLogEntry is rgw_bucket_olh_log_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L520-L554 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L519-L553 at v20.2.4),
// one change in an OLH's pending log for radosgw to apply to the OLH object.
type OLHLogEntry struct {
	Epoch        uint64
	Op           OLHLogOp
	OpTag        string
	Key          ObjKey
	DeleteMarker bool
}

// Encode mirrors rgw_bucket_olh_log_entry::encode, ENCODE_START(1, 1).
func (l OLHLogEntry) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(l.Epoch)
	e.U8(uint8(l.Op))
	e.String(l.OpTag)
	l.Key.Encode(e, r)
	e.Bool(l.DeleteMarker)
	e.EndStruct(f)
}

// DecodeOLHLogEntry mirrors rgw_bucket_olh_log_entry::decode, DECODE_START(1).
func DecodeOLHLogEntry(d *denc.Decoder) OLHLogEntry {
	h := d.BeginStruct(1)
	var l OLHLogEntry
	l.Epoch = d.U64()
	l.Op = OLHLogOp(d.U8())
	l.OpTag = d.String()
	l.Key = DecodeObjKey(d)
	l.DeleteMarker = d.Bool()
	d.EndStruct(h)
	return l
}

// OLHEntry is rgw_bucket_olh_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L556-L593 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L555-L592 at v20.2.4),
// the entry stored under a name's OLHDataKey: the current version, the
// epoch, the pending log by epoch, and the tag of the OLH object it belongs
// to.
type OLHEntry struct {
	Key            ObjKey
	DeleteMarker   bool
	Epoch          uint64
	PendingLog     map[uint64][]OLHLogEntry
	Tag            string
	Exists         bool
	PendingRemoval bool
}

// Encode mirrors rgw_bucket_olh_entry::encode, ENCODE_START(1, 1).
func (o OLHEntry) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Key.Encode(e, r)
	e.Bool(o.DeleteMarker)
	e.U64(o.Epoch)
	encodeOLHLog(e, o.PendingLog, r)
	e.String(o.Tag)
	e.Bool(o.Exists)
	e.Bool(o.PendingRemoval)
	e.EndStruct(f)
}

// DecodeOLHEntry mirrors rgw_bucket_olh_entry::decode, DECODE_START(1).
func DecodeOLHEntry(d *denc.Decoder) OLHEntry {
	h := d.BeginStruct(1)
	var o OLHEntry
	o.Key = DecodeObjKey(d)
	o.DeleteMarker = d.Bool()
	o.Epoch = d.U64()
	o.PendingLog = decodeOLHLog(d)
	o.Tag = d.String()
	o.Exists = d.Bool()
	o.PendingRemoval = d.Bool()
	d.EndStruct(h)
	return o
}

// encodeOLHLog writes a map<uint64_t, vector<rgw_bucket_olh_log_entry>>,
// epochs in order.
func encodeOLHLog(e *denc.Encoder, m map[uint64][]OLHLogEntry, r denc.Release) {
	denc.EncodeMap(e, m, (*denc.Encoder).U64, func(e *denc.Encoder, es []OLHLogEntry) {
		denc.EncodeSlice(e, es, func(e *denc.Encoder, l OLHLogEntry) { l.Encode(e, r) })
	})
}

// decodeOLHLog reads the log map. Its entries have no denc traits, so C++
// decodes the map through operator[] and a repeated epoch keeps the last
// vector.
func decodeOLHLog(d *denc.Decoder) map[uint64][]OLHLogEntry {
	return denc.DecodeMapLast(d, (*denc.Decoder).U64, func(d *denc.Decoder) []OLHLogEntry {
		return denc.DecodeSlice(d, DecodeOLHLogEntry)
	})
}
