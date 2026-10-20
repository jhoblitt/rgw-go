package rgw

import (
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The lifecycle method names, RGW_LC_GET_ENTRY through RGW_LC_LIST_ENTRIES
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_const.h#L59-L65 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_const.h#L62-L68 at v20.2.4).
// Both releases register lc_set_entry, lc_rm_entry and lc_put_head RD|WR and
// the others RD
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4734-L4740 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5153-L5159 at v20.2.4).
const (
	methodLCGetEntry     = "lc_get_entry"
	methodLCSetEntry     = "lc_set_entry"
	methodLCRmEntry      = "lc_rm_entry"
	methodLCGetNextEntry = "lc_get_next_entry"
	methodLCPutHead      = "lc_put_head"
	methodLCGetHead      = "lc_get_head"
	methodLCListEntries  = "lc_list_entries"
)

// lcRequest encodes an lc call's input as radosgw's client functions,
// cls_rgw_lc_get_head through cls_rgw_lc_list, do, each call one exec
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L943-L1072 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L755-L879 at v20.2.4),
// for the class methods rgw_cls_lc_get_entry through rgw_cls_lc_get_head
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4175-L4358 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4577-L4760 at v20.2.4).
// No lc struct encodes differently at Tentacle, so the calls take no release.
func lcRequest(v clsutil.Request) []byte { return clsutil.Encode(v, denc.Squid) }

// LCEntryResult is the pending output of an lc_get_entry or
// lc_get_next_entry call.
type LCEntryResult struct {
	res    *radosclient.ExecResult
	method string
	decode func(*denc.Decoder) LCEntry
}

// Result decodes the entry once the op has run.
func (r *LCEntryResult) Result() (LCEntry, error) {
	return clsutil.DecodeReply(r.res, Class, r.method, r.decode)
}

// LCGetEntry adds lc_get_entry, which reads the shard's entry stored under
// marker, a bucket's key. The class fails with ENOENT when the shard holds no
// entry there, and with EIO when the stored value does not decode as an
// entry: unlike lc_list_entries, it does not read the legacy pair. radosclient
// has no sentinel for EIO, so the result's error is a *radosclient.Error
// whose Errno is EIO, found with errors.As.
func LCGetEntry(op *radosclient.ReadOp, marker string) *LCEntryResult {
	in := lcRequest(LCGetEntryOp{Marker: marker})
	return &LCEntryResult{
		res:    op.Exec(Class, methodLCGetEntry, in),
		method: methodLCGetEntry,
		decode: func(d *denc.Decoder) LCEntry { return DecodeLCGetEntryRet(d).Entry },
	}
}

// LCSetEntry adds lc_set_entry, which stores e under e.Bucket, creating the
// shard object when it does not exist.
func LCSetEntry(op radosclient.Execer, e LCEntry) {
	op.Exec(Class, methodLCSetEntry, lcRequest(LCSetEntryOp{Entry: e}))
}

// LCRmEntry adds lc_rm_entry, which removes the entry stored under e.Bucket;
// the class reads no other field.
func LCRmEntry(op radosclient.Execer, e LCEntry) {
	op.Exec(Class, methodLCRmEntry, lcRequest(LCRmEntryOp{Entry: e}))
}

// LCGetNextEntry adds lc_get_next_entry, which reads the shard's first entry
// after marker, "" for the first of all. Past the shard's last entry the
// class replies an empty entry, Bucket "", rather than an error. A next entry
// whose stored value does not decode as an entry, the legacy pair included,
// fails it with EIO, a *radosclient.Error found with errors.As.
func LCGetNextEntry(op *radosclient.ReadOp, marker string) *LCEntryResult {
	in := lcRequest(LCGetNextEntryOp{Marker: marker})
	return &LCEntryResult{
		res:    op.Exec(Class, methodLCGetNextEntry, in),
		method: methodLCGetNextEntry,
		decode: func(d *denc.Decoder) LCEntry { return DecodeLCGetNextEntryRet(d).Entry },
	}
}

// LCPutHead adds lc_put_head, which writes h as the shard's omap header,
// creating the shard object when it does not exist.
func LCPutHead(op radosclient.Execer, h LCHead) {
	op.Exec(Class, methodLCPutHead, lcRequest(LCPutHeadOp{Head: h}))
}

// LCHeadResult is the pending output of an lc_get_head call.
type LCHeadResult struct {
	res *radosclient.ExecResult
}

// Result decodes the head once the op has run.
func (r *LCHeadResult) Result() (LCHead, error) {
	return clsutil.DecodeReply(r.res, Class, methodLCGetHead, func(d *denc.Decoder) LCHead {
		return DecodeLCGetHeadRet(d).Head
	})
}

// LCGetHead adds lc_get_head, which reads the shard's head. It sends no
// input. A shard whose omap header is empty replies the zero head.
func LCGetHead(op *radosclient.ReadOp) *LCHeadResult {
	return &LCHeadResult{res: op.Exec(Class, methodLCGetHead, nil)}
}

// LCListResult is the pending output of an lc_list_entries call.
type LCListResult struct {
	res *radosclient.ExecResult
}

// Result decodes the listing once the op has run, its entries sorted by
// bucket as radosgw's client sorts them.
func (r *LCListResult) Result() (LCListEntriesRet, error) {
	ret, err := clsutil.DecodeReply(r.res, Class, methodLCListEntries, DecodeLCListEntriesRet)
	if err != nil {
		return LCListEntriesRet{}, err
	}
	slices.SortStableFunc(ret.Entries, func(a, b LCEntry) int { return strings.Compare(a.Bucket, b.Bucket) })
	return ret, nil
}

// LCListEntries adds lc_list_entries, which lists up to maxEntries of the
// shard's entries after marker, "" for the first of all, and reports
// whether more remain. The OSD caps the page at
// osd_max_omap_entries_per_request and osd_max_omap_bytes_per_request, so a
// page can be short of maxEntries and still truncated, and a maxEntries of 0
// answers no entries, truncated while any key remains
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7702-L7763 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7779-L7840 at v20.2.4):
// page on Truncated and the last bucket, never on the page's length, with a
// maximum of at least 1. It sends version 3, so the class replies with whole
// entries; a reply to an older client carries no start times.
func LCListEntries(op *radosclient.ReadOp, marker string, maxEntries uint32) *LCListResult {
	in := lcRequest(LCListEntriesOp{Marker: marker, MaxEntries: maxEntries})
	return &LCListResult{res: op.Exec(Class, methodLCListEntries, in)}
}
