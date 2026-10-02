package op

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// AccountRecord is an account as the metadata store returns it:
// RGWAccountInfo with the object's xattrs and version.
type AccountRecord struct {
	Info    meta.AccountInfo
	Attrs   map[string][]byte
	Version meta.ObjVersion
	Mtime   time.Time
}

// PutAccountOptions is store_account's exclusive flag.
type PutAccountOptions struct{ Exclusive bool }

//counterfeiter:generate . AccountStore

// AccountStore is the account reads, the admin API's writes and indexes
// (driver/rados/account.cc), and the AccountName authz.AccountLookup needs.
type AccountStore interface {
	// GetAccount returns the account with id, or ErrNoSuchEntity.
	GetAccount(ctx context.Context, id string) (*AccountRecord, error)
	// GetAccountByName and GetAccountByEmail follow the name and email index
	// objects (account.cc:200-240), the email matched case-insensitively; an
	// index that names no account is ErrNoSuchEntity.
	GetAccountByName(ctx context.Context, tenant, name string) (*AccountRecord, error)
	GetAccountByEmail(ctx context.Context, email string) (*AccountRecord, error)
	// PutAccount is rgwrados::account::write (account.cc:242-392): old is the
	// record before the change (nil on create). Each refusal comes before
	// anything is written:
	//   - an id that differs from old's is ErrInvalidArgument;
	//   - a name (in its tenant) or an email that differs from old's and that
	//     any index entry holds is ErrAccountAlreadyExists, whoever the entry
	//     names: another account, a user (users and accounts share the email
	//     index, account.cc:102-114), or this account when old does not list it;
	//   - Exclusive over a stored id is ErrAccountAlreadyExists;
	//   - a non-zero rec.Version that differs from the stored version is
	//     ErrConcurrentModification; a zero one is not checked.
	// The write leaves rec.Version at {1, a fresh tag} for a new account and at
	// {Ver+1, the same tag} over a stored one, as cls_version_inc counts, and
	// drops old's name and email entries only where they still name this account.
	PutAccount(ctx context.Context, rec *AccountRecord, old *meta.AccountInfo, opts PutAccountOptions) error
	// RemoveAccount is account.cc:394-438.
	RemoveAccount(ctx context.Context, rec *AccountRecord) error
	// The users index "users.<account>" (driver/rados/users.cc): AddAccountUser
	// is add with exclusive=false and no limit, keyed by the display name;
	// RemoveAccountUser removes that key; ListAccountUsers returns user ids.
	// cls_user keys the index by the display name lowercased
	// (cls_user.cc:511-518), so names differing only in case share an entry.
	// Removing a key the index lacks is ErrNoSuchKey, the ENOENT radosgw's
	// remove_user does not ignore. A listing returns the entries after
	// marker, at most maxIDs and never more than cls_user's 1000; next is
	// the last key returned when more remain, else "".
	AddAccountUser(ctx context.Context, accountID string, info meta.UserInfo) error
	RemoveAccountUser(ctx context.Context, accountID, displayName string) error
	ListAccountUsers(ctx context.Context, accountID, marker string, maxIDs uint32) (ids []string, next string, err error)
	// AccountName is GetAccount(ctx, id).Info.Name: what an ACL owner or
	// grantee naming an account resolves to.
	AccountName(ctx context.Context, id string) (string, error)
}
