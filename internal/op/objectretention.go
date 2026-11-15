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

	// retention is the document's retention as Init parsed it, nil when the
	// body was not read or its read or its parse failed: readErr is the
	// read's error and parseErr the parse's.
	retention         *meta.ObjectRetention
	readErr, parseErr error
	// authorized is that VerifyPermission passed the requester, which hands
	// the document on to Execute.
	authorized bool
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

// Init is initLockDocument's: the bucket, the document, which it parses
// once, if it was read, into the retention Execute writes, then the head of
// the version the request names.
func (o *PutObjectRetention) Init(ctx context.Context, r *Request) error {
	doc, err := initLockDocument(ctx, o, r)
	if err != nil {
		return err
	}
	o.readErr = doc.readErr
	if doc.read && doc.readErr == nil {
		v, perr := objectlock.ParseRetention(doc.body)
		if perr != nil {
			o.parseErr = perr
		} else {
			o.retention = &v
		}
	}
	return nil
}

// VerifyPermission is RGWPutObjRetention::verify_permission, in its order,
// with the document Init read: s3:PutObjectRetention on the object,
// authorized by verifyLockWrite, with the document's retention, when it
// parsed, as r.Lock for the policies' object-lock keys; the op mask, which
// radosgw's verify_op_mask
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L207-L211 at v19.2.7 and v20.2.4)
// checks before verify_permission runs; get_params' read of the body under
// rgw_max_put_param_size, whose error Init kept
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4529-L4531 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5040-L5042 at v20.2.4);
// and, only when BypassHeader is set, s3:BypassGovernanceRetention on the
// object, authorized as verifyLockWrite authorizes, whose access denial
// leaves the bypass off and refuses nothing, as bypass_perm is a bool. Any
// other error of that check, a fault such as policies whose tags cannot be
// read, is the request's, so that it is not answered as the refusal of a
// change the bypass would have let through, and still nothing is written.
// A document that does not parse sets no key, and Execute refuses it.
// radosgw's admin override reaches execute with get_params never run, so an
// admin Run lets through the check's refusal reaches Execute with no
// document handed on and no bypass (docs/ceph-upstream-bugs.md, "radosgw's
// PutObjectRetention reads no document for an admin its permission check
// refuses").
func (o *PutObjectRetention) VerifyPermission(ctx context.Context, r *Request) error {
	if err := VerifyObjectPrelude(ctx, r); err != nil {
		return err
	}
	if o.retention != nil {
		r.Lock = &LockConditions{Retention: o.retention}
	}
	if err := verifyLockWrite(ctx, r, policy.S3PutObjectRetention); err != nil {
		return err
	}
	if err := verifyOpMask(o, r); err != nil {
		return err
	}
	if o.readErr != nil {
		return o.readErr
	}
	o.authorized = true
	if o.BypassHeader {
		a := policy.S3BypassGovernanceRetention
		switch err := VerifyBucketPermissionOwnerOrPolicy(ctx, r, a, acl.PermFor(a)); {
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
// objectlock.ParseRetention refused, an empty one included, is 400
// MalformedXML, and so is the request of an admin Run let through
// VerifyPermission's refusal, which hands no document on, as radosgw's
// execute parses the data get_params never read. A retain-until date whose
// whole second is before the clock is 400 InvalidArgument, compared exactly
// where radosgw reads the clock as a double (docs/differences.md, "A
// retain-until second is compared with the clock exactly"). A version that
// does not exist is NoSuchKey. An existing retention
// objectlock.DecodeRetention refuses, bytes past its encoding included, is
// radosgw's -EIO, 500 UnknownError (docs/differences.md, "An object-lock attr
// with bytes past its encoding does not decode"). The
// change from it is objectlock.RetentionChange's, with the bypass both the
// header and the permission, its refusals 403 AccessDenied with radosgw's
// messages; a lapsed retention binds none of them (docs/differences.md, "A
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
	switch {
	case !o.authorized:
		return fmt.Errorf("%w: no document handed on past a refused permission check", ErrMalformedXML)
	case o.parseErr != nil:
		return lockError(o.parseErr)
	case o.retention == nil:
		return fmt.Errorf("%w: no document", ErrMalformedXML)
	}
	proposed := *o.retention
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
type PutObjectLegalHold struct {
	// hold is the document's legal hold as Init parsed it, nil when the body
	// was not read or its read or its parse failed: readErr is the read's
	// error and parseErr the parse's, which Execute answers where get_params
	// and the parse do.
	hold              *meta.ObjectLegalHold
	readErr, parseErr error
}

var _ Op = (*PutObjectLegalHold)(nil)

// Name is RGWPutObjLegalHold::name.
func (*PutObjectLegalHold) Name() string { return "put_obj_legal_hold" }

// Action is s3:PutObjectLegalHold, for a version as for a key.
func (*PutObjectLegalHold) Action() policy.Action { return policy.S3PutObjectLegalHold }

// OpMask is RGW_OP_TYPE_WRITE.
func (*PutObjectLegalHold) OpMask() uint32 { return OpTypeWrite }

// Init is initLockDocument's: the bucket, the document, which it parses
// once, if it was read, into the legal hold Execute writes, then the head of
// the version the request names.
func (o *PutObjectLegalHold) Init(ctx context.Context, r *Request) error {
	doc, err := initLockDocument(ctx, o, r)
	if err != nil {
		return err
	}
	o.readErr = doc.readErr
	if doc.read && doc.readErr == nil {
		h, perr := objectlock.ParseLegalHold(doc.body)
		if perr != nil {
			o.parseErr = perr
		} else {
			o.hold = &h
		}
	}
	return nil
}

// VerifyPermission is RGWPutObjLegalHold::verify_permission, its action
// authorized by verifyLockWrite after VerifyObjectPrelude's refusals, with
// the document's legal hold, when it parsed, as r.Lock for the policies'
// object-lock keys. radosgw reads the body in execute; the document is kept
// for Execute whatever the check answers, so an admin Run lets through a
// refusal is served the document it sent, as radosgw serves it.
func (o *PutObjectLegalHold) VerifyPermission(ctx context.Context, r *Request) error {
	if err := VerifyObjectPrelude(ctx, r); err != nil {
		return err
	}
	if o.hold != nil {
		r.Lock = &LockConditions{LegalHold: o.hold}
	}
	return verifyLockWrite(ctx, r, o.Action())
}

// Execute is RGWPutObjLegalHold::execute, in its order: a bucket whose flags
// do not enable object lock is 400 InvalidRequest; then get_params' read of
// the body under rgw_max_put_param_size, made in Init, whose error comes
// here
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1498-L1503 at v19.2.7 and v20.2.4);
// a document objectlock.ParseLegalHold refused is 400 MalformedXML; a
// version that does not exist is NoSuchKey, which both releases' attr reads
// answer before the write; and the legal hold is written by
// modify_obj_attrs.
func (o *PutObjectLegalHold) Execute(ctx context.Context, r *Request) error {
	if !r.BucketRec.Info.ObjLockEnabled() {
		return ErrInvalidRequest.WithMessage(msgLegalHoldNoLock)
	}
	switch {
	case o.readErr != nil:
		return o.readErr
	case o.parseErr != nil:
		return lockError(o.parseErr)
	case o.hold == nil:
		return fmt.Errorf("%w: no document", ErrMalformedXML)
	}
	if st := r.ObjState; st == nil || !st.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	return modifyObjAttr(ctx, r, meta.AttrObjectLegalHold, encodeAt(*o.hold, r.Env.Zone.Release()))
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

// lockDocument is what initLockDocument read of the request's body: read is
// that the op mask passed and the body was read, readErr that read's error,
// which the op answers where radosgw's get_params does. A requester the op
// mask refuses has none of the three set.
type lockDocument struct {
	body    []byte
	readErr error
	read    bool
}

// initLockDocument is PutObjectRetention's and PutObjectLegalHold's Init:
// the bucket, a missing one NoSuchBucket; then, for a requester whose op
// mask passes, the document, read under rgw_max_put_param_size as
// ReadParamBody reads it; then the head of the version the request names.
// The document comes before the version is looked up, as DeleteObjects reads
// its body, because the policies' object-lock keys are taken from it before
// the permission check: read after the version, its body would be read, and
// net/http's 100 Continue sent, only for a version that exists, which would
// tell a requester the missing-object rule refuses (VerifyObjectPrelude)
// what that rule hides. The op mask depends on the identity alone, and a
// requester it refuses is never read, as radosgw's verify_op_mask refuses it
// before verify_permission; its lockDocument is empty, so that the op parses
// no document for it.
func initLockDocument(ctx context.Context, o Op, r *Request) (lockDocument, error) {
	var doc lockDocument
	if err := loadBucket(ctx, r); err != nil {
		return doc, err
	}
	if verifyOpMask(o, r) == nil {
		doc.body, doc.readErr = ReadParamBody(r, confSize(r, "rgw_max_put_param_size", defaultMaxPutParamSize))
		doc.read = true
	}
	st, err := r.Env.Objects.StatObject(ctx, r.BucketRec, r.Object)
	if err != nil {
		return doc, err
	}
	r.ObjState = st
	return doc, nil
}

// verifyLockWrite authorizes a, s3:PutObjectRetention or
// s3:PutObjectLegalHold, on the version the request names as a write's
// object-lock headers are authorized (verifyLockHeaders), once the op has
// made VerifyObjectPrelude's refusals: the identity's
// and the bucket's policies decide, the version's tags added as they need
// them, and where neither decides the bucket's owner alone passes
// (VerifyBucketPermissionOwnerOrPolicy). PutObjectRetention's
// s3:BypassGovernanceRetention is authorized the same way. radosgw evaluates
// the three actions with verify_object_permission
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8340,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8348 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8492 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9275,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9283 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9440 at v20.2.4),
// whose fallback to the object's ACL passes the version's owner, who holds
// FULL_CONTROL on what it wrote: a requester allowed s3:PutObject alone sets
// a lock on its version there, lifts a legal hold set on it, and with the
// bypass header shortens a GOVERNANCE retention on it (docs/differences.md,
// "No ACL grant authorizes PutObjectRetention, PutObjectLegalHold or their
// governance bypass").
func verifyLockWrite(ctx context.Context, r *Request, a policy.Action) error {
	return VerifyBucketPermissionOwnerOrPolicy(ctx, r, a, acl.PermFor(a))
}

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
