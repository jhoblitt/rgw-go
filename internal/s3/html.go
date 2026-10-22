package s3

import (
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// htmlErrorPage is the error page the website endpoint's end_header writes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L620-L624 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629 at v20.2.4)
// through the pretty HTMLFormatter allocate_formatter gives a website request
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1807-L1808 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1806-L1807 at v20.2.4):
// output_header's status page, then dump's fields as list items, which for
// HTML opens no Error section and writes Code only when there is one,
// BucketName only once postauth_init has named the bucket and RequestId only
// with a transaction id
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L381-L404 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L394-L417 at v20.2.4).
// radosgw sends one more line break after the page, past its Content-Length;
// the page ends where the length does (docs/exclusions.md, "A website error
// page ends at its Content-Length").
func htmlErrorPage(r *op.Request, status int, code, message string) []byte {
	f := formatter.NewHTML(true)
	f.SetStatus(status, formatter.StatusName(status))
	f.OutputHeader()
	if code != "" {
		f.DumpString("Code", code)
	}
	f.DumpString("Message", message)
	if r.Bucket != "" {
		f.DumpString("BucketName", r.Bucket)
	}
	if r.ID != "" {
		f.DumpString("RequestId", r.ID)
	}
	var hostID string
	if r.Env != nil {
		hostID = r.Env.HostID
	}
	f.DumpString("HostId", hostID)
	f.OutputFooter()
	return f.Bytes()
}

// writeHTMLError sends htmlErrorPage at status as end_header's error branch
// does: Content-Type text/html, to_mime_type's for HTML
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L198-L199 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L220-L221 at v20.2.4),
// the length through dump_content_length, and no x-amz-request-charged,
// which end_header adds only to a success.
func writeHTMLError(w http.ResponseWriter, r *op.Request, status int, code, message string) {
	body := htmlErrorPage(r, status, code, message)
	SetCommonHeaders(w, r)
	h := w.Header()
	delete(h, "x-amz-request-charged")
	h.Set("Content-Type", "text/html")
	SetContentLength(h, uint64(len(body)))
	w.WriteHeader(status)
	// A failed write means the client is gone, with no one left to tell; the
	// formatter escapes every value as radosgw's does.
	_, _ = w.Write(body) //nolint:errcheck,gosec // see above
}
