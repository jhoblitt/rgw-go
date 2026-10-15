package report_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/report/internal/report"
)

// phaseEdit changes one line of an elbencho results file in place.
type phaseEdit func(phase map[string]any)

// gatewayRun builds a run directory of gateway from testdata/gateway: an
// elbencho small-get.json captured from a two-second run at 1 and 8 threads
// against the Tentacle radosgw, httpload's noop-8.json, the radosgw's samples
// over the small-get phases, and five lines of rgw-go's gctrace, which only an
// rgw-go run keeps. edits rewrite each line of small-get.json.
func gatewayRun(gateway string, edits ...phaseEdit) string {
	GinkgoHelper()
	dir := filepath.Join(GinkgoT().TempDir(), "gateway-"+gateway+"-20261012T141331Z")
	Expect(os.Mkdir(dir, 0o700)).To(Succeed())
	entries, err := os.ReadDir(filepath.Join("testdata", "gateway"))
	Expect(err).NotTo(HaveOccurred())
	for _, e := range entries {
		if e.Name() == "gctrace-small-get.log" && gateway != "rgw-go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", "gateway", e.Name()))
		Expect(err).NotTo(HaveOccurred())
		if e.Name() == "small-get.json" {
			data = editPhases(data, edits)
		}
		Expect(os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600)).To(Succeed())
	}
	frontends := "beast port=80 tcp_nodelay=1"
	if gateway == "rgw-go" {
		frontends = "beast endpoint=127.0.0.1:7482"
	}
	writeJSON(filepath.Join(dir, "env.json"), map[string]any{
		"release": "tentacle", "gateway": gateway, "seconds": 2, "frontends": frontends, "tcp_nodelay": "on",
	})
	writeJSON(filepath.Join(dir, "config.json"), map[string]any{
		"client.rgw.ceph.objectstore.a": map[string]any{"source": "config show", "options": map[string]any{"rgw_enable_gc_threads": "false"}},
		"client.rgw.rgw-go":             map[string]any{"source": "config get", "options": map[string]any{"rgw_enable_gc_threads": "false"}},
	})
	return dir
}

func editPhases(data []byte, edits []phaseEdit) []byte {
	GinkgoHelper()
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		// elbencho ends each phase's line with two newlines.
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var phase map[string]any
		Expect(json.Unmarshal(sc.Bytes(), &phase)).To(Succeed())
		for _, e := range edits {
			e(phase)
		}
		enc, err := json.Marshal(phase)
		Expect(err).NotTo(HaveOccurred())
		out.Write(append(enc, '\n'))
	}
	Expect(sc.Err()).NotTo(HaveOccurred())
	return out.Bytes()
}

// appendLog appends lines to the log at path, creating it if need be.
func appendLog(path, lines string) {
	GinkgoHelper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	Expect(err).NotTo(HaveOccurred())
	_, err = f.WriteString(lines)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.Close()).To(Succeed())
}

func writeJSON(path string, v any) {
	GinkgoHelper()
	data, err := json.Marshal(v)
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
}

// atThreads applies edit to the phase elbencho ran at threads, as its JSON
// writes every value, as a string.
func atThreads(threads string, edit phaseEdit) phaseEdit {
	return func(phase map[string]any) {
		if object(phase, "config")["threads"] == threads {
			edit(phase)
		}
	}
}

// object is the JSON object at key.
func object(parent map[string]any, key string) map[string]any {
	GinkgoHelper()
	child, ok := parent[key].(map[string]any)
	Expect(ok).To(BeTrue(), "%q is not an object", key)
	return child
}

func lastDone(phase map[string]any) map[string]any {
	return object(phase, "last_done")
}

