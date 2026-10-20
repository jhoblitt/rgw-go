package objectlock_test

import (
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
)

var _ = Describe("RetentionPeriodValid", func() {
	DescribeTable("is retention_period_valid: exactly one of days and years positive",
		func(r meta.DefaultRetention, valid bool) {
			Expect(objectlock.RetentionPeriodValid(r)).To(Equal(valid))
		},
		Entry("days alone", meta.DefaultRetention{Days: 1}, true),
		Entry("years alone", meta.DefaultRetention{Years: 1}, true),
		Entry("both", meta.DefaultRetention{Days: 1, Years: 1}, false),
		Entry("neither", meta.DefaultRetention{}, false),
		Entry("negative days and positive years", meta.DefaultRetention{Days: -1, Years: 1}, true),
		Entry("negative days alone", meta.DefaultRetention{Days: -1}, false),
	)
})

// mtime is a write's stamp with a fraction, which LockUntil keeps.
var mtime = time.Date(2026, 10, 20, 12, 0, 0, 123, time.UTC)

var _ = Describe("LockUntil", func() {
	DescribeTable("is the mtime plus the period, std::chrono's 86400-second days or 365.2425-day years",
		func(r meta.DefaultRetention, from, want time.Time) {
			Expect(objectlock.LockUntil(r, from)).To(Equal(want))
		},
		Entry("days", meta.DefaultRetention{Mode: "GOVERNANCE", Days: 30}, mtime, mtime.Add(30*24*time.Hour)),
		Entry("days before years, when both are set", meta.DefaultRetention{Days: 1, Years: 1}, mtime, mtime.Add(24*time.Hour)),
		Entry("a year of 31,556,952 seconds", meta.DefaultRetention{Years: 1}, mtime, mtime.Add(31556952*time.Second)),
		Entry("four years, 1460.97 days and not a calendar's 1461", meta.DefaultRetention{Years: 4},
			mtime, mtime.Add(4*31556952*time.Second)),
		Entry("years when days are not positive", meta.DefaultRetention{Days: -5, Years: 2}, mtime, mtime.Add(2*31556952*time.Second)),
		Entry("no period, the mtime itself", meta.DefaultRetention{}, mtime, mtime),
		Entry("the zero mtime, the epoch as denc reads an unset real_time", meta.DefaultRetention{Days: 1}, time.Time{}, time.Unix(86400, 0).UTC()),
		Entry("a period that reaches real_time's last instant exactly", meta.DefaultRetention{Days: 1},
			meta.MaxRealTime.Add(-24*time.Hour), meta.MaxRealTime),
		Entry("one nanosecond past it, clamped to it", meta.DefaultRetention{Days: 1},
			meta.MaxRealTime.Add(-24*time.Hour+time.Nanosecond), meta.MaxRealTime),
		Entry("600 years, clamped where radosgw wraps to 2042", meta.DefaultRetention{Years: 600}, mtime, meta.MaxRealTime),
		Entry("int's largest days, clamped", meta.DefaultRetention{Days: 2147483647}, mtime, meta.MaxRealTime),
		Entry("int's largest years, clamped", meta.DefaultRetention{Years: 2147483647}, mtime, meta.MaxRealTime),
		Entry("a negative period before the epoch, at the epoch", meta.DefaultRetention{Years: -100}, mtime, time.Unix(0, 0).UTC()),
	)
})

// periodFrom is the PeriodFits clock, midnight on 2026-10-20.
var periodFrom = time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)

