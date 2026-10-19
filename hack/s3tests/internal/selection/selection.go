// Package selection decides which s3-tests the parity set runs: the tests
// pytest collects from test_s3.py and test_headers.py under the excluded
// markers' expression and the tests the include lists name, less those the
// deselect lists name.
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

// IncludeLists are the lists of the tests an excluded marker removes that a
// phase serves, by pytest node id, one file per phase, in the lists
// directory: a marker stays excluded while it also marks tests of a feature
// not served yet. A line goes once its test's markers leave MarkersFile,
// since Select then refuses it.
var IncludeLists = []string{"include-phase2.txt"}

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
	return readLists(dir, DeselectLists)
}

// ReadInclude reads IncludeLists from dir, in order.
func ReadInclude(dir string) ([]ListLine, error) {
	return readLists(dir, IncludeLists)
}

func readLists(dir string, names []string) ([]ListLine, error) {
	var out []ListLine
	for _, name := range names {
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
	// Unfiltered is pytest's output for the same files without the
	// expression, which holds the tests the include lines name. Nothing
	// needs it while they name none.
	Unfiltered []string
	// Deselect is the deselect lists' lines.
	Deselect []ListLine
	// Include is the include lists' lines.
	Include []ListLine
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
// ids and the tests the include lines name, less each one whose base id a
// deselect line names, and with a TESTS file only those it names by id or
// base id. Collection order is Unfiltered's when it holds a node id, and
// Collected's otherwise. It returns Refusals instead when anything in its
// input is wrong, listing each.
//
// A deselect line removes its tests by their base id. It is refused when it
// is also the start of another id in the set, since the line, read as the
// pytest --deselect argument it is written as, would remove that test too:
// --deselect removes every node id that starts with its argument. It is
// refused when it names no test the markers leave, since it is then stale.
//
// An include line adds the test of its id, or every parametrization of a
// base id, from Unfiltered. It is refused when it names no test there or one
// the markers leave, since it is then stale, and when it names a test a
// deselect line removes. Unfiltered must hold every collected id, since the
// set, taken in its order, would lose the others.
//
// A set left empty is refused too: pytest given no node id runs every test it
// finds.
func Select(in Input) ([]string, error) {
	collected := collectedIDs(in.Collected)
	unfiltered := collectedIDs(in.Unfiltered)
	marked := make(map[string]bool, len(collected))
	for _, c := range collected {
		marked[c] = true
	}
	deselect := uniqueIDs(in.Deselect)
	listed := make(map[string]bool, len(deselect))
	for _, id := range deselect {
		listed[id] = true
	}
	included, includeRefusals := includeTests(uniqueIDs(in.Include), unfiltered, marked, listed)
	order, orderRefusals := collected, Refusals(nil)
	if len(unfiltered) > 0 {
		order, orderRefusals = unfiltered, lacking(collected, unfiltered)
	}

	refusals := testsRefusals(in)
	seen, inSet := map[string]bool{}, map[string]bool{}
	var set []string
	for _, c := range order {
		if !marked[c] && !included[c] {
			continue
		}
		base := baseID(c)
		if listed[base] {
			if marked[c] {
				seen[base] = true
			}
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
	refusals = append(append(refusals, orderRefusals...), includeRefusals...)
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

// includeTests returns the unfiltered ids the include lines ids name, and the
// refusals of the lines that are stale or that a deselect line contradicts.
func includeTests(ids, unfiltered []string, marked, listed map[string]bool) (map[string]bool, Refusals) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(unfiltered) == 0 {
		return nil, Refusals{"include lines name tests, and the collection without the marker expression holds no node id"}
	}
	exact := make(map[string]bool, len(unfiltered))
	byBase := map[string][]string{}
	for _, c := range unfiltered {
		exact[c] = true
		byBase[baseID(c)] = append(byBase[baseID(c)], c)
	}
	included := map[string]bool{}
	var refusals Refusals
	for _, id := range ids {
		named := byBase[id]
		if exact[id] {
			named = []string{id}
		}
		if len(named) == 0 {
			refusals = append(refusals, id+" names no collected test: renamed or removed at this commit")
			continue
		}
		if i := slices.IndexFunc(named, func(c string) bool { return marked[c] }); i >= 0 {
			refusals = append(refusals,
				naming(id, named[i])+" a test the markers leave: include lines add only tests they exclude")
		}
		if i := slices.IndexFunc(named, func(c string) bool { return listed[baseID(c)] }); i >= 0 {
			refusals = append(refusals, naming(id, named[i])+" a test the deselect line "+baseID(named[i])+
				" removes: a test is included or deselected, not both")
		}
		for _, c := range named {
			included[c] = true
		}
	}
	return included, refusals
}

// naming returns how a refusal of the include line id names the test c it
// names: by the line alone when that is the test's id.
func naming(id, c string) string {
	if id == c {
		return id + " is"
	}
	return id + " names " + c + ","
}

// lacking returns the refusal of an unfiltered collection that lacks some of
// the collected ids.
func lacking(collected, unfiltered []string) Refusals {
	held := make(map[string]bool, len(unfiltered))
	for _, c := range unfiltered {
		held[c] = true
	}
	var lacks []string
	for _, c := range collected {
		if !held[c] {
			lacks = append(lacks, c)
		}
	}
	if len(lacks) == 0 {
		return nil
	}
	return Refusals{fmt.Sprintf("the collection without the marker expression lacks %d of the tests the markers "+
		"leave, %s first: the two are not of the same tests", len(lacks), lacks[0])}
}

// testsRefusals returns the refusal of a TESTS file that names no test.
func testsRefusals(in Input) Refusals {
	if in.TestsFile != "" && len(in.Tests) == 0 {
		return Refusals{fmt.Sprintf("the tests file %s names no test", in.TestsFile)}
	}
	return nil
}

// Hash returns the digest a recorded result carries in its deselect meta:
// the first 12 hex digits of the sha256 of the deselect lines' ids and the
// include lines' ids, each of those after a "+", sorted by byte together,
// each followed by a newline. The "+" keeps an include line from hashing as
// the deselect line of the same test; with no include line, the digest is
// the deselect lines' alone.
func Hash(deselect, include []ListLine) string {
	ids := make([]string, 0, len(deselect)+len(include))
	for _, l := range deselect {
		ids = append(ids, l.ID)
	}
	for _, l := range include {
		ids = append(ids, "+"+l.ID)
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
