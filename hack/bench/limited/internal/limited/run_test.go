package limited_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/limited/internal/limited"
	"github.com/jhoblitt/rgw-go/hack/bench/limited/internal/limited/limitedfakes"
)

// toolboxArgs is how every toolbox call starts.
var toolboxArgs = []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--"}

// world is a fake host and cluster: a repository holding the release's
// rooket output and chart values, the commands' answers, and /proc.
type world struct {
	repo   string
	exec   *limitedfakes.FakeExecer
	launch *limitedfakes.FakeLauncher
	proc   fstest.MapFS
	stdout *bytes.Buffer

	mu sync.Mutex
	// avail is what each ceph df reports in turn, the last one from then on.
	avail []int64
	// left is what rados ls lists in a benchmark's namespace.
	left []string
	// failWith answers the commands whose line contains a key with its error.
	failWith map[string]error
}

func newWorld() *world {
	w := &world{
		repo:     GinkgoT().TempDir(),
		exec:     &limitedfakes.FakeExecer{},
		launch:   &limitedfakes.FakeLauncher{},
		stdout:   &bytes.Buffer{},
		avail:    []int64{9286684672},
		failWith: map[string]error{},
		proc: fstest.MapFS{
			"meminfo":              {Data: []byte("MemTotal:       65007132 kB\nMemFree:        12345678 kB\n")},
			"cpuinfo":              {Data: []byte("processor\t: 0\nmodel name\t: AMD Ryzen 9 7950X3D 16-Core Processor\n\nprocessor\t: 1\nmodel name\t: other\n")},
			"loadavg":              {Data: []byte("0.98 0.96 1.31 2/1203 98765\n")},
			"sys/kernel/osrelease": {Data: []byte("7.2.8-100.fc43.x86_64\n")},
		},
	}
	out := filepath.Join(w.repo, "hack", "rooket", "out", "squid")
	values := filepath.Join(w.repo, "hack", "rooket", "squid", "values")
	Expect(os.MkdirAll(out, 0o750)).To(Succeed())
	Expect(os.MkdirAll(values, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "ceph.conf"), []byte("[global]\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "image"), []byte("quay.io/ceph/ceph:v19.2.6\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(values, "rook-ceph-cluster.yaml"),
		[]byte("cephImage:\n  repository: quay.io/ceph/ceph\n  tag: v19.2.6\ncephBlockPools: []\n"), 0o600)).To(Succeed())
	w.exec.OutputStub = w.output
	w.launch.BenchStub = func(_ context.Context, p limited.Process) (int, error) {
		results, ok := strings.CutPrefix(p.Args[slices.IndexFunc(p.Args, func(a string) bool { return strings.HasPrefix(a, "-results=") })], "-results=")
		Expect(ok).To(BeTrue())
		Expect(os.WriteFile(results, []byte(`{"mode":"x"}`+"\n"), 0o600)).To(Succeed())
		fmt.Fprintln(p.Out, "BenchmarkSeam/shape=write4m/conc=64")
		return 4242 + w.launch.BenchCallCount(), nil
	}
	return w
}

