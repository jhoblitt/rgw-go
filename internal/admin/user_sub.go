package admin

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newUserSubHandlers are the user routes for keys, subusers, caps and
// quotas.
func newUserSubHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"create_access_key": createKey,
		"remove_access_key": removeKey,
		"create_subuser":    createSubuser,
		"modify_subuser":    modifySubuser,
		"remove_subuser":    removeSubuser,
		"add_user_caps":     addCaps,
		"remove_user_caps":  removeCaps,
		"get_quota_info":    getQuotaInfo,
		"set_quota_info":    setQuotaInfo,
	}
}

// uidArg is the uid argument as rgw_user's string constructor parses it.
func uidArg(a Args) meta.UserID {
	uid, _ := a.String("uid", "")
	return meta.ParseUserID(uid)
}

// createKey is RGWOp_Key_Create::execute (driver/rados/rgw_rest_user.cc:680-726
// at v19.2.6, :690-736 at v20.2.4): generate-key and active default to
// true, active applies when named, and a non-empty key-type other than
// swift or s3 is undefined. The body is the user's Swift keys or S3 keys
// by the type check_op settled on, secrets included, as
// RGWUserAdminOp_Key::create dumps them whatever the caller's caps
// (driver/rados/rgw_user.cc:2608-2622 at v19.2.6).
func createKey(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewCreateKey()
	o.UID = uidArg(a)
	o.Key.Subuser, _ = a.String("subuser", "")
	o.Key.AccessKey, _ = a.String("access-key", "")
	o.Key.SecretKey, _ = a.String("secret-key", "")
	if t, _ := a.String("key-type", ""); t != "" {
		o.Key.Type = op.ParseKeyType(t)
	}
	fl := &flags{a: a}
	o.Key.GenerateKey = fl.get("generate-key", true)
	if active, ok := fl.present("active", true); ok {
		o.Key.Active = &active
	}
	// get_bool reads an empty value as true, which would make "active=" a
	// reactivation; rgw-go refuses it with the unparsable values.
	if v, ok := a.Get("active"); ok && v == "" && fl.err == nil {
		fl.err = op.ErrInvalidArgument
	}
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) {
		switch o.Type {
		case op.KeyTypeSwift:
			dumpSwiftKeys(f, o.Result)
		case op.KeyTypeS3:
			dumpAccessKeys(f, o.Result)
		}
	})
}

// removeKey is RGWOp_Key_Remove::execute (driver/rados/rgw_rest_user.cc:742-773
// at v19.2.6, :752-783 at v20.2.4), which sends no body.
func removeKey(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewRemoveKey()
	o.UID = uidArg(a)
	o.Key.Subuser, _ = a.String("subuser", "")
	o.Key.AccessKey, _ = a.String("access-key", "")
	if t, _ := a.String("key-type", ""); t != "" {
		o.Key.Type = op.ParseKeyType(t)
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// subuserKeyType is the key-type argument as the subuser bodies read it:
// Swift unless it is exactly "s3".
func subuserKeyType(a Args) op.KeyType {
	if t, _ := a.String("key-type", ""); t == "s3" {
		return op.KeyTypeS3
	}
	return op.KeyTypeSwift
}

// createSubuser is RGWOp_Subuser_Create::execute
// (driver/rados/rgw_rest_user.cc:495-554 at v19.2.6, :505-564 at v20.2.4).
// The body is dump_subusers_info's (driver/rados/rgw_user.cc:2519-2525).
func createSubuser(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewCreateSubuser()
	o.UID = uidArg(a)
	o.Subuser, _ = a.String("subuser", "")
	o.Perm, _ = a.String("access", "")
	o.Key.AccessKey, _ = a.String("access-key", "")
	o.Key.SecretKey, _ = a.String("secret-key", "")
	o.Key.Type = subuserKeyType(a)
	fl := &flags{a: a}
	o.Key.GenSecret = fl.get("generate-secret", false)
	o.Key.GenAccess = fl.get("gen-access-key", false)
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpSubusers(f, o.Result) })
}

// modifySubuser is RGWOp_Subuser_Modify::execute
// (driver/rados/rgw_rest_user.cc:570-621 at v19.2.6, :580-631 at v20.2.4),
// which sets the permission it reads whether or not access is named. The
// body is dump_subusers_info's (driver/rados/rgw_user.cc:2552-2558).
func modifySubuser(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewModifySubuser()
	o.UID = uidArg(a)
	o.Subuser, _ = a.String("subuser", "")
	perm, _ := a.String("access", "")
	o.Perm = &perm
	o.Key.SecretKey, _ = a.String("secret-key", "")
	o.Key.Type = subuserKeyType(a)
	fl := &flags{a: a}
	o.Key.GenSecret = fl.get("generate-secret", false)
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpSubusers(f, o.Result) })
}

