// Package gateway runs the gateway comparison's profiles against one gateway
// of one release's populated rooket cluster, radosgw or the rgw-go on the
// host, and renders the run's REPORT.md with hack/bench/report.
package gateway

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Profiles are the comparison's profiles, in the order a run without any
// named takes them.
var Profiles = []string{"small-put", "small-get", "noop", "large-put", "large-get", "list"}

const (
	objSmall = 4 << 10
	objLarge = 64 << 20

	defaultSeconds      = 60
	defaultPutObjects   = 3000
	defaultGetObjects   = 400
	defaultLargeThreads = 32
	defaultLargeObjects = 1
	defaultListThreads  = 64
	defaultListObjects  = 1600

	// A smoke run only proves the tooling: its numbers measure nothing.
	smokeSeconds      = 5
	smokeGetObjects   = 50
	smokeLargeThreads = 4
	smokeListObjects  = 16
)

var (
	defaultConc = []int64{1, 8, 32, 64, 128, 256}
	smokeConc   = []int64{8}
)

var (
	// At most 9 digits, so that a phase's bound in nanoseconds and a listing's
	// keys, threads times keys a thread, fit an int64.
	countPattern = regexp.MustCompile(`^[1-9]\d{0,8}$`)
	// At most 18 digits, so that the bytes fit an int64.
	bytesPattern = regexp.MustCompile(`^[1-9]\d{0,17}$`)
)

// Settings are a run's inputs as given, before they are checked. An empty
// string is a setting left unset.
type Settings struct {
	Release string
	Gateway string
	// Profiles are the profiles to run, in order; none runs them all.
	Profiles []string
	// Smoke and Plan are 1 or unset, as BENCH_SMOKE and BENCH_PLAN.
	Smoke string
	Plan  string
	// IdleAvail is the data pool's free bytes a plan is judged against.
	IdleAvail    string
	Seconds      string
	Conc         string
	PutObjects   string
	GetObjects   string
	LargeThreads string
	LargeObjects string
	ListThreads  string
	ListObjects  string
	RgwGoFlags   string
	GoTags       string
	Rooket       string
	Repo         string
	// ConfigFile is the config file the settings were read from, empty when
	// none was.
	ConfigFile string
}

// Config is a run's checked settings.
type Config struct {
	Release  string
	Gateway  string
	Profiles []string
	Smoke    bool
	Plan     bool
	// IdleAvail is the data pool's free bytes a plan is judged against.
	IdleAvail int64
	// Seconds bounds every measured phase.
	Seconds int64
	// Conc are the small and noop profiles' concurrencies, in order.
	Conc         []int64
	PutObjects   int64
	GetObjects   int64
	LargeThreads int64
	LargeObjects int64
	ListThreads  int64
	ListObjects  int64
	// RgwGoFlags is RGW_GO_FLAGS as given, which env.json records, and
	// RgwGoArgs its words, the flags rgw-go-up.sh passes rgw-go serve.
	RgwGoFlags string
	RgwGoArgs  []string
	GoTags     string
	Rooket     string
	Repo       string
	// ConfigFile is the config file the settings were read from, absolute,
	// or empty when none was.
	ConfigFile string
}

