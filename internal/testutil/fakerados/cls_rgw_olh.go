package fakerados

import (
	"maps"
	"slices"
	"syscall"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The methods RGWClass emulates of those that link and unlink a versioned
// object's instances and keep its olh entry and log, bucket_link_olh to
// bucket_clear_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4699-L4703 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5120 at v20.2.4).
// They are Squid's, whatever the release. Tentacle's writes also read the
// index header first, so one that does not decode fails them, refuse a
// resharding bucket themselves and record each change in the reshard log;
// the fake keeps no reshard log, and here the guard_bucket_resharding radosgw
// sends ahead of each write refuses the reshard. Like the class, they read
// every entry from the stored object, as cls_cxx_map_get_val and
// cls_cxx_map_get_vals read the store, and write through the op's object, so
// a key the call has already written or removed still reads as stored.
var _ = rgwTable(methodTable{
	"bucket_link_olh":        {fn: noOutput(rgwBucketLinkOLH), write: true},
	"bucket_unlink_instance": {fn: noOutput(rgwBucketUnlinkInstance), write: true},
	"bucket_read_olh_log":    {fn: rgwBucketReadOLHLog},
	"bucket_trim_olh_log":    {fn: noOutput(rgwBucketTrimOLHLog), write: true},
	"bucket_clear_olh":       {fn: noOutput(rgwBucketClearOLH), write: true},
})

// rgwReadOmap is read_omap_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L914-L944 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1044-L1074 at v20.2.4):
// the stored value under idx decoded with dec, ENOENT when the object or the
// key is missing, and EIO when the value does not decode.
func rgwReadOmap[T any](call *ClassCall, idx string, dec func(*denc.Decoder) T) (v T, rval int32) {
	if call.Stored == nil {
		return v, -int32(syscall.ENOENT)
	}
	b, ok := call.Stored.Omap[idx]
	if !ok {
		return v, -int32(syscall.ENOENT)
	}
	d := denc.NewDecoder(b)
	v = dec(d)
	if d.Err() != nil {
		var zero T
		return zero, -int32(syscall.EIO)
	}
	return v, 0
}

// rgwSetOmap stores v under idx, creating the object, as cls_cxx_map_set_val
// does.
func rgwSetOmap(call *ClassCall, idx string, v classValue) {
	call.Create().Omap[idx] = encodeSquid(v)
}

// rgwRemoveOmap is cls_cxx_map_remove_key: the OSD's OMAPRMKEYS, ENOENT when
// the op's object does not exist and a no-op for a key it lacks
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7959-L7988 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8036-L8065 at v20.2.4).
func rgwRemoveOmap(call *ClassCall, idx string) int32 {
	obj := call.Object()
	if obj == nil {
		return -int32(syscall.ENOENT)
	}
	delete(obj.Omap, idx)
	return 0
}

// rgwObjIndexKey is encode_obj_index_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L339-L346 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389-L396 at v20.2.4):
// the name itself for an empty instance, so the null version is read
// through the plain key.
func rgwObjIndexKey(key rgwcls.ObjKey) string {
	if key.Instance == "" {
		return key.Name
	}
	return rgwcls.InstanceIndexKey(key, false)
}

// rgwReadKeyEntry is read_key_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 at v20.2.4):
// the entry rgwObjIndexKey names; when that is the plain key and holds a
// version marker, the null version's delete marker entry if specialDM asks
// for it and there is one, and otherwise its data entry. idx is the last key
// read, a failed read's included, which a caller may go on to remove.
func rgwReadKeyEntry(call *ClassCall, key rgwcls.ObjKey, specialDM bool) (idx string, en rgwcls.DirEntry, rval int32) {
	idx = rgwObjIndexKey(key)
	en, rval = rgwReadOmap(call, idx, rgwcls.DecodeDirEntry)
	if rval < 0 {
		return idx, rgwcls.NewDirEntry(), rval
	}
	if key.Instance != "" || en.Flags&rgwcls.FlagVerMarker == 0 {
		return idx, en, 0
	}
	if specialDM {
		idx = rgwcls.InstanceIndexKey(key, true)
		if dm, r := rgwReadOmap(call, idx, rgwcls.DecodeDirEntry); r == 0 {
			return idx, dm, 0
		}
	}
	idx = rgwcls.InstanceIndexKey(key, false)
	en, rval = rgwReadOmap(call, idx, rgwcls.DecodeDirEntry)
	if rval < 0 {
		return idx, rgwcls.NewDirEntry(), rval
	}
	return idx, en, 0
}

