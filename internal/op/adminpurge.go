package op

import (
	"context"
	"errors"
)

// purgePage is the page RadosBucket::remove lists the bucket in.
const purgePage = 1000

// PurgeBucket is RadosBucket::remove with delete_children
// (driver/rados/rgw_sal_rados.cc:350-468 at v19.2.6, :367-489 at v20.2.4):
// every entry of the plain namespace, versions included, listed unordered
// and deleted, an entry already gone skipped as -ENOENT is; then
// DeleteBucket, which aborts the in-flight uploads as remove's
// abort_multiparts does, so the multipart namespace is never deleted entry
// by entry. DeleteBucket refuses a bucket an object reached after the
// listing, where radosgw's delete_bucket does not check.
func PurgeBucket(ctx context.Context, env *Env, rec *BucketRecord) error {
	p := ListObjectsParams{ListVersions: true, AllowUnordered: true, MaxKeys: purgePage}
	for {
		res, err := env.Buckets.ListObjects(ctx, rec, p)
		if err != nil {
			return err
		}
		for i := range res.Entries {
			if delErr := env.Objects.DeleteObject(ctx, rec, res.Entries[i].Key, DeleteParams{}); delErr != nil && !errors.Is(delErr, ErrNoSuchKey) {
				return delErr
			}
		}
		if !res.Truncated {
			break
		}
		p.Marker = res.NextMarker
	}
	return env.Buckets.DeleteBucket(ctx, rec)
}

// DeleteBucketWithChildren deletes rec, purging it first when purge is set.
func DeleteBucketWithChildren(ctx context.Context, env *Env, rec *BucketRecord, purge bool) error {
	if purge {
		return PurgeBucket(ctx, env, rec)
	}
	return env.Buckets.DeleteBucket(ctx, rec)
}
