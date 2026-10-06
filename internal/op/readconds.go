package op

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// ParseRange is RGWGetObj::parse_range (rgw_op.cc:160-224 at v19.2.6,
// :203-267 at v20.2.4) on a Range header value. It returns ofs and end as
// radosgw holds them: end is -1 for an open range, and ofs is negative for a
// suffix range. partial reports that a range was found, which radosgw answers
// with 206. A value whose unit is not bytes is no range, the whole object
// with partial false; one radosgw refuses is ErrInvalidRange. Like radosgw it
// takes "bytes=" anywhere in the value, reads each bound with atoll, so a
// second range is dropped and a non-number reads as 0, and compares the unit
// from the value's first byte however much whitespace precedes the unit
// (docs/ceph-upstream-bugs.md).
func ParseRange(value string) (ofs, end int64, partial bool, err error) {
	rs := rgwtext.CString(value)
	end = -1
	if i := strings.Index(rs, "bytes="); i >= 0 {
		rs = rs[i+len("bytes="):]
	} else {
		pos := 0
		for isCSpace(byteAt(rs, pos)) {
			pos++
		}
		e := pos
		for isCAlpha(byteAt(rs, e)) {
			e++
		}
		if !strncaseEqual(rs, "bytes", e-pos) {
			return 0, -1, false, nil
		}
		for isCSpace(byteAt(rs, e)) {
			e++
		}
		if byteAt(rs, e) != '=' {
			return 0, -1, false, nil
		}
		rs = rs[e+1:]
	}
	ofsStr, endStr, found := strings.Cut(rs, "-")
	if !found {
		return 0, -1, false, ErrInvalidRange
	}
	if endStr != "" {
		end = atoll(endStr)
		if end < 0 {
			return 0, -1, false, ErrInvalidRange
		}
	}
	if ofsStr != "" {
		ofs = atoll(ofsStr)
	} else {
		// RFC 2616's suffix-byte-range-spec. radosgw negates whatever end
		// holds, so "bytes=-" takes its initial -1 and starts at byte 1.
		ofs, end = -end, -1
	}
	if end >= 0 && end < ofs {
		return 0, -1, false, ErrInvalidRange
	}
	return ofs, end, true, nil
}

// RangeToOfs is RGWRados::Object::Read::range_to_ofs
// (driver/rados/rgw_rados.cc:7002-7022 at v19.2.6, :7851-7871 at v20.2.4): it
// resolves a parsed range against an object of size bytes to its first and
// last byte, and refuses with ErrInvalidRange a range that starts at or past
// the end of a non-empty object. An empty object has no last byte, where
// radosgw leaves end at -1 and sends nothing; RangeToOfs returns its offset
// for both, and the caller takes the length as 0.
func RangeToOfs(size uint64, ofs, end int64) (first, last uint64, err error) {
	n := int64(size) //nolint:gosec // radosgw converts the size to off_t as well
	if ofs < 0 {
		ofs = max(ofs+n, 0)
		end = n - 1
	} else if end < 0 {
		end = n - 1
	}
	if size > 0 {
		if ofs >= n {
			return 0, 0, ErrInvalidRange
		}
		end = min(end, n-1)
	}
	if end < ofs {
		return uint64(ofs), uint64(ofs), nil //nolint:gosec // ofs is not negative here
	}
	return uint64(ofs), uint64(end), nil //nolint:gosec // 0 <= ofs <= end here
}

