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
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// subresWorld is the memstore the bucket subresource specs run on: alice's
// bucket "plain", whose ACL is her private default under "Alice", and bob,
// who owns nothing, authorized by the policy evaluator.
type subresWorld struct {
	store      *memstore.Store
	env        *op.Env
	alice, bob *op.UserRecord
}

func newSubresWorld(ctx context.Context, rel denc.Release) *subresWorld {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: rel})
	w := &subresWorld{store: store}
	w.alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	w.bob = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
	w.env = testEnv(store)
	w.env.Authz = authz.New(authz.DefaultConfig(rel))
	owner := meta.UserOwner(w.alice.Info.UserID)
	_, err := store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "plain", Owner: owner, Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Alice"))},
	})
	Expect(err).NotTo(HaveOccurred())
	return w
}

// send serves method and target as who; hdr alternates header names and
// values.
func (w *subresWorld) send(who *op.UserRecord, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	var b io.Reader
	if body != "" {
		b = strings.NewReader(body)
	}
	return serveReq(s3.NewHandler(w.env, authAs(who), testConfig(s3.Config{})), method, target, b, hdr...)
}

// sendTampered serves a PUT of body whose payload check fails where the
// body ends, as the authenticator's verifying reader reports a body whose
// hash does not match x-amz-content-sha256.
func (w *subresWorld) sendTampered(target, body string) *httptest.ResponseRecorder {
	auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
		return &op.AuthResult{
			Identity:      op.Identity{User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: w.alice.Info.OpMask},
			Body:          &failingBody{r: strings.NewReader(body), err: op.ErrContentSHA256Mismatch},
			ContentLength: int64(len(body)),
		}, nil
	})
	return serveReq(s3.NewHandler(w.env, auth, testConfig(s3.Config{})), http.MethodPut, target, strings.NewReader(body))
}

// bucket is plain as stored.
func (w *subresWorld) bucket(ctx context.Context) *op.BucketRecord {
	GinkgoHelper()
	rec, err := w.store.GetBucket(ctx, "", "plain")
	Expect(err).NotTo(HaveOccurred())
	return rec
}

// setBucketAttrs sets attrs on plain.
func (w *subresWorld) setBucketAttrs(ctx context.Context, set map[string][]byte) {
	GinkgoHelper()
	Expect(w.store.PutBucketAttrs(ctx, w.bucket(ctx), set, nil)).To(Succeed())
}

func encodeACL(p acl.Policy) []byte {
	e := denc.NewEncoder()
	p.Encode(e, denc.Squid)
	return e.Bytes()
}

func encodePublicAccess(b acl.PublicAccessBlock) []byte {
	e := denc.NewEncoder()
	b.Encode(e, denc.Squid)
	return e.Bytes()
}

// expectEmptyXML checks rec is status with Content-Type: application/xml and
// no body: radosgw's end_header names the type and flushes the formatter
// before dump_start puts the declaration in it, which nothing flushes after
// (rgw_rest_s3.cc:3649-3656 and :915-935 at v19.2.6, :3932-3939 and
// :997-1017 at v20.2.4; rgw_rest.cc:589-655 at v19.2.6).
func expectEmptyXML(rec *httptest.ResponseRecorder, status int) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(status), rec.Body.String())
	Expect(rec.Body.String()).To(BeEmpty())
	Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
	Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
	Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
}

const allUsersRead = `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant>`

