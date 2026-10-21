package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/aws/aws-sdk-go-v2/aws"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// radosgw's CreateBucket reads its object lock from x_meta_map, under any of
// the meta prefixes init_meta_info renames to x-amz-
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2542 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2695 at v20.2.4);
// rgw-go's reads X-Amz- alone.
var _ = Describe("CreateBucket's object lock under another meta prefix", func() {
	var (
		store *memstore.Store
		h     *s3.Handler
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", nil))
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		h = s3.NewHandler(env, auth.New(cfg, store, store), s3.Config{})
	})

	serve := func(req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// creates is a CreateBucket of /newbkt as alice carrying name: true, by each
	// scheme and route, by its description. A SigV2 request signs amz, its
	// x-amz- lines; SigV4 signs the header on its header route and leaves it
	// unsigned on a presigned URL, which signs host alone.
	creates := func(ctx context.Context, name, amz string) map[string]*http.Request {
		v2 := headerSignedV2(ctx, http.MethodPut, "/newbkt", "PUT\n\n\n"+v2Date+"\n"+amz+"/newbkt")
		pre2 := aliceV2(ctx, http.MethodPut, "/newbkt", "", nil, "PUT\n\n\n"+v2Expires+"\n"+amz+"/newbkt")
		pre2.Body, pre2.ContentLength = http.NoBody, 0
		v4 := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/newbkt", nil)
		v4.Header.Set(name, "true")
		signedAs(ctx, v4, "alice")
		creds := aws.Credentials{AccessKeyID: accessKeyOf("alice"), SecretAccessKey: secretOf("alice")}
		url, _, err := sdkSigner().PresignHTTP(ctx, creds,
			httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/newbkt?X-Amz-Expires=300", nil),
			"UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())
		Expect(err).NotTo(HaveOccurred())
		pre4 := httptest.NewRequestWithContext(ctx, http.MethodPut, url, nil)
		for _, req := range []*http.Request{v2, pre2, pre4} {
			req.Header.Set(name, "true")
		}
		return map[string]*http.Request{"SigV2": v2, "presigned SigV2": pre2, "SigV4": v4, "presigned SigV4": pre4}
	}

	DescribeTable("creates the bucket without object lock, on both schemes and routes",
		func(ctx SpecContext, prefix string) {
			name := prefix + "Bucket-Object-Lock-Enabled"
			for how, req := range creates(ctx, name, "") {
				code, body := serve(req)
				Expect(code).To(Equal(http.StatusOK), "%s %s: %s", how, name, body)
				rec, err := store.GetBucket(ctx, "", "newbkt")
				Expect(err).NotTo(HaveOccurred(), "%s %s", how, name)
				Expect(rec.Info.ObjLockEnabled()).To(BeFalse(), "%s %s", how, name)
				Expect(rec.Info.ObjLock).To(BeNil(), "%s %s", how, name)
				Expect(store.DeleteBucket(ctx, rec)).To(Succeed())
			}
		},
		Entry("X-Goog-", "X-Goog-"),
		Entry("X-Dho-", "X-Dho-"),
		Entry("X-Rgw-", "X-Rgw-"),
		Entry("X-Object-", "X-Object-"),
		Entry("X-Container-", "X-Container-"),
		Entry("X-Account-", "X-Account-"),
	)

	// A presigned SigV4 URL signs host alone, so an X-Amz- header sent with
	// one is refused unsigned before CreateBucket reads it.
	It("reads X-Amz-Bucket-Object-Lock-Enabled on SigV2's routes and SigV4's header route, creating the bucket with object lock", func(ctx SpecContext) {
		reqs := creates(ctx, "X-Amz-Bucket-Object-Lock-Enabled", "x-amz-bucket-object-lock-enabled:true\n")
		for _, how := range []string{"SigV2", "presigned SigV2", "SigV4"} {
			code, body := serve(reqs[how])
			Expect(code).To(Equal(http.StatusOK), "%s: %s", how, body)
			rec, err := store.GetBucket(ctx, "", "newbkt")
			Expect(err).NotTo(HaveOccurred(), how)
			Expect(rec.Info.ObjLockEnabled()).To(BeTrue(), how)
			Expect(store.DeleteBucket(ctx, rec)).To(Succeed())
		}
	})
})
