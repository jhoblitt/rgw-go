package gateway_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
)

// clock is a fake clock that moves step at each reading.
type clock struct {
	mu   sync.Mutex
	next time.Time
	last time.Time
	step time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = c.next
	c.next = c.next.Add(c.step)
	return c.last
}

// read is the time the clock last gave.
func (c *clock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

var _ = Describe("SampleLine", func() {
	It("writes the time, user and system CPU in seconds, threads and resident KiB as the report reads them", func() {
		t := time.Date(2026, 10, 13, 7, 11, 45, 537_600_000, time.UTC)
		Expect(gateway.SampleLine(t, []byte(procStat("rgw-go", 2216, 543)+procStatus(605, 172412)))).
			To(Equal("1791875505.538,22.16,5.43,605,172412\n"))
	})

	It("counts stat's fields from the last parenthesis, past a name that holds spaces and parentheses", func() {
		Expect(gateway.SampleLine(runStart, []byte(procStat("rgw go) (x", 100, 50)+procStatus(4, 1024)))).
			To(Equal("1791875505.000,1.00,0.50,4,1024\n"))
	})

	DescribeTable("fails a read that lacks what a sample needs",
		func(data, want string) {
			_, err := gateway.SampleLine(runStart, []byte(data))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a stat line cut short", "1 (radosgw) S 1 1\n"+procStatus(4, 1024), "no utime and stime"),
		Entry("no Threads", procStat("radosgw", 1, 1)+"VmRSS:\t1024 kB\n", "no thread count"),
		Entry("no VmRSS, as a zombie's status has", procStat("radosgw", 1, 1)+"Threads:\t4\n", "no resident memory"),
	)
})

var _ = Describe("the sampler", func() {
	var (
		path string
		log  *bytes.Buffer
		c    *clock
		stop chan struct{}
	)

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "samples-small-get.csv")
		log = &bytes.Buffer{}
		// Three readings a sample, one each side of the read and one to
		// schedule the next, so that every sample is due as the last ends.
		c = &clock{next: runStart, step: time.Second}
		stop = make(chan struct{})
	})

	pid := func(context.Context) (string, error) { return rgwGoPid, nil }
	// process is the process's stat and status at the n-th read: its user
	// CPU a second a read.
	process := func(n int) []byte { return []byte(procStat("rgw-go", 100*n, 50) + procStatus(21, 156504)) }
	sample := func(ctx context.Context, read func(context.Context, string) ([]byte, error)) error {
		return gateway.Sample(ctx, stop, path, "pid:"+rgwGoPid, pid, read, c.now, 3*time.Second,
			slog.New(slog.NewJSONHandler(log, nil)))
	}
	readFile := func() string {
		GinkgoHelper()
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	It("writes a new file's header, then a sample a second until it is stopped", func(ctx SpecContext) {
		reads := 0
		Expect(sample(ctx, func(context.Context, string) ([]byte, error) {
			if reads++; reads == 3 {
				close(stop)
			}
			return process(reads), nil
		})).To(Succeed())
		Expect(readFile()).To(Equal("time,utime,stime,threads,rss_kib\n" +
			"1791875506.500,1.00,0.50,21,156504\n" +
			"1791875509.500,2.00,0.50,21,156504\n" +
			"1791875512.500,3.00,0.50,21,156504\n"))
		Expect(log.String()).To(ContainSubstring(`"msg":"sampled","target":"pid:777","taken":3,"skipped":0`))
	})

	It("appends to a file that has samples, under no second header", func(ctx SpecContext) {
		Expect(os.WriteFile(path, []byte("time,utime,stime,threads,rss_kib\n1791875500.000,0.50,0.25,20,150000\n"), 0o600)).To(Succeed())
		close(stop)
		Expect(sample(ctx, func(context.Context, string) ([]byte, error) { return process(1), nil })).To(Succeed())
		Expect(readFile()).To(Equal("time,utime,stime,threads,rss_kib\n1791875500.000,0.50,0.25,20,150000\n" +
			"1791875506.500,1.00,0.50,21,156504\n"))
	})

	It("tries a failed sample once more, then skips it, and carries on", func(ctx SpecContext) {
		reads := 0
		Expect(sample(ctx, func(context.Context, string) ([]byte, error) {
			reads++
			switch reads {
			case 1, 3, 4:
				return nil, errors.New("exit status 1: error: unable to upgrade connection")
			case 5:
				close(stop)
			}
			return process(reads), nil
		})).To(Succeed())
		Expect(reads).To(Equal(5), "the first sample's second try, the second sample's two, the third's one")
		Expect(strings.Split(readFile(), "\n")).To(HaveLen(4), "the header, the first and the third samples, and the end")
		Expect(log.String()).To(SatisfyAll(
			ContainSubstring(`"level":"WARN","msg":"skipped a sample","target":"pid:777","pid":"777","skipped":1,"error":"exit status 1: error: unable to upgrade connection"`),
			ContainSubstring(`"msg":"sampled","target":"pid:777","taken":2,"skipped":1`)))
	})

	It("fails when it cannot find the process, writing nothing", func(ctx SpecContext) {
		err := gateway.Sample(ctx, stop, path, "pod:"+pod,
			func(context.Context) (string, error) {
				return "", errors.New("pod rook-ceph-rgw runs no single process named radosgw: pids ''")
			},
			func(context.Context, string) ([]byte, error) { return nil, nil }, c.now, time.Second, slog.New(slog.NewJSONHandler(log, nil)))
		Expect(err).To(MatchError(ContainSubstring("runs no single process named radosgw")))
		Expect(path).NotTo(BeAnExistingFile())
	})

	It("writes samples hack/bench/report reads for the phase they bracket", func(ctx SpecContext) {
		gomod, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
		Expect(err).NotTo(HaveOccurred())
		root := filepath.Dir(strings.TrimSpace(string(gomod)))
		dir := filepath.Dir(path)
		Expect(os.WriteFile(filepath.Join(dir, "env.json"), []byte(`{"release":"tentacle","gateway":"rgw-go"}`), 0o600)).To(Succeed())
		// One phase of 1000 GETs from 07:11:50 for 2 s, which the samples
		// bracket.
		Expect(os.WriteFile(filepath.Join(dir, "small-get.json"), []byte(`{"phase_type":"READ",`+
			`"iso_start_date":"2026-10-13T07:11:50.000+0000","config":{"threads":"8","file_size":"4096","block_size":"4096",`+
			`"command":"elbencho -r -t 8 bench-small"},"last_done":{"elapsed_time_ms":"2000","entries":"1000","ios":"1000",`+
			`"bytes":"4096000","latency":{"IO":{"histogram":{"buckets":[{"range":"500-600us","count":"1000"}]}}}}}`+"\n"), 0o600)).
			To(Succeed())
		// Samples every 1.5 s from 07:11:45.75, each read while the process has
		// used a second of CPU a second since 07:11:45, at 4242 threads and
		// 777 MiB resident.
		c.step = 500 * time.Millisecond
		reads := 0
		Expect(gateway.Sample(ctx, stop, path, "pid:"+rgwGoPid, pid, func(context.Context, string) ([]byte, error) {
			if reads++; reads == 8 {
				close(stop)
			}
			used := c.read().Add(c.step/2).Sub(runStart) / (10 * time.Millisecond)
			return []byte(procStat("rgw-go", int(used), 0) + procStatus(4242, 777<<10)), nil
		}, c.now, 1500*time.Millisecond, slog.New(slog.NewJSONHandler(log, nil)))).To(Succeed())
		out, err := harness.NewExecer(time.Second).Output(ctx, harness.Cmd{Name: "go", Args: []string{
			"-C", root, "run", "-tags=ceph_preview", "./hack/bench/report", "gateway", "--dir", dir,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(HavePrefix(filepath.Join(dir, "REPORT.md")))
		report, err := os.ReadFile(filepath.Join(dir, "REPORT.md"))
		Expect(err).NotTo(HaveOccurred())
		// Two seconds of CPU over 1000 requests, the samples' threads and RSS.
		Expect(strings.Split(string(report), "\n")).To(ContainElement("| GET 4 KiB | 8 | 2.00 | 4242 | 777 | 0 | 0 |"),
			"the cost row in\n%s", report)
	}, SpecTimeout(5*time.Minute))

	It("fails the run after the profile when the radosgw pod runs no single radosgw, and still undoes it", func(ctx SpecContext) {
		w := newWorld("radosgw")
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if sampler(c) && slices.Contains(c.Args, "sh") {
				return []byte("12\n34\n"), nil
			}
			return w.output(ctx, c)
		}
		Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"noop"}}))).To(MatchError(
			"noop: the sampler of pod:" + pod + " failed: pod " + pod + " runs no single process named radosgw: pids '12 34'"))
		Expect(w.trace()).To(ContainSubstring("noop-8.json"), "the profile ran")
		Expect(w.trace()).To(HaveSuffix(exitTail("radosgw")), "noop wrote no bucket")
	})
})