// tableRow returns the markdown row of the table under heading whose first
// cells are cells.
func tableRow(md, heading string, cells ...string) string {
	GinkgoHelper()
	_, section, found := strings.Cut(md, heading+"\n")
	Expect(found).To(BeTrue(), "no heading %q in\n%s", heading, md)
	if next := strings.Index(section, "\n#"); next >= 0 {
		section = section[:next]
	}
	prefix := "| " + strings.Join(cells, " | ") + " |"
	for line := range strings.SplitSeq(section, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	Fail("no row " + prefix + " under " + heading + " in\n" + section)
	return ""
}

var _ = Describe("Gateway", func() {
	It("renders a row per variant and concurrency, with the latency percentiles elbencho prints", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(HavePrefix("# Gateway comparison: tentacle\n"))
		// elbencho printed IO lat % us: 50%<=664 99%<=888 99.9%<=1392 at 1
		// thread and 50%<=904 99%<=1264 99.9%<=1776 at 8.
		Expect(tableRow(r.Markdown, "### small-get and noop: throughput and latency", "GET 4 KiB", "1")).
			To(Equal("| GET 4 KiB | 1 | 1458 | 1458 | 5.70 | 0.66 | 0.89 | 1.39 |"))
		Expect(tableRow(r.Markdown, "### small-get and noop: throughput and latency", "GET 4 KiB", "8")).
			To(Equal("| GET 4 KiB | 8 | 8538 | 8538 | 33.4 | 0.90 | 1.26 | 1.78 |"))
		Expect(tableRow(r.Markdown, "### small-get and noop: throughput and latency", "anonymous GET /", "8")).
			To(Equal("| anonymous GET / | 8 | 196 | — | — | 41.0 | 42.1 | 42.1 |"))
	})

	It("puts the noop rows beside small-get at the same concurrency", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		rows := regexp.MustCompile(`(?m)^\| (GET 4 KiB|anonymous GET /) \| (\d+) \|`).FindAllStringSubmatch(r.Markdown, 3)
		Expect(rows).To(HaveLen(3))
		Expect([]string{rows[0][1] + "@" + rows[0][2], rows[1][1] + "@" + rows[1][2], rows[2][1] + "@" + rows[2][2]}).
			To(Equal([]string{"GET 4 KiB@1", "GET 4 KiB@8", "anonymous GET /@8"}))
	})

	It("reads each phase's CPU per request, peak threads and peak RSS from the samples around it", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		// The 8-thread phase ran from 14:14:07.279 for 2.001 s: utime+stime
		// interpolated to 35.037 s at its start and 41.45 s at its end, 6.41 s
		// over 17084 requests; the samples around it peak at 605 threads and
		// 193396 KiB.
		Expect(tableRow(r.Markdown, "### small-get and noop: cost", "GET 4 KiB", "8")).
			To(Equal("| GET 4 KiB | 8 | 0.38 | 605 | 189 | 0 | 0 |"))
		Expect(tableRow(r.Markdown, "### small-get and noop: cost", "GET 4 KiB", "1")).
			To(Equal("| GET 4 KiB | 1 | 2.15 | 605 | 187 | 0 | 0 |"))
		// No samples-noop.csv, and httpload retries nothing.
		Expect(tableRow(r.Markdown, "### small-get and noop: cost", "anonymous GET /", "8")).
			To(Equal("| anonymous GET / | 8 | — | — | — | 0 | — |"))
	})

	It("renders two runs side by side with the ratio column rgw-go/radosgw", func(ctx SpecContext) {
		faster := atThreads("8", func(p map[string]any) { lastDone(p)["ios"] = "34168" })
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw"), gatewayRun("rgw-go", faster)})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | "))
		Expect(tableRow(r.Markdown, "### small-get and noop: throughput and latency", "GET 4 KiB", "8")).To(HavePrefix(
			"| GET 4 KiB | 8 | 8538 | 17075 | 2.00 | 8538 | 8538 | 1.00 | 33.4 | 33.4 | 1.00 | 0.90 | 0.90 | 1.00 |"))
		Expect(tableRow(r.Markdown, "### small-get and noop: cost", "GET 4 KiB", "8")).To(Equal(
			"| GET 4 KiB | 8 | 0.38 | 0.19 | 0.50 | 605 | 605 | 1.00 | 189 | 189 | 1.00 | 0 | 0 | 0 | 0 |"))
	})

	It("names the thread column for the high-water mark it is", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw"), gatewayRun("rgw-go")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | " +
			"thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw |"))
		Expect(r.Markdown).NotTo(ContainSubstring("peak threads"))
		Expect(r.Markdown).To(ContainSubstring("Go parks an idle OS thread rather than ending it"))
	})

	It("labels a multipart PUT's throughput as parts per second", func(ctx SpecContext) {
		multipart := atThreads("8", func(p map[string]any) {
			p["phase_type"] = "WRITE"
			object(p, "config")["file_size"] = "67108864"
			object(p, "config")["block_size"] = "8388608"
		})
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw", multipart)})
		Expect(err).NotTo(HaveOccurred())
		Expect(tableRow(r.Markdown, "### small-get and noop: throughput and latency", "PUT 64 MiB in 8 MiB parts (parts/s)", "8")).
			To(HavePrefix("| PUT 64 MiB in 8 MiB parts (parts/s) | 8 | 8538 |"))
		Expect(r.Markdown).To(ContainSubstring("its req/s counts UploadPart requests"))
	})

	It("reports rgw-go's GC from its gctrace log", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw"), gatewayRun("rgw-go")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("GC cycles over the whole profile, its preparation and removals included"))
		Expect(r.Markdown).To(ContainSubstring("- rgw-go, small-get: 5 cycles, longest stop-the-world pause 79.0 µs, 417 µs in all\n"))
		Expect(r.Markdown).NotTo(ContainSubstring("torn"), "no line of the log is torn")
	})

	It("counts the pauses of a goroutine leak check's cycle, whose line says so before its colon", func(ctx SpecContext) {
		dir := gatewayRun("rgw-go")
		// As go1.27.1 prints it under GODEBUG=gctrace=1 when
		// pprof.Lookup("goroutineleak") is written.
		appendLog(filepath.Join(dir, "gctrace-noop.log"), "gc 1 @0.000s 7% (checking for goroutine leaks): 0.26+0.097+0.007 ms clock, "+
			"8.3+0/0.41/0+0.24 ms cpu, 0->0->0 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 32 P (forced)\n")
		r, err := report.Gateway(ctx, []string{dir})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("- rgw-go, noop: 1 cycles, longest stop-the-world pause 260 µs, 267 µs in all\n"))
		Expect(r.Markdown).NotTo(ContainSubstring("torn"), "the leak check's line is whole")
	})

	Context("with torn gctrace lines", func() {
		// tornAt is a line a Squid run's rgw-go wrote, torn by a record of its
		// JSON log after its CPU share; tornEarly is torn after its sweep
		// termination's pause, which a parse of a torn line would take for the
		// longest.
		const (
			tornAt = `gc 310774 @1479.916s 4{"time":"2026-10-15T03:57:30.583966873-07:00","level":"WARN",` +
				`"msg":"bucket listing failed after the response started","error":"write tcp4 127.0.0.1:7481->127.0.0.1:35898: write: broken pipe"}`
			tornEarly = `gc 10 @41.040s 0%: 0.900+0.1{"level":"WARN","msg":"bucket listing failed after the response started"}`
			tornLate  = `gc 11 @41.045s 0%{"level":"WARN","msg":"bucket listing failed after the response started"}`
			wholeNoop = "gc 310775 @1479.920s 4%: 0.100+0.30+0.200 ms clock, 1.3+0.43/1.7/0.10+0.92 ms cpu, 3->4->1 MB, 4 MB goal, " +
				"0 MB stacks, 0 MB globals, 32 P"
		)

		It("counts each as a cycle with no pauses, and says how many lines of each profile were torn", func(ctx SpecContext) {
			dir := gatewayRun("rgw-go")
			appendLog(filepath.Join(dir, "gctrace-small-get.log"), tornEarly+"\n"+tornLate+"\n")
			appendLog(filepath.Join(dir, "gctrace-noop.log"), tornAt+"\n"+wholeNoop+"\n")
			r, err := report.Gateway(ctx, []string{dir})
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Markdown).To(ContainSubstring("- rgw-go, small-get: 7 cycles, longest stop-the-world pause 79.0 µs, 417 µs in all; " +
				"2 torn lines, their cycles' pauses unknown\n"))
			Expect(r.Markdown).To(ContainSubstring("- rgw-go, noop: 2 cycles, longest stop-the-world pause 200 µs, 300 µs in all; " +
				"1 torn line, its cycle's pauses unknown\n"))
			Expect(r.Markdown).To(ContainSubstring("A torn line is a cycle's line cut short, by a record of rgw-go's JSON log, which " +
				"shares its stderr, or by the end of the log the run read; one torn too early to be kept is known from the gap it " +
				"leaves in the cycles' numbers. A torn line's cycle counts, and its pauses are in neither the longest nor the total:\n"))
			Expect(r.Findings).To(BeEmpty(), "a torn line is no phase's error")
		})

		It("counts a line cut short at the log's end, without its newline, as torn", func(ctx SpecContext) {
			dir := gatewayRun("rgw-go")
			appendLog(filepath.Join(dir, "gctrace-small-get.log"), "gc 10 @41.040s 0%: 0.900+0.1")
			r, err := report.Gateway(ctx, []string{dir})
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Markdown).To(ContainSubstring("- rgw-go, small-get: 6 cycles, longest stop-the-world pause 79.0 µs, 417 µs in all; " +
				"1 torn line, its cycle's pauses unknown\n"))
		})

		It("counts a cycle missing from the numbers, its line torn too early to be kept, as a torn line, "+
			"and none before the first line", func(ctx SpecContext) {
			dir := gatewayRun("rgw-go")
			// Cycle 10's line is missing between the testdata's 9 and this 11.
			appendLog(filepath.Join(dir, "gctrace-small-get.log"), "gc 11 @41.050s 0%: 0.010+0.30+0.020 ms clock, "+
				"1.3+0.43/1.7/0.10+0.92 ms cpu, 3->4->1 MB, 4 MB goal, 0 MB stacks, 0 MB globals, 32 P\n")
			appendLog(filepath.Join(dir, "gctrace-noop.log"), wholeNoop+"\n")
			r, err := report.Gateway(ctx, []string{dir})
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Markdown).To(ContainSubstring("- rgw-go, small-get: 7 cycles, longest stop-the-world pause 79.0 µs, 447 µs in all; " +
				"1 torn line, its cycle's pauses unknown\n"))
			Expect(r.Markdown).To(ContainSubstring("- rgw-go, noop: 1 cycles, longest stop-the-world pause 200 µs, 300 µs in all\n"))
		})

		It("still refuses a line that does not start as a GC line, as a torn line's rest", func(ctx SpecContext) {
			dir := gatewayRun("rgw-go")
			log := filepath.Join(dir, "gctrace-small-get.log")
			appendLog(log, tornEarly+"\n"+`%: 0.086+0.61+0.039 ms clock, 2.7+1.6/2.7/0+1.2 ms cpu, 10->10->4 MB`+"\n")
			_, err := report.Gateway(ctx, []string{dir})
			Expect(err).To(MatchError(log + " line 7 is not a gctrace line"))
		})

		It("refuses a cycle that does not follow the line before's, which no tear makes", func(ctx SpecContext) {
			dir := gatewayRun("rgw-go")
			log := filepath.Join(dir, "gctrace-small-get.log")
			appendLog(log, "gc 9 @41.040s 0%: 0.010+0.30+0.020 ms clock, 1.3+0.43/1.7/0.10+0.92 ms cpu, 3->4->1 MB, "+
				"4 MB goal, 0 MB stacks, 0 MB globals, 32 P\n")
			_, err := report.Gateway(ctx, []string{dir})
			Expect(err).To(MatchError(log + " line 6 is cycle 9, which does not follow cycle 9"))
		})
	})

	It("names each run's frontends and TCP_NODELAY in the header", func(ctx SpecContext) {
		radosgw, rgwGo := gatewayRun("radosgw"), gatewayRun("rgw-go")
		r, err := report.Gateway(ctx, []string{radosgw, rgwGo})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("- radosgw: " + radosgw + "; frontends `beast port=80 tcp_nodelay=1`, TCP_NODELAY on\n"))
		Expect(r.Markdown).To(ContainSubstring("- rgw-go: " + rgwGo + "; frontends `beast endpoint=127.0.0.1:7482`, TCP_NODELAY on\n"))
	})

	It("says when a run did not record its frontends", func(ctx SpecContext) {
		dir := gatewayRun("radosgw")
		editEnv(dir, func(env map[string]any) {
			delete(env, "frontends")
			delete(env, "tcp_nodelay")
		})
		r, err := report.Gateway(ctx, []string{dir})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("- radosgw: " + dir + "; frontends and TCP_NODELAY not recorded\n"))
	})

	It("reads a phase without IO latencies, as a listing's, from its entries histogram", func(ctx SpecContext) {
		listing := atThreads("1", func(p map[string]any) {
			delete(object(lastDone(p), "latency"), "IO")
			delete(lastDone(p), "ios")
		})
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw", listing)})
		Expect(err).NotTo(HaveOccurred())
		// Objects lat % us: 50%<=664 99%<=896 99.9%<=1392, over the 2918
		// requests the histogram counts.
		Expect(tableRow(r.Markdown, "### small-get and noop: throughput and latency", "GET 4 KiB", "1")).
			To(Equal("| GET 4 KiB | 1 | 1458 | 1458 | 5.70 | 0.66 | 0.90 | 1.39 |"))
	})

	It("names each phase with an error or a retry as a finding", func(ctx SpecContext) {
		failing := atThreads("8", func(p map[string]any) {
			lastDone(p)["errors"] = map[string]any{"total": "3", "by_kind": map[string]any{"http_503": "3"}}
			lastDone(p)["retries"] = map[string]any{"total": "2", "wait_ms": "75", "by_kind": map[string]any{"http_503": "2"}}
		})
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw", failing)})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Findings).To(Equal([]string{"radosgw, small-get, GET 4 KiB at 8 threads: 3 errors (http_503=3), 2 retries (http_503=2)"}))
		Expect(r.Markdown).To(ContainSubstring("\n## Findings\n\n- radosgw, small-get, GET 4 KiB at 8 threads: 3 errors (http_503=3), 2 retries (http_503=2)\n"))
		Expect(tableRow(r.Markdown, "### small-get and noop: cost", "GET 4 KiB", "8")).To(HaveSuffix("| 3 | 2 |"))
	})

	It("says when no phase had an error or a retry", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Findings).To(BeEmpty())
		Expect(r.Markdown).To(ContainSubstring("\n## Findings\n\nNo phase had an error or a retry.\n"))
	})

	It("records every command each run ran", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{gatewayRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring(`"-r" "-t" "8" "-n" "0" "-N" "50" "-s" "4k" "-b" "4k" "--timelimit" "2" "--infloop" "bench-small"`))
		Expect(r.Markdown).To(ContainSubstring("httpload -url http://10.89.0.34:80/ -threads 8 -duration 2s"))
	})

	It("refuses runs of different releases", func(ctx SpecContext) {
		squid := gatewayRun("rgw-go")
		writeJSON(filepath.Join(squid, "env.json"), map[string]any{"release": "squid", "gateway": "rgw-go"})
		_, err := report.Gateway(ctx, []string{gatewayRun("radosgw"), squid})
		Expect(err).To(MatchError(ContainSubstring("runs of tentacle and squid")))
	})

	It("refuses runs whose settings differ", func(ctx SpecContext) {
		rgwGo := gatewayRun("rgw-go")
		editEnv(rgwGo, func(env map[string]any) {
			env["seconds"] = 60
			env["tcp_nodelay"] = "off"
			env["nproc"] = 32
			env["kernel"] = "7.2.8-100.fc43.x86_64"
			env["elbencho_packages"] = "curl-static-8.22.0-r0"
		})
		radosgw := gatewayRun("radosgw")
		_, err := report.Gateway(ctx, []string{radosgw, rgwGo})
		Expect(err).To(MatchError(And(
			ContainSubstring(radosgw+" and "+rgwGo+" ran under different settings"),
			ContainSubstring("seconds: 2 against 60"),
			ContainSubstring("tcp_nodelay: on against off"),
			ContainSubstring("nproc: unset against 32"),
			ContainSubstring("kernel: unset against 7.2.8-100.fc43.x86_64"),
			ContainSubstring("elbencho_packages: unset against curl-static-8.22.0-r0"),
		)))
	})

	It("refuses runs of different profiles", func(ctx SpecContext) {
		rgwGo := gatewayRun("rgw-go")
		Expect(os.Remove(filepath.Join(rgwGo, "noop-8.json"))).To(Succeed())
		_, err := report.Gateway(ctx, []string{gatewayRun("radosgw"), rgwGo})
		Expect(err).To(MatchError(ContainSubstring("profiles: noop, small-get against small-get")))
	})

	It("refuses a directory without env.json", func(ctx SpecContext) {
		dir := gatewayRun("radosgw")
		Expect(os.Remove(filepath.Join(dir, "env.json"))).To(Succeed())
		_, err := report.Gateway(ctx, []string{dir})
		Expect(err).To(MatchError(ContainSubstring("env.json")))
	})

	It("refuses a directory without results", func(ctx SpecContext) {
		dir := gatewayRun("radosgw")
		Expect(os.Remove(filepath.Join(dir, "small-get.json"))).To(Succeed())
		Expect(os.Remove(filepath.Join(dir, "noop-8.json"))).To(Succeed())
		_, err := report.Gateway(ctx, []string{dir})
		Expect(err).To(MatchError(ContainSubstring("no profile results")))
	})
})

