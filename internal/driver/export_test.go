package driver

import (
	"context"
	"hash"
	"io"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// CaptureLog is captureLog for the external specs.
var CaptureLog = captureLog

// ReadAccount is readAccount for the external specs.
var ReadAccount = (*Store).readAccount

// OwnerBucketsObj is ownerBucketsObj for the external specs: the pool and
// oid of an owner's bucket list.
func OwnerBucketsObj(s *Store, owner meta.Owner) (meta.Pool, string) {
	o := s.ownerBucketsObj(owner)
	return o.pool, o.oid
}

// ZoneForTest is the zone a Store from NewStoreForTest serves.
type ZoneForTest struct {
	Params    meta.ZoneParams
	ZoneGroup meta.ZoneGroup
}

// ReadConfigForTest is the read path's options for NewStoreForTest:
// rgw_max_chunk_size, rgw_get_obj_max_req_size and rgw_get_obj_window_size.
type ReadConfigForTest struct{ Chunk, MaxReq, Window uint64 }

// NewStoreForTest builds a Squid Store over cluster that serves zone with
// cfg, skipping Open's zone resolution and control objects, so a spec can
// drive the data path over a counterfeiter cluster.
func NewStoreForTest(cluster radosclient.Cluster, zone ZoneForTest, cfg ReadConfigForTest) *Store {
	s := &Store{
		cluster: cluster,
		release: denc.Squid,
		zone:    &zoneConfig{Params: zone.Params, ZoneGroup: zone.ZoneGroup},
		pools:   newPoolCache(cluster),
		readCfg: readConfig{chunk: cfg.Chunk, maxReq: cfg.MaxReq, window: cfg.Window},
		quota:   newStatsCaches(options{bucketQuotaCacheSize: 10000, bucketQuotaTTL: 600 * time.Second}, time.Now),
	}
	s.heads = s
	s.bgAIO = semaphore.NewWeighted(1)
	return s
}

// SetHeadStaterForTest makes h what s's listings check pending index entries
// against.
func SetHeadStaterForTest(s *Store, h HeadStater) { s.heads = h }

// HoldSuggestSlotsForTest takes every slot listing suggestions are sent
// under until release runs.
func HoldSuggestSlotsForTest(s *Store) (release func()) {
	n := int64(max(s.opts.bucketIndexMaxAIO, 1))
	if !s.bgAIO.TryAcquire(n) {
		panic("driver: a suggestion slot is busy")
	}
	return func() { s.bgAIO.Release(n) }
}

// DataPoolForTest is dataPool for the external specs.
func (s *Store) DataPoolForTest(rule meta.PlacementRule, bucket meta.BucketID) (meta.Pool, bool) {
	return s.dataPool(rule, bucket)
}

// WriteEntryPoint is writeEntryPoint for the external specs, without attrs
// and at the time now, under a tracker that checks read and sets write when
// each has a Ver; it returns the version the tracker holds after the write.
func WriteEntryPoint(s *Store, ctx context.Context, ep meta.BucketEntryPoint, exclusive bool, read, write meta.ObjVersion) (meta.ObjVersion, error) {
	v := &objv{read: read, write: write}
	err := s.writeEntryPoint(ctx, ep, nil, exclusive, time.Time{}, v)
	return v.read, err
}

// RemoveEntryPoint is removeEntryPoint for the external specs, checking read
// when it has a Ver.
func RemoveEntryPoint(s *Store, ctx context.Context, tenant, name string, read meta.ObjVersion) error {
	var v *objv
	if read.Ver != 0 {
		v = &objv{read: read}
	}
	return s.removeEntryPoint(ctx, tenant, name, v)
}

// WriteInstance is writeInstance for the external specs, without attrs, at
// the time now and setting write.
func WriteInstance(s *Store, ctx context.Context, info meta.BucketInfo, exclusive bool, write meta.ObjVersion) error {
	return s.writeInstance(ctx, &info, nil, exclusive, time.Time{}, &objv{write: write})
}

// RemoveInstance is removeInstance for the external specs, unchecked.
func RemoveInstance(s *Store, ctx context.Context, b meta.BucketID) error {
	return s.removeInstance(ctx, b, nil)
}

// ReadEntryPoint is readEntryPoint for the external specs: the entry point
// with the attrs and the version it was read with.
func ReadEntryPoint(s *Store, ctx context.Context, tenant, name string) (meta.BucketEntryPoint, map[string][]byte, meta.ObjVersion, error) {
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return meta.BucketEntryPoint{}, nil, meta.ObjVersion{}, err
	}
	return e.ep, e.attrs, e.v.read, nil
}

