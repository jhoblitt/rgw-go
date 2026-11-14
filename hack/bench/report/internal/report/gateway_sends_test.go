package report_test

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/report/internal/report"
)

// copyTestdata copies a file of testdata to path.
func copyTestdata(name, path string) []byte {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	Expect(err).NotTo(HaveOccurred())
	Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
	return data
}

// sendRun builds a run directory of gateway that holds only the profiles the
// comparison added after small-put's six, none of those six and no noop.
// s3send's results, comp-small-put's, comp-large-put's two and crc-put's, are
// testdata/gateway-sends. elbencho's are the testdata's small-get.json, a
// capture of a two-second run, written again as each profile's own:
// comp-large-get's with 64 MiB objects read in 4 MiB ranges, ver-put's as
// writes, and comp-small-get's and ver-get's as they are. crc-put has the
// radosgw's samples of the small-get phases, whose second phase it shares its
// start with, and an rgw-go run its gctrace.
func sendRun(gateway string) string {
	GinkgoHelper()
	dir := filepath.Join(GinkgoT().TempDir(), "gateway-"+gateway+"-20261012T141331Z")
	Expect(os.Mkdir(dir, 0o700)).To(Succeed())
	sends, err := os.ReadDir(filepath.Join("testdata", "gateway-sends"))
	Expect(err).NotTo(HaveOccurred())
	for _, e := range sends {
		copyTestdata(filepath.Join("gateway-sends", e.Name()), filepath.Join(dir, e.Name()))
	}
	smallGet, err := os.ReadFile(filepath.Join("testdata", "gateway", "small-get.json"))
	Expect(err).NotTo(HaveOccurred())
	largeGet := func(phase map[string]any) {
		object(phase, "config")["file_size"] = "67108864"
		object(phase, "config")["block_size"] = "4194304"
	}
	writes := func(phase map[string]any) { phase["phase_type"] = "WRITE" }
	for name, edits := range map[string][]phaseEdit{"comp-small-get": nil, "comp-large-get": {largeGet}, "ver-put": {writes}, "ver-get": nil} {
		Expect(os.WriteFile(filepath.Join(dir, name+".json"), editPhases(smallGet, edits), 0o600)).To(Succeed())
	}
	copyTestdata(filepath.Join("gateway", "samples-small-get.csv"), filepath.Join(dir, "samples-crc-put.csv"))
	if gateway == "rgw-go" {
		copyTestdata(filepath.Join("gateway", "gctrace-small-get.log"), filepath.Join(dir, "gctrace-crc-put.log"))
	}
	writeJSON(filepath.Join(dir, "env.json"), map[string]any{
		"release": "tentacle", "gateway": gateway, "seconds": 2, "frontends": "beast port=80 tcp_nodelay=1", "tcp_nodelay": "on",
		"comp_class": "COMP_ZLIB", "ver_objects": 1570,
	})
	return dir
}