// ParseHTTPTime is parse_time (rgw_common.cc:702-715 at v19.2.6, :715-728 at
// v20.2.4), which reads If-Modified-Since and If-Unmodified-Since: glibc
// strptime with RFC 850's, asctime's and RFC 1123's formats, the last with a
// GMT or UTC zone or a numeric offset, then ISO 8601 as parse_iso8601 takes
// it. Anything else is ErrInvalidArgument. It keeps radosgw's arithmetic: a
// numeric offset is parsed but not applied, as internal_timegm ignores it,
// and the seconds pass through utime_t's 32 bits, so a date before 1970 or
// after 2106-02-07T06:28:15Z wraps (docs/ceph-upstream-bugs.md).
func ParseHTTPTime(s string) (time.Time, error) {
	s = rgwtext.CString(s)
	tm, ok := parseRFC2616(s)
	var ns uint32
	if !ok {
		tm, ns, ok = parseISO8601(s)
	}
	if !ok {
		return time.Time{}, fmt.Errorf("%w: %q is not a date radosgw parses", ErrInvalidArgument, s)
	}
	sec, nsec := utime(internalTimegm(tm), ns)
	return time.Unix(int64(sec), int64(nsec)).UTC(), nil
}

// CheckReadConditions is RGWRados::Object::Read::prepare's conditional block
// (driver/rados/rgw_rados.cc:6945-6990 at v19.2.6, :7793-7839 at v20.2.4) as
// a copy applies it to its source state st, with the dates as
// RGWCopyObj::init_common parses them through parse_time, ErrInvalidArgument
// when one does not parse. Each condition is nil when its header is absent;
// one present with an empty value is a condition, an empty date failing to
// parse and an empty If-Match matching no ETag, as in radosgw.
func CheckReadConditions(st *ObjectState, ifModifiedSince, ifUnmodifiedSince, ifMatch, ifNoneMatch *string) error {
	c, err := parseReadConds(ifModifiedSince, ifUnmodifiedSince, ifMatch, ifNoneMatch)
	if err != nil {
		return err
	}
	return c.check(st)
}

// readConds are a copy's source conditions once init_common has parsed the
// dates; nil fields are absent headers.
type readConds struct {
	since, unmodSince    *time.Time
	ifMatch, ifNoneMatch *string
}

// parseReadConds is init_common's parse of the two dates.
func parseReadConds(ifModifiedSince, ifUnmodifiedSince, ifMatch, ifNoneMatch *string) (readConds, error) {
	c := readConds{ifMatch: ifMatch, ifNoneMatch: ifNoneMatch}
	for _, d := range []struct {
		in  *string
		out **time.Time
	}{{ifModifiedSince, &c.since}, {ifUnmodifiedSince, &c.unmodSince}} {
		if d.in == nil {
			continue
		}
		t, err := ParseHTTPTime(*d.in)
		if err != nil {
			return readConds{}, err
		}
		*d.out = &t
	}
	return c, nil
}

// check is Read::prepare's comparisons on st: the dates against its mtime in
// whole seconds, If-None-Match turning If-Modified-Since off and If-Match
// turning If-Unmodified-Since off; then an unquoted If-Match must start with
// the stored ETag and an If-None-Match must not, "*" being no wildcard. A
// state without the ETag attr fails get_attr with -ENODATA, which the S3
// error table lacks.
func (c readConds) check(st *ObjectState) error {
	mtime := st.Mtime.Unix()
	if c.since != nil && c.ifNoneMatch == nil && c.since.Unix() >= mtime {
		return ErrNotModified
	}
	if c.unmodSince != nil && c.ifMatch == nil && c.unmodSince.Unix() < mtime {
		return ErrPreconditionFailed
	}
	if c.ifMatch == nil && c.ifNoneMatch == nil {
		return nil
	}
	etag, ok := st.Attrs[meta.AttrETag]
	if !ok {
		return fmt.Errorf("%w: %s has no etag to compare", ErrUnknown, st.Key.Name)
	}
	if c.ifMatch != nil && !strings.HasPrefix(Unquote(*c.ifMatch), string(etag)) {
		return ErrPreconditionFailed
	}
	if c.ifNoneMatch != nil && strings.HasPrefix(Unquote(*c.ifNoneMatch), string(etag)) {
		return ErrNotModified
	}
	return nil
}