// PooledBuffersForTest is the number of read buffers handed back to the pool
// and not taken out again; the pool may have dropped some of them since.
func (s *Store) PooledBuffersForTest() int64 { return s.pooledReadBufs.Load() }

// ReadStoredForTest is readStored for the external specs.
func (s *Store) ReadStoredForTest(ctx context.Context, st *op.ObjectState, ofs, n uint64, w io.Writer) error {
	return s.readStored(ctx, st, ofs, n, w)
}

// SetClock makes now the clock s stamps its metadata and cls_user writes
// with.
func SetClock(s *Store, now func() time.Time) { s.sysobj.now = now }

// LinkBucket is linkBucket for the external specs.
var LinkBucket = (*Store).linkBucket

// UnlinkBucket is unlinkBucket for the external specs.
var UnlinkBucket = (*Store).unlinkBucket

// SyncBucketOwnerStats is syncOwnerStats for the external specs.
var SyncBucketOwnerStats = (*Store).syncOwnerStats

// ReadOwnerStats is readOwnerStats for the external specs.
var ReadOwnerStats = (*Store).readOwnerStats

// CompleteOwnerStatsSync is completeOwnerStatsSync for the external specs.
var CompleteOwnerStatsSync = (*Store).completeOwnerStatsSync

// ReadShardHeaders is readShardHeaders for the external specs.
var ReadShardHeaders = (*Store).readShardHeaders

// ReadIndexStats is readIndexStats for the external specs.
var ReadIndexStats = (*Store).readIndexStats

// WriteOptions is writeOptions with its fields exported for the specs.
type WriteOptions struct {
	PutWindow, StripeSize, ChunkSize, MaxPutSize uint64
	GCMaxObjs, GCObjMinWait                      uint32
	GCProcessorMaxTime, GCProcessorPeriod        time.Duration
	GCMaxConcurrentIO, GCMaxTrimChunk            int
	GCMaxQueueSize, GCMaxDeferred                uint64
	GCThreads                                    bool
	MultiObjDelMaxAIO, CopyConcurrentIO          int
}

// WriteOptionsForTest is the write options Open read.
func (s *Store) WriteOptionsForTest() WriteOptions {
	o := s.w.opts
	return WriteOptions{
		PutWindow: o.putWindow, StripeSize: o.stripeSize, ChunkSize: o.chunkSize, MaxPutSize: o.maxPutSize,
		GCMaxObjs: o.gcMaxObjs, GCObjMinWait: o.gcObjMinWait,
		GCProcessorMaxTime: o.gcProcessorMaxTime, GCProcessorPeriod: o.gcProcessorPeriod,
		GCMaxConcurrentIO: o.gcMaxConcurrentIO, GCMaxTrimChunk: o.gcMaxTrimChunk,
		GCMaxQueueSize: o.gcMaxQueueSize, GCMaxDeferred: o.gcMaxDeferred, GCThreads: o.gcThreads,
		MultiObjDelMaxAIO: o.multiObjDelMaxAIO, CopyConcurrentIO: o.copyConcurrentIO,
	}
}

// RandAlnumForTest is randAlnum for the external specs.
func RandAlnumForTest(n int) string { return randAlnum(n) }

