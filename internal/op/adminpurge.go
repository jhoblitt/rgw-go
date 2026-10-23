package op

import (
	"context"
	"errors"
	"fmt"
)

// purgePage is the page RadosBucket::remove lists the bucket in.
const purgePage = 1000

// PurgeBucket is RadosBucket::remove with delete_children
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L350-L468 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L367-L489 at v20.2.4):
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
			if _, delErr := env.Objects.DeleteObject(ctx, rec, res.Entries[i].Key, DeleteParams{}); delErr != nil && !errors.Is(delErr, ErrNoSuchKey) {
				return delErr
			}
		}
		if !res.Truncated {
			break
		}
		p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L470-L608 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L491-L629 at v20.2.4):
// the driver's data pass, whose failure ends the removal before anything
// else runs, then the ordinary purge, remove with delete_children, whose
// result is returned
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L598-L607 at v19.2.6).
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
// bucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L366-L369 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L383-L386 at v20.2.4);
// rgw-go knows no other zonegroup to leave them to, and the RADOS store
// refuses the removal (docs/exclusions.md).
func refuseForeignPurge(env *Env, rec *BucketRecord) error {
	if env.Zone.ZoneGroup().Equals(rec.Info.Zonegroup) {
		return nil
	}
	return fmt.Errorf("%w: bucket %s belongs to zonegroup %q", ErrPermanentRedirect, rec.Info.Bucket.Name, rec.Info.Zonegroup)
}

// refuseVersionedPurge answers NotImplemented for a bucket whose versioning
// was ever enabled, before a purge deletes anything: its listing names the
// null version with no instance, which the store's delete on such a bucket
// answers with a delete marker, not the version's removal, so a purge
// would write markers and never empty the bucket (docs/exclusions.md). It refuses a bucket with object lock and without
// versioning too, a state radosgw never writes and purges as an
// unversioned bucket, which neverVersioned takes for a versioned one, and
// the bypass-gc purge refuses both as the plain purge does.
func refuseVersionedPurge(rec *BucketRecord) error {
	if !neverVersioned(rec) {
		return fmt.Errorf("%w: purging bucket %s, which is or was versioned or has object lock", ErrNotImplemented, rec.Info.Bucket.Name)
	}
	return nil
}
