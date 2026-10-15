// Package vhost reads the bucket a virtual-hosted request's Host names, as
// radosgw's RGWREST::preprocess does, and the request target req_info splits
// before it. Routing and SigV2 read the bucket here; routing, SigV2, SigV4
// and the S3 handler's Location header read the target from RequestTarget, so
// a request is served on the path and query it is authenticated for.
package vhost

import (
	"net"
	"net/http"
	"strings"
)

// RequestTarget is req_info's request_uri and request_params: REQUEST_URI is
// the target as sent; get_abs_path reduces an absolute-form one before the
// split at its first '?', so a query without one falls back to beast's
// QUERY_STRING
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L209-L245 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L209-L255 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L74-L84). The
// path is what RGWREST::preprocess then puts a virtual-hosted bucket in front of.
func RequestTarget(req *http.Request) (path, query string) {
	target := req.RequestURI
	if target == "" {
		target = req.URL.RequestURI()
	}
	_, queryString, _ := strings.Cut(target, "?")
	uri := target
	if !strings.HasPrefix(uri, "/") {
		uri = absPath(uri)
	}
	path, query, hasQuery := strings.Cut(uri, "?")
	if !hasQuery {
		query = queryString
	}
	return path, query
}

// absPath is get_abs_path: an http, https, ws or wss URI keeps what
// follows its authority from the first '/', which may lie in the query;
// without a '/' it stays as it is.
func absPath(uri string) string {
	for _, scheme := range []string{"http://", "https://", "ws://", "wss://"} {
		if rest, ok := strings.CutPrefix(uri, scheme); ok {
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				return rest[i:]
			}
			return uri
		}
	}
	return uri
}

// maxBucketNameLen is RGWHandler_REST's MAX_BUCKET_NAME_LEN
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.h#L555 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L567 at v20.2.4).
const maxBucketNameLen = 255

// Hostname is the Host header as RGWREST::preprocess leaves it. req_info first
// drops a trailing ':' and the digits after it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L246-L261 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L256-L271 at v20.2.4);
// preprocess then keeps what a leading '[' and the first ']' enclose, or cuts
// at the first ':'
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2048-L2060 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2065-L2077 at v20.2.4).
// radosgw keeps the case the client sent.
func Hostname(h string) string {
	if i := strings.LastIndexByte(h, ':'); i >= 0 && isDigits(h[i+1:]) {
		h = h[:i]
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i > 0 {
			return h[1:i]
		}
		return h
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

func isDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Bucket is the bucket RGWREST::preprocess reads from host, a Host as
// Hostname leaves it, given rgw_dns_name's entries and the zonegroup's
// hostnames
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2048-L2143 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2065-L2160 at v20.2.4):
// the subdomain of a configured name, or, when names are configured, the
// whole host if it is not a configured name, looks like no IP address and
// passes validate_bucket_name (the CNAME case). "" names no bucket.
// rgw_resolve_cname's DNS lookup has no counterpart.
func Bucket(host string, names []string) string {
	if host == "" {
		return ""
	}
	domain, subdomain := findHostInDomains(host, names)
	if subdomain == "" && domain != host && hasName(names) &&
		!LooksLikeIPAddress(host) && validBucketName(host) {
		return host
	}
	return subdomain
}

// Domain is the configured name RGWREST::preprocess finds host in, a Host as
// Hostname leaves it, as cut from host with the client's case: the domain
// preprocess makes s->info.domain
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2163-L2165 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2180-L2182 at v20.2.4).
// "" when host is in no configured name.
func Domain(host string, names []string) string {
	domain, _ := findHostInDomains(host, names)
	return domain
}

// findHostInDomains is rgw_find_host_in_domains
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L260-L287 at v19.2.6
// and v20.2.4). radosgw walks its hostname set in sorted order and stops at
// the first name host equals or ends in after a ".", comparing without regard
// to ASCII case; that first match is the least matching name. The domain and
// subdomain are cut from host, so they keep the client's case.
func findHostInDomains(host string, names []string) (domain, subdomain string) {
	found := ""
	for _, name := range names {
		if name == "" || found != "" && name >= found {
			continue
		}
		if pos := len(host) - len(name); pos >= 0 && equalFoldASCII(host[pos:], name) &&
			(pos == 0 || host[pos-1] == '.') {
			found = name
		}
	}
	if found == "" {
		return "", ""
	}
	pos := len(host) - len(found)
	if pos == 0 {
		return host, ""
	}
	return host[pos:], host[:pos-1]
}

// hasName reports whether radosgw's hostname set would be non-empty; it drops
// empty names.
func hasName(names []string) bool {
	for _, name := range names {
		if name != "" {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// LooksLikeIPAddress is looks_like_ip_address
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.h#L801-L826 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.h#L831-L856 at v20.2.4):
// an address inet_pton(AF_INET6) accepts, or digits split by exactly three
// dots with none leading or doubled, whatever the digits' values.
func LooksLikeIPAddress(s string) bool {
	if strings.IndexByte(s, ':') >= 0 && net.ParseIP(s) != nil {
		return true
	}
	periods := 0
	expectPeriod := false
	for i := range len(s) {
		switch c := s[i]; {
		case c == '.':
			if !expectPeriod {
				return false
			}
			periods++
			if periods > 3 {
				return false
			}
			expectPeriod = false
		case '0' <= c && c <= '9':
			expectPeriod = true
		default:
			return false
		}
	}
	return periods == 3
}

// validBucketName is RGWHandler_REST::validate_bucket_name
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1815-L1840 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1819-L1844 at v20.2.4),
// which radosgw applies only to a Host it might take as a bucket: empty, which
// names no bucket, or 3 to 255 bytes with no '/' and no 0xff byte. A bucket in
// the path is left to the bucket lookup and to CreateBucket's own check.
func validBucketName(name string) bool {
	if name == "" {
		return true
	}
	if len(name) < 3 || len(name) > maxBucketNameLen {
		return false
	}
	return strings.IndexByte(name, '/') < 0 && strings.IndexByte(name, 0xff) < 0
}
