package op

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The bucket subresource ops of this file are rgw_op.cc's RGWGetACLs and
// RGWPutACLs at bucket scope, RGWGetBucketPolicy, RGWPutBucketPolicy,
// RGWDeleteBucketPolicy, RGWGetBucketTags, RGWPutBucketTags,
// RGWDeleteBucketTags and RGWGetLC. Each loads the bucket in Init, so a
// missing bucket is NoSuchBucket before the permission check, and authorizes
// its action against the bucket. The forward to the metadata master each
// write makes is a no-op on a single zone, which is its own master. A bare
// line number is v19.2.6's rgw_op.cc.

// AttrIAMPolicy is RGW_ATTR_IAM_POLICY
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L154 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L173 at v20.2.4),
// the bucket attr holding its policy's text.
const AttrIAMPolicy = meta.AttrPrefix + "iam-policy"

// AttrLC is RGW_ATTR_LC
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L82 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L87 at v20.2.4),
// the bucket attr holding its lifecycle configuration.
const AttrLC = meta.AttrPrefix + "lc"

// attrIAMPolicyRemoveSelfAccess is Tentacle's
// RGW_ATTR_IAM_POLICY_REMOVE_SELF_ACCESS
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L177 at v20.2.4).
const attrIAMPolicyRemoveSelfAccess = meta.AttrPrefix + "iam-policy-remove-self-access"

// racedWriteRetries is the number of times retry_raced_bucket_write reads the
// bucket again after a lost race.
const racedWriteRetries = 15

// RetryRacedBucketWrite is retry_raced_bucket_write
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L183-L196 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L197-L210 at v20.2.4):
// f, and while it returns ErrConcurrentModification, up to fifteen times,
// the bucket read again by name into r.BucketRec, as try_refresh_info reads
// it (driver/rados/rgw_rados.cc:9011-9026 at v19.2.6, :9956-9971 at
// v20.2.4), then f again. A read that fails ends it with the read's error, a
// bucket gone since being the -ENOENT radosgw answers as NoSuchKey
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L97 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L98 at v20.2.4).
// A read that finds the name naming another instance, a bucket deleted and
// created again since the request was authorized, ends it with
// ErrConcurrentModification and nothing written, where radosgw writes into
// that bucket (docs/ceph-upstream-bugs.md, "radosgw's retried bucket write
// can land in a bucket re-created under the same name").
func RetryRacedBucketWrite(ctx context.Context, r *Request, f func() error) error {
	err := f()
	for i := 0; i < racedWriteRetries && errors.Is(err, ErrConcurrentModification); i++ {
		b := r.BucketRec.Info.Bucket
		rec, rerr := r.Env.Buckets.GetBucket(ctx, b.Tenant, b.Name)
		if errors.Is(rerr, ErrNoSuchBucket) {
			return fmt.Errorf("%w: bucket %s was removed during the write", ErrNoSuchKey, b.Name)
		}
		if rerr != nil {
			return rerr
		}
		if rec.Info.Bucket.ID != b.ID {
			return fmt.Errorf("%w: bucket %s was re-created as instance %s during the write", ErrConcurrentModification, b.Name, rec.Info.Bucket.ID)
		}
		r.BucketRec = rec
		err = f()
	}
	return err
}

// verifyBucketAction authorizes o's action against the request's bucket with
// the ACL permission the action maps to.
func verifyBucketAction(ctx context.Context, r *Request, o Op) error {
	a := o.Action()
	return VerifyBucketPermission(ctx, r, a, acl.PermFor(a))
}

// retryBucketWrite is RetryRacedWriteReauthorized for a write of the bucket:
// each retry runs o's VerifyPermission, the check Run made, again against
// the ACL, policy and public-access block of the bucket
// RetryRacedBucketWrite has just read, so that a retry writes nothing for a
// requester the write it lost to refused (docs/exclusions.md).
func retryBucketWrite(ctx context.Context, r *Request, o Op, f func() error) error {
	return RetryRacedWriteReauthorized(ctx, r, nil, func() error { return o.VerifyPermission(ctx, r) }, f)
}

