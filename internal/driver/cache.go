package driver

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// normalName is RGWSI_SysObj_Cache's normal_name, "<pool>+<ns>+<oid>"
// (services/svc_sys_obj_cache.cc:67-72 at v19.2.6 and v20.2.4). It keys the
// cache and picks the control object a notify goes to.
func normalName(p meta.Pool, oid string) string {
	return p.Name + "+" + p.NS + "+" + oid
}

// cacheInfo is ObjectCacheInfo without the wire form: flags names the parts
// that are set.
type cacheInfo struct {
	status   int32 // 0, or -ENOENT for a negative entry
	flags    uint32
	data     []byte
	xattrs   map[string][]byte
	rmxattrs map[string][]byte // the xattrs a modify-xattrs put removes
	size     uint64
	mtime    time.Time
	version  meta.ObjVersion
}

// clone returns a copy of i sharing nothing with it.
func (i cacheInfo) clone() cacheInfo {
	i.data = slices.Clone(i.data)
	i.xattrs = cloneAttrs(i.xattrs)
	i.rmxattrs = cloneAttrs(i.rmxattrs)
	return i
}

func cloneAttrs(m map[string][]byte) map[string][]byte {
	if m == nil {
		return nil
	}
	c := make(map[string][]byte, len(m))
	for k, v := range m {
		c[k] = slices.Clone(v)
	}
	return c
}

var (
	// errCacheMiss reports a lookup the cache cannot serve.
	errCacheMiss = errors.New("driver: metadata cache miss")
	// errNegativeEntry reports a lookup of an object the cache holds as
	// missing; get wraps it with radosclient.ErrNotFound.
	errNegativeEntry = errors.New("driver: cached negative entry")
)

// cacheEntry is ObjectCacheEntry: an entry, when it was put, and its place
// in the LRU.
type cacheEntry struct {
	name       string
	info       cacheInfo
	added      time.Time
	promoted   uint64 // lru_promotion_ts
	prev, next *cacheEntry
	listed     bool
}

// objectCache is ObjectCache (rgw_cache.cc at v19.2.6 and v20.2.4): an LRU
// that evicts once it holds more than rgw_cache_lru_size entries, expiring
// each rgw_cache_expiry_interval after it was put, with negative entries
// and per-flag partial hits, enabled only while every control watch is
// registered.
type objectCache struct {
	mu      sync.RWMutex
	enabled bool
	entries map[string]*cacheEntry
	lru     lru

	limit   uint64 // rgw_cache_lru_size as the unsigned radosgw compares it as
	window  uint64 // lru_window
	counter uint64 // lru_counter

	expiry     time.Duration // 0 never expires
	domainRoot meta.Pool
	hidden     hiddenPools
	now        func() time.Time
}

// newObjectCache returns a disabled cache of maxSize entries whose entries
// expire after expiry, 0 for never, and which normalizes a notify for an
// object without an oid onto domainRoot.
func newObjectCache(maxSize int, expiry time.Duration, domainRoot meta.Pool, now func() time.Time) *objectCache {
	// touch_lru compares the size with (size_t)rgw_cache_lru_size, and
	// set_ctx keeps half the option in the unsigned lru_window
	// (rgw_cache.cc:246, rgw_cache.h:209). Go's conversions wrap a negative
	// size as C++'s do: it never evicts, and below -1 a hit never promotes.
	return &objectCache{
		entries: map[string]*cacheEntry{},
		limit:   uint64(maxSize),     //nolint:gosec // wraps as radosgw's size_t cast does
		window:  uint64(maxSize / 2), //nolint:gosec // wraps as radosgw's unsigned lru_window does
		expiry:  expiry, domainRoot: domainRoot, now: now,
	}
}

// lookup classifies name's entry with at least the read lock held.
func (c *objectCache) lookup(name string) (e *cacheEntry, expired, promote bool) {
	if !c.enabled {
		return nil, false, false
	}
	e = c.entries[name]
	switch {
	case e == nil:
		return nil, false, false
	case c.expiry > 0 && c.now().Sub(e.added) > c.expiry:
		return e, true, false
	}
	return e, false, c.counter-e.promoted > c.window
}

