package s3

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// getObjectTags serves get_obj_tags (RGWGetObjTags_ObjStore_S3::send_response_data,
// rgw_rest_s3.cc:746-773 at v19.2.6, :829-856 at v20.2.4): the tag set, an
// empty TagSet for none.
func getObjectTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetObjectTagging{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, o.Tags.MarshalS3XML())
	return nil
}

// getObjectACL is the object half of get_acls (RGWGetACLs_ObjStore_S3::send_response,
// rgw_rest_s3.cc:3605-3614 at v19.2.6, :3888-3897 at v20.2.4): the object's
// ACL.
func getObjectACL(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetObjectACL{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, o.Policy.MarshalS3XML())
	return nil
}

// writeDocument answers 200 with the XML declaration and doc, already
// escaped, as application/xml. Its length is the one radosgw's frontend adds
// to a response whose end_header named none, without Accept-Ranges
// (rgw_client_io_filters.h:221-253 at v19.2.6 and v20.2.4). A HEAD gets the
// headers alone (docs/exclusions.md).
func writeDocument(w http.ResponseWriter, r *op.Request, doc []byte) {
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/xml")
	h.Set("Content-Length", strconv.Itoa(len(xmlHeader)+len(doc)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// A failed write means the client is gone, with no one left to tell.
	_, _ = w.Write(append([]byte(xmlHeader), doc...)) //nolint:errcheck // see above
}
