package fakerados

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
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
	hook := p.store.beforeRead[oid]
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	name := opName("read", oid)
	if err := p.usable(name); err != nil {
		return 0, err
	}
	p.store.reads[oid]++
	p.store.lastReads[oid] = RecordedRead{steps: steps, flags: flags}
	if err := p.store.failRead[oid].take(); err != nil {
		failResults(steps, err)
		return 0, err
	}
	return p.run(name, oid, steps, false, flags, time.Time{})
}

// BeforeWrite makes fn run before every later write op on the object, with
// the stored object, nil when it does not exist, so a spec can stage what an
// op meets: a racing writer's change, or the object gone. fn runs without the
// cluster's lock, so it may call the Cluster. A change it makes to the object
// in place is one the op sees only while no other op on the object runs
// concurrently: such an op races the change, and may replace the object
// between fn and this op, which then meets the replacement. A nil fn removes
// the hook.
func (c *Cluster) BeforeWrite(pool, ns, oid string, fn func(o *Object)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fn == nil {
		delete(c.store(pool, ns).beforeWrite, oid)
		return
	}
	c.store(pool, ns).beforeWrite[oid] = fn
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
	hook, stored := p.store.beforeWrite[oid], p.store.objects[oid]
	c.mu.Unlock()
	if hook != nil {
		hook(stored)
	}
	ver, err := p.write(oid, op, steps, flags)
	if err != nil {
		return ver, err
	}
	c.mu.Lock()
	after := p.store.afterWrite[oid]
	c.mu.Unlock()
	if after != nil {
		after()
	}
	return ver, nil
}

// write runs Write's op under the cluster's lock.
func (p *Pool) write(oid string, op *radosclient.WriteOp, steps []radosclient.Step, flags radosclient.OpFlags) (uint64, error) {
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	name := opName("write", oid)
	if err := p.usable(name); err != nil {
		return 0, err
	}
	mtime, ok := op.Mtime()
	p.store.writes[oid] = append(p.store.writes[oid], RecordedWrite{steps: steps, mtime: mtime, hasMtime: ok, flags: flags})
	if err := p.store.failWrite[oid].take(); err != nil {
		failResults(steps, err)
		return 0, err
	}
	// librados stamps a write op without an mtime with the client's clock.
	if !ok {
		mtime = c.now()
	}
	ver, err := p.run(name, oid, steps, true, flags, mtime)
	if err == nil {
		p.store.order = append(p.store.order, oid)
	}
	return ver, err
}

// listing returns the namespace's objects in the order RADOS lists them,
// hobject order: the fake keeps no locators, so that is by bit-reversed
// placement hash, then by name.
func (p *Pool) listing() ([]string, error) {
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := p.usable("list objects"); err != nil {
		return nil, err
	}
	oids := slices.Collect(maps.Keys(p.store.objects))
	slices.SortFunc(oids, func(a, b string) int {
		ha, hb := radosclient.PlacementHash(p.ns, a), radosclient.PlacementHash(p.ns, b)
		switch {
		case radosclient.ListAfter(ha, a, hb, b):
			return 1
		case radosclient.ListAfter(hb, b, ha, a):
			return -1
		}
		return 0
	})
	return oids, nil
}

// ListObjects calls fn for every object in the namespace in RADOS's listing
// order, with an empty locator: the one page of an unlimited
// ListObjectsFrom from the start.
func (p *Pool) ListObjects(ctx context.Context, fn func(oid, locator string) error) error {
	_, _, err := p.ListObjectsFrom(ctx, "", 0, fn)
	return err
}

// ListObjectsFrom pages ListObjects' listing with radosclient.ListPage, as
// goceph pages a librados listing.
func (p *Pool) ListObjectsFrom(ctx context.Context, token string, limit int, fn func(oid, locator string) error) (next string, more bool, err error) {
	oids, err := p.listing()
	if err != nil {
		return "", false, err
	}
	return radosclient.ListPage(ctx, &listSource{oids: oids}, p.ns, token, limit, fn)
}

// listSource is a snapshot of a listing as a radosclient.ListSource. Its
// seek stays at the start, as in a pool of one placement group, so
// ListPage skips the whole listing before the resume point.
type listSource struct{ oids []string }

func (*listSource) Seek(uint32) {}

func (s *listSource) Next() (oid, locator string, ok bool) {
	if len(s.oids) == 0 {
		return "", "", false
	}
	oid, s.oids = s.oids[0], s.oids[1:]
	return oid, "", true
}

func (*listSource) Err() error { return nil }

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

// LockExclusive takes the named exclusive lock on oid as rados_lock_exclusive
// does, through the lock class's lock method in an op of its own, so it needs
// LockClass registered and fails with EOPNOTSUPP otherwise. The op is not a
// seam write, so Writes and WritesTo leave it out; Locks shows its holders.
func (p *Pool) LockExclusive(ctx context.Context, oid, name, cookie, desc string, duration time.Duration, flags radosclient.LockFlags) error {
	var lf lock.Flags
	if flags&radosclient.LockRenew != 0 {
		lf = lock.FlagMayRenew
	}
	op := radosclient.NewWriteOp()
	lock.Lock(op, lock.LockOp{Name: name, Type: lock.TypeExclusive, Cookie: cookie, Description: desc, Duration: duration, Flags: lf}, denc.Squid)
	return p.lockOp(ctx, opName("lock exclusive", oid), oid, op)
}

// Locks returns the holders of the named lock on the object that have not
// expired by the cluster's clock, ordered by holder, nil when it has none.
// Each names its client as ListLockers does, with an empty Address. A lock
// xattr that does not decode panics, failing the spec.
func (c *Cluster) Locks(pool, ns, oid, name string) []radosclient.Locker {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj := c.store(pool, ns).objects[oid]
	if obj == nil {
		return nil
	}
	b, ok := obj.Xattrs[lockXattrPrefix+name]
	if !ok {
		return nil
	}
	d := denc.NewDecoder(b)
	info := lock.DecodeInfo(d)
	if err := d.Err(); err != nil {
		panic(fmt.Sprintf("fakerados: lock %q on %s/%s/%s does not decode: %v", name, pool, ns, oid, err))
	}
	now := c.now()
	var out []radosclient.Locker
	for id, li := range info.Lockers {
		if !li.Expiration.IsZero() && li.Expiration.Before(now) {
			continue
		}
		out = append(out, radosclient.Locker{Client: id.Locker.String(), Cookie: id.Cookie})
	}
	slices.SortFunc(out, func(a, b radosclient.Locker) int {
		return cmp.Or(strings.Compare(a.Client, b.Client), strings.Compare(a.Cookie, b.Cookie))
	})
	return out
}

// lockOp runs a lock call's op on oid without recording it as a write.
func (p *Pool) lockOp(ctx context.Context, name, oid string, op *radosclient.WriteOp) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := p.cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := p.usable(name); err != nil {
		return err
	}
	_, err := p.run(name, oid, op.Steps(), true, radosclient.OpFlagNone, c.now())
	return err
}

// LockShared is not supported.
func (p *Pool) LockShared(context.Context, string, string, string, string, string, time.Duration, radosclient.LockFlags) error {
	return notSupported("lock shared")
}

// Unlock releases the named lock the fake client holds on oid under cookie,
// as rados_unlock does through the lock class's unlock method: ENOENT when it
// holds none.
func (p *Pool) Unlock(ctx context.Context, oid, name, cookie string) error {
	op := radosclient.NewWriteOp()
	lock.Unlock(op, name, cookie, denc.Squid)
	return p.lockOp(ctx, opName("unlock", oid), oid, op)
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
