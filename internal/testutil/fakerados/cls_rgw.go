package fakerados

import (
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// rgwTables are the rgw class's area tables, each added by rgwTable as it is
// declared, whatever file declares it.
var rgwTables []methodTable

// rgwTable adds t to the rgw class's tables and returns it: declaring an
// area's table as `var _ = rgwTable(methodTable{...})` is what puts its
// methods in RGWClass and RGWMethods and its write flags in RGWWriteMethods.
func rgwTable(t methodTable) methodTable { //nolint:unparam // the result lets the call initialize a package variable, which runs before init
	rgwTables = append(rgwTables, t)
	return t
}

// rgwClassMethods is every method RGWClass emulates, the area tables merged.
var rgwClassMethods methodTable

// RGWMethods are every method RGWClass emulates, in name order, which
// RegisterClassMethods takes with RGWClass and RGWWriteMethods.
var RGWMethods []string

// RGWWriteMethods are the methods RGWClass emulates that the rgw class
// registers with CLS_METHOD_WR, in name order, which RegisterClassMethods
// takes with RGWClass and RGWMethods.
var RGWWriteMethods []string

// init assembles the class: Go initializes every package variable before any
// init runs, so every area's table is registered, whatever file declares it.
func init() {
	rgwClassMethods, RGWMethods, RGWWriteMethods = assembleClass(rgwTables...)
	tableClasses["rgw"] = true
}

// RGWClass emulates the rgw class (src/cls/rgw/cls_rgw.cc at v19.2.7 and
// v20.2.4): it runs the methods of the area tables merged into
// rgwClassMethods, and every other method is EOPNOTSUPP.
//
// It is Squid's class, whatever the release: the guard refuses every reshard
// status but not-resharding, and neither index method guards itself or keeps
// a reshard log, as Tentacle's do. It keeps no bucket index log.
func RGWClass() ClassFunc { return rgwClassMethods.dispatch() }

// The bucket index, reshard status, head object, gc, usage log and multipart
// methods RGWClass emulates. The bucket index methods work over a shard's
// omap: one entry per index key, and the rgw_bucket_dir_header in the omap
// header. The methods are bucket_init_index; bucket_list asking for no
// entries, which is how radosgw reads a shard's header; the index
// transaction, bucket_prepare_op and bucket_complete_op;
// guard_bucket_resharding, get_bucket_resharding and set_bucket_resharding,
// with which a spec stages a reshard; mp_upload_part_info_update on a
// multipart meta object; obj_remove, obj_store_pg_ver and obj_check_mtime on
// a head object; on a gc shard the omap-era log, gc_set_entry, gc_list and
// gc_remove; user_usage_log_add on a usage log object, with
// user_usage_log_read, user_usage_log_trim and usage_log_clear; and on an
// index shard bucket_check_index and bucket_rebuild_index, as rgwCheckIndex
// says. A bucket_list asking for entries and dir_suggest_changes are emulated
// as rgwBucketListEntries and rgwDirSuggestChanges say. The bucket index
// methods read the header and entries from the stored object, as
// cls_cxx_map_read_header and cls_cxx_map_get_val do, and encode what they
// store and reply at the Squid release, whose rgw_bucket_dir_header lacks
// only Tentacle's reshard log count. Each write flag is the method's
// CLS_METHOD_WR in the class's registrations
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4691-L4759 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5107-L5178 at v20.2.4).
var _ = rgwTable(methodTable{
	"bucket_init_index":          {fn: noOutput(rgwBucketInitIndex), write: true},
	"bucket_list":                {fn: rgwBucketList},
	"bucket_prepare_op":          {fn: noOutput(rgwBucketPrepareOp), write: true},
	"bucket_complete_op":         {fn: noOutput(rgwBucketCompleteOp), write: true},
	"guard_bucket_resharding":    {fn: noOutput(rgwGuardBucketResharding)},
	"get_bucket_resharding":      {fn: rgwGetBucketResharding},
	"set_bucket_resharding":      {fn: noOutput(rgwSetBucketResharding), write: true},
	"mp_upload_part_info_update": {fn: noOutput(rgwMPUploadPartInfoUpdate), write: true},
	"obj_remove":                 {fn: noOutput(rgwObjRemove), write: true},
	"obj_store_pg_ver":           {fn: noOutput(rgwObjStorePGVer), write: true},
	"obj_check_mtime":            {fn: noOutput(rgwObjCheckMtime)},
	"gc_set_entry":               {fn: noOutput(rgwGCSetEntry), write: true},
	"gc_list":                    {fn: rgwGCList},
	"gc_remove":                  {fn: noOutput(rgwGCRemove), write: true},
	"user_usage_log_add":         {fn: noOutput(rgwUserUsageLogAdd), write: true},
	"user_usage_log_read":        {fn: rgwUserUsageLogRead},
	"user_usage_log_trim":        {fn: noOutput(rgwUserUsageLogTrim), write: true},
	"usage_log_clear":            {fn: noOutput(rgwUsageLogClear), write: true},
	"dir_suggest_changes":        {fn: noOutput(rgwDirSuggestChanges), write: true},
	"bucket_check_index":         {fn: rgwBucketCheckIndex},
	"bucket_rebuild_index":       {fn: noOutput(rgwBucketRebuildIndex), write: true},
})

// rgwObjRemove is rgw_obj_remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2410-L2482 at v19.2.6):
// the object is removed, and when it held xattrs under one of the request's
// prefixes it is created again, empty, with only those. It reads the xattrs
// from the stored object, as cls_cxx_getxattrs does, and a missing object is
// cls_cxx_remove's ENOENT. radosgw sends it after a create, so the steps that
// follow in the op build the new head on what it leaves.
func rgwObjRemove(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeObjRemoveOp)
	if rval < 0 {
		return rval
	}
	kept := map[string][]byte{}
	if len(op.KeepAttrPrefixes) > 0 && call.Stored != nil {
		for name, v := range call.Stored.Xattrs {
			if slices.ContainsFunc(op.KeepAttrPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
				kept[name] = slices.Clone(v)
			}
		}
	}
	if rval := call.Remove(); rval < 0 {
		return rval
	}
	if len(kept) > 0 {
		maps.Copy(call.Create().Xattrs, kept)
	}
	return 0
}

// rgwObjStorePGVer is rgw_obj_store_pg_ver
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2484-L2507 at v19.2.6):
// the named xattr takes cls_current_version, the placement group's last user
// version, as 8 little-endian bytes. The fake has no placement group and
// stores the version the object had before the op, 0 for a new one.
func rgwObjStorePGVer(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeStorePGVerOp)
	if rval < 0 {
		return rval
	}
	var ver uint64
	if call.Stored != nil {
		ver = call.Stored.Version
	}
	call.Create().Xattrs[op.Attr] = binary.LittleEndian.AppendUint64(nil, ver)
	return 0
}

