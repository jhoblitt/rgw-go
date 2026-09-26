package meta

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ZoneGroup is RGWZoneGroup, the zonegroup_info.<id> object and an entry of
// a period map.
type ZoneGroup struct {
	ID                 string                              `json:"id"`
	Name               string                              `json:"name"`
	APIName            string                              `json:"api_name"`
	IsMaster           bool                                `json:"is_master"`
	Endpoints          []string                            `json:"endpoints"`
	Hostnames          []string                            `json:"hostnames"`
	HostnamesS3Website []string                            `json:"hostnames_s3website"`
	MasterZone         string                              `json:"master_zone"`
	Zones              map[string]Zone                     `json:"-"`
	PlacementTargets   map[string]ZoneGroupPlacementTarget `json:"-"`
	DefaultPlacement   PlacementRule                       `json:"default_placement"`
	RealmID            string                              `json:"realm_id"`
	SyncPolicy         SyncPolicy                          `json:"sync_policy"`
	EnabledFeatures    []string                            `json:"enabled_features"`
}

// MarshalJSON renders RGWZoneGroup::dump: zones and placement targets as
// encode_json_map writes them, their values alone in key order.
func (g ZoneGroup) MarshalJSON() ([]byte, error) {
	type plain ZoneGroup
	p := plain(g)
	p.Endpoints = orEmpty(p.Endpoints)
	p.Hostnames = orEmpty(p.Hostnames)
	p.HostnamesS3Website = orEmpty(p.HostnamesS3Website)
	p.EnabledFeatures = orEmpty(p.EnabledFeatures)
	return json.Marshal(struct {
		plain
		Zones            []Zone                     `json:"zones"`
		PlacementTargets []ZoneGroupPlacementTarget `json:"placement_targets"`
	}{p, mapValues(g.Zones), mapValues(g.PlacementTargets)})
}

// Encode mirrors RGWZoneGroup::encode, ENCODE_START(6, 1).
func (g ZoneGroup) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(6, 1)
	e.String(g.Name)
	e.String(g.APIName)
	e.Bool(g.IsMaster)
	encodeStrings(e, g.Endpoints)
	e.String(g.MasterZone)
	denc.EncodeMap(e, g.Zones, (*denc.Encoder).String, func(e *denc.Encoder, z Zone) { z.Encode(e, r) })
	denc.EncodeMap(e, g.PlacementTargets, (*denc.Encoder).String,
		func(e *denc.Encoder, t ZoneGroupPlacementTarget) { t.Encode(e, r) })
	g.DefaultPlacement.Encode(e, r)
	encodeStrings(e, g.Hostnames)
	encodeStrings(e, g.HostnamesS3Website)
	encodeSysObj(e, g.ID, g.Name)
	e.String(g.RealmID)
	g.SyncPolicy.Encode(e, r)
	encodeStringSet(e, g.EnabledFeatures)
	e.EndStruct(f)
}

// DecodeZoneGroup mirrors RGWZoneGroup::decode, DECODE_START(6). Below
// version 4 the id is the name.
func DecodeZoneGroup(d *denc.Decoder) ZoneGroup {
	h := d.BeginStruct(6)
	v := h.Version
	var g ZoneGroup
	g.Name = d.String()
	g.APIName = d.String()
	g.IsMaster = d.Bool()
	g.Endpoints = decodeStrings(d)
	g.MasterZone = d.String()
	g.Zones = denc.DecodeMap(d, (*denc.Decoder).String, DecodeZone)
	g.PlacementTargets = denc.DecodeMap(d, (*denc.Decoder).String, DecodeZoneGroupPlacementTarget)
	g.DefaultPlacement = DecodePlacementRule(d)
	if v >= 2 {
		g.Hostnames = decodeStrings(d)
	}
	if v >= 3 {
		g.HostnamesS3Website = decodeStrings(d)
	}
	if v >= 4 {
		g.ID, g.Name = decodeSysObj(d)
		g.RealmID = d.String()
	} else {
		g.ID = g.Name
	}
	if v >= 5 {
		g.SyncPolicy = DecodeSyncPolicy(d)
	}
	if v >= 6 {
		g.EnabledFeatures = decodeStringSet(d)
	}
	d.EndStruct(h)
	return g
}

// defaultBucketIndexMaxShards is RGWZone::default_bucket_index_max_shards.
const defaultBucketIndexMaxShards = 11

