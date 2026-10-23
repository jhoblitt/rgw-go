package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// The user elbencho signs as. Its keys are fixed, as the s3-tests users' are,
// so that every run's commands are the same; the secret reaches elbencho
// through the environment, never its command line.
const (
	benchUID    = "bench"
	benchKey    = "BENCH0000000000000AK"
	benchSecret = "bench-secret-0123456789abcdefghijklmnopqrstuv"
	// rgwGoEntity is the config entity rgw-go-up.sh runs rgw-go as.
	rgwGoEntity = "client.rgw.rgw-go"
)

// buckets are the buckets the profiles write, which the run empties after
// each profile and removes whenever it ends.
var buckets = []string{"bench-small", "bench-large", "bench-large-mpu", "bench-list"}

// Runner runs the comparison against a release's cluster. Its fields are the
// seams a spec replaces: the commands, /proc, the clock, the environment, the
// CPU profile's HTTP client, and the intervals and bounds it keeps.
type Runner struct {
	Exec harness.Execer
	// Proc is /proc.
	Proc   fs.FS
	Now    func() time.Time
	Getenv func(string) string
	// Sleep waits out the quiet after each phase and between the polls of the
	// service map, the garbage collection queue and the pool's free space.
	Sleep func(ctx context.Context, d time.Duration) error
	// HTTP fetches rgw-go's CPU profiles.
	HTTP *http.Client
	// Stdout takes the plan, the measured phases' output and the report's
	// answer, and Stderr the log.
	Stdout, Stderr io.Writer
	// Handler makes the log's handler for a writer.
	Handler func(io.Writer) slog.Handler
	// Quiet is the gateway's idle time after each phase and preparation, two
	// samples, so that the samples a phase's CPU is read between see little
	// of its neighbors.
	Quiet time.Duration
	// SiteWait bounds the wait for the service map to list one radosgw, and
	// SitePoll is how often it is read: the manager can list a restarted
	// radosgw's old entry for about a minute.
	SitePoll, SiteWait time.Duration
	// GCWait bounds the draining of the garbage collection queue, and GCPoll
	// is how often it is listed: the parity options leave no collector thread.
	GCPoll, GCWait time.Duration
	// SpaceWait bounds the wait for twice a write's dataset to be free, and
	// SpacePoll is how often ceph df is read: the space a removal frees
	// reaches ceph df a few seconds after it.
	SpacePoll, SpaceWait time.Duration
	// SampleEvery is the sampler's interval, and SampleTimeout bounds each
	// read of the radosgw pod's process.
	SampleEvery, SampleTimeout time.Duration
	// CallTimeout bounds each call to the cluster; LocalTimeout each other
	// query on this host; BuildTimeout each go build and the report's go run;
	// ElbenchoBuildTimeout elbencho's build, which clones and builds the AWS
	// SDK when cold; UpTimeout rgw-go-up.sh, which can restart the radosgw;
	// DownTimeout rgw-go-down.sh; PrepTimeout each unmeasured elbencho
	// command, a preparation or a removal, and remove-user.sh; CompactTimeout
	// the OSD's compaction, which ceph tell osd.* compact waits out, since the
	// OSD answers only once its store is compacted (OSD.cc's "compact"
	// handler at v19.2.6 and v20.2.4); and PhaseMargin how long a measured
	// phase, an httpload run or a CPU profile may run past BENCH_SECONDS.
	CallTimeout, LocalTimeout, BuildTimeout, ElbenchoBuildTimeout time.Duration
	UpTimeout, DownTimeout, PrepTimeout, CompactTimeout           time.Duration
	PhaseMargin                                                   time.Duration
}

