package report

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Frame is a cgo frame and the CPU time of the samples whose stacks hold it.
type Frame struct {
	Name string
	Cum  time.Duration
}

// Share is how much of a CPU profile's sampled time has a cgo frame on its
// stack: Cgo of Total, and each cgo frame's cumulative time, largest first.
//
// InC is the part of Cgo whose leaf is runtime.cgocall. The runtime records
// a sample taken while a Go thread runs C under the Go stack that made the
// call, which ends in runtime.cgocall (sigprof), so InC is the C, librados's
// included, that Go calls ran, and the crossing itself. External is the time
// sampled on threads the runtime does not run, librados's own among them,
// which the runtime records under runtime._ExternalCode (sigprofNonGoPC).
type Share struct {
	Total, Cgo    time.Duration
	InC, External time.Duration
	Frames        []Frame
}

// percent is d as a percentage of the profile's sampled time.
func (s Share) percent(d time.Duration) float64 {
	if s.Total == 0 {
		return 0
	}
	return 100 * float64(d) / float64(s.Total)
}

// cgoFrame reports whether a function is a frame of the cgo boundary: the
// call into C, the callback from C, a cgo-generated stub, or the Pinner that
// keeps Go memory in place for C.
func cgoFrame(name string) bool {
	return name == "runtime.cgocall" ||
		strings.HasPrefix(name, "runtime.cgocallback") ||
		strings.HasPrefix(name, "_Cfunc_") || strings.Contains(name, "._Cfunc_") ||
		strings.HasPrefix(name, "runtime.(*Pinner).")
}

// CgoShare reads the CPU profile at path through go tool pprof's -traces
// listing, one stack per distinct sample.
func CgoShare(ctx context.Context, path string) (Share, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "go", "tool", "pprof", "-traces", "-unit=ns", path) //nolint:gosec // a profile in the directory the command line names, passed as one argument
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Share{}, fmt.Errorf("go tool pprof -traces %s: %w: %s", path, err, bytes.TrimSpace(stderr.Bytes()))
	}
	s, err := parseTraces(bytes.NewReader(out))
	if err != nil {
		return Share{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// traceSep opens each sample in go tool pprof's -traces listing; the lines
// before the first are the profile's header.
const traceSep = "-----------+-"

// parseTraces reads a -traces listing with -unit=ns: each sample is its value
// and leaf frame on one line, then one frame per line up to the root, with
// " (inline)" after a frame the compiler inlined.
func parseTraces(r io.Reader) (Share, error) {
	var (
		s       Share
		cum     = map[string]time.Duration{}
		value   time.Duration
		frames  []string
		started bool
		samples int
	)
	flush := func() {
		if frames == nil {
			return
		}
		s.Total += value
		if frames[0] == "runtime.cgocall" {
			s.InC += value
		}
		if slices.Contains(frames, "runtime._ExternalCode") {
			s.External += value
		}
		hit := false
		for _, f := range slices.Compact(slices.Sorted(slices.Values(frames))) {
			if cgoFrame(f) {
				cum[f] += value
				hit = true
			}
		}
		if hit {
			s.Cgo += value
		}
		frames = nil
		samples++
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, traceSep):
			flush()
			started = true
		case !started || strings.TrimSpace(line) == "":
		case frames == nil:
			v, leaf, ok := strings.Cut(strings.TrimSpace(line), " ")
			ns, unit := strings.CutSuffix(v, "ns")
			n, err := strconv.ParseInt(ns, 10, 64)
			if !ok || !unit || err != nil {
				return Share{}, fmt.Errorf("sample line %q does not start with a value in ns", line)
			}
			value = time.Duration(n)
			frames = append(frames, frameName(leaf))
		default:
			frames = append(frames, frameName(line))
		}
	}
	if err := sc.Err(); err != nil {
		return Share{}, err
	}
	flush()
	if samples == 0 {
		return Share{}, errors.New("the profile has no samples")
	}
	for _, name := range slices.Sorted(maps.Keys(cum)) {
		s.Frames = append(s.Frames, Frame{Name: name, Cum: cum[name]})
	}
	slices.SortStableFunc(s.Frames, func(a, b Frame) int { return cmp.Compare(b.Cum, a.Cum) })
	return s, nil
}

func frameName(line string) string {
	name := strings.TrimSpace(line)
	name, _ = strings.CutSuffix(name, " (inline)")
	return name
}