// Zone is RGWZone, a zone's entry in its zonegroup. The zero value lacks the
// C++ defaults of 11 index shards and syncing from all zones; NewZone has
// them.
type Zone struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	Endpoints            []string `json:"endpoints"`
	LogMeta              bool     `json:"log_meta"`
	LogData              bool     `json:"log_data"`
	BucketIndexMaxShards uint32   `json:"bucket_index_max_shards"`
	ReadOnly             bool     `json:"read_only"`
	TierType             string   `json:"tier_type"`
	SyncFromAll          bool     `json:"sync_from_all"`
	SyncFrom             []string `json:"sync_from"`
	RedirectZone         string   `json:"redirect_zone"`
	SupportedFeatures    []string `json:"supported_features"`
}

// NewZone returns what RGWZone's constructor builds.
func NewZone() Zone {
	return Zone{BucketIndexMaxShards: defaultBucketIndexMaxShards, SyncFromAll: true}
}

// MarshalJSON renders RGWZone::dump as v20.2.4 writes it, log_meta included,
// which main no longer prints.
func (z Zone) MarshalJSON() ([]byte, error) {
	type plain Zone
	p := plain(z)
	p.Endpoints = orEmpty(p.Endpoints)
	p.SyncFrom = orEmpty(p.SyncFrom)
	p.SupportedFeatures = orEmpty(p.SupportedFeatures)
	return json.Marshal(p)
}

// Encode mirrors RGWZone::encode, ENCODE_START(8, 1).
func (z Zone) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(8, 1)
	e.String(z.Name)
	encodeStrings(e, z.Endpoints)
	e.Bool(z.LogMeta)
	e.Bool(z.LogData)
	e.U32(z.BucketIndexMaxShards)
	e.String(z.ID)
	e.Bool(z.ReadOnly)
	e.String(z.TierType)
	e.Bool(z.SyncFromAll)
	encodeStringSet(e, z.SyncFrom)
	e.String(z.RedirectZone)
	encodeStringSet(e, z.SupportedFeatures)
	e.EndStruct(f)
}

// DecodeZone mirrors RGWZone::decode, DECODE_START(8). Below version 4 the
// id is the name; fields a version predates keep RGWZone's defaults.
func DecodeZone(d *denc.Decoder) Zone {
	h := d.BeginStruct(8)
	v := h.Version
	z := NewZone()
	z.Name = d.String()
	if v < 4 {
		z.ID = z.Name
	}
	z.Endpoints = decodeStrings(d)
	if v >= 2 {
		z.LogMeta = d.Bool()
		z.LogData = d.Bool()
	}
	if v >= 3 {
		z.BucketIndexMaxShards = d.U32()
	}
	if v >= 4 {
		z.ID = d.String()
		z.ReadOnly = d.Bool()
	}
	if v >= 5 {
		z.TierType = d.String()
	}
	if v >= 6 {
		z.SyncFromAll = d.Bool()
		z.SyncFrom = decodeStringSet(d)
	}
	if v >= 7 {
		z.RedirectZone = d.String()
	}
	if v >= 8 {
		z.SupportedFeatures = decodeStringSet(d)
	}
	d.EndStruct(h)
	return z
}

// ZoneGroupPlacementTarget is RGWZoneGroupPlacementTarget, a zonegroup's
// placement target.
type ZoneGroupPlacementTarget struct {
	Name           string                            `json:"name"`
	Tags           []string                          `json:"tags"`
	StorageClasses []string                          `json:"storage_classes"`
	TierTargets    map[string]ZoneGroupPlacementTier `json:"-"`
}

// MarshalJSON renders RGWZoneGroupPlacementTarget::dump, which writes
// tier_targets only when there are any.
func (t ZoneGroupPlacementTarget) MarshalJSON() ([]byte, error) {
	type plain ZoneGroupPlacementTarget
	p := plain(t)
	p.Tags = orEmpty(p.Tags)
	p.StorageClasses = orEmpty(p.StorageClasses)
	return json.Marshal(struct {
		plain
		TierTargets []mapEntry[string, ZoneGroupPlacementTier] `json:"tier_targets,omitempty"`
	}{p, mapEntries(t.TierTargets)})
}

