package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// The object retention and legal-hold ops of this file are rgw_op.cc's
// RGWPutObjRetention, RGWGetObjRetention, RGWPutObjLegalHold and
// RGWGetObjLegalHold. Each loads the bucket and the head of the version the
// request names, as read_permissions does for them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1898-L1938 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1897-L1950 at v20.2.4),
// so that a missing object, version or a current delete marker is refused
// before the permission check by the authorizer's rule for a missing object,
// and authorizes its action against that version. Each write is
// modify_obj_attrs on the version the state resolved, through
// SetObjectAttrs, which the RADOS store guards on the write tag Init read
// where the head has one: a change to the version since then fails the
// write, so the retention a change was checked against is the one it
// replaces.

// The messages these ops set in s->err.message for a bucket without object
// lock (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8360-L8365,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8456-L8461,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8504-L8509 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8560-L8565 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9295-L9300,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9404-L9409,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9452-L9457 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9528-L9533 at v20.2.4),
// and PutObjectRetention's for a date in the past
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8387-L8392 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9322-L9327 at v20.2.4).
const (
	msgRetentionNoLock = "object retention can't be set if bucket object lock not configured"
	msgLegalHoldNoLock = "object legal hold can't be set if bucket object lock not enabled"
	msgLockNotSet      = "bucket object lock not configured"
	msgRetainUntilPast = "the retain-until date must be in the future"
)

// BypassGovernanceHeader is get_params' reading of
// x-amz-bypass-governance-retention, whose value is v: the value as a C
// string, url_decode'd, then "true" in any case, iequals in radosgw's C
// locale, where no other value bypasses
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4523-L4527 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5034-L5038 at v20.2.4).
// DeleteObject's and DeleteObjects' get_params read it the same way
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3476-L3480 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4268-L4272 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3726-L3730 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4786-L4790 at v20.2.4).
// An absent header reads as "", which does not bypass.
func BypassGovernanceHeader(v string) bool {
	// No rune outside ASCII folds to a letter of "true", so EqualFold is the
	// ASCII-only iequals.
	return strings.EqualFold(rgwtext.URLDecode(rgwtext.CString(v), false), "true")
}

// PutObjectRetention is RGWPutObjRetention
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8334-L8435 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9269-L9383 at v20.2.4).
type PutObjectRetention struct {
	// BypassHeader is x-amz-bypass-governance-retention as
	// BypassGovernanceHeader reads it, bypass_governance_mode.
	BypassHeader bool

	// body is the document get_params read; nil when VerifyPermission
	// refused the requester first.
	body []byte
	// bypass is bypass_governance_mode with bypass_perm, the
	// s3:BypassGovernanceRetention check, evaluated only for the header.
	bypass bool
}

var _ Op = (*PutObjectRetention)(nil)

// Name is RGWPutObjRetention::name.
func (*PutObjectRetention) Name() string { return "put_obj_retention" }

// Action is s3:PutObjectRetention, for a version as for a key.
func (*PutObjectRetention) Action() policy.Action { return policy.S3PutObjectRetention }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutObjectRetention) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket and the head of the version the request names.
func (*PutObjectRetention) Init(ctx context.Context, r *Request) error { return statHead(ctx, r) }

// VerifyPermission is RGWPutObjRetention::verify_permission, in its order:
// s3:PutObjectRetention on the object; the op mask, which radosgw's
// verify_op_mask
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L207-L211 at v19.2.7 and v20.2.4)
// checks before verify_permission runs, so that the body of a requester it
// refuses is never read; get_params, which reads the body under
// rgw_max_put_param_size as ReadParamBody reads it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4529-L4531 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5040-L5042 at v20.2.4);
// and, only when BypassHeader is set, s3:BypassGovernanceRetention on the
// object, whose access denial leaves the bypass off and refuses nothing, as
// bypass_perm is a bool. Any other error of that check, a fault such as
// policies whose tags cannot be read, is the request's, so that it is not
// answered as the refusal of a change the bypass would have let through,
// and still nothing is written. A requester the first check
// refuses is answered before the body is read, and radosgw's admin override
// then reaches execute with get_params never run, so an admin Run lets
// through that refusal reaches Execute with no document and no bypass
// (docs/ceph-upstream-bugs.md, "radosgw's PutObjectRetention reads no
// document for an admin its permission check refuses").
func (o *PutObjectRetention) VerifyPermission(ctx context.Context, r *Request) error {
	if err := VerifyObjectPermission(ctx, r, policy.S3PutObjectRetention, acl.PermFor(policy.S3PutObjectRetention)); err != nil {
		return err
	}
	if err := verifyOpMask(o, r); err != nil {
		return err
	}
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	o.body = body
	if o.BypassHeader {
		a := policy.S3BypassGovernanceRetention
		switch err := VerifyObjectPermission(ctx, r, a, acl.PermFor(a)); {
		case err == nil:
			o.bypass = true
		case !isAccessDenial(err):
			return fmt.Errorf("checking %s: %w", a, err)
		}
	}
	return nil
}

