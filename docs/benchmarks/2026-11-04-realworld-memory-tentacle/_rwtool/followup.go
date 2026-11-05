package main

import (
	"fmt"
	"io"
)

// writeFollowup writes, per load's GET and PUT phase and per run, what the
// follow-up compares: peak VmRSS and RssAnon, the Go heap in use, the live
// heap and goal from gctrace, GC cycles and CPU, the phase's CPU and
// throughput, the free list's peak, and the bytes it mmap'd and munmap'd by
// the phase's end.
func writeFollowup(w io.Writer, runs []*Run) {
	fmt.Fprintln(w, "## Follow-up per run (MiB; live and goal MB as gctrace prints them)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| load | phase | run | VmRSS peak | RssAnon peak | HeapInuse peak | live median / max | goal max | GC cycles | GC CPU s | phase CPU s | MiB/s | req/s | free list peak | mmap'd / munmap'd |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	mm := goMetric("rgw_go_memory_budget_free_list_mmap_bytes_total")
	mu := goMetric("rgw_go_memory_budget_free_list_munmap_bytes_total")
	for _, load := range []string{"large", "small"} {
		vs := byVariant(runs, load)
		for _, ph := range loadPhases[load] {
			for _, v := range variantOrder {
				for _, r := range vs[v] {
					f := func(m metric) string {
						if x, ok := r.Peak(ph, m); ok {
							return fmt.Sprintf("%.0f", x)
						}
						return "-"
					}
					gc := "- | - | - | - | -"
					if st, ok := r.GC(ph); ok {
						gc = fmt.Sprintf("%.0f / %.0f | %.0f | %d | %.1f | %.1f", st.LiveMedian, st.LiveMax, st.GoalMax, st.Cycles, st.CPUs, st.PhaseCPUs)
					}
					tp := "- | -"
					if t, ok := r.Throughput(ph); ok {
						tp = fmt.Sprintf("%.0f | %.0f", t.MiBps(), t.Rps())
					}
					maps := "-"
					if a, ok := r.Peak(ph, mm); ok {
						b, _ := r.Peak(ph, mu)
						maps = fmt.Sprintf("%.0f / %.0f", a, b)
					}
					fmt.Fprintf(w, "| %s | %s | %s-%d | %s | %s | %s | %s | %s | %s | %s |\n", load, ph, v, r.Rep,
						f(metricByName("VmRSS")), f(metricByName("RssAnon")), f(metricByName("HeapInuse")),
						gc, tp, f(metricByName("free list")), maps)
				}
			}
		}
	}
	fmt.Fprintln(w)
}
