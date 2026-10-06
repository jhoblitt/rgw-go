package driver_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// mpETag is the object ETag RadosMultipartUpload::complete computes: the hex
// MD5 of the parts' binary MD5s, "-" and the part count.
func mpETag(etags ...string) string {
	GinkgoHelper()
	h := md5.New()
	for _, e := range etags {
		raw, err := hex.DecodeString(e)
		Expect(err).NotTo(HaveOccurred())
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(etags))
}

// execStep is st as the exec step it must be.
func execStep(st radosclient.Step) *radosclient.ExecStep {
	GinkgoHelper()
	x, ok := st.(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "%T is not an exec step", st)
	return x
}

// execMethods lists the class methods the exec steps of writes call, in
// order.
func execMethods(writes []fakerados.RecordedWrite) []string {
	var out []string
	for _, w := range writes {
		for _, st := range w.Steps() {
			if x, ok := st.(*radosclient.ExecStep); ok {
				out = append(out, x.Method)
			}
		}
	}
	return out
}

// indexCompletions decodes every bucket_complete_op of mod sent to shard.
func indexCompletions(c *fakerados.Cluster, shard string, mod rgwcls.ModifyOp) []rgwcls.CompleteOp {
	GinkgoHelper()
	var out []rgwcls.CompleteOp
	for _, w := range c.WritesTo(rookIndexPool, "", shard) {
		for _, st := range w.Steps() {
			x, ok := st.(*radosclient.ExecStep)
			if !ok || x.Method != "bucket_complete_op" {
				continue
			}
			op := rgwcls.DecodeCompleteOp(denc.NewDecoder(x.In))
			if op.Op == mod {
				out = append(out, op)
			}
		}
	}
	return out
}

// allGCEntries is every entry queued on the fixture's 32 gc shards.
func allGCEntries(c *fakerados.Cluster) []rgwcls.GCObjInfo {
	var out []rgwcls.GCObjInfo
	for i := range 32 {
		out = append(out, c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", i))...)
	}
	return out
}

// chainOIDs is the oids the entries' chains name.
func chainOIDs(entries []rgwcls.GCObjInfo) []string {
	var out []string
	for _, e := range entries {
		for _, o := range e.Chain {
			out = append(out, o.Key.Name)
		}
	}
	return out
}

// specClock is a cluster clock one spec owns and moves. The cluster reads it
// from the store's index completion goroutines too, so it is guarded.
type specClock struct {
	mu sync.Mutex
	t  time.Time
}

func (k *specClock) Now() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.t
}

func (k *specClock) Advance(d time.Duration) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.t = k.t.Add(d)
}

