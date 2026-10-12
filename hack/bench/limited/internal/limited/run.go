package limited

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
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner runs the limiter re-run against a release's cluster. Its fields are
// the seams a spec replaces: the commands, the benchmark processes, /proc,
// the clock, the environment and the intervals it waits.
type Runner struct {
	Exec     Execer
	Launcher Launcher
	// Proc is /proc.
	Proc    fs.FS
	Now     func() time.Time
	Getenv  func(string) string
	Resolve func(string) (string, error)
	Sleep   func(ctx context.Context, d time.Duration) error
	// Stdout takes the benchmark processes' output, and Stderr the log.
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
	// SIGKILL.
	Grace time.Duration
	// CallTimeout bounds each call to the cluster's toolbox. It outlasts the
	// two minutes that bound each rados operation inside it, so that rados
	// reports a stuck operation itself.
	CallTimeout time.Duration
	// PurgeWait bounds the removal of what a failed benchmark process left in
	// the pool.
	PurgeWait time.Duration
	// BuildTimeout bounds the benchmark's build, which a cold build cache
	// makes slow, and LocalTimeout every other command run on this host:
	// ldd, rpm and dpkg, git, go version and rooket version.
	BuildTimeout, LocalTimeout time.Duration
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
		Stdout:       stdout,
		Stderr:       stderr,
		Handler:      handler,
		Poll:         10 * time.Second,
		AvailWait:    3 * time.Minute,
		NoiseEvery:   15 * time.Second,
		NoiseWindow:  time.Second,
		Grace:        watchdogGrace,
		CallTimeout:  3 * time.Minute,
		PurgeWait:    5 * time.Minute,
		BuildTimeout: 15 * time.Minute,
		LocalTimeout: time.Minute,
	}
}

// local runs a command on this host within timeout: its whole process group
// is killed at the deadline, as a toolbox call's is, so no command can hold
// the run.
func (r *Runner) local(ctx context.Context, timeout time.Duration, c Cmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return r.Exec.Output(ctx, c)
}

// Run builds the benchmark, records env.json and runs the over-budget cell
// in each mode under the limiter, into the run directory. A benchmark process
// that fails, the watchdog's stop included, fails the run after its objects
// are removed from the pool.
func (r *Runner) Run(ctx context.Context, cfg Config) (err error) {
	conf, image := cfg.CephConf(), filepath.Join(cfg.clusterOut(), "image")
	if !isFile(conf) || !isFile(image) {
		return fmt.Errorf("no %s or %s: run make cluster-up RELEASE=%s", conf, image, cfg.Release)
	}
	data, err := os.ReadFile(image) //nolint:gosec // the path is the release's rooket output
	if err != nil {
		return fmt.Errorf("reading the cluster's image: %w", err)
	}
	clusterImage := strings.TrimRight(string(data), "\n")

	dir := cfg.Dir(r.Now())
	err = os.MkdirAll(dir, 0o750)
	if err != nil {
		return fmt.Errorf("creating the run directory: %w", err)
	}
	sweepLog, err := openLog(filepath.Join(dir, "sweep.log"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sweepLog.Close()) }()
	log := slog.New(r.Handler(io.MultiWriter(r.Stderr, sweepLog)))
	// The run is read from its directory afterwards, so a failure's reason is
	// kept there as well as on stderr.
	defer func() {
		if err != nil {
			log.ErrorContext(ctx, "the limiter re-run failed", slog.Any("error", err))
		}
	}()
	noiseLog, err := openLog(filepath.Join(dir, "noise.log"))
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

	log.InfoContext(ctx, "seam limiter re-run", slog.String("release", cfg.Release),
		slog.String("cluster_image", clusterImage), slog.String("dir", dir), slog.Int64("inflight_bytes", cfg.InflightBytes))
	bin := cfg.Bin()
	log.InfoContext(ctx, "building the benchmark", slog.String("bin", bin))
	if _, err = r.local(ctx, r.BuildTimeout, Cmd{Name: "go", Args: []string{
		"-C", cfg.Repo, "test", "-c",
		"-tags=" + cfg.GoTags + ",integration", "-o", bin, "./test/bench/seam",
	}}); err != nil {
		return fmt.Errorf("building %s: %w", bin, err)
	}

	cl := cluster{exec: r.Exec, rooket: cfg.Rooket, release: cfg.Release, callTimeout: r.CallTimeout}
	env, err := r.environment(ctx, cfg, cl, bin, clusterImage)
	if err != nil {
		return err
	}
	err = writeJSON(filepath.Join(dir, "env.json"), env)
	if err != nil {
		return err
	}

	for _, mode := range modes {
		err = r.waitAvail(ctx, log, cl, cfg, mode)
		if err != nil {
			return err
		}
		err = r.bench(ctx, log, cl, cfg, dir, mode)
		if err != nil {
			return err
		}
	}

	left, err := cl.objects(ctx, "")
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "objects left in the pool", slog.String("pool", pool), slog.Int("objects", len(left)))
	log.InfoContext(ctx, "the limiter re-run's cells are in the run directory; it renders no REPORT.md", slog.String("dir", dir))
	return nil
}

