package meta

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ZoneParams is RGWZoneParams, the zone_info.<id> object: the zone's pools,
// system key and placement. It holds every field main decodes; the pools a
// stored version predates are filled as RGWZoneParams::decode fills them.
type ZoneParams struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	DomainRoot        Pool   `json:"domain_root"`
	ControlPool       Pool   `json:"control_pool"`
	DedupPool         Pool   `json:"dedup_pool"`
	GCPool            Pool   `json:"gc_pool"`
	LCPool            Pool   `json:"lc_pool"`
	LogPool           Pool   `json:"log_pool"`
	IntentLogPool     Pool   `json:"intent_log_pool"`
	UsageLogPool      Pool   `json:"usage_log_pool"`
	RolesPool         Pool   `json:"roles_pool"`
	ReshardPool       Pool   `json:"reshard_pool"`
	UserKeysPool      Pool   `json:"user_keys_pool"`
	UserEmailPool     Pool   `json:"user_email_pool"`
	UserSwiftPool     Pool   `json:"user_swift_pool"`
	UserUIDPool       Pool   `json:"user_uid_pool"`
	OTPPool           Pool   `json:"otp_pool"`
	NotifPool         Pool   `json:"notif_pool"`
	TopicsPool        Pool   `json:"topics_pool"`
	AccountPool       Pool   `json:"account_pool"`
	GroupPool         Pool   `json:"group_pool"`
	BucketLoggingPool Pool   `json:"bucket_logging_pool"`
	// OIDCPool is encoded but, like the C++ dump, never printed.
	OIDCPool       Pool                         `json:"-"`
	SystemKey      AccessKey                    `json:"-"`
	PlacementPools map[string]ZonePlacementInfo `json:"-"`
	TierConfig     JSONFormattable              `json:"tier_config,omitzero"`
	RealmID        string                       `json:"realm_id"`
	RestorePool    Pool                         `json:"restore_pool"`
	// VectorPool is main's; JSON carries it only when it differs from the
	// default RGWZoneParams::decode fills for a zone that predates it.
	VectorPool Pool `json:"-"`
}

// MarshalJSON renders RGWZoneParams::dump as v20.2.4 writes it: the system
// key as dump_plain writes it, and placement_pools in std::map form.
func (z ZoneParams) MarshalJSON() ([]byte, error) {
	type plain ZoneParams
	var vector *Pool
	if z.VectorPool != z.defaultVectorPool() {
		vector = &z.VectorPool
	}
	return json.Marshal(struct {
		plain
		SystemKey      systemKeyPlain                        `json:"system_key"`
		PlacementPools []mapEntry[string, ZonePlacementInfo] `json:"placement_pools"`
		VectorPool     *Pool                                 `json:"vector_pool,omitempty"`
	}{plain(z), systemKeyPlain{z.SystemKey.ID, z.SystemKey.Secret}, mapEntries(z.PlacementPools), vector})
}

func (z ZoneParams) defaultVectorPool() Pool { return ParsePool(z.Name + ".rgw.meta:vector") }

// systemKeyPlain is RGWAccessKey::dump_plain, the form encode_json_plain
// gives the zone's system key.
type systemKeyPlain struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// Encode mirrors RGWZoneParams::encode: ENCODE_START(15, 1) for Squid and
// ENCODE_START(18, 1) for Tentacle (v20.2.4), which appends the restore, dedup
// and bucket logging pools. Main's version 19 vector pool is not written.
func (z ZoneParams) Encode(e *denc.Encoder, r denc.Release) {
	version := uint8(15)
	if r >= denc.Tentacle {
		// v20.2.0 and v20.2.1 write 17 and read this through compat 1, but a
		// rewrite by such a radosgw drops bucket_logging_pool.
		version = 18
	}
	f := e.BeginStruct(version, 1)
	for _, p := range []Pool{
		z.DomainRoot, z.ControlPool, z.GCPool, z.LogPool, z.IntentLogPool, z.UsageLogPool,
		z.UserKeysPool, z.UserEmailPool, z.UserSwiftPool, z.UserUIDPool,
	} {
		p.Encode(e, r)
	}
	encodeSysObj(e, z.ID, z.Name)
	z.SystemKey.Encode(e, r)
	denc.EncodeMap(e, z.PlacementPools, (*denc.Encoder).String,
		func(e *denc.Encoder, p ZonePlacementInfo) { p.Encode(e, r) })
	Pool{}.Encode(e, r) // unused metadata_heap
	e.String(z.RealmID)
	z.LCPool.Encode(e, r)
	e.U32(0) // old_tier_config, always empty
	z.RolesPool.Encode(e, r)
	z.ReshardPool.Encode(e, r)
	z.OTPPool.Encode(e, r)
	z.TierConfig.Encode(e, r)
	z.OIDCPool.Encode(e, r)
	z.NotifPool.Encode(e, r)
	z.TopicsPool.Encode(e, r)
	z.AccountPool.Encode(e, r)
	z.GroupPool.Encode(e, r)
	if r >= denc.Tentacle {
		z.RestorePool.Encode(e, r)
		z.DedupPool.Encode(e, r)
		z.BucketLoggingPool.Encode(e, r)
	}
	e.EndStruct(f)
}

