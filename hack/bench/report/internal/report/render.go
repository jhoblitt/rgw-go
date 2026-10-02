package report

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

func (s *sweep) render(r SeamReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Seam microbenchmark: %s\n", s.releaseName())
	s.renderEnv(&b)
	s.renderVerdict(&b, r)
	for _, shape := range s.shapes {
		s.renderShape(&b, shape)
	}
	s.renderFloors(&b)
	s.renderOver(&b)
	s.renderProfiles(&b)
	return b.String()
}

func (s *sweep) renderEnv(b *strings.Builder) {
	if len(s.envKeys) == 0 {
		b.WriteString("\nenv.json records nothing.\n")
		return
	}
	b.WriteString("\n| env.json | |\n|---|---|\n")
	for _, k := range s.envKeys {
		fmt.Fprintf(b, "| %s | %s |\n", k, envValue(s.env[k]))
	}
}

func envValue(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(data)
	}
}

func (s *sweep) renderVerdict(b *strings.Builder, r SeamReport) {
	b.WriteString("\n## Verdict\n\n")
	b.WriteString("cgo is not a bottleneck on a release when one of the callback and pipe modes passes all four criteria there. " +
		"Throughput and mean latency compare read4k and write4k at 64 and 256 in flight, and read4m and write4m at 1, 4 and 16, " +
		"with rados bench at the same size and concurrency. The threads criterion allows each cell under the byte budget " +
		"GOMAXPROCS + 8 threads of growth, its peak less its idle count. The cgo criterion is the share of the CPU profile " +
		"of read4k at 256 in flight whose stacks hold runtime.cgocall, runtime.cgocallback*, a _Cfunc_ stub or runtime.(*Pinner). " +
		"sync parks an OS thread per operation in flight and is the baseline, not judged.\n\n")
	b.WriteString("| criterion |")
	for _, m := range s.modes {
		label := m
		if !judged[m] {
			label += " (baseline)"
		}
		fmt.Fprintf(b, " %s |", label)
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(s.modes)) + "\n")
	for _, c := range criteria {
		fmt.Fprintf(b, "| %s |", c)
		for _, m := range s.modes {
			i := slices.IndexFunc(r.Verdicts, func(v Verdict) bool { return v.Mode == m && v.Criterion == c })
			v := r.Verdicts[i]
			fmt.Fprintf(b, " %s: %s |", v.Status, v.Margin)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "\n**Answer:** %s on %s.\n", r.Answer(), s.releaseName())
	s.renderLimits(b)
}

// renderLimits states the in-flight limits the cells ran under and any
// submission that parked on the limiter, which a cell under the byte budget
// should never do.
func (s *sweep) renderLimits(b *strings.Builder) {
	limits := map[string]bool{}
	var waited []string
	for _, shape := range s.shapes {
		for _, m := range s.modes {
			for _, c := range s.cellsOf(m, shape) {
				limits[c.limiter()] = true
				if c.ThrottleWaits > 0 {
					waited = append(waited, fmt.Sprintf("%s %s at %d (%d)", m, shape, c.Conc, c.ThrottleWaits))
				}
			}
		}
	}
	fmt.Fprintf(b, "\nIn-flight limits of the cells: %s. ", strings.Join(ordered(limits, nil), "; "))
	if len(waited) == 0 {
		b.WriteString("No submission parked on the limiter.\n")
		return
	}
	fmt.Fprintf(b, "Submissions parked on the limiter in %s.\n", strings.Join(waited, ", "))
}

func (s *sweep) renderShape(b *strings.Builder, shape string) {
	mirrored := slices.ContainsFunc(mirrors, func(m mirror) bool { return m.shape == shape })
	fmt.Fprintf(b, "\n## %s\n\n", shape)
	b.WriteString("| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |")
	if mirrored {
		b.WriteString(" floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor |")
	}
	b.WriteString("\n|" + strings.Repeat("---|", 11))
	if mirrored {
		b.WriteString(strings.Repeat("---|", 4))
	}
	b.WriteString("\n")
	var concs []int
	for k := range s.cells {
		if k.shape == shape && !slices.Contains(concs, k.conc) {
			concs = append(concs, k.conc)
		}
	}
	slices.Sort(concs)
	for _, conc := range concs {
		f, fok := s.floors[floorKey{shape, conc}]
		for _, m := range s.modes {
			c := s.cells[key{m, shape, conc}]
			fmt.Fprintf(b, "| %d | %s | %d | %s | %s | %s | %s | %s | %.1f | %d→%d (%+d) | %d |",
				conc, m, c.N, num(c.OpsPerSec), num(c.P50us), num(c.P99us), num(c.P999us), num(c.MeanUs),
				c.CPUusPerOp, c.ThreadsIdle, c.ThreadsPeak, c.growth(), c.Errors)
			switch {
			case !mirrored:
			case fok && f.OpsPerSec > 0 && f.MeanUs > 0:
				fmt.Fprintf(b, " %s | %s | %.2fx | %.2fx |", num(f.OpsPerSec), num(f.MeanUs), c.OpsPerSec/f.OpsPerSec, c.MeanUs/f.MeanUs)
			default:
				b.WriteString(" — | — | — | — |")
			}
			b.WriteString("\n")
		}
	}
}

