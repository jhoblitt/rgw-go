package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// ListMultipartUploads is RGWListBucketMultiparts, ListMultipartUploads
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6696-L6745 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7635-L7684 at v20.2.4;
// rgw_rest.cc:1624-1658 at v19.2.6, :1629-1663 at v20.2.4). The handler fills
// the inputs; Execute lists a page of the bucket's uploads through
// MultipartStore.ListUploads.
type ListMultipartUploads struct {
	Prefix, Delimiter         string
	KeyMarker, UploadIDMarker string
	// MaxUploads is max-uploads as parse_value_and_bound bounds it.
	MaxUploads int
	// EncodingURL is encoding-type=url, which the response applies.
	EncodingURL bool

	Result ListUploadsResult
}

var _ Op = (*ListMultipartUploads)(nil)

// Name is RGWListBucketMultiparts::name.
func (o *ListMultipartUploads) Name() string { return "list_bucket_multiparts" }

// Action is the action RGWListBucketMultiparts::verify_permission authorizes.
func (o *ListMultipartUploads) Action() policy.Action { return policy.S3ListBucketMultipartUploads }

// OpMask is RGW_OP_TYPE_READ.
func (o *ListMultipartUploads) OpMask() uint32 { return OpTypeRead }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket.
func (o *ListMultipartUploads) Init(ctx context.Context, r *Request) error {
	return loadBucket(ctx, r)
}

// VerifyPermission is RGWListBucketMultiparts::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6696-L6708 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7635-L7647 at v20.2.4).
func (o *ListMultipartUploads) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3ListBucketMultipartUploads, acl.PermFor(policy.S3ListBucketMultipartUploads))
}

// Execute is RGWListBucketMultiparts::execute's listing
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6734-L6744 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7673-L7683 at v20.2.4).
func (o *ListMultipartUploads) Execute(ctx context.Context, r *Request) error {
	res, err := r.Env.Multipart.ListUploads(ctx, r.BucketRec, ListUploadsParams{
		Prefix: o.Prefix, Delimiter: o.Delimiter, KeyMarker: o.KeyMarker, UploadIDMarker: o.UploadIDMarker, MaxUploads: o.MaxUploads,
	})
	if err != nil {
		return err
	}
	o.Result = res
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *ListMultipartUploads) Complete(context.Context, *Request) {}
