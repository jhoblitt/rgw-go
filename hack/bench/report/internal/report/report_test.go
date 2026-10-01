package report_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/hack/bench/report/internal/report"
)

// edit changes a JSON line of one of a sweep directory's files in place, or
// returns false to drop it.
type edit func(file string, line map[string]any) bool

// sweep copies the files at testdata's top level into a new directory,
// passing every line of each .jsonl file through edits.
func sweep(edits ...edit) string {
	GinkgoHelper()
	dir := GinkgoT().TempDir()
	entries, err := os.ReadDir("testdata")
	Expect(err).NotTo(HaveOccurred())
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		Expect(err).NotTo(HaveOccurred())
		if strings.HasSuffix(e.Name(), ".jsonl") {
			data = editLines(e.Name(), data, edits)
		}
		Expect(os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600)).To(Succeed())
	}
	return dir
}

func editLines(file string, data []byte, edits []edit) []byte {
	GinkgoHelper()
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var line map[string]any
		Expect(json.Unmarshal(sc.Bytes(), &line)).To(Succeed())
		keep := true
		for _, e := range edits {
			keep = keep && e(file, line)
		}
		if !keep {
			continue
		}
		enc, err := json.Marshal(line)
		Expect(err).NotTo(HaveOccurred())
		out.Write(append(enc, '\n'))
	}
	Expect(sc.Err()).NotTo(HaveOccurred())
	return out.Bytes()
}

// is reports whether a JSON line is the named cell or floor line.
func is(line map[string]any, mode, shape string, conc int) bool {
	return (mode == "" || line["mode"] == mode) && line["shape"] == shape && line["conc"] == float64(conc)
}

func verdict(mode string, c report.Criterion, s report.Status, margin string) types.GomegaMatcher {
	return SatisfyAll(
		HaveField("Mode", mode),
		HaveField("Criterion", c),
		HaveField("Status", s),
		HaveField("Margin", ContainSubstring(margin)),
	)
}

var _ = Describe("Seam", func() {
	It("renders one table per shape with the floor ratios", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		for _, shape := range []string{"read4k", "write4k", "read4m", "write4m"} {
			Expect(r.Markdown).To(ContainSubstring("\n## "+shape+"\n"), "shape %s", shape)
		}
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 64 \| callback \| \d+ \| 54000 \| .* \| 57000 \| 1120 \| 0\.95x \| 1\.05x \|$`),
			"read4k at 64: 54000 ops/s against the floor's 57000, a mean of 1180 µs against 1120")
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 4 \| callback \| \d+ \| 520 \| .* \| 560 \| 7100 \| 0\.93x \| 1\.08x \|$`),
			"read4m at 4, a concurrency only the 4 MiB shapes run")
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 16 \| callback \| \d+ \| 30000 \| .* \| — \| — \| — \| — \|$`),
			"read4k at 16 has no floor")
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Pass, "worst 0.92x (read4k at 256), margin +0.12")))
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Latency, report.Pass, "worst 1.09x (read4k at 256), margin +0.16")))
	})

	It("renders the run's environment", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(HavePrefix("# Seam microbenchmark: squid\n"))
		Expect(r.Markdown).To(ContainSubstring("| ceph_version | 19.2.6 |"))
		Expect(r.Markdown).To(ContainSubstring("| nproc | 32 |"))
	})

	It("keeps the largest-n line of a cell that testing.B called more than once", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 64 \| callback \| 1080000 \| 54000 \| `),
			"the n=1080000 call, not the n=1 and n=100 calls that follow it in the file")
		Expect(r.Markdown).NotTo(MatchRegexp(`(?m)^\| 64 \| callback \| (1|100) \| `))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 64 \| sync \| 294000 \| 14280 \| `),
			"the n=294000 call, not the n=1 call before it")
	})

	It("judges callback and pipe, and reports sync as the baseline", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("sync", report.Threads, report.Fail, "worst +70 of 16 allowed (read4k at 256), margin -54"),
			HaveField("Judged", false),
		)), "the sync cell in testdata grows 70 threads")
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("callback", report.Threads, report.Pass, "worst +2 of 16 allowed"),
			HaveField("Judged", true),
		)))
		Expect(r.Passing).To(Equal([]string{"callback"}), "sync's failure does not count against the release")
		Expect(r.Markdown).To(ContainSubstring("callback passes all four criteria"))
	})

	It("judges the 4 MiB shapes at 1, 4 and 16 in flight, and is incomplete without one", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			return file != "floor.jsonl" || !is(line, "", "write4m", 4)
		})
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Incomplete, "missing: write4m at 4 (no floor)")))
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Latency, report.Incomplete, "missing: write4m at 4 (no floor)")))
		Expect(r.Passing).To(BeEmpty())
		Expect(r.Markdown).To(ContainSubstring("no judged mode passes all four criteria"))
	})

	It("fails a criterion on its worst cell and gives the margin", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			if file == "seam.jsonl" && is(line, "callback", "write4k", 256) {
				line["ops_per_sec"] = 12000.0
				line["mean_us"] = 21000.0
			}
			return true
		})
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Fail, "worst 0.75x (write4k at 256), margin -0.05")))
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Latency, report.Fail, "worst 1.31x (write4k at 256), margin -0.06")))
		Expect(r.Passing).To(BeEmpty())
	})

	It("counts a cell whose iterations failed as missing", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			if file == "seam.jsonl" && is(line, "callback", "read4k", 64) {
				line["errors"] = 3.0
			}
			return true
		})
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Incomplete, "missing: read4k at 64 (3 errors)")))
	})

	It("lists the cells over the byte budget with their limiter and thread growth", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("\n## Over the byte budget\n"))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| callback \| write4m \| 64 \| ops 1048576, bytes 1099511627776 \| .* \| 23→64 \| \+41 \| 16 \| 0 \|$`))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| callback \| write4m \| 64 \| derived \| .* \| 23→24 \| \+1 \| 16 \| 600 \|$`))
	})

	It("judges the cgo share of the read4k profile at 256 in flight", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Cgo, report.Pass, "2.7% of 1.5s sampled, margin +7.3 points")))
		Expect(r.Verdicts).To(ContainElement(verdict("sync", report.Cgo, report.Incomplete, "missing: cpu-sync-read4k-256.pprof")))
		Expect(r.Markdown).To(ContainSubstring("\n## CPU profiles\n"))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| callback \| cpu-callback-read4k-256\.pprof \| 1\.5s \| 2\.7% \| 2\.0% \| 0\.0% \| `),
			"the cgo share, the C under runtime.cgocall, and the threads the Go runtime does not run")
	})

	It("fails when a mode's file is missing a cell another mode has", func(ctx SpecContext) {
		_, err := report.Seam(ctx, "testdata/uneven")
		Expect(err).To(MatchError(ContainSubstring("conc=256 missing for mode pipe")))
	})

	DescribeTable("refuses a floor it cannot match to a mirror shape",
		func(ctx SpecContext, e edit, want string) {
			_, err := report.Seam(ctx, sweep(e))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a shape rados bench does not mirror",
			edit(func(file string, line map[string]any) bool {
				if file == "floor.jsonl" && is(line, "", "read4k", 64) {
					line["shape"] = "indexrtt"
				}
				return true
			}),
			`shape "indexrtt" has no rados bench floor`),
		Entry("two lines for one cell",
			edit(func(file string, line map[string]any) bool {
				if file == "floor.jsonl" && is(line, "", "read4k", 256) {
					line["conc"] = 64.0
				}
				return true
			}),
			"two floor lines for read4k at conc=64"),
	)

	It("refuses a directory without seam cells", func(ctx SpecContext) {
		_, err := report.Seam(ctx, GinkgoT().TempDir())
		Expect(err).To(MatchError(ContainSubstring("no seam cells")))
	})
})

