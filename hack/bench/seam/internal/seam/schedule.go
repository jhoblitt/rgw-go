package seam

import (
	"cmp"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// step is one entry of a run's schedule: a benchmark process or a floor run.
type step interface {
	// plan is the step's lines in the run's plan.
	plan(c Config) []string
}

// benchStep is one benchmark process, which runs one cell in one mode.
type benchStep struct {
	mode string
	// results is the file in the run directory the process appends its cells
	// to; empty when it appends them nowhere.
	results string
	cell    []string
	// profile is the file in the run directory the process writes its CPU
	// profile to; empty for none.
	profile string
	// wait names the cell when the process waits for the pool's free space
	// first, and is empty when it does not.
	wait string
}

// args are the cell's arguments for a run in dir.
func (b benchStep) args(dir string) []string {
	if b.profile == "" {
		return b.cell
	}
	return append(append([]string{}, b.cell...), "-test.cpuprofile="+filepath.Join(dir, b.profile))
}

func (b benchStep) plan(c Config) []string {
	var lines []string
	if b.wait != "" {
		lines = append(lines, waitLine(c))
	}
	cell := b.args("")
	line := "bench " + b.mode + ": " + strings.Join(cell, " ")
	if b.results != "" {
		line += " -> " + b.results
	}
	return append(lines, line+" (limit "+strconv.FormatInt(int64(c.Limit(cell)/time.Second), 10)+" s)")
}

// floorStep is one floor run: rados bench measures shape's floor at conc,
// next to a cell the criteria compare when bracket is before or after.
type floorStep struct {
	shape   string
	conc    int
	bracket string
	// cluster runs it from the cluster's own image, for comparison only,
	// where that image is not the floor image.
	cluster bool
}

// size is the shape's object size, and its rados bench -b.
func (f floorStep) size() int64 {
	if strings.HasSuffix(f.shape, "4m") {
		return 4 << 20
	}
	return 4096
}

// write reports whether the shape's floor is a write; the rest read among
// objects written for them.
func (f floorStep) write() bool {
	return strings.HasPrefix(f.shape, "write")
}

// window is the measured run's length in seconds: a 4 MiB write is bounded
// by its objects and capped at floorCapSeconds, so that both sides write the
// same bytes.
func (f floorStep) window(c Config) int64 {
	if f.shape == "write4m" {
		return floorCapSeconds
	}
	return c.FloorSeconds
}

// waits reports whether the run waits for the pool's free space first, as a
// 4 MiB write does.
func (f floorStep) waits() bool {
	return f.shape == "write4m"
}

func (f floorStep) plan(c Config) []string {
	var lines []string
	if f.waits() {
		lines = append(lines, waitLine(c))
	}
	from := "the floor image"
	if f.cluster {
		from = "the cluster's image, unless it is the floor image"
	}
	var what string
	switch {
	case f.shape == "write4m":
		what = fmt.Sprintf("rados bench write of %d objects", c.Write4mObjects)
	case f.write():
		what = fmt.Sprintf("rados bench write for %d s", c.FloorSeconds)
	default:
		what = fmt.Sprintf("rados bench rand for %d s among %d objects", c.FloorSeconds, f.conc)
	}
	return append(lines, fmt.Sprintf("floor %s at %d (%s) from %s: %s (limit %d s)",
		f.shape, f.conc, cmp.Or(f.bracket, "single"), from, what, int64(c.floorLimit(f.window(c))/time.Second)))
}

func waitLine(c Config) string {
	return fmt.Sprintf("wait for %d GiB available in %s", c.MinAvailGiB, pool)
}

// schedule is the run's steps in the order it takes them.
func (c Config) schedule() []step {
	if c.Limiter {
		var steps []step
		for _, mode := range modes {
			steps = append(steps, benchStep{
				mode: mode, results: "overbudget-" + mode + ".jsonl", cell: c.CellArgs(),
				wait: "the over-budget cell in " + mode + " mode",
			})
		}
		return steps
	}
	// The concurrencies of Conc at which the throughput and latency criteria
	// compare read4k and write4k, and the rest.
	var judged, other []int
	for _, n := range c.Conc {
		if n == 64 || n == 256 {
			judged = append(judged, n)
		} else {
			other = append(other, n)
		}
	}
	var steps []step
	// The head and index shapes run at every concurrency of Conc, and read4k
	// and write4k at those the criteria do not compare, each followed by one
	// floor run.
	for _, shape := range []string{"headread4k", "headwrite4k", "indexrtt"} {
		for _, n := range c.Conc {
			steps = append(steps, c.cellModes(shape, n)...)
		}
	}
	for _, shape := range []string{"read4k", "write4k"} {
		for _, n := range other {
			steps = append(steps, c.cellModes(shape, n)...)
			steps = append(steps, floorStep{shape: shape, conc: n})
		}
	}
	for _, shape := range []string{"read4k", "write4k"} {
		for _, n := range judged {
			steps = append(steps, c.judgedCell(shape, n)...)
		}
	}
	// read4m runs bracketed too: the report shows it against its floor, but
	// does not judge it, since rados bench's 4 MiB rand does not read as the
	// Go cell does.
	for _, shape := range []string{"read4m", "write4m"} {
		for _, n := range c.LargeConc {
			steps = append(steps, c.judgedCell(shape, n)...)
		}
	}
	// Per asynchronous mode, write4m runs at 64 in flight past the byte
	// budget, once with the in-flight limiter's bounds above what the cell
	// reaches and once with them derived from the objecter throttle.
	for _, mode := range []string{"callback", "pipe"} {
		for _, lifted := range []bool{true, false} {
			cell := append([]string{}, overCell...)
			if lifted {
				cell = append(cell, noLimit...)
			}
			cell = append(cell, "-test.benchtime="+strconv.FormatInt(c.OverObjects, 10)+"x")
			steps = append(steps, benchStep{
				mode: mode, results: "overbudget-" + mode + ".jsonl", cell: cell,
				wait: "the over-budget cell in " + mode + " mode",
			})
		}
	}
	// Per mode, read4k runs at 256 under a CPU profile.
	for _, mode := range modes {
		steps = append(steps, benchStep{
			mode: mode, cell: []string{"-shapes=read4k", "-conc=256"}, profile: "cpu-" + mode + "-read4k-256.pprof",
		})
	}
	return steps
}

// cellModes runs a cell in each mode, each in a benchmark process of its own,
// so that the cell's thread growth is its own and not what an earlier cell of
// its process left.
func (c Config) cellModes(shape string, conc int) []step {
	var steps []step
	for _, mode := range modes {
		steps = append(steps, benchStep{mode: mode, results: "seam-" + mode + ".jsonl", cell: cellArgs(shape, conc)})
	}
	return steps
}

// judgedCell runs a cell the criteria compare between two floor runs, one
// just before its three modes and one just after, so that the floor brackets
// the cell rather than running long after it; then, where the cluster runs
// another release, the cluster image's floor once. A 4 MiB write cell writes
// Write4mObjects objects, not a time window, so that it writes the bytes its
// floor does, and waits for the pool's free space first.
func (c Config) judgedCell(shape string, conc int) []step {
	steps := []step{floorStep{shape: shape, conc: conc, bracket: "before"}}
	for _, mode := range modes {
		b := benchStep{mode: mode, results: "seam-" + mode + ".jsonl", cell: cellArgs(shape, conc)}
		if shape == "write4m" {
			b.cell = append(b.cell, "-test.benchtime="+strconv.FormatInt(c.Write4mObjects, 10)+"x")
			b.wait = fmt.Sprintf("write4m at %d in %s mode", conc, mode)
		}
		steps = append(steps, b)
	}
	return append(steps,
		floorStep{shape: shape, conc: conc, bracket: "after"},
		floorStep{shape: shape, conc: conc, cluster: true})
}

func cellArgs(shape string, conc int) []string {
	return []string{"-shapes=" + shape, "-conc=" + strconv.Itoa(conc)}
}

// WritePlan writes the run's schedule to w, in the order the run takes it:
// the run directory, then each benchmark process with its cell, its results
// file and its watchdog limit, each floor run with its limit, and each wait
// for the pool's free space.
func (c Config) WritePlan(w io.Writer) {
	if c.Limiter {
		fmt.Fprintf(w, "seam: the %s limiter re-run at %d B, into hack/bench/out/%s/seam-<UTC time>%s/\n",
			c.Release, c.InflightBytes, c.Release, c.Suffix())
	} else {
		fmt.Fprintf(w, "seam: the %s sweep, into hack/bench/out/%s/seam-<UTC time>/\n", c.Release, c.Release)
	}
	for _, s := range c.schedule() {
		for _, line := range s.plan(c) {
			fmt.Fprintln(w, line)
		}
	}
}
