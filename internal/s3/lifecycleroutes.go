package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// lifecycleRoutes is the bucket lifecycle routes: rgw_rest_s3.cc's
// RGWGetLC_ObjStore_S3, RGWPutLC_ObjStore_S3 and RGWDeleteLC_ObjStore_S3.
// Each is authorized by its op alone.
func lifecycleRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
		"get_lifecycle":    getLifecycle,
		"put_lifecycle":    putLifecycle,
		"delete_lifecycle": deleteLifecycle,
	}}}
}
