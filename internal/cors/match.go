package cors

import (
	"bytes"
	"iter"
	"slices"
	"strconv"
	"strings"
)

// MaxRequestHeaders is the most distinct header names, compared in ASCII
// lower case, a request may ask a rule to allow. Match matches no rule for a
// request naming more, and ResponseHeaders allows none of them; radosgw has
// no such limit. A rule costs its one-* AllowedHeaders times the names asked
// for, and an unauthenticated preflight chooses both the bucket and the
// names. With this limit, the densest configuration a 1 MiB PutBucketCors
// body stores, about 30,700 one-* members, cost about 4 ms of CPU per
// preflight on the development host; a larger stored attr costs
// proportionally more. A browser names only the headers its page sets
// beyond the CORS-safelisted ones.
const MaxRequestHeaders = 32

// Match is RGWCORSConfiguration::match_rule on radosgw's main branch
// (https://github.com/ceph/ceph/blob/cc1dcecee08/src/rgw/rgw_cors.cc#L242-L290 at cc1dcecee08):
// the first rule, in stored order, that allows the origin, the method and
// every one of reqHeaders, or nil. A preflight passes its
// Access-Control-Request-Headers, split by ParseRequestHeaders; nil asks for
// no header. An empty origin matches no rule, as main's RGWCORSRule::matches
// refuses one, and so do more than MaxRequestHeaders distinct headers.
//
// Both floors take instead the first rule that allows the origin alone and
// then refuse the request if that rule does not allow its method or headers
// (host_name_rule,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L186-L194 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.cc#L186-L194 at v20.2.4),
// so a later rule that allows the request is never used
// (docs/ceph-upstream-bugs.md, "radosgw answers a CORS request with the first
// rule whose origin matches").
//
// method maps to a flag by its exact name, as the floors'
// get_cors_method_flags maps it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L137-L147 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L137-L147 at v20.2.4):
// GET, POST, PUT, DELETE or HEAD. Any other method, COPY and the empty string
// among them, matches no rule.
//
// An origin is allowed when a member of the rule's AllowedOrigins is * or the
// origin itself, compared byte for byte, or holds one * whose prefix and
// suffix the origin starts and ends with, around zero or more bytes. A
// header is allowed the same way against AllowedHeaders with both sides
// lower-cased in ASCII, as is_header_allowed lowers them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L96-L173 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.cc#L96-L173 at v20.2.4).
// radosgw's is_string_in_set lets the prefix and suffix overlap and splits a
// member at spaces and tabs as well as at the *, which lets in values the
// pattern does not cover; it reads a member with more than one * through its
// first one or two pieces, which lets in less than the pattern covers.
// rgw-go matches a pattern as written (docs/ceph-upstream-bugs.md,
// "radosgw's CORS wildcard match allows origins and headers its pattern does
// not cover").
func (c *Configuration) Match(origin, method string, reqHeaders []string) *Rule {
	r, _ := c.match(origin, method, reqHeaders)
	return r
}

// match is Match, also returning how many times it tried a one-* member of
// AllowedHeaders against a requested name, the work that grows with both
// the configuration and the request.
func (c *Configuration) match(origin, method string, reqHeaders []string) (rule *Rule, tries int) {
	flag := requestMethod(method)
	if origin == "" || flag == 0 {
		return nil, 0
	}
	var want *requested
	if len(reqHeaders) > 0 {
		var ok bool
		if want, ok = newRequested(reqHeaders); !ok {
			return nil, 0
		}
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.Methods&flag != 0 && inSet(r.AllowedOrigins, origin) && (want == nil || want.cover(r) == 0) {
			rule = r
			break
		}
	}
	if want != nil {
		tries = want.tries
	}
	return rule, tries
}

func requestMethod(m string) Methods {
	switch m {
	case "GET":
		return MethodGet
	case "POST":
		return MethodPost
	case "PUT":
		return MethodPut
	case "DELETE":
		return MethodDelete
	case "HEAD":
		return MethodHead
	}
	return 0
}