// GetBucketACL is RGWGetACLs at bucket scope
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5695-L5734 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6275-L6314 at v20.2.4):
// s->bucket_acl, as BucketACLFor reads it.
type GetBucketACL struct {
	// Policy is the bucket's ACL.
	Policy acl.Policy
}

var _ Op = (*GetBucketACL)(nil)

// Name is RGWGetACLs::name, which the object ACL shares.
func (*GetBucketACL) Name() string { return "get_acls" }

// Action is s3:GetBucketAcl.
func (*GetBucketACL) Action() policy.Action { return policy.S3GetBucketAcl }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketACL) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket; verify_permission answers a missing one with
// ERR_NO_SUCH_BUCKET before it authorizes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5707-L5709).
func (*GetBucketACL) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetACLs::verify_permission for a bucket.
func (o *GetBucketACL) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute takes the bucket's ACL.
func (o *GetBucketACL) Execute(ctx context.Context, r *Request) error {
	p, err := BucketACLFor(ctx, r.BucketRec)
	if err != nil {
		return err
	}
	o.Policy = p
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketACL) Complete(context.Context, *Request) {}

// PutBucketACL is RGWPutACLs at bucket scope
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5821-L5928 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6467-L6590 at v20.2.4).
// Build is the handler's get_params and policy derivation: it receives
// s->bucket_acl, the stored ACL, once the requester is authorized, and
// returns the new policy, or its refusal already mapped to an S3 error.
type PutBucketACL struct {
	Build func(existing acl.Policy) (acl.Policy, error)
}

var _ Op = (*PutBucketACL)(nil)

// Name is RGWPutACLs::name, which the object ACL shares.
func (*PutBucketACL) Name() string { return "put_acls" }

