package fakerados

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Pool is a handle on one namespace of one pool of a Cluster.
type Pool struct {
	cluster *Cluster
	name    string
	ns      string
	store   *store
	// h is shared with the handles WithLocator derives, which closing does
	// not close.
	h       *handle
	derived bool
}

var _ radosclient.Pool = (*Pool)(nil)

// handle is what closing a Pool closes: the handle itself and the watches
// registered through it.
type handle struct {
	closed  bool
	watches map[*watch]struct{}
}

// Name returns the pool name.
func (p *Pool) Name() string { return p.name }

// Namespace returns the namespace, "" for the default namespace.
func (p *Pool) Namespace() string { return p.ns }

// ID returns the pool's id: a small integer, the same for every namespace
// of the pool, in the order the cluster first saw each pool.
func (p *Pool) ID() int64 { return p.store.id }

// RequiredAlignment returns what SetRequiredAlignment set for the pool, 0
// until then.
func (p *Pool) RequiredAlignment(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := p.usable("required alignment"); err != nil {
		return 0, err
	}
	return c.alignments[p.name], nil
}

// WithLocator returns a handle on the same objects: the fake keeps an object
// by name alone. Closing it does nothing, as closing a goceph handle derived
// by WithLocator does nothing.
func (p *Pool) WithLocator(string) radosclient.Pool {
	d := *p
	d.derived = true
	return &d
}

// usable is the error an operation on p fails with before it runs, nil when
// it may run; the cluster's lock is held.
func (p *Pool) usable(op string) error {
	switch {
	case p.cluster.closed:
		return closedError(op, "cluster")
	case p.h.closed:
		return closedError(op, "pool")
	}
	return nil
}

// Read runs a read op and returns the object's version after it. It fails
// with ENOENT when the object does not exist, unless the op calls a method
// with the WR flag, which makes it a write that may create the object.
func (p *Pool) Read(ctx context.Context, oid string, op *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	steps := op.Steps()
	if err := validate(steps, false, flags); err != nil {
		return 0, err
	}
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	name := opName("read", oid)
	if err := p.usable(name); err != nil {
		return 0, err
	}
	return p.run(name, oid, steps, false, flags, time.Time{})
}

// Write runs a write op atomically and returns the object's version after
// it: the next version when the op changed the object, the version it had
// when the op changed nothing.
func (p *Pool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	steps := op.Steps()
	if err := validate(steps, true, flags); err != nil {
		return 0, err
	}
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	name := opName("write", oid)
	if err := p.usable(name); err != nil {
		return 0, err
	}
	// librados stamps a write op without an mtime with the client's clock.
	mtime, ok := op.Mtime()
	if !ok {
		mtime = c.now()
	}
	return p.run(name, oid, steps, true, flags, mtime)
}

// ListObjects calls fn for every object in the namespace in name order, with
// an empty locator.
func (p *Pool) ListObjects(ctx context.Context, fn func(oid, locator string) error) error {
	c := p.cluster
	c.mu.Lock()
	if err := p.usable("list objects"); err != nil {
		c.mu.Unlock()
		return err
	}
	oids := slices.Sorted(maps.Keys(p.store.objects))
	c.mu.Unlock()
	for _, oid := range oids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(oid, ""); err != nil {
			return err
		}
	}
	return nil
}

// watch is a registered watch on one object.
type watch struct {
	cluster *Cluster
	store   *store
	h       *handle
	oid     string
	cookie  uint64
	fn      func(notifyID, notifierID uint64, payload []byte)
	errs    chan error
	// attached reports that the object's watch list holds the watch, which
	// breaking and closing end; closed that Close ran. Both are guarded by
	// the cluster's lock.
	attached bool
	closed   bool
}

// Err delivers the error that broke the watch, once.
func (w *watch) Err() <-chan error { return w.errs }

// Close unregisters the watch and closes its error channel.
func (w *watch) Close() error {
	w.cluster.mu.Lock()
	defer w.cluster.mu.Unlock()
	w.closeLocked()
	return nil
}

// closeLocked is Close with the cluster's lock held.
func (w *watch) closeLocked() {
	if w.closed {
		return
	}
	w.closed = true
	w.detach()
	delete(w.h.watches, w)
	close(w.errs)
}

