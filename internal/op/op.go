package op

import (
	"context"
	"errors"
	"log/slog"
	"syscall"

	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The RGW_OP_TYPE_* bits of a user's op mask (rgw_common.h:232-237 at v19.2.6, :254-259 at v20.2.4).
const (
	OpTypeRead   uint32 = 0x01
	OpTypeWrite  uint32 = 0x02
	OpTypeDelete uint32 = 0x04
	OpTypeModify uint32 = OpTypeWrite | OpTypeDelete
	OpTypeAll    uint32 = OpTypeRead | OpTypeWrite | OpTypeDelete
)

// Op is radosgw's op lifecycle in order. One type implements it per S3 or
// admin operation, with typed input fields the protocol layer fills after
// parsing and typed result fields it renders afterwards.
type Op interface {
	// Name is radosgw's RGWOp::name() ("get_obj", "list_buckets"), even where the
	// route name differs: the usage log files each request under it as its
	// category (rgw_log.cc:245, :556-559), and the ops log and metrics use it too.
	Name() string
	// Action is the s3:* action VerifyPermission evaluates.
	Action() policy.Action
	// OpMask is the RGW_OP_TYPE_* bits the identity must hold.
	OpMask() uint32
	// Init loads the bucket and object state the op needs into r.
	Init(ctx context.Context, r *Request) error
	// VerifyPermission is radosgw's verify_permission, op mask aside, which Run
	// checks. It passes on the authorizer's errors, wrapped at most, so that a
	// refusal radosgw makes before verify_op_mask (rgw_process.cc:173-211),
	// which the authorizer marks with BeforeVerify, reaches Run with its mark.
	VerifyPermission(ctx context.Context, r *Request) error
	Execute(ctx context.Context, r *Request) error
	// Complete runs once Execute has run, whatever Execute returned, and
	// never fails the request. It logs no usage: the protocol handler does,
	// through LogUsage, once the response is written.
	Complete(ctx context.Context, r *Request)
}

// Run drives o through the lifecycle for r in rgw_process_authenticated's
// order (rgw_process.cc:173-211 at v19.2.6 and v20.2.4). radosgw refuses some
// requests before verify_op_mask, while authenticating or in init_permissions
// or read_permissions, and never overrides those refusals. The authorizer
// makes those checks inside VerifyPermission and marks their refusals with
// BeforeVerify, so Run calls VerifyPermission ahead of the op-mask check and
// returns a marked refusal first; the op mask then decides ahead of any other
// VerifyPermission error. An Admin identity (auth sets Admin for admin and
// system users) is let through such an error only when it is an access
// denial (EACCES, EPERM or ERR_AUTHORIZATION), because the others might be
// invalid input (rgw_process.cc:228-239 at v20.2.4). radosgw at v19.2.6 lets
// an admin through any error (:228-236); docs/exclusions.md records the
// difference.
func Run(ctx context.Context, o Op, r *Request) error {
	if err := o.Init(ctx, r); err != nil {
		return err
	}
	permErr := o.VerifyPermission(ctx, r)
	if IsBeforeVerify(permErr) {
		return permErr
	}
	if err := verifyOpMask(o, r); err != nil {
		return err
	}
	if permErr != nil {
		if !r.Identity.Admin || !isAccessDenial(permErr) {
			return permErr
		}
		slog.DebugContext(ctx, "overriding permissions for an admin identity", slog.String("op", o.Name()))
	}
	defer o.Complete(ctx, r)
	return o.Execute(ctx, r)
}

// BeforeVerify marks err as a refusal radosgw makes before verify_permission:
// while authenticating, in init_permissions or in read_permissions. Run
// returns it ahead of the op-mask check and never overrides it. The mark
// unwraps to err, so errors.Is, AsError and the rendered error are unchanged;
// BeforeVerify(nil) is nil.
func BeforeVerify(err error) error {
	if err == nil {
		return nil
	}
	return &beforeVerifyError{err: err}
}

// IsBeforeVerify reports whether err, or an error it wraps, carries the
// BeforeVerify mark.
func IsBeforeVerify(err error) bool {
	return errors.As(err, new(*beforeVerifyError))
}

type beforeVerifyError struct{ err error }

func (e *beforeVerifyError) Error() string { return e.err.Error() }

func (e *beforeVerifyError) Unwrap() error { return e.err }

// verifyOpMask is RGWOp::verify_op_mask: the identity holds every bit the op
// needs, and a non-system identity never modifies a read-only zone.
func verifyOpMask(o Op, r *Request) error {
	required := o.OpMask()
	if r.Identity.OpMask&required != required {
		return ErrAccessDenied
	}
	if required&OpTypeModify != 0 && !r.Identity.System && r.Env != nil && r.Env.Zone != nil && r.Env.Zone.Zone().ReadOnly {
		return ErrAccessDenied
	}
	return nil
}

// isAccessDenial reports whether err is a refusal radosgw lets an admin
// through at v20.2.4: -EACCES, -EPERM or -ERR_AUTHORIZATION. It reads Errno
// because ERR_MFA_REQUIRED shares AccessDenied/403 with EACCES.
func isAccessDenial(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && (e.Errno == int(syscall.EACCES) || e.Errno == int(syscall.EPERM) || e.Errno == ErrAuthorization.Errno)
}
