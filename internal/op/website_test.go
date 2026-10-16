package op_test

import (
	"context"
	"encoding/json"
	"fmt"
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

// indexWebsite is an index document, an error document and a routing rule,
// as PutBucketWebsite stores them.
func indexWebsite() meta.WebsiteConf {
	return meta.WebsiteConf{
		IndexDocSuffix: "index.html",
		ErrorDoc:       "error.html",
		RoutingRules: meta.RoutingRules{{
			Condition:    meta.RoutingCondition{KeyPrefixEquals: "docs/", HTTPErrorCodeReturnedEquals: 404},
			RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 301}, ReplaceKeyPrefixWith: "documents/"},
		}},
	}
}

// redirectWebsite is a RedirectAllRequestsTo alone.
func redirectWebsite() meta.WebsiteConf {
	return meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "example.org"}}
}

// websiteFrame is conf as RGWBucketWebsiteConf encodes it at version v,
// ENCODE_START(v, 1): version 1 ends before the Swift members.
func websiteFrame(conf meta.WebsiteConf, v uint8) []byte {
	e := denc.NewEncoder()
	fr := e.BeginStruct(v, 1)
	e.String(conf.IndexDocSuffix)
	e.String(conf.ErrorDoc)
	conf.RoutingRules.Encode(e, denc.Squid)
	conf.RedirectAll.Encode(e, denc.Squid)
	if v >= 2 {
		e.String(conf.SubdirMarker)
		e.String(conf.ListingCSSDoc)
		e.Bool(conf.ListingEnabled)
	}
	e.EndStruct(fr)
	return e.Bytes()
}

