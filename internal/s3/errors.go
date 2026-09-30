package s3

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// errorDocument is radosgw's rgw_err dump (rgw_common.cc:381-404 at v19.2.6,
// :394-417 at v20.2.4); every field is dump_string text.
type errorDocument struct {
	XMLName    xml.Name     `xml:"Error"`
	Code       xmltext.Text `xml:"Code,omitempty"`
	Message    xmltext.Text `xml:"Message"`
	BucketName xmltext.Text `xml:"BucketName,omitempty"`
	RequestID  xmltext.Text `xml:"RequestId,omitempty"`
	HostID     xmltext.Text `xml:"HostId"`
}

// WriteError renders err as radosgw's S3 error document with the common
// headers; it is what every HandlerFunc calls on failure and what other
// protocol layers (admin) reuse. It is end_header's error branch
// (rgw_rest.cc:620-624 at v19.2.6, :625-629 at v20.2.4), so the length goes
// out through SetContentLength, with Accept-Ranges. The document names
// r.Bucket, which radosgw knows only once postauth_init has run. A status
// rgw_err::is_err does not count as an error, 200-399 (rgw_common.cc:203-207
// at v19.2.6 and v20.2.4), gets no document, as end_header then dumps none.
// An InternalError or UnknownError is logged with its cause under ctx.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, err error) {
	e := op.AsError(err)
	if errors.Is(e, op.ErrInternalError) || errors.Is(e, op.ErrUnknown) {
		slog.ErrorContext(ctx, "request failed", slog.String("request_id", r.ID), slog.Any("error", err))
	}
	if 200 <= e.Status && e.Status <= 399 {
		SetCommonHeaders(w, r)
		w.WriteHeader(e.Status)
		return
	}
	writeXML(w, r, e.Status, errorDocumentFor(r, e), true)
}

func errorDocumentFor(r *op.Request, e *op.Error) errorDocument {
	doc := errorDocument{
		Code:       xmltext.Text(e.Code),
		Message:    xmltext.Text(e.Message),
		BucketName: xmltext.Text(r.Bucket),
		RequestID:  xmltext.Text(r.ID),
	}
	if r.Env != nil {
		doc.HostID = xmltext.Text(r.Env.HostID)
	}
	return doc
}
