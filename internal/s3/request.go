package s3

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

// Config is what the handler needs from Ceph configuration.
type Config struct {
	// DNSNames are rgw_dns_name's entries plus the zonegroup's hostnames;
	// empty, with WebsiteNames, means path-style addressing only.
	DNSNames []string
	// WebsiteNames are rgw_dns_s3website_name, taken whole, plus the
	// zonegroup's hostnames_s3website (vhost.Names.Website).
	WebsiteNames []string
	// Website and WebsiteFirst are cephconf.APIs's: s3website enabled, and
	// ranked above s3. WebsiteFirst counts only with Website, as preprocess
	// checks both.
	Website      bool
	WebsiteFirst bool
	// MaxConcurrent is rgw_max_concurrent_requests; 0 means unlimited.
	MaxConcurrent int
	// InFlight is the count of requests MaxConcurrent caps, which the admin
	// handler shares, as radosgw's frontend throttles every request through
	// one scheduler; nil gives the handler a count of its own.
	InFlight *atomic.Int64
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

// maxObjectNameLen is RGWHandler_REST's MAX_OBJ_NAME_LEN
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.h#L556 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L568 at v20.2.4).
const maxObjectNameLen = 1024

// ParseRequest parses Host, path and query once, in RGWREST::preprocess and
// RGWHandler_REST_S3::init_from_header's order. The path and query are the
// request target as vhost.RequestTarget splits it, the split SigV2 and SigV4
// sign, never net/http's parse of it. Errors are op.Error values,
// the two radosgw answers before authentication: InvalidRequest for a NUL in
// the path and MethodNotAllowed for a method no S3 op serves. The tenant,
// bucket and object names are PostAuthInit's to check. A bucket token with
// nothing after its first colon names no bucket; it stays whole in
// Req.Bucket, unsplit, so the request keeps the scope radosgw dispatches it
// at, and PostAuthInit rejects it.
func ParseRequest(req *http.Request, cfg Config, now time.Time) (Parsed, error) {
	host := vhost.Hostname(req.Host)
	target := vhost.Resolve(host, cfg.hostNames())
	// With s3website enabled and ranked above s3 every request is a website
	// one, as at v20.2.4
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2162-L2168
	// and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2193-L2198);
	// v19.2.7 flags only one with a Host
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2150-L2156),
	// and rgw-go takes v20.2.4's rule on both releases.
	website := target.Website || cfg.Website && cfg.WebsiteFirst
	rawPath, rawQuery := vhost.RequestTarget(req)
	uri := DecodedURI(req, cfg)
	if strings.IndexByte(uri, 0) >= 0 {
		return Parsed{}, op.ErrInvalidRequest // ERR_ZERO_IN_URL
	}
	if !knownMethod(req.Method) {
		return Parsed{}, op.ErrMethodNotAllowed
	}

	query := parseArgs(rawQuery)
	r := &op.Request{
		Time:          now,
		Method:        req.Method,
		Host:          strings.ToLower(host),
		HTTPHost:      httpHost(req),
		Domain:        target.Domain,
		Website:       website,
		Path:          rgwtext.URLDecode(rawPath, false),
		RawPath:       rawPath,
		Query:         query,
		RawQuery:      rawQuery,
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

// httpHost is the Host header beast files as HTTP_HOST, nil when the request
// sent none. net/http refuses an HTTP/1.1 request without one, so an empty
// Host is one sent empty there, and on HTTP/1.0 is taken for none.
func httpHost(req *http.Request) *string {
	if req.Host == "" && !req.ProtoAtLeast(1, 1) {
		return nil
	}
	return &req.Host
}

// DecodedURI is decoded_uri, the path radosgw picks a request's API and
// bucket by: url_decode of request_uri as preprocess leaves it, the target's
// path with a bucket the Host names in front
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2154-L2161
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2182 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2171-L2178
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2204 at v20.2.4).
// The path is the one SigV2 and SigV4 sign, so a request is routed on the
// path it is authenticated for. It is not net/http's URL path for an
// absolute-form target: get_abs_path takes the path from the first '/' after
// the authority, in the query if that is where it lies, and keeps a target
// of another scheme whole, which names no bucket.
func DecodedURI(req *http.Request, cfg Config) string {
	return rgwtext.URLDecode(requestURI(req, cfg), false)
}

// MalformedPath reports whether the URI DecodedURI decodes, the target's
// path with a bucket the Host names in front, holds a '%' without two hex
// digits after it. net/http refuses one with 400 in an origin-form or
// absolute-form path, but checks neither a path radosgw takes from the
// query or keeps whole, as in `http://h?x=/b/%zz`, nor the Host. At a bad
// hex digit url_decode would empty the whole URI, the Host's bucket
// included, while SigV4 signs that bucket in the Host and the path as "/",
// the bucket's root; a malformed escape in the bucket itself serves another
// bucket or none (docs/exclusions.md).
func MalformedPath(req *http.Request, cfg Config) bool {
	_, err := url.PathUnescape(requestURI(req, cfg))
	return err != nil
}

// requestURI is req_info.request_uri as RGWREST::preprocess leaves it: the
// target's path as the client sent it (vhost.RequestTarget), with a bucket
// the Host names put in front of it.
func requestURI(req *http.Request, cfg Config) string {
	path, _ := vhost.RequestTarget(req)
	if b := vhost.Resolve(vhost.Hostname(req.Host), cfg.hostNames()).Bucket; b != "" {
		return withHostBucket(b, path)
	}
	return path
}

// hostNames are the hostnames RGWREST::preprocess finds a Host in.
func (c Config) hostNames() vhost.Names {
	return vhost.Names{S3: c.DNSNames, Website: c.WebsiteNames, WebsiteAPI: c.Website}
}

// withHostBucket is preprocess's "/" and bucket b in front of uri, with a
// "/" between them unless uri starts with one.
func withHostBucket(b, uri string) string {
	sep := "/"
	if strings.HasPrefix(uri, "/") {
		sep = ""
	}
	return "/" + b + sep + uri
}

// PostAuthInit finishes parsing once the request is authenticated, as
// RGWHandler_REST_S3::postauth_init does, which radosgw runs after get_op's
// MethodNotAllowed and verify_requester
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L325-L365 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L367 at v20.2.4).
// authTenant is the authenticated identity's op.Identity.Tenant;
// PostAuthInit gives it to p.Req.Tenant unless the URL named a tenant. It
// returns postauth_init's errors in that function's order
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4961-L5002 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5521-L5562 at v20.2.4):
// InvalidBucketName for a bucket token with nothing after its first colon,
// InvalidTenantName for a tenant of anything but letters, digits and
// underscores, InvalidObjectName for an object name over 1024 bytes or not
// valid UTF-8, then the bucket and tenant checks again for the copy source
// RGWHandler_REST_S3::init parsed, whatever the method, whose tenant and
// bucket it gives to p.Req.SrcTenant and p.Req.SrcBucket. radosgw makes none
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
			tenant, bucket := authTenant, src
			if t, b, found := strings.Cut(src, ":"); found {
				if b == "" {
					return op.ErrInvalidBucketName
				}
				tenant, bucket = t, b
			}
			if !validTenantName(tenant) {
				return op.ErrInvalidTenantName
			}
			r.SrcTenant, r.SrcBucket = tenant, bucket
		}
	}
	return nil
}