// removeSubuser is RGWOp_Subuser_Remove::execute
// (driver/rados/rgw_rest_user.cc:637-664 at v19.2.6, :647-674 at v20.2.4),
// which sends no body.
func removeSubuser(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewRemoveSubuser()
	o.UID = uidArg(a)
	o.Subuser, _ = a.String("subuser", "")
	fl := &flags{a: a}
	o.PurgeKeys = fl.get("purge-keys", true)
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// addCaps is RGWOp_Caps_Add::execute (driver/rados/rgw_rest_user.cc:789-811
// at v19.2.6, :799-821 at v20.2.4). The body is the user's caps,
// RGWUserCaps::dump (driver/rados/rgw_user.cc:2675-2678).
func addCaps(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewAddCaps()
	o.UID = uidArg(q.Args)
	o.Caps, _ = q.Args.String("user-caps", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) { o.Result.DumpAs(f, "caps") })
}

// removeCaps is RGWOp_Caps_Remove::execute (driver/rados/rgw_rest_user.cc:827-849
// at v19.2.6, :837-859 at v20.2.4). The body is the user's caps
// (driver/rados/rgw_user.cc:2710-2713).
func removeCaps(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewRemoveCaps()
	o.UID = uidArg(q.Args)
	o.Caps, _ = q.Args.String("user-caps", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	return WriteBody(w, r, q, func(f formatter.Formatter) { o.Result.DumpAs(f, "caps") })
}

// getQuotaInfo is RGWOp_Quota_Info::execute (driver/rados/rgw_rest_user.cc:886-941
// at v19.2.6, :896-951 at v20.2.4).
func getQuotaInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewGetUserQuota()
	o.UID = uidArg(q.Args)
	o.QuotaType, _ = q.Args.String("quota-type", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpQuotaInfo(f, o.Result, o.QuotaType, rel) })
}

// dumpQuotaInfo is RGWOp_Quota_Info::execute's body
// (driver/rados/rgw_rest_user.cc:930-938 at v19.2.6): for no quota type,
// UserQuotas::dump's bucket_quota then user_quota in a "quota" section
// (:861-864), otherwise the one quota under its own name.
func dumpQuotaInfo(f formatter.Formatter, info meta.UserInfo, quotaType string, rel denc.Release) {
	switch quotaType {
	case "":
		f.OpenObjectSection("quota")
		dumpQuota(f, "bucket_quota", info.BucketQuota, rel)
		dumpQuota(f, "user_quota", info.UserQuota, rel)
		f.CloseSection()
	case "user":
		dumpQuota(f, "user_quota", info.UserQuota, rel)
	default:
		dumpQuota(f, "bucket_quota", info.BucketQuota, rel)
	}
}

// dumpQuota is encode_json(name, RGWQuotaInfo).
func dumpQuota(f formatter.Formatter, name string, q meta.Quota, rel denc.Release) {
	f.OpenObjectSection(name)
	q.Dump(f, rel)
	f.CloseSection()
}

// setQuotaInfo is RGWOp_Quota_Set::execute (driver/rados/rgw_rest_user.cc:1005-1127
// at v19.2.6, :1015-1137 at v20.2.4), which sends no body. radosgw's argument
// readers are unchecked: stringtoll writes nothing for a value it refuses,
// leaving max-objects and max-size at RGWQuotaInfo's -1, no limit, and
// max-size-kb uninitialized; get_bool leaves enabled at the current value
// (docs/ceph-upstream-bugs.md, "radosgw's quota set stores garbage for an
// unparsable max-size-kb"). rgw-go answers any of them it cannot parse with
// ErrInvalidArgument after the permission check, and stores nothing.
func setQuotaInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewSetUserQuota()
	o.UID = uidArg(a)
	o.QuotaType, _ = a.String("quota-type", "")
	fl := &flags{a: a}
	o.Params.MaxObjects = quotaInt(a, fl, "max-objects")
	o.Params.MaxSize = quotaInt(a, fl, "max-size")
	o.Params.MaxSizeKB = quotaInt(a, fl, "max-size-kb")
	o.Params.Enabled = fl.ptr("enabled")
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// quotaInt is RESTArgs::get_int64 as RGWOp_Quota_Set calls it: nil when
// absent; a value stringtoll refuses is recorded in fl.
func quotaInt(a Args, fl *flags, name string) *int64 {
	v, ok, err := a.Int64(name, 0)
	switch {
	case !ok:
		return nil
	case err != nil:
		if fl.err == nil {
			fl.err = err
		}
		return nil
	}
	return &v
}