// rgwWriteObjEntries is write_obj_entries
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1281-L1315 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1404-L1441 at v20.2.4):
// en under its instance key idx, and under its list key, which the entry's
// own key and versioned epoch name, when that is another key.
func rgwWriteObjEntries(call *ClassCall, en rgwcls.DirEntry, idx string) {
	rgwSetOmap(call, idx, en)
	if list := rgwcls.ListIndexKey(en.Key, en.VersionedEpoch); list != idx {
		rgwSetOmap(call, list, en)
	}
}

// rgwVerObj is BIVerObjEntry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1318-L1478 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1444-L1605 at v20.2.4):
// one version's instance entry as a link or an unlink reads and rewrites it.
// key is the version the call names; entry.Key, which a stored entry brings
// with it, names its list key.
type rgwVerObj struct {
	call        *ClassCall
	key         rgwcls.ObjKey
	idx         string
	entry       rgwcls.DirEntry
	initialized bool
}

func newRGWVerObj(call *ClassCall, key rgwcls.ObjKey) *rgwVerObj {
	return &rgwVerObj{call: call, key: key, entry: rgwcls.NewDirEntry()}
}

// init reads the stored entry; for the null version checkDM prefers its
// delete marker's entry, which the class keeps apart from its data.
func (v *rgwVerObj) init(checkDM bool) int32 {
	idx, en, rval := rgwReadKeyEntry(v.call, v.key, checkDM && v.key.Instance == "")
	v.idx = idx
	if rval < 0 {
		return rval
	}
	v.entry, v.initialized = en, true
	return 0
}

// initAsDeleteMarker is init_as_delete_marker: the entry, read or not,
// becomes a delete marker carrying meta, every other field kept.
func (v *rgwVerObj) initAsDeleteMarker(meta rgwcls.DirEntryMeta) {
	v.entry.Key = v.key
	v.entry.Flags = rgwcls.FlagDeleteMarker
	v.entry.Meta = meta
	v.entry.Tag = "delete-marker"
	v.initialized = true
}

func (v *rgwVerObj) isDeleteMarker() bool { return v.entry.Flags&rgwcls.FlagDeleteMarker != 0 }

func (v *rgwVerObj) unlinkListEntry() int32 {
	return rgwRemoveOmap(v.call, rgwcls.ListIndexKey(v.entry.Key, v.entry.VersionedEpoch))
}

func (v *rgwVerObj) unlink() int32 { return rgwRemoveOmap(v.call, v.idx) }

// writeEntries is write_entries: an entry not yet read is read first, which
// replaces whatever epoch write set on it, and its instance and list
// entries are rewritten with the flags changed, the null version's delete
// marker under its own instance key.
func (v *rgwVerObj) writeEntries(set, reset uint16) int32 {
	if !v.initialized {
		if rval := v.init(true); rval < 0 {
			return rval
		}
	}
	v.entry.Flags &^= reset
	v.entry.Flags |= set
	v.idx = rgwcls.InstanceIndexKey(v.key, v.isDeleteMarker() && v.entry.Key.Instance == "")
	rgwWriteObjEntries(v.call, v.entry, v.idx)
	return 0
}

// write is BIVerObjEntry::write: the list entry of an epoch the entry
// already has goes, and the entry is written as a version at epoch, current
// or not.
func (v *rgwVerObj) write(epoch uint64, current bool) int32 {
	if v.entry.VersionedEpoch > 0 {
		if rval := v.unlinkListEntry(); rval < 0 {
			return rval
		}
	}
	flags := rgwcls.FlagVer
	if current {
		flags |= rgwcls.FlagCurrent
	}
	v.entry.VersionedEpoch = epoch
	return v.writeEntries(flags, 0)
}

func (v *rgwVerObj) demoteCurrent() int32 { return v.writeEntries(0, rgwcls.FlagCurrent) }

