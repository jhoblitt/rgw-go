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

// errorDocument is radosgw's rgw_err dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L381-L404 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L394-L417 at v20.2.4);
// every field is dump_string text.
type errorDocument struct {
	XMLName    xml.Name     `xml:"Error"`
	Code       xmltext.Text `xml:"Code,omitempty"`
	Message    xmltext.Text `xml:"Message"`
	BucketName xmltext.Text `xml:"BucketName,omitempty"`
	RequestID  xmltext.Text `xml:"RequestId,omitempty"`
	HostID     xmltext.Text `xml:"HostId"`
}

// WriteError renders err as radosgw's S3 error document, or for a website
// request its HTML error page, with the common headers; it is what every
// HandlerFunc calls on failure and what other protocol layers (admin) reuse.
// It is end_header's error branch
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629 at v20.2.4),
// so the length goes out through SetContentLength, with Accept-Ranges. The
// document names r.Bucket, which radosgw knows only once postauth_init has
// run. A status rgw_err::is_err does not count as an error, 200-399
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L203-L207 at v19.2.6
// and v20.2.4), gets no document, as end_header then dumps none. An
// InternalError or UnknownError is logged under ctx with its code and its
// cause, so err must carry no request header value: it is an error the
// handler or its ops built, never an authenticator's, which refuseAuth
// answers instead.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, err error) {
	e := op.AsError(err)
	if errors.Is(e, op.ErrInternalError) || errors.Is(e, op.ErrUnknown) {
		slog.ErrorContext(ctx, "request failed", slog.String("code", e.Code), slog.Any("error", err))
	}
	writeErrorDocument(w, r, e)
}

// writeErrorDocument renders e as WriteError's response: for a website
// request the HTML page of the HTMLFormatter radosgw's S3 handler allocates
// for one, as its default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5290-L5294 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5820-L5824 at v20.2.4),
// whatever the request's format parameter and Accept header ask for
// (docs/exclusions.md, "Every document is XML, and every website error page
// HTML").
func writeErrorDocument(w http.ResponseWriter, r *op.Request, e *op.Error) {
	switch {
	case 200 <= e.Status && e.Status <= 399:
		SetCommonHeaders(w, r)
		w.WriteHeader(e.Status)
	case r.Website:
		writeHTMLError(w, r, e.Status, e.Code, e.Message)
	default:
		writeXML(w, r, e.Status, errorDocumentFor(r, e), true)
	}
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
