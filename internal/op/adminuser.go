package op

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// KeyType is RGWUserAdminOpState's key_type (rgw_user.h at v19.2.6 and
// v20.2.4): KEY_TYPE_SWIFT, KEY_TYPE_S3 or KEY_TYPE_UNDEFINED.
type KeyType int8

// The key types.
const (
	KeyTypeSwift     KeyType = 0
	KeyTypeS3        KeyType = 1
	KeyTypeUndefined KeyType = -1
)

// ParseKeyType is the key-type argument as the RGWOp_User_* bodies map it
// (driver/rados/rgw_rest_user.cc:225-233 at v19.2.6): "swift", "s3", and
// anything else undefined.
func ParseKeyType(s string) KeyType {
	switch s {
	case "swift":
		return KeyTypeSwift
	case "s3":
		return KeyTypeS3
	}
	return KeyTypeUndefined
}

// UserKeyParams is the key part of RGWUserAdminOpState
// (driver/rados/rgw_user.h:212-230 and :340-356 at v19.2.6). GenerateKey is
// set_generate_key, which generates whichever of AccessKey and SecretKey is
// empty. An undefined Type is Swift when a subuser is named and S3
// otherwise (RGWAccessKeyPool::check_op, driver/rados/rgw_user.cc:474-483).
type UserKeyParams struct {
	AccessKey, SecretKey string
	Type                 KeyType
	GenerateKey          bool
	Subuser              string
	// Active is the key's active flag when the request names one.
	Active *bool
}

// hasKeyOp is RGWUserAdminOpState::has_key_op: set_access_key,
// set_secret_key and set_generate_key each set key_op.
func (p UserKeyParams) hasKeyOp() bool {
	return p.AccessKey != "" || p.SecretKey != "" || p.GenerateKey
}

// genAccess and genSecret are will_gen_access and will_gen_secret: the
// request asked for a key and named no access key or no secret.
func (p UserKeyParams) genAccess() bool { return p.GenerateKey && p.AccessKey == "" }
func (p UserKeyParams) genSecret() bool { return p.GenerateKey && p.SecretKey == "" }

// anonymousUser is rgw_user(RGW_USER_ANON_ID).
var anonymousUser = meta.UserID{ID: AnonymousUserID}

// opUserID is the uid RGWUserAdminOpState holds once set_user_id(uid) ran:
// set_user_id ignores an id rgw_user::empty() calls empty, leaving the
// anonymous user the op state starts with (driver/rados/rgw_user.cc:268-283).
func opUserID(uid meta.UserID) meta.UserID {
	if uid.ID == "" {
		return anonymousUser
	}
	return uid
}

// userLookup is what RGWUser::init found and through which index.
type userLookup struct {
	rec                                  *UserRecord
	foundByUID, foundByEmail, foundByKey bool
}

// lookupUser is RGWUser::init's search (driver/rados/rgw_user.cc:1389-1440
// at v19.2.6): by uid unless it is the anonymous user; then, when none was
// found, by email if rgw_user_unique_email is set; then by access key. A
// Swift key is looked up through no index here: init's get_user_by_swift
// finds the key's user for the subuser and key ops, which keep Swift keys.
// radosgw treats every failed read as "not found"; a failure other than a
// missing user fails the lookup here, as its S3 error alone (storeErr).
func lookupUser(ctx context.Context, env *Env, uid meta.UserID, email, accessKey string, swift bool) (userLookup, error) {
	var l userLookup
	if uid.ID != "" && uid != anonymousUser {
		rec, err := env.Users.GetUser(ctx, uid)
		switch {
		case err == nil:
			l.rec, l.foundByUID = rec, true
			return l, nil
		case !errors.Is(err, ErrNoSuchUser):
			return l, storeErr(err)
		}
	}
	if email != "" && confFlag(env, "rgw_user_unique_email", true) {
		rec, err := env.Users.GetUserByEmail(ctx, email)
		switch {
		case err == nil:
			l.rec, l.foundByEmail = rec, true
			return l, nil
		case !errors.Is(err, ErrNoSuchUser):
			return l, storeErr(err)
		}
	}
	if accessKey != "" && !swift {
		rec, err := env.Users.GetUserByAccessKey(ctx, accessKey)
		switch {
		case err == nil:
			l.rec, l.foundByKey = rec, true
		case !errors.Is(err, ErrNoSuchUser):
			return l, storeErr(err)
		}
	}
	return l, nil
}

