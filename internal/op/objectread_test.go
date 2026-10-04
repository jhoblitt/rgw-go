package op_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

func encodedTags(s tags.Set) []byte {
	e := denc.NewEncoder()
	s.Encode(e, denc.Squid)
	return e.Bytes()
}

func encodedPolicy(p acl.Policy) []byte {
	e := denc.NewEncoder()
	p.Encode(e, denc.Squid)
	return e.Bytes()
}

// objectReadFixture is the memstore the object read specs run on: alice's
// bucket "plain", whose ACL names her "Alice", holding "small", 1024 bytes
// under the same ACL.
type objectReadFixture struct {
	store *memstore.Store
	env   *op.Env
	alice op.Identity
	rec   *op.BucketRecord
}

func newObjectReadFixture(ctx context.Context, release denc.Release) *objectReadFixture {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: release, Now: smallMtime})
	f := &objectReadFixture{store: store}
	f.env = &op.Env{
		Zone: store, Users: store, Buckets: store, Objects: store, Usage: store,
		Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
		Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "false", "rgw_crypt_require_ssl": "true"}),
	}
	a := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	f.alice = op.Identity{User: &a.Info, Owner: meta.UserOwner(a.Info.UserID), OpMask: op.OpTypeAll}
	aliceACL := encodedPolicy(acl.DefaultPolicy(f.alice.Owner, "Alice"))
	var err error
	f.rec, err = store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "plain", Owner: f.alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: aliceACL},
	})
	Expect(err).NotTo(HaveOccurred())
	_, err = store.PutObject(ctx, f.rec, meta.ObjKey{Name: "small"}, bytes.NewReader(payload(1024)),
		op.PutParams{Size: 1024, Attrs: map[string][]byte{meta.AttrACL: aliceACL}})
	Expect(err).NotTo(HaveOccurred())
	return f
}

func (f *objectReadFixture) req(key string) *op.Request {
	return &op.Request{Method: http.MethodGet, Bucket: "plain", Object: meta.ObjKey{Name: key}, Identity: f.alice, Env: f.env, Header: http.Header{}, Query: url.Values{}}
}

// setAttrs sets and removes attrs on key's head.
func (f *objectReadFixture) setAttrs(ctx context.Context, key string, set map[string][]byte, rm ...string) {
	GinkgoHelper()
	st, err := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred())
	Expect(f.store.SetObjectAttrs(ctx, st, set, rm)).To(Succeed())
}

var _ = Describe("GetObjectTagging", func() {
	var f *objectReadFixture
	BeforeEach(func(ctx SpecContext) { f = newObjectReadFixture(ctx, denc.Squid) })

	It("is radosgw's get_obj_tags, a read", func() {
		o := &op.GetObjectTagging{}
		Expect(o.Name()).To(Equal("get_obj_tags"))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
	})
	DescribeTable("authorizes the tagging action of the instance asked for, with that action's ACL permission",
		func(ctx SpecContext, instance string, want policy.Action) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := &op.GetObjectTagging{}
			r := f.req("small")
			r.Object.Instance = instance
			Expect(op.Run(ctx, o, r)).To(Succeed())
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := authz.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3GetObjectTagging),
		Entry("the null instance", "null", policy.S3GetObjectVersionTagging),
	)
	It("returns no tags for an object without the attr and the decoded set with it, logging no usage", func(ctx SpecContext) {
		o := &op.GetObjectTagging{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.HasTags).To(BeFalse())
		Expect(o.Tags.Tags).To(BeEmpty())

		set := tags.Set{}
		Expect(set.Add("k", "v", tags.MaxObjectTags)).To(Succeed())
		Expect(set.Add("a", "b", tags.MaxObjectTags)).To(Succeed())
		f.setAttrs(ctx, "small", map[string][]byte{tags.Attr: encodedTags(set)})
		o = &op.GetObjectTagging{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.HasTags).To(BeTrue())
		Expect(o.Tags.Tags).To(Equal([]tags.Tag{{Key: "a", Value: "b"}, {Key: "k", Value: "v"}}))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("reads the URL-encoded text some older objects store", func(ctx SpecContext) {
		f.setAttrs(ctx, "small", map[string][]byte{tags.Attr: []byte("k=v&a=b")})
		o := &op.GetObjectTagging{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.Tags.Tags).To(Equal([]tags.Tag{{Key: "a", Value: "b"}, {Key: "k", Value: "v"}}))
	})
	It("answers UnknownError, radosgw's -EIO, for tags that decode neither way", func(ctx SpecContext) {
		f.setAttrs(ctx, "small", map[string][]byte{tags.Attr: {0}})
		o := &op.GetObjectTagging{}
		err := op.Run(ctx, o, f.req("small"))
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err).To(MatchError(denc.ErrShortBuffer), "the binary decoder's error is kept")
	})
	It("is NoSuchKey for a missing object and NoSuchBucket for a missing bucket", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetObjectTagging{}, f.req("nope"))).To(MatchError(op.ErrNoSuchKey))
		r := f.req("small")
		r.Bucket = "nobucket"
		Expect(op.Run(ctx, &op.GetObjectTagging{}, r)).To(MatchError(op.ErrNoSuchBucket))
	})
	It("stats the head without a prefetch and leaves it in the request for the authorizer", func(ctx SpecContext) {
		objects := &opfakes.FakeObjectStore{}
		f.env.Objects = objects
		st := &op.ObjectState{Exists: true, Attrs: map[string][]byte{}}
		objects.StatObjectReturns(st, nil)
		r := f.req("k")
		Expect(op.Run(ctx, &op.GetObjectTagging{}, r)).To(Succeed())
		Expect(objects.StatObjectCallCount()).To(Equal(1))
		Expect(objects.PrefetchObjectCallCount()).To(BeZero())
		Expect(r.ObjState).To(BeIdenticalTo(st))
	})
})

