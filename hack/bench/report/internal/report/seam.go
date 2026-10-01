// Package report renders benchmark results into markdown: a seam sweep
// directory into its REPORT.md, with the verdict of the cgo question's four
// criteria.
package report

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Cell is one line BenchmarkSeam appends to a results file, written for
// every call of a cell. testing.B calls a cell more than once while it
// calibrates b.N, so a cell has several lines, and the one with the largest n
// is its measurement.
type Cell struct {
	Time        string  `json:"time"`
	Release     string  `json:"release"`
	Mode        string  `json:"mode"`
	Shape       string  `json:"shape"`
	BudgetBytes int64   `json:"budget_bytes"`
	Ops         int     `json:"ops_per_iter"`
	Conc        int     `json:"conc"`
	N           int     `json:"n"`
	NsPerOp     float64 `json:"ns_per_iter"`
	OpsPerSec   float64 `json:"ops_per_sec"`
	P50us       float64 `json:"p50_us"`
	P99us       float64 `json:"p99_us"`
	P999us      float64 `json:"p999_us"`
	MeanUs      float64 `json:"mean_us"`
	CPUusPerOp  float64 `json:"cpu_us_per_op"`
	ThreadsIdle int     `json:"threads_idle"`
	ThreadsPeak int     `json:"threads_peak"`
	GOMAXPROCS  int     `json:"gomaxprocs"`
	Errors      int     `json:"errors"`
	// MaxInflightOps and MaxInflightBytes are the in-flight limiter's
	// bounds the cell ran under; 0 derives one from librados's objecter
	// throttle.
	MaxInflightOps   int    `json:"max_inflight_ops"`
	MaxInflightBytes int64  `json:"max_inflight_bytes"`
	ThrottleWaits    uint64 `json:"throttle_waits"`
}

// growth is how many OS threads the cell added to the process.
func (c Cell) growth() int { return c.ThreadsPeak - c.ThreadsIdle }

// allowance is the thread growth the threads criterion allows: GOMAXPROCS
// plus a handful.
func (c Cell) allowance() int { return c.GOMAXPROCS + 8 }

func (c Cell) limiter() string {
	if c.MaxInflightOps == 0 && c.MaxInflightBytes == 0 {
		return "derived"
	}
	bound := func(name string, v int64) string {
		if v == 0 {
			return name + " derived"
		}
		return fmt.Sprintf("%s %d", name, v)
	}
	return bound("ops", int64(c.MaxInflightOps)) + ", " + bound("bytes", c.MaxInflightBytes)
}

// Floor is one rados bench run, a line of floor.jsonl.
type Floor struct {
	Kind  string `json:"kind"`
	Tool  string `json:"tool"`
	Shape string `json:"shape"`
	Op    string `json:"op"`
	Size  int64  `json:"size"`
	Conc  int    `json:"conc"`
	// Seconds and MaxObjects are what the run was given: it stops at
	// whichever it reaches first, and MaxObjects 0 is no limit.
	Seconds    int `json:"seconds"`
	MaxObjects int `json:"max_objects"`
	// Objects is how many objects a rand run read among.
	Objects       int     `json:"objects"`
	TotalOps      int     `json:"ops"`
	ElapsedS      float64 `json:"elapsed_s"`
	OpsPerSec     float64 `json:"ops_per_sec"`
	MeanUs        float64 `json:"mean_us"`
	BandwidthMBps float64 `json:"bandwidth_mbps"`
}

// mirror is a shape rados bench mirrors exactly, with the concurrencies the
// throughput and latency criteria compare it at.
type mirror struct {
	shape string
	concs []int
}

// mirrors compares a 4 MiB shape only where its bytes in flight stay under
// the byte budget.
var mirrors = []mirror{
	{"read4k", []int{64, 256}},
	{"write4k", []int{64, 256}},
	{"read4m", []int{1, 4, 16}},
	{"write4m", []int{1, 4, 16}},
}

// shapeOrder and modeOrder are the sweep's orders; anything else sorts after.
var (
	shapeOrder = []string{"read4k", "write4k", "read4m", "write4m", "headread4k", "headwrite4k", "indexrtt"}
	modeOrder  = []string{"sync", "callback", "pipe"}
)

// judged are the modes the criteria are applied to. sync parks an OS thread
// per operation in flight, so it is the baseline the other two are read
// against, not a candidate.
var judged = map[string]bool{"callback": true, "pipe": true}

