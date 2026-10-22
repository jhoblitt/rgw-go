package s3

import (
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
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
