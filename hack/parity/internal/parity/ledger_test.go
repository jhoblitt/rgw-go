package parity_test

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// recorded returns a result as the record command stamps it: radosgw's
// outcomes on squid's 19.2.6 at the s3-tests commit source, from two runs,
// the unstable tests listed.
func recorded(source string, outcomes map[string]string, unstable ...string) parity.Result {
	return parity.Result{
		Suite: "s3tests",
		Meta: map[string]string{
			"s3tests_commit": source, "release": "squid", "ceph_version": "19.2.6", "gateway": "radosgw",
			"deselect": "999e22cc41a0", "runs": "2", "recorded": "2026-10-12T16:55:39Z",
		},
		Outcomes: outcomes,
		Unstable: append([]string{}, unstable...),
	}
}

// mustAppend appends res to l, failing the spec on an error.
func mustAppend(l *parity.Ledger, res parity.Result, opts parity.RecordOptions) (*parity.Ledger, []parity.Appended) {
	out, appended, err := parity.AppendRecord(l, res, opts)
	Expect(err).NotTo(HaveOccurred())
	return out, appended
}

// ids returns the test ids of appended, in order.
func ids(appended []parity.Appended) []string {
	out := []string{}
	for _, a := range appended {
		out = append(out, a.ID)
	}
	return out
}

// outcomes returns each test's outcomes, oldest first, joined by spaces.
func outcomes(l *parity.Ledger) map[string]string {
	out := map[string]string{}
	for id, entries := range l.Tests {
		var o []string
		for _, e := range entries {
			o = append(o, e.Outcome)
		}
		out[id] = strings.Join(o, " ")
	}
	return out
}

