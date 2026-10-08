package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The account ops are rgw::account's create, modify, remove and info
// (rgw_account.cc) behind rgw_rest_account.cc's RGWOp_Account_*. A bare line
// number is rgw_account.cc's at v19.2.6; each fact cited holds at v20.2.4,
// whose differences are named.

// AccountParams is rgw::account::AdminOpState (rgw_account.h:47-62 at
// v20.2.4): each pointer is nil when the request does not name it. The quota
// fields exist on Squid too, but nothing sets them there.
type AccountParams struct {
	ID, Tenant, Name, Email                                  string
	MaxUsers, MaxRoles, MaxGroups, MaxAccessKeys, MaxBuckets *int32
	// QuotaScope is "account" for the account quota, "bucket" for the
	// quota each of its buckets gets, and "" for neither.
	QuotaScope                    string
	QuotaMaxSize, QuotaMaxObjects *int64
	QuotaEnabled                  *bool
}

// The account id's form, rgw::account::generate_id and validate_id
// (:34-71): "RGW" and 17 decimal digits.
const (
	accountIDPrefix = "RGW"
	accountIDLen    = 20
	decimalDigits   = "0123456789"
)

// generateAccountID is generate_id (:38-45): gen_rand_numeric's crypto
// random digits with "RGW" over the first three.
func generateAccountID() string {
	return accountIDPrefix + randomFrom(decimalDigits, accountIDLen-len(accountIDPrefix))
}

// validateAccountID is validate_id (:47-71) with its messages.
func validateAccountID(id string) error {
	switch {
	case len(id) != accountIDLen:
		return ErrInvalidArgument.WithMessage(fmt.Sprintf("account id must be %d bytes long", accountIDLen))
	case !strings.HasPrefix(id, accountIDPrefix):
		return ErrInvalidArgument.WithMessage("account id must start with " + accountIDPrefix)
	case !meta.ValidAccountID(id):
		return ErrInvalidArgument.WithMessage("account id must end with numeric digits")
	}
	return nil
}

// validateAccountName is validate_name (:73-103) with its messages:
// check_utf8 accepts the well-formed sequences of Unicode's Table 3-7, as
// utf8.ValidString does.
func validateAccountName(name string) error {
	switch {
	case name == "":
		return ErrInvalidArgument.WithMessage("account name must not be empty")
	case strings.Contains(name, "$"):
		return ErrInvalidArgument.WithMessage("account name must not contain $")
	case strings.Contains(name, ":"):
		return ErrInvalidArgument.WithMessage("account name must not contain :")
	case !utf8.ValidString(name):
		return ErrInvalidArgument.WithMessage("account name must be valid utf8")
	}
	return nil
}

// applyLimits sets each limit p names.
func (p AccountParams) applyLimits(info *meta.AccountInfo) {
	for _, l := range []struct {
		v   *int32
		dst *int32
	}{
		{p.MaxUsers, &info.MaxUsers},
		{p.MaxRoles, &info.MaxRoles},
		{p.MaxGroups, &info.MaxGroups},
		{p.MaxAccessKeys, &info.MaxAccessKeys},
		{p.MaxBuckets, &info.MaxBuckets},
	} {
		if l.v != nil {
			*l.dst = *l.v
		}
	}
}

// loadAccount is the lookup info, modify and remove share (:190-206): by id,
// else by name in the tenant, else by email. A missing account is radosgw's
// raw ENOENT, which the REST layer answers as the NoSuchKey row. A store's
// failure is its S3 error alone (storeErr): the store names an email
// redirect by the email.
func loadAccount(ctx context.Context, env *Env, p AccountParams) (*AccountRecord, error) {
	var (
		rec *AccountRecord
		err error
	)
	switch {
	case p.ID != "":
		rec, err = env.Accounts.GetAccount(ctx, p.ID)
	case p.Name != "":
		rec, err = env.Accounts.GetAccountByName(ctx, p.Tenant, p.Name)
	case p.Email != "":
		rec, err = env.Accounts.GetAccountByEmail(ctx, p.Email)
	default:
		return nil, ErrInvalidArgument.WithMessage("requires --account-id or --account-name or --email")
	}
	switch {
	case errors.Is(err, ErrNoSuchEntity):
		return nil, ErrNoSuchKey
	case err != nil:
		return nil, storeErr(err)
	}
	return rec, nil
}

