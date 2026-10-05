package s3

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

// Config is what the handler needs from Ceph configuration.
type Config struct {
	// DNSNames are rgw_dns_name's entries plus the zonegroup's hostnames;
	// empty means path-style addressing only.
	DNSNames []string
	// MaxConcurrent is rgw_max_concurrent_requests; 0 means unlimited.
	MaxConcurrent int
	// TransIDSuffix is op.TransIDSuffix(instance id, zone name).
	TransIDSuffix string
	// ServerHeader is the Server header value, "Ceph Object Gateway (<release>)".
	ServerHeader string
}

// Parsed is a request after Host, path and query are parsed, before auth.
type Parsed struct {
	Req *op.Request
	// ExplicitTenant is set when the URL named "tenant:bucket"; otherwise the
	// tenant is the identity's once auth has run.
	ExplicitTenant bool
}

// maxObjectNameLen is RGWHandler_REST's MAX_OBJ_NAME_LEN (rgw_rest.h:556 at
// v19.2.6, :568 at v20.2.4).
const maxObjectNameLen = 1024

// ParseRequest parses Host, path and query once, in RGWREST::preprocess and
// RGWHandler_REST_S3::init_from_header's order. Errors are op.Error values,
// the two radosgw answers before authentication: InvalidRequest for a NUL in
// the path and MethodNotAllowed for a method no S3 op serves. The tenant,
// bucket and object names are PostAuthInit's to check. A bucket token with
// nothing after its first colon names no bucket; it stays whole in
// Req.Bucket, unsplit, so the request keeps the scope radosgw dispatches it
// at, and PostAuthInit rejects it.
func ParseRequest(req *http.Request, cfg Config, now time.Time) (Parsed, error) {
	host := vhost.Hostname(req.Host)
	rawPath := req.URL.EscapedPath()
	uri := DecodedURI(req, cfg)
	if strings.IndexByte(uri, 0) >= 0 {
		return Parsed{}, op.ErrInvalidRequest // ERR_ZERO_IN_URL
	}
	if !knownMethod(req.Method) {
		return Parsed{}, op.ErrMethodNotAllowed
	}

	query := parseArgs(req.URL.RawQuery)
	r := &op.Request{
		Time:          now,
		Method:        req.Method,
		Host:          strings.ToLower(host),
		Path:          req.URL.Path,
		RawPath:       rawPath,
		Query:         query,
		RawQuery:      req.URL.RawQuery,
		Header:        req.Header,
		Body:          req.Body,
		ContentLength: req.ContentLength,
		RemoteAddr:    req.RemoteAddr,
		Referer:       req.Referer(),
		TLS:           req.TLS != nil,
	}
	p := Parsed{Req: r}

	token, object := splitURI(uri)
	if token == "" {
		return p, nil
	}
	r.Bucket = token
	// rgw_parse_url_bucket: "tenant:bucket" splits at the first colon, and
	// ":bucket" names the legacy tenant explicitly.
	if tenant, bucket, ok := strings.Cut(token, ":"); ok && bucket != "" {
		r.Tenant, r.Bucket = tenant, bucket
		p.ExplicitTenant = true
	}
	if object != "" {
		r.Object = meta.ObjKey{Name: object, Instance: query.Get("versionId")}
	}
	return p, nil
}

// DecodedURI is the path radosgw picks a request's API and bucket by, its
// decoded_uri: with a bucket the Host names, preprocess puts the bucket in
// front of the undecoded URI and decodes the two together
// (rgw_rest.cc:2154-2161 and :2182 at v19.2.6, :2171-2177 and :2204 at
// v20.2.4); otherwise it is the decoded path.
func DecodedURI(req *http.Request, cfg Config) string {
	b := vhost.Bucket(vhost.Hostname(req.Host), cfg.DNSNames)
	if b == "" {
		return req.URL.Path
	}
	rawPath := req.URL.EscapedPath()
	sep := "/"
	if strings.HasPrefix(rawPath, "/") {
		sep = ""
	}
	return urlDecode("/"+b+sep+rawPath, false)
}

// PostAuthInit finishes parsing once the request is authenticated, as
// RGWHandler_REST_S3::postauth_init does, which radosgw runs after get_op's
// MethodNotAllowed and verify_requester (rgw_process.cc:325-365 at v19.2.6,
// :327-367 at v20.2.4). authTenant is the authenticated identity's
// op.Identity.Tenant; PostAuthInit gives it to p.Req.Tenant unless the URL
// named a tenant. It returns postauth_init's errors in that function's order
// (rgw_rest_s3.cc:4961-5002 at v19.2.6, :5521-5562 at v20.2.4):
// InvalidBucketName for a bucket token with nothing after its first colon,
// InvalidTenantName for a tenant of anything but letters, digits and
// underscores, InvalidObjectName for an object name over 1024 bytes or not
// valid UTF-8, then the bucket and tenant checks again for the copy source
// RGWHandler_REST_S3::init parsed, whatever the method. radosgw makes none
// of these checks earlier: init's own tenant and object checks read what
// postauth_init has not yet set, so they pass. Its x-amz-mfa verification
// fails no request.
func PostAuthInit(p Parsed, authTenant string) error {
	r := p.Req
	if !p.ExplicitTenant {
		// rgw_parse_url_bucket on the token ParseRequest left unsplit.
		r.Tenant = authTenant
		if tenant, bucket, ok := strings.Cut(r.Bucket, ":"); ok {
			if bucket == "" {
				return op.ErrInvalidBucketName
			}
			r.Tenant, r.Bucket = tenant, bucket
		}
	}
	if !validTenantName(r.Tenant) {
		return op.ErrInvalidTenantName
	}
	if r.Bucket != "" && r.Object.Name != "" && !validObjectName(r.Object.Name) {
		return op.ErrInvalidObjectName
	}
	if v, ok := copySource(r); ok {
		if src, parsed := copySourceBucket(v); parsed && src != "" {
			tenant := authTenant
			if t, bucket, found := strings.Cut(src, ":"); found {
				if bucket == "" {
					return op.ErrInvalidBucketName
				}
				tenant = t
			}
			if !validTenantName(tenant) {
				return op.ErrInvalidTenantName
			}
		}
	}
	return nil
}

