package driver

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"

	"github.com/cespare/xxhash/v2"
	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// An overwritten object's tails go to the GC queue, as radosgw's
// complete_atomic_modification sends them (driver/rados/rgw_rados.cc and
// rgw_gc.cc; a bare line number below is v19.2.6's rgw_gc.cc, which v20.2.4
// keeps).

// gcSeed is RGWGC::seed (rgw_gc.h:27).
const gcSeed = 8675309

// shardsMod is rgw_shards_mod (rgw_tools.h:46-55 at v19.2.6, :63-72 at
// v20.2.4) for a positive shard count. Its hval parameter is an unsigned
// int, so the 64-bit XXH64 that RGWGC::tag_index passes reaches the modulo
// as its low 32 bits.
func shardsMod(h uint64, shards uint32) int {
	hv := uint32(h) //nolint:gosec // rgw_shards_mod(unsigned hval, ...) keeps the low 32 bits
	if shards <= 7877 {
		return int(hv % 7877 % shards)
	}
	return int(hv % rgwShardsMax % shards)
}

// gcShard is RGWGC::tag_index (:63-66): XXH64 of the tag's bytes, NUL
// included, under RGWGC::seed, reduced by rgw_shards_mod.
func (w *writer) gcShard(tag string) int {
	d := xxhash.NewWithSeed(gcSeed)
	_, _ = d.WriteString(tag) //nolint:errcheck // a Digest's writes never fail
	return shardsMod(d.Sum64(), w.opts.gcMaxObjs)
}

// gcChain is update_gc_chain (rgw_rados.cc:5409-5421; v20.2.4 :6132-6144):
// every stripe of m by its pool, oid and locator, but the one that is the
// head object placed by headRule. A stripe whose placement gives no pool
// keeps an empty one, as the iterator's get_raw_obj ignores obj_to_raw's
// failure (rgw_rados.cc:2460-2465).
func (s *Store) gcChain(m *meta.Manifest, headRule meta.PlacementRule) ([]rgw.GCObj, error) {
	stripes, err := m.Stripes()
	if err != nil {
		return nil, err
	}
	head := meta.Stripe{Obj: m.Obj}
	headPool, _ := s.dataPool(headRule, m.Obj.Bucket)
	var chain []rgw.GCObj
	for i := range stripes {
		st := &stripes[i]
		pool, _ := s.dataPool(st.Placement, st.Obj.Bucket)
		oid, loc := st.OID(), st.Locator()
		if pool == headPool && oid == head.OID() && loc == head.Locator() {
			continue
		}
		chain = append(chain, rgw.GCObj{Pool: pool.String(), Key: rgw.ObjKey{Name: oid}, Loc: loc})
	}
	return chain, nil
}

// gcObjSize is cls_rgw_obj::estimate_encoded_size (cls_rgw_types.h:1149-1157
// at v19.2.6, with rgw_obj_index_key's, rgw_obj_types.h:98-104), the bytes
// send_split_chain counts for one object of a chain.
func gcObjSize(o rgw.GCObj) int {
	return 6 + 4 + len(o.Pool) + 4 + len(o.Key.Name) + 4 + len(o.Loc) + 6 + 4 + len(o.Key.Name) + 4 + len(o.Key.Instance)
}

// gcBaseSize is cls_rgw_gc_set_entry_op::estimate_encoded_size
// (cls_rgw_ops.h:995-999, cls_rgw_types.h:1203-1211 and :1254-1260 at
// v19.2.6) with an empty chain: the request around the tag.
func gcBaseSize(tag string) int { return 6 + 4 + 6 + 4 + len(tag) + 8 + 6 + 4 }

