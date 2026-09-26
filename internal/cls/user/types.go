package user

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Bucket is cls_user_bucket, the bucket an entry names. C++ keeps the three
// pools in an explicit_placement struct; they are flattened here.
type Bucket struct {
	Name, Marker, BucketID, PlacementID string
	DataPool, IndexPool, DataExtraPool  string
}

// Encode mirrors cls_user_bucket::encode: ENCODE_START(9, 8) with the
// placement id when there is one, and otherwise the older ENCODE_START(7, 3)
// with the explicit pools, which is what radosgw writes for a bucket created
// through rgw_bucket::convert.
func (b Bucket) Encode(e *denc.Encoder, _ denc.Release) {
	if b.PlacementID != "" {
		f := e.BeginStruct(9, 8)
		e.String(b.Name)
		e.String(b.Marker)
		e.String(b.BucketID)
		e.String(b.PlacementID)
		e.EndStruct(f)
		return
	}
	f := e.BeginStruct(7, 3)
	e.String(b.Name)
	e.String(b.DataPool)
	e.String(b.Marker)
	e.String(b.BucketID)
	e.String(b.IndexPool)
	e.String(b.DataExtraPool)
	e.EndStruct(f)
}

// legacyIDDigits is the room snprintf leaves for a version 3 numeric bucket
// id in its 16-byte buffer.
const legacyIDDigits = 15

// DecodeBucket mirrors cls_user_bucket::decode,
// DECODE_START_LEGACY_COMPAT_LEN(8, 3, 3). Up to version 3 the bucket id is a
// u64, printed as C++ does into a 16-byte buffer, so at most 15 digits
// survive; before version 5 the index pool is the data pool; version 8 only
// carries the pools when its placement id is empty, and version 9 never does.
func DecodeBucket(d *denc.Decoder) Bucket {
	h := d.BeginStructLegacy(8, 3, 3, 0)
	var b Bucket
	b.Name = d.String()
	if h.Version < 8 {
		b.DataPool = d.String()
	}
	if h.Version >= 2 {
		b.Marker = d.String()
		if h.Version <= 3 {
			id := strconv.FormatUint(d.U64(), 10)
			b.BucketID = id[:min(len(id), legacyIDDigits)]
		} else {
			b.BucketID = d.String()
		}
	}
	if h.Version < 8 {
		if h.Version >= 5 {
			b.IndexPool = d.String()
		} else {
			b.IndexPool = b.DataPool
		}
		if h.Version >= 7 {
			b.DataExtraPool = d.String()
		}
	} else {
		b.PlacementID = d.String()
		if h.Version == 8 && b.PlacementID == "" {
			b.DataPool = d.String()
			b.IndexPool = d.String()
			b.DataExtraPool = d.String()
		}
	}
	d.EndStruct(h)
	return b
}

// MarshalJSON writes cls_user_bucket::dump, which leaves out the placement.
func (b Bucket) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name     string `json:"name"`
		Marker   string `json:"marker"`
		BucketID string `json:"bucket_id"`
	}{b.Name, b.Marker, b.BucketID})
}

// BucketEntry is cls_user_bucket_entry, one bucket of a user's list, keyed
// in the omap by the bucket name. The stats are the bucket's as of the last
// sync.
type BucketEntry struct {
	Bucket        Bucket
	Size          uint64
	SizeRounded   uint64
	Count         uint64
	UserStatsSync bool
	CreationTime  time.Time
}

// Encode mirrors cls_user_bucket_entry::encode, ENCODE_START(9, 5): an empty
// string where the name once was, the size, the creation time's whole
// seconds as a u32, the count, the bucket, the rounded size, the sync flag
// and the creation time again at full precision.
func (be BucketEntry) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(9, 5)
	e.String("")
	e.U64(be.Size)
	e.U32(timeT32(be.CreationTime))
	e.U64(be.Count)
	be.Bucket.Encode(e, r)
	e.U64(be.SizeRounded)
	e.Bool(be.UserStatsSync)
	e.Time(be.CreationTime)
	e.EndStruct(f)
}

// timeT32 is real_clock::to_time_t narrowed to __u32; the zero time is the epoch.
func timeT32(t time.Time) uint32 {
	if t.IsZero() {
		return 0
	}
	return uint32(t.Unix()) //nolint:gosec // C++ narrows time_t to __u32
}

