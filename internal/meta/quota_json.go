package meta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// ErrBadJSONValue is JSONDecoder::err from decode_json_obj: a member whose
// text is not the number or boolean the field holds.
var ErrBadJSONValue = errors.New("meta: bad JSON value")

// UnmarshalJSON is RGWQuotaInfo::decode_json (rgw_quota.cc:1054-1067 at
// v19.2.6, :1052-1065 at v20.2.4): max_size, or max_size_kb * 1024 when
// max_size is absent; max_objects; check_on_raw; enabled. JSONDecoder's
// decode_json resets an absent member's field to its type's zero value
// (ceph_json.h:334-357 at v19.2.6, :339-362 at v20.2.4), so every field is
// the body's: an object without max_size or max_size_kb has a max_size of
// 0. A value other than an object has no named members. A max_size_kb
// whose size in bytes overflows int64 is refused with ErrBadJSONValue:
// radosgw's multiplication is then undefined behavior, which in practice
// wraps to a negative size, no limit.
func (q *Quota) UnmarshalJSON(b []byte) error {
	m, err := JSONMembers(b)
	if err != nil {
		return err
	}
	var out Quota
	if v, ok := m["max_size"]; ok {
		if out.MaxSize, err = jsonInt64("max_size", v); err != nil {
			return err
		}
	} else if v, ok := m["max_size_kb"]; ok {
		var kb int64
		if kb, err = jsonInt64("max_size_kb", v); err != nil {
			return err
		}
		if kb > math.MaxInt64/1024 || kb < math.MinInt64/1024 {
			return fmt.Errorf("%w: max_size_kb overflows bytes", ErrBadJSONValue)
		}
		out.MaxSize = kb * 1024
	}
	if v, ok := m["max_objects"]; ok {
		if out.MaxObjects, err = jsonInt64("max_objects", v); err != nil {
			return err
		}
	}
	if v, ok := m["check_on_raw"]; ok {
		if out.CheckOnRaw, err = jsonBool("check_on_raw", v); err != nil {
			return err
		}
	}
	if v, ok := m["enabled"]; ok {
		if out.Enabled, err = jsonBool("enabled", v); err != nil {
			return err
		}
	}
	*q = out
	return nil
}

// JSONMember is one member of a JSON object as JSONObj holds it: Raw is the
// value as written, and Text is what JSONObj::get_data returns, a string's
// contents or any other value's text (ceph_json.cc JSONObj::init).
type JSONMember struct {
	Raw  json.RawMessage
	Text string
}

// JSONMembers is the members of the JSON object b by name, the first of a
// repeated name kept, as JSONObj::find_first finds it in the children
// multimap. An array or a scalar has no named members, so its map is
// empty. b must be one JSON value.
func JSONMembers(b []byte) (map[string]JSONMember, error) {
	m := map[string]JSONMember{}
	if t := bytes.TrimLeft(b, " \t\r\n"); len(t) == 0 || t[0] != '{' {
		if !json.Valid(b) {
			return nil, fmt.Errorf("%w: not JSON", ErrBadJSONValue)
		}
		return m, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadJSONValue, err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadJSONValue, err)
		}
		name, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: a member name that is no string", ErrBadJSONValue)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadJSONValue, err)
		}
		if _, seen := m[name]; seen {
			continue
		}
		mem := JSONMember{Raw: raw, Text: string(raw)}
		if len(raw) > 0 && raw[0] == '"' {
			if err := json.Unmarshal(raw, &mem.Text); err != nil {
				return nil, fmt.Errorf("%w: %w", ErrBadJSONValue, err)
			}
		}
		m[name] = mem
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadJSONValue, err)
	}
	return m, nil
}

// jsonInt64 is decode_json_obj(long&), which int64_t selects on LP64
// (src/common/ceph_json.cc:306-330 at v19.2.6 and v20.2.4): strtol over the
// text as a C string, refused when it reads no digits, saturates with
// ERANGE, or leaves anything but white space.
func jsonInt64(name string, v JSONMember) (int64, error) {
	s := rgwtext.CString(v.Text)
	n, end, erange := rgwtext.Strtoll(s)
	if end == 0 || erange || strings.TrimLeft(s[end:], cSpace) != "" {
		return 0, fmt.Errorf("%w: %s", ErrBadJSONValue, name)
	}
	return n, nil
}

// jsonBool is decode_json_obj(bool&): "true" or "false" in any case,
// otherwise an int read as decode_json_obj(int&) reads one, non-zero being
// true.
func jsonBool(name string, v JSONMember) (bool, error) {
	b := []byte(rgwtext.CString(v.Text))
	for i, c := range b {
		b[i] = lowerASCII(c)
	}
	switch string(b) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	n, err := jsonInt64(name, v)
	if err != nil {
		return false, err
	}
	if n > math.MaxInt32 || n < math.MinInt32 {
		return false, fmt.Errorf("%w: %s out of int's range", ErrBadJSONValue, name)
	}
	return n != 0, nil
}
