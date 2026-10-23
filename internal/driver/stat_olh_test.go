package driver_test

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("StatObject and PrefetchObject of a versioned key", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		rec   *op.BucketRecord
		clock atomic.Int64
		shard string
	)
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	version := func(instance string) meta.ObjKey { return meta.ObjKey{Name: "k", Instance: instance} }
	BeforeEach(func(ctx SpecContext) {
		clock.Store(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixNano())
		c = newPutCluster()
		c.SetClock(now)
		rel := denc.Squid
		var err error
		s, err = driver.Open(ctx, c, conf(nil), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		driver.SetClock(s, now)
		rec = testBucket(putBucketID, 11)
		rec.Info.Flags |= meta.BucketVersioned
		seedShards(c, rec)
		seedRecordInstance(c, rec)
		shard = indexShardOID(rec, "k")
	})
	AfterEach(func() { settle(s) })

	// write is a versioned write of version instance up to its link: the
	// prepare, the instance head and the awaited ADD completion.
	write := func(ctx context.Context, instance string) meta.ObjKey {
		GinkgoHelper()
		v := version(instance)
		x := s.NewIndexOpForTest(rec, v, "go-tag-"+instance)
		x.SetBILogFlagsForTest(rgwcls.BILogFlagVersionedOp)
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		ver := writeInstanceHead(ctx, c, v)
		Expect(x.CompleteSync(ctx, rgwcls.EntryVer{Pool: c.PoolID(testDataPool), Epoch: ver}, versionMeta(v))).To(Succeed())
		return v
	}
	put := func(ctx context.Context, instance string) {
		GinkgoHelper()
		Expect(s.SetOLHForTest(ctx, rec, write(ctx, instance), driver.OLHLink{})).To(Succeed())
	}
	markerLink := func(expectTag []byte) driver.OLHLink {
		return driver.OLHLink{DeleteMarker: true, Meta: rgwcls.DirEntryMeta{Owner: "alice", Mtime: now()}, ExpectTag: expectTag}
	}
	marker := func(ctx context.Context, instance string) {
		GinkgoHelper()
		Expect(s.SetOLHForTest(ctx, rec, version(instance), markerLink(nil))).To(Succeed())
	}
	unlink := func(ctx context.Context, instance string, p driver.OLHUnlink) {
		GinkgoHelper()
		Expect(s.UnlinkObjInstanceForTest(ctx, rec, version(instance), p)).To(Succeed())
	}
	olhHead := func() *fakerados.Object { return c.Object(testDataPool, "", olhHeadOID(version(""))) }
	versionHead := func(instance string) *fakerados.Object {
		return c.Object(testDataPool, "", instanceHeadOID(version(instance)))
	}
	pending := func() []string {
		var out []string
		if o := olhHead(); o != nil {
			for name := range o.Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
					out = append(out, name)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	target := func() string {
		GinkgoHelper()
		d := denc.NewDecoder(olhHead().Xattrs[meta.AttrOLHInfo])
		info := meta.DecodeOLHInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return info.Target.Key.Instance
	}
	stat := func(ctx context.Context, key meta.ObjKey) *op.ObjectState {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, key)
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	// withManifest gives the null version's head a manifest of its whole
	// data, as every head radosgw's PUT writes carries one.
	withManifest := func() {
		GinkgoHelper()
		o := olhHead()
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: rec.Info.Bucket, Key: version("")}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = uint64(len(o.Data)), uint64(len(o.Data)), 4<<20
		o.Xattrs[meta.AttrManifest] = encode(m)
	}

	Describe("a key named without an instance", func() {
		It("follows the OLH to the current version and answers its head's state under its key", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			st := stat(ctx, version(""))
			Expect(st.Exists).To(BeTrue())
			Expect(st.Key).To(Equal(version("v2")), "Key.Instance is the version id")
			Expect([]any{st.ETag, st.Size, st.WriteTag, st.DeleteMarker}).To(Equal([]any{"etag-v2", uint64(len("data-v2")), "idtag-v2", false}))
			var body bytes.Buffer
			Expect(s.ReadObject(ctx, st, op.ByteRange{Length: st.Size}, &body)).To(Succeed())
			Expect(body.String()).To(Equal("data-v2"), "the read takes the version's head")
		})

		It("prefetches the current version's head bytes, not the OLH head's", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			st, err := s.PrefetchObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Key).To(Equal(version("v2")))
			Expect(st.Head).To(Equal([]byte("data-v2")))
		})

		It("answers the OLH head itself for the null version, which has no head of its own", func(ctx SpecContext) {
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
			c.ResetCounters()
			st, err := s.PrefetchObject(ctx, rec, version(""))
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeTrue())
			Expect(st.Key).To(Equal(version("")))
			Expect(st.Head).To(Equal([]byte("data-")))
			Expect(st.Attrs).To(HaveKey(meta.AttrOLHVer), "the state is the OLH head as read")
			Expect(c.Reads(testDataPool, "", olhHeadOID(version("")))).To(Equal(1), "read once")
		})

		It("answers a current delete marker as not existing, with DeleteMarker set", func(ctx SpecContext) {
			put(ctx, "v1")
			marker(ctx, "dm")
			for _, read := range []func(context.Context, *op.BucketRecord, meta.ObjKey) (*op.ObjectState, error){s.StatObject, s.PrefetchObject} {
				st, err := read(ctx, rec, version(""))
				Expect(err).NotTo(HaveOccurred())
				Expect(st).To(Equal(&op.ObjectState{Bucket: rec, Key: version(""), DeleteMarker: true}))
			}
		})

		It("answers an OLH head that no link reached as not existing", func(ctx SpecContext) {
			rw := newRGWWriter(c, s, rec, denc.Squid, "rgw")
			rw.ReadHead(ctx)
			Expect(rw.Init(ctx)).To(BeTrue())
			Expect(pending()).To(HaveLen(1))
			st := stat(ctx, version(""))
			Expect(st).To(Equal(&op.ObjectState{Bucket: rec, Key: version("")}),
				"the log read finds no OLH entry under the head's tag, ECANCELED, which follow_olh answers ENOENT")
		})

		It("applies the pending log a writer stopped before its replay left, and answers the version it links", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
			Expect(pending()).To(HaveLen(1))
			Expect(target()).To(Equal("v1"))

			st := stat(ctx, version(""))
			Expect(st.Key).To(Equal(version("v2")), "resolved from the head as the replay left it")
			Expect(pending()).To(BeEmpty())
			Expect(target()).To(Equal("v2"))
			e, ok := c.OLHEntry(rookIndexPool, "", shard, "k")
			Expect(ok).To(BeTrue())
			Expect(e.PendingLog).To(BeEmpty(), "the log is trimmed")
		})

		It("answers the next version when the replay it runs removes the current one", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			unlink(ctx, "v2", driver.OLHUnlink{SkipUpdate: true})
			Expect(target()).To(Equal("v2"), "the writer stopped between its unlink and its replay")
			Expect(versionHead("v2")).NotTo(BeNil())

			st := stat(ctx, version(""))
			Expect(st.Exists).To(BeTrue(), "radosgw's follow_olh answers the removed version, 404")
			Expect(st.Key).To(Equal(version("v1")))
			Expect(versionHead("v2")).To(BeNil(), "the replay removed the unlinked version")
		})

		It("resolves from its second head read without running the update again", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
			v3 := write(ctx, "v3")
			c.AfterWrite(testDataPool, "", olhHeadOID(version("")), func() {
				c.AfterWrite(testDataPool, "", olhHeadOID(version("")), nil)
				Expect(s.SetOLHForTest(ctx, rec, v3, driver.OLHLink{SkipUpdate: true})).To(Succeed())
			})
			st := stat(ctx, version(""))
			Expect(st.Key).To(Equal(version("v2")), "the head the update left, before the other writer's replay")
			Expect(pending()).To(HaveLen(1), "the other writer's link waits for the next reader")
		})

		It("answers the key as missing once the replay it runs clears the OLH", func(ctx SpecContext) {
			put(ctx, "v1")
			unlink(ctx, "v1", driver.OLHUnlink{SkipUpdate: true})
			st := stat(ctx, version(""))
			Expect(st).To(Equal(&op.ObjectState{Bucket: rec, Key: version("")}))
			Expect(olhHead()).To(BeNil(), "the replay cleared the OLH")
		})

		It("answers the head its second read finds when that head is no longer an OLH", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, "v2"), driver.OLHLink{SkipUpdate: true})).To(Succeed())
			reads := 0
			c.BeforeRead(testDataPool, "", olhHeadOID(version("")), func() {
				if reads++; reads == 2 {
					c.BeforeRead(testDataPool, "", olhHeadOID(version("")), nil)
					c.Put(testDataPool, "", olhHeadOID(version("")), []byte("plain"))
				}
			})
			st := stat(ctx, version(""))
			Expect([]any{st.Exists, st.Key, st.Size}).To(Equal([]any{true, version(""), uint64(len("plain"))}))
		})

		It("decodes the OLH head as any head before it follows it", func(ctx SpecContext) {
			put(ctx, "v1")
			olhHead().Xattrs[meta.AttrCompression] = []byte{0xff}
			_, err := s.StatObject(ctx, rec, version(""))
			Expect(err).To(MatchError(op.ErrUnknown), "get_obj_state_impl decodes the compression info before it checks is_olh")
		})
	})

	Describe("a named version", func() {
		It("reads the version's own head without following the OLH", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			c.ResetCounters()
			st := stat(ctx, version("v1"))
			Expect([]any{st.Exists, st.Key, st.ETag}).To(Equal([]any{true, version("v1"), "etag-v1"}))
			Expect(c.Reads(testDataPool, "", olhHeadOID(version("")))).To(BeZero())
			Expect(c.Reads(rookIndexPool, "", shard)).To(BeZero(), "a version found pays no index read")
		})

		It("answers a delete marker's version id as not existing, with DeleteMarker set from its index entry", func(ctx SpecContext) {
			put(ctx, "v1")
			marker(ctx, "dm")
			put(ctx, "v2")
			Expect(stat(ctx, version("dm"))).To(Equal(&op.ObjectState{Bucket: rec, Key: version("dm"), DeleteMarker: true}))
		})

		It("answers a missing version id as not existing, without DeleteMarker", func(ctx SpecContext) {
			put(ctx, "v1")
			Expect(stat(ctx, version("nope"))).To(Equal(&op.ObjectState{Bucket: rec, Key: version("nope")}))
		})

		It("takes a delete marker whose list entry is gone for a missing version", func(ctx SpecContext) {
			put(ctx, "v1")
			marker(ctx, "dm")
			put(ctx, "v2")
			unlink(ctx, "dm", driver.OLHUnlink{Epoch: 3})
			_, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "dm"})
			Expect(ok).To(BeTrue(), "an unlink at an older epoch leaves the marker's instance entry")
			Expect(stat(ctx, version("dm"))).To(Equal(&op.ObjectState{Bucket: rec, Key: version("dm")}))
		})

		It("reads no index for a missing version of a bucket whose versioning was never enabled", func(ctx SpecContext) {
			put(ctx, "v1")
			marker(ctx, "dm")
			plain := testBucket(putBucketID, 11)
			c.ResetCounters()
			st, err := s.StatObject(ctx, plain, version("dm"))
			Expect(err).NotTo(HaveOccurred())
			Expect(st).To(Equal(&op.ObjectState{Bucket: plain, Key: version("dm")}))
			Expect(c.Reads(rookIndexPool, "", shard)).To(BeZero())
		})

		Describe("null", func() {
			It("answers the null version the OLH head holds", func(ctx SpecContext) {
				Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
				withManifest()
				put(ctx, "v1")
				st := stat(ctx, version(meta.NullInstance))
				Expect([]any{st.Exists, st.Key, st.ETag}).To(Equal([]any{true, version(meta.NullInstance), "etag-"}))
				Expect(st.Manifest).NotTo(BeNil())
			})

			It("answers an OLH head without a manifest as no null version", func(ctx SpecContext) {
				put(ctx, "v1")
				Expect(olhHead()).NotTo(BeNil())
				Expect(stat(ctx, version(meta.NullInstance))).To(Equal(&op.ObjectState{Bucket: rec, Key: version(meta.NullInstance)}),
					"read null version, and the head object only have olh info")
			})

			It("answers a null delete marker as not existing, with DeleteMarker set", func(ctx SpecContext) {
				Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
				tag := slices.Clone(olhHead().Xattrs[meta.AttrIDTag])
				Expect(s.SetOLHForTest(ctx, rec, version(""), markerLink(tag))).To(Succeed())
				settle(s)
				Expect(stat(ctx, version(meta.NullInstance))).To(Equal(&op.ObjectState{Bucket: rec, Key: version(meta.NullInstance), DeleteMarker: true}),
					"found behind the version marker, at the null delete marker's own key")
				Expect(stat(ctx, version(""))).To(Equal(&op.ObjectState{Bucket: rec, Key: version(""), DeleteMarker: true}), "and current")
			})
		})
	})

	Describe("SetObjectAttrs on a version", func() {
		const attr = meta.AttrPrefix + "x-amz-meta-color"
		// unchanged checks, when the func it returns is called, that the
		// object get finds still holds the xattrs it holds now: a write op
		// replaces the cluster's object, so it is found again each time.
		unchanged := func(get func() *fakerados.Object, what string) func() {
			want := maps.Clone(get().Xattrs)
			return func() {
				GinkgoHelper()
				Expect(get().Xattrs).To(Equal(want), what)
			}
		}

		It("sets a noncurrent version's attrs and index entry and leaves the current version and the OLH head alone", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			settle(s)
			current := unchanged(func() *fakerados.Object { return versionHead("v2") }, "the current version's head")
			olh := unchanged(olhHead, "the OLH head")
			st := stat(ctx, version("v1"))
			c.ResetCounters()
			Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{attr: []byte("blue")}, nil)).To(Succeed())
			settle(s)

			Expect(versionHead("v1").Xattrs).To(HaveKeyWithValue(attr, []byte("blue")))
			Expect(versionHead("v1").Xattrs[meta.AttrIDTag]).NotTo(Equal([]byte("idtag-v1")), "a fresh write tag")
			en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "v1"})
			Expect(ok).To(BeTrue())
			Expect([]any{en.Meta.ETag, en.Flags}).To(Equal([]any{"etag-v1", rgwcls.FlagVer}))
			Expect(en.PendingMap).To(BeEmpty(), "the completion landed on the version's instance entry")
			current()
			olh()
			Expect(c.Writes(testDataPool, "", olhHeadOID(version("")))).To(BeZero())
			Expect(target()).To(Equal("v2"))
		})

		It("sets the current version's attrs through the state a follow resolved, the list entry kept current", func(ctx SpecContext) {
			put(ctx, "v1")
			put(ctx, "v2")
			settle(s)
			olh := unchanged(olhHead, "the OLH head")
			st := stat(ctx, version(""))
			Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{attr: []byte("red")}, nil)).To(Succeed())
			settle(s)

			Expect(versionHead("v2").Xattrs).To(HaveKeyWithValue(attr, []byte("red")))
			olh()
			en, _ := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k", Instance: "v2"})
			Expect(en.Flags).To(Equal(rgwcls.FlagVer), "the class's complete cuts the instance entry's flags to FLAG_VER")
			list := c.ListEntries(rookIndexPool, "", shard, "k")
			Expect(list[0].Key.Instance).To(Equal("v2"))
			Expect(list[0].Flags&rgwcls.FlagCurrent).To(Equal(rgwcls.FlagCurrent), "and leaves the list entry current, as radosgw's state")
		})

		It("sets the null version's attrs on the OLH head and its own instance entry, not the version marker", func(ctx SpecContext) {
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
			withManifest()
			put(ctx, "v1")
			settle(s)
			vm := c.Object(rookIndexPool, "", shard).Omap["k"]
			st := stat(ctx, version(meta.NullInstance))
			Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{attr: []byte("green")}, nil)).To(Succeed())
			settle(s)

			Expect(olhHead().Xattrs).To(HaveKeyWithValue(attr, []byte("green")))
			Expect(olhHead().Xattrs).To(HaveKey(meta.AttrOLHInfo), "the OLH attrs stay")
			Expect(c.Object(rookIndexPool, "", shard).Omap).To(HaveKeyWithValue("k", vm), "the version marker is untouched")
			en, ok := c.InstanceEntry(rookIndexPool, "", shard, rgwcls.ObjKey{Name: "k"})
			Expect(ok).To(BeTrue())
			Expect(en.PendingMap).To(BeEmpty())
			Expect(en.Tag+"\x00").To(Equal(string(olhHead().Xattrs[meta.AttrIDTag])), "the completion reached the null version's instance entry")
			Expect(c.Object(rookIndexPool, "", shard).Omap).NotTo(HaveKey(rgwcls.InstanceIndexKey(rgwcls.ObjKey{Name: "k", Instance: meta.NullInstance}, false)),
				"set_attrs clears the null instance before the index")
		})

		It("leaves the OLH attrs as they are on disk, so a link applied after the read survives the write", func(ctx SpecContext) {
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
			st := stat(ctx, version(""))
			Expect(st.Attrs).To(HaveKey(meta.AttrOLHInfo), "the null version is current, so the state is the OLH head")
			put(ctx, "v1")
			settle(s)
			linked := map[string][]byte{}
			for name, v := range olhHead().Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPrefix) {
					linked[name] = v
				}
			}
			Expect(target()).To(Equal("v1"))

			// The op writes the whole attrset it read back, as modify_obj_attrs does.
			set := maps.Clone(st.Attrs)
			set[attr] = []byte("blue")
			Expect(s.SetObjectAttrs(ctx, st, set, nil)).To(Succeed())
			settle(s)

			Expect(olhHead().Xattrs).To(HaveKeyWithValue(attr, []byte("blue")), "the attr change lands")
			Expect(target()).To(Equal("v1"), "the link applied between the read and the write stays")
			after := map[string][]byte{}
			for name, v := range olhHead().Xattrs {
				if strings.HasPrefix(name, meta.AttrOLHPrefix) {
					after[name] = v
				}
			}
			Expect(after).To(Equal(linked), "olh.ver, olh.info, olh.idtag and the pending attrs as the link left them")
		})

		It("removes no OLH attr it is asked to remove, and the others it is", func(ctx SpecContext) {
			Expect(s.SetOLHForTest(ctx, rec, write(ctx, ""), driver.OLHLink{})).To(Succeed())
			olhHead().Xattrs[attr] = []byte("old")
			st := stat(ctx, version(""))
			info := slices.Clone(olhHead().Xattrs[meta.AttrOLHInfo])
			Expect(s.SetObjectAttrs(ctx, st, nil, []string{meta.AttrOLHInfo, attr})).To(Succeed())
			settle(s)
			Expect(olhHead().Xattrs).NotTo(HaveKey(attr))
			Expect(olhHead().Xattrs).To(HaveKeyWithValue(meta.AttrOLHInfo, info))
		})
	})

	Describe("the driver's writers", func() {
		It("refuse an OLH head with NotImplemented and write nothing", func(ctx SpecContext) {
			put(ctx, "v1")
			plain := testBucket(putBucketID, 11)
			c.ResetCounters()
			_, err := s.DeleteObject(ctx, plain, version(""), op.DeleteParams{})
			Expect(err).To(MatchError(op.ErrNotImplemented), "an unversioned delete never removes a versioned key's OLH head")
			Expect(c.Writes(testDataPool, "", olhHeadOID(version("")))).To(BeZero())
			Expect(c.Writes(rookIndexPool, "", shard)).To(BeZero())
			Expect(olhHead()).NotTo(BeNil())
		})
	})
})
