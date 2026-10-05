package fakerados

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// instanceID is the fake client's global id, which radosgw builds bucket
// instance ids and transaction ids from (RGWRados::create_bucket_id,
// RGWSI_ZoneUtils::init_unique_trans_id_deps).
const instanceID = 4155

// defaultMaxWriteOpReplyLen is the OSD's default osd_max_write_op_reply_len.
const defaultMaxWriteOpReplyLen = 64

// FakeFSID is the fake cluster's fsid, which FSID answers.
const FakeFSID = "00000000-0000-4000-8000-00000000f51d"

// Cluster is an in-memory radosclient.Cluster. Every pool exists; a spec that
// needs a missing one names it with FailPool. The zero value is not usable;
// call New.
type Cluster struct {
	mu         sync.Mutex
	release    string
	config     map[string]string
	classes    map[string]*class
	maxReply   int
	stores     map[storeKey]*store
	poolIDs    map[string]int64
	alignments map[string]uint64
	failPools  map[string]bool
	now        func() time.Time
	closed     bool
	lastCookie uint64
	lastNotify uint64
}

var _ radosclient.Cluster = (*Cluster)(nil)

type storeKey struct{ pool, ns string }

// class is a registered class emulator and the methods it gives the WR flag.
type class struct {
	fn     ClassFunc
	writes map[string]bool
}

// store is one namespace of one pool: its objects, the watches on them and
// the failures a spec injected.
type store struct {
	id      int64
	objects map[string]*Object
	// lastVer is the last version handed to each object name, so an object
	// removed and created again gets a later one, as RADOS's per-PG versions
	// only grow.
	lastVer    map[string]uint64
	watches    map[string][]*watch
	failWatch  map[string]*failure
	failNotify map[string]*failure
	notifies   map[string][][]byte
	reads      map[string]int
	lastReads  map[string]RecordedRead
	writes     map[string][]RecordedWrite
	// beforeWrite holds the hooks BeforeWrite registered, by object name.
	beforeWrite map[string]func(o *Object)
	// failWrite holds the errno FailNextWrite set, by object name.
	failWrite map[string]syscall.Errno
}

// RecordedRead is one read op a pool ran on an object, as Read received it.
type RecordedRead struct {
	steps []radosclient.Step
	flags radosclient.OpFlags
}

// Steps returns the op's steps in call order, their results filled as the op
// left them.
func (r RecordedRead) Steps() []radosclient.Step { return slices.Clone(r.steps) }

// Flags returns the op flags Read was given.
func (r RecordedRead) Flags() radosclient.OpFlags { return r.flags }

// RecordedWrite is one write op a pool ran on an object, as Write received
// it.
type RecordedWrite struct {
	steps    []radosclient.Step
	mtime    time.Time
	hasMtime bool
	flags    radosclient.OpFlags
}

// Steps returns the op's steps in call order, their results filled as the op
// left them.
func (w RecordedWrite) Steps() []radosclient.Step { return slices.Clone(w.steps) }

// Mtime returns the mtime the op carried, and whether it carried one.
func (w RecordedWrite) Mtime() (time.Time, bool) { return w.mtime, w.hasMtime }

// Flags returns the op flags Write was given.
func (w RecordedWrite) Flags() radosclient.OpFlags { return w.flags }

// failure is an injected error with the number of calls it still fails.
type failure struct {
	n   int
	err error
}

// take reports the error the next call fails with, spending one call.
func (f *failure) take() error {
	if f == nil || f.n <= 0 {
		return nil
	}
	f.n--
	return f.err
}

// New returns an empty cluster whose required OSD release is squid.
func New() *Cluster {
	return &Cluster{
		release:    "squid",
		config:     map[string]string{},
		classes:    map[string]*class{},
		maxReply:   defaultMaxWriteOpReplyLen,
		stores:     map[storeKey]*store{},
		poolIDs:    map[string]int64{},
		alignments: map[string]uint64{},
		failPools:  map[string]bool{},
		now:        time.Now,
	}
}

// SetRequiredOSDRelease sets the name RequiredOSDRelease answers.
func (c *Cluster) SetRequiredOSDRelease(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.release = name
}

// SetConfig sets the value ConfigGet answers for name.
func (c *Cluster) SetConfig(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config[name] = value
}

