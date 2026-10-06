// Package strptime transcribes glibc's strptime in the C locale and ceph's
// internal_timegm, the date parsing radosgw does through the C library, for
// the packages that must read dates as radosgw reads them.
package strptime

import "time"

// Tm is the part of struct tm radosgw's date parsing fills and reads.
type Tm struct {
	Year   int // in full
	Mon    int // from 0
	Mday   int
	Hour   int
	Min    int
	Sec    int
	Gmtoff int // seconds east of UTC, from %z
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

// Timegm is internal_timegm (include/timegm.h:24-77 at v19.2.6 and v20.2.4):
// calendar arithmetic that never checks the day against the month, so the
// 31st of April is the 1st of May and second 60 the next minute. strptime
// leaves the month in range, so its normalization is not needed. It ignores
// Gmtoff, as internal_timegm does.
func (t Tm) Timegm() time.Time {
	leap := 0
	if t.Year%400 == 0 || t.Year%100 != 0 && t.Year%4 == 0 {
		leap = 1
	}
	days := daysFrom0(t.Year) - daysFrom0(1970) + cumulativeDays[leap][t.Mon] + t.Mday - 1
	return time.Unix(int64(days)*86400+int64(3600*t.Hour+60*t.Min+t.Sec), 0).UTC()
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

// isSpace is isspace in the C locale.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

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

// Parse is glibc's strptime in the C locale (time/strptime_l.c at
// glibc-2.34, the C library of radosgw's el9 images) on s, starting from the
// fields of t, as a caller that passes the same struct tm twice does. It
// knows the conversions radosgw's date formats use, and the ones a format
// built from request text can name that need no locale data: %a %A %b %B %h
// %d %e %H %m %M %S %y %Y %z %n %t %%. Any other character after a % fails,
// including a flag, width or E/O modifier on a listed conversion; glibc runs
// those modified forms and %C %D %F %G %I %R %T %U %V %W %X %Z %c %g %j %k
// %l %p %r %s %u %w %x, and fails on the rest too. Whitespace in the format
// matches any run of whitespace, an empty one included; any other character
// must match itself. It returns what follows the match.
func Parse(t Tm, s, format string) (out Tm, rest string, ok bool) {
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
				return Tm{}, "", false
			}
			p++
			continue
		}
		f++
		if f == len(format) {
			return Tm{}, "", false
		}
		var v int
		switch format[f] {
		case 'a', 'A':
			p, ok = matchWeekday(s, p)
		case 'b', 'B', 'h':
			t.Mon, p, ok = matchMonth(s, p)
		case 'd', 'e':
			t.Mday, p, ok = getNumber(s, p, 1, 31, 2)
		case 'H':
			t.Hour, p, ok = getNumber(s, p, 0, 23, 2)
		case 'm':
			v, p, ok = getNumber(s, p, 1, 12, 2)
			t.Mon = v - 1
		case 'M':
			t.Min, p, ok = getNumber(s, p, 0, 59, 2)
		case 'S':
			t.Sec, p, ok = getNumber(s, p, 0, 61, 2)
		case 'y':
			v, p, ok = getNumber(s, p, 0, 99, 2)
			t.Year = 1900 + v
			if v < 69 {
				t.Year += 100
			}
		case 'Y':
			t.Year, p, ok = getNumber(s, p, 0, 9999, 4)
		case 'z':
			t.Gmtoff, p, ok = matchZone(s, p)
		case 'n', 't':
			for p < len(s) && isSpace(s[p]) {
				p++
			}
			ok = true
		case '%':
			ok = p < len(s) && s[p] == '%'
			if ok {
				p++
			}
		default:
			ok = false
		}
		if !ok {
			return Tm{}, "", false
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

// matchMonth is strptime's %b, %B and %h: the longest month name, full or
// abbreviated, in any case.
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
