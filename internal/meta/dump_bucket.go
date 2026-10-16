package meta

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is rgw_data_placement_target::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_basic_types.cc#L131-L136 at v19.2.6
// and v20.2.4).
func (p DataPlacement) Dump(f formatter.Formatter, _ denc.Release) {
	dumpPool(f, "data_pool", p.DataPool)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	dumpPool(f, "index_pool", p.IndexPool)
}

// Dump is rgw_bucket::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_basic_types.cc#L144-L151 at v19.2.6
// and v20.2.4).
func (b BucketID) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", b.Name)
	f.DumpString("marker", b.Marker)
	f.DumpString("bucket_id", b.ID)
	f.DumpString("tenant", b.Tenant)
	dumpSection(f, "explicit_placement", b.ExplicitPlacement, rel)
}

// Dump is RGWBucketEntryPoint::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3580-L3591 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L3681-L3692 at v20.2.4).
func (b BucketEntryPoint) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket", b.Bucket, rel)
	f.DumpString("owner", b.Owner.String())
	dumpTime(f, "creation_time", b.CreationTime)
	f.DumpBool("linked", b.Linked)
	f.DumpBool("has_bucket_info", b.HasBucketInfo)
	if b.HasBucketInfo {
		old := NewBucketInfo()
		if b.OldBucketInfo != nil {
			old = *b.OldBucketInfo
		}
		dumpSection(f, "old_bucket_info", old, rel)
	}
}

// Dump is RGWBucketInfo::dump at v19.2.7
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2515-L2545)
// on both releases, as MarshalJSON writes it. The sync policy is carried
// opaque, so a bucket with one that has groups fails the formatter as
// MarshalJSON fails, and so does an ObjLock that does not decode.
func (b BucketInfo) Dump(f formatter.Formatter, rel denc.Release) {
	if b.SyncPolicy != nil && !syncPolicyEmpty(b.SyncPolicy) {
		f.Fail(fmt.Errorf("%w: sync_policy", ErrOpaqueJSON))
		return
	}
	lock, err := b.ObjectLock()
	if err != nil {
		f.Fail(err)
		return
	}
	current := b.Layout.Current.Layout
	dumpSection(f, "bucket", b.Bucket, rel)
	dumpTime(f, "creation_time", b.CreationTime)
	f.DumpString("owner", b.Owner.String())
	f.DumpUnsigned("flags", uint64(b.Flags))
	f.DumpString("zonegroup", b.Zonegroup)
	f.DumpString("placement_rule", b.PlacementRule.String())
	f.DumpBool("has_instance_obj", b.HasInstanceObj)
	dumpSection(f, "quota", b.Quota, rel)
	f.DumpUnsigned("num_shards", uint64(current.Normal.NumShards))
	f.DumpUnsigned("bi_shard_hash_type", uint64(current.Normal.HashType))
	f.DumpBool("requester_pays", b.RequesterPays)
	f.DumpBool("has_website", b.HasWebsite())
	if b.Website != nil {
		dumpSection(f, "website_conf", *b.Website, rel)
	}
	f.DumpBool("swift_versioning", b.SwiftVersioning)
	f.DumpString("swift_ver_location", b.SwiftVerLocation)
	f.DumpUnsigned("index_type", uint64(current.Type))
	dumpMap(f, "mdsearch_config", b.MDSearchConfig,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v uint32) { f.DumpUnsigned("val", uint64(v)) })
	f.DumpInt("reshard_status", int64(b.ReshardStatus))
	f.DumpString("new_bucket_instance_id", b.NewBucketInstanceID)
	if b.ObjLockEnabled() {
		dumpSection(f, "obj_lock", lock, rel)
	}
}

// Dump is RGWObjectLock::dump at v19.2.7
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L93-L101):
// enabled only when true.
func (l ObjectLock) Dump(f formatter.Formatter, rel denc.Release) {
	if l.Enabled {
		f.DumpBool("enabled", true)
	}
	f.DumpBool("rule_exist", l.Rule != nil)
	if l.Rule != nil {
		dumpSection(f, "rule", *l.Rule, rel)
	}
}

// Dump is ObjectLockRule::dump at v19.2.7
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L56-L58).
func (r ObjectLockRule) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "defaultRetention", r.DefaultRetention, rel)
}

// Dump is DefaultRetention::dump at v19.2.7
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L26-L33):
// the mode, then days when positive, else years.
func (r DefaultRetention) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("mode", r.Mode)
	if r.Days > 0 {
		f.DumpInt("days", int64(r.Days))
	} else {
		f.DumpInt("years", int64(r.Years))
	}
}

// Dump is RGWBucketCompleteInfo::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2112-L2115 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2278-L2281 at v20.2.4).
func (c BucketCompleteInfo) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket_info", c.Info, rel)
	dumpAttrs(f, "attrs", c.Attrs)
}
