package s3

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// getBucketACL is get_acls at bucket scope
// (RGWGetACLs_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3605-L3614 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3888-L3897 at v20.2.4):
// the bucket's ACL. The object scope's is getObjectACL.
func getBucketACL(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketACL{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, o.Policy.MarshalS3XML())
	return nil
}

// putBucketACL is put_acls at bucket scope: RGWPutACLs_ObjStore_S3::get_params
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3616-L3635 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3899-L3918 at v20.2.4),
// then the policy built from the canned ACL, the grant headers or the body by
// the evaluator, which also refuses a canned ACL beside a body, a new owner,
// too many grants and a public policy under a block of public ACLs; its errors
// are mapped through authz.ErrorFor. A body longer than rgw_max_put_param_size
// is radosgw's -ERANGE, which RGWPutACLs::execute answers as MalformedXML
// naming the limit
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5830-L5837 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6476-L6483 at v20.2.4).
// The 200 carries no body (writeEmptyXML).
func putBucketACL(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutBucketACL{Build: func(existing acl.Policy) (acl.Policy, error) {
		e, err := evaluator(r)
		if err != nil {
			return acl.Policy{}, err
		}
		body, err := readParamBody(r, func(limit uint64) error {
			return op.ErrMalformedXML.WithMessage(fmt.Sprintf("The XML you provided was larger than the maximum %d bytes allowed.", limit))
		})
		if err != nil {
			return acl.Policy{}, err
		}
		res := authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}
		p, err := e.BuildACL(ctx, r, res, existing, body, false)
		if err != nil {
			return acl.Policy{}, authz.ErrorFor(err)
		}
		return p, nil
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// evaluator is the request's authorizer as the policy evaluator, whose
// builders the ACL and policy writes call. Every gateway runs one; any other
// authorizer cannot build a policy, and the write is NotImplemented.
func evaluator(r *op.Request) (*authz.Evaluator, error) {
	e, ok := r.Env.Authz.(*authz.Evaluator)
	if !ok {
		return nil, fmt.Errorf("%w: building a policy needs the policy evaluator", op.ErrNotImplemented)
	}
	return e, nil
}