var _ = Describe("the gateway command", func() {
	var stdout, stderr *bytes.Buffer

	BeforeEach(func() {
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	run := func(ctx SpecContext, args ...string) error {
		return report.Run(ctx, args, nil, stdout, stderr)
	}

	It("writes one run's REPORT.md into its directory and prints its path", func(ctx SpecContext) {
		dir := gatewayRun("radosgw")
		Expect(run(ctx, "gateway", "--dir", dir)).To(Succeed())
		Expect(filepath.Join(dir, "REPORT.md")).To(BeARegularFile())
		Expect(stdout.String()).To(Equal(filepath.Join(dir, "REPORT.md") + ": no phase had an error or a retry\n"))
	})

	It("prints a comparison of several runs", func(ctx SpecContext) {
		Expect(run(ctx, "gateway", "--dir", gatewayRun("radosgw"), "--dir", gatewayRun("rgw-go"))).To(Succeed())
		Expect(stdout.String()).To(HavePrefix("# Gateway comparison: tentacle\n"))
	})

	It("writes the report where --out says", func(ctx SpecContext) {
		out := filepath.Join(GinkgoT().TempDir(), "COMPARISON.md")
		Expect(run(ctx, "gateway", "--dir", gatewayRun("radosgw"), "--dir", gatewayRun("rgw-go"), "--out", out)).To(Succeed())
		Expect(out).To(BeARegularFile())
		Expect(stdout.String()).To(Equal(out + ": no phase had an error or a retry\n"))
	})

	It("requires --dir", func(ctx SpecContext) {
		Expect(run(ctx, "gateway")).To(MatchError("--dir is required"))
	})
})
