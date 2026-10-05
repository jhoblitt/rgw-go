package driver_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The PUT fixture: the zone seedRookZone seeds for "ceph-objectstore", whose
// default-placement keeps data inline and has a COLD class in coldPool, with
// its gc pool in the log pool's gc namespace and the short id 12345, and the
// bucket "plain" with eleven index shards.
const (
	putBucketID  = "zone-ceph-objectstore.4156.1"
	putZoneID    = "zone-ceph-objectstore"
	coldPool     = "cold.data"
	gcPoolName   = "ceph-objectstore.rgw.log"
	gcNS         = "gc"
	putPrefix    = "AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u"
	secondPrefix = "SECONDSECONDSECONDSECONDSECOND1"
)

// alice is the fixture bucket's owner.
var alice = meta.UserOwner(meta.UserID{ID: "alice"})

// seedGCShards creates gc.0 to gc.<n-1> in the gc pool at cls version 1, as
// RGWGC::initialize leaves a shard that has moved to the rgw_gc queue.
func seedGCShards(c *fakerados.Cluster, n int) {
	for i := range n {
		oid := fmt.Sprintf("gc.%d", i)
		c.Put(gcPoolName, gcNS, oid, nil)
		c.Object(gcPoolName, gcNS, oid).Xattrs[version.XattrName] = encode(version.ObjVersion{Ver: 1})
	}
}

// newPutCluster is the cluster of the PUT fixture, with the classes a PUT
// calls and the user alice, whose quotas a PUT's quota check loads.
func newPutCluster() *fakerados.Cluster {
	c := newIndexCluster()
	c.RegisterClass("rgw_gc", fakerados.GCQueueClass(), fakerados.GCQueueWriteMethods...)
	c.RegisterClass("refcount", fakerados.RefcountClass(), fakerados.RefcountWriteMethods...)
	editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
		z.GCPool = meta.Pool{Name: gcPoolName, NS: gcNS}
		pi := z.PlacementPools["default-placement"]
		pi.InlineData = true
		pi.StorageClasses["COLD"] = meta.ZoneStorageClass{DataPool: new(meta.ParsePool(coldPool))}
		z.PlacementPools["default-placement"] = pi
	})
	editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
		p.PeriodMap.ShortZoneIDs = map[string]uint32{putZoneID: 12345}
	})
	seedGCShards(c, 32)
	owner := meta.NewUserInfo()
	owner.UserID, owner.DisplayName = meta.UserID{ID: "alice"}, "Alice"
	seedUser(c, owner, nil, meta.ObjVersion{Ver: 1, Tag: "alice"})
	return c
}

// openPutStore opens a Store over c at rel with the options kv, its clock at
// now and its random strings fixedRand(putPrefix).
func openPutStore(ctx context.Context, c *fakerados.Cluster, rel denc.Release, kv map[string]string, now time.Time) *driver.Store {
	GinkgoHelper()
	s, err := driver.Open(ctx, c, conf(kv), driver.Options{Release: &rel})
	Expect(err).NotTo(HaveOccurred())
	driver.SetClock(s, func() time.Time { return now })
	s.SetRandForTest(fixedRand(putPrefix))
	return s
}

// fixedRand returns s, cut to n characters, for every call.
func fixedRand(s string) func(int) string {
	return func(n int) string { return s[:min(n, len(s))] }
}

func md5hex[T string | []byte](b T) string {
	sum := md5.Sum([]byte(b))
	return hex.EncodeToString(sum[:])
}

// names lists the names the SetXattr steps among steps set.
func names(steps []radosclient.Step) []string {
	var out []string
	for _, st := range steps {
		if x, ok := st.(*radosclient.SetXattrStep); ok {
			out = append(out, x.Name)
		}
	}
	return out
}

// settle waits for every bucket index completion s submitted.
func settle(s *driver.Store) {
	GinkgoHelper()
	Eventually(s.PendingCompletionsForTest).WithTimeout(5 * time.Second).WithPolling(time.Millisecond).Should(BeZero())
}

