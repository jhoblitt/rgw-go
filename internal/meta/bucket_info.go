package meta

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The RGWBucketFlags bits of BucketInfo.Flags.
const (
	BucketSuspended         = 0x1
	BucketVersioned         = 0x2
	BucketVersionsSuspended = 0x4
	BucketDatasyncDisabled  = 0x8
	BucketMFAEnabled        = 0x10
	BucketObjLockEnabled    = 0x20
	BucketDeleted           = 0x40
)

// ReshardStatus is cls_rgw_reshard_status.
type ReshardStatus uint8

// The cls_rgw_reshard_status values.
const (
	ReshardStatusNotResharding ReshardStatus = 0
	ReshardStatusInProgress    ReshardStatus = 1
	ReshardStatusDone          ReshardStatus = 2
	ReshardStatusInLogrecord   ReshardStatus = 3
)

// RawStruct is an encoded struct this package does not model: its whole
// ENCODE_START block, header included, written back verbatim. Decoding one
// applies the owning type's DECODE_START compat check and nothing more, so an
// older version is not upgraded to the one radosgw would re-encode.
type RawStruct []byte

// ErrOpaqueJSON is returned by MarshalJSON for a value whose dump includes a
// RawStruct, which has no JSON form here.
var ErrOpaqueJSON = errors.New("meta: no JSON form for an opaque struct")

// rawHeaderLen is the ENCODE_START header: version, compat and a u32 length.
const rawHeaderLen = 6

// decodeRawStruct reads one ENCODE_START block whole, failing as
// DECODE_START(maxVersion) would when its compat exceeds maxVersion.
func decodeRawStruct(d *denc.Decoder, maxVersion uint8) RawStruct {
	hdr := d.Raw(rawHeaderLen)
	if d.Err() != nil {
		return nil
	}
	if hdr[1] > maxVersion {
		d.Fail(fmt.Errorf("%w: struct_v %d compat %d, decoder %d", denc.ErrIncompatible, hdr[0], hdr[1], maxVersion))
		return nil
	}
	body := d.Raw(int(binary.LittleEndian.Uint32(hdr[2:])))
	if d.Err() != nil {
		return nil
	}
	return append(hdr, body...)
}

// The DECODE_START versions of the types BucketInfo holds as RawStruct:
// RGWBucketWebsiteConf, RGWObjectLock and rgw_sync_policy_info.
const (
	websiteVersion    = 2
	objLockVersion    = 1
	syncPolicyVersion = 1
)

// defaultObjLock is RGWObjectLock() as RGWObjectLock::encode writes it:
// ENCODE_START(1, 1), enabled true, rule_exist false. radosgw writes this
// when it creates a bucket with object lock enabled and no rule.
var defaultObjLock = RawStruct{1, 1, 2, 0, 0, 0, 1, 0}

// syncPolicyEmpty is rgw_sync_policy_info::empty on an encoding: its groups
// map, the first field, has no entries.
func syncPolicyEmpty(p RawStruct) bool {
	return len(p) < rawHeaderLen+4 || binary.LittleEndian.Uint32(p[rawHeaderLen:]) == 0
}

// BucketInfo is RGWBucketInfo, the bucket instance. Owner is a user for every
// bucket written before version 24.
type BucketInfo struct {
	Bucket         BucketID
	Owner          Owner
	Flags          uint32
	Zonegroup      string
	CreationTime   Time
	PlacementRule  PlacementRule
	HasInstanceObj bool
	Quota          Quota
	Layout         BucketLayout
	RequesterPays  bool
	// Website is the RGWBucketWebsiteConf, nil when has_website is false.
	Website             RawStruct
	SwiftVersioning     bool
	SwiftVerLocation    string
	MDSearchConfig      map[string]uint32
	ReshardStatus       ReshardStatus
	NewBucketInstanceID string
	// ObjLock is the RGWObjectLock, encoded only while Flags has
	// BucketObjLockEnabled; nil stands for RGWObjectLock's defaults.
	ObjLock RawStruct
	// SyncPolicy is the rgw_sync_policy_info, nil when absent. An encoding
	// with no groups is written as absent, as empty_sync_policy() decides.
	SyncPolicy RawStruct
}

// NewBucketInfo returns the value RGWBucketInfo's member initializers give:
// the empty user as owner, unlimited quota and NewBucketLayout.
func NewBucketInfo() BucketInfo {
	return BucketInfo{
		Owner:  UserOwner(UserID{}),
		Quota:  defaultQuota(),
		Layout: NewBucketLayout(),
	}
}

// HasWebsite is has_website.
func (b BucketInfo) HasWebsite() bool { return b.Website != nil }

// ObjLockEnabled is obj_lock_enabled().
func (b BucketInfo) ObjLockEnabled() bool { return b.Flags&BucketObjLockEnabled != 0 }

// unixSeconds is real_clock::to_time_t as the bucket encoders store it in a u64.
func unixSeconds(t Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.Unix()) //nolint:gosec // C++ casts time_t to uint64_t
}

