package meta

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Period is RGWPeriod, one epoch of a realm's configuration, the
// periods.<id>.<epoch> object. The zero value has realm epoch 0;
// NewPeriod gives the C++ default of 1.
type Period struct {
	ID              string       `json:"id"`
	Epoch           uint32       `json:"epoch"`
	PredecessorUUID string       `json:"predecessor_uuid"`
	SyncStatus      []string     `json:"sync_status"`
	PeriodMap       PeriodMap    `json:"period_map"`
	MasterZoneGroup string       `json:"master_zonegroup"`
	MasterZone      string       `json:"master_zone"`
	PeriodConfig    PeriodConfig `json:"period_config"`
	RealmID         string       `json:"realm_id"`
	// RealmEpoch is the realm's epoch when the period was made current.
	RealmEpoch uint32 `json:"realm_epoch"`
}

// NewPeriod returns what RGWPeriod's member initializers give: epoch 0 of
// realm epoch 1.
func NewPeriod() Period { return Period{RealmEpoch: 1} }

// MarshalJSON renders RGWPeriod::dump.
func (p Period) MarshalJSON() ([]byte, error) {
	type plain Period
	q := plain(p)
	q.SyncStatus = orEmpty(q.SyncStatus)
	return json.Marshal(q)
}

// Encode mirrors RGWPeriod::encode, ENCODE_START(1, 1). The removed
// realm_name is written empty, as the C++ writes it.
func (p Period) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(p.ID)
	e.U32(p.Epoch)
	e.U32(p.RealmEpoch)
	e.String(p.PredecessorUUID)
	encodeStrings(e, p.SyncStatus)
	p.PeriodMap.Encode(e, r)
	e.String(p.MasterZone)
	e.String(p.MasterZoneGroup)
	p.PeriodConfig.Encode(e, r)
	e.String(p.RealmID)
	e.String("")
	e.EndStruct(f)
}

// DecodePeriod mirrors RGWPeriod::decode, which discards realm_name.
func DecodePeriod(d *denc.Decoder) Period {
	h := d.BeginStruct(1)
	var p Period
	p.ID = d.String()
	p.Epoch = d.U32()
	p.RealmEpoch = d.U32()
	p.PredecessorUUID = d.String()
	p.SyncStatus = decodeStrings(d)
	p.PeriodMap = DecodePeriodMap(d)
	p.MasterZone = d.String()
	p.MasterZoneGroup = d.String()
	p.PeriodConfig = DecodePeriodConfig(d)
	p.RealmID = d.String()
	_ = d.String() // realm_name, removed
	d.EndStruct(h)
	return p
}

// PeriodMap is RGWPeriodMap, a period's zonegroups.
type PeriodMap struct {
	ID         string               `json:"id"`
	ZoneGroups map[string]ZoneGroup `json:"-"`
	// MasterZoneGroup is encoded but not dumped; decoding sets it from the
	// zonegroup marked master, when there is one.
	MasterZoneGroup string            `json:"-"`
	ShortZoneIDs    map[string]uint32 `json:"-"`
}

// MarshalJSON renders RGWPeriodMap::dump: the zonegroups as encode_json_map
// writes them and short_zone_ids in std::map form.
func (m PeriodMap) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID           string                     `json:"id"`
		ZoneGroups   []ZoneGroup                `json:"zonegroups"`
		ShortZoneIDs []mapEntry[string, uint32] `json:"short_zone_ids"`
	}{m.ID, mapValues(m.ZoneGroups), mapEntries(m.ShortZoneIDs)})
}

// Encode mirrors RGWPeriodMap::encode, ENCODE_START(2, 1).
func (m PeriodMap) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(m.ID)
	denc.EncodeMap(e, m.ZoneGroups, (*denc.Encoder).String, func(e *denc.Encoder, g ZoneGroup) { g.Encode(e, r) })
	e.String(m.MasterZoneGroup)
	denc.EncodeMap(e, m.ShortZoneIDs, (*denc.Encoder).String, (*denc.Encoder).U32)
	e.EndStruct(f)
}

// DecodePeriodMap mirrors RGWPeriodMap::decode, DECODE_START(2), including
// its pass that makes the last zonegroup marked master, in key order, the
// master zonegroup.
func DecodePeriodMap(d *denc.Decoder) PeriodMap {
	h := d.BeginStruct(2)
	var m PeriodMap
	m.ID = d.String()
	m.ZoneGroups = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeZoneGroup)
	m.MasterZoneGroup = d.String()
	if h.Version >= 2 {
		m.ShortZoneIDs = denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).U32)
	}
	d.EndStruct(h)
	for _, k := range slices.Sorted(maps.Keys(m.ZoneGroups)) {
		if g := m.ZoneGroups[k]; g.IsMaster {
			m.MasterZoneGroup = g.ID
		}
	}
	return m
}

