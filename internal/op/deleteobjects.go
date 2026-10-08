package op

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The defaults RGWDeleteMultiObj uses: rgw_max_put_param_size's, for a
// configuration that cannot be read, and DELETE_MULTI_OBJ_MAX_NUM, for a
// negative rgw_delete_multi_obj_max_num (rgw_op.cc:7040-7044 at v19.2.6,
// :7954-7958 at v20.2.4).
const (
	defaultMaxPutParamSize = 1 << 20
	deleteMultiObjMaxNum   = 1000
)

// DeleteResult is one key's outcome of a DeleteObjects: what
// send_partial_response renders (rgw_rest_s3.cc:4273-4329 at v19.2.6).
type DeleteResult struct {
	// Key is the requested key; its Instance renders as VersionId.
	Key meta.ObjKey
	// Err is nil for success.
	Err error
	// DeleteMarker and MarkerVersionID are del_op->result: whether the
	// delete created a delete marker, and its version id. Both stay unset:
	// every delete rgw-go performs is unversioned, whose result carries
	// neither.
	DeleteMarker    bool
	MarkerVersionID string
}

// DeleteObjectsEntry is one <Object> of the request (RGWMultiDelObject).
type DeleteObjectsEntry struct {
	// Key is the entry's Key, with its VersionId as Instance.
	Key meta.ObjKey
	// IfMatch, IfMatchSize and IfMatchLastModified are the entry's ETag,
	// Size and LastModifiedTime, which v20.2.4 reads and rgw-go applies on
	// both releases; nil, nil and zero when absent. An ETag element present
	// and empty is a condition, which fails (rgw_multi_del.cc:38-40 at
	// v20.2.4).
	IfMatch             *string
	IfMatchSize         *uint64
	IfMatchLastModified time.Time
}

// DeleteObjects is RGWDeleteMultiObj (rgw_op.cc:6758-7106 at v19.2.6,
// :7697-8020 at v20.2.4). Init reads the body; Execute parses it through
// Parse and makes the whole-request checks, then calls Begin once and hands
// each key's outcome to Result as the key completes, as radosgw streams its
// response. A check that fails before the first key goes to Status instead.
type DeleteObjects struct {
	// Parse is the protocol's parser for the body (RGWMultiDelXMLParser,
	// rgw_multi_del.cc), called where execute parses; its error is sent
	// through Status.
	Parse func(body []byte) ([]DeleteObjectsEntry, error)
	// Status is send_status: called instead of Begin when Execute fails
	// before the first key, to send that error's status line and nothing
	// else.
	Status func(err error)
	// Begin is begin_response: called once, after the whole-request checks
	// and before the first key is deleted, to send the status, the headers
	// and the DeleteResult open tag. An error from it ends Execute.
	Begin func() error
	// Result is send_partial_response: called once per key, in completion
	// order, never concurrently.
	Result func(DeleteResult)

	body []byte
}

var _ Op = (*DeleteObjects)(nil)

// Name is RGWDeleteMultiObj::name.
func (o *DeleteObjects) Name() string { return "multi_object_delete" }

// Action is the action of the request's keys without a version; each key
// is authorized on its own.
func (o *DeleteObjects) Action() policy.Action { return policy.S3DeleteObject }

// OpMask is RGW_OP_TYPE_DELETE.
func (o *DeleteObjects) OpMask() uint32 { return OpTypeDelete }

// Init is init_permissions, then get_params during init_processing
// (rgw_op.cc:6758-6766 at v19.2.6, :7697-7705 at v20.2.4; rgw_rest.cc
// :1660-1674 at v19.2.6): the bucket, NoSuchBucket before the body is
// touched, then the whole body as ReadParamBody reads it under
// rgw_max_put_param_size. get_params asks read_all_input for no chunked
// input, which it ignores, so a chunked body is read too.
func (o *DeleteObjects) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	o.body, err = ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	return err
}

// VerifyPermission makes the refusals radosgw makes on the request's bucket
// in init_permissions, before verify_permission: a suspended bucket, a bucket
// policy that does not parse, an ACL that does not decode. Those come marked
// from the authorizer's bucket check, which this one call makes. Any other
// refusal is none here: RGWDeleteMultiObj::verify_permission authorizes
// nothing (rgw_op.cc:6768-6780 at v19.2.6, :7707-7719 at v20.2.4), and each
// key is authorized as it is deleted.
func (o *DeleteObjects) VerifyPermission(ctx context.Context, r *Request) error {
	if err := VerifyBucketPermission(ctx, r, policy.S3DeleteObject, acl.PermFor(policy.S3DeleteObject)); IsBeforeVerify(err) {
		return err
	}
	return nil
}

