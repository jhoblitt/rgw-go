package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// gcLine is one GODEBUG=gctrace=1 line.
type gcLine struct {
	N int
	// CPUms sums the line's five CPU figures: the two stop-the-world
	// phases, each charged at GOMAXPROCS, and the assist, background and
	// idle mark work.
	CPUms float64
	// Start, End and Live are the heap at the cycle's start and end and the
	// live heap it marked, Goal the next goal, MB as gctrace prints them.
	Start, End, Live, Goal float64
}

var gcRe = regexp.MustCompile(`^gc (\d+) @[0-9.]+s \d+%: \S+ ms clock, (\S+) ms cpu, (\d+)->(\d+)->(\d+) MB, (\d+) MB goal`)

func parseGCTrace(b []byte) []gcLine {
	var out []gcLine
	for line := range strings.Lines(string(b)) {
		m := gcRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		var l gcLine
		l.N, _ = strconv.Atoi(m[1])
		for _, f := range strings.FieldsFunc(m[2], func(r rune) bool { return r == '+' || r == '/' }) {
			v, err := strconv.ParseFloat(f, 64)
			if err == nil {
				l.CPUms += v
			}
		}
		l.Start, _ = strconv.ParseFloat(m[3], 64)
		l.End, _ = strconv.ParseFloat(m[4], 64)
		l.Live, _ = strconv.ParseFloat(m[5], 64)
		l.Goal, _ = strconv.ParseFloat(m[6], 64)
		out = append(out, l)
	}
	return out
}

// GCStats are a phase's collections: those the GC count went through from
// the last sample before the phase to the first sample after it ended.
type GCStats struct {
	Cycles                      int
	CPUs                        float64
	LiveMedian, LiveMax         float64
	HeapAtStartMax, GoalMax     float64
	PhaseCPUs                   float64
	HeapInusePeak, HeapAllocMax float64
}

func (r *Run) gcCount(s Sample) (int, bool) {
	v, ok := s.Go["go_gc_duration_seconds_count"]
	return int(v), ok
}

// GC reads the run's gctrace and the phase's CPU, nil when the run has no
// gctrace.
func (r *Run) GC(phase string) (GCStats, bool) {
	w, ok := r.Marks[phase]
	if !ok {
		return GCStats{}, false
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "rgw-go.log"))
	if err != nil {
		return GCStats{}, false
	}
	lines := parseGCTrace(b)
	if len(lines) == 0 {
		return GCStats{}, false
	}
	n0, n1 := -1, -1
	for _, s := range r.Samples {
		c, ok := r.gcCount(s)
		if !ok {
			continue
		}
		if s.T <= w[0] {
			n0 = c
		}
		if s.T >= w[1]+1 && n1 < 0 {
			n1 = c
		}
	}
	if n0 < 0 || n1 < 0 {
		return GCStats{}, false
	}
	var st GCStats
	var lives []float64
	for _, l := range lines {
		if l.N <= n0 || l.N > n1 {
			continue
		}
		st.Cycles++
		st.CPUs += l.CPUms / 1000
		lives = append(lives, l.Live)
		st.LiveMax = max(st.LiveMax, l.Live)
		st.HeapAtStartMax = max(st.HeapAtStartMax, l.Start)
		st.GoalMax = max(st.GoalMax, l.Goal)
	}
	if len(lives) > 0 {
		slices.Sort(lives)
		st.LiveMedian = lives[len(lives)/2]
	}
	if v, ok := r.Peak(phase, metricByName("HeapInuse")); ok {
		st.HeapInusePeak = v
	}
	if v, ok := r.Peak(phase, goMetric("go_memstats_heap_alloc_bytes")); ok {
		st.HeapAllocMax = v
	}
	if b, err := os.ReadFile(filepath.Join(r.Dir, "cpu.csv")); err == nil {
		for line := range strings.Lines(string(b)) {
			f := strings.Split(strings.TrimSpace(line), ",")
			if len(f) == 6 && f[0] == phase {
				us, _ := strconv.ParseFloat(f[3], 64)
				st.PhaseCPUs = us / 1e6
			}
		}
	}
	return st, true
}

func goMetric(k string) metric { return metric{Name: k, Get: goMiB(k)} }

// writeGC writes the collections of the GET phases and the mix, per run.
func writeGC(w io.Writer, runs []*Run) {
	fmt.Fprintln(w, "## GC per phase (gctrace; MB as gctrace prints them, MiB from /metrics)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| load | phase | run | GC cycles | GC CPU s | phase CPU s | live after GC, median / max MB | heap at GC start max MB | goal max MB | HeapAlloc max MiB | HeapInuse peak MiB |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|")
	for _, load := range []string{"large", "vlarge", "small", "mix"} {
		vs := byVariant(runs, load)
		var names []string
		for v := range vs {
			names = append(names, v)
		}
		slices.Sort(names)
		for _, ph := range loadPhases[load] {
			for _, v := range names {
				if isRadosgw(v) {
					continue
				}
				for _, r := range vs[v] {
					st, ok := r.GC(ph)
					if !ok {
						continue
					}
					fmt.Fprintf(w, "| %s | %s | %s-%d | %d | %.1f | %.1f | %.0f / %.0f | %.0f | %.0f | %.0f | %.0f |\n",
						load, ph, v, r.Rep, st.Cycles, st.CPUs, st.PhaseCPUs, st.LiveMedian, st.LiveMax,
						st.HeapAtStartMax, st.GoalMax, st.HeapAllocMax, st.HeapInusePeak)
				}
			}
		}
	}
	fmt.Fprintln(w)
}
