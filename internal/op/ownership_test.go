package op_test

import (
	"context"
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

// ownershipAttr is o as v21.1.2 stores it, its byte inside ENCODE_START(1, 1)
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L77-L82 at v21.1.2),
// built field by field.
func ownershipAttr(o acl.Ownership) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	e.U8(uint8(o))
	e.EndStruct(f)
	return e.Bytes()
}

// rawGrant is one entry of an ACL's grant multimap, its key and the fields
// ACLGrant::encode writes.
type rawGrant struct {
	key, id, email, name, url string
	typ, group                uint32
	perm                      int32
}

// rawACL is an RGWAccessControlPolicy as radosgw encodes it, built field by
// field from v21.1.2's encode bodies, never through acl's encoders: the
// policy (2,2) holding the owner (3,2), then the list (4,3) with its maps
// initialized, its user map, its grants (5,3) each with its grantee type and
// permission at (2,2), its group map and its referer list
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L404-L409,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L360-L366,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L301-L310,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L87-L127,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl_types.h#L171-L175 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl_types.h#L202-L206 at v21.1.2).
type rawACL struct {
	ownerID, display string
	users            map[string]int32
	grants           []rawGrant
	groups           map[uint32]int32
	referers         []string
}

func (a rawACL) bytes() []byte {
	e := denc.NewEncoder()
	p := e.BeginStruct(2, 2)
	o := e.BeginStruct(3, 2)
	e.String(a.ownerID)
	e.String(a.display)
	e.EndStruct(o)
	l := e.BeginStruct(4, 3)
	e.Bool(true)
	denc.EncodeMap(e, a.users, (*denc.Encoder).String, (*denc.Encoder).I32)
	e.U32(uint32(len(a.grants)))
	for _, g := range a.grants {
		e.String(g.key)
		gf := e.BeginStruct(5, 3)
		t := e.BeginStruct(2, 2)
		e.U32(g.typ)
		e.EndStruct(t)
		e.String(g.id)
		e.String("")
		e.String(g.email)
		pf := e.BeginStruct(2, 2)
		e.I32(g.perm)
		e.EndStruct(pf)
		e.String(g.name)
		e.U32(g.group)
		e.String(g.url)
		e.EndStruct(gf)
	}
	denc.EncodeMap(e, a.groups, (*denc.Encoder).U32, (*denc.Encoder).I32)
	e.U32(uint32(len(a.referers)))
	for _, r := range a.referers {
		rf := e.BeginStruct(1, 1)
		e.String(r)
		e.U32(uint32(acl.PermRead))
		e.EndStruct(rf)
	}
	e.EndStruct(l)
	e.EndStruct(p)
	return e.Bytes()
}

// aliceDefault is the ACL radosgw's CreateBucket stores for alice,
// create_default's
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L341-L349 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L430-L434 at v21.1.2): alice,
// "Alice", owning it with one FULL_CONTROL grant to herself under her
// display name.
func aliceDefault() rawACL {
	return rawACL{
		ownerID: "alice", display: "Alice",
		users:  map[string]int32{"alice": int32(acl.PermFullControl)},
		grants: []rawGrant{{key: "alice", id: "alice", name: "Alice", typ: uint32(acl.GranteeCanonUser), perm: int32(acl.PermFullControl)}},
	}
}

// ownershipPolicy is a bucket policy of one statement of effect for the user
// arn on both ownership controls actions on plain.
func ownershipPolicy(effect, arn string) []byte {
	return fmt.Appendf(nil, `{"Version":"2012-10-17","Statement":[{"Effect":%q,"Principal":{"AWS":[%q]},`+
		`"Action":["s3:PutBucketOwnershipControls","s3:GetBucketOwnershipControls"],"Resource":"arn:aws:s3:::plain"}]}`,
		effect, arn)
}

// otherAccount is an account other than the bucket owner's.
const otherAccount = "RGW11111111111111111"

// privateMessage is v21.1.2's message for BucketOwnerEnforced over an ACL
// other than the default
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10295-L10296 at v21.1.2).
const privateMessage = "The bucket's ACL must be made private before setting BucketOwnerEnforced."