// Unquote is rgw_string_unquote (rgw_common.cc:518-533 at v19.2.6, :531-546
// at v20.2.4): a value that opens with a double quote and, past any trailing
// spaces, closes with one loses both quotes and those spaces.
func Unquote(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	n := len(s)
	for n > 2 && s[n-1] == ' ' {
		n--
	}
	if s[n-1] != '"' {
		return s
	}
	return s[1 : n-1]
}

// cNumber is what glibc's strtoll and strtoul scan in base 10: leading
// whitespace, an optional sign, then digits. v is the digits' value, saturated
// at math.MaxUint64 when overflow is set; digits is how many there were, and
// end the index past them.
type cNumber struct {
	v             uint64
	neg, overflow bool
	digits, end   int
}

func scanCNumber(s string) cNumber {
	var n cNumber
	i := 0
	for i < len(s) && isCSpace(s[i]) {
		i++
	}
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		n.neg = s[i] == '-'
		i++
	}
	for ; i < len(s) && isCDigit(s[i]); i++ {
		n.digits++
		d := uint64(s[i] - '0')
		if n.overflow || n.v > (math.MaxUint64-d)/10 {
			n.v, n.overflow = math.MaxUint64, true
			continue
		}
		n.v = n.v*10 + d
	}
	n.end = i
	return n
}

// atoll is glibc's atoll, strtoll in base 10: 0 when there are no digits,
// saturating at the int64 bounds.
func atoll(s string) int64 {
	n := scanCNumber(s)
	switch {
	case n.neg && n.v > math.MaxInt64:
		return math.MinInt64
	case n.neg:
		return -int64(n.v) //nolint:gosec // n.v <= math.MaxInt64 here
	case n.v > math.MaxInt64:
		return math.MaxInt64
	default:
		return int64(n.v) //nolint:gosec // n.v <= math.MaxInt64 here
	}
}

// strtoul is glibc's strtoul in base 10 as radosgw's stringtoul calls it
// (rgw_string.h:86-99 at v19.2.6 and v20.2.4): a minus negates in unsigned
// arithmetic, and the rest of the string must be digits. ok is false where
// stringtoul refuses: a byte after the digits, a non-empty string with no
// digits, or a result of ULONG_MAX, which an overflow saturates to. The
// empty string is 0.
func strtoul(s string) (v uint64, ok bool) {
	n := scanCNumber(s)
	switch {
	case n.digits == 0:
		// No digits: strtoul leaves its end pointer at the string's start.
		return 0, s == ""
	case n.end < len(s), n.overflow:
		return 0, false
	}
	v = n.v
	if n.neg {
		v = -v
	}
	return v, v != math.MaxUint64
}

// fromBase64 is rgw::from_base64 (rgw_b64.h:61-80 at v19.2.6 and v20.2.4),
// which decodes the SSE-C key headers: the trailing '=' are dropped, and
// boost's decoder skips whitespace, reads any other '=' as a zero and drops
// the bits that do not fill a byte. ok is false where boost throws: on a byte
// outside the alphabet, and on input that ends in whitespace or one character
// into a group of four.
func fromBase64(s string) (out []byte, ok bool) {
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, true
	}
	if isCSpace(s[len(s)-1]) {
		return nil, false
	}
	var acc uint32
	bits, n := 0, 0
	out = make([]byte, 0, len(s)*3/4)
	for i := range len(s) {
		c := s[i]
		if isCSpace(c) {
			continue
		}
		v, valid := base64Value(c)
		if !valid {
			return nil, false
		}
		n++
		acc = acc<<6 | uint32(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits)) //nolint:gosec // the 8 bits above the leftover ones
			acc &= 1<<bits - 1
		}
	}
	if n%4 == 1 {
		return nil, false
	}
	return out, true
}

// base64Value is boost's binary_from_base64 table: the standard alphabet,
// and '=' as 0.
func base64Value(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case isCDigit(c):
		return c - '0' + 52, true
	case c == '+':
		return 62, true
	case c == '/':
		return 63, true
	case c == '=':
		return 0, true
	default:
		return 0, false
	}
}

