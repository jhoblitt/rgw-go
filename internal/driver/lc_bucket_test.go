package driver_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/lifecycle/lifecyclefakes"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// lcDay is the lifecycle day of the bucket pass specs, which run with
// rgw_lc_debug_interval at 10 s: an Expiration of one day is due for an
// object 10 s old, and a bucket completed is due again 10 s later.
const lcDay = 10 * time.Second

// lcShard is the one lifecycle shard of the specs, which run with
// rgw_lc_max_objs at 1.
const lcShard = "lc.0"

// lcAttr is the user.rgw.lc attr of a configuration of rules, as radosgw
// stores it on Squid.
func lcAttr(rules ...lifecycle.Rule) []byte {
	e := denc.NewEncoder()
	lifecycle.Configuration{Rules: rules}.Encode(e, denc.Squid)
	return e.Bytes()
}

// expireRule is the enabled rule id expiring the objects under prefix a day
// after their mtime.
func expireRule(id, prefix string) lifecycle.Rule {
	return lifecycle.Rule{ID: id, Status: "Enabled", Filter: lifecycle.Filter{Prefix: prefix}, Expiration: lifecycle.Expiration{Days: "1"}}
}

// lcNotified is the keys of the notifications n got, in order.
func lcNotified(n *lifecyclefakes.FakeNotifier) []string {
	var keys []string
	for i := range n.NotifyCallCount() {
		_, note := n.NotifyArgsForCall(i)
		keys = append(keys, note.Key.Name)
	}
	return keys
}

