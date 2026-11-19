package parity_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// mustLeave parses a leave list, failing the spec on an error.
func mustLeave(list string) parity.Leave {
	leave, err := parity.ParseLeave(strings.NewReader(list))
	Expect(err).NotTo(HaveOccurred())
	return leave
}

// passedRun writes a junit run file in which each of test_s3.py's tests
// names passed, and returns its path.
func passedRun(names ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n" + `<testsuites name="pytest tests"><testsuite name="pytest">` + "\n")
	for _, name := range names {
		b.WriteString(`<testcase classname="s3tests.functional.test_s3" name="` + name + `" time="0.1"/>` + "\n")
	}
	b.WriteString("</testsuite></testsuites>\n")
	return writeRun("run.xml", b.String())
}

// s3id returns hack/parity's id for test_s3.py's test name.
func s3id(name string) string {
	return "s3tests.functional.test_s3::" + name
}

var _ = Describe("ParseLeave", func() {
	It("reads a .gate-only.ids file: a pytest node id a line, blank lines aside, by its junit id", func() {
		leave := mustLeave("s3tests/functional/test_s3.py::test_a\n\n" +
			"s3tests/functional/test_s3.py::test_q[x]\n" +
			"s3tests/functional/test_headers.py::test_h")
		Expect(parity.LeaveIDs(leave)).To(Equal([]string{
			"s3tests.functional.test_headers::test_h", s3id("test_a"), s3id("test_q[x]"),
		}))
	})

	It("may name no test, as the file of a run whose selection left none out", func() {
		Expect(parity.LeaveIDs(mustLeave(""))).To(BeEmpty())
	})

	It("names each test by its exact id, so a base id names no parametrization", func() {
		leave := mustLeave("s3tests/functional/test_s3.py::test_q\n")
		Expect(parity.LeaveIDs(leave)).To(Equal([]string{s3id("test_q")}))
		out, _, err := parity.AppendRecord(nil, recorded("aaa", map[string]string{s3id("test_q[x]"): "passed"}),
			parity.RecordOptions{Leave: leave})
		Expect(err).NotTo(HaveOccurred(), "a run of test_q[x] is not one the list names")
		Expect(out.Tests).To(HaveKey(s3id("test_q[x]")))
	})

	DescribeTable("refuses a line that is not a node id",
		func(list, want string) {
			_, err := parity.ParseLeave(strings.NewReader(list))
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a name alone", "s3tests/functional/test_s3.py::test_a\ntest_b\n",
			"line 2: test_b is not a pytest node id, FILE.py::NAME"),
		Entry("gate-only.txt's comment, which only the selection reads", "# the time-driven tests\n",
			"line 1: # the time-driven tests is not a pytest node id, FILE.py::NAME"),
	)
})

var _ = Describe("AppendRecord with a leave list", func() {
	var first *parity.Ledger

	BeforeEach(func() {
		first, _ = mustAppend(nil, recorded("aaa", map[string]string{
			s3id("test_a"): "passed", s3id("test_b"): "failed", s3id("test_p[x]"): "passed", s3id("test_p[y]"): "failed",
		}), parity.RecordOptions{})
	})

	It("keeps the entries of each test it names through a full record that lacks them", func() {
		leave := mustLeave("s3tests/functional/test_s3.py::test_b\n" +
			"s3tests/functional/test_s3.py::test_p[x]\ns3tests/functional/test_s3.py::test_p[y]\n")
		out, appended := mustAppend(first, recorded("aaa", map[string]string{s3id("test_a"): "passed"}),
			parity.RecordOptions{Leave: leave})
		Expect(appended).To(BeEmpty())
		Expect(out).To(Equal(first))
	})

	It("still retires a test the runs lack that it does not name", func() {
		leave := mustLeave("s3tests/functional/test_s3.py::test_p[x]\n")
		_, appended := mustAppend(first, recorded("aaa", map[string]string{s3id("test_a"): "passed", s3id("test_b"): "failed"}),
			parity.RecordOptions{Leave: leave, Reasons: map[string]string{s3id("test_p[y]"): "deselected for phase 3"}})
		Expect(ids(appended)).To(Equal([]string{s3id("test_p[y]")}))
		Expect(appended[0].Entry.Outcome).To(Equal("retired"))
	})

	It("refuses runs that ran a test it names, naming each", func() {
		leave := mustLeave("s3tests/functional/test_s3.py::test_a\ns3tests/functional/test_s3.py::test_p[x]\n")
		_, _, err := parity.AppendRecord(first, recorded("aaa", map[string]string{
			s3id("test_a"): "passed", s3id("test_b"): "failed", s3id("test_p[x]"): "passed",
		}), parity.RecordOptions{Leave: leave})
		Expect(err).To(MatchError(ContainSubstring(s3id("test_a") + ": the runs ran it, and the leave list names it for another record")))
		Expect(err).To(MatchError(ContainSubstring(s3id("test_p[x]") + ": the runs ran it")))
		Expect(err).NotTo(MatchError(ContainSubstring(s3id("test_b"))))
	})

	It("is refused for a subset record, which retires nothing", func() {
		_, _, err := parity.AppendRecord(first, recorded("aaa", map[string]string{s3id("test_a"): "passed"}),
			parity.RecordOptions{Subset: true, Leave: mustLeave("s3tests/functional/test_s3.py::test_b\n")})
		Expect(err).To(MatchError(ContainSubstring("a subset record retires nothing")))
	})

	It("starts a ledger with no entry for a test it names", func() {
		out, _ := mustAppend(nil, recorded("aaa", map[string]string{s3id("test_a"): "passed"}),
			parity.RecordOptions{Leave: mustLeave("s3tests/functional/test_s3.py::test_b\n")})
		Expect(out.Tests).To(HaveLen(1))
		Expect(out.Tests).To(HaveKey(s3id("test_a")))
	})

	It("records as a full record does when it names no test", func() {
		res := recorded("aaa", map[string]string{s3id("test_a"): "passed"})
		plain, _, err := parity.AppendRecord(first, res, parity.RecordOptions{Draft: "recorded by CI"})
		Expect(err).NotTo(HaveOccurred())
		left, _, err := parity.AppendRecord(first, res, parity.RecordOptions{Draft: "recorded by CI", Leave: mustLeave("")})
		Expect(err).NotTo(HaveOccurred())
		Expect(left).To(Equal(plain))
	})
})