// rgwObjCheckMtime is rgw_obj_check_mtime
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2553-L2615 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2721-L2783 at v20.2.4,
// the same code): the object's mtime, the zero real_time when it does not
// exist, compared with the request's under its type, both cut to whole
// seconds unless the request asks for high precision; a comparison that
// fails is ECANCELED, and an unknown type EINVAL. cls_cxx_stat2 sees the
// object as the op has left it so far.
func rgwObjCheckMtime(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeCheckMtimeOp)
	if rval < 0 {
		return rval
	}
	obj, want := time.Unix(0, 0), op.Mtime
	if o := call.Object(); o != nil {
		obj = o.Mtime
	}
	if !op.HighPrecisionTime {
		obj, want = time.Unix(obj.Unix(), 0), time.Unix(want.Unix(), 0)
	}
	var ok bool
	switch op.Type {
	case rgwcls.MtimeEQ:
		ok = obj.Equal(want)
	case rgwcls.MtimeLT:
		ok = obj.Before(want)
	case rgwcls.MtimeLE:
		ok = !obj.After(want)
	case rgwcls.MtimeGT:
		ok = obj.After(want)
	case rgwcls.MtimeGE:
		ok = !obj.Before(want)
	default:
		return -int32(syscall.EINVAL)
	}
	if !ok {
		return -int32(syscall.ECANCELED)
	}
	return 0
}

// The omap key prefixes of the omap-era gc queue, gc_index_prefixes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3838-L3842 at v19.2.6):
// the entry under its tag, and again under its due time.
const (
	gcNameIndex = "0_"
	gcTimeIndex = "1_"
)

// gcTimeKey is get_time_key
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L119-L125 at v19.2.6):
// seconds and nanoseconds, zero-padded to 11 and 9 digits, the seconds
// the 32 bits ceph_timespec keeps.
func gcTimeKey(t time.Time) string {
	return fmt.Sprintf("%011d.%09d", uint32(t.Unix()), t.Nanosecond()) //nolint:gosec // ceph_timespec's tv_sec is 32 bits
}

// rgwGCSetEntry is rgw_cls_gc_set_entry through gc_update_entry
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3896-L3941
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3964-L3978 at v19.2.6):
// an entry already under the tag loses its time-index key, and the entry,
// due ExpirationSecs from the class's clock, is stored under the tag and
// under its time.
func rgwGCSetEntry(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeGCSetEntryOp)
	if rval < 0 {
		return rval
	}
	info := op.Info
	if call.Stored != nil {
		if b, ok := call.Stored.Omap[gcNameIndex+info.Tag]; ok {
			d := denc.NewDecoder(b)
			old := rgwcls.DecodeGCObjInfo(d)
			if d.Err() != nil {
				return -int32(syscall.EIO)
			}
			if obj := call.Object(); obj != nil {
				delete(obj.Omap, gcTimeIndex+gcTimeKey(old.Time))
			}
		}
	}
	info.Time = call.Now().Add(time.Duration(op.ExpirationSecs) * time.Second)
	obj := call.Create()
	obj.Omap[gcNameIndex+info.Tag] = encodeSquid(info)
	obj.Omap[gcTimeIndex+gcTimeKey(info.Time)] = encodeSquid(info)
	return 0
}

// gcListDefaultMax is GC_LIST_ENTRIES_DEFAULT, what gc_list lists for a max
// of 0
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4117 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4519 at v20.2.4).
const gcListDefaultMax = 128

// rgwGCList is rgw_cls_gc_list through gc_iterate_entries
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3996-L4126 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4398-L4528 at v20.2.4):
// the time index after the marker, or from its start, in key order, so
// earliest due first, one omap read of max keys (128 for 0); a key outside
// the time index ends the listing, as does, when only the expired are asked
// for, a key at or past the class's clock. The next marker is the last key
// returned, set only while the read was truncated. It reads the stored
// omap, as cls_cxx_map_get_vals does.
func rgwGCList(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeGCListOp)
	if rval < 0 {
		return nil, rval
	}
	if call.Stored == nil {
		return nil, -int32(syscall.ENOENT)
	}
	start := op.Marker
	if start == "" {
		start = gcTimeIndex
	}
	var end string
	if op.ExpiredOnly {
		end = gcTimeIndex + gcTimeKey(call.Now())
	}
	maxEntries := uint64(op.Max)
	if maxEntries == 0 {
		maxEntries = gcListDefaultMax
	}
	vals, more := omapPage(call.Stored.Omap, start, "", maxEntries)
	ret := rgwcls.GCListRet{Truncated: more}
	var last string
	for _, k := range slices.Sorted(maps.Keys(vals)) {
		if (end != "" && k >= end) || !strings.HasPrefix(k, gcTimeIndex) {
			ret.Truncated = false
			break
		}
		d := denc.NewDecoder(vals[k])
		info := rgwcls.DecodeGCObjInfo(d)
		if d.Err() != nil {
			return nil, -int32(syscall.EIO)
		}
		ret.Entries = append(ret.Entries, info)
		last = k
	}
	if ret.Truncated {
		ret.NextMarker = last
	}
	return encodeSquid(ret), 0
}

// rgwGCRemove is rgw_cls_gc_remove through gc_remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4128-L4173 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4530-L4575 at v20.2.4):
// each tag's name-index entry, and the time-index key of the due time it
// records, go; a tag the shard does not hold is skipped. It reads each
// entry from the stored omap, as cls_cxx_map_get_val does.
func rgwGCRemove(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeGCRemoveOp)
	if rval < 0 {
		return rval
	}
	if call.Stored == nil {
		return 0
	}
	for _, tag := range op.Tags {
		b, ok := call.Stored.Omap[gcNameIndex+tag]
		if !ok {
			continue
		}
		d := denc.NewDecoder(b)
		info := rgwcls.DecodeGCObjInfo(d)
		if d.Err() != nil {
			return -int32(syscall.EIO)
		}
		if obj := call.Object(); obj != nil {
			delete(obj.Omap, gcTimeIndex+gcTimeKey(info.Time))
			delete(obj.Omap, gcNameIndex+tag)
		}
	}
	return 0
}

// rgwDirHeader is read_bucket_header
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L464-L485 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L514-L535 at v20.2.4):
// the stored header, the zero header when the shard has none, and EIO
// when it does not decode.
func rgwDirHeader(call *ClassCall) (h rgwcls.DirHeader, rval int32) {
	if call.Stored == nil || len(call.Stored.OmapHdr) == 0 {
		return rgwcls.DirHeader{}, 0
	}
	d := denc.NewDecoder(call.Stored.OmapHdr)
	h = rgwcls.DecodeDirHeader(d)
	if d.Err() != nil {
		return rgwcls.DirHeader{}, -int32(syscall.EIO)
	}
	return h, 0
}

// rgwBucketInitIndex is rgw_bucket_init_index
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L741-L764 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L837-L860 at v20.2.4):
// a shard that already has a header is EINVAL, "index already
// initialized", and any other gets an empty header at version 1, as
// write_bucket_header counts the write. radosgw precedes the call with an
// exclusive create, so it meets EEXIST from the create first.
func rgwBucketInitIndex(call *ClassCall) int32 {
	if call.Stored != nil && len(call.Stored.OmapHdr) != 0 {
		return -int32(syscall.EINVAL)
	}
	call.Create().OmapHdr = encodeSquid(rgwcls.DirHeader{Ver: 1})
	return 0
}

