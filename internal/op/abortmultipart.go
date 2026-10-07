package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// AbortMultipart is RGWAbortMultipart, AbortMultipartUpload
// (rgw_op.cc:6595-6650 at v19.2.6, :7519-7574 at v20.2.4). Execute aborts the
// upload through MultipartStore.Abort.
type AbortMultipart struct{ UploadID string }

var _ Op = (*AbortMultipart)(nil)

// Name is RGWAbortMultipart::name.
func (o *AbortMultipart) Name() string { return "abort_multipart" }

// Action is the action RGWAbortMultipart::verify_permission authorizes.
func (o *AbortMultipart) Action() policy.Action { return policy.S3AbortMultipartUpload }

// OpMask is RGW_OP_TYPE_DELETE.
func (o *AbortMultipart) OpMask() uint32 { return OpTypeDelete }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket.
func (o *AbortMultipart) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	return nil
}

// VerifyPermission is RGWAbortMultipart::verify_permission
// (rgw_op.cc:6595-6607 at v19.2.6, :7519-7531 at v20.2.4).
func (o *AbortMultipart) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3AbortMultipartUpload, acl.PermFor(policy.S3AbortMultipartUpload))
}

// Execute is RGWAbortMultipart::execute (rgw_op.cc:6614-6650 at v19.2.6,
// :7538-7574 at v20.2.4): an empty upload id or key is -EINVAL, then the
// abort.
func (o *AbortMultipart) Execute(ctx context.Context, r *Request) error {
	if o.UploadID == "" || r.Object.Name == "" {
		return ErrInvalidArgument
	}
	if err := aliasedUpload(o.UploadID); err != nil {
		return err
	}
	return r.Env.Multipart.Abort(ctx, &Upload{ID: o.UploadID, Bucket: r.BucketRec, Key: r.Object})
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *AbortMultipart) Complete(context.Context, *Request) {}
