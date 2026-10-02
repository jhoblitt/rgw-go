package report

import (
	"fmt"
	"slices"
	"strings"
)

// Criterion is one of the four tests that say cgo is not a phase 1
// bottleneck when a mode meets all of them on both releases.
type Criterion string

// The criteria. The throughput and latency criteria compare the four mirror
// shapes with rados bench at the same size and concurrency; the threads
// criterion covers every cell under the byte budget; the cgo criterion reads
// the CPU profile of read4k at 256 in flight.
const (
	Throughput Criterion = "throughput>=0.8x floor"
	Latency    Criterion = "mean latency<=1.25x floor"
	Threads    Criterion = "threads<=gomaxprocs+8"
	Cgo        Criterion = "cgo<=10% of CPU"
)

var criteria = []Criterion{Throughput, Latency, Threads, Cgo}

const (
	minThroughputRatio = 0.8
	maxLatencyRatio    = 1.25
	maxCgoPercent      = 10.0
)

// Status is a criterion's outcome in one mode.
type Status string

// The outcomes. A criterion is Incomplete when a cell it needs is missing
// and none it has fails.
const (
	Pass       Status = "PASS"
	Fail       Status = "FAIL"
	Incomplete Status = "INCOMPLETE"
)

// Verdict is one criterion applied to one mode's cells. Judged is false for
// sync, the baseline. Margin names the worst cell and its distance from the
// threshold, positive when it is on the passing side, and lists any cell the
// criterion needs that is missing.
type Verdict struct {
	Criterion Criterion
	Mode      string
	Judged    bool
	Status    Status
	Margin    string
	// Unstable lists the cells feeding the verdict whose floor runs before
	// and after them differ by more than maxFloorDrift.
	Unstable []string
}

// SeamReport is a sweep directory rendered: its markdown, every verdict, and
// the judged modes that pass all four criteria.
type SeamReport struct {
	Markdown string
	Verdicts []Verdict
	Passing  []string
}

// Answer says which judged modes pass all four criteria.
func (r SeamReport) Answer() string {
	switch len(r.Passing) {
	case 0:
		return "no judged mode passes all four criteria"
	case 1:
		return r.Passing[0] + " passes all four criteria"
	default:
		return strings.Join(r.Passing, " and ") + " pass all four criteria"
	}
}

func passing(modes []string, vs []Verdict) []string {
	var out []string
	for _, m := range modes {
		if !judged[m] {
			continue
		}
		ok := true
		for _, v := range vs {
			if v.Mode == m && v.Status != Pass {
				ok = false
			}
		}
		if ok {
			out = append(out, m)
		}
	}
	return out
}

func (s *sweep) verdicts() []Verdict {
	var vs []Verdict
	for _, m := range s.modes {
		for _, v := range []Verdict{s.throughput(m), s.latency(m), s.threads(m), s.cgo(m)} {
			v.Mode, v.Judged = m, judged[m]
			vs = append(vs, v)
		}
	}
	return vs
}

// ratioCell is a mirror cell against its floor.
type ratioCell struct {
	shape string
	conc  int
	ratio float64
}

// floorRatios returns cellValue of each judged mirror cell of mode over
// floorValue of its floor, the cells it could not compare with the reason,
// and the cells whose two floor runs drifted apart in floorValue.
func (s *sweep) floorRatios(mode string, cellValue func(Cell) float64, floorValue func(Floor) float64) (cells []ratioCell, missing, unstable []string) {
	for _, m := range mirrors {
		for _, conc := range m.concs {
			c, ok := s.cells[key{mode, m.shape, conc}]
			cf := s.floorAt(m.shape, conc)
			fv, why := cf.value(floorValue)
			switch {
			case !ok:
				why = "no cell"
			case c.Errors > 0:
				why = fmt.Sprintf("%d errors", c.Errors)
			case why == "" && fv <= 0:
				why = "floor has no operations"
			}
			if why != "" {
				missing = append(missing, fmt.Sprintf("%s at %d (%s)", m.shape, conc, why))
				continue
			}
			if d := cf.drift(floorValue); d > maxFloorDrift {
				unstable = append(unstable, fmt.Sprintf("%s at %d (%.1f%%)", m.shape, conc, 100*d))
			}
			cells = append(cells, ratioCell{m.shape, conc, cellValue(c) / fv})
		}
	}
	return cells, missing, unstable
}

