package memstore

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// BucketStats implements op.StatsStore.
func (s *Store) BucketStats(_ context.Context, rec *op.BucketRecord) (op.Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := s.instance(rec)
	if err != nil {
		return op.Stats{}, err
	}
	return stats(b), nil
}

// UserStats implements op.StatsStore over the owner's buckets.
func (s *Store) UserStats(_ context.Context, owner meta.Owner) (op.Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerStats(owner), nil
}

// CheckQuota implements op.StatsStore as RGWOp::init_quota and
// RGWQuotaHandlerImpl::check_quota check a write (rgw_op.cc:1383-1449 and
// rgw_quota.cc:912-955 at v19.2.6): the effective bucket quota against the
// bucket's totals, then the effective user quota against the owner's,
// without the driver's caches. An owner it does not hold fails the check
// with radosgw's bare ENOENT, NoSuchKey.
func (s *Store) CheckQuota(_ context.Context, rec *op.BucketRecord, owner meta.Owner, addBytes, addObjs int64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bq, uq, err := s.effectiveQuotas(rec, owner)
	if err != nil {
		return err
	}
	if bq.Enabled {
		b, err := s.instance(rec)
		if err != nil {
			return err
		}
		if exceeds(bq, stats(b), addBytes, addObjs) {
			return fmt.Errorf("bucket %s: %w", rec.Info.Bucket.Name, op.ErrQuotaExceeded)
		}
	}
	if exceeds(uq, s.ownerStats(owner), addBytes, addObjs) {
		return fmt.Errorf("user %s: %w", owner, op.ErrQuotaExceeded)
	}
	return nil
}

// effectiveQuotas is init_quota's precedence: the period's quotas, then the
// bucket's own bucket quota or else the owner's when enabled, and the
// owner's user quota when enabled.
func (s *Store) effectiveQuotas(rec *op.BucketRecord, owner meta.Owner) (bucket, user meta.Quota, err error) {
	var ownerBucket, ownerUser meta.Quota
	if owner.User != nil {
		u, ok := s.users[owner.User.String()]
		if !ok {
			return meta.Quota{}, meta.Quota{}, fmt.Errorf("bucket owner %s: %w", owner, op.ErrNoSuchKey)
		}
		ownerBucket, ownerUser = u.Info.BucketQuota, u.Info.UserQuota
	} else {
		a, ok := s.accounts[owner.Account]
		if !ok {
			return meta.Quota{}, meta.Quota{}, fmt.Errorf("bucket owner %s: %w", owner, op.ErrNoSuchKey)
		}
		ownerBucket, ownerUser = a.Info.BucketQuota, a.Info.Quota
	}
	bucket, user = s.cfg.Period.PeriodConfig.BucketQuota, s.cfg.Period.PeriodConfig.UserQuota
	switch {
	case rec.Info.Quota.Enabled:
		bucket = rec.Info.Quota
	case ownerBucket.Enabled:
		bucket = ownerBucket
	}
	if ownerUser.Enabled {
		user = ownerUser
	}
	return bucket, user, nil
}

// AdjustStats implements op.StatsStore. memstore keeps no quota cache: its
// totals are summed from its objects at every read, and those already hold
// the write.
func (s *Store) AdjustStats(context.Context, *op.BucketRecord, meta.Owner, int64, int64, int64) error {
	return nil
}

// stats totals a bucket's objects; SizeRounded rounds each object up to
// 4 KiB, as rgw_rounded_objsize does.
func stats(b *bucket) op.Stats {
	var st op.Stats
	for _, o := range b.objects {
		st.Size += o.state.Size
		st.SizeRounded += roundedObjSize(o.state.Size)
		st.NumObjects++
	}
	return st
}

func (s *Store) ownerStats(owner meta.Owner) op.Stats {
	var st op.Stats
	for _, b := range s.buckets {
		if b.rec.Info.Owner.String() == owner.String() {
			bs := stats(b)
			st.Size += bs.Size
			st.SizeRounded += bs.SizeRounded
			st.NumObjects += bs.NumObjects
		}
	}
	return st
}

// roundedObjSize is rgw_rounded_objsize: bytes rounded up to 4 KiB.
func roundedObjSize(bytes uint64) uint64 { return (bytes + 4095) &^ 4095 }

// exceeds is check_quota for one quota (rgw_quota.cc:877-904 at v19.2.6)
// through the applier the quota selects (:769-866): the objects, then the
// size, which the default applier takes as size_rounded plus the rounded
// addition and CheckOnRaw's as the raw size plus the raw addition. A
// negative limit is none. radosgw's additions are unsigned, so a negative
// one counts as none.
func exceeds(q meta.Quota, st op.Stats, addBytes, addObjs int64) bool {
	if !q.Enabled {
		return false
	}
	objs, bytes := uint64(max(addObjs, 0)), uint64(max(addBytes, 0))
	if q.MaxObjects >= 0 && st.NumObjects+objs > uint64(q.MaxObjects) {
		return true
	}
	if q.MaxSize < 0 {
		return false
	}
	if q.CheckOnRaw {
		return st.Size+bytes > uint64(q.MaxSize)
	}
	return st.SizeRounded+roundedObjSize(bytes) > uint64(q.MaxSize)
}
