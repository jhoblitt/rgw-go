package selection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/selection"
)

// The testdata, captured once:
//   - 0239625/: the deselect lists as they stood at 0239625, unchanged on main
//     when the collection below was captured;
//   - a8fe2b0/: the deselect lists as they stood at a8fe2b0, which pinned
//     7cfa64a;
//   - runsh-2f63a04/: run.sh's excluded markers as excluded-markers.txt holds
//     them, and the -m expression run.sh built from its array at 2f63a04;
//   - ab228b1/collect.txt: pytest --collect-only -q -p no:cacheprovider -m
//     "$(cat runsh-2f63a04/expr.txt)" over test_s3.py and test_headers.py at
//     the s3-tests pin ab228b1, with a conf run.sh wrote;
//   - ab228b1/collect-unfiltered.txt: the same collection without -m, with a
//     conf of the same shape and no cluster;
//   - ab228b1/selected.txt: the ids run.sh's own selection lines picked from
//     that collection with the 0239625 lists, which are the 455 tests
//     radosgw's recorded run at that pin holds;
//   - 7cfa64a/collect.txt and collect-unfiltered.txt: the same two
//     collections at the s3-tests pin 7cfa64a, under the same expression,
//     which excluded-markers.txt still made at a8fe2b0;
//   - 7cfa64a/selected.txt: the ids select picked from 7cfa64a/collect.txt
//     with the a8fe2b0 lists and no include list, which are the 457 tests the
//     Squid and Tentacle ledgers both record at that pin.
const (
	lists0239625 = "testdata/0239625"
	listsA8FE2B0 = "testdata/a8fe2b0"
	runSh2f63a04 = "testdata/runsh-2f63a04"
	pinAB228B1   = "testdata/ab228b1"
	pin7CFA64A   = "testdata/7cfa64a"
	repoLists    = "../.."
)

const s3 = "s3tests/functional/test_s3.py::"

// frozenListNames are the deselect lists 0239625 and a8fe2b0 held. The
// frozen specs read them by these names, so that a change to the lists the
// command reads leaves their input as it was.
var frozenListNames = []string{"deselect-phase2.txt", "deselect-phase3.txt"}

// frozenDeselect returns the lines of the frozen deselect lists in dir.
func frozenDeselect(dir string) []selection.ListLine {
	var out []selection.ListLine
	for _, name := range frozenListNames {
		l, err := selection.ReadList(filepath.Join(dir, name))
		Expect(err).NotTo(HaveOccurred())
		out = append(out, l...)
	}
	return out
}

// lines returns list lines naming ids.
func lines(ids ...string) []selection.ListLine {
	out := make([]selection.ListLine, len(ids))
	for i, id := range ids {
		out[i] = selection.ListLine{ID: id, File: "list.txt", Line: i + 1}
	}
	return out
}