type key struct {
	mode, shape string
	conc        int
}

type overKey struct {
	key
	maxOps   int
	maxBytes int64
}

type floorKey struct {
	shape string
	conc  int
}

// sweep is a sweep directory read.
type sweep struct {
	dir      string
	release  string
	env      map[string]any
	envKeys  []string
	cells    map[key]Cell
	modes    []string
	shapes   []string
	over     []Cell
	floors   map[floorKey]Floor
	profiles map[string]profile
}

// profile is a mode's read4k CPU profile at 256 in flight.
type profile struct {
	file  string
	share Share
}

// Seam reads the sweep directory dir and renders it: every seam*.jsonl
// file's cells, overbudget*.jsonl's cells run past the byte budget,
// floor.jsonl's rados bench runs, env.json, and each
// cpu-<mode>-read4k-256.pprof.
func Seam(ctx context.Context, dir string) (SeamReport, error) {
	s, err := readSweep(ctx, dir)
	if err != nil {
		return SeamReport{}, err
	}
	r := SeamReport{Verdicts: s.verdicts()}
	r.Passing = passing(s.modes, r.Verdicts)
	r.Markdown = s.render(r)
	return r, nil
}

func readSweep(ctx context.Context, dir string) (*sweep, error) {
	s := &sweep{dir: dir, cells: map[key]Cell{}, floors: map[floorKey]Floor{}, profiles: map[string]profile{}}
	files, err := filepath.Glob(filepath.Join(dir, "seam*.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := readLines(f, func(c Cell) error { return s.addCell(c) }); err != nil {
			return nil, err
		}
	}
	if len(s.cells) == 0 {
		return nil, fmt.Errorf("no seam cells in %s", dir)
	}
	if err := s.checkEven(); err != nil {
		return nil, err
	}
	if err := s.readOver(); err != nil {
		return nil, err
	}
	if err := s.readFloors(); err != nil {
		return nil, err
	}
	if err := s.readEnv(); err != nil {
		return nil, err
	}
	if err := s.readProfiles(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// readLines decodes every line of the JSON-lines file path into a T and
// hands it to add, naming the file and line in any error.
func readLines[T any](path string, add func(T) error) error {
	data, err := os.ReadFile(path) //nolint:gosec // reading the directory the command line names is the command's purpose
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return fmt.Errorf("%s line %d: %w", path, n, err)
		}
		if err := add(v); err != nil {
			return fmt.Errorf("%s line %d: %w", path, n, err)
		}
	}
	return sc.Err()
}

func validCell(c Cell) error {
	if c.Mode == "" || c.Shape == "" || c.Conc < 1 || c.N < 1 {
		return fmt.Errorf("a cell needs a mode, a shape, conc and n: %+v", c)
	}
	return nil
}

// addCell keeps c when it is the first line of its cell or has a larger n
// than the line kept; of two lines with the same n, the later one.
func (s *sweep) addCell(c Cell) error {
	if err := validCell(c); err != nil {
		return err
	}
	k := key{c.Mode, c.Shape, c.Conc}
	if kept, ok := s.cells[k]; ok {
		if kept.MaxInflightOps != c.MaxInflightOps || kept.MaxInflightBytes != c.MaxInflightBytes {
			return fmt.Errorf("%s %s at conc=%d has lines with different in-flight limits", c.Mode, c.Shape, c.Conc)
		}
		if c.N < kept.N {
			return nil
		}
	}
	s.cells[k] = c
	return nil
}

// checkEven fails when one mode lacks a cell another mode has, and orders
// the modes and shapes.
func (s *sweep) checkEven() error {
	modes, shapes := map[string]bool{}, map[string]bool{}
	cells := map[floorKey]bool{}
	for k := range s.cells {
		modes[k.mode], shapes[k.shape] = true, true
		cells[floorKey{k.shape, k.conc}] = true
	}
	s.modes = ordered(modes, modeOrder)
	s.shapes = ordered(shapes, shapeOrder)
	for _, fk := range slices.SortedFunc(maps.Keys(cells), s.compareCells) {
		for _, m := range s.modes {
			if _, ok := s.cells[key{m, fk.shape, fk.conc}]; !ok {
				return fmt.Errorf("%s: conc=%d missing for mode %s", fk.shape, fk.conc, m)
			}
		}
	}
	return nil
}