// Encode mirrors RGWZoneGroupPlacementTarget::encode, ENCODE_START(3, 1).
func (t ZoneGroupPlacementTarget) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(3, 1)
	e.String(t.Name)
	encodeStringSet(e, t.Tags)
	encodeStringSet(e, t.StorageClasses)
	denc.EncodeMap(e, t.TierTargets, (*denc.Encoder).String,
		func(e *denc.Encoder, x ZoneGroupPlacementTier) { x.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeZoneGroupPlacementTarget mirrors RGWZoneGroupPlacementTarget::decode,
// DECODE_START(3), which fills an empty storage class set with STANDARD.
func DecodeZoneGroupPlacementTarget(d *denc.Decoder) ZoneGroupPlacementTarget {
	h := d.BeginStruct(3)
	var t ZoneGroupPlacementTarget
	t.Name = d.String()
	t.Tags = decodeStringSet(d)
	if h.Version >= 2 {
		t.StorageClasses = decodeStringSet(d)
	}
	if len(t.StorageClasses) == 0 {
		t.StorageClasses = []string{StorageClassStandard}
	}
	if h.Version >= 3 {
		t.TierTargets = denc.DecodeMap(d, (*denc.Decoder).String, DecodeZoneGroupPlacementTier)
	}
	d.EndStruct(h)
	return t
}

// The cloud tier types, RGWTierType.
const (
	TierTypeCloudS3        = "cloud-s3"
	TierTypeCloudS3Glacier = "cloud-s3-glacier"
)

// ZoneGroupPlacementTier is RGWZoneGroupPlacementTier, a placement target's
// cloud tier. The zero value lacks the C++ member defaults;
// NewZoneGroupPlacementTier has them.
type ZoneGroupPlacementTier struct {
	TierType         string `json:"tier_type"`
	StorageClass     string `json:"storage_class"`
	RetainHeadObject bool   `json:"retain_head_object"`
	// RetainCurrentVersion is main's, in JSON only when set.
	RetainCurrentVersion   bool                     `json:"retain_current_version,omitempty"`
	S3                     ZoneGroupPlacementTierS3 `json:"-"`
	AllowReadThrough       bool                     `json:"allow_read_through"`
	ReadThroughRestoreDays uint64                   `json:"read_through_restore_days"`
	RestoreStorageClass    string                   `json:"restore_storage_class"`
	S3Glacier              ZoneGroupTierS3Glacier   `json:"-"`
}

// NewZoneGroupPlacementTier returns a tier with RGWZoneGroupPlacementTier's
// defaults.
func NewZoneGroupPlacementTier() ZoneGroupPlacementTier {
	return ZoneGroupPlacementTier{
		S3:                     NewZoneGroupPlacementTierS3(),
		ReadThroughRestoreDays: 1,
		RestoreStorageClass:    StorageClassStandard,
		S3Glacier:              NewZoneGroupTierS3Glacier(),
	}
}

func (t ZoneGroupPlacementTier) isS3() bool {
	return t.TierType == TierTypeCloudS3 || t.TierType == TierTypeCloudS3Glacier
}

func (t ZoneGroupPlacementTier) isGlacier() bool { return t.TierType == TierTypeCloudS3Glacier }

// MarshalJSON renders RGWZoneGroupPlacementTier::dump as v20.2.4 writes it:
// s3 for either cloud tier type and s3-glacier for the glacier type.
func (t ZoneGroupPlacementTier) MarshalJSON() ([]byte, error) {
	type plain ZoneGroupPlacementTier
	out := struct {
		plain
		S3        *ZoneGroupPlacementTierS3 `json:"s3,omitempty"`
		S3Glacier *ZoneGroupTierS3Glacier   `json:"s3-glacier,omitempty"`
	}{plain: plain(t)}
	if t.isS3() {
		out.S3 = &t.S3
	}
	if t.isGlacier() {
		out.S3Glacier = &t.S3Glacier
	}
	return json.Marshal(out)
}

// Encode mirrors RGWZoneGroupPlacementTier::encode: ENCODE_START(1, 1) for
// Squid, which writes the S3 config only for cloud-s3, and ENCODE_START(4, 1)
// for Tentacle, which writes it for either cloud type and adds the
// read-through fields and the glacier config.
func (t ZoneGroupPlacementTier) Encode(e *denc.Encoder, r denc.Release) {
	if r < denc.Tentacle {
		f := e.BeginStruct(1, 1)
		e.String(t.TierType)
		e.String(t.StorageClass)
		e.Bool(t.RetainHeadObject)
		if t.TierType == TierTypeCloudS3 {
			t.S3.Encode(e, r)
		}
		e.EndStruct(f)
		return
	}
	f := e.BeginStruct(4, 1)
	e.String(t.TierType)
	e.String(t.StorageClass)
	e.Bool(t.RetainHeadObject)
	if t.isS3() {
		t.S3.Encode(e, r)
	}
	e.Bool(t.AllowReadThrough)
	e.U64(t.ReadThroughRestoreDays)
	e.String(t.RestoreStorageClass)
	if t.isGlacier() {
		t.S3Glacier.Encode(e, r)
	}
	e.EndStruct(f)
}

// DecodeZoneGroupPlacementTier mirrors RGWZoneGroupPlacementTier::decode in
// main, DECODE_START(5). Versions 1 and 2 hold the S3 config only for
// cloud-s3, and version 2 wrote the read-through fields ahead of it.
func DecodeZoneGroupPlacementTier(d *denc.Decoder) ZoneGroupPlacementTier {
	h := d.BeginStruct(5)
	v := h.Version
	t := NewZoneGroupPlacementTier()
	t.TierType = d.String()
	t.StorageClass = d.String()
	t.RetainHeadObject = d.Bool()
	switch v {
	case 0:
		// The C++ branches start at version 1, so version 0 reads none of these.
	case 1:
		if t.TierType == TierTypeCloudS3 {
			t.S3 = DecodeZoneGroupPlacementTierS3(d)
		}
	case 2:
		t.AllowReadThrough = d.Bool()
		t.ReadThroughRestoreDays = d.U64()
		if t.TierType == TierTypeCloudS3 {
			t.S3 = DecodeZoneGroupPlacementTierS3(d)
		}
	default:
		if t.isS3() {
			t.S3 = DecodeZoneGroupPlacementTierS3(d)
		}
		t.AllowReadThrough = d.Bool()
		t.ReadThroughRestoreDays = d.U64()
	}
	if v >= 4 {
		t.RestoreStorageClass = d.String()
		if t.isGlacier() {
			t.S3Glacier = DecodeZoneGroupTierS3Glacier(d)
		}
	}
	if v >= 5 {
		t.RetainCurrentVersion = d.Bool()
	}
	d.EndStruct(h)
	return t
}

// defaultMultipartSyncPartSize is DEFAULT_MULTIPART_SYNC_PART_SIZE.
const defaultMultipartSyncPartSize = 32 << 20

// ZoneGroupPlacementTierS3 is RGWZoneGroupPlacementTierS3, a cloud tier's S3
// endpoint. The zero value lacks the C++ member defaults;
// NewZoneGroupPlacementTierS3 has them. LocationConstraint, TargetByBucket
// and TargetByBucketPrefix are main's and in JSON only when set.
type ZoneGroupPlacementTierS3 struct {
	Endpoint string
	Key      AccessKey
	Region   string
	// HostStyle is the HostStyle enum as encoded: 0 is path style, any other
	// value virtual-host style.
	HostStyle              uint32
	TargetStorageClass     string
	LocationConstraint     string
	TargetPath             string
	TargetByBucket         bool
	TargetByBucketPrefix   string
	ACLMappings            map[string]TierACLMapping
	MultipartSyncThreshold uint64
	MultipartMinPartSize   uint64
}

// NewZoneGroupPlacementTierS3 returns what RGWZoneGroupPlacementTierS3's
// member initializers give: an active empty key, path style, and 32 MiB
// multipart threshold and part size.
func NewZoneGroupPlacementTierS3() ZoneGroupPlacementTierS3 {
	return ZoneGroupPlacementTierS3{
		Key:                    NewAccessKey(),
		MultipartSyncThreshold: defaultMultipartSyncPartSize,
		MultipartMinPartSize:   defaultMultipartSyncPartSize,
	}
}

// MarshalJSON renders RGWZoneGroupPlacementTierS3::dump as v20.2.4 writes
// it, with main's fields added when set.
func (s ZoneGroupPlacementTierS3) MarshalJSON() ([]byte, error) {
	style := "path"
	if s.HostStyle != 0 {
		style = "virtual"
	}
	return json.Marshal(struct {
		Endpoint               string                             `json:"endpoint"`
		AccessKey              string                             `json:"access_key"`
		Secret                 string                             `json:"secret"`
		Region                 string                             `json:"region"`
		HostStyle              string                             `json:"host_style"`
		LocationConstraint     string                             `json:"location_constraint,omitempty"`
		TargetStorageClass     string                             `json:"target_storage_class"`
		TargetPath             string                             `json:"target_path"`
		TargetByBucket         bool                               `json:"target_by_bucket,omitempty"`
		TargetByBucketPrefix   string                             `json:"target_by_bucket_prefix,omitempty"`
		ACLMappings            []mapEntry[string, TierACLMapping] `json:"acl_mappings"`
		MultipartSyncThreshold uint64                             `json:"multipart_sync_threshold"`
		MultipartMinPartSize   uint64                             `json:"multipart_min_part_size"`
	}{
		s.Endpoint, s.Key.ID, s.Key.Secret, s.Region, style, s.LocationConstraint,
		s.TargetStorageClass, s.TargetPath, s.TargetByBucket, s.TargetByBucketPrefix,
		mapEntries(s.ACLMappings), s.MultipartSyncThreshold, s.MultipartMinPartSize,
	})
}

// Encode mirrors RGWZoneGroupPlacementTierS3::encode at version 1,
// ENCODE_START(1, 1), which Squid and Tentacle both write; main's version 3
// fields are not written.
func (s ZoneGroupPlacementTierS3) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(s.Endpoint)
	s.Key.Encode(e, r)
	e.String(s.Region)
	e.U32(s.HostStyle)
	e.String(s.TargetStorageClass)
	e.String(s.TargetPath)
	denc.EncodeMap(e, s.ACLMappings, (*denc.Encoder).String, func(e *denc.Encoder, m TierACLMapping) { m.Encode(e, r) })
	e.U64(s.MultipartSyncThreshold)
	e.U64(s.MultipartMinPartSize)
	e.EndStruct(f)
}

// DecodeZoneGroupPlacementTierS3 mirrors RGWZoneGroupPlacementTierS3::decode
// in main, DECODE_START(3).
func DecodeZoneGroupPlacementTierS3(d *denc.Decoder) ZoneGroupPlacementTierS3 {
	h := d.BeginStruct(3)
	s := NewZoneGroupPlacementTierS3()
	s.Endpoint = d.String()
	s.Key = DecodeAccessKey(d)
	s.Region = d.String()
	s.HostStyle = d.U32()
	s.TargetStorageClass = d.String()
	s.TargetPath = d.String()
	s.ACLMappings = denc.DecodeMap(d, (*denc.Decoder).String, DecodeTierACLMapping)
	s.MultipartSyncThreshold = d.U64()
	s.MultipartMinPartSize = d.U64()
	if h.Version >= 2 {
		s.LocationConstraint = d.String()
	}
	if h.Version >= 3 {
		s.TargetByBucket = d.Bool()
		s.TargetByBucketPrefix = d.String()
	}
	d.EndStruct(h)
	return s
}

// TierACLMapping is RGWTierACLMapping. Type is ACLGranteeTypeEnum as encoded.
type TierACLMapping struct {
	Type     uint32
	SourceID string
	DestID   string
}

// The ACLGranteeTypeEnum values RGWTierACLMapping::dump names.
const (
	aclTypeEmailUser = 1
	aclTypeGroup     = 2
)

// MarshalJSON renders RGWTierACLMapping::dump.
func (m TierACLMapping) MarshalJSON() ([]byte, error) {
	typ := "id"
	switch m.Type {
	case aclTypeEmailUser:
		typ = "email"
	case aclTypeGroup:
		typ = "uri"
	}
	return json.Marshal(struct {
		Type     string `json:"type"`
		SourceID string `json:"source_id"`
		DestID   string `json:"dest_id"`
	}{typ, m.SourceID, m.DestID})
}

// Encode mirrors RGWTierACLMapping::encode, ENCODE_START(1, 1).
func (m TierACLMapping) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U32(m.Type)
	e.String(m.SourceID)
	e.String(m.DestID)
	e.EndStruct(f)
}

