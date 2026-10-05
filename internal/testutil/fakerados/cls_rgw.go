package fakerados

import (
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// RGWWriteMethods are the methods RGWClass emulates that the rgw class
// registers with CLS_METHOD_WR (cls_rgw.cc:4691-4698, :4705-4706, :4716,
// :4722, :4728, :4731, :4743 and :4752 at v19.2.6, :5107-5115, :5122-5123,
// :5135, :5141, :5147, :5150, :5162 and :5171 at v20.2.4), which
// RegisterClass takes with RGWClass.
var RGWWriteMethods = []string{
	"bucket_init_index", "bucket_prepare_op", "bucket_complete_op", "set_bucket_resharding",
	"mp_upload_part_info_update", "obj_remove", "obj_store_pg_ver", "gc_set_entry", "gc_remove",
	"user_usage_log_add", "dir_suggest_changes",
}

// RGWClass emulates the rgw class (src/cls/rgw/cls_rgw.cc at v19.2.6 and
// v20.2.4) over a bucket index shard's omap: one entry per index key, and
// the rgw_bucket_dir_header in the omap header. It runs bucket_init_index;
// bucket_list asking for no entries, which is how radosgw reads a shard's
// header; the index transaction, bucket_prepare_op and bucket_complete_op;
// guard_bucket_resharding, get_bucket_resharding and
// set_bucket_resharding, with which a spec stages a reshard;
// mp_upload_part_info_update on a multipart meta object; obj_remove,
// obj_store_pg_ver and obj_check_mtime on a head object; on a gc shard the
// omap-era log, gc_set_entry, gc_list and gc_remove; and user_usage_log_add
// on a usage log object. Every other
// method is EOPNOTSUPP. A bucket_list asking for entries and
// dir_suggest_changes are emulated as rgwBucketListEntries and
// rgwDirSuggestChanges say. It reads the header and entries from the stored
// object, as cls_cxx_map_read_header and cls_cxx_map_get_val do, and encodes
// what it stores and replies at the Squid release, whose
// rgw_bucket_dir_header lacks only Tentacle's reshard log count.
//
// It is Squid's class, whatever the release: the guard refuses every reshard
// status but not-resharding, and neither index method guards itself or keeps
// a reshard log, as Tentacle's do. It keeps no bucket index log, and it does
// not emulate versioned keys: a prepare or complete naming an instance is
// EOPNOTSUPP.
func RGWClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case "bucket_init_index":
			return nil, rgwBucketInitIndex(call)
		case "bucket_list":
			return rgwBucketList(call)
		case "bucket_prepare_op":
			return nil, rgwBucketPrepareOp(call)
		case "bucket_complete_op":
			return nil, rgwBucketCompleteOp(call)
		case "guard_bucket_resharding":
			return nil, rgwGuardBucketResharding(call)
		case "get_bucket_resharding":
			return rgwGetBucketResharding(call)
		case "set_bucket_resharding":
			return nil, rgwSetBucketResharding(call)
		case "mp_upload_part_info_update":
			return nil, rgwMPUploadPartInfoUpdate(call)
		case "obj_remove":
			return nil, rgwObjRemove(call)
		case "obj_store_pg_ver":
			return nil, rgwObjStorePGVer(call)
		case "obj_check_mtime":
			return nil, rgwObjCheckMtime(call)
		case "gc_set_entry":
			return nil, rgwGCSetEntry(call)
		case "gc_list":
			return rgwGCList(call)
		case "gc_remove":
			return nil, rgwGCRemove(call)
		case "user_usage_log_add":
			return nil, rgwUserUsageLogAdd(call)
		case "dir_suggest_changes":
			return nil, rgwDirSuggestChanges(call)
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// rgwObjRemove is rgw_obj_remove (cls_rgw.cc:2410-2482 at v19.2.6): the
// object is removed, and when it held xattrs under one of the request's
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

// rgwObjStorePGVer is rgw_obj_store_pg_ver (cls_rgw.cc:2484-2507 at
// v19.2.6): the named xattr takes cls_current_version, the placement group's
// last user version, as 8 little-endian bytes. The fake has no placement
// group and stores the version the object had before the op, 0 for a new
// one.
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

// rgwObjCheckMtime is rgw_obj_check_mtime (cls_rgw.cc:2553-2615 at v19.2.6,
// :2721-2783 at v20.2.4, the same code): the object's mtime, the zero
// real_time when it does not exist, compared with the request's under its
// type, both cut to whole seconds unless the request asks for high
// precision; a comparison that fails is ECANCELED, and an unknown type
// EINVAL. cls_cxx_stat2 sees the object as the op has left it so far.
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
// (cls_rgw.cc:3838-3842 at v19.2.6): the entry under its tag, and again
// under its due time.
const (
	gcNameIndex = "0_"
	gcTimeIndex = "1_"
)

// gcTimeKey is get_time_key (cls_rgw.cc:119-125 at v19.2.6): seconds and
// nanoseconds, zero-padded to 11 and 9 digits.
func gcTimeKey(t time.Time) string {
	return fmt.Sprintf("%011d.%09d", t.Unix(), t.Nanosecond())
}

// rgwGCSetEntry is rgw_cls_gc_set_entry through gc_update_entry
// (cls_rgw.cc:3896-3941 and :3964-3978 at v19.2.6): an entry already under
// the tag loses its time-index key, and the entry, due ExpirationSecs from
// the class's clock, is stored under the tag and under its time.
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
// of 0 (cls_rgw.cc:4117 at v19.2.6, :4519 at v20.2.4).
const gcListDefaultMax = 128

// rgwGCList is rgw_cls_gc_list through gc_iterate_entries
// (cls_rgw.cc:3996-4126 at v19.2.6, :4398-4528 at v20.2.4): the time index
// after the marker, or from its start, in key order, so earliest due first,
// one omap read of max keys (128 for 0); a key outside the time index ends
// the listing, as does, when only the expired are asked for, a key at or
// past the class's clock. The next marker is the last key returned, set
// only while the read was truncated. It reads the stored omap, as
// cls_cxx_map_get_vals does.
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

// rgwGCRemove is rgw_cls_gc_remove through gc_remove (cls_rgw.cc:4128-4173
// at v19.2.6, :4530-4575 at v20.2.4): each tag's name-index entry, and the
// time-index key of the due time it records, go; a tag the shard does not
// hold is skipped. It reads each entry from the stored omap, as
// cls_cxx_map_get_val does.
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

// rgwDirHeader is read_bucket_header (cls_rgw.cc:464-485 at v19.2.6,
// :514-535 at v20.2.4): the stored header, the zero header when the shard
// has none, and EIO when it does not decode.
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

// rgwBucketInitIndex is rgw_bucket_init_index (cls_rgw.cc:741-764 at
// v19.2.6, :837-860 at v20.2.4): a shard that already has a header is
// EINVAL, "index already initialized", and any other gets an empty header
// at version 1, as write_bucket_header counts the write. radosgw precedes
// the call with an exclusive create, so it meets EEXIST from the create
// first.
func rgwBucketInitIndex(call *ClassCall) int32 {
	if call.Stored != nil && len(call.Stored.OmapHdr) != 0 {
		return -int32(syscall.EINVAL)
	}
	call.Create().OmapHdr = encodeSquid(rgwcls.DirHeader{Ver: 1})
	return 0
}

// rgwBucketList is rgw_bucket_list (cls_rgw.cc:487-694 at v19.2.6,
// :537-744 at v20.2.4): for a request of no entries the shard's header,
// untruncated, and otherwise rgwBucketListEntries.
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

// rgwStoredEntry is read_key_entry for a key without an instance
// (cls_rgw.cc:946-978 at v19.2.6): the stored entry under idx, whether it
// is there, and EIO when it does not decode. An entry flagged as a version
// marker is EOPNOTSUPP, since the class would follow it to a versioned key.
func rgwStoredEntry(call *ClassCall, idx string) (en rgwcls.DirEntry, found bool, rval int32) {
	if call.Stored == nil {
		return rgwcls.DirEntry{}, false, 0
	}
	b, ok := call.Stored.Omap[idx]
	if !ok {
		return rgwcls.DirEntry{}, false, 0
	}
	d := denc.NewDecoder(b)
	en = rgwcls.DecodeDirEntry(d)
	if d.Err() != nil {
		return rgwcls.DirEntry{}, false, -int32(syscall.EIO)
	}
	if en.Flags&rgwcls.FlagVerMarker != 0 {
		return rgwcls.DirEntry{}, false, -int32(syscall.EOPNOTSUPP)
	}
	return en, true, 0
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

// rgwUnaccount is unaccount_entry (cls_rgw.cc:887-898 at v19.2.6, :1017-1028
// at v20.2.4): an existing entry's size leaves its category's stats.
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

// rgwBucketPrepareOp is rgw_bucket_prepare_op (cls_rgw.cc:803-885 at
// v19.2.6): a request without a tag is EINVAL; a key with no entry gets an
// unset one carrying the request's locator; the tag joins the entry's
// pending_map in state PENDING_MODIFY with the op and the time. Tentacle's
// (:923-1015) also guards itself, which the guard call radosgw sends ahead
// of it already does here.
func rgwBucketPrepareOp(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodePrepareOp)
	if rval < 0 {
		return rval
	}
	if op.Tag == "" {
		return -int32(syscall.EINVAL)
	}
	if op.Key.Instance != "" {
		return -int32(syscall.EOPNOTSUPP)
	}
	idx := op.Key.Name
	en, found, rval := rgwStoredEntry(call, idx)
	if rval < 0 {
		return rval
	}
	if !found {
		en = rgwcls.NewDirEntry()
		en.Key, en.Locator = op.Key, op.Locator
	}
	en.PendingMap = append(en.PendingMap, rgwcls.PendingEntry{Tag: op.Tag, Info: rgwcls.PendingInfo{
		State: rgwcls.PendingModify, Timestamp: call.Now(), Op: uint8(op.Op),
	}})
	rgwPutEntry(call, idx, en)
	return 0
}

// rgwBucketCompleteOp is rgw_bucket_complete_op (cls_rgw.cc:1006-1241 at
// v19.2.6, :1136-1372 at v20.2.4). A header that cannot be read is EINVAL,
// and a tag that is not pending is EINVAL. A completion whose epoch is not
// above the entry's on the same pool becomes a cancel, but the request's
// version is copied onto the entry first, so a cancel writes it back
// (tracker #80894, reproduced on purpose). A cancel drops the tag and
// removes an entry left neither existing nor pending; a DEL unaccounts the
// entry and removes it, or keeps it as not existing while another tag is
// pending; an ADD replaces the entry's meta and accounts it. Every
// completion then removes and unaccounts the entries remove_objs names,
// skipping one it cannot read, and writes the header with its version
// counted.
func rgwBucketCompleteOp(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeCompleteOp)
	if rval < 0 {
		return rval
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return -int32(syscall.EINVAL)
	}
	if op.Key.Instance != "" {
		return -int32(syscall.EOPNOTSUPP)
	}
	idx := op.Key.Name
	en, ondisk, rval := rgwStoredEntry(call, idx)
	if rval < 0 {
		return rval
	}
	if !ondisk {
		en = rgwcls.NewDirEntry()
		en.Key, en.Ver, en.Meta, en.Locator = op.Key, op.Ver, op.Meta, op.Locator
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
		if k.Instance != "" {
			continue
		}
		old, found, rval := rgwStoredEntry(call, k.Name)
		if rval < 0 || !found {
			continue
		}
		rgwUnaccount(&h, old)
		rgwRemoveEntry(call, k.Name)
	}
	h.Ver++
	call.Create().OmapHdr = encodeSquid(h)
	return 0
}