// NewRunner returns a Runner on this host: child processes, /proc, the wall
// clock and the process's environment.
func NewRunner(stdout, stderr io.Writer, handler func(io.Writer) slog.Handler) *Runner {
	return &Runner{
		Exec:                 harness.NewExecer(harness.ExecWaitDelay),
		Proc:                 os.DirFS("/proc"),
		Now:                  time.Now,
		Getenv:               os.Getenv,
		Sleep:                harness.Sleep,
		HTTP:                 &http.Client{},
		Stdout:               stdout,
		Stderr:               stderr,
		Handler:              handler,
		Quiet:                2 * time.Second,
		SitePoll:             5 * time.Second,
		SiteWait:             3 * time.Minute,
		GCPoll:               5 * time.Second,
		GCWait:               3 * time.Minute,
		SpacePoll:            10 * time.Second,
		SpaceWait:            3 * time.Minute,
		SampleEvery:          time.Second,
		SampleTimeout:        10 * time.Second,
		CallTimeout:          3 * time.Minute,
		LocalTimeout:         time.Minute,
		BuildTimeout:         15 * time.Minute,
		ElbenchoBuildTimeout: time.Hour,
		UpTimeout:            15 * time.Minute,
		DownTimeout:          2 * time.Minute,
		PrepTimeout:          30 * time.Minute,
		CompactTimeout:       30 * time.Minute,
		PhaseMargin:          5 * time.Minute,
	}
}

// site is the realm, zonegroup and zone radosgw-admin works in.
type site struct {
	realm, zonegroup, zone string
}

// option is one parity option and its value.
type option struct {
	name, value string
}

// running is a background task of the run, the sampler or a CPU profile's
// fetch: end stops it, and done delivers its result.
type running struct {
	end  func()
	done <-chan error
}

// stop ends the task and returns its result.
func (t *running) stop() error {
	t.end()
	return <-t.done
}

// finish waits for the task to end by itself and returns its result.
func (t *running) finish() error {
	err := <-t.done
	t.end()
	return err
}

// state is a run under way: its settings, directory, logs and cluster, what
// it learned of the gateways, and what its exit must undo.
type state struct {
	cfg Config
	dir string
	// log takes what the run does, to stderr and run.log; out takes the
	// commands' output, to run.log only.
	log, out *slog.Logger
	runLog   string
	cl       harness.Cluster
	site     site
	dataPool string
	// clusterImage is the image the cluster runs, and cephVersion the version
	// its chart values pin.
	clusterImage, cephVersion string
	// parity are the options both gateways' entities must hold.
	parity             []option
	elbencho, httpload string
	// The radosgw's: its endpoint, config entity and pod, and its frontends
	// as the service map reports them.
	radosgwEndpoint, radosgwEntity, radosgwPod, radosgwFrontends string
	// The compared gateway's: where elbencho reaches it, what the sampler
	// reads (pid:<pid> or pod:<name>), its frontends and their TCP_NODELAY,
	// and the zonegroup's api_name elbencho signs for.
	endpoint, target, frontends, tcpNoDelay, apiName string
	// metrics is the running rgw-go's metrics address.
	metrics string
	// idle is the data pool's free bytes when the run started.
	idle int64
	// userCreated and rgwGoStarted say what the exit must undo, and sampler
	// and profile what it must stop: the profile's sampler and the CPU
	// profile's fetch, while they run.
	userCreated, rgwGoStarted bool
	sampler, profile          *running
}

// admin runs radosgw-admin in the toolbox, in the site the radosgw serves:
// without the site options it works in a zone named default, which it
// creates on first use and the radosgw never serves.
func (st *state) admin(ctx context.Context, args ...string) ([]byte, error) {
	return st.cl.Toolbox(ctx, append(append([]string{"radosgw-admin"}, args...),
		"--rgw-realm="+st.site.realm, "--rgw-zonegroup="+st.site.zonegroup, "--rgw-zone="+st.site.zone)...)
}

// ran records a command's output in run.log.
func (st *state) ran(ctx context.Context, cmd string, out []byte, err error) {
	if err != nil {
		st.out.ErrorContext(ctx, "ran", slog.String("cmd", cmd), slog.String("output", string(out)), slog.Any("error", err))
		return
	}
	st.out.InfoContext(ctx, "ran", slog.String("cmd", cmd), slog.String("output", string(out)))
}