// storeErr is a user store's failure as its S3 error alone. A store's error
// text can name the object it failed on, and the user stores name their
// index objects by the email or the access key id, so the chain is dropped
// before it can reach WriteError's log.
func storeErr(err error) error { return AsError(err) }

// confFlag reads a boolean option, def when it cannot be read.
func confFlag(env *Env, name string, def bool) bool {
	if env.Conf == nil {
		return def
	}
	v, err := env.Conf.Bool(name)
	if err != nil {
		return def
	}
	return v
}

// confInt reads an integer option, def when it cannot be read.
func confInt(env *Env, name string, def int64) int64 {
	if env.Conf == nil {
		return def
	}
	v, err := env.Conf.Int64(name)
	if err != nil {
		return def
	}
	return v
}

// DefaultMaxBuckets is rgw_user_max_buckets, the max-buckets a created user
// gets when the request names none (RGW_DEFAULT_MAX_BUCKETS by default).
func DefaultMaxBuckets(env *Env) int32 {
	return int32(confInt(env, "rgw_user_max_buckets", meta.DefaultMaxBuckets)) //nolint:gosec // radosgw narrows the int64 option to int32 too
}

// defaultQuota is rgw_apply_default_bucket_quota and
// rgw_apply_default_user_quota (rgw_quota.cc:998-1020 at v19.2.6): the
// rgw_<kind>_default_quota_max_objects and _max_size options, each set and
// enabling the quota when it is not negative.
func defaultQuota(env *Env, kind string) meta.Quota {
	q := meta.Quota{MaxSize: -1, MaxObjects: -1}
	if v := confInt(env, "rgw_"+kind+"_default_quota_max_objects", -1); v >= 0 {
		q.MaxObjects, q.Enabled = v, true
	}
	if v := confInt(env, "rgw_"+kind+"_default_quota_max_size", -1); v >= 0 {
		q.MaxSize, q.Enabled = v, true
	}
	return q
}

// listChunk is rgw_list_buckets_max_chunk, the page the user ops list a
// user's buckets in.
func listChunk(env *Env) int {
	return int(max(confInt(env, "rgw_list_buckets_max_chunk", 1000), 1))
}

