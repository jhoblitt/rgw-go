package op

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// GetBucketPolicyStatus is RGWGetBucketPolicyStatus
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2584-L2594 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2764-L2774 at v20.2.4):
// whether the bucket is public.
type GetBucketPolicyStatus struct {
	// IsPublic is whether the bucket's policy or its ACL is public.
	IsPublic bool
}

var _ Op = (*GetBucketPolicyStatus)(nil)

// Name is RGWGetBucketPolicyStatus::name.
func (*GetBucketPolicyStatus) Name() string { return "get_bucket_policy_status" }

// Action is s3:GetBucketPolicyStatus.
func (*GetBucketPolicyStatus) Action() policy.Action { return policy.S3GetBucketPolicyStatus }

// OpMask is RGW_OP_TYPE_READ.
func (*GetBucketPolicyStatus) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket.
func (*GetBucketPolicyStatus) Init(ctx context.Context, r *Request) error { return loadBucket(ctx, r) }

// VerifyPermission is RGWGetBucketPolicyStatus::verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8595-L8606 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9563-L9574 at v20.2.4),
// which adds the bucket's tags when a policy names them.
func (o *GetBucketPolicyStatus) VerifyPermission(ctx context.Context, r *Request) error {
	return verifyBucketAction(ctx, r, o)
}

// Execute is RGWGetBucketPolicyStatus::execute
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8608-L8611 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9576-L9579 at v20.2.4):
// public when the bucket's policy is or its ACL is, whatever its
// public-access block sets. The policy is judged as Amazon S3 judges one, the
// same on both releases, where radosgw's is_public differs from that and
// between the releases (docs/exclusions.md, "A bucket policy is public as
// Amazon S3 judges it").
func (o *GetBucketPolicyStatus) Execute(ctx context.Context, r *Request) error {
	bacl, err := BucketACLFor(ctx, r.BucketRec)
	if err != nil {
		return err
	}
	p := storedBucketPolicy(ctx, r)
	o.IsPublic = (p != nil && p.IsPublic()) || bacl.IsPublic()
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetBucketPolicyStatus) Complete(context.Context, *Request) {}

// storedBucketPolicy is the request's bucket's policy as the authorizer reads
// it: BucketPolicyFor with the bucket's tenant under the cluster's release,
// nil for a bucket without one. One that does not parse is nil too: the
// authorizer refuses every requester but an admin for it, and radosgw leaves
// s->iam_policy unset for the requester it exempts, a system request on Squid
// and an admin on Tentacle
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L598-L615 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L628-L645 at v20.2.4).
func storedBucketPolicy(ctx context.Context, r *Request) *policy.Policy {
	p, err := BucketPolicyFor(ctx, r.BucketRec, r.BucketRec.Info.Bucket.Tenant, r.Env.Zone.Release())
	if err != nil {
		slog.WarnContext(ctx, "the bucket's stored policy does not parse, judging the bucket without it",
			slog.String("bucket", r.BucketRec.Info.Bucket.Name), slog.Any("error", err))
		return nil
	}
	return p
}

// BucketPolicyFor is get_iam_policy_from_attr
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L329-L338 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3276-L3285 at v20.2.4):
// rec's stored policy parsed with tenant under rel and, as for every stored
// policy, unsupported principals dropped; nil for a bucket without one. A
// policy that does not parse is the *policy.ParseError, wrapped. The
// authorizer and GetBucketPolicyStatus both read the policy through it, so
// the status judges the policy the checks enforce.
func BucketPolicyFor(ctx context.Context, rec *BucketRecord, tenant string, rel denc.Release) (*policy.Policy, error) {
	if rec == nil {
		return nil, nil
	}
	b, ok := rec.Attrs[AttrIAMPolicy]
	if !ok {
		return nil, nil
	}
	p, err := policy.Parse(string(b), policy.ParseOptions{Tenant: &tenant, Release: rel, Context: ctx})
	if err != nil {
		return nil, fmt.Errorf("parsing the policy of bucket %s: %w", rec.Info.Bucket.Name, err)
	}
	return p, nil
}
