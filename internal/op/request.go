package op

import (
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Scope is the resource level a request addresses.
type Scope uint8

// The scopes, from the URL: no bucket, a bucket, a bucket and an object; and
// the two FromRADOS distinguishes for metadata ops.
const (
	ScopeService Scope = iota
	ScopeBucket
	ScopeObject
	ScopeUser
	ScopeUpload
)

// Request is the protocol-neutral request the ops see. The protocol layer
// fills everything above Identity from the HTTP request, auth fills Identity
// and AuthRoute, and the ops fill the rest.
type Request struct {
	// ID is radosgw's transaction id, the x-amz-request-id.
	ID   string
	Time time.Time

	Method string
	// Host is the Host header without its port, lowercased; "" when absent.
	Host string
	// HTTPHost is the Host header as the client sent it, its case and port
	// kept, radosgw's HTTP_HOST; nil for a request that sent none.
	HTTPHost *string
	// Domain is the configured name the Host matched, as cut from the Host,
	// "" when it matched none: what RGWREST::preprocess sets s->info.domain
	// to before it falls back to rgw_dns_name.
	Domain string
	// Path is the request path decoded once; RawPath is as the client sent it,
	// which SigV4 canonicalizes.
	Path    string
	RawPath string
	// Query holds the decoded query; a key containing "X-Amz-" is lowercased
	// except for its dashes, as RGWHTTPArgs::parse does.
	Query    url.Values
	RawQuery string
	Header   http.Header
	// Body is the request body; auth may replace it with a chunk-verifying reader.
	Body io.Reader
	// ContentLength is the declared length, -1 when unknown.
	ContentLength int64
	// RemoteAddr is the client's address as net/http gives it, host and port.
	// radosgw's REMOTE_ADDR is the address alone
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L92 at v19.2.6
	// and v20.2.4), so consumers take the address through one shared helper
	// that strips the port, never from this string directly.
	RemoteAddr string
	Referer    string
	TLS        bool

	// Tenant is the bucket's tenant: explicit from "tenant:bucket" in the URL,
	// otherwise the identity's tenant once auth has run. "" is the default tenant.
	Tenant string
	// Bucket is "" for service scope.
	Bucket string
	// Object has an empty Name for service and bucket scope; Instance carries
	// the versionId query parameter.
	Object meta.ObjKey
	// SrcTenant and SrcBucket are radosgw's src_tenant_name and
	// src_bucket_name: the bucket of the x-amz-copy-source that
	// RGWHandler_REST_S3::init parses, which postauth_init resolves under
	// the identity's tenant once auth has run. SrcBucket is "" when the
	// request names none.
	SrcTenant, SrcBucket string

	Identity Identity
	// MFAVerified is radosgw's s->mfa_verified
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1376 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1420 at v20.2.4):
	// that the request's x-amz-mfa named a device of the requester and its
	// current code
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5026-L5029 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556-L5559 at v20.2.4).
	// No handler sets it, so every change that needs it is refused.
	MFAVerified bool
	// AuthRoute is AuthResult.Route, which says what of the query a read
	// may take.
	AuthRoute AuthRoute
	Env       *Env

	// BucketRec and ObjState are loaded by the op's Init.
	BucketRec *BucketRecord
	ObjState  *ObjectState
	// List is set by a bucket listing once it has read its parameters, before
	// it authorizes; nil for every other request.
	List *ListConditions

	// Status and the byte counters are filled by the protocol layer as the
	// response goes out, for Complete, metrics and the usage log.
	Status   int
	BytesIn  int64
	BytesOut int64
}

// ListConditions are the listing parameters RGWListBucket::verify_permission
// adds to the IAM environment once get_params has read them
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3033-L3045 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3267-L3279 at v20.2.4):
// s3:prefix and s3:delimiter when not empty, and s3:max-keys always, the
// count the listing returns at most, after parse_value_and_bound has bounded
// it.
type ListConditions struct {
	Prefix    string
	Delimiter string
	MaxKeys   int
}

// HeaderValue is a request header as radosgw's RGWEnv holds it: beast sets
// one variable per header field in arrival order, so the last of a repeated
// header is the one radosgw sees
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L36-L65,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_env.cc#L22-L25 at v19.2.6
// and v20.2.4). A header present with an empty value is present.
func HeaderValue(h http.Header, name string) (string, bool) {
	vs := h.Values(name)
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

// Scope reports the resource level the request addresses.
func (r *Request) Scope() Scope {
	switch {
	case r.Bucket == "":
		return ScopeService
	case r.Object.Name == "":
		return ScopeBucket
	default:
		return ScopeObject
	}
}
