package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

//counterfeiter:generate . Authorizer

// Authorizer is radosgw's verify_permission machinery: op mask aside, the
// ACL, bucket-policy and identity-policy evaluation for one action.
// authz.Evaluator implements it; OwnerOnly is the development stub.
type Authorizer interface {
	// VerifyUser authorizes an action with no bucket, such as ListAllMyBuckets
	// and CreateBucket, against the identity's own policies.
	VerifyUser(ctx context.Context, r *Request, a policy.Action) error
	// VerifyBucket authorizes a against r.BucketRec with perm as the ACL permission.
	VerifyBucket(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
	// VerifyObject authorizes a against r.ObjState within r.BucketRec.
	VerifyObject(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
	// VerifyBucketIn is VerifyBucket against an explicit bucket and key:
	// CopyObject's source and DeleteObjects' per-key check. key.Name "" is the
	// bucket ARN. Every input that describes the resource comes from bucket:
	// the ARN, the ACL, the bucket policy, parsed with bucket's own tenant, and
	// the owner. bucket's public-access block applies as well as the request's
	// bucket's: IgnorePublicACLs holds when either block sets it, and each
	// block's RestrictPublicBuckets is judged against its own bucket's owner.
	// Beyond its block, the request's bucket supplies only the requester-pays
	// check and the x-amz-expected-bucket-owner comparison, whose header names
	// the request's bucket.
	VerifyBucketIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, key meta.ObjKey) error
	// VerifyObjectIn is VerifyObject against an explicit object in an explicit
	// bucket, UploadPartCopy's source, with VerifyBucketIn's split of inputs
	// between the two buckets; an object with no ACL of its own gets a default
	// ACL owned by bucket's owner.
	VerifyObjectIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, obj *ObjectState) error
}

// VerifyUserPermission authorizes a service-scope action through r.Env.Authz.
func VerifyUserPermission(ctx context.Context, r *Request, a policy.Action) error {
	return r.Env.Authz.VerifyUser(ctx, r, a)
}

// VerifyBucketPermission authorizes a bucket action through r.Env.Authz.
func VerifyBucketPermission(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error {
	return r.Env.Authz.VerifyBucket(ctx, r, a, perm)
}

// VerifyObjectPermission authorizes an object action through r.Env.Authz.
func VerifyObjectPermission(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error {
	return r.Env.Authz.VerifyObject(ctx, r, a, perm)
}

// VerifyBucketPermissionIn authorizes a against an explicit bucket and key
// (CopyObject's source, DeleteObjects' per-key check) through r.Env.Authz.
func VerifyBucketPermissionIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, key meta.ObjKey) error {
	return r.Env.Authz.VerifyBucketIn(ctx, r, a, perm, bucket, key)
}

// VerifyObjectPermissionIn authorizes a against an explicit object
// (UploadPartCopy's source) through r.Env.Authz.
func VerifyObjectPermissionIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, obj *ObjectState) error {
	return r.Env.Authz.VerifyObjectIn(ctx, r, a, perm, bucket, obj)
}

// OwnerOnly is the interim authorizer: service actions are allowed, as
// radosgw's verify_user_permission_no_policy allows them for a user with no
// user ACL, and bucket and object actions are allowed to the bucket owner
// alone. It evaluates no ACL and no policy; authz.Evaluator replaces it.
type OwnerOnly struct{}

var _ Authorizer = OwnerOnly{}

// VerifyUser allows every service action.
func (OwnerOnly) VerifyUser(context.Context, *Request, policy.Action) error { return nil }

// VerifyBucket allows r.BucketRec's owner.
func (OwnerOnly) VerifyBucket(_ context.Context, r *Request, _ policy.Action, _ acl.Permission) error {
	return ownerOnlyOf(r, r.BucketRec)
}

// VerifyObject allows r.BucketRec's owner.
func (OwnerOnly) VerifyObject(_ context.Context, r *Request, _ policy.Action, _ acl.Permission) error {
	return ownerOnlyOf(r, r.BucketRec)
}

// VerifyBucketIn allows bucket's owner.
func (OwnerOnly) VerifyBucketIn(_ context.Context, r *Request, _ policy.Action, _ acl.Permission, bucket *BucketRecord, _ meta.ObjKey) error {
	return ownerOnlyOf(r, bucket)
}

// VerifyObjectIn allows bucket's owner.
func (OwnerOnly) VerifyObjectIn(_ context.Context, r *Request, _ policy.Action, _ acl.Permission, bucket *BucketRecord, _ *ObjectState) error {
	return ownerOnlyOf(r, bucket)
}

func ownerOnlyOf(r *Request, rec *BucketRecord) error {
	if r.Identity.Anonymous || rec == nil || rec.Info.Owner.String() != r.Identity.Owner.String() {
		return ErrAccessDenied
	}
	return nil
}
