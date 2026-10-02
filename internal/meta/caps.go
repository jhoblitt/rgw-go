package meta

import (
	"errors"
	"strings"
)

// The RGW_CAP_* bits (rgw_common.h:209-211 at v19.2.6, :231-233 at v20.2.4)
// and RGW_PERM_INVALID (rgw_acl_types.h:40), what rgw_str_to_perm answers for
// a word it does not know.
const (
	CapRead     uint32 = 0x1
	CapWrite    uint32 = 0x2
	CapAll      uint32 = CapRead | CapWrite
	PermInvalid uint32 = 0xFF00
)

// ErrInvalidCap is -ERR_INVALID_CAP: a capability whose type radosgw does not
// know.
var ErrInvalidCap = errors.New("meta: invalid capability")

// capTypes is RGWUserCaps::is_valid_cap_type's list (rgw_common.cc:2085-2101
// at v19.2.6, :2148-2164 at v20.2.4).
var capTypes = map[string]struct{}{
	"user": {}, "users": {}, "buckets": {}, "metadata": {}, "info": {}, "usage": {}, "zone": {},
	"bilog": {}, "mdlog": {}, "datalog": {}, "roles": {}, "user-policy": {}, "amz-cache": {},
	"oidc-provider": {}, "user-info-without-keys": {}, "ratelimit": {}, "accounts": {},
}

// ValidCapType is RGWUserCaps::is_valid_cap_type: whether t names a
// capability type, matched case-sensitively.
func ValidCapType(t string) bool {
	_, ok := capTypes[t]
	return ok
}

// flagDelims are get_str_list's default delimiters (str_list.cc:32).
const flagDelims = ";,= \t"

// parseFlags is rgw_parse_list_of_flags: s split on any of get_str_list's
// delimiters, empty words skipped, each word matched exactly against table
// and unknown words ignored.
func parseFlags(table []flagName, s string) uint32 {
	var v uint32
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(flagDelims, r) }) {
		for _, fl := range table {
			if w == fl.name {
				v |= fl.mask
			}
		}
	}
	return v
}

// cSpace is isspace in the C locale, what rgw_trim_whitespace trims.
const cSpace = " \t\n\v\f\r"

// ParseCap is RGWUserCaps::get_cap (rgw_common.cc:1907-1929 at v19.2.6,
// :1970-1992 at v20.2.4): "type=perm" split at the first '=', the type
// trimmed of whitespace and perm a list of "*", "read" and "write" with
// unknown words ignored, an absent perm being 0. A string without '=' leaves
// the type empty, so it fails as an unknown type does, with ErrInvalidCap.
func ParseCap(s string) (typ string, perm uint32, err error) {
	typ, rest, found := strings.Cut(s, "=")
	if !found {
		return "", 0, ErrInvalidCap
	}
	typ = strings.Trim(typ, cSpace)
	if !ValidCapType(typ) {
		return "", 0, ErrInvalidCap
	}
	return typ, parseFlags(capNames, rest), nil
}

// splitCaps is the loop add_from_string and remove_from_string share: s split
// at each ';', where a trailing ';' ends the string but an empty string, a
// leading ';' or a doubled one yields an empty cap that fails to parse.
func splitCaps(s string) []string {
	caps := strings.Split(s, ";")
	if n := len(caps); n > 1 && caps[n-1] == "" {
		caps = caps[:n-1]
	}
	return caps
}

// AddString is RGWUserCaps::add_from_string (rgw_common.cc:1966-1982 at
// v19.2.6, :2029-2045 at v20.2.4): each ';'-separated cap or-ed into c. The
// first cap that fails to parse stops it with ErrInvalidCap, the caps before
// it already added. A nil *c, which NewUserInfo and DecodeCaps give a user
// with no caps, is allocated by the first cap added.
func (c *Caps) AddString(s string) error {
	for _, one := range splitCaps(s) {
		typ, perm, err := ParseCap(one)
		if err != nil {
			return err
		}
		if *c == nil {
			*c = Caps{}
		}
		(*c)[typ] |= perm
	}
	return nil
}

// RemoveString is RGWUserCaps::remove_from_string (rgw_common.cc:1984-2000 at
// v19.2.6, :2047-2063 at v20.2.4): each ';'-separated cap's bits cleared from
// c and a type left with none deleted; a type c lacks is skipped. Errors stop
// it as they stop AddString.
func (c Caps) RemoveString(s string) error {
	for _, one := range splitCaps(s) {
		typ, perm, err := ParseCap(one)
		if err != nil {
			return err
		}
		old, ok := c[typ]
		if !ok {
			continue
		}
		if old &^= perm; old == 0 {
			delete(c, typ)
		} else {
			c[typ] = old
		}
	}
	return nil
}

// Check is RGWUserCaps::check_cap (rgw_common.cc:2071-2081 at v19.2.6,
// :2134-2144 at v20.2.4): whether c holds typ with every bit of perm. A type
// c lacks fails even a zero perm.
func (c Caps) Check(typ string, perm uint32) bool {
	have, ok := c[typ]
	return ok && have&perm == perm
}

// opTypeMapping is op_type_mapping (rgw_common.cc:2155-2159 at v19.2.6,
// :2218-2222 at v20.2.4): op_type_flags with a leading "*" row.
var opTypeMapping = append([]flagName{{OpTypeAll, "*"}}, opTypeFlags...)

// ParseOpTypeList is rgw_parse_op_type_list (rgw_common.cc:2162-2165 at
// v19.2.6, :2225-2228 at v20.2.4): a list of "*", "read", "write" and
// "delete", unknown words ignored.
func ParseOpTypeList(s string) uint32 { return parseFlags(opTypeMapping, s) }

// OpTypeString is op_type_to_str, the form a user's op_mask takes in its
// JSON: "read, write, delete", or "<none>" for an empty mask.
func OpTypeString(mask uint32) string { return maskString(opTypeFlags, mask) }

// ParseSubuserPerm is rgw_str_to_perm (rgw_common.cc:2696-2710 at v19.2.6,
// :2758-2772 at v20.2.4): "", "read", "write", "readwrite" or "full",
// compared as strcasecmp compares, and PermInvalid for anything else.
func ParseSubuserPerm(s string) uint32 {
	switch {
	case s == "":
		return 0
	case strings.EqualFold(s, "read"):
		return 0x1
	case strings.EqualFold(s, "write"):
		return 0x2
	case strings.EqualFold(s, "readwrite"):
		return 0x3
	case strings.EqualFold(s, "full"):
		return 0xf
	}
	return PermInvalid
}

// PermString is perm_to_str, the form a subuser's permissions take in a
// user's JSON: "read-write", "full-control", or "<none>" for an empty mask.
func PermString(mask uint32) string { return permString(mask) }
