package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// ListMultipartUploads is RGWListBucketMultiparts, ListMultipartUploads
// (rgw_op.cc:6696-6745 at v19.2.6, :7635-7684 at v20.2.4; rgw_rest.cc:1624-1658
// at v19.2.6, :1629-1663 at v20.2.4). The handler fills the inputs; Execute
// lists a page of the bucket's uploads through MultipartStore.ListUploads.
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
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	return nil
}

// VerifyPermission is RGWListBucketMultiparts::verify_permission
// (rgw_op.cc:6696-6708 at v19.2.6, :7635-7647 at v20.2.4).
func (o *ListMultipartUploads) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3ListBucketMultipartUploads, acl.PermFor(policy.S3ListBucketMultipartUploads))
}

// Execute is RGWListBucketMultiparts::execute's listing (rgw_op.cc:6734-6744
// at v19.2.6, :7673-7683 at v20.2.4).
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
