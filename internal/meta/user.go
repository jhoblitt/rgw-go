package meta

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// AccessKey is RGWAccessKey, an S3 or Swift credential. Its JSON form is
// RGWAccessKey::dump; RGWUserInfo renders keys differently, naming the owner.
// The zero value is inactive; NewAccessKey gives the C++ default, active.
type AccessKey struct {
	ID        string `json:"access_key"`
	Secret    string `json:"secret_key"`
	Subuser   string `json:"subuser"`
	Active    bool   `json:"active"`
	CreatedAt Time   `json:"create_date"`
}

// NewAccessKey returns the value RGWAccessKey's member initializers give:
// an active key with no id or secret.
func NewAccessKey() AccessKey { return AccessKey{Active: true} }

// Encode mirrors RGWAccessKey::encode, ENCODE_START(4, 2).
func (k AccessKey) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(4, 2)
	e.String(k.ID)
	e.String(k.Secret)
	e.String(k.Subuser)
	e.Bool(k.Active)
	k.CreatedAt.Encode(e)
	e.EndStruct(f)
}

// DecodeAccessKey mirrors RGWAccessKey::decode,
// DECODE_START_LEGACY_COMPAT_LEN_32(4, 2, 2). A key from before version 3 is
// active, the constructor's default.
func DecodeAccessKey(d *denc.Decoder) AccessKey {
	h := d.BeginStructLegacy(4, 2, 2, 3)
	k := AccessKey{
		ID:      d.String(),
		Secret:  d.String(),
		Subuser: d.String(),
		Active:  true,
	}
	if h.Version >= 3 {
		k.Active = d.Bool()
	}
	if h.Version >= 4 {
		k.CreatedAt = DecodeTime(d)
	}
	d.EndStruct(h)
	return k
}

// userDump is RGWAccessKey::dump(f, user, swift), the form RGWUserInfo::dump
// lists keys in: the owner, with the subuser appended, in place of the
// subuser field, and no access key for a Swift key.
func (k AccessKey) userDump(user string, swift bool) map[string]any {
	if k.Subuser != "" {
		user += ":" + k.Subuser
	}
	m := map[string]any{
		"user":        user,
		"secret_key":  k.Secret,
		"active":      k.Active,
		"create_date": k.CreatedAt,
	}
	if !swift {
		m["access_key"] = k.ID
	}
	return m
}

// SubUser is RGWSubUser: a subuser name and its RGW_PERM_* mask.
type SubUser struct {
	Name string
	Perm uint32
}

// Encode mirrors RGWSubUser::encode, ENCODE_START(2, 2).
func (s SubUser) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 2)
	e.String(s.Name)
	e.U32(s.Perm)
	e.EndStruct(f)
}

// DecodeSubUser mirrors RGWSubUser::decode,
// DECODE_START_LEGACY_COMPAT_LEN_32(2, 2, 2).
func DecodeSubUser(d *denc.Decoder) SubUser {
	h := d.BeginStructLegacy(2, 2, 2, 3)
	var s SubUser
	s.Name = d.String()
	s.Perm = d.U32()
	d.EndStruct(h)
	return s
}

// MarshalJSON is RGWSubUser::dump(f). RGWUserInfo::dump uses the
// dump(f, user) form instead, which prefixes the id with the owner.
func (s SubUser) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.dump(s.Name))
}

func (s SubUser) dump(id string) map[string]string {
	return map[string]string{"id": id, "permissions": permString(s.Perm)}
}

// flagName is one row of the rgw_flags_desc tables in rgw_common.cc.
type flagName struct {
	mask uint32
	name string
}

// rgwPerms is rgw_perms: RGW_PERM_* combinations, widest first.
var rgwPerms = []flagName{
	{0xf, "full-control"},
	{0x3, "read-write"},
	{0x1, "read"},
	{0x2, "write"},
	{0x4, "read-acp"},
	{0x8, "write-acp"},
}

// opTypeFlags is op_type_flags: the RGW_OP_TYPE_* bits.
var opTypeFlags = []flagName{
	{0x1, "read"},
	{0x2, "write"},
	{0x4, "delete"},
}

// permString is perm_to_str.
func permString(mask uint32) string { return maskString(rgwPerms, mask) }

