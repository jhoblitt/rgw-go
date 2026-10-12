package parity

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// outcomeRetired is the outcome of the entry that retires a test the
// selection no longer runs.
const outcomeRetired = "retired"

// The meta keys a record must carry to append to a ledger, besides one of
// sourceKeys.
const (
	metaRelease     = "release"
	metaCephVersion = "ceph_version"
)

// sourceKeys are the meta keys that name a suite's source: the s3-tests
// commit and the go-ceph tag.
var sourceKeys = []string{"s3tests_commit", "go_ceph_tag"}

// minRuns is the fewest runs a ledger entry is recorded from, so that a test
// whose outcome flips between them shows as unstable.
const minRuns = 2

// Admission is how a pin joined a ledger's chain.
type Admission string

// The admissions: a base pin was recorded whole, an additive one was proven
// to only add tests over the pin before it, and a reworded one to have the
// same trees as the pin before it.
const (
	AdmitBase     Admission = "base"
	AdmitAdditive Admission = "additive"
	AdmitReworded Admission = "reworded"
)

// Pin is a link of a ledger's chain: a source the suite was pinned at.
// Reason says why a base pin after the first was recorded whole: such a
// pin re-stamps every outcome at a source no proof ties to the one before,
// so a person must say why.
type Pin struct {
	Source   string    `json:"source"`
	Admitted Admission `json:"admitted"`
	Reason   string    `json:"reason,omitempty"`
}