// SetRandForTest makes f the source of the random tags s generates.
func (s *Store) SetRandForTest(f func(int) string) { s.w.rand = f }

// SetReshardWaitForTest sets the pause between two reads of a resharding
// shard's status.
func (s *Store) SetReshardWaitForTest(d time.Duration) { s.w.reshardWait = d }

// PendingCompletionsForTest is the number of bucket index completions
// submitted or queued and not yet done.
func (s *Store) PendingCompletionsForTest() int { return s.w.completions.pending() }

// IndexOp is indexOp for the external specs.
type IndexOp = indexOp

// NewIndexOpForTest is newIndexOp for the external specs.
func (s *Store) NewIndexOpForTest(rec *op.BucketRecord, key meta.ObjKey, tag string) *IndexOp {
	return s.newIndexOp(rec, key, tag)
}

// Prepare is prepare for the external specs.
func (x *IndexOp) Prepare(ctx context.Context, mod rgw.ModifyOp) error { return x.prepare(ctx, mod) }

// Complete is complete for the external specs.
func (x *IndexOp) Complete(ver rgw.EntryVer, m rgw.DirEntryMeta) { x.complete(ver, m) }

// CompleteDel is completeDel for the external specs.
func (x *IndexOp) CompleteDel(ver rgw.EntryVer, removedMtime time.Time) {
	x.completeDel(ver, removedMtime)
}

// Cancel is cancel for the external specs.
func (x *IndexOp) Cancel() { x.cancel() }

// PreparedForTest reports whether a prepare of x succeeded.
func (x *IndexOp) PreparedForTest() bool { return x.prepared }

// GCShardForTest is gcShard for the external specs.
func GCShardForTest(s *Store, tag string) int { return s.w.gcShard(tag) }

// Layout is layout with its fields exported for the specs.
type Layout struct {
	HeadPool, TailPool     radosclient.Pool
	HeadRule, TailRule     meta.PlacementRule
	Chunk, Stripe, MaxHead uint64
}

// PlanPutForTest is planPut for the external specs.
func (s *Store) PlanPutForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, storageClass string) (Layout, error) {
	l, err := s.planPut(ctx, rec, key, storageClass)
	return Layout{
		HeadPool: l.headPool, TailPool: l.tailPool, HeadRule: l.headRule, TailRule: l.tailRule,
		Chunk: l.chunk, Stripe: l.stripe, MaxHead: l.maxHead,
	}, err
}

// TailWriter is a tailWriter over the manifest a PUT of a key builds, for
// the external specs.
type TailWriter struct {
	tw *tailWriter
	m  *meta.Manifest
}

// NewTailWriterForTest plans a PUT of key under storageClass and returns
// its tail writer, with the prefix "." plus 31 characters of s's rand and
// "_".
func (s *Store) NewTailWriterForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, storageClass string) (*TailWriter, error) {
	l, err := s.planPut(ctx, rec, key, storageClass)
	if err != nil {
		return nil, err
	}
	m := meta.NewTrivialManifest(meta.Obj{Bucket: rec.Info.Bucket, Key: key}, l.headRule, l.tailRule, "."+s.w.rand(31)+"_", l.maxHead, l.stripe)
	return &TailWriter{tw: s.newTailWriter(&m, l), m: &m}, nil
}

// Consume is consume.
func (t *TailWriter) Consume(ctx context.Context, body io.Reader, size int64, h hash.Hash) (head []byte, n uint64, err error) {
	return t.tw.consume(ctx, body, size, h)
}

// Drain is drain.
func (t *TailWriter) Drain() error { return t.tw.drain() }

// Discard is discard.
func (t *TailWriter) Discard() { t.tw.discard() }