// maskString is mask_to_str: the names of the table rows mask covers, each
// row's bits cleared once named. A mask with no named bit leaves C++'s buffer
// unwritten; here it gives "".
func maskString(table []flagName, mask uint32) string {
	if mask == 0 {
		return "<none>"
	}
	var names []string
	for mask != 0 {
		orig := mask
		for _, fl := range table {
			if mask&fl.mask == fl.mask {
				names = append(names, fl.name)
				mask &^= fl.mask
				if mask == 0 {
					break
				}
			}
		}
		if mask == orig {
			break
		}
	}
	return strings.Join(names, ", ")
}

// Caps is RGWUserCaps: admin capability type to RGW_CAP_* bits. Its JSON form
// is the list RGWUserCaps::dump(f, name) writes.
type Caps map[string]uint32 //nolint:recvcheck // AddString allocates a nil map, which a value receiver cannot; the rest only read or delete, safe on nil

// capNames is cap_names in rgw_common.cc.
var capNames = []flagName{
	{0x3, "*"},
	{0x1, "read"},
	{0x2, "write"},
}

// Encode mirrors RGWUserCaps::encode, ENCODE_START(1, 1).
func (c Caps) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeMap(e, c, (*denc.Encoder).String, (*denc.Encoder).U32)
	e.EndStruct(f)
}

// DecodeCaps mirrors RGWUserCaps::decode, DECODE_START(1).
func DecodeCaps(d *denc.Decoder) Caps {
	h := d.BeginStruct(1)
	c := Caps(denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).U32))
	d.EndStruct(h)
	return c
}

// MarshalJSON writes a {"type", "perm"} object per cap in type order.
func (c Caps) MarshalJSON() ([]byte, error) {
	out := make([]map[string]string, 0, len(c))
	for _, typ := range slices.Sorted(maps.Keys(c)) {
		out = append(out, map[string]string{"type": typ, "perm": capPermString(c[typ])})
	}
	return json.Marshal(out)
}

// capPermString is the perm string loop in RGWUserCaps::dump(f, name), which
// differs from mask_to_str in making a single pass and in its empty result.
func capPermString(perm uint32) string {
	var names []string
	for _, fl := range capNames {
		if perm&fl.mask == fl.mask {
			names = append(names, fl.name)
			perm &^= fl.mask
		}
	}
	if len(names) == 0 {
		return "<none>"
	}
	return strings.Join(names, ", ")
}

// IdentityType is RGWIdentityType, the source of a user's identity.
type IdentityType uint32

// The RGWIdentityType values.
const (
	IdentityNone     IdentityType = 0
	IdentityRGW      IdentityType = 1
	IdentityKeystone IdentityType = 2
	IdentityLDAP     IdentityType = 3
	IdentityRole     IdentityType = 4
	IdentityWeb      IdentityType = 5
	IdentityRoot     IdentityType = 6
)

// DumpName is the user_source_type RGWUserInfo::dump and the admin API's
// dump_user_info write, which name only some types and call the rest "none"
// (driver/rados/rgw_user.cc:164-185 at v19.2.6).
func (t IdentityType) DumpName() string {
	switch t {
	case IdentityRGW:
		return "rgw"
	case IdentityKeystone:
		return "keystone"
	case IdentityLDAP:
		return "ldap"
	case IdentityRoot:
		return "root"
	default:
		return "none"
	}
}

// Defaults from rgw_common.h: RGW_DEFAULT_MAX_BUCKETS and RGW_OP_TYPE_ALL.
const (
	DefaultMaxBuckets = 1000
	OpTypeAll         = 0x7
)

// UserTag is one entry of RGWUserInfo's tags multimap.
type UserTag struct {
	Key, Value string
}

// UserInfo is RGWUserInfo. Maps hold what C++ keeps in std::map; MFAIDs and
// GroupIDs are sets, encoded sorted and without duplicates whatever their
// order here; Tags is a multimap, encoded in key order with equal keys kept in
// slice order. Its JSON form is RGWUserInfo::dump as Squid and Tentacle write
// it.
type UserInfo struct {
	UserID      UserID
	DisplayName string
	Email       string
	AccessKeys  map[string]AccessKey
	SwiftKeys   map[string]AccessKey
	SubUsers    map[string]SubUser
	Suspended   uint8
	MaxBuckets  int32
	OpMask      uint32
	Caps        Caps
	Admin       uint8
	System      uint8
	// DefaultPlacement dumps as two fields, default_placement and
	// default_storage_class, not as its string form.
	DefaultPlacement PlacementRule
	PlacementTags    []string
	TempURLKeys      map[int32]string
	BucketQuota      Quota
	UserQuota        Quota
	Type             IdentityType
	MFAIDs           []string
	AccountID        string
	Path             string
	CreateDate       Time
	Tags             []UserTag
	GroupIDs         []string
}