var _ = Describe("Gateway, for the profiles s3send and elbencho write beside small-put's", func() {
	DescribeTable("renders each profile's phases under a table of its own",
		func(ctx SpecContext, heading string, cells []string, want string) {
			r, err := report.Gateway(ctx, []string{sendRun("radosgw")})
			Expect(err).NotTo(HaveOccurred())
			Expect(tableRow(r.Markdown, heading, cells...)).To(Equal(want))
		},
		Entry("comp-small-put's s3send puts, by their class", "### comp-small-put: throughput and latency",
			[]string{"PUT 4 KiB, class COMP_ZLIB", "8"}, "| PUT 4 KiB, class COMP_ZLIB | 8 | 2000 | 2000 | 7.81 | 3.00 | 6.00 | 9.00 |"),
		Entry("comp-large-put's whole objects", "### comp-large-put: throughput and latency",
			[]string{"PUT 64 MiB, class COMP_ZLIB", "4"}, "| PUT 64 MiB, class COMP_ZLIB | 4 | 2.00 | 2.00 | 128 | 1500 | 2000 | 2500 |"),
		Entry("comp-large-put's uploads, whose rate counts uploads", "### comp-large-put: throughput and latency",
			[]string{"PUT 64 MiB in 8 MiB parts, class COMP_ZLIB (uploads/s)", "4"},
			"| PUT 64 MiB in 8 MiB parts, class COMP_ZLIB (uploads/s) | 4 | 1.50 | 1.50 | 96.0 | 2000 | 3000 | 3000 |"),
		Entry("crc-put's checksummed puts, by their checksum", "### crc-put: throughput and latency",
			[]string{"PUT 4 KiB, CRC32", "8"}, "| PUT 4 KiB, CRC32 | 8 | 1999 | 1999 | 7.81 | 2.50 | 5.00 | 8.00 |"),
		Entry("comp-small-get's reads", "### comp-small-get: throughput and latency",
			[]string{"GET 4 KiB", "1"}, "| GET 4 KiB | 1 | 1458 | 1458 | 5.70 | 0.66 | 0.89 | 1.39 |"),
		Entry("comp-large-get's ranged reads", "### comp-large-get: throughput and latency",
			[]string{"GET 64 MiB in 4 MiB ranges", "8"}, "| GET 64 MiB in 4 MiB ranges | 8 | 8538 | 8538 | 33.4 | 0.90 | 1.26 | 1.78 |"),
		Entry("ver-put's writes", "### ver-put: throughput and latency",
			[]string{"PUT 4 KiB", "8"}, "| PUT 4 KiB | 8 | 8538 | 8538 | 33.4 | 0.90 | 1.26 | 1.78 |"),
		Entry("ver-get's reads", "### ver-get: throughput and latency",
			[]string{"GET 4 KiB", "8"}, "| GET 4 KiB | 8 | 8538 | 8538 | 33.4 | 0.90 | 1.26 | 1.78 |"),
	)

	It("reads an s3send phase's CPU per request, peaks and errors from its samples, and leaves its retries unknown", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{sendRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		// crc-put's 8-thread phase ran as small-get's did, from 14:14:07.279 for
		// 2.001 s: 6.41 s of utime and stime over its 4000 objects.
		Expect(tableRow(r.Markdown, "### crc-put: cost", "PUT 4 KiB, CRC32", "8")).
			To(Equal("| PUT 4 KiB, CRC32 | 8 | 1.60 | 605 | 189 | 0 | — |"))
		// comp-small-put has no samples; s3send retries nothing and says nothing of retries.
		Expect(tableRow(r.Markdown, "### comp-small-put: cost", "PUT 4 KiB, class COMP_ZLIB", "8")).
			To(Equal("| PUT 4 KiB, class COMP_ZLIB | 8 | — | — | — | 0 | — |"))
	})

	It("reports rgw-go's GC under the table of the profile whose log it is", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{sendRun("rgw-go")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("- rgw-go, crc-put: 5 cycles, longest stop-the-world pause 79.0 µs, 417 µs in all\n"))
	})

	It("names how s3send's rows count in its header, and lists its commands", func(ctx SpecContext) {
		r, err := report.Gateway(ctx, []string{sendRun("radosgw")})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Markdown).To(ContainSubstring("s3send's multipart row, marked (uploads/s), counts each upload as one request"))
		Expect(r.Markdown).To(ContainSubstring("s3send's are per object stored"))
		Expect(r.Markdown).To(ContainSubstring("s3send -endpoint http://10.89.0.34:80 -key BENCH0000000000000AK -region ceph-objectstore -op put " +
			"-duration 2s"))
		Expect(r.Markdown).NotTo(ContainSubstring("bench-secret"))
	})

	It("names an s3send phase with errors as a finding", func(ctx SpecContext) {
		dir := sendRun("radosgw")
		path := filepath.Join(dir, "crc-put-8.json")
		data, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(path, bytes.Replace(data, []byte(`"errors":0`), []byte(`"errors":3`), 1), 0o600)).To(Succeed())
		r, err := report.Gateway(ctx, []string{dir})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Findings).To(Equal([]string{"radosgw, crc-put, PUT 4 KiB, CRC32 at 8 threads: 3 errors"}))
		Expect(tableRow(r.Markdown, "### crc-put: cost", "PUT 4 KiB, CRC32", "8")).To(HaveSuffix("| 3 | — |"))
	})

	It("refuses runs that differ in the class they wrote through or in ver-put's objects", func(ctx SpecContext) {
		other := sendRun("rgw-go")
		editEnv(other, func(env map[string]any) {
			env["comp_class"] = "COMP_ZSTD"
			env["ver_objects"] = 3000
		})
		_, err := report.Gateway(ctx, []string{sendRun("radosgw"), other})
		Expect(err).To(MatchError(And(
			ContainSubstring("comp_class: COMP_ZLIB against COMP_ZSTD"),
			ContainSubstring("ver_objects: 1570 against 3000"),
		)))
	})

	It("refuses an s3send result without threads or a duration", func(ctx SpecContext) {
		dir := sendRun("radosgw")
		path := filepath.Join(dir, "comp-small-put-8.json")
		Expect(os.WriteFile(path, []byte(`{"op":"put","objects":10}`), 0o600)).To(Succeed())
		_, err := report.Gateway(ctx, []string{dir})
		Expect(err).To(MatchError(path + " has no threads or no duration"))
	})

	It("refuses an s3send result that is not JSON", func(ctx SpecContext) {
		dir := sendRun("radosgw")
		path := filepath.Join(dir, "crc-put-8.json")
		Expect(os.WriteFile(path, []byte(`{"op":"put","threads":8,`), 0o600)).To(Succeed())
		_, err := report.Gateway(ctx, []string{dir})
		Expect(err).To(MatchError(ContainSubstring("parsing " + path)))
	})

	It("writes the REPORT.md of such a run, which holds none of small-put's six", func(ctx SpecContext) {
		dir := sendRun("radosgw")
		stdout := &bytes.Buffer{}
		Expect(report.Run(ctx, []string{"gateway", "--dir", dir}, nil, stdout, &bytes.Buffer{})).To(Succeed())
		Expect(filepath.Join(dir, "REPORT.md")).To(BeARegularFile())
		Expect(stdout.String()).To(Equal(filepath.Join(dir, "REPORT.md") + ": no phase had an error or a retry\n"))
	})

	It("refuses a directory that holds no profile of any writer, naming the three", func(ctx SpecContext) {
		dir := sendRun("radosgw")
		matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
		Expect(err).NotTo(HaveOccurred())
		for _, m := range matches {
			if filepath.Base(m) != "env.json" {
				Expect(os.Remove(m)).To(Succeed())
			}
		}
		_, err = report.Gateway(ctx, []string{dir})
		Expect(err).To(MatchError(ContainSubstring("no <profile>.json from elbencho, no noop-<t>.json from httpload and no <profile>-<label>.json from s3send")))
	})
})
