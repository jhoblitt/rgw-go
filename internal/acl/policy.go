package acl

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Referer is ACLReferer, a Swift referer grant as the list caches it. C++
// declares ACLReferer::dump but never defines it, and the list's dump leaves
// referers out, so this JSON form is Go's own.
type Referer struct {
	URLSpec string `json:"url_spec"`
	Perm    uint32 `json:"perm"`
}

// Encode mirrors ACLReferer::encode, ENCODE_START(1, 1).
func (r Referer) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(r.URLSpec)
	e.U32(r.Perm)
	e.EndStruct(f)
}

// DecodeReferer mirrors ACLReferer::decode,
// DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1).
func DecodeReferer(d *denc.Decoder) Referer {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	r := Referer{URLSpec: d.String(), Perm: d.U32()}
	d.EndStruct(h)
	return r
}

// GrantEntry is one element of the grant multimap: the key, a canonical
// user's id or an email address or empty for other grantees, and its grant.
type GrantEntry struct {
	Key   string
	Grant Grant
}

// List is RGWAccessControlList. UserMap, GroupMap and RefererList cache the
// permissions the grants give; Grants is the ACLGrantMap multimap, so equal
// keys may repeat. Encode and MarshalJSON write Grants in multimap order,
// sorted by key with equal keys in slice order, without reordering the
// caller's slice, and DecodeList returns them in that order.
type List struct { //nolint:recvcheck // add_grant and remove_canon_user_grant mutate the list, while it encodes by value as every stored type does
	UserMap     map[string]int32
	GroupMap    map[uint32]int32
	RefererList []Referer
	Grants      []GrantEntry
}

// sortedGrants returns the grants in the order the multimap iterates them.
func (l List) sortedGrants() []GrantEntry {
	gs := slices.Clone(l.Grants)
	slices.SortStableFunc(gs, func(a, b GrantEntry) int { return cmp.Compare(a.Key, b.Key) })
	return gs
}

// Encode mirrors RGWAccessControlList::encode, ENCODE_START(4, 3):
// maps_initialized, always true, then the user map, grants, group map and
// referer list.
func (l List) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(4, 3)
	e.Bool(true)
	denc.EncodeMap(e, l.UserMap, (*denc.Encoder).String, (*denc.Encoder).I32)
	denc.EncodeSlice(e, l.sortedGrants(), func(e *denc.Encoder, g GrantEntry) {
		e.String(g.Key)
		g.Grant.Encode(e, r)
	})
	denc.EncodeMap(e, l.GroupMap, (*denc.Encoder).U32, (*denc.Encoder).I32)
	denc.EncodeSlice(e, l.RefererList, func(e *denc.Encoder, x Referer) { x.Encode(e, r) })
	e.EndStruct(f)
}

// DecodeList mirrors RGWAccessControlList::decode,
// DECODE_START_LEGACY_COMPAT_LEN(4, 3, 3). The group map arrived in version
// 2 and the referer list in version 4; a version 1 list stored without its
// maps initialized has them rebuilt from the grants by register_grant.
func DecodeList(d *denc.Decoder) List {
	h := d.BeginStructLegacy(4, 3, 3, 0)
	var l List
	mapsInitialized := d.Bool()
	l.UserMap = denc.DecodeMap(d, (*denc.Decoder).String, (*denc.Decoder).I32)
	l.Grants = decodeGrantMap(d)
	if h.Version >= 2 {
		l.GroupMap = denc.DecodeMap(d, (*denc.Decoder).U32, (*denc.Decoder).I32)
	} else if !mapsInitialized {
		for _, g := range l.Grants {
			registerGrant(&l, g.Grant)
		}
	}
	if h.Version >= 4 {
		l.RefererList = denc.DecodeSlice(d, DecodeReferer)
	}
	d.EndStruct(h)
	return l
}

// decodeGrantMap mirrors decode(std::multimap&): each pair is inserted after
// any equal keys, which a stable sort by key reproduces.
func decodeGrantMap(d *denc.Decoder) []GrantEntry {
	gs := denc.DecodeSlice(d, func(d *denc.Decoder) GrantEntry {
		return GrantEntry{Key: d.String(), Grant: DecodeGrant(d)}
	})
	slices.SortStableFunc(gs, func(a, b GrantEntry) int { return cmp.Compare(a.Key, b.Key) })
	return gs
}

// refererWildcard is RGW_REFERER_WILDCARD.
const refererWildcard = "*"

