package admin

import (
	"math"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
)

// Args are the query arguments as RGWHTTPArgs holds them, with RESTArgs'
// readers (rgw_rest.cc:854-1032 at v19.2.6, :859-1037 at v20.2.4). A
// repeated key keeps its last value: RGWHTTPArgs::append assigns
// val_map[name] = val (rgw_common.cc:918-924 at v19.2.6, :931-937 at
// v20.2.4), the rule auth's query parser follows too.
type Args struct {
	vals map[string]string
	// sys holds the names starting "rgwx-", which append keeps in
	// sys_val_map, where get and exists never look (rgw_common.cc:918-924 at
	// v19.2.6, :931-937 at v20.2.4).
	sys map[string]string
	// sub is the first admin sub-resource in query order: append records
	// only the first (rgw_common.cc:962-975 at v19.2.6).
	sub string
}

// adminSubResources are the names RGWHTTPArgs::append takes as admin
// sub-resources.
var adminSubResources = []string{"subuser", "key", "caps", "index", "policy", "quota", "list", "object", "sync"}

// ParseArgs is RGWHTTPArgs::parse (rgw_common.cc:848-898 at v19.2.6): each
// "&"-separated pair is url-decoded whole as a query, so '+' is a space and
// an escaped '=' separates, then split at its first '='; a name holding
// "X-Amz-" has every byte but its dashes lowercased.
func ParseArgs(rawQuery string) Args {
	a := Args{vals: map[string]string{}, sys: map[string]string{}}
	if rawQuery == "" {
		return a
	}
	for pair := range strings.SplitSeq(strings.TrimPrefix(rawQuery, "?"), "&") {
		name, val, _ := strings.Cut(rgwtext.URLDecode(pair, true), "=")
		if strings.Contains(name, "X-Amz-") {
			name = lowerASCII(name)
		}
		if strings.HasPrefix(name, "rgwx-") {
			a.sys[name] = val
		} else {
			a.vals[name] = val
		}
		if a.sub == "" && slices.Contains(adminSubResources, name) {
			a.sub = name
		}
	}
	return a
}

// Has is RGWHTTPArgs::exists.
func (a Args) Has(name string) bool {
	_, ok := a.vals[name]
	return ok
}

// Get is RGWHTTPArgs::get: the value as the parse decoded it, once.
func (a Args) Get(name string) (string, bool) {
	v, ok := a.vals[name]
	return v, ok
}

// String is RESTArgs::get_string: def when absent, otherwise the value
// url-decoded a second time as a query, so a "+" the parse decoded from
// "%2B" becomes a space (rgw_rest.cc:854-871 at v19.2.6, :859-876 at
// v20.2.4).
func (a Args) String(name, def string) (v string, present bool) {
	v, ok := a.vals[name]
	if !ok {
		return def, false
	}
	return rgwtext.URLDecode(v, true), true
}

// Bool is RESTArgs::get_bool (rgw_rest.cc:1002-1032 at v19.2.6): absent is
// def; empty, "true" in any case and "1" are true; "false" in any case and
// "0" are false; anything else is def and ErrInvalidArgument.
func (a Args) Bool(name string, def bool) (v, present bool, err error) {
	s, ok := a.vals[name]
	if !ok {
		return def, false, nil
	}
	switch c := lowerASCII(rgwtext.CString(s)); {
	case s == "" || c == "true" || s == "1":
		return true, true, nil
	case c == "false" || s == "0":
		return false, true, nil
	}
	return def, true, op.ErrInvalidArgument
}

// Int64 is RESTArgs::get_int64 over stringtoll (rgw_string.h:38-52 at
// v19.2.6 and v20.2.4): strtoll, refusing LLONG_MAX and trailing text.
func (a Args) Int64(name string, def int64) (v int64, present bool, err error) {
	return read(a, name, def, stringToLL)
}

// Int32 is RESTArgs::get_int32 over stringtol (rgw_string.h:70-84): strtol's
// 64-bit long, refusing LONG_MAX and trailing text, then truncated to 32
// bits.
func (a Args) Int32(name string, def int32) (v int32, present bool, err error) {
	return read(a, name, def, func(s string) (int32, error) {
		n, err := stringToLL(s)
		return int32(n), err //nolint:gosec // stringtol casts its long to int32_t
	})
}

// Uint64 is RESTArgs::get_uint64 over stringtoull (rgw_string.h:54-68):
// strtoull, which negates a negative value, refusing ULLONG_MAX and
// trailing text.
func (a Args) Uint64(name string, def uint64) (v uint64, present bool, err error) {
	return read(a, name, def, stringToULL)
}

// Uint32 is RESTArgs::get_uint32 over stringtoul (rgw_string.h:86-100):
// strtoul's 64-bit unsigned long, refusing ULONG_MAX and trailing text, then
// truncated to 32 bits.
func (a Args) Uint32(name string, def uint32) (v uint32, present bool, err error) {
	return read(a, name, def, func(s string) (uint32, error) {
		n, err := stringToULL(s)
		return uint32(n), err //nolint:gosec // stringtoul casts its unsigned long to uint32_t
	})
}

