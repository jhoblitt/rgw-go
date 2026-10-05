package authz

import (
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// envPair is one key and value radosgw emplaces in a request's environment.
type envPair struct{ key, value string }

// BuildEnv is the IAM environment radosgw evaluates action a with:
// rgw_build_iam_environment's keys (rgw_op.cc:848-910 at v19.2.6, :884-946 at
// v20.2.4), then the keys the op for a adds in its verify_permission. Tag keys
// are not among them; the evaluator adds those where the op does.
//
// radosgw's environment is an unordered_multimap, and on Tentacle which of a
// key's values a typed condition reads depends on how many pairs the
// environment held when each was added (policy.Env), so the keys are added in
// radosgw's order.
func BuildEnv(cfg Config, r *op.Request, a policy.Action) policy.Env {
	return buildEnv(cfg, r, a, nil)
}

// buildEnv is BuildEnv with addTags, when non-nil, run where the op for a
// adds its tag keys: before its own keys for CopyObject's destination check
// and the two multipart ops that check s3:PutObject, after them for every
// other op.
func buildEnv(cfg Config, r *op.Request, a policy.Action, addTags func(*policy.Env)) policy.Env {
	env := baseEnv(cfg, r)
	keys, tagsFirst := opKeys(cfg, r, a)
	if tagsFirst && addTags != nil {
		addTags(&env)
	}
	for _, p := range keys {
		env.Add(p.key, p.value)
	}
	if !tagsFirst && addTags != nil {
		addTags(&env)
	}
	return env
}

// baseEnv is rgw_build_iam_environment's keys alone, the environment
// read_obj_policy extends with s3:prefix for a missing object's ListBucket
// check (rgw_op.cc:438 at v19.2.6, :468 at v20.2.4).
func baseEnv(cfg Config, r *op.Request) policy.Env {
	var env policy.Env
	for _, p := range baseKeys(cfg, r) {
		env.Add(p.key, p.value)
	}
	return env
}

// baseKeys are rgw_build_iam_environment's keys in its order.
// aws:CurrentTime is the epoch in seconds and aws:EpochTime the ISO 8601
// time, the reverse of AWS's names, as radosgw sets them.
func baseKeys(cfg Config, r *op.Request) []envPair {
	now := cfg.now().UTC()
	ps := []envPair{
		{"aws:CurrentTime", strconv.FormatInt(now.Unix(), 10)},
		{"aws:EpochTime", now.Format("2006-01-02T15:04:05.000000000Z")},
		{"aws:PrincipalType", "User"},
	}
	if v, ok := header(r.Header, "Referer"); ok {
		ps = append(ps, envPair{"aws:Referer", v})
	}
	if secureTransport(cfg, r) {
		ps = append(ps, envPair{"aws:SecureTransport", "true"})
	}
	if ip, ok := sourceIP(cfg, r); ok {
		ps = append(ps, envPair{"aws:SourceIp", ip})
	}
	if v, ok := header(r.Header, "User-Agent"); ok {
		ps = append(ps, envPair{"aws:UserAgent", v})
	}
	if u := r.Identity.User; u != nil {
		ps = append(ps, envPair{"aws:username", u.UserID.ID})
	}
	// get_subuser() goes in through c_str(), which ends it at a NUL.
	subuser, _, _ := strings.Cut(r.Identity.SubUser, "\x00")
	ps = append(ps, envPair{"rgw:subuser", subuser})
	sts := "false"
	if _, ok := header(r.Header, "X-Amz-Security-Token"); ok {
		sts = "true"
	}
	return append(ps, envPair{"sts:authentication", sts})
}

// header is a request header as radosgw's RGWEnv holds it: beast sets one
// variable per header field in arrival order, so the last of a repeated
// header is the one radosgw sees (rgw_asio_client.cc:36-65, rgw_env.cc:22-25
// at v19.2.6 and v20.2.4). A header present with an empty value is present.
func header(h http.Header, name string) (string, bool) {
	vs := h.Values(name)
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

// secureTransport is rgw_transport_is_secure (rgw_common.cc:1071-1094 at
// v19.2.6, :1084-1107 at v20.2.4).
func secureTransport(cfg Config, r *op.Request) bool {
	if r.TLS {
		return true
	}
	if !cfg.TrustForwardedHTTPS {
		return false
	}
	if v, ok := header(r.Header, "Forwarded"); ok && strings.Contains(v, "proto=https") {
		return true
	}
	v, ok := header(r.Header, "X-Forwarded-Proto")
	return ok && v == "https"
}

// sourceIP is aws:SourceIp: the CGI variable rgw_remote_addr_param names,
// REMOTE_ADDR when it is empty, cut at its first comma only when the
// parameter is exactly HTTP_X_FORWARDED_FOR (rgw_op.cc:869-886 at v19.2.6,
// :905-922 at v20.2.4). A parameter in other capitals finds the header but
// keeps the whole proxy chain, as radosgw's does (docs/ceph-upstream-bugs.md,
// "radosgw cuts X-Forwarded-For only for an rgw_remote_addr_param written in
// capitals").
func sourceIP(cfg Config, r *op.Request) (string, bool) {
	name := cfg.RemoteAddrParam
	if name == "" {
		name = "REMOTE_ADDR"
	}
	ip, ok := cgiVar(r, name)
	if ok && cfg.RemoteAddrParam == "HTTP_X_FORWARDED_FOR" {
		ip, _, _ = strings.Cut(ip, ",")
	}
	return ip, ok
}

// cgiVar is the CGI variable name in radosgw's RGWEnv, whose names compare
// without regard to case: REMOTE_ADDR, the client's address alone
// (rgw_asio_client.cc:92 at v19.2.6 and v20.2.4), CONTENT_LENGTH and
// CONTENT_TYPE, and HTTP_ followed by any other header's name in capitals
// with '-' and '_' swapped (:40-65). rgw-go holds neither the other variables
// beast sets, the request line's among them, nor the headers net/http takes
// out of a server request: Host and Transfer-Encoding always, and Trailer and
// Content-Length from a chunked one (net/http/server.go:1087,
// net/http/transfer.go:644, :727, :790 in Go 1.27). For those it reports
// none.
func cgiVar(r *op.Request, name string) (string, bool) {
	switch upper := strings.ToUpper(name); upper {
	case "REMOTE_ADDR":
		if r.RemoteAddr == "" {
			return "", false
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr, true
		}
		return host, true
	case "CONTENT_LENGTH":
		return header(r.Header, "Content-Length")
	case "CONTENT_TYPE":
		return header(r.Header, "Content-Type")
	case "HTTP_VERSION", "HTTP_HOST", "HTTP_CONTENT_LENGTH", "HTTP_CONTENT_TYPE":
		return "", false
	default:
		field, ok := strings.CutPrefix(upper, "HTTP_")
		if !ok {
			return "", false
		}
		return header(r.Header, swapDashes(field))
	}
}

// swapDashes swaps '-' and '_', the mapping between a header's name and its
// CGI variable's.
func swapDashes(s string) string {
	return strings.Map(func(c rune) rune {
		switch c {
		case '-':
			return '_'
		case '_':
			return '-'
		}
		return c
	}, s)
}

// opKeys are the keys the op for a adds to the environment before it
// authorizes, in its order, and whether it adds its tag keys before them.
// Most ops add none. Each case is one verify_permission; citations are
// rgw_op.cc at v19.2.6, then v20.2.4.
func opKeys(cfg Config, r *op.Request, a policy.Action) (keys []envPair, tagsFirst bool) {
	switch a {
	case policy.S3ListBucket, policy.S3ListBucketVersions:
		// RGWListBucket (:3033-3060, :3267-3294). HEAD bucket is
		// RGWStatBucket, which also checks s3:ListBucket and adds nothing.
		if r.Method == http.MethodGet && r.Object.Name == "" {
			return listKeys(cfg, r), false
		}
	case policy.S3PutObject:
		switch r.Method {
		case http.MethodPut:
			if isCopyObject(r) {
				// RGWCopyObj's destination check (:5477-5485, :6043-6051).
				return copyKeys(r), true
			}
			// RGWPutObj, which serves UploadPart and UploadPartCopy too
			// (:3966-3980, :4175-4189).
			return putObjectKeys(cfg, r), false
		case http.MethodPost:
			// RGWInitMultipart and RGWCompleteMultipart (:6273-6278,
			// :6347-6352; :6943-6948, :7023-7028), which read their
			// parameters only once authorized. A POST naming neither is a
			// browser upload, which rgw-go does not serve.
			if hasQuery(r, "uploads") || hasQuery(r, "uploadId") {
				return cryptKeys(cfg, r, false), true
			}
		}
	case policy.S3PutBucketAcl, policy.S3PutObjectAcl, policy.S3PutObjectVersionAcl:
		// RGWPutACLs (:5742-5744, :6320-6322).
		return append([]envPair{{"s3:x-amz-acl", cannedACL(r)}}, grantKeys(r)...), false
	}
	return nil, false
}

// listKeys are RGWListBucket's: the prefix and the delimiter when not empty,
// and always the max-keys it lists with, which parse_value_and_bound bounds
// by rgw_max_listing_results with 1000 when absent (rgw_rest_s3.h:160 at
// v19.2.6, :161 at v20.2.4). A max-keys that does not parse fails the
// request before radosgw adds a key; the op refuses it first in rgw-go too.
func listKeys(cfg Config, r *op.Request) []envPair {
	var ps []envPair
	if v := query(r, "prefix"); v != "" {
		ps = append(ps, envPair{"s3:prefix", v})
	}
	if v := query(r, "delimiter"); v != "" {
		ps = append(ps, envPair{"s3:delimiter", v})
	}
	if n, err := op.ParseValueAndBound(query(r, "max-keys"), 0, cfg.maxListing(), 1000); err == nil {
		ps = append(ps, envPair{"s3:max-keys", strconv.Itoa(n)})
	}
	return ps
}

// isCopyObject reports whether a PUT is routed to RGWCopyObj: it names a
// copy source, and neither a source range nor an upload id
// (RGWHandler_REST_S3::init, rgw_rest_s3.cc:5026-5029 at v19.2.6, :5586-5589
// at v20.2.4). Any other PUT that checks s3:PutObject is RGWPutObj.
func isCopyObject(r *op.Request) bool {
	_, src := header(r.Header, "X-Amz-Copy-Source")
	_, ranged := header(r.Header, "X-Amz-Copy-Source-Range")
	return src && !ranged && !hasQuery(r, "uploadId")
}

// copyKeys are RGWCopyObj's: the copy source header as sent, and the
// metadata directive when present.
func copyKeys(r *op.Request) []envPair {
	src, _ := header(r.Header, "X-Amz-Copy-Source")
	ps := []envPair{{"s3:x-amz-copy-source", src}}
	if v, ok := header(r.Header, "X-Amz-Metadata-Directive"); ok {
		ps = append(ps, envPair{"s3:x-amz-metadata-directive", v})
	}
	return ps
}

// putObjectKeys are RGWPutObj's: the grant headers, the canned ACL, the
// request's tags and the encryption parameters. The tags are x-amz-tagging
// as get_params parsed it; a header it refuses fails the request before
// verify_permission, so it adds none here.
func putObjectKeys(cfg Config, r *op.Request) []envPair {
	ps := append(grantKeys(r), envPair{"s3:x-amz-acl", cannedACL(r)})
	if h, ok := header(r.Header, "X-Amz-Tagging"); ok {
		if set, err := tags.ParseHeader(h, tags.MaxObjectTags); err == nil {
			for _, t := range set.Tags {
				ps = append(ps, envPair{"s3:RequestObjectTag/" + t.Key, t.Value})
			}
		}
	}
	return append(ps, cryptKeys(cfg, r, true)...)
}

// cannedACL is s->canned_acl, x-amz-acl or empty, which the ops add even when
// empty.
func cannedACL(r *op.Request) string {
	v, _ := header(r.Header, "X-Amz-Acl")
	return v
}

// grantHeaders are rgw_add_grant_to_iam_environment's headers and keys, in
// its order (rgw_op.cc:827-846 at v19.2.6, :863-882 at v20.2.4).
var grantHeaders = []struct{ header, key string }{
	{"X-Amz-Grant-Read", "s3:x-amz-grant-read"},
	{"X-Amz-Grant-Write", "s3:x-amz-grant-write"},
	{"X-Amz-Grant-Read-Acp", "s3:x-amz-grant-read-acp"},
	{"X-Amz-Grant-Write-Acp", "s3:x-amz-grant-write-acp"},
	{"X-Amz-Grant-Full-Control", "s3:x-amz-grant-full-control"},
}

// grantKeys are the grant headers present, when the request has any header
// starting with x-amz-grant (has_acl_header, rgw_rest_s3.cc:5024 at v19.2.6,
// :5584 at v20.2.4).
func grantKeys(r *op.Request) []envPair {
	if !hasACLHeader(r.Header) {
		return nil
	}
	var ps []envPair
	for _, g := range grantHeaders {
		if v, ok := header(r.Header, g.header); ok {
			ps = append(ps, envPair{g.key, v})
		}
	}
	return ps
}

func hasACLHeader(h http.Header) bool {
	for k := range h {
		if strings.HasPrefix(strings.ToUpper(k), "X-AMZ-GRANT") {
			return true
		}
	}
	return false
}

// cryptKeys are rgw_iam_add_crypt_attrs' keys (rgw_op.cc:760-774 at v19.2.6,
// :790-810 at v20.2.4, which adds the SSE-C algorithm), from the request's
// crypt attributes as they stand at verify_permission: the headers, and for
// RGWPutObj, whose get_params has run, the query string after them
// (map_qs_metadata, rgw_rest_s3.cc:2581-2595 and :2611 at v19.2.6, :2742-2756
// and :2772 at v20.2.4). get_params also fills in the bucket's default
// encryption, which rgw-go does not apply (docs/exclusions.md).
func cryptKeys(cfg Config, r *op.Request, withQuery bool) []envPair {
	names := []string{"server-side-encryption"}
	if cfg.Release >= denc.Tentacle {
		names = append(names, "server-side-encryption-customer-algorithm")
	}
	names = append(names, "server-side-encryption-aws-kms-key-id")
	var ps []envPair
	for _, n := range names {
		if v, ok := cryptAttr(r, n, withQuery); ok {
			ps = append(ps, envPair{"s3:x-amz-" + n, v})
		}
	}
	return ps
}

// metaPrefixes are the header prefixes init_meta_info maps to x-amz- names,
// in the order it visits them: it walks the request's variables sorted by
// name, so for one attribute the prefix sorting last wins
// (rgw_common.cc:413-460 at v19.2.6, :426-473 at v20.2.4).
var metaPrefixes = []string{"X-Account-", "X-Amz-", "X-Container-", "X-Dho-", "X-Goog-", "X-Object-", "X-Rgw-"}

// cryptAttr is the crypt attribute "x-amz-"+name: the header under the last
// metadata prefix carrying it, then, when withQuery, a query parameter of
// that name in any case, the last in name order winning.
func cryptAttr(r *op.Request, name string, withQuery bool) (string, bool) {
	var val string
	var found bool
	for _, p := range metaPrefixes {
		if v, ok := header(r.Header, p+name); ok {
			val, found = v, true
		}
	}
	if !withQuery {
		return val, found
	}
	for _, k := range slices.Sorted(maps.Keys(r.Query)) {
		if vs := r.Query[k]; lowerASCII(k) == "x-amz-"+name && len(vs) > 0 {
			val, found = vs[len(vs)-1], true
		}
	}
	return val, found
}

// lowerASCII is boost::to_lower_copy in the C locale radosgw runs in, which
// changes only ASCII letters.
func lowerASCII(s string) string {
	return strings.Map(func(c rune) rune {
		if 'A' <= c && c <= 'Z' {
			return c + 'a' - 'A'
		}
		return c
	}, s)
}

// query is RGWHTTPArgs::get: the last value of a repeated parameter, empty
// when absent.
func query(r *op.Request, name string) string {
	vs := r.Query[name]
	if len(vs) == 0 {
		return ""
	}
	return vs[len(vs)-1]
}

// hasQuery is RGWHTTPArgs::exists.
func hasQuery(r *op.Request, name string) bool {
	_, ok := r.Query[name]
	return ok
}