var _ = Describe("AppendRecord", func() {
	var first *parity.Ledger

	BeforeEach(func() {
		first, _ = mustAppend(nil, recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}),
			parity.RecordOptions{})
	})

	It("starts a ledger with one base pin and an entry per test, the meta's other pairs in each", func() {
		Expect(first.Suite).To(Equal("s3tests"))
		Expect(first.Release).To(Equal("squid"))
		Expect(first.SourceKey).To(Equal("s3tests_commit"))
		Expect(first.Pins).To(Equal([]parity.Pin{{Source: "aaa", Admitted: parity.AdmitBase}}))
		Expect(first.Tests).To(HaveLen(3))
		Expect(first.Tests["t2"]).To(Equal([]parity.Entry{{
			Outcome: "failed", Source: "aaa", CephVersion: "19.2.6", Recorded: "2026-10-12T16:55:39Z", Runs: 2,
			Meta: map[string]string{"gateway": "radosgw", "deselect": "999e22cc41a0"},
		}}))
		Expect(parity.Validate(first)).To(BeEmpty())
	})

	It("refuses to start a ledger from a subset", func() {
		_, _, err := parity.AppendRecord(nil, recorded("aaa", map[string]string{"t1": "passed"}), parity.RecordOptions{Subset: true})
		Expect(err).To(MatchError(ContainSubstring("full record")))
	})

	It("refuses a record of fewer than two runs", func() {
		res := recorded("aaa", map[string]string{"t1": "passed"})
		res.Meta["runs"] = "1"
		_, _, err := parity.AppendRecord(first, res, parity.RecordOptions{})
		Expect(err).To(MatchError(ContainSubstring("at least 2 runs")))
	})

	It("appends nothing for a record in which nothing changed", func() {
		res := recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"})
		res.Meta["recorded"] = "2026-10-13T00:00:00Z"
		out, appended := mustAppend(first, res, parity.RecordOptions{})
		Expect(appended).To(BeEmpty())
		Expect(out).To(Equal(first))
	})

	Describe("the reason rule", func() {
		var flipped parity.Result

		BeforeEach(func() {
			flipped = recorded("aaa", map[string]string{"t1": "failed", "t2": "passed", "t3": "passed"})
		})

		It("refuses an unreasoned flip, naming every offender, and leaves the ledger alone", func() {
			before := first.Tests["t1"]
			_, _, err := parity.AppendRecord(first, flipped, parity.RecordOptions{})
			Expect(err).To(MatchError(ContainSubstring("t1: passed -> failed needs a reason")))
			Expect(err).To(MatchError(ContainSubstring("t2: failed -> passed needs a reason")))
			Expect(first.Tests["t1"]).To(Equal(before))
		})

		It("accepts a reasoned flip and keeps its reason", func() {
			out, appended := mustAppend(first, flipped, parity.RecordOptions{Reasons: map[string]string{
				"t1": "radosgw 19.2.6 answers 400 since the fix", "t2": "the suite's fix landed",
			}})
			Expect(ids(appended)).To(Equal([]string{"t1", "t2"}))
			Expect(appended[0].Change).To(Equal("passed -> failed"))
			Expect(appended[0].Reasoned).To(BeTrue())
			Expect(out.Tests["t1"]).To(HaveLen(2))
			Expect(out.Tests["t1"][1].Reason).To(Equal("radosgw 19.2.6 answers 400 since the fix"))
			Expect(parity.Validate(out)).To(BeEmpty())
		})

		It("makes a draft, marking each entry that needs a reason the reasons do not give", func() {
			out, _ := mustAppend(first, flipped, parity.RecordOptions{
				Reasons: map[string]string{"t1": "own"}, Draft: "recorded by CI",
			})
			Expect(out.Tests["t1"][1].Reason).To(Equal("own"))
			Expect(out.Tests["t2"][1].Reason).To(Equal("unreviewed: recorded by CI"))
			Expect(parity.ValidateDraft(out)).To(BeEmpty())
			Expect(parity.Validate(out)).To(ConsistOf(
				`t2 entry 2: the draft's reason "unreviewed: recorded by CI"; replace it with the change's own before committing`))

			again, appended := mustAppend(out, flipped, parity.RecordOptions{})
			Expect(appended).To(BeEmpty(), "a draft takes further records")
			Expect(again).To(Equal(out))
		})

		It("refuses a reason that starts as a draft's", func() {
			_, _, err := parity.AppendRecord(first, flipped, parity.RecordOptions{
				Reasons: map[string]string{"t1": "unreviewed: later", "t2": "own"},
			})
			Expect(err).To(MatchError(ContainSubstring(`t1: the reason starts with "unreviewed:", which marks a draft`)))
		})

		It("needs a reason for a test newly unstable", func() {
			res := recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}, "t3")
			_, _, err := parity.AppendRecord(first, res, parity.RecordOptions{})
			Expect(err).To(MatchError(ContainSubstring("t3: passed -> passed (unstable) needs a reason")))
		})

		It("appends nothing for an unstable test whose last run gave another outcome", func() {
			unstable, _ := mustAppend(first, recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}, "t3"),
				parity.RecordOptions{Reasons: map[string]string{"t3": "flaky on radosgw"}})
			_, appended := mustAppend(unstable, recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "failed"}, "t3"),
				parity.RecordOptions{})
			Expect(appended).To(BeEmpty())
		})

		It("refuses a reason for a test the record does not append", func() {
			_, _, err := parity.AppendRecord(first, recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}),
				parity.RecordOptions{Reasons: map[string]string{"t1": "a flip that did not happen"}})
			Expect(err).To(MatchError(ContainSubstring("t1: a reason for a test this record does not append")))
		})
	})

	It("appends for every test of a full record under a new Ceph version, with no reason where the outcome holds", func() {
		res := recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"})
		res.Meta["ceph_version"] = "19.2.7"
		out, appended := mustAppend(first, res, parity.RecordOptions{})
		Expect(ids(appended)).To(Equal([]string{"t1", "t2", "t3"}))
		for _, id := range []string{"t1", "t2", "t3"} {
			Expect(out.Tests[id]).To(HaveLen(2))
			Expect(out.Tests[id][1].CephVersion).To(Equal("19.2.7"))
		}
	})

	Describe("a full record that lacks a test", func() {
		var res parity.Result

		BeforeEach(func() {
			res = recorded("aaa", map[string]string{"t1": "passed", "t2": "failed"})
		})

		It("retires it, which needs a reason", func() {
			_, _, err := parity.AppendRecord(first, res, parity.RecordOptions{})
			Expect(err).To(MatchError(ContainSubstring("t3: passed -> retired needs a reason")))

			out, appended := mustAppend(first, res, parity.RecordOptions{Reasons: map[string]string{"t3": "deselected for phase 3"}})
			Expect(ids(appended)).To(Equal([]string{"t3"}))
			Expect(out.Tests["t3"][1]).To(SatisfyAll(
				HaveField("Outcome", "retired"), HaveField("Unstable", false), HaveField("Reason", "deselected for phase 3")))
		})

		It("leaves a test it retired before alone", func() {
			retired, _ := mustAppend(first, res, parity.RecordOptions{Reasons: map[string]string{"t3": "deselected"}})
			_, appended := mustAppend(retired, res, parity.RecordOptions{})
			Expect(appended).To(BeEmpty())
		})

		It("brings a retired test back with a reason", func() {
			retired, _ := mustAppend(first, res, parity.RecordOptions{Reasons: map[string]string{"t3": "deselected"}})
			back := recorded("aaa", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"})
			_, _, err := parity.AppendRecord(retired, back, parity.RecordOptions{})
			Expect(err).To(MatchError(ContainSubstring("t3: retired -> passed needs a reason")))
		})
	})

	Describe("a subset record", func() {
		It("appends only the tests it ran, leaving the others alone", func() {
			out, appended := mustAppend(first, recorded("aaa", map[string]string{"t4": "passed", "t2": "error"}),
				parity.RecordOptions{Subset: true, Reasons: map[string]string{"t2": "radosgw times out now"}})
			Expect(ids(appended)).To(Equal([]string{"t2", "t4"}))
			Expect(outcomes(out)).To(Equal(map[string]string{"t1": "passed", "t2": "failed error", "t3": "passed", "t4": "passed"}))
			Expect(appended[1].Change).To(BeEmpty())
		})

		It("refuses a source the chain does not pin", func() {
			_, _, err := parity.AppendRecord(first, recorded("bbb", map[string]string{"t4": "passed"}), parity.RecordOptions{Subset: true})
			Expect(err).To(MatchError(ContainSubstring("pin the new source first")))
		})
	})

	It("resets the chain with a base pin for a full record at a new source, re-appending every test", func() {
		out, appended := mustAppend(first, recorded("bbb", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}),
			parity.RecordOptions{PinReason: "the pin moved past a rewritten commit"})
		Expect(out.Pins).To(Equal([]parity.Pin{
			{Source: "aaa", Admitted: parity.AdmitBase},
			{Source: "bbb", Admitted: parity.AdmitBase, Reason: "the pin moved past a rewritten commit"},
		}))
		Expect(ids(appended)).To(Equal([]string{"t1", "t2", "t3"}))
		Expect(out.Tests["t1"][1].Source).To(Equal("bbb"))
	})

	It("gives a draft's base pin the draft's reason", func() {
		out, _ := mustAppend(first, recorded("bbb", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}),
			parity.RecordOptions{Draft: "recorded by CI"})
		Expect(out.Pins[1].Reason).To(Equal(parity.DraftMarker + " recorded by CI"))
		Expect(parity.ValidateDraft(out)).To(BeEmpty())
		Expect(parity.Validate(out)).To(ContainElement(ContainSubstring(`pin 2's reason "unreviewed: recorded by CI" is a draft's`)))
	})

	DescribeTable("refuses a pin reason",
		func(source, reason, want string) {
			_, _, err := parity.AppendRecord(first, recorded(source, map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}),
				parity.RecordOptions{PinReason: reason})
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("for a record that appends no pin", "aaa", "moved", "the record ran at the chain's head aaa and appends no pin"),
		Entry("that is a draft's", "bbb", "unreviewed: later", "the pin's reason starts with \"unreviewed:\", which marks a draft"),
		Entry("nobody can read", "bbb", "--", "the pin's reason \"--\" is not one a reader can read"),
	)

	It("keeps entries current across an additive pin", func() {
		pinned := first
		Expect(parity.AddPin(pinned, "bbb", parity.AdmitAdditive)).To(Succeed())
		out, appended := mustAppend(pinned, recorded("bbb", map[string]string{"t1": "passed", "t2": "failed", "t3": "passed", "t4": "failed"}),
			parity.RecordOptions{})
		Expect(ids(appended)).To(Equal([]string{"t4"}))
		Expect(out.Pins).To(HaveLen(2))
	})

	It("refuses a record at a source pinned before the head", func() {
		Expect(parity.AddPin(first, "bbb", parity.AdmitReworded)).To(Succeed())
		_, _, err := parity.AppendRecord(first, recorded("aaa", map[string]string{"t1": "passed"}), parity.RecordOptions{Subset: true})
		Expect(err).To(MatchError(ContainSubstring("not at its head bbb")))
	})

	DescribeTable("refuses a record of another ledger",
		func(key, value, want string) {
			res := recorded("aaa", map[string]string{"t1": "passed"})
			if key == "suite" {
				res.Suite = value
			} else {
				res.Meta[key] = value
			}
			_, _, err := parity.AppendRecord(first, res, parity.RecordOptions{Subset: true})
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("suite", "suite", "admin", "suite is admin, the ledger's s3tests"),
		Entry("release", "release", "tentacle", "release is tentacle, the ledger's squid"),
		Entry("source key", "go_ceph_tag", "v0.39.0", "2 of the source keys"),
	)

	It("refuses a record that lacks a meta key it needs", func() {
		res := recorded("aaa", map[string]string{"t1": "passed"})
		delete(res.Meta, "ceph_version")
		_, _, err := parity.AppendRecord(first, res, parity.RecordOptions{Subset: true})
		Expect(err).To(MatchError(ContainSubstring("no ceph_version")))
	})
})