// rgwGuardBucketResharding is rgw_guard_bucket_resharding (cls_rgw.cc:4577-4602
// at v19.2.6): ret_err, verbatim, when the header's reshard status is
// anything but not-resharding. Tentacle's (:4995-5016, through
// guard_bucket_resharding at :906-921) refuses only an in-progress reshard,
// and a log-record one whose reshard log has reached rgw_reshardlog_threshold.
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

// rgwGetBucketResharding is rgw_get_bucket_resharding (cls_rgw.cc:4604-4631
// at v19.2.6, :5018-5045 at v20.2.4): the header's reshard entry, framed as
// cls_rgw_get_bucket_resharding_ret.
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

// rgwSetBucketResharding is rgw_set_bucket_resharding (cls_rgw.cc:4528-4551
// at v19.2.6, :4946-4969 at v20.2.4): the header takes the request's reshard
// status and is written with its version counted.
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
	obj := c.store(pool, ns).objects[oid]
	if obj == nil {
		return rgwcls.DirEntry{}, false
	}
	b, ok := obj.Omap[key]
	if !ok {
		return rgwcls.DirEntry{}, false
	}
	d := denc.NewDecoder(b)
	en := rgwcls.DecodeDirEntry(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("fakerados: index entry %q of %s/%s/%s does not decode: %v", key, pool, ns, oid, err))
	}
	return en, true
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