// fileLines returns the lines of the file at path, without their newlines.
func fileLines(path string) []string {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// runShHash returns what run.sh's --print-deselect pipeline prints for files,
// each read by its sed rule in the C locale.
func runShHash(ctx context.Context, files ...string) string {
	cmd := exec.CommandContext(ctx, "bash", "-c",
		`sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "$@" </dev/null | sort | sha256sum | cut -c1-12`,
		"bash")
	cmd.Args = append(cmd.Args, files...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSuffix(string(out), "\n")
}

var _ = Describe("ReadList", func() {
	DescribeTable("reads a line's id as run.sh's sed did",
		func(text string, ids []string, numbers []int) {
			path := filepath.Join(GinkgoT().TempDir(), "list.txt")
			Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
			var want []selection.ListLine
			for i := range ids {
				want = append(want, selection.ListLine{ID: ids[i], File: path, Line: numbers[i]})
			}
			got, err := selection.ReadList(path)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("a comment line drops", "# why\na\n", []string{"a"}, []int{2}),
		Entry("a comment after an id drops with the blanks before it", "a  # why\n", []string{"a"}, []int{1}),
		Entry("a # inside a word ends the id", "a#b\n", []string{"a"}, []int{1}),
		Entry("trailing blanks, tabs, carriage returns, vertical tabs and form feeds drop",
			"a \t\r\v\f\n", []string{"a"}, []int{1}),
		Entry("blank lines drop", "a\n\n \t\nb\n", []string{"a", "b"}, []int{1, 4}),
		Entry("leading blanks stay", "  a\n", []string{"  a"}, []int{1}),
		Entry("a last line without a newline is read", "a\nb", []string{"a", "b"}, []int{1, 2}),
		Entry("an empty file names nothing", "", nil, nil),
	)

	It("fails for a file it cannot read, naming it", func() {
		path := filepath.Join(GinkgoT().TempDir(), "missing.txt")
		_, err := selection.ReadList(path)
		Expect(err).To(MatchError(ContainSubstring(path)))
	})
})

var _ = Describe("ReadDeselect", func() {
	It("reads every deselect list in the lists directory, in order", func() {
		dir := GinkgoT().TempDir()
		var want []selection.ListLine
		for i, name := range selection.DeselectLists {
			// Each list's id sorts after the next list's, so that a sorted
			// read differs from an ordered one.
			id := string(rune('z' - i))
			Expect(os.WriteFile(filepath.Join(dir, name), []byte(id+"\n"), 0o600)).To(Succeed())
			want = append(want, selection.ListLine{ID: id, File: filepath.Join(dir, name), Line: 1})
		}
		got, err := selection.ReadDeselect(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	})

	It("fails for a list the command names that is missing, naming it", func() {
		dir := GinkgoT().TempDir()
		lists := selection.DeselectLists
		for _, name := range lists[:len(lists)-1] {
			Expect(os.WriteFile(filepath.Join(dir, name), nil, 0o600)).To(Succeed())
		}
		_, err := selection.ReadDeselect(dir)
		Expect(err).To(MatchError(ContainSubstring(lists[len(lists)-1])))
	})

	It("reads the repository's lists and markers", func() {
		_, err := selection.ReadDeselect(repoLists)
		Expect(err).NotTo(HaveOccurred())
		_, err = selection.ReadInclude(repoLists)
		Expect(err).NotTo(HaveOccurred())
		_, err = selection.ReadList(filepath.Join(repoLists, selection.MarkersFile))
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("ReadInclude", func() {
	It("reads every include list in the lists directory, in order, and no deselect list", func() {
		dir := GinkgoT().TempDir()
		for _, name := range selection.DeselectLists {
			Expect(os.WriteFile(filepath.Join(dir, name), []byte("deselected\n"), 0o600)).To(Succeed())
		}
		var want []selection.ListLine
		for i, name := range selection.IncludeLists {
			id := string(rune('z' - i))
			Expect(os.WriteFile(filepath.Join(dir, name), []byte("# why\n"+id+"\n"), 0o600)).To(Succeed())
			want = append(want, selection.ListLine{ID: id, File: filepath.Join(dir, name), Line: 2})
		}
		got, err := selection.ReadInclude(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	})

	It("fails for an include list the command names that is missing, naming it", func() {
		_, err := selection.ReadInclude(GinkgoT().TempDir())
		Expect(err).To(MatchError(ContainSubstring(selection.IncludeLists[0])))
	})
})

var _ = Describe("MarkerExpr", func() {
	It("is run.sh's expression for run.sh's markers", func() {
		markers, err := selection.ReadList(filepath.Join(runSh2f63a04, selection.MarkersFile))
		Expect(err).NotTo(HaveOccurred())
		want, err := os.ReadFile(filepath.Join(runSh2f63a04, "expr.txt"))
		Expect(err).NotTo(HaveOccurred())
		Expect(selection.MarkerExpr(markers)).To(Equal(string(want)))
	})

	It("negates each marker and joins them with and, in the list's order", func() {
		Expect(selection.MarkerExpr(lines("b", "a", "c"))).To(Equal("not b and not a and not c"))
	})

	It("is empty for no marker, which pytest's -m takes as no filter", func() {
		Expect(selection.MarkerExpr(nil)).To(BeEmpty())
	})
})

var _ = Describe("Select", func() {
	DescribeTable("selects",
		func(in selection.Input, want []string) {
			got, err := selection.Select(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("only the collected lines that are node ids under s3tests/",
			selection.Input{Collected: []string{
				s3 + "test_b", "", "s3tests/functional/test_s3.py:12: PytestWarning: x", "other/test_x.py::test_c",
				s3 + "test_a", "565/977 tests collected (412 deselected) in 0.26s",
			}},
			[]string{s3 + "test_b", s3 + "test_a"}),
		Entry("every parametrization of a test a line names by its base id, in collection order",
			selection.Input{
				Collected: []string{s3 + "test_b", s3 + "test_p[x]", s3 + "test_a", s3 + "test_p[y]"},
				Deselect:  lines(s3 + "test_p"),
			},
			[]string{s3 + "test_b", s3 + "test_a"}),
		Entry("the TESTS file's tests in collection order, whatever its order",
			selection.Input{
				Collected: []string{s3 + "test_a", s3 + "test_b", s3 + "test_c"},
				TestsFile: "t.txt", Tests: lines(s3+"test_c", s3+"test_a"),
			},
			[]string{s3 + "test_a", s3 + "test_c"}),
		Entry("each parametrization a TESTS base id names, and the one a TESTS parametrization id names",
			selection.Input{
				Collected: []string{s3 + "test_p[x]", s3 + "test_p[y]", s3 + "test_q[x]", s3 + "test_q[y]"},
				TestsFile: "t.txt", Tests: lines(s3+"test_p", s3+"test_q[y]"),
			},
			[]string{s3 + "test_p[x]", s3 + "test_p[y]", s3 + "test_q[y]"}),
		Entry("a test the TESTS file names twice, once",
			selection.Input{
				Collected: []string{s3 + "test_a", s3 + "test_b"},
				TestsFile: "t.txt", Tests: lines(s3+"test_a", s3+"test_a"),
			},
			[]string{s3 + "test_a"}),
		Entry("the tests the markers leave and the include list's, in the order of the collection without the markers",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_c"},
				Unfiltered: []string{s3 + "test_d", s3 + "test_a", "", s3 + "test_b", s3 + "test_c", "4 tests collected in 0.2s"},
				Include:    lines(s3+"test_b", s3+"test_d"),
			},
			[]string{s3 + "test_d", s3 + "test_a", s3 + "test_b", s3 + "test_c"}),
		Entry("the tests the markers leave in the same order from either collection, with an empty include list",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_c"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_b", s3 + "test_c"},
			},
			[]string{s3 + "test_a", s3 + "test_c"}),
		Entry("every parametrization an include base id names, and only the one an include parametrization id names",
			selection.Input{
				Collected:  []string{s3 + "test_a"},
				Unfiltered: []string{s3 + "test_p[x]", s3 + "test_a", s3 + "test_p[y]", s3 + "test_q[x]", s3 + "test_q[y]"},
				Include:    lines(s3+"test_p", s3+"test_q[y]"),
			},
			[]string{s3 + "test_p[x]", s3 + "test_a", s3 + "test_p[y]", s3 + "test_q[y]"}),
		Entry("the included tests less the deselected ones",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_b"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_b", s3 + "test_c"},
				Deselect:   lines(s3 + "test_b"),
				Include:    lines(s3 + "test_c"),
			},
			[]string{s3 + "test_a", s3 + "test_c"}),
		Entry("an included test a TESTS line names",
			selection.Input{
				Collected:  []string{s3 + "test_a"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_p[x]", s3 + "test_p[y]"},
				Include:    lines(s3 + "test_p"),
				TestsFile:  "t.txt", Tests: lines(s3 + "test_p"),
			},
			[]string{s3 + "test_p[x]", s3 + "test_p[y]"}),
		Entry("a test the include list names twice, once",
			selection.Input{
				Collected:  []string{s3 + "test_a"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_b"},
				Include:    lines(s3+"test_b", s3+"test_b"),
			},
			[]string{s3 + "test_a", s3 + "test_b"}),
	)

	DescribeTable("refuses, with run.sh's message",
		func(in selection.Input, want selection.Refusals) {
			got, err := selection.Select(in)
			Expect(err).To(MatchError(want))
			Expect(got).To(BeNil())
		},
		Entry("a line that would also deselect a test it does not name",
			selection.Input{
				Collected: []string{s3 + "test_a", s3 + "test_ab"},
				Deselect:  lines(s3 + "test_a"),
			},
			selection.Refusals{
				s3 + "test_a would also deselect " + s3 + "test_ab (pytest matches --deselect as a node-id prefix)",
			}),
		Entry("a line naming one parametrization, which only its base id removes",
			selection.Input{
				Collected: []string{s3 + "test_p[x]", s3 + "test_p[y]", s3 + "test_q"},
				Deselect:  lines(s3 + "test_p[x]"),
			},
			selection.Refusals{
				s3 + "test_p[x] would also deselect " + s3 + "test_p[x] (pytest matches --deselect as a node-id prefix)",
				s3 + "test_p[x] names no test the markers leave: renamed, removed or marker-excluded at this commit",
			}),
		Entry("a line naming no test the markers leave",
			selection.Input{
				Collected: []string{s3 + "test_a"},
				Deselect:  lines(s3 + "test_gone"),
			},
			selection.Refusals{
				s3 + "test_gone names no test the markers leave: renamed, removed or marker-excluded at this commit",
			}),
		Entry("a TESTS line naming no collected test",
			selection.Input{
				Collected: []string{s3 + "test_a"},
				TestsFile: "t.txt", Tests: lines(s3 + "test_gone"),
			},
			selection.Refusals{
				s3 + "test_gone is not in the set: no such test, or the markers or a deselect list remove it",
			}),
		Entry("a TESTS line naming a test a deselect line removes",
			selection.Input{
				Collected: []string{s3 + "test_a", s3 + "test_b"},
				Deselect:  lines(s3 + "test_b"),
				TestsFile: "t.txt", Tests: lines(s3 + "test_b"),
			},
			selection.Refusals{
				s3 + "test_b is not in the set: no such test, or the markers or a deselect list remove it",
			}),
		Entry("a TESTS file naming no test",
			selection.Input{Collected: []string{s3 + "test_a"}, TestsFile: "t.txt"},
			selection.Refusals{"the tests file t.txt names no test"}),
		Entry("a selection the deselect lists leave empty, since pytest given no node id runs every test it finds",
			selection.Input{Collected: []string{s3 + "test_a"}, Deselect: lines(s3 + "test_a")},
			selection.Refusals{"no test is left to run: the deselect lists remove every test the markers leave"}),
		Entry("a collection holding no node id, without blaming the lists, which name nothing",
			selection.Input{Collected: []string{"no tests collected (977 deselected) in 0.26s", ""}},
			selection.Refusals{"no test is left to run: the collection holds no node id"}),
		Entry("a line listed twice, once",
			selection.Input{
				Collected: []string{s3 + "test_a"},
				Deselect:  lines(s3+"test_gone", s3+"test_gone"),
			},
			selection.Refusals{
				s3 + "test_gone names no test the markers leave: renamed, removed or marker-excluded at this commit",
			}),
		Entry("every refusal of a run, in the order run.sh checks them",
			selection.Input{
				Collected: []string{s3 + "test_a", s3 + "test_ab", s3 + "test_c", s3 + "test_cd"},
				Deselect:  lines(s3+"test_c", s3+"test_gone", s3+"test_a"),
				TestsFile: "t.txt", Tests: lines(s3 + "test_zz"),
			},
			selection.Refusals{
				s3 + "test_a would also deselect " + s3 + "test_ab (pytest matches --deselect as a node-id prefix)",
				s3 + "test_c would also deselect " + s3 + "test_cd (pytest matches --deselect as a node-id prefix)",
				s3 + "test_gone names no test the markers leave: renamed, removed or marker-excluded at this commit",
				s3 + "test_zz is not in the set: no such test, or the markers or a deselect list remove it",
			}),
		Entry("an include line naming no collected test, as one renamed or removed",
			selection.Input{
				Collected:  []string{s3 + "test_a"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_b"},
				Include:    lines(s3 + "test_gone"),
			},
			selection.Refusals{s3 + "test_gone names no collected test: renamed or removed at this commit"}),
		Entry("an include parametrization id the collection lacks, though it holds others of the test",
			selection.Input{
				Collected:  []string{s3 + "test_a"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_p[x]", s3 + "test_p[y]"},
				Include:    lines(s3 + "test_p[z]"),
			},
			selection.Refusals{s3 + "test_p[z] names no collected test: renamed or removed at this commit"}),
		Entry("an include line naming a test the markers leave, which they select already",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_b"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_b"},
				Include:    lines(s3 + "test_b"),
			},
			selection.Refusals{
				s3 + "test_b is a test the markers leave: include lines add only tests they exclude",
			}),
		Entry("an include base id naming a parametrization the markers leave, though they exclude another",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_p[y]"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_p[x]", s3 + "test_p[y]"},
				Include:    lines(s3 + "test_p"),
			},
			selection.Refusals{
				s3 + "test_p names " + s3 + "test_p[y], a test the markers leave: include lines add only tests they exclude",
			}),
		Entry("an include line and a deselect line naming the same test, besides the deselect line's own staleness",
			selection.Input{
				Collected:  []string{s3 + "test_a"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_p[x]", s3 + "test_p[y]"},
				Deselect:   lines(s3 + "test_p"),
				Include:    lines(s3 + "test_p[y]"),
			},
			selection.Refusals{
				s3 + "test_p names no test the markers leave: renamed, removed or marker-excluded at this commit",
				s3 + "test_p[y] is a test the deselect line " + s3 + "test_p removes: " +
					"a test is included or deselected, not both",
			}),
		Entry("a deselect line that would also deselect an included test it does not name",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_b"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_ab", s3 + "test_b"},
				Deselect:   lines(s3 + "test_a"),
				Include:    lines(s3 + "test_ab"),
			},
			selection.Refusals{
				s3 + "test_a would also deselect " + s3 + "test_ab (pytest matches --deselect as a node-id prefix)",
			}),
		Entry("include lines without a collection without the marker expression to find them in",
			selection.Input{
				Collected: []string{s3 + "test_a"},
				Include:   lines(s3+"test_b", s3+"test_c"),
			},
			selection.Refusals{
				"include lines name tests, and the collection without the marker expression holds no node id",
			}),
		Entry("a collection without the marker expression that lacks tests the markers leave, which would drop them",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_b", s3 + "test_c"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_x"},
			},
			selection.Refusals{
				"the collection without the marker expression lacks 2 of the tests the markers leave, " +
					s3 + "test_b first: the two are not of the same tests",
			}),
		Entry("every include refusal, after the deselect lines' and before the TESTS file's",
			selection.Input{
				Collected:  []string{s3 + "test_a", s3 + "test_ab", s3 + "test_m"},
				Unfiltered: []string{s3 + "test_a", s3 + "test_ab", s3 + "test_m", s3 + "test_x"},
				Deselect:   lines(s3 + "test_a"),
				Include:    lines(s3+"test_x", s3+"test_gone", s3+"test_m"),
				TestsFile:  "t.txt", Tests: lines(s3 + "test_zz"),
			},
			selection.Refusals{
				s3 + "test_a would also deselect " + s3 + "test_ab (pytest matches --deselect as a node-id prefix)",
				s3 + "test_gone names no collected test: renamed or removed at this commit",
				s3 + "test_m is a test the markers leave: include lines add only tests they exclude",
				s3 + "test_zz is not in the set: no such test, or the markers or a deselect list remove it",
			}),
	)

	DescribeTable("selects a pin's set as it stood before the include list, given an empty one",
		func(pin, lists string, n int, unfiltered bool) {
			in := selection.Input{
				Collected: fileLines(filepath.Join(pin, "collect.txt")),
				Deselect:  frozenDeselect(lists),
			}
			if unfiltered {
				in.Unfiltered = fileLines(filepath.Join(pin, "collect-unfiltered.txt"))
			}
			got, err := selection.Select(in)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(HaveLen(n))
			Expect(got).To(Equal(fileLines(filepath.Join(pin, "selected.txt"))))
		},
		Entry("the 455 ids run.sh selected at ab228b1 with the 0239625 lists", pinAB228B1, lists0239625, 455, false),
		Entry("the same 455 at ab228b1 from both collections", pinAB228B1, lists0239625, 455, true),
		Entry("the 457 ids both ledgers record at 7cfa64a, with the a8fe2b0 lists", pin7CFA64A, listsA8FE2B0, 457, false),
		Entry("the same 457 at 7cfa64a from both collections, as a pull request's s3tests check runs them",
			pin7CFA64A, listsA8FE2B0, 457, true),
	)

	It("adds the six bucket encryption configuration tests to the 7cfa64a set, in collection order", func() {
		six := []string{
			s3 + "test_put_bucket_encryption_s3", s3 + "test_put_bucket_encryption_kms",
			s3 + "test_get_bucket_encryption_s3", s3 + "test_get_bucket_encryption_kms",
			s3 + "test_delete_bucket_encryption_s3", s3 + "test_delete_bucket_encryption_kms",
		}
		unfiltered := fileLines(filepath.Join(pin7CFA64A, "collect-unfiltered.txt"))
		selected := fileLines(filepath.Join(pin7CFA64A, "selected.txt"))
		want := slices.DeleteFunc(slices.Clone(unfiltered), func(c string) bool {
			return !slices.Contains(selected, c) && !slices.Contains(six, c)
		})
		got, err := selection.Select(selection.Input{
			Collected:  fileLines(filepath.Join(pin7CFA64A, "collect.txt")),
			Unfiltered: unfiltered,
			Deselect:   frozenDeselect(listsA8FE2B0),
			Include:    lines(six...),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(463))
		Expect(got).To(Equal(want))
	})
})

var _ = Describe("Hash", func() {
	It("is 999e22cc41a0 for the lists at 0239625 and no include line, the value the ledgers record to ab228b1", func() {
		Expect(selection.Hash(frozenDeselect(lists0239625), nil)).To(Equal("999e22cc41a0"))
	})

	It("is 499666025fc3 for the lists at a8fe2b0 and no include line, the value both ledgers record at 7cfa64a",
		func() {
			Expect(selection.Hash(frozenDeselect(listsA8FE2B0), nil)).To(Equal("499666025fc3"))
		})

	It("is what run.sh's --print-deselect pipeline prints for the repository's deselect lists", func(ctx SpecContext) {
		deselect, err := selection.ReadDeselect(repoLists)
		Expect(err).NotTo(HaveOccurred())
		files := make([]string, len(selection.DeselectLists))
		for i, name := range selection.DeselectLists {
			files[i] = filepath.Join(repoLists, name)
		}
		Expect(selection.Hash(deselect, nil)).To(Equal(runShHash(ctx, files...)))
	})

	It("covers each include line as its id after a +, sorted with the deselect lines", func() {
		Expect(selection.Hash(lines("b"), lines("a", "c"))).To(Equal(selection.Hash(lines("+a", "b", "+c"), nil)))
	})

	It("tells an include line from a deselect line naming the same test", func() {
		Expect(selection.Hash(nil, lines("a"))).NotTo(Equal(selection.Hash(lines("a"), nil)))
	})

	DescribeTable("is what run.sh's --print-deselect pipeline prints",
		func(ctx SpecContext, texts ...string) {
			dir := GinkgoT().TempDir()
			var files []string
			var all []selection.ListLine
			for i, text := range texts {
				path := filepath.Join(dir, "list"+string(rune('a'+i))+".txt")
				Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
				l, err := selection.ReadList(path)
				Expect(err).NotTo(HaveOccurred())
				files, all = append(files, path), append(all, l...)
			}
			Expect(selection.Hash(all, nil)).To(Equal(runShHash(ctx, files...)))
		},
		Entry("for lines out of order across two lists", "c\na\n", "b\n"),
		Entry("for a line listed twice", "a\na\n"),
		Entry("for comments, blanks and blank lines", "# x\n\na \t# y\n  b\r\n"),
		Entry("for ids that sort differently by byte than by locale", "a_b\naB\nA\nab\n"),
		Entry("for no line", "# nothing\n"),
	)

	It("ignores the order of the lines", func() {
		Expect(selection.Hash(lines("a", "b"), lines("c", "d"))).To(Equal(selection.Hash(lines("b", "a"), lines("d", "c"))))
	})
})
