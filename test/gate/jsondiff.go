package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Canonical decodes JSON into maps, slices, strings, bools, nil and
// json.Numbers, so two documents compare equal exactly when they hold the
// same values of the same JSON types whatever their key order and whitespace.
func Canonical(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("gate: decoding %s: %w", b, err)
	}
	return v, nil
}

// jsonType names a canonical value's JSON type.
func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// JSONDiff lists the paths at which two canonical documents differ, in type
// or in value, got being rgw-go's and want radosgw-admin's. Numbers compare
// by their text, as both sides print integers.
func JSONDiff(path string, got, want any) []string {
	mismatch := []string{fmt.Sprintf("%s: got %s %v, want %s %v", path, jsonType(got), got, jsonType(want), want)}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return mismatch
		}
		var out []string
		for _, k := range slices.Sorted(maps.Keys(w)) {
			if _, ok := g[k]; !ok {
				out = append(out, fmt.Sprintf("%s.%s: missing, radosgw-admin has %v", path, k, w[k]))
				continue
			}
			out = append(out, JSONDiff(path+"."+k, g[k], w[k])...)
		}
		for _, k := range slices.Sorted(maps.Keys(g)) {
			if _, ok := w[k]; !ok {
				out = append(out, fmt.Sprintf("%s.%s: extra, rgw-go has %v", path, k, g[k]))
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return mismatch
		}
		if len(g) != len(w) {
			return []string{fmt.Sprintf("%s: got %d elements %v, want %d %v", path, len(g), got, len(w), want)}
		}
		var out []string
		for i := range w {
			out = append(out, JSONDiff(fmt.Sprintf("%s[%d]", path, i), g[i], w[i])...)
		}
		return out
	default:
		if jsonType(got) != jsonType(want) {
			return mismatch
		}
		if got != want {
			return []string{fmt.Sprintf("%s: got %v, want %v", path, got, want)}
		}
		return nil
	}
}

// DropPath deletes the key at path from every object of the canonical
// document v it reaches and returns how many it deleted. A "[]" segment
// stands for every element of an array.
func DropPath(v any, path []string) int {
	if len(path) == 0 {
		return 0
	}
	if path[0] == "[]" {
		arr, ok := v.([]any)
		if !ok {
			return 0
		}
		n := 0
		for _, e := range arr {
			n += DropPath(e, path[1:])
		}
		return n
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	if len(path) == 1 {
		if _, ok := obj[path[0]]; !ok {
			return 0
		}
		delete(obj, path[0])
		return 1
	}
	return DropPath(obj[path[0]], path[1:])
}

// DiffJSON compares rgw-go's JSON for v with radosgw-admin's canonical
// document want, once the keys at the paths in absent, which the running
// release's dump does not write, are dropped from rgw-go's. A path is
// dot-separated keys, a "[]" segment standing for every element of an
// array. It returns the differences, a path in absent that names no key in
// rgw-go's document among them.
func DiffJSON(what string, v, want any, absent ...string) ([]string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("gate: marshaling %s: %w", what, err)
	}
	got, err := Canonical(b)
	if err != nil {
		return nil, err
	}
	var diff []string
	for _, path := range absent {
		if DropPath(got, strings.Split(path, ".")) == 0 {
			diff = append(diff, fmt.Sprintf("%s lacks %s, which only an older dump omits", what, path))
		}
	}
	return append(diff, JSONDiff(what, got, want)...), nil
}

// SquidZoneDumpLacks are the RGWZoneParams::dump keys v20.2.4 added:
// v19.2.6's dump in src/rgw/rgw_zone.cc writes none of these pools.
var SquidZoneDumpLacks = []string{"dedup_pool", "bucket_logging_pool", "restore_pool"}

// SquidZoneGroupDumpLacks are the RGWZoneGroupPlacementTier::dump keys
// v20.2.4 added: v19.2.6's dump writes only tier_type, storage_class,
// retain_head_object and s3.
var SquidZoneGroupDumpLacks = []string{
	"placement_targets.[].tier_targets.[].val.allow_read_through",
	"placement_targets.[].tier_targets.[].val.read_through_restore_days",
	"placement_targets.[].tier_targets.[].val.restore_storage_class",
}
