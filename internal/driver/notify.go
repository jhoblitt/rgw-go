package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// controlPrefix is notify_oid_prefix (services/svc_notify.cc:19 at v19.2.6,
// :22 at v20.2.4).
const controlPrefix = "notify"

// controlOIDs names the control objects as init_watch does
// (services/svc_notify.cc:199-221 at v19.2.6, :162-183 at v20.2.4): num
// objects notify.<i>, the single legacy object notify for 0, and one object,
// notify.0, for a negative count. init_watch holds the count in an int, so
// only the option's low 32 bits count.
func controlOIDs(num int64) []string {
	n := int32(num) //nolint:gosec // truncated as radosgw's int num_watchers truncates it
	if n == 0 {
		return []string{controlPrefix}
	}
	oids := make([]string, max(n, 1))
	for i := range oids {
		oids[i] = controlPrefix + "." + strconv.Itoa(i)
	}
	return oids
}

// The pause between attempts to register a control watch doubles from
// watchBackoffMin after each failure, up to watchBackoffMax, and returns to
// watchBackoffMin once a registration succeeds.
const (
	watchBackoffMin = time.Second
	watchBackoffMax = 30 * time.Second
)

// errWatchClosed reports a watch whose error channel closed without an
// error, as it does when the pool it was registered through closes.
var errWatchClosed = errors.New("watch closed without an error")

// notifier is RGWSI_Notify: the control objects, a watch on each, the
// metadata cache's enable flag, and the cache-notify record sent after a
// write.
type notifier struct {
	pool       radosclient.Pool
	oids       []string
	release    denc.Release
	maxRetries uint64 // rgw_max_notify_retries
	handle     func(meta.CacheNotifyInfo)
	onEnabled  func(bool)
	hidden     hiddenPools

	backoffMin, backoffMax time.Duration
	sleep                  func(context.Context, time.Duration) error

	mu sync.Mutex
	up map[int]struct{} // the indexes into oids of the registered watches
}

// newNotifier returns a notifier over the control objects oids of pool, which
// must not be empty. handle gets every notify that decodes; onEnabled is told
// true once every watch is registered and false once one stops being. Both
// are called from the watches' goroutines, onEnabled with the notifier's
// lock held, so it must not call back into the notifier.
func newNotifier(pool radosclient.Pool, oids []string, release denc.Release, maxRetries uint64,
	handle func(meta.CacheNotifyInfo), onEnabled func(bool),
) *notifier {
	return &notifier{
		pool: pool, oids: oids, release: release, maxRetries: maxRetries, handle: handle, onEnabled: onEnabled,
		backoffMin: watchBackoffMin, backoffMax: watchBackoffMax, sleep: sleepCtx, up: map[int]struct{}{},
	}
}

// sleepCtx waits d, or until ctx ends and then returns its error.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// createControlObjects creates each control object that does not exist.
func (n *notifier) createControlObjects(ctx context.Context) error {
	for _, oid := range n.oids {
		if err := n.createControlObject(ctx, oid); err != nil {
			return err
		}
	}
	return nil
}

// createControlObject creates oid unless it exists, as init_watch's
// op.create(false) does (services/svc_notify.cc:231-238 at v19.2.6,
// :198-205 at v20.2.4); one that exists keeps its data.
func (n *notifier) createControlObject(ctx context.Context, oid string) error {
	wop := radosclient.NewWriteOp()
	wop.Create(false)
	if _, err := n.pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrExists) {
		return fmt.Errorf("creating control object %s: %w", oid, err)
	}
	return nil
}

