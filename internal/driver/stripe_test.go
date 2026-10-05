package driver_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("planPut", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
		key meta.ObjKey
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		rec = testBucket(putBucketID, 11)
		key = meta.ObjKey{Name: "k"}
	})

	It("puts the head chunk in the head when head and tail share a pool", func(ctx SpecContext) {
		l, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.MaxHead).To(BeEquivalentTo(4 << 20))
		Expect(l.Chunk).To(BeEquivalentTo(4 << 20))
		Expect(l.Stripe).To(BeEquivalentTo(4 << 20))
		Expect(l.HeadPool.Name()).To(Equal(testDataPool))
		Expect(l.TailPool.Name()).To(Equal(testDataPool))
		Expect(l.TailRule).To(Equal(meta.PlacementRule{Name: "default-placement"}), "inherit_from the bucket rule")
	})
	It("keeps the head empty when the storage class lives in another pool", func(ctx SpecContext) {
		l, err := s.PlanPutForTest(ctx, rec, key, "COLD")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.MaxHead).To(BeZero(), "rgw_putobj_processor.cc:292-301")
		Expect(l.TailPool.Name()).To(Equal(coldPool))
		Expect(l.TailRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
	})
	It("keeps the head empty when the placement keeps no data inline", func(ctx SpecContext) {
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			pi := z.PlacementPools["default-placement"]
			pi.InlineData = false
			z.PlacementPools["default-placement"] = pi
		})
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		l, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.MaxHead).To(BeZero(), "rgw_putobj_processor.cc:304-310")
		Expect(l.Chunk).To(BeEquivalentTo(4 << 20))
	})
	It("inlines the head of a bucket whose placement the zone does not name, as get_placement's failure does", func(ctx SpecContext) {
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			pi := z.PlacementPools["default-placement"]
			pi.InlineData = false
			z.PlacementPools["default-placement"] = pi
		})
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		rec.Info.PlacementRule = meta.PlacementRule{}
		l, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.HeadPool.Name()).To(Equal(testDataPool), "the zonegroup's default placement holds the data")
		Expect(l.MaxHead).To(BeEquivalentTo(4<<20), "get_placement(\"\") fails, which inlines")
	})
	DescribeTable("aligns the chunk and stripe to the pool's alignment, as get_max_aligned_size does",
		func(ctx SpecContext, alignment, chunk, stripe uint64) {
			c.SetRequiredAlignment(testDataPool, alignment)
			l, err := s.PlanPutForTest(ctx, rec, key, "")
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{l.Chunk, l.Stripe, l.MaxHead}).To(Equal([]uint64{chunk, stripe, chunk}))
		},
		Entry("rounded down to a multiple", uint64(3<<20), uint64(3<<20), uint64(3<<20)),
		Entry("raised to an alignment above the size", uint64(8<<20), uint64(8<<20), uint64(8<<20)),
		Entry("kept by no alignment", uint64(0), uint64(4<<20), uint64(4<<20)),
	)
	It("aligns a tail in another pool's chunk to that pool and its stripe to the head pool's alignment", func(ctx SpecContext) {
		c.SetRequiredAlignment(coldPool, 3<<20)
		l, err := s.PlanPutForTest(ctx, rec, key, "COLD")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Chunk).To(BeEquivalentTo(3<<20), "rgw_putobj_processor.cc:295")
		Expect(l.Stripe).To(BeEquivalentTo(4<<20), "rgw_putobj_processor.cc:317 aligns the stripe by the head pool")
	})
	It("refuses a PUT whose aligned chunk passes the put window with radosgw's UnknownError", func(ctx SpecContext) {
		c.SetRequiredAlignment(testDataPool, 32<<20)
		_, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).To(MatchError(op.ErrUnknown),
			"BlockingAioThrottle::get fails a piece above the window with EDEADLK (rgw_aio_throttle.cc:40-42), which no S3 error names")
	})
	It("asks the pool for its alignment on every PUT, as radosgw does", func(ctx SpecContext) {
		_, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		c.SetRequiredAlignment(testDataPool, 3<<20)
		l, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Chunk).To(BeEquivalentTo(3 << 20))
	})
	It("fails with UnknownError when no placement gives the bucket a data pool", func(ctx SpecContext) {
		rec.Info.PlacementRule = meta.PlacementRule{Name: "nope"}
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			z.PlacementPools = map[string]meta.ZonePlacementInfo{}
		})
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		_, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).To(MatchError(op.ErrUnknown), "prepare's -EIO, rgw_putobj_processor.cc:276-278")
	})
})

