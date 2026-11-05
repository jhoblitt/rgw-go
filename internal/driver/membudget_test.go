package driver_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/semaphore"

	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// uniqueRand returns a different string of digits on every call, so
// concurrent writes get tail prefixes and tags of their own.
func uniqueRand() func(int) string {
	var n atomic.Int64
	return func(k int) string {
		s := fmt.Sprintf("%031d", n.Add(1))
		return s[:min(k, len(s))]
	}
}

// limitedCluster is a cluster whose pools let ops run sem's weight at a
// time, as the in-flight limiter does: held only while an op runs.
type limitedCluster struct {
	*fakerados.Cluster
	sem *semaphore.Weighted
}

func (c limitedCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	return limitedPool{Pool: p, sem: c.sem}, nil
}

type limitedPool struct {
	radosclient.Pool
	sem *semaphore.Weighted
}

func (p limitedPool) WithLocator(loc string) radosclient.Pool {
	return limitedPool{Pool: p.Pool.WithLocator(loc), sem: p.sem}
}

func (p limitedPool) Read(ctx context.Context, oid string, rop *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return 0, err
	}
	defer p.sem.Release(1)
	return p.Pool.Read(ctx, oid, rop, flags)
}

func (p limitedPool) Write(ctx context.Context, oid string, w *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return 0, err
	}
	defer p.sem.Release(1)
	return p.Pool.Write(ctx, oid, w, flags)
}

