package meta

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// UserID is rgw_user: tenant, id and namespace. Its JSON form is the string
// form, as encode_json(const char*, const rgw_user&) writes it.
type UserID struct {
	Tenant string
	ID     string
	NS     string
}

// ParseUserID parses "id", "tenant$id", "tenant$ns$id" or "$ns$id", as
// rgw_user::from_str does.
func ParseUserID(s string) UserID {
	tenant, rest, ok := strings.Cut(s, "$")
	if !ok {
		return UserID{ID: s}
	}
	if ns, id, ok := strings.Cut(rest, "$"); ok {
		return UserID{Tenant: tenant, NS: ns, ID: id}
	}
	return UserID{Tenant: tenant, ID: rest}
}

// String is rgw_user::to_str, the inverse of ParseUserID.
func (u UserID) String() string {
	switch {
	case u.Tenant != "" && u.NS != "":
		return u.Tenant + "$" + u.NS + "$" + u.ID
	case u.Tenant != "":
		return u.Tenant + "$" + u.ID
	case u.NS != "":
		return "$" + u.NS + "$" + u.ID
	default:
		return u.ID
	}
}

// MarshalJSON writes the string form.
func (u UserID) MarshalJSON() ([]byte, error) { return json.Marshal(u.String()) }

// UnmarshalJSON parses the string form.
func (u *UserID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("meta: user id: %w", err)
	}
	*u = ParseUserID(s)
	return nil
}

// Encode mirrors rgw_user::encode, ENCODE_START(2, 1).
func (u UserID) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(u.Tenant)
	e.String(u.ID)
	e.String(u.NS)
	e.EndStruct(f)
}

// DecodeUserID mirrors rgw_user::decode, DECODE_START(2).
func DecodeUserID(d *denc.Decoder) UserID {
	h := d.BeginStruct(2)
	var u UserID
	u.Tenant = d.String()
	u.ID = d.String()
	if h.Version >= 2 {
		u.NS = d.String()
	}
	d.EndStruct(h)
	return u
}
