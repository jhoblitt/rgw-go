package driver

import (
	"context"
	"math"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// bodyBudget is the byte budget that bounds the tail bytes of request bodies
// held in memory across every request. radosgw bounds only each writer's
// writes in flight, by rgw_put_obj_min_window_size (the throttle
// RadosStore::get_atomic_writer gives each writer,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2284 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2770 at v20.2.4),
// so its body memory grows with the PUTs it serves. A piece takes its share
// before it is read from the body and gives it back when its write ends, so
// nothing that holds a share waits on the budget, and it cannot deadlock. A
// share is clamped to the budget: a piece larger than the whole budget takes
// all of it and proceeds alone.
//
// A client that sends its body slowly holds its piece's share for as long as
// it takes: request_timeout_ms bounds each read, not the piece, as beast's
// timer does (StreamIO::recv_body,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_frontend.cc#L152-L175 at v19.2.6
// and v20.2.4). bodyBudgetSize keeps a piece for every request the cap
// admits, so stalled clients cannot hold back another PUT, which radosgw's
// slow clients do only by filling that cap: radosgw reads a chunk before its
// throttle (get_data, then process,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4391-L4413 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4623-L4645 at v20.2.4).
// The piece is sized from rgw_max_chunk_size and rgw_obj_stripe_size, not
// from the sizes planPut aligns them to. Only an erasure-coded pool without
// allow_ec_overwrites has an alignment, its stripe width
// (pg_pool_t::requires_aligned_append,
// https://github.com/ceph/ceph/blob/v19.2.6/src/osd/osd_types.h#L1754-L1757 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_types.h#L1791-L1794 at v20.2.4,
// as RGWRados::get_required_alignment asks it,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L659-L693 at v19.2.6
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L694-L728 at v20.2.4):
// on such a pool with a stripe width over 4 MiB a chunk or stripe exceeds
// its option, a slow client holds more than its piece's room, and stalled
// clients there can hold back another PUT.
//
// The head a PUT keeps for its head write takes no share: it is held for the
// whole request, so a request would hold it while its tail waited on the
// budget, and enough such requests would hold the whole budget waiting. A
// head is one aligned chunk, so on such a pool it too exceeds
// rgw_max_chunk_size.
type bodyBudget struct {
	sem  *semaphore.Weighted
	size int64

	mu         sync.Mutex
	held, peak int64
}

func newBodyBudget(size int64) *bodyBudget {
	return &bodyBudget{sem: semaphore.NewWeighted(size), size: size}
}

// bodyBudgetSize is the byte bound of cluster's in-flight limiter, which
// every piece is written through, raised to one piece for each request the
// gateway admits (Options.MaxConcurrentRequests): a request reads one piece
// at a time, a chunk of its tail or a part's first stripe, which takes the
// window and one byte when it runs past the window. A cluster without a
// limiter, or no request cap, leaves the budget unbounded.
func bodyBudgetSize(cluster radosclient.Cluster, o writeOptions) int64 {
	l, ok := cluster.(radosclient.InflightLimiter)
	if !ok || l.MaxInflightBytes() <= 0 || o.maxRequests <= 0 {
		return math.MaxInt64
	}
	first := min(o.stripeSize, o.putWindow)
	if o.stripeSize > o.putWindow {
		first++
	}
	piece := max(o.chunkSize, first)
	if uint64(o.maxRequests) > math.MaxInt64/piece {
		return math.MaxInt64
	}
	return max(l.MaxInflightBytes(), o.maxRequests*int64(piece)) //nolint:gosec // piece times the cap is within int64, checked above
}

// share is what a buffer of n bytes takes from the budget.
func (b *bodyBudget) share(n uint64) int64 {
	return int64(min(n, uint64(b.size))) //nolint:gosec // within [0, size]
}

// acquire takes the share of a buffer of n bytes, waiting on ctx, and
// returns it.
func (b *bodyBudget) acquire(ctx context.Context, n uint64) (int64, error) {
	w := b.share(n)
	if err := b.sem.Acquire(ctx, w); err != nil {
		return 0, err
	}
	b.mu.Lock()
	b.held += w
	b.peak = max(b.peak, b.held)
	b.mu.Unlock()
	return w, nil
}

// release gives back w that acquire took.
func (b *bodyBudget) release(w int64) {
	if w == 0 {
		return
	}
	b.mu.Lock()
	b.held -= w
	b.mu.Unlock()
	b.sem.Release(w)
}
