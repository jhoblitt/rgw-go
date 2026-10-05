package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("gc worker", func() {
	// tag is the GC tag of the object queueTails overwrites: its tail_tag's
	// bytes, NUL included.
	const tag = "tx-put\x00"
	var (
		c     *fakerados.Cluster
		clock *fakeClock
		s     *driver.Store
		g     *driver.GCWorker
		rec   *op.BucketRecord
		kv    map[string]string
		t0    time.Time
	)
	BeforeEach(func() {
		t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		clock = newFakeClock(t0)
		c = newPutCluster()
		c.SetClock(clock.Now)
		kv = map[string]string{"rgw_gc_max_objs": "4"}
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
	})
	// reopen opens s over c with the options kv, and g, its worker.
	reopen := func(ctx context.Context) {
		GinkgoHelper()
		rel := denc.Squid
		var err error
		s, err = driver.Open(ctx, c, conf(kv), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, clock.Now)
		s.SetRandForTest(fixedRand(putPrefix))
		g = s.GCWorkerForTest()
		g.SetNow(clock.Now)
	}
	JustBeforeEach(func(ctx SpecContext) { reopen(ctx) })
	gcPool := func(ctx context.Context) radosclient.Pool {
		GinkgoHelper()
		p, err := c.Pool(ctx, gcPoolName, gcNS)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	gcWrite := func(ctx context.Context, oid string, build func(w *radosclient.WriteOp)) {
		GinkgoHelper()
		w := radosclient.NewWriteOp()
		build(w)
		_, err := gcPool(ctx).Write(ctx, oid, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
	}
	// chain stores objects named oids in the data pool and returns them as
	// a GC chain.
	chain := func(oids ...string) []rgwcls.GCObj {
		var out []rgwcls.GCObj
		for _, oid := range oids {
			c.Put(testDataPool, "", oid, []byte("tail"))
			out = append(out, rgwcls.GCObj{Pool: testDataPool, Key: rgwcls.ObjKey{Name: oid}})
		}
		return out
	}
	// enqueue queues an entry for the chain under t on the rgw_gc queue of
	// shard, due now.
	enqueue := func(ctx context.Context, shard, t string, ch []rgwcls.GCObj) {
		GinkgoHelper()
		gcWrite(ctx, shard, func(w *radosclient.WriteOp) {
			gc.QueueEnqueue(w, 0, rgwcls.GCObjInfo{Tag: t, Chain: ch}, denc.Squid)
		})
	}
	// queueTails writes a 10 MiB object under the tag tx-put and overwrites
	// it, which queues its two tails; it returns their gc shard and oids.
	queueTails := func(ctx context.Context) (shard string, tails []string) {
		GinkgoHelper()
		attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
		key := meta.ObjKey{Name: "k"}
		body := bytes.Repeat([]byte("g"), 10<<20)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(body), op.PutParams{Attrs: attrs, Size: int64(len(body)), Tag: "tx-put"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		s.SetRandForTest(fixedRand(secondPrefix))
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		shard = fmt.Sprintf("gc.%d", driver.GCShardForTest(s, tag))
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(HaveLen(1))
		tails = []string{tailOID(putPrefix, 1), tailOID(putPrefix, 2)}
		for _, t := range tails {
			Expect(c.Object(testDataPool, "", t)).NotTo(BeNil())
		}
		return shard, tails
	}

	It("initializes every shard with create, version check 0, queue init and version set 1", func(ctx SpecContext) {
		Expect(c.Writes(gcPoolName, gcNS, "gc.0")).To(Equal(1), "Open initializes the shards")
		Expect(c.Writes(gcPoolName, gcNS, "gc.4")).To(BeZero(), "rgw_gc_max_objs 4")
		for i := range 4 {
			c.Remove(gcPoolName, gcNS, fmt.Sprintf("gc.%d", i))
		}
		Expect(s.GCInitializeForTest(ctx)).To(Succeed())
		for i := range 4 {
			oid := fmt.Sprintf("gc.%d", i)
			w := c.LastWrite(gcPoolName, gcNS, oid)
			Expect(w.Steps()).To(HaveLen(4))
			Expect(w.Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
			Expect(execIn(w, 1, "check_conds", version.DecodeCheckOp).Conds).
				To(Equal([]version.Condition{{Ver: version.ObjVersion{}, Cond: version.CondEQ}}), "gc_log_init2, rgw_gc_log.cc:11-19")
			Expect(execIn(w, 2, "rgw_gc_queue_init", gc.DecodeQueueInitOp)).To(Equal(gc.QueueInitOp{Size: 131068 << 10, NumDeferredEntries: 50}))
			Expect(execIn(w, 3, "set", version.DecodeSetOp).Objv).To(Equal(version.ObjVersion{Ver: 1}))
			obj := c.Object(gcPoolName, gcNS, oid)
			Expect(obj).NotTo(BeNil())
			Expect(version.DecodeObjVersion(denc.NewDecoder(obj.Xattrs[version.XattrName]))).To(Equal(version.ObjVersion{Ver: 1}))
		}
		before := c.Object(gcPoolName, gcNS, "gc.0").Version
		Expect(s.GCInitializeForTest(ctx)).To(Succeed(), "a second run's ECANCELED is ignored")
		Expect(c.Object(gcPoolName, gcNS, "gc.0").Version).To(Equal(before))
	})

	It("frees the tails of an expired queue-era entry and removes the entry", func(ctx SpecContext) {
		shard, tails := queueTails(ctx)
		var mu sync.Mutex
		var held [][]radosclient.Locker
		for _, t := range tails {
			c.BeforeWrite(testDataPool, "", t, func(*fakerados.Object) {
				mu.Lock()
				held = append(held, c.Locks(gcPoolName, gcNS, shard, "gc_process"))
				mu.Unlock()
			})
		}
		c.ResetCounters()
		Expect(g.Process(ctx, true)).To(Succeed())
		for _, t := range tails {
			Expect(c.Object(testDataPool, "", t)).NotTo(BeNil(), "not yet due")
		}
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(HaveLen(1))
		Expect(c.Writes(gcPoolName, gcNS, shard)).To(BeZero())

		clock.Advance(2 * time.Hour)
		Expect(g.Process(ctx, true)).To(Succeed())
		for _, t := range tails {
			Expect(c.Object(testDataPool, "", t)).To(BeNil(), "the wildcard ref dropped, the object removed")
			w := c.LastWrite(testDataPool, "", t)
			Expect(w.Steps()).To(HaveLen(1))
			Expect(execIn(w, 0, "put", refcount.DecodePutOp)).To(Equal(refcount.PutOp{Tag: tag, ImplicitRef: true}), "rgw_gc.cc:683-684")
			Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), "set_pool_full_try, rgw_gc.cc:677")
		}
		Expect(held).To(Equal([][]radosclient.Locker{{{Client: "client.4155"}}, {{Client: "client.4155"}}}), "gc_process held, cookie empty")
		Expect(c.Locks(gcPoolName, gcNS, shard, "gc_process")).To(BeEmpty(), "and released")
		writes := c.WritesTo(gcPoolName, gcNS, shard)
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Steps()).To(HaveLen(1))
		Expect(execIn(writes[0], 0, "rgw_gc_queue_remove_entries", gc.DecodeQueueRemoveEntriesOp).NumEntries).To(BeEquivalentTo(1))
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(BeEmpty())
	})

	It("frees the tails a DELETE queues once they are due", func(ctx SpecContext) {
		attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
		key := meta.ObjKey{Name: "gone"}
		body := bytes.Repeat([]byte("d"), 10<<20)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(body), op.PutParams{Attrs: attrs, Size: int64(len(body)), Tag: "tx-put"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		settle(s)
		shard := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, tag))
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(HaveLen(1))
		tails := []string{tailOID(putPrefix, 1), tailOID(putPrefix, 2)}
		clock.Advance(2 * time.Hour)
		Expect(g.Process(ctx, true)).To(Succeed())
		for _, t := range tails {
			Expect(c.Object(testDataPool, "", t)).To(BeNil())
		}
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(BeEmpty())
	})

	It("removes nothing from a queue page whose drain the gateway's stop cut short, and still unlocks", func(ctx SpecContext) {
		enqueue(ctx, "gc.0", "q", chain("c1", "c2"))
		clock.Advance(time.Second)
		pass, stop := context.WithCancel(ctx)
		defer stop()
		c.BeforeWrite(testDataPool, "", "c1", func(*fakerados.Object) { stop() })
		c.ResetCounters()
		Expect(g.ProcessShardForTest(pass, 0, time.Hour, true)).To(Succeed())
		Expect(c.GCEntries(gcPoolName, gcNS, "gc.0")).To(HaveLen(1), "drain_ios answers -EAGAIN going down, rgw_gc.cc:472-484 and :703-707")
		Expect(c.Writes(gcPoolName, gcNS, "gc.0")).To(BeZero())
		Expect(c.Locks(gcPoolName, gcNS, "gc.0", "gc_process")).To(BeEmpty())
	})

	It("ends the pass on its own lock still held, which the lock class answers EEXIST", func(ctx SpecContext) {
		Expect(gcPool(ctx).LockExclusive(ctx, "gc.0", "gc_process", "", "", time.Hour, 0)).To(Succeed())
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 0, time.Hour, true)).To(MatchError(radosclient.ErrExists), "rgw_gc.cc:577-578 returns it, ending process(bool) at :739-741")
		Expect(c.Reads(gcPoolName, gcNS, "gc.0")).To(BeZero())
	})

	It("puts the tag the entry recorded, so a tail another object still references survives", func(ctx SpecContext) {
		shard, tails := queueTails(ctx)
		c.Object(testDataPool, "", tails[0]).Xattrs[refcount.XattrName] = encode(refcount.Refcount{Refs: map[string]bool{tag: true, "tx-copy\x00": true}})
		clock.Advance(2 * time.Hour)
		Expect(g.Process(ctx, true)).To(Succeed())
		kept := c.Object(testDataPool, "", tails[0])
		Expect(kept).NotTo(BeNil())
		rc := refcount.DecodeRefcount(denc.NewDecoder(kept.Xattrs[refcount.XattrName]))
		Expect(rc.Refs).To(Equal(map[string]bool{"tx-copy\x00": true}))
		Expect(c.Object(testDataPool, "", tails[1])).To(BeNil())
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(BeEmpty())
	})

	It("removes queue entries by count, so an entry not yet due ahead of a due one goes uncollected, as in radosgw", func(ctx SpecContext) {
		gcWrite(ctx, "gc.0", func(w *radosclient.WriteOp) {
			gc.QueueEnqueue(w, 7200, rgwcls.GCObjInfo{Tag: "later", Chain: chain("t7")}, denc.Squid)
		})
		enqueue(ctx, "gc.0", "due", chain("t8"))
		clock.Advance(time.Second)
		Expect(g.ProcessShardForTest(ctx, 0, time.Hour, true)).To(Succeed())
		Expect(c.Object(testDataPool, "", "t8")).To(BeNil())
		Expect(c.Object(testDataPool, "", "t7")).NotTo(BeNil(), "never put: docs/ceph-upstream-bugs.md")
		entries := c.GCEntries(gcPoolName, gcNS, "gc.0")
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal("due"), "rgw_gc_queue_remove_entries 1 took the oldest, cls_rgw_gc.cc:227-375")
	})

	Describe("with a negative rgw_gc_obj_min_wait", func() {
		BeforeEach(func() { kv["rgw_gc_obj_min_wait"] = "-1" })

		It("queues the tails due after the default wait, where radosgw's due time wraps into the past", func(ctx SpecContext) {
			shard, tails := queueTails(ctx)
			Expect(c.GCEntries(gcPoolName, gcNS, shard)[0].Time).To(Equal(t0.Add(2*time.Hour)),
				"radosgw sends 2^32-1 s, which encode(real_time)'s 32-bit seconds wrap to one second ago (docs/ceph-upstream-bugs.md)")
			Expect(g.Process(ctx, true)).To(Succeed())
			for _, t := range tails {
				Expect(c.Object(testDataPool, "", t)).NotTo(BeNil(), "readers keep the tails until the wait is over")
			}
			clock.Advance(2 * time.Hour)
			Expect(g.Process(ctx, true)).To(Succeed())
			for _, t := range tails {
				Expect(c.Object(testDataPool, "", t)).To(BeNil())
			}
		})
	})

	// t0Unix is the specs' t0, 2026-10-05T12:00:00Z, in Unix seconds.
	const t0Unix = 1791201600
	DescribeTable("saturates a wait reaching past 2106 a day short of it, where radosgw's due time wraps into the past",
		func(ctx SpecContext, wait string) {
			kv["rgw_gc_obj_min_wait"] = wait
			reopen(ctx)
			shard, tails := queueTails(ctx)
			due := time.Unix(math.MaxUint32-24*60*60, 0).UTC()
			Expect(c.GCEntries(gcPoolName, gcNS, shard)[0].Time).To(Equal(due), "docs/ceph-upstream-bugs.md")
			Expect(g.Process(ctx, true)).To(Succeed())
			for _, t := range tails {
				Expect(c.Object(testDataPool, "", t)).NotTo(BeNil())
			}
		},
		Entry("MaxInt64", strconv.FormatInt(math.MaxInt64, 10)),
		Entry("the first wait whose due time overflows 32 bits", strconv.FormatInt(1<<32-t0Unix, 10)),
	)

	Describe("an omap-era shard", func() {
		// seedOmap stores an omap-era entry for the chain under t on gc.1, due
		// now, and leaves the shard at cls version 0, as a shard
		// RGWGC::initialize never converted.
		seedOmap := func(ctx context.Context, t string, ch []rgwcls.GCObj) {
			GinkgoHelper()
			gcWrite(ctx, "gc.1", func(w *radosclient.WriteOp) {
				rgwcls.GCSetEntry(w, 0, rgwcls.GCObjInfo{Tag: t, Chain: ch}, denc.Squid)
			})
			delete(c.Object(gcPoolName, gcNS, "gc.1").Xattrs, version.XattrName)
		}

		It("processes it through gc_list and removes the tag once its objects are gone", func(ctx SpecContext) {
			seedOmap(ctx, "omap-tag", chain("t1", "t2"))
			clock.Advance(time.Second)
			c.ResetCounters()
			Expect(g.Process(ctx, true)).To(Succeed())
			Expect(c.Object(testDataPool, "", "t1")).To(BeNil())
			Expect(c.Object(testDataPool, "", "t2")).To(BeNil())
			Expect(g.TransitionedForTest(1)).To(BeFalse())
			writes := c.WritesTo(gcPoolName, gcNS, "gc.1")
			Expect(writes).To(HaveLen(1), "the tag removal, flushed by the drain")
			Expect(execIn(writes[0], 0, "gc_remove", rgwcls.DecodeGCRemoveOp).Tags).To(Equal([]string{"omap-tag"}))
			Expect(c.Object(gcPoolName, gcNS, "gc.1").Omap).To(BeEmpty())
		})

		It("keeps the tag while one of its objects is left, sending the drain's removal empty", func(ctx SpecContext) {
			seedOmap(ctx, "omap-tag", chain("t1", "t2"))
			c.FailNextWrite(testDataPool, "", "t2", syscall.EIO)
			clock.Advance(time.Second)
			c.ResetCounters()
			Expect(g.Process(ctx, true)).To(Succeed())
			Expect(c.Object(testDataPool, "", "t1")).To(BeNil())
			Expect(c.Object(testDataPool, "", "t2")).NotTo(BeNil())
			writes := c.WritesTo(gcPoolName, gcNS, "gc.1")
			Expect(writes).To(HaveLen(1), "flush_remove_tags of every unconverted shard, rgw_gc.cc:525-533")
			Expect(execIn(writes[0], 0, "gc_remove", rgwcls.DecodeGCRemoveOp).Tags).To(BeEmpty())
			Expect(c.Object(gcPoolName, gcNS, "gc.1").Omap).To(HaveKey("0_omap-tag"))
		})

		It("keeps the tag of an object whose pool cannot be opened, freeing the rest", func(ctx SpecContext) {
			c.FailPool("gone.data")
			ch := append([]rgwcls.GCObj{{Pool: "gone.data", Key: rgwcls.ObjKey{Name: "lost"}}}, chain("t1")...)
			seedOmap(ctx, "omap-tag", ch)
			clock.Advance(time.Second)
			Expect(g.Process(ctx, true)).To(Succeed())
			Expect(c.Object(testDataPool, "", "t1")).To(BeNil(), "the omap era goes on past the pool, rgw_gc.cc:665-672")
			Expect(c.Object(gcPoolName, gcNS, "gc.1").Omap).To(HaveKey("0_omap-tag"), "one object still counted")
		})

		Context("with rgw_gc_max_trim_chunk 2", func() {
			BeforeEach(func() { kv["rgw_gc_max_trim_chunk"] = "2" })

			It("removes the freed tags two at a time during the pass, and the rest at the drain", func(ctx SpecContext) {
				for i, t := range []string{"a", "b", "c"} {
					gcWrite(ctx, "gc.1", func(w *radosclient.WriteOp) {
						rgwcls.GCSetEntry(w, uint32(i), rgwcls.GCObjInfo{Tag: t}, denc.Squid) //nolint:gosec // 0 to 2
					})
				}
				delete(c.Object(gcPoolName, gcNS, "gc.1").Xattrs, version.XattrName)
				clock.Advance(3 * time.Second)
				c.ResetCounters()
				Expect(g.Process(ctx, true)).To(Succeed())
				var removed [][]string
				for _, w := range c.WritesTo(gcPoolName, gcNS, "gc.1") {
					removed = append(removed, execIn(w, 0, "gc_remove", rgwcls.DecodeGCRemoveOp).Tags)
				}
				Expect(removed).To(Equal([][]string{{"a", "b"}, {"c"}}), "schedule_tag_removal's flush, rgw_gc.cc:462-464, then the drain's")
				Expect(c.Object(gcPoolName, gcNS, "gc.1").Omap).To(BeEmpty())
			})
		})

		It("removes an entry without objects through the drain", func(ctx SpecContext) {
			seedOmap(ctx, "empty-tag", nil)
			clock.Advance(time.Second)
			Expect(g.Process(ctx, true)).To(Succeed())
			Expect(c.Object(gcPoolName, gcNS, "gc.1").Omap).To(BeEmpty(), "schedule_tag_removal of an empty chain, rgw_gc.cc:650-651")
		})
	})

	It("detects a converted shard by version 1 with no omap entries and then lists the queue", func(ctx SpecContext) {
		enqueue(ctx, "gc.2", "q", chain("t3"))
		clock.Advance(time.Second)
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 2, time.Hour, true)).To(Succeed())
		Expect(c.Reads(gcPoolName, gcNS, "gc.2")).To(Equal(4), "gc_list, the cls_version read, gc_list(1, false), then the queue")
		last := c.LastRead(gcPoolName, gcNS, "gc.2").Steps()
		Expect(last).To(HaveLen(1))
		Expect(last[0]).To(HaveField("Method", "rgw_gc_queue_list_entries"))
		Expect(g.TransitionedForTest(2)).To(BeTrue())
		Expect(c.Object(testDataPool, "", "t3")).To(BeNil())
		Expect(c.GCEntries(gcPoolName, gcNS, "gc.2")).To(BeEmpty())

		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 2, time.Hour, true)).To(Succeed())
		Expect(c.Reads(gcPoolName, gcNS, "gc.2")).To(Equal(1), "only the queue once converted")
	})

	It("leaves a version 1 shard on its omap log while it holds an entry not yet due", func(ctx SpecContext) {
		gcWrite(ctx, "gc.2", func(w *radosclient.WriteOp) {
			rgwcls.GCSetEntry(w, 60, rgwcls.GCObjInfo{Tag: "later", Chain: chain("t4")}, denc.Squid)
		})
		enqueue(ctx, "gc.2", "q", chain("t3"))
		clock.Advance(time.Second)
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 2, time.Hour, true)).To(Succeed())
		Expect(c.Reads(gcPoolName, gcNS, "gc.2")).To(Equal(3), "rgw_gc.cc:598-608")
		Expect(g.TransitionedForTest(2)).To(BeFalse())
		Expect(c.Object(testDataPool, "", "t3")).NotTo(BeNil(), "the queue waits for the omap log")
		Expect(c.Object(testDataPool, "", "t4")).NotTo(BeNil())
	})

	It("skips a shard another gateway holds", func(ctx SpecContext) {
		Expect(gcPool(ctx).LockExclusive(ctx, "gc.3", "gc_process", "other-cookie", "", time.Hour, 0)).To(Succeed())
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 3, time.Hour, true)).To(Succeed())
		Expect(c.Reads(gcPoolName, gcNS, "gc.3")).To(BeZero(), "EBUSY: nothing listed (rgw_gc.cc:571-576)")
		Expect(c.Locks(gcPoolName, gcNS, "gc.3", "gc_process")).To(Equal([]radosclient.Locker{{Client: "client.4155", Cookie: "other-cookie"}}))
	})

	It("refuses a pass with no time to hold the lock for, before taking it", func(ctx SpecContext) {
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 0, 0, true)).To(MatchError(ContainSubstring("rgw_gc_processor_max_time")), "-EAGAIN, rgw_gc.cc:564-565")
		Expect(c.Reads(gcPoolName, gcNS, "gc.0")).To(BeZero())
		Expect(c.Locks(gcPoolName, gcNS, "gc.0", "gc_process")).To(BeEmpty())
	})

	It("keeps one put more than rgw_gc_max_concurrent_io in flight, as radosgw's schedule_io does", func(ctx SpecContext) {
		var oids []string
		for i := range 25 {
			oids = append(oids, fmt.Sprintf("m%d", i))
		}
		enqueue(ctx, "gc.0", "big", chain(oids...))
		clock.Advance(time.Second)
		// Each put waits at the gate before it runs, so while the gate is
		// shut every put started is still in flight.
		var started atomic.Int32
		gate := make(chan struct{})
		for _, oid := range oids {
			c.BeforeWrite(testDataPool, "", oid, func(*fakerados.Object) {
				started.Add(1)
				<-gate
			})
		}
		done := make(chan error, 1)
		go func() { done <- g.ProcessShardForTest(ctx, 0, time.Hour, true) }()
		Eventually(started.Load).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(11))
		Consistently(started.Load).WithTimeout(50*time.Millisecond).WithPolling(time.Millisecond).
			Should(BeEquivalentTo(11), "ios.size() > max_aio, rgw_gc.cc:384")
		close(gate)
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Succeed()))
		for _, oid := range oids {
			Expect(c.Object(testDataPool, "", oid)).To(BeNil())
		}
		Expect(c.GCEntries(gcPoolName, gcNS, "gc.0")).To(BeEmpty())
	})

	It("frees a queue of more than one page in one pass, each page removed after its puts", func(ctx SpecContext) {
		var oids []string
		for i := range 250 {
			oid := fmt.Sprintf("p%d", i)
			oids = append(oids, oid)
			enqueue(ctx, "gc.0", oid, chain(oid))
		}
		clock.Advance(time.Second)
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 0, time.Hour, true)).To(Succeed())
		for _, oid := range oids {
			Expect(c.Object(testDataPool, "", oid)).To(BeNil(), oid)
		}
		Expect(c.GCEntries(gcPoolName, gcNS, "gc.0")).To(BeEmpty())
		removals := c.WritesTo(gcPoolName, gcNS, "gc.0")
		Expect(removals).To(HaveLen(3), "pages of 100, rgw_gc.cc:585")
		var n []uint64
		for _, w := range removals {
			n = append(n, execIn(w, 0, "rgw_gc_queue_remove_entries", gc.DecodeQueueRemoveEntriesOp).NumEntries)
		}
		Expect(n).To(Equal([]uint64{100, 100, 50}), "marker = next_marker before the removal, rgw_gc.cc:631 and :710")
	})

	It("stops at the time budget before an entry, removing nothing", func(ctx SpecContext) {
		enqueue(ctx, "gc.0", "q", chain("t5", "t6"))
		clock.Advance(time.Second)
		var calls atomic.Int32
		g.SetNow(func() time.Time {
			if calls.Add(1) == 1 {
				return t0
			}
			return t0.Add(time.Hour)
		})
		c.ResetCounters()
		Expect(g.ProcessShardForTest(ctx, 0, time.Hour, true)).To(Succeed())
		Expect(c.Reads(gcPoolName, gcNS, "gc.0")).To(Equal(4), "one page listed")
		Expect(c.Writes(testDataPool, "", "t5")).To(BeZero(), "rgw_gc.cc:645-648")
		Expect(c.Writes(gcPoolName, gcNS, "gc.0")).To(BeZero())
		Expect(c.GCEntries(gcPoolName, gcNS, "gc.0")).To(HaveLen(1))
		Expect(c.Locks(gcPoolName, gcNS, "gc.0", "gc_process")).To(BeEmpty())
	})

	It("does not remove a queue page whose puts failed, and retries it on the next pass", func(ctx SpecContext) {
		shard, tails := queueTails(ctx)
		clock.Advance(2 * time.Hour)
		c.FailNextWrite(testDataPool, "", tails[0], syscall.EIO)
		c.ResetCounters()
		Expect(g.Process(ctx, true)).To(Succeed())
		Expect(c.Object(testDataPool, "", tails[0])).NotTo(BeNil())
		Expect(c.Object(testDataPool, "", tails[1])).To(BeNil(), "the other put went ahead")
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(HaveLen(1), "rgw_gc.cc:703-716")
		Expect(c.Writes(gcPoolName, gcNS, shard)).To(BeZero())

		Expect(g.Process(ctx, true)).To(Succeed())
		Expect(c.Object(testDataPool, "", tails[0])).To(BeNil())
		Expect(c.GCEntries(gcPoolName, gcNS, shard)).To(BeEmpty(), "the gone tail's ENOENT counts as freed, rgw_gc.cc:413-415")
	})

	It("leaves a queue entry whose object's pool cannot be opened, where radosgw faults on one without a pool", func(ctx SpecContext) {
		c.FailPool("")
		enqueue(ctx, "gc.0", "nopool", []rgwcls.GCObj{{Key: rgwcls.ObjKey{Name: "x"}}})
		clock.Advance(time.Second)
		Expect(g.ProcessShardForTest(ctx, 0, time.Hour, true)).To(Succeed())
		Expect(c.GCEntries(gcPoolName, gcNS, "gc.0")).To(HaveLen(1))
		Expect(c.Locks(gcPoolName, gcNS, "gc.0", "gc_process")).To(BeEmpty())
	})

	Describe("run", func() {
		var (
			mu     sync.Mutex
			sleeps []time.Duration
			cancel context.CancelFunc
		)
		BeforeEach(func() { sleeps = nil })
		// begin returns the context a spec runs the worker under, which
		// cancel ends.
		begin := func(ctx context.Context) context.Context {
			runCtx, end := context.WithCancel(ctx)
			cancel = end
			DeferCleanup(end)
			return runCtx
		}
		// sleepFor records each sleep and ends the run at the nth.
		sleepFor := func(n int) func(context.Context, time.Duration) error {
			return func(ctx context.Context, d time.Duration) error {
				mu.Lock()
				defer mu.Unlock()
				sleeps = append(sleeps, d)
				if len(sleeps) == n {
					cancel()
					return ctx.Err()
				}
				return nil
			}
		}
		// passTaking reopens the store with the options kv and makes each
		// pass, as run times it, take d. A pass refused for its max time
		// reads the clock only at its start and end.
		passTaking := func(ctx context.Context, d time.Duration) {
			reopen(ctx)
			calls := 0
			g.SetNow(func() time.Time {
				calls++
				start := t0.Add(time.Duration((calls+1)/2) * time.Hour)
				if calls%2 == 1 {
					return start
				}
				return start.Add(d)
			})
		}

		It("sleeps the period less the whole seconds a pass took, and stops with the context", func(ctx SpecContext) {
			runCtx := begin(ctx)
			kv["rgw_gc_processor_max_time"] = "0"
			passTaking(ctx, 1500*time.Millisecond)
			g.SetSleep(sleepFor(2))
			Expect(g.Run(runCtx)).To(Succeed())
			Expect(sleeps).To(Equal([]time.Duration{3599 * time.Second, 3599 * time.Second}), "rgw_gc.cc:795-805")
		})

		It("runs the next pass at once when a pass outlasts the period", func(ctx SpecContext) {
			runCtx := begin(ctx)
			kv["rgw_gc_processor_max_time"] = "0"
			kv["rgw_gc_processor_period"] = "2"
			reopen(ctx)
			calls := 0
			g.SetNow(func() time.Time {
				calls++
				if calls == 2 {
					return t0.Add(2 * time.Second)
				}
				return t0
			})
			g.SetSleep(sleepFor(1))
			Expect(g.Run(runCtx)).To(Succeed())
			Expect(calls).To(Equal(4), "two passes")
			Expect(sleeps).To(Equal([]time.Duration{2 * time.Second}), "secs <= end.sec(): continue, rgw_gc.cc:799-800")
		})

		It("waits one second for a period that is not positive, logging an error", func(ctx SpecContext) {
			runCtx := begin(ctx)
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			kv["rgw_gc_processor_max_time"] = "0"
			kv["rgw_gc_processor_period"] = "-5"
			passTaking(ctx, 0)
			g.SetSleep(sleepFor(1))
			Expect(g.Run(runCtx)).To(Succeed())
			Expect(sleeps).To(Equal([]time.Duration{time.Second}))
			Expect(logs.String()).To(MatchRegexp(`"level":"ERROR".*"option":"rgw_gc_processor_period","value":-5`))
		})

		It("logs a pass refused for a max time that is not positive, without listing a shard", func(ctx SpecContext) {
			runCtx := begin(ctx)
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			kv["rgw_gc_processor_max_time"] = "-1"
			passTaking(ctx, 0)
			g.SetSleep(sleepFor(1))
			c.ResetCounters()
			Expect(g.Run(runCtx)).To(Succeed())
			Expect(logs.String()).To(ContainSubstring("garbage collection process failed"))
			for i := range 4 {
				Expect(c.Reads(gcPoolName, gcNS, fmt.Sprintf("gc.%d", i))).To(BeZero())
			}
		})

		It("is a worker Open registers while rgw_enable_gc_threads is set", func(ctx SpecContext) {
			runCtx := begin(ctx)
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			done := make(chan error, 1)
			go func() { done <- s.Run(runCtx) }()
			Eventually(logs.String).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).
				Should(ContainSubstring(`"msg":"worker started","worker":"gc"`))
			cancel()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Succeed()))

			kv["rgw_enable_gc_threads"] = "false"
			reopen(ctx)
			bareLogs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(bareLogs))
			bareCtx, bareCancel := context.WithCancel(ctx)
			defer bareCancel()
			go func() { done <- s.Run(bareCtx) }()
			Eventually(bareLogs.String).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).
				Should(ContainSubstring(`"worker":"index-completions"`))
			bareCancel()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(Succeed()))
			Expect(bareLogs.String()).NotTo(ContainSubstring(`"worker":"gc"`))
		})
	})
})
