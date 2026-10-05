package driver

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// lcHashPrime is HASH_PRIME, the most lifecycle shards radosgw uses
// (rgw_lc.h:27; RGWLC::initialize caps rgw_lc_max_objs at it, rgw_lc.cc:237-239,
// at v19.2.6 and v20.2.4).
const lcHashPrime = 7877

// options are the rgw_* tunables the driver reads, once, at Open, so a
// change made while the gateway runs takes effect at its next start
// (docs/exclusions.md). The defaults in the comments are rgw.yaml.in's at
// v19.2.6 and v20.2.4.
type options struct {
	cacheEnabled     bool          // rgw_cache_enabled, true
	cacheLRUSize     int           // rgw_cache_lru_size, 25000
	cacheExpiry      time.Duration // rgw_cache_expiry_interval, 900 s
	numControlOIDs   int64         // rgw_num_control_oids, 8
	maxNotifyRetries uint64        // rgw_max_notify_retries, 10

	usageLogEnabled     bool          // rgw_enable_usage_log, false
	usageFlushThreshold int           // rgw_usage_log_flush_threshold, 1024
	usageTick           time.Duration // rgw_usage_log_tick_interval, 30 s
	usageMaxShards      uint32        // rgw_usage_max_shards, 32, floored to 1
	usageMaxUserShards  uint32        // rgw_usage_max_user_shards, 1, floored to 1
	lcMaxObjs           uint32        // rgw_lc_max_objs, 32, floored to 1 and capped at lcHashPrime; for lifecycle, which rgw-go does not serve yet

	bucketQuotaTTL       time.Duration // rgw_bucket_quota_ttl, 600 s
	bucketQuotaCacheSize int           // rgw_bucket_quota_cache_size, 10000
	bucketSyncInterval   time.Duration // rgw_user_quota_bucket_sync_interval, 180 s
	ownerSyncInterval    time.Duration // rgw_user_quota_sync_interval, 86400 s
	ownerSyncWait        time.Duration // rgw_user_quota_sync_wait_time, 86400 s
	ownerSyncIdle        bool          // rgw_user_quota_sync_idle_users, false
	quotaThreads         bool          // rgw_enable_quota_threads, true
	listBucketsChunk     int           // rgw_list_buckets_max_chunk, 1000, as op.ListBucketsChunk reads it

	listMinReadahead       int    // rgw_list_bucket_min_readahead, 1000
	overrideIndexMaxShards uint32 // rgw_override_bucket_index_max_shards, 0
	bucketIndexMaxAIO      int    // rgw_bucket_index_max_aio, 128, floored to 1
	dynamicResharding      bool   // rgw_dynamic_resharding, true: warned about at startup, never run (docs/exclusions.md)
	runSyncThread          bool   // rgw_run_sync_thread, true: logged at startup; a single zone has nothing to sync
}