// rgwMPUploadPartInfoUpdate is rgw_mp_upload_part_info_update (cls_rgw.cc:4360-4400
// at v19.2.6, :4762-4802 at v20.2.4). A request that does not decode is
// EINVAL. The part info stored under the request's key, read as
// read_omap_entry does (:915-932 at v19.2.6, :1045-1062 at v20.2.4), is EIO
// when it does not decode and a default one when the key or the object is
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

// rgwUserUsageLogAdd is rgw_user_usage_log_add (cls_rgw.cc:3563-3618 at
// v19.2.6, :3965-4020 at v20.2.4). A request or a stored record that does
// not decode is EINVAL. Each entry is filed under its payer, or its owner
// when it has none, merged into the record already stored for its hour,
// that user and its bucket, and stored under both the by-time and the
// by-user key, creating the object. The stored record is read from the
// store, so of two entries of one request with the same key the second
// overwrites the first; radosgw never sends two.
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

// usageKeyByTime is usage_record_name_by_time (cls_rgw.cc:3536-3541 at
// v19.2.6, :3938-3943 at v20.2.4).
func usageKeyByTime(epoch uint64, user, bucket string) string {
	return fmt.Sprintf("%011d_%s_%s", epoch, user, bucket)
}

// usageKeyByUser is usage_record_name_by_user (cls_rgw.cc:3543-3548 at
// v19.2.6, :3945-3950 at v20.2.4).
func usageKeyByUser(user string, epoch uint64, bucket string) string {
	return fmt.Sprintf("%s_%011d_%s", user, epoch, bucket)
}

