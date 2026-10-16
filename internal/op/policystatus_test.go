package op_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// listPlainPolicy is a policy of one Allow statement granting principal
// s3:ListBucket on plain and its objects, under condition unless it is "",
// the shape of the policy-status s3-tests' policies.
func listPlainPolicy(principal, condition string) string {
	stmt := `"Effect": "Allow", "Principal": ` + principal +
		`, "Action": "s3:ListBucket", "Resource": ["arn:aws:s3:::plain", "arn:aws:s3:::plain/*"]`
	if condition != "" {
		stmt += `, "Condition": ` + condition
	}
	return `{"Version": "2012-10-17", "Statement": [{` + stmt + `}]}`
}

// unparsablePolicy is a stored policy radosgw's parser refuses.
const unparsablePolicy = `{"Version": "2012-10-17", "Statement": [{"Effect": "Maybe"}]}`

// policyStatusReleases are the releases the policy-status specs run on.
var policyStatusReleases = []denc.Release{denc.Squid, denc.Tentacle}

// policyStatusBlock sets every flag of a public-access block.
var policyStatusBlock = acl.PublicAccessBlock{BlockPublicACLs: true, IgnorePublicACLs: true, BlockPublicPolicy: true, RestrictPublicBuckets: true}

var _ = Describe("GetBucketPolicyStatus", func() {
	// cannedACL is plain's ACL as alice's canned ACL names it.
	cannedACL := func(f *writeFixture, canned string) []byte {
		GinkgoHelper()
		alice := acl.Owner{ID: f.alice.Owner.String(), DisplayName: "Alice"}
		p, err := acl.Canned(alice, alice, canned)
		Expect(err).NotTo(HaveOccurred())
		return encodedPolicy(p)
	}
	// plainWith is a fixture on release whose bucket plain holds the canned
	// ACL and, unless it is "", the policy.
	plainWith := func(ctx context.Context, rel denc.Release, canned, bucketPolicy string) *writeFixture {
		GinkgoHelper()
		f := newWriteFixture(ctx, rel)
		set := map[string][]byte{meta.AttrACL: cannedACL(f, canned)}
		if bucketPolicy != "" {
			set[op.AttrIAMPolicy] = []byte(bucketPolicy)
		}
		f.setBucketAttrs(ctx, set)
		return f
	}
	// isPublic runs the op for r and returns its answer.
	isPublic := func(ctx context.Context, r *op.Request) bool {
		GinkgoHelper()
		o := &op.GetBucketPolicyStatus{}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		return o.IsPublic
	}

	It("is radosgw's op, authorized as its action under its op mask", func() {
		o := &op.GetBucketPolicyStatus{}
		Expect(o.Name()).To(Equal("get_bucket_policy_status"))
		Expect(o.Action()).To(Equal(policy.S3GetBucketPolicyStatus))
		Expect(o.OpMask()).To(Equal(op.OpTypeRead))
	})

	It("answers NoSuchBucket before any permission check", func(ctx SpecContext) {
		f := newWriteFixture(ctx, denc.Squid)
		r := f.req(http.MethodGet, "missing", "")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.GetBucketPolicyStatus{}, r)).To(MatchError(op.ErrNoSuchBucket))
	})

	DescribeTable("is public when the bucket's ACL or its policy is, judging the policy as Amazon S3 does on both releases",
		func(ctx SpecContext, canned, bucketPolicy string, want bool) {
			for _, rel := range policyStatusReleases {
				f := plainWith(ctx, rel, canned, bucketPolicy)
				Expect(isPublic(ctx, f.req(http.MethodGet, "plain", ""))).To(Equal(want), "%v: a %s ACL and the policy %q", rel, canned, bucketPolicy)
			}
		},
		Entry("a new bucket, test_get_bucket_policy_status", "private", "", false),
		Entry("a public-read ACL, test_get_public_acl_bucket_policy_status", "public-read", "", true),
		Entry("an authenticated-read ACL, test_get_authpublic_acl_bucket_policy_status", "authenticated-read", "", true),
		Entry("a grant to every principal, test_get_publicpolicy_acl_bucket_policy_status",
			"private", listPlainPolicy(`{"AWS": "*"}`, ""), true),
		Entry("a grant to every principal from 10.0.0.0/32, test_get_nonpublicpolicy_acl_bucket_policy_status",
			"private", listPlainPolicy(`{"AWS": "*"}`, `{"IpAddress": {"aws:SourceIp": "10.0.0.0/32"}}`), false),
		// Squid's radosgw calls it public (docs/ceph-upstream-bugs.md,
		// "Squid's is_public counts every Allow statement whose NotPrincipal
		// is not the wildcard").
		Entry("a grant to one tenant's account, test_get_nonpublicpolicy_principal_bucket_policy_status",
			"private", listPlainPolicy(`{"AWS": "arn:aws:iam::s3tenant1:root"}`, ""), false),
		// radosgw judges these two the other way on both releases
		// (docs/ceph-upstream-bugs.md, "radosgw's is_public judges a
		// wildcard-principal statement against a fixed three-key
		// environment").
		Entry("a grant to every principal from 1.1.1.1/32 alone, an address radosgw's fixed environment holds",
			"private", listPlainPolicy(`"*"`, `{"IpAddress": {"aws:SourceIp": "1.1.1.1/32"}}`), false),
		Entry("a grant to every principal over TLS, a key radosgw's fixed environment lacks",
			"private", listPlainPolicy(`"*"`, `{"Bool": {"aws:SecureTransport": "true"}}`), true),
		Entry("a public-read ACL beside a policy that is not public",
			"public-read", listPlainPolicy(`{"AWS": "arn:aws:iam::s3tenant1:root"}`, ""), true),
	)

	DescribeTable("ignores the bucket's public-access block",
		func(ctx SpecContext, canned, bucketPolicy string) {
			for _, rel := range policyStatusReleases {
				f := plainWith(ctx, rel, canned, bucketPolicy)
				f.env.Authz = authz.New(authz.DefaultConfig(rel))
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(policyStatusBlock)})
				Expect(isPublic(ctx, f.req(http.MethodGet, "plain", ""))).To(BeTrue(), "%v: a %s ACL and the policy %q under %+v", rel, canned, bucketPolicy, policyStatusBlock)
			}
		},
		Entry("for a public policy stored before the block", "private", listPlainPolicy(`"*"`, "")),
		Entry("for a public ACL stored before the block", "public-read", ""),
	)

	// radosgw's is_public calls each of these ACLs private on both releases
	// (docs/exclusions.md, "IgnorePublicAcls sets aside a Swift container's
	// grants to everyone, and an ACL holding them is public" and
	// "BlockPublicAcls counts grants to the user anonymous and its ownership,
	// but for an anonymous writer's own").
	DescribeTable("is public when the bucket's private ACL holds a grant IgnorePublicAcls sets aside, on both releases",
		func(ctx SpecContext, g acl.Grant) {
			for _, rel := range policyStatusReleases {
				f := newWriteFixture(ctx, rel)
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(g)
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(p)})
				Expect(isPublic(ctx, f.req(http.MethodGet, "plain", ""))).To(BeTrue(), "%v: a private ACL and %+v", rel, g)
			}
		},
		Entry("a referer grant", acl.Grant{Type: acl.GranteeReferer, URLSpec: ".example.com", Permission: acl.PermRead}),
		Entry("a grant to the user anonymous", acl.Grant{Type: acl.GranteeCanonUser, ID: "anonymous", Permission: acl.PermRead}),
	)

	It("judges the policy as the cluster's release parses it", func(ctx SpecContext) {
		// Squid's parse drops a Service principal, which leaves the statement
		// none; Tentacle's keeps the wildcard service.
		doc := listPlainPolicy(`{"Service": ["s3.amazonaws.com", "*"]}`, "")
		for rel, want := range map[denc.Release]bool{denc.Squid: false, denc.Tentacle: true} {
			f := plainWith(ctx, rel, "private", doc)
			Expect(isPublic(ctx, f.req(http.MethodGet, "plain", ""))).To(Equal(want), "%v: %s", rel, doc)
		}
	})

	// An admin is let through a policy that does not parse for the tenant.
	DescribeTable("parses the policy as one of the bucket's tenant",
		func(ctx SpecContext, resource string, want bool) {
			f := newWriteFixture(ctx, denc.Squid)
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			doc := `{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": "*", "Action": "s3:ListBucket", "Resource": "` + resource + `"}]}`
			_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
				Tenant: "t1", Name: "tb", Owner: f.alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, op.AttrIAMPolicy: []byte(doc)},
			})
			Expect(err).NotTo(HaveOccurred())
			r := f.req(http.MethodGet, "tb", "")
			r.Tenant = "t1"
			r.Identity.Admin = true
			Expect(isPublic(ctx, r)).To(Equal(want), "t1/tb: %s", doc)
		},
		Entry("public when it grants every principal the tenant's bucket", "arn:aws:s3::t1:tb", true),
		Entry("as none when it names another tenant's bucket", "arn:aws:s3::t2:tb", false),
	)

	Describe("authorization", func() {
		It("refuses a requester the authorizer refuses", func(ctx SpecContext) {
			f := newWriteFixture(ctx, denc.Squid)
			r := f.req(http.MethodGet, "plain", "")
			r.Identity = f.bob
			Expect(op.Run(ctx, &op.GetBucketPolicyStatus{}, r)).To(MatchError(op.ErrAccessDenied))
		})

		DescribeTable("needs READ_ACP from the bucket's ACL",
			func(ctx SpecContext, perm acl.Permission, want error) {
				f := newWriteFixture(ctx, denc.Squid)
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: perm})
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(p)})
				r := f.req(http.MethodGet, "plain", "")
				r.Identity = f.bob
				err := op.Run(ctx, &op.GetBucketPolicyStatus{}, r)
				if want == nil {
					Expect(err).NotTo(HaveOccurred(), "bob granted %v", perm)
				} else {
					Expect(err).To(MatchError(want), "bob granted %v", perm)
				}
			},
			Entry("granted by READ_ACP", acl.PermReadACP, nil),
			Entry("refused with READ alone", acl.PermRead, op.ErrAccessDenied),
		)

		DescribeTable("adds the bucket's tags for a policy that names them",
			func(ctx SpecContext, value string, want error) {
				f := newWriteFixture(ctx, denc.Squid)
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				var set tags.Set
				Expect(set.Add("k", value, tags.MaxBucketTags)).To(Succeed())
				f.setBucketAttrs(ctx, map[string][]byte{
					tags.Attr: encodeTags(set),
					op.AttrIAMPolicy: []byte(`{"Version": "2012-10-17", "Statement": [{"Effect": "Allow",
						"Principal": {"AWS": "arn:aws:iam:::user/bob"}, "Action": "s3:GetBucketPolicyStatus", "Resource": "arn:aws:s3:::plain",
						"Condition": {"StringEquals": {"s3:ResourceTag/k": "v"}}}]}`),
				})
				r := f.req(http.MethodGet, "plain", "")
				r.Identity = f.bob
				err := op.Run(ctx, &op.GetBucketPolicyStatus{}, r)
				if want == nil {
					Expect(err).NotTo(HaveOccurred(), "bucket tag k=%s", value)
				} else {
					Expect(err).To(MatchError(want), "bucket tag k=%s", value)
				}
			},
			Entry("allowing bob when the tag matches", "v", nil),
			Entry("refusing bob when it does not", "w", op.ErrAccessDenied),
		)
	})

	Describe("a stored policy that does not parse", func() {
		It("refuses every requester but an admin before the op runs", func(ctx SpecContext) {
			for _, rel := range policyStatusReleases {
				f := plainWith(ctx, rel, "public-read", unparsablePolicy)
				f.env.Authz = authz.New(authz.DefaultConfig(rel))
				err := op.Run(ctx, &op.GetBucketPolicyStatus{}, f.req(http.MethodGet, "plain", ""))
				Expect(err).To(MatchError(op.ErrAccessDenied), "%v: the owner", rel)
				Expect(op.IsBeforeVerify(err)).To(BeTrue(), "%v: %v is refused before verify_permission", rel, err)
			}
		})

		// radosgw leaves an exempt requester's s->iam_policy unset.
		DescribeTable("counts as none for an admin",
			func(ctx SpecContext, canned string, want bool) {
				for _, rel := range policyStatusReleases {
					f := plainWith(ctx, rel, canned, unparsablePolicy)
					f.env.Authz = authz.New(authz.DefaultConfig(rel))
					r := f.req(http.MethodGet, "plain", "")
					r.Identity.Admin = true
					Expect(isPublic(ctx, r)).To(Equal(want), "%v: a %s ACL", rel, canned)
				}
			},
			Entry("beside a public-read ACL", "public-read", true),
			Entry("beside a private ACL", "private", false),
		)
	})
})