// detach removes the watch from its object's watch list.
func (w *watch) detach() {
	if !w.attached {
		return
	}
	w.attached = false
	w.store.watches[w.oid] = slices.DeleteFunc(w.store.watches[w.oid], func(o *watch) bool { return o == w })
	if len(w.store.watches[w.oid]) == 0 {
		delete(w.store.watches, w.oid)
	}
}

// breakWatches detaches every watch on oid and hands each err once; the
// cluster's lock is held.
func (s *store) breakWatches(oid string, err error) {
	for _, w := range slices.Clone(s.watches[oid]) {
		w.detach()
		select {
		case w.errs <- err:
		default:
		}
	}
}

// Watch registers fn for the object's notifies. It fails with ENOENT when
// the object does not exist, as the OSD refuses to watch a missing object.
func (p *Pool) Watch(ctx context.Context, oid string, fn func(notifyID, notifierID uint64, payload []byte)) (radosclient.Watch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	name := opName("watch", oid)
	if err := p.usable(name); err != nil {
		return nil, err
	}
	if err := p.store.failWatch[oid].take(); err != nil {
		return nil, err
	}
	if p.store.objects[oid] == nil {
		return nil, &radosclient.Error{Errno: int32(syscall.ENOENT), Op: name}
	}
	c.lastCookie++
	w := &watch{
		cluster:  c,
		store:    p.store,
		h:        p.h,
		oid:      oid,
		cookie:   c.lastCookie,
		fn:       fn,
		errs:     make(chan error, 1),
		attached: true,
	}
	p.store.watches[oid] = append(p.store.watches[oid], w)
	p.h.watches[w] = struct{}{}
	return w, nil
}

// Notify delivers payload to every watch on the object, in the order they
// registered, and returns once each has handled it, with an ack from each.
// The watches run without the cluster's lock, so they may call it. Notify
// fails with ENOENT when the object does not exist, as a notify is a read
// of the object.
func (p *Pool) Notify(ctx context.Context, oid string, payload []byte, _ time.Duration) ([]radosclient.NotifyAck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := p.cluster
	c.mu.Lock()
	name := opName("notify", oid)
	if err := p.usable(name); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if err := p.store.failNotify[oid].take(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if p.store.objects[oid] == nil {
		c.mu.Unlock()
		return nil, &radosclient.Error{Errno: int32(syscall.ENOENT), Op: name}
	}
	p.store.notifies[oid] = append(p.store.notifies[oid], slices.Clone(payload))
	c.lastNotify++
	notifyID := c.lastNotify
	watches := slices.Clone(p.store.watches[oid])
	c.mu.Unlock()

	acks := make([]radosclient.NotifyAck, 0, len(watches))
	for _, w := range watches {
		w.fn(notifyID, instanceID, slices.Clone(payload))
		acks = append(acks, radosclient.NotifyAck{NotifierID: instanceID, Cookie: w.cookie})
	}
	return acks, nil
}

// LockExclusive is not supported.
func (p *Pool) LockExclusive(context.Context, string, string, string, string, time.Duration, radosclient.LockFlags) error {
	return notSupported("lock exclusive")
}

// LockShared is not supported.
func (p *Pool) LockShared(context.Context, string, string, string, string, string, time.Duration, radosclient.LockFlags) error {
	return notSupported("lock shared")
}

// Unlock is not supported.
func (p *Pool) Unlock(context.Context, string, string, string) error {
	return notSupported("unlock")
}

// BreakLock is not supported.
func (p *Pool) BreakLock(context.Context, string, string, string, string) error {
	return notSupported("break lock")
}

// ListLockers is not supported.
func (p *Pool) ListLockers(context.Context, string, string) ([]radosclient.Locker, error) {
	return nil, notSupported("list lockers")
}

func notSupported(op string) error {
	return fmt.Errorf("fakerados: %s: %w", op, radosclient.ErrNotSupported)
}

// Close closes the handle and the watches registered through it. Closing a
// handle WithLocator derived does nothing.
func (p *Pool) Close() error {
	if p.derived {
		return nil
	}
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	p.h.closed = true
	for _, w := range slices.Collect(maps.Keys(p.h.watches)) {
		w.closeLocked()
	}
	return nil
}

// opName names an operation on oid as goceph names it in its errors.
func opName(kind, oid string) string {
	return kind + " " + strconv.Quote(oid)
}
