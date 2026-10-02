package meta

import (
	"encoding/json"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Manifest is RGWObjManifest, the user.rgw.manifest attr of an object's head:
// how the object's data is laid out across its head and tail RADOS objects.
// A manifest is either explicit, listing every piece in Objs, or rule-based,
// deriving tail names from Prefix and Rules. The zero value lacks the C++
// member defaults; NewManifest has them.
type Manifest struct { //nolint:recvcheck // SetObjSize fills the manifest in place as generator::create_next does, while it encodes and reads by value as every stored type does
	// ExplicitObjs marks the explicit form, which only very old releases wrote.
	ExplicitObjs bool
	// Objs maps an offset in the object to the piece holding the data there.
	Objs    map[uint64]ManifestPart
	ObjSize uint64
	// Obj is the head object.
	Obj               Obj
	HeadSize          uint64
	HeadPlacementRule PlacementRule
	MaxHeadSize       uint64
	// Prefix starts every tail object's name.
	Prefix string
	// TailPlacement is where the tail objects live, which differs from the
	// head's bucket when the object was copied across pools.
	TailPlacement BucketPlacement
	// Rules maps the offset each rule starts at to the rule.
	Rules map[uint64]ManifestRule
	// TailInstance is the version instance of the tail objects' names.
	TailInstance string
	TierType     string
	TierConfig   ObjTier
}

// NewManifest returns what RGWObjManifest's constructor leaves: an empty
// manifest whose tier config has RGWObjTier's defaults.
func NewManifest() Manifest { return Manifest{TierConfig: NewObjTier()} }

// BucketPlacement is rgw_bucket_placement: a bucket and its placement rule.
type BucketPlacement struct {
	Bucket        BucketID      `json:"bucket"`
	PlacementRule PlacementRule `json:"placement_rule"`
}

// isSupportedTierType is RGWTierType::is_tier_type_supported.
func isSupportedTierType(t string) bool {
	return t == TierTypeCloudS3 || t == TierTypeCloudS3Glacier
}

// MarshalJSON renders RGWObjManifest::dump as v20.2.4 writes it, the form
// radosgw-admin object stat prints: objs alternates each offset with its part,
// as a formatter drops names inside an array, tier_config appears for either
// cloud tier type, and begin_iter and end_iter are the states of the
// iterators obj_begin and obj_end return.
func (m Manifest) MarshalJSON() ([]byte, error) {
	objs := make([]any, 0, 2*len(m.Objs))
	for _, k := range sortedKeys(m.Objs) {
		objs = append(objs, k, m.Objs[k])
	}
	out := struct {
		Objs          []any                            `json:"objs"`
		ObjSize       uint64                           `json:"obj_size"`
		ExplicitObjs  bool                             `json:"explicit_objs"`
		HeadSize      uint64                           `json:"head_size"`
		MaxHeadSize   uint64                           `json:"max_head_size"`
		Prefix        string                           `json:"prefix"`
		Rules         []mapEntry[uint64, ManifestRule] `json:"rules"`
		TailInstance  string                           `json:"tail_instance"`
		TailPlacement BucketPlacement                  `json:"tail_placement"`
		TierType      string                           `json:"tier_type"`
		TierConfig    *ObjTier                         `json:"tier_config,omitempty"`
		BeginIter     iterDump                         `json:"begin_iter"`
		EndIter       iterDump                         `json:"end_iter"`
	}{
		Objs:          objs,
		ObjSize:       m.ObjSize,
		ExplicitObjs:  m.ExplicitObjs,
		HeadSize:      m.HeadSize,
		MaxHeadSize:   m.MaxHeadSize,
		Prefix:        m.Prefix,
		Rules:         mapEntries(m.Rules),
		TailInstance:  m.TailInstance,
		TailPlacement: m.TailPlacement,
		TierType:      m.TierType,
	}
	if isSupportedTierType(m.TierType) {
		out.TierConfig = &m.TierConfig
	}
	begin, err := newManifestIter(&m, 0)
	if err != nil {
		return nil, err
	}
	end, err := newManifestIter(&m, m.ObjSize)
	if err != nil {
		return nil, err
	}
	out.BeginIter, out.EndIter = begin.dump(), end.dump()
	return json.Marshal(out)
}

// Encode mirrors RGWObjManifest::encode, ENCODE_START(8, 6). The tail bucket
// is written only when it differs from the head's bucket, as rgw_bucket's ==
// compares them, and the tail instance only when it differs from the head's
// instance. The tier config's placement tier is written at r's version.
func (m Manifest) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(8, 6)
	e.U64(m.ObjSize)
	denc.EncodeMap(e, m.Objs, (*denc.Encoder).U64, func(e *denc.Encoder, p ManifestPart) { p.Encode(e, r) })
	e.Bool(m.ExplicitObjs)
	m.Obj.Encode(e, r)
	e.U64(m.HeadSize)
	e.U64(m.MaxHeadSize)
	e.String(m.Prefix)
	denc.EncodeMap(e, m.Rules, (*denc.Encoder).U64, func(e *denc.Encoder, x ManifestRule) { x.Encode(e, r) })
	tailBucket := !sameBucket(m.TailPlacement.Bucket, m.Obj.Bucket)
	e.Bool(tailBucket)
	if tailBucket {
		m.TailPlacement.Bucket.Encode(e, r)
	}
	tailInstance := m.TailInstance != m.Obj.Key.Instance
	e.Bool(tailInstance)
	if tailInstance {
		e.String(m.TailInstance)
	}
	m.HeadPlacementRule.Encode(e, r)
	m.TailPlacement.PlacementRule.Encode(e, r)
	e.String(m.TierType)
	m.TierConfig.Encode(e, r)
	e.EndStruct(f)
}

