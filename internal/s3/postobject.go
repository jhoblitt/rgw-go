package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// postObjectRoutes is the browser-based POST object route. It takes
// auth because radosgw authenticates the form inside the op, in
// RGWPostObj_ObjStore_S3::get_policy
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3190 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3326 at v20.2.4).
func postObjectRoutes(env *op.Env, auth Authenticator) []routeSet { return nil }
