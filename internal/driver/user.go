package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The user store is RGWSI_User_RADOS (services/svc_user_rados.cc). A bare
// line number is v19.2.6's, and each fact cited holds at v20.2.4 too.

// userObj is a user's info object in users.uid, named by the user id
// (RGWSI_User_Module, :41-48; v20.2.4 :112-113).
func (s *Store) userObj(id meta.UserID) sysObj {
	return sysObj{pool: s.zone.Params.UserUIDPool, oid: id.String()}
}

// keyIndexObj is an access key's index object in users.keys (:335, :517).
func (s *Store) keyIndexObj(key string) sysObj {
	return sysObj{pool: s.zone.Params.UserKeysPool, oid: key}
}

// emailIndexObj is an email's index object in users.email, named by the
// lower-cased email so that a lookup matches any case (:318-321, :529-531).
func (s *Store) emailIndexObj(email string) sysObj {
	return sysObj{pool: s.zone.Params.UserEmailPool, oid: lowerASCII(email)}
}

// swiftIndexObj is a Swift key's index object in users.swift (:347, :540).
func (s *Store) swiftIndexObj(name string) sysObj {
	return sysObj{pool: s.zone.Params.UserSwiftPool, oid: name}
}

// bucketsSuffix is RGW_BUCKETS_OBJ_SUFFIX (:29).
const bucketsSuffix = ".buckets"

// ownerBucketsObj is an owner's bucket list: "<uid>.buckets" in users.uid
// for a user (:109-113), "buckets.<id>" in the account pool for an account
// (driver/rados/account.cc:44-50 at both tags).
func (s *Store) ownerBucketsObj(owner meta.Owner) sysObj {
	if owner.User != nil {
		return sysObj{pool: s.zone.Params.UserUIDPool, oid: owner.User.String() + bucketsSuffix}
	}
	return sysObj{pool: s.zone.Params.AccountPool, oid: "buckets." + owner.Account}
}

// accountObj is an account's info object, "account.<id>" in the account
// pool (account.cc:84-90 at both tags).
func (s *Store) accountObj(id string) sysObj {
	return sysObj{pool: s.zone.Params.AccountPool, oid: "account." + id}
}

// mapUserErr maps a seam error from a user's objects as op.FromRADOS does
// at user scope: ENOENT is NoSuchUser, EEXIST UserAlreadyExists and
// ECANCELED, a lost version race, ConcurrentModification.
func mapUserErr(err error) error { return op.FromRADOS(err, op.ScopeUser) }

// lowerASCII is boost::to_lower in the C locale radosgw runs in, which
// lower-cases ASCII letters alone.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// GetUser implements op.UserStore as read_user_info (:115-156; v20.2.4
// :96-137): the anonymous user is NoSuchUser without a read, and a uid
// object that does not decode or names another user is radosgw's -EIO.
func (s *Store) GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error) {
	if id.ID == op.AnonymousUserID {
		return nil, fmt.Errorf("user %s: %w", id, op.ErrNoSuchUser)
	}
	v := &objv{}
	res, err := s.sysobj.read(ctx, s.userObj(id), readParams{data: true, attrs: true, meta: true, objv: v})
	if err != nil {
		return nil, mapUserErr(err)
	}
	d := denc.NewDecoder(res.data)
	uo := meta.DecodeUserObject(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding user %s: %w", op.ErrInternalError, id, err)
	}
	if meta.ParseUserID(string(uo.UID)) != id {
		return nil, fmt.Errorf("%w: the object of user %s names %q", op.ErrInternalError, id, uo.UID)
	}
	return &op.UserRecord{Info: uo.Info, Attrs: res.attrs, Version: v.read, Mtime: res.mtime}, nil
}

// GetUserByAccessKey implements op.UserStore through the access key's index
// object (get_user_info_by_access_key, :765-777).
func (s *Store) GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error) {
	return s.userFromIndex(ctx, s.keyIndexObj(key))
}

// GetUserByEmail implements op.UserStore through the email's index object
// (get_user_info_by_email, :730-741).
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*op.UserRecord, error) {
	return s.userFromIndex(ctx, s.emailIndexObj(email))
}

// userFromIndex is get_user_info_from_index (:670-724; v20.2.4 :646-700):
// the user an index object names. Accounts share users.email with users, so
// an index naming an account is NoSuchUser.
func (s *Store) userFromIndex(ctx context.Context, o sysObj) (*op.UserRecord, error) {
	uid, err := s.readIndex(ctx, o)
	if err != nil {
		return nil, err
	}
	owner := meta.ParseOwner(string(uid))
	if owner.User == nil {
		return nil, fmt.Errorf("index %s/%s names account %s: %w", o.pool, o.oid, uid, op.ErrNoSuchUser)
	}
	return s.GetUser(ctx, *owner.User)
}