var _ = Describe("bucket ownership controls ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	// setting is a Params that sets o's Ownership to own.
	setting := func(own acl.Ownership) func(context.Context, *op.PutBucketOwnershipControls) error {
		return func(_ context.Context, o *op.PutBucketOwnershipControls) error {
			o.Ownership = own
			return nil
		}
	}
	put := func(own acl.Ownership) *op.PutBucketOwnershipControls {
		return &op.PutBucketOwnershipControls{Params: setting(own)}
	}
	putReq := func() *op.Request { return f.req(http.MethodPut, "plain", "") }
	getReq := func() *op.Request { return f.req(http.MethodGet, "plain", "") }
	delReq := func() *op.Request { return f.req(http.MethodDelete, "plain", "") }
	teamTag := func(v string) []byte {
		GinkgoHelper()
		var s tags.Set
		Expect(s.Add("team", v, tags.MaxBucketTags)).To(Succeed())
		return encodeTags(s)
	}

	DescribeTable("is v21.1.2's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetBucketOwnershipControls, rgw_op.h:2012-2021 at v21.1.2", &op.GetBucketOwnershipControls{},
			"get_bucket_ownership_controls", policy.S3GetBucketOwnershipControls, op.OpTypeRead),
		Entry("PutBucketOwnershipControls, rgw_op.h:1999-2010 at v21.1.2", &op.PutBucketOwnershipControls{},
			"put_bucket_ownership_controls", policy.S3PutBucketOwnershipControls, op.OpTypeWrite),
		Entry("DeleteBucketOwnershipControls, a write authorized as s3:PutBucketOwnershipControls, rgw_op.h:2023-2030 and rgw_op.cc:10336-10342 at v21.1.2",
			&op.DeleteBucketOwnershipControls{}, "delete_bucket_ownership_controls", policy.S3PutBucketOwnershipControls, op.OpTypeWrite),
	)

	Describe("error order", func() {
		var (
			seen    []string
			buckets *opfakes.FakeBucketStore
			authzr  *opfakes.FakeAuthorizer
		)
		// Each fake records its step in seen and passes the call to the
		// memstore, or allows it; Params records "params".
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
			f.env.Buckets, f.env.Authz = buckets, authzr
		})
		params := func(own acl.Ownership, err error) func(context.Context, *op.PutBucketOwnershipControls) error {
			return func(_ context.Context, o *op.PutBucketOwnershipControls) error {
				seen = append(seen, "params")
				o.Ownership = own
				return err
			}
		}
		get := func() op.Op { return &op.GetBucketOwnershipControls{} }
		putOf := func(own acl.Ownership, err error) func() op.Op {
			return func() op.Op { return &op.PutBucketOwnershipControls{Params: params(own, err)} }
		}
		del := func() op.Op { return &op.DeleteBucketOwnershipControls{} }

		none := func(context.Context, *op.Request) {}
		missing := func(_ context.Context, r *op.Request) { r.Bucket = "missing" }
		refused := func(context.Context, *op.Request) {
			authzr.VerifyBucketOwnerOrPolicyStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		stored := func(set map[string][]byte) func(context.Context, *op.Request) {
			return func(ctx context.Context, _ *op.Request) { f.setBucketAttrs(ctx, set) }
		}
		publicRead := func() []byte {
			a := aliceDefault()
			a.grants = append(a.grants, rawGrant{typ: uint32(acl.GranteeGroup), group: acl.GroupAllUsers, perm: int32(acl.PermRead)})
			a.groups = map[uint32]int32{acl.GroupAllUsers: int32(acl.PermRead)}
			return a.bytes()
		}
		writeFails := func(context.Context, *op.Request) {
			buckets.PutBucketAttrsStub = func(context.Context, *op.BucketRecord, map[string][]byte, []string) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}

		DescribeTable("stops at the first step that fails, in v21.1.2's order",
			func(ctx SpecContext, mk func() op.Op, fail func(context.Context, *op.Request), want error, steps []string) {
				r := putReq()
				fail(ctx, r)
				Expect(op.Run(ctx, mk(), r)).To(MatchError(want))
				Expect(seen).To(Equal(steps))
			},
			Entry("GetBucketOwnershipControls on a missing bucket, before any permission check", get, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("GetBucketOwnershipControls for a refused requester, before it looks for the attr", get, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("GetBucketOwnershipControls on a bucket without ownership controls", get, none, op.ErrNoSuchOwnershipControls, []string{"load", "verify"}),
			Entry("GetBucketOwnershipControls on an attr that does not decode", get,
				stored(map[string][]byte{acl.AttrOwnershipControls: {0xff}}), op.ErrUnknown, []string{"load", "verify"}),
			Entry("PutBucketOwnershipControls on a missing bucket, its body unread", putOf(acl.ObjectWriter, nil), missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("PutBucketOwnershipControls for a refused requester, its body unread: get_params runs in execute, rgw_op.cc:10257 at v21.1.2",
				putOf(acl.ObjectWriter, nil), refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("PutBucketOwnershipControls whose body Params refuses, writing nothing", putOf(acl.ObjectWriter, op.ErrMalformedXML), none,
				op.ErrMalformedXML, []string{"load", "verify", "params"}),
			Entry("PutBucketOwnershipControls of BucketOwnerEnforced over a public ACL, writing nothing, rgw_op.cc:10282-10300 at v21.1.2",
				putOf(acl.BucketOwnerEnforced, nil), stored(map[string][]byte{meta.AttrACL: publicRead()}),
				op.ErrInvalidBucketACL, []string{"load", "verify", "params"}),
			Entry("PutBucketOwnershipControls of BucketOwnerEnforced over an ACL that does not decode, -EIO, rgw_op.cc:10287-10293 at v21.1.2",
				putOf(acl.BucketOwnerEnforced, nil), stored(map[string][]byte{meta.AttrACL: {0xff}}), op.ErrUnknown, []string{"load", "verify", "params"}),
			Entry("PutBucketOwnershipControls whose write fails otherwise than by a race, retrying nothing", putOf(acl.ObjectWriter, nil), writeFails,
				op.ErrInternalError, []string{"load", "verify", "params", "write"}),
			Entry("DeleteBucketOwnershipControls on a missing bucket", del, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("DeleteBucketOwnershipControls for a refused requester", del, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("DeleteBucketOwnershipControls whose write fails otherwise than by a race", del, writeFails, op.ErrInternalError,
				[]string{"load", "verify", "write"}),
		)
		DescribeTable("refuses an op mask without the op's bit before Params or a write",
			func(ctx SpecContext, mk func() op.Op) {
				o := mk()
				r := putReq()
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).To(Equal([]string{"load", "verify"}))
			},
			Entry("GetBucketOwnershipControls, without read", get),
			Entry("PutBucketOwnershipControls, without write", putOf(acl.ObjectWriter, nil)),
			Entry("DeleteBucketOwnershipControls, without write", del),
		)
		DescribeTable("authorizes its action through the check without the ACL fallback, with the permission v21.1.2's op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, a policy.Action, perm acl.Permission, steps []string) {
				stored(map[string][]byte{acl.AttrOwnershipControls: ownershipAttr(acl.BucketOwnerPreferred)})(ctx, nil)
				Expect(op.Run(ctx, mk(), putReq())).To(Succeed())
				Expect(seen).To(Equal(steps))
				Expect(authzr.VerifyBucketCallCount()).To(BeZero(), "no ACL fallback")
				Expect(authzr.VerifyBucketOwnerOrPolicyCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketOwnerOrPolicyArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(a))
				Expect(gotPerm).To(Equal(perm))
			},
			Entry("GetBucketOwnershipControls, READ_ACP, rgw_iam_policy.h:314-315 at v21.1.2", get, policy.S3GetBucketOwnershipControls,
				acl.PermReadACP, []string{"load", "verify"}),
			Entry("PutBucketOwnershipControls, WRITE_ACP, rgw_iam_policy.h:338-339 at v21.1.2", putOf(acl.ObjectWriter, nil),
				policy.S3PutBucketOwnershipControls, acl.PermWriteACP, []string{"load", "verify", "params", "write"}),
			Entry("DeleteBucketOwnershipControls, WRITE_ACP as s3:PutBucketOwnershipControls", del, policy.S3PutBucketOwnershipControls,
				acl.PermWriteACP, []string{"load", "verify", "write"}),
		)
	})

	// Amazon S3 maps a bucket's WRITE_ACP to s3:PutBucketAcl alone and its
	// READ_ACP to s3:GetBucketAcl alone
	// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#acl-access-policy-permission-mapping),
	// so no ACL grant reaches the ownership controls actions, which v21.1.2's
	// op_to_perm maps to READ_ACP and WRITE_ACP
	// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L314-L315 and
	// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L338-L339 at v21.1.2).
	Describe("authorization by the owner and policies alone", func() {
		BeforeEach(func() { f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid)) })
		type namedOp struct {
			name string
			mk   func() op.Op
			req  func() *op.Request
		}
		ops := func() []namedOp {
			return []namedOp{
				{"GetBucketOwnershipControls", func() op.Op { return &op.GetBucketOwnershipControls{} }, getReq},
				{"PutBucketOwnershipControls", func() op.Op { return put(acl.BucketOwnerPreferred) }, putReq},
				{"DeleteBucketOwnershipControls", func() op.Op { return &op.DeleteBucketOwnershipControls{} }, delReq},
			}
		}
		as := func(id op.Identity, req func() *op.Request) *op.Request {
			r := req()
			r.Identity = id
			return r
		}
		// aliceGrants is alice's ACL with grants added.
		aliceGrants := func(grants ...acl.Grant) []byte {
			p := acl.DefaultPolicy(f.alice.Owner, "Alice")
			for _, g := range grants {
				p.ACL.AddGrant(g)
			}
			return encodedPolicy(p)
		}
		stored := func(ctx context.Context) {
			GinkgoHelper()
			f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: ownershipAttr(acl.ObjectWriter)})
		}
		unchanged := func(ctx context.Context, name string) {
			GinkgoHelper()
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(acl.ObjectWriter)), "%s left the setting", name)
		}

		DescribeTable("refuses with 403 every ACL grantee but the owner, whatever the grant",
			func(ctx SpecContext, id func() op.Identity, grants ...acl.Grant) {
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: aliceGrants(grants...)})
				for _, o := range ops() {
					stored(ctx)
					err := op.Run(ctx, o.mk(), as(id(), o.req))
					Expect(err).To(MatchError(op.ErrAccessDenied), o.name)
					Expect(op.AsError(err).Status).To(Equal(http.StatusForbidden), o.name)
					unchanged(ctx, o.name)
				}
			},
			Entry("a named user's WRITE_ACP", func() op.Identity { return f.bob },
				acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP}),
			Entry("a named user's FULL_CONTROL", func() op.Identity { return f.bob },
				acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermFullControl}),
			Entry("a named user's READ_ACP", func() op.Identity { return f.bob },
				acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermReadACP}),
			Entry("AllUsers' WRITE_ACP and READ_ACP, to the anonymous user", op.Anonymous,
				acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermWriteACP},
				acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermReadACP}),
			Entry("AuthenticatedUsers' FULL_CONTROL, to bob", func() op.Identity { return f.bob },
				acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAuthenticatedUsers, Permission: acl.PermFullControl}),
			// Across accounts the identity side and the resource side must
			// each allow; the identity's own policy allows, so only the
			// resource side, the owner or a bucket policy, can refuse it.
			Entry("another account's FULL_CONTROL, to its user whose own policy allows the actions", func() op.Identity {
				u := meta.UserInfo{UserID: meta.UserID{ID: "carol"}, DisplayName: "Carol", AccountID: otherAccount, OpMask: op.OpTypeAll}
				return op.Identity{
					User: &u, Owner: meta.AccountOwner(otherAccount), Account: &meta.AccountInfo{ID: otherAccount}, OpMask: op.OpTypeAll,
					Attrs: map[string][]byte{"user.rgw.user-policy": userPolicyAttr(
						policyStatement("Allow", "s3:PutBucketOwnershipControls", "arn:aws:s3:::plain"),
						policyStatement("Allow", "s3:GetBucketOwnershipControls", "arn:aws:s3:::plain"))},
				}
			}, acl.Grant{Type: acl.GranteeCanonUser, ID: otherAccount, Name: "acme", Permission: acl.PermFullControl}),
		)
		It("serves the owner, whom no policy names", func(ctx SpecContext) {
			for _, o := range ops() {
				stored(ctx)
				Expect(op.Run(ctx, o.mk(), o.req())).To(Succeed(), o.name)
			}
		})
		DescribeTable("bounds the owner by its subuser's permission mask, with the permission v21.1.2's op_to_perm gives the action",
			func(ctx SpecContext, mask acl.Permission, okGet, okWrite bool) {
				stored(ctx)
				info := *f.alice.User
				info.SubUsers = map[string]meta.SubUser{"alice:sub": {Name: "alice:sub", Perm: uint32(mask)}}
				sub := f.alice
				sub.User, sub.SubUser = &info, "alice:sub"
				for _, o := range ops() {
					want := okWrite
					if o.name == "GetBucketOwnershipControls" {
						want = okGet
					}
					err := op.Run(ctx, o.mk(), as(sub, o.req))
					if want {
						Expect(err).NotTo(HaveOccurred(), o.name)
						stored(ctx)
						continue
					}
					Expect(err).To(MatchError(op.ErrAccessDenied), o.name)
					unchanged(ctx, o.name)
				}
			},
			Entry("READ_ACP alone reads", acl.PermReadACP, true, false),
			Entry("WRITE_ACP alone writes", acl.PermWriteACP, false, true),
			Entry("READ and WRITE do neither", acl.PermRead|acl.PermWrite, false, false),
		)
		It("serves a requester a bucket policy allows, and refuses one it denies, the owner included", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: ownershipPolicy("Allow", "arn:aws:iam:::user/bob")})
			for _, o := range ops() {
				stored(ctx)
				Expect(op.Run(ctx, o.mk(), as(f.bob, o.req))).To(Succeed(), "bob, %s", o.name)
			}
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: ownershipPolicy("Deny", "arn:aws:iam:::user/alice")})
			for _, o := range ops() {
				stored(ctx)
				Expect(op.Run(ctx, o.mk(), o.req())).To(MatchError(op.ErrAccessDenied), "alice, %s", o.name)
				unchanged(ctx, o.name)
			}
		})
		It("lets an admin through, as Run lets one through any access denial", func(ctx SpecContext) {
			admin := f.bob
			admin.Admin = true
			for _, o := range ops() {
				stored(ctx)
				Expect(op.Run(ctx, o.mk(), as(admin, o.req))).To(Succeed(), o.name)
			}
		})
		DescribeTable("gives a policy none of the bucket's tags, as no ownership controls op calls rgw_iam_add_buckettags",
			func(ctx SpecContext, mk func() op.Op, req func() *op.Request, action string) {
				f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: ownershipAttr(acl.ObjectWriter), tags.Attr: teamTag("a")})
				deny := func(condition string) []byte {
					return fmt.Appendf(nil,
						`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":%q,"Resource":"arn:aws:s3:::plain"%s}]}`,
						action, condition)
				}
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: deny("")})
				Expect(op.Run(ctx, mk(), req())).To(MatchError(op.ErrAccessDenied), "the policy names the op's action")
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: deny(`,"Condition":{"StringEquals":{"s3:ResourceTag/team":"a"}}`)})
				Expect(op.Run(ctx, mk(), req())).To(Succeed(), "a tagged bucket the tag-conditioned Deny does not reach")
			},
			Entry("GetBucketOwnershipControls, rgw_op.cc:10307-10313 at v21.1.2", func() op.Op { return &op.GetBucketOwnershipControls{} },
				getReq, "s3:GetBucketOwnershipControls"),
			Entry("PutBucketOwnershipControls, rgw_op.cc:10247-10253 at v21.1.2", func() op.Op { return put(acl.ObjectWriter) },
				putReq, "s3:PutBucketOwnershipControls"),
			Entry("DeleteBucketOwnershipControls, rgw_op.cc:10336-10342 at v21.1.2", func() op.Op { return &op.DeleteBucketOwnershipControls{} },
				delReq, "s3:PutBucketOwnershipControls"),
		)
	})

	Describe("GetBucketOwnershipControls", func() {
		run := func(ctx context.Context) (*op.GetBucketOwnershipControls, error) {
			o := &op.GetBucketOwnershipControls{}
			return o, op.Run(ctx, o, getReq())
		}

		It("answers a bucket without the attr with 404 and execute's message", func(ctx SpecContext) {
			_, err := run(ctx)
			Expect(err).To(MatchError(op.ErrNoSuchOwnershipControls), "-ENOENT, which send_response maps, rgw_rest_s3.cc:4590-4596 at v21.1.2")
			e := op.AsError(err)
			Expect(e.Status).To(Equal(404))
			Expect(e.Code).To(Equal("OwnershipControlsNotFoundError"))
			Expect(e.Message).To(Equal("The bucket ownership controls were not found"), "rgw_op.cc:10318-10322 at v21.1.2")
		})
		DescribeTable("takes the stored setting as it decodes",
			func(ctx SpecContext, b []byte, want acl.Ownership) {
				f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: b})
				o, err := run(ctx)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.Ownership).To(Equal(want))
			},
			Entry("BucketOwnerEnforced", ownershipAttr(acl.BucketOwnerEnforced), acl.BucketOwnerEnforced),
			Entry("BucketOwnerPreferred", ownershipAttr(acl.BucketOwnerPreferred), acl.BucketOwnerPreferred),
			Entry("ObjectWriter", ownershipAttr(acl.ObjectWriter), acl.ObjectWriter),
			Entry("a byte outside the three, kept as stored as the enum's decode keeps it", ownershipAttr(7), acl.Ownership(7)),
			Entry("one with bytes after it, which decode does not read", append(ownershipAttr(acl.BucketOwnerPreferred), 'x'), acl.BucketOwnerPreferred),
		)
		DescribeTable("answers an attr that does not decode with 500 UnknownError, v21.1.2's -EIO, which no error row holds",
			func(ctx SpecContext, b []byte) {
				f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: b})
				_, err := run(ctx)
				Expect(err).To(MatchError(op.ErrUnknown), "rgw_op.cc:10324-10332 at v21.1.2")
				Expect(op.AsError(err).Status).To(Equal(500))
			},
			Entry("a truncated one", []byte{0xff}),
			Entry("an empty one, which is present, so not 404", []byte{}),
			Entry("one whose compat version is above 1", []byte{2, 2, 1, 0, 0, 0, 0}),
		)
		It("writes nothing", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: ownershipAttr(acl.ObjectWriter)})
			before := bucket(ctx)
			_, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(bucket(ctx)).To(Equal(before))
		})
	})

	Describe("PutBucketOwnershipControls", func() {
		DescribeTable("stores the setting in the ownership attr alone, as v21.1.2 encodes it, which GetBucketOwnershipControls reads back",
			func(ctx SpecContext, own acl.Ownership) {
				f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag("a")})
				before := bucket(ctx).Attrs
				Expect(op.Run(ctx, put(own), putReq())).To(Succeed())
				want := maps.Clone(before)
				want[acl.AttrOwnershipControls] = ownershipAttr(own)
				Expect(bucket(ctx).Attrs).To(Equal(want), "rgw_op.cc:10269-10270 and :10302 at v21.1.2")
				g := &op.GetBucketOwnershipControls{}
				Expect(op.Run(ctx, g, getReq())).To(Succeed())
				Expect(g.Ownership).To(Equal(own))
			},
			Entry("BucketOwnerEnforced over the bucket's default ACL", acl.BucketOwnerEnforced),
			Entry("BucketOwnerPreferred", acl.BucketOwnerPreferred),
			Entry("ObjectWriter", acl.ObjectWriter),
		)
		It("writes the same bytes on Tentacle", func(ctx SpecContext) {
			f = newWriteFixture(ctx, denc.Tentacle)
			Expect(op.Run(ctx, put(acl.BucketOwnerPreferred), putReq())).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(acl.BucketOwnerPreferred)))
		})
		It("replaces a stored setting whole without decoding it", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: {0xff}})
			Expect(op.Run(ctx, put(acl.ObjectWriter), putReq())).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(acl.ObjectWriter)))
		})
		It("runs Params only once the requester is authorized", func(ctx SpecContext) {
			r := putReq()
			r.Identity = f.bob
			o := &op.PutBucketOwnershipControls{Params: func(context.Context, *op.PutBucketOwnershipControls) error {
				Fail("Params ran for a refused requester")
				return nil
			}}
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
		})

		// v21.1.2 compares the bucket's ACL with create_default of
		// s->bucket_owner, the owner that ACL names, by
		// RGWAccessControlPolicy's operator==: the owners, the user and group
		// maps, the referer lists and the grant multimaps, a canonical user
		// grantee's display name included
		// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10272-L10300,
		// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.cc#L22-L66 and
		// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L20-L26 at v21.1.2).
		Describe("BucketOwnerEnforced over the bucket's ACL", func() {
			enforce := func(ctx context.Context, stored []byte) error {
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: stored})
				return op.Run(ctx, put(acl.BucketOwnerEnforced), putReq())
			}
			DescribeTable("is stored over an ACL equal to the default of the owner it names",
				func(ctx SpecContext, stored func() []byte) {
					Expect(enforce(ctx, stored())).To(Succeed())
					Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(acl.BucketOwnerEnforced)))
				},
				Entry("the ACL radosgw's CreateBucket stores, built field by field", func() []byte { return aliceDefault().bytes() }),
				Entry("the same ACL with bytes after it, which decode does not read", func() []byte { return append(aliceDefault().bytes(), 'x') }),
				Entry("another owner's default, as s->bucket_owner is the owner the ACL names, rgw_op.cc:628 at v21.1.2", func() []byte {
					return rawACL{
						ownerID: "bob", display: "Bob",
						users:  map[string]int32{"bob": int32(acl.PermFullControl)},
						grants: []rawGrant{{key: "bob", id: "bob", name: "Bob", typ: uint32(acl.GranteeCanonUser), perm: int32(acl.PermFullControl)}},
					}.bytes()
				}),
			)
			It("is stored on a bucket without an ACL attr, which v21.1.2 does not compare", func(ctx SpecContext) {
				Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{meta.AttrACL})).To(Succeed())
				Expect(op.Run(ctx, put(acl.BucketOwnerEnforced), putReq())).To(Succeed(), "rgw_op.cc:10286 at v21.1.2")
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(acl.BucketOwnerEnforced)))
			})
			DescribeTable("is refused with 400 InvalidBucketAclWithObjectOwnership and v21.1.2's message over any other ACL, storing nothing",
				func(ctx SpecContext, edit func(*rawACL)) {
					a := aliceDefault()
					edit(&a)
					err := enforce(ctx, a.bytes())
					Expect(err).To(MatchError(op.ErrInvalidBucketACL))
					Expect(op.AsError(err).Status).To(Equal(400))
					Expect(messageOf(err)).To(Equal(privateMessage))
					Expect(bucket(ctx).Attrs).NotTo(HaveKey(acl.AttrOwnershipControls))
				},
				Entry("public-read, a grant to AllUsers", func(a *rawACL) {
					a.grants = append(a.grants, rawGrant{typ: uint32(acl.GranteeGroup), group: acl.GroupAllUsers, perm: int32(acl.PermRead)})
					a.groups = map[uint32]int32{acl.GroupAllUsers: int32(acl.PermRead)}
				}),
				Entry("a grant to another user", func(a *rawACL) {
					a.grants = append(a.grants, rawGrant{key: "bob", id: "bob", name: "Bob", typ: uint32(acl.GranteeCanonUser), perm: int32(acl.PermRead)})
					a.users["bob"] = int32(acl.PermRead)
				}),
				Entry("the owner's grant under another display name, which the grantee's comparison includes", func(a *rawACL) {
					a.grants[0].name = "Alicia"
				}),
				Entry("an owner's display name that differs from its grant's", func(a *rawACL) { a.display = "Alicia" }),
				Entry("the owner holding READ alone", func(a *rawACL) {
					a.grants[0].perm = int32(acl.PermRead)
					a.users["alice"] = int32(acl.PermRead)
				}),
				Entry("the owner's grant twice", func(a *rawACL) { a.grants = append(a.grants, a.grants[0]) }),
				Entry("a user map entry no grant gives", func(a *rawACL) { a.users["bob"] = int32(acl.PermRead) }),
				Entry("a group map entry no grant gives", func(a *rawACL) {
					a.groups = map[uint32]int32{acl.GroupAuthenticatedUsers: int32(acl.PermRead)}
				}),
				Entry("a referer", func(a *rawACL) { a.referers = []string{"*"} }),
			)
			It("answers an ACL that does not decode with 500 UnknownError, storing nothing", func(ctx SpecContext) {
				err := enforce(ctx, []byte{0xff})
				Expect(err).To(MatchError(op.ErrUnknown), "-EIO, rgw_op.cc:10287-10293 at v21.1.2")
				Expect(op.AsError(err).Status).To(Equal(500))
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(acl.AttrOwnershipControls))
			})
			DescribeTable("is not looked at for another setting",
				func(ctx SpecContext, own acl.Ownership) {
					a := aliceDefault()
					a.grants = append(a.grants, rawGrant{typ: uint32(acl.GranteeGroup), group: acl.GroupAllUsers, perm: int32(acl.PermRead)})
					a.groups = map[uint32]int32{acl.GroupAllUsers: int32(acl.PermRead)}
					f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: a.bytes()})
					Expect(op.Run(ctx, put(own), putReq())).To(Succeed())
					Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(own)))
				},
				Entry("BucketOwnerPreferred", acl.BucketOwnerPreferred),
				Entry("ObjectWriter", acl.ObjectWriter),
			)
		})
	})

	Describe("DeleteBucketOwnershipControls", func() {
		It("removes the ownership attr and nothing else, after which GetBucketOwnershipControls finds none", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag("a")})
			before := bucket(ctx).Attrs
			f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: ownershipAttr(acl.BucketOwnerEnforced)})
			Expect(op.Run(ctx, &op.DeleteBucketOwnershipControls{}, delReq())).To(Succeed())
			Expect(bucket(ctx).Attrs).To(Equal(before), "rgw_op.cc:10353-10357 at v21.1.2")
			Expect(op.Run(ctx, &op.GetBucketOwnershipControls{}, getReq())).To(MatchError(op.ErrNoSuchOwnershipControls))
		})
		It("removes a stored attr without decoding it", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{acl.AttrOwnershipControls: {0xff}})
			Expect(op.Run(ctx, &op.DeleteBucketOwnershipControls{}, delReq())).To(Succeed())
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(acl.AttrOwnershipControls))
		})
		It("succeeds on a bucket without the attr, rewriting the instance as v21.1.2's put_info does", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.DeleteBucketOwnershipControls{}, delReq())).To(Succeed())
			after := bucket(ctx)
			Expect(after.Version.Ver).To(Equal(before.Version.Ver+1), "no presence check, rgw_op.cc:10344-10358 at v21.1.2")
			Expect(after.Attrs).To(Equal(before.Attrs))
		})
	})

	Describe("an ownership controls write that loses a race", func() {
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			Expect(op.Run(ctx, put(acl.BucketOwnerPreferred), putReq())).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16), "retry_raced_bucket_write, rgw_op.cc:10277 at v21.1.2")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			_, _, set, rm := buckets.PutBucketAttrsArgsForCall(15)
			Expect(set).To(Equal(map[string][]byte{acl.AttrOwnershipControls: ownershipAttr(acl.BucketOwnerPreferred)}), "the setting alone on each try")
			Expect(rm).To(BeEmpty())
			Expect(op.Run(ctx, &op.DeleteBucketOwnershipControls{}, delReq())).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(32))
			_, _, set, rm = buckets.PutBucketAttrsArgsForCall(31)
			Expect(set).To(BeEmpty())
			Expect(rm).To(ConsistOf(acl.AttrOwnershipControls))
		})
		DescribeTable("stops without writing when the name now names another bucket",
			func(ctx SpecContext, mk func() op.Op, req func() *op.Request) {
				other := bucket(ctx)
				other.Info.Bucket.ID = "another-instance"
				buckets := &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
					if buckets.GetBucketCallCount() == 1 {
						return f.store.GetBucket(ctx, tenant, name)
					}
					return other, nil
				}
				buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
				f.env.Buckets = buckets
				before := bucket(ctx)
				Expect(op.Run(ctx, mk(), req())).To(MatchError(op.ErrConcurrentModification))
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "v21.1.2 writes into the bucket re-created under the name, rgw_op.cc:10277-10304 at v21.1.2")
				Expect(bucket(ctx)).To(Equal(before))
			},
			Entry("PutBucketOwnershipControls", func() op.Op { return put(acl.ObjectWriter) }, putReq),
			Entry("DeleteBucketOwnershipControls, rgw_op.cc:10353-10357 at v21.1.2", func() op.Op { return &op.DeleteBucketOwnershipControls{} }, delReq),
		)

		Describe("to another write of the bucket", func() {
			// bobMayOwn is a bucket policy that lets bob read and change the
			// setting, the only grant that can.
			bobMayOwn := func() []byte { return ownershipPolicy("Allow", "arn:aws:iam:::user/bob") }
			// noLongerAllowed is a policy that grants bob nothing of the setting.
			noLongerAllowed := func() map[string][]byte { return map[string][]byte{op.AttrIAMPolicy: []byte(allowAll)} }
			denyBob := func() map[string][]byte {
				return map[string][]byte{op.AttrIAMPolicy: ownershipPolicy("Deny", "arn:aws:iam:::user/bob")}
			}
			retagged := func() map[string][]byte { return map[string][]byte{tags.Attr: teamTag("b")} }

			var (
				buckets *opfakes.FakeBucketStore
				// raced is the bucket's attrs as the write the first try
				// lost to left them.
				raced map[string][]byte
			)
			// race gives plain the setting ObjectWriter, the tag set team=a and
			// bob's policy, then makes the first attr write lose to concurrent,
			// another request's change of the bucket's attrs, and lets every
			// retry through to the store.
			race := func(ctx context.Context, concurrent map[string][]byte) {
				GinkgoHelper()
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				f.setBucketAttrs(ctx, map[string][]byte{
					op.AttrIAMPolicy: bobMayOwn(), acl.AttrOwnershipControls: ownershipAttr(acl.ObjectWriter), tags.Attr: teamTag("a"),
				})
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
			asBob := func(r *op.Request) *op.Request {
				r.Identity = f.bob
				return r
			}
			type write struct {
				op    op.Op
				req   *op.Request
				check func(attrs map[string][]byte)
			}
			putPreferred := func() write {
				return write{
					op: put(acl.BucketOwnerPreferred), req: asBob(putReq()),
					check: func(attrs map[string][]byte) {
						Expect(attrs).To(HaveKeyWithValue(acl.AttrOwnershipControls, ownershipAttr(acl.BucketOwnerPreferred)))
					},
				}
			}
			del := func() write {
				return write{
					op: &op.DeleteBucketOwnershipControls{}, req: asBob(delReq()),
					check: func(attrs map[string][]byte) { Expect(attrs).NotTo(HaveKey(acl.AttrOwnershipControls)) },
				}
			}

			DescribeTable("writes over the bucket as the retry read it, keeping the other write's change",
				func(ctx SpecContext, mk func() write) {
					race(ctx, retagged())
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2), "the first try lost, the retry wrote")
					attrs := bucket(ctx).Attrs
					w.check(attrs)
					Expect(attrs).To(HaveKeyWithValue(tags.Attr, teamTag("b")), "s->bucket->get_attrs() inside the retry, rgw_op.cc:10279 and :10354 at v21.1.2")
				},
				Entry("PutBucketOwnershipControls", putPreferred),
				Entry("DeleteBucketOwnershipControls", del),
			)
			DescribeTable("refuses the retry once the write it lost to revoked the requester's permission, leaving the revoking bucket",
				func(ctx SpecContext, mk func() write, revoke func() map[string][]byte) {
					race(ctx, revoke())
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
					Expect(bucket(ctx).Attrs).To(Equal(raced))
				},
				Entry("PutBucketOwnershipControls under a policy that no longer allows bob", putPreferred, noLongerAllowed),
				Entry("PutBucketOwnershipControls under a policy that denies bob the setting", putPreferred, denyBob),
				Entry("DeleteBucketOwnershipControls under a policy that no longer allows bob", del, noLongerAllowed),
				// A grant of WRITE_ACP in the winner's ACL authorizes nothing.
				Entry("DeleteBucketOwnershipControls under a policy that no longer allows bob, beside an ACL granting bob WRITE_ACP", del,
					func() map[string][]byte {
						set := noLongerAllowed()
						p := acl.DefaultPolicy(f.alice.Owner, "Alice")
						p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
						set[meta.AttrACL] = encodedPolicy(p)
						return set
					}),
			)
			DescribeTable("writes on a retry whose requester the write it lost to left the permission",
				func(ctx SpecContext, mk func() write) {
					race(ctx, map[string][]byte{meta.AttrACL: f.aliceACL, op.AttrIAMPolicy: append(bobMayOwn(), ' ')})
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
					w.check(bucket(ctx).Attrs)
				},
				Entry("PutBucketOwnershipControls", putPreferred),
				Entry("DeleteBucketOwnershipControls", del),
			)
			It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
				race(ctx, noLongerAllowed())
				w := putPreferred()
				w.req.Identity.Admin = true
				Expect(op.Run(ctx, w.op, w.req)).To(Succeed())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
				w.check(bucket(ctx).Attrs)
			})
			It("compares BucketOwnerEnforced with the ACL the retry read, refusing it once the write it lost to made the ACL public", func(ctx SpecContext) {
				public := aliceDefault()
				public.grants = append(public.grants, rawGrant{typ: uint32(acl.GranteeGroup), group: acl.GroupAllUsers, perm: int32(acl.PermRead)})
				public.groups = map[uint32]int32{acl.GroupAllUsers: int32(acl.PermRead)}
				race(ctx, map[string][]byte{meta.AttrACL: public.bytes()})
				err := op.Run(ctx, put(acl.BucketOwnerEnforced), putReq())
				Expect(err).To(MatchError(op.ErrInvalidBucketACL), "the try's attrs, rgw_op.cc:10279-10299 at v21.1.2")
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(bucket(ctx).Attrs).To(Equal(raced))
			})
		})
	})
})
