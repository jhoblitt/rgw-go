package s3

import (
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// SetCommonHeaders writes end_header's headers on w
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L589-L655 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L594-L660 at v20.2.4).
// Every S3 response is written through it, error documents included:
//   - x-amz-request-id;
//   - x-amz-request-charged: requester when r.BucketRec is requester-pays and
//     the identity does not own it
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L597-L601 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L602-L606 at v20.2.4;
//     WriteError never adds it, end_header's !is_err());
//   - once r.BucketRec is loaded, the CORS headers corsHeaders derives,
//     which radosgw's end_header adds only when it is given the op
//     (docs/exclusions.md, "Every S3 response carries CORS headers once its
//     bucket is loaded").
//
// An OPTIONS request gets x-amz-request-id alone, on success and on every
// error status alike. Dispatch sends one to the CORS preflight and nothing
// else, whose Access-Control headers are SetPreflightHeaders' alone, and a
// preflight granted on its key's id alone must answer as an anonymous one
// does (docs/exclusions.md, "A preflight's answer says nothing of its key's
// user"): x-amz-request-charged, which radosgw sends to every requester that
// does not own the bucket, would say whether the key's user owns it.
// radosgw's abort_early also sends the CORS headers end_header derives for
// any op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L746 at v19.2.7 and v20.2.4)
// to a preflight refused once its bucket has loaded: by the bucket load
// itself, a 301 among them, by init_processing for a quota owner that does
// not load, and by the op mask and OPA
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L201-L219 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L201-L219 at v20.2.4)
// and the rate limiter
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L247-L250 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L250-L253 at v20.2.4).
// rgw-go's preflight makes the bucket load's refusals alone, with no CORS
// headers, and answers from the configuration a preflight the others would
// refuse; a browser fails a preflight answered outside 200-299 whatever its
// headers
// (https://fetch.spec.whatwg.org/#cors-preflight-fetch). The Server header
// is not r's to know: the Handler sends it on every response.
func SetCommonHeaders(w http.ResponseWriter, r *op.Request) {
	h := w.Header()
	// dump_trans_id skips an empty id
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L579-L587 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L584-L592 at v20.2.4).
	if r.ID != "" {
		SetHeader(h, "x-amz-request-id", r.ID)
	}
	if b := r.BucketRec; b != nil && r.Method != http.MethodOptions && b.Info.RequesterPays && !isOwnerOf(r.Identity, b.Info.Owner) {
		SetHeader(h, "x-amz-request-charged", "requester")
	}
	setCORSHeaders(h, corsHeaders(r))
}

// corsHeaders are the CORS headers for r as generate_cors_headers derives
// them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1541-L1600 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1778-L1837 at v20.2.4),
// none unless r is not an OPTIONS request, carries an Origin, and has a
// bucket record loaded whose configuration decodes:
//   - the rule is the one cors.Configuration.Match finds for the Origin and
//     the method, Access-Control-Request-Method when r sends one and else
//     r's own. Access-Control-Request-Headers chooses no rule, as Amazon S3
//     matches it for a preflight alone (docs/exclusions.md, "CORS rules are
//     matched as Amazon S3 matches them"); the rule then allows those of its
//     names it covers;
//   - Access-Control-Allow-Origin is * for a rule that allows every origin
//     when r carries no Authorization header, a presigned request among
//     them, and the Origin as sent otherwise.
//
// The configuration is the one r.BucketRec holds as the response is
// written, so a PutBucketCors is answered by what it stored and a
// DeleteBucketCors by none. One that does not decode adds nothing, and is
// left to the op to answer, as radosgw's read error is.
func corsHeaders(r *op.Request) cors.Headers {
	if r.BucketRec == nil || r.Method == http.MethodOptions {
		return cors.Headers{}
	}
	origin, ok := op.HeaderValue(r.Header, "Origin")
	if !ok {
		return cors.Headers{}
	}
	stored, ok := r.BucketRec.Attrs[op.AttrCORS]
	if !ok {
		return cors.Headers{}
	}
	c, err := cors.Decode(stored)
	if err != nil {
		return cors.Headers{}
	}
	method, ok := op.HeaderValue(r.Header, "Access-Control-Request-Method")
	if !ok {
		method = r.Method
	}
	rule := c.Match(origin, method, nil)
	if rule == nil {
		return cors.Headers{}
	}
	var asked []string
	if v, ok := op.HeaderValue(r.Header, "Access-Control-Request-Headers"); ok {
		asked = cors.ParseRequestHeaders(v)
	}
	_, authorized := op.HeaderValue(r.Header, "Authorization")
	return rule.ResponseHeaders(origin, method, asked, authorized)
}

// SetPreflightHeaders writes on w the Access-Control headers of the CORS
// preflight o answered, as RGWOptionsCORS_ObjStore_S3::send_response writes
// them on its 200
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326 at v20.2.4):
// o's rule's, for its Origin, method and requested headers, with
// Access-Control-Allow-Origin the Origin as sent whatever the rule allows,
// so cors.Rule.ResponseHeaders is told the request is authorized whatever
// it carries. It reads neither the identity nor the Authorization header,
// so a preflight granted on its key's id alone answers as an anonymous one
// does. An op that found no rule gets none, as send_response's errors carry
// none.
func SetPreflightHeaders(w http.ResponseWriter, o *op.OptionsCORS) {
	if o.Rule == nil {
		return
	}
	setCORSHeaders(w.Header(), o.Rule.ResponseHeaders(o.Origin, o.Method, o.Headers, true))
}

// setCORSHeaders sets each header c yields, under its own spelling.
func setCORSHeaders(h http.Header, c cors.Headers) {
	for name, v := range c.All() {
		SetHeader(h, name, v)
	}
}

// SetHeader sets name to v under name's own spelling. radosgw's dump_header
// hands the name to the frontend, which writes its bytes as they are
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L347-L357,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L167-L178 at v19.2.6
// and v20.2.4), and an SDK reads a user metadata key back case and all.
// http.Header.Set would send net/http's canonical form instead; net/http
// writes a key set directly as it stands.
func SetHeader(h http.Header, name, v string) { h[name] = []string{v} }

// addHeader is SetHeader that keeps the values already under name.
func addHeader(h http.Header, name, v string) { h[name] = append(h[name], v) }

// isOwnerOf is LocalApplier::is_owner_of's match_owner
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L67-L75 at v19.2.6
// and v20.2.4): a user owner is the identity's user, an account owner the
// account the user belongs to.
func isOwnerOf(id op.Identity, owner meta.Owner) bool {
	if owner.User != nil {
		return id.User != nil && owner.User.String() == id.User.UserID.String()
	}
	return id.Account != nil && owner.Account == id.Account.ID
}

// SetContentLength is dump_content_length
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L388-L397 at v19.2.6
// and v20.2.4): the length and Accept-Ranges: bytes. radosgw calls it for
// every error document and for a success whose op names its length: the GET
// and HEAD of an object
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L459 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L495 at v20.2.4)
// and the PUT of an object or part without a copy source
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2747,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2911).
// Every other Content-Length radosgw sends is the one its frontend adds when
// the response completes, which carries no Accept-Ranges.
func SetContentLength(h http.Header, n uint64) {
	h.Set("Content-Length", strconv.FormatUint(n, 10))
	h.Set("Accept-Ranges", "bytes")
}
