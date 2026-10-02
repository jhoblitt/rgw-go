package report

import (
	"cmp"
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
	switch {
	case s.unbracketed:
		librados := "unrecorded"
		if v, ok := s.env["host_librados"].(string); ok && v != "" {
			librados = v
		}
		fmt.Fprintf(b, "\nThis sweep predates the bracketed floor and the floor image: each floor ran once, after the "+
			"sweep, from the cluster's own image, %s, while the benchmark linked the host's librados, %s. Its throughput "+
			"and latency ratios are shown but not decided.", s.clusterImage, librados)
	case s.clusterImage == s.floorImage:
		fmt.Fprintf(b, "\nThe judged floor is rados bench from %s, Ceph %s, the release of the librados the benchmark "+
			"links. The cluster runs the same image.", s.floorImage, s.floorVersion)
	default:
		fmt.Fprintf(b, "\nThe judged floor is rados bench from %s, Ceph %s, the release of the librados the benchmark "+
			"links. The cluster runs %s; its own rados bench ran once more at each judged cell, shown beside the judged "+
			"floor and not judged.", s.floorImage, s.floorVersion, s.clusterImage)
	}
	// rados bench sends an allocation hint with every write unless told not
	// to; the Go cells send none.
	if noHints, ok := s.env["floor_no_hints"].(bool); ok && noHints {
		b.WriteString(" The floor's writes pass --no-hints, so rados bench sends no allocation hints, as the Go cells send none.\n")
		return
	}
	b.WriteString(" The floor's writes carried rados bench's allocation hints, which the Go cells do not send.\n")
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
	floor := "run just before and just after the cell's three modes: a ratio divides by the mean of the two runs, and a " +
		"cell whose runs differ by more than 10% of that mean is flagged unstable beside the verdict it feeds."
	if s.unbracketed {
		floor = "which in this sweep ran once per cell, after the sweep."
	}
	b.WriteString("cgo is not a bottleneck on a release when one of the callback and pipe modes passes all four criteria there. " +
		"Throughput and mean latency compare read4k and write4k at 64 and 256 in flight, and write4m at 1, 4 and 16, " +
		"with rados bench at the same size and concurrency, " + floor + " read4m is measured and shown with its floor, but " +
		"not judged; its table says why. The threads criterion allows each cell under the byte budget " +
		"GOMAXPROCS + 8 threads of growth, its peak less its idle count. The boundary cost criterion is the share of the " +
		"CPU profile of read4k at 256 in flight that a pure-Go client would no longer pay: the submit crossing and the " +
		"delivery, not the C librados runs on the Go thread nor the Go completion handler. " +
		"sync parks an OS thread per operation in flight and is the baseline, not judged. The informational row under the " +
		"boundary cost reads the same profile for every sample whose stack holds a frame of the cgo boundary, cgo's " +
		"runtime.cgoCheck* pointer checks included. It is a measurement, compared with no threshold: it counts the C " +
		"librados runs on the Go thread, which a pure-Go client would run too, as a cost of the boundary. " +
		"For pipe the boundary cost leaves " +
		"out the C side of each completion, one write(2) to the pipe on a librados thread, which the profile records " +
		"under runtime._ExternalCode with no frame to tell it by: it is unmeasured, and pipe's boundary cost is low by " +
		"that much.\n\n")
	b.WriteString("| criterion |")
	for _, m := range s.modes {
		label := m
		if !judged[m] {
			label += " (baseline)"
		}
		fmt.Fprintf(b, " %s |", label)
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(s.modes)) + "\n")
	for _, c := range append(slices.Clone(criteria), Cgo) {
		label := string(c)
		if c == Cgo {
			label = "informational: " + label
		}
		fmt.Fprintf(b, "| %s |", label)
		for _, m := range s.modes {
			i := slices.IndexFunc(r.Verdicts, func(v Verdict) bool { return v.Mode == m && v.Criterion == c })
			v := r.Verdicts[i]
			if v.Status == NotJudged {
				fmt.Fprintf(b, " %s |", v.Margin)
			} else {
				fmt.Fprintf(b, " %s: %s |", v.Status, v.Margin)
			}
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "\n**Answer:** %s on %s.\n", r.Answer(), s.releaseName())
	s.renderLimits(b)
	s.renderProcesses(b)
}

