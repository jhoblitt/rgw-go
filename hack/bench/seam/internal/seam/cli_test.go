package seam_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// settingEnv is every environment variable the command reads, unset in each
// spec so that the caller's shell cannot reach it.
var settingEnv = []string{
	"BENCH_LIMITER", "BENCH_PLAN", "BENCH_INFLIGHT_BYTES", "BENCH_OVER_OBJECTS", "BENCH_QUICK", "BENCH_TIMEOUT",
	"BENCH_MIN_AVAIL_GIB", "BENCH_REPO", "BENCH_CONFIG", "BENCH_LOG_LEVEL", "BENCH_LOG_FORMAT",
	"BENCH_CEPH_CONF", "BENCH_ROOKET", "BENCH_GO_TAGS", "RGW_GO_TEST_CEPH_CONF", "ROOKET", "GO_TAGS",
	"BENCH_TIME", "BENCH_BENCH_TIME", "BENCH_FLOOR_SECONDS", "CONC", "BENCH_CONC", "BENCH_LARGE_CONC", "BENCH_WRITE4M_OBJECTS",
	"BENCH_FLOOR_TIMEOUT", "BENCH_FLOOR_IMAGE", "ROOKET_ENGINE", "BENCH_ENGINE",
}

var _ = Describe("Run", func() {
	var (
		w              *world
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		for _, name := range settingEnv {
			// Setenv restores the caller's value after the spec; unset, an
			// empty BENCH_LARGE_CONC would skip the 4 MiB cells.
			GinkgoT().Setenv(name, "")
			Expect(os.Unsetenv(name)).To(Succeed())
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
		Expect(w.launch.FloorCallCount()).To(BeZero(), "floor runs run")
		Expect(filepath.Join(w.repo, "hack", "bench")).NotTo(BeAnExistingFile())
	}

	It("prints the sweep's plan from bench-seam's environment, touching nothing", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("RGW_GO_TEST_CEPH_CONF", filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf"))
		GinkgoT().Setenv("GO_TAGS", "ceph_preview")
		Expect(run(ctx, "squid")).To(Succeed())
		Expect(stdout.String()).To(Equal(golden("plan-sweep.txt")))
		untouched()
	})

	It("prints the quick sweep's plan for BENCH_QUICK=1", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("BENCH_QUICK", "1")
		Expect(run(ctx, "squid")).To(Succeed())
		Expect(stdout.String()).To(Equal(golden("plan-sweep-quick.txt")))
		untouched()
	})

	It("prints the limiter plan from bench-seam's environment, touching nothing", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("RGW_GO_TEST_CEPH_CONF", filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf"))
		Expect(run(ctx, "squid")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("squid", "104857600", "-limited", "640", "364")))
		untouched()
	})

	It("prints the limiter plan of another bound and quick mode from flags", func(ctx SpecContext) {
		Expect(run(ctx, "tentacle", "--limiter=1", "--plan", "--inflight-bytes", "67108864", "--quick")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("tentacle", "67108864", "-limited-67108864", "128", "312")))
		untouched()
	})

	It("reads the limiter's bound, count and timeout from their BENCH_ names", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_INFLIGHT_BYTES", "52428800")
		GinkgoT().Setenv("BENCH_OVER_OBJECTS", "300")
		GinkgoT().Setenv("BENCH_TIMEOUT", "10m")
		Expect(run(ctx, "squid", "--plan")).To(Succeed())
		Expect(stdout.String()).To(Equal(plan("squid", "52428800", "-limited-52428800", "300", "600")))
	})

	// Each of the sweep's settings comes from its environment variable and
	// changes the plan; the default of each is in the default plan above.
	DescribeTable("plans the sweep under",
		func(ctx SpecContext, name, value string, want, unwanted []string) {
			GinkgoT().Setenv(name, value)
			Expect(run(ctx, "squid", "--plan")).To(Succeed())
			for _, line := range want {
				Expect(stdout.String()).To(ContainSubstring(line))
			}
			for _, line := range unwanted {
				Expect(stdout.String()).NotTo(ContainSubstring(line))
			}
			untouched()
		},
		Entry("BENCH_TIME, each time-bounded cell's limit ten times it and five minutes", "BENCH_TIME", "1m",
			[]string{"bench sync: -shapes=headread4k -conc=1 -> seam-sync.jsonl (limit 900 s)\n"}, nil),
		Entry("BENCH_TIME in plain seconds", "BENCH_TIME", "60",
			[]string{"bench pipe: -shapes=read4k -conc=256 -test.cpuprofile=cpu-pipe-read4k-256.pprof (limit 900 s)\n"}, nil),
		Entry("BENCH_FLOOR_SECONDS", "BENCH_FLOOR_SECONDS", "30",
			[]string{"floor write4k at 64 (before) from the floor image: rados bench write for 30 s (limit 330 s)\n"}, nil),
		Entry("CONC", "CONC", "64,512",
			[]string{"bench sync: -shapes=indexrtt -conc=512 -> seam-sync.jsonl", "floor read4k at 512 (single)", "floor read4k at 64 (before)"},
			[]string{"-shapes=headread4k -conc=1 ", "-shapes=read4k -conc=16 ", "read4k at 256"}),
		Entry("BENCH_LARGE_CONC", "BENCH_LARGE_CONC", "4",
			[]string{"floor read4m at 4 (before)", "bench sync: -shapes=write4m -conc=4 -test.benchtime=1000x"},
			[]string{"read4m at 1 ", "read4m at 16 ", "write4m at 16 "}),
		Entry("an empty BENCH_LARGE_CONC, which skips the 4 MiB cells", "BENCH_LARGE_CONC", "",
			[]string{"bench callback: -shapes=write4m -conc=64 -budget-bytes=0"},
			[]string{"read4m", "floor write4m"}),
		Entry("BENCH_WRITE4M_OBJECTS", "BENCH_WRITE4M_OBJECTS", "50",
			[]string{"bench sync: -shapes=write4m -conc=1 -test.benchtime=50x -> seam-sync.jsonl (limit 305 s)\n", "rados bench write of 50 objects"}, nil),
		Entry("BENCH_OVER_OBJECTS", "BENCH_OVER_OBJECTS", "300",
			[]string{"-budget-bytes=0 -test.benchtime=300x -> overbudget-callback.jsonl (limit 330 s)\n"}, nil),
		Entry("BENCH_MIN_AVAIL_GIB", "BENCH_MIN_AVAIL_GIB", "7",
			[]string{"wait for 7 GiB available in rgw-go-test\n"}, []string{"wait for 5 GiB"}),
		Entry("BENCH_TIMEOUT", "BENCH_TIMEOUT", "10m",
			[]string{"bench sync: -shapes=write4m -conc=1 -test.benchtime=1000x -> seam-sync.jsonl (limit 600 s)\n"}, []string{"(limit 500 s)"}),
		Entry("BENCH_FLOOR_TIMEOUT", "BENCH_FLOOR_TIMEOUT", "16m",
			[]string{"rados bench write of 1000 objects (limit 960 s)\n", "rados bench write for 20 s (limit 960 s)\n"}, []string{"(limit 320 s)"}),
	)

	It("lets a flag win over the environment", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_INFLIGHT_BYTES", "52428800")
		Expect(run(ctx, "squid", "--plan", "--inflight-bytes", "67108864")).To(Succeed())
		Expect(stdout.String()).To(ContainSubstring("-inflight-bytes=67108864 "))
	})

	It("lets --large-conc win over BENCH_LARGE_CONC, empty or not", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LARGE_CONC", "")
		Expect(run(ctx, "squid", "--plan", "--large-conc", "16")).To(Succeed())
		Expect(stdout.String()).To(ContainSubstring("floor read4m at 16 (before)"))
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

	// Each alias, were it read, would fail the run: a sweep setting under
	// BENCH_LIMITER=1 is refused, another ceph.conf is refused, and the world
	// answers no other rooket.
	DescribeTable("reads its documented variables from those names only",
		func(ctx SpecContext, env map[string]string, tags string) {
			GinkgoT().Setenv("BENCH_LIMITER", "1")
			for name, value := range env {
				GinkgoT().Setenv(name, value)
			}
			Expect(run(ctx, "squid")).To(Succeed())
			_, build := w.exec.OutputArgsForCall(0)
			Expect(build.Args).To(ContainElement("-tags=" + tags + ",integration"))
		},
		Entry("a BENCH_ alias beside each, which overrides none of them",
			map[string]string{
				"GO_TAGS": "ceph_preview,doc", "BENCH_GO_TAGS": "alias", "ROOKET": "rooket", "BENCH_ROOKET": "/alias/rooket",
				"BENCH_CEPH_CONF": "/alias/ceph.conf", "BENCH_BENCH_TIME": "5s", "BENCH_CONC": "64",
			},
			"ceph_preview,doc"),
		Entry("a BENCH_ alias alone, which stands in for none of them",
			map[string]string{
				"BENCH_GO_TAGS": "alias", "BENCH_ROOKET": "/alias/rooket", "BENCH_CEPH_CONF": "/alias/ceph.conf",
				"BENCH_BENCH_TIME": "5s", "BENCH_CONC": "64",
			},
			"ceph_preview"),
	)

	It("records the config file it read and the repository, rooket and build tags in effect, in env.json and its first log record", func(ctx SpecContext) {
		config := filepath.Join(GinkgoT().TempDir(), "seam.yaml")
		Expect(os.WriteFile(config, []byte("go-tags: ceph_preview,fromfile\nover-objects: \"300\"\n"), 0o600)).To(Succeed())
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("BENCH_CONFIG", config)
		Expect(run(ctx, "squid")).To(Succeed())
		env, err := os.ReadFile(filepath.Join(w.limiterDir(), "env.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(env)).To(SatisfyAll(
			ContainSubstring(`"over_objects": 300,`),
			HaveSuffix(fmt.Sprintf("  \"inflight_bytes\": 104857600,\n  \"repo\": %q,\n  \"rooket\": \"rooket\",\n"+
				"  \"go_tags\": \"ceph_preview,fromfile\",\n  \"config\": %q\n}\n", w.repo, config))))
		first := fmt.Sprintf(`"msg":"seam limiter re-run","release":"squid","cluster_image":"quay.io/ceph/ceph:v20.2.4","dir":%q,`+
			`"inflight_bytes":104857600,"config":%q,"repo":%q,"rooket":"rooket","go_tags":"ceph_preview,fromfile"}`,
			w.limiterDir(), config, w.repo)
		Expect(stderr.String()).To(ContainSubstring(first))
		sweep, err := os.ReadFile(filepath.Join(w.limiterDir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(first))
	})

	It("runs the toolbox through ROOKET", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_LIMITER", "1")
		GinkgoT().Setenv("ROOKET", "/opt/bin/rooket")
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
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

	It("runs the quick sweep's floor through ROOKET_ENGINE from BENCH_FLOOR_IMAGE, renders its report and prints its answer", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_QUICK", "1")
		GinkgoT().Setenv("ROOKET_ENGINE", "docker")
		GinkgoT().Setenv("BENCH_FLOOR_IMAGE", "registry.local/ceph:v19.2.6")
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if c.Name == "docker" {
				c.Name = "podman"
			}
			return w.output(ctx, c)
		}
		Expect(run(ctx, "squid")).To(Succeed())
		Expect(w.commands()).To(ContainElements(
			"docker image inspect registry.local/ceph:v19.2.6",
			"docker run --rm registry.local/ceph:v19.2.6 rados --version",
			"go -C "+w.repo+" run -tags=ceph_preview ./hack/bench/report seam --dir "+w.sweepDir()))
		Expect(w.launch.BenchCallCount()).To(Equal(43), "the quick plan's benchmark processes")
		_, floor := w.launch.FloorArgsForCall(0)
		Expect([]string{floor.Engine, floor.Args[slices.Index(floor.Args, "rados")-1]}).To(Equal([]string{"docker", "registry.local/ceph:v19.2.6"}))
		Expect(stdout.String()).To(HaveSuffix(filepath.Join(w.sweepDir(), "REPORT.md") + ": cgo is not the bottleneck\n"))
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
		Entry("quick mode of yes, which must not run a full sweep", map[string]string{"BENCH_QUICK": "yes"},
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("quick mode of yes in a plan", map[string]string{"BENCH_PLAN": "1", "BENCH_QUICK": "yes"},
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("quick mode of yes in the limiter re-run", map[string]string{"BENCH_LIMITER": "1", "BENCH_QUICK": "yes"},
			"BENCH_QUICK is yes: set it to 1 or leave it unset"),
		Entry("a byte bound without the limiter", map[string]string{"BENCH_INFLIGHT_BYTES": "67108864"},
			"BENCH_INFLIGHT_BYTES applies only with BENCH_LIMITER=1"),
		Entry("a limiter other than 1", map[string]string{"BENCH_LIMITER": "yes", "BENCH_PLAN": "1"},
			"BENCH_LIMITER is yes: set it to 1 or leave it unset"),
		Entry("a plan other than 1, which must not start the run", map[string]string{"BENCH_PLAN": "yes"},
			"BENCH_PLAN is yes: set it to 1 or leave it unset"),
		Entry("a zero timeout in a plan", map[string]string{"BENCH_PLAN": "1", "BENCH_TIMEOUT": "0"},
			"BENCH_TIMEOUT 0 is not a duration in h, m and s from 1 s to 99999999 s"),
		Entry("a zero timeout in a run", map[string]string{"BENCH_LIMITER": "1", "BENCH_TIMEOUT": "0"},
			"BENCH_TIMEOUT 0 is not a duration in h, m and s from 1 s to 99999999 s"),
		Entry("a benchtime of go test's own", map[string]string{"BENCH_TIME": "1.5s"},
			"BENCH_TIME 1.5s is not a duration in h, m and s from 1 s to 99999999 s"),
		Entry("floor seconds with a unit", map[string]string{"BENCH_FLOOR_SECONDS": "20s"},
			"BENCH_FLOOR_SECONDS is 20s: give a positive whole number of seconds, at most 8 digits"),
		Entry("concurrencies with spaces", map[string]string{"CONC": "16, 256"},
			"CONC is 16, 256: give positive whole numbers, comma separated, each at most once"),
		Entry("a 4 MiB concurrency the report pairs with no floor", map[string]string{"BENCH_LARGE_CONC": "1,2"},
			"BENCH_LARGE_CONC is 1,2: the 4 MiB cells run only at 1, 4 and 16, each at most once, where the report pairs them with their floor; empty skips them"),
		Entry("zero write4m objects", map[string]string{"BENCH_WRITE4M_OBJECTS": "0"},
			"BENCH_WRITE4M_OBJECTS is 0: give a positive whole number of at most 10 digits"),
		Entry("zero over-budget objects", map[string]string{"BENCH_OVER_OBJECTS": "0"},
			"BENCH_OVER_OBJECTS is 0: give a positive whole number of at most 10 digits"),
		Entry("free space with a unit", map[string]string{"BENCH_MIN_AVAIL_GIB": "5G"},
			"BENCH_MIN_AVAIL_GIB is 5G: give a whole number of GiB, at most 8589934591"),
		Entry("a floor timeout in milliseconds", map[string]string{"BENCH_FLOOR_TIMEOUT": "500ms"},
			"BENCH_FLOOR_TIMEOUT 500ms is not a duration in h, m and s from 1 s to 99999999 s"),
		Entry("the sweep's settings in the limiter re-run", map[string]string{"BENCH_LIMITER": "1", "BENCH_FLOOR_SECONDS": "20"},
			"BENCH_FLOOR_SECONDS applies only to the sweep, not with BENCH_LIMITER=1"),
		Entry("an empty BENCH_LARGE_CONC in the limiter re-run", map[string]string{"BENCH_LIMITER": "1", "BENCH_LARGE_CONC": ""},
			"BENCH_LARGE_CONC applies only to the sweep, not with BENCH_LIMITER=1"),
		Entry("another floor image in the limiter re-run", map[string]string{"BENCH_LIMITER": "1", "BENCH_FLOOR_IMAGE": "quay.io/ceph/ceph:v19.2.6"},
			"BENCH_FLOOR_IMAGE applies only to the sweep, not with BENCH_LIMITER=1"),
	)

	It("refuses another ceph.conf, naming the release's", func(ctx SpecContext) {
		GinkgoT().Setenv("RGW_GO_TEST_CEPH_CONF", "/elsewhere/ceph.conf")
		Expect(run(ctx, "squid", "--plan")).To(MatchError("RGW_GO_TEST_CEPH_CONF is /elsewhere/ceph.conf, but the squid cluster is reached through " +
			filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf") + ": unset it"))
		untouched()
	})

	It("refuses a --plan or a --quick given a value other than 1", func(ctx SpecContext) {
		Expect(run(ctx, "squid", "--plan=yes")).To(MatchError("BENCH_PLAN is yes: set it to 1 or leave it unset"))
		Expect(run(ctx, "squid", "--plan", "--quick=true")).To(MatchError("BENCH_QUICK is true: set it to 1 or leave it unset"))
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
