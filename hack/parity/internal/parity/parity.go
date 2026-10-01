// Package parity records test outcomes from run files and compares two
// recorded results. hack/parity's package doc is the home of the result
// file's schema and of the command line Run implements.
package parity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Format is the format of a run file.
type Format string

// The run file formats: a junit XML report, as pytest's --junitxml writes,
// and the event stream of go test -json.
const (
	FormatJunit  Format = "junit"
	FormatGoTest Format = "gotest"
)

// The outcomes a test can have.
const (
	outcomePassed  = "passed"
	outcomeFailed  = "failed"
	outcomeError   = "error"
	outcomeSkipped = "skipped"
)

// metaRuns is the meta key Record sets to the number of runs it read.
const metaRuns = "runs"

// comparedMeta are the meta keys whose values Diff requires to agree: a
// difference in any of them means the two results ran different tests or ran
// them against a different Ceph.
var comparedMeta = []string{"s3tests_commit", "go_ceph_tag", "release", "ceph_version", "deselect"}

// Result is a result file: the outcome of every test of a suite and the
// tests whose outcome differed between the runs it was recorded from.
type Result struct {
	Suite    string            `json:"suite"`
	Meta     map[string]string `json:"meta"`
	Outcomes map[string]string `json:"outcomes"`
	Unstable []string          `json:"unstable"`
}

// validate reports whether r holds outcomes and only known ones.
func (r *Result) validate() error {
	if len(r.Outcomes) == 0 {
		return errors.New("no outcomes")
	}
	for _, id := range slices.Sorted(maps.Keys(r.Outcomes)) {
		switch o := r.Outcomes[id]; o {
		case outcomePassed, outcomeFailed, outcomeError, outcomeSkipped:
		default:
			return fmt.Errorf("%s has the outcome %q, not one of passed, failed, error or skipped", id, o)
		}
	}
	return nil
}

// Record reads the run files, in the format given, and returns the suite's
// result: each test's outcome in the last file, every test whose outcome
// differs between any two of them as unstable, and meta with the number of
// runs added. Every file must hold the same tests.
func Record(format Format, suite string, meta map[string]string, files []string) (Result, error) {
	var read func(string) (map[string]string, error)
	switch format {
	case FormatJunit:
		read = readJunit
	case FormatGoTest:
		read = readGoTest
	default:
		return Result{}, fmt.Errorf("unknown run file format %q, want %s or %s", format, FormatJunit, FormatGoTest)
	}
	if len(files) == 0 {
		return Result{}, errors.New("no run file to record")
	}

	runs := make([]map[string]string, 0, len(files))
	all := map[string]bool{}
	for _, file := range files {
		outcomes, err := read(file)
		if err != nil {
			return Result{}, err
		}
		if len(outcomes) == 0 {
			return Result{}, fmt.Errorf("%s holds no test", file)
		}
		for id := range outcomes {
			all[id] = true
		}
		runs = append(runs, outcomes)
	}

	var errs []error
	for i, outcomes := range runs {
		var missing []string
		for id := range all {
			if _, ok := outcomes[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			errs = append(errs, fmt.Errorf("%s lacks tests another run has: %s", files[i], strings.Join(missing, ", ")))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Result{}, err
	}

	last := runs[len(runs)-1]
	unstable := []string{}
	for _, id := range slices.Sorted(maps.Keys(all)) {
		for _, outcomes := range runs {
			if outcomes[id] != last[id] {
				unstable = append(unstable, id)
				break
			}
		}
	}

	m := maps.Clone(meta)
	if m == nil {
		m = map[string]string{}
	}
	m[metaRuns] = strconv.Itoa(len(runs))
	return Result{Suite: suite, Meta: m, Outcomes: last, Unstable: unstable}, nil
}

// Diff compares cand with base and returns one line per difference, by test
// id: "<test>: baseline <outcome>, candidate <outcome>" for a test whose
// outcome differs, with "missing" for a test one of them lacks. Tests in
// base's unstable set are not compared. A compared test that a known entry
// matches is expected to differ: when it does, it is skipped, and when it
// agrees the line is "<test>: known difference no longer differs". Each entry
// that matches no compared test adds, after those, "<pattern>: known
// difference matches no test".
//
// Diff refuses to compare, and returns an error, when cand lacks a key of
// comparedMeta that base carries, or, unless allowMetaDrift is set, carries it
// with another value. A key only cand carries is not compared.
func Diff(base, cand Result, allowMetaDrift bool, known KnownList) ([]string, error) {
	var drift []string
	for _, key := range comparedMeta {
		b, inBase := base.Meta[key]
		if !inBase {
			continue
		}
		c, inCand := cand.Meta[key]
		switch {
		case !inCand:
			drift = append(drift, fmt.Sprintf("%s: baseline %s, candidate missing", key, b))
		case b != c && !allowMetaDrift:
			drift = append(drift, fmt.Sprintf("%s: baseline %s, candidate %s", key, b, c))
		}
	}
	if len(drift) > 0 {
		return nil, fmt.Errorf("the results ran under different meta (%s)", strings.Join(drift, "; "))
	}

	unstable := map[string]bool{}
	for _, id := range base.Unstable {
		unstable[id] = true
	}
	ids := map[string]bool{}
	for id := range base.Outcomes {
		ids[id] = true
	}
	for id := range cand.Outcomes {
		ids[id] = true
	}

	matched := make([]bool, len(known.entries))
	var diffs []string
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		if unstable[id] {
			continue
		}
		b, c := outcome(base.Outcomes, id), outcome(cand.Outcomes, id)
		expected := false
		for i, k := range known.entries {
			if k.re.MatchString(id) {
				matched[i] = true
				expected = true
			}
		}
		switch {
		case expected && b == c:
			diffs = append(diffs, id+": known difference no longer differs")
		case !expected && b != c:
			diffs = append(diffs, fmt.Sprintf("%s: baseline %s, candidate %s", id, b, c))
		}
	}
	for i, k := range known.entries {
		if !matched[i] {
			diffs = append(diffs, k.pattern+": known difference matches no test")
		}
	}
	return diffs, nil
}

func outcome(outcomes map[string]string, id string) string {
	if o, ok := outcomes[id]; ok {
		return o
	}
	return "missing"
}

// KnownList is a known-difference list: regular expressions that must each
// match a test id whole. The zero KnownList is empty, and ParseKnown is the
// only way to fill one, so every entry Diff sees is compiled.
type KnownList struct {
	entries []knownEntry
}

type knownEntry struct {
	pattern string
	re      *regexp.Regexp
}

// Patterns returns the list's entries as they were written.
func (l KnownList) Patterns() []string {
	patterns := make([]string, 0, len(l.entries))
	for _, k := range l.entries {
		patterns = append(patterns, k.pattern)
	}
	return patterns
}

// ParseKnown reads a known-difference list: one regular expression per line,
// with a "#" starting a comment that runs to the end of the line, and blank
// lines ignored.
func ParseKnown(r io.Reader) (KnownList, error) {
	var l KnownList
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		pattern, _, _ := strings.Cut(sc.Text(), "#")
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(`^(?:` + pattern + `)$`)
		if err != nil {
			return KnownList{}, fmt.Errorf("line %d: %w", line, err)
		}
		l.entries = append(l.entries, knownEntry{pattern: pattern, re: re})
	}
	if err := sc.Err(); err != nil {
		return KnownList{}, fmt.Errorf("reading the known-difference list: %w", err)
	}
	return l, nil
}
