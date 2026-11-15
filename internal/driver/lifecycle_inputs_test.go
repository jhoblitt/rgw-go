package driver_test

import (
	"bytes"
	"context"
	"maps"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The lifecycle headers of a copy and of a completion name the object the
// response describes by its tags and its size before compression, which the
// store reports in the result; a ListParts names its upload's initiation.
var _ = Describe("what the stores report for the lifecycle headers", func() {
	const tagged = "key1=tag1&key5=tag5\x00"
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		clock *specClock
		start time.Time
		key   meta.ObjKey
	)
	BeforeEach(func(ctx SpecContext) {
		start = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		clock = &specClock{t: start}
		c = newPutCluster()
		c.SetClock(clock.Now)
		s = openPutStore(ctx, c, denc.Squid, nil, start)
		driver.SetClock(s, clock.Now)
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: -1, Enabled: true}
		Expect(s.CheckQuota(ctx, rec, rec.Info.Owner, 0, 0)).To(Succeed())
		key = meta.ObjKey{Name: "k"}
	})
	AfterEach(func() { settle(s) })

	Describe("CopyObject", func() {
		var dstKey meta.ObjKey
		BeforeEach(func() { dstKey = meta.ObjKey{Name: "dst"} })
		// put writes size bytes under name with attrs on top of alice's ACL
		// and returns the prefetched state.
		put := func(ctx context.Context, name string, size int, attrs map[string][]byte) *op.ObjectState {
			GinkgoHelper()
			all := attrsWithACL(initiator)
			maps.Copy(all, attrs)
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: name}, bytes.NewReader(bytes.Repeat([]byte("x"), size)),
				op.PutParams{Attrs: all, Size: int64(size), Tag: "tx-" + name})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			st, err := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: name})
			Expect(err).NotTo(HaveOccurred())
			return st
		}

		DescribeTable("reports the destination's tags as stored, the source's under COPY and the request's under REPLACE, and its size",
			func(ctx SpecContext, size int, replace bool, request map[string][]byte, want []byte) {
				src := put(ctx, "src", size, map[string][]byte{tags.Attr: []byte(tagged)})
				attrs := attrsWithACL(initiator)
				maps.Copy(attrs, request)
				s.SetRandForTest(fixedRand(secondPrefix))
				res, err := s.CopyObject(ctx, src, rec, dstKey, op.CopyParams{Attrs: attrs, ReplaceAttrs: replace, Tag: "tx-copy"})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(res.Tags).To(Equal(want))
				Expect(res.OrigSize).To(BeEquivalentTo(size))
				dst, err := s.StatObject(ctx, rec, dstKey)
				Expect(err).NotTo(HaveOccurred())
				Expect(dst.Attrs[tags.Attr]).To(Equal(want), "what the result reports is what the head stores")
			},
			Entry("COPY of a source held in its head, streamed", 1<<10, false, nil, []byte(tagged)),
			Entry("COPY of a source with tails, which the copy shares", 6<<20, false, nil, []byte(tagged)),
			Entry("REPLACE without tags, streamed", 1<<10, true, nil, nil),
			Entry("REPLACE without tags, tails shared", 6<<20, true, nil, nil),
			Entry("REPLACE with the request's tags, streamed", 1<<10, true, map[string][]byte{tags.Attr: []byte("a=b\x00")}, []byte("a=b\x00")),
			Entry("REPLACE with the request's tags, tails shared", 6<<20, true, map[string][]byte{tags.Attr: []byte("a=b\x00")}, []byte("a=b\x00")),
		)

		DescribeTable("reports the size before compression of a compressed source, whose copy keeps the compression info",
			func(ctx SpecContext, stored int) {
				ci := meta.NewCompressionInfo()
				ci.Type, ci.OrigSize = "snappy", 9<<20
				ci.Blocks = []meta.CompressionBlock{{Len: uint64(stored)}}
				src := seedCompressedSource(ctx, c, s, rec, "csrc", bytes.Repeat([]byte("z"), stored), ci)
				s.SetRandForTest(fixedRand(secondPrefix))
				res, err := s.CopyObject(ctx, src, rec, dstKey, op.CopyParams{Attrs: attrsWithACL(initiator), Tag: "tx-copy"})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				Expect(res.Size).To(BeEquivalentTo(stored), "the stored size")
				Expect(res.OrigSize).To(BeEquivalentTo(9 << 20))
			},
			Entry("streamed", 1<<10),
			Entry("with its tails shared", 6<<20),
		)
	})

	Describe("Complete", func() {
		var rule meta.PlacementRule
		BeforeEach(func() { rule = meta.PlacementRule{Name: "default-placement"} })
		// create begins an upload of k whose completed object takes the
		// tags attr, and returns it.
		create := func(ctx context.Context) *op.Upload {
			GinkgoHelper()
			attrs := attrsWithACL(initiator)
			attrs[tags.Attr] = []byte(tagged)
			up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: rule, Attrs: attrs})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			return up
		}
		// uploaded is create with one part of 1 KiB, and the completion
		// naming it.
		uploaded := func(ctx context.Context) (*op.Upload, []op.CompletePart) {
			GinkgoHelper()
			up := create(ctx)
			r, err := s.PutPart(ctx, up, 1, bytes.NewReader(bytes.Repeat([]byte("p"), 1<<10)), op.PutParams{Attrs: attrsWithACL(initiator), Size: 1 << 10})
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			return up, []op.CompletePart{{Number: 1, ETag: `"` + r.ETag + `"`}}
		}
		// compressed is create with two parts radosgw wrote compressed: 3
		// MiB stored of 5 MiB each.
		compressed := func(ctx context.Context) (*op.Upload, []op.CompletePart) {
			GinkgoHelper()
			up := create(ctx)
			prefix := "k." + up.ID
			target := meta.Obj{Bucket: rec.Info.Bucket, Key: key}
			mObj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			var parts []op.CompletePart
			for n := uint32(1); n <= 2; n++ {
				m := meta.NewPartManifest(target, rec.Info.PlacementRule, rule, prefix, n, 4<<20)
				m.SetObjSize(3 << 20)
				etag := md5hex("compressed" + strconv.Itoa(int(n)))
				mObj.Omap[meta.MultipartPartKey(n)] = encode(meta.UploadPartInfo{
					Num: n, Size: 3 << 20, AccountedSize: 5 << 20, ETag: etag, Modified: start, Manifest: m,
					Compression: meta.CompressionInfo{Type: "zlib", OrigSize: 5 << 20, Blocks: []meta.CompressionBlock{{Len: 3 << 20}}},
				})
				c.Put(testDataPool, "", partHeadOID(prefix, int(n)), bytes.Repeat([]byte("c"), 3<<20))
				parts = append(parts, op.CompletePart{Number: int(n), ETag: `"` + etag + `"`})
			}
			return up, parts
		}
		// leaveMeta completes up with the meta object's removal failing, so
		// that the meta object outlives the head write.
		leaveMeta := func(ctx context.Context, up *op.Upload, parts []op.CompletePart) *op.PutResult {
			GinkgoHelper()
			mOID := metaOID(rec, "k", up.ID)
			armed := false
			c.BeforeWrite(extraPoolName, "", mOID, func(*fakerados.Object) {
				if !armed && c.Object(testDataPool, "", putBucketID+"_k") != nil {
					armed = true
					c.FailNextWrite(extraPoolName, "", mOID, syscall.EIO)
				}
			})
			res, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(c.Object(extraPoolName, "", mOID)).NotTo(BeNil(), "the failed removal left the meta object")
			c.BeforeWrite(extraPoolName, "", mOID, nil)
			return res
		}

		It("reports the tags the head stores and its size", func(ctx SpecContext) {
			up, parts := uploaded(ctx)
			res, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Tags).To(Equal([]byte(tagged)))
			Expect(res.Size).To(BeEquivalentTo(1 << 10))
			Expect(res.OrigSize).To(BeEquivalentTo(1 << 10))
			Expect(c.Object(testDataPool, "", putBucketID+"_k").Xattrs).To(HaveKeyWithValue(tags.Attr, res.Tags))
		})

		It("reports the size before compression of compressed parts, where Size is the stored size", func(ctx SpecContext) {
			up, parts := compressed(ctx)
			res, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(res.Size).To(BeEquivalentTo(6<<20), "the stored sizes")
			Expect(res.OrigSize).To(BeEquivalentTo(10<<20), "the compression info's orig_size, which lcProbe.Size reads")
		})

		It("reports the head a retried completion finds, once the upload is gone", func(ctx SpecContext) {
			up, parts := compressed(ctx)
			first, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			retried, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{retried.Tags, retried.Size, retried.OrigSize}).To(Equal([]any{[]byte(tagged), first.Size, first.OrigSize}))
			Expect(retried.OrigSize).To(BeEquivalentTo(10 << 20))
		})

		It("reports the head a recorded completion whose meta object outlived its head write finishes", func(ctx SpecContext) {
			up, parts := compressed(ctx)
			first := leaveMeta(ctx, up, parts)
			retried, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil(), "the retry finished the removal")
			Expect([]any{retried.Tags, retried.Size, retried.OrigSize}).To(Equal([]any{[]byte(tagged), first.Size, first.OrigSize}))
			Expect(retried.OrigSize).To(BeEquivalentTo(10 << 20))
		})

		It("reports the head a completion that a gateway without the record left finishes", func(ctx SpecContext) {
			up, parts := compressed(ctx)
			first := leaveMeta(ctx, up, parts)
			obj := c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))
			delete(obj.Xattrs, "user.rgw.mp_completion_tag")
			delete(obj.Xattrs, "user.rgw.mp_completion_instance")
			retried, err := s.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred())
			settle(s)
			Expect(c.Object(extraPoolName, "", metaOID(rec, "k", up.ID))).To(BeNil(), "the retry finished the removal")
			Expect([]any{retried.Tags, retried.Size, retried.OrigSize}).To(Equal([]any{[]byte(tagged), first.Size, first.OrigSize}))
			Expect(retried.OrigSize).To(BeEquivalentTo(10 << 20))
		})
	})

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("UploadInitiated on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) {
				s = openPutStore(ctx, c, rel, nil, start)
				driver.SetClock(s, clock.Now)
			})
			// begun is an upload created and settled in plain's bucket.
			begun := func(ctx context.Context) *op.Upload {
				GinkgoHelper()
				up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: initiator, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(initiator)})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				return up
			}

			It("is the mtime of the upload's index entry, which no part moves, where GetUpload reads the meta object's own", func(ctx SpecContext) {
				up := begun(ctx)
				clock.Advance(time.Hour)
				_, err := s.PutPart(ctx, up, 1, strings.NewReader("part"), op.PutParams{Attrs: attrsWithACL(initiator), Size: 4})
				Expect(err).NotTo(HaveOccurred())
				settle(s)
				got, err := s.GetUpload(ctx, rec, key, up.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Initiated).To(BeTemporally("==", start.Add(time.Hour)), "the part moved the meta object's mtime")
				initiated, err := s.UploadInitiated(ctx, got)
				Expect(err).NotTo(HaveOccurred())
				Expect(initiated).To(BeTemporally("==", start), "the entry CreateUpload wrote")
				Expect(initiated).To(BeTemporally("==", up.Initiated))
			})

			It("is the zero time, with no error, for an upload whose shard lists no index entry", func(ctx SpecContext) {
				initiated, err := s.UploadInitiated(ctx, &op.Upload{ID: "2~" + strings.Repeat("a", 31), Bucket: rec, Key: key})
				Expect(err).NotTo(HaveOccurred())
				Expect(initiated).To(BeZero())
			})

			It("is the zero time, with no error and no read, for an indexless bucket, which has no shard to read", func(ctx SpecContext) {
				const id = "zone-ceph-objectstore.4156.9"
				blind := testBucket(id, 0)
				blind.Info.Layout.Current.Layout.Type = meta.IndexIndexless
				seedShards(c, blind)
				c.ResetCounters()
				initiated, err := s.UploadInitiated(ctx, &op.Upload{ID: "2~" + strings.Repeat("a", 31), Bucket: blind, Key: key})
				Expect(err).NotTo(HaveOccurred())
				Expect(initiated).To(BeZero())
				Expect(c.Reads(rookIndexPool, "", ".dir."+id)).To(BeZero(), "a bucket without an index has no entry, and radosgw's ListParts reads none")
			})

			It("is the zero time, with no error, once the shard is gone, as a concurrent abort or completion leaves it", func(ctx SpecContext) {
				up := begun(ctx)
				c.Remove(rookIndexPool, "", indexShardOID(rec, "k"))
				initiated, err := s.UploadInitiated(ctx, up)
				Expect(err).NotTo(HaveOccurred())
				Expect(initiated).To(BeZero())
			})

			It("fails with any other error reading the shard", func(ctx SpecContext) {
				up := begun(ctx)
				c.FailNextRead(rookIndexPool, "", indexShardOID(rec, "k"), syscall.EIO)
				initiated, err := s.UploadInitiated(ctx, up)
				Expect(err).To(HaveOccurred())
				Expect(initiated).To(BeZero())
			})
		})
	}
})
