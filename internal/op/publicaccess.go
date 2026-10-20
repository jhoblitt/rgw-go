package op

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The public access block ops of this file are rgw_op.cc's
// RGWGetBucketPublicAccessBlock, RGWPutBucketPublicAccessBlock and
// RGWDeleteBucketPublicAccessBlock. Each loads the bucket in Init, so a
// missing bucket is NoSuchBucket before the permission check, and authorizes
// its action against the bucket, whose tags the authorizer adds for a policy
// that needs them, with no ACL grant consulted
// (VerifyBucketPermissionOwnerOrPolicy; docs/exclusions.md, "No ACL grant
// authorizes the public access block's ops"). The block is the bucket attr
// user.rgw.public-access, which the two writes set or remove alone over the
// bucket as each try read it. A stored block reaches every later check
// through the bucket record, whose write sends the cache notify. The forward
// to the metadata master each write makes is a no-op on a single zone, which
// is its own master.

// verifyBlockAction authorizes o's action against the request's bucket by
// the owner and the policies alone, with the ACL permission op_to_perm maps
// the action to, which bounds the owner's permission mask.
func verifyBlockAction(ctx context.Context, r *Request, o Op) error {
	a := o.Action()
	return VerifyBucketPermissionOwnerOrPolicy(ctx, r, a, acl.PermFor(a))
}

// GetPublicAccessBlock is RGWGetBucketPublicAccessBlock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8677-L8712 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9645-L9680 at v20.2.4):
// the bucket's stored block.
type GetPublicAccessBlock struct {
	// Block is the stored block.
	Block acl.PublicAccessBlock
}

var _ Op = (*GetPublicAccessBlock)(nil)

// Name is RGWGetBucketPublicAccessBlock::name.
func (*GetPublicAccessBlock) Name() string { return "get_bucket_public_access_block" }

// Action is s3:GetBucketPublicAccessBlock.
func (*GetPublicAccessBlock) Action() policy.Action { return policy.S3GetBucketPublicAccessBlock }

// OpMask is RGW_OP_TYPE_READ.
func (*GetPublicAccessBlock) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetPublicAccessBlock) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketPublicAccessBlock::verify_permission,
// without its ACL fallback.
func (o *GetPublicAccessBlock) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBlockAction(ctx, r, o)
}

// Execute is RGWGetBucketPublicAccessBlock::execute: a bucket without the
// attr is NoSuchPublicAccessBlockConfiguration with radosgw's message, and an
// attr that does not decode is radosgw's -EIO, which the S3 error table lacks,
// so 500 UnknownError. The authorizer refuses such an attr to everyone but an
// admin before the op runs (docs/exclusions.md, "A public-access block that
// does not decode refuses the request").
func (o *GetPublicAccessBlock) Execute(_ context.Context, r *Request) error {
	b, ok := r.BucketRec.Attrs[AttrPublicAccess]
	if !ok {
		return ErrNoSuchPublicAccessBlock.WithMessage("The public access block configuration was not found")
	}
	d := denc.NewDecoder(b)
	block := acl.DecodePublicAccessBlock(d)
	if err := d.Err(); err != nil {
		return fmt.Errorf("%w: decoding the public access block of bucket %s: %w", ErrUnknown, r.BucketRec.Info.Bucket.Name, err)
	}
	o.Block = block
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetPublicAccessBlock) Complete(context.Context, *Request) {}

// PutPublicAccessBlock is RGWPutBucketPublicAccessBlock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8613-L8675 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9581-L9643 at v20.2.4).
// Params, when set, is the handler's read and parse of the body, which
// execute makes once the requester is authorized and which fills Block; a
// refusal comes back already mapped to an S3 error.
type PutPublicAccessBlock struct {
	Params func(ctx context.Context, o *PutPublicAccessBlock) error
	// Block is the block to store, which replaces the stored one whole.
	Block acl.PublicAccessBlock
}

var _ Op = (*PutPublicAccessBlock)(nil)

// Name is RGWPutBucketPublicAccessBlock::name.
func (*PutPublicAccessBlock) Name() string { return "put_bucket_public_access_block" }

// Action is s3:PutBucketPublicAccessBlock.
func (*PutPublicAccessBlock) Action() policy.Action { return policy.S3PutBucketPublicAccessBlock }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutPublicAccessBlock) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*PutPublicAccessBlock) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWPutBucketPublicAccessBlock::verify_permission,
// without its ACL fallback.
func (o *PutPublicAccessBlock) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBlockAction(ctx, r, o)
}

// Execute is RGWPutBucketPublicAccessBlock::execute: Params, then the encoded
// block set over the bucket through retryBucketWrite, which authorizes each
// retry again against the bucket it read. radosgw's tries merge the attrs
// the request started with, so a retry writes back what the write it lost to
// had changed
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9637-L9641 at v20.2.4;
// docs/exclusions.md, "A retried PutBucketPolicy keeps what the write it lost
// to changed").
func (o *PutPublicAccessBlock) Execute(ctx context.Context, r *Request) error {
	if o.Params != nil {
		if err := o.Params(ctx, o); err != nil {
			return err
		}
	}
	set := map[string][]byte{AttrPublicAccess: encodeAt(o.Block, r.Env.Zone.Release())}
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, set, nil)
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutPublicAccessBlock) Complete(context.Context, *Request) {}

// DeletePublicAccessBlock is RGWDeleteBucketPublicAccessBlock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8715-L8755 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9683-L9723 at v20.2.4).
type DeletePublicAccessBlock struct{}

var _ Op = (*DeletePublicAccessBlock)(nil)

// Name is RGWDeleteBucketPublicAccessBlock::name.
func (*DeletePublicAccessBlock) Name() string { return "delete_bucket_public_access_block" }

// Action is s3:PutBucketPublicAccessBlock, which
// RGWDeleteBucketPublicAccessBlock authorizes.
func (*DeletePublicAccessBlock) Action() policy.Action { return policy.S3PutBucketPublicAccessBlock }

// OpMask is RGW_OP_TYPE_WRITE, which RGWDeleteBucketPublicAccessBlock
// declares.
func (*DeletePublicAccessBlock) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket.
func (*DeletePublicAccessBlock) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWDeleteBucketPublicAccessBlock::verify_permission,
// without its ACL fallback.
func (o *DeletePublicAccessBlock) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBlockAction(ctx, r, o)
}

// Execute is RGWDeleteBucketPublicAccessBlock::execute: the attr removed
// through retryBucketWrite. A bucket without a block is rewritten too, and
// succeeds, as radosgw's does.
func (o *DeletePublicAccessBlock) Execute(ctx context.Context, r *Request) error {
	return retryBucketWrite(ctx, r, o, func() error {
		return r.Env.Buckets.PutBucketAttrs(ctx, r.BucketRec, nil, []string{AttrPublicAccess})
	})
}

// Complete does nothing: the handler logs usage once the response is written.
func (*DeletePublicAccessBlock) Complete(context.Context, *Request) {}