// validTenant is rgw_validate_tenant_name (rgw_user.cc:91-101 at v19.2.6):
// every byte alphanumeric in the C locale, or '_'.
func validTenant(t string) bool {
	for _, c := range []byte(t) {
		if !isAlnum(c) && c != '_' {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool { return isCAlpha(c) || isCDigit(c) }

// maxIAMUserName is MAX_USER_NAME_LEN (rgw_rest_iam.cc:170 at v19.2.6, :173
// at v20.2.4).
const maxIAMUserName = 64

// validIAMUserName is validate_iam_user_name (rgw_rest_iam.cc:172-188 at
// v19.2.6): non-empty, at most 64 bytes, and matching [\w+=,.@-]+, where
// std::regex's \w is [A-Za-z0-9_].
func validIAMUserName(name string) bool {
	if name == "" || len(name) > maxIAMUserName {
		return false
	}
	for _, c := range []byte(name) {
		switch {
		case isAlnum(c):
		case c == '_' || c == '+' || c == '=' || c == ',' || c == '.' || c == '@' || c == '-':
		default:
			return false
		}
	}
	return true
}

// requesterIsSystem is s->user->get_info().system, which the create and
// modify bodies require before a request may set the system flag.
func requesterIsSystem(r *Request) bool {
	return r.Identity.User != nil && r.Identity.User.System != 0
}

// checkPlacement is driver->valid_placement over the default-placement
// argument: a rule the zone has no placement or storage class for is
// EINVAL (rgw_rest_user.cc:253-261 at v19.2.6).
func checkPlacement(env *Env, rule *meta.PlacementRule) error {
	if rule == nil {
		return nil
	}
	if _, err := env.Zone.Placement(*rule); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

// accountLink is users_entry, what decides whether a write links or
// unlinks the user in its account's users index (svc_user_rados.cc:157-171
// at v19.2.6): the account, the path and the display name.
type accountLink struct{ account, path, name string }

func linkOf(info *meta.UserInfo) accountLink {
	if info == nil || info.AccountID == "" {
		return accountLink{}
	}
	return accountLink{info.AccountID, info.Path, info.DisplayName}
}

// accountNameTaken is PutOperation::prepare's check before a user is
// linked under a display name (svc_user_rados.cc:272-290 at v19.2.6): some
// other user of the account holds the name, compared as cls_user keys it,
// lowercased. The AccountStore names no user by display name, so the
// account's users are read one by one.
func accountNameTaken(ctx context.Context, env *Env, info *meta.UserInfo) (bool, error) {
	marker := ""
	for {
		ids, next, err := env.Accounts.ListAccountUsers(ctx, info.AccountID, marker, 1000)
		if err != nil {
			return false, storeErr(err)
		}
		for _, id := range ids {
			if id == info.UserID.ID {
				continue
			}
			rec, err := env.Users.GetUser(ctx, meta.UserID{Tenant: info.UserID.Tenant, ID: id})
			if errors.Is(err, ErrNoSuchUser) {
				continue
			}
			if err != nil {
				return false, storeErr(err)
			}
			if foldEqual(rec.Info.DisplayName, info.DisplayName) {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		marker = next
	}
}

// foldEqual reports whether a and b are equal once each is lowercased as
// boost::to_lower does in the C locale, the form cls_user keys names by.
func foldEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

// writeUser stores rec.Info over old as RGWUser::update's store_user does,
// non-exclusively whether it creates or modifies (driver/rados/rgw_user.cc:1500
// at v19.2.6, :1505 at v20.2.4), so the uid object and every index object
// are overwritten as radosgw overwrites them, and a modify is checked
// against the version it read. The account users index is kept as
// PutOperation keeps it: a display name another user of the account holds
// refuses the write with exists, radosgw's EEXIST as the op translates it;
// the old entry goes and the new one comes when the account, path or name
// changed (svc_user_rados.cc:272-290, :352-375 and :444-455 at v19.2.6).
// Nothing is undone on a failure: a create that fails after the uid object
// is written leaves the user as radosgw leaves it. Every error is
// storeErr's.
func writeUser(ctx context.Context, env *Env, rec *UserRecord, old *meta.UserInfo, exists *Error) error {
	newLink, oldLink := linkOf(&rec.Info), linkOf(old)
	relink := newLink != (accountLink{}) && newLink != oldLink
	if relink {
		taken, err := accountNameTaken(ctx, env, &rec.Info)
		if err != nil {
			return err
		}
		if taken {
			return exists
		}
	}
	var opts PutUserOptions
	if old != nil {
		opts.IfVersion = &rec.Version
	}
	err := env.Users.PutUser(ctx, rec, opts)
	if err == nil && oldLink != (accountLink{}) && oldLink != newLink {
		if err = env.Accounts.RemoveAccountUser(ctx, old.AccountID, old.DisplayName); errors.Is(err, ErrNoSuchKey) {
			err = nil
		}
	}
	if err == nil && relink {
		err = env.Accounts.AddAccountUser(ctx, rec.Info.AccountID, rec.Info)
	}
	if err != nil {
		return storeErr(err)
	}
	return nil
}

// CreateUser is RGWOp_User_Create and RGWUserAdminOp_User::create
// (driver/rados/rgw_rest_user.cc:133-285, driver/rados/rgw_user.cc:1744-1918
// and :2410-2442 at v19.2.6). Cap users=write.
type CreateUser struct {
	AdminOp
	// UID carries the tenant argument already, as the handler applies it.
	UID         meta.UserID
	DisplayName string
	Email       string
	Key         UserKeyParams
	// Caps is the user-caps argument, ';'-separated.
	Caps      string
	Suspended *bool
	// MaxBuckets is nil for DefaultMaxBuckets; the REST body sets it only
	// when it differs from that, a negative value as -1.
	MaxBuckets  *int32
	System      *bool
	AccountRoot *bool
	// UserOpMask is the op-mask argument; the field cannot be OpMask, the
	// Op method.
	UserOpMask       *uint32
	DefaultPlacement *meta.PlacementRule
	// PlacementTags is nil when the request names none.
	PlacementTags []string
	AccountID     string
	Path          string
	Result        meta.UserInfo
}

// NewCreateUser returns a CreateUser with no key type named.
func NewCreateUser() *CreateUser { return &CreateUser{Key: UserKeyParams{Type: KeyTypeUndefined}} }

// Name is create_user.
func (o *CreateUser) Name() string { return "create_user" }

// VerifyPermission is RGWOp_User_Create::check_caps.
func (o *CreateUser) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute runs the body's own refusals, then RGWUser::init, add's
// user_add_helper and check_op, and execute_add. radosgw sends no message
// with these errors, as create passes add no message sink.
func (o *CreateUser) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	if o.System != nil && *o.System && !requesterIsSystem(r) {
		return ErrInvalidArgument
	}
	if err := checkPlacement(env, o.DefaultPlacement); err != nil {
		return err
	}
	uid := opUserID(o.UID)
	l, err := lookupUser(ctx, env, uid, o.Email, o.Key.AccessKey, o.Key.Type == KeyTypeSwift)
	if err != nil {
		return err
	}
	switch {
	case l.foundByEmail:
		return ErrEmailExists
	case l.foundByKey:
		return ErrKeyExists
	case l.rec != nil:
		return ErrUserAlreadyExists
	}
	if o.DisplayName == "" || uid == anonymousUser {
		return ErrInvalidArgument
	}
	if !validTenant(uid.Tenant) {
		return ErrInvalidTenantName
	}
	if meta.ValidAccountID(uid.ID) || meta.ValidAccountID(uid.Tenant) {
		return ErrInvalidArgument
	}

	info := meta.NewUserInfo()
	info.UserID = uid
	info.DisplayName = o.DisplayName
	info.Type = meta.IdentityRGW
	info.Email = o.Email
	info.MaxBuckets = DefaultMaxBuckets(env)
	if o.MaxBuckets != nil {
		info.MaxBuckets = *o.MaxBuckets
	}
	if o.Suspended != nil && *o.Suspended {
		info.Suspended = 1
	}
	if o.System != nil && *o.System {
		info.System = 1
	}
	if o.UserOpMask != nil {
		info.OpMask = *o.UserOpMask
	}
	info.BucketQuota = defaultQuota(env, "bucket")
	info.UserQuota = defaultQuota(env, "user")
	if o.DefaultPlacement != nil {
		info.DefaultPlacement = *o.DefaultPlacement
	}
	if o.PlacementTags != nil {
		info.PlacementTags = o.PlacementTags
	}
	if o.AccountID != "" {
		if err := joinAccount(ctx, env, &info, o.AccountID, ErrNoSuchKey); err != nil {
			return err
		}
	}
	if o.AccountRoot != nil && *o.AccountRoot {
		if info.AccountID == "" {
			return ErrInvalidArgument
		}
		info.Type = meta.IdentityRoot
	}
	if info.AccountID != "" && !validIAMUserName(info.DisplayName) {
		return ErrInvalidArgument
	}
	if o.Path != "" {
		info.Path = o.Path
	}
	info.CreateDate = meta.Time{Time: env.Clock()}

	if o.Key.hasKeyOp() {
		key := o.Key
		if key.Type == KeyTypeUndefined {
			key.Type = KeyTypeS3 // RGWUser::check_op's type "set by context"
		}
		if err := addKey(ctx, env, &info, key); err != nil {
			return err
		}
	}
	if o.Caps != "" {
		if err := info.Caps.AddString(o.Caps); err != nil {
			return ErrInvalidCapability
		}
	}
	rec := &UserRecord{Info: info}
	if err := writeUser(ctx, env, rec, nil, ErrUserAlreadyExists); err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// joinAccount is execute_add's and execute_modify's account step: a valid
// id (rgw::account::validate_id) of an account in the user's tenant
// (validate_account_tenant, driver/rados/rgw_user.cc:1658-1679 at v19.2.6).
// A missing account is radosgw's -ENOENT, which create answers as the
// NoSuchKey row and modify maps to NoSuchUser; missing is that answer.
func joinAccount(ctx context.Context, env *Env, info *meta.UserInfo, accountID string, missing *Error) error {
	if !meta.ValidAccountID(accountID) {
		return ErrInvalidArgument
	}
	acct, err := env.Accounts.GetAccount(ctx, accountID)
	if errors.Is(err, ErrNoSuchEntity) {
		return missing
	}
	if err != nil {
		return err
	}
	if acct.Info.Tenant != info.UserID.Tenant {
		return ErrInvalidArgument
	}
	info.AccountID = accountID
	return nil
}

// ModifyUser is RGWOp_User_Modify and RGWUserAdminOp_User::modify
// (driver/rados/rgw_rest_user.cc:287-438, driver/rados/rgw_user.cc:2017-2228
// and :2444-2475 at v19.2.6). Cap users=write.
type ModifyUser struct {
	AdminOp
	UID         meta.UserID
	DisplayName string
	// Email is nil when the request names none; "" clears the email.
	Email       *string
	Key         UserKeyParams
	Suspended   *bool
	MaxBuckets  *int32
	System      *bool
	AccountRoot *bool
	// UserOpMask is the op-mask argument; the field cannot be OpMask, the
	// Op method.
	UserOpMask       *uint32
	DefaultPlacement *meta.PlacementRule
	PlacementTags    []string
	AccountID        string
	Path             string
	// UserQuota and BucketQuota are set_quota_info's.
	UserQuota   *meta.Quota
	BucketQuota *meta.Quota
	Result      meta.UserInfo
}

// NewModifyUser returns a ModifyUser with no key type named.
func NewModifyUser() *ModifyUser { return &ModifyUser{Key: UserKeyParams{Type: KeyTypeUndefined}} }

// Name is modify_user.
func (o *ModifyUser) Name() string { return "modify_user" }

// VerifyPermission is RGWOp_User_Modify::check_caps.
func (o *ModifyUser) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute runs the body's own refusals, then RGWUser::init, check_op and
// execute_modify; a missing user is NoSuchUser, as modify maps -ENOENT.
func (o *ModifyUser) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	if o.System != nil && *o.System && !requesterIsSystem(r) {
		return ErrInvalidArgument
	}
	if err := checkPlacement(env, o.DefaultPlacement); err != nil {
		return err
	}
	uid := opUserID(o.UID)
	email := ""
	if o.Email != nil {
		email = *o.Email
	}
	l, err := lookupUser(ctx, env, uid, email, o.Key.AccessKey, o.Key.Type == KeyTypeSwift)
	if err != nil {
		return err
	}
	if err := checkFound(uid, l); err != nil {
		return err
	}
	if l.rec == nil {
		return ErrNoSuchUser
	}
	rec := l.rec
	old := rec.Info
	info := rec.Info

	if email != "" {
		if old.Email != email {
			dup, err := env.Users.GetUserByEmail(ctx, email)
			switch {
			case err == nil && dup.Info.UserID != info.UserID:
				return ErrEmailExists
			case err != nil && !errors.Is(err, ErrNoSuchUser):
				return AsError(err)
			}
		}
		info.Email = email
	} else if o.Email != nil {
		info.Email = ""
	}
	if o.DisplayName != "" {
		info.DisplayName = o.DisplayName
	}
	if o.MaxBuckets != nil {
		info.MaxBuckets = *o.MaxBuckets
	}
	if o.System != nil {
		info.System = boolU8(*o.System)
	}
	if o.UserOpMask != nil {
		info.OpMask = *o.UserOpMask
	}
	if o.BucketQuota != nil {
		info.BucketQuota = *o.BucketQuota
	}
	if o.UserQuota != nil {
		info.UserQuota = *o.UserQuota
	}
	if o.Suspended != nil {
		info.Suspended = boolU8(*o.Suspended)
		if err := suspendBuckets(ctx, env, info.UserID, *o.Suspended); err != nil {
			return err
		}
	}
	if o.DefaultPlacement != nil {
		info.DefaultPlacement = *o.DefaultPlacement
	}
	if o.PlacementTags != nil {
		info.PlacementTags = o.PlacementTags
	}
	if o.AccountID != "" {
		if !meta.ValidAccountID(o.AccountID) {
			return ErrInvalidArgument
		}
		if info.AccountID != o.AccountID {
			if info.AccountID != "" {
				return ErrInvalidArgument // radosgw: users cannot be moved out of their account
			}
			if err := joinAccount(ctx, env, &info, o.AccountID, ErrNoSuchUser); err != nil {
				return err
			}
			if err := adoptBuckets(ctx, env, info.UserID, info.AccountID); err != nil {
				return err
			}
		}
	}
	if o.AccountRoot != nil {
		if *o.AccountRoot && info.AccountID == "" {
			return ErrInvalidArgument
		}
		info.Type = meta.IdentityRGW
		if *o.AccountRoot {
			info.Type = meta.IdentityRoot
		}
	}
	if info.AccountID != "" && !validIAMUserName(info.DisplayName) {
		return ErrInvalidArgument
	}
	if o.Path != "" {
		info.Path = o.Path
	}
	if o.Key.hasKeyOp() {
		key := o.Key
		if key.Type == KeyTypeUndefined {
			key.Type = KeyTypeS3
		}
		if err := addKey(ctx, env, &info, key); err != nil {
			return err
		}
	}
	rec.Info = info
	if err := writeUser(ctx, env, rec, &old, ErrBucketAlreadyExists); err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// checkFound is RGWUser::check_op past what init found (driver/rados/rgw_user.cc:1515-1546
// at v19.2.6): the op state's uid, which init replaced with the user it
// found, must not be the anonymous user and must be the uid asked for, so
// a user found only through another user's email or access key is EINVAL;
// its tenant must be a valid tenant name.
func checkFound(uid meta.UserID, l userLookup) error {
	opUID := uid
	if l.rec != nil {
		opUID = l.rec.Info.UserID
	}
	if opUID == anonymousUser || opUID != uid {
		return ErrInvalidArgument
	}
	if !validTenant(opUID.Tenant) {
		return ErrInvalidTenantName
	}
	return nil
}

func boolU8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// eachUserBucket pages the buckets uid's own list holds,
// rgw_list_buckets_max_chunk at a time, and calls fn with each page, as the
// user ops' list_buckets loops do.
func eachUserBucket(ctx context.Context, env *Env, uid meta.UserID, fn func([]meta.BucketEnt) error) error {
	marker := ""
	for {
		ents, next, more, err := env.Users.ListUserBuckets(ctx, meta.UserOwner(uid), marker, listChunk(env))
		if err != nil {
			return err
		}
		if len(ents) == 0 {
			return nil
		}
		if err := fn(ents); err != nil {
			return err
		}
		if !more || next == "" {
			return nil
		}
		marker = next
	}
}

// suspendBuckets is the suspension step of execute_modify over
// RGWRados::set_buckets_enabled (driver/rados/rgw_rados.cc:5332-5368 at
// v19.2.6): every bucket of the page gets BUCKET_SUSPENDED set or cleared,
// a bucket that fails is logged and skipped, and a page with a failure
// ends the listing with the last failure.
func suspendBuckets(ctx context.Context, env *Env, uid meta.UserID, suspended bool) error {
	return eachUserBucket(ctx, env, uid, func(ents []meta.BucketEnt) error {
		var last error
		for i := range ents {
			ent := &ents[i]
			rec, err := env.Buckets.GetBucket(ctx, ent.Bucket.Tenant, ent.Bucket.Name)
			if err == nil {
				if suspended {
					rec.Info.Flags |= meta.BucketSuspended
				} else {
					rec.Info.Flags &^= meta.BucketSuspended
				}
				err = env.Buckets.PutBucketInfo(ctx, rec)
			}
			if err != nil {
				slog.WarnContext(ctx, "could not change a bucket's suspension, skipping it",
					slog.String("bucket", ent.Bucket.Name), slog.Any("error", err))
				last = err
			}
		}
		return last
	})
}

// adoptBuckets is adopt_user_buckets (driver/rados/rgw_user.cc:1714-1742 at
// v19.2.6): every bucket of uid's own list chowned to the account under its
// name, a bucket gone meanwhile skipped. ChownBucket retries a lost race as
// adopt_user_bucket does.
func adoptBuckets(ctx context.Context, env *Env, uid meta.UserID, accountID string) error {
	acct, err := env.Accounts.GetAccount(ctx, accountID)
	if err != nil {
		return err
	}
	owner := meta.AccountOwner(accountID)
	return eachUserBucket(ctx, env, uid, func(ents []meta.BucketEnt) error {
		for i := range ents {
			ent := &ents[i]
			rec, err := env.Buckets.GetBucket(ctx, ent.Bucket.Tenant, ent.Bucket.Name)
			if err == nil {
				err = env.BucketAdmin.ChownBucket(ctx, rec, owner, acct.Info.Name)
			}
			if err != nil && !errors.Is(err, ErrNoSuchBucket) {
				return err
			}
		}
		return nil
	})
}

// GetUserInfo is RGWOp_User_Info and RGWUserAdminOp_User::info
// (driver/rados/rgw_rest_user.cc:72-131, driver/rados/rgw_user.cc:2347-2408
// at v19.2.6). Cap user-info-without-keys=read or users=read.
type GetUserInfo struct {
	AdminOp
	// UID or AccessKey names the user.
	UID        meta.UserID
	AccessKey  string
	FetchStats bool
	SyncStats  bool
	Result     meta.UserInfo
	// Stats is set when FetchStats.
	Stats *Stats
	// DumpKeys says the document may show the user's keys: the requester
	// holds users=read, or is a system request or an admin
	// (rgw_rest_user.cc:121-128 at v19.2.6; Squid's is_admin_of is
	// admin || system, rgw_auth.cc:1048-1051, Tentacle's is_admin the same).
	DumpKeys bool
}

// NewGetUserInfo returns a GetUserInfo.
func NewGetUserInfo() *GetUserInfo { return &GetUserInfo{} }

// Name is get_user_info.
func (o *GetUserInfo) Name() string { return "get_user_info" }

// VerifyPermission is RGWOp_User_Info::check_caps: either cap admits.
func (o *GetUserInfo) VerifyPermission(_ context.Context, r *Request) error {
	if CheckCaps(r, "user-info-without-keys", meta.CapRead) == nil {
		return nil
	}
	return CheckCaps(r, "users", meta.CapRead)
}

// Execute is the body's check that a uid or an access key is named, then
// RGWUserAdminOp_User::info.
func (o *GetUserInfo) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	if o.UID == (meta.UserID{}) && o.AccessKey == "" {
		return ErrInvalidArgument
	}
	uid := opUserID(o.UID)
	l, err := lookupUser(ctx, env, uid, "", o.AccessKey, false)
	if err != nil {
		return err
	}
	switch {
	case l.rec == nil && uid == anonymousUser:
		// init_members' keys.init refuses the anonymous op uid with EINVAL
		// before info asks whether a user was found (rgw_user.cc:246-264
		// and :1454-1456 at v19.2.6, :252-270 and :1458-1460 at v20.2.4).
		return ErrInvalidArgument
	case l.rec == nil:
		return ErrNoSuchUser
	}
	info := l.rec.Info
	owner := meta.UserOwner(info.UserID)
	if info.AccountID != "" {
		owner = meta.AccountOwner(info.AccountID)
	}
	if o.SyncStats {
		if err := env.BucketAdmin.SyncOwnerStats(ctx, owner); err != nil {
			return err
		}
	}
	if o.FetchStats {
		st, err := env.Stats.UserStats(ctx, owner)
		if err != nil && !errors.Is(err, ErrNoSuchKey) && !errors.Is(err, ErrNotFound) {
			return err
		}
		o.Stats = &st
	}
	o.DumpKeys = CheckCaps(r, "users", meta.CapRead) == nil || r.Identity.System || r.Identity.Admin
	o.Result = info
	return nil
}

// RemoveUser is RGWOp_User_Remove and RGWUserAdminOp_User::remove
// (driver/rados/rgw_rest_user.cc:440-479, driver/rados/rgw_user.cc:1940-2015
// and :2477-2492 at v19.2.6). Cap users=write.
type RemoveUser struct {
	AdminOp
	UID       meta.UserID
	PurgeData bool
}

// NewRemoveUser returns a RemoveUser.
func NewRemoveUser() *RemoveUser { return &RemoveUser{} }

// Name is remove_user.
func (o *RemoveUser) Name() string { return "remove_user" }

// VerifyPermission is RGWOp_User_Remove::check_caps.
func (o *RemoveUser) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init by uid, check_op and execute_remove: the
// buckets of the user's own list refuse the removal with radosgw's EEXIST,
// the 409 BucketAlreadyExists, unless PurgeData, which removes each with
// its objects first. A bucket the list names that another owner now holds
// is left alone. The account users index loses the user's entry, then the
// user goes. Each step is done again or found done by a retry: an entry
// already gone from the account users index, which radosgw's
// remove_user_info refuses (svc_user_rados.cc:601-610 at v19.2.6, :575-584
// at v20.2.4), is taken as removed, so a removal that failed later
// completes.
func (o *RemoveUser) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	uid := opUserID(o.UID)
	l, err := lookupUser(ctx, env, uid, "", "", false)
	if err != nil {
		return err
	}
	if err := checkFound(uid, l); err != nil {
		return err
	}
	if l.rec == nil {
		return ErrNoSuchUser
	}
	rec := l.rec
	if err := eachUserBucket(ctx, env, rec.Info.UserID, o.removeBuckets(ctx, env, rec.Info.UserID)); err != nil {
		return err
	}
	if rec.Info.AccountID != "" {
		err := env.Accounts.RemoveAccountUser(ctx, rec.Info.AccountID, rec.Info.DisplayName)
		if err != nil && !errors.Is(err, ErrNoSuchKey) {
			return storeErr(err)
		}
	}
	if err := env.Users.RemoveUser(ctx, rec); err != nil {
		return storeErr(err)
	}
	return nil
}

// removeBuckets is execute_remove's handling of one page of uid's buckets.
// A listed bucket that is gone is radosgw's ENOENT from load_bucket or
// remove, which RGWUserAdminOp_User::remove answers as NoSuchUser
// (driver/rados/rgw_user.cc:1968-1981 and :2488-2492 at v19.2.6); nothing
// of the user is removed yet, so a retry, listing again, goes on.
func (o *RemoveUser) removeBuckets(ctx context.Context, env *Env, uid meta.UserID) func([]meta.BucketEnt) error {
	return func(ents []meta.BucketEnt) error {
		if !o.PurgeData {
			return ErrBucketAlreadyExists
		}
		for i := range ents {
			ent := &ents[i]
			b, err := env.Buckets.GetBucket(ctx, ent.Bucket.Tenant, ent.Bucket.Name)
			if errors.Is(err, ErrNoSuchBucket) {
				return ErrNoSuchUser
			}
			if err != nil {
				return err
			}
			if b.Info.Owner.User == nil || *b.Info.Owner.User != uid {
				slog.WarnContext(ctx, "the user's bucket list names a bucket another owner holds, leaving it",
					slog.String("bucket", ent.Bucket.Name))
				continue
			}
			if err := DeleteBucketWithChildren(ctx, env, b, true); errors.Is(err, ErrNoSuchBucket) {
				return ErrNoSuchUser
			} else if err != nil {
				return err
			}
		}
		return nil
	}
}

// maxListUsers is RGWUser::list's cap on max-entries.
const maxListUsers = 1000

// ListUsers is RGWOp_User_List and RGWUser::list
// (driver/rados/rgw_rest_user.cc:44-70, driver/rados/rgw_user.cc:2276-2329
// at v19.2.6): the keys of the "user" metadata section from Marker. Cap
// users=read.
type ListUsers struct {
	AdminOp
	Marker string
	// MaxEntries is 1000 by default and at most 1000.
	MaxEntries uint32
	Keys       []string
	Truncated  bool
	Count      uint64
	// NextMarker is the metadata lister's marker when Truncated, raw.
	NextMarker string
}

// NewListUsers returns a ListUsers for max-entries' default.
func NewListUsers() *ListUsers { return &ListUsers{MaxEntries: maxListUsers} }

// Name is list_user.
func (o *ListUsers) Name() string { return "list_user" }

// VerifyPermission is RGWOp_User_List::check_caps.
func (o *ListUsers) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapRead)
}

// Execute lists while the lister reports more and the request has room,
// the room measured before each call, as RGWUser::list's loop does.
func (o *ListUsers) Execute(ctx context.Context, r *Request) error {
	limit := min(o.MaxEntries, maxListUsers)
	marker := o.Marker
	o.Keys = nil
	for {
		left := limit - uint32(len(o.Keys)) //nolint:gosec // at most maxListUsers keys are kept
		keys, next, more, err := r.Env.Metadata.List(ctx, "user", marker, int(left))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		o.Keys = append(o.Keys, keys...)
		o.Truncated = more
		marker = next
		if !more || left == 0 {
			break
		}
	}
	o.Count = uint64(len(o.Keys))
	o.NextMarker = ""
	if o.Truncated {
		o.NextMarker = marker
	}
	return nil
}
