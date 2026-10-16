package selection_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/selection"
)

// The testdata, captured once:
//   - 0239625/: the deselect lists as they stood at 0239625, unchanged on main
//     when the collection below was captured;
//   - runsh-2f63a04/: run.sh's excluded markers as excluded-markers.txt holds
//     them, and the -m expression run.sh built from its array at 2f63a04;
//   - ab228b1/collect.txt: pytest --collect-only -q -p no:cacheprovider -m
//     "$(cat runsh-2f63a04/expr.txt)" over test_s3.py and test_headers.py at
//     the s3-tests pin ab228b1, with a conf run.sh wrote;
//   - ab228b1/selected.txt: the ids run.sh's own selection lines picked from
//     that collection with the 0239625 lists, which are the 455 tests
//     radosgw's recorded run at that pin holds.
const (
	lists0239625 = "testdata/0239625"
	runSh2f63a04 = "testdata/runsh-2f63a04"
	pinAB228B1   = "testdata/ab228b1"
	repoLists    = "../.."
)

const s3 = "s3tests/functional/test_s3.py::"

// frozenListNames are the deselect lists 0239625 held. The frozen specs read
// them by these names, so that a change to the lists the command reads
// leaves their input as it was.
var frozenListNames = []string{"deselect-phase2.txt", "deselect-phase3.txt"}

// frozenDeselect returns the lines of the deselect lists at 0239625.
func frozenDeselect() []selection.ListLine {
	var out []selection.ListLine
	for _, name := range frozenListNames {
		l, err := selection.ReadList(filepath.Join(lists0239625, name))
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
		_, err = selection.ReadList(filepath.Join(repoLists, selection.MarkersFile))
		Expect(err).NotTo(HaveOccurred())
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
	)

	It("selects from the pin's collection the 455 ids run.sh selected", func() {
		want := fileLines(filepath.Join(pinAB228B1, "selected.txt"))
		got, err := selection.Select(selection.Input{
			Collected: fileLines(filepath.Join(pinAB228B1, "collect.txt")),
			Deselect:  frozenDeselect(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(455))
		Expect(got).To(Equal(want))
	})
})

var _ = Describe("Hash", func() {
	It("is 999e22cc41a0 for the lists at 0239625, the value every ledger entry records", func() {
		Expect(selection.Hash(frozenDeselect())).To(Equal("999e22cc41a0"))
	})

	It("is what run.sh's --print-deselect pipeline prints for the repository's lists", func(ctx SpecContext) {
		deselect, err := selection.ReadDeselect(repoLists)
		Expect(err).NotTo(HaveOccurred())
		files := make([]string, len(selection.DeselectLists))
		for i, name := range selection.DeselectLists {
			files[i] = filepath.Join(repoLists, name)
		}
		Expect(selection.Hash(deselect)).To(Equal(runShHash(ctx, files...)))
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
			Expect(selection.Hash(all)).To(Equal(runShHash(ctx, files...)))
		},
		Entry("for lines out of order across two lists", "c\na\n", "b\n"),
		Entry("for a line listed twice", "a\na\n"),
		Entry("for comments, blanks and blank lines", "# x\n\na \t# y\n  b\r\n"),
		Entry("for ids that sort differently by byte than by locale", "a_b\naB\nA\nab\n"),
		Entry("for no line", "# nothing\n"),
	)

	It("ignores the order of the lines", func() {
		Expect(selection.Hash(lines("a", "b"))).To(Equal(selection.Hash(lines("b", "a"))))
	})
})
