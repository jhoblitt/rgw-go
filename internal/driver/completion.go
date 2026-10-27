package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// completion is one bucket_complete_op request and the index change it
// belongs to, a copy of the request's own so that the request and the
// completion never share it. k is the wait set of its entry, and retried
// its retry's flight once a reshard refused its first send, nil for one no
// OLH call waits for.
type completion struct {
	x       indexOp
	c       rgw.CompleteOp
	k       waitKey
	retried *flight
}

// completionID is what makes two completions a reshard refused the same
// request: the bucket's queue holds one of them (retryQueue).
type completionID struct {
	name    string
	key     rgw.ObjKey
	op      rgw.ModifyOp
	tag     string
	ver     rgw.EntryVer
	unraced bool
}

func (comp *completion) id() completionID {
	return completionID{name: comp.k.name, key: comp.c.Key, op: comp.c.Op, tag: comp.c.Tag, ver: comp.c.Ver, unraced: comp.x.unraced}
}

// waitKey names a wait set: the index entries of a bucket whose shard is
// found from one name (indexOp.waitKey). bucket is the bucket's key, which a
// reshard keeps, and a reshard moves an entry by that name, so a completion
// and an OLH call that share a wait set meet on one shard whatever the
// bucket's index layout.
type waitKey struct {
	bucket string
	name   string
}

// flight is a send of a completion that later index calls wait for: done
// closes once it has answered, and then, set before that under the
// manager's lock, is the retry it handed over to when a reshard refused it.
type flight struct {
	done chan struct{}
	then *flight
}

func newFlight() *flight { return &flight{done: make(chan struct{})} }

// completionManager is RGWIndexCompletionManager
// (driver/rados/rgw_rados.cc): bucket_complete_op writes sent off the
// request path, and the worker that sends again the ones a reshard refused.
// Each submitted write runs in its own goroutine, and each bucket's retries
// in one goroutine of the bucket's, under the manager's context, which Run's
// context ending cancels; the worker runs as the Store's index-completions
// worker.
type completionManager struct {
	s      *Store
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	wg     sync.WaitGroup

	mu       sync.Mutex
	closed   bool // run has stopped: nothing is sent any more
	inflight int  // submitted or being retried, and not yet done
	queued   int  // refused by a reshard, in a bucket's queue
	// retries holds, per bucket key, the completions a reshard refused until
	// the bucket's retrier takes them (retryBucket).
	retries map[string]*retryQueue
	// waits holds, per wait set, the flights an OLH call to it waits for:
	// each submitted completion's first send until it answers, and each
	// refused one's retry until it has answered or been abandoned
	// (awaitSent).
	waits map[waitKey]map[*flight]struct{}
}

// newCompletionManager returns the manager of s's completions, its context
// carrying ctx's values but not its cancellation.
func newCompletionManager(ctx context.Context, s *Store) *completionManager {
	mctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &completionManager{
		s: s, ctx: mctx, cancel: cancel, wake: make(chan struct{}, 1),
		retries: map[string]*retryQueue{}, waits: map[waitKey]map[*flight]struct{}{},
	}
}

// submit is cls_obj_complete_op's aio_operate and handle_completion
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9514-L9518,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L994-L1021):
// the write runs in its own goroutine and the request does not wait
// for it, though the OLH calls to its wait set after it wait for its first
// send to answer, and for its retry when a reshard refused that send
// (awaitSent). A busy-resharding answer queues the completion for its
// bucket's retrier (retryBucket); any other failure is logged and the
// completion dropped, leaving the pending entry for listing to reconcile.
func (m *completionManager) submit(x *indexOp, c rgw.CompleteOp) {
	comp := completion{x: *x, c: c, k: x.waitKey()}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		slog.WarnContext(m.ctx, "bucket index completion dropped: the completion manager has stopped",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Int("op", int(c.Op)))
		return
	}
	m.inflight++
	m.wg.Add(1)
	var sent *flight
	if !x.unraced {
		sent = newFlight()
		m.addWait(comp.k, sent)
	}
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		x := &comp.x
		sh, err := x.currentShard(m.ctx)
		if err == nil {
			_, err = sh.pool.Write(m.ctx, sh.oid, x.completeOp(c), radosclient.OpFlagNone)
		}
		busy := errors.Is(err, radosclient.ErrBusyResharding)
		m.mu.Lock()
		m.inflight--
		queued := busy && !m.closed
		var retried *flight
		if queued {
			retried = m.enqueue(comp)
		}
		if sent != nil {
			sent.then = retried
			m.answered(comp.k, sent)
		}
		m.mu.Unlock()
		if err != nil && !queued {
			slog.WarnContext(m.ctx, "bucket index completion failed; the pending entry is left for listing to reconcile",
				slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Int("op", int(c.Op)), slog.Any("error", err))
		}
	}()
}

