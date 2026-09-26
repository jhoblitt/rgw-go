package meta

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// StorageClassStandard is RGW_STORAGE_CLASS_STANDARD, the class an empty
// storage class stands for.
const StorageClassStandard = "STANDARD"

// PlacementRule is rgw_placement_rule: "name" or "name/storage_class", STANDARD omitted.
// Its JSON form is the string form, as encode_json(const char*, const
// rgw_placement_rule&) writes it.
type PlacementRule struct{ Name, StorageClass string }

// ParsePlacementRule parses the string form as rgw_placement_rule::from_str
// does, splitting at the first slash.
func ParsePlacementRule(s string) PlacementRule {
	name, class, _ := strings.Cut(s, "/")
	return PlacementRule{Name: name, StorageClass: class}
}

// String is rgw_placement_rule::to_str: the name alone when the storage class
// is empty or STANDARD, and "name/storage_class" otherwise.
func (p PlacementRule) String() string {
	if p.StorageClass == "" || p.StorageClass == StorageClassStandard {
		return p.Name
	}
	return p.Name + "/" + p.StorageClass
}

// CanonicalStorageClass is rgw_placement_rule::get_storage_class: the storage
// class, or STANDARD when it is empty.
func (p PlacementRule) CanonicalStorageClass() string {
	if p.StorageClass == "" {
		return StorageClassStandard
	}
	return p.StorageClass
}

// MarshalJSON writes the string form.
func (p PlacementRule) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

// UnmarshalJSON parses the string form.
func (p *PlacementRule) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("meta: placement rule: %w", err)
	}
	*p = ParsePlacementRule(s)
	return nil
}

// Encode mirrors rgw_placement_rule::encode: the string form alone, with no
// struct header, for backward compatibility.
func (p PlacementRule) Encode(e *denc.Encoder, _ denc.Release) { e.String(p.String()) }

// DecodePlacementRule mirrors rgw_placement_rule::decode.
func DecodePlacementRule(d *denc.Decoder) PlacementRule {
	return ParsePlacementRule(d.String())
}
