package op

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// GetInfo is RGWOp_Info_Get
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_info.cc#L10-L44 at v19.2.6
// and v20.2.4): the cluster's fsid under cap info=read.
type GetInfo struct {
	AdminOp
	// ClusterID is the result, the RADOS fsid.
	ClusterID string
}

// NewGetInfo returns a GetInfo.
func NewGetInfo() *GetInfo { return &GetInfo{} }

// Name is get_info.
func (o *GetInfo) Name() string { return "get_info" }

// VerifyPermission is RGWOp_Info_Get::check_caps.
func (o *GetInfo) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "info", meta.CapRead)
}

// Execute reads the fsid radosgw's driver reports as its cluster id.
func (o *GetInfo) Execute(_ context.Context, r *Request) error {
	o.ClusterID = r.Env.ClusterID
	return nil
}

// GetZoneConfig is RGWOp_ZoneConfig_Get
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_config.h#L22-L38 at v19.2.6
// and v20.2.4): the zone's parameters under cap zone=read.
type GetZoneConfig struct {
	AdminOp
	// Params is the result.
	Params meta.ZoneParams
}

// NewGetZoneConfig returns a GetZoneConfig.
func NewGetZoneConfig() *GetZoneConfig { return &GetZoneConfig{} }

// Name is get_zone_config.
func (o *GetZoneConfig) Name() string { return "get_zone_config" }

// VerifyPermission is RGWOp_ZoneConfig_Get::check_caps.
func (o *GetZoneConfig) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "zone", meta.CapRead)
}

// Execute reads the zone parameters the gateway loaded at startup, as
// radosgw's send_response reads its zone service's.
func (o *GetZoneConfig) Execute(_ context.Context, r *Request) error {
	o.Params = r.Env.Zone.ZoneParams()
	return nil
}

// GetRealm is RGWOp_Realm_Get
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L262-L304 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L283-L325 at v20.2.4):
// a realm by id, by name, or the one RGWRealm::init resolves for
// neither, under cap zone=read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L265-L270 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L286-L291 at v20.2.4).
type GetRealm struct {
	AdminOp
	ID string
	// RealmName is the name argument; Name is the op's.
	RealmName string
	Result    meta.Realm
}

// Name is get_realm.
func (o *GetRealm) Name() string { return "get_realm" }

// VerifyPermission is RGWOp_Realm_Get::check_caps.
func (o *GetRealm) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "zone", meta.CapRead)
}

// Execute reads the realm.
func (o *GetRealm) Execute(ctx context.Context, r *Request) error {
	realm, err := r.Env.Realms.GetRealm(ctx, o.ID, o.RealmName)
	if err != nil {
		return err
	}
	o.Result = realm
	return nil
}

// ListRealms is RGWOp_Realm_List
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L307-L350 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L328-L371 at v20.2.4):
// the default realm's id and the realm names, under cap zone=read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L311-L316 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L332-L337 at v20.2.4).
type ListRealms struct {
	AdminOp
	DefaultID string
	Names     []string
}

// Name is list_realms.
func (o *ListRealms) Name() string { return "list_realms" }

// VerifyPermission is RGWOp_Realm_List::check_caps.
func (o *ListRealms) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "zone", meta.CapRead)
}

// Execute lists the realms.
func (o *ListRealms) Execute(ctx context.Context, r *Request) error {
	def, names, err := r.Env.Realms.ListRealms(ctx)
	if err != nil {
		return err
	}
	o.DefaultID, o.Names = def, names
	return nil
}

// GetPeriod is RGWOp_Period_Get
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L54-L80 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L55-L81 at v20.2.4):
// a period of a realm, the realm's current one when PeriodID is empty, at
// its latest epoch when Epoch is 0, under cap zone=read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L57-L62 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L58-L63 at v20.2.4).
type GetPeriod struct {
	AdminOp
	RealmID, PeriodID string
	Epoch             uint32
	Result            meta.Period
}

// Name is get_period.
func (o *GetPeriod) Name() string { return "get_period" }

// VerifyPermission is RGWOp_Period_Get::check_caps.
func (o *GetPeriod) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "zone", meta.CapRead)
}

// Execute reads the period.
func (o *GetPeriod) Execute(ctx context.Context, r *Request) error {
	p, err := r.Env.Realms.GetPeriod(ctx, o.RealmID, o.PeriodID, o.Epoch)
	if err != nil {
		return err
	}
	o.Result = p
	return nil
}

// The ratelimit scopes, ratelimit-scope's values.
const (
	rateLimitBucket = "bucket"
	rateLimitUser   = "user"
	rateLimitAnon   = "anon"
)

