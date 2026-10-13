package seam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner runs the sweep or the limiter re-run against a release's cluster.
// Its fields are the seams a spec replaces: the commands, the benchmark
// processes and floor runs, /proc, the clock, the environment and the
// intervals it waits.
type Runner struct {
	Exec     Execer
	Launcher Launcher
	// Proc is /proc.
	Proc    fs.FS
	Now     func() time.Time
	Getenv  func(string) string
	Resolve func(string) (string, error)
	Sleep   func(ctx context.Context, d time.Duration) error
	// Pid names the floor's containers, so that two runs on one host never
	// share one.
	Pid int
	// Stdout takes the benchmark processes' output and the report's answer,
	// and Stderr the log.
	Stdout, Stderr io.Writer
	// Handler makes the log's handler for a writer.
	Handler func(io.Writer) slog.Handler
	// Poll is how often the run reads the pool's free space while it waits
	// for it, and AvailWait how long it waits: the space a removal frees
	// reaches ceph df a few seconds after it.
	Poll, AvailWait time.Duration
	// NoiseEvery is how often the run samples the host's noise, and
	// NoiseWindow the span of each sample.
	NoiseEvery, NoiseWindow time.Duration
	// Grace is the time a stopped benchmark process gets between SIGTERM and
	// SIGKILL, and a floor run's client after its container's kill.
	Grace time.Duration
	// CallTimeout bounds each call to the cluster's toolbox. It outlasts the
	// two minutes that bound each rados operation inside it, so that rados
	// reports a stuck operation itself.
	CallTimeout time.Duration
	// PurgeWait bounds the removal of each namespace a failed run left in the
	// pool: a large write4k cell leaves hundreds of thousands of objects.
	PurgeWait time.Duration
	// BuildTimeout bounds the benchmark's build, which a cold build cache
	// makes slow, and the report's, which go run builds; PullTimeout the
	// floor image's pull; and LocalTimeout every other command run on this
	// host: ldd, rpm and dpkg, git, go version, rooket version, and the
	// engine's image inspect and rados --version.
	BuildTimeout, PullTimeout, LocalTimeout time.Duration
}

// NewRunner returns a Runner on this host: child processes, /proc, the wall
// clock and the process's environment.
func NewRunner(stdout, stderr io.Writer, handler func(io.Writer) slog.Handler) *Runner {
	return &Runner{
		Exec:         execer{waitDelay: execWaitDelay},
		Launcher:     watchdog{},
		Proc:         os.DirFS("/proc"),
		Now:          time.Now,
		Getenv:       os.Getenv,
		Resolve:      filepath.EvalSymlinks,
		Sleep:        sleepContext,
		Pid:          os.Getpid(),
		Stdout:       stdout,
		Stderr:       stderr,
		Handler:      handler,
		Poll:         10 * time.Second,
		AvailWait:    3 * time.Minute,
		NoiseEvery:   15 * time.Second,
		NoiseWindow:  time.Second,
		Grace:        watchdogGrace,
		CallTimeout:  3 * time.Minute,
		PurgeWait:    30 * time.Minute,
		BuildTimeout: 15 * time.Minute,
		PullTimeout:  15 * time.Minute,
		LocalTimeout: time.Minute,
	}
}

// state is a run under way: its settings, directory, log and cluster, the
// images whose rados runs the floor, and what a failure must remove from the
// pool.
type state struct {
	cfg Config
	dir string
	log *slog.Logger
	cl  cluster
	// floorLog takes the output of the floor's rados runs that measure
	// nothing: the floor image's pull, a rand floor's writes and every
	// cleanup.
	floorLog io.Writer
	// clusterImage is the image the cluster runs, and floorImage the image of
	// the release of the librados the benchmark links, which runs Ceph
	// floorVersion.
	clusterImage, floorImage, floorVersion string
	// floorRuns counts the floor's containers, which it names.
	floorRuns int
	left      leftovers
}

// leftovers are the namespaces a failed run must remove: that of the
// benchmark process that failed, bench-<pid>, and those the floor wrote to.
// A process that ends cleanly removes its own objects.
type leftovers struct {
	bench string
	floor []string
}

func (l *leftovers) addFloor(ns string) {
	if !slices.Contains(l.floor, ns) {
		l.floor = append(l.floor, ns)
	}
}

func (l *leftovers) namespaces() []string {
	if l.bench == "" {
		return l.floor
	}
	return append([]string{l.bench}, l.floor...)
}

// local runs a command on this host within timeout: its whole process group
// is killed at the deadline, as a toolbox call's is, so no command can hold
// the run.
func (r *Runner) local(ctx context.Context, timeout time.Duration, c Cmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return r.Exec.Output(ctx, c)
}

