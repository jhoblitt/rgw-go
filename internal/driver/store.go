package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

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

	mu      sync.Mutex
	started bool // Run has taken the workers
	workers []worker
}

var (
	_ op.ZoneInfo       = (*Store)(nil)
	_ op.UserStore      = (*Store)(nil)
	_ op.AccountStore   = (*Store)(nil)
	_ op.BucketStore    = (*Store)(nil)
	_ op.ObjectStore    = (*Store)(nil)
	_ op.MultipartStore = (*Store)(nil)
	_ op.StatsStore     = (*Store)(nil)
	_ op.UsageLogger    = (*Store)(nil)
	_ op.MetadataStore  = (*Store)(nil)
)

// Open connects the driver to cluster: it detects the release, resolves the
// zone the gateway serves from the root pools, reads the options, creates
// the control objects in the zone's control pool, and logs the zone and the
// options that ask for a worker it does not run. The control watches, which
// keep the metadata cache coherent with every other gateway, run as Run's
// control-watch worker.
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
	sys, err := openSysObj(ctx, pools, zc.Params, opts, release)
	if err != nil {
		return nil, errors.Join(err, pools.closeAll())
	}
	zc.log(ctx)
	opts.logWorkersNotRun(ctx)
	s := &Store{cluster: cluster, conf: conf, opts: opts, release: release, zone: zc, pools: pools, sysobj: sys}
	s.AddWorker("control-watch", sys.notify.run)
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

// Close closes the pools the Store opened. The workers stop with Run's
// context, not here.
func (s *Store) Close() error { return s.pools.closeAll() }

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
		Zone:      s,
		Users:     s,
		Accounts:  s,
		Buckets:   s,
		Objects:   s,
		Multipart: s,
		Stats:     s,
		Usage:     s,
		Metadata:  s,
		Conf:      s.conf,
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

// GetUser implements op.UserStore.
func (s *Store) GetUser(context.Context, meta.UserID) (*op.UserRecord, error) {
	return nil, op.ErrNotImplemented
}

// GetUserByAccessKey implements op.UserStore.
func (s *Store) GetUserByAccessKey(context.Context, string) (*op.UserRecord, error) {
	return nil, op.ErrNotImplemented
}

// GetUserByEmail implements op.UserStore.
func (s *Store) GetUserByEmail(context.Context, string) (*op.UserRecord, error) {
	return nil, op.ErrNotImplemented
}

// PutUser implements op.UserStore.
func (s *Store) PutUser(context.Context, *op.UserRecord, op.PutUserOptions) error {
	return op.ErrNotImplemented
}

// RemoveUser implements op.UserStore.
func (s *Store) RemoveUser(context.Context, *op.UserRecord) error {
	return op.ErrNotImplemented
}

// ListUserBuckets implements op.UserStore.
func (s *Store) ListUserBuckets(context.Context, meta.Owner, string, int) (ents []meta.BucketEnt, next string, more bool, err error) {
	return nil, "", false, op.ErrNotImplemented
}

// GetAccount implements op.AccountStore.
func (s *Store) GetAccount(context.Context, string) (*op.AccountRecord, error) {
	return nil, op.ErrNotImplemented
}

// AccountName implements op.AccountStore.
func (s *Store) AccountName(context.Context, string) (string, error) {
	return "", op.ErrNotImplemented
}

// GetBucket implements op.BucketStore.
func (s *Store) GetBucket(context.Context, string, string) (*op.BucketRecord, error) {
	return nil, op.ErrNotImplemented
}

// GetBucketInstance implements op.BucketStore.
func (s *Store) GetBucketInstance(context.Context, meta.BucketID) (*op.BucketRecord, error) {
	return nil, op.ErrNotImplemented
}

// CreateBucket implements op.BucketStore.
func (s *Store) CreateBucket(context.Context, op.CreateBucketParams) (*op.BucketRecord, error) {
	return nil, op.ErrNotImplemented
}

// DeleteBucket implements op.BucketStore.
func (s *Store) DeleteBucket(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
}

// PutBucketInfo implements op.BucketStore.
func (s *Store) PutBucketInfo(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
}

// PutBucketAttrs implements op.BucketStore.
func (s *Store) PutBucketAttrs(context.Context, *op.BucketRecord, map[string][]byte, []string) error {
	return op.ErrNotImplemented
}

// ListObjects implements op.BucketStore.
func (s *Store) ListObjects(context.Context, *op.BucketRecord, op.ListObjectsParams) (op.ListObjectsResult, error) {
	return op.ListObjectsResult{}, op.ErrNotImplemented
}

// StatObject implements op.ObjectStore.
func (s *Store) StatObject(context.Context, *op.BucketRecord, meta.ObjKey) (*op.ObjectState, error) {
	return nil, op.ErrNotImplemented
}

// ReadObject implements op.ObjectStore.
func (s *Store) ReadObject(context.Context, *op.ObjectState, op.ByteRange, io.Writer) error {
	return op.ErrNotImplemented
}

// PutObject implements op.ObjectStore.
func (s *Store) PutObject(context.Context, *op.BucketRecord, meta.ObjKey, io.Reader, op.PutParams) (*op.PutResult, error) {
	return nil, op.ErrNotImplemented
}

// DeleteObject implements op.ObjectStore.
func (s *Store) DeleteObject(context.Context, *op.BucketRecord, meta.ObjKey, op.DeleteParams) error {
	return op.ErrNotImplemented
}

// CopyObject implements op.ObjectStore.
func (s *Store) CopyObject(context.Context, *op.ObjectState, *op.BucketRecord, meta.ObjKey, op.CopyParams) (*op.PutResult, error) {
	return nil, op.ErrNotImplemented
}

// SetObjectAttrs implements op.ObjectStore.
func (s *Store) SetObjectAttrs(context.Context, *op.ObjectState, map[string][]byte, []string) error {
	return op.ErrNotImplemented
}

// CreateUpload implements op.MultipartStore.
func (s *Store) CreateUpload(context.Context, *op.BucketRecord, meta.ObjKey, op.UploadParams) (*op.Upload, error) {
	return nil, op.ErrNotImplemented
}

// GetUpload implements op.MultipartStore.
func (s *Store) GetUpload(context.Context, *op.BucketRecord, meta.ObjKey, string) (*op.Upload, error) {
	return nil, op.ErrNotImplemented
}

// PutPart implements op.MultipartStore.
func (s *Store) PutPart(context.Context, *op.Upload, int, io.Reader, op.PutParams) (*op.PartResult, error) {
	return nil, op.ErrNotImplemented
}

// CopyPart implements op.MultipartStore.
func (s *Store) CopyPart(context.Context, *op.Upload, int, *op.ObjectState, op.ByteRange) (*op.PartResult, error) {
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

// BucketStats implements op.StatsStore.
func (s *Store) BucketStats(context.Context, *op.BucketRecord) (op.Stats, error) {
	return op.Stats{}, op.ErrNotImplemented
}

// UserStats implements op.StatsStore.
func (s *Store) UserStats(context.Context, meta.Owner) (op.Stats, error) {
	return op.Stats{}, op.ErrNotImplemented
}

// CheckQuota implements op.StatsStore.
func (s *Store) CheckQuota(context.Context, *op.BucketRecord, meta.Owner, int64, int64) error {
	return op.ErrNotImplemented
}

// Log implements op.UsageLogger. Having no usage log to write to yet, it
// drops the entry.
func (s *Store) Log(context.Context, op.UsageEntry) {}

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
