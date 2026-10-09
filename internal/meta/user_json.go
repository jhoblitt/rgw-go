package meta

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// jsonFields is a JSON object's members as JSONObj finds them, read through
// decode_json_obj's conversions. JSONDecoder::decode_json resets an absent
// member's field to its type's default (ceph_json.h:334-357 at v19.2.6,
// :339-362 at v20.2.4), so each reader returns that default for a member
// the object lacks. The first member that does not convert is kept in err,
// and later reads return defaults.
//
// Where decode_json_obj would take a value it cannot mean, the readers
// refuse it: a string field given null, an object or an array; a container
// given anything but an array, null included, or holding a map entry or
// keyed element that is no object; and a time in a form other than the two
// encode_json writes.
type jsonFields struct {
	m   map[string]JSONMember
	err error
}

func newJSONFields(b []byte) (*jsonFields, error) {
	m, err := JSONMembers(b)
	if err != nil {
		return nil, err
	}
	return &jsonFields{m: m}, nil
}

// fail keeps the first failure.
func (f *jsonFields) fail(name, why string) {
	if f.err == nil {
		f.err = fmt.Errorf("%w: %s: %s", ErrBadJSONValue, name, why)
	}
}

// member returns the member name while nothing has failed.
func (f *jsonFields) member(name string) (JSONMember, bool) {
	if f.err != nil {
		return JSONMember{}, false
	}
	v, ok := f.m[name]
	return v, ok
}

func (f *jsonFields) has(name string) bool {
	_, ok := f.m[name]
	return ok
}

// jsonKind is the first byte of a JSON value past white space.
func jsonKind(raw json.RawMessage) byte {
	t := bytes.TrimLeft(raw, " \t\r\n")
	if len(t) == 0 {
		return 0
	}
	return t[0]
}

// str is decode_json_obj(std::string&): JSONObj::get_data, a string's
// contents or a number's or boolean's text.
func (f *jsonFields) str(name string) string {
	v, ok := f.member(name)
	if !ok {
		return ""
	}
	switch jsonKind(v.Raw) {
	case 'n', '{', '[':
		f.fail(name, "not a string")
		return ""
	}
	return v.Text
}

// boolean is decode_json_obj(bool&).
func (f *jsonFields) boolean(name string) bool {
	v, ok := f.member(name)
	if !ok {
		return false
	}
	b, err := jsonBool(name, v)
	if err != nil {
		f.err = err
	}
	return b
}

// int32v is decode_json_obj(int&): strtol, then refused outside int's range
// (ceph_json.cc:418-429 at v19.2.6 and v20.2.4).
func (f *jsonFields) int32v(name string) int32 {
	v, ok := f.member(name)
	if !ok {
		return 0
	}
	n, err := jsonInt64(name, v)
	if err != nil {
		f.err = err
		return 0
	}
	if n > math.MaxInt32 || n < math.MinInt32 {
		f.fail(name, "out of int's range")
		return 0
	}
	return int32(n)
}

// uint32v is decode_json_obj(unsigned&): strtoul, refused above UINT_MAX
// (ceph_json.cc:431-442 at v19.2.6 and v20.2.4), and refused negative.
func (f *jsonFields) uint32v(name string) uint32 {
	v, ok := f.member(name)
	if !ok {
		return 0
	}
	n, ok := jsonUnsigned(v.Text)
	if !ok || n > math.MaxUint32 {
		f.fail(name, "not an unsigned int")
		return 0
	}
	return uint32(n)
}

// jsonUnsigned is decode_json_obj(unsigned long&)'s strtoul over the text
// as a C string (ceph_json.cc:334-360 at v19.2.6 and v20.2.4), refused when
// it reads no digits, saturates, or leaves anything but white space. A
// minus sign negates in unsigned arithmetic, which lifts any negative but
// -0 above UINT_MAX.
func jsonUnsigned(text string) (uint64, bool) {
	s := rgwtext.CString(text)
	n, end, erange := rgwtext.Strtoull(s)
	if end == 0 || erange || strings.TrimLeft(s[end:], cSpace) != "" {
		return 0, false
	}
	return n, true
}

// elems is a container member's elements: decode_json_obj of a list, set or
// map iterates the member's children, which here must be an array's.
func (f *jsonFields) elems(name string) []json.RawMessage {
	v, ok := f.member(name)
	if !ok {
		return nil
	}
	if jsonKind(v.Raw) != '[' {
		f.fail(name, "not an array")
		return nil
	}
	var out []json.RawMessage
	if err := json.Unmarshal(v.Raw, &out); err != nil {
		f.fail(name, err.Error())
		return nil
	}
	return out
}

