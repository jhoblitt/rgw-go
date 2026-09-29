package op

import (
	"context"
	"io"
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

// CompletePart names one part of a completion, in the client's order.
type CompletePart struct {
	Number int
	ETag   string
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
	// lock and returns it; a part that does not match is ErrInvalidPart, an
	// out-of-order list ErrInvalidPartOrder.
	Complete(ctx context.Context, up *Upload, parts []CompletePart) (*PutResult, error)
	Abort(ctx context.Context, up *Upload) error
}
