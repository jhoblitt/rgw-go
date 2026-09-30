package driver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// removeRoot removes a root object with a write op, as radosgw-admin would.
func removeRoot(ctx context.Context, c *fakerados.Cluster, oid string) {
	GinkgoHelper()
	p, err := c.Pool(ctx, meta.RootPool, "")
	Expect(err).NotTo(HaveOccurred())
	w := radosclient.NewWriteOp()
	w.Remove()
	_, err = p.Write(ctx, oid, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), oid)
}

// editRoot decodes the root object oid, lets edit change it and stores it
// back.
func editRoot[T encoder](c *fakerados.Cluster, oid string, decode func(*denc.Decoder) T, edit func(*T)) {
	GinkgoHelper()
	obj := c.Object(meta.RootPool, "", oid)
	Expect(obj).NotTo(BeNil(), oid)
	d := denc.NewDecoder(obj.Data)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred(), oid)
	edit(&v)
	c.Put(meta.RootPool, "", oid, encode(v))
}

// rootData returns a root object's bytes.
func rootData(c *fakerados.Cluster, oid string) []byte {
	GinkgoHelper()
	obj := c.Object(meta.RootPool, "", oid)
	Expect(obj).NotTo(BeNil(), oid)
	return bytes.Clone(obj.Data)
}

