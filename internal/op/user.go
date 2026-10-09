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
	// IfVersion, when non-nil with a non-zero Ver, fails with
	// ErrConcurrentModification unless the stored version matches.
	IfVersion *meta.ObjVersion
	// Mtime is stored as given, as store_user_info's mtime argument is
	// (svc_user_rados.cc:300 at v19.2.6, :281-282 at v20.2.4); zero stores
	// the current time (svc_sys_obj_core.cc:508-510 at both tags), as for
	// every admin write, since RadosUser::store_user passes none
	// (rgw_sal_rados.cc:269-276 at v19.2.6, :286-293 at v20.2.4).
	Mtime time.Time
	// WriteVersion, when its Tag is not empty, is the version written in
	// place of the one PutUser computes, as a tracker's write version is
	// (PutOperation::prepare, svc_user_rados.cc:234-241 at v19.2.6, :213-220
	// at v20.2.4): a metadata put's document version.
	WriteVersion meta.ObjVersion
}

//counterfeiter:generate . UserStore

// UserStore reads and writes users and their index objects.
type UserStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*UserRecord, error)
	GetUserByEmail(ctx context.Context, email string) (*UserRecord, error)
	// PutUser writes the user, maintains the email index and the active
	// access keys' index objects, and sends the cache notify, leaving rec at
	// the version and mtime written. rec.Version and rec.Mtime are not read;
	// the version written is IfVersion's plus one under its tag, or a fresh
	// one when IfVersion is nil or its Tag empty (PutOperation::prepare,
	// svc_user_rados.cc:234-241 at v19.2.6, :213-220 at v20.2.4), and the
	// mtime is opts.Mtime's. The stored version, ver and tag both, is checked
	// only when IfVersion is non-nil with a non-zero Ver, so a zero IfVersion
	// overwrites unchecked (RGWObjVersionTracker::version_for_check,
	// rgw_common.h:950-955 at v19.2.6, :974-979 at v20.2.4; obj_version's
	// compare, cls_version_types.h:46-49 at v19.2.6, :54-57 at v20.2.4). An
	// active access key another user holds is ErrKeyExists (:257-270 at
	// v19.2.6), and so is any access key FindKeyHolder gives another user;
	// each of the record's access keys is claimed for the user before the
	// user is written, so of two writes giving one key id to two users one
	// fails, and a key the record drops is released after.
	PutUser(ctx context.Context, rec *UserRecord, opts PutUserOptions) error
	// RemoveUser removes the active access keys' and Swift keys' index
	// objects, the email index and, for a user outside an account, the
	// user's bucket list, then the user, in remove_user_info's order
	// (svc_user_rados.cc:551-630 at v19.2.6, :526-604 at v20.2.4). The user's
	// removal checks rec.Version only when its Ver is not 0. A missing user
	// is ErrNoSuchUser and a changed one ErrConcurrentModification; on
	// either, the indexes and the bucket list may already be gone. The
	// user's access keys are released once the user is gone.
	RemoveUser(ctx context.Context, rec *UserRecord) error
	// FindKeyHolder returns the user holding the access key id among its
	// keys, active or not: the user PutUser claimed it for, else, a store
	// whose claims do not cover every user, the user radosgw's active-key
	// index names or a stored user whose record holds the key. No holder is
	// ErrNoSuchUser.
	FindKeyHolder(ctx context.Context, id string) (meta.UserID, error)
	// ListUserBuckets pages the owner's bucket list from marker, at most
	// maxEntries entries, which must not be negative. When the page has
	// entries, more reports whether entries remain past next. A maxEntries
	// of 0 lists nothing, and more need not report what remains: radosgw's
	// listing then ends with an empty next marker (rgw_op.cc:2604 at
	// v19.2.6, :2836 at v20.2.4), so a caller stops on an empty page.
	ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, maxEntries int) (ents []meta.BucketEnt, next string, more bool, err error)
}
