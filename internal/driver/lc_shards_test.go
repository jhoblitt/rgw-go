package driver

import (
	"context"
	"strconv"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The zone's lc pool in the lifecycle specs, the log pool's lc namespace as
// fix_zone_pool_dup names it.
const (
	lcSpecPool = "zone.rgw.log"
	lcSpecNS   = "lc"
)

// lcSleeper records the waits a Store's lifecycle index asks for, running
// each against its turn, 1 for the first.
type lcSleeper struct {
	waits []time.Duration
	on    func(turn int) error
}

func (f *lcSleeper) sleep(_ context.Context, d time.Duration) error {
	f.waits = append(f.waits, d)
	if f.on != nil {
		return f.on(len(f.waits))
	}
	return nil
}

// newLCStore is a Store holding only what the lifecycle index reads: the lc
// pool, rgw_lc_max_objs shards, a 90 s lock, the cookie and the sleeper.
func newLCStore(c *fakerados.Cluster, shards uint32, cookie string, sleep *lcSleeper) *Store {
	return &Store{
		pools: newPoolCache(c),
		zone:  &zoneConfig{Params: meta.ZoneParams{LCPool: meta.Pool{Name: lcSpecPool, NS: lcSpecNS}}},
		opts:  options{lcMaxObjs: shards, lcLockMaxTime: 90 * time.Second},
		lc:    lcIndex{cookie: cookie, sleep: sleep.sleep},
	}
}

// lcRecord is a BucketRecord for the bucket tenant/name with marker, its key
// all SetEntry and RemoveEntry read.
func lcRecord(tenant, name, marker string) *op.BucketRecord {
	return &op.BucketRecord{Info: meta.BucketInfo{Bucket: meta.BucketID{Tenant: tenant, Name: name, Marker: marker, ID: marker + ".id"}}}
}

// lcExec is the one exec step of w, its class, method and input.
func lcExec(w fakerados.RecordedWrite) radosclient.ExecStep {
	GinkgoHelper()
	steps := w.Steps()
	Expect(steps).To(HaveLen(1))
	step, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "the step is %T", steps[0])
	return *step
}