// registerGrant mirrors RGWAccessControlList::register_grant.
func registerGrant(l *List, g Grant) {
	perm := int32(g.Permission) //nolint:gosec // C++ ors the flags into an int
	switch g.kind() {
	case GranteeCanonUser:
		l.UserMap = orInto(l.UserMap, g.ID, perm)
	case GranteeEmail:
		l.UserMap = orInto(l.UserMap, g.Email, perm)
	case GranteeGroup:
		l.GroupMap = orInto(l.GroupMap, g.Group, perm)
	case GranteeReferer:
		l.RefererList = append(l.RefererList, Referer{URLSpec: g.URLSpec, Perm: uint32(g.Permission)})
		if g.URLSpec == refererWildcard {
			l.GroupMap = orInto(l.GroupMap, GroupAllUsers, perm)
		}
	default:
	}
}

// orInto is m[k] |= perm, creating the map when it is nil, and returns m.
func orInto[K comparable](m map[K]int32, k K, perm int32) map[K]int32 {
	if m == nil {
		m = map[K]int32{}
	}
	m[k] |= perm
	return m
}

// MarshalJSON writes RGWAccessControlList::dump: the user and group maps in
// key order and the grants in multimap order. The referer list is not dumped.
func (l List) MarshalJSON() ([]byte, error) {
	type userEntry struct {
		User string `json:"user"`
		ACL  int32  `json:"acl"`
	}
	type groupEntry struct {
		Group uint32 `json:"group"`
		ACL   int32  `json:"acl"`
	}
	type grantEntry struct {
		ID    string `json:"id"`
		Grant Grant  `json:"grant"`
	}
	v := struct {
		Users  []userEntry  `json:"acl_user_map"`
		Groups []groupEntry `json:"acl_group_map"`
		Grants []grantEntry `json:"grant_map"`
	}{Users: []userEntry{}, Groups: []groupEntry{}, Grants: []grantEntry{}}
	for _, k := range slices.Sorted(maps.Keys(l.UserMap)) {
		v.Users = append(v.Users, userEntry{k, l.UserMap[k]})
	}
	for _, k := range slices.Sorted(maps.Keys(l.GroupMap)) {
		v.Groups = append(v.Groups, groupEntry{k, l.GroupMap[k]})
	}
	for _, g := range l.sortedGrants() {
		v.Grants = append(v.Grants, grantEntry{g.Key, g.Grant})
	}
	return json.Marshal(v)
}

// Owner is ACLOwner. ID is to_string of the owner's rgw_owner: a user's
// string form or an account id. Its JSON form is ACLOwner::dump.
type Owner struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// Encode mirrors ACLOwner::encode, ENCODE_START(3, 2). The id is written
// through parse_owner and to_string, as C++ can only write that form.
func (o Owner) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(3, 2)
	e.String(meta.ParseOwner(o.ID).String())
	e.String(o.DisplayName)
	e.EndStruct(f)
}

// DecodeOwner mirrors ACLOwner::decode, DECODE_START_LEGACY_COMPAT_LEN(3, 2,
// 2). The id passes through parse_owner, so ID is its to_string form.
func DecodeOwner(d *denc.Decoder) Owner {
	h := d.BeginStructLegacy(3, 2, 2, 0)
	o := Owner{ID: meta.ParseOwner(d.String()).String(), DisplayName: d.String()}
	d.EndStruct(h)
	return o
}

// Policy is RGWAccessControlPolicy, the ACL stored with a bucket or object.
// Its JSON form is RGWAccessControlPolicy::dump, which writes the list
// before the owner.
type Policy struct {
	ACL   List  `json:"acl"`
	Owner Owner `json:"owner"`
}

// Encode mirrors RGWAccessControlPolicy::encode, ENCODE_START(2, 2): the
// owner, then the list.
func (p Policy) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 2)
	p.Owner.Encode(e, r)
	p.ACL.Encode(e, r)
	e.EndStruct(f)
}

// DecodePolicy mirrors RGWAccessControlPolicy::decode,
// DECODE_START_LEGACY_COMPAT_LEN(2, 2, 2).
func DecodePolicy(d *denc.Decoder) Policy {
	h := d.BeginStructLegacy(2, 2, 2, 0)
	var p Policy
	p.Owner = DecodeOwner(d)
	p.ACL = DecodeList(d)
	d.EndStruct(h)
	return p
}