// rgwBucketList is rgw_bucket_list
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L487-L694 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L537-L744 at v20.2.4):
// for a request of no entries the shard's header, untruncated, and
// otherwise rgwBucketListEntries.
func rgwBucketList(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeListOp)
	if rval < 0 {
		return nil, rval
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return nil, rval
	}
	if op.NumEntries > 0 {
		return rgwBucketListEntries(call, op, h)
	}
	return encodeSquid(rgwcls.ListRet{Dir: rgwcls.Dir{Header: h}}), 0
}

// rgwPutEntry stores en under idx in the op's object, creating it, as
// cls_cxx_map_set_val does.
func rgwPutEntry(call *ClassCall, idx string, en rgwcls.DirEntry) {
	call.Create().Omap[idx] = encodeSquid(en)
}

// rgwRemoveEntry removes idx from the op's object, as cls_cxx_map_remove_key
// does.
func rgwRemoveEntry(call *ClassCall, idx string) {
	if obj := call.Object(); obj != nil {
		delete(obj.Omap, idx)
	}
}

// rgwRoundedSize is cls_rgw_get_rounded_size: up to the next 4 KiB.
func rgwRoundedSize(n uint64) uint64 { return (n + 4095) &^ 4095 }

// rgwUnaccount is unaccount_entry
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L887-L898 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1017-L1028 at v20.2.4):
// an existing entry's size leaves its category's stats.
func rgwUnaccount(h *rgwcls.DirHeader, en rgwcls.DirEntry) {
	if !en.Exists {
		return
	}
	if h.Stats == nil {
		h.Stats = map[uint8]rgwcls.CategoryStats{}
	}
	st := h.Stats[en.Meta.Category]
	st.NumEntries--
	st.TotalSize -= en.Meta.AccountedSize
	st.TotalSizeRounded -= rgwRoundedSize(en.Meta.AccountedSize)
	st.ActualSize -= en.Meta.Size
	h.Stats[en.Meta.Category] = st
}

// rgwBucketPrepareOp is rgw_bucket_prepare_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L803-L885 at v19.2.7):
// a request without a tag is EINVAL; the entry is the one rgwReadKeyEntry
// reads, so a version's under its instance key and, for a name whose plain
// entry is a version marker, the null version's data; a key with no entry
// gets an unset one carrying the request's locator, under the last key the
// read tried; the tag joins the entry's pending_map in state PENDING_MODIFY
// with the op and the time. Tentacle's
// (https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L923-L1015)
// also reads the header first, failing on one that does not decode, and
// guards itself, which the guard call radosgw sends ahead of it already does
// here.
func rgwBucketPrepareOp(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodePrepareOp)
	if rval < 0 {
		return rval
	}
	if op.Tag == "" {
		return -int32(syscall.EINVAL)
	}
	idx, en, rval := rgwReadKeyEntry(call, op.Key, false)
	switch {
	case rval == -int32(syscall.ENOENT):
		en = rgwcls.NewDirEntry()
		en.Key, en.Locator = op.Key, op.Locator
	case rval < 0:
		return rval
	}
	en.PendingMap = append(en.PendingMap, rgwcls.PendingEntry{Tag: op.Tag, Info: rgwcls.PendingInfo{
		State: rgwcls.PendingModify, Timestamp: call.Now(), Op: uint8(op.Op),
	}})
	rgwPutEntry(call, idx, en)
	return 0
}

// rgwBucketCompleteOp is rgw_bucket_complete_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1006-L1241 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1136-L1372 at v20.2.4).
// A header that cannot be read is EINVAL, and a tag that is not pending is
// EINVAL. The entry is the one rgwReadKeyEntry reads, and the completion
// writes only the key that read stopped at, so a version's instance entry
// and never its list entry, its flags cut to FLAG_VER. A completion whose
// epoch is not above the entry's on the same pool becomes a cancel, but the
// request's version is copied onto the entry first, so a cancel writes it
// back (tracker #80894, reproduced on purpose). A cancel drops the tag and
// removes an entry left neither existing nor pending; a DEL unaccounts the
// entry and removes it, or keeps it as not existing while another tag is
// pending; an ADD replaces the entry's meta and accounts it. Every
// completion then removes and unaccounts the entries remove_objs names, each
// read as rgwReadKeyEntry reads it, skipping one it cannot read, and writes
// the header with its version counted.
func rgwBucketCompleteOp(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeCompleteOp)
	if rval < 0 {
		return rval
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return -int32(syscall.EINVAL)
	}
	idx, en, rval := rgwReadKeyEntry(call, op.Key, false)
	ondisk := rval == 0
	switch {
	case rval == -int32(syscall.ENOENT):
		en = rgwcls.NewDirEntry()
		en.Key, en.Ver, en.Meta, en.Locator = op.Key, op.Ver, op.Meta, op.Locator
	case rval < 0:
		return rval
	}
	en.IndexVer = h.Ver
	en.Flags &= rgwcls.FlagVer
	if op.Tag != "" {
		i := slices.IndexFunc(en.PendingMap, func(p rgwcls.PendingEntry) bool { return p.Tag == op.Tag })
		if i < 0 {
			return -int32(syscall.EINVAL)
		}
		en.PendingMap = slices.Delete(en.PendingMap, i, i+1)
	}
	mod := op.Op
	if (op.Tag == "" || mod != rgwcls.OpCancel) &&
		op.Ver.Pool == en.Ver.Pool && op.Ver.Epoch != 0 && op.Ver.Epoch <= en.Ver.Epoch {
		mod = rgwcls.OpCancel
	}
	en.Ver = op.Ver
	switch mod {
	case rgwcls.OpCancel:
		if op.Tag != "" {
			if !en.Exists && len(en.PendingMap) == 0 {
				rgwRemoveEntry(call, idx)
			} else {
				rgwPutEntry(call, idx, en)
			}
		}
	case rgwcls.OpDel:
		rgwUnaccount(&h, en)
		en.Meta = op.Meta
		switch {
		case !ondisk:
		case len(en.PendingMap) == 0:
			rgwRemoveEntry(call, idx)
		default:
			en.Exists = false
			rgwPutEntry(call, idx, en)
		}
	case rgwcls.OpAdd:
		rgwUnaccount(&h, en)
		if h.Stats == nil {
			h.Stats = map[uint8]rgwcls.CategoryStats{}
		}
		st := h.Stats[op.Meta.Category]
		st.NumEntries++
		st.TotalSize += op.Meta.AccountedSize
		st.TotalSizeRounded += rgwRoundedSize(op.Meta.AccountedSize)
		st.ActualSize += op.Meta.Size
		h.Stats[op.Meta.Category] = st
		en.Meta, en.Key, en.Exists, en.Tag = op.Meta, op.Key, true, op.Tag
		rgwPutEntry(call, idx, en)
	}
	for _, k := range op.RemoveObjs {
		ridx, old, rval := rgwReadKeyEntry(call, k, false)
		if rval < 0 {
			continue
		}
		rgwUnaccount(&h, old)
		rgwRemoveEntry(call, ridx)
	}
	h.Ver++
	call.Create().OmapHdr = encodeSquid(h)
	return 0
}

