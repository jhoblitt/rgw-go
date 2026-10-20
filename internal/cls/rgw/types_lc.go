package rgw

import (
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The LC_BUCKET_STATUS values an LCEntry carries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L34-L39 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L34-L39 at v20.2.4).
const (
	LCStatusUninitial  uint32 = 0
	LCStatusProcessing uint32 = 1
	LCStatusFailed     uint32 = 2
	LCStatusComplete   uint32 = 3
)

// LCEntry is cls_rgw_lc_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L1301-L1333 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1338-L1370 at v20.2.4),
// one bucket's entry on a lifecycle shard, stored under its Bucket key,
// "tenant:name:marker". StartTime, in seconds, is when the claim that set it
// processing was made.
type LCEntry struct {
	Bucket    string
	StartTime uint64
	Status    uint32
}

// Encode mirrors cls_rgw_lc_entry::encode, ENCODE_START(1, 1).
func (en LCEntry) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(en.Bucket)
	e.U64(en.StartTime)
	e.U32(en.Status)
	e.EndStruct(f)
}

// DecodeLCEntry mirrors cls_rgw_lc_entry::decode, DECODE_START(1).
func DecodeLCEntry(d *denc.Decoder) LCEntry {
	h := d.BeginStruct(1)
	var en LCEntry
	en.Bucket = d.String()
	en.StartTime = d.U64()
	en.Status = d.U32()
	d.EndStruct(h)
	return en
}

// decodeLCLegacyEntry reads the pair<string, int> of bucket and status that
// the entry carriers held before cls_rgw_lc_entry existed, with no start
// time. The int's bits are the status's, as C++'s uint32_t conversion keeps
// them.
func decodeLCLegacyEntry(d *denc.Decoder) LCEntry {
	return LCEntry{Bucket: d.String(), Status: d.U32()}
}

// encodeLCEntryCarrier writes the ENCODE_START(2, 2) frame around one entry
// that set_entry_op, rm_entry_op, get_entry_ret and get_next_entry_ret share.
func encodeLCEntryCarrier(e *denc.Encoder, en LCEntry, r denc.Release) {
	f := e.BeginStruct(2, 2)
	en.Encode(e, r)
	e.EndStruct(f)
}

// decodeLCEntryCarrier reads that frame, DECODE_START(2): the entry from
// version 2, the legacy pair before it.
func decodeLCEntryCarrier(d *denc.Decoder) LCEntry {
	h := d.BeginStruct(2)
	var en LCEntry
	if h.Version < 2 {
		en = decodeLCLegacyEntry(d)
	} else {
		en = DecodeLCEntry(d)
	}
	d.EndStruct(h)
	return en
}

// LCHead is cls_rgw_lc_obj_head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L1264-L1299 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1301-L1336 at v20.2.4),
// a lifecycle shard's omap header: when the head was last stored, the key of
// the entry a pass goes to next, and when a pass last reached the shard's
// end, both dates in seconds. radosgw's advance_head stamps the head's
// start_date with the time of every advance, and its pass logs it as "head
// last stored at"
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2188-L2192 at v19.2.7,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2325-L2328 at v19.2.7;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2253-L2257 at v20.2.4,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2391-L2394 at v20.2.4).
type LCHead struct {
	StartDate         uint64
	Marker            string
	ShardRolloverDate uint64
}

// Encode mirrors cls_rgw_lc_obj_head::encode, ENCODE_START(2, 2). Both dates
// go as 8 bytes: the start date through a uint64_t, the rollover date as
// time_t, which is int64_t on the 64-bit platforms Ceph builds for.
func (h LCHead) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 2)
	e.U64(h.StartDate)
	e.String(h.Marker)
	e.U64(h.ShardRolloverDate)
	e.EndStruct(f)
}

// DecodeLCHead mirrors cls_rgw_lc_obj_head::decode, DECODE_START(2).
// Version 1 carries no rollover date.
func DecodeLCHead(d *denc.Decoder) LCHead {
	s := d.BeginStruct(2)
	var h LCHead
	h.StartDate = d.U64()
	h.Marker = d.String()
	if s.Version >= 2 {
		h.ShardRolloverDate = d.U64()
	}
	d.EndStruct(s)
	return h
}

// The op and reply structs below are cls_rgw_lc_get_next_entry_op through
// cls_rgw_lc_list_entries_ret
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L1183-L1456 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L1221-L1494 at v20.2.4),
// the same at both releases.

// LCGetEntryOp is cls_rgw_lc_get_entry_op, the input of lc_get_entry.
type LCGetEntryOp struct {
	Marker string
}

// Encode mirrors cls_rgw_lc_get_entry_op::encode, ENCODE_START(1, 1).
func (o LCGetEntryOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Marker)
	e.EndStruct(f)
}

// DecodeLCGetEntryOp mirrors cls_rgw_lc_get_entry_op::decode, DECODE_START(1).
func DecodeLCGetEntryOp(d *denc.Decoder) LCGetEntryOp {
	h := d.BeginStruct(1)
	o := LCGetEntryOp{Marker: d.String()}
	d.EndStruct(h)
	return o
}

