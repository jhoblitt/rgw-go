package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const mib = 1 << 20

// Run is one measured run's data.
type Run struct {
	Load, Variant string
	Rep           int
	Dir           string
	Samples       []Sample
	// PSS holds radosgw's smaps_rollup, read inside its pod: time and the
	// Rss, Pss, Pss_Anon and Pss_File in kB.
	PSS   [][5]float64
	Marks map[string][2]float64
}

// metric reads one value, in MiB or a count, from a sample of a run.
type metric struct {
	Name string
	Get  func(r *Run, s Sample) (float64, bool)
}

func statusMiB(k string) func(*Run, Sample) (float64, bool) {
	return func(_ *Run, s Sample) (float64, bool) {
		v, ok := s.Status[k]
		return float64(v) / 1024, ok
	}
}

func cgMiB(k string) func(*Run, Sample) (float64, bool) {
	return func(_ *Run, s Sample) (float64, bool) {
		v, ok := s.Cg[k]
		return float64(v) / mib, ok
	}
}

func goMiB(k string) func(*Run, Sample) (float64, bool) {
	return func(_ *Run, s Sample) (float64, bool) {
		v, ok := s.Go[k]
		return v / mib, ok
	}
}

// pssAt is a sample's PSS: the sampler's own for rgw-go, the in-pod reading
// nearest in time, within 2 s, for radosgw.
func pssAt(r *Run, s Sample) (float64, bool) {
	if v, ok := s.Rollup["Pss"]; ok {
		return float64(v) / 1024, true
	}
	if len(r.PSS) == 0 {
		return 0, false
	}
	i := sort.Search(len(r.PSS), func(i int) bool { return r.PSS[i][0] >= s.T })
	best, bestD := -1, 2.0
	for _, j := range []int{i - 1, i} {
		if j >= 0 && j < len(r.PSS) {
			if d := math.Abs(r.PSS[j][0] - s.T); d <= bestD {
				best, bestD = j, d
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	return r.PSS[best][2] / 1024, true
}

var metrics = []metric{
	{"VmRSS", statusMiB("VmRSS")},
	{"RssAnon", statusMiB("RssAnon")},
	{"RssFile", statusMiB("RssFile")},
	{"PSS", pssAt},
	{"cg current", cgMiB("current")},
	{"working set", func(_ *Run, s Sample) (float64, bool) {
		c, ok := s.Cg["current"]
		return float64(c-s.Cg["inactive_file"]) / mib, ok
	}},
	{"cg anon", cgMiB("anon")},
	{"cg file", cgMiB("file")},
	{"memory.peak", cgMiB("peak")},
	{"threads", func(_ *Run, s Sample) (float64, bool) {
		v, ok := s.Status["Threads"]
		return float64(v), ok
	}},
	{"HeapInuse", goMiB("go_memstats_heap_inuse_bytes")},
	{"heap resident", func(_ *Run, s Sample) (float64, bool) {
		sys, ok := s.Go["go_memstats_heap_sys_bytes"]
		return (sys - s.Go["go_memstats_heap_released_bytes"]) / mib, ok
	}},
	{"HeapReleased", goMiB("go_memstats_heap_released_bytes")},
	{"Go sys", goMiB("go_memstats_sys_bytes")},
	{"Go limit", goMiB("go_gc_gomemlimit_bytes")},
	{"budget held", func(_ *Run, s Sample) (float64, bool) {
		var sum float64
		var ok bool
		for k, v := range s.Go {
			if strings.HasPrefix(k, "rgw_go_memory_budget_held_bytes{") {
				sum += v
				ok = true
			}
		}
		return sum / mib, ok
	}},
	{"free list", goMiB("rgw_go_memory_budget_free_list_bytes")},
	// The anonymous memory the Go runtime does not hold: tcmalloc's heap
	// (librados's buffers), C thread stacks and the rest, inferred.
	{"non-Go anon", func(_ *Run, s Sample) (float64, bool) {
		sys, ok := s.Go["go_memstats_sys_bytes"]
		if !ok {
			return 0, false
		}
		goRes := sys - s.Go["go_memstats_heap_released_bytes"]
		return float64(s.Status["RssAnon"])/1024 - goRes/mib, true
	}},
	{"GC cycles", func(_ *Run, s Sample) (float64, bool) {
		v, ok := s.Go["go_gc_duration_seconds_count"]
		return v, ok
	}},
}

func metricByName(name string) metric {
	for _, m := range metrics {
		if m.Name == name {
			return m
		}
	}
	panic("no metric " + name)
}

// window is the samples from t0 to t1.
func (r *Run) window(t0, t1 float64) []Sample {
	var out []Sample
	for _, s := range r.Samples {
		if s.T >= t0 && s.T <= t1 {
			out = append(out, s)
		}
	}
	return out
}

func values(r *Run, ss []Sample, m metric) []float64 {
	var out []float64
	for _, s := range ss {
		if v, ok := m.Get(r, s); ok {
			out = append(out, v)
		}
	}
	return out
}

func maxOf(v []float64) (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	return slices.Max(v), true
}

func median(v []float64) (float64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	c := slices.Clone(v)
	slices.Sort(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2], true
	}
	return (c[n/2-1] + c[n/2]) / 2, true
}

// at is the value of the sample nearest t, within 3 s.
func (r *Run) at(t float64, m metric) (float64, bool) {
	best, bestD := -1, 3.0
	for i, s := range r.Samples {
		if d := math.Abs(s.T - t); d <= bestD {
			if _, ok := m.Get(r, s); ok {
				best, bestD = i, d
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	return m.Get(r, r.Samples[best])
}

// Peak is the highest sample from the phase's start to 1 s past its end.
func (r *Run) Peak(phase string, m metric) (float64, bool) {
	w, ok := r.Marks[phase]
	if !ok {
		return 0, false
	}
	return maxOf(values(r, r.window(w[0], w[1]+1), m))
}

// Steady is the median of the samples in the phase's second half.
func (r *Run) Steady(phase string, m metric) (float64, bool) {
	w, ok := r.Marks[phase]
	if !ok {
		return 0, false
	}
	return median(values(r, r.window((w[0]+w[1])/2, w[1]), m))
}

// IdleAt is the sample p seconds after the idle phase started.
func (r *Run) IdleAt(p float64, m metric) (float64, bool) {
	w, ok := r.Marks["idle"]
	if !ok {
		return 0, false
	}
	return r.at(w[0]+p, m)
}

// Throughput is a phase's MiB/s and requests/s from its load driver's
// results and its marks.
type Throughput struct {
	Bytes, Requests, Errors int64
	Seconds                 float64
	PausedS                 int64
}

func (t Throughput) MiBps() float64 { return float64(t.Bytes) / mib / t.Seconds }
func (t Throughput) Rps() float64   { return float64(t.Requests) / t.Seconds }

var waveFile = regexp.MustCompile(`^(.+?)(-\d+)?\.json$`)

func (r *Run) Throughput(phase string) (Throughput, bool) {
	w, ok := r.Marks[phase]
	if !ok {
		return Throughput{}, false
	}
	t := Throughput{Seconds: w[1] - w[0]}
	if phase == "mix" {
		var m MixResult
		if err := readJSON(filepath.Join(r.Dir, "mix.json"), &m); err != nil {
			return Throughput{}, false
		}
		t.Bytes, t.Requests, t.Errors, t.PausedS = m.Bytes, m.Done, m.Errors, m.PausedS
		return t, true
	}
	ents, err := os.ReadDir(r.Dir)
	if err != nil {
		return Throughput{}, false
	}
	found := false
	for _, e := range ents {
		mm := waveFile.FindStringSubmatch(e.Name())
		if mm == nil || mm[1] != phase {
			continue
		}
		var res struct {
			Bytes, Requests, Errors int64
		}
		if err := readJSON(filepath.Join(r.Dir, e.Name()), &res); err != nil {
			continue
		}
		t.Bytes += res.Bytes
		t.Requests += res.Requests
		t.Errors += res.Errors
		found = true
	}
	return t, found
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// LoadRun reads runs/LOAD/VARIANT-REP.
func LoadRun(dir string) (*Run, error) {
	base := filepath.Base(dir)
	v, rep, ok := strings.Cut(base, "-")
	if !ok {
		return nil, fmt.Errorf("%s: not VARIANT-REP", dir)
	}
	n, err := strconv.Atoi(rep)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	r := &Run{Load: filepath.Base(filepath.Dir(dir)), Variant: v, Rep: n, Dir: dir, Marks: map[string][2]float64{}}
	f, err := os.Open(filepath.Join(dir, "samples.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var s Sample
		if err := json.Unmarshal(sc.Bytes(), &s); err == nil {
			r.Samples = append(r.Samples, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, "pss.txt")); err == nil {
		r.PSS = parsePSS(b)
	}
	b, err := os.ReadFile(filepath.Join(dir, "marks.csv"))
	if err != nil {
		return nil, err
	}
	r.Marks = parseMarks(b)
	return r, nil
}

func parsePSS(b []byte) [][5]float64 {
	var out [][5]float64
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) != 5 {
			continue
		}
		var p [5]float64
		ok := true
		for i := range f {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				ok = false
				break
			}
			p[i] = v
		}
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func parseMarks(b []byte) map[string][2]float64 {
	m := map[string][2]float64{}
	for line := range strings.Lines(string(b)) {
		f := strings.Split(strings.TrimSpace(line), ",")
		if len(f) != 3 {
			continue
		}
		t, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			continue
		}
		w := m[f[1]]
		switch f[2] {
		case "start":
			w[0] = t
		case "end":
			w[1] = t
		}
		m[f[1]] = w
	}
	return m
}

func analyzeCmd(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	runsDir := fs.String("runs", "", "the campaign's runs directory: LOAD/VARIANT-REP/")
	out := fs.String("out", "", "the Markdown tables")
	breakdown := fs.String("breakdown", "", "the snapshots' smaps classes, Markdown; empty for none")
	gcOut := fs.String("gc", "", "the phases' collections from gctrace, Markdown; empty for none")
	fuOut := fs.String("followup", "", "the follow-up's per-run table, Markdown; empty for none")
	samplesOut := fs.String("samples", "", "every run's per-second samples, gzipped TSV; empty for none")
	summaryOut := fs.String("summary", "", "one TSV line per run and phase; empty for none")
	variants := fs.String("variants", "R,M,F", "the variants, in table order")
	ratios := fs.String("ratios", "M/R,F/R,F/M", "the ratio rows, A/B each")
	if err := fs.Parse(args); err != nil {
		return err
	}
	variantOrder = strings.Split(*variants, ",")
	ratioPairs = nil
	for _, p := range strings.Split(*ratios, ",") {
		a, b, ok := strings.Cut(p, "/")
		if !ok {
			return fmt.Errorf("ratio %q is not A/B", p)
		}
		ratioPairs = append(ratioPairs, [2]string{a, b})
	}
	if *runsDir == "" || *out == "" {
		return errors.New("analyze needs -runs and -out")
	}
	dirs, err := filepath.Glob(filepath.Join(*runsDir, "*", "*-*"))
	if err != nil {
		return err
	}
	var runs []*Run
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, "done")); err != nil {
			continue
		}
		r, err := LoadRun(d)
		if err != nil {
			fmt.Fprintln(os.Stderr, "skipping", d, err)
			continue
		}
		runs = append(runs, r)
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()
	writeTables(w, runs)
	if *breakdown != "" {
		bf, err := os.Create(*breakdown)
		if err != nil {
			return err
		}
		defer bf.Close()
		bw := bufio.NewWriter(bf)
		defer bw.Flush()
		writeBreakdown(bw, runs)
	}
	if *gcOut != "" {
		gf, err := os.Create(*gcOut)
		if err != nil {
			return err
		}
		defer gf.Close()
		gw := bufio.NewWriter(gf)
		defer gw.Flush()
		writeGC(gw, runs)
	}
	if *fuOut != "" {
		ff, err := os.Create(*fuOut)
		if err != nil {
			return err
		}
		defer ff.Close()
		fw := bufio.NewWriter(ff)
		defer fw.Flush()
		writeFollowup(fw, runs)
	}
	if *samplesOut != "" {
		if err := writeSamples(*samplesOut, runs); err != nil {
			return err
		}
	}
	if *summaryOut != "" {
		sf, err := os.Create(*summaryOut)
		if err != nil {
			return err
		}
		defer sf.Close()
		sw := bufio.NewWriter(sf)
		defer sw.Flush()
		writeSummary(sw, runs)
	}
	return nil
}

// isRadosgw reports whether the variant is a radosgw: R, the cluster's own on Tentacle, and RM and
// RMH, the Ceph main image's without and with rgw_heap_release_interval.
func isRadosgw(v string) bool {
	return v == "R" || v == "RM" || v == "RMH"
}

// goVariants are the variants that have a Go runtime: all but the radosgws.
func goVariants() []string {
	var out []string
	for _, v := range variantOrder {
		if !isRadosgw(v) {
			out = append(out, v)
		}
	}
	return out
}

var (
	variantOrder = []string{"R", "M", "F"}
	ratioPairs   = [][2]string{{"M", "R"}, {"F", "R"}, {"F", "M"}}
	loadPhases   = map[string][]string{
		"large":  {"put64", "get64"},
		"vlarge": {"put256x1", "get256x1", "put256x2", "get256x2"},
		"small":  {"put4k", "get4k"},
		"mix":    {"mix"},
	}
	steadyPhases = map[string]bool{"get64": true, "get4k": true, "mix": true, "get256x1": true, "get256x2": true}
	idlePoints   = []float64{30, 120, 240, 330, 600}
	memCols      = []string{"VmRSS", "RssAnon", "PSS", "cg current", "working set", "memory.peak", "threads"}
	goCols       = []string{"HeapInuse", "heap resident", "HeapReleased", "budget held", "free list", "non-Go anon", "Go limit"}
	ratioCols    = []string{"VmRSS", "RssAnon", "PSS", "cg current"}
	idleCols     = []string{"VmRSS", "RssAnon", "PSS", "cg current", "HeapInuse", "HeapReleased", "free list", "non-Go anon"}
)

// cell formats each run's value, in rep order, as "a / b".
func cell(rs []*Run, get func(*Run) (float64, bool)) string {
	var parts []string
	for _, r := range rs {
		if v, ok := get(r); ok {
			parts = append(parts, fmt.Sprintf("%.0f", v))
		} else {
			parts = append(parts, "-")
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " / ")
}

func mean(rs []*Run, get func(*Run) (float64, bool)) (float64, bool) {
	var sum float64
	n := 0
	for _, r := range rs {
		if v, ok := get(r); ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

func byVariant(runs []*Run, load string) map[string][]*Run {
	m := map[string][]*Run{}
	for _, r := range runs {
		if r.Load == load {
			m[r.Variant] = append(m[r.Variant], r)
		}
	}
	for _, rs := range m {
		slices.SortFunc(rs, func(a, b *Run) int { return a.Rep - b.Rep })
	}
	return m
}

func writeTables(w io.Writer, runs []*Run) {
	for _, load := range []string{"large", "vlarge", "small", "mix"} {
		vs := byVariant(runs, load)
		if len(vs) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s\n\n", load)
		for _, v := range variantOrder {
			if rs := vs[v]; len(rs) > 0 {
				var reps []string
				for _, r := range rs {
					reps = append(reps, strconv.Itoa(r.Rep))
				}
				fmt.Fprintf(w, "- %s: reps %s\n", v, strings.Join(reps, ", "))
			}
		}
		fmt.Fprintln(w)
		for _, kind := range []string{"peak", "steady"} {
			for _, ph := range loadPhases[load] {
				if kind == "steady" && !steadyPhases[ph] {
					continue
				}
				get := func(m metric) func(*Run) (float64, bool) {
					return func(r *Run) (float64, bool) {
						if kind == "peak" {
							return r.Peak(ph, m)
						}
						return r.Steady(ph, m)
					}
				}
				fmt.Fprintf(w, "### %s %s, MiB (runs in rep order)\n\n", ph, kind)
				fmt.Fprintf(w, "| variant | %s |\n|---|%s\n", strings.Join(memCols, " | "), strings.Repeat("---|", len(memCols)))
				for _, v := range variantOrder {
					rs := vs[v]
					if len(rs) == 0 {
						continue
					}
					var cs []string
					for _, c := range memCols {
						cs = append(cs, cell(rs, get(metricByName(c))))
					}
					fmt.Fprintf(w, "| %s | %s |\n", v, strings.Join(cs, " | "))
				}
				fmt.Fprintln(w)
				fmt.Fprintf(w, "| variant | %s |\n|---|%s\n", strings.Join(goCols, " | "), strings.Repeat("---|", len(goCols)))
				for _, v := range goVariants() {
					rs := vs[v]
					if len(rs) == 0 {
						continue
					}
					var cs []string
					for _, c := range goCols {
						cs = append(cs, cell(rs, get(metricByName(c))))
					}
					fmt.Fprintf(w, "| %s | %s |\n", v, strings.Join(cs, " | "))
				}
				fmt.Fprintln(w)
				fmt.Fprintf(w, "| ratio of means | %s |\n|---|%s\n", strings.Join(ratioCols, " | "), strings.Repeat("---|", len(ratioCols)))
				for _, pr := range ratioPairs {
					var cs []string
					for _, c := range ratioCols {
						a, okA := mean(vs[pr[0]], get(metricByName(c)))
						b, okB := mean(vs[pr[1]], get(metricByName(c)))
						if okA && okB && b > 0 {
							cs = append(cs, fmt.Sprintf("%.2f", a/b))
						} else {
							cs = append(cs, "-")
						}
					}
					fmt.Fprintf(w, "| %s/%s | %s |\n", pr[0], pr[1], strings.Join(cs, " | "))
				}
				fmt.Fprintln(w)
			}
		}
		fmt.Fprintf(w, "### %s throughput (runs in rep order)\n\n", load)
		fmt.Fprintln(w, "| phase | variant | seconds | MiB/s | requests/s | errors | paused s |\n|---|---|---|---|---|---|---|")
		for _, ph := range loadPhases[load] {
			for _, v := range variantOrder {
				rs := vs[v]
				if len(rs) == 0 {
					continue
				}
				var secs, mibs, rps, errs, paused []string
				for _, r := range rs {
					t, ok := r.Throughput(ph)
					if !ok {
						secs, mibs, rps, errs, paused = append(secs, "-"), append(mibs, "-"), append(rps, "-"), append(errs, "-"), append(paused, "-")
						continue
					}
					secs = append(secs, fmt.Sprintf("%.1f", t.Seconds))
					mibs = append(mibs, fmt.Sprintf("%.0f", t.MiBps()))
					rps = append(rps, fmt.Sprintf("%.1f", t.Rps()))
					errs = append(errs, strconv.FormatInt(t.Errors, 10))
					paused = append(paused, strconv.FormatInt(t.PausedS, 10))
				}
				fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s |\n", ph, v, strings.Join(secs, " / "),
					strings.Join(mibs, " / "), strings.Join(rps, " / "), strings.Join(errs, " / "), strings.Join(paused, " / "))
			}
		}
		fmt.Fprintln(w)
		if load == "large" || load == "mix" {
			fmt.Fprintf(w, "### %s idle curve, MiB at seconds after the load (runs in rep order)\n\n", load)
			fmt.Fprintf(w, "| variant | s | %s |\n|---|---|%s\n", strings.Join(idleCols, " | "), strings.Repeat("---|", len(idleCols)))
			for _, v := range variantOrder {
				rs := vs[v]
				if len(rs) == 0 {
					continue
				}
				for _, p := range idlePoints {
					var cs []string
					for _, c := range idleCols {
						m := metricByName(c)
						cs = append(cs, cell(rs, func(r *Run) (float64, bool) { return r.IdleAt(p, m) }))
					}
					fmt.Fprintf(w, "| %s | %.0f | %s |\n", v, p, strings.Join(cs, " | "))
				}
			}
			fmt.Fprintln(w)
			fmt.Fprintf(w, "| idle ratio of means | s | %s |\n|---|---|%s\n", strings.Join(ratioCols, " | "), strings.Repeat("---|", len(ratioCols)))
			for _, pr := range ratioPairs {
				for _, p := range idlePoints {
					var cs []string
					for _, c := range ratioCols {
						m := metricByName(c)
						g := func(r *Run) (float64, bool) { return r.IdleAt(p, m) }
						a, okA := mean(vs[pr[0]], g)
						b, okB := mean(vs[pr[1]], g)
						if okA && okB && b > 0 {
							cs = append(cs, fmt.Sprintf("%.2f", a/b))
						} else {
							cs = append(cs, "-")
						}
					}
					fmt.Fprintf(w, "| %s/%s | %.0f | %s |\n", pr[0], pr[1], p, strings.Join(cs, " | "))
				}
			}
			fmt.Fprintln(w)
		}
	}
}
