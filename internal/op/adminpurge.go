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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L350-L468 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L367-L489 at v20.2.4):
// PurgeObjects, then DeleteBucket, which aborts the in-flight uploads as
// remove's abort_multiparts does, so the multipart namespace is never
// deleted entry by entry. DeleteBucket refuses a bucket an object reached
// after the listing, where radosgw's delete_bucket does not check.
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
	if err := PurgeObjects(ctx, env, rec); err != nil {
		return err
	}
	return env.Buckets.DeleteBucket(ctx, rec)
}

// PurgeObjects is RadosBucket::remove's delete_children pass
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L362-L393 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L379-L410 at v20.2.4):
// every entry of the plain namespace, versions included, listed unordered a
// page at a time and deleted, an entry already gone skipped as -ENOENT is.
// It checks nothing about rec; PurgeBucket's refusals come first.
//
// On a bucket whose flags hold BUCKET_VERSIONED each entry is deleted by
// its exact version, so the pass writes no delete marker: an entry listed
// without an instance, the null version or an object written before
// versioning was enabled, is named null, as radosgw's lifecycle names it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L498-L505 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L506-L513 at v20.2.4),
// where radosgw's purge deletes its plain key and so writes a delete marker
// (docs/ceph-upstream-bugs.md, "radosgw's bucket purge writes a delete
// marker in place of a versioned bucket's null version"). Each next page of
// such a bucket starts at the first version left of the name the page ended
// on (ListObjectsParams.RelistMarker): the next marker names the version
// the page deleted last, and a marker whose version is gone starts past the
// rest of its name (ListObjectsResult.NextMarkerInstance), so paging by it
// would leave those versions behind, as radosgw's purge does
// (docs/exclusions.md, "A purging removal resumes each page of a versioned
// bucket at its last key's remaining versions"). Two pages in a row that
// delete nothing and start at the same entry are a key holding more
// versions the pass cannot remove than a page, and the next page starts
// past every version of that key, as radosgw's next marker passes it. The
// pass ends with the listing, the entries it could not remove left for
// DeleteBucket to refuse. Any other bucket is paged by the next marker, as
// radosgw pages it: a marker without an instance starts after its name
// whether or not the entry is still there.
func PurgeObjects(ctx context.Context, env *Env, rec *BucketRecord) error {
	versioned := rec.Info.Versioned()
	p := ListObjectsParams{ListVersions: true, AllowUnordered: true, MaxKeys: purgePage}
	var (
		idle      bool // the last page deleted nothing
		idleFirst meta.ObjKey
	)
	for {
		res, err := env.Buckets.ListObjects(ctx, rec, p)
		if err != nil {
			return err
		}
		deleted := 0
		for i := range res.Entries {
			key := res.Entries[i].Key
			if versioned && key.Instance == "" {
				key.Instance = meta.NullInstance
			}
			_, delErr := env.Objects.DeleteObject(ctx, rec, key, DeleteParams{})
			switch {
			case delErr == nil:
				deleted++
			case !errors.Is(delErr, ErrNoSuchKey):
				return delErr
			}
		}
		if !res.Truncated {
			return nil
		}
		if !versioned {
			p.Marker, p.MarkerInstance = res.NextMarker, res.NextMarkerInstance
			continue
		}
		var first meta.ObjKey
		if len(res.Entries) > 0 {
			first = res.Entries[0].Key
		}
		if deleted == 0 && idle && first == idleFirst {
			idle = false
			p.Marker, p.MarkerInstance, p.RelistMarker = first.Name, "", false
			continue
		}
		idle, idleFirst = deleted == 0, first
		p.Marker, p.MarkerInstance, p.RelistMarker = res.NextMarker, "", true
	}
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
// was ever enabled, before a purge deletes anything, as the admin removal
// routes do not serve such a bucket (docs/exclusions.md, "A purging bucket
// removal refuses a bucket whose versioning was ever enabled"). It refuses
// a bucket with object lock and without
// versioning too, a state radosgw never writes and purges as an
// unversioned bucket, which neverVersioned takes for a versioned one, and
// the bypass-gc purge refuses both as the plain purge does.
func refuseVersionedPurge(rec *BucketRecord) error {
	if !neverVersioned(rec) {
		return fmt.Errorf("%w: purging bucket %s, which is or was versioned or has object lock", ErrNotImplemented, rec.Info.Bucket.Name)
	}
	return nil
}
