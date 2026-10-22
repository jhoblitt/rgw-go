package seam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam"
	"github.com/jhoblitt/rgw-go/hack/bench/seam/internal/seam/seamfakes"
	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/internal/harness/harnessfakes"
)

// toolboxArgs is how every toolbox call starts, and toolboxStdinArgs one that
// passes the call stdin.
var (
	toolboxArgs      = []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--"}
	toolboxStdinArgs = []string{"kubectl", "-n", "rook-ceph", "exec", "-i", "deploy/rook-ceph-tools", "--"}
)

// purgeArgs is the toolbox command that removes the names on its stdin from
// namespace ns.
func purgeArgs(ns string) []string {
	return append(slices.Clone(toolboxStdinArgs),
		"xargs", "-r", "-d", `\n`, "-n", "1024", "-P", "8", "rados", "-p", "rgw-go-test", "-N", ns, "rm")
}

const (
	// clusterImage is the image the world's cluster runs, another release
	// than floorImage, the image of the librados its benchmark links, so
	// that a sweep also runs the cluster image's floor.
	clusterImage = "quay.io/ceph/ceph:v20.2.4"
	floorImage   = "quay.io/ceph/ceph:v19.2.6"
	// worldPid is the pid the world's Runner names its floor containers for.
	worldPid = 777
)

// world is a fake host and cluster: a repository holding the release's
// rooket output and chart values, the commands' answers, the benchmark
// processes and floor runs, and /proc.
type world struct {
	repo   string
	exec   *harnessfakes.FakeExecer
	launch *seamfakes.FakeLauncher
	proc   fstest.MapFS
	stdout *bytes.Buffer

	mu sync.Mutex
	// avail is what each ceph df reports in turn, the last one from then on.
	avail []int64
	// left is what rados ls lists in a namespace.
	left map[string][]string
	// failWith answers the commands whose line contains a key with its error.
	failWith map[string]error
	// events are the benchmark processes and the floor's rados runs in the
	// order they ran, the processes as the plan writes them.
	events []string
	// pids counts the benchmark processes, whose pids it gives.
	pids int
}

func newWorld() *world {
	w := &world{
		repo:     GinkgoT().TempDir(),
		exec:     &harnessfakes.FakeExecer{},
		launch:   &seamfakes.FakeLauncher{},
		stdout:   &bytes.Buffer{},
		avail:    []int64{9286684672},
		left:     map[string][]string{},
		failWith: map[string]error{},
		proc: fstest.MapFS{
			"meminfo":              {Data: []byte("MemTotal:       65007132 kB\nMemFree:        12345678 kB\n")},
			"cpuinfo":              {Data: []byte("processor\t: 0\nmodel name\t: AMD Ryzen 9 7950X3D 16-Core Processor\n\nprocessor\t: 1\nmodel name\t: other\n")},
			"loadavg":              {Data: []byte("0.98 0.96 1.31 2/1203 98765\n")},
			"sys/kernel/osrelease": {Data: []byte("7.2.8-100.fc43.x86_64\n")},
		},
	}
	w.writeCluster(clusterImage)
	values := filepath.Join(w.repo, "hack", "rooket", "squid", "values")
	Expect(os.MkdirAll(values, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(values, "rook-ceph-cluster.yaml"),
		[]byte("cephImage:\n  repository: quay.io/ceph/ceph\n  tag: v19.2.6\ncephBlockPools: []\n"), 0o600)).To(Succeed())
	w.exec.OutputStub = w.output
	w.launch.BenchStub = w.bench
	w.launch.FloorStub = w.floor
	return w
}

