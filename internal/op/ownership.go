package op

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The ownership controls ops of this file are v21.1.2's
// RGWGetBucketOwnershipControls, RGWPutBucketOwnershipControls and
// RGWDeleteBucketOwnershipControls, which neither floor has; rgw-go serves
// them on both releases (docs/exclusions.md, "rgw-go stores and enforces
// ownership controls a floor radosgw ignores"). Each loads the bucket in
// Init, so a missing bucket is NoSuchBucket before the permission check, and
// authorizes its action against the bucket without the bucket's tags, as none
// of their verify_permission calls rgw_iam_add_buckettags
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10247-L10253,
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10307-L10313 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10336-L10342 at v21.1.2),
// and with no ACL grant consulted (verifyOwnershipAction).
// The setting is the bucket attr acl.AttrOwnershipControls, which the write
// sets and the delete removes, each alone over the bucket as each try read
// it. The forward to the metadata master each write makes is a no-op on a
// single zone, which is its own master.

// verifyOwnershipAction authorizes o's action against the request's bucket
// by the owner and the policies alone, with the ACL permission v21.1.2's
// op_to_perm maps the action to, which bounds the owner's permission mask.
// v21.1.2 falls back to the bucket's ACL, so any READ_ACP grantee reads the
// setting and any WRITE_ACP grantee changes it
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L314-L315 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L338-L339 at v21.1.2),
// where Amazon S3 maps those ACL permissions to s3:GetBucketAcl and
// s3:PutBucketAcl alone (docs/exclusions.md, "rgw-go stores and enforces
// ownership controls a floor radosgw ignores").
func verifyOwnershipAction(ctx context.Context, r *Request, o Op) error {
	a := o.Action()
	return VerifyBucketPermissionOwnerOrPolicy(ctx, r, a, acl.PermFor(a))
}

// GetBucketOwnershipControls is RGWGetBucketOwnershipControls
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10307-L10334 at v21.1.2):
// the bucket's ownership setting as stored.
type GetBucketOwnershipControls struct {
	// Ownership is the stored setting, a byte outside the three included,
	// which the document names "invalid".
	Ownership acl.Ownership
}

var _ Op = (*GetBucketOwnershipControls)(nil)

// Name is RGWGetBucketOwnershipControls::name.
func (*GetBucketOwnershipControls) Name() string { return "get_bucket_ownership_controls" }

// Action is s3:GetBucketOwnershipControls.
func (*GetBucketOwnershipControls) Action() policy.Action {
	return policy.S3GetBucketOwnershipControls
}

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketOwnershipControls) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketOwnershipControls) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWGetBucketOwnershipControls::verify_permission,
// without its ACL fallback.
func (o *GetBucketOwnershipControls) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyOwnershipAction(ctx, r, o)
}

// Execute is RGWGetBucketOwnershipControls::execute. A bucket without the
// attr is -ENOENT with execute's message, which send_response answers as
// OwnershipControlsNotFoundError
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4590-L4597 at v21.1.2).
// The attr is read as acl.DecodeOwnership reads it, the bytes after the
// setting ignored and its byte kept as stored. One that does not decode, an
// empty one among them, is -EIO, which no row of the error table holds, so
// set_req_state_err answers it 500 UnknownError.
func (o *GetBucketOwnershipControls) Execute(_ context.Context, r *Request) error {
	b, ok := r.BucketRec.Attrs[acl.AttrOwnershipControls]
	if !ok {
		return ErrNoSuchOwnershipControls.WithMessage("The bucket ownership controls were not found")
	}
	d := denc.NewDecoder(b)
	own := acl.DecodeOwnership(d)
	if err := d.Err(); err != nil {
		return fmt.Errorf("%w: decoding the ownership controls of bucket %s: %w", ErrUnknown, r.BucketRec.Info.Bucket.Name, err)
	}
	o.Ownership = own
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketOwnershipControls) Complete(context.Context, *Request) {}

// PutBucketOwnershipControls is RGWPutBucketOwnershipControls
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10247-L10305 at v21.1.2).
// Params, when set, is the handler's get_params, which execute runs once the
// requester is authorized: it reads and parses the body and fills Ownership,
// or returns its refusal already mapped to an S3 error.
type PutBucketOwnershipControls struct {
	Params func(ctx context.Context, o *PutBucketOwnershipControls) error
	// Ownership is the setting to store. Its zero value is
	// BucketOwnerEnforced, so Params must fill it.
	Ownership acl.Ownership
}

var _ Op = (*PutBucketOwnershipControls)(nil)

// Name is RGWPutBucketOwnershipControls::name.
func (*PutBucketOwnershipControls) Name() string { return "put_bucket_ownership_controls" }

