package s3

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// errBadDeleteObject is an <Object> RGWMultiDelObject::xml_end refuses,
// which fails the whole parse.
var errBadDeleteObject = errors.New("s3: an Object element radosgw refuses")

// deleteObjects is multi_object_delete (RGWDeleteMultiObj_ObjStore_S3,
// :4231-4336, [T] :4779-4877), streamed as radosgw streams it:
//   - a failure the op meets before its first key is send_status alone
//     (:4247-4255): the status line, none of end_header's headers and no
//     document, which net/http completes with Content-Length: 0 and Date, as
//     radosgw's frontend completes such a response
//     (rgw_client_io_filters.h:221-253 at v19.2.6 and v20.2.4);
//   - otherwise begin_response (:4257-4271) sends the common headers,
//     Content-Type: application/xml and 200 without a length, so that
//     net/http frames the body chunked, as end_header(...,
//     CHUNKED_TRANSFER_ENCODING) does, then the XML declaration dump_start
//     put before it, then the DeleteResult open tag, each flushed;
//   - each key's element leaves as the key completes (send_partial_response,
//     :4273-4329), and end_response closes the document (:4331-4336).
//
// An error before either, the bucket's or the body's, is an ordinary error
// response; after begin_response only the request's end can stop the op,
// which is logged, and the document ends short.
func deleteObjects(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	rel := r.Env.Zone.Release()
	var (
		quiet bool
		sent  bool
		cx    *chunkedXML
	)
	deliver := func() {
		if err := cx.flush(); err != nil {
			slog.DebugContext(ctx, "delete result not delivered", slog.Any("error", err))
		}
	}
	o := &op.DeleteObjects{
		Parse: func(body []byte) ([]op.DeleteObjectsEntry, error) {
			entries, q, err := parseDelete(body, rel)
			quiet = q
			return entries, err
		},
		Status: func(err error) {
			sent = true
			h := w.Header()
			h.Del("x-amz-request-id")
			h.Del("Server")
			w.WriteHeader(op.AsError(err).Status)
		},
		Begin: func() error {
			var err error
			if cx, err = startChunkedXML(w, r); err != nil {
				return err
			}
			cx.add(xmlHeader)
			if err := cx.flush(); err != nil {
				return err
			}
			cx.add(`<DeleteResult xmlns="` + xmlnsS3 + `">`)
			return cx.flush()
		},
		Result: func(res op.DeleteResult) {
			cx.add(deleteResultElement(res, quiet))
			deliver()
		},
	}
	err := op.Run(ctx, o, r)
	switch {
	case sent:
		return nil
	case cx == nil:
		return err
	case err != nil:
		slog.WarnContext(ctx, "multi-object delete ended after its response started", slog.Any("error", err))
		return nil
	}
	cx.add("</DeleteResult>")
	deliver()
	return nil
}

// deleteResultElement renders one key's outcome as send_partial_response
// does (:4273-4329, [T] :4821-4871): a success, unless quiet, as Deleted with
// the Key, the VersionId when the key names one, and DeleteMarker, which
// dump_bool writes as true, and DeleteMarkerVersionId when the delete made a
// marker; a failure, quiet or not, as Error with the Key, the VersionId even
// when empty, and the S3 code as both Code and Message. Each text goes
// through xmltext.Escape, as XMLFormatter::dump_string escapes it.
func deleteResultElement(res op.DeleteResult, quiet bool) string {
	var b strings.Builder
	el := func(name, text string) { b.WriteString("<" + name + ">" + xmltext.Escape(text) + "</" + name + ">") }
	if res.Err != nil {
		code := op.AsError(res.Err).Code
		b.WriteString("<Error>")
		el("Key", res.Key.Name)
		el("VersionId", res.Key.Instance)
		el("Code", code)
		el("Message", code)
		b.WriteString("</Error>")
		return b.String()
	}
	if quiet {
		return ""
	}
	b.WriteString("<Deleted>")
	el("Key", res.Key.Name)
	if res.Key.Instance != "" {
		el("VersionId", res.Key.Instance)
	}
	if res.DeleteMarker {
		el("DeleteMarker", "true")
		el("DeleteMarkerVersionId", res.MarkerVersionID)
	}
	b.WriteString("</Deleted>")
	return b.String()
}

