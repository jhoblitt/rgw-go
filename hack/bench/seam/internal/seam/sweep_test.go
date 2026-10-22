package seam_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// planFloor is a floor run's line in a plan.
var planFloor = regexp.MustCompile(`^floor (\S+) at (\d+) \((\w+)\) from (the floor image|the cluster's image, unless it is the floor image): `)

// planEvents are the events the world records when a run follows plan: each
// benchmark process as the plan writes it, and each floor run's rados runs,
// the write, the rand of a read floor, and the cleanup, by their run name.
func planEvents(plan string) []string {
	GinkgoHelper()
	var events []string
	for _, line := range strings.Split(strings.TrimSuffix(plan, "\n"), "\n")[1:] {
		switch {
		case strings.HasPrefix(line, "bench "):
			events = append(events, line)
		case strings.HasPrefix(line, "floor "):
			m := planFloor.FindStringSubmatch(line)
			Expect(m).NotTo(BeNil(), "floor line %q", line)
			image := floorImage
			if m[4] != "the floor image" {
				image = clusterImage
			}
			run := "floor " + fmt.Sprintf("floor-%s-%s-%s-%s", m[1], m[2], m[3], image[strings.LastIndexByte(image, ':')+1:])
			events = append(events, run+" write")
			if strings.HasPrefix(m[1], "read") {
				events = append(events, run+" rand")
			}
			events = append(events, run+" cleanup")
		}
	}
	return events
}

// exitError is the error of a real process that exits as script makes it.
func exitError(ctx context.Context, script string) error {
	GinkgoHelper()
	err := exec.CommandContext(ctx, "/bin/sh", "-c", script).Run()
	Expect(err).To(BeAssignableToTypeOf(&exec.ExitError{}))
	return err
}

