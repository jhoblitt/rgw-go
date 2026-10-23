package driver_test

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("a versioned CompleteMultipartUpload", func() {
	var (
		c     *fakerados.Cluster
		ac    *aroundCluster
		s     *driver.Store
		rec   *op.BucketRecord
		clock atomic.Int64
		shard string
		attrs map[string][]byte
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		ac = &aroundCluster{Cluster: c}
		r := denc.Squid
		var err error
		s, err = driver.Open(ctx, ac, conf(nil), driver.Options{Release: &r})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		s.SetReshardWaitForTest(0)
		driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
		s.SetRandForTest(fixedRand(putPrefix))
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
		attrs = map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
	})
	AfterEach(func() {
		ac.set(nil)
		settle(s)
	})

	mustPut := func(ctx context.Context, instance, body string) {
		GinkgoHelper()
		_, err := s.PutObject(ctx, rec, version(instance), strings.NewReader(body), op.PutParams{Attrs: attrs, Size: int64(len(body)), Tag: "tag-" + instance})
		Expect(err).NotTo(HaveOccurred(), "the put of version %q", instance)
		settle(s)
	}
	suspend := func() { rec.Info.Flags |= meta.BucketVersionsSuspended }
	// upload creates an upload of k under the random strings of prefix and
	// puts a part of each size, part i+1 filled with one letter.
	upload := func(ctx context.Context, prefix string, sizes ...int) (*op.Upload, []string) {
		GinkgoHelper()
		s.SetRandForTest(fixedRand(prefix))
		up, err := s.CreateUpload(ctx, rec, version(""), op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
		Expect(err).NotTo(HaveOccurred())
		var etags []string
		for i, n := range sizes {
			body := bytes.Repeat([]byte{byte('a' + i)}, n)
			r, err := s.PutPart(ctx, up, i+1, bytes.NewReader(body), op.PutParams{Attrs: map[string][]byte{meta.AttrACL: attrs[meta.AttrACL]}, Size: int64(n)})
			Expect(err).NotTo(HaveOccurred())
			etags = append(etags, r.ETag)
		}
		settle(s)
		return up, etags
	}
	// complete completes up into k's version instance, as the op hands the
	// driver the instance it chose.
	complete := func(ctx context.Context, up *op.Upload, instance string, etags []string) (*op.PutResult, error) {
		u := *up
		u.Key = version(instance)
		parts := make([]op.CompletePart, len(etags))
		for i, e := range etags {
			parts[i] = op.CompletePart{Number: i + 1, ETag: `"` + e + `"`}
		}
		return s.Complete(ctx, &u, parts)
	}
	mOID := func(up *op.Upload) string { return metaOID(rec, "k", up.ID) }
	// failMetaDelete fails the first removal of up's meta object once the
	// head of version instance carries up's write tag.
	failMetaDelete := func(up *op.Upload, instance string) {
		cl, head, oid, tag := c, instanceHeadOID(version(instance)), mOID(up), up.WriteTag+"\x00"
		armed := false
		cl.BeforeWrite(extraPoolName, "", oid, func(*fakerados.Object) {
			if h := cl.Object(testDataPool, "", head); !armed && h != nil && string(h.Xattrs[meta.AttrIDTag]) == tag {
				armed = true
				cl.FailNextWrite(extraPoolName, "", oid, syscall.EIO)
			}
		})
	}
	record := func(up *op.Upload) []string {
		GinkgoHelper()
		o := c.Object(extraPoolName, "", mOID(up))
		Expect(o).NotTo(BeNil(), "the meta object")
		return []string{string(o.Xattrs["user.rgw.mp_completion_tag"]), string(o.Xattrs["user.rgw.mp_completion_instance"])}
	}
	head := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", instanceHeadOID(version(instance)))
	}
	target := func() meta.ObjKey {
		GinkgoHelper()
		o := c.Object(testDataPool, "", olhHeadOID(version("")))
		Expect(o).NotTo(BeNil(), "the OLH head")
		d := denc.NewDecoder(o.Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info.Target.Key
	}
	listed := func() []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", shard, "k") {
			out = append(out, en.Key.Instance)
		}
		return out
	}
	links := func() int {
		n := 0
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			if callsMethod(w.Steps(), "bucket_link_olh") {
				n++
			}
		}
		return n
	}
	partsIntact := func(up *op.Upload, n int) {
		GinkgoHelper()
		metaObj := c.Object(extraPoolName, "", mOID(up))
		Expect(metaObj).NotTo(BeNil(), "the meta object stays")
		for i := 1; i <= n; i++ {
			_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey("k."+up.ID, i))
			Expect(ok).To(BeTrue(), "part %d's index entry", i)
			Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, i))).NotTo(BeNil(), "part %d's head", i)
		}
	}
	readBack := func(ctx context.Context, key meta.ObjKey) []byte {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue(), "%v exists", key)
		var out bytes.Buffer
		Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &out)).To(Succeed())
		return out.Bytes()
	}

	It("completes an upload on an enabled bucket into a version at the instance it is given, recording the instance", func(ctx SpecContext) {
		mustPut(ctx, "v1", "one")
		up, etags := upload(ctx, reprefix, 5<<20, 3<<20)
		up.WriteTag = "tx-complete"
		c.ResetCounters()
		res, err := complete(ctx, up, "c1", etags)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		want := mpETag(etags...)
		Expect([]any{res.ETag, res.Size, res.Version}).To(Equal([]any{want, uint64(8 << 20), "c1"}))

		By("the record, naming the version, in the renewal's op")
		renew := c.WritesTo(extraPoolName, "", mOID(up))[1].Steps()
		Expect(renew[2:]).To(Equal([]radosclient.Step{
			&radosclient.SetXattrStep{Name: "user.rgw.mp_completion_tag", Value: []byte("tx-complete\x00")},
			&radosclient.SetXattrStep{Name: "user.rgw.mp_completion_instance", Value: []byte("c1")},
		}), "ceph/ceph#72103 at 2c1db8239cc records the instance a versioning-enabled completion writes")

		By("its version, current over the one before")
		h := head("c1")
		Expect(h).NotTo(BeNil(), "the version's head at its instance's oid")
		Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-complete\x00")))
		Expect(meta.DecodeManifest(denc.NewDecoder(h.Xattrs[meta.AttrManifest])).Prefix).To(Equal("k." + up.ID))
		Expect(target()).To(Equal(version("c1")))
		Expect(listed()).To(Equal([]string{"c1", "v1"}))
		en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
		Expect(ok).To(BeTrue())
		Expect([]any{en.Flags, en.VersionedEpoch, en.Meta.ETag}).To(Equal([]any{rgwcls.FlagVer | rgwcls.FlagCurrent, uint64(3), want}))
		prep := execIn(c.WritesTo(rookIndexPool, "", shard)[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect([]any{prep.Key, prep.BILogFlags}).To(Equal([]any{rgwcls.ObjKey{Name: "k", Instance: "c1"}, rgwcls.BILogFlagVersionedOp}))
		Expect(links()).To(Equal(1), "the version's link alone")

		By("the upload retired with its meta object")
		Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil())
		for n := 1; n <= 2; n++ {
			_, ok := c.Entry(rookIndexPool, "", shard, partIndexKey("k."+up.ID, n))
			Expect(ok).To(BeFalse(), "part %d's entry", n)
		}
		Expect(readBack(ctx, version(""))).To(Equal(append(bytes.Repeat([]byte("a"), 5<<20), bytes.Repeat([]byte("b"), 3<<20)...)))
	})

	It("writes the parts unversioned on an enabled bucket, as a part head has no epoch", func(ctx SpecContext) {
		c.ResetCounters()
		up, _ := upload(ctx, reprefix, 5<<20)
		Expect(links()).To(BeZero())
		for _, w := range c.WritesTo(rookIndexPool, "", shard) {
			if callsMethod(w.Steps(), "bucket_prepare_op") {
				p := execIn(w, 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
				Expect(p.BILogFlags).To(BeZero(), "%v", p.Key)
			}
		}
		_, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeFalse(), "no OLH for k before the completion")
		partsIntact(up, 1)
	})

	It("completes into the null version over a suspended bucket's OLH head, recording null", func(ctx SpecContext) {
		mustPut(ctx, "v1", "one")
		suspend()
		up, etags := upload(ctx, reprefix, 5<<20)
		up.WriteTag = "tx-complete"
		failMetaDelete(up, "")
		res, err := complete(ctx, up, "", etags)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(res.Version).To(BeEmpty(), "the null version")
		Expect(record(up)).To(Equal([]string{"tx-complete\x00", "null"}))
		Expect(target()).To(Equal(version("")), "the null version is current")
		Expect(listed()).To(Equal([]string{"", "v1"}))
		olh := c.Object(testDataPool, "", olhHeadOID(version("")))
		Expect(olh.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-complete\x00")))
		Expect(meta.DecodeManifest(denc.NewDecoder(olh.Xattrs[meta.AttrManifest])).Prefix).To(Equal("k." + up.ID))
	})

	It("completes on an object-lock bucket, which the S3 op refuses", func(ctx SpecContext) {
		rec.Info.Flags |= meta.BucketObjLockEnabled
		up, etags := upload(ctx, reprefix, 5<<20)
		_, err := complete(ctx, up, "c1", etags)
		Expect(err).NotTo(HaveOccurred())
		Expect(target()).To(Equal(version("c1")))
	})

	It("answers If-None-Match: * against a current version 412, leaving no instance and the upload whole", func(ctx SpecContext) {
		mustPut(ctx, "v1", "one")
		up, etags := upload(ctx, reprefix, 5<<20, 5<<20)
		up.IfNoneMatch = "*"
		_, err := complete(ctx, up, "c1", etags)
		Expect(err).To(MatchError(op.ErrPreconditionFailed))
		settle(s)
		Expect(head("c1")).To(BeNil(), "no head at the instance")
		_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
		Expect(ok).To(BeFalse(), "no instance entry")
		Expect(listed()).To(Equal([]string{"v1"}))
		Expect(target()).To(Equal(version("v1")))
		partsIntact(up, 2)
		Expect(record(up)).To(Equal([]string{"", ""}), "the record of a head write that wrote nothing is taken back")
		up.IfNoneMatch = ""
		_, err = complete(ctx, up, "c1", etags)
		Expect(err).NotTo(HaveOccurred(), "the upload can still be completed")
		Expect(target()).To(Equal(version("c1")))
	})

	It("answers 412 when the version its If-Match matched goes between its init and its link, its record kept so the retry is refused", func(ctx SpecContext) {
		mustPut(ctx, "v1", "one")
		up, etags := upload(ctx, reprefix, 5<<20)
		up.WriteTag = "tx-first"
		up.IfMatch = md5hex("one")
		olhOID := olhHeadOID(version(""))
		c.AfterWrite(testDataPool, "", olhOID, func() {
			c.AfterWrite(testDataPool, "", olhOID, nil)
			Expect(s.UnlinkObjInstanceForTest(ctx, rec, version("v1"), driver.OLHUnlink{})).To(Succeed())
		})
		_, err := complete(ctx, up, "c1", etags)
		Expect(err).To(MatchError(op.ErrPreconditionFailed), "the check after the link voids it")
		settle(s)
		Expect(head("c1")).To(BeNil(), "the void version is removed")
		Expect(listed()).To(BeEmpty())
		Expect(record(up)).To(Equal([]string{"tx-first\x00", "c1"}), "its head was written, so the record stays")
		Expect(chainOIDs(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-first\x00"))))).
			To(ContainElement(partHeadOID("k."+up.ID, 1)), "the removal queued the parts")
		up.IfMatch = ""
		_, err = complete(ctx, up, "c2", etags)
		Expect(err).To(MatchError(op.ErrInternalError), "no completion over parts the GC will remove")
		Expect(head("c2")).To(BeNil())
		Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the meta object stays for an abort")
	})

	It("answers a re-sent completion with the version it completed, current on the key", func(ctx SpecContext) {
		up, etags := upload(ctx, reprefix, 5<<20)
		first, err := complete(ctx, up, "c1", etags)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		again, err := complete(ctx, up, "c2", etags)
		Expect(err).NotTo(HaveOccurred(), "check_previously_completed reads the key's current version (rgw_op.cc:6438-6445 at v19.2.7)")
		Expect(again).To(Equal(&op.PutResult{ETag: first.ETag, Size: first.Size, Mtime: first.Mtime, Epoch: first.Epoch, Version: "c1"}))
		Expect(head("c2")).To(BeNil())
	})

	Describe("whose meta object outlived its head write", func() {
		// leaveMeta completes up into instance with the meta object's removal
		// failing, as a gateway that stops after the head write leaves it.
		leaveMeta := func(ctx context.Context, up *op.Upload, instance string, etags []string) {
			GinkgoHelper()
			failMetaDelete(up, instance)
			_, err := complete(ctx, up, instance, etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the failed removal left the meta object")
			c.BeforeWrite(extraPoolName, "", mOID(up), nil)
		}

		It("finishes a recorded completion whose version is linked, answering with that version and writing no second link", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, "c1", etags)
			Expect(record(up)).To(Equal([]string{"tx-first\x00", "c1"}))
			mustPut(ctx, "v2", "two")
			up.WriteTag = "tx-retry"
			linked := links()
			res, err := complete(ctx, up, "c2", etags)
			Expect(links()).To(Equal(linked), "no second link")
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{res.ETag, res.Version}).To(Equal([]any{mpETag(etags...), "c1"}), "a replay answers with the version the earlier completion wrote")
			Expect(head("c2")).To(BeNil(), "no second version over the same parts")
			Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the retry removed the meta object")
			Expect(listed()).To(Equal([]string{"v2", "c1"}), "the noncurrent version the record names keeps its parts")
			Expect(c.Object(testDataPool, "", partHeadOID("k."+up.ID, 1))).NotTo(BeNil())
		})

		It("answers 500 to the retry of a recorded completion whose version is gone, writing nothing over its queued parts", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, "c1", etags)
			_, err := s.DeleteObject(ctx, rec, version("c1"), op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(chainOIDs(allGCEntries(c))).To(ContainElement(partHeadOID("k."+up.ID, 1)), "the version's removal queued its parts")
			_, err = complete(ctx, up, "c2", etags)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(head("c2")).To(BeNil())
			Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the meta object stays for an abort")
		})

		It("answers 500 to the retry of a recorded null version that was deleted, its OLH head left", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, "", etags)
			Expect(record(up)).To(Equal([]string{"tx-first\x00", "null"}))
			_, err := s.DeleteObject(ctx, rec, version(meta.NullInstance), op.DeleteParams{})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			olh := c.Object(testDataPool, "", olhHeadOID(version("")))
			Expect(olh).NotTo(BeNil(), "the removal keeps the OLH head")
			Expect(olh.Xattrs).NotTo(HaveKey(meta.AttrManifest), "without the null version")
			up.WriteTag = "tx-retry"
			_, err = complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the meta object stays for an abort")
			Expect(target()).To(Equal(version("v1")))
		})

		It("reads no head of the multipart versions written before its upload's initiation less 15 minutes", func(ctx SpecContext) {
			rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}
			for i := range 40 {
				up, etags := upload(ctx, fmt.Sprintf("HISTORY%02dHISTORYHISTORYHISTORYHISTORY", i), 1)
				_, err := complete(ctx, up, fmt.Sprintf("h%02d", i), etags)
				Expect(err).NotTo(HaveOccurred())
				settle(s)
			}
			clock.Add(int64(time.Hour))
			up, etags := upload(ctx, reprefix, 1)
			c.ResetCounters()
			res, err := complete(ctx, up, "c1", etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Version).To(Equal("c1"))
			for i := range 39 {
				Expect(c.Reads(testDataPool, "", instanceHeadOID(version(fmt.Sprintf("h%02d", i))))).To(BeZero(), "noncurrent version h%02d", i)
			}
			Expect(c.Reads(rookIndexPool, "", shard)).To(BeNumerically("<", 8), "one page of versions, not the three the 40 fill, beside the null version's lookup")
		})

		// leaveNullUnlinked completes up into the null version of a key whose
		// null version n1 and versions v1 and v2 are an hour older than up, the
		// link sent nowhere, then strips the record, as a floor radosgw would
		// have left the upload: the null instance entry is up's, its list entry
		// n1's.
		leaveNullUnlinked := func(ctx context.Context) (*op.Upload, []string) {
			GinkgoHelper()
			mustPut(ctx, "v1", "one")
			suspend()
			mustPut(ctx, "", "n1")
			rec.Info.Flags &^= meta.BucketVersionsSuspended
			mustPut(ctx, "v2", "two")
			suspend()
			clock.Add(int64(time.Hour))
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			o := c.Object(extraPoolName, "", mOID(up))
			delete(o.Xattrs, "user.rgw.mp_completion_tag")
			delete(o.Xattrs, "user.rgw.mp_completion_instance")
			Expect(listed()).To(Equal([]string{"v2", "", "v1"}), "the null list entry is n1's, below v2")
			return up, etags
		}

		It("finds a null version rewritten after its upload's initiation, though its list entry is an older one's", func(ctx SpecContext) {
			up, etags := leaveNullUnlinked(ctx)
			up.WriteTag = "tx-retry"
			res, err := complete(ctx, up, "", etags)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Version).To(BeEmpty(), "the null version that names the parts")
			Expect(c.Object(testDataPool, "", olhHeadOID(version(""))).Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-first\x00")), "no second head over the parts")
			settle(s)
			Expect(target()).To(Equal(version("")), "linked before the 200")
			Expect(listed()).To(Equal([]string{"", "v2", "v1"}))
		})

		It("links an unlinked null version a retry without a record finds, rather than answer 200 for it unlinked", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			o := c.Object(extraPoolName, "", mOID(up))
			delete(o.Xattrs, "user.rgw.mp_completion_tag")
			delete(o.Xattrs, "user.rgw.mp_completion_instance")
			Expect(listed()).To(Equal([]string{"v1"}), "the null version is not listed")
			up.WriteTag = "tx-retry"
			res, err := complete(ctx, up, "", etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Version).To(BeEmpty())
			Expect(target()).To(Equal(version("")), "the null version is current")
			Expect(listed()).To(Equal([]string{"", "v1"}))
			Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the upload is retired")
		})

		It("answers 500 to a retry without a record whose unlinked null version cannot be linked, keeping the upload", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			o := c.Object(extraPoolName, "", mOID(up))
			delete(o.Xattrs, "user.rgw.mp_completion_tag")
			delete(o.Xattrs, "user.rgw.mp_completion_instance")
			ac.set(onFirst("bucket_link_olh", failWith(syscall.EIO)))
			up.WriteTag = "tx-retry"
			_, err = complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrInternalError))
			settle(s)
			ac.set(nil)
			Expect(target()).To(Equal(version("v1")))
			Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the meta object stays")
			partsIntact(up, 1)
		})

		It("refuses an abort while a null version rewritten after its upload's initiation names its parts", func(ctx SpecContext) {
			up, _ := leaveNullUnlinked(ctx)
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrNoSuchUpload))
			settle(s)
			Expect(chainOIDs(allGCEntries(c))).NotTo(ContainElement(partHeadOID("k."+up.ID, 1)), "the null version keeps its parts")
			Expect(readBack(ctx, meta.ObjKey{Name: "k", Instance: meta.NullInstance})).To(Equal(bytes.Repeat([]byte("a"), 5<<20)))
		})

		It("reads every version when its upload's index entry is missing, rather than narrow the scan on a guess", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			clock.Add(-int64(time.Hour))
			leaveMeta(ctx, up, "c1", etags)
			o := c.Object(extraPoolName, "", mOID(up))
			delete(o.Xattrs, "user.rgw.mp_completion_tag")
			delete(o.Xattrs, "user.rgw.mp_completion_instance")
			clock.Add(int64(2 * time.Hour))
			mustPut(ctx, "v2", "two")
			metaEntry := meta.ObjKey{Name: "k." + up.ID + ".meta", NS: meta.NSMultipart}.IndexKeyName()
			Expect(c.Object(rookIndexPool, "", shard).Omap).To(HaveKey(metaEntry))
			delete(c.Object(rookIndexPool, "", shard).Omap, metaEntry)
			res, err := complete(ctx, up, "c2", etags)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Version).To(Equal("c1"), "c1, written an hour before up's initiation, is found unbounded")
			Expect(head("c2")).To(BeNil())
		})

		DescribeTable("bounds its search for an earlier completion at its upload's initiation less 15 minutes",
			func(ctx SpecContext, skew time.Duration, found bool) {
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				clock.Add(-int64(skew))
				leaveMeta(ctx, up, "c1", etags)
				o := c.Object(extraPoolName, "", mOID(up))
				delete(o.Xattrs, "user.rgw.mp_completion_tag")
				delete(o.Xattrs, "user.rgw.mp_completion_instance")
				clock.Add(int64(skew + time.Hour))
				mustPut(ctx, "v2", "two")
				res, err := complete(ctx, up, "c2", etags)
				Expect(err).NotTo(HaveOccurred())
				if found {
					Expect(res.Version).To(Equal("c1"), "the version that names the parts")
					Expect(head("c2")).To(BeNil())
					return
				}
				Expect(res.Version).To(Equal("c2"), "a second version over the parts c1 names")
			},
			Entry("finding a version written under a clock 14 minutes behind", 14*time.Minute, true),
			Entry("missing one written under a clock 16 minutes behind, which no correct clock writes", 16*time.Minute, false),
		)

		It("finishes an earlier completion a floor radosgw left without a record, when a later version made it noncurrent", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, "c1", etags)
			o := c.Object(extraPoolName, "", mOID(up))
			delete(o.Xattrs, "user.rgw.mp_completion_tag")
			delete(o.Xattrs, "user.rgw.mp_completion_instance")
			mustPut(ctx, "v2", "two")
			res, err := complete(ctx, up, "c2", etags)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Version).To(Equal("c1"), "the noncurrent version that names the parts")
			Expect(head("c2")).To(BeNil(), "no second version over the parts version c1 names")
			Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil())
			Expect(listed()).To(Equal([]string{"v2", "c1"}))
		})

		It("finishes an earlier completion a floor radosgw left without a record, when it is the current version", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			leaveMeta(ctx, up, "c1", etags)
			o := c.Object(extraPoolName, "", mOID(up))
			delete(o.Xattrs, "user.rgw.mp_completion_tag")
			delete(o.Xattrs, "user.rgw.mp_completion_instance")
			res, err := complete(ctx, up, "c2", etags)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Version).To(Equal("c1"))
			Expect(head("c2")).To(BeNil())
			Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil())
		})

		Describe("over a version whose index entries changed after its link", func() {
			// stripRecord removes up's completion record, as a floor radosgw's
			// completion leaves the meta object.
			stripRecord := func(up *op.Upload) {
				o := c.Object(extraPoolName, "", mOID(up))
				delete(o.Xattrs, "user.rgw.mp_completion_tag")
				delete(o.Xattrs, "user.rgw.mp_completion_instance")
			}
			// changeAttrs sets an attr on key's version, as set_attrs does under a
			// fresh tag on the head and its instance entry, and checks that the
			// list entry kept the tag of the version's link.
			changeAttrs := func(ctx context.Context, key meta.ObjKey) {
				GinkgoHelper()
				st, err := s.StatObject(ctx, rec, key)
				Expect(err).NotTo(HaveOccurred())
				Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{"user.rgw.x-amz-meta-color": []byte("blue")}, nil)).To(Succeed())
				settle(s)
				instance := key.Instance
				if instance == meta.NullInstance {
					instance = ""
				}
				en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: instance})
				Expect(ok).To(BeTrue())
				var le rgwcls.DirEntry
				for _, l := range c.ListEntries(rookIndexPool, "", shard, "k") {
					if l.Key.Instance == instance {
						le = l
					}
				}
				Expect(le.Tag).To(Equal("tx-first"), "the list entry keeps the link's tag")
				Expect(en.Tag).NotTo(Equal(le.Tag), "set_attrs rewrote the instance entry's tag (rgw_rados.cc:6853-6860, 6909 at v19.2.7)")
				Expect([]any{en.Meta.ETag, en.Meta.Size}).To(Equal([]any{le.Meta.ETag, le.Meta.Size}), "and kept its ETag and size")
			}

			It("finishes a version a later write made noncurrent without linking it current again", func(ctx SpecContext) {
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				leaveMeta(ctx, up, "c1", etags)
				stripRecord(up)
				mustPut(ctx, "v2", "two")
				changeAttrs(ctx, version("c1"))
				linked := links()
				res, err := complete(ctx, up, "c2", etags)
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(res.Version).To(Equal("c1"))
				Expect(links()).To(Equal(linked), "the listing found c1, so it is linked")
				Expect(target()).To(Equal(version("v2")), "v2 stays current")
				Expect(listed()).To(Equal([]string{"v2", "c1"}))
				Expect(head("c2")).To(BeNil())
				Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the upload is retired")
			})

			It("finishes the current version without linking it a second time", func(ctx SpecContext) {
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				leaveMeta(ctx, up, "c1", etags)
				stripRecord(up)
				changeAttrs(ctx, version("c1"))
				linked := links()
				res, err := complete(ctx, up, "c2", etags)
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(res.Version).To(Equal("c1"))
				Expect(links()).To(Equal(linked), "the current version's read found c1, so it is linked")
				Expect(target()).To(Equal(version("c1")))
				Expect(listed()).To(Equal([]string{"c1"}))
			})

			It("finishes a version whose instance entry a listing repair left unlike its list entry without linking it", func(ctx SpecContext) {
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				leaveMeta(ctx, up, "c1", etags)
				stripRecord(up)
				mustPut(ctx, "v2", "two")
				By("a repair that rewrote c1's instance entry and not its list entry, as dir_suggest_changes does")
				idx := rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: "c1"}, false)
				o := c.Object(rookIndexPool, "", shard)
				d := denc.NewDecoder(o.Omap[idx])
				en := rgwcls.DecodeDirEntry(d)
				Expect(d.Err()).NotTo(HaveOccurred())
				en.Meta.Size++
				o.Omap[idx] = encode(en)
				linked := links()
				res, err := complete(ctx, up, "c2", etags)
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(res.Version).To(Equal("c1"))
				Expect(links()).To(Equal(linked), "the listing found c1, so it is linked whatever its entries hold")
				Expect(target()).To(Equal(version("v2")), "v2 stays current")
				Expect(listed()).To(Equal([]string{"v2", "c1"}))
			})

			It("finishes a null version a later write made noncurrent without linking it current again, judging it by ETag and size", func(ctx SpecContext) {
				mustPut(ctx, "v1", "one")
				suspend()
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				leaveMeta(ctx, up, "", etags)
				stripRecord(up)
				rec.Info.Flags &^= meta.BucketVersionsSuspended
				mustPut(ctx, "v2", "two")
				changeAttrs(ctx, version(meta.NullInstance))
				linked := links()
				res, err := complete(ctx, up, "c2", etags)
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(res.Version).To(BeEmpty(), "the null version")
				Expect(links()).To(Equal(linked), "its list entry has its instance entry's ETag and size")
				Expect(target()).To(Equal(version("v2")), "v2 stays current")
				Expect(listed()).To(Equal([]string{"v2", "", "v1"}))
				Expect(head("c2")).To(BeNil())
			})
		})
	})

	It("keeps its record after a refused link removed its version, and answers the retry 500, writing no second version over the queued parts", func(ctx SpecContext) {
		up, etags := upload(ctx, reprefix, 5<<20)
		up.WriteTag = "tx-first"
		ac.set(onFirst("bucket_link_olh", failWith(syscall.EIO)))
		_, err := complete(ctx, up, "c1", etags)
		Expect(err).To(MatchError(op.ErrUnknown))
		settle(s)
		ac.set(nil)
		Expect(record(up)).To(Equal([]string{"tx-first\x00", "c1"}))
		Expect(chainOIDs(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-first\x00"))))).
			To(ContainElement(partHeadOID("k."+up.ID, 1)), "the refused link's version was removed, its parts queued")
		up.WriteTag = "tx-retry"
		_, err = complete(ctx, up, "c2", etags)
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(head("c2")).To(BeNil(), "no second version")
		Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the meta object stays for an abort")
	})

	Describe("a retry of a completion whose version never linked", func() {
		// retry answers the retry of up's completion, the version it wrote then
		// checked linked and current.
		retry := func(ctx context.Context, up *op.Upload, etags []string) {
			GinkgoHelper()
			ac.set(nil)
			up.WriteTag = "tx-retry"
			res, err := complete(ctx, up, "c2", etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Version).To(Equal("c1"), "the version the earlier completion wrote")
			Expect(head("c2")).To(BeNil(), "no second version")
			Expect(listed()).To(Equal([]string{"c1", "v1"}), "linked over the version current before")
			Expect(target()).To(Equal(version("c1")))
			en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
			Expect(ok).To(BeTrue())
			Expect([]any{en.Exists, en.Meta.ETag, en.Meta.Size}).To(Equal([]any{true, mpETag(etags...), uint64(5 << 20)}))
			Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the upload is retired")
			Expect(readBack(ctx, version(""))).To(Equal(bytes.Repeat([]byte("a"), 5<<20)))
		}

		It("links it after a link that timed out, and answers 200", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			Expect(listed()).To(Equal([]string{"v1"}), "no link landed")
			retry(ctx, up, etags)
		})

		It("completes its index entry and links it after a gateway stopped once the head was written", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_complete_op", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			en, _ := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
			Expect(en.Exists).To(BeFalse(), "only the prepare reached the index")
			retry(ctx, up, etags)
		})

		It("links a suspended bucket's null version after a link that timed out", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			Expect(target()).To(Equal(version("v1")), "no link landed")
			ac.set(nil)
			up.WriteTag = "tx-retry"
			res, err := complete(ctx, up, "", etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Version).To(BeEmpty())
			Expect(target()).To(Equal(version("")), "the null version is current")
			Expect(listed()).To(Equal([]string{"", "v1"}))
		})

		It("answers 500 and keeps the meta object when the version's head never landed", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirstMatching(writesManifest, failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			Expect(head("c1")).To(BeNil())
			Expect(record(up)).To(Equal([]string{"tx-first\x00", "c1"}), "the head may yet land")
			up.WriteTag = "tx-retry"
			_, err = complete(ctx, up, "c2", etags)
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(head("c2")).To(BeNil())
			partsIntact(up, 1)
		})

		It("answers 500 and keeps the version and the meta object when the link is refused", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(onFirst("bucket_link_olh", failWith(syscall.EIO)))
			up.WriteTag = "tx-retry"
			_, err = complete(ctx, up, "c2", etags)
			Expect(err).To(MatchError(op.ErrInternalError))
			settle(s)
			Expect(head("c1")).NotTo(BeNil(), "the version stays, its parts with it")
			Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil())
			ac.set(nil)
			retry(ctx, up, etags)
		})

		It("links a null version written over an earlier null version, whose entry keeps the earlier one's epoch", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			mustPut(ctx, "", "n1")
			rec.Info.Flags &^= meta.BucketVersionsSuspended
			mustPut(ctx, "v2", "two")
			suspend()
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			Expect(target()).To(Equal(version("v2")), "no link landed")
			en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k"})
			Expect(ok).To(BeTrue())
			Expect([]any{en.Tag, en.VersionedEpoch > 0}).To(Equal([]any{"tx-first", true}), "complete_op kept n1's epoch")
			ac.set(nil)
			up.WriteTag = "tx-retry"
			res, err := complete(ctx, up, "", etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Version).To(BeEmpty())
			Expect(target()).To(Equal(version("")), "the null version is current")
			Expect(listed()).To(Equal([]string{"", "v2", "v1"}))
		})

		It("answers 412 to a retry whose condition no longer holds, keeping the version, and a later retry links it", func(ctx SpecContext) {
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			up.IfNoneMatch = "*"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			mustPut(ctx, "v2", "two")
			up.WriteTag = "tx-retry"
			_, err = complete(ctx, up, "c2", etags)
			Expect(err).To(MatchError(op.ErrPreconditionFailed), "v2 is current")
			Expect(err).NotTo(MatchError(op.ErrInternalError), "a 412, not a 500")
			settle(s)
			Expect(c.Object(extraPoolName, "", mOID(up))).NotTo(BeNil(), "the meta object stays")
			Expect(head("c1")).NotTo(BeNil(), "the version stays, its parts with it")
			Expect(listed()).To(Equal([]string{"v2"}))
			up.IfNoneMatch = ""
			res, err := complete(ctx, up, "c3", etags)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Version).To(Equal("c1"))
			Expect(listed()).To(Equal([]string{"c1", "v2"}))
			Expect(target()).To(Equal(version("c1")))
		})

		DescribeTable("an abort after a retry answered 500 removes the unlinked version, and its parts go to the GC",
			func(ctx SpecContext, fail string) {
				mustPut(ctx, "v1", "one")
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				ac.set(onFirst(fail, failWith(syscall.ETIMEDOUT)))
				_, err := complete(ctx, up, "c1", etags)
				Expect(err).To(MatchError(op.ErrRequestTimedOut))
				settle(s)
				ac.set(onFirst("bucket_link_olh", failWith(syscall.EIO)))
				up.WriteTag = "tx-retry"
				_, err = complete(ctx, up, "c2", etags)
				Expect(err).To(MatchError(op.ErrInternalError))
				settle(s)
				ac.set(nil)
				clock.Add(int64(2 * time.Hour))
				Expect(s.Abort(ctx, up)).To(Succeed(), "the first link's pending modification expired, so no link of c1 can land")
				settle(s)
				Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the upload is gone")
				Expect(head("c1")).To(BeNil(), "the unlinked version's head is gone")
				_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
				Expect(ok).To(BeFalse(), "and its instance entry")
				st, err := s.StatObject(ctx, rec, version("c1"))
				Expect(err).NotTo(HaveOccurred())
				Expect(st.Exists).To(BeFalse())
				Expect(listed()).To(Equal([]string{"v1"}))
				Expect(target()).To(Equal(version("v1")))
				Expect(chainOIDs(allGCEntries(c))).To(ContainElement(partHeadOID("k."+up.ID, 1)), "the parts go to the GC")
				_, ok = c.Entry(rookIndexPool, "", shard, partIndexKey("k."+up.ID, 1))
				Expect(ok).To(BeFalse(), "the abort goes on to retire the parts' entries")
			},
			Entry("after a link that timed out", "bucket_link_olh"),
			Entry("after a gateway stopped once the head was written", "bucket_complete_op"),
		)

		// pendingOLH is the pending modifications on k's OLH head.
		pendingOLH := func() []string {
			var out []string
			for name := range c.Object(testDataPool, "", olhHeadOID(version(""))).Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
					out = append(out, name)
				}
			}
			return out
		}
		// unlinkedKept checks that c1, its index entry, the upload and its parts
		// all stand, nothing queued.
		unlinkedKept := func(up *op.Upload) {
			GinkgoHelper()
			Expect(head("c1")).NotTo(BeNil(), "the version's head")
			_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
			Expect(ok).To(BeTrue(), "the version's instance entry")
			partsIntact(up, 1)
			Expect(chainOIDs(allGCEntries(c))).NotTo(ContainElement(partHeadOID("k."+up.ID, 1)), "nothing queued")
		}

		It("answers 500 and removes nothing when an abort races a link of the version inited but not applied", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			Expect(pendingOLH()).To(HaveLen(1), "the init landed; the link may yet")
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrInternalError))
			settle(s)
			unlinkedKept(up)

			By("once that modification expires, no link of c1 can land, and the abort removes it")
			clock.Add(int64(2 * time.Hour))
			Expect(s.Abort(ctx, up)).To(Succeed())
			settle(s)
			Expect(head("c1")).To(BeNil())
			Expect(chainOIDs(allGCEntries(c))).To(ContainElement(partHeadOID("k."+up.ID, 1)))
		})

		It("answers 500 and removes nothing while the OLH log names the version, its pending modification expired", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			clock.Add(int64(2 * time.Hour))
			en, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue())
			if en.PendingLog == nil {
				en.PendingLog = map[uint64][]rgwcls.OLHLogEntry{}
			}
			epoch := en.Epoch + 1
			en.PendingLog[epoch] = append(en.PendingLog[epoch], rgwcls.OLHLogEntry{Epoch: epoch, Op: rgwcls.OLHOpLinkOLH, OpTag: "landed", Key: rgwcls.ObjKey{Name: "k", Instance: "c1"}})
			c.Object(rookIndexPool, "", shard).Omap[rgwcls.OLHDataKey("k")] = encode(en)
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrInternalError), "a link of c1 is in the log, not yet replayed")
			settle(s)
			unlinkedKept(up)
		})

		It("removes nothing when its lock term runs out before the record branch removes the version", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			clock.Add(int64(2 * time.Hour))
			armed := false
			c.AfterWrite(extraPoolName, "", mOID(up), func() {
				if !armed {
					armed = true
					clock.Add(int64(11 * time.Minute))
				}
			})
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable), "the lock the abort took has lapsed")
			c.AfterWrite(extraPoolName, "", mOID(up), nil)
			settle(s)
			unlinkedKept(up)
		})

		It("removes nothing when its lock is taken over between its term check and the removal", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "c1", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			clock.Add(int64(2 * time.Hour))
			oid := mOID(up)
			ac.set(onFirstMatching(func(w *radosclient.WriteOp) bool {
				return callsMethod(w.Steps(), "assert_locked") && !callsMethod(w.Steps(), "obj_remove")
			}, func(send sendFunc) (uint64, error) {
				releaseLock(c, oid)
				holdLock(ctx, c, oid)
				return send()
			}))
			Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable), "another gateway holds the lock now")
			ac.set(nil)
			settle(s)
			unlinkedKept(up)
		})

		Describe("removing the unlinked version, its index entry before its head", func() {
			// leaveUnlinked completes up into c1 over v1 with the link timing out,
			// and lets that link's pending modification expire, so that the abort
			// takes c1 for unlinked and removes it.
			leaveUnlinked := func(ctx context.Context) (*op.Upload, []string) {
				GinkgoHelper()
				mustPut(ctx, "v1", "one")
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
				_, err := complete(ctx, up, "c1", etags)
				Expect(err).To(MatchError(op.ErrRequestTimedOut))
				settle(s)
				ac.set(nil)
				clock.Add(int64(2 * time.Hour))
				return up, etags
			}
			// linkC1 links c1, as the timed-out link landing late would.
			linkC1 := func(ctx context.Context) {
				GinkgoHelper()
				Expect(s.SetOLHForTest(ctx, rec, version("c1"), driver.OLHLink{})).To(Succeed())
			}
			// preparesDelOfC1 matches the index prepare DEL of c1's instance key.
			preparesDelOfC1 := func(w *radosclient.WriteOp) bool {
				for _, st := range w.Steps() {
					if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_prepare_op" {
						p := rgwcls.DecodePrepareOp(denc.NewDecoder(x.In))
						return p.Op == rgwcls.OpDel && p.Key == rgwcls.ObjKey{Name: "k", Instance: "c1"}
					}
				}
				return false
			}
			// comparesEntry matches a write op that compares an omap value.
			comparesEntry := func(w *radosclient.WriteOp) bool {
				return slices.ContainsFunc(w.Steps(), func(st radosclient.Step) bool {
					_, ok := st.(*radosclient.OmapCmpStep)
					return ok
				})
			}
			// linkedKept checks that c1 is linked and current with its head, and
			// the upload and its parts stand, nothing queued.
			linkedKept := func(ctx context.Context, up *op.Upload) {
				GinkgoHelper()
				Expect(head("c1")).NotTo(BeNil(), "the version's head")
				Expect(listed()).To(Equal([]string{"c1", "v1"}))
				Expect(target()).To(Equal(version("c1")))
				Expect(readBack(ctx, version(""))).To(Equal(bytes.Repeat([]byte("a"), 5<<20)), "a listed version with its data")
				partsIntact(up, 1)
				Expect(chainOIDs(allGCEntries(c))).NotTo(ContainElement(partHeadOID("k."+up.ID, 1)), "nothing queued")
			}

			It("answers 500 and removes nothing when a link lands between its prepare and its read of the entry", func(ctx SpecContext) {
				up, _ := leaveUnlinked(ctx)
				ac.set(onFirstMatching(preparesDelOfC1, func(send sendFunc) (uint64, error) {
					v, err := send()
					linkC1(ctx)
					return v, err
				}))
				Expect(s.Abort(ctx, up)).To(MatchError(op.ErrInternalError), "the entry it read carries the link's versioned epoch")
				ac.set(nil)
				settle(s)
				linkedKept(ctx, up)
			})

			It("answers 500 and removes nothing when a link lands between its read of the entry and the removal, which compares it", func(ctx SpecContext) {
				up, _ := leaveUnlinked(ctx)
				ac.set(onFirstMatching(comparesEntry, func(send sendFunc) (uint64, error) {
					linkC1(ctx)
					return send()
				}))
				Expect(s.Abort(ctx, up)).To(MatchError(op.ErrInternalError), "OMAP_CMP answers ECANCELED for the entry the link rewrote")
				ac.set(nil)
				settle(s)
				linkedKept(ctx, up)
			})

			Describe("meeting another op pending on the version", func() {
				// pendingTags is the tags of the ops pending on c1's instance entry.
				pendingTags := func() []string {
					GinkgoHelper()
					en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
					Expect(ok).To(BeTrue(), "c1's instance entry")
					var out []string
					for _, p := range en.PendingMap {
						out = append(out, p.Tag)
					}
					return out
				}
				// cancelsUnder counts the complete_op CANCELs under tag sent to the
				// shard.
				cancelsUnder := func(tag string) int {
					GinkgoHelper()
					n := 0
					for _, w := range c.WritesTo(rookIndexPool, "", shard) {
						for _, st := range w.Steps() {
							if x, ok := st.(*radosclient.ExecStep); ok && x.Method == "bucket_complete_op" {
								d := denc.NewDecoder(x.In)
								co := rgwcls.DecodeCompleteOp(d)
								Expect(d.Err()).NotTo(HaveOccurred())
								if co.Op == rgwcls.OpCancel && co.Tag == tag {
									n++
								}
							}
						}
					}
					return n
				}
				// removed checks that c1 and the upload are gone, the parts to the GC.
				removed := func(up *op.Upload) {
					GinkgoHelper()
					Expect(head("c1")).To(BeNil(), "the version's head")
					_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
					Expect(ok).To(BeFalse(), "its instance entry")
					Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the upload")
					Expect(chainOIDs(allGCEntries(c))).To(ContainElement(partHeadOID("k."+up.ID, 1)), "the parts go to the GC")
					Expect(listed()).To(Equal([]string{"v1"}))
					Expect(target()).To(Equal(version("v1")))
				}
				// reopen opens s again with the options kv adds, as the outer
				// BeforeEach opens it.
				reopen := func(ctx context.Context, kv map[string]string) {
					GinkgoHelper()
					settle(s)
					r := denc.Squid
					var err error
					s, err = driver.Open(ctx, ac, conf(kv), driver.Options{Release: &r})
					Expect(err).NotTo(HaveOccurred())
					driver.SetClock(s, now)
					s.SetReshardWaitForTest(0)
					driver.SetCondSleepForTest(s, func(context.Context, time.Duration) error { return nil })
					s.SetRandForTest(fixedRand(putPrefix))
				}
				// setTagTimeout sets the tag timeout of k's index shard's header, as
				// bucket_set_tag_timeout stores it.
				setTagTimeout := func(seconds uint64) {
					GinkgoHelper()
					o := c.Object(rookIndexPool, "", shard)
					d := denc.NewDecoder(o.OmapHdr)
					h := rgwcls.DecodeDirHeader(d)
					Expect(d.Err()).NotTo(HaveOccurred())
					h.TagTimeout = seconds
					o.OmapHdr = encode(h)
				}

				It("answers 500 and removes nothing while that op is younger than the tag timeout", func(ctx SpecContext) {
					up, _ := leaveUnlinked(ctx)
					Expect(s.NewIndexOpForTest(rec, version("c1"), "tx-other").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
					clock.Add(int64(100 * time.Second))
					Expect(s.Abort(ctx, up)).To(MatchError(op.ErrInternalError), "complete_op would keep an entry with another op pending")
					settle(s)
					unlinkedKept(up)
					Expect(pendingTags()).To(ConsistOf("tx-other"), "a live op is not canceled")
					Expect(cancelsUnder("tx-other")).To(BeZero())
				})

				It("cancels another writer's op older than the tag timeout, as dir_suggest_changes drops one, and removes the version", func(ctx SpecContext) {
					up, _ := leaveUnlinked(ctx)
					Expect(s.NewIndexOpForTest(rec, version("c1"), "tx-other").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
					clock.Add(int64(130 * time.Second))
					Expect(s.Abort(ctx, up)).To(Succeed())
					settle(s)
					Expect(cancelsUnder("tx-other")).To(Equal(1))
					removed(up)
				})

				It("answers a later abort 500 while the prepare an interrupted abort left is younger than the tag timeout, and removes the version once it is older", func(ctx SpecContext) {
					up, _ := leaveUnlinked(ctx)
					By("an abort whose removal and its cancel are both lost, as a gateway that stops after its prepare leaves them")
					ac.set(func(w *radosclient.WriteOp, send sendFunc) (uint64, error) {
						if callsMethod(w.Steps(), "bucket_complete_op") {
							return failWith(syscall.ETIMEDOUT)(send)
						}
						return send()
					})
					Expect(s.Abort(ctx, up)).To(MatchError(op.ErrRequestTimedOut))
					settle(s)
					ac.set(nil)
					stuck := pendingTags()
					Expect(stuck).To(HaveLen(1), "the interrupted abort's prepare")
					partsIntact(up, 1)

					By("a later abort within the tag timeout")
					// Each abort's prepare takes a tag of its own, as random tags differ.
					s.SetRandForTest(fixedRand("SECONDABORTSECONDABORTSECONDABORT"))
					clock.Add(int64(100 * time.Second))
					Expect(s.Abort(ctx, up)).To(MatchError(op.ErrInternalError))
					settle(s)
					unlinkedKept(up)
					Expect(pendingTags()).To(Equal(stuck), "not canceled yet")

					By("one past it")
					s.SetRandForTest(fixedRand("THIRDABORTTHIRDABORTTHIRDABORTTHIRD"))
					clock.Add(int64(30 * time.Second))
					Expect(s.Abort(ctx, up)).To(Succeed())
					settle(s)
					Expect(cancelsUnder(stuck[0])).To(Equal(1))
					removed(up)
				})

				DescribeTable("takes the tag timeout as dir_suggest_changes does: the header's, else rgw_pending_bucket_index_op_expiration, else 120 s",
					func(ctx SpecContext, header uint64, kv map[string]string, age time.Duration, canceled bool) {
						if kv != nil {
							reopen(ctx, kv)
						}
						up, _ := leaveUnlinked(ctx)
						setTagTimeout(header)
						Expect(s.NewIndexOpForTest(rec, version("c1"), "tx-other").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
						clock.Add(int64(age))
						err := s.Abort(ctx, up)
						settle(s)
						if canceled {
							Expect(err).NotTo(HaveOccurred())
							removed(up)
							return
						}
						Expect(err).To(MatchError(op.ErrInternalError))
						unlinkedKept(up)
						Expect(cancelsUnder("tx-other")).To(BeZero())
					},
					Entry("an op of 130 s within the header's 600 s", uint64(600), nil, 130*time.Second, false),
					Entry("an op of 130 s within the option's 600 s, the header holding none",
						uint64(0), map[string]string{"rgw_pending_bucket_index_op_expiration": "600"}, 130*time.Second, false),
					Entry("an op of 90 s past the header's 60 s, which the option's 600 s does not override",
						uint64(60), map[string]string{"rgw_pending_bucket_index_op_expiration": "600"}, 90*time.Second, true),
					Entry("an op of 130 s past the default, the option at 0", uint64(0), map[string]string{"rgw_pending_bucket_index_op_expiration": "0"}, 130*time.Second, true),
				)
			})

			It("leaves the version's head unindexed when it stops after the entry's removal, and the next abort removes it", func(ctx SpecContext) {
				up, _ := leaveUnlinked(ctx)
				c.FailNextWrite(testDataPool, "", instanceHeadOID(version("c1")), syscall.ETIMEDOUT)
				Expect(s.Abort(ctx, up)).To(MatchError(op.ErrRequestTimedOut))
				settle(s)
				Expect(head("c1")).NotTo(BeNil(), "the head's removal did not land")
				_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
				Expect(ok).To(BeFalse(), "its instance entry went first")
				partsIntact(up, 1)

				By("the next abort, which finds it through the record")
				Expect(s.Abort(ctx, up)).To(Succeed())
				settle(s)
				Expect(head("c1")).To(BeNil())
				_, ok = c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
				Expect(ok).To(BeFalse(), "the entry its prepare made is gone too")
				Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the upload is gone")
				Expect(chainOIDs(allGCEntries(c))).To(ContainElement(partHeadOID("k."+up.ID, 1)), "the parts go to the GC")
				Expect(listed()).To(Equal([]string{"v1"}))
				Expect(target()).To(Equal(version("v1")))
			})

			It("completes an entry its completion left pending before the removal, so that completion's index op and link find nothing", func(ctx SpecContext) {
				mustPut(ctx, "v1", "one")
				up, etags := upload(ctx, reprefix, 5<<20)
				up.WriteTag = "tx-first"
				var stalled sendFunc
				ac.set(onFirst("bucket_complete_op", func(send sendFunc) (uint64, error) {
					stalled = send
					return failWith(syscall.ETIMEDOUT)(send)
				}))
				_, err := complete(ctx, up, "c1", etags)
				Expect(err).To(MatchError(op.ErrRequestTimedOut))
				settle(s)
				ac.set(nil)
				en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
				Expect(ok).To(BeTrue())
				Expect([]any{en.Exists, len(en.PendingMap)}).To(Equal([]any{false, 1}), "the completion's prepare alone landed")
				Expect(s.Abort(ctx, up)).To(Succeed())
				settle(s)
				Expect(head("c1")).To(BeNil())
				_, ok = c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
				Expect(ok).To(BeFalse(), "no entry stays for the completion's pending op")

				By("the stalled completion's index op lands")
				_, err = stalled()
				Expect(err).To(MatchError(radosclient.ErrInvalid), "complete_op's EINVAL for a tag no entry holds (cls_rgw.cc:1066-1073 at v19.2.7)")
				_, ok = c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "c1"})
				Expect(ok).To(BeFalse())

				By("and so does a link of it")
				Expect(s.SetOLHForTest(ctx, rec, version("c1"), driver.OLHLink{})).NotTo(Succeed(), "bucket_link_olh's ENOENT (cls_rgw.cc:1668-1688 at v19.2.7)")
				settle(s)
				Expect(listed()).To(Equal([]string{"v1"}))
				Expect(target()).To(Equal(version("v1")))
			})
		})

		It("leaves an unlinked null version its parts on an abort, its head being the key's OLH head", func(ctx SpecContext) {
			mustPut(ctx, "v1", "one")
			suspend()
			up, etags := upload(ctx, reprefix, 5<<20)
			up.WriteTag = "tx-first"
			ac.set(onFirst("bucket_link_olh", failWith(syscall.ETIMEDOUT)))
			_, err := complete(ctx, up, "", etags)
			Expect(err).To(MatchError(op.ErrRequestTimedOut))
			settle(s)
			ac.set(nil)
			Expect(s.Abort(ctx, up)).To(Succeed())
			settle(s)
			Expect(c.Object(extraPoolName, "", mOID(up))).To(BeNil(), "the meta object alone is removed")
			Expect(allGCEntries(c)).To(BeEmpty(), "no part goes to the GC: the null version's head names them")
			Expect(readBack(ctx, meta.ObjKey{Name: "k", Instance: meta.NullInstance})).To(Equal(bytes.Repeat([]byte("a"), 5<<20)))
			Expect(target()).To(Equal(version("v1")))
		})
	})

	It("links its version though its client leaves once its head is written", func(ctx SpecContext) {
		up, etags := upload(ctx, reprefix, 5<<20)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		ac.set(onFirstMatching(writesManifest, cancelAfter(cancel)))
		_, err := complete(cctx, up, "c1", etags)
		Expect(err).NotTo(HaveOccurred())
		Expect(cctx.Err()).To(MatchError(context.Canceled), "the client left")
		Expect(target()).To(Equal(version("c1")))
		for name := range c.Object(testDataPool, "", olhHeadOID(version(""))).Xattrs {
			Expect(name).NotTo(HavePrefix(meta.AttrOLHPendingPrefix), "no modification left to wait for rgw_olh_pending_timeout_sec")
		}
	})
})