// logged runs c within timeout, its output, standard and error, recorded in
// run.log, where gateway comparisons have always kept it.
func (r *Runner) logged(ctx context.Context, st *state, timeout time.Duration, c harness.Cmd) error {
	var out bytes.Buffer
	c.Out = &out
	_, err := harness.Output(ctx, r.Exec, timeout, c)
	st.ran(ctx, c.String(), out.Bytes(), err)
	return err
}

// manifest is what the run reads of the release's manifest.json, which make
// populate writes.
type manifest struct {
	Realm     string `json:"realm"`
	Zonegroup string `json:"zonegroup"`
	Zone      string `json:"zone"`
	Pools     struct {
		Data string `json:"data"`
	} `json:"pools"`
}

// readManifest reads the release's populated site and data pool.
func readManifest(cfg Config) (manifest, error) {
	path := filepath.Join(cfg.clusterOut(), "manifest.json")
	data, err := os.ReadFile(path) //nolint:gosec // the path is the release's rooket output
	if errors.Is(err, fs.ErrNotExist) {
		return manifest{}, fmt.Errorf("no %s; run make populate RELEASE=%s first", path, cfg.Release)
	}
	if err != nil {
		return manifest{}, fmt.Errorf("reading the manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m.Realm == "" || m.Zonegroup == "" || m.Zone == "" {
		return manifest{}, fmt.Errorf("%s names no realm, zonegroup or zone", path)
	}
	if m.Pools.Data == "" {
		return manifest{}, fmt.Errorf("%s names no data pool", path)
	}
	return m, nil
}

// Run runs cfg's profiles against its gateway into the run directory and
// renders the run's REPORT.md. Whenever the run ends, its exit removes the
// bench user with its buckets, stops the rgw-go it started, collects the
// garbage and compacts the OSD, and returns the run's failure joined with
// the exit's own.
func (r *Runner) Run(ctx context.Context, cfg Config) (err error) {
	out := cfg.clusterOut()
	conf, image := filepath.Join(out, "ceph.conf"), filepath.Join(out, "image")
	if !harness.IsFile(conf) || !harness.IsFile(image) {
		return fmt.Errorf("no %s or %s: run make cluster-up RELEASE=%s", conf, image, cfg.Release)
	}
	data, err := os.ReadFile(image) //nolint:gosec // the path is the release's rooket output
	if err != nil {
		return fmt.Errorf("reading the cluster's image: %w", err)
	}
	m, err := readManifest(cfg)
	if err != nil {
		return err
	}
	cephVersion, err := harness.PinnedVersion(cfg.Repo, cfg.Release)
	if err != nil {
		return err
	}
	parity, err := parityOptions(cfg.Repo, cfg.Release)
	if err != nil {
		return err
	}
	st := &state{
		cfg:          cfg,
		dir:          cfg.Dir(r.Now()),
		cl:           harness.Cluster{Exec: r.Exec, Rooket: cfg.Rooket, Release: cfg.Release, CallTimeout: r.CallTimeout},
		site:         site{realm: m.Realm, zonegroup: m.Zonegroup, zone: m.Zone},
		dataPool:     m.Pools.Data,
		clusterImage: strings.TrimRight(string(data), "\n"),
		cephVersion:  cephVersion,
		parity:       parity,
		elbencho:     filepath.Join(cfg.Repo, "hack", "bench", "out", "elbencho"),
		httpload:     filepath.Join(cfg.Repo, "hack", "bench", "out", "httpload"),
	}
	if err = os.MkdirAll(st.dir, 0o750); err != nil {
		return fmt.Errorf("creating the run directory: %w", err)
	}
	st.runLog = filepath.Join(st.dir, "run.log")
	runLog, err := harness.OpenLog(st.runLog)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runLog.Close()) }()
	st.log = slog.New(r.Handler(io.MultiWriter(r.Stderr, runLog)))
	st.out = slog.New(r.Handler(runLog))
	defer func() { err = r.exit(ctx, st, err) }()
	return r.run(ctx, st)
}

