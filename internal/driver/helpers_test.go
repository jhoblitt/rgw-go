package driver_test

import (
	"maps"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// encoder is a meta type radosgw stores in RADOS.
type encoder interface {
	Encode(e *denc.Encoder, r denc.Release)
}

// encode renders a meta type as radosgw stores it at the Squid release.
func encode(v encoder) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

// rookZone is the ids seedRookZone chose.
type rookZone struct{ realmID, zgID, zoneID, periodID string }

// seedRookZone seeds the root pool with a single-zone realm in Rook's zoned
// shape and returns the ids it chose: realm, zonegroup and zone share the
// store's name, the zone's pools are namespaces of shared pools, and with
// withPeriod the realm's period is committed.
func seedRookZone(c *fakerados.Cluster, store string, withPeriod bool) rookZone {
	ids := rookZone{realmID: "realm-" + store, zgID: "zg-" + store, zoneID: "zone-" + store, periodID: "period-" + store}
	zone := meta.NewZone()
	zone.ID, zone.Name = ids.zoneID, store
	zg := meta.ZoneGroup{
		ID: ids.zgID, Name: store, APIName: store, IsMaster: true, MasterZone: ids.zoneID,
		Zones: map[string]meta.Zone{ids.zoneID: zone}, RealmID: ids.realmID,
		DefaultPlacement: meta.PlacementRule{Name: "default-placement"},
		PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{
			"default-placement": {Name: "default-placement", StorageClasses: []string{meta.StorageClassStandard}},
		},
	}
	params := meta.ZoneParams{
		ID: ids.zoneID, Name: store, RealmID: ids.realmID,
		DomainRoot:    meta.ParsePool(store + ".rgw.meta:root"),
		ControlPool:   meta.ParsePool(store + ".rgw.control"),
		UserUIDPool:   meta.ParsePool(store + ".rgw.meta:users.uid"),
		UserKeysPool:  meta.ParsePool(store + ".rgw.meta:users.keys"),
		UserEmailPool: meta.ParsePool(store + ".rgw.meta:users.email"),
		UserSwiftPool: meta.ParsePool(store + ".rgw.meta:users.swift"),
		AccountPool:   meta.ParsePool(store + ".rgw.meta:accounts"),
		LogPool:       meta.ParsePool(store + ".rgw.log"),
		UsageLogPool:  meta.ParsePool(store + ".rgw.log:usage"),
		PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": {
			IndexPool:     meta.ParsePool(store + ".rgw.buckets.index"),
			DataExtraPool: meta.ParsePool(store + ".rgw.buckets.non-ec"),
			StorageClasses: meta.ZoneStorageClasses{
				meta.StorageClassStandard: {DataPool: new(meta.ParsePool(store + ".rgw.buckets.data"))},
			},
		}},
	}
	realm := meta.Realm{ID: ids.realmID, Name: store, Epoch: 1}
	if withPeriod {
		realm.CurrentPeriod = ids.periodID
	}
	root := meta.RootPool
	c.Put(root, "", meta.RealmNameOID(store), encode(meta.NameToID{ObjID: ids.realmID}))
	c.Put(root, "", meta.RealmOID(ids.realmID), encode(realm))
	c.Put(root, "", meta.DefaultRealmOID(), encode(meta.DefaultSystemMetaObjInfo{DefaultID: ids.realmID}))
	c.Put(root, "", meta.ZoneNameOID(store), encode(meta.NameToID{ObjID: ids.zoneID}))
	c.Put(root, "", meta.ZoneInfoOID(ids.zoneID), encode(params))
	c.Put(root, "", meta.DefaultZoneOID(ids.realmID), encode(meta.DefaultSystemMetaObjInfo{DefaultID: ids.zoneID}))
	c.Put(root, "", meta.ZoneGroupNameOID(store), encode(meta.NameToID{ObjID: ids.zgID}))
	c.Put(root, "", meta.ZoneGroupInfoOID(ids.zgID), encode(zg))
	c.Put(root, "", meta.DefaultZoneGroupOID(ids.realmID), encode(meta.DefaultSystemMetaObjInfo{DefaultID: ids.zgID}))
	if withPeriod {
		period := meta.NewPeriod()
		period.ID, period.Epoch, period.RealmID = ids.periodID, 1, ids.realmID
		period.MasterZoneGroup, period.MasterZone = ids.zgID, ids.zoneID
		period.PeriodMap = meta.PeriodMap{ID: ids.periodID, ZoneGroups: map[string]meta.ZoneGroup{ids.zgID: zg}, MasterZoneGroup: ids.zgID}
		period.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: 7, Enabled: true}
		c.Put(root, "", meta.PeriodLatestEpochOID(ids.periodID), encode(meta.PeriodLatestEpochInfo{Epoch: 1}))
		c.Put(root, "", meta.PeriodOID(ids.periodID, 1), encode(period))
	}
	return ids
}

