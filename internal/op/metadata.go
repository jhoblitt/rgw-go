package op

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// MetadataEntry is one entry of a metadata section as the admin API shows it.
type MetadataEntry struct {
	Key     string
	Data    json.RawMessage
	Version meta.ObjVersion
	Mtime   time.Time
	// Doc is the entry's document as RGWMetadataObject::dump writes it,
	// what the admin getter renders under "data" in every format; Get sets it.
	Doc meta.Dumper
}

// PutMetadataOptions guards a metadata Put.
type PutMetadataOptions struct {
	// IfVersion, when non-nil, fails with ErrConcurrentModification unless the
	// stored version matches.
	IfVersion *meta.ObjVersion
}

//counterfeiter:generate . MetadataStore

// MetadataStore is the admin API's metadata sections: user, bucket and
// bucket.instance.
type MetadataStore interface {
	Get(ctx context.Context, section, key string) (MetadataEntry, error)
	Put(ctx context.Context, section, key string, e MetadataEntry, opts PutMetadataOptions) error
	Remove(ctx context.Context, section, key string) error
	List(ctx context.Context, section, marker string, maxEntries int) (keys []string, next string, more bool, err error)
}

// RefuseRawAttrs is ErrInvalidArgument for a metadata document's attr whose
// name lacks meta.AttrPrefix. radosgw sets each attr a put's document
// carries on the object, in the op that sets its version, so one named
// ceph.objclass.version replaces that version (docs/ceph-upstream-bugs.md,
// "radosgw's metadata put writes a document's attrs raw, the object's
// version among them"); a get shows the user.rgw. attrs alone, so no
// document read back carries another. A name holding a NUL byte is refused
// too (RefuseNUL), as it would set the attr its first part names.
func RefuseRawAttrs(attrs map[string][]byte) error {
	for _, k := range slices.Sorted(maps.Keys(attrs)) {
		if !strings.HasPrefix(k, meta.AttrPrefix) {
			return fmt.Errorf("%w: a document attr outside %s", ErrInvalidArgument, meta.AttrPrefix)
		}
		if err := RefuseNUL("a document attr's name", k); err != nil {
			return err
		}
	}
	return nil
}

// RefuseNUL is ErrInvalidArgument when any of values, names a document
// gives, holds a NUL byte: go-ceph cuts an object, pool, namespace or xattr
// name at the first NUL (docs/cgo-limitations.md, "go-ceph cuts object
// names at a NUL byte"), so a name holding one would name another object
// than its own. radosgw takes such a name whole.
func RefuseNUL(what string, values ...string) error {
	for _, v := range values {
		if strings.IndexByte(v, 0) >= 0 {
			return fmt.Errorf("%w: %s holds a NUL byte", ErrInvalidArgument, what)
		}
	}
	return nil
}

// RefuseNULKey is ErrInvalidRequest for a metadata key holding a NUL byte,
// for RefuseNUL's reason. It is radosgw's answer to a NUL in a request's
// decoded path, ERR_ZERO_IN_URL (rgw_rest.cc:2182-2186 at v19.2.6, :2204-2208
// at v20.2.4; rgw_common.cc:136, :137), which does not cover the query the
// key arrives in (req_info, rgw_common.cc:238-243 at v19.2.6), so radosgw
// takes such a key.
func RefuseNULKey(key string) error {
	if strings.IndexByte(key, 0) >= 0 {
		return fmt.Errorf("%w: the metadata key holds a NUL byte", ErrInvalidRequest)
	}
	return nil
}

// RefuseNULKeys is RefuseNUL over a user document's names that name
// objects: the user id, the email, the account id and each access key's
// and Swift key's id.
func RefuseNULKeys(info *meta.UserInfo) error {
	names := []string{info.UserID.Tenant, info.UserID.ID, info.Email, info.AccountID}
	for _, keys := range []map[string]meta.AccessKey{info.AccessKeys, info.SwiftKeys} {
		for id, k := range keys {
			names = append(names, id, k.ID)
		}
	}
	return RefuseNUL("the user document", names...)
}

// The user metadata section is RGWUserMetadataHandler
// (driver/rados/rgw_user.cc:2719-2839 at v19.2.6, :2741-2868 at v20.2.4)
// over a UserStore and an AccountStore. Both stores serve it through these
// functions, so the section is the same whichever store holds the users.

