package goceph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"github.com/ceph/go-ceph/rados"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// maxIdle bounds the I/O contexts a pool keeps for reuse; one returned past
// it is destroyed once the operations submitted on it finish.
const maxIdle = 64

// handle is one pooled I/O context. An operation holds it exclusively from
// acquire until its submission returns, so it may set the locator: librados
// copies the context's object locator into the Objecter op when it prepares
// it (Objecter::prepare_read_op / prepare_mutate_op), before Operate or
// OperateAsync returns. The completion still points at the context, so it is
// destroyed only when no operation submitted on it is in flight.
type handle struct {
	ioctx    *rados.IOContext
	inflight int
	retired  bool
}

// poolState is shared by a Pool and every Pool WithLocator derives from it.
// Every field below mu is guarded by it.
type poolState struct {
	cluster   *cluster
	name      string
	namespace string
	id        int64

	// closeDone is closed once the first close has destroyed the I/O
	// contexts; closeErr is its result, readable after closeDone.
	closeDone chan struct{}
	closeErr  error

	mu      sync.Mutex
	drained *sync.Cond // broadcast when ops drops to zero
	closed  bool
	// ops counts the operations using a handle, abandoned ones still in
	// flight included; Close destroys nothing before it reaches zero.
	ops     int
	idle    []*handle
	all     map[*handle]struct{}
	watches map[*watch]struct{}
}

// pool is the seam's Pool: a poolState and the locator its operations set.
// A derived pool came from WithLocator and does not own the state.
type pool struct {
	state   *poolState
	locator string
	derived bool
}

var _ radosclient.Pool = (*pool)(nil)

func openPool(c *cluster, name, namespace string) (*pool, error) {
	s := &poolState{
		cluster:   c,
		name:      name,
		namespace: namespace,
		all:       map[*handle]struct{}{},
		watches:   map[*watch]struct{}{},
		closeDone: make(chan struct{}),
	}
	s.drained = sync.NewCond(&s.mu)
	// Opening one context up front reports a missing pool here rather than
	// on the first operation.
	ioctx, err := s.open()
	if err != nil {
		return nil, err
	}
	s.id = ioctx.GetPoolID()
	h := &handle{ioctx: ioctx}
	s.all[h] = struct{}{}
	s.idle = append(s.idle, h)
	if err := c.track(s); err != nil {
		ioctx.Destroy()
		return nil, err
	}
	return &pool{state: s}, nil
}

// open creates an I/O context on the pool and namespace. It is never called
// with s.mu held.
func (s *poolState) open() (*rados.IOContext, error) {
	op := "open pool " + s.name
	ioctx, err := s.cluster.conn.OpenIOContext(s.name)
	if err != nil {
		return nil, toSeamError(op, err)
	}
	ioctx.SetNamespace(s.namespace)
	// radosgw's rgw_init_ioctx sets full-try on every I/O context so that, at
	// a full pool or its quota, an op that adds data fails at once with ENOSPC
	// or EDQUOT instead of waiting in the Objecter for space (src/rgw/driver/
	// rados/rgw_tools.cc:97-98 at v19.2.6, :98-99 at v20.2.4).
	if err := ioctx.SetPoolFullTry(); err != nil {
		ioctx.Destroy()
		return nil, toSeamError(op, err)
	}
	return ioctx, nil
}

// closedError is what an operation on a closed Pool or Cluster returns. noun
// names what is closed: "pool" for a pool-level refusal, "cluster" for a
// cluster-level one.
func closedError(op, noun string) error {
	return fmt.Errorf("goceph: %s on a closed %s: %w", op, noun, radosclient.ErrClosed)
}

// acquire takes an I/O context for one operation, with locator set, and
// counts the operation until finish. The caller hands the context back with
// giveBack once its submission returns.
func (s *poolState) acquire(op, locator string) (*handle, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, closedError(op, "pool")
	}
	s.ops++
	var h *handle
	if n := len(s.idle); n > 0 {
		h = s.idle[n-1]
		s.idle = s.idle[:n-1]
		h.inflight++
	}
	s.mu.Unlock()

	if h == nil {
		ioctx, err := s.open()
		s.mu.Lock()
		if err != nil {
			s.endOp()
			s.mu.Unlock()
			return nil, err
		}
		h = &handle{ioctx: ioctx, inflight: 1}
		s.all[h] = struct{}{}
		s.mu.Unlock()
	}
	h.ioctx.SetLocator(locator)
	return h, nil
}