var _ = Describe("Validate", func() {
	It("accepts a ledger that keeps every rule", func() {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed"), reasoned(entry("aaa", "failed"), "radosgw changed")}})
		Expect(parity.Validate(l)).To(BeEmpty())
	})

	DescribeTable("names each broken rule",
		func(want string, breakIt func(l *parity.Ledger)) {
			l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
			breakIt(l)
			Expect(parity.Validate(l)).To(ContainElement(ContainSubstring(want)))
		},
		Entry("an unreasoned flip", "t1 entry 2: passed -> failed without a reason", func(l *parity.Ledger) {
			l.Tests["t1"] = append(l.Tests["t1"], entry("aaa", "failed"))
		}),
		Entry("an unreasoned retirement", "t1 entry 2: passed -> retired without a reason", func(l *parity.Ledger) {
			l.Tests["t1"] = append(l.Tests["t1"], entry("aaa", "retired"))
		}),
		Entry("a flip whose reason is a draft's", `t1 entry 2: the draft's reason "  unreviewed: recorded by CI"`, func(l *parity.Ledger) {
			l.Tests["t1"] = append(l.Tests["t1"], reasoned(entry("aaa", "failed"), "  unreviewed: recorded by CI"))
		}),
		Entry("any entry whose reason is a draft's", `t1 entry 1: the draft's reason "unreviewed:"`, func(l *parity.Ledger) {
			l.Tests["t1"][0].Reason = "unreviewed:"
		}),
		Entry("a first entry that retires", "t2 entry 1: a test's first entry cannot retire it", func(l *parity.Ledger) {
			l.Tests["t2"] = []parity.Entry{reasoned(entry("aaa", "retired"), "gone")}
		}),
		Entry("an unknown outcome", `"xfailed" is not one of`, func(l *parity.Ledger) {
			l.Tests["t1"][0].Outcome = "xfailed"
		}),
		Entry("an unpinned source", `the source "zzz" is not pinned`, func(l *parity.Ledger) {
			l.Tests["t1"][0].Source = "zzz"
		}),
		Entry("an entry pinned before the one it follows", "is pinned before the previous entry's", func(l *parity.Ledger) {
			l.Pins = append(l.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitAdditive})
			l.Tests["t1"] = []parity.Entry{entry("bbb", "passed"), withCeph(entry("aaa", "passed"), "19.2.7")}
		}),
		Entry("a single run", "recorded from 1 runs", func(l *parity.Ledger) {
			l.Tests["t1"][0].Runs = 1
		}),
		Entry("a recording time that is not one", "is not an RFC 3339 time", func(l *parity.Ledger) {
			l.Tests["t1"][0].Recorded = "yesterday"
		}),
		Entry("a chain that starts with another admission", "pin 1 is admitted additive", func(l *parity.Ledger) {
			l.Pins[0].Admitted = parity.AdmitAdditive
		}),
		Entry("a source pinned twice", "pin 2 pins aaa again", func(l *parity.Ledger) {
			l.Pins = append(l.Pins, parity.Pin{Source: "aaa", Admitted: parity.AdmitReworded})
		}),
		Entry("meta that repeats a field", "meta repeats ceph_version", func(l *parity.Ledger) {
			l.Tests["t1"][0].Meta = map[string]string{"ceph_version": "19.2.6"}
		}),
		Entry("an unknown source key", `source_key "commit"`, func(l *parity.Ledger) {
			l.SourceKey = "commit"
		}),
	)
})