// defaultQuota is RGWQuotaInfo's constructor: both limits unlimited.
func defaultQuota() Quota { return Quota{MaxSize: -1, MaxObjects: -1} }

// NewUserInfo returns the value RGWUserInfo's constructor and member
// initializers give.
func NewUserInfo() UserInfo {
	return UserInfo{
		MaxBuckets:  DefaultMaxBuckets,
		OpMask:      OpTypeAll,
		BucketQuota: defaultQuota(),
		UserQuota:   defaultQuota(),
		Path:        "/",
	}
}

// Encode mirrors RGWUserInfo::encode, ENCODE_START(23, 9).
func (u UserInfo) Encode(e *denc.Encoder, r denc.Release) {
	encKey := func(e *denc.Encoder, k AccessKey) { k.Encode(e, r) }
	f := e.BeginStruct(23, 9)
	e.U64(0) // old auid
	first := firstKey(u.AccessKeys)
	e.String(first.ID)
	e.String(first.Secret)
	e.String(u.DisplayName)
	e.String(u.Email)
	swift := firstKey(u.SwiftKeys)
	e.String(swift.ID)
	e.String(swift.Secret)
	e.String(u.UserID.ID)
	denc.EncodeMap(e, u.AccessKeys, (*denc.Encoder).String, encKey)
	denc.EncodeMap(e, u.SubUsers, (*denc.Encoder).String, func(e *denc.Encoder, s SubUser) { s.Encode(e, r) })
	e.U8(u.Suspended)
	denc.EncodeMap(e, u.SwiftKeys, (*denc.Encoder).String, encKey)
	e.I32(u.MaxBuckets)
	u.Caps.Encode(e, r)
	e.U32(u.OpMask)
	e.U8(u.System)
	u.DefaultPlacement.Encode(e, r)
	denc.EncodeSlice(e, u.PlacementTags, (*denc.Encoder).String)
	u.BucketQuota.Encode(e, r)
	denc.EncodeMap(e, u.TempURLKeys, (*denc.Encoder).I32, (*denc.Encoder).String)
	u.UserQuota.Encode(e, r)
	e.String(u.UserID.Tenant)
	e.U8(u.Admin)
	e.U32(uint32(u.Type))
	denc.EncodeSlice(e, stringSet(u.MFAIDs), (*denc.Encoder).String)
	e.String("") // assumed_role_arn, removed
	e.String(u.UserID.NS)
	e.String(u.AccountID)
	e.String(u.Path)
	u.CreateDate.Encode(e)
	denc.EncodeSlice(e, sortedTags(u.Tags), func(e *denc.Encoder, t UserTag) {
		e.String(t.Key)
		e.String(t.Value)
	})
	denc.EncodeSlice(e, stringSet(u.GroupIDs), (*denc.Encoder).String)
	e.EndStruct(f)
}

// firstKey is the key std::map::begin() finds, or a zero key for an empty map.
func firstKey(m map[string]AccessKey) AccessKey {
	if len(m) == 0 {
		return AccessKey{}
	}
	return m[slices.Min(slices.Collect(maps.Keys(m)))]
}

// stringSet returns ss as std::set would hold it: sorted, without
// duplicates, and nil when empty. It does not modify ss.
func stringSet(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	return slices.Compact(slices.Sorted(slices.Values(ss)))
}

// sortedTags returns ts in std::multimap order: by key, equal keys in their
// original order. It does not modify ts.
func sortedTags(ts []UserTag) []UserTag {
	if len(ts) == 0 {
		return nil
	}
	out := slices.Clone(ts)
	slices.SortStableFunc(out, func(a, b UserTag) int { return cmp.Compare(a.Key, b.Key) })
	return out
}