// GetRateLimit is RGWOp_Ratelimit_Info
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L4-L120 at v19.2.6
// and v20.2.4) under cap ratelimit=read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L5-L7):
// a bucket's or a user's limits, or with Global the realm's period
// config. The handler refuses a global argument other than true or false,
// as radosgw checks it before anything else
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L24-L32).
type GetRateLimit struct {
	AdminOp
	UID, Scope, Bucket, Tenant string
	Global                     bool
	// Result is the bucket's or user's limits.
	Result meta.RateLimitInfo
	// Period is the realm's period config, read with Global.
	Period meta.PeriodConfig
}

// Name is get_ratelimit_info.
func (o *GetRateLimit) Name() string { return "get_ratelimit_info" }

// VerifyPermission is RGWOp_Ratelimit_Info::check_caps, which
// RGWRESTOp::verify_permission runs
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1687-L1690 at v19.2.6).
func (o *GetRateLimit) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "ratelimit", meta.CapRead)
}

// Execute is the op's branches in radosgw's order
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L35-L120):
// a bucket scope naming a bucket, a user scope naming a uid, then global;
// anything else is -EINVAL. radosgw's user branch falls through to -EINVAL
// once it has flushed its body, after the 200 and the body are sent, so the
// client sees the limits, which is what this answers.
func (o *GetRateLimit) Execute(ctx context.Context, r *Request) error {
	switch {
	case o.Scope == rateLimitBucket && o.Bucket != "" && !o.Global:
		rec, err := loadRateLimitBucket(ctx, r.Env, o.Tenant, o.Bucket)
		if err != nil {
			return err
		}
		o.Result, err = decodeRateLimit(rec.Attrs, "bucket "+o.Bucket)
		return err
	case o.Scope == rateLimitUser && o.UID != "" && !o.Global:
		rec, err := loadRateLimitUser(ctx, r.Env, o.UID)
		if err != nil {
			return err
		}
		o.Result, err = decodeRateLimit(rec.Attrs, "user "+o.UID)
		return err
	case o.Global:
		cfg, err := readPeriodConfig(ctx, r.Env)
		if err != nil {
			return err
		}
		o.Period = cfg
		return nil
	}
	return ErrInvalidArgument
}

// SetRateLimit is RGWOp_Ratelimit_Set
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L122-L343 at v19.2.6
// and v20.2.4) under cap ratelimit=write
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L123-L125):
// the limits given laid over a user's, a bucket's, or with Global one
// scope of the realm's period config. A nil field was not given; a
// negative limit is ignored (set_ratelimit_info,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L141-L175).
// The handler refuses a limit that does not parse and an enabled or global
// other than true or false, as radosgw does before anything else
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L197-L227).
type SetRateLimit struct {
	AdminOp
	UID, Scope, Bucket, Tenant                           string
	Global                                               bool
	MaxReadOps, MaxWriteOps, MaxReadBytes, MaxWriteBytes *int64
	Enabled                                              *bool
}

// Name is put_ratelimit_info.
func (o *SetRateLimit) Name() string { return "put_ratelimit_info" }

// VerifyPermission is RGWOp_Ratelimit_Set::check_caps.
func (o *SetRateLimit) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "ratelimit", meta.CapWrite)
}

// apply is set_ratelimit_info over l: each limit given and not negative,
// and enabled when given. It reports whether it set anything.
func (o *SetRateLimit) apply(l *meta.RateLimitInfo) bool {
	set := false
	for _, f := range []struct {
		v   *int64
		dst *int64
	}{
		{o.MaxReadOps, &l.MaxReadOps},
		{o.MaxWriteOps, &l.MaxWriteOps},
		{o.MaxReadBytes, &l.MaxReadBytes},
		{o.MaxWriteBytes, &l.MaxWriteBytes},
	} {
		if f.v != nil && *f.v >= 0 {
			*f.dst, set = *f.v, true
		}
	}
	if o.Enabled != nil {
		l.Enabled, set = *o.Enabled, true
	}
	return set
}