var _ = Describe("bucket website ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	setWebsite := func(ctx context.Context, conf *meta.WebsiteConf) {
		GinkgoHelper()
		rec := bucket(ctx)
		rec.Info.Website = conf
		Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	teamTag := func() []byte {
		GinkgoHelper()
		var s tags.Set
		Expect(s.Add("team", "a", tags.MaxBucketTags)).To(Succeed())
		return encodeTags(s)
	}
	asBob := func(method string) *op.Request {
		r := f.req(method, "plain", "")
		r.Identity = f.bob
		return r
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetBucketWebsite, rgw_op.h:1057-1059 at v19.2.7", &op.GetBucketWebsite{}, "get_bucket_website", policy.S3GetBucketWebsite, op.OpTypeRead),
		Entry("SetBucketWebsite, rgw_op.h:1076-1078 at v19.2.7", &op.SetBucketWebsite{}, "set_bucket_website", policy.S3PutBucketWebsite, op.OpTypeWrite),
		Entry("DeleteBucketWebsite, a write, rgw_op.h:1090-1092 at v19.2.7", &op.DeleteBucketWebsite{}, "delete_bucket_website", policy.S3DeleteBucketWebsite, op.OpTypeWrite),
	)

	Describe("error order", func() {
		var (
			seen    []string
			buckets *opfakes.FakeBucketStore
			authzr  *opfakes.FakeAuthorizer
		)
		// Each fake records its step in seen and passes the call to the
		// memstore, or allows it.
		BeforeEach(func() {
			seen = nil
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
				seen = append(seen, "load")
				return f.store.GetBucket(ctx, tenant, name)
			}
			buckets.PutBucketInfoStub = func(ctx context.Context, rec *op.BucketRecord) error {
				seen = append(seen, "write")
				return f.store.PutBucketInfo(ctx, rec)
			}
			authzr = &opfakes.FakeAuthorizer{}
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return nil
			}
			f.env.Buckets, f.env.Authz = buckets, authzr
		})

		get := func() op.Op { return &op.GetBucketWebsite{} }
		set := func() op.Op {
			return &op.SetBucketWebsite{Params: func(_ context.Context, o *op.SetBucketWebsite) error {
				seen = append(seen, "params")
				o.Conf = indexWebsite()
				return nil
			}}
		}
		setRefusedParams := func() op.Op {
			return &op.SetBucketWebsite{Params: func(context.Context, *op.SetBucketWebsite) error {
				seen = append(seen, "params")
				return op.ErrMalformedXML
			}}
		}
		del := func() op.Op { return &op.DeleteBucketWebsite{} }

		none := func(*op.Request, op.Op) {}
		missing := func(r *op.Request, _ op.Op) { r.Bucket = "missing" }
		refused := func(*op.Request, op.Op) {
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		writeFails := func(*op.Request, op.Op) {
			buckets.PutBucketInfoStub = func(context.Context, *op.BucketRecord) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}

		DescribeTable("stops at the first step that fails, in radosgw's order",
			func(ctx SpecContext, mk func() op.Op, fail func(*op.Request, op.Op), want error, steps []string) {
				o := mk()
				r := f.req(http.MethodPut, "plain", "")
				fail(r, o)
				Expect(op.Run(ctx, o, r)).To(MatchError(want))
				Expect(seen).To(Equal(steps))
			},
			Entry("GetBucketWebsite on a missing bucket, before any permission check", get, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("GetBucketWebsite for a refused requester", get, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("GetBucketWebsite on a bucket without a website", get, none, op.ErrNoSuchWebsiteConfiguration, []string{"load", "verify"}),
			Entry("SetBucketWebsite on a missing bucket", set, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("SetBucketWebsite for a refused requester, before it reads the body", set, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("SetBucketWebsite whose Params refuses the body, writing nothing", setRefusedParams, none, op.ErrMalformedXML, []string{"load", "verify", "params"}),
			Entry("SetBucketWebsite whose write fails otherwise than by a race, retrying nothing", set, writeFails, op.ErrInternalError, []string{"load", "verify", "params", "write"}),
			Entry("DeleteBucketWebsite on a missing bucket", del, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("DeleteBucketWebsite for a refused requester", del, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("DeleteBucketWebsite whose write fails otherwise than by a race", del, writeFails, op.ErrInternalError, []string{"load", "verify", "write"}),
		)
		DescribeTable("refuses an op mask without the op's bit before it reads the body or writes",
			func(ctx SpecContext, mk func() op.Op) {
				o := mk()
				r := f.req(http.MethodPut, "plain", "")
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).NotTo(ContainElement(BeElementOf("params", "write")))
			},
			Entry("GetBucketWebsite, without read", get),
			Entry("SetBucketWebsite, without write", set),
			Entry("DeleteBucketWebsite, without write", del),
		)
		DescribeTable("authorizes its action against the bucket with the ACL permission op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, a policy.Action, perm acl.Permission, steps []string) {
				setWebsite(ctx, new(indexWebsite()))
				Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(Succeed())
				Expect(seen).To(Equal(steps))
				Expect(authzr.VerifyBucketCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(a))
				Expect(gotPerm).To(Equal(perm))
			},
			Entry("GetBucketWebsite, READ_ACP, rgw_iam_policy.h:282-289 at v19.2.7", get, policy.S3GetBucketWebsite, acl.PermReadACP,
				[]string{"load", "verify"}),
			Entry("SetBucketWebsite, WRITE_ACP, rgw_iam_policy.h:304-311 at v19.2.7", set, policy.S3PutBucketWebsite, acl.PermWriteACP,
				[]string{"load", "verify", "params", "write"}),
			Entry("DeleteBucketWebsite, WRITE_ACP, rgw_iam_policy.h:292-311 at v19.2.7", del, policy.S3DeleteBucketWebsite, acl.PermWriteACP,
				[]string{"load", "verify", "write"}),
		)
		It("serves GetBucketWebsite from the bucket it loaded", func(ctx SpecContext) {
			setWebsite(ctx, new(indexWebsite()))
			o := &op.GetBucketWebsite{}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(o.Conf).To(Equal(indexWebsite()))
			Expect(seen).To(Equal([]string{"load", "verify"}), "it writes nothing")
		})
	})

	DescribeTable("gives a policy the bucket's tags",
		func(ctx SpecContext, mk func() op.Op, name string, untagged error) {
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
				`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":%q,"Resource":"arn:aws:s3:::plain","Condition":{"StringEquals":{"s3:ResourceTag/team":"a"}}}]}`,
				name)})
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag()})
			Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied), "a tagged bucket")
			Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{tags.Attr})).To(Succeed())
			err := op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))
			if untagged == nil {
				Expect(err).NotTo(HaveOccurred(), "an untagged one")
			} else {
				Expect(err).To(MatchError(untagged), "an untagged one")
			}
		},
		Entry("GetBucketWebsite, rgw_op.cc:2864-2866 at v19.2.7", func() op.Op { return &op.GetBucketWebsite{} },
			"s3:GetBucketWebsite", op.ErrNoSuchWebsiteConfiguration),
		Entry("SetBucketWebsite, rgw_op.cc:2889-2891 at v19.2.7", func() op.Op { return &op.SetBucketWebsite{Conf: indexWebsite()} },
			"s3:PutBucketWebsite", nil),
		Entry("DeleteBucketWebsite, rgw_op.cc:2940-2942 at v19.2.7", func() op.Op { return &op.DeleteBucketWebsite{} },
			"s3:DeleteBucketWebsite", nil),
	)

	Describe("GetBucketWebsite", func() {
		It("answers NoSuchWebsiteConfiguration with no message for a bucket without a website", func(ctx SpecContext) {
			err := op.Run(ctx, &op.GetBucketWebsite{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrNoSuchWebsiteConfiguration), "rgw_op.cc:2880-2885 at v19.2.7")
			Expect(op.AsError(err).Message).To(BeEmpty())
		})
		It("takes the stored configuration, its Swift members with it", func(ctx SpecContext) {
			stored := indexWebsite()
			stored.SubdirMarker, stored.ListingCSSDoc, stored.ListingEnabled = "application/directory", "listing.css", true
			setWebsite(ctx, &stored)
			o := &op.GetBucketWebsite{}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(o.Conf).To(Equal(stored))
		})
		It("serves a configuration that is RGWBucketWebsiteConf() under has_website", func(ctx SpecContext) {
			setWebsite(ctx, &meta.WebsiteConf{})
			o := &op.GetBucketWebsite{}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed(), "has_website alone decides, rgw_op.cc:2882 at v19.2.7")
			Expect(o.Conf).To(Equal(meta.WebsiteConf{}))
		})
	})

	Describe("SetBucketWebsite", func() {
		It("stores the configuration with has_website, which GetBucketWebsite returns", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.SetBucketWebsite{Conf: indexWebsite()}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			rec := bucket(ctx)
			Expect(rec.Info.HasWebsite()).To(BeTrue(), "rgw_op.cc:2925 at v19.2.7")
			Expect(rec.Info.Website).To(Equal(new(indexWebsite())))
			g := &op.GetBucketWebsite{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Conf).To(Equal(indexWebsite()))
		})
		It("runs Params once the requester is authorized, and stores what it parsed", func(ctx SpecContext) {
			calls := 0
			params := func(_ context.Context, o *op.SetBucketWebsite) error {
				calls++
				o.Conf = redirectWebsite()
				return nil
			}
			Expect(op.Run(ctx, &op.SetBucketWebsite{Params: params}, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero(), "get_params runs in execute, after verify_permission, rgw_op.cc:2907 at v19.2.7")
			Expect(op.Run(ctx, &op.SetBucketWebsite{Params: params}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(calls).To(Equal(1))
			Expect(bucket(ctx).Info.Website).To(Equal(new(redirectWebsite())))
		})
		It("returns Params' error and stores nothing", func(ctx SpecContext) {
			params := func(context.Context, *op.SetBucketWebsite) error { return op.ErrInvalidArgument }
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.SetBucketWebsite{Params: params}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInvalidArgument))
			Expect(bucket(ctx)).To(Equal(before))
		})
		It("replaces a stored configuration whole, its Swift members with it", func(ctx SpecContext) {
			swift := indexWebsite()
			swift.SubdirMarker, swift.ListingCSSDoc, swift.ListingEnabled = "application/directory", "listing.css", true
			setWebsite(ctx, &swift)
			Expect(op.Run(ctx, &op.SetBucketWebsite{Conf: redirectWebsite()}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Info.Website).To(Equal(new(redirectWebsite())), "website_conf = website_conf, rgw_op.cc:2926 at v19.2.7")
		})
		It("rewrites the instance with the rest of it and the bucket's attrs as they were", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag()})
			f.setBucketFlags(ctx, meta.BucketVersioned)
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.SetBucketWebsite{Conf: indexWebsite()}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			after := bucket(ctx)
			Expect(after.Version.Ver).To(Equal(before.Version.Ver + 1))
			Expect(after.Attrs).To(Equal(before.Attrs))
			after.Info.Website = nil
			Expect(after.Info).To(Equal(before.Info))
		})
		It("reads a stored version 1 configuration for GetBucketWebsite, and Set writes it back as version 2", func(ctx SpecContext) {
			d := denc.NewDecoder(websiteFrame(indexWebsite(), 1))
			v1 := meta.DecodeWebsiteConf(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			setWebsite(ctx, &v1)
			g := &op.GetBucketWebsite{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Conf).To(Equal(indexWebsite()), "version 1 holds no Swift members, rgw_website.h:211-215 at v19.2.7")
			Expect(op.Run(ctx, &op.SetBucketWebsite{Conf: g.Conf}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			e := denc.NewEncoder()
			bucket(ctx).Info.Website.Encode(e, denc.Squid)
			Expect(e.Bytes()).To(Equal(websiteFrame(indexWebsite(), 2)), "ENCODE_START(2, 1), rgw_website.h:194-204 at v19.2.7")
		})
	})

	Describe("DeleteBucketWebsite", func() {
		It("clears has_website and the configuration, after which GetBucketWebsite finds none", func(ctx SpecContext) {
			setWebsite(ctx, new(indexWebsite()))
			Expect(op.Run(ctx, &op.DeleteBucketWebsite{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			rec := bucket(ctx)
			Expect(rec.Info.HasWebsite()).To(BeFalse(), "rgw_op.cc:2971-2972 at v19.2.7")
			Expect(rec.Info.Website).To(BeNil())
			Expect(op.Run(ctx, &op.GetBucketWebsite{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoSuchWebsiteConfiguration))
		})
		It("succeeds on a bucket without a website, rewriting the instance as radosgw does", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.DeleteBucketWebsite{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			after := bucket(ctx)
			Expect(after.Version.Ver).To(Equal(before.Version.Ver+1), "no has_website check, rgw_op.cc:2956-2981 at v19.2.7")
			Expect(after.Info).To(Equal(before.Info))
			Expect(after.Attrs).To(Equal(before.Attrs))
		})
	})

	Describe("a website write that loses a race", func() {
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketInfoReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			calls := 0
			set := &op.SetBucketWebsite{Params: func(_ context.Context, o *op.SetBucketWebsite) error {
				calls++
				o.Conf = indexWebsite()
				return nil
			}}
			Expect(op.Run(ctx, set, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(16), "rgw_op.h:189-195 at v19.2.7")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			Expect(calls).To(Equal(1), "the body is read once")
			_, last := buckets.PutBucketInfoArgsForCall(15)
			Expect(last.Info.Website).To(Equal(new(indexWebsite())))
			Expect(op.Run(ctx, &op.DeleteBucketWebsite{}, f.req(http.MethodDelete, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(32))
		})

		Describe("to another write of the bucket", func() {
			// bobCanWriteACP is alice's ACL with WRITE_ACP, the permission
			// both website writes map to, granted to bob.
			bobCanWriteACP := func() []byte {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				return encodedPolicy(p)
			}
			revokeByACL := func(ctx context.Context) {
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: f.aliceACL})
			}
			denyBobOn := func(actions ...string) func(ctx context.Context) {
				return func(ctx context.Context) {
					names, err := json.Marshal(actions)
					Expect(err).NotTo(HaveOccurred())
					f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
						`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":%s,"Resource":"arn:aws:s3:::plain"}]}`,
						names)})
				}
			}
			// tagAndVersion is another request's change of both the attrs and
			// the instance: a tag set and the versioning flag.
			tagAndVersion := func(ctx context.Context) {
				f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag()})
				f.setBucketFlags(ctx, meta.BucketVersioned)
			}

			var (
				buckets *opfakes.FakeBucketStore
				// raced is the bucket as the write the first try lost to
				// left it.
				raced *op.BucketRecord
			)
			// race gives plain a website and bob WRITE_ACP, then makes the
			// first instance write lose to concurrent, another request's
			// change of the bucket, and lets every retry through to the
			// store.
			race := func(ctx context.Context, concurrent func(ctx context.Context)) {
				GinkgoHelper()
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: bobCanWriteACP()})
				setWebsite(ctx, new(redirectWebsite()))
				buckets = &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = f.store.GetBucket
				buckets.PutBucketInfoStub = func(ctx context.Context, rec *op.BucketRecord) error {
					if buckets.PutBucketInfoCallCount() == 1 {
						concurrent(ctx)
						raced = bucket(ctx)
					}
					return f.store.PutBucketInfo(ctx, rec)
				}
				f.env.Buckets = buckets
			}
			set := func() op.Op { return &op.SetBucketWebsite{Conf: indexWebsite()} }
			del := func() op.Op { return &op.DeleteBucketWebsite{} }

			DescribeTable("writes over the bucket as the retry read it, keeping the other write's change",
				func(ctx SpecContext, mk func() op.Op, want *meta.WebsiteConf) {
					race(ctx, tagAndVersion)
					Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(Succeed())
					Expect(buckets.PutBucketInfoCallCount()).To(Equal(2), "the first try lost, the retry wrote")
					rec := bucket(ctx)
					Expect(rec.Info.Website).To(Equal(want))
					Expect(rec.Attrs).To(HaveKeyWithValue(tags.Attr, teamTag()), "put_info writes the attrs try_refresh_info read, rgw_sal_rados.cc:753-757 and :780-783 at v19.2.7")
					Expect(rec.Info.Flags&meta.BucketVersioned).To(Equal(uint32(meta.BucketVersioned)), "the other write's instance change")
				},
				Entry("SetBucketWebsite", set, new(indexWebsite())),
				Entry("DeleteBucketWebsite", del, (*meta.WebsiteConf)(nil)),
			)
			DescribeTable("refuses the retry once the write it lost to revoked the requester's permission, leaving that write's bucket",
				func(ctx SpecContext, mk func() op.Op, revoke func(ctx context.Context)) {
					race(ctx, revoke)
					Expect(op.Run(ctx, mk(), asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "the retry writes nothing")
					Expect(bucket(ctx)).To(Equal(raced))
				},
				Entry("SetBucketWebsite under an ACL that drops the grant", set, revokeByACL),
				Entry("SetBucketWebsite under a policy that denies s3:PutBucketWebsite alone", set, denyBobOn("s3:PutBucketWebsite")),
				Entry("DeleteBucketWebsite under an ACL that drops the grant", del, revokeByACL),
				Entry("DeleteBucketWebsite under a policy that denies s3:DeleteBucketWebsite alone", del, denyBobOn("s3:DeleteBucketWebsite")),
			)
			DescribeTable("writes on a retry whose requester still holds the permission",
				func(ctx SpecContext, mk func() op.Op, want *meta.WebsiteConf) {
					race(ctx, denyBobOn("s3:PutBucketAcl", "s3:PutBucketPolicy", "s3:PutBucketTagging", "s3:GetBucketWebsite"))
					Expect(op.Run(ctx, mk(), asBob(http.MethodPut))).To(Succeed())
					Expect(buckets.PutBucketInfoCallCount()).To(Equal(2))
					Expect(bucket(ctx).Info.Website).To(Equal(want))
				},
				Entry("SetBucketWebsite", set, new(indexWebsite())),
				Entry("DeleteBucketWebsite", del, (*meta.WebsiteConf)(nil)),
			)
			It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
				race(ctx, revokeByACL)
				r := asBob(http.MethodPut)
				r.Identity.Admin = true
				Expect(op.Run(ctx, set(), r)).To(Succeed())
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(2))
				Expect(bucket(ctx).Info.Website).To(Equal(new(indexWebsite())))
			})
		})
	})
})