// waitAvail waits until the pool reports MinAvailGiB available, or fails
// once AvailWait has passed. A poll gets no more than the wait has left, but
// at least a second, so that the last one still asks.
func (r *Runner) waitAvail(ctx context.Context, log *slog.Logger, cl cluster, cfg Config, mode string) error {
	need := cfg.MinAvailGiB << 30
	deadline := r.Now().Add(r.AvailWait)
	for {
		poll, cancel := context.WithTimeout(ctx, max(deadline.Sub(r.Now()), time.Second))
		avail, err := cl.poolAvail(poll)
		cancel()
		if err == nil && avail >= need {
			log.InfoContext(ctx, "the pool has room", slog.Int64("bytes_available", avail))
			return nil
		}
		if !r.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("not enough space for the over-budget cell in %s mode: %w", mode, err)
			}
			return fmt.Errorf("not enough space for the over-budget cell in %s mode: the pool has %d bytes available, under %d GiB",
				mode, avail, cfg.MinAvailGiB)
		}
		err = r.Sleep(ctx, r.Poll)
		if err != nil {
			return err
		}
	}
}

// bench runs the over-budget cell in mode in a benchmark process of its own,
// appending its cells to overbudget-<mode>.jsonl and its output to
// bench-<mode>.log. When the process fails, it removes what the process left
// in the pool, bench-<pid>, the namespace the benchmark writes in.
func (r *Runner) bench(ctx context.Context, log *slog.Logger, cl cluster, cfg Config, dir, mode string) (err error) {
	out, err := openLog(filepath.Join(dir, "bench-"+mode+".log"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	limit := cfg.Limit()
	load, lerr := loadavg(r.Proc)
	if lerr != nil {
		load = "unknown"
	}
	log.InfoContext(ctx, "bench", slog.String("mode", mode), slog.String("args", strings.Join(cfg.CellArgs(), " ")),
		slog.Int64("limit_s", int64(limit/time.Second)), slog.String("load", load))
	pid, err := r.Launcher.Bench(ctx, Process{
		Path:  cfg.Bin(),
		Args:  cfg.BenchArgs(mode, filepath.Join(dir, "overbudget-"+mode+".jsonl")),
		Env:   []string{"RGW_GO_TEST_CEPH_CONF=" + cfg.CephConf()},
		Out:   io.MultiWriter(out, r.Stdout),
		Limit: limit,
		Grace: r.Grace,
	})
	if err == nil {
		log.InfoContext(ctx, "bench done", slog.String("mode", mode))
		return nil
	}
	if errors.Is(err, ErrStopped) {
		log.ErrorContext(ctx, "bench stopped past its limit", slog.String("mode", mode), slog.Int64("limit_s", int64(limit/time.Second)))
	} else {
		log.ErrorContext(ctx, "bench failed", slog.String("mode", mode), slog.Any("error", err))
	}
	err = fmt.Errorf("bench %s: %w", mode, err)
	if pid == 0 {
		return err
	}
	// The removal runs even when the run was interrupted, and ends within
	// PurgeWait: each toolbox call it makes is killed, kubectl with rooket,
	// when that time is up.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.PurgeWait)
	defer cancel()
	if perr := cl.purge(cleanup, log, "bench-"+strconv.Itoa(pid)); perr != nil {
		return errors.Join(err, perr)
	}
	return err
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