// GetMetadataUser is the user section's get: the user the key names, its
// rgw_user string form, as RGWUserCompleteInfo with the user's attrs, at
// the version and mtime it was read at. A missing user is ErrNoSuchKey, the
// ENOENT the handler returns.
func GetMetadataUser(ctx context.Context, users UserStore, key string) (MetadataEntry, error) {
	rec, err := users.GetUser(ctx, meta.ParseUserID(key))
	if errors.Is(err, ErrNoSuchUser) {
		return MetadataEntry{}, fmt.Errorf("user %s: %w", key, ErrNoSuchKey)
	}
	if err != nil {
		return MetadataEntry{}, err
	}
	doc := meta.UserCompleteInfo{Info: rec.Info, Attrs: rec.Attrs}
	data, err := json.Marshal(doc)
	if err != nil {
		return MetadataEntry{}, err
	}
	return MetadataEntry{Key: key, Data: data, Doc: doc, Version: rec.Version, Mtime: rec.Mtime}, nil
}

// PutMetadataUser is the user section's put, RGWMetadataHandlerPut_User's
// put_checked through store_user_info (rgw_user.cc:2817-2839 at v19.2.6;
// RGWUserMetadataHandler::put, :2820-2851 at v20.2.4): e.Data decoded as
// RGWUserCompleteInfo, then written as writeUser writes a user, the account
// users index around it, with the document's attrs when it has them. The
// stored user is read first, as put_pre reads it (rgw_metadata.cc:255-278
// at v19.2.6), and the write is made under the version read, or as an
// exclusive create when there is none; a version opts.IfVersion names that
// the read does not find is ErrConcurrentModification. The write version
// is e.Version's when it has a tag.
//
// Each refusal comes before anything is written:
//   - a document that does not decode is ErrInvalidArgument;
//   - a document naming another user than the key is ErrInvalidArgument:
//     radosgw reads the key's user and writes the document's, keeping the
//     key's indexes against the document's record;
//   - a user id or tenant in an account id's form is ErrInvalidArgument, as
//     a user create refuses one, since the email and key indexes would read
//     the user as an account;
//   - an attr outside user.rgw. is ErrInvalidArgument (RefuseRawAttrs);
//   - a NUL byte in the user id, the email, the account id or a key's id
//     is ErrInvalidArgument (RefuseNULKeys);
//   - an account the account store does not hold, an account of another
//     tenant, and a root user outside an account are ErrInvalidArgument;
//   - a key without a secret is ErrInvalidSecretKey, as a key create
//     refuses one, since anyone holding its id could sign with it;
//   - an email another user or an account holds is ErrEmailExists, as the
//     user and account share the email index;
//   - a display name another user of the account holds is
//     ErrBucketAlreadyExists, radosgw's EEXIST.
func PutMetadataUser(ctx context.Context, users UserStore, accounts AccountStore, key string, e MetadataEntry, opts PutMetadataOptions) error {
	var doc meta.UserCompleteInfo
	if err := json.Unmarshal(e.Data, &doc); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	info := doc.Info
	uid := meta.ParseUserID(key)
	if info.UserID != uid {
		return fmt.Errorf("%w: the document names user %s under key %s", ErrInvalidArgument, info.UserID, key)
	}
	if meta.ValidAccountID(uid.ID) || meta.ValidAccountID(uid.Tenant) {
		return fmt.Errorf("%w: user %s has an account id's form", ErrInvalidArgument, key)
	}
	if err := RefuseNULKeys(&info); err != nil {
		return err
	}
	if err := RefuseRawAttrs(doc.Attrs); err != nil {
		return err
	}
	cur, err := users.GetUser(ctx, uid)
	switch {
	case errors.Is(err, ErrNoSuchUser):
		cur = nil
	case err != nil:
		return storeErr(err)
	}
	var read meta.ObjVersion
	if cur != nil {
		read = cur.Version
		if read.Ver == 0 {
			return fmt.Errorf("%w: user %s carries no version to write it under", ErrInternalError, key)
		}
	}
	if opts.IfVersion != nil && *opts.IfVersion != read {
		return fmt.Errorf("user %s: %w", key, ErrConcurrentModification)
	}
	if err := checkMetadataUser(ctx, users, accounts, &info, cur); err != nil {
		return err
	}
	rec := &UserRecord{Info: info, Version: read}
	if doc.HasAttrs {
		rec.Attrs = doc.Attrs
	}
	put := PutUserOptions{Exclusive: cur == nil, Mtime: e.Mtime, WriteVersion: e.Version}
	var old *meta.UserInfo
	if cur != nil {
		put.IfVersion = &rec.Version
		old = &cur.Info
	}
	env := &Env{Users: users, Accounts: accounts}
	return writeUserWith(ctx, env, rec, old, ErrBucketAlreadyExists, put)
}

