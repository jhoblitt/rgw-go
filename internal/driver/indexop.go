package driver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// An object write keeps the bucket index in step through
// RGWRados::Bucket::UpdateIndex (driver/rados/rgw_rados.cc): a prepare
// before the head is written records the change as pending under a tag, and
// a completion or a cancel after it applies or drops it. A bare line number
// is v19.2.6's rgw_rados.cc.

// reshardRetries is NUM_RESHARD_RETRIES (:7030) and block_while_resharding's
// num_retries (:7818), the same at v20.2.4.
const reshardRetries = 10

// indexShard is RGWRados::BucketShard: one index shard object, the pool
// handle it is in, and its shard id, -1 for an unsharded index.
type indexShard struct {
	pool radosclient.Pool
	oid  string
	id   int32
}

// indexShardFor is BucketShard::init through
// RGWSI_BucketIndex_RADOS::open_bucket_index_shard (svc_bi_rados.cc:275-300
// and get_bucket_index_object, :240-273, at v19.2.6): the shard of info's
// current index generation that holds key. The key hashes by its name,
// rgw_obj::get_hash_object. A bucket without an id is open_bucket_index_base's
// EIO (:83-86), and a hash type other than Mod its ENOTSUP; no row of the
// S3 error table names either, so both answer UnknownError.
func (s *Store) indexShardFor(ctx context.Context, info *meta.BucketInfo, key meta.ObjKey) (indexShard, error) {
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return indexShard{}, err
	}
	if info.Bucket.ID == "" {
		return indexShard{}, fmt.Errorf("%w: bucket %s has no bucket id", op.ErrUnknown, info.Bucket.Name)
	}
	gen := info.Layout.Current
	if gen.Layout.Normal.HashType != meta.HashMod {
		return indexShard{}, fmt.Errorf("%w: bucket %s index hash type %d", op.ErrUnknown, info.Bucket.Name, gen.Layout.Normal.HashType)
	}
	sh := indexShard{pool: pool, id: -1}
	shard, ok := meta.IndexShard(key.Name, gen.Layout.Normal.NumShards)
	if ok {
		sh.id = int32(shard) //nolint:gosec // a shard index is below the shard count, at most 65521
	}
	sh.oid = info.IndexShardOID(gen, shard)
	return sh, nil
}

// indexOp is RGWRados::Bucket::UpdateIndex for one object: the pending
// index change under one tag, prepared before the head write and completed
// or canceled after it. Each RADOS call carries the reshard guard.
type indexOp struct {
	s     *Store
	rec   *op.BucketRecord // the instance the shard was found from, replaced when a reshard ends
	obj   meta.Obj
	tag   string
	blind bool // an indexless bucket: every method does nothing
	shard *indexShard
	// prepared is UpdateIndex::is_prepared: the prepare succeeded, so a
	// second write pass does not send another (_do_write_meta, :3289-3294).
	prepared bool
	// hashName replaces the key's name as the shard's hash source,
	// rgw_obj::index_hash_source (rgw_obj_types.h:540-541): the multipart
	// meta object and the part heads are indexed on their upload key's
	// shard.
	hashName string
	// removeObjs are the entries the completion or cancel closing this op
	// retires in the same class call, rgw_cls_obj_complete_op::remove_objs.
	removeObjs []rgw.ObjKey
	// noLog is a write_meta caller's log_op false, which keeps the change
	// out of the bucket index log whatever the zone's log_data: what
	// RadosMultipartUpload::init passes for the meta object
	// (rgw_sal_rados.cc:3323; v20.2.4 :4169).
	noLog bool
}

// newIndexOp starts the index change of key in rec's bucket under tag, or,
// when tag is empty, under the random tag UpdateIndex::prepare makes
// (:7086-7093).
func (s *Store) newIndexOp(rec *op.BucketRecord, key meta.ObjKey, tag string) *indexOp {
	if tag == "" {
		tag = s.w.randTag()
	}
	return &indexOp{
		s:     s,
		rec:   rec,
		obj:   meta.Obj{Bucket: rec.Info.Bucket, Key: key},
		tag:   tag,
		blind: rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless,
	}
}

// currentShard is UpdateIndex::get_bucket_shard: the shard found from the
// current instance, kept until a reshard drops it.
func (x *indexOp) currentShard(ctx context.Context) (indexShard, error) {
	if x.shard == nil {
		sh, err := x.s.indexShardFor(ctx, &x.rec.Info, meta.ObjKey{Name: cmp.Or(x.hashName, x.obj.Key.Name)})
		if err != nil {
			return indexShard{}, err
		}
		x.shard = &sh
	}
	return *x.shard, nil
}

