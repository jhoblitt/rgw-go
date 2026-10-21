package s3

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// getLifecycle is get_lifecycle (RGWGetLC_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3708-L3726 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4159-L4177 at v20.2.4):
// the stored configuration as lifecycle.Marshal writes it, under
// LifecycleConfiguration in S3's namespace, after the XML declaration
// dump_start writes, as application/xml. An error is its error document
// alone, as send_response encodes the configuration only when the op
// succeeded.
func getLifecycle(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketLifecycle{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, lifecycle.Marshal(o.Config))
	return nil
}

// putLifecycle is put_lifecycle, RGWPutLC_ObjStore_S3, whose op reads and
// parses the body once it has authorized the request: 200 with no body
// under writeEmptyXML's type, as send_response ends the header with
// to_mime_type's type and then calls dump_start
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3728-L3735 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4179-L4186 at v20.2.4).
func putLifecycle(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.PutBucketLifecycle{}, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// deleteLifecycle is delete_lifecycle, RGWDeleteLC_ObjStore_S3: 204 with no
// body under writeEmptyXML's type, whether or not the bucket held a
// configuration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3737-L3747 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4188-L4198 at v20.2.4).
func deleteLifecycle(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucketLifecycle{}, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusNoContent)
	return nil
}
