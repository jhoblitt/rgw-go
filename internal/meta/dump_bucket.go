package meta

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is rgw_data_placement_target::dump (rgw_basic_types.cc:131-136 at
// v19.2.6 and v20.2.4).
func (p DataPlacement) Dump(f formatter.Formatter, _ denc.Release) {
	dumpPool(f, "data_pool", p.DataPool)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	dumpPool(f, "index_pool", p.IndexPool)
}

// Dump is rgw_bucket::dump (rgw_basic_types.cc:144-151 at v19.2.6 and
// v20.2.4).
func (b BucketID) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", b.Name)
	f.DumpString("marker", b.Marker)
	f.DumpString("bucket_id", b.ID)
	f.DumpString("tenant", b.Tenant)
	dumpSection(f, "explicit_placement", b.ExplicitPlacement, rel)
}

// Dump is RGWBucketEntryPoint::dump (driver/rados/rgw_bucket.cc:3580-3591 at
// v19.2.6, :3681-3692 at v20.2.4).
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

// Dump is RGWBucketInfo::dump (rgw_common.cc:2515-2542 at v19.2.6,
// :2577-2604 at v20.2.4). The website configuration and the sync policy are
// carried opaque, so a bucket with either fails the formatter as MarshalJSON
// fails.
func (b BucketInfo) Dump(f formatter.Formatter, rel denc.Release) {
	if b.HasWebsite() {
		f.Fail(fmt.Errorf("%w: website_conf", ErrOpaqueJSON))
		return
	}
	if b.SyncPolicy != nil && !syncPolicyEmpty(b.SyncPolicy) {
		f.Fail(fmt.Errorf("%w: sync_policy", ErrOpaqueJSON))
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
	f.DumpBool("has_website", false)
	f.DumpBool("swift_versioning", b.SwiftVersioning)
	f.DumpString("swift_ver_location", b.SwiftVerLocation)
	f.DumpUnsigned("index_type", uint64(current.Type))
	dumpMap(f, "mdsearch_config", b.MDSearchConfig,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v uint32) { f.DumpUnsigned("val", uint64(v)) })
	f.DumpInt("reshard_status", int64(b.ReshardStatus))
	f.DumpString("new_bucket_instance_id", b.NewBucketInstanceID)
}

// Dump is RGWBucketCompleteInfo::dump (driver/rados/rgw_bucket.cc:2112-2115
// at v19.2.6, :2278-2281 at v20.2.4).
func (c BucketCompleteInfo) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket_info", c.Info, rel)
	dumpAttrs(f, "attrs", c.Attrs)
}
