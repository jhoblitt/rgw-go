package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// zoneNames are the rgw_* options that name the zone, read once at Open.
type zoneNames struct {
	Realm, RealmID         string // rgw_realm, rgw_realm_id
	ZoneGroup, ZoneGroupID string // rgw_zonegroup, or rgw_region when it is unset; rgw_zonegroup_id
	Zone, ZoneID           string // rgw_zone, rgw_zone_id
}

// readZoneNames reads the options that name the zone. radosgw's startup
// gives an unset rgw_zonegroup the legacy rgw_region
// (AppMain::init_frontends1, rgw_appmain.cc:164-168 at v19.2.6, :166-170 at
// v20.2.4).
func readZoneNames(conf *cephconf.Options) (zoneNames, error) {
	var n zoneNames
	var region string
	for _, o := range []struct {
		option string
		dst    *string
	}{
		{"rgw_realm", &n.Realm},
		{"rgw_realm_id", &n.RealmID},
		{"rgw_zonegroup", &n.ZoneGroup},
		{"rgw_zonegroup_id", &n.ZoneGroupID},
		{"rgw_zone", &n.Zone},
		{"rgw_zone_id", &n.ZoneID},
		{"rgw_region", &region},
	} {
		v, err := conf.String(o.option)
		if err != nil {
			return zoneNames{}, fmt.Errorf("reading the zone names: %w", err)
		}
		*o.dst = v
	}
	if n.ZoneGroup == "" {
		n.ZoneGroup = region
	}
	return n, nil
}

// rootPools are the pools the four kinds of root object live in; all four
// default to .rgw.root (rgw_realm_root_pool, rgw_zonegroup_root_pool,
// rgw_zone_root_pool, rgw_period_root_pool).
type rootPools struct{ realm, zonegroup, zone, period radosclient.Pool }

// emptyRootPool is the pool radosgw uses for a root pool option set empty
// (RGW_DEFAULT_ZONE_ROOT_POOL and its siblings, rgw_zone.cc:33-35 at
// v19.2.6; config/impl.cc:25-28 at v20.2.4).
const emptyRootPool = "rgw.root"

// openRootPools reads the four root pool options and opens their pools.
// radosgw's startup gives an empty realm, zonegroup or period option the
// legacy rgw_region_root_pool (AppMain::init_frontends1,
// rgw_appmain.cc:150-162 at v19.2.6, :152-164 at v20.2.4).
func openRootPools(ctx context.Context, conf *cephconf.Options, pools *poolCache) (rootPools, error) {
	region, err := conf.String("rgw_region_root_pool")
	if err != nil {
		return rootPools{}, fmt.Errorf("reading the root pools: %w", err)
	}
	var r rootPools
	for _, o := range []struct {
		option string
		legacy bool
		dst    *radosclient.Pool
	}{
		{"rgw_realm_root_pool", true, &r.realm},
		{"rgw_zonegroup_root_pool", true, &r.zonegroup},
		{"rgw_zone_root_pool", false, &r.zone},
		{"rgw_period_root_pool", true, &r.period},
	} {
		name, err := conf.String(o.option)
		if err != nil {
			return rootPools{}, fmt.Errorf("reading the root pools: %w", err)
		}
		if name == "" && o.legacy {
			name = region
		}
		if name == "" {
			name = emptyRootPool
		}
		if *o.dst, err = pools.get(ctx, meta.ParsePool(name)); err != nil {
			return rootPools{}, err
		}
	}
	return r, nil
}

// zoneConfig is what RGWSI_Zone::do_start leaves behind when it only reads.
type zoneConfig struct {
	Realm        meta.Realm
	HasRealm     bool
	Period       meta.Period
	HasPeriod    bool
	FromPeriod   bool // the zonegroup came from the period map, not zonegroup_info
	ZoneGroup    meta.ZoneGroup
	Zone         meta.Zone
	Params       meta.ZoneParams
	PeriodConfig meta.PeriodConfig
}

// ErrNoZone wraps every failure to find a root object that names the zone
// this gateway serves; the message names the object looked for.
var ErrNoZone = errors.New("driver: zone not found in the root pool")