var _ = Describe("tailWriter", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
		key meta.ObjKey
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		rec = testBucket(putBucketID, 11)
		key = meta.ObjKey{Name: "k"}
	})
	writer := func(ctx context.Context) *driver.TailWriter {
		GinkgoHelper()
		tw, err := s.NewTailWriterForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		return tw
	}

	It("streams a 10 MiB body as a 4 MiB head and two tails, hashing every byte", func(ctx SpecContext) {
		body := bytes.Repeat([]byte("0123456789abcdef"), (10<<20)/16)
		tw := writer(ctx)
		h := md5.New()
		head, size, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), h)
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(10 << 20))
		Expect(head).To(Equal(body[:4<<20]))
		sum := md5.Sum(body)
		Expect(h.Sum(nil)).To(Equal(sum[:]))
		Expect(tw.Tails()).To(Equal([]string{tailOID(putPrefix, 1), tailOID(putPrefix, 2)}))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1)).Data).To(Equal(body[4<<20 : 8<<20]))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 2)).Data).To(Equal(body[8<<20:]))
		w := c.LastWrite(testDataPool, "", tailOID(putPrefix, 2))
		Expect(w.Steps()).To(HaveLen(2))
		Expect(w.Steps()[0]).To(Equal(&radosclient.SetAllocHintStep{}), "add_write_hint: set_alloc_hint2(0, 0, 0)")
		Expect(w.Steps()[1]).To(Equal(&radosclient.WriteFullStep{Data: body[8<<20:]}))
		_, stamped := w.Mtime()
		Expect(stamped).To(BeFalse(), "a tail write carries no mtime")
	})

	It("writes a stripe in pieces when the chunk is smaller than the stripe", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "1048576"}, time.Now())
		body := bytes.Repeat([]byte("abcdefgh"), (6<<20)/8)
		tw := writer(ctx)
		head, size, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(6 << 20))
		Expect(head).To(Equal(body[:1<<20]), "the head takes one chunk")
		Expect(tw.Tails()).To(Equal([]string{tailOID(putPrefix, 1), tailOID(putPrefix, 2)}))
		writes := c.WritesTo(testDataPool, "", tailOID(putPrefix, 1))
		Expect(writes).To(HaveLen(4))
		Expect(writes[0].Steps()).To(Equal([]radosclient.Step{&radosclient.SetAllocHintStep{}, &radosclient.WriteFullStep{Data: body[1<<20 : 2<<20]}}))
		for i, w := range writes[1:] {
			ofs := uint64(i+1) << 20
			Expect(w.Steps()).To(Equal([]radosclient.Step{
				&radosclient.SetAllocHintStep{}, &radosclient.WriteStep{Data: body[(1<<20)+ofs : (2<<20)+ofs], Offset: ofs},
			}), "piece %d", i+1)
		}
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1)).Data).To(Equal(body[1<<20 : 5<<20]))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 2)).Data).To(Equal(body[5<<20:]))
	})

	It("writes a stripe's pieces one at a time in stream order, while other stripes go on", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "1048576"}, time.Now())
		body := mibPattern(6)
		var started atomic.Int32
		release := make(chan struct{})
		c.BeforeWrite(testDataPool, "", tailOID(putPrefix, 1), func(*fakerados.Object) {
			if started.Add(1) == 1 {
				<-release
			}
		})
		tw := writer(ctx)
		done := make(chan error, 1)
		go func() {
			_, _, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
			done <- err
		}()
		Eventually(func() int { return c.Writes(testDataPool, "", tailOID(putPrefix, 2)) }).
			WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(Equal(1), "stripe _2 is written while _1's first piece is held")
		Consistently(started.Load).WithTimeout(100*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeEquivalentTo(1),
			"RadosWriter::process submits a stripe's pieces in stream order: none starts before the one before it ends")
		close(release)
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
		Expect(tw.Drain()).To(Succeed())
		var offsets []uint64
		for _, w := range c.WritesTo(testDataPool, "", tailOID(putPrefix, 1)) {
			switch st := w.Steps()[1].(type) {
			case *radosclient.WriteFullStep:
				offsets = append(offsets, 0)
			case *radosclient.WriteStep:
				offsets = append(offsets, st.Offset)
			}
		}
		Expect(offsets).To(Equal([]uint64{0, 1 << 20, 2 << 20, 3 << 20}))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1)).Data).To(Equal(body[1<<20 : 5<<20]))
	})

	It("keeps every piece of a stripe on one processor, where a newer goroutine runs first", func(ctx SpecContext) {
		prev := runtime.GOMAXPROCS(1)
		DeferCleanup(func() { runtime.GOMAXPROCS(prev) })
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "1048576"}, time.Now())
		body := mibPattern(4)
		tw := writer(ctx)
		_, size, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(4 << 20))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1)).Data).To(Equal(body[1<<20:]), "stripe _1 holds bytes 1-4 MiB")
	})

	It("keeps at most the put window in flight", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_put_obj_min_window_size": "8388608"}, time.Now())
		var started atomic.Int32
		release := make(chan struct{})
		for n := 1; n <= 4; n++ {
			c.BeforeWrite(testDataPool, "", tailOID(putPrefix, n), func(*fakerados.Object) {
				started.Add(1)
				<-release
			})
		}
		tw := writer(ctx)
		body := bytes.Repeat([]byte("w"), 20<<20)
		done := make(chan error, 1)
		go func() {
			_, _, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
			done <- err
		}()
		Eventually(started.Load).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(2))
		Consistently(started.Load).WithTimeout(100*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeEquivalentTo(2),
			"two 4 MiB writes fill an 8 MiB window")
		close(release)
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
		Expect(tw.Drain()).To(Succeed())
		Expect(started.Load()).To(BeEquivalentTo(4))
	})

	DescribeTable("holds no more than the window and one chunk of tail buffers while the writes are slow, however long the body",
		func(ctx SpecContext, chunk uint64, started int32) {
			s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": strconv.FormatUint(chunk, 10)}, time.Now())
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			DeferCleanup(unblock)
			var held atomic.Int32
			for n := 1; n <= 32; n++ {
				c.BeforeWrite(testDataPool, "", tailOID(putPrefix, n), func(*fakerados.Object) {
					held.Add(1)
					<-release
				})
			}
			tw := writer(ctx)
			var read atomic.Int64
			body := &countingReader{r: io.LimitReader(zeros{}, 64<<20), n: &read}
			done := make(chan error, 1)
			go func() {
				_, _, err := tw.Consume(ctx, body, -1, md5.New())
				done <- err
			}()
			last := int64(-1)
			Eventually(func() bool {
				cur := read.Load()
				quiet := cur == last
				last = cur
				return quiet
			}).WithTimeout(5*time.Second).WithPolling(20*time.Millisecond).Should(BeTrue(), "consume stops reading once the window is full")
			Consistently(read.Load).WithTimeout(100 * time.Millisecond).WithPolling(10 * time.Millisecond).Should(Equal(last))
			Expect(last).To(BeNumerically("<", 64<<20), "the body is not read to its end")
			Expect(tw.PeakBuffered()).To(BeNumerically("<=", (16<<20)+chunk),
				"rgw_put_obj_min_window_size weighs every buffer in flight, plus the piece waiting for room")
			Eventually(held.Load).Should(Equal(started), "the stripes whose first write the window lets start")
			unblock()
			Eventually(done).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
			Expect(tw.Drain()).To(Succeed())
		},
		Entry("a stripe of one chunk: four 4 MiB writes fill the window", uint64(4<<20), int32(4)),
		Entry("a stripe that is not a multiple of the chunk, whose short pieces still hold a whole buffer: five 3 MiB buffers over three stripes",
			uint64(3<<20), int32(3)),
	)

	It("sizes the head by the declared length, as get_data sizes its read", func(ctx SpecContext) {
		tw := writer(ctx)
		head, _, err := tw.Consume(ctx, bytes.NewReader([]byte("hello")), 5, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(head).To(Equal([]byte("hello")))
		Expect(head).To(HaveCap(5), "rgw_rest.cc:1068-1085 reads min(length - ofs, rgw_max_chunk_size)")
	})

	It("issues nothing for a body within the head, and no empty writes", func(ctx SpecContext) {
		tw := writer(ctx)
		body := bytes.Repeat([]byte("h"), 4<<20)
		head, size, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect([]any{len(head), size}).To(Equal([]any{4 << 20, uint64(4 << 20)}))
		Expect(tw.Tails()).To(BeEmpty())
		tw = writer(ctx)
		head, size, err = tw.Consume(ctx, bytes.NewReader(nil), 0, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(head).NotTo(BeNil(), "an empty head is still written with write_full")
		Expect([]any{len(head), size}).To(Equal([]any{0, uint64(0)}))
		Expect(tw.Tails()).To(BeEmpty())
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	It("ends a body that ends on a stripe boundary without another tail", func(ctx SpecContext) {
		tw := writer(ctx)
		body := bytes.Repeat([]byte("s"), 8<<20)
		_, size, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(8 << 20))
		Expect(tw.Tails()).To(Equal([]string{tailOID(putPrefix, 1)}))
	})

	DescribeTable("returns the body's error, deletes what it wrote and leaves the pool clean",
		func(ctx SpecContext, cause, want error) {
			tw := writer(ctx)
			body := &cutReader{r: bytes.NewReader(bytes.Repeat([]byte("x"), 9<<20)), err: cause}
			_, _, err := tw.Consume(ctx, body, 10<<20, md5.New())
			Expect(err).To(BeIdenticalTo(want))
			Expect(tw.Tails()).To(Equal([]string{tailOID(putPrefix, 1)}), "the full stripe _1 was issued, the partial _2 piece was not")
			tw.Discard()
			Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "~RadosWriter removes what it wrote")
		},
		Entry("io.ErrUnexpectedEOF, a body cut short, is RequestTimeout", io.ErrUnexpectedEOF, op.ErrRequestTimeout),
		Entry("an auth verdict comes back verbatim", op.ErrSignatureDoesNotMatch, op.ErrSignatureDoesNotMatch),
	)

	It("survives a client disconnect without canceling the writes in flight", func(ctx SpecContext) {
		reqCtx, cancel := context.WithCancel(ctx)
		release := make(chan struct{})
		entered := make(chan struct{})
		enter := sync.OnceFunc(func() { close(entered) })
		c.BeforeWrite(testDataPool, "", tailOID(putPrefix, 1), func(*fakerados.Object) {
			enter()
			<-release
		})
		tw := writer(ctx)
		body := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("y"), 8<<20)), ctxReader{reqCtx})
		done := make(chan error, 1)
		go func() {
			_, _, err := tw.Consume(reqCtx, body, 12<<20, md5.New())
			done <- err
		}()
		Eventually(entered).WithTimeout(5 * time.Second).Should(BeClosed())
		cancel()
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(MatchError(context.Canceled)))
		close(release)
		Eventually(func() *fakerados.Object { return c.Object(testDataPool, "", tailOID(putPrefix, 1)) }).
			WithTimeout(5*time.Second).WithPolling(time.Millisecond).ShouldNot(BeNil(), "the write in flight lands under the driver's lifetime")
		tw.Discard()
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1))).To(BeNil())
	})

	It("reports a failed tail write no later than drain, as process_completed and drain do", func(ctx SpecContext) {
		closed := make(chan error, 1)
		shut := sync.OnceFunc(func() { closed <- c.Close() })
		c.BeforeWrite(testDataPool, "", tailOID(putPrefix, 1), func(*fakerados.Object) { shut() })
		tw := writer(ctx)
		body := bytes.Repeat([]byte("f"), 10<<20)
		_, _, err := tw.Consume(ctx, bytes.NewReader(body), int64(len(body)), md5.New())
		if err == nil {
			err = tw.Drain()
		}
		Expect(closed).To(Receive(BeNil()))
		Expect(err).To(MatchError(radosclient.ErrClosed))
		Expect(err).To(MatchError(op.ErrInternalError), "a failure without an errno")
	})

	It("refuses to plan a PUT whose tail pool does not open", func(ctx SpecContext) {
		c.FailPool(coldPool)
		_, err := s.NewTailWriterForTest(ctx, rec, key, "COLD")
		Expect(err).To(MatchError(ContainSubstring(coldPool)))
	})
})

