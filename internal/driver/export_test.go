package driver

import (
	"context"
	"io"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
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
	return &Store{
		cluster: cluster,
		release: denc.Squid,
		zone:    &zoneConfig{Params: zone.Params, ZoneGroup: zone.ZoneGroup},
		pools:   newPoolCache(cluster),
		readCfg: readConfig{chunk: cfg.Chunk, maxReq: cfg.MaxReq, window: cfg.Window},
	}
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