// run is the run itself, from elbencho's build to the report.
func (r *Runner) run(ctx context.Context, st *state) error {
	cfg := st.cfg
	// The settings that name no measured quantity, where they came from
	// included, since a config file can fill any of them.
	st.log.InfoContext(ctx, "gateway comparison", slog.String("gateway", cfg.Gateway),
		slog.String("cluster", "rgw-go-"+cfg.Release), slog.String("profiles", strings.Join(cfg.Profiles, " ")),
		slog.String("dir", st.dir), slog.String("config", cfg.ConfigFile), slog.String("repo", cfg.Repo),
		slog.String("rooket", cfg.Rooket), slog.String("go_tags", cfg.GoTags), slog.String("rgw_go_flags", cfg.RgwGoFlags))
	if err := r.logged(ctx, st, r.ElbenchoBuildTimeout,
		harness.Cmd{Name: filepath.Join(cfg.Repo, "hack", "bench", "elbencho-build.sh")}); err != nil {
		return fmt.Errorf("building elbencho failed; see %s: %w", st.runLog, err)
	}
	if slices.Contains(cfg.Profiles, "noop") {
		if _, err := harness.Output(ctx, r.Exec, r.BuildTimeout, harness.Cmd{Name: "go", Args: []string{
			"-C", cfg.Repo, "build", "-o", st.httpload, "./hack/bench/httpload",
		}}); err != nil {
			return fmt.Errorf("building httpload: %w", err)
		}
	}
	if err := r.useSite(ctx, st); err != nil {
		return err
	}
	// Garbage an earlier run left queued would hold space this run's datasets
	// need, and would be collected during its phases.
	st.log.InfoContext(ctx, "collecting queued garbage")
	if err := r.drainGC(ctx, st); err != nil {
		return fmt.Errorf("the garbage collection queue did not drain in %d s; see %s: %w", seconds(r.GCWait), st.runLog, err)
	}
	var err error
	if st.idle, err = st.cl.PoolAvail(ctx, st.dataPool); err != nil {
		return fmt.Errorf("ceph df names no pool %s: %w", st.dataPool, err)
	}
	var plan strings.Builder
	fits := cfg.WritePlan(&plan, st.idle)
	if _, err = io.WriteString(r.Stdout, plan.String()); err != nil {
		return fmt.Errorf("writing the plan: %w", err)
	}
	st.out.InfoContext(ctx, "plan", slog.String("output", plan.String()))
	if !fits {
		return fmt.Errorf("a profile's dataset is more than half of the %d B the data pool can take: lower its threads or objects", st.idle)
	}
	switch cfg.Gateway {
	case "radosgw":
		err = r.useRadosgw(ctx, st)
	case "rgw-go":
		err = r.startRgwGo(ctx, st)
	}
	if err != nil {
		return err
	}
	if st.frontends == "" {
		return fmt.Errorf("cannot tell which frontends %s serves with", cfg.Gateway)
	}
	if st.tcpNoDelay = noDelay(cfg.Gateway, st.frontends); st.tcpNoDelay != "on" {
		return fmt.Errorf("%s serves with the frontends '%s', which leave TCP_NODELAY off, and both gateways run with it on: "+
			"give the radosgw the chart values' rgwCommandFlags with make cluster-up RELEASE=%s, then make populate RELEASE=%s",
			cfg.Gateway, st.frontends, cfg.Release, cfg.Release)
	}
	if st.apiName, err = r.apiName(ctx, st); err != nil {
		return err
	}
	if err := r.writeConfig(ctx, st); err != nil {
		return err
	}
	if err := r.writeEnvironment(ctx, st); err != nil {
		return err
	}
	if err := r.ensureUser(ctx, st); err != nil {
		return err
	}
	st.log.InfoContext(ctx, "endpoint", slog.String("endpoint", st.endpoint), slog.String("api_name", st.apiName),
		slog.String("sampling", st.target))
	for _, p := range cfg.Profiles {
		if err := r.profile(ctx, st, p); err != nil {
			return err
		}
	}
	return r.render(ctx, st)
}