// poolObjects lists a namespace's objects.
func poolObjects(ctx context.Context, c *fakerados.Cluster, pool, ns string) []string {
	GinkgoHelper()
	p, err := c.Pool(ctx, pool, ns)
	Expect(err).NotTo(HaveOccurred())
	var oids []string
	Expect(p.ListObjects(ctx, func(oid, _ string) error {
		oids = append(oids, oid)
		return nil
	})).To(Succeed())
	return oids
}

// cutReader yields data, then err in place of io.EOF.
type cutReader struct {
	r   *bytes.Reader
	err error
}

func (c *cutReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		return n, c.err
	}
	return n, err
}

// cancelAtEOF yields r, then ends its request's context as it reports
// io.EOF, as a client that leaves once its body is sent does.
type cancelAtEOF struct {
	r      *bytes.Reader
	cancel context.CancelFunc
}

func (c *cancelAtEOF) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		c.cancel()
	}
	return n, err
}

// tailOID is the RADOS name of tail stripe n of a PUT under prefix.
func tailOID(prefix string, n int) string {
	return fmt.Sprintf("%s__shadow_.%s_%d", putBucketID, prefix, n)
}

var _ = Describe("PutObject", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		mtime   time.Time
		attrs   map[string][]byte
		headOID string
		shard   string
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		// The clock keeps its own copy: an index completion of the spec
		// before may still read it when this one starts.
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		attrs = map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
		headOID = putBucketID + "_k"
		shard = indexShardOID(rec, "k")
	})
	entry := func() rgwcls.DirEntry {
		GinkgoHelper()
		en, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the index entry of k")
		return en
	}
	put := func(ctx context.Context, body []byte, p op.PutParams) (*op.PutResult, error) {
		if p.Attrs == nil {
			p.Attrs = attrs
		}
		if p.Size == 0 && len(body) > 0 {
			p.Size = int64(len(body))
		}
		return s.PutObject(ctx, rec, key, bytes.NewReader(body), p)
	}

	It("writes a small object with radosgw's two round trips and attributes", func(ctx SpecContext) {
		attrs[meta.AttrContentType] = []byte("text/plain\x00")
		attrs[meta.AttrMetaPrefix+"k"] = []byte("v\x00")
		release := make(chan struct{})
		indexWrites := 0
		c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
			indexWrites++
			if indexWrites == 2 {
				<-release
			}
		})
		res, err := s.PutObject(ctx, rec, key, strings.NewReader("hello"), op.PutParams{Attrs: attrs, Size: 5, Tag: "tx000001-a", Mtime: mtime})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(1), "PutObject returned with its completion still held: it does not wait for it")
		close(release)
		settle(s)
		Expect(res.ETag).To(Equal("5d41402abc4b2a76b9719d911017c592"))
		Expect(res.Size).To(BeEquivalentTo(5))
		Expect(res.Mtime).To(Equal(mtime))
		head := c.Object(testDataPool, "", headOID)
		Expect(head.Data).To(Equal([]byte("hello")))
		Expect(res.Epoch).To(Equal(head.Version))
		Expect(head.Mtime).To(Equal(mtime))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx000001-a\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx000001-a\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte("5d41402abc4b2a76b9719d911017c592")), "no NUL, rgw_op.cc:4522-4523")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.content_type", []byte("text/plain\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-k", []byte("v\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.source_zone", []byte{0x39, 0x30, 0, 0}), "the short id 12345 as LE u32")
		Expect(head.Xattrs).To(HaveKey("user.rgw.pg_ver"))
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.storage_class"), "an empty class writes no attr")
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.ObjSize).To(BeEquivalentTo(5))
		Expect(m.HeadSize).To(BeEquivalentTo(5))
		Expect(m.MaxHeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.Prefix).To(Equal("." + putPrefix + "_"))
		Expect(m.Rules[0]).To(Equal(meta.ManifestRule{StartOfs: 4 << 20, StripeMaxSize: 4 << 20}))
		Expect(m.TailPlacement).To(Equal(meta.BucketPlacement{Bucket: rec.Info.Bucket, PlacementRule: meta.PlacementRule{Name: "default-placement"}}))

		w := c.LastWrite(testDataPool, "", headOID)
		steps := w.Steps()
		Expect(steps).To(HaveLen(11))
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}), "the assume-no-entry pass")
		Expect(steps[1]).To(Equal(&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte("tx000001-a\x00")}))
		Expect(steps[2]).To(Equal(&radosclient.SetXattrStep{Name: "user.rgw.tail_tag", Value: []byte("tx000001-a\x00")}))
		Expect(steps[3]).To(Equal(&radosclient.WriteFullStep{Data: []byte("hello")}))
		Expect(names(steps[4:5])).To(Equal([]string{"user.rgw.manifest"}))
		Expect(names(steps[5:9])).To(Equal([]string{"user.rgw.acl", "user.rgw.content_type", "user.rgw.etag", "user.rgw.x-amz-meta-k"}), "std::map order")
		Expect(execIn(w, 9, "obj_store_pg_ver", rgwcls.DecodeStorePGVerOp).Attr).To(Equal("user.rgw.pg_ver"))
		Expect(names(steps[10:])).To(Equal([]string{"user.rgw.source_zone"}))
		mt, ok := w.Mtime()
		Expect([]any{mt, ok}).To(Equal([]any{mtime, true}))
		Expect(w.Flags()).To(Equal(radosclient.OpFlagNone))

		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "the prepare and the complete")
		prep := execIn(c.WritesTo(rookIndexPool, "", shard)[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(prep.Tag).To(Equal("tx000001-a"), "the bare tag")
		en := entry()
		Expect(en.Exists).To(BeTrue())
		Expect(en.Tag).To(Equal("tx000001-a"))
		Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{
			Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, Mtime: mtime, ETag: "5d41402abc4b2a76b9719d911017c592",
			Owner: "alice", OwnerDisplayName: "Alice", ContentType: "text/plain",
		}))
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: head.Version}))
		Expect(c.Writes(testDataPool, "", headOID)).To(Equal(1), "one head write: two RADOS round trips in all")
		Expect(c.Reads(testDataPool, "", headOID)).To(BeZero(), "the assume-no-entry pass reads nothing")
	})

	It("writes a 10 MiB object as a head and two tails that the manifest's stripes read back", func(ctx SpecContext) {
		body := bytes.Repeat([]byte("0123456789abcdef"), (10<<20)/16)
		res, err := put(ctx, body, op.PutParams{Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(md5hex(body)))
		head := c.Object(testDataPool, "", headOID)
		Expect(head.Data).To(Equal(body[:4<<20]))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1)).Data).To(Equal(body[4<<20 : 8<<20]))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 2)).Data).To(Equal(body[8<<20:]))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.ObjSize).To(BeEquivalentTo(10 << 20))
		Expect(m.HeadSize).To(BeEquivalentTo(4 << 20))
		stripes, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		oids := make([]string, 0, len(stripes))
		for _, st := range stripes {
			oids = append(oids, st.OID())
		}
		Expect(oids).To(Equal([]string{headOID, tailOID(putPrefix, 1), tailOID(putPrefix, 2)}))
		settle(s)
		Expect(entry().Meta.Size).To(BeEquivalentTo(10 << 20))
	})

	It("overwrites with the EEXIST retry, guards on the old tag, removes the old head and queues its tails", func(ctx SpecContext) {
		_, err := put(ctx, bytes.Repeat([]byte("a"), 6<<20), op.PutParams{Tag: "tx-old"})
		Expect(err).NotTo(HaveOccurred())
		oldTail := tailOID(putPrefix, 1)
		Expect(c.Object(testDataPool, "", oldTail)).NotTo(BeNil())
		c.Object(testDataPool, "", headOID).Xattrs["user.rgw.olh.idtag"] = []byte("olh")
		settle(s)
		s.SetRandForTest(fixedRand(secondPrefix))
		c.ResetCounters()
		res, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(md5hex("new")))
		Expect(c.Writes(testDataPool, "", headOID)).To(Equal(2), "the exclusive create fails EEXIST, then the guarded write")
		Expect(c.Reads(testDataPool, "", headOID)).To(Equal(1), "one raw_obj_stat between them")
		Expect(c.WritesTo(testDataPool, "", headOID)[0].Steps()[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))
		last := c.LastWrite(testDataPool, "", headOID)
		steps := last.Steps()
		Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-old\x00")}))
		Expect(steps[1]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
		Expect(execIn(last, 2, "obj_remove", rgwcls.DecodeObjRemoveOp).KeepAttrPrefixes).To(Equal([]string{"user.rgw.olh."}))
		head := c.Object(testDataPool, "", headOID)
		Expect(head.Data).To(Equal([]byte("new")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.olh.idtag", []byte("olh")), "remove_rgw_head_obj keeps the olh attrs")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx-new\x00")))

		gcOID := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-old\x00"))
		entries := c.GCEntries(gcPoolName, gcNS, gcOID)
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal("tx-old\x00"), "the tail_tag's bytes, NUL included")
		Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: oldTail}}}))
		Expect(entries[0].Time).To(Equal(mtime.Add(2*time.Hour)), "rgw_gc_obj_min_wait")
		gcOp := c.LastWrite(gcPoolName, gcNS, gcOID)
		Expect(gcOp.Steps()).To(HaveLen(2))
		Expect(execIn(gcOp, 0, "check_conds", version.DecodeCheckOp).Conds).
			To(Equal([]version.Condition{{Ver: version.ObjVersion{Ver: 1}, Cond: version.CondEQ}}), "gc_log_enqueue2's cls_version_check")
		Expect(execIn(gcOp, 1, "rgw_gc_queue_enqueue", rgwcls.DecodeGCSetEntryOp).ExpirationSecs).To(BeEquivalentTo(7200))
		Expect(c.Object(testDataPool, "", oldTail)).NotTo(BeNil(), "the GC worker, not the PUT, removes it")

		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "prepared once for both passes (UpdateIndex::is_prepared), then completed")
		execIn(c.WritesTo(rookIndexPool, "", shard)[1], 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(entry().Tag).To(Equal("tx-new"))
	})

	It("queues on gc_set_entry when the shard has not moved to the queue", func(ctx SpecContext) {
		_, err := put(ctx, bytes.Repeat([]byte("a"), 6<<20), op.PutParams{Tag: "tx-old"})
		Expect(err).NotTo(HaveOccurred())
		gcOID := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-old\x00"))
		c.Object(gcPoolName, gcNS, gcOID).Xattrs[version.XattrName] = encode(version.ObjVersion{})
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred())
		writes := c.WritesTo(gcPoolName, gcNS, gcOID)
		Expect(writes).To(HaveLen(2), "the enqueue, refused ECANCELED, then the omap-era entry")
		set := execIn(writes[1], 0, "gc_set_entry", rgwcls.DecodeGCSetEntryOp)
		Expect(set.ExpirationSecs).To(BeEquivalentTo(7200))
		Expect(set.Info.Tag).To(Equal("tx-old\x00"))
		Expect(set.Info.Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: tailOID(putPrefix, 1)}}}))
		Expect(c.Object(gcPoolName, gcNS, gcOID).Omap).To(HaveKey("0_tx-old\x00"))
		Expect(c.GCEntries(gcPoolName, gcNS, gcOID)).To(BeEmpty())
	})

	It("deletes the old tails inline with refcount put under full-try when the gc shard refuses", func(ctx SpecContext) {
		_, err := put(ctx, bytes.Repeat([]byte("a"), 6<<20), op.PutParams{Tag: "tx-old"})
		Expect(err).NotTo(HaveOccurred())
		oldTail := tailOID(putPrefix, 1)
		c.RegisterClass("rgw_gc", func(*fakerados.ClassCall) ([]byte, int32) { return nil, -int32(syscall.EIO) }, fakerados.GCQueueWriteMethods...)
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred(), "complete_atomic_modification never fails the write")
		w := c.LastWrite(testDataPool, "", oldTail)
		Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), "set_pool_full_try, rgw_rados.cc:5448")
		Expect(execIn(w, 0, "put", refcount.DecodePutOp)).To(Equal(refcount.PutOp{Tag: "tx-old\x00", ImplicitRef: true}))
		Expect(c.Object(testDataPool, "", oldTail)).To(BeNil(), "the implicit wildcard ref was the last")
		Expect(c.Object(testDataPool, "", headOID).Data).To(Equal([]byte("new")))
	})

	It("answers success and cancels with radosgw's -1:0 when another writer replaced the head", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("first"), op.PutParams{Attrs: attrs, Size: 5, Tag: "tx-1"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		s.SetRandForTest(fixedRand(secondPrefix))
		writes := 0
		c.BeforeWrite(testDataPool, "", headOID, func(o *fakerados.Object) {
			writes++
			if writes == 2 && o != nil {
				o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00")
			}
		})
		body := bytes.Repeat([]byte("b"), 5<<20)
		res, err := put(ctx, body, op.PutParams{Tag: "tx-2"})
		Expect(err).NotTo(HaveOccurred(), "rgw_rados.cc:3388-3391: ECANCELED without conditions is success")
		Expect(res.ETag).To(Equal(md5hex(body)))
		Expect(c.Object(testDataPool, "", headOID).Xattrs["user.rgw.idtag"]).To(Equal([]byte("tx-racer\x00")), "the racer's head stands")
		Expect(c.Object(testDataPool, "", tailOID(secondPrefix, 1))).To(BeNil(), "our tail was deleted, as ~RadosWriter deletes what a canceled write wrote")
		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(4), "prepare and complete of the first PUT, prepare and cancel of the second")
		cancel := execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(cancel.Op).To(Equal(rgwcls.OpCancel))
		Expect(cancel.Tag).To(Equal("tx-2"))
		Expect(cancel.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "cls_obj_complete_cancel, rgw_rados.cc:9553-9563")
		en := entry()
		Expect(en.Tag).To(Equal("tx-1"), "the first PUT's entry stands")
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "the class copies the cancel's version onto the entry, tracker #80894")
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 5, SizeRounded: 4096, NumObjects: 1}), "a canceled write leaves the quota cache alone")
	})

	It("refuses If-None-Match: * on an existing key before touching the index", func(ctx SpecContext) {
		_, err := put(ctx, []byte("x"), op.PutParams{Tag: "t1"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		c.ResetCounters()
		_, err = put(ctx, []byte("y"), op.PutParams{Tag: "t2", IfNoneMatch: "*"})
		Expect(err).To(MatchError(op.ErrPreconditionFailed))
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "prepare_atomic_modification fails before UpdateIndex::prepare")
		Expect(c.Reads(testDataPool, "", headOID)).To(Equal(1), "a condition skips the assume-no-entry pass")
		Expect(c.Writes(testDataPool, "", headOID)).To(BeZero())
	})

	It("refuses a body whose MD5 is not the Content-MD5 and leaves no object behind", func(ctx SpecContext) {
		sum := md5.Sum([]byte("other"))
		_, err := put(ctx, bytes.Repeat([]byte("c"), 5<<20), op.PutParams{Tag: "t", ContentMD5: sum[:]})
		Expect(err).To(MatchError(op.ErrBadDigest))
		Expect(c.Writes(testDataPool, "", tailOID(putPrefix, 1))).To(Equal(2), "the tail was written, then removed")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "no head was written")
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("refuses a body the decoded length cut short of the Content-MD5's payload with BadDigest", func(ctx SpecContext) {
		full := []byte("hello, world")
		sum := md5.Sum(full)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(full[:5]), op.PutParams{Attrs: attrs, Size: 5, Tag: "t", ContentMD5: sum[:]})
		Expect(err).To(MatchError(op.ErrBadDigest), "rgw_op.cc:4483-4486 at v19.2.6, :4715-4718 at v20.2.4")
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
	})

	DescribeTable("answers RequestTimeout when the body ends short of its length, before any Content-MD5",
		func(ctx SpecContext, body io.Reader, size int64) {
			sum := md5.Sum([]byte("abc"))
			_, err := s.PutObject(ctx, rec, key, body, op.PutParams{Attrs: attrs, Size: size, Tag: "t", ContentMD5: sum[:]})
			Expect(err).To(MatchError(op.ErrRequestTimeout), "rgw_op.cc:4430-4433 at v19.2.6, :4662-4665 at v20.2.4")
			Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		},
		Entry("a plain body shorter than Content-Length", strings.NewReader("abc"), int64(10)),
		Entry("an aws-chunked body cut short, which the reader reports as io.ErrUnexpectedEOF",
			&cutReader{r: bytes.NewReader([]byte("abc")), err: io.ErrUnexpectedEOF}, int64(10)),
		Entry("an aws-chunked body of unknown length cut short", &cutReader{r: bytes.NewReader([]byte("abc")), err: io.ErrUnexpectedEOF}, int64(-1)),
	)

	It("passes a body verification error through verbatim and deletes what it wrote", func(ctx SpecContext) {
		body := &cutReader{r: bytes.NewReader(bytes.Repeat([]byte("d"), 9<<20)), err: op.ErrSignatureDoesNotMatch}
		_, err := s.PutObject(ctx, rec, key, body, op.PutParams{Attrs: attrs, Size: 9 << 20, Tag: "t"})
		Expect(err).To(BeIdenticalTo(op.ErrSignatureDoesNotMatch))
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	It("refuses a body past rgw_max_put_size with EntityTooLarge", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_put_size": "6291456"}, mtime)
		_, err := put(ctx, bytes.Repeat([]byte("e"), 7<<20), op.PutParams{Tag: "t"})
		Expect(err).To(MatchError(op.ErrEntityTooLarge), "rgw_rest.cc:1096-1098")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	It("stores an empty object as an empty head", func(ctx SpecContext) {
		res, err := s.PutObject(ctx, rec, key, strings.NewReader(""), op.PutParams{Attrs: attrs, Size: 0, Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal("d41d8cd98f00b204e9800998ecf8427e"))
		head := c.Object(testDataPool, "", headOID)
		Expect(head).NotTo(BeNil())
		Expect(head.Data).To(BeEmpty())
		Expect(c.LastWrite(testDataPool, "", headOID).Steps()).To(ContainElement(&radosclient.WriteFullStep{Data: []byte{}}),
			"write_full even when empty")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(Equal([]string{headOID}))
	})

	It("places a COLD object entirely in tails numbered from zero and records the class", func(ctx SpecContext) {
		body := bytes.Repeat([]byte("d"), 5<<20)
		_, err := put(ctx, body, op.PutParams{Tag: "t", StorageClass: "COLD"})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(testDataPool, "", headOID)
		Expect(head.Data).To(BeEmpty())
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.storage_class", []byte("COLD")))
		Expect(c.Object(coldPool, "", tailOID(putPrefix, 0)).Data).To(Equal(body[:4<<20]))
		Expect(c.Object(coldPool, "", tailOID(putPrefix, 1)).Data).To(Equal(body[4<<20:]))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.MaxHeadSize).To(BeZero())
		Expect(m.HeadSize).To(BeZero())
		Expect(m.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
		set := names(c.LastWrite(testDataPool, "", headOID).Steps())
		Expect(set[len(set)-1]).To(Equal("user.rgw.storage_class"), "the last attr, after source_zone")
		settle(s)
		Expect(entry().Meta.StorageClass).To(Equal("COLD"))
	})

	It("adjusts the quota cache with the new object and the size it replaced", func(ctx SpecContext) {
		_, err := put(ctx, []byte("12345"), op.PutParams{Tag: "t1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 5, SizeRounded: 4096, NumObjects: 1}),
			"the period's bucket quota had the check fetch the empty bucket's stats")
		_, err = put(ctx, []byte("abc"), op.PutParams{Tag: "t2"})
		Expect(err).NotTo(HaveOccurred())
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{Size: 3, SizeRounded: 4096, NumObjects: 1}), "rgw_rados.cc:3358-3366: no new object, 3 added, 5 removed")
	})

	It("checks the quota on the bytes received, as the second check_quota does", func(ctx SpecContext) {
		rec.Info.Quota = meta.Quota{MaxSize: 8, MaxObjects: 1, CheckOnRaw: true, Enabled: true}
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("123456789"), op.PutParams{Attrs: attrs, Size: -1, Tag: "t"})
		Expect(err).To(MatchError(op.ErrQuotaExceeded), "rgw_op.cc:4444-4448 at v19.2.6, :4676-4680 at v20.2.4")
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("1234"), op.PutParams{Attrs: attrs, Size: -1, Tag: "t1"})
		Expect(err).NotTo(HaveOccurred(), "4 bytes and one object fit the empty bucket")
		settle(s)
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("1"), op.PutParams{Attrs: attrs, Size: -1, Tag: "t2"})
		Expect(err).To(MatchError(op.ErrQuotaExceeded),
			"5 bytes fit, but the check counts one object even for an overwrite: 1 + 1 > 1 (RGWRados::check_quota, rgw_rados.cc:10608-10618 at v19.2.6)")
	})

	It("finishes a PUT whose client leaves once its body is sent, the quota check included", func(ctx SpecContext) {
		reqCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		body := &cancelAtEOF{r: bytes.NewReader(bytes.Repeat([]byte("q"), 5<<20)), cancel: cancel}
		_, err := s.PutObject(reqCtx, rec, key, body, op.PutParams{Attrs: attrs, Size: 5 << 20, Tag: "t"})
		Expect(err).NotTo(HaveOccurred(), "radosgw's second check_quota, which loads the owner and fetches the bucket's stats, does not watch the client")
		Expect(reqCtx.Err()).To(MatchError(context.Canceled))
		Expect(c.Object(testDataPool, "", headOID)).NotTo(BeNil())
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1))).NotTo(BeNil())
	})

	It("keeps its tails when the head write times out, as it may yet land", func(ctx SpecContext) {
		rgw := fakerados.RGWClass()
		c.RegisterClass("rgw", func(call *fakerados.ClassCall) ([]byte, int32) {
			if call.Method == "obj_store_pg_ver" {
				return nil, -int32(syscall.ETIMEDOUT)
			}
			return rgw(call)
		}, fakerados.RGWWriteMethods...)
		_, err := put(ctx, bytes.Repeat([]byte("t"), 6<<20), op.PutParams{Tag: "t"})
		Expect(err).To(MatchError(op.ErrRequestTimedOut))
		Expect(c.Object(testDataPool, "", tailOID(putPrefix, 1))).NotTo(BeNil(), "writer.clear_written, rgw_putobj_processor.cc:395-401")
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(1), "no cancel after ETIMEDOUT: the entry stays pending (rgw_rados.cc:3370-3374)")
	})

	It("queues the tails of a head without write tags under the tag radosgw fakes for it", func(ctx SpecContext) {
		_, err := put(ctx, bytes.Repeat([]byte("f"), 6<<20), op.PutParams{Tag: "old"})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(testDataPool, "", headOID)
		delete(head.Xattrs, meta.AttrIDTag)
		delete(head.Xattrs, meta.AttrTailTag)
		sum := md5.Sum(append(slices.Clone(head.Xattrs[meta.AttrManifest]), head.Xattrs[meta.AttrETag]...))
		fake := tailOID(putPrefix, 1) + "_" + hex.EncodeToString(sum[:]) + "\x00"
		s.SetRandForTest(fixedRand(secondPrefix))
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "new"})
		Expect(err).NotTo(HaveOccurred())
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, fake)))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal(fake), "generate_fake_tag: the first tail's oid, _, and the MD5 of the manifest and etag attrs (rgw_rados.cc:6052-6083)")
		Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: tailOID(putPrefix, 1)}}}))
	})

	It("refuses a PUT whose aligned chunk passes the put window before it writes anything", func(ctx SpecContext) {
		c.SetRequiredAlignment(testDataPool, 32<<20)
		_, err := put(ctx, []byte("small"), op.PutParams{Tag: "t"})
		Expect(err).To(MatchError(op.ErrUnknown), "BlockingAioThrottle's EDEADLK, which set_req_state_err answers with 500 UnknownError")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("tags a write the request gave no tag with append_rand_alpha's 32 characters", func(ctx SpecContext) {
		_, err := put(ctx, []byte("x"), op.PutParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object(testDataPool, "", headOID).Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("_"+putPrefix+"\x00")))
	})

	It("stamps the store's clock on a write without an mtime", func(ctx SpecContext) {
		res, err := put(ctx, []byte("x"), op.PutParams{Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Mtime).To(Equal(mtime))
		mt, ok := c.LastWrite(testDataPool, "", headOID).Mtime()
		Expect([]any{mt, ok}).To(Equal([]any{mtime, true}))
	})
})

