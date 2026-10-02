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
// The other fields divide the profile so that each sample counts in at most
// one of them, the one its frame nearest the leaf decides:
//
//   - External is the time sampled on threads the runtime does not run,
//     librados's own among them, which the runtime records under
//     runtime._ExternalCode (sigprofNonGoPC).
//   - InC is the time whose leaf is runtime.cgocall: the runtime records a
//     sample taken while a Go thread runs C under the Go stack that made the
//     call (sigprof), so InC is the C that Go calls ran, librados's included.
//   - Submit, (i), is the cgo machinery around that C: runtime.cgocall's own
//     Go side, the _Cfunc_ stubs, the Pinner and the runtime.cgoCheck* pointer
//     checks, wherever they run, the handler's nested calls included.
//   - Delivery, (ii), is how a completion reaches Go: the runtime.cgocallback*
//     frames a callback arrives through, or the pipe's drain goroutine and the
//     read it waits in, short of the handler either one calls.
//   - Handler is the Go completion handler, rados.aioComplete, and the Go it
//     calls; a pure-Go client runs one too.
//
// A sample of the handler's nested cgo call lands in InC or Submit, not in
// Handler, because the walk from the leaf meets runtime.cgocall first; Cgo
// counts it once as well, since it counts samples, not frames.
type Share struct {
	Total, Cgo                time.Duration
	InC, External             time.Duration
	Submit, Delivery, Handler time.Duration
	Frames                    []Frame
}

// Boundary is the boundary cost, what a pure-Go client would no longer pay:
// the submit crossing and the delivery mechanism, without librados's C or
// the Go completion handler.
func (s Share) Boundary() time.Duration { return s.Submit + s.Delivery }

// percent is d as a percentage of the profile's sampled time.
func (s Share) percent(d time.Duration) float64 {
	if s.Total == 0 {
		return 0
	}
	return 100 * float64(d) / float64(s.Total)
}

// The frames that tell the boundary cost's parts apart.
const (
	aioComplete  = "github.com/ceph/go-ceph/rados.aioComplete"
	aioPipeDrain = "github.com/ceph/go-ceph/rados.(*aioPipe).drain"
)

// cgoFrame reports whether a function is a frame of the cgo boundary: the
// machinery of a call into C, or the callback from C.
func cgoFrame(name string) bool {
	return cgoMachinery(name) || strings.HasPrefix(name, "runtime.cgocallback")
}

// cgoMachinery reports whether a function is part of a call into C: the call
// itself, a cgo-generated stub, the Pinner that keeps Go memory in place for
// C, or the pointer checks cgo inserts before the call.
func cgoMachinery(name string) bool {
	return name == "runtime.cgocall" ||
		strings.HasPrefix(name, "_Cfunc_") || strings.Contains(name, "._Cfunc_") ||
		strings.HasPrefix(name, "runtime.(*Pinner).") ||
		strings.HasPrefix(name, "runtime.cgoCheck")
}

// part returns the field of s a sample with the stack frames, leaf first,
// counts in, or nil for Go time outside the boundary.
func part(s *Share, frames []string) *time.Duration {
	switch {
	case slices.Contains(frames, "runtime._ExternalCode"):
		return &s.External
	case frames[0] == "runtime.cgocall":
		return &s.InC
	}
	for _, f := range frames {
		switch {
		case f == aioComplete:
			return &s.Handler
		case cgoMachinery(f):
			return &s.Submit
		case strings.HasPrefix(f, "runtime.cgocallback"), f == aioPipeDrain:
			return &s.Delivery
		}
	}
	return nil
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
		if p := part(&s, frames); p != nil {
			*p += value
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
