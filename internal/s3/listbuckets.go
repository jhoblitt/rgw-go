package s3

import (
	"context"
	"encoding/xml"
	"log/slog"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// serviceHandlers is the service-scope routes.
func serviceHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{"list_buckets": listBuckets}
}

// listOwner is dump_owner's section; DisplayName is written only when set
// (rgw_rest.cc:506-517 at v19.2.6, :511-522 at v20.2.4).
type listOwner struct {
	XMLName     xml.Name     `xml:"Owner"`
	ID          xmltext.Text `xml:"ID"`
	DisplayName xmltext.Text `xml:"DisplayName,omitempty"`
}

// listBucket is dump_bucket's section (rgw_rest_s3.cc:91-97 at v19.2.6,
// :96-102 at v20.2.4).
type listBucket struct {
	XMLName      xml.Name     `xml:"Bucket"`
	Name         xmltext.Text `xml:"Name"`
	CreationDate xmltext.Text `xml:"CreationDate"`
}

// listBuckets is RGWListBuckets_ObjStore_S3. Its get_params sets only
// limit = -1 (rgw_rest_s3.h:133-136 at v19.2.6): max-buckets and
// continuation-token are never read, and no ContinuationToken is written.
// The document streams in radosgw's order (rgw_rest_s3.cc:1511-1547 at
// v19.2.6, :1622-1658 at v20.2.4): send_response_begin sends the headers
// and the declaration, then opens the result, the owner and Buckets once the
// first page is read; send_response_data adds each page and flushes it;
// send_response_end closes the two sections and flushes, after a failed
// later page too. The owner is the requesting user, never its account.
func listBuckets(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	var cx *chunkedXML
	o := &op.ListBuckets{Limit: -1}
	o.Begin = func() error {
		var err error
		if cx, err = startChunkedXML(w, r); err != nil {
			return err
		}
		// dump_start fills the formatter before end_header, whose own flush
		// sends the declaration as the first chunk.
		cx.add(xmlHeader)
		if err := cx.flush(); err != nil {
			return err
		}
		cx.add(`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
		owner := listOwner{}
		if u := r.Identity.User; u != nil {
			owner.ID, owner.DisplayName = xmltext.Text(u.UserID.String()), xmltext.Text(u.DisplayName)
		}
		if err := cx.encode(owner); err != nil {
			return err
		}
		cx.add("<Buckets>")
		return nil
	}
	o.Page = func(page []meta.BucketEnt) error {
		for i := range page {
			b := &page[i]
			if err := cx.encode(listBucket{Name: xmltext.Text(b.Bucket.Name), CreationDate: xmltext.Text(ISO8601(b.CreationTime.Time))}); err != nil {
				return err
			}
		}
		return cx.flush()
	}
	err := op.Run(ctx, o, r)
	if cx == nil {
		return err
	}
	if err != nil {
		slog.WarnContext(ctx, "bucket listing failed after the response started", slog.String("request_id", r.ID), slog.Any("error", err))
	}
	cx.add("</Buckets></ListAllMyBucketsResult>")
	if err := cx.flush(); err != nil {
		slog.DebugContext(ctx, "bucket listing not delivered", slog.String("request_id", r.ID), slog.Any("error", err))
	}
	return nil
}
