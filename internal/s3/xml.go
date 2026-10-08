package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// xmlHeader is the declaration XMLFormatter's output_header writes; radosgw
// sends no line break after it.
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>`

// WriteXML renders v with the XML header radosgw's formatter emits and the
// common headers, at status. Every string v carries is an xmltext.Text, so
// its text is escaped as radosgw's formatter escapes it. Its Content-Length
// is the one radosgw's frontend adds when a response whose end_header named
// no length completes (rgw_client_io_filters.h:221-253 at v19.2.6 and
// v20.2.4), so it carries no Accept-Ranges.
func WriteXML(w http.ResponseWriter, r *op.Request, status int, v any) {
	writeXML(w, r, status, v, false)
}

// writeXML sends xmlHeader and v at status. errDoc is end_header's error
// branch (rgw_rest.cc:620-624 at v19.2.6, :625-629 at v20.2.4): the length
// goes out through dump_content_length, and x-amz-request-charged, which
// end_header adds only when !is_err() (:597-601, :602-606), is withheld.
func writeXML(w http.ResponseWriter, r *op.Request, status int, v any, errDoc bool) {
	body, err := xml.Marshal(v)
	if err != nil {
		// Only a value encoding/xml cannot encode fails, which no document
		// type holds; an errorDocument holds only Text and always marshals.
		slog.Error("response document does not marshal", slog.String("request_id", r.ID), slog.Any("error", err))
		status, errDoc = op.ErrInternalError.Status, true
		body, _ = xml.Marshal(errorDocumentFor(r, op.ErrInternalError)) //nolint:errcheck // an errorDocument always marshals
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/xml")
	n := uint64(len(xmlHeader) + len(body))
	if errDoc {
		delete(h, "x-amz-request-charged")
		SetContentLength(h, n)
	} else {
		h.Set("Content-Length", strconv.FormatUint(n, 10))
	}
	w.WriteHeader(status)
	// A failed write means the client is gone, with no one left to tell; the
	// document's text is escaped as XMLFormatter escapes it.
	_, _ = w.Write(append([]byte(xmlHeader), body...)) //nolint:errcheck,gosec // see above
}

// ISO8601 formats t as radosgw's rgw_to_iso8601: "2006-01-02T15:04:05.000Z".
func ISO8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// chunkedXML is the body of a response radosgw sends with end_header(...,
// CHUNKED_TRANSFER_ENCODING) (rgw_rest.cc:626-627 at v19.2.6, :631-632 at
// v20.2.4): add and encode collect the next chunk and flush sends it, where
// rgw_flush_formatter hands the formatter's output to the chunking filter.
// add writes its argument as is, so it takes markup and text already escaped
// with xmltext.Escape; encode takes a value whose strings are xmltext.Text.
// On a HEAD all three do nothing.
type chunkedXML struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	head bool
	buf  bytes.Buffer
}

// startChunkedXML sends the common headers, Content-Type: application/xml
// and 200 without a Content-Length or Accept-Ranges, and flushes them;
// net/http then frames the body with Transfer-Encoding: chunked. A route
// calls it where radosgw's end_header runs, once the op has succeeded; from
// then on a failure can only end the response short, and the returned writer
// is never nil. A HEAD gets no Transfer-Encoding (dump_chunked_encoding skips
// it, rgw_rest.cc:399-411) and the Content-Length: 0 that radosgw's
// BufferingFilter completes it with, which carries no Accept-Ranges
// (rgw_client_io_filters.h:207-253 at v19.2.6 and v20.2.4).
func startChunkedXML(w http.ResponseWriter, r *op.Request) (*chunkedXML, error) {
	c := &chunkedXML{w: w, rc: http.NewResponseController(w), head: r.Method == http.MethodHead}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/xml")
	if c.head {
		// net/http adds no length to a HEAD response it wrote no body for.
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return c, nil
	}
	w.WriteHeader(http.StatusOK)
	return c, c.rc.Flush()
}

func (c *chunkedXML) add(s string) {
	if !c.head {
		c.buf.WriteString(s)
	}
}

func (c *chunkedXML) encode(v any) error {
	if c.head {
		return nil
	}
	return xml.NewEncoder(&c.buf).Encode(v)
}

// flush sends what was collected as one chunk, and nothing when nothing was:
// rgw_flush_formatter skips empty output and every HEAD (rgw_rest.cc:316-324
// at v19.2.6 and v20.2.4). One write per flush keeps a flush one chunk:
// net/http's response buffer, empty after every flush, hands a write larger
// than itself straight to the chunk writer, and Flush sends a smaller one
// whole.
func (c *chunkedXML) flush() error {
	if c.head || c.buf.Len() == 0 {
		return nil
	}
	_, err := c.w.Write(c.buf.Bytes())
	c.buf.Reset()
	if err != nil {
		return err
	}
	return c.rc.Flush()
}

// writeChunkedXML is a whole document in the one flush that the listings
// rendered after execute end with (rgw_flush_formatter_and_reset after
// end_header): the headers, then the XML declaration and v. A failure after
// the headers is logged under ctx and the response ends short.
func writeChunkedXML(ctx context.Context, w http.ResponseWriter, r *op.Request, v any) {
	c, err := startChunkedXML(w, r)
	if err == nil {
		c.add(xmlHeader)
		err = c.encode(v)
	}
	if err == nil {
		err = c.flush()
	}
	if err != nil {
		slog.DebugContext(ctx, "chunked response not delivered", slog.Any("error", err))
	}
}