// DecodeManifest mirrors RGWObjManifest::decode in main,
// DECODE_START_LEGACY_COMPAT_LEN_32(8, 2, 2). Below version 3 the manifest is
// explicit and its first piece is the head. From version 4 the tail bucket is
// stored, and from version 6 only when it differs from the head's; the tail
// instance likewise from versions 5 and 6. The placement rules arrive in
// version 7 and the tier in version 8.
func DecodeManifest(d *denc.Decoder) Manifest {
	h := d.BeginStructLegacy(8, 2, 2, 3)
	v := h.Version
	m := NewManifest()
	m.ObjSize = d.U64()
	m.Objs = denc.DecodeMap(d, (*denc.Decoder).U64, DecodeManifestPart)
	if v >= 3 {
		m.ExplicitObjs = d.Bool()
		m.Obj = DecodeObj(d)
		m.HeadSize = d.U64()
		m.MaxHeadSize = d.U64()
		m.Prefix = d.String()
		m.Rules = denc.DecodeMap(d, (*denc.Decoder).U64, DecodeManifestRule)
	} else {
		m.ExplicitObjs = true
		if len(m.Objs) > 0 {
			first := m.Objs[sortedKeys(m.Objs)[0]]
			m.Obj = first.Loc
			m.HeadSize = first.Size
			m.MaxHeadSize = m.HeadSize
		}
	}
	if m.ExplicitObjs && m.HeadSize > 0 && len(m.Objs) > 0 {
		// Ceph issue 16435: a copy of an old explicit manifest may name another
		// object at offset 0, so the head replaces it. objs[0] inserts an empty
		// part when there is none, which the C++ then keeps.
		p := m.Objs[0]
		if p.Loc.Key.OID() != "" && p.Loc.Key.NS == "" {
			p.Loc = m.Obj
			p.Size = m.HeadSize
		}
		m.Objs[0] = p
	}
	if v >= 4 {
		if v < 6 || d.Bool() {
			m.TailPlacement.Bucket = DecodeBucketID(d)
		} else {
			m.TailPlacement.Bucket = m.Obj.Bucket
		}
	}
	m.TailInstance = m.Obj.Key.Instance
	if v >= 5 && (v < 6 || d.Bool()) {
		m.TailInstance = d.String()
	}
	if v >= 7 {
		m.HeadPlacementRule = DecodePlacementRule(d)
		m.TailPlacement.PlacementRule = DecodePlacementRule(d)
	}
	if v >= 8 {
		m.TierType = d.String()
		m.TierConfig = DecodeObjTier(d)
	}
	d.EndStruct(h)
	return m
}