// validObjectName is RGWHandler_REST::validate_object_name (rgw_rest.cc
// :1846-1859 at v19.2.6, :1850-1863 at v20.2.4): at most 1024 bytes, of
// UTF-8 as check_utf8 accepts it, which is what utf8.ValidString accepts.
func validObjectName(name string) bool {
	return len(name) <= maxObjectNameLen && utf8.ValidString(name)
}

// copySource is the x-amz-copy-source value RGWHandler_REST_S3::init parses
// into init_state.src_bucket: the last such header, as RGWEnv::set keeps the
// last, when neither x-amz-copy-source-range nor uploadId is present
// (rgw_rest_s3.cc:5026-5041 at v19.2.6, :5586-5601 at v20.2.4).
func copySource(r *op.Request) (string, bool) {
	v, ok := op.HeaderValue(r.Header, "X-Amz-Copy-Source")
	if _, ranged := op.HeaderValue(r.Header, "X-Amz-Copy-Source-Range"); !ok || ranged || r.Query.Has("uploadId") {
		return "", false
	}
	return v, true
}

// copySourceBucket is the bucket RGWCopyObj::parse_copy_location reads from
// an x-amz-copy-source value (rgw_op.cc:5329-5374 at v19.2.6, :5895-5940 at
// v20.2.4): the value up to its first "?", less one leading "/", decoded,
// then split at its first "/". It is false where parse_copy_location fails,
// an empty value or a missing or empty key, which init answers with 400
// before authentication.
func copySourceBucket(v string) (string, bool) {
	name, _, _ := strings.Cut(v, "?")
	if name == "" {
		return "", false
	}
	bucket, key, ok := strings.Cut(urlDecode(strings.TrimPrefix(name, "/"), false), "/")
	if !ok || key == "" {
		return "", false
	}
	return bucket, true
}

// knownMethod reports whether op_from_method maps method to an op the S3
// handlers serve. COPY maps to one only Swift's handlers serve.
func knownMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodHead,
		http.MethodPost, http.MethodOptions:
		return true
	default:
		return false
	}
}

// splitURI is init_from_header's split of the decoded URI: the first segment
// is the bucket token and the rest, slashes included, the object name. A URI
// without a leading slash or with an empty first segment names no bucket.
func splitURI(uri string) (token, object string) {
	rest, ok := strings.CutPrefix(uri, "/")
	if !ok {
		return "", ""
	}
	token, object, _ = strings.Cut(rest, "/")
	return token, object
}

// validTenantName is rgw_validate_tenant_name: letters, digits and
// underscores, as isalnum reads them in the C locale.
func validTenantName(tenant string) bool {
	for i := range len(tenant) {
		c := tenant[i]
		if c != '_' && !isASCIIAlnum(c) {
			return false
		}
	}
	return true
}

func isASCIIAlnum(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// parseArgs is RGWHTTPArgs::parse. Each "&"-separated pair is decoded whole
// and then split at its first "=", so an escaped "=" separates; a malformed
// escape empties the pair rather than failing the request; a name containing
// "X-Amz-" has its ASCII letters lowercased; and a repeated name keeps its
// last value, as radosgw's val_map does, so every key holds one value.
func parseArgs(raw string) url.Values {
	args := url.Values{}
	if raw == "" {
		return args
	}
	for pair := range strings.SplitSeq(strings.TrimPrefix(raw, "?"), "&") {
		name, val, _ := strings.Cut(urlDecode(pair, true), "=")
		if strings.Contains(name, "X-Amz-") {
			name = lowerASCIIString(name)
		}
		args[name] = []string{val}
	}
	return args
}

func lowerASCIIString(s string) string {
	b := []byte(s)
	for i, c := range b {
		b[i] = lowerASCII(c)
	}
	return string(b)
}

// urlDecode is radosgw's url_decode (rgw_common.cc:1701-1734 at v19.2.6).
// "%XX" decodes and, in a query, "+" is a space; a "%" with fewer than two
// characters after it ends the result there, and one followed by a non-hex
// character makes the whole result empty.
func urlDecode(s string, inQuery bool) string {
	if strings.IndexByte(s, '%') < 0 && strings.IndexByte(s, '+') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '%':
			if len(s)-i < 3 {
				return b.String()
			}
			hi, okHi := unhex(s[i+1])
			lo, okLo := unhex(s[i+2])
			if !okHi || !okLo {
				return ""
			}
			b.WriteByte(hi<<4 | lo)
			i += 2
		case c == '+' && inQuery:
			b.WriteByte(' ')
		default:
			if c == '?' {
				inQuery = true
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
