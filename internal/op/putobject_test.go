package op_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // a Content-MD5 is an MD5
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"

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

// writeReqID is the transaction id of the write specs' requests.
const writeReqID = "tx00000000000000000001-0066f5a0c3-1234-default"

// writeFixture is the memstore the object write specs run on: alice's
// bucket "plain", whose ACL names her "Alice", holding "small", 1024 bytes
// under the same ACL, and bob, who owns nothing.
type writeFixture struct {
	store    *memstore.Store
	env      *op.Env
	conf     cephconf.MapGetter
	alice    op.Identity
	bob      op.Identity
	rec      *op.BucketRecord
	aliceACL []byte
}

func newWriteFixture(ctx context.Context, release denc.Release) *writeFixture {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: release, Now: smallMtime})
	f := &writeFixture{store: store, conf: cephconf.MapGetter{
		"rgw_max_put_size": "5368709120", "rgw_max_put_param_size": "1048576",
		"rgw_delete_multi_obj_max_num": "1000", "rgw_multi_obj_del_max_aio": "16",
	}}
	f.env = &op.Env{
		Zone: store, Users: store, Buckets: store, Objects: store, Stats: store, Usage: store,
		Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}, Conf: cephconf.NewOptions(f.conf),
	}
	a := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	f.alice = op.Identity{User: &a.Info, Owner: meta.UserOwner(a.Info.UserID), OpMask: op.OpTypeAll}
	b := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
	f.bob = op.Identity{User: &b.Info, Owner: meta.UserOwner(b.Info.UserID), OpMask: op.OpTypeAll}
	f.aliceACL = encodedPolicy(acl.DefaultPolicy(f.alice.Owner, "Alice"))
	var err error
	f.rec, err = store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "plain", Owner: f.alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: f.aliceACL},
	})
	Expect(err).NotTo(HaveOccurred())
	f.put(ctx, "small", payload(1024))
	return f
}

// put stores key in plain with data and alice's ACL.
func (f *writeFixture) put(ctx context.Context, key string, data []byte) {
	GinkgoHelper()
	_, err := f.store.PutObject(ctx, f.rec, meta.ObjKey{Name: key}, bytes.NewReader(data),
		op.PutParams{Size: int64(len(data)), Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}})
	Expect(err).NotTo(HaveOccurred())
}

// req is alice's request for key in bucket.
func (f *writeFixture) req(method, bucket, key string) *op.Request {
	return &op.Request{
		ID: writeReqID, Method: method, Bucket: bucket, Object: meta.ObjKey{Name: key},
		Identity: f.alice, Env: f.env, Header: http.Header{}, Query: url.Values{}, ContentLength: -1,
	}
}

// stat is key's state in plain.
func (f *writeFixture) stat(ctx context.Context, key string) *op.ObjectState {
	GinkgoHelper()
	st, err := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred())
	return st
}

// setBucketAttrs sets attrs on plain.
func (f *writeFixture) setBucketAttrs(ctx context.Context, set map[string][]byte) {
	GinkgoHelper()
	rec, err := f.store.GetBucket(ctx, "", "plain")
	Expect(err).NotTo(HaveOccurred())
	Expect(f.store.PutBucketAttrs(ctx, rec, set, nil)).To(Succeed())
}

// setBucketFlags sets plain's flags.
func (f *writeFixture) setBucketFlags(ctx context.Context, flags uint32) {
	GinkgoHelper()
	rec, err := f.store.GetBucket(ctx, "", "plain")
	Expect(err).NotTo(HaveOccurred())
	rec.Info.Flags |= flags
	Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
}

func publicAccessAttr(b acl.PublicAccessBlock) []byte {
	e := denc.NewEncoder()
	b.Encode(e, denc.Squid)
	return e.Bytes()
}

// errReader fails its first Read with err.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// failingReader fails the spec when it is read.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	Fail("the body was read")
	return 0, io.EOF
}