// get is ObjectCache::get (rgw_cache.cc:13-96): it misses while disabled,
// on an absent or expired entry, which it erases, and on an entry lacking
// one of mask's flags, and it returns a negative entry as errNegativeEntry.
// It takes the write lock only to erase or to promote an entry the LRU has
// moved more than lru_window touches past: half the cache until the first
// flush, and 0 after it (rgw_cache.cc:321-328).
func (c *objectCache) get(name string, mask uint32) (cacheInfo, error) {
	c.mu.RLock()
	e, expired, promote := c.lookup(name)
	if e != nil && !expired && !promote {
		defer c.mu.RUnlock()
		return serve(e.info, mask)
	}
	c.mu.RUnlock()
	if e == nil {
		return cacheInfo{}, errCacheMiss
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	e, expired, promote = c.lookup(name)
	switch {
	case e == nil:
		return cacheInfo{}, errCacheMiss
	case expired:
		c.erase(e)
		return cacheInfo{}, errCacheMiss
	case promote:
		c.touch(e)
	}
	return serve(e.info, mask)
}

// serve answers a lookup from a live entry.
func serve(info cacheInfo, mask uint32) (cacheInfo, error) {
	if info.status == -int32(syscall.ENOENT) {
		return cacheInfo{}, fmt.Errorf("%w: %w", errNegativeEntry, radosclient.ErrNotFound)
	}
	if info.flags&mask != mask {
		return cacheInfo{}, errCacheMiss
	}
	return info.clone(), nil
}

// put is ObjectCache::put (rgw_cache.cc:142-215): it restarts the entry's
// expiry and moves it to the LRU's back, then merges info into it by
// info.flags.
func (c *objectCache) put(name string, info cacheInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return
	}
	e := c.entries[name]
	if e == nil {
		e = &cacheEntry{name: name}
		c.entries[name] = e
	}
	e.added = c.now()
	c.touch(e)

	t := &e.info
	t.status = info.status
	if info.status < 0 {
		t.flags, t.xattrs, t.data = 0, nil, nil
		return
	}
	// A put must carry the version for the entry to keep one.
	t.flags &^= meta.CacheFlagObjVersion
	t.flags |= info.flags
	switch {
	case info.flags&meta.CacheFlagMeta != 0:
		t.size, t.mtime = info.size, info.mtime
	case info.flags&meta.CacheFlagModifyXattrs == 0:
		t.flags &^= meta.CacheFlagMeta
	}
	switch {
	case info.flags&meta.CacheFlagXattrs != 0:
		t.xattrs = cloneAttrs(info.xattrs)
	case info.flags&meta.CacheFlagModifyXattrs != 0:
		if t.xattrs == nil {
			t.xattrs = map[string][]byte{}
		}
		for k := range info.rmxattrs {
			delete(t.xattrs, k)
		}
		maps.Copy(t.xattrs, cloneAttrs(info.xattrs))
	}
	if info.flags&meta.CacheFlagData != 0 {
		t.data = slices.Clone(info.data)
	}
	if info.flags&meta.CacheFlagObjVersion != 0 {
		t.version = info.version
	}
}

// touch is touch_lru (rgw_cache.cc:243-281): it evicts from the front while
// the cache holds more than its limit, stopping at e itself, then moves e to
// the back. Evicting before inserting leaves the cache one entry past its
// limit.
func (c *objectCache) touch(e *cacheEntry) {
	for c.lru.len > c.limit && c.lru.head != e {
		c.erase(c.lru.head)
	}
	c.lru.remove(e)
	c.lru.pushBack(e)
	c.counter++
	e.promoted = c.counter
}

// erase removes e from the cache.
func (c *objectCache) erase(e *cacheEntry) {
	c.lru.remove(e)
	delete(c.entries, e.name)
}

// invalidateRemove is ObjectCache::invalidate_remove (rgw_cache.cc:217-241):
// it removes name's entry and reports whether there was one, and never
// leaves a negative entry behind.
func (c *objectCache) invalidateRemove(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return false
	}
	e := c.entries[name]
	if e == nil {
		return false
	}
	c.erase(e)
	return true
}

// setEnabled is ObjectCache::set_enabled (rgw_cache.cc:303-312); disabling
// flushes every entry. The flush zeroes lru_window as do_invalidate_all
// does (:321-328), so from then on a hit promotes any entry touched since.
func (c *objectCache) setEnabled(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = on
	if !on {
		clear(c.entries)
		c.lru = lru{}
		c.counter, c.window = 0, 0
	}
}

// onNotify handles a control notify by invalidating the object it names,
// whichever op it carries, and never stores its record: radosgw puts an
// UPDATE_OBJ record into its cache unchecked
// (services/svc_sys_obj_cache.cc:489-492 at v19.2.6 and v20.2.4;
// docs/ceph-upstream-bugs.md, "radosgw caches any control-pool UPDATE_OBJ
// notify payload unchecked"). An object without an oid names the pool's
// entry in the domain root, as normalize_pool_and_obj does (:74-83).
func (c *objectCache) onNotify(info meta.CacheNotifyInfo) {
	if info.Op != meta.CacheUpdateObj && info.Op != meta.CacheInvalidateObj {
		slog.Warn("invalidating for a control notify of an unknown op",
			slog.Uint64("op", uint64(info.Op)), slog.String("pool", info.Obj.Pool.String()), c.hidden.attr(info.Obj.Pool, info.Obj.OID))
	}
	pool, oid := info.Obj.Pool, info.Obj.OID
	if oid == "" {
		pool, oid = c.domainRoot, info.Obj.Pool.Name
	}
	c.invalidateRemove(normalName(pool, oid))
}

// lru orders the cache's entries from the next to be evicted, head, to the
// most recently touched, tail.
type lru struct {
	head, tail *cacheEntry
	len        uint64
}

func (l *lru) pushBack(e *cacheEntry) {
	e.prev, e.next, e.listed = l.tail, nil, true
	if l.tail == nil {
		l.head = e
	} else {
		l.tail.next = e
	}
	l.tail = e
	l.len++
}

// remove takes e out of the list; an entry not in it is left alone.
func (l *lru) remove(e *cacheEntry) {
	if !e.listed {
		return
	}
	if e.prev == nil {
		l.head = e.next
	} else {
		e.prev.next = e.next
	}
	if e.next == nil {
		l.tail = e.prev
	} else {
		e.next.prev = e.prev
	}
	e.prev, e.next, e.listed = nil, nil, false
	l.len--
}
