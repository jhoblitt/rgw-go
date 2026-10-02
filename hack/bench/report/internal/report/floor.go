package report

import (
	"fmt"
	"math"
	"slices"
)

// maxFloorDrift is how far apart, as a fraction of their mean, a judged
// cell's two floor runs may be before its floor is unstable: the cluster or
// the host changed under the cell enough to question its ratio.
const maxFloorDrift = 0.10

// cellFloor is what the floor measured at one mirror cell. A cell the
// throughput and latency criteria compare has a run before its modes and one
// after them; any other cell has a single run. cluster is the run of the
// cluster's own image, where it differs from the floor image, which the
// criteria do not judge by.
//
// A sweep recorded before the floor bracketed its cells has a single run
// for every cell, after the sweep: unbracketed.
type cellFloor struct {
	judged, unbracketed            bool
	before, after, single, cluster *Floor
}

func (s *sweep) floorAt(shape string, conc int) cellFloor {
	run := func(bracket, image string) *Floor {
		if f, ok := s.floors[floorKey{cellKey{shape, conc}, bracket, image}]; ok {
			return &f
		}
		return nil
	}
	cf := cellFloor{
		judged:      judgedCell(shape, conc) && !s.unbracketed,
		unbracketed: s.unbracketed,
		before:      run("before", s.floorImage),
		after:       run("after", s.floorImage),
		single:      run("", s.floorImage),
	}
	if s.clusterImage != s.floorImage {
		cf.cluster = run("", s.clusterImage)
	}
	return cf
}

// clusterRun renders the cluster image's run as its ops/s and mean latency.
func (cf cellFloor) clusterRun() string {
	if cf.cluster == nil {
		return "—"
	}
	return num(cf.cluster.OpsPerSec) + ", " + num(cf.cluster.MeanUs)
}

// judgedCell reports whether the throughput and latency criteria compare
// shape at conc.
func judgedCell(shape string, conc int) bool {
	i := slices.IndexFunc(mirrors, func(m mirror) bool { return m.shape == shape })
	return i >= 0 && slices.Contains(mirrors[i].concs, conc)
}

// value returns metric of the floor a cell's ratio divides by, the mean of a
// judged cell's two runs or another cell's single run, or why there is none.
func (cf cellFloor) value(metric func(Floor) float64) (v float64, why string) {
	if !cf.judged {
		if cf.single == nil {
			return 0, "no floor"
		}
		return metric(*cf.single), ""
	}
	switch {
	case cf.before == nil && cf.after == nil:
		return 0, "no floor"
	case cf.before == nil || cf.after == nil:
		return 0, "one floor run"
	}
	return (metric(*cf.before) + metric(*cf.after)) / 2, ""
}

// drift is how far apart a judged cell's two runs are in metric, as a
// fraction of their mean, and 0 without both.
func (cf cellFloor) drift(metric func(Floor) float64) float64 {
	if cf.before == nil || cf.after == nil {
		return 0
	}
	a, b := metric(*cf.before), metric(*cf.after)
	if a+b == 0 {
		return 0
	}
	return math.Abs(a-b) / ((a + b) / 2)
}

func floorOps(f Floor) float64 { return f.OpsPerSec }

func floorMean(f Floor) float64 { return f.MeanUs }

// runs renders metric of the cell's floor: both runs of a judged cell, a dash
// for one missing, or another cell's single run.
func (cf cellFloor) runs(metric func(Floor) float64) string {
	show := func(f *Floor) string {
		if f == nil {
			return "—"
		}
		return num(metric(*f))
	}
	if !cf.judged || (cf.before == nil && cf.after == nil) {
		return show(cf.single)
	}
	return show(cf.before) + " / " + show(cf.after)
}

// ratio renders v over the floor's metric.
func (cf cellFloor) ratio(v float64, metric func(Floor) float64) string {
	fv, why := cf.value(metric)
	if why != "" || fv <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2fx", v/fv)
}

// note says how the cell's floor was measured and, for a judged cell with
// both runs, how far they drifted.
func (cf cellFloor) note() string {
	switch {
	case cf.unbracketed && cf.single != nil:
		return "single run after the sweep"
	case !cf.judged && cf.single != nil:
		return "single run"
	case !cf.judged, cf.before == nil && cf.after == nil:
		return "—"
	case cf.before == nil || cf.after == nil:
		return "one floor run"
	}
	ops, mean := cf.drift(floorOps), cf.drift(floorMean)
	d := fmt.Sprintf("ops %.1f%%, mean %.1f%%", 100*ops, 100*mean)
	if ops > maxFloorDrift || mean > maxFloorDrift {
		return "unstable: " + d
	}
	return d
}