var _ = Describe("GetObjectACL", func() {
	var f *objectReadFixture
	BeforeEach(func(ctx SpecContext) { f = newObjectReadFixture(ctx, denc.Squid) })

	It("is radosgw's get_acls, a read", func() {
		o := &op.GetObjectACL{}
		Expect(o.Name()).To(Equal("get_acls"))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
	})
	DescribeTable("authorizes the ACL action of the instance asked for, with that action's ACL permission",
		func(ctx SpecContext, instance string, want policy.Action) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := &op.GetObjectACL{}
			r := f.req("small")
			r.Object.Instance = instance
			Expect(op.Run(ctx, o, r)).To(Succeed())
			Expect(o.Action()).To(Equal(want))
			_, _, a, perm := authz.VerifyObjectArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermReadACP))
		},
		Entry("no instance", "", policy.S3GetObjectAcl),
		Entry("the null instance", "null", policy.S3GetObjectVersionAcl),
	)
	It("returns the stored policy, logging no usage", func(ctx SpecContext) {
		o := &op.GetObjectACL{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.Policy).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")))
		Expect(string(o.Policy.MarshalS3XML())).To(ContainSubstring("<Permission>FULL_CONTROL</Permission>"))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("generates the bucket owner's default policy when the head has no acl attr", func(ctx SpecContext) {
		f.setAttrs(ctx, "small", nil, meta.AttrACL)
		o := &op.GetObjectACL{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.Policy).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")))
	})
	It("takes that owner from the bucket's ACL, not from the bucket's info", func(ctx SpecContext) {
		bob := meta.UserOwner(meta.UserID{ID: "bob"})
		Expect(f.store.PutBucketAttrs(ctx, f.rec, map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(bob, "Bob"))}, nil)).To(Succeed())
		f.setAttrs(ctx, "small", nil, meta.AttrACL)
		o := &op.GetObjectACL{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.Policy).To(Equal(acl.DefaultPolicy(bob, "Bob")), "s->bucket_owner is the bucket ACL's owner")
	})
	It("names the bucket owner with no display name when the bucket has no ACL either", func(ctx SpecContext) {
		Expect(f.store.PutBucketAttrs(ctx, f.rec, nil, []string{meta.AttrACL})).To(Succeed())
		f.setAttrs(ctx, "small", nil, meta.AttrACL)
		o := &op.GetObjectACL{}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.Policy).To(Equal(acl.DefaultPolicy(f.alice.Owner, "")))
	})
	It("is NoSuchKey for a missing object", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetObjectACL{}, f.req("nope"))).To(MatchError(op.ErrNoSuchKey))
	})
	It("answers UnknownError, radosgw's -EIO, for an object ACL that does not decode", func(ctx SpecContext) {
		f.setAttrs(ctx, "small", map[string][]byte{meta.AttrACL: {0xff}})
		Expect(op.Run(ctx, &op.GetObjectACL{}, f.req("small"))).To(MatchError(op.ErrUnknown))
	})
	It("answers UnknownError for a bucket ACL that does not decode when it builds the default", func(ctx SpecContext) {
		Expect(f.store.PutBucketAttrs(ctx, f.rec, map[string][]byte{meta.AttrACL: {0xff}}, nil)).To(Succeed())
		Expect(op.Run(ctx, &op.GetObjectACL{}, f.req("small"))).To(Succeed(),
			"the op itself decodes the bucket ACL only to build a default; OwnerOnly reads no ACL")
		f.setAttrs(ctx, "small", nil, meta.AttrACL)
		Expect(op.Run(ctx, &op.GetObjectACL{}, f.req("small"))).To(MatchError(op.ErrUnknown))
	})
})

