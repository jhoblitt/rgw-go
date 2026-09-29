package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Stats are a bucket's or user's totals.
type Stats struct {
	Size        uint64
	SizeRounded uint64
	NumObjects  uint64
}

//counterfeiter:generate . StatsStore

// StatsStore serves bucket and user statistics with radosgw's cache TTLs and
// enforces quotas from them.
type StatsStore interface {
	BucketStats(ctx context.Context, rec *BucketRecord) (Stats, error)
	UserStats(ctx context.Context, owner meta.Owner) (Stats, error)
	// CheckQuota fails with ErrQuotaExceeded when adding addBytes and addObjs
	// to rec or to owner's totals would exceed an enabled quota.
	CheckQuota(ctx context.Context, rec *BucketRecord, owner meta.Owner, addBytes, addObjs int64) error
}
