package driver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// usageTrimWindow is MAX_USAGE_TRIM_ENTRIES, the keys one
// user_usage_log_trim walks
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3794 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4196 at v20.2.4);
// countUsage reads in windows of the same size.
const usageTrimWindow = 1000

// ReadUsage implements op.UsageReader. It is RGWRados::read_usage
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1674-L1716 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1777-L1819 at v20.2.4):
// from the shard it.Index names, one user_usage_log_read of the records
// still wanted at a time, continuing from it.ReadIter. A shard that is not
// truncated, or has no usage object, moves it to the next shard with a
// fresh iterator; the walk stops once maxRecs records are read, a read is
// truncated, or the next shard is the first again, user's shards being
// usage_log_hash's. Each read's records are merged per (user, bucket) with
// rgw_usage_log_entry::aggregate and returned in that order. A page that a
// shard fills exactly without truncation ends the listing untruncated,
// even with shards left, as radosgw's loop does
// (docs/ceph-upstream-bugs.md).
func (s *Store) ReadUsage(ctx context.Context, user, bucket string, start, end uint64, maxRecs uint32, it *op.UsageIter) (recs []op.UsageRecord, truncated bool, err error) {
	pool, err := s.pools.get(ctx, s.zone.Params.UsageLogPool)
	if err != nil {
		return nil, false, err
	}
	first := s.usageOID(user, 0)
	oid := first
	if it.Index != 0 {
		oid = s.usageOID(user, it.Index)
	}
	page := map[rgwcls.UserBucket]rgwcls.UsageLogEntry{}
	num := maxRecs
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		ret, err := s.readUsageShard(ctx, pool, oid, rgwcls.UsageReadOp{
			StartEpoch: start, EndEpoch: end, Owner: user, Bucket: bucket, Iter: it.ReadIter, MaxEntries: num,
		})
		truncated = false
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
		case err != nil:
			return nil, false, err
		default:
			truncated = ret.Truncated
			// radosgw's num is a uint32_t, so a call asking for 0, which the
			// class reads as 1000, wraps it.
			num -= uint32(len(ret.Usage)) //nolint:gosec // a page holds at most the 1024 keys the OSD reads
			for k, e := range ret.Usage {
				page[k] = aggregateUsageEntry(page[k], e)
			}
			it.ReadIter = ret.NextIter
		}
		if !truncated {
			it.ReadIter = ""
			it.Index++
			oid = s.usageOID(user, it.Index)
		}
		if num == 0 || truncated || oid == first {
			break
		}
	}
	keys := slices.SortedFunc(maps.Keys(page), func(a, b rgwcls.UserBucket) int {
		return cmp.Or(strings.Compare(a.User, b.User), strings.Compare(a.Bucket, b.Bucket))
	})
	recs = make([]op.UsageRecord, 0, len(keys))
	for _, k := range keys {
		e := page[k]
		rec := op.UsageRecord{
			User: k.User, Owner: e.Owner, Payer: e.Payer, Bucket: k.Bucket, Epoch: e.Epoch,
			Total:    op.UsageData(e.TotalUsage),
			S3Select: op.S3SelectUsage(e.S3SelectUsage),
		}
		if len(e.UsageMap) > 0 {
			rec.Categories = make(map[string]op.UsageData, len(e.UsageMap))
			for cat, d := range e.UsageMap {
				rec.Categories[cat] = op.UsageData(d)
			}
		}
		recs = append(recs, rec)
	}
	return recs, truncated, nil
}

// readUsageShard is cls_rgw_usage_log_read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L793-L828 at v19.2.6)
// on one usage object.
func (s *Store) readUsageShard(ctx context.Context, pool radosclient.Pool, oid string, q rgwcls.UsageReadOp) (rgwcls.UsageReadRet, error) {
	r := radosclient.NewReadOp()
	res := rgwcls.UsageLogRead(r, q, s.release)
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return rgwcls.UsageReadRet{}, fmt.Errorf("reading usage log object %s: %w", oid, err)
	}
	ret, err := res.Result()
	if err != nil {
		return rgwcls.UsageReadRet{}, fmt.Errorf("reading usage log object %s: %w", oid, err)
	}
	return ret, nil
}

// aggregateUsageEntry is rgw_usage_log_entry::aggregate without a category
// filter
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L1005-L1023 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1044-L1062 at v20.2.4):
// e's owner, payer, bucket and epoch when dst has no owner yet, then each
// of e's categories added to dst's and to its total, which so becomes the
// categories' sum whatever total e carried, and the s3select counters.
func aggregateUsageEntry(dst, e rgwcls.UsageLogEntry) rgwcls.UsageLogEntry {
	if meta.ParseUserID(dst.Owner).ID == "" {
		dst.Owner, dst.Payer, dst.Bucket, dst.Epoch = e.Owner, e.Payer, e.Bucket, e.Epoch
	}
	sums := maps.Clone(dst.UsageMap)
	if sums == nil {
		sums = map[string]rgwcls.UsageData{}
	}
	for cat, d := range e.UsageMap {
		sums[cat] = addUsage(sums[cat], d)
		dst.TotalUsage = addUsage(dst.TotalUsage, d)
	}
	dst.UsageMap = sums
	dst.S3SelectUsage.BytesProcessed += e.S3SelectUsage.BytesProcessed
	dst.S3SelectUsage.BytesReturned += e.S3SelectUsage.BytesReturned
	return dst
}

