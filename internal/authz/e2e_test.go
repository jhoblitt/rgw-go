package authz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// The evaluator behind the S3 handler: op.Run calls it from ListBuckets'
// VerifyPermission, and the handler renders its refusal.
var _ = Describe("the authorizer behind the S3 handler", func() {
	const hostID = "4155-z-zg"
	var (
		store *memstore.Store
		bob   *op.UserRecord
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid})
		bob = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "bobs", Owner: meta.UserOwner(bob.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
	})

	// listAs sends GET / through a handler whose authorizer is authz's and
	// whose authenticator answers id for every request.
	listAs := func(ctx context.Context, id op.Identity) *httptest.ResponseRecorder {
		env := &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, Usage: store, Metadata: store, Metrics: op.NopMetrics{},
			Authz: authz.New(authz.DefaultConfig(denc.Squid)),
			Now:   func() time.Time { return time.Unix(0x68d7a1b2, 0) }, HostID: hostID,
		}
		as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: id}, nil
		})
		h := s3.NewHandler(env, as, s3.Config{TransIDSuffix: "-4155-z", ServerHeader: "Ceph Object Gateway (squid)"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
		return rec
	}
	bobID := func() op.Identity {
		info := bob.Info
		return op.Identity{User: &info, Owner: meta.UserOwner(info.UserID), OpMask: info.OpMask}
	}
	denyList := func() string { return doc(stmt("Deny", "", "s3:ListAllMyBuckets", "*", "")) }
	expectAccessDenied := func(rec *httptest.ResponseRecorder) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(403))
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message></Message>` +
			`<RequestId>` + rec.Header().Get("x-amz-request-id") + `</RequestId><HostId>` + hostID + `</HostId></Error>`))
	}
	expectListed := func(rec *httptest.ResponseRecorder) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring("<Bucket><Name>bobs</Name>"))
	}

	It("refuses ListAllMyBuckets that the user's own policy denies", func(ctx SpecContext) {
		expectAccessDenied(listAs(ctx, withUserPolicy(bobID(), denyList())))
	})
	It("lists the buckets once the Deny is gone", func(ctx SpecContext) {
		expectListed(listAs(ctx, withUserPolicy(bobID(), doc(stmt("Allow", "", "s3:ListAllMyBuckets", "*", "")))))
		expectListed(listAs(ctx, bobID()))
	})
	It("lets an admin through the same Deny, as op.Run overrides an access denial", func(ctx SpecContext) {
		expectListed(listAs(ctx, adminOf(withUserPolicy(bobID(), denyList()))))
	})
	Describe("an IAM group member, whose group policies rgw-go does not evaluate", func() {
		member := func() op.Identity {
			id := bobID()
			id.User.GroupIDs = []string{"g1"}
			return id
		}
		It("is refused", func(ctx SpecContext) {
			expectAccessDenied(listAs(ctx, member()))
		})
		It("is served as an admin, the override applying as to any policy denial", func(ctx SpecContext) {
			expectListed(listAs(ctx, adminOf(member())))
		})
	})
})
