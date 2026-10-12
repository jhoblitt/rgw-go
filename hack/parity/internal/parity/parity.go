// Package parity records test outcomes from run files into result files and
// ledgers, and compares a result with a ledger. hack/parity's package doc is
// the home of both files' schemas, of the comparison's rules and of the
// command line Run implements.
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

// Diff compares cand, a result, with base, a ledger, and returns one line
// per difference, by test id, judging each test by its last entry:
//   - a test with no entry prints "<test>: no baseline entry", unless a
//     known entry, plain or either-outcome, matches it;
//   - a retired test is skipped when cand lacks it, and prints "<test>:
//     baseline retired, candidate <outcome>" when cand has it;
//   - a test whose last entry is not valid prints "<test>: baseline stale,
//     re-record": valid, its source is pinned at or after the chain's last
//     base pin and its ceph_version is cand's;
//   - an unstable test is skipped;
//   - any other test is compared, and prints "<test>: baseline <outcome>,
//     candidate <outcome>" when its outcome differs, with "missing" for a
//     test cand lacks.
//
// A compared test that a plain known entry matches is expected to differ:
// when it does, it is skipped, and when it agrees the line is "<test>: known
// difference no longer differs". A test that only either-outcome entries
// match is skipped whatever its outcome. A retired or stale test's line is
// printed whatever the list says. Each entry that matches no test compared,
// skipped for having no entry, or reported adds, after those, "<entry>:
// known difference matches no test", the entry as ParseKnown read it.
//
// Diff refuses to compare, and returns an error, when cand lacks base's
// source key, release or ceph_version, or, unless allowMetaDrift is set,
// carries a source other than the chain's head or another release.
// allowMetaDrift also compares a test whose last entry was recorded under
// another Ceph version.
func Diff(base *Ledger, cand Result, allowMetaDrift bool, known KnownList) ([]string, error) {
	var drift []string
	for _, m := range []struct{ key, want string }{{base.SourceKey, base.head()}, {metaRelease, base.Release}} {
		switch got, ok := cand.Meta[m.key]; {
		case !ok:
			drift = append(drift, fmt.Sprintf("%s: baseline %s, candidate missing", m.key, m.want))
		case got != m.want && !allowMetaDrift:
			drift = append(drift, fmt.Sprintf("%s: baseline %s, candidate %s", m.key, m.want, got))
		}
	}
	cephVersion, ok := cand.Meta[metaCephVersion]
	if !ok {
		drift = append(drift, metaCephVersion+": candidate missing")
	}
	if len(drift) > 0 {
		return nil, fmt.Errorf("the results ran under different meta (%s)", strings.Join(drift, "; "))
	}

	ids := map[string]bool{}
	for id := range base.Tests {
		ids[id] = true
	}
	for id := range cand.Outcomes {
		ids[id] = true
	}

	matched := make([]bool, len(known.entries))
	var diffs []string
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		c, inCand := cand.Outcomes[id]
		last, inBase := base.last(id)
		var line string
		switch {
		case !inBase:
			if strict, either := known.match(id, matched); strict || either {
				continue
			}
			line = id + ": no baseline entry"
		case last.Outcome == outcomeRetired:
			if !inCand {
				continue
			}
			line = fmt.Sprintf("%s: baseline retired, candidate %s", id, c)
		case !base.current(last) || (last.CephVersion != cephVersion && !allowMetaDrift):
			line = id + ": baseline stale, re-record"
		case last.Unstable:
			continue
		}
		strict, either := known.match(id, matched)
		if line != "" {
			diffs = append(diffs, line)
			continue
		}
		b, c := last.Outcome, outcome(cand.Outcomes, id)
		switch {
		case strict && b == c:
			diffs = append(diffs, id+": known difference no longer differs")
		case !strict && !either && b != c:
			diffs = append(diffs, fmt.Sprintf("%s: baseline %s, candidate %s", id, b, c))
		}
	}
	for i, k := range known.entries {
		if !matched[i] {
			diffs = append(diffs, k.String()+": known difference matches no test")
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

// knownEntry is one entry of a known-difference list. An eitherOutcome entry
// is for a test whose outcome on rgw-go depends on timing, so it may differ
// from radosgw's on one run and agree on the next.
type knownEntry struct {
	pattern       string
	re            *regexp.Regexp
	eitherOutcome bool
}

// String returns the entry in its normalized written form: the pattern,
// after "~ " for an either-outcome entry.
func (k knownEntry) String() string {
	if k.eitherOutcome {
		return eitherOutcomeMarker + " " + k.pattern
	}
	return k.pattern
}

// eitherOutcomeMarker starts an either-outcome entry. No test id starts with
// it, so no plain entry that could match a test is read as one.
const eitherOutcomeMarker = "~"

// match marks in matched the entries that match id and reports whether a
// plain one and an either-outcome one did.
func (l KnownList) match(id string, matched []bool) (strict, either bool) {
	for i, k := range l.entries {
		if k.re.MatchString(id) {
			matched[i] = true
			if k.eitherOutcome {
				either = true
			} else {
				strict = true
			}
		}
	}
	return strict, either
}

// Patterns returns the list's entries in their normalized written form.
func (l KnownList) Patterns() []string {
	patterns := make([]string, 0, len(l.entries))
	for _, k := range l.entries {
		patterns = append(patterns, k.String())
	}
	return patterns
}

// ParseKnown reads a known-difference list: one regular expression per line,
// with a "#" starting a comment that runs to the end of the line, and blank
// lines ignored. A line that starts with "~" is an either-outcome entry: the
// pattern after it and any blanks matches tests whose outcome may differ or
// agree.
func ParseKnown(r io.Reader) (KnownList, error) {
	var l KnownList
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		pattern, _, _ := strings.Cut(sc.Text(), "#")
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		rest, either := strings.CutPrefix(pattern, eitherOutcomeMarker)
		if either {
			pattern = strings.TrimSpace(rest)
			if pattern == "" {
				return KnownList{}, fmt.Errorf("line %d: %s without a pattern", line, eitherOutcomeMarker)
			}
		}
		re, err := regexp.Compile(`^(?:` + pattern + `)$`)
		if err != nil {
			return KnownList{}, fmt.Errorf("line %d: %w", line, err)
		}
		l.entries = append(l.entries, knownEntry{pattern: pattern, re: re, eitherOutcome: either})
	}
	if err := sc.Err(); err != nil {
		return KnownList{}, fmt.Errorf("reading the known-difference list: %w", err)
	}
	return l, nil
}