// accountCapType is the cap type the account get and delete check: Squid
// checks "account", a type no caps command can grant, so only an admin or
// system identity passes there (rgw_rest_account.cc:173-174 and :195-196
// at v19.2.6); Tentacle checks "accounts" (:173-174 and :195-196 at
// v20.2.4).
func accountCapType(r *Request) string {
	if r.Env != nil && r.Env.Zone != nil && r.Env.Zone.Release() < denc.Tentacle {
		return "account"
	}
	return "accounts"
}

// CreateAccount is RGWOp_Account_Create (rgw_rest_account.cc:20-102) and
// create (:112-177). Cap accounts=write.
type CreateAccount struct {
	AdminOp
	Params AccountParams
	Result meta.AccountInfo
}

// Name is create_account.
func (o *CreateAccount) Name() string { return "create_account" }

// VerifyPermission is RGWOp_Account_Create::check_caps.
func (o *CreateAccount) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "accounts", meta.CapWrite)
}

// Execute validates a name when one is given, builds the account with the
// limits given and the configured default quotas, takes the id given once
// it is valid or generates one, and writes the account exclusively. An id,
// name or email another account holds is ErrAccountAlreadyExists, the row
// the REST layer maps EEXIST to here alone.
func (o *CreateAccount) Execute(ctx context.Context, r *Request) error {
	env, p := r.Env, o.Params
	if p.Name != "" {
		if err := validateAccountName(p.Name); err != nil {
			return err
		}
	}
	info := meta.NewAccountInfo()
	info.Tenant, info.Name, info.Email = p.Tenant, p.Name, p.Email
	p.applyLimits(&info)
	info.Quota = defaultQuota(env, "account")
	info.BucketQuota = defaultQuota(env, "bucket")
	if p.ID == "" {
		info.ID = generateAccountID()
	} else if err := validateAccountID(p.ID); err != nil {
		return err
	} else {
		info.ID = p.ID
	}
	rec := &AccountRecord{Info: info}
	if err := env.Accounts.PutAccount(ctx, rec, nil, PutAccountOptions{Exclusive: true}); err != nil {
		return storeErr(err)
	}
	o.Result = rec.Info
	return nil
}

// GetAccountInfo is RGWOp_Account_Get (rgw_rest_account.cc:171-191) and
// info (:461-496). Cap account=read on Squid, accounts=read on Tentacle.
type GetAccountInfo struct {
	AdminOp
	Params AccountParams
	Result meta.AccountInfo
}

// Name is get_account.
func (o *GetAccountInfo) Name() string { return "get_account" }

// VerifyPermission is RGWOp_Account_Get::check_caps at the zone's release.
func (o *GetAccountInfo) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, accountCapType(r), meta.CapRead)
}

// Execute is info's lookup.
func (o *GetAccountInfo) Execute(ctx context.Context, r *Request) error {
	rec, err := loadAccount(ctx, r.Env, o.Params)
	if err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// ModifyAccount is RGWOp_Account_Modify (rgw_rest_account.cc:104-168) and,
// on Tentacle, RGWOp_Account_Quota_Set (v20.2.4 :223-299), over modify
// (:179-272). Cap accounts=write.
type ModifyAccount struct {
	AdminOp
	Params AccountParams
	Result meta.AccountInfo
}

// Name is modify_account.
func (o *ModifyAccount) Name() string { return "modify_account" }

// VerifyPermission is RGWOp_Account_Modify::check_caps, which
// RGWOp_Account_Quota_Set's matches.
func (o *ModifyAccount) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "accounts", meta.CapWrite)
}

