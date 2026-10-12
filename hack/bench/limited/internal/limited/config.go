// Package limited re-runs the seam microbenchmark's over-budget cell under
// the in-flight limiter: write4m at 64 in flight past the byte budget, in
// each completion mode, each in a benchmark process of its own, with the
// limiter's byte bound set and its operation bound derived, as rgw-go runs.
// It is the evidence that the limiter parks the excess submissions on
// goroutines rather than on OS threads blocked inside C.
package limited

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInflightBytes is librados's default objecter_inflight_op_bytes,
	// 100 MiB.
	DefaultInflightBytes = 104857600

	defaultOverObjects = 640
	quickOverObjects   = 128
	defaultMinAvailGiB = 5

	// A stopped benchmark process gets watchdogGrace between SIGTERM and
	// SIGKILL; every planned duration gets watchdogMargin more.
	watchdogGrace  = 30 * time.Second
	watchdogMargin = 300 * time.Second

	// maxSeconds caps every duration so that ten times it, the limit of a
	// cell bounded by time, still fits go test's -test.timeout, a Go
	// duration.
	maxSeconds = 99999999

	// pool is the pool the benchmark writes to, cephtest.TestPool.
	pool = "rgw-go-test"
)

// overCell is the over-budget cell, write4m at 64 in flight, 256 MiB, past
// the byte budget, as hack/bench/seam.sh runs it.
var overCell = []string{"-shapes=write4m", "-conc=64", "-budget-bytes=0"}

// modes are the completion modes, each run in a benchmark process of its
// own: the runtime keeps the OS threads a process grows, and go-ceph's
// completion notifier is process-wide.
var modes = []string{"sync", "callback", "pipe"}

var (
	// At most 18 digits, so that the bound fits -inflight-bytes, an int64;
	// no leading zero, so that no spelling of the default names another
	// directory.
	bytesPattern = regexp.MustCompile(`^[1-9]\d{0,17}$`)
	// At most 10 digits, so that a count-bounded cell's limit, a tenth of a
	// second an object, fits go test's -test.timeout.
	countPattern   = regexp.MustCompile(`^[1-9]\d{0,9}$`)
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
}

// Config is a run's checked settings.
type Config struct {
	Release       string
	Repo          string
	Rooket        string
	GoTags        string
	InflightBytes int64
	OverObjects   int64
	// Timeout is each benchmark process's limit; zero derives it from the
	// cell.
	Timeout     time.Duration
	MinAvailGiB int64
	Plan        bool
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
	switch {
	case s.InflightBytes != "" && s.Limiter != "1":
		return Config{}, errors.New("BENCH_INFLIGHT_BYTES applies only with BENCH_LIMITER=1")
	case s.Plan == "1" && s.Limiter != "1":
		return Config{}, errors.New("BENCH_PLAN=1 prints the limiter re-run only: set BENCH_LIMITER=1")
	case s.Limiter != "1":
		return Config{}, errors.New("the limiter re-run needs BENCH_LIMITER=1")
	}
	repo, err := filepath.Abs(cmp.Or(s.Repo, "."))
	if err != nil {
		return Config{}, fmt.Errorf("resolving the repository: %w", err)
	}
	cfg := Config{
		Release:       s.Release,
		Repo:          repo,
		Rooket:        cmp.Or(s.Rooket, "rooket"),
		GoTags:        cmp.Or(s.GoTags, "ceph_preview"),
		InflightBytes: DefaultInflightBytes,
		OverObjects:   defaultOverObjects,
		MinAvailGiB:   defaultMinAvailGiB,
		Plan:          s.Plan == "1",
	}
	if s.Quick == "1" {
		cfg.OverObjects = quickOverObjects
	}
	if s.InflightBytes != "" {
		if cfg.InflightBytes, err = parseMatching(s.InflightBytes, bytesPattern); err != nil {
			return Config{}, fmt.Errorf("BENCH_INFLIGHT_BYTES is %s: give a positive whole number of bytes, at most 18 digits", s.InflightBytes)
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
	return cfg, nil
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
// would stop every process at once, and anything over maxSeconds.
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

// clusterOut is the release's rooket output, which holds its ceph.conf and
// the image its cluster runs.
func (c Config) clusterOut() string {
	return filepath.Join(c.Repo, "hack", "rooket", "out", c.Release)
}

// CephConf is the release's ceph.conf, through which the benchmark reaches
// its cluster.
func (c Config) CephConf() string {
	return filepath.Join(c.clusterOut(), "ceph.conf")
}

// Suffix ends the run directory's name: -limited, and the byte bound when it
// is not the default, so that runs at two bounds never share a name.
func (c Config) Suffix() string {
	if c.InflightBytes == DefaultInflightBytes {
		return "-limited"
	}
	return "-limited-" + strconv.FormatInt(c.InflightBytes, 10)
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
	return append(append([]string{}, overCell...),
		"-inflight-bytes="+strconv.FormatInt(c.InflightBytes, 10),
		"-test.benchtime="+strconv.FormatInt(c.OverObjects, 10)+"x")
}

// Limit is each benchmark process's limit: Timeout when set, else the cell's
// planned duration and watchdogMargin. A cell bounded by a count plans 0.1 s
// an iteration, about eight times the slowest 4 MiB write rate the
// preliminary sweeps saw.
func (c Config) Limit() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return time.Duration(c.OverObjects/10)*time.Second + watchdogMargin
}

// BenchArgs are the benchmark binary's arguments for mode, appending its
// cells to results.
func (c Config) BenchArgs(mode, results string) []string {
	// A finite -test.timeout, beyond the watchdog's limit and grace so that
	// the watchdog stays the bound: go1.27.1 stops the timeout's alarm before
	// it runs benchmarks, and a Go that did apply it would fire last.
	timeout := c.Limit() + watchdogGrace + time.Minute
	args := []string{
		"-test.run", "^$", "-test.bench", "BenchmarkSeam",
		"-test.timeout", strconv.FormatInt(int64(timeout/time.Second), 10) + "s",
		"-mode=" + mode, "-results=" + results,
	}
	return append(args, c.CellArgs()...)
}

// WritePlan writes the run's plan to w: the run directory and, per mode, the
// cell's arguments, its results file and its process's limit.
func (c Config) WritePlan(w io.Writer) {
	fmt.Fprintf(w, "limited: the %s limiter re-run at %d B, into hack/bench/out/%s/seam-<UTC time>%s/\n",
		c.Release, c.InflightBytes, c.Release, c.Suffix())
	limit := int64(c.Limit() / time.Second)
	for _, mode := range modes {
		fmt.Fprintf(w, "bench %s: %s -> overbudget-%s.jsonl (limit %d s)\n", mode, strings.Join(c.CellArgs(), " "), mode, limit)
	}
}
