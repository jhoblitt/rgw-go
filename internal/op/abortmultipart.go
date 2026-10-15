package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// AbortMultipart is RGWAbortMultipart, AbortMultipartUpload
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6595-L6650 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7519-L7574 at v20.2.4).
// Execute aborts the upload through MultipartStore.Abort.
type AbortMultipart struct{ UploadID string }

var _ Op = (*AbortMultipart)(nil)

// Name is RGWAbortMultipart::name.
func (o *AbortMultipart) Name() string { return "abort_multipart" }

// Action is the action RGWAbortMultipart::verify_permission authorizes.
func (o *AbortMultipart) Action() policy.Action { return policy.S3AbortMultipartUpload }

// OpMask is RGW_OP_TYPE_DELETE.
func (o *AbortMultipart) OpMask() uint32 { return OpTypeDelete }

// Init is init_permissions' load of the bucket, a missing one NoSuchBucket.
func (o *AbortMultipart) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWAbortMultipart::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6595-L6607 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7519-L7531 at v20.2.4).
func (o *AbortMultipart) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3AbortMultipartUpload, acl.PermFor(policy.S3AbortMultipartUpload))
}

// Execute is RGWAbortMultipart::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6614-L6650 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7538-L7574 at v20.2.4):
// an empty upload id or key is -EINVAL, then the abort.
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
