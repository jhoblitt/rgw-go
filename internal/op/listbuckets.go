package op

import (
	"context"
	"math"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// defaultListBucketsChunk is rgw_list_buckets_max_chunk's default
// (src/common/options/rgw.yaml.in:1939-1948 at v19.2.6, :2039-2048 at
// v20.2.4).
const defaultListBucketsChunk = 1000

// ListBuckets is RGWListBuckets: the service-scope GET and HEAD. Execute
// hands the owner's buckets to Page one listing page at a time, as
// RGWListBuckets::execute hands each page to send_response_data
// (rgw_op.cc:2511-2607 at v19.2.6, :2743-2839 at v20.2.4).
type ListBuckets struct {
	// Marker is where the listing starts; Limit caps it, -1 for no limit.
	// radosgw's S3 get_params sets only limit = -1 and no marker
	// (rgw_rest_s3.h:133-136 at v19.2.6, :134-137 at v20.2.4).
	Marker string
	Limit  int

	// Begin is send_response_begin: called once, when the first page has
	// been read or at once when nothing is listed, before any Page. An error
	// Execute returns before Begin is the request's error.
	Begin func() error
	// Page is send_response_data: called with each page, in name order.
	Page func([]meta.BucketEnt) error
}

var _ Op = (*ListBuckets)(nil)

// Name is RGWListBuckets::name().
func (*ListBuckets) Name() string { return "list_buckets" }

// Action is the action RGWListBuckets::verify_permission evaluates.
func (*ListBuckets) Action() policy.Action { return policy.S3ListAllMyBuckets }

// OpMask is RGWListBuckets::op_mask().
func (*ListBuckets) OpMask() uint32 { return OpTypeRead }

// Init loads nothing: the listing names no bucket.
func (*ListBuckets) Init(context.Context, *Request) error { return nil }

// VerifyPermission is RGWListBuckets::verify_permission: s3:ListAllMyBuckets
// against the identity's own policies. An anonymous identity passes, as it
// has no user ACL to deny it (verify_user_permission_no_policy).
func (o *ListBuckets) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyUserPermission(ctx, r, o.Action())
}

// Execute pages the owner's buckets rgw_list_buckets_max_chunk at a time
// until Limit is reached or the listing ends, calling Begin once the first
// page is read and Page with every page. A zero Limit and an anonymous
// identity list nothing, and the scope guard still begins the response. A
// page that lists nothing ends the listing: radosgw loops only while the
// page leaves a marker to continue from.
func (o *ListBuckets) Execute(ctx context.Context, r *Request) error {
	if o.Limit == 0 || r.Identity.Anonymous {
		return o.Begin()
	}
	chunk := defaultListBucketsChunk
	if r.Env.Conf != nil {
		// radosgw converts a negative chunk to a huge unsigned count, which
		// lists everything, as the default chunk does page by page; a chunk
		// past what an int holds on every build lists everything too.
		if v, err := r.Env.Conf.Int64("rgw_list_buckets_max_chunk"); err == nil && v >= 0 && v <= math.MaxInt32 {
			chunk = int(v)
		}
	}
	marker, total, begun := o.Marker, 0, false
	for {
		want := chunk
		if o.Limit > 0 {
			want = min(chunk, o.Limit-total)
		}
		ents, next, more, err := r.Env.Users.ListUserBuckets(ctx, r.Identity.Owner, marker, want)
		if err != nil {
			// After Begin the listing is under way: the handler closes the
			// document, as the scope guard's send_response_end does.
			return FromRADOS(err, ScopeService)
		}
		total += len(ents)
		if !begun {
			if err := o.Begin(); err != nil {
				return err
			}
			begun = true
		}
		if err := o.Page(ents); err != nil {
			return err
		}
		if !more || len(ents) == 0 || (o.Limit > 0 && total >= o.Limit) {
			return nil
		}
		marker = next
	}
}

// Complete does nothing: the protocol handler logs the request's usage once
// its response is written.
func (*ListBuckets) Complete(context.Context, *Request) {}
