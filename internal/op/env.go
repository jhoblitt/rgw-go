package op

import (
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

// Env is the process-wide environment a request runs in: the stores, the
// authorizer, configuration and clocks. It is radosgw's RGWProcessEnv.
type Env struct {
	Zone      ZoneInfo
	Users     UserStore
	Buckets   BucketStore
	Objects   ObjectStore
	Multipart MultipartStore
	Stats     StatsStore
	Usage     UsageLogger
	Metadata  MetadataStore
	Authz     Authorizer
	Conf      *cephconf.Options
	Metrics   Metrics
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// HostID is radosgw's host_id, rendered in error documents.
	HostID string
}

// Clock returns the current time from Now or time.Now.
func (e *Env) Clock() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

//counterfeiter:generate . Metrics

// Metrics is the op-level observation hook; the metrics package implements it.
type Metrics interface {
	// InFlight adds delta to the in-flight request gauge.
	InFlight(delta int)
	// Observe records one finished request.
	Observe(opName string, status int, elapsed time.Duration, bytesIn, bytesOut int64)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

var _ Metrics = NopMetrics{}

// InFlight discards delta.
func (NopMetrics) InFlight(int) {}

// Observe discards the observation.
func (NopMetrics) Observe(string, int, time.Duration, int64, int64) {}