var _ = Describe("zone resolution", func() {
	var c *fakerados.Cluster
	BeforeEach(func() {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
	})

	It("resolves Rook's zoned store from the period when the names are configured", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_realm": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Release()).To(Equal(denc.Squid))
		Expect(s.Realm().ID).To(Equal(ids.realmID))
		Expect(s.Period().ID).To(Equal(ids.periodID))
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
		Expect(s.Zone().ID).To(Equal(ids.zoneID))
		Expect(s.ZoneParams().DomainRoot).To(Equal(meta.ParsePool("ceph-objectstore.rgw.meta:root")))
		Expect(s.PeriodConfig().BucketQuota.MaxObjects).To(BeEquivalentTo(7), "period config comes from the period")
	})

	It("falls back to the default realm, zonegroup and zone objects when no name is configured", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Zone().ID).To(Equal(ids.zoneID))
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
	})

	It("uses the local zonegroup and reads period_config when the realm has no period", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", false)
		pc := meta.PeriodConfig{UserQuota: meta.Quota{MaxSize: 4096, MaxObjects: -1, Enabled: true}}
		c.Put(meta.RootPool, "", meta.PeriodConfigOID(ids.realmID), encode(pc))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Period().ID).To(BeEmpty())
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
		Expect(s.PeriodConfig().UserQuota.MaxSize).To(BeEquivalentTo(4096))
	})

	It("takes the zonegroup from the period map, not zonegroup_info, when both exist and differ", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		stale := meta.ZoneGroup{ID: ids.zgID, Name: "ceph-objectstore", APIName: "stale", RealmID: ids.realmID}
		c.Put(meta.RootPool, "", meta.ZoneGroupInfoOID(ids.zgID), encode(stale))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.ZoneGroup().APIName).To(Equal("ceph-objectstore"), "the committed period wins")
	})

	It("refuses to start when the configured zone does not exist, naming the object", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "missing"}), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone))
		Expect(err.Error()).To(ContainSubstring("zone_names.missing"))
	})

	It("refuses to start when rgw_zonegroup names a different zonegroup", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", "rgw_zonegroup": "other"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring(`zonegroup "ceph-objectstore" is not "other"`)))
	})

	It("refuses to start when the zone is not a member of its zonegroup", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", false)
		zg := meta.ZoneGroup{ID: ids.zgID, Name: "ceph-objectstore", RealmID: ids.realmID, Zones: map[string]meta.Zone{}}
		c.Put(meta.RootPool, "", meta.ZoneGroupInfoOID(ids.zgID), encode(zg))
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring("is not in zonegroup")))
	})

	It("never creates a zone or zonegroup on an empty root pool", func(ctx SpecContext) {
		_, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone))
		Expect(c.Object(meta.RootPool, "", meta.ZoneNameOID("default"))).To(BeNil(), "radosgw would have bootstrapped one; rgw-go must not")
	})

	It("honors the release override and the Squid floor as G left them", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		t := denc.Tentacle
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{Release: &t})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Release()).To(Equal(denc.Tentacle))
		c.SetRequiredOSDRelease("reef")
		_, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(radosclient.ErrReleaseTooOld))
	})

	It("refuses to start when rgw_realm names a realm the root pool lacks, naming the object", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_realm": "missing", "rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone), "SiteConfig::load fails where do_start would go on")
		Expect(err.Error()).To(ContainSubstring("realms_names.missing"))
	})

	It("reads the realm the zone names, and its period, when no realm is named or default, and requires it", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		removeRoot(ctx, c, meta.DefaultRealmOID())
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Realm().ID).To(Equal(ids.realmID))
		Expect(s.Period().ID).To(Equal(ids.periodID))
		Expect(s.PeriodConfig().BucketQuota.MaxObjects).To(BeEquivalentTo(7))
		removeRoot(ctx, c, meta.RealmOID(ids.realmID))
		_, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone))
		Expect(err.Error()).To(ContainSubstring(meta.RealmOID(ids.realmID)))
	})

	It("reads period_config by the zonegroup's realm id, as radosgw's zone service does", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", false)
		editRoot(c, meta.ZoneGroupInfoOID(ids.zgID), meta.DecodeZoneGroup, func(zg *meta.ZoneGroup) { zg.RealmID = "zg-realm" })
		c.Put(meta.RootPool, "", meta.PeriodConfigOID("zg-realm"), encode(meta.PeriodConfig{UserQuota: meta.Quota{MaxSize: 1}}))
		c.Put(meta.RootPool, "", meta.PeriodConfigOID(ids.realmID), encode(meta.PeriodConfig{UserQuota: meta.Quota{MaxSize: 2}}))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.PeriodConfig().UserQuota.MaxSize).To(BeEquivalentTo(1))
	})

	It("leaves rgw_zonegroup unchecked against the period's zonegroup on Tentacle, whose startup has no such check", func(ctx SpecContext) {
		c.SetRequiredOSDRelease("tentacle")
		ids := seedRookZone(c, "ceph-objectstore", true)
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", "rgw_zonegroup": "other"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
	})

	DescribeTable("takes the local zonegroup when the period lacks the zone, keeping the period only on Squid",
		func(ctx SpecContext, release string, keepsPeriod bool) {
			c.SetRequiredOSDRelease(release)
			ids := seedRookZone(c, "ceph-objectstore", true)
			editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
				p.PeriodMap.ZoneGroups = map[string]meta.ZoneGroup{}
			})
			s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
			if keepsPeriod {
				Expect(s.Period().ID).To(Equal(ids.periodID), "Squid's zone service keeps the realm's current period")
				Expect(s.PeriodConfig().BucketQuota.MaxObjects).To(BeEquivalentTo(7), "and its config without a period_config object")
			} else {
				Expect(s.Period().ID).To(BeEmpty(), "Tentacle takes the period from SiteConfig, which drops it")
				Expect(s.PeriodConfig()).To(Equal(meta.PeriodConfig{}))
			}
		},
		Entry("on Squid", "squid", true),
		Entry("on Tentacle", "tentacle", false),
	)

	It("takes rgw_zonegroup from rgw_region when it is unset, as radosgw's startup does", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", "rgw_region": "other"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring(`zonegroup "ceph-objectstore" is not "other"`)))
	})

	DescribeTable("reads each kind of root object from the pool radosgw derives from its option",
		func(ctx SpecContext, opts map[string]string, zonePool, otherPool string) {
			seedRookZone(c, "ceph-objectstore", true)
			root, err := c.Pool(ctx, meta.RootPool, "")
			Expect(err).NotTo(HaveOccurred())
			Expect(root.ListObjects(ctx, func(oid, _ string) error {
				to := otherPool
				if strings.HasPrefix(oid, "zone_") || strings.HasPrefix(oid, "default.zone.") {
					to = zonePool
				}
				c.Put(to, "", oid, c.Object(meta.RootPool, "", oid).Data)
				return nil
			})).To(Succeed())
			c.FailPool(meta.RootPool)
			kv := maps.Clone(opts)
			kv["rgw_zone"] = "ceph-objectstore"
			_, err = driver.Open(ctx, c, conf(kv), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("rgw.root for every empty option",
			map[string]string{"rgw_realm_root_pool": "", "rgw_zonegroup_root_pool": "", "rgw_zone_root_pool": "", "rgw_period_root_pool": "", "rgw_region_root_pool": ""},
			"rgw.root", "rgw.root"),
		Entry("rgw_region_root_pool for an empty realm, zonegroup or period option",
			map[string]string{"rgw_realm_root_pool": "", "rgw_zonegroup_root_pool": "", "rgw_zone_root_pool": "zones", "rgw_period_root_pool": "", "rgw_region_root_pool": "legacy"},
			"zones", "legacy"),
	)

	DescribeTable("refuses to start on a zonegroup whose master zone is not one of its zones",
		func(ctx SpecContext, withPeriod bool, edit func(c *fakerados.Cluster, ids rookZone)) {
			ids := seedRookZone(c, "ceph-objectstore", withPeriod)
			edit(c, ids)
			_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
			Expect(err).To(MatchError(ContainSubstring(`master zone "ghost"`)))
		},
		Entry("in the period", true, func(c *fakerados.Cluster, ids rookZone) {
			editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
				zg := p.PeriodMap.ZoneGroups[ids.zgID]
				zg.MasterZone = "ghost"
				p.PeriodMap.ZoneGroups[ids.zgID] = zg
			})
		}),
		Entry("another zonegroup of the period", true, func(c *fakerados.Cluster, ids rookZone) {
			editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
				p.PeriodMap.ZoneGroups["zg-other"] = meta.ZoneGroup{
					ID: "zg-other", Name: "other", MasterZone: "ghost",
					Zones: map[string]meta.Zone{"zone-other": {ID: "zone-other", Name: "other"}},
				}
			})
		}),
		Entry("in the master zonegroup_info without a period", false, func(c *fakerados.Cluster, ids rookZone) {
			editRoot(c, meta.ZoneGroupInfoOID(ids.zgID), meta.DecodeZoneGroup, func(zg *meta.ZoneGroup) { zg.MasterZone = "ghost" })
		}),
	)

	It("serves a single-zone zonegroup without a master zone with that zone as master, writing nothing back", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
			zg := p.PeriodMap.ZoneGroups[ids.zgID]
			zg.MasterZone = ""
			p.PeriodMap.ZoneGroups[ids.zgID] = zg
		})
		period, zgInfo := rootData(c, meta.PeriodOID(ids.periodID, 1)), rootData(c, meta.ZoneGroupInfoOID(ids.zgID))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.ZoneGroup().MasterZone).To(Equal(ids.zoneID))
		Expect(rootData(c, meta.PeriodOID(ids.periodID, 1))).To(Equal(period))
		Expect(rootData(c, meta.ZoneGroupInfoOID(ids.zgID))).To(Equal(zgInfo), "radosgw writes the fix back; rgw-go must not")
	})

	It("falls back from a default realm without a default zone to the zone named default, unless rgw_realm is set", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		removeRoot(ctx, c, meta.DefaultZoneOID(ids.realmID))
		c.Put(meta.RootPool, "", meta.ZoneNameOID("default"), encode(meta.NameToID{ObjID: ids.zoneID}))
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Zone().ID).To(Equal(ids.zoneID))
		Expect(s.Period().ID).To(Equal(ids.periodID), "the zone names its realm again")
		_, err = driver.Open(ctx, c, conf(map[string]string{"rgw_realm": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone))
		Expect(err.Error()).To(ContainSubstring(meta.DefaultZoneOID(ids.realmID)))
	})

	It("resolves by rgw_realm_id, rgw_zonegroup_id and rgw_zone_id without the name or default objects", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", false)
		for _, oid := range []string{
			meta.RealmNameOID("ceph-objectstore"), meta.DefaultRealmOID(),
			meta.ZoneGroupNameOID("ceph-objectstore"), meta.DefaultZoneGroupOID(ids.realmID),
			meta.ZoneNameOID("ceph-objectstore"), meta.DefaultZoneOID(ids.realmID),
		} {
			removeRoot(ctx, c, oid)
		}
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_realm_id": ids.realmID, "rgw_zonegroup_id": ids.zgID, "rgw_zone_id": ids.zoneID}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{s.Realm().ID, s.ZoneGroup().ID, s.Zone().ID}).To(Equal([]string{ids.realmID, ids.zgID, ids.zoneID}))
	})

	It("refuses to start on a root object that does not decode, naming it", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		c.Put(meta.RootPool, "", meta.ZoneInfoOID(ids.zoneID), []byte{0xff})
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring("decoding .rgw.root/" + meta.ZoneInfoOID(ids.zoneID))))
		Expect(err).NotTo(MatchError(driver.ErrNoZone))
	})

	It("returns a root read's error other than ENOENT as itself, naming the object, not as ErrNoZone", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		readErr := &radosclient.Error{Errno: int32(syscall.EIO), Op: "read"}
		rc := &recordingCluster{Cluster: c, readErrs: map[string]error{meta.ZoneInfoOID(ids.zoneID): readErr}}
		_, err := driver.Open(ctx, rc, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(readErr))
		Expect(err).NotTo(MatchError(driver.ErrNoZone))
		Expect(err.Error()).To(ContainSubstring(".rgw.root/" + meta.ZoneInfoOID(ids.zoneID)))
	})

	It("refuses a root object larger than a root read takes", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		c.Put(meta.RootPool, "", meta.ZoneInfoOID(ids.zoneID), make([]byte, 4<<20))
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring("exceeds")))
	})

	It("refuses to start when a root pool cannot be opened, naming it", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		c.FailPool(meta.RootPool)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(err.Error()).To(ContainSubstring(meta.RootPool))
	})

	It("logs the zone it resolved once", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		var buf bytes.Buffer
		DeferCleanup(driver.CaptureLog(&buf))
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		var resolved []map[string]any
		for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
			var rec map[string]any
			Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), line)
			if rec["msg"] == "zone resolved" {
				resolved = append(resolved, rec)
			}
		}
		Expect(resolved).To(HaveLen(1))
		Expect(resolved[0]).To(And(
			HaveKeyWithValue("realm", "ceph-objectstore"), HaveKeyWithValue("realm_id", ids.realmID),
			HaveKeyWithValue("zonegroup", "ceph-objectstore"), HaveKeyWithValue("zonegroup_id", ids.zgID),
			HaveKeyWithValue("zone", "ceph-objectstore"), HaveKeyWithValue("zone_id", ids.zoneID),
			HaveKeyWithValue("period", ids.periodID), HaveKeyWithValue("period_epoch", BeEquivalentTo(1)),
			HaveKeyWithValue("from_period", true),
		))
	})
})

