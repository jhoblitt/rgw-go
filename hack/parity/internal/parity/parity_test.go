package parity_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// writeRun writes a run file holding body to a fresh directory and returns
// its path.
func writeRun(name, body string) string {
	path := filepath.Join(GinkgoT().TempDir(), name)
	Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
	return path
}

// mustKnown parses a known-difference list, failing the spec on an error.
func mustKnown(list string) parity.KnownList {
	known, err := parity.ParseKnown(strings.NewReader(list))
	Expect(err).NotTo(HaveOccurred())
	return known
}

// candidate returns a result with outcomes and the meta of ledgerOf's
// ledger.
func candidate(outcomes map[string]string) parity.Result {
	return parity.Result{
		Suite:    "s3tests",
		Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid", "ceph_version": "19.2.6"},
		Outcomes: outcomes,
	}
}

// entry returns an entry of outcome recorded at source on 19.2.6 from two
// runs.
func entry(source, outcome string) parity.Entry {
	return parity.Entry{Outcome: outcome, Source: source, CephVersion: "19.2.6", Recorded: "2026-10-12T16:55:39Z", Runs: 2}
}

// reasoned returns e with the reason given.
func reasoned(e parity.Entry, reason string) parity.Entry {
	e.Reason = reason
	return e
}

// withCeph returns e recorded under the Ceph version given.
func withCeph(e parity.Entry, version string) parity.Entry {
	e.CephVersion = version
	return e
}

// ledgerOf returns an s3-tests ledger for squid, its chain one base pin at
// aaa, holding tests.
func ledgerOf(tests map[string][]parity.Entry) *parity.Ledger {
	return &parity.Ledger{
		Suite: "s3tests", Release: "squid", SourceKey: "s3tests_commit",
		Pins: []parity.Pin{{Source: "aaa", Admitted: parity.AdmitBase}}, Tests: tests,
	}
}

