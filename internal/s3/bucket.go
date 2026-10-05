package s3

// bucketHandlers is the bucket-scope routes; each entry binds to bucket scope.
func bucketHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"list_bucket":    listObjects,
		"list_bucket_v2": listObjectsV2,
	}
}