// Tails returns the oids of the tails the writer issued, in issue order.
func (t *TailWriter) Tails() []string {
	t.tw.mu.Lock()
	defer t.tw.mu.Unlock()
	oids := make([]string, 0, len(t.tw.stripes))
	for _, st := range t.tw.stripes {
		oids = append(oids, meta.Stripe{Obj: st.obj}.OID())
	}
	return oids
}

// PeakBuffered is the most bytes of tail buffers the writer held at once.
func (t *TailWriter) PeakBuffered() uint64 {
	t.tw.mu.Lock()
	defer t.tw.mu.Unlock()
	return t.tw.peak
}

// CancelWriteForTest is cancelWrite after a head write that failed with err
// under the conditions ifMatch and ifNoneMatch, through x.
func (s *Store) CancelWriteForTest(x *IndexOp, ifMatch, ifNoneMatch string, err error) (canceled bool, out error) {
	res, out := s.cancelWrite(&headWrite{ifMatch: ifMatch, ifNoneMatch: ifNoneMatch}, x, err)
	return res.canceled, out
}

// Ticker is ticker for the external specs' clocks.
type Ticker = ticker

// SetTickerForTest makes newTicker the source of the tickers s's workers
// run on.
func (s *Store) SetTickerForTest(newTicker func(time.Duration) Ticker) { s.newTicker = newTicker }

// ResetUsageLogForTest starts s's usage log over as radosgw's UsageLogger
// constructor does at now, in the time zone loc.
func (s *Store) ResetUsageLogForTest(now time.Time, loc *time.Location) {
	s.usage.reset(now, loc)
}

// SetUsageFinalFlushTimeoutForTest sets how long s's usage worker goes on
// flushing once its context has ended.
func (s *Store) SetUsageFinalFlushTimeoutForTest(d time.Duration) { s.usage.finalFlush = d }

// UsageOIDForTest is usageOID for the external specs.
func UsageOIDForTest(s *Store, user string, index uint32) string { return s.usageOID(user, index) }

// FlushUsageForTest is flushUsage for the external specs.
func (s *Store) FlushUsageForTest(ctx context.Context) error { return s.flushUsage(ctx) }

// RunUsageFlushForTest is runUsageFlush for the external specs.
func (s *Store) RunUsageFlushForTest(ctx context.Context) error { return s.runUsageFlush(ctx) }

// SetHashNameForTest sets x's index hash source.
func (x *IndexOp) SetHashNameForTest(name string) { x.hashName = name }

// SetRemoveObjsForTest sets the entries x's completion or cancel retires.
func (x *IndexOp) SetRemoveObjsForTest(keys []rgw.ObjKey) { x.removeObjs = keys }

// HeadWriteForTest is the headWrite fields the specs set.
type HeadWriteForTest struct {
	Key                  meta.ObjKey
	Tag                  string
	IfMatch, IfNoneMatch string
	Data                 []byte
	Attrs                map[string][]byte
	Create, NonAtomic    bool
	Pool                 radosclient.Pool
	OID, Loc             string
	Category             uint8
	CompleteMultipart    bool
	Size, AccountedSize  uint64
}

// WriteMetaForTest is writeMeta of hw in rec's bucket through x; it returns
// the head's epoch.
func (s *Store) WriteMetaForTest(ctx context.Context, rec *op.BucketRecord, hw HeadWriteForTest, x *IndexOp) (uint64, error) {
	res, err := s.writeMeta(ctx, &headWrite{
		rec: rec, key: hw.Key, tag: hw.Tag, ifMatch: hw.IfMatch, ifNoneMatch: hw.IfNoneMatch, data: hw.Data, attrs: hw.Attrs, create: hw.Create,
		nonAtomic: hw.NonAtomic, pool: hw.Pool, oid: hw.OID, loc: hw.Loc, category: hw.Category,
		completeMultipart: hw.CompleteMultipart, size: hw.Size, accountedSize: hw.AccountedSize,
	}, x)
	return res.epoch, err
}

// MetaRef is mpRef with its fields exported for the specs.
type MetaRef struct {
	Pool     radosclient.Pool
	OID, Loc string
	Key      meta.ObjKey
}

