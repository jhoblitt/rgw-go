package driver_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/compression"
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
		c.ResetCounters()
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

	DescribeTable("refuses a body of unknown length past rgw_max_put_size once get_data's chunk that carries it there is read",
		func(ctx SpecContext, data int, want error) {
			s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_put_size": "6291456"}, mtime)
			body := &cutReader{r: bytes.NewReader(bytes.Repeat([]byte("e"), data)), err: op.ErrSignatureDoesNotMatch}
			_, err := s.PutObject(ctx, rec, key, body, op.PutParams{Attrs: attrs, Size: -1, Tag: "t"})
			Expect(err).To(MatchError(want))
			Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
		},
		Entry("a body error inside the 4-8 MiB chunk wins, as recv_body's error precedes the size check", 7<<20, op.ErrSignatureDoesNotMatch),
		Entry("EntityTooLarge once that chunk is whole, ahead of a later body error", 9<<20, op.ErrEntityTooLarge),
	)

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
		c.FailNextWrite(testDataPool, "", headOID, syscall.ETIMEDOUT)
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

// errAfter yields data, then err with the read that returns its last bytes.
type errAfter struct {
	data []byte
	err  error
}

func (e *errAfter) Read(p []byte) (int, error) {
	n := copy(p, e.data)
	e.data = e.data[n:]
	if len(e.data) == 0 {
		return n, e.err
	}
	return n, nil
}

// piecesReader yields each of pieces in reads of its own, then err, counting
// the bytes it has handed out.
type piecesReader struct {
	pieces [][]byte
	err    error
	read   int
}

func (r *piecesReader) Read(p []byte) (int, error) {
	if len(r.pieces) == 0 {
		return 0, r.err
	}
	n := copy(p, r.pieces[0])
	r.pieces[0] = r.pieces[0][n:]
	if len(r.pieces[0]) == 0 {
		r.pieces = r.pieces[1:]
	}
	r.read += n
	return n, nil
}

// The stage specs below hold the body to 6 bytes in chunks of 4, so the
// chunk get_data reads that carries the body past its limit is bytes 4-8.
var _ = Describe("the plaintext stage", func() {
	It("hands every byte of the body to each sink and counts them", func() {
		body := mibPattern(3)
		h, buf := md5.New(), &bytes.Buffer{}
		r, n := driver.PlaintextForTest(bytes.NewReader(body), uint64(len(body)), 1<<20, h, buf)
		got, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(body))
		Expect(buf.Bytes()).To(Equal(body))
		sum := md5.Sum(body)
		Expect(h.Sum(nil)).To(Equal(sum[:]))
		Expect(n()).To(BeEquivalentTo(len(body)), "a body of exactly rgw_max_put_size passes")
	})
	It("refuses with EntityTooLarge at the end of the chunk that carries the body past its limit", func() {
		r, n := driver.PlaintextForTest(bytes.NewReader([]byte("0123456789")), 6, 4)
		p := make([]byte, 4)
		k, err := r.Read(p)
		Expect([]any{k, err}).To(Equal([]any{4, nil}))
		_, err = r.Read(p)
		Expect(err).To(MatchError(op.ErrEntityTooLarge), "get_data's ERR_TOO_LARGE, rgw_rest.cc:1101-1103 at v19.2.7 and v20.2.4")
		Expect(n()).To(BeEquivalentTo(8))
	})
	It("lets a body error inside that chunk win, as get_data returns recv_body's error before its size check", func() {
		r, _ := driver.PlaintextForTest(&piecesReader{pieces: [][]byte{[]byte("0123456")}, err: op.ErrBadDigest}, 6, 4)
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrBadDigest), "rgw_rest.cc:1090-1093 at v19.2.7 and v20.2.4")
	})
	It("reads no further than that chunk's end, and refuses there, ahead of a later body error", func() {
		body := &piecesReader{pieces: [][]byte{[]byte("0123456"), []byte("78901")}, err: op.ErrBadDigest}
		r, _ := driver.PlaintextForTest(body, 6, 4)
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrEntityTooLarge))
		Expect(body.read).To(Equal(8), "recv_body fills the chunk and reads no more")
	})
	It("refuses a body that ends inside that chunk", func() {
		r, _ := driver.PlaintextForTest(&piecesReader{pieces: [][]byte{[]byte("0123456")}, err: io.EOF}, 6, 4)
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrEntityTooLarge))
	})
	It("refuses at once a read that runs past that chunk's end, whatever error comes with it", func() {
		r, _ := driver.PlaintextForTest(&errAfter{data: []byte("0123456789"), err: op.ErrBadDigest}, 6, 4)
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrEntityTooLarge), "the chunk was whole before the error came")
	})
})

// compClass is the PUT fixture's storage class whose compression type is typ.
func compClass(typ string) string { return "COMP_" + strings.ToUpper(typ) }

