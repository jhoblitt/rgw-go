package rgwtext

import "math"

// isSpace is isspace in the C locale, which radosgw never leaves.
func isSpace(c byte) bool {
	return c == ' ' || '\t' <= c && c <= '\r'
}

// scan reads s as glibc's strto* functions read base 10: white space, an
// optional sign, then digits. mag is the digits' value, saturated at
// math.MaxUint64 when overflow is set. end is the index past the digits, and
// 0 when there are none, as strto* then leave their end pointer at the
// string's start. A NUL is none of these, so s need not be cut at one first.
func scan(s string) (neg bool, mag uint64, overflow bool, end int) {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	for ; i < len(s) && '0' <= s[i] && s[i] <= '9'; i++ {
		d := uint64(s[i] - '0')
		if overflow || mag > (math.MaxUint64-d)/10 {
			mag, overflow = math.MaxUint64, true
			continue
		}
		mag = mag*10 + d
	}
	if i == start {
		return false, 0, false, 0
	}
	return neg, mag, overflow, i
}

// Strtoll is glibc's strtoll(s, &end, 10), and its strtol, which is the
// same function on LP64: v saturates at the int64 bounds, setting erange as
// strtoll sets ERANGE, and end is the index past the digits, 0 when there
// are none.
func Strtoll(s string) (v int64, end int, erange bool) {
	neg, mag, overflow, end := scan(s)
	switch {
	case neg && (overflow || mag > 1<<63):
		return math.MinInt64, end, true
	case neg:
		return -int64(mag-1) - 1, end, false //nolint:gosec // mag is at most 1<<63 here
	case overflow || mag > math.MaxInt64:
		return math.MaxInt64, end, true
	default:
		return int64(mag), end, false //nolint:gosec // mag is at most MaxInt64 here
	}
}

// Strtoull is glibc's strtoull(s, &end, 10), and its strtoul, which is the
// same function on LP64: a minus negates the value in unsigned arithmetic,
// and an overflow of either sign saturates at math.MaxUint64, setting erange
// as strtoull sets ERANGE. end is the index past the digits, 0 when there
// are none.
func Strtoull(s string) (v uint64, end int, erange bool) {
	neg, mag, overflow, end := scan(s)
	switch {
	case overflow:
		return math.MaxUint64, end, true
	case neg:
		return -mag, end, false
	default:
		return mag, end, false
	}
}

// Atoll is glibc's atoll: strtoll's value, 0 when s has no digits.
func Atoll(s string) int64 {
	v, _, _ := Strtoll(s)
	return v
}

// StringToLL is stringtoll (rgw_string.h:38-52 at v19.2.6 and v20.2.4):
// strtoll on s as a C string. ok is false for LLONG_MAX, which an overflow
// saturates to, and for any byte after the digits, so for a non-empty string
// with none; an empty string is 0.
func StringToLL(s string) (v int64, ok bool) {
	s = CString(s)
	v, end, _ := Strtoll(s)
	if v == math.MaxInt64 || end != len(s) {
		return 0, false
	}
	return v, true
}

// StringToL is stringtol (rgw_string.h:70-84 at v19.2.6 and v20.2.4):
// stringtoll's checks on strtol's 64-bit long, then a cast to int32_t.
func StringToL(s string) (v int32, ok bool) {
	n, ok := StringToLL(s)
	return int32(n), ok //nolint:gosec // stringtol casts its long to int32_t
}

// StringToULL is stringtoull (rgw_string.h:54-68 at v19.2.6 and v20.2.4):
// strtoull on s as a C string. ok is false for ULLONG_MAX, which an overflow
// saturates to and "-1" negates to, and for any byte after the digits, so
// for a non-empty string with none; an empty string is 0.
func StringToULL(s string) (v uint64, ok bool) {
	s = CString(s)
	v, end, _ := Strtoull(s)
	if v == math.MaxUint64 || end != len(s) {
		return 0, false
	}
	return v, true
}

// StringToUL is stringtoul (rgw_string.h:86-100 at v19.2.6 and v20.2.4):
// stringtoull's checks on strtoul's 64-bit unsigned long, then a cast to
// uint32_t.
func StringToUL(s string) (v uint32, ok bool) {
	n, ok := StringToULL(s)
	return uint32(n), ok //nolint:gosec // stringtoul casts its unsigned long to uint32_t
}

// StrictStrtoll is ceph's strict_strtoll in base 10 (common/strtol.cc:42-59
// at v19.2.6 and v20.2.4): strtoll over all of s, refused when it reads no
// digits, leaves any byte after them, or sets ERANGE.
func StrictStrtoll(s string) (v int64, ok bool) {
	v, end, erange := Strtoll(s)
	if end == 0 || end != len(s) || erange {
		return 0, false
	}
	return v, true
}

// ScanInt is glibc sscanf's %d: white space, an optional sign and at least
// one digit, converted as strtol converts them, saturating at the bounds of a
// 64-bit long, then stored into an int. rest follows the digits.
func ScanInt(s string) (v int32, rest string, ok bool) {
	n, end, _ := Strtoll(s)
	if end == 0 {
		return 0, s, false
	}
	return int32(n), s[end:], true //nolint:gosec // %d stores an int
}