// strings is decode_json_obj of a list or set of strings.
func (f *jsonFields) strings(name string) []string {
	var out []string
	for i, e := range f.elems(name) {
		ef := &jsonFields{m: map[string]JSONMember{"obj": {Raw: e, Text: jsonText(e)}}}
		s := ef.str("obj")
		if ef.err != nil {
			f.fail(name, "element "+strconv.Itoa(i)+" is not a string")
			return nil
		}
		out = append(out, s)
	}
	return out
}

// jsonText is what JSONObj::get_data returns for a value: a string's
// contents, or the value's text.
func jsonText(raw json.RawMessage) string {
	var s string
	if jsonKind(raw) == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(bytes.TrimSpace(raw))
}

// entries is decode_json_obj of a std::map or multimap: each element's
// "key" and "val" members, read by key and val.
func (f *jsonFields) entries(name string, each func(e *jsonFields)) {
	for i, raw := range f.elems(name) {
		if jsonKind(raw) != '{' {
			f.fail(name, "entry "+strconv.Itoa(i)+" is not an object")
			return
		}
		ef, err := newJSONFields(raw)
		if err != nil {
			f.err = err
			return
		}
		each(ef)
		if ef.err != nil {
			f.err = ef.err
			return
		}
	}
}

// objects is decode_json_obj(container, cb): cb on each element, which must
// be an object.
func (f *jsonFields) objects(name string, each func(i int, e *jsonFields)) {
	for i, raw := range f.elems(name) {
		if jsonKind(raw) != '{' {
			f.fail(name, "element "+strconv.Itoa(i)+" is not an object")
			return
		}
		ef, err := newJSONFields(raw)
		if err != nil {
			f.err = err
			return
		}
		each(i, ef)
		if ef.err != nil {
			f.err = ef.err
			return
		}
	}
}

// decode is decode_json_obj(T&) for a type with its own decode_json: v's
// UnmarshalJSON on the member, which it leaves alone when absent.
func (f *jsonFields) decode(name string, v json.Unmarshaler) bool {
	m, ok := f.member(name)
	if !ok {
		return false
	}
	if err := v.UnmarshalJSON(m.Raw); err != nil {
		f.err = fmt.Errorf("%s: %w", name, err)
		return false
	}
	return true
}

// time is decode_json_obj(utime_t&) and of ceph::real_time&: the member's
// text through utime_t::parse_date (utime.h:397-502 at v19.2.6), here only
// in the two forms encode_json writes, "<sec>.<usec>" and the ISO date.
func (f *jsonFields) time(name string) Time {
	v, ok := f.member(name)
	if !ok {
		return Time{}
	}
	text := v.Text
	if !strings.Contains(text, "-") && !strings.Contains(text, ".") {
		f.fail(name, "not a time")
		return Time{}
	}
	q, err := json.Marshal(text)
	if err != nil {
		f.fail(name, err.Error())
		return Time{}
	}
	var t Time
	if err := t.UnmarshalJSON(q); err != nil {
		f.fail(name, "not a time")
		return Time{}
	}
	return t
}

// quota is decode_json of an RGWQuotaInfo member: RGWQuotaInfo's defaults,
// both limits unlimited, when absent.
func (f *jsonFields) quota(name string) Quota {
	q := defaultQuota()
	f.decode(name, &q)
	return q
}

// decodeAccessKey is RGWAccessKey::decode_json over an RGWAccessKey's
// defaults, an active key. The S3 form (rgw_common.cc:2990-3005 at v19.2.6,
// :3052-3067 at v20.2.4) needs access_key and secret_key, takes the subuser
// from "subuser", else from what follows the ':' of "user", and changes
// active only when present. The Swift form (:3007-3023; :3069-3085) takes
// the id from "user", which it needs, unless "subuser" is present, in which
// case radosgw leaves the id empty; a key without an id is refused here,
// which refuses a Swift key without its user too. It reads "active" as
// decode_json reads any member, so a Swift key without one is inactive.
func decodeAccessKey(f *jsonFields, swift bool) (AccessKey, error) {
	k := NewAccessKey()
	if !swift {
		if !f.has("access_key") {
			return AccessKey{}, fmt.Errorf("%w: missing mandatory field access_key", ErrBadJSONValue)
		}
		k.ID = f.str("access_key")
	}
	if !f.has("secret_key") {
		return AccessKey{}, fmt.Errorf("%w: missing mandatory field secret_key", ErrBadJSONValue)
	}
	k.Secret = f.str("secret_key")
	if f.has("subuser") {
		k.Subuser = f.str("subuser")
	} else {
		user := f.str("user")
		if swift {
			k.ID = user
		}
		if _, sub, ok := strings.Cut(user, ":"); ok {
			k.Subuser = sub
		}
	}
	switch {
	case swift:
		k.Active = f.boolean("active")
	case f.has("active"):
		k.Active = f.boolean("active")
	}
	k.CreatedAt = f.time("create_date")
	if f.err != nil {
		return AccessKey{}, f.err
	}
	if k.ID == "" {
		return AccessKey{}, fmt.Errorf("%w: a key without an id", ErrBadJSONValue)
	}
	return k, nil
}

