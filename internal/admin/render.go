package admin

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// mimeType is to_mime_type
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L189-L207 at v19.2.6).
func (f Format) mimeType() string {
	switch f {
	case FormatXML:
		return "application/xml"
	case FormatHTML:
		return "text/html"
	default:
		return "application/json"
	}
}

// newFormatter is reallocate_formatter for the admin handlers
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1766-L1804 at v19.2.6):
// XML lowercases names for a bulk-delete or multipart-manifest=delete query;
// HTML is pretty only for the website endpoint, which an admin request never
// is.
func (q Request) newFormatter() formatter.Formatter {
	switch q.Format {
	case FormatXML:
		mm, _ := q.Args.Get("multipart-manifest")
		return formatter.NewXML(false, q.Args.Has("bulk-delete") || mm == "delete")
	case FormatHTML:
		return formatter.NewHTML(false)
	default:
		return formatter.NewJSON(false)
	}
}

// WriteBody writes a body an op's flusher started: RGWRESTFlusher::do_start's
// dump_errno and dump_start, flushed with the headers by end_header, whose
// output_footer closes HTML's status page, then the op's dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1035-L1042,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L589-L655 and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L303-L314 at v19.2.6).
// A dump that calls Fail writes nothing, and WriteBody returns the failure.
func WriteBody(w http.ResponseWriter, r *op.Request, q Request, dump func(formatter.Formatter)) error {
	f := q.newFormatter()
	f.SetStatus(http.StatusOK, formatter.StatusName(http.StatusOK))
	f.OutputHeader()
	f.OutputFooter()
	head := bytes.Clone(f.Bytes())
	f.Reset()
	dump(f)
	if err := f.Err(); err != nil {
		return err
	}
	write(w, r, http.StatusOK, q.Format.mimeType(), append(head, f.Bytes()...), false)
	return nil
}

// WriteDumped writes a body the op dumped without dump_start, as the zone
// config, metadata get and list do
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_config.cc#L35-L47 at v19.2.6):
// no declaration and no status page. A dump that calls Fail writes
// nothing, and WriteDumped returns the failure.
func WriteDumped(w http.ResponseWriter, r *op.Request, q Request, dump func(formatter.Formatter)) error {
	return writeDump(w, r, q, q.Format.mimeType(), false, dump)
}

// WriteTyped is WriteDumped under a content type the op names itself, as
// the realm and period getters name application/json whatever the format.
// Those getters name the length too, end_header(s, NULL, "application/json",
// s->formatter->get_len()), so it goes out through dump_content_length,
// with Accept-Ranges.
func WriteTyped(w http.ResponseWriter, r *op.Request, q Request, contentType string, dump func(formatter.Formatter)) error {
	return writeDump(w, r, q, contentType, true, dump)
}

func writeDump(w http.ResponseWriter, r *op.Request, q Request, contentType string, named bool, dump func(formatter.Formatter)) error {
	f := q.newFormatter()
	dump(f)
	if err := f.Err(); err != nil {
		return err
	}
	write(w, r, http.StatusOK, contentType, f.Bytes(), named)
	return nil
}

// WriteEmpty writes status with the common headers and no body.
func WriteEmpty(w http.ResponseWriter, r *op.Request, status int) {
	s3.SetCommonHeaders(w, r)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// WriteError is end_header's error branch in q's format
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629 at v20.2.4):
// dump_start, the error document dump(req_state*) writes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L381-L404 at v19.2.6),
// output_footer, and the length through dump_content_length, with
// Accept-Ranges. An admin request names no bucket, so BucketName never
// appears. A failure to render a record rgw-go carries opaque is
// NotImplemented; an InternalError or UnknownError is logged with its code
// and cause, so err must carry no request header value: an authentication
// failure is answered by refuseAuth instead.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request, err error) {
	e := op.AsError(err)
	if errors.Is(err, meta.ErrOpaqueJSON) {
		e = op.ErrNotImplemented
	}
	if errors.Is(e, op.ErrInternalError) || errors.Is(e, op.ErrUnknown) {
		slog.ErrorContext(ctx, "admin request failed", slog.String("code", e.Code), slog.Any("error", err))
	}
	writeErrorDocument(w, r, q, e)
}

// refuseAuth answers a failed authentication with err's S3 error, after
// s3.LogAuthError logs a server error by its code alone.
func refuseAuth(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request, err error) {
	s3.LogAuthError(ctx, err)
	writeErrorDocument(w, r, q, op.AsError(err))
}

// writeErrorDocument renders e as WriteError's document.
func writeErrorDocument(w http.ResponseWriter, r *op.Request, q Request, e *op.Error) {
	f := q.newFormatter()
	f.SetStatus(e.Status, formatter.StatusName(e.Status))
	f.OutputHeader()
	if q.Format != FormatHTML {
		f.OpenObjectSection("Error")
	}
	if e.Code != "" {
		f.DumpString("Code", e.Code)
	}
	f.DumpString("Message", e.Message)
	if r.ID != "" {
		f.DumpString("RequestId", r.ID)
	}
	var hostID string
	if r.Env != nil {
		hostID = r.Env.HostID
	}
	f.DumpString("HostId", hostID)
	if q.Format != FormatHTML {
		f.CloseSection()
	}
	f.OutputFooter()
	write(w, r, e.Status, q.Format.mimeType(), f.Bytes(), true)
}

// write sends body at status. named says radosgw's end_header was handed the
// length, which it sends through dump_content_length with Accept-Ranges:
// every error document and the getters that name it. A success that names
// none gets the length radosgw's frontend adds when the response completes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io_filters.h#L221-L253 at v19.2.6
// and v20.2.4), without Accept-Ranges.
func write(w http.ResponseWriter, r *op.Request, status int, contentType string, body []byte, named bool) {
	s3.SetCommonHeaders(w, r)
	w.Header().Set("Content-Type", contentType)
	if named {
		s3.SetContentLength(w.Header(), uint64(len(body)))
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(status)
	// A failed write means the client is gone, with no one left to tell; the
	// formatters escape every value as radosgw's do.
	_, _ = w.Write(body) //nolint:errcheck,gosec // see above
}