// defaultName is the name radosgw gives the zone and zonegroup it would
// create (default_zone_name, default_zonegroup_name, rgw_zone.cc:30-31 at
// v19.2.6).
const defaultName = "default"

// rootObjectMax bounds a root-object read. radosgw reads these objects whole
// with read(0, 0), which a read into the seam's buffer cannot do without the
// size (radosclient.ReadStep), and every realm, period, zonegroup and zone
// object is far smaller than rgw_max_chunk_size's default of 4 MiB.
const rootObjectMax = 4 << 20

// where names an object for an error.
func where(p radosclient.Pool, oid string) string {
	return meta.Pool{Name: p.Name(), NS: p.Namespace()}.String() + "/" + oid
}

// readWhole returns the bytes of one small object.
func readWhole(ctx context.Context, p radosclient.Pool, oid string) ([]byte, error) {
	rop := radosclient.NewReadOp()
	res := rop.Read(0, rootObjectMax)
	if _, err := p.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
		return nil, fmt.Errorf("reading %s: %w", where(p, oid), err)
	}
	if res.Err != nil {
		return nil, fmt.Errorf("reading %s: %w", where(p, oid), res.Err)
	}
	if res.N >= rootObjectMax {
		return nil, fmt.Errorf("reading %s: object exceeds %d bytes", where(p, oid), rootObjectMax)
	}
	return res.Data[:res.N], nil
}

// readRoot reads and decodes one root object. radosgw fails its startup with
// EIO on an object that does not decode.
func readRoot[T any](ctx context.Context, p radosclient.Pool, oid string, dec func(*denc.Decoder) T) (T, error) {
	var zero T
	b, err := readWhole(ctx, p, oid)
	if err != nil {
		return zero, err
	}
	d := denc.NewDecoder(b)
	v := dec(d)
	if err := d.Err(); err != nil {
		return zero, fmt.Errorf("decoding %s: %w", where(p, oid), err)
	}
	return v, nil
}

// readID reads a name object (realms_names.<name> and the like) for the id
// it maps the name to.
func readID(ctx context.Context, p radosclient.Pool, oid string) (string, error) {
	v, err := readRoot(ctx, p, oid, meta.DecodeNameToID)
	return v.ObjID, err
}

// readDefaultID reads a default object (default.realm,
// default.zone.<realm> and the like) for the id it names.
func readDefaultID(ctx context.Context, p radosclient.Pool, oid string) (string, error) {
	v, err := readRoot(ctx, p, oid, meta.DecodeDefaultSystemMetaObjInfo)
	return v.DefaultID, err
}

func missing(err error) bool { return errors.Is(err, radosclient.ErrNotFound) }

// noZone reports a missing root object as ErrNoZone and returns any other
// error as it is.
func noZone(err error) error {
	if missing(err) {
		return fmt.Errorf("%w: %w", ErrNoZone, err)
	}
	return err
}

