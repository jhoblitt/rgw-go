package auth

import (
	"math"
	"strings"
	"time"
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

// tm is the part of struct tm radosgw's date parsing fills and reads.
type tm struct {
	year, mon, mday, hour, min, sec int // year in full; mon from 0
	gmtoff                          int // seconds east of UTC, from %z
}

// cumulativeDays is days_from_1jan's table: the days before each month, in
// a common year and in a leap year.
var cumulativeDays = [2][12]int{
	{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334},
	{0, 31, 60, 91, 121, 152, 182, 213, 244, 274, 305, 335},
}

// daysFrom0 is days_from_0. Its truncating division counts year 0 as 365
// days long, though is_leap calls it a leap year, so year-0 dates land a
// day later than on the proleptic Gregorian calendar.
func daysFrom0(year int) int {
	year--
	return 365*year + year/400 - year/100 + year/4
}

// timegm is internal_timegm (include/timegm.h:24-77 at v19.2.6 and v20.2.4):
// calendar arithmetic that never checks the day against the month, so the
// 31st of April is the 1st of May and second 60 the next minute. strptime
// leaves the month in range, so its normalization is not needed.
func (t tm) timegm() time.Time {
	leap := 0
	if t.year%400 == 0 || t.year%100 != 0 && t.year%4 == 0 {
		leap = 1
	}
	days := daysFrom0(t.year) - daysFrom0(1970) + cumulativeDays[leap][t.mon] + t.mday - 1
	return time.Unix(int64(days)*86400+int64(3600*t.hour+60*t.min+t.sec), 0).UTC()
}

var (
	weekdayNames = [7][2]string{
		{"Sunday", "Sun"},
		{"Monday", "Mon"},
		{"Tuesday", "Tue"},
		{"Wednesday", "Wed"},
		{"Thursday", "Thu"},
		{"Friday", "Fri"},
		{"Saturday", "Sat"},
	}
	monthNames = [12][2]string{
		{"January", "Jan"},
		{"February", "Feb"},
		{"March", "Mar"},
		{"April", "Apr"},
		{"May", "May"},
		{"June", "Jun"},
		{"July", "Jul"},
		{"August", "Aug"},
		{"September", "Sep"},
		{"October", "Oct"},
		{"November", "Nov"},
		{"December", "Dec"},
	}
)

// hasPrefixFold is match_string: strncasecmp in the C locale.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := range len(prefix) {
		if lowerASCII(s[i]) != lowerASCII(prefix[i]) {
			return false
		}
	}
	return true
}

// strptime is glibc's strptime in the C locale (time/strptime_l.c at
// glibc-2.34, the C library of radosgw's el9 images), for the conversions
// radosgw's date formats use: %a %A %b %d %H %m %M %S %y %Y %z. Whitespace in
// the format matches any run of whitespace, an empty one included; any other
// character must match itself. It returns what follows the match.
func strptime(s, format string) (t tm, rest string, ok bool) {
	p := 0
	for f := 0; f < len(format); f++ {
		fc := format[f]
		if isSpace(fc) {
			for p < len(s) && isSpace(s[p]) {
				p++
			}
			continue
		}
		if fc != '%' {
			if p >= len(s) || s[p] != fc {
				return tm{}, "", false
			}
			p++
			continue
		}
		f++
		if f == len(format) {
			return tm{}, "", false
		}
		var v int
		switch format[f] {
		case 'a', 'A':
			p, ok = matchWeekday(s, p)
		case 'b':
			t.mon, p, ok = matchMonth(s, p)
		case 'd':
			t.mday, p, ok = getNumber(s, p, 1, 31, 2)
		case 'H':
			t.hour, p, ok = getNumber(s, p, 0, 23, 2)
		case 'm':
			v, p, ok = getNumber(s, p, 1, 12, 2)
			t.mon = v - 1
		case 'M':
			t.min, p, ok = getNumber(s, p, 0, 59, 2)
		case 'S':
			t.sec, p, ok = getNumber(s, p, 0, 61, 2)
		case 'y':
			v, p, ok = getNumber(s, p, 0, 99, 2)
			t.year = 1900 + v
			if v < 69 {
				t.year += 100
			}
		case 'Y':
			t.year, p, ok = getNumber(s, p, 0, 9999, 4)
		case 'z':
			t.gmtoff, p, ok = matchZone(s, p)
		default:
			ok = false
		}
		if !ok {
			return tm{}, "", false
		}
	}
	return t, s[p:], true
}

// getNumber is strptime's get_number: whitespace is skipped, a digit must
// follow, and more are read while fewer than n are and another could not
// take the value past to.
func getNumber(s string, p, from, to, n int) (val, next int, ok bool) {
	for p < len(s) && isSpace(s[p]) {
		p++
	}
	if p == len(s) || !isDigit(s[p]) {
		return 0, p, false
	}
	for {
		val = val*10 + int(s[p]-'0')
		p++
		n--
		if n == 0 || val*10 > to || p == len(s) || !isDigit(s[p]) {
			break
		}
	}
	return val, p, val >= from && val <= to
}

// matchWeekday is strptime's %a and %A: the longest day name, full or
// abbreviated, in any case. glibc tries the C names twice, and its second
// try of an abbreviation advances the scan position itself (it passes rp,
// not trp, to match_string, strptime_l.c:378), so the days after a matched
// abbreviation are tried where it ends and "SunMon" matches whole.
func matchWeekday(s string, p int) (next int, ok bool) {
	longest := -1
	for _, names := range weekdayNames {
		for _, name := range names {
			if hasPrefixFold(s[p:], name) && p+len(name) > longest {
				longest = p + len(name)
			}
		}
		if hasPrefixFold(s[p:], names[1]) {
			p += len(names[1])
		}
	}
	return longest, longest >= 0
}

// matchMonth is strptime's %b: the longest month name, full or abbreviated,
// in any case.
func matchMonth(s string, p int) (mon, next int, ok bool) {
	mon, longest := -1, -1
	for i, names := range monthNames {
		for _, name := range names {
			if hasPrefixFold(s[p:], name) && p+len(name) > longest {
				mon, longest = i, p+len(name)
			}
		}
	}
	return mon, longest, longest >= 0
}

// matchZone is strptime's %z: whitespace is skipped, then 'Z', or a sign
// and two or four digits, a ':' allowed after the first two, with minutes
// below 60.
func matchZone(s string, p int) (gmtoff, next int, ok bool) {
	for p < len(s) && isSpace(s[p]) {
		p++
	}
	if p < len(s) && s[p] == 'Z' {
		return 0, p + 1, true
	}
	if p == len(s) || s[p] != '+' && s[p] != '-' {
		return 0, p, false
	}
	neg := s[p] == '-'
	p++
	val, n := 0, 0
	for n < 4 && p < len(s) && isDigit(s[p]) {
		val = val*10 + int(s[p]-'0')
		p++
		n++
		if n == 2 && p+1 < len(s) && s[p] == ':' && isDigit(s[p+1]) {
			p++
		}
	}
	switch {
	case n == 2:
		val *= 100
	case n != 4, val%100 >= 60:
		return 0, p, false
	}
	gmtoff = val/100*3600 + val%100*60
	if neg {
		gmtoff = -gmtoff
	}
	return gmtoff, p, true
}
