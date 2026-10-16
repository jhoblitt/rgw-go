package authz_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
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
			`<RequestId>` + exactHeader(rec.Header(), "x-amz-request-id") + `</RequestId><HostId>` + hostID + `</HostId></Error>`))
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

// The anonymous writer's own rights through the S3 handler: op's
// BlockPublicAcls check of a write judges the ACL authz.DefaultACL builds.
var _ = Describe("an anonymous writer behind the S3 handler", func() {
	var (
		store *memstore.Store
		h     *s3.Handler
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid})
		bob := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
		owner := meta.UserOwner(bob.Info.UserID)
		pol, err := acl.Canned(acl.Owner{ID: "bob", DisplayName: "Bob"}, acl.Owner{ID: "bob", DisplayName: "Bob"}, "public-read-write")
		Expect(err).NotTo(HaveOccurred())
		_, err = store.CreateBucket(ctx, op.CreateBucketParams{
			Name: "pubw", Owner: owner, Attrs: map[string][]byte{meta.AttrACL: aclAttr(pol)},
		})
		Expect(err).NotTo(HaveOccurred())
		env := &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, Usage: store, Metadata: store, Metrics: op.NopMetrics{},
			Authz: authz.New(authz.DefaultConfig(denc.Squid)),
			Now:   func() time.Time { return time.Unix(0x68d7a1b2, 0) }, HostID: "4155-z-zg",
		}
		anonymous := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Anonymous()}, nil
		})
		h = s3.NewHandler(env, anonymous, s3.Config{TransIDSuffix: "-4155-z", ServerHeader: "Ceph Object Gateway (squid)"})
	})

	block := func(ctx context.Context, b acl.PublicAccessBlock) {
		GinkgoHelper()
		rec, err := store.GetBucket(ctx, "", "pubw")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{attrPublicAccess: pabAttr(b)}, nil)).To(Succeed())
	}
	send := func(ctx context.Context, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req := httptest.NewRequestWithContext(ctx, method, target, r)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	It("writes under BlockPublicAcls unless the request names a public ACL", func(ctx SpecContext) {
		block(ctx, acl.PublicAccessBlock{BlockPublicACLs: true})
		rec := send(ctx, http.MethodPut, "/pubw/canned", "x")
		Expect(rec.Code).To(Equal(200), "no ACL header, its canned FULL_CONTROL its own: %s", rec.Body.String())
		rec = send(ctx, http.MethodPut, "/pubw/owned", "x", "X-Amz-Grant-Read", `id="bob"`)
		Expect(rec.Code).To(Equal(200), "a grant header, its ownership its own: %s", rec.Body.String())
		rec = send(ctx, http.MethodPut, "/pubw/public", "x", "X-Amz-Acl", "public-read")
		Expect(rec.Code).To(Equal(403), "a public canned ACL: %s", rec.Body.String())
	})

	It("refuses under BlockPublicAcls to rewrite the ACL of an object the anonymous user owns", func(ctx SpecContext) {
		rec := send(ctx, http.MethodPut, "/pubw/owned", "x", "X-Amz-Grant-Read", `id="bob"`)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		rec = send(ctx, http.MethodPut, "/pubw/owned?acl", "", "Content-Length", "0", "X-Amz-Grant-Read", `id="bob"`)
		Expect(rec.Code).To(Equal(200), "no block: %s", rec.Body.String())
		block(ctx, acl.PublicAccessBlock{BlockPublicACLs: true})
		rec = send(ctx, http.MethodPut, "/pubw/owned?acl", "", "Content-Length", "0", "X-Amz-Grant-Read", `id="bob"`)
		Expect(rec.Code).To(Equal(403), "an ACL the anonymous user owns is public: %s", rec.Body.String())
	})

	It("refuses under IgnorePublicAcls what an anonymous write gave the anonymous user", func(ctx SpecContext) {
		Expect(send(ctx, http.MethodPut, "/pubw/canned", "x").Code).To(Equal(200))
		Expect(send(ctx, http.MethodPut, "/pubw/owned", "x", "X-Amz-Grant-Read", `id="bob"`).Code).To(Equal(200))
		Expect(send(ctx, http.MethodGet, "/pubw/canned", "").Code).To(Equal(200), "GetObject before the block")
		Expect(send(ctx, http.MethodGet, "/pubw/owned?acl", "").Code).To(Equal(200), "GetObjectAcl before the block")
		block(ctx, acl.PublicAccessBlock{IgnorePublicACLs: true})
		Expect(send(ctx, http.MethodGet, "/pubw/canned", "").Code).To(Equal(403), "GetObject")
		Expect(send(ctx, http.MethodGet, "/pubw/canned?acl", "").Code).To(Equal(403), "GetObjectAcl through the canned grant")
		Expect(send(ctx, http.MethodGet, "/pubw/owned?acl", "").Code).To(Equal(403), "GetObjectAcl through ownership")
		Expect(send(ctx, http.MethodPut, "/pubw/another", "x").Code).To(Equal(403), "PutObject, the bucket's public grant ignored")
	})
})

// exactHeader is the first value the handler's header map h holds under
// exactly name, or "" when there is none. net/http writes a key as the
// handler spelled it; http.Header.Get would look up the canonical spelling
// instead.
func exactHeader(h http.Header, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}
