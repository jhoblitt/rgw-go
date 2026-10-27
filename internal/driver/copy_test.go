package driver_test

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/snappy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// refsOf decodes the refcount xattr of a stored object.
func refsOf(c *fakerados.Cluster, pool, oid string) refcount.Refcount {
	GinkgoHelper()
	o := c.Object(pool, "", oid)
	Expect(o).NotTo(BeNil(), "%s exists", oid)
	b, ok := o.Xattrs[refcount.XattrName]
	Expect(ok).To(BeTrue(), "%s has a refcount", oid)
	return refcount.DecodeRefcount(denc.NewDecoder(b))
}

var _ = Describe("CopyObject", func() {
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		mtime  time.Time
		body   []byte
		srcKey meta.ObjKey
		srcSt  *op.ObjectState
		srcOID string
		tail1  string
		tail2  string
		dstKey meta.ObjKey
		dstOID string
		shard  string
	)
	aliceACL := func() []byte { return encode(acl.DefaultPolicy(alice, "Alice")) }
	// reqAttrs is the attrs a copy request carries: the destination ACL for
	// alice, then pairs of name and value, each value NUL-terminated.
	reqAttrs := func(kv ...string) map[string][]byte {
		attrs := map[string][]byte{meta.AttrACL: aliceACL()}
		for i := 0; i+1 < len(kv); i += 2 {
			attrs[kv[i]] = []byte(kv[i+1] + "\x00")
		}
		return attrs
	}
	putObject := func(ctx context.Context, key meta.ObjKey, data []byte, p op.PutParams) {
		GinkgoHelper()
		if p.Attrs == nil {
			p.Attrs = reqAttrs()
		}
		p.Size = int64(len(data))
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(data), p)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
	}
	// openStore opens a Store at rel with the options kv over the cluster
	// the source was written to, its random strings fixedRand(secondPrefix).
	openStore := func(ctx context.Context, rel denc.Release, kv map[string]string) {
		GinkgoHelper()
		s = openPutStore(ctx, c, rel, kv, mtime)
		s.SetRandForTest(fixedRand(secondPrefix))
	}
	BeforeEach(func(ctx SpecContext) {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		body = bytes.Repeat([]byte("0123456789abcdef"), (10<<20)/16)
		srcKey = meta.ObjKey{Name: "src"}
		srcOID = putBucketID + "_src"
		tail1, tail2 = tailOID(putPrefix, 1), tailOID(putPrefix, 2)
		dstKey = meta.ObjKey{Name: "dst"}
		dstOID = putBucketID + "_dst"
		shard = indexShardOID(rec, "dst")
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		putObject(ctx, srcKey, body, op.PutParams{
			Tag:   "tx-src",
			Attrs: reqAttrs(meta.AttrMetaPrefix+"a", "1", meta.AttrContentType, "text/plain"),
		})
		var err error
		srcSt, err = s.PrefetchObject(ctx, rec, srcKey)
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(fixedRand(secondPrefix))
	})
	readBack := func(ctx context.Context, key meta.ObjKey) []byte {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
		var out bytes.Buffer
		Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &out)).To(Succeed())
		return out.Bytes()
	}

	It("shares the tails through refcount under the new tag and copies only the head", func(ctx SpecContext) {
		c.ResetCounters()
		release := make(chan struct{})
		indexWrites := 0
		c.BeforeWrite(rookIndexPool, "", shard, func(*fakerados.Object) {
			indexWrites++
			if indexWrites == 2 {
				<-release
			}
		})
		res, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(1), "the prepare; CopyObject returned with its completion still held")
		close(release)
		settle(s)
		Expect(res.ETag).To(Equal(srcSt.ETag))
		Expect(res.Size).To(BeEquivalentTo(10 << 20))
		Expect(res.Mtime).To(Equal(mtime))
		for _, t := range []string{tail1, tail2} {
			w := c.LastWrite(testDataPool, "", t)
			Expect(w.Steps()).To(HaveLen(1))
			Expect(execIn(w, 0, "get", refcount.DecodeGetOp)).To(Equal(refcount.GetOp{Tag: "tx-copy\x00", ImplicitRef: true}),
				"rgw_rados.cc:4919-4922: the tag NUL-terminated, implicit")
			rc := refsOf(c, testDataPool, t)
			Expect(rc.Refs).To(HaveKey(""), "the wildcard for the tail's writer")
			Expect(rc.Refs).To(HaveKey("tx-copy\x00"))
		}
		Expect(c.Writes(testDataPool, "", srcOID)).To(BeZero(), "the source head is not touched")
		Expect(c.Reads(testDataPool, "", srcOID)).To(BeZero(), "copy_first served from PrefetchObject's Head")
		head := c.Object(testDataPool, "", dstOID)
		Expect(head.Data).To(Equal(body[:4<<20]))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx-copy\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-copy\x00")), "modify_tail on a copy that is not onto itself")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte(srcSt.ETag)))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-a", []byte("1\x00")), "COPY keeps the source metadata")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.content_type", []byte("text/plain\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.acl", aliceACL()))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.Obj).To(Equal(meta.Obj{Bucket: rec.Info.Bucket, Key: dstKey}))
		Expect(m.HeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.ObjSize).To(BeEquivalentTo(10 << 20))
		Expect(m.Prefix).To(Equal(srcSt.Manifest.Prefix), "the tails are the source's")
		Expect(m.TailPlacement.Bucket).To(Equal(rec.Info.Bucket))
		Expect(c.Writes(testDataPool, "", dstOID)).To(Equal(1), "the exclusive create of the assume-no-entry pass")
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "the prepare and the complete")
		prep := execIn(c.WritesTo(rookIndexPool, "", shard)[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
		Expect(prep.Tag).To(Equal("tx-copy"))
		en, ok := c.Entry(rookIndexPool, "", shard, "dst")
		Expect(ok).To(BeTrue())
		Expect(en.Meta.Size).To(BeEquivalentTo(10 << 20))
		Expect(en.Meta.AccountedSize).To(BeEquivalentTo(10 << 20))
		Expect(en.Meta.ETag).To(Equal(srcSt.ETag))
		Expect(readBack(ctx, dstKey)).To(Equal(body))
	})

	It("replaces the metadata when asked and keeps the source's etag", func(ctx SpecContext) {
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{
			Attrs: reqAttrs(meta.AttrMetaPrefix+"b", "2", meta.AttrContentType, "image/png"), ReplaceAttrs: true, Tag: "tx-copy",
		})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(testDataPool, "", dstOID)
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.x-amz-meta-a"))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-b", []byte("2\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.content_type", []byte("image/png\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte(srcSt.ETag)), "set_copy_attrs REPLACE: the source's etag when the request has none")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-copy\x00")), "the source's tail tag dropped, then the copy's set")
	})

	It("rewrites only the head when copying onto itself with REPLACE", func(ctx SpecContext) {
		c.ResetCounters()
		_, err := s.CopyObject(ctx, srcSt, rec, srcKey, op.CopyParams{Attrs: reqAttrs(meta.AttrMetaPrefix+"c", "3"), ReplaceAttrs: true, Tag: "tx-self"})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Writes(testDataPool, "", tail1)).To(BeZero(), "no refcount change, rgw_rados.cc:4910 and :4955-4957")
		Expect(c.Writes(testDataPool, "", tail2)).To(BeZero())
		head := c.Object(testDataPool, "", srcOID)
		Expect(head.Data).To(Equal(body[:4<<20]))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx-self\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-src\x00")), "keep_tail: the tails stay under the old tag, :4980-4981")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-c", []byte("3\x00")))
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.x-amz-meta-a"))
		Expect(c.Object(testDataPool, "", tail1)).NotTo(BeNil(), "keep_tail: nothing was queued for GC")
		for i := range 32 {
			Expect(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", i))).To(BeEmpty())
		}
		Expect(c.Writes(testDataPool, "", srcOID)).To(Equal(1), "one write, guarded on the head it read; no exclusive create")
		Expect(c.Reads(testDataPool, "", srcOID)).To(BeZero(), "and no read of its own")
		Expect(c.LastWrite(testDataPool, "", srcOID).Steps()[0]).
			To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-src\x00")}))
		Expect(readBack(ctx, srcKey)).To(Equal(body))
	})

	It("takes references for a source naming the null version copied onto its plain key, as copy_obj compares the keys as named", func(ctx SpecContext) {
		nullSt, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "src", Instance: meta.NullInstance})
		Expect(err).NotTo(HaveOccurred())
		Expect(nullSt.Exists).To(BeTrue())
		_, err = s.CopyObject(ctx, nullSt, rec, srcKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		for _, t := range []string{tail1, tail2} {
			Expect(refsOf(c, testDataPool, t).Refs).To(HaveKey("tx-copy\x00"), "rgw_rados.cc:4922 at v19.2.7: {src, null} is not {src, \"\"}")
		}
		head := c.Object(testDataPool, "", srcOID)
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-copy\x00")))
		gcOID := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-src\x00"))
		Expect(c.GCEntries(gcPoolName, gcNS, gcOID)).To(HaveLen(1), "the overwrite queued the tails under the old tag")
		Expect(s.GCWorkerForTest().Process(ctx, false)).To(Succeed())
		Expect(readBack(ctx, srcKey)).To(Equal(body), "the copy's references keep the tails the GC put")
	})

	It("fails a copy onto itself whose head was overwritten since it was read, and leaves the new object", func(ctx SpecContext) {
		s.SetRandForTest(fixedRand("NEWNEWNEWNEWNEWNEWNEWNEWNEWNEW1"))
		newBody := bytes.Repeat([]byte("n"), 6<<20)
		putObject(ctx, srcKey, newBody, op.PutParams{Tag: "tx-new"})
		_, err := s.CopyObject(ctx, srcSt, rec, srcKey, op.CopyParams{Attrs: reqAttrs(meta.AttrMetaPrefix+"c", "3"), ReplaceAttrs: true, Tag: "tx-self"})
		Expect(err).To(MatchError(op.ErrConcurrentModification), "ECANCELED, rgw_common.cc:141")
		head := c.Object(testDataPool, "", srcOID)
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx-new\x00")), "the PUT's head stands")
		Expect(readBack(ctx, srcKey)).To(Equal(newBody))
		gcOID := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-src\x00"))
		Expect(c.GCEntries(gcPoolName, gcNS, gcOID)).To(HaveLen(1), "the PUT queued the old tails, which no head may name again")
	})

	It("fails a copy onto itself whose head was deleted since it was read, and leaves it deleted", func(ctx SpecContext) {
		Expect(s.DeleteObject(ctx, rec, srcKey, op.DeleteParams{})).Error().To(Succeed())
		settle(s)
		_, err := s.CopyObject(ctx, srcSt, rec, srcKey, op.CopyParams{Attrs: reqAttrs(meta.AttrMetaPrefix+"c", "3"), ReplaceAttrs: true, Tag: "tx-self"})
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(c.Object(testDataPool, "", srcOID)).To(BeNil(), "no head resurrected on tails queued for the GC")
		settle(s)
		_, ok := c.Entry(rookIndexPool, "", indexShardOID(rec, "src"), "src")
		Expect(ok).To(BeFalse(), "the copy's index entry was canceled")
	})

	It("streams the data when the destination lives in another pool", func(ctx SpecContext) {
		c.ResetCounters()
		res, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), StorageClass: "COLD", Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(srcSt.ETag), "copy_obj_data keeps the source etag")
		Expect(res.Size).To(BeEquivalentTo(10 << 20))
		Expect(c.Object(coldPool, "", tailOID(secondPrefix, 0)).Data).To(Equal(body[:4<<20]), "no head data: stripes from 0")
		Expect(c.Object(coldPool, "", tailOID(secondPrefix, 1)).Data).To(Equal(body[4<<20 : 8<<20]))
		Expect(c.Object(coldPool, "", tailOID(secondPrefix, 2)).Data).To(Equal(body[8<<20:]))
		head := c.Object(testDataPool, "", dstOID)
		Expect(head.Data).To(BeEmpty())
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("_"+secondPrefix+"\x00")), "copy_obj_data's random tag, :5050-5051")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("_"+secondPrefix+"\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.storage_class", []byte("COLD")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte(srcSt.ETag)))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-a", []byte("1\x00")))
		Expect(c.Writes(testDataPool, "", tail1)).To(BeZero(), "no refcount get across pools")
		Expect(c.Writes(testDataPool, "", tail2)).To(BeZero())
		settle(s)
		en, ok := c.Entry(rookIndexPool, "", shard, "dst")
		Expect(ok).To(BeTrue())
		Expect(en.Tag).To(Equal("_" + secondPrefix))
		Expect(en.Meta.StorageClass).To(Equal("COLD"))
		Expect(readBack(ctx, dstKey)).To(Equal(body))
	})

	It("streams the data for a source held within its head", func(ctx SpecContext) {
		small := bytes.Repeat([]byte("s"), 1024)
		smallKey := meta.ObjKey{Name: "small"}
		putObject(ctx, smallKey, small, op.PutParams{Tag: "tx-small"})
		st, err := s.PrefetchObject(ctx, rec, smallKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Manifest.HasTail()).To(BeFalse())
		c.ResetCounters()
		res, err := s.CopyObject(ctx, st, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(st.ETag))
		head := c.Object(testDataPool, "", dstOID)
		Expect(head.Data).To(Equal(small), "!has_tail: copy_data, the bytes in the new head")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("_"+secondPrefix+"\x00")))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.Prefix).To(Equal("." + secondPrefix + "_"))
		Expect(m.HasTail()).To(BeFalse())
		Expect(poolObjects(ctx, c, testDataPool, "")).To(ConsistOf(srcOID, tail1, tail2, putBucketID+"_small", dstOID), "no tail was written")
		Expect(c.Writes(testDataPool, "", tail1)).To(BeZero())
		Expect(c.Reads(testDataPool, "", putBucketID+"_small")).To(BeZero(), "the head's bytes came from the prefetch")
	})

	It("copies a compressed source's stored bytes and compression info across pools, as copy_obj_data does", func(ctx SpecContext) {
		plain := bytes.Repeat([]byte("compressible "), 400000)
		comp := snappy.Encode(nil, plain)
		ci := meta.NewCompressionInfo()
		ci.Type, ci.OrigSize = "snappy", uint64(len(plain))
		ci.Blocks = []meta.CompressionBlock{{OldOfs: 0, NewOfs: 0, Len: uint64(len(comp))}}
		csrc := seedCompressedSource(ctx, c, s, rec, "csrc", comp, ci)
		res, err := s.CopyObject(ctx, csrc, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), StorageClass: "COLD", Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal("e-csrc"), "copy_obj_data keeps the source etag")
		head := c.Object(testDataPool, "", dstOID)
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.compression", encode(ci)), "the source's compression info, rgw_rados.cc:4791-4793")
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.ObjSize).To(BeEquivalentTo(len(comp)), "the stored size: the bytes were copied undecoded")
		Expect(c.Object(coldPool, "", tailOID(secondPrefix, 0)).Data).To(Equal(comp), "the destination class's pool holds the stored bytes")
		dst, err := s.StatObject(ctx, rec, dstKey)
		Expect(err).NotTo(HaveOccurred())
		var stored bytes.Buffer
		Expect(s.ReadStoredForTest(ctx, dst, 0, dst.Size, &stored)).To(Succeed())
		Expect(stored.Bytes()).To(Equal(comp), "Read::read's bytes, rgw_rados.cc:5067")
		var back bytes.Buffer
		Expect(s.ReadObject(ctx, dst, op.ByteRange{Length: ci.OrigSize}, &back)).To(Succeed())
		Expect(back.Bytes()).To(Equal(plain))
		settle(s)
		en, ok := c.Entry(rookIndexPool, "", shard, "dst")
		Expect(ok).To(BeTrue())
		Expect(en.Meta.Size).To(BeEquivalentTo(len(comp)))
		Expect(en.Meta.AccountedSize).To(BeEquivalentTo(len(plain)), "accounted_size = orig_size, rgw_rados.cc:5098-5108")
	})

	It("shares a compressed source's tails and accounts its original size", func(ctx SpecContext) {
		stored := bytes.Repeat([]byte("z"), 6<<20)
		ci := meta.NewCompressionInfo()
		ci.Type, ci.OrigSize = "snappy", 9<<20
		ci.Blocks = []meta.CompressionBlock{{Len: 6 << 20}}
		csrc := seedCompressedSource(ctx, c, s, rec, "csrc", stored, ci)
		Expect(csrc.Manifest.HasTail()).To(BeTrue())
		_, err := s.CopyObject(ctx, csrc, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(testDataPool, "", dstOID)
		Expect(head.Data).To(Equal(stored[:4<<20]), "the head's stored bytes, not decoded")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.compression", encode(ci)))
		settle(s)
		en, ok := c.Entry(rookIndexPool, "", shard, "dst")
		Expect(ok).To(BeTrue())
		Expect(en.Meta.Size).To(BeEquivalentTo(6 << 20))
		Expect(en.Meta.AccountedSize).To(BeEquivalentTo(9<<20), "astate->accounted_size, rgw_rados.cc:4983")
	})

	DescribeTable("refuses an encrypted source with NotImplemented and writes nothing",
		func(ctx SpecContext, rel denc.Release) {
			openStore(ctx, rel, nil)
			c.Object(testDataPool, "", srcOID).Xattrs[meta.AttrCryptMode] = []byte("SSE-C-AES256")
			st, err := s.PrefetchObject(ctx, rec, srcKey)
			Expect(err).NotTo(HaveOccurred())
			c.ResetCounters()
			_, err = s.CopyObject(ctx, st, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
			Expect(err).To(MatchError(op.ErrNotImplemented))
			Expect(c.Object(testDataPool, "", dstOID)).To(BeNil())
			Expect(c.Writes(testDataPool, "", tail1)).To(BeZero())
			Expect(c.Writes(testDataPool, "", tail2)).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
		},
		Entry("Squid, as rgw_rados.cc:4755-4763 refuses it", denc.Squid),
		Entry("Tentacle, which would decrypt it", denc.Tentacle),
	)

	// classCase is a copy between storage classes: a 5 MiB source PUT under
	// srcClass, its head relabeled label when that is set, copied under
	// reqClass, with REPLACE when replace; shared is whether the copy shares
	// the source's tails, and want is the destination's storage class attr,
	// empty for none, which GetObject and HeadObject report as STANDARD.
	type classCase struct {
		srcClass, label, reqClass string
		replace, shared           bool
		want                      string
	}
	DescribeTable("labels a copy with its destination placement's storage class on both releases",
		func(ctx SpecContext, rel denc.Release, cc classCase) {
			openStore(ctx, rel, nil)
			key := meta.ObjKey{Name: "classed"}
			putObject(ctx, key, bytes.Repeat([]byte("c"), 5<<20), op.PutParams{Tag: "tx-classed", StorageClass: cc.srcClass})
			if cc.label != "" {
				c.Object(testDataPool, "", putBucketID+"_classed").Xattrs[meta.AttrStorageClass] = []byte(cc.label)
			}
			st, err := s.PrefetchObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			if label := cmp.Or(cc.label, cc.srcClass); label != "" {
				Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrStorageClass, []byte(label)), "the source's label")
			}
			s.SetRandForTest(fixedRand("THIRDTHIRDTHIRDTHIRDTHIRDTHIRD3"))
			_, err = s.CopyObject(ctx, st, rec, dstKey, op.CopyParams{
				Attrs: reqAttrs(), ReplaceAttrs: cc.replace, StorageClass: cc.reqClass, Tag: "tx-copy",
			})
			Expect(err).NotTo(HaveOccurred())
			srcTailPool := testDataPool
			if cc.srcClass != "" {
				srcTailPool = coldPool
			}
			if cc.shared {
				Expect(refsOf(c, srcTailPool, tailOID(secondPrefix, 1)).Refs).To(HaveKey("tx-copy\x00"), "the copy shares the source's tails")
			} else {
				Expect(c.Object(srcTailPool, "", tailOID(secondPrefix, 1)).Xattrs).NotTo(HaveKey(refcount.XattrName), "the copy streams its data")
			}
			head := c.Object(testDataPool, "", dstOID)
			dst, err := s.StatObject(ctx, rec, dstKey)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			en, ok := c.Entry(rookIndexPool, "", shard, "dst")
			Expect(ok).To(BeTrue())
			if cc.want == "" {
				Expect(head.Xattrs).NotTo(HaveKey(meta.AttrStorageClass), "no label: STANDARD")
			} else {
				Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrStorageClass, []byte(cc.want)))
			}
			Expect(dst.StorageClass).To(Equal(cc.want), "the class GetObject and HeadObject report")
			Expect(en.Meta.StorageClass).To(Equal(cc.want), "the class a listing reports")
		},
		Entry("Squid: COLD to the default class reports STANDARD, where its radosgw keeps COLD",
			denc.Squid, classCase{srcClass: "COLD"}),
		Entry("Tentacle: COLD to the default class reports STANDARD, v20.2.4 rgw_rados.cc:5028",
			denc.Tentacle, classCase{srcClass: "COLD"}),
		Entry("Squid: COLD to the default class with REPLACE reports STANDARD",
			denc.Squid, classCase{srcClass: "COLD", replace: true}),
		Entry("Tentacle: COLD to the default class with REPLACE reports STANDARD",
			denc.Tentacle, classCase{srcClass: "COLD", replace: true}),
		Entry("Squid: COLD to an explicit STANDARD is labeled STANDARD",
			denc.Squid, classCase{srcClass: "COLD", reqClass: "STANDARD", want: "STANDARD"}),
		Entry("Tentacle: COLD to an explicit STANDARD is labeled STANDARD",
			denc.Tentacle, classCase{srcClass: "COLD", reqClass: "STANDARD", want: "STANDARD"}),
		Entry("Squid: a STANDARD source labeled COLD shares its tails and drops the label",
			denc.Squid, classCase{label: "COLD", shared: true}),
		Entry("Tentacle: a STANDARD source labeled COLD shares its tails and drops the label",
			denc.Tentacle, classCase{label: "COLD", shared: true}),
		Entry("Squid: STANDARD to COLD reports COLD",
			denc.Squid, classCase{reqClass: "COLD", want: "COLD"}),
		Entry("Tentacle: STANDARD to COLD reports COLD",
			denc.Tentacle, classCase{reqClass: "COLD", want: "COLD"}),
		Entry("Squid: COLD to COLD shares its tails and stays COLD",
			denc.Squid, classCase{srcClass: "COLD", reqClass: "COLD", shared: true, want: "COLD"}),
		Entry("Tentacle: COLD to COLD shares its tails and stays COLD",
			denc.Tentacle, classCase{srcClass: "COLD", reqClass: "COLD", shared: true, want: "COLD"}),
	)

	It("drops the references it took when a tail is gone, and keeps the tails' own", func(ctx SpecContext) {
		openStore(ctx, denc.Squid, map[string]string{"rgw_max_copy_obj_concurrent_io": "1"})
		c.ResetCounters()
		c.BeforeWrite(testDataPool, "", tail2, func(*fakerados.Object) { c.Remove(testDataPool, "", tail2) })
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).To(MatchError(op.ErrNoSuchKey), "the get's ENOENT, rgw_rados.cc:4938-4943")
		w := c.LastWrite(testDataPool, "", tail1)
		Expect(execIn(w, 0, "put", refcount.DecodePutOp)).To(Equal(refcount.PutOp{Tag: "tx-copy\x00", ImplicitRef: true}), "done_ret: refcount put on what succeeded, :4990-5031")
		Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), ":5009")
		rc := refsOf(c, testDataPool, tail1)
		Expect(rc.Refs).NotTo(HaveKey("tx-copy\x00"))
		Expect(rc.Refs).To(HaveKey(""), "the source's own reference stands")
		Expect(c.Object(testDataPool, "", dstOID)).To(BeNil())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "the references precede the prepare")
	})

	It("rolls the references back under full-try when a tail refuses, and puts none on the tail that refused", func(ctx SpecContext) {
		c.ResetCounters()
		c.FailNextWrite(testDataPool, "", tail2, syscall.EIO)
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).To(MatchError(op.ErrUnknown), "EIO, which no S3 error names")
		w := c.LastWrite(testDataPool, "", tail1)
		Expect(execIn(w, 0, "put", refcount.DecodePutOp)).To(Equal(refcount.PutOp{Tag: "tx-copy\x00", ImplicitRef: true}), "done_ret: refcount put on what succeeded, :4990-5031")
		Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), ":5009")
		Expect(refsOf(c, testDataPool, tail1).Refs).To(Equal(map[string]bool{"": true}), "the source's own reference stands")
		Expect(c.Writes(testDataPool, "", tail2)).To(Equal(1), "the refused get, and no put: :5001-5003 skips errors")
		Expect(c.Object(testDataPool, "", tail2).Data).To(Equal(body[8<<20:]), "a put of a tag never taken would have dropped the writer's implicit reference and removed the tail")
		Expect(c.Object(testDataPool, "", tail2).Xattrs).NotTo(HaveKey(refcount.XattrName))
		Expect(c.Object(testDataPool, "", dstOID)).To(BeNil())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero(), "the references precede the prepare")
		Expect(readBack(ctx, srcKey)).To(Equal(body))
	})

	It("drops the references it took when its client leaves, and takes no more", func(ctx SpecContext) {
		reqCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		// The client leaves as tail1's get is sent: a get made under the
		// request's context fails the pool's context check and is never
		// recorded, while one under the driver's context lands.
		wc := &cancelingCluster{Cluster: c, oid: tail1, cancel: cancel}
		rel := denc.Squid
		var err error
		s, err = driver.Open(ctx, wc, conf(map[string]string{"rgw_max_copy_obj_concurrent_io": "1"}), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, func() time.Time { return mtime })
		c.ResetCounters()
		_, err = s.CopyObject(reqCtx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).To(MatchError(context.Canceled))
		gets := c.WritesTo(testDataPool, "", tail1)
		Expect(gets).To(HaveLen(2), "the get, which ran to its end under the driver's context, then the put")
		execIn(gets[0], 0, "get", refcount.DecodeGetOp)
		execIn(gets[1], 0, "put", refcount.DecodePutOp)
		Expect(refsOf(c, testDataPool, tail1).Refs).To(Equal(map[string]bool{"": true}))
		Expect(c.Writes(testDataPool, "", tail2)).To(BeZero(), "no get after the client left")
		Expect(c.Object(testDataPool, "", dstOID)).To(BeNil())
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("keeps the references when the head write times out, as the head may yet land", func(ctx SpecContext) {
		c.ResetCounters()
		c.FailNextWrite(testDataPool, "", dstOID, syscall.ETIMEDOUT)
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).To(MatchError(op.ErrRequestTimedOut))
		for _, t := range []string{tail1, tail2} {
			Expect(refsOf(c, testDataPool, t).Refs).To(HaveKey("tx-copy\x00"), "where radosgw's done_ret drops it")
			Expect(c.Writes(testDataPool, "", t)).To(Equal(1), "the get, and no put")
		}
		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(1), "no cancel after ETIMEDOUT: the entry stays pending (rgw_rados.cc:3370-3374)")
	})

	It("drops the references when its head write loses a race, and answers success", func(ctx SpecContext) {
		putObject(ctx, dstKey, []byte("first"), op.PutParams{Tag: "tx-1"})
		writes := 0
		c.BeforeWrite(testDataPool, "", dstOID, func(o *fakerados.Object) {
			writes++
			if writes == 2 && o != nil {
				o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00")
			}
		})
		res, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred(), "ECANCELED without conditions is success, rgw_rados.cc:3388-3391")
		Expect(res.ETag).To(Equal(srcSt.ETag))
		Expect(c.Object(testDataPool, "", dstOID).Xattrs["user.rgw.idtag"]).To(Equal([]byte("tx-racer\x00")), "the racer's head stands")
		for _, t := range []string{tail1, tail2} {
			w := c.LastWrite(testDataPool, "", t)
			Expect(execIn(w, 0, "put", refcount.DecodePutOp)).To(Equal(refcount.PutOp{Tag: "tx-copy\x00", ImplicitRef: true}),
				"no head names the tails under tx-copy, where radosgw keeps the reference")
			Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry))
			Expect(refsOf(c, testDataPool, t).Refs).To(Equal(map[string]bool{"": true}))
		}
	})

	It("overwrites an existing destination through the EEXIST retry and queues its old tails", func(ctx SpecContext) {
		s.SetRandForTest(fixedRand("OLDOLDOLDOLDOLDOLDOLDOLDOLDOLD1"))
		putObject(ctx, dstKey, bytes.Repeat([]byte("o"), 6<<20), op.PutParams{Tag: "tx-old"})
		oldTail := tailOID("OLDOLDOLDOLDOLDOLDOLDOLDOLDOLD1", 1)
		Expect(c.Object(testDataPool, "", oldTail)).NotTo(BeNil())
		c.ResetCounters()
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Writes(testDataPool, "", dstOID)).To(Equal(2), "the exclusive create fails EEXIST, then the guarded write")
		Expect(c.Reads(testDataPool, "", dstOID)).To(Equal(1), "one raw_obj_stat between them")
		Expect(c.LastWrite(testDataPool, "", dstOID).Steps()[0]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-old\x00")}))
		gcOID := fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-old\x00"))
		entries := c.GCEntries(gcPoolName, gcNS, gcOID)
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal("tx-old\x00"))
		Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: testDataPool, Key: rgwcls.ObjKey{Name: oldTail}}}))
		for _, t := range []string{tail1, tail2} {
			Expect(refsOf(c, testDataPool, t).Refs).To(HaveKey("tx-copy\x00"))
		}
		Expect(readBack(ctx, dstKey)).To(Equal(body))
	})

	It("shares tails with rgw_max_copy_obj_concurrent_io 0 used as 1, where radosgw fails with EDEADLK", func(ctx SpecContext) {
		openStore(ctx, denc.Squid, map[string]string{"rgw_max_copy_obj_concurrent_io": "0"})
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		for _, t := range []string{tail1, tail2} {
			Expect(refsOf(c, testDataPool, t).Refs).To(HaveKey("tx-copy\x00"))
		}
		Expect(readBack(ctx, dstKey)).To(Equal(body))
	})

	It("references the first stripe of a source whose head holds no data", func(ctx SpecContext) {
		coldKey := meta.ObjKey{Name: "cold"}
		coldBody := bytes.Repeat([]byte("c"), 5<<20)
		putObject(ctx, coldKey, coldBody, op.PutParams{Tag: "tx-cold", StorageClass: "COLD"})
		st, err := s.PrefetchObject(ctx, rec, coldKey)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Manifest.HeadSize).To(BeZero())
		c.ResetCounters()
		_, err = s.CopyObject(ctx, st, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), StorageClass: "COLD", Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		for _, n := range []int{0, 1} {
			t := tailOID(secondPrefix, n)
			execIn(c.LastWrite(coldPool, "", t), 0, "get", refcount.DecodeGetOp)
			Expect(refsOf(c, coldPool, t).Refs).To(HaveKey("tx-copy\x00"), "stripe %d is a tail: no copy_first skips it", n)
		}
		Expect(c.Object(testDataPool, "", dstOID).Data).To(BeEmpty(), "set_head with 0 bytes")
		Expect(readBack(ctx, dstKey)).To(Equal(coldBody))
	})

	It("answers NoSuchKey for a missing source", func(ctx SpecContext) {
		_, err := s.CopyObject(ctx, &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: "gone"}}, rec, dstKey, op.CopyParams{Attrs: reqAttrs()})
		Expect(err).To(MatchError(op.ErrNoSuchKey))
	})

	It("drops the source's object-lock, delete-at, replication and write attrs and takes the request's lock attrs", func(ctx SpecContext) {
		st := *srcSt
		st.Attrs = maps.Clone(srcSt.Attrs)
		for _, n := range []string{
			meta.AttrDeleteAt, meta.AttrObjectRetention, meta.AttrObjectLegalHold, meta.AttrOLHIDTag, meta.AttrOLHInfo,
			meta.AttrReplicationTrace, meta.AttrReplicatedAt, meta.AttrReplicationStatus,
		} {
			st.Attrs[n] = []byte("src")
		}
		attrs := reqAttrs()
		attrs[meta.AttrObjectLegalHold] = []byte("req")
		_, err := s.CopyObject(ctx, &st, rec, dstKey, op.CopyParams{Attrs: attrs, Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(testDataPool, "", dstOID)
		for _, n := range []string{
			meta.AttrDeleteAt, meta.AttrObjectRetention, meta.AttrOLHIDTag, meta.AttrOLHInfo,
			meta.AttrReplicationTrace, meta.AttrReplicatedAt, meta.AttrReplicationStatus,
		} {
			Expect(head.Xattrs).NotTo(HaveKey(n))
		}
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, []byte("req")))
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrSourceZone, []byte{0x39, 0x30, 0, 0}), "this zone's, not the source's")
	})

	DescribeTable("drops every OLH attr of its source, pending ones included, wherever the copy goes",
		func(ctx SpecContext, flags uint32, instance string) {
			st := *srcSt
			st.Attrs = maps.Clone(srcSt.Attrs)
			olh := []string{meta.AttrOLHIDTag, meta.AttrOLHInfo, meta.AttrOLHVer}
			for _, n := range olh {
				st.Attrs[n] = []byte("src")
			}
			pendingAttr := meta.AttrOLHPendingPrefix + "src-op"
			st.Attrs[pendingAttr] = []byte("src")
			dst := *rec
			dst.Info.Flags |= flags
			key := meta.ObjKey{Name: "dst", Instance: instance}
			_, err := s.CopyObject(ctx, &st, &dst, key, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
			Expect(err).NotTo(HaveOccurred())
			head := c.Object(testDataPool, "", putBucketID+"_"+key.OID())
			for _, n := range olh {
				Expect(head.Xattrs).NotTo(HaveKeyWithValue(n, []byte("src")), n)
			}
			Expect(head.Xattrs).NotTo(HaveKey(pendingAttr), "the source's modification in flight")
		},
		Entry("an unversioned bucket", uint32(0), ""),
		Entry("a bucket whose versioning is suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), ""),
		Entry("a version's own head in a bucket whose versioning is enabled, where copy_obj keeps them", uint32(meta.BucketVersioned), "c1"),
		Entry("the null version in a bucket whose versioning is enabled, whose head is its key's OLH head", uint32(meta.BucketVersioned), ""),
	)

	It("streams a source written without a manifest", func(ctx SpecContext) {
		bare := []byte(strings.Repeat("b", 100))
		c.Put(testDataPool, "", putBucketID+"_bare", bare)
		o := c.Object(testDataPool, "", putBucketID+"_bare")
		o.Xattrs[meta.AttrETag] = []byte("e-bare")
		o.Xattrs[meta.AttrACL] = aliceACL()
		st, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "bare"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Manifest).To(BeNil())
		res, err := s.CopyObject(ctx, st, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal("e-bare"))
		Expect(readBack(ctx, dstKey)).To(Equal(bare))
	})
})

