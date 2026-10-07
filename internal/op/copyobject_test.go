package op_test

import (
	"context"
	"maps"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// copyMsg is check_storage_class's message for a copy onto itself.
const copyMsg = "This copy request is illegal because it is trying to copy an object to itself without " +
	"changing the object's metadata, storage class, website redirect location or encryption attributes."

// putAttrs stores key in plain with data and attrs beside alice's ACL.
func (f *writeFixture) putAttrs(ctx context.Context, key, data string, attrs map[string][]byte) {
	GinkgoHelper()
	all := map[string][]byte{meta.AttrACL: f.aliceACL}
	maps.Copy(all, attrs)
	_, err := f.store.PutObject(ctx, f.rec, meta.ObjKey{Name: key}, strings.NewReader(data),
		op.PutParams{Size: int64(len(data)), Attrs: all})
	Expect(err).NotTo(HaveOccurred())
}

// copyOf is a copy of plain's key under alice's default ACL.
func (f *writeFixture) copyOf(key string) *op.CopyObject {
	return &op.CopyObject{SrcBucket: "plain", SrcKey: meta.ObjKey{Name: key}, ACL: acl.DefaultPolicy(f.alice.Owner, "Alice")}
}

var _ = Describe("CopyObject", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		f.putAttrs(ctx, "src", "hello", map[string][]byte{meta.AttrMetaPrefix + "k": []byte("v\x00")})
	})

	It("is radosgw's copy_obj, a write of s3:PutObject", func() {
		o := &op.CopyObject{}
		Expect(o.Name()).To(Equal("copy_obj"))
		Expect(o.Action()).To(Equal(policy.S3PutObject))
		Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
	})
	It("copies the source's data and metadata with the request's ACL and reports the source's ETag", func(ctx SpecContext) {
		o := f.copyOf("src")
		r := f.req(http.MethodPut, "plain", "dst")
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.ETag).To(Equal(md5Hex([]byte("hello"))))
		Expect(o.Mtime).To(Equal(smallMtime()))
		st := f.stat(ctx, "dst")
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"k", []byte("v\x00")))
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrACL, f.aliceACL))
		Expect(st.WriteTag).To(Equal(writeReqID), "the request id is the copy's tag")
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("passes the request's attrs, the ACL, the tag and the directive to the store", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.PrefetchObjectReturns(&op.ObjectState{Exists: true, Size: 5, Attrs: map[string][]byte{meta.AttrETag: []byte("e")}}, nil)
		stub.CopyObjectReturns(&op.PutResult{ETag: "e", Mtime: smallMtime(), Version: "v1"}, nil)
		f.env.Objects = stub
		o := f.copyOf("src")
		o.Attrs = map[string][]byte{meta.AttrMetaPrefix + "n": []byte("w\x00")}
		o.Replace, o.StorageClass = true, meta.StorageClassStandard
		o.IfMatch, o.IfNoneMatch = new(`"e"`), new("f")
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(Succeed())
		_, src, dst, key, p := stub.CopyObjectArgsForCall(0)
		Expect(src.Size).To(Equal(uint64(5)))
		Expect(dst.Info.Bucket.Name).To(Equal("plain"))
		Expect(key).To(Equal(meta.ObjKey{Name: "dst"}))
		Expect(p.Attrs).To(Equal(map[string][]byte{meta.AttrMetaPrefix + "n": []byte("w\x00"), meta.AttrACL: f.aliceACL}))
		Expect(p.ReplaceAttrs).To(BeTrue())
		Expect(p.StorageClass).To(Equal(meta.StorageClassStandard))
		Expect(p.Tag).To(Equal(writeReqID))
		Expect(p.IfMatch).To(Equal(`"e"`))
		Expect(p.IfNoneMatch).To(Equal("f"))
		Expect(o.Attrs).To(HaveLen(1), "the request's attrs stay as the handler gave them")
		Expect(o.VersionID).To(Equal("v1"))
	})
	It("takes the request's metadata over the source's with REPLACE", func(ctx SpecContext) {
		o := f.copyOf("src")
		o.Replace = true
		o.Attrs = map[string][]byte{meta.AttrMetaPrefix + "n": []byte("w\x00")}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(Succeed())
		st := f.stat(ctx, "dst")
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"n", []byte("w\x00")))
		Expect(st.Attrs).NotTo(HaveKey(meta.AttrMetaPrefix + "k"))
	})
	It("refuses a source bucket that does not exist before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		o := f.copyOf("src")
		o.SrcBucket = "nope"
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrNoSuchBucket))
		Expect(authz.Invocations()).To(BeEmpty())
	})
	It("refuses a destination storage class the placement lacks as InvalidArgument before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		o := f.copyOf("src")
		o.StorageClass = "GLACIAL"
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrInvalidArgument))
		Expect(authz.Invocations()).To(BeEmpty())
	})
	Describe("Params", func() {
		It("runs between the destination's load and the source bucket's, before permissions", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := f.copyOf("src")
			o.SrcBucket = "nope"
			r := f.req(http.MethodPut, "plain", "dst")
			o.Params = func(context.Context, *op.CopyObject) error {
				Expect(r.BucketRec).NotTo(BeNil(), "init_permissions has loaded the destination")
				return op.ErrInvalidArgument.WithMessage("Unknown metadata directive.")
			}
			err := op.Run(ctx, o, r)
			Expect(err).To(MatchError(op.ErrInvalidArgument), "get_params refuses before the source bucket's NoSuchBucket")
			Expect(authz.Invocations()).To(BeEmpty())
		})
		It("is not reached for a missing destination or a placement the zone lacks", func(ctx SpecContext) {
			called := false
			params := func(context.Context, *op.CopyObject) error { called = true; return nil }
			o := f.copyOf("src")
			o.Params = params
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "missing", "dst"))).To(MatchError(op.ErrNoSuchBucket))
			o = f.copyOf("src")
			o.Params, o.StorageClass = params, "GLACIAL"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrInvalidArgument))
			Expect(called).To(BeFalse())
		})
		It("builds the ACL that the block of public ACLs then refuses", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			stub := &opfakes.FakeObjectStore{}
			f.env.Objects = stub
			o := &op.CopyObject{SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "src"}, Params: func(_ context.Context, o *op.CopyObject) error {
				o.ACL = publicReadOf(f.alice.Owner)
				return nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrAccessDenied))
			Expect(stub.PrefetchObjectCallCount()).To(BeZero())
			Expect(stub.CopyObjectCallCount()).To(BeZero())
		})
	})
	It("answers a missing source with NoSuchKey", func(ctx SpecContext) {
		Expect(op.Run(ctx, f.copyOf("missing"), f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrNoSuchKey))
		Expect(f.stat(ctx, "dst").Exists).To(BeFalse())
	})
	It("takes a missing source's answer from read_obj_policy's rule", func(ctx SpecContext) {
		fake := &opfakes.FakeAuthorizer{}
		fake.VerifyObjectInReturns(op.ErrAccessDenied)
		f.env.Authz = fake
		Expect(op.Run(ctx, f.copyOf("missing"), f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrAccessDenied))
		Expect(fake.VerifyObjectInCallCount()).To(Equal(1))
		_, _, a, _, bucket, obj := fake.VerifyObjectInArgsForCall(0)
		Expect(a).To(Equal(policy.S3GetObject))
		Expect(bucket.Info.Bucket.Name).To(Equal("plain"))
		Expect(obj.Exists).To(BeFalse())
		Expect(fake.VerifyBucketInCallCount()).To(BeZero())
		Expect(fake.VerifyBucketCallCount()).To(Equal(1), "the destination is checked first")
	})
	DescribeTable("authorizes the source in its bucket, by both ACLs, then s3:PutObject on the destination",
		func(ctx SpecContext, instance string, want policy.Action) {
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			o := f.copyOf("src")
			o.SrcKey.Instance = instance
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))
			if instance == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(op.ErrNotImplemented), "a version is not served yet")
			}
			Expect(fake.VerifyBucketInCallCount()).To(Equal(1))
			_, _, a, perm, bucket, key := fake.VerifyBucketInArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
			Expect(bucket.Info.Bucket.Name).To(Equal("plain"))
			Expect(key).To(Equal(o.SrcKey))
			Expect(fake.VerifyObjectInCallCount()).To(Equal(1))
			_, _, a, perm, bucket, obj := fake.VerifyObjectInArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
			Expect(bucket.Info.Bucket.Name).To(Equal("plain"))
			Expect(obj.Key.Name).To(Equal("src"))
			Expect(fake.VerifyBucketCallCount()).To(Equal(1))
			_, _, a, perm = fake.VerifyBucketArgsForCall(0)
			Expect(a).To(Equal(policy.S3PutObject))
			Expect(perm).To(Equal(acl.PermFor(policy.S3PutObject)))
		},
		Entry("no version", "", policy.S3GetObject),
		Entry("a version", "null", policy.S3GetObjectVersion),
	)
	Describe("paths that must not write", func() {
		It("does not copy a missing source for an admin let through a refusal", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyObjectInReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			stub := &opfakes.FakeObjectStore{}
			stub.PrefetchObjectReturns(&op.ObjectState{Key: meta.ObjKey{Name: "missing"}}, nil)
			stub.CopyObjectReturns(&op.PutResult{}, nil)
			f.env.Objects = stub
			r := f.req(http.MethodPut, "plain", "dst")
			r.Identity.Admin = true
			Expect(op.Run(ctx, f.copyOf("missing"), r)).To(MatchError(op.ErrNoSuchKey))
			Expect(stub.CopyObjectCallCount()).To(BeZero())
		})
		It("refuses a store that reports no source state rather than failing open", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			stub.PrefetchObjectReturns(nil, nil)
			f.env.Objects = stub
			Expect(op.Run(ctx, f.copyOf("src"), f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrNoSuchKey))
			Expect(stub.CopyObjectCallCount()).To(BeZero())
		})
		It("refuses a public destination policy under the destination's block of public ACLs", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			o := f.copyOf("src")
			o.ACL = publicReadOf(f.alice.Owner)
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrAccessDenied))
			Expect(f.stat(ctx, "dst").Exists).To(BeFalse())
		})
		It("copies a private destination policy under that block", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			Expect(op.Run(ctx, f.copyOf("src"), f.req(http.MethodPut, "plain", "dst"))).To(Succeed())
		})
	})
	It("refuses a source whose ACL does not decode before checking its permission", func(ctx SpecContext) {
		f.putAttrs(ctx, "bad", "x", map[string][]byte{meta.AttrACL: {0xff}})
		fake := &opfakes.FakeAuthorizer{}
		f.env.Authz = fake
		Expect(op.Run(ctx, f.copyOf("bad"), f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrUnknown))
		Expect(fake.VerifyBucketInCallCount()).To(BeZero())
	})
	Describe("a copy onto itself", func() {
		It("is refused with check_storage_class's message when the request asks for that check", func(ctx SpecContext) {
			o := f.copyOf("src")
			o.CheckStorageClass = true
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "src"))
			Expect(err).To(MatchError(op.ErrInvalidRequest))
			Expect(messageOf(err)).To(Equal(copyMsg))
		})
		It("is refused when it names the source's class, STANDARD, explicitly", func(ctx SpecContext) {
			o := f.copyOf("src")
			o.CheckStorageClass, o.StorageClass = true, meta.StorageClassStandard
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "src"))).To(MatchError(op.ErrInvalidRequest))
		})
		It("proceeds to the permission checks once the check passes", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			stub.PrefetchObjectReturns(&op.ObjectState{Exists: true, StorageClass: "COLD", Attrs: map[string][]byte{}}, nil)
			stub.CopyObjectReturns(&op.PutResult{}, nil)
			f.env.Objects = stub
			o := f.copyOf("src")
			o.CheckStorageClass = true
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "src"))).To(Succeed())
		})
	})
	DescribeTable("applies the x-amz-copy-source conditions to the source",
		func(ctx SpecContext, mutate func(*op.CopyObject), want error) {
			o := f.copyOf("src")
			mutate(o)
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(want))
			Expect(f.stat(ctx, "dst").Exists).To(BeFalse())
		},
		Entry("If-Match naming another ETag", func(o *op.CopyObject) { o.IfMatch = new(md5Hex([]byte("other"))) }, op.ErrPreconditionFailed),
		Entry("If-Match naming the ETag, quoted", func(o *op.CopyObject) { o.IfMatch = new(`"` + md5Hex([]byte("hello")) + `"`) }, nil),
		Entry("If-None-Match naming the ETag", func(o *op.CopyObject) { o.IfNoneMatch = new(md5Hex([]byte("hello"))) }, op.ErrNotModified),
		Entry("If-Modified-Since the mtime's second", func(o *op.CopyObject) { o.IfModifiedSince = new("Sun, 27 Sep 2026 01:02:03 GMT") }, op.ErrNotModified),
		Entry("If-Modified-Since the second before", func(o *op.CopyObject) { o.IfModifiedSince = new("Sun, 27 Sep 2026 01:02:02 GMT") }, nil),
		Entry("If-Modified-Since with If-None-Match, which turns it off", func(o *op.CopyObject) {
			o.IfModifiedSince, o.IfNoneMatch = new("Sun, 27 Sep 2026 01:02:03 GMT"), new("other")
		}, nil),
		Entry("If-Unmodified-Since the second before", func(o *op.CopyObject) { o.IfUnmodifiedSince = new("Sun, 27 Sep 2026 01:02:02 GMT") }, op.ErrPreconditionFailed),
		Entry("If-Unmodified-Since with If-Match, which turns it off", func(o *op.CopyObject) {
			o.IfUnmodifiedSince, o.IfMatch = new("Sun, 27 Sep 2026 01:02:02 GMT"), new(md5Hex([]byte("hello")))
		}, nil),
		Entry("an If-Modified-Since that is no date", func(o *op.CopyObject) { o.IfModifiedSince = new("yesterday") }, op.ErrInvalidArgument),
		Entry("an If-Unmodified-Since that is no date", func(o *op.CopyObject) { o.IfUnmodifiedSince = new("tomorrow") }, op.ErrInvalidArgument),
		Entry("an empty If-Match, which no ETag is a prefix of", func(o *op.CopyObject) { o.IfMatch = new("") }, op.ErrPreconditionFailed),
		Entry("an empty If-None-Match, which matches nothing", func(o *op.CopyObject) { o.IfNoneMatch = new("") }, nil),
		Entry("an empty If-None-Match, which still turns If-Modified-Since off", func(o *op.CopyObject) {
			o.IfModifiedSince, o.IfNoneMatch = new("Sun, 27 Sep 2026 01:02:03 GMT"), new("")
		}, nil),
		Entry("an empty If-Modified-Since, which parse_time refuses", func(o *op.CopyObject) { o.IfModifiedSince = new("") }, op.ErrInvalidArgument),
		Entry("an empty If-Unmodified-Since, which parse_time refuses", func(o *op.CopyObject) { o.IfUnmodifiedSince = new("") }, op.ErrInvalidArgument),
	)
	Describe("the size and quota checks", func() {
		It("refuses a source over rgw_max_put_size", func(ctx SpecContext) {
			f.conf["rgw_max_put_size"] = "4"
			Expect(op.Run(ctx, f.copyOf("src"), f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrEntityTooLarge))
		})
		It("refuses an unparsable date before the size, as init_common runs first", func(ctx SpecContext) {
			f.conf["rgw_max_put_size"] = "4"
			o := f.copyOf("src")
			o.IfModifiedSince = new("yesterday")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrInvalidArgument))
		})
		It("refuses the size before a failed condition, which copy_obj checks", func(ctx SpecContext) {
			f.conf["rgw_max_put_size"] = "4"
			o := f.copyOf("src")
			o.IfMatch = new("other")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrEntityTooLarge))
		})
		It("checks the source's accounted size against the destination bucket's quota", func(ctx SpecContext) {
			stats := &opfakes.FakeStatsStore{}
			stats.CheckQuotaReturns(op.ErrQuotaExceeded)
			f.env.Stats = stats
			stub := &opfakes.FakeObjectStore{}
			stub.PrefetchObjectReturns(&op.ObjectState{Exists: true, Size: 3, Compression: &meta.CompressionInfo{OrigSize: 9}}, nil)
			f.env.Objects = stub
			Expect(op.Run(ctx, f.copyOf("src"), f.req(http.MethodPut, "plain", "dst"))).To(MatchError(op.ErrQuotaExceeded))
			_, rec, owner, bytes, objs := stats.CheckQuotaArgsForCall(0)
			Expect(rec.Info.Bucket.Name).To(Equal("plain"))
			Expect(owner).To(Equal(f.alice.Owner))
			Expect(bytes).To(Equal(int64(9)))
			Expect(objs).To(Equal(int64(1)))
			Expect(stub.CopyObjectCallCount()).To(BeZero())
		})
		It("skips both for a system request", func(ctx SpecContext) {
			f.conf["rgw_max_put_size"] = "4"
			stats := &opfakes.FakeStatsStore{}
			stats.CheckQuotaReturns(op.ErrQuotaExceeded)
			f.env.Stats = stats
			r := f.req(http.MethodPut, "plain", "dst")
			r.Identity.System = true
			Expect(op.Run(ctx, f.copyOf("src"), r)).To(Succeed())
			Expect(stats.CheckQuotaCallCount()).To(BeZero())
		})
	})
	DescribeTable("refuses a source transitioned to a cloud tier",
		func(ctx SpecContext, release denc.Release, tier string, size uint64, refused bool) {
			f = newWriteFixture(ctx, release)
			stub := &opfakes.FakeObjectStore{}
			m := meta.NewManifest()
			m.TierType = tier
			stub.PrefetchObjectReturns(&op.ObjectState{Exists: true, Size: size, Manifest: &m, Attrs: map[string][]byte{}}, nil)
			stub.CopyObjectReturns(&op.PutResult{}, nil)
			f.env.Objects = stub
			err := op.Run(ctx, f.copyOf("src"), f.req(http.MethodPut, "plain", "dst"))
			if !refused {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(op.ErrInvalidObjectState))
			Expect(messageOf(err)).To(Equal("This object was transitioned to cloud-s3"))
			Expect(stub.CopyObjectCallCount()).To(BeZero())
		},
		Entry("cloud-s3 on Squid, whatever its size", denc.Squid, meta.TierTypeCloudS3, uint64(10), true),
		Entry("cloud-s3-glacier on Squid, which Squid does not know", denc.Squid, meta.TierTypeCloudS3Glacier, uint64(0), false),
		Entry("cloud-s3 on Tentacle, empty", denc.Tentacle, meta.TierTypeCloudS3, uint64(0), true),
		Entry("cloud-s3-glacier on Tentacle, empty", denc.Tentacle, meta.TierTypeCloudS3Glacier, uint64(0), true),
		Entry("cloud-s3 on Tentacle, restored", denc.Tentacle, meta.TierTypeCloudS3, uint64(10), false),
		Entry("no tier", denc.Squid, "", uint64(0), false),
	)
	Describe("the source's ACLs, when no policy decides", func() {
		var bob op.Identity
		BeforeEach(func(ctx SpecContext) {
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			u := meta.NewUserInfo()
			u.UserID, u.DisplayName, u.Type = meta.UserID{ID: "bob"}, "Bob", meta.IdentityRGW
			bob = op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, AccessKey: "bob"}
			_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
				Name: "bobs", Owner: bob.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(bob.Owner, "Bob"))},
			})
			Expect(err).NotTo(HaveOccurred())
		})
		readBy := func(id string) acl.Policy {
			p := acl.DefaultPolicy(f.alice.Owner, "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: id, Name: id, Permission: acl.PermRead})
			return p
		}
		bobCopies := func(ctx context.Context) error {
			r := f.req(http.MethodPut, "bobs", "dst")
			r.Identity = bob
			return op.Run(ctx, &op.CopyObject{SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "src"}, ACL: acl.DefaultPolicy(bob.Owner, "Bob")}, r)
		}
		DescribeTable("needs READ on both the bucket's and the object's",
			func(ctx SpecContext, bucketRead, objectRead, allowed bool) {
				if bucketRead {
					f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
				}
				objACL := f.aliceACL
				if objectRead {
					objACL = encodedPolicy(readBy("bob"))
				}
				f.putAttrs(ctx, "src", "hello", map[string][]byte{meta.AttrACL: objACL})
				err := bobCopies(ctx)
				if allowed {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(MatchError(op.ErrAccessDenied))
			},
			Entry("a grantee of the bucket alone is refused, where radosgw copies", true, false, false),
			Entry("a grantee of both copies", true, true, true),
			Entry("a grantee of the object alone is refused, as by radosgw", false, true, false),
			Entry("a grantee of neither is refused", false, false, false),
		)
		DescribeTable("lets a bucket policy decide first",
			func(ctx SpecContext, effect string, grants, allowed bool) {
				if grants {
					f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
					f.putAttrs(ctx, "src", "hello", map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
				}
				doc := `{"Version": "2012-10-17", "Statement": [{"Effect": "` + effect +
					`", "Principal": {"AWS": "arn:aws:iam:::user/bob"}, "Action": "s3:GetObject", "Resource": "arn:aws:s3:::plain/src"}]}`
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrPrefix + "iam-policy": []byte(doc)})
				err := bobCopies(ctx)
				if allowed {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(MatchError(op.ErrAccessDenied))
			},
			Entry("an Allow copies without either grant", "Allow", false, true),
			Entry("a Deny refuses despite both grants", "Deny", true, false),
		)
		It("needs WRITE on the destination too", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
			f.putAttrs(ctx, "src", "hello", map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
			r := f.req(http.MethodPut, "plain", "dst")
			r.Identity = bob
			err := op.Run(ctx, &op.CopyObject{SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "src"}, ACL: acl.DefaultPolicy(bob.Owner, "Bob")}, r)
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(f.stat(ctx, "dst").Exists).To(BeFalse())
		})
		It("refuses an admin a destination whose ACL does not decode before any source check", func(ctx SpecContext) {
			_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
				Name: "broken", Owner: bob.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{meta.AttrACL: {0xff}},
			})
			Expect(err).NotTo(HaveOccurred())
			r := f.req(http.MethodPut, "broken", "dst")
			r.Identity = bob
			r.Identity.Admin = true
			err = op.Run(ctx, &op.CopyObject{SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "src"}, ACL: acl.DefaultPolicy(bob.Owner, "Bob")}, r)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
			rec, err := f.store.GetBucket(ctx, "", "broken")
			Expect(err).NotTo(HaveOccurred())
			st, err := f.store.StatObject(ctx, rec, meta.ObjKey{Name: "dst"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeFalse())
		})
		It("refuses a suspended destination marked, ahead of the op mask and the source", func(ctx SpecContext) {
			_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
				Name: "frozen", Owner: bob.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(bob.Owner, "Bob"))},
			})
			Expect(err).NotTo(HaveOccurred())
			rec, err := f.store.GetBucket(ctx, "", "frozen")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.Flags |= meta.BucketSuspended
			Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
			r := f.req(http.MethodPut, "frozen", "dst")
			r.Identity = bob
			r.Identity.OpMask = op.OpTypeRead
			err = op.Run(ctx, &op.CopyObject{SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "missing"}, ACL: acl.DefaultPolicy(bob.Owner, "Bob")}, r)
			Expect(err).To(MatchError(op.ErrUserSuspended))
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
		})
		It("refuses a source whose ACL does not decode even when a policy allows the read", func(ctx SpecContext) {
			f.putAttrs(ctx, "src", "hello", map[string][]byte{meta.AttrACL: {0xff}})
			doc := `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/bob"}, ` +
				`"Action": "s3:GetObject", "Resource": "arn:aws:s3:::plain/src"}]}`
			f.setBucketAttrs(ctx, map[string][]byte{meta.AttrPrefix + "iam-policy": []byte(doc)})
			Expect(bobCopies(ctx)).To(MatchError(op.ErrUnknown))
		})
	})
})
