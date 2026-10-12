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
// data pass (driver/rados/rgw_sal_rados.cc:470-595 at v19.2.6, :491-616 at
// v20.2.4; a bare line number below is v19.2.6's, v20.2.4's lying 21 lines
// on): the index shard headers read as read_stats reads them, the in-flight
// uploads aborted, then every listed object's tail stripes released directly
// and its head and index entry removed, instead of queueing the tails for
// GC. The bucket read it starts with is the caller's, and the bucket removal
// it ends with the ordinary purge the caller runs next (:601).
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
	// read_stats over the current index (:487-490). radosgw names the shard
	// objects from num_shards whatever the layout, so a bucket without index
	// objects fails here with ENOENT.
	if _, err := s.readShardHeaders(ctx, &rec.Info); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	// abort_multiparts (:492-495): the ordinary abort, whose parts the
	// garbage collector takes under the upload id.
	if _, err := s.abortMultiparts(ctx, rec); err != nil {
		return err
	}
	p := &bypassPurge{s: s, rec: rec, sem: semaphore.NewWeighted(int64(max(s.w.opts.gcMaxConcurrentIO, 1)))}
	// Every entry of the plain namespace, versions included, unordered
	// (:497-509).
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
		params.Marker = res.NextMarker
	}
	if err := p.stop(nil); err != nil {
		return err
	}
	// sync_owner_stats, whose failure radosgw means to log and goes on past
	// (:591-594).
	if _, err := s.syncOwnerStats(ctx, rec.Info.Owner, &rec.Info); err != nil {
		slog.WarnContext(ctx, "failed to sync owner stats before the bucket delete",
			slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// bypassPurge issues remove_bypass_gc's deletes with at most
// rgw_gc_max_concurrent_io of them in flight across tails and heads,
// floored at one. The garbage collector reads the same option
// (rgw_gc.cc:371 at v19.2.6, :385 at v20.2.4) but issues a delete while no
// more than that many are in flight, so it runs one more (:384, v20.2.4
// :399). The first failed delete stops
// new ones, and the purge returns it once the deletes in flight have ended,
// as drain_aio returns a failed completion's result (rgw_sal_rados.cc:98-112
// at v19.2.6, :107-121 at v20.2.4).
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
// errno radosgw's drain returns (rgw_rest_bucket.cc:245-247).
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

// object is one listed entry (:510-582): its head read as get_obj_state
// reads it, without following an olh; a missing head is skipped
// (:522-525); with a manifest, every stripe but the head is released under
// the refcount tag, then the head and its index entry removed; without one,
// nothing is done here and the ordinary purge deletes the object.
func (p *bypassPurge) object(ctx context.Context, key meta.ObjKey) error {
	st, err := p.s.readHead(ctx, p.rec, key, false)
	if err != nil {
		return err
	}
	if !st.Exists {
		slog.InfoContext(ctx, "bypass-gc purge cannot find the object's head; skipping it",
			slog.String("bucket", p.rec.Info.Bucket.Name), slog.String("key", key.Name))
		return nil
	}
	if st.Manifest == nil {
		return nil
	}
	// The stripe that is the head object as the manifest's head placement
	// rule places it is skipped (:533-536, :552-556).
	chain, err := p.s.gcChain(st.Manifest, st.Manifest.HeadPlacementRule)
	if err != nil {
		return err
	}
	// The tail tag, else the object tag (:538-541), which get_obj_state fakes
	// for a head with a manifest and no idtag (rgw_rados.cc:6238-6245 at
	// v19.2.6, :6992-6999 at v20.2.4). A copy's refcount reference is held
	// under the copy's own tag, so the release drops this object's
	// reference, or the implicit one, and the stripe stays while another
	// object holds it (cls_refcount.cc:88-139).
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
// (rgw_gc.cc:676-686 at v19.2.6, :688-698 at v20.2.4): a refcount put under
// tag with the implicit reference, the locator set, allowed into a full
// pool's reserve, and ENOENT counted as done (:413-415, v20.2.4 :428-430).
// remove_bypass_gc's delete_tail_obj_aio sends the same put without
// full-try (rgw_rados.cc:10659-10685 at v19.2.6, :11596-11622 at v20.2.4).
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

// head is delete_obj_aio with keep_index_consistent (rgw_rados.cc:10687-10732
// at v19.2.6, :11624-11669 at v20.2.4): an index prepare DEL under a fresh
// tag, since a state get_obj_state read carries no write tag
// (UpdateIndex::prepare, :7087-7093); obj_remove without a kept prefix, a
// guard or flags; then delete_obj_index's unprepared complete_del
// (:6033-6050). A head already gone counts as removed, as a tail does,
// where radosgw's drain returns its ENOENT. A removal that fails otherwise
// than by ENOENT or a timeout cancels the prepare and keeps the entry, where
// radosgw completes the delete without waiting for the removal; a timed-out
// one leaves the pending entry for a listing's check, as an object delete
// does.
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
