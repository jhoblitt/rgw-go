package main

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("the sampler's parsers", func() {
	It("reads status fields in kB and the thread count", func() {
		status := "Name:\trgw-go\nVmHWM:\t  123456 kB\nVmRSS:\t  120000 kB\nRssAnon:\t   80000 kB\n" +
			"RssFile:\t   40000 kB\nRssShmem:\t       0 kB\nThreads:\t29\nVmSwap:\t       0 kB\n"
		Expect(parseKV([]byte(status), statusKeys)).To(Equal(map[string]int64{
			"VmHWM": 123456, "VmRSS": 120000, "RssAnon": 80000, "RssFile": 40000, "RssShmem": 0, "Threads": 29,
		}))
	})

	It("reads smaps_rollup's Pss fields without taking Pss_Dirty for Pss", func() {
		rollup := "55d79ad4b000-7ffe2bed5000 ---p 00000000 00:00 0  [rollup]\nRss:  230480 kB\n" +
			"Pss:  223260 kB\nPss_Dirty:  207224 kB\nPss_Anon:  207224 kB\nPss_File:  16036 kB\n"
		Expect(parseKV([]byte(rollup), rollupKeys)).To(Equal(map[string]int64{
			"Rss": 230480, "Pss": 223260, "Pss_Anon": 207224, "Pss_File": 16036,
		}))
	})

	It("reads a cgroup's current, peak and memory.stat fields", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "memory.current"), []byte("241545216\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "memory.peak"), []byte("291307520\n"), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "memory.stat"),
			[]byte("anon 200000000\nfile 30000000\nkernel 9000000\nactive_file 1000\ninactive_file 2000\nanon_thp 0\n"), 0o644)).To(Succeed())
		Expect(readCgroup(dir)).To(Equal(map[string]int64{
			"current": 241545216, "peak": 291307520, "anon": 200000000, "file": 30000000,
			"kernel": 9000000, "active_file": 1000, "inactive_file": 2000,
		}))
	})

	It("keeps the selected metrics, labelled series whole, and sums the summed ones", func() {
		text := "# HELP go_memstats_heap_inuse_bytes x\n# TYPE go_memstats_heap_inuse_bytes gauge\n" +
			"go_memstats_heap_inuse_bytes 1.2e+07\n" +
			"go_gc_duration_seconds{quantile=\"0.5\"} 0.0001\ngo_gc_duration_seconds_count 42\n" +
			"rgw_go_memory_budget_held_bytes{class=\"piece\"} 4194304\n" +
			"rgw_go_memory_budget_free_list_bytes 1.6777216e+07\n" +
			"rgw_go_requests_in_flight{op=\"get_obj\"} 3\nrgw_go_requests_in_flight{op=\"put_obj\"} 2\n" +
			"rgw_go_request_duration_seconds_bucket{le=\"0.1\"} 7\n" +
			"go_gc_gomemlimit_bytes 1.717986918e+09\n"
		Expect(selectMetrics([]byte(text))).To(Equal(map[string]float64{
			"go_memstats_heap_inuse_bytes":                   1.2e7,
			"go_gc_duration_seconds_count":                   42,
			`rgw_go_memory_budget_held_bytes{class="piece"}`: 4194304,
			"rgw_go_memory_budget_free_list_bytes":           16777216,
			"rgw_go_requests_in_flight":                      5,
			"go_gc_gomemlimit_bytes":                         1717986918,
		}))
	})

	It("splits a series whose label value holds a space", func() {
		s, v, ok := splitSeries(`x{a="b c"} 2 1700000000000`)
		Expect(ok).To(BeTrue())
		Expect(s).To(Equal(`x{a="b c"}`))
		Expect(v).To(Equal(2.0))
	})
})

var _ = Describe("the mix", func() {
	It("gives the same requests for the same seed", func() {
		Expect(sequence(7, 1000)).To(Equal(sequence(7, 1000)))
		Expect(sequence(7, 1000)).NotTo(Equal(sequence(8, 1000)))
	})

	It("draws the owner's shares of ops and sizes", func() {
		seq := sequence(20261104, 200000)
		var puts int
		perClass := make([]int, len(classes))
		for _, r := range seq {
			if r.Put {
				puts++
			}
			perClass[r.Class]++
			Expect(r.Key).To(BeNumerically("<", classes[r.Class].Keys))
		}
		n := float64(len(seq))
		Expect(float64(puts) / n).To(BeNumerically("~", 0.30, 0.005))
		Expect(float64(perClass[0]) / n).To(BeNumerically("~", 0.50, 0.005))
		Expect(float64(perClass[1]) / n).To(BeNumerically("~", 0.30, 0.005))
		Expect(float64(perClass[2]) / n).To(BeNumerically("~", 0.15, 0.005))
		Expect(float64(perClass[3]) / n).To(BeNumerically("~", 0.05, 0.005))
	})

	It("keeps GETs on the get set and PUTs on the put set, but for the prep's", func() {
		Expect(request{Class: 2, Key: 3}.key()).To(Equal("get/64m/00003"))
		Expect(request{Put: true, Class: 3, Key: 1}.key()).To(Equal("put/256m/00001"))
		Expect(request{Put: true, Class: 0, Key: 9, Prep: true}.key()).To(Equal("get/4k/00009"))
	})

	It("takes nearest-rank quantiles", func() {
		d := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
		Expect(quantile(d, 0.5)).To(Equal(time.Duration(5)))
		Expect(quantile(d, 0.99)).To(Equal(time.Duration(10)))
		Expect(quantile(d, 1)).To(Equal(time.Duration(10)))
		Expect(quantile(nil, 0.5)).To(BeZero())
	})
})