// findNextKey is find_next_key: the first stored key after the entry's list
// key that starts with the name, one cls_cxx_map_get_vals of one entry. It
// is the next version only when that entry's own name is the name, and
// list keys sort by decreasing_str, so once two or more versions stand at or
// above epoch 2^32 the next key can be another version than the next one
// down (docs/ceph-upstream-bugs.md).
func (v *rgwVerObj) findNextKey() (next rgwcls.ObjKey, found bool, rval int32) {
	if v.call.Stored == nil {
		return rgwcls.ObjKey{}, false, 0
	}
	vals, _ := omapPage(v.call.Stored.Omap, rgwcls.ListIndexKey(v.entry.Key, v.entry.VersionedEpoch), v.key.Name, 1)
	for _, b := range vals {
		d := denc.NewDecoder(b)
		en := rgwcls.DecodeDirEntry(d)
		if d.Err() != nil {
			return rgwcls.ObjKey{}, false, -int32(syscall.EIO)
		}
		if en.Key.Name == v.key.Name {
			return en.Key, true, 0
		}
	}
	return rgwcls.ObjKey{}, false, 0
}

// rgwOLH is BIOLHEntry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1481-L1566 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1608-L1693 at v20.2.4):
// a name's OLH entry under its OLH data key, the default entry until one is
// read.
type rgwOLH struct {
	call  *ClassCall
	idx   string
	entry rgwcls.OLHEntry
}

func newRGWOLH(call *ClassCall, key rgwcls.ObjKey) *rgwOLH {
	return &rgwOLH{call: call, idx: rgwcls.OLHDataKey(key.Name)}
}

// init is read_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1251-L1266 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1374-L1389 at v20.2.4).
func (o *rgwOLH) init() (found bool, rval int32) {
	en, rval := rgwReadOmap(o.call, o.idx, rgwcls.DecodeOLHEntry)
	switch {
	case rval == -int32(syscall.ENOENT):
		return false, 0
	case rval < 0:
		return false, rval
	}
	o.entry = en
	return true, 0
}

// startModify is start_modify: a named epoch below the entry's is refused,
// and any other is taken; with none the epoch counts up from 2, 1 being
// what a converted plain entry gets.
func (o *rgwOLH) startModify(candidate uint64) bool {
	switch {
	case candidate != 0:
		if candidate < o.entry.Epoch {
			return false
		}
		o.entry.Epoch = candidate
	case o.entry.Epoch == 0:
		o.entry.Epoch = 2
	default:
		o.entry.Epoch++
	}
	return true
}

func (o *rgwOLH) update(key rgwcls.ObjKey, deleteMarker bool) {
	o.entry.Key, o.entry.DeleteMarker = key, deleteMarker
}

// updateLog is update_olh_log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1268-L1279 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1391-L1402 at v20.2.4):
// the change joins the log under the entry's epoch, carrying epoch, or the
// entry's when that is 0.
func (o *rgwOLH) updateLog(op rgwcls.OLHLogOp, opTag string, key rgwcls.ObjKey, deleteMarker bool, epoch uint64) {
	if epoch == 0 {
		epoch = o.entry.Epoch
	}
	if o.entry.PendingLog == nil {
		o.entry.PendingLog = map[uint64][]rgwcls.OLHLogEntry{}
	}
	o.entry.PendingLog[o.entry.Epoch] = append(o.entry.PendingLog[o.entry.Epoch], rgwcls.OLHLogEntry{
		Epoch: epoch, Op: op, OpTag: opTag, Key: key, DeleteMarker: deleteMarker,
	})
}

func (o *rgwOLH) write() { rgwSetOmap(o.call, o.idx, o.entry) }

// rgwConvertPlainEntry is convert_plain_entry_to_versioned with
// write_version_marker
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1568-L1634 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1695-L1763 at v20.2.4):
// the name's plain entry, when it has one, becomes the null version at epoch
// 1, its current flag cleared when demote, under its instance key and, unless
// instanceOnly, its list key; then a version marker takes the plain key.
func rgwConvertPlainEntry(call *ClassCall, name string, demote, instanceOnly bool) int32 {
	key := rgwcls.ObjKey{Name: name}
	_, en, rval := rgwReadKeyEntry(call, key, false)
	switch {
	case rval == -int32(syscall.ENOENT):
	case rval < 0:
		return rval
	default:
		en.VersionedEpoch = 1
		en.Flags |= rgwcls.FlagVer
		if demote {
			en.Flags &^= rgwcls.FlagCurrent
		}
		idx := rgwcls.InstanceIndexKey(key, false)
		if instanceOnly {
			rgwSetOmap(call, idx, en)
		} else {
			rgwWriteObjEntries(call, en, idx)
		}
	}
	marker := rgwcls.NewDirEntry()
	marker.Key, marker.Flags = key, rgwcls.FlagVerMarker
	rgwSetOmap(call, name, marker)
	return 0
}

