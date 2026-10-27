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
// a completion or a cancel after it applies or drops it.

// reshardRetries is NUM_RESHARD_RETRIES
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7030)
// and block_while_resharding's num_retries
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7818),
// the same at v20.2.4.
const reshardRetries = 10

// indexShard is RGWRados::BucketShard: one index shard object, the pool
// handle it is in, and its shard id, -1 for an unsharded index.
type indexShard struct {
	pool radosclient.Pool
	oid  string
	id   int32
}

// indexShardFor is BucketShard::init through
// RGWSI_BucketIndex_RADOS::open_bucket_index_shard
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bi_rados.cc#L275-L300
// and get_bucket_index_object,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bi_rados.cc#L240-L273, at v19.2.6):
// the shard of info's current index generation that holds key. The key hashes
// by its name, rgw_obj::get_hash_object. A bucket without an id is
// open_bucket_index_base's EIO
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bi_rados.cc#L83-L86),
// and a hash type other than Mod its ENOTSUP; no row of the S3 error table
// names either, so both answer UnknownError.
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
	// second write pass does not send another (_do_write_meta,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3289-L3294).
	prepared bool
	// hashName replaces the key's name as the shard's hash source,
	// rgw_obj::index_hash_source
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_obj_types.h#L540-L541):
	// the multipart meta object and the part heads are indexed on their
	// upload key's shard.
	hashName string
	// unraced marks an entry no OLH call reads or writes, whose completions
	// join no wait set (completionManager.awaitSent).
	unraced bool
	// removeObjs are the entries the completion or cancel closing this op
	// retires in the same class call, rgw_cls_obj_complete_op::remove_objs.
	removeObjs []rgw.ObjKey
	// noLog is a write_meta caller's log_op false, which keeps the change
	// out of the bucket index log whatever the zone's log_data: what
	// RadosMultipartUpload::init passes for the meta object
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3323;
	// v20.2.4
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4169).
	noLog bool
	// bilogFlags are the RGWBILogFlags of the op, VERSIONED_OP for a
	// versioned write's, as _do_write_meta sets them
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3317-L3319 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3438-L3440 at v20.2.4).
	// Every completion and cancel carries them, and so does Squid's prepare;
	// the class reads them only when it logs the change to the bucket index
	// log.
	bilogFlags uint16
}

// newIndexOp starts the index change of key in rec's bucket under tag, or,
// when tag is empty, under the random tag UpdateIndex::prepare makes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7086-L7093).
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
		sh, err := x.s.indexShardFor(ctx, &x.rec.Info, meta.ObjKey{Name: x.shardName()})
		if err != nil {
			return indexShard{}, err
		}
		x.shard = &sh
	}
	return *x.shard, nil
}

// shardName is the name the op's shard is found from.
func (x *indexOp) shardName() string { return cmp.Or(x.hashName, x.obj.Key.Name) }

// waitKey is the wait set of the op's entry (completionManager.awaitSent).
func (x *indexOp) waitKey() waitKey {
	return waitKey{bucket: x.obj.Bucket.Key(), name: x.shardName()}
}

// clsKey is the object's index key, rgw_obj_key::get_index_key.
func (x *indexOp) clsKey() rgw.ObjKey {
	return rgw.ObjKey{Name: x.obj.Key.IndexKeyName(), Instance: x.obj.Key.Instance}
}

// zonesTrace is the zone set a prepare and a completion carry:
// zones_trace.insert(zone id, bucket.get_key())
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9468,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9505).
func (x *indexOp) zonesTrace() []string {
	return []string{x.s.w.zoneID + ":" + x.obj.Bucket.Key()}
}

// prepareOp is cls_obj_prepare_op's write op
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9456-L9479;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10395-L10411).
// Tentacle's cls_rgw_bucket_prepare_op takes no log_op, bilog flags or
// zones_trace
// (https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L164-L175 at v20.2.4).
func (x *indexOp) prepareOp(mod rgw.ModifyOp) *radosclient.WriteOp {
	p := rgw.PrepareOp{Op: mod, Key: x.clsKey(), Tag: x.tag, Locator: x.obj.Key.Locator()}
	if x.s.release < denc.Tentacle {
		p.LogOp = x.logOp()
		p.BILogFlags = x.bilogFlags
		p.ZonesTrace = x.zonesTrace()
	}
	return x.guardedOp(func(w *radosclient.WriteOp) { rgw.BucketPrepareOp(w, p, x.s.release) })
}

