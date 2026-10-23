package op_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// appCORS is three rules: https://app.example may GET and PUT with x-amz-*
// headers, any origin may HEAD, and https://app.example may DELETE.
func appCORS() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{
			ID: "app", MaxAge: 600, Methods: cors.MethodGet | cors.MethodPut,
			AllowedOrigins: []string{"https://app.example"}, AllowedHeaders: []string{"x-amz-*"}, ExposeHeaders: []string{"ETag"},
		},
		{ID: "anyone", MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodHead, AllowedOrigins: []string{"*"}},
		{ID: "app-delete", MaxAge: 60, Methods: cors.MethodDelete, AllowedOrigins: []string{"https://app.example"}},
	}}
}

// otherCORS is one rule: https://other.example may GET.
func otherCORS() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{ID: "other", MaxAge: 30, Methods: cors.MethodGet, AllowedOrigins: []string{"https://other.example"}},
	}}
}

// decodedCORS is c as GetBucketCORS reads it back from its stored form.
func decodedCORS(c *cors.Configuration) *cors.Configuration {
	GinkgoHelper()
	d, err := cors.Decode(c.Encode())
	Expect(err).NotTo(HaveOccurred())
	return d
}

// withHeaders adds each name and value pair of kv to r's headers: a header
// kv does not name is absent, and one it names twice is repeated.
func withHeaders(r *op.Request, kv ...string) *op.Request {
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Add(kv[i], kv[i+1])
	}
	return r
}

// forgedPreflight is the identity the verifier grants a preflight on a key
// of u, whose account is acct when it has one, on the key's id alone:
// internal/auth/identity.go's fields, marked Unverified.
func forgedPreflight(u meta.UserInfo, acct *meta.AccountInfo) op.Identity {
	id := op.Identity{
		User: &u, Owner: meta.UserOwner(u.UserID), Account: acct, Tenant: u.UserID.Tenant,
		AccessKey: "AK" + strings.ToUpper(u.UserID.ID), OpMask: u.OpMask,
		Admin: u.Admin != 0 || u.System != 0, System: u.System != 0, Unverified: true,
	}
	if acct != nil {
		id.Owner = meta.AccountOwner(acct.ID)
	}
	return id
}

// preflightAnswer is everything OptionsCORS's caller sees of one preflight:
// its error, its results, and the usage entries LogUsage writes for it.
type preflightAnswer struct {
	Code    string
	Status  int
	Message string
	Rule    *cors.Rule
	Origin  string
	Method  string
	Headers []string
	Usage   []op.UsageEntry
}

