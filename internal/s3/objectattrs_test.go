package s3_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// taggingDoc is a Tagging document of n tags, k0=v0 onwards.
func taggingDoc(n int) string {
	var b strings.Builder
	b.WriteString("<Tagging><TagSet>")
	for i := range n {
		fmt.Fprintf(&b, "<Tag><Key>k%d</Key><Value>v%d</Value></Tag>", i, i)
	}
	b.WriteString("</TagSet></Tagging>")
	return b.String()
}

var _ = Describe("object put_acls", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	aclDoc := func(owner string, grants ...string) string {
		return `<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>` + owner +
			`</ID></Owner><AccessControlList>` + strings.Join(grants, "") + `</AccessControlList></AccessControlPolicy>`
	}
	userGrant := func(id, perm string) string {
		return `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` + id +
			`</ID></Grantee><Permission>` + perm + `</Permission></Grant>`
	}
	private := func() acl.Policy { return acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice") }
	storedACL := func(ctx context.Context) acl.Policy {
		GinkgoHelper()
		p, err := op.ObjectACLFor(ctx, w.stat(ctx, "src"), w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	It("applies a canned ACL and answers 200 with no body", func(ctx SpecContext) {
		expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain/src?acl", "", "x-amz-acl", "public-read", "Content-Length", "0"), 200)
		Expect(w.send(w.alice, http.MethodGet, "/plain/src?acl", "").Body.String()).To(ContainSubstring(allUsersRead))
	})
	It("applies a bucket-owner canned ACL at object scope, which a bucket's ACL ignores", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		Expect(w.send(w.bob, http.MethodPut, "/plain/bobs", "x").Code).To(Equal(200))
		expectEmptyXML(w.send(w.bob, http.MethodPut, "/plain/bobs?acl", "", "x-amz-acl", "bucket-owner-read", "Content-Length", "0"), 200)
		p, err := op.ObjectACLFor(ctx, w.stat(ctx, "bobs"), w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.ACL.Grants).To(ContainElement(SatisfyAll(HaveField("Grant.ID", "alice"), HaveField("Grant.Permission", acl.PermRead))),
			"the bucket owner's READ, rgw_rest_s3.cc:3640-3644 at v19.2.6 clears it only for a bucket")
	})
	It("applies an AccessControlPolicy document, keeping the object's other attrs", func(ctx SpecContext) {
		before := w.stat(ctx, "src")
		expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain/src?acl", aclDoc("alice", userGrant("alice", "FULL_CONTROL"), userGrant("bob", "READ"))), 200)
		Expect(storedACL(ctx).ACL.Grants).To(ContainElement(HaveField("Grant.ID", "bob")))
		Expect(w.stat(ctx, "src").ETag).To(Equal(before.ETag))
		Expect(w.send(w.bob, http.MethodGet, "/plain/src", "").Code).To(Equal(200), "bob may now read the object")
	})
	It("reads a chunked document whole", func(ctx SpecContext) {
		doc := aclDoc("alice", userGrant("alice", "FULL_CONTROL"), userGrant("bob", "READ"))
		expectEmptyXML(serveReq(w.handler(w.alice), http.MethodPut, "/plain/src?acl", io.MultiReader(strings.NewReader(doc))), 200)
		Expect(storedACL(ctx).ACL.Grants).To(ContainElement(HaveField("Grant.ID", "bob")))
	})
	It("refuses a body past rgw_max_put_param_size as MalformedXML with radosgw's message", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPut, "/plain/src?acl", strings.Repeat("x", 2<<20))
		expectError(rec, 400, "MalformedXML")
		Expect(rec.Body.String()).To(ContainSubstring("<Message>The XML you provided was larger than the maximum 1048576 bytes allowed.</Message>"),
			"rgw_op.cc:5830-5837 at v19.2.6")
		w.conf["rgw_max_put_param_size"] = "16"
		rec = w.send(w.alice, http.MethodPut, "/plain/src?acl", aclDoc("alice"))
		Expect(rec.Body.String()).To(ContainSubstring("<Message>The XML you provided was larger than the maximum 16 bytes allowed.</Message>"))
		Expect(storedACL(ctx)).To(Equal(private()))
	})
	It("refuses a canned ACL beside a body with InvalidArgument", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?acl", aclDoc("alice"), "x-amz-acl", "public-read"), 400, "InvalidArgument")
		Expect(storedACL(ctx)).To(Equal(private()))
	})
	It("refuses to change the owner", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPut, "/plain/src?acl", aclDoc("bob", userGrant("bob", "FULL_CONTROL")))
		expectError(rec, 403, "AccessDenied")
		Expect(rec.Body.String()).To(ContainSubstring("<Message>Cannot modify ACL Owner</Message>"))
		Expect(storedACL(ctx)).To(Equal(private()))
	})
	It("refuses a public ACL under a block of public ACLs", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?acl", "", "x-amz-acl", "public-read", "Content-Length", "0"), 403, "AccessDenied")
		Expect(storedACL(ctx)).To(Equal(private()))
	})
	It("answers a missing object 404 NoSuchKey", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/missing?acl", "", "x-amz-acl", "public-read", "Content-Length", "0"), 404, "NoSuchKey")
	})
	It("refuses a requester the object refuses before it reads the body, 403 and not the body's 400", func(ctx SpecContext) {
		expectError(w.send(w.bob, http.MethodPut, "/plain/src?acl", strings.Repeat("x", 2<<20)), 403, "AccessDenied")
		expectError(w.send(w.bob, http.MethodPut, "/plain/src?acl", "<AccessControlPolicy"), 403, "AccessDenied")
		Expect(storedACL(ctx)).To(Equal(private()))
	})
	It("refuses a body whose payload hash does not match, changing nothing", func(ctx SpecContext) {
		rec := w.sendTampered(http.MethodPut, "/plain/src?acl", aclDoc("alice", userGrant("alice", "FULL_CONTROL"), userGrant("bob", "FULL_CONTROL")))
		expectError(rec, 400, "XAmzContentSHA256Mismatch")
		Expect(storedACL(ctx)).To(Equal(private()))
	})
	It("refuses a request without a Content-Length with MissingContentLength, as radosgw's tolerance never holds", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?acl", "", "x-amz-acl", "public-read"), 411, "MissingContentLength")
	})
	It("answers 409 ConcurrentModification once every try loses a race, where radosgw answers 200", func(ctx SpecContext) {
		objects := &opfakes.FakeObjectStore{}
		objects.StatObjectStub = w.store.StatObject
		objects.SetObjectAttrsReturns(op.ErrConcurrentModification)
		w.env.Objects = objects
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?acl", "", "x-amz-acl", "public-read", "Content-Length", "0"), 409, "ConcurrentModification")
		Expect(objects.SetObjectAttrsCallCount()).To(Equal(16), "rgw_op.cc:5925-5927 at v19.2.6")
		Expect(storedACL(ctx)).To(Equal(private()))
	})
})