// cancelingCluster is c with pools that call cancel just before each write to
// oid reaches the fake, ahead of its context check.
type cancelingCluster struct {
	*fakerados.Cluster
	oid    string
	cancel context.CancelFunc
}

func (c *cancelingCluster) Pool(ctx context.Context, pool, ns string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, ns)
	if err != nil {
		return nil, err
	}
	return &cancelingPool{Pool: p, c: c}, nil
}

// cancelingPool is a pool of a cancelingCluster.
type cancelingPool struct {
	radosclient.Pool
	c *cancelingCluster
}

func (p *cancelingPool) Write(ctx context.Context, oid string, w *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if oid == p.c.oid {
		p.c.cancel()
	}
	return p.Pool.Write(ctx, oid, w, flags)
}

// seedCompressedSource writes the head <marker>_<name> holding stored, with
// the etag "e-<name>", alice's ACL, a trivial manifest of len(stored) bytes
// in 4 MiB head and stripe chunks, and ci as its compression info; a source
// longer than the head chunk gets the rest as its first tail. It returns the
// prefetched state.
func seedCompressedSource(ctx context.Context, c *fakerados.Cluster, s *driver.Store, rec *op.BucketRecord, name string, stored []byte, ci meta.CompressionInfo) *op.ObjectState {
	GinkgoHelper()
	key := meta.ObjKey{Name: name}
	rule := meta.PlacementRule{Name: "default-placement"}
	m := meta.NewTrivialManifest(meta.Obj{Bucket: rec.Info.Bucket, Key: key}, rule, rule, ".comp_", 4<<20, 4<<20)
	m.SetObjSize(uint64(len(stored)))
	head := stored[:m.HeadSize]
	c.Put(testDataPool, "", rec.Info.Bucket.Marker+"_"+name, head)
	o := c.Object(testDataPool, "", rec.Info.Bucket.Marker+"_"+name)
	o.Xattrs[meta.AttrETag] = []byte("e-" + name)
	o.Xattrs[meta.AttrACL] = encode(acl.DefaultPolicy(alice, "Alice"))
	o.Xattrs[meta.AttrManifest] = encode(m)
	o.Xattrs[meta.AttrCompression] = encode(ci)
	o.Xattrs[meta.AttrIDTag] = []byte("tx-" + name + "\x00")
	if rest := stored[m.HeadSize:]; len(rest) > 0 {
		c.Put(testDataPool, "", meta.Stripe{Obj: m.TailObj(1)}.OID(), rest)
	}
	st, err := s.PrefetchObject(ctx, rec, key)
	Expect(err).NotTo(HaveOccurred())
	return st
}