var _ = Describe("bucket CORS ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	setCORS := func(ctx context.Context, c *cors.Configuration) {
		GinkgoHelper()
		f.setBucketAttrs(ctx, map[string][]byte{op.AttrCORS: c.Encode()})
	}
	asBob := func(method string) *op.Request {
		r := f.req(method, "plain", "")
		r.Identity = f.bob
		return r
	}
	// preflight is an anonymous OPTIONS request on plain with the headers kv.
	preflight := func(kv ...string) *op.Request {
		r := f.req(http.MethodOptions, "plain", "")
		r.Identity = op.Anonymous()
		return withHeaders(r, kv...)
	}
	// answer runs o for r as the handler runs a preflight, the op, then
	// LogUsage once the status is known, and returns what its caller sees of
	// it.
	answer := func(ctx context.Context, o *op.OptionsCORS, r *op.Request) preflightAnswer {
		GinkgoHelper()
		usage := &opfakes.FakeUsageLogger{}
		f.env.Usage = usage
		err := op.Run(ctx, o, r)
		a := preflightAnswer{Status: http.StatusOK, Rule: o.Rule, Origin: o.Origin, Method: o.Method, Headers: o.Headers}
		if err != nil {
			e := op.AsError(err)
			a.Code, a.Status, a.Message = e.Code, e.Status, e.Message
		}
		r.Status = a.Status
		op.LogUsage(ctx, r, op.OptionsCORSName)
		for i := range usage.LogCallCount() {
			_, e := usage.LogArgsForCall(i)
			a.Usage = append(a.Usage, e)
		}
		return a
	}
	teamTag := func() []byte {
		GinkgoHelper()
		var s tags.Set
		Expect(s.Add("team", "a", tags.MaxBucketTags)).To(Succeed())
		return encodeTags(s)
	}

	It("stores the configuration under radosgw's RGW_ATTR_CORS", func() {
		Expect(op.AttrCORS).To(Equal("user.rgw.cors"), "rgw_common.h:83 at v19.2.7, :88 at v20.2.4")
	})

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetBucketCORS, rgw_op.h:1707-1709 at v19.2.7", &op.GetBucketCORS{}, "get_cors", policy.S3GetBucketCORS, op.OpTypeRead),
		Entry("PutBucketCORS, rgw_op.h:1726-1728 at v19.2.7", &op.PutBucketCORS{}, "put_cors", policy.S3PutBucketCORS, op.OpTypeWrite),
		Entry("DeleteBucketCORS, a write authorized as s3:PutBucketCORS, rgw_op.h:1741-1743 and rgw_op.cc:6116-6117 at v19.2.7",
			&op.DeleteBucketCORS{}, "delete_cors", policy.S3PutBucketCORS, op.OpTypeWrite),
		Entry("OptionsCORS, which IAM never evaluates and which asks for no op mask, where radosgw's asks for read",
			&op.OptionsCORS{}, op.OptionsCORSName, policy.ActionNone, uint32(0)),
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
			buckets.PutBucketAttrsStub = func(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
				seen = append(seen, "write")
				return f.store.PutBucketAttrs(ctx, rec, set, rm)
			}
			authzr = &opfakes.FakeAuthorizer{}
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return nil
			}
			f.env.Buckets, f.env.Authz = buckets, authzr
		})

		get := func() op.Op { return &op.GetBucketCORS{} }
		put := func() op.Op {
			return &op.PutBucketCORS{Params: func(_ context.Context, o *op.PutBucketCORS) error {
				seen = append(seen, "params")
				o.Config = appCORS()
				return nil
			}}
		}
		putRefusedParams := func() op.Op {
			return &op.PutBucketCORS{Params: func(context.Context, *op.PutBucketCORS) error {
				seen = append(seen, "params")
				return op.ErrMalformedXML
			}}
		}
		del := func() op.Op { return &op.DeleteBucketCORS{} }
		options := func() op.Op { return &op.OptionsCORS{} }

		// Each setup readies the request and the bucket for one entry; the
		// bucket holds appCORS unless the entry says otherwise.
		type setup func(ctx context.Context, r *op.Request)
		configured := func(ctx context.Context, _ *op.Request) { setCORS(ctx, appCORS()) }
		unconfigured := func(context.Context, *op.Request) {}
		missing := func(_ context.Context, r *op.Request) { r.Bucket = "missing" }
		undecodable := func(ctx context.Context, _ *op.Request) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrCORS: {0xff}})
		}
		undecodableACL := func(ctx context.Context, r *op.Request) {
			configured(ctx, r)
			f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: {0xff}})
		}
		refused := func(ctx context.Context, r *op.Request) {
			configured(ctx, r)
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		refusedUndecodable := func(ctx context.Context, r *op.Request) {
			refused(ctx, r)
			undecodable(ctx, r)
		}
		writeFails := func(ctx context.Context, r *op.Request) {
			configured(ctx, r)
			buckets.PutBucketAttrsStub = func(context.Context, *op.BucketRecord, map[string][]byte, []string) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}
		unverified := func(ctx context.Context, r *op.Request) {
			configured(ctx, r)
			r.Identity.Unverified = true
		}
		// asks is the preflight's own headers on a configured bucket, kv as
		// withHeaders takes them.
		asks := func(kv ...string) setup {
			return func(ctx context.Context, r *op.Request) {
				configured(ctx, r)
				withHeaders(r, kv...)
			}
		}
		asksOf := func(base setup, kv ...string) setup {
			return func(ctx context.Context, r *op.Request) {
				base(ctx, r)
				withHeaders(r, kv...)
			}
		}
		const origin, appOrigin, method = "Origin", "https://app.example", "Access-Control-Request-Method"

		DescribeTable("stops at the first step that fails, in radosgw's order",
			func(ctx SpecContext, mk func() op.Op, prepare setup, want error, steps []string) {
				o := mk()
				r := f.req(http.MethodPut, "plain", "")
				prepare(ctx, r)
				seen = nil
				err := op.Run(ctx, o, r)
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(want))
				}
				Expect(seen).To(Equal(steps))
			},
			Entry("GetBucketCORS refuses an identity granted without a signature compare before it loads anything",
				get, unverified, op.ErrInternalError, []string(nil)),
			Entry("GetBucketCORS on a missing bucket, before any permission check", get, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("GetBucketCORS for a refused requester, before it decodes the configuration",
				get, refusedUndecodable, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("GetBucketCORS on a bucket without a configuration, rgw_op.cc:6068-6072 at v19.2.7",
				get, unconfigured, op.ErrNoCORSFound, []string{"load", "verify"}),
			Entry("GetBucketCORS on a configuration that does not decode, read_bucket_cors's -EIO, rgw_op.cc:1498-1504 at v19.2.7",
				get, undecodable, op.ErrUnknown, []string{"load", "verify"}),
			Entry("PutBucketCORS refuses an identity granted without a signature compare", put, unverified, op.ErrInternalError, []string(nil)),
			Entry("PutBucketCORS on a missing bucket", put, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("PutBucketCORS for a refused requester, before it reads the body", put, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("PutBucketCORS whose Params refuses the body, writing nothing",
				putRefusedParams, configured, op.ErrMalformedXML, []string{"load", "verify", "params"}),
			Entry("PutBucketCORS whose write fails otherwise than by a race, retrying nothing",
				put, writeFails, op.ErrInternalError, []string{"load", "verify", "params", "write"}),
			Entry("DeleteBucketCORS refuses an identity granted without a signature compare", del, unverified, op.ErrInternalError, []string(nil)),
			Entry("DeleteBucketCORS on a missing bucket", del, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("DeleteBucketCORS for a refused requester", del, refusedUndecodable, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("DeleteBucketCORS on a bucket without a configuration, which succeeds without a write, rgw_op.cc:6138-6142 at v19.2.7",
				del, unconfigured, nil, []string{"load", "verify"}),
			Entry("DeleteBucketCORS on a configuration that does not decode, which it removes where radosgw answers 500, rgw_op.cc:6134-6136 at v19.2.7",
				del, undecodable, nil, []string{"load", "verify", "write"}),
			Entry("DeleteBucketCORS whose write fails otherwise than by a race", del, writeFails, op.ErrInternalError, []string{"load", "verify", "write"}),
			Entry("OptionsCORS on a missing bucket", options, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("OptionsCORS on a configuration that does not decode, before it reads the Origin, rgw_op.cc:6179-6181 at v19.2.7",
				options, undecodable, op.ErrUnknown, []string{"load"}),
			Entry("OptionsCORS on a bucket whose ACL does not decode, decode_policy's -EIO in the bucket load, rgw_op.cc:232-238 and :548-550 at v19.2.7",
				options, asksOf(undecodableACL, origin, appOrigin, method, "GET"), op.ErrUnknown, []string{"load"}),
			Entry("OptionsCORS without an Origin, rgw_op.cc:6183-6188 at v19.2.7", options, asks(method, "GET"), op.ErrInvalidArgument, []string{"load"}),
			Entry("OptionsCORS without an Origin on a bucket without a configuration, which it checks first",
				options, asksOf(unconfigured, method, "GET"), op.ErrInvalidArgument, []string{"load"}),
			Entry("OptionsCORS without an Access-Control-Request-Method, rgw_op.cc:6189-6194 at v19.2.7",
				options, asksOf(unconfigured, origin, appOrigin), op.ErrInvalidArgument, []string{"load"}),
			Entry("OptionsCORS on a bucket without a configuration, ENOENT answered as EACCES, rgw_op.cc:6195-6199 and rgw_rest_s3.cc:3861-3862 at v19.2.7",
				options, asksOf(unconfigured, origin, appOrigin, method, "GET"), op.ErrAccessDenied, []string{"load"}),
			Entry("OptionsCORS whose request no rule allows, rgw_op.cc:6200-6205 at v19.2.7",
				options, asks(origin, "https://elsewhere.example", method, "GET"), op.ErrAccessDenied, []string{"load"}),
			Entry("OptionsCORS whose request a rule allows, checking no permission and writing nothing",
				options, asks(origin, appOrigin, method, "GET"), nil, []string{"load"}),
			Entry("OptionsCORS for an identity granted without a signature compare, the one op that serves one",
				options, asksOf(unverified, origin, appOrigin, method, "GET"), nil, []string{"load"}),
		)
		DescribeTable("refuses an op mask without the op's bit before it reads the body or writes",
			func(ctx SpecContext, mk func() op.Op) {
				setCORS(ctx, appCORS())
				o := mk()
				r := f.req(http.MethodPut, "plain", "")
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				seen = nil
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).NotTo(ContainElement(BeElementOf("params", "write")))
			},
			Entry("GetBucketCORS, without read", get),
			Entry("PutBucketCORS, without write", put),
			Entry("DeleteBucketCORS, without write", del),
		)
		DescribeTable("authorizes its action against the bucket with the ACL permission op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, a policy.Action, perm acl.Permission, steps []string) {
				setCORS(ctx, appCORS())
				seen = nil
				Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(Succeed())
				Expect(seen).To(Equal(steps))
				Expect(authzr.VerifyBucketCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(a))
				Expect(gotPerm).To(Equal(perm))
			},
			Entry("GetBucketCORS, READ_ACP, rgw_iam_policy.h:272 and :289 at v19.2.7", get, policy.S3GetBucketCORS, acl.PermReadACP,
				[]string{"load", "verify"}),
			Entry("PutBucketCORS, WRITE_ACP, rgw_iam_policy.h:296 and :311 at v19.2.7", put, policy.S3PutBucketCORS, acl.PermWriteACP,
				[]string{"load", "verify", "params", "write"}),
			Entry("DeleteBucketCORS, WRITE_ACP as s3:PutBucketCORS", del, policy.S3PutBucketCORS, acl.PermWriteACP,
				[]string{"load", "verify", "write"}),
		)
		It("authorizes no preflight, whatever the authorizer would answer", func(ctx SpecContext) {
			refused(ctx, nil)
			seen = nil
			Expect(op.Run(ctx, &op.OptionsCORS{}, preflight(origin, appOrigin, method, "GET"))).To(Succeed(),
				"verify_permission returns 0, rgw_op.h:1756 at v19.2.7, :1912 at v20.2.4")
			Expect(authzr.Invocations()).To(BeEmpty())
		})

		// glacial is a storage class the bucket's placement lacks.
		const glacial = "GLACIAL"
		DescribeTable("refuses a storage class the bucket's placement lacks with 400 InvalidArgument, rgw_op.cc:576-583 at v19.2.7, :606-613 at v20.2.4",
			func(ctx SpecContext, mk func(class string) op.Op, prepare setup, want error, steps []string) {
				// run is the op for class on a request and bucket prepared
				// alike, and the code it answers, "" for success.
				run := func(class string) string {
					r := f.req(http.MethodPut, "plain", "")
					prepare(ctx, r)
					seen = nil
					if err := op.Run(ctx, mk(class), r); err != nil {
						return op.AsError(err).Code
					}
					return ""
				}
				Expect(run(glacial)).To(Equal(op.AsError(want).Code))
				Expect(seen).To(Equal(steps))
				Expect(run(meta.StorageClassStandard)).To(Equal(run("")), "a class the placement has answers as no class does")
			},
			Entry("GetBucketCORS, once the requester is authorized and before it decodes the configuration",
				func(c string) op.Op { return &op.GetBucketCORS{StorageClass: c} }, undecodable, op.ErrInvalidArgument,
				[]string{"load", "verify"}),
			Entry("GetBucketCORS, which a refused requester is not told",
				func(c string) op.Op { return &op.GetBucketCORS{StorageClass: c} }, refused, op.ErrAccessDenied,
				[]string{"load", "verify"}),
			Entry("PutBucketCORS, once the requester is authorized and before it reads the body",
				func(c string) op.Op {
					return &op.PutBucketCORS{StorageClass: c, Params: func(_ context.Context, o *op.PutBucketCORS) error {
						seen = append(seen, "params")
						o.Config = appCORS()
						return nil
					}}
				}, configured, op.ErrInvalidArgument, []string{"load", "verify"}),
			Entry("PutBucketCORS, which a refused requester is not told",
				func(c string) op.Op { return &op.PutBucketCORS{StorageClass: c, Config: appCORS()} }, refused, op.ErrAccessDenied,
				[]string{"load", "verify"}),
			Entry("DeleteBucketCORS, once the requester is authorized, writing nothing",
				func(c string) op.Op { return &op.DeleteBucketCORS{StorageClass: c} }, configured, op.ErrInvalidArgument,
				[]string{"load", "verify"}),
			Entry("DeleteBucketCORS, which a refused requester is not told",
				func(c string) op.Op { return &op.DeleteBucketCORS{StorageClass: c} }, refused, op.ErrAccessDenied,
				[]string{"load", "verify"}),
			Entry("OptionsCORS, after the redirect and ahead of an ACL and a configuration that do not decode, as the bucket load orders them",
				func(c string) op.Op { return &op.OptionsCORS{StorageClass: c} },
				asksOf(func(ctx context.Context, r *op.Request) {
					undecodable(ctx, r)
					f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: {0xff}})
				}, origin, appOrigin, method, "GET"), op.ErrInvalidArgument, []string{"load"}),
		)
	})

	DescribeTable("gives a policy the bucket's tags",
		func(ctx SpecContext, mk func() op.Op, name string) {
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			setCORS(ctx, appCORS())
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
				`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":%q,"Resource":"arn:aws:s3:::plain","Condition":{"StringEquals":{"s3:ResourceTag/team":"a"}}}]}`,
				name)})
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag()})
			Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied), "a tagged bucket")
			Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{tags.Attr})).To(Succeed())
			Expect(op.Run(ctx, mk(), f.req(http.MethodPut, "plain", ""))).To(Succeed(), "an untagged one")
		},
		Entry("GetBucketCORS, rgw_op.cc:6051-6053 at v19.2.7", func() op.Op { return &op.GetBucketCORS{} }, "s3:GetBucketCORS"),
		Entry("PutBucketCORS, rgw_op.cc:6077-6079 at v19.2.7", func() op.Op { return &op.PutBucketCORS{Config: otherCORS()} },
			"s3:PutBucketCORS"),
		Entry("DeleteBucketCORS, rgw_op.cc:6112-6114 at v19.2.7", func() op.Op { return &op.DeleteBucketCORS{} }, "s3:PutBucketCORS"),
	)

	Describe("GetBucketCORS", func() {
		It("answers NoSuchCORSConfiguration with no message for a bucket without a configuration", func(ctx SpecContext) {
			err := op.Run(ctx, &op.GetBucketCORS{}, f.req(http.MethodGet, "plain", ""))
			Expect(err).To(MatchError(op.ErrNoCORSFound))
			Expect(op.AsError(err).Errno).To(Equal(op.ErrNoCORSFound.Errno), "ERR_NO_CORS_FOUND, rgw_op.cc:6070 at v19.2.7")
			Expect(op.AsError(err).Message).To(BeEmpty())
		})
		It("takes the stored configuration", func(ctx SpecContext) {
			setCORS(ctx, appCORS())
			o := &op.GetBucketCORS{}
			Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(o.Config).To(Equal(decodedCORS(appCORS())))
		})
		It("refuses a requester the authorizer refuses", func(ctx SpecContext) {
			setCORS(ctx, appCORS())
			o := &op.GetBucketCORS{}
			Expect(op.Run(ctx, o, asBob(http.MethodGet))).To(MatchError(op.ErrAccessDenied))
			Expect(o.Config).To(BeNil())
		})
	})

	Describe("PutBucketCORS", func() {
		It("stores the configuration's encoding, which GetBucketCORS returns, replacing a stored one whole", func(ctx SpecContext) {
			setCORS(ctx, otherCORS())
			Expect(op.Run(ctx, &op.PutBucketCORS{Config: appCORS()}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrCORS, appCORS().Encode()), "rgw_op.cc:6105 at v19.2.7")
			g := &op.GetBucketCORS{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Config).To(Equal(decodedCORS(appCORS())))
		})
		It("runs Params once the requester is authorized, and stores what it parsed", func(ctx SpecContext) {
			calls := 0
			params := func(_ context.Context, o *op.PutBucketCORS) error {
				calls++
				o.Config = otherCORS()
				return nil
			}
			Expect(op.Run(ctx, &op.PutBucketCORS{Params: params}, asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero(), "get_params runs in execute, after verify_permission, rgw_op.cc:6092 at v19.2.7")
			Expect(op.Run(ctx, &op.PutBucketCORS{Params: params}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(calls).To(Equal(1))
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrCORS, otherCORS().Encode()))
		})
		It("returns Params' error and stores nothing", func(ctx SpecContext) {
			params := func(context.Context, *op.PutBucketCORS) error { return op.ErrInvalidCORSRules }
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.PutBucketCORS{Params: params}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInvalidCORSRules))
			Expect(bucket(ctx)).To(Equal(before))
		})
		It("answers InternalError, storing nothing, when neither Params nor Config gives a configuration", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.PutBucketCORS{}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInternalError))
			Expect(bucket(ctx)).To(Equal(before))
		})
		It("sets the attr alone over the bucket, keeping every other attr", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag()})
			before := bucket(ctx).Attrs
			Expect(op.Run(ctx, &op.PutBucketCORS{Config: appCORS()}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			want := maps.Clone(before)
			want[op.AttrCORS] = appCORS().Encode()
			Expect(bucket(ctx).Attrs).To(Equal(want))
		})
	})

	Describe("DeleteBucketCORS", func() {
		It("removes the configuration alone, after which GetBucketCORS finds none", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag()})
			setCORS(ctx, appCORS())
			Expect(op.Run(ctx, &op.DeleteBucketCORS{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			attrs := bucket(ctx).Attrs
			Expect(attrs).NotTo(HaveKey(op.AttrCORS), "rgw_op.cc:6144-6146 at v19.2.7")
			Expect(attrs).To(HaveKeyWithValue(tags.Attr, teamTag()))
			Expect(op.Run(ctx, &op.GetBucketCORS{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoCORSFound))
		})
		It("succeeds on a bucket without a configuration and writes nothing, as radosgw answers its -ENOENT 204", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.DeleteBucketCORS{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed(),
				"rgw_op.cc:6138-6142 and rgw_rest_s3.cc:3845-3847 at v19.2.7")
			Expect(bucket(ctx)).To(Equal(before))
		})
		It("removes a configuration that does not decode, where radosgw answers 500 and keeps it", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrCORS: {0xff}})
			Expect(op.Run(ctx, &op.DeleteBucketCORS{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed(),
				`read_bucket_cors's -EIO, rgw_op.cc:6134-6136 at v19.2.7; docs/exclusions.md, "DeleteBucketCors removes a CORS configuration that does not decode"`)
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrCORS))
			Expect(op.Run(ctx, &op.GetBucketCORS{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoCORSFound))
		})
		It("refuses a requester the authorizer refuses and keeps the configuration", func(ctx SpecContext) {
			setCORS(ctx, appCORS())
			Expect(op.Run(ctx, &op.DeleteBucketCORS{}, asBob(http.MethodDelete))).To(MatchError(op.ErrAccessDenied))
			Expect(bucket(ctx).Attrs).To(HaveKey(op.AttrCORS))
		})
	})

	Describe("a CORS write that loses a race", func() {
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			setCORS(ctx, otherCORS())
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			calls := 0
			put := &op.PutBucketCORS{Params: func(_ context.Context, o *op.PutBucketCORS) error {
				calls++
				o.Config = appCORS()
				return nil
			}}
			Expect(op.Run(ctx, put, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16), "rgw_op.h:189-195 at v19.2.7")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			Expect(calls).To(Equal(1), "the body is read once")
			_, _, set, rm := buckets.PutBucketAttrsArgsForCall(15)
			Expect(set).To(Equal(map[string][]byte{op.AttrCORS: appCORS().Encode()}), "each try sets the attr alone")
			Expect(rm).To(BeEmpty())
			Expect(op.Run(ctx, &op.DeleteBucketCORS{}, f.req(http.MethodDelete, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(32))
			_, _, set, rm = buckets.PutBucketAttrsArgsForCall(31)
			Expect(set).To(BeEmpty())
			Expect(rm).To(Equal([]string{op.AttrCORS}), "each try removes the attr alone")
		})
		DescribeTable("stops without writing when the name now names another bucket",
			func(ctx SpecContext, o op.Op) {
				setCORS(ctx, otherCORS())
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
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "radosgw writes into the bucket re-created under the name")
			},
			Entry("PutBucketCORS, rgw_op.cc:6103-6107 at v19.2.7", &op.PutBucketCORS{Config: appCORS()}),
			Entry("DeleteBucketCORS, rgw_op.cc:6133-6152 at v19.2.7", &op.DeleteBucketCORS{}),
		)

		Describe("to another write of the bucket", func() {
			// bobCanWriteACP is alice's ACL with WRITE_ACP, the permission both
			// CORS writes map to, granted to bob.
			bobCanWriteACP := func() acl.Policy {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				return p
			}
			// keepGrant is a winning PutBucketAcl that keeps bob's grant and
			// makes the bucket publicly readable.
			keepGrant := func() map[string][]byte {
				p := bobCanWriteACP()
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeGroup, Group: acl.GroupAllUsers, Permission: acl.PermRead})
				return map[string][]byte{meta.AttrACL: encodedPolicy(p)}
			}
			// revokeByACL is a winning PutBucketAcl that drops bob's grant.
			revokeByACL := func() map[string][]byte {
				return map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(f.alice.Owner, "Alice"))}
			}
			// denyBobOn is a winning PutBucketPolicy that denies bob the actions
			// alone.
			denyBobOn := func(actions ...string) func() map[string][]byte {
				return func() map[string][]byte {
					names, err := json.Marshal(actions)
					Expect(err).NotTo(HaveOccurred())
					return map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
						`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":%s,"Resource":"arn:aws:s3:::plain"}]}`,
						names)}
				}
			}

			var (
				buckets *opfakes.FakeBucketStore
				// raced is the bucket's attrs as the write the first try lost
				// to left them.
				raced map[string][]byte
			)
			// race gives plain bob's WRITE_ACP grant and otherCORS, then makes
			// the first write of the bucket lose to concurrent, another
			// request's change of the bucket's attrs, and lets every retry
			// through to the store.
			race := func(ctx context.Context, concurrent map[string][]byte) {
				GinkgoHelper()
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(bobCanWriteACP()), op.AttrCORS: otherCORS().Encode()})
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
			put := func() op.Op { return &op.PutBucketCORS{Config: appCORS()} }
			del := func() op.Op { return &op.DeleteBucketCORS{} }

			DescribeTable("writes over the bucket as the retry read it, keeping the winner's ACL",
				func(ctx SpecContext, mk func() op.Op, check func(attrs map[string][]byte)) {
					race(ctx, keepGrant())
					Expect(op.Run(ctx, mk(), asBob(http.MethodPut))).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2), "the first try lost, the retry wrote")
					attrs := bucket(ctx).Attrs
					Expect(attrs).To(HaveKeyWithValue(meta.AttrACL, keepGrant()[meta.AttrACL]),
						"radosgw's retry merges the ACL its request loaded back, rgw_op.cc:6104-6106 at v19.2.7")
					check(attrs)
				},
				Entry("PutBucketCORS", put, func(attrs map[string][]byte) {
					Expect(attrs).To(HaveKeyWithValue(op.AttrCORS, appCORS().Encode()))
				}),
				Entry("DeleteBucketCORS", del, func(attrs map[string][]byte) { Expect(attrs).NotTo(HaveKey(op.AttrCORS)) }),
			)
			DescribeTable("authorizes the retry again and writes nothing for a requester the winner refuses, leaving the winner's bucket",
				func(ctx SpecContext, mk func() op.Op, revoke func() map[string][]byte) {
					race(ctx, revoke())
					Expect(op.Run(ctx, mk(), asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
					Expect(bucket(ctx).Attrs).To(Equal(raced))
				},
				Entry("PutBucketCORS under an ACL that drops the grant", put, revokeByACL),
				Entry("PutBucketCORS under a policy that denies s3:PutBucketCORS alone", put, denyBobOn("s3:PutBucketCORS")),
				Entry("DeleteBucketCORS under an ACL that drops the grant", del, revokeByACL),
				Entry("DeleteBucketCORS under a policy that denies s3:PutBucketCORS alone, the action it authorizes",
					del, denyBobOn("s3:PutBucketCORS")),
			)
			DescribeTable("writes on a retry whose requester still holds the permission",
				func(ctx SpecContext, mk func() op.Op) {
					race(ctx, denyBobOn("s3:PutBucketAcl", "s3:PutBucketPolicy", "s3:PutBucketTagging", "s3:GetBucketCORS")())
					Expect(op.Run(ctx, mk(), asBob(http.MethodPut))).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
				},
				Entry("PutBucketCORS", put),
				Entry("DeleteBucketCORS", del),
			)
		})
	})

	Describe("OptionsCORS", func() {
		BeforeEach(func(ctx SpecContext) { setCORS(ctx, appCORS()) })

		It("answers with the rule that allows the origin, the method and every requested header", func(ctx SpecContext) {
			o := &op.OptionsCORS{}
			r := preflight("Origin", "https://app.example", "Access-Control-Request-Method", "PUT",
				"Access-Control-Request-Headers", "X-Amz-Date, x-amz-meta-a")
			Expect(op.Run(ctx, o, r)).To(Succeed())
			Expect(o.Rule).To(Equal(&decodedCORS(appCORS()).Rules[0]))
			Expect(o.Origin).To(Equal("https://app.example"))
			Expect(o.Method).To(Equal("PUT"))
			Expect(o.Headers).To(Equal([]string{"X-Amz-Date", "x-amz-meta-a"}), "as sent, split as get_str_list splits them")
		})
		It("refuses with 403 a request whose origin a rule allows but whose method none does", func(ctx SpecContext) {
			o := &op.OptionsCORS{}
			err := op.Run(ctx, o, preflight("Origin", "https://app.example", "Access-Control-Request-Method", "POST"))
			Expect(err).To(MatchError(op.ErrAccessDenied), "validate_cors_rule_method's -ENOENT as EACCES, rgw_op.cc:6166-6168 at v19.2.7")
			Expect(op.AsError(err).Message).To(BeEmpty())
			Expect(o.Rule).To(BeNil())
		})
		It("takes a later rule that allows the method where the first rule for the origin does not, as Amazon S3 does", func(ctx SpecContext) {
			o := &op.OptionsCORS{}
			Expect(op.Run(ctx, o, preflight("Origin", "https://app.example", "Access-Control-Request-Method", "DELETE"))).To(Succeed(),
				`docs/exclusions.md, "CORS rules are matched as Amazon S3 matches them"`)
			Expect(o.Rule.ID).To(Equal("app-delete"))
		})
		It("refuses with 403 a request one of whose headers the rule does not allow", func(ctx SpecContext) {
			r := preflight("Origin", "https://app.example", "Access-Control-Request-Method", "GET",
				"Access-Control-Request-Headers", "x-amz-date,content-type")
			Expect(op.Run(ctx, &op.OptionsCORS{}, r)).To(MatchError(op.ErrAccessDenied))
		})
		It("reads the last of a repeated header, as radosgw's RGWEnv holds it", func(ctx SpecContext) {
			o := &op.OptionsCORS{}
			r := preflight("Origin", "https://elsewhere.example", "Origin", "https://app.example",
				"Access-Control-Request-Method", "POST", "Access-Control-Request-Method", "GET")
			Expect(op.Run(ctx, o, r)).To(Succeed())
			Expect([]string{o.Origin, o.Method}).To(Equal([]string{"https://app.example", "GET"}))
		})
		It("takes an empty Origin for one that is sent, which no rule allows", func(ctx SpecContext) {
			r := preflight("Origin", "", "Access-Control-Request-Method", "HEAD")
			Expect(op.Run(ctx, &op.OptionsCORS{}, r)).To(MatchError(op.ErrAccessDenied), "the any-origin rule allows HEAD")
		})
		It("redirects a preflight of another zonegroup's bucket, from the master zonegroup for a system key too", func(ctx SpecContext) {
			zone := &opfakes.FakeZoneInfo{}
			zone.ZoneGroupReturns(meta.ZoneGroup{ID: "zg", IsMaster: true})
			zone.ReleaseReturns(denc.Squid)
			f.env.Zone = zone
			rec := bucket(ctx)
			rec.Info.Zonegroup = "elsewhere"
			Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
			r := preflight("Origin", "https://app.example", "Access-Control-Request-Method", "GET")
			u := meta.NewUserInfo()
			u.UserID, u.System = meta.UserID{ID: "sys"}, 1
			r.Identity = forgedPreflight(u, nil)
			Expect(op.Run(ctx, &op.OptionsCORS{}, r)).To(MatchError(op.ErrPermanentRedirect),
				"radosgw lets a system request through, rgw_op.cc:565-566 at v19.2.7")
		})
	})

	Describe("OptionsCORS's answer to a forged preflight", func() {
		// keys are the preflights the table runs beside an anonymous one, each
		// on a key whose user is the kind its name says and owns none of the
		// buckets. A new kind of key is a new member here.
		keys := func() map[string]op.Identity {
			user := func(tenant, id string, set func(*meta.UserInfo)) meta.UserInfo {
				u := meta.NewUserInfo()
				u.UserID = meta.UserID{Tenant: tenant, ID: id}
				if set != nil {
					set(&u)
				}
				return u
			}
			acct := &meta.AccountInfo{ID: "RGW12345678901234567", Name: "acct"}
			return map[string]op.Identity{
				"a plain user's":     forgedPreflight(user("", "plain", nil), nil),
				"a tenanted user's":  forgedPreflight(user("t", "tenanted", nil), nil),
				"an account user's":  forgedPreflight(user("", "member", func(u *meta.UserInfo) { u.AccountID = acct.ID }), acct),
				"a suspended user's": forgedPreflight(user("", "suspended", func(u *meta.UserInfo) { u.Suspended = 1 }), nil),
				"a system user's":    forgedPreflight(user("", "system", func(u *meta.UserInfo) { u.System = 1 }), nil),
				"an admin's":         forgedPreflight(user("", "admin", func(u *meta.UserInfo) { u.Admin = 1 }), nil),
				"a user's whose op mask lacks read": forgedPreflight(user("", "noread",
					func(u *meta.UserInfo) { u.OpMask = op.OpTypeModify }), nil),
			}
		}
		// name and key are the bucket and object every preflight of an entry
		// names, the bucket resolved alike for every key, query its query and
		// class its x-amz-storage-class: alice's requester-pays bucket plain at
		// bucket scope with neither, unless the entry's preparation names
		// others.
		var (
			name, key, class string
			query            url.Values
		)
		BeforeEach(func(ctx SpecContext) {
			name, key, class, query = "plain", "", "", url.Values{}
			f.env.Now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			rec := bucket(ctx)
			rec.Info.RequesterPays = true
			Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
			setCORS(ctx, appCORS())
		})

		const allowed = "https://app.example"
		DescribeTable("is the anonymous preflight's for every key that resolves, and charges none of their users",
			func(ctx SpecContext, prepare func(ctx context.Context), status int, kv ...string) {
				prepare(ctx)
				of := func(id op.Identity) preflightAnswer {
					r := withHeaders(f.req(http.MethodOptions, name, key), kv...)
					r.Query = query
					r.Identity = id
					return answer(ctx, &op.OptionsCORS{StorageClass: class}, r)
				}
				want := of(op.Anonymous())
				Expect(want.Status).To(Equal(status), "the anonymous preflight")
				for kind, id := range keys() {
					got := of(id)
					Expect(got).To(Equal(want), "%s key", kind)
					charged := []meta.Owner{id.Owner, meta.UserOwner(id.User.UserID)}
					for _, e := range got.Usage {
						Expect(charged).NotTo(ContainElement(e.Owner), "%s key: the entry's owner", kind)
						Expect(charged).NotTo(ContainElement(e.Payer), "%s key: the entry's payer", kind)
					}
				}
			},
			Entry("on a request a rule allows, the anonymous user its payer", func(context.Context) {}, http.StatusOK,
				"Origin", allowed, "Access-Control-Request-Method", "PUT", "Access-Control-Request-Headers", "x-amz-date"),
			Entry("on a request whose method no rule allows for its origin, which charges no payer", func(context.Context) {},
				http.StatusForbidden, "Origin", allowed, "Access-Control-Request-Method", "POST"),
			Entry("on a request without an Origin", func(context.Context) {}, http.StatusBadRequest,
				"Access-Control-Request-Method", "GET"),
			Entry("on a request without an Access-Control-Request-Method", func(context.Context) {}, http.StatusBadRequest,
				"Origin", allowed),
			Entry("on a bucket without a configuration", func(ctx context.Context) {
				Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{op.AttrCORS})).To(Succeed())
			}, http.StatusForbidden, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on a configuration that does not decode", func(ctx context.Context) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrCORS: {0xff}})
			}, http.StatusInternalServerError, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on a missing bucket, which logs nothing", func(context.Context) { name = "missing" },
				http.StatusNotFound, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on another zonegroup's bucket from the master zonegroup, where radosgw lets a system key through",
				func(ctx context.Context) {
					zone := &opfakes.FakeZoneInfo{}
					zone.ZoneGroupReturns(meta.ZoneGroup{ID: "zg", IsMaster: true})
					zone.ReleaseReturns(denc.Squid)
					f.env.Zone = zone
					rec := bucket(ctx)
					rec.Info.Zonegroup = "elsewhere"
					Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
				}, http.StatusMovedPermanently, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on a suspended bucket, which radosgw refuses but to a system key, and on Tentacle an admin's",
				func(ctx context.Context) { f.setBucketFlags(ctx, meta.BucketSuspended) },
				http.StatusOK, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on a bucket whose policy does not parse, which radosgw refuses but to a system key, and on Tentacle an admin's",
				func(ctx context.Context) { f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte("{")}) },
				http.StatusOK, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on a bucket whose ACL does not decode, radosgw's 500 for every key",
				func(ctx context.Context) { f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: {0xff}}) },
				http.StatusInternalServerError, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("on a bucket whose ACL names an owner that does not load, which radosgw refuses but to a system key or one without the modify bits",
				func(ctx context.Context) {
					gone := meta.UserOwner(meta.UserID{ID: "gone"})
					rec := bucket(ctx)
					rec.Info.Owner = gone
					Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
					f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(gone, ""))})
				}, http.StatusOK, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("with an rgwx-bucket-instance that names no instance, which radosgw answers 400 for a system key alone",
				func(context.Context) { query.Set("rgwx-bucket-instance", "x") },
				http.StatusOK, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("with an x-amz-storage-class the bucket's placement lacks, which radosgw answers 400 for every key too",
				func(context.Context) { class = "GLACIAL" },
				http.StatusBadRequest, "Origin", allowed, "Access-Control-Request-Method", "GET"),
			Entry("at object scope, of a key that does not exist, which the preflight never reads",
				func(context.Context) { key = "missing" },
				http.StatusOK, "Origin", allowed, "Access-Control-Request-Method", "GET"),
		)
	})

	Describe("the tenant a forged preflight's bucket resolves under", func() {
		// resolve is the handler's resolution of a bucket name, made before
		// any op runs: a name that names no tenant takes the identity's
		// (s3.PostAuthInit, internal/s3/request.go:221-233).
		resolve := func(r *op.Request, name string) *op.Request {
			if t, b, ok := strings.Cut(name, ":"); ok {
				r.Tenant, r.Bucket = t, b
				return r
			}
			r.Tenant, r.Bucket = r.Identity.Tenant, name
			return r
		}
		of := func(ctx context.Context, id op.Identity, name string) preflightAnswer {
			GinkgoHelper()
			r := preflight("Origin", "https://other.example", "Access-Control-Request-Method", "GET")
			r.Identity = id
			return answer(ctx, &op.OptionsCORS{}, resolve(r, name))
		}
		BeforeEach(func(ctx SpecContext) {
			f.env.Now = func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }
			setCORS(ctx, appCORS())
			_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
				Tenant: "t", Name: "plain", Owner: meta.UserOwner(meta.UserID{Tenant: "t", ID: "towner"}),
				Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs:     map[string][]byte{op.AttrCORS: otherCORS().Encode()},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("is the key user's, so a tenanted key's preflight of a name without one answers as an anonymous preflight of the name under it",
			func(ctx SpecContext) {
				u := meta.NewUserInfo()
				u.UserID = meta.UserID{Tenant: "t", ID: "tenanted"}
				got := of(ctx, forgedPreflight(u, nil), "plain")
				Expect(got).To(Equal(of(ctx, op.Anonymous(), "t:plain")), "rgw_rest_s3.cc:4995-4998 and rgw_bucket.cc:127-130 at v19.2.7")
				Expect(got.Rule).NotTo(BeNil())
				Expect(got.Rule.ID).To(Equal("other"))
				untenanted := of(ctx, op.Anonymous(), "plain")
				Expect(untenanted.Code).To(Equal(op.ErrAccessDenied.Code), "the default tenant's plain allows no https://other.example")
				Expect(got).NotTo(Equal(untenanted))
			})
	})
})