// Execute is modify: the tenant cannot change; a name, once valid, an
// email and each limit given replace the stored ones; the quota the scope
// names takes each value given. The write is checked against the version
// read. A name or email another account holds is radosgw's EEXIST, which
// modify passes through to the S3 table's BucketAlreadyExists row. A
// maximum size below -1, which radosgw stores and reads as no limit, is
// refused as the user quota's is (validSizeLimit).
func (o *ModifyAccount) Execute(ctx context.Context, r *Request) error {
	p := o.Params
	rec, err := loadAccount(ctx, r.Env, p)
	if err != nil {
		return err
	}
	old := rec.Info
	info := &rec.Info
	if p.Tenant != "" && p.Tenant != info.Tenant {
		return ErrInvalidArgument.WithMessage("cannot modify account tenant")
	}
	if p.Name != "" {
		if verr := validateAccountName(p.Name); verr != nil {
			return verr
		}
		info.Name = p.Name
	}
	if p.Email != "" {
		info.Email = p.Email
	}
	p.applyLimits(info)
	var q *meta.Quota
	switch p.QuotaScope {
	case "account":
		q = &info.Quota
	case "bucket":
		q = &info.BucketQuota
	}
	if q != nil {
		if p.QuotaMaxSize != nil {
			if !validSizeLimit(*p.QuotaMaxSize) {
				return ErrInvalidArgument
			}
			q.MaxSize = *p.QuotaMaxSize
		}
		if p.QuotaMaxObjects != nil {
			q.MaxObjects = *p.QuotaMaxObjects
		}
		if p.QuotaEnabled != nil {
			q.Enabled = *p.QuotaEnabled
		}
	}
	err = r.Env.Accounts.PutAccount(ctx, rec, &old, PutAccountOptions{})
	switch {
	case errors.Is(err, ErrAccountAlreadyExists):
		return ErrBucketAlreadyExists
	case err != nil:
		return storeErr(err)
	}
	o.Result = rec.Info
	return nil
}

// accountListChunk is remove's max_items (:306).
const accountListChunk = 100

// RemoveAccount is RGWOp_Account_Delete (rgw_rest_account.cc:193-221) and
// remove (:274-459). Cap account=write on Squid, accounts=write on
// Tentacle.
type RemoveAccount struct {
	AdminOp
	Params AccountParams
}

// Name is delete_account.
func (o *RemoveAccount) Name() string { return "delete_account" }

// VerifyPermission is RGWOp_Account_Delete::check_caps at the zone's
// release.
func (o *RemoveAccount) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, accountCapType(r), meta.CapWrite)
}

// Execute is remove without purge_data, which the REST layer never sets: a
// user of the account refuses the removal with -ENOTEMPTY, the
// BucketNotEmpty row, and a bucket the account owns with -EEXIST, the
// BucketAlreadyExists row, each with radosgw's message. Roles, groups,
// OIDC providers and topics have no store here and are none. Then the
// account goes under the version read; one removed meanwhile is NoSuchKey.
func (o *RemoveAccount) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	rec, err := loadAccount(ctx, env, o.Params)
	if err != nil {
		return err
	}
	id := rec.Info.ID
	hasUser, err := accountHasUser(ctx, env, rec.Info)
	if err != nil {
		return AsError(err).WithMessage("Unable to list account users")
	}
	if hasUser {
		return ErrBucketNotEmpty.WithMessage("The account cannot be deleted until all users are removed.")
	}
	ents, _, _, err := env.Users.ListUserBuckets(ctx, meta.AccountOwner(id), "", accountListChunk)
	if err != nil {
		return AsError(err).WithMessage("Unable to list account buckets")
	}
	if len(ents) > 0 {
		return ErrBucketAlreadyExists.WithMessage("The account cannot be deleted until all buckets are removed.")
	}
	err = env.Accounts.RemoveAccount(ctx, rec)
	switch {
	case errors.Is(err, ErrNoSuchEntity):
		return ErrNoSuchKey
	case err != nil:
		return storeErr(err)
	}
	return nil
}

// accountHasUser is list_account_users as remove pages it
// (rgw_sal_rados.cc:1399-1437 at v19.2.6, :1938-1976 at v20.2.4): each id
// the users index lists is loaded as a user of the account's tenant, and an
// id whose user is gone is skipped, so an entry a removal left behind holds
// nothing off. A user that does not name the account counts, as there: a
// join adds the user's entry before it stores the user (writeUser), and
// that entry holds the removal off while the join is in flight.
func accountHasUser(ctx context.Context, env *Env, info meta.AccountInfo) (bool, error) {
	marker := ""
	for {
		ids, next, err := env.Accounts.ListAccountUsers(ctx, info.ID, marker, accountListChunk)
		if err != nil {
			return false, err
		}
		for _, uid := range ids {
			_, err := env.Users.GetUser(ctx, meta.UserID{Tenant: info.Tenant, ID: uid})
			switch {
			case err == nil:
				return true, nil
			case !errors.Is(err, ErrNoSuchUser):
				return false, err
			}
		}
		if next == "" {
			return false, nil
		}
		marker = next
	}
}