// PeriodConfig is RGWPeriodConfig: the realm-wide default quotas and rate
// limits.
type PeriodConfig struct {
	BucketQuota     Quota         `json:"bucket_quota"`
	UserQuota       Quota         `json:"user_quota"`
	UserRateLimit   RateLimitInfo `json:"user_ratelimit"`
	BucketRateLimit RateLimitInfo `json:"bucket_ratelimit"`
	AnonRateLimit   RateLimitInfo `json:"anonymous_ratelimit"`
}

// Encode mirrors RGWPeriodConfig::encode, ENCODE_START(2, 1).
func (c PeriodConfig) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	c.BucketQuota.Encode(e, r)
	c.UserQuota.Encode(e, r)
	c.BucketRateLimit.Encode(e, r)
	c.UserRateLimit.Encode(e, r)
	c.AnonRateLimit.Encode(e, r)
	e.EndStruct(f)
}

// DecodePeriodConfig mirrors RGWPeriodConfig::decode, DECODE_START(2);
// version 1 had no rate limits.
func DecodePeriodConfig(d *denc.Decoder) PeriodConfig {
	h := d.BeginStruct(2)
	var c PeriodConfig
	c.BucketQuota = DecodeQuota(d)
	c.UserQuota = DecodeQuota(d)
	if h.Version >= 2 {
		c.BucketRateLimit = DecodeRateLimitInfo(d)
		c.UserRateLimit = DecodeRateLimitInfo(d)
		c.AnonRateLimit = DecodeRateLimitInfo(d)
	}
	d.EndStruct(h)
	return c
}

// RateLimitInfo is RGWRateLimitInfo. Zero means unlimited. MaxListOps and
// MaxDeleteOps are main's and in JSON only when set.
type RateLimitInfo struct {
	MaxReadOps    int64 `json:"max_read_ops"`
	MaxWriteOps   int64 `json:"max_write_ops"`
	MaxListOps    int64 `json:"max_list_ops,omitempty"`
	MaxDeleteOps  int64 `json:"max_delete_ops,omitempty"`
	MaxReadBytes  int64 `json:"max_read_bytes"`
	MaxWriteBytes int64 `json:"max_write_bytes"`
	Enabled       bool  `json:"enabled"`
}

// Encode mirrors RGWRateLimitInfo::encode at version 1, ENCODE_START(1, 1),
// which Squid and Tentacle both write; main's version 2 list and delete
// limits are not written.
func (l RateLimitInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.I64(l.MaxWriteOps)
	e.I64(l.MaxReadOps)
	e.I64(l.MaxWriteBytes)
	e.I64(l.MaxReadBytes)
	e.Bool(l.Enabled)
	e.EndStruct(f)
}

// DecodeRateLimitInfo mirrors RGWRateLimitInfo::decode in main,
// DECODE_START(2), whose version 2 put the list and delete limits after the
// read limit.
func DecodeRateLimitInfo(d *denc.Decoder) RateLimitInfo {
	h := d.BeginStruct(2)
	var l RateLimitInfo
	l.MaxWriteOps = d.I64()
	l.MaxReadOps = d.I64()
	if h.Version >= 2 {
		l.MaxListOps = d.I64()
		l.MaxDeleteOps = d.I64()
	}
	l.MaxWriteBytes = d.I64()
	l.MaxReadBytes = d.I64()
	l.Enabled = d.Bool()
	d.EndStruct(h)
	return l
}

// PeriodLatestEpochInfo is RGWPeriodLatestEpochInfo, the
// periods.<id>.latest_epoch object.
type PeriodLatestEpochInfo struct {
	Epoch uint32 `json:"latest_epoch"`
}

// Encode mirrors RGWPeriodLatestEpochInfo::encode, ENCODE_START(1, 1).
func (i PeriodLatestEpochInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U32(i.Epoch)
	e.EndStruct(f)
}

// DecodePeriodLatestEpochInfo mirrors RGWPeriodLatestEpochInfo::decode.
func DecodePeriodLatestEpochInfo(d *denc.Decoder) PeriodLatestEpochInfo {
	h := d.BeginStruct(1)
	i := PeriodLatestEpochInfo{Epoch: d.U32()}
	d.EndStruct(h)
	return i
}