// cTM is the struct tm fields parse_time's formats fill. year is tm_year +
// 1900 and mon is 0-based, as in struct tm; zero is what memset leaves.
type cTM struct {
	year, mon, mday, hour, minute, sec int
}

// parseRFC2616 is parse_rfc2616 (rgw_common.cc:566-597 at v19.2.6, :579-610
// at v20.2.4): RFC 850, asctime, RFC 1123 with a GMT or UTC zone, then RFC
// 1123 with a numeric offset, each from a zeroed struct tm.
func parseRFC2616(s string) (cTM, bool) {
	if tm, rest, ok := strptime(s, "%A, %d-%b-%y %H:%M:%S "); ok && checkGMTEnd(rest) {
		return tm, true
	}
	if tm, rest, ok := strptime(s, "%a %b %d %H:%M:%S %Y"); ok && checkStrEnd(rest) {
		return tm, true
	}
	if tm, rest, ok := strptime(s, "%a, %d %b %Y %H:%M:%S "); ok && checkGMTEnd(rest) {
		return tm, true
	}
	if tm, rest, ok := strptime(s, "%a, %d %b %Y %H:%M:%S %z"); ok && checkStrEnd(rest) {
		return tm, true
	}
	return cTM{}, false
}

// checkGMTEnd is check_gmt_end: something follows, and past any whitespace
// it starts with GMT or UTC, whatever comes after.
func checkGMTEnd(rest string) bool {
	if rest == "" {
		return false
	}
	rest = strings.TrimLeft(rest, cSpaces)
	return strings.HasPrefix(rest, "GMT") || strings.HasPrefix(rest, "UTC")
}

// checkStrEnd is check_str_end: nothing but whitespace follows.
func checkStrEnd(rest string) bool { return strings.TrimLeft(rest, cSpaces) == "" }

// isoNanoScale is parse_iso8601's mul_table: the multiplier that turns a
// fraction of n digits into nanoseconds.
var isoNanoScale = [...]uint64{0, 100000000, 10000000, 1000000, 100000, 10000, 1000, 100, 10, 1}

// parseISO8601 is parse_iso8601 in its extended format (rgw_common.cc:599-659
// at v19.2.6, :612-672 at v20.2.4): a date, "T" or whitespace, a time, and
// then, whitespace aside, nothing, "Z", or "." and a fraction that stringtoul
// takes followed by "Z". The fraction keeps radosgw's arithmetic: its value
// cut to 32 bits times the scale for its first nine bytes, cut to 32 bits.
func parseISO8601(s string) (cTM, uint32, bool) {
	tm, rest, ok := strptime(s, "%Y-%m-%dT%T")
	if !ok {
		tm, rest, ok = strptime(s, "%Y-%m-%d %T")
	}
	if !ok {
		return cTM{}, 0, false
	}
	str := strings.Trim(rest, cSpaces)
	if str == "" || str == "Z" {
		return tm, 0, true
	}
	if str[0] != '.' || str[len(str)-1] != 'Z' {
		return cTM{}, 0, false
	}
	frac := str[1 : len(str)-1]
	v, ok := strtoul(frac)
	if !ok {
		return cTM{}, 0, false
	}
	digits := min(len(frac), 9)
	return tm, uint32(uint64(uint32(v)) * isoNanoScale[digits]), true //nolint:gosec // radosgw truncates both to 32 bits
}

var (
	cWeekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	cMonths   = []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"}
)

