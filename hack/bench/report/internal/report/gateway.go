package report

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GatewayReport is a gateway comparison rendered from the run directories
// hack/bench/gateway writes.
type GatewayReport struct {
	Markdown string
	// Findings names every phase that had an error or a retry, each a finding
	// about the harness or the gateway rather than noise.
	Findings []string
}

// Summary is what the command prints after the report's path.
func (r GatewayReport) Summary() string {
	switch n := len(r.Findings); n {
	case 0:
		return "no phase had an error or a retry"
	case 1:
		return "1 phase had errors or retries"
	default:
		return fmt.Sprintf("%d phases had errors or retries", n)
	}
}

// gwTable is one table of the report and the profiles whose phases it shows:
// the noop baseline's rows sit beside small-get's at the same concurrency.
type gwTable struct {
	name     string
	profiles []string
}

var gwTables = []gwTable{
	{"small-put", []string{"small-put"}},
	{"small-get and noop", []string{"small-get", "noop"}},
	{"large-put", []string{"large-put"}},
	{"large-get", []string{"large-get"}},
	{"list", []string{"list"}},
}

// opt is a value a phase may lack.
type opt struct {
	v  float64
	ok bool
}

func some(v float64) opt { return opt{v, true} }

// gwPhase is one measured phase: an elbencho phase or an httpload run.
type gwPhase struct {
	profile string
	variant string
	threads int
	start   time.Time
	elapsed time.Duration
	// requests is the S3 requests the phase made: elbencho's IOs, or for a
	// listing, which counts none, its entries histogram's count.
	requests float64
	objects  opt
	bytes    opt
	// lat is p50, p99 and p999 in µs.
	lat                     [3]opt
	errors                  int64
	errorKinds              string
	retries                 opt
	retryKinds              string
	cpuMsPerRequest         opt
	peakThreads, peakRSSKiB opt
	command                 string
}

// gcStats is what a gctrace log says about the GC cycles of one profile. A
// torn line, kept or known from a gap in the cycles' numbers, counts among the
// cycles and adds nothing to the pauses.
type gcStats struct {
	cycles       int
	torn         int
	maxPauseUs   float64
	totalPauseUs float64
}

type gwEntity struct {
	Source  string            `json:"source"`
	Options map[string]string `json:"options"`
}

// gwRun is one run directory read.
type gwRun struct {
	dir, label, gateway, release string
	env                          map[string]any
	envKeys                      []string
	config                       map[string]gwEntity
	phases                       []gwPhase
	gc                           map[string]gcStats
	profiles                     []string
}

// gwComparedSettings are the env.json keys two runs must agree on to be
// compared: what each phase ran, the client and the packages it was built
// with, libcurl among them, the host and its kernel, and the TCP_NODELAY the
// gateway answered with.
var gwComparedSettings = []string{
	"ceph_version", "seconds", "conc", "put_objects", "get_objects", "large_threads", "large_objects",
	"list_threads", "list_objects", "smoke", "elbencho_commit", "elbencho_image", "elbencho_packages",
	"tcp_nodelay", "nproc", "cpu", "mem_total_kib", "kernel", "gomaxprocs",
}

