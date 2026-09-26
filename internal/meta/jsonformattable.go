package meta

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// JSONFormattableType is JSONFormattable::Type. The decoder keeps any byte it
// reads; values beyond FormattableObject render as nothing, as FMT_NONE does.
type JSONFormattableType uint8

// The JSONFormattable node types.
const (
	FormattableNone JSONFormattableType = iota
	FormattableValue
	FormattableArray
	FormattableObject
)

// JSONFormattable is ceph's JSONFormattable (src/common/ceph_json.h), a
// recursive JSON-like value that RGW stores as the zone's tier_config. Every
// field is encoded whatever the type, so Value, Array and Object may all hold
// data at once, as the C++ members may.
type JSONFormattable struct {
	Type   JSONFormattableType
	Value  string
	Quoted bool
	Array  []JSONFormattable
	Object map[string]JSONFormattable
}

// Encode mirrors JSONFormattable::encode, ENCODE_START(2, 1).
func (j JSONFormattable) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	e.U8(uint8(j.Type))
	e.String(j.Value)
	denc.EncodeSlice(e, j.Array, func(e *denc.Encoder, x JSONFormattable) { x.Encode(e, r) })
	denc.EncodeMap(e, j.Object, (*denc.Encoder).String, func(e *denc.Encoder, x JSONFormattable) { x.Encode(e, r) })
	e.Bool(j.Quoted)
	e.EndStruct(f)
}

// DecodeJSONFormattable mirrors JSONFormattable::decode. Version 1 carried no
// quoted flag, and its values read as quoted.
func DecodeJSONFormattable(d *denc.Decoder) JSONFormattable {
	h := d.BeginStruct(2)
	var j JSONFormattable
	j.Type = JSONFormattableType(d.U8())
	j.Value = d.String()
	j.Array = denc.DecodeSlice(d, DecodeJSONFormattable)
	j.Object = denc.DecodeMap(d, (*denc.Decoder).String, DecodeJSONFormattable)
	if h.Version >= 2 {
		j.Quoted = d.Bool()
	} else {
		j.Quoted = true
	}
	d.EndStruct(h)
	return j
}

// IsZero reports whether encode_json writes nothing for j, which it does for
// FMT_NONE; a struct field tagged omitzero is then left out, as the C++
// leaves out the key.
func (j JSONFormattable) IsZero() bool {
	_, ok := j.jsonValue()
	return !ok
}

// MarshalJSON renders j as encode_json(const char*, const JSONFormattable&)
// does. The C++ writes an unquoted value bare even when it is not JSON; such
// a value is written here as a string instead. A node that writes nothing
// marshals as null.
func (j JSONFormattable) MarshalJSON() ([]byte, error) {
	v, ok := j.jsonValue()
	if !ok {
		return []byte("null"), nil
	}
	return json.Marshal(v)
}

func (j JSONFormattable) jsonValue() (any, bool) {
	switch j.Type {
	case FormattableValue:
		if !j.Quoted && json.Valid([]byte(j.Value)) {
			return json.RawMessage(j.Value), true
		}
		// A quoted value is a string. Divergence for an unquoted one: the C++
		// writes it bare, so radosgw prints
		// invalid JSON for one that is not JSON, such as the "+5", "1." or
		// "inf" a legacy tier config keeps as numeric text; encoding/json
		// cannot emit that, so it is written as a string.
		return j.Value, true
	case FormattableArray:
		arr := make([]any, 0, len(j.Array))
		for _, x := range j.Array {
			if v, ok := x.jsonValue(); ok {
				arr = append(arr, v)
			}
		}
		return arr, true
	case FormattableObject:
		obj := make(map[string]any, len(j.Object))
		for k, x := range j.Object {
			if v, ok := x.jsonValue(); ok {
				obj[k] = v
			}
		}
		return obj, true
	default:
		return nil, false
	}
}

// errFormattablePath is JSONFormattable::set's -EINVAL, which its only caller
// here, RGWZoneParams::decode, ignores.
var errFormattablePath = errors.New("meta: invalid JSONFormattable path")

// setFormattable mirrors JSONFormattable::set: name is a dotted path whose
// components may index arrays ("a.b[2]", "list[]" to append), and val is
// stored as json_spirit reads it when JSONParser::parse accepts it (see
// parseJSON), else as a value quoted unless it is numeric. A form of val the
// json_spirit transcription rejects is returned wrapping denc.ErrMalformed.
// The C++ walks each path component as it parses it, so the nodes
// before a failing component are created, and so they are here. An escape
// boost's escaped_list_separator rejects, which the C++ throws, is returned
// wrapping denc.ErrMalformed.
func setFormattable(j *JSONFormattable, name, val string) error {
	toks, tokErr := splitEscaped(name)
	var path []fieldEntity
	var parseErr error
	for _, t := range toks {
		ents, ok := parseEntity(t)
		if !ok {
			parseErr = errFormattablePath
			break
		}
		path = append(path, ents...)
	}
	if parseErr == nil {
		// The tokenizer throws while producing the token after toks.
		parseErr = tokErr
	}
	if parseErr != nil {
		if err := walkFormattable(j, path, nil); err != nil {
			return err
		}
		return parseErr
	}
	n, isJSON, err := parseJSON(val)
	if err != nil {
		return err
	}
	return walkFormattable(j, path, func(f *JSONFormattable) {
		if isJSON {
			applyJSON(f, n)
			return
		}
		f.Type = FormattableValue
		f.Value = val
		f.Quoted = !isNumeric(val)
	})
}

