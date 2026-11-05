package op

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// msgForbiddenByLock is the s->err.message DeleteObject sets when object lock
// refuses it with -EACCES
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5213-L5219 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5603-L5609 at v20.2.4).
const msgForbiddenByLock = "forbidden by object lock"

// lockedDeleteRetries bounds the re-checks of a delete on a bucket with object
// lock whose removal the version's expected state refused, each reading the
// version again and checking its lock on what it read.
const lockedDeleteRetries = 3

// deleteUnderLock is the store's delete of key, with p, as DeleteObject and
// each key of DeleteObjects make it, with radosgw's object-lock check where it
// applies (lockTarget): the version the delete removes for good is read,
// following the key's OLH as get_obj_state does, and a state that exists is
// checked by objectlock.VerifyDelete at r.Env.Clock(). bypass is that the
// request asks for the governance bypass, x-amz-bypass-governance-retention,
// for a requester whose bypass permission may be evaluated, and only then is
// it (bypassAllowed). A state that does not exist, a delete marker's own
// version among them, is not checked, as radosgw skips the check on -ENOENT
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5186-L5221 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6852-L6879 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5576-L5611 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7780-L7807 at v20.2.4).
// A read that fails is the delete's error. A locked version is 403
// AccessDenied, "forbidden by object lock", and a lock attr that does not
// decode is radosgw's -EIO, 500 UnknownError.
//
// The removal is held to the state the check read, so that a retention or a
// legal hold written after the read refuses it: on a versioned bucket as
// DeleteParams.Expect, whatever the read found, a delete marker or no
// version at all, and on a bucket whose versioning was never enabled, whose
// delete takes no Expect, as DeleteParams.AssertEpoch of a version that
// exists. A removal refused so, ConcurrentModification, left the version
// as it was, so the version is read and checked again, up to
// lockedDeleteRetries times; past them the refusal is the answer, radosgw's
// lost race (docs/differences.md, "DeleteObjects checks
// s3:BypassGovernanceRetention for each key, on the key's ARN").
func deleteUnderLock(ctx context.Context, r *Request, key meta.ObjKey, p DeleteParams, bypass bool) (*DeleteObjectResult, error) {
	rec := r.BucketRec
	target, checked := lockTarget(rec, key)
	if !checked {
		return r.Env.Objects.DeleteObject(ctx, rec, key, p)
	}
	var lost error
	for range lockedDeleteRetries + 1 {
		st, err := r.Env.Objects.StatObject(ctx, rec, target)
		if err != nil {
			return nil, err
		}
		if lockErr := verifyDeleteLock(ctx, r, target, st, bypass); lockErr != nil {
			return nil, lockErr
		}
		q, guarded := guardRemoval(rec, p, st)
		res, err := r.Env.Objects.DeleteObject(ctx, rec, key, q)
		if !guarded || !errors.Is(err, ErrConcurrentModification) {
			return res, err
		}
		lost = err
		slog.DebugContext(ctx, "a delete under object lock found the version changed since its check; checking it again",
			slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", target.Name), slog.String("instance", target.Instance))
	}
	return nil, lost
}

// lockTarget is the version a delete of key in rec removes for good, whose
// retention and legal hold it checks, and false when there is none to check:
// on a bucket without object lock, and on one whose versioning is enabled for
// a delete naming no version, which writes a delete marker. A delete naming a
// version, null included, removes it, as radosgw checks it
// (check_obj_lock = have_instance() && obj_lock_enabled()). A delete naming no
// version removes the plain object outright on a bucket whose versioning was
// never enabled, and the null version, which the null delete marker
// replaces, on one whose versioning is suspended; radosgw checks neither
// (docs/differences.md, "A delete that removes a version for good on an
// object-lock bucket checks its lock").
func lockTarget(rec *BucketRecord, key meta.ObjKey) (meta.ObjKey, bool) {
	switch {
	case !rec.Info.ObjLockEnabled():
		return key, false
	case key.Instance != "", !rec.Info.Versioned():
		return key, true
	case !rec.Info.VersioningEnabled():
		key.Instance = meta.NullInstance
		return key, true
	}
	return key, false
}

// verifyDeleteLock is verify_object_lock on st, the version target as read,
// when it exists, with deleteUnderLock's bypass.
func verifyDeleteLock(ctx context.Context, r *Request, target meta.ObjKey, st *ObjectState, bypass bool) error {
	if !st.Exists {
		return nil
	}
	allowed := bypass && bypassAllowed(ctx, r, target, st)
	err := objectlock.VerifyDelete(st.Attrs, r.Env.Clock(), bypass, allowed)
	switch {
	case errors.Is(err, objectlock.ErrLocked):
		return fmt.Errorf("%w: %s: %w", ErrAccessDenied.WithMessage(msgForbiddenByLock), versionName(r.BucketRec, target), err)
	case err != nil:
		return fmt.Errorf("%w: the lock of %s: %w", ErrUnknown, versionName(r.BucketRec, target), err)
	}
	return nil
}

// bypassAllowed is bypass_perm for a delete of target, whose state st the
// check read: s3:BypassGovernanceRetention on target's object ARN, with the
// request's identity and its bucket's block, authorized by the identity's or
// the bucket's policies, or else for the bucket's owner alone
// (VerifyBucketPermissionOwnerOrPolicy), the object's tags read from st.
// radosgw's verify_bucket_permission falls back to the bucket's ACL, whose
// WRITE grant, everyone's under public-read-write, passes the bypass
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5153-L5156 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5543-L5546 at v20.2.4),
// where Amazon S3 maps no ACL permission to the action (docs/differences.md,
// "No ACL grant authorizes a delete's governance bypass"). Any refusal is
// "not allowed" and fails nothing, as bypass_perm is a bool; one that is no
// access denial, a fault, is logged. The check reads a copy of r, which
// for a DeleteObjects key is the copy Execute took before the keys, as
// another key's response writes the request itself meanwhile.
func bypassAllowed(ctx context.Context, r *Request, target meta.ObjKey, st *ObjectState) bool {
	kr := *r
	kr.Object, kr.ObjState = target, st
	a := policy.S3BypassGovernanceRetention
	err := VerifyBucketPermissionOwnerOrPolicy(ctx, &kr, a, acl.PermFor(a))
	if err != nil && !isAccessDenial(err) {
		slog.WarnContext(ctx, "the governance bypass check failed; the delete is checked without the bypass",
			slog.String("bucket", r.BucketRec.Info.Bucket.Name), slog.String("key", target.Name), slog.String("code", ErrorCode(err)))
	}
	return err == nil
}

// guardRemoval is p held to st, the version the check read, and whether it
// is held: on a versioned bucket st is p's Expect whatever it found, so a
// version written where the read found none, a null version above all,
// refuses the removal as one changed since would; and on any other bucket
// the RADOS version of a state that exists is its AssertEpoch, a version
// that does not exist holding nothing there.
func guardRemoval(rec *BucketRecord, p DeleteParams, st *ObjectState) (DeleteParams, bool) {
	switch {
	case rec.Info.Versioned():
		p.Expect = st
		return p, true
	case !rec.Info.Versioned() && st.Exists && st.Epoch != 0:
		p.AssertEpoch = st.Epoch
		return p, true
	}
	return p, false
}

// versionName names key's version in rec for an error.
func versionName(rec *BucketRecord, key meta.ObjKey) string {
	if key.Instance == "" {
		return fmt.Sprintf("%s/%s", rec.Info.Bucket.Name, key.Name)
	}
	return fmt.Sprintf("%s/%s version %q", rec.Info.Bucket.Name, key.Name, key.Instance)
}
