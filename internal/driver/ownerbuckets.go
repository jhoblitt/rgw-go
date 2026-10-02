package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// An owner's bucket list is the cls_user omap of its buckets object, which
// rgwrados::buckets (driver/rados/buckets.cc) reads and writes straight
// through RADOS, past the metadata cache. A bare line number is
// buckets.cc's at v19.2.6, and it is the same at v20.2.4. An op that
// carries a time carries the gateway's clock, as cls_user_set_buckets and
// cls_user_complete_stats_sync stamp radosgw's (cls/user/cls_user_client.cc:25
// and :34 at both tags).

// clsBucket is rgw_bucket::convert (rgw_basic_types.cc:52-60 at both tags):
// the name, marker and id, and the explicit placement's pools in their
// string form.
func clsBucket(b meta.BucketID) user.Bucket {
	p := b.ExplicitPlacement
	return user.Bucket{
		Name: b.Name, Marker: b.Marker, BucketID: b.ID,
		DataPool: p.DataPool.String(), DataExtraPool: p.DataExtraPool.String(), IndexPool: p.IndexPool.String(),
	}
}

// ownerBucketsPool opens the pool of owner's buckets object and names it.
func (s *Store) ownerBucketsPool(ctx context.Context, owner meta.Owner) (radosclient.Pool, string, error) {
	o := s.ownerBucketsObj(owner)
	p, err := s.pools.get(ctx, o.pool)
	return p, o.oid, err
}

// setOwnerBucket is buckets.cc's set (:27-43): cls_user_set_buckets of the
// one entry.
func (s *Store) setOwnerBucket(ctx context.Context, owner meta.Owner, entry user.BucketEntry, add bool) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.SetBucketsInfo(wop, []user.BucketEntry{entry}, add, s.sysobj.now(), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

// linkBucket is buckets::add (:45-60): the class creates the owner's entry
// for b, or gives an existing one b's bucket id and created, keeping its
// stats. A zero created is the clock's time.
func (s *Store) linkBucket(ctx context.Context, owner meta.Owner, b meta.BucketID, created time.Time) error {
	if created.IsZero() {
		created = s.sysobj.now()
	}
	return s.setOwnerBucket(ctx, owner, user.BucketEntry{Bucket: clsBucket(b), CreationTime: created}, true)
}

// unlinkBucket is buckets::remove (:62-78): the class removes the owner's
// entry for b, a missing one being no failure.
func (s *Store) unlinkBucket(ctx context.Context, owner meta.Owner, b meta.BucketID) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.RemoveBucket(wop, clsBucket(b), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

// writeOwnerBucketStats is buckets::write_stats (:142-151): ent's stats
// replace those of the owner's entry for its bucket, which the class skips
// when the list no longer holds it.
func (s *Store) writeOwnerBucketStats(ctx context.Context, owner meta.Owner, ent meta.BucketEnt) error {
	return s.setOwnerBucket(ctx, owner, user.BucketEntry{
		Bucket: clsBucket(ent.Bucket), Size: ent.Size, SizeRounded: ent.SizeRounded,
		Count: ent.Count, CreationTime: ent.CreationTime.Time,
	}, false)
}

// readOwnerStats is buckets::read_stats (:153-184): the owner's summed
// stats and the header's last sync and last update, all zero when the
// owner has no buckets object.
func (s *Store) readOwnerStats(ctx context.Context, owner meta.Owner) (stats op.Stats, lastSync, lastUpdate time.Time, err error) {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return op.Stats{}, time.Time{}, time.Time{}, err
	}
	rop := radosclient.NewReadOp()
	res := user.GetHeader(rop, s.release)
	_, err = pool.Read(ctx, oid, rop, radosclient.OpFlagNone)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return op.Stats{}, time.Time{}, time.Time{}, nil
	case err != nil:
		return op.Stats{}, time.Time{}, time.Time{}, err
	}
	h, err := res.Header()
	if err != nil {
		return op.Stats{}, time.Time{}, time.Time{}, fmt.Errorf("%w: the header of %s: %w", op.ErrUnknown, oid, err)
	}
	st := h.Stats
	return op.Stats{Size: st.TotalBytes, SizeRounded: st.TotalBytesRounded, NumObjects: st.TotalEntries}, h.LastStatsSync, h.LastStatsUpdate, nil
}

