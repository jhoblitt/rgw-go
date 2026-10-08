package admin

import (
	"context"
	"math"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newAccountHandlers are the account routes (rgw_rest_account.cc at v19.2.6
// and v20.2.4).
func newAccountHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"create_account":         createAccount,
		"get_account":            getAccount,
		"modify_account":         modifyAccount,
		"set_account_quota_info": setAccountQuota,
		"delete_account":         deleteAccount,
	}
}

// accountIdentity is the id, tenant and name arguments every account route
// reads.
func accountIdentity(a Args) op.AccountParams {
	var p op.AccountParams
	p.ID, _ = a.String("id", "")
	p.Tenant, _ = a.String("tenant", "")
	p.Name, _ = a.String("name", "")
	return p
}

// accountParams is what RGWOp_Account_Create and RGWOp_Account_Modify read
// (rgw_rest_account.cc:31-72 and :125-164 at v20.2.4): the identity, the
// email, and each limit present. radosgw reads the limits with get_int32
// and checks none: a value stringtol refuses stores the 0 the body starts
// from, which for max-buckets is no limit, and one past int32 wraps, a
// negative max-users, max-roles, max-groups or max-access-keys being no
// limit either (docs/ceph-upstream-bugs.md, "radosgw's account create and
// modify store an unparsable or wrapped limit, which can mean no limit").
// rgw-go records such a value in fl, which the route answers with
// ErrInvalidArgument after the permission check.
func accountParams(a Args, fl *flags) op.AccountParams {
	p := accountIdentity(a)
	p.Email, _ = a.String("email", "")
	for _, l := range []struct {
		name string
		dst  **int32
	}{
		{"max-users", &p.MaxUsers},
		{"max-roles", &p.MaxRoles},
		{"max-groups", &p.MaxGroups},
		{"max-access-keys", &p.MaxAccessKeys},
		{"max-buckets", &p.MaxBuckets},
	} {
		if v := checkedInt32(a, fl, l.name); v != nil {
			*l.dst = new(int32(*v)) //nolint:gosec // checkedInt32 keeps it within int32
		}
	}
	return p
}

// writeAccountInfo is encode_json("AccountInfo", info) in the body create,
// modify and info start their flusher for (rgw_account.cc:172-174,
// :267-269 and :491-493 at v19.2.6).
func writeAccountInfo(w http.ResponseWriter, r *op.Request, q Request, info meta.AccountInfo) error {
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("AccountInfo")
		info.Dump(f, rel)
		f.CloseSection()
	})
}

// createAccount is RGWOp_Account_Create::execute (rgw_rest_account.cc:31-102).
func createAccount(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	fl := &flags{a: q.Args}
	o := &op.CreateAccount{Params: accountParams(q.Args, fl)}
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return writeAccountInfo(w, r, q, o.Result)
}

// modifyAccount is RGWOp_Account_Modify::execute (rgw_rest_account.cc:115-168),
// which Squid also runs for PUT ?quota, so the quota arguments go unread.
func modifyAccount(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	fl := &flags{a: q.Args}
	o := &op.ModifyAccount{Params: accountParams(q.Args, fl)}
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return writeAccountInfo(w, r, q, o.Result)
}

// getAccount is RGWOp_Account_Get::execute (rgw_rest_account.cc:182-191),
// which reads no email.
func getAccount(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := &op.GetAccountInfo{Params: accountIdentity(q.Args)}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	return writeAccountInfo(w, r, q, o.Result)
}

// deleteAccount is RGWOp_Account_Delete::execute (rgw_rest_account.cc:204-221),
// which sends no body.
func deleteAccount(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := &op.RemoveAccount{Params: accountIdentity(q.Args)}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// setAccountQuota is Tentacle's RGWOp_Account_Quota_Set::execute
// (rgw_rest_account.cc:255-299 at v20.2.4): id and quota-type must be
// present and the type "account" or "bucket", or EINVAL; then max-size,
// max-objects and enabled when present.
//
// radosgw reads max-size and max-objects with get_int32 and enabled with
// get_bool, checking neither: a value stringtol refuses stores 0, one past
// int32 wraps, so a size of 3 GiB becomes negative and no limit, and an
// unparsable enabled disables the quota (docs/ceph-upstream-bugs.md). rgw-go
// answers each with ErrInvalidArgument after the permission check and
// stores nothing. The op reads no max-size-kb, which rgw-go refuses too
// rather than leave a caller's limit unset unnoticed.
func setAccountQuota(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	var p op.AccountParams
	var hasID, hasScope bool
	p.ID, hasID = a.String("id", "")
	p.QuotaScope, hasScope = a.String("quota-type", "")
	fl := &flags{a: a}
	if !hasID || !hasScope || (p.QuotaScope != "account" && p.QuotaScope != "bucket") {
		fl.err = op.ErrInvalidArgument
	}
	p.QuotaMaxSize = checkedInt32(a, fl, "max-size")
	p.QuotaMaxObjects = checkedInt32(a, fl, "max-objects")
	p.QuotaEnabled = fl.ptr("enabled")
	if a.Has("max-size-kb") && fl.err == nil {
		fl.err = op.ErrInvalidArgument
	}
	o := &op.ModifyAccount{Params: p}
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return writeAccountInfo(w, r, q, o.Result)
}

// checkedInt32 is a limit read within get_int32's range: nil when absent,
// and recorded in fl when empty, which stringtol reads as 0, or when
// stringtol refuses it or int32 cannot hold it.
func checkedInt32(a Args, fl *flags, name string) *int64 {
	v := quotaInt(a, fl, name)
	if raw, ok := a.Get(name); ok && raw == "" || v != nil && (*v < math.MinInt32 || *v > math.MaxInt32) {
		if fl.err == nil {
			fl.err = op.ErrInvalidArgument
		}
		return nil
	}
	return v
}
