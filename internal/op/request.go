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
// fills everything above Identity from the HTTP request, auth fills Identity,
// and the ops fill the rest.
type Request struct {
	// ID is radosgw's transaction id, the x-amz-request-id.
	ID   string
	Time time.Time

	Method string
	// Host is the Host header without its port, lowercased; "" when absent.
	Host string
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
	RemoteAddr    string
	Referer       string
	TLS           bool

	// Tenant is the bucket's tenant: explicit from "tenant:bucket" in the URL,
	// otherwise the identity's tenant once auth has run. "" is the default tenant.
	Tenant string
	// Bucket is "" for service scope.
	Bucket string
	// Object has an empty Name for service and bucket scope; Instance carries
	// the versionId query parameter.
	Object meta.ObjKey

	Identity Identity
	Env      *Env

	// BucketRec and ObjState are loaded by the op's Init.
	BucketRec *BucketRecord
	ObjState  *ObjectState

	// Status and the byte counters are filled by the protocol layer as the
	// response goes out, for Complete, metrics and the usage log.
	Status   int
	BytesIn  int64
	BytesOut int64
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