// clsKey is the object's index key, rgw_obj_key::get_index_key.
func (x *indexOp) clsKey() rgw.ObjKey {
	return rgw.ObjKey{Name: x.obj.Key.IndexKeyName(), Instance: x.obj.Key.Instance}
}

// zonesTrace is the zone set a prepare and a completion carry:
// zones_trace.insert(zone id, bucket.get_key()) (:9468, :9505).
func (x *indexOp) zonesTrace() []string {
	return []string{x.s.w.zoneID + ":" + x.obj.Bucket.Key()}
}

// prepareOp is cls_obj_prepare_op's write op (:9456-9479; v20.2.4
// :10395-10411). Tentacle's cls_rgw_bucket_prepare_op takes no log_op,
// bilog flags or zones_trace (cls_rgw_client.cc:164-175 at v20.2.4).
func (x *indexOp) prepareOp(mod rgw.ModifyOp) *radosclient.WriteOp {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.GuardBucketResharding(w, x.s.release)
	p := rgw.PrepareOp{Op: mod, Key: x.clsKey(), Tag: x.tag, Locator: x.obj.Key.Locator()}
	if x.s.release < denc.Tentacle {
		p.LogOp = x.logOp()
		p.ZonesTrace = x.zonesTrace()
	}
	rgw.BucketPrepareOp(w, p, x.s.release)
	return w
}

// completeOp is cls_obj_complete_op's write op (:9481-9523; v20.2.4
// :10413-10455).
func (x *indexOp) completeOp(c rgw.CompleteOp) *radosclient.WriteOp {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.GuardBucketResharding(w, x.s.release)
	rgw.BucketCompleteOp(w, c, x.s.release)
	return w
}

// completion is the bucket_complete_op request of mod for the object.
func (x *indexOp) completion(mod rgw.ModifyOp, ver rgw.EntryVer, m rgw.DirEntryMeta) rgw.CompleteOp {
	return rgw.CompleteOp{
		Op: mod, Key: x.clsKey(), Locator: x.obj.Key.Locator(), Ver: ver, Meta: m, Tag: x.tag,
		LogOp: x.logOp(), ZonesTrace: x.zonesTrace(), RemoveObjs: x.removeObjs,
	}
}

// logOp is add_log: the caller's log_op and the zone's need_to_log_data
// (:7095, :7147, :7177, :7201).
func (x *indexOp) logOp() bool { return x.s.w.logData && !x.noLog }

// prepare is UpdateIndex::prepare (:7079-7106; v20.2.4 :7932-7957): the
// prepare, behind the reshard guard, before the caller writes the head. Only
// a prepare that was sent and succeeded marks the op prepared (:7104;
// v20.2.4 :7955); an indexless bucket's never is.
func (x *indexOp) prepare(ctx context.Context, mod rgw.ModifyOp) error {
	if x.blind {
		return nil
	}
	err := x.guardReshard(ctx, func(sh indexShard) error {
		_, err := sh.pool.Write(ctx, sh.oid, x.prepareOp(mod), radosclient.OpFlagNone)
		return err
	})
	if err != nil {
		return err
	}
	x.prepared = true
	return nil
}

// complete is UpdateIndex::complete (:7108-7156): the entry m describes,
// under the category m names, sent without waiting for it.
func (x *indexOp) complete(ver rgw.EntryVer, m rgw.DirEntryMeta) {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.completion(rgw.OpAdd, ver, m))
}

// completeDel is UpdateIndex::complete_del (:7158-7188) through
// cls_obj_complete_del (:9536-9551): a removal stamped with the removed
// object's mtime under category None, sent without waiting for it.
func (x *indexOp) completeDel(ver rgw.EntryVer, removedMtime time.Time) {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.completion(rgw.OpDel, ver, rgw.DirEntryMeta{Category: rgw.CategoryNone, Mtime: removedMtime}))
}

// cancel is UpdateIndex::cancel (:7190-7218) through
// cls_obj_complete_cancel, which sends pool -1 and epoch 0 whatever the
// object's state (:9553-9563 at v19.2.6, :10485-10495 at v20.2.4), sent
// without waiting for it. The class copies that version onto the entry
// before it writes the cancel back, so the next completion applies at any
// epoch (tracker #80894), as it does behind radosgw.
func (x *indexOp) cancel() {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.completion(rgw.OpCancel, rgw.EntryVer{Pool: -1, Epoch: 0}, rgw.DirEntryMeta{Category: rgw.CategoryNone}))
}

