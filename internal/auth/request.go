package auth

import (
	"net"
	"net/http"
	"strings"
)

// aws4Algorithm is AWS4_HMAC_SHA256_STR.
const aws4Algorithm = "AWS4-HMAC-SHA256"

// sysParamPrefix is RGW_SYS_PARAM_PREFIX (rgw_common.h:78 at v19.2.6).
const sysParamPrefix = "rgwx-"

var (
	// subResources are the names RGWHTTPArgs::append files under
	// sub_resources with their value (rgw_common.cc:927-961 at v19.2.6,
	// :940-974 at v20.2.4).
	subResources = map[string]bool{
		"acl": true, "cors": true, "notification": true, "location": true, "logging": true, "usage": true,
		"lifecycle": true, "delete": true, "uploads": true, "partNumber": true, "uploadId": true, "versionId": true,
		"start-date": true, "end-date": true, "versions": true, "versioning": true, "website": true,
		"requestPayment": true, "torrent": true, "tagging": true, "append": true, "position": true,
		"policyStatus": true, "publicAccessBlock": true,
		"response-content-type": true, "response-content-language": true, "response-expires": true,
		"response-cache-control": true, "response-content-disposition": true, "response-content-encoding": true,
	}
	// adminSubResources are the names of which only the first a query
	// carries is filed, with an empty value (rgw_common.cc:962-975 at
	// v19.2.6, :975-988 at v20.2.4).
	adminSubResources = map[string]bool{
		"subuser": true, "key": true, "caps": true, "index": true, "policy": true, "quota": true,
		"list": true, "object": true, "sync": true,
	}
)

// requestView is the part of req_info the engines read.
type requestView struct {
	req *http.Request
	// rawPath is req_info.request_uri before RGWREST::preprocess prepends a
	// virtual-hosted bucket: the target as sent, absolute form reduced.
	rawPath string
	// rawQuery is req_info.request_params, the query as sent.
	rawQuery string
	// params is RGWHTTPArgs::val_map, sysParams its sys_val_map (the rgwx-
	// names) and subres its sub_resources.
	params, sysParams, subres map[string]string
}