// validObjectName is RGWHandler_REST::validate_object_name
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1846-L1859 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1850-L1863 at v20.2.4):
// at most 1024 bytes, of UTF-8 as check_utf8 accepts it, which is what
// utf8.ValidString accepts.
func validObjectName(name string) bool {
	return len(name) <= maxObjectNameLen && utf8.ValidString(name)
}

// copySource is the x-amz-copy-source value RGWHandler_REST_S3::init parses
// into init_state.src_bucket: the last such header, as RGWEnv::set keeps the
// last, when neither x-amz-copy-source-range nor uploadId is present
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5026-L5041 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5586-L5601 at v20.2.4).
func copySource(r *op.Request) (string, bool) {
	v, ok := op.HeaderValue(r.Header, "X-Amz-Copy-Source")
	if _, ranged := op.HeaderValue(r.Header, "X-Amz-Copy-Source-Range"); !ok || ranged || r.Query.Has("uploadId") {
		return "", false
	}
	return v, true
}

// copySourceBucket is the bucket RGWCopyObj::parse_copy_location reads from
// an x-amz-copy-source value
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5329-L5374 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5895-L5940 at v20.2.4):
// the value up to its first "?", less one leading "/", decoded, then split
// at its first "/". It is false where parse_copy_location fails, an empty
// value or a missing or empty key, which init answers with 400 before
// authentication.
func copySourceBucket(v string) (string, bool) {
	name, _, _ := strings.Cut(v, "?")
	if name == "" {
		return "", false
	}
	bucket, key, ok := strings.Cut(rgwtext.URLDecode(strings.TrimPrefix(name, "/"), false), "/")
	if !ok || key == "" {
		return "", false
	}
	return bucket, true
}

// nameArgs are the query arguments whose value becomes part of a RADOS
// object name: an object's instance and a multipart upload's id.
var nameArgs = []string{"versionId", "uploadId"}

// refuseNUL is ErrInvalidArgument for a NUL in what of r becomes part of a
// RADOS object or xattr name, which go-ceph hands librados as a C string
// that ends at the NUL: any query argument's name, as an x-amz-meta-* one
// names an xattr; nameArgs' values; and the tenant, bucket, key or versionId
// of the copy source the ops read, as either parser reads it. radosgw checks
// only the path for a NUL and keeps one anywhere else in the name, which
// librados's C++ API takes whole (docs/exclusions.md).
func refuseNUL(r *op.Request) error {
	for name := range r.Query {
		if strings.IndexByte(name, 0) >= 0 {
			return fmt.Errorf("%w: a NUL in a query argument's name", op.ErrInvalidArgument)
		}
	}
	for _, name := range nameArgs {
		if strings.IndexByte(r.Query.Get(name), 0) >= 0 {
			return fmt.Errorf("%w: a NUL in %s", op.ErrInvalidArgument, name)
		}
	}
	v, ok := op.HeaderValue(r.Header, "X-Amz-Copy-Source")
	if !ok {
		return nil
	}
	for _, parse := range []func(v, defaultTenant string) (string, string, meta.ObjKey, bool){parseCopySource, partCopySource} {
		tenant, bucket, key, _ := parse(v, "")
		if strings.IndexByte(tenant+bucket+key.Name+key.Instance, 0) >= 0 {
			return fmt.Errorf("%w: a NUL in x-amz-copy-source", op.ErrInvalidArgument)
		}
	}
	return nil
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
		name, val, _ := strings.Cut(rgwtext.URLDecode(pair, true), "=")
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
