package gateway_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// settingEnv is every environment variable the command reads, unset in each
// spec so that the caller's shell cannot reach it.
var settingEnv = []string{
	"BENCH_SECONDS", "BENCH_CONC", "BENCH_PUT_OBJECTS", "BENCH_GET_OBJECTS", "BENCH_LARGE_THREADS", "BENCH_LARGE_OBJECTS",
	"BENCH_LIST_THREADS", "BENCH_LIST_OBJECTS", "BENCH_SMOKE", "BENCH_PLAN", "BENCH_IDLE_AVAIL", "RGW_GO_FLAGS", "GO_TAGS",
	"ROOKET", "BENCH_RGW_GO_FLAGS", "BENCH_GO_TAGS", "BENCH_ROOKET", "BENCH_REPO", "BENCH_CONFIG", "BENCH_LOG_LEVEL",
	"BENCH_LOG_FORMAT",
}

var _ = Describe("Run", func() {
	var (
		w              *world
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		for _, name := range settingEnv {
			// Setenv restores the caller's value after the spec.
			GinkgoT().Setenv(name, "")
			Expect(os.Unsetenv(name)).To(Succeed())
		}
		w = newWorld("rgw-go")
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	// run runs the command against the fake world, keeping the streams and
	// the log handler the command gave the Runner.
	run := func(ctx context.Context, args ...string) error {
		return gateway.RunWith(ctx, append([]string{"--repo", w.repo}, args...), nil, stdout, stderr, func(r *gateway.Runner) {
			out, errOut, handler := r.Stdout, r.Stderr, r.Handler
			w.runner(r)
			r.Stdout, r.Stderr, r.Handler = out, errOut, handler
		})
	}

	// untouched holds when nothing ran and nothing was created.
	untouched := func() {
		GinkgoHelper()
		Expect(w.exec.OutputCallCount()).To(BeZero(), "commands run")
		Expect(filepath.Join(w.repo, "hack", "bench", "out", release)).NotTo(BeAnExistingFile())
	}

	It("prints the defaults' plan from bench-plan-check's environment, touching nothing", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("BENCH_IDLE_AVAIL", "8220119040")
		GinkgoT().Setenv("ROOKET", "/nonexistent/rooket")
		Expect(run(ctx, "tentacle", "radosgw")).To(Succeed())
		Expect(stdout.String()).To(Equal(golden("plan-default.txt")))
		untouched()
	})

	It("prints a smoke run's plan for BENCH_SMOKE=1", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("BENCH_SMOKE", "1")
		GinkgoT().Setenv("BENCH_IDLE_AVAIL", "8220119040")
		Expect(run(ctx, "squid", "rgw-go")).To(Succeed())
		Expect(stdout.String()).To(Equal(golden("plan-smoke.txt")))
		untouched()
	})

	It("takes --plan, --smoke and --idle-avail as flags, --plan and --smoke alone meaning 1", func(ctx SpecContext) {
		Expect(run(ctx, "tentacle", "radosgw", "--plan", "--smoke", "--idle-avail", "8220119040")).To(Succeed())
		Expect(stdout.String()).To(Equal(golden("plan-smoke.txt")))
		untouched()
	})

	It("fails a plan whose dataset needs more than the free bytes, after printing it", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("BENCH_IDLE_AVAIL", "4000000000")
		Expect(run(ctx, "tentacle", "radosgw", "small-put")).To(MatchError(
			"a profile's dataset is more than half of 4000000000 B: lower its threads or objects"))
		Expect(stdout.String()).To(Equal("small-put dataset  3145728000 B, needs  6291456000 B of  4000000000 B free: DOES NOT FIT\n"))
		untouched()
	})

	// Each sizing setting comes from its environment variable and changes
	// the plan; the default of each is in the defaults' plan above.
	DescribeTable("plans under",
		func(ctx SpecContext, name, value, profile, want string) {
			GinkgoT().Setenv("BENCH_PLAN", "1")
			GinkgoT().Setenv("BENCH_IDLE_AVAIL", "99999999999")
			GinkgoT().Setenv(name, value)
			Expect(run(ctx, "tentacle", "radosgw", profile)).To(Succeed())
			Expect(stdout.String()).To(Equal(want))
			untouched()
		},
		Entry("BENCH_CONC, whose top concurrency sizes small-put", "BENCH_CONC", "8 64", "small-put",
			"small-put dataset   786432000 B, needs  1572864000 B of 99999999999 B free: fits\n"),
		Entry("BENCH_PUT_OBJECTS", "BENCH_PUT_OBJECTS", "100", "small-put",
			"small-put dataset   104857600 B, needs   209715200 B of 99999999999 B free: fits\n"),
		Entry("BENCH_GET_OBJECTS", "BENCH_GET_OBJECTS", "100", "small-get",
			"small-get dataset   104857600 B, needs   209715200 B of 99999999999 B free: fits\n"),
		Entry("BENCH_LARGE_THREADS", "BENCH_LARGE_THREADS", "8", "large-put",
			"large-put dataset   536870912 B, needs  1073741824 B of 99999999999 B free: fits\n"),
		Entry("BENCH_LARGE_OBJECTS", "BENCH_LARGE_OBJECTS", "3", "large-get",
			"large-get dataset  6442450944 B, needs 12884901888 B of 99999999999 B free: fits\n"),
	)

	// The settings the plan does not show reach the commands of a run.
	DescribeTable("runs under",
		func(ctx SpecContext, env map[string]string, profile, want string) {
			for name, value := range env {
				GinkgoT().Setenv(name, value)
			}
			Expect(run(ctx, "tentacle", "rgw-go", profile)).To(Succeed())
			Expect(w.trace()).To(ContainSubstring(want))
		},
		Entry("BENCH_SECONDS, each phase's time limit and the CPU profile's", map[string]string{"BENCH_SECONDS": "7"}, "small-get",
			"-r -t 256 -n 0 -N 400 -s 4k -b 4k --timelimit 7 --infloop bench-small\n"),
		Entry("BENCH_LIST_THREADS and BENCH_LIST_OBJECTS", map[string]string{"BENCH_LIST_THREADS": "2", "BENCH_LIST_OBJECTS": "3"}, "list",
			"--s3listobj 6 -t 1 --timelimit 60 --infloop bench-list\n"),
		Entry("RGW_GO_FLAGS, rgw-go serve's flags", map[string]string{"RGW_GO_FLAGS": "--rados-completions=pipe --log-level=debug"}, "list",
			"REPO/hack/rooket/rgw-go-up.sh tentacle --rados-completions=pipe --log-level=debug\n"),
		Entry("GO_TAGS, rgw-go's build tags and the report's", map[string]string{"GO_TAGS": "ceph_preview,extra"}, "list",
			"go -C REPO build -tags=ceph_preview,extra -o bin/rgw-go ./cmd/rgw-go\nGODEBUG=gctrace=1 GO_TAGS=ceph_preview,extra"),
		Entry("BENCH_SMOKE=1's concurrency where BENCH_CONC is unset", map[string]string{"BENCH_SMOKE": "1"}, "noop",
			"REPO/hack/bench/out/httpload -url http://127.0.0.1:7481/ -threads 8 -duration 5s -out DIR/noop-8.json\n"),
	)

	It("runs rooket and rgw-go-up.sh through ROOKET", func(ctx SpecContext) {
		GinkgoT().Setenv("ROOKET", "/opt/bin/rooket")
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if c.Name == "/opt/bin/rooket" {
				c.Name = "rooket"
			}
			return w.output(ctx, c)
		}
		Expect(run(ctx, "tentacle", "rgw-go", "list")).To(Succeed())
		names := map[string]bool{}
		for i := range w.exec.OutputCallCount() {
			_, c := w.exec.OutputArgsForCall(i)
			names[c.Name] = true
			if strings.HasSuffix(c.Name, ".sh") && !strings.HasSuffix(c.Name, "elbencho-build.sh") {
				Expect(c.Env).To(ContainElement("ROOKET=/opt/bin/rooket"), "script %s", c.Name)
			}
		}
		Expect(names).To(HaveKey("/opt/bin/rooket"))
		Expect(names).NotTo(HaveKey("rooket"))
	})

	DescribeTable("reads RGW_GO_FLAGS, GO_TAGS and ROOKET from those names only",
		func(ctx SpecContext, env map[string]string, tags, flags string) {
			for name, value := range env {
				GinkgoT().Setenv(name, value)
			}
			// A rooket the world does not answer to would fail the run.
			Expect(run(ctx, "tentacle", "rgw-go", "list")).To(Succeed())
			Expect(w.trace()).To(SatisfyAll(
				ContainSubstring("go -C REPO build -tags="+tags+" -o bin/rgw-go ./cmd/rgw-go\n"),
				ContainSubstring("GO_TAGS="+tags+" ROOKET=rooket REPO/hack/rooket/rgw-go-up.sh tentacle"+flags+"\n"),
				ContainSubstring("ROOKET_NAME=rgw-go-tentacle rooket version\n")))
		},
		Entry("a BENCH_ alias beside each, which overrides none of them",
			map[string]string{
				"GO_TAGS": "ceph_preview,doc", "BENCH_GO_TAGS": "alias", "RGW_GO_FLAGS": "--rados-completions=pipe",
				"BENCH_RGW_GO_FLAGS": "--alias", "ROOKET": "rooket", "BENCH_ROOKET": "/alias/rooket",
			},
			"ceph_preview,doc", " --rados-completions=pipe"),
		Entry("a BENCH_ alias alone, which stands in for none of them",
			map[string]string{"BENCH_GO_TAGS": "alias", "BENCH_RGW_GO_FLAGS": "--alias", "BENCH_ROOKET": "/alias/rooket"},
			"ceph_preview", ""),
	)

	It("records the config file it read and the repository, rooket and build tags in effect, in env.json and its first log record", func(ctx SpecContext) {
		config := filepath.Join(GinkgoT().TempDir(), "gateway.yaml")
		Expect(os.WriteFile(config, []byte("go-tags: ceph_preview,fromfile\nput-objects: \"100\"\n"), 0o600)).To(Succeed())
		GinkgoT().Setenv("BENCH_CONFIG", config)
		GinkgoT().Setenv("RGW_GO_FLAGS", "--rados-completions=pipe")
		Expect(run(ctx, "tentacle", "rgw-go", "small-put", "--smoke")).To(Succeed())
		Expect(w.readFile("env.json")).To(SatisfyAll(
			ContainSubstring(`"rgw_go_flags": "--rados-completions=pipe",`),
			ContainSubstring(`"put_objects": 100,`),
			HaveSuffix(fmt.Sprintf("  \"repo\": %q,\n  \"rooket\": \"rooket\",\n  \"go_tags\": \"ceph_preview,fromfile\",\n  \"config\": %q\n}\n",
				w.repo, config))))
		first := fmt.Sprintf(`"msg":"gateway comparison","gateway":"rgw-go","cluster":"rgw-go-tentacle","profiles":"small-put",`+
			`"dir":%q,"config":%q,"repo":%q,"rooket":"rooket","go_tags":"ceph_preview,fromfile","rgw_go_flags":"--rados-completions=pipe"}`,
			w.dir(), config, w.repo)
		Expect(stderr.String()).To(ContainSubstring(first))
		Expect(w.readFile("run.log")).To(ContainSubstring(first))
	})

	It("lets a flag win over the environment", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_PLAN", "1")
		GinkgoT().Setenv("BENCH_IDLE_AVAIL", "8220119040")
		GinkgoT().Setenv("BENCH_PUT_OBJECTS", "100")
		Expect(run(ctx, "tentacle", "radosgw", "small-put", "--put-objects", "200")).To(Succeed())
		Expect(stdout.String()).To(HavePrefix("small-put dataset   209715200 B"))
	})

	It("logs the run to stderr and run.log, and prints the plan, the phases and the report's answer", func(ctx SpecContext) {
		GinkgoT().Setenv("BENCH_SMOKE", "1")
		Expect(run(ctx, "tentacle", "rgw-go", "list")).To(Succeed())
		Expect(stderr.String()).To(ContainSubstring(`"msg":"profile done","profile":"list"`))
		Expect(stdout.String()).To(Equal("list      dataset           0 B, needs           0 B of  8220119040 B free: fits\n" +
			"elbencho done\nelbencho done\n" + filepath.Join(w.dir(), "REPORT.md") + ": no phase had an error or a retry\n"))
		Expect(w.readFile("run.log")).To(ContainSubstring(`"msg":"profile done","profile":"list"`))
	})

	DescribeTable("refuses before it touches anything",
		func(ctx SpecContext, env map[string]string, args []string, want string) {
			for name, value := range env {
				GinkgoT().Setenv(name, value)
			}
			Expect(run(ctx, args...)).To(MatchError(want))
			Expect(stdout.String()).To(BeEmpty())
			untouched()
		},
		Entry("BENCH_SMOKE=yes, which must not start a full run", map[string]string{"BENCH_SMOKE": "yes"},
			[]string{"tentacle", "radosgw"}, "BENCH_SMOKE is yes: set it to 1 or leave it unset"),
		Entry("BENCH_PLAN=yes, which must not start the run", map[string]string{"BENCH_PLAN": "yes", "BENCH_IDLE_AVAIL": "1"},
			[]string{"tentacle", "radosgw"}, "BENCH_PLAN is yes: set it to 1 or leave it unset"),
		Entry("a --smoke other than 1", nil, []string{"tentacle", "radosgw", "--smoke=true"},
			"BENCH_SMOKE is true: set it to 1 or leave it unset"),
		Entry("BENCH_IDLE_AVAIL without BENCH_PLAN=1", map[string]string{"BENCH_IDLE_AVAIL": "8220119040"},
			[]string{"tentacle", "radosgw"}, "BENCH_IDLE_AVAIL applies only with BENCH_PLAN=1"),
		Entry("BENCH_PLAN=1 without BENCH_IDLE_AVAIL", map[string]string{"BENCH_PLAN": "1"},
			[]string{"tentacle", "radosgw"}, "BENCH_PLAN=1 needs BENCH_IDLE_AVAIL, the data pool's free bytes"),
		Entry("a zero BENCH_SECONDS", map[string]string{"BENCH_SECONDS": "0"},
			[]string{"tentacle", "rgw-go"}, "BENCH_SECONDS is '0', not a positive count of at most 9 digits"),
		Entry("BENCH_CONC separated by commas", map[string]string{"BENCH_CONC": "1,8"},
			[]string{"tentacle", "rgw-go"}, "BENCH_CONC holds '1,8', not a positive count of at most 9 digits"),
		Entry("a BENCH_LIST_OBJECTS that is no count", map[string]string{"BENCH_LIST_OBJECTS": "lots"},
			[]string{"tentacle", "rgw-go"}, "BENCH_LIST_OBJECTS is 'lots', not a positive count of at most 9 digits"),
		Entry("an unknown profile", nil, []string{"tentacle", "rgw-go", "small-put", "mixed"},
			"no profile mixed; the profiles are small-put small-get noop large-put large-get list"),
		Entry("an unknown gateway", nil, []string{"tentacle", "minio"}, `gateway "minio" is not radosgw or rgw-go`),
		Entry("an unknown release", nil, []string{"reef", "radosgw"}, `release "reef" is not squid or tentacle`),
		Entry("no gateway", nil, []string{"tentacle"}, "requires at least 2 arg(s), only received 1"),
		Entry("an unknown log format", nil, []string{"--log-format", "xml", "tentacle", "radosgw"}, `unknown log format "xml"`),
	)
})