// profile runs one profile between its sampler's start and stop, keeps
// rgw-go's GC lines of it, and removes its objects and collects their
// garbage, so that the next profile starts from the space the run started
// with.
func (r *Runner) profile(ctx context.Context, st *state, p string) error {
	started := r.Now()
	var gcFrom int64
	if st.cfg.Gateway == "rgw-go" {
		var err error
		if gcFrom, err = completeLines(filepath.Join(st.cfg.clusterOut(), "rgw-go.log")); err != nil {
			return err
		}
	}
	st.sampler = r.startSampler(ctx, st, filepath.Join(st.dir, "samples-"+p+".csv"))
	// Samples before the first phase too, so that its CPU is read between
	// samples that bracket it.
	if err := r.Sleep(ctx, r.Quiet); err != nil {
		return err
	}
	if err := r.runProfile(ctx, st, p); err != nil {
		return err
	}
	err := st.sampler.stop()
	st.sampler = nil
	if err != nil {
		return fmt.Errorf("%s: the sampler of %s failed: %w", p, st.target, err)
	}
	if st.cfg.Gateway == "rgw-go" {
		if err := keepGCTrace(filepath.Join(st.cfg.clusterOut(), "rgw-go.log"), gcFrom,
			filepath.Join(st.dir, "gctrace-"+p+".log")); err != nil {
			return err
		}
	}
	st.log.InfoContext(ctx, "profile done", slog.String("profile", p), slog.Int64("seconds", seconds(r.Now().Sub(started))))
	started = r.Now()
	if err := r.emptyBuckets(ctx, st); err != nil {
		return fmt.Errorf("%s: removing the bench objects failed; see %s: %w", p, st.runLog, err)
	}
	if err := r.drainGC(ctx, st); err != nil {
		return fmt.Errorf("%s: the garbage collection queue did not drain in %d s; see %s: %w", p, seconds(r.GCWait), st.runLog, err)
	}
	st.log.InfoContext(ctx, "removed its objects and collected the garbage", slog.String("profile", p),
		slog.Int64("seconds", seconds(r.Now().Sub(started))))
	return nil
}

// render renders the run's REPORT.md with hack/bench/report.
func (r *Runner) render(ctx context.Context, st *state) error {
	c := harness.Cmd{Name: "go", Args: []string{
		"-C", st.cfg.Repo, "run", "-tags=" + st.cfg.GoTags, "./hack/bench/report", "gateway", "--dir", st.dir,
	}}
	out, err := harness.Output(ctx, r.Exec, r.BuildTimeout, c)
	if err != nil {
		return fmt.Errorf("rendering the report: %w", err)
	}
	st.ran(ctx, c.String(), out, nil)
	if _, err := r.Stdout.Write(out); err != nil {
		return fmt.Errorf("writing the report's answer: %w", err)
	}
	return nil
}

