package driver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Options tunes Open.
type Options struct {
	// Release overrides the encoding release detected from the cluster's
	// required OSD release; nil detects.
	Release *denc.Release
}

// Store is the RADOS driver: one type implementing every store interface in
// op over the seam. A method that is not implemented yet returns
// op.ErrNotImplemented.
type Store struct {
	cluster radosclient.Cluster
	conf    *cephconf.Options
	opts    options
	release denc.Release
	zone    *zoneConfig
	pools   *poolCache
	sysobj  *sysobjs
	readCfg readConfig
	w       *writer
	mp      mpOptions
	// headBufs holds the *[]byte buffers prefetches read the head into.
	headBufs sync.Pool
	// readBufs holds the *[]byte buffers of rgw_get_obj_max_req_size that
	// data reads of more than half that go into, and pooledReadBufs counts
	// those handed back and not taken out again.
	readBufs       sync.Pool
	pooledReadBufs atomic.Int64
	// tailBufs holds the *[]byte buffers a PUT reads its tail pieces into,
	// each at least one chunk.
	tailBufs sync.Pool
	quota    statsCaches
	usage    usageLogger
	// newTicker makes the tickers the workers run on.
	newTicker func(time.Duration) ticker
	// heads is what a listing checks a pending index entry's head with: the
	// Store itself, or a spec's fake.
	heads HeadStater
	// bgAIO bounds the index suggestions a listing sends in the background
	// at rgw_bucket_index_max_aio.
	bgAIO *semaphore.Weighted
	// sweepLimit bounds the stripes sweepParts walks; 0 is
	// meta.MaxWalkStripes.
	sweepLimit int
	// nextBucketID is next_bucket_id's counter, the last number a bucket id
	// was given.
	nextBucketID atomic.Uint64

	mu      sync.Mutex
	started bool // Run has taken the workers
	workers []worker
}

var (
	_ op.ZoneInfo         = (*Store)(nil)
	_ op.UserStore        = (*Store)(nil)
	_ op.AccountStore     = (*Store)(nil)
	_ op.BucketStore      = (*Store)(nil)
	_ op.ObjectStore      = (*Store)(nil)
	_ op.MultipartStore   = (*Store)(nil)
	_ op.StatsStore       = (*Store)(nil)
	_ op.UsageLogger      = (*Store)(nil)
	_ op.MetadataStore    = (*Store)(nil)
	_ op.UsageReader      = (*Store)(nil)
	_ op.BucketAdminStore = (*Store)(nil)
	_ op.RealmStore       = (*Store)(nil)
)

// Open connects the driver to cluster: it detects the release, resolves the
// zone the gateway serves from the root pools, then reads the options,
// creates the control objects in the zone's control pool, initializes the
// gc shards as RGWGC::initialize does, refusing a gc pool it cannot open,
// and logs the zone and the options that ask for a worker it does not run.
// The control watches, which keep the metadata cache coherent with every
// other gateway, run as Run's control-watch worker, the retries of bucket
// index completions a reshard refused as its index-completions worker, with
// rgw_enable_gc_threads the garbage collector as its gc worker, with
// rgw_enable_quota_threads the stats syncs as its quota workers, and with
// rgw_enable_usage_log the usage log's flushes as its usage-flush worker.
func Open(ctx context.Context, cluster radosclient.Cluster, conf *cephconf.Options, o Options) (*Store, error) {
	release, err := detectRelease(ctx, cluster, o.Release)
	if err != nil {
		return nil, err
	}
	names, err := readZoneNames(conf)
	if err != nil {
		return nil, err
	}
	pools := newPoolCache(cluster)
	zc, err := openZone(ctx, conf, pools, names, release)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	opts, err := readOptions(conf)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	readCfg, err := loadReadConfig(conf)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	wopts, err := readWriteOptions(conf)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	mp, err := readMPOptions(conf)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	sys, err := openSysObj(ctx, pools, zc.Params, opts, release)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	zc.log(ctx)
	opts.logWorkersNotRun(ctx)
	s := &Store{
		cluster: cluster, conf: conf, opts: opts, release: release, zone: zc, pools: pools, sysobj: sys, readCfg: readCfg,
		mp: mp, newTicker: newTimeTicker,
	}
	s.heads = s
	s.bgAIO = semaphore.NewWeighted(int64(opts.bucketIndexMaxAIO))
	s.w = newWriter(ctx, s, wopts)
	if err := s.gcInitialize(ctx); err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	s.AddWorker("control-watch", sys.notify.run)
	s.AddWorker("index-completions", s.w.completions.run)
	if wopts.gcThreads {
		s.AddWorker("gc", newGCWorker(s).run)
	}
	s.openQuota(ctx)
	s.openUsageLog(ctx)
	return s, nil
}

