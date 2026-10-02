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

// editEnv rewrites a sweep directory's env.json through change.
func editEnv(dir string, change func(env map[string]any)) string {
	GinkgoHelper()
	path := filepath.Join(dir, "env.json")
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	var env map[string]any
	Expect(json.Unmarshal(data, &env)).To(Succeed())
	change(env)
	data, err = json.Marshal(env)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
	return dir
}

// legacySweep is the testdata as a sweep recorded before the floor
// bracketed its cells and came from the linked librados's release: one
// floor run per cell, after the sweep, from the cluster's own image.
func legacySweep() string {
	GinkgoHelper()
	dir := sweep(func(file string, line map[string]any) bool {
		if file != "floor.jsonl" {
			return true
		}
		if line["bracket"] == "after" || line["image"] != "quay.io/ceph/ceph:v19.2.6" {
			return false
		}
		delete(line, "bracket")
		delete(line, "image")
		return true
	})
	return editEnv(dir, func(env map[string]any) {
		for _, k := range []string{"cluster_image", "floor_image", "floor_image_version", "librados_version", "librados_path"} {
			delete(env, k)
		}
		env["image"] = "quay.io/ceph/ceph:v20.2.4"
	})
}

// remove deletes the named files from a sweep directory.
func remove(dir string, names ...string) string {
	GinkgoHelper()
	for _, n := range names {
		Expect(os.Remove(filepath.Join(dir, n))).To(Succeed())
	}
	return dir
}