var _ = Describe("PutObject", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	It("is radosgw's put_obj, a write of s3:PutObject", func() {
		o := &op.PutObject{}
		Expect(o.Name()).To(Equal("put_obj"))
		Expect(o.Action()).To(Equal(policy.S3PutObject))
		Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
	})
	It("stores the body with the ACL, tags and metadata attrs and reports the ETag", func(ctx SpecContext) {
		set := tags.Set{}
		Expect(set.Add("a", "b", tags.MaxObjectTags)).To(Succeed())
		o := &op.PutObject{
			Body: strings.NewReader("hello"), Size: 5,
			Attrs: map[string][]byte{meta.AttrContentType: []byte("text/plain\x00"), meta.AttrMetaPrefix + "k": []byte("v\x00")},
			ACL:   acl.DefaultPolicy(f.alice.Owner, "Alice"), Tags: &set,
		}
		r := f.req(http.MethodPut, "plain", "k")
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.ETag).To(Equal("5d41402abc4b2a76b9719d911017c592"))
		Expect(o.Mtime).To(Equal(smallMtime()))
		st := f.stat(ctx, "k")
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrACL, f.aliceACL))
		Expect(st.Attrs).To(HaveKeyWithValue(tags.Attr, encodedTags(set)))
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"k", []byte("v\x00")))
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
		Expect(st.WriteTag).To(Equal(writeReqID), "the request id is the write tag")
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("stores no tag attr for an empty tag set, as encode_obj_tags_attr skips one", func(ctx SpecContext) {
		o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), Tags: &tags.Set{}}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(f.stat(ctx, "k").Attrs).NotTo(HaveKey(tags.Attr))
	})
	It("authorizes s3:PutObject on the bucket with its ACL permission", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("x"), Size: 1}, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(authz.VerifyBucketCallCount()).To(Equal(1))
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect(a).To(Equal(policy.S3PutObject))
		Expect(perm).To(Equal(acl.PermFor(policy.S3PutObject)))
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 1}, f.req(http.MethodPut, "nope", "k"))).To(MatchError(op.ErrNoSuchBucket))
		Expect(authz.Invocations()).To(BeEmpty())
	})
	It("refuses a storage class the bucket's placement lacks as InvalidArgument once the requester is authorized", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		o := &op.PutObject{Body: failingReader{}, Size: 1, StorageClass: "GLACIAL"}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
		Expect(authz.VerifyBucketCallCount()).To(Equal(1), "the placement is checked once the requester is authorized")
		f.env.Authz = op.OwnerOnly{}
		r := f.req(http.MethodPut, "plain", "k")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 1, StorageClass: "GLACIAL"}, r)).To(MatchError(op.ErrAccessDenied))
	})
	It("refuses a storage class the bucket's placement lacks ahead of the bucket's default encryption, as init_permissions comes first", func(ctx SpecContext) {
		f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
		o := &op.PutObject{Body: failingReader{}, Size: 1, StorageClass: "GLACIAL"}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
	})
	It("takes the bucket's placement and STANDARD for a storage class it names", func(ctx SpecContext) {
		o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, StorageClass: meta.StorageClassStandard}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
	})
	Describe("a Content-Length over rgw_max_put_size", func() {
		BeforeEach(func() { f.conf["rgw_max_put_size"] = "10" })

		It("is EntityTooLarge once permitted, before the body is read", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 11}, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrEntityTooLarge))
			Expect(authz.VerifyBucketCallCount()).To(Equal(1), "verify_params runs after verify_permission")
			Expect(f.stat(ctx, "k").Exists).To(BeFalse())
		})
		It("comes after a refusal of permission", func(ctx SpecContext) {
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 11}, r)).To(MatchError(op.ErrAccessDenied))
		})
		It("passes a length at the limit", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("0123456789"), Size: 10}, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		})
	})
	Describe("the quota", func() {
		var stats *opfakes.FakeStatsStore
		BeforeEach(func() {
			stats = &opfakes.FakeStatsStore{}
			f.env.Stats = stats
		})

		It("is checked against the announced size, one object, for the bucket's owner", func(ctx SpecContext) {
			stats.CheckQuotaReturns(op.ErrQuotaExceeded)
			Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 5}, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrQuotaExceeded))
			Expect(stats.CheckQuotaCallCount()).To(Equal(1))
			_, rec, owner, bytes, objs := stats.CheckQuotaArgsForCall(0)
			Expect(rec.Info.Bucket.Name).To(Equal("plain"))
			Expect(owner).To(Equal(f.alice.Owner))
			Expect(bytes).To(Equal(int64(5)))
			Expect(objs).To(Equal(int64(1)))
			Expect(f.stat(ctx, "k").Exists).To(BeFalse())
		})
		It("is not checked for a body of unknown length", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("x"), Size: -1}, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(stats.CheckQuotaCallCount()).To(BeZero())
		})
		It("is not checked for a system request", func(ctx SpecContext) {
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity.System = true
			Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("x"), Size: 1}, r)).To(Succeed())
			Expect(stats.CheckQuotaCallCount()).To(BeZero())
		})
	})
	It("passes a Content-MD5 to the store and reports BadDigest", func(ctx SpecContext) {
		sum := md5.Sum([]byte("other")) //nolint:gosec // a Content-MD5 is an MD5
		o := &op.PutObject{Body: strings.NewReader("hello"), Size: 5, ContentMD5: sum[:]}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrBadDigest))
		Expect(f.stat(ctx, "k").Exists).To(BeFalse())
	})
	Describe("a public canned ACL", func() {
		DescribeTable("under a block of public ACLs is refused once the requester is authorized",
			func(ctx SpecContext, canned string) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
				authz := &opfakes.FakeAuthorizer{}
				f.env.Authz = authz
				calls := 0
				o := &op.PutObject{Body: failingReader{}, Size: 1, CannedACL: canned, Authorized: func(context.Context, *op.PutObject) error {
					calls++
					return nil
				}}
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
				Expect(authz.VerifyBucketCallCount()).To(Equal(1), "init_processing refuses it before verify_permission; rgw-go authorizes first")
				Expect(calls).To(BeZero(), "init_processing refuses it before get_params")
			},
			Entry("public-read", "public-read"),
			Entry("public-read-write", "public-read-write"),
			Entry("authenticated-read", "authenticated-read"),
		)
		It("is refused when grant headers make the policy public, which radosgw lets through", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			stub := &opfakes.FakeObjectStore{}
			f.env.Objects = stub
			o := &op.PutObject{Body: failingReader{}, Size: 1, ACL: publicReadOf(f.alice.Owner)}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			Expect(stub.PutObjectCallCount()).To(BeZero())
		})
		It("is refused under a block that does not decode, which fails closed", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: {0xff}})
			stub := &opfakes.FakeObjectStore{}
			f.env.Objects = stub
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity.Admin = true
			o := &op.PutObject{Body: failingReader{}, Size: 1, CannedACL: "public-read", ACL: publicReadOf(f.alice.Owner)}
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
			Expect(stub.PutObjectCallCount()).To(BeZero())
		})
		It("is stored under a block that does not block public ACLs", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{IgnorePublicACLs: true})})
			o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, CannedACL: "public-read"}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		})
		It("is stored when the bucket has no block", func(ctx SpecContext) {
			o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, CannedACL: "public-read-write"}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		})
		It("does not cover private or bucket-owner-full-control", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			for _, canned := range []string{"private", "bucket-owner-full-control"} {
				o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, CannedACL: canned}
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed(), canned)
			}
		})
	})
	Describe("Params", func() {
		It("runs once the bucket is loaded, before permissions and ahead of every check of what the bucket holds", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{
				op.AttrPublicAccess:     publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true}),
				op.AttrBucketEncryption: {1},
			})
			f.setBucketFlags(ctx, meta.BucketObjLockEnabled|meta.BucketVersioned)
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			r := f.req(http.MethodPut, "plain", "k")
			o := &op.PutObject{Body: failingReader{}, Size: 1, StorageClass: "NOPE", CannedACL: "public-read", Params: func(context.Context, *op.PutObject) error {
				Expect(r.BucketRec).NotTo(BeNil(), "init_permissions has loaded the bucket")
				return op.ErrInvalidDigest
			}}
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrInvalidDigest))
			Expect(authz.Invocations()).To(BeEmpty(), "a refusal of the request alone comes before verify_permission")
		})
		It("is not reached for a missing bucket", func(ctx SpecContext) {
			called := false
			params := func(context.Context, *op.PutObject) error { called = true; return nil }
			Expect(op.Run(ctx, &op.PutObject{Size: 1, Params: params}, f.req(http.MethodPut, "missing", "k"))).To(MatchError(op.ErrNoSuchBucket))
			Expect(called).To(BeFalse())
		})
	})
	Describe("Authorized", func() {
		It("runs only for an authorized requester, before the body is read", func(ctx SpecContext) {
			calls := 0
			authorized := func(context.Context, *op.PutObject) error { calls++; return op.ErrInvalidArgument }
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 1, Authorized: authorized}, r)).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero())
			Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 1, Authorized: authorized}, f.req(http.MethodPut, "plain", "k"))).
				To(MatchError(op.ErrInvalidArgument))
			Expect(calls).To(Equal(1))
			Expect(f.stat(ctx, "k").Exists).To(BeFalse())
		})
		It("runs after the placement check, as get_params follows init_permissions", func(ctx SpecContext) {
			calls := 0
			o := &op.PutObject{Body: failingReader{}, Size: 1, StorageClass: "NOPE", Authorized: func(context.Context, *op.PutObject) error {
				calls++
				return op.ErrInvalidDigest
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
			Expect(calls).To(BeZero())
		})
		It("runs ahead of the refusals of a versioned bucket and a default encryption, which execute makes", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			f.setBucketFlags(ctx, meta.BucketObjLockEnabled|meta.BucketVersioned)
			o := &op.PutObject{Body: failingReader{}, Size: 1, Authorized: func(context.Context, *op.PutObject) error { return op.ErrInvalidRequest }}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidRequest))
		})
		It("builds the ACL that the block of public ACLs then refuses, writing nothing", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			stub := &opfakes.FakeObjectStore{}
			f.env.Objects = stub
			o := &op.PutObject{Body: failingReader{}, Size: 1, Authorized: func(_ context.Context, o *op.PutObject) error {
				o.ACL = publicReadOf(f.alice.Owner)
				return nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			Expect(authz.VerifyBucketCallCount()).To(Equal(1))
			Expect(stub.PutObjectCallCount()).To(BeZero())
		})
	})
	DescribeTable("passes If-Match and If-None-Match to the store, which applies v20.2.4's conditions on both releases",
		func(ctx SpecContext, release denc.Release, key string, ifMatch, ifNoneMatch *string, want error) {
			f = newWriteFixture(ctx, release)
			before := f.stat(ctx, "small").ETag
			o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, IfMatch: ifMatch, IfNoneMatch: ifNoneMatch}
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", key))
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(want))
			Expect(f.stat(ctx, "small").ETag).To(Equal(before), "the object stays")
		},
		Entry("If-None-Match * on an existing key fails, Squid", denc.Squid, "small", nil, new("*"), op.ErrPreconditionFailed),
		Entry("If-None-Match * on an existing key fails, Tentacle", denc.Tentacle, "small", nil, new("*"), op.ErrPreconditionFailed),
		Entry("If-None-Match * on a missing key writes", denc.Tentacle, "k", nil, new("*"), nil),
		Entry("If-Match * on a missing key is NoSuchKey, Squid", denc.Squid, "k", new("*"), nil, op.ErrNoSuchKey),
		Entry("If-Match * on a missing key is NoSuchKey, Tentacle", denc.Tentacle, "k", new("*"), nil, op.ErrNoSuchKey),
		Entry("If-Match naming the ETag, quoted, writes, Squid", denc.Squid, "small", new(`"`+md5Hex(payload(1024))+`"`), nil, nil),
		Entry("If-Match naming another ETag fails, Tentacle", denc.Tentacle, "small", new(md5Hex([]byte("other"))), nil, op.ErrPreconditionFailed),
		Entry("If-None-Match naming another ETag on a missing key writes, Squid", denc.Squid, "k", nil, new(md5Hex([]byte("other"))), nil),
		Entry("If-None-Match naming the ETag fails, Tentacle", denc.Tentacle, "small", nil, new(md5Hex(payload(1024))), op.ErrPreconditionFailed),
		Entry("an empty If-Match on an object fails, as no ETag is the empty string's prefix", denc.Squid, "small", new(""), nil, op.ErrPreconditionFailed),
		Entry("an empty If-Match on an object fails, Tentacle", denc.Tentacle, "small", new(""), nil, op.ErrPreconditionFailed),
		Entry("an empty If-Match on a missing key is NoSuchKey", denc.Squid, "k", new(""), nil, op.ErrNoSuchKey),
		Entry("an empty If-None-Match on an object writes, as radosgw's compare finds no match", denc.Tentacle, "small", nil, new(""), nil),
	)
	DescribeTable("writes nothing for a requester refused write permission, whatever the request carries",
		func(ctx SpecContext, o *op.PutObject) {
			stub := &opfakes.FakeObjectStore{}
			f.env.Objects = stub
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			o.Body, o.Size = failingReader{}, 1
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
			Expect(stub.PutObjectCallCount()).To(BeZero())
		},
		Entry("If-None-Match *", &op.PutObject{IfNoneMatch: new("*")}),
		Entry("If-Match *", &op.PutObject{IfMatch: new("*")}),
		Entry("a canned ACL", &op.PutObject{CannedACL: "bucket-owner-full-control"}),
		Entry("a policy granting the bucket owner, as grant headers build it", &op.PutObject{ACL: acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "alice"}), "Alice")}),
		Entry("tags and a storage class", &op.PutObject{Tags: &tags.Set{Tags: []tags.Tag{{Key: "a", Value: "b"}}}, StorageClass: meta.StorageClassStandard}),
	)
	It("surfaces the body's verification verdict and stores nothing", func(ctx SpecContext) {
		body := io.MultiReader(strings.NewReader("hel"), errReader{op.ErrContentSHA256Mismatch})
		Expect(op.Run(ctx, &op.PutObject{Body: body, Size: 3}, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrContentSHA256Mismatch))
		Expect(f.stat(ctx, "k").Exists).To(BeFalse())
	})
	It("leaves the request's attrs as the handler gave them", func(ctx SpecContext) {
		attrs := map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")}
		o := &op.PutObject{Body: strings.NewReader("x"), Size: 1, Attrs: attrs, ACL: acl.DefaultPolicy(f.alice.Owner, "Alice")}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(attrs).To(HaveLen(1))
	})
})