// resolveZone reads the realm, zone, period and zonegroup the gateway
// serves as radosgw's startup does: rgw::SiteConfig::load, which both
// releases run first (driver/rados/rgw_zone.cc:1172-1334 at v19.2.6,
// :1166-1328 at v20.2.4), then Squid's zone service, RGWSI_Zone::do_start
// (services/svc_zone.cc:128-297 at v19.2.6). It only reads: where radosgw
// would create a missing zonegroup or zone, resolveZone fails with
// ErrNoZone, and the master zone radosgw writes into a zonegroup lacking one
// it sets in memory alone. It does not search the other realms for the
// zone, as Squid's zone service does, because a Rook cluster has one.
func resolveZone(ctx context.Context, pools rootPools, names zoneNames, rel denc.Release) (*zoneConfig, error) {
	zc := &zoneConfig{}
	if err := readRealm(ctx, pools.realm, names, zc); err != nil {
		return nil, err
	}
	if err := readZoneParams(ctx, pools.zone, names, zc); err != nil {
		return nil, err
	}
	if !zc.HasRealm && zc.Params.RealmID != "" {
		// SiteConfig::load reads the realm the zone names when no realm
		// is named or default, and fails without it.
		realm, err := readRoot(ctx, pools.realm, meta.RealmOID(zc.Params.RealmID), meta.DecodeRealm)
		if err != nil {
			return nil, noZone(err)
		}
		zc.Realm, zc.HasRealm = realm, true
	}
	if zc.HasRealm && zc.Realm.CurrentPeriod != "" {
		if err := readPeriod(ctx, pools.period, zc); err != nil {
			return nil, err
		}
	}
	if !zc.HasPeriod || !zc.zoneGroupFromPeriod() {
		if rel >= denc.Tentacle {
			// Tentacle's zone service takes the period from SiteConfig,
			// which drops one that lacks the zone (rgw_zone.cc:1285 and
			// svc_zone.cc:98-100 at v20.2.4); Squid's keeps the realm's
			// current period.
			zc.Period, zc.HasPeriod = meta.Period{}, false
		}
		if err := readLocalZoneGroup(ctx, pools, names, zc); err != nil {
			return nil, err
		}
	}
	// Squid's zone service refuses a zonegroup other than the one
	// rgw_zonegroup names (svc_zone.cc:228-232 at v19.2.6); Tentacle's,
	// which takes the zonegroup from SiteConfig, has no such check.
	if rel < denc.Tentacle && names.ZoneGroup != "" && zc.ZoneGroup.Name != names.ZoneGroup {
		return nil, fmt.Errorf("driver: zonegroup %q is not %q (rgw_zonegroup)", zc.ZoneGroup.Name, names.ZoneGroup)
	}
	zone, ok := zc.ZoneGroup.Zones[zc.Params.ID]
	if !ok {
		return nil, fmt.Errorf("driver: zone %s (%s) is not in zonegroup %s (%s)", zc.Params.ID, zc.Params.Name, zc.ZoneGroup.ID, zc.ZoneGroup.Name)
	}
	zc.Zone = zone
	if err := zc.checkMasterZones(ctx); err != nil {
		return nil, err
	}
	return zc, nil
}

// readRealm reads the realm rgw_realm_id, rgw_realm or default.realm names,
// in that order, as RGWSystemMetaObj::init picks it (rgw_zone.cc:106-145 at
// v19.2.6). A missing realm is no realm, as both zone services take it,
// unless rgw_realm names it, which SiteConfig::load refuses.
func readRealm(ctx context.Context, p radosclient.Pool, names zoneNames, zc *zoneConfig) error {
	id := names.RealmID
	if id == "" {
		var err error
		if names.Realm != "" {
			id, err = readID(ctx, p, meta.RealmNameOID(names.Realm))
		} else {
			id, err = readDefaultID(ctx, p, meta.DefaultRealmOID())
		}
		if err != nil {
			if missing(err) && names.Realm == "" {
				return nil
			}
			return noZone(err)
		}
	}
	realm, err := readRoot(ctx, p, meta.RealmOID(id), meta.DecodeRealm)
	if err != nil {
		if missing(err) && names.Realm == "" {
			return nil
		}
		return noZone(err)
	}
	zc.Realm, zc.HasRealm = realm, true
	return nil
}

// readZoneParams reads the parameters of the zone rgw_zone_id or rgw_zone
// names, or else of the realm's default zone, or else of the zone named
// "default" (SiteConfig::load, RGWZoneParams::init). A default realm without
// a default zone gives way to the zone named "default" when rgw_realm does
// not name the realm, as SiteConfig::load drops such a realm.
func readZoneParams(ctx context.Context, p radosclient.Pool, names zoneNames, zc *zoneConfig) error {
	id := names.ZoneID
	if id == "" {
		var err error
		switch {
		case names.Zone != "":
			id, err = readID(ctx, p, meta.ZoneNameOID(names.Zone))
		case zc.HasRealm:
			id, err = readDefaultID(ctx, p, meta.DefaultZoneOID(zc.Realm.ID))
			if missing(err) && names.Realm == "" && names.RealmID == "" {
				zc.Realm, zc.HasRealm = meta.Realm{}, false
				id, err = readID(ctx, p, meta.ZoneNameOID(defaultName))
			}
		default:
			id, err = readID(ctx, p, meta.ZoneNameOID(defaultName))
		}
		if err != nil {
			return noZone(err)
		}
	}
	params, err := readRoot(ctx, p, meta.ZoneInfoOID(id), meta.DecodeZoneParams)
	if err != nil {
		return noZone(err)
	}
	zc.Params = params
	return nil
}