// fieldEntity is field_entity in src/common/ceph_json.cc.
type fieldEntity struct {
	isObj  bool
	name   string
	index  int
	append bool
}

// parseEntity mirrors parse_entity: "name", "name[1]", "[2][]" and the like.
func parseEntity(s string) ([]fieldEntity, bool) {
	var out []fieldEntity
	ofs := 0
	for ofs < len(s) {
		open := strings.IndexByte(s[ofs:], '[')
		if open < 0 {
			if ofs != 0 {
				return nil, false
			}
			return append(out, fieldEntity{isObj: true, name: s}), true
		}
		open += ofs
		if open > ofs {
			out = append(out, fieldEntity{isObj: true, name: s[ofs:open]})
		}
		closing := strings.IndexByte(s[open+1:], ']')
		if closing < 0 {
			return nil, false
		}
		closing += open + 1
		idx := s[open+1 : closing]
		ofs = closing + 1
		if idx == "" {
			out = append(out, fieldEntity{append: true})
		} else {
			out = append(out, fieldEntity{index: atoi(idx)})
		}
	}
	return out, true
}

// atoi is C's atoi: optional leading space and sign, then digits up to the
// first non-digit, 0 when there are none.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}

// walkFormattable follows path from j as JSONFormattable::set does, typing
// FMT_NONE nodes as it goes, then calls apply on the node reached. A node
// already holding a value is passed through without consuming anything but
// the path component.
func walkFormattable(j *JSONFormattable, path []fieldEntity, apply func(*JSONFormattable)) error {
	if len(path) == 0 {
		if apply != nil {
			apply(j)
		}
		return nil
	}
	vi, rest := path[0], path[1:]
	if j.Type == FormattableNone {
		if vi.isObj {
			j.Type = FormattableObject
		} else {
			j.Type = FormattableArray
		}
	}
	switch j.Type {
	case FormattableObject:
		if !vi.isObj {
			return errFormattablePath
		}
		if j.Object == nil {
			j.Object = map[string]JSONFormattable{}
		}
		child := j.Object[vi.name]
		err := walkFormattable(&child, rest, apply)
		j.Object[vi.name] = child
		return err
	case FormattableArray:
		if vi.isObj {
			return errFormattablePath
		}
		index := vi.index
		switch {
		case vi.append:
			index = len(j.Array)
		case index < 0:
			index += len(j.Array)
			if index < 0 {
				return errFormattablePath
			}
		}
		if index >= len(j.Array) {
			j.Array = append(j.Array, make([]JSONFormattable, index+1-len(j.Array))...)
		}
		return walkFormattable(&j.Array[index], rest, apply)
	default:
		return walkFormattable(j, rest, apply)
	}
}

// splitEscaped tokenizes s as boost::tokenizer with escaped_list_separator
// ('\\', '.', '"') does: '.' separates, '"' toggles quoting, and '\\' escapes
// a backslash, dot, quote or 'n'. The tokens read before an invalid escape
// are returned with the error.
func splitEscaped(s string) ([]string, error) {
	var toks []string
	var tok strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
			if i == len(s) {
				return toks, fmt.Errorf("%w: tier config key %q ends with an escape", denc.ErrMalformed, s)
			}
			switch s[i] {
			case 'n':
				tok.WriteByte('\n')
			case '\\', '.', '"':
				tok.WriteByte(s[i])
			default:
				return toks, fmt.Errorf("%w: tier config key %q has an unknown escape", denc.ErrMalformed, s)
			}
		case c == '.' && !inQuote:
			toks = append(toks, tok.String())
			tok.Reset()
		case c == '"':
			inQuote = !inQuote
		default:
			tok.WriteByte(c)
		}
	}
	if s != "" {
		toks = append(toks, tok.String())
	}
	return toks, nil
}

// isNumeric is boost::lexical_cast<double> succeeding: a decimal float,
// "inf", "infinity" or "nan" in any case, with an optional sign and no
// surrounding space.
func isNumeric(s string) bool {
	if strings.ContainsAny(s, "_xXpP \t\n\v\f\r") {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// applyJSON mirrors JSONFormattable::decode_json: an array replaces j's
// elements, an object merges into j's members in the key order of JSONObj's
// children multimap (duplicates in document order), and a scalar
// sets the value. The fields the new type does not use are left as they were.
func applyJSON(j *JSONFormattable, n spiritNode) {
	switch n.kind {
	case 'a':
		j.Type = FormattableArray
		j.Array = nil
		for _, c := range n.arr {
			var x JSONFormattable
			applyJSON(&x, c)
			j.Array = append(j.Array, x)
		}
	case 'o':
		j.Type = FormattableObject
		if j.Object == nil {
			j.Object = map[string]JSONFormattable{}
		}
		order := make([]int, len(n.keys))
		for i := range order {
			order[i] = i
		}
		slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(n.keys[a], n.keys[b]) })
		for _, i := range order {
			child := j.Object[n.keys[i]]
			applyJSON(&child, n.values[i])
			j.Object[n.keys[i]] = child
		}
	default:
		j.Type = FormattableValue
		j.Value = n.text
		j.Quoted = n.kind == 's'
	}
}
