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

var _ = Describe("the permissions a write's ACL and tag headers need", func() {
	var (
		f      *writeFixture
		authz  *opfakes.FakeAuthorizer
		denied map[policy.Action]bool
		// writes run each write that takes the headers, in plain for r.
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
	// headerReq is alice's request for k carrying hdr, a name given twice
	// sent twice.
	headerReq := func(hdr ...string) *op.Request {
		r := f.req(http.MethodPut, "plain", "k")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Add(hdr[i], hdr[i+1])
		}
		return r
	}
	// checks are the actions beside s3:PutObject that each VerifyBucket call
	// since the call numbered from evaluated, on the key each named, with no
	// ACL permission asked.
	checks := func(from int) []string {
		var got []string
		for i := from; i < authz.VerifyBucketCallCount(); i++ {
			_, r, a, perm := authz.VerifyBucketArgsForCall(i)
			if a == policy.S3PutObject {
				continue
			}
			Expect(perm).To(Equal(acl.PermNone), "no ACL permission for %s", a)
			got = append(got, a.String()+" "+r.Object.Name)
		}
		return got
	}

	DescribeTable("evaluate, once s3:PutObject has passed, the action each header names on the object's key, and none without them",
		func(ctx SpecContext, hdr, want []string) {
			for name, write := range writes {
				from := authz.VerifyBucketCallCount()
				Expect(write(ctx, headerReq(hdr...))).To(Succeed(), name)
				Expect(checks(from)).To(Equal(want), name)
			}
		},
		Entry("none", nil, nil),
		Entry("a public canned ACL", []string{"X-Amz-Acl", "public-read"}, []string{"s3:PutObjectAcl k"}),
		Entry("authenticated-read", []string{"X-Amz-Acl", "authenticated-read"}, []string{"s3:PutObjectAcl k"}),
		Entry("bucket-owner-read, which grants the bucket's owner access to the object",
			[]string{"X-Amz-Acl", "bucket-owner-read"}, []string{"s3:PutObjectAcl k"}),
		Entry("bucket-owner-full-control, which Amazon's cross-account upload policies allow s3:PutObject alone to name",
			[]string{"X-Amz-Acl", "bucket-owner-full-control"}, nil),
		Entry("bucket-owner-full-control in another case, which is no canned ACL and is refused once authorized",
			[]string{"X-Amz-Acl", "Bucket-Owner-Full-Control"}, []string{"s3:PutObjectAcl k"}),
		Entry("bucket-owner-full-control beside a grant header, the grant counted",
			[]string{"X-Amz-Acl", "bucket-owner-full-control", "X-Amz-Grant-Full-Control", `id="bob"`}, []string{"s3:PutObjectAcl k"}),
		Entry("a canned ACL radosgw does not know, which the ACL's parse refuses once authorized",
			[]string{"X-Amz-Acl", "Private"}, []string{"s3:PutObjectAcl k"}),
		Entry("a repeated x-amz-acl read as its last value, which is bucket-owner-full-control",
			[]string{"X-Amz-Acl", "public-read", "X-Amz-Acl", "bucket-owner-full-control"}, nil),
		Entry("a repeated x-amz-acl read as its last value, which is public-read",
			[]string{"X-Amz-Acl", "bucket-owner-full-control", "X-Amz-Acl", "public-read"}, []string{"s3:PutObjectAcl k"}),
		Entry("private, the ACL a write without one gets", []string{"X-Amz-Acl", "private"}, nil),
		Entry("an empty canned ACL, which create_s3_policy reads as none", []string{"X-Amz-Acl", ""}, nil),
		Entry("a grant header", []string{"X-Amz-Grant-Read", `id="bob"`}, []string{"s3:PutObjectAcl k"}),
		Entry("a grant header sent empty, which has_acl_header counts", []string{"X-Amz-Grant-Full-Control", ""}, []string{"s3:PutObjectAcl k"}),
		Entry("a grant header and private", []string{"X-Amz-Acl", "private", "X-Amz-Grant-Write-Acp", `id="bob"`},
			[]string{"s3:PutObjectAcl k"}),
		Entry("tags", []string{"X-Amz-Tagging", "a=b"}, []string{"s3:PutObjectTagging k"}),
		Entry("tags sent empty, which a copy that replaces its metadata stores as the tag attr", []string{"X-Amz-Tagging", ""},
			[]string{"s3:PutObjectTagging k"}),
		Entry("an ACL and tags, the ACL first", []string{"X-Amz-Tagging", "a=b", "X-Amz-Acl", "public-read"},
			[]string{"s3:PutObjectAcl k", "s3:PutObjectTagging k"}),
		Entry("an ACL, tags and the object-lock headers, whose actions the lock headers' check evaluates", []string{
			"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Legal-Hold", "ON",
			"X-Amz-Acl", "public-read", "X-Amz-Tagging", "a=b",
		}, []string{"s3:PutObjectAcl k", "s3:PutObjectTagging k"}),
	)
	DescribeTable("refuse a header whose action is refused with 403 AccessDenied, writing nothing",
		func(ctx SpecContext, refused policy.Action, hdr ...string) {
			denied[refused] = true
			for name, write := range writes {
				err := write(ctx, headerReq(hdr...))
				Expect(err).To(MatchError(op.ErrAccessDenied), name)
				Expect(err.Error()).To(ContainSubstring(refused.String()), name+" names the action it lacked")
			}
			Expect(f.stat(ctx, "k").Exists).To(BeFalse())
			uploads, err := f.store.ListUploads(ctx, f.rec, op.ListUploadsParams{MaxUploads: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(uploads.Uploads).To(BeEmpty())
		},
		Entry("an ACL without s3:PutObjectAcl", policy.S3PutObjectAcl, "X-Amz-Acl", "public-read"),
		Entry("a grant without s3:PutObjectAcl", policy.S3PutObjectAcl, "X-Amz-Grant-Read", `id="bob"`),
		Entry("tags without s3:PutObjectTagging", policy.S3PutObjectTagging, "X-Amz-Tagging", "a=b"),
		Entry("tags beside a permitted ACL", policy.S3PutObjectTagging, "X-Amz-Acl", "public-read", "X-Amz-Tagging", "a=b"),
		Entry("an ACL beside permitted tags", policy.S3PutObjectAcl, "X-Amz-Acl", "public-read", "X-Amz-Tagging", "a=b"),
	)
	DescribeTable("serve an x-amz-acl that needs no action though both actions are refused",
		func(ctx SpecContext, canned string) {
			denied[policy.S3PutObjectAcl], denied[policy.S3PutObjectTagging] = true, true
			for name, write := range writes {
				Expect(write(ctx, headerReq("X-Amz-Acl", canned))).To(Succeed(), name)
			}
		},
		Entry("private, the ACL a write without one gets", "private"),
		Entry("bucket-owner-full-control, which Amazon allows s3:PutObject alone to name", "bucket-owner-full-control"),
	)
	It("evaluate nothing of the ACL or the tags for a requester s3:PutObject refuses", func(ctx SpecContext) {
		denied[policy.S3PutObject] = true
		for name, write := range writes {
			Expect(write(ctx, headerReq("X-Amz-Acl", "public-read", "X-Amz-Tagging", "a=b"))).To(MatchError(op.ErrAccessDenied), name)
		}
		Expect(checks(0)).To(BeEmpty())
		Expect(authz.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
	})
	It("let an admin through a refusal, as Run lets one through every permission check's", func(ctx SpecContext) {
		denied[policy.S3PutObjectAcl], denied[policy.S3PutObjectTagging] = true, true
		for name, write := range writes {
			r := headerReq("X-Amz-Acl", "public-read", "X-Amz-Tagging", "a=b")
			r.Identity.Admin = true
			Expect(write(ctx, r)).To(Succeed(), name)
		}
		Expect(f.stat(ctx, "k").Exists).To(BeTrue())
	})
	It("leave UploadPart's headers, which only its part's head stores, to s3:PutObject", func(ctx SpecContext) {
		id := f.initUpload(ctx, "k")
		denied[policy.S3PutObjectAcl], denied[policy.S3PutObjectTagging] = true, true
		r := f.bodyReq(http.MethodPut, "k", "x")
		r.Header.Set("X-Amz-Acl", "public-read")
		r.Header.Set("X-Amz-Tagging", "a=b")
		Expect(op.Run(ctx, &op.UploadPart{UploadID: id, PartNumber: 1, Body: r.Body, Size: 1}, r)).To(Succeed())
		Expect(checks(0)).To(BeEmpty())
		Expect(authz.VerifyBucketOwnerOrPolicyCallCount()).To(BeZero())
	})
})
