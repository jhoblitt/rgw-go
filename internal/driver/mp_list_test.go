package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
	"github.com/jhoblitt/rgw-go/internal/testutil/mptest"
)

// partNumbers is the numbers of the listed parts, in listing order.
func partNumbers(parts []op.Part) []int {
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.Number)
	}
	return out
}

// uploadKeysAndIDs is the key and id of each listed upload, in listing order.
func uploadKeysAndIDs(ups []op.Upload) [][2]string {
	out := make([][2]string, 0, len(ups))
	for _, u := range ups {
		out = append(out, [2]string{u.Key.Name, u.ID})
	}
	return out
}

// omapRead is the omap step of the last read of the meta object oid.
func omapRead(c *fakerados.Cluster, oid string) *radosclient.OmapGetValsStep {
	GinkgoHelper()
	steps := c.LastRead(extraPoolName, "", oid).Steps()
	Expect(steps).To(HaveLen(1))
	st, ok := steps[0].(*radosclient.OmapGetValsStep)
	Expect(ok).To(BeTrue(), "%T", steps[0])
	return st
}

// seedPartInfo stores part n's info under omap key k of the meta object oid.
func seedPartInfo(c *fakerados.Cluster, oid, k string, n uint32, etag string) {
	GinkgoHelper()
	obj := c.Object(extraPoolName, "", oid)
	Expect(obj).NotTo(BeNil(), oid)
	obj.Omap[k] = encode(meta.UploadPartInfo{
		Num: n, Size: uint64(n), AccountedSize: uint64(n), ETag: etag, Modified: indexMtime,
		Manifest: meta.NewManifest(), Compression: meta.NewCompressionInfo(),
	})
}

