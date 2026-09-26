package meta_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The corpus holds these types with empty or repeated values, where swapping
// two fields of one type leaves the bytes unchanged. These specs give every
// field a distinct value and build the expected bytes field by field in the
// C++ encode order at the version written, without the type's own Encode;
// nested types pinned elsewhere are encoded with theirs.

// encStrings writes a std::list, vector or set of strings.
func encStrings(e *denc.Encoder, ss ...string) {
	e.U32(uint32(len(ss)))
	for _, s := range ss {
		e.String(s)
	}
}

// encSysObj writes the RGWSystemMetaObj framing of an id and name.
func encSysObj(e *denc.Encoder, id, name string) {
	f := e.BeginStruct(1, 1)
	e.String(id)
	e.String(name)
	e.EndStruct(f)
}

// expectOrder asserts that encode writes want and that want decodes to v.
func expectOrder[T any](v T, want []byte, encode func(*denc.Encoder), decode func(*denc.Decoder) T) {
	GinkgoHelper()
	Expect(encoded(encode)).To(Equal(want))
	Expect(decodeWhole(want, decode)).To(Equal(v))
}

func orderZone() meta.Zone {
	return meta.Zone{
		ID: "zone-id", Name: "zone-name", Endpoints: []string{"http://e1", "http://e2"},
		LogMeta: true, LogData: false, BucketIndexMaxShards: 5, ReadOnly: true,
		TierType: "archive", SyncFromAll: false, SyncFrom: []string{"from-a", "from-b"},
		RedirectZone: "redirect", SupportedFeatures: []string{"feature"},
	}
}

func orderZoneGroup() meta.ZoneGroup {
	return meta.ZoneGroup{
		ID: "zg-id", Name: "zg-name", APIName: "zg-api", IsMaster: true,
		Endpoints: []string{"http://g1", "http://g2"}, Hostnames: []string{"host"},
		HostnamesS3Website: []string{"web1", "web2"}, MasterZone: "master-zone",
		Zones: map[string]meta.Zone{"zone-id": orderZone()},
		PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{
			"target": {Name: "target", Tags: []string{"tag"}, StorageClasses: []string{"STANDARD"}},
		},
		DefaultPlacement: meta.PlacementRule{Name: "target", StorageClass: "COLD"},
		RealmID:          "zg-realm", EnabledFeatures: []string{"f1", "f2"},
	}
}