// Execute is RGWDeleteMultiObj::execute (rgw_op.cc:7007-7106 at v19.2.6,
// :7921-8014 at v20.2.4): the whole-request checks, each answered with
// send_status alone (goto error at v19.2.6; v20.2.4 returns and its
// send_response calls send_status, :8016-8020), then begin_response and the
// keys, at most max(1, rgw_multi_obj_del_max_aio) of them in flight.
func (o *DeleteObjects) Execute(ctx context.Context, r *Request) error {
	objects, err := o.checkRequest(r)
	if err != nil {
		o.Status(err)
		return err
	}
	if err := o.Begin(); err != nil {
		return err
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(o.maxAIO(r))
	for _, e := range objects {
		g.Go(func() error {
			res := o.deleteOne(gctx, r, e)
			mu.Lock()
			defer mu.Unlock()
			o.Result(res)
			return nil
		})
	}
	return g.Wait()
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *DeleteObjects) Complete(context.Context, *Request) {}

// checkRequest is execute's work before begin_response, in its order
// (rgw_op.cc:7019-7068 at v19.2.6, :7923-7983 at v20.2.4): the body, its
// parse, Tentacle's refusal of an empty list, the key count, and MFA. A
// bucket whose versioning or object lock rgw-go does not serve is refused
// first, and a request naming a version last.
func (o *DeleteObjects) checkRequest(r *Request) ([]DeleteObjectsEntry, error) {
	if err := versioningUnserved(r.BucketRec); err != nil {
		return nil, err
	}
	if len(o.body) == 0 {
		// data.c_str() is NULL for an empty bufferlist.
		return nil, fmt.Errorf("%w: an empty body", ErrInvalidArgument)
	}
	objects, err := o.Parse(o.body)
	if err != nil {
		return nil, err
	}
	tentacle := r.Env.Zone.Release() >= denc.Tentacle
	if tentacle && len(objects) == 0 {
		return nil, ErrMalformedXML.WithMessage("Missing required element Object")
	}
	if maxNum := maxDeleteObjects(r); len(objects) > maxNum {
		if tentacle {
			return nil, ErrMalformedXML.WithMessage(fmt.Sprintf("Object count limit %d exceeded", maxNum))
		}
		return nil, fmt.Errorf("%w: %d keys over rgw_delete_multi_obj_max_num %d", ErrMalformedXML, len(objects), maxNum)
	}
	if mfaEnabled(r.BucketRec) {
		for _, e := range objects {
			if e.Key.Instance != "" {
				return nil, ErrMFARequired
			}
		}
	}
	for _, e := range objects {
		if err := versioningUnserved(r.BucketRec, e.Key); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// maxDeleteObjects is rgw_delete_multi_obj_max_num as execute holds it, in
// an int: cut to 32 bits, and DELETE_MULTI_OBJ_MAX_NUM when negative.
func maxDeleteObjects(r *Request) int {
	v := int64(deleteMultiObjMaxNum)
	if r.Env.Conf != nil {
		if c, err := r.Env.Conf.Int64("rgw_delete_multi_obj_max_num"); err == nil {
			v = int64(int32(c)) //nolint:gosec // int max_num = conf value
		}
	}
	if v < 0 {
		v = deleteMultiObjMaxNum
	}
	return int(v)
}

// maxAIO is std::max<uint32_t>(1, rgw_multi_obj_del_max_aio): the option cut
// to 32 bits, at least 1. A configuration that cannot be read takes the
// option's default, 16.
func (o *DeleteObjects) maxAIO(r *Request) int {
	v := uint64(16)
	if r.Env.Conf != nil {
		if c, err := r.Env.Conf.Uint64("rgw_multi_obj_del_max_aio"); err == nil {
			v = c
		}
	}
	return int(max(uint32(v), 1)) //nolint:gosec // std::max<uint32_t> narrows the option
}

// deleteOne is handle_individual_object (rgw_op.cc:6818-6910 at v19.2.6,
// :7743-7855 at v20.2.4): the key, its permission, s3:DeleteObjectVersion for
// a key naming any version, the null one included (:6829-6831, :7766-7768),
// checked in the request's bucket with the request's public-access block, and
// the delete with the entry's conditions, where a missing key is success.
func (o *DeleteObjects) deleteOne(ctx context.Context, r *Request, e DeleteObjectsEntry) DeleteResult {
	res := DeleteResult{Key: e.Key}
	if e.Key.Name == "" {
		res.Err = ErrInvalidArgument
		return res
	}
	a := policy.S3DeleteObject
	if e.Key.Instance != "" {
		a = policy.S3DeleteObjectVersion
	}
	if err := VerifyBucketPermissionIn(ctx, r, a, acl.PermFor(a), r.BucketRec, e.Key); err != nil {
		// -EACCES whatever the authorizer's reason, which it has logged.
		res.Err = ErrAccessDenied
		return res
	}
	p := DeleteParams{IfMatch: storeCondition(e.IfMatch), IfMatchSize: e.IfMatchSize, IfMatchLastModified: e.IfMatchLastModified}
	if err := r.Env.Objects.DeleteObject(ctx, r.BucketRec, e.Key, p); !errors.Is(err, ErrNoSuchKey) {
		res.Err = err
	}
	return res
}
