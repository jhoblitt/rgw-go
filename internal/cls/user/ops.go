package user

import (
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class and the method names its CLS_INIT registers.
const (
	class                   = "user"
	methodSetBucketsInfo    = "set_buckets_info"
	methodCompleteStatsSync = "complete_stats_sync"
	methodRemoveBucket      = "remove_bucket"
	methodListBuckets       = "list_buckets"
	methodGetHeader         = "get_header"
	methodResetUserStats2   = "reset_user_stats2"
)

func encode(v interface {
	Encode(*denc.Encoder, denc.Release)
}, r denc.Release,
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

// decodeReply decodes res's output with dec once the op has run.
func decodeReply[T any](res *radosclient.ExecResult, method string, dec func(*denc.Decoder) T) (T, error) {
	var zero T
	b, err := res.Bytes()
	if err != nil {
		return zero, err
	}
	d := denc.NewDecoder(b)
	v := dec(d)
	if err := d.Err(); err != nil {
		return zero, fmt.Errorf("user: decoding %s reply: %w", method, err)
	}
	return v, nil
}

// SetBucketsInfo mirrors cls_user_set_buckets with t as the op time, which
// C++ takes from the clock. With add, a missing entry is created and an
// existing one keeps its stats but takes the bucket id and creation time;
// without add, a missing entry is skipped and an existing one takes the
// stats. Either way the entry is marked synced and the header stats adjusted.
// An entry with an empty bucket name fails the op with ErrInvalid.
func SetBucketsInfo(op radosclient.Execer, entries []BucketEntry, add bool, t time.Time, r denc.Release) {
	op.Exec(class, methodSetBucketsInfo, encode(SetBucketsOp{Entries: entries, Add: add, Time: t}, r))
}

// RemoveBucket mirrors cls_user_remove_bucket: it removes the entry named
// b.Name, subtracting its stats from the header when they were synced. A
// missing entry is not an error; an empty b.Name fails the op with ErrInvalid.
func RemoveBucket(op radosclient.Execer, b Bucket, r denc.Release) {
	op.Exec(class, methodRemoveBucket, encode(RemoveBucketOp{Bucket: b}, r))
}

// ListResult is a pending "list_buckets" reply.
type ListResult struct {
	res *radosclient.ExecResult
}

// ListBuckets mirrors cls_user_bucket_list: up to max entries, at most 1000,
// after marker and before endMarker ("" for no end).
func ListBuckets(op *radosclient.ReadOp, marker, endMarker string, maxEntries int32, r denc.Release) *ListResult {
	in := encode(ListBucketsOp{Marker: marker, MaxEntries: maxEntries, EndMarker: endMarker}, r)
	return &ListResult{res: op.Exec(class, methodListBuckets, in)}
}

// Entries decodes the listed entries, the marker to resume after, and
// whether more remain. The marker is empty unless truncated. The class drops
// entries it cannot decode rather than failing.
func (res *ListResult) Entries() (entries []BucketEntry, marker string, truncated bool, err error) {
	ret, err := decodeReply(res.res, methodListBuckets, DecodeListBucketsRet)
	if err != nil {
		return nil, "", false, err
	}
	return ret.Entries, ret.Marker, ret.Truncated, nil
}

// HeaderResult is a pending "get_header" reply.
type HeaderResult struct {
	res *radosclient.ExecResult
}

// GetHeader mirrors cls_user_get_header.
func GetHeader(op *radosclient.ReadOp, r denc.Release) *HeaderResult {
	return &HeaderResult{res: op.Exec(class, methodGetHeader, encode(GetHeaderOp{}, r))}
}

// Header decodes the header; an object without one reads as the zero Header.
func (res *HeaderResult) Header() (Header, error) {
	ret, err := decodeReply(res.res, methodGetHeader, DecodeGetHeaderRet)
	return ret.Header, err
}

// CompleteStatsSync mirrors cls_user_complete_stats_sync with t as the op
// time, which C++ takes from the clock: it advances the header's
// LastStatsSync to t.
func CompleteStatsSync(op radosclient.Execer, t time.Time, r denc.Release) {
	op.Exec(class, methodCompleteStatsSync, encode(CompleteStatsSyncOp{Time: t}, r))
}

// ResetResult is a pending "reset_user_stats2" reply.
type ResetResult struct {
	res *radosclient.ExecResult
}

// ResetStats2 mirrors one round of rgwrados::buckets::reset_stats: it sums
// up to 1000 entries after marker onto acc and, on the last page, writes a
// fresh header holding the sum and t: its LastStatsSync is zero until the next
// CompleteStatsSync. An entry that does not decode fails the op with EIO.
// The method writes, and its reply only comes back when the op runs with
// OpFlagReturnVec, so it takes a ReadOp; loop, passing the returned marker
// and stats on, while the result is truncated.
func ResetStats2(op *radosclient.ReadOp, t time.Time, marker string, acc Stats, r denc.Release) *ResetResult {
	in := encode(ResetStats2Op{Time: t, Marker: marker, AccStats: acc}, r)
	return &ResetResult{res: op.Exec(class, methodResetUserStats2, in)}
}

// Result decodes the page's marker, accumulated stats and truncation.
func (res *ResetResult) Result() (ResetStats2Ret, error) {
	return decodeReply(res.res, methodResetUserStats2, DecodeResetStats2Ret)
}