var _ = Describe("ErrMetadataNameTooLong", func() {
	It("renders as radosgw's S3 answer to ENAMETOOLONG, which only its Swift table names", func() {
		Expect(op.ErrMetadataNameTooLong.Code).To(Equal("UnknownError"))
		Expect(op.ErrMetadataNameTooLong.Status).To(Equal(http.StatusInternalServerError))
		Expect(op.ErrMetadataNameTooLong.Errno).To(Equal(int(syscall.ENAMETOOLONG)))
		Expect(errors.Is(op.ErrMetadataNameTooLong, op.ErrUnknown)).To(BeTrue())
		Expect(op.Errors()).NotTo(ContainElement(op.ErrMetadataNameTooLong), "it is no row of the S3 table")
	})
})

// opCase builds a write op and its request.
type opCase func(f *writeFixture, instance string) (op.Op, *op.Request)

func versionedPut(f *writeFixture, instance string) (op.Op, *op.Request) {
	r := f.req(http.MethodPut, "plain", "small")
	r.Object.Instance = instance
	return &op.PutObject{Body: failingReader{}, Size: 1}, r
}

func versionedCopy(f *writeFixture, instance string) (op.Op, *op.Request) {
	o := f.copyOf("small")
	o.SrcKey.Instance = instance
	return o, f.req(http.MethodPut, "plain", "dst")
}

