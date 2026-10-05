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
// zone the gateway serves from the root pools, reads the options, creates
// the control objects in the zone's control pool, and logs the zone and the
// options that ask for a worker it does not run. The control watches, which
// keep the metadata cache coherent with every other gateway, run as Run's
// control-watch worker, and the retries of bucket index completions a
// reshard refused as its index-completions worker.
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
	sys, err := openSysObj(ctx, pools, zc.Params, opts, release)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	zc.log(ctx)
	opts.logWorkersNotRun(ctx)
	s := &Store{
		cluster: cluster, conf: conf, opts: opts, release: release, zone: zc, pools: pools, sysobj: sys, readCfg: readCfg,
		newTicker: newTimeTicker,
	}
	s.heads = s
	s.bgAIO = semaphore.NewWeighted(int64(opts.bucketIndexMaxAIO))
	s.w = newWriter(ctx, s, wopts)
	s.AddWorker("control-watch", sys.notify.run)
	s.AddWorker("index-completions", s.w.completions.run)
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

// GetAccount implements op.AccountStore.
func (s *Store) GetAccount(context.Context, string) (*op.AccountRecord, error) {
	return nil, op.ErrNotImplemented
}

// AccountName implements op.AccountStore.
func (s *Store) AccountName(context.Context, string) (string, error) {
	return "", op.ErrNotImplemented
}

// GetAccountByName implements op.AccountStore.
func (s *Store) GetAccountByName(context.Context, string, string) (*op.AccountRecord, error) {
	return nil, op.ErrNotImplemented
}

// GetAccountByEmail implements op.AccountStore.
func (s *Store) GetAccountByEmail(context.Context, string) (*op.AccountRecord, error) {
	return nil, op.ErrNotImplemented
}

// PutAccount implements op.AccountStore.
func (s *Store) PutAccount(context.Context, *op.AccountRecord, *meta.AccountInfo, op.PutAccountOptions) error {
	return op.ErrNotImplemented
}

// RemoveAccount implements op.AccountStore.
func (s *Store) RemoveAccount(context.Context, *op.AccountRecord) error {
	return op.ErrNotImplemented
}

// AddAccountUser implements op.AccountStore.
func (s *Store) AddAccountUser(context.Context, string, meta.UserInfo) error {
	return op.ErrNotImplemented
}

// RemoveAccountUser implements op.AccountStore.
func (s *Store) RemoveAccountUser(context.Context, string, string) error {
	return op.ErrNotImplemented
}

// ListAccountUsers implements op.AccountStore.
func (s *Store) ListAccountUsers(context.Context, string, string, uint32) (ids []string, next string, err error) {
	return nil, "", op.ErrNotImplemented
}

// ReadUsage implements op.UsageReader.
func (s *Store) ReadUsage(context.Context, string, string, uint64, uint64, uint32, *op.UsageIter) (recs []op.UsageRecord, truncated bool, err error) {
	return nil, false, op.ErrNotImplemented
}

// TrimUsage implements op.UsageReader.
func (s *Store) TrimUsage(context.Context, string, string, uint64, uint64) error {
	return op.ErrNotImplemented
}

// IndexStats implements op.BucketAdminStore.
func (s *Store) IndexStats(context.Context, *op.BucketRecord) (op.BucketIndexStats, error) {
	return op.BucketIndexStats{}, op.ErrNotImplemented
}

// ChangeBucketOwner implements op.BucketAdminStore.
func (s *Store) ChangeBucketOwner(context.Context, *op.BucketRecord, meta.Owner, string, *meta.BucketID) error {
	return op.ErrNotImplemented
}

// UnlinkBucketOwner implements op.BucketAdminStore.
func (s *Store) UnlinkBucketOwner(context.Context, *op.BucketRecord, meta.Owner) error {
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

// ChownBucket implements op.BucketAdminStore.
func (s *Store) ChownBucket(context.Context, *op.BucketRecord, meta.Owner, string) error {
	return op.ErrNotImplemented
}

// SyncOwnerStats implements op.BucketAdminStore.
func (s *Store) SyncOwnerStats(context.Context, meta.Owner) error {
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

// CreateBucket implements op.BucketStore.
func (s *Store) CreateBucket(context.Context, op.CreateBucketParams) (*op.BucketRecord, error) {
	return nil, op.ErrNotImplemented
}

// DeleteBucket implements op.BucketStore.
func (s *Store) DeleteBucket(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
}

// CopyObject implements op.ObjectStore.
func (s *Store) CopyObject(context.Context, *op.ObjectState, *op.BucketRecord, meta.ObjKey, op.CopyParams) (*op.PutResult, error) {
	return nil, op.ErrNotImplemented
}

// ListParts implements op.MultipartStore.
func (s *Store) ListParts(context.Context, *op.Upload, int, int) (op.ListPartsResult, error) {
	return op.ListPartsResult{}, op.ErrNotImplemented
}

// ListUploads implements op.MultipartStore.
func (s *Store) ListUploads(context.Context, *op.BucketRecord, op.ListUploadsParams) (op.ListUploadsResult, error) {
	return op.ListUploadsResult{}, op.ErrNotImplemented
}

// Complete implements op.MultipartStore.
func (s *Store) Complete(context.Context, *op.Upload, []op.CompletePart) (*op.PutResult, error) {
	return nil, op.ErrNotImplemented
}

// Abort implements op.MultipartStore.
func (s *Store) Abort(context.Context, *op.Upload) error {
	return op.ErrNotImplemented
}

// Get implements op.MetadataStore.
func (s *Store) Get(context.Context, string, string) (op.MetadataEntry, error) {
	return op.MetadataEntry{}, op.ErrNotImplemented
}

// Put implements op.MetadataStore.
func (s *Store) Put(context.Context, string, string, op.MetadataEntry, op.PutMetadataOptions) error {
	return op.ErrNotImplemented
}

// Remove implements op.MetadataStore.
func (s *Store) Remove(context.Context, string, string) error {
	return op.ErrNotImplemented
}

// List implements op.MetadataStore.
func (s *Store) List(context.Context, string, string, int) (keys []string, next string, more bool, err error) {
	return nil, "", false, op.ErrNotImplemented
}
