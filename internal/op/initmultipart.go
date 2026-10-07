package op

import (
	"context"
	"fmt"
	"maps"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// InitMultipart is RGWInitMultipart, CreateMultipartUpload
// (rgw_op.cc:6271-6343 at v19.2.6, :6941-7019 at v20.2.4;
// rgw_rest_s3.cc:3969-4062 at v19.2.6). The handler fills the inputs;
// Execute creates the upload through MultipartStore.CreateUpload.
type InitMultipart struct {
	// Attrs are the handler's request attrs, each NUL-terminated.
	Attrs map[string][]byte
	// ACL is create_s3_policy's: the canned ACL, the grant headers or the
	// default.
	ACL acl.Policy
	// Tags is x-amz-tagging, nil when absent.
	Tags *tags.Set
	// StorageClass is x-amz-storage-class, "" when absent.
	StorageClass string

	UploadID string
	Upload   *Upload

	dest meta.PlacementRule
}

var _ Op = (*InitMultipart)(nil)

// Name is RGWInitMultipart::name.
func (o *InitMultipart) Name() string { return "init_multipart" }

// Action is the action RGWInitMultipart::verify_permission authorizes.
func (o *InitMultipart) Action() policy.Action { return policy.S3PutObject }

// OpMask is RGW_OP_TYPE_WRITE.
func (o *InitMultipart) OpMask() uint32 { return OpTypeWrite }

// Init is init_permissions: the bucket, a missing one NoSuchBucket, and the
// destination placement, the bucket's rule with the request's storage class,
// which the zone must have (rgw_op.cc:576-583 at v19.2.6, :606-613 at
// v20.2.4). A public ACL under the bucket's block of public ACLs is refused
// here too, as PutObject refuses one; radosgw checks no block for
// CreateMultipartUpload, whose ACL the completed object takes
// (docs/exclusions.md).
func (o *InitMultipart) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	if err := checkDestPlacement(r, o.StorageClass); err != nil {
		return err
	}
	o.dest = meta.PlacementRule{StorageClass: o.StorageClass}.InheritFrom(rec.Info.PlacementRule)
	if blockPublicACLs(rec) && o.ACL.IsPublic() {
		return fmt.Errorf("%w: a public acl under a block of public acls", ErrAccessDenied)
	}
	return nil
}

// VerifyPermission is RGWInitMultipart::verify_permission (rgw_op.cc:6271-6286
// at v19.2.6, :6941-6956 at v20.2.4).
func (o *InitMultipart) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
}

// Execute is RGWInitMultipart::execute (rgw_op.cc:6293-6343 at v19.2.6,
// :6963-7019 at v20.2.4): the upload's attrs are the request's with the
// policy and, when the request tags the object with any tag, the tag set,
// and the upload is created for the requester on the destination placement.
// radosgw ends a request without a key there with success and no upload,
// where no S3 route reaches it; rgw-go answers InvalidArgument.
func (o *InitMultipart) Execute(ctx context.Context, r *Request) error {
	if r.Object.Name == "" {
		return ErrInvalidArgument
	}
	release := r.Env.Zone.Release()
	attrs := maps.Clone(o.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrACL] = encodeAt(o.ACL, release)
	if o.Tags != nil && o.Tags.Len() > 0 {
		attrs[tags.Attr] = encodeAt(*o.Tags, release)
	}
	var ownerName string
	if r.Identity.User != nil {
		ownerName = r.Identity.User.DisplayName
	}
	up, err := r.Env.Multipart.CreateUpload(ctx, r.BucketRec, r.Object, UploadParams{
		Owner: r.Identity.Owner, OwnerName: ownerName, Placement: o.dest, Attrs: attrs,
	})
	if err != nil {
		return err
	}
	o.UploadID, o.Upload = up.ID, up
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *InitMultipart) Complete(context.Context, *Request) {}