// readPeriod reads the realm's current period at its latest epoch, as
// read_period without an epoch does (driver/rados/config/period.cc:156-174
// at v19.2.6). A missing period is none: radosgw then takes the local
// zonegroup.
func readPeriod(ctx context.Context, p radosclient.Pool, zc *zoneConfig) error {
	id := zc.Realm.CurrentPeriod
	latest, err := readRoot(ctx, p, meta.PeriodLatestEpochOID(id), meta.DecodePeriodLatestEpochInfo)
	if missing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	period, err := readRoot(ctx, p, meta.PeriodOID(id, latest.Epoch), meta.DecodePeriod)
	if missing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	zc.Period, zc.HasPeriod = period, true
	return nil
}

// zoneGroupFromPeriod takes the zonegroup of the period map that holds the
// zone, in the map's key order, and the period's config, and reports
// whether one did.
func (zc *zoneConfig) zoneGroupFromPeriod() bool {
	for _, id := range slices.Sorted(maps.Keys(zc.Period.PeriodMap.ZoneGroups)) {
		zg := zc.Period.PeriodMap.ZoneGroups[id]
		if _, ok := zg.Zones[zc.Params.ID]; ok {
			zc.ZoneGroup, zc.PeriodConfig, zc.FromPeriod = zg, zc.Period.PeriodConfig, true
			return true
		}
	}
	return false
}

// readLocalZoneGroup reads the zonegroup rgw_zonegroup_id or rgw_zonegroup
// names, or else the realm's default zonegroup, or else the zonegroup named
// "default" (SiteConfig::load_local_zonegroup, RGWZoneGroup::init), then the
// period config.
func readLocalZoneGroup(ctx context.Context, pools rootPools, names zoneNames, zc *zoneConfig) error {
	p := pools.zonegroup
	id := names.ZoneGroupID
	if id == "" {
		var err error
		switch {
		case names.ZoneGroup != "":
			id, err = readID(ctx, p, meta.ZoneGroupNameOID(names.ZoneGroup))
		case zc.HasRealm:
			id, err = readDefaultID(ctx, p, meta.DefaultZoneGroupOID(zc.Realm.ID))
		default:
			id, err = readID(ctx, p, meta.ZoneGroupNameOID(defaultName))
		}
		if err != nil {
			return noZone(err)
		}
	}
	zg, err := readRoot(ctx, p, meta.ZoneGroupInfoOID(id), meta.DecodeZoneGroup)
	if err != nil {
		return noZone(err)
	}
	zc.ZoneGroup = zg
	// The zone service reads the period config of the zonegroup's realm
	// over the current period's, which it keeps when there is none
	// (svc_zone.cc:287-294 at v19.2.6, :123-130 at v20.2.4).
	if zc.HasPeriod {
		zc.PeriodConfig = zc.Period.PeriodConfig
	}
	pc, err := readRoot(ctx, pools.period, meta.PeriodConfigOID(zg.RealmID), meta.DecodePeriodConfig)
	switch {
	case err == nil:
		zc.PeriodConfig = pc
	case !missing(err):
		return err
	}
	return nil
}

// checkMasterZones checks the master zones as init_zg_from_period and
// init_zg_from_local do (svc_zone.cc:488-554 and :588-617 at v19.2.6,
// :324-390 and :392-421 at v20.2.4): every zonegroup of the period, or the
// local zonegroup when it is the master, must have its master zone among
// its zones. A single-zone zonegroup without a master zone is the
// exception: radosgw makes its zone the master and writes the zonegroup
// back, and rgw-go does the first only.
func (zc *zoneConfig) checkMasterZones(ctx context.Context) error {
	if !zc.FromPeriod {
		if zc.ZoneGroup.IsMaster {
			return zc.checkMasterZone(ctx, zc.ZoneGroup)
		}
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(zc.Period.PeriodMap.ZoneGroups)) {
		zg := zc.Period.PeriodMap.ZoneGroups[id]
		// radosgw passes over a zonegroup whose zones were all removed.
		if len(zg.Zones) == 0 {
			continue
		}
		if err := zc.checkMasterZone(ctx, zg); err != nil {
			return err
		}
	}
	return nil
}

