package op

import "context"

//counterfeiter:generate . LifecycleStore

// LifecycleStore keeps a bucket's entry in the lifecycle shard its key
// hashes to, the list radosgw's lifecycle threads walk. Each call holds the
// shard's lock while it writes, as guard_lc_modify does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2584-L2634 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2651-L2701 at v20.2.4),
// and fails with the lock's error when the shard stays locked through its
// 500 retries, 100 ms apart.
type LifecycleStore interface {
	// SetEntry writes rec's bucket's entry into its shard with status
	// UNINITIAL, set_bucket_config's set_entry
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2659-L2663 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2726-L2730 at v20.2.4).
	SetEntry(ctx context.Context, rec *BucketRecord) error
	// RemoveEntry removes rec's bucket from its shard,
	// remove_bucket_config's rm_entry
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2686-L2690 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2753-L2757 at v20.2.4).
	RemoveEntry(ctx context.Context, rec *BucketRecord) error
}
