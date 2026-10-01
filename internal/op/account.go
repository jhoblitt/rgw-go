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

//counterfeiter:generate . AccountStore

// AccountStore reads accounts, rgw::sal::Driver::load_account_by_id.
type AccountStore interface {
	// GetAccount returns the account with id, or ErrNoSuchEntity.
	GetAccount(ctx context.Context, id string) (*AccountRecord, error)
	// AccountName is GetAccount(ctx, id).Info.Name: what an ACL owner or
	// grantee naming an account resolves to.
	AccountName(ctx context.Context, id string) (string, error)
}