// readIndex is read_index (:650-668; v20.2.4 :626-644): the RGWUID an index
// object holds.
func (s *Store) readIndex(ctx context.Context, o sysObj) (meta.UID, error) {
	res, err := s.sysobj.read(ctx, o, readParams{data: true})
	if err != nil {
		return "", mapUserErr(err)
	}
	d := denc.NewDecoder(res.data)
	uid := meta.DecodeUID(d)
	if err := d.Err(); err != nil {
		return "", fmt.Errorf("%w: decoding index %s/%s: %w", op.ErrInternalError, o.pool, o.oid, err)
	}
	return uid, nil
}

// activeKeys is the ids of the active keys in m.
func activeKeys(m map[string]meta.AccessKey) map[string]bool {
	out := make(map[string]bool, len(m))
	for id, k := range m {
		if k.Active {
			out[id] = true
		}
	}
	return out
}

// PutUser implements op.UserStore as store_user_info's PutOperation
// (:189-511; v20.2.4 :171-486), whose old_info is the stored record unless
// the put is exclusive. The account and group user indexes it also keeps
// are the admin API's.
func (s *Store) PutUser(ctx context.Context, rec *op.UserRecord, opts op.PutUserOptions) error {
	id := rec.Info.UserID
	var old *meta.UserInfo
	if !opts.Exclusive {
		cur, err := s.GetUser(ctx, id)
		switch {
		case err == nil:
			old = &cur.Info
		case !errors.Is(err, op.ErrNoSuchUser):
			return err
		}
	}
	var oldKeys, oldSwift map[string]bool
	if old != nil {
		oldKeys, oldSwift = activeKeys(old.AccessKeys), activeKeys(old.SwiftKeys)
	}
	if err := s.checkKeys(ctx, id, rec.Info.SwiftKeys, oldSwift, s.swiftIndexObj); err != nil {
		return err
	}
	if err := s.checkKeys(ctx, id, rec.Info.AccessKeys, oldKeys, s.keyIndexObj); err != nil {
		return err
	}

	// prepare (:234-241; v20.2.4 :213-220): a read version with a tag moves
	// on by one under it; without one the write version is fresh. A read
	// version of 0 is not checked (version_for_check, rgw_common.h:950-955;
	// v20.2.4 :974-979).
	v := &objv{write: newWriteVersion()}
	if opts.IfVersion != nil {
		v.read = *opts.IfVersion
		if v.read.Tag != "" {
			v.write = meta.ObjVersion{Ver: v.read.Ver + 1, Tag: v.read.Tag}
		}
	}
	data := encodeAt(meta.UserObject{UID: meta.UID(id.String()), Info: rec.Info}, s.release)
	mtime, err := s.sysobj.write(ctx, s.userObj(id), data, rec.Attrs, opts.Exclusive, opts.Mtime, v)
	if err != nil {
		return mapUserErr(err)
	}
	rec.Version, rec.Mtime = v.read, mtime

	// complete (:309-358; v20.2.4 :289-338): each new index holds the
	// encoded RGWUID and is written as the uid object was, exclusively or
	// not, without a version.
	link := encodeAt(meta.UID(id.String()), s.release)
	putIndex := func(o sysObj) error {
		if _, err := s.sysobj.write(ctx, o, link, nil, opts.Exclusive, time.Time{}, nil); err != nil {
			return mapUserErr(err)
		}
		return nil
	}
	if e := rec.Info.Email; e != "" && (old == nil || lowerASCII(e) != lowerASCII(old.Email)) {
		if err := putIndex(s.emailIndexObj(e)); err != nil {
			return err
		}
	}
	for key := range activeKeys(rec.Info.AccessKeys) {
		if !oldKeys[key] {
			if err := putIndex(s.keyIndexObj(key)); err != nil {
				return err
			}
		}
	}
	for name := range activeKeys(rec.Info.SwiftKeys) {
		if !oldSwift[name] {
			if err := putIndex(s.swiftIndexObj(name)); err != nil {
				return err
			}
		}
	}
	if old != nil {
		return s.removeOldIndexes(ctx, *old, rec.Info)
	}
	return nil
}

// checkKeys is prepare's check of each key newly active in keys (:243-270):
// one whose index names an existing other user is radosgw's -EEXIST. The
// check reads the user through the index, as get_user_info_by_swift and
// get_user_info_by_access_key do, and takes any failure to read it, a
// dangling index's NoSuchUser among them, as no other user.
func (s *Store) checkKeys(ctx context.Context, id meta.UserID, keys map[string]meta.AccessKey, old map[string]bool, index func(string) sysObj) error {
	for key := range activeKeys(keys) {
		if old[key] {
			continue
		}
		holder, err := s.userFromIndex(ctx, index(key))
		if err == nil && holder.Info.UserID != id {
			return fmt.Errorf("key %s of user %s belongs to user %s: %w", key, id, holder.Info.UserID, op.ErrKeyExists)
		}
	}
	return nil
}

