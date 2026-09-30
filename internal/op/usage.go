package op

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// UsageEntry is one op's contribution to the usage log, rgw_usage_data keyed
// as radosgw keys it.
type UsageEntry struct {
	// Owner is the bucket's owner, or the identity's owner for a request
	// without a bucket.
	Owner meta.Owner
	// Payer is the requesting user, never its account, on a request to a
	// requester-pays bucket that was not refused with 403; zero otherwise.
	// radosgw files the entry under the payer when there is one, else under
	// the owner, and records both (UsageLogger::insert, rgw_log.cc:159-165 at
	// v19.2.6 and v20.2.4).
	Payer         meta.Owner
	Bucket        string
	Time          time.Time
	Category      string // the op name
	BytesSent     uint64
	BytesReceived uint64
	Ops           uint64
	SuccessfulOps uint64
}

//counterfeiter:generate . UsageLogger

// UsageLogger accumulates usage entries; the driver flushes them on its schedule.
type UsageLogger interface {
	Log(ctx context.Context, e UsageEntry)
}