var _ = Describe("BucketACLFor", func() {
	owner := func() meta.Owner { return meta.UserOwner(meta.UserID{Tenant: "t", ID: "carol"}) }
	rec := func(attrs map[string][]byte) *op.BucketRecord {
		r := &op.BucketRecord{Info: meta.NewBucketInfo(), Attrs: attrs}
		r.Info.Owner = owner()
		r.Info.Bucket.Name = "b"
		return r
	}
	It("is the bucket's stored ACL", func() {
		stored := acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "dave"}), "Dave")
		p, err := op.BucketACLFor(rec(map[string][]byte{meta.AttrACL: encodedPolicy(stored)}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(stored))
	})
	It("is FULL_CONTROL to the bucket's owner, with no display name, when the bucket has no ACL", func() {
		p, err := op.BucketACLFor(rec(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(acl.DefaultPolicy(owner(), "")))
	})
	It("answers UnknownError, radosgw's -EIO, for an ACL that does not decode", func() {
		_, err := op.BucketACLFor(rec(map[string][]byte{meta.AttrACL: {}}))
		Expect(err).To(MatchError(op.ErrUnknown))
	})
})

var _ = Describe("GetObjectAttributes", func() {
	var f *objectReadFixture
	BeforeEach(func(ctx SpecContext) { f = newObjectReadFixture(ctx, denc.Squid) })

	It("is radosgw's get_obj_attrs, a read, first authorizing GetObject for the instance asked for", func() {
		o := &op.GetObjectAttributes{}
		Expect(o.Name()).To(Equal("get_obj_attrs"))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
		Expect(o.Action()).To(Equal(policy.S3GetObject))
		o.Versioned = true
		Expect(o.Action()).To(Equal(policy.S3GetObjectVersion))
	})
	DescribeTable("ParseObjectAttrs is recognize_attrs",
		func(h string, want op.ObjectAttrs) { Expect(op.ParseObjectAttrs(h)).To(Equal(want)) },
		Entry("all five, in any case", "ETag,Checksum,objectparts,STORAGECLASS,ObjectSize",
			op.AttrETag|op.AttrChecksum|op.AttrObjectParts|op.AttrStorageClass|op.AttrObjectSize),
		Entry("an unknown name is ignored", "ETag,Bogus", op.AttrETag),
		Entry("empty items are skipped", ",,ObjectSize,,", op.AttrObjectSize),
		Entry("whitespace around a name is not trimmed", "ETag, ObjectSize", op.AttrETag),
		Entry("only ASCII letters fold, as in the C locale", "objectſize", op.ObjectAttrs(0)),
		Entry("empty", "", op.ObjectAttrs(0)),
	)
	It("stats without a prefetch and reports a single-part object's size and no parts, logging no usage", func(ctx SpecContext) {
		o := &op.GetObjectAttributes{Requested: op.AttrETag | op.AttrObjectSize | op.AttrObjectParts | op.AttrStorageClass}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.State.Head).To(BeNil())
		Expect(o.State.ETag).To(Equal(md5Hex(payload(1024))))
		Expect(o.ObjSize).To(BeEquivalentTo(1024))
		Expect(o.PartsCount).To(BeNil())
		Expect(o.Parts).To(BeEmpty())
		Expect(o.PartsTruncated).To(BeFalse())
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("ignores the GetObject inputs radosgw's get_params for it never reads", func(ctx SpecContext) {
		five := 5
		o := &op.GetObjectAttributes{}
		o.GetData, o.Range, o.IfMatch, o.IfNoneMatch = true, "bytes=0-1", `"nope"`, `"`+md5Hex(payload(1024))+`"`
		o.IfModifiedSince, o.IfUnmodifiedSince = "yesterday", "yesterday"
		o.PartNumber, o.Torrent, o.ResponseOverrides = &five, true, map[string]string{"Content-Type": "a\r\nb"}
		Expect(op.Run(ctx, o, f.req("small"))).To(Succeed())
		Expect(o.State.Head).To(BeNil(), "no data, so no prefetch")
		Expect(o.Partial).To(BeFalse())
		Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{0, 1024}))
	})
	It("answers NoSuchKey for a missing object and NoSuchBucket for a missing bucket", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("nope"))).To(MatchError(op.ErrNoSuchKey))
		r := f.req("small")
		r.Bucket = "nobucket"
		Expect(op.Run(ctx, &op.GetObjectAttributes{}, r)).To(MatchError(op.ErrNoSuchBucket))
	})

	Describe("on a fake store", func() {
		const prefix = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		var objects *opfakes.FakeObjectStore
		BeforeEach(func() {
			objects = &opfakes.FakeObjectStore{}
			f.env.Objects = objects
		})
		mpHead := func() *op.ObjectState {
			m := decodeGoldenManifest("squid-multipart")
			return &op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, ETag: "mp-3", Key: meta.ObjKey{Name: "k"}, Attrs: map[string][]byte{meta.AttrETag: []byte("mp-3")}}
		}
		// partHead is a part head as radosgw writes it: no manifest of its
		// own, and its first stripe's worth of data.
		partHead := func() *op.ObjectState {
			return &op.ObjectState{Exists: true, Size: 4 << 20, Attrs: map[string][]byte{}}
		}
		// serve answers the head with head and each later stat with the next
		// of parts, then runs o on "k".
		serve := func(ctx context.Context, o *op.GetObjectAttributes, head *op.ObjectState, parts ...*op.ObjectState) error {
			objects.StatObjectReturnsOnCall(0, head, nil)
			for i, p := range parts {
				objects.StatObjectReturnsOnCall(i+1, p, nil)
			}
			return op.Run(ctx, o, f.req("k"))
		}
		statKeys := func() []string {
			var keys []string
			for i := 1; i < objects.StatObjectCallCount(); i++ {
				_, _, key := objects.StatObjectArgsForCall(i)
				keys = append(keys, key.NS+"/"+key.Name)
			}
			return keys
		}

		It("lists a multipart object's parts, stating each part head and sizing it from the manifest", func(ctx SpecContext) {
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, mpHead(), partHead(), partHead(), partHead())).To(Succeed())
			Expect(*o.PartsCount).To(Equal(3), "counted on Squid too")
			Expect(o.Parts).To(Equal([]op.ObjectPart{{Number: 1, Size: 8 << 20}, {Number: 2, Size: 8 << 20}, {Number: 3, Size: 4 << 20}}))
			Expect(o.PartsTruncated).To(BeFalse())
			Expect(o.NextPartMarker).To(Equal(3))
			Expect(objects.PrefetchObjectCallCount()).To(BeZero())
			Expect(objects.ReadObjectCallCount()).To(BeZero(), "no data is read")
			Expect(statKeys()).To(Equal([]string{"multipart/" + prefix + ".1", "multipart/" + prefix + ".2", "multipart/" + prefix + ".3"}))
			_, rec, _ := objects.StatObjectArgsForCall(1)
			Expect(rec.Info.Bucket.ID).To(Equal("e7bceed5-d2a6-4b0b-b484-cac20e0beb53.4156.1"), "a copy's parts live in its source bucket")
			Expect(rec.Info.PlacementRule).To(Equal(f.rec.Info.PlacementRule), "under the request bucket's placement")
		})
		It("lists nothing, and reads no part head, unless ObjectParts is asked for", func(ctx SpecContext) {
			o := &op.GetObjectAttributes{Requested: op.AttrETag | op.AttrObjectSize}
			Expect(serve(ctx, o, mpHead())).To(Succeed())
			Expect(*o.PartsCount).To(Equal(3))
			Expect(o.Parts).To(BeEmpty())
			Expect(objects.StatObjectCallCount()).To(Equal(1))
		})
		DescribeTable("pages as list_parts does",
			func(ctx SpecContext, maxParts, marker *int, want []int, truncated bool, next int) {
				o := &op.GetObjectAttributes{Requested: op.AttrObjectParts, MaxParts: maxParts, PartMarker: marker}
				Expect(serve(ctx, o, mpHead(), partHead(), partHead(), partHead())).To(Succeed())
				got := make([]int, 0, len(o.Parts))
				for _, p := range o.Parts {
					got = append(got, p.Number)
				}
				Expect(got).To(Equal(want))
				Expect(o.PartsTruncated).To(Equal(truncated))
				Expect(o.NextPartMarker).To(Equal(next))
				Expect(objects.StatObjectCallCount()).To(Equal(1+len(want)), "a part head is read only for a part listed")
			},
			Entry("max-parts 2", new(2), nil, []int{1, 2}, true, 2),
			Entry("after part 1", nil, new(1), []int{2, 3}, false, 3),
			Entry("one part after part 2, which is the last: not truncated", new(1), new(2), []int{3}, false, 3),
			Entry("a marker at the count lists nothing", nil, new(3), []int{}, false, 3),
			Entry("a negative marker names no part", nil, new(-1), []int{}, false, -1),
			Entry("max-parts 0 truncates at the first part, resuming at the marker", new(0), new(1), []int{}, true, 1),
		)
		It("counts and pages an object whose part numbers skip one as list_parts does", func(ctx SpecContext) {
			gapped := func() *op.ObjectState {
				m := meta.NewManifest()
				m.Obj = meta.Obj{Bucket: f.rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}}
				m.ObjSize, m.Prefix = 16<<20, "p"
				m.Rules = map[uint64]meta.ManifestRule{
					0:       {StartPartNum: 1, PartSize: 8 << 20, StripeMaxSize: 4 << 20},
					8 << 20: {StartPartNum: 3, StartOfs: 8 << 20, PartSize: 8 << 20, StripeMaxSize: 4 << 20},
				}
				return &op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, Attrs: map[string][]byte{}}
			}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, gapped(), partHead(), partHead())).To(Succeed())
			Expect(*o.PartsCount).To(Equal(3), "the count is the last part's number")
			Expect(o.Parts).To(Equal([]op.ObjectPart{{Number: 1, Size: 8 << 20}, {Number: 3, Size: 8 << 20}}))
			Expect(o.NextPartMarker).To(Equal(2), "two parts listed after marker 0")

			objects = &opfakes.FakeObjectStore{}
			f.env.Objects = objects
			o = &op.GetObjectAttributes{Requested: op.AttrObjectParts, PartMarker: new(1)}
			Expect(serve(ctx, o, gapped())).To(Succeed())
			Expect(o.Parts).To(BeEmpty(), "there is no part 2 to resume at")
			Expect(o.PartsTruncated).To(BeFalse())
		})
		It("lists the one part of a single-part upload, whose rule has no part size", func(ctx SpecContext) {
			m := meta.NewManifest()
			m.Obj = meta.Obj{Bucket: f.rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}}
			m.ObjSize, m.Prefix = 10<<20, "p"
			m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, StripeMaxSize: 4 << 20}}
			head := &op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, Attrs: map[string][]byte{}}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, head, partHead())).To(Succeed())
			Expect(*o.PartsCount).To(Equal(1))
			Expect(o.Parts).To(Equal([]op.ObjectPart{{Number: 1, Size: 10 << 20}}))
			Expect(statKeys()).To(Equal([]string{"multipart/p.1"}))
		})
		It("sizes a compressed object's parts from their heads' compression info", func(ctx SpecContext) {
			head := mpHead()
			head.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 60 << 20, Blocks: []meta.CompressionBlock{{Len: 1}}}
			var parts []*op.ObjectState
			for i, typ := range []string{"zlib", "zlib", "none"} {
				p := partHead()
				p.Compression = &meta.CompressionInfo{Type: typ, OrigSize: uint64(i+1) * 10 << 20, Blocks: []meta.CompressionBlock{{Len: 1}}}
				parts = append(parts, p)
			}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts | op.AttrObjectSize}
			Expect(serve(ctx, o, head, parts...)).To(Succeed())
			Expect(o.ObjSize).To(BeEquivalentTo(60 << 20))
			Expect(o.Parts).To(Equal([]op.ObjectPart{{Number: 1, Size: 10 << 20}, {Number: 2, Size: 20 << 20}, {Number: 3, Size: 30 << 20}}),
				"accounted_size is orig_size whatever the compression type")
		})
		It("sizes a part whose head has a manifest of its own by that manifest", func(ctx SpecContext) {
			own := partHead()
			pm := meta.NewManifest()
			pm.ObjSize = 7 << 20
			own.Manifest, own.Size = &pm, 7<<20
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, mpHead(), partHead(), own, partHead())).To(Succeed())
			Expect(o.Parts[1]).To(Equal(op.ObjectPart{Number: 2, Size: 7 << 20}))
		})
		It("lists a part whose head is missing at its manifest size, as get_obj_state's ENOENT is no failure", func(ctx SpecContext) {
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, mpHead(), partHead(), &op.ObjectState{}, partHead())).To(Succeed())
			Expect(o.Parts).To(Equal([]op.ObjectPart{{Number: 1, Size: 8 << 20}, {Number: 2, Size: 8 << 20}, {Number: 3, Size: 4 << 20}}))
		})
		It("stops at a part head it cannot read, keeping the parts listed so far", func(ctx SpecContext) {
			objects.StatObjectReturnsOnCall(0, mpHead(), nil)
			objects.StatObjectReturnsOnCall(1, partHead(), nil)
			objects.StatObjectReturnsOnCall(2, nil, op.ErrServiceUnavailable)
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(op.Run(ctx, o, f.req("k"))).To(Succeed())
			Expect(o.Parts).To(Equal([]op.ObjectPart{{Number: 1, Size: 8 << 20}}))
			Expect(o.PartsTruncated).To(BeFalse())
			Expect(o.NextPartMarker).To(Equal(1))
			Expect(objects.StatObjectCallCount()).To(Equal(3), "part 3's head is not read")
		})
		It("fails where the manifest walk does not move forward, where radosgw's never ends", func(ctx SpecContext) {
			m := meta.NewManifest()
			m.Obj = meta.Obj{Bucket: f.rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}}
			m.ObjSize, m.Prefix = 16<<20, "p"
			m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, PartSize: 8 << 20}}
			head := &op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, Attrs: map[string][]byte{}}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			err := serve(ctx, o, head, partHead())
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(errors.Is(err, denc.ErrMalformed)).To(BeTrue(), "%v", err)
			Expect(objects.StatObjectCallCount()).To(Equal(2), "the count is taken, part 1's head read, and the walk past part 1 fails")
		})
		It("refuses a manifest whose tail needs more stripes than the walk's bound before it reads a part head", func(ctx SpecContext) {
			size := uint64(meta.MaxWalkStripes) + 1
			m := meta.NewManifest()
			m.Obj = meta.Obj{Bucket: f.rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}}
			m.ObjSize, m.Prefix = size, "p"
			m.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: 1, PartSize: size, StripeMaxSize: 1}}
			head := &op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, Attrs: map[string][]byte{}}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			err := serve(ctx, o, head, partHead())
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(err).To(MatchError(meta.ErrTooManyStripes))
			Expect(objects.StatObjectCallCount()).To(Equal(1), "the object's head alone")
		})
		It("returns a canceled request's error rather than ending the list as a failed part head does", func(ctx SpecContext) {
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			objects.StatObjectCalls(func(c context.Context, _ *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
				if key.NS == "" {
					return mpHead(), nil
				}
				cancel()
				return nil, c.Err()
			})
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(op.Run(cctx, o, f.req("k"))).To(MatchError(context.Canceled))
			Expect(o.Parts).To(BeEmpty())
		})
		DescribeTable("refuses as GetObject does",
			func(ctx SpecContext, mut func(st *op.ObjectState), want error) {
				st := &op.ObjectState{Exists: true, Size: 3, Key: meta.ObjKey{Name: "k"}, Attrs: map[string][]byte{}}
				mut(st)
				o := &op.GetObjectAttributes{}
				o.Secure = true
				Expect(serve(ctx, o, st)).To(MatchError(want))
			},
			Entry("SSE-KMS without a key server", func(st *op.ObjectState) {
				st.Attrs[meta.AttrCryptMode], st.Attrs[meta.AttrCryptKeyID] = []byte("SSE-KMS"), []byte("kid")
			}, op.ErrInvalidArgument),
			Entry("SSE-C without the customer key", func(st *op.ObjectState) {
				st.Attrs[meta.AttrCryptMode] = []byte("SSE-C-AES256")
			}, op.ErrInvalidArgument),
			Entry("compression info without blocks", func(st *op.ObjectState) {
				st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 9}
			}, op.ErrUnknown),
			Entry("a Swift large object", func(st *op.ObjectState) {
				st.Attrs[meta.AttrSLOManifest] = []byte("x")
			}, op.ErrNotImplemented),
		)
		It("makes no cloud-tier check on Tentacle, as a HEAD does not", func(ctx SpecContext) {
			zone := &opfakes.FakeZoneInfo{}
			zone.ReleaseReturns(denc.Tentacle)
			f.env.Zone = zone
			m := meta.NewManifest()
			m.TierType = meta.TierTypeCloudS3
			st := &op.ObjectState{Exists: true, Size: 3, Manifest: &m, Key: meta.ObjKey{Name: "k"}, Attrs: map[string][]byte{meta.AttrStorageClass: []byte("CLOUDTIER")}}
			Expect(serve(ctx, &op.GetObjectAttributes{}, st)).To(Succeed())
		})
	})

	Describe("authorization", func() {
		var authz *opfakes.FakeAuthorizer
		// deny refuses the actions named and allows every other.
		deny := func(actions ...policy.Action) {
			authz.VerifyObjectCalls(func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
				if slices.Contains(actions, a) {
					return op.ErrAccessDenied
				}
				return nil
			})
		}
		actions := func() []policy.Action {
			var got []policy.Action
			for i := range authz.VerifyObjectCallCount() {
				_, _, a, perm := authz.VerifyObjectArgsForCall(i)
				Expect(perm).To(Equal(acl.PermRead), "%v asks READ", a)
				got = append(got, a)
			}
			return got
		}
		Context("on Tentacle", func() {
			BeforeEach(func(ctx SpecContext) {
				f = newObjectReadFixture(ctx, denc.Tentacle)
				authz = &opfakes.FakeAuthorizer{}
				f.env.Authz = authz
			})
			It("allows a request s3:GetObject refuses when s3:GetObjectAttributes allows it", func(ctx SpecContext) {
				deny(policy.S3GetObject)
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))).To(Succeed())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectAttributes}))
			})
			It("asks for the version actions for a version", func(ctx SpecContext) {
				deny(policy.S3GetObjectVersion)
				r := f.req("small")
				r.Object.Instance = "null"
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, r)).To(Succeed())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes}))
			})
			It("refuses with the second action's plain denial when both refuse", func(ctx SpecContext) {
				deny(policy.S3GetObject, policy.S3GetObjectAttributes)
				err := op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.AsError(err).Message).To(BeEmpty(), "unlike GetObject, it names no missing permission")
				Expect(actions()).To(HaveLen(2))
			})
			It("returns a refusal made before verify_permission, such as a missing object's, from the first action alone", func(ctx SpecContext) {
				authz.VerifyObjectReturns(op.BeforeVerify(op.ErrAccessDenied))
				err := op.Run(ctx, &op.GetObjectAttributes{}, f.req("nope"))
				Expect(op.IsBeforeVerify(err)).To(BeTrue())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
			})
			It("returns an error that is no access denial from the first action alone", func(ctx SpecContext) {
				authz.VerifyObjectReturns(op.ErrUnknown)
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))).To(MatchError(op.ErrUnknown))
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
			})
		})
		Context("on Squid", func() {
			BeforeEach(func() {
				authz = &opfakes.FakeAuthorizer{}
				f.env.Authz = authz
			})
			It("lets s3:GetObject alone decide, never asking for the action Squid lacks", func(ctx SpecContext) {
				deny(policy.S3GetObject)
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))).To(MatchError(op.ErrAccessDenied))
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
			})
		})
	})
})
