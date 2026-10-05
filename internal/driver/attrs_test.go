package driver_test

import (
	"bytes"
	"context"
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
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// attrsTag is the tag set_attrs makes, append_rand_alpha's "_" and 31
// characters, which the PUT fixture's random source fixes.
const attrsTag = "_" + putPrefix

// attrsOwner is the owner of the fixture's object.
var attrsOwner = meta.UserOwner(meta.UserID{ID: "alice"})

var _ = Describe("SetObjectAttrs", func() {
	const tagSet = "tagset"
	var (
		c       *fakerados.Cluster
		s       *driver.Store
		rec     *op.BucketRecord
		key     meta.ObjKey
		mtime   time.Time
		body    []byte
		headOID string
		shard   string
		st      *op.ObjectState
	)
	// setup opens the store at rel and writes k, 6 MiB with a tail, under the
	// write tag tx-put, then reads its state as the op's Init does.
	setup := func(ctx context.Context, rel denc.Release) {
		GinkgoHelper()
		s = openPutStore(ctx, c, rel, nil, mtime)
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(body), op.PutParams{
			Attrs: map[string][]byte{
				meta.AttrACL:         encode(acl.DefaultPolicy(attrsOwner, "Alice")),
				meta.AttrContentType: []byte("text/plain\x00"),
				tags.Attr:            []byte(tagSet),
			},
			Size: int64(len(body)), Tag: "tx-put", Mtime: mtime,
		})
		Expect(err).NotTo(HaveOccurred())
		settle(s)
		st, err = s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.WriteTag).To(Equal("tx-put\x00"))
		c.ResetCounters()
	}
	entry := func() rgwcls.DirEntry {
		GinkgoHelper()
		settle(s)
		en, ok := c.Entry(rookIndexPool, "", shard, "k")
		Expect(ok).To(BeTrue(), "the index entry of k")
		return en
	}
	BeforeEach(func() {
		mtime = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c = newPutCluster()
		clock := mtime
		c.SetClock(func() time.Time { return clock })
		rec = testBucket(putBucketID, 11)
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		key = meta.ObjKey{Name: "k"}
		body = bytes.Repeat([]byte("b"), 6<<20)
		headOID = putBucketID + "_k"
		shard = indexShardOID(rec, "k")
	})

	DescribeTable("replaces the ACL behind the tag guard, retags the head, and rebuilds the index entry",
		func(ctx SpecContext, rel denc.Release) {
			setup(ctx, rel)
			newACL := encode(acl.DefaultPolicy(attrsOwner, "Alice Renamed"))
			Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: newACL}, nil)).To(Succeed())
			Expect(c.Reads(testDataPool, "", headOID)).To(BeZero(), "the op's Init did the stat")
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(1))
			w := c.LastWrite(testDataPool, "", headOID)
			Expect(w.Steps()).To(Equal([]radosclient.Step{
				&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")},
				&radosclient.SetXattrStep{Name: "user.rgw.acl", Value: newACL},
				&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte(attrsTag + "\x00")},
			}), "append_atomic_test, the attrs, then the new tag NUL-terminated (rgw_rados.cc:6615, :6634-6676)")
			mt, ok := w.Mtime()
			Expect(ok).To(BeTrue())
			Expect(mt).To(Equal(mtime.Add(time.Nanosecond)), "set_obj_attrs' nudge, rgw_sal_rados.cc:2382-2384")
			Expect(w.Flags()).To(Equal(radosclient.OpFlagNone))

			head := c.Object(testDataPool, "", headOID)
			Expect(head.Mtime).To(Equal(mtime.Add(time.Nanosecond)))
			Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.acl", newACL))
			Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte(attrsTag+"\x00")))
			Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-put\x00")), "the tails keep their tag")

			Expect(c.Writes(rookIndexPool, "", shard)).To(BeNumerically(">=", 1))
			prep := execIn(c.WritesTo(rookIndexPool, "", shard)[0], 2, "bucket_prepare_op", rgwcls.DecodePrepareOp)
			Expect(prep.Op).To(Equal(rgwcls.OpAdd))
			Expect(prep.Tag).To(Equal(attrsTag), "one tag for the prepare and the head")
			en := entry()
			Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "the prepare and the complete")
			Expect(execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpAdd))
			Expect(en.Tag).To(Equal(attrsTag))
			Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: head.Version}))
			Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{
				Category: rgwcls.CategoryMain, Size: 6 << 20, AccountedSize: 6 << 20, Mtime: mtime.Add(time.Nanosecond),
				ETag: md5hex(body), ContentType: "text/plain",
				Owner: "alice", OwnerDisplayName: "Alice Renamed",
			}), "owner from the new ACL, the rest from the existing attrs (rgw_rados.cc:6693-6725)")
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)

	It("removes the tagging attr and rebuilds the entry from the existing attrs", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		Expect(s.SetObjectAttrs(ctx, st, nil, []string{tags.Attr})).To(Succeed())
		Expect(c.LastWrite(testDataPool, "", headOID).Steps()).To(Equal([]radosclient.Step{
			&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")},
			&radosclient.RmXattrStep{Name: "user.rgw.x-amz-tagging"},
			&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte(attrsTag + "\x00")},
		}))
		Expect(c.Object(testDataPool, "", headOID).Xattrs).NotTo(HaveKey("user.rgw.x-amz-tagging"))
		en := entry()
		Expect(en.Meta.ETag).To(Equal(md5hex(body)))
		Expect(en.Meta.ContentType).To(Equal("text/plain"))
		Expect([]string{en.Meta.Owner, en.Meta.OwnerDisplayName}).To(Equal([]string{"alice", "Alice"}), "from the existing ACL")
	})

	It("removes in name order once each, then sets in name order, skipping empty values", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		set := map[string][]byte{
			"user.rgw.x-amz-meta-b": []byte("b"),
			"user.rgw.x-amz-meta-a": []byte("a"),
			"user.rgw.x-amz-meta-z": {},
		}
		rm := []string{"user.rgw.x-amz-meta-b", tags.Attr, "user.rgw.x-amz-meta-a", tags.Attr}
		Expect(s.SetObjectAttrs(ctx, st, set, rm)).To(Succeed())
		Expect(c.LastWrite(testDataPool, "", headOID).Steps()).To(Equal([]radosclient.Step{
			&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")},
			&radosclient.RmXattrStep{Name: "user.rgw.x-amz-meta-a"},
			&radosclient.RmXattrStep{Name: "user.rgw.x-amz-meta-b"},
			&radosclient.RmXattrStep{Name: "user.rgw.x-amz-tagging"},
			&radosclient.SetXattrStep{Name: "user.rgw.x-amz-meta-a", Value: []byte("a")},
			&radosclient.SetXattrStep{Name: "user.rgw.x-amz-meta-b", Value: []byte("b")},
			&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte(attrsTag + "\x00")},
		}), "rmattrs and attrs are std::maps (rgw_rados.cc:6624-6656)")
		head := c.Object(testDataPool, "", headOID)
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-a", []byte("a")), "a name both removed and set ends set")
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.x-amz-meta-z"))
	})

	It("reads an attr the change names from the change, even when empty", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		set := map[string][]byte{meta.AttrACL: {}, meta.AttrContentType: {}, meta.AttrETag: []byte("e\x00")}
		Expect(s.SetObjectAttrs(ctx, st, set, nil)).To(Succeed())
		Expect(names(c.LastWrite(testDataPool, "", headOID).Steps())).To(Equal([]string{"user.rgw.etag", "user.rgw.idtag"}))
		en := entry()
		Expect([]string{en.Meta.Owner, en.Meta.OwnerDisplayName}).To(Equal([]string{"", ""}), "an empty ACL decodes to no owner (rgw_rados.cc:6695-6696)")
		Expect(en.Meta.ContentType).To(BeEmpty())
		Expect(en.Meta.ETag).To(Equal("e"), "rgw_bl_str")
	})

	It("still retags a guarded head for a change that sets nothing", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{"user.rgw.empty": {}}, nil)).To(Succeed())
		w := c.LastWrite(testDataPool, "", headOID)
		Expect(w.Steps()).To(Equal([]radosclient.Step{
			&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")},
			&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte(attrsTag + "\x00")},
		}), "the guard makes op.size() nonzero (rgw_rados.cc:6658-6659)")
		mt, _ := w.Mtime()
		Expect(mt).To(Equal(mtime.Add(time.Nanosecond)))
		Expect(entry().Tag).To(Equal(attrsTag))
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2))
	})

	It("does nothing for a change that sets nothing on a head without a tag", func(ctx SpecContext) {
		s = openPutStore(ctx, c, denc.Squid, nil, mtime)
		c.Put(testDataPool, "", headOID, []byte("bare"))
		c.Object(testDataPool, "", headOID).Xattrs[meta.AttrACL] = encode(acl.DefaultPolicy(attrsOwner, "Alice"))
		bare, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(bare.Manifest).To(BeNil())
		c.ResetCounters()
		Expect(s.SetObjectAttrs(ctx, bare, map[string][]byte{"user.rgw.empty": {}}, nil)).To(Succeed())
		Expect(c.Writes(testDataPool, "", headOID)).To(BeZero(), "!op.size(), rgw_rados.cc:6658-6659")
		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
	})

	It("sends no guard for a head whose tag radosgw would fake", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		delete(c.Object(testDataPool, "", headOID).Xattrs, meta.AttrIDTag)
		tagless, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(tagless.Manifest).NotTo(BeNil())
		Expect(tagless.WriteTag).To(BeEmpty())
		newACL := encode(acl.DefaultPolicy(attrsOwner, "x"))
		Expect(s.SetObjectAttrs(ctx, tagless, map[string][]byte{meta.AttrACL: newACL}, nil)).To(Succeed())
		Expect(c.LastWrite(testDataPool, "", headOID).Steps()).To(Equal([]radosclient.Step{
			&radosclient.SetXattrStep{Name: "user.rgw.acl", Value: newACL},
			&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte(attrsTag + "\x00")},
		}), "append_atomic_test skips a fake tag (rgw_rados.cc:6466-6470)")
		Expect(entry().Tag).To(Equal(attrsTag))
	})

	It("cancels with radosgw's -1:0 and reports the race when the tag moved", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.BeforeWrite(testDataPool, "", headOID, func(o *fakerados.Object) { o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00") })
		err := s.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(attrsOwner, "x"))}, nil)
		Expect(err).To(MatchError(op.ErrConcurrentModification))
		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2), "the prepare and the cancel")
		cancel := execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp)
		Expect(cancel.Op).To(Equal(rgwcls.OpCancel))
		Expect(cancel.Tag).To(Equal(attrsTag))
		Expect(cancel.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "index_op.cancel, rgw_rados.cc:6726-6731")
		head := c.Object(testDataPool, "", headOID)
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.acl", encode(acl.DefaultPolicy(attrsOwner, "Alice"))))
		Expect(head.Mtime).To(Equal(mtime))
	})

	It("cancels and answers NoSuchKey when the head is gone", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.BeforeWrite(testDataPool, "", headOID, func(*fakerados.Object) { c.Remove(testDataPool, "", headOID) })
		err := s.SetObjectAttrs(ctx, st, nil, []string{tags.Attr})
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		settle(s)
		Expect(execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpCancel))
	})

	It("cancels whatever the failure, unlike a PUT's head write", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		long := meta.AttrMetaPrefix + string(bytes.Repeat([]byte("n"), 100))
		err := s.SetObjectAttrs(ctx, st, map[string][]byte{long: []byte("v")}, nil)
		Expect(err).To(MatchError(op.ErrUnknown),
			"ENAMETOOLONG past osd_max_attr_name_len, which only rgw_http_swift_errors names (rgw_common.cc:149 at v19.2.6, :151 at v20.2.4)")
		settle(s)
		Expect(c.Writes(rookIndexPool, "", shard)).To(Equal(2))
		Expect(execIn(c.LastWrite(rookIndexPool, "", shard), 2, "bucket_complete_op", rgwcls.DecodeCompleteOp).Op).To(Equal(rgwcls.OpCancel),
			"set_attrs cancels on any r < 0 (rgw_rados.cc:6726-6731)")
	})

	It("writes no head when the prepare fails", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		c.Remove(rookIndexPool, "", shard)
		Expect(s.SetObjectAttrs(ctx, st, nil, []string{tags.Attr})).To(MatchError(op.ErrNoSuchKey), "the prepare's assert_exists")
		Expect(c.Writes(testDataPool, "", headOID)).To(BeZero())
	})

	It("accounts a compressed object at its original size", func(ctx SpecContext) {
		setup(ctx, denc.Squid)
		st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 9 << 20}
		Expect(s.SetObjectAttrs(ctx, st, nil, []string{tags.Attr})).To(Succeed())
		en := entry()
		Expect(en.Meta.Size).To(BeEquivalentTo(6 << 20))
		Expect(en.Meta.AccountedSize).To(BeEquivalentTo(9<<20), "state->accounted_size, rgw_rados.cc:6186-6199")
	})

	Describe("a null version id", func() {
		It("writes the plain head of an object with a manifest", func(ctx SpecContext) {
			setup(ctx, denc.Squid)
			null := *st
			null.Key.Instance = "null"
			Expect(s.SetObjectAttrs(ctx, &null, nil, []string{tags.Attr})).To(Succeed())
			Expect(c.Writes(testDataPool, "", headOID)).To(Equal(1), "rgw_rados.cc:6600-6603")
			Expect(entry().Tag).To(Equal(attrsTag), "the plain key's entry")
		})
		It("is NoSuchKey for a head without one", func(ctx SpecContext) {
			s = openPutStore(ctx, c, denc.Squid, nil, mtime)
			c.Put(testDataPool, "", headOID, []byte("bare"))
			bare, err := s.StatObject(ctx, rec, key)
			Expect(err).NotTo(HaveOccurred())
			bare.Key.Instance = "null"
			c.ResetCounters()
			Expect(s.SetObjectAttrs(ctx, bare, map[string][]byte{meta.AttrACL: []byte("x")}, nil)).To(MatchError(op.ErrNoSuchKey), "rgw_rados.cc:6619-6622")
			Expect(c.Writes(testDataPool, "", headOID)).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
		})
	})

	DescribeTable("files a restoring or temporarily restored object under the cloud-tiered category on Tentacle",
		func(ctx SpecContext, rel denc.Release, set map[string][]byte, category uint8, storageClass string) {
			setup(ctx, rel)
			Expect(s.SetObjectAttrs(ctx, st, set, nil)).To(Succeed())
			en := entry()
			Expect(en.Meta.Category).To(Equal(category))
			Expect(en.Meta.StorageClass).To(Equal(storageClass))
		},
		Entry("a restore in progress", denc.Tentacle,
			map[string][]byte{meta.AttrRestoreStatus: {byte(meta.RestoreAlreadyInProgress)}}, rgwcls.CategoryCloudTiered, ""),
		Entry("a failed restore", denc.Tentacle,
			map[string][]byte{meta.AttrRestoreStatus: {byte(meta.RestoreFailed)}}, rgwcls.CategoryCloudTiered, ""),
		Entry("a permanent restore", denc.Tentacle,
			map[string][]byte{meta.AttrRestoreStatus: {byte(meta.CloudRestored)}, meta.AttrRestoreType: {2}}, rgwcls.CategoryMain, ""),
		Entry("a temporary restore, under the tier's storage class", denc.Tentacle,
			map[string][]byte{
				meta.AttrRestoreStatus: {byte(meta.CloudRestored)}, meta.AttrRestoreType: {1},
				meta.AttrStorageClass: []byte("STANDARD"), meta.AttrCloudTierStorageClass: []byte("CLOUD\x00"),
			}, rgwcls.CategoryCloudTiered, "CLOUD"),
		Entry("a temporary restore without a tier class", denc.Tentacle,
			map[string][]byte{meta.AttrRestoreStatus: {byte(meta.CloudRestored)}, meta.AttrRestoreType: {1}, meta.AttrStorageClass: []byte("COLD")},
			rgwcls.CategoryCloudTiered, "COLD"),
		Entry("a restore type that does not decode", denc.Tentacle,
			map[string][]byte{meta.AttrRestoreStatus: {byte(meta.CloudRestored)}, meta.AttrRestoreType: {}}, rgwcls.CategoryMain, ""),
		Entry("a restore status that does not decode", denc.Tentacle,
			map[string][]byte{meta.AttrRestoreStatus: {}}, rgwcls.CategoryMain, ""),
		Entry("a restore in progress on Squid, which has no restore", denc.Squid,
			map[string][]byte{meta.AttrRestoreStatus: {byte(meta.RestoreAlreadyInProgress)}}, rgwcls.CategoryMain, ""),
	)
})