var _ = Describe("CheckAppend", func() {
	var old *parity.Ledger

	BeforeEach(func() {
		old = ledgerOf(map[string][]parity.Entry{
			"t1": {entry("aaa", "passed"), reasoned(entry("aaa", "failed"), "radosgw changed")},
			"t2": {entry("aaa", "failed")},
		})
	})

	// appended returns old with a pin, an entry and a test appended.
	appended := func() *parity.Ledger {
		next := ledgerOf(map[string][]parity.Entry{
			"t1": slices.Clone(old.Tests["t1"]),
			"t2": append(slices.Clone(old.Tests["t2"]), reasoned(entry("bbb", "passed"), "the fix landed")),
			"t3": {entry("bbb", "passed")},
		})
		next.Pins = append(next.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitAdditive})
		return next
	}

	It("accepts appended pins, entries and tests", func() {
		Expect(parity.CheckAppend(old, appended(), false)).To(BeEmpty())
	})

	It("lists each appended entry that needed a reason, with it", func() {
		Expect(parity.Reasoned(old, appended())).To(Equal([]string{"t2: failed -> passed: the fix landed"}))
	})

	DescribeTable("refuses",
		func(want string, change func(next *parity.Ledger)) {
			next := appended()
			change(next)
			Expect(parity.CheckAppend(old, next, false)).To(ContainElement(ContainSubstring(want)))
		},
		Entry("an edited entry", "t1 entry 1: changed or moved", func(next *parity.Ledger) {
			next.Tests["t1"][0].Outcome = "error"
		}),
		Entry("an edited reason", "t1 entry 2: changed or moved", func(next *parity.Ledger) {
			next.Tests["t1"][1].Reason = "another"
		}),
		Entry("a removed entry", "t1 entry 2: removed", func(next *parity.Ledger) {
			next.Tests["t1"] = next.Tests["t1"][:1]
		}),
		Entry("reordered entries", "t1 entry 1: changed or moved", func(next *parity.Ledger) {
			t := next.Tests["t1"]
			t[0], t[1] = t[1], t[0]
		}),
		Entry("a removed test", "t2: removed", func(next *parity.Ledger) {
			delete(next.Tests, "t2")
		}),
		Entry("an edited pin", "pin 1: old aaa, base, new aaa, additive", func(next *parity.Ledger) {
			next.Pins[0].Admitted = parity.AdmitAdditive
		}),
		Entry("reordered pins", "a pin is never changed or moved", func(next *parity.Ledger) {
			next.Pins[0], next.Pins[1] = next.Pins[1], next.Pins[0]
		}),
		Entry("a removed pin", "pin 1 (aaa, base): removed", func(next *parity.Ledger) {
			next.Pins = nil
		}),
		Entry("an appended entry without the reason it needs", "t3 entry 2: passed -> failed without a reason", func(next *parity.Ledger) {
			next.Tests["t3"] = append(next.Tests["t3"], entry("bbb", "failed"))
		}),
		Entry("another release", "release: old squid, new tentacle", func(next *parity.Ledger) {
			next.Release = "tentacle"
		}),
	)

	It("names a problem the old ledger has", func() {
		old.Tests["t2"] = append(old.Tests["t2"], entry("aaa", "passed"))
		Expect(parity.CheckAppend(old, old, false)).To(ContainElement("old: t2 entry 2: failed -> passed without a reason"))
	})

	It("refuses an appended entry whose reason is a draft's, unless told the ledger is a draft", func() {
		next := appended()
		next.Tests["t3"] = append(next.Tests["t3"], reasoned(entry("bbb", "failed"), "unreviewed: recorded by CI"))
		Expect(parity.CheckAppend(old, next, false)).To(ConsistOf(
			`t3 entry 2: the draft's reason "unreviewed: recorded by CI"; replace it with the change's own before committing`))
		Expect(parity.CheckAppend(old, next, true)).To(BeEmpty())
		Expect(parity.Reasoned(old, next)).To(ContainElement("t3: passed -> failed: unreviewed: recorded by CI"))
	})

	It("lists, for a ledger with no older version, every entry that needed a reason", func() {
		Expect(parity.Reasoned(nil, appended())).To(Equal([]string{"t1: passed -> failed: radosgw changed", "t2: failed -> passed: the fix landed"}))
	})
})