// snappyClass and zlibClass are the PUT fixture's classes that compress with
// snappy and zlib, once withCompressionClasses has made them.
var snappyClass, zlibClass = compClass(compression.Snappy), compClass(compression.Zlib)

// compositeFlags are the flags of a checksum combined as a composite.
const compositeFlags = cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite

var _ = Describe("a copy's compression and checksum records", func() {
	var (
		c      *fakerados.Cluster
		s      *driver.Store
		rec    *op.BucketRecord
		mtime  time.Time
		plain  []byte
		srcKey meta.ObjKey
		srcOID string
		dstKey meta.ObjKey
		dstOID string
		shard  string
	)
	BeforeEach(func() {
		mtime = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		withCompressionClasses(c, compression.Snappy, compression.Zlib, "random")
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		plain = hexNoise(10 << 20)
		srcKey, srcOID = meta.ObjKey{Name: "src"}, putBucketID+"_src"
		dstKey, dstOID = meta.ObjKey{Name: "dst"}, putBucketID+"_dst"
		shard = indexShardOID(rec, "dst")
	})
	// prefetched reads the source's state once its index completions are
	// done; the copy that follows draws fixedRand(secondPrefix).
	prefetched := func(ctx context.Context) *op.ObjectState {
		GinkgoHelper()
		settle(s)
		st, err := s.PrefetchObject(ctx, rec, srcKey)
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(fixedRand(secondPrefix))
		return st
	}
	// source PUTs plain as the source under class, through a Store opened at
	// rel, and returns its prefetched state.
	source := func(ctx context.Context, rel denc.Release, class string) *op.ObjectState {
		GinkgoHelper()
		s = openPutStore(ctx, c, rel, nil, mtime)
		_, err := s.PutObject(ctx, rec, srcKey, bytes.NewReader(plain), op.PutParams{
			Attrs: attrsWithACL(initiator), Size: int64(len(plain)), Tag: "tx-src", StorageClass: class,
		})
		Expect(err).NotTo(HaveOccurred())
		return prefetched(ctx)
	}
	// multipartSource completes plain as the source in two parts, of 5 MiB
	// and the rest, through a Store opened at rel, and returns its prefetched
	// state.
	multipartSource := func(ctx context.Context, rel denc.Release) *op.ObjectState {
		GinkgoHelper()
		s = openPutStore(ctx, c, rel, nil, mtime)
		up, err := s.CreateUpload(ctx, rec, srcKey, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator),
		})
		Expect(err).NotTo(HaveOccurred())
		var parts []op.CompletePart
		for i, b := range [][]byte{plain[:5<<20], plain[5<<20:]} {
			res, perr := s.PutPart(ctx, up, i+1, bytes.NewReader(b), op.PutParams{Attrs: attrsWithACL(initiator), Size: int64(len(b))})
			Expect(perr).NotTo(HaveOccurred())
			parts = append(parts, op.CompletePart{Number: i + 1, ETag: res.ETag})
		}
		_, err = s.Complete(ctx, up, parts)
		Expect(err).NotTo(HaveOccurred())
		st := prefetched(ctx)
		Expect(st.Manifest.PartsCount()).To(Equal(2))
		return st
	}
	copyTo := func(ctx context.Context, st *op.ObjectState, class string, replace bool) {
		GinkgoHelper()
		_, err := s.CopyObject(ctx, st, rec, dstKey, op.CopyParams{Attrs: attrsWithACL(initiator), ReplaceAttrs: replace, StorageClass: class, Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
	}
	// stored is st's stored bytes, as Read::read hands them over.
	stored := func(ctx context.Context, st *op.ObjectState) []byte {
		GinkgoHelper()
		var b bytes.Buffer
		Expect(s.ReadStoredForTest(ctx, st, 0, st.Size, &b)).To(Succeed())
		return b.Bytes()
	}
	// expectHint expects every write of the copy's tails in testDataPool, and
	// its head's write_full, hinted INCOMPRESSIBLE when hinted and not
	// otherwise.
	expectHint := func(hinted bool) {
		GinkgoHelper()
		tails := tailHints(c, secondPrefix)
		Expect(tails).NotTo(BeEmpty(), "the copy has tails")
		after := afterWriteFull(c.LastWrite(testDataPool, "", dstOID).Steps())
		if hinted {
			Expect(tails).To(HaveEach(radosclient.AllocHintFlags(512)), "add_write_hint, rgw_putobj_processor.cc:146-155 at v20.2.4")
			Expect(after).To(Equal(&radosclient.SetAllocHintStep{Flags: 512}), "_do_write_meta, rgw_rados.cc:3330-3334 at v20.2.4")
			return
		}
		Expect(tails).To(HaveEach(radosclient.AllocHintFlags(0)))
		Expect(after).NotTo(BeAssignableToTypeOf(&radosclient.SetAllocHintStep{}))
	}
	// streamed reports whether the copy's head names tails of its own, which
	// copy_obj_data writes, rather than the source's.
	streamed := func(src *op.ObjectState) bool {
		GinkgoHelper()
		m := meta.DecodeManifest(denc.NewDecoder(c.Object(testDataPool, "", dstOID).Xattrs[meta.AttrManifest]))
		Expect(m.Prefix).To(BeElementOf(src.Manifest.Prefix, "."+secondPrefix+"_"))
		return m.Prefix != src.Manifest.Prefix
	}

	// compressionCase is a streamed copy of the source PUT under srcClass into
	// dstClass. want is the codec the copy is stored in, "" for none; raw is
	// whether it holds the source's stored bytes and compression info, and
	// hinted whether that raw copy is hinted INCOMPRESSIBLE.
	type compressionCase struct {
		srcClass, dstClass, want string
		raw, hinted              bool
	}
	DescribeTable("compresses an uncompressed source as the destination's class says, and copies a compressed one raw",
		func(ctx SpecContext, rel denc.Release, cc compressionCase) {
			src := source(ctx, rel, cc.srcClass)
			Expect(src.Compression != nil).To(Equal(cc.srcClass == snappyClass), "the source's form")
			c.ResetCounters()
			copyTo(ctx, src, cc.dstClass, false)
			Expect(streamed(src)).To(BeTrue())
			dst, err := s.StatObject(ctx, rec, dstKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(readBack(ctx, s, dst, 0, 10<<20)).To(Equal(plain), "the copy reads back as its source")
			got := stored(ctx, dst)
			head := c.Object(testDataPool, "", dstOID)
			switch {
			case cc.raw:
				Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCompression, src.Attrs[meta.AttrCompression]), "the source's compression info, block map included")
				Expect(got).To(Equal(stored(ctx, src)), "the source's stored bytes, undecoded")
				expectHint(cc.hinted)
			case cc.want != "":
				Expect(dst.Compression).NotTo(BeNil(), "user.rgw.compression")
				Expect([]any{dst.Compression.Type, dst.Compression.OrigSize}).To(Equal([]any{cc.want, uint64(10 << 20)}))
				Expect(dst.Compression.Blocks).To(HaveLen(3), "one block per rgw_max_chunk_size of plaintext")
				Expect(len(got)).To(BeNumerically("<", 10<<20), "the stored bytes are the codec's")
				expectHint(true)
			default:
				Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCompression))
				Expect(got).To(Equal(plain), "the plaintext as it is")
			}
			en, ok := c.Entry(rookIndexPool, "", shard, "dst")
			Expect(ok).To(BeTrue())
			Expect([]uint64{en.Meta.Size, en.Meta.AccountedSize}).To(Equal([]uint64{uint64(len(got)), 10 << 20}),
				"the stored size, and the plaintext's accounted")
		},
		Entry("Squid: an uncompressed source into a zlib class is compressed, where Squid's radosgw stores it as it is",
			denc.Squid, compressionCase{dstClass: zlibClass, want: compression.Zlib}),
		Entry("Tentacle: an uncompressed source into a zlib class is compressed, as RGWCopyObjDPF compresses it",
			denc.Tentacle, compressionCase{dstClass: zlibClass, want: compression.Zlib}),
		Entry("Squid: an uncompressed source into a class without a compression type is stored as it is",
			denc.Squid, compressionCase{dstClass: "COLD"}),
		Entry("Tentacle: a snappy source into a zlib class keeps its snappy blocks, hinted as set_compressed marks the copy, where Tentacle's radosgw encodes it with zlib",
			denc.Tentacle, compressionCase{srcClass: snappyClass, dstClass: zlibClass, want: compression.Snappy, raw: true, hinted: true}),
		Entry("Squid: a snappy source into a zlib class keeps its snappy blocks, unhinted, as copy_obj_data copies them",
			denc.Squid, compressionCase{srcClass: snappyClass, dstClass: zlibClass, want: compression.Snappy, raw: true}),
		Entry("Tentacle: a snappy source into a class without a compression type keeps its snappy blocks, unhinted, where Tentacle's radosgw stores it decoded",
			denc.Tentacle, compressionCase{srcClass: snappyClass, want: compression.Snappy, raw: true}),
		Entry("Tentacle: a snappy source into a random class keeps its snappy blocks, unhinted, as rgw-go loads no codec for random",
			denc.Tentacle, compressionCase{srcClass: snappyClass, dstClass: compClass("random"), want: compression.Snappy, raw: true}),
	)

	DescribeTable("copies the stored bytes and both records as they are under the raw flag, as transition_obj's copy_obj_data runs without a data-processor factory",
		func(ctx SpecContext, srcClass string) {
			source(ctx, denc.Tentacle, srcClass)
			record := cksumRecord(cksum.CRC32C, be32(0xe3069283), compositeFlags)
			c.Object(testDataPool, "", srcOID).Xattrs[meta.AttrCksum] = record
			src := prefetched(ctx)
			attrs := maps.Clone(src.Attrs)
			delete(attrs, meta.AttrIDTag)
			delete(attrs, meta.AttrTailTag)
			_, err := s.CopyDataForTest(ctx, src, rec, dstKey, zlibClass, attrs, true)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			dst, err := s.StatObject(ctx, rec, dstKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(stored(ctx, dst)).To(Equal(stored(ctx, src)), "the source's stored bytes, though the class compresses with zlib")
			expectHint(false)
			head := c.Object(testDataPool, "", dstOID)
			Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCksum, record), "a composite record, on an object without parts")
			if b, ok := src.Attrs[meta.AttrCompression]; ok {
				Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCompression, b))
			} else {
				Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCompression))
			}
			Expect(readBack(ctx, s, dst, 0, 10<<20)).To(Equal(plain))
		},
		Entry("an uncompressed source stays uncompressed", ""),
		Entry("a snappy source keeps its snappy blocks and compression info", snappyClass),
	)

	// checksumCase is a copy under the storage class class, "" sharing the
	// tails and COLD streaming them, with REPLACE when replace, of a source
	// whose user.rgw.cksum carries flags; multipart sources have two parts.
	// kept is whether the copy carries the record.
	type checksumCase struct {
		multipart, replace bool
		flags              uint16
		class              string
		kept               bool
	}
	DescribeTable("carries the source's checksum record as set_copy_attrs does, and drops one a copy that streams would misread",
		func(ctx SpecContext, rel denc.Release, cc checksumCase) {
			if cc.multipart {
				multipartSource(ctx, rel)
			} else {
				source(ctx, rel, "")
			}
			record := cksumRecord(cksum.CRC32C, be32(0xe3069283), cc.flags)
			c.Object(testDataPool, "", srcOID).Xattrs[meta.AttrCksum] = record
			src := prefetched(ctx)
			Expect(src.Checksum).NotTo(BeNil())
			copyTo(ctx, src, cc.class, cc.replace)
			Expect(streamed(src)).To(Equal(cc.class != ""))
			head := c.Object(testDataPool, "", dstOID)
			if cc.kept {
				Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCksum, record))
			} else {
				Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCksum))
			}
			dst, err := s.StatObject(ctx, rec, dstKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(readBack(ctx, s, dst, 0, 10<<20)).To(Equal(plain))
		},
		Entry("Squid: COPY keeps a record on a copy that shares the tails",
			denc.Squid, checksumCase{kept: true}),
		Entry("Tentacle: COPY keeps a record on a copy that streams",
			denc.Tentacle, checksumCase{class: "COLD", kept: true}),
		Entry("Tentacle: REPLACE drops it from a copy that shares the tails, rgw_rados.cc:3866-3876 at v20.2.4",
			denc.Tentacle, checksumCase{replace: true}),
		Entry("Squid: REPLACE drops it from a copy that streams",
			denc.Squid, checksumCase{replace: true, class: "COLD"}),
		Entry("Tentacle: a composite record stays on a copy that shares the tails",
			denc.Tentacle, checksumCase{flags: compositeFlags, kept: true}),
		Entry("Squid: a composite record is dropped from a copy that streams",
			denc.Squid, checksumCase{flags: compositeFlags, class: "COLD"}),
		Entry("Tentacle: a multipart source's composite record stays on a copy that shares its parts",
			denc.Tentacle, checksumCase{multipart: true, flags: compositeFlags, kept: true}),
		Entry("Tentacle: a multipart source's composite record is dropped from a copy that streams, which has no parts",
			denc.Tentacle, checksumCase{multipart: true, flags: compositeFlags, class: "COLD"}),
		Entry("Tentacle: a multipart source's record with neither V2 nor FULL_OBJECT, COMPOSITE to get_checksum_type, is dropped from a copy that streams",
			denc.Tentacle, checksumCase{multipart: true, flags: cksum.FlagCombined, class: "COLD"}),
		Entry("Squid: a multipart source's full-object CRC rides a copy that streams",
			denc.Squid, checksumCase{multipart: true, flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagFullObject, class: "COLD", kept: true}),
		Entry("Tentacle: a multipart source's full-object CRC without V2, as a one-part upload's is, rides a copy that streams",
			denc.Tentacle, checksumCase{multipart: true, flags: cksum.FlagCombined | cksum.FlagFullObject, class: "COLD", kept: true}),
	)

	DescribeTable("drops from a copy that streams a checksum attr cksum.DecodeAttr refuses, and keeps it on one that shares the tails, as set_copy_attrs does",
		func(ctx SpecContext, attr []byte, class string, kept bool) {
			source(ctx, denc.Tentacle, "")
			c.Object(testDataPool, "", srcOID).Xattrs[meta.AttrCksum] = attr
			src := prefetched(ctx)
			Expect(src.Checksum).To(BeNil(), "the source reads as having none")
			copyTo(ctx, src, class, false)
			Expect(streamed(src)).To(Equal(class != ""))
			head := c.Object(testDataPool, "", dstOID)
			if kept {
				Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrCksum, attr))
			} else {
				Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCksum))
			}
		},
		Entry("a composite record with a byte past it, which Tentacle's iterator readers would show as the copy's FULL_OBJECT checksum, streamed",
			slices.Concat(cksumRecord(cksum.CRC32C, be32(0xe3069283), compositeFlags), []byte{0x99}), "COLD", false),
		Entry("a full-object record with a byte past it, on which Tentacle's checksum-mode GET aborts, streamed",
			slices.Concat(cksumRecord(cksum.CRC32C, be32(0xe3069283), cksum.FlagFullObject), []byte{0x99}), "COLD", false),
		Entry("a record of type 9, streamed", cksumRecord(9, make([]byte, 16), 0), "COLD", false),
		Entry("a composite record with a byte past it, on a copy that shares the tails",
			slices.Concat(cksumRecord(cksum.CRC32C, be32(0xe3069283), compositeFlags), []byte{0x99}), "", true),
	)
	It("keeps a checksum attr cksum.DecodeAttr refuses under the raw flag, as transition_obj copies every attr", func(ctx SpecContext) {
		source(ctx, denc.Tentacle, "")
		attr := slices.Concat(cksumRecord(cksum.CRC32C, be32(0xe3069283), compositeFlags), []byte{0x99})
		c.Object(testDataPool, "", srcOID).Xattrs[meta.AttrCksum] = attr
		src := prefetched(ctx)
		attrs := maps.Clone(src.Attrs)
		delete(attrs, meta.AttrIDTag)
		delete(attrs, meta.AttrTailTag)
		_, err := s.CopyDataForTest(ctx, src, rec, dstKey, "COLD", attrs, true)
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		Expect(c.Object(testDataPool, "", dstOID).Xattrs).To(HaveKeyWithValue(meta.AttrCksum, attr))
	})

	It("computes no checksum for a copy, as RGWCopyObj computes none", func(ctx SpecContext) {
		src := source(ctx, denc.Tentacle, "")
		res, err := s.CopyObject(ctx, src, rec, dstKey, op.CopyParams{Attrs: attrsWithACL(initiator), StorageClass: zlibClass, Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Checksum).To(BeNil())
		Expect(c.Object(testDataPool, "", dstOID).Xattrs).NotTo(HaveKey(meta.AttrCksum), "the source has none")
	})

	It("recompresses a compressed source's decoded range into a copied part, as PutPart compresses a part", func(ctx SpecContext) {
		src := source(ctx, denc.Squid, snappyClass)
		Expect(src.Compression).NotTo(BeNil())
		up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "k"}, op.UploadParams{
			Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement", StorageClass: zlibClass}, Attrs: attrsWithACL(initiator),
		})
		Expect(err).NotTo(HaveOccurred())
		res, err := s.CopyPart(ctx, up, 1, src, op.ByteRange{Offset: 1 << 20, Length: 6 << 20})
		Expect(err).NotTo(HaveOccurred())
		want := plain[1<<20 : 7<<20]
		Expect(res.ETag).To(Equal(md5hex(want)), "the MD5 of the decoded range")
		info := storedPart(c, metaOID(rec, "k", up.ID), "part.00000001")
		ci := info.Compression
		Expect([]any{ci.Type, ci.OrigSize, info.AccountedSize}).To(Equal([]any{compression.Zlib, uint64(6 << 20), uint64(6 << 20)}),
			"the upload's codec, not the source's")
		prefix := "k." + up.ID
		partStored := slices.Clone(c.Object(testDataPool, "", partHeadOID(prefix, 1)).Data)
		for i := 1; c.Object(testDataPool, "", partShadowOID(prefix, 1, i)) != nil; i++ {
			partStored = append(partStored, c.Object(testDataPool, "", partShadowOID(prefix, 1, i)).Data...)
		}
		dec, err := compression.NewDecoder(ci.Type)
		Expect(err).NotTo(HaveOccurred())
		var back []byte
		for i, b := range ci.Blocks {
			end := ci.OrigSize
			if i+1 < len(ci.Blocks) {
				end = ci.Blocks[i+1].OldOfs
			}
			got, err := dec.Decode(nil, partStored[b.NewOfs:b.NewOfs+b.Len], int(end-b.OldOfs), ci.CompressorMessage) //nolint:gosec // spec sizes are small
			Expect(err).NotTo(HaveOccurred(), "block %d", i)
			back = append(back, got...)
		}
		Expect(back).To(Equal(want))
	})
})