// output answers a command as the host and the cluster would.
func (w *world) output(_ context.Context, c limited.Cmd) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := c.String()
	for key, err := range w.failWith {
		if strings.Contains(line, key) {
			return nil, err
		}
	}
	args := c.Args
	if c.Name == "rooket" && slices.Equal(args[:min(len(args), len(toolboxArgs))], toolboxArgs) {
		args = args[len(toolboxArgs):]
	}
	switch {
	case c.Name == "go" && args[0] == "version":
		return []byte("go version go1.27.1 linux/amd64\n"), nil
	case c.Name == "go":
		return nil, nil
	case c.Name == "ldd":
		return []byte("\tlinux-vdso.so.1 (0x00007ffd)\n\tlibrados.so.2 => /usr/lib64/librados.so.2 (0x00007f12)\n"), nil
	case c.Name == "rpm" && args[0] == "-qf":
		return []byte("19.2.6"), nil
	case c.Name == "rpm":
		return []byte("19.2.6-1.fc43"), nil
	case c.Name == "git":
		return []byte("af535e4-dirty\n"), nil
	case c.Name == "rooket" && args[0] == "version":
		return []byte("rooket v0.0.0-20260928053658-3d8551957e08\nbuilt by go1.27.1\n"), nil
	case args[0] == "ceph" && slices.Contains(args, "df"):
		avail := w.avail[0]
		if len(w.avail) > 1 {
			w.avail = w.avail[1:]
		}
		return fmt.Appendf(nil, `{"pools":[{"name":"rgw-go-test","stats":{"max_avail":%d}}]}`, avail), nil
	case args[0] == "ceph" && slices.Contains(args, "objecter_inflight_ops"):
		return []byte("1024\n"), nil
	case args[0] == "ceph" && slices.Contains(args, "objecter_inflight_op_bytes"):
		return []byte("104857600\n"), nil
	case args[0] == "rados" && slices.Contains(args, "--all"):
		return []byte("bench-floor-4096\tleft-behind\n"), nil
	case args[0] == "rados" && args[len(args)-1] == "ls":
		return []byte(strings.Join(w.left, "\n") + "\n"), nil
	case args[0] == "rados" && slices.Contains(args, "rm"):
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s", line)
}

// commands are the command lines run so far.
func (w *world) commands() []string {
	var lines []string
	for i := range w.exec.OutputCallCount() {
		_, c := w.exec.OutputArgsForCall(i)
		lines = append(lines, c.String())
	}
	return lines
}

func (w *world) runner(r *limited.Runner) {
	r.Exec, r.Launcher, r.Proc = w.exec, w.launch, w.proc
	r.Now = func() time.Time { return runStart }
	r.Getenv = func(string) string { return "" }
	r.Resolve = func(p string) (string, error) { return p + ".0.0", nil }
	r.Stdout = w.stdout
	r.Stderr = GinkgoWriter
	r.Handler = func(out io.Writer) slog.Handler { return slog.NewJSONHandler(out, nil) }
	r.Poll, r.AvailWait = time.Millisecond, time.Minute
	r.NoiseEvery, r.NoiseWindow = time.Hour, time.Millisecond
	r.Grace = 30 * time.Second
}

func (w *world) config() limited.Config {
	cfg, err := limited.Settings{Release: "squid", Limiter: "1", Repo: w.repo}.Check()
	Expect(err).NotTo(HaveOccurred())
	return cfg
}

func (w *world) run(ctx context.Context, cfg limited.Config) error {
	r := limited.NewRunner(io.Discard, io.Discard, nil)
	w.runner(r)
	return r.Run(ctx, cfg)
}

func (w *world) dir() string {
	return filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam-20261012T210509Z-limited")
}

