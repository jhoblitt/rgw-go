// Package seam sweeps the seam microbenchmark against one release's rooket
// cluster and measures its floor with rados bench, run in a container on the
// host where the Go process runs; or it re-runs the sweep's over-budget cell
// under the in-flight limiter, the evidence that the limiter parks the excess
// submissions on goroutines rather than on OS threads blocked inside C.
package seam

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInflightBytes is librados's default objecter_inflight_op_bytes,
	// 100 MiB.
	DefaultInflightBytes = 104857600

	defaultOverObjects    = 640
	quickOverObjects      = 128
	defaultWrite4mObjects = 1000
	quickWrite4mObjects   = 100
	defaultFloorSeconds   = 20
	quickFloorSeconds     = 5
	defaultMinAvailGiB    = 5
	defaultBenchTime      = 20 * time.Second
	quickBenchTime        = 5 * time.Second

	// A stopped benchmark process gets watchdogGrace between SIGTERM and
	// SIGKILL; every planned duration gets watchdogMargin more.
	watchdogGrace  = 30 * time.Second
	watchdogMargin = 300 * time.Second

	// maxSeconds caps every duration so that ten times it, the limit of a
	// cell bounded by time, still fits go test's -test.timeout, a Go
	// duration.
	maxSeconds = 99999999

	// floorCapSeconds is the time cap of a floor write bounded by its object
	// count, write4m's and a rand floor's set: rados bench stops at whichever
	// of the two it reaches first.
	floorCapSeconds = 600

	// pool is the pool the benchmark writes to, cephtest.TestPool.
	pool = "rgw-go-test"
)

// overCell is the over-budget cell, write4m at 64 in flight, 256 MiB, past
// the byte budget.
var overCell = []string{"-shapes=write4m", "-conc=64", "-budget-bytes=0"}

// noLimit are the in-flight limiter's bounds no cell reaches, which leave
// submission to the objecter throttle.
var noLimit = []string{"-inflight-ops=1048576", "-inflight-bytes=1099511627776"}

// modes are the completion modes, each run in a benchmark process of its
// own: the runtime keeps the OS threads a process grows, so a later cell of
// the same process would start from an earlier one's threads, and go-ceph's
// completion notifier is process-wide.
var modes = []string{"sync", "callback", "pipe"}

var (
	defaultConc = []int{1, 16, 64, 256, 512}
	quickConc   = []int{16, 256}
	// The report pairs the 4 MiB cells with their floor runs only at 1, 4 and
	// 16 in flight, so a cell elsewhere would get floor runs it shows as no
	// floor. They are also the default.
	largeConcs     = []int{1, 4, 16}
	quickLargeConc = []int{16}
)

var (
	// At most 18 digits, so that the bound fits -inflight-bytes, an int64;
	// no leading zero, so that no spelling of the default names another
	// directory.
	bytesPattern = regexp.MustCompile(`^[1-9]\d{0,17}$`)
	// At most 10 digits, so that a count-bounded cell's limit, a tenth of a
	// second an object, fits go test's -test.timeout.
	countPattern = regexp.MustCompile(`^[1-9]\d{0,9}$`)
	// A floor run's seconds, and its limit with them, stay within
	// maxSeconds.
	floorSecondsPattern = regexp.MustCompile(`^[1-9]\d{0,7}$`)
	// No leading zero, so that no concurrency has two spellings.
	concPattern    = regexp.MustCompile(`^[1-9]\d{0,8}$`)
	gibPattern     = regexp.MustCompile(`^(0|[1-9]\d*)$`)
	secondsPattern = regexp.MustCompile(`^\d{1,9}$`)
	// Each part has at most 9 digits, so that no sum overflows.
	durationPattern = regexp.MustCompile(`^(\d{1,9}h)?(\d{1,9}m)?(\d{1,9}s)?$`)
)