// exit undoes what the run did, whether it succeeded, failed or was
// interrupted: it stops the CPU profile's fetch and the sampler, removes the
// bench user with its buckets so that make gate passes after the run, stops
// the rgw-go it started, and collects the garbage the removals left and
// compacts the OSD, so that the next run, rgw-go's after radosgw's, starts on
// an OSD without the tombstones this one's deletes left. Each step runs past
// an interrupt, within its own bounds. A failed removal or compaction fails
// the run; a failed stop of rgw-go is logged and leaves the run's outcome as
// it was.
func (r *Runner) exit(ctx context.Context, st *state, failure error) error {
	// Either still runs only when the run failed during its profile, so what
	// it returns adds nothing to that failure.
	if st.profile != nil {
		if err := st.profile.stop(); err != nil {
			st.log.DebugContext(ctx, "the CPU profile was cut short", slog.Any("error", err))
		}
		st.profile = nil
	}
	if st.sampler != nil {
		if err := st.sampler.stop(); err != nil {
			st.log.WarnContext(ctx, "the sampler failed", slog.String("target", st.target), slog.Any("error", err))
		}
		st.sampler = nil
	}
	if failure != nil {
		st.log.ErrorContext(ctx, "the run failed", slog.Any("error", failure))
	}
	ctx = context.WithoutCancel(ctx)
	var errs []error
	if st.userCreated {
		if err := r.removeBench(ctx, st); err != nil {
			st.log.ErrorContext(ctx, "removing the user and its buckets failed", slog.String("user", benchUID), slog.Any("error", err))
			errs = append(errs, fmt.Errorf("removing the user %s and its buckets failed; see %s: %w", benchUID, st.runLog, err))
		} else {
			st.log.InfoContext(ctx, "removed the user and its buckets", slog.String("user", benchUID))
		}
	}
	if st.rgwGoStarted {
		down := harness.Cmd{
			Name: filepath.Join(st.cfg.Repo, "hack", "rooket", "rgw-go-down.sh"),
			Args: []string{st.cfg.Release},
			Env:  []string{"ROOKET=" + st.cfg.Rooket},
		}
		if err := r.logged(ctx, st, r.DownTimeout, down); err != nil {
			st.log.ErrorContext(ctx, "stopping rgw-go failed", slog.String("log", st.runLog), slog.Any("error", err))
		}
	}
	if st.userCreated {
		if err := r.settleOSD(ctx, st); err != nil {
			st.log.ErrorContext(ctx, "collecting the garbage or compacting the OSD failed", slog.Any("error", err))
			errs = append(errs, fmt.Errorf("collecting the garbage or compacting the OSD failed; see %s: %w", st.runLog, err))
		} else {
			avail := "?"
			if n, err := st.cl.PoolAvail(ctx, st.dataPool); err == nil {
				avail = strconv.FormatInt(n, 10)
			}
			st.log.InfoContext(ctx, "collected the garbage and compacted the OSD", slog.String("bytes_available", avail))
		}
	}
	return errors.Join(failure, errors.Join(errs...))
}

// removeBench empties the bench buckets, then removes the user with its
// buckets and the garbage the removal leaves (remove-user.sh), trying both
// whatever the first gives.
func (r *Runner) removeBench(ctx context.Context, st *state) error {
	emptied := r.emptyBuckets(ctx, st)
	removed := r.logged(ctx, st, r.PrepTimeout, harness.Cmd{
		Name: filepath.Join(st.cfg.Repo, "hack", "admin", "remove-user.sh"),
		Args: []string{st.cfg.Release, benchUID},
		Env:  []string{"ROOKET=" + st.cfg.Rooket},
	})
	return errors.Join(emptied, removed)
}

// settleOSD collects the garbage the run's removals left and compacts the
// OSD's RocksDB, which their deletes left full of tombstones, so that the
// next run does not start on them. The compaction is bounded by
// CompactTimeout rather than the cluster-call bound: cut short, it would go
// on in the OSD under the next run.
func (r *Runner) settleOSD(ctx context.Context, st *state) error {
	if err := r.drainGC(ctx, st); err != nil {
		return err
	}
	st.log.InfoContext(ctx, "compacting the OSD")
	args := []string{"tell", "osd.*", "compact"}
	cl := st.cl
	cl.CallTimeout = r.CompactTimeout
	out, err := cl.Ceph(ctx, args...)
	st.ran(ctx, "ceph "+strings.Join(args, " "), out, err)
	return err
}

// seconds is d in whole seconds.
func seconds(d time.Duration) int64 {
	return int64(d / time.Second)
}