// rgwLogOLHOp is the tail of the link and the unlink when the request asks
// for log_op: unless sync is stopped the class logs the change to the bucket
// index log, which the fake does not keep, and writes the header with its
// version counted
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1829-L1864 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1991-L2017 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1970-L1999 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2138-L2158 at v20.2.4).
func rgwLogOLHOp(call *ClassCall, logOp bool) int32 {
	if !logOp {
		return 0
	}
	h, rval := rgwDirHeader(call)
	if rval < 0 {
		return rval
	}
	if h.SyncStopped {
		return 0
	}
	h.Ver++
	call.Create().OmapHdr = encodeSquid(h)
	return 0
}

// timespecBefore is a < b as utime_t compares the timespecs the class takes
// of two real_times: by seconds, then nanoseconds, with the epoch for the
// zero time.
func timespecBefore(a, b time.Time, highPrecision bool) bool {
	sec := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.Unix()
	}
	nsec := func(t time.Time) int {
		if t.IsZero() || !highPrecision {
			return 0
		}
		return t.Nanosecond()
	}
	return sec(a) < sec(b) || (sec(a) == sec(b) && nsec(a) < nsec(b))
}

// rgwBucketLinkOLH is Squid's rgw_bucket_link_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1652-L1865 at v19.2.7;
// Tentacle's, the same but for the reshard guard and log, is
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1781-L2000).
// The instance must have an entry unless the link is a delete marker, whose
// entry it makes. An existing instance no older than unmod_since is left
// alone. The null version's data and delete marker each unlink the other.
// A named epoch below the OLH entry's writes the version noncurrent and
// returns, dropping the REMOVE_INSTANCE it may have logged, since the OLH
// entry is not written (docs/ceph-upstream-bugs.md). Otherwise the version
// is promoted at a newer epoch, or an equal one whose current instance sorts
// at or after its own; another OLH tag is ECANCELED unless the entry is
// pending removal; the version it replaces is demoted; a name with no OLH
// entry is converted first; and the OLH entry is written with LINK_OLH and,
// for a marker over a version, REMOVE_INSTANCE logged, then the instance and
// its list entry.
func rgwBucketLinkOLH(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeLinkOLHOp)
	if rval < 0 {
		return rval
	}
	obj := newRGWVerObj(call, op.Key)
	rval = obj.init(op.DeleteMarker)
	existed := rval == 0
	if rval == -int32(syscall.ENOENT) && op.DeleteMarker {
		rval = 0
	}
	if rval < 0 {
		return rval
	}
	if existed && !op.UnmodSince.IsZero() && !timespecBefore(obj.entry.Meta.Mtime, op.UnmodSince, op.HighPrecisionTime) {
		return 0
	}

	var removing bool
	if op.Key.Instance == "" {
		other := newRGWVerObj(call, op.Key)
		rval = other.init(!op.DeleteMarker)
		existed = rval >= 0 && !other.isDeleteMarker()
		if rval >= 0 && other.isDeleteMarker() != op.DeleteMarker {
			if rval = other.unlinkListEntry(); rval < 0 {
				return rval
			}
		}
		removing = existed && op.DeleteMarker
		if !removing {
			if rval = other.unlink(); rval < 0 {
				return rval
			}
		}
	} else {
		removing = existed && !obj.isDeleteMarker() && op.DeleteMarker
	}
	if op.DeleteMarker {
		obj.initAsDeleteMarker(op.Meta)
	}

	olh := newRGWOLH(call, op.Key)
	olhFound, rval := olh.init()
	if rval < 0 {
		return rval
	}
	prevEpoch := olh.entry.Epoch
	if !olh.startModify(op.OLHEpoch) {
		if rval = obj.write(op.OLHEpoch, false); rval < 0 {
			return rval
		}
		if removing {
			olh.updateLog(rgwcls.OLHOpRemoveInstance, op.OpTag, op.Key, false, op.OLHEpoch)
		}
		return 0
	}
	promote := olh.entry.Epoch > prevEpoch || (olh.entry.Epoch == prevEpoch && olh.entry.Key.Instance >= op.Key.Instance)
	if olhFound {
		if op.OLHTag != olh.entry.Tag {
			if !olh.entry.PendingRemoval {
				return -int32(syscall.ECANCELED)
			}
			olh.entry.Tag = op.OLHTag
		}
		if promote && olh.entry.Exists && olh.entry.Key != op.Key {
			if rval = newRGWVerObj(call, olh.entry.Key).demoteCurrent(); rval < 0 {
				return rval
			}
		}
		olh.entry.PendingRemoval = false
	} else {
		if rval = rgwConvertPlainEntry(call, op.Key.Name, promote, op.Key.Instance == "" && op.DeleteMarker); rval < 0 {
			return rval
		}
		olh.entry.Tag = op.OLHTag
		if op.Key.Instance == "" {
			obj.entry.VersionedEpoch = 1
		}
	}

	olh.updateLog(rgwcls.OLHOpLinkOLH, op.OpTag, op.Key, op.DeleteMarker, 0)
	if removing {
		olh.updateLog(rgwcls.OLHOpRemoveInstance, op.OpTag, op.Key, false, 0)
	}
	if promote {
		olh.update(op.Key, op.DeleteMarker)
	}
	olh.entry.Exists = true
	olh.write()
	if rval = obj.write(olh.entry.Epoch, promote); rval < 0 {
		return rval
	}
	return rgwLogOLHOp(call, op.LogOp)
}