var _ = Describe("Runner.Run of the sweep", func() {
	var w *world

	BeforeEach(func() {
		w = newWorld()
	})

	readFile := func(name string) string {
		GinkgoHelper()
		data, err := os.ReadFile(filepath.Join(w.sweepDir(), name))
		Expect(err).NotTo(HaveOccurred())
		return string(data)
	}

	It("runs the planned schedule, in order: each benchmark process, and each floor run's write, rand and cleanup", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig(""))).To(Succeed())
		Expect(w.events).To(Equal(planEvents(golden("plan-sweep.txt"))))
		Expect(w.launch.BenchCallCount()).To(Equal(100))
	})

	It("runs the quick schedule in order", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		Expect(w.events).To(Equal(planEvents(golden("plan-sweep-quick.txt"))))
	})

	It("runs each process with BENCH_TIME, its cell and its results file against the release's ceph.conf", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		bin := filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam.test")
		_, p := w.launch.BenchArgsForCall(0)
		Expect(p.Path).To(Equal(bin))
		Expect(p.Args).To(Equal([]string{
			"-test.run", "^$", "-test.bench", "BenchmarkSeam", "-test.timeout", "440s", "-test.benchtime", "5s",
			"-mode=sync", "-results=" + filepath.Join(w.sweepDir(), "seam-sync.jsonl"), "-shapes=headread4k", "-conc=16",
		}))
		Expect(p.Env).To(Equal([]string{"RGW_GO_TEST_CEPH_CONF=" + filepath.Join(w.repo, "hack/rooket/out/squid/ceph.conf")}))
		Expect([]time.Duration{p.Limit, p.Grace}).To(Equal([]time.Duration{350 * time.Second, 30 * time.Second}))
		_, last := w.launch.BenchArgsForCall(w.launch.BenchCallCount() - 1)
		Expect(last.Args).To(Equal([]string{
			"-test.run", "^$", "-test.bench", "BenchmarkSeam", "-test.timeout", "440s", "-test.benchtime", "5s",
			"-mode=pipe", "-shapes=read4k", "-conc=256", "-test.cpuprofile=" + filepath.Join(w.sweepDir(), "cpu-pipe-read4k-256.pprof"),
		}), "the CPU profile's process appends its cells nowhere")
		Expect(filepath.Join(w.sweepDir(), "cpu-pipe-read4k-256.pprof")).To(BeARegularFile())
	})

	It("records env.json with the host, the cluster, both images and the sweep's settings", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig(""))).To(Succeed())
		Expect(readFile("env.json")).To(Equal(fmt.Sprintf(`{
  "release": "squid",
  "ceph_version": "19.2.6",
  "cluster_image": "quay.io/ceph/ceph:v20.2.4",
  "floor_image": "quay.io/ceph/ceph:v19.2.6",
  "floor_image_version": "19.2.6",
  "librados_version": "19.2.6",
  "librados_path": "/usr/lib64/librados.so.2.0.0",
  "floor_no_hints": true,
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
  "bench_time": "20s",
  "floor_seconds": 20,
  "conc": "1,16,64,256,512",
  "large_conc": "1,4,16",
  "write4m_objects": 1000,
  "over_objects": 640,
  "objecter_inflight_ops": "1024",
  "objecter_inflight_op_bytes": "104857600",
  "pool_max_avail": 9286684672,
  "load": "0.98 0.96 1.31",
  "repo": "%s",
  "rooket": "rooket",
  "go_tags": "ceph_preview"
}
`, runtime.NumCPU(), w.repo)))
	})

	It("records an empty BENCH_LARGE_CONC in env.json", func(ctx SpecContext) {
		cfg := w.sweepConfig("1")
		cfg.LargeConc = nil
		Expect(w.run(ctx, cfg)).To(Succeed())
		Expect(readFile("env.json")).To(ContainSubstring(`  "large_conc": "",` + "\n"))
		Expect(w.events).NotTo(ContainElement(Or(ContainSubstring("read4m"), ContainSubstring("floor-write4m"), ContainSubstring("-shapes=write4m -conc=16"))))
	})

	It("appends each measured floor run's summary to floor.jsonl, and keeps its output", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig(""))).To(Succeed())
		lines := strings.Split(strings.TrimSuffix(readFile("floor.jsonl"), "\n"), "\n")
		Expect(lines).To(HaveLen(36), "one a floor run of the plan")
		Expect(lines[0]).To(Equal(`{"kind":"floor","tool":"rados bench","image":"quay.io/ceph/ceph:v19.2.6","shape":"read4k","op":"rand",` +
			`"size":4096,"conc":1,"bracket":"","seconds":20,"max_objects":0,"objects":1,"ops":190012,"elapsed_s":20.0004,` +
			`"ops_per_sec":9500.409991800165,"mean_us":1681.52,"bandwidth_mbps":37.1116}`))
		Expect(lines).To(ContainElement(`{"kind":"floor","tool":"rados bench","image":"quay.io/ceph/ceph:v20.2.4","shape":"write4m","op":"write",` +
			`"size":4194304,"conc":16,"bracket":"","seconds":600,"max_objects":1000,"objects":0,"ops":190012,"elapsed_s":20.0004,` +
			`"ops_per_sec":9500.409991800165,"mean_us":1681.52,"bandwidth_mbps":37.1116}`))
		Expect(readFile("floor-read4k-1-single-v19.2.6.txt")).To(Equal(radosBenchOutput("reads")))
		Expect(readFile("floor.log")).To(ContainSubstring("== 21:05:09 rados -N bench-floor-4096 bench 600 write -t 1 -b 4096 --max-objects 1 " +
			"--no-cleanup --no-hints --run-name floor-read4k-1-single-v19.2.6 from quay.io/ceph/ceph:v19.2.6\n"))
	})

	It("runs each floor run's rados in a container named for this process, against the release's rooket output", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		out := filepath.Join(w.repo, "hack", "rooket", "out", "squid")
		var names []string
		for i := range w.launch.FloorCallCount() {
			_, c := w.launch.FloorArgsForCall(i)
			names = append(names, c.Name)
		}
		Expect(names).To(HaveLen(w.launch.FloorCallCount()))
		Expect(names[:3]).To(Equal([]string{"rgw-go-bench-floor-777-1", "rgw-go-bench-floor-777-2", "rgw-go-bench-floor-777-3"}))
		_, set := w.launch.FloorArgsForCall(0)
		Expect(set.Args[slices.Index(set.Args, "-N"):]).To(Equal([]string{
			"-N", "bench-floor-4096", "bench", "600", "write", "-t", "16", "-b", "4096", "--max-objects", "16",
			"--no-cleanup", "--no-hints", "--run-name", "floor-read4k-16-single-v19.2.6",
		}))
		Expect(set.Limit).To(Equal(900*time.Second), "the rand set's write runs over 600 s, and five minutes more")
		_, rand := w.launch.FloorArgsForCall(1)
		Expect(rand.Engine).To(Equal("podman"))
		Expect(rand.Args).To(Equal([]string{
			"--network", "host", "--hostname", "rgw-go-bench-floor", "-v", out + ":/etc/ceph:ro", floorImage,
			"rados", "-c", "/etc/ceph/ceph.conf", "-k", "/etc/ceph/ceph.client.admin.keyring",
			"--rados-osd-op-timeout=120", "--rados-mon-op-timeout=120", "-p", "rgw-go-test",
			"-N", "bench-floor-4096", "bench", "5", "rand", "-t", "16", "--run-name", "floor-read4k-16-single-v19.2.6", "--no-verify",
		}))
		Expect([]time.Duration{rand.Limit, rand.Grace}).To(Equal([]time.Duration{305 * time.Second, 30 * time.Second}))
		_, cleanup := w.launch.FloorArgsForCall(2)
		Expect(cleanup.Args[len(cleanup.Args)-7:]).To(Equal([]string{"-N", "bench-floor-4096", "cleanup", "--run-name", "floor-read4k-16-single-v19.2.6", "-t", "64"}))
		Expect(cleanup.Limit).To(Equal(300*time.Second), "a cleanup's window is 0")
	})

	It("writes each 4 MiB floor of BENCH_WRITE4M_OBJECTS objects without hints, capped at 600 s", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		var writes []seam.Container
		for i := range w.launch.FloorCallCount() {
			_, c := w.launch.FloorArgsForCall(i)
			if slices.Contains(c.Args, "floor-write4m-16-before-v19.2.6") && !slices.Contains(c.Args, "cleanup") {
				writes = append(writes, c)
			}
		}
		Expect(writes).To(HaveLen(1))
		Expect(writes[0].Args[slices.Index(writes[0].Args, "-N"):]).To(Equal([]string{
			"-N", "bench-floor-4194304", "bench", "600", "write", "-t", "16", "--run-name", "floor-write4m-16-before-v19.2.6",
			"-b", "4194304", "--no-cleanup", "--no-hints", "--max-objects", "100",
		}))
		Expect(writes[0].Limit).To(Equal(900*time.Second), "its 600 s cap, and five minutes more")
	})

	It("skips the cluster image's floor where the cluster runs the floor image", func(ctx SpecContext) {
		w.writeCluster(floorImage)
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		Expect(w.events).NotTo(ContainElement(ContainSubstring("v20.2.4")))
		Expect(strings.Count(readFile("floor.jsonl"), "\n")).To(Equal(10), "the quick plan's 14 floor runs, less the 4 from the cluster's image")
	})

	It("waits for the pool's space before each 4 MiB write, cell or floor, and each over-budget cell", func(ctx SpecContext) {
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		var dfs int
		for _, line := range w.commands() {
			if strings.Contains(line, " df ") {
				dfs++
			}
		}
		Expect(dfs).To(Equal(1+strings.Count(golden("plan-sweep-quick.txt"), "wait for 5 GiB")), "env.json's, then one a wait")
	})

	It("fails, naming the floor, when the pool lacks the space for a 4 MiB floor write", func(ctx SpecContext) {
		r := seam.NewRunner(io.Discard, io.Discard, nil)
		w.runner(r)
		r.AvailWait = 0
		// env.json's df, then the first wait's, before the write4m floor.
		w.avail = []int64{9286684672, 1 << 30}
		cfg := w.sweepConfig("1")
		cfg.MinAvailGiB = 2
		Expect(r.Run(ctx, cfg)).To(MatchError("not enough space for the write4m floor at 16: the pool has 1073741824 bytes available, under 2 GiB"))
		Expect(w.events[len(w.events)-1]).To(Equal("floor floor-read4m-16-single-v20.2.4 cleanup"), "the 4 MiB write never ran")
	})

	It("pulls a floor image the engine lacks", func(ctx SpecContext) {
		w.failWith["podman image inspect"] = errors.New("exit status 125: no such image")
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		Expect(w.commands()).To(ContainElement("podman pull " + floorImage))
		Expect(readFile("floor.log")).To(HavePrefix("== 21:05:09 podman pull quay.io/ceph/ceph:v19.2.6\n[]\n"))
	})

	DescribeTable("refuses a floor image it cannot use, before any benchmark process",
		func(ctx SpecContext, edit func(*world, *seam.Config), want string) {
			cfg := w.sweepConfig("1")
			edit(w, &cfg)
			Expect(w.run(ctx, cfg)).To(MatchError(want))
			Expect(w.launch.BenchCallCount()).To(BeZero())
			Expect(filepath.Join(w.sweepDir(), "env.json")).NotTo(BeAnExistingFile())
		},
		Entry("one that cannot be pulled", func(w *world, _ *seam.Config) {
			w.failWith["podman image inspect"] = errors.New("exit status 125")
			w.failWith["podman pull"] = errors.New("exit status 125: manifest unknown")
		}, "no floor image quay.io/ceph/ceph:v19.2.6 for librados 19.2.6: the floor's rados must be the release the benchmark links: exit status 125: manifest unknown"),
		Entry("one whose rados cannot run", func(w *world, _ *seam.Config) {
			w.failWith["rados --version"] = errors.New("exit status 127")
		}, "cannot run rados --version in quay.io/ceph/ceph:v19.2.6: exit status 127"),
		Entry("a BENCH_FLOOR_IMAGE of another release", func(_ *world, c *seam.Config) {
			c.FloorImage = "quay.io/ceph/ceph:v20.2.4"
		}, "the floor image quay.io/ceph/ceph:v20.2.4 runs Ceph 20.2.4, but the benchmark links librados 19.2.6"),
	)

	It("renders a faked sweep's directory with hack/bench/report", func(ctx SpecContext) {
		gomod, err := exec.CommandContext(ctx, "go", "env", "GOMOD").Output()
		Expect(err).NotTo(HaveOccurred())
		root := filepath.Dir(strings.TrimSpace(string(gomod)))
		host := harness.NewExecer(time.Second)
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if c.Name == "go" && slices.Contains(c.Args, "run") {
				// The report in this repository, not the world's.
				args := slices.Clone(c.Args)
				args[slices.Index(args, "-C")+1] = root
				return host.Output(ctx, harness.Cmd{Name: "go", Args: args})
			}
			return w.output(ctx, c)
		}
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		Expect(w.stdout.String()).To(ContainSubstring(filepath.Join(w.sweepDir(), "REPORT.md") + ": "))
		report := readFile("REPORT.md")
		Expect(report).To(ContainSubstring("floor"))
		GinkgoWriter.Println(report)
	}, SpecTimeout(5*time.Minute))

	Context("when it fails partway", func() {
		// stopAt makes the benchmark process of mode, shape and conc stop past
		// its limit with pid and leave objects behind.
		stopAt := func(mode, shape, conc string, pid int, failure error) {
			w.left[fmt.Sprintf("bench-%d", pid)] = []string{"seam-w0-0", "seam-w1-1"}
			w.launch.BenchStub = func(ctx context.Context, p seam.Process) (int, error) {
				if flagValue(p.Args, "mode") == mode && flagValue(p.Args, "shapes") == shape && flagValue(p.Args, "conc") == conc {
					w.record("failed")
					return pid, failure
				}
				return w.bench(ctx, p)
			}
		}
		// namespaces are the namespaces a command after the failure touched.
		touched := func() []string {
			var nss []string
			after := false
			for _, line := range w.commands() {
				after = after || strings.Contains(line, "-N ")
				if f := strings.Fields(line); after && slices.Contains(f, "-N") {
					if ns := f[slices.Index(f, "-N")+1]; !slices.Contains(nss, ns) {
						nss = append(nss, ns)
					}
				}
			}
			return nss
		}
		// removals are the toolbox sessions that removed objects by name.
		removals := func() []harness.Cmd {
			var cmds []harness.Cmd
			for i := range w.exec.OutputCallCount() {
				if _, c := w.exec.OutputArgsForCall(i); slices.Contains(c.Args, "xargs") {
					cmds = append(cmds, c)
				}
			}
			return cmds
		}

		It("removes the stopped process's objects and the floor's written so far, then exits with the process's status", func(ctx SpecContext) {
			stopAt("callback", "read4k", "64", 5150, fmt.Errorf("%w of 8m20s: %w", seam.ErrStopped, exitError(ctx, "kill -TERM $$")))
			err := w.run(ctx, w.sweepConfig(""))
			Expect(err).To(MatchError(seam.ErrStopped))
			Expect(err).To(MatchError("bench callback: stopped past its limit of 8m20s: signal: terminated"))
			Expect(seam.ExitStatus(ctx, err)).To(Equal(143), "128 and SIGTERM, as the shell reports it")
			Expect(w.events[len(w.events)-1]).To(Equal("failed"), "nothing runs after the failure")
			Expect(touched()).To(Equal([]string{"bench-5150", "bench-floor-4096"}), "no 4 MiB floor had run")
			Expect(w.commands()).To(ContainElement(ContainSubstring("-N bench-floor-4096 cleanup --prefix benchmark_data -t 64")))
			Expect(removals()).To(Equal([]harness.Cmd{{
				Name: "rooket", Args: purgeArgs("bench-5150"), Env: []string{"ROOKET_NAME=rgw-go-squid"},
				Stdin: []byte("seam-w0-0\nseam-w1-1\n"),
			}}), "the floor's namespace listed nothing left to remove")
			sweep := readFile("sweep.log")
			Expect(sweep).To(ContainSubstring(`"msg":"removing what the failed run left in the pool","pool":"rgw-go-test","namespaces":["bench-5150","bench-floor-4096"]`))
			Expect(sweep).To(ContainSubstring(`"msg":"removed what the failed run left in the pool"`))
			Expect(sweep).To(ContainSubstring(`"level":"ERROR","msg":"the sweep failed","error":"bench callback: stopped past its limit of 8m20s: signal: terminated"`))
		})

		It("exits with a failed process's own status", func(ctx SpecContext) {
			stopAt("sync", "headread4k", "16", 4300, exitError(ctx, "exit 3"))
			err := w.run(ctx, w.sweepConfig("1"))
			Expect(err).To(MatchError("bench sync: exit status 3"))
			Expect(seam.ExitStatus(ctx, err)).To(Equal(3))
			Expect(touched()).To(Equal([]string{"bench-4300"}), "the floor had written nothing")
		})

		It("removes the floor's namespaces, both sizes, after a floor run's container is stopped, and exits with 1", func(ctx SpecContext) {
			w.launch.FloorStub = func(ctx context.Context, c seam.Container) error {
				if slices.Contains(c.Args, "floor-write4m-16-after-v19.2.6") && !slices.Contains(c.Args, "cleanup") {
					w.record("stopped")
					return fmt.Errorf("%w of 15m0s", seam.ErrStopped)
				}
				return w.floor(ctx, c)
			}
			err := w.run(ctx, w.sweepConfig("1"))
			Expect(err).To(MatchError(seam.ErrStopped))
			Expect(err).To(MatchError("rados bench failed: stopped past its limit of 15m0s"))
			Expect(seam.ExitStatus(ctx, err)).To(Equal(1))
			Expect(w.events[len(w.events)-1]).To(Equal("stopped"))
			Expect(touched()).To(Equal([]string{"bench-floor-4096", "bench-floor-4194304"}))
		})

		It("tries every namespace when a removal fails, and the failure's status stands", func(ctx SpecContext) {
			stopAt("callback", "read4k", "64", 5150, fmt.Errorf("%w of 8m20s: %w", seam.ErrStopped, exitError(ctx, "kill -TERM $$")))
			w.failWith["-N bench-5150 ls"] = errors.New("exit status 110: connection timed out")
			err := w.run(ctx, w.sweepConfig(""))
			Expect(err).To(MatchError(ContainSubstring("bench callback: stopped past its limit of 8m20s")))
			Expect(err).To(MatchError(ContainSubstring("listing the objects in bench-5150 of rgw-go-test: exit status 110: connection timed out")))
			Expect(seam.ExitStatus(ctx, err)).To(Equal(143))
			Expect(w.commands()).To(ContainElement(ContainSubstring("-N bench-floor-4096 ls")), "the floor's namespace is still tried")
			Expect(readFile("sweep.log")).To(ContainSubstring(`"msg":"removing what the failed run left failed as well; the run's failure stands"`))
		})

		DescribeTable("fails on a floor run's rados",
			func(ctx SpecContext, run, op string, answer func(context.Context, seam.Container) error, want string) {
				w.launch.FloorStub = func(ctx context.Context, c seam.Container) error {
					if slices.Contains(c.Args, run) && slices.Contains(c.Args, op) {
						return answer(ctx, c)
					}
					return w.floor(ctx, c)
				}
				Expect(w.run(ctx, w.sweepConfig("1"))).To(MatchError(strings.ReplaceAll(want, "DIR", w.sweepDir())))
				Expect(touched()).To(ContainElement("bench-floor-4096"))
			},
			Entry("a bench that fails, with the end of its output", "floor-write4k-16-single-v19.2.6", "write",
				func(ctx context.Context, c seam.Container) error {
					fmt.Fprint(c.Out, "line 1\nline 2\nline 3\nline 4\nline 5\nerror opening pool rgw-go-test: (2) No such file or directory\n")
					return exitError(ctx, "exit 1")
				},
				"rados bench failed: exit status 1: line 2\nline 3\nline 4\nline 5\nerror opening pool rgw-go-test: (2) No such file or directory"),
			Entry("a bench that reports no summary", "floor-write4k-16-single-v19.2.6", "write",
				func(_ context.Context, c seam.Container) error {
					fmt.Fprint(c.Out, "Total time run:         0\nTotal writes made:      0\n")
					return nil
				},
				"no summary in DIR/floor-write4k-16-single-v19.2.6.txt: no bandwidth\nno mean latency"),
			Entry("a bench whose time run is zero", "floor-write4k-16-single-v19.2.6", "write",
				func(_ context.Context, c seam.Container) error {
					fmt.Fprint(c.Out, strings.Replace(radosBenchOutput("writes"), "20.0004", "0", 1))
					return nil
				},
				"no summary in DIR/floor-write4k-16-single-v19.2.6.txt: the time run, 0 s, is no time to divide by"),
			Entry("a rand floor's write", "floor-read4k-16-single-v19.2.6", "write",
				func(ctx context.Context, _ seam.Container) error { return exitError(ctx, "exit 1") },
				"writing the rand set of floor-read4k-16-single-v19.2.6: exit status 1"),
			Entry("a cleanup", "floor-read4k-16-single-v19.2.6", "cleanup",
				func(ctx context.Context, _ seam.Container) error { return exitError(ctx, "exit 1") },
				"rados cleanup of floor-read4k-16-single-v19.2.6: exit status 1"),
		)

		It("fails when the report does not render", func(ctx SpecContext) {
			w.failWith["./hack/bench/report"] = errors.New("exit status 1: env.json names no floor_image")
			Expect(w.run(ctx, w.sweepConfig("1"))).To(MatchError("rendering the report: exit status 1: env.json names no floor_image"))
		})

		It("exits 130 when SIGINT interrupts a floor run, once it has removed what it left", func(ctx SpecContext) {
			sigs := make(chan os.Signal, 1)
			runCtx, stop := harness.WatchSignals(ctx, sigs)
			defer stop()
			w.launch.FloorStub = func(ctx context.Context, c seam.Container) error {
				if slices.Contains(c.Args, "floor-write4k-16-single-v19.2.6") && !slices.Contains(c.Args, "cleanup") {
					w.record("interrupted")
					sigs <- syscall.SIGINT
					// As the watchdog does once it has killed the container.
					<-ctx.Done()
					return context.Cause(ctx)
				}
				return w.floor(ctx, c)
			}
			err := w.run(runCtx, w.sweepConfig("1"))
			Expect(err).To(MatchError(ContainSubstring("signal received: interrupt")))
			Expect(seam.ExitStatus(runCtx, err)).To(Equal(130), "128 and SIGINT, as the shell reports it")
			Expect(w.events[len(w.events)-1]).To(Equal("interrupted"))
			Expect(touched()).To(Equal([]string{"bench-floor-4096"}))
		})

		It("exits 143 when SIGTERM interrupts a wait for the pool's space, once it has removed what it left", func(ctx SpecContext) {
			sigs := make(chan os.Signal, 1)
			runCtx, stop := harness.WatchSignals(ctx, sigs)
			defer stop()
			// env.json's df, then a pool too full for the first write4m floor.
			w.avail = []int64{9286684672, 1 << 30}
			dfs := 0
			w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
				if strings.Contains(c.String(), " df ") {
					if dfs++; dfs == 3 {
						sigs <- syscall.SIGTERM
					}
				}
				return w.output(ctx, c)
			}
			err := w.run(runCtx, w.sweepConfig("1"))
			Expect(err).To(MatchError(harness.Interrupted{Signal: syscall.SIGTERM}))
			Expect(seam.ExitStatus(runCtx, err)).To(Equal(143), "128 and SIGTERM, as the shell reports it")
			Expect(w.events[len(w.events)-1]).To(Equal("floor floor-read4m-16-single-v20.2.4 cleanup"), "the write4m floor never started")
			Expect(touched()).To(Equal([]string{"bench-floor-4096", "bench-floor-4194304"}))
		})
	})

	It("gives every command a deadline of its own", func(ctx SpecContext) {
		w.failWith["podman image inspect"] = errors.New("exit status 125: no such image")
		Expect(w.run(ctx, w.sweepConfig("1"))).To(Succeed())
		kinds := map[string]int{}
		var unbounded []string
		for i := range w.exec.OutputCallCount() {
			callCtx, c := w.exec.OutputArgsForCall(i)
			kind, bound := "local", time.Minute
			switch {
			case c.Name == "rooket" && c.Args[0] == "kubectl":
				kind, bound = "toolbox", 3*time.Minute
			case c.Name == "go" && (slices.Contains(c.Args, "-c") || slices.Contains(c.Args, "run")):
				kind, bound = "build", 15*time.Minute
			case c.Name == "podman" && c.Args[0] == "pull":
				kind, bound = "pull", 15*time.Minute
			}
			kinds[kind]++
			if deadline, ok := callCtx.Deadline(); !ok || time.Until(deadline) > bound {
				unbounded = append(unbounded, c.String())
			}
		}
		Expect(kinds).To(Equal(map[string]int{
			"toolbox": 1 + 2 + strings.Count(golden("plan-sweep-quick.txt"), "wait for") + 1, // env.json's df and config gets, the waits' df, the final count
			"build":   2,                                                                     // the benchmark and the report
			"pull":    1,
			"local":   8, // ldd, rpm -qf, image inspect, rados --version, rooket version, go version, rpm -q and git describe
		}))
		Expect(unbounded).To(BeEmpty(), "commands with no deadline, or one past their bound")
	})
})
