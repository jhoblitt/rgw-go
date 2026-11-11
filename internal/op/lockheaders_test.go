package op_test

import (
	"context"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("the permissions a write's object-lock headers need", func() {
	var (
		f      *writeFixture
		authz  *opfakes.FakeAuthorizer
		denied map[policy.Action]bool
		// writes run each write of k in plain for r.
		writes map[string]func(ctx context.Context, r *op.Request) error
	)
	BeforeEach(func(ctx SpecContext) {
		f = newMPFixture(ctx, denc.Squid)
		denied = map[policy.Action]bool{}
		authz = &opfakes.FakeAuthorizer{}
		authz.VerifyBucketStub = func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
			if denied[a] {
				return op.ErrAccessDenied
			}
			return nil
		}
		authz.VerifyBucketOwnerOrPolicyStub = authz.VerifyBucketStub
		f.env.Authz = authz
		writes = map[string]func(ctx context.Context, r *op.Request) error{
			"PutObject": func(ctx context.Context, r *op.Request) error {
				return op.Run(ctx, &op.PutObject{Body: strings.NewReader("x"), Size: 1}, r)
			},
			"CopyObject": func(ctx context.Context, r *op.Request) error {
				return op.Run(ctx, f.copyOf("small"), r)
			},
			"CreateMultipartUpload": func(ctx context.Context, r *op.Request) error {
				return op.Run(ctx, &op.InitMultipart{}, r)
			},
		}
	})
	// lockReq is alice's request for k carrying hdr.
	lockReq := func(hdr ...string) *op.Request {
		r := f.req(http.MethodPut, "plain", "k")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		return r
	}
	// lockChecks are the actions each VerifyBucketOwnerOrPolicy call since the
	// call numbered from evaluated, on the key each named.
	lockChecks := func(from int) []string {
		var got []string
		for i := from; i < authz.VerifyBucketOwnerOrPolicyCallCount(); i++ {
			_, r, a, perm := authz.VerifyBucketOwnerOrPolicyArgsForCall(i)
			Expect(perm).To(Equal(acl.PermWrite), "op_to_perm's WRITE for %s", a)
			got = append(got, a.String()+" "+r.Object.Name)
		}
		return got
	}

	DescribeTable("evaluate, once s3:PutObject has passed, the action each header names on the object's key, and none without them",
		func(ctx SpecContext, hdr, want []string) {
			for name, write := range writes {
				from := authz.VerifyBucketOwnerOrPolicyCallCount()
				Expect(write(ctx, lockReq(hdr...))).To(Succeed(), name)
				Expect(lockChecks(from)).To(Equal(want), name)
			}
		},
		Entry("none", nil, nil),
		Entry("a mode", []string{"X-Amz-Object-Lock-Mode", "GOVERNANCE"}, []string{"s3:PutObjectRetention k"}),
		Entry("a retain-until date", []string{"X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"}, []string{"s3:PutObjectRetention k"}),
		Entry("a legal hold", []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}, []string{"s3:PutObjectLegalHold k"}),
		Entry("all three", []string{
			"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z",
			"X-Amz-Object-Lock-Legal-Hold", "OFF",
		}, []string{"s3:PutObjectRetention k", "s3:PutObjectLegalHold k"}),
	)
	DescribeTable("refuse a header whose action is refused with 403 AccessDenied, writing nothing",
		func(ctx SpecContext, refused policy.Action, hdr ...string) {
			denied[refused] = true
			for name, write := range writes {
				Expect(write(ctx, lockReq(hdr...))).To(MatchError(op.ErrAccessDenied), name)
			}
			Expect(f.stat(ctx, "k").Exists).To(BeFalse())
			uploads, err := f.store.ListUploads(ctx, f.rec, op.ListUploadsParams{MaxUploads: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(uploads.Uploads).To(BeEmpty())
		},
		Entry("a retention without s3:PutObjectRetention", policy.S3PutObjectRetention, "X-Amz-Object-Lock-Mode", "GOVERNANCE"),
		Entry("a legal hold without s3:PutObjectLegalHold", policy.S3PutObjectLegalHold, "X-Amz-Object-Lock-Legal-Hold", "ON"),
		Entry("a legal hold beside a permitted retention", policy.S3PutObjectLegalHold,
			"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Legal-Hold", "ON"),
	)
	It("serve a header whose own action passes while the other's is refused", func(ctx SpecContext) {
		denied[policy.S3PutObjectLegalHold] = true
		for name, write := range writes {
			Expect(write(ctx, lockReq("X-Amz-Object-Lock-Mode", "GOVERNANCE"))).To(Succeed(), name)
		}
	})
	It("evaluate nothing of the lock for a requester s3:PutObject refuses", func(ctx SpecContext) {
		denied[policy.S3PutObject] = true
		for name, write := range writes {
			Expect(write(ctx, lockReq("X-Amz-Object-Lock-Legal-Hold", "ON"))).To(MatchError(op.ErrAccessDenied), name)
		}
		Expect(authz.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
	})
	It("let an admin through a refusal, as Run lets one through every permission check's", func(ctx SpecContext) {
		denied[policy.S3PutObjectRetention] = true
		for name, write := range writes {
			r := lockReq("X-Amz-Object-Lock-Mode", "GOVERNANCE")
			r.Identity.Admin = true
			Expect(write(ctx, r)).To(Succeed(), name)
		}
		Expect(f.stat(ctx, "k").Exists).To(BeTrue())
	})
	It("leave UploadPart's headers, which only its part's head stores, to s3:PutObject", func(ctx SpecContext) {
		id := f.initUpload(ctx, "k")
		denied[policy.S3PutObjectRetention], denied[policy.S3PutObjectLegalHold] = true, true
		r := f.bodyReq(http.MethodPut, "k", "x")
		r.Header.Set("X-Amz-Object-Lock-Legal-Hold", "ON")
		Expect(op.Run(ctx, &op.UploadPart{UploadID: id, PartNumber: 1, Body: r.Body, Size: 1}, r)).To(Succeed())
		Expect(authz.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
	})
})
