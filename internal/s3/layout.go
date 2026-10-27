package s3

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// layoutRoutes is the object ?layout route.
func layoutRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeObject, handlers: map[string]HandlerFunc{
		"get_obj_layout": getObjectLayout,
	}}}
}

// getObjectLayout is get_obj_layout (RGWGetObjLayout_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4368-L4385 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4879-L4896 at v20.2.4):
// 200 with the op's document as application/json. send_response names no
// length, so the frontend's buffering filter sends the body's, and no
// Accept-Ranges
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_client_io_filters.h#L207-L253 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_client_io_filters.h#L207-L253 at v20.2.4).
// A failure once the header is out, the client gone or the request's
// context ended among them, is logged and ends the connection, so the short
// body never passes for a whole one.
func getObjectLayout(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetObjectLayout{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.FormatInt(o.Document.Len(), 10))
	w.WriteHeader(http.StatusOK)
	if err := writeLayout(w, o.Document); err != nil {
		if op.EndForGoneClient(ctx, r, true, o.Name(), err) {
			panic(http.ErrAbortHandler)
		}
		abortAfterStart(ctx, w, r, err)
	}
	return nil
}

// writeLayout writes doc to w through a buffer: the document writes each
// stripe's entry and the comma before it apart.
func writeLayout(w io.Writer, doc op.LayoutDocument) error {
	bw := bufio.NewWriter(w)
	if _, err := doc.WriteTo(bw); err != nil {
		return err
	}
	return bw.Flush()
}
