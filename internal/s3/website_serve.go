package s3

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/website"
)

// websiteRoute is the route of a website request's GET and HEAD: the website
// GetObject, an RGWGetObj whose name() is get_obj.
const websiteRoute = "get_obj"

// websiteDispatch is the dispatch of RGWHandler_REST_S3Website, which
// RGWRESTMgr_S3::get_handler picks for every website request ahead of S3's
// handlers, at service, bucket and object scope alike, without the refusal
// of an object's subresource at bucket scope that S3's bucket handler gets
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5285-L5307 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5815-L5837 at v20.2.4).
// Its op_get and op_head give the website GetObject whatever the query
// names, and every other method has no op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3website.h#L20-L72 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3website.h#L20-L72 at v20.2.4),
// which process_request answers 405 at get_op, before it authenticates
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L325-L329 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L331 at v20.2.4).
func websiteDispatch(r *op.Request, rel denc.Release) (Route, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return Route{}, op.ErrMethodNotAllowed
	}
	return Route{Name: websiteRoute, Scope: r.Scope(), Payloads: payloads(websiteRoute, rel)}, nil
}

// websiteGet serves the website GetObject with what radosgw reads once at
// startup, as objectReads serves S3's.
type websiteGet struct {
	reads *objectReads
}

// websiteGetObject is the handler of a website request's GET and HEAD.
func websiteGetObject(env *op.Env) HandlerFunc {
	return (&websiteGet{reads: newObjectReads(env)}).serve
}

// serve is RGWGetObj_ObjStore_S3Website: S3's GetObject with the website
// endpoint's retarget, whose failures radosgw answers in two ways. A refusal
// before execute, retarget's redirect among them, goes through
// abort_early, which hands it to the website error handler
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L686-L698 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L691-L703 at v20.2.4;
// RGWOp::error_handler,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8037-L8039 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8942-L8944 at v20.2.4).
// A failure of execute, its parameters, conditionals, range and decryption
// among them, the op answers itself through send_response_data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2460-L2461 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2692-L2693 at v20.2.4),
// and rgw_process_authenticated returns 0 after it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L251-L262 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L254-L265 at v20.2.4),
// so no routing rule or error document applies to it: a 304 keeps its four
// headers, and every other failure is the HTML page.
func (g *websiteGet) serve(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	originalKey := r.Object.Name
	o, paramErr := getObjectInput(r)
	o.Retarget = &op.WebsiteRetarget{}
	err := g.reads.runGetObject(ctx, w, r, o, executeMarked{deferredParams{Op: o, err: paramErr}})
	if err == nil {
		return nil
	}
	if wr, ok := errors.AsType[*op.WebsiteRedirectError](err); ok && wr.Object {
		writeObjectRedirect(w, r)
		return nil
	}
	if ee, ok := errors.AsType[*executeError](err); ok {
		return getObjectFailure(w, o, ee.err)
	}
	return g.errorHandler(ctx, w, r, originalKey, err)
}

// writeObjectRedirect is the object's x-amz-website-redirect-location
// answered from the op's own response
// (RGWGetObj_ObjStore_S3Website::send_response_data,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L274-L292 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L271-L289 at v20.2.4),
// which no error handler sees: 301, a Content-Length of 0 through
// dump_content_length, with Accept-Ranges, and the Location unless it is
// empty, as dump_redirect skips one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L440-L443 at v19.2.7 and v20.2.4).
// net/http writes a CR or LF the stored Location holds as a space
// (docs/exclusions.md, "A stored header value's CR and LF go out as
// spaces").
func writeObjectRedirect(w http.ResponseWriter, r *op.Request) {
	SetCommonHeaders(w, r)
	h := w.Header()
	SetContentLength(h, 0)
	if r.Redirect != "" {
		SetHeader(h, "Location", r.Redirect)
	}
	w.WriteHeader(http.StatusMovedPermanently)
}