// DecodeBucketEntry mirrors cls_user_bucket_entry::decode,
// DECODE_START_LEGACY_COMPAT_LEN(9, 5, 5). Before version 7 the creation
// time is the u32 seconds; before version 4 the rounded size is the size;
// version 8's placement rule is read and dropped.
func DecodeBucketEntry(d *denc.Decoder) BucketEntry {
	h := d.BeginStructLegacy(9, 5, 5, 0)
	var be BucketEntry
	_ = d.String() // the bucket name, before version 3
	s := d.U64()
	mt := d.U32()
	be.Size = s
	if h.Version < 7 && mt != 0 {
		be.CreationTime = time.Unix(int64(mt), 0).UTC()
	}
	if h.Version >= 2 {
		be.Count = d.U64()
	}
	if h.Version >= 3 {
		be.Bucket = DecodeBucket(d)
	}
	if h.Version >= 4 {
		s = d.U64()
	}
	be.SizeRounded = s
	if h.Version >= 6 {
		be.UserStatsSync = d.Bool()
	}
	if h.Version >= 7 {
		be.CreationTime = d.Time()
	}
	if h.Version == 8 {
		_ = d.String() // placement_rule, added in version 8 and removed in 9
	}
	d.EndStruct(h)
	return be
}

// MarshalJSON writes cls_user_bucket_entry::dump.
func (be BucketEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Bucket        Bucket   `json:"bucket"`
		Size          uint64   `json:"size"`
		SizeRounded   uint64   `json:"size_rounded"`
		CreationTime  dumpTime `json:"creation_time"`
		Count         uint64   `json:"count"`
		UserStatsSync bool     `json:"user_stats_sync"`
	}{be.Bucket, be.Size, be.SizeRounded, dumpTime(be.CreationTime), be.Count, be.UserStatsSync})
}

// Stats is cls_user_stats, a user's summed bucket stats.
type Stats struct {
	TotalEntries      uint64
	TotalBytes        uint64
	TotalBytesRounded uint64
}

// Encode mirrors cls_user_stats::encode, ENCODE_START(1, 1).
func (s Stats) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(s.TotalEntries)
	e.U64(s.TotalBytes)
	e.U64(s.TotalBytesRounded)
	e.EndStruct(f)
}

// DecodeStats mirrors cls_user_stats::decode, DECODE_START(1).
func DecodeStats(d *denc.Decoder) Stats {
	h := d.BeginStruct(1)
	s := Stats{TotalEntries: d.U64(), TotalBytes: d.U64(), TotalBytesRounded: d.U64()}
	d.EndStruct(h)
	return s
}

// MarshalJSON writes cls_user_stats::dump, whose dump_int prints the u64
// counters as signed.
func (s Stats) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		TotalEntries      int64 `json:"total_entries"`
		TotalBytes        int64 `json:"total_bytes"`
		TotalBytesRounded int64 `json:"total_bytes_rounded"`
	}{int64(s.TotalEntries), int64(s.TotalBytes), int64(s.TotalBytesRounded)}) //nolint:gosec // dump_int's signed rendering
}

// Header is cls_user_header, the omap header of <user>.buckets.
type Header struct {
	Stats Stats
	// LastStatsSync is when a full stats sync last completed.
	LastStatsSync time.Time
	// LastStatsUpdate is when the stats were last updated.
	LastStatsUpdate time.Time
}

// Encode mirrors cls_user_header::encode, ENCODE_START(1, 1).
func (h Header) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	h.Stats.Encode(e, r)
	e.Time(h.LastStatsSync)
	e.Time(h.LastStatsUpdate)
	e.EndStruct(f)
}

// DecodeHeader mirrors cls_user_header::decode, DECODE_START(1).
func DecodeHeader(d *denc.Decoder) Header {
	sh := d.BeginStruct(1)
	h := Header{Stats: DecodeStats(d), LastStatsSync: d.Time(), LastStatsUpdate: d.Time()}
	d.EndStruct(sh)
	return h
}

// MarshalJSON writes cls_user_header::dump.
func (h Header) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Stats           Stats    `json:"stats"`
		LastStatsSync   dumpTime `json:"last_stats_sync"`
		LastStatsUpdate dumpTime `json:"last_stats_update"`
	}{h.Stats, dumpTime(h.LastStatsSync), dumpTime(h.LastStatsUpdate)})
}

