// Package vhost reads the bucket a virtual-hosted request's Host names, as
// radosgw's RGWREST::preprocess does. The S3 handler routes by that bucket and
// SigV2 signs it in front of the path, so both read it here and cannot
// disagree.
package vhost

import (
	"net"
	"strings"
)

// maxBucketNameLen is RGWHandler_REST's MAX_BUCKET_NAME_LEN (rgw_rest.h:555
// at v19.2.6, :567 at v20.2.4).
const maxBucketNameLen = 255

// Hostname is the Host header as RGWREST::preprocess leaves it. req_info first
// drops a trailing ':' and the digits after it (rgw_common.cc:246-261 at
// v19.2.6, :256-271 at v20.2.4); preprocess then keeps what a leading '[' and
// the first ']' enclose, or cuts at the first ':' (rgw_rest.cc:2048-2060 at
// v19.2.6, :2065-2077 at v20.2.4). radosgw keeps the case the client sent.
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
// hostnames (rgw_rest.cc:2048-2143 at v19.2.6, :2065-2160 at v20.2.4): the
// subdomain of a configured name, or, when names are configured, the whole
// host if it is not a configured name, looks like no IP address and passes
// validate_bucket_name (the CNAME case). "" names no bucket.
// rgw_resolve_cname's DNS lookup has no counterpart.
func Bucket(host string, names []string) string {
	if host == "" {
		return ""
	}
	domain, subdomain := findHostInDomains(host, names)
	if subdomain == "" && domain != host && hasName(names) &&
		!looksLikeIPAddress(host) && validBucketName(host) {
		return host
	}
	return subdomain
}

// findHostInDomains is rgw_find_host_in_domains (rgw_rest.cc:260-287 at
// v19.2.6 and v20.2.4). radosgw walks its hostname set in sorted order and
// stops at the first name host equals or ends in after a ".", comparing
// without regard to ASCII case; that first match is the least matching name.
// The domain and subdomain are cut from host, so they keep the client's case.
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

// looksLikeIPAddress is looks_like_ip_address (rgw_rest_s3.h:801-826 at
// v19.2.6, :831-856 at v20.2.4): an address inet_pton(AF_INET6) accepts, or
// digits split by exactly three dots with none leading or doubled, whatever
// the digits' values.
func looksLikeIPAddress(s string) bool {
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

// validBucketName is RGWHandler_REST::validate_bucket_name (rgw_rest.cc
// :1815-1840 at v19.2.6, :1819-1844 at v20.2.4), which radosgw applies only to
// a Host it might take as a bucket: empty, which names no bucket, or 3 to 255
// bytes with no '/' and no 0xff byte. A bucket in the path is left to the
// bucket lookup and to CreateBucket's own check.
func validBucketName(name string) bool {
	if name == "" {
		return true
	}
	if len(name) < 3 || len(name) > maxBucketNameLen {
		return false
	}
	return strings.IndexByte(name, '/') < 0 && strings.IndexByte(name, 0xff) < 0
}
