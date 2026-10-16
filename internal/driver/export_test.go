package driver

import (
	"context"
	"hash"
	"io"
	"strings"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/compression"
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

// SetSweepLimitForTest makes n the most stripes a listing's sweep of a
// head's multipart parts walks.
func (s *Store) SetSweepLimitForTest(n int) { s.sweepLimit = n }

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
	GCMaxObjs                                    uint32
	GCObjMinWait                                 int64
	GCProcessorMaxTime, GCProcessorPeriod        time.Duration
	GCMaxConcurrentIO, GCMaxTrimChunk            int
	GCMaxQueueSize, GCMaxDeferred                uint64
	GCThreads                                    bool
	MultiObjDelMaxAIO, CopyConcurrentIO          int
	Codec                                        compression.EncoderOptions
}

// WriteOptionsForTest is the write options Open read.
func (s *Store) WriteOptionsForTest() WriteOptions {
	o := s.w.opts
	return WriteOptions{
		PutWindow: o.putWindow, StripeSize: o.stripeSize, ChunkSize: o.chunkSize, MaxPutSize: o.maxPutSize,
		GCMaxObjs: o.gcMaxObjs, GCObjMinWait: o.gcObjMinWait,
		GCProcessorMaxTime: time.Duration(o.gcProcessorMaxTime) * time.Second, GCProcessorPeriod: time.Duration(o.gcProcessorPeriod) * time.Second,
		GCMaxConcurrentIO: o.gcMaxConcurrentIO, GCMaxTrimChunk: o.gcMaxTrimChunk,
		GCMaxQueueSize: o.gcMaxQueueSize, GCMaxDeferred: o.gcMaxDeferred, GCThreads: o.gcThreads,
		MultiObjDelMaxAIO: o.multiObjDelMaxAIO, CopyConcurrentIO: o.copyConcurrentIO, Codec: o.codec,
	}
}

// CompressesForTest reports whether a write into a placement of compression
// type typ, encrypted or not, compresses.
func (s *Store) CompressesForTest(ctx context.Context, typ string, encrypted bool) bool {
	return s.storeFor(ctx, newPlaintext(strings.NewReader(""), 0, s.w.opts.chunkSize), 0, typ, encrypted).comp != nil
}

// StoredStageForTest is the stored stage a client write of body, declared
// long, into a placement of compression type typ reads, and the bytes of block
// buffers its compressor holds.
func (s *Store) StoredStageForTest(ctx context.Context, body io.Reader, declared int64, typ string) (io.Reader, func() int) {
	st := s.storeFor(ctx, newPlaintext(body, s.w.opts.maxPutSize, s.w.opts.chunkSize), declared, typ, false)
	return st, func() int {
		if st.comp == nil {
			return 0
		}
		return cap(st.comp.src) + cap(st.comp.dst)
	}
}

