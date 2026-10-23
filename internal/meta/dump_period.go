package meta

import (
	"fmt"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWRealm::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_realm.cc#L251-L256 at v19.2.6
// and v20.2.4) over RGWSystemMetaObj::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L812-L816 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L822-L826 at v20.2.4).
func (r Realm) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("id", r.ID)
	f.DumpString("name", r.Name)
	f.DumpString("current_period", r.CurrentPeriod)
	f.DumpUnsigned("epoch", uint64(r.Epoch))
}

// Dump is RGWPeriod::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_period.cc#L234-L246 at v19.2.6
// and v20.2.4).
func (p Period) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", p.ID)
	f.DumpUnsigned("epoch", uint64(p.Epoch))
	f.DumpString("predecessor_uuid", p.PredecessorUUID)
	dumpStrings(f, "sync_status", p.SyncStatus)
	dumpSection(f, "period_map", p.PeriodMap, rel)
	f.DumpString("master_zonegroup", p.MasterZoneGroup)
	f.DumpString("master_zone", p.MasterZone)
	dumpSection(f, "period_config", p.PeriodConfig, rel)
	f.DumpString("realm_id", p.RealmID)
	f.DumpUnsigned("realm_epoch", uint64(p.RealmEpoch))
}

// Dump is RGWPeriodMap::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L1000-L1005 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L1040-L1045 at v20.2.4).
func (m PeriodMap) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", m.ID)
	dumpValues(f, "zonegroups", m.ZoneGroups, rel)
	dumpMap(f, "short_zone_ids", m.ShortZoneIDs,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v uint32) { f.DumpUnsigned("val", uint64(v)) })
}

// dumpValues is encode_json_map(name, m): the values alone, each an "obj"
// section, in key order (ceph_json.h at v19.2.6 and v20.2.4).
func dumpValues[V Dumper](f formatter.Formatter, name string, m map[string]V, rel denc.Release) {
	f.OpenArraySection(name)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		dumpSection(f, "obj", m[k], rel)
	}
	f.CloseSection()
}

// Dump is RGWPeriodConfig::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L662-L669 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L670-L677 at v20.2.4).
func (c PeriodConfig) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket_quota", c.BucketQuota, rel)
	dumpSection(f, "user_quota", c.UserQuota, rel)
	dumpSection(f, "user_ratelimit", c.UserRateLimit, rel)
	dumpSection(f, "bucket_ratelimit", c.BucketRateLimit, rel)
	dumpSection(f, "anonymous_ratelimit", c.AnonRateLimit, rel)
}

// Dump is RGWRateLimitInfo::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2766-L2773 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2828-L2835 at v20.2.4);
// neither release has main's list and delete limits.
func (l RateLimitInfo) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpInt("max_read_ops", l.MaxReadOps)
	f.DumpInt("max_write_ops", l.MaxWriteOps)
	f.DumpInt("max_read_bytes", l.MaxReadBytes)
	f.DumpInt("max_write_bytes", l.MaxWriteBytes)
	f.DumpBool("enabled", l.Enabled)
}

// Dump is RGWZoneGroup::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L735-L750 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L743-L758 at v20.2.4).
// A sync policy with groups is carried opaque, so it fails the formatter as
// MarshalJSON fails; an empty one is rgw_sync_policy_info::dump's empty
// "groups" array
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_sync_policy.cc#L777-L783 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_sync_policy.cc#L784-L790 at v20.2.4).
func (g ZoneGroup) Dump(f formatter.Formatter, rel denc.Release) {
	if !g.SyncPolicy.Empty() {
		f.Fail(fmt.Errorf("%w: zonegroup %s sync policy groups", ErrOpaqueJSON, g.ID))
		return
	}
	f.DumpString("id", g.ID)
	f.DumpString("name", g.Name)
	f.DumpString("api_name", g.APIName)
	f.DumpBool("is_master", g.IsMaster)
	dumpStrings(f, "endpoints", g.Endpoints)
	dumpStrings(f, "hostnames", g.Hostnames)
	dumpStrings(f, "hostnames_s3website", g.HostnamesS3Website)
	f.DumpString("master_zone", g.MasterZone)
	dumpValues(f, "zones", g.Zones, rel)
	dumpValues(f, "placement_targets", g.PlacementTargets, rel)
	f.DumpString("default_placement", g.DefaultPlacement.String())
	f.DumpString("realm_id", g.RealmID)
	f.OpenObjectSection("sync_policy")
	f.OpenArraySection("groups")
	f.CloseSection()
	f.CloseSection()
	dumpStrings(f, "enabled_features", g.EnabledFeatures)
}

