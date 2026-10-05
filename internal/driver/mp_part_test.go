package driver_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The part fixture is the PUT fixture with an upload of "k" created through
// CreateUpload, whose id is "2~" and putPrefix.
const reprefix = "RANDOMRANDOMRANDOMRANDOMRANDOM12"

// partHeadOID is the RADOS name of part n's head under prefix.
func partHeadOID(prefix string, n int) string {
	return putBucketID + "__multipart_" + prefix + "." + strconv.Itoa(n)
}

// partShadowOID is the RADOS name of stripe s of part n under prefix.
func partShadowOID(prefix string, n, s int) string {
	return putBucketID + "__shadow_" + prefix + "." + strconv.Itoa(n) + "_" + strconv.Itoa(s)
}

// partIndexKey is the index key of part n's head under prefix.
func partIndexKey(prefix string, n int) string { return "_multipart_" + prefix + "." + strconv.Itoa(n) }

// storedPart decodes the part info the meta object holds under key.
func storedPart(c *fakerados.Cluster, oid, key string) meta.UploadPartInfo {
	GinkgoHelper()
	obj := c.Object(extraPoolName, "", oid)
	Expect(obj).NotTo(BeNil())
	b, ok := obj.Omap[key]
	Expect(ok).To(BeTrue(), "the meta object holds %s", key)
	d := denc.NewDecoder(b)
	info := meta.DecodeUploadPartInfo(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return info
}

// indexOps names the bucket index ops written to shard, in order: "prepare"
// or "complete" and the modify op.
func indexOps(c *fakerados.Cluster, shard string) []string {
	GinkgoHelper()
	var out []string
	for _, w := range c.WritesTo(rookIndexPool, "", shard) {
		exec, ok := w.Steps()[2].(*radosclient.ExecStep)
		Expect(ok).To(BeTrue())
		switch exec.Method {
		case "bucket_prepare_op":
			out = append(out, "prepare "+modName(rgwcls.DecodePrepareOp(denc.NewDecoder(exec.In)).Op))
		case "bucket_complete_op":
			out = append(out, "complete "+modName(rgwcls.DecodeCompleteOp(denc.NewDecoder(exec.In)).Op))
		default:
			out = append(out, exec.Method)
		}
	}
	return out
}

// indexLogOps is indexOps with each op's log_op flag.
func indexLogOps(c *fakerados.Cluster, shard string) []string {
	GinkgoHelper()
	var out []string
	for _, w := range c.WritesTo(rookIndexPool, "", shard) {
		exec, ok := w.Steps()[2].(*radosclient.ExecStep)
		Expect(ok).To(BeTrue())
		switch exec.Method {
		case "bucket_prepare_op":
			p := rgwcls.DecodePrepareOp(denc.NewDecoder(exec.In))
			out = append(out, "prepare "+modName(p.Op)+" "+strconv.FormatBool(p.LogOp))
		case "bucket_complete_op":
			p := rgwcls.DecodeCompleteOp(denc.NewDecoder(exec.In))
			out = append(out, "complete "+modName(p.Op)+" "+strconv.FormatBool(p.LogOp))
		}
	}
	return out
}

func modName(m rgwcls.ModifyOp) string {
	switch m {
	case rgwcls.OpAdd:
		return "add"
	case rgwcls.OpDel:
		return "del"
	case rgwcls.OpCancel:
		return "cancel"
	}
	return "op " + strconv.Itoa(int(m))
}

func md5sumOf(s string) []byte {
	sum := md5.Sum([]byte(s))
	return sum[:]
}

// failingReader yields n bytes of 'f', then err.
type failingReader struct {
	n   int
	err error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n == 0 {
		return 0, f.err
	}
	k := min(len(p), f.n)
	for i := range p[:k] {
		p[i] = 'f'
	}
	f.n -= k
	return k, nil
}

var _ = Describe("PutPart", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		mtime   time.Time
		up      *op.Upload
		prefix  string
		metaObj string
		shard   string
		before  op.Stats
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		var err error
		up, err = s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		prefix = "k." + up.ID
		metaObj = metaOID(rec, "k", up.ID)
		shard = indexShardOID(rec, "k")
		// An enabled bucket quota without limits has CheckQuota load the
		// bucket's stats into the cache, whose changes the specs read.
		rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}
		Expect(s.CheckQuota(ctx, rec, rec.Info.Owner, 0, 0)).To(Succeed())
		before = driver.CachedBucketStatsForTest(s, rec)
	})
	partAttrs := func() map[string][]byte {
		return map[string][]byte{
			meta.AttrACL:              encode(acl.DefaultPolicy(initiator, "Alice")),
			meta.AttrContentType:      []byte("application/octet-stream\x00"),
			meta.AttrMetaPrefix + "p": []byte("1\x00"),
		}
	}
	// quotaSince is how far the cached bucket stats moved since BeforeEach:
	// objects and bytes.
	quotaSince := func() [2]int64 {
		now := driver.CachedBucketStatsForTest(s, rec)
		return [2]int64{int64(now.NumObjects) - int64(before.NumObjects), int64(now.Size) - int64(before.Size)} //nolint:gosec // spec sizes are small
	}

	It("writes a 9 MiB part as radosgw does and registers it on the meta object", func(ctx SpecContext) {
		body := mibPattern(9)
		c.ResetCounters()
		res, err := s.PutPart(ctx, up, 2, bytes.NewReader(body), op.PutParams{Attrs: partAttrs(), Size: int64(len(body)), Mtime: mtime})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(res.ETag).To(Equal(md5hex(body)))
		Expect(res.Size).To(BeEquivalentTo(9 << 20))
		Expect(res.Mtime).To(Equal(mtime))
		Expect(c.Reads(extraPoolName, "", metaObj)).To(Equal(1), "get_info, before the body")

		head := partHeadOID(prefix, 2)
		Expect(c.Writes(testDataPool, "", head)).To(Equal(2), "write_exclusive, then write_meta")
		Expect(c.WritesTo(testDataPool, "", head)[0].Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))
		w := c.LastWrite(testDataPool, "", head)
		hs := w.Steps()
		Expect(hs).To(HaveLen(6), "no create, no data, no idtag, no manifest, no storage_class")
		Expect(names(hs)).To(Equal([]string{meta.AttrACL, meta.AttrContentType, meta.AttrETag, meta.AttrMetaPrefix + "p", meta.AttrSourceZone}))
		Expect(execIn(w, 4, "obj_store_pg_ver", rgwcls.DecodeStorePGVerOp).Attr).To(Equal(meta.AttrPGVer))
		mt, ok := w.Mtime()
		Expect([]any{mt, ok}).To(Equal([]any{mtime, true}))
		obj := c.Object(testDataPool, "", head)
		Expect(obj.Data).To(Equal(body[:4<<20]))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrIDTag))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrManifest))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrStorageClass))
		Expect(obj.Xattrs).To(HaveKeyWithValue(meta.AttrETag, []byte(md5hex(body))), "no NUL, as rgw_op.cc:4522-4523 appends none")
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 2, 1)).Data).To(Equal(body[4<<20 : 8<<20]))
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 2, 2)).Data).To(Equal(body[8<<20:]))

		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "the part's prepare and complete on the UPLOAD key's shard")
		prep := execIn(c.WritesTo(rookIndexPool, "", shard)[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(prep.Key).To(Equal(rgwcls.ObjKey{Name: partIndexKey(prefix, 2)}))
		Expect(prep.Tag).To(Equal("_"+putPrefix), "a random tag: the part head is never set atomic")
		en, ok := c.Entry(rookIndexPool, "", shard, partIndexKey(prefix, 2))
		Expect(ok).To(BeTrue())
		Expect(en.Meta.Category).To(Equal(rgwcls.CategoryMain))
		Expect(en.Meta.Size).To(BeEquivalentTo(9 << 20))
		Expect(en.Meta.AccountedSize).To(BeEquivalentTo(9 << 20))
		Expect(en.Meta.ETag).To(Equal(md5hex(body)))
		Expect(en.Meta.StorageClass).To(BeEmpty())
		Expect(en.Meta.Owner).To(Equal("alice"))
		Expect(en.Meta.ContentType).To(Equal("application/octet-stream"))
		Expect(en.Meta.Mtime).To(Equal(mtime))
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: obj.Version}))

		reg := c.LastWrite(extraPoolName, "", metaObj)
		steps := reg.Steps()
		Expect(steps).To(HaveLen(3))
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		upd := execIn(reg, 1, "mp_upload_part_info_update", rgwcls.DecodeMPUploadPartInfoUpdateOp)
		Expect(upd.PartKey).To(Equal("part.00000002"))
		Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.ExecStep{}))
		Expect(steps[2]).To(HaveField("Class", "version"))
		Expect(steps[2]).To(HaveField("Method", "inc"))
		stored := storedPart(c, metaObj, "part.00000002")
		Expect(stored.Num).To(BeEquivalentTo(2))
		Expect(stored.ETag).To(Equal(md5hex(body)))
		Expect(stored.Size).To(BeEquivalentTo(9 << 20))
		Expect(stored.AccountedSize).To(BeEquivalentTo(9 << 20))
		Expect(stored.Modified).To(Equal(mtime))
		Expect(stored.Manifest.Prefix).To(Equal(prefix))
		Expect(stored.Manifest.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartPartNum: 2, StripeMaxSize: 4 << 20}}))
		Expect(stored.Manifest.ObjSize).To(BeEquivalentTo(9 << 20))
		Expect(stored.Manifest.HeadSize).To(BeZero())
		Expect(stored.Manifest.Obj.Key).To(Equal(key), "the manifest's head is the UPLOAD key")
		Expect(stored.Manifest.HeadPlacementRule).To(Equal(rec.Info.PlacementRule))
		Expect(stored.Compression.Type).To(Equal("none"))
		Expect(stored.PastPrefixes).To(BeEmpty())
		v := version.DecodeObjVersion(denc.NewDecoder(c.Object(extraPoolName, "", metaObj).Xattrs[version.XattrName]))
		Expect(v.Ver).To(BeEquivalentTo(2), "cls_version_inc on a meta object that had none: init_version's 1, then inc")
		Expect(quotaSince()).To(Equal([2]int64{1, 9 << 20}), "update_stats(owner, bucket, 1, accounted_size, 0)")
	})

	It("logs neither the prepare nor the complete of a part in a zone that logs data, but logs delete_obj's removal of a failed one", func(ctx SpecContext) {
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			zg := p.PeriodMap.ZoneGroups["zg-ceph-objectstore"]
			z := zg.Zones[putZoneID]
			z.LogData = true
			zg.Zones[putZoneID] = z
			p.PeriodMap.ZoneGroups["zg-ceph-objectstore"] = zg
		})
		s2 := openPutStore(ctx, c, denc.Squid, nil, mtime)
		Expect(s2.Zone().LogData).To(BeTrue())
		c.ResetCounters()
		_, err := s2.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Attrs: partAttrs(), Size: 1})
		Expect(err).NotTo(HaveOccurred())
		settle(s2)
		Expect(indexLogOps(c, shard)).To(Equal([]string{"prepare add false", "complete add false"}),
			"complete_flags = multipart ? 0 : FLAG_LOG_OP (rgw_op.cc:4555 at v19.2.6, :4830 at v20.2.4)")
		c.ResetCounters()
		_, err = s2.PutPart(ctx, up, 2, strings.NewReader("y"), op.PutParams{Attrs: partAttrs(), Size: 1, ContentMD5: md5sumOf("other")})
		Expect(err).To(MatchError(op.ErrBadDigest))
		settle(s2)
		Expect(indexLogOps(c, shard)).To(Equal([]string{"prepare del true", "complete del true"}), "delete_obj's log_op defaults to true")
	})

	It("ignores a part's write conditions on both releases", func(ctx SpecContext) {
		for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
			s2 := openPutStore(ctx, c, rel, nil, mtime)
			for i, p := range []op.PutParams{{IfNoneMatch: "*"}, {IfMatch: md5hex("other")}, {IfMatch: "*"}} {
				n := 10*int(rel) + i + 1
				p.Attrs, p.Size = partAttrs(), 1
				_, err := s2.PutPart(ctx, up, n, strings.NewReader("c"), p)
				Expect(err).NotTo(HaveOccurred(), "release %v, %+v: v20.2.4 checks them against the part head the request created", rel, p)
				for _, st := range c.LastWrite(testDataPool, "", partHeadOID(prefix, n)).Steps() {
					Expect(st).NotTo(BeAssignableToTypeOf(&radosclient.CmpXattrStep{}))
				}
				Expect(storedPart(c, metaObj, meta.MultipartPartKey(uint32(n))).Size).To(BeEquivalentTo(1)) //nolint:gosec // a small part number
			}
		}
	})

	It("re-prefixes a re-uploaded part number and the class records the old prefix", func(ctx SpecContext) {
		first := bytes.Repeat([]byte("a"), 5<<20)
		_, err := s.PutPart(ctx, up, 1, bytes.NewReader(first), op.PutParams{Attrs: partAttrs(), Size: int64(len(first))})
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(fixedRand(reprefix + "XYZ"))
		second := mibPattern(5)
		res, err := s.PutPart(ctx, up, 1, bytes.NewReader(second), op.PutParams{Attrs: partAttrs(), Size: int64(len(second))})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(res.ETag).To(Equal(md5hex(second)), "the digest covers the re-sent first stripe exactly once")
		newHead := c.Object(testDataPool, "", partHeadOID("k."+reprefix, 1))
		Expect(newHead).NotTo(BeNil())
		Expect(newHead.Data).To(Equal(second[:4<<20]))
		Expect(c.Object(testDataPool, "", partShadowOID("k."+reprefix, 1, 1)).Data).To(Equal(second[4<<20:]))
		Expect(c.Object(testDataPool, "", partHeadOID(prefix, 1)).Data).To(Equal(first[:4<<20]), "the first part's head stands until Complete or Abort removes it")
		Expect(c.Writes(testDataPool, "", partHeadOID(prefix, 1))).To(Equal(3), "the second upload's exclusive create met EEXIST and changed nothing")
		stored := storedPart(c, metaObj, "part.00000001")
		Expect(stored.Manifest.Prefix).To(Equal("k." + reprefix))
		Expect(stored.PastPrefixes).To(Equal([]string{prefix}), "cls_rgw.cc:4375-4382")
		Expect(stored.ETag).To(Equal(md5hex(second)))
		_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey("k."+reprefix, 1))
		Expect(ok).To(BeTrue(), "the re-uploaded part has its own index entry")
		_, ok = c.Entry(rookIndexPool, "", shard, partIndexKey(prefix, 1))
		Expect(ok).To(BeTrue(), "and the old one stays until Complete or Abort removes it")
	})

	It("fails with BucketAlreadyExists, radosgw's EEXIST, and leaves nothing when the second prefix's head exists too", func(ctx SpecContext) {
		c.Put(testDataPool, "", partHeadOID(prefix, 1), []byte("old"))
		c.Put(testDataPool, "", partHeadOID("k."+reprefix, 1), []byte("older"))
		s.SetRandForTest(fixedRand(reprefix))
		_, err := s.PutPart(ctx, up, 1, bytes.NewReader([]byte("new")), op.PutParams{Attrs: partAttrs(), Size: 3})
		Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
		Expect(c.Object(testDataPool, "", partHeadOID(prefix, 1)).Data).To(Equal([]byte("old")))
		Expect(c.Object(testDataPool, "", partHeadOID("k."+reprefix, 1)).Data).To(Equal([]byte("older")))
		Expect(c.Object(extraPoolName, "", metaObj).Omap).NotTo(HaveKey("part.00000001"))
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "the upload's own prepare and complete")
	})

	It("answers NoSuchUpload and removes what it wrote, the head through the index, when the upload vanished during the body", func(ctx SpecContext) {
		c.BeforeWrite(extraPoolName, "", metaObj, func(*fakerados.Object) { c.Remove(extraPoolName, "", metaObj) })
		_, err := s.PutPart(ctx, up, 3, bytes.NewReader(mibPattern(6)), op.PutParams{Attrs: partAttrs(), Size: 6 << 20})
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		settle(s)
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 3, 1))).To(BeNil(), "~RadosWriter removes the stripes")
		Expect(c.Object(testDataPool, "", partHeadOID(prefix, 3))).To(BeNil(), "and then the head")
		rm := c.LastWrite(testDataPool, "", partHeadOID(prefix, 3))
		Expect(execIn(rm, 0, "obj_remove", rgwcls.DecodeObjRemoveOp).KeepAttrPrefixes).To(Equal([]string{meta.AttrOLHPrefix}), "delete_obj's remove_rgw_head_obj")
		Expect(rm.Flags()).To(Equal(radosclient.OpFlagFullTry), "set_pool_full_try")
		Expect(indexOps(c, shard)).To(ConsistOf(
			"prepare add", "complete add", "prepare add", "complete add", "prepare del", "complete del",
		), "the upload's pair, the part's pair, and delete_obj's prepare and complete; the part's complete is not awaited")
		_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey(prefix, 3))
		Expect(ok).To(BeFalse(), "delete_obj completes the removal of the part's entry")
		Expect(quotaSince()).To(Equal([2]int64{0, (6 << 20) - (4 << 20)}),
			"write_meta added the part; delete_obj subtracts one object and the head's own size")
	})

	It("keeps the stripes when the registration times out", func(ctx SpecContext) {
		c.FailNextWrite(extraPoolName, "", metaObj, syscall.ETIMEDOUT)
		_, err := s.PutPart(ctx, up, 4, bytes.NewReader(mibPattern(5)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
		Expect(err).To(MatchError(op.ErrRequestTimedOut))
		settle(s)
		Expect(c.Object(testDataPool, "", partHeadOID(prefix, 4))).NotTo(BeNil(), "writer.clear_written on ETIMEDOUT")
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 4, 1))).NotTo(BeNil())
		_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey(prefix, 4))
		Expect(ok).To(BeTrue())
	})

	It("removes the part and registers nothing when the head write fails", func(ctx SpecContext) {
		head := partHeadOID(prefix, 1)
		c.BeforeWrite(testDataPool, "", head, func(o *fakerados.Object) {
			if o != nil {
				c.FailNextWrite(testDataPool, "", head, syscall.EIO)
				c.BeforeWrite(testDataPool, "", head, nil)
			}
		})
		_, err := s.PutPart(ctx, up, 1, bytes.NewReader(mibPattern(5)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
		Expect(err).To(HaveOccurred())
		settle(s)
		Expect(c.Object(testDataPool, "", head)).To(BeNil())
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 1, 1))).To(BeNil())
		Expect(c.Object(extraPoolName, "", metaObj).Omap).NotTo(HaveKey("part.00000001"))
		Expect(quotaSince()).To(Equal([2]int64{0, 0}), "the failed head write counted nothing, so its removal subtracts nothing")
	})

	It("keys a part of an upload without a v2 id by its number", func(ctx SpecContext) {
		seedMeta(ctx, c, rec, "k", "legacy", meta.PlacementRule{Name: "default-placement"}, attrsWithACL(initiator), nil)
		_, err := s.PutPart(ctx, &op.Upload{ID: "legacy", Bucket: rec, Key: key}, 3, strings.NewReader("x"), op.PutParams{Attrs: partAttrs(), Size: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(storedPart(c, metaOID(rec, "k", "legacy"), "part.3").Num).To(BeEquivalentTo(3), "rgw_putobj_processor.cc:534-543: part_num_str")
		Expect(c.Object(testDataPool, "", partHeadOID("k.legacy", 3))).NotTo(BeNil())
	})

	DescribeTable("refuses a bad body before registering anything, and removes its head through the index",
		func(ctx SpecContext, body func() io.Reader, size int64, md5sum []byte, kv map[string]string, want error) {
			if kv != nil {
				s = openPutStore(ctx, c, denc.Squid, kv, mtime)
				Expect(s.CheckQuota(ctx, rec, rec.Info.Owner, 0, 0)).To(Succeed())
				before = driver.CachedBucketStatsForTest(s, rec)
			}
			_, err := s.PutPart(ctx, up, 5, body(), op.PutParams{Attrs: partAttrs(), Size: size, ContentMD5: md5sum})
			Expect(err).To(MatchError(want))
			settle(s)
			Expect(c.Object(extraPoolName, "", metaObj).Omap).NotTo(HaveKey("part.00000005"))
			Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "~RadosWriter removes the head and the stripes")
			Expect(indexOps(c, shard)).To(Equal([]string{"prepare add", "complete add", "prepare del", "complete del"}),
				"the upload's own pair and delete_obj's: no part prepare went out")
			_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey(prefix, 5))
			Expect(ok).To(BeFalse())
			Expect(quotaSince()).To(Equal([2]int64{0, 0}),
				"radosgw subtracts the head it never counted from the quota cache; rgw-go does not (docs/ceph-upstream-bugs.md)")
		},
		Entry("a short body is RequestTimeout", func() io.Reader { return strings.NewReader("abc") }, int64(10), nil, nil, op.ErrRequestTimeout),
		Entry("a wrong Content-MD5 is BadDigest", func() io.Reader { return bytes.NewReader(bytes.Repeat([]byte("e"), 5<<20)) }, int64(5<<20), md5sumOf("other"), nil, op.ErrBadDigest),
		Entry("A's verification verdict passes through", func() io.Reader { return &failingReader{n: 4 << 20, err: op.ErrSignatureDoesNotMatch} }, int64(-1), nil, nil, op.ErrSignatureDoesNotMatch),
		Entry("past rgw_max_put_size is EntityTooLarge", func() io.Reader { return io.LimitReader(zeros{}, 16<<20) }, int64(-1), nil,
			map[string]string{"rgw_max_put_size": "8388608"}, op.ErrEntityTooLarge),
	)

	It("checks the quota on the bytes received before the digest, and removes the part", func(ctx SpecContext) {
		rec.Info.Quota = meta.Quota{MaxSize: int64(before.Size) + 4, MaxObjects: -1, CheckOnRaw: true, Enabled: true} //nolint:gosec // spec sizes are small
		_, err := s.PutPart(ctx, up, 1, strings.NewReader("12345"), op.PutParams{Attrs: partAttrs(), Size: -1, ContentMD5: md5sumOf("other")})
		Expect(err).To(MatchError(op.ErrQuotaExceeded), "rgw_op.cc:4444-4448 runs before :4483-4486")
		settle(s)
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	It("answers NoSuchUpload for an unknown upload before reading the body", func(ctx SpecContext) {
		var read atomic.Int64
		r := &countingReader{r: strings.NewReader("x"), n: &read}
		_, err := s.PutPart(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key}, 1, r, op.PutParams{Attrs: partAttrs(), Size: 1})
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		Expect(read.Load()).To(BeZero())
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	It("writes an empty part as an exclusive empty head", func(ctx SpecContext) {
		res, err := s.PutPart(ctx, up, 1, strings.NewReader(""), op.PutParams{Attrs: partAttrs(), Size: 0})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Size).To(BeZero())
		Expect(res.ETag).To(Equal(md5hex("")))
		obj := c.Object(testDataPool, "", partHeadOID(prefix, 1))
		Expect(obj).NotTo(BeNil())
		Expect(obj.Data).To(BeEmpty())
		Expect(storedPart(c, metaObj, "part.00000001").Size).To(BeZero())
	})

	It("places a COLD upload's parts in the class's pool, aligned to it, and removes a failed part's head as a raw object", func(ctx SpecContext) {
		c.SetRequiredAlignment(coldPool, 3<<20)
		cold, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "c"}, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		l, indexed, err := s.PartLayoutForTest(ctx, rec, meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"})
		Expect(err).NotTo(HaveOccurred())
		Expect([]uint64{l.Chunk, l.Stripe}).To(Equal([]uint64{3 << 20, 3 << 20}), "prepare_head aligns both to the tail pool (rgw_putobj_processor.cc:446-453)")
		Expect(indexed).To(BeFalse(), "the bucket's placement puts heads in another pool")
		body := mibPattern(5)
		_, err = s.PutPart(ctx, cold, 1, bytes.NewReader(body), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
		Expect(err).NotTo(HaveOccurred())
		cprefix := "c." + cold.ID
		head := c.Object(coldPool, "", partHeadOID(cprefix, 1))
		Expect(head).NotTo(BeNil(), "set_meta_placement_rule(&tail_placement_rule)")
		Expect(head.Data).To(Equal(body[:3<<20]))
		Expect(head.Xattrs).NotTo(HaveKey(meta.AttrStorageClass), "no manifest attr on a part head, so no storage_class attr")
		Expect(c.Object(coldPool, "", partShadowOID(cprefix, 1, 1)).Data).To(Equal(body[3<<20:]))
		stored := storedPart(c, metaOID(rec, "c", cold.ID), "part.00000001")
		Expect(stored.Manifest.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
		Expect(stored.Manifest.Rules[0].StripeMaxSize).To(BeEquivalentTo(3 << 20))

		settle(s)
		cshard := indexShardOID(rec, "c")
		n := c.Writes(rookIndexPool, "", cshard)
		_, err = s.PutPart(ctx, cold, 2, strings.NewReader("abc"), op.PutParams{Attrs: partAttrs(), Size: 10})
		Expect(err).To(MatchError(op.ErrRequestTimeout))
		settle(s)
		Expect(c.Object(coldPool, "", partHeadOID(cprefix, 2))).To(BeNil(), "delete_raw_obj: obj_to_raw by the bucket's rule names another pool")
		Expect(c.Writes(rookIndexPool, "", cshard)).To(Equal(n), "no index op")
	})

	It("encodes Tentacle's version 6 part info with no checksum", func(ctx SpecContext) {
		s2 := openPutStore(ctx, c, denc.Tentacle, nil, mtime)
		_, err := s2.PutPart(ctx, up, 1, strings.NewReader("t"), op.PutParams{Attrs: partAttrs(), Size: 1})
		Expect(err).NotTo(HaveOccurred())
		upd := execIn(c.LastWrite(extraPoolName, "", metaObj), 1, "mp_upload_part_info_update", rgwcls.DecodeMPUploadPartInfoUpdateOp)
		Expect(upd.Info[0]).To(Equal(byte(6)))
		Expect(upd.Info[len(upd.Info)-1]).To(Equal(byte(0)), "no checksum")
	})
})

var _ = Describe("CopyPart", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		up      *op.Upload
		prefix  string
		metaObj string
	)
	BeforeEach(func(ctx SpecContext) {
		now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		c.SetClock(func() time.Time { return now })
		s = openPutStore(ctx, c, denc.Squid, nil, now)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		var err error
		up, err = s.CreateUpload(ctx, rec, meta.ObjKey{Name: "k"}, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		prefix = "k." + up.ID
		metaObj = metaOID(rec, "k", up.ID)
	})
	source := func(ctx context.Context, data []byte) *op.ObjectState {
		GinkgoHelper()
		attrs := attrsWithACL(initiator)
		attrs[meta.AttrMetaPrefix+"src-only"] = []byte("1\x00")
		s.SetRandForTest(fixedRand(secondPrefix))
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "src"}, bytes.NewReader(data), op.PutParams{Attrs: attrs, Size: int64(len(data)), Tag: "tx-src"})
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(fixedRand(putPrefix))
		st, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "src"})
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	partAttrs := func() map[string][]byte {
		return map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice")), meta.AttrMetaPrefix + "p": []byte("1\x00")}
	}

	It("streams the source range through ReadObject into a part", func(ctx SpecContext) {
		src := mibPattern(10)
		st := source(ctx, src)
		upc := *up
		upc.Attrs = partAttrs()
		res, err := s.CopyPart(ctx, &upc, 1, st, op.ByteRange{Offset: 1 << 20, Length: 6 << 20})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Size).To(BeEquivalentTo(6 << 20))
		Expect(res.ETag).To(Equal(md5hex(src[1<<20 : 7<<20])))
		head := c.Object(testDataPool, "", partHeadOID(prefix, 1))
		Expect(head.Data).To(Equal(src[1<<20 : 5<<20]))
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 1, 1)).Data).To(Equal(src[5<<20 : 7<<20]))
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"p", []byte("1\x00")), "the request's attrs, not the source's")
		Expect(head.Xattrs).NotTo(HaveKey(meta.AttrMetaPrefix + "src-only"))
		stored := storedPart(c, metaObj, "part.00000001")
		Expect(stored.Size).To(BeEquivalentTo(6 << 20))
		Expect(stored.ETag).To(Equal(res.ETag))
	})

	It("copies the version it was given when the source is overwritten during the copy", func(ctx SpecContext) {
		old := mibPattern(10)
		st := source(ctx, old)
		var once sync.Once
		c.BeforeWrite(testDataPool, "", partHeadOID(prefix, 1), func(*fakerados.Object) {
			once.Do(func() {
				_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "src"}, bytes.NewReader(bytes.Repeat([]byte("N"), 10<<20)),
					op.PutParams{Attrs: attrsWithACL(initiator), Size: 10 << 20, Tag: "tx-new"})
				Expect(err).NotTo(HaveOccurred())
			})
		})
		res, err := s.CopyPart(ctx, up, 1, st, op.ByteRange{Length: 10 << 20})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object(testDataPool, "", putBucketID+"_src").Data[0]).To(Equal(byte('N')), "the source was overwritten mid-copy")
		Expect(res.ETag).To(Equal(md5hex(old)), "radosgw reads the source again per chunk and would copy the new version's later pieces")
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 1, 1)).Data).To(Equal(old[4<<20 : 8<<20]))
		Expect(c.Object(testDataPool, "", partShadowOID(prefix, 1, 2)).Data).To(Equal(old[8<<20:]))
	})

	It("removes the part when the source read fails midway", func(ctx SpecContext) {
		st := source(ctx, mibPattern(10))
		c.FailNextRead(testDataPool, "", tailOID(secondPrefix, 1), syscall.EIO)
		_, err := s.CopyPart(ctx, up, 1, st, op.ByteRange{Offset: 0, Length: 10 << 20})
		var oe *op.Error
		Expect(errors.As(err, &oe)).To(BeTrue(), "the read path's op error, as the body's error: %v", err)
		settle(s)
		Expect(c.Object(testDataPool, "", partHeadOID(prefix, 1))).To(BeNil())
		Expect(c.Object(extraPoolName, "", metaObj).Omap).NotTo(HaveKey("part.00000001"))
	})

	It("reads nothing of the source for an unknown upload", func(ctx SpecContext) {
		st := source(ctx, mibPattern(5))
		c.ResetCounters()
		_, err := s.CopyPart(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: meta.ObjKey{Name: "k"}}, 1, st, op.ByteRange{Length: 5 << 20})
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		Expect(c.Reads(testDataPool, "", tailOID(secondPrefix, 1))).To(BeZero(), "get_info runs before the source is read")
	})
})