func versionedDelete(f *writeFixture, instance string) (op.Op, *op.Request) {
	r := f.req(http.MethodDelete, "plain", "small")
	r.Object.Instance = instance
	return &op.DeleteObject{}, r
}

func versionedMulti(f *writeFixture, instance string) (op.Op, *op.Request) {
	m := &multiDelete{}
	return m.op([]op.DeleteObjectsEntry{{Key: meta.ObjKey{Name: "small", Instance: instance}}}, nil), f.multiReq("x")
}

func versionedPutACL(f *writeFixture, instance string) (op.Op, *op.Request) {
	r := f.req(http.MethodPut, "plain", "small")
	r.Object.Instance = instance
	return &op.PutObjectACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }}, r
}

func versionedPutTags(f *writeFixture, instance string) (op.Op, *op.Request) {
	r := f.req(http.MethodPut, "plain", "small")
	r.Object.Instance = instance
	return &op.PutObjectTagging{}, r
}

func versionedDelTags(f *writeFixture, instance string) (op.Op, *op.Request) {
	r := f.req(http.MethodDelete, "plain", "small")
	r.Object.Instance = instance
	return &op.DeleteObjectTagging{}, r
}

// versionedOps are the write ops, each building its request for key small
// in plain, naming instance when it is not empty.
var versionedOps = map[string]opCase{
	"PutObject": versionedPut, "CopyObject": versionedCopy, "DeleteObject": versionedDelete,
	"DeleteObjects": versionedMulti, "PutObjectACL": versionedPutACL, "PutObjectTagging": versionedPutTags,
	"DeleteObjectTagging": versionedDelTags,
}