// rgwGuardBucketResharding is rgw_guard_bucket_resharding
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4577-L4602 at v19.2.6):
// ret_err, verbatim, when the header's reshard status is anything but
// not-resharding. Tentacle's
// (https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4995-L5016,
// through guard_bucket_resharding at
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L906-L921)
// refuses only an in-progress reshard, and a log-record one whose reshard log
// has reached rgw_reshardlog_threshold.
func rgwGuardBucketResharding(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeGuardOp)
	if rval < 0 {
		return rval
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return rval
	}
	if h.NewInstance.ReshardStatus != rgwcls.ReshardNone {
		return op.RetErr
	}
	return 0
}

// rgwGetBucketResharding is rgw_get_bucket_resharding
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4604-L4631 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5018-L5045 at v20.2.4):
// the header's reshard entry, framed as cls_rgw_get_bucket_resharding_ret.
func rgwGetBucketResharding(call *ClassCall) (out []byte, rval int32) {
	d := denc.NewDecoder(call.In)
	d.EndStruct(d.BeginStruct(1))
	if d.Err() != nil {
		return nil, -int32(syscall.EINVAL)
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return nil, rval
	}
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	h.NewInstance.Encode(e, denc.Squid)
	e.EndStruct(f)
	return e.Bytes(), 0
}

// rgwSetBucketResharding is rgw_set_bucket_resharding
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4528-L4551 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4946-L4969 at v20.2.4):
// the header takes the request's reshard status and is written with its
// version counted.
func rgwSetBucketResharding(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeSetReshardingOp)
	if rval < 0 {
		return rval
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return rval
	}
	h.NewInstance.ReshardStatus = op.Entry.ReshardStatus
	h.Ver++
	call.Create().OmapHdr = encodeSquid(h)
	return 0
}

// Entry decodes the bucket index entry the stored object keeps under key,
// reporting false when the object or the key is missing. An entry that does
// not decode panics, failing the spec.
func (c *Cluster) Entry(pool, ns, oid, key string) (rgwcls.DirEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return storedOmap(c.store(pool, ns).objects[oid], pool, ns, oid, key, "index entry", rgwcls.DecodeDirEntry)
}

// InstanceEntry decodes the instance entry the stored shard keeps for key,
// under rgwcls.InstanceIndexKey: for an empty instance the null version's
// data entry, never its delete marker's. It reports false when the shard or
// the entry is missing. An entry that does not decode panics, failing the
// spec.
func (c *Cluster) InstanceEntry(pool, ns, oid string, key rgwcls.ObjKey) (rgwcls.DirEntry, bool) {
	return c.Entry(pool, ns, oid, rgwcls.InstanceIndexKey(key, false))
}

// ListEntries decodes the list entries the stored shard keeps for name's
// versions, the keys after its plain key that rgwcls.ListIndexKey gives, in
// key order, which for epochs below 2^32 is newest first. It is nil when
// there are none. An entry that does not decode panics, failing the spec.
func (c *Cluster) ListEntries(pool, ns, oid, name string) []rgwcls.DirEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj := c.store(pool, ns).objects[oid]
	if obj == nil {
		return nil
	}
	var out []rgwcls.DirEntry
	for _, k := range slices.Sorted(maps.Keys(obj.Omap)) {
		if strings.HasPrefix(k, name+"\x00") {
			en, _ := storedOmap(obj, pool, ns, oid, k, "list entry", rgwcls.DecodeDirEntry)
			out = append(out, en)
		}
	}
	return out
}

// OLHEntry decodes the OLH entry the stored shard keeps for name, under
// rgwcls.OLHDataKey, reporting false when the shard or the entry is missing.
// An entry that does not decode panics, failing the spec.
func (c *Cluster) OLHEntry(pool, ns, oid, name string) (rgwcls.OLHEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return storedOmap(c.store(pool, ns).objects[oid], pool, ns, oid, rgwcls.OLHDataKey(name), "OLH entry", rgwcls.DecodeOLHEntry)
}

// storedOmap decodes the value obj keeps under key with dec, reporting false
// when obj or the key is missing, and panics, naming what it read, when the
// value does not decode. The cluster's lock is held.
func storedOmap[T any](obj *Object, pool, ns, oid, key, what string, dec func(*denc.Decoder) T) (v T, found bool) {
	if obj == nil {
		return v, false
	}
	b, ok := obj.Omap[key]
	if !ok {
		return v, false
	}
	d := denc.NewDecoder(b)
	v = dec(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("fakerados: %s %q of %s/%s/%s does not decode: %v", what, key, pool, ns, oid, err))
	}
	return v, true
}

// Header decodes the bucket index header the stored object keeps, the zero
// header when the object or its header is missing. A header that does not
// decode panics, failing the spec.
func (c *Cluster) Header(pool, ns, oid string) rgwcls.DirHeader {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj := c.store(pool, ns).objects[oid]
	if obj == nil || len(obj.OmapHdr) == 0 {
		return rgwcls.DirHeader{}
	}
	d := denc.NewDecoder(obj.OmapHdr)
	h := rgwcls.DecodeDirHeader(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("fakerados: index header of %s/%s/%s does not decode: %v", pool, ns, oid, err))
	}
	return h
}

// rgwMPUploadPartInfoUpdate is rgw_mp_upload_part_info_update
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4360-L4400 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4762-L4802 at v20.2.4).
// A request that does not decode is EINVAL. The part info stored under the
// request's key, read as read_omap_entry does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L915-L932 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1045-L1062 at v20.2.4),
// is EIO when it does not decode and a default one when the key or the object is
// missing. Its manifest's prefix, when the manifest is not empty, and its
// past prefixes join the new info's past prefixes; a new prefix among them
// is EEXIST; otherwise the merged info is stored under the key, creating the
// object as cls_cxx_map_set_val does. radosgw sends assert_exists ahead of
// the call, which fails first on a missing meta object. The info is stored
// at the Squid release, as a Squid OSD re-encodes it, dropping a Tentacle
// checksum.
func rgwMPUploadPartInfoUpdate(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeMPUploadPartInfoUpdateOp)
	if rval < 0 {
		return rval
	}
	info, rval := decodeRequest(op.Info, meta.DecodeUploadPartInfo)
	if rval < 0 {
		return rval
	}
	var stored meta.UploadPartInfo
	if call.Stored != nil {
		if b, ok := call.Stored.Omap[op.PartKey]; ok {
			d := denc.NewDecoder(b)
			stored = meta.DecodeUploadPartInfo(d)
			if d.Err() != nil {
				return -int32(syscall.EIO)
			}
		}
	}
	past := info.PastPrefixes
	if !manifestEmpty(stored.Manifest) {
		past = append(past, stored.Manifest.Prefix)
	}
	past = append(past, stored.PastPrefixes...)
	slices.Sort(past)
	info.PastPrefixes = slices.Compact(past)
	if _, found := slices.BinarySearch(info.PastPrefixes, info.Manifest.Prefix); found {
		return -int32(syscall.EEXIST)
	}
	call.Create().Omap[op.PartKey] = encodeSquid(info)
	return 0
}

