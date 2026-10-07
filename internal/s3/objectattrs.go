package s3

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// putObjectACL is put_acls at object scope: RGWPutACLs_ObjStore_S3::get_params
// (:3616-3635, [T] :3899-3918), then the policy built from the canned ACL, the
// grant headers or the body by the evaluator, which also refuses a canned
// ACL beside a body, a new owner, too many grants and a public policy under a
// block of public ACLs; its errors are mapped through authz.ErrorFor. The
// body is read only once the requester is authorized, as RGWPutACLs::execute
// calls get_params (rgw_op.cc:5828 at v19.2.6, :6474 at v20.2.4), through
// what authentication left in r.Body to its final Read; a body longer than
// rgw_max_put_param_size is MalformedXML naming the limit (rgw_op.cc
// :5830-5837 at v19.2.6, :6476-6483 at v20.2.4). The 200 carries no body
// (writeEmptyXML).
func putObjectACL(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutObjectACL{Build: func(existing acl.Policy) (acl.Policy, error) {
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
		p, err := e.BuildACL(ctx, r, res, existing, body, true)
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

// putObjectTags is put_obj_tags: RGWPutObjTags_ObjStore_S3::get_params
// (:776-815, [T] :858-897) once the requester is authorized, the body read
// through what authentication left in r.Body to its final Read, a body
// longer than rgw_max_put_param_size InvalidRange, and the document parsed
// into at most ten tags, its errors mapped through authz.ErrorFor; 200 with
// no body (send_response :817-825, [T] :899-907; writeEmptyXML).
func putObjectTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutObjectTagging{Params: func(_ context.Context, o *op.PutObjectTagging) error {
		body, err := readParamBody(r, invalidRange)
		if err != nil {
			return err
		}
		set, err := tags.ParseXML(body, tags.MaxObjectTags)
		if err != nil {
			return authz.ErrorFor(err)
		}
		o.Set = set
		return nil
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// deleteObjectTags is delete_obj_tags: 204 with no body
// (RGWDeleteObjTags_ObjStore_S3::send_response, :827-837, [T] :909-919),
// under writeEmptyXML's type.
func deleteObjectTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteObjectTagging{}, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusNoContent)
	return nil
}