// fromUnixSeconds is real_clock::from_time_t; zero is the zero Time.
func fromUnixSeconds(s uint64) Time {
	if s == 0 {
		return Time{}
	}
	return Time{time.Unix(int64(s), 0).UTC()} //nolint:gosec // C++ casts uint64_t to time_t
}

// ownerUser is the rgw_user the bucket encoders duplicate into their legacy
// owner fields: the owner when it is a user, the empty user otherwise.
func (o Owner) ownerUser() UserID {
	if o.User != nil {
		return *o.User
	}
	return UserID{}
}

// Encode mirrors RGWBucketInfo::encode, ENCODE_START(24, 4). The owner's id,
// tenant and namespace are duplicated into their pre-v24 positions, empty for
// an account.
func (b BucketInfo) Encode(e *denc.Encoder, r denc.Release) {
	user := b.Owner.ownerUser()
	f := e.BeginStruct(24, 4)
	b.Bucket.Encode(e, r)
	e.String(user.ID)
	e.U32(b.Flags)
	e.String(b.Zonegroup)
	e.U64(unixSeconds(b.CreationTime))
	b.PlacementRule.Encode(e, r)
	e.Bool(b.HasInstanceObj)
	b.Quota.Encode(e, r)
	e.Bool(b.RequesterPays)
	e.String(user.Tenant)
	e.Bool(b.HasWebsite())
	if b.HasWebsite() {
		e.Raw(b.Website)
	}
	e.Bool(b.SwiftVersioning)
	if b.SwiftVersioning {
		e.String(b.SwiftVerLocation)
	}
	b.CreationTime.Encode(e)
	denc.EncodeMap(e, b.MDSearchConfig, (*denc.Encoder).String, (*denc.Encoder).U32)
	e.U8(uint8(b.ReshardStatus))
	e.String(b.NewBucketInstanceID)
	if b.ObjLockEnabled() {
		if b.ObjLock == nil {
			e.Raw(defaultObjLock)
		} else {
			e.Raw(b.ObjLock)
		}
	}
	hasSyncPolicy := b.SyncPolicy != nil && !syncPolicyEmpty(b.SyncPolicy)
	e.Bool(hasSyncPolicy)
	if hasSyncPolicy {
		e.Raw(b.SyncPolicy)
	}
	b.Layout.Encode(e, r)
	e.String(user.NS)
	b.Owner.EncodeVersioned(e, r)
	e.EndStruct(f)
}

// newLayoutVersion is the RGWBucketInfo version that replaced the piecewise
// index fields with rgw::BucketLayout.
const newLayoutVersion = 22

// DecodeBucketInfo mirrors RGWBucketInfo::decode,
// DECODE_START_LEGACY_COMPAT_LEN_32(24, 4, 4), into NewBucketInfo.
func DecodeBucketInfo(d *denc.Decoder) BucketInfo {
	h := d.BeginStructLegacy(24, 4, 4, 3)
	b := decodeBucketInfoBody(d, h)
	d.EndStruct(h)
	return b
}

// decodeBucketInfoBody reads the fields of an RGWBucketInfo whose header h has
// been read, leaving the struct open. Before version 22 the index layout was
// stored piecewise; before version 24 the owner was a user stored piecewise.
// A layout left without logs gets one derived from a Normal index.
func decodeBucketInfoBody(d *denc.Decoder, h denc.Header) BucketInfo {
	v := h.Version
	b := NewBucketInfo()
	var user UserID
	normal := &b.Layout.Current.Layout.Normal
	b.Bucket = DecodeBucketID(d)
	if v >= 2 {
		user = ParseUserID(d.String())
	}
	if v >= 3 {
		b.Flags = d.U32()
	}
	if v >= 5 {
		b.Zonegroup = d.String()
	}
	if v >= 6 {
		ct := d.U64()
		if v < 17 {
			b.CreationTime = fromUnixSeconds(ct)
		}
	}
	if v >= 7 {
		b.PlacementRule = DecodePlacementRule(d)
	}
	if v >= 8 {
		b.HasInstanceObj = d.Bool()
	}
	if v >= 9 {
		b.Quota = DecodeQuota(d)
	}
	if v >= 10 && v < newLayoutVersion {
		normal.NumShards = d.U32()
	}
	if v >= 11 && v < newLayoutVersion {
		normal.HashType = HashType(d.U8())
	}
	if v >= 12 {
		b.RequesterPays = d.Bool()
	}
	if v >= 13 {
		user.Tenant = d.String()
	}
	if v >= 14 && d.Bool() {
		b.Website = decodeRawStruct(d, websiteVersion)
	}
	if v >= 15 && v < newLayoutVersion {
		b.Layout.Current.Layout.Type = IndexType(d.U32()) //nolint:gosec // C++ casts the u32 to the u8 enum
	}
	if v >= 16 {
		b.SwiftVersioning = d.Bool()
		if b.SwiftVersioning {
			b.SwiftVerLocation = d.String()
		}
	}
	if v >= 17 {
		b.CreationTime = DecodeTime(d)
	}
	if v >= 18 {
		b.MDSearchConfig = denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).U32)
	}
	if v >= 19 {
		b.ReshardStatus = ReshardStatus(d.U8())
		b.NewBucketInstanceID = d.String()
	}
	if v >= 20 && b.ObjLockEnabled() {
		b.ObjLock = decodeRawStruct(d, objLockVersion)
	}
	if v >= 21 && d.Bool() {
		b.SyncPolicy = decodeRawStruct(d, syncPolicyVersion)
	}
	if v >= newLayoutVersion {
		b.Layout = DecodeBucketLayout(d)
	}
	if v >= 23 {
		user.NS = d.String()
	}
	if v >= 24 {
		b.Owner = DecodeOwnerVersioned(d)
	} else {
		b.Owner = UserOwner(user)
	}
	if len(b.Layout.Logs) == 0 && b.Layout.Current.Layout.Type == IndexNormal {
		b.Layout.Logs = []LogLayoutGen{LogLayoutFromIndex(0, b.Layout.Current)}
	}
	return b
}