var _ = Describe("the memory budget", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
	)
	var zlibClass string
	open := func(ctx context.Context, cl radosclient.Cluster) {
		GinkgoHelper()
		rel := denc.Squid
		var err error
		s, err = driver.Open(ctx, cl, conf(nil), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(uniqueRand())
	}
	BeforeEach(func(ctx SpecContext) {
		zlibClass = compClass(compression.Zlib)
		c = newPutCluster()
		// The fixture's period caps a bucket at 7 objects, fewer than a spec
		// of many transfers writes.
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			p.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: -1}
		})
		withCompressionClasses(c, compression.Zlib)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		open(ctx, c)
	})
	key := func(name string, i int) meta.ObjKey { return meta.ObjKey{Name: name + strconv.Itoa(i)} }
	held := func() int64 { return s.MemoryBudgetForTest().Held }
	put := func(ctx context.Context, k meta.ObjKey, data []byte, class string) error {
		_, err := s.PutObject(ctx, rec, k, bytes.NewReader(data), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(data)), StorageClass: class})
		return err
	}
	mustPut := func(ctx context.Context, k meta.ObjKey, data []byte, class string) {
		GinkgoHelper()
		Expect(put(ctx, k, data, class)).To(Succeed())
		settle(s)
	}
	// length is the logical length of st's object.
	length := func(st *op.ObjectState) uint64 {
		if ci := st.Compression; ci != nil && ci.Type != compression.None {
			return ci.OrigSize
		}
		return st.Size
	}
	// read is a whole GET of k, as one request: what its prefetch holds goes
	// back when the request ends.
	read := func(ctx context.Context, k meta.ObjKey) ([]byte, error) {
		reqCtx, end := context.WithCancel(ctx)
		defer end()
		st, err := s.PrefetchObject(reqCtx, rec, k)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		err = s.ReadObject(reqCtx, st, op.ByteRange{Length: length(st)}, &buf)
		return buf.Bytes(), err
	}
	// drained waits for every share to come back, a request's prefetch
	// included, which goes back when the request's context ends.
	drained := func() {
		GinkgoHelper()
		Eventually(held).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeZero())
	}
	// all runs each of fns at once and returns their errors, nil for none.
	all := func(fns ...func() error) []error {
		errs := make([]error, len(fns))
		var wg sync.WaitGroup
		for i, fn := range fns {
			wg.Go(func() { errs[i] = fn() })
		}
		wg.Wait()
		return errs
	}
	noErrors := func(errs []error) {
		GinkgoHelper()
		Expect(errs).To(HaveEach(Succeed()))
	}
	// stallPUTs starts n PUTs that each declare 64 MiB and send nothing, so
	// each holds its 4 MiB head's hold, until the returned stop ends them.
	stallPUTs := func(ctx context.Context, n int) (stop func()) {
		GinkgoHelper()
		stallCtx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range n {
			wg.Go(func() {
				_, errs[i] = s.PutObject(stallCtx, rec, key("stalled", i), ctxReader{stallCtx},
					op.PutParams{Attrs: attrsWithACL(initiator), Size: 64 << 20})
			})
		}
		Eventually(s.MemoryBudgetStats).WithTimeout(5*time.Second).WithPolling(time.Millisecond).
			Should(HaveField("HoldHeld", BeEquivalentTo(n*(4<<20))), "each stalled PUT holds its head")
		return func() {
			cancel()
			wg.Wait()
			Expect(errs).To(HaveEach(MatchError(context.Canceled)), "a stalled PUT ends when its client leaves")
		}
	}
	// stallTails holds n pieces of the budget: a GET of an object's n 4 MiB
	// tails, whose reads wait until stop.
	stallTails := func(ctx context.Context, n int) (stop func()) {
		GinkgoHelper()
		data := mibPattern(4 + 4*n)
		mustPut(ctx, meta.ObjKey{Name: "stalling"}, data, "")
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "stalling"})
		Expect(err).NotTo(HaveOccurred())
		release := make(chan struct{})
		var once sync.Once
		for _, oid := range poolObjects(ctx, c, testDataPool, "") {
			if strings.Contains(oid, "__shadow_") {
				c.BeforeRead(testDataPool, "", oid, func() { <-release })
			}
		}
		done := make(chan error, 1)
		go func() {
			done <- s.ReadObject(ctx, st, op.ByteRange{Offset: 4 << 20, Length: uint64(4 * n << 20)}, io.Discard)
		}()
		Eventually(s.MemoryBudgetStats).WithTimeout(5*time.Second).WithPolling(time.Millisecond).
			Should(HaveField("PieceHeld", BeEquivalentTo(n*(4<<20))), "the stalled GET holds its pieces")
		return func() {
			once.Do(func() { close(release) })
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
		}
	}

	// deadlockTimeout bounds a spec that would otherwise hang on a deadlock.
	// It is no measure of speed: under -race and coverage on a 4-vCPU CI
	// runner the compressing copies take most of 30 s, and a deadlock is
	// still caught, just later.
	const deadlockTimeout = 2 * time.Minute

	Describe("cannot deadlock", func() {
		It("lets PUTs finish whose heads fill the hold class", func(ctx SpecContext) {
			data := mibPattern(12)
			s.SetMemoryBudgetForTest(16<<20, 0)
			fns := make([]func() error, 6)
			for i := range fns {
				fns[i] = func() error { return put(ctx, key("k", i), data, "") }
			}
			noErrors(all(fns...))
			settle(s)
			for i := range fns {
				Expect(read(ctx, key("k", i))).To(Equal(data), "object %d", i)
			}
			drained()
		}, SpecTimeout(deadlockTimeout))

		It("lets a copy into a compressing class finish whose read side fills the budget before its write side draws", func(ctx SpecContext) {
			// Half the entropy of random bytes: the stored head holds about
			// two plaintext blocks, so the write side has compressed and
			// stored eight MiB before it draws its first piece, by when the
			// read side has read all it may.
			src := hexNoise(40 << 20)
			mustPut(ctx, meta.ObjKey{Name: "src"}, src, "")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "src"})
			Expect(err).NotTo(HaveOccurred())
			s.SetMemoryBudgetForTest(16<<20, 0)
			_, err = s.CopyObject(ctx, st, rec, meta.ObjKey{Name: "dst"}, op.CopyParams{Attrs: attrsWithACL(initiator), StorageClass: zlibClass, Tag: "tx-copy"})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(read(ctx, meta.ObjKey{Name: "dst"})).To(Equal(src))
			drained()
		}, SpecTimeout(deadlockTimeout))

		It("lets part copies finish that decode a compressed source into a compressing class", func(ctx SpecContext) {
			plain := hexNoise(12 << 20)
			mustPut(ctx, meta.ObjKey{Name: "csrc"}, plain, zlibClass)
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "csrc"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Compression).NotTo(BeNil(), "the source is compressed")
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "mp"}, op.UploadParams{
				Owner: initiator, OwnerName: "Alice",
				Placement: meta.PlacementRule{Name: "default-placement", StorageClass: zlibClass}, Attrs: attrsWithACL(initiator),
			})
			Expect(err).NotTo(HaveOccurred())
			s.SetMemoryBudgetForTest(32<<20, 0)
			fns := make([]func() error, 3)
			etags := make([]string, len(fns))
			for i := range fns {
				fns[i] = func() error {
					res, err := s.CopyPart(ctx, up, i+1, st, op.ByteRange{Length: uint64(len(plain))})
					if err == nil {
						etags[i] = res.ETag
					}
					return err
				}
			}
			noErrors(all(fns...))
			Expect(etags).To(HaveEach(Equal(md5hex(plain))))
			settle(s)
			drained()
		}, SpecTimeout(deadlockTimeout))

		It("lets GETs finish that each want a window, on a budget of two windows", func(ctx SpecContext) {
			data := mibPattern(64)
			mustPut(ctx, meta.ObjKey{Name: "big"}, data, "")
			s.SetMemoryBudgetForTest(32<<20, 0)
			fns := make([]func() error, 8)
			for i := range fns {
				fns[i] = func() error {
					got, err := read(ctx, meta.ObjKey{Name: "big"})
					if err == nil && !bytes.Equal(got, data) {
						return fmt.Errorf("GET %d read other bytes", i)
					}
					return err
				}
			}
			noErrors(all(fns...))
			drained()
		}, SpecTimeout(deadlockTimeout))

		It("lets a compressed GET finish whose prefetched head and decoder blocks pass the hold cap together", func(ctx SpecContext) {
			plain := hexNoise(8 << 20)
			mustPut(ctx, meta.ObjKey{Name: "comp"}, plain, zlibClass)
			s.SetMemoryBudgetForTest(16<<20, 0)
			got, err := read(ctx, meta.ObjKey{Name: "comp"})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(plain))
			drained()
		}, SpecTimeout(deadlockTimeout))

		It("lets every kind of transfer finish at once on a small budget", func(ctx SpecContext) {
			small, big, plain := mibPattern(20), mibPattern(40), hexNoise(12<<20)
			mustPut(ctx, meta.ObjKey{Name: "big"}, big, "")
			mustPut(ctx, meta.ObjKey{Name: "comp"}, plain, zlibClass)
			bigSt, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "big"})
			Expect(err).NotTo(HaveOccurred())
			compSt, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "comp"})
			Expect(err).NotTo(HaveOccurred())
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "mp"}, op.UploadParams{
				Owner: initiator, OwnerName: "Alice",
				Placement: meta.PlacementRule{Name: "default-placement", StorageClass: zlibClass}, Attrs: attrsWithACL(initiator),
			})
			Expect(err).NotTo(HaveOccurred())
			s.SetMemoryBudgetForTest(48<<20, 0)
			fns := []func() error{
				func() error { return put(ctx, key("put", 0), small, "") },
				func() error { return put(ctx, key("put", 1), small, zlibClass) },
				func() error {
					_, err := s.PutPart(ctx, up, 1, bytes.NewReader(small), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(small))})
					return err
				},
				func() error {
					_, err := s.CopyObject(ctx, bigSt, rec, key("copy", 0), op.CopyParams{Attrs: attrsWithACL(initiator), StorageClass: "COLD", Tag: "tx-copy"})
					return err
				},
				func() error {
					_, err := s.CopyPart(ctx, up, 2, compSt, op.ByteRange{Offset: 1 << 20, Length: 8 << 20})
					return err
				},
				func() error {
					var buf bytes.Buffer
					return s.ReadObject(ctx, bigSt, op.ByteRange{Offset: 6 << 20, Length: 20 << 20}, &buf)
				},
			}
			for range 3 {
				fns = append(fns, func() error { _, err := read(ctx, meta.ObjKey{Name: "big"}); return err })
				fns = append(fns, func() error { _, err := read(ctx, meta.ObjKey{Name: "comp"}); return err })
			}
			noErrors(all(fns...))
			settle(s)
			drained()
		}, SpecTimeout(deadlockTimeout))

		It("lets transfers finish under an in-flight limiter that runs one op at a time", func(ctx SpecContext) {
			open(ctx, limitedCluster{Cluster: c, sem: semaphore.NewWeighted(1)})
			data := mibPattern(20)
			mustPut(ctx, meta.ObjKey{Name: "big"}, data, "")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "big"})
			Expect(err).NotTo(HaveOccurred())
			s.SetMemoryBudgetForTest(32<<20, 0)
			fns := []func() error{
				func() error { return put(ctx, key("put", 0), data, "") },
				func() error { return put(ctx, key("put", 1), data, "") },
				func() error {
					_, err := s.CopyObject(ctx, st, rec, key("copy", 0), op.CopyParams{Attrs: attrsWithACL(initiator), StorageClass: "COLD", Tag: "tx-copy"})
					return err
				},
			}
			for range 4 {
				fns = append(fns, func() error { _, err := read(ctx, meta.ObjKey{Name: "big"}); return err })
			}
			noErrors(all(fns...))
			settle(s)
			drained()
		}, SpecTimeout(deadlockTimeout))
	})

	Describe("what a request sees", func() {
		It("answers SlowDown, before any byte, to a GET that finds no room for its first piece", func(ctx SpecContext) {
			mustPut(ctx, meta.ObjKey{Name: "k"}, mibPattern(8), "")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			s.SetMemoryBudgetForTest(16<<20, 50*time.Millisecond)
			stop := stallTails(ctx, 4)
			defer stop()
			var sink bytes.Buffer
			Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &sink)).To(MatchError(op.ErrSlowDown))
			Expect(sink.Len()).To(BeZero(), "no byte went out, so the error document can")
		}, SpecTimeout(10*time.Second))

		It("pauses a GET mid-body, without an error, until room frees", func(ctx SpecContext) {
			data := mibPattern(8)
			mustPut(ctx, meta.ObjKey{Name: "k"}, data, "")
			st, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Head).To(HaveLen(4<<20), "the prefetch holds the head")
			s.SetMemoryBudgetForTest(16<<20, 50*time.Millisecond)
			stop := stallTails(ctx, 4)
			var sink sinkBuffer
			done := make(chan error, 1)
			go func() { done <- s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &sink) }()
			Eventually(sink.Len).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(Equal(4<<20), "the head went out")
			Consistently(done).WithTimeout(200*time.Millisecond).WithPolling(10*time.Millisecond).ShouldNot(Receive(),
				"past the deadline, a GET that has started waits")
			stop()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
			Expect(sink.Bytes()).To(Equal(data))
		}, SpecTimeout(10*time.Second))

		It("answers SlowDown to a PUT whose head finds no room, its body unread", func(ctx SpecContext) {
			s.SetMemoryBudgetForTest(16<<20, 50*time.Millisecond)
			stop := stallPUTs(ctx, 2)
			defer stop()
			var n atomic.Int64
			body := &countingReader{r: bytes.NewReader(mibPattern(8)), n: &n}
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, body, op.PutParams{Attrs: attrsWithACL(initiator), Size: 8 << 20})
			Expect(err).To(MatchError(op.ErrSlowDown))
			Expect(n.Load()).To(BeZero(), "nothing of the body was read")
			Expect(s.MemoryBudgetStats()).To(HaveField("HoldSlowdowns", BeEquivalentTo(1)))
		}, SpecTimeout(10*time.Second))

		It("lets a PUT that has started wait past the deadline for its pieces", func(ctx SpecContext) {
			s.SetMemoryBudgetForTest(16<<20, 50*time.Millisecond)
			stop := stallTails(ctx, 3)
			data := mibPattern(12)
			done := make(chan error, 1)
			go func() { done <- put(ctx, meta.ObjKey{Name: "k"}, data, "") }()
			Consistently(done).WithTimeout(200*time.Millisecond).WithPolling(10*time.Millisecond).ShouldNot(Receive(),
				"its head held, its pieces wait")
			stop()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
			settle(s)
			Expect(read(ctx, meta.ObjKey{Name: "k"})).To(Equal(data))
		}, SpecTimeout(10*time.Second))

		It("reads a GET's head with its data when the budget has no room for a prefetch", func(ctx SpecContext) {
			data := mibPattern(6)
			mustPut(ctx, meta.ObjKey{Name: "a"}, data, "")
			mustPut(ctx, meta.ObjKey{Name: "b"}, data, "")
			s.SetMemoryBudgetForTest(16<<20, 0)
			first, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "a"})
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Head).To(HaveLen(4<<20), "it fills the prefetch class")
			c.ResetCounters()
			second, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "b"})
			Expect(err).NotTo(HaveOccurred())
			Expect(second.Head).To(BeNil(), "no room for its prefetch")
			Expect(s.MemoryBudgetStats()).To(HaveField("PrefetchSkipped", BeEquivalentTo(1)))
			var sink bytes.Buffer
			Expect(s.ReadObject(ctx, second, op.ByteRange{Length: second.Size}, &sink)).To(Succeed())
			Expect(sink.Bytes()).To(Equal(data), "the same bytes")
			Expect(c.Reads(testDataPool, "", putBucketID+"_b")).To(BeEquivalentTo(2), "the stat, then the head's bytes with their own read")
		})

		It("keeps a prefetched head's share for as long as the request keeps its copy, after the head is written", func(ctx SpecContext) {
			data := mibPattern(6)
			mustPut(ctx, meta.ObjKey{Name: "k"}, data, "")
			prefetched := func() int64 { return s.MemoryBudgetStats().PrefetchHeld }
			reqCtx, end := context.WithCancel(ctx)
			st, err := s.PrefetchObject(reqCtx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(prefetched()).To(BeEquivalentTo(4<<20), "the head's copy holds its share")
			Expect(s.ReadObject(reqCtx, st, op.ByteRange{Length: st.Size}, io.Discard)).To(Succeed())
			Expect(prefetched()).To(BeEquivalentTo(4<<20), "written, the copy lives on in the request's state")
			end()
			Eventually(prefetched).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeZero(),
				"the request ended, and its copy with it")
		})

		It("gives back a prefetched head's share and buffer together when the state is released, ahead of the request's end", func(ctx SpecContext) {
			mustPut(ctx, meta.ObjKey{Name: "k"}, mibPattern(6), "")
			prefetched := func() int64 { return s.MemoryBudgetStats().PrefetchHeld }
			reqCtx, end := context.WithCancel(ctx)
			defer end()
			st, err := s.PrefetchObject(reqCtx, rec, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect([]int64{prefetched(), s.LentBufBytesForTest()}).To(Equal([]int64{4 << 20, 4 << 20}), "the share covers the buffer the head is served from")
			Expect(s.ReadObject(reqCtx, st, op.ByteRange{Length: st.Size}, io.Discard)).To(Succeed())
			st.ReleaseHead()
			Expect([]int64{prefetched(), s.LentBufBytesForTest()}).To(Equal([]int64{0, 0}))
			end()
			Consistently(prefetched).WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeZero(), "the context's end gives nothing back twice")
		})

		It("bounds the live copies of prefetched heads by the prefetch room, however fast each head is written", func(ctx SpecContext) {
			// A head and 4 KiB more: each GET's head goes out in one write
			// while its tail is still to come.
			data := mibPattern(5)[:4<<20+4<<10]
			mustPut(ctx, meta.ObjKey{Name: "k"}, data, "")
			s.SetMemoryBudgetForTest(32<<20, 0)
			reqCtx, end := context.WithCancel(ctx)
			defer end()
			var live int64
			for range 16 {
				st, err := s.PrefetchObject(reqCtx, rec, meta.ObjKey{Name: "k"})
				Expect(err).NotTo(HaveOccurred())
				Expect(s.ReadObject(reqCtx, st, op.ByteRange{Length: st.Size}, io.Discard)).To(Succeed())
				live += int64(len(st.Head))
			}
			stats := s.MemoryBudgetStats()
			Expect(live).To(BeNumerically("<=", stats.PrefetchCap), "the copies the requests still hold")
			Expect(stats.PrefetchHeld).To(Equal(live), "every live copy is counted")
			Expect(stats.PrefetchSkipped).To(BeEquivalentTo(14), "two 4 MiB heads fill the 8 MiB prefetch room")
		})

		It("serves uncompressed GETs while stalled PUTs fill the hold class", func(ctx SpecContext) {
			data := mibPattern(12)
			mustPut(ctx, meta.ObjKey{Name: "k"}, data, "")
			s.SetMemoryBudgetForTest(16<<20, 50*time.Millisecond)
			stop := stallPUTs(ctx, 2)
			defer stop()
			Expect(read(ctx, meta.ObjKey{Name: "k"})).To(Equal(data))
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "other"}, bytes.NewReader(data), op.PutParams{Attrs: attrsWithACL(initiator), Size: 12 << 20})
			Expect(err).To(MatchError(op.ErrSlowDown), "a new PUT finds no room for its head")
		}, SpecTimeout(10*time.Second))
	})

	It("stops a tail-sharing copy whose client leaves while its head's read waits for the budget, and gives back all it took", func(ctx SpecContext) {
		mustPut(ctx, meta.ObjKey{Name: "src"}, mibPattern(10), "")
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "src"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(BeNil(), "a stat, so the copy reads the head's bytes")
		var tails []string
		for _, oid := range poolObjects(ctx, c, testDataPool, "") {
			if strings.Contains(oid, "__shadow_") {
				tails = append(tails, oid)
			}
		}
		Expect(tails).To(HaveLen(2))
		s.SetMemoryBudgetForTest(64<<20, 0)
		// The budget is spent once the copy holds its head's room and takes
		// its first tail reference, so the head's read waits for a piece.
		release := func() {}
		var once sync.Once
		st2 := s
		c.BeforeWrite(testDataPool, "", tails[0], func(*fakerados.Object) { once.Do(func() { release = st2.FillMemoryBudgetForTest() }) })
		DeferCleanup(func() { release() })
		reqCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := s.CopyObject(reqCtx, st, rec, meta.ObjKey{Name: "dst"}, op.CopyParams{Attrs: attrsWithACL(initiator), Tag: "tx-copy"})
			done <- err
		}()
		Eventually(s.MemoryBudgetStats).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(And(
			HaveField("HoldHeld", BeEquivalentTo(4<<20)), HaveField("PieceWaiting", BeEquivalentTo(1))),
			"the copy holds the head's room and its head's read waits")
		cancel()
		Eventually(done).WithTimeout(5*time.Second).Should(Receive(MatchError(context.Canceled)), "the client left")
		Expect(s.MemoryBudgetStats()).To(And(HaveField("HoldHeld", BeZero()), HaveField("PieceWaiting", BeZero())))
		release()
		Expect(held()).To(BeZero(), "the hold and every piece given back")
		for _, oid := range tails {
			Expect(refsOf(c, testDataPool, oid).Refs).To(Equal(map[string]bool{"": true}), "%s: the copy's reference dropped", oid)
		}
		Expect(c.Object(testDataPool, "", putBucketID+"_dst")).To(BeNil(), "no head written")
	})

	It("waits at most 5 s for a transfer's first draw on a Store Open makes", func() {
		Expect(s.MemoryStartWaitForTest()).To(Equal(5 * time.Second))
	})

	Describe("what it holds for a whole transfer", func() {
		holdHeld := func() int64 { return s.MemoryBudgetStats().HoldHeld }
		// decoderOf is what decoding st's whole object holds: its largest
		// stored block and its largest decoded one.
		decoderOf := func(st *op.ObjectState) int64 {
			ci := st.Compression
			var stored, decoded uint64
			for i, b := range ci.Blocks {
				end := ci.OrigSize
				if i+1 < len(ci.Blocks) {
					end = ci.Blocks[i+1].OldOfs
				}
				stored, decoded = max(stored, b.Len), max(decoded, end-b.OldOfs)
			}
			return int64(stored + decoded) //nolint:gosec // block lengths of a 12 MiB object
		}
		BeforeEach(func() { s.SetMemoryBudgetForTest(256<<20, 0) })

		It("holds a compressing PUT's head and both of its block buffers", func(ctx SpecContext) {
			reqCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				_, err := s.PutObject(reqCtx, rec, meta.ObjKey{Name: "k"}, ctxReader{reqCtx},
					op.PutParams{Attrs: attrsWithACL(initiator), Size: 64 << 20, StorageClass: zlibClass})
				done <- err
			}()
			Eventually(holdHeld).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(4<<20+10<<20),
				"a 4 MiB head, a 4 MiB plaintext block and room for its 6 MiB encoded form")
			cancel()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(MatchError(context.Canceled)))
			drained()
		})

		It("holds a compressing part's block buffers", func(ctx SpecContext) {
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "mp"}, op.UploadParams{
				Owner: initiator, OwnerName: "Alice",
				Placement: meta.PlacementRule{Name: "default-placement", StorageClass: zlibClass}, Attrs: attrsWithACL(initiator),
			})
			Expect(err).NotTo(HaveOccurred())
			reqCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				_, err := s.PutPart(reqCtx, up, 1, ctxReader{reqCtx}, op.PutParams{Attrs: attrsWithACL(initiator), Size: 64 << 20})
				done <- err
			}()
			Eventually(holdHeld).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(10<<20),
				"a part keeps no head, only the compressor's buffers")
			cancel()
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(MatchError(context.Canceled)))
			drained()
		})

		It("holds a copy's read window, which covers its read side", func(ctx SpecContext) {
			src := mibPattern(10)
			mustPut(ctx, meta.ObjKey{Name: "src"}, src, "")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "src"})
			Expect(err).NotTo(HaveOccurred())
			s.SetRandForTest(fixedRand(secondPrefix))
			release := make(chan struct{})
			var once sync.Once
			c.BeforeWrite(coldPool, "", tailOID(secondPrefix, 0), func(*fakerados.Object) { once.Do(func() { <-release }) })
			done := make(chan error, 1)
			go func() {
				_, err := s.CopyObject(ctx, st, rec, meta.ObjKey{Name: "dst"}, op.CopyParams{Attrs: attrsWithACL(initiator), StorageClass: "COLD", Tag: "tx-copy"})
				done <- err
			}()
			Eventually(holdHeld).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(16<<20),
				"the 16 MiB read window; the COLD pool keeps no head and does not compress")
			close(release)
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
			drained()
		})

		It("holds the head bytes a tail-sharing copy reads", func(ctx SpecContext) {
			src := mibPattern(10)
			mustPut(ctx, meta.ObjKey{Name: "src"}, src, "")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "src"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Head).To(BeNil(), "a stat, so the copy reads the head's bytes")
			release := make(chan struct{})
			var once sync.Once
			c.BeforeRead(testDataPool, "", putBucketID+"_src", func() { once.Do(func() { <-release }) })
			done := make(chan error, 1)
			go func() {
				_, err := s.CopyObject(ctx, st, rec, meta.ObjKey{Name: "dst"}, op.CopyParams{Attrs: attrsWithACL(initiator), Tag: "tx-copy"})
				done <- err
			}()
			Eventually(holdHeld).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(4<<20), "the source's 4 MiB head")
			close(release)
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
			drained()
		})

		It("holds a part copy's read window and its compressed source's decoder blocks", func(ctx SpecContext) {
			plain := hexNoise(12 << 20)
			mustPut(ctx, meta.ObjKey{Name: "csrc"}, plain, zlibClass)
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "csrc"})
			Expect(err).NotTo(HaveOccurred())
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "mp"}, op.UploadParams{
				Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
			})
			Expect(err).NotTo(HaveOccurred())
			release := make(chan struct{})
			var once sync.Once
			c.BeforeWrite(testDataPool, "", partHeadOID("mp."+up.ID, 1), func(*fakerados.Object) { once.Do(func() { <-release }) })
			done := make(chan error, 1)
			go func() {
				_, err := s.CopyPart(ctx, up, 1, st, op.ByteRange{Length: uint64(len(plain))})
				done <- err
			}()
			Eventually(holdHeld).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(Equal(16<<20+decoderOf(st)),
				"the 16 MiB read window and the decoder's blocks; the plain upload does not compress")
			close(release)
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
			drained()
		})

		It("holds a compressed GET's decoder blocks", func(ctx SpecContext) {
			plain := hexNoise(12 << 20)
			mustPut(ctx, meta.ObjKey{Name: "comp"}, plain, zlibClass)
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "comp"})
			Expect(err).NotTo(HaveOccurred())
			sink := &gateSink{release: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- s.ReadObject(ctx, st, op.ByteRange{Length: uint64(len(plain))}, sink) }()
			Eventually(holdHeld).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Equal(decoderOf(st)))
			close(sink.release)
			Eventually(done).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
			drained()
		})
	})

	DescribeTable("gives back every share a GET took when the GET fails",
		func(ctx SpecContext, fail func(reqCtx context.Context, cancel context.CancelFunc) io.Writer, check func(func() error)) {
			mustPut(ctx, meta.ObjKey{Name: "big"}, mibPattern(40), "")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "big"})
			Expect(err).NotTo(HaveOccurred())
			s.SetMemoryBudgetForTest(64<<20, 0)
			reqCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			sink := fail(reqCtx, cancel)
			check(func() error { return s.ReadObject(reqCtx, st, op.ByteRange{Length: st.Size}, sink) })
			Expect(s.MemoryBudgetStats().PieceHeld).To(BeZero(), "no share kept by the results still queued")
			Expect(s.LentBufBytesForTest()).To(BeZero(), "no buffer of theirs still counted as lent by the free list")
		},
		Entry("its client's connection fails", func(context.Context, context.CancelFunc) io.Writer {
			return &failingSink{fail: func([]byte) (int, error) { return 0, errors.New("connection reset") }}
		}, func(read func() error) { Expect(read()).To(MatchError("connection reset")) }),
		Entry("its client goes away", func(_ context.Context, cancel context.CancelFunc) io.Writer {
			return &failingSink{fail: func([]byte) (int, error) {
				cancel()
				return 0, context.Canceled
			}}
		}, func(read func() error) { Expect(read()).To(MatchError(context.Canceled)) }),
		Entry("its sink panics, which the request's recovery survives", func(context.Context, context.CancelFunc) io.Writer {
			return &failingSink{fail: func([]byte) (int, error) { panic("sink") }}
		}, func(read func() error) { Expect(func() { Expect(read()).To(Succeed()) }).To(PanicWith("sink")) }),
	)

	It("gives back every share a GET took when a RADOS read fails", func(ctx SpecContext) {
		mustPut(ctx, meta.ObjKey{Name: "big"}, mibPattern(40), "")
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "big"})
		Expect(err).NotTo(HaveOccurred())
		s.SetMemoryBudgetForTest(64<<20, 0)
		var tails []string
		for _, oid := range poolObjects(ctx, c, testDataPool, "") {
			if strings.Contains(oid, "__shadow_") {
				tails = append(tails, oid)
			}
		}
		slices.Sort(tails)
		Expect(len(tails)).To(BeNumerically(">=", 3))
		c.FailNextRead(testDataPool, "", tails[2], syscall.EIO)
		Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, io.Discard)).To(HaveOccurred())
		Expect(s.MemoryBudgetStats().PieceHeld).To(BeZero())
		Expect(s.LentBufBytesForTest()).To(BeZero())
	})
})

// sinkBuffer is a response sink a spec reads while a read fills it.
type sinkBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *sinkBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *sinkBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *sinkBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buf.Bytes())
}

// gateSink takes no write until release is closed.
type gateSink struct{ release chan struct{} }

func (g *gateSink) Write(p []byte) (int, error) {
	<-g.release
	return len(p), nil
}
