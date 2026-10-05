package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// failingBody yields data and then, where it would end, err: a verifying
// reader whose payload check failed.
type failingBody struct {
	r   io.Reader
	err error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

var _ = Describe("create_bucket, delete_bucket, stat_bucket and get_bucket_location", func() {
	const locationHead = `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`
	var (
		store *memstore.Store
		env   *op.Env
		alice *op.UserRecord
		bob   *op.UserRecord
		h     *s3.Handler
	)
	setup := func(rel denc.Release, apiName string) {
		zg := meta.ZoneGroup{
			ID: "zg-1", Name: apiName, APIName: apiName, IsMaster: true,
			DefaultPlacement: meta.PlacementRule{Name: "default-placement"},
			PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{
				"default-placement": {Name: "default-placement", StorageClasses: []string{meta.StorageClassStandard}},
			},
		}
		period := meta.NewPeriod()
		period.ID = "period-1"
		period.PeriodMap.ZoneGroups = map[string]meta.ZoneGroup{zg.ID: zg}
		store = memstore.New(memstore.Config{Release: rel, ZoneGroup: zg, Period: period})
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll, MaxBuckets: 1000})
		bob = store.AddUser(meta.UserInfo{
			UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll, MaxBuckets: 1000,
			UserQuota: meta.Quota{MaxSize: 4096, MaxObjects: 7, Enabled: true},
		})
		env = testEnv(store)
		h = s3.NewHandler(env, authAs(bob), testConfig(s3.Config{}))
	}
	BeforeEach(func() { setup(denc.Squid, "ceph-objectstore") })
	as := func(who *op.UserRecord) *s3.Handler { return s3.NewHandler(env, authAs(who), testConfig(s3.Config{})) }
	put := func(target, body string, hdr ...string) *httptest.ResponseRecorder {
		var b io.Reader
		if body != "" {
			b = strings.NewReader(body)
		}
		return serveReq(h, http.MethodPut, target, b, hdr...)
	}
	config := func(inner string) string {
		return "<CreateBucketConfiguration>" + inner + "</CreateBucketConfiguration>"
	}
	bucketACL := func(ctx context.Context) acl.Policy {
		GinkgoHelper()
		rec, err := store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		p, err := op.BucketACLFor(rec)
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	Describe("PUT bucket", func() {
		It("creates the bucket for the requester, answering 200 with no body", func(ctx SpecContext) {
			rec := put("/plain", "")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Body.String()).To(BeEmpty())
			Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
			Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "end_header sends no type for an empty body, rgw_rest.cc:611-619")
			b, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(b.Info.Owner).To(Equal(meta.UserOwner(bob.Info.UserID)))
			Expect(bucketACL(ctx)).To(Equal(acl.DefaultPolicy(meta.UserOwner(bob.Info.UserID), "Bob")),
				"a private canned ACL: the owner's FULL_CONTROL")
		})
		It("answers the owner's re-create 200 and another owner's 409", func() {
			Expect(put("/plain", "").Code).To(Equal(200))
			Expect(put("/plain", "").Code).To(Equal(200), "rgw_rest_s3.cc:2546-2547")
			rec := serveReq(as(alice), http.MethodPut, "/plain", nil)
			Expect(rec.Code).To(Equal(409))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>BucketAlreadyExists</Code>"))
		})
		It("refuses an invalid name", func() {
			rec := put("/ab", "")
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidBucketName</Code>"))
		})
		It("checks the name under rgw_relaxed_s3_bucket_names", func() {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_relaxed_s3_bucket_names": "true"})
			Expect(put("/Under_score", "").Code).To(Equal(200))
		})
		It("refuses a location constraint the period lacks with radosgw's message", func() {
			rec := put("/plain", config("<LocationConstraint>zz</LocationConstraint>"))
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidLocationConstraint</Code><Message>The zz location constraint is not valid.</Message>"))
		})
		It("accepts this zonegroup's api name and takes the placement after a colon", func(ctx SpecContext) {
			Expect(put("/plain", config("<LocationConstraint>ceph-objectstore</LocationConstraint>")).Code).To(Equal(200))
			rec := put("/other", config("<LocationConstraint>ceph-objectstore:absent</LocationConstraint>"))
			Expect(rec.Code).To(Equal(400), "the placement half is a placement name, rgw_rest_s3.cc:2526-2531")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidLocationConstraint</Code>"))
		})
		It("takes the storage class from x-amz-storage-class", func(ctx SpecContext) {
			Expect(put("/plain", "", "x-amz-storage-class", "STANDARD").Code).To(Equal(200))
			b, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			Expect(b.Info.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "STANDARD"}))
		})
		DescribeTable("refuses a body that is not a CreateBucketConfiguration with a LocationConstraint on Squid",
			func(body string) {
				rec := put("/plain", body)
				Expect(rec.Code).To(Equal(400), body)
				Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
			},
			Entry("not XML", "nope"),
			Entry("another root", "<Other><LocationConstraint>x</LocationConstraint></Other>"),
			Entry("no LocationConstraint", config("")),
			Entry("a prefixed root", `<s3:CreateBucketConfiguration xmlns:s3="x"><LocationConstraint>ceph-objectstore</LocationConstraint></s3:CreateBucketConfiguration>`),
		)
		It("ignores a body sent without a Content-Length", func() {
			req := httptest.NewRequestWithContext(GinkgoT().Context(), http.MethodPut, "/plain", io.NopCloser(strings.NewReader("nope")))
			req.ContentLength = -1
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(200), "read_all_input without chunked input, rgw_rest.cc:1545-1548")
		})
		It("refuses a body past rgw_max_put_param_size as InvalidRange", func() {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			rec := put("/plain", config("<LocationConstraint>ceph-objectstore</LocationConstraint>"))
			Expect(rec.Code).To(Equal(416), "-ERANGE, rgw_rest.cc:1551-1552")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
		})
		It("returns the verifying reader's failure before it parses the body", func(ctx SpecContext) {
			auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				body := config("<LocationConstraint>zz</LocationConstraint>")
				return &op.AuthResult{
					Identity:      op.Identity{User: &bob.Info, Owner: meta.UserOwner(bob.Info.UserID), OpMask: bob.Info.OpMask},
					Body:          &failingBody{r: strings.NewReader(body), err: op.ErrContentSHA256Mismatch},
					ContentLength: int64(len(body)),
				}, nil
			})
			rec := serveReq(s3.NewHandler(env, auth, testConfig(s3.Config{})), http.MethodPut, "/plain",
				strings.NewReader(config("<LocationConstraint>zz</LocationConstraint>")))
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			_, err := store.GetBucket(ctx, "", "plain")
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
		DescribeTable("checks x-amz-bucket-object-lock-enabled",
			func(v string, code int, errCode string) {
				rec := put("/plain", "", "x-amz-bucket-object-lock-enabled", v)
				Expect(rec.Code).To(Equal(code))
				if errCode != "" {
					Expect(rec.Body.String()).To(ContainSubstring("<Code>" + errCode + "</Code>"))
				}
			},
			Entry("maybe is InvalidArgument", "maybe", 400, "InvalidArgument"),
			Entry("true is not served yet", "true", 501, "NotImplemented"),
			Entry("false creates the bucket", "false", 200, ""),
		)
		It("applies a canned ACL", func(ctx SpecContext) {
			Expect(put("/plain", "", "x-amz-acl", "public-read").Code).To(Equal(200))
			want, err := acl.Canned(acl.Owner{ID: "bob", DisplayName: "Bob"}, acl.Owner{}, "public-read")
			Expect(err).NotTo(HaveOccurred())
			e := denc.NewEncoder()
			want.Encode(e, denc.Squid)
			Expect(bucketACL(ctx)).To(Equal(acl.DecodePolicy(denc.NewDecoder(e.Bytes()))), "the owner's FULL_CONTROL and AllUsers READ")
		})
		It("refuses an unknown canned ACL", func() {
			rec := put("/plain", "", "x-amz-acl", "everyone")
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
		})
		It("builds the ACL from grant headers alone, without the owner's grant", func(ctx SpecContext) {
			Expect(put("/plain", "", "x-amz-grant-read", `id="alice"`).Code).To(Equal(200))
			got := bucketACL(ctx)
			Expect(got.Owner).To(Equal(acl.Owner{ID: "bob", DisplayName: "Bob"}))
			Expect(got.ACL.Grants).To(HaveLen(1), "create_policy_from_headers, rgw_acl_s3.cc:691-709")
			Expect(got.ACL.Grants[0].Grant.ID).To(Equal("alice"))
			Expect(got.ACL.Grants[0].Grant.Name).To(Equal("Alice"))
			Expect(got.ACL.Grants[0].Grant.Permission).To(Equal(acl.PermRead))
		})
		DescribeTable("reads the last of a repeated header, as RGWEnv::set keeps the last",
			func(ctx SpecContext, name string, values []string, check func(context.Context)) {
				req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/plain", nil)
				for _, v := range values {
					req.Header.Add(name, v)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				check(ctx)
			},
			Entry("x-amz-acl: the private ACL after a public one", "x-amz-acl", []string{"public-read", "private"}, func(ctx context.Context) {
				Expect(bucketACL(ctx)).To(Equal(acl.DefaultPolicy(meta.UserOwner(bob.Info.UserID), "Bob")))
			}),
			Entry("x-amz-grant-read: the grant to alice after one to bob", "x-amz-grant-read", []string{`id="bob"`, `id="alice"`}, func(ctx context.Context) {
				grants := bucketACL(ctx).ACL.Grants
				Expect(grants).To(HaveLen(1))
				Expect(grants[0].Grant.ID).To(Equal("alice"))
			}),
			Entry("x-amz-storage-class: STANDARD after an unknown class", "x-amz-storage-class", []string{"COLD", "STANDARD"}, func(ctx context.Context) {
				b, err := store.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				Expect(b.Info.PlacementRule.StorageClass).To(Equal("STANDARD"))
			}),
			Entry("x-amz-bucket-object-lock-enabled: false after true", "x-amz-bucket-object-lock-enabled", []string{"true", "false"}, func(context.Context) {}),
		)
		It("refuses a canned ACL together with a grant header", func() {
			rec := put("/plain", "", "x-amz-acl", "private", "x-amz-grant-read", `id="alice"`)
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"), "rgw_rest_s3.cc:2412-2414")
		})
		It("answers a grant to an unknown user with radosgw's ENOENT", func() {
			rec := put("/plain", "", "x-amz-grant-read", `id="nobody"`)
			Expect(rec.Code).To(Equal(404))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
		})

		Describe("on Tentacle", func() {
			BeforeEach(func() { setup(denc.Tentacle, "ceph-objectstore") })
			It("accepts a configuration without a LocationConstraint", func() {
				Expect(put("/plain", config("")).Code).To(Equal(200), "v20.2.4 rgw_rest_s3.cc:2644-2649")
			})
			It("names the missing configuration", func() {
				rec := put("/plain", "<Other/>")
				Expect(rec.Code).To(Equal(400))
				Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code><Message>Missing required element CreateBucketConfiguration</Message>"))
			})
			DescribeTable("checks a BucketIndex as radosgw does",
				func(index string, code int, message string) {
					rec := put("/plain", config(index))
					Expect(rec.Code).To(Equal(code), rec.Body.String())
					if message != "" {
						Expect(rec.Body.String()).To(ContainSubstring("<Message>" + message + "</Message>"))
					}
				},
				Entry("without a Type", "<BucketIndex/>", 400, "Missing required element Type in BucketIndex"),
				Entry("an unknown Type", "<BucketIndex><Type>Fifo</Type></BucketIndex>", 400, "Unknown Type in BucketIndex"),
				Entry("NumShards of an indexless index", "<BucketIndex><Type>indexless</Type><NumShards>3</NumShards></BucketIndex>", 400, "NumShards requires Type to be Normal"),
				Entry("NumShards not a number", "<BucketIndex><Type>Normal</Type><NumShards>x</NumShards></BucketIndex>", 400, "Failed to parse integer NumShards in BucketIndex"),
				Entry("NumShards of zero", "<BucketIndex><Type>Normal</Type><NumShards>0</NumShards></BucketIndex>", 400, "NumShards must be greater than 0"),
				Entry("NumShards past rgw_max_dynamic_shards", "<BucketIndex><Type>Normal</Type><NumShards>2000</NumShards></BucketIndex>", 400, "NumShards cannot exceed 1999"),
				Entry("a valid one, which rgw-go does not serve yet", "<BucketIndex><Type>Normal</Type><NumShards>7</NumShards></BucketIndex>", 501, ""),
			)
			It("checks the object-lock header before it refuses a valid one", func() {
				rec := put("/plain", config("<BucketIndex><Type>Normal</Type></BucketIndex>"), "x-amz-bucket-object-lock-enabled", "maybe")
				Expect(rec.Code).To(Equal(400))
				Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
			})
		})
	})

	Describe("DELETE bucket", func() {
		It("answers 204 and removes the bucket", func() {
			Expect(put("/plain", "").Code).To(Equal(200))
			rec := serveReq(h, http.MethodDelete, "/plain", nil)
			Expect(rec.Code).To(Equal(204))
			Expect(rec.Body.String()).To(BeEmpty())
			Expect(serveReq(h, http.MethodHead, "/plain", nil).Code).To(Equal(404))
		})
		It("answers 404 for a missing bucket", func() {
			rec := serveReq(h, http.MethodDelete, "/plain", nil)
			Expect(rec.Code).To(Equal(404))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
		})
	})

	Describe("HEAD bucket", func() {
		BeforeEach(func(ctx SpecContext) {
			Expect(put("/plain", "").Code).To(Equal(200))
			rec, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.Quota = meta.Quota{MaxSize: -1, MaxObjects: 50}
			Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
		})
		// open lets every identity read every bucket, so a non-owner's HEAD succeeds.
		open := func(who *op.UserRecord) *s3.Handler {
			env.Authz = &opfakes.FakeAuthorizer{}
			return as(who)
		}
		It("sends the counts and, to the owner, every quota on Squid", func() {
			rec := serveReq(h, http.MethodHead, "/plain", nil)
			Expect(rec.Code).To(Equal(200))
			hd := rec.Header()
			Expect(hd.Get("X-RGW-Object-Count")).To(Equal("0"))
			Expect(hd.Get("X-RGW-Bytes-Used")).To(Equal("0"))
			Expect(hd.Get("X-RGW-Quota-User-Size")).To(Equal("4096"))
			Expect(hd.Get("X-RGW-Quota-User-Objects")).To(Equal("7"))
			Expect(hd.Get("X-RGW-Quota-Max-Buckets")).To(Equal("1000"))
			Expect(hd.Get("X-RGW-Quota-Bucket-Size")).To(Equal("-1"))
			Expect(hd.Get("X-RGW-Quota-Bucket-Objects")).To(Equal("50"), "rgw_rest_s3.cc:2377-2393")
			Expect(hd.Get("Content-Length")).To(Equal("0"))
		})
		It("sends no quota to another identity", func() {
			rec := serveReq(open(alice), http.MethodHead, "/plain", nil)
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header().Get("X-RGW-Object-Count")).To(Equal("0"))
			for _, k := range []string{"X-RGW-Quota-User-Size", "X-RGW-Quota-Max-Buckets", "X-RGW-Quota-Bucket-Size"} {
				Expect(rec.Header()).NotTo(HaveKey(k))
			}
		})
		Describe("on Tentacle", func() {
			BeforeEach(func(ctx SpecContext) {
				setup(denc.Tentacle, "ceph-objectstore")
				Expect(put("/plain", "").Code).To(Equal(200))
			})
			It("sends the counts only with read-stats, and only the enabled quotas", func(ctx SpecContext) {
				rec := serveReq(h, http.MethodHead, "/plain", nil)
				Expect(rec.Code).To(Equal(200))
				hd := rec.Header()
				Expect(hd).NotTo(HaveKey("X-Rgw-Object-Count"), "v20.2.4 rgw_rest_s3.cc:2497-2499")
				Expect(hd.Get("X-RGW-Quota-Max-Buckets")).To(Equal("1000"))
				Expect(hd.Get("X-RGW-Quota-User-Size")).To(Equal("4096"), "bob's user quota is enabled")
				Expect(hd).NotTo(HaveKey("X-Rgw-Quota-Bucket-Size"), "the bucket quota is not")
				rec = serveReq(h, http.MethodHead, "/plain?read-stats", nil)
				Expect(rec.Header().Get("X-RGW-Object-Count")).To(Equal("0"))
				Expect(rec.Header().Get("X-RGW-Bytes-Used")).To(Equal("0"))
			})
		})
	})

	Describe("GET bucket location", func() {
		BeforeEach(func() { Expect(put("/plain", "").Code).To(Equal(200)) })
		It("names the zonegroup's api name with no Content-Type on Squid", func() {
			rec := serveReq(h, http.MethodGet, "/plain?location", nil)
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Body.String()).To(Equal(locationHead + `ceph-objectstore</LocationConstraint>`))
			Expect(rec.Header().Get("Content-Type")).To(BeEmpty(), "end_header runs before dump_start, rgw_rest_s3.cc:2132-2133")
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
			Expect(rec.Header()).NotTo(HaveKey("X-Rgw-Bucket-Placement-Target"))
		})
		It("escapes the api name as XMLFormatter does", func() {
			setup(denc.Squid, `a&'b`)
			Expect(put("/plain", "").Code).To(Equal(200))
			Expect(serveReq(h, http.MethodGet, "/plain?location", nil).Body.String()).To(Equal(locationHead + `a&amp;&apos;b</LocationConstraint>`))
		})
		It("sends the type and the placement target on Tentacle", func() {
			setup(denc.Tentacle, "ceph-objectstore")
			Expect(put("/plain", "").Code).To(Equal(200))
			rec := serveReq(h, http.MethodGet, "/plain?location", nil)
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Body.String()).To(Equal(locationHead + `ceph-objectstore</LocationConstraint>`))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "v20.2.4 rgw_rest_s3.cc:2235")
			Expect(rec.Header().Get("x-rgw-bucket-placement-target")).To(Equal("default-placement"), "v20.2.4 rgw_rest_s3.cc:2233-2234")
		})
	})
})