// DecodeZoneParams mirrors RGWZoneParams::decode in main, DECODE_START(19).
// Version 5 and below stored the name without the RGWSystemMetaObj framing,
// and versions 8 to 11 stored the tier config as a string map, which is
// converted through JSONFormattable::set.
func DecodeZoneParams(d *denc.Decoder) ZoneParams {
	h := d.BeginStruct(19)
	v := h.Version
	var z ZoneParams
	for _, p := range []*Pool{
		&z.DomainRoot, &z.ControlPool, &z.GCPool, &z.LogPool, &z.IntentLogPool, &z.UsageLogPool,
		&z.UserKeysPool, &z.UserEmailPool, &z.UserSwiftPool, &z.UserUIDPool,
	} {
		*p = DecodePool(d)
	}
	switch {
	case v >= 6:
		z.ID, z.Name = decodeSysObj(d)
	case v >= 2:
		z.Name = d.String()
		z.ID = z.Name
	}
	if v >= 3 {
		z.SystemKey = DecodeAccessKey(d)
	}
	if v >= 4 {
		z.PlacementPools = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeZonePlacementInfo)
	}
	if v >= 5 {
		DecodePool(d) // unused metadata_heap
	}
	if v >= 6 {
		z.RealmID = d.String()
	}
	z.LCPool = decodeOrDefault(d, v >= 7, z.LogPool.Name+":lc")
	var oldTierConfig [][2]string
	if v >= 8 {
		oldTierConfig = decodeNoCaseMap(d)
	}
	z.RolesPool = decodeOrDefault(d, v >= 9, z.Name+".rgw.meta:roles")
	z.ReshardPool = decodeOrDefault(d, v >= 10, z.LogPool.Name+":reshard")
	z.OTPPool = decodeOrDefault(d, v >= 11, z.Name+".rgw.otp")
	if v >= 12 {
		z.TierConfig = DecodeJSONFormattable(d)
	} else {
		for _, kv := range oldTierConfig {
			if err := setFormattable(&z.TierConfig, kv[0], kv[1]); !errorsIsPath(err) {
				d.Fail(err)
			}
		}
	}
	z.OIDCPool = decodeOrDefault(d, v >= 13, z.Name+".rgw.meta:oidc")
	z.NotifPool = decodeOrDefault(d, v >= 14, z.LogPool.Name+":notif")
	z.TopicsPool = decodeOrDefault(d, v >= 15, z.Name+".rgw.meta:topics")
	z.AccountPool = decodeOrDefault(d, v >= 15, z.Name+".rgw.meta:accounts")
	z.GroupPool = decodeOrDefault(d, v >= 15, z.Name+".rgw.meta:groups")
	z.RestorePool = decodeOrDefault(d, v >= 16, z.LogPool.Name+":restore")
	z.DedupPool = decodeOrDefault(d, v >= 17, z.Name+".rgw.dedup")
	z.BucketLoggingPool = decodeOrDefault(d, v >= 18, z.LogPool.Name+":logging")
	if v >= 19 {
		z.VectorPool = DecodePool(d)
	} else {
		z.VectorPool = z.defaultVectorPool()
	}
	d.EndStruct(h)
	return z
}

// errorsIsPath reports whether err is nil or the -EINVAL that
// RGWZoneParams::decode ignores from JSONFormattable::set.
func errorsIsPath(err error) bool { return err == nil || errors.Is(err, errFormattablePath) }

// decodeOrDefault decodes a pool when present, else assigns the string form
// the C++ assigns, which rgw_pool's string constructor parses.
func decodeOrDefault(d *denc.Decoder, present bool, def string) Pool {
	if present {
		return DecodePool(d)
	}
	return ParsePool(def)
}

// decodeNoCaseMap reads std::map<string, string, ltstr_nocase> in the
// container's order: ASCII case-insensitive, the first of two keys equal
// but for case kept, as emplace keeps it.
func decodeNoCaseMap(d *denc.Decoder) [][2]string {
	kvs := denc.DecodeSlice(d, func(d *denc.Decoder) [2]string {
		k := d.String()
		return [2]string{k, d.String()}
	})
	slices.SortStableFunc(kvs, func(a, b [2]string) int { return strcasecmp(a[0], b[0]) })
	return slices.CompactFunc(kvs, func(a, b [2]string) bool { return strcasecmp(a[0], b[0]) == 0 })
}