// Execute is RGWPutObjRetention::execute, in its order. A bucket whose flags
// do not enable object lock is 400 InvalidRequest. A document
// objectlock.ParseRetention refuses, an empty one included, is 400
// MalformedXML. A retain-until date whose whole second is before the clock
// is 400 InvalidArgument, compared exactly where radosgw reads the clock as
// a double (docs/exclusions.md, "A retain-until second is compared with the
// clock exactly"). A version that does not exist is NoSuchKey. An existing
// retention objectlock.DecodeRetention refuses, bytes past its encoding
// included, is radosgw's -EIO, 500 UnknownError (docs/exclusions.md, "An
// object-lock attr with bytes past its encoding does not decode"). The
// change from it is objectlock.RetentionChange's, with the bypass both the
// header and the permission, its refusals 403 AccessDenied with radosgw's
// messages; a lapsed retention binds none of them (docs/exclusions.md, "A
// lapsed retention binds no retention change"). The retention is written in
// RGWObjectRetention's version 2, its nanoseconds kept, by
// modify_obj_attrs
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2402-L2420 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2994-L3012 at v20.2.4),
// whose write fails with ConcurrentModification when the version changed
// since Init read the retention it checked, radosgw's -ECANCELED.
func (o *PutObjectRetention) Execute(ctx context.Context, r *Request) error {
	if !r.BucketRec.Info.ObjLockEnabled() {
		return ErrInvalidRequest.WithMessage(msgRetentionNoLock)
	}
	proposed, err := objectlock.ParseRetention(o.body)
	if err != nil {
		return lockError(err)
	}
	now := r.Env.Clock()
	if time.Unix(proposed.RetainUntil.Unix(), 0).Before(now) {
		return ErrInvalidArgument.WithMessage(msgRetainUntilPast)
	}
	st := r.ObjState
	if st == nil || !st.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	var old *meta.ObjectRetention
	if b, ok := st.Attrs[meta.AttrObjectRetention]; ok {
		v, err := objectlock.DecodeRetention(b)
		if err != nil {
			return fmt.Errorf("%w: the retention of %s: %w", ErrUnknown, r.Object.Name, err)
		}
		old = &v
	}
	if err := objectlock.RetentionChange(old, proposed, now, o.bypass); err != nil {
		return lockError(err)
	}
	return modifyObjAttr(ctx, r, meta.AttrObjectRetention, encodeAt(proposed, r.Env.Zone.Release()))
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutObjectRetention) Complete(context.Context, *Request) {}

// GetObjectRetention is RGWGetObjRetention
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8437-L8484 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9385-L9432 at v20.2.4).
type GetObjectRetention struct {
	// Retention is the version's retention.
	Retention meta.ObjectRetention
}

var _ Op = (*GetObjectRetention)(nil)

// Name is RGWGetObjRetention::name.
func (*GetObjectRetention) Name() string { return "get_obj_retention" }

// Action is s3:GetObjectRetention, for a version as for a key.
func (*GetObjectRetention) Action() policy.Action { return policy.S3GetObjectRetention }

// OpMask is RGW_OP_TYPE_READ.
func (*GetObjectRetention) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket and the head of the version the request names.
func (*GetObjectRetention) Init(ctx context.Context, r *Request) error { return statHead(ctx, r) }

// VerifyPermission is RGWGetObjRetention::verify_permission.
func (o *GetObjectRetention) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyObjectPermission(ctx, r, o.Action(), acl.PermFor(o.Action()))
}

// Execute is RGWGetObjRetention::execute: a bucket whose flags do not enable
// object lock is 400 InvalidRequest; a version that does not exist is
// NoSuchKey; one without a retention is 404
// ObjectLockConfigurationNotFoundError, with no message; and a retention
// objectlock.DecodeRetentionLenient refuses, one too short, is -EIO, 500
// UnknownError, where bytes past its encoding are left unread.
func (o *GetObjectRetention) Execute(_ context.Context, r *Request) error {
	b, err := lockAttr(r, meta.AttrObjectRetention)
	if err != nil {
		return err
	}
	v, err := objectlock.DecodeRetentionLenient(b)
	if err != nil {
		return fmt.Errorf("%w: the retention of %s: %w", ErrUnknown, r.Object.Name, err)
	}
	o.Retention = v
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetObjectRetention) Complete(context.Context, *Request) {}

// PutObjectLegalHold is RGWPutObjLegalHold
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8486-L8539 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9434-L9507 at v20.2.4).
type PutObjectLegalHold struct{}

var _ Op = (*PutObjectLegalHold)(nil)

// Name is RGWPutObjLegalHold::name.
func (*PutObjectLegalHold) Name() string { return "put_obj_legal_hold" }

