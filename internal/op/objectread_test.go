package op_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	authzpkg "github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cksum"
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

// captureLogs sends slog's default logger to a buffer until the spec ends.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so the cleanup puts
// log's writer and flags back too.
func captureLogs() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

const noObjectACL = "couldn't find acl header for object, generating default"

var _ = Describe("ObjectACLFor", func() {
	var (
		bucket *op.BucketRecord
		st     *op.ObjectState
		alice  meta.Owner
	)

	BeforeEach(func() {
		alice = meta.UserOwner(meta.UserID{ID: "alice"})
		bucket = &op.BucketRecord{Info: meta.NewBucketInfo(), Attrs: map[string][]byte{
			meta.AttrACL: encodedPolicy(acl.DefaultPolicy(alice, "Alice")),
		}}
		bucket.Info.Bucket.Name = "b"
		bucket.Info.Owner = meta.UserOwner(meta.UserID{ID: "carol"})
		st = &op.ObjectState{Key: meta.ObjKey{Name: "o"}, Exists: true, Attrs: map[string][]byte{}}
	})

	It("is the head's stored ACL, leaving the bucket's unread", func(ctx SpecContext) {
		stored := acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "dave"}), "Dave")
		st.Attrs[meta.AttrACL] = encodedPolicy(stored)
		bucket.Attrs[meta.AttrACL] = []byte{0xff}
		Expect(op.ObjectACLFor(ctx, st, bucket)).To(Equal(stored))
	})

	It("is FULL_CONTROL to the owner the bucket's ACL names, under its display name, for a head without one", func(ctx SpecContext) {
		Expect(op.ObjectACLFor(ctx, st, bucket)).To(Equal(acl.DefaultPolicy(alice, "Alice")))
	})

	It("answers UnknownError for an ACL that does not decode, and NoSuchKey for a missing object", func(ctx SpecContext) {
		st.Attrs[meta.AttrACL] = []byte{0xff}
		_, err := op.ObjectACLFor(ctx, st, bucket)
		Expect(err).To(MatchError(op.ErrUnknown))
		_, err = op.ObjectACLFor(ctx, nil, bucket)
		Expect(err).To(MatchError(op.ErrNoSuchKey), "nil state")
		st.Exists = false
		_, err = op.ObjectACLFor(ctx, st, bucket)
		Expect(err).To(MatchError(op.ErrNoSuchKey), "not existing")
	})

	It("warns about a head without an ACL once per state", func(ctx SpecContext) {
		logs := captureLogs()
		for range 2 {
			_, err := op.ObjectACLFor(ctx, st, bucket)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(strings.Count(logs.String(), noObjectACL)).To(Equal(1), logs.String())
		other := &op.ObjectState{Key: meta.ObjKey{Name: "o"}, Exists: true}
		_, err := op.ObjectACLFor(ctx, other, bucket)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(logs.String(), noObjectACL)).To(Equal(2), "another request's state")
	})

	It("warns once for a GetObjectAcl whose authorizer reads the ACL too", func(ctx SpecContext) {
		f := newObjectReadFixture(ctx, denc.Squid)
		f.setAttrs(ctx, "small", nil, meta.AttrACL)
		authz := &opfakes.FakeAuthorizer{}
		authz.VerifyObjectStub = func(ctx context.Context, r *op.Request, _ policy.Action, _ acl.Permission) error {
			_, err := op.ObjectACLFor(ctx, r.ObjState, r.BucketRec)
			return err
		}
		f.env.Authz = authz
		logs := captureLogs()
		Expect(op.Run(ctx, &op.GetObjectACL{}, f.req("small"))).To(Succeed())
		Expect(authz.VerifyObjectCallCount()).To(Equal(1))
		Expect(strings.Count(logs.String(), noObjectACL)).To(Equal(1), logs.String())
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
	It("is the bucket's stored ACL", func(ctx SpecContext) {
		stored := acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "dave"}), "Dave")
		p, err := op.BucketACLFor(ctx, rec(map[string][]byte{meta.AttrACL: encodedPolicy(stored)}))
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(stored))
	})
	It("is FULL_CONTROL to the bucket's owner, with no display name, when the bucket has no ACL", func(ctx SpecContext) {
		p, err := op.BucketACLFor(ctx, rec(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(acl.DefaultPolicy(owner(), "")))
	})
	It("answers UnknownError, radosgw's -EIO, for an ACL that does not decode", func(ctx SpecContext) {
		_, err := op.BucketACLFor(ctx, rec(map[string][]byte{meta.AttrACL: {}}))
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
		It("gives each part its own head's checksum, and none to a part whose head has none", func(ctx SpecContext) {
			var parts []*op.ObjectState
			for _, c := range []*cksum.Cksum{
				{Type: cksum.CRC32C, Digest: []byte{1, 2, 3, 4}, Flags: cksum.FlagFullObject},
				nil,
				{Type: cksum.CRC32C, Digest: []byte{5, 6, 7, 8}, Flags: cksum.FlagFullObject},
			} {
				p := partHead()
				p.Checksum = c
				parts = append(parts, p)
			}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, mpHead(), parts...)).To(Succeed())
			Expect(o.Parts).To(HaveExactElements(
				HaveField("Checksum", Equal(parts[0].Checksum)),
				HaveField("Checksum", BeNil()),
				HaveField("Checksum", Equal(parts[2].Checksum)),
			), "never the part's before it")
		})
		It("shows no part checksum of type none, as radosgw's ObjectParts skips one", func(ctx SpecContext) {
			none := partHead()
			none.Checksum = &cksum.Cksum{Flags: cksum.FlagV2}
			o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
			Expect(serve(ctx, o, mpHead(), none, partHead(), partHead())).To(Succeed())
			Expect(o.Parts).To(HaveLen(3))
			Expect(o.Parts[0].Checksum).To(BeNil())
		})
		DescribeTable("reports the object's checksum, when Checksum is asked for, by the GET's whole-object rules, never reading x-amz-checksum-mode",
			func(ctx SpecContext, requested op.ObjectAttrs, multipart bool, c *cksum.Cksum, want *op.ReadChecksum) {
				head := &op.ObjectState{Exists: true, Size: 10, Key: meta.ObjKey{Name: "k"}, Attrs: map[string][]byte{}}
				if multipart {
					head = mpHead()
				}
				head.Checksum = c
				o := &op.GetObjectAttributes{Requested: requested}
				o.ChecksumMode = "enabled"
				Expect(serve(ctx, o, head)).To(Succeed())
				Expect(o.Checksum).To(Equal(want))
			},
			Entry("a multipart object's composite, with its parts count, on Squid too", op.AttrChecksum, true,
				&cksum.Cksum{Type: cksum.SHA1, Digest: make([]byte, 20), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite},
				&op.ReadChecksum{Type: cksum.SHA1, Value: "AAAAAAAAAAAAAAAAAAAAAAAAAAA=-3", ChecksumType: cksum.CompositeType}),
			Entry("a multipart object's combined CRC, bare", op.AttrChecksum, true,
				&cksum.Cksum{Type: cksum.CRC32, Digest: []byte{1, 2, 3, 4}, Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagFullObject},
				&op.ReadChecksum{Type: cksum.CRC32, Value: "AQIDBA==", ChecksumType: cksum.FullObjectType}),
			Entry("a PutObject's record", op.AttrChecksum, false,
				&cksum.Cksum{Type: cksum.CRC32, Digest: []byte{1, 2, 3, 4}},
				&op.ReadChecksum{Type: cksum.CRC32, Value: "AQIDBA==", ChecksumType: cksum.FullObjectType}),
			Entry("a composite on an object without parts, which shows nothing", op.AttrChecksum, false,
				&cksum.Cksum{Type: cksum.SHA1, Digest: make([]byte, 20), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite},
				(*op.ReadChecksum)(nil)),
			Entry("Checksum not asked for", op.AttrETag|op.AttrObjectSize, true,
				&cksum.Cksum{Type: cksum.CRC32, Digest: []byte{1, 2, 3, 4}, Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagFullObject},
				(*op.ReadChecksum)(nil)),
		)
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
			Entry("a crypt mode rgw-go does not know, whose attributes radosgw answers", func(st *op.ObjectState) {
				st.Attrs[meta.AttrCryptMode] = []byte("AES256-GCM")
			}, op.ErrNotImplemented),
			Entry("compression info without blocks", func(st *op.ObjectState) {
				st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 9}
			}, op.ErrUnknown),
			Entry("a Swift large object", func(st *op.ObjectState) {
				st.Attrs[meta.AttrSLOManifest] = []byte("x")
			}, op.ErrNotImplemented),
		)
		// rgw_s3_prepare_decrypt reads the algorithm header as beast files it
		// on both releases: v19.2.7 answers ?attributes as a GET, v20.2.4's
		// get_decrypt_filter makes the same checks without a decryption.
		DescribeTable("reads an SSE-C object's algorithm header as sent, on both releases",
			func(ctx SpecContext, algorithm *string, want error) {
				zone := &opfakes.FakeZoneInfo{}
				zone.ReleaseReturns(denc.Tentacle)
				for rel, z := range map[denc.Release]op.ZoneInfo{denc.Squid: f.store, denc.Tentacle: zone} {
					f.env.Zone = z
					objects = &opfakes.FakeObjectStore{}
					f.env.Objects = objects
					st := &op.ObjectState{Exists: true, Size: 3, Key: meta.ObjKey{Name: "k"}, Attrs: map[string][]byte{meta.AttrCryptMode: []byte("SSE-C-AES256")}}
					o := &op.GetObjectAttributes{}
					o.Secure, o.SSECAlgorithm = true, algorithm
					Expect(serve(ctx, o, st)).To(MatchError(want), "release %d", rel)
				}
			},
			Entry("absent: radosgw's EINVAL", nil, op.ErrInvalidArgument),
			Entry("sent empty, which beast files and strcmp refuses", new(""), op.ErrInvalidEncryptionAlgorithm),
			Entry("AES128", new("AES128"), op.ErrInvalidEncryptionAlgorithm),
			Entry("AES256 without a key", new("AES256"), op.ErrInvalidArgument),
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
			It("allows a request both s3:GetObject and s3:GetObjectAttributes allow, asking for them in that order", func(ctx SpecContext) {
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))).To(Succeed())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectAttributes}))
			})
			It("refuses a request s3:GetObject refuses without asking for s3:GetObjectAttributes, which would allow it", func(ctx SpecContext) {
				deny(policy.S3GetObject)
				err := op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.AsError(err).Message).To(BeEmpty(), "unlike GetObject, it names no missing permission")
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
			})
			It("refuses a request s3:GetObjectAttributes refuses though s3:GetObject allows it", func(ctx SpecContext) {
				deny(policy.S3GetObjectAttributes)
				err := op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.IsBeforeVerify(err)).To(BeFalse())
				Expect(op.AsError(err).Message).To(BeEmpty())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectAttributes}))
			})
			// versionCase is the actions the authorizer refuses, whether the
			// request is refused, and the actions it is asked for, in order.
			type versionCase struct {
				denied  []policy.Action
				refused bool
				asked   []policy.Action
			}
			DescribeTable("asks for both version actions for a version, and refuses when either refuses",
				func(ctx SpecContext, c versionCase) {
					deny(c.denied...)
					r := f.req("small")
					r.Object.Instance = "null"
					err := op.Run(ctx, &op.GetObjectAttributes{}, r)
					if c.refused {
						Expect(err).To(MatchError(op.ErrAccessDenied))
					} else {
						Expect(err).NotTo(HaveOccurred())
					}
					Expect(actions()).To(Equal(c.asked))
				},
				Entry("both allowed", versionCase{asked: []policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes}}),
				Entry("s3:GetObjectVersion refused", versionCase{
					denied: []policy.Action{policy.S3GetObjectVersion}, refused: true, asked: []policy.Action{policy.S3GetObjectVersion},
				}),
				Entry("s3:GetObjectVersionAttributes refused", versionCase{
					denied: []policy.Action{policy.S3GetObjectVersionAttributes}, refused: true,
					asked: []policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes},
				}),
				Entry("the current object's actions, which a version does not ask for, refused", versionCase{
					denied: []policy.Action{policy.S3GetObject, policy.S3GetObjectAttributes},
					asked:  []policy.Action{policy.S3GetObjectVersion, policy.S3GetObjectVersionAttributes},
				}),
			)
			It("returns an error of the second action that is no access denial", func(ctx SpecContext) {
				authz.VerifyObjectReturnsOnCall(1, op.ErrUnknown)
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))).To(MatchError(op.ErrUnknown))
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectAttributes}))
			})
			It("lets an admin through a refusal of the second action, as Run lets one through any access denial", func(ctx SpecContext) {
				deny(policy.S3GetObjectAttributes)
				r := f.req("small")
				r.Identity.Admin = true
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, r)).To(Succeed())
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
			It("lets the read action alone decide, for the current object and a version, never asking for the actions Squid lacks", func(ctx SpecContext) {
				deny(policy.S3GetObject)
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, f.req("small"))).To(MatchError(op.ErrAccessDenied))
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject}))
				authz.VerifyObjectCalls(nil)
				authz.VerifyObjectReturns(nil)
				r := f.req("small")
				r.Object.Instance = "null"
				Expect(op.Run(ctx, &op.GetObjectAttributes{}, r)).To(Succeed())
				Expect(actions()).To(Equal([]policy.Action{policy.S3GetObject, policy.S3GetObjectVersion}))
			})
		})

		// These run the evaluator, so each action is decided as any other
		// object permission is: the identity's and the bucket's policies, then
		// the object's ACL, where op_to_perm maps both actions to READ
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L247-L265).
		Describe("by the policies and ACL grants that decide", func() {
			var bob op.Identity
			// objectKey is the object a spec reads: plain's small, or the
			// version v1 of v when versioned.
			objectKey := func(versioned bool) meta.ObjKey {
				if versioned {
					return meta.ObjKey{Name: "v", Instance: "v1"}
				}
				return meta.ObjKey{Name: "small"}
			}
			setUp := func(ctx context.Context, release denc.Release) {
				GinkgoHelper()
				f = newObjectReadFixture(ctx, release)
				f.env.Authz = authzpkg.New(authzpkg.DefaultConfig(release))
				b := f.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
				bob = op.Identity{User: &b.Info, Owner: meta.UserOwner(b.Info.UserID), OpMask: op.OpTypeAll}
				_, err := f.store.PutObject(ctx, f.rec, objectKey(true), bytes.NewReader(payload(16)), op.PutParams{
					Size: 16, Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(f.alice.Owner, "Alice"))},
				})
				Expect(err).NotTo(HaveOccurred())
			}
			// bobMay sets plain's bucket policy to statements naming bob, each
			// an effect and an action on every object of plain.
			bobMay := func(ctx context.Context, stmts ...[2]string) {
				GinkgoHelper()
				parts := make([]string, 0, len(stmts))
				for _, st := range stmts {
					parts = append(parts, `{"Effect":"`+st[0]+`","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":"`+st[1]+`","Resource":"arn:aws:s3:::plain/*"}`)
				}
				doc := `{"Version":"2012-10-17","Statement":[` + strings.Join(parts, ",") + `]}`
				Expect(f.store.PutBucketAttrs(ctx, f.rec, map[string][]byte{op.AttrIAMPolicy: []byte(doc)}, nil)).To(Succeed())
			}
			// grantBobRead gives bob READ in key's ACL.
			grantBobRead := func(ctx context.Context, key meta.ObjKey) {
				GinkgoHelper()
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermRead})
				st, err := f.store.StatObject(ctx, f.rec, key)
				Expect(err).NotTo(HaveOccurred())
				Expect(f.store.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: encodedPolicy(p)}, nil)).To(Succeed())
			}
			bobReads := func(ctx context.Context, key meta.ObjKey) error {
				r := f.req(key.Name)
				r.Object, r.Identity = key, bob
				return op.Run(ctx, &op.GetObjectAttributes{Requested: op.AttrETag | op.AttrObjectSize}, r)
			}
			// access is one spec's case: what bob holds on the object it reads,
			// and whether GetObjectAttributes serves him.
			type access struct {
				// versioned reads the version v1 of v, not plain's small.
				versioned bool
				// aclRead grants bob READ in the object's ACL.
				aclRead bool
				// policy is plain's bucket policy for bob; none when empty.
				policy [][2]string
				served bool
			}
			decide := func(ctx context.Context, c access) {
				GinkgoHelper()
				key := objectKey(c.versioned)
				if len(c.policy) > 0 {
					bobMay(ctx, c.policy...)
				}
				if c.aclRead {
					grantBobRead(ctx, key)
				}
				err := bobReads(ctx, key)
				if c.served {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.IsBeforeVerify(err)).To(BeFalse())
			}
			allows := func(a string) [2]string { return [2]string{"Allow", a} }
			denies := func(a string) [2]string { return [2]string{"Deny", a} }
			// actionPair is the two actions a request needs, and the pair the
			// other kind of request needs.
			type actionPair struct {
				name                  string
				versioned             bool
				read, attrs           string
				otherRead, otherAttrs string
			}

			// v20.2.4 serves every request here that either action allows.
			Context("on Tentacle, needing both actions as Amazon S3 does", func() {
				BeforeEach(func(ctx SpecContext) { setUp(ctx, denc.Tentacle) })
				for _, p := range []actionPair{
					{"for the current object", false, "s3:GetObject", "s3:GetObjectAttributes", "s3:GetObjectVersion", "s3:GetObjectVersionAttributes"},
					{"for a named version", true, "s3:GetObjectVersion", "s3:GetObjectVersionAttributes", "s3:GetObject", "s3:GetObjectAttributes"},
				} {
					DescribeTable(p.name+", "+p.read+" and "+p.attrs,
						func(ctx SpecContext, c access) {
							c.versioned = p.versioned
							decide(ctx, c)
						},
						Entry("a policy allowing "+p.read+" alone is refused", access{policy: [][2]string{allows(p.read)}}),
						Entry("a policy allowing "+p.attrs+" alone is refused", access{policy: [][2]string{allows(p.attrs)}}),
						Entry("a policy allowing both is served", access{policy: [][2]string{allows(p.read), allows(p.attrs)}, served: true}),
						Entry("a policy allowing "+p.otherRead+" and "+p.otherAttrs+" is refused",
							access{policy: [][2]string{allows(p.otherRead), allows(p.otherAttrs)}}),
						Entry("a Deny of "+p.read+" refuses what both are allowed",
							access{policy: [][2]string{allows(p.read), allows(p.attrs), denies(p.read)}}),
						Entry("a Deny of "+p.attrs+" refuses what both are allowed",
							access{policy: [][2]string{allows(p.read), allows(p.attrs), denies(p.attrs)}}),
						Entry("an ACL READ grant with no policy satisfies both", access{aclRead: true, served: true}),
						Entry("a Deny of "+p.read+" refuses an ACL READ grantee", access{aclRead: true, policy: [][2]string{denies(p.read)}}),
						Entry("a Deny of "+p.attrs+" refuses an ACL READ grantee", access{aclRead: true, policy: [][2]string{denies(p.attrs)}}),
						Entry("neither a grant nor a policy is refused", access{}),
					)
				}
			})
			Context("on Squid, needing the read action alone, as it has no s3:GetObjectAttributes", func() {
				BeforeEach(func(ctx SpecContext) { setUp(ctx, denc.Squid) })
				DescribeTable("for the current object and for a named version",
					func(ctx SpecContext, c access) { decide(ctx, c) },
					Entry("a policy allowing s3:GetObject alone is served", access{policy: [][2]string{allows("s3:GetObject")}, served: true}),
					Entry("a Deny of s3:GetObject refuses an ACL READ grantee", access{aclRead: true, policy: [][2]string{denies("s3:GetObject")}}),
					Entry("an ACL READ grant with no policy is served", access{aclRead: true, served: true}),
					Entry("a policy allowing s3:GetObjectVersion alone is served for a version",
						access{versioned: true, policy: [][2]string{allows("s3:GetObjectVersion")}, served: true}),
					Entry("a Deny of s3:GetObjectVersion refuses an ACL READ grantee of a version",
						access{versioned: true, aclRead: true, policy: [][2]string{denies("s3:GetObjectVersion")}}),
					Entry("an ACL READ grant with no policy is served for a version", access{versioned: true, aclRead: true, served: true}),
				)
			})
		})
	})
})