// parseDelete is RGWMultiDelXMLParser (rgw_multi_del.cc) as
// RGWDeleteMultiObj::execute reads its result (rgw_op.cc:7025-7039 at
// v19.2.6, v20.2.4 :7929-7946): the entries of the root Delete's Object
// children and its Quiet, true when its text is "true" in any case. Every
// Object element in the document, wherever it sits, must hold a non-empty
// Key, and its ETag, LastModifiedTime and Size must read as v20.2.4 reads
// them (deleteEntry); rgw-go reads them on both releases, as it checks them
// on both (docs/exclusions.md). A document that fails is InvalidArgument on
// Squid, -EINVAL, and MalformedXML with radosgw's message on Tentacle; so is
// a root other than Delete.
func parseDelete(body []byte, rel denc.Release) (entries []op.DeleteObjectsEntry, quiet bool, err error) {
	refused := func(msg string) error {
		if rel >= denc.Tentacle {
			return op.ErrMalformedXML.WithMessage(msg)
		}
		return op.ErrInvalidArgument
	}
	root, perr := xmltext.ParseFunc(body, func(e *xmltext.Element) error {
		if e.Name != "Object" {
			return nil
		}
		_, bad := deleteEntry(e)
		return bad
	})
	if perr != nil {
		return nil, false, refused("Failed to parse xml input")
	}
	if root.Name != "Delete" {
		return nil, false, refused("Missing require element Delete")
	}
	if q := root.FindFirst("Quiet"); q != nil {
		quiet = strings.EqualFold(rgwtext.CString(q.Text), "true")
	}
	for e := range root.Find("Object") {
		// The parse has checked every Object.
		entry, _ := deleteEntry(e) //nolint:errcheck // see above
		entries = append(entries, entry)
	}
	return entries, quiet, nil
}

// deleteEntry is RGWMultiDelObject::xml_end at v20.2.4 (rgw_multi_del.cc
// :17-58): the Key, which must be present and not empty; the VersionId as
// the instance; the ETag, read as a C string, as an If-Match; the
// LastModifiedTime, which must not be empty and must parse after url_decode
// as parse_time takes it; and the Size, which strict_strtoll must read whole.
// v19.2.6 reads the first two alone (:17-36).
func deleteEntry(e *xmltext.Element) (op.DeleteObjectsEntry, error) {
	var entry op.DeleteObjectsEntry
	key := e.FindFirst("Key")
	if key == nil || key.Text == "" {
		return entry, errBadDeleteObject
	}
	entry.Key = meta.ObjKey{Name: key.Text}
	if v := e.FindFirst("VersionId"); v != nil {
		entry.Key.Instance = v.Text
	}
	if etag := e.FindFirst("ETag"); etag != nil {
		entry.IfMatch = new(rgwtext.CString(etag.Text))
	}
	if lm := e.FindFirst("LastModifiedTime"); lm != nil {
		if lm.Text == "" {
			return entry, errBadDeleteObject
		}
		t, err := op.ParseHTTPTime(rgwtext.CString(rgwtext.URLDecode(lm.Text, false)))
		if err != nil {
			return entry, errBadDeleteObject
		}
		entry.IfMatchLastModified = t
	}
	if size := e.FindFirst("Size"); size != nil {
		v, ok := rgwtext.StrictStrtoll(size.Text)
		if !ok {
			return entry, errBadDeleteObject
		}
		entry.IfMatchSize = new(uint64(v)) //nolint:gosec // size_match = uint64_t(size_tmp)
	}
	return entry, nil
}