var _ = Describe("AddPin", func() {
	It("appends an additive or reworded pin", func() {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
		Expect(parity.AddPin(l, "bbb", parity.AdmitReworded)).To(Succeed())
		Expect(parity.AddPin(l, "ccc", parity.AdmitAdditive)).To(Succeed())
		Expect(l.Pins).To(HaveLen(3))
		Expect(parity.Validate(l)).To(BeEmpty())
	})

	It("refuses a base pin, which only a full record appends", func() {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
		Expect(parity.AddPin(l, "bbb", parity.AdmitBase)).To(MatchError(ContainSubstring("full record")))
	})

	It("refuses a source the chain pins already", func() {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
		Expect(parity.AddPin(l, "aaa", parity.AdmitAdditive)).To(MatchError(ContainSubstring("already pin 1")))
	})
})

var _ = Describe("migration", func() {
	var flat parity.Result

	BeforeEach(func() {
		flat = parity.Result{
			Suite: "s3tests",
			Meta: map[string]string{
				"ceph_version": "19.2.6", "deselect": "999e22cc41a0", "gateway": "radosgw", "pytest_freeze": "c53be7f8edea",
				"recorded": "2026-10-12T16:55:39Z", "release": "squid", "runs": "2",
				"s3tests_commit": "f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3",
			},
			Outcomes: map[string]string{"t1": "passed", "t2": "failed", "t3": "error", "t4": "skipped"},
			Unstable: []string{"t3"},
		}
	})

	It("round-trips a flat baseline through a ledger file", func() {
		l, err := parity.Migrate(flat)
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Pins).To(Equal([]parity.Pin{{Source: "f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3", Admitted: parity.AdmitBase}}))
		data, err := parity.MarshalLedger(l)
		Expect(err).NotTo(HaveOccurred())
		read, err := parity.ParseLedger(data)
		Expect(err).NotTo(HaveOccurred())
		Expect(read).To(Equal(l))

		back := parity.Result{Suite: read.Suite, Meta: map[string]string{}, Outcomes: map[string]string{}, Unstable: []string{}}
		for _, id := range slices.Sorted(maps.Keys(read.Tests)) {
			entries := read.Tests[id]
			Expect(entries).To(HaveLen(1))
			e := entries[0]
			back.Outcomes[id] = e.Outcome
			if e.Unstable {
				back.Unstable = append(back.Unstable, id)
			}
			meta := maps.Clone(e.Meta)
			meta[read.SourceKey] = e.Source
			meta["release"], meta["ceph_version"], meta["recorded"] = read.Release, e.CephVersion, e.Recorded
			meta["runs"] = strconv.Itoa(e.Runs)
			Expect(meta).To(Equal(flat.Meta), "test %s", id)
			back.Meta = meta
		}
		Expect(back).To(Equal(flat))
	})

	It("refuses a flat baseline that lacks a meta key", func() {
		delete(flat.Meta, "recorded")
		_, err := parity.Migrate(flat)
		Expect(err).To(MatchError(ContainSubstring("no recorded")))
	})

	It("is refused by ParseLedger, which reads only ledgers", func() {
		_, err := parity.ParseLedger([]byte(`{"suite": "s3tests", "meta": {}, "outcomes": {"t1": "passed"}, "unstable": []}`))
		Expect(err).To(MatchError(parity.ErrFlatBaseline))
	})
})