// newRequestView builds req_info's view of req. REQUEST_URI is the target
// as sent; get_abs_path reduces an absolute-form one before the split at
// its first '?', so a query without one falls back to beast's QUERY_STRING
// (rgw_common.cc:209-245 at v19.2.6, :209-255 at v20.2.4;
// rgw_asio_client.cc:74-84).
func newRequestView(req *http.Request) *requestView {
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
	rv := &requestView{
		req: req, rawPath: path, rawQuery: query,
		params: map[string]string{}, sysParams: map[string]string{}, subres: map[string]string{},
	}
	rv.parseQuery()
	return rv
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

// parseQuery is RGWHTTPArgs::parse and append (rgw_common.cc:848-975 at
// v19.2.6, :861-988 at v20.2.4). Each '&'-separated piece is url-decoded as
// query text before it is split at its first '=', so an escaped '=' splits
// too. A name containing "X-Amz-" is lower-cased; the last of a repeated
// name wins.
func (rv *requestView) parseQuery() {
	if rv.rawQuery == "" {
		return
	}
	adminAdded := false
	for piece := range strings.SplitSeq(strings.TrimPrefix(rv.rawQuery, "?"), "&") {
		name, val, _ := strings.Cut(urlDecode(piece, true), "=")
		if strings.Contains(name, "X-Amz-") {
			name = asciiLower(name)
		}
		if strings.HasPrefix(name, sysParamPrefix) {
			rv.sysParams[name] = val
		} else {
			rv.params[name] = val
		}
		switch {
		case subResources[name]:
			rv.subres[name] = val
		case adminSubResources[name] && !adminAdded:
			rv.subres[name] = ""
			adminAdded = true
		}
	}
}

// param is RGWHTTPArgs::get.
func (rv *requestView) param(name string) (string, bool) {
	v, ok := rv.params[name]
	return v, ok
}

// header is RGWEnv::get for a signed-header token. Beast files a header as
// HTTP_ and its name upper-cased with '-' and '_' swapped, and radosgw maps a
// token the same way into a map that ignores case (rgw_asio_client.cc:36-66;
// rgw_auth_s3.cc:749-755, rgw_env.cc:46-53 at v19.2.6), so a token finds the
// header whose name equals it but for case, and the last of a repeated
// header. net/http keeps the Host header in req.Host and a chunked
// Transfer-Encoding in req.TransferEncoding; for an absolute-form target it
// puts the target's authority in req.Host and drops the Host header, which
// beast keeps. HTTP/1.1 requires a Host header, so an empty req.Host there
// is one sent empty, which beast files as an empty HTTP_HOST; under
// HTTP/1.0 net/http leaves an empty header and none alike.
func (rv *requestView) header(name string) (string, bool) {
	switch asciiLower(name) {
	case "host":
		return rv.req.Host, rv.req.Host != "" || rv.req.ProtoAtLeast(1, 1)
	case "transfer-encoding":
		if len(rv.req.TransferEncoding) > 0 {
			return strings.Join(rv.req.TransferEncoding, ","), true
		}
	}
	vs := rv.req.Header.Values(name)
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

// hasHeader is RGWEnv::exists for a signed-header token.
func (rv *requestView) hasHeader(name string) bool {
	_, ok := rv.header(name)
	return ok
}

// contentLength is the request's Content-Length, -1 for a chunked transfer
// encoding.
func (rv *requestView) contentLength() int64 { return rv.req.ContentLength }

// chunkedTE reports a chunked Transfer-Encoding, the only one net/http
// accepts.
func (rv *requestView) chunkedTE() bool { return len(rv.req.TransferEncoding) > 0 }

// localPort is SERVER_PORT, the listener's port, and whether beast would
// also set SERVER_PORT_SECURE, which it does on a TLS listener
// (rgw_asio_client.cc:86-91).
func (rv *requestView) localPort() (port string, secure bool) {
	secure = rv.req.TLS != nil
	addr, ok := rv.req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return "", secure
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", secure
	}
	return port, secure
}

// hostNoPort is req_info.host after RGWREST::preprocess. The req_info
// constructor drops a trailing ':' and the digits after it
// (rgw_common.cc:246-261 at v19.2.6); preprocess then keeps what a leading
// '[' and the first ']' enclose, or cuts at the first ':'
// (rgw_rest.cc:2048-2060 at v19.2.6, :2065-2077 at v20.2.4).
func (rv *requestView) hostNoPort() string {
	h := rv.req.Host
	if i := strings.LastIndexByte(h, ':'); i >= 0 && strings.TrimLeft(h[i+1:], "0123456789") == "" {
		h = h[:i]
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i >= 0 {
			return h[1:i]
		}
		return h
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

// version and route are radosgw's AwsVersion and AwsRoute.
type version uint8

const (
	versionUnknown version = iota
	versionV2
	versionV4
)

type route uint8

const (
	routeQuery route = iota
	routeHeaders
)

// discoverFlavour is radosgw's choice of signature version and route
// (rgw_rest_s3.cc:5071-5105 at v19.2.6, :5631-5665 at v20.2.4): a non-empty
// Authorization header is the header route, v4 or v2 by its prefix;
// otherwise the query decides, by an exact x-amz-algorithm or a non-empty
// AWSAccessKeyId.
func discoverFlavour(rv *requestView) (version, route) {
	if a, _ := rv.header("authorization"); a != "" {
		switch {
		case strings.HasPrefix(a, aws4Algorithm):
			return versionV4, routeHeaders
		case strings.HasPrefix(a, "AWS "):
			return versionV2, routeHeaders
		}
		return versionUnknown, routeHeaders
	}
	if alg, _ := rv.param("x-amz-algorithm"); alg == aws4Algorithm {
		return versionV4, routeQuery
	}
	if id, _ := rv.param("AWSAccessKeyId"); id != "" {
		return versionV2, routeQuery
	}
	return versionUnknown, routeQuery
}