// SetClock sets the clock that stamps an object's mtime when a write op
// carries none, and that class emulators read through ClassCall.Now, as
// LockClass does to expire lock holders; it is time.Now until then.
func (c *Cluster) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// RegisterClass makes fn the emulator of every method of name, replacing
// any earlier one, and gives the methods writes names the WR flag
// (CLS_METHOD_WR). An op calling a class without an emulator fails with
// EOPNOTSUPP, as the OSD answers a class it cannot load.
func (c *Cluster) RegisterClass(name string, fn ClassFunc, writes ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cls := &class{fn: fn, writes: map[string]bool{}}
	for _, m := range writes {
		cls.writes[m] = true
	}
	c.classes[name] = cls
}

// SetMaxWriteOpReplyLen sets the OSD's osd_max_write_op_reply_len, the
// longest output a step may return from a ReturnVec op the OSD serves as a
// write; it is the OSD's default of 64 bytes until then.
func (c *Cluster) SetMaxWriteOpReplyLen(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxReply = n
}

// SetRequiredAlignment sets the alignment every handle on the named pool
// reports, whatever its namespace, as an erasure-coded pool without
// overwrites reports its stripe width. A pool keeps 0, a replicated pool's,
// until then. Only the report changes: the pool still runs every op as a
// replicated pool does.
func (c *Cluster) SetRequiredAlignment(pool string, alignment uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alignments[pool] = alignment
}

// FailPool makes opening the named pool fail with ENOENT, as opening a pool
// that does not exist does.
func (c *Cluster) FailPool(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failPools[name] = true
}

// store returns the store of pool and ns, creating it; c.mu is held. A pool
// keeps the id its first namespace was given.
func (c *Cluster) store(pool, ns string) *store {
	k := storeKey{pool, ns}
	s := c.stores[k]
	if s != nil {
		return s
	}
	id, ok := c.poolIDs[pool]
	if !ok {
		id = int64(len(c.poolIDs)) + 1
		c.poolIDs[pool] = id
	}
	s = &store{
		id:         id,
		objects:    map[string]*Object{},
		lastVer:    map[string]uint64{},
		watches:    map[string][]*watch{},
		failWatch:  map[string]*failure{},
		failNotify: map[string]*failure{},
		notifies:   map[string][][]byte{},
		reads:      map[string]int{},
		lastReads:  map[string]RecordedRead{},
		writes:     map[string][]RecordedWrite{},

		beforeWrite: map[string]func(o *Object){},
		failWrite:   map[string]syscall.Errno{},
	}
	c.stores[k] = s
	return s
}

// Object returns the stored object, nil when it does not exist. It is the
// cluster's own copy, so a spec may change it to stage a state, but a write
// op replaces it with a new one rather than changing it in place.
func (c *Cluster) Object(pool, ns, oid string) *Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store(pool, ns).objects[oid]
}

// Put stores an object holding data, at version 1, replacing any object of
// that name.
func (c *Cluster) Put(pool, ns, oid string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.store(pool, ns)
	obj := newObject(c.now())
	obj.Data = slices.Clone(data)
	obj.Version = 1
	s.objects[oid] = obj
	s.lastVer[oid] = max(s.lastVer[oid], 1)
}

// Remove deletes the object, if it exists. A write op that creates the name
// again gets a version past the one it had, as after a remove op; Put stores
// at version 1 as ever.
func (c *Cluster) Remove(pool, ns, oid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.store(pool, ns).objects, oid)
}

// Watches returns the number of watches registered on the object and not
// yet broken or closed.
func (c *Cluster) Watches(pool, ns, oid string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.store(pool, ns).watches[oid])
}

// BreakWatches breaks every watch on the object: each reports err on Err
// once and receives no more notifies, as a watch the OSD dropped does.
func (c *Cluster) BreakWatches(pool, ns, oid string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(pool, ns).breakWatches(oid, err)
}

// FailWatch makes the next n Watch calls on the object fail with err.
func (c *Cluster) FailWatch(pool, ns, oid string, n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(pool, ns).failWatch[oid] = &failure{n: n, err: err}
}

// FailNextWrite makes the next write op on the object fail with errno
// without applying any of its steps, as an op the OSD refuses whole does. The
// op is still counted and recorded. A spec stages with it a reply that leaves
// the op's outcome unknown, such as ETIMEDOUT.
func (c *Cluster) FailNextWrite(pool, ns, oid string, errno syscall.Errno) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(pool, ns).failWrite[oid] = errno
}