// enqueue queues comp, which a reshard refused, for its bucket's retrier,
// and returns the retry flight a call waiting on its first send follows. A
// completion that is the same request as one still queued is not queued
// again, and hands over to that one's retry: a listing during a reshard
// sends the same DEL for a part on every pass (deleteObjIndex), and the
// queue so holds each distinct refused request once. m.mu is held.
func (m *completionManager) enqueue(comp completion) *flight {
	q := m.retries[comp.k.bucket]
	if q == nil {
		q = newRetryQueue()
		m.retries[comp.k.bucket] = q
	}
	id := comp.id()
	if f, ok := q.ids[id]; ok {
		return f
	}
	if !comp.x.unraced {
		comp.retried = newFlight()
		m.addWait(comp.k, comp.retried)
	}
	q.push(id, comp)
	m.queued++
	if !q.running {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	return comp.retried
}

// pending returns the completions submitted or queued and not yet done.
func (m *completionManager) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inflight + m.queued
}

// addWait adds f to k's wait set. m.mu is held.
func (m *completionManager) addWait(k waitKey, f *flight) {
	set := m.waits[k]
	if set == nil {
		set = map[*flight]struct{}{}
		m.waits[k] = set
	}
	set[f] = struct{}{}
}

// answered closes f, a flight of k's wait set that has answered or been
// abandoned, and drops it from the set. m.mu is held.
func (m *completionManager) answered(k waitKey, f *flight) {
	close(f.done)
	set := m.waits[k]
	delete(set, f)
	if len(set) == 0 {
		delete(m.waits, k)
	}
}

// awaitSent waits until every completion submitted to k's wait set before
// the call has had its first send answered, and the retry of each a reshard
// refused has answered, or ctx ends. librados applies the operations one
// client sends to one object in the order it sent them, so radosgw's
// unawaited completion reaches the shard before any later call of the same
// gateway there; rgw-go's runs in a goroutine of its own and keeps no order
// (docs/cgo-limitations.md, "Bucket index completions to one shard can land
// out of order"). The OLH calls, their reads included, wait so: a null
// delete marker's link takes the pending ops of the plain or null entry it
// replaces, and a completion landing after it clears its op from the null
// data key alone, leaving the marker pending for good
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1349-L1357 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484 at v20.2.4),
// and a read sent before the completion lands finds the entry without the
// meta the completion writes, which a versioned delete's conditions compare.
// Both concern the entries of the call's own name, so the call waits for
// those completions alone, wherever their shard. A reshard copies the plain
// entry with its pending op to the shard that follows, where the retry goes,
// which radosgw's retry thread does not order against the link
// (docs/ceph-upstream-bugs.md, "radosgw's listing repair of a null version's
// list entry overwrites or removes the key's version marker"). A send whose
// reshard refusal handed it over to its retry is followed into the retry,
// and the name's queued retries go ahead of the bucket's others (want). A
// completion the manager drops is waited for no longer and leaves its
// pending op, as radosgw's dropped one does.
func (m *completionManager) awaitSent(ctx context.Context, k waitKey) error {
	m.mu.Lock()
	waits := slices.Collect(maps.Keys(m.waits[k]))
	if len(waits) > 0 {
		m.want(k)
	}
	m.mu.Unlock()
	for _, f := range waits {
		for f != nil {
			select {
			case <-f.done:
			case <-ctx.Done():
				return fmt.Errorf("waiting for the bucket index completions of %s: %w", k.name, ctx.Err())
			}
			m.mu.Lock()
			if f = f.then; f != nil {
				m.want(k)
			}
			m.mu.Unlock()
		}
	}
	return nil
}

// want has k's queued retries sent before the rest of its bucket's queue.
// m.mu is held.
func (m *completionManager) want(k waitKey) {
	if q := m.retries[k.bucket]; q != nil {
		q.want(k.name)
	}
}

// run is RGWIndexCompletionManager::process
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L875-L938)
// as the Store's worker: it starts a retrier for each bucket with completions
// a reshard refused and none running (retryBucket). process sends every
// bucket's completions in turn from one thread, but an OLH call waits for
// the retries of its name's completions (awaitSent), so a retry waiting out
// one bucket's reshard would hold another bucket's calls behind it. When ctx
// ends, the writes in flight are canceled and awaited and the queued ones
// abandoned, as the manager's stop abandons them
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L804-L819).
func (m *completionManager) run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, m.cancel)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.closed = true
			for bucket, q := range m.retries {
				for _, nq := range q.byName {
					for i := range nq.comps {
						if f := nq.comps[i].retried; f != nil {
							m.answered(nq.comps[i].k, f)
						}
					}
				}
				q.clear()
				if !q.running {
					delete(m.retries, bucket)
				}
			}
			m.queued = 0
			m.mu.Unlock()
			m.cancel()
			m.wg.Wait()
			return nil
		case <-m.wake:
			m.mu.Lock()
			for bucket, q := range m.retries {
				if !q.running && q.n > 0 {
					q.running = true
					m.wg.Add(1)
					go m.retryBucket(bucket, q)
				}
			}
			m.mu.Unlock()
		}
	}
}

