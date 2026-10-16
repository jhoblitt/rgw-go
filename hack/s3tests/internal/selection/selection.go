// Package selection decides which s3-tests the parity set runs: the tests
// pytest collects from test_s3.py and test_headers.py under the excluded
// markers' expression, less those the deselect lists name.
package selection

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// MarkersFile is the list of the s3-tests markers whose tests the set leaves
// out, in the lists directory.
const MarkersFile = "excluded-markers.txt"

// DeselectLists are the lists of the tests of later phases' features that no
// excluded marker removes, by pytest node id, one file per phase, in the
// lists directory. A phase deletes its lines as it lands its features, and
// its file and entry here with the last of them.
var DeselectLists = []string{"deselect-phase2.txt", "deselect-phase3.txt"}

// ListLine is a line of a list that names something, a marker or a pytest
// node id, and where it stands.
type ListLine struct {
	ID   string
	File string
	Line int
}

// blanks are what [[:space:]] matches in the C locale, but for the newline
// that ends a line.
const blanks = " \t\r\v\f"

// ReadList reads the list at path. A "#" starts a comment, and trailing
// blanks and blank lines are dropped; leading blanks stay part of the id.
func ReadList(path string) ([]ListLine, error) {
	data, err := os.ReadFile(path) //nolint:gosec // reading the list named is the command's work
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []ListLine
	for i, line := range strings.Split(string(data), "\n") {
		id, _, _ := strings.Cut(line, "#")
		if id = strings.TrimRight(id, blanks); id != "" {
			out = append(out, ListLine{ID: id, File: path, Line: i + 1})
		}
	}
	return out, nil
}

// ReadDeselect reads DeselectLists from dir, in order.
func ReadDeselect(dir string) ([]ListLine, error) {
	var out []ListLine
	for _, name := range DeselectLists {
		l, err := ReadList(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out = append(out, l...)
	}
	return out, nil
}

// MarkerExpr returns the pytest -m expression that leaves out every marker,
// "not M1 and not M2 ...", in the list's order; for no marker it is empty,
// which -m takes as no filter.
func MarkerExpr(markers []ListLine) string {
	terms := make([]string, len(markers))
	for i, m := range markers {
		terms[i] = "not " + m.ID
	}
	return strings.Join(terms, " and ")
}

// Input is what Select decides from.
type Input struct {
	// Collected is pytest --collect-only -q's output under MarkerExpr's
	// expression, a line each. Select reads the lines that are node ids.
	Collected []string
	// Deselect is the deselect lists' lines.
	Deselect []ListLine
	// TestsFile names the file whose lines Tests holds, each a node id or a
	// base id to run alone; empty runs the whole set.
	TestsFile string
	Tests     []ListLine
}

// Refusals is Select's error: every reason it refuses its input, one a line.
type Refusals []string

func (r Refusals) Error() string { return strings.Join(r, "\n") }

// nodeID matches the lines of pytest --collect-only -q that name a test of
// the suite; the others are its summary and warnings.
var nodeID = regexp.MustCompile(`^s3tests/.+\.py::`)

// collectedIDs returns the node ids among the collection's lines.
func collectedIDs(collection []string) []string {
	var ids []string
	for _, line := range collection {
		if nodeID.MatchString(line) {
			ids = append(ids, line)
		}
	}
	return ids
}

// baseID returns a node id less the parametrization pytest writes after it
// in brackets.
func baseID(id string) string {
	base, _, _ := strings.Cut(id, "[")
	return base
}

// uniqueIDs returns the lines' ids, each once, in the order they first stand.
func uniqueIDs(lines []ListLine) []string {
	seen := make(map[string]bool, len(lines))
	var ids []string
	for _, l := range lines {
		if !seen[l.ID] {
			seen[l.ID] = true
			ids = append(ids, l.ID)
		}
	}
	return ids
}

// Select returns the node ids of the set, in collection order: the collected
// ids less each one whose base id a deselect line names, and with a TESTS
// file only those it names by id or base id. It returns Refusals instead
// when anything in its input is wrong, listing each.
//
// A deselect line removes its tests by their base id. It is refused when it
// is also the start of another collected test's id, since the line, read as
// the pytest --deselect argument it is written as, would remove that test
// too: --deselect removes every node id that starts with its argument. It is
// refused when it names no test the markers leave, since it is then stale.
// A set left empty is refused too: pytest given no node id runs every test it
// finds.
func Select(in Input) ([]string, error) {
	refusals := testsRefusals(in)
	deselect := uniqueIDs(in.Deselect)
	listed := make(map[string]bool, len(deselect))
	for _, id := range deselect {
		listed[id] = true
	}
	seen, inSet := map[string]bool{}, map[string]bool{}
	var set []string
	collected := collectedIDs(in.Collected)
	for _, c := range collected {
		base := baseID(c)
		if listed[base] {
			seen[base] = true
			continue
		}
		for _, id := range deselect {
			if strings.HasPrefix(c, id) {
				refusals = append(refusals,
					fmt.Sprintf("%s would also deselect %s (pytest matches --deselect as a node-id prefix)", id, c))
			}
		}
		inSet[c], inSet[base] = true, true
		set = append(set, c)
	}
	for _, id := range deselect {
		if !seen[id] {
			refusals = append(refusals,
				id+" names no test the markers leave: renamed, removed or marker-excluded at this commit")
		}
	}
	tests := uniqueIDs(in.Tests)
	for _, id := range tests {
		if !inSet[id] {
			refusals = append(refusals,
				id+" is not in the set: no such test, or the markers or a deselect list remove it")
		}
	}
	if len(tests) > 0 {
		named := make(map[string]bool, len(tests))
		for _, id := range tests {
			named[id] = true
		}
		set = slices.DeleteFunc(set, func(c string) bool { return !named[c] && !named[baseID(c)] })
	}
	if len(set) == 0 && len(refusals) == 0 {
		why := "the deselect lists remove every test the markers leave"
		if len(collected) == 0 {
			why = "the collection holds no node id"
		}
		refusals = append(refusals, "no test is left to run: "+why)
	}
	if len(refusals) > 0 {
		return nil, refusals
	}
	return set, nil
}

// testsRefusals returns the refusal of a TESTS file that names no test.
func testsRefusals(in Input) Refusals {
	if in.TestsFile != "" && len(in.Tests) == 0 {
		return Refusals{fmt.Sprintf("the tests file %s names no test", in.TestsFile)}
	}
	return nil
}

// Hash returns the digest a recorded result carries in its deselect meta:
// the first 12 hex digits of the sha256 of the lines' ids sorted by byte,
// each followed by a newline.
func Hash(deselect []ListLine) string {
	ids := make([]string, len(deselect))
	for i, l := range deselect {
		ids[i] = l.ID
	}
	slices.Sort(ids)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(id)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:12]
}
