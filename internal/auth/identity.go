package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// identity is LocalApplier wrapped in SysReqApplier: what op.Identity carries
// for the user record rec and its key that signed, which radosgw records under
// the id it looked up, accessKey (rgw_auth.cc:1019-1051 at v19.2.6,
// :1039-1071 at v20.2.4). An account user owns as the account but keeps the
// user's tenant (get_tenant, rgw_auth.h:740-742 at v19.2.6, :750-752 at
// v20.2.4), and radosgw's permission override takes an admin and a system
// user alike (is_admin_of, is_admin at v20.2.4).
//
// A system user's non-empty rgwx-uid names the effective owner, a user or an
// account, which must load or the request is EACCES; the tenant becomes the
// named one's and the identity stays the system user's
// (SysReqApplier::load_acct_info, rgw_auth_filters.h:282-317 at v19.2.6,
// :304-345 at v20.2.4; docs/exclusions.md has Tentacle's user record). A
// failure is logged by its code alone, without the rgwx-uid. An rgwx-uid
// holding a NUL is EACCES without a lookup, as go-ceph would hand librados
// the owner's object name as a C string that ends at the NUL.
func (v *Verifier) identity(ctx context.Context, rv *requestView, rec *op.UserRecord, accessKey string, key meta.AccessKey, account *meta.AccountInfo) (op.Identity, error) {
	info := rec.Info
	id := op.Identity{
		User:      &info,
		Owner:     meta.UserOwner(info.UserID),
		Account:   account,
		SubUser:   key.Subuser,
		Tenant:    info.UserID.Tenant,
		AccessKey: accessKey,
		OpMask:    info.OpMask,
		Caps:      info.Caps,
		Admin:     info.Admin != 0 || info.System != 0,
		System:    info.System != 0,
		Attrs:     rec.Attrs,
	}
	if account != nil {
		id.Owner = meta.AccountOwner(account.ID)
	}
	uid := rv.sysParams[sysParamPrefix+"uid"]
	if !id.System || uid == "" {
		return id, nil
	}
	if strings.IndexByte(uid, 0) >= 0 {
		return op.Identity{}, fmt.Errorf("%w: the rgwx-uid holds a NUL byte", op.ErrAccessDenied)
	}
	owner := meta.ParseOwner(uid)
	if owner.User == nil {
		acct, err := v.loadAccount(ctx, owner.Account)
		if err != nil {
			slog.Log(ctx, lookupLevel(ctx, err), "rgwx-uid account lookup failed", slog.String("code", op.ErrorCode(err)))
			return op.Identity{}, refusal(ctx, err, fmt.Errorf("%w: the rgwx-uid account does not load", op.ErrAccessDenied))
		}
		id.Owner, id.Tenant = owner, acct.Tenant
		return id, nil
	}
	if _, err := v.creds.GetUser(ctx, *owner.User); err != nil {
		if !errors.Is(err, op.ErrNoSuchUser) {
			slog.Log(ctx, lookupLevel(ctx, err), "rgwx-uid user lookup failed", slog.String("code", op.ErrorCode(err)))
		}
		return op.Identity{}, refusal(ctx, err, fmt.Errorf("%w: the rgwx-uid user does not load", op.ErrAccessDenied))
	}
	id.Owner, id.Tenant = owner, owner.User.Tenant
	return id, nil
}
