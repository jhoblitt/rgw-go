package op

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// UserRecord is a user as stored: the info object, its xattrs and the
// cls_version the metadata cache tracks.
type UserRecord struct {
	Info    meta.UserInfo
	Attrs   map[string][]byte
	Version meta.ObjVersion
	Mtime   time.Time
}

// PutUserOptions guards a PutUser.
type PutUserOptions struct {
	// Exclusive fails with ErrUserAlreadyExists when the user exists.
	Exclusive bool
	// IfVersion, when non-nil, fails with ErrConcurrentModification unless the
	// stored version matches.
	IfVersion *meta.ObjVersion
}

//counterfeiter:generate . UserStore

// UserStore reads and writes users and their index objects.
type UserStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*UserRecord, error)
	GetUserByEmail(ctx context.Context, email string) (*UserRecord, error)
	// PutUser writes the user, maintains the email index and the active
	// access keys' index objects, and sends the cache notify, leaving rec at
	// the version written: IfVersion's plus one under its tag, or a fresh one
	// without IfVersion (svc_user_rados.cc:234-241 at v19.2.6). An active
	// access key another user holds is ErrKeyExists (:257-270).
	PutUser(ctx context.Context, rec *UserRecord, opts PutUserOptions) error
	RemoveUser(ctx context.Context, rec *UserRecord) error
	// ListUserBuckets pages the owner's bucket list from marker, at most
	// maxEntries entries; more reports whether entries remain past next.
	ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, maxEntries int) (ents []meta.BucketEnt, next string, more bool, err error)
}
