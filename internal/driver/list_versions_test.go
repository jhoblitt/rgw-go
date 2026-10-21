package driver_test

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/driver/driverfakes"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// versionIDs is the listed entries as name@instance, "null" for an empty
// instance, in listing order.
func versionIDs(entries []op.ObjectEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Key.Name+"@"+orNull(e.Key.Instance))
	}
	return out
}

// orNull is instance, or "null" for the null version's empty one, as S3
// names it.
func orNull(instance string) string {
	if instance == "" {
		return "null"
	}
	return instance
}

var _ = Describe("ListObjects with versions", func() {
	const (
		bucketID = "zone-ceph-objectstore.4158.1"
		olhTag   = "olhtag-olhtag-olhtag-olhtag-0001"
	)
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		stater *driverfakes.FakeHeadStater
	)
	open := func(ctx context.Context, kv map[string]string) *driver.Store {
		GinkgoHelper()
		st, err := driver.Open(ctx, c, conf(kv), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		st.SetReshardWaitForTest(time.Millisecond)
		driver.SetHeadStaterForTest(st, stater)
		return st
	}
	useBucket := func(shards uint32) {
		rec = testBucket(bucketID, shards)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
	}
	BeforeEach(func(ctx SpecContext) {
		c = newIndexCluster()
		stater = &driverfakes.FakeHeadStater{}
		s = open(ctx, nil)
		useBucket(1)
	})
	head := func(name string) *op.ObjectState {
		return &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: name}, Exists: true, Attrs: map[string][]byte{meta.AttrOLHIDTag: []byte(olhTag)}}
	}
	// version gives name a version, instance "" for the null one, as a
	// versioned write leaves it: the prepare and the awaited completion of its
	// instance entry, then its link, which makes it current.
	version := func(ctx context.Context, name, instance string) {
		GinkgoHelper()
		key := meta.ObjKey{Name: name, Instance: instance}
		x := s.NewIndexOpForTest(rec, key, "write-"+name+"-"+instance)
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 1},
			rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: "e-" + name + "-" + instance})).To(Succeed())
		Expect(s.NewOLHIndexForTest(rec, meta.ObjKey{Name: name}).LinkOLH(ctx, head(name), key, "link-"+name+"-"+instance, driver.OLHLinkFields{})).To(Succeed())
	}
	// marker gives name a delete marker, instance "" for the null one.
	marker := func(ctx context.Context, name, instance string) {
		GinkgoHelper()
		f := driver.OLHLinkFields{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", OwnerDisplayName: "Alice", Mtime: indexMtime}}
		Expect(s.NewOLHIndexForTest(rec, meta.ObjKey{Name: name}).LinkOLH(ctx, head(name), meta.ObjKey{Name: name, Instance: instance}, "dm-"+name+"-"+instance, f)).To(Succeed())
	}
	list := func(ctx context.Context, st *driver.Store, p op.ListObjectsParams) op.ListObjectsResult {
		GinkgoHelper()
		res, err := st.ListObjects(ctx, rec, p)
		Expect(err).NotTo(HaveOccurred())
		return res
	}
	// pageAll lists every page of p as a client pages a listing, each next
	// page from NextMarker and NextMarkerInstance copied back unchanged.
	pageAll := func(ctx context.Context, st *driver.Store, p op.ListObjectsParams) (ids []string, pages int) {
		GinkgoHelper()
		for pages = 1; ; pages++ {
			Expect(pages).To(BeNumerically("<", 100), "the listing ends")
			res := list(ctx, st, p)
			prefixes := res.CommonPrefixes
			for i, e := range res.Entries {
				for len(prefixes) > 0 && prefixes[0] < e.Key.Name {
					ids, prefixes = append(ids, prefixes[0]), prefixes[1:]
				}
				ids = append(ids, versionIDs(res.Entries[i:i+1])...)
			}
			ids = append(ids, prefixes...)
			if !res.Truncated {
				return ids, pages
			}
			p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
		}
	}
	shardOID := func(name string) string {
		sh, _ := meta.IndexShard(name, rec.Info.Layout.Current.Layout.Normal.NumShards)
		return rec.Info.IndexShardOID(rec.Info.Layout.Current, sh)
	}

	Describe("the marker", func() {
		// k holds v1 at epoch 2, the null version at 3 and v3 at 4, current,
		// and z one version.
		BeforeEach(func(ctx SpecContext) {
			for _, id := range []string{"v1", "", "v3"} {
				version(ctx, "k", id)
			}
			version(ctx, "z", "z1")
		})
		// The class resolves the start key through encode_list_index_key
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L440 at v20.2.4).
		DescribeTable("reaches the class's start key, ordered and unordered",
			func(ctx SpecContext, instance string, want []string) {
				for _, unordered := range []bool{false, true} {
					res := list(ctx, s, op.ListObjectsParams{MaxKeys: 10, ListVersions: true, AllowUnordered: unordered, Marker: "k", MarkerInstance: instance})
					Expect(versionIDs(res.Entries)).To(Equal(want), "unordered %v", unordered)
					Expect(res.Truncated).To(BeFalse())
				}
			},
			// radosgw starts after the plain key and lists them again
			// (docs/exclusions.md, "A versions listing starts after every
			// version of a key-marker given alone").
			Entry("without an instance it starts after every version of the key, as S3 does", "", []string{"z@z1"}),
			Entry("null starts after the null version", "null", []string{"k@v1", "z@z1"}),
			Entry("an existing instance starts after that version", "v3", []string{"k@null", "k@v1", "z@z1"}),
			Entry("a stale instance skips the rest of its key", "gone", []string{"z@z1"}),
		)

		It("names the last entry visited, name and instance, as the next marker, null for the null version", func(ctx SpecContext) {
			for _, unordered := range []bool{false, true} {
				p := op.ListObjectsParams{MaxKeys: 2, ListVersions: true, AllowUnordered: unordered}
				res := list(ctx, s, p)
				Expect(versionIDs(res.Entries)).To(Equal([]string{"k@v3", "k@null"}), "unordered %v", unordered)
				Expect(res.Truncated).To(BeTrue())
				Expect([]string{res.NextMarker, res.NextMarkerInstance}).To(Equal([]string{"k", "null"}),
					"as NextVersionIdMarker names it, rgw_rest_s3.cc:1817-1825 at v19.2.7")
				p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
				Expect(versionIDs(list(ctx, s, p).Entries)).To(Equal([]string{"k@v1", "z@z1"}),
					"copied back unchanged, the marker resumes after the null version, unordered %v", unordered)
				p = op.ListObjectsParams{MaxKeys: 1, ListVersions: true, AllowUnordered: unordered}
				res = list(ctx, s, p)
				Expect([]string{res.NextMarker, res.NextMarkerInstance}).To(Equal([]string{"k", "v3"}), "unordered %v", unordered)
			}
		})

		It("starts after every version of a key-marker given alone, and before an object named like it and 0x01", func(ctx SpecContext) {
			seedIndexEntry(c, rookIndexPool, shardOID("k\x01"), entry("k\x01", 1))
			for _, unordered := range []bool{false, true} {
				res := list(ctx, s, op.ListObjectsParams{MaxKeys: 10, ListVersions: true, AllowUnordered: unordered, Marker: "k"})
				Expect(versionIDs(res.Entries)).To(Equal([]string{"k\x01@null", "z@z1"}), "unordered %v", unordered)
			}
		})

		It("copies the versioned epoch and category of each entry", func(ctx SpecContext) {
			res := list(ctx, s, op.ListObjectsParams{MaxKeys: 10, ListVersions: true})
			epochs := map[string]uint64{}
			for _, e := range res.Entries {
				epochs[e.Key.Name+"@"+orNull(e.Key.Instance)] = e.VersionedEpoch
				Expect(e.Category).To(Equal(rgwcls.CategoryMain), e.Key.Name)
			}
			Expect(epochs).To(Equal(map[string]uint64{"k@v1": 2, "k@null": 3, "k@v3": 4, "z@z1": 2}))
		})
	})

	It("names a delete marker's instance as the next marker and resumes after it", func(ctx SpecContext) {
		version(ctx, "k", "v1")
		marker(ctx, "k", "dm2")
		version(ctx, "k", "v3")
		for _, unordered := range []bool{false, true} {
			p := op.ListObjectsParams{MaxKeys: 2, ListVersions: true, AllowUnordered: unordered}
			res := list(ctx, s, p)
			Expect(versionIDs(res.Entries)).To(Equal([]string{"k@v3", "k@dm2"}), "unordered %v", unordered)
			Expect(res.Entries[1].DeleteMarker).To(BeTrue())
			Expect(res.Entries[1].IsLatest).To(BeFalse())
			Expect([]string{res.NextMarker, res.NextMarkerInstance}).To(Equal([]string{"k", "dm2"}))
			p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
			res = list(ctx, s, p)
			Expect(versionIDs(res.Entries)).To(Equal([]string{"k@v1"}), "unordered %v", unordered)
		}
	})

	// The class resolves null to the null data key alone
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L366-L381 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L416-L431 at v20.2.4),
	// which a null delete marker does not have: it starts past every version
	// of the key, or after the old null version a marker's link left behind
	// (docs/exclusions.md, "A versions listing resumes after the null delete
	// marker its version-id-marker names").
	DescribeTable("resumes a page that ended on a null delete marker at the key's next version",
		func(ctx SpecContext, setup func(context.Context), page1, rest []string) {
			setup(ctx)
			for _, unordered := range []bool{false, true} {
				p := op.ListObjectsParams{MaxKeys: len(page1), ListVersions: true, AllowUnordered: unordered}
				res := list(ctx, s, p)
				Expect(versionIDs(res.Entries)).To(Equal(page1), "unordered %v", unordered)
				Expect(res.Entries[len(page1)-1].DeleteMarker).To(BeTrue())
				Expect([]string{res.NextMarker, res.NextMarkerInstance}).To(Equal([]string{"k", "null"}))
				p.MaxKeys, p.Marker, p.MarkerInstance = 10, res.NextMarker, res.NextMarkerInstance
				Expect(versionIDs(list(ctx, s, p).Entries)).To(Equal(rest), "unordered %v", unordered)
			}
		},
		Entry("with no null version before it: radosgw starts past the key",
			func(ctx context.Context) {
				version(ctx, "k", "v1")
				marker(ctx, "k", "")
				version(ctx, "k", "v3")
				version(ctx, "z", "z1")
			},
			[]string{"k@v3", "k@null"}, []string{"k@v1", "z@z1"}),
		Entry("over the null version its link replaced: radosgw starts after that version's old list key",
			func(ctx context.Context) {
				version(ctx, "k", "")
				version(ctx, "k", "v2")
				marker(ctx, "k", "")
				version(ctx, "z", "z1")
			},
			[]string{"k@null"}, []string{"k@v2", "z@z1"}),
	)

	// A round that ends on the null version resumes after it: radosgw's
	// marker, the entry's key, names no instance, so the class would start
	// after the plain key and list the key's newer versions again
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10130-L10133 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10856-L10859 at v20.2.4).
	It("resumes a round that ended on the null version after it, listing each version once", func(ctx SpecContext) {
		useBucket(4)
		small := open(ctx, map[string]string{"rgw_list_bucket_min_readahead": "1"})
		var want []string
		for i := 1; i <= 20; i++ {
			id := fmt.Sprintf("v%02d", i)
			if i == 5 {
				id = ""
			}
			version(ctx, "a", id)
			want = append([]string{"a@" + orNull(id)}, want...)
		}
		Expect(want[15]).To(Equal("a@null"), "the null version is the sixteenth newest")
		for i := range 10 {
			name := "b" + strconv.Itoa(i)
			version(ctx, name, "x")
			want = append(want, name+"@x")
		}
		before := c.Reads(rookIndexPool, "", shardOID("a"))
		res := list(ctx, small, op.ListObjectsParams{MaxKeys: 40, ListVersions: true})
		Expect(versionIDs(res.Entries)).To(Equal(want))
		Expect(res.Truncated).To(BeFalse())
		Expect(c.Reads(rookIndexPool, "", shardOID("a"))-before).To(Equal(2),
			"each shard is asked for 16 of 41 (calcPerShard), so a's shard runs out truncated on the null version and a second round reads it again")
		l := lastListOp(c, shardOID("a"))
		Expect(l.StartObj).To(Equal(rgwcls.ObjKey{Name: rgwcls.ListIndexKey(rgwcls.ObjKey{Name: "a"}, 6) + "\x00"}),
			"the second round starts right after the null version's list key, which the class takes whole for a key with no instance")
	})

	Describe("over a run of delete markers and noncurrent versions", func() {
		// Forty names each hold a version and, current, a delete marker, before
		// one visible version. Each bucket_list call makes eight omap reads
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L492 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L542 at v20.2.4);
		// reading eight keys or two at a time, they list nothing and the class
		// answers RGWBIAdvanceAndRetryError with the marker to go on from.
		BeforeEach(func(ctx SpecContext) {
			for i := range 40 {
				name := fmt.Sprintf("d%02d", i)
				version(ctx, name, "v1")
				marker(ctx, name, "dm2")
			}
			version(ctx, "zz", "v1")
		})
		DescribeTable("completes a listing without versions by listing the shard again from the marker",
			func(ctx SpecContext, unordered bool) {
				small := open(ctx, map[string]string{"rgw_list_bucket_min_readahead": "1"})
				oid := shardOID("zz")
				before := c.Reads(rookIndexPool, "", oid)
				res := list(ctx, small, op.ListObjectsParams{MaxKeys: 1, AllowUnordered: unordered})
				Expect(versionIDs(res.Entries)).To(Equal([]string{"zz@v1"}))
				Expect(res.Truncated).To(BeFalse())
				Expect(c.Reads(rookIndexPool, "", oid)-before).To(BeNumerically(">", 1), "the shard answered advance-and-retry and was listed again")
			},
			// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L161-L166 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L948-L968 at v20.2.4
			Entry("ordered, as radosgw lists the shard again", false),
			// radosgw's unordered listing fails with the class's -EFBIG
			// (docs/exclusions.md, "An unordered listing lists a shard that
			// answers advance-and-retry again from its marker").
			Entry("unordered", true),
		)
		DescribeTable("fails a listing whose shard answers advance-and-retry with no marker past its start",
			func(ctx SpecContext, marker string) {
				rgw := fakerados.RGWClass()
				c.RegisterClassMethods("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
					if call.Method != "bucket_list" {
						return rgw(call)
					}
					d := denc.NewDecoder(call.In)
					l := rgwcls.DecodeListOp(d)
					if d.Err() != nil {
						return nil, -int32(syscall.EINVAL)
					}
					return encode(rgwcls.ListRet{IsTruncated: true, Marker: l.StartObj}), -int32(syscall.EFBIG)
				}, fakerados.RGWMethods, fakerados.RGWWriteMethods)
				for _, unordered := range []bool{false, true} {
					_, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10, Marker: marker, AllowUnordered: unordered})
					Expect(err).To(MatchError(op.ErrUnknown), "unordered %v", unordered)
					Expect(err).To(MatchError(ContainSubstring("advance-and-retry with no marker past")), "unordered %v", unordered)
				}
			},
			// ListReader lists such a shard again from its start key without
			// end
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L954-L956 at v20.2.4);
			// the timeout fails a listing that does so here in seconds.
			Entry("an empty marker", "", SpecTimeout(5*time.Second)),
			Entry("the start key again", "d05", SpecTimeout(5*time.Second)),
		)
		// The reply's marker past a name's versions is the name and 0x01,
		// cls_rgw_after_versions
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L617-L618 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L667-L668 at v20.2.4),
		// which an omap read starting there leaves out.
		DescribeTable("lists again from a run's end past an object named like the last name and 0x01",
			func(ctx SpecContext, unordered bool, numEntries uint32) {
				pool, err := c.Pool(ctx, rookIndexPool, "")
				Expect(err).NotTo(HaveOccurred())
				rop := radosclient.NewReadOp()
				r := rgwcls.BucketList(rop, rgwcls.ListOp{NumEntries: numEntries}, denc.Squid)
				_, err = pool.Read(ctx, shardOID("zz"), rop, radosclient.OpFlagNone)
				Expect(err).To(MatchError(radosclient.ErrTooBig), "the class answers advance-and-retry with -EFBIG")
				ret, retry, err := r.Reply()
				Expect(err).NotTo(HaveOccurred())
				Expect(retry).To(BeTrue(), "the listing's first read of the shard answers advance-and-retry")
				Expect(ret.Marker.Name).To(MatchRegexp(`^d[0-9]{2}\x01$`))
				seedIndexEntry(c, rookIndexPool, shardOID("zz"), entry(ret.Marker.Name, 1))
				small := open(ctx, map[string]string{"rgw_list_bucket_min_readahead": "1"})
				res := list(ctx, small, op.ListObjectsParams{MaxKeys: 1, AllowUnordered: unordered})
				Expect(versionIDs(res.Entries)).To(Equal([]string{ret.Marker.Name + "@null"}))
			},
			Entry("ordered, each shard asked for 8", false, uint32(8)),
			Entry("unordered, each read asked for 2", true, uint32(2)),
		)
		It("lists the versions of the run with a page of each", func(ctx SpecContext) {
			small := open(ctx, map[string]string{"rgw_list_bucket_min_readahead": "1"})
			var want []string
			for i := range 40 {
				want = append(want, fmt.Sprintf("d%02d@dm2", i), fmt.Sprintf("d%02d@v1", i))
			}
			ids, _ := pageAll(ctx, small, op.ListObjectsParams{MaxKeys: 7, ListVersions: true})
			Expect(ids).To(Equal(append(want, "zz@v1")))
		})
	})

	It("pages an unordered listing with versions inside one key without a gap or a repeat", func(ctx SpecContext) {
		for _, id := range []string{"v1", "v2", "", "v4", "v5"} {
			version(ctx, "k", id)
		}
		marker(ctx, "k", "dm6")
		version(ctx, "k", "v7")
		for _, maxKeys := range []int{1, 2, 3} {
			ids, pages := pageAll(ctx, s, op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: true, AllowUnordered: true})
			Expect(ids).To(Equal([]string{"k@v7", "k@dm6", "k@v5", "k@v4", "k@null", "k@v2", "k@v1"}), "max-keys %d", maxKeys)
			Expect(pages).To(Equal((7+maxKeys-1)/maxKeys), "every page is full, max-keys %d", maxKeys)
		}
	})

	// A page that ends on a null version or a null delete marker resumes from
	// that version's list key, on the shard its name, before the NUL, hashes
	// to.
	It("pages an unordered listing with versions across shards without a gap or a repeat", func(ctx SpecContext) {
		useBucket(4)
		var want []string
		for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			version(ctx, n, "")
			version(ctx, n, n+"2")
			marker(ctx, n, "")
			version(ctx, n, n+"4")
			want = append(want, n+"@"+n+"4", n+"@null", n+"@"+n+"2")
		}
		for _, maxKeys := range []int{1, 2, 3, 5} {
			ids, _ := pageAll(ctx, s, op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: true, AllowUnordered: true})
			Expect(ids).To(ConsistOf(want), "max-keys %d", maxKeys)
		}
	})

	// radosgw hashes the whole name of the marker's object to choose the
	// shard to start on
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10215-L10246 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10941-L10972 at v20.2.4);
	// object names may hold U+0000.
	It("pages an unordered listing across an object whose name holds a NUL", func(ctx SpecContext) {
		useBucket(4)
		// The name with a NUL hashes to a later shard than the part before
		// the NUL, and that earlier shard holds another name, which a page
		// resumed on it would list again.
		nul, other := "", ""
		for i := 0; nul == ""; i++ {
			if n := fmt.Sprintf("a\x00%d", i); shardOf(rec, "a") < shardOf(rec, n) {
				nul = n
			}
		}
		for i := 0; other == ""; i++ {
			if n := fmt.Sprintf("b%d", i); shardOf(rec, n) == shardOf(rec, "a") {
				other = n
			}
		}
		names := []string{nul, other, "c", "d", "e", "f", "g", "h"}
		var want []string
		for _, n := range names {
			seedIndexEntry(c, rookIndexPool, shardOID(n), entry(n, 1))
			want = append(want, n+"@null")
		}
		for _, maxKeys := range []int{1, 2, 3} {
			for _, versions := range []bool{false, true} {
				ids, _ := pageAll(ctx, s, op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: versions, AllowUnordered: true})
				Expect(ids).To(ConsistOf(want), "max-keys %d, versions %v", maxKeys, versions)
			}
		}
	})

	// A start key that ends in the delimiter moves past the subdirectory it
	// names (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L548-L554 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L598-L604 at v20.2.4),
	// and the null version's list key ends in "i".
	It("resumes after the null version under a delimiter its list key ends with, keeping a version of its epoch", func(ctx SpecContext) {
		version(ctx, "k", "v1")
		version(ctx, "k", "")
		key := meta.ObjKey{Name: "k", Instance: "x3"}
		x := s.NewIndexOpForTest(rec, key, "write-k-x3")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5})).To(Succeed())
		Expect(s.NewOLHIndexForTest(rec, meta.ObjKey{Name: "k"}).LinkOLH(ctx, head("k"), key, "link-k-x3", driver.OLHLinkFields{Epoch: 3})).To(Succeed())
		for _, maxKeys := range []int{1, 2, 10} {
			ids, _ := pageAll(ctx, s, op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: true, Delimiter: "i"})
			Expect(ids).To(Equal([]string{"k@null", "k@x3", "k@v1"}), "x3 shares the null version's epoch, max-keys %d", maxKeys)
		}
	})

	It("pages a delimited versions listing through a common prefix and the versions around it", func(ctx SpecContext) {
		version(ctx, "a", "a1")
		version(ctx, "a", "a2")
		version(ctx, "d/x", "x1")
		version(ctx, "d/y", "")
		version(ctx, "e", "")
		version(ctx, "e", "e2")
		version(ctx, "e", "e3")
		for _, maxKeys := range []int{1, 2, 3, 10} {
			ids, _ := pageAll(ctx, s, op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: true, Delimiter: "/"})
			Expect(ids).To(Equal([]string{"a@a2", "a@a1", "d/", "e@e3", "e@e2", "e@null"}), "max-keys %d", maxKeys)
		}
		res := list(ctx, s, op.ListObjectsParams{MaxKeys: 3, ListVersions: true, Delimiter: "/"})
		Expect(res.CommonPrefixes).To(Equal([]string{"d/"}))
		Expect([]string{res.NextMarker, res.NextMarkerInstance}).To(Equal([]string{"d/", ""}), "a common prefix's next marker has no instance, rgw_rados.cc:2088 at v19.2.7")
	})

	It("pages a versions listing under a prefix through a nested common prefix", func(ctx SpecContext) {
		version(ctx, "c", "c1")
		version(ctx, "d/a", "")
		version(ctx, "d/a", "a2")
		marker(ctx, "d/a", "")
		version(ctx, "d/e/x", "x1")
		version(ctx, "d/e/y", "y1")
		version(ctx, "d/f", "f1")
		version(ctx, "d/f", "")
		version(ctx, "e", "e1")
		for _, maxKeys := range []int{1, 2, 3, 10} {
			ids, _ := pageAll(ctx, s, op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: true, Prefix: "d/", Delimiter: "/"})
			Expect(ids).To(Equal([]string{"d/a@null", "d/a@a2", "d/e/", "d/f@null", "d/f@f1"}), "max-keys %d", maxKeys)
		}
	})

	Describe("across a reshard", func() {
		// reshardTo commits a reshard of rec to n shards in the next index
		// generation, as commit_reshard leaves it in a zone that logs no data:
		// every entry moves to the shard its object name hashes to there, the
		// instance names the new generation, and the old shards go. It runs
		// while no read is in flight.
		reshardTo := func(n uint32) {
			next := testBucket(bucketID, n)
			next.Info.Layout.Current.Gen = rec.Info.Layout.Current.Gen + 1
			seedShards(c, next)
			old := rec.Info.Layout.Current
			for i := range max(old.Layout.Normal.NumShards, 1) {
				oid := rec.Info.IndexShardOID(old, i)
				for k, v := range c.Object(rookIndexPool, "", oid).Omap {
					sh, _ := meta.IndexShard(indexKeyObjectName(k), n)
					c.Object(rookIndexPool, "", next.Info.IndexShardOID(next.Info.Layout.Current, sh)).Omap[k] = v
				}
				c.Remove(rookIndexPool, "", oid)
			}
			seedRecordInstance(c, next)
			rec = next
		}
		Context("over six names of two versions each", func() {
			var want []string
			BeforeEach(func(ctx SpecContext) {
				want = []string{"a@a2", "a@null", "b@b2", "b@null", "c@c2", "c@null", "d@d2", "d@null", "e@e2", "e@null", "f@f2", "f@null"}
				useBucket(2)
				for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
					version(ctx, n, "")
					version(ctx, n, n+"2")
				}
			})
			It("resumes an ordered versions listing on the new shards from the old page's marker", func(ctx SpecContext) {
				for _, maxKeys := range []int{1, 3, 5} {
					p := op.ListObjectsParams{MaxKeys: maxKeys, ListVersions: true}
					res := list(ctx, s, p)
					Expect(res.Truncated).To(BeTrue())
					ids := versionIDs(res.Entries)
					reshardTo(rec.Info.Layout.Current.Layout.Normal.NumShards + 1)
					p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
					rest, _ := pageAll(ctx, s, p)
					Expect(append(ids, rest...)).To(Equal(want), "max-keys %d", maxKeys)
				}
			})
			// radosgw lists through the bucket info its request read and fails
			// with the removed shard's ENOENT
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9906-L9915 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10631-L10639 at v20.2.4).
			It("lists the shards the bucket now names when its record predates a reshard", func(ctx SpecContext) {
				stale := rec
				reshardTo(3)
				res, err := s.ListObjects(ctx, stale, op.ListObjectsParams{MaxKeys: 20, ListVersions: true})
				Expect(err).NotTo(HaveOccurred(), "docs/exclusions.md, \"An ordered listing reads the shards the bucket names after a reshard removes its own\"")
				Expect(versionIDs(res.Entries)).To(Equal(want))
			})
			// The unordered listing's marker names a shard of the layout it was
			// read under, so it fails, as radosgw's does, rather than resume on
			// shards that hold other names.
			It("fails an unordered listing whose shard a reshard removed", func(ctx SpecContext) {
				stale := rec
				reshardTo(3)
				_, err := s.ListObjects(ctx, stale, op.ListObjectsParams{MaxKeys: 20, ListVersions: true, AllowUnordered: true})
				Expect(err).To(MatchError(op.ErrNoSuchKey), "radosgw's ENOENT, rgw_rados.cc:10267-10272 at v19.2.7")
			})
		})

		It("lists the rest from the new shards when a reshard commits between two rounds", func(ctx SpecContext) {
			for i := range 10 {
				e := entry("x", 1)
				e.Key.Name = meta.ObjKey{Name: fmt.Sprintf("up%02d.2~x.meta", i), NS: meta.NSMultipart}.IndexKeyName()
				seedIndexEntry(c, rookIndexPool, shardOID("x"), e)
			}
			for _, n := range []string{"z1", "z2", "z3"} {
				version(ctx, n, n)
			}
			small := open(ctx, map[string]string{"rgw_list_bucket_min_readahead": "1"})
			stale, oid := rec, shardOID("z1")
			reads := 0
			c.BeforeRead(rookIndexPool, "", oid, func() {
				if reads++; reads == 2 {
					c.BeforeRead(rookIndexPool, "", oid, nil)
					reshardTo(2)
				}
			})
			res, err := small.ListObjects(ctx, stale, op.ListObjectsParams{MaxKeys: 4})
			Expect(err).NotTo(HaveOccurred())
			Expect(versionIDs(res.Entries)).To(Equal([]string{"z1@z1", "z2@z2", "z3@z3"}),
				"the first round met the multipart names and moved its marker past them, rgw_rados.cc:1972-1993 at v19.2.7")
			Expect(res.Truncated).To(BeFalse())
		})
	})

	Describe("the head check of a version's list entry", func() {
		// pendingListEntry gives the stored list entry of name's version at
		// epoch a pending op, as a link that copied a pending instance entry
		// leaves it.
		pendingListEntry := func(name, instance string, epoch uint64) string {
			GinkgoHelper()
			oid := shardOID(name)
			k := rgwcls.ListIndexKey(rgwcls.ObjKey{Name: name, Instance: instance}, epoch)
			d := denc.NewDecoder(c.Object(rookIndexPool, "", oid).Omap[k])
			en := rgwcls.DecodeDirEntry(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			en.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: indexMtime, Op: uint8(rgwcls.OpAdd)}}}
			c.Object(rookIndexPool, "", oid).Omap[k] = encode(en)
			return oid
		}
		BeforeEach(func() {
			stater.StatObjectReturns(&op.ObjectState{Exists: true, Size: 9, Mtime: indexMtime, Epoch: 3}, nil)
		})
		// rgw_dir_suggest_changes applies a change under encode_obj_index_key
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2249-L2250 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2419-L2420 at v20.2.4):
		// the null version's is the plain key, which on a versioned name is
		// the version marker.
		It("suggests nothing for the null version's, whose change would land on the version marker", func(ctx SpecContext) {
			version(ctx, "k", "v1")
			version(ctx, "k", "")
			oid := pendingListEntry("k", "", 3)
			writes := c.Writes(rookIndexPool, "", oid)
			res := list(ctx, s, op.ListObjectsParams{MaxKeys: 10, ListVersions: true})
			Expect(versionIDs(res.Entries)).To(Equal([]string{"k@null", "k@v1"}))
			Expect(res.Entries[0].Size).To(BeEquivalentTo(9), "the page shows the head's state, rgw_rados.cc:10581-10583 at v19.2.7")
			Expect(stater.StatObjectCallCount()).To(Equal(1))
			_, _, key := stater.StatObjectArgsForCall(0)
			Expect(key).To(Equal(meta.ObjKey{Name: "k"}), "check_disk_state reads the null version's head, its key without an instance")
			Consistently(func() int { return c.Writes(rookIndexPool, "", oid) }).
				WithTimeout(50*time.Millisecond).WithPolling(5*time.Millisecond).Should(Equal(writes),
				"docs/exclusions.md, \"A listing sends no repair for the null version's list entry\"")
			Expect(c.Object(rookIndexPool, "", oid).Omap).To(HaveKey("k"), "the version marker stays")
		})
		// The null version's head is the name's plain object, the OLH head,
		// which check_disk_state reads without following
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10552 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11280 at v20.2.4);
		// read through the OLH it would be the current delete marker, which has
		// no head, and the null version would drop out of the listing.
		It("keeps the null version listed under a current delete marker with the store's own head read", func(ctx SpecContext) {
			version(ctx, "k", "")
			marker(ctx, "k", "dm3")
			pendingListEntry("k", "", 2)
			c.Put("ceph-objectstore.rgw.buckets.data", "", bucketID+"_k", nil)
			maps.Copy(c.Object("ceph-objectstore.rgw.buckets.data", "", bucketID+"_k").Xattrs, map[string][]byte{
				meta.AttrIDTag:    []byte("headtagheadtagheadtagheadtaghead"),
				meta.AttrOLHIDTag: []byte(olhTag),
				meta.AttrOLHVer:   []byte("3"),
				meta.AttrOLHInfo: encode(meta.OLHInfo{
					Target: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k", Instance: "dm3"}}, Removed: true,
				}),
			})
			real, err := driver.Open(ctx, c, conf(nil), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			res := list(ctx, real, op.ListObjectsParams{MaxKeys: 10, ListVersions: true})
			Expect(versionIDs(res.Entries)).To(Equal([]string{"k@dm3", "k@null"}),
				"the listing's head check must read the OLH head itself, not the version it names")
		})
		It("suggests a named version's repair under its key, which the class applies to its instance entry", func(ctx SpecContext) {
			version(ctx, "k", "v1")
			oid := pendingListEntry("k", "v1", 2)
			list(ctx, s, op.ListObjectsParams{MaxKeys: 10, ListVersions: true})
			Eventually(func() []rgwcls.Suggestion { return c.Suggestions(rookIndexPool, "", oid) }).
				WithTimeout(time.Second).WithPolling(time.Millisecond).Should(ContainElement(And(
				HaveField("Op", rgwcls.SuggestUpdate),
				HaveField("Entry.Key", rgwcls.ObjKey{Name: "k", Instance: "v1"}),
				HaveField("Entry.Meta.Size", BeEquivalentTo(9)),
			)), "rgw_rados.cc:10519-10671 at v19.2.7 suggests under the listed entry's own key")
		})
	})
})

// indexKeyObjectName is the object name an index shard key belongs to, the
// name every key of it is hashed by: a list key's name before its NUL, an
// instance key's between its prefix and NUL, an OLH key's after its prefix.
func indexKeyObjectName(k string) string {
	if rest, ok := strings.CutPrefix(k, "\x801000_"); ok {
		name, _, _ := strings.Cut(rest, "\x00")
		return name
	}
	if rest, ok := strings.CutPrefix(k, "\x801001_"); ok {
		return rest
	}
	name, _, _ := strings.Cut(k, "\x00")
	if key, ok := meta.ParseIndexKeyName(name); ok {
		return key.Name
	}
	return name
}

// lastListOp decodes the bucket_list request of the last read op run on the
// index shard oid.
func lastListOp(c *fakerados.Cluster, oid string) rgwcls.ListOp {
	GinkgoHelper()
	steps := c.LastRead(rookIndexPool, "", oid).Steps()
	Expect(steps).To(HaveLen(1))
	x, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue())
	Expect(x.Method).To(Equal("bucket_list"))
	d := denc.NewDecoder(x.In)
	l := rgwcls.DecodeListOp(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return l
}