// withCompressionClasses gives the fixture zone's default-placement a class
// for each of types, named by compClass, its data in the STANDARD pool.
func withCompressionClasses(c *fakerados.Cluster, types ...string) {
	editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
		pi := z.PlacementPools["default-placement"]
		for _, t := range types {
			pi.StorageClasses[compClass(t)] = meta.ZoneStorageClass{DataPool: new(meta.ParsePool(testDataPool)), CompressionType: new(t)}
		}
		z.PlacementPools["default-placement"] = pi
	})
}

// hexNoise is n seeded random hex digits, half the entropy of random bytes:
// every codec stores 10 MiB of it in more than one 4 MiB head.
func hexNoise(n int) []byte {
	r := rand.New(rand.NewPCG(3, 4))
	const digits = "0123456789abcdef"
	out := make([]byte, n)
	for i := range out {
		out[i] = digits[r.IntN(len(digits))]
	}
	return out
}

// storedStream is what a PUT under prefix stored: its head's bytes, then
// each tail's.
func storedStream(c *fakerados.Cluster, headOID, prefix string) []byte {
	out := slices.Clone(c.Object(testDataPool, "", headOID).Data)
	for n := 1; c.Object(testDataPool, "", tailOID(prefix, n)) != nil; n++ {
		out = append(out, c.Object(testDataPool, "", tailOID(prefix, n)).Data...)
	}
	return out
}

// readBack is n bytes at ofs of st, read through ReadObject.
func readBack(ctx context.Context, s *driver.Store, st *op.ObjectState, ofs, n uint64) []byte {
	GinkgoHelper()
	var buf bytes.Buffer
	Expect(s.ReadObject(ctx, st, op.ByteRange{Offset: ofs, Length: n}, &buf)).To(Succeed())
	return buf.Bytes()
}

// afterWriteFull is the step after steps' write_full, nil when there is none.
func afterWriteFull(steps []radosclient.Step) radosclient.Step {
	for i, st := range steps {
		if _, ok := st.(*radosclient.WriteFullStep); ok && i+1 < len(steps) {
			return steps[i+1]
		}
	}
	return nil
}

// tailHints is the alloc hint flags of every write to the tails under prefix.
func tailHints(c *fakerados.Cluster, prefix string) []radosclient.AllocHintFlags {
	var out []radosclient.AllocHintFlags
	for n := 1; c.Object(testDataPool, "", tailOID(prefix, n)) != nil; n++ {
		for _, w := range c.WritesTo(testDataPool, "", tailOID(prefix, n)) {
			if h, ok := w.Steps()[0].(*radosclient.SetAllocHintStep); ok {
				out = append(out, h.Flags)
			}
		}
	}
	return out
}

// failingEncoder is codec's encoder that fails its calls from the n-th on.
type failingEncoder struct {
	enc   compression.Encoder
	calls *atomic.Int32
	n     int32
}

func (f failingEncoder) Encode(dst, src []byte) ([]byte, *int32, error) {
	if f.calls.Add(1) >= f.n {
		return nil, nil, errors.New("compress failed")
	}
	return f.enc.Encode(dst, src)
}

