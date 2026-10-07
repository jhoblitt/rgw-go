package op_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("AbortMultipart", func() {
	var (
		f  *writeFixture
		id string
	)
	BeforeEach(func(ctx SpecContext) {
		f = newMPFixture(ctx, denc.Squid)
		id = f.initUpload(ctx, "k")
	})

	It("is radosgw's abort_multipart, a delete of s3:AbortMultipartUpload", func() {
		o := &op.AbortMultipart{}
		Expect(o.Name()).To(Equal("abort_multipart"))
		Expect(o.Action()).To(Equal(policy.S3AbortMultipartUpload))
		Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
	})
	It("aborts the upload, and a second abort finds none", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: id}, f.req(http.MethodDelete, "plain", "k"))).To(Succeed())
		_, err := f.upload(ctx, "k", id)
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: id}, f.req(http.MethodDelete, "plain", "k"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("hands the store the upload of the request's key in its bucket", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		f.env.Multipart = stub
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: id}, f.req(http.MethodDelete, "plain", "k"))).To(Succeed())
		_, up := stub.AbortArgsForCall(0)
		Expect([]any{up.ID, up.Bucket.Info.Bucket.Name, up.Key}).To(Equal([]any{id, "plain", meta.ObjKey{Name: "k"}}))
	})
	DescribeTable("answers InvalidArgument once permitted, aborting nothing, for",
		func(ctx SpecContext, uploadID, key string) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			stub := &opfakes.FakeMultipartStore{}
			f.env.Multipart = stub
			Expect(op.Run(ctx, &op.AbortMultipart{UploadID: uploadID}, f.req(http.MethodDelete, "plain", key))).To(MatchError(op.ErrInvalidArgument))
			Expect(authz.VerifyBucketCallCount()).To(Equal(1))
			Expect(stub.Invocations()).To(BeEmpty())
		},
		Entry("an empty upload id", "", "k"),
		Entry("no key", "2~x", ""),
	)
	It("authorizes s3:AbortMultipartUpload on the bucket with its ACL permission", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: id}, f.req(http.MethodDelete, "plain", "k"))).To(Succeed())
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3AbortMultipartUpload, acl.PermFor(policy.S3AbortMultipartUpload)}))
	})
	It("aborts nothing for a requester refused permission", func(ctx SpecContext) {
		r := f.req(http.MethodDelete, "plain", "k")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: id}, r)).To(MatchError(op.ErrAccessDenied))
		_, err := f.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred())
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: id}, f.req(http.MethodDelete, "nope", "k"))).To(MatchError(op.ErrNoSuchBucket))
		Expect(authz.Invocations()).To(BeEmpty())
	})
})
