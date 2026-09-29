package op

import (
	"context"
	"log/slog"

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
	// VerifyPermission is radosgw's verify_permission: op mask aside, which Run checks.
	VerifyPermission(ctx context.Context, r *Request) error
	Execute(ctx context.Context, r *Request) error
	// Complete records usage and stats; it never fails the request. It runs
	// once Execute has run, whatever Execute returned.
	Complete(ctx context.Context, r *Request)
}

// Run drives o through the lifecycle for r in rgw_process_authenticated's
// order: Init, the op-mask check, VerifyPermission, Execute, Complete. An
// Admin identity (auth sets Admin for admin and system users) overrides any
// error from VerifyPermission, as rgw_process_authenticated does for a system
// request or an is_admin_of identity (rgw_process.cc:228-236 at v19.2.6).
func Run(ctx context.Context, o Op, r *Request) error {
	if err := o.Init(ctx, r); err != nil {
		return err
	}
	if err := verifyOpMask(o, r); err != nil {
		return err
	}
	if err := o.VerifyPermission(ctx, r); err != nil {
		if !r.Identity.Admin {
			return err
		}
		slog.DebugContext(ctx, "overriding permissions for an admin identity", slog.String("op", o.Name()))
	}
	defer o.Complete(ctx, r)
	return o.Execute(ctx, r)
}

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