// Action is s3:PutObjectLegalHold, for a version as for a key.
func (*PutObjectLegalHold) Action() policy.Action { return policy.S3PutObjectLegalHold }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutObjectLegalHold) OpMask() uint32 { return OpTypeWrite }

// Init loads the bucket and the head of the version the request names.
func (*PutObjectLegalHold) Init(ctx context.Context, r *Request) error { return statHead(ctx, r) }

// VerifyPermission is RGWPutObjLegalHold::verify_permission.
func (o *PutObjectLegalHold) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyObjectPermission(ctx, r, o.Action(), acl.PermFor(o.Action()))
}

// Execute is RGWPutObjLegalHold::execute, in its order: a bucket whose flags
// do not enable object lock is 400 InvalidRequest, before the body is read;
// then get_params, which reads the body under rgw_max_put_param_size as
// ReadParamBody reads it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1498-L1503 at v19.2.7 and v20.2.4);
// a document objectlock.ParseLegalHold refuses is 400 MalformedXML; a
// version that does not exist is NoSuchKey, which both releases' attr reads
// answer before the write; and the legal hold is written by
// modify_obj_attrs.
func (o *PutObjectLegalHold) Execute(ctx context.Context, r *Request) error {
	if !r.BucketRec.Info.ObjLockEnabled() {
		return ErrInvalidRequest.WithMessage(msgLegalHoldNoLock)
	}
	body, err := ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
	if err != nil {
		return err
	}
	h, err := objectlock.ParseLegalHold(body)
	if err != nil {
		return lockError(err)
	}
	if st := r.ObjState; st == nil || !st.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	return modifyObjAttr(ctx, r, meta.AttrObjectLegalHold, encodeAt(h, r.Env.Zone.Release()))
}

// Complete does nothing: the handler logs usage once the response is written.
func (*PutObjectLegalHold) Complete(context.Context, *Request) {}

// GetObjectLegalHold is RGWGetObjLegalHold
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8541-L8588 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9509-L9556 at v20.2.4).
type GetObjectLegalHold struct {
	// LegalHold is the version's legal hold.
	LegalHold meta.ObjectLegalHold
}

var _ Op = (*GetObjectLegalHold)(nil)

// Name is RGWGetObjLegalHold::name.
func (*GetObjectLegalHold) Name() string { return "get_obj_legal_hold" }

// Action is s3:GetObjectLegalHold, for a version as for a key.
func (*GetObjectLegalHold) Action() policy.Action { return policy.S3GetObjectLegalHold }

// OpMask is RGW_OP_TYPE_READ.
func (*GetObjectLegalHold) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket and the head of the version the request names.
func (*GetObjectLegalHold) Init(ctx context.Context, r *Request) error { return statHead(ctx, r) }

// VerifyPermission is RGWGetObjLegalHold::verify_permission.
func (o *GetObjectLegalHold) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyObjectPermission(ctx, r, o.Action(), acl.PermFor(o.Action()))
}

// Execute is RGWGetObjLegalHold::execute, GetObjectRetention's with the
// legal hold, which objectlock.DecodeLegalHoldLenient reads.
func (o *GetObjectLegalHold) Execute(_ context.Context, r *Request) error {
	b, err := lockAttr(r, meta.AttrObjectLegalHold)
	if err != nil {
		return err
	}
	h, err := objectlock.DecodeLegalHoldLenient(b)
	if err != nil {
		return fmt.Errorf("%w: the legal hold of %s: %w", ErrUnknown, r.Object.Name, err)
	}
	o.LegalHold = h
	return nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (*GetObjectLegalHold) Complete(context.Context, *Request) {}

// lockAttr is the Get ops' read of the version's attr name: a bucket without
// object lock is InvalidRequest, a version that does not exist NoSuchKey, and
// a version without the attr -ERR_NO_SUCH_OBJECT_LOCK_CONFIGURATION
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8469-L8473 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8573-L8577 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9417-L9421 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9541-L9545 at v20.2.4).
func lockAttr(r *Request, name string) ([]byte, error) {
	if !r.BucketRec.Info.ObjLockEnabled() {
		return nil, ErrInvalidRequest.WithMessage(msgLockNotSet)
	}
	st := r.ObjState
	if st == nil || !st.Exists {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	b, ok := st.Attrs[name]
	if !ok {
		return nil, ErrNoSuchObjectLockConfig
	}
	return b, nil
}

// lockError is the S3 error of an objectlock refusal: a document it refuses
// is MalformedXML, with no message, and a retention change it refuses
// AccessDenied with radosgw's message.
func lockError(err error) error {
	e, ok := errors.AsType[*objectlock.Error](err)
	switch {
	case !ok:
		return err
	case errors.Is(e.Err, objectlock.ErrMalformedXML):
		return fmt.Errorf("%w: %w", ErrMalformedXML, err)
	case errors.Is(e.Err, objectlock.ErrAccessDenied):
		return fmt.Errorf("%w: %w", ErrAccessDenied.WithMessage(e.Message), err)
	}
	return err
}
