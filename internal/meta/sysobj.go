package meta

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// RootPool is the default rgw_zone_root_pool, rgw_zonegroup_root_pool,
// rgw_realm_root_pool and rgw_period_root_pool. Every object named below
// lives in it, and each holds the binary encoding of its type.
const RootPool = ".rgw.root"

// The object names are the prefixes of src/rgw/driver/rados/config/{zone,
// zonegroup,realm,period}.cc with the default rgw_default_*_info_oid and
// rgw_period_latest_epoch_info_oid options.
const (
	zoneInfoPrefix      = "zone_info."
	zoneNamesPrefix     = "zone_names."
	defaultZoneOID      = "default.zone"
	zoneGroupInfoPrefix = "zonegroup_info."
	zoneGroupNamePrefix = "zonegroups_names."
	defaultZoneGroupOID = "default.zonegroup"
	realmInfoPrefix     = "realms."
	realmNamesPrefix    = "realms_names."
	defaultRealmOID     = "default.realm"
	periodInfoPrefix    = "periods."
	periodLatestEpoch   = ".latest_epoch"
	periodStagingSuffix = ":staging"
)

// ZoneInfoOID names the object holding a zone's ZoneParams.
func ZoneInfoOID(zoneID string) string { return zoneInfoPrefix + zoneID }

// ZoneNameOID names the object mapping a zone name to its id, a NameToID.
func ZoneNameOID(name string) string { return zoneNamesPrefix + name }

// DefaultZoneOID names the object holding a realm's default zone id, a
// DefaultSystemMetaObjInfo. Without a realm the name ends in a dot, as
// default_zone_oid formats it.
func DefaultZoneOID(realmID string) string { return defaultZoneOID + "." + realmID }

// ZoneGroupInfoOID names the object holding a zonegroup's ZoneGroup.
func ZoneGroupInfoOID(id string) string { return zoneGroupInfoPrefix + id }

// ZoneGroupNameOID names the object mapping a zonegroup name to its id.
func ZoneGroupNameOID(name string) string { return zoneGroupNamePrefix + name }

// DefaultZoneGroupOID names the object holding a realm's default zonegroup
// id; like DefaultZoneOID it ends in a dot without a realm.
func DefaultZoneGroupOID(realmID string) string { return defaultZoneGroupOID + "." + realmID }

// RealmOID names the object holding a Realm.
func RealmOID(id string) string { return realmInfoPrefix + id }

// RealmNameOID names the object mapping a realm name to its id.
func RealmNameOID(name string) string { return realmNamesPrefix + name }

// DefaultRealmOID names the object holding the default realm id.
func DefaultRealmOID() string { return defaultRealmOID }

// PeriodOID names the object holding one epoch of a Period, as period_oid
// does: a staging period's name has no epoch.
func PeriodOID(id string, epoch uint32) string {
	if strings.HasSuffix(id, periodStagingSuffix) {
		return periodInfoPrefix + id
	}
	return periodInfoPrefix + id + "." + strconv.FormatUint(uint64(epoch), 10)
}

// PeriodLatestEpochOID names the object holding a period's
// PeriodLatestEpochInfo.
func PeriodLatestEpochOID(id string) string { return periodInfoPrefix + id + periodLatestEpoch }

// NameToID is RGWNameToId, the payload of the *_names.<name> objects.
type NameToID struct {
	ObjID string `json:"obj_id"`
}

// Encode mirrors RGWNameToId::encode, ENCODE_START(1, 1).
func (n NameToID) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(n.ObjID)
	e.EndStruct(f)
}

// DecodeNameToID mirrors RGWNameToId::decode.
func DecodeNameToID(d *denc.Decoder) NameToID {
	h := d.BeginStruct(1)
	n := NameToID{ObjID: d.String()}
	d.EndStruct(h)
	return n
}

// DefaultSystemMetaObjInfo is RGWDefaultSystemMetaObjInfo, the payload of the
// default.* objects.
type DefaultSystemMetaObjInfo struct {
	DefaultID string `json:"default_id"`
}

// Encode mirrors RGWDefaultSystemMetaObjInfo::encode, ENCODE_START(1, 1).
func (i DefaultSystemMetaObjInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(i.DefaultID)
	e.EndStruct(f)
}

// DecodeDefaultSystemMetaObjInfo mirrors RGWDefaultSystemMetaObjInfo::decode.
func DecodeDefaultSystemMetaObjInfo(d *denc.Decoder) DefaultSystemMetaObjInfo {
	h := d.BeginStruct(1)
	i := DefaultSystemMetaObjInfo{DefaultID: d.String()}
	d.EndStruct(h)
	return i
}

// encodeSysObj writes the id and name as RGWSystemMetaObj::encode did,
// ENCODE_START(1, 1). Main dropped the class but keeps the framing in each
// type that embedded it.
func encodeSysObj(e *denc.Encoder, id, name string) {
	f := e.BeginStruct(1, 1)
	e.String(id)
	e.String(name)
	e.EndStruct(f)
}

// decodeSysObj mirrors RGWSystemMetaObj::decode.
func decodeSysObj(d *denc.Decoder) (id, name string) {
	h := d.BeginStruct(1)
	id = d.String()
	name = d.String()
	d.EndStruct(h)
	return id, name
}

// encodeStrings writes a std::list or std::vector of strings.
func encodeStrings(e *denc.Encoder, s []string) { denc.EncodeSlice(e, s, (*denc.Encoder).String) }

// decodeStrings reads a std::list or std::vector of strings.
func decodeStrings(d *denc.Decoder) []string { return denc.DecodeSlice(d, (*denc.Decoder).String) }

// encodeStringSet writes a std::set or flat_set of strings: sorted, without
// duplicates, as the C++ container holds them.
func encodeStringSet(e *denc.Encoder, s []string) { encodeStrings(e, sortedSet(s)) }

// decodeStringSet reads a std::set or flat_set of strings into the order and
// uniqueness the container imposes on insertion.
func decodeStringSet(d *denc.Decoder) []string { return sortedSet(decodeStrings(d)) }

func sortedSet(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return slices.Compact(slices.Sorted(slices.Values(s)))
}

// orEmpty maps nil to an empty slice, so that a C++ container that is empty
// marshals as [] rather than null.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// mapEntry is one element of encode_json's std::map form.
type mapEntry[K, V any] struct {
	Key K `json:"key"`
	Val V `json:"val"`
}

// mapEntries renders a std::map as encode_json does: an array of key and
// value pairs in key order.
func mapEntries[K cmp.Ordered, V any](m map[K]V) []mapEntry[K, V] {
	out := make([]mapEntry[K, V], 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, mapEntry[K, V]{k, m[k]})
	}
	return out
}

// mapValues renders a std::map as encode_json_map does: its values in key
// order.
func mapValues[K cmp.Ordered, V any](m map[K]V) []V {
	out := make([]V, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, m[k])
	}
	return out
}
