package radosclient

import (
	"context"
	"time"
)

// Cluster is one connected RADOS client.
type Cluster interface {
	// Pool opens an I/O context on pool within namespace ("" for the default namespace).
	Pool(ctx context.Context, pool, namespace string) (Pool, error)
	// MonCommand runs a JSON mon command and returns its output and status line.
	MonCommand(ctx context.Context, cmd []byte) (out []byte, status string, err error)
	// ConfigGet reads any Ceph configuration option by name through librados.
	ConfigGet(name string) (string, error)
	// RequiredOSDRelease returns the OSD map's require_osd_release name, e.g. "squid".
	RequiredOSDRelease(ctx context.Context) (string, error)
	// Close shuts the connection down.
	Close() error
}

// Pool is an I/O context: one pool and one namespace, with an optional object locator.
type Pool interface {
	// Name returns the pool name.
	Name() string
	// Namespace returns the namespace, "" for the default namespace.
	Namespace() string
	// ID returns the pool's id, which radosgw records in bucket index entries.
	ID() int64
	// WithLocator returns a Pool whose operations set the given object locator key.
	WithLocator(loc string) Pool
	// Read runs a read op and returns the object version the OSD reports for
	// it, the epoch radosgw stores from a stat; it is 0 when err is not nil.
	// Results are available on the op's steps afterwards. After a context
	// error the results are undefined and must not be read: the
	// implementation may still fill them when the completion fires.
	Read(ctx context.Context, oid string, op *ReadOp, flags OpFlags) (version uint64, err error)
	// Write runs a write op and returns the object version the OSD reports
	// for it; it is 0 when err is not nil. After a context error the results
	// are undefined and must not be read: the implementation may still fill
	// them when the completion fires.
	Write(ctx context.Context, oid string, op *WriteOp, flags OpFlags) (version uint64, err error)
	// ListObjects calls fn for every object in the namespace until fn returns an error.
	ListObjects(ctx context.Context, fn func(oid, locator string) error) error
	// Watch registers fn for notifications on oid until the returned Watch is closed.
	Watch(ctx context.Context, oid string, fn func(notifyID, notifierID uint64, payload []byte)) (Watch, error)
	// Notify sends payload to oid's watchers and returns their acknowledgements.
	Notify(ctx context.Context, oid string, payload []byte, timeout time.Duration) ([]NotifyAck, error)
	// LockExclusive takes the named exclusive advisory lock on oid.
	LockExclusive(ctx context.Context, oid, name, cookie, desc string, duration time.Duration, flags LockFlags) error
	// LockShared takes the named shared advisory lock on oid under tag.
	LockShared(ctx context.Context, oid, name, cookie, tag, desc string, duration time.Duration, flags LockFlags) error
	// Unlock releases this client's named lock on oid held under cookie.
	Unlock(ctx context.Context, oid, name, cookie string) error
	// BreakLock releases another client's named lock on oid.
	BreakLock(ctx context.Context, oid, name, client, cookie string) error
	// ListLockers returns the holders of the named lock on oid.
	ListLockers(ctx context.Context, oid, name string) ([]Locker, error)
	// Close releases the I/O context.
	Close() error
}

// Watch is a registered watch on one object.
type Watch interface {
	// Close unregisters the watch.
	Close() error
}

// NotifyAck is one watcher's acknowledgement of a notify.
type NotifyAck struct {
	NotifierID, Cookie uint64
	Payload            []byte
}

// Locker is one holder of an advisory lock.
type Locker struct {
	Client, Cookie, Address string
}

// Execer is what a class package needs: a place to add an exec step.
type Execer interface {
	Exec(class, method string, in []byte) *ExecResult
}

var (
	_ Execer = (*ReadOp)(nil)
	_ Execer = (*WriteOp)(nil)
)