var _ = Describe("the ledger file", func() {
	It("holds each pin and each entry on a line of its own, tests in order", func() {
		l := ledgerOf(map[string][]parity.Entry{
			"t2": {entry("aaa", "passed")},
			"t1": {entry("aaa", "passed"), reasoned(entry("aaa", "failed"), "radosgw -> 400 & <Error>")},
		})
		data, err := parity.MarshalLedger(l)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(`{
  "suite": "s3tests",
  "release": "squid",
  "source_key": "s3tests_commit",
  "pins": [
    {"source":"aaa","admitted":"base"}
  ],
  "tests": {
    "t1": [
      {"outcome":"passed","source":"aaa","ceph_version":"19.2.6","recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false},
      {"outcome":"failed","source":"aaa","ceph_version":"19.2.6","recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false,"reason":"radosgw -> 400 & <Error>"}
    ],
    "t2": [
      {"outcome":"passed","source":"aaa","ceph_version":"19.2.6","recorded":"2026-10-12T16:55:39Z","runs":2,"unstable":false}
    ]
  }
}
`))
	})

	It("refuses a field the schema does not hold", func() {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed")}})
		data, err := parity.MarshalLedger(l)
		Expect(err).NotTo(HaveOccurred())
		_, err = parity.ParseLedger(bytes.Replace(data, []byte(`"runs":2`), []byte(`"runs":2,"flaky":true`), 1))
		Expect(err).To(MatchError(ContainSubstring("flaky")))
	})
})

