package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The realm getters read the root objects through the metadata cache, as
// radosgw's RGWSystemMetaObj and RGWPeriod read them through RGWSI_SysObj,
// and every error they return is mapped as set_req_state_err maps radosgw's
// (FromRADOS, a missing object being radosgw's -ENOENT, NoSuchKey).

// rootObj names oid in root pool p for the metadata cache.
func rootObj(p radosclient.Pool, oid string) sysObj {
	return sysObj{pool: meta.Pool{Name: p.Name(), NS: p.Namespace()}, oid: oid}
}

// readSysObj reads o whole and decodes it. An object that does not decode is
// radosgw's -EIO, which rgw_http_s3_errors lacks: UnknownError.
func readSysObj[T any](ctx context.Context, s *Store, o sysObj, dec func(*denc.Decoder) T) (T, error) {
	var zero T
	res, err := s.sysobj.read(ctx, o, readParams{data: true})
	if err != nil {
		return zero, err
	}
	d := denc.NewDecoder(res.data)
	v := dec(d)
	if err := d.Err(); err != nil {
		return zero, fmt.Errorf("%w: decoding %s: %w", op.ErrUnknown, o.oid, err)
	}
	return v, nil
}

// GetRealm implements op.RealmStore as RGWSystemMetaObj::init for a realm
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L106-L145 at v19.2.6
// and v20.2.4): with no id, rgw_realm_id; with neither, the name, else
// rgw_realm, through realms_names.<name>; with none of them, default.realm;
// then realms.<id>.
func (s *Store) GetRealm(ctx context.Context, id, name string) (meta.Realm, error) {
	r, err := s.initRealm(ctx, id, name)
	return r, op.FromRADOS(err, op.ScopeService)
}

func (s *Store) initRealm(ctx context.Context, id, name string) (meta.Realm, error) {
	if id == "" {
		id = s.names.RealmID
	}
	if id == "" {
		if name == "" {
			name = s.names.Realm
		}
		var err error
		if name == "" {
			id, err = s.defaultRealmID(ctx)
		} else {
			var n meta.NameToID
			n, err = readSysObj(ctx, s, rootObj(s.roots.realm, meta.RealmNameOID(name)), meta.DecodeNameToID)
			id = n.ObjID
		}
		if err != nil {
			return meta.Realm{}, err
		}
	}
	return readSysObj(ctx, s, rootObj(s.roots.realm, meta.RealmOID(id)), meta.DecodeRealm)
}

// defaultRealmID is RGWSystemMetaObj::read_default_id for a realm
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L1092-L1105 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L1132-L1145 at v20.2.4):
// default.realm's id.
func (s *Store) defaultRealmID(ctx context.Context) (string, error) {
	v, err := readSysObj(ctx, s, rootObj(s.roots.realm, meta.DefaultRealmOID()), meta.DecodeDefaultSystemMetaObjInfo)
	return v.DefaultID, err
}

// ListRealms implements op.RealmStore as RGWOp_Realm_List::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L322-L332 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L343-L353 at v20.2.4):
// the default id, "" whatever keeps it from being read, as radosgw ignores
// read_default_id's result, then RGWSI_Zone::list_realms
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L421-L427 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_zone.cc#L257-L263 at v20.2.4):
// the names after "realms_names." of the realm root pool's objects in the
// pool's listing order, as pool_list_prefixed_objs walks it, skipping a
// name that is the prefix alone
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_core.cc#L579-L607 at v19.2.6
// and v20.2.4).
func (s *Store) ListRealms(ctx context.Context) (defaultID string, names []string, err error) {
	defaultID, _ = s.defaultRealmID(ctx) //nolint:errcheck // radosgw ignores the read's result
	err = s.roots.realm.ListObjects(ctx, func(oid, _ string) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if name, ok := strings.CutPrefix(oid, meta.RealmNamesPrefix); ok && name != "" {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return "", nil, op.FromRADOS(fmt.Errorf("listing the realms: %w", err), op.ScopeService)
	}
	return defaultID, names, nil
}

// GetPeriod implements op.RealmStore as RGWPeriod::init (rgw_period.cc:14-60
// at v19.2.6 and v20.2.4): with no period id, the current period of the
// realm initRealm resolves, under that realm's id; with no epoch,
// periods.<id>.latest_epoch's; then the period object, named as
// meta.RealmPeriodOID names it.
func (s *Store) GetPeriod(ctx context.Context, realmID, periodID string, epoch uint32) (meta.Period, error) {
	p, err := s.initPeriod(ctx, realmID, periodID, epoch)
	return p, op.FromRADOS(err, op.ScopeService)
}

func (s *Store) initPeriod(ctx context.Context, realmID, periodID string, epoch uint32) (meta.Period, error) {
	if periodID == "" {
		realm, err := s.initRealm(ctx, realmID, "")
		if err != nil {
			return meta.Period{}, err
		}
		periodID, realmID = realm.CurrentPeriod, realm.ID
	}
	if epoch == 0 {
		latest, err := readSysObj(ctx, s, rootObj(s.roots.period, meta.PeriodLatestEpochOID(periodID)), meta.DecodePeriodLatestEpochInfo)
		if err != nil {
			return meta.Period{}, err
		}
		epoch = latest.Epoch
	}
	return readSysObj(ctx, s, rootObj(s.roots.period, meta.RealmPeriodOID(realmID, periodID, epoch)), meta.DecodePeriod)
}

// GetPeriodConfig implements op.RealmStore as RGWPeriodConfig::read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L616-L636 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L624-L644 at v20.2.4):
// period_config.<realm id> in the period root pool. A missing object is
// ErrNotFound, which the callers take as the zero config, as radosgw's do
// its -ENOENT.
func (s *Store) GetPeriodConfig(ctx context.Context, realmID string) (meta.PeriodConfig, error) {
	cfg, err := readSysObj(ctx, s, rootObj(s.roots.period, meta.PeriodConfigOID(realmID)), meta.DecodePeriodConfig)
	switch {
	case missing(err):
		return meta.PeriodConfig{}, fmt.Errorf("%w: %w", op.ErrNotFound, err)
	case err != nil:
		return meta.PeriodConfig{}, op.FromRADOS(err, op.ScopeService)
	}
	return cfg, nil
}

// PutPeriodConfig implements op.RealmStore as RGWPeriodConfig::write
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L638-L651 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L646-L659 at v20.2.4):
// the config encoded at the cluster's release, written whole, not
// exclusively and unversioned, through the metadata cache.
func (s *Store) PutPeriodConfig(ctx context.Context, realmID string, cfg meta.PeriodConfig) error {
	e := denc.NewEncoder()
	cfg.Encode(e, s.release)
	_, err := s.sysobj.write(ctx, rootObj(s.roots.period, meta.PeriodConfigOID(realmID)), e.Bytes(), nil, false, time.Time{}, nil)
	return op.FromRADOS(err, op.ScopeService)
}
