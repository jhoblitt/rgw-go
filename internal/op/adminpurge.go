package op

import (
	"context"
	"errors"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/meta"
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
	if err := refuseUnfinishedRename(ctx, env, rec); err != nil {
		return err
	}
	if err := refuseForeignPurge(env, rec); err != nil {
		return err
	}
	if err := refuseVersionedPurge(rec); err != nil {
		return err
	}
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
// Each removal path refuses a bucket an unfinished rename marks
// (refuseUnfinishedRename) before it deletes anything.
func DeleteBucketWithChildren(ctx context.Context, env *Env, rec *BucketRecord, purge bool) error {
	if purge {
		return PurgeBucket(ctx, env, rec)
	}
	if err := refuseUnfinishedRename(ctx, env, rec); err != nil {
		return err
	}
	return env.Buckets.DeleteBucket(ctx, rec)
}

// RemoveBucketBypassGC is RadosBucket::remove_bypass_gc
// (driver/rados/rgw_sal_rados.cc:470-608 at v19.2.6, :491-629 at v20.2.4):
// the driver's data pass, whose failure ends the removal before anything
// else runs, then the ordinary purge, remove with delete_children, whose
// result is returned (:598-607 at v19.2.6).
func RemoveBucketBypassGC(ctx context.Context, env *Env, rec *BucketRecord) error {
	if err := refuseUnfinishedRename(ctx, env, rec); err != nil {
		return err
	}
	if err := refuseForeignPurge(env, rec); err != nil {
		return err
	}
	if err := refuseVersionedPurge(rec); err != nil {
		return err
	}
	if err := env.BucketAdmin.PurgeBypassGC(ctx, rec); err != nil {
		return err
	}
	return PurgeBucket(ctx, env, rec)
}

// refuseForeignPurge answers PermanentRedirect for a bucket of another
// zonegroup, as RGWZoneGroup::equals tells it, before a purge deletes
// anything. radosgw's remove neither lists nor deletes such a bucket's
// objects, which it leaves to the zonegroup that owns them, and removes the
// bucket (rgw_sal_rados.cc:366-369 at v19.2.6, :383-386 at v20.2.4); rgw-go
// knows no other zonegroup to leave them to, and the RADOS store refuses
// the removal (docs/exclusions.md).
func refuseForeignPurge(env *Env, rec *BucketRecord) error {
	if env.Zone.ZoneGroup().Equals(rec.Info.Zonegroup) {
		return nil
	}
	return fmt.Errorf("%w: bucket %s belongs to zonegroup %q", ErrPermanentRedirect, rec.Info.Bucket.Name, rec.Info.Zonegroup)
}

// refuseVersionedPurge answers NotImplemented for a bucket whose versioning
// was ever enabled, before a purge deletes anything: its listing returns
// object instances and their olh heads, which no delete here removes as
// radosgw's versioned delete does, so a purge would release some versions
// and then fail on an olh head (docs/exclusions.md).
func refuseVersionedPurge(rec *BucketRecord) error {
	if rec.Info.Flags&(meta.BucketVersioned|meta.BucketVersionsSuspended) != 0 {
		return fmt.Errorf("%w: purging bucket %s, whose versioning was enabled", ErrNotImplemented, rec.Info.Bucket.Name)
	}
	return nil
}