// Action is s3:PutBucketAcl.
func (*PutBucketACL) Action() policy.Action { return policy.S3PutBucketAcl }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketACL) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketACL) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutACLs::verify_permission for a bucket.
func (o *PutBucketACL) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWPutACLs::execute for a bucket: Build from the stored ACL,
// then the ACL written over the bucket's attrs. A new owner is refused with
// -EPERM, "Cannot modify ACL Owner"
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5859-L5864),
// and a public policy under a block of public ACLs, or a block that does not
// decode (:5905-5910; docs/exclusions.md). radosgw answers a lost race with
// success and leaves the ACL unwritten (:5925-5927;
// docs/ceph-upstream-bugs.md, "radosgw's PutBucketAcl answers success when
// its write loses a race"); rgw-go retries it through retryBucketWrite and
// makes both checks again against the bucket as each try reads it, so a retry
// never puts back an owner a concurrent change replaced; a retry that passes
// them and finds the requested ACL in place writes nothing and succeeds
// (docs/exclusions.md).
func (o *PutBucketACL) Execute(ctx context.Context, r *Request) error {
	existing, err := BucketACLFor(ctx, r.BucketRec)
	if err != nil {
		return err
	}
	p, err := o.Build(existing)
	if err != nil {
		return err
	}
	set := map[string][]byte{meta.AttrACL: encodeAt(p, r.Env.Zone.Release())}
	raced := false
	return retryBucketWrite(ctx, r, o, func() error {
		current, err := BucketACLFor(ctx, r.BucketRec)
		if err != nil {
			return err
		}
		if aclOwnerChanged(current.Owner.ID, p.Owner.ID) {
			return ErrAccessDenied.WithMessage("Cannot modify ACL Owner")
		}
		if blockPublicACLs(r.BucketRec) && p.IsPublic() {
			return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
		}
		if raced && aclInPlace(r.BucketRec.Attrs, current, p) {
			return nil
		}
		raced = true
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// aclInPlace is whether a retry of an ACL write finds the requested ACL, want,
// already stored in attrs as current, its decoded form: radosgw answers the
// lost race 200
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5925-L5927 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6587-L6589 at v20.2.4),
// and the client's intent already holds. An ACL that holds only as the default
// generated for a missing attr is not in place.
func aclInPlace(attrs map[string][]byte, current, want acl.Policy) bool {
	_, stored := attrs[meta.AttrACL]
	return stored && sameACL(current, want)
}

// sameACL is whether a and b are one policy: the same owner and display name,
// the same grants in any order, the same user and group maps, and the same
// referer list in order, as get_referer_perm lets the last match win
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L137-L158 at v19.2.6
// and v20.2.4). Each grant compares as its key and its encoding, which keeps
// only the fields of its grantee type and the to_string form of a canonical
// user's id.
func sameACL(a, b acl.Policy) bool {
	return meta.ParseOwner(a.Owner.ID).String() == meta.ParseOwner(b.Owner.ID).String() &&
		a.Owner.DisplayName == b.Owner.DisplayName &&
		maps.Equal(a.ACL.UserMap, b.ACL.UserMap) &&
		maps.Equal(a.ACL.GroupMap, b.ACL.GroupMap) &&
		slices.Equal(a.ACL.RefererList, b.ACL.RefererList) &&
		slices.Equal(grantForms(a.ACL), grantForms(b.ACL))
}

// grantForms is l's grants, each as its key then its encoding, sorted.
func grantForms(l acl.List) []string {
	forms := make([]string, 0, len(l.Grants))
	for _, g := range l.Grants {
		e := denc.NewEncoder()
		e.String(g.Key)
		g.Grant.Encode(e, denc.Squid)
		forms = append(forms, string(e.Bytes()))
	}
	slices.Sort(forms)
	return forms
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketACL) Complete(context.Context, *Request) {}

// aclOwnerChanged is RGWPutACLs::execute's owner check: an existing owner
// that is not ACLOwner::empty
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_acl.cc#L249-L255 at v19.2.6
// and v20.2.4) and differs from the new one, both compared as rgw_owner.
func aclOwnerChanged(existing, next string) bool {
	o := meta.ParseOwner(existing)
	if o.User != nil && o.User.ID == "" || o.User == nil && o.Account == "" {
		return false
	}
	return o.String() != meta.ParseOwner(next).String()
}

// GetBucketPolicy is RGWGetBucketPolicy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8123-L8167 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9054-L9106 at v20.2.4):
// the bucket's policy text as stored.
type GetBucketPolicy struct {
	// JSON is the stored text.
	JSON []byte
}

var _ Op = (*GetBucketPolicy)(nil)

// Name is RGWGetBucketPolicy::name.
func (*GetBucketPolicy) Name() string { return "get_bucket_policy" }

// Action is s3:GetBucketPolicy.
func (*GetBucketPolicy) Action() policy.Action { return policy.S3GetBucketPolicy }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketPolicy) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketPolicy) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketPolicy::verify_permission, without
// Tentacle's pass for the root of the bucket owner's account
// (docs/exclusions.md).
func (o *GetBucketPolicy) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetBucketPolicy::execute: a policy attr that is absent or
// empty is NoSuchBucketPolicy, "The bucket policy does not exist".
func (o *GetBucketPolicy) Execute(_ context.Context, r *Request) error {
	b := r.BucketRec.Attrs[AttrIAMPolicy]
	if len(b) == 0 {
		return ErrNoSuchBucketPolicy.WithMessage("The bucket policy does not exist")
	}
	o.JSON = bytes.Clone(b)
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketPolicy) Complete(context.Context, *Request) {}

// PutBucketPolicy is RGWPutBucketPolicy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8060-L8121 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8978-L9052 at v20.2.4).
// Params, when set, is the handler's get_params and parse, which fill Policy
// once the requester is authorized; a refusal comes back already mapped to
// an S3 error.
type PutBucketPolicy struct {
	Params func(ctx context.Context, o *PutBucketPolicy) error
	// Policy is the parsed policy, whose Text is stored.
	Policy *policy.Policy
}

var _ Op = (*PutBucketPolicy)(nil)

// Name is RGWPutBucketPolicy::name.
func (*PutBucketPolicy) Name() string { return "put_bucket_policy" }