// giveBack returns h for reuse once the operation's submission returned.
func (s *poolState) giveBack(h *handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && len(s.idle) < maxIdle {
		s.idle = append(s.idle, h)
		return
	}
	h.retired = true
	s.destroyIfIdle(h)
}

// finish ends an operation acquire counted, once librados is done with it.
func (s *poolState) finish(h *handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h.inflight--
	s.destroyIfIdle(h)
	s.endOp()
}

// release is giveBack and finish together, for an operation that completed
// before its call returned.
func (s *poolState) release(h *handle) {
	s.giveBack(h)
	s.finish(h)
}

func (s *poolState) destroyIfIdle(h *handle) {
	if h.retired && h.inflight == 0 {
		h.ioctx.Destroy()
		delete(s.all, h)
	}
}

func (s *poolState) endOp() {
	s.ops--
	if s.ops == 0 {
		s.drained.Broadcast()
	}
}

// close refuses new operations, closes the outstanding watches, waits for
// every operation to finish, abandoned ones included, and destroys the I/O
// contexts. A concurrent or later close waits for the first to finish, so no
// closer returns while a context may still be live.
func (s *poolState) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return s.closeErr
	}
	s.closed = true
	watches := make([]*watch, 0, len(s.watches))
	for w := range s.watches {
		watches = append(watches, w)
	}
	s.mu.Unlock()

	var errs []error
	for _, w := range watches {
		errs = append(errs, w.Close())
	}

	s.mu.Lock()
	for s.ops > 0 {
		s.drained.Wait()
	}
	for h := range s.all {
		h.ioctx.Destroy()
	}
	s.all, s.idle = nil, nil
	s.mu.Unlock()

	s.cluster.untrack(s)
	s.closeErr = errors.Join(errs...)
	close(s.closeDone)
	return s.closeErr
}

// Name returns the pool name.
func (p *pool) Name() string { return p.state.name }

// Namespace returns the namespace.
func (p *pool) Namespace() string { return p.state.namespace }

// ID returns the pool's id, which librados resolved when the Pool opened.
func (p *pool) ID() int64 { return p.state.id }

// RequiredAlignment reads the alignment only when the pool requires one, as
// RGWRados::get_required_alignment does.
func (p *pool) RequiredAlignment(ctx context.Context) (uint64, error) {
	var align uint64
	err := p.withIOContext(ctx, "required alignment", func(ioctx *rados.IOContext) error {
		need, err := ioctx.RequiresAlignment()
		if err != nil {
			return toSeamError("requires alignment", err)
		}
		if !need {
			return nil
		}
		align, err = ioctx.Alignment()
		return toSeamError("required alignment", err)
	})
	return align, err
}

// WithLocator returns a Pool whose operations set loc as the object locator,
// "" for none. It shares this Pool's I/O contexts, and closing it does
// nothing.
func (p *pool) WithLocator(loc string) radosclient.Pool {
	return &pool{state: p.state, locator: loc, derived: true}
}

// Close closes the watches registered through the Pool, waits for its
// operations, including ones whose caller gave up, and destroys its I/O
// contexts, when called on the Pool Cluster.Pool returned. A Pool from
// WithLocator is only a locator over that one, and closing it does nothing.
func (p *pool) Close() error {
	if p.derived {
		return nil
	}
	return p.state.close()
}

// admit parks on the Cluster's in-flight limiter for one operation of n
// payload bytes, then takes an I/O context for it. The caller releases n to
// the limiter once librados is done with the operation.
func (p *pool) admit(ctx context.Context, op string, n int64) (*handle, error) {
	limit := p.state.cluster.limit
	if err := limit.acquire(ctx, n); err != nil {
		return nil, err
	}
	h, err := p.state.acquire(op, p.locator)
	if err != nil {
		limit.release(n)
		return nil, err
	}
	return h, nil
}