// Dump is RGWZone::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L71-L85 at v19.2.6
// and v20.2.4).
func (z Zone) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("id", z.ID)
	f.DumpString("name", z.Name)
	dumpStrings(f, "endpoints", z.Endpoints)
	f.DumpBool("log_meta", z.LogMeta)
	f.DumpBool("log_data", z.LogData)
	f.DumpUnsigned("bucket_index_max_shards", uint64(z.BucketIndexMaxShards))
	f.DumpBool("read_only", z.ReadOnly)
	f.DumpString("tier_type", z.TierType)
	f.DumpBool("sync_from_all", z.SyncFromAll)
	dumpStrings(f, "sync_from", z.SyncFrom)
	f.DumpString("redirect_zone", z.RedirectZone)
	dumpStrings(f, "supported_features", z.SupportedFeatures)
}

// Dump is RGWZoneGroupPlacementTarget::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L848-L856 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L858-L866 at v20.2.4),
// tier_targets only when there are any.
func (t ZoneGroupPlacementTarget) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", t.Name)
	dumpStrings(f, "tags", t.Tags)
	dumpStrings(f, "storage_classes", t.StorageClasses)
	if len(t.TierTargets) > 0 {
		dumpMap(f, "tier_targets", t.TierTargets,
			func(f formatter.Formatter, k string) { f.DumpString("key", k) },
			func(f formatter.Formatter, v ZoneGroupPlacementTier) { dumpSection(f, "val", v, rel) })
	}
}

// Dump is RGWZoneGroupPlacementTier::dump. Squid writes the S3 config for
// cloud-s3 alone
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L895-L904 at v19.2.6);
// Tentacle writes it for either cloud type, then the read-through fields
// and, for cloud-s3-glacier, the glacier config
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L929-L944 at v20.2.4).
func (t ZoneGroupPlacementTier) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("tier_type", t.TierType)
	f.DumpString("storage_class", t.StorageClass)
	f.DumpBool("retain_head_object", t.RetainHeadObject)
	if rel < denc.Tentacle {
		if t.TierType == TierTypeCloudS3 {
			dumpSection(f, "s3", t.S3, rel)
		}
		return
	}
	if t.IsTierTypeS3() {
		dumpSection(f, "s3", t.S3, rel)
	}
	f.DumpBool("allow_read_through", t.AllowReadThrough)
	f.DumpUnsigned("read_through_restore_days", t.ReadThroughRestoreDays)
	f.DumpString("restore_storage_class", t.RestoreStorageClass)
	if t.isGlacier() {
		dumpSection(f, "s3-glacier", t.S3Glacier, rel)
	}
}

// Dump is RGWZoneGroupPlacementTierS3::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L966-L979 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L1006-L1019 at v20.2.4),
// which writes the tier's secret key.
func (s ZoneGroupPlacementTierS3) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("endpoint", s.Endpoint)
	f.DumpString("access_key", s.Key.ID)
	f.DumpString("secret", s.Key.Secret)
	f.DumpString("region", s.Region)
	style := "path"
	if s.HostStyle != 0 {
		style = "virtual"
	}
	f.DumpString("host_style", style)
	f.DumpString("target_storage_class", s.TargetStorageClass)
	f.DumpString("target_path", s.TargetPath)
	dumpMap(f, "acl_mappings", s.ACLMappings,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v TierACLMapping) { dumpSection(f, "val", v, rel) })
	f.DumpUnsigned("multipart_sync_threshold", s.MultipartSyncThreshold)
	f.DumpUnsigned("multipart_min_part_size", s.MultipartMinPartSize)
}

// Dump is RGWTierACLMapping::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L981-L998 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L1021-L1038 at v20.2.4).
func (m TierACLMapping) Dump(f formatter.Formatter, _ denc.Release) {
	typ := "id"
	switch m.Type {
	case aclTypeEmailUser:
		typ = "email"
	case aclTypeGroup:
		typ = "uri"
	}
	f.DumpString("type", typ)
	f.DumpString("source_id", m.SourceID)
	f.DumpString("dest_id", m.DestID)
}

// Dump is RGWZoneGroupTierS3Glacier::dump
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L910-L915 at v20.2.4),
// Expedited for any tier type but Standard's 0.
func (g ZoneGroupTierS3Glacier) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpUnsigned("glacier_restore_days", g.RestoreDays)
	tier := "Standard"
	if g.RestoreTierType != 0 {
		tier = "Expedited"
	}
	f.DumpString("glacier_restore_tier_type", tier)
}
