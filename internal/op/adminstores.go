package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// UsageData is rgw_usage_data.
type UsageData struct{ BytesSent, BytesReceived, Ops, SuccessfulOps uint64 }

// S3SelectUsage is rgw_s3select_usage_data.
type S3SelectUsage struct{ BytesProcessed, BytesReturned uint64 }

// UsageRecord is one rgw_usage_log_entry as user_usage_log_read returns it:
// keyed by the payer when the record has one, else the owner (cls_rgw.cc
// usage_log_read_cb, v19.2.6:3706-3720), already aggregated per (User,
// Bucket) within one page, Epoch being the first record's.
type UsageRecord struct {
	User, Owner, Payer, Bucket string
	Epoch                      uint64
	Total                      UsageData
	Categories                 map[string]UsageData
	S3Select                   S3SelectUsage
}

// UsageIter is RGWUsageIter: the shard index and the class read iterator a
// paged ReadUsage continues from.
type UsageIter struct {
	Index    uint32
	ReadIter string
}

//counterfeiter:generate . UsageReader

// UsageReader reads and trims the usage log; the admin API's usage ops.
type UsageReader interface {
	// ReadUsage is RGWRados::read_usage (rgw_rados.cc:1674-1716): every shard
	// of user (all shards when user is ""), records of bucket only when bucket
	// is non-empty, epochs in [start, end). It returns at most maxRecs records
	// and advances it; truncated says whether a further call has more.
	ReadUsage(ctx context.Context, user, bucket string, start, end uint64, maxRecs uint32, it *UsageIter) (recs []UsageRecord, truncated bool, err error)
	// TrimUsage is RGWRados::trim_usage (:1718-1736) with at most
	// ceil(records/1000)+1 trim rounds per shard, since radosgw's usage
	// trim may never answer ENODATA (trackers #72593, #58136). It succeeds
	// when nothing is left to trim, and also when the bound is reached; the
	// driver logs the second case.
	TrimUsage(ctx context.Context, user, bucket string, start, end uint64) error
}

// CategoryStats is RGWStorageStats for one RGWObjCategory.
type CategoryStats struct{ Size, SizeRounded, SizeUtilized, NumObjects uint64 }

// BucketIndexStats is RGWRados::get_bucket_stats' output (rgw_rados.cc:8872-8914):
// per-category totals over every shard and the three per-shard strings in
// BucketIndexShardsManager::to_string form, "<shard>#<value>" joined by ",".
type BucketIndexStats struct {
	Categories                map[string]CategoryStats // keyed by to_string(RGWObjCategory): "rgw.main", ...
	Ver, MasterVer, MaxMarker string
}

//counterfeiter:generate . BucketAdminStore

// BucketAdminStore is what the admin bucket ops need of the driver and no S3
// op does.
type BucketAdminStore interface {
	IndexStats(ctx context.Context, rec *BucketRecord) (BucketIndexStats, error)
	// ChangeBucketOwner is RGWBucketAdminOp::link's storage steps
	// (driver/rados/rgw_bucket.cc:1093-1184): unlink from the ACL owner, a
	// default ACL for owner with displayName, Info.Owner rewritten, the entry
	// point relinked; with newName the bucket is renamed (a new instance and
	// entry point, the old ones removed). rec is updated in place.
	ChangeBucketOwner(ctx context.Context, rec *BucketRecord, owner meta.Owner, displayName string, newName *meta.BucketID) error
	// UnlinkBucketOwner is RGWBucketCtl::unlink_bucket with update_entrypoint
	// (rgw_bucket.cc:1023): the cls_user entry removed and the entry point
	// written with linked=false.
	UnlinkBucketOwner(ctx context.Context, rec *BucketRecord, owner meta.Owner) error
	// CheckIndex is RGWRados::bucket_check_index (rgw_rados.cc:5481-5514);
	// RebuildIndex is bucket_rebuild_index (:5516-5527).
	CheckIndex(ctx context.Context, rec *BucketRecord) (existing, calculated map[string]CategoryStats, err error)
	RebuildIndex(ctx context.Context, rec *BucketRecord) error
	// RemoveIndexEntries is RGWRados::remove_objs_from_index (:10247).
	RemoveIndexEntries(ctx context.Context, rec *BucketRecord, keys []meta.ObjKey) error
	// ChownBucket is RadosBucket::chown (rgw_sal_rados.cc:699-751), the form
	// account migration uses: unlink the old owner, link the new one, rewrite
	// Info.Owner, and in the ACL replace the old owner's canonical grant by a
	// FULL_CONTROL grant for the new owner and the owner itself; other grants
	// stay. ECANCELED is retried up to 10 times (adopt_user_bucket, rgw_user.cc:1681-1712).
	ChownBucket(ctx context.Context, rec *BucketRecord, owner meta.Owner, displayName string) error
	// RemoveDanglingEntryPoint removes the entry point of tenant/name when
	// the instance it names does not exist and no live rename claims the
	// name, under the version read; nothing else is written but, for a name
	// a rename reserved, a guarded rewrite of that rename's old instance,
	// which a rename resuming concurrently then fails on. A name without an
	// entry point is ErrNoSuchBucket; a live rename's, or one that changed
	// meanwhile, ErrConcurrentModification.
	RemoveDanglingEntryPoint(ctx context.Context, tenant, name string) error
	// SyncOwnerStats is rgw_sync_all_stats (rgw_user.cc:16-55), what
	// user info's sync=true runs: every bucket of owner resynced into the
	// owner's stats, then the sync completed.
	SyncOwnerStats(ctx context.Context, owner meta.Owner) error
	// PurgeBypassGC is RadosBucket::remove_bypass_gc's data pass
	// (rgw_sal_rados.cc:483-590): the index shard headers read as read_stats
	// reads them, the in-flight uploads aborted, then every listed object's
	// tail stripes released directly and its head and index entry removed,
	// instead of queueing the tails for GC. The bucket itself is left for
	// the ordinary purge the caller runs next, as remove_bypass_gc ends in
	// remove(delete_children=true) (:598-605).
	PurgeBypassGC(ctx context.Context, rec *BucketRecord) error
}

//counterfeiter:generate . RealmStore

// RealmStore reads the realm and period objects of the root pool for the
// read-only admin getters and the period config for ratelimit.
type RealmStore interface {
	// GetRealm is RGWRealm::init (rgw_zone.cc:106-145): by id, else by name,
	// else the default realm; a missing object is ErrNoSuchKey.
	GetRealm(ctx context.Context, id, name string) (meta.Realm, error)
	// ListRealms is RGWSI_Zone::list_realms plus read_default_id (svc_zone.cc:421-427,
	// rgw_zone.cc:1092-1105); a missing default is "".
	ListRealms(ctx context.Context) (defaultID string, names []string, err error)
	// GetPeriod is RGWPeriod::init (rgw_period.cc:14-46): an empty periodID is
	// the realm's current period, a zero epoch its latest epoch.
	GetPeriod(ctx context.Context, realmID, periodID string, epoch uint32) (meta.Period, error)
	// GetPeriodConfig reads "period_config.<realm id>" (rgw_zone.cc:616-636,
	// :671-677); a missing object is ErrNotFound.
	GetPeriodConfig(ctx context.Context, realmID string) (meta.PeriodConfig, error)
	PutPeriodConfig(ctx context.Context, realmID string, cfg meta.PeriodConfig) error
}