// requested is the header names a request asks a rule to allow, lower-cased
// in ASCII with each once, so that one rule costs a pass over its
// AllowedHeaders whatever the request repeats.
type requested struct {
	names   []string
	index   map[string]int
	covered []bool
	buf     []byte
	tries   int
}

// newRequested reads hs, reporting false when it names more than
// MaxRequestHeaders distinct headers.
func newRequested(hs []string) (*requested, bool) {
	q := &requested{index: make(map[string]int)}
	for _, h := range hs {
		q.buf = appendLowerASCII(q.buf[:0], h)
		if _, seen := q.index[string(q.buf)]; seen {
			continue
		}
		if len(q.names) == MaxRequestHeaders {
			return nil, false
		}
		n := string(q.buf)
		q.index[n] = len(q.names)
		q.names = append(q.names, n)
	}
	q.covered = make([]bool, len(q.names))
	return q, true
}

// cover marks the names r's AllowedHeaders allow and returns how many it
// does not. An exact member, or one with more than one *, is looked up; a
// member with one * is tried against each name not yet allowed.
func (q *requested) cover(r *Rule) int {
	clear(q.covered)
	left := len(q.names)
	for _, a := range r.AllowedHeaders {
		if left == 0 {
			break
		}
		q.buf = appendLowerASCII(q.buf[:0], a)
		star := bytes.IndexByte(q.buf, '*')
		if star < 0 || bytes.IndexByte(q.buf[star+1:], '*') >= 0 {
			if i, ok := q.index[string(q.buf)]; ok && !q.covered[i] {
				q.covered[i] = true
				left--
			}
			continue
		}
		prefix, suffix := q.buf[:star], q.buf[star+1:]
		for i, n := range q.names {
			if q.covered[i] {
				continue
			}
			q.tries++
			if len(n) >= len(prefix)+len(suffix) &&
				n[:len(prefix)] == string(prefix) && n[len(n)-len(suffix):] == string(suffix) {
				q.covered[i] = true
				left--
			}
		}
	}
	return left
}

