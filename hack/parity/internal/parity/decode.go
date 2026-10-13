package parity

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// decodeStrict decodes data, one JSON value, into v, and is the only way
// parity reads JSON. It refuses what another reader of the same bytes, a
// person, jq or a JSON library that folds case differently, could read as
// something else: a byte order mark, text that is not UTF-8, anything after
// the value, a member v does not hold, a member named twice, and a member
// whose name differs from v's only in case, which encoding/json would fold
// into v's while the bytes say otherwise.
func decodeStrict(data []byte, v any) error {
	switch {
	case bytes.HasPrefix(data, []byte("\xef\xbb\xbf")):
		return errors.New("a byte order mark before the value")
	case !utf8.Valid(data):
		return errors.New("text that is not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("text after the value")
	}
	if !jsontext.Value(data).IsValid() {
		return errors.New("duplicate member names in an object")
	}
	var in, out any
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	again, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(again, &out); err != nil {
		return err
	}
	if where := uncovered(in, out, ""); where != "" {
		return fmt.Errorf("a member name that differs from the schema's only in case, or a value it does not keep, at %s", where)
	}
	return nil
}

// uncovered returns the path of the first member or element of in, the
// value as its bytes say, that out, the value as decoded and encoded again,
// does not hold the same, or "" when out holds all of in.
func uncovered(in, out any, at string) string {
	switch iv := in.(type) {
	case map[string]any:
		ov, ok := out.(map[string]any)
		if !ok {
			return cmpAt(at)
		}
		for k, v := range iv {
			o, ok := ov[k]
			if !ok {
				return cmpAt(at + "." + k)
			}
			if where := uncovered(v, o, at+"."+k); where != "" {
				return where
			}
		}
		return ""
	case []any:
		ov, ok := out.([]any)
		if !ok || len(ov) != len(iv) {
			return cmpAt(at)
		}
		for i := range iv {
			if where := uncovered(iv[i], ov[i], fmt.Sprintf("%s[%d]", at, i)); where != "" {
				return where
			}
		}
		return ""
	}
	if in != out {
		return cmpAt(at)
	}
	return ""
}

func cmpAt(at string) string {
	if at == "" {
		return "the top"
	}
	return strings.TrimPrefix(at, ".")
}