// enqueueGC is complete_atomic_modification's GC half (rgw_rados.cc:5394-5405)
// through send_split_chain (:68-118): the chain is queued under tag in
// entries whose estimated encoding stays within rgw_max_chunk_size, and when
// an entry is refused, the objects it and the rest of the chain hold are
// deleted inline. It never fails the caller.
func (s *Store) enqueueGC(ctx context.Context, chain []rgw.GCObj, tag string) {
	limit := int(min(s.w.opts.chunkSize, uint64(1<<62))) //nolint:gosec // bounded above
	base := gcBaseSize(tag)
	total := base
	var batch []rgw.GCObj
	for i, o := range chain {
		size := gcObjSize(o)
		if len(batch) > 0 && total+size > limit {
			if err := s.sendGCChain(ctx, batch, tag); err != nil {
				slog.WarnContext(ctx, "gc enqueue failed; deleting the tails inline", slog.String("tag", tag), slog.Any("error", err))
				// The failed batch and the chain from the batch's last
				// object on, which send_split_chain's --it repeats (:89-96).
				s.deleteInline(ctx, append(slices.Clone(batch), chain[i-1:]...), tag)
				return
			}
			batch, total = batch[:0], base
		}
		batch = append(batch, o)
		total += size
	}
	if len(batch) == 0 {
		return
	}
	if err := s.sendGCChain(ctx, batch, tag); err != nil {
		slog.WarnContext(ctx, "gc enqueue failed; deleting the tails inline", slog.String("tag", tag), slog.Any("error", err))
		s.deleteInline(ctx, batch, tag)
	}
}

// sendGCChain is RGWGC::send_chain (:120-138): gc_log_enqueue2, a version
// check for a shard that moved to the rgw_gc queue and the enqueue, and on a
// shard that has not moved (ECANCELED) or refuses (EPERM), the omap-era
// gc_set_entry instead.
func (s *Store) sendGCChain(ctx context.Context, objs []rgw.GCObj, tag string) error {
	pool, err := s.pools.get(ctx, s.ZoneParams().GCPool)
	if err != nil {
		return err
	}
	oid := s.w.gcShards[s.w.gcShard(tag)]
	info := rgw.GCObjInfo{Tag: tag, Chain: objs}
	w := radosclient.NewWriteOp()
	version.Check(w, version.ObjVersion{Ver: 1}, version.CondEQ, s.release)
	gc.QueueEnqueue(w, s.w.opts.gcObjMinWait, info, s.release)
	_, err = pool.Write(ctx, oid, w, radosclient.OpFlagNone)
	if !errors.Is(err, radosclient.ErrCanceled) && !errors.Is(err, radosclient.ErrPermission) {
		return err
	}
	w = radosclient.NewWriteOp()
	rgw.GCSetEntry(w, s.w.opts.gcObjMinWait, info, s.release)
	_, err = pool.Write(ctx, oid, w, radosclient.OpFlagNone)
	return err
}

// deleteInline is delete_objs_inline (rgw_rados.cc:5432-5462; v20.2.4
// :6155-6194): refcount put(tag, implicit) under full-try on every object,
// rgw_multi_obj_del_max_aio at a time, failures logged. v19.2.6 sends them
// one at a time; the requests are the same.
func (s *Store) deleteInline(ctx context.Context, objs []rgw.GCObj, tag string) {
	sem := semaphore.NewWeighted(int64(s.w.opts.multiObjDelMaxAIO))
	var wg sync.WaitGroup
	for _, o := range objs {
		if err := sem.Acquire(ctx, 1); err != nil {
			slog.WarnContext(ctx, "inline tail delete abandoned; the tails are leaked", slog.String("tag", tag), slog.Any("error", err))
			break
		}
		wg.Go(func() {
			defer sem.Release(1)
			pool, err := s.pools.get(ctx, meta.ParsePool(o.Pool))
			if err != nil {
				slog.WarnContext(ctx, "inline tail delete failed", slog.String("pool", o.Pool), slog.String("oid", o.Key.Name), slog.Any("error", err))
				return
			}
			if o.Loc != "" {
				pool = pool.WithLocator(o.Loc)
			}
			w := radosclient.NewWriteOp()
			refcount.Put(w, tag, true, s.release)
			if _, err := pool.Write(ctx, o.Key.Name, w, radosclient.OpFlagFullTry); err != nil {
				slog.WarnContext(ctx, "inline tail delete failed", slog.String("pool", o.Pool), slog.String("oid", o.Key.Name), slog.Any("error", err))
			}
		})
	}
	wg.Wait()
}