// openZone opens the root pools and resolves the zone from them.
func openZone(ctx context.Context, conf *cephconf.Options, pools *poolCache, names zoneNames, rel denc.Release) (*zoneConfig, error) {
	roots, err := openRootPools(ctx, conf, pools)
	if err != nil {
		return nil, err
	}
	return resolveZone(ctx, roots, names, rel)
}

// Close cancels the stats caches' refreshes under way and waits for them,
// then closes the pools the Store opened. The workers stop with Run's
// context, not here.
func (s *Store) Close() error {
	s.quota.close()
	return s.pools.closeAll()
}

func detectRelease(ctx context.Context, cluster radosclient.Cluster, override *denc.Release) (denc.Release, error) {
	if override != nil {
		return *override, nil
	}
	name, err := cluster.RequiredOSDRelease(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the required osd release: %w", err)
	}
	r, ok := denc.ClusterRelease(ctx, name)
	if !ok {
		return 0, fmt.Errorf("required osd release %q: %w", name, radosclient.ErrReleaseTooOld)
	}
	return r, nil
}

// Env returns an op.Env whose every store is s and whose options are the
// ones Open was given, with authz, metrics and the host id left for the
// caller.
func (s *Store) Env() *op.Env {
	return &op.Env{
		Zone:        s,
		Users:       s,
		Accounts:    s,
		UsageReader: s,
		BucketAdmin: s,
		Realms:      s,
		Buckets:     s,
		Objects:     s,
		Multipart:   s,
		Stats:       s,
		Usage:       s,
		Metadata:    s,
		Conf:        s.conf,
	}
}

// Release implements op.ZoneInfo.
func (s *Store) Release() denc.Release { return s.release }

// Zone implements op.ZoneInfo.
func (s *Store) Zone() meta.Zone { return s.zone.Zone }

// ZoneGroup implements op.ZoneInfo.
func (s *Store) ZoneGroup() meta.ZoneGroup { return s.zone.ZoneGroup }

// ZoneParams implements op.ZoneInfo.
func (s *Store) ZoneParams() meta.ZoneParams { return s.zone.Params }

// Realm implements op.ZoneInfo; it is the zero Realm without one.
func (s *Store) Realm() meta.Realm { return s.zone.Realm }

// Period implements op.ZoneInfo; it is the zero Period without one.
func (s *Store) Period() meta.Period { return s.zone.Period }

// PeriodConfig returns the realm's default quotas and rate limits: the
// period's config, or the realm's period_config object when the zonegroup
// is not in the period.
func (s *Store) PeriodConfig() meta.PeriodConfig { return s.zone.PeriodConfig }

// ReadUsage implements op.UsageReader.
func (s *Store) ReadUsage(context.Context, string, string, uint64, uint64, uint32, *op.UsageIter) (recs []op.UsageRecord, truncated bool, err error) {
	return nil, false, op.ErrNotImplemented
}

// TrimUsage implements op.UsageReader.
func (s *Store) TrimUsage(context.Context, string, string, uint64, uint64) error {
	return op.ErrNotImplemented
}

// CheckIndex implements op.BucketAdminStore.
func (s *Store) CheckIndex(context.Context, *op.BucketRecord) (existing, calculated map[string]op.CategoryStats, err error) {
	return nil, nil, op.ErrNotImplemented
}

// RebuildIndex implements op.BucketAdminStore.
func (s *Store) RebuildIndex(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
}

// RemoveIndexEntries implements op.BucketAdminStore.
func (s *Store) RemoveIndexEntries(context.Context, *op.BucketRecord, []meta.ObjKey) error {
	return op.ErrNotImplemented
}

// PurgeBypassGC implements op.BucketAdminStore.
func (s *Store) PurgeBypassGC(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
}

// GetRealm implements op.RealmStore.
func (s *Store) GetRealm(context.Context, string, string) (meta.Realm, error) {
	return meta.Realm{}, op.ErrNotImplemented
}

// ListRealms implements op.RealmStore.
func (s *Store) ListRealms(context.Context) (defaultID string, names []string, err error) {
	return "", nil, op.ErrNotImplemented
}

// GetPeriod implements op.RealmStore.
func (s *Store) GetPeriod(context.Context, string, string, uint32) (meta.Period, error) {
	return meta.Period{}, op.ErrNotImplemented
}

// GetPeriodConfig implements op.RealmStore.
func (s *Store) GetPeriodConfig(context.Context, string) (meta.PeriodConfig, error) {
	return meta.PeriodConfig{}, op.ErrNotImplemented
}

// PutPeriodConfig implements op.RealmStore.
func (s *Store) PutPeriodConfig(context.Context, string, meta.PeriodConfig) error {
	return op.ErrNotImplemented
}
