package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// defaultMaxKeys is the S3 listings' default_max
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.h#L160 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.h#L161 at v20.2.4).
const defaultMaxKeys = 1000

// MaxListingResults is rgw_max_listing_results, the bound on max-keys,
// max-parts and max-uploads: the option as librados reads it, which under
// Rook is the cluster's own release's library, and for an Env without
// options the release's default, 1000 on Squid and 5000 on Tentacle
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L3428-L3435 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3613-L3620 at v20.2.4).
func MaxListingResults(r *Request) int64 {
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
// the versions listing
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3033-L3121 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3267-L3365 at v20.2.4).
// The protocol layer fills the inputs from the query as
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
	// MarkerInstance is the versions listing's version-id-marker as sent,
	// null included, as ListObjectsParams.MarkerInstance takes it. The v2
	// form reads none.
	MarkerInstance string
	// RelistMarker is ListObjectsParams.RelistMarker: the v2 form with
	// versions sets it for a continuation-token.
	RelistMarker bool
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

// Name is RGWListBucket::name()
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L970 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1042 at v20.2.4),
// which the v2 handler inherits, so the usage log files both versions
// as list_bucket.
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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L523-L541 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L553-L571 at v20.2.4),
// or a bucket of another zonegroup PermanentRedirect (loadBucket).
func (*ListObjects) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWListBucket::verify_permission, which runs
// get_params first, so a max-keys that is not a number is InvalidArgument
// whatever the requester may do, then adds the listing's condition keys, and
// then authorizes the action against the bucket.
func (o *ListObjects) VerifyPermission(ctx context.Context, r *Request) error {
	n, err := ParseValueAndBound(o.MaxKeys, 0, MaxListingResults(r), defaultMaxKeys)
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
// delimiter is InvalidArgument; then the store lists a page of Max entries
// from the marker and its instance, which radosgw passes on as sent
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3099-L3110 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3340-L3351 at v20.2.4).
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
	res, err := r.Env.Buckets.ListObjects(ctx, rec, ListObjectsParams{
		Prefix: o.Prefix, Delimiter: o.Delimiter, Marker: o.Marker, MarkerInstance: o.MarkerInstance, RelistMarker: o.RelistMarker,
		MaxKeys: o.Max, ListVersions: o.ListVersions, AllowUnordered: o.AllowUnordered,
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