// strptime is glibc's strptime in the C locale for the conversions
// parse_time's formats use. Whitespace in the format matches any run of
// whitespace, including none; any other byte must match exactly. %a and %A
// match a weekday's full or three-letter name, %b a month's, either case and
// the longest that fits, and neither checks the weekday against the date.
// Each number skips leading whitespace and reads at most its width in digits,
// stopping before a digit that would pass the field's maximum. %z takes "Z"
// or a sign and two or four digits, a colon allowed after the first two,
// whose minutes must be below 60. It returns the unparsed rest.
func strptime(s, format string) (tm cTM, rest string, ok bool) {
	tm.year = 1900
	for f := 0; f < len(format); f++ {
		c := format[f]
		if isCSpace(c) {
			s = strings.TrimLeft(s, cSpaces)
			continue
		}
		if c != '%' {
			if s == "" || s[0] != c {
				return cTM{}, "", false
			}
			s = s[1:]
			continue
		}
		f++
		var v int
		switch format[f] {
		case 'a', 'A':
			if s, ok = matchCWeekday(s); !ok {
				return cTM{}, "", false
			}
		case 'b':
			if tm.mon, s, ok = matchCName(s, cMonths); !ok {
				return cTM{}, "", false
			}
		case 'd':
			if tm.mday, s, ok = getNumber(s, 1, 31, 2); !ok {
				return cTM{}, "", false
			}
		case 'm':
			if v, s, ok = getNumber(s, 1, 12, 2); !ok {
				return cTM{}, "", false
			}
			tm.mon = v - 1
		case 'y':
			if v, s, ok = getNumber(s, 0, 99, 2); !ok {
				return cTM{}, "", false
			}
			// "values in the range 69-99 refer to the twentieth century"
			if v < 69 {
				v += 100
			}
			tm.year = 1900 + v
		case 'Y':
			if tm.year, s, ok = getNumber(s, 0, 9999, 4); !ok {
				return cTM{}, "", false
			}
		case 'H':
			if tm.hour, s, ok = getNumber(s, 0, 23, 2); !ok {
				return cTM{}, "", false
			}
		case 'M':
			if tm.minute, s, ok = getNumber(s, 0, 59, 2); !ok {
				return cTM{}, "", false
			}
		case 'S':
			if tm.sec, s, ok = getNumber(s, 0, 61, 2); !ok {
				return cTM{}, "", false
			}
		case 'T':
			var t cTM
			if t, s, ok = strptime(s, "%H:%M:%S"); !ok {
				return cTM{}, "", false
			}
			tm.hour, tm.minute, tm.sec = t.hour, t.minute, t.sec
		case 'z':
			if s, ok = skipZone(s); !ok {
				return cTM{}, "", false
			}
		default:
			panic("strptime: unsupported conversion %" + string(format[f]))
		}
	}
	return tm, s, true
}

// getNumber is glibc strptime's get_number.
func getNumber(s string, from, to, width int) (v int, rest string, ok bool) {
	s = strings.TrimLeft(s, cSpaces)
	if s == "" || !isCDigit(s[0]) {
		return 0, "", false
	}
	i := 0
	for {
		v = v*10 + int(s[i]-'0')
		i++
		width--
		if width == 0 || v*10 > to || i == len(s) || !isCDigit(s[i]) {
			break
		}
	}
	if v < from || v > to {
		return 0, "", false
	}
	return v, s[i:], true
}

// matchCWeekday is glibc strptime's %a and %A in the C locale. glibc tries
// each weekday in turn, its full name and then its abbreviation, and keeps the
// longest match; but its second, untranslated pass matches the abbreviation
// against the input pointer itself, so every abbreviation it finds moves
// where the later weekdays are matched from (time/strptime_l.c:378 at
// glibc-2.34, :380 at glibc-2.42). "MonTue" is thus read as Tuesday, and
// "TueMon" fails at "Mon".
func matchCWeekday(s string) (rest string, ok bool) {
	at, longest := 0, -1
	for _, name := range cWeekdays {
		if strncaseEqual(s[at:], name, len(name)) {
			longest = max(longest, at+len(name))
		}
		if strncaseEqual(s[at:], name[:3], 3) {
			longest = max(longest, at+3)
			at += 3
		}
	}
	if longest < 0 {
		return "", false
	}
	return s[longest:], true
}

// matchCName matches the longest of names, or of their three-letter forms,
// at the start of s, ignoring case, and returns its index.
func matchCName(s string, names []string) (idx int, rest string, ok bool) {
	best := 0
	for i, name := range names {
		for _, n := range []string{name, name[:3]} {
			if len(n) > best && strncaseEqual(s, n, len(n)) {
				best, idx = len(n), i
			}
		}
	}
	return idx, s[best:], best > 0
}