var _ = Describe("encode order with distinct values", func() {
	It("Realm", func() {
		r := meta.Realm{ID: "realm-id", Name: "realm-name", CurrentPeriod: "period", Epoch: 7}
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			encSysObj(e, "realm-id", "realm-name")
			e.String("period")
			e.U32(7)
			e.EndStruct(f)
		})
		expectOrder(r, want, func(e *denc.Encoder) { r.Encode(e, denc.Squid) }, meta.DecodeRealm)
	})

	It("Zone", func() {
		z := orderZone()
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(8, 1)
			e.String("zone-name")
			encStrings(e, "http://e1", "http://e2")
			e.Bool(true)  // log_meta
			e.Bool(false) // log_data
			e.U32(5)
			e.String("zone-id")
			e.Bool(true) // read_only
			e.String("archive")
			e.Bool(false) // sync_from_all
			encStrings(e, "from-a", "from-b")
			e.String("redirect")
			encStrings(e, "feature")
			e.EndStruct(f)
		})
		expectOrder(z, want, func(e *denc.Encoder) { z.Encode(e, denc.Squid) }, meta.DecodeZone)
	})

	// The first Zone spec's four flags come in equal pairs; this one flips
	// them so a swap within either pair shows.
	It("Zone with the flags paired the other way", func() {
		z := orderZone()
		z.LogMeta, z.LogData, z.ReadOnly, z.SyncFromAll = true, true, false, false
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(8, 1)
			e.String("zone-name")
			encStrings(e, "http://e1", "http://e2")
			e.Bool(true) // log_meta
			e.Bool(true) // log_data
			e.U32(5)
			e.String("zone-id")
			e.Bool(false) // read_only
			e.String("archive")
			e.Bool(false) // sync_from_all
			encStrings(e, "from-a", "from-b")
			e.String("redirect")
			encStrings(e, "feature")
			e.EndStruct(f)
		})
		expectOrder(z, want, func(e *denc.Encoder) { z.Encode(e, denc.Squid) }, meta.DecodeZone)
	})

	It("ZoneGroup", func() {
		g := orderZoneGroup()
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(6, 1)
			e.String("zg-name")
			e.String("zg-api")
			e.Bool(true)
			encStrings(e, "http://g1", "http://g2")
			e.String("master-zone")
			e.U32(1)
			e.String("zone-id")
			g.Zones["zone-id"].Encode(e, denc.Squid)
			e.U32(1)
			e.String("target")
			g.PlacementTargets["target"].Encode(e, denc.Squid)
			e.String("target/COLD")
			encStrings(e, "host")
			encStrings(e, "web1", "web2")
			encSysObj(e, "zg-id", "zg-name")
			e.String("zg-realm")
			e.Raw(emptySyncPolicy)
			encStrings(e, "f1", "f2")
			e.EndStruct(f)
		})
		expectOrder(g, want, func(e *denc.Encoder) { g.Encode(e, denc.Squid) }, meta.DecodeZoneGroup)
	})

	Describe("Period", func() {
		var p meta.Period
		var body func(e *denc.Encoder, realmName string)
		BeforeEach(func() {
			zg := orderZoneGroup()
			zg.IsMaster = false // so the map's stored master zonegroup survives decode
			p = meta.Period{
				ID: "period-id", Epoch: 3, PredecessorUUID: "predecessor",
				SyncStatus: []string{"s1", "s2"},
				PeriodMap: meta.PeriodMap{
					ID: "map-id", ZoneGroups: map[string]meta.ZoneGroup{"zg-id": zg},
					MasterZoneGroup: "map-master-zg", ShortZoneIDs: map[string]uint32{"zone-id": 42},
				},
				MasterZoneGroup: "master-zg", MasterZone: "master-zone",
				PeriodConfig: meta.PeriodConfig{
					BucketQuota:   meta.Quota{MaxSize: 1024, MaxObjects: 1},
					UserRateLimit: meta.RateLimitInfo{MaxReadOps: 10},
				},
				RealmID: "realm-id", RealmEpoch: 9,
			}
			body = func(e *denc.Encoder, realmName string) {
				f := e.BeginStruct(1, 1)
				e.String("period-id")
				e.U32(3) // epoch
				e.U32(9) // realm_epoch
				e.String("predecessor")
				encStrings(e, "s1", "s2")
				m := e.BeginStruct(2, 1)
				e.String("map-id")
				e.U32(1)
				e.String("zg-id")
				zg.Encode(e, denc.Squid)
				e.String("map-master-zg")
				e.U32(1)
				e.String("zone-id")
				e.U32(42)
				e.EndStruct(m)
				e.String("master-zone")
				e.String("master-zg")
				p.PeriodConfig.Encode(e, denc.Squid)
				e.String("realm-id")
				e.String(realmName)
				e.EndStruct(f)
			}
		})
		It("writes master_zone before master_zonegroup and an empty realm_name", func() {
			want := encoded(func(e *denc.Encoder) { body(e, "") })
			expectOrder(p, want, func(e *denc.Encoder) { p.Encode(e, denc.Squid) }, meta.DecodePeriod)
		})
		It("discards a stored realm_name", func() {
			Expect(decodeWhole(encoded(func(e *denc.Encoder) { body(e, "realm-name") }), meta.DecodePeriod)).To(Equal(p))
		})
	})

	It("ZoneParams, its pools and embedded access key", func() {
		created := meta.Time{Time: time.Unix(1700000000, 5).UTC()}
		key := meta.AccessKey{ID: "AKID", Secret: "SECRET", Subuser: "sub", Active: false, CreatedAt: created}
		pools := []string{
			"domain_root", "control", "gc", "log", "intent_log", "usage_log",
			"user_keys", "user_email", "user_swift", "user_uid",
		}
		p := func(n string) meta.Pool { return meta.Pool{Name: n} }
		z := meta.ZoneParams{
			ID: "zone-id", Name: "zone-name",
			DomainRoot: p("domain_root"), ControlPool: p("control"), GCPool: p("gc"), LogPool: p("log"),
			IntentLogPool: p("intent_log"), UsageLogPool: p("usage_log"), UserKeysPool: p("user_keys"),
			UserEmailPool: p("user_email"), UserSwiftPool: p("user_swift"), UserUIDPool: p("user_uid"),
			SystemKey: key, RealmID: "realm-id", LCPool: p("lc"), RolesPool: p("roles"),
			ReshardPool: p("reshard"), OTPPool: p("otp"), OIDCPool: p("oidc"), NotifPool: p("notif"),
			TopicsPool: p("topics"), AccountPool: p("account"), GroupPool: p("group"),
			// What RGWZoneParams::decode fills for version 15.
			RestorePool:       pool("log", "restore"),
			DedupPool:         p("zone-name.rgw.dedup"),
			BucketLoggingPool: pool("log", "logging"),
			VectorPool:        pool("zone-name.rgw.meta", "vector"),
		}
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(15, 1)
			for _, n := range pools {
				encPool(e, n, "")
			}
			encSysObj(e, "zone-id", "zone-name")
			k := e.BeginStruct(4, 2)
			e.String("AKID")
			e.String("SECRET")
			e.String("sub")
			e.Bool(false)
			e.U32(1700000000)
			e.U32(5)
			e.EndStruct(k)
			e.U32(0)           // placement_pools
			encPool(e, "", "") // unused metadata_heap
			e.String("realm-id")
			encPool(e, "lc", "")
			e.U32(0) // old_tier_config
			encPool(e, "roles", "")
			encPool(e, "reshard", "")
			encPool(e, "otp", "")
			t := e.BeginStruct(2, 1) // empty JSONFormattable tier_config
			e.U8(0)
			e.String("")
			e.U32(0)
			e.U32(0)
			e.Bool(false)
			e.EndStruct(t)
			for _, n := range []string{"oidc", "notif", "topics", "account", "group"} {
				encPool(e, n, "")
			}
			e.EndStruct(f)
		})
		expectOrder(z, want, func(e *denc.Encoder) { z.Encode(e, denc.Squid) }, meta.DecodeZoneParams)
	})
})
