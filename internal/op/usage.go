package op

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// UsageEntry is one op's contribution to the usage log, rgw_usage_data keyed
// as radosgw keys it.
type UsageEntry struct {
	Owner         meta.Owner
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
