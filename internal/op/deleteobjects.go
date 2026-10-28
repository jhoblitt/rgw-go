package op

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
// negative rgw_delete_multi_obj_max_num
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7040-L7044 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7954-L7958 at v20.2.4).
const (
	defaultMaxPutParamSize = 1 << 20
	deleteMultiObjMaxNum   = 1000
)

// DeleteResult is one key's outcome of a DeleteObjects: what
// send_partial_response renders
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4303-L4359 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4821-L4871 at v20.2.4).
type DeleteResult struct {
	// Key is the requested key; its Instance renders as VersionId.
	Key meta.ObjKey
	// Err is nil for success.
	Err error
	// DeleteMarker and MarkerVersionID are del_op->result as
	// send_partial_response takes it: whether the version the delete wrote
	// or removed is a delete marker, and that version's id. Both stay unset
	// for a failure, an unversioned delete, the removal of a version that is
	// no marker and a missing key.
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
	// and empty is a condition, which fails
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_multi_del.cc#L38-L40 at v20.2.4).
	IfMatch             *string
	IfMatchSize         *uint64
	IfMatchLastModified time.Time
}

// DeleteObjects is RGWDeleteMultiObj
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6764-L7119 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7697-L8020 at v20.2.4).
// Init reads the body; Execute parses it through Parse and makes the
// whole-request checks, then calls Begin once and hands each key's outcome
// to Result as the key completes, as radosgw streams its response. A check
// that fails before the first key goes to Status instead.
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
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6758-L6766 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7697-L7705 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1660-L1674 at v19.2.6):
// the bucket, NoSuchBucket before the body is touched, then the whole
// body as ReadParamBody reads it under rgw_max_put_param_size.
// get_params asks read_all_input for no chunked input, which it
// ignores, so a chunked body is read too.
func (o *DeleteObjects) Init(ctx context.Context, r *Request) error {
	err := loadBucket(ctx, r)
	if err != nil {
		return err
	}
	o.body, err = ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	return err
}

// VerifyPermission makes the refusals radosgw makes on the request's bucket
// in init_permissions, before verify_permission: a suspended bucket, a bucket
// policy that does not parse, an ACL that does not decode. Those come marked
// from the authorizer's bucket check, which this one call makes. Any other
// refusal is none here: RGWDeleteMultiObj::verify_permission authorizes
// nothing
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6768-L6780 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7707-L7719 at v20.2.4),
// and each key is authorized as it is deleted.
func (o *DeleteObjects) VerifyPermission(ctx context.Context, r *Request) error {
	if err := VerifyBucketPermission(ctx, r, policy.S3DeleteObject, acl.PermFor(policy.S3DeleteObject)); IsBeforeVerify(err) {
		return err
	}
	return nil
}

// Execute is RGWDeleteMultiObj::execute
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7019-L7119 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7921-L8014 at v20.2.4):
// the whole-request checks, each answered with send_status alone (goto
// error at v19.2.7; v20.2.4 returns and its send_response calls
// send_status,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8016-L8020),
// then begin_response and the keys, at most max(1,
// rgw_multi_obj_del_max_aio) of them in flight, as handle_objects deletes
// them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7008-L7017 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7910-L7919 at v20.2.4).
// On a bucket whose versioning was ever enabled, bucket->versioned(), the
// keys go by name, as handle_versioned_objects groups them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6924-L6979 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7857-L7892 at v20.2.4):
// every delete of a name but its last skips the key's OLH update, and the
// last of each name starts once every other delete has returned, so that
// its update replays the deletes of its name before it, one update a name
// where each delete would make its own. Any other bucket's keys go in the
// request's order.
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
	report := func(res DeleteResult) {
		mu.Lock()
		defer mu.Unlock()
		o.Result(res)
	}
	if !r.BucketRec.Info.Versioned() {
		return o.deleteAll(ctx, r, objects, false, report)
	}
	rest, last := groupByName(objects)
	if err := o.deleteAll(ctx, r, rest, true, report); err != nil {
		return err
	}
	return o.deleteAll(ctx, r, last, false, report)
}

