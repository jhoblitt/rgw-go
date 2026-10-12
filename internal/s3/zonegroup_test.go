package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// countingBuckets counts the bucket removals that reach the store.
type countingBuckets struct {
	op.BucketStore
	deletes int
}

func (c *countingBuckets) DeleteBucket(ctx context.Context, rec *op.BucketRecord) error {
	c.deletes++
	return c.BucketStore.DeleteBucket(ctx, rec)
}

var _ = Describe("a bucket whose instance names another zonegroup", func() {
	const (
		localZG   = "zg-east"
		foreignZG = "zg-west"
	)
	var (
		store   *memstore.Store
		env     *op.Env
		buckets *countingBuckets
		alice   *op.UserRecord
		bob     *op.UserRecord
	)
	zonegroup := func(master bool) meta.ZoneGroup {
		return meta.ZoneGroup{ID: localZG, Name: "east", APIName: "east", IsMaster: master}
	}
	policyOf := func(u *op.UserRecord) []byte {
		e := denc.NewEncoder()
		acl.DefaultPolicy(meta.UserOwner(u.Info.UserID), u.Info.DisplayName).Encode(e, denc.Squid)
		return e.Bytes()
	}
	newBucket := func(ctx context.Context, name, zg string) *op.BucketRecord {
		GinkgoHelper()
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
			Name: name, Owner: meta.UserOwner(alice.Info.UserID), Zonegroup: zg,
			Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs:     map[string][]byte{meta.AttrACL: policyOf(alice)},
		})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	putObject := func(ctx context.Context, rec *op.BucketRecord, key string) {
		GinkgoHelper()
		_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader("hello"),
			op.PutParams{Size: 5, Attrs: map[string][]byte{meta.AttrACL: policyOf(alice)}})
		Expect(err).NotTo(HaveOccurred())
	}
	exists := func(ctx context.Context, bucket, key string) bool {
		GinkgoHelper()
		rec, err := store.GetBucket(ctx, "", bucket)
		Expect(err).NotTo(HaveOccurred(), "bucket %s", bucket)
		st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: key})
		Expect(err).NotTo(HaveOccurred(), "%s/%s", bucket, key)
		return st != nil && st.Exists
	}
	setup := func(ctx context.Context, zg meta.ZoneGroup) {
		period := meta.NewPeriod()
		period.ID = "period-1"
		period.PeriodMap.ZoneGroups = map[string]meta.ZoneGroup{
			localZG:   {ID: localZG, Name: "east", APIName: "east", IsMaster: zg.IsMaster},
			foreignZG: {ID: foreignZG, Name: "west", APIName: "west", IsMaster: !zg.IsMaster},
		}
		store = memstore.New(memstore.Config{ZoneGroup: zg, Period: period})
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll, MaxBuckets: 1000})
		bob = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll, MaxBuckets: 1000})
		env = testEnv(store)
		buckets = &countingBuckets{BucketStore: store}
		env.Buckets = buckets
		putObject(ctx, newBucket(ctx, "far", foreignZG), "k")
		putObject(ctx, newBucket(ctx, "near", localZG), "src")
	}
	BeforeEach(func(ctx SpecContext) { setup(ctx, zonegroup(true)) })
	sendWith := func(cfg s3.Config, auth s3.Authenticator, method, target string, hdr ...string) *httptest.ResponseRecorder {
		return serveReq(s3.NewHandler(env, auth, testConfig(cfg)), method, target, nil, hdr...)
	}
	send := func(auth s3.Authenticator, method, target string, hdr ...string) *httptest.ResponseRecorder {
		return sendWith(s3.Config{}, auth, method, target, hdr...)
	}
	expectRedirect := func(rec *httptest.ResponseRecorder) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(http.StatusMovedPermanently), rec.Body.String())
		Expect(rec.Body.String()).To(BeEmpty(), "rgw_err::is_err takes 301 for no error, so end_header dumps no document")
	}
	systemAs := func(u *op.UserRecord) s3.Authenticator {
		return s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{
				User: &u.Info, Owner: meta.UserOwner(u.Info.UserID), OpMask: u.Info.OpMask, System: true, Admin: true,
			}}, nil
		})
	}
	anonymous := func() s3.Authenticator {
		return s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Anonymous()}, nil
		})
	}

	Describe("the redirect", func() {
		It("answers DeleteBucket 301 without reaching the store, and keeps the bucket and its objects", func(ctx SpecContext) {
			expectRedirect(send(authAs(alice), http.MethodDelete, "/far"))
			Expect(buckets.deletes).To(BeZero(), "rgw_build_bucket_policies refuses before the op runs")
			Expect(exists(ctx, "far", "k")).To(BeTrue())
		})
		DescribeTable("answers every op that loads the bucket 301 before it reads or writes",
			func(ctx SpecContext, method, target, body string, hdr []string, check func(context.Context)) {
				var b io.Reader
				if body != "" {
					b = strings.NewReader(body)
				}
				expectRedirect(serveReq(s3.NewHandler(env, authAs(alice), testConfig(s3.Config{})), method, target, b, hdr...))
				if check != nil {
					check(ctx)
				}
			},
			Entry("GetObject", http.MethodGet, "/far/k", "", nil, nil),
			Entry("HeadObject", http.MethodHead, "/far/k", "", nil, nil),
			Entry("GetObjectAttributes", http.MethodGet, "/far/k?attributes", "", []string{"X-Amz-Object-Attributes", "ETag"}, nil),
			Entry("ListObjects", http.MethodGet, "/far", "", nil, nil),
			Entry("ListObjectsV2", http.MethodGet, "/far?list-type=2", "", nil, nil),
			Entry("HeadBucket", http.MethodHead, "/far", "", nil, nil),
			Entry("GetBucketAcl", http.MethodGet, "/far?acl", "", nil, nil),
			Entry("PutBucketAcl", http.MethodPut, "/far?acl", "", []string{"X-Amz-Acl", "private"}, nil),
			Entry("GetBucketPolicy", http.MethodGet, "/far?policy", "", nil, nil),
			Entry("PutBucketPolicy", http.MethodPut, "/far?policy", `{"Version":"2012-10-17","Statement":[]}`, nil, nil),
			Entry("DeleteBucketPolicy", http.MethodDelete, "/far?policy", "", nil, nil),
			Entry("GetBucketTagging", http.MethodGet, "/far?tagging", "", nil, nil),
			Entry("PutBucketTagging", http.MethodPut, "/far?tagging", "<Tagging><TagSet></TagSet></Tagging>", nil, nil),
			Entry("DeleteBucketTagging", http.MethodDelete, "/far?tagging", "", nil, nil),
			Entry("GetBucketLifecycle", http.MethodGet, "/far?lifecycle", "", nil, nil),
			Entry("PutObject, which writes nothing", http.MethodPut, "/far/new", "data", nil, func(ctx context.Context) {
				rec, err := store.GetBucket(ctx, "", "far")
				Expect(err).NotTo(HaveOccurred())
				st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: "new"})
				Expect(err).NotTo(HaveOccurred())
				Expect(st == nil || !st.Exists).To(BeTrue(), "new was written")
			}),
			Entry("DeleteObject, which keeps the object", http.MethodDelete, "/far/k", "", nil, func(ctx context.Context) {
				Expect(exists(ctx, "far", "k")).To(BeTrue())
			}),
			Entry("DeleteObjects, which keeps the object", http.MethodPost, "/far?delete",
				"<Delete><Object><Key>k</Key></Object></Delete>", nil, func(ctx context.Context) {
					Expect(exists(ctx, "far", "k")).To(BeTrue())
				}),
			Entry("GetObjectAcl", http.MethodGet, "/far/k?acl", "", nil, nil),
			Entry("PutObjectAcl", http.MethodPut, "/far/k?acl", "", []string{"X-Amz-Acl", "private"}, nil),
			Entry("GetObjectTagging", http.MethodGet, "/far/k?tagging", "", nil, nil),
			Entry("PutObjectTagging", http.MethodPut, "/far/k?tagging", "<Tagging><TagSet></TagSet></Tagging>", nil, nil),
			Entry("DeleteObjectTagging", http.MethodDelete, "/far/k?tagging", "", nil, nil),
			Entry("CreateMultipartUpload, which starts no upload", http.MethodPost, "/far/k?uploads", "", nil, func(ctx context.Context) {
				rec, err := store.GetBucket(ctx, "", "far")
				Expect(err).NotTo(HaveOccurred())
				res, err := store.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 10})
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Uploads).To(BeEmpty(), "an upload was started")
			}),
			Entry("UploadPart", http.MethodPut, "/far/k?partNumber=1&uploadId=u1", "data", nil, nil),
			Entry("UploadPartCopy, for which init records no copy source", http.MethodPut, "/far/k?partNumber=1&uploadId=u1", "",
				[]string{"X-Amz-Copy-Source", "near/src"}, nil),
			Entry("CompleteMultipartUpload", http.MethodPost, "/far/k?uploadId=u1",
				"<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>e</ETag></Part></CompleteMultipartUpload>", nil, nil),
			Entry("AbortMultipartUpload", http.MethodDelete, "/far/k?uploadId=u1", "", nil, nil),
			Entry("ListParts", http.MethodGet, "/far/k?uploadId=u1", "", nil, nil),
			Entry("ListMultipartUploads", http.MethodGet, "/far?uploads", "", nil, nil),
			Entry("GetObject naming a copy source this zonegroup holds, which only a PUT may", http.MethodGet, "/far/k", "",
				[]string{"X-Amz-Copy-Source", "near/src"}, nil),
			Entry("DeleteObject naming a copy source this zonegroup holds, which keeps the object", http.MethodDelete, "/far/k", "",
				[]string{"X-Amz-Copy-Source", "near/src"}, func(ctx context.Context) {
					Expect(exists(ctx, "far", "k")).To(BeTrue())
				}),
		)
		It("redirects before the permission check, so a requester the bucket refuses learns where it lives", func() {
			expectRedirect(send(authAs(bob), http.MethodDelete, "/far"))
			expectRedirect(send(anonymous(), http.MethodGet, "/far/k"))
			Expect(buckets.deletes).To(BeZero())
		})
		It("charges a requester-pays bucket's 301 to a requester who does not own it, as end_header does for a non-error", func(ctx SpecContext) {
			rec, err := store.GetBucket(ctx, "", "far")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.RequesterPays = true
			Expect(store.PutBucketInfo(ctx, rec)).To(Succeed())
			res := send(authAs(bob), http.MethodGet, "/far/k")
			expectRedirect(res)
			Expect(exactHeader(res.Header(), "x-amz-request-charged")).To(Equal("requester"))
		})
		DescribeTable("sends Location as abort_early builds it from this zonegroup's endpoint",
			func(ctx SpecContext, edit func(*meta.ZoneGroup), cfg s3.Config, target, want string) {
				zg := zonegroup(true)
				edit(&zg)
				setup(ctx, zg)
				res := sendWith(cfg, authAs(alice), http.MethodGet, target)
				expectRedirect(res)
				if want == "" {
					Expect(res.Header()).NotTo(HaveKey("Location"))
					return
				}
				Expect(res.Header().Get("Location")).To(Equal(want))
			},
			Entry("the zonegroup's first endpoint, less one trailing slash, then the path and the query as sent",
				func(zg *meta.ZoneGroup) { zg.Endpoints = []string{"http://east.example.com:8000/", "http://other"} },
				s3.Config{}, "/far?prefix=a%2Fb&list-type=2", "http://east.example.com:8000/far?prefix=a%2Fb&list-type=2"),
			Entry("the master zone's first endpoint when the zonegroup lists none",
				func(zg *meta.ZoneGroup) {
					zg.MasterZone = "z1"
					zg.Zones = map[string]meta.Zone{"z1": {ID: "z1", Name: "z1", Endpoints: []string{"https://z1.example.com"}}}
				},
				s3.Config{}, "/far/k%20v", "https://z1.example.com/far/k%20v"),
			Entry("a virtual-hosted request's bucket in front of its path",
				func(zg *meta.ZoneGroup) { zg.Endpoints = []string{"http://east.example.com"} },
				s3.Config{DNSNames: []string{"example.com"}}, "http://far.example.com/k?x=1", "http://east.example.com/far/k?x=1"),
			Entry("the path as the client sent it, a byte net/url would escape kept",
				func(zg *meta.ZoneGroup) { zg.Endpoints = []string{"http://east.example.com"} },
				s3.Config{}, "/far/a|b", "http://east.example.com/far/a|b"),
			Entry("none when neither the zonegroup nor its master zone has an endpoint",
				func(*meta.ZoneGroup) {}, s3.Config{}, "/far/k", ""),
		)
	})

	Describe("a bucket with an empty zonegroup id", func() {
		BeforeEach(func(ctx SpecContext) {
			putObject(ctx, newBucket(ctx, "legacy", ""), "k")
		})
		It("is served on the master zonegroup, which RGWZoneGroup::equals takes it for", func(ctx SpecContext) {
			Expect(send(authAs(alice), http.MethodGet, "/legacy/k").Code).To(Equal(http.StatusOK))
			res := send(authAs(alice), http.MethodDelete, "/legacy")
			Expect(res.Code).To(Equal(http.StatusConflict), "the delete reaches the store, which finds an object")
			Expect(res.Body.String()).To(ContainSubstring("<Code>BucketNotEmpty</Code>"))
			Expect(exists(ctx, "legacy", "k")).To(BeTrue())
		})
		It("is redirected from a zonegroup that is not the master", func(ctx SpecContext) {
			setup(ctx, zonegroup(false))
			putObject(ctx, newBucket(ctx, "legacy", ""), "k")
			expectRedirect(send(authAs(alice), http.MethodGet, "/legacy/k"))
			expectRedirect(send(authAs(alice), http.MethodDelete, "/legacy"))
			Expect(buckets.deletes).To(BeZero())
		})
	})

	Describe("the exceptions", func() {
		It("answers GetBucketLocation with the bucket's zonegroup", func() {
			res := send(authAs(alice), http.MethodGet, "/far?location")
			Expect(res.Code).To(Equal(http.StatusOK), res.Body.String())
			Expect(res.Body.String()).To(ContainSubstring(">west</LocationConstraint>"))
		})
		It("serves a system request on the master zonegroup", func() {
			Expect(send(systemAs(alice), http.MethodGet, "/far/k").Code).To(Equal(http.StatusOK))
		})
		It("refuses a system request's DeleteBucket on the master zonegroup in the store, which the redirect lets through", func(ctx SpecContext) {
			newBucket(ctx, "void", foreignZG)
			expectRedirect(send(systemAs(alice), http.MethodDelete, "/void"))
			Expect(buckets.deletes).To(Equal(1), "the delete reached the store")
			_, err := store.GetBucket(ctx, "", "void")
			Expect(err).NotTo(HaveOccurred(), "the empty bucket stays")
		})
		It("redirects a system request on a zonegroup that is not the master", func(ctx SpecContext) {
			setup(ctx, zonegroup(false))
			expectRedirect(send(systemAs(alice), http.MethodGet, "/far/k"))
		})
		It("serves a PUT of an object whose copy source this zonegroup holds", func(ctx SpecContext) {
			res := send(authAs(alice), http.MethodPut, "/far/copied", "X-Amz-Copy-Source", "near/src")
			Expect(res.Code).To(Equal(http.StatusOK), res.Body.String())
			Expect(exists(ctx, "far", "copied")).To(BeTrue())
		})
		DescribeTable("redirects a PUT whose copy source does not place the request here",
			func(target, src string) {
				expectRedirect(send(authAs(alice), http.MethodPut, target, "X-Amz-Copy-Source", src))
			},
			Entry("a source in another zonegroup", "/far/copied", "far/k"),
			Entry("a source bucket that does not exist", "/far/copied", "missing/src"),
			Entry("a PUT that names no object", "/far?acl", "near/src"),
		)
	})
})
