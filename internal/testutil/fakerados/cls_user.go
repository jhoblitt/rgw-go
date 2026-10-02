package fakerados

import (
	"maps"
	"slices"
	"syscall"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// UserWriteMethods are the methods UserClass emulates that the user class
// registers with CLS_METHOD_WR (cls_user.cc:739-747 at v19.2.6 and
// v20.2.4), which RegisterClass takes with UserClass.
var UserWriteMethods = []string{"set_buckets_info", "complete_stats_sync", "remove_bucket", "reset_user_stats2"}

// userMaxEntries is the class's MAX_ENTRIES, the most entries list_buckets
// and reset_user_stats2 read in one call.
const userMaxEntries = 1000

// UserClass emulates cls_user (src/cls/user/cls_user.cc at v19.2.6 and
// v20.2.4, which do not differ) over the object's omap: one encoded
// cls_user_bucket_entry per bucket name, and the cls_user_header in the omap
// header. It runs set_buckets_info, remove_bucket, list_buckets,
// get_header, complete_stats_sync and reset_user_stats2, reading the entries
// and the header from the stored object, as the class's
// cls_cxx_map_get_val and cls_cxx_map_read_header do. A request that does
// not decode is EINVAL, and a stored entry or header that does not decode
// is EIO, except that list_buckets drops an entry it cannot decode.
func UserClass() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		switch call.Method {
		case "set_buckets_info":
			return nil, userSetBucketsInfo(call)
		case "remove_bucket":
			return nil, userRemoveBucket(call)
		case "list_buckets":
			return userListBuckets(call)
		case "get_header":
			if _, rval := decodeRequest(call.In, user.DecodeGetHeaderOp); rval < 0 {
				return nil, rval
			}
			h, rval := userHeader(call)
			if rval < 0 {
				return nil, rval
			}
			return encodeSquid(user.GetHeaderRet{Header: h}), 0
		case "complete_stats_sync":
			op, rval := decodeRequest(call.In, user.DecodeCompleteStatsSyncOp)
			if rval < 0 {
				return nil, rval
			}
			h, rval := userHeader(call)
			if rval < 0 {
				return nil, rval
			}
			if h.LastStatsSync.Before(op.Time) {
				h.LastStatsSync = op.Time
			}
			call.Create().OmapHdr = encodeSquid(h)
			return nil, 0
		case "reset_user_stats2":
			return userResetStats2(call)
		}
		return nil, -int32(syscall.EOPNOTSUPP)
	}
}

// classValue is a type a class emulator stores or replies with.
type classValue interface {
	Encode(e *denc.Encoder, r denc.Release)
}

// encodeSquid encodes v at the Squid release, as an emulator that cannot
// know the cluster's release encodes what it stores and replies; no type the
// user class encodes differs between releases.
func encodeSquid(v classValue) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

// userHeader is read_header: the stored header, the zero header when the
// object has none.
func userHeader(call *ClassCall) (h user.Header, rval int32) {
	if call.Stored == nil || len(call.Stored.OmapHdr) == 0 {
		return user.Header{}, 0
	}
	d := denc.NewDecoder(call.Stored.OmapHdr)
	h = user.DecodeHeader(d)
	if d.Err() != nil {
		return user.Header{}, -int32(syscall.EIO)
	}
	return h, 0
}

// userEntry is get_existing_bucket_entry: EINVAL for an empty name, ENOENT
// for a name the object does not hold, and EIO for an entry that does not
// decode.
func userEntry(call *ClassCall, name string) (e user.BucketEntry, rval int32) {
	if name == "" {
		return user.BucketEntry{}, -int32(syscall.EINVAL)
	}
	var b []byte
	ok := false
	if call.Stored != nil {
		b, ok = call.Stored.Omap[name]
	}
	if !ok {
		return user.BucketEntry{}, -int32(syscall.ENOENT)
	}
	d := denc.NewDecoder(b)
	e = user.DecodeBucketEntry(d)
	if d.Err() != nil {
		return user.BucketEntry{}, -int32(syscall.EIO)
	}
	return e, 0
}

func addStats(s *user.Stats, e user.BucketEntry) {
	s.TotalEntries += e.Count
	s.TotalBytes += e.Size
	s.TotalBytesRounded += e.SizeRounded
}

// decStats is dec_header_stats, which wraps below zero as the class's
// unsigned counters do.
func decStats(s *user.Stats, e user.BucketEntry) {
	s.TotalBytes -= e.Size
	s.TotalBytesRounded -= e.SizeRounded
	s.TotalEntries -= e.Count
}

