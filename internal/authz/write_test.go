package authz_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// acmeAcct is the account the resolver specs look up.
const acmeAcct = "RGW00000000000000009"

// aclDoc is an AccessControlPolicy owned by owner holding grants.
func aclDoc(owner string, grants ...string) []byte {
	return []byte(`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>` + owner +
		`</ID></Owner><AccessControlList>` + strings.Join(grants, "") + `</AccessControlList></AccessControlPolicy>`)
}

// userGrantXML grants perm to the canonical user id.
func userGrantXML(id, perm string) string {
	return `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` +
		id + `</ID></Grantee><Permission>` + perm + `</Permission></Grant>`
}

func canned(owner, bucketOwner acl.Owner, name string) acl.Policy {
	GinkgoHelper()
	p, err := acl.Canned(owner, bucketOwner, name)
	Expect(err).NotTo(HaveOccurred())
	return p
}

var _ = Describe("the write-side helpers", func() {
	var (
		store *memstore.Store
		res   authz.UserResolver
		eval  *authz.Evaluator
		alice acl.Owner
		bob   acl.Owner
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{})
		store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", Email: "alice@example.com"})
		store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob"})
		store.AddAccount(meta.AccountInfo{ID: acmeAcct, Name: "acme", Email: "acme@example.com"})
		res = authz.UserResolver{Users: store, Accounts: store}
		eval = authz.New(evalConfig(denc.Squid))
		alice = acl.Owner{ID: "alice", DisplayName: "Alice"}
		bob = acl.Owner{ID: "bob", DisplayName: "Bob"}
	})

	// bucketReq is a request on bucket b, owned by alice, whose stored ACL is
	// existing and whose public-access block attr is block when not nil; hdr
	// is header name, value pairs.
	bucketReq := func(ctx context.Context, existing acl.Policy, block []byte, hdr ...string) *op.Request {
		GinkgoHelper()
		attrs := map[string][]byte{meta.AttrACL: aclAttr(existing)}
		if block != nil {
			attrs[attrPublicAccess] = block
		}
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: userOwner("", "alice"), Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		h := http.Header{}
		for i := 0; i+1 < len(hdr); i += 2 {
			h.Add(hdr[i], hdr[i+1])
		}
		return &op.Request{Header: h, Bucket: "b", BucketRec: rec, Identity: userIdentity("", "alice")}
	}
	private := func(o acl.Owner) acl.Policy { return canned(o, o, "private") }

	Describe("BuildACL", func() {
		It("builds a canned ACL with an empty body for the existing owner", func(ctx SpecContext) {
			existing := private(alice)
			p, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil, "X-Amz-Acl", "public-read"), res, existing, nil, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(canned(alice, alice, "public-read")))
		})
		DescribeTable("ignores a canned name holding \"bucket\" on a bucket, as get_policy_from_state clears it",
			func(ctx SpecContext, name string) {
				existing := private(alice)
				p, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil, "X-Amz-Acl", name), res, existing, nil, false)
				Expect(err).NotTo(HaveOccurred(), "rgw_rest_s3.cc:3640-3644 at v19.2.6")
				Expect(p).To(Equal(private(alice)))
			},
			Entry("bucket-owner-read", "bucket-owner-read"),
			Entry("a name radosgw does not know, which it would refuse on an object", "nobucketsuch"),
		)
		It("honors bucket-owner-read on an object, granting the bucket's owner", func(ctx SpecContext) {
			existing := private(bob)
			r := bucketReq(ctx, private(alice), nil, "X-Amz-Acl", "bucket-owner-read")
			p, err := eval.BuildACL(ctx, r, res, existing, nil, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(canned(bob, alice, "bucket-owner-read")))
			Expect(p.ACL.Grants).To(HaveLen(2), "bob's FULL_CONTROL and alice's READ")
		})
		It("refuses on an object a canned name that a bucket would ignore", func(ctx SpecContext) {
			existing := private(bob)
			_, err := eval.BuildACL(ctx, bucketReq(ctx, private(alice), nil, "X-Amz-Acl", "nobucketsuch"), res, existing, nil, true)
			Expect(err).To(MatchError(acl.ErrInvalid))
		})
		It("refuses a canned ACL with a body as InvalidArgument", func(ctx SpecContext) {
			existing := private(alice)
			_, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil, "X-Amz-Acl", "private"), res, existing, aclDoc("alice"), false)
			Expect(err).To(MatchError(acl.ErrInvalid))
			Expect(op.AsError(authz.ErrorFor(err)).Code).To(Equal("InvalidArgument"), "rgw_op.cc:5844-5847 at v19.2.6")
		})
		It("parses a body that names no canned ACL or grant header", func(ctx SpecContext) {
			existing := private(alice)
			p, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil), res, existing,
				aclDoc("alice", userGrantXML("alice", "FULL_CONTROL"), userGrantXML("bob", "READ")), false)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Owner).To(Equal(alice))
			Expect(p.ACL.Grants).To(HaveLen(2))
		})
		It("refuses a body whose owner is not the existing owner", func(ctx SpecContext) {
			existing := private(alice)
			_, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil), res, existing, aclDoc("bob", userGrantXML("bob", "FULL_CONTROL")), false)
			Expect(err).To(MatchError(acl.ErrOwnerMismatch))
			e := op.AsError(authz.ErrorFor(err))
			Expect(e.Code).To(Equal("AccessDenied"))
			Expect(e.Message).To(Equal("Cannot modify ACL Owner"), "rgw_op.cc:5859-5864 at v19.2.6")
		})
		It("lets any owner through when the existing policy names none", func(ctx SpecContext) {
			_, err := eval.BuildACL(ctx, bucketReq(ctx, private(alice), nil), res, acl.Policy{}, aclDoc("bob", userGrantXML("bob", "FULL_CONTROL")), false)
			Expect(err).NotTo(HaveOccurred(), "ACLOwner::empty, rgw_acl.cc:249-255")
		})
		Describe("the grant limit", func() {
			grants := func(n int) []byte {
				gs := make([]string, n)
				for i := range gs {
					gs[i] = userGrantXML("bob", "READ")
				}
				return aclDoc("alice", gs...)
			}
			It("accepts rgw_acl_grants_max_num grants", func(ctx SpecContext) {
				existing := private(alice)
				p, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil), res, existing, grants(100), false)
				Expect(err).NotTo(HaveOccurred())
				Expect(p.ACL.Grants).To(HaveLen(100))
			})
			It("refuses one more as LimitExceeded with radosgw's message", func(ctx SpecContext) {
				existing := private(alice)
				_, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil), res, existing, grants(101), false)
				Expect(err).To(MatchError(acl.ErrTooManyGrants))
				e := op.AsError(authz.ErrorFor(err))
				Expect(e.Code).To(Equal("LimitExceeded"))
				Expect(e.Message).To(Equal("The request is rejected, because the acl grants number you requested is larger than the maximum 100 grants allowed in an acl."),
					"rgw_op.cc:5875-5882 at v19.2.6")
			})
			It("takes a negative limit for 100", func(ctx SpecContext) {
				cfg := evalConfig(denc.Squid)
				cfg.ACLGrantsMaxNum = -1
				existing := private(alice)
				r := bucketReq(ctx, existing, nil)
				_, err := authz.New(cfg).BuildACL(ctx, r, res, existing, grants(100), false)
				Expect(err).NotTo(HaveOccurred(), "rgw_op.cc:5869-5872 at v19.2.6")
				_, err = authz.New(cfg).BuildACL(ctx, r, res, existing, grants(101), false)
				Expect(err).To(MatchError(acl.ErrTooManyGrants))
			})
		})
		Describe("a block of public ACLs on the request's bucket", func() {
			It("refuses a public ACL as AccessDenied", func(ctx SpecContext) {
				existing := private(alice)
				r := bucketReq(ctx, existing, pabAttr(acl.PublicAccessBlock{BlockPublicACLs: true}), "X-Amz-Acl", "public-read")
				_, err := eval.BuildACL(ctx, r, res, existing, nil, false)
				Expect(err).To(MatchError(op.ErrAccessDenied), "rgw_op.cc:5905-5910 at v19.2.6")
			})
			It("lets a private ACL through", func(ctx SpecContext) {
				existing := private(alice)
				r := bucketReq(ctx, existing, pabAttr(acl.PublicAccessBlock{BlockPublicACLs: true}), "X-Amz-Acl", "private")
				_, err := eval.BuildACL(ctx, r, res, existing, nil, false)
				Expect(err).NotTo(HaveOccurred())
			})
			It("takes a block that does not decode for one that blocks", func(ctx SpecContext) {
				existing := private(alice)
				r := bucketReq(ctx, existing, []byte{1}, "X-Amz-Acl", "public-read")
				_, err := eval.BuildACL(ctx, r, res, existing, nil, false)
				Expect(err).To(MatchError(op.ErrAccessDenied))
			})
		})
		It("builds the ACL from grant headers alone for the existing owner", func(ctx SpecContext) {
			existing := private(alice)
			p, err := eval.BuildACL(ctx, bucketReq(ctx, existing, nil, "X-Amz-Grant-Read", `id="bob"`), res, existing, nil, false)
			Expect(err).NotTo(HaveOccurred())
			want, err := acl.FromHeaders(ctx, res, alice, acl.GrantHeaders{Read: `id="bob"`})
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(want))
			Expect(p.ACL.Grants).To(HaveLen(1))
		})
		It("refuses a grant header beside a canned ACL on an object as InvalidRequest", func(ctx SpecContext) {
			existing := private(bob)
			r := bucketReq(ctx, private(alice), nil, "X-Amz-Acl", "private", "X-Amz-Grant-Read", `id="bob"`)
			_, err := eval.BuildACL(ctx, r, res, existing, nil, true)
			Expect(err).To(MatchError(op.ErrInvalidRequest), "rgw_rest_s3.cc:2412-2414 at v19.2.6")
		})
		It("builds from grant headers on a bucket whose canned name it ignores", func(ctx SpecContext) {
			existing := private(alice)
			r := bucketReq(ctx, existing, nil, "X-Amz-Acl", "bucket-owner-read", "X-Amz-Grant-Read", `id="bob"`)
			p, err := eval.BuildACL(ctx, r, res, existing, nil, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.ACL.Grants).To(HaveLen(1))
		})
	})

	Describe("BuildDefaultACL", func() {
		req := func(hdr ...string) *op.Request {
			h := http.Header{}
			for i := 0; i+1 < len(hdr); i += 2 {
				h.Add(hdr[i], hdr[i+1])
			}
			return &op.Request{Header: h}
		}
		It("is private for the owner without a header", func(ctx SpecContext) {
			p, err := eval.BuildDefaultACL(ctx, req(), res, bob, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(private(bob)))
		})
		It("refuses a grant header beside a canned ACL as InvalidRequest", func(ctx SpecContext) {
			_, err := eval.BuildDefaultACL(ctx, req("X-Amz-Grant-Read", `id="bob"`, "X-Amz-Acl", "private"), res, bob, alice)
			Expect(err).To(MatchError(op.ErrInvalidRequest))
		})
		It("gives an anonymous owner's object to the bucket owner", func(ctx SpecContext) {
			anon := acl.Owner{ID: "anonymous"}
			p, err := eval.BuildDefaultACL(ctx, req(), res, anon, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Owner).To(Equal(alice), "create_canned, rgw_acl_s3.cc:407-461")
			Expect(p.ACL.Grants).To(HaveLen(1))
			Expect(p.ACL.Grants[0].Grant.ID).To(Equal("anonymous"))
		})
		It("builds from grant headers alone", func(ctx SpecContext) {
			p, err := eval.BuildDefaultACL(ctx, req("X-Amz-Grant-Full-Control", `emailAddress="acme@example.com"`), res, bob, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Owner).To(Equal(bob))
			Expect(p.ACL.Grants).To(HaveLen(1))
			Expect(p.ACL.Grants[0].Grant.ID).To(Equal(acmeAcct))
			Expect(p.ACL.Grants[0].Grant.Name).To(Equal("acme"))
		})
		It("reads the last of a repeated x-amz-acl, as RGWEnv keeps it", func(ctx SpecContext) {
			p, err := eval.BuildDefaultACL(ctx, req("X-Amz-Acl", "public-read", "X-Amz-Acl", "private"), res, bob, alice)
			Expect(err).NotTo(HaveOccurred())
			Expect(p).To(Equal(private(bob)))
		})
		It("answers a grant to an unknown user as NoSuchKey through ErrorFor", func(ctx SpecContext) {
			_, err := eval.BuildDefaultACL(ctx, req("X-Amz-Grant-Read", `id="nobody"`), res, bob, alice)
			Expect(err).To(MatchError(acl.ErrGranteeNotFound))
			Expect(op.AsError(authz.ErrorFor(err)).Status).To(Equal(404))
		})
	})

	Describe("ParseBucketPolicy", func() {
		policyReq := func(ctx context.Context, tenant string, block []byte) *op.Request {
			GinkgoHelper()
			attrs := map[string][]byte{}
			if block != nil {
				attrs[attrPublicAccess] = block
			}
			rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Tenant: tenant, Name: "b", Owner: userOwner(tenant, "alice"), Attrs: attrs})
			Expect(err).NotTo(HaveOccurred())
			return &op.Request{Header: http.Header{}, Tenant: tenant, Bucket: "b", BucketRec: rec}
		}
		It("returns the document as its text", func(ctx SpecContext) {
			body := doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObject", "arn:aws:s3:::b/*", ""))
			p, err := eval.ParseBucketPolicy(policyReq(ctx, "", nil), []byte(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Text).To(Equal(body))
		})
		DescribeTable("parses the actions of the cluster's release",
			func(ctx SpecContext, r denc.Release, ok bool) {
				body := doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:GetObjectAttributes", "arn:aws:s3:::b/*", ""))
				_, err := authz.New(evalConfig(r)).ParseBucketPolicy(policyReq(ctx, "", nil), []byte(body))
				if ok {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				var pe *policy.ParseError
				Expect(errors.As(err, &pe)).To(BeTrue(), "got %v", err)
				Expect(op.AsError(authz.ErrorFor(err)).Message).To(Equal(pe.Error()))
			},
			Entry("Squid refuses s3:GetObjectAttributes", denc.Squid, false),
			Entry("Tentacle knows it", denc.Tentacle, true),
		)
		It("refuses a resource in another tenant", func(ctx SpecContext) {
			body := doc(stmt("Allow", awsPrincipal("arn:aws:iam::t1:user/bob"), "s3:GetObject", "arn:aws:s3::t2:b/*", ""))
			_, err := eval.ParseBucketPolicy(policyReq(ctx, "t1", nil), []byte(body))
			var pe *policy.ParseError
			Expect(errors.As(err, &pe)).To(BeTrue(), "got %v", err)
			Expect(pe.Annotation).To(ContainSubstring("tenant `t1`"))
		})
		DescribeTable("refuses a public policy as AccessDenied under a block of public policies",
			func(ctx SpecContext, r denc.Release, block []byte) {
				body := doc(stmt("Allow", `"*"`, "s3:GetObject", "arn:aws:s3:::b/*", ""))
				_, err := authz.New(evalConfig(r)).ParseBucketPolicy(policyReq(ctx, "", block), []byte(body))
				Expect(err).To(MatchError(op.ErrAccessDenied), "rgw_op.cc:8103-8108 at v19.2.6")
			},
			Entry("on Squid", denc.Squid, pabAttr(acl.PublicAccessBlock{BlockPublicPolicy: true})),
			Entry("on Tentacle", denc.Tentacle, pabAttr(acl.PublicAccessBlock{BlockPublicPolicy: true})),
			Entry("with a block that does not decode", denc.Tentacle, []byte{1}),
		)
		It("lets a public policy through a block of public ACLs alone", func(ctx SpecContext) {
			body := doc(stmt("Allow", `"*"`, "s3:GetObject", "arn:aws:s3:::b/*", ""))
			_, err := eval.ParseBucketPolicy(policyReq(ctx, "", pabAttr(acl.PublicAccessBlock{BlockPublicACLs: true})), []byte(body))
			Expect(err).NotTo(HaveOccurred())
		})
		It("refuses a principal radosgw does not support, rgw_policy_reject_invalid_principals being true", func(ctx SpecContext) {
			body := doc(stmt("Allow", `{"CanonicalUser": "x"}`, "s3:GetObject", "arn:aws:s3:::b/*", ""))
			_, err := eval.ParseBucketPolicy(policyReq(ctx, "", nil), []byte(body))
			var pe *policy.ParseError
			Expect(errors.As(err, &pe)).To(BeTrue(), "got %v", err)
		})
		It("drops that principal when rgw_policy_reject_invalid_principals is false", func(ctx SpecContext) {
			cfg := evalConfig(denc.Squid)
			cfg.RejectInvalidPrincipals = false
			body := doc(stmt("Allow", `{"CanonicalUser": "x"}`, "s3:GetObject", "arn:aws:s3:::b/*", ""))
			_, err := authz.New(cfg).ParseBucketPolicy(policyReq(ctx, "", nil), []byte(body))
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("UserResolver", func() {
		It("names a user by its display name and an account by its name", func(ctx SpecContext) {
			Expect(res.DisplayName(ctx, userOwner("", "alice"))).To(Equal("Alice"))
			Expect(res.DisplayName(ctx, meta.AccountOwner(acmeAcct))).To(Equal("acme"))
		})
		It("reports a user or account that does not exist as ErrNoSuchOwner", func(ctx SpecContext) {
			_, err := res.DisplayName(ctx, userOwner("", "nobody"))
			Expect(err).To(MatchError(acl.ErrNoSuchOwner))
			_, err = res.DisplayName(ctx, meta.AccountOwner("RGW00000000000000008"))
			Expect(err).To(MatchError(acl.ErrNoSuchOwner))
		})
		It("reports an account as ErrNoSuchOwner without an account store", func(ctx SpecContext) {
			_, err := authz.UserResolver{Users: store}.DisplayName(ctx, meta.AccountOwner(acmeAcct))
			Expect(err).To(MatchError(acl.ErrNoSuchOwner))
		})
		It("resolves an email to a user, or else to an account", func(ctx SpecContext) {
			Expect(res.OwnerByEmail(ctx, "alice@example.com")).To(Equal(alice))
			Expect(res.OwnerByEmail(ctx, "ACME@example.com")).To(Equal(acl.Owner{ID: acmeAcct, DisplayName: "acme"}))
		})
		It("reports an email nothing holds as ErrUnresolvableEmail", func(ctx SpecContext) {
			_, err := res.OwnerByEmail(ctx, "nobody@example.com")
			Expect(err).To(MatchError(acl.ErrUnresolvableEmail))
			_, err = authz.UserResolver{Users: store}.OwnerByEmail(ctx, "acme@example.com")
			Expect(err).To(MatchError(acl.ErrUnresolvableEmail))
		})
		Describe("a lookup that fails other than by a miss", func() {
			const secret = "grantee-secret@example.com"
			var (
				users    *opfakes.FakeUserStore
				accounts *opfakes.FakeAccountStore
				failing  authz.UserResolver
			)
			BeforeEach(func() {
				users = &opfakes.FakeUserStore{}
				accounts = &opfakes.FakeAccountStore{}
				failing = authz.UserResolver{Users: users, Accounts: accounts}
			})
			expectFailure := func(err error, want *op.Error) {
				GinkgoHelper()
				Expect(err).To(MatchError(want))
				Expect(errors.Is(err, acl.ErrNoSuchOwner)).To(BeFalse(), "not a miss: %v", err)
				Expect(errors.Is(err, acl.ErrUnresolvableEmail)).To(BeFalse(), "not a miss: %v", err)
				Expect(err.Error()).NotTo(ContainSubstring(secret), "a grantee may be a request header's value")
			}
			It("passes a user read's failure on as its S3 error", func(ctx SpecContext) {
				users.GetUserReturns(nil, fmt.Errorf("%w: reading users.uid/%s", op.ErrInternalError, secret))
				_, err := failing.DisplayName(ctx, userOwner("", secret))
				expectFailure(err, op.ErrInternalError)
			})
			It("passes an account lookup radosgw's driver does not implement on as NotImplemented", func(ctx SpecContext) {
				accounts.AccountNameReturns("", op.ErrNotImplemented)
				_, err := failing.DisplayName(ctx, meta.AccountOwner(acmeAcct))
				expectFailure(err, op.ErrNotImplemented)
			})
			It("passes a failed email index read on", func(ctx SpecContext) {
				users.GetUserByEmailReturns(nil, fmt.Errorf("%w: reading users.email/%s", op.ErrServiceUnavailable, secret))
				_, err := failing.OwnerByEmail(ctx, secret)
				expectFailure(err, op.ErrServiceUnavailable)
				Expect(accounts.GetAccountByEmailCallCount()).To(BeZero())
			})
			It("passes an account email lookup's failure on", func(ctx SpecContext) {
				users.GetUserByEmailReturns(nil, op.ErrNoSuchUser)
				accounts.GetAccountByEmailReturns(nil, op.ErrNotImplemented)
				_, err := failing.OwnerByEmail(ctx, secret)
				expectFailure(err, op.ErrNotImplemented)
			})
			It("renders a failure without an S3 error as InternalError", func(ctx SpecContext) {
				users.GetUserReturns(nil, errors.New("rados: "+secret))
				_, err := failing.DisplayName(ctx, userOwner("", "bob"))
				expectFailure(err, op.ErrInternalError)
			})
		})
	})
})
