package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/policy"
)

// AdminOp is the part every admin op shares, RGWRESTOp (rgw_rest.h:522-537
// at v19.2.6). Its Action is ActionNone and its OpMask 0, RGWOp::op_mask's
// default; Init does nothing, and neither does Complete: radosgw registers
// the admin API's managers without set_logging, so process_request never
// logs an admin request's usage (rgw_appmain.cc:354-361, rgw_process.cc:322
// and :461-463 at v19.2.6). Each op writes its own VerifyPermission, which
// is RGWRESTOp::verify_permission (rgw_rest.cc:1687-1690): CheckCaps with
// the op's check_caps pair, which Run lets an admin identity through.
type AdminOp struct{}

// Action is ActionNone: radosgw's IAM never evaluates an admin op.
func (AdminOp) Action() policy.Action { return policy.ActionNone }

// OpMask is 0: an admin op needs no op-mask bit.
func (AdminOp) OpMask() uint32 { return 0 }

// Init loads nothing.
func (AdminOp) Init(context.Context, *Request) error { return nil }

// Complete logs nothing.
func (AdminOp) Complete(context.Context, *Request) {}

// CheckCaps is RGWUserCaps::check_cap on the identity's caps: its -EPERM,
// AccessDenied, when the identity lacks typ or any bit of perm.
func CheckCaps(r *Request, typ string, perm uint32) error {
	if r.Identity.Caps.Check(typ, perm) {
		return nil
	}
	return ErrAccessDenied
}
