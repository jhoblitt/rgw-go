package driver

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// purgeListPage is remove_bypass_gc's listing_max_entries.
const purgeListPage = 1000

// PurgeBypassGC implements op.BucketAdminStore as RadosBucket::remove_bypass_gc's
// data pass
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L470-L595 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L491-L616 at v20.2.4;
// the remove_bypass_gc lines the v19.2.6 links below cite lie 21 lines on
// at v20.2.4): the index shard headers read as read_stats reads them, the
// in-flight uploads aborted, then every listed object's tail stripes released
// directly and its head and index entry removed, instead of queueing the
// tails for GC. The bucket read it starts with is the caller's, and the
// bucket removal it ends with the ordinary purge the caller runs next
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L601).
//
// The deletes run with at most rgw_gc_max_concurrent_io in flight, where
// the admin op's zero budget leaves the first object's tails in place and
// bounds nothing after (docs/ceph-upstream-bugs.md, "radosgw's admin API
// bypass-gc removal leaks a tail and runs unbounded"); a tail stripe or a
// head already gone counts as released, as the garbage collector counts it, where
// radosgw's drain fails on it (docs/ceph-upstream-bugs.md, "radosgw's
// bypass-gc bucket removal fails once a tail stripe is gone"); and an
// object's head goes only once all its tails are released, so a failed
// release leaves the object for a retry instead of orphaning its tails
// (docs/exclusions.md, "The admin bucket removal with bypass-gc").
func (s *Store) PurgeBypassGC(ctx context.Context, rec *op.BucketRecord) error {
	// read_stats over the current index
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L487-L490).
	// radosgw names the shard objects from num_shards whatever the layout,
	// so a bucket without index objects fails here with ENOENT.
	if _, err := s.readShardHeaders(ctx, &rec.Info); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	// abort_multiparts
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L492-L495):
	// the ordinary abort, whose parts the garbage collector takes
	// under the upload id.
	if _, err := s.abortMultiparts(ctx, rec); err != nil {
		return err
	}
	p := &bypassPurge{s: s, rec: rec, sem: semaphore.NewWeighted(int64(max(s.w.opts.gcMaxConcurrentIO, 1)))}
	// Every entry of the plain namespace, versions included, unordered
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L497-L509).
	// Paging by the next marker skips nothing here, though the pass deletes:
	// every entry whose head it removes stays in the index, marked gone under
	// the removal's pending tag as delete_obj_aio leaves it, so a marker
	// naming a version still resolves after that version, and one naming an
	// unversioned object starts after its name either way (markerStart).
	params := op.ListObjectsParams{ListVersions: true, AllowUnordered: true, MaxKeys: purgeListPage}
	for {
		res, err := s.ListObjects(ctx, rec, params)
		if err != nil {
			return p.stop(err)
		}
		for i := range res.Entries {
			if err := p.object(ctx, res.Entries[i].Key); err != nil {
				return p.stop(err)
			}
		}
		if !res.Truncated {
			break
		}
		params.Marker, params.MarkerInstance = res.NextMarker, res.NextMarkerInstance
	}
	if err := p.stop(nil); err != nil {
		return err
	}
	// sync_owner_stats, whose failure radosgw means to log and goes on past
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L591-L594).
	if _, err := s.syncOwnerStats(ctx, rec.Info.Owner, &rec.Info); err != nil {
		slog.WarnContext(ctx, "failed to sync owner stats before the bucket delete",
			slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// bypassPurge issues remove_bypass_gc's deletes with at most
// rgw_gc_max_concurrent_io of them in flight across tails and heads,
// floored at one. The garbage collector reads the same option
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L371 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L385 at v20.2.4)
// but issues a delete while no more than that many are in flight, so it runs
// one more
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L384,
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L399).
// The first failed delete stops new ones, and the purge returns it once the
// deletes in flight have ended, as drain_aio returns a failed completion's
// result
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L98-L112 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L107-L121 at v20.2.4).
type bypassPurge struct {
	s   *Store
	rec *op.BucketRecord
	sem *semaphore.Weighted
	wg  sync.WaitGroup // every object's head goroutine, each of which outlives its object's tail deletes
	mu  sync.Mutex
	err error
}

func (p *bypassPurge) failed() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// fail records the first failed delete, mapped as the admin op maps the
// errno radosgw's drain returns
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L245-L247).
func (p *bypassPurge) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = op.FromRADOS(err, op.ScopeBucket)
	}
}

// issue runs del in group once the budget has room, unless a delete has
// failed or ctx has ended. An issued delete runs to its end whatever ctx
// does, as an issued aio_operate does.
func (p *bypassPurge) issue(ctx context.Context, group *sync.WaitGroup, del func(context.Context) error) error {
	if err := p.failed(); err != nil {
		return err
	}
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	group.Go(func() {
		defer p.sem.Release(1)
		if err := del(ctx); err != nil {
			p.fail(err)
		}
	})
	return nil
}

// stop waits for every delete issued, then returns err, else the first
// failed delete.
func (p *bypassPurge) stop(err error) error {
	p.wg.Wait()
	if err != nil {
		return err
	}
	return p.failed()
}

