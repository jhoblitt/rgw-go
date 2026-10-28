package driver_test

import (
	"bytes"
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
	"github.com/jhoblitt/rgw-go/internal/cksum"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/lifecycle/lifecyclefakes"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// transitionTo is the enabled rule id moving the current objects under
// prefix to class a day after their mtime.
func transitionTo(id, prefix, class string) lifecycle.Rule {
	return lifecycle.Rule{
		ID: id, Status: "Enabled", Filter: lifecycle.Filter{Prefix: prefix},
		Transitions: map[string]lifecycle.Transition{class: {Days: "1", StorageClass: class}},
	}
}

// lcListPage is the entries a lifecycle listing page asks for.
const lcListPage = 1000

// noncurrentTo is the enabled rule id moving the noncurrent versions to class
// a day after they became noncurrent.
func noncurrentTo(id, class string) lifecycle.Rule {
	return lifecycle.Rule{
		ID: id, Status: "Enabled",
		NoncurTransitions: map[string]lifecycle.Transition{class: {Days: "1", StorageClass: class}},
	}
}

var _ = Describe("the lifecycle transition", func() {
	var (
		c     *fakerados.Cluster
		clk   *specClock
		s     *driver.Store
		h     *driver.LCHarness
		notes *lifecyclefakes.FakeNotifier
		rel   denc.Release
		// waited is whether a conditional write's wait on a pending
		// modification slept.
		waited bool
	)
	BeforeEach(func() {
		c = newPutCluster()
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			p.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: -1}
		})
		clk = &specClock{t: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
		c.SetClock(clk.Now)
		rel = denc.Squid
		waited = false
	})
	open := func(ctx context.Context) {
		GinkgoHelper()
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_lc_debug_interval": "10", "rgw_lc_max_objs": "1", "rgw_lc_max_wp_worker": "1"}),
			driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		driver.SetClock(s, clk.Now)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error {
			waited = true
			return nil
		})
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
	setFlags := func(ctx context.Context, name string, flags uint32) *op.BucketRecord {
		GinkgoHelper()
		rec := reload(ctx, name)
		rec.Info.Flags |= flags
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		return reload(ctx, name)
	}
	// create makes the bucket name of alice's with flags on its instance and
	// rules as its lifecycle configuration, and its lifecycle entry.
	create := func(ctx context.Context, name string, flags uint32, rules ...lifecycle.Rule) (*op.BucketRecord, string) {
		GinkgoHelper()
		rec, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: attrsOwner, Zonegroup: "zg-ceph-objectstore", Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "Alice"))}, Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrLC: lcAttr(rules...)}, nil)).To(Succeed())
		if flags != 0 {
			setFlags(ctx, name, flags)
		}
		rec = reload(ctx, name)
		Expect(s.SetEntry(ctx, rec)).To(Succeed())
		return rec, ":" + name + ":" + rec.Info.Bucket.Marker
	}
	// put writes data as name, a new version under a fresh instance where
	// rec's versioning is enabled, with the attrs and the storage class
	// given, and returns its key.
	put := func(ctx context.Context, rec *op.BucketRecord, name, data string, attrs map[string][]byte, class string) meta.ObjKey {
		GinkgoHelper()
		a := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "Alice"))}
		maps.Copy(a, attrs)
		key := meta.ObjKey{Name: name}
		if rec.Info.VersioningEnabled() {
			key.Instance = meta.NewInstance()
		}
		_, err := s.PutObject(ctx, rec, key, strings.NewReader(data), op.PutParams{Attrs: a, Size: int64(len(data)), StorageClass: class})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		return key
	}
	pass := func(ctx context.Context) {
		GinkgoHelper()
		settle(s)
		h.Pass(ctx)
		settle(s)
	}
	stat := func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) *op.ObjectState {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	body := func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) string {
		GinkgoHelper()
		st := stat(ctx, rec, key)
		Expect(st.Exists).To(BeTrue(), "%v exists", key)
		n := st.Size
		if st.Compression != nil {
			n = st.Compression.OrigSize
		}
		var b bytes.Buffer
		Expect(s.ReadObject(ctx, st, op.ByteRange{Length: n}, &b)).To(Succeed())
		return b.String()
	}
	headOID := func(rec *op.BucketRecord, key meta.ObjKey) string {
		return rec.Info.Bucket.Marker + "_" + key.OID()
	}
	// versions is name's versions in rec, newest first: each version's
	// storage class as its index entry holds it, "" for STANDARD, then "@null"
	// for the null version and "*" for the current one.
	versions := func(ctx context.Context, rec *op.BucketRecord, name string) []string {
		GinkgoHelper()
		settle(s)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Prefix: name, MaxKeys: 1000, ListVersions: rec.Info.Versioned()})
		Expect(err).NotTo(HaveOccurred())
		var out []string
		for _, e := range res.Entries {
			if e.Key.Name != name {
				continue
			}
			v := e.StorageClass
			switch {
			case e.DeleteMarker:
				v = "DM"
			case v == meta.StorageClassStandard:
				v = ""
			}
			if e.Key.Instance == "" && rec.Info.Versioned() {
				v += "@null"
			}
			if e.IsLatest && rec.Info.Versioned() {
				v += "*"
			}
			out = append(out, v)
		}
		return out
	}
	// currentNames is the names ListObjects lists in rec, the current
	// version of each.
	currentNames := func(ctx context.Context, rec *op.BucketRecord) []string {
		GinkgoHelper()
		settle(s)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1000})
		Expect(err).NotTo(HaveOccurred())
		return keys(res.Entries)
	}
	// links counts the bucket_link_olh calls written to shard since the
	// counters were last reset.
	links := func(shard string) int {
		n := 0
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			for _, st := range w.Steps() {
				if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_link_olh" {
					n++
				}
			}
		}
		return n
	}
	// pendingAttrs is the user.rgw.olh.pending attrs of name's head.
	pendingAttrs := func(rec *op.BucketRecord, name string) []string {
		var out []string
		for k := range c.Object(testDataPool, "", headOID(rec, meta.ObjKey{Name: name})).Xattrs {
			if strings.HasPrefix(k, meta.AttrOLHPendingPrefix) {
				out = append(out, k)
			}
		}
		return out
	}
	// atReread runs write once, after the transition of the object named
	// name has read it again.
	atReread := func(name string, write func(ctx context.Context)) {
		var once sync.Once
		h.SetHook(func(ctx context.Context, at driver.LCStep, key meta.ObjKey) {
			defer GinkgoRecover()
			if at == driver.LCStepReread && key.Name == name {
				once.Do(func() { write(ctx) })
			}
		})
	}

	Describe("on a bucket never versioned", func() {
		var rec *op.BucketRecord
		var key string

		It("rewrites an object into the class's pool under a new tag, its head where the placement keeps heads, its attrs, mtime and ETag kept and its old tails queued for the GC", func(ctx SpecContext) {
			open(ctx)
			rec, key = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			data := strings.Repeat("0123456789abcdef", (5<<20)/16)
			k := put(ctx, rec, "k", data, map[string][]byte{meta.AttrMetaPrefix + "a": []byte("1\x00"), meta.AttrContentType: []byte("text/plain\x00")}, "")
			before := stat(ctx, rec, k)
			stripes, err := before.Manifest.Stripes()
			Expect(err).NotTo(HaveOccurred())
			var oldTails []string
			for _, st := range stripes[1:] {
				oldTails = append(oldTails, st.OID())
			}
			Expect(oldTails).NotTo(BeEmpty(), "a source with tails")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD"}), "the index entry's class")
			after := stat(ctx, rec, k)
			Expect([]any{after.Mtime, after.ETag, after.Size}).To(Equal([]any{before.Mtime, before.ETag, before.Size}))
			Expect(body(ctx, rec, k)).To(Equal(data))
			head := c.Object(testDataPool, "", headOID(rec, k))
			Expect(head.Data).To(BeEmpty(), "the stripes in the class's pool, none in the head")
			m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
			Expect(m.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
			Expect(poolObjects(ctx, c, coldPool, "")).To(HaveLen(2), "5 MiB in two 4 MiB stripes")
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrStorageClass, []byte("COLD")))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"a", []byte("1\x00")))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
			tag := head.Xattrs[meta.AttrIDTag]
			Expect(tag).To(HaveLen(33), "copy_obj_data's new tag, NUL-terminated")
			Expect(tag).NotTo(Equal(before.Attrs[meta.AttrIDTag]))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrTailTag, tag))
			Expect(chainOIDs(allGCEntries(c))).To(ContainElements(oldTails), "complete_atomic_modification of the old head")
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Unix()), Status: rgwcls.LCStatusComplete})) //nolint:gosec // a time_t after the epoch
			Expect(notes.NotifyCallCount()).To(BeZero(), "Squid's in-cluster transition publishes nothing")
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD"}), "an object in its target class is not rewritten again")
		})

		It("notifies the transition on Tentacle", func(ctx SpecContext) {
			rel = denc.Tentacle
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			k := put(ctx, rec, "k", "v", nil, "")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(notes.NotifyCallCount()).To(Equal(1))
			_, note := notes.NotifyArgsForCall(0)
			Expect(note).To(Equal(lifecycle.Notification{
				Bucket: rec.Info.Bucket, Key: k, ETag: md5hex("v"), Size: 1,
				Events: []lifecycle.Event{"s3:LifecycleTransition", "s3:ObjectLifecycle:Transition:Current"},
			}), "rgw_lc.cc:1501-1508 at v20.2.4")
		})

		It("keeps a compressed object's stored bytes and compression info, and leaves an uncompressed one uncompressed in a compressing class", func(ctx SpecContext) {
			withCompressionClasses(c, compression.Snappy, compression.Zlib)
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "packed/", "COLD"), transitionTo("zlib", "plain/", compClass(compression.Zlib)))
			data := string(hexNoise(5 << 20))
			packed := put(ctx, rec, "packed/k", data, nil, compClass(compression.Snappy))
			plain := put(ctx, rec, "plain/k", data, nil, "")
			src := stat(ctx, rec, packed)
			Expect(src.Compression).NotTo(BeNil())
			var stored bytes.Buffer
			Expect(s.ReadStoredForTest(ctx, src, 0, src.Size, &stored)).To(Succeed())
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "packed/k")).To(Equal([]string{"COLD"}))
			dst := stat(ctx, rec, packed)
			Expect(dst.Attrs).To(HaveKeyWithValue(meta.AttrCompression, src.Attrs[meta.AttrCompression]), "the snappy block map, as stored")
			var got bytes.Buffer
			Expect(s.ReadStoredForTest(ctx, dst, 0, dst.Size, &got)).To(Succeed())
			Expect(got.Bytes()).To(Equal(stored.Bytes()), "the stored bytes, not decoded or encoded again")
			Expect(body(ctx, rec, packed)).To(Equal(data))
			Expect(versions(ctx, rec, "plain/k")).To(Equal([]string{compClass(compression.Zlib)}))
			Expect(stat(ctx, rec, plain).Compression).To(BeNil(), "transition_obj compresses nothing at either floor, where main does")
			Expect(body(ctx, rec, plain)).To(Equal(data))
		})

		It("keeps an encrypted multipart object's part lengths, which its manifest loses, and keeps part lengths it already has", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			parts := []string{strings.Repeat("a", 5<<20), strings.Repeat("b", 1<<20)}
			k := meta.ObjKey{Name: "k"}
			up, err := s.CreateUpload(ctx, rec, k, op.UploadParams{
				Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
			})
			Expect(err).NotTo(HaveOccurred())
			var done []op.CompletePart
			for i, p := range parts {
				res, perr := s.PutPart(ctx, up, i+1, strings.NewReader(p), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(p))})
				Expect(perr).NotTo(HaveOccurred())
				done = append(done, op.CompletePart{Number: i + 1, ETag: res.ETag})
			}
			_, err = s.Complete(ctx, up, done)
			Expect(err).NotTo(HaveOccurred())
			put(ctx, rec, "kept", "v", map[string][]byte{meta.AttrCryptMode: []byte("AES256"), meta.AttrCryptPrefix + "part-lengths": []byte("as stored")}, "")
			c.Object(testDataPool, "", headOID(rec, k)).Xattrs[meta.AttrCryptMode] = []byte("AES256")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD"}))
			e := denc.NewEncoder()
			e.U32(2)
			e.U64(5 << 20)
			e.U64(1 << 20)
			head := c.Object(testDataPool, "", headOID(rec, k))
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCryptPrefix+"part-lengths", e.Bytes()),
				"a vector<size_t> of one length per part, read_manifest_parts, rgw_crypt.cc:685-710")
			m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
			Expect(m.PartsCount()).To(BeZero(), "the rewritten manifest holds no parts")
			Expect(c.Object(testDataPool, "", headOID(rec, meta.ObjKey{Name: "kept"})).Xattrs).
				To(HaveKeyWithValue(meta.AttrCryptPrefix+"part-lengths", []byte("as stored")), "fixup_manifest_to_parts_len keeps an attr already set")
		})

		DescribeTable("drops a composite checksum from a multipart object it rewrites without parts, and keeps a full-object one",
			func(ctx SpecContext, flags uint16, kept bool) {
				open(ctx)
				rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
				k := meta.ObjKey{Name: "k"}
				up, err := s.CreateUpload(ctx, rec, k, op.UploadParams{
					Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
				})
				Expect(err).NotTo(HaveOccurred())
				res, err := s.PutPart(ctx, up, 1, strings.NewReader("part"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 4})
				Expect(err).NotTo(HaveOccurred())
				_, err = s.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: res.ETag}})
				Expect(err).NotTo(HaveOccurred())
				record := cksumRecord(cksum.CRC32C, be32(0xe3069283), flags)
				c.Object(testDataPool, "", headOID(rec, k)).Xattrs[meta.AttrCksum] = record
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD"}))
				head := c.Object(testDataPool, "", headOID(rec, k))
				if kept {
					Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCksum, record))
				} else {
					Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCksum), "radosgw's transition_obj keeps it")
				}
			},
			Entry("a composite, which the rewritten object would report as its body's", compositeFlags, false),
			Entry("a full-object CRC, the body's own", cksum.FlagV2|cksum.FlagCombined|cksum.FlagFullObject, true),
		)

		DescribeTable("leaves an object written between its read and its rewrite, deletes the tails it wrote, and transitions it at a later pass",
			func(ctx SpecContext, write, check func(ctx context.Context, k meta.ObjKey)) {
				open(ctx)
				rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
				k := put(ctx, rec, "k", "old", nil, "")
				clk.Advance(lcDay)
				atReread("k", func(ctx context.Context) { write(ctx, k) })
				logs := &syncBuffer{}
				restore := driver.CaptureDebugLog(logs)
				pass(ctx)
				restore()
				Expect(logs.String()).To(ContainSubstring("lifecycle leaves an object written since its read for a later pass"))
				Expect(versions(ctx, rec, "k")).To(Equal([]string{""}), "still STANDARD")
				Expect(poolObjects(ctx, c, coldPool, "")).To(BeEmpty(), "the tails the rewrite wrote, deleted")
				check(ctx, k)
				h.SetHook(nil)
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD"}), "decided again and transitioned")
				check(ctx, k)
			},
			Entry("a PUT, whose bytes an unguarded rewrite would replace with the old ones",
				func(ctx context.Context, k meta.ObjKey) { put(ctx, rec, k.Name, "new", nil, "") },
				func(ctx context.Context, k meta.ObjKey) { Expect(body(ctx, rec, k)).To(Equal("new")) }),
			Entry("a retention write, which a rewrite of the attrs it read would undo",
				func(ctx context.Context, k meta.ObjKey) {
					Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, k), map[string][]byte{
						meta.AttrObjectRetention: encode(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: clk.Now().Add(time.Hour)}),
					}, nil)).To(Succeed())
				},
				func(ctx context.Context, k meta.ObjKey) {
					Expect(stat(ctx, rec, k).Attrs).To(HaveKey(meta.AttrObjectRetention))
					Expect(body(ctx, rec, k)).To(Equal("old"))
				}),
		)

		It("leaves an object written again after its listing, at another mtime, where transition_obj answers ECANCELED", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			k := put(ctx, rec, "k", "old", nil, "")
			clk.Advance(lcDay)
			var once sync.Once
			h.SetHook(func(ctx context.Context, at driver.LCStep, key meta.ObjKey) {
				defer GinkgoRecover()
				if at == driver.LCStepDecided {
					once.Do(func() { put(ctx, rec, key.Name, "old", nil, "") })
				}
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{""}), "the object written since is not due")
			Expect(stat(ctx, rec, k).Mtime).To(Equal(clk.Now()), "its own mtime, not the listed one")
			Expect(poolObjects(ctx, c, coldPool, "")).To(BeEmpty())
		})

		It("leaves an object removed between its read and its rewrite removed", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			k := put(ctx, rec, "k", "old", nil, "")
			clk.Advance(lcDay)
			atReread("k", func(ctx context.Context) {
				Expect(s.DeleteObject(ctx, rec, k, op.DeleteParams{})).Error().To(Succeed())
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(BeEmpty())
			Expect(c.Object(testDataPool, "", headOID(rec, k))).To(BeNil(), "no head brought back")
			Expect(poolObjects(ctx, c, coldPool, "")).To(BeEmpty())
		})

		It("fails the action for a class the bucket's placement lacks, leaving the object", func(ctx SpecContext) {
			open(ctx)
			rec, key = create(ctx, "plain", 0, transitionTo("nope", "", "NOPE"))
			k := put(ctx, rec, "k", "v", nil, "")
			clk.Advance(lcDay)
			logs := &syncBuffer{}
			restore := driver.CaptureLog(logs)
			pass(ctx)
			restore()
			Expect(logs.String()).To(ContainSubstring("lifecycle cannot transition an object to a storage class its bucket's placement lacks"))
			Expect(versions(ctx, rec, "k")).To(Equal([]string{""}))
			Expect(body(ctx, rec, k)).To(Equal("v"))
			Expect(lcEntries(c)).To(HaveKeyWithValue(key, rgwcls.LCEntry{Bucket: key, StartTime: uint64(clk.Now().Unix()), Status: rgwcls.LCStatusComplete}), //nolint:gosec // a time_t after the epoch
				"a failed action fails no bucket, as the work pool ignores its result")
		})

		It("leaves an object its index lists as tiered to a cloud", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			put(ctx, rec, "k", "v", nil, "")
			shard := indexShardOID(rec, "k")
			en, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue())
			en.Meta.Category = rgwcls.CategoryCloudTiered
			c.Object(rookIndexPool, "", shard).Omap["k"] = encode(en)
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{""}), "rgw_lc.cc:1473-1477 at v19.2.7")
			Expect(poolObjects(ctx, c, coldPool, "")).To(BeEmpty())
		})

		It("keeps an object under a stored Transition Date past real_time, which radosgw wraps to 2015-06-14 and transitions at once, and acts on that date itself", func(ctx SpecContext) {
			open(ctx)
			dated := func(date string) lifecycle.Rule {
				return lifecycle.Rule{ID: "dated", Status: "Enabled", Transitions: map[string]lifecycle.Transition{"COLD": {Date: date, StorageClass: "COLD"}}}
			}
			wrapped, _ := create(ctx, "wrapped", 0, dated("2600-01-01T23:34:33.709551616Z"))
			plain, _ := create(ctx, "plain", 0, dated("2015-06-14T00:00:00Z"))
			put(ctx, wrapped, "k", "v", nil, "")
			put(ctx, plain, "k", "v", nil, "")
			pass(ctx)
			Expect(versions(ctx, wrapped, "k")).To(Equal([]string{""}), "the prefix map holds no date for it, so the rule names no instant to act at")
			Expect(versions(ctx, plain, "k")).To(Equal([]string{"COLD"}), "the instant radosgw's wrap lands at")
		})
	})

	Describe("with object lock", func() {
		It("keeps an object's own retention and legal hold, and adds no default retention to one without, as S3 applies the default to versions placed in the bucket alone", func(ctx SpecContext) {
			open(ctx)
			rec, _ := create(ctx, "locked", meta.BucketVersioned|meta.BucketObjLockEnabled, transitionTo("cold", "", "COLD"))
			own := map[string][]byte{
				meta.AttrObjectRetention: encode(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: clk.Now().Add(100 * 24 * time.Hour)}),
				meta.AttrObjectLegalHold: encode(meta.ObjectLegalHold{Status: "ON"}),
			}
			held := put(ctx, rec, "held", "h", own, "")
			bare := put(ctx, rec, "bare", "b", nil, "")
			Expect(stat(ctx, rec, bare).Attrs).NotTo(HaveKey(meta.AttrObjectRetention), "written before the bucket had a rule")
			rec = reload(ctx, "locked")
			rule := meta.DefaultRetention{Mode: "GOVERNANCE", Days: 3}
			rec.Info.SetObjectLock(meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: rule}})
			Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
			rec = reload(ctx, "locked")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "held")).To(Equal([]string{"COLD*"}))
			Expect(versions(ctx, rec, "bare")).To(Equal([]string{"COLD*"}))
			for name, v := range own {
				Expect(stat(ctx, rec, held).Attrs).To(HaveKeyWithValue(name, v))
			}
			Expect(stat(ctx, rec, bare).Attrs).NotTo(HaveKey(meta.AttrObjectRetention),
				"where radosgw's create write adds the default, rgw_rados.cc:3211-3221 at v19.2.7, :3308-3318 at v20.2.4")
			fresh := put(ctx, rec, "fresh", "f", nil, "")
			Expect(stat(ctx, rec, fresh).Attrs).To(HaveKeyWithValue(meta.AttrObjectRetention,
				encode(meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: objectlock.LockUntil(rule, stat(ctx, rec, fresh).Mtime)})),
				"a version placed in the bucket still takes the default")
		})
	})

	Describe("on a bucket whose versioning is enabled", func() {
		var rec *op.BucketRecord

		It("keeps the current version current, linked at its own epoch, its key's OLH as it was and no pending attr left", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
			v1 := put(ctx, rec, "k", "v1", nil, "")
			info := c.Object(testDataPool, "", headOID(rec, meta.ObjKey{Name: "k"})).Xattrs[meta.AttrOLHInfo]
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD*"}))
			Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("v1"))
			Expect(stat(ctx, rec, meta.ObjKey{Name: "k"}).Key).To(Equal(v1))
			Expect(c.Object(testDataPool, "", headOID(rec, meta.ObjKey{Name: "k"})).Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info))
			Expect(pendingAttrs(rec, "k")).To(BeEmpty())
		})

		It("keeps a noncurrent version noncurrent, olh.info unchanged and no pending attr on the OLH head, so an If-None-Match: * PUT straight after does not wait", func(ctx SpecContext) {
			rel = denc.Tentacle
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, noncurrentTo("cold", "COLD"))
			v1 := put(ctx, rec, "k", "v1", nil, "")
			clk.Advance(lcDay)
			put(ctx, rec, "k", "v2", nil, "")
			info := c.Object(testDataPool, "", headOID(rec, meta.ObjKey{Name: "k"})).Xattrs[meta.AttrOLHInfo]
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"*", "COLD"}))
			Expect(body(ctx, rec, v1)).To(Equal("v1"))
			Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("v2"))
			Expect(c.Object(testDataPool, "", headOID(rec, meta.ObjKey{Name: "k"})).Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info))
			Expect(pendingAttrs(rec, "k")).To(BeEmpty(), "the link below the OLH entry's epoch logs nothing, and setOLH removes its own attr")
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k", Instance: meta.NewInstance()}, strings.NewReader("v3"),
				op.PutParams{Attrs: attrsWithACL(attrsOwner), Size: 2, IfNoneMatch: "*"})
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			Expect(waited).To(BeFalse(), "no pending modification to wait on")
			Expect(notes.NotifyCallCount()).To(Equal(1))
			_, note := notes.NotifyArgsForCall(0)
			Expect(note.Events).To(Equal([]lifecycle.Event{"s3:LifecycleTransition", "s3:ObjectLifecycle:Transition:NonCurrent"}))
			Expect(note.VersionID).To(Equal(v1.Instance))
		})

		It("transitions an object put before versioning was enabled as the current null version", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
			put(ctx, rec, "k", "old", nil, "")
			rec = setFlags(ctx, "plain", meta.BucketVersioned)
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD@null*"}))
			Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("old"))
		})

		It("rewrites the null version's data in its key's OLH head without writing back the OLH attrs it read, so a link landing during the rewrite stands", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, noncurrentTo("cold", "COLD"))
			put(ctx, rec, "k", "old", nil, "")
			rec = setFlags(ctx, "plain", meta.BucketVersioned)
			put(ctx, rec, "k", "v1", nil, "")
			olhHead := headOID(rec, meta.ObjKey{Name: "k"})
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"*", "@null"}))
			clk.Advance(lcDay)
			var v2 meta.ObjKey
			atReread("k", func(ctx context.Context) { v2 = put(ctx, rec, "k", "v2", nil, "") })
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"*", "", "COLD@null"}))
			Expect(stat(ctx, rec, meta.ObjKey{Name: "k"}).Key).To(Equal(v2), "the OLH names v2, linked during the rewrite")
			Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("v2"))
			Expect(body(ctx, rec, meta.ObjKey{Name: "k", Instance: meta.NullInstance})).To(Equal("old"))
			Expect(c.Object(testDataPool, "", olhHead).Xattrs).To(HaveKeyWithValue(meta.AttrStorageClass, []byte("COLD")), "the null version's data, rewritten")
			Expect(c.Object(testDataPool, "", olhHead).Xattrs[meta.AttrIDTag]).To(HaveLen(33), "the rewrite's own write tag")
			Expect(pendingAttrs(rec, "k")).To(BeEmpty())
		})

		It("rewrites a null version whose OLH head's mtime a radosgw gateway's removal of a pending attr moved, at its listed mtime, where transition_obj answers ECANCELED", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "plain", 0, noncurrentTo("cold", "COLD"))
			put(ctx, rec, "k", "old", nil, "")
			rec = setFlags(ctx, "plain", meta.BucketVersioned)
			put(ctx, rec, "k", "v1", nil, "")
			null := meta.ObjKey{Name: "k", Instance: meta.NullInstance}
			listed := stat(ctx, rec, null).Mtime
			clk.Advance(time.Second)
			pool, err := c.Pool(ctx, testDataPool, "")
			Expect(err).NotTo(HaveOccurred())
			w := radosclient.NewWriteOp()
			w.RmXattr(meta.AttrOLHPendingPrefix + "00000000000000000radosgwpending")
			_, err = pool.Write(ctx, headOID(rec, meta.ObjKey{Name: "k"}), w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(stat(ctx, rec, null).Mtime).NotTo(BeTemporally("==", listed), "the head's mtime, which the removal moved")
			clk.Advance(lcDay)
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"*", "COLD@null"}))
			Expect(stat(ctx, rec, null).Mtime).To(BeTemporally("==", listed), "the rewrite stamps the mtime the index lists")
		})

		DescribeTable("leaves a version written or removed between its read and its rewrite, where radosgw's rewrite of a version is unguarded",
			func(ctx SpecContext, write func(ctx context.Context, v meta.ObjKey), want []string, check func(ctx context.Context, v meta.ObjKey)) {
				open(ctx)
				rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
				v1 := put(ctx, rec, "k", "v1", nil, "")
				clk.Advance(lcDay)
				atReread("k", func(ctx context.Context) { write(ctx, v1) })
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal(want))
				Expect(poolObjects(ctx, c, coldPool, "")).To(BeEmpty(), "the tails the rewrite wrote, deleted")
				check(ctx, v1)
			},
			Entry("a legal hold, which radosgw's rewrite of the attrs it read takes off",
				func(ctx context.Context, v meta.ObjKey) {
					Expect(s.SetObjectAttrs(ctx, stat(ctx, rec, v), map[string][]byte{meta.AttrObjectLegalHold: encode(meta.ObjectLegalHold{Status: "ON"})}, nil)).To(Succeed())
				}, []string{"*"},
				func(ctx context.Context, v meta.ObjKey) {
					Expect(stat(ctx, rec, v).Attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, encode(meta.ObjectLegalHold{Status: "ON"})))
				}),
			Entry("a removal of the version, which radosgw's rewrite brings back",
				func(ctx context.Context, v meta.ObjKey) {
					Expect(s.DeleteObject(ctx, rec, v, op.DeleteParams{})).Error().To(Succeed())
				}, []string(nil),
				func(ctx context.Context, v meta.ObjKey) {
					Expect(stat(ctx, rec, v).Exists).To(BeFalse())
					Expect(c.Object(testDataPool, "", headOID(rec, v))).To(BeNil(), "no head brought back")
				}),
		)

		DescribeTable("keeps a version whose transition's index completion or link fails, its data in the tails the rewrite wrote, and transitions it at a later pass",
			func(ctx SpecContext, arm func(shard, olhHead string)) {
				open(ctx)
				rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
				v1 := put(ctx, rec, "k", "v1", nil, "")
				shard, olhHead, v1Head := indexShardOID(rec, "k"), headOID(rec, meta.ObjKey{Name: "k"}), headOID(rec, v1)
				clk.Advance(lcDay)
				var once sync.Once
				c.AfterWrite(testDataPool, "", v1Head, func() { once.Do(func() { arm(shard, olhHead) }) })
				logs := &syncBuffer{}
				restore := driver.CaptureLog(logs)
				pass(ctx)
				restore()
				c.AfterWrite(testDataPool, "", v1Head, nil)
				c.AfterWrite(testDataPool, "", olhHead, nil)
				Expect(c.Object(testDataPool, "", v1Head)).NotTo(BeNil(), "the version's head")
				Expect(body(ctx, rec, v1)).To(Equal("v1"), "its data, which the tails the rewrite wrote now hold")
				Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("v1"))
				Expect(poolObjects(ctx, c, coldPool, "")).NotTo(BeEmpty(), "the tails the version's head names")
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"*"}), "still listed current, by its list entry as it was")
				Expect(logs.String()).To(ContainSubstring("lifecycle could not transition an object; it is decided again at a later pass"))
				settle(s)
				en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: v1.Instance})
				Expect(ok).To(BeTrue())
				Expect(en.PendingMap).To(BeEmpty(), "a failed completion's prepare is canceled, as done_cancel cancels it")
				clk.Advance(lcDay)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD*"}))
				Expect(body(ctx, rec, v1)).To(Equal("v1"))
			},
			Entry("an EIO on the index completion", func(shard, _ string) {
				c.FailNextWrite(rookIndexPool, "", shard, syscall.EIO)
			}),
			Entry("an EIO on the link's init", func(_, olhHead string) {
				c.FailNextWrite(testDataPool, "", olhHead, syscall.EIO)
			}),
			Entry("an EIO on the link", func(shard, olhHead string) {
				var once sync.Once
				c.AfterWrite(testDataPool, "", olhHead, func() { once.Do(func() { c.FailNextWrite(rookIndexPool, "", shard, syscall.EIO) }) })
			}),
		)

		It("keeps the current version current once a noncurrent version's removal raised its OLH entry's epoch, linking it at that epoch where radosgw links at the version's own and hides it", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
			v1 := put(ctx, rec, "k", "v1", nil, "")
			v2 := put(ctx, rec, "k", "v2", nil, "")
			Expect(s.DeleteObject(ctx, rec, v1, op.DeleteParams{})).Error().To(Succeed())
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"*"}))
			clk.Advance(lcDay)
			settle(s)
			c.ResetCounters()
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD*"}), "ListObjectVersions has a latest version")
			Expect(currentNames(ctx, rec)).To(Equal([]string{"k"}), "and ListObjects lists the key")
			Expect(stat(ctx, rec, meta.ObjKey{Name: "k"}).Key).To(Equal(v2))
			Expect(links(indexShardOID(rec, "k"))).To(Equal(1), "one link, at the OLH entry's epoch, and no re-link")
		})

		It("lists the current version current again when another version's removal lands between its OLH entry's read and its link", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
			v1 := put(ctx, rec, "k", "v1", nil, "")
			v2 := put(ctx, rec, "k", "v2", nil, "")
			clk.Advance(lcDay)
			atReread("k", func(ctx context.Context) {
				Expect(s.DeleteObject(ctx, rec, v1, op.DeleteParams{})).Error().To(Succeed())
				settle(s)
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD*"}))
			Expect(currentNames(ctx, rec)).To(Equal([]string{"k"}))
			Expect(stat(ctx, rec, meta.ObjKey{Name: "k"}).Key).To(Equal(v2))
			Expect(pendingAttrs(rec, "k")).To(BeEmpty())
		})

		It("lists a noncurrent version current when the current version's removal makes it current during its copy", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, noncurrentTo("cold", "COLD"))
			v1 := put(ctx, rec, "k", "v1", nil, "")
			clk.Advance(lcDay)
			v2 := put(ctx, rec, "k", "v2", nil, "")
			clk.Advance(lcDay)
			atReread("k", func(ctx context.Context) {
				Expect(s.DeleteObject(ctx, rec, v2, op.DeleteParams{})).Error().To(Succeed())
				settle(s)
			})
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD*"}), "ListObjectVersions lists v1 latest")
			Expect(currentNames(ctx, rec)).To(Equal([]string{"k"}), "and ListObjects lists the key")
			Expect(stat(ctx, rec, meta.ObjKey{Name: "k"}).Key).To(Equal(v1))
			Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("v1"))
			Expect(pendingAttrs(rec, "k")).To(BeEmpty())
		})

		It("rewrites a current null version, put on a suspended bucket over a version, in its key's OLH head, keeping the OLH attrs a removal leaves during the copy", func(ctx SpecContext) {
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
			v1 := put(ctx, rec, "k", "v1", nil, "")
			rec = setFlags(ctx, "versioned", meta.BucketVersionsSuspended)
			put(ctx, rec, "k", "null", nil, "")
			olhHead := headOID(rec, meta.ObjKey{Name: "k"})
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"@null*", ""}))
			clk.Advance(lcDay)
			atReread("k", func(ctx context.Context) {
				Expect(s.DeleteObject(ctx, rec, v1, op.DeleteParams{})).Error().To(Succeed())
				settle(s)
			})
			settle(s)
			c.ResetCounters()
			pass(ctx)
			Expect(versions(ctx, rec, "k")).To(Equal([]string{"COLD@null*"}), "listed current again after the removal raised the OLH entry's epoch")
			Expect(currentNames(ctx, rec)).To(Equal([]string{"k"}))
			Expect(body(ctx, rec, meta.ObjKey{Name: "k"})).To(Equal("null"))
			var olhSets []string
			for _, w := range c.WritesTo(testDataPool, "", olhHead) {
				if set := names(w.Steps()); slices.Contains(set, meta.AttrManifest) {
					olhSets = slices.DeleteFunc(set, func(n string) bool { return !strings.HasPrefix(n, meta.AttrOLHPrefix) })
				}
			}
			Expect(olhSets).To(Equal([]string{meta.AttrOLHIDTag}), "the head write sets the OLH tag again and none of the OLH attrs it read")
			Expect(pendingAttrs(rec, "k")).To(BeEmpty())
		})

		DescribeTable("leaves a current plain null version a PUT or a delete marker covers during its copy, whose OLH init gives the head a new write tag",
			func(ctx SpecContext, write func(ctx context.Context), want []string) {
				open(ctx)
				rec, _ = create(ctx, "plain", 0, transitionTo("cold", "", "COLD"))
				put(ctx, rec, "k", "old", nil, "")
				rec = setFlags(ctx, "plain", meta.BucketVersioned)
				clk.Advance(lcDay)
				atReread("k", write)
				pass(ctx)
				Expect(versions(ctx, rec, "k")).To(Equal(want))
				Expect(body(ctx, rec, meta.ObjKey{Name: "k", Instance: meta.NullInstance})).To(Equal("old"))
				Expect(poolObjects(ctx, c, coldPool, "")).To(BeEmpty(), "the tails the rewrite wrote, deleted")
			},
			Entry("a PUT of a new version", func(ctx context.Context) { put(ctx, rec, "k", "new", nil, "") }, []string{"*", "@null"}),
			Entry("a delete marker", func(ctx context.Context) {
				Expect(s.DeleteObject(ctx, rec, meta.ObjKey{Name: "k"}, op.DeleteParams{MarkerOwner: attrsOwner})).Error().To(Succeed())
				settle(s)
			}, []string{"DM*", "@null"}),
		)

		It("transitions each version of a versions listing of more than one page once", func(ctx SpecContext) {
			rel = denc.Tentacle
			open(ctx)
			rec, _ = create(ctx, "versioned", meta.BucketVersioned, transitionTo("cold", "", "COLD"))
			for i := range lcListPage + 1 {
				put(ctx, rec, fmt.Sprintf("k%04d", i), "v", nil, "")
			}
			clk.Advance(lcDay)
			pass(ctx)
			Expect(notes.NotifyCallCount()).To(Equal(lcListPage+1), "one transition each")
			res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 2 * lcListPage, ListVersions: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Entries).To(HaveLen(lcListPage + 1))
			for _, e := range res.Entries {
				Expect([]any{e.StorageClass, e.IsLatest}).To(Equal([]any{"COLD", true}), e.Key.Name)
			}
		})
	})
})
