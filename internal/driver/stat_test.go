package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// testDataPool is the STANDARD data pool of testZone's default-placement,
// and of seedRookZone's for the store "ceph-objectstore".
const testDataPool = "ceph-objectstore.rgw.buckets.data"

// testZone is a zone whose zonegroup's default placement, default-placement,
// keeps its STANDARD data in testDataPool.
func testZone() driver.ZoneForTest {
	return driver.ZoneForTest{
		Params: meta.ZoneParams{PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": {
			StorageClasses: meta.ZoneStorageClasses{meta.StorageClassStandard: {DataPool: new(meta.ParsePool(testDataPool))}},
		}}},
		ZoneGroup: meta.ZoneGroup{DefaultPlacement: meta.PlacementRule{Name: "default-placement"}},
	}
}

// testReadConfig is the read options at rgw.yaml.in's defaults.
func testReadConfig() driver.ReadConfigForTest {
	return driver.ReadConfigForTest{Chunk: 4 << 20, MaxReq: 4 << 20, Window: 16 << 20}
}

// fakeClusterWith is a counterfeiter cluster that opens every pool as pool.
func fakeClusterWith(pool radosclient.Pool) *radosclientfakes.FakeCluster {
	c := &radosclientfakes.FakeCluster{}
	c.PoolReturns(pool, nil)
	return c
}

// stepTypes names the type of each step, in order.
func stepTypes(steps []radosclient.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, fmt.Sprintf("%T", s))
	}
	return out
}