var _ = Describe("the lifecycle bucket pass", func() {
	var (
		c     *fakerados.Cluster
		clk   *specClock
		s     *driver.Store
		h     *driver.LCHarness
		notes *lifecyclefakes.FakeNotifier
		// ac, when a spec sets it before open, is the cluster s opens over,
		// around c, and rel the release s encodes for, when set.
		ac  *aroundCluster
		rel *denc.Release
	)
	BeforeEach(func() {
		c = newPutCluster()
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			p.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: -1}
		})
		clk = &specClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
		c.SetClock(clk.Now)
		ac, rel = nil, nil
	})
	// open opens s over c with the specs' lifecycle day and one shard,
	// overridden by kv, and a harness h whose notifier is notes.
	open := func(ctx context.Context, kv map[string]string) {
		GinkgoHelper()
		opts := map[string]string{"rgw_lc_debug_interval": "10", "rgw_lc_max_objs": "1"}
		maps.Copy(opts, kv)
		var cl radosclient.Cluster = c
		if ac != nil {
			cl = ac
		}
		var err error
		s, err = driver.Open(ctx, cl, conf(opts), driver.Options{Release: rel})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		driver.SetClock(s, clk.Now)
		h = s.LCHarnessForTest(clk.Now)
		notes = &lifecyclefakes.FakeNotifier{}
		h.SetNotifier(notes)
	}
	reload := func(ctx context.Context, name string) *op.BucketRecord {
		GinkgoHelper()
		rec, err := s.GetBucket(ctx, "", name)
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	// create makes the bucket name of alice's with flags set on its
	// instance and attr as its user.rgw.lc, nil for none, and its lifecycle
	// entry, and returns it and the entry's key.
	create := func(ctx context.Context, name string, flags uint32, attr []byte) (*op.BucketRecord, string) {
		GinkgoHelper()
		rec, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: attrsOwner, Zonegroup: "zg-ceph-objectstore", Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "Alice"))}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		if attr != nil {
			Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrLC: attr}, nil)).To(Succeed())
		}
		if flags != 0 {
			rec = reload(ctx, name)
			rec.Info.Flags |= flags
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		}
		Expect(s.SetEntry(ctx, rec)).To(Succeed())
		return reload(ctx, name), ":" + name + ":" + rec.Info.Bucket.Marker
	}
	put := func(ctx context.Context, rec *op.BucketRecord, name, data string, attrs map[string][]byte) {
		GinkgoHelper()
		a := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "Alice"))}
		maps.Copy(a, attrs)
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: name}, strings.NewReader(data), op.PutParams{Attrs: a, Size: int64(len(data))})
		Expect(err).NotTo(HaveOccurred())
	}
	stat := func(ctx context.Context, rec *op.BucketRecord, name string) *op.ObjectState {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: name})
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	listed := func(ctx context.Context, rec *op.BucketRecord) []string {
		GinkgoHelper()
		settle(s)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 2000})
		Expect(err).NotTo(HaveOccurred())
		return keys(res.Entries)
	}
	pass := func(ctx context.Context) {
		GinkgoHelper()
		settle(s)
		h.Pass(ctx)
		settle(s)
	}
	// indexed is the count of rec's objects its index shards' headers hold,
	// the stats its quota is checked against.
	indexed := func(rec *op.BucketRecord) uint64 {
		GinkgoHelper()
		settle(s)
		var n uint64
		for i := range rec.Info.Layout.Current.Layout.Normal.NumShards {
			n += c.Header(rookIndexPool, "", rec.Info.IndexShardOID(rec.Info.Layout.Current, i)).Stats[rgwcls.CategoryMain].NumEntries
		}
		return n
	}
	failed := func(key string) rgwcls.LCEntry {
		return rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Unix()), Status: rgwcls.LCStatusFailed} //nolint:gosec // a time_t after the epoch
	}
	completed := func(key string) rgwcls.LCEntry {
		return rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Unix()), Status: rgwcls.LCStatusComplete} //nolint:gosec // a time_t after the epoch
	}
	retention := func(until time.Time) map[string][]byte {
		return map[string][]byte{meta.AttrObjectRetention: encode(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: until})}
	}
	legalHold := func(status string) map[string][]byte {
		return map[string][]byte{meta.AttrObjectLegalHold: encode(meta.ObjectLegalHold{Status: status})}
	}
	// setKeepingMtime writes attrs on rec's head of name with its mtime kept,
	// as radosgw's set_attrs writes without FLAG_LOG_OP: the ETag and the
	// mtime stay, and the RADOS version moves.
	setKeepingMtime := func(ctx context.Context, rec *op.BucketRecord, name string, attrs map[string][]byte) {
		GinkgoHelper()
		st := stat(ctx, rec, name)
		pool, err := c.Pool(ctx, testDataPool, "")
		Expect(err).NotTo(HaveOccurred())
		w := radosclient.NewWriteOp()
		for k, v := range attrs {
			w.SetXattr(k, v)
		}
		w.SetMtime(st.Mtime)
		_, err = pool.Write(ctx, rec.Info.Bucket.Marker+"_"+name, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		after := stat(ctx, rec, name)
		Expect([]any{after.Mtime, after.ETag}).To(Equal([]any{st.Mtime, st.ETag}))
		Expect(after.Epoch).To(BeNumerically(">", st.Epoch))
	}
	// driftHead writes rec's head of name with no mtime, as radosgw's removal
	// of an OLH head's pending attr does, so the OSD stamps it with the
	// clock's time.
	driftHead := func(ctx context.Context, rec *op.BucketRecord, name string) {
		GinkgoHelper()
		pool, err := c.Pool(ctx, testDataPool, "")
		Expect(err).NotTo(HaveOccurred())
		w := radosclient.NewWriteOp()
		w.RmXattr(meta.AttrOLHPendingPrefix + "00000000000000000radosgwpending")
		_, err = pool.Write(ctx, rec.Info.Bucket.Marker+"_"+name, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
	}
	// setFlags sets flags on the instance of the bucket name.
	setFlags := func(ctx context.Context, name string, flags uint32) {
		GinkgoHelper()
		rec := reload(ctx, name)
		rec.Info.Flags |= flags
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	// clearFlags clears flags on the instance of the bucket name.
	clearFlags := func(ctx context.Context, name string, flags uint32) {
		GinkgoHelper()
		rec := reload(ctx, name)
		rec.Info.Flags &^= flags
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	// written maps the ETag of each body putAs wrote to the body.
	var written map[string]string
	BeforeEach(func() { written = map[string]string{} })
	// putAs is put of data under an ACL that owner owns, which the index
	// entry names as the object's owner, as a new version under a fresh
	// instance when rec's versioning is enabled, as S3's PutObject names it.
	// It waits for the write's index completion, which a delete that converts
	// the entry would otherwise race.
	putAt := func(ctx context.Context, rec *op.BucketRecord, name, data string, owner meta.Owner, attrs map[string][]byte, mtime time.Time) {
		GinkgoHelper()
		a := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(owner, owner.String()))}
		maps.Copy(a, attrs)
		key := meta.ObjKey{Name: name}
		if rec.Info.VersioningEnabled() {
			key.Instance = meta.NewInstance()
		}
		_, err := s.PutObject(ctx, rec, key, strings.NewReader(data), op.PutParams{Attrs: a, Size: int64(len(data)), Mtime: mtime})
		Expect(err).NotTo(HaveOccurred())
		written[md5hex(data)] = data
		settle(s)
	}
	putAs := func(ctx context.Context, rec *op.BucketRecord, name, data string, owner meta.Owner, attrs map[string][]byte) {
		GinkgoHelper()
		putAt(ctx, rec, name, data, owner, attrs, time.Time{})
	}
	putVersion := func(ctx context.Context, rec *op.BucketRecord, name, data string) {
		GinkgoHelper()
		putAs(ctx, rec, name, data, attrsOwner, nil)
	}
	// listVersions is name's versions in rec, newest first, as an ordered
	// listing with versions gives them, page after page.
	listVersions := func(ctx context.Context, rec *op.BucketRecord, name string) []op.ObjectEntry {
		GinkgoHelper()
		settle(s)
		var all []op.ObjectEntry
		p := op.ListObjectsParams{Prefix: name, MaxKeys: 1000, ListVersions: true}
		for {
			res, err := s.ListObjects(ctx, rec, p)
			Expect(err).NotTo(HaveOccurred())
			all = append(all, res.Entries...)
			if !res.Truncated {
				break
			}
			p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
		}
		return slices.DeleteFunc(all, func(e op.ObjectEntry) bool { return e.Key.Name != name })
	}
	// versions is listVersions as text: each version's body, or DM for a
	// delete marker, then "@null" for the null version and "*" for the
	// current one.
	versions := func(ctx context.Context, rec *op.BucketRecord, name string) []string {
		GinkgoHelper()
		var out []string
		for _, e := range listVersions(ctx, rec, name) {
			v := written[e.ETag]
			if e.DeleteMarker {
				v = "DM"
			}
			if e.Key.Instance == "" {
				v += "@null"
			}
			if e.IsLatest {
				v += "*"
			}
			out = append(out, v)
		}
		return out
	}
	// versionOf is the listed version of name that holds data, or the delete
	// marker for "DM".
	versionOf := func(ctx context.Context, rec *op.BucketRecord, name, data string) op.ObjectEntry {
		GinkgoHelper()
		for _, e := range listVersions(ctx, rec, name) {
			if e.DeleteMarker && data == "DM" || !e.DeleteMarker && e.ETag == md5hex(data) {
				return e
			}
		}
		Fail(fmt.Sprintf("no version of %q holds %q", name, data))
		return op.ObjectEntry{}
	}
	del := func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) *op.DeleteObjectResult {
		GinkgoHelper()
		res, err := s.DeleteObject(ctx, rec, key, op.DeleteParams{MarkerOwner: attrsOwner})
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	Describe("on a bucket never versioned", func() {
		It("removes an expired object, keeps one not yet due, completes the entry and notifies the removal", func(ctx SpecContext) {
			open(ctx, nil)
			rec, key := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			put(ctx, rec, "old", "o", nil)
			clk.Advance(lcDay)
			put(ctx, rec, "new", "n", nil)
			pass(ctx)
			Expect(listed(ctx, rec)).To(Equal([]string{"new"}))
			Expect(stat(ctx, rec, "old").Exists).To(BeFalse())
			Expect(indexed(rec)).To(BeEquivalentTo(1))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			Expect(notes.NotifyCallCount()).To(Equal(1))
			_, note := notes.NotifyArgsForCall(0)
			Expect(note).To(Equal(lifecycle.Notification{
				Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "old"}, ETag: md5hex("o"), Size: 1,
				Events: []lifecycle.Event{lifecycle.EventExpirationCurrent, lifecycle.EventExpirationDelete},
			}), "remove_expired_obj's ObjectExpirationCurrent and LifecycleExpirationDelete")
		})

		DescribeTable("keeps an object written again after its listing",
			func(ctx SpecContext, data string, offset time.Duration) {
				open(ctx, nil)
				rec, _ := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
				written := clk.Now()
				put(ctx, rec, "k", "first", nil)
				clk.Advance(lcDay)
				h.SetHook(func(ctx context.Context, at driver.LCStep, key meta.ObjKey) {
					defer GinkgoRecover()
					if at == driver.LCStepDecided {
						_, err := s.PutObject(ctx, rec, key, strings.NewReader(data), op.PutParams{Size: int64(len(data)), Mtime: written.Add(offset)})
						Expect(err).NotTo(HaveOccurred())
					}
				})
				pass(ctx)
				st := stat(ctx, rec, "k")
				Expect(st.Exists).To(BeTrue())
				Expect(st.ETag).To(Equal(md5hex(data)))
				Expect(st.Mtime).To(Equal(written.Add(offset)))
				Expect(notes.NotifyCallCount()).To(BeZero())
			},
			Entry("with other data, at the time of the pass", "second", lcDay),
			Entry("with other data, within the second of the mtime listed, which radosgw's whole-second guard removes", "second", 500*time.Millisecond),
			Entry("with the same data, so only its mtime moved", "first", lcDay),
			Entry("with the same data, within the second of the mtime listed", "first", 500*time.Millisecond),
		)

		DescribeTable("applies the rule's tag filter to the object's own tags",
			func(ctx SpecContext, objTags []tags.Tag, removed bool) {
				open(ctx, nil)
				rule := expireRule("tagged", "")
				rule.Filter.Tags = tags.Set{Tags: []tags.Tag{{Key: "project", Value: "x"}}}
				rec, _ := create(ctx, "plain", 0, lcAttr(rule))
				var attrs map[string][]byte
				if objTags != nil {
					attrs = map[string][]byte{tags.Attr: encode(tags.Set{Tags: objTags})}
				}
				put(ctx, rec, "k", "v", attrs)
				clk.Advance(lcDay)
				pass(ctx)
				Expect(stat(ctx, rec, "k").Exists).To(Equal(!removed))
			},
			Entry("the rule's tag among others", []tags.Tag{{Key: "a", Value: "b"}, {Key: "project", Value: "x"}}, true),
			Entry("the rule's key with another value", []tags.Tag{{Key: "project", Value: "y"}}, false),
			Entry("no tags", nil, false),
		)

		DescribeTable("decides again an object written between the decision and its read again, whatever the write keeps",
			func(ctx SpecContext, write map[string][]byte, removedNextPass bool) {
				open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
				rule := expireRule("tagged", "")
				rule.Filter.Tags = tags.Set{Tags: []tags.Tag{{Key: "project", Value: "x"}}}
				rec, _ := create(ctx, "plain", 0, lcAttr(rule))
				put(ctx, rec, "k", "v", map[string][]byte{tags.Attr: encode(tags.Set{Tags: []tags.Tag{{Key: "project", Value: "x"}}})})
				clk.Advance(lcDay)
				var once sync.Once
				h.SetHook(func(ctx context.Context, at driver.LCStep, k meta.ObjKey) {
					defer GinkgoRecover()
					if at == driver.LCStepDecided {
						once.Do(func() { setKeepingMtime(ctx, rec, k.Name, write) })
					}
				})
				pass(ctx)
				Expect(stat(ctx, rec, "k").Exists).To(BeTrue(), "the read again is at another version than the decision's")
				clk.Advance(lcDay)
				pass(ctx)
				Expect(stat(ctx, rec, "k").Exists).To(Equal(!removedNextPass), "decided again")
			},
			Entry("a tag set that takes it out of the rule's filter, kept on the next pass too",
				map[string][]byte{tags.Attr: encode(tags.Set{Tags: []tags.Tag{{Key: "project", Value: "y"}}})}, false),
			Entry("an attr the rule does not read, removed on the next pass", map[string][]byte{meta.AttrMetaPrefix + "note": []byte("n\x00")}, true),
		)

		DescribeTable("filters a compressed object by its uncompressed size, where radosgw compares the size stored",
			func(ctx SpecContext, filter lifecycle.Filter, removed bool) {
				open(ctx, nil)
				rule := expireRule("sized", "")
				rule.Filter = filter
				rec, _ := create(ctx, "plain", 0, lcAttr(rule))
				put(ctx, rec, "k", "stored", nil)
				ci := meta.NewCompressionInfo()
				ci.Type, ci.OrigSize = "zlib", 2000
				c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_k").Xattrs[meta.AttrCompression] = encode(ci)
				clk.Advance(lcDay)
				pass(ctx)
				Expect(stat(ctx, rec, "k").Exists).To(Equal(!removed))
			},
			Entry("larger than 1000 bytes: 2000 uncompressed, 6 stored", lifecycle.Filter{SizeGT: "1000"}, true),
			Entry("smaller than 1000 bytes", lifecycle.Filter{SizeLT: "1000"}, false),
		)

		It("keeps a stored Expiration Date past real_time, which radosgw wraps to 2015-06-14 and expires at once, and acts on that date itself", func(ctx SpecContext) {
			open(ctx, nil)
			dated := func(date string) []byte {
				return lcAttr(lifecycle.Rule{ID: "dated", Status: "Enabled", Expiration: lifecycle.Expiration{Date: date}})
			}
			wrapped, wrappedKey := create(ctx, "wrapped", 0, dated("2600-01-01T23:34:33.709551616Z"))
			plain, _ := create(ctx, "plain", 0, dated("2015-06-14T00:00:00Z"))
			put(ctx, wrapped, "k", "v", nil)
			put(ctx, plain, "k", "v", nil)
			pass(ctx)
			Expect(stat(ctx, wrapped, "k").Exists).To(BeTrue(), "the prefix map holds no date for it, so the rule names no instant to act at")
			Expect(lcEntries(c)).To(HaveKeyWithValue(wrappedKey, completed(wrappedKey)))
			Expect(stat(ctx, plain, "k").Exists).To(BeFalse(), "the instant radosgw's wrap lands at")
		})

		DescribeTable("applies no rule the zone does not run",
			func(ctx SpecContext, rule lifecycle.Rule) {
				open(ctx, nil)
				rec, key := create(ctx, "plain", 0, lcAttr(rule))
				put(ctx, rec, "k", "v", nil)
				clk.Advance(lcDay)
				pass(ctx)
				Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			},
			Entry("a disabled rule", lifecycle.Rule{ID: "off", Status: "Disabled", Expiration: lifecycle.Expiration{Days: "1"}}),
			Entry("a rule flagged ArchiveZone outside an archive zone, as zone_check skips it",
				lifecycle.Rule{ID: "archive", Status: "Enabled", Filter: lifecycle.Filter{Flags: lifecycle.FlagArchiveZone}, Expiration: lifecycle.Expiration{Days: "1"}}),
		)

		It("fails a bucket refused since its claim, before it lists anything", func(ctx SpecContext) {
			open(ctx, nil)
			rule := expireRule("transition", "")
			rule.Transitions = map[string]lifecycle.Transition{"COLD": {Days: "30", StorageClass: "COLD"}}
			rec, key := create(ctx, "plain", 0, lcAttr(rule))
			put(ctx, rec, "k", "v", nil)
			clk.Advance(lcDay)
			settle(s)
			c.ResetCounters()
			Expect(h.RunForTest(ctx, key)).To(MatchError(ContainSubstring(`refused since its claim: rule transitions objects "transition"`)))
			Expect(c.Reads(rookIndexPool, "", indexShardOID(rec, "k"))).To(BeZero(), "nothing listed")
			Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
		})

		It("completes the entry with rgwlc_skip_bucket_step, removing nothing", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgwlc_skip_bucket_step": "true"})
			rec, key := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			put(ctx, rec, "old", "o", nil)
			clk.Advance(lcDay)
			pass(ctx)
			Expect(stat(ctx, rec, "old").Exists).To(BeTrue())
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("fails the entry of a configuration that does not decode, as bucket_lc_process does", func(ctx SpecContext) {
			open(ctx, nil)
			_, key := create(ctx, "broken", 0, []byte{1})
			pass(ctx)
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, failed(key)))
		})

		It("walks the rules in prefix order, every entry of a rule before the next rule", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "3"})
			rec, _ := create(ctx, "plain", 0, lcAttr(expireRule("a-second", "b/"), expireRule("z-first", "a/")))
			for _, k := range []string{"a/1", "a/2", "a/3", "b/1", "b/2", "b/3"} {
				put(ctx, rec, k, "v", nil)
			}
			clk.Advance(lcDay)
			var mu sync.Mutex
			var order []string
			release := make(chan struct{})
			decided := func() []string {
				mu.Lock()
				defer mu.Unlock()
				return slices.Clone(order)
			}
			h.SetHook(func(_ context.Context, at driver.LCStep, key meta.ObjKey) {
				defer GinkgoRecover()
				if at != driver.LCStepDecided {
					return
				}
				mu.Lock()
				order = append(order, key.Name)
				mu.Unlock()
				if strings.HasPrefix(key.Name, "a/") {
					<-release
				}
			})
			settle(s)
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.Pass(ctx)
			}()
			Eventually(decided).WithTimeout(5*time.Second).WithPolling(time.Millisecond).Should(ConsistOf("a/1", "a/2", "a/3"),
				"the prefix map orders by prefix, not ID")
			Consistently(decided).WithTimeout(50*time.Millisecond).WithPolling(time.Millisecond).Should(HaveLen(3),
				"no b/ entry while an a/ entry runs")
			close(release)
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			Expect(decided()[3:]).To(ConsistOf("b/1", "b/2", "b/3"))
			Expect(listed(ctx, rec)).To(BeEmpty())
		})

		DescribeTable("lists a rule in pages of 1000, waiting rgw_lc_thread_delay after each",
			func(ctx SpecContext, objects, pages int) {
				open(ctx, map[string]string{"rgw_lc_thread_delay": "7"})
				var mu sync.Mutex
				var waits []time.Duration
				h.SetSleep(func(_ context.Context, d time.Duration) error {
					mu.Lock()
					defer mu.Unlock()
					waits = append(waits, d)
					return nil
				})
				rec, _ := create(ctx, "many", 0, lcAttr(expireRule("expire", "")))
				for i := range objects {
					put(ctx, rec, fmt.Sprintf("k%04d", i), "v", nil)
				}
				clk.Advance(lcDay)
				pass(ctx)
				Expect(waits).To(Equal(slices.Repeat([]time.Duration{7 * time.Millisecond}, pages)))
				Expect(notes.NotifyCallCount()).To(Equal(objects))
				Expect(listed(ctx, rec)).To(BeEmpty())
			},
			Entry("1000 objects, one page", 1000, 1),
			Entry("1001 objects, two pages", 1001, 2),
		)

		It("runs at most rgw_lc_max_wp_worker entries at once", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "2"})
			rec, _ := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			for i := range 6 {
				put(ctx, rec, fmt.Sprintf("k%d", i), "v", nil)
			}
			clk.Advance(lcDay)
			var mu sync.Mutex
			inFlight, peak := 0, 0
			release := make(chan struct{})
			running := func() int {
				mu.Lock()
				defer mu.Unlock()
				return inFlight
			}
			h.SetHook(func(_ context.Context, at driver.LCStep, _ meta.ObjKey) {
				defer GinkgoRecover()
				if at != driver.LCStepDecided {
					return
				}
				mu.Lock()
				inFlight++
				peak = max(peak, inFlight)
				mu.Unlock()
				<-release
				mu.Lock()
				inFlight--
				mu.Unlock()
			})
			settle(s)
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.Pass(ctx)
			}()
			Eventually(running).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(Equal(2))
			Consistently(running).WithTimeout(50 * time.Millisecond).WithPolling(time.Millisecond).Should(Equal(2))
			close(release)
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeClosed())
			Expect(peak).To(Equal(2))
			Expect(listed(ctx, rec)).To(BeEmpty())
		})

		DescribeTable("stops at the next page, leaving its objects",
			func(ctx SpecContext, during func(ctx context.Context, key string), left int, want func(key string) rgwcls.LCEntry) {
				open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
				rec, key := create(ctx, "many", 0, lcAttr(expireRule("expire", "")))
				for i := range 1001 {
					put(ctx, rec, fmt.Sprintf("k%04d", i), "v", nil)
				}
				clk.Advance(lcDay)
				var once sync.Once
				h.SetHook(func(ctx context.Context, at driver.LCStep, _ meta.ObjKey) {
					defer GinkgoRecover()
					if at == driver.LCStepDecided {
						once.Do(func() { during(ctx, key) })
					}
				})
				logs := &syncBuffer{}
				DeferCleanup(driver.CaptureLog(logs))
				pass(ctx)
				Expect(listed(ctx, rec)).To(HaveLen(left))
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, want(key)))
				Expect(logs.String()).NotTo(ContainSubstring("could not list"), "the next page is not asked for")
			},
			Entry("once the claim is another worker's, recording nothing",
				func(_ context.Context, key string) {
					c.Object(rookLCPool, rookLCNS, lcShard).Omap[key] = encode(rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Unix()) + 1, Status: rgwcls.LCStatusProcessing}) //nolint:gosec // a time_t after the epoch
				},
				1, // the first page's 1000 handed out before the check
				func(key string) rgwcls.LCEntry {
					return rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Unix()) + 1, Status: rgwcls.LCStatusProcessing} //nolint:gosec // a time_t after the epoch
				}),
			Entry("once thread_stop_at passed, complete, as worker_should_stop's return 0 leaves it",
				func(context.Context, string) { clk.Advance(lcDay + time.Second) },
				1,
				func(key string) rgwcls.LCEntry {
					return rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Add(-lcDay - time.Second).Unix()), Status: rgwcls.LCStatusComplete} //nolint:gosec // a time_t after the epoch
				}),
			Entry("once the bucket's versioning is enabled, failed, as radosgw's pass goes on with the record it loaded, each removal refusing it first",
				func(ctx context.Context, _ string) { setFlags(ctx, "many", meta.BucketVersioned) }, 1001, failed),
			Entry("once its lifecycle configuration changes, failed, each removal refusing it first",
				func(ctx context.Context, _ string) {
					Expect(s.PutBucketAttrs(ctx, reload(ctx, "many"), map[string][]byte{op.AttrLC: lcAttr(expireRule("expire", "other/"))}, nil)).To(Succeed())
				}, 1001, failed),
		)

		It("ends a rule, not the bucket, at a later page that fails, as LCObjsLister does", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_override_bucket_index_max_shards": "1"})
			rec, key := create(ctx, "many", 0, lcAttr(expireRule("a", "a/"), expireRule("b", "b/")))
			for i := range 1001 {
				put(ctx, rec, fmt.Sprintf("a/%04d", i), "v", nil)
			}
			put(ctx, rec, "b/0", "v", nil)
			Expect(rec.Info.Layout.Current.Layout.Normal.NumShards).To(BeEquivalentTo(1))
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(context.Context, driver.LCStep, meta.ObjKey) {
				once.Do(func() { c.FailNextRead(rookIndexPool, "", indexShardOID(rec, "a/0000"), syscall.EIO) })
			})
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			pass(ctx)
			Expect(logs.String()).To(ContainSubstring("lifecycle could not list a rule's next page; the rule ends"))
			Expect(listed(ctx, rec)).To(Equal([]string{"a/1000"}), "rule b ran: its object is gone")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("ends the pass on a bucket gone during it and removes its entry, as for any stale entry", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_cache_enabled": "false"})
			rec, key := create(ctx, "many", 0, lcAttr(expireRule("expire", "")))
			for i := range 1001 {
				put(ctx, rec, fmt.Sprintf("k%04d", i), "v", nil)
			}
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(context.Context, driver.LCStep, meta.ObjKey) {
				once.Do(func() { c.Remove(rookMetaPool, rookRoot, rec.Info.Bucket.EntryPointOID()) })
			})
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			pass(ctx)
			Expect(lcEntries(c)).NotTo(HaveKey(key))
			Expect(logs.String()).To(ContainSubstring("lifecycle skips an expired object of a bucket changed during its pass"), "a bucket gone is a change, not a failed read")
			Expect(logs.String()).NotTo(ContainSubstring("could not read the bucket's record again"))
		})

		It("logs a failed read of the bucket's record before a removal as that read's failure, keeps the object and fails the entry", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_cache_enabled": "false"})
			rec, key := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			put(ctx, rec, "k", "v", nil)
			clk.Advance(lcDay)
			h.SetHook(func(_ context.Context, at driver.LCStep, _ meta.ObjKey) {
				if at == driver.LCStepReread {
					c.FailNextRead(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID(), syscall.EIO)
				}
			})
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			pass(ctx)
			Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
			Expect(logs.String()).To(ContainSubstring("lifecycle could not read the bucket's record again; the expired object stays"))
			Expect(logs.String()).NotTo(ContainSubstring("changed during its pass"))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, failed(key)))
		})

		It("ends the pass on the later page's own stop, a bucket gone, over the change a removal found before it", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1", "rgw_cache_enabled": "false", "rgw_lc_thread_delay": "1"})
			rec, key := create(ctx, "many", 0, lcAttr(expireRule("expire", "")))
			for i := range 1001 {
				put(ctx, rec, fmt.Sprintf("k%04d", i), "v", nil)
			}
			clk.Advance(lcDay)
			var mu sync.Mutex
			changed := false
			h.SetHook(func(ctx context.Context, at driver.LCStep, _ meta.ObjKey) {
				defer GinkgoRecover()
				mu.Lock()
				defer mu.Unlock()
				if at == driver.LCStepDecided && !changed {
					setFlags(ctx, "many", meta.BucketVersioned)
					changed = true
				}
			})
			h.SetSleep(func(context.Context, time.Duration) error {
				mu.Lock()
				defer mu.Unlock()
				if changed {
					c.Remove(rookMetaPool, rookRoot, rec.Info.Bucket.EntryPointOID())
				}
				return nil
			})
			pass(ctx)
			Expect(lcEntries(c)).NotTo(HaveKey(key), "stale, as the second page's read found it, not failed")
		})

		It("leaves the rest of a page unremoved once the bucket's versioning is enabled between two of its entries, and fails the entry", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, key := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			for _, k := range []string{"k0", "k1", "k2"} {
				put(ctx, rec, k, "v", nil)
			}
			clk.Advance(lcDay)
			var removed []string
			notes.NotifyStub = func(ctx context.Context, n lifecycle.Notification) {
				defer GinkgoRecover()
				removed = append(removed, n.Key.Name)
				if len(removed) == 1 {
					setFlags(ctx, "plain", meta.BucketVersioned)
				}
			}
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			pass(ctx)
			Expect(removed).To(HaveLen(1))
			Expect(listed(ctx, rec)).To(ConsistOf(slices.DeleteFunc([]string{"k0", "k1", "k2"}, func(k string) bool { return k == removed[0] })))
			Expect(strings.Count(logs.String(), "lifecycle skips an expired object of a bucket changed during its pass")).To(Equal(1),
				"the next entry's removal finds the change, and the last is dropped unread")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, failed(key)))
		})

		It("checks the lock of a bucket whose object lock is set during the pass, on the object as read again", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, key := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			put(ctx, rec, "free", "v", nil)
			put(ctx, rec, "retained", "v", retention(clk.Now().Add(time.Hour)))
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(ctx context.Context, at driver.LCStep, _ meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepDecided {
					once.Do(func() { setFlags(ctx, "plain", meta.BucketObjLockEnabled) })
				}
			})
			pass(ctx)
			Expect(listed(ctx, rec)).To(Equal([]string{"retained"}), "decided on the page's record, which has no lock, and kept on the record read before its removal")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		DescribeTable("reads the bucket's record again once a page and once a removal, through the metadata cache",
			func(ctx SpecContext, cache string, want int) {
				open(ctx, map[string]string{"rgw_cache_enabled": cache})
				if cache == "true" {
					driver.EnableCacheForTest(s)
				}
				rec, _ := create(ctx, "many", 0, lcAttr(expireRule("expire", "")))
				for i := range 1001 {
					put(ctx, rec, fmt.Sprintf("k%04d", i), "v", nil)
				}
				clk.Advance(lcDay)
				settle(s)
				c.ResetCounters()
				pass(ctx)
				Expect(listed(ctx, rec)).To(BeEmpty())
				Expect(c.Reads(rookMetaPool, rookRoot, rec.Info.Bucket.InstanceOID())).To(Equal(want))
			},
			Entry("with the cache off: the claim's, the pass's, one for each of two pages and one for each removal", "false", 4+1001),
			Entry("with the cache on: none, each served from the record the bucket's writes left in it", "true", 0),
		)
	})

	Describe("on a bucket whose flags carry object lock without BUCKET_VERSIONED", func() {
		var (
			rec *op.BucketRecord
			key string
		)
		BeforeEach(func(ctx SpecContext) {
			open(ctx, nil)
			rec, key = create(ctx, "locked", meta.BucketObjLockEnabled, lcAttr(expireRule("expire", "")))
			Expect(rec.Info.Versioned()).To(BeFalse())
		})

		It("keeps a retained object and a held one, which radosgw's current expiration removes, and removes the others", func(ctx SpecContext) {
			put(ctx, rec, "retained", "v", retention(clk.Now().Add(time.Hour)))
			put(ctx, rec, "held", "v", legalHold("ON"))
			put(ctx, rec, "lapsed", "v", retention(clk.Now()))
			put(ctx, rec, "released", "v", legalHold("OFF"))
			put(ctx, rec, "free", "v", nil)
			clk.Advance(lcDay)
			var mu sync.Mutex
			var decided []string
			h.SetHook(func(_ context.Context, at driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepDecided {
					mu.Lock()
					defer mu.Unlock()
					decided = append(decided, k.Name)
				}
			})
			pass(ctx)
			Expect(listed(ctx, rec)).To(ConsistOf("retained", "held"))
			Expect(decided).To(ConsistOf("lapsed", "released", "free"), "the decision's own lock check, before any read again")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("keeps an object whose retention lands after its listing and before its read again", func(ctx SpecContext) {
			put(ctx, rec, "k", "v", nil)
			clk.Advance(lcDay)
			h.SetHook(func(ctx context.Context, at driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepDecided {
					Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, k.Name), retention(clk.Now().Add(time.Hour)), nil)).To(Succeed())
				}
			})
			pass(ctx)
			Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
		})

		It("keeps an object whose retention lands between its decision and its read again without moving its mtime", func(ctx SpecContext) {
			put(ctx, rec, "k", "v", nil)
			clk.Advance(lcDay)
			h.SetHook(func(ctx context.Context, at driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepDecided {
					setKeepingMtime(ctx, rec, k.Name, retention(clk.Now().Add(time.Hour)))
				}
			})
			pass(ctx)
			Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
		})

		It("fails the removal's version assert on an attr write after the read again, keeping the object, its entry and its count, and decides it afresh at the next pass", func(ctx SpecContext) {
			put(ctx, rec, "k", "v", nil)
			shard := indexShardOID(rec, "k")
			var write map[string][]byte
			h.SetHook(func(ctx context.Context, at driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepReread && write != nil {
					Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, k.Name), write, nil)).To(Succeed())
					write = nil
				}
			})
			cancels := func() int {
				n := 0
				for _, w := range c.WritesTo(rookIndexPool, "", shard) {
					for i, st := range w.Steps() {
						if e, ok := st.(*radosclient.ExecStep); ok && e.Method == "bucket_complete_op" &&
							execIn(w, i, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op == rgwcls.OpCancel {
							n++
						}
					}
				}
				return n
			}
			tagSet := map[string][]byte{tags.Attr: encode(tags.Set{Tags: []tags.Tag{{Key: "project", Value: "x"}}})}
			for _, day := range []struct {
				what     string
				before   map[string][]byte // written before the pass
				after    map[string][]byte // written after the read again
				survives bool
				asserted bool // the removal was sent and refused
			}{
				{what: "a retention written after the read again", after: retention(clk.Now().Add(2*lcDay + lcDay/2)), survives: true, asserted: true},
				{what: "the retention holding it", survives: true},
				{what: "a legal hold written after the read again, the retention lapsed", after: legalHold("ON"), survives: true, asserted: true},
				{what: "the legal hold holding it", survives: true},
				{what: "a tag set alone written after the read again, the hold released", before: legalHold("OFF"), after: tagSet, survives: true, asserted: true},
				{what: "nothing holding it"},
			} {
				clk.Advance(lcDay)
				if day.before != nil {
					Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, "k"), day.before, nil)).To(Succeed(), day.what)
				}
				write = day.after
				settle(s)
				c.ResetCounters()
				pass(ctx)
				Expect(stat(ctx, rec, "k").Exists).To(Equal(day.survives), day.what)
				Expect(cancels()).To(Equal(map[bool]int{true: 1}[day.asserted]), "%s: the prepare's cancel after the refused removal", day.what)
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)), day.what)
				if day.survives {
					Expect(listed(ctx, rec)).To(Equal([]string{"k"}), "%s: the index entry stays", day.what)
					Expect(indexed(rec)).To(BeEquivalentTo(1), "%s: the index's stats, which the quota counts, are left alone", day.what)
					Expect(notes.NotifyCallCount()).To(BeZero(), day.what)
				}
			}
			Expect(listed(ctx, rec)).To(BeEmpty())
			Expect(notes.NotifyCallCount()).To(Equal(1))
		})
	})

	Describe("on a bucket whose versioning is enabled", func() {
		var (
			rec *op.BucketRecord
			key string
		)
		BeforeEach(func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, key = create(ctx, "versioned", meta.BucketVersioned, lcAttr(expireRule("expire", "")))
			Expect(rec.Info.VersioningEnabled()).To(BeTrue())
		})

		It("writes a delete marker over an expired current version, owned by the version's owner, keeps the version noncurrent and notifies", func(ctx SpecContext) {
			bob := meta.UserOwner(meta.UserID{ID: "bob"})
			putAs(ctx, rec, "k", "v1", bob, nil)
			v1 := versionOf(ctx, rec, "k", "v1")
			clk.Advance(lcDay)
			putVersion(ctx, rec, "young", "y1")
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM*", "v1"}))
			Expect(versionOf(ctx, rec, "k", "DM").Owner).To(Equal(bob), "remove_expired_obj's obj_owner, the entry's owner")
			Expect(versions(ctx, rec, "young")).To(Equal([]string{"y1*"}))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			Expect(notes.NotifyCallCount()).To(Equal(1))
			_, note := notes.NotifyArgsForCall(0)
			Expect(note).To(Equal(lifecycle.Notification{
				Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}, ETag: md5hex("v1"), Size: 2, VersionID: v1.Key.Instance,
				Events: []lifecycle.Event{lifecycle.EventExpirationCurrent, lifecycle.EventExpirationDelete},
			}))
		})

		It("writes a delete marker over an object put before versioning was enabled, which stays as the noncurrent null version", func(ctx SpecContext) {
			plain, _ := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
			putVersion(ctx, plain, "k", "old")
			setFlags(ctx, "plain", meta.BucketVersioned)
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, reload(ctx, "plain"), "k")).To(Equal([]string{"DM*", "old@null"}))
		})

		It("writes a delete marker over a null current version, though a radosgw gateway's removal of a pending attr moved its head's mtime", func(ctx SpecContext) {
			putVersion(ctx, rec, "k", "v1")
			setFlags(ctx, "versioned", meta.BucketVersionsSuspended)
			putVersion(ctx, reload(ctx, "versioned"), "k", "null")
			clearFlags(ctx, "versioned", meta.BucketVersionsSuspended)
			rec = reload(ctx, "versioned")
			clk.Advance(time.Second)
			driftHead(ctx, rec, "k")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"null@null*", "v1"}))
			Expect(stat(ctx, rec, "k").Mtime).NotTo(BeTemporally("==", versionOf(ctx, rec, "k", "null").Mtime),
				"the head's mtime, which the removal moved")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM*", "null@null", "v1"}))
		})

		It("writes one delete marker per expired key over two passes", func(ctx SpecContext) {
			putVersion(ctx, rec, "a", "a1")
			putVersion(ctx, rec, "b", "b1")
			clk.Advance(lcDay)
			pass(ctx)
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "a")).To(Equal([]string{"DM*", "a1"}), "a current delete marker an older version follows is kept")
			Expect(versions(ctx, rec, "b")).To(Equal([]string{"DM*", "b1"}))
			Expect(notes.NotifyCallCount()).To(Equal(2))
		})

		DescribeTable("writes no delete marker over a version put after the listing, where radosgw's marker ignores unmod_since",
			func(ctx SpecContext, at driver.LCStep, data string, sameMtime bool) {
				v1At := clk.Now()
				putVersion(ctx, rec, "k", "v1")
				clk.Advance(lcDay)
				var mtime time.Time
				if sameMtime {
					mtime = v1At
				}
				var once sync.Once
				h.SetHook(func(ctx context.Context, step driver.LCStep, k meta.ObjKey) {
					defer GinkgoRecover()
					if step == at {
						once.Do(func() { putAt(ctx, rec, k.Name, data, attrsOwner, nil, mtime) })
					}
				})
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{data + "*", "v1"}))
				Expect(notes.NotifyCallCount()).To(BeZero())
			},
			Entry("before the read again", driver.LCStepDecided, "v2", false),
			Entry("before the read again, with the same body and mtime, which the read again tells by its instance", driver.LCStepDecided, "v1", true),
			Entry("after the read again, at the same mtime, which the marker's If-Match refuses", driver.LCStepReread, "v2", true),
			Entry("after the read again, with the same body, which the marker's If-Match-Last-Modified refuses", driver.LCStepReread, "v1", false),
		)

		DescribeTable("writes no delete marker over a null current version written over after the read again, which the marker's Expect refuses",
			func(ctx SpecContext, write func(ctx context.Context, rec *op.BucketRecord), want []string) {
				plain, _ := create(ctx, "plain", 0, lcAttr(expireRule("expire", "")))
				putVersion(ctx, plain, "k", "old")
				setFlags(ctx, "plain", meta.BucketVersioned)
				plain = reload(ctx, "plain")
				clk.Advance(lcDay)
				var once sync.Once
				h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
					defer GinkgoRecover()
					if step == driver.LCStepReread {
						once.Do(func() { write(ctx, plain) })
					}
				})
				pass(ctx)
				Expect(versions(ctx, plain, "k")).To(Equal(want))
				Expect(notes.NotifyCallCount()).To(BeZero())
			},
			Entry("a PutObject of the same body, a version under a fresh instance, which If-Match passes", func(ctx context.Context, rec *op.BucketRecord) {
				putVersion(ctx, rec, "k", "old")
			}, []string{"old*", "old@null"}),
			Entry("an attr-only write, which If-Match passes too", func(ctx context.Context, rec *op.BucketRecord) {
				Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, "k"), map[string][]byte{meta.AttrMetaPrefix + "note": []byte("n\x00")}, nil)).To(Succeed())
				settle(s)
			}, []string{"old@null*"}),
		)

		DescribeTable("removes a delete marker no version follows",
			func(ctx SpecContext, rule lifecycle.Rule) {
				rec, _ = create(ctx, "markers", meta.BucketVersioned, lcAttr(rule))
				putVersion(ctx, rec, "k", "v1")
				v1 := versionOf(ctx, rec, "k", "v1")
				m := del(ctx, rec, meta.ObjKey{Name: "k"})
				del(ctx, rec, v1.Key)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM*"}))
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(BeEmpty())
				Expect(notes.NotifyCallCount()).To(Equal(1))
				_, note := notes.NotifyArgsForCall(0)
				Expect(note).To(Equal(lifecycle.Notification{
					Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k", Instance: m.VersionID}, VersionID: m.VersionID,
					Events: []lifecycle.Event{"s3:ObjectLifecycle:Expiration:DeleteMarker", "s3:LifecycleExpiration:DeleteMarkerCreated"},
				}))
			},
			Entry("by the current expiration", expireRule("expire", "")),
			Entry("by ExpiredObjectDeleteMarker", lifecycle.Rule{ID: "markers", Status: "Enabled", DMExpiration: true}),
		)

		It("keeps a current delete marker an older version follows, by ExpiredObjectDeleteMarker", func(ctx SpecContext) {
			rec, _ = create(ctx, "markers", meta.BucketVersioned, lcAttr(lifecycle.Rule{ID: "markers", Status: "Enabled", DMExpiration: true}))
			putVersion(ctx, rec, "k", "v1")
			del(ctx, rec, meta.ObjKey{Name: "k"})
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM*", "v1"}))
		})

		It("keeps the delete marker a DELETE writes during the pass over the object that ended a page, which the next page lists ahead of the object", func(ctx SpecContext) {
			rec, key = create(ctx, "converted", 0, lcAttr(lifecycle.Rule{ID: "markers", Status: "Enabled", DMExpiration: true}))
			for i := range 1001 {
				putVersion(ctx, rec, fmt.Sprintf("k%04d", i), "old")
			}
			setFlags(ctx, "converted", meta.BucketVersioned)
			rec = reload(ctx, "converted")
			// The walk's first page, as it lists it: the object it ends on is
			// still plain, written before versioning was enabled.
			first, err := s.ListObjects(ctx, rec, op.ListObjectsParams{AllowUnordered: true, ListVersions: true, MaxKeys: 1000})
			Expect(err).NotTo(HaveOccurred())
			Expect(first.Truncated).To(BeTrue())
			end := first.Entries[len(first.Entries)-1].Key.Name
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(ctx context.Context, step driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				if step == driver.LCStepListed && k.Name == end {
					once.Do(func() { del(ctx, rec, meta.ObjKey{Name: end}) })
				}
			})
			pass(ctx)
			Expect(versions(ctx, rec, end)).To(Equal([]string{"DM*", "old@null"}),
				"the marker, whose next entry is the object's own null version, stays, and the object stays deleted")
			Expect(notes.NotifyCallCount()).To(BeZero())
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("fails, writing no marker, once the bucket's versioning is suspended during the pass", func(ctx SpecContext) {
			putVersion(ctx, rec, "k", "v1")
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
				defer GinkgoRecover()
				if step == driver.LCStepReread {
					once.Do(func() { setFlags(ctx, "versioned", meta.BucketVersionsSuspended) })
				}
			})
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v1*"}), "neither a marker nor the null marker, which the decision did not guard")
			Expect(logs.String()).To(ContainSubstring("the versioning of " + key + " is no longer the pass's"))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, failed(key)))
		})

		It("hands the null version out once though radosgw's listing repair copied its entry to the name's plain key", func(ctx SpecContext) {
			setFlags(ctx, "versioned", meta.BucketVersionsSuspended)
			putVersion(ctx, reload(ctx, "versioned"), "k", "null")
			clearFlags(ctx, "versioned", meta.BucketVersionsSuspended)
			rec = reload(ctx, "versioned")
			putVersion(ctx, rec, "k", "v1")
			shard := indexShardOID(rec, "k")
			null, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k"})
			Expect(ok).To(BeTrue(), "the null version's instance entry")
			c.Object(rookIndexPool, "", shard).Omap["k"] = encode(null)
			listed := versionIDs(listVersions(ctx, rec, "k"))
			clk.Advance(lcDay)
			var mu sync.Mutex
			var handed []string
			h.SetHook(func(_ context.Context, step driver.LCStep, k meta.ObjKey) {
				if step == driver.LCStepListed {
					mu.Lock()
					defer mu.Unlock()
					handed = append(handed, k.Name+"@"+orNull(k.Instance))
				}
			})
			v1 := "k@" + versionOf(ctx, rec, "k", "v1").Key.Instance
			Expect(listed).To(Equal([]string{"k@null", v1, "k@null"}), "the copy at the plain key lists first, at the null version's versioned_epoch")
			pass(ctx)
			Expect(handed).To(Equal([]string{v1, "k@null"}), "each version once, at its own place: the copy, out of key order, passed over")
		})

		It("hands each version of a key out once when the null delete marker removes the null version that ends a page", func(ctx SpecContext) {
			rec, key = create(ctx, "many", meta.BucketVersioned, lcAttr(expireRule("expire", "k")))
			putVersion(ctx, rec, "k", "o1")
			putVersion(ctx, rec, "k", "o2")
			setFlags(ctx, "many", meta.BucketVersionsSuspended)
			putVersion(ctx, reload(ctx, "many"), "k", "null")
			clearFlags(ctx, "many", meta.BucketVersionsSuspended)
			rec = reload(ctx, "many")
			for i := range 999 {
				putVersion(ctx, rec, "k", fmt.Sprintf("n%03d", i))
			}
			setFlags(ctx, "many", meta.BucketVersionsSuspended)
			rec = reload(ctx, "many")
			listed := versionIDs(listVersions(ctx, rec, "k"))
			Expect(listed).To(HaveLen(1002))
			Expect(listed[999]).To(Equal("k@null"), "the null version ends the first page")
			clk.Advance(lcDay)
			var mu sync.Mutex
			var handed []string
			h.SetHook(func(_ context.Context, step driver.LCStep, k meta.ObjKey) {
				if step == driver.LCStepListed {
					mu.Lock()
					defer mu.Unlock()
					handed = append(handed, k.Name+"@"+orNull(k.Instance))
				}
			})
			pass(ctx)
			Expect(handed).To(Equal(listed), "every version once, in the listing's order")
			Expect(versions(ctx, rec, "k")[:2]).To(Equal([]string{"DM@null*", "n998"}))
			Expect(versions(ctx, rec, "k")).NotTo(ContainElement("null@null"), "the null version, replaced")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})
	})

	Describe("on a bucket whose versioning is suspended", func() {
		BeforeEach(func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
		})
		// suspended is the bucket name with versioning enabled for its
		// versions' bodies, then suspended for nulls, where "" is none.
		suspended := func(ctx context.Context, name string, flags uint32, bodies ...string) *op.BucketRecord {
			GinkgoHelper()
			rec, _ := create(ctx, name, meta.BucketVersioned|flags, lcAttr(expireRule("expire", "")))
			for _, b := range bodies {
				putVersion(ctx, rec, "k", b)
			}
			setFlags(ctx, name, meta.BucketVersionsSuspended)
			return reload(ctx, name)
		}

		DescribeTable("writes the null delete marker, which replaces the null version and keeps any other as noncurrent",
			func(ctx SpecContext, bodies []string, null bool, want []string) {
				rec := suspended(ctx, "suspended", 0, bodies...)
				if null {
					putVersion(ctx, rec, "k", "null")
				}
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal(want))
				Expect(notes.NotifyCallCount()).To(Equal(1))
				_, note := notes.NotifyArgsForCall(0)
				Expect(note.Events).To(Equal([]lifecycle.Event{lifecycle.EventExpirationCurrent, lifecycle.EventExpirationDelete}))
			},
			Entry("the null version current, put before any version", nil, true, []string{"DM@null*"}),
			Entry("the null version current over a noncurrent version", []string{"v1"}, true, []string{"DM@null*", "v1"}),
			Entry("a version current, no null version", []string{"v1"}, false, []string{"DM@null*", "v1"}),
		)

		It("replaces a noncurrent null version too, as the null delete marker does wherever the null version sits", func(ctx SpecContext) {
			rec, _ := create(ctx, "suspended", 0, lcAttr(expireRule("expire", "")))
			putVersion(ctx, rec, "k", "old")
			setFlags(ctx, "suspended", meta.BucketVersioned)
			putVersion(ctx, reload(ctx, "suspended"), "k", "v1")
			setFlags(ctx, "suspended", meta.BucketVersionsSuspended)
			rec = reload(ctx, "suspended")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v1*", "old@null"}))
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM@null*", "v1"}))
		})

		It("writes the null delete marker over a null current version, though a radosgw gateway's removal of a pending attr moved its head's mtime", func(ctx SpecContext) {
			rec := suspended(ctx, "suspended", 0, "v1")
			putVersion(ctx, rec, "k", "null")
			clk.Advance(time.Second)
			driftHead(ctx, rec, "k")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"null@null*", "v1"}))
			Expect(stat(ctx, rec, "k").Mtime).NotTo(BeTemporally("==", versionOf(ctx, rec, "k", "null").Mtime),
				"the head's mtime, which the removal moved")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM@null*", "v1"}))
		})

		It("removes a null delete marker that took a slow completion's pending op from the null entry it replaced, though its listing shows the OLH head's mtime", func(ctx SpecContext) {
			ac = &aroundCluster{Cluster: c}
			DeferCleanup(func() { ac.set(nil) })
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, _ := create(ctx, "markers", meta.BucketVersioned|meta.BucketVersionsSuspended,
				lcAttr(lifecycle.Rule{ID: "markers", Status: "Enabled", DMExpiration: true}))
			arrived, held := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(held) })
			DeferCleanup(release)
			ac.set(onFirst("bucket_complete_op", func(send sendFunc) (uint64, error) {
				close(arrived)
				<-held
				return send()
			}))
			put := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				defer close(put)
				putVersion(ctx, rec, "k", "null")
			}()
			Eventually(arrived).WithTimeout(5 * time.Second).Should(BeClosed())
			clk.Advance(10 * time.Second)
			m := del(ctx, rec, meta.ObjKey{Name: "k"})
			release()
			Eventually(put).WithTimeout(5 * time.Second).Should(BeClosed())
			ac.set(nil)
			listed := versionOf(ctx, rec, "k", "DM")
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k", Instance: m.VersionID})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.VersionedEpoch).To(Equal(listed.VersionedEpoch))
			Expect(st.Mtime).NotTo(BeTemporally("==", listed.Mtime),
				"the marker keeps the PUT's pending op, so the listing reports it with the OLH head's state")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(BeEmpty())
			Expect(notes.NotifyCallCount()).To(Equal(1))
		})

		It("keeps a retained null version, writing no null delete marker", func(ctx SpecContext) {
			rec := suspended(ctx, "locked", meta.BucketObjLockEnabled)
			putAs(ctx, rec, "k", "null", attrsOwner, retention(clk.Now().Add(time.Hour)))
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"null@null*"}))
			Expect(notes.NotifyCallCount()).To(BeZero())
		})

		It("keeps a retained noncurrent null version under a version current, writing no null delete marker, on a bucket whose flags carry lock and suspension", func(ctx SpecContext) {
			rec, key := create(ctx, "locked", meta.BucketObjLockEnabled, lcAttr(expireRule("expire", "")))
			putAs(ctx, rec, "k", "old", attrsOwner, retention(clk.Now().Add(time.Hour)))
			setFlags(ctx, "locked", meta.BucketVersioned)
			putVersion(ctx, reload(ctx, "locked"), "k", "v1")
			setFlags(ctx, "locked", meta.BucketVersionsSuspended)
			rec = reload(ctx, "locked")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v1*", "old@null"}))
			clk.Advance(lcDay)
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureDebugLog(logs))
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v1*", "old@null"}))
			Expect(logs.String()).To(ContainSubstring("lifecycle keeps a locked null version and writes no null delete marker"))
			Expect(notes.NotifyCallCount()).To(BeZero())
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		DescribeTable("keeps a null version written after the listing",
			func(ctx SpecContext, at driver.LCStep, write func(ctx context.Context, rec *op.BucketRecord), want []string) {
				rec := suspended(ctx, "suspended", 0, "v1")
				putVersion(ctx, rec, "k", "null")
				clk.Advance(lcDay)
				var once sync.Once
				h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
					defer GinkgoRecover()
					if step == at {
						once.Do(func() { write(ctx, rec) })
					}
				})
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal(want))
				Expect(notes.NotifyCallCount()).To(BeZero())
			},
			Entry("a null PutObject before the read again", driver.LCStepDecided,
				func(ctx context.Context, rec *op.BucketRecord) { putVersion(ctx, rec, "k", "rewritten") }, []string{"rewritten@null*", "v1"}),
			Entry("a null PutObject of the same body before the read again, which the null version's index entry tells", driver.LCStepDecided,
				func(ctx context.Context, rec *op.BucketRecord) { putVersion(ctx, rec, "k", "null") }, []string{"null@null*", "v1"}),
			Entry("a null PutObject after the read again", driver.LCStepReread,
				func(ctx context.Context, rec *op.BucketRecord) { putVersion(ctx, rec, "k", "rewritten") }, []string{"rewritten@null*", "v1"}),
			Entry("an attr-only write after the read again, which the marker's Expect refuses", driver.LCStepReread,
				func(ctx context.Context, rec *op.BucketRecord) {
					Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, "k"), map[string][]byte{meta.AttrMetaPrefix + "note": []byte("n\x00")}, nil)).To(Succeed())
				}, []string{"null@null*", "v1"}),
		)

		It("keeps a null version whose retention lands after its listing", func(ctx SpecContext) {
			rec := suspended(ctx, "locked", meta.BucketObjLockEnabled, "v1")
			putVersion(ctx, rec, "k", "null")
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
				defer GinkgoRecover()
				if step == driver.LCStepDecided {
					once.Do(func() {
						Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, "k"), retention(clk.Now().Add(time.Hour)), nil)).To(Succeed())
					})
				}
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"null@null*", "v1"}))
		})

		DescribeTable("keeps the null delete marker a second delete replaced since its listing, and removes it at the next pass",
			func(ctx SpecContext, at driver.LCStep, sameMtime bool) {
				rec := suspended(ctx, "suspended", 0)
				putVersion(ctx, rec, "k", "null")
				del(ctx, rec, meta.ObjKey{Name: "k"})
				first := versionOf(ctx, rec, "k", "DM")
				clk.Advance(lcDay)
				p := op.DeleteParams{}
				if sameMtime {
					p.Mtime = first.Mtime
				}
				var once sync.Once
				h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
					defer GinkgoRecover()
					if step == at {
						once.Do(func() {
							_, err := s.DeleteObject(ctx, rec, meta.ObjKey{Name: "k"}, p)
							Expect(err).NotTo(HaveOccurred())
							settle(s)
						})
					}
				})
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM@null*"}))
				Expect(versionOf(ctx, rec, "k", "DM").VersionedEpoch).To(BeNumerically(">", first.VersionedEpoch), "the second delete's marker")
				Expect(notes.NotifyCallCount()).To(BeZero())
				h.SetHook(nil)
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(BeEmpty())
			},
			Entry("before the read again", driver.LCStepDecided, false),
			Entry("before the read again, at the first's mtime, which the read again tells by the versioned_epoch", driver.LCStepDecided, true),
			Entry("after the read again", driver.LCStepReread, false),
			Entry("after the read again, at the first's mtime, which only the removal's Expect tells by the versioned_epoch", driver.LCStepReread, true),
		)
	})

	Describe("the noncurrent expiration", func() {
		// noncurrentRule expires a version a lifecycle day after it went
		// noncurrent, keeping the newer newest noncurrent versions.
		noncurrentRule := func(newer string) lifecycle.Rule {
			return lifecycle.Rule{ID: "noncurrent", Status: "Enabled", NoncurExpiration: lifecycle.Expiration{Days: "1", NewerNoncurrent: newer}}
		}
		// versioned is a bucket with flags, its versioning enabled, under
		// rule, with the bodies put to k, oldest first.
		versioned := func(ctx context.Context, flags uint32, rule lifecycle.Rule, bodies ...string) (*op.BucketRecord, string) {
			GinkgoHelper()
			rec, key := create(ctx, "versioned", meta.BucketVersioned|flags, lcAttr(rule))
			for _, b := range bodies {
				putVersion(ctx, rec, "k", b)
			}
			return rec, key
		}

		DescribeTable("removes the noncurrent versions past NewerNoncurrentVersions, each by its instance",
			func(ctx SpecContext, newer string, want []string) {
				open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
				rec, key := versioned(ctx, 0, noncurrentRule(newer), "v1", "v2", "v3", "v4")
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal(want))
				Expect(notes.NotifyCallCount()).To(Equal(4 - len(want)))
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			},
			Entry("NewerNoncurrentVersions 0", "0", []string{"v4*"}),
			Entry("NewerNoncurrentVersions 1", "1", []string{"v4*", "v3"}),
			Entry("NewerNoncurrentVersions 2", "2", []string{"v4*", "v3", "v2"}),
		)

		DescribeTable("notifies a removal with the release's events",
			func(ctx SpecContext, r denc.Release, events []lifecycle.Event) {
				rel = &r
				open(ctx, nil)
				rec, _ := versioned(ctx, 0, noncurrentRule("0"), "v1", "v2")
				v1 := versionOf(ctx, rec, "k", "v1")
				clk.Advance(lcDay)
				pass(ctx)
				Expect(notes.NotifyCallCount()).To(Equal(1))
				_, note := notes.NotifyArgsForCall(0)
				Expect(note).To(Equal(lifecycle.Notification{
					Bucket: rec.Info.Bucket, Key: v1.Key, ETag: md5hex("v1"), Size: 2, VersionID: v1.Key.Instance, Events: events,
				}))
			},
			Entry("on Squid, ObjectExpirationNoncurrent alone", denc.Squid, []lifecycle.Event{"s3:ObjectLifecycle:Expiration:NonCurrent"}),
			Entry("on Tentacle, LifecycleExpirationDelete with it", denc.Tentacle,
				[]lifecycle.Event{"s3:LifecycleExpiration:Delete", "s3:ObjectLifecycle:Expiration:NonCurrent"}),
		)

		It("removes a noncurrent null version by the name null and a noncurrent delete marker", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, _ := create(ctx, "versioned", 0, lcAttr(noncurrentRule("0")))
			putVersion(ctx, rec, "k", "old")
			setFlags(ctx, "versioned", meta.BucketVersioned)
			rec = reload(ctx, "versioned")
			putVersion(ctx, rec, "k", "v1")
			del(ctx, rec, meta.ObjKey{Name: "k"})
			putVersion(ctx, rec, "k", "v2")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*", "DM", "v1", "old@null"}))
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*"}))
			var removed []meta.ObjKey
			for i := range notes.NotifyCallCount() {
				_, note := notes.NotifyArgsForCall(i)
				removed = append(removed, note.Key)
			}
			Expect(removed).To(ContainElement(meta.ObjKey{Name: "k", Instance: meta.NullInstance}))
		})

		It("keeps a held noncurrent version, which the lock check refuses", func(ctx SpecContext) {
			open(ctx, nil)
			rec, _ := versioned(ctx, meta.BucketObjLockEnabled, noncurrentRule("0"))
			putAs(ctx, rec, "k", "v1", attrsOwner, legalHold("ON"))
			putVersion(ctx, rec, "k", "v2")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*", "v1"}))
			Expect(notes.NotifyCallCount()).To(BeZero())
		})

		It("checks the lock of a bucket whose object lock is set during the pass, on the version as read again", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, _ := versioned(ctx, 0, noncurrentRule("0"))
			putAs(ctx, rec, "k", "v1", attrsOwner, retention(clk.Now().Add(time.Hour)))
			putVersion(ctx, rec, "k", "v2")
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
				defer GinkgoRecover()
				if step == driver.LCStepDecided {
					once.Do(func() { setFlags(ctx, "versioned", meta.BucketObjLockEnabled) })
				}
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*", "v1"}), "decided on the page's record, which has no lock, and kept on the record read before its removal")
		})

		DescribeTable("keeps a noncurrent version whose retention lands after its listing",
			func(ctx SpecContext, at driver.LCStep) {
				open(ctx, nil)
				rec, _ := versioned(ctx, meta.BucketObjLockEnabled, noncurrentRule("0"), "v1", "v2")
				v1 := versionOf(ctx, rec, "k", "v1")
				clk.Advance(lcDay)
				var once sync.Once
				h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
					defer GinkgoRecover()
					if step == at {
						once.Do(func() {
							st, err := s.StatObject(ctx, rec, v1.Key)
							Expect(err).NotTo(HaveOccurred())
							Expect(s.SetObjectAttrs(ctx, st, retention(clk.Now().Add(time.Hour)), nil)).To(Succeed())
						})
					}
				})
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*", "v1"}))
				Expect(notes.NotifyCallCount()).To(BeZero())
			},
			Entry("before the read again", driver.LCStepDecided),
			Entry("after the read again, which the removal's Expect refuses", driver.LCStepReread),
		)

		DescribeTable("keeps a null version written after the listing, where radosgw's removal of a named version takes no condition",
			func(ctx SpecContext, at driver.LCStep, write func(ctx context.Context, rec *op.BucketRecord), want []string) {
				open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
				rec, _ := create(ctx, "suspended", 0, lcAttr(noncurrentRule("0")))
				putVersion(ctx, rec, "k", "old")
				setFlags(ctx, "suspended", meta.BucketVersioned)
				putVersion(ctx, reload(ctx, "suspended"), "k", "v1")
				setFlags(ctx, "suspended", meta.BucketVersionsSuspended)
				rec = reload(ctx, "suspended")
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"v1*", "old@null"}))
				clk.Advance(lcDay)
				var once sync.Once
				h.SetHook(func(ctx context.Context, step driver.LCStep, _ meta.ObjKey) {
					defer GinkgoRecover()
					if step == at {
						once.Do(func() { write(ctx, rec) })
					}
				})
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal(want))
				Expect(notes.NotifyCallCount()).To(BeZero())
			},
			Entry("a null PutObject before the read again", driver.LCStepDecided,
				func(ctx context.Context, rec *op.BucketRecord) { putVersion(ctx, rec, "k", "rewritten") }, []string{"rewritten@null*", "v1"}),
			Entry("a null PutObject of the same body before the read again, which the null version's index entry tells", driver.LCStepDecided,
				func(ctx context.Context, rec *op.BucketRecord) { putVersion(ctx, rec, "k", "old") }, []string{"old@null*", "v1"}),
			Entry("a null PutObject after the read again, which the removal's Expect refuses", driver.LCStepReread,
				func(ctx context.Context, rec *op.BucketRecord) { putVersion(ctx, rec, "k", "rewritten") }, []string{"rewritten@null*", "v1"}),
			Entry("an attr-only write after the read again, which the removal's Expect refuses", driver.LCStepReread,
				func(ctx context.Context, rec *op.BucketRecord) {
					st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: "k", Instance: meta.NullInstance})
					Expect(err).NotTo(HaveOccurred())
					Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrMetaPrefix + "note": []byte("n\x00")}, nil)).To(Succeed())
				}, []string{"v1*", "old@null"}),
		)

		It("keeps a noncurrent version whose retention lands between its unlink and its removal, listed again at its own epoch, until the retention lapses", func(ctx SpecContext) {
			ac = &aroundCluster{Cluster: c}
			hooks := &writeHooks{}
			ac.set(hooks.around)
			DeferCleanup(func() { ac.set(nil) })
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rec, key := versioned(ctx, meta.BucketObjLockEnabled, noncurrentRule("0"), "v1", "v2")
			v1 := versionOf(ctx, rec, "k", "v1")
			clk.Advance(lcDay)
			until := clk.Now().Add(lcDay + lcDay/2)
			hooks.add(&writeHook{
				match: func(w *radosclient.WriteOp) bool { return callsMethod(w.Steps(), "bucket_unlink_instance") },
				n:     1,
				fn: func() {
					defer GinkgoRecover()
					st, err := s.StatObject(ctx, rec, v1.Key)
					Expect(err).NotTo(HaveOccurred())
					Expect(s.SetObjectAttrs(ctx, st, retention(until), nil)).To(Succeed())
				},
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*", "v1"}))
			again := versionOf(ctx, rec, "k", "v1")
			Expect(again.VersionedEpoch).To(Equal(v1.VersionedEpoch), "listed again at its own epoch")
			Expect(again.IsLatest).To(BeFalse())
			_, instance := c.InstanceEntry(rookIndexPool, "", indexShardOID(rec, "k"), rgwcls.ObjKey{Name: "k", Instance: v1.Key.Instance})
			Expect(instance).To(BeTrue(), "its instance entry")
			Expect(stat(ctx, rec, "k").Key.Instance).NotTo(Equal(v1.Key.Instance), "v2 current")
			st, err := s.StatObject(ctx, rec, v1.Key)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeTrue(), "its head")
			Expect(notes.NotifyCallCount()).To(BeZero())
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))

			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*", "v1"}), "kept while retained")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v2*"}), "removed once the retention lapsed")
		})

		It("removes every expired version of a key of more than one page, handing each out once", func(ctx SpecContext) {
			ac = &aroundCluster{Cluster: c}
			hooks := &writeHooks{}
			ac.set(hooks.around)
			DeferCleanup(func() { ac.set(nil) })
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rule := noncurrentRule("0")
			rule.Filter.Prefix = "k"
			rec, key := create(ctx, "many", meta.BucketVersioned, lcAttr(rule))
			for i := range 1500 {
				putVersion(ctx, rec, "k", fmt.Sprintf("v%04d", i))
			}
			listed := versionIDs(listVersions(ctx, rec, "k"))
			Expect(listed).To(HaveLen(1500))
			// The removal of the first page's last version answers its DEL
			// completion at once and lands it only once its next page is
			// chosen, as another gateway's completion that has not reached the
			// shard looks to rgw-go: until then the version's instance entry
			// outlasts its list entry. The completion lands before that page
			// is listed.
			last := strings.TrimPrefix(listed[999], "k@")
			var send sendFunc
			arrived := make(chan struct{})
			hooks.add(&writeHook{
				match: func(w *radosclient.WriteOp) bool {
					for _, st := range w.Steps() {
						if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_complete_op" {
							co := rgwcls.DecodeCompleteOp(denc.NewDecoder(x.In))
							return co.Op == rgwcls.OpDel && co.Key.Instance == last
						}
					}
					return false
				},
				n: 1, late: func(sf sendFunc) {
					send = sf
					close(arrived)
				},
			})
			// land waits for the completion to arrive, which one sent unawaited
			// does from the completion manager's goroutine.
			land := sync.OnceValue(func() error {
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					return errors.New("the held DEL completion never came")
				}
				_, err := send()
				return err
			})
			DeferCleanup(land)
			clk.Advance(lcDay)
			var mu sync.Mutex
			var handed []string
			var drained []int
			h.SetHook(func(_ context.Context, step driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				mu.Lock()
				defer mu.Unlock()
				switch step {
				case driver.LCStepListed:
					handed = append(handed, k.Name+"@"+orNull(k.Instance))
				case driver.LCStepPaged:
					drained = append(drained, notes.NotifyCallCount())
					Expect(land()).To(Succeed())
					settle(s)
				}
			})
			pass(ctx)
			Expect(drained).To(Equal([]int{999}), "the first page's work done before its next page is chosen: every noncurrent version removed")
			Expect(handed).To(Equal(listed), "every version once, in the listing's order")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"v1499*"}), "none of the second page's versions skipped")
			Expect(notes.NotifyCallCount()).To(Equal(1499))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("counts no null delete marker the pass wrote toward NewerNoncurrentVersions when it lists the marker's key again", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1", "rgw_override_bucket_index_max_shards": "1"})
			rule := noncurrentRule("2")
			rule.Expiration = lifecycle.Expiration{Days: "1"}
			rec, key := create(ctx, "counted", meta.BucketVersioned, lcAttr(rule))
			putVersion(ctx, rec, "k", "v1")
			putVersion(ctx, rec, "k", "v2")
			setFlags(ctx, "counted", meta.BucketVersionsSuspended)
			rec = reload(ctx, "counted")
			putVersion(ctx, rec, "k", "null")
			clk.Advance(2 * lcDay)
			// 999 young objects ahead of k, so that k's null version, which
			// the null delete marker replaces, ends the first page.
			for i := range 999 {
				putVersion(ctx, rec, fmt.Sprintf("a%03d", i), "young")
			}
			first, err := s.ListObjects(ctx, rec, op.ListObjectsParams{AllowUnordered: true, ListVersions: true, MaxKeys: 1000})
			Expect(err).NotTo(HaveOccurred())
			Expect(versionIDs(first.Entries[999:])).To(Equal([]string{"k@null"}), "the null version ends the first page")
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM@null*", "v2", "v1"}),
				"v2 and v1 have one and two newer versions, the null version and v2; the marker, written since, waits for the next pass")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("lists a key again from its first version once a page's work removed every version of it the page handed out", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rule := noncurrentRule("")
			rule.Filter.Prefix = "k"
			rule.Expiration = lifecycle.Expiration{Days: "1"}
			rec, key := create(ctx, "gone", meta.BucketVersioned, lcAttr(rule))
			for i := range 1500 {
				putVersion(ctx, rec, "k", fmt.Sprintf("v%04d", i))
			}
			setFlags(ctx, "gone", meta.BucketVersionsSuspended)
			rec = reload(ctx, "gone")
			putVersion(ctx, rec, "k", "null")
			listed := versionIDs(listVersions(ctx, rec, "k"))
			Expect(listed).To(HaveLen(1501))
			Expect(listed[0]).To(Equal("k@null"), "the null version current, which the null delete marker replaces")
			clk.Advance(2 * lcDay)
			var mu sync.Mutex
			var handed []string
			h.SetHook(func(_ context.Context, step driver.LCStep, k meta.ObjKey) {
				if step == driver.LCStepListed {
					mu.Lock()
					defer mu.Unlock()
					handed = append(handed, k.Name+"@"+orNull(k.Instance))
				}
			})
			pass(ctx)
			Expect(handed).To(Equal(listed), "every version once, in the listing's order")
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"DM@null*"}), "none of the versions past the first page skipped")
			Expect(notes.NotifyCallCount()).To(Equal(1501))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("resumes after the newest version of a key it keeps, not at the key's first version, once a page's work removed the version it ends on", func(ctx SpecContext) {
			open(ctx, map[string]string{"rgw_lc_max_wp_worker": "1"})
			rule := noncurrentRule("")
			rule.Filter = lifecycle.Filter{Prefix: "k", SizeGT: "1"}
			rec, key := create(ctx, "kept", meta.BucketVersioned, lcAttr(rule))
			// Newest first: the current version, 1998 versions the size filter
			// keeps, then 1002 it expires. The second page ends on the first of
			// those, the third holds a thousand more, and the fourth the last.
			for range 1002 {
				putVersion(ctx, rec, "k", "rr")
			}
			for range 1998 {
				putVersion(ctx, rec, "k", "k")
			}
			putVersion(ctx, rec, "k", "current")
			listed := versionIDs(listVersions(ctx, rec, "k"))
			Expect(listed).To(HaveLen(3001))
			clk.Advance(2 * lcDay)
			var mu sync.Mutex
			var handed []string
			pages := 0
			h.SetHook(func(_ context.Context, step driver.LCStep, k meta.ObjKey) {
				mu.Lock()
				defer mu.Unlock()
				switch step {
				case driver.LCStepListed:
					handed = append(handed, k.Name+"@"+orNull(k.Instance))
				case driver.LCStepPaged:
					pages++
				}
			})
			pass(ctx)
			Expect(handed).To(Equal(listed), "every version once, in the listing's order")
			Expect(pages).To(Equal(3), "four pages: the second resumes after the last version it keeps, the third after that one again, listing none of the kept versions again")
			Expect(versionIDs(listVersions(ctx, rec, "k"))).To(HaveLen(1999))
			Expect(notes.NotifyCallCount()).To(Equal(1002))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})
	})

	Describe("the claim predicate", func() {
		var (
			// claimable is the buckets rgw-go claims, and claimed their
			// entries' keys; refused is the others' keys, in the shard's
			// order, and reasons why each is refused.
			claimable        []*op.BucketRecord
			claimed, refused []string
			reasons          map[string]string
		)
		BeforeEach(func(ctx SpecContext) {
			tier := func(zg *meta.ZoneGroup) {
				t := zg.PlacementTargets["default-placement"]
				t.TierTargets = map[string]meta.ZoneGroupPlacementTier{"CLOUDY": {TierType: meta.TierTypeCloudS3, StorageClass: "CLOUDY"}}
				zg.PlacementTargets["default-placement"] = t
			}
			editRoot(c, meta.ZoneGroupInfoOID("zg-ceph-objectstore"), meta.DecodeZoneGroup, tier)
			editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
				zg := p.PeriodMap.ZoneGroups["zg-ceph-objectstore"]
				tier(&zg)
				p.PeriodMap.ZoneGroups["zg-ceph-objectstore"] = zg
			})
			open(ctx, nil)
			transition := expireRule("transition", "")
			transition.Transitions = map[string]lifecycle.Transition{"COLD": {Days: "30", StorageClass: "COLD"}}
			cloud := expireRule("cloud", "")
			cloud.Transitions = map[string]lifecycle.Transition{"CLOUDY": {Days: "30", StorageClass: "CLOUDY"}}
			noncurrent := expireRule("noncurrent", "")
			noncurrent.NoncurExpiration = lifecycle.Expiration{Days: "1"}
			claimable, claimed, refused, reasons = nil, nil, nil, map[string]string{}
			for _, b := range []struct {
				name   string
				flags  uint32
				rule   lifecycle.Rule
				reason string
			}{
				{name: "a-plain", rule: expireRule("expire", "")},
				{name: "b-versioned", flags: meta.BucketVersioned, rule: expireRule("expire", "")},
				{name: "c-suspended", flags: meta.BucketVersioned | meta.BucketVersionsSuspended, rule: expireRule("expire", "")},
				{name: "d-noncurrent", flags: meta.BucketVersioned, rule: noncurrent},
				{name: "e-transition", rule: transition, reason: "rule transitions objects"},
				{name: "f-cloud", rule: cloud, reason: "rule transitions to a cloud tier"},
				{name: "g-plain", rule: expireRule("expire", "")},
			} {
				rec, key := create(ctx, b.name, b.flags, lcAttr(b.rule))
				if b.reason == "" {
					claimable, claimed = append(claimable, rec), append(claimed, key)
					continue
				}
				refused, reasons[key] = append(refused, key), b.reason
			}
		})
		// day puts an object in each bucket rgw-go may claim and passes a
		// lifecycle day later. It expects those objects expired, removed or
		// behind a delete marker, those buckets complete, the others'
		// entries never claimed and each of them logged once, and returns the
		// pass's log.
		day := func(ctx context.Context) string {
			GinkgoHelper()
			for _, rec := range claimable {
				putVersion(ctx, rec, "k", "v")
			}
			clk.Advance(lcDay)
			logs := &syncBuffer{}
			restore := driver.CaptureLog(logs)
			pass(ctx)
			restore()
			entries := lcEntries(c)
			for _, key := range claimed {
				Expect(entries).To(HaveKeyWithValue(key, completed(key)))
			}
			for _, key := range refused {
				Expect(entries).To(HaveKeyWithValue(key, rgwcls.LCEntry{Bucket: key}), "never claimed")
			}
			for _, rec := range claimable {
				Expect(stat(ctx, rec, "k").Exists).To(BeFalse(), rec.Info.Bucket.Name)
			}
			Expect(strings.Count(logs.String(), "lifecycle leaves a bucket for radosgw's worker")).To(Equal(len(refused)), "logged once a pass each:\n%s", logs.String())
			return logs.String()
		}

		It("claims a bucket ever versioned, its noncurrent rules too, and leaves a transition rule's and a cloud tier's to radosgw's worker, which claims them where rgw-go left the head", func(ctx SpecContext) {
			logged := day(ctx)
			for key, reason := range reasons {
				Expect(logged).To(ContainSubstring(fmt.Sprintf(`"bucket":%q,"reason":%q`, key, reason)))
			}
			s2, err := driver.Open(ctx, driver.LCClientForTest(c, "client.2"), conf(map[string]string{"rgw_lc_debug_interval": "10", "rgw_lc_max_objs": "1"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(s2.Close)
			var byRadosgw []string
			s2.RadosgwShardPassForTest(ctx, clk.Now, 0, 0, func(key string) error {
				byRadosgw = append(byRadosgw, key)
				return nil
			})
			Expect(byRadosgw).To(Equal(refused), "radosgw's walk from the head rgw-go left at the first of them, the others done today")
			for _, key := range refused {
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			}
		})

		It("leaves them unprocessed in an rgw-go-only zone, and processes every other bucket of their shard on every pass", func(ctx SpecContext) {
			for range 3 {
				day(ctx)
			}
		})
	})

	Describe("the claim predicate's reading of a configuration", func() {
		BeforeEach(func(ctx SpecContext) {
			tier := func(zg *meta.ZoneGroup) {
				t := zg.PlacementTargets["default-placement"]
				t.TierTargets = map[string]meta.ZoneGroupPlacementTier{
					"CLOUDY":  {TierType: meta.TierTypeCloudS3, StorageClass: "CLOUDY"},
					"GLACIER": {TierType: meta.TierTypeCloudS3Glacier, StorageClass: "GLACIER"},
				}
				zg.PlacementTargets["default-placement"] = t
			}
			editRoot(c, meta.ZoneGroupInfoOID("zg-ceph-objectstore"), meta.DecodeZoneGroup, tier)
			editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
				zg := p.PeriodMap.ZoneGroups["zg-ceph-objectstore"]
				tier(&zg)
				p.PeriodMap.ZoneGroups["zg-ceph-objectstore"] = zg
			})
			open(ctx, nil)
		})
		transitionTo := func(rule lifecycle.Rule, class string) lifecycle.Rule {
			rule.Transitions = map[string]lifecycle.Transition{class: {Days: "30", StorageClass: class}}
			return rule
		}

		DescribeTable("leaves a bucket whose rules transition to a cloud tier of either type, on either release",
			func(ctx SpecContext, class string) {
				_, key := create(ctx, "clouded", 0, lcAttr(transitionTo(expireRule("cloud", ""), class)))
				logs := &syncBuffer{}
				DeferCleanup(driver.CaptureLog(logs))
				pass(ctx)
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, rgwcls.LCEntry{Bucket: key}))
				Expect(logs.String()).To(ContainSubstring(fmt.Sprintf(`"bucket":%q,"reason":"rule transitions to a cloud tier","rule":"cloud"`, key)))
			},
			Entry("cloud-s3", "CLOUDY"),
			Entry("cloud-s3-glacier, which v20.2.4 tiers to a cloud and v19.2.7 does not", "GLACIER"),
		)

		DescribeTable("claims a bucket whose transition rule the zone does not run, and applies its other rules",
			func(ctx SpecContext, transition lifecycle.Rule) {
				rec, key := create(ctx, "plain", 0, lcAttr(expireRule("expire", "e/"), transition))
				put(ctx, rec, "e/k", "v", nil)
				clk.Advance(lcDay)
				pass(ctx)
				Expect(stat(ctx, rec, "e/k").Exists).To(BeFalse())
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			},
			Entry("a disabled transition", transitionTo(lifecycle.Rule{ID: "t", Status: "Disabled"}, "COLD")),
			Entry("a disabled transition to a cloud tier", transitionTo(lifecycle.Rule{ID: "t", Status: "Disabled"}, "CLOUDY")),
			Entry("a transition to a cloud tier flagged ArchiveZone outside an archive zone",
				transitionTo(lifecycle.Rule{ID: "t", Status: "Enabled", Filter: lifecycle.Filter{Flags: lifecycle.FlagArchiveZone}}, "CLOUDY")),
		)
	})

	Describe("the multipart abort", func() {
		var rec *op.BucketRecord
		abortRule := func(id, prefix string) lifecycle.Rule {
			return lifecycle.Rule{ID: id, Status: "Enabled", Filter: lifecycle.Filter{Prefix: prefix}, MPExpiration: lifecycle.Expiration{Days: "1"}}
		}
		// uploadPart initiates an upload of name and puts its part 1, whose
		// ETag it returns.
		uploadPart := func(ctx context.Context, name string) (*op.Upload, string) {
			GinkgoHelper()
			up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: name}, op.UploadParams{
				Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
			})
			Expect(err).NotTo(HaveOccurred())
			part, err := s.PutPart(ctx, up, 1, strings.NewReader("part"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 4})
			Expect(err).NotTo(HaveOccurred())
			return up, part.ETag
		}
		upload := func(ctx context.Context, name string) *op.Upload {
			GinkgoHelper()
			up, _ := uploadPart(ctx, name)
			return up
		}
		uploaded := func(ctx context.Context, up *op.Upload) bool {
			GinkgoHelper()
			_, err := s.GetUpload(ctx, rec, up.Key, up.ID)
			if errors.Is(err, op.ErrNoSuchUpload) {
				return false
			}
			Expect(err).NotTo(HaveOccurred())
			return true
		}
		partHead := func(up *op.Upload) string {
			return rec.Info.Bucket.Marker + "__multipart_" + up.Key.Name + "." + up.ID + ".1"
		}

		It("aborts an upload initiated DaysAfterInitiation ago through GetUpload and Abort, freeing its parts, keeps a newer one and notifies the abort", func(ctx SpecContext) {
			open(ctx, nil)
			var key string
			rec, key = create(ctx, "uploads", 0, lcAttr(abortRule("abort", "")))
			old := upload(ctx, "old")
			clk.Advance(lcDay)
			young := upload(ctx, "young")
			pass(ctx)
			Expect(uploaded(ctx, old)).To(BeFalse())
			Expect(uploaded(ctx, young)).To(BeTrue())
			Expect(chainOIDs(allGCEntries(c))).To(ContainElement(partHead(old)), "AbortMultipartUpload's GC of the parts")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			Expect(notes.NotifyCallCount()).To(Equal(1))
			_, note := notes.NotifyArgsForCall(0)
			Expect(note.Key).To(Equal(meta.ObjKey{Name: "old." + old.ID + ".meta", NS: meta.NSMultipart}))
			Expect(note.Events).To(Equal([]lifecycle.Event{lifecycle.EventExpirationAbortMPU}))
		})

		It("leaves the parts of an upload whose completion record names the object they back (tracker #80896)", func(ctx SpecContext) {
			open(ctx, nil)
			rec, _ = create(ctx, "uploads", 0, lcAttr(abortRule("abort", "")))
			up, etag := uploadPart(ctx, "k")
			mOID := metaOID(rec, "k", up.ID)
			head := rec.Info.Bucket.Marker + "_k"
			armed := false
			c.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				if !armed && c.Object(testDataPool, "", head) != nil {
					armed = true
					c.FailNextWrite(extraPoolName, "", mOID, syscall.EIO)
				}
			})
			_, err := s.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: `"` + etag + `"`}})
			Expect(err).NotTo(HaveOccurred())
			c.BeforeWrite(extraPoolName, "", mOID, nil)
			Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil(), "the completion's failed removal left the meta object")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(uploaded(ctx, up)).To(BeFalse(), "its meta object removed")
			Expect(c.Object(testDataPool, "", partHead(up))).NotTo(BeNil(), "the completed object's data")
			Expect(chainOIDs(allGCEntries(c))).NotTo(ContainElement(partHead(up)))
			Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
		})

		DescribeTable("aborts whatever the rule's filter, with or without an object action, on any bucket the predicate admits",
			func(ctx SpecContext, flags uint32, filter lifecycle.Filter, expire bool) {
				open(ctx, nil)
				rule := abortRule("abort", "")
				rule.Filter = filter
				if expire {
					rule.Expiration = lifecycle.Expiration{Days: "100"}
				}
				rec, _ = create(ctx, "uploads", flags, lcAttr(rule))
				up := upload(ctx, "k")
				clk.Advance(lcDay)
				pass(ctx)
				Expect(uploaded(ctx, up)).To(BeFalse())
			},
			Entry("a tag the upload has no object to carry", uint32(0), lifecycle.Filter{Tags: tags.Set{Tags: []tags.Tag{{Key: "x", Value: "y"}}}}, false),
			Entry("a size no upload reaches", uint32(0), lifecycle.Filter{SizeGT: "1099511627776"}, false),
			Entry("beside an Expiration not yet due", uint32(0), lifecycle.Filter{}, true),
			Entry("on a bucket whose flags carry object lock without BUCKET_VERSIONED", uint32(meta.BucketObjLockEnabled), lifecycle.Filter{}, false),
		)

		DescribeTable("aborts nothing for a rule that is disabled or has no DaysAfterInitiation, or under another prefix",
			func(ctx SpecContext, rule lifecycle.Rule) {
				open(ctx, nil)
				var key string
				rec, key = create(ctx, "uploads", 0, lcAttr(rule))
				up := upload(ctx, "k")
				clk.Advance(lcDay)
				pass(ctx)
				Expect(uploaded(ctx, up)).To(BeTrue())
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
			},
			Entry("disabled", lifecycle.Rule{ID: "off", Status: "Disabled", MPExpiration: lifecycle.Expiration{Days: "1"}}),
			Entry("an Expiration alone", lifecycle.Rule{ID: "expire", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}}),
			Entry("DaysAfterInitiation of 0", lifecycle.Rule{ID: "zero", Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "0"}}),
			Entry("another prefix", lifecycle.Rule{ID: "other", Status: "Enabled", Filter: lifecycle.Filter{Prefix: "other/"}, MPExpiration: lifecycle.Expiration{Days: "1"}}),
		)

		It("logs an upload gone since its listing as NoSuchUpload and goes on", func(ctx SpecContext) {
			open(ctx, nil)
			var key string
			rec, key = create(ctx, "uploads", 0, lcAttr(abortRule("abort", "")))
			first, second := upload(ctx, "a"), upload(ctx, "b")
			clk.Advance(lcDay)
			h.SetHook(func(ctx context.Context, at driver.LCStep, k meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepUpload && k.Name == "a" {
					Expect(s.Abort(ctx, first)).To(Succeed())
				}
			})
			logs := &syncBuffer{}
			DeferCleanup(driver.CaptureLog(logs))
			pass(ctx)
			Expect(logs.String()).To(ContainSubstring("lifecycle found no multipart upload to abort"))
			Expect(uploaded(ctx, second)).To(BeFalse())
			Expect(lcNotified(notes)).To(Equal([]string{"b." + second.ID + ".meta"}))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
		})

		It("leaves an upload whose completion lock another gateway holds for a later pass", func(ctx SpecContext) {
			open(ctx, nil)
			rec, _ = create(ctx, "uploads", 0, lcAttr(abortRule("abort", "")))
			up := upload(ctx, "k")
			holdLock(ctx, c, metaOID(rec, "k", up.ID))
			clk.Advance(lcDay)
			pass(ctx)
			Expect(uploaded(ctx, up)).To(BeTrue(), "radosgw's worker takes no lock and frees its parts under the completion")
			releaseLock(c, metaOID(rec, "k", up.ID))
			clk.Advance(lcDay)
			pass(ctx)
			Expect(uploaded(ctx, up)).To(BeFalse())
		})
	})
})

