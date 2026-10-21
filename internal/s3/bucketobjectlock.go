package s3

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// getObjectLockConfiguration is get_bucket_object_lock
// (RGWGetBucketObjectLock_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4504-L4518 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5015-L5029 at v20.2.4):
// the stored configuration, without S3's namespace, after the XML
// declaration dump_start writes, as application/xml. An error is its error
// document alone: send_response encodes the configuration only when the op
// succeeded, and end_header's error branch has already started the content,
// so dump_start adds nothing.
func getObjectLockConfiguration(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetObjectLockConfiguration{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, objectlock.MarshalConfiguration(o.Lock))
	return nil
}

// putObjectLockConfiguration is put_bucket_object_lock, whose op reads and
// parses the body itself once it has authorized the request and found the
// bucket's versioning enabled: 200 with no body and no type, as
// send_response ends the header with neither
// (RGWPutBucketObjectLock_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4495-L4502 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5006-L5013 at v20.2.4).
func putObjectLockConfiguration(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.PutObjectLockConfiguration{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}
