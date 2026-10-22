package op

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/website"
)

// noIndexSuffix is the message retarget sets when get_effective_key finds no
// index suffix.
const noIndexSuffix = "The IndexDocument Suffix is not configurated or not well formed!"

// noRedirectHost is the message of the refusal of a redirect to the
// request's host for a request that sent none.
const noRedirectHost = "The routing rule names no host and the request sent no Host header."

// WebsiteRetarget is GetObject's hook for the website endpoint's GET and
// HEAD: RGWHandler_REST_S3Website::retarget
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5387-L5448 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5913-L5974 at v20.2.4),
// which radosgw runs after init_permissions and before read_permissions
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L174-L196 at v19.2.7 and v20.2.4).
type WebsiteRetarget struct {
	// OriginalKey is the request's key before retarget replaced it with the
	// effective key: radosgw's original_object_name, which the website error
	// handler matches its routing rules on and redirects
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5372-L5385 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5898-L5911 at v20.2.4).
	// Init sets it.
	OriginalKey string
}

// WebsiteRedirectError is a website request answered with a redirect to
// r.Redirect. It unwraps to ErrWebsiteRedirect, whose 301 is AsError's
// status; Code is the one to send.
type WebsiteRedirectError struct {
	// Code is the redirect's status: a routing rule's code, any that a
	// stored configuration holds (website.Apply), or 301.
	Code int
	// Object is that the object's x-amz-website-redirect-location made the
	// redirect. radosgw answers that one from GetObject's own response,
	// which no error handler sees, where retarget's reaches the website
	// error handler through abort_early.
	Object bool
}

func (e *WebsiteRedirectError) Error() string {
	return fmt.Sprintf("%s: a redirect with status %d", ErrWebsiteRedirect.Code, e.Code)
}

// Unwrap returns ErrWebsiteRedirect.
func (e *WebsiteRedirectError) Unwrap() error { return ErrWebsiteRedirect }

// WebsiteRedirect is the redirect the website endpoint answers r with, as
// retarget and the website error handler each build it: should_redirect on
// matchKey at status, then apply_rule on applyKey with r's protocol and Host
// header. retarget's should_redirect takes the effective key at 0 and its
// apply_rule the request's key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5419-L5429 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5945-L5955 at v20.2.4);
// the error handler's should_redirect and apply_rule both take the request's
// key, at the error's HTTP status
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5545-L5560 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6071-L6086 at v20.2.4).
// secure is rgw_transport_is_secure. A rule that names no host takes the
// Host header as sent, HTTP_HOST; for a request with none, radosgw writes
// a Location of "http:///" and the key, which RFC 9110 section 4.2.1 forbids
// a sender to generate and a WHATWG URL parser reads with the key's first
// segment for its host, so rgw-go refuses it (docs/exclusions.md, "A
// website redirect to the request's host is refused for a request with no
// Host"). The omission is the client's, so the refusal, which matches
// website.ErrUnsafeRedirect, is 400 InvalidRequest to AsError: the row
// radosgw's table gives ERR_INVALID_REQUEST and ERR_ZERO_IN_URL
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L62 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L136 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L63 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L137 at v20.2.4),
// the ERR_ZERO_IN_URL preprocess answers a request path holding a NUL with
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2189-L2190 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2206-L2207 at v20.2.4).
// website.Apply's own refusals, of a stored host or protocol, carry no
// *Error, so AsError answers them 500; they are returned as they are.
func WebsiteRedirect(r *Request, conf meta.WebsiteConf, matchKey, applyKey string, status int, secure bool) (url string, code int, ok bool, err error) {
	rule, ok := website.Match(conf, matchKey, status)
	if !ok {
		return "", 0, false, nil
	}
	var host string
	if r.HTTPHost != nil {
		host = *r.HTTPHost
	}
	if host == "" && rule.RedirectInfo.Redirect.Hostname == "" {
		return "", 0, false, fmt.Errorf("%w: %w", ErrInvalidRequest.WithMessage(noRedirectHost), website.ErrUnsafeRedirect)
	}
	proto := "http"
	if secure {
		proto = "https"
	}
	url, code, err = website.Apply(rule, proto, host, applyKey)
	if err != nil {
		return "", 0, false, err
	}
	return url, code, true, nil
}