// Run builds the benchmark, records env.json and runs cfg's schedule into
// the run directory; the sweep then renders its REPORT.md. A run that fails
// partway, a watchdog's stop included, removes what it left in the pool, the
// objects of the floor namespaces it wrote to and of the benchmark process
// that failed, and returns the failure, whether or not the removal succeeds.
func (r *Runner) Run(ctx context.Context, cfg Config) (err error) {
	conf, image := cfg.CephConf(), filepath.Join(cfg.clusterOut(), "image")
	if !isFile(conf) || !isFile(image) {
		return fmt.Errorf("no %s or %s: run make cluster-up RELEASE=%s", conf, image, cfg.Release)
	}
	data, err := os.ReadFile(image) //nolint:gosec // the path is the release's rooket output
	if err != nil {
		return fmt.Errorf("reading the cluster's image: %w", err)
	}

	st := &state{cfg: cfg, dir: cfg.Dir(r.Now()), clusterImage: strings.TrimRight(string(data), "\n")}
	err = os.MkdirAll(st.dir, 0o750)
	if err != nil {
		return fmt.Errorf("creating the run directory: %w", err)
	}
	sweepLog, err := openLog(filepath.Join(st.dir, "sweep.log"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sweepLog.Close()) }()
	st.log = slog.New(r.Handler(io.MultiWriter(r.Stderr, sweepLog)))
	// The run is read from its directory afterwards, so a failure's reason is
	// kept there as well as on stderr.
	defer func() {
		switch {
		case err == nil:
		case cfg.Limiter:
			st.log.ErrorContext(ctx, "the limiter re-run failed", slog.Any("error", err))
		default:
			st.log.ErrorContext(ctx, "the sweep failed", slog.Any("error", err))
		}
	}()
	noiseLog, err := openLog(filepath.Join(st.dir, "noise.log"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, noiseLog.Close()) }()
	sampling, stopSampling := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		noise{proc: r.Proc, now: r.Now, window: r.NoiseWindow, sleep: r.Sleep}.run(sampling, noiseLog, r.NoiseEvery)
	})
	defer wg.Wait()
	defer stopSampling()

	st.cl = cluster{exec: r.Exec, rooket: cfg.Rooket, release: cfg.Release, callTimeout: r.CallTimeout}
	defer func() {
		if err != nil {
			err = r.cleanUp(ctx, st, err)
		}
	}()
	if cfg.Limiter {
		st.log.InfoContext(ctx, "seam limiter re-run", slog.String("release", cfg.Release),
			slog.String("cluster_image", st.clusterImage), slog.String("dir", st.dir), slog.Int64("inflight_bytes", cfg.InflightBytes))
	} else {
		var floorLog *os.File
		floorLog, err = openLog(filepath.Join(st.dir, "floor.log"))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, floorLog.Close()) }()
		st.floorLog = floorLog
		st.log.InfoContext(ctx, "seam sweep", slog.String("release", cfg.Release),
			slog.String("cluster_image", st.clusterImage), slog.String("dir", st.dir))
	}
	bin := cfg.Bin()
	st.log.InfoContext(ctx, "building the benchmark", slog.String("bin", bin))
	if _, err = r.local(ctx, r.BuildTimeout, Cmd{Name: "go", Args: []string{
		"-C", cfg.Repo, "test", "-c",
		"-tags=" + cfg.GoTags + ",integration", "-o", bin, "./test/bench/seam",
	}}); err != nil {
		return fmt.Errorf("building %s: %w", bin, err)
	}
	lib, err := r.linkedLibrados(ctx, bin)
	if err != nil {
		return err
	}
	if !cfg.Limiter {
		err = r.resolveFloorImage(ctx, st, lib)
		if err != nil {
			return err
		}
	}
	env, err := r.environment(ctx, st, lib)
	if err != nil {
		return err
	}
	err = writeJSON(filepath.Join(st.dir, "env.json"), env)
	if err != nil {
		return err
	}

	for _, s := range cfg.schedule() {
		switch s := s.(type) {
		case benchStep:
			err = r.bench(ctx, st, s)
		case floorStep:
			err = r.floor(ctx, st, s)
		}
		if err != nil {
			return err
		}
	}

	left, err := st.cl.objects(ctx, "")
	if err != nil {
		return err
	}
	st.log.InfoContext(ctx, "objects left in the pool", slog.String("pool", pool), slog.Int("objects", len(left)))
	if cfg.Limiter {
		st.log.InfoContext(ctx, "the limiter re-run's cells are in the run directory; it renders no REPORT.md", slog.String("dir", st.dir))
		return nil
	}
	return r.render(ctx, st)
}

// render renders the sweep's REPORT.md with hack/bench/report.
func (r *Runner) render(ctx context.Context, st *state) error {
	out, err := r.local(ctx, r.BuildTimeout, Cmd{Name: "go", Args: []string{
		"-C", st.cfg.Repo, "run", "-tags=" + st.cfg.GoTags, "./hack/bench/report", "seam", "--dir", st.dir,
	}})
	if err != nil {
		return fmt.Errorf("rendering the report: %w", err)
	}
	if _, err := r.Stdout.Write(out); err != nil {
		return fmt.Errorf("writing the report's answer: %w", err)
	}
	return nil
}

