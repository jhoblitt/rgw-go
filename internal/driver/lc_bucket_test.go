package driver_test

import (
	"context"
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

var _ = Describe("the lifecycle bucket pass", func() {
	var (
		c     *fakerados.Cluster
		clk   *specClock
		s     *driver.Store
		h     *driver.LCHarness
		notes *lifecyclefakes.FakeNotifier
	)
	BeforeEach(func() {
		c = newPutCluster()
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			p.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: -1}
		})
		clk = &specClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
		c.SetClock(clk.Now)
	})
	// open opens s over c with the specs' lifecycle day and one shard,
	// overridden by kv, and a harness h whose notifier is notes.
	open := func(ctx context.Context, kv map[string]string) {
		GinkgoHelper()
		opts := map[string]string{"rgw_lc_debug_interval": "10", "rgw_lc_max_objs": "1"}
		maps.Copy(opts, kv)
		var err error
		s, err = driver.Open(ctx, c, conf(opts), driver.Options{})
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
	// setFlags sets flags on the instance of the bucket name.
	setFlags := func(ctx context.Context, name string, flags uint32) {
		GinkgoHelper()
		rec := reload(ctx, name)
		rec.Info.Flags |= flags
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
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
			rec, key := create(ctx, "plain", meta.BucketVersioned, lcAttr(expireRule("expire", "")))
			put(ctx, rec, "k", "v", nil)
			clk.Advance(lcDay)
			settle(s)
			c.ResetCounters()
			Expect(h.RunForTest(ctx, key)).To(MatchError(ContainSubstring("refused since its claim: ever versioned")))
			Expect(c.Reads(rookIndexPool, "", indexShardOID(rec, "k"))).To(BeZero(), "nothing listed")
			Expect(stat(ctx, rec, "k").Exists).To(BeTrue())
		})

		DescribeTable("leaves a bucket with an abort of incomplete multipart uploads for radosgw's worker, which claims it",
			func(ctx SpecContext, abort lifecycle.Rule) {
				open(ctx, nil)
				_, key := create(ctx, "uploads", 0, lcAttr(expireRule("expire", ""), abort))
				logs := &syncBuffer{}
				restore := driver.CaptureLog(logs)
				pass(ctx)
				restore()
				Expect(lcEntries(c)).To(HaveKeyWithValue(key, rgwcls.LCEntry{Bucket: key}), "never claimed")
				Expect(logs.String()).To(ContainSubstring(fmt.Sprintf(`"bucket":%q,"reason":"rule aborts multipart uploads","rule":"abort"`, key)))
				s2, err := driver.Open(ctx, driver.LCClientForTest(c, "client.2"), conf(map[string]string{"rgw_lc_debug_interval": "10", "rgw_lc_max_objs": "1"}), driver.Options{})
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(s2.Close)
				var claimed []string
				s2.RadosgwShardPassForTest(ctx, clk.Now, 0, 0, func(k string) error {
					claimed = append(claimed, k)
					return nil
				})
				Expect(claimed).To(Equal([]string{key}))
			},
			Entry("an enabled rule with DaysAfterInitiation", lifecycle.Rule{ID: "abort", Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "1"}}),
			Entry("one flagged ArchiveZone outside an archive zone, under which radosgw aborts all the same",
				lifecycle.Rule{ID: "abort", Status: "Enabled", Filter: lifecycle.Filter{Flags: lifecycle.FlagArchiveZone}, MPExpiration: lifecycle.Expiration{Days: "1"}}),
		)

		It("claims a bucket whose abort rule is disabled, and applies its other rules", func(ctx SpecContext) {
			open(ctx, nil)
			rec, key := create(ctx, "uploads", 0, lcAttr(expireRule("expire", ""),
				lifecycle.Rule{ID: "abort", Status: "Disabled", MPExpiration: lifecycle.Expiration{Days: "1"}}))
			put(ctx, rec, "k", "v", nil)
			clk.Advance(lcDay)
			pass(ctx)
			Expect(stat(ctx, rec, "k").Exists).To(BeFalse())
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, completed(key)))
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

	Describe("the claim predicate", func() {
		var (
			first, last                           *op.BucketRecord
			firstKey, lastKey                     string
			versionedKey, transitionKey, cloudKey string
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
			first, firstKey = create(ctx, "a-plain", 0, lcAttr(expireRule("expire", "")))
			_, versionedKey = create(ctx, "b-versioned", meta.BucketVersioned, lcAttr(expireRule("expire", "")))
			_, transitionKey = create(ctx, "c-transition", 0, lcAttr(transition))
			_, cloudKey = create(ctx, "d-cloud", 0, lcAttr(cloud))
			last, lastKey = create(ctx, "e-plain", 0, lcAttr(expireRule("expire", "")))
		})
		// day puts an object in each bucket rgw-go may claim and passes a
		// lifecycle day later. It expects those objects removed, those
		// buckets complete, the others' entries never claimed and each of
		// them logged once, and returns the pass's log.
		day := func(ctx context.Context) string {
			GinkgoHelper()
			put(ctx, first, "k", "v", nil)
			put(ctx, last, "k", "v", nil)
			clk.Advance(lcDay)
			logs := &syncBuffer{}
			restore := driver.CaptureLog(logs)
			pass(ctx)
			restore()
			entries := lcEntries(c)
			for _, key := range []string{firstKey, lastKey} {
				Expect(entries).To(HaveKeyWithValue(key, completed(key)))
			}
			for _, key := range []string{versionedKey, transitionKey, cloudKey} {
				Expect(entries).To(HaveKeyWithValue(key, rgwcls.LCEntry{Bucket: key}), "never claimed")
			}
			Expect(stat(ctx, first, "k").Exists).To(BeFalse())
			Expect(stat(ctx, last, "k").Exists).To(BeFalse())
			Expect(strings.Count(logs.String(), "lifecycle leaves a bucket for radosgw's worker")).To(Equal(3), "logged once a pass each:\n%s", logs.String())
			return logs.String()
		}

		It("leaves a versioned bucket, a transition rule's and a cloud tier's to radosgw's worker, which claims them where rgw-go left the head", func(ctx SpecContext) {
			logged := day(ctx)
			for key, reason := range map[string]string{
				versionedKey: "ever versioned", transitionKey: "rule transitions objects", cloudKey: "rule transitions to a cloud tier",
			} {
				Expect(logged).To(ContainSubstring(fmt.Sprintf(`"bucket":%q,"reason":%q`, key, reason)))
			}
			s2, err := driver.Open(ctx, driver.LCClientForTest(c, "client.2"), conf(map[string]string{"rgw_lc_debug_interval": "10", "rgw_lc_max_objs": "1"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(s2.Close)
			var claimed []string
			s2.RadosgwShardPassForTest(ctx, clk.Now, 0, 0, func(key string) error {
				claimed = append(claimed, key)
				return nil
			})
			refused := []string{versionedKey, transitionKey, cloudKey}
			Expect(claimed).To(Equal(refused), "radosgw's walk from the head rgw-go left at the first of them, the others done today")
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
})