// Read runs a read op and returns the object version the OSD reports.
func (p *pool) Read(ctx context.Context, oid string, op *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rflags, err := translateFlags(flags)
	if err != nil {
		return 0, err
	}
	rop := rados.CreateReadOp()
	defer rop.Release()
	fs, err := translateRead(rop, op)
	if err != nil {
		return 0, err
	}
	name := opName("read", oid)
	n := weight(op.Steps())
	h, err := p.admit(ctx, name, n)
	if err != nil {
		return 0, err
	}
	cl := p.state.cluster
	cl.reads.add(n)

	if cl.mode == ModeSync {
		opErr := rop.Operate(h.ioctx, oid, rflags)
		version, verr := h.ioctx.GetLastVersion()
		p.state.release(h)
		cl.limit.release(n)
		return syncResult(name, fs, opErr, version, verr)
	}

	c, err := rop.OperateAsync(h.ioctx, oid, rflags)
	p.state.giveBack(h)
	// An abandoned operation keeps its budget until the reaper runs done:
	// its completion owns the operation's buffers until librados finishes.
	done := func() {
		p.state.finish(h)
		cl.limit.release(n)
	}
	if err != nil {
		done()
		return 0, toSeamError(name, err)
	}
	if err := cl.reaper.await(ctx, c, done); err != nil {
		return 0, err
	}
	defer done()
	defer c.Release()
	return asyncResult(name, fs, c)
}

// Write runs a write op and returns the object version librados reports.
func (p *pool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	wflags, err := translateFlags(flags)
	if err != nil {
		return 0, err
	}
	wop := rados.CreateWriteOp()
	defer wop.Release()
	fs, err := translateWrite(wop, op)
	if err != nil {
		return 0, err
	}
	name := opName("write", oid)
	n := weight(op.Steps())
	h, err := p.admit(ctx, name, n)
	if err != nil {
		return 0, err
	}
	cl := p.state.cluster
	cl.writes.add(n)

	mtime, hasMtime := op.Mtime()
	if cl.mode == ModeSync {
		defer cl.limit.release(n)
		return p.writeSync(h, wop, oid, name, fs, wflags, mtime, hasMtime)
	}

	var c *rados.AioCompletion
	if hasMtime {
		c, err = wop.OperateAsyncWithMtime(h.ioctx, oid, timespec(mtime), wflags)
	} else {
		c, err = wop.OperateAsync(h.ioctx, oid, wflags)
	}
	p.state.giveBack(h)
	done := func() {
		p.state.finish(h)
		cl.limit.release(n)
	}
	if err != nil {
		done()
		return 0, toSeamError(name, err)
	}
	if err := cl.reaper.await(ctx, c, done); err != nil {
		return 0, err
	}
	defer done()
	defer c.Release()
	return asyncResult(name, fs, c)
}

// writeSync runs wop with the blocking Operate.
func (p *pool) writeSync(h *handle, wop *rados.WriteOp, oid, name string, fs []finisher,
	flags rados.OperationFlags, mtime time.Time, hasMtime bool,
) (uint64, error) {
	var err error
	if hasMtime {
		err = wop.OperateWithMtime(h.ioctx, oid, timespec(mtime), flags)
	} else {
		err = wop.Operate(h.ioctx, oid, flags)
	}
	version, verr := h.ioctx.GetLastVersion()
	p.state.release(h)
	return syncResult(name, fs, err, version, verr)
}

// syncResult fills the results of an operation the blocking Operate ran and
// returns its version, which rados_get_last_version read from the handle
// while it was still this operation's alone.
func syncResult(name string, fs []finisher, opErr error, version uint64, verr error) (uint64, error) {
	o := newOutcome(name, opErr)
	finish(fs, o)
	switch {
	case o.err != nil:
		return 0, o.err
	case verr != nil:
		return 0, toSeamError(name, verr)
	}
	return version, nil
}

// asyncResult fills the results of a completed asynchronous operation and
// returns its version, before c is released.
func asyncResult(name string, fs []finisher, c *rados.AioCompletion) (uint64, error) {
	o := newOutcome(name, c.Err())
	finish(fs, o)
	if o.err != nil {
		return 0, o.err
	}
	return c.Version(), nil
}

// timespec converts a modification time for librados.
func timespec(t time.Time) rados.Timespec {
	return rados.Timespec{Sec: t.Unix(), Nsec: int64(t.Nanosecond())}
}