// TrimUsage implements op.UsageReader. It is RGWRados::trim_usage
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1718-L1736 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1821-L1839 at v20.2.4)
// over user's shards, every shard without a user, with each shard's rounds
// bounded (trimUsageShard) where radosgw's are not.
func (s *Store) TrimUsage(ctx context.Context, user, bucket string, start, end uint64) error {
	pool, err := s.pools.get(ctx, s.zone.Params.UsageLogPool)
	if err != nil {
		return err
	}
	first := s.usageOID(user, 0)
	oid := first
	for index := uint32(1); ; index++ {
		if err := s.trimUsageShard(ctx, pool, oid, user, bucket, start, end); err != nil {
			return err
		}
		if oid = s.usageOID(user, index); oid == first {
			return nil
		}
	}
}

// usageTrimCapFactor multiplies usageTrimRounds into the most rounds one
// shard's trim runs while it still makes progress.
var usageTrimCapFactor = 10

// trimUsageShard is cls_rgw_usage_log_trim_repeat
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10197-L10211 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11121-L11135 at v20.2.4)
// on one usage object, which repeats user_usage_log_trim until ENODATA, or
// ENOENT for a shard without a usage object. Each call walks its window
// from the range's start
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3791 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4193 at v20.2.4),
// so records it skips pile up at the window's front and each round removes
// fewer; on Squid and Tentacle the class may never answer ENODATA at all,
// for a record filed under its payer (tracker #72593) or a bucket filter
// behind 1000 other records (#58136). So after each round that answers 0
// the shard is read again with the same filter, and the trim stops, logged
// and no failure, once a round changed nothing that read sees, where
// radosgw's loop would spin. A cap of usageTrimCapFactor times
// usageTrimRounds of the first read's count backs that up.
func (s *Store) trimUsageShard(ctx context.Context, pool radosclient.Pool, oid, user, bucket string, start, end uint64) error {
	before, err := s.countUsage(ctx, pool, oid, user, bucket, start, end)
	if err != nil {
		return err
	}
	limit := usageTrimCapFactor * usageTrimRounds(before.windows)
	for round := 1; ; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		w := radosclient.NewWriteOp()
		rgwcls.UsageLogTrim(w, rgwcls.UsageTrimOp{StartEpoch: start, EndEpoch: end, User: user, Bucket: bucket}, s.release)
		_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
		switch {
		case errors.Is(err, radosclient.ErrNoData), errors.Is(err, radosclient.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("trimming usage log object %s: %w", oid, err)
		}
		after, err := s.countUsage(ctx, pool, oid, user, bucket, start, end)
		if err != nil {
			return err
		}
		if after == before {
			slog.WarnContext(ctx, "usage trim stopped: a round removed nothing, so the records left are payer-keyed (tracker #72593) or behind 1000 records of other buckets (#58136)",
				slog.String("oid", oid), slog.Int("rounds", round))
			return nil
		}
		if round >= limit {
			slog.WarnContext(ctx, "usage trim stopped at its round cap while still removing records; records may remain",
				slog.String("oid", oid), slog.Int("rounds", round))
			return nil
		}
		before = after
	}
}

// usageTrimRounds is the rounds a trim of n keys' windows takes when every
// key in each window is removed, ceil(n/1000) + 1; trimUsageShard's cap is
// a multiple of it.
func usageTrimRounds(n int) int {
	return (n+usageTrimWindow-1)/usageTrimWindow + 1
}

// usageCount is what a read of one shard sees of the records a trim's
// filter matches: windows counts a truncated page as the full window of
// keys it walked and the last page as its records, and the rest are the
// pages' record count and summed counters. A round that removes a record
// the read sees changes it.
type usageCount struct {
	windows, records                             int
	bytesSent, bytesReceived, ops, successfulOps uint64
	bytesProcessed, bytesReturned                uint64
}

// countUsage pages user_usage_log_read over one shard in the trim's window
// size and counts what it reads; a shard without a usage object counts
// nothing.
func (s *Store) countUsage(ctx context.Context, pool radosclient.Pool, oid, user, bucket string, start, end uint64) (usageCount, error) {
	var c usageCount
	q := rgwcls.UsageReadOp{StartEpoch: start, EndEpoch: end, Owner: user, Bucket: bucket, MaxEntries: usageTrimWindow}
	for {
		if err := ctx.Err(); err != nil {
			return usageCount{}, err
		}
		ret, err := s.readUsageShard(ctx, pool, oid, q)
		if errors.Is(err, radosclient.ErrNotFound) {
			return c, nil
		}
		if err != nil {
			return usageCount{}, err
		}
		c.records += len(ret.Usage)
		for _, e := range ret.Usage {
			c.bytesSent += e.TotalUsage.BytesSent
			c.bytesReceived += e.TotalUsage.BytesReceived
			c.ops += e.TotalUsage.Ops
			c.successfulOps += e.TotalUsage.SuccessfulOps
			c.bytesProcessed += e.S3SelectUsage.BytesProcessed
			c.bytesReturned += e.S3SelectUsage.BytesReturned
		}
		switch {
		case !ret.Truncated:
			c.windows += len(ret.Usage)
			return c, nil
		case ret.NextIter == "" || ret.NextIter == q.Iter:
			// A truncated read that does not move would page for ever.
			c.windows += usageTrimWindow
			return c, nil
		}
		c.windows += usageTrimWindow
		q.Iter = ret.NextIter
	}
}