// strcasecmp is C's strcasecmp in the C locale: ASCII letters fold to lower
// case, other bytes compare unsigned, and a NUL ends the string.
func strcasecmp(a, b string) int {
	if i := strings.IndexByte(a, 0); i >= 0 {
		a = a[:i]
	}
	if i := strings.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		ca, cb := lowerASCII(a[i]), lowerASCII(b[i])
		if ca != cb {
			return int(ca) - int(cb)
		}
	}
	return len(a) - len(b)
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// ZonePool names a ZoneParams pool field by its JSON name.
type ZonePool string

// The ZoneParams pool fields.
const (
	ZonePoolDomainRoot    ZonePool = "domain_root"
	ZonePoolControl       ZonePool = "control_pool"
	ZonePoolDedup         ZonePool = "dedup_pool"
	ZonePoolGC            ZonePool = "gc_pool"
	ZonePoolLC            ZonePool = "lc_pool"
	ZonePoolLog           ZonePool = "log_pool"
	ZonePoolIntentLog     ZonePool = "intent_log_pool"
	ZonePoolUsageLog      ZonePool = "usage_log_pool"
	ZonePoolRoles         ZonePool = "roles_pool"
	ZonePoolReshard       ZonePool = "reshard_pool"
	ZonePoolUserKeys      ZonePool = "user_keys_pool"
	ZonePoolUserEmail     ZonePool = "user_email_pool"
	ZonePoolUserSwift     ZonePool = "user_swift_pool"
	ZonePoolUserUID       ZonePool = "user_uid_pool"
	ZonePoolOTP           ZonePool = "otp_pool"
	ZonePoolOIDC          ZonePool = "oidc_pool"
	ZonePoolNotif         ZonePool = "notif_pool"
	ZonePoolTopics        ZonePool = "topics_pool"
	ZonePoolAccount       ZonePool = "account_pool"
	ZonePoolGroup         ZonePool = "group_pool"
	ZonePoolBucketLogging ZonePool = "bucket_logging_pool"
	ZonePoolRestore       ZonePool = "restore_pool"
	ZonePoolVector        ZonePool = "vector_pool"
)

// PoolFor returns the pool and RADOS namespace of the named pool field, and
// false for a name that is not one.
func (z ZoneParams) PoolFor(p ZonePool) (Pool, bool) {
	pools := map[ZonePool]Pool{
		ZonePoolDomainRoot: z.DomainRoot, ZonePoolControl: z.ControlPool, ZonePoolDedup: z.DedupPool,
		ZonePoolGC: z.GCPool, ZonePoolLC: z.LCPool, ZonePoolLog: z.LogPool,
		ZonePoolIntentLog: z.IntentLogPool, ZonePoolUsageLog: z.UsageLogPool, ZonePoolRoles: z.RolesPool,
		ZonePoolReshard: z.ReshardPool, ZonePoolUserKeys: z.UserKeysPool, ZonePoolUserEmail: z.UserEmailPool,
		ZonePoolUserSwift: z.UserSwiftPool, ZonePoolUserUID: z.UserUIDPool, ZonePoolOTP: z.OTPPool,
		ZonePoolOIDC: z.OIDCPool, ZonePoolNotif: z.NotifPool, ZonePoolTopics: z.TopicsPool,
		ZonePoolAccount: z.AccountPool, ZonePoolGroup: z.GroupPool, ZonePoolBucketLogging: z.BucketLoggingPool,
		ZonePoolRestore: z.RestorePool, ZonePoolVector: z.VectorPool,
	}
	pool, ok := pools[p]
	return pool, ok
}

// ZonePlacementInfo is RGWZonePlacementInfo, one entry of a zone's
// placement_pools. The zero value lacks the C++ defaults, inline data and a
// STANDARD class; NewZonePlacementInfo has them.
type ZonePlacementInfo struct {
	IndexPool      Pool               `json:"index_pool"`
	StorageClasses ZoneStorageClasses `json:"storage_classes"`
	DataExtraPool  Pool               `json:"data_extra_pool"`
	// IndexType is rgw::BucketIndexType, a u8 encoded as a u32.
	IndexType  uint8 `json:"index_type"`
	InlineData bool  `json:"inline_data"`
}