func (s *sweep) renderFloors(b *strings.Builder) {
	if len(s.floors) == 0 {
		return
	}
	b.WriteString("\n## rados bench floor\n\n")
	b.WriteString("| shape | op | size | conc | seconds | max objects | objects read | ops | elapsed s | ops/s | mean µs | MB/s |\n")
	b.WriteString("|" + strings.Repeat("---|", 12) + "\n")
	keys := make([]floorKey, 0, len(s.floors))
	for k := range s.floors {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, s.compareCells)
	for _, k := range keys {
		f := s.floors[k]
		fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %s | %s | %d | %.2f | %s | %s | %.2f |\n",
			f.Shape, f.Op, f.Size, f.Conc, f.Seconds, count(f.MaxObjects), count(f.Objects), f.TotalOps,
			f.ElapsedS, num(f.OpsPerSec), num(f.MeanUs), f.BandwidthMBps)
	}
}

// count renders 0, a limit or count that does not apply, as a dash.
func count(n int) string {
	if n == 0 {
		return "—"
	}
	return strconv.Itoa(n)
}

func (s *sweep) renderOver(b *strings.Builder) {
	if len(s.over) == 0 {
		return
	}
	b.WriteString("\n## Over the byte budget\n\n")
	b.WriteString("Cells run past the byte budget on purpose, each in a process of its own. With the limiter's bounds above what the cell " +
		"can reach, librados's objecter throttle blocks submitting threads inside C; with the bounds derived from that throttle, " +
		"the limiter parks the excess submissions in Go. The threads criterion does not cover these cells.\n\n")
	b.WriteString("| mode | shape | conc | limiter | n | ops/s | mean µs | threads | growth | allowance | limiter waits |\n")
	b.WriteString("|" + strings.Repeat("---|", 11) + "\n")
	for i := range s.over {
		c := &s.over[i]
		fmt.Fprintf(b, "| %s | %s | %d | %s | %d | %s | %s | %d→%d | %+d | %d | %d |\n",
			c.Mode, c.Shape, c.Conc, c.limiter(), c.N, num(c.OpsPerSec), num(c.MeanUs),
			c.ThreadsIdle, c.ThreadsPeak, c.growth(), c.allowance(), c.ThrottleWaits)
	}
}

func (s *sweep) renderProfiles(b *strings.Builder) {
	if len(s.profiles) == 0 {
		return
	}
	b.WriteString("\n## CPU profiles\n\n")
	b.WriteString("The profile samples every thread of the process. A sample taken while a Go thread runs C is recorded " +
		"under the Go stack that called it, ending in runtime.cgocall, so the cgo share holds the C that Go calls ran, " +
		"librados's submission included, as well as the crossing itself; that C is the second share. A sample of a " +
		"thread the Go runtime does not run, librados's messenger and finisher threads among them, is recorded under " +
		"runtime._ExternalCode; that is the third share, and outside the cgo share.\n\n")
	b.WriteString("| mode | profile | sampled | cgo frames | C under runtime.cgocall | threads outside Go | largest cgo frames, cumulative |\n")
	b.WriteString("|" + strings.Repeat("---|", 7) + "\n")
	for _, m := range ordered(keySet(s.profiles), modeOrder) {
		p := s.profiles[m]
		var top []string
		for _, f := range p.share.Frames[:min(len(p.share.Frames), 5)] {
			top = append(top, fmt.Sprintf("%s %s", f.Name, f.Cum))
		}
		fmt.Fprintf(b, "| %s | %s | %s | %.1f%% | %.1f%% | %.1f%% | %s |\n", m, p.file, p.share.Total,
			p.share.percent(p.share.Cgo), p.share.percent(p.share.InC), p.share.percent(p.share.External), strings.Join(top, "; "))
	}
}

func keySet[V any](m map[string]V) map[string]bool {
	set := make(map[string]bool, len(m))
	for k := range m {
		set[k] = true
	}
	return set
}

// num renders a rate or a latency with the precision its size warrants.
func num(v float64) string {
	switch a := math.Abs(v); {
	case a >= 100:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case a >= 10:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
}