var _ = Describe("ParseReasons", func() {
	It("reads a test id and the rest of the line as its reason, skipping comments and blank lines", func() {
		reasons, err := parity.ParseReasons(strings.NewReader(
			"# flips of the 19.2.7 record\n\nt1 radosgw answers 400 (tracker #123)\n  t2\tthe suite's fix landed  \n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(reasons).To(Equal(map[string]string{"t1": "radosgw answers 400 (tracker #123)", "t2": "the suite's fix landed"}))
	})

	It("refuses a test with no reason", func() {
		_, err := parity.ParseReasons(strings.NewReader("t1 ok\nt2\n"))
		Expect(err).To(MatchError(ContainSubstring("line 2: t2 has no reason")))
	})

	It("refuses a test given twice", func() {
		_, err := parity.ParseReasons(strings.NewReader("t1 one\nt1 two\n"))
		Expect(err).To(MatchError(ContainSubstring("line 2: t1 has a reason already")))
	})

	It("refuses a reason that starts as a draft's", func() {
		_, err := parity.ParseReasons(strings.NewReader("t1 one\nt2 unreviewed: later\n"))
		Expect(err).To(MatchError(ContainSubstring(`line 2: t2's reason starts with "unreviewed:", which marks a draft`)))
	})
})

var _ = Describe("a base pin in a ledger", func() {
	It("after the first, needs a reason", func() {
		l := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed"), entry("bbb", "passed")}})
		l.Pins = append(l.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitBase})
		Expect(parity.Validate(l)).To(ContainElement("pin 2 (bbb, base) needs a reason: it re-stamps outcomes at an unproven source"))
	})

	Describe("recorded at a new source", func() {
		var ledger string
		// atBBB returns record's arguments for a full record at bbb.
		atBBB := func(flags ...string) []string {
			a := ledgerArgs(ledger, flags, "testdata/run1.xml", "testdata/run1.xml")
			for i, s := range a {
				if s == "s3tests_commit=aaa" {
					a[i] = "s3tests_commit=bbb"
				}
			}
			return a
		}

		BeforeEach(func(ctx SpecContext) {
			ledger = filepath.Join(GinkgoT().TempDir(), "l.json")
			appendLedger(ctx, ledger, nil, "testdata/run1.xml", "testdata/run1.xml")
		})

		It("is appended only with --pin-reason", func(ctx SpecContext) {
			status, _, stderr := run(ctx, atBBB()...)
			Expect(status).To(Equal(1))
			Expect(stderr).To(ContainSubstring("a full record at bbb appends a base pin, which needs --pin-reason"))
			data, err := os.ReadFile(ledger)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).NotTo(ContainSubstring(`"bbb"`))
		})

		It("carries --pin-reason, which record prints and check --list-reasoned lists", func(ctx SpecContext) {
			old := filepath.Join(GinkgoT().TempDir(), "old.json")
			data, err := os.ReadFile(ledger)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(old, data, 0o600)).To(Succeed())
			status, stdout, stderr := run(ctx, atBBB("--pin-reason", "the pin moved past a rewritten commit")...)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("pinned bbb, admitted base: the pin moved past a rewritten commit\n"))
			status, stdout, stderr = run(ctx, "check", "--old", old, "--new", ledger, "--list-reasoned")
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(HavePrefix("pin 2 (bbb, base): the pin moved past a rewritten commit\n"))
		})

		It("refuses --pin-reason without --ledger", func(ctx SpecContext) {
			out := filepath.Join(GinkgoT().TempDir(), "r.json")
			status, _, stderr := run(ctx, "record", "--format", "junit", "--suite", "s3tests", "--out", out,
				"--pin-reason", "moved", "testdata/run1.xml")
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring("--subset, --reasons, --draft and --pin-reason are for --ledger"))
		})
	})

	It("never has its reason changed", func() {
		old := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed"), entry("bbb", "passed")}})
		old.Pins = append(old.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitBase, Reason: "moved"})
		next := ledgerOf(map[string][]parity.Entry{"t1": {entry("aaa", "passed"), entry("bbb", "passed")}})
		next.Pins = append(next.Pins, parity.Pin{Source: "bbb", Admitted: parity.AdmitBase, Reason: "moved on"})
		Expect(parity.CheckAppend(old, next, false)).To(ContainElement("pin 2 (bbb, base): the reason changed; a pin is never changed"))
	})
})
