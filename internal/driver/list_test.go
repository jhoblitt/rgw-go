package driver_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/driver/driverfakes"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// keys is the names of the listed entries, in listing order.
func keys(entries []op.ObjectEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Key.Name)
	}
	return names
}

var _ = Describe("ListObjects", func() {
	const bucketID = "zone-ceph-objectstore.4157.1"
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		stater *driverfakes.FakeHeadStater
	)
	// shardOID is the index shard that holds the object name.
	shardOID := func(name string) string {
		return ".dir." + bucketID + "." + strconv.FormatUint(uint64(shardOf(rec, name)), 10)
	}
	// seedEntry stores e on the shard its object name hashes to.
	seedEntry := func(e rgwcls.DirEntry) {
		k, ok := meta.ParseIndexKeyName(e.Key.Name)
		Expect(ok).To(BeTrue(), e.Key.Name)
		seedIndexEntry(c, rookIndexPool, shardOID(k.Name), e)
	}
	poolID := func(ctx context.Context, name string) int64 {
		p, err := c.Pool(ctx, name, "")
		Expect(err).NotTo(HaveOccurred())
		return p.ID()
	}
	pending := func(e rgwcls.DirEntry, tag string) rgwcls.DirEntry {
		e.PendingMap = []rgwcls.PendingEntry{{Tag: tag, Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: indexMtime, Op: uint8(rgwcls.OpAdd)}}}
		return e
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newIndexCluster()
		var err error
		s, err = driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		rec = testBucket(bucketID, 2)
		seedShards(c, rec)
		stater = &driverfakes.FakeHeadStater{}
		driver.SetHeadStaterForTest(s, stater)
	})

	It("lists in key order across shards with common prefixes and a bounded page", func(ctx SpecContext) {
		for _, n := range []string{"a", "dir/x", "dir/y", "b", "c"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"a", "b", "c"}))
		Expect(res.CommonPrefixes).To(BeEmpty())
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal("c"))
		res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 3, Marker: "c"})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(BeEmpty())
		Expect(res.CommonPrefixes).To(Equal([]string{"dir/"}), "the class rolls the names up, cls_rgw.cc:625-663 at v19.2.6")
		Expect(res.Truncated).To(BeFalse())
		Expect(res.NextMarker).To(BeEmpty(), "no next page")
	})
	It("ends a page at a common prefix and makes it the next marker", func(ctx SpecContext) {
		for _, n := range []string{"a", "dir/x", "e"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"a"}))
		Expect(res.CommonPrefixes).To(Equal([]string{"dir/"}))
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal("dir/"), "rgw_rados.cc:2075 at v19.2.6")
		res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 2, Marker: res.NextMarker})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"e"}))
		Expect(res.CommonPrefixes).To(BeEmpty())
	})
	It("fast-forwards a marker inside a common prefix", func(ctx SpecContext) {
		for _, n := range []string{"dir/x", "dir/y", "e"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 10, Marker: "dir/x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"e"}), "rgw_rados.cc:1844-1854: the marker becomes dir/\\xFF")
		Expect(res.CommonPrefixes).To(BeEmpty())
	})
	It("filters by prefix and rolls up after it", func(ctx SpecContext) {
		for _, n := range []string{"a", "p/x", "p/q/y", "p/q/z", "z"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Prefix: "p/", Delimiter: "/", MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"p/x"}))
		Expect(res.CommonPrefixes).To(Equal([]string{"p/q/"}))
	})
	It("lists an object whose name starts with an underscore and skips the multipart namespace", func(ctx SpecContext) {
		seedEntry(entry("_under", 1))
		mp := entry("x", 1)
		mp.Key.Name = meta.ObjKey{Name: "up.2~x.meta", NS: "multipart"}.IndexKeyName()
		seedEntry(mp)
		seedEntry(entry("z", 1))
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"_under", "z"}), "parse_raw_oid unescapes, rgw_obj_types.h:293-296; the namespaced entry is skipped, rgw_rados.cc:1926-1988")
		Expect(res.Entries[0].Key.NS).To(BeEmpty())
	})
	It("gives the next marker as the object name parsed from the index key", func(ctx SpecContext) {
		seedEntry(entry("_under", 1))
		seedEntry(entry("z", 1))
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"_under"}))
		Expect(res.NextMarker).To(Equal("_under"), "next_marker = index_key converts through parse_index_key, rgw_obj_types.h:120-146")
		res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1, Marker: res.NextMarker})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"z"}), "the marker escapes again and resumes after the key")
	})
	It("lists a namespace with namespace-local names and next marker", func(ctx SpecContext) {
		for _, n := range []string{"a.2~2.meta", "b.2~3.meta"} {
			e := entry("x", 1)
			e.Key.Name = meta.ObjKey{Name: n, NS: "multipart"}.IndexKeyName()
			seedEntry(e)
		}
		seedEntry(entry("plain", 1))
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: "multipart", MaxKeys: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"a.2~2.meta"}))
		Expect(res.Entries[0].Key.NS).To(Equal("multipart"))
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal("a.2~2.meta"))
		res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{NS: "multipart", MaxKeys: 10, Marker: res.NextMarker})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"b.2~3.meta"}))
		Expect(res.Truncated).To(BeFalse())
	})
	Describe("NameFilter", func() {
		seedMultipart := func(names ...string) {
			for _, n := range names {
				e := entry("x", 1)
				e.Key.Name = meta.ObjKey{Name: n, NS: meta.NSMultipart}.IndexKeyName()
				seedEntry(e)
			}
		}
		It("skips a refused name after the marker moves past it and before counting, as access_list_filter does", func(ctx SpecContext) {
			seedMultipart("a.2~1.1", "a.2~1.meta", "a.2~2.1", "a.2~2.meta", "b.2~3.meta")
			p := op.ListObjectsParams{NS: meta.NSMultipart, MaxKeys: 2, NameFilter: meta.IsMultipartMeta}
			res, err := s.ListObjects(ctx, rec, p)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{"a.2~1.meta", "a.2~2.meta"}), "part heads neither count nor appear, rgw_rados.cc:2003-2009 at v19.2.6")
			Expect(res.Truncated).To(BeTrue())
			Expect(res.NextMarker).To(Equal("a.2~2.meta"), "the last counted entry")
			p.Marker = res.NextMarker
			res, err = s.ListObjects(ctx, rec, p)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{"b.2~3.meta"}))
			Expect(res.Truncated).To(BeFalse())
		})
		It("keeps the common prefixes the class rolls refused names into", func(ctx SpecContext) {
			seedMultipart("d/x.2~1.1", "d/x.2~1.meta", "e.2~2.meta", "f/y.2~9.1")
			res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: meta.NSMultipart, Delimiter: "/", MaxKeys: 10, NameFilter: meta.IsMultipartMeta})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{"e.2~2.meta"}))
			Expect(res.CommonPrefixes).To(Equal([]string{"d/", "f/"}),
				"radosgw filters the class's common-prefix entries too, rgw_rados.cc:2003-2009 (docs/exclusions.md)")
		})
		It("filters an unordered listing after the namespace and before the prefix", func(ctx SpecContext) {
			seedMultipart("a.2~1.1", "a.2~1.meta", "b.2~3.meta", "b.2~3.2")
			res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: meta.NSMultipart, MaxKeys: 10, AllowUnordered: true, NameFilter: meta.IsMultipartMeta})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(ConsistOf("a.2~1.meta", "b.2~3.meta"), "rgw_rados.cc:2297-2303 at v19.2.6")
			Expect(res.Truncated).To(BeFalse())
		})
	})
	It("renders an index entry as the listing's object entry", func(ctx SpecContext) {
		e := entry("k", 9)
		e.Meta.AccountedSize, e.Meta.StorageClass, e.Meta.AppendableValue = 7, "", true
		seedEntry(e)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(Equal([]op.ObjectEntry{{
			Key: meta.ObjKey{Name: "k"}, Size: 7, Mtime: indexMtime, ETag: "e-k",
			Owner: meta.UserOwner(meta.UserID{ID: "alice"}), OwnerDisplayName: "Alice",
			StorageClass: meta.StorageClassStandard, IsLatest: true, Exists: true, Appendable: true,
		}}), "accounted_size is the listed Size, an empty class is STANDARD, an unversioned entry is current")
	})
	It("reconciles a pending entry against its head and drops one whose head is gone", func(ctx SpecContext) {
		seedEntry(pending(entry("gone", 1), "tx1"))
		seedEntry(pending(entry("stale", 1), "tx1"))
		stater.StatObjectStub = func(_ context.Context, _ *op.BucketRecord, k meta.ObjKey) (*op.ObjectState, error) {
			if k.Name == "gone" {
				return &op.ObjectState{Key: k, Exists: false}, nil
			}
			return &op.ObjectState{
				Key: k, Exists: true, Size: 77, Mtime: indexMtime, Epoch: 9, ETag: "fresh", WriteTag: "tx2",
				Compression: &meta.CompressionInfo{OrigSize: 100},
				Attrs: map[string][]byte{
					meta.AttrETag:         []byte("fresh\x00"),
					meta.AttrACL:          encode(acl.Policy{Owner: acl.Owner{ID: "bob", DisplayName: "Bob"}}),
					meta.AttrContentType:  []byte("text/plain\x00"),
					meta.AttrStorageClass: []byte("COLD"),
				},
			}, nil
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"stale"}), "rgw_rados.cc:10361-10377 drops the missing head")
		Expect(res.Entries[0].Size).To(BeEquivalentTo(100), "a compressed head's accounted size is its original size, :10385-10386")
		Expect(res.Entries[0].ETag).To(Equal("fresh"))
		Expect(res.Entries[0].StorageClass).To(Equal("COLD"))
		Expect(res.Entries[0].Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "bob"})), "the owner comes from the head's ACL, :10401-10407")

		gone := shardOID("gone")
		Eventually(func() []rgwcls.Suggestion { return c.Suggestions(rookIndexPool, "", gone) }).
			WithTimeout(time.Second).WithPolling(time.Millisecond).Should(ContainElement(And(
			HaveField("Op", rgwcls.SuggestRemove),
			HaveField("Log", false),
			HaveField("Entry.Key.Name", "gone"),
			HaveField("Entry.PendingMap", BeEmpty()),
			HaveField("Entry.Ver", rgwcls.EntryVer{Pool: poolID(ctx, rookIndexPool)}),
		)), "log_data is false in a single zone; the removal carries the index pool and epoch 0, :10373")
		stale := shardOID("stale")
		Eventually(func() []rgwcls.Suggestion { return c.Suggestions(rookIndexPool, "", stale) }).
			WithTimeout(time.Second).WithPolling(time.Millisecond).Should(ContainElement(And(
			HaveField("Op", rgwcls.SuggestUpdate),
			HaveField("Entry.Key.Name", "stale"),
			HaveField("Entry.Exists", true),
			HaveField("Entry.Tag", "tx2"),
			HaveField("Entry.Meta", rgwcls.DirEntryMeta{
				Category: rgwcls.CategoryMain, Size: 77, AccountedSize: 100, Mtime: indexMtime, ETag: "fresh",
				Owner: "bob", OwnerDisplayName: "Bob", ContentType: "text/plain", StorageClass: "COLD",
			}),
			HaveField("Entry.Ver", rgwcls.EntryVer{Pool: poolID(ctx, "ceph-objectstore.rgw.buckets.data"), Epoch: 9}),
		)), "the head's state wins, :10440-10467")
	})
	It("lists a delete marker without a head check and skips it unless versions are listed", func(ctx SpecContext) {
		dm := entry("dm", 0)
		dm.Exists, dm.Flags = false, rgwcls.FlagDeleteMarker
		seedEntry(dm)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(BeEmpty())
		res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10, ListVersions: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"dm"}))
		Expect(res.Entries[0].DeleteMarker).To(BeTrue())
		Expect(stater.StatObjectCallCount()).To(BeZero())
	})
	It("keeps an entry and sends nothing when the object store is not implemented yet", func(ctx SpecContext) {
		seedEntry(pending(entry("p", 1), "t"))
		stater.StatObjectReturns(nil, op.ErrNotImplemented)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"p"}))
		Consistently(func() int { return c.Writes(rookIndexPool, "", shardOID("p")) }).
			WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(BeZero())
	})
	It("fails the listing when a head check fails", func(ctx SpecContext) {
		seedEntry(pending(entry("p", 1), "t"))
		stater.StatObjectReturns(nil, op.ErrInternalError)
		_, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).To(MatchError(op.ErrInternalError), "rgw_rados.cc:9836-9841")
	})
	It("leaves a pending multipart upload entry alone", func(ctx SpecContext) {
		mp := pending(entry("x", 1), "t")
		mp.Key.Name = meta.ObjKey{Name: "up.2~x.meta", NS: "multipart"}.IndexKeyName()
		seedEntry(mp)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: "multipart", MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"up.2~x.meta"}))
		Expect(stater.StatObjectCallCount()).To(BeZero(), "radosgw stats it in the data-extra pool, rgw_rados.cc:10341-10344 (docs/exclusions.md)")
	})
	DescribeTable("sends suggestions unguarded on Squid and guarded on Tentacle",
		func(ctx SpecContext, release denc.Release, methods []string) {
			st, err := driver.Open(ctx, c, conf(nil), driver.Options{Release: &release})
			Expect(err).NotTo(HaveOccurred())
			driver.SetHeadStaterForTest(st, stater)
			seedEntry(pending(entry("gone", 1), "tx1"))
			stater.StatObjectReturns(&op.ObjectState{Exists: false}, nil)
			_, err = st.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
			Expect(err).NotTo(HaveOccurred())
			oid := shardOID("gone")
			Eventually(func() int { return c.Writes(rookIndexPool, "", oid) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1))
			var got []string
			for _, step := range c.LastWrite(rookIndexPool, "", oid).Steps() {
				switch v := step.(type) {
				case *radosclient.AssertExistsStep:
					got = append(got, "assert_exists")
				case *radosclient.ExecStep:
					got = append(got, v.Method)
				default:
					got = append(got, fmt.Sprintf("%T", v))
				}
			}
			Expect(got).To(Equal(methods))
		},
		Entry("Squid, v19.2.6 rgw_rados.cc:9899-9912", denc.Squid, []string{"dir_suggest_changes"}),
		Entry("Tentacle, v20.2.4 rgw_rados.cc:10819-10832", denc.Tentacle, []string{"assert_exists", "guard_bucket_resharding", "dir_suggest_changes"}),
	)
	It("lists unordered by shard without common prefixes", func(ctx SpecContext) {
		for _, n := range []string{"a", "b", "c", "d"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 3, AllowUnordered: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(HaveLen(3))
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal(res.Entries[2].Key.Name))
		res2, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 3, AllowUnordered: true, Marker: res.NextMarker})
		Expect(err).NotTo(HaveOccurred())
		Expect(res2.Truncated).To(BeFalse())
		Expect(append(keys(res.Entries), keys(res2.Entries)...)).To(ConsistOf("a", "b", "c", "d"))
	})
	It("refuses an unordered listing with a delimiter", func(ctx SpecContext) {
		_, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 3, AllowUnordered: true, Delimiter: "/"})
		Expect(err).To(MatchError(op.ErrInvalidArgument), "rgw_op.cc:3085-3090 at v19.2.6")
	})
	It("answers a zero page with nothing, truncated when an entry exists, and no next marker", func(ctx SpecContext) {
		seedEntry(entry("a", 1))
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 0})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(BeEmpty())
		Expect(res.Truncated).To(BeTrue(), "rgw_rados.cc:2089-2095; the S3 handler renders max && is_truncated")
		Expect(res.NextMarker).To(BeEmpty(), "only an entry counted below max sets it, :1998-2001")
	})
	It("answers NoSuchKey for a missing index shard, radosgw's ENOENT", func(ctx SpecContext) {
		missing := testBucket("zone-ceph-objectstore.4157.9", 2)
		_, err := s.ListObjects(ctx, missing, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).To(MatchError(op.ErrNoSuchKey))
	})
	It("checks a pending plain object named like a multipart meta object in the data pool", func(ctx SpecContext) {
		seedEntry(pending(entry("a.b.meta", 1), "t"))
		stater.StatObjectReturns(&op.ObjectState{Exists: true, Size: 5, Mtime: indexMtime}, nil)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"a.b.meta"}))
		Expect(stater.StatObjectCallCount()).To(Equal(1))
		_, _, key := stater.StatObjectArgsForCall(0)
		Expect(key).To(Equal(meta.ObjKey{Name: "a.b.meta"}),
			"radosgw's MultipartMetaFilter sends it to the data-extra pool, rgw_rados.cc:10341-10344 (docs/exclusions.md)")
		Expect(res.Entries[0].Size).To(BeEquivalentTo(5))
	})
	It("drops the suggestions rather than wait when every slot is busy", func(ctx SpecContext) {
		release := driver.HoldSuggestSlotsForTest(s)
		seedEntry(pending(entry("gone", 1), "tx1"))
		stater.StatObjectReturns(&op.ObjectState{Exists: false}, nil)
		lctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		start := time.Now()
		res, err := s.ListObjects(lctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(time.Since(start)).To(BeNumerically("<", 500*time.Millisecond), "the listing does not wait for a slot")
		Expect(res.Entries).To(BeEmpty())
		release()
		Consistently(func() int { return c.Writes(rookIndexPool, "", shardOID("gone")) }).
			WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).Should(BeZero(),
			"radosgw's aio_operate never blocks the listing, rgw_rados.cc:9903 at v19.2.6")
	})
	DescribeTable("loses an object starting with an underscore under a delimiter starting with one, as radosgw does",
		func(ctx SpecContext, delim string, want []string) {
			seedEntry(entry("_x", 1))
			seedEntry(entry("a", 1))
			res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: delim, MaxKeys: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal(want))
			Expect(res.CommonPrefixes).To(BeEmpty())
		},
		Entry("_: the class rolls __x up to the prefix _, which parse_raw_oid refuses (rgw_rados.cc:1912-1917 at v19.2.6)", "_", []string{"a"}),
		Entry("__: the prefix __ parses as the object _, listed as an entry", "__", []string{"_", "a"}),
	)

	Describe("across page boundaries on a sharded bucket", func() {
		var small *driver.Store
		// namesOn is n names prefix000, prefix001, ... that hash to shard.
		namesOn := func(prefix string, shard uint32, n int) []string {
			var out []string
			for i := 0; len(out) < n; i++ {
				name := fmt.Sprintf("%s%03d", prefix, i)
				if shardOf(rec, name) == shard {
					out = append(out, name)
				}
			}
			return out
		}
		reads := func(shard uint32) int {
			return c.Reads(rookIndexPool, "", ".dir."+bucketID+"."+strconv.FormatUint(uint64(shard), 10))
		}
		BeforeEach(func(ctx SpecContext) {
			var err error
			small, err = driver.Open(ctx, c, conf(map[string]string{"rgw_list_bucket_min_readahead": "1"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			driver.SetHeadStaterForTest(small, stater)
		})
		It("stops a round when a truncated shard runs out and returns the half page it has", func(ctx SpecContext) {
			as := namesOn("a", 0, 12)
			bs := namesOn("b", 1, 4)
			for _, n := range append(slices.Clone(as), bs...) {
				seedEntry(entry(n, 1))
			}
			res, err := small.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 9})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal(as[:8]),
				"each shard is asked for 8 (calcPerShard, min_read); shard 0 runs out truncated, rgw_rados.cc:9871-9895")
			Expect(res.Truncated).To(BeTrue())
			Expect(res.NextMarker).To(Equal(as[7]))
			Expect([]int{reads(0), reads(1)}).To(Equal([]int{1, 1}), "8 of 9 is at least half the page, one round, :2133-2141")
			res, err = small.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 9, Marker: res.NextMarker})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal(append(slices.Clone(as[8:]), bs...)))
			Expect(res.Truncated).To(BeFalse())
		})
		It("runs another round from the last entry visited when a round keeps nothing", func(ctx SpecContext) {
			as := namesOn("a", 0, 10)
			bs := namesOn("b", 1, 4)
			for _, n := range as {
				seedEntry(pending(entry(n, 1), "t"))
			}
			for _, n := range bs {
				seedEntry(entry(n, 1))
			}
			stater.StatObjectReturns(&op.ObjectState{Exists: false}, nil)
			res, err := small.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 4})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal(bs))
			Expect(res.Truncated).To(BeFalse())
			Expect([]int{reads(0), reads(1)}).To(Equal([]int{2, 2}), "each round asks each shard for 5 and drops five gone heads, :1862-1896, :9675-9687")
			Expect(stater.StatObjectCallCount()).To(Equal(10))
		})
		It("skips a run of namespaced names with another round", func(ctx SpecContext) {
			for i := range 10 {
				e := entry("x", 1)
				e.Key.Name = meta.ObjKey{Name: fmt.Sprintf("up%02d.2~x.meta", i), NS: "multipart"}.IndexKeyName()
				seedEntry(e)
			}
			for _, n := range []string{"z1", "z2", "z3"} {
				seedEntry(entry(n, 1))
			}
			res, err := small.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 4})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{"z1", "z2", "z3"}))
			Expect(res.Truncated).To(BeFalse())
			Expect([]int{reads(0), reads(1)}).To(Equal([]int{2, 2}), "the marker moves to _\\xFF for a second round, rgw_rados.cc:1959-1980")
		})
		It("ends an unordered page on an escaped name and resumes after it", func(ctx SpecContext) {
			u := namesOn("_u", 0, 1)[0]
			v := namesOn("v", 0, 1)[0]
			seedEntry(entry(u, 1))
			seedEntry(entry(v, 1))
			res, err := small.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1, AllowUnordered: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{u}))
			Expect(res.Truncated).To(BeTrue())
			Expect(res.NextMarker).To(Equal(u), "next_marker.set parses the escaped index name, rgw_rados.cc:2256-2259")
			res, err = small.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 1, AllowUnordered: true, Marker: res.NextMarker})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{v}))
			Expect(res.Truncated).To(BeFalse())
		})
		It("starts an unordered multipart listing at the shard its upload's key hashes to", func(ctx SpecContext) {
			var m string
			for i := 0; m == ""; i++ {
				cand := fmt.Sprintf("m%03d", i)
				if shardOf(rec, cand) == 1 && shardOf(rec, cand+".2~u1.meta") == 0 {
					m = cand
				}
			}
			n := namesOn("n", 0, 1)[0]
			seedMeta := func(obj, upload string, shard uint32) {
				e := entry("x", 1)
				e.Key.Name = meta.ObjKey{Name: obj + ".2~" + upload + ".meta", NS: "multipart"}.IndexKeyName()
				seedIndexEntry(c, rookIndexPool, ".dir."+bucketID+"."+strconv.FormatUint(uint64(shard), 10), e)
			}
			seedMeta(m, "u1", 1)
			seedMeta(m, "u2", 1)
			seedMeta(n, "u3", 0)
			res, err := small.ListObjects(ctx, rec, op.ListObjectsParams{NS: "multipart", MaxKeys: 10, AllowUnordered: true, Marker: m + ".2~u1.meta"})
			Expect(err).NotTo(HaveOccurred())
			Expect(keys(res.Entries)).To(Equal([]string{m + ".2~u2.meta"}),
				"parse_index_hash_source hashes the upload's key, rgw_rados.cc:10033-10047; shard 0 comes before it")
			Expect(reads(0)).To(BeZero())
		})
	})
})