// writeCluster writes the release's rooket output for a cluster that runs
// image.
func (w *world) writeCluster(image string) {
	out := filepath.Join(w.repo, "hack", "rooket", "out", "squid")
	Expect(os.MkdirAll(out, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "ceph.conf"), []byte("[global]\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(out, "image"), []byte(image+"\n"), 0o600)).To(Succeed())
}

// output answers a command as the host and the cluster would.
func (w *world) output(_ context.Context, c harness.Cmd) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	line := c.String()
	for key, err := range w.failWith {
		if strings.Contains(line, key) {
			return nil, err
		}
	}
	args := c.Args
	for _, prefix := range [][]string{toolboxArgs, toolboxStdinArgs} {
		if c.Name == "rooket" && slices.Equal(args[:min(len(args), len(prefix))], prefix) {
			args = args[len(prefix):]
		}
	}
	switch {
	case c.Name == "go" && args[0] == "version":
		return []byte("go version go1.27.1 linux/amd64\n"), nil
	case c.Name == "go" && slices.Contains(args, "run"):
		return []byte(filepath.Join(args[len(args)-1], "REPORT.md") + ": cgo is not the bottleneck\n"), nil
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
	case c.Name == "podman" && args[0] == "image", c.Name == "podman" && args[0] == "pull":
		return []byte("[]\n"), nil
	case c.Name == "podman" && slices.Contains(args, "--version"):
		version := strings.TrimPrefix(args[len(args)-3][strings.LastIndexByte(args[len(args)-3], ':')+1:], "v")
		return []byte("ceph version " + version + " (b8d8c6cbbd5c5b8b0d8b1c3a58b16e2a6a8e2b1c) squid (stable)\n"), nil
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
		return []byte(strings.Join(w.left[args[slices.Index(args, "-N")+1]], "\n") + "\n"), nil
	case args[0] == "rados" && slices.Contains(args, "cleanup"), args[0] == "xargs":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected command %s", line)
}

// flagValue is the value of the -name= argument in args, or empty.
func flagValue(args []string, name string) string {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "-"+name+"="); ok {
			return v
		}
	}
	return ""
}

// intFlag is the number the -name= argument in args gives, 0 when there is
// none, as the benchmark's flag defaults.
func intFlag(args []string, name string) int64 {
	GinkgoHelper()
	v := flagValue(args, name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	Expect(err).NotTo(HaveOccurred())
	return n
}

// bench runs a benchmark process as BenchmarkSeam would: it appends a line
// for its cell to its results file and writes its CPU profile, a real one,
// which the report reads through go tool pprof.
func (w *world) bench(_ context.Context, p seam.Process) (int, error) {
	w.mu.Lock()
	w.pids++
	pid := 4242 + w.pids
	w.mu.Unlock()
	mode, shape, results := flagValue(p.Args, "mode"), flagValue(p.Args, "shapes"), flagValue(p.Args, "results")
	conc, err := strconv.Atoi(flagValue(p.Args, "conc"))
	Expect(err).NotTo(HaveOccurred())
	cell := slices.Clone(p.Args[slices.IndexFunc(p.Args, func(a string) bool { return strings.HasPrefix(a, "-mode=") })+1:])
	if results != "" {
		cell = cell[1:]
		ops, maxBytes := intFlag(p.Args, "inflight-ops"), intFlag(p.Args, "inflight-bytes")
		line, err := json.Marshal(map[string]any{
			"process": fmt.Sprintf("%d-1760303109000000000", pid), "time": "2026-10-12T21:05:09Z", "release": "squid",
			"mode": mode, "shape": shape, "budget_bytes": 4096, "ops_per_iter": 1, "conc": conc, "n": 1000,
			"ns_per_iter": 50000.0, "ops_per_sec": 20000.0, "p50_us": 800.0, "p99_us": 1500.0, "p999_us": 2500.0,
			"mean_us": 900.0, "cpu_us_per_op": 20.0, "threads_idle": 22, "threads_peak": 30, "gomaxprocs": 8, "errors": 0,
			"max_inflight_ops": ops, "max_inflight_bytes": maxBytes, "throttle_waits": 0,
		})
		Expect(err).NotTo(HaveOccurred())
		f, err := os.OpenFile(results, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.Write(append(line, '\n'))
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())
	}
	if profile := flagValue(p.Args, "test.cpuprofile"); profile != "" {
		data, err := os.ReadFile(filepath.Join("testdata", "cpu-read4k-256.pprof"))
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(profile, data, 0o600)).To(Succeed())
	}
	fmt.Fprintf(p.Out, "BenchmarkSeam/shape=%s/conc=%d\n", shape, conc)
	dir := ""
	if results != "" {
		dir = filepath.Dir(results)
	}
	event := "bench " + mode + ": " + strings.Join(cell, " ")
	if dir != "" {
		event = strings.ReplaceAll(event, dir+"/", "")
		event += " -> " + filepath.Base(results)
	} else if profile := flagValue(p.Args, "test.cpuprofile"); profile != "" {
		event = strings.ReplaceAll(event, filepath.Dir(profile)+"/", "")
	}
	w.record(fmt.Sprintf("%s (limit %d s)", event, int64(p.Limit/time.Second)))
	return pid, nil
}