// Epoch is RESTArgs::get_epoch over utime_t::parse_date (rgw_rest.cc:982-1000
// at v19.2.6; src/include/utime.h:397-500 at v19.2.6 and v20.2.4): a date,
// a date with a time, or "<sec>.<usec>"; anything else is
// ErrInvalidArgument.
func (a Args) Epoch(name string, def uint64) (v uint64, present bool, err error) {
	return read(a, name, def, func(s string) (uint64, error) {
		n, ok := parseDate(s)
		if !ok {
			return 0, op.ErrInvalidArgument
		}
		return n, nil
	})
}

// read is the shape the numeric RESTArgs readers share: def when the
// argument is absent, and def with conv's error when conv refuses it.
func read[T any](a Args, name string, def T, conv func(string) (T, error)) (v T, present bool, err error) {
	s, ok := a.vals[name]
	if !ok {
		return def, false, nil
	}
	if v, err = conv(s); err != nil {
		return def, true, err
	}
	return v, true, nil
}

// SubResource is the first admin sub-resource in the query, "" when none.
func (a Args) SubResource() string { return a.sub }

// lowerASCII is tolower over every byte in the C locale.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// strtoMagnitude reads s as glibc's strtoll and strtoull do in base 10:
// leading white space, an optional sign, then digits. It reports the
// magnitude, saturated, whether it overflowed, and whether the conversion
// consumed the whole C string, as `*end == '\0'` asks: true for an empty
// string, which converts to 0, and false for one with no digits.
func strtoMagnitude(s string) (neg bool, mag uint64, overflow, whole bool) {
	s = rgwtext.CString(s)
	i := 0
	for i < len(s) && strings.IndexByte(" \t\n\v\f\r", s[i]) >= 0 {
		i++
	}
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	for ; i < len(s) && '0' <= s[i] && s[i] <= '9'; i++ {
		d := uint64(s[i] - '0')
		if mag > (math.MaxUint64-d)/10 {
			overflow = true
			mag = math.MaxUint64
			continue
		}
		if !overflow {
			mag = mag*10 + d
		}
	}
	if i == start {
		return false, 0, false, s == ""
	}
	return neg, mag, overflow, i == len(s)
}

// stringToLL is stringtoll: strtoll's value, ErrInvalidArgument for
// LLONG_MAX, the value an overflow saturates to, or trailing text.
func stringToLL(s string) (int64, error) {
	neg, mag, overflow, whole := strtoMagnitude(s)
	v := strtoll(neg, mag, overflow)
	if v == math.MaxInt64 || !whole {
		return 0, op.ErrInvalidArgument
	}
	return v, nil
}

// strtoll is the value strtoll gives a magnitude strtoMagnitude read,
// saturated at the int64 bounds.
func strtoll(neg bool, mag uint64, overflow bool) int64 {
	switch {
	case neg && (overflow || mag > 1<<63):
		return math.MinInt64
	case neg:
		return -int64(mag-1) - 1 //nolint:gosec // mag is at most 1<<63 here
	case overflow || mag > math.MaxInt64:
		return math.MaxInt64
	default:
		return int64(mag) //nolint:gosec // mag is at most MaxInt64 here
	}
}

// stringToULL is stringtoull: strtoull's value, a negative one negated,
// ErrInvalidArgument for ULLONG_MAX, the value an overflow saturates to, or
// trailing text.
func stringToULL(s string) (uint64, error) {
	neg, mag, overflow, whole := strtoMagnitude(s)
	v := mag
	switch {
	case overflow:
		v = math.MaxUint64
	case neg:
		v = -mag
	}
	if v == math.MaxUint64 || !whole {
		return 0, op.ErrInvalidArgument
	}
	return v, nil
}

// parseDate is utime_t::parse_date's epoch (src/include/utime.h:397-500 at
// v19.2.6 and v20.2.4) over glibc's strptime. "%Y-%m-%d" is read first; past
// it a ' ' or 'T' introduces the time, read into the same struct tm with a
// format built from the time's own text (timeFormat); anything else after
// the date is ignored. A text that is not a date is "<sec>.<usec>", read
// with sscanf's "%d.%d". The epoch is internal_timegm's less the %z offset.
func parseDate(s string) (uint64, bool) {
	s = rgwtext.CString(s)
	tm, rest, ok := strptime.Parse(strptime.Tm{}, s, "%Y-%m-%d")
	if !ok {
		return secUsec(s)
	}
	if rest != "" && (rest[0] == ' ' || rest[0] == 'T') {
		p := rest[1:]
		if tm, _, ok = strptime.Parse(tm, p, timeFormat(p)); !ok {
			return 0, false
		}
	}
	return uint64(tm.Timegm().Unix() - int64(tm.Gmtoff)), true //nolint:gosec // radosgw casts time_t to uint64_t
}