// checkMetadataUser is PutMetadataUser's refusals of info over cur, the
// stored record or nil, past the document's own.
func checkMetadataUser(ctx context.Context, users UserStore, accounts AccountStore, info *meta.UserInfo, cur *UserRecord) error {
	for _, keys := range []map[string]meta.AccessKey{info.AccessKeys, info.SwiftKeys} {
		for _, k := range keys {
			if k.Secret == "" {
				return ErrInvalidSecretKey
			}
		}
	}
	switch {
	case info.AccountID != "":
		acct, err := accounts.GetAccount(ctx, info.AccountID)
		if errors.Is(err, ErrNoSuchEntity) {
			return fmt.Errorf("%w: account %s does not exist", ErrInvalidArgument, info.AccountID)
		}
		if err != nil {
			return storeErr(err)
		}
		if acct.Info.Tenant != info.UserID.Tenant {
			return fmt.Errorf("%w: account %s is of another tenant", ErrInvalidArgument, info.AccountID)
		}
	case info.Type == meta.IdentityRoot:
		return fmt.Errorf("%w: a root user outside an account", ErrInvalidArgument)
	}
	if info.Email == "" || cur != nil && foldEqual(cur.Info.Email, info.Email) {
		return nil
	}
	_, err := accounts.GetAccountByEmail(ctx, info.Email)
	switch {
	case err == nil:
		return ErrEmailExists
	case !errors.Is(err, ErrNoSuchEntity):
		return storeErr(err)
	}
	holder, err := users.GetUserByEmail(ctx, info.Email)
	switch {
	case errors.Is(err, ErrNoSuchUser):
		return nil
	case err != nil:
		return storeErr(err)
	case holder.Info.UserID != info.UserID && foldEqual(holder.Info.Email, info.Email):
		return ErrEmailExists
	}
	return nil
}

// RemoveMetadataUser is the user section's remove (rgw_user.cc:2772-2787 at
// v19.2.6, :2853-2868 at v20.2.4): the user the key names, read and then
// removed under the version read, then its entry in its account's users
// index. A user outside an account whose bucket list holds a bucket is
// ErrBucketAlreadyExists, as the user removal route refuses it, since
// remove_user_info removes that list and with it the buckets' place in
// their owner's list; radosgw's metadata remove does not check. A missing
// user is ErrNoSuchKey.
func RemoveMetadataUser(ctx context.Context, users UserStore, accounts AccountStore, key string) error {
	rec, err := users.GetUser(ctx, meta.ParseUserID(key))
	if errors.Is(err, ErrNoSuchUser) {
		return fmt.Errorf("user %s: %w", key, ErrNoSuchKey)
	}
	if err != nil {
		return storeErr(err)
	}
	if rec.Info.AccountID == "" {
		ents, _, _, err := users.ListUserBuckets(ctx, meta.UserOwner(rec.Info.UserID), "", 1)
		if err != nil {
			return storeErr(err)
		}
		if len(ents) > 0 {
			return ErrBucketAlreadyExists
		}
	}
	if err := users.RemoveUser(ctx, rec); err != nil {
		if errors.Is(err, ErrNoSuchUser) {
			return fmt.Errorf("user %s: %w", key, ErrNoSuchKey)
		}
		return storeErr(err)
	}
	if rec.Info.AccountID != "" {
		err := accounts.RemoveAccountUser(ctx, rec.Info.AccountID, rec.Info.DisplayName)
		if err != nil && !errors.Is(err, ErrNoSuchKey) {
			return storeErr(err)
		}
	}
	return nil
}