var _ = Describe("Placement", func() {
	var (
		c   *fakerados.Cluster
		ids rookZone
	)
	BeforeEach(func() {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		ids = seedRookZone(c, "ceph-objectstore", true)
	})
	open := func(ctx context.Context) *driver.Store {
		GinkgoHelper()
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	It("resolves the STANDARD class of a rule", func(ctx SpecContext) {
		p, err := open(ctx).Placement(meta.PlacementRule{Name: "default-placement"})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Rule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "STANDARD"}))
		Expect(p.DataPool).To(Equal(meta.ParsePool("ceph-objectstore.rgw.buckets.data")))
		Expect(p.IndexPool).To(Equal(meta.ParsePool("ceph-objectstore.rgw.buckets.index")))
		Expect(p.DataExtraPool).To(Equal(meta.ParsePool("ceph-objectstore.rgw.buckets.non-ec")))
		Expect(p.Compression).To(BeEmpty())
	})

	It("is InvalidLocationConstraint for an unknown rule or class", func(ctx SpecContext) {
		s := open(ctx)
		_, err := s.Placement(meta.PlacementRule{Name: "nope"})
		Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
		_, err = s.Placement(meta.PlacementRule{Name: "default-placement", StorageClass: "GLACIER"})
		Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
	})

	Context("with more storage classes and no data-extra pool", func() {
		var dataPool meta.Pool
		BeforeEach(func() {
			dataPool = meta.ParsePool("ceph-objectstore.rgw.buckets.data")
			editRoot(c, meta.ZoneInfoOID(ids.zoneID), meta.DecodeZoneParams, func(z *meta.ZoneParams) {
				pi := z.PlacementPools["default-placement"]
				pi.DataExtraPool = meta.Pool{}
				pi.StorageClasses["COMP_ZLIB"] = meta.ZoneStorageClass{DataPool: new(dataPool), CompressionType: new("zlib")}
				pi.StorageClasses["NO_POOL"] = meta.ZoneStorageClass{}
				z.PlacementPools["default-placement"] = pi
			})
		})

		It("resolves a class's compression", func(ctx SpecContext) {
			p, err := open(ctx).Placement(meta.PlacementRule{Name: "default-placement", StorageClass: "COMP_ZLIB"})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Rule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COMP_ZLIB"}))
			Expect(p.Compression).To(Equal("zlib"))
			Expect(p.DataPool).To(Equal(dataPool))
		})

		It("puts extra data in STANDARD's data pool when the placement has no data-extra pool", func(ctx SpecContext) {
			p, err := open(ctx).Placement(meta.PlacementRule{Name: "default-placement"})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.DataExtraPool).To(Equal(dataPool), "get_data_extra_pool, rgw_zone_types.h:274-280")
		})

		It("gives a class without a data pool STANDARD's", func(ctx SpecContext) {
			p, err := open(ctx).Placement(meta.PlacementRule{Name: "default-placement", StorageClass: "NO_POOL"})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.DataPool).To(Equal(dataPool))
		})

		DescribeTable("resolves a rule without a name through the zonegroup's default placement",
			func(ctx SpecContext, defaultClass string, rule, want meta.PlacementRule) {
				editRoot(c, meta.PeriodOID(ids.periodID, 1), meta.DecodePeriod, func(p *meta.Period) {
					zg := p.PeriodMap.ZoneGroups[ids.zgID]
					zg.DefaultPlacement.StorageClass = defaultClass
					p.PeriodMap.ZoneGroups[ids.zgID] = zg
				})
				p, err := open(ctx).Placement(rule)
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Rule).To(Equal(want))
			},
			Entry("the zero rule", "", meta.PlacementRule{}, meta.PlacementRule{Name: "default-placement", StorageClass: "STANDARD"}),
			Entry("a storage class alone", "", meta.PlacementRule{StorageClass: "COMP_ZLIB"}, meta.PlacementRule{Name: "default-placement", StorageClass: "COMP_ZLIB"}),
			Entry("the zero rule under a default with a class", "COMP_ZLIB", meta.PlacementRule{}, meta.PlacementRule{Name: "default-placement", StorageClass: "COMP_ZLIB"}),
		)
	})
})