// DecodeTierACLMapping mirrors RGWTierACLMapping::decode.
func DecodeTierACLMapping(d *denc.Decoder) TierACLMapping {
	h := d.BeginStruct(1)
	var m TierACLMapping
	m.Type = d.U32()
	m.SourceID = d.String()
	m.DestID = d.String()
	d.EndStruct(h)
	return m
}

// ZoneGroupTierS3Glacier is RGWZoneGroupTierS3Glacier. RestoreTierType is
// GlacierRestoreTierType: 0 Standard, 1 Expedited, and in main 2 NoTier.
// The zero value has no restore days; NewZoneGroupTierS3Glacier gives the
// C++ default of one.
type ZoneGroupTierS3Glacier struct {
	RestoreDays     uint64
	RestoreTierType uint8
}

// NewZoneGroupTierS3Glacier returns what RGWZoneGroupTierS3Glacier's member
// initializers give: one restore day at the Standard tier.
func NewZoneGroupTierS3Glacier() ZoneGroupTierS3Glacier {
	return ZoneGroupTierS3Glacier{RestoreDays: 1}
}

// MarshalJSON renders RGWZoneGroupTierS3Glacier::dump as v20.2.4 writes it:
// Standard for 0 and Expedited for any other tier type, main's NoTier
// included.
func (g ZoneGroupTierS3Glacier) MarshalJSON() ([]byte, error) {
	tier := "Standard"
	if g.RestoreTierType != 0 {
		tier = "Expedited"
	}
	return json.Marshal(struct {
		RestoreDays     uint64 `json:"glacier_restore_days"`
		RestoreTierType string `json:"glacier_restore_tier_type"`
	}{g.RestoreDays, tier})
}

