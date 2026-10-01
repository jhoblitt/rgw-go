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

// candidate returns a result with outcomes and the meta of the diff specs'
// baseline.
func candidate(outcomes map[string]string) parity.Result {
	return parity.Result{Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid"}, Outcomes: outcomes}
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
	var base parity.Result

	BeforeEach(func() {
		base = parity.Result{
			Suite: "s3tests", Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid"},
			Outcomes: map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}, Unstable: []string{"t3"},
		}
	})

	It("is equal when only unstable tests differ", func() {
		cand := parity.Result{
			Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid", "gateway": "rgw-go"},
			Outcomes: map[string]string{"t1": "passed", "t2": "failed", "t3": "failed"},
		}
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	It("names every stable test whose outcome changed, and a test missing from the candidate", func() {
		cand := parity.Result{
			Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid"},
			Outcomes: map[string]string{"t1": "failed", "t3": "passed"},
		}
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(ConsistOf("t1: baseline passed, candidate failed", "t2: baseline failed, candidate missing"))
	})

	It("names a test missing from the baseline", func() {
		cand := parity.Result{
			Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid"},
			Outcomes: map[string]string{"t1": "passed", "t2": "failed", "t3": "passed", "t4": "error"},
		}
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(Equal([]string{"t4: baseline missing, candidate error"}))
	})

	It("refuses to compare across a different s3-tests commit unless told to", func() {
		cand := parity.Result{Meta: map[string]string{"s3tests_commit": "bbb", "release": "squid"}, Outcomes: base.Outcomes}
		_, err := parity.Diff(base, cand, false, parity.KnownList{})
		Expect(err).To(MatchError(ContainSubstring("s3tests_commit")))
		_, err = parity.Diff(base, cand, true, parity.KnownList{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses to compare results run under different deselect lists", func() {
		listed := parity.Result{
			Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid", "deselect": "111111111111"},
			Outcomes: base.Outcomes,
		}
		cand := parity.Result{
			Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid", "deselect": "222222222222"},
			Outcomes: base.Outcomes,
		}
		_, err := parity.Diff(listed, cand, false, parity.KnownList{})
		Expect(err).To(MatchError(ContainSubstring("deselect")))
	})

	It("names every compared key that differs", func() {
		baseMeta := map[string]string{
			"s3tests_commit": "aaa", "go_ceph_tag": "v0.39.0", "release": "squid", "ceph_version": "19.2.6", "deselect": "111111111111",
		}
		candMeta := map[string]string{
			"s3tests_commit": "bbb", "go_ceph_tag": "v0.40.0", "release": "tentacle", "ceph_version": "20.2.4", "deselect": "222222222222",
		}
		_, err := parity.Diff(parity.Result{Meta: baseMeta, Outcomes: base.Outcomes},
			parity.Result{Meta: candMeta, Outcomes: base.Outcomes}, false, parity.KnownList{})
		for _, key := range []string{"s3tests_commit", "go_ceph_tag", "release", "ceph_version", "deselect"} {
			Expect(err).To(MatchError(ContainSubstring("%s: baseline %s, candidate %s", key, baseMeta[key], candMeta[key])), "key %q", key)
		}
	})

	It("refuses a candidate that lacks a compared key the baseline carries, drift allowed or not", func() {
		cand := parity.Result{Meta: map[string]string{"release": "squid"}, Outcomes: base.Outcomes}
		_, err := parity.Diff(base, cand, false, parity.KnownList{})
		Expect(err).To(MatchError(ContainSubstring("s3tests_commit: baseline aaa, candidate missing")))
		_, err = parity.Diff(base, cand, true, parity.KnownList{})
		Expect(err).To(MatchError(ContainSubstring("s3tests_commit: baseline aaa, candidate missing")))
	})

	It("allows a compared key only the candidate carries", func() {
		cand := parity.Result{
			Meta: map[string]string{
				"s3tests_commit": "aaa", "release": "squid", "deselect": "136c2f295daa", "go_ceph_tag": "v0.39.0", "ceph_version": "19.2.6",
			},
			Outcomes: base.Outcomes,
		}
		diffs, err := parity.Diff(base, cand, false, parity.KnownList{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	It("ignores keys outside the compared set, such as the gateway", func() {
		cand := parity.Result{
			Meta:     map[string]string{"s3tests_commit": "aaa", "release": "squid", "gateway": "rgw-go"},
			Outcomes: base.Outcomes,
		}
		withGateway := base
		withGateway.Meta = map[string]string{"s3tests_commit": "aaa", "release": "squid", "gateway": "radosgw"}
		diffs, err := parity.Diff(withGateway, cand, false, parity.KnownList{})
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})

	Describe("with a known-difference list", func() {
		It("skips a listed test whose outcome differs", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "passed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t1\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t2: baseline failed, candidate passed"}))
		})

		It("reports a listed test whose outcome no longer differs", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t[12]\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(Equal([]string{"t2: known difference no longer differs"}))
		})

		It("reports an entry that matches no compared test, an unstable one included", func() {
			diffs, err := parity.Diff(base, candidate(base.Outcomes), false, mustKnown("t9\nt3\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(ConsistOf("t9: known difference matches no test", "t3: known difference matches no test"))
		})

		It("matches a pattern against the whole test id", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(ConsistOf("t: known difference matches no test", "t1: baseline passed, candidate failed"))
		})

		It("counts a test missing from the candidate as differing", func() {
			cand := candidate(map[string]string{"t1": "passed", "t3": "passed"})
			diffs, err := parity.Diff(base, cand, false, mustKnown("t2\n"))
			Expect(err).NotTo(HaveOccurred())
			Expect(diffs).To(BeEmpty())
		})

		It("compares under the zero list as under an empty parsed one", func() {
			cand := candidate(map[string]string{"t1": "failed", "t2": "failed", "t3": "passed"})
			zero, err := parity.Diff(base, cand, false, parity.KnownList{})
			Expect(err).NotTo(HaveOccurred())
			Expect(zero).To(Equal([]string{"t1: baseline passed, candidate failed"}))
			parsed, err := parity.Diff(base, cand, false, mustKnown("# no entries\n"))
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

	It("names the line of a pattern that does not compile", func() {
		_, err := parity.ParseKnown(strings.NewReader("t1\n\nt[2\n"))
		Expect(err).To(MatchError(ContainSubstring("line 3")))
	})
})
