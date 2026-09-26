package acl

import (
	"encoding/json"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Permission is ACLPermission, a set of RGW_PERM_* flags. C++ stores it as an
// int, so the wire form is the same four bytes and the JSON form is signed.
type Permission uint32

// The RGW_PERM_* flags an ACL grant carries.
const (
	PermNone        Permission = 0x00
	PermRead        Permission = 0x01
	PermWrite       Permission = 0x02
	PermReadACP     Permission = 0x04
	PermWriteACP    Permission = 0x08
	PermFullControl            = PermRead | PermWrite | PermReadACP | PermWriteACP
)

// Encode mirrors ACLPermission::encode, ENCODE_START(2, 2).
func (p Permission) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 2)
	e.I32(int32(p)) //nolint:gosec // C++ stores the flags as an int
	e.EndStruct(f)
}

// DecodePermission mirrors ACLPermission::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2).
func DecodePermission(d *denc.Decoder) Permission {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	p := Permission(d.I32()) //nolint:gosec // C++ stores the flags as an int
	d.EndStruct(h)
	return p
}

// MarshalJSON writes ACLPermission::dump.
func (p Permission) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Flags int32 `json:"flags"`
	}{int32(p)}) //nolint:gosec // dump_int of the int C++ stores
}

// GranteeType is ACLGranteeType, which of the ACLGranteeTypeEnum grantees a
// grant names. A standalone C++ ACLGranteeType default-constructs to
// GranteeUnknown, which NewGranteeType returns; a default ACLGrant instead
// names a canonical user, which the zero Grant matches.
type GranteeType uint32

// The ACLGranteeTypeEnum values; they are encoded, and index the C++ grantee variant.
const (
	GranteeCanonUser GranteeType = 0
	GranteeEmail     GranteeType = 1
	GranteeGroup     GranteeType = 2
	GranteeUnknown   GranteeType = 3
	GranteeReferer   GranteeType = 4
)

// NewGranteeType returns the ACLGranteeType default, GranteeUnknown.
func NewGranteeType() GranteeType { return GranteeUnknown }

// Encode mirrors ACLGranteeType::encode, ENCODE_START(2, 2).
func (t GranteeType) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 2)
	e.U32(uint32(t))
	e.EndStruct(f)
}

// DecodeGranteeType mirrors ACLGranteeType::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2). Any value is kept.
func DecodeGranteeType(d *denc.Decoder) GranteeType {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	t := GranteeType(d.U32())
	d.EndStruct(h)
	return t
}

// MarshalJSON writes ACLGranteeType::dump.
func (t GranteeType) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type uint32 `json:"type"`
	}{uint32(t)})
}

// The ACLGroupTypeEnum values a group grant names.
const (
	GroupNone               uint32 = 0
	GroupAllUsers           uint32 = 1
	GroupAuthenticatedUsers uint32 = 2
)

// Grant is ACLGrant. C++ holds the grantee as a variant indexed by the
// grantee type; here its alternatives are flattened, and only the fields of
// Type's alternative are meaningful: ID and Name for a canonical user, Email
// for an email grantee, Group for a group, URLSpec for a referer. Encode
// writes the others as empty, as C++ can hold nothing else, and a Type beyond
// GranteeReferer as GranteeUnknown. ID is to_string of the grantee's
// rgw_owner; Encode writes it through parse_owner and to_string, as C++ can
// only write that form.
type Grant struct {
	Type       GranteeType
	ID         string
	Email      string
	Permission Permission
	Name       string
	Group      uint32
	URLSpec    string
}

// kind is ACLGrant::get_type, the index of the variant alternative Type selects.
func (g Grant) kind() GranteeType {
	if g.Type > GranteeReferer {
		return GranteeUnknown
	}
	return g.Type
}

// Encode mirrors ACLGrant::encode, ENCODE_START(5, 3). The uri string, which
// version 2 converted to the group, is always empty.
func (g Grant) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(5, 3)
	k := g.kind()
	k.Encode(e, r)
	e.String(pick(k == GranteeCanonUser, meta.ParseOwner(g.ID).String()))
	e.String("") // uri
	e.String(pick(k == GranteeEmail, g.Email))
	g.Permission.Encode(e, r)
	e.String(pick(k == GranteeCanonUser, g.Name))
	if k == GranteeGroup {
		e.U32(g.Group)
	} else {
		e.U32(GroupNone)
	}
	e.String(pick(k == GranteeReferer, g.URLSpec))
	e.EndStruct(f)
}

// pick returns s when ok, and otherwise the empty string.
func pick(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

// DecodeGrant mirrors ACLGrant::decode, DECODE_START_LEGACY_COMPAT_LEN(5, 3,
// 3), as Squid and later decode it: every version is read with a group field
// and the uri discarded, the URL spec from version 5. Reef read the group
// only above version 1 (Squid's 7a3eb76761c dropped that branch), so a true
// pre-v2 grant misparses here as it does in radosgw. Only the fields of the
// grantee type's alternative survive; the id passes through parse_owner, so
// the decoded ID is its to_string form. A type beyond GranteeReferer decodes
// as GranteeUnknown.
func DecodeGrant(d *denc.Decoder) Grant {
	h := d.BeginStructLegacy(5, 3, 3, 0)
	typ := DecodeGranteeType(d)
	id := d.String()
	_ = d.String() // uri
	email := d.String()
	perm := DecodePermission(d)
	name := d.String()
	group := d.U32()
	var urlSpec string
	if h.Version >= 5 {
		urlSpec = d.String()
	}
	d.EndStruct(h)

	g := Grant{Type: typ, Permission: perm}
	switch typ {
	case GranteeCanonUser:
		g.ID = meta.ParseOwner(id).String()
		g.Name = name
	case GranteeEmail:
		g.Email = email
	case GranteeGroup:
		g.Group = group
	case GranteeReferer:
		g.URLSpec = urlSpec
	default:
		g.Type = GranteeUnknown
	}
	return g
}

// MarshalJSON writes ACLGrant::dump: the type, the fields of its grantee,
// then the permission. The group is dumped as a signed int.
func (g Grant) MarshalJSON() ([]byte, error) {
	type head struct {
		Type GranteeType `json:"type"`
	}
	type tail struct {
		Permission Permission `json:"permission"`
	}
	h := head{g.kind()}
	t := tail{g.Permission}
	var v any
	switch h.Type {
	case GranteeCanonUser:
		v = struct {
			head
			ID   string `json:"id"`
			Name string `json:"name"`
			tail
		}{h, g.ID, g.Name, t}
	case GranteeEmail:
		v = struct {
			head
			Email string `json:"email"`
			tail
		}{h, g.Email, t}
	case GranteeGroup:
		v = struct {
			head
			Group int32 `json:"group"`
			tail
		}{h, int32(g.Group), t} //nolint:gosec // C++ dumps static_cast<int>(group.type)
	case GranteeReferer:
		v = struct {
			head
			URLSpec string `json:"url_spec"`
			tail
		}{h, g.URLSpec, t}
	default:
		v = struct {
			head
			tail
		}{h, t}
	}
	return json.Marshal(v)
}