// completeOwnerStatsSync is buckets::complete_flush_stats (:261-273): the
// header's last sync becomes the clock's time when that is later.
func (s *Store) completeOwnerStatsSync(ctx context.Context, owner meta.Owner) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.CompleteStatsSync(wop, s.sysobj.now(), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

// syncOwnerStats is RGWBucketCtl::sync_owner_stats
// (driver/rados/rgw_bucket.cc:3474-3501 at v19.2.6, :3556-3583 at v20.2.4):
// the bucket's Main-category index stats, written into its owner's entry.
// It returns the stats it read.
func (s *Store) syncOwnerStats(ctx context.Context, owner meta.Owner, info *meta.BucketInfo) (meta.BucketEnt, error) {
	ent, err := s.readIndexStats(ctx, info)
	if err != nil {
		return meta.BucketEnt{}, err
	}
	return ent, s.writeOwnerBucketStats(ctx, owner, ent)
}

// ownerTenant is the tenant an owner's listed buckets belong to: the
// user's, or the account's. radosgw takes the requester's tenant
// (rgw_op.cc:2567-2568 at v19.2.6), which for an account's user is its
// account's.
func (s *Store) ownerTenant(ctx context.Context, owner meta.Owner) (string, error) {
	if owner.User != nil {
		return owner.User.Tenant, nil
	}
	acct, err := s.readAccount(ctx, owner.Account)
	if err != nil {
		return "", err
	}
	return acct.Tenant, nil
}

// ListUserBuckets implements op.UserStore as rgwrados::buckets::list
// (:80-140): cls_user_bucket_list from marker for what maxEntries leaves,
// again while the class's page is truncated and fewer than maxEntries are
// listed. Each entry is a bucket of the owner's tenant with the stats it
// last synced. A missing buckets object ends the listing without error.
//
// next is the class's marker when the class truncated its page, and more
// is that truncation. radosgw's listing carries the marker alone, and an
// empty one ends it (rgw_sal.h:244-249 and rgw_op.cc:2604 at v19.2.6,
// :229-234 and :2836 at v20.2.4), so a truncation without a marker is not
// more. A maxEntries of 0, which radosgw is asked for when
// rgw_list_buckets_max_chunk is 0, is one class call of 0 entries, which
// the OSD truncates before the first key (osd/PrimaryLogPG.cc:7744 at
// v19.2.6, :7821 at v20.2.4) and the class answers with no marker.
// radosgw's maximum is unsigned (:83), so a negative maxEntries is a
// caller's bug and an InternalError.
func (s *Store) ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, maxEntries int) (ents []meta.BucketEnt, next string, more bool, err error) {
	if maxEntries < 0 {
		return nil, "", false, fmt.Errorf("%w: listing the buckets of %s with a negative maximum %d", op.ErrInternalError, owner, maxEntries)
	}
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return nil, "", false, err
	}
	var tenant string
	resolved := false
	for {
		// The class takes the count as an int and caps it at 1000.
		count := min(maxEntries-len(ents), math.MaxInt32)
		rop := radosclient.NewReadOp()
		res := user.ListBuckets(rop, marker, "", int32(count), s.release) //nolint:gosec // within [0, MaxInt32]: maxEntries is not negative and the loop stops at it
		if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
			if errors.Is(err, radosclient.ErrNotFound) {
				return ents, "", false, nil
			}
			return nil, "", false, err
		}
		entries, page, truncated, err := res.Entries()
		if err != nil {
			return nil, "", false, fmt.Errorf("%w: listing %s: %w", op.ErrUnknown, oid, err)
		}
		if len(entries) > 0 && !resolved {
			if tenant, err = s.ownerTenant(ctx, owner); err != nil {
				return nil, "", false, err
			}
			resolved = true
		}
		for i := range entries {
			e := &entries[i]
			ents = append(ents, meta.BucketEnt{
				Bucket:       meta.BucketID{Tenant: tenant, Name: e.Bucket.Name, Marker: e.Bucket.Marker, ID: e.Bucket.BucketID},
				Size:         e.Size,
				SizeRounded:  e.SizeRounded,
				CreationTime: meta.Time{Time: e.CreationTime},
				Count:        e.Count,
			})
		}
		marker, more = page, truncated
		if !more || len(ents) >= maxEntries {
			break
		}
	}
	if !more || marker == "" {
		return ents, "", false, nil
	}
	return ents, marker, true, nil
}