// Check checks every setting and returns the run's Config, or the first
// setting it refuses. It touches no cluster and creates nothing.
func (s Settings) Check() (Config, error) {
	if s.Release != "squid" && s.Release != "tentacle" {
		return Config{}, fmt.Errorf("release %q is not squid or tentacle", s.Release)
	}
	if s.Gateway != "radosgw" && s.Gateway != "rgw-go" {
		return Config{}, fmt.Errorf("gateway %q is not radosgw or rgw-go", s.Gateway)
	}
	profiles := slices.Clone(s.Profiles)
	if len(profiles) == 0 {
		profiles = slices.Clone(Profiles)
	}
	for i, p := range profiles {
		if !slices.Contains(Profiles, p) {
			return Config{}, fmt.Errorf("no profile %s; the profiles are %s", p, strings.Join(Profiles, " "))
		}
		// A profile run twice would append a second run's phases to the same
		// results.
		if slices.Contains(profiles[:i], p) {
			return Config{}, fmt.Errorf("profile %s is given twice", p)
		}
	}
	for _, sw := range []struct{ name, value string }{{"BENCH_SMOKE", s.Smoke}, {"BENCH_PLAN", s.Plan}} {
		if sw.value != "" && sw.value != "1" {
			return Config{}, fmt.Errorf("%s is %s: set it to 1 or leave it unset", sw.name, sw.value)
		}
	}
	repo, rerr := filepath.Abs(cmp.Or(s.Repo, "."))
	if rerr != nil {
		return Config{}, fmt.Errorf("resolving the repository: %w", rerr)
	}
	cfg := Config{
		Release:    s.Release,
		Gateway:    s.Gateway,
		Profiles:   profiles,
		Smoke:      s.Smoke == "1",
		Plan:       s.Plan == "1",
		RgwGoFlags: s.RgwGoFlags,
		GoTags:     cmp.Or(s.GoTags, "ceph_preview"),
		Rooket:     cmp.Or(s.Rooket, "rooket"),
		Repo:       repo,
	}
	if words := strings.Fields(s.RgwGoFlags); len(words) > 0 {
		cfg.RgwGoArgs = words
	}
	if s.ConfigFile != "" {
		if cfg.ConfigFile, rerr = filepath.Abs(s.ConfigFile); rerr != nil {
			return Config{}, fmt.Errorf("resolving the config file: %w", rerr)
		}
	}
	if err := s.checkCounts(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.checkDatasets(); err != nil {
		return Config{}, err
	}
	switch {
	case cfg.Plan:
		if s.IdleAvail == "" {
			return Config{}, errors.New("BENCH_PLAN=1 needs BENCH_IDLE_AVAIL, the data pool's free bytes")
		}
		idle, err := parseMatching(s.IdleAvail, bytesPattern)
		if err != nil {
			return Config{}, fmt.Errorf("BENCH_IDLE_AVAIL is %s: give the data pool's free bytes, a positive whole number of at most 18 digits",
				s.IdleAvail)
		}
		cfg.IdleAvail = idle
	case s.IdleAvail != "":
		// Without BENCH_PLAN=1 a run would start where a plan was meant.
		return Config{}, errors.New("BENCH_IDLE_AVAIL applies only with BENCH_PLAN=1")
	}
	return cfg, nil
}

// checkCounts checks the counts and the concurrencies into cfg, a smoke run's
// where unset, the defaults where still unset.
func (s Settings) checkCounts(cfg *Config) error {
	seconds, getObjects, largeThreads, listObjects := int64(defaultSeconds), int64(defaultGetObjects),
		int64(defaultLargeThreads), int64(defaultListObjects)
	conc := defaultConc
	if cfg.Smoke {
		seconds, getObjects, largeThreads, listObjects = smokeSeconds, smokeGetObjects, smokeLargeThreads, smokeListObjects
		conc = smokeConc
	}
	for _, count := range []struct {
		name, value string
		def         int64
		into        *int64
	}{
		{"BENCH_SECONDS", s.Seconds, seconds, &cfg.Seconds},
		{"BENCH_PUT_OBJECTS", s.PutObjects, defaultPutObjects, &cfg.PutObjects},
		{"BENCH_GET_OBJECTS", s.GetObjects, getObjects, &cfg.GetObjects},
		{"BENCH_LARGE_THREADS", s.LargeThreads, largeThreads, &cfg.LargeThreads},
		{"BENCH_LARGE_OBJECTS", s.LargeObjects, defaultLargeObjects, &cfg.LargeObjects},
		{"BENCH_LIST_THREADS", s.ListThreads, defaultListThreads, &cfg.ListThreads},
		{"BENCH_LIST_OBJECTS", s.ListObjects, listObjects, &cfg.ListObjects},
	} {
		*count.into = count.def
		if count.value == "" {
			continue
		}
		n, err := parseMatching(count.value, countPattern)
		if err != nil {
			return fmt.Errorf("%s is '%s', not a positive count of at most 9 digits", count.name, count.value)
		}
		*count.into = n
	}
	cfg.Conc = slices.Clone(conc)
	if s.Conc == "" {
		return nil
	}
	words := strings.Fields(s.Conc)
	if len(words) == 0 {
		return errors.New("BENCH_CONC names no concurrency")
	}
	cfg.Conc = nil
	for _, w := range words {
		n, err := parseMatching(w, countPattern)
		if err != nil {
			return fmt.Errorf("BENCH_CONC holds '%s', not a positive count of at most 9 digits", w)
		}
		// The profile's last phase, which rgw-go is profiled through, is its
		// last concurrency's; one given twice would be profiled twice.
		if slices.Contains(cfg.Conc, n) {
			return fmt.Errorf("BENCH_CONC holds %d twice", n)
		}
		cfg.Conc = append(cfg.Conc, n)
	}
	return nil
}

// checkDatasets refuses settings whose datasets, or the free space twice one
// needs, overflow an int64.
func (c Config) checkDatasets() error {
	for _, p := range Profiles {
		if _, ok := c.dataset(p); !ok {
			return fmt.Errorf("the dataset of %s overflows: lower its threads or objects", p)
		}
	}
	return nil
}

// parseMatching parses s as a decimal int64 once it matches pattern.
func parseMatching(s string, pattern *regexp.Regexp) (int64, error) {
	if !pattern.MatchString(s) {
		return 0, fmt.Errorf("%q does not match %s", s, pattern)
	}
	return strconv.ParseInt(s, 10, 64)
}

// Top is the highest of the concurrencies.
func (c Config) Top() int64 {
	return slices.Max(c.Conc)
}

// Dataset is the bytes of object data profile holds at once, from the
// threads, objects per thread and object size its commands use. A lower
// concurrency's keys are among the top one's, since elbencho names an object
// by its thread's rank and its index, r<rank>-f<index>; the large profiles
// remove each write's objects before the next; list's keys are empty.
func (c Config) Dataset(profile string) int64 {
	n, _ := c.dataset(profile)
	return n
}

// dataset is Dataset, and whether it and twice it fit an int64.
func (c Config) dataset(profile string) (int64, bool) {
	var factors []int64
	switch profile {
	case "small-put":
		factors = []int64{c.Top(), c.PutObjects, objSmall}
	case "small-get":
		factors = []int64{c.Top(), c.GetObjects, objSmall}
	case "large-put", "large-get":
		factors = []int64{c.LargeThreads, c.LargeObjects, objLarge}
	default:
		return 0, true
	}
	n := int64(1)
	for _, f := range factors {
		if n > math.MaxInt64/2/f {
			return 0, false
		}
		n *= f
	}
	return n, true
}

// WritePlan writes each profile's dataset and the free space it needs, twice
// the dataset, against free bytes, and reports whether every one fits.
func (c Config) WritePlan(w io.Writer, free int64) bool {
	fits := true
	for _, p := range c.Profiles {
		bytes := c.Dataset(p)
		need := 2 * bytes
		verdict := "fits"
		if need > free {
			verdict, fits = "DOES NOT FIT", false
		}
		fmt.Fprintf(w, "%-9s dataset %11d B, needs %11d B of %11d B free: %s\n", p, bytes, need, free, verdict)
	}
	return fits
}

// clusterOut is the release's rooket output: its ceph.conf, image,
// manifest.json and, while one runs, the rgw-go's endpoint, metrics address,
// pid and log.
func (c Config) clusterOut() string {
	return filepath.Join(c.Repo, "hack", "rooket", "out", c.Release)
}

// Dir is the run directory for a run started at t.
func (c Config) Dir(t time.Time) string {
	return filepath.Join(c.Repo, "hack", "bench", "out", c.Release,
		"gateway-"+c.Gateway+"-"+t.UTC().Format("20060102T150405Z"))
}

// concWords is the concurrencies as BENCH_CONC writes them.
func (c Config) concWords() string {
	words := make([]string, len(c.Conc))
	for i, n := range c.Conc {
		words[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(words, " ")
}