var _ = Describe("PutObject into a compressing class", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		attrs   map[string][]byte
		headOID string
		shard   string
		mtime   time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		withCompressionClasses(c, compression.Zlib, compression.Snappy, compression.Zstd, compression.LZ4, "random", "brotli")
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
	put := func(ctx context.Context, body []byte, class string, p op.PutParams) (*op.PutResult, error) {
		p.Attrs, p.StorageClass, p.Tag = attrs, class, "t"
		if p.Size == 0 {
			p.Size = int64(len(body))
		}
		return s.PutObject(ctx, rec, key, bytes.NewReader(body), p)
	}
	compressionInfo := func() (meta.CompressionInfo, bool) {
		GinkgoHelper()
		b, ok := c.Object(testDataPool, "", headOID).Xattrs[meta.AttrCompression]
		if !ok {
			return meta.CompressionInfo{}, false
		}
		d := denc.NewDecoder(b)
		ci := meta.DecodeCompressionInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return ci, true
	}

	DescribeTable("stores a 10 MiB body as its codec's blocks, the head and tails cut from the compressed stream",
		func(ctx SpecContext, codec string, message *int32) {
			body := hexNoise(10 << 20)
			res, err := put(ctx, body, compClass(codec), op.PutParams{})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.ETag).To(Equal(md5hex(body)), "the MD5 of the plaintext")
			head := c.Object(testDataPool, "", headOID)
			stored := storedStream(c, headOID, putPrefix)
			Expect(len(stored)).To(BeNumerically(">", 4<<20), "the stored stream has tails")
			Expect(head.Data).To(Equal(stored[:4<<20]), "HeadObjectProcessor's head chunk is the stream's first 4 MiB")
			m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
			Expect([]uint64{m.ObjSize, m.HeadSize}).To(Equal([]uint64{uint64(len(stored)), 4 << 20}), "the manifest's size is the stored size")

			ci, ok := compressionInfo()
			Expect(ok).To(BeTrue(), "user.rgw.compression")
			Expect([]any{ci.Type, ci.OrigSize, ci.CompressorMessage}).To(Equal([]any{codec, uint64(10 << 20), message}))
			Expect(ci.Blocks).To(HaveLen(3), "one block per rgw_max_chunk_size of plaintext, the last shorter")
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			var end uint64
			for i, b := range ci.Blocks {
				Expect([]uint64{b.OldOfs, b.NewOfs}).To(Equal([]uint64{uint64(i) << 22, end}), "block %d: its plaintext offset, and the end of the block before", i)
				plain := body[b.OldOfs:min(b.OldOfs+4<<20, 10<<20)]
				got, derr := dec.Decode(nil, stored[b.NewOfs:b.NewOfs+b.Len], len(plain), ci.CompressorMessage)
				Expect(derr).NotTo(HaveOccurred(), "block %d", i)
				Expect(got).To(Equal(plain), "block %d decodes with the read path's decoder", i)
				end = b.NewOfs + b.Len
			}
			Expect(end).To(BeEquivalentTo(len(stored)), "the blocks cover the stored bytes")

			settle(s)
			en, ok := c.Entry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue())
			Expect([]uint64{en.Meta.Size, en.Meta.AccountedSize}).To(Equal([]uint64{uint64(len(stored)), 10 << 20}),
				"the stored size, and the plaintext's accounted")
			Expect(driver.CachedBucketStatsForTest(s, rec).Size).To(BeEquivalentTo(10<<20), "the quota cache adds the accounted size")

			hints := tailHints(c, putPrefix)
			Expect(hints).NotTo(BeEmpty())
			Expect(hints).To(HaveEach(radosclient.AllocHintFlags(512)), "add_write_hint, rgw_putobj_processor.cc:146-155 at v20.2.4")
			Expect(afterWriteFull(c.LastWrite(testDataPool, "", headOID).Steps())).To(Equal(&radosclient.SetAllocHintStep{Flags: 512}),
				"_do_write_meta, rgw_rados.cc:3327-3335 at v20.2.4")

			st, err := s.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(readBack(ctx, s, st, 0, 10<<20)).To(Equal(body), "a GET decodes it")
			Expect(readBack(ctx, s, st, 4<<20-5, 10)).To(Equal(body[4<<20-5:4<<20+5]), "a range across two blocks")
			Expect(readBack(ctx, s, st, 9<<20, 7)).To(Equal(body[9<<20:9<<20+7]), "a range in the last block")
		},
		Entry("zlib, whose message is the default window's -15", compression.Zlib, new(int32(-15))),
		Entry("snappy, which sets no message", compression.Snappy, (*int32)(nil)),
		Entry("zstd, which sets no message", compression.Zstd, (*int32)(nil)),
		Entry("lz4, which sets no message", compression.LZ4, (*int32)(nil)),
	)

	DescribeTable("states the window zlib deflated with in the message, never a narrower one",
		func(ctx SpecContext, winsize string, message int32) {
			s = openPutStore(ctx, c, denc.Squid, map[string]string{"compressor_zlib_winsize": winsize}, mtime)
			body := hexNoise(1 << 20)
			_, err := put(ctx, body, compClass(compression.Zlib), op.PutParams{})
			Expect(err).NotTo(HaveOccurred())
			ci, ok := compressionInfo()
			Expect(ok).To(BeTrue())
			Expect(ci.CompressorMessage).To(Equal(&message))
			st, err := s.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(readBack(ctx, s, st, 0, 1<<20)).To(Equal(body))
		},
		Entry("raw deflate at -15, radosgw's own", "-15", int32(-15)),
		Entry("raw deflate at -9, where radosgw states -9", "-9", int32(-15)),
		Entry("the zlib wrapper at 8, where radosgw states 8", "8", int32(15)),
		Entry("the zlib wrapper at 15, radosgw's own", "15", int32(15)),
		Entry("gzip at 20, which deflateInit2 refuses and radosgw stores raw", "20", int32(31)),
		Entry("gzip at 31, radosgw's own", "31", int32(31)),
	)

	It("checks the declared length on the plaintext", func(ctx SpecContext) {
		_, err := put(ctx, hexNoise(6<<20), compClass(compression.Zstd), op.PutParams{Size: 10 << 20})
		Expect(err).To(MatchError(op.ErrRequestTimeout))
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "the tails written are removed")
	})
	It("checks the Content-MD5 on the plaintext", func(ctx SpecContext) {
		body := hexNoise(5 << 20)
		sum := md5.Sum(body)
		_, err := put(ctx, body, compClass(compression.Zstd), op.PutParams{ContentMD5: sum[:]})
		Expect(err).NotTo(HaveOccurred())
		_, ok := compressionInfo()
		Expect(ok).To(BeTrue())
	})
	It("checks rgw_max_put_size on the plaintext", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"rgw_max_put_size": "6291456"}, mtime)
		_, err := put(ctx, bytes.Repeat([]byte("a"), 8<<20), compClass(compression.Zstd), op.PutParams{})
		Expect(err).To(MatchError(op.ErrEntityTooLarge), "8 MiB of plaintext, though it stores in a few KiB")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})
	It("checks the quota on the plaintext", func(ctx SpecContext) {
		rec.Info.Quota = meta.Quota{MaxSize: 9 << 20, MaxObjects: -1, CheckOnRaw: true, Enabled: true}
		_, err := put(ctx, hexNoise(10<<20), compClass(compression.Zstd), op.PutParams{})
		Expect(err).To(MatchError(op.ErrQuotaExceeded), "10 MiB of plaintext, though it stores in about 5")
	})

	It("stores the object as it is, hinted incompressible, when its first block does not compress", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, map[string]string{"compressor_zlib_level": "10"}, mtime)
		body := hexNoise(6 << 20)
		res, err := put(ctx, body, compClass(compression.Zlib), op.PutParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(md5hex(body)))
		_, ok := compressionInfo()
		Expect(ok).To(BeFalse(), "deflateInit2 refuses level 10, and RGWPutObj_Compress::process stores the object uncompressed")
		Expect(storedStream(c, headOID, putPrefix)).To(Equal(body))
		Expect(tailHints(c, putPrefix)).To(HaveEach(radosclient.AllocHintFlags(512)), "set_compressed ran when the plugin loaded")
		Expect(afterWriteFull(c.LastWrite(testDataPool, "", headOID).Steps())).To(Equal(&radosclient.SetAllocHintStep{Flags: 512}))
		settle(s)
		en, _ := c.Entry(rookIndexPool, "", shard, "k")
		Expect([]uint64{en.Meta.Size, en.Meta.AccountedSize}).To(Equal([]uint64{6 << 20, 6 << 20}))
	})
	It("fails a later block that does not compress with UnknownError and leaves nothing behind", func(ctx SpecContext) {
		calls := &atomic.Int32{}
		s.SetEncoderForTest(func(codec string, o compression.EncoderOptions) (compression.Encoder, error) {
			enc, err := compression.NewEncoder(codec, o)
			return failingEncoder{enc: enc, calls: calls, n: 2}, err
		})
		_, err := put(ctx, hexNoise(10<<20), compClass(compression.Zstd), op.PutParams{})
		Expect(err).To(MatchError(op.ErrUnknown), "process's -EIO for the next part, rgw_compression.cc:54-60")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
	})

	DescribeTable("stores the object as it is, unhinted, for a type it has no encoder for, and logs the type once",
		func(ctx SpecContext, typ string) {
			logs := &bytes.Buffer{}
			DeferCleanup(driver.CaptureLog(logs))
			body := hexNoise(6 << 20)
			for range 2 {
				_, err := put(ctx, body, compClass(typ), op.PutParams{})
				Expect(err).NotTo(HaveOccurred())
			}
			_, ok := compressionInfo()
			Expect(ok).To(BeFalse())
			Expect(storedStream(c, headOID, putPrefix)).To(Equal(body))
			Expect(tailHints(c, putPrefix)).To(HaveEach(radosclient.AllocHintFlags(0)))
			Expect(afterWriteFull(c.LastWrite(testDataPool, "", headOID).Steps())).NotTo(BeAssignableToTypeOf(&radosclient.SetAllocHintStep{}))
			Expect(strings.Count(logs.String(), `"compression_type":"`+typ+`"`)).To(Equal(1), logs.String())
		},
		Entry("random, which radosgw's teuthology runs set", "random"),
		Entry("brotli, a plugin radosgw may load", "brotli"),
	)

	It("stores an empty body with no compression info, its head hinted as radosgw hints it", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, strings.NewReader(""), op.PutParams{Attrs: attrs, Tag: "t", StorageClass: compClass(compression.Zlib)})
		Expect(err).NotTo(HaveOccurred())
		_, ok := compressionInfo()
		Expect(ok).To(BeFalse(), "is_compressed is false until a block compresses")
		Expect(afterWriteFull(c.LastWrite(testDataPool, "", headOID).Steps())).To(Equal(&radosclient.SetAllocHintStep{Flags: 512}))
	})

	It("cuts its blocks by rgw_max_chunk_size, not by the chunk a pool's alignment makes", func(ctx SpecContext) {
		c.SetRequiredAlignment(testDataPool, 3<<20)
		_, err := put(ctx, hexNoise(10<<20), compClass(compression.Zstd), op.PutParams{})
		Expect(err).NotTo(HaveOccurred())
		ci, ok := compressionInfo()
		Expect(ok).To(BeTrue())
		Expect(ci.Blocks).To(HaveLen(3))
		Expect([]uint64{ci.Blocks[1].OldOfs, ci.Blocks[2].OldOfs}).To(Equal([]uint64{4 << 20, 8 << 20}), "get_data reads rgw_max_chunk_size, rgw_rest.cc:1076")
		Expect(c.Object(testDataPool, "", headOID).Data).To(HaveLen(3<<20), "the head chunk is the aligned one")
	})

	It("compresses an encrypted write only where the zonegroup supports compress-encrypted", func(ctx SpecContext) {
		Expect(s.CompressesForTest(ctx, compression.Zlib, false)).To(BeTrue())
		Expect(s.CompressesForTest(ctx, compression.Zlib, true)).To(BeFalse(), "rgw_op.cc:4576-4577 at v20.2.4")
		editRoot(c, meta.PeriodOID("period-ceph-objectstore", 1), meta.DecodePeriod, func(p *meta.Period) {
			zg := p.PeriodMap.ZoneGroups["zg-ceph-objectstore"]
			zg.EnabledFeatures = append(zg.EnabledFeatures, "compress-encrypted")
			p.PeriodMap.ZoneGroups["zg-ceph-objectstore"] = zg
		})
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		Expect(s.CompressesForTest(ctx, compression.Zlib, true)).To(BeTrue())
	})

	DescribeTable("lets go of its block buffers once the plaintext ends, before the drain and the head write",
		func(ctx SpecContext, declared int64) {
			body := hexNoise(6 << 20)
			r, held := s.StoredStageForTest(ctx, bytes.NewReader(body), declared, compression.Zstd)
			_, err := r.Read(make([]byte, 1))
			Expect(err).NotTo(HaveOccurred())
			Expect(held()).To(BeNumerically(">=", 4<<20), "a block of plaintext and its encoded form")
			n, err := io.Copy(io.Discard, r)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(BeNumerically(">", 0))
			Expect(held()).To(BeZero())
		},
		Entry("a body of its declared length, which ends on the probe past it", int64(6<<20)),
		Entry("a body of unknown length, which ends with its last block", int64(-1)),
	)

	It("stores a body into a class without a compression type as it is", func(ctx SpecContext) {
		body := hexNoise(6 << 20)
		_, err := put(ctx, body, "", op.PutParams{})
		Expect(err).NotTo(HaveOccurred())
		_, ok := compressionInfo()
		Expect(ok).To(BeFalse())
		Expect(tailHints(c, putPrefix)).To(HaveEach(radosclient.AllocHintFlags(0)))
	})

	It("takes no compression from a bucket whose placement has no name, as get_compression_type finds none", func(ctx SpecContext) {
		editRoot(c, meta.ZoneInfoOID(putZoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
			pi := z.PlacementPools["default-placement"]
			std := pi.StorageClasses[meta.StorageClassStandard]
			std.CompressionType = new(compression.Zlib)
			pi.StorageClasses[meta.StorageClassStandard] = std
			z.PlacementPools["default-placement"] = pi
		})
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		rec.Info.PlacementRule = meta.PlacementRule{}
		_, err := put(ctx, hexNoise(1<<20), "", op.PutParams{})
		Expect(err).NotTo(HaveOccurred())
		_, ok := compressionInfo()
		Expect(ok).To(BeFalse(), "rgw_zone.cc:356-365 looks the placement up by its empty name")
		rec.Info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
		_, err = put(ctx, hexNoise(1<<20), "", op.PutParams{})
		Expect(err).NotTo(HaveOccurred())
		_, ok = compressionInfo()
		Expect(ok).To(BeTrue(), "the same class under its name compresses")
	})
})