// timeFormat is the strptime format parse_date builds from the time text p
// in char fmt[32] (utime.h:413-437): the first 31 bytes of p, with "%H:%M"
// over bytes 0-4 and "%S" over 6-7, so p's sixth byte stays as a literal
// and every byte from the ninth on stays as format text, conversions
// included. When the ninth byte is '.', the digits after it stay literal
// and a sign where they end becomes "%z", which also ends the format. With
// the sign at byte 30, radosgw writes that terminator one byte past the
// array (docs/ceph-upstream-bugs.md); the format it reads is the same.
func timeFormat(p string) string {
	var f [32]byte
	copy(f[:31], p)
	copy(f[0:5], "%H:%M")
	copy(f[6:8], "%S")
	q := 8
	if f[q] == '.' {
		q = 9
		for f[q] != 0 && '0' <= f[q] && f[q] <= '9' {
			q++
		}
	}
	if f[q] == '-' || f[q] == '+' {
		f[q], f[q+1] = '%', 'z'
		if q+2 < len(f) {
			f[q+2] = 0
		}
	}
	return rgwtext.CString(string(f[:]))
}

// secUsec is sscanf(s, "%d.%d", &sec, &usec) == 2, then
// internal_timegm(gmtime_r(sec)), which is sec.
func secUsec(s string) (uint64, bool) {
	sec, rest, ok := scanInt(s)
	if !ok {
		return 0, false
	}
	if rest, ok = strings.CutPrefix(rest, "."); !ok {
		return 0, false
	}
	if _, _, ok = scanInt(rest); !ok {
		return 0, false
	}
	return uint64(sec), true //nolint:gosec // radosgw casts time_t to uint64_t
}

// scanInt is glibc sscanf's %d: white space, an optional sign and at least
// one digit, converted as strtol converts them, saturating at the bounds of
// a 64-bit long, then stored into an int.
func scanInt(s string) (n int64, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	for i < len(s) && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0, s, false
	}
	neg, mag, overflow, _ := strtoMagnitude(s[:i])
	return int64(int32(strtoll(neg, mag, overflow))), s[i:], true //nolint:gosec // %d stores an int
}

// Format is RGWFormat for the admin API.
type Format uint8

// The formats the admin handlers render.
const (
	FormatJSON Format = iota
	FormatXML
	FormatHTML
)

// SelectFormat is RGWHandler_REST::allocate_formatter with the admin
// handlers' default, JSON (rgw_rest.cc:1732-1764 at v19.2.6;
// RGWHandler_Auth_S3::init, rgw_rest_s3.cc:5130-5138): the format argument
// when it is exactly "xml", "json" or "html", otherwise the Accept header up
// to its first ';', compared exactly.
func SelectFormat(a Args, accept string) Format {
	switch v, _ := a.Get("format"); v {
	case "xml":
		return FormatXML
	case "json":
		return FormatJSON
	case "html":
		return FormatHTML
	}
	if i := strings.IndexByte(accept, ';'); i >= 0 {
		accept = accept[:i]
	}
	switch accept {
	case "text/xml", "application/xml":
		return FormatXML
	case "application/json":
		return FormatJSON
	case "text/html":
		return FormatHTML
	}
	return FormatJSON
}

// Request is one parsed admin request.
type Request struct {
	// Resource is the manager the path names under the entry: "user",
	// "bucket", "info", ...
	Resource string
	// Sub is the next segment, the metadata section or "period" under realm.
	Sub  string
	Args Args
	// Format is SelectFormat's answer; JSON until the handler selects it,
	// and for a request no handler takes.
	Format Format
}

// managers are the resources radosgw registers under rgw_admin_entry:
// info, usage and account in init_apis (rgw_appmain.cc:354-361 at v19.2.6,
// :363-370 at v20.2.4), and the RADOS driver's register_admin_apis
// (driver/rados/rgw_sal_rados.cc:2025-2036 at v19.2.6, :2568-2579 at
// v20.2.4).
var managers = []string{"info", "usage", "account", "user", "bucket", "metadata", "log", "config", "realm", "ratelimit"}

// ParseRequest parses path, the decoded URI radosgw picks the manager by,
// under the entry prefix, as RGWRESTMgr::get_resource_mgr walks it
// (rgw_rest.cc:1974-1997 at v19.2.6): past the entry, a '/' and a
// registered resource's exact, case-sensitive name, then the end or a '/'.
// Anything else stays with RGWRESTMgr_Admin, which has no handler, and is
// ErrMethodNotAllowed with Format JSON. The segment after the resource is
// Sub; any further one is ignored, as init_from_header leaves it to the
// object name no admin op reads.
func ParseRequest(path, prefix, rawQuery string) (Request, error) {
	q := Request{Args: ParseArgs(rawQuery)}
	rest, ok := strings.CutPrefix(path, "/"+prefix)
	if !ok {
		return q, op.ErrMethodNotAllowed
	}
	rest, ok = strings.CutPrefix(rest, "/")
	if !ok {
		return q, op.ErrMethodNotAllowed
	}
	resource, rest, _ := strings.Cut(rest, "/")
	if !slices.Contains(managers, resource) {
		return q, op.ErrMethodNotAllowed
	}
	q.Resource = resource
	q.Sub, _, _ = strings.Cut(rest, "/")
	return q, nil
}
