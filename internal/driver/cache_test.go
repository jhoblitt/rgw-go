package driver

import (
	"strconv"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// lruOrder lists the cache's entries from the next to be evicted to the most
// recently touched.
func lruOrder(c *objectCache) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var names []string
	for e := c.lru.head; e != nil; e = e.next {
		names = append(names, e.name)
	}
	return names
}

var _ = Describe("objectCache", func() {
	var (
		now  time.Time
		c    *objectCache
		root meta.Pool
		name string
	)
	BeforeEach(func() {
		root = meta.ParsePool("zone.rgw.meta:root")
		name = normalName(root, "plain")
		now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		c = newObjectCache(3, 900*time.Second, root, func() time.Time { return now })
		c.setEnabled(true)
	})

	It("keys entries by pool, namespace and oid", func() {
		Expect(name).To(Equal("zone.rgw.meta+root+plain"))
		Expect(normalName(meta.ParsePool("zone.rgw.control"), "notify.0")).To(Equal("zone.rgw.control++notify.0"))
	})

	It("misses while disabled, and flushes when disabled", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		c.setEnabled(false)
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		c.setEnabled(true)
		_, err = c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "the flush is not undone by enabling, and a disabled cache stores nothing")
		Expect(c.invalidateRemove(name)).To(BeFalse())
	})

	It("serves only the flags it holds", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagXattrs, xattrs: map[string][]byte{"user.rgw.acl": {1}}})
		_, err := c.get(name, meta.CacheFlagXattrs|meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "type miss, rgw_cache.cc:77-83")
		got, err := c.get(name, meta.CacheFlagXattrs)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.xattrs).To(HaveKeyWithValue("user.rgw.acl", []byte{1}))
	})

	It("returns a negative entry as not found, whatever flags are asked for", func() {
		c.put(name, cacheInfo{status: -int32(syscall.ENOENT)})
		_, err := c.get(name, meta.CacheFlagXattrs)
		Expect(err).To(MatchError(errNegativeEntry))
		Expect(err).To(MatchError(radosclient.ErrNotFound), "callers see the seam's sentinel")
		_, err = c.get(name, 0)
		Expect(err).To(MatchError(errNegativeEntry))
	})

	It("clears the flags, xattrs and data of an entry a negative put replaces", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData | meta.CacheFlagXattrs, data: []byte("d"), xattrs: map[string][]byte{"a": {1}}})
		c.put(name, cacheInfo{status: -int32(syscall.ENOENT)})
		c.put(name, cacheInfo{flags: meta.CacheFlagMeta, size: 1})
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "rgw_cache.cc:170-175 dropped the data")
		got, err := c.get(name, meta.CacheFlagMeta)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.xattrs).To(BeEmpty())
		Expect(got.size).To(BeEquivalentTo(1))
	})

	It("expires an entry once more than rgw_cache_expiry_interval has passed since it was put", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		now = now.Add(900 * time.Second)
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred(), "an entry exactly the interval old still hits, rgw_cache.cc:30-31")
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		now = now.Add(900*time.Second + time.Nanosecond)
		_, err = c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
		Expect(lruOrder(c)).To(BeEmpty(), "the expired entry is erased")
	})

	It("never expires an entry when rgw_cache_expiry_interval is 0", func() {
		c = newObjectCache(3, 0, root, func() time.Time { return now })
		c.setEnabled(true)
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		now = now.Add(100 * 365 * 24 * time.Hour)
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred(), "expiry.count() is 0, rgw_cache.cc:30")
	})

	It("evicts the least recently used entry once it holds more than rgw_cache_lru_size", func() {
		for _, k := range []string{"a", "b", "c"} {
			c.put(normalName(root, k), cacheInfo{flags: meta.CacheFlagData, data: []byte(k)})
		}
		_, err := c.get(normalName(root, "a"), meta.CacheFlagData) // a is now the most recent
		Expect(err).NotTo(HaveOccurred())
		c.put(normalName(root, "d"), cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		Expect(lruOrder(c)).To(HaveLen(4), "touch_lru evicts before it inserts, so the cache holds one entry past the limit, rgw_cache.cc:246-268")
		c.put(normalName(root, "e"), cacheInfo{flags: meta.CacheFlagData, data: []byte("e")})
		_, err = c.get(normalName(root, "b"), meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "b was the oldest")
		_, err = c.get(normalName(root, "a"), meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
	})

	It("promotes a hit only once the entry has fallen more than half the cache behind, until the first flush", func() {
		for _, k := range []string{"a", "b", "c"} {
			c.put(k, cacheInfo{flags: meta.CacheFlagData})
		}
		_, err := c.get("b", meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(lruOrder(c)).To(Equal([]string{"a", "b", "c"}), "one touch behind is within lru_window, 3/2, rgw_cache.cc:51")
		_, err = c.get("a", meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(lruOrder(c)).To(Equal([]string{"b", "c", "a"}))

		c.setEnabled(false)
		c.setEnabled(true)
		for _, k := range []string{"a", "b", "c"} {
			c.put(k, cacheInfo{flags: meta.CacheFlagData})
		}
		_, err = c.get("b", meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(lruOrder(c)).To(Equal([]string{"a", "c", "b"}), "do_invalidate_all zeroes lru_window, rgw_cache.cc:321-328")
	})

	It("does not evict the entry it is touching", func() {
		c = newObjectCache(0, 900*time.Second, root, func() time.Time { return now })
		c.setEnabled(true)
		c.put("a", cacheInfo{flags: meta.CacheFlagData})
		c.put("b", cacheInfo{flags: meta.CacheFlagData})
		Expect(lruOrder(c)).To(Equal([]string{"b"}))
		c.put("b", cacheInfo{flags: meta.CacheFlagMeta})
		Expect(lruOrder(c)).To(Equal([]string{"b"}), "rgw_cache.cc:248-254")
	})

	It("never evicts with a negative rgw_cache_lru_size, which radosgw compares as unsigned", func() {
		c = newObjectCache(-1, 900*time.Second, root, func() time.Time { return now })
		c.setEnabled(true)
		for i := range 10 {
			c.put(strconv.Itoa(i), cacheInfo{flags: meta.CacheFlagData})
		}
		Expect(lruOrder(c)).To(HaveLen(10))
		_, err := c.get("8", meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(lruOrder(c)[9]).To(Equal("8"), "-1/2 is 0 in C++ too, so every hit promotes")

		c = newObjectCache(-4, 900*time.Second, root, func() time.Time { return now })
		c.setEnabled(true)
		for _, k := range []string{"a", "b", "c"} {
			c.put(k, cacheInfo{flags: meta.CacheFlagData})
		}
		_, err = c.get("a", meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(lruOrder(c)).To(Equal([]string{"a", "b", "c"}), "-2 wraps to a window no hit falls past")
	})

	It("merges a modify-xattrs put over the stored set", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagXattrs | meta.CacheFlagMeta, xattrs: map[string][]byte{"a": {1}, "b": {2}}, size: 9})
		c.put(name, cacheInfo{flags: meta.CacheFlagModifyXattrs, xattrs: map[string][]byte{"c": {3}}, rmxattrs: map[string][]byte{"a": nil}})
		got, err := c.get(name, meta.CacheFlagXattrs|meta.CacheFlagMeta)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.xattrs).To(Equal(map[string][]byte{"b": {2}, "c": {3}}), "rgw_cache.cc:198-208")
		Expect(got.size).To(BeEquivalentTo(9), "META survives a modify-xattrs put, :187-190")
	})

	It("drops META on a put that changes something else without it", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData | meta.CacheFlagMeta, data: []byte("d"), size: 1})
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("dd")})
		_, err := c.get(name, meta.CacheFlagMeta)
		Expect(err).To(MatchError(errCacheMiss), "rgw_cache.cc:189-190")
		got, err := c.get(name, meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("dd")))
	})

	It("keeps the version only while every put carries it", func() {
		v := meta.ObjVersion{Ver: 2, Tag: "t"}
		c.put(name, cacheInfo{flags: meta.CacheFlagMeta | meta.CacheFlagObjVersion, version: v})
		got, err := c.get(name, meta.CacheFlagObjVersion)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.version).To(Equal(v))
		c.put(name, cacheInfo{flags: meta.CacheFlagModifyXattrs, xattrs: map[string][]byte{"a": {1}}})
		_, err = c.get(name, meta.CacheFlagObjVersion)
		Expect(err).To(MatchError(errCacheMiss), "a put without the version drops it, rgw_cache.cc:182-183")
		_, err = c.get(name, meta.CacheFlagMeta)
		Expect(err).NotTo(HaveOccurred())
	})

	It("hands out and keeps copies, so a caller's changes do not reach the cache", func() {
		in := cacheInfo{flags: meta.CacheFlagData | meta.CacheFlagXattrs, data: []byte("d"), xattrs: map[string][]byte{"a": {1}}}
		c.put(name, in)
		in.data[0], in.xattrs["a"][0] = 'x', 9
		in.xattrs["b"] = []byte{2}
		got, err := c.get(name, meta.CacheFlagData|meta.CacheFlagXattrs)
		Expect(err).NotTo(HaveOccurred())
		got.data[0] = 'y'
		delete(got.xattrs, "a")
		again, err := c.get(name, meta.CacheFlagData|meta.CacheFlagXattrs)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.data).To(Equal([]byte("d")))
		Expect(again.xattrs).To(Equal(map[string][]byte{"a": {1}}))
	})

	It("never caches a negative on invalidation", func() {
		Expect(c.invalidateRemove(name)).To(BeFalse())
		c.put(name, cacheInfo{flags: meta.CacheFlagData})
		Expect(c.invalidateRemove(name)).To(BeTrue())
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "rgw_cache.cc:217-241")
		Expect(err).NotTo(MatchError(errNegativeEntry))
		Expect(lruOrder(c)).To(BeEmpty())
	})

	It("never stores a notify payload: both ops invalidate and nothing is created", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("stale")})
		c.onNotify(meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "plain"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("forged")},
		})
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "UPDATE_OBJ removed the entry rather than replacing it")
		other := normalName(root, "absent")
		c.onNotify(meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "absent"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("planted")},
		})
		_, err = c.get(other, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "no entry planted from a payload")
		c.onNotify(meta.CacheNotifyInfo{
			Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "absent"},
			ObjInfo: meta.ObjectCacheInfo{Status: -int32(syscall.ENOENT)},
		})
		_, err = c.get(other, meta.CacheFlagData)
		Expect(err).NotTo(MatchError(errNegativeEntry), "no negative entry planted either")
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		c.onNotify(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: root, OID: "plain"}})
		_, err = c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
		Expect(lruOrder(c)).To(BeEmpty())
	})

	It("invalidates on a notify whose op radosgw does not define", func() {
		DeferCleanup(captureLog(GinkgoWriter))
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		c.onNotify(meta.CacheNotifyInfo{Op: 7, Obj: meta.RawObj{Pool: root, OID: "plain"}})
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
	})

	It("normalizes a record without an oid onto the domain root, naming the source pool", func() {
		users := meta.ParsePool("zone.rgw.meta:users.uid")
		key := normalName(root, users.Name)
		Expect(key).To(Equal("zone.rgw.meta+root+zone.rgw.meta"), "the pool's name alone, svc_sys_obj_cache.cc:79-82")
		c.put(key, cacheInfo{flags: meta.CacheFlagData})
		c.onNotify(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: users}})
		_, err := c.get(key, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "svc_sys_obj_cache.cc:74-83")
	})

	It("serves concurrent readers and writers", func() {
		c = newObjectCache(4, 900*time.Second, root, time.Now)
		c.setEnabled(true)
		var wg sync.WaitGroup
		for g := range 8 {
			wg.Go(func() {
				defer GinkgoRecover()
				for i := range 200 {
					k := strconv.Itoa((g + i) % 6)
					switch i % 4 {
					case 0:
						c.put(k, cacheInfo{flags: meta.CacheFlagData, data: []byte(k)})
					case 1:
						c.invalidateRemove(k)
					default:
						if got, err := c.get(k, meta.CacheFlagData); err == nil {
							Expect(got.data).To(Equal([]byte(k)))
						}
					}
				}
			})
		}
		wg.Wait()
		Expect(len(lruOrder(c))).To(BeNumerically("<=", 5))
	})
})