// errorHandler is RGWHandler_REST_S3Website::error_handler
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5533-L5585 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6059-L6111 at v20.2.4)
// for err, a refusal before execute: with the bucket loaded, the first
// routing rule matching the request's original key and err's status
// redirects, built as retarget builds its redirect (op.WebsiteRedirect),
// at the code retarget's redirect set when the rule stores none, as
// apply_rule and error_handler set a code only above 0 and abort_early keeps
// one set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L61-L62 at v19.2.7 and v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5562-L5563 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6088-L6089 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L704-L706 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L709-L711 at v20.2.4);
// else retarget's own redirect stands; else the error document, when the
// bucket names one and err is a 4XX, is served in err's place; else, or
// when that read fails, err is returned for the HTML page. A request
// refused before its bucket loaded meets no rule and no error document.
//
// radosgw serves the error document for every status
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5571-L5579 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6097-L6105 at v20.2.4);
// rgw-go serves it for 400-499 only, as Amazon S3 does
// (docs/exclusions.md, "A website error document is served for a 4XX error
// only").
//
// A redirect rgw-go declines to build, of a stored host or protocol or to
// the Host of a request that sent none (website.ErrUnsafeRedirect), is
// answered with its own status, 500 InternalError or 400 InvalidRequest,
// and meets no rule and no error document, so it fails closed
// (docs/exclusions.md, "A website redirect is refused for a host holding a
// control byte or a protocol other than http or https", and "A website
// redirect to the request's host is refused for a request with no Host").
func (g *websiteGet) errorHandler(ctx context.Context, w http.ResponseWriter, r *op.Request, originalKey string, err error) error {
	if errors.Is(err, website.ErrUnsafeRedirect) {
		return err
	}
	var conf *meta.WebsiteConf
	if r.BucketRec != nil {
		conf = r.BucketRec.Info.Website
	}
	if conf != nil {
		status := errorStatus(err)
		url, code, ok, rerr := op.WebsiteRedirect(r, *conf, originalKey, originalKey, status, secure(r))
		if rerr != nil {
			return rerr
		}
		if ok {
			if wr, retargeted := errors.AsType[*op.WebsiteRedirectError](err); retargeted && !storesCode(*conf, originalKey, status) {
				code = wr.Code
			}
			r.Redirect = url
			setMissingDeleteMarker(ctx, w.Header(), r, err)
			writeWebsiteRedirect(w, r, code, err)
			return nil
		}
	}
	if wr, ok := errors.AsType[*op.WebsiteRedirectError](err); ok {
		writeWebsiteRedirect(w, r, wr.Code, err)
		return nil
	}
	if status := op.AsError(err).Status; conf != nil && conf.ErrorDoc != "" &&
		http.StatusBadRequest <= status && status < http.StatusInternalServerError {
		setMissingDeleteMarker(ctx, w.Header(), r, err)
		if g.serveErrorDoc(ctx, w, r, conf.ErrorDoc, status) {
			return nil
		}
	}
	return err
}

// storesCode reports whether the rule conf matches for key at status stores
// a redirect code of its own.
func storesCode(conf meta.WebsiteConf, key string, status int) bool {
	rule, _ := website.Match(conf, key, status)
	return rule.RedirectInfo.Redirect.HTTPRedirectCode > 0
}

// errorStatus is the status the error handler matches routing rules on: the
// HTTP status of err's row in rgw_http_s3_errors, or -1 for an error the
// table has no row for, which radosgw answers UnknownError
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5536-L5542 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6062-L6068 at v20.2.4).
func errorStatus(err error) int {
	e := op.AsError(err)
	if errors.Is(e, op.ErrUnknown) {
		return -1
	}
	return e.Status
}

// writeWebsiteRedirect answers a redirect of retarget or the error handler
// as abort_early does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L702-L747 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L707-L747 at v20.2.4):
// at the rule's code (redirectStatus), with r.Redirect as the Location
// unless it is empty, and no body for a status below 400. A code from 400 on
// is an error to end_header, which writes the HTML page: with no Code, as
// abort_early sets no error over a code the rule set, and with the message
// of cause, the error the redirect answers.
func writeWebsiteRedirect(w http.ResponseWriter, r *op.Request, code int, cause error) {
	status := redirectStatus(code)
	if r.Redirect != "" {
		SetHeader(w.Header(), "Location", r.Redirect)
	}
	if status >= http.StatusBadRequest {
		writeHTMLError(w, r, status, "", op.AsError(cause).Message)
		return
	}
	SetCommonHeaders(w, r)
	w.WriteHeader(status)
}