// aggregateUsage is rgw_usage_log_entry::aggregate (cls_rgw_types.h:1005-1023
// at v19.2.6, :1044-1062 at v20.2.4) of e into en: en takes e's owner,
// payer, bucket and epoch only when its own owner is empty, and every
// category of e is added to en's category and to its total.
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
// (cls_rgw_const.h:11 at v19.2.6 and v20.2.4).
const rgwBIAdvanceAndRetry = -int32(syscall.EFBIG)

// rgwListMaxAttempts is rgw_bucket_list's max_attempts, its bound on the
// omap reads one call makes (cls_rgw.cc:492 at v19.2.6, :542 at v20.2.4).
const rgwListMaxAttempts = 8

// rgwBucketListEntries is the rest of rgw_bucket_list (cls_rgw.cc:525-694 at
// v19.2.6, :575-744 at v20.2.4) for a request of entries. It starts after
// the start key, past the subdirectory a delimiter-terminated start key
// names, and makes up to eight omap reads of the entries still wanted. Each
// entry is decoded, EINVAL when it does not decode; a key
// decode_list_index_key refuses and a version marker are skipped; without
// list_versions an entry that is not visible, or named as the start key, is
// skipped with every version of its name. With a delimiter a name holding it
// after the prefix becomes one common-prefix entry and the listing skips
// past the subdirectory. The reply is truncated when the last read left
// keys and found some, with the last key visited as its marker, and a
// truncated reply of no entries is RGWBIAdvanceAndRetryError. The fake
// stores no key in the 0x80 "ugly namespace" get_obj_vals skips.
func rgwBucketListEntries(call *ClassCall, op rgwcls.ListOp, h rgwcls.DirHeader) (out []byte, rval int32) {
	var keys []string
	if call.Stored != nil {
		keys = slices.Sorted(maps.Keys(call.Stored.Omap))
	}
	ret := rgwcls.ListRet{Dir: rgwcls.Dir{Header: h, Entries: map[string]rgwcls.DirEntry{}}}
	m := ret.Dir.Entries
	start := op.StartObj.Name
	if op.StartObj.Instance != "" {
		// encode_list_index_key (:358-390) finds no versioned instance entry,
		// which the fake never stores, and starts after every version of the
		// name.
		start += "\x01"
	}
	var startEntry rgwcls.ObjKey
	var prevKey, prevPrefix string
	done, more := false, true
	if op.Delimiter != "" && start > op.FilterPrefix && strings.HasSuffix(start, op.Delimiter) {
		start += "\xFF"
	}
	for attempt := 0; attempt < rgwListMaxAttempts && more && !done && uint32(len(m)) < op.NumEntries; attempt++ { //nolint:gosec // an entry count fits a u32
		var page []string
		page, more = rgwObjVals(keys, start, op.FilterPrefix, int(op.NumEntries)-len(m))
		done = len(page) == 0
		for i := 0; i < len(page); i++ {
			k := page[i]
			d := denc.NewDecoder(call.Stored.Omap[k])
			en := rgwcls.DecodeDirEntry(d)
			if d.Err() != nil {
				return nil, -int32(syscall.EINVAL)
			}
			start, startEntry = k, en.Key
			key, ok := rgwDecodeListIndexKey(k)
			if !ok || en.Flags&rgwcls.FlagVerMarker != 0 {
				continue
			}
			if !op.ListVersions && (!rgwVisible(en) || op.StartObj.Name == key.Name) {
				start = key.Name + "\x01"
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

// rgwDecodeListIndexKey is decode_list_index_key (cls_rgw.cc:417-462 at
// v19.2.6, :467-512 at v20.2.4): a key without a NUL is the name; a longer
// one is the name, then NUL-separated values, "i<instance>" and "v<ver>". A
// key with no value after its NUL, or a version that does not parse, is
// refused.
func rgwDecodeListIndexKey(k string) (rgwcls.ObjKey, bool) {
	name, rest, found := strings.Cut(k, "\x00")
	if !found {
		return rgwcls.ObjKey{Name: k}, true
	}
	if rest == "" {
		return rgwcls.ObjKey{}, false
	}
	key := rgwcls.ObjKey{Name: name}
	for v := range strings.SplitSeq(rest, "\x00") {
		switch {
		case strings.HasPrefix(v, "i"):
			key.Instance = v[1:]
		case strings.HasPrefix(v, "v"):
			if _, err := strconv.ParseInt(v[1:], 10, 64); err != nil {
				return rgwcls.ObjKey{}, false
			}
		}
	}
	return key, true
}

// rgwVisible is rgw_bucket_dir_entry::is_visible (cls_rgw_types.h:446-457 at
// v19.2.6): current, which an unversioned entry always is, and not a delete
// marker.
func rgwVisible(en rgwcls.DirEntry) bool {
	current := en.Flags&rgwcls.FlagVer == 0 || en.Flags&(rgwcls.FlagVer|rgwcls.FlagCurrent) == rgwcls.FlagVer|rgwcls.FlagCurrent
	return current && en.Flags&rgwcls.FlagDeleteMarker == 0
}

// rgwDefaultTagTimeout is CEPH_RGW_DEFAULT_TAG_TIMEOUT, and the default of
// rgw_pending_bucket_index_op_expiration, 120 seconds (cls_rgw_types.h at
// v19.2.6 and v20.2.4).
const rgwDefaultTagTimeout = 120 * time.Second

// rgwDirSuggestChanges is Squid's rgw_dir_suggest_changes (cls_rgw.cc:2192-2408
// at v19.2.6). A shard that does not exist is ENOENT, and a header that does
// not decode EIO. Each change is an op byte, its log flag stripped, and an
// entry; one that does not decode is EINVAL. A key the shard does not hold is
// skipped. The stored entry's pending ops older than the header's tag
// timeout, or 120 s, are dropped; a change made against an older index
// version than the entry's is skipped; while a pending op remains nothing
// changes. Otherwise the stored entry is unaccounted when it exists, and a
// removal removes it while an update stores the suggested entry at the
// header's version and accounts it. The header is written, its version
// counted, when its stats changed. The fake keeps no bucket index log, so the
// log flag changes nothing, and Tentacle's own reshard guard is left to the
// guard call radosgw sends ahead of it.
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
		idx := change.Key.Name
		if change.Key.Instance != "" {
			return -int32(syscall.EOPNOTSUPP)
		}
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