// check9 is the check string of the CRC catalog, whose CRC32 is 0xCBF43926,
// CRC32C 0xE3069283 and CRC-64/NVME 0xAE8B14860A799888.
var check9 = []byte("123456789")

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func be64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// check9Digests is each writable type's digest of check9, as a record stores
// it: a CRC big-endian, computed without rgw-go's cksum package.
func check9Digests() map[cksum.Type][]byte {
	s1, s256, s512 := sha1.Sum(check9), sha256.Sum256(check9), sha512.Sum512(check9)
	return map[cksum.Type][]byte{
		cksum.CRC32: be32(0xcbf43926), cksum.CRC32C: be32(0xe3069283), cksum.CRC64NVME: be64(0xae8b14860a799888),
		cksum.SHA1: s1[:], cksum.SHA256: s256[:], cksum.SHA512: s512[:],
	}
}

// cksumRecord hand-builds the user.rgw.cksum Cksum::encode writes:
// ENCODE_START(2, 1)'s struct_v, compat and u32 length, then the u16 type,
// the u16 digest size, the digest and the u16 flags.
func cksumRecord(t cksum.Type, digest []byte, flags uint16) []byte {
	body := binary.LittleEndian.AppendUint16(nil, uint16(t))
	body = binary.LittleEndian.AppendUint16(body, uint16(len(digest))) //nolint:gosec // a digest is at most 64 bytes
	body = binary.LittleEndian.AppendUint16(append(body, digest...), flags)
	return slices.Concat([]byte{2, 1}, binary.LittleEndian.AppendUint32(nil, uint32(len(body))), body) //nolint:gosec // as above
}