// removeIndex removes an index object; one already gone is no failure.
func (s *Store) removeIndex(ctx context.Context, o sysObj) error {
	if err := s.sysobj.remove(ctx, o, nil); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		return mapUserErr(err)
	}
	return nil
}

// removeOldIndexes is PutOperation::remove_old_indexes (:399-443): the
// indexes of the old record that the new one no longer has. radosgw names
// an access key's index by the key's own id when it removes one
// (remove_key_index, :513-520), and by its map key when it writes one.
func (s *Store) removeOldIndexes(ctx context.Context, old, cur meta.UserInfo) error {
	if old.Email != "" && lowerASCII(old.Email) != lowerASCII(cur.Email) {
		if err := s.removeIndex(ctx, s.emailIndexObj(old.Email)); err != nil {
			return err
		}
	}
	curKeys, curSwift := activeKeys(cur.AccessKeys), activeKeys(cur.SwiftKeys)
	for id, k := range old.AccessKeys {
		if k.Active && !curKeys[id] {
			if err := s.removeIndex(ctx, s.keyIndexObj(k.ID)); err != nil {
				return err
			}
		}
	}
	for name := range activeKeys(old.SwiftKeys) {
		if !curSwift[name] {
			if err := s.removeIndex(ctx, s.swiftIndexObj(name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveUser implements op.UserStore as remove_user_info (:551-630; v20.2.4
// :526-604): the active keys' indexes, the email index and, for a user
// outside an account, its bucket list go first, then the uid object, under
// rec's version when its Ver is not 0. The account and group unlinks it
// also makes (:601-622; v20.2.4 :575-596) are the admin API's.
//
// radosgw takes a uid object that is gone, or whose version check fails,
// for success (remove_uid_index, :639; v20.2.4 :614-616); RemoveUser
// reports the first as NoSuchUser and the second as ConcurrentModification,
// by which time the indexes and the bucket list are already gone, as they
// are in radosgw. A version check of a missing object fails as one of a
// changed object does, so a failed check is told apart by reading the
// object from RADOS.
func (s *Store) RemoveUser(ctx context.Context, rec *op.UserRecord) error {
	info := rec.Info
	for _, k := range info.AccessKeys {
		if k.Active {
			if err := s.removeIndex(ctx, s.keyIndexObj(k.ID)); err != nil {
				return err
			}
		}
	}
	for name := range activeKeys(info.SwiftKeys) {
		if err := s.removeIndex(ctx, s.swiftIndexObj(name)); err != nil {
			return err
		}
	}
	if info.Email != "" {
		if err := s.removeIndex(ctx, s.emailIndexObj(info.Email)); err != nil {
			return err
		}
	}
	if info.AccountID == "" {
		if err := s.removeIndex(ctx, s.ownerBucketsObj(meta.UserOwner(info.UserID))); err != nil {
			return err
		}
	}
	o := s.userObj(info.UserID)
	var v *objv
	if rec.Version.Ver != 0 {
		v = &objv{read: rec.Version}
	}
	err := s.sysobj.remove(ctx, o, v)
	if errors.Is(err, radosclient.ErrCanceled) && s.missing(ctx, o) {
		return fmt.Errorf("removing user %s: %w", info.UserID, op.ErrNoSuchUser)
	}
	return mapUserErr(err)
}

// missing reports whether a read of o from RADOS, past a cache entry that
// may be stale, finds it gone. A read that fails otherwise is logged and
// reports false.
func (s *Store) missing(ctx context.Context, o sysObj) bool {
	name := normalName(o.pool, o.oid)
	s.sysobj.cache.invalidateRemove(name)
	_, err := s.sysobj.readMiss(ctx, o, name, readParams{})
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		return true
	case err != nil:
		slog.WarnContext(ctx, "reading back an object whose version check failed",
			slog.String("pool", o.pool.String()), slog.String("oid", o.oid), slog.Any("error", err))
	}
	return false
}

// readAccount is rgwrados::account::read (account.cc:163-198 at both tags):
// the account's info, whose id must be the one asked for. A missing account
// is radosgw's raw ENOENT.
func (s *Store) readAccount(ctx context.Context, id string) (meta.AccountInfo, error) {
	res, err := s.sysobj.read(ctx, s.accountObj(id), readParams{data: true})
	if err != nil {
		return meta.AccountInfo{}, op.FromRADOS(err, op.ScopeService)
	}
	d := denc.NewDecoder(res.data)
	info := meta.DecodeAccountInfo(d)
	if err := d.Err(); err != nil {
		return meta.AccountInfo{}, fmt.Errorf("%w: decoding account %s: %w", op.ErrInternalError, id, err)
	}
	if info.ID != id {
		return meta.AccountInfo{}, fmt.Errorf("%w: the object of account %s names %q", op.ErrInternalError, id, info.ID)
	}
	return info, nil
}