// completeOp is cls_obj_complete_op's write op
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9481-L9523;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10413-L10455).
func (x *indexOp) completeOp(c rgw.CompleteOp) *radosclient.WriteOp {
	return x.guardedOp(func(w *radosclient.WriteOp) { rgw.BucketCompleteOp(w, c, x.s.release) })
}

// guardedOp is the write op every index shard write of radosgw's takes: the
// shard must exist, assert_exists, and must not be resharding, the guard
// answering -ERR_BUSY_RESHARDING, ahead of the class call that call adds.
func (x *indexOp) guardedOp(call func(w *radosclient.WriteOp)) *radosclient.WriteOp {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.GuardBucketResharding(w, x.s.release)
	call(w)
	return w
}

// guardedWrite sends call's class call in a guardedOp to the current shard
// inside guardReshard, the shape of the OLH index calls RGWRados makes
// through its own guard_reshard (v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7912-L7966,
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8575-L8629).
// That guard finds the shard from the bucket instance on every try; this one
// keeps the shard until a busy answer's wait or a missing shard reads the
// instance again. Each send follows the completions submitted for its name
// before it, and the retries of those a reshard refused
// (completionManager.awaitSent).
func (x *indexOp) guardedWrite(ctx context.Context, call func(w *radosclient.WriteOp)) error {
	return x.guardReshard(ctx, func(sh indexShard) error {
		if err := x.s.w.completions.awaitSent(ctx, x.waitKey()); err != nil {
			return err
		}
		_, err := sh.pool.Write(ctx, sh.oid, x.guardedOp(call), radosclient.OpFlagNone)
		return err
	})
}

// completeSync is complete's ADD completion, sent and awaited: complete's
// request, locator included, on the reshard-guarded path the completion
// manager's resend takes without it, whose answer, the class's or the
// guard's, it returns where complete would leave a failure for listing to
// reconcile. An error does not prove the completion did not land: a context
// that ends, or an OSD that times out, while the op is in flight leaves it to
// land later. So after any error the caller must not link, as a completion
// landing after the link is what the wait prevents. A versioned write awaits
// it before its link, which reads the instance entry the completion writes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1670 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1811 at v20.2.4):
// radosgw's unawaited completion reaches the shard first because librados
// keeps one client's ops to one object in order, and rgw-go's completions
// keep no order (docs/cgo-limitations.md, "Bucket index completions to one
// shard can land out of order"; docs/exclusions.md, "A versioned write
// awaits its index completion").
func (x *indexOp) completeSync(ctx context.Context, ver rgw.EntryVer, m rgw.DirEntryMeta) error {
	if x.blind {
		return nil
	}
	c := x.completion(rgw.OpAdd, ver, m)
	return x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.BucketCompleteOp(w, c, x.s.release) })
}

// completeDelSync is completeDel's request sent and awaited as completeSync
// sends its own. An OLH replay's removal of a version awaits it before the
// replay's head op and its trim or clear: the clear removes the plain entry
// through which the class reads the null version's instance entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 at v20.2.4),
// and radosgw's unawaited completion lands first only because librados
// keeps one client's ops to one object in order (docs/cgo-limitations.md,
// "Bucket index completions to one shard can land out of order";
// docs/exclusions.md, "An OLH replay awaits the index completion of each
// version it removes").
func (x *indexOp) completeDelSync(ctx context.Context, ver rgw.EntryVer, removedMtime time.Time) error {
	if x.blind {
		return nil
	}
	c := x.completion(rgw.OpDel, ver, rgw.DirEntryMeta{Category: rgw.CategoryNone, Mtime: removedMtime})
	return x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.BucketCompleteOp(w, c, x.s.release) })
}