// SetEncoderForTest makes f the source of the encoders s's writes compress
// with.
func (s *Store) SetEncoderForTest(f func(codec string, o compression.EncoderOptions) (compression.Encoder, error)) {
	s.w.newEncoder = f
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

// GCInitializeForTest is gcInitialize for the external specs.
func (s *Store) GCInitializeForTest(ctx context.Context) error { return s.gcInitialize(ctx) }

// GCWorker is gcWorker for the external specs.
type GCWorker = gcWorker

// GCWorkerForTest is the GC worker Open registers for s.
func (s *Store) GCWorkerForTest() *GCWorker { return newGCWorker(s) }

// SetNow makes now the clock g times its passes and budgets with.
func (g *GCWorker) SetNow(now func() time.Time) { g.now = now }

// SetSleep makes sleep how g waits between passes.
func (g *GCWorker) SetSleep(sleep func(ctx context.Context, d time.Duration) error) { g.sleep = sleep }

// Run is run for the external specs.
func (g *GCWorker) Run(ctx context.Context) error { return g.run(ctx) }

// Process is process for the external specs.
func (g *GCWorker) Process(ctx context.Context, expiredOnly bool) error {
	return g.process(ctx, expiredOnly)
}

// ProcessShardForTest is a pass of process over shard idx alone, with the
// budget given: processShard, then the drain unless ctx has ended.
func (g *GCWorker) ProcessShardForTest(ctx context.Context, idx int, budget time.Duration, expiredOnly bool) error {
	io := newGCIO(g)
	defer io.wait()
	if err := g.processShard(ctx, idx, budget, expiredOnly, io); err != nil {
		return err
	}
	if ctx.Err() == nil {
		io.drain(ctx)
	}
	return nil
}

// TransitionedForTest reports whether g has seen shard idx move to the
// rgw_gc queue.
func (g *GCWorker) TransitionedForTest(idx int) bool { return g.transitioned[idx] }

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

// Consume is consume of body's plaintext stage under rgw_max_put_size, with h
// its sink, stored as it is.
func (t *TailWriter) Consume(ctx context.Context, body io.Reader, size int64, h hash.Hash) (head []byte, n uint64, err error) {
	return t.tw.consume(ctx, storeAsIs(newPlaintext(body, t.tw.s.w.opts.maxPutSize, t.tw.s.w.opts.chunkSize, h)), size)
}

// SetIncompressibleForTest marks the writer's data as compressed, as a
// compressing write's stored stage does.
func (t *TailWriter) SetIncompressibleForTest() { t.tw.incompressible = true }

// PlaintextForTest is newPlaintext over body, held to limit in reads of
// chunk: the stage reader and a count of the bytes it has passed.
func PlaintextForTest(body io.Reader, limit, chunk uint64, sinks ...io.Writer) (io.Reader, func() uint64) {
	p := newPlaintext(body, limit, chunk, sinks...)
	return p, func() uint64 { return p.n }
}

// Drain is drain.
func (t *TailWriter) Drain() error { return t.tw.drain() }

// Discard is discard.
func (t *TailWriter) Discard() { t.tw.discard() }

// Buffered is the bytes of buffers the writer holds.
func (t *TailWriter) Buffered() uint64 {
	t.tw.mu.Lock()
	defer t.tw.mu.Unlock()
	return t.tw.held
}

// BodyBudget is the byte budget PUT bodies share: its size, the shares
// taken and not given back, and the most taken at once.
type BodyBudget struct{ Size, Held, Peak int64 }

// BodyBudgetForTest reports s's body byte budget.
func (s *Store) BodyBudgetForTest() BodyBudget {
	b := s.w.body
	b.mu.Lock()
	defer b.mu.Unlock()
	return BodyBudget{Size: b.size, Held: b.held, Peak: b.peak}
}

// SetBodyBudgetForTest gives s a body byte budget of n bytes.
func (s *Store) SetBodyBudgetForTest(n int64) { s.w.body = newBodyBudget(n) }

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

// UsageTrimRoundsForTest is usageTrimRounds for the external specs.
var UsageTrimRoundsForTest = usageTrimRounds

// SetUsageTrimCapFactorForTest makes n the factor of a shard trim's round
// cap until restore runs.
func SetUsageTrimCapFactorForTest(n int) (restore func()) {
	old := usageTrimCapFactor
	usageTrimCapFactor = n
	return func() { usageTrimCapFactor = old }
}

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
	Incompressible       bool
}

// WriteMetaForTest is writeMeta of hw in rec's bucket through x; it returns
// the head's epoch.
func (s *Store) WriteMetaForTest(ctx context.Context, rec *op.BucketRecord, hw HeadWriteForTest, x *IndexOp) (uint64, error) {
	res, err := s.writeMeta(ctx, &headWrite{
		rec: rec, key: hw.Key, tag: hw.Tag, ifMatch: hw.IfMatch, ifNoneMatch: hw.IfNoneMatch, data: hw.Data, attrs: hw.Attrs, create: hw.Create,
		nonAtomic: hw.NonAtomic, pool: hw.Pool, oid: hw.OID, loc: hw.Loc, category: hw.Category,
		completeMultipart: hw.CompleteMultipart, size: hw.Size, accountedSize: hw.AccountedSize, incompressible: hw.Incompressible,
	}, x)
	return res.epoch, err
}

