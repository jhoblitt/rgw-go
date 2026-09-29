package seam_test

import (
	"crypto/sha256"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/test/bench/seam"
)

var _ = Describe("Summarize", func() {
	It("returns the percentiles of the sorted durations without sorting the input", func() {
		d := []time.Duration{9, 1, 5, 3, 7}
		p := seam.Summarize(d)
		Expect(p.P50).To(Equal(time.Duration(5)))
		Expect(p.P99).To(Equal(time.Duration(9)))
		Expect(p.P999).To(Equal(time.Duration(9)))
		Expect(p.Mean).To(Equal(time.Duration(5)))
		Expect(d).To(Equal([]time.Duration{9, 1, 5, 3, 7}), "input left in place")
	})

	It("takes the duration at rank n*q of a thousand", func() {
		d := make([]time.Duration, 1000)
		for i := range d {
			d[i] = time.Duration(len(d) - i)
		}
		Expect(seam.Summarize(d)).To(Equal(seam.Percentiles{P50: 501, P99: 991, P999: 1000, Mean: 500}))
	})

	It("panics on no durations", func() {
		Expect(func() { seam.Summarize(nil) }).To(Panic())
	})
})

var _ = Describe("OSThreads", func() {
	It("counts at least this goroutine's thread", func() {
		n, err := seam.OSThreads()
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeNumerically(">=", 1))
	})
})

var _ = Describe("CPUTime", func() {
	It("grows while the process works", func() {
		before, err := seam.CPUTime()
		Expect(err).NotTo(HaveOccurred())
		var sum [sha256.Size]byte
		Eventually(func(g Gomega) {
			sum = sha256.Sum256(sum[:])
			now, err := seam.CPUTime()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(now).To(BeNumerically(">", before))
		}).WithTimeout(10 * time.Second).WithPolling(time.Millisecond).Should(Succeed())
	})
})

var _ = Describe("Sampler", func() {
	It("reports a peak no lower than the count at start", func() {
		// The process's own count can fall between specs, as another spec's
		// locked threads finish exiting, so the counts are scripted: 7 while
		// the Sampler starts and 3 once it has, with no poll in an hour.
		var started atomic.Bool
		s := seam.NewSamplerFrom(time.Hour, func() (int, error) {
			if started.Load() {
				return 3, nil
			}
			return 7, nil
		})
		started.Store(true)
		Expect(s.Stop()).To(Equal(7))
	})

	It("keeps the highest count its polls see between start and stop", func() {
		// The start sample reads 5 and every sample after the first poll,
		// Stop's included, reads 3, so only a poll can report the 9.
		polled := make(chan struct{}, 1)
		var calls atomic.Int32
		s := seam.NewSamplerFrom(time.Millisecond, func() (int, error) {
			switch calls.Add(1) {
			case 1:
				return 5, nil
			case 2:
				polled <- struct{}{}
				return 9, nil
			default:
				return 3, nil
			}
		})
		Eventually(polled).WithTimeout(10 * time.Second).WithPolling(time.Millisecond).Should(Receive())
		Expect(s.Stop()).To(Equal(9))
	})

	It("counts the threads running when it stops", func() {
		before, err := seam.OSThreads()
		Expect(err).NotTo(HaveOccurred())
		// An hour's interval leaves no poll between start and stop, so only
		// the count Stop takes can see the threads started in between.
		s := seam.NewSampler(time.Hour)
		release := make(chan struct{})
		var locked, exited sync.WaitGroup
		DeferCleanup(func() {
			close(release)
			exited.Wait()
		})
		// Each goroutine holds an OS thread of its own until released, and
		// exits still locked so that the runtime ends the thread with it.
		k := before + 1
		locked.Add(k)
		for range k {
			exited.Go(func() {
				runtime.LockOSThread()
				locked.Done()
				<-release
			})
		}
		locked.Wait()
		Expect(s.Stop()).To(BeNumerically(">", k), "%d locked threads and the one running this spec", k)
	})
})
