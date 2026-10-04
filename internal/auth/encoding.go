package auth

import (
	"strings"
	"time"
)

// isSpace is isspace in the C locale, which radosgw never leaves.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlnum(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// asciiLower is ::tolower over every byte, which in the C locale changes
// only A to Z.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		b[i] = lowerASCII(c)
	}
	return string(b)
}

// unhex is HexTable::to_num for the hex digits. radosgw indexes the table
// with a signed char, so a byte above 0x7f reads outside it
// (rgw_common.cc:1690-1692 at v19.2.6, :1753-1755 at v20.2.4); here such a
// byte is no hex digit.
func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// urlDecode is url_decode (rgw_common.cc:1701-1734 at v19.2.6, :1764-1797 at
// v20.2.4): '+' is a space only in query mode, which a literal '?' switches
// on; a bad hex digit empties the whole result; a truncated escape ends
// decoding.
func urlDecode(s string, inQuery bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			if inQuery && c == '+' {
				b.WriteByte(' ')
				continue
			}
			if c == '?' {
				inQuery = true
			}
			b.WriteByte(c)
			continue
		}
		if len(s)-i < 3 {
			break
		}
		hi, okHi := unhex(s[i+1])
		lo, okLo := unhex(s[i+2])
		if !okHi || !okLo {
			return ""
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String()
}

const upperHex = "0123456789ABCDEF"

// aws4URIEncode is aws4_uri_encode (rgw_auth_s3.h:555-590 at v19.2.6,
// :558-593 at v20.2.4): alphanumerics, '-', '_', '.', '~' and, unless
// encodeSlash, '/' pass; every other byte is rgw_uri_escape_char's %XX in
// upper case.
func aws4URIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		switch {
		case isAlnum(c), c == '-', c == '_', c == '.', c == '~', c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		}
	}
	return b.String()
}

// aws4Recode is aws4_uri_recode: url_decode outside query mode, then
// aws4_uri_encode.
func aws4Recode(s string, encodeSlash bool) string {
	return aws4URIEncode(urlDecode(s, false), encodeSlash)
}

// trimSpace is rgw_trim_whitespace.
func trimSpace(s string) string {
	for s != "" && isSpace(s[0]) {
		s = s[1:]
	}
	for s != "" && isSpace(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

// collapseSpace is boost::trim_all: trimSpace, then each inner run of
// whitespace becomes the run's first character, since boost formats each run
// with head_finder(1), not with a space; "a\t b" keeps its tab.
func collapseSpace(s string) string {
	s = trimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b.WriteByte(s[i])
		if isSpace(s[i]) {
			for i+1 < len(s) && isSpace(s[i+1]) {
				i++
			}
		}
	}
	return b.String()
}

// isBase64Charset is is_base64_for_content_md5 over every byte: an
// alphanumeric, whitespace, '+', '/' or '='.
func isBase64Charset(s string) bool {
	for i := range len(s) {
		c := s[i]
		if !isAlnum(c) && !isSpace(c) && c != '+' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}

// checkStrEnd is check_str_end: only whitespace remains.
func checkStrEnd(rest string) bool { return trimSpace(rest) == "" }

// checkGMTEnd is check_gmt_end: something remains, and past any whitespace
// it starts with GMT or UTC; what follows is never read.
func checkGMTEnd(rest string) bool {
	if rest == "" {
		return false
	}
	for rest != "" && isSpace(rest[0]) {
		rest = rest[1:]
	}
	return strings.HasPrefix(rest, "GMT") || strings.HasPrefix(rest, "UTC")
}

// parseISO8601Basic is parse_iso8601 with extended_format false
// (rgw_common.cc:599-659 at v19.2.6, :612-672 at v20.2.4): strptime's
// "%Y%m%dT%H%M%S", then, once trimmed, nothing, "Z", or '.' and a fraction
// stringtoul accepts, and 'Z'. The fraction is dropped: radosgw's auth paths
// pass no pns, or build the header time from whole seconds.
func parseISO8601Basic(s string) (time.Time, bool) {
	t, _, ok := iso8601BasicDate(s)
	return t, ok
}

// iso8601BasicDate is parseISO8601Basic that also returns the year as
// strptime read it, before internal_timegm carries a second 60 into the next
// year.
func iso8601BasicDate(s string) (t time.Time, year int, ok bool) {
	fields, rest, ok := strptime(cString(s), "%Y%m%dT%H%M%S")
	if !ok {
		return time.Time{}, 0, false
	}
	rest = trimSpace(rest)
	if rest != "" && rest != "Z" && (rest[0] != '.' || rest[len(rest)-1] != 'Z' || !stringToULOK(rest[1:len(rest)-1])) {
		return time.Time{}, 0, false
	}
	return fields.timegm(), fields.year, true
}

// rfc2616Forms are parse_rfc2616's attempts in its order, each with the
// check it makes of what strptime leaves (rgw_common.cc:566-597 at v19.2.6,
// :579-610 at v20.2.4).
var rfc2616Forms = []struct {
	format string
	end    func(string) bool
}{
	{"%A, %d-%b-%y %H:%M:%S ", checkGMTEnd},   // RFC 850
	{"%a %b %d %H:%M:%S %Y", checkStrEnd},     // asctime
	{"%a, %d %b %Y %H:%M:%S ", checkGMTEnd},   // RFC 1123
	{"%a, %d %b %Y %H:%M:%S %z", checkStrEnd}, // RFC 1123 with a numeric zone
}

// parseRFC2616 is parse_rfc2616 (rgw_common.cc:594-597 at v19.2.6, :607-610
// at v20.2.4). The numeric zone is applied, as
// rgw_create_s3_canonical_header subtracts tm_gmtoff from the header time
// (rgw_auth_s3.cc:241-242 at v19.2.6).
func parseRFC2616(s string) (time.Time, bool) {
	t, _, ok := rfc2616Date(s)
	return t, ok
}

// rfc2616Date is parseRFC2616 that also returns the year as strptime read it,
// before internal_timegm's carries and the zone offset move the instant.
func rfc2616Date(s string) (t time.Time, year int, ok bool) {
	s = cString(s)
	for _, form := range rfc2616Forms {
		if fields, rest, parsed := strptime(s, form.format); parsed && form.end(rest) {
			return fields.timegm().Add(-time.Duration(fields.gmtoff) * time.Second), fields.year, true
		}
	}
	return time.Time{}, 0, false
}