// manifestEmpty is RGWObjManifest::empty: no pieces when explicit, no rules
// otherwise.
func manifestEmpty(m meta.Manifest) bool {
	if m.ExplicitObjs {
		return len(m.Objs) == 0
	}
	return len(m.Rules) == 0
}

// rgwUserUsageLogAdd is rgw_user_usage_log_add
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3563-L3618 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3965-L4020 at v20.2.4).
// A request or a stored record that does not decode is EINVAL. Each entry
// is filed under its payer, or its owner when it has none, merged into
// the record already stored for its hour, that user and its bucket, and
// stored under both the by-time and the by-user key, creating the object.
// The stored record is read from the store, so of two entries of one
// request with the same key the second overwrites the first; radosgw
// never sends two.
func rgwUserUsageLogAdd(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeUsageAddOp)
	if rval < 0 {
		return rval
	}
	for _, en := range op.Info.Entries {
		payerKeyed := !usageUserEmpty(en.Payer)
		byTime := usageKeyByTime(en.Epoch, usageRecordUser(en), en.Bucket)
		if call.Stored != nil {
			if b, ok := call.Stored.Omap[byTime]; ok {
				stored, rval := decodeRequest(b, rgwcls.DecodeUsageLogEntry)
				if rval < 0 {
					return rval
				}
				en = aggregateUsage(en, stored)
			}
		}
		// The class keeps a pointer to the entry's payer or owner, so the
		// by-user key takes that field as the merge left it.
		user := en.Owner
		if payerKeyed {
			user = en.Payer
		}
		rec := encodeSquid(en)
		omap := call.Create().Omap
		omap[byTime] = rec
		omap[usageKeyByUser(user, en.Epoch, en.Bucket)] = slices.Clone(rec)
	}
	return 0
}

// usageUserEmpty is rgw_user::empty of a user's string form: its id is
// empty.
func usageUserEmpty(s string) bool { return meta.ParseUserID(s).ID == "" }

// usageRecordUser is the user cls_rgw files en under: its payer, or its
// owner when it has no payer.
func usageRecordUser(en rgwcls.UsageLogEntry) string {
	if usageUserEmpty(en.Payer) {
		return en.Owner
	}
	return en.Payer
}

// usageKeyByTime is usage_record_name_by_time
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3536-L3541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3938-L3943 at v20.2.4).
func usageKeyByTime(epoch uint64, user, bucket string) string {
	return fmt.Sprintf("%011d_%s_%s", epoch, user, bucket)
}

// usageKeyByUser is usage_record_name_by_user
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3543-L3548 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3945-L3950 at v20.2.4).
func usageKeyByUser(user string, epoch uint64, bucket string) string {
	return fmt.Sprintf("%s_%011d_%s", user, epoch, bucket)
}

// aggregateUsage is rgw_usage_log_entry::aggregate
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L1005-L1023 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1044-L1062 at v20.2.4)
// of e into en: en takes e's owner, payer, bucket and epoch only when its own
// owner is empty, and every category of e is added to en's category and to
// its total.
func aggregateUsage(en, e rgwcls.UsageLogEntry) rgwcls.UsageLogEntry {
	if usageUserEmpty(en.Owner) {
		en.Owner, en.Payer, en.Bucket, en.Epoch = e.Owner, e.Payer, e.Bucket, e.Epoch
	}
	sums := maps.Clone(en.UsageMap)
	if sums == nil {
		sums = map[string]rgwcls.UsageData{}
	}
	for cat, d := range e.UsageMap {
		sums[cat] = addUsageData(sums[cat], d)
		en.TotalUsage = addUsageData(en.TotalUsage, d)
	}
	en.UsageMap = sums
	en.S3SelectUsage.BytesProcessed += e.S3SelectUsage.BytesProcessed
	en.S3SelectUsage.BytesReturned += e.S3SelectUsage.BytesReturned
	return en
}

// addUsageData is rgw_usage_data::aggregate.
func addUsageData(a, b rgwcls.UsageData) rgwcls.UsageData {
	return rgwcls.UsageData{
		BytesSent:     a.BytesSent + b.BytesSent,
		BytesReceived: a.BytesReceived + b.BytesReceived,
		Ops:           a.Ops + b.Ops,
		SuccessfulOps: a.SuccessfulOps + b.SuccessfulOps,
	}
}

// usageMaxEntries is both MAX_ENTRIES, the window user_usage_log_read
// reads when the request names none, and MAX_USAGE_TRIM_ENTRIES, the window
// of every user_usage_log_trim
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3739
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3794 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4141
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4196 at v20.2.4).
const usageMaxEntries = 1000

// usageIterateRange is usage_iterate_range
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3620-L3704 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4022-L4106 at v20.2.4):
// one omap read of maxEntries keys after *iter, or, when *iter is empty,
// after the user's by-user prefix at start, or without a user the by-time
// prefix at start. Each key read moves *iter; a key without the user's
// prefix, or without a user one at or past the by-time prefix of end, ends
// the walk untruncated. Each record is decoded, EINVAL when it does not
// decode, and skipped unless of bucket when one is named and at or after
// start; one at or past end ends the walk untruncated. The prefix test lets
// in the by-user keys of another user whose id begins with user and "_",
// and the by-time walk any by-user key that sorts below end's key
// (docs/ceph-upstream-bugs.md). The callback sees every other record.
// truncated is the omap read's, unless the walk ended.
func usageIterateRange(call *ClassCall, start, end uint64, user, bucket string, iter *string, maxEntries uint32, cb func(e rgwcls.UsageLogEntry)) (truncated bool, rval int32) {
	byUser := user != ""
	var startKey, endKey, userKey string
	if byUser {
		userKey = user + "_"
	} else {
		endKey = fmt.Sprintf("%011d", end)
	}
	switch {
	case *iter != "":
		startKey = *iter
	case byUser:
		startKey = fmt.Sprintf("%s_%011d_", user, start)
	default:
		startKey = fmt.Sprintf("%011d", start)
	}
	if call.Stored == nil {
		return false, -int32(syscall.ENOENT)
	}
	vals, more := omapPage(call.Stored.Omap, startKey, "", uint64(maxEntries))
	for _, k := range slices.Sorted(maps.Keys(vals)) {
		*iter = k
		if (!byUser && k >= endKey) || (byUser && !strings.HasPrefix(k, userKey)) {
			return false, 0
		}
		e, rval := decodeRequest(vals[k], rgwcls.DecodeUsageLogEntry)
		if rval < 0 {
			return more, rval
		}
		if (bucket != "" && bucket != e.Bucket) || e.Epoch < start {
			continue
		}
		if e.Epoch >= end {
			return false, 0
		}
		cb(e)
	}
	return more, 0
}