// lcStoredEntry decodes the entry the shard stores under key.
func lcStoredEntry(c *fakerados.Cluster, oid, key string) rgwcls.LCEntry {
	GinkgoHelper()
	obj := c.Object(lcSpecPool, lcSpecNS, oid)
	Expect(obj).NotTo(BeNil(), oid)
	b, ok := obj.Omap[key]
	Expect(ok).To(BeTrue(), "%s holds no entry under %q", oid, key)
	d := denc.NewDecoder(b)
	e := rgwcls.DecodeLCEntry(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return e
}

var _ = Describe("the lifecycle index", func() {
	const cookie = "Abc-_0123456789"
	var (
		c     *fakerados.Cluster
		sleep *lcSleeper
		s     *Store
		now   time.Time
	)
	BeforeEach(func() {
		c = fakerados.New()
		c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
		c.RegisterClass("lock", fakerados.LockClass(), fakerados.LockWriteMethods...)
		now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return now })
		sleep = &lcSleeper{}
		s = newLCStore(c, 32, cookie, sleep)
	})

	// holdShard takes the shard's lc_process lock as another holder would:
	// under its own cookie, or this gateway's.
	holdShard := func(ctx context.Context, oid, holder string) {
		pool, err := c.Pool(ctx, lcSpecPool, lcSpecNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(pool.LockExclusive(ctx, oid, "lc_process", holder, "", time.Hour, 0)).To(Succeed())
	}
	releaseShard := func(ctx context.Context, oid, holder string) {
		pool, err := c.Pool(ctx, lcSpecPool, lcSpecNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(pool.Unlock(ctx, oid, "lc_process", holder)).To(Succeed())
	}

	DescribeTable("keys a bucket tenant:name:marker and files it on the shard ceph_str_hash_linux(key) % 7877 % rgw_lc_max_objs",
		func(ctx SpecContext, tenant, name, marker string, shards, want uint32) {
			key := tenant + ":" + name + ":" + marker
			Expect(lcBucketKey(lcRecord(tenant, name, marker).Info.Bucket)).To(Equal(key))
			Expect(lcShardIndex(key, shards)).To(Equal(want))
			Expect(want).To(Equal(meta.StrHashLinux(key)%7877%shards), "the vector is get_lc_index's")
			s.opts.lcMaxObjs = shards
			Expect(s.SetEntry(ctx, lcRecord(tenant, name, marker))).To(Succeed())
			oid := "lc." + strconv.FormatUint(uint64(want), 10)
			Expect(lcStoredEntry(c, oid, key)).To(Equal(rgwcls.LCEntry{Bucket: key}))
			Expect(c.Objects(lcSpecPool, lcSpecNS)).To(HaveLen(1), "only %s is written", oid)
		},
		Entry("no tenant, 32 shards", "", "bucket", "a1b2c3d4-0000-4000-8000-000000000000.4137.1", uint32(32), uint32(14)),
		Entry("a tenant, 32 shards", "tenant", "photos", "a1b2c3d4-0000-4000-8000-000000000000.4137.2", uint32(32), uint32(17)),
		Entry("a short key, 32 shards", "", "b", "m", uint32(32), uint32(17)),
		Entry("5 shards", "", "lc-bucket", "ceph-objectstore.4242.7", uint32(5), uint32(4)),
		Entry("one shard", "tenant", "photos", "a1b2c3d4-0000-4000-8000-000000000000.4137.2", uint32(1), uint32(0)),
		Entry("7877 shards, where the prime alone reduces", "", "bucket", "a1b2c3d4-0000-4000-8000-000000000000.4137.1", uint32(7877), uint32(5742)),
	)

	It("names the shards lc.0 to lc.<n-1>", func() {
		Expect(lcShardOID(0)).To(Equal("lc.0"))
		Expect(lcShardOID(31)).To(Equal("lc.31"))
		Expect(lcShardOID(7876)).To(Equal("lc.7876"))
	})

	Describe("SetEntry", func() {
		const key, oid = ":b:m", "lc.17"

		It("writes {key, 0, uninitial} with lc_set_entry under the shard's lc_process lock, then unlocks", func(ctx SpecContext) {
			var during []radosclient.Locker
			var expiry time.Time
			c.BeforeWrite(lcSpecPool, lcSpecNS, oid, func(o *fakerados.Object) {
				during = c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")
				d := denc.NewDecoder(o.Xattrs["lock.lc_process"])
				for _, li := range lock.DecodeInfo(d).Lockers {
					expiry = li.Expiration
				}
				Expect(d.Err()).NotTo(HaveOccurred())
			})
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(Succeed())
			Expect(during).To(ConsistOf(HaveField("Cookie", cookie)), "the lock is held while the entry is written")
			Expect(expiry).To(Equal(now.Add(90*time.Second)), "for rgw_lc_lock_max_time")
			Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(BeEmpty(), "then released")

			writes := c.WritesTo(lcSpecPool, lcSpecNS, oid)
			Expect(writes).To(HaveLen(1))
			step := lcExec(writes[0])
			Expect([]string{step.Class, step.Method}).To(Equal([]string{"rgw", "lc_set_entry"}))
			Expect(step.In).To(Equal([]byte{
				2, 2, 26, 0, 0, 0, // cls_rgw_lc_set_entry_op, ENCODE_START(2, 2)
				1, 1, 20, 0, 0, 0, // cls_rgw_lc_entry, ENCODE_START(1, 1)
				4, 0, 0, 0, ':', 'b', ':', 'm', // bucket
				0, 0, 0, 0, 0, 0, 0, 0, // start_time
				0, 0, 0, 0, // status, lc_uninitial
			}))
			Expect(lcStoredEntry(c, oid, key)).To(Equal(rgwcls.LCEntry{Bucket: key, Status: rgwcls.LCStatusUninitial}))
			Expect(sleep.waits).To(BeEmpty())
		})

		It("resets an entry the shard holds to {key, 0, uninitial}", func(ctx SpecContext) {
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(Succeed())
			c.Object(lcSpecPool, lcSpecNS, oid).Omap[key] = encodeLC(rgwcls.LCEntry{Bucket: key, StartTime: 7, Status: rgwcls.LCStatusComplete})
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(Succeed())
			Expect(lcStoredEntry(c, oid, key)).To(Equal(rgwcls.LCEntry{Bucket: key}))
		})

		It("takes the lock for rgw_lc_lock_max_time as read", func(ctx SpecContext) {
			s.opts.lcLockMaxTime = 7 * time.Second
			var expiry time.Time
			c.BeforeWrite(lcSpecPool, lcSpecNS, oid, func(o *fakerados.Object) {
				for _, li := range lock.DecodeInfo(denc.NewDecoder(o.Xattrs["lock.lc_process"])).Lockers {
					expiry = li.Expiration
				}
			})
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(Succeed())
			Expect(expiry).To(Equal(now.Add(7 * time.Second)))
		})

		It("retries a shard another holder has locked every 100 ms until it is free", func(ctx SpecContext) {
			holdShard(ctx, oid, "lc_thrd: 0")
			sleep.on = func(turn int) error {
				if turn == 3 {
					releaseShard(ctx, oid, "lc_thrd: 0")
				}
				return nil
			}
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(Succeed())
			Expect(sleep.waits).To(Equal([]time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}))
			Expect(lcStoredEntry(c, oid, key)).To(Equal(rgwcls.LCEntry{Bucket: key}))
			Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(BeEmpty())
		})

		It("retries EEXIST, the lock this gateway's own cookie holds, the same way", func(ctx SpecContext) {
			holdShard(ctx, oid, cookie)
			sleep.on = func(turn int) error {
				if turn == 2 {
					releaseShard(ctx, oid, cookie)
				}
				return nil
			}
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(Succeed())
			Expect(sleep.waits).To(HaveLen(2))
			Expect(lcStoredEntry(c, oid, key)).To(Equal(rgwcls.LCEntry{Bucket: key}))
		})

		DescribeTable("fails with the lock's error after 500 retries, 501 tries each followed by 100 ms, writing nothing and leaving the holder's lock",
			func(ctx SpecContext, holder string, want syscall.Errno) {
				holdShard(ctx, oid, holder)
				err := s.SetEntry(ctx, lcRecord("", "b", "m"))
				Expect(errno(err)).To(Equal(want), "%v", err)
				Expect(err).To(MatchError(ContainSubstring(oid)))
				Expect(sleep.waits).To(HaveLen(501), "the waits for a locked shard")
				Expect(sleep.waits).To(HaveEach(100 * time.Millisecond))
				Expect(c.WritesTo(lcSpecPool, lcSpecNS, oid)).To(BeEmpty())
				Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(ConsistOf(HaveField("Cookie", holder)))
			},
			Entry("EBUSY, another holder's lock", "lc_thrd: 1", syscall.EBUSY),
			Entry("EEXIST, this gateway's own", cookie, syscall.EEXIST),
		)

		It("fails at once on a lock error other than EBUSY or EEXIST", func(ctx SpecContext) {
			c.RegisterClass("lock", func(*fakerados.ClassCall) ([]byte, int32) { return nil, -int32(syscall.EIO) }, fakerados.LockWriteMethods...)
			err := s.SetEntry(ctx, lcRecord("", "b", "m"))
			Expect(errno(err)).To(Equal(syscall.EIO), "%v", err)
			Expect(sleep.waits).To(BeEmpty())
			Expect(c.WritesTo(lcSpecPool, lcSpecNS, oid)).To(BeEmpty())
		})

		It("stops waiting when ctx ends, writing nothing", func(ctx SpecContext) {
			holdShard(ctx, oid, "lc_thrd: 2")
			sleep.on = func(int) error { return context.Canceled }
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(MatchError(context.Canceled))
			Expect(sleep.waits).To(HaveLen(1))
			Expect(c.WritesTo(lcSpecPool, lcSpecNS, oid)).To(BeEmpty())
		})

		It("unlocks after a failed write and returns the write's error", func(ctx SpecContext) {
			c.FailNextWrite(lcSpecPool, lcSpecNS, oid, syscall.ENOSPC)
			err := s.SetEntry(ctx, lcRecord("", "b", "m"))
			Expect(err).To(MatchError(radosclient.ErrNoSpace))
			Expect(err).To(MatchError(ContainSubstring(key)))
			Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(BeEmpty())
		})

		It("unlocks though ctx ends while the entry is written, so the shard is not held for rgw_lc_lock_max_time", func(ctx SpecContext) {
			wctx, cancel := context.WithCancel(ctx)
			c.BeforeWrite(lcSpecPool, lcSpecNS, oid, func(*fakerados.Object) { cancel() })
			Expect(s.SetEntry(wctx, lcRecord("", "b", "m"))).To(Succeed())
			Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(BeEmpty())
		})

		It("fails, naming the pool, when the lc pool cannot be opened", func(ctx SpecContext) {
			c.FailPool(lcSpecPool)
			Expect(s.SetEntry(ctx, lcRecord("", "b", "m"))).To(MatchError(ContainSubstring(lcSpecPool)))
		})
	})

	Describe("RemoveEntry", func() {
		const key, other, oid = ":b:m", "tenant:photos:a1b2c3d4-0000-4000-8000-000000000000.4137.2", "lc.17"
		var rec *op.BucketRecord
		BeforeEach(func() { rec = lcRecord("", "b", "m") })

		It("removes the key's entry with lc_rm_entry carrying {key, 0, uninitial} under the lock, keeping the shard's others", func(ctx SpecContext) {
			Expect(s.SetEntry(ctx, rec)).To(Succeed())
			Expect(s.SetEntry(ctx, lcRecord("tenant", "photos", "a1b2c3d4-0000-4000-8000-000000000000.4137.2"))).To(Succeed())
			c.ResetCounters()
			var during []radosclient.Locker
			c.BeforeWrite(lcSpecPool, lcSpecNS, oid, func(*fakerados.Object) {
				during = c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")
			})
			Expect(s.RemoveEntry(ctx, rec)).To(Succeed())
			Expect(during).To(ConsistOf(HaveField("Cookie", cookie)))
			Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(BeEmpty())

			writes := c.WritesTo(lcSpecPool, lcSpecNS, oid)
			Expect(writes).To(HaveLen(1))
			step := lcExec(writes[0])
			Expect([]string{step.Class, step.Method}).To(Equal([]string{"rgw", "lc_rm_entry"}))
			Expect(step.In).To(Equal([]byte{
				2, 2, 26, 0, 0, 0, // cls_rgw_lc_rm_entry_op, ENCODE_START(2, 2)
				1, 1, 20, 0, 0, 0, // cls_rgw_lc_entry, ENCODE_START(1, 1)
				4, 0, 0, 0, ':', 'b', ':', 'm', // bucket
				0, 0, 0, 0, 0, 0, 0, 0, // start_time
				0, 0, 0, 0, // status, lc_uninitial
			}))
			Expect(c.Object(lcSpecPool, lcSpecNS, oid).Omap).To(HaveKey(other))
			Expect(c.Object(lcSpecPool, lcSpecNS, oid).Omap).NotTo(HaveKey(key))
		})

		It("succeeds for a bucket its shard holds no entry for", func(ctx SpecContext) {
			Expect(s.RemoveEntry(ctx, rec)).To(Succeed())
			Expect(c.WritesTo(lcSpecPool, lcSpecNS, oid)).To(HaveLen(1))
			Expect(c.Object(lcSpecPool, lcSpecNS, oid).Omap).To(BeEmpty())
		})

		It("retries a locked shard as SetEntry does, and fails with the lock's error after 500 retries", func(ctx SpecContext) {
			holdShard(ctx, oid, "lc_thrd: 0")
			err := s.RemoveEntry(ctx, rec)
			Expect(errno(err)).To(Equal(syscall.EBUSY), "%v", err)
			Expect(sleep.waits).To(HaveLen(501), "the waits for a locked shard")
			Expect(c.WritesTo(lcSpecPool, lcSpecNS, oid)).To(BeEmpty())
		})
	})

	Describe("the shard helpers", func() {
		const oid = "lc.3"
		var pool radosclient.Pool
		BeforeEach(func(ctx SpecContext) {
			var err error
			pool, err = s.lcPool(ctx)
			Expect(err).NotTo(HaveOccurred())
		})

		It("reads a head lc_put_head wrote, and answers ENOENT for a shard that does not exist", func(ctx SpecContext) {
			_, err := lcGetHead(ctx, pool, oid)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			h := rgwcls.LCHead{StartDate: 1760961600, Marker: ":b:m", ShardRolloverDate: 1760918400}
			Expect(lcPutHead(ctx, pool, oid, h)).To(Succeed())
			Expect(lcGetHead(ctx, pool, oid)).To(Equal(h))
		})

		It("sets, reads, steps through, lists and removes a shard's entries", func(ctx SpecContext) {
			a := rgwcls.LCEntry{Bucket: ":a:1", StartTime: 1760961600, Status: rgwcls.LCStatusProcessing}
			z := rgwcls.LCEntry{Bucket: ":z:1", Status: rgwcls.LCStatusComplete}
			Expect(lcSetEntry(ctx, pool, oid, z)).To(Succeed())
			Expect(lcSetEntry(ctx, pool, oid, a)).To(Succeed())
			Expect(lcGetEntry(ctx, pool, oid, ":a:1")).To(Equal(a))
			_, err := lcGetEntry(ctx, pool, oid, ":m:1")
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(lcGetNextEntry(ctx, pool, oid, "")).To(Equal(a))
			Expect(lcGetNextEntry(ctx, pool, oid, ":a:1")).To(Equal(z))
			Expect(lcGetNextEntry(ctx, pool, oid, ":z:1")).To(Equal(rgwcls.LCEntry{}), "past the end, the empty entry")
			_, err = lcGetNextEntry(ctx, pool, "lc.4", "")
			Expect(err).To(MatchError(radosclient.ErrNotFound), "a shard that does not exist")
			page, err := lcListEntries(ctx, pool, oid, "", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Entries).To(Equal([]rgwcls.LCEntry{a}))
			Expect(page.Truncated).To(BeTrue())
			page, err = lcListEntries(ctx, pool, oid, ":a:1", 5)
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Entries).To(Equal([]rgwcls.LCEntry{z}))
			Expect(page.Truncated).To(BeFalse())
			Expect(lcRmEntry(ctx, pool, oid, a)).To(Succeed())
			Expect(lcGetNextEntry(ctx, pool, oid, "")).To(Equal(z))
		})

		It("unlocks under the cookie it is given", func(ctx SpecContext) {
			holdShard(ctx, oid, "lc_thrd: 4")
			unlockLC(ctx, pool, oid, "lc_thrd: 4")
			Expect(c.Locks(lcSpecPool, lcSpecNS, oid, "lc_process")).To(BeEmpty())
		})
	})

	It("is given one cookie per Store, 15 characters of gen_rand_alphanumeric's table, as RGWLC::initialize draws it", func() {
		a, b := newLCIndex(), newLCIndex()
		Expect(a.cookie).To(MatchRegexp(`^[A-Za-z0-9_-]{15}$`))
		Expect(b.cookie).To(MatchRegexp(`^[A-Za-z0-9_-]{15}$`))
		Expect(a.cookie).NotTo(Equal(b.cookie))
	})
})

// lcEncodable is a cls_rgw lc struct.
type lcEncodable interface {
	Encode(e *denc.Encoder, r denc.Release)
}

// encodeLC renders v as the class reads it.
func encodeLC(v lcEncodable) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}