// guardReshard is UpdateIndex::guard_reshard (:7024-7077): call on the
// current shard, and on a busy-resharding answer wait the reshard out with
// blockWhileResharding and call again, at most ten times. The count starts
// over when the wait ended with the instance moving the object to another
// shard; a shard that keeps refusing the guard while its status reads as
// finished uses up the ten calls and answers ErrBusyResharding.
func (x *indexOp) guardReshard(ctx context.Context, call func(sh indexShard) error) error {
	var err error
	for i := 0; i < reshardRetries; i++ {
		sh, serr := x.currentShard(ctx)
		if serr != nil {
			return serr
		}
		err = call(sh)
		if !errors.Is(err, radosclient.ErrBusyResharding) {
			return err
		}
		slog.DebugContext(ctx, "bucket index shard is resharding; waiting",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("shard", sh.oid))
		berr := x.s.blockWhileResharding(ctx, x, sh)
		switch {
		case errors.Is(berr, radosclient.ErrBusyResharding):
			continue
		case berr != nil:
			return berr
		}
		if moved, merr := x.currentShard(ctx); merr == nil && moved.oid != sh.oid {
			i = 0
		}
	}
	return err
}

// reshardBusy is how the release reads a shard's reshard status while it
// waits: resharding_in_progress() on Squid (:7837), resharding() on
// Tentacle (v20.2.4 :8779; cls_rgw_types.h:800-810).
func (s *Store) reshardBusy(e rgw.InstanceEntry) bool {
	if s.release < denc.Tentacle {
		return e.ReshardStatus == rgw.ReshardInProgress
	}
	return e.ReshardStatus != rgw.ReshardNone
}

// blockWhileResharding is RGWRados::block_while_resharding (:7773-7935;
// v20.2.4 :8715-8873) without its reshard-lock recovery and the bucket
// re-read each failed lock attempt makes (:7854-7919; v20.2.4 :8796-8861;
// docs/exclusions.md, "Dynamic resharding worker"): up to ten reads of the
// shard's reshard status, reshardWait apart. A status that is no longer
// busy, or a shard that is gone, re-reads the bucket instance into x and
// drops its shard, and returns nil; the tenth busy read returns
// ErrBusyResharding. A status that does not decode is radosgw's EIO, which
// answers UnknownError.
func (s *Store) blockWhileResharding(ctx context.Context, x *indexOp, sh indexShard) error {
	for i := 1; i <= reshardRetries; i++ {
		rop := radosclient.NewReadOp()
		res := rgw.GetBucketResharding(rop, s.release)
		_, err := sh.pool.Read(ctx, sh.oid, rop, radosclient.OpFlagNone)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return s.refreshIndexOp(ctx, x)
		case err != nil:
			return fmt.Errorf("reading the reshard status of index shard %s: %w", sh.oid, err)
		}
		entry, err := res.Result()
		if err != nil {
			return fmt.Errorf("%w: the reshard status of index shard %s: %w", op.ErrUnknown, sh.oid, err)
		}
		if !s.reshardBusy(entry) {
			return s.refreshIndexOp(ctx, x)
		}
		if i == reshardRetries {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.w.reshardWait):
		}
	}
	return radosclient.ErrBusyResharding
}

// refreshIndexOp is block_while_resharding's fetch_new_bucket_info: the
// bucket instance read again, since a reshard keeps the instance and moves
// only its index generation, and the shard found from it next. A bucket
// deleted while the write waited answers 404 NoSuchKey, as radosgw does:
// its re-read fails with -ENOENT, which reaches the request unchanged
// (:7822-7844, :7054-7058, :7100-7101, :3293-3294; v20.2.4 :8764-8786,
// :7903-7907, :7951-7953, :3446-3447) and rgw_common.cc:97 (v20.2.4 :98)
// names NoSuchKey.
func (s *Store) refreshIndexOp(ctx context.Context, x *indexOp) error {
	rec, err := s.GetBucketInstance(ctx, x.rec.Info.Bucket)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return fmt.Errorf("%w: bucket %s is gone after its reshard", op.ErrNoSuchKey, x.obj.Bucket.Name)
	case err != nil:
		return fmt.Errorf("reading bucket %s again after its reshard: %w", x.obj.Bucket.Name, err)
	}
	x.rec, x.shard = rec, nil
	return nil
}
