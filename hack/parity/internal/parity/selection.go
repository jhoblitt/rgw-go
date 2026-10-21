package parity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// Selection is the tests a run selected, by the ids a junit report gives
// them, and the gate-only tests it left out. The zero Selection selects every
// test, and ParseSelection is the only way to fill one.
type Selection struct {
	ids, gateOnly map[string]bool
}

// holds reports whether s selects the test id.
func (s Selection) holds(id string) bool {
	return s.ids == nil || s.ids[id]
}

// ParseSelection reads the pytest node ids a run selected, one a line, as
// hack/s3tests select prints them, blank lines aside, and returns them by the
// ids pytest's junit report gives them. It refuses a line that is not a node
// id, FILE.py::NAME, and a selection of no test.
func ParseSelection(r io.Reader) (Selection, error) {
	ids, err := readNodeIDs(r)
	if err != nil {
		return Selection{}, fmt.Errorf("reading the selection: %w", err)
	}
	if len(ids) == 0 {
		return Selection{}, errors.New("the selection names no test")
	}
	return Selection{ids: ids}, nil
}

// WithGateOnly returns s with the gate-only tests: the pytest node ids, one a
// line, of the gate set's tests that run at a phase gate and on demand alone,
// as hack/s3tests select --gate-only-ids writes them. It may name none.
// Dropped leaves them out.
func (s Selection) WithGateOnly(r io.Reader) (Selection, error) {
	ids, err := readNodeIDs(r)
	if err != nil {
		return Selection{}, fmt.Errorf("reading the gate-only tests: %w", err)
	}
	s.gateOnly = ids
	return s, nil
}

// Dropped returns, sorted, the ledger tests that a selection other than the
// gate set's no longer runs: those sel leaves out and cand lacks, but for the
// gate-only tests, which sel leaves out by design, and for a test whose last
// entry retires it. A comparison of the gate set names such a test missing,
// or stale when its last entry is, and skips one whose last entry is current
// and unstable, which the next full record of the gate set retires. Dropped
// returns none for the zero Selection, which selects every test.
func Dropped(base *Ledger, cand Result, sel Selection) []string {
	var dropped []string
	for _, id := range slices.Sorted(maps.Keys(base.Tests)) {
		if _, inCand := cand.Outcomes[id]; inCand || sel.holds(id) || sel.gateOnly[id] {
			continue
		}
		if last, _ := base.last(id); last.Outcome != outcomeRetired {
			dropped = append(dropped, id)
		}
	}
	return dropped
}

// droppedHeading is the line under which diff lists n dropped tests.
func droppedHeading(n int) string {
	return fmt.Sprintf("dropped from the pr set: %d ledger tests the selection no longer runs and no gate-only "+
		"line names, which the next comparison of the gate set judges", n)
}

// droppedLine is the line diff lists the dropped test id under.
func droppedLine(base *Ledger, id string) string {
	last, _ := base.last(id)
	return id + ": baseline " + last.Outcome
}

// droppedSummary returns the markdown a job summary shows for the dropped
// tests: droppedHeading as a heading and a code block of their lines, which
// no ledger text can close; none for no test.
func droppedSummary(base *Ledger, dropped []string) string {
	if len(dropped) == 0 {
		return ""
	}
	lines := make([]string, len(dropped))
	for i, id := range dropped {
		lines[i] = droppedLine(base, id)
	}
	return "### " + droppedHeading(len(dropped)) + "\n\n" + codeBlock(lines) + "\n"
}

// readNodeIDs reads pytest node ids, one a line, blank lines aside, and
// returns them by the ids pytest's junit report gives them, refusing a line
// that is not a node id, FILE.py::NAME.
func readNodeIDs(r io.Reader) (map[string]bool, error) {
	ids := map[string]bool{}
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		nodeID := sc.Text()
		if nodeID == "" {
			continue
		}
		id, ok := junitID(nodeID)
		if !ok {
			return nil, fmt.Errorf("line %d: %s is not a pytest node id, FILE.py::NAME", line, nodeID)
		}
		ids[id] = true
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning: %w", err)
	}
	return ids, nil
}

// junitID returns the id hack/parity reads from a junit report for the
// pytest node id nodeID, classname "::" name, as pytest's junitxml plugin
// writes them: the parametrization, from the first "[", is left as it is;
// the rest splits at "::"; the file path's slashes become dots and its .py
// goes; and the last part, with the parametrization, is the name, the others
// joined by dots the classname. It reports false for an id that names no
// test of a .py file.
func junitID(nodeID string) (string, bool) {
	path, params, bracket := strings.Cut(nodeID, "[")
	names := strings.Split(path, "::")
	module, isPy := strings.CutSuffix(names[0], ".py")
	if len(names) < 2 || !isPy || module == "" || names[len(names)-1] == "" {
		return "", false
	}
	names[0] = strings.ReplaceAll(module, "/", ".")
	name := names[len(names)-1]
	if bracket {
		name += "[" + params
	}
	return strings.Join(names[:len(names)-1], ".") + "::" + name, true
}