// UnmarshalJSON is RGWUserCaps::decode_json (rgw_common.cc:2045-2069 at
// v19.2.6, :2108-2132 at v20.2.4): a list of {"type", "perm"}, perm parsed
// by parse_cap_perm, each type's last entry kept.
func (c *Caps) UnmarshalJSON(b []byte) error {
	f := &jsonFields{m: map[string]JSONMember{"caps": {Raw: b}}}
	out := Caps{}
	f.objects("caps", func(_ int, e *jsonFields) {
		typ := e.str("type")
		out[typ] = parseFlags(capNames, e.str("perm"))
	})
	if f.err != nil {
		return f.err
	}
	if len(out) == 0 {
		out = nil
	}
	*c = out
	return nil
}

// strToPerm is str_to_perm (rgw_common.cc:2924-2935 at v19.2.6, :2986-2997
// at v20.2.4), what RGWSubUser::decode_json reads permissions with: four
// words, anything else none.
func strToPerm(s string) uint32 {
	switch s {
	case "read":
		return 0x1
	case "write":
		return 0x2
	case "read-write":
		return 0x3
	case "full-control":
		return 0xf
	}
	return 0
}

// identityTypes are the type names RGWUserInfo::decode_json reads; another
// leaves the default, TYPE_NONE.
var identityTypes = map[string]IdentityType{
	"rgw": IdentityRGW, "keystone": IdentityKeystone, "ldap": IdentityLDAP, "root": IdentityRoot, "none": IdentityNone,
}

// UnmarshalJSON is RGWUserInfo::decode_json (rgw_common.cc:2837-2893 at
// v19.2.6, :2899-2955 at v20.2.4) over RGWUserInfo's defaults: user_id is
// mandatory and parsed as rgw_user::from_str; the keys, Swift keys and
// subusers come from their dump's arrays, each keyed by its own id; op_mask
// is parsed by rgw_parse_op_type_list. Every other absent member is its
// type's default: a max_buckets of 0, an op mask of none, a path of "".
func (u *UserInfo) UnmarshalJSON(b []byte) error {
	f, err := newJSONFields(b)
	if err != nil {
		return err
	}
	if !f.has("user_id") {
		return fmt.Errorf("%w: missing mandatory field user_id", ErrBadJSONValue)
	}
	out := NewUserInfo()
	out.UserID = ParseUserID(f.str("user_id"))
	out.DisplayName = f.str("display_name")
	out.Email = f.str("email")
	out.Suspended = boolByte(f.boolean("suspended"))
	out.MaxBuckets = f.int32v("max_buckets")
	out.AccessKeys = decodeKeys(f, "keys", false)
	out.SwiftKeys = decodeKeys(f, "swift_keys", true)
	f.objects("subusers", func(_ int, e *jsonFields) {
		s := SubUser{Perm: strToPerm(e.str("permissions"))}
		if _, name, ok := strings.Cut(e.str("id"), ":"); ok {
			s.Name = name
		}
		if out.SubUsers == nil {
			out.SubUsers = map[string]SubUser{}
		}
		out.SubUsers[s.Name] = s
	})
	out.Caps = nil
	f.decode("caps", &out.Caps)
	out.OpMask = ParseOpTypeList(f.str("op_mask"))
	out.System = boolByte(f.boolean("system"))
	out.Admin = boolByte(f.boolean("admin"))
	out.DefaultPlacement = PlacementRule{Name: f.str("default_placement"), StorageClass: f.str("default_storage_class")}
	out.PlacementTags = f.strings("placement_tags")
	out.BucketQuota = f.quota("bucket_quota")
	out.UserQuota = f.quota("user_quota")
	f.entries("temp_url_keys", func(e *jsonFields) {
		k, v := e.int32v("key"), e.str("val")
		if out.TempURLKeys == nil {
			out.TempURLKeys = map[int32]string{}
		}
		out.TempURLKeys[k] = v
	})
	if t, ok := identityTypes[f.str("type")]; ok {
		out.Type = t
	}
	out.MFAIDs = stringSet(f.strings("mfa_ids"))
	out.AccountID = f.str("account_id")
	out.Path = f.str("path")
	out.CreateDate = f.time("create_date")
	f.entries("tags", func(e *jsonFields) {
		out.Tags = append(out.Tags, UserTag{Key: e.str("key"), Value: e.str("val")})
	})
	out.Tags = sortedTags(out.Tags)
	out.GroupIDs = stringSet(f.strings("group_ids"))
	if f.err != nil {
		return f.err
	}
	*u = out
	return nil
}