// DecodeUserInfo mirrors RGWUserInfo::decode,
// DECODE_START_LEGACY_COMPAT_LEN_32(23, 9, 9), into a NewUserInfo. Before
// version 6 the single key pair becomes the only access key, and before
// version 5 its id is also the user id.
func DecodeUserInfo(d *denc.Decoder) UserInfo {
	h := d.BeginStructLegacy(23, 9, 9, 3)
	u := NewUserInfo()
	v := h.Version
	if v >= 2 {
		d.U64() // old auid
	}
	accessKey := d.String()
	secretKey := d.String()
	if v < 6 {
		u.AccessKeys = map[string]AccessKey{accessKey: {ID: accessKey, Secret: secretKey, Active: true}}
	}
	u.DisplayName = d.String()
	u.Email = d.String()
	if v >= 3 {
		_ = d.String() // swift_name, superseded by swift_keys
	}
	if v >= 4 {
		_ = d.String() // swift_key, likewise
	}
	if v >= 5 {
		u.UserID.ID = d.String()
	} else {
		u.UserID.ID = accessKey
	}
	if v >= 6 {
		u.AccessKeys = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeAccessKey)
		u.SubUsers = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeSubUser)
	}
	if v >= 7 {
		u.Suspended = d.U8()
	}
	if v >= 8 {
		u.SwiftKeys = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeAccessKey)
	}
	if v >= 10 {
		u.MaxBuckets = d.I32()
	}
	if v >= 11 {
		u.Caps = DecodeCaps(d)
	}
	if v >= 12 {
		u.OpMask = d.U32()
	}
	if v >= 13 {
		u.System = d.U8()
		u.DefaultPlacement = DecodePlacementRule(d)
		u.PlacementTags = denc.DecodeSlice(d, (*denc.Decoder).String)
	}
	if v >= 14 {
		u.BucketQuota = DecodeQuota(d)
	}
	if v >= 15 {
		u.TempURLKeys = denc.DecodeMap(d, (*denc.Decoder).I32, (*denc.Decoder).String)
	}
	if v >= 16 {
		u.UserQuota = DecodeQuota(d)
	}
	if v >= 17 {
		u.UserID.Tenant = d.String()
	}
	if v >= 18 {
		u.Admin = d.U8()
	}
	if v >= 19 {
		u.Type = IdentityType(d.U32())
	}
	if v >= 20 {
		u.MFAIDs = stringSet(denc.DecodeSlice(d, (*denc.Decoder).String))
	}
	if v >= 21 {
		_ = d.String() // assumed_role_arn, removed
	}
	if v >= 22 {
		u.UserID.NS = d.String()
	}
	if v >= 23 {
		u.AccountID = d.String()
		u.Path = d.String()
		u.CreateDate = DecodeTime(d)
		u.Tags = sortedTags(denc.DecodeSlice(d, func(d *denc.Decoder) UserTag {
			return UserTag{Key: d.String(), Value: d.String()}
		}))
		u.GroupIDs = stringSet(denc.DecodeSlice(d, (*denc.Decoder).String))
	}
	d.EndStruct(h)
	return u
}

// jsonEntry is the {"key", "val"} object encode_json writes per entry of a
// std::map or std::multimap.
type jsonEntry[K, V any] struct {
	Key K `json:"key"`
	Val V `json:"val"`
}

// userInfoDump is the field layout of RGWUserInfo::dump.
type userInfoDump struct {
	UserID              string                      `json:"user_id"`
	DisplayName         string                      `json:"display_name"`
	Email               string                      `json:"email"`
	Suspended           int                         `json:"suspended"`
	MaxBuckets          int32                       `json:"max_buckets"`
	SubUsers            []map[string]string         `json:"subusers"`
	Keys                []map[string]any            `json:"keys"`
	SwiftKeys           []map[string]any            `json:"swift_keys"`
	Caps                Caps                        `json:"caps"`
	OpMask              string                      `json:"op_mask"`
	System              bool                        `json:"system,omitempty"`
	Admin               bool                        `json:"admin,omitempty"`
	DefaultPlacement    string                      `json:"default_placement"`
	DefaultStorageClass string                      `json:"default_storage_class"`
	PlacementTags       []string                    `json:"placement_tags"`
	BucketQuota         Quota                       `json:"bucket_quota"`
	UserQuota           Quota                       `json:"user_quota"`
	TempURLKeys         []jsonEntry[int32, string]  `json:"temp_url_keys"`
	Type                string                      `json:"type"`
	MFAIDs              []string                    `json:"mfa_ids"`
	AccountID           string                      `json:"account_id"`
	Path                string                      `json:"path"`
	CreateDate          Time                        `json:"create_date"`
	Tags                []jsonEntry[string, string] `json:"tags"`
	GroupIDs            []string                    `json:"group_ids"`
}

