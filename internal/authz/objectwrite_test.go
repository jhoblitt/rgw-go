package authz_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// objectWriteActions are the object-scope actions op_to_perm maps to WRITE
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L260-L268 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L273-L284 at v20.2.4),
// which an object ACL's WRITE grant authorizes in radosgw and authorizes
// nothing in Amazon S3, where an object's WRITE is "not applicable"
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#acl-access-policy-permission-mapping).
var objectWriteActions = []policy.Action{
	policy.S3PutObjectRetention, policy.S3PutObjectLegalHold, policy.S3BypassGovernanceRetention,
	policy.S3PutObjectTagging, policy.S3PutObjectVersionTagging,
	policy.S3DeleteObjectTagging, policy.S3DeleteObjectVersionTagging,
	policy.S3RestoreObject,
}

// aliceOwner owns the specs' buckets and objects.
var aliceOwner = userOwner("", "alice")

var _ = Describe("an object ACL's WRITE grant", func() {
	var f *fixture
	BeforeEach(func() { f = newFixture(denc.Squid) })

	// verify checks every object WRITE action for id on priv's key, each
	// with WRITE as op_to_perm gives it, and returns the actions refused.
	verify := func(id op.Identity, key string) []policy.Action {
		GinkgoHelper()
		var refused []policy.Action
		for _, a := range objectWriteActions {
			Expect(acl.PermFor(a)).To(Equal(acl.PermWrite), a.String())
			r := f.req(id, "priv", key)
			r.Method = http.MethodPut
			if err := f.eval.VerifyObject(f.ctx, r, a, acl.PermFor(a)); err != nil {
				Expect(err).To(MatchError(op.ErrAccessDenied), a.String())
				refused = append(refused, a)
			}
		}
		return refused
	}

	DescribeTable("authorizes no grantee but the object's owner",
		func(id func() op.Identity, grants ...acl.Grant) {
			p := ownerACL(aliceOwner)
			for _, g := range grants {
				p = withGrant(p, g)
			}
			f.object("priv", "granted", aclAttr(p), nil)
			Expect(verify(id(), "granted")).To(Equal(objectWriteActions))
		},
		Entry("the anonymous user, through AllUsers' WRITE as public-read-write grants it", anon,
			groupGrant(acl.GroupAllUsers, acl.PermRead), groupGrant(acl.GroupAllUsers, acl.PermWrite)),
		Entry("an authenticated stranger, through AllUsers' WRITE", bob, groupGrant(acl.GroupAllUsers, acl.PermWrite)),
		Entry("an authenticated stranger, through AuthenticatedUsers' WRITE", bob,
			groupGrant(acl.GroupAuthenticatedUsers, acl.PermWrite)),
		Entry("a named user's WRITE", bob, userGrant("bob", acl.PermWrite)),
		Entry("a named user's FULL_CONTROL", bob, userGrant("bob", acl.PermFullControl)),
		Entry("a named user's Swift WRITE_OBJS, which the ACL counts as WRITE", bob, userGrant("bob", acl.PermWriteObjs)),
		Entry("the anonymous user's own grant of WRITE", anon, userGrant("anonymous", acl.PermWrite)),
	)
	It("refuses the WRITE a Swift container's write list gives through the bucket's ACL", func() {
		f.bucket("swiftw", aliceOwner, aclAttr(withGrant(ownerACL(aliceOwner), userGrant("bob", acl.PermWriteObjs))))
		f.object("swiftw", "o", aclAttr(ownerACL(aliceOwner)), nil)
		for _, a := range objectWriteActions {
			r := f.req(bob(), "swiftw", "o")
			r.Method = http.MethodPut
			Expect(f.eval.VerifyObject(f.ctx, r, a, acl.PermFor(a))).To(MatchError(op.ErrAccessDenied), a.String())
		}
	})
	It("leaves a non-owner's FULL_CONTROL its other rights: READ, READ_ACP and WRITE_ACP", func() {
		f.object("priv", "full", aclAttr(withGrant(ownerACL(aliceOwner), userGrant("bob", acl.PermFullControl))), nil)
		for a, perm := range map[policy.Action]acl.Permission{
			policy.S3GetObject: acl.PermRead, policy.S3GetObjectAcl: acl.PermReadACP, policy.S3PutObjectAcl: acl.PermWriteACP,
		} {
			Expect(f.eval.VerifyObject(f.ctx, f.req(bob(), "priv", "full"), a, perm)).To(Succeed(), a.String())
		}
	})
	It("authorizes the object's owner through her own grant", func() {
		Expect(verify(alice(), "o")).To(BeEmpty())
	})
	It("authorizes no owner its own ACL does not grant WRITE", func() {
		readOnly := ownerACL(aliceOwner)
		readOnly.ACL = acl.List{}
		readOnly.ACL.AddGrant(userGrant("alice", acl.PermRead))
		f.object("priv", "ro", aclAttr(readOnly), nil)
		Expect(verify(alice(), "ro")).To(Equal(objectWriteActions))
	})
	It("authorizes the anonymous owner of an anonymous write no WRITE", func() {
		anonOwned := ownerACL(userOwner("", "anonymous"))
		f.object("priv", "anonw", aclAttr(anonOwned), nil)
		Expect(verify(anon(), "anonw")).To(Equal(objectWriteActions))
	})
	It("authorizes a requester a bucket policy allows, whatever the object's ACL", func() {
		f.setPolicy("priv", doc(stmt("Allow", awsPrincipal("arn:aws:iam:::user/bob"), "s3:*", "arn:aws:s3:::priv/*", "")))
		Expect(verify(bob(), "o")).To(BeEmpty())
	})
	It("leaves the bucket-scope WRITE PutObject checks to the bucket's ACL", func() {
		f.bucket("pubw", aliceOwner, aclAttr(withGrant(ownerACL(aliceOwner), groupGrant(acl.GroupAllUsers, acl.PermWrite))))
		Expect(f.putObject(anon(), "pubw", "new")).To(Succeed())
	})
})