func (s *sweep) compareCells(a, b floorKey) int {
	return cmp.Or(
		cmp.Compare(slices.Index(s.shapes, a.shape), slices.Index(s.shapes, b.shape)),
		cmp.Compare(a.conc, b.conc),
	)
}

// ordered returns set's members in the order of known, then the rest sorted.
func ordered(set map[string]bool, known []string) []string {
	var out []string
	for _, k := range known {
		if set[k] {
			out = append(out, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		if !slices.Contains(known, k) {
			out = append(out, k)
		}
	}
	return out
}

// readOver reads the cells run past the byte budget, keeping the largest-n
// line of each cell and in-flight limit.
func (s *sweep) readOver() error {
	files, err := filepath.Glob(filepath.Join(s.dir, "overbudget*.jsonl"))
	if err != nil {
		return err
	}
	kept := map[overKey]Cell{}
	for _, f := range files {
		err := readLines(f, func(c Cell) error {
			if err := validCell(c); err != nil {
				return err
			}
			k := overKey{key{c.Mode, c.Shape, c.Conc}, c.MaxInflightOps, c.MaxInflightBytes}
			if prev, ok := kept[k]; !ok || c.N >= prev.N {
				kept[k] = c
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	s.over = slices.SortedFunc(maps.Values(kept), func(a, b Cell) int {
		return cmp.Or(
			cmp.Compare(rank(a.Mode, modeOrder), rank(b.Mode, modeOrder)),
			cmp.Compare(rank(a.Shape, shapeOrder), rank(b.Shape, shapeOrder)),
			cmp.Compare(a.Conc, b.Conc),
			cmp.Compare(b.MaxInflightBytes, a.MaxInflightBytes),
			cmp.Compare(b.MaxInflightOps, a.MaxInflightOps),
		)
	})
	return nil
}

// rank is v's index in known, or len(known) for anything else.
func rank(v string, known []string) int {
	if i := slices.Index(known, v); i >= 0 {
		return i
	}
	return len(known)
}

func (s *sweep) readFloors() error {
	path := filepath.Join(s.dir, "floor.jsonl")
	err := readLines(path, func(f Floor) error {
		if f.Kind != "floor" {
			return fmt.Errorf("kind %q is not floor", f.Kind)
		}
		if !slices.ContainsFunc(mirrors, func(m mirror) bool { return m.shape == f.Shape }) {
			return fmt.Errorf("shape %q has no rados bench floor", f.Shape)
		}
		k := floorKey{f.Shape, f.Conc}
		if _, dup := s.floors[k]; dup {
			return fmt.Errorf("two floor lines for %s at conc=%d", f.Shape, f.Conc)
		}
		s.floors[k] = f
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// readEnv reads env.json, which seam.sh writes before the sweep, keeping
// its numbers as written.
func (s *sweep) readEnv() error {
	data, err := os.ReadFile(filepath.Join(s.dir, "env.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&s.env); err != nil {
		return fmt.Errorf("parsing env.json: %w", err)
	}
	s.envKeys = slices.Sorted(maps.Keys(s.env))
	if r, ok := s.env["release"].(string); ok {
		s.release = r
	}
	return nil
}

func (s *sweep) readProfiles(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(s.dir, "cpu-*-read4k-256.pprof"))
	if err != nil {
		return err
	}
	for _, f := range files {
		base := filepath.Base(f)
		mode := strings.TrimSuffix(strings.TrimPrefix(base, "cpu-"), "-read4k-256.pprof")
		share, err := CgoShare(ctx, f)
		if err != nil {
			return err
		}
		slog.DebugContext(ctx, "read cpu profile", slog.String("file", f), slog.String("mode", mode))
		s.profiles[mode] = profile{file: base, share: share}
	}
	return nil
}

// releaseName is env.json's release, or else the cells' label.
func (s *sweep) releaseName() string {
	if s.release != "" {
		return s.release
	}
	for _, k := range slices.SortedFunc(maps.Keys(s.cells), func(a, b key) int {
		return cmp.Or(cmp.Compare(a.mode, b.mode), cmp.Compare(a.shape, b.shape), cmp.Compare(a.conc, b.conc))
	}) {
		if r := s.cells[k].Release; r != "" {
			return r
		}
	}
	return "unknown release"
}
