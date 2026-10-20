package op_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// allBlockSettings sets all four of a public access block's settings, and
// someBlockSettings two of them.
var (
	allBlockSettings  = acl.PublicAccessBlock{BlockPublicACLs: true, IgnorePublicACLs: true, BlockPublicPolicy: true, RestrictPublicBuckets: true}
	someBlockSettings = acl.PublicAccessBlock{IgnorePublicACLs: true, RestrictPublicBuckets: true}
)

var _ = Describe("public access block ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	as := func(id op.Identity, method string) *op.Request {
		r := f.req(method, "plain", "")
		r.Identity = id
		return r
	}
	evaluate := func() { f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid)) }
	get := func() op.Op { return &op.GetPublicAccessBlock{} }
	put := func(b acl.PublicAccessBlock) func() op.Op {
		return func() op.Op { return &op.PutPublicAccessBlock{Block: b} }
	}
	del := func() op.Op { return &op.DeletePublicAccessBlock{} }
	// aliceGrants is alice's ACL with grants added.
	aliceGrants := func(grants ...acl.Grant) []byte {
		p := acl.DefaultPolicy(f.alice.Owner, "Alice")
		for _, g := range grants {
			p.ACL.AddGrant(g)
		}
		return encodedPolicy(p)
	}
	// blockPolicy is a policy of one statement of effect for principal on the
	// block's actions on plain.
	blockPolicy := func(effect, principal string) []byte {
		return fmt.Appendf(nil, `{"Version":"2012-10-17","Statement":[{"Effect":%q,"Principal":%s,`+
			`"Action":["s3:PutBucketPublicAccessBlock","s3:GetBucketPublicAccessBlock"],"Resource":"arn:aws:s3:::plain"}]}`,
			effect, principal)
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetPublicAccessBlock, rgw_op.h:2610-2621 at v19.2.7", &op.GetPublicAccessBlock{},
			"get_bucket_public_access_block", policy.S3GetBucketPublicAccessBlock, op.OpTypeRead),
		Entry("PutPublicAccessBlock, rgw_op.h:2596-2608 at v19.2.7", &op.PutPublicAccessBlock{},
			"put_bucket_public_access_block", policy.S3PutBucketPublicAccessBlock, op.OpTypeWrite),
		Entry("DeletePublicAccessBlock, a write authorized as s3:PutBucketPublicAccessBlock, rgw_op.h:2623-2635 and rgw_op.cc:8733 at v19.2.7",
			&op.DeletePublicAccessBlock{}, "delete_bucket_public_access_block", policy.S3PutBucketPublicAccessBlock, op.OpTypeWrite),
	)

	Describe("error order", func() {
		var (
			seen    []string
			buckets *opfakes.FakeBucketStore
			authzr  *opfakes.FakeAuthorizer
		)
		// Each fake records its step in seen and passes the call to the
		// memstore, or allows it. The ACL-consulting bucket check fails the
		// spec: no block op may ask it.
		BeforeEach(func() {
			seen = nil
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
				seen = append(seen, "load")
				return f.store.GetBucket(ctx, tenant, name)
			}
			buckets.PutBucketAttrsStub = func(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
				seen = append(seen, "write")
				return f.store.PutBucketAttrs(ctx, rec, set, rm)
			}
			authzr = &opfakes.FakeAuthorizer{}
			authzr.VerifyBucketOwnerOrPolicyStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return nil
			}
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				Fail("a block op asked for the check with the ACL fallback")
				return nil
			}
			f.env.Buckets, f.env.Authz = buckets, authzr
		})

		putParams := func() op.Op {
			return &op.PutPublicAccessBlock{Params: func(_ context.Context, o *op.PutPublicAccessBlock) error {
				seen = append(seen, "params")
				o.Block = allBlockSettings
				return nil
			}}
		}
		putRefusedParams := func() op.Op {
			return &op.PutPublicAccessBlock{Params: func(context.Context, *op.PutPublicAccessBlock) error {
				seen = append(seen, "params")
				return op.ErrMalformedXML
			}}
		}

		none := func(context.Context, *op.Request) {}
		missing := func(_ context.Context, r *op.Request) { r.Bucket = "missing" }
		refused := func(context.Context, *op.Request) {
			authzr.VerifyBucketOwnerOrPolicyStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		undecodable := func(ctx context.Context, _ *op.Request) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: {0xff}})
		}
		writeFails := func(context.Context, *op.Request) {
			buckets.PutBucketAttrsStub = func(context.Context, *op.BucketRecord, map[string][]byte, []string) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}

		DescribeTable("stops at the first step that fails, in radosgw's order",
			func(ctx SpecContext, mk func() op.Op, fail func(context.Context, *op.Request), want error, steps []string) {
				r := f.req(http.MethodPut, "plain", "")
				fail(ctx, r)
				Expect(op.Run(ctx, mk(), r)).To(MatchError(want))
				Expect(seen).To(Equal(steps))
			},
			Entry("GetPublicAccessBlock on a missing bucket, before any permission check", get, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("GetPublicAccessBlock for a refused requester", get, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("GetPublicAccessBlock on a bucket without a block", get, none, op.ErrNoSuchPublicAccessBlock, []string{"load", "verify"}),
			Entry("GetPublicAccessBlock on a block that does not decode, after the permission check", get, undecodable, op.ErrUnknown,
				[]string{"load", "verify"}),
			Entry("PutPublicAccessBlock on a missing bucket", putParams, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("PutPublicAccessBlock for a refused requester, before it reads the body", putParams, refused, op.ErrAccessDenied,
				[]string{"load", "verify"}),
			Entry("PutPublicAccessBlock whose Params refuses the body, writing nothing", putRefusedParams, none, op.ErrMalformedXML,
				[]string{"load", "verify", "params"}),
			Entry("PutPublicAccessBlock whose write fails otherwise than by a race, retrying nothing", putParams, writeFails,
				op.ErrInternalError, []string{"load", "verify", "params", "write"}),
			Entry("DeletePublicAccessBlock on a missing bucket", del, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("DeletePublicAccessBlock for a refused requester", del, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("DeletePublicAccessBlock whose write fails otherwise than by a race", del, writeFails, op.ErrInternalError,
				[]string{"load", "verify", "write"}),
		)
		DescribeTable("refuses an op mask without the op's bit before it reads the body or writes",
			func(ctx SpecContext, mk func() op.Op) {
				o := mk()
				r := f.req(http.MethodPut, "plain", "")
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).NotTo(ContainElement(BeElementOf("params", "write")))
			},
			Entry("GetPublicAccessBlock, without read", get),
			Entry("PutPublicAccessBlock, without write", putParams),
			Entry("DeletePublicAccessBlock, without write", del),
		)
		DescribeTable("authorizes its action through the check without the ACL fallback, with the permission op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, a policy.Action, perm acl.Permission) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(someBlockSettings)})
				Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(Succeed())
				Expect(authzr.VerifyBucketOwnerOrPolicyCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketOwnerOrPolicyArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(a))
				Expect(gotPerm).To(Equal(perm))
				Expect(authzr.VerifyBucketCallCount()).To(BeZero())
			},
			Entry("GetPublicAccessBlock, READ_ACP, rgw_iam_policy.h:288-289 at v19.2.7", get, policy.S3GetBucketPublicAccessBlock, acl.PermReadACP),
			Entry("PutPublicAccessBlock, WRITE_ACP, rgw_iam_policy.h:310-311 at v19.2.7", putParams, policy.S3PutBucketPublicAccessBlock, acl.PermWriteACP),
			Entry("DeletePublicAccessBlock, WRITE_ACP", del, policy.S3PutBucketPublicAccessBlock, acl.PermWriteACP),
		)
	})

	DescribeTable("gives a policy the bucket's tags",
		func(ctx SpecContext, mk func() op.Op, untagged error) {
			evaluate()
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*",` +
				`"Action":"s3:*PublicAccessBlock","Resource":"arn:aws:s3:::plain","Condition":{"StringEquals":{"s3:ResourceTag/team":"a"}}}]}`)})
			var s tags.Set
			Expect(s.Add("team", "a", tags.MaxBucketTags)).To(Succeed())
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(s)})
			Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied), "a tagged bucket")
			Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{tags.Attr})).To(Succeed())
			err := op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))
			if untagged == nil {
				Expect(err).NotTo(HaveOccurred(), "an untagged one")
			} else {
				Expect(err).To(MatchError(untagged), "an untagged one")
			}
		},
		Entry("GetPublicAccessBlock, rgw_op.cc:8679-8681 at v19.2.7", get, op.ErrNoSuchPublicAccessBlock),
		Entry("PutPublicAccessBlock, rgw_op.cc:8615-8617 at v19.2.7", put(allBlockSettings), nil),
		Entry("DeletePublicAccessBlock, rgw_op.cc:8729-8731 at v19.2.7", del, nil),
	)

	Describe("GetPublicAccessBlock", func() {
		It("answers NoSuchPublicAccessBlockConfiguration with radosgw's message for a bucket without one", func(ctx SpecContext) {
			err := op.Run(ctx, &op.GetPublicAccessBlock{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrNoSuchPublicAccessBlock), "rgw_op.cc:8693-8701 at v19.2.7")
			Expect(op.AsError(err).Status).To(Equal(http.StatusNotFound))
			Expect(op.AsError(err).Message).To(Equal("The public access block configuration was not found"))
		})
		It("takes the stored block", func(ctx SpecContext) {
			for _, b := range []acl.PublicAccessBlock{{}, someBlockSettings, allBlockSettings} {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(b)})
				o := &op.GetPublicAccessBlock{}
				Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
				Expect(o.Block).To(Equal(b))
			}
		})
		It("answers 500 for a block that does not decode, radosgw's -EIO, which only an admin reaches", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: {1, 1, 4, 0, 0, 0, 1}})
			err := op.Run(ctx, &op.GetPublicAccessBlock{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrUnknown), "rgw_op.cc:8703-8710 at v19.2.7; no S3 error row for EIO")
			Expect(op.AsError(err).Status).To(Equal(http.StatusInternalServerError))

			evaluate()
			err = op.Run(ctx, &op.GetPublicAccessBlock{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrAccessDenied), "the owner, whom the authorizer refuses")
			Expect(op.IsBeforeVerify(err)).To(BeTrue())
			admin := f.alice
			admin.Admin = true
			Expect(op.Run(ctx, &op.GetPublicAccessBlock{}, as(admin, http.MethodGet))).To(MatchError(op.ErrUnknown), "an admin")
		})
	})

	Describe("PutPublicAccessBlock", func() {
		It("stores the block as radosgw encodes it, which GetPublicAccessBlock returns", func(ctx SpecContext) {
			// The attr radosgw stored at v19.2.7 and v20.2.4 for all four
			// settings (internal/authz/evaluator_test.go, allBlocksStored).
			stored, err := base64.StdEncoding.DecodeString("AQEEAAAAAQEBAQ==")
			Expect(err).NotTo(HaveOccurred())
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Block: allBlockSettings}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, stored), "ENCODE_START(1, 1) over the four settings, rgw_public_access.h:45-52 at v19.2.7")
			g := &op.GetPublicAccessBlock{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Block).To(Equal(allBlockSettings))
		})
		It("replaces a stored block whole", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(allBlockSettings)})
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Block: someBlockSettings}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, publicAccessAttr(someBlockSettings)))
		})
		It("sets only the block attr, leaving the bucket's other attrs and its instance as they were", func(ctx SpecContext) {
			var s tags.Set
			Expect(s.Add("k", "v", tags.MaxBucketTags)).To(Succeed())
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(s), op.AttrIAMPolicy: []byte(allowAll)})
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Block: someBlockSettings}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			after := bucket(ctx)
			want := maps.Clone(before.Attrs)
			want[op.AttrPublicAccess] = publicAccessAttr(someBlockSettings)
			Expect(after.Attrs).To(Equal(want))
			Expect(after.Info).To(Equal(before.Info))
		})
		It("runs Params once the requester is authorized, and stores what it parsed", func(ctx SpecContext) {
			calls := 0
			params := func(_ context.Context, o *op.PutPublicAccessBlock) error {
				calls++
				o.Block = someBlockSettings
				return nil
			}
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Params: params}, as(f.bob, http.MethodPut))).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero(), "get_params runs in execute, after verify_permission, rgw_op.cc:8642 at v19.2.7")
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Params: params}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(calls).To(Equal(1))
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, publicAccessAttr(someBlockSettings)))
		})
		It("returns Params' error and stores nothing", func(ctx SpecContext) {
			params := func(context.Context, *op.PutPublicAccessBlock) error { return op.ErrMalformedXML }
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Params: params}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrMalformedXML))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrPublicAccess))
		})
	})

	Describe("DeletePublicAccessBlock", func() {
		It("removes the block, after which GetPublicAccessBlock finds none", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(allBlockSettings)})
			Expect(op.Run(ctx, &op.DeletePublicAccessBlock{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrPublicAccess), "rgw_op.cc:8749-8754 at v19.2.7")
			Expect(op.Run(ctx, &op.GetPublicAccessBlock{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoSuchPublicAccessBlock))
		})
		It("succeeds on a bucket without a block, rewriting the instance as radosgw does", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.DeletePublicAccessBlock{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			after := bucket(ctx)
			Expect(after.Version.Ver).To(Equal(before.Version.Ver+1), "no presence check, rgw_op.cc:8740-8755 at v19.2.7")
			Expect(after.Attrs).To(Equal(before.Attrs))
			Expect(after.Info).To(Equal(before.Info))
		})
	})

	// A block's grantee kinds, each of which radosgw's ACL fallback lets put,
	// delete or read the block (docs/ceph-upstream-bugs.md, "radosgw lets an
	// ACL grant change a bucket's public access block").
	Describe("authorization by the owner and policies alone", func() {
		type namedOp struct {
			name string
			mk   func() op.Op
		}
		ops := func() []namedOp {
			return []namedOp{{"GetPublicAccessBlock", get}, {"PutPublicAccessBlock", put(someBlockSettings)}, {"DeletePublicAccessBlock", del}}
		}
		// setBlock stores b; unchanged is that b is still stored.
		setBlock := func(ctx context.Context, b acl.PublicAccessBlock) {
			GinkgoHelper()
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(b)})
		}
		unchanged := func(ctx context.Context, b acl.PublicAccessBlock, name string) {
			GinkgoHelper()
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, publicAccessAttr(b)), "%s left the block", name)
		}
		BeforeEach(evaluate)

		DescribeTable("refuses with 403 every ACL grantee but the owner, whatever the grant",
			func(ctx SpecContext, id func() op.Identity, aclBytes func() []byte, block acl.PublicAccessBlock) {
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: aclBytes(), op.AttrPublicAccess: publicAccessAttr(block)})
				for _, o := range ops() {
					err := op.Run(ctx, o.mk(), as(id(), http.MethodPut))
					Expect(err).To(MatchError(op.ErrAccessDenied), o.name)
					Expect(op.AsError(err).Status).To(Equal(http.StatusForbidden), o.name)
					unchanged(ctx, block, o.name)
				}
			},
			Entry("a named user's WRITE_ACP", func() op.Identity { return f.bob },
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				},
				acl.PublicAccessBlock{}),
			Entry("a named user's FULL_CONTROL", func() op.Identity { return f.bob },
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermFullControl})
				},
				acl.PublicAccessBlock{}),
			Entry("a named user's READ_ACP", func() op.Identity { return f.bob },
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermReadACP})
				},
				acl.PublicAccessBlock{}),
			Entry("AllUsers' WRITE_ACP and READ_ACP with IgnorePublicAcls off, to the anonymous user", op.Anonymous,
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermWriteACP},
						acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermReadACP})
				},
				acl.PublicAccessBlock{BlockPublicACLs: true, BlockPublicPolicy: true}),
			Entry("AllUsers' WRITE_ACP and READ_ACP with IgnorePublicAcls off, to bob", func() op.Identity { return f.bob },
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermWriteACP},
						acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermReadACP})
				},
				acl.PublicAccessBlock{}),
			// X-Container-Write: anonymous stores WRITE_OBJS for that user
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_swift.cc#L195-L206 at v19.2.7 and v20.2.4).
			Entry("a Swift X-Container-Write: anonymous container, under no block", op.Anonymous,
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "anonymous", Permission: acl.PermWriteObjs})
				},
				acl.PublicAccessBlock{}),
			Entry("a Swift X-Container-Write: anonymous container, under all four settings", op.Anonymous,
				func() []byte {
					return aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "anonymous", Permission: acl.PermWriteObjs})
				},
				allBlockSettings),
		)

		It("serves the owner", func(ctx SpecContext) {
			for _, o := range ops() {
				setBlock(ctx, allBlockSettings)
				Expect(op.Run(ctx, o.mk(), f.req(http.MethodPut, "plain", ""))).To(Succeed(), o.name)
			}
		})
		It("serves a requester a bucket policy allows, and refuses one it denies, the owner included", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: blockPolicy("Allow", `{"AWS":["arn:aws:iam:::user/bob"]}`)})
			for _, o := range ops() {
				setBlock(ctx, allBlockSettings)
				Expect(op.Run(ctx, o.mk(), as(f.bob, http.MethodPut))).To(Succeed(), "bob, %s", o.name)
			}
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: blockPolicy("Deny", `{"AWS":["arn:aws:iam:::user/alice"]}`)})
			setBlock(ctx, allBlockSettings)
			for _, o := range ops() {
				Expect(op.Run(ctx, o.mk(), f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied), "alice, %s", o.name)
				unchanged(ctx, allBlockSettings, o.name)
			}
		})
		It("lets an admin through, as Run lets one through any access denial", func(ctx SpecContext) {
			admin := f.bob
			admin.Admin = true
			for _, o := range ops() {
				setBlock(ctx, allBlockSettings)
				Expect(op.Run(ctx, o.mk(), as(admin, http.MethodPut))).To(Succeed(), o.name)
			}
		})
	})

	Describe("a block it writes, enforced by the checks that read the bucket", func() {
		BeforeEach(evaluate)
		putBlock := func(ctx context.Context, b acl.PublicAccessBlock) {
			GinkgoHelper()
			Expect(op.Run(ctx, &op.PutPublicAccessBlock{Block: b}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
		}
		parsed := func(text string) *policy.Policy {
			GinkgoHelper()
			p, err := policy.Parse(text, policy.ParseOptions{Release: denc.Squid})
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		head := func(ctx context.Context, id op.Identity, key string) error {
			r := as(id, http.MethodHead)
			r.Object = meta.ObjKey{Name: key}
			return op.Run(ctx, &op.GetObject{Sink: &captureSink{}}, r)
		}

		It("refuses a public PutBucketAcl under BlockPublicAcls", func(ctx SpecContext) {
			putBlock(ctx, acl.PublicAccessBlock{BlockPublicACLs: true})
			public := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) { return publicReadOf(f.alice.Owner), nil }}
			Expect(op.Run(ctx, public, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied))
			private := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) { return acl.DefaultPolicy(f.alice.Owner, "Alice"), nil }}
			Expect(op.Run(ctx, private, f.req(http.MethodPut, "plain", ""))).To(Succeed())
		})
		It("refuses an anonymous GET through a public grant under IgnorePublicAcls, until the block is deleted", func(ctx SpecContext) {
			_, err := f.store.PutObject(ctx, bucket(ctx), meta.ObjKey{Name: "pub"}, bytes.NewReader([]byte("x")),
				op.PutParams{Size: 1, Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(publicReadOf(f.alice.Owner))}})
			Expect(err).NotTo(HaveOccurred())
			Expect(head(ctx, op.Anonymous(), "pub")).To(Succeed(), "before the block")
			putBlock(ctx, acl.PublicAccessBlock{IgnorePublicACLs: true})
			Expect(head(ctx, op.Anonymous(), "pub")).To(MatchError(op.ErrAccessDenied), "under it")
			Expect(op.Run(ctx, &op.DeletePublicAccessBlock{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(head(ctx, op.Anonymous(), "pub")).To(Succeed(), "once it is deleted")
		})
		// Amazon S3 judges a grant to everyone over TLS public, and one from
		// 1.1.1.1/32 not, where radosgw's is_public judges both the other way
		// (docs/exclusions.md, "A bucket policy is public as Amazon S3 judges
		// it").
		DescribeTable("judges PutBucketPolicy under BlockPublicPolicy by Amazon S3's IsPublic",
			func(ctx SpecContext, doc string, want error) {
				putBlock(ctx, acl.PublicAccessBlock{BlockPublicPolicy: true})
				err := op.Run(ctx, &op.PutBucketPolicy{Policy: parsed(doc)}, f.req(http.MethodPut, "plain", ""))
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
					Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(doc)))
				} else {
					Expect(err).To(MatchError(want))
					Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
				}
			},
			Entry("refusing a grant to everyone", listPlainPolicy(`"*"`, ""), op.ErrAccessDenied),
			Entry("refusing a grant to everyone over TLS", listPlainPolicy(`"*"`, `{"Bool": {"aws:SecureTransport": "true"}}`), op.ErrAccessDenied),
			Entry("storing a grant to everyone from 1.1.1.1/32 alone", listPlainPolicy(`"*"`, `{"IpAddress": {"aws:SourceIp": "1.1.1.1/32"}}`), nil),
			Entry("storing a grant to bob alone", listPlainPolicy(`{"AWS": "arn:aws:iam:::user/bob"}`, ""), nil),
		)
		It("refuses all but the owner a public policy's grant under RestrictPublicBuckets, judged by Amazon S3's IsPublic", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(allowAll)})
			Expect(head(ctx, f.bob, "small")).To(Succeed(), "bob through the public policy, before the block")
			putBlock(ctx, acl.PublicAccessBlock{RestrictPublicBuckets: true})
			Expect(head(ctx, f.bob, "small")).To(MatchError(op.ErrAccessDenied), "bob under it")
			Expect(head(ctx, f.alice, "small")).To(Succeed(), "the owner under it")
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":{"AWS":"arn:aws:iam:::user/bob"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::plain/*"}]}`)})
			Expect(head(ctx, f.bob, "small")).To(Succeed(), "bob through a policy that is not public, under it")
		})
		// radosgw's is_public evaluates a grant to everyone in a fixed
		// environment holding aws:SourceIp 1.1.1.1, so it calls one from
		// 1.1.1.1/32 alone public, and v20.2.4 refuses a non-owner it under
		// RestrictPublicBuckets
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1929-L1953 at v20.2.4);
		// Amazon S3 calls it not public (docs/exclusions.md, "A bucket policy is
		// public as Amazon S3 judges it").
		It("admits a non-owner under RestrictPublicBuckets through a grant Amazon S3 calls not public and radosgw public", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":"*","Action":["s3:GetObject","s3:PutBucketPublicAccessBlock"],"Resource":["arn:aws:s3:::plain","arn:aws:s3:::plain/*"],` +
				`"Condition":{"IpAddress":{"aws:SourceIp":"1.1.1.1/32"}}}]}`)})
			putBlock(ctx, acl.PublicAccessBlock{RestrictPublicBuckets: true})
			from := func(r *op.Request, addr string) *op.Request {
				r.RemoteAddr = addr
				return r
			}
			bobHead := func(addr string) error {
				r := from(as(f.bob, http.MethodHead), addr)
				r.Object = meta.ObjKey{Name: "small"}
				return op.Run(ctx, &op.GetObject{Sink: &captureSink{}}, r)
			}
			Expect(bobHead("1.1.1.1:1")).To(Succeed(), "bob's GET from 1.1.1.1")
			Expect(bobHead("10.0.0.1:1")).To(MatchError(op.ErrAccessDenied), "bob's GET from elsewhere, which the grant does not cover")
			o := &op.PutPublicAccessBlock{Block: allBlockSettings}
			Expect(op.Run(ctx, o, from(as(f.bob, http.MethodPut), "1.1.1.1:1"))).To(Succeed(), "bob's PutPublicAccessBlock from 1.1.1.1")
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, publicAccessAttr(allBlockSettings)))
		})
	})

	Describe("a block write that loses a race", func() {
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			calls := 0
			o := &op.PutPublicAccessBlock{Params: func(_ context.Context, o *op.PutPublicAccessBlock) error {
				calls++
				o.Block = allBlockSettings
				return nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16), "rgw_op.h:189-195 at v19.2.7")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			Expect(calls).To(Equal(1), "the body is read once")
			Expect(op.Run(ctx, &op.DeletePublicAccessBlock{}, f.req(http.MethodDelete, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(32))
		})

		DescribeTable("stops without writing when the name now names another bucket",
			func(ctx SpecContext, mk func() op.Op) {
				other := bucket(ctx)
				other.Info.Bucket.ID = "another-instance"
				buckets := &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
					if buckets.GetBucketCallCount() > 1 {
						return other, nil
					}
					return f.store.GetBucket(ctx, tenant, name)
				}
				buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
				f.env.Buckets = buckets
				Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "radosgw writes into the bucket re-created under the name")
			},
			Entry("PutPublicAccessBlock", put(allBlockSettings)),
			Entry("DeletePublicAccessBlock", del),
		)

		Describe("to another write of the bucket", func() {
			// bobMayBlock is a bucket policy that lets bob put and read the
			// block, the only grant that can.
			bobMayBlock := func() map[string][]byte {
				return map[string][]byte{op.AttrIAMPolicy: blockPolicy("Allow", `{"AWS":["arn:aws:iam:::user/bob"]}`)}
			}
			var (
				buckets *opfakes.FakeBucketStore
				// raced is the bucket's attrs as the write the first try lost
				// to left them.
				raced map[string][]byte
			)
			// race gives plain the block someBlockSettings and bob's policy, then makes
			// the first attr write lose to concurrent, another request's
			// change of the bucket's attrs, and lets every retry through.
			race := func(ctx context.Context, concurrent map[string][]byte) {
				GinkgoHelper()
				evaluate()
				set := bobMayBlock()
				set[op.AttrPublicAccess] = publicAccessAttr(someBlockSettings)
				f.setBucketAttrs(ctx, set)
				buckets = &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = f.store.GetBucket
				buckets.PutBucketAttrsStub = func(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
					if buckets.PutBucketAttrsCallCount() == 1 {
						f.setBucketAttrs(ctx, concurrent)
						raced = maps.Clone(bucket(ctx).Attrs)
					}
					return f.store.PutBucketAttrs(ctx, rec, set, rm)
				}
				f.env.Buckets = buckets
			}
			// noLongerAllowed is a policy that grants bob nothing of the block.
			noLongerAllowed := func() map[string][]byte {
				return map[string][]byte{op.AttrIAMPolicy: []byte(allowAll)}
			}
			// winnersACL is the ACL the write the first try lost to stored.
			winnersACL := func() []byte {
				return aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "carol", Name: "Carol", Permission: acl.PermRead})
			}

			DescribeTable("writes over the bucket as the retry read it, keeping the winner's ACL",
				func(ctx SpecContext, mk func() op.Op, check func(attrs map[string][]byte)) {
					race(ctx, map[string][]byte{meta.AttrACL: winnersACL()})
					Expect(op.Run(ctx, mk(), as(f.bob, http.MethodPut))).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2), "the first try lost, the retry wrote")
					attrs := bucket(ctx).Attrs
					Expect(attrs).To(HaveKeyWithValue(meta.AttrACL, winnersACL()),
						"radosgw merges the request-start attrs back, rgw_op.cc:8669-8673 at v19.2.7")
					check(attrs)
				},
				Entry("PutPublicAccessBlock", put(allBlockSettings), func(attrs map[string][]byte) {
					Expect(attrs).To(HaveKeyWithValue(op.AttrPublicAccess, publicAccessAttr(allBlockSettings)))
				}),
				Entry("DeletePublicAccessBlock", del, func(attrs map[string][]byte) {
					Expect(attrs).NotTo(HaveKey(op.AttrPublicAccess))
				}),
			)
			DescribeTable("refuses the retry once the write it lost to revoked the requester's permission, leaving that write's bucket",
				func(ctx SpecContext, mk func() op.Op, revoke func() map[string][]byte) {
					race(ctx, revoke())
					Expect(op.Run(ctx, mk(), as(f.bob, http.MethodPut))).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
					Expect(bucket(ctx).Attrs).To(Equal(raced))
				},
				Entry("PutPublicAccessBlock under a policy that no longer allows bob", put(allBlockSettings), noLongerAllowed),
				Entry("PutPublicAccessBlock under a policy that denies bob s3:PutBucketPublicAccessBlock", put(allBlockSettings), func() map[string][]byte {
					return map[string][]byte{op.AttrIAMPolicy: blockPolicy("Deny", `{"AWS":["arn:aws:iam:::user/bob"]}`)}
				}),
				Entry("DeletePublicAccessBlock under a policy that no longer allows bob", del, noLongerAllowed),
				// A grant of WRITE_ACP in the winner's ACL authorizes nothing.
				Entry("DeletePublicAccessBlock under a policy that no longer allows bob, beside an ACL granting bob WRITE_ACP", del,
					func() map[string][]byte {
						set := noLongerAllowed()
						set[meta.AttrACL] = aliceGrants(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
						return set
					}),
			)
			It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
				race(ctx, noLongerAllowed())
				admin := f.bob
				admin.Admin = true
				Expect(op.Run(ctx, &op.PutPublicAccessBlock{Block: allBlockSettings}, as(admin, http.MethodPut))).To(Succeed())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrPublicAccess, publicAccessAttr(allBlockSettings)))
			})
		})
	})
})