// LCGetEntryRet is cls_rgw_lc_get_entry_ret, the output of lc_get_entry.
type LCGetEntryRet struct {
	Entry LCEntry
}

// Encode mirrors cls_rgw_lc_get_entry_ret::encode, ENCODE_START(2, 2).
func (o LCGetEntryRet) Encode(e *denc.Encoder, r denc.Release) {
	encodeLCEntryCarrier(e, o.Entry, r)
}

// DecodeLCGetEntryRet mirrors cls_rgw_lc_get_entry_ret::decode,
// DECODE_START(2). Version 1 changed its encoding without a version bump, so
// a version 1 reply is read as an entry, and as the legacy pair when it does
// not decode as one. Both attempts are bounded by the struct's length.
func DecodeLCGetEntryRet(d *denc.Decoder) LCGetEntryRet {
	h := d.BeginStruct(2)
	var o LCGetEntryRet
	if h.Version < 2 {
		o.Entry = decodeLCEntryEitherWay(d, d.RestOfStruct(h))
	} else {
		o.Entry = DecodeLCEntry(d)
	}
	d.EndStruct(h)
	return o
}

// decodeLCEntryEitherWay decodes body as an entry, else as the legacy pair,
// recording on d the pair's failure when both fail.
func decodeLCEntryEitherWay(d *denc.Decoder, body []byte) LCEntry {
	sub := denc.NewDecoder(body)
	if en := DecodeLCEntry(sub); sub.Err() == nil {
		return en
	}
	sub = denc.NewDecoder(body)
	en := decodeLCLegacyEntry(sub)
	d.Fail(sub.Err())
	return en
}

// LCSetEntryOp is cls_rgw_lc_set_entry_op, the input of lc_set_entry.
type LCSetEntryOp struct {
	Entry LCEntry
}

// Encode mirrors cls_rgw_lc_set_entry_op::encode, ENCODE_START(2, 2).
func (o LCSetEntryOp) Encode(e *denc.Encoder, r denc.Release) {
	encodeLCEntryCarrier(e, o.Entry, r)
}

// DecodeLCSetEntryOp mirrors cls_rgw_lc_set_entry_op::decode, DECODE_START(2).
func DecodeLCSetEntryOp(d *denc.Decoder) LCSetEntryOp {
	return LCSetEntryOp{Entry: decodeLCEntryCarrier(d)}
}

// LCRmEntryOp is cls_rgw_lc_rm_entry_op, the input of lc_rm_entry.
// ceph-dencoder does not register it.
type LCRmEntryOp struct {
	Entry LCEntry
}

// Encode mirrors cls_rgw_lc_rm_entry_op::encode, ENCODE_START(2, 2).
func (o LCRmEntryOp) Encode(e *denc.Encoder, r denc.Release) {
	encodeLCEntryCarrier(e, o.Entry, r)
}

// DecodeLCRmEntryOp mirrors cls_rgw_lc_rm_entry_op::decode, DECODE_START(2).
func DecodeLCRmEntryOp(d *denc.Decoder) LCRmEntryOp {
	return LCRmEntryOp{Entry: decodeLCEntryCarrier(d)}
}

// LCGetNextEntryOp is cls_rgw_lc_get_next_entry_op, the input of
// lc_get_next_entry.
type LCGetNextEntryOp struct {
	Marker string
}

// Encode mirrors cls_rgw_lc_get_next_entry_op::encode, ENCODE_START(1, 1).
func (o LCGetNextEntryOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Marker)
	e.EndStruct(f)
}

// DecodeLCGetNextEntryOp mirrors cls_rgw_lc_get_next_entry_op::decode,
// DECODE_START(1).
func DecodeLCGetNextEntryOp(d *denc.Decoder) LCGetNextEntryOp {
	h := d.BeginStruct(1)
	o := LCGetNextEntryOp{Marker: d.String()}
	d.EndStruct(h)
	return o
}

// LCGetNextEntryRet is cls_rgw_lc_get_next_entry_ret, the output of
// lc_get_next_entry.
type LCGetNextEntryRet struct {
	Entry LCEntry
}

// Encode mirrors cls_rgw_lc_get_next_entry_ret::encode, ENCODE_START(2, 2).
func (o LCGetNextEntryRet) Encode(e *denc.Encoder, r denc.Release) {
	encodeLCEntryCarrier(e, o.Entry, r)
}

// DecodeLCGetNextEntryRet mirrors cls_rgw_lc_get_next_entry_ret::decode,
// DECODE_START(2).
func DecodeLCGetNextEntryRet(d *denc.Decoder) LCGetNextEntryRet {
	return LCGetNextEntryRet{Entry: decodeLCEntryCarrier(d)}
}

// LCPutHeadOp is cls_rgw_lc_put_head_op, the input of lc_put_head.
type LCPutHeadOp struct {
	Head LCHead
}

// Encode mirrors cls_rgw_lc_put_head_op::encode, ENCODE_START(1, 1).
func (o LCPutHeadOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Head.Encode(e, r)
	e.EndStruct(f)
}