var _ = Describe("record --leave", func() {
	var dir, ledger, leave string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		ledger = filepath.Join(dir, "squid.json")
		leave = filepath.Join(dir, "base2.gate-only.ids")
		Expect(os.WriteFile(leave, []byte("s3tests/functional/test_s3.py::test_g\n"), 0o600)).To(Succeed())
	})

	// leaveArgs returns the command line that appends the run file, twice,
	// to the ledger with flags.
	leaveArgs := func(file string, flags ...string) []string {
		return ledgerArgs(ledger, flags, file, file)
	}

	It("keeps the entries of a listed test the ledger holds through a full record that lacks it", func(ctx SpecContext) {
		appendLedger(ctx, ledger, nil, passedRun("test_a", "test_g"), passedRun("test_a", "test_g"))
		status, stdout, stderr := run(ctx, leaveArgs(passedRun("test_a"), "--leave", leave)...)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(ContainSubstring("appended 0 entries"))
		Expect(stdout).To(ContainSubstring("left 1 ledger tests the leave list names to another record"))
		Expect(outcomes(readLedger(ledger))).To(Equal(map[string]string{s3id("test_a"): "passed", s3id("test_g"): "passed"}))
	})

	It("still retires a test the runs lack that the list does not name", func(ctx SpecContext) {
		appendLedger(ctx, ledger, nil, passedRun("test_a", "test_f", "test_g"), passedRun("test_a", "test_f", "test_g"))
		status, stdout, stderr := run(ctx, leaveArgs(passedRun("test_a"), "--leave", leave, "--draft", "recorded by CI")...)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(ContainSubstring(s3id("test_f") + ": passed -> retired: unreviewed: recorded by CI"))
		Expect(outcomes(readLedger(ledger))).To(Equal(map[string]string{
			s3id("test_a"): "passed", s3id("test_f"): "passed retired", s3id("test_g"): "passed",
		}))
	})

	It("refuses runs that ran a listed test, naming it, and writes nothing", func(ctx SpecContext) {
		appendLedger(ctx, ledger, nil, passedRun("test_a", "test_g"), passedRun("test_a", "test_g"))
		before, err := os.ReadFile(ledger)
		Expect(err).NotTo(HaveOccurred())
		status, _, stderr := run(ctx, leaveArgs(passedRun("test_a", "test_g"), "--leave", leave)...)
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring(s3id("test_g") + ": the runs ran it, and the leave list names it for another record"))
		after, err := os.ReadFile(ledger)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
	})

	It("starts a ledger with no entry for a listed test", func(ctx SpecContext) {
		status, stdout, stderr := run(ctx, leaveArgs(passedRun("test_a"), "--leave", leave)...)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).NotTo(ContainSubstring("left"))
		Expect(outcomes(readLedger(ledger))).To(Equal(map[string]string{s3id("test_a"): "passed"}))
	})

	It("exits 1 for a list it cannot read as one", func(ctx SpecContext) {
		Expect(os.WriteFile(leave, []byte("test_g\n"), 0o600)).To(Succeed())
		status, _, stderr := run(ctx, leaveArgs(passedRun("test_a"), "--leave", leave)...)
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring(leave + ": reading the leave list: line 1: test_g is not a pytest node id"))
		_, err := os.Stat(ledger)
		Expect(err).To(MatchError(os.ErrNotExist))
	})

	DescribeTable("exits 2 for a record that cannot leave tests",
		func(ctx SpecContext, want string, flags ...string) {
			args := append([]string{"record", "--format", "junit", "--suite", "s3tests"}, flags...)
			status, _, stderr := run(ctx, append(args, "--leave", leave, "testdata/run1.xml", "testdata/run2.xml")...)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("a subset record, which retires nothing", "--leave is for a full record", "--ledger", "l.json", "--subset"),
		Entry("a result file", "--subset, --leave, --reasons, --draft and --pin-reason are for --ledger", "--out", "x.json"),
	)

	It("never reads --leave from PARITY_LEAVE", func(ctx SpecContext) {
		appendLedger(ctx, ledger, nil, passedRun("test_a", "test_g"), passedRun("test_a", "test_g"))
		GinkgoT().Setenv("PARITY_LEAVE", leave)
		status, _, stderr := run(ctx, leaveArgs(passedRun("test_a"))...)
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring(s3id("test_g") + ": passed -> retired needs a reason"))
	})
})
