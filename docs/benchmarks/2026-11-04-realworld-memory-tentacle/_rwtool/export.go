package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// phaseAt is the run's phase whose window holds t, or "".
func (r *Run) phaseAt(t float64) string {
	for name, w := range r.Marks {
		if t >= w[0] && (w[1] == 0 || t <= w[1]) && !strings.HasPrefix(name, "prep") {
			return name
		}
	}
	return ""
}

var exportCols = []string{
	"VmRSS", "RssAnon", "RssFile", "PSS", "cg current", "working set", "memory.peak", "threads",
	"HeapInuse", "heap resident", "HeapReleased", "budget held", "free list",
}

// writeSamples writes every run's once-a-second samples as one TSV, gzipped:
// the run, seconds since its first sample, the phase, and exportCols in MiB
// (threads as a count), with the GC count and F-mmap's mapped and unmapped
// bytes.
func writeSamples(path string, runs []*Run) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	w := bufio.NewWriter(zw)
	cols := append([]string{"load", "variant", "rep", "t_s", "phase"}, exportCols...)
	cols = append(cols, "gc_count", "mmap_MiB", "munmap_MiB")
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	mm := goMetric("rgw_go_memory_budget_free_list_mmap_bytes_total")
	mu := goMetric("rgw_go_memory_budget_free_list_munmap_bytes_total")
	for _, r := range sortedRuns(runs) {
		if len(r.Samples) == 0 {
			continue
		}
		t0 := r.Samples[0].T
		for _, s := range r.Samples {
			row := []string{r.Load, r.Variant, fmt.Sprint(r.Rep), fmt.Sprintf("%.0f", s.T-t0), r.phaseAt(s.T)}
			for _, c := range exportCols {
				if v, ok := metricByName(c).Get(r, s); ok {
					row = append(row, fmt.Sprintf("%.1f", v))
				} else {
					row = append(row, "")
				}
			}
			if n, ok := r.gcCount(s); ok {
				row = append(row, fmt.Sprint(n))
			} else {
				row = append(row, "")
			}
			for _, m := range []metric{mm, mu} {
				if v, ok := m.Get(r, s); ok {
					row = append(row, fmt.Sprintf("%.0f", v))
				} else {
					row = append(row, "")
				}
			}
			fmt.Fprintln(w, strings.Join(row, "\t"))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

func sortedRuns(runs []*Run) []*Run {
	out := slices.Clone(runs)
	slices.SortFunc(out, func(a, b *Run) int {
		if c := strings.Compare(a.Load, b.Load); c != 0 {
			return c
		}
		if c := strings.Compare(a.Variant, b.Variant); c != 0 {
			return c
		}
		return a.Rep - b.Rep
	})
	return out
}

// writeSummary writes one TSV line per run and phase: peaks and steady
// states of the memory columns, the GC figures and the throughput.
func writeSummary(w io.Writer, runs []*Run) {
	cols := []string{"load", "variant", "rep", "phase", "seconds", "MiB_per_s", "req_per_s", "errors"}
	for _, c := range exportCols {
		cols = append(cols, "peak "+c)
	}
	cols = append(cols, "steady VmRSS", "steady RssAnon", "steady PSS", "gc_cycles", "gc_cpu_s", "phase_cpu_s",
		"live_median_MB", "live_max_MB", "goal_max_MB")
	fmt.Fprintln(w, strings.Join(cols, "\t"))
	for _, r := range sortedRuns(runs) {
		for _, ph := range loadPhases[r.Load] {
			row := []string{r.Load, r.Variant, fmt.Sprint(r.Rep), ph}
			if t, ok := r.Throughput(ph); ok {
				row = append(row, fmt.Sprintf("%.1f", t.Seconds), fmt.Sprintf("%.1f", t.MiBps()), fmt.Sprintf("%.1f", t.Rps()), fmt.Sprint(t.Errors))
			} else {
				row = append(row, "", "", "", "")
			}
			for _, c := range exportCols {
				if v, ok := r.Peak(ph, metricByName(c)); ok {
					row = append(row, fmt.Sprintf("%.0f", v))
				} else {
					row = append(row, "")
				}
			}
			for _, c := range []string{"VmRSS", "RssAnon", "PSS"} {
				if v, ok := r.Steady(ph, metricByName(c)); ok {
					row = append(row, fmt.Sprintf("%.0f", v))
				} else {
					row = append(row, "")
				}
			}
			if st, ok := r.GC(ph); ok {
				row = append(row, fmt.Sprint(st.Cycles), fmt.Sprintf("%.2f", st.CPUs), fmt.Sprintf("%.1f", st.PhaseCPUs),
					fmt.Sprintf("%.0f", st.LiveMedian), fmt.Sprintf("%.0f", st.LiveMax), fmt.Sprintf("%.0f", st.GoalMax))
			} else {
				row = append(row, "", "", "", "", "", "")
			}
			fmt.Fprintln(w, strings.Join(row, "\t"))
		}
		if _, ok := r.Marks["idle"]; ok {
			for _, p := range idlePoints {
				row := []string{r.Load, r.Variant, fmt.Sprint(r.Rep), fmt.Sprintf("idle+%.0fs", p), "", "", "", ""}
				for _, c := range exportCols {
					if v, ok := r.IdleAt(p, metricByName(c)); ok {
						row = append(row, fmt.Sprintf("%.0f", v))
					} else {
						row = append(row, "")
					}
				}
				row = append(row, "", "", "", "", "", "", "", "", "")
				fmt.Fprintln(w, strings.Join(row, "\t"))
			}
		}
	}
}