// Action is s3:PutBucketPolicy.
func (*PutBucketPolicy) Action() policy.Action { return policy.S3PutBucketPolicy }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketPolicy) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketPolicy) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutBucketPolicy::verify_permission, without
// Tentacle's pass for the root of the bucket owner's account
// (docs/exclusions.md).
func (o *PutBucketPolicy) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWPutBucketPolicy::execute: Params, whose parse refuses a
// public policy under a block of public policies, then the policy's text
// written over the bucket through retryBucketWrite. Each try refuses a
// public policy under the block of the bucket as that try read it, a block
// that does not decode blocking, and sets the policy over that bucket.
// radosgw's tries merge the attrs the request started with, so a retry
// writes back what the write it lost to had changed (docs/exclusions.md).
func (o *PutBucketPolicy) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	if o.Policy == nil {
		return fmt.Errorf("%w: no bucket policy to store", ErrInternalError)
	}
	public := o.Policy.IsPublic(policy.SemanticsFor(r.Env.Zone.Release()))
	set := map[string][]byte{AttrIAMPolicy: []byte(o.Policy.Text)}
	return retryBucketWrite(ctx, r, o, func() error {
		if public && blocksPublicPolicy(r.BucketRec) {
			return fmt.Errorf("%w: a public policy under a block of public policies", ErrAccessDenied)
		}
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// blocksPublicPolicy reports whether rec's public-access block sets
// BlockPublicPolicy. A block that does not decode blocks, as blockPublicACLs
// takes one (docs/exclusions.md).
func blocksPublicPolicy(rec *BucketRecord) bool {
	b, ok := rec.Attrs[AttrPublicAccess]
	if !ok {
		return false
	}
	d := denc.NewDecoder(b)
	block := acl.DecodePublicAccessBlock(d)
	return d.Err() != nil || block.BlockPublicPolicy
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketPolicy) Complete(context.Context, *Request) {}

// DeleteBucketPolicy is RGWDeleteBucketPolicy
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8169-L8210 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9108-L9158 at v20.2.4).
type DeleteBucketPolicy struct{}

var _ Op = (*DeleteBucketPolicy)(nil)

// Name is RGWDeleteBucketPolicy::name.
func (*DeleteBucketPolicy) Name() string { return "delete_bucket_policy" }

// Action is s3:DeleteBucketPolicy.
func (*DeleteBucketPolicy) Action() policy.Action { return policy.S3DeleteBucketPolicy }

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteBucketPolicy declares.
func (*DeleteBucketPolicy) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeleteBucketPolicy) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWDeleteBucketPolicy::verify_permission, without
// Tentacle's pass for the root of the bucket owner's account
// (docs/exclusions.md).
func (o *DeleteBucketPolicy) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute removes the policy attr, and on Tentacle the remove-self-access
// flag with it, through retryBucketWrite.
func (o *DeleteBucketPolicy) Execute(ctx context.Context, r *Request) error {
	rm := []string{AttrIAMPolicy}
	if r.Env.Zone.Release() >= denc.Tentacle {
		rm = append(rm, attrIAMPolicyRemoveSelfAccess)
	}
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, rm)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketPolicy) Complete(context.Context, *Request) {}

// GetBucketTagging is RGWGetBucketTags
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1141-L1169 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1378-L1406 at v20.2.4)
// with the decode RGWGetBucketTags_ObjStore_S3::send_response_data
// makes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L839-L866 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L921-L948 at v20.2.4).
type GetBucketTagging struct {
	// Set is the bucket's tag set.
	Set tags.Set
}

var _ Op = (*GetBucketTagging)(nil)

// Name is RGWGetBucketTags::name.
func (*GetBucketTagging) Name() string { return "get_bucket_tags" }

// Action is s3:GetBucketTagging.
func (*GetBucketTagging) Action() policy.Action { return policy.S3GetBucketTagging }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketTagging) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketTagging) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketTags::verify_permission.
func (o *GetBucketTagging) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetBucketTags::execute: no tag attr is NoSuchTagSet. A tag
// set that decodes neither as RGWObjTags nor as the text older buckets store
// is -EIO, which radosgw meets after it has sent its 200 and rgw-go answers
// as UnknownError (docs/exclusions.md).
func (o *GetBucketTagging) Execute(_ context.Context, r *Request) error {
	b, ok := r.BucketRec.Attrs[tags.Attr]
	if !ok {
		return ErrNoSuchTagSet
	}
	d := denc.NewDecoder(b)
	set := tags.Decode(d)
	if err := d.Err(); err != nil {
		return fmt.Errorf("%w: decoding the tags of bucket %s: %w", ErrUnknown, r.BucketRec.Info.Bucket.Name, err)
	}
	o.Set = set
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketTagging) Complete(context.Context, *Request) {}