// cleanUp removes what the failed run left in the pool and returns its
// failure, joined with the removal's own when that fails too. The removal
// runs even when the run was interrupted, each namespace's within PurgeWait,
// every toolbox call of it bounded by what that has left; a namespace whose
// removal fails leaves the next one to be tried.
func (r *Runner) cleanUp(ctx context.Context, st *state, failure error) error {
	namespaces := st.left.namespaces()
	if len(namespaces) == 0 {
		return failure
	}
	st.log.InfoContext(ctx, "removing what the failed run left in the pool", slog.String("pool", pool),
		slog.Any("namespaces", namespaces))
	cl := st.cl
	cl.callTimeout = r.PurgeWait
	var errs []error
	for _, ns := range namespaces {
		purge, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.PurgeWait)
		errs = append(errs, cl.purge(purge, st.log, ns))
		cancel()
	}
	if err := errors.Join(errs...); err != nil {
		st.log.ErrorContext(ctx, "removing what the failed run left failed as well; the run's failure stands", slog.Any("error", err))
		return errors.Join(failure, err)
	}
	st.log.InfoContext(ctx, "removed what the failed run left in the pool", slog.String("pool", pool))
	return failure
}

// waitAvail waits until the pool reports MinAvailGiB available before what
// it names, or fails once AvailWait has passed. A poll gets no more than the
// wait has left, but at least a second, so that the last one still asks.
func (r *Runner) waitAvail(ctx context.Context, st *state, what string) error {
	need := st.cfg.MinAvailGiB << 30
	deadline := r.Now().Add(r.AvailWait)
	for {
		poll, cancel := context.WithTimeout(ctx, max(deadline.Sub(r.Now()), time.Second))
		avail, err := st.cl.poolAvail(poll)
		cancel()
		if err == nil && avail >= need {
			st.log.InfoContext(ctx, "the pool has room", slog.Int64("bytes_available", avail))
			return nil
		}
		if !r.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("not enough space for %s: %w", what, err)
			}
			return fmt.Errorf("not enough space for %s: the pool has %d bytes available, under %d GiB",
				what, avail, st.cfg.MinAvailGiB)
		}
		err = r.Sleep(ctx, r.Poll)
		if err != nil {
			return err
		}
	}
}

// bench runs one cell in one mode in a benchmark process of its own, after
// waiting for the pool's free space when the step asks, appending its output
// to bench-<mode>.log. When the process fails, its namespace, bench-<pid>, is
// left for the failure's removal.
func (r *Runner) bench(ctx context.Context, st *state, b benchStep) (err error) {
	if b.wait != "" {
		err = r.waitAvail(ctx, st, b.wait)
		if err != nil {
			return err
		}
	}
	out, err := openLog(filepath.Join(st.dir, "bench-"+b.mode+".log"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	cell := b.args(st.dir)
	limit := st.cfg.Limit(cell)
	var results string
	if b.results != "" {
		results = filepath.Join(st.dir, b.results)
	}
	st.log.InfoContext(ctx, "bench", slog.String("mode", b.mode), slog.String("args", strings.Join(cell, " ")),
		slog.Int64("limit_s", int64(limit/time.Second)), slog.String("load", r.load()))
	pid, err := r.Launcher.Bench(ctx, Process{
		Path:  st.cfg.Bin(),
		Args:  st.cfg.BenchArgs(b.mode, results, cell),
		Env:   []string{"RGW_GO_TEST_CEPH_CONF=" + st.cfg.CephConf()},
		Out:   io.MultiWriter(out, r.Stdout),
		Limit: limit,
		Grace: r.Grace,
	})
	if err == nil {
		st.log.InfoContext(ctx, "bench done", slog.String("mode", b.mode), slog.String("load", r.load()))
		return nil
	}
	if errors.Is(err, ErrStopped) {
		st.log.ErrorContext(ctx, "bench stopped past its limit", slog.String("mode", b.mode), slog.Int64("limit_s", int64(limit/time.Second)))
	} else {
		st.log.ErrorContext(ctx, "bench failed", slog.String("mode", b.mode), slog.Any("error", err))
	}
	if pid != 0 {
		st.left.bench = "bench-" + strconv.Itoa(pid)
	}
	return &processError{mode: b.mode, err: err}
}

// load is the host's three load averages, or unknown.
func (r *Runner) load() string {
	load, err := loadavg(r.Proc)
	if err != nil {
		return "unknown"
	}
	return load
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func openLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is in the run directory
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return f, nil
}

// writeJSON writes v to path as indented JSON.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", filepath.Base(path), err)
	}
	err = os.WriteFile(path, append(data, '\n'), 0o600) //nolint:gosec // the path is in the run directory
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