// eofReader is a body that records whether it has returned io.EOF.
type eofReader struct {
	r     io.Reader
	ended bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if errors.Is(err, io.EOF) {
		e.ended = true
	}
	return n, err
}

// suppliedArmor is a ChecksumRequest's Expected that supplies digest's armor,
// counting its calls into asked.
func suppliedArmor(digest []byte, asked *int) func() (string, bool) {
	return func() (string, bool) {
		*asked++
		return base64.StdEncoding.EncodeToString(digest), true
	}
}

// expectReadableChecksums asserts that every user.rgw.cksum a write op set on
// an object of the data pool, and every checksum a part record sent to the
// extra pool's meta objects carries, is a record a v20.2.4 radosgw reads:
// one cksum.DecodeCksum takes, of a type of at most 8, its index into
// radosgw's table. It reads the ops, not the objects: the PUT fixture's fake
// cluster is a Squid one, whose mp_upload_part_info_update stores the record
// as a Squid OSD re-encodes it, without the checksum.
func expectReadableChecksums(c *fakerados.Cluster) {
	GinkgoHelper()
	check := func(where string, b []byte) {
		d := denc.NewDecoder(b)
		got := cksum.DecodeCksum(d)
		Expect(d.Err()).NotTo(HaveOccurred(), where)
		Expect(got.Type).To(BeNumerically("<=", cksum.CRC64NVME), where)
	}
	for _, pool := range []string{testDataPool, extraPoolName} {
		for _, oid := range slices.Compact(slices.Sorted(slices.Values(c.WriteOrder(pool, "")))) {
			for _, w := range c.WritesTo(pool, "", oid) {
				for _, st := range w.Steps() {
					switch st := st.(type) {
					case *radosclient.SetXattrStep:
						if st.Name == meta.AttrCksum {
							check(oid, st.Value)
						}
					case *radosclient.ExecStep:
						if st.Method != "mp_upload_part_info_update" {
							continue
						}
						upd := rgwcls.DecodeMPUploadPartInfoUpdateOp(denc.NewDecoder(st.In))
						d := denc.NewDecoder(upd.Info)
						info := meta.DecodeUploadPartInfo(d)
						Expect(d.Err()).NotTo(HaveOccurred(), "%s %s", oid, upd.PartKey)
						if info.Cksum != nil {
							Expect(info.Cksum[0]).To(Equal(byte(1)), "%s %s: the optional's presence byte", oid, upd.PartKey)
							check(oid+" "+upd.PartKey, info.Cksum[1:])
						}
					}
				}
			}
		}
	}
}