var _ = Describe("Runner.Run", func() {
	var w *world

	BeforeEach(func() {
		w = newWorld()
	})

	It("builds the benchmark, records env.json and runs the cell in each mode under the limiter", func(ctx SpecContext) {
		cfg := w.config()
		Expect(w.run(ctx, cfg)).To(Succeed())

		bin := filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam.test")
		_, build := w.exec.OutputArgsForCall(0)
		Expect(build).To(Equal(limited.Cmd{Name: "go", Args: []string{
			"-C", w.repo, "test", "-c",
			"-tags=ceph_preview,integration", "-o", bin, "./test/bench/seam",
		}}))

		Expect(w.launch.BenchCallCount()).To(Equal(3))
		for i, mode := range []string{"sync", "callback", "pipe"} {
			_, p := w.launch.BenchArgsForCall(i)
			Expect(p.Path).To(Equal(bin), "mode %s", mode)
			Expect(p.Args).To(Equal(cfg.BenchArgs(mode, filepath.Join(w.dir(), "overbudget-"+mode+".jsonl"))), "mode %s", mode)
			Expect(p.Env).To(Equal([]string{"RGW_GO_TEST_CEPH_CONF=" + filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf")}), "mode %s", mode)
			Expect([]time.Duration{p.Limit, p.Grace}).To(Equal([]time.Duration{364 * time.Second, 30 * time.Second}), "mode %s", mode)
			Expect(filepath.Join(w.dir(), "overbudget-"+mode+".jsonl")).To(BeARegularFile())
			log, err := os.ReadFile(filepath.Join(w.dir(), "bench-"+mode+".log"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(log)).To(Equal("BenchmarkSeam/shape=write4m/conc=64\n"), "mode %s", mode)
		}
		Expect(w.stdout.String()).To(Equal(strings.Repeat("BenchmarkSeam/shape=write4m/conc=64\n", 3)))

		sweep, err := os.ReadFile(filepath.Join(w.dir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(sweep), `"msg":"bench done"`)).To(Equal(3))
		Expect(string(sweep)).To(ContainSubstring(`"msg":"objects left in the pool","pool":"rgw-go-test","objects":1`))
		noise, err := os.ReadFile(filepath.Join(w.dir(), "noise.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(noise)).To(HavePrefix("== 21:05:09 load 0.98 0.96 1.31\n"))
		Expect(filepath.Join(w.dir(), "REPORT.md")).NotTo(BeAnExistingFile())
	})

	It("records env.json with the run's host, cluster, librados and bound", func(ctx SpecContext) {
		Expect(w.run(ctx, w.config())).To(Succeed())
		data, err := os.ReadFile(filepath.Join(w.dir(), "env.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(fmt.Sprintf(`{
  "release": "squid",
  "ceph_version": "19.2.6",
  "cluster_image": "quay.io/ceph/ceph:v19.2.6",
  "librados_version": "19.2.6",
  "librados_path": "/usr/lib64/librados.so.2.0.0",
  "date": "2026-10-12T21:05:09Z",
  "rooket_version": "rooket v0.0.0-20260928053658-3d8551957e08",
  "nproc": %d,
  "mem_total_kib": 65007132,
  "cpu": "AMD Ryzen 9 7950X3D 16-Core Processor",
  "kernel": "7.2.8-100.fc43.x86_64",
  "go_version": "go version go1.27.1 linux/amd64",
  "host_librados": "19.2.6-1.fc43",
  "git_describe": "af535e4-dirty",
  "gomaxprocs": "unset",
  "over_objects": 640,
  "objecter_inflight_ops": "1024",
  "objecter_inflight_op_bytes": "104857600",
  "pool_max_avail": 9286684672,
  "load": "0.98 0.96 1.31",
  "limited": true,
  "inflight_bytes": 104857600
}
`, runtime.NumCPU())))
	})

	It("reaches the cluster through the toolbox, naming it with ROOKET_NAME", func(ctx SpecContext) {
		Expect(w.run(ctx, w.config())).To(Succeed())
		var df []limited.Cmd
		for i := range w.exec.OutputCallCount() {
			if _, c := w.exec.OutputArgsForCall(i); strings.Contains(c.String(), " df ") {
				df = append(df, c)
			}
		}
		Expect(df).To(HaveLen(4), "once for env.json and once before each process")
		Expect(df[0]).To(Equal(limited.Cmd{
			Name: "rooket",
			Args: append(slices.Clone(toolboxArgs), "ceph", "--connect-timeout=20", "--rados-mon-op-timeout=20", "df", "-f", "json"),
			Env:  []string{"ROOKET_NAME=rgw-go-squid"},
		}))
	})

	It("takes the rooket and Ceph versions it can from the environment, and falls back where a host has no rpm", func(ctx SpecContext) {
		w.failWith["rpm -q "] = errors.New("exit status 1")
		w.failWith["rpm -qf"] = errors.New("exit status 1")
		w.exec.OutputStub = func(ctx context.Context, c limited.Cmd) ([]byte, error) {
			switch c.String() {
			case "dpkg -S /usr/lib64/librados.so.2.0.0":
				return []byte("librados2:amd64: /usr/lib64/librados.so.2.0.0\n"), nil
			case "dpkg-query -W -f ${Version} librados2":
				return []byte("2:19.2.6-0ubuntu1"), nil
			}
			return w.output(ctx, c)
		}
		r := limited.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.Getenv = func(name string) string {
			return map[string]string{"ROOKET_VERSION": "rooket v1.2.3", "GOMAXPROCS": "8"}[name]
		}
		Expect(r.Run(ctx, w.config())).To(Succeed())
		data, err := os.ReadFile(filepath.Join(w.dir(), "env.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(SatisfyAll(
			ContainSubstring(`"librados_version": "19.2.6",`),
			ContainSubstring(`"host_librados": "2:19.2.6-0ubuntu1",`),
			ContainSubstring(`"rooket_version": "rooket v1.2.3",`),
			ContainSubstring(`"gomaxprocs": "8",`)))
		Expect(w.commands()).NotTo(ContainElement("rooket version"))
	})

	It("writes the directory of another bound under its own name", func(ctx SpecContext) {
		cfg := w.config()
		cfg.InflightBytes = 67108864
		Expect(w.run(ctx, cfg)).To(Succeed())
		Expect(w.dir() + "-67108864").To(BeADirectory())
		_, p := w.launch.BenchArgsForCall(0)
		Expect(p.Args).To(ContainElement("-inflight-bytes=67108864"))
	})

	It("waits for the pool to report the free space before each process", func(ctx SpecContext) {
		w.avail = []int64{9286684672, 1 << 30, 2 << 30, 9286684672}
		Expect(w.run(ctx, w.config())).To(Succeed())
		Expect(w.launch.BenchCallCount()).To(Equal(3))
		var dfs int
		for _, line := range w.commands() {
			if strings.Contains(line, " df ") {
				dfs++
			}
		}
		Expect(dfs).To(Equal(6), "env.json, three reads before sync, then one before callback and one before pipe")
	})

	It("fails, naming the mode, when the pool lacks the space past the wait", func(ctx SpecContext) {
		w.avail = []int64{9286684672, 1 << 30}
		r := limited.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		start := time.Now()
		r.Now = func() time.Time { return start.Add(time.Duration(w.exec.OutputCallCount()) * time.Minute) }
		r.AvailWait = 2 * time.Minute
		dir := filepath.Join(w.repo, "hack", "bench", "out", "squid")
		Expect(r.Run(ctx, w.config())).To(MatchError(
			"not enough space for the over-budget cell in sync mode: the pool has 1073741824 bytes available, under 5 GiB"))
		Expect(w.launch.BenchCallCount()).To(BeZero())
		Expect(dir).To(BeADirectory())
	})

	It("removes what a stopped process left in its namespace, then fails naming its mode", func(ctx SpecContext) {
		w.left = make([]string, 1500)
		for i := range w.left {
			w.left[i] = fmt.Sprintf("seam-w%d-%d", i%64, i)
		}
		w.launch.BenchReturnsOnCall(1, 4243, fmt.Errorf("%w of 6m4s", limited.ErrStopped))
		err := w.run(ctx, w.config())
		Expect(err).To(MatchError(limited.ErrStopped))
		Expect(err).To(MatchError("bench callback: stopped past its limit of 6m4s"))
		Expect(w.launch.BenchCallCount()).To(Equal(2), "pipe does not run after callback failed")

		prefix := strings.Join(append(append([]string{"rooket"}, toolboxArgs...),
			"rados", "--rados-osd-op-timeout=120", "--rados-mon-op-timeout=120", "-p", "rgw-go-test", "-N", "bench-4243"), " ")
		var listed, removed []int
		for _, line := range w.commands() {
			switch {
			case line == prefix+" ls":
				listed = append(listed, 1)
			case strings.HasPrefix(line, prefix+" rm "):
				removed = append(removed, len(strings.Fields(strings.TrimPrefix(line, prefix+" rm "))))
			}
		}
		Expect(listed).To(HaveLen(1))
		Expect(removed).To(Equal([]int{1024, 476}), "1024 names a rados rm")

		sweep, err := os.ReadFile(filepath.Join(w.dir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(`"level":"ERROR","msg":"bench stopped past its limit","mode":"callback","limit_s":364`))
		Expect(string(sweep)).To(ContainSubstring(`"level":"ERROR","msg":"the limiter re-run failed","error":"bench callback: stopped past its limit of 6m4s"`))
	})

	It("removes nothing for a process that never started", func(ctx SpecContext) {
		w.launch.BenchReturnsOnCall(0, 0, errors.New("starting seam.test: no such file"))
		Expect(w.run(ctx, w.config())).To(MatchError("bench sync: starting seam.test: no such file"))
		for _, line := range w.commands() {
			Expect(line).NotTo(ContainSubstring("bench-"), "no namespace to remove")
		}
	})

	It("reports a failed removal beside the process's failure", func(ctx SpecContext) {
		w.left = []string{"seam-w0-0"}
		w.failWith[" rm "] = errors.New("rados: permission denied")
		w.launch.BenchReturnsOnCall(0, 4242, errors.New("exit status 2"))
		err := w.run(ctx, w.config())
		Expect(err).To(MatchError(ContainSubstring("bench sync: exit status 2")))
		Expect(err).To(MatchError(ContainSubstring("removing the objects left in bench-4242: rados: permission denied")))
		sweep, err := os.ReadFile(filepath.Join(w.dir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(`"msg":"bench failed","mode":"sync","error":"exit status 2"`))
		Expect(string(sweep)).To(ContainSubstring(`"msg":"the limiter re-run failed","error":"bench sync: exit status 2\nremoving the objects left in bench-4242: rados: permission denied"`))
	})

	It("records why a run failed in sweep.log when the pool lacks the space", func(ctx SpecContext) {
		w.avail = []int64{9286684672, 1 << 30}
		r := limited.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.AvailWait = 0
		Expect(r.Run(ctx, w.config())).To(MatchError(ContainSubstring("not enough space for the over-budget cell in sync mode")))
		sweep, err := os.ReadFile(filepath.Join(w.dir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(`"msg":"the limiter re-run failed","error":"not enough space for the over-budget cell in sync mode`))
	})

	It("gives every command a deadline of its own", func(ctx SpecContext) {
		Expect(w.run(ctx, w.config())).To(Succeed())
		kinds := map[string]int{}
		var unbounded []string
		for i := range w.exec.OutputCallCount() {
			callCtx, c := w.exec.OutputArgsForCall(i)
			kind, bound := "local", time.Minute
			switch {
			case c.Name == "rooket" && c.Args[0] == "kubectl":
				kind, bound = "toolbox", 3*time.Minute
			case c.Name == "go" && slices.Contains(c.Args, "-c"):
				kind, bound = "build", 15*time.Minute
			}
			kinds[kind]++
			if deadline, ok := callCtx.Deadline(); !ok || time.Until(deadline) > bound {
				unbounded = append(unbounded, c.String())
			}
		}
		Expect(kinds).To(Equal(map[string]int{
			"toolbox": 7, // env.json's df and two config gets, a df before each process, and the final count
			"build":   1,
			"local":   6, // ldd, rpm -qf, rooket version, go version, rpm -q and git describe
		}))
		Expect(unbounded).To(BeEmpty(), "commands with no deadline, or one past their bound")
	})

	DescribeTable("fails a hung local command at its deadline",
		func(ctx SpecContext, name string, set func(*limited.Runner), want string) {
			r := limited.NewRunner(io.Discard, io.Discard, nil)
			w.runner(r)
			set(r)
			host := limited.NewExecer(time.Second)
			r.Exec = &limitedfakes.FakeExecer{OutputStub: func(ctx context.Context, c limited.Cmd) ([]byte, error) {
				if c.Name == name {
					// A shell that forks a child holding its output, as a
					// wedged tool would.
					return host.Output(ctx, limited.Cmd{Name: "/bin/sh", Args: []string{"-c", "sleep 30; true"}})
				}
				return w.output(ctx, c)
			}}
			start := time.Now()
			err := r.Run(ctx, w.config())
			elapsed := time.Since(start)
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(elapsed).To(BeNumerically("<", 5*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
			Expect(w.launch.BenchCallCount()).To(BeZero())
			GinkgoWriter.Printf("a hung %s returned after %s\n", name, elapsed)
		},
		Entry("ldd, at LocalTimeout", "ldd", func(r *limited.Runner) { r.LocalTimeout = 300 * time.Millisecond },
			"listing the libraries"),
		Entry("the build, at BuildTimeout", "go", func(r *limited.Runner) { r.BuildTimeout = 300 * time.Millisecond },
			"building "),
	)

	Context("with the real execer and a toolbox that hangs", func() {
		var rooket string

		BeforeEach(func() {
			// The fake rooket answers ceph df and config get, and hangs where
			// LIMITED_HANG says, as a shell that forks a child sharing its
			// output, the way rooket forks kubectl.
			rooket = filepath.Join(w.repo, "rooket")
			Expect(os.WriteFile(rooket, []byte(`#!/bin/sh
case "$*" in
*" $LIMITED_HANG "*) sleep 30; true ;;
*" df "*) echo '{"pools":[{"name":"rgw-go-test","stats":{"max_avail":9286684672}}]}' ;;
*" config get "*) echo 1024 ;;
esac
`), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
		})

		// run runs against the fake rooket through the real execer, and every
		// other command through the fakes.
		run := func(ctx context.Context, adjust func(*limited.Runner)) (time.Duration, error) {
			host := limited.NewExecer(time.Second)
			cfg := w.config()
			cfg.Rooket = rooket
			r := limited.NewRunner(io.Discard, io.Discard, nil)
			w.runner(r)
			r.Exec = &limitedfakes.FakeExecer{OutputStub: func(ctx context.Context, c limited.Cmd) ([]byte, error) {
				if c.Name == rooket {
					return host.Output(ctx, c)
				}
				return w.output(ctx, c)
			}}
			adjust(r)
			start := time.Now()
			err := r.Run(ctx, cfg)
			return time.Since(start), err
		}

		It("ends the purge within its bound, kubectl's stand-in killed with rooket's", func(ctx SpecContext) {
			GinkgoT().Setenv("LIMITED_HANG", "rados")
			w.launch.BenchReturnsOnCall(0, 4242, fmt.Errorf("%w of 6m4s", limited.ErrStopped))
			elapsed, err := run(ctx, func(r *limited.Runner) { r.CallTimeout, r.PurgeWait = time.Minute, 500*time.Millisecond })
			Expect(err).To(MatchError(ContainSubstring("bench sync: stopped past its limit of 6m4s")))
			Expect(err).To(MatchError(ContainSubstring("listing the objects in rgw-go-test")))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(elapsed).To(BeNumerically("<", 5*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
			GinkgoWriter.Printf("a 500ms purge bound returned after %s\n", elapsed)
		})

		It("fails a toolbox call that outlives its own timeout", func(ctx SpecContext) {
			GinkgoT().Setenv("LIMITED_HANG", "df")
			elapsed, err := run(ctx, func(r *limited.Runner) { r.CallTimeout = 300 * time.Millisecond })
			Expect(err).To(MatchError(ContainSubstring("reading ceph df")))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(elapsed).To(BeNumerically("<", 5*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
			Expect(w.launch.BenchCallCount()).To(BeZero())
			GinkgoWriter.Printf("a 300ms call timeout returned after %s\n", elapsed)
		})

		It("bounds each space-wait poll by what the wait has left", func(ctx SpecContext) {
			GinkgoT().Setenv("LIMITED_HANG", "rados")
			// The polls run through the real execer; only the rados count at
			// the end would hang, and the wait fails first.
			calls := 0
			elapsed, err := run(ctx, func(r *limited.Runner) {
				r.CallTimeout, r.AvailWait, r.Poll = time.Minute, 2*time.Second, 10*time.Millisecond
				// The wait's deadline runs on the clock, which must move.
				r.Now = time.Now
				inner := r.Exec
				r.Exec = &limitedfakes.FakeExecer{OutputStub: func(ctx context.Context, c limited.Cmd) ([]byte, error) {
					if c.Name == rooket && strings.Contains(c.String(), " df ") {
						calls++
						if calls > 1 {
							// From the first wait on, df hangs as a stuck mon would.
							return limited.NewExecer(time.Second).Output(ctx, limited.Cmd{Name: "/bin/sh", Args: []string{"-c", "sleep 30; true"}})
						}
					}
					return inner.Output(ctx, c)
				}}
			})
			Expect(err).To(MatchError(ContainSubstring("not enough space for the over-budget cell in sync mode")))
			Expect(elapsed).To(BeNumerically("<", 6*time.Second), "returned after %s: the 2 s wait bounds its polls, not the one-minute call timeout", elapsed)
			GinkgoWriter.Printf("a 2s space wait over a hung df returned after %s\n", elapsed)
		})
	})

	It("fails before it creates the run directory when the cluster is not up", func(ctx SpecContext) {
		Expect(os.Remove(filepath.Join(w.repo, "hack", "rooket", "out", "squid", "image"))).To(Succeed())
		out := filepath.Join(w.repo, "hack", "rooket", "out", "squid")
		Expect(w.run(ctx, w.config())).To(MatchError(fmt.Sprintf("no %s or %s: run make cluster-up RELEASE=squid",
			filepath.Join(out, "ceph.conf"), filepath.Join(out, "image"))))
		Expect(filepath.Join(w.repo, "hack", "bench")).NotTo(BeAnExistingFile())
		Expect(w.exec.OutputCallCount()).To(BeZero())
	})

	It("runs no process when the build fails", func(ctx SpecContext) {
		w.failWith["test -c"] = errors.New("exit status 1: cannot find package")
		Expect(w.run(ctx, w.config())).To(MatchError(ContainSubstring("building " +
			filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam.test") + ": exit status 1: cannot find package")))
		Expect(w.launch.BenchCallCount()).To(BeZero())
	})

	It("refuses a librados it cannot place in a Ceph release", func(ctx SpecContext) {
		w.exec.OutputStub = func(ctx context.Context, c limited.Cmd) ([]byte, error) {
			if c.Name == "rpm" && c.Args[0] == "-qf" {
				return []byte("19.2.6~rc1"), nil
			}
			return w.output(ctx, c)
		}
		Expect(w.run(ctx, w.config())).To(MatchError(ContainSubstring(`, /usr/lib64/librados.so.2.0.0, is version "19.2.6~rc1", not X.Y.Z`)))
		Expect(w.launch.BenchCallCount()).To(BeZero())
	})

	It("refuses a binary that links no librados", func(ctx SpecContext) {
		w.exec.OutputStub = func(ctx context.Context, c limited.Cmd) ([]byte, error) {
			if c.Name == "ldd" {
				return []byte("\tlibc.so.6 => /lib64/libc.so.6 (0x00007f)\n"), nil
			}
			return w.output(ctx, c)
		}
		Expect(w.run(ctx, w.config())).To(MatchError(ContainSubstring("links no librados.so.2")))
	})

	It("refuses chart values whose Ceph tag is no version", func(ctx SpecContext) {
		Expect(os.WriteFile(filepath.Join(w.repo, "hack", "rooket", "squid", "values", "rook-ceph-cluster.yaml"),
			[]byte("cephImage:\n  tag: latest\n"), 0o600)).To(Succeed())
		Expect(w.run(ctx, w.config())).To(MatchError(ContainSubstring(`cephImage.tag "latest" is not a vX.Y.Z version`)))
	})
})
