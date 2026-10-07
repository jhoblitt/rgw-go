package op_test

import (
	"context"
	"encoding/json"
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

const allowAll = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::plain/*"}]}`

// removeSelfAccess is Tentacle's RGW_ATTR_IAM_POLICY_REMOVE_SELF_ACCESS
// (rgw_common.h:177 at v20.2.4).
const removeSelfAccess = meta.AttrPrefix + "iam-policy-remove-self-access"

var _ = Describe("bucket subresource ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	asBob := func(method string) *op.Request {
		r := f.req(method, "plain", "")
		r.Identity = f.bob
		return r
	}
	tagSet := func(kv ...string) tags.Set {
		GinkgoHelper()
		var s tags.Set
		for i := 0; i+1 < len(kv); i += 2 {
			Expect(s.Add(kv[i], kv[i+1], tags.MaxBucketTags)).To(Succeed())
		}
		return s
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetBucketACL", &op.GetBucketACL{}, "get_acls", policy.S3GetBucketAcl, op.OpTypeRead),
		Entry("PutBucketACL", &op.PutBucketACL{}, "put_acls", policy.S3PutBucketAcl, op.OpTypeWrite),
		Entry("GetBucketPolicy", &op.GetBucketPolicy{}, "get_bucket_policy", policy.S3GetBucketPolicy, op.OpTypeRead),
		Entry("PutBucketPolicy", &op.PutBucketPolicy{}, "put_bucket_policy", policy.S3PutBucketPolicy, op.OpTypeWrite),
		Entry("DeleteBucketPolicy, a write", &op.DeleteBucketPolicy{}, "delete_bucket_policy", policy.S3DeleteBucketPolicy, op.OpTypeWrite),
		Entry("GetBucketTagging", &op.GetBucketTagging{}, "get_bucket_tags", policy.S3GetBucketTagging, op.OpTypeRead),
		Entry("PutBucketTagging", &op.PutBucketTagging{}, "put_bucket_tags", policy.S3PutBucketTagging, op.OpTypeWrite),
		Entry("DeleteBucketTagging, a delete authorized as s3:PutBucketTagging", &op.DeleteBucketTagging{}, "delete_bucket_tags", policy.S3PutBucketTagging, op.OpTypeDelete),
	)

	DescribeTable("answers NoSuchBucket before any permission check",
		func(ctx SpecContext, o op.Op) {
			r := f.req(http.MethodGet, "missing", "")
			r.Identity = f.bob
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrNoSuchBucket))
		},
		Entry("GetBucketACL", &op.GetBucketACL{}),
		Entry("PutBucketACL", &op.PutBucketACL{}),
		Entry("GetBucketPolicy", &op.GetBucketPolicy{}),
		Entry("PutBucketPolicy", &op.PutBucketPolicy{}),
		Entry("DeleteBucketPolicy", &op.DeleteBucketPolicy{}),
		Entry("GetBucketTagging", &op.GetBucketTagging{}),
		Entry("PutBucketTagging", &op.PutBucketTagging{}),
		Entry("DeleteBucketTagging", &op.DeleteBucketTagging{}),
	)

	Describe("GetBucketACL", func() {
		It("takes the stored ACL, and for a bucket without one its owner's default with no display name", func(ctx SpecContext) {
			o := &op.GetBucketACL{}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(o.Policy).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")))
			Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{meta.AttrACL})).To(Succeed())
			o = &op.GetBucketACL{}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(string(o.Policy.MarshalS3XML())).To(ContainSubstring(`<Owner><ID>alice</ID></Owner>`),
				`create_default(owner, ""), rgw_op.cc:283 at v19.2.6: no DisplayName`)
		})
		It("refuses a requester the authorizer refuses", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.GetBucketACL{}, asBob(http.MethodGet))).To(MatchError(op.ErrAccessDenied))
		})
	})

	Describe("PutBucketACL", func() {
		publicRead := func() acl.Policy {
			p := acl.DefaultPolicy(f.alice.Owner, "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermRead})
			return p
		}
		storedACL := func(ctx context.Context) acl.Policy {
			GinkgoHelper()
			p, err := op.BucketACLFor(bucket(ctx))
			Expect(err).NotTo(HaveOccurred())
			return p
		}

		It("builds from the stored ACL and stores the policy Build returns", func(ctx SpecContext) {
			var existing acl.Policy
			o := &op.PutBucketACL{Build: func(p acl.Policy) (acl.Policy, error) {
				existing = p
				return publicRead(), nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(existing).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")), "s->bucket_acl, rgw_op.cc:5823-5824")
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(publicRead())))
		})
		It("calls Build only once the requester is authorized", func(ctx SpecContext) {
			built := false
			o := &op.PutBucketACL{Build: func(p acl.Policy) (acl.Policy, error) { built = true; return p, nil }}
			Expect(op.Run(ctx, o, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
			Expect(built).To(BeFalse(), "get_params runs in execute, after verify_permission")
		})
		It("returns Build's error and stores nothing", func(ctx SpecContext) {
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) { return acl.Policy{}, op.ErrMalformedXML }}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrMalformedXML))
			Expect(storedACL(ctx)).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")))
		})
		It("refuses to change the ACL owner", func(ctx SpecContext) {
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) {
				return acl.DefaultPolicy(f.bob.Owner, "Bob"), nil
			}}
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(op.AsError(err).Message).To(Equal("Cannot modify ACL Owner"), "rgw_op.cc:5859-5864 at v19.2.6")
			Expect(storedACL(ctx).Owner.ID).To(Equal("alice"))
		})
		It("refuses a public ACL when the bucket blocks public ACLs, or holds a block that does not decode", func(ctx SpecContext) {
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) { return publicRead(), nil }}
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied), "rgw_op.cc:5905-5910 at v19.2.6")
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: {0xff}})
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied))
			Expect(storedACL(ctx)).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")))
		})
		It("retries a write that loses a race and answers ConcurrentModification after the last, where radosgw answers success", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketReturns(bucket(ctx), nil)
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			builds := 0
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) { builds++; return publicRead(), nil }}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification),
				"rgw_op.cc:5925-5927 at v19.2.6 answers 0 with nothing stored")
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16))
			Expect(builds).To(Equal(1), "the body is read once")
			_, _, set, rm := buckets.PutBucketAttrsArgsForCall(15)
			Expect(set).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(publicRead())))
			Expect(rm).To(BeEmpty())
		})
		It("writes the ACL over the bucket as a lost race left it, keeping what the other write set", func(ctx SpecContext) {
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) {
				// Another request changes the bucket after this one loaded it.
				f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(tagSet("k", "v"))})
				return publicRead(), nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(publicRead())))
			Expect(bucket(ctx).Attrs).To(HaveKey(tags.Attr))
		})
		It("refuses a retry once a concurrent change has given the ACL another owner, and keeps that ACL", func(ctx SpecContext) {
			bobs := encodedPolicy(acl.DefaultPolicy(f.bob.Owner, "Bob"))
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) {
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: bobs})
				return publicRead(), nil
			}}
			err := op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(op.AsError(err).Message).To(Equal("Cannot modify ACL Owner"))
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(meta.AttrACL, bobs), "the retry never restores alice's ACL")
		})
		It("refuses a public ACL on a retry once a concurrent change has blocked public ACLs", func(ctx SpecContext) {
			o := &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
				return publicRead(), nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied))
			Expect(storedACL(ctx)).To(Equal(acl.DefaultPolicy(f.alice.Owner, "Alice")))
		})
	})

	Describe("bucket policy", func() {
		parsed := func(text string) *policy.Policy {
			GinkgoHelper()
			p, err := policy.Parse(text, policy.ParseOptions{Release: denc.Squid})
			Expect(err).NotTo(HaveOccurred())
			return p
		}

		It("is NoSuchBucketPolicy with radosgw's message for a bucket without one or with an empty one", func(ctx SpecContext) {
			err := op.Run(ctx, &op.GetBucketPolicy{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrNoSuchBucketPolicy))
			Expect(op.AsError(err).Message).To(Equal("The bucket policy does not exist"), "rgw_op.cc:8146-8167 at v19.2.6")
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: {}})
			err = op.Run(ctx, &op.GetBucketPolicy{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrNoSuchBucketPolicy))
			Expect(op.AsError(err).Message).To(Equal("The bucket policy does not exist"))
		})
		It("stores the policy's text verbatim, returns it and deletes it", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.PutBucketPolicy{Policy: parsed(allowAll)}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(allowAll)), "rgw_op.cc:8111-8112 at v19.2.6")
			g := &op.GetBucketPolicy{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(string(g.JSON)).To(Equal(allowAll))
			Expect(op.Run(ctx, &op.DeleteBucketPolicy{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
			Expect(op.Run(ctx, &op.GetBucketPolicy{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoSuchBucketPolicy))
		})
		It("runs Params once the requester is authorized, and stores what it parsed", func(ctx SpecContext) {
			calls := 0
			params := func(_ context.Context, o *op.PutBucketPolicy) error {
				calls++
				o.Policy = parsed(allowAll)
				return nil
			}
			Expect(op.Run(ctx, &op.PutBucketPolicy{Params: params}, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero(), "get_params runs in execute, after verify_permission")
			Expect(op.Run(ctx, &op.PutBucketPolicy{Params: params}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(calls).To(Equal(1))
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(allowAll)))
		})
		It("answers InternalError, storing nothing, when neither Params nor Policy gives a policy", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.PutBucketPolicy{}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInternalError))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses a public policy on a retry once a concurrent change has blocked public policies", func(ctx SpecContext) {
			o := &op.PutBucketPolicy{Params: func(ctx context.Context, o *op.PutBucketPolicy) error {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicPolicy: true})})
				o.Policy = parsed(allowAll)
				return nil
			}}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: {0xff}})
			Expect(op.Run(ctx, &op.PutBucketPolicy{Policy: parsed(allowAll)}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied),
				"a block that does not decode blocks")
		})
		It("returns Params' error and stores nothing", func(ctx SpecContext) {
			params := func(context.Context, *op.PutBucketPolicy) error { return op.ErrInvalidArgument }
			Expect(op.Run(ctx, &op.PutBucketPolicy{Params: params}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInvalidArgument))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("refuses the policy ops to a requester the authorizer refuses", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.GetBucketPolicy{}, asBob(http.MethodGet))).To(MatchError(op.ErrAccessDenied))
			Expect(op.Run(ctx, &op.DeleteBucketPolicy{}, asBob(http.MethodDelete))).To(MatchError(op.ErrAccessDenied))
		})
		It("removes Tentacle's remove-self-access flag with the policy on Tentacle alone", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(allowAll), removeSelfAccess: []byte("x")})
			Expect(op.Run(ctx, &op.DeleteBucketPolicy{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKey(removeSelfAccess), "Squid knows no such attr")

			f = newWriteFixture(ctx, denc.Tentacle)
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(allowAll), removeSelfAccess: []byte("x")})
			Expect(op.Run(ctx, &op.DeleteBucketPolicy{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(removeSelfAccess), "rgw_op.cc:9151-9157 at v20.2.4")
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrIAMPolicy))
		})
		It("writes the policy over the bucket as a lost race left it, keeping what the other write set", func(ctx SpecContext) {
			r := f.req(http.MethodPut, "plain", "")
			o := &op.PutBucketPolicy{Params: func(ctx context.Context, o *op.PutBucketPolicy) error {
				// Another request changes the bucket after this one loaded it.
				f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(tagSet("k", "v"))})
				o.Policy = parsed(allowAll)
				return nil
			}}
			Expect(op.Run(ctx, o, r)).To(Succeed())
			attrs := bucket(ctx).Attrs
			Expect(attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(allowAll)))
			Expect(attrs).To(HaveKey(tags.Attr), "radosgw merges the request-start attrs back on the retry, rgw_op.cc:8102 and :8110-8115 at v19.2.6")
		})
	})

	Describe("bucket tagging", func() {
		It("is NoSuchTagSet without a tag attr, then stores, returns and deletes the set", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.GetBucketTagging{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoSuchTagSet), "rgw_op.cc:1161-1167 at v19.2.6")
			Expect(op.Run(ctx, &op.PutBucketTagging{Set: tagSet("k", "v")}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(tagSet("k", "v"))))
			g := &op.GetBucketTagging{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Set.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
			Expect(op.Run(ctx, &op.DeleteBucketTagging{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(tags.Attr))
			Expect(op.Run(ctx, &op.GetBucketTagging{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoSuchTagSet))
		})
		It("stores an empty set, which a GET then returns", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.PutBucketTagging{}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(tags.Set{})), "an empty set encodes, rgw_rest_s3.cc:898-903 at v19.2.6")
			g := &op.GetBucketTagging{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Set.Tags).To(BeEmpty())
		})
		It("answers UnknownError for a tag attr that does not decode", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: {0}})
			Expect(op.Run(ctx, &op.GetBucketTagging{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrUnknown))
		})
		It("runs Params once the requester is authorized, and returns its error", func(ctx SpecContext) {
			calls := 0
			params := func(context.Context, *op.PutBucketTagging) error { calls++; return op.ErrInvalidTag }
			Expect(op.Run(ctx, &op.PutBucketTagging{Params: params}, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero())
			Expect(op.Run(ctx, &op.PutBucketTagging{Params: params}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInvalidTag))
			Expect(calls).To(Equal(1))
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(tags.Attr))
		})
		It("refuses the tagging ops to a requester the authorizer refuses", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.GetBucketTagging{}, asBob(http.MethodGet))).To(MatchError(op.ErrAccessDenied))
			Expect(op.Run(ctx, &op.DeleteBucketTagging{}, asBob(http.MethodDelete))).To(MatchError(op.ErrAccessDenied))
		})
	})

	Describe("RetryRacedBucketWrite", func() {
		var buckets *opfakes.FakeBucketStore
		BeforeEach(func(ctx SpecContext) {
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketReturns(bucket(ctx), nil)
			f.env.Buckets = buckets
		})
		request := func(ctx context.Context) *op.Request {
			GinkgoHelper()
			r := f.req(http.MethodPut, "plain", "")
			r.BucketRec = bucket(ctx)
			return r
		}

		It("retries a raced write fifteen times, reading the bucket again before each, then gives up", func(ctx SpecContext) {
			calls := 0
			err := op.RetryRacedBucketWrite(ctx, request(ctx), func() error { calls++; return op.ErrConcurrentModification })
			Expect(err).To(MatchError(op.ErrConcurrentModification))
			Expect(calls).To(Equal(16), "rgw_op.h:188-195 at v19.2.6")
			Expect(buckets.GetBucketCallCount()).To(Equal(15))
			_, tenant, name := buckets.GetBucketArgsForCall(0)
			Expect([]string{tenant, name}).To(Equal([]string{"", "plain"}), "try_refresh_info reads by name, rgw_rados.cc:9016-9017 at v19.2.6")
		})
		It("writes into the bucket it read again", func(ctx SpecContext) {
			r := request(ctx)
			fresh := bucket(ctx)
			fresh.Version.Ver++
			buckets.GetBucketReturns(fresh, nil)
			var seen []*op.BucketRecord
			err := op.RetryRacedBucketWrite(ctx, r, func() error {
				seen = append(seen, r.BucketRec)
				if len(seen) == 1 {
					return op.ErrConcurrentModification
				}
				return nil
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(seen).To(HaveLen(2))
			Expect(seen[1]).To(BeIdenticalTo(fresh))
		})
		It("passes on any other error without a retry", func(ctx SpecContext) {
			calls := 0
			err := op.RetryRacedBucketWrite(ctx, request(ctx), func() error { calls++; return op.ErrInternalError })
			Expect(err).To(MatchError(op.ErrInternalError))
			Expect(calls).To(Equal(1))
			Expect(buckets.GetBucketCallCount()).To(BeZero())
		})
		It("answers a bucket removed during the write with radosgw's -ENOENT, NoSuchKey", func(ctx SpecContext) {
			buckets.GetBucketReturns(nil, op.ErrNoSuchBucket)
			calls := 0
			err := op.RetryRacedBucketWrite(ctx, request(ctx), func() error { calls++; return op.ErrConcurrentModification })
			Expect(err).To(MatchError(op.ErrNoSuchKey), "try_refresh_info's -ENOENT, rgw_common.cc:97 at v19.2.6, :98 at v20.2.4")
			Expect(err).NotTo(MatchError(op.ErrNoSuchBucket))
			Expect(calls).To(Equal(1))
		})
		It("returns the error of a read that fails otherwise", func(ctx SpecContext) {
			buckets.GetBucketReturns(nil, op.ErrInternalError)
			err := op.RetryRacedBucketWrite(ctx, request(ctx), func() error { return op.ErrConcurrentModification })
			Expect(err).To(MatchError(op.ErrInternalError))
		})
		It("stops without writing when the name now names another bucket", func(ctx SpecContext) {
			other := bucket(ctx)
			other.Info.Bucket.ID = "another-instance"
			buckets.GetBucketReturns(other, nil)
			r := request(ctx)
			calls := 0
			err := op.RetryRacedBucketWrite(ctx, r, func() error { calls++; return op.ErrConcurrentModification })
			Expect(err).To(MatchError(op.ErrConcurrentModification))
			Expect(calls).To(Equal(1), "radosgw writes into the bucket re-created under the name")
			Expect(r.BucketRec.Info.Bucket.ID).NotTo(Equal("another-instance"))
		})
		It("retries the ACL, tagging and policy writes", func(ctx SpecContext) {
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			ops := []op.Op{
				&op.PutBucketACL{Build: func(p acl.Policy) (acl.Policy, error) { return p, nil }},
				&op.PutBucketTagging{}, &op.DeleteBucketTagging{},
				&op.PutBucketPolicy{Policy: &policy.Policy{Text: allowAll}}, &op.DeleteBucketPolicy{},
			}
			for i, o := range ops {
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification), o.Name())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16*(i+1)), o.Name())
			}
		})
	})

	Describe("a retried write, re-authorized against the bucket each retry read", func() {
		// bobCanWriteACP is alice's ACL with WRITE_ACP, the permission every
		// retried bucket write maps to, granted to bob.
		bobCanWriteACP := func() acl.Policy {
			p := acl.DefaultPolicy(f.alice.Owner, "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
			return p
		}
		// publicWriteACP is alice's ACL with WRITE_ACP granted to AllUsers,
		// a grant IgnorePublicAcls takes away.
		publicWriteACP := func() acl.Policy {
			p := acl.DefaultPolicy(f.alice.Owner, "Alice")
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermWriteACP})
			return p
		}
		// bobReads is a policy that decides none of bob's bucket writes.
		const bobReads = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":"s3:GetObject","Resource":"arn:aws:s3:::plain/*"}]}`
		revokeByACL := func() map[string][]byte {
			return map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(f.alice.Owner, "Alice"))}
		}
		// denyBobOn is a concurrent change to a policy that denies bob the
		// actions alone.
		denyBobOn := func(actions ...string) func() map[string][]byte {
			return func() map[string][]byte {
				names, err := json.Marshal(actions)
				Expect(err).NotTo(HaveOccurred())
				return map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
					`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":%s,"Resource":"arn:aws:s3:::plain"}]}`,
					names)}
			}
		}
		ignorePublicACLs := func() map[string][]byte {
			return map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{IgnorePublicACLs: true})}
		}
		keepGrant := func() map[string][]byte {
			p := bobCanWriteACP()
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermRead})
			return map[string][]byte{meta.AttrACL: encodedPolicy(p)}
		}
		wantedACL := func() acl.Policy {
			p := bobCanWriteACP()
			p.ACL.AddGrant(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermRead})
			return p
		}

		var (
			buckets *opfakes.FakeBucketStore
			// raced is the bucket's attrs as the write the first try lost to
			// left them.
			raced map[string][]byte
		)
		// raceFrom gives plain the ACL start, a policy and a tag set for the
		// deletes to remove, then makes the first write of the bucket lose to
		// concurrent, another request's change of the bucket's attrs, and lets
		// every retry through to the store.
		raceFrom := func(ctx context.Context, start acl.Policy, concurrent map[string][]byte) {
			GinkgoHelper()
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			f.setBucketAttrs(ctx, map[string][]byte{
				meta.AttrACL: encodedPolicy(start), op.AttrIAMPolicy: []byte(bobReads), tags.Attr: encodeTags(tagSet("k", "v")),
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
		// race is raceFrom with WRITE_ACP granted to bob.
		race := func(ctx context.Context, concurrent map[string][]byte) {
			GinkgoHelper()
			raceFrom(ctx, bobCanWriteACP(), concurrent)
		}
		// written is an op's write as bob asks for it, and what it leaves in
		// the bucket's attrs.
		type written struct {
			op    op.Op
			check func(attrs map[string][]byte)
		}
		putACL := func() written {
			return written{
				op: &op.PutBucketACL{Build: func(acl.Policy) (acl.Policy, error) { return wantedACL(), nil }},
				check: func(attrs map[string][]byte) {
					Expect(attrs).To(HaveKeyWithValue(meta.AttrACL, encodedPolicy(wantedACL())))
				},
			}
		}
		putPolicy := func() written {
			return written{
				op: &op.PutBucketPolicy{Policy: &policy.Policy{Text: allowAll}},
				check: func(attrs map[string][]byte) {
					Expect(attrs).To(HaveKeyWithValue(op.AttrIAMPolicy, []byte(allowAll)))
				},
			}
		}
		deletePolicy := func() written {
			return written{
				op:    &op.DeleteBucketPolicy{},
				check: func(attrs map[string][]byte) { Expect(attrs).NotTo(HaveKey(op.AttrIAMPolicy)) },
			}
		}
		putTagging := func() written {
			return written{
				op: &op.PutBucketTagging{Set: tagSet("k2", "v2")},
				check: func(attrs map[string][]byte) {
					Expect(attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(tagSet("k2", "v2"))))
				},
			}
		}
		deleteTagging := func() written {
			return written{
				op:    &op.DeleteBucketTagging{},
				check: func(attrs map[string][]byte) { Expect(attrs).NotTo(HaveKey(tags.Attr)) },
			}
		}

		DescribeTable("refuses the retry once the write it lost to revoked the requester's permission, leaving the revoking bucket",
			func(ctx SpecContext, write func() written, revoke func() map[string][]byte) {
				race(ctx, revoke())
				o := write().op
				Expect(op.Run(ctx, o, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied), o.Name())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(bucket(ctx).Attrs).To(Equal(raced))
			},
			Entry("PutBucketACL under an ACL that drops the grant", putACL, revokeByACL),
			Entry("PutBucketACL under a policy that denies s3:PutBucketAcl alone", putACL, denyBobOn("s3:PutBucketAcl")),
			Entry("PutBucketPolicy under an ACL that drops the grant", putPolicy, revokeByACL),
			Entry("PutBucketPolicy under a policy that denies s3:PutBucketPolicy alone, which it would replace",
				putPolicy, denyBobOn("s3:PutBucketPolicy")),
			Entry("DeleteBucketPolicy under an ACL that drops the grant", deletePolicy, revokeByACL),
			Entry("DeleteBucketPolicy under a policy that denies s3:DeleteBucketPolicy alone, which it would remove",
				deletePolicy, denyBobOn("s3:DeleteBucketPolicy")),
			Entry("PutBucketTagging under an ACL that drops the grant", putTagging, revokeByACL),
			Entry("PutBucketTagging under a policy that denies s3:PutBucketTagging alone", putTagging, denyBobOn("s3:PutBucketTagging")),
			Entry("DeleteBucketTagging under an ACL that drops the grant", deleteTagging, revokeByACL),
			Entry("DeleteBucketTagging under a policy that denies s3:PutBucketTagging alone, the action it authorizes",
				deleteTagging, denyBobOn("s3:PutBucketTagging")),
		)
		DescribeTable("refuses the retry once the write it lost to set IgnorePublicAcls, leaving the bucket that block left",
			func(ctx SpecContext, write func() written) {
				raceFrom(ctx, publicWriteACP(), ignorePublicACLs())
				o := write().op
				Expect(op.Run(ctx, o, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied), o.Name())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(bucket(ctx).Attrs).To(Equal(raced))
			},
			Entry("PutBucketACL, which the block of public ACLs it re-checks would not refuse", putACL),
			Entry("PutBucketPolicy, which the block of public policies it re-checks would not refuse", putPolicy),
		)
		DescribeTable("writes on a retry whose requester still holds the permission",
			func(ctx SpecContext, write func() written, concurrent func() map[string][]byte) {
				race(ctx, concurrent())
				w := write()
				o := w.op
				Expect(op.Run(ctx, o, asBob(http.MethodPut))).To(Succeed(), o.Name())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2), "the first try lost, the retry wrote")
				w.check(bucket(ctx).Attrs)
			},
			Entry("PutBucketACL under an ACL that keeps the grant", putACL, keepGrant),
			Entry("PutBucketPolicy under an ACL that keeps the grant", putPolicy, keepGrant),
			Entry("DeleteBucketPolicy under an ACL that keeps the grant", deletePolicy, keepGrant),
			Entry("PutBucketTagging under an ACL that keeps the grant", putTagging, keepGrant),
			Entry("DeleteBucketTagging under an ACL that keeps the grant", deleteTagging, keepGrant),
			Entry("PutBucketACL under a policy that denies every other retried action", putACL,
				denyBobOn("s3:PutBucketPolicy", "s3:DeleteBucketPolicy", "s3:PutBucketTagging")),
			Entry("PutBucketPolicy under a policy that denies every other retried action", putPolicy,
				denyBobOn("s3:PutBucketAcl", "s3:DeleteBucketPolicy", "s3:PutBucketTagging")),
			Entry("DeleteBucketPolicy under a policy that denies every other retried action", deletePolicy,
				denyBobOn("s3:PutBucketAcl", "s3:PutBucketPolicy", "s3:PutBucketTagging")),
			Entry("PutBucketTagging under a policy that denies every other retried action", putTagging,
				denyBobOn("s3:PutBucketAcl", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy")),
			Entry("DeleteBucketTagging under a policy that denies every action but s3:PutBucketTagging", deleteTagging,
				denyBobOn("s3:PutBucketAcl", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy")),
		)
		It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
			race(ctx, revokeByACL())
			r := asBob(http.MethodPut)
			r.Identity.Admin = true
			w := deleteTagging()
			Expect(op.Run(ctx, w.op, r)).To(Succeed())
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
			w.check(bucket(ctx).Attrs)
		})
	})
})

// encodeTags is set as RGWObjTags::encode writes it.
func encodeTags(set tags.Set) []byte {
	e := denc.NewEncoder()
	set.Encode(e, denc.Squid)
	return e.Bytes()
}
