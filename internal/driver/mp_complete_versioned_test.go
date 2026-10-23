package driver_test

import (
	"bytes"
	"context"
	"fmt"
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