// Gateway reads the run directories dirs, in order, and renders them side by
// side: per profile, a table of throughput and latency and one of cost, with
// a column per run for each measure and, beside every run but the first
// radosgw one, its ratio to that run. Every directory needs env.json, naming
// the release and the gateway, and the results of at least one profile;
// runs of different releases, of different profiles, or under different
// settings are refused.
func Gateway(ctx context.Context, dirs []string) (GatewayReport, error) {
	if len(dirs) == 0 {
		return GatewayReport{}, errors.New("no run directory")
	}
	runs := make([]*gwRun, 0, len(dirs))
	for _, d := range dirs {
		r, err := readGatewayRun(ctx, d)
		if err != nil {
			return GatewayReport{}, err
		}
		if len(runs) > 0 && r.release != runs[0].release {
			return GatewayReport{}, fmt.Errorf("runs of %s and %s cannot be compared: %s, %s", runs[0].release, r.release, runs[0].dir, d)
		}
		if len(runs) > 0 {
			if diffs := runs[0].differences(r); len(diffs) > 0 {
				return GatewayReport{}, fmt.Errorf("%s and %s ran under different settings: %s", runs[0].dir, d, strings.Join(diffs, "; "))
			}
		}
		runs = append(runs, r)
	}
	labelRuns(runs)
	g := &gwRender{runs: runs, baseline: -1}
	for i, r := range runs {
		if r.gateway == "radosgw" {
			g.baseline = i
			break
		}
	}
	rep := GatewayReport{Findings: g.findings()}
	rep.Markdown = g.render(rep.Findings)
	return rep, nil
}

// labelRuns names each run by its gateway, and by its directory too where two
// runs share a gateway.
func labelRuns(runs []*gwRun) {
	count := map[string]int{}
	for _, r := range runs {
		count[r.gateway]++
	}
	for _, r := range runs {
		r.label = r.gateway
		if count[r.gateway] > 1 {
			r.label = r.gateway + " (" + filepath.Base(r.dir) + ")"
		}
	}
}

func readGatewayRun(ctx context.Context, dir string) (*gwRun, error) {
	r := &gwRun{dir: dir, gc: map[string]gcStats{}}
	if err := r.readEnv(); err != nil {
		return nil, err
	}
	if err := r.readConfig(); err != nil {
		return nil, err
	}
	for _, p := range []string{"small-put", "small-get", "large-put", "large-get", "list"} {
		if err := r.readElbencho(p); err != nil {
			return nil, err
		}
	}
	if err := r.readNoop(); err != nil {
		return nil, err
	}
	if len(r.phases) == 0 {
		return nil, fmt.Errorf("%s holds no profile results: no <profile>.json from elbencho and no noop-<t>.json from httpload", dir)
	}
	profiles := map[string]bool{}
	for i := range r.phases {
		profiles[r.phases[i].profile] = true
	}
	for _, p := range slices.Sorted(maps.Keys(profiles)) {
		if err := r.readSamples(p); err != nil {
			return nil, err
		}
		if err := r.readGCTrace(p); err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(dir, "cpu-"+p+".pprof")); err == nil {
			r.profiles = append(r.profiles, "cpu-"+p+".pprof")
		}
	}
	slog.DebugContext(ctx, "read gateway run", slog.String("dir", dir), slog.String("gateway", r.gateway), slog.Int("phases", len(r.phases)))
	return r, nil
}