// Action is s3:PutBucketOwnershipControls.
func (*PutBucketOwnershipControls) Action() policy.Action {
	return policy.S3PutBucketOwnershipControls
}

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutBucketOwnershipControls) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutBucketOwnershipControls) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWPutBucketOwnershipControls::verify_permission,
// without its ACL fallback.
func (o *PutBucketOwnershipControls) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyOwnershipAction(ctx, r, o)
}

// Execute is RGWPutBucketOwnershipControls::execute: Params, then the
// setting's (1,1) encoding set over the bucket as the attr alone through
// retryBucketWrite, which authorizes each retry again and stops when the name
// names another bucket; v21.1.2's retries authorize nothing
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10277-L10304 at v21.1.2;
// docs/exclusions.md, "A PutBucketAcl that loses a race is retried, and every
// retried ACL, policy or tagging write is authorized again"). Each try of
// BucketOwnerEnforced first holds the ACL that try read to the default
// private one (privateBucketACL).
func (o *PutBucketOwnershipControls) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	set := map[string][]byte{acl.AttrOwnershipControls: encodeAt(o.Ownership, r.Env.Zone.Release())}
	return retryBucketWrite(ctx, r, o, func() error {
		if o.Ownership == acl.BucketOwnerEnforced {
			if err := privateBucketACL(r.BucketRec); err != nil {
				return err
			}
		}
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutBucketOwnershipControls) Complete(context.Context, *Request) {}

// privateBucketACL is v21.1.2's check that a bucket's ACL is private before
// BucketOwnerEnforced is set
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10272-L10300 at v21.1.2):
// a bucket without the ACL attr passes; one that does not decode is -EIO, 500
// UnknownError; and any other is held to enforcedOver. v21.1.2 builds the
// default it compares with once, from the ACL its request loaded,
// s->bucket_owner, and compares each try's ACL with it; rgw-go compares each
// try's ACL with the default of the owner that ACL names, the same on the
// first try.
func privateBucketACL(rec *BucketRecord) error {
	b, ok := rec.Attrs[meta.AttrACL]
	if !ok {
		return nil
	}
	p, err := decodeACL(b, "bucket "+rec.Info.Bucket.Name)
	if err != nil {
		return err
	}
	return enforcedOver(p)
}

// enforcedOver refuses BucketOwnerEnforced beside p, a bucket ACL other than
// the default private one of the owner it names (isPrivateACL), with
// InvalidBucketAclWithObjectOwnership and v21.1.2's message
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10294-L10298 at v21.1.2).
func enforcedOver(p acl.Policy) error {
	if isPrivateACL(p) {
		return nil
	}
	return ErrInvalidBucketACL.WithMessage("The bucket's ACL must be made private before setting BucketOwnerEnforced.")
}

// isPrivateACL is whether p is create_default of the owner it names
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L430-L434 at v21.1.2),
// as RGWAccessControlPolicy's operator== compares them: the owners, the user
// and group maps, the referer lists and the grant multimaps, a canonical
// user grantee's display name included
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.cc#L22-L66 and
// https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_acl.h#L20-L26 at v21.1.2).
func isPrivateACL(p acl.Policy) bool {
	return samePolicy(p, acl.DefaultPolicy(meta.ParseOwner(p.Owner.ID), p.Owner.DisplayName))
}

// DeleteBucketOwnershipControls is RGWDeleteBucketOwnershipControls
// (https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10336-L10358 at v21.1.2).
type DeleteBucketOwnershipControls struct{}

var _ Op = (*DeleteBucketOwnershipControls)(nil)

// Name is RGWDeleteBucketOwnershipControls::name.
func (*DeleteBucketOwnershipControls) Name() string { return "delete_bucket_ownership_controls" }

// Action is s3:PutBucketOwnershipControls, which
// RGWDeleteBucketOwnershipControls authorizes.
func (*DeleteBucketOwnershipControls) Action() policy.Action {
	return policy.S3PutBucketOwnershipControls
}

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteBucketOwnershipControls
// declares.
func (*DeleteBucketOwnershipControls) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeleteBucketOwnershipControls) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWDeleteBucketOwnershipControls::verify_permission,
// without its ACL fallback.
func (o *DeleteBucketOwnershipControls) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyOwnershipAction(ctx, r, o)
}

// Execute is RGWDeleteBucketOwnershipControls::execute: the attr removed
// through retryBucketWrite. A bucket without one is rewritten too, and
// succeeds, as v21.1.2's put_info rewrites it.
func (o *DeleteBucketOwnershipControls) Execute(ctx context.Context, r *Request) error {
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, []string{acl.AttrOwnershipControls})
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeleteBucketOwnershipControls) Complete(context.Context, *Request) {}