// Settings are a run's inputs as given, before they are checked. An empty
// string is a setting left unset.
type Settings struct {
	Release       string
	Limiter       string
	InflightBytes string
	OverObjects   string
	Timeout       string
	MinAvailGiB   string
	CephConf      string
	Rooket        string
	GoTags        string
	Repo          string
	// Plan and Quick are 1 or unset, as BENCH_PLAN and BENCH_QUICK.
	Plan  string
	Quick string

	// The sweep's own settings, which the limiter re-run refuses.
	BenchTime    string
	FloorSeconds string
	Conc         string
	// LargeConc is nil when unset: unlike the other settings, an empty
	// BENCH_LARGE_CONC is a value, which skips the 4 MiB cells.
	LargeConc      *string
	Write4mObjects string
	FloorTimeout   string
	FloorImage     string

	// Engine is the container engine that runs the floor's rados bench.
	Engine string
}

// Config is a run's checked settings.
type Config struct {
	Release string
	Repo    string
	Rooket  string
	GoTags  string
	// Limiter selects the limiter re-run of the over-budget cell over the
	// sweep.
	Limiter bool
	Plan    bool
	// InflightBytes is the limiter re-run's byte bound.
	InflightBytes int64
	OverObjects   int64
	// Timeout is each benchmark process's limit; zero derives it from the
	// cell.
	Timeout     time.Duration
	MinAvailGiB int64

	// The sweep's settings, zero in the limiter re-run.
	BenchTime      time.Duration
	FloorSeconds   int64
	Conc           []int
	LargeConc      []int
	Write4mObjects int64
	// FloorTimeout is each floor run's limit; zero derives it from the run.
	FloorTimeout time.Duration
	// FloorImage is the image whose rados runs the floor; empty takes the
	// Ceph image of the release of the librados the benchmark links.
	FloorImage string
	Engine     string
}