// dumpTime is a time as encode_json writes a utime_t, through utime_t::gmtime.
type dumpTime time.Time

// relativeBelow is the second count under which utime_t::gmtime prints a
// time as raw seconds rather than a date, taking it for a duration.
const relativeBelow = 60 * 60 * 24 * 365 * 10

// MarshalJSON writes "<sec>.<usec>" within ten years of the epoch, the zero
// time included, and an ISO 8601 UTC date with microseconds otherwise.
func (t dumpTime) MarshalJSON() ([]byte, error) {
	var sec, nsec uint32
	if tt := time.Time(t); !tt.IsZero() {
		sec = uint32(tt.Unix())        //nolint:gosec // utime_t truncates seconds to u32
		nsec = uint32(tt.Nanosecond()) //nolint:gosec // Nanosecond is within [0, 1e9)
	}
	if sec < relativeBelow {
		return json.Marshal(fmt.Sprintf("%d.%06d", sec, nsec/1000))
	}
	return json.Marshal(time.Unix(int64(sec), int64(nsec)).UTC().Format("2006-01-02T15:04:05.000000Z"))
}

// SetBucketsOp is cls_user_set_buckets_op, the "set_buckets_info" request.
type SetBucketsOp struct {
	Entries []BucketEntry
	Add     bool
	Time    time.Time
}

// Encode mirrors cls_user_set_buckets_op::encode, ENCODE_START(1, 1).
func (o SetBucketsOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.Entries, func(e *denc.Encoder, be BucketEntry) { be.Encode(e, r) })
	e.Bool(o.Add)
	e.Time(o.Time)
	e.EndStruct(f)
}

// DecodeSetBucketsOp mirrors cls_user_set_buckets_op::decode, DECODE_START(1).
func DecodeSetBucketsOp(d *denc.Decoder) SetBucketsOp {
	h := d.BeginStruct(1)
	o := SetBucketsOp{Entries: denc.DecodeSlice(d, DecodeBucketEntry), Add: d.Bool(), Time: d.Time()}
	d.EndStruct(h)
	return o
}

// RemoveBucketOp is cls_user_remove_bucket_op, the "remove_bucket" request.
type RemoveBucketOp struct {
	Bucket Bucket
}

// Encode mirrors cls_user_remove_bucket_op::encode, ENCODE_START(1, 1).
func (o RemoveBucketOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Bucket.Encode(e, r)
	e.EndStruct(f)
}

// DecodeRemoveBucketOp mirrors cls_user_remove_bucket_op::decode, DECODE_START(1).
func DecodeRemoveBucketOp(d *denc.Decoder) RemoveBucketOp {
	h := d.BeginStruct(1)
	o := RemoveBucketOp{Bucket: DecodeBucket(d)}
	d.EndStruct(h)
	return o
}

// ListBucketsOp is cls_user_list_buckets_op, the "list_buckets" request.
// EndMarker arrived in version 2.
type ListBucketsOp struct {
	Marker     string
	MaxEntries int32
	EndMarker  string
}

// Encode mirrors cls_user_list_buckets_op::encode, ENCODE_START(2, 1).
func (o ListBucketsOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Marker)
	e.I32(o.MaxEntries)
	e.String(o.EndMarker)
	e.EndStruct(f)
}

// DecodeListBucketsOp mirrors cls_user_list_buckets_op::decode, DECODE_START(2).
func DecodeListBucketsOp(d *denc.Decoder) ListBucketsOp {
	h := d.BeginStruct(2)
	o := ListBucketsOp{Marker: d.String(), MaxEntries: d.I32()}
	if h.Version >= 2 {
		o.EndMarker = d.String()
	}
	d.EndStruct(h)
	return o
}

// ListBucketsRet is cls_user_list_buckets_ret, the "list_buckets" reply.
type ListBucketsRet struct {
	Entries   []BucketEntry
	Marker    string
	Truncated bool
}

// Encode mirrors cls_user_list_buckets_ret::encode, ENCODE_START(1, 1).
func (o ListBucketsRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.Entries, func(e *denc.Encoder, be BucketEntry) { be.Encode(e, r) })
	e.String(o.Marker)
	e.Bool(o.Truncated)
	e.EndStruct(f)
}