// readShard runs read, a read without the reshard guard as radosgw's index
// reads are, on the current shard. A shard object a reshard removed answers
// ENOENT as a missing entry does, so an ENOENT reads the bucket instance
// again and, when it puts the name on another shard, reads there, within
// reshardRetries tries: only an ENOENT from the shard the bucket now names
// is read's answer. radosgw's read_olh_log finds the shard from the cached
// instance once per call (v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8237-L8243,
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8983-L8989)
// and its bi_get from the bucket info its caller holds (v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9490-L9495,
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10239-L10244),
// and neither reads again (docs/exclusions.md, "An index read of a versioned
// key that finds its shard gone reads the shard the bucket now names"). Each
// read follows the completions submitted before it as a guardedWrite does.
func (x *indexOp) readShard(ctx context.Context, read func(sh indexShard) error) error {
	var err error
	for i := range reshardRetries {
		sh, serr := x.currentShard(ctx)
		if serr != nil {
			return serr
		}
		if werr := x.s.w.completions.awaitSent(ctx, x.waitKey()); werr != nil {
			return werr
		}
		err = read(sh)
		if !errors.Is(err, radosclient.ErrNotFound) || i == reshardRetries-1 {
			return err
		}
		if rerr := x.s.refreshIndexOp(ctx, x); rerr != nil {
			return rerr
		}
		next, nerr := x.currentShard(ctx)
		switch {
		case nerr != nil:
			return nerr
		case next.oid == sh.oid:
			return err
		}
		slog.DebugContext(ctx, "bucket index shard is gone; reading the shard the bucket now names",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("shard", sh.oid), slog.String("next", next.oid))
	}
	return err
}

// completion is the bucket_complete_op request of mod for the object.
func (x *indexOp) completion(mod rgw.ModifyOp, ver rgw.EntryVer, m rgw.DirEntryMeta) rgw.CompleteOp {
	return rgw.CompleteOp{
		Op: mod, Key: x.clsKey(), Locator: x.obj.Key.Locator(), Ver: ver, Meta: m, Tag: x.tag,
		LogOp: x.logOp(), BILogFlags: x.bilogFlags, ZonesTrace: x.zonesTrace(), RemoveObjs: x.removeObjs,
	}
}

// logOp is add_log: the caller's log_op and the zone's need_to_log_data
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7095,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7147,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7177,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7201).
func (x *indexOp) logOp() bool { return x.s.w.logData && !x.noLog }

// prepare is UpdateIndex::prepare
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7079-L7106;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7932-L7957):
// the prepare, behind the reshard guard, before the caller writes the head.
// Only a prepare that was sent and succeeded marks the op prepared
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7104;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7955);
// an indexless bucket's never is.
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

// complete is UpdateIndex::complete
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7108-L7156):
// the entry m describes, under the category m names, sent without
// waiting for it.
func (x *indexOp) complete(ver rgw.EntryVer, m rgw.DirEntryMeta) {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.completion(rgw.OpAdd, ver, m))
}

// completeDel is UpdateIndex::complete_del
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7158-L7188)
// through cls_obj_complete_del
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9536-L9551):
// a removal stamped with the removed object's mtime under category
// None, sent without waiting for it.
func (x *indexOp) completeDel(ver rgw.EntryVer, removedMtime time.Time) {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.completion(rgw.OpDel, ver, rgw.DirEntryMeta{Category: rgw.CategoryNone, Mtime: removedMtime}))
}

// cancel is UpdateIndex::cancel
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7190-L7218)
// through cls_obj_complete_cancel, which sends pool -1 and epoch 0
// whatever the object's state
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9553-L9563 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10485-L10495 at v20.2.4),
// sent without waiting for it. The class copies that version onto the
// entry before it writes the cancel back, so the next completion applies
// at any epoch (tracker #80894), as it does behind radosgw.
func (x *indexOp) cancel() {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.completion(rgw.OpCancel, rgw.EntryVer{Pool: -1, Epoch: 0}, rgw.DirEntryMeta{Category: rgw.CategoryNone}))
}

