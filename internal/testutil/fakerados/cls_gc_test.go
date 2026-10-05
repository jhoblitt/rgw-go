package fakerados_test

import (
	"context"
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

	It("refuses an enqueue on a missing shard with ENOENT and a deferral with EOPNOTSUPP", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "gc.9", func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 1, info("a"), denc.Squid) })).
			To(MatchError(radosclient.ErrNotFound))
		Expect(c.Object(logPool, "gc", "gc.9")).To(BeNil())
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueUpdateEntry(op, 1, info("a"), denc.Squid) })).
			To(MatchError(radosclient.ErrNotSupported))
	})

	It("initializes an empty shard, refuses a queue that holds entries with EEXIST and a missing shard with ENOENT", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueInit(op, 1<<20, 50, denc.Squid) })).To(Succeed())
		Expect(c.GCEntries(logPool, "gc", "gc.0")).To(BeNil())
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, 1, info("a"), denc.Squid) })).To(Succeed())
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueInit(op, 1<<20, 50, denc.Squid) })).
			To(MatchError(radosclient.ErrExists), "queue_init, cls_queue_src.cc:113-116")
		Expect(writeErr(ctx, p, "gc.9", func(op *radosclient.WriteOp) { gc.QueueInit(op, 1<<20, 50, denc.Squid) })).
			To(MatchError(radosclient.ErrNotFound), "queue_read_head's cls_cxx_read")
		Expect(writeErr(ctx, p, "gc.9", func(op *radosclient.WriteOp) {
			op.Create(false)
			gc.QueueInit(op, 1<<20, 50, denc.Squid)
		})).To(Succeed(), "RGWGC::initialize creates the shard first")
		Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { op.Exec(gc.Class, gc.MethodQueueInit, []byte{1}) })).
			To(MatchError(radosclient.ErrInvalid))
	})

	Describe("listing and removing", func() {
		list := func(ctx context.Context, marker string, maxEntries uint32, expiredOnly bool) (rgwcls.GCListRet, error) {
			op := radosclient.NewReadOp()
			res := gc.QueueList(op, marker, maxEntries, expiredOnly, denc.Squid)
			if _, err := p.Read(ctx, "gc.0", op, radosclient.OpFlagNone); err != nil {
				return rgwcls.GCListRet{}, err
			}
			return res.Result()
		}
		tags := func(entries []rgwcls.GCObjInfo) []string {
			var out []string
			for _, e := range entries {
				out = append(out, e.Tag)
			}
			return out
		}
		BeforeEach(func(ctx SpecContext) {
			for _, tc := range []struct {
				tag string
				exp uint32
			}{{"a", 0}, {"b", 60}, {"c", 0}, {"d", 0}} {
				Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueEnqueue(op, tc.exp, info(tc.tag), denc.Squid) })).To(Succeed())
			}
		})

		It("pages the queue from a marker naming the next entry's index, oldest first", func(ctx SpecContext) {
			ret, err := list(ctx, "", 3, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(tags(ret.Entries)).To(Equal([]string{"a", "b", "c"}))
			Expect([]any{ret.Truncated, ret.NextMarker}).To(Equal([]any{true, "3"}))
			Expect(ret.Entries[1].Time).To(Equal(now.Add(time.Minute)))
			ret, err = list(ctx, ret.NextMarker, 3, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(tags(ret.Entries)).To(Equal([]string{"d"}))
			Expect([]any{ret.Truncated, ret.NextMarker}).To(Equal([]any{false, ""}), "next_marker only while truncated, cls_rgw_gc.cc:217-220")
		})

		It("skips an entry not yet due when it lists the expired only, still counting it toward the page", func(ctx SpecContext) {
			ret, err := list(ctx, "", 2, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(tags(ret.Entries)).To(Equal([]string{"a"}), "cls_rgw_gc.cc:192-203")
			Expect([]any{ret.Truncated, ret.NextMarker}).To(Equal([]any{true, "2"}))
			now = now.Add(time.Minute)
			ret, err = list(ctx, "", 0, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(tags(ret.Entries)).To(Equal([]string{"a", "b", "c", "d"}), "due at the class's clock; a max of 0 lists 128")
		})

		It("removes the oldest entries by count, every entry for a count past the end, and refuses a missing shard", func(ctx SpecContext) {
			Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 2, denc.Squid) })).To(Succeed())
			Expect(tags(c.GCEntries(logPool, "gc", "gc.0"))).To(Equal([]string{"c", "d"}))
			Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 5, denc.Squid) })).To(Succeed())
			Expect(c.GCEntries(logPool, "gc", "gc.0")).To(BeEmpty())
			Expect(writeErr(ctx, p, "gc.9", func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 1, denc.Squid) })).
				To(MatchError(radosclient.ErrNotFound))
			_, err := list(ctx, "", 1, false)
			Expect(err).NotTo(HaveOccurred())
			c.Remove(logPool, "gc", "gc.0")
			_, err = list(ctx, "", 1, false)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})

		It("keeps a marker valid across a removal of the entries before it, as cls_queue's offsets stay", func(ctx SpecContext) {
			ret, err := list(ctx, "", 2, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.NextMarker).To(Equal("2"))
			Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 2, denc.Squid) })).To(Succeed())
			ret, err = list(ctx, ret.NextMarker, 1, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(tags(ret.Entries)).To(Equal([]string{"c"}), "RGWGC::process lists from next_marker after the removal, rgw_gc.cc:631 and :710")
			Expect([]any{ret.Truncated, ret.NextMarker}).To(Equal([]any{true, "3"}))
			ret, err = list(ctx, "", 0, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(tags(ret.Entries)).To(Equal([]string{"c", "d"}), "no marker starts at the front")
			_, err = list(ctx, "1", 1, false)
			Expect(err).To(MatchError(radosclient.ErrInvalid), "a marker before the front")
			Expect(writeErr(ctx, p, "gc.0", func(op *radosclient.WriteOp) { gc.QueueInit(op, 1<<20, 50, denc.Squid) })).
				To(MatchError(radosclient.ErrExists), "a queue that has held entries has a head")
		})

		It("answers a marker that names no entry with EINVAL", func(ctx SpecContext) {
			for _, m := range []string{"x", "-1"} {
				_, err := list(ctx, m, 1, false)
				Expect(err).To(MatchError(radosclient.ErrInvalid), m)
			}
		})
	})
})