// GuardHeadForTest is guardHead of hw's key and conditions over st, the head
// as read, adding the guard's steps to w.
func (s *Store) GuardHeadForTest(w *radosclient.WriteOp, hw HeadWriteForTest, st *op.ObjectState) error {
	return s.guardHead(w, &headWrite{key: hw.Key, ifMatch: hw.IfMatch, ifNoneMatch: hw.IfNoneMatch}, st)
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

// MPOptionsForTest is the multipart options Open read: the completion lock's
// duration and the minimum part size.
func (s *Store) MPOptionsForTest() (lockMaxTime time.Duration, minPartSize uint64) {
	return s.mp.lockMaxTime, s.mp.minPartSize
}

// NamesUploadObjectsForTest is namesUploadObjects over prefixes.
func NamesUploadObjectsForTest(m *meta.Manifest, prefixes ...string) bool {
	set := map[string]bool{}
	for _, p := range prefixes {
		set[p] = true
	}
	return namesUploadObjects(m, set)
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

// PartWriter is a part's tailWriter for the external specs, with the
// plaintext stage Consume read its body through.
type PartWriter struct {
	tw    *tailWriter
	plain *plaintext
}

// NewPartWriterForTest is newPartWriter for part n of m over l.
func (s *Store) NewPartWriterForTest(m *meta.Manifest, l Layout, n uint32) *PartWriter {
	return &PartWriter{tw: s.newPartWriter(m, layout{
		headPool: l.HeadPool, tailPool: l.TailPool, headRule: l.HeadRule, tailRule: l.TailRule,
		chunk: l.Chunk, stripe: l.Stripe, maxHead: l.MaxHead,
	}, n)}
}

// Consume is consume of a body of the declared size, -1 when unknown, through
// its plaintext stage under rgw_max_put_size with h its sink; it returns the
// body's length.
func (p *PartWriter) Consume(ctx context.Context, body io.Reader, size int64, h hash.Hash) (uint64, error) {
	p.plain = newPlaintext(body, p.tw.s.w.opts.maxPutSize, p.tw.s.w.opts.chunkSize, h)
	_, n, err := p.tw.consume(ctx, storeAsIs(p.plain), size)
	return n, err
}

// Resume is resume: p takes over prev's kept first stripe and streams the
// rest of body, counted on from what prev's Consume read, with h its sink.
func (p *PartWriter) Resume(ctx context.Context, prev *PartWriter, body io.Reader, h hash.Hash) (uint64, error) {
	p.plain = newPlaintext(body, p.tw.s.w.opts.maxPutSize, p.tw.s.w.opts.chunkSize, h)
	p.plain.n = prev.plain.n
	return p.tw.resume(ctx, prev.tw, storeAsIs(p.plain))
}

// SetIncompressibleForTest marks the writer's data as compressed.
func (p *PartWriter) SetIncompressibleForTest() { p.tw.incompressible = true }

// Drain is drain.
func (p *PartWriter) Drain() error { return p.tw.drain() }

// Discard is discard.
func (p *PartWriter) Discard() { p.tw.discard() }

// First is the first stripe the writer kept when its exclusive create met
// EEXIST, nil when it kept none.
func (p *PartWriter) First() []byte {
	if p.tw.first == nil {
		return nil
	}
	return (*p.tw.first.p)[:p.tw.first.n]
}

// PeakBuffered is the most bytes of buffers the writer held at once.
func (p *PartWriter) PeakBuffered() uint64 {
	p.tw.mu.Lock()
	defer p.tw.mu.Unlock()
	return p.tw.peak
}

// PartLayoutForTest is partLayout for the external specs: the layout and
// whether the part head goes through the bucket index when it is removed.
func (s *Store) PartLayoutForTest(ctx context.Context, rec *op.BucketRecord, dest meta.PlacementRule) (Layout, bool, error) {
	pl, err := s.partLayout(ctx, rec, dest)
	l := pl.layout
	return Layout{
		HeadPool: l.headPool, TailPool: l.tailPool, HeadRule: l.headRule, TailRule: l.tailRule,
		Chunk: l.chunk, Stripe: l.stripe, MaxHead: l.maxHead,
	}, pl.indexedHead, err
}

// AbortMultipartsForTest is abortMultiparts for the external specs.
func AbortMultipartsForTest(s *Store, ctx context.Context, rec *op.BucketRecord) (int, error) {
	return s.abortMultiparts(ctx, rec)
}

// CategoryName is categoryName for the external specs.
var CategoryName = categoryName

// ShardString is shardString for the external specs.
var ShardString = shardString

// SetHolderWalkPageForTest sets the page the key holder walk lists users
// in, restoring it when the spec ends.
func SetHolderWalkPageForTest(n int) (restore func()) {
	old := holderWalkPage
	holderWalkPage = n
	return func() { holderWalkPage = old }
}

// LinkBucketForTest is linkBucket at the clock's time.
func (s *Store) LinkBucketForTest(ctx context.Context, owner meta.Owner, b meta.BucketID) error {
	return s.linkBucket(ctx, owner, b, time.Time{})
}

// WriteInstanceAttrs creates the instance object of info with attrs, under
// a fresh version.
func WriteInstanceAttrs(s *Store, ctx context.Context, info meta.BucketInfo, attrs map[string][]byte) error {
	return s.writeInstance(ctx, &info, attrs, true, time.Time{}, &objv{write: newWriteVersion()})
}

// HoldClaimForTest marks b's bucket claim with owner as a metadata put does
// before its write, and returns the clear the put makes after it.
func HoldClaimForTest(s *Store, ctx context.Context, b meta.BucketID, owner meta.Owner) (func() error, error) {
	h, err := s.readClaim(ctx, b)
	if err != nil {
		return nil, err
	}
	if err := s.markClaim(ctx, h, owner); err != nil {
		return nil, err
	}
	return func() error { return s.clearClaim(ctx, h) }, nil
}
