package lifecycle_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/lifecycle/lifecyclefakes"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// now is the instant every Decide spec runs at, in UTC unless a spec says
// otherwise.
var now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }

// opOf is r's Op as the prefix map makes it, enabled unless r says
// otherwise.
func opOf(r lifecycle.Rule) lifecycle.Op {
	GinkgoHelper()
	if r.Status == "" {
		r.Status = "Enabled"
	}
	m := lifecycle.Configuration{Rules: []lifecycle.Rule{r}}.PrefixMap()
	Expect(m).To(HaveLen(1))
	return m[0].Op
}

func bucket(flags uint32) *meta.BucketInfo { return &meta.BucketInfo{Flags: flags} }

// The bucket flags of each versioning state the specs run under.
const (
	versioningOn = meta.BucketVersioned
	suspended    = meta.BucketVersioned | meta.BucketVersionsSuspended
	locking      = meta.BucketObjLockEnabled
)

func key(name, instance string) meta.ObjKey { return meta.ObjKey{Name: name, Instance: instance} }

func cur(name, instance string, mtime time.Time) lifecycle.Entry {
	return lifecycle.Entry{Key: key(name, instance), Current: true, Mtime: mtime}
}

func noncur(name, instance string, mtime time.Time) lifecycle.Entry {
	return lifecycle.Entry{Key: key(name, instance), Mtime: mtime}
}

func marker(e lifecycle.Entry) lifecycle.Entry {
	e.DeleteMarker = true
	return e
}

func inClass(e lifecycle.Entry, class string) lifecycle.Entry {
	e.StorageClass = class
	return e
}

type page struct {
	entries   []lifecycle.Entry
	truncated bool
}

func last(es ...lifecycle.Entry) page { return page{entries: es} }

// result is one entry's decision.
type result struct {
	key meta.ObjKey
	d   lifecycle.Decision
	err error
}

// walk lists pages through one Lister and decides each entry at clock,
// returning every entry's result in listing order.
func walk(op lifecycle.Op, b *meta.BucketInfo, clock lifecycle.Clock, probe lifecycle.Probe, pages ...page) []result {
	GinkgoHelper()
	var l lifecycle.Lister
	var out []result
	for _, p := range pages {
		states := l.Page(p.entries, p.truncated)
		Expect(states).To(HaveLen(len(p.entries)))
		for i, e := range p.entries {
			d, err := lifecycle.Decide(context.Background(), &op, b, e, states[i], clock, probe)
			out = append(out, result{e.Key, d, err})
		}
	}
	return out
}

func utcClock() lifecycle.Clock { return lifecycle.Clock{Now: now, Loc: time.UTC} }

// acted maps each listed key whose decision has an effect to it.
func acted(rs []result) map[meta.ObjKey]lifecycle.Decision {
	GinkgoHelper()
	m := map[meta.ObjKey]lifecycle.Decision{}
	for _, r := range rs {
		Expect(r.err).NotTo(HaveOccurred(), "%v", r.key)
		if r.d.Effect != lifecycle.EffectNone {
			m[r.key] = r.d
		}
	}
	return m
}

func names(m map[meta.ObjKey]lifecycle.Decision) []string {
	var out []string
	for k := range m {
		out = append(out, k.Name+"@"+k.Instance)
	}
	return out
}

func tagAttr(ts ...tags.Tag) []byte {
	return encode(tags.Set{Tags: ts}, tags.Set.Encode, denc.Squid)
}

func errLocked() error { return &objectlock.Error{Err: objectlock.ErrLocked, Detail: "legal hold ON"} }

// fourVersions is a key's current version and three noncurrent ones, each
// older than the last.
func fourVersions() []lifecycle.Entry {
	return []lifecycle.Entry{
		cur("k", "v4", daysAgo(10)), noncur("k", "v3", daysAgo(20)), noncur("k", "v2", daysAgo(30)), noncur("k", "v1", daysAgo(40)),
	}
}

// sizes makes probe answer each name's size from m.
func sizes(probe *lifecyclefakes.FakeProbe, m map[string]uint64) {
	probe.SizeCalls(func(_ context.Context, k meta.ObjKey) (uint64, error) { return m[k.Name], nil })
}

func expiration(days string) lifecycle.Rule {
	return lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: days}}
}