var _ = Describe("Complete", func() {
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		mtime   time.Time
		clock   *specClock
		shard   string
		headOID string
		before  op.Stats
		// stores are the spec's stores, whose index completions AfterEach
		// waits for, so that none outlives its spec.
		stores []*driver.Store
	)
	// openStore opens another store over the spec's cluster.
	openStore := func(ctx context.Context, rel denc.Release) *driver.Store {
		GinkgoHelper()
		st := openPutStore(ctx, c, rel, nil, mtime)
		stores = append(stores, st)
		return st
	}
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		clock = &specClock{t: mtime}
		c = newPutCluster()
		c.SetClock(clock.Now)
		stores = nil
		s = openStore(ctx, denc.Squid)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		shard = indexShardOID(rec, "k")
		headOID = putBucketID + "_k"
		rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}
		Expect(s.CheckQuota(ctx, rec, rec.Info.Owner, 0, 0)).To(Succeed())
	})
	AfterEach(func() {
		for _, st := range stores {
			settle(st)
		}
	})
	partAttrs := func() map[string][]byte {
		return map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(initiator, "Alice"))}
	}
	// uploadOn creates an upload of k through st and puts a part of each
	// size, part i+1 filled with one letter, returning the upload and the
	// parts' ETags.
	uploadOn := func(ctx context.Context, st *driver.Store, sizes ...int) (*op.Upload, []string) {
		GinkgoHelper()
		attrs := attrsWithACL(initiator)
		attrs[meta.AttrContentType] = []byte("text/plain\x00")
		up, err := st.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		var etags []string
		for i, n := range sizes {
			body := bytes.Repeat([]byte{byte('a' + i)}, n)
			r, err := st.PutPart(ctx, up, i+1, bytes.NewReader(body), op.PutParams{Attrs: partAttrs(), Size: int64(n)})
			Expect(err).NotTo(HaveOccurred())
			etags = append(etags, r.ETag)
		}
		settle(st)
		before = driver.CachedBucketStatsForTest(st, rec)
		return up, etags
	}
	uploadWithParts := func(ctx context.Context, sizes ...int) (*op.Upload, []string) {
		GinkgoHelper()
		return uploadOn(ctx, s, sizes...)
	}
	completeParts := func(etags []string) []op.CompletePart {
		out := make([]op.CompletePart, len(etags))
		for i, e := range etags {
			out[i] = op.CompletePart{Number: i + 1, ETag: `"` + e + `"`}
		}
		return out
	}
	partEntry := func(up *op.Upload, n int) string { return partIndexKey("k."+up.ID, n) }
	// partsIntact checks that each part of up still has its index entry, its
	// part info and its head object.
	partsIntact := func(up *op.Upload, n int) {
		GinkgoHelper()
		metaObj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
		Expect(metaObj).NotTo(BeNil(), "the meta object stays")
		for i := 1; i <= n; i++ {
			_, ok := c.Entry(rookIndexPool, "", shard, partEntry(up, i))
			Expect(ok).To(BeTrue(), "part %d's index entry", i)
			Expect(metaObj.Omap).To(HaveKey(meta.MultipartPartKey(uint32(i))), "part %d's info", i) //nolint:gosec // a small part number
			Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, i))).NotTo(BeNil(), "part %d's head", i)
		}
	}
	// failMetaDeleteWith fails the first write to the meta object of up after
	// the head of k exists, the removal, with errno.
	failMetaDeleteWith := func(up *op.Upload, errno syscall.Errno) {
		cl, head, oid := c, headOID, metaOID(rec, "k", up.ID)
		armed := false
		cl.BeforeWrite(extraPoolName, "", oid, func(*fakerados.Object) {
			if !armed && cl.Object(testDataPool, "", head) != nil {
				armed = true
				cl.FailNextWrite(extraPoolName, "", oid, errno)
			}
		})
	}

	It("assembles three parts into the object radosgw would write", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20, 3<<20)
		up.WriteTag = "tx-complete"
		mOID := metaOID(rec, "k", up.ID)
		metaObj := c.Object(extraPoolName, "", mOID)
		metaPGVer := slices.Clone(metaObj.Xattrs[meta.AttrPGVer])
		metaSize := uint64(len(metaObj.Data))
		c.ResetCounters()
		res, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		want := mpETag(etags...)
		Expect(res.ETag).To(Equal(want))
		Expect(res.Size).To(BeEquivalentTo(13 << 20))

		By("the lock, in one op, first; renewed before the head write")
		writes := c.WritesTo(extraPoolName, "", mOID)
		lockOp := writes[0].Steps()
		Expect(lockOp[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		Expect(lock.DecodeLockOp(denc.NewDecoder(execStep(lockOp[1]).In))).To(Equal(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second}))
		renew := writes[1].Steps()
		Expect(renew[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		Expect(lock.DecodeLockOp(denc.NewDecoder(execStep(renew[1]).In))).To(Equal(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second, Flags: lock.FlagMustRenew}))
		Expect(renew[2:]).To(Equal([]radosclient.Step{
			&radosclient.SetXattrStep{Name: "user.rgw.mp_completion_tag", Value: []byte("tx-complete\x00")},
			&radosclient.SetXattrStep{Name: "user.rgw.mp_completion_instance", Value: []byte("null")},
		}), "the completion record of ceph/ceph#72103, in the renewal's op")
		Expect(c.Reads(extraPoolName, "", mOID)).To(Equal(2), "get_obj_attrs, then one omap page")

		By("the head")
		head := c.Object(testDataPool, "", headOID)
		Expect(head).NotTo(BeNil())
		Expect(head.Data).To(BeEmpty(), "every byte is in the part stripes")
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-complete\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrTailTag, []byte("tx-complete\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrETag, []byte(want)))
		Expect(head.Xattrs).To(HaveKey(meta.AttrACL), "the meta object's attrs become the object's")
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrPGVer, metaPGVer), "pg_ver is copied from the meta object")
		Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCompression))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
		Expect(m.ObjSize).To(BeEquivalentTo(13 << 20))
		Expect(m.HeadSize).To(BeZero())
		Expect(m.Prefix).To(Equal("k." + up.ID))
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{
			0:        {StartPartNum: 1, StartOfs: 0, PartSize: 5 << 20, StripeMaxSize: 4 << 20},
			10 << 20: {StartPartNum: 3, StartOfs: 10 << 20, PartSize: 3 << 20, StripeMaxSize: 4 << 20},
		}))
		stripes, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(stripes).To(HaveLen(5), "5+5+3 MiB in 4 MiB stripes: 2+2+1")
		for _, st := range stripes {
			Expect(c.Object(testDataPool, "", st.OID())).NotTo(BeNil(), st.OID())
		}
		steps := c.LastWrite(testDataPool, "", headOID).Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))
		Expect(execMethods(c.WritesTo(testDataPool, "", headOID))).NotTo(ContainElement("obj_store_pg_ver"), "the attrs carried pg_ver")
		Expect(names(steps)).NotTo(ContainElement(meta.AttrStorageClass), "the default storage class names none")
		Expect(names(steps)).To(ContainElements(meta.AttrIDTag, meta.AttrTailTag, meta.AttrManifest, meta.AttrETag))

		By("the index: the head's entry, then the parts and the meta object retired with the meta object's removal")
		en, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue())
		Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{
			Category: rgwcls.CategoryMain, Size: 13 << 20, AccountedSize: 13 << 20, Mtime: res.Mtime, ETag: want,
			Owner: "alice", OwnerDisplayName: "Alice", ContentType: "text/plain",
		}))
		adds := indexCompletions(c, shard, rgwcls.OpAdd)
		Expect(adds).To(HaveLen(1))
		Expect(adds[0].RemoveObjs).To(BeEmpty(), "a refused head write must not take the parts' entries with its cancel")
		dels := indexCompletions(c, shard, rgwcls.OpDel)
		Expect(dels).To(HaveLen(1))
		Expect(dels[0].Key.Name).To(Equal(metaIndexKey("k", up.ID)))
		Expect(dels[0].LogOp).To(BeFalse(), "delete_object without FLAG_LOG_OP")
		Expect(dels[0].RemoveObjs).To(ConsistOf(
			rgwcls.ObjKey{Name: partEntry(up, 1)}, rgwcls.ObjKey{Name: partEntry(up, 2)}, rgwcls.ObjKey{Name: partEntry(up, 3)},
		))
		for n := 1; n <= 3; n++ {
			_, found := c.Entry(rookIndexPool, "", shard, partEntry(up, n))
			Expect(found).To(BeFalse(), "part %d retired", n)
		}
		_, ok = c.Entry(rookIndexPool, "", shard, metaIndexKey("k", up.ID))
		Expect(ok).To(BeFalse(), "the meta entry went with complete_del")
		hdr := c.Header(rookIndexPool, "", shard)
		Expect(hdr.Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(1))
		Expect(hdr.Stats[rgwcls.CategoryMain].TotalSize).To(BeEquivalentTo(13 << 20))
		Expect(hdr.Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeZero())

		By("the meta object's removal behind cls_version_check, with the lock")
		Expect(c.Object(extraPoolName, "", mOID)).To(BeNil())
		del := c.LastWrite(extraPoolName, "", mOID)
		Expect(del.Flags()).To(Equal(radosclient.OpFlagFullTry))
		ds := del.Steps()
		Expect(execStep(ds[0]).Method).To(Equal("obj_remove"))
		check := execStep(ds[1])
		Expect([]string{check.Class, check.Method}).To(Equal([]string{"version", "check_conds"}))
		chk := version.DecodeCheckOp(denc.NewDecoder(check.In))
		Expect(chk.Objv.Ver).To(BeEquivalentTo(4), "the first part registration's inc creates the version at 1 and increments it, the next two increment it")
		Expect(execMethods(writes)).NotTo(ContainElement("unlock"), "the lock went with the object")
		Expect(allGCEntries(c)).To(BeEmpty(), "no part history to collect")

		now := driver.CachedBucketStatsForTest(s, rec)
		Expect([]int64{int64(now.NumObjects) - int64(before.NumObjects), int64(now.Size) - int64(before.Size)}).To(Equal([]int64{0, -int64(metaSize)}), //nolint:gosec // spec sizes are small
			"+1 object for the head, no bytes (completeMultipart); -1 object and the meta object's size for its removal")
	})

	It("accepts the parts in any order and keeps a repeated number's last entry", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
		parts := []op.CompletePart{{Number: 2, ETag: etags[1]}, {Number: 1, ETag: `"bogus"`}, {Number: 1, ETag: etags[0]}}
		_, err := s.Complete(ctx, up, parts)
		Expect(err).NotTo(HaveOccurred(), "std::map<int, string>: sorted, last duplicate wins (rgw_multi.cc:44-53)")
	})

	DescribeTable("refuses what radosgw refuses, leaving the upload whole and unlocked",
		func(ctx SpecContext, mutate func(ctx context.Context, up *op.Upload, parts []op.CompletePart) []op.CompletePart, want error) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20, 1<<20)
			parts := mutate(ctx, up, completeParts(etags))
			settle(s)
			c.ResetCounters()
			_, err := s.Complete(ctx, up, parts)
			Expect(err).To(MatchError(want))
			settle(s)
			Expect(c.Object(testDataPool, "", headOID)).To(BeNil(), "nothing written")
			mOID := metaOID(rec, "k", up.ID)
			Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(Equal([]string{"lock", "unlock"}), "RGWCompleteMultipart::complete releases the lock")
			Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(BeEmpty())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "no index change")
			Expect(allGCEntries(c)).To(BeEmpty(), "nothing queued for the GC")
		},
		Entry("a wrong etag", func(_ context.Context, _ *op.Upload, p []op.CompletePart) []op.CompletePart {
			p[1].ETag = `"` + md5hex("x") + `"`
			return p
		}, op.ErrInvalidPart),
		Entry("a missing part", func(_ context.Context, _ *op.Upload, p []op.CompletePart) []op.CompletePart { return p[:2] }, op.ErrInvalidPart),
		Entry("a part that was never uploaded", func(_ context.Context, _ *op.Upload, p []op.CompletePart) []op.CompletePart {
			p[2].Number = 7
			return p
		}, op.ErrInvalidPart),
		Entry("a small part before the last", func(ctx context.Context, up *op.Upload, p []op.CompletePart) []op.CompletePart {
			r, err := s.PutPart(ctx, up, 4, strings.NewReader("tiny"), op.PutParams{Attrs: partAttrs(), Size: 4})
			Expect(err).NotTo(HaveOccurred())
			return append(p, op.CompletePart{Number: 4, ETag: r.ETag})
		}, op.ErrEntityTooSmall),
		Entry("a part whose head object is gone", func(_ context.Context, up *op.Upload, p []op.CompletePart) []op.CompletePart {
			c.Remove(testDataPool, "", partHeadOID("k."+up.ID, 2))
			return p
		}, op.ErrInvalidPart),
	)

	It("GCs a re-uploaded part's earlier objects under the upload id after the head write, and retires both entries", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
		s.SetRandForTest(fixedRand(reprefix))
		second := bytes.Repeat([]byte("b"), 5<<20)
		r, err := s.PutPart(ctx, up, 1, bytes.NewReader(second), op.PutParams{Attrs: partAttrs(), Size: int64(len(second))})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		etags[0] = r.ETag
		_, err = s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		for _, name := range []string{partEntry(up, 1), partIndexKey("k."+reprefix, 1), partEntry(up, 2)} {
			_, ok := c.Entry(rookIndexPool, "", shard, name)
			Expect(ok).To(BeFalse(), name)
		}
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal(up.ID), "cleanup_part_history tags the chain with the upload id")
		Expect(chainOIDs(entries)).To(ConsistOf(partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1)), "the old part's head and stripe")
		m := meta.DecodeManifest(denc.NewDecoder(c.Object(testDataPool, "", headOID).Xattrs[meta.AttrManifest]))
		Expect(m.Prefix).To(Equal("k."+reprefix), "the first part's manifest is adopted whole")
		Expect(m.Rules[5<<20].OverridePrefix).To(Equal("k."+up.ID), "part 2's rule names the upload's prefix")
	})

	It("answers 500 'already in progress' when another client holds the lock, and touches nothing", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20)
		mOID := metaOID(rec, "k", up.ID)
		p, err := c.Pool(ctx, extraPoolName, "")
		Expect(err).NotTo(HaveOccurred())
		w := radosclient.NewWriteOp()
		lock.LockExisting(w, "RGWCompleteMultipart", "", "", 10*time.Minute, denc.Squid)
		_, err = p.Write(ctx, mOID, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		obj := c.Object(extraPoolName, "", mOID)
		info := lock.DecodeInfo(denc.NewDecoder(obj.Xattrs["lock.RGWCompleteMultipart"]))
		for id, li := range info.Lockers {
			delete(info.Lockers, id)
			id.Locker.Num = 999
			info.Lockers[id] = li
		}
		obj.Xattrs["lock.RGWCompleteMultipart"] = encode(info)
		_, err = s.Complete(ctx, up, completeParts(etags))
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(op.AsError(err).Message).To(Equal("This multipart completion is already in progress"))
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
		Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(HaveLen(1), "the other client's lock stands")
		partsIntact(up, 1)
	})

	It("answers 500 when this gateway's earlier attempt still holds the lock", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20)
		p, err := c.Pool(ctx, extraPoolName, "")
		Expect(err).NotTo(HaveOccurred())
		w := radosclient.NewWriteOp()
		lock.LockExisting(w, "RGWCompleteMultipart", "", "", 10*time.Minute, denc.Squid)
		_, err = p.Write(ctx, metaOID(rec, "k", up.ID), w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		_, err = s.Complete(ctx, up, completeParts(etags))
		Expect(err).To(MatchError(op.ErrInternalError), "lock_obj's EEXIST")
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
	})

	It("fails closed when the lock lapsed before the head write", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
		lapsed := false
		k := clock
		c.BeforeRead(testDataPool, "", headOID, func() {
			if !lapsed {
				lapsed = true
				k.Advance(11 * time.Minute)
			}
		})
		_, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(op.AsError(err).Message).To(Equal("This multipart completion is already in progress"))
		Expect(lapsed).To(BeTrue())
		Expect(c.Object(testDataPool, "", headOID)).To(BeNil(), "no head without the lock")
		settle(s)
		partsIntact(up, 2)
		Expect(allGCEntries(c)).To(BeEmpty())
	})

	It("answers a re-sent completion of a finished upload as radosgw does on each release, and 500 for an unknown id", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
		first, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		c.ResetCounters()
		again, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred(), "check_previously_completed: the etags match")
		Expect(again.ETag).To(BeEmpty(), "Squid returns before etag is assigned (rgw_op.cc:6438-6441, :6533 at v19.2.6)")
		Expect([]any{again.Size, again.Mtime}).To(Equal([]any{first.Size, first.Mtime}))
		Expect(c.Writes(testDataPool, "", headOID)).To(BeZero())
		s2 := openStore(ctx, denc.Tentacle)
		again, err = s2.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		Expect(again.ETag).To(Equal(first.ETag), "Tentacle sets etag = oetag (rgw_op.cc:7472 at v20.2.4)")
		_, err = s.Complete(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key}, completeParts(etags[:1]))
		Expect(err).To(MatchError(op.ErrInternalError), "a lock on a missing object is ENOENT, and the etag does not match")
	})

	It("retries the meta object's removal after a racing part, and GCs the orphan but not the object's parts", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
		mOID := metaOID(rec, "k", up.ID)
		raced := false
		cl, st, head := c, s, headOID
		cl.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
			if !raced && cl.Object(testDataPool, "", head) != nil {
				raced = true
				_, err := st.PutPart(ctx, up, 3, bytes.NewReader(bytes.Repeat([]byte("z"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
				Expect(err).NotTo(HaveOccurred())
			}
		})
		c.ResetCounters()
		res, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(raced).To(BeTrue())
		Expect(res.Size).To(BeEquivalentTo(10<<20), "part 3 is not part of the object")
		Expect(c.Object(extraPoolName, "", mOID)).To(BeNil(), "the second removal succeeded")
		Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(HaveExactElements(
			"lock", "lock", "mp_upload_part_info_update", "inc", "obj_remove", "check_conds", "obj_remove", "check_conds"), "the lock, its renewal, part 3, two removals")
		for n := 1; n <= 3; n++ {
			_, ok := c.Entry(rookIndexPool, "", shard, partEntry(up, n))
			Expect(ok).To(BeFalse(), "part %d", n)
		}
		Expect(chainOIDs(allGCEntries(c))).To(ConsistOf(partHeadOID("k."+up.ID, 3), partShadowOID("k."+up.ID, 3, 1)),
			"cleanup_orphaned_parts collects part 3 alone: the completed parts' prefixes were processed")
		for _, oid := range []string{partHeadOID("k."+up.ID, 1), partShadowOID("k."+up.ID, 1, 1), partHeadOID("k."+up.ID, 2), partShadowOID("k."+up.ID, 2, 1)} {
			Expect(c.Object(testDataPool, "", oid)).NotTo(BeNil(), oid)
		}
	})

	Describe("conditions and refused head writes", func() {
		It("refuses If-None-Match: * over an existing key before writing, leaving every part and its history in place", func(ctx SpecContext) {
			_, err := s.PutObject(ctx, rec, key, strings.NewReader("old"), op.PutParams{Attrs: attrsWithACL(alice), Size: 3, Tag: "t"})
			Expect(err).NotTo(HaveOccurred())
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			s.SetRandForTest(fixedRand(reprefix))
			r, err := s.PutPart(ctx, up, 2, bytes.NewReader(bytes.Repeat([]byte("y"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
			Expect(err).NotTo(HaveOccurred())
			etags[1] = r.ETag
			settle(s)
			up.IfNoneMatch = "*"
			_, err = s.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			settle(s)
			Expect(c.Object(testDataPool, "", headOID).Data).To(Equal([]byte("old")))
			partsIntact(up, 1)
			_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey("k."+reprefix, 2))
			Expect(ok).To(BeTrue(), "the re-uploaded part's entry")
			_, ok = c.Entry(rookIndexPool, "", shard, partEntry(up, 2))
			Expect(ok).To(BeTrue(), "the replaced part's entry, which a completion retires")
			Expect(allGCEntries(c)).To(BeEmpty(), "the replaced part is queued only once a head is written, where radosgw queues it first")
			Expect(c.Locks(extraPoolName, "", metaOID(rec, "k", up.ID), "RGWCompleteMultipart")).To(BeEmpty())
			up.IfNoneMatch = ""
			_, err = s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred(), "the upload can still be completed")
		})
		It("keeps every part's index entry when If-None-Match: * loses the race to create the key (tracker #80907)", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			raced := false
			cl, head := c, headOID
			cl.BeforeWrite(testDataPool, "", head, func(o *fakerados.Object) {
				if !raced && o == nil {
					raced = true
					cl.Put(testDataPool, "", head, []byte("racer"))
				}
			})
			up.IfNoneMatch = "*"
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			settle(s)
			Expect(raced).To(BeTrue())
			cancels := indexCompletions(c, shard, rgwcls.OpCancel)
			Expect(cancels).To(HaveLen(1))
			Expect(cancels[0].RemoveObjs).To(BeEmpty(), "radosgw's done_cancel sends remove_objs (rgw_rados.cc:3374 at v19.2.6)")
			partsIntact(up, 2)
			Expect(allGCEntries(c)).To(BeEmpty())
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID)).Xattrs).NotTo(HaveKey("user.rgw.mp_completion_tag"),
				"a head write that certainly failed takes its record back")
			up.IfNoneMatch = ""
			_, err = s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred(), "so the upload can still be completed")
		})
		It("honors If-None-Match: * on Tentacle", func(ctx SpecContext) {
			s2 := openStore(ctx, denc.Tentacle)
			_, err := s2.PutObject(ctx, rec, key, strings.NewReader("old"), op.PutParams{Attrs: attrsWithACL(alice), Size: 3, Tag: "t"})
			Expect(err).NotTo(HaveOccurred())
			up, etags := uploadOn(ctx, s2, 5<<20)
			up.IfNoneMatch = "*"
			_, err = s2.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrPreconditionFailed))
			Expect(c.Object(testDataPool, "", headOID).Data).To(Equal([]byte("old")))
		})
	})

	Describe("crash safety", func() {
		// leaveMeta completes up with its meta object's removal failing, as a
		// gateway that stops after the head write or a removal that keeps
		// failing leaves it.
		leaveMeta := func(ctx context.Context, up *op.Upload, etags []string) {
			GinkgoHelper()
			failMetaDeleteWith(up, syscall.EIO)
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			mOID := metaOID(rec, "k", up.ID)
			Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil(), "the failed removal left the meta object")
			c.BeforeWrite(extraPoolName, "", mOID, nil)
		}
		It("keeps the completion record after a head write that timed out, and refuses the retry, retiring nothing", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			up.WriteTag = "tx-first"
			c.FailNextWrite(testDataPool, "", headOID, syscall.ETIMEDOUT)
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			partsIntact(up, 2)
			Expect(allGCEntries(c)).To(BeEmpty())
			mOID := metaOID(rec, "k", up.ID)
			Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(BeEmpty())
			Expect(c.Object(extraPoolName, "", mOID).Xattrs).To(HaveKeyWithValue("user.rgw.mp_completion_tag", []byte("tx-first\x00")),
				"the head may yet land, so the record stays (ceph/ceph#72103)")
			_, err = s.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrNoSuchUpload), "no head carries the recorded tag")
			Expect(c.Object(testDataPool, "", headOID)).To(BeNil())
			partsIntact(up, 2)
		})
		It("refuses a retry after a crash between the completion record and the head write", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			mOID := metaOID(rec, "k", up.ID)
			obj := c.Object(extraPoolName, "", mOID)
			obj.Xattrs["user.rgw.mp_completion_tag"] = []byte("tx-crashed\x00")
			obj.Xattrs["user.rgw.mp_completion_instance"] = []byte("null")
			c.ResetCounters()
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrNoSuchUpload), "upstream's trade-off: the upload must be aborted")
			Expect(c.Writes(testDataPool, "", headOID)).To(BeZero())
			Expect(execMethods(c.WritesTo(extraPoolName, "", mOID))).To(Equal([]string{"lock", "unlock"}))
			partsIntact(up, 2)
		})
		DescribeTable("refuses a retry once the completed object was replaced, leaving its queued parts unnamed (tracker #80896)",
			func(ctx SpecContext, replace func(ctx context.Context)) {
				up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
				up.WriteTag = "tx-first"
				leaveMeta(ctx, up, etags)
				replace(ctx)
				settle(s)
				queued := chainOIDs(allGCEntries(c))
				Expect(queued).To(ContainElements(partHeadOID("k."+up.ID, 1), partHeadOID("k."+up.ID, 2)), "the replacement queued the parts")
				headWrites := c.Writes(testDataPool, "", headOID)
				up.WriteTag = "tx-retry"
				_, err := s.Complete(ctx, up, completeParts(etags))
				Expect(err).To(MatchError(op.ErrNoSuchUpload))
				Expect(c.Writes(testDataPool, "", headOID)).To(Equal(headWrites), "no head over the queued parts")
				Expect(c.Locks(extraPoolName, "", metaOID(rec, "k", up.ID), "RGWCompleteMultipart")).To(BeEmpty())
			},
			Entry("by an overwrite", func(ctx context.Context) {
				_, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrsWithACL(alice), Size: 3, Tag: "t"})
				Expect(err).NotTo(HaveOccurred())
			}),
			Entry("by a delete", func(ctx context.Context) {
				Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
			}),
			Entry("by another upload of the same bytes, whose head has the same ETag and another tag", func(ctx context.Context) {
				s.SetRandForTest(fixedRand(secondPrefix))
				other, etags := uploadWithParts(ctx, 5<<20, 5<<20)
				other.WriteTag = "tx-other"
				_, err := s.Complete(ctx, other, completeParts(etags))
				Expect(err).NotTo(HaveOccurred())
			}),
		)
		It("finishes an earlier completion that a floor radosgw left without a record", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, etags)
			obj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			delete(obj.Xattrs, "user.rgw.mp_completion_tag")
			delete(obj.Xattrs, "user.rgw.mp_completion_instance")
			headWrites := c.Writes(testDataPool, "", headOID)
			res, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.ETag).To(Equal(mpETag(etags...)))
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(headWrites))
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil())
		})
		It("leaves the parts recoverable when the meta object's removal times out after the head write", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			up.WriteTag = "tx-first"
			failMetaDeleteWith(up, syscall.ETIMEDOUT)
			res, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred(), "radosgw logs the removal's failure and answers the completion")
			settle(s)
			Expect(res.ETag).To(Equal(mpETag(etags...)))
			partsIntact(up, 2)
			Expect(allGCEntries(c)).To(BeEmpty())
			Expect(c.Locks(extraPoolName, "", metaOID(rec, "k", up.ID), "RGWCompleteMultipart")).To(BeEmpty(), "unlocked")
			c.BeforeWrite(extraPoolName, "", metaOID(rec, "k", up.ID), nil)

			headWrites := c.Writes(testDataPool, "", headOID)
			up.WriteTag = "tx-retry"
			again, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(again.ETag).To(Equal(res.ETag))
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(headWrites), "the completed head is not written again (tracker #80896)")
			Expect(c.Object(testDataPool, "", headOID).Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-first\x00")))
			Expect(allGCEntries(c)).To(BeEmpty(), "nothing of the object goes to the GC")
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil(), "the retry finished the removal")
			for n := 1; n <= 2; n++ {
				_, ok := c.Entry(rookIndexPool, "", shard, partEntry(up, n))
				Expect(ok).To(BeFalse(), "part %d", n)
				Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, n))).NotTo(BeNil(), "part %d's data", n)
			}
		})
		It("does not rewrite or GC a completed object whose meta object a failed removal left (tracker #80896)", func(ctx SpecContext) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			up.WriteTag = "tx-first"
			failMetaDeleteWith(up, syscall.EIO)
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			mOID := metaOID(rec, "k", up.ID)
			Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil(), "the failed removal left the meta object")
			c.BeforeWrite(extraPoolName, "", mOID, nil)

			By("a later part makes the retry's parts differ from the object's")
			r, err := s.PutPart(ctx, up, 3, strings.NewReader("tail"), op.PutParams{Attrs: partAttrs(), Size: 4})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			headWrites := c.Writes(testDataPool, "", headOID)
			_, err = s.Complete(ctx, up, completeParts(append(slices.Clone(etags), r.ETag)))
			Expect(err).To(MatchError(op.ErrNoSuchUpload), "the upload's parts already back the object")
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(headWrites))
			Expect(allGCEntries(c)).To(BeEmpty())
			Expect(c.Locks(extraPoolName, "", mOID, "RGWCompleteMultipart")).To(BeEmpty())
			for n := 1; n <= 2; n++ {
				Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, n))).NotTo(BeNil(), "part %d's data", n)
			}
		})
	})

	DescribeTable("answers 501 in a bucket whose writes take a versioned path, before touching the store",
		func(ctx SpecContext, flags uint32) {
			up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
			rec.Info.Flags |= flags
			settle(s)
			c.ResetCounters()
			_, err := s.Complete(ctx, up, completeParts(etags))
			Expect(err).To(MatchError(op.ErrNotImplemented))
			Expect(c.Writes(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeZero(), "no lock taken")
			Expect(c.Reads(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeZero())
			Expect(c.Object(testDataPool, "", headOID)).To(BeNil(), "no plain head beside an olh")
		},
		Entry("versioning enabled, where radosgw writes a new instance (rgw_op.cc:6459-6466 at v19.2.6)", uint32(meta.BucketVersioned)),
		Entry("versioning suspended, where radosgw writes the null version through the olh", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
		Entry("object lock, whose buckets are versioned", uint32(meta.BucketObjLockEnabled)),
	)

	It("answers success to a completion that loses its head write's race, and leaks its parts' current objects as radosgw does", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("old"), op.PutParams{Attrs: attrsWithACL(alice), Size: 3, Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		up, etags := uploadWithParts(ctx, 5<<20, 5<<20)
		writes := 0
		c.BeforeWrite(testDataPool, "", headOID, func(o *fakerados.Object) {
			writes++
			if writes == 2 { // the guarded pass, after the exclusive create met the head
				o.Xattrs[meta.AttrIDTag] = []byte("racer\x00")
			}
		})
		res, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred(), "done_cancel answers a lost race without conditions as success (rgw_rados.cc:3388-3391 at v19.2.6)")
		settle(s)
		Expect(writes).To(Equal(2))
		Expect(res.ETag).To(Equal(mpETag(etags...)))
		Expect([]any{res.Mtime, res.Epoch}).To(Equal([]any{mtime, uint64(0)}), "the time the head would have carried; no version")
		Expect(c.Object(testDataPool, "", headOID).Data).To(Equal([]byte("old")), "the racer's object stands")
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil(), "the upload is gone")
		for n := 1; n <= 2; n++ {
			Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, n))).NotTo(BeNil(), "part %d's data leaks", n)
			_, ok := c.Entry(rookIndexPool, "", shard, partEntry(up, n))
			Expect(ok).To(BeFalse(), "part %d's entry went with the upload", n)
		}
		Expect(allGCEntries(c)).To(BeEmpty(), "a racing completion of the same upload may name the parts' current objects, so none goes to the GC; only earlier uploads of a part would, and none was uploaded again here")
	})

	It("merges radosgw-written compressed parts' block maps and refuses a codec change", func(ctx SpecContext) {
		attrs := attrsWithACL(initiator)
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		target := meta.Obj{Bucket: rec.Info.Bucket, Key: key}
		prefix := "k." + up.ID
		mObj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
		seed := func(n uint32, codec string) string {
			m := meta.NewPartManifest(target, rec.Info.PlacementRule, meta.PlacementRule{Name: "default-placement"}, prefix, n, 4<<20)
			m.SetObjSize(3 << 20)
			etag := md5hex(codec + strconv.Itoa(int(n)))
			info := meta.UploadPartInfo{
				Num: n, Size: 3 << 20, AccountedSize: 5 << 20, ETag: etag, Modified: mtime, Manifest: m,
				Compression: meta.CompressionInfo{Type: codec, OrigSize: 5 << 20, Blocks: []meta.CompressionBlock{{OldOfs: 0, NewOfs: 0, Len: 3 << 20}}},
			}
			mObj.Omap[meta.MultipartPartKey(n)] = encode(info)
			c.Put(testDataPool, "", partHeadOID(prefix, int(n)), bytes.Repeat([]byte("c"), 3<<20))
			return etag
		}
		e1, e2 := seed(1, "zlib"), seed(2, "zlib")
		res, err := s.Complete(ctx, up, completeParts([]string{e1, e2}))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Size).To(BeEquivalentTo(6<<20), "the stored sizes")
		head := c.Object(testDataPool, "", headOID)
		ci := meta.DecodeCompressionInfo(denc.NewDecoder(head.Xattrs[meta.AttrCompression]))
		Expect(ci.Type).To(Equal("zlib"))
		Expect(ci.OrigSize).To(BeEquivalentTo(10 << 20))
		Expect(ci.Blocks).To(Equal([]meta.CompressionBlock{{OldOfs: 0, NewOfs: 0, Len: 3 << 20}, {OldOfs: 5 << 20, NewOfs: 3 << 20, Len: 3 << 20}}),
			"rgw_sal_rados.cc:3545-3566 at v19.2.6")
		settle(s)
		en, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue())
		Expect([]uint64{en.Meta.Size, en.Meta.AccountedSize}).To(Equal([]uint64{6 << 20, 10 << 20}))

		By("a codec change between parts")
		s.SetRandForTest(fixedRand(secondPrefix))
		up, err = s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		prefix = "k." + up.ID
		mObj = c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
		f1, f2 := seed(1, "zlib"), seed(2, "snappy")
		headWrites := c.Writes(testDataPool, "", headOID)
		_, err = s.Complete(ctx, up, completeParts([]string{f1, f2}))
		Expect(err).To(MatchError(op.ErrInvalidPart), "rgw_sal_rados.cc:3531-3543 at v19.2.6")
		Expect(c.Writes(testDataPool, "", headOID)).To(Equal(headWrites))
		Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).NotTo(BeNil())
	})
})