// DecodeListBucketsRet mirrors cls_user_list_buckets_ret::decode, DECODE_START(1).
func DecodeListBucketsRet(d *denc.Decoder) ListBucketsRet {
	h := d.BeginStruct(1)
	o := ListBucketsRet{Entries: denc.DecodeSlice(d, DecodeBucketEntry), Marker: d.String(), Truncated: d.Bool()}
	d.EndStruct(h)
	return o
}

// GetHeaderOp is cls_user_get_header_op, the empty "get_header" request.
type GetHeaderOp struct{}

// Encode mirrors cls_user_get_header_op::encode, ENCODE_START(1, 1).
func (GetHeaderOp) Encode(e *denc.Encoder, _ denc.Release) {
	e.EndStruct(e.BeginStruct(1, 1))
}

// DecodeGetHeaderOp mirrors cls_user_get_header_op::decode, DECODE_START(1).
func DecodeGetHeaderOp(d *denc.Decoder) GetHeaderOp {
	d.EndStruct(d.BeginStruct(1))
	return GetHeaderOp{}
}

// GetHeaderRet is cls_user_get_header_ret, the "get_header" reply.
type GetHeaderRet struct {
	Header Header
}

// Encode mirrors cls_user_get_header_ret::encode, ENCODE_START(1, 1).
func (o GetHeaderRet) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	o.Header.Encode(e, r)
	e.EndStruct(f)
}

// DecodeGetHeaderRet mirrors cls_user_get_header_ret::decode, DECODE_START(1).
func DecodeGetHeaderRet(d *denc.Decoder) GetHeaderRet {
	h := d.BeginStruct(1)
	o := GetHeaderRet{Header: DecodeHeader(d)}
	d.EndStruct(h)
	return o
}

// CompleteStatsSyncOp is cls_user_complete_stats_sync_op, the
// "complete_stats_sync" request.
type CompleteStatsSyncOp struct {
	Time time.Time
}

// Encode mirrors cls_user_complete_stats_sync_op::encode, ENCODE_START(1, 1).
func (o CompleteStatsSyncOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Time(o.Time)
	e.EndStruct(f)
}

// DecodeCompleteStatsSyncOp mirrors cls_user_complete_stats_sync_op::decode, DECODE_START(1).
func DecodeCompleteStatsSyncOp(d *denc.Decoder) CompleteStatsSyncOp {
	h := d.BeginStruct(1)
	o := CompleteStatsSyncOp{Time: d.Time()}
	d.EndStruct(h)
	return o
}

// ResetStats2Op is cls_user_reset_stats2_op, the "reset_user_stats2"
// request: one page of the recount, resumed after Marker with the stats
// accumulated so far.
type ResetStats2Op struct {
	Time     time.Time
	Marker   string
	AccStats Stats
}

// Encode mirrors cls_user_reset_stats2_op::encode, ENCODE_START(1, 1).
func (o ResetStats2Op) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Time(o.Time)
	e.String(o.Marker)
	o.AccStats.Encode(e, r)
	e.EndStruct(f)
}

// DecodeResetStats2Op mirrors cls_user_reset_stats2_op::decode, DECODE_START(1).
func DecodeResetStats2Op(d *denc.Decoder) ResetStats2Op {
	h := d.BeginStruct(1)
	o := ResetStats2Op{Time: d.Time(), Marker: d.String(), AccStats: DecodeStats(d)}
	d.EndStruct(h)
	return o
}

// ResetStats2Ret is cls_user_reset_stats2_ret, the "reset_user_stats2"
// reply. While Truncated, pass Marker and AccStats to the next call; the
// last page writes AccStats into the header.
type ResetStats2Ret struct {
	Marker    string
	AccStats  Stats
	Truncated bool
}

// Encode mirrors cls_user_reset_stats2_ret::encode, ENCODE_START(1, 1).
func (o ResetStats2Ret) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Marker)
	o.AccStats.Encode(e, r)
	e.Bool(o.Truncated)
	e.EndStruct(f)
}

// DecodeResetStats2Ret mirrors cls_user_reset_stats2_ret::decode, DECODE_START(1).
func DecodeResetStats2Ret(d *denc.Decoder) ResetStats2Ret {
	h := d.BeginStruct(1)
	o := ResetStats2Ret{Marker: d.String(), AccStats: DecodeStats(d), Truncated: d.Bool()}
	d.EndStruct(h)
	return o
}