// readOptions reads the options through conf, each through the accessor its
// rgw.yaml.in type calls for, and fails on the first option librados does
// not know or whose value does not parse. A value its field cannot hold is
// used as the nearest one it can. A shard count or bucket index AIO limit
// that is not positive is used as 1, with an error-level log line naming the
// option and the value read: radosgw faults on a zero shard count, and on a
// zero AIO limit it skips or never finishes every batch of index operations
// (docs/ceph-upstream-bugs.md).
func readOptions(conf *cephconf.Options) (options, error) {
	var r reads
	o := options{
		cacheEnabled:     readOption(&r, conf.Bool, "rgw_cache_enabled"),
		cacheLRUSize:     int(readOption(&r, conf.Int64, "rgw_cache_lru_size")),
		cacheExpiry:      secondsToDuration(saturate(readOption(&r, conf.Uint64, "rgw_cache_expiry_interval"), int64(math.MaxInt64))),
		numControlOIDs:   readOption(&r, conf.Int64, "rgw_num_control_oids"),
		maxNotifyRetries: readOption(&r, conf.Uint64, "rgw_max_notify_retries"),

		usageLogEnabled:     readOption(&r, conf.Bool, "rgw_enable_usage_log"),
		usageFlushThreshold: int(readOption(&r, conf.Int64, "rgw_usage_log_flush_threshold")),
		usageTick:           secondsToDuration(readOption(&r, conf.Int64, "rgw_usage_log_tick_interval")),

		bucketQuotaTTL:       secondsToDuration(readOption(&r, conf.Int64, "rgw_bucket_quota_ttl")),
		bucketQuotaCacheSize: int(readOption(&r, conf.Int64, "rgw_bucket_quota_cache_size")),
		bucketSyncInterval:   secondsToDuration(readOption(&r, conf.Int64, "rgw_user_quota_bucket_sync_interval")),
		ownerSyncInterval:    secondsToDuration(readOption(&r, conf.Int64, "rgw_user_quota_sync_interval")),
		ownerSyncWait:        secondsToDuration(readOption(&r, conf.Int64, "rgw_user_quota_sync_wait_time")),
		ownerSyncIdle:        readOption(&r, conf.Bool, "rgw_user_quota_sync_idle_users"),
		quotaThreads:         readOption(&r, conf.Bool, "rgw_enable_quota_threads"),
		listBucketsChunk:     op.ListBucketsChunk(readOption(&r, conf.Int64, "rgw_list_buckets_max_chunk")),

		listMinReadahead:       int(readOption(&r, conf.Int64, "rgw_list_bucket_min_readahead")),
		overrideIndexMaxShards: saturate(readOption(&r, conf.Uint64, "rgw_override_bucket_index_max_shards"), uint32(math.MaxUint32)),
		dynamicResharding:      readOption(&r, conf.Bool, "rgw_dynamic_resharding"),
		runSyncThread:          readOption(&r, conf.Bool, "rgw_run_sync_thread"),
	}
	usageShards := readOption(&r, conf.Int64, "rgw_usage_max_shards")
	userShards := readOption(&r, conf.Int64, "rgw_usage_max_user_shards")
	lcObjs := readOption(&r, conf.Int64, "rgw_lc_max_objs")
	aio := readOption(&r, conf.Uint64, "rgw_bucket_index_max_aio")
	if r.err != nil {
		return options{}, fmt.Errorf("reading the driver options: %w", r.err)
	}
	// Floored only now, so that a count a failed read left at zero is not
	// reported.
	o.usageMaxShards = floorShards("rgw_usage_max_shards", usageShards)
	o.usageMaxUserShards = floorShards("rgw_usage_max_user_shards", userShards)
	o.lcMaxObjs = min(floorShards("rgw_lc_max_objs", lcObjs), lcHashPrime)
	if aio == 0 {
		slog.Error("bucket index aio limit is not positive; using 1",
			slog.String("option", "rgw_bucket_index_max_aio"), slog.Uint64("value", aio))
		aio = 1
	}
	o.bucketIndexMaxAIO = saturate(aio, math.MaxInt)
	return o, nil
}

// reads holds the first failure of the reads made through readOption.
type reads struct{ err error }

// readOption returns get(name), or the zero value once r holds an earlier
// failure.
func readOption[T any](r *reads, get func(string) (T, error), name string) T {
	var v T
	if r.err == nil {
		v, r.err = get(name)
	}
	return v
}

// saturate converts n to T, using ceiling, T's largest value, for an n above
// it.
func saturate[T int | int64 | uint32](n uint64, ceiling T) T {
	if n > uint64(ceiling) {
		return ceiling
	}
	return T(n)
}

// secondsToDuration converts a count of seconds to a Duration, using the
// nearest one a Duration holds, about 292 years either way, for a count
// beyond them.
func secondsToDuration(n int64) time.Duration {
	const limit = math.MaxInt64 / int64(time.Second)
	switch {
	case n > limit:
		return math.MaxInt64
	case n < -limit:
		return math.MinInt64
	}
	return time.Duration(n) * time.Second
}

// positiveInterval returns d, a worker's interval read from option, or one
// second with an error-level log line naming the option and the value read
// when d is not positive: radosgw's usage-log timer and quota sync threads
// then run without pause, and a time.Ticker refuses it
// (docs/ceph-upstream-bugs.md, tracker #81226).
func positiveInterval(ctx context.Context, option string, d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	slog.ErrorContext(ctx, "worker interval is not positive; using 1 second (tracker #81226)",
		slog.String("option", option), slog.Int64("value", int64(d/time.Second)))
	return time.Second
}

// logWorkersNotRun logs each enabled option whose worker rgw-go does not run.
func (o options) logWorkersNotRun(ctx context.Context) {
	if o.dynamicResharding {
		slog.WarnContext(ctx, "dynamic resharding is enabled in config but rgw-go runs no reshard worker")
	}
	if o.runSyncThread {
		slog.InfoContext(ctx, "rgw_run_sync_thread is set; a single zone has nothing to sync")
	}
}