// ListObjects calls fn for every object in the namespace with its locator,
// "" for an object without one: the one page of an unlimited
// ListObjectsFrom from the start.
func (p *pool) ListObjects(ctx context.Context, fn func(oid, locator string) error) error {
	_, _, err := p.ListObjectsFrom(ctx, "", 0, fn)
	return err
}

// watch is a registered go-ceph watcher, the I/O context it holds for its
// lifetime, and the goroutine dispatching its events and errors.
type watch struct {
	state   *poolState
	h       *handle
	w       *rados.Watcher
	oid     string
	errs    chan error
	stop    chan struct{}
	stopped chan struct{}
	close   sync.Once
	err     error
}

// Watch registers fn for notifications on oid. fn runs on one goroutine per
// watch, and each notification is acknowledged with an empty payload after
// fn returns. fn must not close the Watch, its Pool, or the Cluster: each
// close waits for the dispatch goroutine that is running fn, which deadlocks.
// The same goroutine forwards the watch's errors to Err, so an error waits
// behind a running fn.
func (p *pool) Watch(ctx context.Context, oid string, fn func(notifyID, notifierID uint64, payload []byte)) (radosclient.Watch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := opName("watch", oid)
	h, err := p.state.acquire(name, p.locator)
	if err != nil {
		return nil, err
	}
	rw, err := h.ioctx.Watch(oid)
	if err != nil {
		p.state.release(h)
		return nil, toSeamError(name, err)
	}
	w := &watch{
		state:   p.state,
		h:       h,
		w:       rw,
		oid:     oid,
		errs:    make(chan error, 1),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go w.dispatch(fn)

	s := p.state
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.Join(closedError(name, "pool"), w.Close())
	}
	s.watches[w] = struct{}{}
	s.mu.Unlock()
	return w, nil
}

