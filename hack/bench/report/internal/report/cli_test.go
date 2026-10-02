package report_test

import (
	"bytes"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/report/internal/report"
)

var _ = Describe("Run", func() {
	var stdout, stderr *bytes.Buffer

	BeforeEach(func() {
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	run := func(ctx SpecContext, args ...string) error {
		return report.Run(ctx, args, nil, stdout, stderr)
	}

	It("writes REPORT.md into --dir and prints its path and the answer", func(ctx SpecContext) {
		dir := sweep()
		Expect(run(ctx, "seam", "--dir", dir)).To(Succeed())
		md, err := os.ReadFile(filepath.Join(dir, "REPORT.md"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(md)).To(HavePrefix("# Seam microbenchmark: squid\n"))
		Expect(stdout.String()).To(Equal(filepath.Join(dir, "REPORT.md") + ": callback passes all four criteria\n"))
	})

	It("reads the directory from REPORT_DIR", func(ctx SpecContext) {
		dir := sweep()
		GinkgoT().Setenv("REPORT_DIR", dir)
		Expect(run(ctx, "seam")).To(Succeed())
		Expect(filepath.Join(dir, "REPORT.md")).To(BeARegularFile())
	})

	It("requires --dir", func(ctx SpecContext) {
		Expect(run(ctx, "seam")).To(MatchError("--dir is required"))
	})

	It("refuses an unknown log format", func(ctx SpecContext) {
		Expect(run(ctx, "--log-format", "xml", "seam", "--dir", sweep())).To(MatchError(ContainSubstring(`unknown log format "xml"`)))
	})

	It("writes no report when the sweep cannot be read", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		Expect(run(ctx, "seam", "--dir", dir)).To(MatchError(ContainSubstring("missing env.json")))
		Expect(filepath.Join(dir, "REPORT.md")).NotTo(BeAnExistingFile())
	})
})
