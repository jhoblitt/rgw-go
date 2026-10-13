package seam_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

// settingEnv is every environment variable the command reads, cleared in
// each spec so that the caller's shell cannot reach it.
var settingEnv = []string{
	"BENCH_LIMITER", "BENCH_PLAN", "BENCH_INFLIGHT_BYTES", "BENCH_OVER_OBJECTS", "BENCH_QUICK", "BENCH_TIMEOUT",
	"BENCH_MIN_AVAIL_GIB", "BENCH_REPO", "BENCH_CONFIG", "BENCH_LOG_LEVEL", "BENCH_LOG_FORMAT",
	"BENCH_CEPH_CONF", "BENCH_ROOKET", "BENCH_GO_TAGS", "RGW_GO_TEST_CEPH_CONF", "ROOKET", "GO_TAGS",
}

var _ = Describe("Run", func() {
	var (
		w              *world
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		for _, name := range settingEnv {
			GinkgoT().Setenv(name, "")
		}
		w = newWorld()
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	// run runs the command against the fake world, keeping the streams and
	// the log handler the command gave the Runner.
	run := func(ctx context.Context, args ...string) error {
		return seam.RunWith(ctx, append([]string{"--repo", w.repo}, args...), nil, stdout, stderr, func(r *seam.Runner) {
			out, errOut, handler := r.Stdout, r.Stderr, r.Handler
			w.runner(r)
			r.Stdout, r.Stderr, r.Handler = out, errOut, handler
		})
	}

	// untouched holds when nothing ran and nothing was created.
	untouched := func() {
		Expect(w.exec.OutputCallCount()).To(BeZero(), "commands run")
		Expect(w.launch.BenchCallCount()).To(BeZero(), "benchmark processes run")
		Expect(filepath.Join(w.repo, "hack", "bench")).NotTo(BeAnExistingFile())
	}

	It("prints tonight's plan from bench-seam's environment, touching nothing", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("RGW_GO_TEST_CEPH_CONF", filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf"))
		Expect(run(ctx, "squid")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("squid", "104857600", "-limited", "640", "364")))
		untouched()
	})

	It("prints the plan of another bound and quick mode from flags", func(ctx SpecContext) {
		Expect(run(ctx, "tentacle", "--limiter=1", "--plan", "--inflight-bytes", "67108864", "--quick")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("tentacle", "67108864", "-limited-67108864", "128", "312")))
		untouched()
	})

	It("reads the bound, the count and the timeout from their BENCH_ names", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_INFLIGHT_BYTES", "52428800")
		GinkgoT().Setenv("BENCH_OVER_OBJECTS", "300")
		GinkgoT().Setenv("BENCH_TIMEOUT", "10m")
		Expect(run(ctx, "squid", "--plan")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("squid", "52428800", "-limited-52428800", "300", "600")))
	})

	It("lets a flag win over the environment", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_INFLIGHT_BYTES", "52428800")
		Expect(run(ctx, "squid", "--plan", "--inflight-bytes", "67108864")).To(Succeed())
		Expect(stdout.String()).To(ContainSubstring("-inflight-bytes=67108864 "))
	})

	It("builds with the tags bench-seam passes as GO_TAGS", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("GO_TAGS", "ceph_preview,extra")
		Expect(run(ctx, "squid")).To(Succeed())
		_, build := w.exec.OutputArgsForCall(0)
		Expect(build.Args).To(ContainElement("-tags=ceph_preview,extra,integration"))
		Expect(w.launch.BenchCallCount()).To(Equal(3))
		Expect(stderr.String()).To(ContainSubstring(`"msg":"bench done","mode":"pipe"`), "the log goes to stderr too")
		Expect(stdout.String()).To(Equal(strings.Repeat("BenchmarkSeam/shape=write4m/conc=64\n", 3)), "the processes' output goes to stdout")
	})

	It("runs the toolbox through ROOKET", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("ROOKET", "/opt/bin/rooket")
		w.exec.OutputStub = func(ctx context.Context, c seam.Cmd) ([]byte, error) {
			if c.Name == "/opt/bin/rooket" {
				c.Name = "rooket"
			}
			return w.output(ctx, c)
		}
		Expect(run(ctx, "squid")).To(Succeed())
		var names []string
		for i := range w.exec.OutputCallCount() {
			_, c := w.exec.OutputArgsForCall(i)
			names = append(names, c.Name)
		}
		Expect(names).To(ContainElement("/opt/bin/rooket"))
		Expect(names).NotTo(ContainElement("rooket"))
	})

	DescribeTable("refuses before it touches anything",
		func(ctx SpecContext, env map[string]string, want string) {
			for name, value := range env {
				GinkgoT().Setenv(name, value)
			}
			Expect(run(ctx, "squid")).To(MatchError(want))
			Expect(stdout.String()).To(BeEmpty())
			untouched()
		},
		Entry("a plan without the limiter", map[string]string{"BENCH_PLAN": "1"},
			"BENCH_PLAN=1 prints the limiter re-run only: set BENCH_LIMITER=1"),
		Entry("a byte bound without the limiter", map[string]string{"BENCH_INFLIGHT_BYTES": "67108864"},
			"BENCH_INFLIGHT_BYTES applies only with BENCH_LIMITER=1"),
		Entry("a limiter other than 1", map[string]string{"BENCH_LIMITER": "yes", "BENCH_PLAN": "1"},
			"BENCH_LIMITER is yes: set it to 1 or leave it unset"),
		Entry("a plan other than 1, which must not start the run", map[string]string{"BENCH_LIMITER": "1", "BENCH_PLAN": "yes"},
			"BENCH_PLAN is yes: set it to 1 or leave it unset"),
		Entry("quick mode other than 1", map[string]string{"BENCH_LIMITER": "1", "BENCH_QUICK": "yes"},
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("a zero timeout in a plan", map[string]string{"BENCH_LIMITER": "1", "BENCH_PLAN": "1", "BENCH_TIMEOUT": "0"},
			"BENCH_TIMEOUT 0 is not a duration in h, m and s from 1 s to 99999999 s"),
		Entry("a zero timeout in a run", map[string]string{"BENCH_LIMITER": "1", "BENCH_TIMEOUT": "0"},
			"BENCH_TIMEOUT 0 is not a duration in h, m and s from 1 s to 99999999 s"),
		Entry("free space with a unit", map[string]string{"BENCH_LIMITER": "1", "BENCH_MIN_AVAIL_GIB": "5G"},
			"BENCH_MIN_AVAIL_GIB is 5G: give a whole number of GiB, at most 8589934591"),
	)

	It("refuses another ceph.conf, naming the release's", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("RGW_GO_TEST_CEPH_CONF", "/elsewhere/ceph.conf")
		Expect(run(ctx, "squid", "--plan")).To(MatchError("RGW_GO_TEST_CEPH_CONF is /elsewhere/ceph.conf, but the squid cluster is reached through " +
			filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf") + ": unset it"))
		untouched()
	})

	It("refuses a --plan given a value other than 1", func(ctx SpecContext) {
		Expect(run(ctx, "squid", "--limiter=1", "--plan=yes")).To(MatchError("BENCH_PLAN is yes: set it to 1 or leave it unset"))
		untouched()
	})

	It("takes --plan=1 as --plan", func(ctx SpecContext) {
		Expect(run(ctx, "squid", "--limiter=1", "--plan=1", "--quick=1")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("squid", "104857600", "-limited", "128", "312")))
		untouched()
	})

	It("takes exactly one release", func(ctx SpecContext) {
		Expect(run(ctx)).To(MatchError("accepts 1 arg(s), received 0"))
		Expect(run(ctx, "squid", "tentacle")).To(MatchError("accepts 1 arg(s), received 2"))
	})

	It("refuses an unknown log format", func(ctx SpecContext) {
		Expect(run(ctx, "--log-format", "xml", "squid", "--limiter=1", "--plan")).To(MatchError(`unknown log format "xml"`))
	})
})
