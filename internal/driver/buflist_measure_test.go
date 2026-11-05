//go:build measure

package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	rtmetrics "runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// heapSample is the heap at one moment after the burst began: in use
// (objects and the unused part of their spans, MemStats.HeapInuse), free and
// still resident, released to the OS, the process's VmRSS, and the 4 MiB
// blocks the free list holds, -1 for a Store without one.
type heapSample struct {
	at                         time.Duration
	inUse, free, released, rss uint64
	idle                       int
}

var heapMetrics = []string{
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/free:bytes",
	"/memory/classes/heap/released:bytes",
}

func sampleHeap(s *driver.Store, since time.Time) heapSample {
	ms := make([]rtmetrics.Sample, len(heapMetrics))
	for i, name := range heapMetrics {
		ms[i].Name = name
	}
	rtmetrics.Read(ms)
	return heapSample{
		at:       time.Since(since),
		inUse:    ms[0].Value.Uint64() + ms[1].Value.Uint64(),
		free:     ms[2].Value.Uint64(),
		released: ms[3].Value.Uint64(),
		rss:      vmRSS(),
		idle:     s.IdleBufsForTest(4 << 20),
	}
}

// vmRSS is the process's resident set, 0 when /proc says nothing.
func vmRSS() uint64 {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(status)) {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}

func (h heapSample) row(label string) string {
	mib := func(n uint64) string { return strconv.FormatUint(n>>20, 10) }
	idle := "n/a"
	if h.idle >= 0 {
		idle = strconv.Itoa(h.idle)
	}
	return fmt.Sprintf("| %s | %.1f s | %s | %s | %s | %s | %s |\n", label, h.at.Seconds(), mib(h.inUse), mib(h.free), mib(h.released), mib(h.rss), idle)
}

// The free list's measurement, which make check does not run:
//
//	go test -tags=ceph_preview,measure ./internal/driver -ginkgo.label-filter=measure
//
// It drives a burst of whole-object GETs, each a 4 MiB head prefetch and
// 4 MiB read pieces, through fakerados under a 2 GiB pod's Go memory limit,
// then idles past the free list's trim. It appends the heap at the burst's
// peak, just before the trim and after it to the file RGW_GO_MEASURE_OUT,
// and every sample to that file with .tsv added. RGW_GO_MEASURE_TRIM_AT, in
// seconds after the burst ends, is when to take the before and after
// samples on a Store without the list, which has no trim to find.
var _ = Describe("the free list's memory over a burst and an idle trim", Label("measure"), func() {
	It("records the heap at the burst's peak, before the trim and after it", func(ctx SpecContext) {
		const (
			limit   = 1434 << 20 // GOMEMLIMIT at a 2 GiB pod's memory-limit ratio of 0.7
			objLen  = 32 << 20
			clients = 64
			rounds  = 4
			idleFor = 330 * time.Second
			poll    = 250 * time.Millisecond
		)
		out := os.Getenv("RGW_GO_MEASURE_OUT")
		Expect(out).NotTo(BeEmpty(), "RGW_GO_MEASURE_OUT names the file the rows go to")
		var trimAt time.Duration
		if v := os.Getenv("RGW_GO_MEASURE_TRIM_AT"); v != "" {
			secs, err := strconv.ParseFloat(v, 64)
			Expect(err).NotTo(HaveOccurred())
			trimAt = time.Duration(secs * float64(time.Second))
		}
		prev := debug.SetMemoryLimit(limit)
		DeferCleanup(func() { debug.SetMemoryLimit(prev) })

		c := newPutCluster()
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			p.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: -1}
		})
		rec := testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		rel := denc.Squid
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{Release: &rel, MemoryLimit: limit})
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(uniqueRand())
		key := meta.ObjKey{Name: "burst"}
		_, err = s.PutObject(ctx, rec, key, bytes.NewReader(mibPattern(objLen>>20)),
			op.PutParams{Attrs: attrsWithACL(initiator), Size: objLen})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		debug.FreeOSMemory()

		get := func() error {
			reqCtx, end := context.WithCancel(ctx)
			defer end()
			st, perr := s.PrefetchObject(reqCtx, rec, key)
			if perr != nil {
				return perr
			}
			defer st.ReleaseHead()
			return s.ReadObject(reqCtx, st, op.ByteRange{Length: st.Size}, io.Discard)
		}
		began := time.Now()
		var (
			peak    heapSample
			stopped = make(chan struct{})
			sampled = make(chan struct{})
		)
		go func() {
			defer close(sampled)
			for {
				if h := sampleHeap(s, began); h.inUse > peak.inUse {
					peak = h
				}
				select {
				case <-stopped:
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}()
		errs := make([]error, clients)
		var wg sync.WaitGroup
		for i := range clients {
			wg.Go(func() {
				for range rounds {
					if errs[i] = get(); errs[i] != nil {
						return
					}
				}
			})
		}
		wg.Wait()
		close(stopped)
		<-sampled
		Expect(errs).To(HaveEach(Succeed()))
		burst := time.Since(began)

		var samples []heapSample
		var before, after *heapSample
		for time.Since(began) < burst+idleFor {
			h := sampleHeap(s, began)
			h.at -= burst
			if n := len(samples); trimAt == 0 && n > 0 && samples[n-1].idle > h.idle && h.idle >= 0 {
				trimAt = h.at
			}
			samples = append(samples, h)
			select {
			case <-ctx.Done():
				Fail("the measurement was cut short")
			case <-time.After(poll):
			}
		}
		Expect(trimAt).NotTo(BeZero(), "the free list trimmed, or RGW_GO_MEASURE_TRIM_AT says when")
		for i := range samples {
			if samples[i].at < trimAt {
				before = &samples[i]
			}
			if after == nil && samples[i].at >= trimAt+2*time.Second {
				after = &samples[i]
			}
		}
		Expect(before).NotTo(BeNil())
		Expect(after).NotTo(BeNil())
		peak.at -= burst

		variant := "free list"
		if peak.idle < 0 {
			variant = "sync.Pool"
		}
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(f.Close)
		fmt.Fprintf(f, "\n%s: %d clients x %d whole GETs of %d MiB in %.1f s, budget %d MiB, trim at %.1f s after the burst\n\n",
			variant, clients, rounds, objLen>>20, burst.Seconds(), s.MemoryBudgetStats().Size>>20, trimAt.Seconds())
		fmt.Fprint(f, "| moment | after burst | heap in use MiB | heap free MiB | heap released MiB | VmRSS MiB | idle 4 MiB blocks |\n|---|---|---|---|---|---|---|\n")
		fmt.Fprint(f, peak.row("peak"), before.row("before trim"), after.row("after trim"), samples[len(samples)-1].row("end"))
		tsv, err := os.OpenFile(out+".tsv", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tsv.Close)
		for _, h := range samples {
			fmt.Fprintf(tsv, "%s\t%.2f\t%d\t%d\t%d\t%d\t%d\n", variant, h.at.Seconds(), h.inUse, h.free, h.released, h.rss, h.idle)
		}
	}, SpecTimeout(15*time.Minute))
})
