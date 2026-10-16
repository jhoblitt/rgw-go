package driver_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The OLH head fixture: the PUT fixture's bucket, its key k, whose head is
// putBucketID_k in the data pool, an OLH tag and the head's mtime.
const olhTag = "olhtagolhtagolhtagolhtagolhtag00"

var _ = Describe("the OLH head operations", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		now   time.Time
		mtime time.Time
	)
	const oid = putBucketID + "_k"
	open := func(ctx context.Context, kv map[string]string) {
		s = openPutStore(ctx, c, denc.Squid, kv, now)
		driver.SetClock(s, func() time.Time { return now })
	}
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 1, 9, 0, 0, 500, time.UTC)
		now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		osd := now.Add(time.Minute)
		c.SetClock(func() time.Time { return osd })
		open(ctx, nil)
		rec = testBucket(putBucketID, 11)
	})

	head := func() *fakerados.Object { return c.Object(testDataPool, "", oid) }
	writes := func() []fakerados.RecordedWrite { return c.WritesTo(testDataPool, "", oid) }
	nWrites := func() int { return c.Writes(testDataPool, "", oid) }
	// seed stores the head with attrs and data at mtime.
	seed := func(attrs map[string][]byte, data []byte) {
		c.Put(testDataPool, "", oid, data)
		o := head()
		maps.Copy(o.Xattrs, attrs)
		o.Mtime = mtime
	}
	// state is the head as get_obj_state reads it without following the OLH:
	// its user.rgw. attrs, size and mtime.
	state := func() *op.ObjectState {
		st := &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "k"}}
		o := head()
		if o == nil {
			return st
		}
		st.Exists, st.Mtime, st.Size = true, o.Mtime, uint64(len(o.Data))
		st.Attrs = map[string][]byte{}
		for name, v := range o.Xattrs {
			if strings.HasPrefix(name, meta.AttrPrefix) {
				st.Attrs[name] = slices.Clone(v)
			}
		}
		st.WriteTag = string(st.Attrs[meta.AttrIDTag])
		return st
	}
	info := func(instance string, removed bool) []byte {
		return encode(meta.OLHInfo{Target: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k", Instance: instance}}, Removed: removed})
	}
	pendingAt := func(t time.Time) []byte { return encode(meta.OLHPendingInfo{Time: t}) }
	// pending names a pending attr stamped at t, as olh_init_modification
	// names them, with suffix as its random part.
	pending := func(t time.Time, suffix string) string {
		return meta.AttrOLHPendingPrefix + fmt.Sprintf("%016x", t.Unix()) + suffix
	}
	// olhHead seeds an OLH head at olh.ver ver whose olh.info is inf, absent
	// when nil, with extra on top.
	olhHead := func(ver string, inf []byte, extra map[string][]byte) {
		attrs := map[string][]byte{
			meta.AttrIDTag:    []byte("headtagheadtagheadtagheadtaghead"),
			meta.AttrOLHIDTag: []byte(olhTag),
			meta.AttrOLHVer:   []byte(ver),
		}
		if inf != nil {
			attrs[meta.AttrOLHInfo] = inf
		}
		maps.Copy(attrs, extra)
		seed(attrs, nil)
	}
	tagGuard := func() radosclient.Step {
		return &radosclient.CmpXattrStep{Name: meta.AttrOLHIDTag, Op: radosclient.CmpEQ, Value: []byte(olhTag)}
	}
	const lowerAlnum = `^[0-9a-z]{32}$`

	Describe("olhInitModification", func() {
		It("creates a missing head exclusively, with fresh tags, an empty olh.ver and its pending attr", func(ctx SpecContext) {
			st := state()
			opTag, ver, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(ver).To(BeNil(), "no olh.ver on the head it was built on")
			Expect(opTag).To(HavePrefix(fmt.Sprintf("%016x", now.Unix())), "the op tag starts with the second of its pending time")
			Expect(opTag).To(MatchRegexp(`^[0-9a-f]{16}[0-9a-z]{16}$`))

			ws := writes()
			Expect(ws).To(HaveLen(1))
			steps := ws[0].Steps()
			Expect(steps).To(HaveLen(5))
			Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))
			Expect(names(steps[1:])).To(Equal([]string{meta.AttrIDTag, meta.AttrOLHIDTag, meta.AttrOLHVer, meta.AttrOLHPendingPrefix + opTag}))
			_, hasMtime := ws[0].Mtime()
			Expect(hasMtime).To(BeFalse(), "a missing head is created without mtime2")

			h := head()
			Expect(string(h.Xattrs[meta.AttrIDTag])).To(MatchRegexp(lowerAlnum))
			Expect(string(h.Xattrs[meta.AttrOLHIDTag])).To(MatchRegexp(lowerAlnum))
			Expect(h.Xattrs[meta.AttrIDTag]).NotTo(Equal(h.Xattrs[meta.AttrOLHIDTag]), "two tags, drawn apart")
			Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, BeEmpty()))
			Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrOLHPendingPrefix+opTag, pendingAt(now)))
			Expect(h.Xattrs).NotTo(HaveKey(meta.AttrOLHInfo))

			Expect(st.Exists).To(BeTrue(), "the state now shows the head")
			Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrOLHIDTag, h.Xattrs[meta.AttrOLHIDTag]))
			Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrIDTag, h.Xattrs[meta.AttrIDTag]))
			Expect(st.WriteTag).To(Equal(string(h.Xattrs[meta.AttrIDTag])))
			Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrOLHPendingPrefix+opTag, pendingAt(now)))
			Expect(st.Attrs).NotTo(HaveKey(meta.AttrOLHVer), "as radosgw's state, which init does not give olh.ver")
		})

		It("guards an existing OLH head on its tag and keeps its mtime", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			st := state()
			opTag, ver, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(ver).To(Equal([]byte("5")))
			w := writes()[0]
			Expect(w.Steps()).To(Equal([]radosclient.Step{
				&radosclient.AssertExistsStep{},
				tagGuard(),
				&radosclient.SetXattrStep{Name: meta.AttrOLHPendingPrefix + opTag, Value: pendingAt(now)},
			}))
			mt, ok := w.Mtime()
			Expect([]any{mt, ok}).To(Equal([]any{mtime, true}))
			Expect(head().Mtime).To(Equal(mtime))
			Expect(head().Xattrs[meta.AttrOLHIDTag]).To(Equal([]byte(olhTag)), "the tags are kept")
		})

		It("is ECANCELED and writes nothing when the OLH tag changed since the head was read", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			st := state()
			head().Xattrs[meta.AttrOLHIDTag] = []byte("another")
			_, _, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).To(MatchError(radosclient.ErrCanceled))
			Expect(head().Xattrs).NotTo(HaveKey(HavePrefix(meta.AttrOLHPendingPrefix)))
		})

		It("is ECANCELED when another writer created the head first", func(ctx SpecContext) {
			st := state()
			olhHead("", nil, nil)
			_, _, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).To(MatchError(radosclient.ErrCanceled), "EEXIST becomes ECANCELED")
			Expect(errors.Is(err, radosclient.ErrExists)).To(BeFalse(), "only ECANCELED reaches the caller, as olh_init_modification returns")
			Expect(head().Xattrs).NotTo(HaveKey(HavePrefix(meta.AttrOLHPendingPrefix)))
		})

		It("answers ENOENT when an existing head is gone, as assert_exists does", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			st := state()
			c.Remove(testDataPool, "", oid)
			_, _, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(head()).To(BeNil())
		})

		It("turns a plain object into an OLH pointing at itself", func(ctx SpecContext) {
			seed(map[string][]byte{meta.AttrIDTag: []byte("_plain\x00"), meta.AttrETag: []byte("e")}, []byte("hello"))
			st := state()
			opTag, ver, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(ver).To(BeNil())
			steps := writes()[0].Steps()
			Expect(steps).To(HaveLen(7))
			Expect(steps[:4]).To(Equal([]radosclient.Step{
				&radosclient.AssertExistsStep{},
				&radosclient.CmpXattrStep{Name: meta.AttrOLHIDTag, Op: radosclient.CmpEQ},
				&radosclient.SetXattrStep{Name: meta.AttrOLHInfo, Value: info("", false)},
				&radosclient.SetXattrStep{Name: meta.AttrIDTag, Value: head().Xattrs[meta.AttrIDTag]},
			}))
			Expect(names(steps[4:])).To(Equal([]string{meta.AttrOLHIDTag, meta.AttrOLHVer, meta.AttrOLHPendingPrefix + opTag}))
			h := head()
			Expect(h.Data).To(Equal([]byte("hello")), "the null version's data stays on the head")
			Expect(string(h.Xattrs[meta.AttrIDTag])).To(MatchRegexp(lowerAlnum), "a fresh write tag")
			Expect(h.Mtime).To(Equal(mtime))
			Expect(st.Attrs).NotTo(HaveKey(meta.AttrOLHInfo), "radosgw's state does not take the olh.info init wrote")
		})

		It("is ECANCELED when a plain object gained an OLH tag since it was read", func(ctx SpecContext) {
			seed(map[string][]byte{meta.AttrIDTag: []byte("_plain\x00")}, []byte("hello"))
			st := state()
			head().Xattrs[meta.AttrOLHIDTag] = []byte(olhTag)
			_, _, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).To(MatchError(radosclient.ErrCanceled))
			Expect(head().Xattrs).NotTo(HaveKey(meta.AttrOLHInfo))
		})

		Describe("with a condition's guard", func() {
			var other string
			BeforeEach(func() {
				other = pending(time.Date(2026, 10, 15, 11, 59, 0, 0, time.UTC), "otherotherother0")
			})

			It("guards on the olh.info it read and on no pending attr, ahead of every set", func(ctx SpecContext) {
				olhHead("5", info("v1", false), nil)
				st := state()
				opTag, _, err := s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{Info: info("v1", false), NoPending: true})
				Expect(err).NotTo(HaveOccurred(), "the op's own pending attr does not fail its check")
				w := writes()[0]
				steps := w.Steps()
				Expect(steps).To(HaveLen(5))
				Expect(steps[:2]).To(Equal([]radosclient.Step{
					&radosclient.AssertExistsStep{},
					&radosclient.CmpXattrStep{Name: meta.AttrOLHInfo, Op: radosclient.CmpEQ, Value: info("v1", false)},
				}))
				chk := execIn(w, 2, "obj_check_attrs_prefix", rgwcls.DecodeCheckAttrsPrefixOp)
				Expect(chk).To(Equal(rgwcls.CheckAttrsPrefixOp{CheckPrefix: meta.AttrOLHPendingPrefix, FailIfExist: true}))
				Expect(steps[3:]).To(Equal([]radosclient.Step{
					tagGuard(),
					&radosclient.SetXattrStep{Name: meta.AttrOLHPendingPrefix + opTag, Value: pendingAt(now)},
				}))
				Expect(head().Xattrs).To(HaveKey(meta.AttrOLHPendingPrefix + opTag))
			})

			It("is ECANCELED when olh.info changed since the condition read it", func(ctx SpecContext) {
				olhHead("5", info("v1", false), nil)
				st := state()
				head().Xattrs[meta.AttrOLHInfo] = info("v2", false)
				_, _, err := s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{Info: info("v1", false), NoPending: true})
				Expect(err).To(MatchError(radosclient.ErrCanceled))
				Expect(head().Xattrs).NotTo(HaveKey(HavePrefix(meta.AttrOLHPendingPrefix)))
			})

			It("is ECANCELED while another writer's pending attr remains", func(ctx SpecContext) {
				olhHead("5", info("v1", false), map[string][]byte{other: pendingAt(now)})
				st := state()
				_, _, err := s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{Info: info("v1", false), NoPending: true})
				Expect(err).To(MatchError(radosclient.ErrCanceled))
				Expect(head().Xattrs).To(HaveLen(5), "only the other writer's pending attr")
			})

			It("keeps the olh.info half and drops the pending check past the bound", func(ctx SpecContext) {
				olhHead("5", info("v1", false), map[string][]byte{other: pendingAt(now)})
				st := state()
				opTag, _, err := s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{Info: info("v1", false)})
				Expect(err).NotTo(HaveOccurred())
				Expect(writes()[0].Steps()[1]).To(Equal(&radosclient.CmpXattrStep{Name: meta.AttrOLHInfo, Op: radosclient.CmpEQ, Value: info("v1", false)}))
				Expect(writes()[0].Steps()).To(HaveLen(4), "no obj_check_attrs_prefix")
				Expect(head().Xattrs).To(HaveKey(meta.AttrOLHPendingPrefix + opTag))

				st = state()
				head().Xattrs[meta.AttrOLHInfo] = info("v2", false)
				_, _, err = s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{Info: info("v1", false)})
				Expect(err).To(MatchError(radosclient.ErrCanceled), "the olh.info half still holds")
			})

			It("guards a plain object on its absent olh.info", func(ctx SpecContext) {
				seed(map[string][]byte{meta.AttrIDTag: []byte("_plain\x00")}, []byte("hello"))
				st := state()
				_, _, err := s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{NoPending: true})
				Expect(err).NotTo(HaveOccurred())
				steps := writes()[0].Steps()
				Expect(steps[1]).To(Equal(&radosclient.CmpXattrStep{Name: meta.AttrOLHInfo, Op: radosclient.CmpEQ}))
				Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("", false)))
			})

			It("leaves a missing head to its exclusive create", func(ctx SpecContext) {
				st := state()
				_, _, err := s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{NoPending: true})
				Expect(err).NotTo(HaveOccurred(), "a cmpxattr on a missing object would fail ENOENT")
				Expect(writes()[0].Steps()).To(HaveLen(5))
				Expect(writes()[0].Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))

				c.Remove(testDataPool, "", oid)
				st = state()
				olhHead("", nil, nil)
				_, _, err = s.OLHInitModificationForTest(ctx, st, &driver.OLHCondGuard{NoPending: true})
				Expect(err).To(MatchError(radosclient.ErrCanceled), "a head another writer created first")
			})
		})
	})

	Describe("olhCancelModification", func() {
		var mine, other, opTag string
		BeforeEach(func() {
			mine = pending(time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC), "minemineminemine")
			other = pending(time.Date(2026, 10, 15, 11, 30, 0, 0, time.UTC), "otherotherother0")
			opTag = strings.TrimPrefix(mine, meta.AttrOLHPendingPrefix)
		})

		It("removes its pending attr under the tag guard and keeps a head that has olh.info", func(ctx SpecContext) {
			olhHead("5", info("v1", false), map[string][]byte{mine: pendingAt(now), other: pendingAt(now)})
			s.OLHCancelModificationForTest(ctx, state(), opTag)
			Expect(writes()).To(HaveLen(1))
			Expect(writes()[0].Steps()).To(Equal([]radosclient.Step{tagGuard(), &radosclient.RmXattrStep{Name: mine}}))
			Expect(head().Xattrs).NotTo(HaveKey(mine))
			Expect(head().Xattrs).To(HaveKey(other))
		})

		It("then removes a head without olh.info once no pending attr remains", func(ctx SpecContext) {
			olhHead("", nil, map[string][]byte{mine: pendingAt(now)})
			s.OLHCancelModificationForTest(ctx, state(), opTag)
			Expect(writes()).To(HaveLen(2))
			w := writes()[1]
			steps := w.Steps()
			Expect(steps).To(HaveLen(4))
			Expect(steps[:2]).To(Equal([]radosclient.Step{tagGuard(), &radosclient.CmpXattrStep{Name: meta.AttrOLHInfo, Op: radosclient.CmpEQ}}))
			Expect(execIn(w, 2, "obj_check_attrs_prefix", rgwcls.DecodeCheckAttrsPrefixOp)).To(Equal(rgwcls.CheckAttrsPrefixOp{CheckPrefix: meta.AttrOLHPendingPrefix, FailIfExist: true}))
			Expect(steps[3]).To(Equal(&radosclient.RemoveStep{}))
			Expect(head()).To(BeNil())
		})

		It("keeps a head without olh.info while another writer's pending attr remains", func(ctx SpecContext) {
			olhHead("", nil, map[string][]byte{mine: pendingAt(now), other: pendingAt(now)})
			s.OLHCancelModificationForTest(ctx, state(), opTag)
			Expect(writes()).To(HaveLen(2), "the removal is tried")
			Expect(head()).NotTo(BeNil())
			Expect(head().Xattrs).To(HaveKey(other))
			Expect(head().Xattrs).NotTo(HaveKey(mine))
		})

		It("keeps a converted plain object, whose olh.info the state never took", func(ctx SpecContext) {
			seed(map[string][]byte{meta.AttrIDTag: []byte("_plain\x00")}, []byte("hello"))
			st := state()
			tag, _, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).NotTo(HaveOccurred())
			s.OLHCancelModificationForTest(ctx, st, tag)
			Expect(writes()).To(HaveLen(3), "init, the pending removal and the refused head removal")
			Expect(head().Data).To(Equal([]byte("hello")))
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("", false)))
		})

		It("removes nothing when the OLH tag changed", func(ctx SpecContext) {
			olhHead("", nil, map[string][]byte{mine: pendingAt(now)})
			st := state()
			head().Xattrs[meta.AttrOLHIDTag] = []byte("another")
			s.OLHCancelModificationForTest(ctx, st, opTag)
			Expect(writes()).To(HaveLen(1), "no removal after the refused one")
			Expect(head().Xattrs).To(HaveKey(mine))
		})
	})

	Describe("the pending attrs' expiry", func() {
		at := func(d time.Duration) time.Time { return now.Add(-d) }

		It("expires attrs older than rgw_olh_pending_timeout_sec, an hour, stopping at the first that has not expired", func(ctx SpecContext) {
			a, b := pending(at(2*time.Hour), "aaaaaaaaaaaaaaaa"), pending(at(time.Hour), "bbbbbbbbbbbbbbbb")
			cc, d := pending(at(30*time.Minute), "cccccccccccccccc"), pending(at(20*time.Minute), "dddddddddddddddd")
			olhHead("5", info("v1", false), map[string][]byte{
				a: pendingAt(at(2 * time.Hour)), b: pendingAt(at(time.Hour)),
				cc: pendingAt(at(30 * time.Minute)), d: pendingAt(at(3 * time.Hour)),
			})
			expired, left := s.CheckPendingOLHEntriesForTest(ctx, state())
			Expect(expired).To(Equal([]string{a, b}), "an attr exactly the timeout old is expired")
			Expect(left).To(Equal([]string{cc, d}), "names sort as their times, so the walk stops at the first unexpired one")
		})

		It("skips an attr that does not decode and leaves it pending", func(ctx SpecContext) {
			bad, ok := pending(at(3*time.Hour), "0000000000000000"), pending(at(2*time.Hour), "1111111111111111")
			olhHead("5", nil, map[string][]byte{bad: []byte("x"), ok: pendingAt(at(2 * time.Hour))})
			expired, left := s.CheckPendingOLHEntriesForTest(ctx, state())
			Expect(expired).To(Equal([]string{ok}))
			Expect(left).To(Equal([]string{bad}))
		})

		It("keeps an attr stamped after the gateway's clock", func(ctx SpecContext) {
			future := pending(now.Add(time.Hour), "ffffffffffffffff")
			olhHead("5", nil, map[string][]byte{future: pendingAt(now.Add(time.Hour))})
			expired, left := s.CheckPendingOLHEntriesForTest(ctx, state())
			Expect(expired).To(BeEmpty())
			Expect(left).To(Equal([]string{future}))
		})

		It("reads the timeout from the configuration on each call", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_olh_pending_timeout_sec": "60"})
			old := pending(at(2*time.Minute), "aaaaaaaaaaaaaaaa")
			olhHead("5", nil, map[string][]byte{old: pendingAt(at(2 * time.Minute))})
			expired, _ := s.CheckPendingOLHEntriesForTest(ctx, state())
			Expect(expired).To(Equal([]string{old}))
		})

		It("removes them in batches of 1000 under the tag guard", func(ctx SpecContext) {
			olhHead("5", nil, nil)
			var all []string
			for i := range 2500 {
				all = append(all, pending(at(2*time.Hour), fmt.Sprintf("%016d", i)))
			}
			Expect(s.RemoveOLHPendingEntriesForTest(ctx, state(), all)).To(Succeed())
			ws := writes()
			Expect(ws).To(HaveLen(3))
			for i, n := range []int{1000, 1000, 500} {
				steps := ws[i].Steps()
				Expect(steps).To(HaveLen(n+1), "batch %d", i)
				Expect(steps[0]).To(Equal(tagGuard()), "batch %d", i)
				Expect(steps[1]).To(Equal(&radosclient.RmXattrStep{Name: all[1000*i]}), "batch %d", i)
			}
		})

		DescribeTable("takes ENOENT and ECANCELED for success and stops",
			func(ctx SpecContext, stage func()) {
				olhHead("5", nil, nil)
				st := state()
				stage()
				names := make([]string, 1500)
				for i := range names {
					names[i] = pending(at(2*time.Hour), fmt.Sprintf("%016d", i))
				}
				Expect(s.RemoveOLHPendingEntriesForTest(ctx, st, names)).To(Succeed())
				Expect(nWrites()).To(Equal(1), "the second batch is not sent")
			},
			Entry("a changed tag", func() { head().Xattrs[meta.AttrOLHIDTag] = []byte("another") }),
			Entry("a removed head", func() { c.Remove(testDataPool, "", oid) }),
		)

		It("returns any other failure", func(ctx SpecContext) {
			olhHead("5", nil, nil)
			c.FailNextWrite(testDataPool, "", oid, syscall.EIO)
			err := s.RemoveOLHPendingEntriesForTest(ctx, state(), []string{pending(at(2*time.Hour), "aaaaaaaaaaaaaaaa")})
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, radosclient.ErrCanceled)).To(BeFalse())
		})
	})

	Describe("followOLH", func() {
		noUpdate := func(context.Context, *op.ObjectState) error {
			Fail("update ran")
			return nil
		}

		It("answers the current version's key", func(ctx SpecContext) {
			olhHead("3", info("v1", false), nil)
			target, dm, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).NotTo(HaveOccurred())
			Expect(dm).To(BeFalse())
			Expect(target).To(Equal(meta.ObjKey{Name: "k", Instance: "v1"}))
			Expect(nWrites()).To(BeZero())
		})

		It("is EINVAL without olh.ver", func(ctx SpecContext) {
			olhHead("3", info("v1", false), nil)
			delete(head().Xattrs, meta.AttrOLHVer)
			_, dm, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).To(MatchError(radosclient.ErrInvalid))
			Expect(dm).To(BeFalse())
		})

		It("is ENOENT without olh.info", func(ctx SpecContext) {
			olhHead("3", nil, nil)
			_, dm, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(dm).To(BeFalse())
		})

		It("is ENOENT with the delete-marker flag when the current version is a delete marker", func(ctx SpecContext) {
			olhHead("3", info("dm1", true), nil)
			target, dm, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(dm).To(BeTrue(), "v20.2.4's is_dm, set on both releases")
			Expect(target).To(BeZero())
		})

		It("fails as EIO when olh.info does not decode", func(ctx SpecContext) {
			olhHead("3", []byte{1}, nil)
			_, _, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).To(MatchError(op.ErrUnknown))
		})

		It("expires old pending attrs under the tag guard and runs no update when none remains", func(ctx SpecContext) {
			old := pending(now.Add(-2*time.Hour), "aaaaaaaaaaaaaaaa")
			olhHead("3", info("v1", false), map[string][]byte{old: pendingAt(now.Add(-2 * time.Hour))})
			target, _, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).NotTo(HaveOccurred())
			Expect(target.Instance).To(Equal("v1"))
			Expect(writes()).To(HaveLen(1))
			Expect(writes()[0].Steps()).To(Equal([]radosclient.Step{tagGuard(), &radosclient.RmXattrStep{Name: old}}))
			Expect(head().Xattrs).NotTo(HaveKey(old))
		})

		It("runs the update for a pending attr left, then answers from the head as it was read", func(ctx SpecContext) {
			old := pending(now.Add(-2*time.Hour), "aaaaaaaaaaaaaaaa")
			fresh := pending(now.Add(-time.Minute), "bbbbbbbbbbbbbbbb")
			olhHead("3", info("v1", false), map[string][]byte{old: pendingAt(now.Add(-2 * time.Hour)), fresh: pendingAt(now.Add(-time.Minute))})
			st := state()
			var updated *op.ObjectState
			target, _, err := s.FollowOLHForTest(ctx, st, func(_ context.Context, h *op.ObjectState) error {
				updated = h
				Expect(nWrites()).To(Equal(1), "the expired attr is removed before the update")
				head().Xattrs[meta.AttrOLHInfo] = info("v2", false)
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeIdenticalTo(st))
			Expect(target.Instance).To(Equal("v1"), "follow_olh reads the state it was given after update_olh, which leaves it as read")
		})

		It("takes the update's ECANCELED for ENOENT", func(ctx SpecContext) {
			fresh := pending(now.Add(-time.Minute), "bbbbbbbbbbbbbbbb")
			olhHead("3", info("v1", false), map[string][]byte{fresh: pendingAt(now.Add(-time.Minute))})
			_, dm, err := s.FollowOLHForTest(ctx, state(), func(context.Context, *op.ObjectState) error {
				return &radosclient.Error{Errno: int32(syscall.ECANCELED)}
			})
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(errors.Is(err, radosclient.ErrCanceled)).To(BeFalse())
			Expect(dm).To(BeFalse())
		})

		It("returns the update's other failures", func(ctx SpecContext) {
			fresh := pending(now.Add(-time.Minute), "bbbbbbbbbbbbbbbb")
			olhHead("3", info("v1", false), map[string][]byte{fresh: pendingAt(now.Add(-time.Minute))})
			boom := errors.New("boom")
			_, _, err := s.FollowOLHForTest(ctx, state(), func(context.Context, *op.ObjectState) error { return boom })
			Expect(err).To(MatchError(boom))
		})

		It("returns a failed expiry before anything else", func(ctx SpecContext) {
			old := pending(now.Add(-2*time.Hour), "aaaaaaaaaaaaaaaa")
			olhHead("3", info("v1", false), map[string][]byte{old: pendingAt(now.Add(-2 * time.Hour))})
			c.FailNextWrite(testDataPool, "", oid, syscall.EIO)
			_, _, err := s.FollowOLHForTest(ctx, state(), noUpdate)
			Expect(err).To(HaveOccurred())
		})

		It("expires by the gateway's clock as it moves", func(ctx SpecContext) {
			p := pending(now.Add(-10*time.Minute), "aaaaaaaaaaaaaaaa")
			olhHead("3", info("v1", false), map[string][]byte{p: pendingAt(now.Add(-10 * time.Minute))})
			updates := 0
			update := func(context.Context, *op.ObjectState) error { updates++; return nil }
			_, _, err := s.FollowOLHForTest(ctx, state(), update)
			Expect(err).NotTo(HaveOccurred())
			Expect(updates).To(Equal(1), "not yet expired")
			now = now.Add(50 * time.Minute)
			later := now.Add(time.Minute)
			c.SetClock(func() time.Time { return later })
			_, _, err = s.FollowOLHForTest(ctx, state(), update)
			Expect(err).NotTo(HaveOccurred())
			Expect(updates).To(Equal(1), "expired an hour after it was stamped")
			Expect(head().Xattrs).NotTo(HaveKey(p))
		})
	})

	Describe("applyOLHLog", func() {
		var idx *driver.FakeOLHIndex
		BeforeEach(func() {
			idx = &driver.FakeOLHIndex{WritesBefore: nWrites}
		})
		const tagP, tagQ = "0000000068ef9b50pppppppppppppppp", "0000000068ef9b50qqqqqqqqqqqqqqqq"
		link := func(instance, tag string) rgwcls.OLHLogEntry {
			return rgwcls.OLHLogEntry{Op: rgwcls.OLHOpLinkOLH, OpTag: tag, Key: rgwcls.ObjKey{Name: "k", Instance: instance}}
		}
		marker := func(instance, tag string) rgwcls.OLHLogEntry {
			e := link(instance, tag)
			e.DeleteMarker = true
			return e
		}
		unlink := func(tag string) rgwcls.OLHLogEntry {
			return rgwcls.OLHLogEntry{Op: rgwcls.OLHOpUnlinkOLH, OpTag: tag, Key: rgwcls.ObjKey{Name: "k"}}
		}
		removal := func(instance, tag string) rgwcls.OLHLogEntry {
			return rgwcls.OLHLogEntry{Op: rgwcls.OLHOpRemoveInstance, OpTag: tag, Key: rgwcls.ObjKey{Name: "k", Instance: instance}}
		}
		withPending := func(tags ...string) map[string][]byte {
			m := map[string][]byte{}
			for _, t := range tags {
				m[meta.AttrOLHPendingPrefix+t] = pendingAt(now)
			}
			return m
		}
		// headOp is the apply's head op: the last write before the trim or
		// the clear.
		headOp := func() fakerados.RecordedWrite {
			GinkgoHelper()
			ws := writes()
			Expect(ws).NotTo(BeEmpty())
			return ws[len(ws)-1]
		}

		It("does nothing for an empty log", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			last, err := s.ApplyOLHLogForTest(ctx, idx, state(), nil, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(last).To(BeZero())
			Expect(nWrites()).To(BeZero())
			Expect(idx.Calls).To(BeEmpty())
		})

		It("writes one head op guarded on the tag and the olh.ver bytes read, then trims the log", func(ctx SpecContext) {
			olhHead("2", info("v1", false), withPending(tagP))
			st := state()
			last, err := s.ApplyOLHLogForTest(ctx, idx, st, map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}}, tagP, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(last).To(BeEquivalentTo(3))
			Expect(writes()).To(HaveLen(1))
			w := writes()[0]
			Expect(w.Steps()).To(Equal([]radosclient.Step{
				tagGuard(),
				&radosclient.CmpXattrStep{Name: meta.AttrOLHVer, Op: radosclient.CmpEQ, Value: []byte("2")},
				&radosclient.SetXattrStep{Name: meta.AttrOLHVer, Value: []byte("3")},
				&radosclient.RmXattrStep{Name: meta.AttrOLHPendingPrefix + tagP},
				&radosclient.SetXattrStep{Name: meta.AttrOLHInfo, Value: info("v2", false)},
			}))
			mt, ok := w.Mtime()
			Expect([]any{mt, ok}).To(Equal([]any{mtime, true}), "the head's mtime is kept")
			Expect(idx.Methods()).To(Equal([]string{"trimLog"}))
			Expect(idx.Calls[0].Ver).To(BeEquivalentTo(3))
			Expect(idx.Calls[0].Head).To(BeIdenticalTo(st))
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("3")))
			Expect(head().Mtime).To(Equal(mtime))
		})

		It("lands a log reaching 10 over an olh.ver of 9, which a string GTE would refuse", func(ctx SpecContext) {
			olhHead("9", info("v1", false), nil)
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{10: {link("v2", tagP)}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("10")))
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("v2", false)))
		})

		It("is ECANCELED and writes nothing when a newer apply moved olh.ver since the head was read", func(ctx SpecContext) {
			olhHead("2", info("v1", false), withPending(tagP, tagQ))
			st := state()
			head().Xattrs[meta.AttrOLHVer] = []byte("4")
			head().Xattrs[meta.AttrOLHInfo] = info("v3", false)
			before := maps.Clone(head().Xattrs)
			_, err := s.ApplyOLHLogForTest(ctx, idx, st, map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}}, tagP, nil)
			Expect(err).To(MatchError(radosclient.ErrCanceled))
			Expect(head().Xattrs).To(Equal(before))
			Expect(idx.Calls).To(BeEmpty(), "no trim after a refused head op")
		})

		It("is ECANCELED when the OLH tag changed", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			st := state()
			head().Xattrs[meta.AttrOLHIDTag] = []byte("another")
			_, err := s.ApplyOLHLogForTest(ctx, idx, st, map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}}, "", nil)
			Expect(err).To(MatchError(radosclient.ErrCanceled))
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("2")))
		})

		DescribeTable("replays LINK_OLH by epoch, then by instance at the head's own epoch",
			func(ctx SpecContext, cur string, epoch uint64, instance, want string) {
				olhHead("5", info(cur, false), nil)
				_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{epoch: {link(instance, tagP)}}, "", nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info(want, false)))
				Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte(strconv.FormatUint(epoch, 10))))
			},
			Entry("an epoch above the head's applies", "m", uint64(6), "z", "z"),
			Entry("the head's epoch and an instance sorting before the current applies", "m", uint64(5), "a", "a"),
			Entry("the head's epoch and an instance sorting after the current does not", "m", uint64(5), "z", "m"),
			Entry("the head's epoch over a target without an instance applies", "", uint64(5), "z", "z"),
		)

		It("compares the log's epoch, not the entry's own", func(ctx SpecContext) {
			olhHead("4", info("m", false), nil)
			e := link("z", tagP)
			e.Epoch = 3
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{5: {e}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("z", false)))
		})

		It("links a delete marker as removed", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {marker("dm", tagP)}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("dm", true)))
		})

		It("takes a link after an unlink, and trims", func(ctx SpecContext) {
			olhHead("2", info("v1", false), withPending(tagP, tagQ))
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {unlink(tagP)}, 4: {link("v2", tagQ)}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(head()).NotTo(BeNil(), "the later link keeps the OLH")
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("v2", false)))
			Expect(idx.Methods()).To(Equal([]string{"trimLog"}))
			Expect(idx.Calls[0].Ver).To(BeEquivalentTo(4))
		})

		It("marks the removal after a link's unlink and clears the head instead of trimming", func(ctx SpecContext) {
			olhHead("2", info("v1", false), withPending(tagP, tagQ))
			st := state()
			_, err := s.ApplyOLHLogForTest(ctx, idx, st, map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}, 4: {unlink(tagQ)}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			ws := writes()
			Expect(ws).To(HaveLen(2), "the head op, then clear_olh's removal")
			Expect(names(ws[0].Steps())).To(Equal([]string{meta.AttrOLHVer}), "no olh.info: the unlink came last")
			Expect(head()).To(BeNil())
			Expect(idx.Methods()).To(Equal([]string{"clear"}))
			Expect(idx.Calls[0].Head).To(BeIdenticalTo(st))
		})

		It("removes every entry's pending attr, in log order", func(ctx SpecContext) {
			olhHead("2", info("v1", false), withPending(tagQ, "r"))
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{
				3: {link("v2", tagQ), removal("v1", tagQ)},
				4: {removal("v0", "r")},
			}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			var rm []string
			for _, st := range headOp().Steps() {
				if x, ok := st.(*radosclient.RmXattrStep); ok {
					rm = append(rm, x.Name)
				}
			}
			Expect(rm).To(Equal([]string{meta.AttrOLHPendingPrefix + tagQ, meta.AttrOLHPendingPrefix + tagQ, meta.AttrOLHPendingPrefix + "r"}))
			Expect(head().Xattrs).NotTo(HaveKey(HavePrefix(meta.AttrOLHPendingPrefix)))
		})

		It("removes the instances the log names before the head op, with the writer's own tag guard on its own", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			own := []byte("tx-own\x00")
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{
				3: {link("v2", tagP), removal("v1", tagP)},
				4: {removal("v0", tagQ)},
			}, tagP, own)
			Expect(err).NotTo(HaveOccurred())
			Expect(idx.Methods()).To(Equal([]string{"removeInstance", "removeInstance", "trimLog"}))
			Expect(idx.Calls[0].Key).To(Equal(meta.ObjKey{Name: "k", Instance: "v1"}))
			Expect(idx.Calls[0].ExpectTag).To(Equal(own), "an entry carrying the replaying writer's op tag")
			Expect(idx.Calls[1].Key).To(Equal(meta.ObjKey{Name: "k", Instance: "v0"}))
			Expect(idx.Calls[1].ExpectTag).To(BeNil(), "another writer's entry")
			Expect(idx.Calls[0].Writes).To(BeZero(), "before the head op")
			Expect(idx.Calls[1].Writes).To(BeZero())
			Expect(idx.Calls[2].Writes).To(Equal(1))
		})

		It("passes no tag guard without the writer's expected tag", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP)}}, tagP, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(idx.Calls[0].ExpectTag).To(BeNil())
		})

		It("goes on past an instance already gone", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			idx.RemoveErr = map[string]error{"v1": &radosclient.Error{Errno: int32(syscall.ENOENT)}}
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP)}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(nWrites()).To(Equal(1))
		})

		It("stops at a removal that fails otherwise, before the head op", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			boom := errors.New("boom")
			idx.RemoveErr = map[string]error{"v1": boom}
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), removal("v0", tagP)}}, "", nil)
			Expect(err).To(MatchError(boom))
			Expect(nWrites()).To(BeZero())
			Expect(idx.Methods()).To(Equal([]string{"removeInstance"}))
		})

		Describe("a removal its expected write tag refuses", func() {
			const ownTag = "tx-own\x00"
			own := func() []byte { return []byte(ownTag) }
			refusal := func(instance string) error {
				return fmt.Errorf("removing version %q: %w", instance, driver.ErrRemovalRefused)
			}

			DescribeTable("is passed over: the replay lands, then reports the refusal",
				func(ctx SpecContext, log map[uint64][]rgwcls.OLHLogEntry, after []string, gone bool) {
					olhHead("2", info("v1", false), withPending(tagP))
					idx.RemoveErr = map[string]error{"v1": refusal("v1")}
					last, err := s.ApplyOLHLogForTest(ctx, idx, state(), log, tagP, own())
					Expect(err).To(MatchError(driver.ErrRemovalRefused))
					Expect(last).To(BeEquivalentTo(3), "the replay completed")
					Expect(idx.Methods()).To(Equal(after), "the removals after the refused one still run")
					Expect(idx.Calls[0].ExpectTag).To(Equal(own()))
					if gone {
						Expect(head()).To(BeNil())
						return
					}
					Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("3")))
					Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("v2", false)))
					Expect(head().Xattrs).NotTo(HaveKey(meta.AttrOLHPendingPrefix+tagP), "its pending attr goes, so no later replay meets the entry")
				},
				Entry("then trims the log",
					map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP), removal("v1", tagP), removal("v0", tagP)}},
					[]string{"removeInstance", "removeInstance", "trimLog"}, false),
				Entry("then clears a versionless OLH",
					map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}},
					[]string{"removeInstance", "clear"}, true),
			)

			It("reports a head op that fails instead, leaving the entry for the retry", func(ctx SpecContext) {
				olhHead("2", info("v1", false), withPending(tagP))
				st := state()
				head().Xattrs[meta.AttrOLHVer] = []byte("4")
				idx.RemoveErr = map[string]error{"v1": refusal("v1")}
				_, err := s.ApplyOLHLogForTest(ctx, idx, st, map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP)}}, tagP, own())
				Expect(err).To(MatchError(radosclient.ErrCanceled))
				Expect(errors.Is(err, driver.ErrRemovalRefused)).To(BeFalse(), "the replay did not land, so its retry meets the entry again")
				Expect(head().Xattrs).To(HaveKey(meta.AttrOLHPendingPrefix + tagP))
			})

			DescribeTable("reports a failed trim or index clear together with the refusal, which is then not final",
				func(ctx SpecContext, log map[uint64][]rgwcls.OLHLogEntry, set func(error)) {
					olhHead("2", info("v1", false), nil)
					boom := errors.New("boom")
					set(boom)
					idx.RemoveErr = map[string]error{"v1": refusal("v1")}
					last, err := s.ApplyOLHLogForTest(ctx, idx, state(), log, tagP, own())
					Expect(err).To(MatchError(boom))
					Expect(err).To(MatchError(driver.ErrRemovalRefused))
					Expect(last).To(BeEquivalentTo(3), "the head op landed")
				},
				Entry("the trim", map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP)}}, func(err error) { idx.TrimErr = err }),
				Entry("the index clear", map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}}, func(err error) { idx.ClearErr = err }),
			)

			Describe("when another writer's init refuses the clear", func() {
				// clearing is the writer's log: its own version's removal, then the
				// unlink that leaves the OLH without a version.
				clearing := func() map[uint64][]rgwcls.OLHLogEntry {
					return map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}}
				}
				BeforeEach(func() {
					olhHead("2", info("v1", false), withPending(tagP))
					idx.RemoveErr = map[string]error{"v1": refusal("v1")}
					idx.During = func(method string) {
						if method == "removeInstance" {
							head().Xattrs[meta.AttrOLHPendingPrefix+tagQ] = pendingAt(now)
						}
					}
				})

				It("trims the log to the last epoch, which the head op holds", func(ctx SpecContext) {
					last, err := s.ApplyOLHLogForTest(ctx, idx, state(), clearing(), tagP, own())
					Expect(err).To(MatchError(driver.ErrRemovalRefused))
					Expect(errors.Is(err, radosclient.ErrCanceled)).To(BeFalse(), "the clear another writer refused is not the caller's")
					Expect(last).To(BeEquivalentTo(3))
					Expect(idx.Methods()).To(Equal([]string{"removeInstance", "trimLog"}), "the clear's head removal was refused, so the index clear never ran")
					Expect(idx.Calls[1].Ver).To(BeEquivalentTo(3))
					Expect(head()).NotTo(BeNil(), "the other writer's pending attr kept the head")
					Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("3")))
					Expect(head().Xattrs).NotTo(HaveKey(meta.AttrOLHPendingPrefix + tagP))
					Expect(head().Xattrs).To(HaveKey(meta.AttrOLHPendingPrefix + tagQ))
				})

				It("leaves the refused version to the other writer's replay that follows", func(ctx SpecContext) {
					idx.Log.Log = map[uint64][]rgwcls.OLHLogEntry{3: clearing()[3], 4: {link("v2", tagQ)}}
					_, err := s.ApplyOLHLogForTest(ctx, idx, state(), clearing(), tagP, own())
					Expect(err).To(MatchError(driver.ErrRemovalRefused))
					idx.During, idx.RemoveErr = nil, nil
					n := len(idx.Calls)
					last, err := s.ApplyOLHLogForTest(ctx, idx, state(), idx.Log.Log, tagQ, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(last).To(BeEquivalentTo(4))
					Expect(idx.Methods()[n:]).To(Equal([]string{"trimLog"}), "the refused removal left the log with the trim, so nothing removes the version")
					Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("v2", false)))
					Expect(head().Xattrs).NotTo(HaveKey(HavePrefix(meta.AttrOLHPendingPrefix)))
				})
			})

			DescribeTable("takes a trim the class refuses for another OLH tag as final only once the writer's entries are gone",
				func(ctx SpecContext, ownHeadRemovalLands bool, olh rgwcls.OLHEntry, olhErr error, final bool) {
					olhHead("2", info("v1", false), withPending(tagP))
					idx.RemoveErr = map[string]error{"v1": refusal("v1")}
					if !ownHeadRemovalLands {
						idx.During = func(method string) {
							if method == "removeInstance" {
								head().Xattrs[meta.AttrOLHPendingPrefix+tagQ] = pendingAt(now)
							}
						}
					}
					canceled := &radosclient.Error{Errno: int32(syscall.ECANCELED)}
					idx.ClearErr, idx.TrimErr = canceled, canceled
					idx.OLH, idx.OLHErr = olh, olhErr
					log := map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}}
					last, err := s.ApplyOLHLogForTest(ctx, idx, state(), log, tagP, own())
					Expect(last).To(BeEquivalentTo(3))
					Expect(err).To(MatchError(driver.ErrRemovalRefused))
					Expect(errors.Is(err, radosclient.ErrCanceled)).To(BeFalse())
					_, retry := driver.UnfinishedOLHTail(err)
					Expect(retry).To(BeFalse(), "running the tail again cannot restore the writer's tag")
					Expect(errors.Is(err, driver.ErrRefusalUnsettled)).To(Equal(!final))
					ms := idx.Methods()
					Expect(ms[len(ms)-2:]).To(Equal([]string{"trimLog", "getOLH"}))
					if ownHeadRemovalLands {
						Expect(head()).To(BeNil(), "the writer's head removal landed")
					}
				},
				Entry("the OLH entry gone: final",
					false, rgwcls.OLHEntry{}, &radosclient.Error{Errno: int32(syscall.ENOENT)}, true),
				Entry("the entry re-tagged, its pending log without the writer's entries: final",
					false, rgwcls.OLHEntry{Tag: "fresh", PendingLog: map[uint64][]rgwcls.OLHLogEntry{4: {link("v9", tagQ)}}}, nil, true),
				Entry("the writer's head removal landed, another writer re-linked under fresh tags keeping the log, "+
					"and the clear and trim answered ECANCELED: not final, not retried",
					true, rgwcls.OLHEntry{Tag: "fresh", PendingLog: map[uint64][]rgwcls.OLHLogEntry{
						3: {removal("v1", tagP), unlink(tagP)},
						4: {link("v9", tagQ)},
					}}, nil, false),
			)

			It("stops the replay when the removal carried no expected tag", func(ctx SpecContext) {
				olhHead("2", info("v1", false), nil)
				idx.RemoveErr = map[string]error{"v1": refusal("v1")}
				_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagQ)}}, tagP, own())
				Expect(err).To(MatchError(driver.ErrRemovalRefused))
				Expect(nWrites()).To(BeZero(), "only a removal guarded on the expected tag can be refused and passed over")
			})
		})

		Describe("the tail after the head op", func() {
			const ownTag = "tx-own\x00"
			refuse := func() {
				idx.RemoveErr = map[string]error{"v1": fmt.Errorf("removing version %q: %w", "v1", driver.ErrRemovalRefused)}
			}
			// another stands for another writer whose init lands while the
			// replay removes its versions, so its pending attr refuses the
			// clear after the head op.
			another := func() {
				idx.During = func(method string) {
					if method == "removeInstance" {
						head().Xattrs[meta.AttrOLHPendingPrefix+tagQ] = pendingAt(now)
					}
				}
			}

			DescribeTable("returns the last epoch the landed head op wrote, and names the step left, when the tail fails",
				func(ctx SpecContext, log map[uint64][]rgwcls.OLHLogEntry, set func(error), want driver.OLHTail) {
					olhHead("2", info("v1", false), withPending(tagP))
					boom := errors.New("boom")
					set(boom)
					var expect []byte
					if want.Keep {
						expect = []byte(ownTag)
					}
					last, err := s.ApplyOLHLogForTest(ctx, idx, state(), log, tagP, expect)
					Expect(err).To(MatchError(boom))
					Expect(last).To(BeEquivalentTo(3))
					tail, ok := driver.UnfinishedOLHTail(err)
					Expect(ok).To(BeTrue(), "the error names the tail left")
					Expect(tail).To(Equal(want))
					Expect(errors.Is(err, driver.ErrRemovalRefused)).To(Equal(want.Keep), "a pending refusal comes with it, not final")
				},
				Entry("a trim that fails",
					map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}},
					func(err error) { idx.TrimErr = err }, driver.OLHTail{Last: 3, OpTag: tagP}),
				Entry("an index clear that fails",
					map[uint64][]rgwcls.OLHLogEntry{3: {unlink(tagP)}},
					func(err error) { idx.ClearErr = err }, driver.OLHTail{Last: 3, Clear: true, OpTag: tagP}),
				Entry("an index clear that fails with a refusal pending",
					map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}},
					func(err error) { refuse(); idx.ClearErr = err }, driver.OLHTail{Last: 3, Clear: true, Keep: true, OpTag: tagP}),
				Entry("the trim after a clear another writer refused, which fails",
					map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}},
					func(err error) { refuse(); another(); idx.TrimErr = err }, driver.OLHTail{Last: 3, Keep: true, OpTag: tagP}),
				Entry("the check of the OLH entry after the class refused that trim, which fails",
					map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), unlink(tagP)}},
					func(err error) {
						refuse()
						another()
						idx.TrimErr = &radosclient.Error{Errno: int32(syscall.ECANCELED)}
						idx.OLHErr = err
					}, driver.OLHTail{Last: 3, Keep: true, OpTag: tagP}),
			)

			DescribeTable("finishes when the step the error names runs again",
				func(ctx SpecContext, log map[uint64][]rgwcls.OLHLogEntry, set func(error), after string, trims bool) {
					olhHead("2", info("v1", false), withPending(tagP))
					idx.Log.Log = maps.Clone(log)
					set(errors.New("boom"))
					st := state()
					_, err := s.ApplyOLHLogForTest(ctx, idx, st, log, tagP, nil)
					tail, ok := driver.UnfinishedOLHTail(err)
					Expect(ok).To(BeTrue())
					set(nil)
					n := len(idx.Calls)
					Expect(s.FinishOLHLogForTest(ctx, idx, st, tail)).To(Succeed())
					Expect(idx.Methods()[n:]).To(Equal([]string{after}))
					if trims {
						Expect(idx.Log.Log).To(BeEmpty(), "the trim reached the last epoch")
					}
				},
				Entry("a trim", map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}},
					func(err error) { idx.TrimErr = err }, "trimLog", true),
				Entry("an index clear", map[uint64][]rgwcls.OLHLogEntry{3: {unlink(tagP)}},
					func(err error) { idx.ClearErr = err }, "clear", false),
			)

			DescribeTable("runs the trim after a clear another writer refused only while a refusal is pending",
				func(ctx SpecContext, keep bool, after []string) {
					olhHead("3", info("v1", false), withPending(tagQ))
					Expect(s.FinishOLHLogForTest(ctx, idx, state(), driver.OLHTail{Last: 3, Clear: true, Keep: keep, OpTag: tagP})).To(Succeed())
					Expect(idx.Methods()).To(Equal(after))
					Expect(head()).NotTo(BeNil(), "the other writer's pending attr kept the head")
				},
				Entry("with a refusal pending, the trim", true, []string{"trimLog"}),
				Entry("without one, nothing, as radosgw's replay", false, []string{}),
			)
		})

		It("replays onto the head init made for a fresh key, keeping init's mtime", func(ctx SpecContext) {
			st := state()
			opTag, ver, err := s.OLHInitModificationForTest(ctx, st, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(ver).To(BeNil())
			created := head().Mtime
			Expect(created).To(Equal(now.Add(time.Minute)), "the create took the OSD's clock")
			Expect(st.Attrs).NotTo(HaveKey(meta.AttrOLHVer), "the state holds no olh.ver, where the head stores an empty one")

			last, err := s.ApplyOLHLogForTest(ctx, idx, st, map[uint64][]rgwcls.OLHLogEntry{2: {link("v1", opTag)}}, opTag, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(last).To(BeEquivalentTo(2))
			w := writes()[1]
			Expect(w.Steps()[:2]).To(Equal([]radosclient.Step{
				&radosclient.CmpXattrStep{Name: meta.AttrOLHIDTag, Op: radosclient.CmpEQ, Value: st.Attrs[meta.AttrOLHIDTag]},
				&radosclient.CmpXattrStep{Name: meta.AttrOLHVer, Op: radosclient.CmpEQ},
			}), "an absent olh.ver compares equal to the stored empty one")
			mt, ok := w.Mtime()
			Expect([]any{mt.IsZero(), ok}).To(Equal([]any{true, true}), "the zero mtime the state read")
			h := head()
			Expect(h.Mtime).To(Equal(created), "a zero mtime keeps the stored one")
			Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("2")))
			Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info("v1", false)))
			Expect(h.Xattrs).NotTo(HaveKey(HavePrefix(meta.AttrOLHPendingPrefix)))
			Expect(idx.Methods()).To(Equal([]string{"trimLog"}))
		})

		It("fails as EIO on an op it does not know, before anything is written", func(ctx SpecContext) {
			olhHead("2", info("v1", false), nil)
			bad := rgwcls.OLHLogEntry{Op: rgwcls.OLHOpUnknown, OpTag: tagP}
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {removal("v1", tagP), bad}}, "", nil)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(nWrites()).To(BeZero())
			Expect(idx.Calls).To(BeEmpty())
		})

		It("fails as EIO when olh.info does not decode", func(ctx SpecContext) {
			olhHead("2", []byte{1}, nil)
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", tagP)}}, "", nil)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(nWrites()).To(BeZero())
		})

		It("unescapes the index name of the version it links", func(ctx SpecContext) {
			olhHead("2", nil, nil)
			e := link("v2", tagP)
			e.Key.Name = "__u"
			_, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {e}}, "", nil)
			Expect(err).NotTo(HaveOccurred())
			got := meta.DecodeOLHInfo(denc.NewDecoder(head().Xattrs[meta.AttrOLHInfo]))
			Expect(got.Target.Key).To(Equal(meta.ObjKey{Name: "_u", Instance: "v2"}))
		})

		It("leaves the log to the next replay when another writer's pending attr refuses the clear, as radosgw's replay does", func(ctx SpecContext) {
			olhHead("2", info("v1", false), withPending(tagP, tagQ))
			last, err := s.ApplyOLHLogForTest(ctx, idx, state(), map[uint64][]rgwcls.OLHLogEntry{3: {unlink(tagP)}}, tagP, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(last).To(BeEquivalentTo(3))
			Expect(idx.Calls).To(BeEmpty(), "neither the index clear nor a trim")
			Expect(head()).NotTo(BeNil())
			Expect(head().Xattrs).To(HaveKey(meta.AttrOLHPendingPrefix + tagQ))
		})

		DescribeTable("tolerates ECANCELED from the trim and the clear",
			func(ctx SpecContext, log map[uint64][]rgwcls.OLHLogEntry, set func(error)) {
				olhHead("2", info("v1", false), nil)
				set(&radosclient.Error{Errno: int32(syscall.ECANCELED)})
				_, err := s.ApplyOLHLogForTest(ctx, idx, state(), log, "", nil)
				Expect(err).NotTo(HaveOccurred())
				boom := errors.New("boom")
				olhHead("2", info("v1", false), nil)
				set(boom)
				_, err = s.ApplyOLHLogForTest(ctx, idx, state(), log, "", nil)
				Expect(err).To(MatchError(boom))
			},
			Entry("trim", map[uint64][]rgwcls.OLHLogEntry{3: {link("v2", "t")}}, func(err error) { idx.TrimErr = err }),
			Entry("clear", map[uint64][]rgwcls.OLHLogEntry{3: {unlink("t")}}, func(err error) { idx.ClearErr = err }),
		)
	})

	Describe("clearOLHHead", func() {
		var (
			idx *driver.FakeOLHIndex
			old string
		)
		BeforeEach(func() {
			idx = &driver.FakeOLHIndex{WritesBefore: nWrites}
			old = pending(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC), "aaaaaaaaaaaaaaaa")
		})

		It("expires pending attrs, removes the head under its guards, then clears the index", func(ctx SpecContext) {
			olhHead("10", info("v1", false), map[string][]byte{old: pendingAt(now.Add(-3 * time.Hour))})
			st := state()
			Expect(s.ClearOLHHeadForTest(ctx, idx, st, 10)).To(Succeed())
			ws := writes()
			Expect(ws).To(HaveLen(2))
			Expect(ws[0].Steps()).To(Equal([]radosclient.Step{tagGuard(), &radosclient.RmXattrStep{Name: old}}))
			rm := ws[1]
			steps := rm.Steps()
			Expect(steps).To(HaveLen(4))
			Expect(steps[:2]).To(Equal([]radosclient.Step{
				tagGuard(),
				&radosclient.CmpXattrStep{Name: meta.AttrOLHVer, Op: radosclient.CmpEQ, Value: []byte("10")},
			}))
			Expect(execIn(rm, 2, "obj_check_attrs_prefix", rgwcls.DecodeCheckAttrsPrefixOp)).To(Equal(rgwcls.CheckAttrsPrefixOp{CheckPrefix: meta.AttrOLHPendingPrefix, FailIfExist: true}))
			Expect(steps[3]).To(Equal(&radosclient.RemoveStep{}))
			Expect(head()).To(BeNil())
			Expect(idx.Methods()).To(Equal([]string{"clear"}))
			Expect(idx.Calls[0].Head).To(BeIdenticalTo(st))
			Expect(idx.Calls[0].Writes).To(Equal(2), "after the head's removal")
		})

		DescribeTable("is ECANCELED, keeping the head and the index, when",
			func(ctx SpecContext, stage func()) {
				olhHead("10", info("v1", false), nil)
				st := state()
				stage()
				Expect(s.ClearOLHHeadForTest(ctx, idx, st, 10)).To(MatchError(radosclient.ErrCanceled))
				Expect(head()).NotTo(BeNil())
				Expect(idx.Calls).To(BeEmpty())
			},
			Entry("a pending attr remains", func() { head().Xattrs[meta.AttrOLHPendingPrefix+"x"] = pendingAt(now) }),
			Entry("olh.ver moved", func() { head().Xattrs[meta.AttrOLHVer] = []byte("11") }),
			Entry("the OLH tag changed", func() { head().Xattrs[meta.AttrOLHIDTag] = []byte("another") }),
		)

		DescribeTable("clears the index after a removal that fails otherwise, as clear_olh does",
			func(ctx SpecContext, stage func()) {
				olhHead("10", info("v1", false), nil)
				st := state()
				stage()
				Expect(s.ClearOLHHeadForTest(ctx, idx, st, 10)).To(Succeed())
				Expect(idx.Methods()).To(Equal([]string{"clear"}))
			},
			Entry("a head already gone", func() { c.Remove(testDataPool, "", oid) }),
			Entry("an I/O error", func() { c.FailNextWrite(testDataPool, "", oid, syscall.EIO) }),
		)

		It("returns the index clear's failure", func(ctx SpecContext) {
			olhHead("10", info("v1", false), nil)
			boom := errors.New("boom")
			idx.ClearErr = boom
			Expect(s.ClearOLHHeadForTest(ctx, idx, state(), 10)).To(MatchError(boom))
		})

		It("returns a failed expiry before the removal", func(ctx SpecContext) {
			olhHead("10", info("v1", false), map[string][]byte{old: pendingAt(now.Add(-3 * time.Hour))})
			c.FailNextWrite(testDataPool, "", oid, syscall.EIO)
			Expect(s.ClearOLHHeadForTest(ctx, idx, state(), 10)).NotTo(Succeed())
			Expect(nWrites()).To(Equal(1))
			Expect(idx.Calls).To(BeEmpty())
		})
	})

	Describe("repairOLH", func() {
		var idx *driver.FakeOLHIndex
		BeforeEach(func() {
			idx = &driver.FakeOLHIndex{}
		})

		It("rewrites olh.idtag and olh.info from the index's OLH entry under the old tag's guard", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			st := state()
			idx.OLH = rgwcls.OLHEntry{Key: rgwcls.ObjKey{Name: "k", Instance: "v2"}, DeleteMarker: true, Tag: "indextag", Epoch: 6}
			Expect(s.RepairOLHForTest(ctx, idx, st)).To(Succeed())
			Expect(idx.Methods()).To(Equal([]string{"getOLH"}))
			Expect(idx.Calls[0].Head).To(BeIdenticalTo(st))
			w := writes()[0]
			Expect(w.Steps()).To(Equal([]radosclient.Step{
				tagGuard(),
				&radosclient.SetXattrStep{Name: meta.AttrOLHIDTag, Value: []byte("indextag")},
				&radosclient.SetXattrStep{Name: meta.AttrOLHInfo, Value: info("v2", true)},
			}))
			mt, ok := w.Mtime()
			Expect([]any{mt, ok}).To(Equal([]any{mtime, true}))
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHVer, []byte("5")), "olh.ver is not touched")
		})

		It("writes nothing when the tags already agree", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			idx.OLH = rgwcls.OLHEntry{Tag: olhTag}
			Expect(s.RepairOLHForTest(ctx, idx, state())).To(Succeed())
			Expect(nWrites()).To(BeZero())
		})

		It("returns the index read's failure", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			idx.OLHErr = &radosclient.Error{Errno: int32(syscall.ENOENT)}
			Expect(s.RepairOLHForTest(ctx, idx, state())).To(MatchError(radosclient.ErrNotFound))
			Expect(nWrites()).To(BeZero())
		})

		It("is ECANCELED when the head's tag changed since it was read", func(ctx SpecContext) {
			olhHead("5", info("v1", false), nil)
			st := state()
			head().Xattrs[meta.AttrOLHIDTag] = []byte("another")
			idx.OLH = rgwcls.OLHEntry{Tag: "indextag"}
			Expect(s.RepairOLHForTest(ctx, idx, st)).To(MatchError(radosclient.ErrCanceled))
			Expect(head().Xattrs).To(HaveKeyWithValue(meta.AttrOLHIDTag, []byte("another")))
		})
	})
})
