package driver_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // generate_fake_tag's digest
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("PurgeBypassGC", func() {
	const zgID = "zg-ceph-objectstore"
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		bodies map[string][]byte
	)
	createBucket := func(ctx context.Context, name string) *op.BucketRecord {
		GinkgoHelper()
		r, err := s.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: initiator, Zonegroup: zgID,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     attrsWithACL(initiator), Exclusive: true,
		})
		Expect(err).NotTo(HaveOccurred())
		return r
	}
	BeforeEach(func(ctx SpecContext) {
		DeferCleanup(driver.CaptureLog(GinkgoWriter))
		c = newPutCluster()
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_gc_max_concurrent_io": "2", "rgw_cache_enabled": "false"},
			time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
		DeferCleanup(s.Close)
		// Every tag, tail prefix and upload id distinct, so objects never
		// share a tail by accident of a fixed random source.
		var n atomic.Int64
		s.SetRandForTest(func(k int) string {
			return (fmt.Sprintf("R%dR", n.Add(1)) + strings.Repeat("x", k))[:k]
		})
		bodies = map[string][]byte{}
		rec = createBucket(ctx, "b")
	})
	AfterEach(func() { settle(s) })

	headOID := func(r *op.BucketRecord, name string) string { return r.Info.Bucket.Marker + "_" + name }
	putObject := func(ctx context.Context, r *op.BucketRecord, name string, size int) {
		GinkgoHelper()
		body := bytes.Repeat([]byte(name[:1]), size)
		_, err := s.PutObject(ctx, r, meta.ObjKey{Name: name}, bytes.NewReader(body), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(size)})
		Expect(err).NotTo(HaveOccurred())
		bodies[r.Info.Bucket.Name+"/"+name] = body
		settle(s)
	}
	// putRawHead stores a head without a manifest and its index entry, as a
	// head a gateway wrote whole once was.
	putRawHead := func(r *op.BucketRecord, name string, data []byte) {
		c.Put(testDataPool, "", headOID(r, name), data)
		seedIndexEntry(c, rookIndexPool, indexShardOID(r, name), entry(name, uint64(len(data))))
	}
	// tailOIDs lists name's stripes past its head, from its manifest.
	tailOIDs := func(ctx context.Context, r *op.BucketRecord, name string) []string {
		GinkgoHelper()
		st, err := s.StatObject(ctx, r, meta.ObjKey{Name: name})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Manifest).NotTo(BeNil(), name)
		stripes, err := st.Manifest.Stripes()
		Expect(err).NotTo(HaveOccurred())
		var oids []string
		for _, sp := range stripes {
			if oid := sp.OID(); oid != headOID(r, name) {
				oids = append(oids, oid)
			}
		}
		Expect(oids).NotTo(BeEmpty(), "%s has tails", name)
		return oids
	}
	// dataObjects lists the data pool's objects named under r's marker.
	dataObjects := func(r *op.BucketRecord) []string {
		oids := []string{}
		for oid := range c.Objects(testDataPool, "") {
			if strings.HasPrefix(oid, r.Info.Bucket.Marker+"_") {
				oids = append(oids, oid)
			}
		}
		return oids
	}
	gcOIDs := func() []string { return chainOIDs(allGCEntries(c)) }
	gcTags := func() []string {
		var tags []string
		for _, e := range allGCEntries(c) {
			tags = append(tags, e.Tag)
		}
		return tags
	}
	// indexKeys lists the plain entries of r's index shards that are stored
	// as existing, so a listing's own repair of a stale entry cannot stand in
	// for the purge's. A removed head's entry stays behind as not existing,
	// pending under the prepare's tag, as delete_obj_aio leaves it.
	indexKeys := func(r *op.BucketRecord) []string {
		GinkgoHelper()
		settle(s)
		keys := []string{}
		for oid, o := range c.Objects(rookIndexPool, "") {
			if base := ".dir." + r.Info.Bucket.ID; oid != base && !strings.HasPrefix(oid, base+".") {
				continue
			}
			for k := range o.Omap {
				if en, ok := c.Entry(rookIndexPool, "", oid, k); ok && !strings.HasPrefix(k, "\x80") && en.Exists {
					keys = append(keys, k)
				}
			}
		}
		return keys
	}
	listedKeys := func(ctx context.Context, r *op.BucketRecord) []string {
		GinkgoHelper()
		res, err := s.ListObjects(ctx, r, op.ListObjectsParams{MaxKeys: 1000})
		Expect(err).NotTo(HaveOccurred())
		keys := []string{}
		for _, e := range res.Entries {
			keys = append(keys, e.Key.Name)
		}
		return keys
	}
	readBack := func(ctx context.Context, r *op.BucketRecord, name string) []byte {
		GinkgoHelper()
		st, err := s.PrefetchObject(ctx, r, meta.ObjKey{Name: name})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue(), name)
		var out bytes.Buffer
		Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &out)).To(Succeed(), name)
		return out.Bytes()
	}
	copyObject := func(ctx context.Context, from *op.BucketRecord, src string, to *op.BucketRecord, dst string) {
		GinkgoHelper()
		st, err := s.PrefetchObject(ctx, from, meta.ObjKey{Name: src})
		Expect(err).NotTo(HaveOccurred())
		_, err = s.CopyObject(ctx, st, to, meta.ObjKey{Name: dst}, op.CopyParams{Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		bodies[to.Info.Bucket.Name+"/"+dst] = bodies[from.Info.Bucket.Name+"/"+src]
		settle(s)
	}
	initUpload := func(ctx context.Context, r *op.BucketRecord, name string) *op.Upload {
		GinkgoHelper()
		up, err := s.CreateUpload(ctx, r, meta.ObjKey{Name: name}, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
		})
		Expect(err).NotTo(HaveOccurred())
		return up
	}
	putPart := func(ctx context.Context, up *op.Upload, n, size int) string {
		GinkgoHelper()
		res, err := s.PutPart(ctx, up, n, bytes.NewReader(bytes.Repeat([]byte("p"), size)), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(size)})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		return res.ETag
	}
	// runAdmin runs o over the driver's env for an identity holding
	// buckets=*.
	runAdmin := func(ctx context.Context, o op.Op) error {
		admin := meta.NewUserInfo()
		admin.UserID = meta.UserID{ID: "admin"}
		return op.Run(ctx, o, &op.Request{Env: s.Env(), Identity: op.Identity{
			User: &admin, Owner: meta.UserOwner(admin.UserID), OpMask: op.OpTypeAll, Caps: meta.Caps{"buckets": meta.CapAll},
		}})
	}

	It("releases every tail directly, the first object's and a multipart object's included, and leaves no GC entry", func(ctx SpecContext) {
		putObject(ctx, rec, "big1", 9<<20)
		putObject(ctx, rec, "big2", 9<<20)
		putObject(ctx, rec, "small", 10)
		putRawHead(rec, "raw", []byte("x"))
		done := initUpload(ctx, rec, "done")
		etag := putPart(ctx, done, 1, 5<<20)
		_, err := s.Complete(ctx, done, []op.CompletePart{{Number: 1, ETag: etag}})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		up := initUpload(ctx, rec, "mp")
		putPart(ctx, up, 1, 5<<20)

		Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
		settle(s)
		Expect(gcTags()).To(ConsistOf(up.ID), "the aborted upload's parts go to GC under the upload id, as abort_multiparts sends them")
		Expect(dataObjects(rec)).To(ConsistOf(append([]string{headOID(rec, "raw")}, gcOIDs()...)),
			"an object without a manifest is the ordinary purge's; the aborted parts wait for GC")
		Expect(indexKeys(rec)).To(ConsistOf("raw"))
		Expect(listedKeys(ctx, rec)).To(ConsistOf("raw"), "the listing the ordinary purge makes next")
		_, err = s.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred(), "the bucket itself is the ordinary purge's")
	})

	It("keeps at most rgw_gc_max_concurrent_io deletes in flight", func(ctx SpecContext) {
		for i := range 6 {
			putObject(ctx, rec, fmt.Sprintf("o%d", i), 13<<20)
		}
		var inFlight, peak atomic.Int32
		hook := func(*fakerados.Object) {
			n := inFlight.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			// The window the deletes overlap in; nothing waits on it.
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
		}
		for i := range 6 {
			name := fmt.Sprintf("o%d", i)
			for _, oid := range append(tailOIDs(ctx, rec, name), headOID(rec, name)) {
				c.BeforeWrite(testDataPool, "", oid, hook)
			}
		}
		Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
		Expect(peak.Load()).To(BeNumerically("==", 2), "the bound is reached and never passed")
		Expect(dataObjects(rec)).To(BeEmpty())
	})

	It("counts a tail stripe already gone as deleted, as the garbage collector counts it", func(ctx SpecContext) {
		putObject(ctx, rec, "big", 9<<20)
		c.Remove(testDataPool, "", tailOIDs(ctx, rec, "big")[0])
		Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
		Expect(dataObjects(rec)).To(BeEmpty())
		Expect(indexKeys(rec)).To(BeEmpty())
	})

	It("stops at a failed delete, keeps the failed object's head, and returns the failure", func(ctx SpecContext) {
		putObject(ctx, rec, "big", 9<<20)
		tails := tailOIDs(ctx, rec, "big")
		c.FailNextWrite(testDataPool, "", tails[1], syscall.EPERM)
		Expect(s.PurgeBypassGC(ctx, rec)).To(MatchError(op.ErrAccessDenied))
		Expect(c.Object(testDataPool, "", headOID(rec, "big"))).NotTo(BeNil(), "a retry walks the object again")
		Expect(indexKeys(rec)).To(ConsistOf("big"))

		Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed(), "the retry counts the released stripe as gone")
		Expect(dataObjects(rec)).To(BeEmpty())
		Expect(indexKeys(rec)).To(BeEmpty())
		Expect(listedKeys(ctx, rec)).To(BeEmpty())
	})

	It("issues nothing more once its context ends, keeping the head of an object whose tails it did not finish", func(ctx SpecContext) {
		putObject(ctx, rec, "big", 13<<20)
		tails := tailOIDs(ctx, rec, "big")
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c.BeforeWrite(testDataPool, "", tails[0], func(*fakerados.Object) {
			c.BeforeWrite(testDataPool, "", tails[0], nil)
			cancel()
		})
		Expect(s.PurgeBypassGC(pctx, rec)).To(MatchError(context.Canceled))
		Expect(c.Object(testDataPool, "", headOID(rec, "big"))).NotTo(BeNil(), "the head waits for every tail")
		Expect(indexKeys(rec)).To(ConsistOf("big"))

		Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
		Expect(dataObjects(rec)).To(BeEmpty())
	})

	Describe("the head removal", func() {
		var head, shard string
		BeforeEach(func(ctx SpecContext) {
			putObject(ctx, rec, "big", 9<<20)
			head, shard = headOID(rec, "big"), indexShardOID(rec, "big")
		})
		// entry is big's index entry as stored.
		entry := func() rgwcls.DirEntry {
			GinkgoHelper()
			settle(s)
			en, ok := c.Entry(rookIndexPool, "", shard, "big")
			Expect(ok).To(BeTrue(), "big's index entry")
			return en
		}
		retried := func(ctx context.Context) {
			GinkgoHelper()
			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed(), "the retry")
			Expect(dataObjects(rec)).To(BeEmpty())
			Expect(indexKeys(rec)).To(BeEmpty())
			Expect(listedKeys(ctx, rec)).To(BeEmpty())
		}

		It("cancels its index change when it fails, so the object stays listed for a retry", func(ctx SpecContext) {
			c.FailNextWrite(testDataPool, "", head, syscall.EPERM)
			Expect(s.PurgeBypassGC(ctx, rec)).To(MatchError(op.ErrAccessDenied))
			Expect(c.Object(testDataPool, "", head)).NotTo(BeNil())
			en := entry()
			Expect(en.Exists).To(BeTrue(), "the entry is not completed as removed, as radosgw's delete_obj_aio completes it")
			Expect(en.PendingMap).To(BeEmpty(), "the prepare is canceled")
			Expect(listedKeys(ctx, rec)).To(ConsistOf("big"))
			retried(ctx)
		})
		It("counts a head already gone as removed", func(ctx SpecContext) {
			c.BeforeWrite(testDataPool, "", head, func(*fakerados.Object) {
				c.BeforeWrite(testDataPool, "", head, nil)
				c.Remove(testDataPool, "", head)
			})
			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed(), "where radosgw's drain answers its ENOENT")
			Expect(dataObjects(rec)).To(BeEmpty())
			Expect(indexKeys(rec)).To(BeEmpty())
			Expect(listedKeys(ctx, rec)).To(BeEmpty())
			retried(ctx)
		})
		It("leaves its index change pending when it times out, for the listing to check", func(ctx SpecContext) {
			c.FailNextWrite(testDataPool, "", head, syscall.ETIMEDOUT)
			Expect(s.PurgeBypassGC(ctx, rec)).To(MatchError(op.ErrRequestTimedOut))
			Expect(c.Object(testDataPool, "", head)).NotTo(BeNil())
			en := entry()
			Expect(en.Exists).To(BeTrue())
			Expect(en.PendingMap).To(HaveLen(1), "the prepare stays pending, neither completed nor canceled")
			retried(ctx)
		})
	})

	DescribeTable("refuses a bucket whose versioning was ever enabled before it deletes anything",
		func(ctx SpecContext, flags uint32, edit func(*op.RemoveBucketAdmin)) {
			putObject(ctx, rec, "big", 9<<20)
			putObject(ctx, rec, "small", 10)
			up := initUpload(ctx, rec, "mp")
			putPart(ctx, up, 1, 5<<20)
			cur, err := s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
			cur.Info.Flags |= flags
			Expect(s.PutBucketInfo(ctx, cur)).To(Succeed())
			before, entries := dataObjects(rec), indexKeys(rec)
			extra := c.Objects(extraPoolName, "")

			o := op.NewRemoveBucketAdmin()
			o.Bucket = "b"
			edit(o)
			Expect(runAdmin(ctx, o)).To(MatchError(op.ErrNotImplemented))
			settle(s)
			Expect(dataObjects(rec)).To(ConsistOf(before), "no head or tail released")
			Expect(c.Objects(extraPoolName, "")).To(HaveLen(len(extra)), "the upload not aborted")
			Expect(allGCEntries(c)).To(BeEmpty(), "nothing queued for GC")
			Expect(entries).To(ContainElements("big", "small"))
			Expect(indexKeys(rec)).To(ConsistOf(entries))
			_, err = s.GetBucket(ctx, "", "b")
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("purge-objects, versioning enabled", uint32(meta.BucketVersioned), func(o *op.RemoveBucketAdmin) { o.PurgeObjects = true }),
		Entry("bypass-gc, versioning enabled", uint32(meta.BucketVersioned), func(o *op.RemoveBucketAdmin) { o.BypassGC = true }),
		Entry("bypass-gc, versioning suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), func(o *op.RemoveBucketAdmin) { o.BypassGC = true }),
	)

	It("fails on a bucket without index objects, as read_stats fails", func(ctx SpecContext) {
		nx := testBucket("zone-ceph-objectstore.4156.99", 0)
		nx.Info.Layout.Current.Layout.Type = meta.IndexIndexless
		Expect(s.PurgeBypassGC(ctx, nx)).To(MatchError(op.ErrNoSuchBucket))
	})

	DescribeTable("releases the tails under the tag radosgw's refcount holds them by, allowed into a full pool's reserve, and removes the head unguarded",
		func(ctx SpecContext, edit func(head *fakerados.Object, firstTail string) string) {
			putObject(ctx, rec, "big", 9<<20)
			tails := tailOIDs(ctx, rec, "big")
			want := edit(c.Object(testDataPool, "", headOID(rec, "big")), tails[0])
			c.ResetCounters()
			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
			for _, oid := range tails {
				w := c.LastWrite(testDataPool, "", oid)
				Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), oid)
				Expect(w.Steps()).To(HaveLen(1), oid)
				Expect(execIn(w, 0, "put", refcount.DecodePutOp)).To(Equal(refcount.PutOp{Tag: want, ImplicitRef: true}), oid)
			}
			w := c.LastWrite(testDataPool, "", headOID(rec, "big"))
			Expect(w.Flags()).To(Equal(radosclient.OpFlagNone), "delete_obj_aio sets no flags")
			Expect(w.Steps()).To(HaveLen(1), "no guard")
			Expect(execIn(w, 0, "obj_remove", rgwcls.DecodeObjRemoveOp).KeepAttrPrefixes).To(BeEmpty())
			Expect(dataObjects(rec)).To(BeEmpty())

			settle(s)
			writes := c.WritesTo(rookIndexPool, "", indexShardOID(rec, "big"))
			Expect(writes).To(HaveLen(2), "the prepare and delete_obj_index's completion")
			prep := execIn(writes[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
			Expect(prep.Op).To(Equal(rgwcls.OpDel))
			Expect(prep.Tag).NotTo(BeEmpty(), "UpdateIndex::prepare's random tag")
			comp := execIn(writes[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
			Expect(comp.Op).To(Equal(rgwcls.OpDel))
			Expect(comp.Tag).To(BeEmpty(), "an UpdateIndex never prepared")
			Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}))
			en, ok := c.Entry(rookIndexPool, "", indexShardOID(rec, "big"), "big")
			Expect(ok).To(BeTrue())
			Expect(en.Exists).To(BeFalse())
			Expect(en.PendingMap).To(HaveLen(1), "the prepare stays pending, as radosgw leaves it, for the listing to clear")
		},
		Entry("the tail tag first", func(head *fakerados.Object, _ string) string {
			head.Xattrs[meta.AttrTailTag] = []byte("tail-tag\x00")
			return "tail-tag\x00"
		}),
		Entry("the idtag without a tail tag", func(head *fakerados.Object, _ string) string {
			delete(head.Xattrs, meta.AttrTailTag)
			head.Xattrs[meta.AttrIDTag] = []byte("id-tag\x00")
			return "id-tag\x00"
		}),
		Entry("the tag get_obj_state fakes without either", func(head *fakerados.Object, firstTail string) string {
			delete(head.Xattrs, meta.AttrTailTag)
			delete(head.Xattrs, meta.AttrIDTag)
			sum := md5.Sum(append(slices.Clone(head.Xattrs[meta.AttrManifest]), head.Xattrs[meta.AttrETag]...)) //nolint:gosec // generate_fake_tag's digest
			return firstTail + "_" + hex.EncodeToString(sum[:]) + "\x00"
		}),
	)

	Describe("tails another object shares", func() {
		var other *op.BucketRecord
		BeforeEach(func(ctx SpecContext) {
			other = createBucket(ctx, "other")
			putObject(ctx, rec, "big", 9<<20)
		})
		It("leaves another bucket's copy readable when the source's bucket goes first, and frees the stripes with the copy", func(ctx SpecContext) {
			shared := tailOIDs(ctx, rec, "big")
			copyObject(ctx, rec, "big", other, "copy")
			Expect(tailOIDs(ctx, other, "copy")).To(Equal(shared), "the copy shares the source's tails")

			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
			Expect(dataObjects(rec)).To(ConsistOf(shared), "the source's head is gone, the shared tails stay")
			Expect(readBack(ctx, other, "copy")).To(Equal(bodies["b/big"]))

			Expect(s.PurgeBypassGC(ctx, other)).To(Succeed())
			Expect(dataObjects(rec)).To(BeEmpty(), "the last holder's release removes the stripes")
			Expect(dataObjects(other)).To(BeEmpty())
			Expect(gcOIDs()).To(BeEmpty())
		})
		It("leaves the source readable when the copy's bucket goes first", func(ctx SpecContext) {
			shared := tailOIDs(ctx, rec, "big")
			copyObject(ctx, rec, "big", other, "copy")

			Expect(s.PurgeBypassGC(ctx, other)).To(Succeed())
			Expect(dataObjects(other)).To(BeEmpty())
			Expect(readBack(ctx, rec, "big")).To(Equal(bodies["b/big"]))

			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
			for _, oid := range shared {
				Expect(c.Object(testDataPool, "", oid)).To(BeNil(), oid)
			}
		})
		It("releases a tail two objects of the bucket share once for each", func(ctx SpecContext) {
			copyObject(ctx, rec, "big", rec, "twin")
			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
			Expect(dataObjects(rec)).To(BeEmpty())
			Expect(gcOIDs()).To(BeEmpty())
		})
		It("leaves a copy of a multipart object readable", func(ctx SpecContext) {
			up := initUpload(ctx, rec, "mp")
			etag := putPart(ctx, up, 1, 5<<20)
			_, err := s.Complete(ctx, up, []op.CompletePart{{Number: 1, ETag: etag}})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			bodies["b/mp"] = bytes.Repeat([]byte("p"), 5<<20)
			copyObject(ctx, rec, "mp", other, "copy")

			Expect(s.PurgeBypassGC(ctx, rec)).To(Succeed())
			Expect(readBack(ctx, other, "copy")).To(Equal(bodies["b/mp"]))
		})
	})

	It("removes a bucket through the admin op on both paths", func(ctx SpecContext) {
		purged := createBucket(ctx, "purged")
		putObject(ctx, purged, "big", 9<<20)
		purgedTails := tailOIDs(ctx, purged, "big")
		bypassed := createBucket(ctx, "bypassed")
		putObject(ctx, bypassed, "big", 9<<20)

		o := op.NewRemoveBucketAdmin()
		o.Bucket, o.PurgeObjects = "purged", true
		Expect(runAdmin(ctx, o)).To(Succeed())
		settle(s)
		Expect(gcOIDs()).To(ContainElements(purgedTails), "the ordinary purge queues the tails for GC")

		o = op.NewRemoveBucketAdmin()
		o.Bucket, o.BypassGC = "bypassed", true // no purge-objects: bypass-gc purges anyway
		Expect(runAdmin(ctx, o)).To(Succeed())
		settle(s)
		Expect(dataObjects(bypassed)).To(BeEmpty())
		Expect(gcOIDs()).NotTo(ContainElement(HavePrefix(bypassed.Info.Bucket.Marker + "_")))

		for _, name := range []string{"purged", "bypassed"} {
			_, err := s.GetBucket(ctx, "", name)
			Expect(err).To(MatchError(op.ErrNoSuchBucket), name)
		}
	})
})