// rgwBucketUnlinkInstance is Squid's rgw_bucket_unlink_instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018 at v19.2.7;
// Tentacle's, the same but for the reshard guard and log, is
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159).
// The instance must have an entry. A name with no OLH entry is converted,
// the entry taking the request's tag. The request's tag is never compared.
// A named epoch below the OLH entry's removes the version's list entry and,
// for a version that is not a delete marker, logs its removal; a delete
// marker's instance entry stays (docs/ceph-upstream-bugs.md). Otherwise
// unlinking the current version promotes findNextKey's, at its own epoch,
// or with none logs UNLINK_OLH for the bare name and marks the entry
// pending removal; a delete marker's instance entry is removed here and any
// other version's removal logged; and the list entry goes.
func rgwBucketUnlinkInstance(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeUnlinkInstanceOp)
	if rval < 0 {
		return rval
	}
	obj := newRGWVerObj(call, op.Key)
	if rval = obj.init(true); rval < 0 {
		return rval
	}
	olh := newRGWOLH(call, op.Key)
	olhFound, rval := olh.init()
	if rval < 0 {
		return rval
	}
	if !olhFound {
		if rval = rgwConvertPlainEntry(call, op.Key.Name, true, false); rval < 0 {
			return rval
		}
		olh.update(op.Key, false)
		olh.entry.Tag = op.OLHTag
		obj.entry.VersionedEpoch = 1
	}

	if !olh.startModify(op.OLHEpoch) {
		if rval = obj.unlinkListEntry(); rval < 0 {
			return rval
		}
		if obj.isDeleteMarker() {
			return 0
		}
		olh.updateLog(rgwcls.OLHOpRemoveInstance, op.OpTag, op.Key, false, op.OLHEpoch)
		olh.write()
		return 0
	}

	if olh.entry.Key == op.Key {
		var next rgwcls.ObjKey
		var found bool
		if next, found, rval = obj.findNextKey(); rval < 0 {
			return rval
		}
		if found {
			n := newRGWVerObj(call, next)
			if rval = n.write(olh.entry.Epoch, true); rval < 0 {
				return rval
			}
			olh.update(next, n.isDeleteMarker())
			olh.updateLog(rgwcls.OLHOpLinkOLH, op.OpTag, next, n.isDeleteMarker(), 0)
		} else {
			// The bare name stays as the key, which places the entry in a
			// reshard.
			bare := rgwcls.ObjKey{Name: op.Key.Name}
			olh.update(bare, false)
			olh.updateLog(rgwcls.OLHOpUnlinkOLH, op.OpTag, bare, false, 0)
			olh.entry.Exists = false
			olh.entry.PendingRemoval = true
		}
	}
	if obj.isDeleteMarker() {
		if rval = obj.unlink(); rval < 0 {
			return rval
		}
	} else {
		olh.updateLog(rgwcls.OLHOpRemoveInstance, op.OpTag, op.Key, false, 0)
	}
	if rval = obj.unlinkListEntry(); rval < 0 {
		return rval
	}
	olh.write()
	return rgwLogOLHOp(call, op.LogOp)
}