// run keeps a watch registered on every control object until ctx ends, then
// returns ctx's error. A registration that fails, the first as any later
// one, is retried after the backoff, for ever. radosgw fails to start when
// a first registration fails (init_watch, services/svc_notify.cc:243-261 at
// v19.2.6, :207-221 at v20.2.4), re-registers a lost watch at once, and
// aborts the process after 100 failures over its lifetime
// (RGWWatcher::reinit, :89-115 at v19.2.6, :87-113 at v20.2.4;
// docs/ceph-upstream-bugs.md, tracker #80992).
func (n *notifier) run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	for i, oid := range n.oids {
		g.Go(func() error {
			n.watchLoop(gctx, i, oid)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

// watchLoop keeps a watch registered on oid, the control object at index i,
// until ctx ends.
func (n *notifier) watchLoop(ctx context.Context, i int, oid string) {
	backoff := n.backoffMin
	for {
		w, err := n.pool.Watch(ctx, oid, n.onNotify)
		switch {
		case err == nil:
			backoff = n.backoffMin
			lost := n.hold(ctx, i, oid, w)
			if ctx.Err() != nil {
				return
			}
			slog.ErrorContext(ctx, "control watch lost",
				slog.String("oid", oid), slog.Duration("retry_in", backoff), slog.Any("error", lost))
		case ctx.Err() != nil:
			return
		default:
			slog.ErrorContext(ctx, "registering a control watch",
				slog.String("oid", oid), slog.Duration("retry_in", backoff), slog.Any("error", err))
			// radosgw creates its control objects only at startup, so a deleted
			// one would stay unwatched, and the cache off, until a restart.
			if errors.Is(err, radosclient.ErrNotFound) {
				if cerr := n.createControlObject(ctx, oid); cerr != nil {
					slog.ErrorContext(ctx, "re-creating a deleted control object", slog.String("oid", oid), slog.Any("error", cerr))
				} else {
					slog.WarnContext(ctx, "re-created a deleted control object", slog.String("oid", oid))
				}
			}
		}
		if n.sleep(ctx, backoff) != nil {
			return
		}
		backoff = min(backoff*2, n.backoffMax)
	}
}

// hold counts watch i registered until it breaks or ctx ends, then closes it
// and returns the error that broke it. A failure to close a watch that broke
// is expected. One at shutdown is logged and does not fail run, as
// finalize_watch ignores it (services/svc_notify.cc:266-276 at v19.2.6,
// :226-240 at v20.2.4).
func (n *notifier) hold(ctx context.Context, i int, oid string, w radosclient.Watch) error {
	n.setUp(ctx, i, true)
	var lost error
	select {
	case <-ctx.Done():
	case err, ok := <-w.Err():
		lost = err
		if !ok {
			lost = errWatchClosed
		}
	}
	n.setUp(ctx, i, false)
	if err := w.Close(); err != nil {
		if lost != nil {
			slog.DebugContext(ctx, "closing a lost control watch", slog.String("oid", oid), slog.Any("error", err))
		} else {
			slog.WarnContext(ctx, "closing a control watch", slog.String("oid", oid), slog.Any("error", err))
		}
	}
	return lost
}

// setUp counts watch i registered or not, and tells onEnabled when every
// watch has become registered or one has stopped being, as add_watcher and
// remove_watcher do (services/svc_notify.cc:343-365 at v19.2.6, :330-352 at
// v20.2.4). onEnabled runs under the lock, as _set_enabled runs under
// watchers_lock, so two watches changing at once cannot reorder the calls.
func (n *notifier) setUp(ctx context.Context, i int, up bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	before := len(n.up) == len(n.oids)
	if up {
		n.up[i] = struct{}{}
	} else {
		delete(n.up, i)
	}
	switch after := len(n.up) == len(n.oids); {
	case after && !before:
		slog.InfoContext(ctx, "all control watches registered", slog.Int("watches", len(n.oids)))
		n.onEnabled(true)
	case before && !after:
		slog.InfoContext(ctx, "a control watch is no longer registered", slog.Int("watches", len(n.oids)))
		n.onEnabled(false)
	}
}

// onNotify decodes a cache-notify record and hands it to handle, as
// RGWWatcher::handle_notify hands it to RGWSI_SysObj_Cache::watch_cb
// (services/svc_notify.cc:56-81 at v19.2.6, :54-79 at v20.2.4;
// services/svc_sys_obj_cache.cc:465-482 at both). A record that does not
// decode is logged and dropped; the seam acks the notify either way, as
// handle_notify acks whatever the callback returned.
func (n *notifier) onNotify(_, notifierID uint64, payload []byte) {
	d := denc.NewDecoder(payload)
	info := meta.DecodeCacheNotifyInfo(d)
	if err := d.Err(); err != nil {
		slog.Warn("dropping a control notify that does not decode",
			slog.Uint64("notifier_id", notifierID), slog.Any("error", err))
		return
	}
	n.handle(info)
}

// pick is pick_control_obj: the control object the notify for the cache entry
// named key goes to (services/svc_notify.cc:191-197 at v19.2.6, :153-160 at
// v20.2.4).
func (n *notifier) pick(key string) string {
	return n.oids[shardMod(meta.StrHashLinux(key), uint32(len(n.oids)))] //nolint:gosec // controlOIDs makes at most 2^31-1 objects
}

// distribute is RGWSI_Notify::distribute and robust_notify
// (services/svc_notify.cc:394-412 and :455-513 at v19.2.6, :381-399 and
// :442-500 at v20.2.4): one notify with librados's default timeout, then,
// while it keeps timing out, up to maxRetries notifies invalidating the same
// object, which carry nothing else. The caller logs a failure and carries
// on, as radosgw does.
func (n *notifier) distribute(ctx context.Context, key string, info meta.CacheNotifyInfo) error {
	oid := n.pick(key)
	_, err := n.pool.Notify(ctx, oid, encodeAt(info, n.release), 0)
	if !errors.Is(err, radosclient.ErrTimedOut) {
		return err
	}
	retry := encodeAt(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: info.Obj}, n.release)
	for try := uint64(0); errors.Is(err, radosclient.ErrTimedOut) && try < n.maxRetries; try++ {
		slog.WarnContext(ctx, "control notify timed out, sending an invalidation",
			slog.String("oid", oid), slog.String("obj", n.hidden.name(info.Obj.Pool, info.Obj.OID)), slog.Uint64("try", try))
		_, err = n.pool.Notify(ctx, oid, retry, 0)
	}
	return err
}

// encodable is a meta type whose encoding depends on the release.
type encodable interface {
	Encode(e *denc.Encoder, r denc.Release)
}

// encodeAt renders v as radosgw encodes it at release r.
func encodeAt(v encodable, r denc.Release) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}