// dispatch delivers notifications and errors until Close stops it or the
// watcher's channels close. It is the only sender on w.errs, so it closes it.
func (w *watch) dispatch(fn func(notifyID, notifierID uint64, payload []byte)) {
	defer close(w.stopped)
	defer close(w.errs)
	events, errs := w.w.Events(), w.w.Errors()
	for events != nil || errs != nil {
		select {
		case <-w.stop:
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			fn(uint64(ev.ID), uint64(ev.NotifierID), ev.Data)
			if err := ev.Ack(nil); err != nil {
				slog.Warn("acknowledging a notify", slog.String("oid", w.oid), slog.Any("error", err))
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			seamErr := toSeamError(opName("watch", w.oid), err)
			select {
			case w.errs <- seamErr:
			default:
				// The error already pending says the watch is broken; this
				// one only shows a watch that keeps failing.
				slog.Debug("dropping a watch error while one is pending", slog.String("oid", w.oid), slog.Any("error", seamErr))
			}
		}
	}
}

// Err delivers the error librados reports when the watch breaks.
func (w *watch) Err() <-chan error { return w.errs }

// Close unregisters the watch, stops its dispatch goroutine and returns its
// I/O context to the pool.
//
// go-ceph's Delete removes the watcher from its registry before it calls
// rados_unwatch2, so a callback arriving later logs "unknown watcher" and
// returns. rados_unwatch2 cancels the watch in librados even when the OSD
// rejects the unwatch, so after it returns no new notification is queued.
// A callback that found the watcher before its removal blocks until its
// event is received only when Delete fails: it then leaves the watcher's
// channels open, which is why dispatch stops on its own channel rather than
// on theirs. Once Delete succeeds, go-ceph closes the watcher's done
// channel, so a blocked or late callback may take the done case instead and
// drop its event; queued notifications may be dropped after a successful
// Delete.
func (w *watch) Close() error {
	w.close.Do(func() {
		w.err = toSeamError(opName("unwatch", w.oid), w.w.Delete())
		if err := w.state.cluster.conn.WatcherFlush(); err != nil {
			w.err = errors.Join(w.err, toSeamError("watch flush", err))
		}
		close(w.stop)
		<-w.stopped

		s := w.state
		s.mu.Lock()
		delete(s.watches, w)
		s.mu.Unlock()
		s.release(w.h)
	})
	return w.err
}

// Notify sends payload to oid's watchers and returns their acknowledgements.
func (p *pool) Notify(ctx context.Context, oid string, payload []byte, timeout time.Duration) ([]radosclient.NotifyAck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := opName("notify", oid)
	h, err := p.state.acquire(name, p.locator)
	if err != nil {
		return nil, err
	}
	defer p.state.release(h)
	acks, _, err := h.ioctx.NotifyWithTimeout(oid, payload, timeout)
	out := make([]radosclient.NotifyAck, 0, len(acks))
	for _, a := range acks {
		out = append(out, radosclient.NotifyAck{
			NotifierID: uint64(a.NotifierID),
			Cookie:     uint64(a.WatcherID),
			Payload:    a.Response,
		})
	}
	return out, toSeamError(name, err)
}

// lockFlags maps the seam's lock flags to librados's LIBRADOS_LOCK_FLAG_* bits.
func lockFlags(f radosclient.LockFlags) *byte {
	var b byte
	if f&radosclient.LockRenew != 0 {
		b |= 1 // LIBRADOS_LOCK_FLAG_RENEW
	}
	return &b
}

// lockResult turns go-ceph's lock return code, which reports some failures
// as a code with a nil error, into a seam error.
func lockResult(op string, ret int, err error) error {
	if err != nil {
		return toSeamError(op, err)
	}
	if ret < 0 {
		return &radosclient.Error{Errno: errnoValue(ret), Op: op}
	}
	return nil
}

// withIOContext runs fn with an I/O context held for the whole call.
func (p *pool) withIOContext(ctx context.Context, op string, fn func(*rados.IOContext) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h, err := p.state.acquire(op, p.locator)
	if err != nil {
		return err
	}
	defer p.state.release(h)
	return fn(h.ioctx)
}

// LockExclusive takes the named exclusive advisory lock on oid.
func (p *pool) LockExclusive(ctx context.Context, oid, name, cookie, desc string, duration time.Duration, flags radosclient.LockFlags) error {
	op := opName("lock exclusive", oid)
	return p.withIOContext(ctx, op, func(ioctx *rados.IOContext) error {
		ret, err := ioctx.LockExclusive(oid, name, cookie, desc, duration, lockFlags(flags))
		return lockResult(op, ret, err)
	})
}

// LockShared takes the named shared advisory lock on oid under tag.
func (p *pool) LockShared(ctx context.Context, oid, name, cookie, tag, desc string, duration time.Duration, flags radosclient.LockFlags) error {
	op := opName("lock shared", oid)
	return p.withIOContext(ctx, op, func(ioctx *rados.IOContext) error {
		ret, err := ioctx.LockShared(oid, name, cookie, tag, desc, duration, lockFlags(flags))
		return lockResult(op, ret, err)
	})
}

// Unlock releases this client's named lock on oid held under cookie.
func (p *pool) Unlock(ctx context.Context, oid, name, cookie string) error {
	op := opName("unlock", oid)
	return p.withIOContext(ctx, op, func(ioctx *rados.IOContext) error {
		ret, err := ioctx.Unlock(oid, name, cookie)
		return lockResult(op, ret, err)
	})
}

// BreakLock releases another client's named lock on oid.
func (p *pool) BreakLock(ctx context.Context, oid, name, client, cookie string) error {
	op := opName("break lock", oid)
	return p.withIOContext(ctx, op, func(ioctx *rados.IOContext) error {
		ret, err := ioctx.BreakLock(oid, name, client, cookie)
		return lockResult(op, ret, err)
	})
}

// ListLockers returns the holders of the named lock on oid.
func (p *pool) ListLockers(ctx context.Context, oid, name string) ([]radosclient.Locker, error) {
	op := opName("list lockers", oid)
	var out []radosclient.Locker
	err := p.withIOContext(ctx, op, func(ioctx *rados.IOContext) error {
		info, err := ioctx.ListLockers(oid, name)
		if err != nil {
			return toSeamError(op, err)
		}
		n := min(len(info.Clients), len(info.Cookies), len(info.Addrs))
		if n != info.NumLockers {
			return &radosclient.Error{Errno: int32(syscall.EIO), Op: op}
		}
		out = make([]radosclient.Locker, n)
		for i := range n {
			out[i] = radosclient.Locker{Client: info.Clients[i], Cookie: info.Cookies[i], Address: info.Addrs[i]}
		}
		return nil
	})
	return out, err
}