// judge turns the worst of cells into a verdict: worse reports whether a is
// worse than b, fails whether a ratio fails, and margin its distance from the
// threshold. The cells with an unstable floor are named after the margin.
func judge(cells []ratioCell, missing, unstable []string, worse func(a, b float64) bool, fails func(float64) bool, margin func(float64) float64) Verdict {
	v := judgeCells(cells, missing, worse, fails, margin)
	if len(unstable) > 0 {
		v.Unstable = unstable
		v.Margin += "; unstable floor: " + strings.Join(unstable, ", ")
	}
	return v
}

func judgeCells(cells []ratioCell, missing []string, worse func(a, b float64) bool, fails func(float64) bool, margin func(float64) float64) Verdict {
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "missing: "+strings.Join(missing, ", "))
	}
	if len(cells) == 0 {
		return Verdict{Status: Incomplete, Margin: strings.Join(parts, "; ")}
	}
	w := cells[0]
	for _, c := range cells[1:] {
		if worse(c.ratio, w.ratio) {
			w = c
		}
	}
	parts = append(parts, fmt.Sprintf("worst %.2fx (%s at %d), margin %+.2f", w.ratio, w.shape, w.conc, margin(w.ratio)))
	status := Pass
	switch {
	case fails(w.ratio):
		status = Fail
		slices.Reverse(parts)
	case len(missing) > 0:
		status = Incomplete
	}
	return Verdict{Status: status, Margin: strings.Join(parts, "; ")}
}

func (s *sweep) throughput(mode string) Verdict {
	cells, missing, unstable := s.floorRatios(mode, func(c Cell) float64 { return c.OpsPerSec }, floorOps)
	v := judge(cells, missing, unstable,
		func(a, b float64) bool { return a < b },
		func(r float64) bool { return r < minThroughputRatio },
		func(r float64) float64 { return r - minThroughputRatio })
	v.Criterion = Throughput
	return v
}

func (s *sweep) latency(mode string) Verdict {
	cells, missing, unstable := s.floorRatios(mode, func(c Cell) float64 { return c.MeanUs }, floorMean)
	v := judge(cells, missing, unstable,
		func(a, b float64) bool { return a > b },
		func(r float64) bool { return r > maxLatencyRatio },
		func(r float64) float64 { return maxLatencyRatio - r })
	v.Criterion = Latency
	return v
}

// threads judges every cell of mode, all of which ran under the byte
// budget, by its growth against its own allowance.
func (s *sweep) threads(mode string) Verdict {
	var worst *Cell
	for _, shape := range s.shapes {
		for _, c := range s.cellsOf(mode, shape) {
			if worst == nil || c.growth()-c.allowance() > worst.growth()-worst.allowance() {
				worst = c
			}
		}
	}
	if worst == nil {
		return Verdict{Criterion: Threads, Status: Incomplete, Margin: "missing: every cell"}
	}
	status := Pass
	if worst.growth() > worst.allowance() {
		status = Fail
	}
	return Verdict{
		Criterion: Threads,
		Status:    status,
		Margin: fmt.Sprintf("worst %+d of %d allowed (%s at %d), margin %+d",
			worst.growth(), worst.allowance(), worst.Shape, worst.Conc, worst.allowance()-worst.growth()),
	}
}

func (s *sweep) cgo(mode string) Verdict {
	p, ok := s.profiles[mode]
	if !ok {
		return Verdict{Criterion: Cgo, Status: Incomplete, Margin: "missing: cpu-" + mode + "-read4k-256.pprof"}
	}
	pct := p.share.percent(p.share.Cgo)
	status := Pass
	if pct > maxCgoPercent {
		status = Fail
	}
	return Verdict{
		Criterion: Cgo,
		Status:    status,
		Margin:    fmt.Sprintf("%.1f%% of %s sampled, margin %+.1f points", pct, p.share.Total, maxCgoPercent-pct),
	}
}

// cellsOf returns mode's cells of shape in concurrency order.
func (s *sweep) cellsOf(mode, shape string) []*Cell {
	var out []*Cell
	for k := range s.cells {
		if k.mode == mode && k.shape == shape {
			c := s.cells[k]
			out = append(out, &c)
		}
	}
	slices.SortFunc(out, func(a, b *Cell) int { return a.Conc - b.Conc })
	return out
}