// Entry is one recorded state of a test: its outcome on the source and Ceph
// version it was recorded at. Meta holds the record's other meta pairs.
type Entry struct {
	Outcome     string            `json:"outcome"`
	Source      string            `json:"source"`
	CephVersion string            `json:"ceph_version"`
	Recorded    string            `json:"recorded"`
	Runs        int               `json:"runs"`
	Unstable    bool              `json:"unstable"`
	Reason      string            `json:"reason,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

func (e Entry) equal(o Entry) bool {
	return e.Outcome == o.Outcome && e.Source == o.Source && e.CephVersion == o.CephVersion &&
		e.Recorded == o.Recorded && e.Runs == o.Runs && e.Unstable == o.Unstable && e.Reason == o.Reason &&
		maps.Equal(e.Meta, o.Meta)
}

func (e Entry) state() string {
	if e.Unstable {
		return e.Outcome + " (unstable)"
	}
	return e.Outcome
}

// change describes next against prev, the entry before it, and reports
// whether next needs a reason: it changes the outcome or the unstable flag,
// or retires the test. Two unstable entries' outcomes are not compared: an
// unstable test's outcome is whichever its last run gave.
func change(prev, next Entry) (string, bool) {
	desc := prev.state() + " -> " + next.state()
	switch {
	case next.Outcome == outcomeRetired, prev.Unstable != next.Unstable:
		return desc, true
	case prev.Unstable:
		return desc, false
	}
	return desc, prev.Outcome != next.Outcome
}

// Ledger is a baseline: radosgw's outcomes for one suite on one release, kept
// append-only. Pins is the chain of sources the suite has been pinned at,
// oldest first, and Tests holds each test's entries, oldest first; a test's
// state is its last entry. SourceKey is the meta key that names a source.
type Ledger struct {
	Suite     string             `json:"suite"`
	Release   string             `json:"release"`
	SourceKey string             `json:"source_key"`
	Pins      []Pin              `json:"pins"`
	Tests     map[string][]Entry `json:"tests"`
}

func (l *Ledger) head() string {
	return l.Pins[len(l.Pins)-1].Source
}

// pinIndex returns the index of source's pin in the chain, or -1.
func (l *Ledger) pinIndex(source string) int {
	return slices.IndexFunc(l.Pins, func(p Pin) bool { return p.Source == source })
}

// lastBase returns the index of the chain's last base pin.
func (l *Ledger) lastBase() int {
	for i, p := range slices.Backward(l.Pins) {
		if p.Admitted == AdmitBase {
			return i
		}
	}
	return 0
}

// current reports whether e's source is pinned at or after the chain's last
// base pin, so that no record since has replaced the tests it ran.
func (l *Ledger) current(e Entry) bool {
	return l.pinIndex(e.Source) >= l.lastBase()
}

func (l *Ledger) last(id string) (Entry, bool) {
	entries := l.Tests[id]
	if len(entries) == 0 {
		return Entry{}, false
	}
	return entries[len(entries)-1], true
}

func (l *Ledger) clone() *Ledger {
	c := *l
	c.Pins = slices.Clone(l.Pins)
	c.Tests = make(map[string][]Entry, len(l.Tests))
	for id, entries := range l.Tests {
		c.Tests[id] = slices.Clone(entries)
	}
	return &c
}

// entryFields are the members an entry carries as fields of its own, as
// the ledger names them.
var entryFields = []string{"outcome", "source", "ceph_version", "recorded", "runs", "unstable", "reason", "meta"}

// reservedMeta reports whether key names a field of an entry, a source key
// or the release, in any case, so that an entry's meta cannot show a reader
// a value the entry does not hold.
func reservedMeta(key string) bool {
	for _, f := range slices.Concat(entryFields, sourceKeys, []string{metaRelease}) {
		if strings.EqualFold(key, f) {
			return true
		}
	}
	return false
}

// DraftMarker starts the reason record --draft gives an entry that needs
// one: the change is recorded but nobody has explained it yet. A ledger
// that holds one is a draft, which diff compares against but validate and
// check refuse, so that it cannot be committed as it is.
const DraftMarker = "unreviewed:"

// isDraftReason reports whether reason is a draft's: its first word, after
// anything that is not a letter or a digit, is "unreviewed" in any case, so
// that no spelling of the marker passes for a reviewed reason.
func isDraftReason(reason string) bool {
	t := strings.TrimLeftFunc(reason, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	word := t[:len(t)-len(strings.TrimLeftFunc(t, unicode.IsLetter))]
	return strings.EqualFold(word, strings.TrimSuffix(DraftMarker, ":"))
}

// printable reports whether s is UTF-8 that holds no control character,
// format character or line separator, which could hide or forge text
// where it is shown.
func printable(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unsafeRune)
}

// tokenProblem returns why s cannot name a test, a source, a suite, a
// release or a version, or "": it must be printable ASCII with no blank, so
// that no letter can pass for another, and not start with "-", which a
// command line would read as an option.
func tokenProblem(s string) string {
	switch {
	case strings.HasPrefix(s, "-"):
		return `starts with "-"`
	case strings.ContainsFunc(s, func(r rune) bool { return r < '!' || r > '~' }):
		return "is not printable ASCII without blanks"
	}
	return ""
}

// readable reports whether a reason says something: it holds a letter or a
// digit, not only blanks, punctuation or invisible characters.
func readable(reason string) bool {
	return strings.ContainsFunc(reason, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) })
}

// Validate returns every way l breaks the ledger's schema or the reason
// rule, one line each, by test id after the ledger's own. A reason that
// starts with DraftMarker breaks the rule.
func Validate(l *Ledger) []string {
	return validate(l, false)
}

// ValidateDraft is Validate for a draft: a reason that starts with
// DraftMarker keeps the reason rule.
func ValidateDraft(l *Ledger) []string {
	return validate(l, true)
}

func validate(l *Ledger, draft bool) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}
	for _, f := range []struct{ name, value string }{{"suite", l.Suite}, {"release", l.Release}} {
		if f.value == "" {
			add("no %s", f.name)
		} else if why := tokenProblem(f.value); why != "" {
			add("the %s %q %s", f.name, f.value, why)
		}
	}
	if !slices.Contains(sourceKeys, l.SourceKey) {
		add("source_key %q is not one of %s", l.SourceKey, strings.Join(sourceKeys, ", "))
	}

	pinned := map[string]int{}
	for i, p := range l.Pins {
		switch first, again := pinned[p.Source]; {
		case p.Source == "":
			add("pin %d has no source", i+1)
		case tokenProblem(p.Source) != "":
			add("pin %d's source %q %s", i+1, p.Source, tokenProblem(p.Source))
		case again:
			add("pin %d pins %s again, after pin %d", i+1, p.Source, first+1)
		default:
			pinned[p.Source] = i
		}
		switch p.Admitted {
		case AdmitBase, AdmitAdditive, AdmitReworded:
		default:
			add("pin %d is admitted %q, not %s, %s or %s", i+1, p.Admitted, AdmitBase, AdmitAdditive, AdmitReworded)
		}
		switch {
		case !printable(p.Reason):
			add("pin %d's reason %q holds a control or format character", i+1, p.Reason)
		case i > 0 && p.Admitted == AdmitBase && !readable(p.Reason):
			add("pin %d (%s, %s) needs a reason: it re-stamps outcomes at an unproven source", i+1, p.Source, p.Admitted)
		case !draft && isDraftReason(p.Reason):
			add("pin %d's reason %q is a draft's; replace it with the pin's own before committing", i+1, p.Reason)
		}
	}
	switch {
	case len(l.Pins) == 0:
		add("no pin")
	case l.Pins[0].Admitted != AdmitBase:
		add("pin 1 is admitted %s: a chain starts with a %s pin", l.Pins[0].Admitted, AdmitBase)
	}

	if len(l.Tests) == 0 {
		add("no test")
	}
	for _, id := range slices.Sorted(maps.Keys(l.Tests)) {
		entries := l.Tests[id]
		if len(entries) == 0 {
			add("%s: no entry", id)
		}
		if why := tokenProblem(id); why != "" || id == "" {
			add("the test id %q %s", id, cmp.Or(why, "is empty"))
		}
		prevPin := 0
		for i, e := range entries {
			at := fmt.Sprintf("%s entry %d", id, i+1)
			switch e.Outcome {
			case outcomePassed, outcomeFailed, outcomeError, outcomeSkipped, outcomeRetired:
			default:
				add("%s: the outcome %q is not one of passed, failed, error, skipped or retired", at, e.Outcome)
			}
			switch pin, ok := pinned[e.Source]; {
			case !ok:
				add("%s: the source %q is not pinned", at, e.Source)
			case pin < prevPin:
				add("%s: the source %s is pinned before the previous entry's", at, e.Source)
			default:
				prevPin = pin
			}
			if e.CephVersion == "" {
				add("%s: no ceph_version", at)
			} else if why := tokenProblem(e.CephVersion); why != "" {
				add("%s: the ceph_version %q %s", at, e.CephVersion, why)
			}
			if _, err := time.Parse(time.RFC3339, e.Recorded); err != nil {
				add("%s: recorded %q is not an RFC 3339 time", at, e.Recorded)
			}
			if e.Runs < minRuns {
				add("%s: recorded from %d runs, fewer than %d", at, e.Runs, minRuns)
			}
			for _, k := range slices.Sorted(maps.Keys(e.Meta)) {
				switch {
				case reservedMeta(k):
					add("%s: meta repeats %s", at, k)
				case !printable(k) || !printable(e.Meta[k]):
					add("%s: the meta pair %q=%q holds a control or format character", at, k, e.Meta[k])
				}
			}
			if !printable(e.Reason) {
				add("%s: the reason %q holds a control or format character", at, e.Reason)
			}
			if e.Outcome == outcomeRetired {
				if i == 0 {
					add("%s: a test's first entry cannot retire it", at)
				}
				if e.Unstable {
					add("%s: a retired test cannot be unstable", at)
				}
			}
			if i > 0 {
				if desc, needs := change(entries[i-1], e); needs && !readable(e.Reason) {
					add("%s: %s without a reason", at, desc)
				}
			}
			if !draft && isDraftReason(e.Reason) {
				add("%s: the draft's reason %q; replace it with the change's own before committing", at, e.Reason)
			}
		}
	}
	return problems
}

// CheckAppend returns every way next is not old with only pins and entries
// appended: old's pins and each of its tests' entries must be next's, in
// place and unchanged, and next must be valid, every appended entry obeying
// the reason rule; draft lets next be a draft. A problem with old itself
// starts "old: ".
func CheckAppend(old, next *Ledger, draft bool) []string {
	var problems []string
	for _, p := range validate(old, draft) {
		problems = append(problems, "old: "+p)
	}
	problems = append(problems, validate(next, draft)...)
	for _, f := range []struct{ name, old, next string }{
		{"suite", old.Suite, next.Suite},
		{"release", old.Release, next.Release},
		{"source_key", old.SourceKey, next.SourceKey},
	} {
		if f.old != f.next {
			problems = append(problems, fmt.Sprintf("%s: old %s, new %s", f.name, f.old, f.next))
		}
	}
	for i, p := range old.Pins {
		switch {
		case i >= len(next.Pins):
			problems = append(problems, fmt.Sprintf("pin %d (%s, %s): removed", i+1, p.Source, p.Admitted))
		case next.Pins[i].Source != p.Source || next.Pins[i].Admitted != p.Admitted:
			problems = append(problems, fmt.Sprintf("pin %d: old %s, %s, new %s, %s: a pin is never changed or moved",
				i+1, p.Source, p.Admitted, next.Pins[i].Source, next.Pins[i].Admitted))
		case next.Pins[i].Reason != p.Reason:
			problems = append(problems, fmt.Sprintf("pin %d (%s, %s): the reason changed; a pin is never changed", i+1, p.Source, p.Admitted))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(old.Tests)) {
		nextEntries, ok := next.Tests[id]
		if !ok {
			problems = append(problems, id+": removed")
			continue
		}
		for i, e := range old.Tests[id] {
			switch {
			case i >= len(nextEntries):
				problems = append(problems, fmt.Sprintf("%s entry %d: removed", id, i+1))
			case !nextEntries[i].equal(e):
				problems = append(problems, fmt.Sprintf("%s entry %d: changed or moved; an entry is never edited", id, i+1))
			}
		}
	}
	return problems
}

// Reasoned returns, by test id, each entry next appends to old that needed a
// reason, the entries that change an outcome or an unstable flag or retire
// a test: "<test>: <old state> -> <new state>: <reason>". A nil old is a
// ledger next starts. It assumes CheckAppend found nothing.
func Reasoned(old, next *Ledger) []string {
	var lines []string
	for _, id := range slices.Sorted(maps.Keys(next.Tests)) {
		entries, from := next.Tests[id], 0
		if old != nil {
			from = len(old.Tests[id])
		}
		for i := max(from, 1); i < len(entries); i++ {
			if desc, needs := change(entries[i-1], entries[i]); needs {
				lines = append(lines, fmt.Sprintf("%s: %s: %s", id, desc, entries[i].Reason))
			}
		}
	}
	return lines
}

// ReasonedPins returns each pin next appends to old's chain that carries a
// reason: "pin <n> (<source>, <admitted>): <reason>". A nil old is a ledger
// next starts.
func ReasonedPins(old, next *Ledger) []string {
	from := 0
	if old != nil {
		from = len(old.Pins)
	}
	var lines []string
	for i := from; i < len(next.Pins); i++ {
		if p := next.Pins[i]; p.Reason != "" {
			lines = append(lines, fmt.Sprintf("pin %d (%s, %s): %s", i+1, p.Source, p.Admitted, p.Reason))
		}
	}
	return lines
}

// AddPin appends a pin to l's chain, admitted additive or reworded. A base
// pin comes only with the full record that replaces every test's outcome.
func AddPin(l *Ledger, source string, admitted Admission) error {
	switch admitted {
	case AdmitAdditive, AdmitReworded:
	case AdmitBase:
		return fmt.Errorf("a %s pin is appended by a full record, never alone", AdmitBase)
	default:
		return fmt.Errorf("a pin is admitted %s or %s, not %q", AdmitAdditive, AdmitReworded, admitted)
	}
	if source == "" {
		return errors.New("no source to pin")
	}
	if i := l.pinIndex(source); i >= 0 {
		return fmt.Errorf("%s is already pin %d of the chain", source, i+1)
	}
	l.Pins = append(l.Pins, Pin{Source: source, Admitted: admitted})
	return nil
}

// stamp is what a record's meta gives every entry it appends.
type stamp struct {
	release, sourceKey, source string
	entry                      Entry
}

// stampOf reads res's meta: release, ceph_version, recorded and runs, and
// exactly one of sourceKeys. Its other pairs become each entry's meta.
func stampOf(res Result) (stamp, error) {
	var st stamp
	var errs []error
	need := func(key string) string {
		v := res.Meta[key]
		if v == "" {
			errs = append(errs, fmt.Errorf("the meta has no %s", key))
		}
		return v
	}
	st.release = need(metaRelease)
	st.entry.CephVersion = need(metaCephVersion)
	st.entry.Recorded = need(metaRecorded)
	if runs := need(metaRuns); runs != "" {
		n, err := strconv.Atoi(runs)
		if err != nil {
			errs = append(errs, fmt.Errorf("the meta's runs %q is not a number", runs))
		}
		st.entry.Runs = n
	}
	var keys []string
	for _, k := range sourceKeys {
		if v, ok := res.Meta[k]; ok {
			keys = append(keys, k)
			st.sourceKey, st.source = k, v
		}
	}
	if len(keys) != 1 {
		errs = append(errs, fmt.Errorf("the meta carries %d of the source keys %s, not one", len(keys), strings.Join(sourceKeys, ", ")))
	}
	if st.source == "" && len(keys) == 1 {
		errs = append(errs, fmt.Errorf("the meta's %s is empty", st.sourceKey))
	}
	st.entry.Source = st.source
	for _, k := range slices.Sorted(maps.Keys(res.Meta)) {
		switch {
		case k == metaRelease || k == metaCephVersion || k == metaRecorded || k == metaRuns || slices.Contains(sourceKeys, k):
		case reservedMeta(k):
			errs = append(errs, fmt.Errorf("the meta's %s would pass for a field of the entry", k))
		default:
			if st.entry.Meta == nil {
				st.entry.Meta = map[string]string{}
			}
			st.entry.Meta[k] = res.Meta[k]
		}
	}
	return st, errors.Join(errs...)
}

func (st stamp) with(outcome string, unstable bool) Entry {
	e := st.entry
	e.Outcome, e.Unstable = outcome, unstable
	return e
}

// RecordOptions are how AppendRecord appends a record to a ledger.
type RecordOptions struct {
	// Subset marks a record of chosen tests, which leaves the ledger's other
	// tests alone; a full record retires every test it did not run.
	Subset bool
	// Reasons holds, by test id, the reason for the test's appended entry.
	Reasons map[string]string
	// Draft, when set, makes the record a draft: every appended entry that
	// needs a reason Reasons does not give gets DraftMarker and Draft, and so
	// does a base pin PinReason gives none.
	Draft string
	// PinReason is the reason for the pin a record starts a ledger with, or
	// for the base pin a full record at a source the chain does not pin
	// appends, which needs one.
	PinReason string
}

// ParseReasons reads a reasons file: on each line a test id, then after
// blanks the reason for its appended entry. A line whose first non-blank is
// "#" is a comment, and blank lines are ignored; a reason may hold a "#",
// but cannot start with DraftMarker.
func ParseReasons(r io.Reader) (map[string]string, error) {
	reasons := map[string]string{}
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		id, reason := text, ""
		if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
			id, reason = text[:i], strings.TrimSpace(text[i:])
		}
		switch _, dup := reasons[id]; {
		case reason == "":
			return nil, fmt.Errorf("line %d: %s has no reason", line, id)
		case dup:
			return nil, fmt.Errorf("line %d: %s has a reason already", line, id)
		case isDraftReason(reason):
			return nil, fmt.Errorf("line %d: %s's reason starts with %q, which marks a draft", line, id, DraftMarker)
		case !printable(reason) || !readable(reason):
			return nil, fmt.Errorf("line %d: %s's reason %q is not one a reader can read", line, id, reason)
		}
		reasons[id] = reason
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading the reasons: %w", err)
	}
	return reasons, nil
}

// Appended is an entry AppendRecord appended to a test.
type Appended struct {
	ID    string
	Entry Entry
	// Change describes the entry against the test's previous one, and is
	// empty for its first.
	Change string
	// Reasoned is set when the entry needed a reason.
	Reasoned bool
}

// AppendRecord appends res, a result Record returned with its recording time
// in its meta, to l, and returns the ledger it makes and the entries it
// appended; l is left unchanged. A nil l starts a ledger whose chain is one
// base pin at res's source.
//
// res must carry release, ceph_version, recorded and runs, at least 2, and
// one source key, and match l's suite, release and source key. Its source
// must be the chain's head, or, for a full record, a source the chain does
// not pin, which a base pin with opts.PinReason, or a draft's, then joins.
// opts.PinReason must be readable and not a draft's, and is refused for a
// record that starts or appends no pin. Each test of res gets an entry
// unless its last entry is a current, valid one in the same state; a full
// record retires every other test whose last entry does not already. It
// fails, listing every one, when an entry needs a reason opts does not give,
// or opts gives a reason for a test it does not append. The ledger it makes
// is a draft when opts.Draft gave a reason, or l was one.
func AppendRecord(l *Ledger, res Result, opts RecordOptions) (*Ledger, []Appended, error) {
	st, err := stampOf(res)
	if err != nil {
		return nil, nil, err
	}
	if st.entry.Runs < minRuns {
		return nil, nil, fmt.Errorf("a ledger takes a record of at least %d runs, so that a flaky test shows as unstable, not %d",
			minRuns, st.entry.Runs)
	}
	switch r := opts.PinReason; {
	case r == "":
	case isDraftReason(r):
		return nil, nil, fmt.Errorf("the pin's reason starts with %q, which marks a draft", DraftMarker)
	case !printable(r) || !readable(r):
		return nil, nil, fmt.Errorf("the pin's reason %q is not one a reader can read", r)
	}

	var out *Ledger
	if l == nil {
		if opts.Subset {
			return nil, nil, errors.New("a ledger starts with a full record, not a subset")
		}
		out = &Ledger{
			Suite: res.Suite, Release: st.release, SourceKey: st.sourceKey,
			Pins: []Pin{{Source: st.source, Admitted: AdmitBase, Reason: opts.PinReason}}, Tests: map[string][]Entry{},
		}
	} else {
		var errs []error
		for _, f := range []struct{ name, ledger, record string }{
			{"suite", l.Suite, res.Suite},
			{"release", l.Release, st.release},
			{"source key", l.SourceKey, st.sourceKey},
		} {
			if f.ledger != f.record {
				errs = append(errs, fmt.Errorf("the record's %s is %s, the ledger's %s", f.name, f.record, f.ledger))
			}
		}
		if err := errors.Join(errs...); err != nil {
			return nil, nil, err
		}
		out = l.clone()
		switch i := out.pinIndex(st.source); {
		case i == len(out.Pins)-1 && opts.PinReason != "":
			return nil, nil, fmt.Errorf("a pin reason, but the record ran at the chain's head %s and appends no pin", st.source)
		case i == len(out.Pins)-1:
		case i >= 0:
			return nil, nil, fmt.Errorf("the record ran at %s, pin %d of the chain, not at its head %s", st.source, i+1, out.head())
		case opts.Subset:
			return nil, nil, fmt.Errorf("the record ran at %s, which the chain does not pin: a subset record runs at the head %s, "+
				"so pin the new source first, or make a full record, which appends a %s pin", st.source, out.head(), AdmitBase)
		default:
			reason := opts.PinReason
			if reason == "" && opts.Draft != "" {
				reason = DraftMarker + " " + opts.Draft
			}
			if reason == "" {
				return nil, nil, fmt.Errorf("a full record at %s appends a %s pin, which needs --pin-reason: "+
					"it re-stamps every outcome at a source no proof ties to %s", st.source, AdmitBase, out.head())
			}
			out.Pins = append(out.Pins, Pin{Source: st.source, Admitted: AdmitBase, Reason: reason})
		}
	}

	var appended []Appended
	var offenders []error
	used := map[string]bool{}
	add := func(id string, e Entry) {
		a := Appended{ID: id, Entry: e}
		if prev, ok := out.last(id); ok {
			a.Change, a.Reasoned = change(prev, e)
		}
		if reason, ok := opts.Reasons[id]; ok {
			used[id] = true
			a.Entry.Reason = reason
		} else if a.Reasoned && opts.Draft != "" {
			a.Entry.Reason = DraftMarker + " " + opts.Draft
		}
		if a.Reasoned && !readable(a.Entry.Reason) {
			offenders = append(offenders, fmt.Errorf("%s: %s needs a reason", id, a.Change))
		}
		out.Tests[id] = append(out.Tests[id], a.Entry)
		appended = append(appended, a)
	}

	ledgerIDs := slices.Sorted(maps.Keys(out.Tests))
	unstable := map[string]bool{}
	for _, id := range res.Unstable {
		unstable[id] = true
	}
	for _, id := range slices.Sorted(maps.Keys(res.Outcomes)) {
		e := st.with(res.Outcomes[id], unstable[id])
		if prev, ok := out.last(id); ok && !out.stale(prev, e) {
			continue
		}
		add(id, e)
	}
	if !opts.Subset {
		for _, id := range ledgerIDs {
			if _, ran := res.Outcomes[id]; ran {
				continue
			}
			if prev, _ := out.last(id); prev.Outcome != outcomeRetired {
				add(id, st.with(outcomeRetired, false))
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(opts.Reasons)) {
		switch {
		case !used[id]:
			offenders = append(offenders, fmt.Errorf("%s: a reason for a test this record does not append", id))
		case isDraftReason(opts.Reasons[id]):
			offenders = append(offenders, fmt.Errorf("%s: the reason starts with %q, which marks a draft", id, DraftMarker))
		}
	}
	if err := errors.Join(offenders...); err != nil {
		return nil, nil, err
	}
	if problems := ValidateDraft(out); len(problems) > 0 {
		return nil, nil, fmt.Errorf("the ledger would be invalid:\n%s", strings.Join(problems, "\n"))
	}
	return out, appended, nil
}

// stale reports whether prev, a test's last entry, no longer stands for
// next, the entry a record would append: it retired the test, it is not
// current or was recorded under another Ceph version, or its state differs.
func (l *Ledger) stale(prev, next Entry) bool {
	if prev.Outcome == outcomeRetired || !l.current(prev) || prev.CephVersion != next.CephVersion {
		return true
	}
	_, needs := change(prev, next)
	return needs
}

// Migrate converts res, a baseline in the flat form ledgers replaced, to a
// ledger whose chain is one base pin at res's source, with one entry per
// test: its outcome, unstable when res lists it, and res's meta.
func Migrate(res Result) (*Ledger, error) {
	if err := res.validate(); err != nil {
		return nil, err
	}
	st, err := stampOf(res)
	if err != nil {
		return nil, err
	}
	unstable := map[string]bool{}
	for _, id := range res.Unstable {
		if _, ok := res.Outcomes[id]; !ok {
			return nil, fmt.Errorf("%s is listed unstable but has no outcome", id)
		}
		unstable[id] = true
	}
	l := &Ledger{
		Suite: res.Suite, Release: st.release, SourceKey: st.sourceKey,
		Pins: []Pin{{Source: st.source, Admitted: AdmitBase}}, Tests: map[string][]Entry{},
	}
	for id, o := range res.Outcomes {
		l.Tests[id] = []Entry{st.with(o, unstable[id])}
	}
	if problems := Validate(l); len(problems) > 0 {
		return nil, fmt.Errorf("the ledger would be invalid:\n%s", strings.Join(problems, "\n"))
	}
	return l, nil
}

// ErrFlatBaseline is the error ParseLedger returns for a baseline in the
// flat form ledgers replaced.
var ErrFlatBaseline = errors.New("a baseline in the flat form, not a ledger")

// ErrNotCanonical is the error ParseLedger returns for a ledger whose bytes
// are not the ones MarshalLedger writes for what they hold.
var ErrNotCanonical = errors.New("not in the form parity writes, each pin and entry on a line of its own")

// ParseLedger reads a ledger through decodeStrict, and refuses one whose
// bytes are not exactly those MarshalLedger writes for it: a ledger then
// reads the same to every reader, a person reading its diff, a line-based
// tool and a JSON library alike.
func ParseLedger(data []byte) (*Ledger, error) {
	l, err := decodeLedger(data)
	if err != nil {
		return nil, err
	}
	canonical, err := MarshalLedger(l)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, data) {
		return nil, ErrNotCanonical
	}
	return l, nil
}

// decodeLedger reads a ledger through decodeStrict, in whatever layout,
// and returns ErrFlatBaseline for the flat form.
func decodeLedger(data []byte) (*Ledger, error) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(data, &probe) == nil {
		for k := range probe {
			if strings.EqualFold(k, "outcomes") {
				return nil, ErrFlatBaseline
			}
		}
	}
	var l Ledger
	if err := decodeStrict(data, &l); err != nil {
		return nil, fmt.Errorf("parsing the ledger: %w", err)
	}
	return &l, nil
}

// MarshalLedger encodes l with each pin and each entry on a line of its own,
// so that appending one adds a line.
func MarshalLedger(l *Ledger) ([]byte, error) {
	var b bytes.Buffer
	var err error
	value := func(v any) {
		if err != nil {
			return
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err = enc.Encode(v); err == nil {
			b.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
		}
	}
	b.WriteString("{\n  \"suite\": ")
	value(l.Suite)
	b.WriteString(",\n  \"release\": ")
	value(l.Release)
	b.WriteString(",\n  \"source_key\": ")
	value(l.SourceKey)
	b.WriteString(",\n  \"pins\": [")
	for i, p := range l.Pins {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    ")
		value(p)
	}
	b.WriteString("\n  ],\n  \"tests\": {")
	for i, id := range slices.Sorted(maps.Keys(l.Tests)) {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n    ")
		value(id)
		b.WriteString(": [")
		for j, e := range l.Tests[id] {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n      ")
			value(e)
		}
		b.WriteString("\n    ]")
	}
	b.WriteString("\n  }\n}\n")
	if err != nil {
		return nil, fmt.Errorf("encoding the ledger: %w", err)
	}
	return b.Bytes(), nil
}