// dirOIDPrefix is dir_oid_prefix in svc_bi_rados.cc.
const dirOIDPrefix = ".dir."

// IndexShardOID names one object of the index generation gen, as
// RGWSI_BucketIndex_RADOS::get_bucket_index_object does: ".dir.<bucket id>"
// when the layout has no shards, ".dir.<bucket id>.<shard>" for generation 0,
// and ".dir.<bucket id>.<gen>.<shard>" otherwise.
func (b BucketInfo) IndexShardOID(gen IndexLayoutGen, shard uint32) string {
	base := dirOIDPrefix + b.Bucket.ID
	switch {
	case gen.Layout.Normal.NumShards == 0:
		return base
	case gen.Gen == 0:
		return base + "." + strconv.FormatUint(uint64(shard), 10)
	default:
		return base + "." + strconv.FormatUint(gen.Gen, 10) + "." + strconv.FormatUint(uint64(shard), 10)
	}
}

// bucketInfoDump is the field layout of RGWBucketInfo::dump as Squid and
// Tentacle write it; main adds obj_lock.
type bucketInfoDump struct {
	Bucket              BucketID                    `json:"bucket"`
	CreationTime        Time                        `json:"creation_time"`
	Owner               Owner                       `json:"owner"`
	Flags               uint32                      `json:"flags"`
	Zonegroup           string                      `json:"zonegroup"`
	PlacementRule       PlacementRule               `json:"placement_rule"`
	HasInstanceObj      bool                        `json:"has_instance_obj"`
	Quota               Quota                       `json:"quota"`
	NumShards           uint32                      `json:"num_shards"`
	BIShardHashType     uint32                      `json:"bi_shard_hash_type"`
	RequesterPays       bool                        `json:"requester_pays"`
	HasWebsite          bool                        `json:"has_website"`
	SwiftVersioning     bool                        `json:"swift_versioning"`
	SwiftVerLocation    string                      `json:"swift_ver_location"`
	IndexType           uint32                      `json:"index_type"`
	MDSearchConfig      []jsonEntry[string, uint32] `json:"mdsearch_config"`
	ReshardStatus       int                         `json:"reshard_status"`
	NewBucketInstanceID string                      `json:"new_bucket_instance_id"`
}

// MarshalJSON is RGWBucketInfo::dump as Squid and Tentacle write it: of the
// layout only the current index's shard count, hash type and index type. It
// fails with ErrOpaqueJSON when the dump would include website_conf or
// sync_policy, which are held as RawStruct.
func (b BucketInfo) MarshalJSON() ([]byte, error) {
	if b.HasWebsite() {
		return nil, fmt.Errorf("%w: website_conf", ErrOpaqueJSON)
	}
	if b.SyncPolicy != nil && !syncPolicyEmpty(b.SyncPolicy) {
		return nil, fmt.Errorf("%w: sync_policy", ErrOpaqueJSON)
	}
	mdsearch := make([]jsonEntry[string, uint32], 0, len(b.MDSearchConfig))
	for _, k := range slices.Sorted(maps.Keys(b.MDSearchConfig)) {
		mdsearch = append(mdsearch, jsonEntry[string, uint32]{k, b.MDSearchConfig[k]})
	}
	current := b.Layout.Current.Layout
	return json.Marshal(bucketInfoDump{
		Bucket:              b.Bucket,
		CreationTime:        b.CreationTime,
		Owner:               b.Owner,
		Flags:               b.Flags,
		Zonegroup:           b.Zonegroup,
		PlacementRule:       b.PlacementRule,
		HasInstanceObj:      b.HasInstanceObj,
		Quota:               b.Quota,
		NumShards:           current.Normal.NumShards,
		BIShardHashType:     uint32(current.Normal.HashType),
		RequesterPays:       b.RequesterPays,
		HasWebsite:          b.HasWebsite(),
		SwiftVersioning:     b.SwiftVersioning,
		SwiftVerLocation:    b.SwiftVerLocation,
		IndexType:           uint32(current.Type),
		MDSearchConfig:      mdsearch,
		ReshardStatus:       int(b.ReshardStatus),
		NewBucketInstanceID: b.NewBucketInstanceID,
	})
}