// decodeKeys is decode_access_keys or decode_swift_keys over the array
// name: each key under its own id, a later one replacing an earlier.
func decodeKeys(f *jsonFields, name string, swift bool) map[string]AccessKey {
	var out map[string]AccessKey
	f.objects(name, func(_ int, e *jsonFields) {
		k, err := decodeAccessKey(e, swift)
		if err != nil {
			e.err = fmt.Errorf("%s: %w", name, err)
			return
		}
		if out == nil {
			out = map[string]AccessKey{}
		}
		out[k.ID] = k
	})
	return out
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// AttrsJSON is a std::map<std::string, bufferlist> as encode_json writes it:
// [{"key": name, "val": base64}], [] when empty.
type AttrsJSON map[string][]byte

// MarshalJSON is encode_json of the map: entries in key order, each value
// base64 with padding (ceph_json.cc:592-603 at v19.2.6 and v20.2.4).
func (a AttrsJSON) MarshalJSON() ([]byte, error) {
	out := make([]jsonEntry[string, string], 0, len(a))
	for _, k := range slices.Sorted(maps.Keys(a)) {
		out = append(out, jsonEntry[string, string]{k, base64.StdEncoding.EncodeToString(a[k])})
	}
	return json.Marshal(out)
}

// UnmarshalJSON is decode_json_obj of the map, each value through
// decode_base64 (ceph_json.cc:460-471 at v19.2.6 and v20.2.4), here read as
// padded standard base64; an empty array is an empty map.
func (a *AttrsJSON) UnmarshalJSON(b []byte) error {
	f := &jsonFields{m: map[string]JSONMember{"attrs": {Raw: b}}}
	out := AttrsJSON{}
	f.entries("attrs", func(e *jsonFields) {
		k := e.str("key")
		v, err := base64.StdEncoding.DecodeString(e.str("val"))
		if err != nil {
			e.fail("attrs", "a value that is not base64")
			return
		}
		out[k] = v
	})
	if f.err != nil {
		return f.err
	}
	*a = out
	return nil
}

// UserCompleteInfo is RGWUserCompleteInfo, the "user" metadata section's
// document (driver/rados/rgw_user.h:731-745 at v19.2.6, rgw_user.cc:2725-2739
// at v20.2.4). HasAttrs is has_attrs: whether decoded JSON carried "attrs".
type UserCompleteInfo struct {
	Info     UserInfo
	Attrs    AttrsJSON
	HasAttrs bool
}

// MarshalJSON is RGWUserCompleteInfo::dump: the user's fields, then "attrs"
// at the same level. The user is a named field because an embedded one
// would lend the wrapper its own JSON methods, which drop "attrs".
func (c UserCompleteInfo) MarshalJSON() ([]byte, error) {
	info, err := json.Marshal(c.Info)
	if err != nil {
		return nil, err
	}
	attrs, err := json.Marshal(c.Attrs)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), info[:len(info)-1]...)
	out = append(out, `,"attrs":`...)
	out = append(out, attrs...)
	return append(out, '}'), nil
}

// UnmarshalJSON is RGWUserCompleteInfo::decode_json: the user from the same
// object, and attrs when the object has them.
func (c *UserCompleteInfo) UnmarshalJSON(b []byte) error {
	var info UserInfo
	if err := info.UnmarshalJSON(b); err != nil {
		return err
	}
	f, err := newJSONFields(b)
	if err != nil {
		return err
	}
	var attrs AttrsJSON
	has := f.decode("attrs", &attrs)
	if f.err != nil {
		return f.err
	}
	c.Info, c.Attrs, c.HasAttrs = info, attrs, has
	return nil
}