func (zc *zoneConfig) checkMasterZone(ctx context.Context, zg meta.ZoneGroup) error {
	if _, ok := zg.Zones[zg.MasterZone]; ok {
		return nil
	}
	if zg.MasterZone != "" || len(zg.Zones) != 1 {
		return fmt.Errorf("driver: zonegroup %s (%s) has no zone for its master zone %q", zg.ID, zg.Name, zg.MasterZone)
	}
	if zg.ID != zc.ZoneGroup.ID {
		return nil
	}
	for id := range zg.Zones {
		zc.ZoneGroup.MasterZone = id
	}
	slog.WarnContext(ctx, "zonegroup has no master zone; serving its only zone as master without writing it back",
		slog.String("zonegroup", zg.Name), slog.String("zone_id", zc.ZoneGroup.MasterZone))
	return nil
}

// log logs the zone, as radosgw logs its Realm, ZoneGroup, Zone and period
// lines (svc_zone.cc:271-276 at v19.2.6).
func (zc *zoneConfig) log(ctx context.Context) {
	slog.InfoContext(ctx, "zone resolved",
		slog.String("realm", zc.Realm.Name), slog.String("realm_id", zc.Realm.ID),
		slog.String("zonegroup", zc.ZoneGroup.Name), slog.String("zonegroup_id", zc.ZoneGroup.ID),
		slog.String("zone", zc.Params.Name), slog.String("zone_id", zc.Params.ID),
		slog.String("period", zc.Period.ID), slog.Uint64("period_epoch", uint64(zc.Period.Epoch)),
		slog.Bool("from_period", zc.FromPeriod))
}

// Placement implements op.ZoneInfo. A rule without a name takes the
// zonegroup's default placement, and its storage class when the rule has
// none either. The rule must then name a placement of the zone and a
// storage class it has, as find_zone_placement requires
// (driver/rados/rgw_zone.cc:1090-1110 at v19.2.6, :1084-1104 at v20.2.4),
// the empty class being STANDARD, which every placement has. The pools are
// RGWZonePlacementInfo's (rgw_zone_types.h:274-303 at both tags): the
// class's data pool, else STANDARD's; the data-extra pool, else STANDARD's
// data pool; the class's compression.
func (s *Store) Placement(rule meta.PlacementRule) (op.Placement, error) {
	if rule.Name == "" {
		def := s.zone.ZoneGroup.DefaultPlacement
		rule.Name = def.Name
		if rule.StorageClass == "" {
			rule.StorageClass = def.StorageClass
		}
	}
	pi, ok := s.zone.Params.PlacementPools[rule.Name]
	if !ok {
		return op.Placement{}, fmt.Errorf("placement %q: %w", rule.Name, op.ErrInvalidLocationConstraint)
	}
	sc := rule.CanonicalStorageClass()
	class, ok := pi.StorageClasses[sc]
	if !ok && sc != meta.StorageClassStandard {
		return op.Placement{}, fmt.Errorf("placement %q storage class %q: %w", rule.Name, sc, op.ErrInvalidLocationConstraint)
	}
	stdPool := deref(pi.StorageClasses[meta.StorageClassStandard].DataPool, meta.Pool{})
	extra := pi.DataExtraPool
	if extra.Name == "" {
		extra = stdPool
	}
	return op.Placement{
		Rule:          meta.PlacementRule{Name: rule.Name, StorageClass: sc},
		DataPool:      deref(class.DataPool, stdPool),
		IndexPool:     pi.IndexPool,
		DataExtraPool: extra,
		Compression:   deref(class.CompressionType, ""),
		InlineData:    pi.InlineData,
	}, nil
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}