func exportRef(r mpRef) MetaRef { return MetaRef{Pool: r.pool, OID: r.oid, Loc: r.loc, Key: r.key} }

// MetaRefForTest is metaRef for the external specs.
func (s *Store) MetaRefForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (MetaRef, error) {
	r, err := s.metaRef(ctx, rec, key, uploadID)
	return exportRef(r), err
}

// MetaState is metaState with its fields exported for the specs.
type MetaState struct {
	Ref     MetaRef
	Size    uint64
	Mtime   time.Time
	Attrs   map[string][]byte
	Version version.ObjVersion
	Info    meta.MultipartUploadInfo
}

// ReadMetaForTest is readMeta of upload uploadID of key in rec's bucket.
func (s *Store) ReadMetaForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (*MetaState, error) {
	r, err := s.metaRef(ctx, rec, key, uploadID)
	if err != nil {
		return nil, err
	}
	st, err := s.readMeta(ctx, r)
	if err != nil {
		return nil, err
	}
	return &MetaState{Ref: exportRef(st.ref), Size: st.size, Mtime: st.mtime, Attrs: st.attrs, Version: st.version, Info: st.info}, nil
}

// Clock is a clock a spec moves by hand, with the tickers it drives.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// SetClockForTest makes c the clock s stamps its writes and times its stats
// caches with, and the source of its workers' tickers.
func SetClockForTest(s *Store, c Clock) {
	s.sysobj.now = c.Now
	s.newTicker = c.NewTicker
}

// WaitQuotaRefreshesForTest waits for the stats caches' background
// refreshes under way.
func WaitQuotaRefreshesForTest(s *Store) {
	s.quota.bucket.wait()
	s.quota.owner.wait()
}

// CachedBucketStatsForTest is the bucket cache's entry for rec, zero when it
// holds none.
func CachedBucketStatsForTest(s *Store, rec *op.BucketRecord) op.Stats {
	st, _ := s.quota.bucket.peek(bucketStatsKey(rec.Info.Bucket))
	return st
}

// CachedOwnerStatsForTest is the owner cache's entry for owner, zero when it
// holds none.
func CachedOwnerStatsForTest(s *Store, owner meta.Owner) op.Stats {
	st, _ := s.quota.owner.peek(ownerStatsKey(owner))
	return st
}

// BucketStatsKeyForTest names rec's bucket in ModifiedBucketsForTest.
func BucketStatsKeyForTest(rec *op.BucketRecord) string { return bucketName(rec.Info.Bucket) }

func bucketName(b meta.BucketID) string { return b.Tenant + "/" + b.Name + ":" + b.ID }

// ModifiedBucketsForTest is the modified-bucket set the bucket sync worker
// drains: each bucket's owner, by BucketStatsKeyForTest's name.
func ModifiedBucketsForTest(s *Store) map[string]meta.Owner {
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	out := map[string]meta.Owner{}
	for _, mb := range s.quota.modified {
		out[bucketName(mb.bucket)] = mb.owner
	}
	return out
}

// RunBucketsSyncForTest is runBucketsSync for the external specs.
func (s *Store) RunBucketsSyncForTest(ctx context.Context) error { return s.runBucketsSync(ctx) }

// RunUserSyncForTest is the user section's runOwnerSync.
func (s *Store) RunUserSyncForTest(ctx context.Context) error { return s.runOwnerSync(ctx, userOwners) }

// RunAccountSyncForTest is the account section's runOwnerSync.
func (s *Store) RunAccountSyncForTest(ctx context.Context) error {
	return s.runOwnerSync(ctx, accountOwners)
}

// SyncAllStatsForTest is syncAllStats for the external specs.
func (s *Store) SyncAllStatsForTest(ctx context.Context, owner meta.Owner) error {
	return s.syncAllStats(ctx, owner)
}