// traces is go tool pprof -traces -unit=ns output, with an inlined frame and
// a sample of a thread the Go runtime does not run.
const traces = `File: seam.test
Type: cpu
Duration: 20.01s, Total samples = 140000000ns ( 0.70%)
-----------+-------------------------------------------------------
40000000ns   runtime.futex
             runtime.notesleep
             runtime.mPark
-----------+-------------------------------------------------------
40000000ns   [libceph-common.so.2]
             runtime._ExternalCode
-----------+-------------------------------------------------------
30000000ns   runtime.cgocall
             github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate
             github.com/ceph/go-ceph/rados.(*ReadOp).OperateAsync
-----------+-------------------------------------------------------
20000000ns   runtime.setPinned
             runtime.(*Pinner).Pin (inline)
             github.com/ceph/go-ceph/rados.(*ReadOp).Read
-----------+-------------------------------------------------------
10000000ns   main.complete
             runtime.cgocallbackg1
             runtime.cgocallbackg
             runtime.cgocallback
-----------+-------------------------------------------------------
`

var _ = Describe("CgoShare", func() {
	It("counts every sample with a cgo or pinner frame anywhere on its stack", func(ctx SpecContext) {
		s, err := report.CgoShare(ctx, "testdata/pprof/heavy.pprof")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Total).To(Equal(1500 * time.Millisecond))
		Expect(s.Cgo).To(Equal(1260*time.Millisecond),
			"runtime.cgocall 270ms, runtime.(*Pinner).Pin 540ms and runtime.(*Pinner).Unpin 450ms cumulative, as go tool pprof -top reports")
		Expect(s.InC).To(Equal(270*time.Millisecond), "runtime.cgocall's flat time, the C the loop called")
		Expect(s.External).To(BeZero(), "the program starts no thread of its own in C")
	})

	It("matches the cgo frames by name, inlined ones included", func() {
		s, err := report.ParseTraces(strings.NewReader(traces))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Total).To(Equal(140 * time.Millisecond))
		Expect(s.Cgo).To(Equal(60*time.Millisecond), "every sample but the futex and the external one")
		Expect(s.InC).To(Equal(30*time.Millisecond), "the sample whose leaf is runtime.cgocall ran in C")
		Expect(s.External).To(Equal(40*time.Millisecond), "the sample of a thread the runtime does not run")
		Expect(s.Frames).To(HaveExactElements(
			report.Frame{Name: "github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate", Cum: 30 * time.Millisecond},
			report.Frame{Name: "runtime.cgocall", Cum: 30 * time.Millisecond},
			report.Frame{Name: "runtime.(*Pinner).Pin", Cum: 20 * time.Millisecond},
			report.Frame{Name: "runtime.cgocallback", Cum: 10 * time.Millisecond},
			report.Frame{Name: "runtime.cgocallbackg", Cum: 10 * time.Millisecond},
			report.Frame{Name: "runtime.cgocallbackg1", Cum: 10 * time.Millisecond},
		))
	})

	It("refuses output with no samples", func() {
		_, err := report.ParseTraces(strings.NewReader("File: seam.test\nType: cpu\n"))
		Expect(err).To(MatchError(ContainSubstring("no samples")))
	})
})