var _ = Describe("bucket get_acls and put_acls", func() {
	var w *subresWorld
	BeforeEach(func(ctx SpecContext) { w = newSubresWorld(ctx, denc.Squid) })

	aclDoc := func(owner string, grants ...string) string {
		return `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>` + owner +
			`</ID></Owner><AccessControlList>` + strings.Join(grants, "") + `</AccessControlList></AccessControlPolicy>`
	}
	userGrant := func(id, perm string) string {
		return `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` + id +
			`</ID></Grantee><Permission>` + perm + `</Permission></Grant>`
	}
	storedACL := func(ctx context.Context) acl.Policy {
		GinkgoHelper()
		p, err := op.BucketACLFor(ctx, w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	private := func() acl.Policy { return acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice") }

	Describe("GET ?acl", func() {
		It("renders the bucket's ACL as application/xml with the frontend's length", func() {
			rec := w.send(w.alice, http.MethodGet, "/plain?acl", "")
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "end_header names no length, rgw_rest_s3.cc:3610 at v19.2.6")
			Expect(rec.Body.String()).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner>`))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		})
		It("answers HEAD ?acl with the GET's headers alone", func() {
			get := w.send(w.alice, http.MethodGet, "/plain?acl", "")
			rec := w.send(w.alice, http.MethodHead, "/plain?acl", "")
			Expect(rec.Code).To(Equal(200))
			Expect(rec.Body.Len()).To(BeZero())
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(get.Body.Len())))
		})
		It("answers a missing bucket 404 and a requester the bucket's ACL refuses 403", func() {
			rec := w.send(w.bob, http.MethodGet, "/missing?acl", "")
			Expect(rec.Code).To(Equal(404))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
			Expect(w.send(w.bob, http.MethodGet, "/plain?acl", "").Code).To(Equal(403))
		})
	})

	Describe("PUT ?acl", func() {
		It("applies a canned ACL and answers 200 with no body", func(ctx SpecContext) {
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?acl", "", "x-amz-acl", "public-read", "Content-Length", "0"), 200)
			Expect(w.send(w.alice, http.MethodGet, "/plain?acl", "").Body.String()).To(ContainSubstring(allUsersRead))
			Expect(storedACL(ctx).IsPublic()).To(BeTrue())
		})
		It("ignores a bucket-owner canned ACL, which builds the private one", func(ctx SpecContext) {
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?acl", "", "x-amz-acl", "public-read", "Content-Length", "0"), 200)
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?acl", "", "x-amz-acl", "bucket-owner-read", "Content-Length", "0"), 200)
			Expect(w.bucket(ctx).Attrs[meta.AttrACL]).To(Equal(encodeACL(private())), "rgw_rest_s3.cc:3640-3644 at v19.2.6")
		})
		It("applies an AccessControlPolicy document", func(ctx SpecContext) {
			doc := aclDoc("alice", userGrant("alice", "FULL_CONTROL"), userGrant("bob", "READ"))
			expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain?acl", doc), 200)
			Expect(w.send(w.bob, http.MethodGet, "/plain?acl", "").Code).To(Equal(403), "READ is not READ_ACP")
			Expect(storedACL(ctx).ACL.Grants).To(ContainElement(HaveField("Grant.ID", "bob")))
		})
		It("refuses a body past rgw_max_put_param_size as MalformedXML with radosgw's message, unread", func(ctx SpecContext) {
			rec := w.send(w.alice, http.MethodPut, "/plain?acl", strings.Repeat("x", 2<<20))
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>MalformedXML</Code><Message>The XML you provided was larger than the maximum 1048576 bytes allowed.</Message>"),
				"rgw_op.cc:5830-5837 at v19.2.6")
			w.env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_max_put_param_size": "16"})
			rec = w.send(w.alice, http.MethodPut, "/plain?acl", aclDoc("alice"))
			Expect(rec.Body.String()).To(ContainSubstring("<Message>The XML you provided was larger than the maximum 16 bytes allowed.</Message>"))
			Expect(storedACL(ctx)).To(Equal(private()))
		})
		It("refuses a canned ACL beside a body with InvalidArgument", func(ctx SpecContext) {
			rec := w.send(w.alice, http.MethodPut, "/plain?acl", aclDoc("alice"), "x-amz-acl", "public-read")
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"), "rgw_op.cc:5844-5847 at v19.2.6")
			Expect(storedACL(ctx)).To(Equal(private()))
		})
		It("reads a chunked ACL document whole", func(ctx SpecContext) {
			doc := aclDoc("alice", userGrant("alice", "FULL_CONTROL"), userGrant("bob", "READ"))
			expectEmptyXML(serveReq(s3.NewHandler(w.env, authAs(w.alice), testConfig(s3.Config{})), http.MethodPut, "/plain?acl",
				io.MultiReader(strings.NewReader(doc))), 200)
			Expect(storedACL(ctx).ACL.Grants).To(ContainElement(HaveField("Grant.ID", "bob")))
		})
		It("answers 409 ConcurrentModification when every try loses a race, where radosgw answers 200", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = w.store.GetBucket
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			w.env.Buckets = buckets
			rec := w.send(w.alice, http.MethodPut, "/plain?acl", "", "x-amz-acl", "public-read", "Content-Length", "0")
			Expect(rec.Code).To(Equal(409), "rgw_op.cc:5925-5927 at v19.2.6")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>ConcurrentModification</Code>"))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16))
			Expect(storedACL(ctx)).To(Equal(private()))
		})
		It("refuses a request without a Content-Length with MissingContentLength", func() {
			rec := w.send(w.alice, http.MethodPut, "/plain?acl", "", "x-amz-acl", "public-read")
			Expect(rec.Code).To(Equal(411), "the length-required tolerance at rgw_rest_s3.cc:3624-3632 at v19.2.6 never holds")
			Expect(rec.Body.String()).To(ContainSubstring("<Code>MissingContentLength</Code>"))
		})
		It("refuses to change the owner", func(ctx SpecContext) {
			rec := w.send(w.alice, http.MethodPut, "/plain?acl", aclDoc("bob", userGrant("bob", "FULL_CONTROL")))
			Expect(rec.Code).To(Equal(403))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>AccessDenied</Code><Message>Cannot modify ACL Owner</Message>"))
			Expect(storedACL(ctx)).To(Equal(private()))
		})
		It("refuses a public ACL under a block of public ACLs", func(ctx SpecContext) {
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			rec := w.send(w.alice, http.MethodPut, "/plain?acl", "", "x-amz-acl", "public-read", "Content-Length", "0")
			Expect(rec.Code).To(Equal(403))
			Expect(storedACL(ctx)).To(Equal(private()))
		})
		It("refuses a requester the bucket's ACL refuses before it reads the body", func(ctx SpecContext) {
			rec := w.send(w.bob, http.MethodPut, "/plain?acl", strings.Repeat("x", 2<<20))
			Expect(rec.Code).To(Equal(403), "verify_permission runs before execute's get_params")
			Expect(storedACL(ctx)).To(Equal(private()))
		})
		It("refuses a body whose payload hash does not match, changing nothing", func(ctx SpecContext) {
			rec := w.sendTampered("/plain?acl", aclDoc("alice", userGrant("alice", "FULL_CONTROL"), userGrant("bob", "FULL_CONTROL")))
			Expect(rec.Code).To(Equal(400))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
			Expect(storedACL(ctx)).To(Equal(private()))
		})
	})
})