// rgwOLHMaxLogEntries is MAX_OLH_LOG_ENTRIES, the most epochs one
// bucket_read_olh_log answers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2054 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2195 at v20.2.4).
const rgwOLHMaxLogEntries = 1000

// rgwReadTaggedOLH is the start the OLH log methods share: EINVAL for a key
// with an instance, the stored OLH entry or the default one, and ECANCELED
// when its tag is not the request's, a missing entry's being empty
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2033-L2050 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2174-L2191 at v20.2.4).
func rgwReadTaggedOLH(call *ClassCall, key rgwcls.ObjKey, tag string) (olh *rgwOLH, rval int32) {
	if key.Instance != "" {
		return nil, -int32(syscall.EINVAL)
	}
	olh = newRGWOLH(call, key)
	if _, rval = olh.init(); rval < 0 {
		return nil, rval
	}
	if olh.entry.Tag != tag {
		return nil, -int32(syscall.ECANCELED)
	}
	return olh, 0
}

// rgwBucketReadOLHLog is rgw_bucket_read_olh_log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2020-L2072 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2161-L2213 at v20.2.4):
// the log's epochs above the marker, at most 1000, truncated when more
// remain. The class answers a log whose first epoch is above the marker and
// that holds at most 1000 whole, which is the same answer; for an empty log
// it reads the first epoch of an empty map (docs/ceph-upstream-bugs.md) and
// answers nothing either way.
func rgwBucketReadOLHLog(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, rgwcls.DecodeReadOLHLogOp)
	if rval < 0 {
		return nil, rval
	}
	olh, rval := rgwReadTaggedOLH(call, op.OLH, op.OLHTag)
	if rval < 0 {
		return nil, rval
	}
	var ret rgwcls.ReadOLHLogRet
	for _, epoch := range slices.Sorted(maps.Keys(olh.entry.PendingLog)) {
		if epoch <= op.VerMarker {
			continue
		}
		if len(ret.Log) == rgwOLHMaxLogEntries {
			ret.IsTruncated = true
			break
		}
		if ret.Log == nil {
			ret.Log = map[uint64][]rgwcls.OLHLogEntry{}
		}
		ret.Log[epoch] = olh.entry.PendingLog[epoch]
	}
	return encodeSquid(ret), 0
}

// rgwBucketTrimOLHLog is Squid's rgw_bucket_trim_olh_log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2074-L2124 at v19.2.7;
// Tentacle's, which also guards itself against a reshard, is
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2215-L2277):
// the log's epochs at or below ver go and the entry is written, so a missing
// entry under an empty tag is written as the default entry.
func rgwBucketTrimOLHLog(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeTrimOLHLogOp)
	if rval < 0 {
		return rval
	}
	olh, rval := rgwReadTaggedOLH(call, op.OLH, op.OLHTag)
	if rval < 0 {
		return rval
	}
	for epoch := range olh.entry.PendingLog {
		if epoch <= op.Ver {
			delete(olh.entry.PendingLog, epoch)
		}
	}
	olh.write()
	return 0
}

// rgwBucketClearOLH is Squid's rgw_bucket_clear_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2126-L2190 at v19.2.7;
// Tentacle's, which also guards itself against a reshard, is
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2279-L2355):
// the OLH entry goes, then the plain entry when it is a version marker; any
// other plain entry stays.
func rgwBucketClearOLH(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, rgwcls.DecodeClearOLHOp)
	if rval < 0 {
		return rval
	}
	olh, rval := rgwReadTaggedOLH(call, op.Key, op.OLHTag)
	if rval < 0 {
		return rval
	}
	if rval = rgwRemoveOmap(call, olh.idx); rval < 0 {
		return rval
	}
	plain, rval := rgwReadOmap(call, op.Key.Name, rgwcls.DecodeDirEntry)
	switch {
	case rval == -int32(syscall.ENOENT):
		return 0
	case rval < 0:
		return rval
	case plain.Flags&rgwcls.FlagVerMarker == 0:
		return 0
	}
	return rgwRemoveOmap(call, op.Key.Name)
}
