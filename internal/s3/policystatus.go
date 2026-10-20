package s3

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// policyStatusRoutes is the bucket policy status route.
func policyStatusRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
		"get_bucket_policy_status": getBucketPolicyStatus,
	}}}
}

// getBucketPolicyStatus is get_bucket_policy_status
// (RGWGetBucketPolicyStatus_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4584-L4602 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5095-L5113 at v20.2.4):
// PolicyStatus in S3's namespace holding IsPublic as dump_bool writes it,
// lower-case true or false: radosgw's comment there says clients want lower
// case and Amazon S3 sends it, where AWS's API reference writes TRUE and FALSE.
func getBucketPolicyStatus(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketPolicyStatus{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, []byte(`<PolicyStatus xmlns="`+xmlnsS3+`"><IsPublic>`+
		strconv.FormatBool(o.IsPublic)+`</IsPublic></PolicyStatus>`))
	return nil
}