// redirectStatus is the status a website redirect with code is answered
// with. abort_early answers a code of 0 or 200 with WebsiteRedirect's 301
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L702-L706 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L707-L711 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L58 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L59 at v20.2.4),
// and writes any other code in its status line
// (ClientIO::send_status,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L109-L118 at v19.2.7 and v20.2.4),
// which only the admin API's JSON can store outside 301-399
// (RGWRedirectInfo::decode_json,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L142-L148 at v19.2.7 and v20.2.4).
// net/http panics at a status outside 100-999, writes 100-199 other than 101
// as an informational response that does not end the response, and 101 as
// a protocol switch (net/http/server.go:1166-1180 and :1223-1227 at Go
// 1.27.1), so rgw-go answers those 301 as well (docs/exclusions.md, "A
// website redirect's code below 200 or above 999 is answered 301").
func redirectStatus(code int) int {
	if code <= http.StatusOK || code > 999 {
		return http.StatusMovedPermanently
	}
	return code
}

// serveErrorDoc is serve_errordoc
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5460-L5531 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5986-L6057 at v20.2.4):
// a GetObject of the error document key, which no retarget precedes, under
// the original error's status. It reports whether it answered the request;
// a failure before the response starts leaves the response unwritten, for
// the HTML page of the original error. A document holding
// x-amz-website-redirect-location is answered with its redirect, as the
// read is the website GetObject's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5450-L5464 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5976-L5990 at v20.2.4),
// once (docs/exclusions.md, "An object's website redirect is answered
// once").
//
// serve_errordoc clears the read's Range and conditionals, but execute's
// get_params reads them back from the request with its partNumber, ?torrent
// and checksum mode, as the website GetObject's errordoc flag, the one
// override that skips them, is never set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3website.h#L79-L99 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3website.h#L79-L99 at v20.2.4).
// rgw-go reads the document whole and unconditionally, the read
// serve_errordoc sets up, with the request's SSE-C headers and response-*
// overrides, which radosgw takes from the request outside get_params; a
// HEAD gets the document's headers and no body (docs/exclusions.md, "A
// website error document is read whole and unconditionally" and "A website
// HEAD's error document has no body").
func (g *websiteGet) serveErrorDoc(ctx context.Context, w http.ResponseWriter, r *op.Request, key string, status int) bool {
	er := *r
	er.Object = meta.ObjKey{Name: key}
	er.ObjState, er.Redirect = nil, ""
	o, _ := getObjectInput(&er) //nolint:errcheck // its one error is a partNumber's, which the read clears
	o.Range, o.IfMatch, o.IfNoneMatch, o.IfModifiedSince, o.IfUnmodifiedSince = "", "", "", "", ""
	o.PartNumber, o.Torrent = nil, false
	err := g.reads.runGetObject(ctx, &statusWriter{ResponseWriter: w, status: status}, &er, o, o)
	if err == nil {
		return true
	}
	if wr, ok := errors.AsType[*op.WebsiteRedirectError](err); ok && wr.Object {
		r.Redirect = er.Redirect
		writeObjectRedirect(w, r)
		return true
	}
	level := slog.LevelDebug
	if e := op.AsError(err); errors.Is(e, op.ErrInternalError) || errors.Is(e, op.ErrUnknown) {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "website error document not served",
		slog.String("bucket", r.Bucket), slog.String("key", key), slog.Any("error", err))
	return false
}

// statusWriter writes status in place of the status its writer is given:
// the error document's GetObject sends its custom_http_ret, the original
// error's status, over the headers of a successful read
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L402-L409 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L405-L412 at v20.2.4).
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(int) { s.ResponseWriter.WriteHeader(s.status) }

// Unwrap lets http.ResponseController reach the connection.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// executeMarked runs an op whose Execute failures are marked executeError,
// the failures RGWGetObj::execute answers itself, which never reach the
// website error handler.
type executeMarked struct{ op.Op }

func (e executeMarked) Execute(ctx context.Context, r *op.Request) error {
	if err := e.Op.Execute(ctx, r); err != nil {
		return &executeError{err: err}
	}
	return nil
}

// executeError is a failure of an op's Execute.
type executeError struct{ err error }

func (e *executeError) Error() string { return e.err.Error() }

func (e *executeError) Unwrap() error { return e.err }
