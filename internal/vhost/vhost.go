// Package vhost reads the bucket a virtual-hosted request's Host names, the
// domain it is in and whether that makes a website request, as radosgw's
// RGWREST::preprocess does, and the request target req_info splits
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

// Names are the hostnames RGWREST::preprocess finds a Host in, as
// rgw_rest_init gathers them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L215-L238 at v19.2.7
// and v20.2.4).
type Names struct {
	// S3 are rgw_dns_name's entries and the zonegroup's hostnames.
	S3 []string
	// Website are rgw_dns_s3website_name, taken whole, and the zonegroup's
	// hostnames_s3website.
	Website []string
	// WebsiteAPI is s3website in rgw_enable_apis, without which preprocess
	// finds no Host in Website. Website names count toward the configured
	// names that let a Host name the bucket either way.
	WebsiteAPI bool
}

// Target is what RGWREST::preprocess reads from a Host.
type Target struct {
	// Bucket is the bucket the Host names; "" names none.
	Bucket string
	// Domain is the configured name the Host is in, as cut from the Host with
	// the client's case, which preprocess makes s->info.domain
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2168-L2170 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2180-L2182 at v20.2.4);
	// "" when it is in none, where preprocess takes rgw_dns_name.
	Domain string
	// Website reports a Host in a website name, which makes the request a
	// website one.
	Website bool
}

// Resolve is what RGWREST::preprocess reads from host, a Host as Hostname
// leaves it, under n
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2053-L2181 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2065-L2193 at v20.2.4).
// Under the website API a Host in a website name takes that name's domain
// and subdomain over an S3 name's. The bucket is the subdomain of the name
// the Host is in or, when any name is configured, the whole host if it is in
// none, looks like no IP address and passes validate_bucket_name (the CNAME
// case), which makes no website request. rgw_resolve_cname's DNS lookup has
// no counterpart.
func Resolve(host string, n Names) Target {
	var t Target
	t.Domain, t.Bucket = findHostInDomains(host, n.S3)
	if n.WebsiteAPI {
		if domain, subdomain := findHostInDomains(host, n.Website); domain != "" {
			t.Domain, t.Bucket, t.Website = domain, subdomain, true
		}
	}
	if t.Bucket == "" && t.Domain != host && (hasName(n.S3) || hasName(n.Website)) &&
		!LooksLikeIPAddress(host) && validBucketName(host) {
		t.Bucket = host
	}
	return t
}

// Bucket is Resolve's bucket for host under the S3 names alone.
func Bucket(host string, names []string) string {
	return Resolve(host, Names{S3: names}).Bucket
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