// floor runs a floor run's rados as rados bench would, writing its summary
// for a measured run.
func (w *world) floor(_ context.Context, c seam.Container) error {
	args := c.Args[slices.Index(c.Args, "rgw-go-test")+1:]
	run := args[slices.Index(args, "--run-name")+1]
	switch {
	case slices.Contains(args, "cleanup"):
		w.record("floor " + run + " cleanup")
	case slices.Contains(args, "rand"):
		w.record("floor " + run + " rand")
		fmt.Fprint(c.Out, radosBenchOutput("reads"))
	default:
		w.record("floor " + run + " write")
		fmt.Fprint(c.Out, radosBenchOutput("writes"))
	}
	return nil
}

// radosBenchOutput is rados bench's output for a run that made what, writes
// or reads.
func radosBenchOutput(what string) string {
	return "hints = 0\n  sec Cur ops   started  finished  avg MB/s  cur MB/s last lat(s)  avg lat(s)\n" +
		"    0      16      9512      9496   37.0906   37.0938  0.00151442  0.00168152\n" +
		"Total time run:         20.0004\n" +
		"Total " + what + " made:      190012\n" +
		"Write size:             4096\n" +
		"Object size:            4096\n" +
		"Bandwidth (MB/sec):     37.1116\n" +
		"Stddev Bandwidth:       1.21\n" +
		"Average IOPS:           9500\n" +
		"Stddev IOPS:            310\n" +
		"Average Latency(s):     0.00168152\n" +
		"Stddev Latency(s):      0.0004\n"
}

func (w *world) record(event string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
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

func (w *world) runner(r *seam.Runner) {
	r.Exec, r.Launcher, r.Proc = w.exec, w.launch, w.proc
	r.Now = func() time.Time { return runStart }
	r.Getenv = func(string) string { return "" }
	r.Resolve = func(p string) (string, error) { return p + ".0.0", nil }
	r.Pid = worldPid
	r.Stdout = w.stdout
	r.Stderr = GinkgoWriter
	r.Handler = func(out io.Writer) slog.Handler { return slog.NewJSONHandler(out, nil) }
	r.Poll, r.AvailWait = time.Millisecond, time.Minute
	r.NoiseEvery, r.NoiseWindow = time.Hour, time.Millisecond
	r.Grace = 30 * time.Second
}

// limiterConfig is the limiter re-run of squid in the world's repository.
func (w *world) limiterConfig() seam.Config {
	cfg, err := seam.Settings{Release: "squid", Limiter: "1", Repo: w.repo}.Check()
	Expect(err).NotTo(HaveOccurred())
	return cfg
}

// sweepConfig is the sweep of squid in the world's repository, in quick
// mode when quick is 1.
func (w *world) sweepConfig(quick string) seam.Config {
	cfg, err := seam.Settings{Release: "squid", Repo: w.repo, Quick: quick}.Check()
	Expect(err).NotTo(HaveOccurred())
	return cfg
}

func (w *world) run(ctx context.Context, cfg seam.Config) error {
	r := seam.NewRunner(io.Discard, io.Discard, nil)
	w.runner(r)
	return r.Run(ctx, cfg)
}

// limiterDir is the limiter re-run's directory.
func (w *world) limiterDir() string {
	return filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam-20261012T210509Z-limited")
}

// sweepDir is the sweep's directory.
func (w *world) sweepDir() string {
	return filepath.Join(w.repo, "hack", "bench", "out", "squid", "seam-20261012T210509Z")
}