// rgwUserUsageLogRead is rgw_user_usage_log_read with usage_log_read_cb
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3706-L3750 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4108-L4152 at v20.2.4):
// the records usageIterateRange walks, merged per payer, or owner when
// there is none, and bucket by aggregateUsage, with the last key walked
// as the next iterator when the read was truncated. A request that does
// not decode is EINVAL.
func rgwUserUsageLogRead(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeUsageReadOp)
	if rval < 0 {
		return nil, rval
	}
	maxEntries := op.MaxEntries
	if maxEntries == 0 {
		maxEntries = usageMaxEntries
	}
	ret := rgwcls.UsageReadRet{Usage: map[rgwcls.UserBucket]rgwcls.UsageLogEntry{}}
	iter := op.Iter
	ret.Truncated, rval = usageIterateRange(call, op.StartEpoch, op.EndEpoch, op.Owner, op.Bucket, &iter, maxEntries,
		func(e rgwcls.UsageLogEntry) {
			ub := rgwcls.UserBucket{User: usageRecordUser(e), Bucket: e.Bucket}
			ret.Usage[ub] = aggregateUsage(ret.Usage[ub], e)
		})
	if rval < 0 {
		return nil, rval
	}
	if ret.Truncated {
		ret.NextIter = iter
	}
	return encodeSquid(ret), 0
}

// rgwUserUsageLogTrim is rgw_user_usage_log_trim with usage_log_trim_cb
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3752-L3803 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4154-L4205 at v20.2.4):
// ENOENT for an object that does not exist, EINVAL for a request that does
// not decode, then one window of usageIterateRange from the range's start,
// each record it hands on removed under the by-time and by-user keys built
// from its owner. A record filed under its payer is therefore found and
// never removed, and a trim of its payer removes the owner's record of the
// same hour and bucket instead (tracker #72593, fixed in v21 only).
// ENODATA answers a window that found no record with nothing after it; a
// window of 1000 keys none of which matches the bucket filter answers 0
// without progress, every time (#58136).
func rgwUserUsageLogTrim(call *ClassCall) int32 {
	if call.Stored == nil {
		return -int32(syscall.ENOENT)
	}
	op, rval := decodeRequest(call.In, rgwcls.DecodeUsageTrimOp)
	if rval < 0 {
		return rval
	}
	var iter string
	found := false
	more, rval := usageIterateRange(call, op.StartEpoch, op.EndEpoch, op.User, op.Bucket, &iter, usageMaxEntries,
		func(e rgwcls.UsageLogEntry) {
			found = true
			omap := call.Object().Omap
			delete(omap, usageKeyByTime(e.Epoch, e.Owner, e.Bucket))
			delete(omap, usageKeyByUser(e.Owner, e.Epoch, e.Bucket))
		})
	if rval < 0 {
		return rval
	}
	if !more && !found {
		return -int32(syscall.ENODATA)
	}
	return 0
}

// rgwUsageLogClear is rgw_usage_log_clear
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3805-L3815 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4207-L4217 at v20.2.4):
// the omap cleared, header included, as cls_cxx_map_clear does; an object
// that does not exist is already clear.
func rgwUsageLogClear(call *ClassCall) int32 {
	if obj := call.Object(); obj != nil {
		clear(obj.Omap)
		obj.OmapHdr = nil
	}
	return 0
}

// UsageEntries decodes the usage records the stored object keeps, each once,
// in the order of their by-time keys: by hour, then user, then bucket. It is
// nil when the object does not exist. A record that does not decode panics,
// failing the spec.
func (c *Cluster) UsageEntries(pool, ns, oid string) []rgwcls.UsageLogEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj := c.store(pool, ns).objects[oid]
	if obj == nil {
		return nil
	}
	var out []rgwcls.UsageLogEntry
	for _, key := range slices.Sorted(maps.Keys(obj.Omap)) {
		d := denc.NewDecoder(obj.Omap[key])
		en := rgwcls.DecodeUsageLogEntry(d)
		if err := d.Err(); err != nil {
			panic(fmt.Sprintf("fakerados: usage record %q of %s/%s/%s does not decode: %v", key, pool, ns, oid, err))
		}
		// Every record is also stored under its by-user key.
		if key == usageKeyByTime(en.Epoch, usageRecordUser(en), en.Bucket) {
			out = append(out, en)
		}
	}
	return out
}

// rgwBIAdvanceAndRetry is RGWBIAdvanceAndRetryError, -EFBIG
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_const.h#L11 at v19.2.6
// and v20.2.4).
const rgwBIAdvanceAndRetry = -int32(syscall.EFBIG)

// rgwListMaxAttempts is rgw_bucket_list's max_attempts, its bound on the
// omap reads one call makes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L492 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L542 at v20.2.4).
const rgwListMaxAttempts = 8

// rgwBucketListEntries is the rest of rgw_bucket_list
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L525-L694 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L575-L744 at v20.2.4)
// for a request of entries. It starts after the key rgwListStart resolves,
// past the subdirectory a delimiter-terminated start key names, and makes up
// to eight reads of the entries still wanted, each as rgwGetObjVals reads.
// Each entry is decoded, EINVAL when it does not decode; a key
// decode_list_index_key refuses and a version marker are skipped; without
// list_versions an entry that is not visible, or named as the start key, is
// skipped with every version of its name. With a delimiter a name holding it
// after the prefix becomes one common-prefix entry and the listing skips past
// the subdirectory. The reply is truncated when the last read left keys and
// found some, with the last key visited as its marker, and a truncated reply
// of no entries is RGWBIAdvanceAndRetryError, the reply still encoded.
func rgwBucketListEntries(call *ClassCall, op rgwcls.ListOp, h rgwcls.DirHeader) (out []byte, rval int32) {
	var keys []string
	if call.Stored != nil {
		keys = slices.Sorted(maps.Keys(call.Stored.Omap))
	}
	ret := rgwcls.ListRet{Dir: rgwcls.Dir{Header: h, Entries: map[string]rgwcls.DirEntry{}}}
	m := ret.Dir.Entries
	start := rgwListStart(call, op.StartObj)
	var startEntry rgwcls.ObjKey
	var prevKey, prevPrefix string
	done, more := false, true
	if op.Delimiter != "" && start > op.FilterPrefix && strings.HasSuffix(start, op.Delimiter) {
		start += "\xFF"
	}
	for attempt := 0; attempt < rgwListMaxAttempts && more && !done && uint32(len(m)) < op.NumEntries; attempt++ { //nolint:gosec // an entry count fits a u32
		var page []string
		page, more = rgwGetObjVals(keys, start, op.FilterPrefix, int(op.NumEntries)-len(m))
		done = len(page) == 0
		for i := 0; i < len(page); i++ {
			k := page[i]
			d := denc.NewDecoder(call.Stored.Omap[k])
			en := rgwcls.DecodeDirEntry(d)
			if d.Err() != nil {
				return nil, -int32(syscall.EINVAL)
			}
			start, startEntry = k, en.Key
			key, err := rgwcls.DecodeListIndexKey(k)
			if err != nil || en.Flags&rgwcls.FlagVerMarker != 0 {
				continue
			}
			if !op.ListVersions && (!rgwVisible(en) || op.StartObj.Name == key.Name) {
				start = rgwcls.AfterVersions(key.Name)
				startEntry = rgwcls.ObjKey{Name: start}
				i = sort.SearchStrings(page, start) - 1
				continue
			}
			if op.Delimiter != "" && len(op.FilterPrefix) <= len(key.Name) {
				if pos := strings.Index(key.Name[len(op.FilterPrefix):], op.Delimiter); pos >= 0 {
					prefixKey := key.Name[:len(op.FilterPrefix)+pos+len(op.Delimiter)]
					if prefixKey == prevPrefix {
						continue
					}
					prevPrefix = prefixKey
					if uint32(len(m)) < op.NumEntries { //nolint:gosec // an entry count fits a u32
						proxy := rgwcls.NewDirEntry()
						proxy.Key = rgwcls.ObjKey{Name: prefixKey}
						proxy.Flags = rgwcls.FlagCommonPrefix
						m[prefixKey] = proxy
					}
					start = prefixKey + "\xFF"
					startEntry = rgwcls.ObjKey{Name: start}
					i = sort.SearchStrings(page, start) - 1
					continue
				}
			}
			if uint32(len(m)) < op.NumEntries && k != prevKey { //nolint:gosec // an entry count fits a u32
				m[k] = en
				prevKey = k
			}
		}
	}
	ret.IsTruncated = more && !done
	if ret.IsTruncated {
		ret.Marker = startEntry
	}
	out = encodeSquid(ret)
	if ret.IsTruncated && len(m) == 0 {
		return out, rgwBIAdvanceAndRetry
	}
	return out, 0
}

