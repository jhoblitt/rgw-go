package s3

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// deleteObject is delete_obj (RGWDeleteObj_ObjStore_S3, get_params
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3435-L3483
// and send_response
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3485-L3500 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3733
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3735-L3750).
// Each condition is its header's last value, nil when the header is
// absent: x-amz-delete-if-unmodified-since and
// x-amz-if-match-last-modified-time url-decoded, If-Match and
// x-amz-if-match-size as sent, all four read by both releases alike. The
// success, a missing key's included, is 204 with x-amz-version-id when the
// delete names one and x-amz-delete-marker when it made one, and no body.
func deleteObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.DeleteObject{
		UnmodifiedSince:     decodedHeader(r, "X-Amz-Delete-If-Unmodified-Since"),
		IfMatch:             headerPtr(r, "If-Match"),
		IfMatchSize:         headerPtr(r, "X-Amz-If-Match-Size"),
		IfMatchLastModified: decodedHeader(r, "X-Amz-If-Match-Last-Modified-Time"),
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	h := w.Header()
	if o.VersionID != "" {
		SetHeader(h, "x-amz-version-id", o.VersionID)
	}
	if o.DeleteMarker {
		SetHeader(h, "x-amz-delete-marker", "true")
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}

// decodedHeader is headerPtr through url_decode, as get_params decodes the
// date conditions.
func decodedHeader(r *op.Request, name string) *string {
	v := headerPtr(r, name)
	if v != nil {
		*v = rgwtext.URLDecode(*v, false)
	}
	return v
}