// Encode mirrors RGWZoneGroupTierS3Glacier::encode, ENCODE_START(1, 1).
func (g ZoneGroupTierS3Glacier) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U64(g.RestoreDays)
	e.U8(g.RestoreTierType)
	e.EndStruct(f)
}

// DecodeZoneGroupTierS3Glacier mirrors RGWZoneGroupTierS3Glacier::decode.
func DecodeZoneGroupTierS3Glacier(d *denc.Decoder) ZoneGroupTierS3Glacier {
	h := d.BeginStruct(1)
	var g ZoneGroupTierS3Glacier
	g.RestoreDays = d.U64()
	g.RestoreTierType = d.U8()
	d.EndStruct(h)
	return g
}

// SyncPolicy is a zonegroup's rgw_sync_policy_info, kept as its encoded
// bytes: multisite sync is excluded, so its groups are never interpreted,
// and holding the bytes keeps any policy radosgw wrote byte-exact. The zero
// value is the empty policy.
type SyncPolicy struct {
	// Raw is the whole encoding, header included, of a policy that is not
	// the empty one at version 1; nil otherwise.
	Raw []byte
}

// emptySyncPolicy is rgw_sync_policy_info::encode of no groups: version 1,
// compat 1, a four-byte payload holding a zero map count.
var emptySyncPolicy = []byte{1, 1, 4, 0, 0, 0, 0, 0, 0, 0}