var _ = Describe("StatObject and PrefetchObject", func() {
	const (
		store  = "ceph-objectstore"
		marker = "m1"
	)
	var (
		c     *fakerados.Cluster
		ids   rookZone
		s     *driver.Store
		plain *op.BucketRecord
		mtime time.Time
	)
	// head stores a head object in the data pool, attrs among its xattrs.
	head := func(oid string, data []byte, attrs map[string][]byte) {
		GinkgoHelper()
		c.Put(testDataPool, "", oid, data)
		maps.Copy(c.Object(testDataPool, "", oid).Xattrs, attrs)
	}
	open := func(ctx context.Context) *driver.Store {
		GinkgoHelper()
		opened, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": store}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(opened.Close)
		return opened
	}
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		c = fakerados.New()
		c.SetClock(func() time.Time { return mtime })
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
		ids = seedRookZone(c, store, true)
		editRoot(c, meta.ZoneInfoOID(ids.zoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			pi := z.PlacementPools["default-placement"]
			pi.StorageClasses["COLD"] = meta.ZoneStorageClass{DataPool: new(meta.ParsePool("cold.data"))}
			pi.StorageClasses["NO_POOL"] = meta.ZoneStorageClass{}
			z.PlacementPools["default-placement"] = pi
		})
		s = open(ctx)
		plain = &op.BucketRecord{Info: meta.BucketInfo{
			Bucket:        meta.BucketID{Name: "plain", Marker: marker, ID: marker},
			PlacementRule: meta.ParsePlacementRule("default-placement"),
		}}
	})

	It("answers Exists false and no error for a missing key, stat or prefetch", func(ctx SpecContext) {
		want := &op.ObjectState{Bucket: plain, Key: meta.ObjKey{Name: "nope"}}
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "nope"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(want))
		st, err = s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "nope"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(want))
	})

	It("decodes a head-only object: size, mtime, epoch, version, attrs, etag, write tag, class and content type, without data", func(ctx SpecContext) {
		head(marker+"_small.bin", bytes.Repeat([]byte{7}, 1024), map[string][]byte{
			meta.AttrETag:         []byte("0123456789abcdef0123456789abcdef\x00"),
			meta.AttrIDTag:        []byte("tx000...\x00"),
			meta.AttrContentType:  []byte("text/plain\x00\x00"),
			meta.AttrStorageClass: []byte("COLD"),
			meta.AttrACL:          {1, 2, 3},
			version.XattrName:     encode(meta.ObjVersion{Ver: 3, Tag: "vtag"}),
		})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(&op.ObjectState{
			Bucket: plain,
			Key:    meta.ObjKey{Name: "small.bin"},
			Exists: true,
			Size:   1024,
			Mtime:  mtime,
			Epoch:  1,
			Attrs: map[string][]byte{
				meta.AttrETag:         []byte("0123456789abcdef0123456789abcdef"),
				meta.AttrIDTag:        []byte("tx000...\x00"),
				meta.AttrContentType:  []byte("text/plain\x00\x00"),
				meta.AttrStorageClass: []byte("COLD"),
				meta.AttrACL:          {1, 2, 3},
			},
			ETag:         "0123456789abcdef0123456789abcdef",
			WriteTag:     "tx000...\x00",
			StorageClass: "COLD",
			ContentType:  "text/plain",
			Version:      meta.ObjVersion{Ver: 3, Tag: "vtag"},
		}), "rgw_filter_attrset keeps user.rgw. only; the etag loses its NUL in the attrs too; the idtag keeps its NUL")
	})

	DescribeTable("drops one trailing NUL from the etag, as get_obj_state_impl does",
		func(ctx SpecContext, stored, want string) {
			head(marker+"_e.bin", nil, map[string][]byte{meta.AttrETag: []byte(stored)})
			st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "e.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.ETag).To(Equal(want))
			Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrETag, []byte(want)))
		},
		Entry("an etag written without one", "abc", "abc"),
		Entry("an etag written with one", "abc\x00", "abc"),
		Entry("an etag ending in two keeps the first", "abc\x00\x00", "abc\x00"),
		Entry("an empty etag", "", ""),
	)

	It("fails with radosgw's UnknownError when the head's version does not decode", func(ctx SpecContext) {
		head(marker+"_v.bin", nil, map[string][]byte{version.XattrName: {9}})
		_, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "v.bin"})
		Expect(err).To(MatchError(op.ErrUnknown), "read_version answers EIO (cls_version.cc:70-76), which rgw_http_s3_errors lacks")
	})

	It("prefetches the whole head when it is shorter than the chunk, and exactly the chunk otherwise", func(ctx SpecContext) {
		small := bytes.Repeat([]byte{1}, 1024)
		head(marker+"_small.bin", small, map[string][]byte{meta.AttrETag: []byte("e")})
		st, err := s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(Equal(small))
		Expect(st.Size).To(BeEquivalentTo(1024), "the rest of the state is StatObject's")
		Expect(st.Version).To(BeZero(), "a head without a version reads as version zero")

		big := bytes.Repeat([]byte("0123456789abcdef"), (5<<20)/16)
		head(marker+"_big.bin", big, map[string][]byte{meta.AttrETag: []byte("e")})
		st, err = s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "big.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(Equal(big[:4<<20]))
	})

	It("hands out a prefetched head that later prefetches leave alone", func(ctx SpecContext) {
		head(marker+"_a.bin", []byte("aaaa"), nil)
		head(marker+"_b.bin", []byte("bb"), nil)
		head(marker+"_empty.bin", nil, nil)
		a, err := s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "a.bin"})
		Expect(err).NotTo(HaveOccurred())
		for range 3 {
			_, err = s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "b.bin"})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(a.Head).To(Equal([]byte("aaaa")))
		empty, err := s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "empty.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(empty.Head).To(BeEmpty())
		Expect(empty.Head).NotTo(BeNil(), "a prefetch of an empty head read it, which a stat's nil does not say")
	})

	It("names and locates an underscore key as get_obj_bucket_and_oid_loc does", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		located := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(located)
		located.ReadReturns(0, &radosclient.Error{Errno: 2, Op: "read"})
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "_underscore.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeFalse())
		Expect(pool.WithLocatorArgsForCall(0)).To(Equal(marker + "__underscore.bin"))
		_, oid, _, _ := located.ReadArgsForCall(0)
		Expect(oid).To(Equal(marker + "___underscore.bin"))
		Expect(pool.ReadCallCount()).To(BeZero(), "the read went through the located handle")
	})

	It("decodes a manifest, patches its head and takes the size from it; a manifest without an idtag keeps an empty write tag", func(ctx SpecContext) {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "large.bin"}}
		m.ObjSize, m.MaxHeadSize = 10<<20, 4<<20
		m.Prefix = ".abc_"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}
		m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
		// stored with a stale head placement and size, as a manifest "broken
		// due to old bugs" would be
		m.HeadSize = 1
		m.HeadPlacementRule = meta.ParsePlacementRule("stale")
		head(marker+"_large.bin", make([]byte, 4<<20), map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrManifest: encode(m)})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Size).To(BeEquivalentTo(10<<20), "the manifest's obj_size, not the head's stat size")
		Expect(st.Manifest).NotTo(BeNil())
		Expect(st.Manifest.HeadSize).To(BeEquivalentTo(4<<20), "set_head patches head_size to the stat size")
		Expect(st.Manifest.HeadPlacementRule).To(Equal(plain.Info.PlacementRule), "and the head placement to the bucket's")
		Expect(st.Manifest.Obj).To(Equal(meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "large.bin"}}))
		Expect(st.Manifest.Prefix).To(Equal(".abc_"))
		Expect(st.WriteTag).To(BeEmpty())
	})

	It("points an explicit manifest's first piece at the head it read", func(ctx SpecContext) {
		obj := meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "old.bin"}}
		shadow := meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "old.bin.1", NS: meta.NSShadow}}
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Obj, m.ObjSize, m.HeadSize = obj, 300, 1
		m.Objs = map[uint64]meta.ManifestPart{0: {Loc: shadow, Size: 1}, 100: {Loc: shadow, Size: 200}}
		head(marker+"_old.bin", make([]byte, 100), map[string][]byte{meta.AttrManifest: encode(m)})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "old.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Manifest.Objs).To(Equal(map[uint64]meta.ManifestPart{0: {Loc: obj, Size: 100}, 100: {Loc: shadow, Size: 200}}))
		Expect(st.Size).To(BeEquivalentTo(300))
	})

	It("decodes compression info and refuses a corrupt one with UnknownError, radosgw's -EIO", func(ctx SpecContext) {
		ci := meta.NewCompressionInfo()
		ci.Type, ci.OrigSize = "zlib", 1<<20
		ci.Blocks = []meta.CompressionBlock{{OldOfs: 0, NewOfs: 0, Len: 100}}
		head(marker+"_c.bin", make([]byte, 100), map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrCompression: encode(ci)})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "c.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Compression).To(Equal(&ci))
		Expect(st.Size).To(BeEquivalentTo(100), "Size stays the stored size; the op reports orig_size")

		head(marker+"_bad.bin", make([]byte, 100), map[string][]byte{meta.AttrETag: []byte("e"), meta.AttrCompression: {0xff, 0xff}})
		_, err = s.StatObject(ctx, plain, meta.ObjKey{Name: "bad.bin"})
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err).To(MatchError(ContainSubstring("plain/bad.bin")))
	})

	It("refuses a corrupt manifest with UnknownError, radosgw's -EIO", func(ctx SpecContext) {
		head(marker+"_bad.bin", nil, map[string][]byte{meta.AttrManifest: {0x01}})
		_, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "bad.bin"})
		Expect(err).To(MatchError(op.ErrUnknown))
	})

	It("refuses an olh head with NotImplemented, logging it, after the decoding radosgw does first", func(ctx SpecContext) {
		var logs bytes.Buffer
		DeferCleanup(driver.CaptureLog(&logs))
		olh := map[string][]byte{meta.AttrOLHVer: {1, 0, 0, 0, 0, 0, 0, 0}, meta.AttrPrefix + "olh.idtag": []byte("t")}
		head(marker+"_v.bin", nil, olh)
		_, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "v.bin"})
		Expect(err).To(MatchError(op.ErrNotImplemented))
		Expect(logs.String()).To(ContainSubstring(`"msg":"refusing the olh head of a versioned object: versioning is not implemented","bucket":"plain","key":"v.bin"`))

		olh[meta.AttrCompression] = []byte{0xff}
		head(marker+"_v.bin", nil, olh)
		_, err = s.StatObject(ctx, plain, meta.ObjKey{Name: "v.bin"})
		Expect(err).To(MatchError(op.ErrUnknown), "get_obj_state_impl decodes the compression info before it checks is_olh")
	})

	It("issues one op: the version read, getxattrs, stat and, for a prefetch, one read of rgw_max_chunk_size at 0", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		pool.ReadStub = func(_ context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			for _, step := range rop.Steps() {
				switch st := step.(type) {
				case *radosclient.ExecStep:
					st.Result.Set(encode(version.ReadRet{Objv: version.ObjVersion{Ver: 5, Tag: "t"}}), 0)
				case *radosclient.GetXattrsStep:
					st.Result.Xattrs = map[string][]byte{meta.AttrETag: []byte("e")}
				case *radosclient.StatStep:
					st.Result.Size, st.Result.ModTime = 3, time.Unix(1700000000, 0)
				case *radosclient.ReadStep:
					st.Result.N = copy(st.Buf, "abc")
					st.Result.Data = st.Buf[:st.Result.N]
				}
			}
			return 42, nil
		}
		cluster := fakeClusterWith(pool)
		cfg := testReadConfig()
		cfg.Chunk = 1 << 20
		s2 := driver.NewStoreForTest(cluster, testZone(), cfg)

		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		_, name, ns := cluster.PoolArgsForCall(0)
		Expect([]string{name, ns}).To(Equal([]string{testDataPool, ""}))
		Expect(pool.ReadCallCount()).To(Equal(1))
		_, oid, rop, flags := pool.ReadArgsForCall(0)
		Expect(oid).To(Equal(marker + "_k"))
		Expect(flags).To(Equal(radosclient.OpFlagNone))
		Expect(stepTypes(rop.Steps())).To(Equal([]string{"*radosclient.ExecStep", "*radosclient.GetXattrsStep", "*radosclient.StatStep"}),
			"cls_version_read first (prepare_op_for_read), then getxattrs and stat2: raw_obj_stat, rgw_rados.cc:8843-8851")
		Expect(rop.Steps()[0]).To(And(HaveField("Class", "version"), HaveField("Method", "read")))
		Expect(st.Epoch).To(BeEquivalentTo(42))
		Expect(st.Version).To(Equal(meta.ObjVersion{Ver: 5, Tag: "t"}))
		Expect(st.Head).To(BeNil())

		st, err = s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pool.ReadCallCount()).To(Equal(2))
		_, _, rop, _ = pool.ReadArgsForCall(1)
		steps := rop.Steps()
		Expect(stepTypes(steps)).To(Equal([]string{"*radosclient.ExecStep", "*radosclient.GetXattrsStep", "*radosclient.StatStep", "*radosclient.ReadStep"}))
		Expect(steps[3]).To(And(HaveField("Offset", BeZero()), HaveField("Length", BeEquivalentTo(1<<20)), HaveField("Buf", HaveLen(1<<20))),
			"rgw_max_chunk_size from the store's options, not its default")
		Expect(st.Head).To(Equal([]byte("abc")))
	})

	It("keeps version zero when the version reply does not decode, as cls_version_read's completion does", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		pool.ReadStub = func(_ context.Context, _ string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			for _, step := range rop.Steps() {
				if st, ok := step.(*radosclient.ExecStep); ok {
					st.Result.Set([]byte{0xff}, 0)
				}
			}
			return 7, nil
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{st.Exists, st.Epoch, st.Version}).To(Equal([]any{true, uint64(7), meta.ObjVersion{}}))
	})

	It("maps a failed head read through radosgw's error table", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		pool.ReadReturns(0, &radosclient.Error{Errno: 13, Op: "read"})
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		_, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	Describe("dataPool", func() {
		DescribeTable("resolves the head pool with radosgw's fallbacks",
			func(rule, explicit, want string) {
				b := plain.Info.Bucket
				b.ExplicitPlacement.DataPool = meta.ParsePool(explicit)
				pool, ok := s.DataPoolForTest(meta.ParsePlacementRule(rule), b)
				Expect(ok).To(BeTrue(), "rule %q", rule)
				Expect(pool).To(Equal(meta.ParsePool(want)), "rule %q", rule)
			},
			Entry("the STANDARD class of the rule", "default-placement", "", testDataPool),
			Entry("an explicit placement wins", "default-placement", "legacy.pool", "legacy.pool"),
			Entry("a class with its own pool", "default-placement/COLD", "", "cold.data"),
			Entry("a class the zone no longer has falls back to STANDARD's pool", "default-placement/GONE", "", testDataPool),
			Entry("a class without a pool takes STANDARD's (docs/exclusions.md)", "default-placement/NO_POOL", "", testDataPool),
			Entry("an empty rule falls back to the zonegroup default placement", "", "", testDataPool),
			Entry("a rule naming no placement of the zone falls back to the default", "nope/COLD", "", testDataPool),
		)

		It("takes the default placement's class, not the rule's, on a fallback", func(ctx SpecContext) {
			editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
				zg := p.PeriodMap.ZoneGroups[ids.zgID]
				zg.DefaultPlacement.StorageClass = "COLD"
				p.PeriodMap.ZoneGroups[ids.zgID] = zg
			})
			pool, ok := open(ctx).DataPoolForTest(meta.ParsePlacementRule("nope"), plain.Info.Bucket)
			Expect([]any{pool, ok}).To(Equal([]any{meta.ParsePool("cold.data"), true}))
		})

		It("fails the stat with UnknownError, the head-object helpers' -EIO, when even the default placement is unknown to the zone", func(ctx SpecContext) {
			editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
				zg := p.PeriodMap.ZoneGroups[ids.zgID]
				zg.DefaultPlacement = meta.PlacementRule{Name: "gone"}
				p.PeriodMap.ZoneGroups[ids.zgID] = zg
			})
			s2 := open(ctx)
			pool, ok := s2.DataPoolForTest(meta.ParsePlacementRule("nope"), plain.Info.Bucket)
			Expect([]any{pool, ok}).To(Equal([]any{meta.Pool{}, false}))
			lost := &op.BucketRecord{Info: meta.BucketInfo{Bucket: plain.Info.Bucket, PlacementRule: meta.ParsePlacementRule("nope")}}
			_, err := s2.StatObject(ctx, lost, meta.ObjKey{Name: "k"})
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(err).To(MatchError(ContainSubstring(`no data pool for placement "nope"`)))
		})

		It("fails the stat with InvalidArgument when the placement's STANDARD class has no pool, as librados refuses an empty pool name", func(ctx SpecContext) {
			editRoot(c, meta.ZoneInfoOID(ids.zoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
				pi := z.PlacementPools["default-placement"]
				pi.StorageClasses[meta.StorageClassStandard] = meta.ZoneStorageClass{}
				z.PlacementPools["default-placement"] = pi
			})
			s2 := open(ctx)
			pool, ok := s2.DataPoolForTest(plain.Info.PlacementRule, plain.Info.Bucket)
			Expect([]any{pool, ok}).To(Equal([]any{meta.Pool{}, true}))
			_, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(err).To(MatchError(ContainSubstring(`placement "default-placement" of bucket plain has no name`)))
		})

		It("stats an object in a data pool that does not exist as absent, as radosgw's stat after it creates the pool", func(ctx SpecContext) {
			c.FailPool(testDataPool)
			want := &op.ObjectState{Bucket: plain, Key: meta.ObjKey{Name: "k"}}
			st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st).To(Equal(want))
			st, err = s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st).To(Equal(want))
		})
	})
})

var _ = Describe("the read options", func() {
	var c *fakerados.Cluster
	BeforeEach(func() {
		c = fakerados.New()
		seedRookZone(c, "ceph-objectstore", true)
	})

	DescribeTable("refuse to open on a zero chunk or request size or a window below one request, naming the values",
		func(ctx SpecContext, option, value, wantInMsg string) {
			_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", option: value}), driver.Options{})
			Expect(err).To(MatchError(ContainSubstring(wantInMsg)))
		},
		Entry("a zero chunk", "rgw_max_chunk_size", "0", "rgw_max_chunk_size 0,"),
		Entry("a zero request size", "rgw_get_obj_max_req_size", "0", "rgw_get_obj_max_req_size 0,"),
		Entry("a window below one request", "rgw_get_obj_window_size", "1048576", "rgw_get_obj_window_size 1048576:"),
	)

	It("fails to open naming a read option that does not parse", func(ctx SpecContext) {
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", "rgw_get_obj_window_size": "lots"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring("rgw_get_obj_window_size")))
	})
})
