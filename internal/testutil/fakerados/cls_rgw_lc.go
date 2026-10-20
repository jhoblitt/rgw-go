package fakerados

import (
	"maps"
	"slices"
	"syscall"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The methods RGWClass emulates of those that keep the lifecycle shards'
// bucket entries and head, lc_get_entry to lc_list_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4734-L4740 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5153-L5159 at v20.2.4).
// The class registers lc_set_entry, lc_rm_entry and lc_put_head RD|WR and
// the others RD, so a read op of only those finds no object context for a
// missing shard and fails with ENOENT before any method runs. The methods,
// rgw_cls_lc_get_entry through rgw_cls_lc_get_head, are the same code at
// both releases
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4175-L4358 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4577-L4760 at v20.2.4).
// Each that takes input answers EINVAL for one that does not decode;
// lc_get_head reads none.
var _ = rgwTable(methodTable{
	"lc_get_entry":      {fn: rgwLCGetEntry},
	"lc_set_entry":      {fn: noOutput(rgwLCSetEntry), write: true},
	"lc_rm_entry":       {fn: noOutput(rgwLCRmEntry), write: true},
	"lc_get_next_entry": {fn: rgwLCGetNextEntry},
	"lc_put_head":       {fn: noOutput(rgwLCPutHead), write: true},
	"lc_get_head":       {fn: rgwLCGetHead},
	"lc_list_entries":   {fn: rgwLCListEntries},
})

// rgwLCEntry decodes a stored value as cls_rgw_lc_entry, reporting false when
// it does not decode. The pair form of releases through v15.2.4 never does:
// where the struct's length would be, it holds the high bytes of the bucket's
// length and its first two characters, far past the value's end.
func rgwLCEntry(b []byte) (rgwcls.LCEntry, bool) {
	d := denc.NewDecoder(b)
	en := rgwcls.DecodeLCEntry(d)
	return en, d.Err() == nil
}

// rgwLCGetEntry is rgw_cls_lc_get_entry through read_omap_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L914-L932 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1044-L1062 at v20.2.4):
// the entry stored under the marker, ENOENT when the shard holds none there,
// and EIO when the value does not decode as an entry, the pair form included,
// since read_omap_entry reads no pair (docs/ceph-upstream-bugs.md).
func rgwLCGetEntry(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLCGetEntryOp)
	if rval < 0 {
		return nil, rval
	}
	omap, _ := call.StoredOmap()
	b, ok := omap[op.Marker]
	if !ok {
		return nil, -int32(syscall.ENOENT)
	}
	en, ok := rgwLCEntry(b)
	if !ok {
		return nil, -int32(syscall.EIO)
	}
	return encodeSquid(rgwcls.LCGetEntryRet{Entry: en}), 0
}

// rgwLCSetEntry is rgw_cls_lc_set_entry: the request's entry, which a
// version 1 request carries as the pair, is stored as cls_rgw_lc_entry under
// its bucket, creating the shard as cls_cxx_map_set_val does.
func rgwLCSetEntry(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLCSetEntryOp)
	if rval < 0 {
		return rval
	}
	call.Create().Omap[op.Entry.Bucket] = encodeSquid(op.Entry)
	return 0
}

// rgwLCRmEntry is rgw_cls_lc_rm_entry: the key of the request's bucket goes.
// cls_cxx_map_remove_key is CEPH_OSD_OP_OMAPRMKEYS, ENOENT on a shard that
// does not exist and nothing for a key it lacks
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7959-L7972 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8036-L8049 at v20.2.4).
func rgwLCRmEntry(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLCRmEntryOp)
	if rval < 0 {
		return rval
	}
	obj := call.Object()
	if obj == nil {
		return -int32(syscall.ENOENT)
	}
	delete(obj.Omap, op.Entry.Bucket)
	return 0
}

// rgwLCGetNextEntry is rgw_cls_lc_get_next_entry: one omap read of one key
// after the marker, the default entry, bucket "", when there is none, and EIO
// when its value does not decode as an entry, the pair form included
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4255-L4265 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4657-L4667 at v20.2.4).
func rgwLCGetNextEntry(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLCGetNextEntryOp)
	if rval < 0 {
		return nil, rval
	}
	var en rgwcls.LCEntry
	omap, _ := call.StoredOmap()
	vals, _ := omapPage(omap, op.Marker, "", 1)
	for _, b := range vals {
		var ok bool
		if en, ok = rgwLCEntry(b); !ok {
			return nil, -int32(syscall.EIO)
		}
	}
	return encodeSquid(rgwcls.LCGetNextEntryRet{Entry: en}), 0
}

// rgwLCListValue reads a listed value as rgw_cls_lc_list_entries does, "try
// backward compat": as an entry, else as the pair of bucket and status with
// start 0, reporting false when it is neither.
func rgwLCListValue(b []byte) (rgwcls.LCEntry, bool) {
	if en, ok := rgwLCEntry(b); ok {
		return en, true
	}
	d := denc.NewDecoder(b)
	en := rgwcls.LCEntry{Bucket: d.String(), Status: d.U32()}
	return en, d.Err() == nil
}

// rgwLCListEntries is rgw_cls_lc_list_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4271-L4313 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4673-L4715 at v20.2.4):
// one omap read of the request's maximum after its marker, which
// CEPH_OSD_OP_OMAPGETVALS truncates while a key follows the page, so a
// maximum of 0 answers no entries, truncated while a key follows the marker
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7702-L7763 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7779-L7840 at v20.2.4).
// Each value is read as rgwLCListValue reads it, EIO when it is neither form,
// and the reply goes at the request's struct_v, a bucket-to-status map
// through version 2.
func rgwLCListEntries(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLCListEntriesOp)
	if rval < 0 {
		return nil, rval
	}
	ret := rgwcls.LCListEntriesRet{Version: &op.Version}
	omap, _ := call.StoredOmap()
	vals, more := omapPage(omap, op.Marker, "", uint64(op.MaxEntries))
	ret.Truncated = more
	for _, k := range slices.Sorted(maps.Keys(vals)) {
		en, ok := rgwLCListValue(vals[k])
		if !ok {
			return nil, -int32(syscall.EIO)
		}
		ret.Entries = append(ret.Entries, en)
	}
	return encodeSquid(ret), 0
}

// rgwLCPutHead is rgw_cls_lc_put_head: the request's head, a version 1 one
// with a zero rollover date, is stored as the omap header at version 2,
// creating the shard as cls_cxx_map_write_header does.
func rgwLCPutHead(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLCPutHeadOp)
	if rval < 0 {
		return rval
	}
	call.Create().OmapHdr = encodeSquid(op.Head)
	return 0
}

// rgwLCGetHead is rgw_cls_lc_get_head, which reads no input: the zero head
// for a shard without a header, and EINVAL, not read_omap_entry's EIO, for
// one that does not decode.
func rgwLCGetHead(call *ClassCall) (out []byte, rval int32) {
	var h rgwcls.LCHead
	if _, hdr := call.StoredOmap(); len(hdr) != 0 {
		d := denc.NewDecoder(hdr)
		h = rgwcls.DecodeLCHead(d)
		if d.Err() != nil {
			return nil, -int32(syscall.EINVAL)
		}
	}
	return encodeSquid(rgwcls.LCGetHeadRet{Head: h}), 0
}