// artifacts are the files every sweep directory holds before its report.
var artifacts = []string{
	"env.json", "floor.jsonl",
	"seam-sync.jsonl", "seam-callback.jsonl", "seam-pipe.jsonl",
	"cpu-sync-read4k-256.pprof", "cpu-callback-read4k-256.pprof", "cpu-pipe-read4k-256.pprof",
	"overbudget-callback.jsonl", "overbudget-pipe.jsonl",
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
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 64 \| callback \| \d+ \| 54000 \| .* \| 55860 / 58140 \| 1131 / 1109 \| 0\.95x \| 1\.05x \| ops 4\.0%, mean 2\.0% \| 114000, 560 \|$`),
			"read4k at 64: 54000 ops/s against the mean of the floor before and after, 57000, and a mean of 1180 µs against 1120")
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 4 \| callback \| \d+ \| 520 \| .* \| 549 / 571 \| 7171 / 7029 \| 0\.93x \| 1\.08x \| ops 4\.0%, mean 2\.0% \| 1120, 3550 \|$`),
			"read4m at 4, a concurrency only the 4 MiB shapes run")
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 16 \| callback \| \d+ \| 9000 \| .* \| 9500 \| 1700 \| 0\.95x \| 1\.04x \| single run \| — \|$`),
			"write4k at 16, which no criterion compares, has one floor run")
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 16 \| callback \| \d+ \| 30000 \| .* \| — \| — \| — \| — \| — \| — \|$`),
			"read4k at 16 has no floor")
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Pass, "worst 0.92x (read4k at 256), margin +0.12")))
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Latency, report.Pass, "worst 1.09x (read4k at 256), margin +0.16")))
	})

	It("shows read4m against its floor but leaves it out of throughput and latency, and says why", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			if file == "seam-callback.jsonl" && line["shape"] == "read4m" {
				line["ops_per_sec"] = 1.0
				line["mean_us"] = 1e9
			}
			return true
		})
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Pass, "worst 0.92x (read4k at 256), margin +0.12")),
			"a read4m cell at 1 op/s fails nothing")
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Latency, report.Pass, "worst 1.09x (read4k at 256), margin +0.16")))
		for _, v := range r.Verdicts {
			Expect(v.Margin).NotTo(ContainSubstring("read4m"), "%s %s", v.Mode, v.Criterion)
		}
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 4 \| callback \| \d+ \| 1\.00 \| .* \| 549 / 571 \| 7171 / 7029 \| 0\.00x \| \d+\.\d+x \| ops 4\.0%, mean 2\.0% \| 1120, 3550 \|$`),
			"read4m's cells keep their floor and ratios")
		Expect(r.Markdown).To(ContainSubstring("\n## read4m\n\nNot judged: rados bench's 4 MiB rand floor ran at about a quarter of the Go cells' rate"))
		Expect(r.Markdown).To(ContainSubstring("Throughput and mean latency compare read4k and write4k at 64 and 256 in flight, and write4m at 1, 4 and 16,"))
		Expect(r.Markdown).To(ContainSubstring("read4m is measured and shown with its floor, but not judged; its table says why."))
	})

	It("flags a judged cell whose two floor runs differ by more than 10% as unstable", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 256 \| callback \| \d+ \| 55000 \| .* \| 55800 / 64200 \| 4303 / 4217 \| 0\.92x \| 1\.09x \| unstable: ops 14\.0%, mean 2\.0% \| 120000, 2130 \|$`),
			"the floor's ops before and after read4k at 256 differ by 14% of their mean")
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("callback", report.Throughput, report.Pass, "; unstable floor: read4k at 256 (14.0%)"),
			HaveField("Unstable", ConsistOf("read4k at 256 (14.0%)")),
		)), "the throughput verdict the cell feeds carries the flag")
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			HaveField("Mode", "callback"), HaveField("Criterion", report.Latency), HaveField("Unstable", BeEmpty()),
		)), "the floor's mean latency moved 2%, under the threshold")
	})

	It("is incomplete for a judged cell with one floor run", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			return file != "floor.jsonl" || !is(line, "", "write4m", 4) || line["bracket"] != "after"
		})
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Incomplete, "missing: write4m at 4 (one floor run)")))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 4 \| callback \| \d+ \| 110 \| .* \| 116 / — \| 34239 / — \| — \| — \| one floor run \| 236, 16950 \|$`))
	})

	It("renders the run's environment", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(HavePrefix("# Seam microbenchmark: tentacle\n"))
		Expect(r.Markdown).To(ContainSubstring("| ceph_version | 20.2.4 |"))
		Expect(r.Markdown).To(ContainSubstring("| nproc | 32 |"))
	})

	It("names the floor's image and the cluster's", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("The judged floor is rados bench from quay.io/ceph/ceph:v19.2.6, Ceph 19.2.6, " +
			"the release of the librados the benchmark links."))
		Expect(r.Markdown).To(ContainSubstring("The cluster runs quay.io/ceph/ceph:v20.2.4; its own rados bench ran once " +
			"more at each judged cell, shown beside the judged floor and not judged."))
	})

	It("judges by the floor image's runs and shows the cluster image's beside them", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring(" floor runs | cluster image floor ops/s, mean µs |"))
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Throughput, report.Pass, "worst 0.92x (read4k at 256)")),
			"against the cluster image's runs, twice as fast, every ratio would fail")
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| read4k \| rand \| 4096 \| 64 \| single \| quay\.io/ceph/ceph:v20\.2\.4 \| `),
			"the floor table names each run's image")
	})

	It("leaves the cluster image's column out when the cluster runs the floor's image", func(ctx SpecContext) {
		dir := editEnv(sweep(func(file string, line map[string]any) bool {
			return file != "floor.jsonl" || line["image"] == "quay.io/ceph/ceph:v19.2.6"
		}), func(env map[string]any) { env["cluster_image"] = "quay.io/ceph/ceph:v19.2.6" })
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).NotTo(ContainSubstring("cluster image floor"))
		Expect(r.Markdown).To(ContainSubstring("The cluster runs the same image."))
	})

	DescribeTable("refuses a sweep whose floor image does not match the librados it links",
		func(ctx SpecContext, change func(env map[string]any), want string) {
			_, err := report.Seam(ctx, editEnv(sweep(), change))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("no floor image", func(env map[string]any) { delete(env, "floor_image") }, "env.json names no floor_image"),
		Entry("no linked librados", func(env map[string]any) { delete(env, "librados_version") }, "env.json names no librados_version"),
		Entry("a floor image of another release", func(env map[string]any) { env["floor_image_version"] = "20.2.4" },
			"the floor image quay.io/ceph/ceph:v19.2.6 runs Ceph 20.2.4, but the benchmark links librados 19.2.6"),
	)

	It("renders a sweep recorded before the bracketed floor, without deciding its ratios", func(ctx SpecContext) {
		r, err := report.Seam(ctx, legacySweep())
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("This sweep predates the bracketed floor and the floor image: each floor ran " +
			"once, after the sweep, from the cluster's own image, quay.io/ceph/ceph:v20.2.4, while the benchmark linked " +
			"the host's librados, 19.2.6-1.fc43."))
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("callback", report.Throughput, report.Inconclusive, "unbracketed floor, one run after the sweep; worst "),
			HaveField("Margin", Not(ContainSubstring("read4m"))),
		)), "read4m at 4, at 0.95x the worst cell, is not judged")
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Latency, report.Inconclusive, "unbracketed floor, one run after the sweep; worst ")))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| 64 \| callback \| \d+ \| 54000 \| .* \| 55860 \| 1131 \| 0\.97x \| 1\.04x \| single run after the sweep \|$`))
		Expect(r.Markdown).To(ContainSubstring("with rados bench at the same size and concurrency, which in this sweep ran "+
			"once per cell, after the sweep. read4m is measured"), "the verdict's prose describes the floor this sweep has")
		Expect(r.Markdown).NotTo(ContainSubstring("run just before and just after the cell's three modes"))
	})

	It("refuses a floor line naming a bracket or image in a sweep that predates them", func(ctx SpecContext) {
		dir := editEnv(sweep(), func(env map[string]any) {
			for _, k := range []string{"cluster_image", "floor_image", "floor_image_version", "librados_version"} {
				delete(env, k)
			}
			env["image"] = "quay.io/ceph/ceph:v20.2.4"
		})
		_, err := report.Seam(ctx, dir)
		Expect(err).To(MatchError(ContainSubstring("a floor line with a bracket or an image in a sweep whose env.json names neither")))
	})

	It("refuses a floor run from an image that is neither the floor's nor the cluster's", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			if file == "floor.jsonl" && is(line, "", "read4k", 64) && line["bracket"] == "after" {
				line["image"] = "quay.io/ceph/ceph:v18.2.7"
			}
			return true
		})
		_, err := report.Seam(ctx, dir)
		Expect(err).To(MatchError(ContainSubstring("a floor run from quay.io/ceph/ceph:v18.2.7, neither the floor image nor the cluster's")))
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
		Expect(r.Passing).To(Equal([]string{"callback"}), "sync's failure does not count against the release, and pipe fails the cgo criterion")
		Expect(r.Markdown).To(ContainSubstring("callback passes all four criteria"))
	})

	It("judges each cell's thread growth within its own process and shows each process's high-water mark", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Threads, report.Pass, "worst +2 of 16 allowed (read4k at 16), margin +14")),
			"every cell ran in a process of its own, so its idle count is its process's first")
		Expect(r.Markdown).To(ContainSubstring("| mode | benchmark processes | largest growth in one process | highest count |"))
		Expect(r.Markdown).To(ContainSubstring("| sync | 12 | 22→92 (+70) | 92 |"))
		Expect(r.Markdown).To(ContainSubstring("| callback | 12 | 23→25 (+2) | 25 |"))
	})

	It("judges thread growth against both baselines where a mode's cells shared a process", func(ctx SpecContext) {
		// Without process ids, as before seam.sh gave each cell a process:
		// callback's twelve measured cells ran in one process, each starting
		// where the last one peaked.
		i := 0
		dir := sweep(func(file string, line map[string]any) bool {
			delete(line, "process")
			if n, ok := line["n"].(float64); file == "seam-callback.jsonl" && ok && n > 100 {
				line["threads_idle"] = float64(23 + 2*i)
				line["threads_peak"] = float64(25 + 2*i)
				i++
			}
			return true
		})
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Threads, report.Inconclusive,
			"against each cell's own idle count, worst +2 of 16 allowed (read4k at 16), margin +14; "+
				"against its process's first, worst +24 of 16 allowed (write4m at 16), margin -8")),
			"the cell's own idle count bounds its growth from below, its process's first from above")
		Expect(r.Verdicts).To(ContainElement(verdict("pipe", report.Threads, report.Pass, "worst +3 of 16 allowed")),
			"pipe's cells each start below the last one's peak, so each ran in a process of its own")
		Expect(r.Passing).To(BeEmpty(), "an inconclusive criterion does not pass")
		Expect(r.Markdown).To(ContainSubstring("| callback | 2 | 23→47 (+24) | 47 |"))
		Expect(r.Markdown).To(ContainSubstring("These cells carry no process id"))
	})

	It("refuses cells that mix process ids with none", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			if file == "seam-pipe.jsonl" {
				delete(line, "process")
			}
			return true
		})
		_, err := report.Seam(ctx, dir)
		Expect(err).To(MatchError(ContainSubstring("some cells carry a process id and some do not")))
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
			if file == "seam-callback.jsonl" && is(line, "callback", "write4k", 256) {
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
			if file == "seam-callback.jsonl" && is(line, "callback", "read4k", 64) {
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

	It("judges the cgo share of each mode's read4k profile at 256 in flight", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Cgo, report.Pass, "2.7% of 1.5s sampled, margin +7.3 points")))
		Expect(r.Verdicts).To(ContainElement(verdict("pipe", report.Cgo, report.Fail, "84.0% of 1.5s sampled, margin -74.0 points")))
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("sync", report.Cgo, report.Pass, "2.7% of 1.5s sampled"),
			HaveField("Judged", false),
		)), "sync's profile is reported beside the judged modes'")
		Expect(r.Markdown).To(ContainSubstring("\n## CPU profiles\n"))
		Expect(r.Markdown).To(ContainSubstring("| mode | profile | sampled | cgo frames | C under runtime.cgocall | " +
			"(i) submit crossing | (ii) delivery | boundary cost | Go completion handler | threads outside Go | largest cgo frames, cumulative |"))
		for _, row := range []string{
			`(?m)^\| sync \| cpu-sync-read4k-256\.pprof \| 1\.5s \| 2\.7% \| 2\.0% \| 0\.7% \| 0\.0% \| 0\.7% \| 0\.0% \| 0\.0% \| `,
			`(?m)^\| callback \| cpu-callback-read4k-256\.pprof \| 1\.5s \| 2\.7% \| 2\.0% \| 0\.7% \| 0\.0% \| 0\.7% \| 0\.0% \| 0\.0% \| `,
			`(?m)^\| pipe \| cpu-pipe-read4k-256\.pprof \| 1\.5s \| 84\.0% \| 18\.0% \| 66\.0% \| 0\.0% \| 66\.0% \| 0\.0% \| 0\.0% \| `,
		} {
			Expect(r.Markdown).To(MatchRegexp(row), "the cgo share, then how it divides, and the threads the Go runtime does not run")
		}
	})

	It("reports the boundary cost beside the cgo criterion without judging by it", func(ctx SpecContext) {
		r, err := report.Seam(ctx, "testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("callback", report.Boundary, report.Pass, "0.7% of 1.5s sampled, margin +9.3 points"),
			HaveField("Informational", true),
		)))
		Expect(r.Verdicts).To(ContainElement(SatisfyAll(
			verdict("pipe", report.Boundary, report.Fail,
				"66.0% of 1.5s sampled, margin -56.0 points; leaves out pipe's write(2) per completion, unmeasured"),
			HaveField("Informational", true),
		)), "pipe's C side of a completion is sampled outside every Go frame")
		for _, mode := range []string{"sync", "callback"} {
			Expect(r.Verdicts).To(ContainElement(SatisfyAll(
				HaveField("Mode", mode), HaveField("Criterion", report.Boundary),
				HaveField("Margin", Not(ContainSubstring("write(2)"))),
			)), mode)
		}
		Expect(r.Markdown).To(ContainSubstring("For pipe it leaves out the C side of each completion, one write(2) to the " +
			"pipe on a librados thread, which the profile records under runtime._ExternalCode with no frame to tell it by: " +
			"it is unmeasured, and pipe's boundary cost is low by that much."))
		Expect(r.Markdown).To(MatchRegexp(`(?m)^\| cgo<=10% of CPU \|.*\|\n\| informational: boundary cost<=10% of CPU `+
			`\(librados's C and the Go completion handler excluded\) \| PASS: 0\.7% `),
			"the informational row sits directly under the cgo criterion's")
		Expect(r.Markdown).To(ContainSubstring("**Answer:** callback passes all four criteria on tentacle.\n" +
			"\n**Answer with the boundary cost in place of the cgo criterion:** callback passes all four criteria on tentacle.\n"))
	})

	It("answers again with the boundary cost where the cgo criterion fails on librados's C", func(ctx SpecContext) {
		dir := sweep()
		data, err := os.ReadFile("testdata/pprof/c-heavy.pprof")
		Expect(err).NotTo(HaveOccurred())
		for _, mode := range []string{"callback", "pipe"} {
			Expect(os.WriteFile(filepath.Join(dir, "cpu-"+mode+"-read4k-256.pprof"), data, 0o600)).To(Succeed())
		}
		r, err := report.Seam(ctx, dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Cgo, report.Fail, "98.7% of 1.49s sampled")),
			"nearly every sample is C under runtime.cgocall")
		Expect(r.Verdicts).To(ContainElement(verdict("callback", report.Boundary, report.Pass, "0.0% of 1.49s sampled")))
		Expect(r.Passing).To(BeEmpty())
		Expect(r.BoundaryPassing).To(Equal([]string{"callback", "pipe"}))
		Expect(r.Answer()).To(Equal("no judged mode passes all four criteria"))
		Expect(r.BoundaryAnswer()).To(Equal("callback and pipe pass all four criteria"))
	})

	It("fails when a mode's file is missing a cell another mode has", func(ctx SpecContext) {
		dir := sweep(func(file string, line map[string]any) bool {
			return file != "seam-pipe.jsonl" || !is(line, "pipe", "read4k", 256)
		})
		_, err := report.Seam(ctx, dir)
		Expect(err).To(MatchError(ContainSubstring("conc=256 missing for mode pipe")))
	})

	It("fails when a mode's file holds no cells", func(ctx SpecContext) {
		dir := sweep(func(file string, _ map[string]any) bool { return file != "seam-pipe.jsonl" })
		_, err := report.Seam(ctx, dir)
		Expect(err).To(MatchError(ContainSubstring("conc=16 missing for mode pipe")))
	})

	DescribeTable("refuses a sweep that lacks an artifact, naming it",
		func(ctx SpecContext, name string) {
			_, err := report.Seam(ctx, remove(sweep(), name))
			Expect(err).To(MatchError(ContainSubstring("missing " + name)))
		},
		Entry("the environment", "env.json"),
		Entry("the floor", "floor.jsonl"),
		Entry("a mode's cells", "seam-pipe.jsonl"),
		Entry("the baseline's profile", "cpu-sync-read4k-256.pprof"),
		Entry("a judged mode's profile", "cpu-callback-read4k-256.pprof"),
		Entry("callback's cells over the byte budget", "overbudget-callback.jsonl"),
		Entry("pipe's cells over the byte budget", "overbudget-pipe.jsonl"),
	)

	DescribeTable("refuses an over-budget file without both of its cells",
		func(ctx SpecContext, derived bool, want string) {
			dir := sweep(func(file string, line map[string]any) bool {
				return file != "overbudget-pipe.jsonl" || (line["max_inflight_ops"] == 0.0) != derived
			})
			_, err := report.Seam(ctx, dir)
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("the cell with the derived limits", true, "overbudget-pipe.jsonl has no cell with the in-flight limits derived"),
		Entry("the cell with the limits lifted", false, "overbudget-pipe.jsonl has no cell with the in-flight limits lifted"),
	)

	DescribeTable("refuses a line that does not belong to its file",
		func(ctx SpecContext, e edit, want string) {
			_, err := report.Seam(ctx, sweep(e))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a cell of another mode",
			edit(func(file string, line map[string]any) bool {
				if file == "seam-sync.jsonl" && is(line, "sync", "read4k", 64) {
					line["mode"] = "callback"
				}
				return true
			}),
			"seam-sync.jsonl line 3: a callback cell"),
		Entry("an over-budget cell of another mode",
			edit(func(file string, line map[string]any) bool {
				if file == "overbudget-pipe.jsonl" {
					line["mode"] = "callback"
				}
				return true
			}),
			"overbudget-pipe.jsonl line 1: a callback cell"),
		Entry("a floor line whose op is not its shape's",
			edit(func(file string, line map[string]any) bool {
				if file == "floor.jsonl" && is(line, "", "read4k", 64) {
					line["op"] = "write"
				}
				return true
			}),
			"read4k's floor is a rand, not a write"),
		Entry("a floor line whose size is not its shape's",
			edit(func(file string, line map[string]any) bool {
				if file == "floor.jsonl" && is(line, "", "write4m", 4) {
					line["size"] = 4096.0
				}
				return true
			}),
			"write4m's floor writes 4194304 bytes, not 4096"),
	)

	It("names every artifact an empty directory lacks", func(ctx SpecContext) {
		_, err := report.Seam(ctx, GinkgoT().TempDir())
		Expect(err).To(MatchError(ContainSubstring("missing " + strings.Join(artifacts, ", "))))
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
		Entry("two lines for one run of a cell",
			edit(func(file string, line map[string]any) bool {
				if file == "floor.jsonl" && is(line, "", "read4k", 256) {
					line["conc"] = 64.0
				}
				return true
			}),
			"two floor lines for read4k at conc=64 (before, quay.io/ceph/ceph:v19.2.6)"),
		Entry("a run neither before nor after its cell",
			edit(func(file string, line map[string]any) bool {
				if file == "floor.jsonl" && is(line, "", "read4k", 64) && line["bracket"] == "after" {
					line["bracket"] = "during"
				}
				return true
			}),
			`bracket "during" is not before, after or empty`),
	)
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
		s, err := report.CgoShare(ctx, "testdata/cpu-pipe-read4k-256.pprof")
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

	It("divides each sample, once, by the frame nearest its leaf, for callback and pipe stacks alike", func() {
		data, err := os.ReadFile("testdata/traces/boundary.txt")
		Expect(err).NotTo(HaveOccurred())
		s, err := report.ParseTraces(bytes.NewReader(data))
		Expect(err).NotTo(HaveOccurred())
		ms := time.Millisecond
		Expect(s.Total).To(Equal(420 * ms))
		Expect(s.Cgo).To(Equal(250*ms),
			"every stack with a cgo frame, the pointer check included, and none of the pipe's drain without one")
		Expect(s.InC).To(Equal(130*ms), "the submission's C and the C the handler calls, in either mode")
		Expect(s.Submit).To(Equal(50*ms), "the crossing out of runtime.cgocall, the pointer check, the pinner, and the handler's own crossings")
		Expect(s.Delivery).To(Equal(80*ms), "runtime.cgocallback* above the handler, and the pipe's read in drain")
		Expect(s.Handler).To(Equal(70*ms), "aioComplete's Go work, under the callback and under drain")
		Expect(s.Boundary()).To(Equal(130 * ms))
		Expect(s.External).To(Equal(40 * ms))
		Expect(s.Total).To(Equal(s.External+50*ms+s.InC+s.Submit+s.Delivery+s.Handler), "each sample is counted once")
	})
})
