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
	Marker  string
	MaxKeys int
	// NS is the index namespace: "" for objects, "multipart" for uploads.
	NS string
	// ListVersions returns every version and delete marker. Versioning is
	// not implemented yet.
	ListVersions bool
	// AllowUnordered is radosgw's non-standard allow-unordered: the shards
	// are listed one after another rather than merged, which no delimiter
	// can accompany (ErrInvalidArgument) and which yields no common
	// prefixes.
	AllowUnordered bool
	// NameFilter, when set, is RGWRados::Bucket::ListParams::
	// access_list_filter: a name in NS it refuses is skipped, uncounted, after
	// the page's next marker has moved past it, as list_objects_ordered and
	// list_objects_unordered skip it (rgw_rados.cc:2003-2009 and :2297-2303 at
	// v19.2.6, :2106-2112 and :2400-2406 at v20.2.4). A name the delimiter
	// rolls into a common prefix still forms it, where radosgw refuses the
	// prefix itself (docs/exclusions.md, "ListMultipartUploads keeps the
	// common prefixes radosgw drops"). ListUploads passes
	// meta.IsMultipartMeta, radosgw's MultipartMetaFilter.
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
	// IsLatest, DeleteMarker and Exists are the versioned-listing flags;
	// versioning is not implemented yet.
	IsLatest     bool
	DeleteMarker bool
	Exists       bool
	// Appendable is that the object was written by AppendObject, which the
	// listings render as its Type.
	Appendable bool
}

// ListObjectsResult is one page of a listing.
type ListObjectsResult struct {
	Entries        []ObjectEntry
	CommonPrefixes []string
	Truncated      bool
	// NextMarker is the marker for the next page when Truncated: the name,
	// in ListObjectsParams.NS, of the last entry or common prefix counted.
	NextMarker string
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
	// (rgw_rados.cc:2417-2449 at v19.2.6, :2525-2557 at v20.2.4), so the op
	// can compare owners; it never re-links the name to a new instance.
	CreateBucket(ctx context.Context, p CreateBucketParams) (*BucketRecord, error)
	// DeleteBucket removes the instance, the entry point and the user's list
	// entry; a bucket with objects fails with ErrBucketNotEmpty.
	DeleteBucket(ctx context.Context, rec *BucketRecord) error
	// PutBucketInfo rewrites the instance from rec.Info, guarded by rec.Version.
	PutBucketInfo(ctx context.Context, rec *BucketRecord) error
	// PutBucketAttrs sets and removes instance xattrs, guarded by rec.Version.
	PutBucketAttrs(ctx context.Context, rec *BucketRecord, set map[string][]byte, rm []string) error
	ListObjects(ctx context.Context, rec *BucketRecord, p ListObjectsParams) (ListObjectsResult, error)
}
