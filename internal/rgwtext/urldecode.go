package rgwtext

import "strings"

// URLDecode is radosgw's url_decode (rgw_common.cc:1701-1734 at v19.2.6,
// :1764-1797 at v20.2.4). "%XX" decodes. A '+' is a space in query mode,
// which inQuery starts in and a literal '?' switches on; an escaped '?' does
// not. A '%' with fewer than two bytes after it ends the result there, and
// one followed by a byte that is not a hex digit empties it. A byte above
// 0x7f is no hex digit here; radosgw looks it up outside its hex table
// (docs/exclusions.md, "A byte above 0x7f in a percent-escape is a bad hex
// digit").
func URLDecode(s string, inQuery bool) string {
	if strings.IndexByte(s, '%') < 0 && strings.IndexByte(s, '+') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '%':
			if len(s)-i < 3 {
				return b.String()
			}
			hi, okHi := unhex(s[i+1])
			lo, okLo := unhex(s[i+2])
			if !okHi || !okLo {
				return ""
			}
			b.WriteByte(hi<<4 | lo)
			i += 2
		case c == '+' && inQuery:
			b.WriteByte(' ')
		default:
			if c == '?' {
				inQuery = true
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// unhex is HexTable::to_num for the bytes up to 0x7f.
func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