func (r *gwRun) readEnv() error {
	data, err := os.ReadFile(filepath.Join(r.dir, "env.json"))
	if err != nil {
		return fmt.Errorf("%s is not a gateway run: %w", r.dir, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&r.env); err != nil {
		return fmt.Errorf("parsing %s/env.json: %w", r.dir, err)
	}
	r.envKeys = slices.Sorted(maps.Keys(r.env))
	r.release, r.gateway = r.envString("release"), r.envString("gateway")
	if r.release == "" || r.gateway == "" {
		return fmt.Errorf("%s/env.json names no release or no gateway", r.dir)
	}
	return nil
}

// envString is env.json's string at key, or empty.
func (r *gwRun) envString(key string) string {
	if s, ok := r.env[key].(string); ok {
		return s
	}
	return ""
}

// differences lists, as "key: this against that", the profiles and the
// compared settings in which run o differs from r; a setting one run lacks
// reads unset.
func (r *gwRun) differences(o *gwRun) []string {
	var out []string
	if a, b := r.profileNames(), o.profileNames(); a != b {
		out = append(out, fmt.Sprintf("profiles: %s against %s", a, b))
	}
	for _, k := range gwComparedSettings {
		if a, b := r.setting(k), o.setting(k); a != b {
			out = append(out, fmt.Sprintf("%s: %s against %s", k, a, b))
		}
	}
	return out
}

// profileNames lists the profiles the run has results of.
func (r *gwRun) profileNames() string {
	var names []string
	for i := range r.phases {
		if p := r.phases[i].profile; !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func (r *gwRun) setting(key string) string {
	v, ok := r.env[key]
	if !ok {
		return "unset"
	}
	return envValue(v)
}

// readConfig reads config.json, which a run may lack.
func (r *gwRun) readConfig() error {
	data, err := os.ReadFile(filepath.Join(r.dir, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &r.config); err != nil {
		return fmt.Errorf("parsing %s/config.json: %w", r.dir, err)
	}
	return nil
}

// jnum is a number in elbencho's JSON, which writes every value as a string,
// as boost's property tree does.
type jnum float64

func (n *jnum) UnmarshalJSON(b []byte) error {
	s := string(b)
	if unq, err := strconv.Unquote(s); err == nil {
		s = unq
	}
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("%q is not a number", s)
	}
	*n = jnum(f)
	return nil
}

type elbenchoHistogram struct {
	Histogram struct {
		Buckets []struct {
			Range string `json:"range"`
			Count jnum   `json:"count"`
		} `json:"buckets"`
	} `json:"histogram"`
}

type elbenchoCounts struct {
	Total  jnum            `json:"total"`
	ByKind map[string]jnum `json:"by_kind"`
}

// kinds renders the counts by kind: http_503=40, timeout=2.
func (c *elbenchoCounts) kinds() string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(c.ByKind)) {
		out = append(out, fmt.Sprintf("%s=%d", k, int64(c.ByKind[k])))
	}
	return strings.Join(out, ", ")
}

// elbenchoPhase is one line of elbencho's --jsonfile, one per phase.
type elbenchoPhase struct {
	PhaseType string `json:"phase_type"`
	Start     string `json:"iso_start_date"`
	Config    struct {
		Threads   jnum   `json:"threads"`
		FileSize  jnum   `json:"file_size"`
		BlockSize jnum   `json:"block_size"`
		Command   string `json:"command"`
	} `json:"config"`
	LastDone struct {
		ElapsedMS jnum `json:"elapsed_time_ms"`
		Entries   jnum `json:"entries"`
		IOs       jnum `json:"ios"`
		Bytes     jnum `json:"bytes"`
		Latency   struct {
			Entries elbenchoHistogram `json:"entries"`
			IO      elbenchoHistogram `json:"IO"`
		} `json:"latency"`
		Errors  *elbenchoCounts `json:"errors"`
		Retries *elbenchoCounts `json:"retries"`
	} `json:"last_done"`
}

// elbenchoStart is iso_start_date's layout, local time with its offset.
const elbenchoStart = "2006-01-02T15:04:05.000-0700"

// readElbencho reads <profile>.json, which a run that skipped the profile
// lacks.
func (r *gwRun) readElbencho(profile string) error {
	path := filepath.Join(r.dir, profile+".json")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return readLines(path, func(e elbenchoPhase) error {
		ph, err := e.phase(profile)
		if err != nil {
			return err
		}
		r.phases = append(r.phases, ph)
		return nil
	})
}

func (e *elbenchoPhase) phase(profile string) (gwPhase, error) {
	ld := &e.LastDone
	start, err := time.Parse(elbenchoStart, e.Start)
	if err != nil {
		return gwPhase{}, fmt.Errorf("iso_start_date: %w", err)
	}
	if ld.ElapsedMS <= 0 {
		return gwPhase{}, fmt.Errorf("a %s phase without an elapsed time", e.PhaseType)
	}
	ph := gwPhase{
		profile: profile,
		variant: e.variant(),
		threads: int(e.Config.Threads),
		start:   start,
		elapsed: time.Duration(float64(ld.ElapsedMS) * float64(time.Millisecond)),
		retries: some(0),
		command: e.Config.Command,
	}
	// A listing counts the keys it lists as entries and no IOs; its
	// latencies are its requests', in the entries histogram.
	hist := ld.Latency.IO
	if len(hist.Histogram.Buckets) == 0 {
		hist = ld.Latency.Entries
	}
	var counted float64
	ph.lat, counted, err = percentiles(hist)
	if err != nil {
		return gwPhase{}, err
	}
	ph.requests = float64(ld.IOs)
	if ph.requests == 0 {
		ph.requests = counted
	}
	if ld.Entries > 0 {
		ph.objects = some(float64(ld.Entries))
	}
	if ld.Bytes > 0 {
		ph.bytes = some(float64(ld.Bytes))
	}
	if ld.Errors != nil {
		ph.errors = int64(ld.Errors.Total)
		ph.errorKinds = ld.Errors.kinds()
	}
	if ld.Retries != nil {
		ph.retries = some(float64(ld.Retries.Total))
		ph.retryKinds = ld.Retries.kinds()
	}
	return ph, nil
}

// variant names what a phase did, from elbencho's phase type and sizes: a
// block smaller than the object makes a PUT a multipart upload and a GET a
// series of ranged reads.
func (e *elbenchoPhase) variant() string {
	size, block := int64(e.Config.FileSize), int64(e.Config.BlockSize)
	split := block > 0 && block < size
	switch e.PhaseType {
	case "WRITE":
		// elbencho counts one IO per UploadPart, and none for the upload's
		// Create and Complete, so the row's rate is of parts.
		if split {
			return fmt.Sprintf("PUT %s in %s parts (parts/s)", sizeName(size), sizeName(block))
		}
		return "PUT " + sizeName(size)
	case "READ":
		if split {
			return fmt.Sprintf("GET %s in %s ranges", sizeName(size), sizeName(block))
		}
		return "GET " + sizeName(size)
	case "LISTOBJ":
		return "ListObjectsV2"
	case "LISTOBJ_P":
		return "ListObjectsV2, a prefix per thread"
	}
	return e.PhaseType
}

// sizeName renders a size in the largest binary unit that divides it.
func sizeName(n int64) string {
	for _, u := range []struct {
		shift uint
		name  string
	}{{30, "GiB"}, {20, "MiB"}, {10, "KiB"}} {
		if n > 0 && n%(1<<u.shift) == 0 {
			return fmt.Sprintf("%d %s", n>>u.shift, u.name)
		}
	}
	return fmt.Sprintf("%d B", n)
}

// bucketRange is a histogram bucket's range, start-endus, or start+us for the
// open-ended last bucket.
var bucketRange = regexp.MustCompile(`^(\d+)(?:-(\d+)|\+)us$`)

// percentiles returns p50, p99 and p999 of elbencho's latency histogram in
// µs, as its --latpercent prints them: the upper bound of the first bucket
// whose cumulative count reaches the percentage of all counted. It also
// returns how many values the histogram counted.
func percentiles(h elbenchoHistogram) (p [3]opt, total float64, err error) {
	type bucket struct{ upper, count float64 }
	buckets := make([]bucket, 0, len(h.Histogram.Buckets))
	for _, b := range h.Histogram.Buckets {
		m := bucketRange.FindStringSubmatch(b.Range)
		if m == nil {
			return p, 0, fmt.Errorf("latency histogram range %q is not start-endus", b.Range)
		}
		bound := m[2]
		if bound == "" {
			bound = m[1]
		}
		upper, err := strconv.ParseFloat(bound, 64)
		if err != nil {
			return p, 0, err
		}
		buckets = append(buckets, bucket{upper, float64(b.Count)})
		total += float64(b.Count)
	}
	if total == 0 {
		return p, 0, nil
	}
	for i, pct := range []float64{50, 99, 99.9} {
		var sofar float64
		for _, b := range buckets {
			sofar += b.count
			if sofar/total >= pct/100 {
				p[i] = some(b.upper)
				break
			}
		}
	}
	return p, total, nil
}

// httploadResult is what hack/bench/httpload writes.
type httploadResult struct {
	Threads   int     `json:"threads"`
	DurationS float64 `json:"duration_s"`
	Requests  int64   `json:"requests"`
	Errors    int64   `json:"errors"`
	LatUs     struct {
		P50  float64 `json:"p50"`
		P99  float64 `json:"p99"`
		P999 float64 `json:"p999"`
	} `json:"lat_us"`
	Start   time.Time `json:"start"`
	Command string    `json:"command"`
}

// readNoop reads every noop-<t>.json, one httpload run each.
func (r *gwRun) readNoop() error {
	files, err := filepath.Glob(filepath.Join(r.dir, "noop-*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // a result in the directory the command line names
		if err != nil {
			return err
		}
		var h httploadResult
		if err := json.Unmarshal(data, &h); err != nil {
			return fmt.Errorf("parsing %s: %w", f, err)
		}
		if h.Threads < 1 || h.DurationS <= 0 {
			return fmt.Errorf("%s has no threads or no duration", f)
		}
		r.phases = append(r.phases, gwPhase{
			profile:  "noop",
			variant:  "anonymous GET /",
			threads:  h.Threads,
			start:    h.Start,
			elapsed:  time.Duration(h.DurationS * float64(time.Second)),
			requests: float64(h.Requests),
			lat:      [3]opt{some(h.LatUs.P50), some(h.LatUs.P99), some(h.LatUs.P999)},
			errors:   h.Errors,
			command:  h.Command,
		})
	}
	return nil
}

// sample is one line of samples-<profile>.csv, the gateway command's
// sampler's.
type sample struct {
	t, cpu, threads, rssKiB float64
}

// readSamples reads samples-<profile>.csv, when the run has one, and gives
// each of the profile's phases its CPU per request and its peaks.
func (r *gwRun) readSamples(profile string) error {
	path := filepath.Join(r.dir, "samples-"+profile+".csv")
	data, err := os.ReadFile(path) //nolint:gosec // a result in the directory the command line names
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	samples, err := parseSamples(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for i := range r.phases {
		if ph := &r.phases[i]; ph.profile == profile {
			ph.cpuMsPerRequest, ph.peakThreads, ph.peakRSSKiB = around(samples, ph)
		}
	}
	return nil
}

func parseSamples(data []byte) ([]sample, error) {
	records, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("no header")
	}
	col := map[string]int{}
	for i, name := range records[0] {
		col[name] = i
	}
	for _, name := range []string{"time", "utime", "stime", "threads", "rss_kib"} {
		if _, ok := col[name]; !ok {
			return nil, fmt.Errorf("no %s column", name)
		}
	}
	out := make([]sample, 0, len(records)-1)
	for n, rec := range records[1:] {
		var v [5]float64
		for i, name := range []string{"time", "utime", "stime", "threads", "rss_kib"} {
			f, err := strconv.ParseFloat(rec[col[name]], 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: %s: %w", n+2, name, err)
			}
			v[i] = f
		}
		out = append(out, sample{t: v[0], cpu: v[1] + v[2], threads: v[3], rssKiB: v[4]})
	}
	slices.SortFunc(out, func(a, b sample) int { return cmp.Compare(a.t, b.t) })
	return out, nil
}

// around reads a phase from the samples: its CPU time, user and system,
// interpolated between the samples around its start and around its end, per
// request in ms, and the highest thread count and RSS from the last sample at
// or before its start to the first at or after its end. Without samples on
// both sides there is no CPU, and the peaks are the samples' within it.
func around(samples []sample, ph *gwPhase) (cpu, threads, rss opt) {
	start := float64(ph.start.UnixNano()) / 1e9
	end := start + ph.elapsed.Seconds()
	// before is the last sample at or before the start, after the first at or
	// after the end, and within the samples between them; -1 for none.
	before, after := -1, -1
	var within []int
	for i, s := range samples {
		switch {
		case s.t <= start:
			before = i
		case s.t >= end:
			if after < 0 {
				after = i
			}
		default:
			within = append(within, i)
		}
	}
	bracketed := before >= 0 && after >= 0
	peaks := within
	if bracketed {
		peaks = append([]int{before}, append(within, after)...)
	}
	for _, i := range peaks {
		threads = some(max(threads.v, samples[i].threads))
		rss = some(max(rss.v, samples[i].rssKiB))
	}
	if bracketed && ph.requests > 0 {
		used := interpolate(samples, end) - interpolate(samples, start)
		cpu = some(used * 1000 / ph.requests)
	}
	return cpu, threads, rss
}

// interpolate is the CPU time at t, which the samples bracket.
func interpolate(samples []sample, t float64) float64 {
	i := slices.IndexFunc(samples, func(s sample) bool { return s.t >= t })
	if i <= 0 {
		return samples[max(i, 0)].cpu
	}
	a, b := samples[i-1], samples[i]
	return a.cpu + (b.cpu-a.cpu)*(t-a.t)/(b.t-a.t)
}

// gcLine is a GODEBUG=gctrace=1 line: its wall-clock phases are the sweep
// termination's stop-the-world pause, the concurrent mark and the mark
// termination's stop-the-world pause, in ms. The cycle of a goroutine leak
// check, which a fetch of /debug/pprof/goroutineleak runs, says so before the
// colon.
var gcLine = regexp.MustCompile(`^gc \d+ @[0-9.]+s \d+%(?: \(checking for goroutine leaks\))?: ([0-9.]+)\+[0-9.]+\+([0-9.]+) ms clock`)

// gcStart is how a GC line begins, with its cycle's number, the rule
// hack/bench/gateway's keepGCTrace keeps a line of rgw-go's log by.
var gcStart = regexp.MustCompile(`^gc (\d+) @`)

// readGCTrace reads gctrace-<profile>.log, when the run has one. A line that
// begins as a GC line but is not a whole one is torn, ended early by a record
// of rgw-go's JSON log, as keepGCTrace's doc tells, or by the end of the log
// it read: it counts as a cycle whose pauses are unknown. The cycles are
// counted from their numbers, consecutive in one rgw-go's log, from the first
// line's on: a gap is the cycles of lines torn too early for keepGCTrace to
// keep, each counted as a torn line.
func (r *gwRun) readGCTrace(profile string) error {
	path := filepath.Join(r.dir, "gctrace-"+profile+".log")
	data, err := os.ReadFile(path) //nolint:gosec // a result in the directory the command line names
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var g gcStats
	// Not a bufio.Scanner: a torn line holds a whole JSON record, which may
	// be longer than the longest token a Scanner takes.
	n, prev := 0, 0
	for line := range bytes.Lines(data) {
		n++
		line = bytes.TrimSuffix(line, []byte("\n"))
		start := gcStart.FindSubmatch(line)
		if start == nil {
			return fmt.Errorf("%s line %d is not a gctrace line", path, n)
		}
		cycle, err := strconv.Atoi(string(start[1]))
		if err != nil {
			return fmt.Errorf("%s line %d: %w", path, n, err)
		}
		switch {
		case n == 1:
			g.cycles = 1
		case cycle <= prev:
			return fmt.Errorf("%s line %d is cycle %d, which does not follow cycle %d", path, n, cycle, prev)
		default:
			g.cycles += cycle - prev
			g.torn += cycle - prev - 1
		}
		prev = cycle
		m := gcLine.FindSubmatch(line)
		if m == nil {
			g.torn++
			continue
		}
		for _, s := range m[1:] {
			ms, err := strconv.ParseFloat(string(s), 64)
			if err != nil {
				return fmt.Errorf("%s line %d: %w", path, n, err)
			}
			g.maxPauseUs = max(g.maxPauseUs, ms*1000)
			g.totalPauseUs += ms * 1000
		}
	}
	r.gc[profile] = g
	return nil
}
