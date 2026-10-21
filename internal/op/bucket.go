package op

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// BucketRecord is a bucket as stored: entry point, instance, the instance's
// xattrs (ACL, policy and tags among them) and both cls_versions.
type BucketRecord struct {
	EntryPoint meta.BucketEntryPoint
	Info       meta.BucketInfo
	Attrs      map[string][]byte
	Version    meta.ObjVersion // of the instance
	EPVersion  meta.ObjVersion // of the entry point
	Mtime      time.Time
}

// CreateBucketParams is what CreateBucket needs to lay a bucket out.
type CreateBucketParams struct {
	Tenant, Name string
	Owner        meta.Owner
	Zonegroup    string
	Placement    meta.PlacementRule
	// Attrs are the instance xattrs to write, the ACL among them.
	Attrs map[string][]byte
	Quota meta.Quota
	// ObjectLock is create_bucket's obj_lock_enabled: the instance is
	// written with BUCKET_VERSIONED and BUCKET_OBJ_LOCK_ENABLED, and its
	// object lock is RGWObjectLock(), enabled with no rule.
	ObjectLock bool
	// Exclusive asks for an exclusive create, which every create is: see
	// BucketStore.CreateBucket for an existing name.
	Exclusive bool
}

// ListObjectsParams is one page of a listing over the bucket index.
type ListObjectsParams struct {
	Prefix    string
	Delimiter string
	// Marker is the key name, in NS, to start after; "" starts at the
	// beginning.
	Marker string
	// MarkerInstance is the version of Marker to start after, a
	// version-id-marker as sent. In a versions listing with a Marker, ""
	// starts past every version of Marker, as S3 does for a key-marker
	// alone, where radosgw lists them again (docs/exclusions.md, "A versions
	// listing starts after every version of a key-marker given alone");
	// "null" starts after the null version, a null delete marker included
	// (docs/exclusions.md, "A versions listing resumes after the null delete
	// marker its version-id-marker names"), or past every version when there
	// is none; an instance starts after that version, or past every version
	// of Marker when it is gone, as cls_rgw resolves its start key
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L440 at v20.2.4).
	MarkerInstance string
	MaxKeys        int
	// NS is the index namespace: "" for objects, "multipart" for uploads.
	NS string
	// ListVersions returns every version and delete marker.
	ListVersions bool
	// AllowUnordered is radosgw's non-standard allow-unordered: the shards
	// are listed one after another rather than merged, which no delimiter
	// can accompany (ErrInvalidArgument) and which yields no common
	// prefixes.
	AllowUnordered bool
	// NameFilter, when set, is RGWRados::Bucket::ListParams::
	// access_list_filter: a name in NS it refuses is skipped, uncounted, after
	// the page's next marker has moved past it, as list_objects_ordered and
	// list_objects_unordered skip it
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2003-L2009
	// and
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2297-L2303 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2106-L2112
	// and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2400-L2406 at v20.2.4).
	// A name the delimiter rolls into a common prefix still forms it, where
	// radosgw refuses the prefix itself (docs/exclusions.md,
	// "ListMultipartUploads keeps the common prefixes radosgw drops").
	// ListUploads passes meta.IsMultipartMeta, radosgw's MultipartMetaFilter.
	NameFilter func(name string) bool
}

// ObjectEntry is one bucket index entry as a listing returns it.
type ObjectEntry struct {
	Key              meta.ObjKey
	Size             uint64
	Mtime            time.Time
	ETag             string
	Owner            meta.Owner
	OwnerDisplayName string
	StorageClass     string
	// IsLatest, DeleteMarker and Exists are the versioned-listing flags.
	IsLatest     bool
	DeleteMarker bool
	Exists       bool
	// Appendable is that the object was written by AppendObject, which the
	// listings render as its Type.
	Appendable bool
	// VersionedEpoch is the entry's versioned_epoch, the OLH epoch its
	// version was linked at, 0 for an unversioned object.
	VersionedEpoch uint64
	// Category is the entry's RGWObjCategory, as cls/rgw names them.
	Category uint8
}

// ListObjectsResult is one page of a listing.
type ListObjectsResult struct {
	Entries        []ObjectEntry
	CommonPrefixes []string
	Truncated      bool
	// NextMarker is the marker for the next page when Truncated: the name,
	// in ListObjectsParams.NS, of the last entry or common prefix counted.
	NextMarker string
	// NextMarkerInstance is that entry's instance: "null" for the null
	// version, as S3's NextVersionIdMarker names it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1817-L1825 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1920-L1928 at v20.2.4),
	// and empty for an unversioned object and a common prefix. A listing
	// resumes after the page with NextMarker and NextMarkerInstance copied
	// back unchanged as Marker and MarkerInstance. A caller that deletes what
	// it lists restarts each page from an empty marker instead, or lists until
	// a pass finds nothing: a marker whose version is gone starts past the
	// rest of its name.
	NextMarkerInstance string
}

//counterfeiter:generate . BucketStore

// BucketStore reads and writes bucket entry points and instances and lists
// the bucket index.
type BucketStore interface {
	GetBucket(ctx context.Context, tenant, name string) (*BucketRecord, error)
	GetBucketInstance(ctx context.Context, id meta.BucketID) (*BucketRecord, error)
	// CreateBucket writes the instance and the entry point exclusively, as
	// RGWRados::create_bucket always does. When the name exists, Exclusive or
	// not, it returns the EXISTING record together with ErrBucketAlreadyExists,
	// as create_bucket re-reads and returns the bucket it lost to
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2417-L2449 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2525-L2557 at v20.2.4),
	// so the op can compare owners; it never re-links the name to a new
	// instance.
	CreateBucket(ctx context.Context, p CreateBucketParams) (*BucketRecord, error)
	// DeleteBucket removes the instance, the entry point and the user's list
	// entry; a bucket with objects fails with ErrBucketNotEmpty, and one
	// whose zonegroup meta.ZoneGroup.Equals does not take for this one with
	// ErrPermanentRedirect, before the store reads its index or removes
	// anything: rgw-go has no zonegroup to leave its objects to.
	DeleteBucket(ctx context.Context, rec *BucketRecord) error
	// PutBucketInfo rewrites the instance from rec.Info, guarded by rec.Version.
	PutBucketInfo(ctx context.Context, rec *BucketRecord) error
	// PutBucketAttrs sets and removes instance xattrs, guarded by rec.Version.
	PutBucketAttrs(ctx context.Context, rec *BucketRecord, set map[string][]byte, rm []string) error
	ListObjects(ctx context.Context, rec *BucketRecord, p ListObjectsParams) (ListObjectsResult, error)
}
