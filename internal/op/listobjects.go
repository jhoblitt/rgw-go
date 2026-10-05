package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// defaultMaxKeys is the S3 listings' default_max (rgw_rest_s3.h:160 at
// v19.2.6, :161 at v20.2.4).
const defaultMaxKeys = 1000

// maxListingResults is rgw_max_listing_results, the bound on max-keys: the
// option as librados reads it, which under Rook is the cluster's own
// release's library, and for an Env without options the release's default,
// 1000 on Squid and 5000 on Tentacle (src/common/options/rgw.yaml.in:3428-3435
// at v19.2.6, :3613-3620 at v20.2.4).
func maxListingResults(r *Request) int64 {
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Uint64("rgw_max_listing_results"); err == nil {
			return int64(v) //nolint:gosec // parse_value_and_bound takes the uint64 option as a long
		}
	}
	if r.Env.Zone != nil && r.Env.Zone.Release() >= denc.Tentacle {
		return 5000
	}
	return 1000
}

// ListObjects is RGWListBucket, the S3 bucket listing in both versions and
// the versions listing (rgw_op.cc:3033-3121 at v19.2.6, :3267-3365 at
// v20.2.4). The protocol layer fills the inputs from the query as
// RGWListBucket_ObjStore_S3's get_params reads it; VerifyPermission bounds
// MaxKeys into Max, and Execute fills Result.
type ListObjects struct {
	// V2 is list-type=2, RGWListBucket_ObjStore_S3v2.
	V2 bool
	// ListVersions is the versions subresource.
	ListVersions      bool
	Prefix, Delimiter string
	// Marker is v1's marker, the versions listing's key-marker, or v2's
	// continuation-token, else its start-after.
	Marker string
	// MaxKeys is max-keys as sent, "" when absent.
	MaxKeys      string
	EncodingType string
	// AllowUnordered is radosgw's non-standard allow-unordered.
	AllowUnordered bool
	// FetchOwner, StartAfter and ContinuationToken are v2's, each Has* that
	// the parameter was sent.
	FetchOwner        bool
	StartAfter        string
	HasStartAfter     bool
	ContinuationToken string
	HasToken          bool

	// Max is MaxKeys bounded.
	Max    int
	Result ListObjectsResult
}

var _ Op = (*ListObjects)(nil)

// Name is RGWListBucket::name() (rgw_op.h:970 at v19.2.6, :1042 at
// v20.2.4), which the v2 handler inherits, so the usage log files both
// versions as list_bucket.
func (*ListObjects) Name() string { return "list_bucket" }

// Action is the action RGWListBucket::verify_permission authorizes.
func (o *ListObjects) Action() policy.Action {
	if o.ListVersions {
		return policy.S3ListBucketVersions
	}
	return policy.S3ListBucket
}

// OpMask is RGW_OP_TYPE_READ.
func (*ListObjects) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket. A missing bucket is NoSuchBucket here, before the
// parameters are read, as rgw_build_bucket_policies fails init_permissions
// (rgw_op.cc:523-541 at v19.2.6, :569-582 at v20.2.4).
func (*ListObjects) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	return nil
}

// VerifyPermission is RGWListBucket::verify_permission, which runs
// get_params first, so a max-keys that is not a number is InvalidArgument
// whatever the requester may do, then adds the listing's condition keys, and
// then authorizes the action against the bucket.
func (o *ListObjects) VerifyPermission(ctx context.Context, r *Request) error {
	n, err := ParseValueAndBound(o.MaxKeys, 0, maxListingResults(r), defaultMaxKeys)
	if err != nil {
		return err
	}
	o.Max = n
	r.List = &ListConditions{Prefix: o.Prefix, Delimiter: o.Delimiter, MaxKeys: n}
	a := o.Action()
	return VerifyBucketPermission(ctx, r, a, acl.PermFor(a))
}

// Execute is RGWListBucket::execute: Tentacle refuses an indexless bucket,
// "Indexless buckets cannot be listed"; an unordered listing with a
// delimiter is InvalidArgument; then the store lists a page of Max entries.
// The versions of a bucket whose versioning was ever enabled are not
// listed (docs/exclusions.md): versioning is not implemented.
func (o *ListObjects) Execute(ctx context.Context, r *Request) error {
	rec := r.BucketRec
	if rec == nil {
		return ErrNoSuchBucket
	}
	if r.Env.Zone != nil && r.Env.Zone.Release() >= denc.Tentacle && rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless {
		return ErrMethodNotAllowed.WithMessage("Indexless buckets cannot be listed")
	}
	if o.AllowUnordered && o.Delimiter != "" {
		return ErrInvalidArgument
	}
	if o.ListVersions && rec.Info.Flags&(meta.BucketVersioned|meta.BucketVersionsSuspended) != 0 {
		return ErrNotImplemented
	}
	res, err := r.Env.Buckets.ListObjects(ctx, rec, ListObjectsParams{
		Prefix: o.Prefix, Delimiter: o.Delimiter, Marker: o.Marker, MaxKeys: o.Max,
		ListVersions: o.ListVersions, AllowUnordered: o.AllowUnordered,
	})
	if err != nil {
		return err
	}
	o.Result = res
	return nil
}

// Complete does nothing: the handler logs the request's usage once its
// response is written.
func (*ListObjects) Complete(context.Context, *Request) {}
