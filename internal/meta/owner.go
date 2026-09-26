package meta

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Owner is rgw_owner, std::variant<rgw_user, rgw_account_id>: a user when
// User is non-nil, and otherwise the account Account. The zero Owner is thus
// the empty account; C++ default-constructs the empty user, so use
// UserOwner(UserID{}) for that. Its JSON form is the string form, as
// encode_json_impl(const char*, const rgw_owner&) writes it.
type Owner struct {
	User    *UserID
	Account string
}

// UserOwner returns the Owner holding user u.
func UserOwner(u UserID) Owner { return Owner{User: &u} }

// AccountOwner returns the Owner holding account id.
func AccountOwner(id string) Owner { return Owner{Account: id} }

// String is to_string(const rgw_owner&): the user's string form, or the
// account id.
func (o Owner) String() string {
	if o.User != nil {
		return o.User.String()
	}
	return o.Account
}

// The rgw::account::validate_id constants: an account id is "RGW" then 17 digits.
const (
	accountIDPrefix = "RGW"
	accountIDLen    = 20
)

// validAccountID is rgw::account::validate_id.
func validAccountID(s string) bool {
	if len(s) != accountIDLen || !strings.HasPrefix(s, accountIDPrefix) {
		return false
	}
	for _, c := range []byte(s[len(accountIDPrefix):]) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ParseOwner is parse_owner: a valid account id is an account, anything else
// a user in its string form.
func ParseOwner(s string) Owner {
	if validAccountID(s) {
		return AccountOwner(s)
	}
	return UserOwner(ParseUserID(s))
}

// MarshalJSON writes the string form.
func (o Owner) MarshalJSON() ([]byte, error) { return json.Marshal(o.String()) }

// UnmarshalJSON parses the string form as decode_json_obj(rgw_owner&) does.
func (o *Owner) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("meta: owner: %w", err)
	}
	*o = ParseOwner(s)
	return nil
}

// convertedMaxVersion is ceph::converted_variant::converted_max_version: a
// struct_v above it holds the alternative struct_v - 128.
const convertedMaxVersion = 128

// EncodeConverted mirrors ceph::converted_variant::encode, the form
// RGWBucketEntryPoint writes: a user is its bare rgw_user encoding, and an
// account is ENCODE_START(129, 129) around the id string.
func (o Owner) EncodeConverted(e *denc.Encoder, r denc.Release) {
	if o.User != nil {
		o.User.Encode(e, r)
		return
	}
	f := e.BeginStruct(convertedMaxVersion+1, convertedMaxVersion+1)
	e.String(o.Account)
	e.EndStruct(f)
}

// DecodeOwnerConverted mirrors ceph::converted_variant::decode for rgw_owner,
// DECODE_START(129). A struct_v up to 128 is rgw_user's own header, which C++
// rewinds to and decodes again; here its body is read under the header
// already consumed, with rgw_user's DECODE_START(2) compat check. A struct_v
// of 130 or more names no alternative and fails with ErrMalformed.
func DecodeOwnerConverted(d *denc.Decoder) Owner {
	h := d.BeginStruct(convertedMaxVersion + 1)
	if d.Err() != nil {
		return Owner{}
	}
	var o Owner
	switch {
	case h.Version <= convertedMaxVersion:
		if h.Compat > userIDVersion {
			d.Fail(fmt.Errorf("%w: rgw_user struct_v %d compat %d, decoder %d",
				denc.ErrIncompatible, h.Version, h.Compat, userIDVersion))
			return Owner{}
		}
		o = UserOwner(decodeUserIDBody(d, h))
	case h.Version == convertedMaxVersion+1:
		o = AccountOwner(d.String())
	default:
		d.Fail(fmt.Errorf("%w: rgw_owner converted_variant struct_v %d", denc.ErrMalformed, h.Version))
		return Owner{}
	}
	d.EndStruct(h)
	return o
}

// EncodeVersioned mirrors ceph::versioned_variant::encode, the form
// RGWBucketInfo writes: ENCODE_START(i, i) for alternative index i, 0 for a
// user and 1 for an account, around the alternative's own encoding.
func (o Owner) EncodeVersioned(e *denc.Encoder, r denc.Release) {
	if o.User != nil {
		f := e.BeginStruct(0, 0)
		o.User.Encode(e, r)
		e.EndStruct(f)
		return
	}
	f := e.BeginStruct(1, 1)
	e.String(o.Account)
	e.EndStruct(f)
}

// DecodeOwnerVersioned mirrors ceph::versioned_variant::decode for rgw_owner,
// DECODE_START(1). A struct_v of 2 or more names no alternative and fails
// with ErrMalformed.
func DecodeOwnerVersioned(d *denc.Decoder) Owner {
	h := d.BeginStruct(1)
	if d.Err() != nil {
		return Owner{}
	}
	var o Owner
	switch h.Version {
	case 0:
		o = UserOwner(DecodeUserID(d))
	case 1:
		o = AccountOwner(d.String())
	default:
		d.Fail(fmt.Errorf("%w: rgw_owner versioned_variant struct_v %d", denc.ErrMalformed, h.Version))
		return Owner{}
	}
	d.EndStruct(h)
	return o
}