var _ = Describe("PutObject's conditions", func() {
	DescribeTable("per release",
		func(ctx SpecContext, rel denc.Release, exists bool, ifMatch, ifNoneMatch string, want error) {
			now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			c := newPutCluster()
			s := openPutStore(ctx, c, rel, nil, now)
			rec := testBucket(putBucketID, 11)
			seedShards(c, rec)
			seedRecordInstance(c, rec)
			key := meta.ObjKey{Name: "k"}
			if exists {
				_, err := s.PutObject(ctx, rec, key, strings.NewReader("x"), op.PutParams{Size: 1, Tag: "t0"})
				Expect(err).NotTo(HaveOccurred())
			}
			_, err := s.PutObject(ctx, rec, key, strings.NewReader("y"), op.PutParams{Size: 1, Tag: "t1", IfMatch: ifMatch, IfNoneMatch: ifNoneMatch})
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
				Expect(c.Object(testDataPool, "", putBucketID+"_k").Data).To(Equal([]byte("y")))
				return
			}
			Expect(err).To(MatchError(want))
			if exists {
				Expect(c.Object(testDataPool, "", putBucketID+"_k").Data).To(Equal([]byte("x")))
			} else {
				Expect(c.Object(testDataPool, "", putBucketID+"_k")).To(BeNil())
			}
		},
		Entry("If-Match * on a missing key: 404, Squid answering as Tentacle", denc.Squid, false, "*", "", op.ErrNoSuchKey),
		Entry("If-Match * on a missing key: Tentacle's 404", denc.Tentacle, false, "*", "", op.ErrNoSuchKey),
		Entry("If-Match * on an existing key", denc.Squid, true, "*", "", nil),
		Entry("If-Match quoted etag, unquoted on Squid too", denc.Squid, true, `"`+md5hex("x")+`"`, "", nil),
		Entry("If-Match quoted etag on Tentacle, unquoted", denc.Tentacle, true, `"`+md5hex("x")+`"`, "", nil),
		Entry("If-Match bare etag on Squid", denc.Squid, true, md5hex("x"), "", nil),
		Entry("If-Match that begins with the etag, compared over the etag's length (tracker #80924)", denc.Tentacle, true, md5hex("x")+"-more", "", nil),
		Entry("If-Match other etag on Tentacle", denc.Tentacle, true, md5hex("other"), "", op.ErrPreconditionFailed),
		Entry("If-Match etag on a missing key: 404, Squid answering as Tentacle", denc.Squid, false, md5hex("x"), "", op.ErrNoSuchKey),
		Entry("If-None-Match * on a missing key", denc.Squid, false, "", "*", nil),
		Entry("If-None-Match * on an existing key: Tentacle's 412", denc.Tentacle, true, "", "*", op.ErrPreconditionFailed),
		Entry("If-None-Match matching etag", denc.Tentacle, true, "", md5hex("x"), op.ErrPreconditionFailed),
		Entry("If-None-Match quoted matching etag, unquoted on Squid too", denc.Squid, true, "", `"`+md5hex("x")+`"`, op.ErrPreconditionFailed),
		Entry("If-None-Match other etag on Tentacle", denc.Tentacle, true, "", md5hex("other"), nil),
		Entry("If-None-Match other etag on a missing key: Squid writes, as Tentacle does", denc.Squid, false, "", md5hex("other"), nil),
		Entry("If-None-Match other etag on a missing key: Tentacle writes", denc.Tentacle, false, "", md5hex("other"), nil),
	)
})