// inSet reports whether a member of set allows s: * or s itself, or one *
// with s starting with what precedes it and ending with what follows it, the
// two not overlapping.
func inSet(set []string, s string) bool {
	for _, m := range set {
		if m == s {
			return true
		}
		prefix, suffix, ok := strings.Cut(m, "*")
		if ok && !strings.Contains(suffix, "*") && len(s) >= len(prefix)+len(suffix) &&
			strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

// appendLowerASCII appends s as lowercase_http_attr lowers it, tolower in the
// C locale on each byte.
func appendLowerASCII(dst []byte, s string) []byte {
	for i := range len(s) {
		dst = append(dst, lowerASCII(s[i]))
	}
	return dst
}

// ParseRequestHeaders splits an Access-Control-Request-Headers value as
// radosgw's get_str_vec and get_str_list split it: at every ;, ,, =, space
// and tab, dropping empty pieces
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/str_list.cc#L22-L55 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/str_list.cc#L22-L55 at v20.2.4;
// for_each_substr,
// https://github.com/ceph/ceph/blob/v19.2.7/src/include/str_list.h#L14-L24 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/include/str_list.h#L13-L23 at v20.2.4).
func ParseRequestHeaders(v string) []string {
	return strings.FieldsFunc(v, func(r rune) bool { return strings.ContainsRune(";,= \t", r) })
}

// Headers are the CORS headers a rule answers a request with, the values
// dump_access_control is given
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L526-L554 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L531-L559 at v20.2.4).
// All yields them as that function writes them.
type Headers struct {
	AllowOrigin   string
	AllowMethods  string
	AllowHeaders  string
	ExposeHeaders string
	MaxAge        uint32
}

// ResponseHeaders are the headers r answers a request from origin with,
// for method and the requested headers reqHeaders, as generate_cors_headers
// and get_cors_response_headers compute them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1519-L1600 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1756-L1837 at v20.2.4):
//   - AllowOrigin is * when authorized is false and r's origins hold *,
//     else origin. authorized reports whether the request carries an
//     Authorization header. radosgw's preflight answer sends the Origin as
//     the request sent it whatever the rule holds
//     (RGWOptionsCORS_ObjStore_S3::send_response,
//     https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326 at v20.2.4).
//   - AllowMethods is method.
//   - AllowHeaders is each of reqHeaders r allows, as sent, joined by commas;
//     none when reqHeaders names more than MaxRequestHeaders distinct headers.
//   - ExposeHeaders is r's expose headers joined as format_exp_headers joins
//     them
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.cc#L175-L184 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.cc#L175-L184 at v20.2.4):
//     a comma before each once the value is not empty, so an empty header
//     before the first that is not adds none, and a line feed written as \n.
//     A carriage return is written as \r, as CVE-2021-3524's fix made
//     format_exp_headers write it
//     (https://github.com/ceph/ceph/blob/v16.2.4/src/rgw/rgw_cors.cc#L145-L155 at v16.2.4),
//     which no release from Quincy on carries (docs/ceph-upstream-bugs.md,
//     "radosgw from Quincy on lacks CVE-2021-3524's fix for CORS expose
//     headers"). Every other byte below 0x20 but a tab, and 0x7f, is written
//     as \x and two lower-case hex digits, so no control byte reaches the
//     header; radosgw sends them as they are and ends the value at a NUL.
//   - MaxAge is r's.
func (r *Rule) ResponseHeaders(origin, method string, reqHeaders []string, authorized bool) Headers {
	h := Headers{AllowOrigin: origin, AllowMethods: method, MaxAge: r.MaxAge}
	if !authorized && slices.Contains(r.AllowedOrigins, "*") {
		h.AllowOrigin = "*"
	}
	if len(reqHeaders) > 0 {
		h.AllowHeaders = allowedHeaders(r, reqHeaders)
	}
	var b strings.Builder
	for _, x := range r.ExposeHeaders {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		writeExposeHeader(&b, x)
	}
	h.ExposeHeaders = b.String()
	return h
}

// allowedHeaders is each of hs r allows, as sent, joined by commas, or none
// when hs names more than MaxRequestHeaders distinct headers.
func allowedHeaders(r *Rule, hs []string) string {
	want, ok := newRequested(hs)
	if !ok {
		return ""
	}
	want.cover(r)
	var b strings.Builder
	for _, x := range hs {
		want.buf = appendLowerASCII(want.buf[:0], x)
		if want.covered[want.index[string(want.buf)]] {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(x)
		}
	}
	return b.String()
}

// writeExposeHeader writes x with its line feeds as \n, its carriage returns
// as \r and every other control byte but a tab as \xHH.
func writeExposeHeader(b *strings.Builder, x string) {
	const hexDigits = "0123456789abcdef"
	for i := range len(x) {
		switch c := x[i]; {
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case (c < 0x20 && c != '\t') || c == 0x7f:
			b.WriteString(`\x`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		default:
			b.WriteByte(c)
		}
	}
}

// All yields the headers as dump_access_control writes them, in its order:
// nothing without an AllowOrigin; then Access-Control-Allow-Origin, Vary:
// Origin unless AllowOrigin is *, and Access-Control-Allow-Methods,
// Access-Control-Allow-Headers and Access-Control-Expose-Headers each when
// it is not empty, and Access-Control-Max-Age unless it is MaxAgeInvalid.
func (h Headers) All() iter.Seq2[string, string] {
	return func(yield func(string, string) bool) {
		if h.AllowOrigin == "" {
			return
		}
		if !yield("Access-Control-Allow-Origin", h.AllowOrigin) {
			return
		}
		if h.AllowOrigin != "*" && !yield("Vary", "Origin") {
			return
		}
		for _, f := range []struct{ name, v string }{
			{"Access-Control-Allow-Methods", h.AllowMethods},
			{"Access-Control-Allow-Headers", h.AllowHeaders},
			{"Access-Control-Expose-Headers", h.ExposeHeaders},
		} {
			if f.v != "" && !yield(f.name, f.v) {
				return
			}
		}
		if h.MaxAge != MaxAgeInvalid {
			yield("Access-Control-Max-Age", strconv.FormatUint(uint64(h.MaxAge), 10))
		}
	}
}