// deleteAll deletes entries, at most maxAIO at once, each skipping its key's
// OLH update when skipOLHUpdate is set, and reports each outcome as it
// completes. It returns once every delete has.
func (o *DeleteObjects) deleteAll(ctx context.Context, r *Request, entries []DeleteObjectsEntry, skipOLHUpdate bool, report func(DeleteResult)) error {
	var g errgroup.Group
	g.SetLimit(o.maxAIO(r))
	for _, e := range entries {
		g.Go(func() error {
			report(o.deleteOne(ctx, r, e, skipOLHUpdate))
			return nil
		})
	}
	return g.Wait()
}

// groupByName is handle_versioned_objects' grouping: entries by their key's
// name, the names in the byte order of the std::map that holds them and each
// name's entries in the request's order; rest holds every entry of a name
// but its last, and last the last of each name.
func groupByName(entries []DeleteObjectsEntry) (rest, last []DeleteObjectsEntry) {
	byName := map[string][]DeleteObjectsEntry{}
	for _, e := range entries {
		byName[e.Key.Name] = append(byName[e.Key.Name], e)
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		group := byName[name]
		rest = append(rest, group[:len(group)-1]...)
		last = append(last, group[len(group)-1])
	}
	return rest, last
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *DeleteObjects) Complete(context.Context, *Request) {}

// checkRequest is execute's work before begin_response, in its order
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7031-L7081 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7923-L7983 at v20.2.4):
// the body, its parse, Tentacle's refusal of an empty list, the key
// count, and MFA delete. A bucket with object lock is refused first
// (deleteLockUnserved).
//
// On a bucket with MFA delete a request any of whose keys names a version
// is ERR_MFA_REQUIRED, 403 AccessDenied, unless its x-amz-mfa verified a
// device of the bucket's owner (OwnerMFAVerified), the check radosgw made
// at v19.2.6 and makes on main. v19.2.7 and v20.2.4 invert it: they refuse a
// request with a key naming no version and pass one whose every key names a
// version (docs/exclusions.md, "DeleteObjects asks for MFA for the keys that
// name a version"). A request whose x-amz-mfa is present and does not verify
// the owner's device is refused whatever its keys name, as S3's
// DeleteObjects reference says: "If you provide an invalid token, whether
// there are versioned keys in the request or not, the entire Multi-Object
// Delete request will fail"
// (https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObjects.html).
// The check is execute's, so op.Run's admin override never passes it.
func (o *DeleteObjects) checkRequest(r *Request) ([]DeleteObjectsEntry, error) {
	if err := deleteLockUnserved(r.BucketRec); err != nil {
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
	if r.BucketRec.Info.MFAEnabled() && !OwnerMFAVerified(r) {
		if r.MFAPresent {
			return nil, fmt.Errorf("%w: an x-amz-mfa that does not verify the bucket owner's device fails the request", ErrMFARequired)
		}
		for _, e := range objects {
			if e.Key.Instance != "" {
				return nil, fmt.Errorf("%w: deleting version %q of %s needs the bucket owner's verified x-amz-mfa", ErrMFARequired, e.Key.Instance, e.Key.Name)
			}
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

// deleteOne is handle_individual_object
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6824-L6921 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7743-L7855 at v20.2.4):
// the key, its permission, s3:DeleteObjectVersion for a key naming any
// version, the null one included
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6837-L6847,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7765-L7775),
// checked in the request's bucket with the request's public-access block,
// and the delete with the entry's conditions and the requester as a delete
// marker's owner, del_op->params.obj_owner being s->owner, where a missing
// key is success. radosgw reads the key's state ahead of the delete, failing
// the key on a read that fails otherwise than with ENOENT; the store's
// delete makes the reads it needs, whose failure fails the key alike.
func (o *DeleteObjects) deleteOne(ctx context.Context, r *Request, e DeleteObjectsEntry, skipOLHUpdate bool) DeleteResult {
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
	p := DeleteParams{
		IfMatch: storeCondition(e.IfMatch), IfMatchSize: e.IfMatchSize, IfMatchLastModified: e.IfMatchLastModified,
		SkipOLHUpdate: skipOLHUpdate,
	}
	p.MarkerOwner, p.MarkerOwnerDisplayName = requesterOwner(r)
	d, err := r.Env.Objects.DeleteObject(ctx, r.BucketRec, e.Key, p)
	switch {
	case errors.Is(err, ErrNoSuchKey):
	case err != nil:
		res.Err = err
	case d != nil && d.DeleteMarker:
		res.DeleteMarker, res.MarkerVersionID = true, d.VersionID
	}
	return res
}