var _ = Describe("PeriodFits", func() {
	DescribeTable("tells whether the period from now stays inside real_time's range",
		func(r meta.DefaultRetention, from time.Time, fits bool) {
			Expect(objectlock.PeriodFits(r, from)).To(Equal(fits))
		},
		Entry("100 years", meta.DefaultRetention{Years: 100}, periodFrom, true),
		Entry("527 years, the last that fits from 2026-10-20", meta.DefaultRetention{Years: 527}, periodFrom, true),
		Entry("528 years, which radosgw wraps to 1970", meta.DefaultRetention{Years: 528}, periodFrom, false),
		Entry("192757 days, the last that fits", meta.DefaultRetention{Days: 192757}, periodFrom, true),
		Entry("192758 days", meta.DefaultRetention{Days: 192758}, periodFrom, false),
		Entry("a period ending on the last instant", meta.DefaultRetention{Days: 1}, meta.MaxRealTime.Add(-24*time.Hour), true),
		Entry("a period ending a nanosecond past it", meta.DefaultRetention{Days: 1}, meta.MaxRealTime.Add(-24*time.Hour+time.Nanosecond), false),
	)
})

// retentionAttr is RGWObjectRetention's encoding of mode until t.
func retentionAttr(mode string, t time.Time) []byte {
	e := denc.NewEncoder()
	meta.ObjectRetention{Mode: mode, RetainUntil: t}.Encode(e, denc.Squid)
	return e.Bytes()
}

// legalHoldAttr is RGWObjectLegalHold's encoding of status.
func legalHoldAttr(status string) []byte {
	e := denc.NewEncoder()
	meta.ObjectLegalHold{Status: status}.Encode(e, denc.Squid)
	return e.Bytes()
}

// undecodable is an attr whose struct claims 30 bytes and holds one.
func undecodable() []byte { return []byte{2, 1, 30, 0, 0, 0, 10} }

// now is the evaluation clock, part way through a second.
var now = time.Date(2026, 10, 20, 12, 0, 0, 500_000_000, time.UTC)

// The retain-until dates the VerifyDelete matrix tries, by how their whole
// second relates to now's.
var untilByRel = map[string]time.Time{
	"a second in the past":      now.Add(-time.Second),
	"now's own second":          now.Add(400 * time.Millisecond),
	"the next second":           now.Add(600 * time.Millisecond),
	"a year in the future":      now.AddDate(1, 0, 0),
	"real_time's last instant":  meta.MaxRealTime,
	"the unset date, the epoch": {},
}

var futureRel = map[string]bool{"the next second": true, "a year in the future": true, "real_time's last instant": true}

// bypassable is, for each stored mode the VerifyDelete matrix tries, whether
// a delete with both bypass flags passes a retention of it still in force:
// only for GOVERNANCE exactly, which verify_object_lock compares
// case-sensitively. A bucket instance's metadata put stores a rule's mode
// unchecked, so any string can become an object's retention mode.
var bypassable = map[string]bool{"GOVERNANCE": true, "COMPLIANCE": false, "governance": false, "": false}

// verifyDeleteMatrix is every combination of stored mode, bypass header,
// bypass permission, retain-until second and legal hold, with the answer
// verify_object_lock gives: a retention whose second is after now's locks
// unless its mode is bypassable and both bypass flags are set, and a legal
// hold of ON locks.
func verifyDeleteMatrix() []TableEntry {
	var entries []TableEntry
	holds := []string{"", "OFF", "ON", "on"}
	for mode, canBypass := range bypassable {
		for rel, until := range untilByRel {
			for _, header := range []bool{false, true} {
				for _, allowed := range []bool{false, true} {
					for _, hold := range holds {
						bypassed := canBypass && header && allowed
						retLocked := futureRel[rel] && !bypassed
						locked := retLocked || hold == "ON"
						name := fmt.Sprintf("mode %q until %s, header %t, permission %t, legal hold %q", mode, rel, header, allowed, hold)
						entries = append(entries, Entry(name, true, mode, until, header, allowed, hold, locked))
					}
				}
			}
		}
	}
	for _, hold := range holds {
		for _, header := range []bool{false, true} {
			entries = append(entries, Entry(fmt.Sprintf("no retention, header %t, legal hold %q", header, hold),
				false, "", time.Time{}, header, true, hold, hold == "ON"))
		}
	}
	return entries
}