var _ = Describe("multipart listings", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		mtime time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
	})
	// create starts an upload of key whose id is "2~" and suffix.
	create := func(ctx context.Context, key, suffix string) *op.Upload {
		GinkgoHelper()
		s.SetRandForTest(fixedRand(suffix))
		up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: key}, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
		})
		Expect(err).NotTo(HaveOccurred())
		return up
	}
	putPart := func(ctx context.Context, up *op.Upload, n int, body []byte) {
		GinkgoHelper()
		_, err := s.PutPart(ctx, up, n, bytes.NewReader(body), op.PutParams{Size: int64(len(body)), Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
	}

	Describe("ListParts", func() {
		var (
			up  *op.Upload
			oid string
		)
		BeforeEach(func(ctx SpecContext) {
			up = create(ctx, "k", putPrefix)
			for _, n := range []int{1, 2, 3, 5} {
				putPart(ctx, up, n, mibPattern(5))
			}
			settle(s)
			oid = metaOID(rec, "k", up.ID)
			c.ResetCounters()
		})
		It("pages the omap after part.%08d of the marker and reports the last number", func(ctx SpecContext) {
			res, err := s.ListParts(ctx, up, 0, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Reads(extraPoolName, "", oid)).To(Equal(1), "the op's Init read the meta object; list_parts reads only the omap")
			st := omapRead(c, oid)
			Expect(st.StartAfter).To(Equal("part.00000000"))
			Expect(st.FilterPrefix).To(BeEmpty())
			Expect(st.Max).To(BeEquivalentTo(3), "num_parts + 1 detects truncation, rgw_sal_rados.cc:3358 at v19.2.6")
			Expect(partNumbers(res.Parts)).To(Equal([]int{1, 2}))
			Expect(res.NextMarker).To(Equal(2))
			Expect(res.Truncated).To(BeTrue())
			Expect(res.Parts[0].ETag).To(Equal(md5hex(mibPattern(5))))
			Expect(res.Parts[0].Size).To(BeEquivalentTo(5<<20), "accounted_size, RadosMultipartPart::get_size")
			Expect(res.Parts[0].Mtime).To(Equal(mtime), "the part info's modified")
		})
		It("falls back to every value on a gap and returns those above the marker", func(ctx SpecContext) {
			res, err := s.ListParts(ctx, up, 2, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(partNumbers(res.Parts)).To(Equal([]int{3, 5}), "the sorted walk meets 5 where it expects 4, rgw_sal_rados.cc:3386-3394")
			Expect(res.NextMarker).To(Equal(5))
			Expect(res.Truncated).To(BeFalse())
			Expect(c.Reads(extraPoolName, "", oid)).To(Equal(2), "one sorted attempt, one get_all")
			st := omapRead(c, oid)
			Expect([]any{st.StartAfter, st.Max}).To(Equal([]any{"", uint64(1024)}), "omap_get_all's first page, MAX_OMAP_GET_ENTRIES")
		})
		It("bounds the fallback by the page and marks it truncated", func(ctx SpecContext) {
			res, err := s.ListParts(ctx, up, 2, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(partNumbers(res.Parts)).To(Equal([]int{3}), "3 is expected; the gap sits past the page")
			Expect(res.Truncated).To(BeTrue())
			res, err = s.ListParts(ctx, up, 3, 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(partNumbers(res.Parts)).To(Equal([]int{5}))
			Expect(res.NextMarker).To(Equal(5))
			Expect(res.Truncated).To(BeFalse())
		})
		It("answers an empty page past the last part with next marker 0, as cur_max starts", func(ctx SpecContext) {
			res, err := s.ListParts(ctx, up, 5, 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Parts).To(BeEmpty())
			Expect(res.NextMarker).To(BeZero(), "NextPartNumberMarker is cur_max, rgw_rest_s3.cc:4135-4149 at v19.2.6")
			Expect(res.Truncated).To(BeFalse())
		})
		It("answers a zero page with nothing, truncated while parts remain", func(ctx SpecContext) {
			res, err := s.ListParts(ctx, up, 0, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Parts).To(BeEmpty())
			Expect(res.Truncated).To(BeTrue())
			Expect(omapRead(c, oid).Max).To(BeEquivalentTo(1))
		})
		It("decodes only the page's values on the sorted path", func(ctx SpecContext) {
			c.Object(extraPoolName, "", oid).Omap[meta.MultipartPartKey(2)] = []byte{0xff}
			res, err := s.ListParts(ctx, up, 0, 1)
			Expect(err).NotTo(HaveOccurred(), "the num_parts+1st value is read, never decoded")
			Expect(partNumbers(res.Parts)).To(Equal([]int{1}))
			_, err = s.ListParts(ctx, up, 0, 2)
			Expect(err).To(MatchError(op.ErrUnknown), "a part info that does not decode is -EIO, rgw_sal_rados.cc:3378-3384")
		})
		It("is NoSuchUpload for a vanished meta object", func(ctx SpecContext) {
			_, err := s.ListParts(ctx, &op.Upload{ID: "2~gone", Bucket: rec, Key: meta.ObjKey{Name: "k"}}, 0, 10)
			Expect(err).To(MatchError(op.ErrNoSuchUpload))
		})
		It("is NoSuchUpload for an empty upload id, as get_info on <key>..meta answers", func(ctx SpecContext) {
			_, err := s.ListParts(ctx, &op.Upload{ID: "", Bucket: rec, Key: meta.ObjKey{Name: "k"}}, 0, 10)
			Expect(err).To(MatchError(op.ErrNoSuchUpload))
		})

		Context("for an upload without a v2 id", func() {
			const legacy = "legacyid"
			var legacyOID string
			BeforeEach(func(ctx SpecContext) {
				seedMeta(ctx, c, rec, "old", legacy, meta.PlacementRule{Name: "default-placement"}, attrsWithACL(initiator), nil)
				legacyOID = metaOID(rec, "old", legacy)
				for _, n := range []uint32{1, 10, 2} {
					seedPartInfo(c, legacyOID, fmt.Sprintf("part.%d", n), n, fmt.Sprintf("e%d", n))
				}
				c.ResetCounters()
			})
			old := func(id string) *op.Upload { return &op.Upload{ID: id, Bucket: rec, Key: meta.ObjKey{Name: "old"}} }
			It("reads every value in one get_all and lists them by number", func(ctx SpecContext) {
				res, err := s.ListParts(ctx, old(legacy), 0, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(partNumbers(res.Parts)).To(Equal([]int{1, 2, 10}), "omap order is part.1, part.10, part.2")
				Expect(res.NextMarker).To(Equal(10))
				Expect(res.Truncated).To(BeFalse())
				Expect(c.Reads(extraPoolName, "", legacyOID)).To(Equal(1))
				st := omapRead(c, legacyOID)
				Expect([]any{st.StartAfter, st.Max}).To(Equal([]any{"", uint64(1024)}))
				res, err = s.ListParts(ctx, old(legacy), 1, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(partNumbers(res.Parts)).To(Equal([]int{2}))
				Expect([]any{res.NextMarker, res.Truncated}).To(Equal([]any{2, true}))
			})
			It("keeps the later key of two spellings of one number, as the parts map does", func(ctx SpecContext) {
				seedPartInfo(c, legacyOID, "part.01", 1, "e01")
				res, err := s.ListParts(ctx, old(legacy), 0, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(partNumbers(res.Parts)).To(Equal([]int{1, 2, 10}))
				Expect(res.Parts[0].ETag).To(Equal("e1"), `"part.1" follows "part.01" in omap order`)
			})
			It("pages get_all by 1024 while the OSD has more", func(ctx SpecContext) {
				for n := uint32(11); n <= 1100; n++ {
					seedPartInfo(c, legacyOID, fmt.Sprintf("part.%d", n), n, "e")
				}
				res, err := s.ListParts(ctx, old(legacy), 1095, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(partNumbers(res.Parts)).To(Equal([]int{1096, 1097, 1098, 1099, 1100}))
				Expect(c.Reads(extraPoolName, "", legacyOID)).To(Equal(2))
			})
			It("takes the sorted path for a 2/ id and falls back from it", func(ctx SpecContext) {
				seedMeta(ctx, c, rec, "old", "2/x", meta.PlacementRule{Name: "default-placement"}, attrsWithACL(initiator), nil)
				o := metaOID(rec, "old", "2/x")
				for _, n := range []uint32{1, 10, 2} {
					seedPartInfo(c, o, fmt.Sprintf("part.%d", n), n, "e")
				}
				c.ResetCounters()
				res, err := s.ListParts(ctx, old("2/x"), 0, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(partNumbers(res.Parts)).To(Equal([]int{1, 2, 10}))
				Expect(c.Reads(extraPoolName, "", o)).To(Equal(2), "is_v2_upload_id accepts 2/, rgw_multi.cc:76-82; 10 follows 1")
			})
		})
	})

	Describe("ListUploads", func() {
		const idA1, idA2, idB1, idC1 = "2~A1", "2~A2", "2~B1", "2~C1"
		BeforeEach(func(ctx SpecContext) {
			a1 := create(ctx, "a/x", "A1")
			create(ctx, "a/x", "A2")
			create(ctx, "a/y", "B1")
			create(ctx, "b", "C1")
			putPart(ctx, a1, 1, []byte("part"))
			settle(s)
		})
		It("lists meta objects only, in index order, with radosgw's markers", func(ctx SpecContext) {
			res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 3})
			Expect(err).NotTo(HaveOccurred())
			Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"a/x", idA1}, {"a/x", idA2}, {"a/y", idB1}}),
				"sorted as _multipart_<key>.<id>.meta, the part head a/x.2~A1.1 filtered out")
			Expect(res.Truncated).To(BeTrue())
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"a/y", idB1}))
			u := res.Uploads[0]
			Expect(u.Owner).To(Equal(alice))
			Expect(u.OwnerName).To(Equal("Alice"))
			Expect(u.Initiated).To(Equal(mtime))
			Expect(u.Bucket).To(BeIdenticalTo(rec))
			res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{KeyMarker: "a/y", UploadIDMarker: idB1, MaxUploads: 3})
			Expect(err).NotTo(HaveOccurred())
			Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"b", idC1}}))
			Expect(res.Truncated).To(BeFalse())
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"b", idC1}), "the last upload's, truncated or not")
		})
		It("passes the prefix and delimiter through the listing, which applies them to the meta names", func(ctx SpecContext) {
			res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{Prefix: "a/", Delimiter: "/", MaxUploads: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"a/x", idA1}, {"a/x", idA2}, {"a/y", idB1}}))
			Expect(res.CommonPrefixes).To(BeEmpty())
			res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: "/", MaxUploads: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.CommonPrefixes).To(Equal([]string{"a/"}))
			Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"b", idC1}}))
			res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", MaxUploads: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Uploads).To(BeEmpty(), "the delimiter is searched in the whole meta name, .<id>.meta included")
			Expect(res.CommonPrefixes).To(Equal([]string{"a/x.", "a/y.", "b."}))
		})
		It("uses <key>..meta for a key marker without an upload id marker", func(ctx SpecContext) {
			res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{KeyMarker: "a/x", MaxUploads: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(uploadKeysAndIDs(res.Uploads)).To(HaveLen(4), `"a/x..meta" sorts before "a/x.2~A1.meta"`)
			Expect(uploadKeysAndIDs(res.Uploads)[0]).To(Equal([2]string{"a/x", idA1}))
		})
		It("pages past a common prefix that ends a page with it as the key marker", func(ctx SpecContext) {
			res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", MaxUploads: 2})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Uploads).To(BeEmpty())
			Expect(res.CommonPrefixes).To(Equal([]string{"a/x.", "a/y."}))
			Expect(res.Truncated).To(BeTrue())
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"a/y.", ""}),
				"radosgw drops its common prefixes and so never ends a page on one (docs/exclusions.md)")
			res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", KeyMarker: res.NextKeyMarker, MaxUploads: 2})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.CommonPrefixes).To(Equal([]string{"b."}), "the marker a/y...meta falls inside a/y., which the listing skips")
			Expect(res.Truncated).To(BeFalse())
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"", ""}))
		})
		It("takes the next markers from the page's last item, an upload or a common prefix", func(ctx SpecContext) {
			create(ctx, "c/z", "D1")
			settle(s)
			res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: "/", MaxUploads: 2})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.CommonPrefixes).To(Equal([]string{"a/"}))
			Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"b", idC1}}))
			Expect(res.Truncated).To(BeTrue())
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"b", idC1}), "the page ends on the upload")
			res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: "/", MaxUploads: 1})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.CommonPrefixes).To(Equal([]string{"a/"}))
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"a/", ""}))
		})
		It("answers a zero page with nothing, truncated, and no next markers", func(ctx SpecContext) {
			res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 0})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Uploads).To(BeEmpty())
			Expect(res.Truncated).To(BeTrue())
			Expect([]string{res.NextKeyMarker, res.NextUploadIDMarker}).To(Equal([]string{"", ""}))
		})
	})

	Describe("ListUploads paging", func() {
		var paged *driver.Store
		BeforeEach(func(ctx SpecContext) {
			paged = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_list_bucket_min_readahead": "4"}, mtime)
		})
		// seedNames stores an index entry under each multipart name, a meta
		// name "<key>.<id>.meta" or a part head's "<key>.<id>.<n>", on the
		// shard of its key.
		seedNames := func(names ...string) {
			for _, n := range names {
				key := strings.TrimSuffix(n, ".meta")
				key = key[:strings.LastIndexByte(key, '.')]
				if !strings.HasSuffix(n, ".meta") {
					key = key[:strings.LastIndexByte(key, '.')]
				}
				e := entry("x", 1)
				e.Key.Name = meta.ObjKey{Name: n, NS: meta.NSMultipart}.IndexKeyName()
				seedIndexEntry(c, rookIndexPool, indexShardOID(rec, key), e)
			}
		}
		It("pages past a half-filled page of common prefixes followed by part heads", func(ctx SpecContext) {
			names := []string{"d1/x.2~1.meta", "d2/x.2~2.meta", "e.2~3.meta"}
			for n := 1; n <= 20; n++ {
				names = append(names, fmt.Sprintf("e.2~3.%02d", n))
			}
			seedNames(names...)
			l, err := mptest.Page(ctx, paged, rec, op.ListUploadsParams{Delimiter: "/", MaxUploads: 4}, 5)
			Expect(err).NotTo(HaveOccurred(), "a round that stops half full names its last counted prefix, not a refused part head")
			Expect(l.Prefixes).To(Equal([]string{"d1/", "d2/"}))
			Expect(l.Uploads).To(Equal([][2]string{{"e", "2~3"}}))
		})
		It("gives every upload and common prefix once at every page size", func(ctx SpecContext) {
			r := rand.New(rand.NewPCG(uint64(GinkgoRandomSeed()), 5)) //nolint:gosec // a spec's reproducible data
			pick := func(alphabet string, lo, hi int) string {
				b := make([]byte, lo+r.IntN(hi-lo+1))
				for i := range b {
					b[i] = alphabet[r.IntN(len(alphabet))]
				}
				return string(b)
			}
			seen := map[string]bool{}
			var names []string
			for range 12 {
				upload := pick("ab/.-x", 1, 4) + ".2~" + pick("AB1-_x", 1, 3)
				if seen[upload] {
					continue
				}
				seen[upload] = true
				names = append(names, upload+".meta")
				for n := range r.IntN(4) {
					names = append(names, fmt.Sprintf("%s.%d", upload, n+1))
				}
			}
			seedNames(names...)
			for _, delim := range []string{"", "/", ".", "-", "x"} {
				for _, prefix := range []string{"", "a"} {
					Expect(mptest.CheckPaging(ctx, paged, rec, op.ListUploadsParams{Prefix: prefix, Delimiter: delim})).To(Succeed(),
						"seed %d, names %q, prefix %q, delimiter %q", GinkgoRandomSeed(), names, prefix, delim)
				}
			}
		})
	})
})