// DecodeLCPutHeadOp mirrors cls_rgw_lc_put_head_op::decode, DECODE_START(1).
func DecodeLCPutHeadOp(d *denc.Decoder) LCPutHeadOp {
	h := d.BeginStruct(1)
	o := LCPutHeadOp{Head: DecodeLCHead(d)}
	d.EndStruct(h)
	return o
}

// LCGetHeadRet is cls_rgw_lc_get_head_ret, the output of lc_get_head.
type LCGetHeadRet struct {
	Head LCHead
}

// Encode mirrors cls_rgw_lc_get_head_ret::encode, ENCODE_START(1, 1).
func (o LCGetHeadRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Head.Encode(e, r)
	e.EndStruct(f)
}

// DecodeLCGetHeadRet mirrors cls_rgw_lc_get_head_ret::decode, DECODE_START(1).
func DecodeLCGetHeadRet(d *denc.Decoder) LCGetHeadRet {
	h := d.BeginStruct(1)
	o := LCGetHeadRet{Head: DecodeLCHead(d)}
	d.EndStruct(h)
	return o
}

// LCListEntriesOp is cls_rgw_lc_list_entries_op, the input of
// lc_list_entries. Version is compat_v, the struct_v a decoded request
// carried, 0 included, which the class replies at.
type LCListEntriesOp struct {
	Marker     string
	MaxEntries uint32
	Version    uint8
}

// Encode mirrors cls_rgw_lc_list_entries_op::encode, ENCODE_START(3, 1),
// whatever Version holds.
func (o LCListEntriesOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(3, 1)
	e.String(o.Marker)
	e.U32(o.MaxEntries)
	e.EndStruct(f)
}

// DecodeLCListEntriesOp mirrors cls_rgw_lc_list_entries_op::decode,
// DECODE_START(3).
func DecodeLCListEntriesOp(d *denc.Decoder) LCListEntriesOp {
	h := d.BeginStruct(3)
	o := LCListEntriesOp{Version: h.Version}
	o.Marker = d.String()
	o.MaxEntries = d.U32()
	d.EndStruct(h)
	return o
}

// LCListEntriesRet is cls_rgw_lc_list_entries_ret, the output of
// lc_list_entries. Version is compat_v, the struct_v it is encoded at, which
// the class takes from the request, 0 included; nil encodes version 3, the
// C++ constructor's default. Decoding sets it.
type LCListEntriesRet struct {
	Entries   []LCEntry
	Truncated bool
	Version   *uint8
}

// Encode mirrors cls_rgw_lc_list_entries_ret::encode, ENCODE_START(compat_v,
// 1): through version 2 the entries go as a map of bucket to status, from
// version 3 as entries, and the truncation flag follows at every version.
func (o LCListEntriesRet) Encode(e *denc.Encoder, r denc.Release) {
	v := uint8(3)
	if o.Version != nil {
		v = *o.Version
	}
	f := e.BeginStruct(v, 1)
	if v <= 2 {
		encodeLCLegacyEntries(e, o.Entries)
	} else {
		denc.EncodeSlice(e, o.Entries, func(e *denc.Encoder, en LCEntry) { en.Encode(e, r) })
	}
	e.Bool(o.Truncated)
	e.EndStruct(f)
}

// DecodeLCListEntriesRet mirrors cls_rgw_lc_list_entries_ret::decode,
// DECODE_START(3). The entries keep the order they were sent in; a legacy
// map's come in bucket order. Version 1 carries no truncation flag that its
// decoder reads.
func DecodeLCListEntriesRet(d *denc.Decoder) LCListEntriesRet {
	h := d.BeginStruct(3)
	o := LCListEntriesRet{Version: new(h.Version)}
	if h.Version <= 2 {
		o.Entries = decodeLCLegacyEntries(d)
	} else {
		o.Entries = denc.DecodeSlice(d, DecodeLCEntry)
	}
	if h.Version >= 2 {
		o.Truncated = d.Bool()
	}
	d.EndStruct(h)
	return o
}

// encodeLCLegacyEntries writes the map<string, int> of bucket to status.
// C++ fills it with std::map::insert, so the first entry for a bucket wins.
func encodeLCLegacyEntries(e *denc.Encoder, entries []LCEntry) {
	m := make(map[string]uint32, len(entries))
	for _, en := range entries {
		if _, ok := m[en.Bucket]; !ok {
			m[en.Bucket] = en.Status
		}
	}
	denc.EncodeMap(e, m, (*denc.Encoder).String, (*denc.Encoder).U32)
}

// decodeLCLegacyEntries reads that map as entries in bucket order. Its key
// and value have denc traits, so a repeated bucket keeps its first status.
func decodeLCLegacyEntries(d *denc.Decoder) []LCEntry {
	m := denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).U32)
	var entries []LCEntry
	for _, b := range slices.Sorted(maps.Keys(m)) {
		entries = append(entries, LCEntry{Bucket: b, Status: m[b]})
	}
	return entries
}