// guardReshard is UpdateIndex::guard_reshard
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7024-L7077;
// v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7210-L7269):
// call on the current shard, at most ten times. On a busy-resharding answer
// it waits the reshard out with blockWhileResharding and calls again. The
// count starts over when the wait ended with the instance moving the object
// to another shard; a shard that keeps refusing the guard while its status
// reads as finished uses up the ten calls and answers ErrBusyResharding. An
// ErrNotFound means that a reshard committed since the shard was found may
// have removed it: it reads the instance again, as v19.2.7 does (v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7225-L7230)
// and v20.2.4 does not, and calls again within the same ten only when the
// instance now puts the object on another shard. A re-read that names the
// same shard returns the ErrNotFound at once, where v19.2.7 sends the call
// ten times (docs/exclusions.md, "Dynamic resharding worker").
func (x *indexOp) guardReshard(ctx context.Context, call func(sh indexShard) error) error {
	var err error
	for i := 0; i < reshardRetries; i++ {
		sh, serr := x.currentShard(ctx)
		if serr != nil {
			return serr
		}
		err = call(sh)
		if errors.Is(err, radosclient.ErrNotFound) {
			// No call is left to use a re-read, so a failed one cannot
			// replace this answer; v19.2.7 leaves its loop here too
			// (v19.2.7
			// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7226-L7229,
			// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7258-L7261).
			if i == reshardRetries-1 {
				return err
			}
			if rerr := x.s.refreshIndexOp(ctx, x); rerr != nil {
				return rerr
			}
			next, nerr := x.currentShard(ctx)
			switch {
			case nerr != nil:
				return nerr
			case next.oid == sh.oid:
				return err
			}
			slog.DebugContext(ctx, "bucket index shard is gone; retrying on the shard the bucket now names",
				slog.String("bucket", x.obj.Bucket.Name), slog.String("shard", sh.oid), slog.String("next", next.oid))
			continue
		}
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
// waits: resharding_in_progress() on Squid
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7837),
// resharding() on Tentacle (v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8779;
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L800-L810).
func (s *Store) reshardBusy(e rgw.InstanceEntry) bool {
	if s.release < denc.Tentacle {
		return e.ReshardStatus == rgw.ReshardInProgress
	}
	return e.ReshardStatus != rgw.ReshardNone
}

// blockWhileResharding is RGWRados::block_while_resharding
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7773-L7935;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8715-L8873)
// without its reshard-lock recovery and the bucket re-read each failed
// lock attempt makes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7854-L7919;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8796-L8861;
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

// refreshIndexOp reads the bucket instance again into x and drops its shard,
// so that the next call finds the shard from the instance's current index
// generation, the one part of the instance a reshard changes. It is
// block_while_resharding's fetch_new_bucket_info after a wait, and after a
// missing shard v19.2.7's init_bs, which reads the instance into the
// target's bucket info (v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.h#L947-L955,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2804-L2830).
// The read skips the metadata cache that radosgw's reads go through, since
// the reshard's notify may not have reached it (docs/exclusions.md,
// "Dynamic resharding worker"). A bucket deleted meanwhile answers 404
// NoSuchKey, as radosgw does: its re-read fails with -ENOENT, which reaches
// the request unchanged
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7822-L7844,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7054-L7058,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7100-L7101,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3293-L3294;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8764-L8786,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7903-L7907,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7951-L7953,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3446-L3447;
// after a missing shard v19.2.7
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7218-L7222)
// and https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L97
// (v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L98) names
// NoSuchKey.
func (s *Store) refreshIndexOp(ctx context.Context, x *indexOp) error {
	o := s.instanceObj(x.rec.Info.Bucket)
	s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	rec, err := s.GetBucketInstance(ctx, x.rec.Info.Bucket)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return fmt.Errorf("%w: bucket %s is gone", op.ErrNoSuchKey, x.obj.Bucket.Name)
	case err != nil:
		return fmt.Errorf("reading bucket %s again for its index shard: %w", x.obj.Bucket.Name, err)
	}
	x.rec, x.shard = rec, nil
	return nil
}