// sameBucket is rgw_bucket's ==, which compares tenant, name and id only.
func sameBucket(a, b BucketID) bool {
	return a.Tenant == b.Tenant && a.Name == b.Name && a.ID == b.ID
}

// ManifestPart is RGWObjManifestPart, one piece of an explicit manifest: the
// object holding the data, where in it the data starts, and its length.
type ManifestPart struct {
	Loc    Obj    `json:"loc"`
	LocOfs uint64 `json:"loc_ofs"`
	Size   uint64 `json:"size"`
}

// Encode mirrors RGWObjManifestPart::encode, ENCODE_START(2, 2).
func (p ManifestPart) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 2)
	p.Loc.Encode(e, r)
	e.U64(p.LocOfs)
	e.U64(p.Size)
	e.EndStruct(f)
}

// DecodeManifestPart mirrors RGWObjManifestPart::decode,
// DECODE_START_LEGACY_COMPAT_LEN_32(2, 2, 2).
func DecodeManifestPart(d *denc.Decoder) ManifestPart {
	h := d.BeginStructLegacy(2, 2, 2, 3)
	var p ManifestPart
	p.Loc = DecodeObj(d)
	p.LocOfs = d.U64()
	p.Size = d.U64()
	d.EndStruct(h)
	return p
}

// ManifestRule is RGWObjManifestRule: from StartOfs on, the object is cut into
// parts of PartSize bytes, numbered from StartPartNum, and each part into
// stripes of at most StripeMaxSize bytes. A PartSize of 0 means one part runs
// to the end of the object. A non-empty OverridePrefix replaces the
// manifest's prefix in the names of this rule's objects.
type ManifestRule struct {
	StartPartNum   uint32 `json:"start_part_num"`
	StartOfs       uint64 `json:"start_ofs"`
	PartSize       uint64 `json:"part_size"`
	StripeMaxSize  uint64 `json:"stripe_max_size"`
	OverridePrefix string `json:"override_prefix"`
}

// Encode mirrors RGWObjManifestRule::encode, ENCODE_START(2, 1).
func (x ManifestRule) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.U32(x.StartPartNum)
	e.U64(x.StartOfs)
	e.U64(x.PartSize)
	e.U64(x.StripeMaxSize)
	e.String(x.OverridePrefix)
	e.EndStruct(f)
}

// DecodeManifestRule mirrors RGWObjManifestRule::decode, DECODE_START(2);
// version 1 has no override prefix.
func DecodeManifestRule(d *denc.Decoder) ManifestRule {
	h := d.BeginStruct(2)
	var x ManifestRule
	x.StartPartNum = d.U32()
	x.StartOfs = d.U64()
	x.PartSize = d.U64()
	x.StripeMaxSize = d.U64()
	if h.Version >= 2 {
		x.OverridePrefix = d.String()
	}
	d.EndStruct(h)
	return x
}

// ObjTier is RGWObjTier, the cloud tier an object was transitioned to. The
// zero value lacks the C++ member defaults; NewObjTier has them.
type ObjTier struct {
	Name              string                 `json:"name"`
	Tier              ZoneGroupPlacementTier `json:"tier_placement"`
	IsMultipartUpload bool                   `json:"is_multipart_upload"`
}

// NewObjTier returns what RGWObjTier's constructor leaves: the name "none"
// and a placement tier with its own defaults.
func NewObjTier() ObjTier {
	return ObjTier{Name: "none", Tier: NewZoneGroupPlacementTier()}
}

// Encode mirrors RGWObjTier::encode, ENCODE_START(2, 2). The placement tier
// is written at r's version.
func (t ObjTier) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 2)
	e.String(t.Name)
	t.Tier.Encode(e, r)
	e.Bool(t.IsMultipartUpload)
	e.EndStruct(f)
}

// DecodeObjTier mirrors RGWObjTier::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2).
func DecodeObjTier(d *denc.Decoder) ObjTier {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	t := NewObjTier()
	t.Name = d.String()
	t.Tier = DecodeZoneGroupPlacementTier(d)
	t.IsMultipartUpload = d.Bool()
	d.EndStruct(h)
	return t
}