// MarshalJSON is RGWUserInfo::dump as Squid and Tentacle write it.
// default_storage_class is the stored class, empty included; releases after
// Tentacle write the canonical class, STANDARD for an empty one.
func (u UserInfo) MarshalJSON() ([]byte, error) {
	user := u.UserID.String()
	dumpKeys := func(m map[string]AccessKey, swift bool) []map[string]any {
		out := make([]map[string]any, 0, len(m))
		for _, id := range slices.Sorted(maps.Keys(m)) {
			out = append(out, m[id].userDump(user, swift))
		}
		return out
	}
	subusers := make([]map[string]string, 0, len(u.SubUsers))
	for _, name := range slices.Sorted(maps.Keys(u.SubUsers)) {
		s := u.SubUsers[name]
		subusers = append(subusers, s.dump(user+":"+s.Name))
	}
	tempURLKeys := make([]jsonEntry[int32, string], 0, len(u.TempURLKeys))
	for _, k := range slices.Sorted(maps.Keys(u.TempURLKeys)) {
		tempURLKeys = append(tempURLKeys, jsonEntry[int32, string]{k, u.TempURLKeys[k]})
	}
	tags := make([]jsonEntry[string, string], 0, len(u.Tags))
	for _, t := range sortedTags(u.Tags) {
		tags = append(tags, jsonEntry[string, string]{t.Key, t.Value})
	}
	return json.Marshal(userInfoDump{
		UserID:              user,
		DisplayName:         u.DisplayName,
		Email:               u.Email,
		Suspended:           int(u.Suspended),
		MaxBuckets:          u.MaxBuckets,
		SubUsers:            subusers,
		Keys:                dumpKeys(u.AccessKeys, false),
		SwiftKeys:           dumpKeys(u.SwiftKeys, true),
		Caps:                u.Caps,
		OpMask:              maskString(opTypeFlags, u.OpMask),
		System:              u.System != 0,
		Admin:               u.Admin != 0,
		DefaultPlacement:    u.DefaultPlacement.Name,
		DefaultStorageClass: u.DefaultPlacement.StorageClass,
		PlacementTags:       nonNil(u.PlacementTags),
		BucketQuota:         u.BucketQuota,
		UserQuota:           u.UserQuota,
		TempURLKeys:         tempURLKeys,
		Type:                u.Type.DumpName(),
		MFAIDs:              nonNil(stringSet(u.MFAIDs)),
		AccountID:           u.AccountID,
		Path:                u.Path,
		CreateDate:          u.CreateDate,
		Tags:                tags,
		GroupIDs:            nonNil(stringSet(u.GroupIDs)),
	})
}

// nonNil maps a nil slice to an empty one, so it marshals as [] as
// encode_json writes an empty container.
func nonNil(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

// UID is RGWUID: a user id in its string form, or in some objects an account
// id. It marshals as a plain string.
type UID string

// Encode mirrors RGWUID::encode: the string alone, with no struct header.
func (u UID) Encode(e *denc.Encoder, _ denc.Release) { e.String(string(u)) }

// DecodeUID mirrors RGWUID::decode.
func DecodeUID(d *denc.Decoder) UID { return UID(d.String()) }

// UserObject is the payload of a users.uid object: RGWUID followed by
// RGWUserInfo, as RGWSI_User_RADOS's PutOperation::put writes it.
type UserObject struct {
	UID  UID
	Info UserInfo
}

// Encode writes the UID then the user info.
func (o UserObject) Encode(e *denc.Encoder, r denc.Release) {
	o.UID.Encode(e, r)
	o.Info.Encode(e, r)
}

// DecodeUserObject mirrors RGWSI_User_RADOS::read_user_info: the UID, then
// the user info only when bytes remain, so an object holding just the UID
// gives NewUserInfo. Checking that the UID names the requested user is the
// caller's job.
func DecodeUserObject(d *denc.Decoder) UserObject {
	o := UserObject{UID: DecodeUID(d), Info: NewUserInfo()}
	if d.Err() == nil && d.Remaining() > 0 {
		o.Info = DecodeUserInfo(d)
	}
	return o
}