// userSetBucketsInfo is cls_user_set_buckets_info (cls_user.cc:122-204).
// With add, a missing entry is created from the request and an existing one
// keeps its stats and marker but takes the request's bucket id and creation
// time; without add, a missing entry is skipped as a racing removal and an
// existing one takes the request's stats. Each entry written is marked
// synced, the header's stats move by what the entry adds over what it held,
// and the header, written whatever the entries, takes the op time as its
// last update when that is later.
func userSetBucketsInfo(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, user.DecodeSetBucketsOp)
	if rval < 0 {
		return rval
	}
	h, rval := userHeader(call)
	if rval < 0 {
		return rval
	}
	for i := range op.Entries {
		upd := &op.Entries[i]
		entry, rval := userEntry(call, upd.Bucket.Name)
		switch {
		case rval == -int32(syscall.ENOENT) && !op.Add:
			continue
		case rval == -int32(syscall.ENOENT):
			entry = *upd
		case rval < 0:
			return rval
		case op.Add:
			entry.Bucket.BucketID = upd.Bucket.BucketID
			entry.CreationTime = upd.CreationTime
		}
		if entry.UserStatsSync {
			decStats(&h.Stats, entry)
		}
		if !op.Add {
			entry.Size, entry.SizeRounded, entry.Count = upd.Size, upd.SizeRounded, upd.Count
		}
		entry.UserStatsSync = true
		call.Create().Omap[upd.Bucket.Name] = encodeSquid(entry)
		addStats(&h.Stats, entry)
	}
	if h.LastStatsUpdate.Before(op.Time) {
		h.LastStatsUpdate = op.Time
	}
	call.Create().OmapHdr = encodeSquid(h)
	return 0
}

// userRemoveBucket is cls_user_remove_bucket (cls_user.cc:239-289): a
// missing entry is no failure and writes nothing, and the header loses the
// entry's stats only when they were synced.
func userRemoveBucket(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, user.DecodeRemoveBucketOp)
	if rval < 0 {
		return rval
	}
	h, rval := userHeader(call)
	if rval < 0 {
		return rval
	}
	entry, rval := userEntry(call, op.Bucket.Name)
	switch {
	case rval == -int32(syscall.ENOENT):
		return 0
	case rval < 0:
		return rval
	}
	obj := call.Create()
	delete(obj.Omap, op.Bucket.Name)
	if !entry.UserStatsSync {
		return 0
	}
	decStats(&h.Stats, entry)
	obj.OmapHdr = encodeSquid(h)
	return 0
}

// userPageMax is the class's size_t conversion of a request's int32 count,
// capped at MAX_ENTRIES: a negative count converts to a huge one.
func userPageMax(n int32) uint64 {
	if n < 0 || n > userMaxEntries {
		return userMaxEntries
	}
	return uint64(n)
}

// userListBuckets is cls_user_list_buckets (cls_user.cc:291-358): the
// entries after the marker, at most min(max, 1000), stopping before the end
// marker when there is one, which also clears the truncation. An entry that
// does not decode is dropped. The reply's marker is the last name read, and
// only when the reply is truncated.
func userListBuckets(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, user.DecodeListBucketsOp)
	if rval < 0 {
		return nil, rval
	}
	var omap map[string][]byte
	if call.Stored != nil {
		omap = call.Stored.Omap
	}
	page, truncated := omapPage(omap, op.Marker, "", userPageMax(op.MaxEntries))
	ret := user.ListBucketsRet{Truncated: truncated}
	var marker string
	for _, k := range slices.Sorted(maps.Keys(page)) {
		marker = k
		if op.EndMarker != "" && op.EndMarker <= k {
			ret.Truncated = false
			break
		}
		d := denc.NewDecoder(page[k])
		if e := user.DecodeBucketEntry(d); d.Err() == nil {
			ret.Entries = append(ret.Entries, e)
		}
	}
	if ret.Truncated {
		ret.Marker = marker
	}
	return encodeSquid(ret), 0
}

// userResetStats2 is cls_user_reset_stats2 (cls_user.cc:444-506): it sums
// up to 1000 entries after the marker and, on the last page, writes a fresh
// header holding that sum and the op time. Each call sums from zero: the
// class ignores the stats the request carries, so the header holds the last
// page's sum alone (docs/ceph-upstream-bugs.md).
func userResetStats2(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, user.DecodeResetStats2Op)
	if rval < 0 {
		return nil, rval
	}
	var omap map[string][]byte
	if call.Stored != nil {
		omap = call.Stored.Omap
	}
	page, truncated := omapPage(omap, op.Marker, "", userMaxEntries)
	ret := user.ResetStats2Ret{Truncated: truncated}
	keys := slices.Sorted(maps.Keys(page))
	for _, k := range keys {
		d := denc.NewDecoder(page[k])
		e := user.DecodeBucketEntry(d)
		if d.Err() != nil {
			return nil, -int32(syscall.EIO)
		}
		addStats(&ret.AccStats, e)
	}
	if !ret.Truncated {
		call.Create().OmapHdr = encodeSquid(user.Header{Stats: ret.AccStats, LastStatsUpdate: op.Time})
		return encodeSquid(ret), 0
	}
	if len(keys) > 0 {
		ret.Marker = keys[len(keys)-1]
	}
	return encodeSquid(ret), 0
}