// skipZone is glibc strptime's %z, whose offset parse_time drops.
func skipZone(s string) (string, bool) {
	rest, _, ok := zoneOffset(s)
	return rest, ok
}

// zoneOffset is glibc strptime's %z: "Z", or a sign and two or four digits, a colon allowed after
// the first two, whose minutes must be below 60. gmtoff is the tm_gmtoff it
// sets, in seconds east of UTC.
func zoneOffset(s string) (rest string, gmtoff int64, ok bool) {
	s = strings.TrimLeft(s, cSpaces)
	if strings.HasPrefix(s, "Z") {
		return s[1:], 0, true
	}
	if s == "" || (s[0] != '+' && s[0] != '-') {
		return "", 0, false
	}
	neg := s[0] == '-'
	s = s[1:]
	n, val := 0, 0
	for n < 4 && s != "" && isCDigit(s[0]) {
		val = val*10 + int(s[0]-'0')
		s = s[1:]
		n++
		if n == 2 && len(s) >= 2 && s[0] == ':' && isCDigit(s[1]) {
			s = s[1:]
		}
	}
	switch {
	case n == 2:
		val *= 100
	case n != 4, val%100 >= 60:
		return "", 0, false
	}
	gmtoff = int64(val/100*3600 + val%100*60)
	if neg {
		gmtoff = -gmtoff
	}
	return s, gmtoff, true
}

// internalTimegm is internal_timegm (include/timegm.h at v19.2.6 and
// v20.2.4): seconds since the epoch counted from the fields as they stand,
// so a day past the month's end runs into the next month.
func internalTimegm(tm cTM) int64 {
	year, month := int64(tm.year), int64(tm.mon)+1
	isLeap := func(y int64) bool { return y%400 == 0 || (y%100 != 0 && y%4 == 0) }
	daysFrom0 := func(y int64) int64 {
		y--
		return 365*y + y/400 - y/100 + y/4
	}
	cumulative := [2][12]int64{
		{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334},
		{0, 31, 60, 91, 121, 152, 182, 213, 244, 274, 305, 335},
	}
	leap := 0
	if isLeap(year) {
		leap = 1
	}
	days := daysFrom0(year) - daysFrom0(1970) + cumulative[leap][month-1] + int64(tm.mday) - 1
	return 86400*days + 3600*int64(tm.hour) + 60*int64(tm.minute) + int64(tm.sec)
}

// utime is utime_t(sec, ns) (include/utime.h at v19.2.6 and v20.2.4): the
// seconds cut to 32 bits, and nanoseconds past a second, but not exactly one,
// carried into them up to the 32-bit maximum.
func utime(sec int64, ns uint32) (sec32, nsec uint32) {
	sec32, nsec = uint32(sec), ns //nolint:gosec // utime_t keeps 32 bits of seconds
	if nsec > 1e9 {
		sec32 = uint32(min(uint64(sec32)+uint64(nsec/1e9), math.MaxUint32))
		nsec %= 1e9
	}
	return sec32, nsec
}

// cSpaces is what C's isspace takes in the C locale.
const cSpaces = " \t\n\v\f\r"

func isCSpace(c byte) bool { return strings.IndexByte(cSpaces, c) >= 0 }

func isCAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isCDigit(c byte) bool { return c >= '0' && c <= '9' }

// byteAt is s[i] as a C string reads it: NUL at and past the end.
func byteAt(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return 0
}

// strncaseEqual is strncasecmp(a, b, n) == 0 over C strings.
func strncaseEqual(a, b string, n int) bool {
	for i := range n {
		ca, cb := byteAt(a, i), byteAt(b, i)
		if lowerASCII(ca) != lowerASCII(cb) {
			return false
		}
		if ca == 0 {
			return true
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
