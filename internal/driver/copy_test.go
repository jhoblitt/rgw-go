package driver_test

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"maps"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/snappy"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
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
		Expect(s.DeleteObject(ctx, rec, srcKey, op.DeleteParams{})).To(Succeed())
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