var _ = Describe("PutObject's checksum", func() {
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
		mtime = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
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
	AfterEach(func() { expectReadableChecksums(c) })
	put := func(ctx context.Context, body io.Reader, size int, p op.PutParams) (*op.PutResult, error) {
		p.Attrs, p.Size, p.Tag = attrs, int64(size), "t"
		return s.PutObject(ctx, rec, key, body, p)
	}

	DescribeTable("stores the body's checksum as user.rgw.cksum, the record Cksum::encode writes, in the guarded head write",
		func(ctx SpecContext, rel denc.Release, t cksum.Type) {
			s = openPutStore(ctx, c, rel, nil, mtime)
			digest := check9Digests()[t]
			var asked int
			res, err := put(ctx, bytes.NewReader(check9), len(check9), op.PutParams{Checksum: &op.ChecksumRequest{Type: t, Expected: suppliedArmor(digest, &asked)}})
			Expect(err).NotTo(HaveOccurred())
			want := &cksum.Cksum{Type: t, Digest: digest}
			Expect(res.Checksum).To(Equal(want), "the result, flags 0 as finalize leaves them")
			Expect(asked).To(Equal(1), "the supplied value is asked for once")
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(1), "no write of its own")
			Expect(c.LastWrite(testDataPool, "", headOID).Steps()).To(ContainElement(
				&radosclient.SetXattrStep{Name: meta.AttrCksum, Value: cksumRecord(t, digest, 0)}))
			st, err := s.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Checksum).To(Equal(want), "the stat")
		},
		Entry("CRC32 on Squid", denc.Squid, cksum.CRC32),
		Entry("CRC32C on Tentacle", denc.Tentacle, cksum.CRC32C),
		Entry("CRC64NVME on Squid", denc.Squid, cksum.CRC64NVME),
		Entry("SHA1 on Tentacle", denc.Tentacle, cksum.SHA1),
		Entry("SHA256 on Squid", denc.Squid, cksum.SHA256),
		Entry("SHA512 on Tentacle", denc.Tentacle, cksum.SHA512),
	)

	It("stores the request's flags exactly as given", func(ctx SpecContext) {
		req := &op.ChecksumRequest{Type: cksum.CRC32C, Flags: cksum.FlagFullObject}
		res, err := put(ctx, bytes.NewReader(check9), len(check9), op.PutParams{Checksum: req})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Checksum.Flags).To(Equal(cksum.FlagFullObject), "no V2: finalize replaces the constructor's flags")
		Expect(c.Object(testDataPool, "", headOID).Xattrs).To(HaveKeyWithValue(meta.AttrCksum, cksumRecord(cksum.CRC32C, be32(0xe3069283), cksum.FlagFullObject)))
	})

	It("checksums a body of tails and a compressed body over the plaintext, asking for the value only once the body has ended", func(ctx SpecContext) {
		withCompressionClasses(c, compression.Zstd)
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		body := hexNoise(9 << 20)
		sum := sha256.Sum256(body)
		for _, class := range []string{"", compClass(compression.Zstd)} {
			r := &eofReader{r: bytes.NewReader(body)}
			var early bool
			req := &op.ChecksumRequest{Type: cksum.SHA256, Expected: func() (string, bool) {
				early = early || !r.ended
				return base64.StdEncoding.EncodeToString(sum[:]), true
			}}
			res, err := put(ctx, r, len(body), op.PutParams{StorageClass: class, Checksum: req})
			Expect(err).NotTo(HaveOccurred(), "class %q", class)
			Expect(early).To(BeFalse(), "class %q: asked before the body's io.EOF, when a trailer's value is not there yet", class)
			Expect(res.Checksum).To(Equal(&cksum.Cksum{Type: cksum.SHA256, Digest: sum[:]}), "class %q", class)
		}
	})

	It("checksums a CRC64NVME body read in small pieces as hash/crc64 does the body whole", func(ctx SpecContext) {
		body := hexNoise(1<<20 + 5)
		want := be64(crc64.Checksum(body, crc64.MakeTable(0x9a6c9329ac4bc9b5)))
		var asked int
		res, err := put(ctx, &cappedReader{r: bytes.NewReader(body), n: 1000}, len(body),
			op.PutParams{Checksum: &op.ChecksumRequest{Type: cksum.CRC64NVME, Expected: suppliedArmor(want, &asked)}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Checksum).To(Equal(&cksum.Cksum{Type: cksum.CRC64NVME, Digest: want}))
	})

	DescribeTable("stores the checksum unverified when the request supplies no value",
		func(ctx SpecContext, expected func() (string, bool)) {
			_, err := put(ctx, bytes.NewReader(check9), len(check9), op.PutParams{Checksum: &op.ChecksumRequest{Type: cksum.CRC32, Expected: expected}})
			Expect(err).NotTo(HaveOccurred())
			Expect(c.Object(testDataPool, "", headOID).Xattrs).To(HaveKeyWithValue(meta.AttrCksum, cksumRecord(cksum.CRC32, be32(0xcbf43926), 0)))
		},
		Entry("no Expected", nil),
		Entry("an Expected that answers it has no value", func() (string, bool) { return "not the checksum", false }),
	)

	It("refuses a body whose checksum is not the value supplied with BadDigest, writing no head and removing its tails", func(ctx SpecContext) {
		var asked int
		other := crc32.Checksum([]byte("other"), crc32.MakeTable(crc32.Castagnoli))
		_, err := put(ctx, bytes.NewReader(mibPattern(5)), 5<<20, op.PutParams{Checksum: &op.ChecksumRequest{Type: cksum.CRC32C, Expected: suppliedArmor(be32(other), &asked)}})
		Expect(err).To(MatchError(op.ErrBadDigest))
		Expect(err).To(MatchError(ContainSubstring("crc32c")), "the error names the checksum, not the Content-MD5")
		Expect(c.Writes(testDataPool, "", tailOID(putPrefix, 1))).To(Equal(2), "the tail was written, then removed")
		Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "no head was written")
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("compares the value supplied with the armor as a string, so another spelling of the digest is BadDigest", func(ctx SpecContext) {
		armor := base64.StdEncoding.EncodeToString(be32(0xcbf43926))
		unpadded := strings.TrimRight(armor, "=")
		_, err := put(ctx, bytes.NewReader(check9), len(check9), op.PutParams{Checksum: &op.ChecksumRequest{Type: cksum.CRC32, Expected: func() (string, bool) { return unpadded, true }}})
		Expect(err).To(MatchError(op.ErrBadDigest), "strcmp(%q, %q), rgw_cksum_pipe.h:202-211 at v20.2.4", unpadded, armor)
	})

	DescribeTable("checks what arrived before the checksum, never asking for its value",
		func(ctx SpecContext, body string, size int, p op.PutParams, quota bool, want error) {
			if quota {
				rec.Info.Quota = meta.Quota{MaxSize: 4, MaxObjects: -1, CheckOnRaw: true, Enabled: true}
			}
			var asked int
			p.Checksum = &op.ChecksumRequest{Type: cksum.CRC32, Expected: suppliedArmor([]byte("not the crc"), &asked)}
			_, err := put(ctx, strings.NewReader(body), size, p)
			Expect(err).To(MatchError(want))
			Expect(asked).To(BeZero(), "the checksum's value, which is asked for only when its check runs")
			Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty())
		},
		Entry("a body short of its length is RequestTimeout", "abc", 10, op.PutParams{}, false, op.ErrRequestTimeout),
		Entry("a body over the quota is QuotaExceeded", string(check9), len(check9), op.PutParams{}, true, op.ErrQuotaExceeded),
		Entry("a wrong Content-MD5 is its BadDigest, the checksum wrong too", string(check9), len(check9),
			op.PutParams{ContentMD5: md5sumOf("other")}, false, op.ErrBadDigest),
	)

	DescribeTable("refuses a type a v20.2.4 radosgw cannot read with InvalidArgument, before it reads the body or writes anything",
		func(ctx SpecContext, t cksum.Type) {
			var read atomic.Int64
			body := &countingReader{r: bytes.NewReader(mibPattern(5)), n: &read}
			_, err := put(ctx, body, 5<<20, op.PutParams{Checksum: &op.ChecksumRequest{Type: t}})
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(read.Load()).To(BeZero(), "the body")
			Expect(poolObjects(ctx, c, testDataPool, "")).To(BeEmpty(), "a tail or a head")
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "the index")
		},
		Entry("type 9, main's md5", cksum.Type(9)),
		Entry("type 10, main's xxhash64", cksum.Type(10)),
		Entry("type 11, main's xxhash128", cksum.Type(11)),
		Entry("type 65535", cksum.Type(65535)),
		Entry("xxhash3, which rgw-go does not compute", cksum.XXH3),
	)
})

