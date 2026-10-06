package op

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
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
		end = rgwtext.Atoll(endStr)
		if end < 0 {
			return 0, -1, false, ErrInvalidRange
		}
	}
	if ofsStr != "" {
		ofs = rgwtext.Atoll(ofsStr)
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
	sec, nsec := utime(tm.Timegm().Unix(), ns)
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

// parseRFC2616 is parse_rfc2616 (rgw_common.cc:566-597 at v19.2.6, :579-610
// at v20.2.4): RFC 850, asctime, RFC 1123 with a GMT or UTC zone, then RFC
// 1123 with a numeric offset, each from a zeroed struct tm.
func parseRFC2616(s string) (strptime.Tm, bool) {
	if tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%A, %d-%b-%y %H:%M:%S "); ok && checkGMTEnd(rest) {
		return tm, true
	}
	if tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%a %b %d %H:%M:%S %Y"); ok && checkStrEnd(rest) {
		return tm, true
	}
	if tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%a, %d %b %Y %H:%M:%S "); ok && checkGMTEnd(rest) {
		return tm, true
	}
	if tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%a, %d %b %Y %H:%M:%S %z"); ok && checkStrEnd(rest) {
		return tm, true
	}
	return strptime.Tm{}, false
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
// Its formats' %T is glibc's "%H:%M:%S", spelled out for strptime.Parse.
func parseISO8601(s string) (strptime.Tm, uint32, bool) {
	tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%Y-%m-%dT%H:%M:%S")
	if !ok {
		tm, rest, ok = strptime.Parse(strptime.Tm{}, s, "%Y-%m-%d %H:%M:%S")
	}
	if !ok {
		return strptime.Tm{}, 0, false
	}
	str := strings.Trim(rest, cSpaces)
	if str == "" || str == "Z" {
		return tm, 0, true
	}
	if str[0] != '.' || str[len(str)-1] != 'Z' {
		return strptime.Tm{}, 0, false
	}
	frac := str[1 : len(str)-1]
	v, ok := rgwtext.StringToUL(frac)
	if !ok {
		return strptime.Tm{}, 0, false
	}
	digits := min(len(frac), 9)
	return tm, uint32(uint64(v) * isoNanoScale[digits]), true //nolint:gosec // radosgw truncates the product to 32 bits
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