// errSyncPolicyGroups reports a policy whose groups this package does not render.
var errSyncPolicyGroups = errors.New("meta: rendering zonegroup sync policy groups is not supported (multisite is excluded)")

// Empty reports whether the policy has no groups.
func (p SyncPolicy) Empty() bool {
	if p.Raw == nil {
		return true
	}
	return len(p.Raw) >= 10 && binary.LittleEndian.Uint32(p.Raw[6:10]) == 0
}

// MarshalJSON renders rgw_sync_policy_info::dump for a policy without
// groups, and fails for one with groups.
func (p SyncPolicy) MarshalJSON() ([]byte, error) {
	if !p.Empty() {
		return nil, errSyncPolicyGroups
	}
	return []byte(`{"groups":[]}`), nil
}

// Encode writes the policy's bytes unchanged, as rgw_sync_policy_info::encode
// at version 1 writes a policy decoded from them.
func (p SyncPolicy) Encode(e *denc.Encoder, _ denc.Release) {
	if p.Raw == nil {
		e.Raw(emptySyncPolicy)
		return
	}
	e.Raw(p.Raw)
}

// DecodeSyncPolicy reads an rgw_sync_policy_info's header and keeps its
// bytes, checking what DECODE_START(1) checks and that the payload opens
// with the groups count.
func DecodeSyncPolicy(d *denc.Decoder) SyncPolicy {
	version := d.U8()
	compat := d.U8()
	n := d.U32()
	if d.Err() != nil {
		return SyncPolicy{}
	}
	if compat > 1 {
		d.Fail(fmt.Errorf("%w: rgw_sync_policy_info struct_v %d compat %d, decoder 1", denc.ErrIncompatible, version, compat))
		return SyncPolicy{}
	}
	body := d.Raw(int(n))
	if d.Err() != nil {
		return SyncPolicy{}
	}
	if len(body) < 4 {
		d.Fail(fmt.Errorf("%w: rgw_sync_policy_info payload of %d bytes", denc.ErrShortBuffer, len(body)))
		return SyncPolicy{}
	}
	raw := slices.Concat([]byte{version, compat}, binary.LittleEndian.AppendUint32(nil, n), body)
	if slices.Equal(raw, emptySyncPolicy) {
		return SyncPolicy{}
	}
	return SyncPolicy{Raw: raw}
}