var _ = Describe("the write ops on a bucket whose versioning is not served", func() {
	var (
		f    *writeFixture
		stub *opfakes.FakeObjectStore
	)
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		stub = &opfakes.FakeObjectStore{}
		st := &op.ObjectState{Exists: true, Key: meta.ObjKey{Name: "small"}, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrETag: []byte("e")}}
		stub.StatObjectReturns(st, nil)
		stub.PrefetchObjectReturns(st, nil)
		f.env.Objects = stub
	})
	DescribeTable("answer 501 NotImplemented and write nothing",
		func(ctx SpecContext, flags uint32, instance string) {
			if flags != 0 {
				f.setBucketFlags(ctx, flags)
			}
			for name, c := range versionedOps {
				o, r := c(f, instance)
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrNotImplemented), name)
			}
			Expect(stub.PutObjectCallCount()).To(BeZero())
			Expect(stub.CopyObjectCallCount()).To(BeZero())
			Expect(stub.DeleteObjectCallCount()).To(BeZero())
			Expect(stub.SetObjectAttrsCallCount()).To(BeZero())
		},
		Entry("a versioned bucket", uint32(meta.BucketVersioned), ""),
		Entry("a bucket whose versioning is suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), ""),
		Entry("the suspended flag alone", uint32(meta.BucketVersionsSuspended), ""),
		Entry("a bucket with object lock", uint32(meta.BucketObjLockEnabled), ""),
		Entry("a request naming a version", uint32(0), "v1"),
		Entry("a version that differs from the null one in case alone", uint32(0), "NULL"),
		Entry("the null version on a versioned bucket", uint32(meta.BucketVersioned), "null"),
		Entry("the null version on a bucket whose versioning is suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "null"),
		Entry("the null version under the suspended flag alone", uint32(meta.BucketVersionsSuspended), "null"),
		Entry("the null version on a bucket with object lock", uint32(meta.BucketObjLockEnabled), "null"),
	)
	DescribeTable("answer 501 NotImplemented to a write creating a head that names the null version, even on a bucket whose versioning was never enabled",
		func(ctx SpecContext, c opCase) {
			o, r := c(f, "null")
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrNotImplemented))
			Expect(stub.PutObjectCallCount()).To(BeZero())
			Expect(stub.CopyObjectCallCount()).To(BeZero())
		},
		Entry("PutObject", versionedPut),
		Entry("CopyObject from the null version", versionedCopy),
		Entry("CopyObject onto the null version", func(f *writeFixture, instance string) (op.Op, *op.Request) {
			r := f.req(http.MethodPut, "plain", "dst")
			r.Object.Instance = instance
			return f.copyOf("small"), r
		}),
	)
	It("never hands a versioned delete on an object-lock bucket to the store", func(ctx SpecContext) {
		f.setBucketFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
		o, r := versionedDelete(f, "v1")
		Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrNotImplemented))
		Expect(stub.DeleteObjectCallCount()).To(BeZero())
	})
	It("refuses a DeleteObjects on such a bucket through Status, before Begin", func(ctx SpecContext) {
		f.setBucketFlags(ctx, meta.BucketVersioned)
		m := &multiDelete{}
		Expect(op.Run(ctx, m.op(entriesOf("small"), nil), f.multiReq("x"))).To(MatchError(op.ErrNotImplemented))
		Expect(m.statuses).To(ConsistOf(MatchError(op.ErrNotImplemented)))
		Expect(m.events).NotTo(ContainElement("begin"))
	})
})