func decision(a lifecycle.Action, e lifecycle.Effect, k meta.ObjKey) lifecycle.Decision {
	return lifecycle.Decision{Action: a, Effect: e, Key: k}
}

func transition(a lifecycle.Action, k meta.ObjKey, class string) lifecycle.Decision {
	return lifecycle.Decision{Action: a, Effect: lifecycle.EffectTransition, Key: k, StorageClass: class}
}

var _ = Describe("Lister", func() {
	It("hands each entry the next name, the earlier entries of its name and the previous mtime, across pages", func() {
		var l lifecycle.Lister
		t1, t2, t3, t4 := daysAgo(1), daysAgo(2), daysAgo(3), daysAgo(4)
		first := l.Page([]lifecycle.Entry{cur("a", "", t1), cur("k", "v3", t2), noncur("k", "v2", t3)}, true)
		Expect(first).To(Equal([]lifecycle.State{
			{Next: "k", HasNext: true},
			{Next: "k", HasNext: true, PrevMtime: t1},
			{Next: "k", HasNext: true, Earlier: 1, PrevMtime: t2},
		}), "the last entry of a truncated page sees its own name")
		second := l.Page([]lifecycle.Entry{noncur("k", "v1", t4), cur("z", "", t1)}, false)
		Expect(second).To(Equal([]lifecycle.State{
			{Next: "z", HasNext: true, Earlier: 2, PrevMtime: t3},
			{PrevMtime: t4},
		}), "the count and the mtime carry over the page boundary, and the listing's last entry has no next")
	})

	It("hands nothing for an empty page, and carries the last entry past it", func() {
		var l lifecycle.Lister
		Expect(l.Page([]lifecycle.Entry{cur("k", "v2", daysAgo(1))}, true)).To(HaveLen(1))
		Expect(l.Page(nil, true)).To(BeEmpty())
		Expect(l.Page([]lifecycle.Entry{noncur("k", "v1", daysAgo(2))}, false)).To(Equal([]lifecycle.State{
			{Earlier: 1, PrevMtime: daysAgo(1)},
		}))
	})
})

var _ = Describe("Op.Applies", func() {
	DescribeTable("is is_valid_op and zone_check",
		func(op lifecycle.Op, archiveZone, want bool) {
			Expect(op.Applies(archiveZone)).To(Equal(want))
		},
		Entry("an enabled rule with an expiration", lifecycle.Op{Enabled: true, Expiration: 1}, false, true),
		Entry("a disabled one", lifecycle.Op{Expiration: 1}, false, false),
		Entry("one whose only action aborts uploads, which is no object action", lifecycle.Op{Enabled: true, MPExpiration: 1}, false, false),
		Entry("a noncurrent expiration of 0 days", lifecycle.Op{Enabled: true}, false, false),
		Entry("a delete-marker expiration", lifecycle.Op{Enabled: true, DMExpiration: true}, false, true),
		Entry("an expiration date", lifecycle.Op{Enabled: true, ExpirationDate: &now}, false, true),
		Entry("an ArchiveZone rule outside an archive zone", lifecycle.Op{Enabled: true, Expiration: 1, Flags: lifecycle.FlagArchiveZone}, false, false),
		Entry("an ArchiveZone rule in one", lifecycle.Op{Enabled: true, Expiration: 1, Flags: lifecycle.FlagArchiveZone}, true, true),
		Entry("a plain rule in an archive zone", lifecycle.Op{Enabled: true, Expiration: 1}, true, false),
	)

	It("skips an op whose only action is a date the prefix map holds as nil", func() {
		Expect(opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Date: "2600-01-01T23:34:33.709551616Z"}}).Applies(false)).To(BeFalse())
	})

	It("takes an op with a transition, though its date is nil, as is_valid_op counts the transition", func() {
		Expect(opOf(lifecycle.Rule{ID: "r", Transitions: map[string]lifecycle.Transition{"COLD": {Date: "never", StorageClass: "COLD"}}}).Applies(false)).To(BeTrue())
	})
})