// renderProcesses gives each mode's thread high-water mark per benchmark
// process, against which the threads criterion reads each cell.
func (s *sweep) renderProcesses(b *strings.Builder) {
	b.WriteString("\nThread counts per benchmark process, the count before its first cell to its highest:\n\n")
	b.WriteString("| mode | benchmark processes | largest growth in one process | highest count |\n|---|---|---|---|\n")
	for _, m := range s.modes {
		largest, highest, n := s.highWater(m)
		fmt.Fprintf(b, "| %s | %d | %s | %d |\n", m, n, largest, highest)
	}
	if s.inferred {
		b.WriteString("\nThese cells carry no process id, so a mode's cells may have shared a benchmark process; the " +
			"processes above are inferred, a cell that starts below its predecessor's process's peak starting a new one. " +
			"Each cell's growth is judged against its own idle count, which leaves out what earlier cells of its process " +
			"grew, and against its process's first idle count, which does not.\n")
	}
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
	i := slices.IndexFunc(mirrors, func(m mirror) bool { return m.shape == shape })
	mirrored := i >= 0
	fmt.Fprintf(b, "\n## %s\n\n", shape)
	if mirrored && mirrors[i].unjudged != "" {
		fmt.Fprintf(b, "Not judged: %s\n\n", mirrors[i].unjudged)
	}
	cluster := mirrored && s.clusterImage != s.floorImage
	b.WriteString("| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |")
	if mirrored {
		b.WriteString(" floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |")
	}
	if cluster {
		b.WriteString(" cluster image floor ops/s, mean µs |")
	}
	b.WriteString("\n|" + strings.Repeat("---|", 11))
	if mirrored {
		b.WriteString(strings.Repeat("---|", 5))
	}
	if cluster {
		b.WriteString("---|")
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
		cf := s.floorAt(shape, conc)
		for _, m := range s.modes {
			c := s.cells[key{m, shape, conc}]
			fmt.Fprintf(b, "| %d | %s | %d | %s | %s | %s | %s | %s | %.1f | %d→%d (%+d) | %d |",
				conc, m, c.N, num(c.OpsPerSec), num(c.P50us), num(c.P99us), num(c.P999us), num(c.MeanUs),
				c.CPUusPerOp, c.ThreadsIdle, c.ThreadsPeak, c.growth(), c.Errors)
			if mirrored {
				fmt.Fprintf(b, " %s | %s | %s | %s | %s |", cf.runs(floorOps), cf.runs(floorMean),
					cf.ratio(c.OpsPerSec, floorOps), cf.ratio(c.MeanUs, floorMean), cf.note())
			}
			if cluster {
				fmt.Fprintf(b, " %s |", cf.clusterRun())
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
	b.WriteString("| shape | op | size | conc | run | image | seconds | max objects | objects read | ops | elapsed s | ops/s | mean µs | MB/s |\n")
	b.WriteString("|" + strings.Repeat("---|", 14) + "\n")
	keys := make([]floorKey, 0, len(s.floors))
	for k := range s.floors {
		keys = append(keys, k)
	}
	images := []string{s.floorImage, s.clusterImage}
	slices.SortFunc(keys, func(a, b floorKey) int {
		return cmp.Or(
			s.compareCells(a.cellKey, b.cellKey),
			cmp.Compare(rank(a.image, images), rank(b.image, images)),
			cmp.Compare(rank(a.bracket, brackets), rank(b.bracket, brackets)),
		)
	})
	for _, k := range keys {
		f := s.floors[k]
		fmt.Fprintf(b, "| %s | %s | %d | %d | %s | %s | %d | %s | %s | %d | %.2f | %s | %s | %.2f |\n",
			f.Shape, f.Op, f.Size, f.Conc, runName(f.Bracket), f.Image, f.Seconds, count(f.MaxObjects), count(f.Objects),
			f.TotalOps, f.ElapsedS, num(f.OpsPerSec), num(f.MeanUs), f.BandwidthMBps)
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
		"runtime._ExternalCode; that is the last share, and outside the cgo share.\n\n" +
		"The columns between divide the profile by the frame nearest each sample's leaf, so no sample counts twice. " +
		"(i), the submit crossing, is the cgo machinery around the C: runtime.cgocall's Go side, the _Cfunc_ stubs, the " +
		"Pinner and the pointer checks, the completion handler's own calls into C included. (ii), the delivery, is how a " +
		"completion reaches Go: the runtime.cgocallback* frames for callback, (*aioPipe).drain and the pipe read it waits " +
		"in for pipe, and nothing for sync, whose wake happens in C. Neither counts the Go completion handler, " +
		"rados.aioComplete, shown for reference: a pure-Go client runs one too. The boundary cost is (i) plus (ii).\n\n")
	b.WriteString("| mode | profile | sampled | cgo frames | C under runtime.cgocall | (i) submit crossing | (ii) delivery | " +
		"boundary cost | Go completion handler | threads outside Go | largest cgo frames, cumulative |\n")
	b.WriteString("|" + strings.Repeat("---|", 11) + "\n")
	for _, m := range ordered(keySet(s.profiles), modeOrder) {
		p := s.profiles[m]
		var top []string
		for _, f := range p.share.Frames[:min(len(p.share.Frames), 5)] {
			top = append(top, fmt.Sprintf("%s %s", f.Name, f.Cum))
		}
		sh := p.share
		fmt.Fprintf(b, "| %s | %s | %s | %.1f%% | %.1f%% | %.1f%% | %.1f%% | %.1f%% | %.1f%% | %.1f%% | %s |\n", m, p.file, sh.Total,
			sh.percent(sh.Cgo), sh.percent(sh.InC), sh.percent(sh.Submit), sh.percent(sh.Delivery), sh.percent(sh.Boundary()),
			sh.percent(sh.Handler), sh.percent(sh.External), strings.Join(top, "; "))
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
