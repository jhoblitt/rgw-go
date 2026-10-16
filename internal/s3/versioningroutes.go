package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// versioningRoutes is the bucket versioning routes.
func versioningRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
		"get_bucket_versioning": getBucketVersioning,
		"set_bucket_versioning": putBucketVersioning,
	}}}
}
