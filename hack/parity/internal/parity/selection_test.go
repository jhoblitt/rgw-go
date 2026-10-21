package parity_test

import (
	"maps"
	"os"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// mustSelection parses a selection, failing the spec on an error.
func mustSelection(text string) parity.Selection {
	sel, err := parity.ParseSelection(strings.NewReader(text))
	Expect(err).NotTo(HaveOccurred())
	return sel
}

// The node ids of s3-tests' test_s3.py, and the ids its junit report gives
// them.
const (
	nodeS3  = "s3tests/functional/test_s3.py::"
	junitS3 = "s3tests.functional.test_s3::"
)

var _ = Describe("ParseSelection", func() {
	// testdata/pytest-ids holds test_ids.py, collect.txt, the output of
	// python3 -m pytest --collect-only -q -p no:cacheprovider
	// s3tests/functional/test_ids.py with the module in s3tests/functional/
	// beside an empty pytest.ini, and junit.xml, its --junitxml report less
	// the hostname, timestamp and time attributes, both under pytest 9.1.1.
	It("maps each node id to the id pytest's junit report gives it", func() {
		var nodeIDs []string
		for line := range strings.SplitSeq(readText("testdata/pytest-ids/collect.txt"), "\n") {
			if strings.Contains(line, "::") {
				nodeIDs = append(nodeIDs, line)
			}
		}
		Expect(nodeIDs).To(HaveLen(11))
		res, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{"testdata/pytest-ids/junit.xml"})
		Expect(err).NotTo(HaveOccurred())
		sel := mustSelection(strings.Join(nodeIDs, "\n") + "\n")
		Expect(parity.SelectionIDs(sel)).To(Equal(slices.Sorted(maps.Keys(res.Outcomes))))
	})

	DescribeTable("maps",
		func(nodeID, want string) {
			Expect(parity.SelectionIDs(mustSelection(nodeID + "\n"))).To(Equal([]string{want}))
		},
		Entry("a module's test", nodeS3+"test_a", junitS3+"test_a"),
		Entry("a parametrization, whose parameters keep their slashes, colons and dots",
			nodeS3+"test_p[a/b::c.py]", junitS3+"test_p[a/b::c.py]"),
		Entry("a class's test, the class joining the module's dotted path",
			nodeS3+"TestX::test_y[z]", "s3tests.functional.test_s3.TestX::test_y[z]"),
	)

	It("reads one node id a line, skipping blank lines and a test named twice", func() {
		sel := mustSelection(nodeS3 + "test_b\n\n" + nodeS3 + "test_a\n" + nodeS3 + "test_b\n")
		Expect(parity.SelectionIDs(sel)).To(Equal([]string{junitS3 + "test_a", junitS3 + "test_b"}))
	})

	DescribeTable("refuses",
		func(text, want string) {
			_, err := parity.ParseSelection(strings.NewReader(text))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a selection of no test", "\n\n", "names no test"),
		Entry("an id as the ledger writes it", junitS3+"test_a\n",
			"line 1: "+junitS3+"test_a is not a pytest node id, FILE.py::NAME"),
		Entry("a line with no test name", nodeS3+"test_a\ns3tests/functional/test_s3.py\n",
			"line 2: s3tests/functional/test_s3.py is not a pytest node id, FILE.py::NAME"),
		Entry("a line with an empty test name", nodeS3+"\n", "line 1: "+nodeS3+" is not a pytest node id"),
	)
})

var _ = Describe("diff with a selection", func() {
	const (
		a, b, g, n = junitS3 + "test_a", junitS3 + "test_b", junitS3 + "test_g", junitS3 + "test_n"
		r, u       = junitS3 + "test_r", junitS3 + "test_u"
	)
	var (
		base *parity.Ledger
		// sel selects test_a and test_b, and none of test_g, the retired
		// test_r and the unstable test_u, which the ledger holds.
		sel parity.Selection
	)

	BeforeEach(func() {
		unstable := entry("aaa", "passed")
		unstable.Unstable = true
		base = ledgerOf(map[string][]parity.Entry{
			a: {entry("aaa", "passed")}, b: {entry("aaa", "failed")}, g: {entry("aaa", "passed")},
			r: {entry("aaa", "passed"), reasoned(entry("aaa", "retired"), "deselected")}, u: {unstable},
		})
		sel = mustSelection(nodeS3 + "test_a\n" + nodeS3 + "test_b\n")
	})

	It("compares no ledger test outside it, printing nothing for one, which it would name missing otherwise", func() {
		cand := candidate(map[string]string{a: "passed", b: "failed"})
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
		diffs, err = parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{g + ": baseline passed, candidate missing"}))
	})

	It("compares a selected test whose outcome differs, either way", func() {
		diffs, err := parity.Diff(base, candidate(map[string]string{a: "failed", b: "passed"}), false, parity.KnownList{}, sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{a + ": baseline passed, candidate failed", b + ": baseline failed, candidate passed"}))
	})

	It("names a selected test the candidate lacks missing", func() {
		diffs, err := parity.Diff(base, candidate(map[string]string{a: "passed"}), false, parity.KnownList{}, sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{b + ": baseline failed, candidate missing"}))
	})

	It("names a selected test the ledger lacks, run or not, as having no baseline entry", func() {
		sel = mustSelection(nodeS3 + "test_a\n" + nodeS3 + "test_b\n" + nodeS3 + "test_m\n" + nodeS3 + "test_n\n")
		cand := candidate(map[string]string{a: "passed", b: "failed", n: "passed"})
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{junitS3 + "test_m: no baseline entry", n + ": no baseline entry"}))
	})

	It("compares a candidate test it does not select", func() {
		cand := candidate(map[string]string{a: "passed", b: "failed", g: "failed", r: "passed"})
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{g + ": baseline passed, candidate failed", r + ": baseline retired, candidate passed"}))
	})

	Describe("and a known-difference list", func() {
		It("keeps an entry that matches only a ledger test outside the selection", func() {
			cand := candidate(map[string]string{a: "passed", b: "failed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown(g+"\n~ "+g+"\n"), sel)
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(BeEmpty())
		})

		It("reports an entry that matches nothing in either", func() {
			cand := candidate(map[string]string{a: "passed", b: "failed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown(junitS3+"test_gone\n"), sel)
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{junitS3 + "test_gone: known difference matches no test"}))
		})

		It("reports an entry whose only test outside the selection is retired or unstable, as a comparison of every test does",
			func() {
				cand := candidate(map[string]string{a: "passed", b: "failed"})
				known := mustKnown(r + "\n" + u + "\n")
				stale := []string{r + ": known difference matches no test", u + ": known difference matches no test"}
				diffs, err := parity.Diff(base, cand, false, known, sel)
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(Equal(stale))
				diffs, err = parity.Diff(base, cand, false, known, parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(Equal(append([]string{g + ": baseline passed, candidate missing"}, stale...)))
			})

		It("judges a selected test by the list as ever", func() {
			cand := candidate(map[string]string{a: "failed", b: "failed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown(a+"\n"+b+"\n"), sel)
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{b + ": known difference no longer differs"}))
		})
	})
})

var _ = Describe("Dropped", func() {
	const (
		a, b, d, g = junitS3 + "test_a", junitS3 + "test_b", junitS3 + "test_d", junitS3 + "test_g"
		r, s, u    = junitS3 + "test_r", junitS3 + "test_s", junitS3 + "test_u"
	)
	var (
		base *parity.Ledger
		cand parity.Result
		// sel selects test_a and test_b and holds test_g as gate-only; the
		// ledger's test_d, the retired test_r, the stale test_s and the
		// unstable test_u are neither.
		sel parity.Selection
	)

	BeforeEach(func() {
		unstable := entry("aaa", "passed")
		unstable.Unstable = true
		base = ledgerOf(map[string][]parity.Entry{
			a: {entry("aaa", "passed")}, b: {entry("aaa", "failed")}, d: {entry("aaa", "passed")},
			g: {entry("aaa", "passed")}, r: {entry("aaa", "passed"), reasoned(entry("aaa", "retired"), "deselected")},
			s: {withCeph(entry("aaa", "passed"), "19.2.5")}, u: {unstable},
		})
		cand = candidate(map[string]string{a: "passed", b: "failed"})
		var err error
		sel, err = mustSelection(nodeS3 + "test_a\n" + nodeS3 + "test_b\n").WithGateOnly(strings.NewReader(nodeS3 + "test_g\n"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("names each ledger test the selection leaves out, but for the gate-only and the retired ones", func() {
		Expect(parity.Dropped(base, cand, sel)).To(Equal([]string{d, s, u}))
	})

	It("does not fail the comparison for a dropped test, which a comparison of every test names missing", func() {
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, sel)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
		diffs, err = parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{
			d + ": baseline passed, candidate missing", g + ": baseline passed, candidate missing",
			s + ": baseline stale, re-record",
		}))
	})

	It("names a gate-only test too when the selection holds no gate-only list", func() {
		Expect(parity.Dropped(base, cand, mustSelection(nodeS3+"test_a\n"+nodeS3+"test_b\n"))).To(Equal([]string{d, g, s, u}))
	})

	It("names no test the candidate runs, selected or not", func() {
		cand.Outcomes[d] = "passed"
		Expect(parity.Dropped(base, cand, sel)).To(Equal([]string{s, u}))
	})

	It("names none for the zero selection, which selects every test", func() {
		Expect(parity.Dropped(base, cand, parity.Selection{})).To(BeEmpty())
	})

	Describe("as a summary", func() {
		It("is a heading and a code block of the tests and their baseline outcomes", func() {
			Expect(parity.DroppedSummary(base, []string{d, u})).To(Equal(
				"### dropped from the pr set: 2 ledger tests the selection no longer runs and no gate-only line names, " +
					"which the next comparison of the gate set judges\n\n" +
					"```\n" + d + ": baseline passed\n" + u + ": baseline passed\n```\n\n"))
		})

		It("is empty for no dropped test", func() {
			Expect(parity.DroppedSummary(base, nil)).To(BeEmpty())
		})

		It("holds a test id in a code block it cannot close", func() {
			odd := junitS3 + "test_```x"
			base.Tests[odd] = []parity.Entry{entry("aaa", "failed")}
			Expect(parity.DroppedSummary(base, []string{odd})).To(HaveSuffix(
				"\n````\n" + odd + ": baseline failed\n````\n\n"))
		})
	})

	It("takes an empty gate-only list, and refuses a line that is not a node id", func() {
		_, err := mustSelection(nodeS3 + "test_a\n").WithGateOnly(strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		_, err = mustSelection(nodeS3 + "test_a\n").WithGateOnly(strings.NewReader(junitS3 + "test_g\n"))
		Expect(err).To(MatchError(ContainSubstring("line 1: " + junitS3 + "test_g is not a pytest node id")))
	})
})

// readText returns the text of the file at path.
func readText(path string) string {
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}