// initBucket loads the bucket as init_permissions does, and with Retarget
// set retargets a website request after it.
func (o *GetObject) initBucket(ctx context.Context, r *Request) error {
	if o.Retarget == nil || !r.Website {
		return loadBucket(ctx, r)
	}
	o.Retarget.OriginalKey = r.Object.Name
	return o.retarget(ctx, r)
}

// retarget is RGWHandler_REST_S3Website::retarget over the bucket load. A
// request that names no bucket loads none, as rgw_build_bucket_policies
// skips an empty name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L523 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L553 at v20.2.4),
// and is NoSuchBucket. An entry point read under the empty name is not made:
// the OSD's do_op refuses an empty oid
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L2067-L2071 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L2114-L2118 at v20.2.4),
// which the bucket load would answer as InvalidArgument. A redirect leaves
// r.Object alone, as retarget leaves s->object; otherwise r.Object becomes
// the effective key with no instance, as new_obj carries the name alone, so
// the read is of its current version and is authorized as s3:GetObject.
func (o *GetObject) retarget(ctx context.Context, r *Request) error {
	if r.Bucket == "" {
		return fmt.Errorf("%w: a website request that names no bucket", ErrNoSuchBucket)
	}
	if err := loadBucket(ctx, r); err != nil {
		return err
	}
	conf := r.BucketRec.Info.Website
	if conf == nil {
		return ErrNoSuchWebsiteConfiguration
	}
	key := r.Object.Name
	eff, ok := website.EffectiveKey(*conf, key, o.isFile(ctx, r, *conf, key))
	if !ok {
		return ErrInvalidArgument.WithMessage(noIndexSuffix)
	}
	url, code, ok, err := WebsiteRedirect(r, *conf, eff, key, 0, o.Secure)
	if err != nil {
		return err
	}
	if ok {
		r.Redirect = url
		return &WebsiteRedirectError{Code: code}
	}
	r.Object = meta.ObjKey{Name: eff}
	o.Versioned, o.VersionID = false, ""
	return nil
}

// isFile is web_dir
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5346-L5370 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5876-L5896 at v20.2.4):
// that the key names an object, a stat whose failure is no file. web_dir
// stats the key url_decode makes of the already decoded name; rgw-go stats
// the key itself (docs/exclusions.md, "A website request's file test reads
// its key decoded once"). get_effective_key reads the answer only under an
// index suffix and for a key that is not empty and does not end in "/", so
// the stat, and the trailing "/" web_dir drops, are left out for the rest.
func (o *GetObject) isFile(ctx context.Context, r *Request, conf meta.WebsiteConf, key string) bool {
	if conf.IndexDocSuffix == "" || key == "" || strings.HasSuffix(key, "/") {
		return false
	}
	st, err := r.Env.Objects.StatObject(ctx, r.BucketRec, meta.ObjKey{Name: key})
	if err != nil {
		slog.DebugContext(ctx, "website file test failed, taking the key for no file",
			slog.String("bucket", r.Bucket), slog.String("key", key), slog.Any("error", err))
		return false
	}
	return st != nil && st.Exists
}

// websiteObjectRedirect is RGWGetObj_ObjStore_S3Website::send_response_data
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L274-L297 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L271-L294 at v20.2.4):
// a website request's object, or the part prepare read, that holds
// x-amz-website-redirect-location is answered 301 to it. The override reads
// the attrs execute takes from read_op->prepare
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2259-L2265 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2495-L2501 at v20.2.4),
// which Read::prepare copies only past its part redirect
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7066-L7125 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7728-L7787 at v20.2.4),
// and it answers success and each later failure, prepare's conditionals
// included, as done_err calls send_response_data_error
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2460-L2461 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2692-L2693 at v20.2.4).
// So prepare passes this the conditionals' result err, which it replaces
// whatever it is, and a part refusal before them stands (docs/exclusions.md,
// "A website GET of a part the object has not is InvalidPart under any
// policy"). radosgw sends a GET with data to read the redirect twice;
// rgw-go answers once, before anything is sent (docs/exclusions.md, "An
// object's website redirect is answered once"). The Location is the attr up
// to its first NUL, as radosgw's c_str() copy reads it.
func (o *GetObject) websiteObjectRedirect(r *Request, err error) error {
	if !r.Website || o.State == nil {
		return err
	}
	v, ok := o.State.Attrs[meta.AttrWebsiteRedirect]
	if !ok {
		return err
	}
	r.Redirect, _, _ = strings.Cut(string(v), "\x00")
	return &WebsiteRedirectError{Code: http.StatusMovedPermanently, Object: true}
}
