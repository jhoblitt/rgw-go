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

// writesManifest reports whether op sets an object's manifest, as a head
// write does and a tail write or an OLH step does not.
func writesManifest(op *radosclient.WriteOp) bool {
	return slices.ContainsFunc(op.Steps(), func(st radosclient.Step) bool {
		x, ok := st.(*radosclient.SetXattrStep)
		return ok && x.Name == meta.AttrManifest
	})
}

// cancelAfter is an around fn that sends the op and then ends the request's
// context, as a client that leaves once the op is sent does.
func cancelAfter(cancel context.CancelFunc) func(sendFunc) (uint64, error) {
	return func(send sendFunc) (uint64, error) {
		v, err := send()
		cancel()
		return v, err
	}
}

// tailStripes is the oids of the stripes past the head that the manifest of
// head names.
func tailStripes(head *fakerados.Object) []string {
	GinkgoHelper()
	Expect(head).NotTo(BeNil(), "the head")
	m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
	stripes, err := m.Stripes()
	Expect(err).NotTo(HaveOccurred())
	var out []string
	for _, st := range stripes[1:] {
		out = append(out, st.OID())
	}
	Expect(out).NotTo(BeEmpty(), "the head names tails")
	return out
}

var _ = Describe("a versioned CopyObject", func() {
	var (
		c     *fakerados.Cluster
		ac    *aroundCluster
		s     *driver.Store
		rec   *op.BucketRecord
		clock atomic.Int64
		attrs map[string][]byte
		body  []byte
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	src := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "src", Instance: instance} }
	dst := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "dst", Instance: instance} }
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
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		attrs = map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice"))}
		body = bytes.Repeat([]byte("0123456789abcdef"), (10<<20)/16)
	})
	AfterEach(func() {
		ac.set(nil)
		settle(s)
	})

	// putVersion writes data as key under tag, its tails named with prefix.
	putVersion := func(ctx context.Context, key meta.ObjKey, data []byte, tag, prefix string) {
		GinkgoHelper()
		s.SetRandForTest(fixedRand(prefix))
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(data), op.PutParams{Attrs: attrs, Size: int64(len(data)), Tag: tag})
		Expect(err).NotTo(HaveOccurred(), "the put of %v", key)
		settle(s)
	}
	prefetch := func(ctx context.Context, key meta.ObjKey) *op.ObjectState {
		GinkgoHelper()
		st, err := s.PrefetchObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	copyTo := func(ctx context.Context, from *op.ObjectState, to meta.ObjKey, p op.CopyParams) (*op.PutResult, error) {
		if p.Attrs == nil {
			p.Attrs = attrs
		}
		if p.Tag == "" {
			p.Tag = "tx-copy"
		}
		s.SetRandForTest(fixedRand(secondPrefix))
		return s.CopyObject(ctx, from, rec, to, p)
	}
	head := func(key meta.ObjKey) *fakerados.Object { return c.Object(testDataPool, "", instanceHeadOID(key)) }
	target := func(name string) meta.ObjKey {
		GinkgoHelper()
		o := c.Object(testDataPool, "", olhHeadOID(meta.ObjKey{Name: name}))
		Expect(o).NotTo(BeNil(), "the OLH head of %s", name)
		d := denc.NewDecoder(o.Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred(), "the olh.info of %s", name)
		return info.Target.Key
	}
	pending := func(name string) []string {
		var out []string
		for n := range c.Object(testDataPool, "", olhHeadOID(meta.ObjKey{Name: name})).Xattrs {
			if strings.HasPrefix(n, meta.AttrOLHPendingPrefix) {
				out = append(out, n)
			}
		}
		return out
	}
	listed := func(name string) []string {
		var out []string
		for _, en := range c.ListEntries(rookIndexPool, "", indexShardOID(rec, name), name) {
			out = append(out, en.Key.Instance)
		}
		return out
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
	refTags := func(oid string) []string {
		GinkgoHelper()
		var out []string
		for t := range refsOf(c, testDataPool, oid).Refs {
			out = append(out, t)
		}
		return out
	}
	prepares := func(name string) []rgwcls.PrepareOp {
		var out []rgwcls.PrepareOp
		for _, w := range c.WritesTo(rookIndexPool, "", indexShardOID(rec, name)) {
			if callsMethod(w.Steps(), "bucket_prepare_op") {
				out = append(out, execIn(w, 2, "bucket_prepare_op", rgwcls.DecodePrepareOp))
			}
		}
		return out
	}

	It("copies a named noncurrent version into a new version of the destination, sharing its tails under the copy's tag", func(ctx SpecContext) {
		putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
		putVersion(ctx, src("v2"), []byte("two"), "tag-v2", reprefix)
		from := prefetch(ctx, src("v1"))
		Expect(from.Key).To(Equal(src("v1")))
		v1Tails := tailStripes(head(src("v1")))
		c.ResetCounters()

		res, err := copyTo(ctx, from, dst("c1"), op.CopyParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.PendingCompletionsForTest()).To(BeZero(), "the completion was awaited before the link")
		Expect(res.ETag).To(Equal(md5hex(body)))

		By("its version, current under the destination's OLH")
		Expect(target("dst")).To(Equal(dst("c1")))
		Expect(listed("dst")).To(Equal([]string{"c1"}))
		Expect(pending("dst")).To(BeEmpty())
		en, ok := c.InstanceEntry(rookIndexPool, "", indexShardOID(rec, "dst"), rgwcls.ObjKey{Name: "dst", Instance: "c1"})
		Expect(ok).To(BeTrue(), "the instance entry")
		Expect([]any{en.Flags, en.VersionedEpoch, en.Meta.ETag, en.Tag}).To(Equal([]any{rgwcls.FlagVer | rgwcls.FlagCurrent, uint64(2), md5hex(body), "tx-copy"}))
		Expect(prepares("dst")[0].BILogFlags).To(Equal(rgwcls.BILogFlagVersionedOp))

		By("the tails of the version it names, referenced under the tag its head carries")
		h := head(dst("c1"))
		Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-copy\x00")))
		Expect(h.Xattrs).To(HaveKeyWithValue(meta.AttrTailTag, []byte("tx-copy\x00")))
		Expect(tailStripes(h)).To(Equal(v1Tails), "the copy's manifest names v1's tails")
		for _, t := range v1Tails {
			Expect(refTags(t)).To(ConsistOf("", "tx-copy\x00"), "%s: v1's writer and the copy", t)
		}
		Expect(readBack(ctx, dst(""))).To(Equal(body), "the destination's current version reads v1's bytes")
		Expect(readBack(ctx, src(""))).To(Equal([]byte("two")), "the source keeps its current version")
	})

	It("copies the source's current version when it names none", func(ctx SpecContext) {
		putVersion(ctx, src("v1"), []byte("one"), "tag-v1", putPrefix)
		putVersion(ctx, src("v2"), body, "tag-v2", reprefix)
		from := prefetch(ctx, src(""))
		Expect(from.Key).To(Equal(src("v2")), "followed to the current version")
		_, err := copyTo(ctx, from, dst("c1"), op.CopyParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(target("dst")).To(Equal(dst("c1")))
		Expect(readBack(ctx, dst("c1"))).To(Equal(body))
	})

	It("makes each copy into an enabled bucket a new version, the last current", func(ctx SpecContext) {
		putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
		from := prefetch(ctx, src("v1"))
		_, err := copyTo(ctx, from, dst("c1"), op.CopyParams{Tag: "tx-c1"})
		Expect(err).NotTo(HaveOccurred())
		_, err = copyTo(ctx, from, dst("c2"), op.CopyParams{Tag: "tx-c2"})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed("dst")).To(Equal([]string{"c2", "c1"}))
		Expect(target("dst")).To(Equal(dst("c2")))
		Expect(head(dst("c1"))).NotTo(BeNil(), "an overwrite keeps the version it replaces")
	})

	It("streams a version into a new version when the destination's placement differs", func(ctx SpecContext) {
		putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
		from := prefetch(ctx, src("v1"))
		res, err := copyTo(ctx, from, dst("c1"), op.CopyParams{StorageClass: "COLD"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(md5hex(body)))
		Expect(target("dst")).To(Equal(dst("c1")))
		Expect(listed("dst")).To(Equal([]string{"c1"}))
		Expect(prepares("dst")[0].BILogFlags).To(Equal(rgwcls.BILogFlagVersionedOp))
		Expect(c.Object(coldPool, "", tailStripes(head(dst("c1")))[0])).NotTo(BeNil(), "the copy's own tails, in the destination's pool")
		Expect(readBack(ctx, dst(""))).To(Equal(body))
	})

	It("copies a key's null version into a version whose head carries no OLH attr, so its deletion leaves nothing", func(ctx SpecContext) {
		putVersion(ctx, src("v1"), []byte("one"), "tag-v1", putPrefix)
		rec.Info.Flags |= meta.BucketVersionsSuspended
		putVersion(ctx, src(""), body, "tag-null", reprefix)
		rec.Info.Flags &^= meta.BucketVersionsSuspended
		from := prefetch(ctx, src(""))
		Expect(from.Key).To(Equal(src("")), "the null version, its key's OLH head")
		Expect(from.Attrs).To(HaveKey(meta.AttrOLHInfo))
		_, err := copyTo(ctx, from, dst("c1"), op.CopyParams{})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		for name := range head(dst("c1")).Xattrs {
			Expect(name).NotTo(HavePrefix(meta.AttrOLHPrefix), "copy_obj keeps olh.idtag, olh.info and olh.ver on a version's head")
		}
		_, err = s.DeleteObject(ctx, rec, dst("c1"), op.DeleteParams{})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(head(dst("c1"))).To(BeNil(), "the removal keeps a head's OLH attrs, so a head carrying them would stay")
		st, err := s.StatObject(ctx, rec, dst("c1"))
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeFalse(), "a named read of the deleted version finds nothing")
	})

	It("answers NoSuchKey when the source's current version is a delete marker, writing nothing", func(ctx SpecContext) {
		putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
		link := driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}}
		Expect(s.SetOLHForTest(ctx, rec, src("dm"), link)).To(Succeed())
		from := prefetch(ctx, src(""))
		Expect([]any{from.Exists, from.DeleteMarker}).To(Equal([]any{false, true}))
		c.ResetCounters()
		_, err := copyTo(ctx, from, dst("c1"), op.CopyParams{})
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(c.Writes(rookIndexPool, "", indexShardOID(rec, "dst"))).To(BeZero())
		Expect(head(dst("c1"))).To(BeNil())
		for _, t := range tailStripes(head(src("v1"))) {
			Expect(c.Writes(testDataPool, "", t)).To(BeZero(), "no reference taken on %s", t)
		}
	})

	DescribeTable("keeps the tail references its head names when its link fails",
		func(ctx SpecContext, errno syscall.Errno, want error, removed bool) {
			putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
			from := prefetch(ctx, src("v1"))
			v1Tails := tailStripes(head(src("v1")))
			ac.set(onFirst("bucket_link_olh", failWith(errno)))
			_, err := copyTo(ctx, from, dst("c1"), op.CopyParams{})
			Expect(err).To(MatchError(want))
			settle(s)
			for _, t := range v1Tails {
				Expect(refTags(t)).To(ConsistOf("", "tx-copy\x00"), "%s keeps the copy's reference", t)
			}
			if !removed {
				Expect(head(dst("c1"))).NotTo(BeNil(), "the version stays, its link's outcome unknown")
				return
			}
			Expect(head(dst("c1"))).To(BeNil(), "the version is removed, its link refused")
			entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-copy\x00")))
			Expect(entries).To(HaveLen(1), "the removal queues the tails under the copy's tag, which drops its reference")
			Expect(chainOIDs(entries)).To(ConsistOf(v1Tails))
		},
		Entry("on a refusal, which removes the version", syscall.EIO, op.ErrUnknown, true),
		Entry("on a timeout, which leaves it", syscall.ETIMEDOUT, op.ErrRequestTimedOut, false),
	)

	DescribeTable("links its version though its client leaves once its head is written",
		func(ctx SpecContext, class string) {
			putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
			from := prefetch(ctx, src("v1"))
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ac.set(onFirstMatching(writesManifest, cancelAfter(cancel)))
			_, err := copyTo(cctx, from, dst("c1"), op.CopyParams{StorageClass: class})
			Expect(err).NotTo(HaveOccurred())
			Expect(cctx.Err()).To(MatchError(context.Canceled), "the client left")
			Expect(target("dst")).To(Equal(dst("c1")))
			Expect(pending("dst")).To(BeEmpty(), "no modification left to wait for rgw_olh_pending_timeout_sec")
		},
		Entry("a copy that shares the source's tails", ""),
		Entry("a copy that streams the source", "COLD"),
	)

	Describe("into a bucket whose versioning is suspended", func() {
		suspend := func() { rec.Info.Flags |= meta.BucketVersionsSuspended }

		It("writes the null version over the destination's OLH head", func(ctx SpecContext) {
			putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
			putVersion(ctx, dst("d1"), []byte("old"), "tag-d1", reprefix)
			suspend()
			_, err := copyTo(ctx, prefetch(ctx, src("v1")), dst(""), op.CopyParams{})
			Expect(err).NotTo(HaveOccurred())
			Expect(target("dst")).To(Equal(dst("")), "the null version is current")
			Expect(listed("dst")).To(Equal([]string{"", "d1"}))
			olh := c.Object(testDataPool, "", olhHeadOID(dst("")))
			Expect(olh.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-copy\x00")))
			Expect(readBack(ctx, dst(""))).To(Equal(body))
		})

		It("copies its key's current version onto the null version under references of its own, so the version keeps its tails", func(ctx SpecContext) {
			putVersion(ctx, src("v1"), body, "tag-v1", putPrefix)
			suspend()
			from := prefetch(ctx, src(""))
			Expect(from.Key).To(Equal(src("v1")), "followed to the version")
			v1Tails := tailStripes(head(src("v1")))
			_, err := copyTo(ctx, from, src(""), op.CopyParams{
				Attrs: map[string][]byte{meta.AttrACL: attrs[meta.AttrACL], meta.AttrMetaPrefix + "c": []byte("3\x00")}, ReplaceAttrs: true, Tag: "tx-self",
			})
			Expect(err).NotTo(HaveOccurred())

			By("the null version, current, naming v1's tails under its own tag")
			Expect(target("src")).To(Equal(src("")))
			null := c.Object(testDataPool, "", olhHeadOID(src("")))
			Expect(null.Xattrs).To(HaveKeyWithValue(meta.AttrTailTag, []byte("tx-self\x00")), "not v1's tail tag, which radosgw's copy_itself keeps")
			Expect(tailStripes(null)).To(Equal(v1Tails))
			for _, t := range v1Tails {
				Expect(refTags(t)).To(ConsistOf("", "tx-self\x00"), "%s: v1's writer and the null version", t)
			}

			By("an overwrite of the null version queuing those tails under its tag alone, which leaves v1's reference")
			putVersion(ctx, src(""), []byte("new"), "tag-new", reprefix)
			entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-self\x00")))
			Expect(entries).To(HaveLen(1))
			Expect(entries[0].Tag).To(Equal("tx-self\x00"))
			Expect(chainOIDs(entries)).To(ConsistOf(v1Tails))
			Expect(readBack(ctx, src("v1"))).To(Equal(body))
		})

		It("rewrites the current null version in place when copied onto itself, keeping its tails", func(ctx SpecContext) {
			putVersion(ctx, src("v1"), []byte("one"), "tag-v1", reprefix)
			suspend()
			putVersion(ctx, src(""), body, "tag-null", putPrefix)
			from := prefetch(ctx, src(""))
			Expect(from.Key).To(Equal(src("")), "the null version is the OLH head itself")
			nullTails := tailStripes(c.Object(testDataPool, "", olhHeadOID(src(""))))
			c.ResetCounters()
			_, err := copyTo(ctx, from, src(""), op.CopyParams{
				Attrs: map[string][]byte{meta.AttrACL: attrs[meta.AttrACL], meta.AttrMetaPrefix + "c": []byte("3\x00")}, ReplaceAttrs: true, Tag: "tx-self",
			})
			Expect(err).NotTo(HaveOccurred())
			for _, t := range nullTails {
				Expect(c.Writes(testDataPool, "", t)).To(BeZero(), "no reference change on %s", t)
			}
			null := c.Object(testDataPool, "", olhHeadOID(src("")))
			Expect(null.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-self\x00")))
			Expect(null.Xattrs).To(HaveKeyWithValue(meta.AttrTailTag, []byte("tag-null\x00")), "keep_tail")
			Expect(null.Xattrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"c", []byte("3\x00")))
			Expect(target("src")).To(Equal(src("")))
			en, ok := c.InstanceEntry(rookIndexPool, "", indexShardOID(rec, "src"), rgwcls.ObjKey{Name: "src"})
			Expect(ok).To(BeTrue())
			Expect([]any{en.Flags, en.VersionedEpoch, en.Tag}).To(Equal([]any{rgwcls.FlagVer | rgwcls.FlagCurrent, uint64(4), "tx-self"}), "linked again at the next epoch")
			Expect(allGCEntries(c)).To(BeEmpty(), "keep_tail: nothing queued")
			Expect(readBack(ctx, src(""))).To(Equal(body))
		})
	})
})
