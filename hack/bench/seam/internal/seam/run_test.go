package seam_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gleak"

	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness/harnessfakes"
	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
)

var _ = Describe("Runner.Run of the limiter re-run", func() {
	var w *world

	BeforeEach(func() {
		w = newWorld()
	})

	It("builds the benchmark, records env.json and runs the cell in each mode under the limiter", func(ctx SpecContext) {
		cfg := w.limiterConfig()
		Expect(w.run(ctx, cfg)).To(Succeed())

		bin := filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam.test")
		_, build := w.exec.OutputArgsForCall(0)
		Expect(build).To(Equal(harness.Cmd{Name: "go", Args: []string{
			"-C", w.repo, "test", "-c",
			"-tags=ceph_preview,integration", "-o", bin, "./test/bench/seam",
		}}))

		Expect(w.launch.BenchCallCount()).To(Equal(3))
		for i, mode := range []string{"sync", "callback", "pipe"} {
			_, p := w.launch.BenchArgsForCall(i)
			Expect(p.Path).To(Equal(bin), "mode %s", mode)
			Expect(p.Args).To(Equal(cfg.BenchArgs(mode, filepath.Join(w.limiterDir(), "overbudget-"+mode+".jsonl"), cfg.CellArgs())), "mode %s", mode)
			Expect(p.Env).To(Equal([]string{"RGW_GO_TEST_CEPH_CONF=" + filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf")}), "mode %s", mode)
			Expect([]time.Duration{p.Limit, p.Grace}).To(Equal([]time.Duration{364 * time.Second, 30 * time.Second}), "mode %s", mode)
			Expect(filepath.Join(w.limiterDir(), "overbudget-"+mode+".jsonl")).To(BeARegularFile())
			log, err := os.ReadFile(filepath.Join(w.limiterDir(), "bench-"+mode+".log"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(log)).To(Equal("BenchmarkSeam/shape=write4m/conc=64\n"), "mode %s", mode)
		}
		Expect(w.stdout.String()).To(Equal(strings.Repeat("BenchmarkSeam/shape=write4m/conc=64\n", 3)))

		sweep, err := os.ReadFile(filepath.Join(w.limiterDir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(sweep), `"msg":"bench done"`)).To(Equal(3))
		Expect(string(sweep)).To(ContainSubstring(`"msg":"objects left in the pool","pool":"rgw-go-test","objects":1`))
		noise, err := os.ReadFile(filepath.Join(w.limiterDir(), "noise.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(noise)).To(HavePrefix("== 21:05:09 load 0.98 0.96 1.31\n"))
		Expect(filepath.Join(w.limiterDir(), "REPORT.md")).NotTo(BeAnExistingFile())
	})

	It("records env.json with the run's host, cluster, librados and bound", func(ctx SpecContext) {
		Expect(w.run(ctx, w.limiterConfig())).To(Succeed())
		data, err := os.ReadFile(filepath.Join(w.limiterDir(), "env.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(fmt.Sprintf(`{
  "release": "squid",
  "ceph_version": "19.2.6",
  "cluster_image": "quay.io/ceph/ceph:v20.2.4",
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
  "inflight_bytes": 104857600,
  "repo": "%s",
  "rooket": "rooket",
  "go_tags": "ceph_preview"
}
`, runtime.NumCPU(), w.repo)))
	})

	It("reaches the cluster through the toolbox, naming it with ROOKET_NAME", func(ctx SpecContext) {
		Expect(w.run(ctx, w.limiterConfig())).To(Succeed())
		var df []harness.Cmd
		for i := range w.exec.OutputCallCount() {
			if _, c := w.exec.OutputArgsForCall(i); strings.Contains(c.String(), " df ") {
				df = append(df, c)
			}
		}
		Expect(df).To(HaveLen(4), "once for env.json and once before each process")
		Expect(df[0]).To(Equal(harness.Cmd{
			Name: "rooket",
			Args: append(slices.Clone(toolboxArgs), "ceph", "--connect-timeout=20", "--rados-mon-op-timeout=20", "df", "-f", "json"),
			Env:  []string{"ROOKET_NAME=rgw-go-squid"},
		}))
	})

	It("takes the rooket and Ceph versions it can from the environment, and falls back where a host has no rpm", func(ctx SpecContext) {
		w.failWith["rpm -q "] = errors.New("exit status 1")
		w.failWith["rpm -qf"] = errors.New("exit status 1")
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			switch c.String() {
			case "dpkg -S /usr/lib64/librados.so.2.0.0":
				return []byte("librados2:amd64: /usr/lib64/librados.so.2.0.0\n"), nil
			case "dpkg-query -W -f ${Version} librados2":
				return []byte("2:19.2.6-0ubuntu1"), nil
			}
			return w.output(ctx, c)
		}
		r := seam.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.Getenv = func(name string) string {
			return map[string]string{"ROOKET_VERSION": "rooket v1.2.3", "GOMAXPROCS": "8"}[name]
		}
		Expect(r.Run(ctx, w.limiterConfig())).To(Succeed())
		data, err := os.ReadFile(filepath.Join(w.limiterDir(), "env.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(SatisfyAll(
			ContainSubstring(`"librados_version": "19.2.6",`),
			ContainSubstring(`"host_librados": "2:19.2.6-0ubuntu1",`),
			ContainSubstring(`"rooket_version": "rooket v1.2.3",`),
			ContainSubstring(`"gomaxprocs": "8",`)))
		Expect(w.commands()).NotTo(ContainElement("rooket version"))
	})

	It("writes the directory of another bound under its own name", func(ctx SpecContext) {
		cfg := w.limiterConfig()
		cfg.InflightBytes = 67108864
		Expect(w.run(ctx, cfg)).To(Succeed())
		Expect(w.limiterDir() + "-67108864").To(BeADirectory())
		_, p := w.launch.BenchArgsForCall(0)
		Expect(p.Args).To(ContainElement("-inflight-bytes=67108864"))
	})

	It("waits for the pool to report the free space before each process", func(ctx SpecContext) {
		w.avail = []int64{9286684672, 1 << 30, 2 << 30, 9286684672}
		Expect(w.run(ctx, w.limiterConfig())).To(Succeed())
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
		r := seam.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		start := time.Now()
		r.Now = func() time.Time { return start.Add(time.Duration(w.exec.OutputCallCount()) * time.Minute) }
		r.AvailWait = 2 * time.Minute
		dir := filepath.Join(w.repo, "hack", "bench", "out", "squid")
		Expect(r.Run(ctx, w.limiterConfig())).To(MatchError(
			"not enough space for the over-budget cell in sync mode: the pool has 1073741824 bytes available, under 5 GiB"))
		Expect(w.launch.BenchCallCount()).To(BeZero())
		Expect(dir).To(BeADirectory())
	})

	It("removes what a stopped process left in its namespace, then fails naming its mode", func(ctx SpecContext) {
		left := make([]string, 1500)
		for i := range left {
			left[i] = fmt.Sprintf("seam-w%d-%d", i%64, i)
		}
		w.left["bench-4243"] = left
		w.launch.BenchReturnsOnCall(1, 4243, fmt.Errorf("%w of 6m4s", seam.ErrStopped))
		err := w.run(ctx, w.limiterConfig())
		Expect(err).To(MatchError(seam.ErrStopped))
		Expect(err).To(MatchError("bench callback: stopped past its limit of 6m4s"))
		Expect(w.launch.BenchCallCount()).To(Equal(2), "pipe does not run after callback failed")

		list := strings.Join(append(append([]string{"rooket"}, toolboxArgs...),
			"rados", "--rados-osd-op-timeout=120", "--rados-mon-op-timeout=120", "-p", "rgw-go-test", "-N", "bench-4243", "ls"), " ")
		var listed int
		var removals []harness.Cmd
		for i := range w.exec.OutputCallCount() {
			_, c := w.exec.OutputArgsForCall(i)
			switch {
			case c.String() == list:
				listed++
			case slices.Contains(c.Args, "xargs"):
				removals = append(removals, c)
			}
		}
		Expect(listed).To(Equal(1))
		Expect(removals).To(Equal([]harness.Cmd{{
			Name:  "rooket",
			Args:  purgeArgs("bench-4243"),
			Env:   []string{"ROOKET_NAME=rgw-go-squid"},
			Stdin: []byte(strings.Join(left, "\n") + "\n"),
		}}), "one toolbox session, its xargs removing the listed names eight rados rm at a time, 1024 names each")

		sweep, err := os.ReadFile(filepath.Join(w.limiterDir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(`"level":"ERROR","msg":"bench stopped past its limit","mode":"callback","limit_s":364`))
		Expect(string(sweep)).To(ContainSubstring(`"level":"ERROR","msg":"the limiter re-run failed","error":"bench callback: stopped past its limit of 6m4s"`))
	})

	It("removes nothing for a process that never started", func(ctx SpecContext) {
		w.launch.BenchReturnsOnCall(0, 0, errors.New("starting seam.test: no such file"))
		Expect(w.run(ctx, w.limiterConfig())).To(MatchError("bench sync: starting seam.test: no such file"))
		for _, line := range w.commands() {
			Expect(line).NotTo(ContainSubstring("bench-"), "no namespace to remove")
		}
	})

	It("reports a failed removal beside the process's failure", func(ctx SpecContext) {
		w.left["bench-4242"] = []string{"seam-w0-0"}
		w.failWith["xargs"] = errors.New("exit status 123: rados: permission denied")
		w.launch.BenchReturnsOnCall(0, 4242, errors.New("exit status 2"))
		err := w.run(ctx, w.limiterConfig())
		Expect(err).To(MatchError(ContainSubstring("bench sync: exit status 2")))
		Expect(err).To(MatchError(ContainSubstring("removing the objects left in bench-4242: exit status 123: rados: permission denied")))
		sweep, err := os.ReadFile(filepath.Join(w.limiterDir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(`"msg":"bench failed","mode":"sync","error":"exit status 2"`))
		Expect(string(sweep)).To(ContainSubstring(`"msg":"the limiter re-run failed","error":"bench sync: exit status 2\nremoving the objects left in bench-4242: exit status 123: rados: permission denied"`))
	})

	It("records why a run failed in sweep.log when the pool lacks the space", func(ctx SpecContext) {
		w.avail = []int64{9286684672, 1 << 30}
		r := seam.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.AvailWait = 0
		Expect(r.Run(ctx, w.limiterConfig())).To(MatchError(ContainSubstring("not enough space for the over-budget cell in sync mode")))
		sweep, err := os.ReadFile(filepath.Join(w.limiterDir(), "sweep.log"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(sweep)).To(ContainSubstring(`"msg":"the limiter re-run failed","error":"not enough space for the over-budget cell in sync mode`))
	})

	It("gives every command a deadline of its own", func(ctx SpecContext) {
		Expect(w.run(ctx, w.limiterConfig())).To(Succeed())
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
		func(ctx SpecContext, name string, set func(*seam.Runner), want string) {
			r := seam.NewRunner(io.Discard, io.Discard, nil)
			w.runner(r)
			set(r)
			host := harness.NewExecer(time.Second)
			r.Exec = &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				if c.Name == name {
					// A shell that forks a child holding its output, as a
					// wedged tool would.
					return host.Output(ctx, harness.Cmd{Name: "/bin/sh", Args: []string{"-c", "sleep 30; true"}})
				}
				return w.output(ctx, c)
			}}
			start := time.Now()
			err := r.Run(ctx, w.limiterConfig())
			elapsed := time.Since(start)
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(elapsed).To(BeNumerically("<", 5*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
			Expect(w.launch.BenchCallCount()).To(BeZero())
			GinkgoWriter.Printf("a hung %s returned after %s\n", name, elapsed)
		},
		Entry("ldd, at LocalTimeout", "ldd", func(r *seam.Runner) { r.LocalTimeout = 300 * time.Millisecond },
			"listing the libraries"),
		Entry("the build, at BuildTimeout", "go", func(r *seam.Runner) { r.BuildTimeout = 300 * time.Millisecond },
			"building "),
	)

	Context("with the real execer and a toolbox that hangs", func() {
		var rooket string

		BeforeEach(func() {
			// The fake rooket answers ceph df and config get, and hangs where
			// SEAM_HANG says, as a shell that forks a child sharing its
			// output, the way rooket forks kubectl.
			rooket = filepath.Join(w.repo, "rooket")
			Expect(os.WriteFile(rooket, []byte(`#!/bin/sh
case "$*" in
*" $SEAM_HANG "*) sleep 30; true ;;
*" df "*) echo '{"pools":[{"name":"rgw-go-test","stats":{"max_avail":9286684672}}]}' ;;
*" config get "*) echo 1024 ;;
esac
`), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
		})

		// run runs against the fake rooket through the real execer, and every
		// other command through the fakes.
		run := func(ctx context.Context, adjust func(*seam.Runner)) (time.Duration, error) {
			host := harness.NewExecer(time.Second)
			cfg := w.limiterConfig()
			cfg.Rooket = rooket
			r := seam.NewRunner(io.Discard, io.Discard, nil)
			w.runner(r)
			r.Exec = &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
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
			GinkgoT().Setenv("SEAM_HANG", "rados")
			w.launch.BenchReturnsOnCall(0, 4242, fmt.Errorf("%w of 6m4s", seam.ErrStopped))
			elapsed, err := run(ctx, func(r *seam.Runner) { r.CallTimeout, r.PurgeWait = time.Minute, 500*time.Millisecond })
			Expect(err).To(MatchError(ContainSubstring("bench sync: stopped past its limit of 6m4s")))
			Expect(err).To(MatchError(ContainSubstring("listing the objects in bench-4242 of rgw-go-test")))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(elapsed).To(BeNumerically("<", 5*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
			GinkgoWriter.Printf("a 500ms purge bound returned after %s\n", elapsed)
		})

		It("ends a removal whose toolbox session hangs at the purge's deadline, its group killed, its unread stdin no hold on the call", func(ctx SpecContext) {
			dir := GinkgoT().TempDir()
			// Far more names than a pipe buffers, so that the goroutine feeding
			// them is blocked writing when the deadline comes.
			var names strings.Builder
			for i := range 100000 {
				fmt.Fprintf(&names, "seam-w%d-%d\n", i%64, i)
			}
			Expect(os.WriteFile(filepath.Join(dir, "names"), []byte(names.String()), 0o600)).To(Succeed())
			child := filepath.Join(dir, "child")
			GinkgoT().Setenv("SEAM_NAMES", filepath.Join(dir, "names"))
			GinkgoT().Setenv("SEAM_CHILD", child)
			// The listing answers; the removal's session reads none of its
			// stdin and hangs, with a child that holds the stdin pipe open.
			Expect(os.WriteFile(rooket, []byte(`#!/bin/sh
case "$*" in
*" df "*) echo '{"pools":[{"name":"rgw-go-test","stats":{"max_avail":9286684672}}]}' ;;
*" config get "*) echo 1024 ;;
*" ls") cat "$SEAM_NAMES" ;;
*" xargs "*)
	exec 3<&0
	sleep 30 <&3 &
	echo $! >"$SEAM_CHILD"
	wait
	true ;;
esac
`), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
			w.launch.BenchReturnsOnCall(0, 4242, fmt.Errorf("%w of 6m4s", seam.ErrStopped))
			before := gleak.Goroutines()
			elapsed, err := run(ctx, func(r *seam.Runner) {
				r.CallTimeout, r.PurgeWait = time.Minute, time.Second
				// A wait delay well past the bound, so that the call ends in time
				// only when the group's kill closes every end of its pipes.
				host := harness.NewExecer(30 * time.Second)
				r.Exec = &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
					if c.Name == rooket {
						return host.Output(ctx, c)
					}
					return w.output(ctx, c)
				}}
			})
			Expect(err).To(MatchError(ContainSubstring("bench sync: stopped past its limit of 6m4s")))
			Expect(err).To(MatchError(ContainSubstring("removing the objects left in bench-4242: " + rooket + " kubectl -n rook-ceph exec -i")))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(err).NotTo(MatchError(ContainSubstring("listing the objects")), "the listing answered")
			Expect(elapsed).To(BeNumerically("<", 5*time.Second),
				"returned after %s, not after the hang's 30 s or the 30 s wait delay", elapsed)
			pid := childPid(child)
			Eventually(func() string { return processState(pid) }).WithTimeout(5*time.Second).WithPolling(20*time.Millisecond).
				Should(Or(Equal("gone"), Equal("Z")), "the session's child %d died with its group", pid)
			Eventually(gleak.Goroutines).WithTimeout(5*time.Second).WithPolling(20*time.Millisecond).
				ShouldNot(gleak.HaveLeaked(before), "no goroutine of the call, the one feeding its stdin included, outlives it")
			GinkgoWriter.Printf("a 1s purge bound over a hung removal returned after %s\n", elapsed)
		})

		It("fails a toolbox call that outlives its own timeout", func(ctx SpecContext) {
			GinkgoT().Setenv("SEAM_HANG", "df")
			elapsed, err := run(ctx, func(r *seam.Runner) { r.CallTimeout = 300 * time.Millisecond })
			Expect(err).To(MatchError(ContainSubstring("reading ceph df")))
			Expect(err).To(MatchError(ContainSubstring("signal: killed")))
			Expect(elapsed).To(BeNumerically("<", 5*time.Second), "returned after %s, not after the hang's 30 s", elapsed)
			Expect(w.launch.BenchCallCount()).To(BeZero())
			GinkgoWriter.Printf("a 300ms call timeout returned after %s\n", elapsed)
		})

		It("bounds each space-wait poll by what the wait has left", func(ctx SpecContext) {
			GinkgoT().Setenv("SEAM_HANG", "rados")
			// The polls run through the real execer; only the rados count at
			// the end would hang, and the wait fails first.
			calls := 0
			elapsed, err := run(ctx, func(r *seam.Runner) {
				r.CallTimeout, r.AvailWait, r.Poll = time.Minute, 2*time.Second, 10*time.Millisecond
				// The wait's deadline runs on the clock, which must move.
				r.Now = time.Now
				inner := r.Exec
				r.Exec = &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
					if c.Name == rooket && strings.Contains(c.String(), " df ") {
						calls++
						if calls > 1 {
							// From the first wait on, df hangs as a stuck mon would.
							return harness.NewExecer(time.Second).Output(ctx, harness.Cmd{Name: "/bin/sh", Args: []string{"-c", "sleep 30; true"}})
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

	Context("with the real execer, and the host's xargs and a fake rados in the toolbox", func() {
		var rooket, removed string

		BeforeEach(func() {
			dir := GinkgoT().TempDir()
			// The fake rooket answers its version and ceph itself and runs
			// anything else here, with its stdin, as kubectl exec -i runs it in
			// the toolbox.
			rooket = filepath.Join(dir, "rooket")
			Expect(os.WriteFile(rooket, []byte(`#!/bin/sh
[ "$1" = kubectl ] || { echo rooket v0.0.0; exit 0; }
while [ "$1" != "--" ]; do shift; done
shift
case "$*" in
ceph*" df "*) echo '{"pools":[{"name":"rgw-go-test","stats":{"max_avail":9286684672}}]}' ;;
ceph*) echo 1024 ;;
*) exec "$@" ;;
esac
`), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
			// The fake rados lists SEAM_NAMES, and records each rm's count and
			// first name, failing the whole first batch at seam-500 as rados rm
			// fails at the first object it cannot remove.
			bin := filepath.Join(dir, "bin")
			Expect(os.MkdirAll(bin, 0o750)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(bin, "rados"), []byte(`#!/bin/sh
case "$*" in
*" ls") cat "$SEAM_NAMES" ;;
*" rm "*)
	shift 5
	echo "$# $1" >>"$SEAM_RM"
	for name in "$@"; do [ "$name" = seam-500 ] && exit 1; done
	exit 0 ;;
esac
`), 0o700)).To(Succeed()) //nolint:gosec // the fixture is a script the spec runs
			// Ten batches, more than xargs runs at once, so that some start
			// only after the first has failed.
			var names strings.Builder
			for i := range 10000 {
				fmt.Fprintf(&names, "seam-%d\n", i)
			}
			Expect(os.WriteFile(filepath.Join(dir, "names"), []byte(names.String()), 0o600)).To(Succeed())
			removed = filepath.Join(dir, "removed")
			GinkgoT().Setenv("SEAM_NAMES", filepath.Join(dir, "names"))
			GinkgoT().Setenv("SEAM_RM", removed)
			GinkgoT().Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		})

		It("removes every batch but the failed one, and reports the failure once the session ends", func(ctx SpecContext) {
			host := harness.NewExecer(time.Second)
			cfg := w.limiterConfig()
			cfg.Rooket = rooket
			r := seam.NewRunner(io.Discard, io.Discard, nil)
			w.runner(r)
			sessions := 0
			r.Exec = &harnessfakes.FakeExecer{OutputStub: func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				if c.Name != rooket {
					return w.output(ctx, c)
				}
				if slices.Contains(c.Args, "xargs") {
					sessions++
				}
				return host.Output(ctx, c)
			}}
			w.launch.BenchReturnsOnCall(0, 4242, fmt.Errorf("%w of 6m4s", seam.ErrStopped))
			err := r.Run(ctx, cfg)
			Expect(err).To(MatchError(ContainSubstring("bench sync: stopped past its limit of 6m4s")))
			Expect(err).To(MatchError(ContainSubstring("removing the objects left in bench-4242: ")))
			Expect(err).To(MatchError(ContainSubstring("exit status 123")), "xargs's status when an invocation failed")
			Expect(sessions).To(Equal(1))
			data, rerr := os.ReadFile(removed)
			Expect(rerr).NotTo(HaveOccurred())
			var batches []string
			for b := range 10 {
				batches = append(batches, fmt.Sprintf("%d seam-%d", min(1024, 10000-b*1024), b*1024))
			}
			Expect(strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")).To(ConsistOf(batches),
				"every batch ran, eight at a time, though the first failed")
		})
	})

	It("fails before it creates the run directory when the cluster is not up", func(ctx SpecContext) {
		Expect(os.Remove(filepath.Join(w.repo, "hack", "rooket", "out", "squid", "image"))).To(Succeed())
		out := filepath.Join(w.repo, "hack", "rooket", "out", "squid")
		Expect(w.run(ctx, w.limiterConfig())).To(MatchError(fmt.Sprintf("no %s or %s: run make cluster-up RELEASE=squid",
			filepath.Join(out, "ceph.conf"), filepath.Join(out, "image"))))
		Expect(filepath.Join(w.repo, "hack", "bench")).NotTo(BeAnExistingFile())
		Expect(w.exec.OutputCallCount()).To(BeZero())
	})

	It("runs no process when the build fails", func(ctx SpecContext) {
		w.failWith["test -c"] = errors.New("exit status 1: cannot find package")
		Expect(w.run(ctx, w.limiterConfig())).To(MatchError(ContainSubstring("building " +
			filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam.test") + ": exit status 1: cannot find package")))
		Expect(w.launch.BenchCallCount()).To(BeZero())
	})

	It("refuses a librados it cannot place in a Ceph release", func(ctx SpecContext) {
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if c.Name == "rpm" && c.Args[0] == "-qf" {
				return []byte("19.2.6~rc1"), nil
			}
			return w.output(ctx, c)
		}
		Expect(w.run(ctx, w.limiterConfig())).To(MatchError(ContainSubstring(`, /usr/lib64/librados.so.2.0.0, is version "19.2.6~rc1", not X.Y.Z`)))
		Expect(w.launch.BenchCallCount()).To(BeZero())
	})

	It("refuses a binary that links no librados", func(ctx SpecContext) {
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if c.Name == "ldd" {
				return []byte("\tlibc.so.6 => /lib64/libc.so.6 (0x00007f)\n"), nil
			}
			return w.output(ctx, c)
		}
		Expect(w.run(ctx, w.limiterConfig())).To(MatchError(ContainSubstring("links no librados.so.2")))
	})

	It("refuses chart values whose Ceph tag is no version", func(ctx SpecContext) {
		Expect(os.WriteFile(filepath.Join(w.repo, "hack", "rooket", "squid", "values", "rook-ceph-cluster.yaml"),
			[]byte("cephImage:\n  tag: latest\n"), 0o600)).To(Succeed())
		Expect(w.run(ctx, w.limiterConfig())).To(MatchError(ContainSubstring(`cephImage.tag "latest" is not a vX.Y.Z version`)))
	})
})