// retryBucket is process's loop over one bucket's completions, q: it takes
// them one at a time and sends each again (retry), until q is empty; a
// completion refused meanwhile joins q. The first waits the reshard out in
// its guard, polling the shard's status, while the rest wait in q, and then
// find the reshard over, so the bucket's completions cost one poller however
// many there are: a listing during a reshard queues one for every multipart
// part it sweeps (deleteObjIndex), and its reads take no reshard guard. q
// hands out first the completions of the names an OLH call waits on, so
// once the reshard is over such a call waits for the retry being sent, those
// of the names other calls began waiting on before it, and its own name's,
// not for the bucket's whole queue. One taken before Run's context ended,
// and not yet sent, is abandoned as the queued ones are.
func (m *completionManager) retryBucket(bucket string, q *retryQueue) {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		comp, ok := q.pop()
		if !ok {
			q.running = false
			if m.retries[bucket] == q {
				delete(m.retries, bucket)
			}
			m.mu.Unlock()
			return
		}
		m.queued--
		m.inflight++
		m.mu.Unlock()
		if m.ctx.Err() == nil {
			m.retry(&comp)
		}
		m.mu.Lock()
		m.inflight--
		if comp.retried != nil {
			m.answered(comp.k, comp.retried)
		}
		m.mu.Unlock()
	}
}

// retry is one pass of process's loop
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L890-L937):
// the bucket instance read again and the completion sent to the shard it
// names, under the reshard guard. Unlike the first attempt, process sends
// the request without the object locator: its cls_rgw_bucket_complete_op
// call leaves it at its default
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L918-L919).
// A failure is logged and the completion dropped.
func (m *completionManager) retry(comp *completion) {
	x := &comp.x
	rec, err := m.s.GetBucketInstance(m.ctx, x.rec.Info.Bucket)
	if err != nil {
		slog.ErrorContext(m.ctx, "bucket index completion dropped: the bucket instance cannot be read again",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("key", comp.c.Key.Name), slog.Any("error", err))
		return
	}
	x.rec, x.shard = rec, nil
	c := comp.c
	c.Locator = ""
	err = x.guardReshard(m.ctx, func(sh indexShard) error {
		_, werr := sh.pool.Write(m.ctx, sh.oid, x.completeOp(c), radosclient.OpFlagNone)
		return werr
	})
	if err != nil {
		slog.ErrorContext(m.ctx, "bucket index completion failed after a reshard; the pending entry is left for listing to reconcile",
			slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Any("error", err))
	}
}

// retryQueue is one bucket's completions a reshard refused, waiting for the
// bucket's retrier. Each name's completions go in the order the reshard
// refused them; the names an OLH call waits on go first, in the order the
// calls began waiting, and the rest in the order each name was queued. Each
// distinct request is queued once (enqueue).
type retryQueue struct {
	running bool // the bucket's retrier runs
	n       int  // completions queued
	byName  map[string]*nameQueue
	// order and wanted name the names to take from, a name whose queue has
	// since emptied being passed over.
	order  []string
	wanted []string
	// ids holds each queued completion, with its retry flight.
	ids map[completionID]*flight
}

// nameQueue is one name's completions in a retryQueue, and whether an OLH
// call waits on them.
type nameQueue struct {
	comps  []completion
	wanted bool
}

func newRetryQueue() *retryQueue {
	return &retryQueue{byName: map[string]*nameQueue{}, ids: map[completionID]*flight{}}
}

// push queues comp, whose id is id.
func (q *retryQueue) push(id completionID, comp completion) {
	nq := q.byName[id.name]
	if nq == nil {
		nq = &nameQueue{}
		q.byName[id.name] = nq
		q.order = append(q.order, id.name)
	}
	nq.comps = append(nq.comps, comp)
	q.ids[id] = comp.retried
	q.n++
}

// want moves name's queued completions ahead of the other names'.
func (q *retryQueue) want(name string) {
	if nq := q.byName[name]; nq != nil && !nq.wanted {
		nq.wanted = true
		q.wanted = append(q.wanted, name)
	}
}

// pop takes the next completion: the first of the name a call has waited on
// longest, else of the name queued longest.
func (q *retryQueue) pop() (completion, bool) {
	name, ok := q.next()
	if !ok {
		return completion{}, false
	}
	nq := q.byName[name]
	comp := nq.comps[0]
	nq.comps[0] = completion{}
	if nq.comps = nq.comps[1:]; len(nq.comps) == 0 {
		delete(q.byName, name)
	}
	delete(q.ids, comp.id())
	q.n--
	return comp, true
}

// next is the name pop takes from, passing over the names whose queue has
// emptied since they were listed.
func (q *retryQueue) next() (string, bool) {
	for len(q.wanted) > 0 {
		if nq := q.byName[q.wanted[0]]; nq != nil && nq.wanted {
			return q.wanted[0], true
		}
		q.wanted = q.wanted[1:]
	}
	for len(q.order) > 0 {
		if q.byName[q.order[0]] != nil {
			return q.order[0], true
		}
		q.order = q.order[1:]
	}
	return "", false
}

// clear empties q.
func (q *retryQueue) clear() {
	q.byName, q.order, q.wanted, q.ids, q.n = map[string]*nameQueue{}, nil, nil, map[completionID]*flight{}, 0
}
