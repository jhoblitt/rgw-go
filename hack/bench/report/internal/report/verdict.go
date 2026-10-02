package report

import (
	"fmt"
	"slices"
	"strings"
)

// Criterion is a test applied to a mode's cells, or a measurement reported
// beside the tests.
type Criterion string

// The criteria: cgo is not a phase 1 bottleneck on a release when a mode
// meets both there. They compare the four mirror shapes with rados bench at
// the same size and concurrency.
const (
	Throughput Criterion = "throughput>=0.8x floor"
	Latency    Criterion = "mean latency<=1.25x floor"
)

// The measurements, reported for every mode against no threshold. CPU is
// the CPU each operation of read4k at 256 in flight used, the cell the
// profiles are taken at; among the modes that meet the criteria, the one
// using the least is preferred. Boundary is the part of that CPU a pure-Go
// client would no longer spend, Share.Boundary. Threads is a mode's
// largest thread growth. Cgo is every sample with a frame of the cgo
// boundary on its stack: it counts the C librados runs on the Go thread as
// a cost of the boundary, and a more efficient Go side raises it.
const (
	CPU      Criterion = "CPU per operation, read4k at 256 (not judged)"
	Boundary Criterion = "boundary cost, read4k at 256 (not judged)"
	Threads  Criterion = "thread growth (not judged)"
	Cgo      Criterion = "cgo frames, share of CPU (not judged)"
)

var (
	criteria     = []Criterion{Throughput, Latency}
	measurements = []Criterion{CPU, Boundary, Threads, Cgo}
)

const (
	minThroughputRatio = 0.8
	maxLatencyRatio    = 1.25
)

// profileConc is the concurrency of the read4k cell the CPU profiles are
// taken at.
const profileConc = 256

// Status is a criterion's outcome in one mode.
type Status string

// The outcomes. A criterion is Incomplete when a cell it needs is missing
// and none it has fails, and Inconclusive when the data bound the measure
// on both sides of its threshold.
const (
	Pass         Status = "PASS"
	Fail         Status = "FAIL"
	Incomplete   Status = "INCOMPLETE"
	Inconclusive Status = "INCONCLUSIVE"
	// NotJudged marks a measurement reported beside the criteria and
	// compared with no threshold.
	NotJudged Status = "NOT JUDGED"
)

// Verdict is one criterion applied to one mode's cells, or one measurement
// of them. Margin names the worst cell and its distance from the
// threshold, positive when it is on the passing side, or the measurement,
// and lists any cell it needs that is missing.
type Verdict struct {
	Criterion Criterion
	Mode      string
	Judged    bool
	Status    Status
	Margin    string
	// Unstable lists the cells feeding the verdict whose floor runs before
	// and after them differ by more than maxFloorDrift.
	Unstable []string
	// Informational marks a verdict reported beside the criteria and not
	// counted in the answer.
	Informational bool
}

// SeamReport is a sweep directory rendered: its markdown, every verdict, the
// modes that meet both criteria, and which of those used the least CPU per
// operation, with that CPU, or "" when none did or none was measured.
type SeamReport struct {
	Markdown  string
	Verdicts  []Verdict
	Passing   []string
	LowestCPU string
}

// Answer says which modes meet both criteria.
func (r SeamReport) Answer() string { return answer(r.Passing) }

func answer(modes []string) string {
	switch len(modes) {
	case 0:
		return "no mode meets both criteria"
	case 1:
		return modes[0] + " meets both criteria"
	default:
		return strings.Join(modes[:len(modes)-1], ", ") + " and " + modes[len(modes)-1] + " meet both criteria"
	}
}

