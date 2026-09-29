package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

func userIdentity(id string) op.Identity {
	return op.Identity{Owner: meta.UserOwner(meta.UserID{ID: id}), OpMask: op.OpTypeAll}
}

func ownedBucket(owner string) *op.BucketRecord {
	return &op.BucketRecord{Info: meta.BucketInfo{Owner: meta.UserOwner(meta.UserID{ID: owner})}}
}

var _ = Describe("OwnerOnly", func() {
	var req *op.Request
	BeforeEach(func() {
		req = &op.Request{
			Identity:  userIdentity("alice"),
			Env:       &op.Env{Authz: op.OwnerOnly{}},
			BucketRec: ownedBucket("alice"),
			ObjState:  &op.ObjectState{Key: meta.ObjKey{Name: "k"}},
		}
	})
	It("allows every service action, as verify_user_permission_no_policy does without a user ACL", func(ctx SpecContext) {
		req.Identity = op.Anonymous()
		Expect(op.VerifyUserPermission(ctx, req, policy.S3ListAllMyBuckets)).To(Succeed())
	})
	It("allows the bucket owner", func(ctx SpecContext) {
		Expect(op.VerifyBucketPermission(ctx, req, policy.S3ListBucket, acl.PermRead)).To(Succeed(), "bucket")
		Expect(op.VerifyObjectPermission(ctx, req, policy.S3GetObject, acl.PermRead)).To(Succeed(), "object")
	})
	It("denies another user", func(ctx SpecContext) {
		req.Identity = userIdentity("bob")
		Expect(op.VerifyBucketPermission(ctx, req, policy.S3ListBucket, acl.PermRead)).To(MatchError(op.ErrAccessDenied), "bucket")
		Expect(op.VerifyObjectPermission(ctx, req, policy.S3GetObject, acl.PermRead)).To(MatchError(op.ErrAccessDenied), "object")
	})
	It("denies an anonymous identity, whatever owns the bucket", func(ctx SpecContext) {
		req.Identity = op.Anonymous()
		req.BucketRec = ownedBucket(op.AnonymousUserID)
		Expect(op.VerifyBucketPermission(ctx, req, policy.S3ListBucket, acl.PermRead)).To(MatchError(op.ErrAccessDenied))
	})
	It("denies when no bucket is loaded", func(ctx SpecContext) {
		req.BucketRec = nil
		Expect(op.VerifyBucketPermission(ctx, req, policy.S3ListBucket, acl.PermRead)).To(MatchError(op.ErrAccessDenied))
	})
	It("checks the explicit bucket's owner in the In forms, not the request's", func(ctx SpecContext) {
		other := ownedBucket("bob")
		src := &op.ObjectState{Bucket: other, Key: meta.ObjKey{Name: "src"}}
		Expect(op.VerifyBucketPermissionIn(ctx, req, policy.S3GetObject, acl.PermRead, other, src.Key)).To(MatchError(op.ErrAccessDenied), "bucket in")
		Expect(op.VerifyObjectPermissionIn(ctx, req, policy.S3GetObject, acl.PermRead, other, src)).To(MatchError(op.ErrAccessDenied), "object in")
		req.BucketRec = other
		mine := ownedBucket("alice")
		Expect(op.VerifyBucketPermissionIn(ctx, req, policy.S3GetObject, acl.PermRead, mine, src.Key)).To(Succeed(), "bucket in")
		Expect(op.VerifyObjectPermissionIn(ctx, req, policy.S3GetObject, acl.PermRead, mine, src)).To(Succeed(), "object in")
	})
})

var _ = Describe("the Verify*Permission helpers", func() {
	It("delegate to Env.Authz with their arguments", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		authz.VerifyUserReturns(op.ErrAccessDenied)
		authz.VerifyObjectInReturns(op.ErrNoSuchKey)
		req := &op.Request{Env: &op.Env{Authz: authz}}
		bucket := ownedBucket("alice")
		obj := &op.ObjectState{Key: meta.ObjKey{Name: "k"}}

		Expect(op.VerifyUserPermission(ctx, req, policy.S3CreateBucket)).To(MatchError(op.ErrAccessDenied))
		_, gotReq, a := authz.VerifyUserArgsForCall(0)
		Expect(gotReq).To(BeIdenticalTo(req))
		Expect(a).To(Equal(policy.S3CreateBucket))

		Expect(op.VerifyBucketPermission(ctx, req, policy.S3PutBucketAcl, acl.PermWriteACP)).To(Succeed())
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3PutBucketAcl, acl.PermWriteACP}))

		Expect(op.VerifyObjectPermission(ctx, req, policy.S3PutObject, acl.PermWrite)).To(Succeed())
		_, _, a, perm = authz.VerifyObjectArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3PutObject, acl.PermWrite}))

		Expect(op.VerifyBucketPermissionIn(ctx, req, policy.S3DeleteObject, acl.PermWrite, bucket, obj.Key)).To(Succeed())
		_, _, a, perm, gotBucket, key := authz.VerifyBucketInArgsForCall(0)
		Expect([]any{a, perm, key}).To(Equal([]any{policy.S3DeleteObject, acl.PermWrite, obj.Key}))
		Expect(gotBucket).To(BeIdenticalTo(bucket))

		Expect(op.VerifyObjectPermissionIn(ctx, req, policy.S3GetObject, acl.PermRead, bucket, obj)).To(MatchError(op.ErrNoSuchKey))
		_, _, a, perm, gotBucket, gotObj := authz.VerifyObjectInArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3GetObject, acl.PermRead}))
		Expect(gotBucket).To(BeIdenticalTo(bucket))
		Expect(gotObj).To(BeIdenticalTo(obj))
	})
})
