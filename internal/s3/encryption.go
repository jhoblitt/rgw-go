package s3

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// encryptionRoutes is the bucket default encryption routes: rgw_rest_s3.cc's
// RGWGetBucketEncryption_ObjStore_S3, RGWPutBucketEncryption_ObjStore_S3 and
// RGWDeleteBucketEncryption_ObjStore_S3. Each is authorized by its op alone.
func encryptionRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
		"get_bucket_encryption":    getBucketEncryption,
		"put_bucket_encryption":    putBucketEncryption,
		"delete_bucket_encryption": deleteBucketEncryption,
	}}}
}

// getBucketEncryption is get_bucket_encryption
// (RGWGetBucketEncryption_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3886-L3904 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4337-L4355 at v20.2.4):
// the stored configuration after the XML declaration dump_start writes, as
// application/xml. An error is its error document alone: send_response
// encodes the configuration only when the op succeeded, and end_header's
// error branch has already started the content, so dump_start adds nothing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L620-L624 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629 at v20.2.4).
func getBucketEncryption(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketEncryption{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, o.Config.MarshalS3XML())
	return nil
}

// putBucketEncryption is put_bucket_encryption, whose op reads and parses
// the body itself once it has authorized the request: 200 with no body and
// no type, as send_response ends the header with neither
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3877-L3884 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4328-L4335 at v20.2.4).
func putBucketEncryption(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.PutBucketEncryption{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// deleteBucketEncryption is delete_bucket_encryption: 204 with no body and
// no type (RGWDeleteBucketEncryption_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3906-L3915 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4357-L4366 at v20.2.4).
func deleteBucketEncryption(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucketEncryption{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}
