package fakerados

import (
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// RGWWriteMethods are the methods RGWClass emulates that the rgw class
// registers with CLS_METHOD_WR (cls_rgw.cc:4691-4698, :4705-4706, :4728,
// :4743 and :4752 at v19.2.6, :5107-5115, :5122-5123, :5147, :5162 and :5171
// at v20.2.4), which RegisterClass takes with RGWClass.
var RGWWriteMethods = []string{
	"bucket_init_index", "bucket_prepare_op", "bucket_complete_op", "set_bucket_resharding",
	"mp_upload_part_info_update", "obj_remove", "obj_store_pg_ver", "gc_set_entry",
}

// RGWClass emulates the rgw class (src/cls/rgw/cls_rgw.cc at v19.2.6 and
// v20.2.4) over a bucket index shard's omap: one entry per index key, and
// the rgw_bucket_dir_header in the omap header. It runs bucket_init_index;
// bucket_list asking for no entries, which is how radosgw reads a shard's
// header; the index transaction, bucket_prepare_op and bucket_complete_op;
// guard_bucket_resharding, get_bucket_resharding and
// set_bucket_resharding, with which a spec stages a reshard;
// mp_upload_part_info_update on a multipart meta object; obj_remove and
// obj_store_pg_ver on a head object; and on a gc shard the omap-era
// enqueue, gc_set_entry. Every other method, and a bucket_list asking for
// entries, is EOPNOTSUPP. It reads the header and entries from the stored
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
		case "gc_set_entry":
			return nil, rgwGCSetEntry(call)
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
// :537-744 at v20.2.4) for a request of no entries: the shard's header,
// untruncated.
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
		return nil, -int32(syscall.EOPNOTSUPP)
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
