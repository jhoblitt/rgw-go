// Package mptest checks a multipart store's upload listing the way a client
// pages it, for the specs of every op.MultipartStore.
package mptest

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// Lister is the one op.MultipartStore method the checks call.
type Lister interface {
	ListUploads(ctx context.Context, rec *op.BucketRecord, p op.ListUploadsParams) (op.ListUploadsResult, error)
}

// Listing is what a client gathers from a listing: each upload as its key
// and id, and each common prefix, in the order the pages gave them.
type Listing struct {
	Uploads  [][2]string
	Prefixes []string
	Pages    int
}

// ErrNoEnd reports a listing still truncated after more pages than it has
// items to give.
var ErrNoEnd = errors.New("mptest: the listing did not end")

// unbounded is a page larger than any spec's listing.
const unbounded = 10000

// Page lists p's uploads from its markers on, page after page, following
// each page's next markers until one is not truncated. It gives up with
// ErrNoEnd after maxPages pages.
func Page(ctx context.Context, s Lister, rec *op.BucketRecord, p op.ListUploadsParams, maxPages int) (Listing, error) {
	var l Listing
	for {
		if l.Pages == maxPages {
			return l, fmt.Errorf("%w after %d pages of %d uploads from %+v", ErrNoEnd, l.Pages, p.MaxUploads, p)
		}
		res, err := s.ListUploads(ctx, rec, p)
		if err != nil {
			return l, err
		}
		l.Pages++
		if n := len(res.Uploads) + len(res.CommonPrefixes); n > max(p.MaxUploads, 0) {
			return l, fmt.Errorf("page %d holds %d items, more than %d", l.Pages, n, p.MaxUploads)
		}
		for i := range res.Uploads {
			l.Uploads = append(l.Uploads, [2]string{res.Uploads[i].Key.Name, res.Uploads[i].ID})
		}
		l.Prefixes = append(l.Prefixes, res.CommonPrefixes...)
		if !res.Truncated {
			return l, nil
		}
		p.KeyMarker, p.UploadIDMarker = res.NextKeyMarker, res.NextUploadIDMarker
	}
}

// CheckPaging pages p's listing at every page size from 1 to one more than
// it has items, and fails unless each paging ends and gives exactly what the
// listing gives in one page: the same uploads and common prefixes, each
// once, in the same order.
func CheckPaging(ctx context.Context, s Lister, rec *op.BucketRecord, p op.ListUploadsParams) error {
	p.KeyMarker, p.UploadIDMarker = "", ""
	p.MaxUploads = unbounded
	want, err := Page(ctx, s, rec, p, 1)
	if err != nil {
		return fmt.Errorf("listing in one page: %w", err)
	}
	items := len(want.Uploads) + len(want.Prefixes)
	for size := 1; size <= items+1; size++ {
		p.MaxUploads = size
		got, err := Page(ctx, s, rec, p, items+2)
		if err != nil {
			return fmt.Errorf("pages of %d: %w", size, err)
		}
		if !slices.Equal(got.Uploads, want.Uploads) || !slices.Equal(got.Prefixes, want.Prefixes) {
			return fmt.Errorf("pages of %d gave uploads %v and prefixes %v, one page %v and %v",
				size, got.Uploads, got.Prefixes, want.Uploads, want.Prefixes)
		}
	}
	return nil
}
