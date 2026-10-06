package op

import (
	"context"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// Upload is an in-progress multipart upload.
type Upload struct {
	ID        string
	Bucket    *BucketRecord
	Key       meta.ObjKey
	Owner     meta.Owner
	OwnerName string
	Initiated time.Time
	Placement meta.PlacementRule
	// Attrs are the head xattrs the completed object receives.
	Attrs map[string][]byte
	// WriteTag is the completed head's idtag and tail_tag; radosgw passes the
	// request id (rgw_op.cc:6482-6485 at v19.2.6, s->req_id). Empty selects a
	// random tag.
	WriteTag string
	// IfMatch and IfNoneMatch are CompleteMultipartUpload's conditional
	// headers, which v20.2.4 honors (rgw_rest_s3.cc:4572-4573 at v20.2.4) and
	// rgw-go honors on both releases (docs/exclusions.md, "Write conditions
	// are Tentacle's on Squid too"); empty means unset.
	IfMatch, IfNoneMatch string
}

// UploadParams shapes CreateUpload.
type UploadParams struct {
	Owner     meta.Owner
	OwnerName string
	Placement meta.PlacementRule
	Attrs     map[string][]byte
}

// Part is one uploaded part.
type Part struct {
	Number int
	ETag   string
	Size   uint64
	Mtime  time.Time
}

// PartResult describes a written part.
type PartResult struct {
	ETag  string
	Size  uint64
	Mtime time.Time
}

// ListPartsResult is one page of ListParts.
type ListPartsResult struct {
	Parts      []Part
	NextMarker int
	Truncated  bool
}

// ListUploadsParams is one page of ListUploads.
type ListUploadsParams struct {
	Prefix, Delimiter         string
	KeyMarker, UploadIDMarker string
	MaxUploads                int
}

// ListUploadsResult is one page of in-progress uploads.
type ListUploadsResult struct {
	Uploads            []Upload
	CommonPrefixes     []string
	NextKeyMarker      string
	NextUploadIDMarker string
	Truncated          bool
}

// UploadListing is the bucket listing RadosBucket::list_multiparts makes for
// p (driver/rados/rgw_sal_rados.cc:915-956 at v19.2.6, :935-976 at v20.2.4):
// the multipart namespace with MultipartMetaFilter, meta.IsMultipartMeta, so
// part heads neither appear nor count, p's prefix and delimiter, which apply
// to the meta names "<key>.<id>.meta" (docs/ceph-upstream-bugs.md,
// "radosgw's ListMultipartUploads applies the prefix and the delimiter to the
// meta object's name"), and the marker RGWMPObj(key_marker,
// upload_id_marker) names, "<key>..meta" for an empty upload id marker
// (rgw_rest.cc:1646-1655 at v19.2.6, :1651-1660 at v20.2.4).
func UploadListing(p ListUploadsParams) ListObjectsParams {
	lp := ListObjectsParams{
		Prefix: p.Prefix, Delimiter: p.Delimiter, MaxKeys: p.MaxUploads, NS: meta.NSMultipart, NameFilter: meta.IsMultipartMeta,
	}
	if p.KeyMarker != "" {
		lp.Marker = meta.MultipartMetaName(p.KeyMarker, p.UploadIDMarker)
	}
	return lp
}

// UploadsFromListing is the page of uploads UploadListing's listing lr
// gives: each entry's upload, the key and id its name holds, with its owner
// and mtime, and lr's common prefixes. The next markers name the last upload
// listed whether or not the page is truncated, as
// RGWListBucketMultiparts::execute sets them (rgw_op.cc:6741-6744 at
// v19.2.6, :7680-7683 at v20.2.4). A truncated page whose last counted item
// is a common prefix names that prefix as the key marker with no upload id
// marker: "<prefix>..meta" falls inside the prefix, which the next page's
// listing skips past. radosgw lists a common prefix only when its name
// passes MultipartMetaFilter, as a whole meta name does under a delimiter
// that is a suffix of ".meta", and ends such a page with the last upload's
// markers or none (docs/exclusions.md, "ListMultipartUploads keeps the
// common prefixes radosgw drops"). lr.NextMarker plays no part: a listing
// round that stops half full leaves it on whatever name it read last, a
// refused part head as well.
func UploadsFromListing(rec *BucketRecord, lr ListObjectsResult) ListUploadsResult {
	res := ListUploadsResult{CommonPrefixes: lr.CommonPrefixes, Truncated: lr.Truncated}
	for i := range lr.Entries {
		e := &lr.Entries[i]
		key, id, _ := meta.ParseMultipartMeta(e.Key.Name)
		res.Uploads = append(res.Uploads, Upload{
			ID: id, Bucket: rec, Key: meta.ObjKey{Name: key}, Owner: e.Owner, OwnerName: e.OwnerDisplayName, Initiated: e.Mtime,
		})
	}
	if n := len(res.Uploads); n > 0 {
		res.NextKeyMarker, res.NextUploadIDMarker = res.Uploads[n-1].Key.Name, res.Uploads[n-1].ID
	}
	// Entries and prefixes each come in listing order, and a prefix sorts
	// after every name listed before the names it covers, so the later of
	// the last of each is the last item counted.
	if n := len(lr.CommonPrefixes); lr.Truncated && n > 0 {
		cp := lr.CommonPrefixes[n-1]
		if len(lr.Entries) == 0 || cp > lr.Entries[len(lr.Entries)-1].Key.Name {
			res.NextKeyMarker, res.NextUploadIDMarker = cp, ""
		}
	}
	return res
}

// CompletePart names one part of a completion, in the client's order.
type CompletePart struct {
	Number int
	ETag   string
}

// SortCompleteParts is the std::map<int, string> RGWMultiCompleteUpload::xml_end
// fills from a completion's parts (rgw_multi.cc:44-53 at v19.2.6 and
// v20.2.4): sorted by number, the last ETag of a repeated number kept.
func SortCompleteParts(parts []CompletePart) []CompletePart {
	byNum := map[int]string{}
	for _, p := range parts {
		byNum[p.Number] = p.ETag
	}
	out := make([]CompletePart, 0, len(byNum))
	for _, n := range slices.Sorted(maps.Keys(byNum)) {
		out = append(out, CompletePart{Number: n, ETag: byNum[n]})
	}
	return out
}

//counterfeiter:generate . MultipartStore

// MultipartStore implements the seven multipart operations over radosgw's layout.
type MultipartStore interface {
	CreateUpload(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p UploadParams) (*Upload, error)
	GetUpload(ctx context.Context, rec *BucketRecord, key meta.ObjKey, uploadID string) (*Upload, error)
	PutPart(ctx context.Context, up *Upload, n int, body io.Reader, p PutParams) (*PartResult, error)
	CopyPart(ctx context.Context, up *Upload, n int, src *ObjectState, rng ByteRange) (*PartResult, error)
	ListParts(ctx context.Context, up *Upload, marker, maxParts int) (ListPartsResult, error)
	ListUploads(ctx context.Context, rec *BucketRecord, p ListUploadsParams) (ListUploadsResult, error)
	// Complete assembles parts into the object under the RGWCompleteMultipart
	// lock and returns it; a part that does not match is ErrInvalidPart.
	// Parts are sorted by number before validation, the last of a repeated
	// number kept, as radosgw's std::map<int, string> keeps them
	// (rgw_multi.cc:44-53 at v19.2.6 and v20.2.4), so no order error exists.
	Complete(ctx context.Context, up *Upload, parts []CompletePart) (*PutResult, error)
	Abort(ctx context.Context, up *Upload) error
}