var _ = Describe("VerifyDelete", func() {
	DescribeTable("is verify_object_lock",
		func(retained bool, mode string, until time.Time, header, allowed bool, hold string, locked bool) {
			attrs := map[string][]byte{meta.AttrETag: []byte("x")}
			if retained {
				attrs[meta.AttrObjectRetention] = retentionAttr(mode, until)
			}
			if hold != "" {
				attrs[meta.AttrObjectLegalHold] = legalHoldAttr(hold)
			}
			err := objectlock.VerifyDelete(attrs, now, header, allowed)
			if locked {
				Expect(err).To(MatchError(objectlock.ErrLocked))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		verifyDeleteMatrix(),
	)
	DescribeTable("checks in verify_object_lock's order, an attr that does not decode being the undecodable error",
		func(attrs map[string][]byte, header, allowed bool, want error) {
			Expect(objectlock.VerifyDelete(attrs, now, header, allowed)).To(MatchError(want))
		},
		Entry("a retention that does not decode, whatever the legal hold", map[string][]byte{
			meta.AttrObjectRetention: undecodable(), meta.AttrObjectLegalHold: legalHoldAttr("ON"),
		}, true, true, objectlock.ErrUndecodable),
		Entry("an empty retention attr", map[string][]byte{meta.AttrObjectRetention: {}}, false, false, objectlock.ErrUndecodable),
		Entry("a retention with bytes past its encoding, which radosgw's decode asserts on", map[string][]byte{
			meta.AttrObjectRetention: append(retentionAttr("GOVERNANCE", now.Add(-time.Hour)), 0),
		}, false, false, objectlock.ErrUndecodable),
		Entry("a locking retention before a legal hold that does not decode", map[string][]byte{
			meta.AttrObjectRetention: retentionAttr("COMPLIANCE", now.Add(time.Hour)), meta.AttrObjectLegalHold: undecodable(),
		}, true, true, objectlock.ErrLocked),
		Entry("a bypassed retention, then a legal hold that does not decode", map[string][]byte{
			meta.AttrObjectRetention: retentionAttr("GOVERNANCE", now.Add(time.Hour)), meta.AttrObjectLegalHold: {1, 1, 9, 0, 0, 0},
		}, true, true, objectlock.ErrUndecodable),
		Entry("a lapsed retention, then a legal hold that does not decode", map[string][]byte{
			meta.AttrObjectRetention: retentionAttr("COMPLIANCE", now.Add(-time.Hour)), meta.AttrObjectLegalHold: undecodable(),
		}, false, false, objectlock.ErrUndecodable),
		Entry("a legal hold with bytes past its encoding", map[string][]byte{
			meta.AttrObjectLegalHold: append(legalHoldAttr("OFF"), 'x'),
		}, false, false, objectlock.ErrUndecodable),
	)
	It("reads a version 1 retention, utime_t's date alone", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("COMPLIANCE")
		e.Time(now.Add(time.Hour))
		e.EndStruct(f)
		Expect(objectlock.VerifyDelete(map[string][]byte{meta.AttrObjectRetention: e.Bytes()}, now, true, true)).To(MatchError(objectlock.ErrLocked))
	})
	It("passes a version with no lock attrs", func() {
		Expect(objectlock.VerifyDelete(nil, now, false, false)).To(Succeed())
	})
})

// inForce is a retain-until date an hour after now.
func inForce() time.Time { return now.Add(time.Hour) }

var _ = Describe("the attr decodes", func() {
	It("decode an attr whole, strict and lenient alike", func() {
		want := meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: inForce()}
		Expect(objectlock.DecodeRetention(retentionAttr("GOVERNANCE", inForce()))).To(Equal(want))
		Expect(objectlock.DecodeRetentionLenient(retentionAttr("GOVERNANCE", inForce()))).To(Equal(want))
		Expect(objectlock.DecodeLegalHold(legalHoldAttr("ON"))).To(Equal(meta.ObjectLegalHold{Status: "ON"}))
		Expect(objectlock.DecodeLegalHoldLenient(legalHoldAttr("ON"))).To(Equal(meta.ObjectLegalHold{Status: "ON"}))
	})
	It("refuse a short attr as undecodable, strict and lenient alike", func() {
		_, err := objectlock.DecodeRetention([]byte{2, 1})
		Expect(err).To(MatchError(objectlock.ErrUndecodable))
		_, err = objectlock.DecodeRetentionLenient([]byte{2, 1})
		Expect(err).To(MatchError(objectlock.ErrUndecodable))
		_, err = objectlock.DecodeLegalHold(undecodable())
		Expect(err).To(MatchError(objectlock.ErrUndecodable))
		_, err = objectlock.DecodeLegalHoldLenient(undecodable())
		Expect(err).To(MatchError(objectlock.ErrUndecodable))
	})
	It("refuse bytes past the encoding in the strict decode, which the full-buffer decode asserts on", func() {
		_, err := objectlock.DecodeRetention(append(retentionAttr("GOVERNANCE", inForce()), 1))
		Expect(err).To(MatchError(objectlock.ErrUndecodable))
		_, err = objectlock.DecodeLegalHold(append(legalHoldAttr("ON"), 1))
		Expect(err).To(MatchError(objectlock.ErrUndecodable))
	})
	It("ignore bytes past the encoding in the lenient decode, as an iterator decode does", func() {
		Expect(objectlock.DecodeRetentionLenient(append(retentionAttr("COMPLIANCE", inForce()), 1, 2))).To(
			Equal(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: inForce()}))
		Expect(objectlock.DecodeLegalHoldLenient(append(legalHoldAttr("OFF"), 'x'))).To(Equal(meta.ObjectLegalHold{Status: "OFF"}))
	})
})