var _ = Describe("record", func() {
	It("takes outcomes from the last run and marks disagreements unstable", func() {
		res, err := parity.Record(parity.FormatJunit, "s3tests", map[string]string{"release": "squid"},
			[]string{"testdata/run1.xml", "testdata/run2.xml"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Suite).To(Equal("s3tests"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_a", "passed"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_b", "failed"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_c", "failed"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_d", "skipped"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_e", "error"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_f", "passed"))
		Expect(res.Outcomes).To(HaveLen(6))
		Expect(res.Unstable).To(ConsistOf("s3tests.functional.test_s3::test_c"))
		Expect(res.Meta).To(Equal(map[string]string{"release": "squid", "runs": "2"}))
	})

	DescribeTable("marks a test unstable when any two runs disagree",
		func(runs ...string) {
			res, err := parity.Record(parity.FormatJunit, "s3tests", nil, runs)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_c", "passed"))
			Expect(res.Unstable).To(ConsistOf("s3tests.functional.test_s3::test_c"))
			Expect(res.Meta).To(HaveKeyWithValue("runs", strconv.Itoa(len(runs))))
		},
		Entry("though the first and last agree", "testdata/run1.xml", "testdata/run2.xml", "testdata/run1.xml"),
		Entry("though the first and last agree, and so do the last two",
			"testdata/run1.xml", "testdata/run2.xml", "testdata/run1.xml", "testdata/run1.xml"),
	)

	It("reads go test -json terminal events", func() {
		res, err := parity.Record(parity.FormatGoTest, "admin", nil, []string{"testdata/gotest.jsonl"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(Equal(map[string]string{
			"TestRadosGWTestSuite/TestUser": "passed", "TestRadosGWTestSuite/TestUsage": "failed", "TestRadosGWTestSuite/TestAccount": "skipped",
		}))
		Expect(res.Unstable).To(BeEmpty())
		Expect(res.Meta).To(Equal(map[string]string{"runs": "1"}))
	})

	It("fails when a run lacks a test another run has", func() {
		_, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{"testdata/run1.xml", "testdata/run-short.xml"})
		Expect(err).To(MatchError(ContainSubstring("test_f")))
		Expect(err).To(MatchError(ContainSubstring("testdata/run-short.xml")))
	})

	It("leaves the caller's meta unchanged", func() {
		meta := map[string]string{"release": "squid"}
		_, err := parity.Record(parity.FormatJunit, "s3tests", meta, []string{"testdata/run1.xml"})
		Expect(err).NotTo(HaveOccurred())
		Expect(meta).To(Equal(map[string]string{"release": "squid"}))
	})

	It("takes the more severe outcome of a test reported twice, as pytest reports a call failure and a teardown error", func() {
		run := writeRun("double.xml", `<?xml version="1.0" encoding="utf-8"?>
<testsuites name="pytest tests"><testsuite name="pytest" errors="1" failures="1" skipped="0" tests="2">
<testcase classname="s3tests.functional.test_s3" name="test_x" time="0.1"><failure message="assert 1 == 2">E assert 1 == 2</failure></testcase>
<testcase classname="s3tests.functional.test_s3" name="test_x" time="0.1"><error message="failed on teardown with &quot;ClientError&quot;">ClientError</error></testcase>
<testcase classname="s3tests.functional.test_s3" name="test_y" time="0.1"/>
<testcase classname="s3tests.functional.test_s3" name="test_z" time="0.1"><error message="failed on setup">E</error></testcase>
<testcase classname="s3tests.functional.test_s3" name="test_z" time="0.1"><failure message="assert 1 == 2">E assert 1 == 2</failure></testcase>
</testsuite></testsuites>`)
		res, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{run})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(Equal(map[string]string{
			"s3tests.functional.test_s3::test_x": "error", "s3tests.functional.test_s3::test_y": "passed",
			"s3tests.functional.test_s3::test_z": "error",
		}))
	})

	It("reads a report whose root is a single testsuite", func() {
		run := writeRun("bare.xml", `<testsuite name="pytest" tests="1"><testcase classname="c" name="t"/></testsuite>`)
		res, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{run})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(Equal(map[string]string{"c::t": "passed"}))
	})

	It("keeps a parent test that failed while every subtest it ran passed or skipped", func() {
		run := writeRun("teardown.jsonl", strings.Join([]string{
			`{"Action":"run","Package":"p","Test":"TestSuite"}`,
			`{"Action":"run","Package":"p","Test":"TestSuite/TestA"}`,
			`{"Action":"pass","Package":"p","Test":"TestSuite/TestA","Elapsed":0.1}`,
			`{"Action":"run","Package":"p","Test":"TestSuite/TestB"}`,
			`{"Action":"skip","Package":"p","Test":"TestSuite/TestB","Elapsed":0}`,
			`{"Action":"output","Package":"p","Test":"TestSuite","Output":"    suite.go:180: teardown failed\n"}`,
			`{"Action":"fail","Package":"p","Test":"TestSuite","Elapsed":0.2}`,
			`{"Action":"run","Package":"p","Test":"TestOther"}`,
			`{"Action":"pass","Package":"p","Test":"TestOther","Elapsed":0.1}`,
			`{"Action":"fail","Package":"p","Elapsed":0.3}`,
		}, "\n")+"\n")
		res, err := parity.Record(parity.FormatGoTest, "admin", nil, []string{run})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(Equal(map[string]string{
			"TestSuite": "failed", "TestSuite/TestA": "passed", "TestSuite/TestB": "skipped", "TestOther": "passed",
		}))
	})

	It("drops a parent test that passed with its subtests", func() {
		run := writeRun("passed.jsonl", strings.Join([]string{
			`{"Action":"run","Package":"p","Test":"TestSuite"}`,
			`{"Action":"run","Package":"p","Test":"TestSuite/TestA"}`,
			`{"Action":"pass","Package":"p","Test":"TestSuite/TestA","Elapsed":0.1}`,
			`{"Action":"pass","Package":"p","Test":"TestSuite","Elapsed":0.2}`,
			`{"Action":"pass","Package":"p","Elapsed":0.3}`,
		}, "\n")+"\n")
		res, err := parity.Record(parity.FormatGoTest, "admin", nil, []string{run})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(Equal(map[string]string{"TestSuite/TestA": "passed"}))
	})

	It("fails on a run that holds no test", func() {
		run := writeRun("empty.xml", `<testsuites><testsuite name="pytest" tests="0"></testsuite></testsuites>`)
		_, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{run})
		Expect(err).To(MatchError(ContainSubstring("no test")))
		Expect(err).To(MatchError(ContainSubstring(run)))
	})

	It("fails naming the file and line of a go test -json line that is not an event", func() {
		run := writeRun("broken.jsonl", `{"Action":"pass","Package":"p","Test":"TestA"}`+"\nFAIL p [build failed]\n")
		_, err := parity.Record(parity.FormatGoTest, "admin", nil, []string{run})
		Expect(err).To(MatchError(ContainSubstring(run + ":2")))
	})

	It("fails naming the file of a junit report it cannot parse", func() {
		run := writeRun("broken.xml", `<testsuites><testsuite><testcase classname="c" name="t">`)
		_, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{run})
		Expect(err).To(MatchError(ContainSubstring(run)))
	})

	It("refuses an unknown format", func() {
		_, err := parity.Record(parity.Format("tap"), "s3tests", nil, []string{"testdata/run1.xml"})
		Expect(err).To(MatchError(ContainSubstring("tap")))
	})

	It("refuses to record from no run", func() {
		_, err := parity.Record(parity.FormatJunit, "s3tests", nil, nil)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("diff", func() {
	var base *parity.Ledger

	BeforeEach(func() {
		unstable := entry("aaa", "passed")
		unstable.Unstable = true
		base = ledgerOf(map[string][]parity.Entry{
			"t1": {entry("aaa", "passed")}, "t2": {entry("aaa", "failed")}, "t3": {unstable},
		})
	})

	It("is equal when only unstable tests differ", func() {
		cand := candidate(map[string]string{"t1": "passed", "t2": "failed", "t3": "failed"})
		cand.Meta["gateway"] = "rgw-go"
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	It("names every stable test whose outcome changed, and a test missing from the candidate", func() {
		diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "failed", "t3": "passed"}), false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{"t1: baseline passed, candidate failed", "t2: baseline failed, candidate missing"}))
	})

	It("names a candidate test the ledger holds no entry for", func() {
		cand := candidate(map[string]string{"t1": "passed", "t2": "failed", "t3": "passed", "t4": "error"})
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{"t4: no baseline entry"}))
	})

	It("judges a test by its last entry", func() {
		base.Tests["t1"] = append(base.Tests["t1"], reasoned(entry("aaa", "failed"), "radosgw 19.2.6 answers 400"))
		diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "failed", "t2": "failed"}), false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	Describe("a retired test", func() {
		BeforeEach(func() {
			base.Tests["t2"] = append(base.Tests["t2"], reasoned(entry("aaa", "retired"), "deselected"))
		})

		It("is not missing from a candidate that lacks it", func() {
			diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "passed"}), false, parity.KnownList{}, parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(BeEmpty())
		})

		It("is named when the candidate runs it again", func() {
			diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "passed", "t2": "failed"}), false, parity.KnownList{}, parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t2: baseline retired, candidate failed"}))
		})
	})

	Describe("validity", func() {
		It("is stale for a last entry recorded under another Ceph version, unless meta drift is allowed", func() {
			base.Tests["t1"] = []parity.Entry{withCeph(entry("aaa", "passed"), "19.2.5")}
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed"})
			diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t1: baseline stale, re-record"}))
			diffs, err = parity.Diff(base, cand, true, parity.KnownList{}, parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t1: baseline passed, candidate failed"}))
		})

		It("is stale for a last entry whose source a later base pin replaced, the chain reset", func() {
			base.Pins = append(base.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitBase})
			base.Tests["t1"] = append(base.Tests["t1"], entry("bbb", "passed"))
			cand := candidate(map[string]string{"t1": "passed", "t2": "failed"})
			cand.Meta["s3tests_commit"] = "bbb"
			for _, drift := range []bool{false, true} {
				diffs, err := parity.Diff(base, cand, drift, parity.KnownList{}, parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(Equal([]string{"t2: baseline stale, re-record", "t3: baseline stale, re-record"}), "drift %v", drift)
			}
		})

		DescribeTable("holds across a pin proven to keep the outcomes",
			func(admitted parity.Admission) {
				base.Pins = append(base.Pins, parity.Pin{Source: "bbb", Admitted: admitted})
				base.Tests["t4"] = []parity.Entry{entry("bbb", "passed")}
				cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t4": "passed"})
				cand.Meta["s3tests_commit"] = "bbb"
				diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(Equal([]string{"t1: baseline passed, candidate failed"}))
			},
			Entry("additive", parity.AdmitAdditive),
			Entry("reworded", parity.AdmitReworded),
		)

		It("is judged before an unstable last entry is skipped", func() {
			base.Tests["t3"][0].CephVersion = "19.2.5"
			diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}), false, parity.KnownList{}, parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t3: baseline stale, re-record"}))
		})
	})

	It("refuses a candidate run at a source other than the chain's head unless told to", func() {
		base.Pins = append(base.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitAdditive})
		cand := candidate(map[string]string{"t1": "passed", "t2": "failed"})
		_, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("s3tests_commit: baseline bbb, candidate aaa")))
		_, err = parity.Diff(base, cand, true, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("names every compared key that differs", func() {
		cand := candidate(map[string]string{"t1": "passed"})
		cand.Meta["s3tests_commit"], cand.Meta["release"] = "bbb", "tentacle"
		_, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).To(MatchError(ContainSubstring("s3tests_commit: baseline aaa, candidate bbb")))
		Expect(err).To(MatchError(ContainSubstring("release: baseline squid, candidate tentacle")))
	})

	DescribeTable("refuses a candidate that lacks a key it needs, drift allowed or not",
		func(key, want string) {
			cand := candidate(map[string]string{"t1": "passed"})
			delete(cand.Meta, key)
			for _, drift := range []bool{false, true} {
				_, err := parity.Diff(base, cand, drift, parity.KnownList{}, parity.Selection{})
				Expect(err).To(MatchError(ContainSubstring(want)), "drift %v", drift)
			}
		},
		Entry("the source key", "s3tests_commit", "s3tests_commit: baseline aaa, candidate missing"),
		Entry("the release", "release", "release: baseline squid, candidate missing"),
		Entry("the Ceph version", "ceph_version", "ceph_version: candidate missing"),
	)

	It("ignores the deselect digest, the gateway and any key outside the source, release and Ceph version", func() {
		cand := candidate(map[string]string{"t1": "passed", "t2": "failed"})
		cand.Meta["deselect"], cand.Meta["gateway"], cand.Meta["go_ceph_tag"] = "222222222222", "rgw-go", "v0.39.0"
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	Describe("with a known-difference list", func() {
		It("skips a listed test whose outcome differs", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "passed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t1\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t2: baseline failed, candidate passed"}))
		})

		It("reports a listed test whose outcome no longer differs", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t[12]\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t2: known difference no longer differs"}))
		})

		It("reports an entry that matches no compared test, an unstable one included", func() {
			diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "passed", "t2": "failed"}), false, mustKnown("t9\nt3\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(ConsistOf("t9: known difference matches no test", "t3: known difference matches no test"))
		})

		It("skips a test with no baseline entry that a plain or an either-outcome entry matches", func() {
			cand := candidate(map[string]string{"t1": "passed", "t2": "failed", "t4": "error", "t5": "passed", "t6": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t4\n~ t5\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t6: no baseline entry"}))
		})

		It("leaves a stale test's line alone, and counts the entry matching it as matched", func() {
			base.Tests["t1"][0].CephVersion = "19.2.5"
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t1\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t1: baseline stale, re-record"}))
		})

		It("matches a pattern against the whole test id", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(ConsistOf("t: known difference matches no test", "t1: baseline passed, candidate failed"))
		})

		It("counts a test missing from the candidate as differing", func() {
			cand := candidate(map[string]string{"t1": "passed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t2\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(BeEmpty())
		})

		Describe("an either-outcome entry", func() {
			It("skips its test when the outcome differs", func() {
				cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
				diffs, err := parity.Diff(base, cand, false, mustKnown("~ t1\n"), parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(BeEmpty())
			})

			It("skips its test when the outcome agrees, where a plain entry would be stale", func() {
				cand := candidate(map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"})
				diffs, err := parity.Diff(base, cand, false, mustKnown("~ t1\nt2\n"), parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(Equal([]string{"t2: known difference no longer differs"}))
			})

			It("leaves a plain entry for the same test strict", func() {
				cand := candidate(map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"})
				diffs, err := parity.Diff(base, cand, false, mustKnown("~ t1\nt1\n"), parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(Equal([]string{"t1: known difference no longer differs"}))
			})

			It("is reported, as written, when it matches no compared test", func() {
				diffs, err := parity.Diff(base, candidate(map[string]string{"t1": "passed", "t2": "failed"}), false, mustKnown("~ t9\n~ t3\n"), parity.Selection{})
				Expect(err).NotTo(HaveOccurred())
				Expect(diffs).To(ConsistOf("~ t9: known difference matches no test", "~ t3: known difference matches no test"))
			})
		})

		It("compares under the zero list as under an empty parsed one", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
			zero, err := parity.Diff(base, cand, false, parity.KnownList{}, parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(zero).To(Equal([]string{"t1: baseline passed, candidate failed"}))
			parsed, err := parity.Diff(base, cand, false, mustKnown("# no entries\n"), parity.Selection{})
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed).To(Equal(zero))
		})
	})
})

var _ = Describe("ParseKnown", func() {
	It("reads one pattern per line, dropping comments and blank lines", func() {
		known, err := parity.ParseKnown(strings.NewReader(
			"# rgw-go answers 501 for these\n\ns3tests.functional.test_s3::test_a  # the reason\n  s3tests\\.functional\\.test_s3::test_b.*\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(known.Patterns()).To(Equal([]string{"s3tests.functional.test_s3::test_a", `s3tests\.functional\.test_s3::test_b.*`}))
	})

	It("reads a line starting with ~ as an either-outcome entry", func() {
		known, err := parity.ParseKnown(strings.NewReader("~ t1  # timing\n  ~\tt2\n~t3\nt4\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(known.Patterns()).To(Equal([]string{"~ t1", "~ t2", "~ t3", "t4"}))
	})

	It("names the line of a pattern that does not compile", func() {
		_, err := parity.ParseKnown(strings.NewReader("t1\n\nt[2\n"))
		Expect(err).To(MatchError(ContainSubstring("line 3")))
		_, err = parity.ParseKnown(strings.NewReader("t1\n~ t[2\n"))
		Expect(err).To(MatchError(ContainSubstring("line 2")))
	})

	It("refuses a ~ with no pattern after it", func() {
		_, err := parity.ParseKnown(strings.NewReader("t1\n~  # flaky\n"))
		Expect(err).To(MatchError(ContainSubstring("line 2: ~ without a pattern")))
	})
})
