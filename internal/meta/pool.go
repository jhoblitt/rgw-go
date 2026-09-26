package meta

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Pool is rgw_pool: a pool name and a RADOS namespace, "name" or "name:ns".
// Its JSON form is the string form, as encode_json(const char*, const
// rgw_pool&) writes it.
type Pool struct{ Name, NS string }

const (
	poolEsc   = '\\'
	poolDelim = ':'
)

// ParsePool parses the string form as rgw_pool::from_str does: a backslash
// escapes the next character, the first unescaped colon ends the name, and a
// second unescaped colon ends the namespace.
func ParsePool(s string) Pool {
	var p Pool
	var rest string
	p.Name, rest = unescapeUntil(s)
	if rest != "" {
		p.NS, _ = unescapeUntil(rest)
	}
	return p
}

// unescapeUntil is rgw_unescape_str with '\\' and ':': it returns s unescaped
// up to the first unescaped colon, and the text after that colon.
func unescapeUntil(s string) (out, rest string) {
	var b strings.Builder
	esc := false
	for i := range len(s) {
		c := s[i]
		switch {
		case !esc && c == poolEsc:
			esc = true
			continue
		case !esc && c == poolDelim:
			return b.String(), s[i+1:]
		}
		b.WriteByte(c)
		esc = false
	}
	return b.String(), ""
}

// escape is rgw_escape_str with '\\' and ':'.
func escape(s string) string {
	if !strings.ContainsAny(s, `\:`) {
		return s
	}
	var b strings.Builder
	for i := range len(s) {
		if s[i] == poolEsc || s[i] == poolDelim {
			b.WriteByte(poolEsc)
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// String is rgw_pool::to_str, the inverse of ParsePool.
func (p Pool) String() string {
	if p.NS == "" {
		return escape(p.Name)
	}
	return escape(p.Name) + ":" + escape(p.NS)
}

// MarshalJSON writes the string form.
func (p Pool) MarshalJSON() ([]byte, error) { return json.Marshal(p.String()) }

// UnmarshalJSON parses the string form.
func (p *Pool) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("meta: pool: %w", err)
	}
	*p = ParsePool(s)
	return nil
}

// Encode mirrors rgw_pool::encode, ENCODE_START(10, 10).
func (p Pool) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(10, 10)
	e.String(p.Name)
	e.String(p.NS)
	e.EndStruct(f)
}

// DecodePool mirrors rgw_pool::decode, DECODE_START_LEGACY_COMPAT_LEN(10, 3, 3).
// Below version 10 the encoding is an old rgw_bucket whose first field was
// the pool name; the rest is skipped by its length, or left unread below
// version 3, which carried none, exactly as the C++ decoder leaves it.
func DecodePool(d *denc.Decoder) Pool {
	h := d.BeginStructLegacy(10, 3, 3, 0)
	var p Pool
	p.Name = d.String()
	if h.Version >= 10 {
		p.NS = d.String()
	}
	d.EndStruct(h)
	return p
}
