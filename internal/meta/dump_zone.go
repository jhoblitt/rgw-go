package meta

import (
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWZoneParams::dump (rgw_zone.cc:309-334 at v19.2.6; :312-340 at
// v20.2.4, which adds dedup_pool, bucket_logging_pool and restore_pool).
// Neither release dumps the OIDC or vector pool.
func (z ZoneParams) Dump(f formatter.Formatter, rel denc.Release) {
	// RGWSystemMetaObj::dump (rgw_zone.cc:812-816 at v19.2.6).
	f.DumpString("id", z.ID)
	f.DumpString("name", z.Name)
	dumpPool(f, "domain_root", z.DomainRoot)
	dumpPool(f, "control_pool", z.ControlPool)
	if rel >= denc.Tentacle {
		dumpPool(f, "dedup_pool", z.DedupPool)
	}
	for _, p := range []struct {
		name string
		pool Pool
	}{
		{"gc_pool", z.GCPool},
		{"lc_pool", z.LCPool},
		{"log_pool", z.LogPool},
		{"intent_log_pool", z.IntentLogPool},
		{"usage_log_pool", z.UsageLogPool},
		{"roles_pool", z.RolesPool},
		{"reshard_pool", z.ReshardPool},
		{"user_keys_pool", z.UserKeysPool},
		{"user_email_pool", z.UserEmailPool},
		{"user_swift_pool", z.UserSwiftPool},
		{"user_uid_pool", z.UserUIDPool},
		{"otp_pool", z.OTPPool},
		{"notif_pool", z.NotifPool},
		{"topics_pool", z.TopicsPool},
		{"account_pool", z.AccountPool},
		{"group_pool", z.GroupPool},
	} {
		dumpPool(f, p.name, p.pool)
	}
	if rel >= denc.Tentacle {
		dumpPool(f, "bucket_logging_pool", z.BucketLoggingPool)
	}
	// encode_json_plain: RGWAccessKey::dump_plain in an object section
	// (rgw_zone.cc:43-48, rgw_common.cc:2968-2972 at v19.2.6).
	f.OpenObjectSection("system_key")
	f.DumpString("access_key", z.SystemKey.ID)
	f.DumpString("secret_key", z.SystemKey.Secret)
	f.CloseSection()
	dumpMap(f, "placement_pools", z.PlacementPools,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, p ZonePlacementInfo) { dumpSection(f, "val", p, rel) })
	z.TierConfig.DumpAs(f, "tier_config")
	f.DumpString("realm_id", z.RealmID)
	if rel >= denc.Tentacle {
		dumpPool(f, "restore_pool", z.RestorePool)
	}
}

// Dump is RGWZonePlacementInfo::dump (rgw_zone.cc:763-773 at v19.2.6,
// :771-781 at v20.2.4).
func (p ZonePlacementInfo) Dump(f formatter.Formatter, rel denc.Release) {
	dumpPool(f, "index_pool", p.IndexPool)
	dumpSection(f, "storage_classes", p.StorageClasses, rel)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	f.DumpUnsigned("index_type", uint64(p.IndexType))
	f.DumpBool("inline_data", p.InlineData)
}

// Dump is RGWZoneStorageClasses::dump (rgw_zone.cc:869-874 at v19.2.6,
// :884-889 at v20.2.4): one section per class, named by the class, in key
// order.
func (c ZoneStorageClasses) Dump(f formatter.Formatter, rel denc.Release) {
	for _, name := range slices.Sorted(maps.Keys(c)) {
		dumpSection(f, name, c[name], rel)
	}
}

// Dump is RGWZoneStorageClass::dump (rgw_zone.cc:926-934 at v19.2.6,
// :966-974 at v20.2.4).
func (s ZoneStorageClass) Dump(f formatter.Formatter, _ denc.Release) {
	if s.DataPool != nil {
		dumpPool(f, "data_pool", *s.DataPool)
	}
	if s.CompressionType != nil {
		f.DumpString("compression_type", *s.CompressionType)
	}
}

// DumpAs is encode_json(name, JSONFormattable) (ceph_json.cc:922-946 at
// v19.2.6): a value quoted or bare as it was stored (:29-36), an array whose
// entries are named "obj", an object whose members carry their keys, in key
// order; nothing for FMT_NONE or a type byte past FMT_OBJ.
func (j JSONFormattable) DumpAs(f formatter.Formatter, name string) {
	switch j.Type {
	case FormattableValue:
		if j.Quoted {
			f.DumpString(name, j.Value)
		} else {
			f.DumpUnquoted(name, j.Value)
		}
	case FormattableArray:
		f.OpenArraySection(name)
		for _, e := range j.Array {
			e.DumpAs(f, "obj")
		}
		f.CloseSection()
	case FormattableObject:
		f.OpenObjectSection(name)
		for _, k := range slices.Sorted(maps.Keys(j.Object)) {
			j.Object[k].DumpAs(f, k)
		}
		f.CloseSection()
	}
}
