package s3_test

import (
	"context"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// allUsersURI is the AllUsers group a grant header names to make a policy
// public.
const allUsersURI = `uri="http://acs.amazonaws.com/groups/global/AllUsers"`

var _ = Describe("the object writes' authorization order", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, denc.Squid)
		// bob owns "bobs", holding "src" under his ACL and "broken", whose ACL
		// does not decode.
		bob := meta.UserOwner(w.bob.Info.UserID)
		bobACL := encodeACL(acl.DefaultPolicy(bob, "Bob"))
		rec, err := w.store.CreateBucket(ctx, op.CreateBucketParams{
			Name: "bobs", Owner: bob, Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: bobACL},
		})
		Expect(err).NotTo(HaveOccurred())
		for key, objACL := range map[string][]byte{"src": bobACL, "broken": {0xff}} {
			_, err = w.store.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader("bobs"),
				op.PutParams{Size: 4, Attrs: map[string][]byte{meta.AttrACL: objACL}})
			Expect(err).NotTo(HaveOccurred())
		}
	})
	// configure gives plain everything a refusal could depend on: object lock,
	// versioning, a quota no write fits, a default encryption and a block of
	// public ACLs.
	configure := func(ctx context.Context) {
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) {
			i.Flags |= meta.BucketObjLockEnabled | meta.BucketVersioned
			i.Quota = meta.Quota{MaxSize: -1, MaxObjects: 0, Enabled: true}
		})
		w.setBucketAttrs(ctx, map[string][]byte{
			op.AttrBucketEncryption: {1},
			op.AttrPublicAccess:     encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true}),
		})
	}
	copyFrom := func(src string, hdr ...string) []string { return append([]string{"X-Amz-Copy-Source", src}, hdr...) }

	DescribeTable("answers a requester the destination refuses 403 AccessDenied, whatever the store holds, writing nothing",
		func(ctx SpecContext, target, body string, hdr []string) {
			srcETag := w.stat(ctx, "src").ETag
			By("on a bucket that holds no configuration")
			expectError(w.send(w.bob, http.MethodPut, target, body, hdr...), 403, "AccessDenied")
			By("on a bucket with object lock, versioning, a quota, a default encryption and a block of public ACLs")
			configure(ctx)
			expectError(w.send(w.bob, http.MethodPut, target, body, hdr...), 403, "AccessDenied")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
			Expect(w.stat(ctx, "src").ETag).To(Equal(srcETag))
		},
		Entry("PutObject", "/plain/k", "x", nil),
		Entry("PutObject naming a storage class the zone lacks", "/plain/k", "x", []string{"X-Amz-Storage-Class", "NOPE"}),
		Entry("PutObject with a public canned ACL", "/plain/k", "x", []string{"X-Amz-Acl", "public-read"}),
		Entry("PutObject with a grant naming no user", "/plain/k", "x", []string{"X-Amz-Grant-Read", `id="nobody"`}),
		Entry("PutObject with a grant to AllUsers", "/plain/k", "x", []string{"X-Amz-Grant-Read", allUsersURI}),
		Entry("PutObject with a legal hold", "/plain/k", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}),
		Entry("PutObject with a retention", "/plain/k", "x",
			[]string{"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"}),
		Entry("PutObject with every header that consults the store", "/plain/k", "x", storedStateHeaders()),
		Entry("CopyObject from a source that exists", "/plain/dst", "", copyFrom("/plain/src")),
		Entry("CopyObject naming a storage class the zone lacks", "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Storage-Class", "NOPE")),
		Entry("CopyObject with a public canned ACL", "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Acl", "public-read")),
		Entry("CopyObject with a grant naming no user", "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Grant-Read", `id="nobody"`)),
		Entry("CopyObject with a grant to AllUsers", "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Grant-Read", allUsersURI)),
		Entry("CopyObject with a legal hold", "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Object-Lock-Legal-Hold", "ON")),
		Entry("CopyObject onto itself, changing nothing", "/plain/src", "", copyFrom("/plain/src")),
		Entry("CopyObject from a source key that does not exist", "/plain/dst", "", copyFrom("/plain/nope")),
		Entry("CopyObject from a missing key of a bucket the requester owns", "/plain/dst", "", copyFrom("/bobs/nope")),
		Entry("CopyObject from a source bucket that does not exist", "/plain/dst", "", copyFrom("/nope/src")),
		Entry("CopyObject from a source the requester owns", "/plain/dst", "", copyFrom("/bobs/src")),
		Entry("CopyObject from a source whose ACL does not decode", "/plain/dst", "", copyFrom("/bobs/broken")),
		Entry("CopyObject with every header that consults the store", "/plain/dst", "", copyFrom("/plain/src", storedStateHeaders()...)),
	)

	It("answers a POST-object 501 NotImplemented whatever the store holds, as no route serves it", func(ctx SpecContext) {
		post := func() {
			GinkgoHelper()
			expectError(w.send(w.bob, http.MethodPost, "/plain", "x", "Content-Type", "multipart/form-data; boundary=b"), 501, "NotImplemented")
		}
		post()
		configure(ctx)
		post()
	})

	DescribeTable("orders each refusal against the permission check and the request's own refusals",
		func(ctx SpecContext, who func() *op.UserRecord, setup func(ctx context.Context), target, body string, hdr []string, status int, code string) {
			if setup != nil {
				setup(ctx)
			}
			expectError(w.send(who(), http.MethodPut, target, body, hdr...), status, code)
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			Expect(w.stat(ctx, "dst").Exists).To(BeFalse())
		},
		Entry("a PutObject's storage class the zone lacks, InvalidArgument once authorized (rgw_op.cc:576-583)",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x", []string{"X-Amz-Storage-Class", "NOPE"}, 400, "InvalidArgument"),
		Entry("a PutObject's Content-MD5 that does not decode, a refusal of the request alone, ahead of a storage class the zone lacks",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x",
			[]string{"X-Amz-Storage-Class", "NOPE", "Content-MD5", "nope"}, 400, "InvalidDigest"),
		Entry("a PutObject's Content-MD5 that does not decode ahead of a public canned ACL under a block (rgw_op.cc:3903-3909)",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, "/plain/k", "x", []string{"X-Amz-Acl", "public-read", "Content-MD5", "nope"}, 400, "InvalidDigest"),
		Entry("a PutObject's public canned ACL under a block, refused once authorized",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, "/plain/k", "x", []string{"X-Amz-Acl", "public-read"}, 403, "AccessDenied"),
		Entry("a PutObject's Content-MD5 that does not decode ahead of a grant naming no user (rgw_rest_s3.cc:2618-2620)",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x",
			[]string{"X-Amz-Grant-Read", `id="nobody"`, "Content-MD5", "nope"}, 400, "InvalidDigest"),
		Entry("a PutObject's grant naming no user, looked up once authorized",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x", []string{"X-Amz-Grant-Read", `id="nobody"`}, 404, "NoSuchKey"),
		Entry("a PutObject's Content-MD5 that does not decode ahead of a legal hold on a bucket without object lock (rgw_rest_s3.cc:2669-2673)",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x",
			[]string{"X-Amz-Object-Lock-Legal-Hold", "ON", "Content-MD5", "nope"}, 400, "InvalidDigest"),
		Entry("a PutObject's legal hold on a bucket without object lock, refused once authorized",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "ON"}, 400, "InvalidRequest"),
		Entry("a PutObject's legal hold value the request alone refuses, before authorization",
			func() *op.UserRecord { return w.bob }, nil, "/plain/k", "x", []string{"X-Amz-Object-Lock-Legal-Hold", "on"}, 400, "InvalidArgument"),
		Entry("a PutObject's storage class the zone lacks ahead of the bucket's default encryption, as init_permissions comes first",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			}, "/plain/k", "x", []string{"X-Amz-Storage-Class", "NOPE"}, 400, "InvalidArgument"),
		Entry("a CopyObject's SSE header, a refusal of the request alone, ahead of a storage class the zone lacks",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "",
			copyFrom("/plain/src", "X-Amz-Storage-Class", "NOPE", "X-Amz-Server-Side-Encryption", "AES256"), 501, "NotImplemented"),
		Entry("a CopyObject's SSE header ahead of a legal hold on a bucket without object lock",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "",
			copyFrom("/plain/src", "X-Amz-Object-Lock-Legal-Hold", "ON", "X-Amz-Server-Side-Encryption", "AES256"), 501, "NotImplemented"),
		Entry("a CopyObject's storage class the zone lacks, InvalidArgument once authorized",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Storage-Class", "NOPE"), 400, "InvalidArgument"),
		Entry("a CopyObject's grant naming no user, looked up once authorized",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Grant-Read", `id="nobody"`), 404, "NoSuchKey"),
		Entry("a CopyObject's legal hold on a bucket without object lock, refused once authorized",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Object-Lock-Legal-Hold", "ON"), 400, "InvalidRequest"),
		Entry("a PutObject's SSE header, a refusal of the request alone, ahead of a public canned ACL under a block",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, "/plain/k", "x", []string{"X-Amz-Acl", "public-read", "X-Amz-Server-Side-Encryption", "AES256"}, 501, "NotImplemented"),
		Entry("a CopyObject's storage class the zone lacks, checked before the source is read, as init_permissions checks it (rgw_op.cc:576-583)",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "", copyFrom("/plain/nope", "X-Amz-Storage-Class", "NOPE"), 400, "InvalidArgument"),
		Entry("a CopyObject's storage class the zone lacks ahead of a source bucket that does not exist",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "", copyFrom("/nope/src", "X-Amz-Storage-Class", "NOPE"), 400, "InvalidArgument"),
		Entry("a PutObject's storage class the zone lacks ahead of a public canned ACL under a block, as init_permissions precedes init_processing",
			func() *op.UserRecord { return w.alice }, func(ctx context.Context) {
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: encodePublicAccess(acl.PublicAccessBlock{BlockPublicACLs: true})})
			}, "/plain/k", "x", []string{"X-Amz-Storage-Class", "NOPE", "X-Amz-Acl", "public-read"}, 400, "InvalidArgument"),
		Entry("a PutObject's grant naming no user ahead of a legal hold on a bucket without object lock, as create_s3_policy precedes it (rgw_rest_s3.cc:2618, :2669)",
			func() *op.UserRecord { return w.alice }, nil, "/plain/k", "x",
			[]string{"X-Amz-Grant-Read", `id="nobody"`, "X-Amz-Object-Lock-Legal-Hold", "ON"}, 404, "NoSuchKey"),
		Entry("a CopyObject's legal hold on a bucket without object lock ahead of a grant naming no user",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "",
			copyFrom("/plain/src", "X-Amz-Grant-Read", `id="nobody"`, "X-Amz-Object-Lock-Legal-Hold", "ON"), 400, "InvalidRequest"),
		Entry("a CopyObject from a source bucket that does not exist, answered once the destination allows the request",
			func() *op.UserRecord { return w.alice }, nil, "/plain/dst", "", copyFrom("/nope/src"), 404, "NoSuchBucket"),
	)
})