// Execute refuses a request that sets nothing, "No rate limit configuration
// arguments have been sent"
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L169-L172),
// then runs radosgw's branches. The user and bucket writes are
// merge_and_store_attrs
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L238-L244
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L771-L778 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L255-L261
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L789-L796 at v20.2.4):
// the user rewritten under the version it was read at, the bucket's attrs
// under the instance's, so a write that lost a race to another is
// ConcurrentModification and leaves the other's change; radosgw does not
// retry it. The user's store_user passes no old_info
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L269-L276 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L286-L293 at v20.2.4),
// so its write links the user into its account again
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L360-L397 at v19.2.6),
// as writeUserWith's every write does. The period config is read, changed
// and written back unguarded, as RGWPeriodConfig::write writes it.
func (o *SetRateLimit) Execute(ctx context.Context, r *Request) error {
	if !o.apply(new(meta.RateLimitInfo)) {
		return ErrInvalidArgument
	}
	env := r.Env
	rel := env.Zone.Release()
	switch {
	case o.Scope == rateLimitUser && o.UID != "" && !o.Global:
		rec, err := loadRateLimitUser(ctx, env, o.UID)
		if err != nil {
			return err
		}
		l, err := decodeRateLimit(rec.Attrs, "user "+o.UID)
		if err != nil {
			return err
		}
		o.apply(&l)
		attrs := maps.Clone(rec.Attrs)
		if attrs == nil {
			attrs = map[string][]byte{}
		}
		attrs[meta.AttrRateLimit] = encodeAt(l, rel)
		rec.Attrs = attrs
		old, read := rec.Info, rec.Version
		return writeUserWith(ctx, env, rec, &old, ErrBucketAlreadyExists,
			PutUserOptions{IfVersion: &read, SharedEmail: !confFlag(env, "rgw_user_unique_email", true)})
	case o.Scope == rateLimitBucket && o.Bucket != "" && !o.Global:
		rec, err := loadRateLimitBucket(ctx, env, o.Tenant, o.Bucket)
		if err != nil {
			return err
		}
		l, err := decodeRateLimit(rec.Attrs, "bucket "+o.Bucket)
		if err != nil {
			return err
		}
		o.apply(&l)
		return env.Buckets.PutBucketAttrs(ctx, rec, map[string][]byte{meta.AttrRateLimit: encodeAt(l, rel)}, nil)
	case o.Global:
		cfg, err := readPeriodConfig(ctx, env)
		if err != nil {
			return err
		}
		switch o.Scope {
		case rateLimitBucket:
			o.apply(&cfg.BucketRateLimit)
		case rateLimitAnon:
			o.apply(&cfg.AnonRateLimit)
		case rateLimitUser:
			o.apply(&cfg.UserRateLimit)
		default:
			return ErrInvalidArgument
		}
		return env.Realms.PutPeriodConfig(ctx, env.Zone.Realm().ID, cfg)
	}
	return ErrInvalidArgument
}

// loadRateLimitBucket is load_bucket of rgw_bucket(tenant, name), whose
// missing bucket is -ENOENT, NoSuchKey.
func loadRateLimitBucket(ctx context.Context, env *Env, tenant, name string) (*BucketRecord, error) {
	rec, err := env.Buckets.GetBucket(ctx, tenant, name)
	if errors.Is(err, ErrNoSuchBucket) {
		return nil, fmt.Errorf("%w: bucket %s", ErrNoSuchKey, name)
	}
	return rec, err
}

// loadRateLimitUser is load_user of rgw_user(uid), whose missing user is
// -ENOENT, NoSuchKey.
func loadRateLimitUser(ctx context.Context, env *Env, uid string) (*UserRecord, error) {
	rec, err := env.Users.GetUser(ctx, meta.ParseUserID(uid))
	if errors.Is(err, ErrNoSuchUser) {
		return nil, fmt.Errorf("%w: user %s", ErrNoSuchKey, uid)
	}
	return rec, err
}

// decodeRateLimit decodes the ratelimit attr of attrs, the zero limits when
// there is none; one that does not decode is radosgw's -EIO, UnknownError.
func decodeRateLimit(attrs map[string][]byte, of string) (meta.RateLimitInfo, error) {
	raw, ok := attrs[meta.AttrRateLimit]
	if !ok {
		return meta.RateLimitInfo{}, nil
	}
	d := denc.NewDecoder(raw)
	l := meta.DecodeRateLimitInfo(d)
	if err := d.Err(); err != nil {
		return meta.RateLimitInfo{}, fmt.Errorf("%w: decoding the ratelimit of %s: %w", ErrUnknown, of, err)
	}
	return l, nil
}

// readPeriodConfig is RGWPeriodConfig::read for the zone's realm, as
// RadosZone::get_realm_id names it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L4100-L4103 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L5254-L5257 at v20.2.4);
// a missing config is the zero config, as radosgw takes its -ENOENT.
func readPeriodConfig(ctx context.Context, env *Env) (meta.PeriodConfig, error) {
	cfg, err := env.Realms.GetPeriodConfig(ctx, env.Zone.Realm().ID)
	if errors.Is(err, ErrNotFound) {
		return meta.PeriodConfig{}, nil
	}
	return cfg, err
}