var _ = Describe("put_obj_tags and delete_obj_tags", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	It("stores a tag set, answers 200 with no body, and GET ?tagging returns it", func(ctx SpecContext) {
		expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain/src?tagging", taggingDoc(2)), 200)
		Expect(w.send(w.alice, http.MethodGet, "/plain/src?tagging", "").Body.String()).To(ContainSubstring(
			"<TagSet><Tag><Key>k0</Key><Value>v0</Value></Tag><Tag><Key>k1</Key><Value>v1</Value></Tag></TagSet>"))
		Expect(w.stat(ctx, "src").ETag).To(Equal(md5Hex("hello")))
	})
	It("refuses eleven tags with InvalidTag", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?tagging", taggingDoc(11)), 400, "InvalidTag")
		Expect(w.stat(ctx, "src").Attrs).NotTo(HaveKey(tags.Attr))
	})
	It("refuses a document that does not parse with MalformedXML", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?tagging", "<Tagging><TagSet>"), 400, "MalformedXML")
	})
	It("refuses a body past rgw_max_put_param_size with InvalidRange", func() {
		w.conf["rgw_max_put_param_size"] = "16"
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?tagging", taggingDoc(1)), 416, "InvalidRange")
	})
	It("refuses a request with neither a length nor chunking with MissingContentLength", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/src?tagging", ""), 411, "MissingContentLength")
	})
	It("answers a missing object 404 NoSuchKey", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/missing?tagging", taggingDoc(1)), 404, "NoSuchKey")
		expectError(w.send(w.alice, http.MethodDelete, "/plain/missing?tagging", ""), 404, "NoSuchKey")
	})
	It("refuses a requester the object refuses before it reads the body, 403 and not the body's 400", func(ctx SpecContext) {
		expectError(w.send(w.bob, http.MethodPut, "/plain/src?tagging", "<Tagging><TagSet>"), 403, "AccessDenied")
		expectError(w.send(w.bob, http.MethodPut, "/plain/src?tagging", strings.Repeat("x", 2<<20)), 403, "AccessDenied")
		Expect(w.stat(ctx, "src").Attrs).NotTo(HaveKey(tags.Attr))
	})
	It("refuses a body whose payload hash does not match, changing nothing", func(ctx SpecContext) {
		expectError(w.sendTampered(http.MethodPut, "/plain/src?tagging", taggingDoc(1)), 400, "XAmzContentSHA256Mismatch")
		Expect(w.stat(ctx, "src").Attrs).NotTo(HaveKey(tags.Attr))
	})
	It("removes the tags with 204 and no body", func(ctx SpecContext) {
		expectEmptyXML(w.send(w.alice, http.MethodPut, "/plain/src?tagging", taggingDoc(1)), 200)
		expectEmptyXML(w.send(w.alice, http.MethodDelete, "/plain/src?tagging", ""), 204)
		Expect(w.stat(ctx, "src").Attrs).NotTo(HaveKey(tags.Attr))
	})
})