// lowestCPU names the mode of modes whose read4k at profileConc used the
// least CPU per operation, the first in mode order on a tie, with that
// CPU, or "" when none has the cell.
func (s *sweep) lowestCPU(modes []string) string {
	best, cpu := "", 0.0
	for _, m := range modes {
		c, ok := s.cells[key{m, "read4k", profileConc}]
		if ok && (best == "" || c.CPUusPerOp < cpu) {
			best, cpu = m, c.CPUusPerOp
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("%s, %.0f µs", best, cpu)
}

// passing returns the judged modes whose verdicts that counts all pass.
func passing(modes []string, vs []Verdict, counts func(Verdict) bool) []string {
	var out []string
	for _, m := range modes {
		if !judged[m] {
			continue
		}
		ok := true
		for _, v := range vs {
			if v.Mode == m && counts(v) && v.Status != Pass {
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
		for _, v := range []Verdict{s.throughput(m), s.latency(m), s.cpu(m), s.boundary(m), s.threads(m), s.cgo(m)} {
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
		if m.unjudged != "" {
			continue
		}
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
	return s.unbracketedVerdict(v)
}

// unbracketedVerdict leaves a ratio criterion undecided in a sweep whose
// floor ran once per cell after the sweep: the cluster and the host change
// enough between a cell and a floor run long after it that the ratio cannot
// be read as the seam's.
func (s *sweep) unbracketedVerdict(v Verdict) Verdict {
	if s.unbracketed && v.Status != Incomplete {
		v.Status = Inconclusive
		v.Margin = "unbracketed floor, one run after the sweep; " + v.Margin
	}
	return v
}

func (s *sweep) latency(mode string) Verdict {
	cells, missing, unstable := s.floorRatios(mode, func(c Cell) float64 { return c.MeanUs }, floorMean)
	v := judge(cells, missing, unstable,
		func(a, b float64) bool { return a > b },
		func(r float64) bool { return r > maxLatencyRatio },
		func(r float64) float64 { return maxLatencyRatio - r })
	v.Criterion = Latency
	return s.unbracketedVerdict(v)
}

// growthAt is a cell's thread growth against one baseline.
type growthAt struct {
	cell   *Cell
	growth int
}

func (g growthAt) String() string {
	return fmt.Sprintf("largest %+d (%s at %d)", g.growth, g.cell.Shape, g.cell.Conc)
}

// threads measures the largest growth among mode's cells, all of which ran
// under the byte budget, from two baselines: the cell's own idle count,
// which leaves out what earlier cells of its process grew and so bounds its
// growth from below, and its process's first idle count, which bounds it
// from above. A cell in a process of its own has one baseline.
func (s *sweep) threads(mode string) Verdict {
	var own, proc *growthAt
	same := true
	for _, shape := range s.shapes {
		for _, c := range s.cellsOf(mode, shape) {
			o := growthAt{c, c.growth()}
			p := growthAt{c, c.ThreadsPeak - s.processStart(key{mode, c.Shape, c.Conc})}
			same = same && o.growth == p.growth
			if own == nil || o.growth > own.growth {
				own = &o
			}
			if proc == nil || p.growth > proc.growth {
				proc = &p
			}
		}
	}
	if own == nil {
		return Verdict{Criterion: Threads, Status: Incomplete, Margin: "missing: every cell", Informational: true}
	}
	margin := own.String()
	if !same {
		margin = "against each cell's own idle count, " + own.String() + "; against its process's first, " + proc.String()
	}
	return Verdict{Criterion: Threads, Status: NotJudged, Margin: margin, Informational: true}
}

// cpu measures the CPU each operation of mode's read4k at profileConc used.
func (s *sweep) cpu(mode string) Verdict {
	v := Verdict{Criterion: CPU, Status: NotJudged, Informational: true}
	c, ok := s.cells[key{mode, "read4k", profileConc}]
	if !ok {
		v.Status, v.Margin = Incomplete, fmt.Sprintf("missing: read4k at %d", profileConc)
		return v
	}
	v.Margin = fmt.Sprintf("%.0f µs", c.CPUusPerOp)
	return v
}

// cgo measures the share of mode's read4k profile at 256 whose stacks hold a
// frame of the cgo boundary, against no threshold.
func (s *sweep) cgo(mode string) Verdict {
	v := Verdict{Criterion: Cgo, Status: NotJudged, Informational: true}
	p, ok := s.profiles[mode]
	if !ok {
		v.Status, v.Margin = Incomplete, "missing: cpu-"+mode+"-read4k-256.pprof"
		return v
	}
	v.Margin = fmt.Sprintf("%.1f%% of %s sampled", p.share.percent(p.share.Cgo), p.share.Total)
	return v
}

// boundary measures the boundary cost in mode's read4k profile at
// profileConc, as a share of the profile and, where the cell was measured,
// as the part of its CPU per operation that share is.
func (s *sweep) boundary(mode string) Verdict {
	v := Verdict{Criterion: Boundary, Status: NotJudged, Informational: true}
	p, ok := s.profiles[mode]
	if !ok {
		v.Status, v.Margin = Incomplete, "missing: cpu-"+mode+"-read4k-256.pprof"
		return v
	}
	pct := p.share.percent(p.share.Boundary())
	v.Margin = fmt.Sprintf("%.1f%% of %s sampled", pct, p.share.Total)
	if c, ok := s.cells[key{mode, "read4k", profileConc}]; ok {
		v.Margin += fmt.Sprintf(", about %.1f µs of the operation's %.0f", c.CPUusPerOp*pct/100, c.CPUusPerOp)
	}
	// Pipe mode's completion is written to the pipe by a librados thread,
	// which the profile samples under runtime._ExternalCode with no frame
	// that tells that write from librados's other work.
	if mode == "pipe" {
		v.Margin += "; leaves out pipe's write(2) per completion, unmeasured"
	}
	return v
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