// PutBucketTagging is RGWPutBucketTags
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1171-L1203 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1408-L1440 at v20.2.4).
// Params, when set, is the handler's get_params, which fills Set once
// the requester is authorized; a refusal comes back already mapped to an
// S3 error.
type PutBucketTagging struct {
	Params func(ctx context.Context, o *PutBucketTagging) error
	// Set is the tag set to store, empty or not.
	Set tags.Set
}

var _ Op = (*PutBucketTagging)(nil)

// Name is RGWPutBucketTags::name.
func (*PutBucketTagging) Name() string { return "put_bucket_tags" }

// Action is s3:PutBucketTagging.
func (*PutBucketTagging) Action() policy.Action { return policy.S3PutBucketTagging }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketTagging) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketTagging) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutBucketTags::verify_permission.
func (o *PutBucketTagging) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWPutBucketTags::execute: Params, then the encoded set written
// over the bucket through retryBucketWrite.
func (o *PutBucketTagging) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	set := map[string][]byte{tags.Attr: encodeAt(o.Set, r.Env.Zone.Release())}
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketTagging) Complete(context.Context, *Request) {}

// DeleteBucketTagging is RGWDeleteBucketTags
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1205-L1243 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1442-L1480 at v20.2.4).
type DeleteBucketTagging struct{}

var _ Op = (*DeleteBucketTagging)(nil)

// Name is RGWDeleteBucketTags::name.
func (*DeleteBucketTagging) Name() string { return "delete_bucket_tags" }

// Action is s3:PutBucketTagging, which RGWDeleteBucketTags authorizes.
func (*DeleteBucketTagging) Action() policy.Action { return policy.S3PutBucketTagging }

// OpMask is RGW_OP_TYPE_DELETE.
func (*DeleteBucketTagging) OpMask() uint32 { return OpTypeDelete }

// Init loads the bucket.
func (*DeleteBucketTagging) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWDeleteBucketTags::verify_permission.
func (o *DeleteBucketTagging) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute removes the tag attr through retryBucketWrite.
func (o *DeleteBucketTagging) Execute(ctx context.Context, r *Request) error {
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, []string{tags.Attr})
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketTagging) Complete(context.Context, *Request) {}

// GetBucketLifecycle is RGWGetLC
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L1631-L1646 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1785-L1801 at v20.2.4)
// with RGWGetLC_ObjStore_S3::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3658-L3676 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4139-L4157 at v20.2.4).
// rgw-go renders no lifecycle configuration yet, so it serves only a bucket
// without one.
type GetBucketLifecycle struct{}

var _ Op = (*GetBucketLifecycle)(nil)

// Name is RGWGetLC::name.
func (*GetBucketLifecycle) Name() string { return "get_lifecycle" }

// Action is s3:GetLifecycleConfiguration.
func (*GetBucketLifecycle) Action() policy.Action { return policy.S3GetLifecycleConfiguration }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketLifecycle) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketLifecycle) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetLC::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5759-L5770;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6405-L6416).
func (o *GetBucketLifecycle) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetLC_ObjStore_S3::execute up to the decode: a bucket without
// the lifecycle attr is -ENOENT, which send_response answers as
// ERR_NO_SUCH_LC, 404 NoSuchLifecycleConfiguration with no message
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3678-L3696 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4159-L4177 at v20.2.4).
// radosgw tests the attr's presence, so an empty one counts as a
// configuration; any configuration is NotImplemented (docs/exclusions.md).
func (*GetBucketLifecycle) Execute(_ context.Context, r *Request) error {
	if _, ok := r.BucketRec.Attrs[AttrLC]; !ok {
		return ErrNoSuchLifecycleConfiguration
	}
	return fmt.Errorf("%w: rendering the lifecycle configuration of bucket %s", ErrNotImplemented, r.BucketRec.Info.Bucket.Name)
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketLifecycle) Complete(context.Context, *Request) {}
