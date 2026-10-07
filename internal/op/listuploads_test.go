package op_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("ListMultipartUploads", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newMPFixture(ctx, denc.Squid) })

	It("is radosgw's list_bucket_multiparts, a read of s3:ListBucketMultipartUploads", func() {
		o := &op.ListMultipartUploads{}
		Expect(o.Name()).To(Equal("list_bucket_multiparts"))
		Expect(o.Action()).To(Equal(policy.S3ListBucketMultipartUploads))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
	})
	It("lists the bucket's uploads", func(ctx SpecContext) {
		a, b := f.initUpload(ctx, "a"), f.initUpload(ctx, "b")
		o := &op.ListMultipartUploads{MaxUploads: 1}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
		Expect(o.Result.Uploads).To(HaveLen(1))
		Expect(o.Result.Truncated).To(BeTrue())
		o = &op.ListMultipartUploads{MaxUploads: 10, KeyMarker: o.Result.NextKeyMarker, UploadIDMarker: o.Result.NextUploadIDMarker}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
		Expect(o.Result.Uploads).To(HaveLen(1))
		Expect([]string{a, b}).To(ContainElement(o.Result.Uploads[0].ID))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("hands the store the request's parameters", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.ListUploadsReturns(op.ListUploadsResult{NextKeyMarker: "n"}, nil)
		f.env.Multipart = stub
		o := &op.ListMultipartUploads{Prefix: "p", Delimiter: "/", KeyMarker: "k", UploadIDMarker: "2~u", MaxUploads: 7, EncodingURL: true}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
		_, rec, p := stub.ListUploadsArgsForCall(0)
		Expect(rec.Info.Bucket.Name).To(Equal("plain"))
		Expect(p).To(Equal(op.ListUploadsParams{Prefix: "p", Delimiter: "/", KeyMarker: "k", UploadIDMarker: "2~u", MaxUploads: 7}))
		Expect(o.Result.NextKeyMarker).To(Equal("n"))
	})
	It("authorizes s3:ListBucketMultipartUploads on the bucket with its ACL permission", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.ListMultipartUploads{MaxUploads: 10}, f.req(http.MethodGet, "plain", ""))).To(Succeed())
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3ListBucketMultipartUploads, acl.PermFor(policy.S3ListBucketMultipartUploads)}))
	})
	It("lists nothing for a requester refused permission", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		f.env.Multipart = stub
		r := f.req(http.MethodGet, "plain", "")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.ListMultipartUploads{MaxUploads: 10}, r)).To(MatchError(op.ErrAccessDenied))
		Expect(stub.Invocations()).To(BeEmpty())
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.ListMultipartUploads{}, f.req(http.MethodGet, "nope", ""))).To(MatchError(op.ErrNoSuchBucket))
		Expect(authz.Invocations()).To(BeEmpty())
	})
})