// rookMetaPool is the pool whose namespaces hold the metadata of the zone
// seedRookZone seeds for the store "ceph-objectstore".
const rookMetaPool = "ceph-objectstore.rgw.meta"

// seedUser stores info in that zone as radosgw's PutOperation does: the
// users.uid object holding the RGWUID and the RGWUserInfo, with attrs and
// version v as its xattrs, and an index object holding the RGWUID for each
// active access key and for the lower-cased email.
func seedUser(c *fakerados.Cluster, info meta.UserInfo, attrs map[string][]byte, v meta.ObjVersion) {
	uid := meta.UID(info.UserID.String())
	c.Put(rookMetaPool, "users.uid", string(uid), encode(meta.UserObject{UID: uid, Info: info}))
	obj := c.Object(rookMetaPool, "users.uid", string(uid))
	maps.Copy(obj.Xattrs, attrs)
	obj.Xattrs[version.XattrName] = encode(v)
	for id, k := range info.AccessKeys {
		if k.Active {
			c.Put(rookMetaPool, "users.keys", id, encode(uid))
		}
	}
	if info.Email != "" {
		c.Put(rookMetaPool, "users.email", lowerASCII(info.Email), encode(uid))
	}
}

// lowerASCII is boost::to_lower in the C locale radosgw runs in, which
// lower-cases ASCII letters alone.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// conf is Options over every option the driver's specs need, at radosgw's
// defaults, with kv on top.
func conf(kv map[string]string) *cephconf.Options {
	m := cephconf.MapGetter{
		"rgw_realm": "", "rgw_realm_id": "", "rgw_zonegroup": "", "rgw_zonegroup_id": "", "rgw_zone": "", "rgw_zone_id": "",
		"rgw_region": "", "rgw_region_root_pool": ".rgw.root",
		"rgw_realm_root_pool": ".rgw.root", "rgw_zonegroup_root_pool": ".rgw.root", "rgw_zone_root_pool": ".rgw.root", "rgw_period_root_pool": ".rgw.root",
		"rgw_num_control_oids": "8", "rgw_max_notify_retries": "10", "rgw_cache_enabled": "true", "rgw_cache_lru_size": "25000", "rgw_cache_expiry_interval": "900",
		"rgw_usage_max_shards": "32", "rgw_usage_max_user_shards": "1", "rgw_lc_max_objs": "32", "rgw_enable_usage_log": "false",
		"rgw_usage_log_flush_threshold": "1024", "rgw_usage_log_tick_interval": "30", "rgw_bucket_quota_ttl": "600", "rgw_bucket_quota_cache_size": "10000",
		"rgw_user_quota_bucket_sync_interval": "180", "rgw_user_quota_sync_interval": "86400", "rgw_user_quota_sync_idle_users": "false", "rgw_user_quota_sync_wait_time": "86400",
		"rgw_enable_quota_threads": "true", "rgw_bucket_default_quota_max_objects": "-1", "rgw_bucket_default_quota_max_size": "-1",
		"rgw_user_default_quota_max_objects": "-1", "rgw_user_default_quota_max_size": "-1", "rgw_account_default_quota_max_objects": "-1", "rgw_account_default_quota_max_size": "-1",
		"rgw_list_bucket_min_readahead": "1000", "rgw_max_listing_results": "1000", "rgw_override_bucket_index_max_shards": "0", "rgw_bucket_index_max_aio": "128",
		"rgw_dynamic_resharding": "true", "rgw_list_buckets_max_chunk": "1000", "rgw_relaxed_s3_bucket_names": "false", "rgw_max_put_param_size": "1048576",
		"rgw_acl_grants_max_num": "100", "rgw_run_sync_thread": "true", "rgw_max_chunk_size": "4194304",
	}
	maps.Copy(m, kv)
	return cephconf.NewOptions(m)
}
