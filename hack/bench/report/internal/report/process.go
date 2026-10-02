package report

import (
	"errors"
	"fmt"
)

// benchProcess is one benchmark process of a mode: the thread count before
// its first cell, its highest count, and the cells it ran.
type benchProcess struct {
	mode            string
	firstIdle, peak int
	cells           map[cellKey]bool
}

// errMixedProcesses refuses a sweep whose cells are partly from before
// seam.sh recorded process ids, which no layout explains.
var errMixedProcesses = errors.New("some cells carry a process id and some do not")

func (s *sweep) newProcess(mode string, c *Cell) int {
	s.procs = append(s.procs, &benchProcess{mode: mode, firstIdle: c.ThreadsIdle, peak: c.ThreadsPeak, cells: map[cellKey]bool{}})
	return len(s.procs) - 1
}

// assignProcesses returns the index into s.procs of the benchmark process
// each of one mode's lines ran in, in file order. With process ids, lines
// group by id. Without, as in a sweep that ran several cells per process,
// the processes are inferred: the calls of one cell are consecutive lines,
// and on the seam's paths a process's thread count only rises, so a cell
// that starts below its predecessor's process's peak started a new process.
func (s *sweep) assignProcesses(mode string, lines []Cell) ([]int, error) {
	idx := make([]int, len(lines))
	byID := map[string]int{}
	for i := range lines {
		c := &lines[i]
		if (c.Process == "") != s.inferred {
			return nil, errMixedProcesses
		}
		var p int
		switch {
		case !s.inferred:
			q, ok := byID[c.Process]
			if !ok {
				q = s.newProcess(mode, c)
				byID[c.Process] = q
			}
			p = q
		case i > 0 && (sameCell(&lines[i-1], c) || c.ThreadsIdle >= s.procs[idx[i-1]].peak):
			p = idx[i-1]
		default:
			p = s.newProcess(mode, c)
		}
		pr := s.procs[p]
		pr.firstIdle = min(pr.firstIdle, c.ThreadsIdle)
		pr.peak = max(pr.peak, c.ThreadsPeak)
		pr.cells[cellKey{c.Shape, c.Conc}] = true
		idx[i] = p
	}
	return idx, nil
}

func sameCell(a, b *Cell) bool { return a.Shape == b.Shape && a.Conc == b.Conc }

// highWater is the process of mode that grew the most, the highest count any
// of mode's processes reached, and how many there were.
func (s *sweep) highWater(mode string) (largest *benchProcess, highest, n int) {
	for _, p := range s.procs {
		if p.mode != mode {
			continue
		}
		n++
		if largest == nil || p.peak-p.firstIdle > largest.peak-largest.firstIdle {
			largest = p
		}
		highest = max(highest, p.peak)
	}
	return largest, highest, n
}

func (p *benchProcess) String() string {
	return fmt.Sprintf("%d→%d (%+d)", p.firstIdle, p.peak, p.peak-p.firstIdle)
}
