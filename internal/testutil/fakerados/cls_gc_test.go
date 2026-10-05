package fakerados_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("GCQueueClass", func() {
	const logPool = "zone.rgw.log"
	var (
		c   *fakerados.Cluster
		p   radosclient.Pool
		now time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw_gc", fakerados.GCQueueClass(), fakerados.GCQueueWriteMethods...)
		now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return now })
		var err error
		p, err = c.Pool(ctx, logPool, "gc")
		Expect(err).NotTo(HaveOccurred())
		c.Put(logPool, "gc", "gc.0", nil)
	})
	info := func(tag string) rgwcls.GCObjInfo {
		return rgwcls.GCObjInfo{Tag: tag, Chain: []rgwcls.GCObj{{Pool: "data", Key: rgwcls.ObjKey{Name: tag + "_1"}}}}
	}
	due := func(i rgwcls.GCObjInfo, t time.Time) rgwcls.GCObjInfo {
		i.Time = t
		return i
	}

	It("names the methods the class registers as writes", func() {
		Expect(fakerados.GCQueueWriteMethods).To(ConsistOf(gc.MethodQueueInit, gc.MethodQueueEnqueue, gc.MethodQueueRemoveEntries, gc.MethodQueueUpdateEntry),
			"CLS_METHOD_WR, cls_rgw_gc.cc:551-555")
	})

	It("queues each entry, oldest first, due its expiration from the class's clock", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 7200, info("a"), denc.Squid) })).To(Succeed())
		now = now.Add(time.Second)
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 60, info("b"), denc.Squid) })).To(Succeed())
		Expect(c.GCEntries(logPool, "gc", "gc.0")).To(Equal([]rgwcls.GCObjInfo{
			due(info("a"), now.Add(-time.Second).Add(2*time.Hour)),
			due(info("b"), now.Add(time.Minute)),
		}), "cls_rgw_gc.cc:71-72 replaces the request's time")
		Expect(c.GCEntries(logPool, "gc", "gc.1")).To(BeNil(), "a shard without entries")
	})

	It("refuses an enqueue on a missing shard with ENOENT and every other method with EOPNOTSUPP", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "gc.9", func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 1, info("a"), denc.Squid) })).
			To(MatchError(radosclient.ErrNotFound))
		Expect(c.Object(logPool, "gc", "gc.9")).To(BeNil())
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 1, denc.Squid) })).
			To(MatchError(radosclient.ErrNotSupported))
	})
})