// FailNotify makes the next n Notify calls on the object fail with err,
// delivering nothing and returning no acks.
func (c *Cluster) FailNotify(pool, ns, oid string, n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(pool, ns).failNotify[oid] = &failure{n: n, err: err}
}

// Notifies returns the payloads the object's notifies delivered, oldest
// first.
func (c *Cluster) Notifies(pool, ns, oid string) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out [][]byte
	for _, p := range c.store(pool, ns).notifies[oid] {
		out = append(out, slices.Clone(p))
	}
	return out
}

// Reads returns the number of read ops run on the object, failed ones
// included.
func (c *Cluster) Reads(pool, ns, oid string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store(pool, ns).reads[oid]
}

// Writes returns the number of write ops run on the object, failed ones
// included.
func (c *Cluster) Writes(pool, ns, oid string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.store(pool, ns).writes[oid])
}

// LastWrite returns the last write op run on the object, failed or not, and
// the zero RecordedWrite when none has run.
func (c *Cluster) LastWrite(pool, ns, oid string) RecordedWrite {
	c.mu.Lock()
	defer c.mu.Unlock()
	ws := c.store(pool, ns).writes[oid]
	if len(ws) == 0 {
		return RecordedWrite{}
	}
	return ws[len(ws)-1]
}

// WritesTo returns the write ops run on the object, failed ones included,
// oldest first, and nil when none has run.
func (c *Cluster) WritesTo(pool, ns, oid string) []RecordedWrite {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.store(pool, ns).writes[oid])
}

// LastRead returns the last read op run on the object, failed or not, and
// the zero RecordedRead when none has run.
func (c *Cluster) LastRead(pool, ns, oid string) RecordedRead {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store(pool, ns).lastReads[oid]
}

// ResetCounters forgets every read and write op run so far in every pool, so
// Reads, Writes, LastRead, LastWrite and WritesTo count from here. The
// objects stay.
func (c *Cluster) ResetCounters() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.stores {
		clear(s.reads)
		clear(s.lastReads)
		clear(s.writes)
	}
}

// PoolID returns the id every handle on the named pool reports.
func (c *Cluster) PoolID(pool string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store(pool, "").id
}

// Pool opens a handle on pool and namespace. Each call returns a new handle,
// as each rados_ioctx_create does, over the objects every handle on that pool
// and namespace shares.
func (c *Cluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	op := "open pool " + pool
	if c.closed {
		return nil, closedError(op, "cluster")
	}
	if c.failPools[pool] {
		return nil, &radosclient.Error{Errno: int32(syscall.ENOENT), Op: op}
	}
	return &Pool{
		cluster: c,
		name:    pool,
		ns:      namespace,
		store:   c.store(pool, namespace),
		h:       &handle{watches: map[*watch]struct{}{}},
	}, nil
}

// MonCommand is not supported.
func (c *Cluster) MonCommand(ctx context.Context, _ []byte) (out []byte, status string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", fmt.Errorf("fakerados: mon command: %w", radosclient.ErrNotSupported)
}

// ConfigGet answers from SetConfig. An option it was not given is ENOENT, as
// rados_conf_get answers a name it does not know; cephconf.Options reports
// that as cephconf.ErrUnknownOption.
func (c *Cluster) ConfigGet(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.config[name]
	if !ok {
		return "", &radosclient.Error{Errno: int32(syscall.ENOENT), Op: "config get " + name}
	}
	return v, nil
}

// RequiredOSDRelease returns the name SetRequiredOSDRelease set.
func (c *Cluster) RequiredOSDRelease(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.release, nil
}

// InstanceID returns the fake client's global id, 4155.
func (c *Cluster) InstanceID() uint64 { return instanceID }

// FSID returns FakeFSID, and refuses once the cluster is closed, as goceph
// refuses a call on a closed connection.
func (c *Cluster) FSID() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", closedError("fsid", "cluster")
	}
	return FakeFSID, nil
}

// Close refuses every later call and closes every watch, as closing a
// connection closes its pools and their watches.
func (c *Cluster) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, s := range c.stores {
		for _, oid := range slices.Collect(maps.Keys(s.watches)) {
			for _, w := range slices.Clone(s.watches[oid]) {
				w.closeLocked()
			}
		}
	}
	return nil
}

// closedError is what a call on a closed Pool or Cluster returns.
func closedError(op, noun string) error {
	return fmt.Errorf("fakerados: %s on a closed %s: %w", op, noun, radosclient.ErrClosed)
}