// rgwListStart is encode_list_index_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L440 at v20.2.4):
// the omap key a listing starts after. A key without an instance is its name.
// For an instance, "null" read as the empty one, it is the list key of the
// version's stored instance entry, from that entry's own key and epoch, and
// with no such entry the name's AfterVersions. When the instance entry cannot
// be read the key stays empty, since rgw_bucket_list ignores the failure
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L528-L529 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L578-L579 at v20.2.4),
// so the listing starts at the shard's first key, or its filter prefix's
// (docs/ceph-upstream-bugs.md).
func rgwListStart(call *ClassCall, key rgwcls.ObjKey) string {
	if key.Instance == "" {
		return key.Name
	}
	inst := key
	if inst.Instance == "null" {
		inst.Instance = ""
	}
	en, rval := rgwReadOmap(call, rgwcls.InstanceIndexKey(inst, false), rgwcls.DecodeDirEntry)
	switch {
	case rval == -int32(syscall.ENOENT):
		return rgwcls.AfterVersions(key.Name)
	case rval < 0:
		return ""
	}
	return rgwcls.ListIndexKey(en.Key, en.VersionedEpoch)
}

// rgwObjVals is cls_cxx_map_get_vals as the OSD serves it (OMAPGETVALS):
// the keys after start, from the filter prefix on when it sorts later, while
// they carry the prefix, at most n, with more set when another would follow.
func rgwObjVals(keys []string, start, prefix string, n int) (page []string, more bool) {
	for _, k := range keys {
		if k <= start || k < prefix {
			continue
		}
		if !strings.HasPrefix(k, prefix) {
			break
		}
		if len(page) >= n {
			return page, true
		}
		page = append(page, k)
	}
	return page, false
}

// The bounds of the 0x80 namespace, BI_PREFIX_CHAR and the byte after it:
// every key from biPrefixBegin up to biPrefixAfter starts with 0x80.
const (
	biPrefixBegin = "\x80"
	biPrefixAfter = "\x81"
)

// rgwGetObjVals is get_obj_vals
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L188-L265 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L238-L315 at v20.2.4):
// rgwObjVals, the keys in the 0x80 namespace dropped. A read whose keys all
// sort below 0x80, or all from 0x81 on, is kept whole. When it dropped keys,
// was truncated and kept fewer than n, a second read of the rest, after 0x81
// or the last key kept when that sorts later, tops it up and gives more.
func rgwGetObjVals(keys []string, start, prefix string, n int) (page []string, more bool) {
	page, more = rgwObjVals(keys, start, prefix, n)
	if len(page) == 0 || page[len(page)-1] < biPrefixBegin || page[0] >= biPrefixAfter {
		return page, more
	}
	page = slices.DeleteFunc(page, func(k string) bool { return k >= biPrefixBegin && k < biPrefixAfter })
	if len(page) == n || !more {
		return page, more
	}
	next := biPrefixAfter
	if len(page) > 0 && next < page[len(page)-1] {
		next = page[len(page)-1]
	}
	var rest []string
	rest, more = rgwObjVals(keys, next, prefix, n-len(page))
	return append(page, rest...), more
}

// rgwVisible is rgw_bucket_dir_entry::is_visible
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L446-L457 at v19.2.6):
// current, which an unversioned entry always is, and not a delete marker.
func rgwVisible(en rgwcls.DirEntry) bool {
	current := en.Flags&rgwcls.FlagVer == 0 || en.Flags&(rgwcls.FlagVer|rgwcls.FlagCurrent) == rgwcls.FlagVer|rgwcls.FlagCurrent
	return current && en.Flags&rgwcls.FlagDeleteMarker == 0
}

// rgwDefaultTagTimeout is CEPH_RGW_DEFAULT_TAG_TIMEOUT, and the default of
// rgw_pending_bucket_index_op_expiration, 120 seconds (cls_rgw_types.h at
// v19.2.6 and v20.2.4).
const rgwDefaultTagTimeout = 120 * time.Second