// object is one listed entry
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L510-L582):
// its head read as get_obj_state reads it, without following an olh; a
// missing head, a delete marker's among them, is skipped
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L522-L525);
// with a manifest, every stripe but the head is released under the refcount
// tag, then the head and its index entry removed; without one, nothing is
// done here and the ordinary purge deletes the object.
//
// A version's head is its own, so it goes as radosgw's does: its instance
// entry is left marked gone under the removal's pending tag, and the
// ordinary purge's removal of the version by its instance unlinks it, its
// replay finding no head to remove or queue. A key's OLH head, which holds
// the null version's data and the key's OLH, is skipped, so the ordinary
// purge removes the null version and queues its tails for the garbage
// collector, where radosgw releases them and removes the OLH head object
// whole (docs/exclusions.md, "The admin bucket removal with bypass-gc
// leaves a versioned key's OLH head to the ordinary purge").
func (p *bypassPurge) object(ctx context.Context, key meta.ObjKey) error {
	st, err := p.s.readOLHHead(ctx, p.rec, key)
	if err != nil {
		return err
	}
	if !st.Exists {
		slog.InfoContext(ctx, "bypass-gc purge cannot find the object's head; skipping it",
			slog.String("bucket", p.rec.Info.Bucket.Name), slog.String("key", key.Name), slog.String("instance", key.Instance))
		return nil
	}
	if isOLHHead(st) || st.Manifest == nil {
		return nil
	}
	// The stripe that is the head object as the manifest's head placement
	// rule places it is skipped
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L533-L536,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L552-L556).
	chain, err := p.s.gcChain(st.Manifest, st.Manifest.HeadPlacementRule)
	if err != nil {
		return err
	}
	// The tail tag, else the object tag
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L538-L541),
	// which get_obj_state fakes for a head with a manifest and no idtag
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6238-L6245 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6992-L6999 at v20.2.4).
	// A copy's refcount reference is held under the copy's own tag, so the
	// release drops this object's reference, or the implicit one, and the
	// stripe stays while another object holds it
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/refcount/cls_refcount.cc#L88-L139).
	tag := rawTag(st, meta.AttrTailTag)
	if tag == "" {
		tag = rawTag(st, meta.AttrIDTag)
	}
	if tag == "" {
		tag = fakeTag(st)
	}
	var tails sync.WaitGroup
	for _, o := range chain {
		if err = p.issue(ctx, &tails, func(ctx context.Context) error { return p.tail(ctx, o, tag) }); err != nil {
			break
		}
	}
	// The head goes once its tails are released; issue refuses it after any
	// failed delete.
	p.wg.Go(func() {
		tails.Wait()
		var heads sync.WaitGroup
		if ierr := p.issue(ctx, &heads, func(ctx context.Context) error { return p.head(ctx, key, st) }); ierr != nil {
			p.fail(ierr)
		}
		heads.Wait()
	})
	return err
}

// tail is the garbage collector's delete of one chain object
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L676-L686 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L688-L698 at v20.2.4):
// a refcount put under tag with the implicit reference, the locator set,
// allowed into a full pool's reserve, and ENOENT counted as done
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L413-L415,
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L428-L430).
// remove_bypass_gc's delete_tail_obj_aio sends the same put without
// full-try
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10659-L10685 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11596-L11622 at v20.2.4).
func (p *bypassPurge) tail(ctx context.Context, o rgw.GCObj, tag string) error {
	pool, err := p.s.pools.get(ctx, meta.ParsePool(o.Pool))
	if err != nil {
		return err
	}
	if o.Loc != "" {
		pool = pool.WithLocator(o.Loc)
	}
	w := radosclient.NewWriteOp()
	refcount.Put(w, tag, true, p.s.release)
	if _, err := pool.Write(ctx, o.Key.Name, w, radosclient.OpFlagFullTry); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		return err
	}
	return nil
}

// head is delete_obj_aio with keep_index_consistent
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10687-L10732 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11624-L11669 at v20.2.4):
// an index prepare DEL under a fresh tag, since a state get_obj_state read
// carries no write tag (UpdateIndex::prepare,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7087-L7093);
// obj_remove without a kept prefix, a guard or flags; then delete_obj_index's
// unprepared complete_del
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6033-L6050).
// A head already gone counts as removed, as a tail does, where radosgw's
// drain returns its ENOENT. A removal that fails otherwise than by ENOENT or
// a timeout cancels the prepare and keeps the entry, where radosgw completes
// the delete without waiting for the removal; a timed-out one leaves the
// pending entry for a listing's check, as an object delete does.
func (p *bypassPurge) head(ctx context.Context, key meta.ObjKey, st *op.ObjectState) error {
	ref, err := p.s.headRef(ctx, p.rec, key)
	if err != nil {
		return err
	}
	x := p.s.newIndexOp(p.rec, key, "")
	if perr := x.prepare(ctx, rgw.OpDel); perr != nil {
		return perr
	}
	w := radosclient.NewWriteOp()
	rgw.ObjRemove(w, nil, p.s.release)
	_, err = ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		return err
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		p.s.deleteObjIndex(p.rec, key, st.Mtime)
		return nil
	default:
		x.cancel()
		return err
	}
}