var _ = Describe("Decide", func() {
	var probe *lifecyclefakes.FakeProbe

	BeforeEach(func() {
		probe = &lifecyclefakes.FakeProbe{}
	})

	Describe("a delete marker at a page boundary", func() {
		var op lifecycle.Op
		var dm lifecycle.Entry

		BeforeEach(func() {
			op = opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: "1"}, DMExpiration: true})
			dm = marker(cur("k", "dm", daysAgo(5)))
		})

		It("keeps a current delete marker at the end of a truncated page when older versions follow on the next, on either release", func() {
			rs := walk(op, bucket(versioningOn), utcClock(), probe,
				page{[]lifecycle.Entry{cur("a", "x", daysAgo(5)), dm}, true},
				last(noncur("k", "v2", daysAgo(6)), noncur("k", "v1", daysAgo(7))),
			)
			Expect(acted(rs)).NotTo(HaveKey(dm.Key), "v20.2.4's next_key_name, on both releases")
		})

		It("removes it at the end of the listing, where nothing follows", func() {
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(cur("a", "x", daysAgo(5)), dm)))).To(
				HaveKeyWithValue(dm.Key, decision(lifecycle.ActionDeleteMarkerExpiration, lifecycle.EffectRemove, dm.Key)),
				"the delete-marker action is due now, after the expiration's due time, so it is selected")
		})

		It("removes a noncurrent delete marker no entry of its name follows, as radosgw's check has no current test", func() {
			old := marker(noncur("k", "dm", daysAgo(9)))
			Expect(acted(walk(opOf(lifecycle.Rule{ID: "r", DMExpiration: true}), bucket(versioningOn), utcClock(), probe,
				last(cur("k", "v2", daysAgo(6)), old)))).To(
				HaveKeyWithValue(old.Key, decision(lifecycle.ActionDeleteMarkerExpiration, lifecycle.EffectRemove, old.Key)))
		})

		It("keeps one followed by an entry of its name on the same page", func() {
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(dm, noncur("k", "v1", daysAgo(6)))))).NotTo(HaveKey(dm.Key))
		})

		It("removes it by the current expiration alone when the rule has no delete-marker action", func() {
			Expect(acted(walk(opOf(expiration("1")), bucket(versioningOn|locking), utcClock(), probe, last(dm)))).To(
				HaveKeyWithValue(dm.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectRemove, dm.Key)))
			Expect(probe.VerifyDeleteCallCount()).To(BeZero(), "a delete marker holds no lock")
		})
	})

	Describe("current expiration by the bucket's versioning", func() {
		It("writes a delete marker on an Enabled bucket, without the lock predicate", func() {
			e := cur("k", "v1", daysAgo(2))
			Expect(acted(walk(opOf(expiration("1")), bucket(versioningOn|locking), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectMarker, e.Key)))
			Expect(probe.VerifyDeleteCallCount()).To(BeZero(), "a delete marker over a locked version is allowed")
		})

		It("writes the null marker on a Suspended bucket, which replaces a null current version once the lock predicate passes", func() {
			e := cur("k", "", daysAgo(2))
			Expect(acted(walk(opOf(expiration("1")), bucket(suspended|locking), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectNullMarker, key("k", "null"))))
			Expect(probe.VerifyDeleteCallCount()).To(Equal(1))
			_, k := probe.VerifyDeleteArgsForCall(0)
			Expect(k).To(Equal(key("k", "null")))
		})

		It("keeps a locked null current version on a Suspended bucket", func() {
			probe.VerifyDeleteReturns(errLocked())
			Expect(acted(walk(opOf(expiration("1")), bucket(suspended|locking), utcClock(), probe, last(cur("k", "null", daysAgo(2)))))).To(BeEmpty())
		})

		It("writes the null marker over a non-null current version on a Suspended bucket, which keeps it as noncurrent", func() {
			e := cur("k", "v1", daysAgo(2))
			probe.VerifyDeleteReturns(errLocked())
			Expect(acted(walk(opOf(expiration("1")), bucket(suspended|locking), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectNullMarker, e.Key)))
			Expect(probe.VerifyDeleteCallCount()).To(BeZero(), "the version is kept, so its lock does not bind")
		})

		It("removes an object of a bucket never versioned", func() {
			e := cur("k", "", daysAgo(2))
			Expect(acted(walk(opOf(expiration("1")), bucket(0), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectRemove, e.Key)))
			Expect(probe.VerifyDeleteCallCount()).To(BeZero(), "pass_object_lock_check passes a bucket without object lock unread")
		})

		It("keeps a retained current object on a lock bucket whose flags lack BUCKET_VERSIONED", func() {
			probe.VerifyDeleteReturns(errLocked())
			e := cur("k", "", daysAgo(2))
			Expect(acted(walk(opOf(expiration("1")), bucket(locking), utcClock(), probe, last(e)))).To(BeEmpty())
			Expect(probe.VerifyDeleteCallCount()).To(Equal(1))
			_, k := probe.VerifyDeleteArgsForCall(0)
			Expect(k).To(Equal(e.Key), "the plain object, which a bucket never versioned names without an instance")
		})

		It("removes an unlocked one there", func() {
			e := cur("k", "", daysAgo(2))
			Expect(acted(walk(opOf(expiration("1")), bucket(locking), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectRemove, e.Key)))
		})

		It("leaves a noncurrent version alone", func() {
			Expect(names(acted(walk(opOf(expiration("1")), bucket(versioningOn), utcClock(), probe,
				last(cur("k", "v2", daysAgo(9)), noncur("k", "v1", daysAgo(9))))))).To(ConsistOf("k@v2"))
		})

		It("is due on its Date once now reaches it", func() {
			op := opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Date: "2026-06-15T00:00:00Z"}})
			e := cur("k", "", now)
			Expect(acted(walk(op, bucket(0), lifecycle.Clock{Now: utc("2026-06-14T23:59:59.999999999Z"), Loc: time.UTC}, probe, last(e)))).To(BeEmpty())
			Expect(acted(walk(op, bucket(0), lifecycle.Clock{Now: utc("2026-06-15T00:00:00Z"), Loc: time.UTC}, probe, last(e)))).To(HaveKey(e.Key))
		})

		It("acts on a Date at the epoch, which radosgw's selection never picks", func() {
			op := opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Date: "1970-01-01T00:00:00Z"}})
			e := cur("k", "", daysAgo(2))
			Expect(acted(walk(op, bucket(0), utcClock(), probe, last(e)))).To(HaveKey(e.Key))
		})

		It("judges an age in UTC for a Clock without a location", func() {
			Expect(names(acted(walk(opOf(expiration("1")), bucket(0), lifecycle.Clock{Now: utc("2026-06-14T00:10:00Z")}, probe,
				last(cur("old", "", utc("2026-06-13T00:00:00Z")), cur("new", "", utc("2026-06-13T00:00:01Z"))))))).To(ConsistOf("old@"))
		})

		It("counts days of the debug interval", func() {
			clock := lifecycle.Clock{Now: now, Debug: 10 * time.Second, Loc: time.UTC}
			Expect(names(acted(walk(opOf(expiration("3")), bucket(0), clock, probe,
				last(cur("old", "", now.Add(-30*time.Second)), cur("new", "", now.Add(-29*time.Second))))))).To(ConsistOf("old@"))
		})
	})

	Describe("noncurrent expiration", func() {
		DescribeTable("removes the versions past NewerNoncurrentVersions",
			func(newer string, want ...string) {
				op := opOf(lifecycle.Rule{ID: "r", Filter: lifecycle.Filter{Prefix: "k"}, NoncurExpiration: lifecycle.Expiration{Days: "1", NewerNoncurrent: newer}})
				got := acted(walk(op, bucket(versioningOn), utcClock(), probe, last(fourVersions()...)))
				for k, d := range got {
					Expect(d).To(Equal(decision(lifecycle.ActionNoncurrentExpiration, lifecycle.EffectRemove, k)))
				}
				Expect(names(got)).To(ConsistOf(want))
			},
			Entry("0, which holds back none", "0", "k@v3", "k@v2", "k@v1"),
			Entry("1", "1", "k@v2", "k@v1"),
			Entry("2", "2", "k@v1"),
		)

		It("is due by the mtime of the version after it, when it became noncurrent", func() {
			op := opOf(lifecycle.Rule{ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "5"}})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(cur("k", "v2", daysAgo(4)), noncur("k", "v1", daysAgo(400)))))).To(BeEmpty())
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(cur("k", "v2", daysAgo(6)), noncur("k", "v1", daysAgo(400)))))).To(HaveKey(key("k", "v1")))
		})

		It("runs the lock predicate on the version, named null when the index holds no instance", func() {
			probe.VerifyDeleteReturns(errLocked())
			op := opOf(lifecycle.Rule{ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "1"}})
			Expect(acted(walk(op, bucket(versioningOn|locking), utcClock(), probe, last(cur("k", "v2", daysAgo(6)), noncur("k", "", daysAgo(9)))))).To(BeEmpty())
			Expect(probe.VerifyDeleteCallCount()).To(Equal(1))
			_, k := probe.VerifyDeleteArgsForCall(0)
			Expect(k).To(Equal(key("k", "null")))
		})

		It("removes a noncurrent delete marker without the lock predicate", func() {
			op := opOf(lifecycle.Rule{ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "1"}})
			dm := marker(noncur("k", "dm", daysAgo(9)))
			Expect(acted(walk(op, bucket(versioningOn|locking), utcClock(), probe, last(cur("k", "v2", daysAgo(6)), dm)))).To(
				HaveKeyWithValue(dm.Key, decision(lifecycle.ActionNoncurrentExpiration, lifecycle.EffectRemove, dm.Key)))
			Expect(probe.VerifyDeleteCallCount()).To(BeZero())
		})
	})

	Describe("the size filter", func() {
		DescribeTable("is size > GT and size < LT, from the version's size",
			func(gt, lt string, want ...string) {
				sizes(probe, map[string]uint64{"s99": 99, "s100": 100, "s101": 101})
				op := opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{SizeGT: gt, SizeLT: lt}})
				Expect(names(acted(walk(op, bucket(0), utcClock(), probe,
					last(cur("s99", "", daysAgo(2)), cur("s100", "", daysAgo(2)), cur("s101", "", daysAgo(2))))))).To(ConsistOf(want))
			},
			Entry("GT 100", "100", "", "s101@"),
			Entry("LT 100", "", "100", "s99@"),
			Entry("both", "99", "101", "s100@"),
			Entry("a stored LT of -1, which matches no object", "", "-1"),
			Entry("a stored GT of -1, which matches every object", "-1", "", "s99@", "s100@", "s101@"),
		)

		It("reads an empty-instance noncurrent version's own size, by the null instance", func() {
			sizes(probe, map[string]uint64{"k": 10})
			op := opOf(lifecycle.Rule{ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{SizeGT: "5"}})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(cur("k", "v2", daysAgo(6)), noncur("k", "", daysAgo(9)))))).To(HaveKey(key("k", "")))
			Expect(probe.SizeCallCount()).To(Equal(1))
			_, k := probe.SizeArgsForCall(0)
			Expect(k).To(Equal(key("k", "null")), "radosgw stats the listed key, which follows the OLH to the current version")
		})

		It("stats nothing for an entry no action is due on, and once for one with several", func() {
			op := opOf(lifecycle.Rule{
				ID: "r", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{SizeGT: "0"},
				Transitions: map[string]lifecycle.Transition{"COLD": {Days: "0", StorageClass: "COLD"}},
			})
			sizes(probe, map[string]uint64{"old": 1})
			Expect(names(acted(walk(op, bucket(0), utcClock(), probe, last(cur("old", "", daysAgo(2)), cur("today", "", now)))))).To(ConsistOf("old@"))
			Expect(probe.SizeCallCount()).To(Equal(1))
		})

		It("reads a delete marker's size as 0, unread", func() {
			op := opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{SizeLT: "1"}})
			dm := marker(cur("k", "dm", daysAgo(2)))
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(dm)))).To(HaveKey(dm.Key))
			Expect(probe.SizeCallCount()).To(BeZero())
		})

		It("skips an object whose size cannot be read, with the error", func() {
			probe.SizeReturns(0, errors.New("EIO"))
			op := opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{SizeGT: "0"}})
			rs := walk(op, bucket(0), utcClock(), probe, last(cur("k", "", daysAgo(2))))
			Expect(rs).To(HaveLen(1))
			Expect(rs[0].err).To(MatchError(ContainSubstring("EIO")))
			Expect(rs[0].d).To(Equal(lifecycle.Decision{}))
		})
	})

	Describe("the tag filter", func() {
		var op lifecycle.Op

		BeforeEach(func() {
			op = opOf(lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: "1"}, Filter: lifecycle.Filter{
				Tags: tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}, {Key: "z", Value: "1"}}},
			}})
		})

		DescribeTable("passes an object holding every tag of the rule",
			func(attr []byte, want lifecycle.Effect) {
				probe.TagsReturns(attr, nil)
				rs := walk(op, bucket(0), utcClock(), probe, last(cur("o", "", daysAgo(2))))
				Expect(rs).To(HaveLen(1))
				Expect(rs[0].err).NotTo(HaveOccurred())
				Expect(rs[0].d.Effect).To(Equal(want))
				_, k := probe.TagsArgsForCall(0)
				Expect(k).To(Equal(key("o", "")))
			},
			Entry("every tag and more", tagAttr(tags.Tag{Key: "a", Value: "b"}, tags.Tag{Key: "k", Value: "v"}, tags.Tag{Key: "z", Value: "1"}), lifecycle.EffectRemove),
			Entry("a tag missing", tagAttr(tags.Tag{Key: "k", Value: "v"}), lifecycle.EffectNone),
			Entry("a value that differs", tagAttr(tags.Tag{Key: "k", Value: "w"}, tags.Tag{Key: "z", Value: "1"}), lifecycle.EffectNone),
			Entry("no tags, a skip", nil, lifecycle.EffectNone),
			Entry("a repeated key standing in for a missing tag, which radosgw's count takes",
				tagAttr(tags.Tag{Key: "k", Value: "v"}, tags.Tag{Key: "k", Value: "v"}), lifecycle.EffectNone),
			Entry("the URL-encoded text older objects store", []byte("k=v&z=1"), lifecycle.EffectRemove),
		)

		It("skips an object whose tags do not decode, with the error", func() {
			probe.TagsReturns([]byte{0}, nil)
			rs := walk(op, bucket(0), utcClock(), probe, last(cur("o", "", daysAgo(2))))
			Expect(rs[0].err).To(HaveOccurred())
			Expect(rs[0].d).To(Equal(lifecycle.Decision{}))
		})

		It("skips an object whose tags cannot be read, with the error", func() {
			probe.TagsReturns(nil, errors.New("EIO"))
			rs := walk(op, bucket(0), utcClock(), probe, last(cur("o", "", daysAgo(2))))
			Expect(rs[0].err).To(MatchError(ContainSubstring("EIO")))
			Expect(rs[0].d).To(Equal(lifecycle.Decision{}))
		})

		It("passes a delete marker unread", func() {
			dm := marker(cur("o", "dm", daysAgo(2)))
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(dm)))).To(HaveKey(dm.Key))
			Expect(probe.TagsCallCount()).To(BeZero())
		})

		It("reads nothing for an entry no action is due on", func() {
			Expect(acted(walk(op, bucket(0), utcClock(), probe, last(cur("o", "", now))))).To(BeEmpty())
			Expect(probe.TagsCallCount()).To(BeZero())
		})

		It("reads nothing for a dropped transition to the entry's own class, though an earlier action is due", func() {
			op := opOf(lifecycle.Rule{
				ID: "r", Expiration: lifecycle.Expiration{Days: "10"},
				Filter:      lifecycle.Filter{Tags: tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}},
				Transitions: map[string]lifecycle.Transition{"COLD": {Days: "15", StorageClass: "COLD"}},
			})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(inClass(cur("k", "v1", daysAgo(20)), "COLD"))))).To(BeEmpty())
			Expect(probe.TagsCallCount()).To(BeZero())
		})
	})

	Describe("selection", func() {
		It("gives a tie of due times to the action built first", func() {
			e := cur("k", "v1", daysAgo(20))
			r := expiration("10")
			r.Transitions = map[string]lifecycle.Transition{"COLD": {Days: "10", StorageClass: "COLD"}}
			Expect(acted(walk(opOf(r), bucket(versioningOn), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, decision(lifecycle.ActionExpiration, lifecycle.EffectMarker, e.Key)))
		})

		It("picks the action due latest", func() {
			e := cur("k", "v1", daysAgo(20))
			r := expiration("10")
			r.Transitions = map[string]lifecycle.Transition{"COLD": {Days: "15", StorageClass: "COLD"}}
			Expect(acted(walk(opOf(r), bucket(versioningOn), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, transition(lifecycle.ActionTransition, e.Key, "COLD")))
		})

		It("gives a tie of transitions to the class the rule's map orders first", func() {
			e := cur("k", "v1", daysAgo(20))
			op := opOf(lifecycle.Rule{ID: "r", Transitions: map[string]lifecycle.Transition{
				"WARM": {Days: "10", StorageClass: "WARM"}, "COLD": {Days: "10", StorageClass: "COLD"}, "ICE": {Days: "10", StorageClass: "ICE"},
			}})
			for range 32 {
				Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(e)))).To(
					HaveKeyWithValue(e.Key, transition(lifecycle.ActionTransition, e.Key, "COLD")))
			}
		})

		It("drops a selected transition to the entry's own class, though an earlier action is due", func() {
			r := expiration("10")
			r.Transitions = map[string]lifecycle.Transition{"COLD": {Days: "15", StorageClass: "COLD"}}
			Expect(acted(walk(opOf(r), bucket(versioningOn), utcClock(), probe, last(inClass(cur("k", "v1", daysAgo(20)), "COLD"))))).To(BeEmpty())
		})

		It("reads an entry's empty class as STANDARD", func() {
			op := opOf(lifecycle.Rule{ID: "r", Transitions: map[string]lifecycle.Transition{"STANDARD": {Days: "1", StorageClass: "STANDARD"}}})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(cur("k", "v1", daysAgo(20)))))).To(BeEmpty())
		})

		It("transitions a noncurrent version by the mtime of the version after it, and never a delete marker", func() {
			op := opOf(lifecycle.Rule{ID: "r", NoncurTransitions: map[string]lifecycle.Transition{"COLD": {Days: "5", StorageClass: "COLD"}}})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe,
				last(cur("k", "v3", daysAgo(6)), noncur("k", "v2", daysAgo(9)), marker(noncur("k", "dm", daysAgo(9))))))).To(Equal(
				map[meta.ObjKey]lifecycle.Decision{key("k", "v2"): transition(lifecycle.ActionNoncurrentTransition, key("k", "v2"), "COLD")}))
		})

		It("gives a noncurrent tie to the expiration", func() {
			op := opOf(lifecycle.Rule{
				ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "5"},
				NoncurTransitions: map[string]lifecycle.Transition{"COLD": {Days: "5", StorageClass: "COLD"}},
			})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(cur("k", "v2", daysAgo(6)), noncur("k", "v1", daysAgo(9)))))).To(
				HaveKeyWithValue(key("k", "v1"), decision(lifecycle.ActionNoncurrentExpiration, lifecycle.EffectRemove, key("k", "v1"))))
		})

		It("transitions a current version by its Date", func() {
			e := cur("k", "v1", now)
			op := opOf(lifecycle.Rule{ID: "r", Transitions: map[string]lifecycle.Transition{"COLD": {Date: "2026-06-01T00:00:00Z", StorageClass: "COLD"}}})
			Expect(acted(walk(op, bucket(versioningOn), utcClock(), probe, last(e)))).To(
				HaveKeyWithValue(e.Key, transition(lifecycle.ActionTransition, e.Key, "COLD")))
		})
	})

	DescribeTable("never acts on a date the prefix map holds as nil, however old the object",
		func(r lifecycle.Rule) {
			ancient := last(cur("k", "v2", time.Unix(1, 0)), noncur("k", "v1", time.Unix(0, 1)))
			Expect(acted(walk(opOf(r), bucket(versioningOn), utcClock(), probe, ancient))).To(BeEmpty())
		},
		Entry("an Expiration of the crafted wrap", lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Date: "2600-01-01T23:34:33.709551616Z"}}),
		Entry("an Expiration of 0 and a nil date", lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Days: "0", Date: "never"}}),
		Entry("a Transition of Days -1 and a nil Date", lifecycle.Rule{ID: "r", Transitions: map[string]lifecycle.Transition{"COLD": {Date: "never", StorageClass: "COLD"}}}),
		Entry("a nil date beside an action not due",
			lifecycle.Rule{ID: "r", Expiration: lifecycle.Expiration{Date: "2554-07-22"}, NoncurExpiration: lifecycle.Expiration{Days: "100000"}}),
	)

	It("decides nothing for an op that does not apply", func() {
		op := opOf(lifecycle.Rule{ID: "r", Status: "Disabled", Expiration: lifecycle.Expiration{Days: "1"}})
		Expect(acted(walk(op, bucket(0), utcClock(), probe, last(cur("k", "", daysAgo(9)))))).To(BeEmpty())
	})
})