// Encode mirrors RGWZonePlacementInfo::encode, ENCODE_START(8, 1): the pools
// in string form, and the STANDARD class's data pool and compression again
// ahead of the storage classes for older decoders.
func (p ZonePlacementInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(8, 1)
	std := p.StorageClasses[StorageClassStandard]
	e.String(p.IndexPool.String())
	e.String(derefOr(std.DataPool, Pool{}).String())
	e.String(p.DataExtraPool.String())
	e.U32(uint32(p.IndexType))
	e.String(derefOr(std.CompressionType, ""))
	p.StorageClasses.Encode(e, r)
	e.Bool(p.InlineData)
	e.EndStruct(f)
}

func derefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// DecodeZonePlacementInfo mirrors RGWZonePlacementInfo::decode. Before
// version 7 the STANDARD class came from the separate data pool and
// compression fields; inline data defaults to true.
func DecodeZonePlacementInfo(d *denc.Decoder) ZonePlacementInfo {
	h := d.BeginStruct(8)
	v := h.Version
	p := ZonePlacementInfo{IndexPool: ParsePool(d.String()), InlineData: true}
	dataPool := ParsePool(d.String())
	if v >= 4 {
		p.DataExtraPool = ParsePool(d.String())
	}
	if v >= 5 {
		p.IndexType = uint8(d.U32()) //nolint:gosec // truncated to BucketIndexType's u8, as the C++ cast does
	}
	var compression string
	if v >= 6 {
		compression = d.String()
	}
	if v >= 7 {
		p.StorageClasses = DecodeZoneStorageClasses(d)
	} else {
		std := ZoneStorageClass{DataPool: &dataPool}
		if compression != "" {
			std.CompressionType = &compression
		}
		p.StorageClasses = ZoneStorageClasses{StorageClassStandard: std}
	}
	if v >= 8 {
		p.InlineData = d.Bool()
	}
	d.EndStruct(h)
	return p
}

// ZoneStorageClasses is RGWZoneStorageClasses, keyed by storage class. The
// C++ always holds STANDARD, inserting it on construction and decode; a
// decoded value holds it, and so does NewZoneStorageClasses, but nil does not.
type ZoneStorageClasses map[string]ZoneStorageClass

// NewZoneStorageClasses returns what RGWZoneStorageClasses' constructor
// builds: an empty STANDARD class.
func NewZoneStorageClasses() ZoneStorageClasses {
	return ZoneStorageClasses{StorageClassStandard: {}}
}

// NewZonePlacementInfo returns what RGWZonePlacementInfo's constructor
// builds: the normal index type, inline data, and NewZoneStorageClasses.
func NewZonePlacementInfo() ZonePlacementInfo {
	return ZonePlacementInfo{StorageClasses: NewZoneStorageClasses(), InlineData: true}
}

// MarshalJSON renders RGWZoneStorageClasses::dump, an object keyed by class.
func (c ZoneStorageClasses) MarshalJSON() ([]byte, error) {
	if c == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]ZoneStorageClass(c))
}

// Encode mirrors RGWZoneStorageClasses::encode, ENCODE_START(1, 1).
func (c ZoneStorageClasses) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeMap(e, c, (*denc.Encoder).String, func(e *denc.Encoder, s ZoneStorageClass) { s.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeZoneStorageClasses mirrors RGWZoneStorageClasses::decode, which
// inserts an empty STANDARD class when the map lacks one.
func DecodeZoneStorageClasses(d *denc.Decoder) ZoneStorageClasses {
	h := d.BeginStruct(1)
	c := ZoneStorageClasses(denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeZoneStorageClass))
	d.EndStruct(h)
	if d.Err() != nil {
		return nil
	}
	if c == nil {
		c = ZoneStorageClasses{}
	}
	if _, ok := c[StorageClassStandard]; !ok {
		c[StorageClassStandard] = ZoneStorageClass{}
	}
	return c
}

// ZoneStorageClass is RGWZoneStorageClass: an optional data pool and
// compression type.
type ZoneStorageClass struct {
	DataPool        *Pool   `json:"data_pool,omitempty"`
	CompressionType *string `json:"compression_type,omitempty"`
}

// Encode mirrors RGWZoneStorageClass::encode, ENCODE_START(1, 1).
func (s ZoneStorageClass) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeOptional(e, s.DataPool, func(e *denc.Encoder, p Pool) { p.Encode(e, r) })
	denc.EncodeOptional(e, s.CompressionType, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeZoneStorageClass mirrors RGWZoneStorageClass::decode.
func DecodeZoneStorageClass(d *denc.Decoder) ZoneStorageClass {
	h := d.BeginStruct(1)
	var s ZoneStorageClass
	s.DataPool = denc.DecodeOptional(d, DecodePool)
	s.CompressionType = denc.DecodeOptional(d, (*denc.Decoder).String)
	d.EndStruct(h)
	return s
}