var _ = Describe("the lifecycle pager's lister states", func() {
	entry := func(name, instance string, epoch uint64, marker, latest bool) op.ObjectEntry {
		return op.ObjectEntry{Key: meta.ObjKey{Name: name, Instance: instance}, VersionedEpoch: epoch, DeleteMarker: marker, IsLatest: latest}
	}

	It("gives an entry the next entry of the page as listed, a dropped one included, so a current delete marker an older version follows has a next of its name", func() {
		marker := entry("k", "dm", 3, true, true)
		page := []op.ObjectEntry{marker, entry("k", "", 1, false, false), entry("z", "", 0, false, true)}
		states := driver.LCStatesForTest(page, []op.ObjectEntry{marker, page[2]}, []int{0, 2}, false)
		Expect(states[0].HasNext).To(BeTrue())
		Expect(states[0].Next).To(Equal("k"), "the null version fresh dropped")
		Expect(states[1].HasNext).To(BeFalse(), "the last entry of a page no other follows")
	})

	It("counts toward Earlier the kept entries alone, which the walk hands out once each", func() {
		page := []op.ObjectEntry{entry("k", "v3", 3, false, true), entry("k", "v2", 2, false, false), entry("k", "v1", 1, false, false)}
		states := driver.LCStatesForTest(page, page[2:], []int{2}, false)
		Expect(states[0].Earlier).To(BeZero(), "v3 and v2, handed out on an earlier page, counted there")
	})
})
