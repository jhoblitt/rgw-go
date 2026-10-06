package auth

import (
	"math"
	"strings"
)

// atoll is the C library's atoll, strtoll in base 10: leading whitespace, a
// sign, then digits up to the first non-digit, saturating at the int64
// bounds as strtoll does.
func atoll(s string) int64 {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	// n accumulates the negated value, so math.MinInt64 is reachable.
	var n int64
	for ; i < len(s) && isDigit(s[i]); i++ {
		d := int64(s[i] - '0')
		if n < (math.MinInt64+d)/10 {
			if neg {
				return math.MinInt64
			}
			return math.MaxInt64
		}
		n = n*10 - d
	}
	switch {
	case neg:
		return n
	case n == math.MinInt64:
		return math.MaxInt64
	}
	return -n
}

// stringToULOK reports whether stringtoul accepts s (rgw_string.h:86-100 at
// v19.2.6 and v20.2.4): strtoul in base 10, so leading whitespace and a sign
// are taken, and the digits must run to the end. It refuses ULONG_MAX, which
// an overflow saturates to and "-1" wraps to. An empty string is 0.
func stringToULOK(s string) bool {
	if s == "" {
		return true
	}
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var n uint64
	overflow := false
	for ; i < len(s) && isDigit(s[i]); i++ {
		d := uint64(s[i] - '0')
		if n > (math.MaxUint64-d)/10 {
			overflow = true
			continue
		}
		n = n*10 + d
	}
	switch {
	case i == start, i != len(s), overflow:
		return false
	case neg:
		return n != 1
	}
	return n != math.MaxUint64
}

// cString is s as a C string: radosgw hands the date parsers a char
// pointer, so a NUL that url_decode produced from %00 ends the text.
func cString(s string) string {
	s, _, _ = strings.Cut(s, "\x00")
	return s
}