// rgwDirSuggestChanges is Squid's rgw_dir_suggest_changes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2192-L2408 at v19.2.7).
// A shard that does not exist is ENOENT, and a header that does not decode EIO.
// Each change is an op byte, its log flag stripped, and an entry; one that does
// not decode is EINVAL. The change applies under its key's rgwObjIndexKey, so
// a version's reaches its instance entry and never the list entry a listing
// found it under. A key the shard does not hold is skipped. The stored
// entry's pending ops older than the header's tag timeout, or 120 s, are
// dropped; a change made against an older index version than the entry's is
// skipped; while a pending op remains nothing changes. Otherwise the stored
// entry is unaccounted when it exists, the change's category gets a stats
// entry, zero when it had none, and a removal removes the entry while an update
// stores the suggested entry at the header's version and accounts it. The
// header is written, its version counted, when its stats changed. The fake
// keeps no bucket index log, so the log flag changes nothing, and Tentacle's
// own reshard guard is left to the guard call radosgw sends ahead of it.
func rgwDirSuggestChanges(call *ClassCall) int32 {
	if call.Stored == nil {
		return -int32(syscall.ENOENT)
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return rval
	}
	timeout := rgwDefaultTagTimeout
	if h.TagTimeout != 0 {
		timeout = time.Duration(h.TagTimeout) * time.Second //nolint:gosec // a tag timeout in seconds fits a Duration
	}
	if h.Stats == nil {
		h.Stats = map[uint8]rgwcls.CategoryStats{}
	}
	changed := false
	d := denc.NewDecoder(call.In)
	for d.Remaining() > 0 {
		op := d.U8() &^ rgwcls.SuggestLog
		change := rgwcls.DecodeDirEntry(d)
		if d.Err() != nil {
			return -int32(syscall.EINVAL)
		}
		idx := rgwObjIndexKey(change.Key)
		b, ok := call.Stored.Omap[idx]
		if !ok {
			continue
		}
		disk := rgwcls.NewDirEntry()
		if len(b) > 0 {
			dd := denc.NewDecoder(b)
			disk = rgwcls.DecodeDirEntry(dd)
			if dd.Err() != nil {
				return -int32(syscall.EINVAL)
			}
			now := call.Now()
			disk.PendingMap = slices.DeleteFunc(disk.PendingMap, func(p rgwcls.PendingEntry) bool {
				return now.After(p.Info.Timestamp.Add(timeout))
			})
		}
		if change.IndexVer < disk.IndexVer || len(disk.PendingMap) > 0 {
			continue
		}
		if disk.Exists {
			rgwUnaccount(&h, disk)
			changed = true
		}
		// The class takes the change category's rgw_bucket_category_stats
		// with std::map's operator[] before it looks at the op, so a removal
		// also leaves a zero entry for that category in the header it writes
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2328 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2498 at v20.2.4).
		if _, ok := h.Stats[change.Meta.Category]; !ok {
			h.Stats[change.Meta.Category] = rgwcls.CategoryStats{}
		}
		switch op {
		case rgwcls.SuggestRemove:
			rgwRemoveEntry(call, idx)
		case rgwcls.SuggestUpdate:
			st := h.Stats[change.Meta.Category]
			st.NumEntries++
			st.TotalSize += change.Meta.AccountedSize
			st.TotalSizeRounded += rgwRoundedSize(change.Meta.AccountedSize)
			st.ActualSize += change.Meta.Size
			h.Stats[change.Meta.Category] = st
			changed = true
			change.IndexVer = h.Ver
			rgwPutEntry(call, idx, change)
		}
	}
	if changed {
		h.Ver++
		call.Create().OmapHdr = encodeSquid(h)
	}
	return 0
}

// Suggestions decodes every dir_suggest_changes the write ops on the object
// carried, failed ones included, oldest first: what a listing suggested to
// the shard, whether or not the class applied it. An input that does not
// decode panics, failing the spec.
func (c *Cluster) Suggestions(pool, ns, oid string) []rgwcls.Suggestion {
	var out []rgwcls.Suggestion
	for _, w := range c.WritesTo(pool, ns, oid) {
		for _, step := range w.Steps() {
			ex, ok := step.(*radosclient.ExecStep)
			if !ok || ex.Class != "rgw" || ex.Method != "dir_suggest_changes" {
				continue
			}
			d := denc.NewDecoder(ex.In)
			for d.Remaining() > 0 {
				b := d.U8()
				s := rgwcls.Suggestion{Op: b &^ rgwcls.SuggestLog, Log: b&rgwcls.SuggestLog != 0, Entry: rgwcls.DecodeDirEntry(d)}
				if err := d.Err(); err != nil {
					panic(fmt.Sprintf("fakerados: dir_suggest_changes to %s/%s/%s does not decode: %v", pool, ns, oid, err))
				}
				out = append(out, s)
			}
		}
	}
	return out
}

// The 0x80 namespace's bounds as the class's plain listing skips it,
// BI_PREFIX_END being BI_PREFIX_CHAR and its last index prefix "9999_", and
// the instance entries' prefix
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L51-L75 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L51-L77 at v20.2.4).
const (
	biPrefixEnd      = "\x809999_"
	biInstancePrefix = "\x801000_"
)

// rgwCheckIndex is check_index
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3035-L3114 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3361-L3434 at v20.2.4)
// over a shard: its stored header, and the header its entries make, which
// carries the stored tag timeout, version and sync-stopped flag and nothing
// else of it. It counts in its category each existing plain entry without
// flags, a plain entry being one below the 0x80 namespace or after
// biPrefixEnd (list_plain_entries), and each existing instance entry, whatever
// its flags (list_instance_entries)
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2794-L2940 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3069-L3215 at v20.2.4).
// The OLH entries and the rest of the 0x80 namespace are not read. An entry
// it reads that does not decode is EIO. The fake answers ENOENT for a shard
// that does not exist, which is what the OSD answers bucket_check_index, a
// read; bucket_rebuild_index, a write, would instead read an empty header
// there
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7767-L7770 at v19.2.7)
// and create the shard holding a zero header at version 1. radosgw sends
// neither method to a shard it has not created.
func rgwCheckIndex(call *ClassCall) (existing, calc rgwcls.DirHeader, rval int32) {
	if call.Stored == nil {
		return rgwcls.DirHeader{}, rgwcls.DirHeader{}, -int32(syscall.ENOENT)
	}
	existing, rval = rgwDirHeader(call)
	if rval < 0 {
		return rgwcls.DirHeader{}, rgwcls.DirHeader{}, rval
	}
	calc = rgwcls.DirHeader{TagTimeout: existing.TagTimeout, Ver: existing.Ver, SyncStopped: existing.SyncStopped, Stats: map[uint8]rgwcls.CategoryStats{}}
	for _, k := range slices.Sorted(maps.Keys(call.Stored.Omap)) {
		plain := k < biPrefixBegin || k > biPrefixEnd
		if !plain && !strings.HasPrefix(k, biInstancePrefix) {
			continue
		}
		d := denc.NewDecoder(call.Stored.Omap[k])
		en := rgwcls.DecodeDirEntry(d)
		if d.Err() != nil {
			return rgwcls.DirHeader{}, rgwcls.DirHeader{}, -int32(syscall.EIO)
		}
		if !en.Exists || (plain && en.Flags != 0) {
			continue
		}
		st := calc.Stats[en.Meta.Category]
		st.NumEntries++
		st.TotalSize += en.Meta.AccountedSize
		st.TotalSizeRounded += rgwRoundedSize(en.Meta.AccountedSize)
		st.ActualSize += en.Meta.Size
		calc.Stats[en.Meta.Category] = st
	}
	return existing, calc, 0
}

// rgwBucketCheckIndex is rgw_bucket_check_index
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3129-L3141 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3461-L3478 at v20.2.4):
// both headers, as rgw_cls_check_index_ret.
func rgwBucketCheckIndex(call *ClassCall) (out []byte, rval int32) {
	existing, calc, rval := rgwCheckIndex(call)
	if rval < 0 {
		return nil, rval
	}
	return encodeSquid(rgwcls.CheckIndexRet{ExistingHeader: existing, CalculatedHeader: calc}), 0
}

// rgwBucketRebuildIndex is rgw_bucket_rebuild_index
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3116-L3126 at v19.2.7):
// the recalculated header written in place of the stored one, its version
// counted as write_bucket_header counts it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L696-L703).
// Tentacle's
// (https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3436-L3458)
// also guards itself against a reshard, which the fake, Squid's class,
// leaves out.
func rgwBucketRebuildIndex(call *ClassCall) int32 {
	_, calc, rval := rgwCheckIndex(call)
	if rval < 0 {
		return rval
	}
	calc.Ver++
	call.Create().OmapHdr = encodeSquid(calc)
	return 0
}