var _ = Describe("part writer", func() {
	const prefix = "k.2~id"
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		target meta.Obj
	)
	BeforeEach(func(ctx SpecContext) {
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, time.Now())
		rec = testBucket(putBucketID, 11)
		target = meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}}
	})
	partOID := func(n int) string { return fmt.Sprintf("%s__multipart_%s.%d", putBucketID, prefix, n) }
	shadowOID := func(n, stripe int) string { return fmt.Sprintf("%s__shadow_%s.%d_%d", putBucketID, prefix, n, stripe) }
	writer := func(ctx context.Context, n uint32) *driver.PartWriter {
		GinkgoHelper()
		l, err := s.PlanPutForTest(ctx, rec, target.Key, "")
		Expect(err).NotTo(HaveOccurred())
		m := meta.NewPartManifest(target, rec.Info.PlacementRule, meta.PlacementRule{}, prefix, n, l.Stripe)
		return s.NewPartWriterForTest(&m, l, n)
	}

	It("creates the part head exclusively, names later stripes as shadows, and awaits the first write", func(ctx SpecContext) {
		body := mibPattern(9)
		tw := writer(ctx, 2)
		h := md5.New()
		size, err := tw.Consume(ctx, bytes.NewReader(body), -1, h)
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(9 << 20))
		sum := md5.Sum(body)
		Expect(h.Sum(nil)).To(Equal(sum[:]))
		// A recorded step holds the writer's buffer, which later pieces reuse,
		// so the bytes are checked on the stored objects.
		steps := c.LastWrite(testDataPool, "", partOID(2)).Steps()
		Expect(steps).To(HaveLen(3))
		Expect(steps[:2]).To(Equal([]radosclient.Step{&radosclient.CreateStep{Exclusive: true}, &radosclient.SetAllocHintStep{}}),
			"write_exclusive: create(true), add_write_hint, write_full (rgw_putobj_processor.cc:161-178)")
		Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.WriteFullStep{}))
		Expect(c.Writes(testDataPool, "", partOID(2))).To(Equal(1))
		Expect(c.Object(testDataPool, "", partOID(2)).Data).To(Equal(body[:4<<20]))
		Expect(c.Object(testDataPool, "", shadowOID(2, 1)).Data).To(Equal(body[4<<20 : 8<<20]))
		Expect(c.Object(testDataPool, "", shadowOID(2, 2)).Data).To(Equal(body[8<<20:]))
		Expect(c.LastWrite(testDataPool, "", shadowOID(2, 1)).Steps()[0]).To(Equal(&radosclient.SetAllocHintStep{}), "a later stripe is no exclusive create")
		Expect(c.WriteOrder(testDataPool, "")[0]).To(Equal(partOID(2)), "the exclusive create completed before any shadow was issued")
	})

	It("writes the whole first stripe in one exclusive write when the chunk is smaller", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "1048576"}, time.Now())
		body := mibPattern(6)
		tw := writer(ctx, 1)
		_, err := tw.Consume(ctx, bytes.NewReader(body), -1, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(c.WritesTo(testDataPool, "", partOID(1))).To(HaveLen(1), "HeadObjectProcessor holds a part's whole first stripe, its head chunk being the stripe size")
		Expect(c.Object(testDataPool, "", partOID(1)).Data).To(Equal(body[:4<<20]))
		writes := c.WritesTo(testDataPool, "", shadowOID(1, 1))
		Expect(writes).To(HaveLen(2), "the second stripe goes in chunks")
		Expect(writes[1].Steps()[1]).To(BeAssignableToTypeOf(&radosclient.WriteStep{}))
		Expect(writes[1].Steps()[1]).To(HaveField("Offset", BeEquivalentTo(1<<20)))
		Expect(c.Object(testDataPool, "", shadowOID(1, 1)).Data).To(Equal(body[4<<20:]))
	})

	It("writes a shadow stripe's pieces one at a time in stream order", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "1048576"}, time.Now())
		body := mibPattern(10)
		var started atomic.Int32
		release := make(chan struct{})
		c.BeforeWrite(testDataPool, "", shadowOID(1, 1), func(*fakerados.Object) {
			if started.Add(1) == 1 {
				<-release
			}
		})
		tw := writer(ctx, 1)
		done := make(chan error, 1)
		go func() {
			_, err := tw.Consume(ctx, bytes.NewReader(body), -1, md5.New())
			done <- err
		}()
		Eventually(func() int { return c.Writes(testDataPool, "", shadowOID(1, 2)) }).
			WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(Equal(2), "stripe _2 is written while _1's first piece is held")
		Consistently(started.Load).WithTimeout(100*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeEquivalentTo(1),
			"RadosWriter::process submits a stripe's pieces in stream order")
		close(release)
		Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
		Expect(tw.Drain()).To(Succeed())
		Expect(c.Object(testDataPool, "", shadowOID(1, 1)).Data).To(Equal(body[4<<20 : 8<<20]))
		Expect(c.Object(testDataPool, "", shadowOID(1, 2)).Data).To(Equal(body[8<<20:]))
	})

	It("holds no more than the window and one stripe of buffers while the writes are slow", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_chunk_size": "1048576"}, time.Now())
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		DeferCleanup(unblock)
		for n := 1; n <= 32; n++ {
			c.BeforeWrite(testDataPool, "", shadowOID(1, n), func(*fakerados.Object) { <-release })
		}
		tw := writer(ctx, 1)
		var read atomic.Int64
		body := &countingReader{r: io.LimitReader(zeros{}, 64<<20), n: &read}
		done := make(chan error, 1)
		go func() {
			_, err := tw.Consume(ctx, body, -1, md5.New())
			done <- err
		}()
		last := int64(-1)
		Eventually(func() bool {
			cur := read.Load()
			quiet := cur == last
			last = cur
			return quiet
		}).WithTimeout(5*time.Second).WithPolling(20*time.Millisecond).Should(BeTrue(), "consume stops reading once the window is full")
		Expect(last).To(BeNumerically("<", 64<<20))
		Expect(tw.PeakBuffered()).To(BeNumerically("<=", (16<<20)+(4<<20)),
			"the window, plus the piece waiting for room, which may be the pooled first-stripe buffer")
		unblock()
		Eventually(done).WithTimeout(10 * time.Second).Should(Receive(BeNil()))
		Expect(tw.Drain()).To(Succeed())
	})

	It("returns ErrExists with the first stripe kept when the part head exists", func(ctx SpecContext) {
		c.Put(testDataPool, "", partOID(2), []byte("old"))
		tw := writer(ctx, 2)
		body := bytes.Repeat([]byte("n"), 5<<20)
		_, err := tw.Consume(ctx, bytes.NewReader(body), -1, md5.New())
		Expect(err).To(MatchError(radosclient.ErrExists))
		Expect(tw.First()).To(Equal(body[:4<<20]))
		Expect(c.Object(testDataPool, "", partOID(2)).Data).To(Equal([]byte("old")), "untouched")
		Expect(c.Object(testDataPool, "", shadowOID(2, 1))).To(BeNil(), "nothing else was issued")
		tw.Discard()
		Expect(c.Object(testDataPool, "", partOID(2)).Data).To(Equal([]byte("old")), "the head it did not write is not removed")
	})

	It("writes an empty part as an exclusive empty head", func(ctx SpecContext) {
		tw := writer(ctx, 1)
		size, err := tw.Consume(ctx, strings.NewReader(""), -1, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeZero())
		obj := c.Object(testDataPool, "", partOID(1))
		Expect(obj).NotTo(BeNil())
		Expect(obj.Data).To(BeEmpty())
		Expect(c.LastWrite(testDataPool, "", partOID(1)).Steps()).To(Equal([]radosclient.Step{
			&radosclient.CreateStep{Exclusive: true}, &radosclient.SetAllocHintStep{}, &radosclient.WriteFullStep{Data: []byte{}},
		}), "process_first_chunk's flush of an empty head chunk")
	})

	It("removes the part head with the rest on discard", func(ctx SpecContext) {
		tw := writer(ctx, 1)
		_, err := tw.Consume(ctx, bytes.NewReader(mibPattern(5)), -1, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		tw.Discard()
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	It("fails a first stripe longer than the put window with radosgw's UnknownError, and takes a shorter one", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_obj_stripe_size": "33554432"}, time.Now())
		tw := writer(ctx, 1)
		var read atomic.Int64
		body := &countingReader{r: bytes.NewReader(make([]byte, 20<<20)), n: &read}
		_, err := tw.Consume(ctx, body, -1, md5.New())
		Expect(err).To(MatchError(op.ErrUnknown), "BlockingAioThrottle::get's EDEADLK (rgw_aio_throttle.cc:40-42)")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
		Expect(read.Load()).To(BeEquivalentTo((16<<20)+1), "one byte past the window shows the stripe cannot fit")
		Expect(tw.PeakBuffered()).To(BeNumerically("<=", (16<<20)+1), "no more than the window and one byte, though the stripe is 32 MiB")
		tw = writer(ctx, 1)
		_, err = tw.Consume(ctx, bytes.NewReader(make([]byte, 1<<20)), 1<<20, md5.New())
		Expect(err).NotTo(HaveOccurred(), "a first stripe within the window fits it")
		Expect(tw.Drain()).To(Succeed())
		Expect(c.Object(testDataPool, "", partOID(1)).Data).To(HaveLen(1 << 20))
		Expect(tw.PeakBuffered()).To(BeEquivalentTo(1<<20), "the declared length sizes the buffer")
	})

	It("sizes a small part's buffer by its declared length, and grows it when the body runs past it", func(ctx SpecContext) {
		tw := writer(ctx, 1)
		_, err := tw.Consume(ctx, strings.NewReader("x"), 1, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.PeakBuffered()).To(BeEquivalentTo(1), "as readHead sizes a head: no whole stripe for one byte")
		tw = writer(ctx, 2)
		body := mibPattern(5)
		size, err := tw.Consume(ctx, bytes.NewReader(body), 10, md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(5<<20), "the length check is the caller's")
		Expect(c.Object(testDataPool, "", partOID(2)).Data).To(Equal(body[:4<<20]))
		Expect(c.Object(testDataPool, "", shadowOID(2, 1)).Data).To(Equal(body[4<<20:]))
	})
})

// mibPattern is n MiB whose every MiB holds its own byte value, 1 to n, so
// a piece written at the wrong place shows.
func mibPattern(n int) []byte {
	b := make([]byte, n<<20)
	for i := range b {
		b[i] = byte(1 + i>>20)
	}
	return b
}

// zeros reads as an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// countingReader counts the bytes read through it into n.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n.Add(int64(k))
	return k, err
}

// ctxReader blocks until ctx ends and then fails with its error, as a
// request body does when its client goes away.
type ctxReader struct{ ctx context.Context }

func (r ctxReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
