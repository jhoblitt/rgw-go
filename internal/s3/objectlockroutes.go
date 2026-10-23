package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// objectLockRoutes is the object lock routes: a bucket's lock
// configuration, and an object's retention and legal hold. Each is
// authorized by its op alone.
func objectLockRoutes(*op.Env) []routeSet {
	return []routeSet{
		{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
			"get_bucket_object_lock": getObjectLockConfiguration,
			"put_bucket_object_lock": putObjectLockConfiguration,
		}},
		{scope: op.ScopeObject, handlers: map[string]HandlerFunc{
			"get_obj_retention":  getObjectRetention,
			"put_obj_retention":  putObjectRetention,
			"get_obj_legal_hold": getObjectLegalHold,
			"put_obj_legal_hold": putObjectLegalHold,
		}},
	}
}