var _ = Describe("the CRC64NVME batch", func() {
	DescribeTable("is no larger than the body, and none where hash/crc64 runs a byte at a time anyway",
		func(t cksum.Type, size int64, written, want int) {
			got, err := driver.ChecksumBatchForTest(&op.ChecksumRequest{Type: t}, size, make([]byte, written))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("none for a 9-byte body", cksum.CRC64NVME, int64(9), 9, 0),
		Entry("none for a 2047-byte body, all of it below the 2048 bytes crc64's slicing needs", cksum.CRC64NVME, int64(2047), 2047, 0),
		Entry("a 2048-byte body's own length", cksum.CRC64NVME, int64(2048), 1, 2048),
		Entry("a 100 KiB body's own length", cksum.CRC64NVME, int64(100<<10), 1, 100<<10),
		Entry("128 KiB for a 1 MiB body", cksum.CRC64NVME, int64(1<<20), 1, 128<<10),
		Entry("128 KiB for a body of unknown length, once it is written", cksum.CRC64NVME, int64(-1), 1, 128<<10),
		Entry("none for a body of unknown length never written", cksum.CRC64NVME, int64(-1), 0, 0),
		Entry("none for a CRC32C body", cksum.CRC32C, int64(1<<20), 1, 0),
	)
})

// cappedReader yields r in reads of at most n bytes, as a request body yields
// what each TLS record or socket read carries.
type cappedReader struct {
	r io.Reader
	n int
}

func (p *cappedReader) Read(b []byte) (int, error) { return p.r.Read(b[:min(len(b), p.n)]) }

// BenchmarkPutObjectChecksum measures a 4 MiB PutObject on fakerados, a
// head-only object, without a checksum and with each CRC, its body read in
// pieces of 1 KiB, of a TLS record's 16 KiB and of 128 KiB. Run it alone:
// go test -tags=ceph_preview -run '^$' -bench PutObjectChecksum ./internal/driver/
func BenchmarkPutObjectChecksum(b *testing.B) {
	RegisterTestingT(b)
	b.Cleanup(driver.CaptureLog(io.Discard))
	ctx := b.Context()
	c := newPutCluster()
	s := openPutStore(ctx, c, denc.Tentacle, nil, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	b.Cleanup(func() { Expect(s.Close()).To(Succeed()) })
	rec := testBucket(putBucketID, 11)
	seedShards(c, rec)
	seedRecordInstance(c, rec)
	attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
	body := hexNoise(4 << 20)
	for _, piece := range []int{1 << 10, 16 << 10, 128 << 10} {
		for _, t := range []cksum.Type{cksum.None, cksum.CRC32C, cksum.CRC64NVME} {
			b.Run(fmt.Sprintf("%s/%dKiB", t.Info().Name, piece>>10), func(b *testing.B) {
				var req *op.ChecksumRequest
				if t != cksum.None {
					req = &op.ChecksumRequest{Type: t}
				}
				p := op.PutParams{Attrs: attrs, Size: int64(len(body)), Tag: "t", Checksum: req}
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					// The cluster records every write op, the head's data
					// with it.
					c.ResetCounters()
					if _, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, &cappedReader{r: bytes.NewReader(body), n: piece}, p); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