// Check checks every setting and returns the run's Config, or the first
// setting it refuses. It touches no cluster and creates nothing.
func (s Settings) Check() (Config, error) {
	if s.Release != "squid" && s.Release != "tentacle" {
		return Config{}, fmt.Errorf("release %q is not squid or tentacle", s.Release)
	}
	for _, sw := range []struct{ name, value string }{
		{"BENCH_LIMITER", s.Limiter}, {"BENCH_PLAN", s.Plan}, {"BENCH_QUICK", s.Quick},
	} {
		if sw.value != "" && sw.value != "1" {
			return Config{}, fmt.Errorf("%s is %s: set it to 1 or leave it unset", sw.name, sw.value)
		}
	}
	limiter := s.Limiter == "1"
	if s.InflightBytes != "" && !limiter {
		return Config{}, errors.New("BENCH_INFLIGHT_BYTES applies only with BENCH_LIMITER=1")
	}
	if limiter {
		if name := s.sweepOnly(); name != "" {
			return Config{}, fmt.Errorf("%s applies only to the sweep, not with BENCH_LIMITER=1", name)
		}
	}
	repo, err := filepath.Abs(cmp.Or(s.Repo, "."))
	if err != nil {
		return Config{}, fmt.Errorf("resolving the repository: %w", err)
	}
	quick := s.Quick == "1"
	cfg := Config{
		Release:     s.Release,
		Repo:        repo,
		Rooket:      cmp.Or(s.Rooket, "rooket"),
		GoTags:      cmp.Or(s.GoTags, "ceph_preview"),
		Limiter:     limiter,
		Plan:        s.Plan == "1",
		OverObjects: defaultOverObjects,
		MinAvailGiB: defaultMinAvailGiB,
	}
	if quick {
		cfg.OverObjects = quickOverObjects
	}
	if limiter {
		cfg.InflightBytes = DefaultInflightBytes
		if s.InflightBytes != "" {
			if cfg.InflightBytes, err = parseMatching(s.InflightBytes, bytesPattern); err != nil {
				return Config{}, fmt.Errorf("BENCH_INFLIGHT_BYTES is %s: give a positive whole number of bytes, at most 18 digits", s.InflightBytes)
			}
		}
	}
	if s.OverObjects != "" {
		if cfg.OverObjects, err = parseMatching(s.OverObjects, countPattern); err != nil {
			return Config{}, fmt.Errorf("BENCH_OVER_OBJECTS is %s: give a positive whole number of at most 10 digits", s.OverObjects)
		}
	}
	if s.MinAvailGiB != "" {
		// The wait compares bytes, so the GiB must fit an int64 once shifted.
		gib, err := parseMatching(s.MinAvailGiB, gibPattern)
		if err != nil || gib > math.MaxInt64>>30 {
			return Config{}, fmt.Errorf("BENCH_MIN_AVAIL_GIB is %s: give a whole number of GiB, at most %d", s.MinAvailGiB, int64(math.MaxInt64>>30))
		}
		cfg.MinAvailGiB = gib
	}
	if s.CephConf != "" && canonical(s.CephConf) != canonical(cfg.CephConf()) {
		return Config{}, fmt.Errorf("RGW_GO_TEST_CEPH_CONF is %s, but the %s cluster is reached through %s: unset it",
			s.CephConf, cfg.Release, cfg.CephConf())
	}
	if s.Timeout != "" {
		var ok bool
		if cfg.Timeout, ok = parseSeconds(s.Timeout); !ok {
			return Config{}, fmt.Errorf("BENCH_TIMEOUT %s is not a duration in h, m and s from 1 s to %d s", s.Timeout, maxSeconds)
		}
	}
	if limiter {
		return cfg, nil
	}
	if err := s.checkSweep(&cfg, quick); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// sweepOnly names the first of the sweep's own settings that is set, or is
// empty when none is.
func (s Settings) sweepOnly() string {
	for _, set := range []struct {
		name string
		set  bool
	}{
		{"BENCH_TIME", s.BenchTime != ""},
		{"BENCH_FLOOR_SECONDS", s.FloorSeconds != ""},
		{"CONC", s.Conc != ""},
		{"BENCH_LARGE_CONC", s.LargeConc != nil},
		{"BENCH_WRITE4M_OBJECTS", s.Write4mObjects != ""},
		{"BENCH_FLOOR_TIMEOUT", s.FloorTimeout != ""},
		{"BENCH_FLOOR_IMAGE", s.FloorImage != ""},
	} {
		if set.set {
			return set.name
		}
	}
	return ""
}

// checkSweep checks the sweep's own settings into cfg, quick's where unset.
func (s Settings) checkSweep(cfg *Config, quick bool) error {
	cfg.BenchTime, cfg.FloorSeconds, cfg.Write4mObjects = defaultBenchTime, defaultFloorSeconds, defaultWrite4mObjects
	cfg.Conc, cfg.LargeConc = slices.Clone(defaultConc), slices.Clone(largeConcs)
	if quick {
		cfg.BenchTime, cfg.FloorSeconds, cfg.Write4mObjects = quickBenchTime, quickFloorSeconds, quickWrite4mObjects
		cfg.Conc, cfg.LargeConc = slices.Clone(quickConc), slices.Clone(quickLargeConc)
	}
	cfg.FloorImage = s.FloorImage
	cfg.Engine = cmp.Or(s.Engine, "podman")
	var (
		err error
		ok  bool
	)
	if s.BenchTime != "" {
		// The watchdog's limits derive from it, so it takes the whole
		// seconds parseSeconds reads, never what only go test reads.
		if cfg.BenchTime, ok = parseSeconds(s.BenchTime); !ok {
			return fmt.Errorf("BENCH_TIME %s is not a duration in h, m and s from 1 s to %d s", s.BenchTime, maxSeconds)
		}
	}
	if s.FloorSeconds != "" {
		if cfg.FloorSeconds, err = parseMatching(s.FloorSeconds, floorSecondsPattern); err != nil {
			return fmt.Errorf("BENCH_FLOOR_SECONDS is %s: give a positive whole number of seconds, at most 8 digits", s.FloorSeconds)
		}
	}
	if s.Conc != "" {
		if cfg.Conc, ok = parseConcs(s.Conc); !ok {
			return fmt.Errorf("CONC is %s: give positive whole numbers, comma separated, each at most once", s.Conc)
		}
	}
	if s.LargeConc != nil {
		if cfg.LargeConc, ok = parseLargeConcs(*s.LargeConc); !ok {
			return fmt.Errorf("BENCH_LARGE_CONC is %s: the 4 MiB cells run only at 1, 4 and 16, each at most once, "+
				"where the report pairs them with their floor; empty skips them", *s.LargeConc)
		}
	}
	if s.Write4mObjects != "" {
		if cfg.Write4mObjects, err = parseMatching(s.Write4mObjects, countPattern); err != nil {
			return fmt.Errorf("BENCH_WRITE4M_OBJECTS is %s: give a positive whole number of at most 10 digits", s.Write4mObjects)
		}
	}
	if s.FloorTimeout != "" {
		if cfg.FloorTimeout, ok = parseSeconds(s.FloorTimeout); !ok {
			return fmt.Errorf("BENCH_FLOOR_TIMEOUT %s is not a duration in h, m and s from 1 s to %d s", s.FloorTimeout, maxSeconds)
		}
	}
	return nil
}

// parseConcs parses a comma-separated list of distinct concurrencies.
func parseConcs(s string) ([]int, bool) {
	var concs []int
	for part := range strings.SplitSeq(s, ",") {
		if !concPattern.MatchString(part) {
			return nil, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || slices.Contains(concs, n) {
			return nil, false
		}
		concs = append(concs, n)
	}
	return concs, true
}

// parseLargeConcs parses BENCH_LARGE_CONC: distinct concurrencies among
// largeConcs, or none for an empty value.
func parseLargeConcs(s string) ([]int, bool) {
	if s == "" {
		return nil, true
	}
	concs, ok := parseConcs(s)
	if !ok {
		return nil, false
	}
	for _, n := range concs {
		if !slices.Contains(largeConcs, n) {
			return nil, false
		}
	}
	return concs, true
}

// parseMatching parses s as a decimal int64 once it matches pattern.
func parseMatching(s string, pattern *regexp.Regexp) (int64, error) {
	if !pattern.MatchString(s) {
		return 0, fmt.Errorf("%q does not match %s", s, pattern)
	}
	return strconv.ParseInt(s, 10, 64)
}

// parseSeconds parses a duration in whole seconds: a number of seconds, or
// Go's notation in whole hours, minutes and seconds (1h30m, 20s). It refuses
// the rest of what Go's durations and -test.benchtime accept, fractions
// (1.5s), smaller units (500ms) and counts (100x), so a limit is never
// derived from a duration misread; and it refuses zero, which as a limit
// would stop every process at once and as a benchtime go test refuses, and
// anything over maxSeconds.
func parseSeconds(s string) (time.Duration, bool) {
	var total int64
	if secondsPattern.MatchString(s) {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		total = n
	} else {
		parts := durationPattern.FindStringSubmatch(s)
		if s == "" || parts == nil {
			return 0, false
		}
		for i, unit := range []int64{3600, 60, 1} {
			part := parts[i+1]
			if part == "" {
				continue
			}
			n, err := strconv.ParseInt(part[:len(part)-1], 10, 64)
			if err != nil {
				return 0, false
			}
			total += n * unit
		}
	}
	if total < 1 || total > maxSeconds {
		return 0, false
	}
	return time.Duration(total) * time.Second, true
}

// canonical is path made absolute with its symbolic links resolved, as far
// as it exists.
func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// seconds is d in whole seconds, as go test's flags take it.
func seconds(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + "s"
}

// clusterOut is the release's rooket output, which holds its ceph.conf, its
// admin keyring and the image its cluster runs.
func (c Config) clusterOut() string {
	return filepath.Join(c.Repo, "hack", "rooket", "out", c.Release)
}

// CephConf is the release's ceph.conf, through which the benchmark reaches
// its cluster. The floor's rados reads it too, so the Go cells take their
// client config from there, or the two halves of a ratio could measure two
// clusters.
func (c Config) CephConf() string {
	return filepath.Join(c.clusterOut(), "ceph.conf")
}

// Suffix ends the run directory's name: nothing for the sweep; for the
// limiter re-run -limited, and the byte bound when it is not the default, so
// that runs at two bounds never share a name.
func (c Config) Suffix() string {
	switch {
	case !c.Limiter:
		return ""
	case c.InflightBytes == DefaultInflightBytes:
		return "-limited"
	default:
		return "-limited-" + strconv.FormatInt(c.InflightBytes, 10)
	}
}

// Dir is the run directory for a run started at t.
func (c Config) Dir(t time.Time) string {
	return filepath.Join(c.Repo, "hack", "bench", "out", c.Release, "seam-"+t.UTC().Format("20060102T150405Z")+c.Suffix())
}

// Bin is the benchmark binary the run builds.
func (c Config) Bin() string {
	return filepath.Join(c.Repo, "hack", "bench", "out", c.Release, "seam.test")
}

// CellArgs are the benchmark arguments that make the over-budget cell under
// the limiter: its operation bound stays derived, as rgw-go runs, which the
// cell's 64 in flight do not reach.
func (c Config) CellArgs() []string {
	return append(slices.Clone(overCell),
		"-inflight-bytes="+strconv.FormatInt(c.InflightBytes, 10),
		"-test.benchtime="+strconv.FormatInt(c.OverObjects, 10)+"x")
}

// countArg matches a cell bounded by a count rather than by time.
var countArg = regexp.MustCompile(`^-test\.benchtime=(\d+)x$`)

// Limit is the limit of the benchmark process that runs cell: Timeout when
// set, else the cell's planned duration and watchdogMargin. A cell bounded by
// time plans ten times BenchTime: testing.B's calibration calls, the prepare
// and the cleanup took up to 6.3 times it in the preliminary sweeps. A cell
// bounded by a count, -test.benchtime=<n>x, plans 0.1 s an iteration, about
// eight times the slowest 4 MiB write rate they saw.
func (c Config) Limit(cell []string) time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	planned := 10 * c.BenchTime
	for _, arg := range cell {
		if m := countArg.FindStringSubmatch(arg); m != nil {
			n, err := strconv.ParseInt(m[1], 10, 64)
			if err == nil {
				planned = time.Duration(n/10) * time.Second
			}
		}
	}
	return planned + watchdogMargin
}

// floorLimit is the limit of a floor run window seconds long: FloorTimeout
// when set, else the window and watchdogMargin.
func (c Config) floorLimit(window int64) time.Duration {
	if c.FloorTimeout > 0 {
		return c.FloorTimeout
	}
	return time.Duration(window)*time.Second + watchdogMargin
}

// BenchArgs are the benchmark binary's arguments for cell in mode, appending
// its cells to results unless that is empty.
func (c Config) BenchArgs(mode, results string, cell []string) []string {
	// A finite -test.timeout, beyond the watchdog's limit and grace so that
	// the watchdog stays the bound: go1.27.1 stops the timeout's alarm before
	// it runs benchmarks, and a Go that did apply it would fire last.
	args := []string{
		"-test.run", "^$", "-test.bench", "BenchmarkSeam",
		"-test.timeout", seconds(c.Limit(cell) + watchdogGrace + time.Minute),
	}
	if !c.Limiter {
		// In whole seconds with their unit: go test refuses a benchtime of
		// plain seconds, which BENCH_TIME accepts.
		args = append(args, "-test.benchtime", seconds(c.BenchTime))
	}
	args = append(args, "-mode="+mode)
	if results != "" {
		args = append(args, "-results="+results)
	}
	return append(args, cell...)
}