const (
	msgShortens       = "proposed retain-until date shortens an existing retention period and governance bypass check failed"
	msgToGovernance   = "can't change retention mode from COMPLIANCE to GOVERNANCE"
	msgFromGovernance = "can't change retention mode from GOVERNANCE without governance bypass"
)

// expectDenied asserts err is access-denied with message msg, or nil when
// msg is empty.
func expectDenied(err error, msg string) {
	GinkgoHelper()
	if msg == "" {
		Expect(err).NotTo(HaveOccurred())
		return
	}
	Expect(err).To(MatchError(objectlock.ErrAccessDenied))
	var e *objectlock.Error
	Expect(errors.As(err, &e)).To(BeTrue())
	Expect(e.Message).To(Equal(msg))
}

// old is an existing retention's date, an hour after now; earlier, same
// and later are a proposed date before its second, in it, and after it.
var (
	old     = now.Add(time.Hour)
	earlier = old.Add(-time.Second)
	same    = old.Add(-400 * time.Millisecond)
	later   = old.Add(time.Hour)
)

var _ = Describe("RetentionChange", func() {
	DescribeTable("is PutObjectRetention's check of an existing retention, in whole seconds",
		func(oldMode string, proposed time.Time, newMode string, bypass bool, msg string) {
			expectDenied(objectlock.RetentionChange(&meta.ObjectRetention{Mode: oldMode, RetainUntil: old},
				meta.ObjectRetention{Mode: newMode, RetainUntil: proposed}, now, bypass), msg)
		},
		Entry("GOVERNANCE shortened, no bypass", "GOVERNANCE", earlier, "GOVERNANCE", false, msgShortens),
		Entry("GOVERNANCE shortened with the bypass", "GOVERNANCE", earlier, "GOVERNANCE", true, ""),
		Entry("GOVERNANCE shortened to COMPLIANCE, no bypass", "GOVERNANCE", earlier, "COMPLIANCE", false, msgShortens),
		Entry("GOVERNANCE shortened to COMPLIANCE with the bypass, the mode never checked", "GOVERNANCE", earlier, "COMPLIANCE", true, ""),
		Entry("GOVERNANCE kept in the same second, no bypass", "GOVERNANCE", same, "GOVERNANCE", false, ""),
		Entry("GOVERNANCE kept in the same second with the bypass", "GOVERNANCE", same, "GOVERNANCE", true, ""),
		Entry("GOVERNANCE to COMPLIANCE in the same second, no bypass", "GOVERNANCE", same, "COMPLIANCE", false, msgFromGovernance),
		Entry("GOVERNANCE to COMPLIANCE in the same second with the bypass", "GOVERNANCE", same, "COMPLIANCE", true, ""),
		Entry("GOVERNANCE extended, no bypass", "GOVERNANCE", later, "GOVERNANCE", false, ""),
		Entry("GOVERNANCE extended with the bypass", "GOVERNANCE", later, "GOVERNANCE", true, ""),
		Entry("GOVERNANCE extended to COMPLIANCE, no bypass", "GOVERNANCE", later, "COMPLIANCE", false, msgFromGovernance),
		Entry("GOVERNANCE extended to COMPLIANCE with the bypass", "GOVERNANCE", later, "COMPLIANCE", true, ""),
		Entry("COMPLIANCE shortened, no bypass", "COMPLIANCE", earlier, "COMPLIANCE", false, msgShortens),
		Entry("COMPLIANCE shortened with the bypass", "COMPLIANCE", earlier, "COMPLIANCE", true, msgShortens),
		Entry("COMPLIANCE shortened to GOVERNANCE, no bypass", "COMPLIANCE", earlier, "GOVERNANCE", false, msgShortens),
		Entry("COMPLIANCE shortened to GOVERNANCE with the bypass", "COMPLIANCE", earlier, "GOVERNANCE", true, msgShortens),
		Entry("COMPLIANCE kept in the same second, no bypass", "COMPLIANCE", same, "COMPLIANCE", false, ""),
		Entry("COMPLIANCE kept in the same second with the bypass", "COMPLIANCE", same, "COMPLIANCE", true, ""),
		Entry("COMPLIANCE to GOVERNANCE in the same second, no bypass", "COMPLIANCE", same, "GOVERNANCE", false, msgToGovernance),
		Entry("COMPLIANCE to GOVERNANCE in the same second with the bypass", "COMPLIANCE", same, "GOVERNANCE", true, msgToGovernance),
		Entry("COMPLIANCE extended, no bypass", "COMPLIANCE", later, "COMPLIANCE", false, ""),
		Entry("COMPLIANCE extended with the bypass", "COMPLIANCE", later, "COMPLIANCE", true, ""),
		Entry("COMPLIANCE extended to GOVERNANCE, no bypass", "COMPLIANCE", later, "GOVERNANCE", false, msgToGovernance),
		Entry("COMPLIANCE extended to GOVERNANCE with the bypass", "COMPLIANCE", later, "GOVERNANCE", true, msgToGovernance),
	)
	DescribeTable("passes when there is no existing retention, or it has lapsed",
		func(prev *meta.ObjectRetention) {
			expectDenied(objectlock.RetentionChange(prev, meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: now.Add(time.Minute)}, now, false), "")
		},
		Entry("no existing retention", (*meta.ObjectRetention)(nil)),
		Entry("a lapsed COMPLIANCE retention replaced by a shorter GOVERNANCE one", &meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: now.Add(-time.Hour)}),
		Entry("a COMPLIANCE retention whose second is now's", &meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: now.Add(400 * time.Millisecond)}),
		Entry("the unset date, the epoch", &meta.ObjectRetention{Mode: "COMPLIANCE"}),
	)
	It("holds a COMPLIANCE retention whose second is the next one", func() {
		prev := &meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: now.Add(600 * time.Millisecond)}
		expectDenied(objectlock.RetentionChange(prev, meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: now.Add(time.Hour)}, now, true), msgToGovernance)
	})
	It("compares a stored mode that is neither as radosgw does", func() {
		prev := &meta.ObjectRetention{Mode: "", RetainUntil: old}
		expectDenied(objectlock.RetentionChange(prev, meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: later}, now, true), msgToGovernance)
		expectDenied(objectlock.RetentionChange(prev, meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: later}, now, false), msgFromGovernance)
		expectDenied(objectlock.RetentionChange(prev, meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: earlier}, now, true), msgShortens)
	})
})
