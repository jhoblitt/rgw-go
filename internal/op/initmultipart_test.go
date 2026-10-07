package op_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// newMPFixture is the write fixture with the memstore as the multipart
// store and radosgw's part limit configured.
func newMPFixture(ctx context.Context, release denc.Release) *writeFixture {
	GinkgoHelper()
	f := newWriteFixture(ctx, release)
	f.env.Multipart = f.store
	f.conf["rgw_multipart_part_upload_limit"] = "10000"
	return f
}

// initUpload starts alice's upload of key in plain and returns its id.
func (f *writeFixture) initUpload(ctx context.Context, key string) string {
	GinkgoHelper()
	o := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice")}
	Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", key))).To(Succeed())
	return o.UploadID
}

// upload is the store's view of key's upload id in plain.
func (f *writeFixture) upload(ctx context.Context, key, id string) (*op.Upload, error) {
	return f.store.GetUpload(ctx, f.rec, meta.ObjKey{Name: key}, id)
}

// bodyReq is alice's request for key in plain carrying body with its
// Content-Length.
func (f *writeFixture) bodyReq(method, key, body string) *op.Request {
	r := f.req(method, "plain", key)
	r.Body = strings.NewReader(body)
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return r
}

var _ = Describe("InitMultipart", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newMPFixture(ctx, denc.Squid) })

	It("is radosgw's init_multipart, a write of s3:PutObject", func() {
		o := &op.InitMultipart{}
		Expect(o.Name()).To(Equal("init_multipart"))
		Expect(o.Action()).To(Equal(policy.S3PutObject))
		Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
	})
	It("creates an upload holding the request's attrs, the ACL and the tags, owned by the requester", func(ctx SpecContext) {
		set := tags.Set{}
		Expect(set.Add("a", "b", tags.MaxObjectTags)).To(Succeed())
		attrs := map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")}
		o := &op.InitMultipart{Attrs: attrs, ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), Tags: &set}
		Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
		Expect(o.UploadID).To(HavePrefix(meta.MultipartUploadIDPrefix))
		Expect(o.UploadID).To(HaveLen(33), `"2~" and the 31 characters gen_rand_alphanumeric writes`)
		Expect(o.Upload).NotTo(BeNil())
		Expect(o.Upload.ID).To(Equal(o.UploadID))
		up, err := f.upload(ctx, "k", o.UploadID)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Attrs).To(Equal(map[string][]byte{
			meta.AttrContentType: []byte("text/plain\x00"), meta.AttrACL: f.aliceACL, tags.Attr: encodedTags(set),
		}))
		Expect(up.Owner).To(Equal(f.alice.Owner))
		Expect(up.OwnerName).To(Equal("Alice"))
		Expect(up.Placement).To(Equal(meta.PlacementRule{Name: "default-placement"}))
		Expect(attrs).To(HaveLen(1), "the request's attrs stay as the handler gave them")
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("stores no tag attr for an empty tag set, as encode_obj_tags_attr skips one", func(ctx SpecContext) {
		o := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), Tags: &tags.Set{}}
		Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
		up, err := f.upload(ctx, "k", o.UploadID)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Attrs).NotTo(HaveKey(tags.Attr))
	})
	It("takes the bucket's placement with the request's storage class as the upload's", func(ctx SpecContext) {
		o := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"), StorageClass: meta.StorageClassStandard}
		Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
		up, err := f.upload(ctx, "k", o.UploadID)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Placement).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: meta.StorageClassStandard}))
	})
	It("authorizes s3:PutObject on the bucket with its ACL permission", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.InitMultipart{}, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
		Expect(authz.VerifyBucketCallCount()).To(Equal(1))
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect(a).To(Equal(policy.S3PutObject))
		Expect(perm).To(Equal(acl.PermFor(policy.S3PutObject)))
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.InitMultipart{}, f.req(http.MethodPost, "nope", "k"))).To(MatchError(op.ErrNoSuchBucket))
		Expect(authz.Invocations()).To(BeEmpty())
	})
	It("refuses a storage class the bucket's placement lacks as InvalidArgument before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.InitMultipart{StorageClass: "GLACIAL"}, f.req(http.MethodPost, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
		Expect(authz.Invocations()).To(BeEmpty())
	})
	It("refuses a key-less request as InvalidArgument and creates nothing", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		f.env.Multipart = stub
		Expect(op.Run(ctx, &op.InitMultipart{}, f.req(http.MethodPost, "plain", ""))).To(MatchError(op.ErrInvalidArgument))
		Expect(stub.CreateUploadCallCount()).To(BeZero())
	})
	Describe("a block of public ACLs, which radosgw does not check here", func() {
		var stub *opfakes.FakeMultipartStore
		BeforeEach(func() {
			stub = &opfakes.FakeMultipartStore{}
			stub.CreateUploadReturns(&op.Upload{ID: "2~x"}, nil)
			f.env.Multipart = stub
		})

		It("refuses a public ACL before checking permissions and creates nothing", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := &op.InitMultipart{ACL: publicReadOf(f.alice.Owner)}
			Expect(op.Run(ctx, o, f.req(http.MethodPost, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			Expect(authz.Invocations()).To(BeEmpty())
			Expect(stub.CreateUploadCallCount()).To(BeZero())
		})
		It("refuses a public ACL under a block that does not decode, which fails closed", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: {0xff}})
			r := f.req(http.MethodPost, "plain", "k")
			r.Identity.Admin = true
			Expect(op.Run(ctx, &op.InitMultipart{ACL: publicReadOf(f.alice.Owner)}, r)).To(MatchError(op.ErrAccessDenied))
			Expect(stub.CreateUploadCallCount()).To(BeZero())
		})
		It("creates an upload with a private ACL under the block", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			Expect(op.Run(ctx, &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice")}, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
			Expect(stub.CreateUploadCallCount()).To(Equal(1))
		})
		It("creates an upload with a public ACL under a block that does not block public ACLs", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{IgnorePublicACLs: true})})
			Expect(op.Run(ctx, &op.InitMultipart{ACL: publicReadOf(f.alice.Owner)}, f.req(http.MethodPost, "plain", "k"))).To(Succeed())
			Expect(stub.CreateUploadCallCount()).To(Equal(1))
		})
	})
	It("creates nothing for a requester refused write permission", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		f.env.Multipart = stub
		r := f.req(http.MethodPost, "plain", "k")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.InitMultipart{ACL: acl.DefaultPolicy(f.bob.Owner, "Bob")}, r)).To(MatchError(op.ErrAccessDenied))
		Expect(stub.CreateUploadCallCount()).To(BeZero())
	})
})
