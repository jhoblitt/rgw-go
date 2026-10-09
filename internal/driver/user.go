package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
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

// keyHolderPool is the pool of rgw-go's key holder index: the key index's
// pool, in a namespace of its own beside the key index's.
func keyHolderPool(keys meta.Pool) meta.Pool {
	return meta.Pool{Name: keys.Name, NS: keys.NS + radosclient.KeyHolderNSSuffix}
}

// keyHolderObj is an access key's entry in rgw-go's key holder index,
// which holds the encoded RGWUID of the user holding the key, active or
// not. radosgw's key index names active keys alone (:329-339), so it cannot
// tell who holds a deactivated key.
func (s *Store) keyHolderObj(key string) sysObj {
	return sysObj{pool: keyHolderPool(s.zone.Params.UserKeysPool), oid: key}
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
		return nil, fmt.Errorf("index %s names account %s: %w", s.sysobj.hidden.name(o.pool, o.oid), uid, op.ErrNoSuchUser)
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
		return "", fmt.Errorf("%w: decoding index %s: %w", op.ErrInternalError, s.sysobj.hidden.name(o.pool, o.oid), err)
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
// are the admin API's. Before the uid object, each of the record's access
// keys is claimed in rgw-go's key holder index (claimKey), a key the write
// gains being refused where another user holds it, and a new email's index
// object is claimed (claimEmail); after the indexes, each key the stored
// record held that the new one drops is released (releaseKey). A write that
// fails after a claim leaves the claim, which names the user and is its own
// to a retry.
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
	var gained []string
	for _, key := range slices.Sorted(maps.Keys(rec.Info.AccessKeys)) {
		if !gainsKey(old, key, rec.Info.AccessKeys[key]) {
			s.keepClaim(ctx, id, key)
			continue
		}
		if err := s.claimKey(ctx, id, key); err != nil {
			return err
		}
		gained = append(gained, key)
	}
	newEmail := rec.Info.Email != "" && (old == nil || lowerASCII(rec.Info.Email) != lowerASCII(old.Email))
	var overwriteEmail *meta.ObjVersion
	if newEmail {
		var err error
		if overwriteEmail, err = s.claimEmail(ctx, id, rec.Info.Email, opts.SharedEmail); err != nil {
			return err
		}
	}

	// prepare (:234-241; v20.2.4 :213-220): a write version given with a tag
	// is written; otherwise a read version with a tag moves on by one under
	// it, and without one the write version is fresh. A read
	// version of 0 is not checked (version_for_check, rgw_common.h:950-955;
	// v20.2.4 :974-979).
	v := &objv{write: newWriteVersion()}
	if opts.IfVersion != nil {
		v.read = *opts.IfVersion
		if v.read.Tag != "" {
			v.write = meta.ObjVersion{Ver: v.read.Ver + 1, Tag: v.read.Tag}
		}
	}
	if opts.WriteVersion.Tag != "" {
		v.write = opts.WriteVersion
	}
	data := encodeAt(meta.UserObject{UID: meta.UID(id.String()), Info: rec.Info}, s.release)
	mtime, err := s.sysobj.write(ctx, s.userObj(id), data, rec.Attrs, opts.Exclusive, opts.Mtime, v)
	if err != nil {
		return mapUserErr(err)
	}
	rec.Version, rec.Mtime = v.read, mtime
	for _, key := range gained {
		s.recheckClaim(ctx, id, key)
	}

	// complete (:309-358; v20.2.4 :289-338): each new key index holds the
	// encoded RGWUID and is written as the uid object was, exclusively or
	// not, without a version. The email's was claimed before the uid object,
	// and is written here only over another user's, as opts.SharedEmail lets.
	link := encodeAt(meta.UID(id.String()), s.release)
	putIndex := func(o sysObj) error {
		if _, err := s.sysobj.write(ctx, o, link, nil, opts.Exclusive, time.Time{}, nil); err != nil {
			return mapUserErr(err)
		}
		return nil
	}
	// A failed write over a shared email's index comes after the user is
	// written, so the rest of the completion still runs, and the email's
	// error is returned once it has; a later step's failure is then logged.
	var emailErr error
	if overwriteEmail != nil {
		emailErr = s.overwriteEmail(ctx, id, rec.Info.Email, *overwriteEmail)
	}
	fail := func(err error) error {
		if emailErr == nil {
			return err
		}
		slog.WarnContext(ctx, "a user's index write failed after its email's", slog.String("user", id.String()), slog.Any("error", err))
		return emailErr
	}
	for key := range activeKeys(rec.Info.AccessKeys) {
		if !oldKeys[key] {
			if err := putIndex(s.keyIndexObj(key)); err != nil {
				return fail(err)
			}
		}
	}
	for name := range activeKeys(rec.Info.SwiftKeys) {
		if !oldSwift[name] {
			if err := putIndex(s.swiftIndexObj(name)); err != nil {
				return fail(err)
			}
		}
	}
	if old != nil {
		if err := s.removeOldIndexes(ctx, *old, rec.Info); err != nil {
			return fail(err)
		}
		for key := range old.AccessKeys {
			if _, kept := rec.Info.AccessKeys[key]; !kept {
				if err := s.releaseKey(ctx, id, key); err != nil {
					return fail(err)
				}
			}
		}
	}
	return emailErr
}

// FindKeyHolder implements op.UserStore: the user the key holder index
// names for key, else, the index holding no entry for it, the holder
// keyHolderOutsideIndex finds. An entry is read as claimKey reads it, so
// one in an account id's form still names a holder, which no user is.
func (s *Store) FindKeyHolder(ctx context.Context, key string) (meta.UserID, error) {
	uid, err := s.readIndex(ctx, s.keyHolderObj(key))
	switch {
	case errors.Is(err, op.ErrNoSuchUser):
		return s.keyHolderOutsideIndex(ctx, key, meta.UserID{})
	case err != nil:
		return meta.UserID{}, err
	}
	return meta.ParseUserID(string(uid)), nil
}

// holderWalkPage is the page keyHolderOutsideIndex lists users in.
var holderWalkPage = listPage

// keyHolderOutsideIndex is a holder of key, other than except, that the key
// holder index does not record: the user radosgw's key index names, which
// holds the key active, else the first stored user whose record holds the
// key, active or not, read from every page of the user section. A user
// radosgw or radosgw-admin wrote, or rgw-go wrote before the index, has no
// entry, so a missing entry is no proof that no user holds the key; the
// walk is one read per user. A user gone between the listing and its read,
// or a key index entry naming a user that is gone, is skipped; any other
// failure is returned, which refuses the key. No holder is ErrNoSuchUser.
func (s *Store) keyHolderOutsideIndex(ctx context.Context, key string, except meta.UserID) (meta.UserID, error) {
	rec, err := s.userFromIndex(ctx, s.keyIndexObj(key))
	switch {
	case err == nil && rec.Info.UserID != except:
		return rec.Info.UserID, nil
	case err != nil && !errors.Is(err, op.ErrNoSuchUser):
		return meta.UserID{}, err
	}
	marker := ""
	for {
		keys, next, more, err := s.List(ctx, sectionUser, marker, holderWalkPage)
		if err != nil {
			return meta.UserID{}, err
		}
		for _, k := range keys {
			uid := meta.ParseUserID(k)
			if uid == except {
				continue
			}
			rec, err := s.GetUser(ctx, uid)
			switch {
			case errors.Is(err, op.ErrNoSuchUser):
				continue
			case err != nil:
				return meta.UserID{}, err
			}
			if _, held := rec.Info.AccessKeys[key]; held {
				return rec.Info.UserID, nil
			}
		}
		if !more {
			return meta.UserID{}, fmt.Errorf("the holder of a key: %w", op.ErrNoSuchUser)
		}
		marker = next
	}
}

// gainsKey reports whether a write of key k over old, the stored record or
// nil, gains the key: a key old lacks, or one the write makes active. A key
// the user already holds and keeps, or deactivates, is no gain, so another
// user holding its id never refuses the write: a revocation always goes
// through.
func gainsKey(old *meta.UserInfo, key string, k meta.AccessKey) bool {
	if old == nil {
		return true
	}
	was, held := old.AccessKeys[key]
	return !held || k.Active && !was.Active
}

// readHolder is the user a key holder entry names and the entry's version.
// A missing entry is ErrNoSuchUser.
func (s *Store) readHolder(ctx context.Context, o sysObj) (meta.UserID, meta.ObjVersion, error) {
	v := &objv{}
	res, err := s.sysobj.read(ctx, o, readParams{data: true, objv: v})
	if err != nil {
		return meta.UserID{}, meta.ObjVersion{}, mapUserErr(err)
	}
	d := denc.NewDecoder(res.data)
	uid := meta.DecodeUID(d)
	if err := d.Err(); err != nil {
		return meta.UserID{}, meta.ObjVersion{}, fmt.Errorf("%w: decoding index %s: %w", op.ErrInternalError, s.sysobj.hidden.name(o.pool, o.oid), err)
	}
	return meta.ParseUserID(string(uid)), v.read, nil
}

// createHolder creates a key holder entry naming user id exclusively, with
// a version of its own for touchHolder and releaseKey to check.
func (s *Store) createHolder(ctx context.Context, o sysObj, id meta.UserID) error {
	_, err := s.sysobj.write(ctx, o, encodeAt(meta.UID(id.String()), s.release), nil, true, time.Time{}, &objv{write: newWriteVersion()})
	return err
}

// touchHolder moves an entry on by one version under v, the version read,
// so a release that read it before fails its check; it fails itself,
// ErrCanceled or ErrNotFound, once the entry has changed or gone.
func (s *Store) touchHolder(ctx context.Context, o sysObj, v meta.ObjVersion) error {
	if v.Ver == 0 {
		return fmt.Errorf("%w: index %s carries no version", op.ErrInternalError, s.sysobj.hidden.name(o.pool, o.oid))
	}
	return s.sysobj.setAttrs(ctx, o, nil, nil, false, &objv{read: v})
}

// claimKey claims key, which the write gains, for user id in the key holder
// index before the user is written. An entry naming id is the user's own,
// one a write that stopped before the user left, and is touched
// (touchHolder), so a release racing the write cannot remove it; one naming
// another user, or, with no entry, another holder keyHolderOutsideIndex
// finds, is ErrKeyExists, and a failure to find the holder is returned,
// which refuses the key. A missing entry is created exclusively, so of two
// writes claiming one key id for two users the second fails. An entry is
// never taken from another user, even one that no longer holds the key: no
// read of that user could tell it apart from one about to hold it again.
func (s *Store) claimKey(ctx context.Context, id meta.UserID, key string) error {
	o := s.keyHolderObj(key)
	for range 3 {
		holder, v, err := s.readHolder(ctx, o)
		switch {
		case err == nil && holder == id:
			err = s.touchHolder(ctx, o, v)
			if err == nil {
				return nil
			}
			if !errors.Is(err, radosclient.ErrCanceled) && !errors.Is(err, radosclient.ErrNotFound) {
				return mapUserErr(err)
			}
			s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
			continue
		case err == nil:
			return fmt.Errorf("a key of user %s is claimed for another: %w", id, op.ErrKeyExists)
		case !errors.Is(err, op.ErrNoSuchUser):
			return err
		}
		other, err := s.keyHolderOutsideIndex(ctx, key, id)
		switch {
		case err == nil:
			return fmt.Errorf("a key of user %s belongs to user %s: %w", id, other, op.ErrKeyExists)
		case !errors.Is(err, op.ErrNoSuchUser):
			return err
		}
		err = s.createHolder(ctx, o, id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, radosclient.ErrExists) {
			return mapUserErr(err)
		}
		s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	}
	return fmt.Errorf("a key of user %s: its holder entry keeps changing: %w", id, op.ErrConcurrentModification)
}

// keepClaim claims key, which user id already holds and keeps or
// deactivates, best effort: an entry naming id is touched, as claimKey
// touches it, and a missing one is created exclusively, without asking who
// else holds the key, since the user already does; an entry naming another
// user is left. Nothing here fails the write, so a revocation never waits on
// the index: a failure is logged, naming the index by its kind, and leaves
// the entry as it was.
func (s *Store) keepClaim(ctx context.Context, id meta.UserID, key string) {
	o := s.keyHolderObj(key)
	var err error
	for range 2 {
		var (
			holder meta.UserID
			v      meta.ObjVersion
		)
		holder, v, err = s.readHolder(ctx, o)
		switch {
		case err == nil && holder == id:
			err = s.touchHolder(ctx, o, v)
		case err == nil:
			return
		case errors.Is(err, op.ErrNoSuchUser):
			err = s.createHolder(ctx, o, id)
		}
		if err == nil {
			return
		}
		if !errors.Is(err, radosclient.ErrCanceled) && !errors.Is(err, radosclient.ErrNotFound) && !errors.Is(err, radosclient.ErrExists) {
			break
		}
		s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	}
	slog.WarnContext(ctx, "leaving a kept access key's holder entry as it is",
		slog.String("pool", o.pool.String()), s.sysobj.hidden.attr(o.pool, o.oid),
		slog.String("user", id.String()), slog.Any("error", err))
}

// recheckClaim reads the entry of key, which the write of user id gained,
// again once the user is written, and creates it exclusively when a
// release removed it since the claim: a release that read the entry before
// the claim's touch removes it all the same. A created entry is checked
// against the user read again past the cache, and released as releaseKey
// releases one when a later write of the user has dropped the key since,
// whose release this create came after. An entry naming
// another user, which claimed the id in between, is logged; the write
// stands, so both users then hold the id. Every failure is logged.
func (s *Store) recheckClaim(ctx context.Context, id meta.UserID, key string) {
	o := s.keyHolderObj(key)
	holder, _, err := s.readHolder(ctx, o)
	created := false
	if errors.Is(err, op.ErrNoSuchUser) {
		err = s.createHolder(ctx, o, id)
		created = err == nil
		if errors.Is(err, radosclient.ErrExists) {
			s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
			holder, _, err = s.readHolder(ctx, o)
		}
	}
	if created {
		err = s.releaseDropped(ctx, id, key)
	}
	if err == nil && holder != (meta.UserID{}) && holder != id {
		err = fmt.Errorf("the entry names user %s", holder)
	}
	if err != nil {
		slog.WarnContext(ctx, "a gained access key's holder entry does not name the user written",
			slog.String("pool", o.pool.String()), s.sysobj.hidden.attr(o.pool, o.oid),
			slog.String("user", id.String()), slog.Any("error", err))
	}
}

// releaseDropped releases key's entry, which recheckClaim created for
// user id, when the user, read past the cache, no longer holds the key.
func (s *Store) releaseDropped(ctx context.Context, id meta.UserID, key string) error {
	uo := s.userObj(id)
	s.sysobj.cache.invalidateRemove(normalName(uo.pool, uo.oid))
	rec, err := s.GetUser(ctx, id)
	switch {
	case errors.Is(err, op.ErrNoSuchUser):
		return s.releaseKey(ctx, id, key)
	case err != nil:
		return err
	}
	if _, held := rec.Info.AccessKeys[key]; held {
		return nil
	}
	return s.releaseKey(ctx, id, key)
}

// releaseKey removes key's holder entry while it names user id, under the
// version read, so an entry a write of the user touched or created since
// stays; an entry already gone, one naming another user, and one carrying
// no version, which rgw-go never writes, stay as they are.
func (s *Store) releaseKey(ctx context.Context, id meta.UserID, key string) error {
	o := s.keyHolderObj(key)
	holder, v, err := s.readHolder(ctx, o)
	switch {
	case errors.Is(err, op.ErrNoSuchUser):
		return nil
	case err != nil:
		return err
	case holder != id || v.Ver == 0:
		return nil
	}
	err = s.sysobj.remove(ctx, o, &objv{read: v})
	switch {
	case err == nil, errors.Is(err, radosclient.ErrNotFound), errors.Is(err, radosclient.ErrCanceled):
		return nil
	}
	return mapUserErr(err)
}

// claimEmail creates a new email's index object for user id exclusively,
// before the user is written, so of two writes giving one email to two
// owners the second fails. An index naming id is the user's own, one a
// write that stopped before the user left; one naming an account is
// ErrEmailExists, and so is one naming another user unless shared, whether
// or not that user still holds the email, as no read could tell one that
// dropped it from one about to hold it again. With shared, another user's
// index is left for overwriteEmail to write over after the user, under the
// version claimEmail returns. The index is written with a version of its
// own, which releaseEmail removes it under. radosgw writes the index after
// the user, without a version, and exclusively only for an exclusive put.
func (s *Store) claimEmail(ctx context.Context, id meta.UserID, email string, shared bool) (*meta.ObjVersion, error) {
	o := s.emailIndexObj(email)
	for range 2 {
		err := s.createEmailIndex(ctx, o, id)
		if err == nil {
			return nil, nil
		}
		if !errors.Is(err, radosclient.ErrExists) {
			return nil, mapUserErr(err)
		}
		holder, v, err := s.readEmailIndex(ctx, o)
		switch {
		case errors.Is(err, op.ErrNoSuchUser):
			continue
		case err != nil:
			return nil, err
		}
		switch {
		case holder.User != nil && *holder.User == id:
			return nil, nil
		case holder.User != nil && shared:
			return &v, nil
		}
		return nil, fmt.Errorf("the email of user %s is another's: %w", id, op.ErrEmailExists)
	}
	return nil, fmt.Errorf("the email of user %s: its index keeps changing: %w", id, op.ErrConcurrentModification)
}

// overwriteEmail writes the index of email, which another user sharing it
// held when claimEmail read it at v, over to user id, under v. An index
// changed or gone since is claimed again: a missing one created
// exclusively, one naming id kept, another user's written over under the
// version read then, and an account's left, which is ErrEmailExists, the
// user just written holding the email unindexed, as a departing sharer
// leaves the others. An index without a version, which radosgw writes,
// cannot be checked, and is written over as radosgw writes it.
func (s *Store) overwriteEmail(ctx context.Context, id meta.UserID, email string, v meta.ObjVersion) error {
	o := s.emailIndexObj(email)
	for range 3 {
		_, err := s.sysobj.write(ctx, o, encodeAt(meta.UID(id.String()), s.release), nil, false, time.Time{}, &objv{read: v})
		if err == nil {
			return nil
		}
		if !errors.Is(err, radosclient.ErrCanceled) && !errors.Is(err, radosclient.ErrNotFound) {
			return mapUserErr(err)
		}
		s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
		err = s.createEmailIndex(ctx, o, id)
		if err == nil {
			return nil
		}
		if !errors.Is(err, radosclient.ErrExists) {
			return mapUserErr(err)
		}
		holder, hv, err := s.readEmailIndex(ctx, o)
		switch {
		case errors.Is(err, op.ErrNoSuchUser):
			continue
		case err != nil:
			return err
		case holder.User != nil && *holder.User == id:
			return nil
		case holder.User == nil:
			slog.WarnContext(ctx, "leaving an account's email index a user sharing the email was to write over",
				slog.String("pool", o.pool.String()), s.sysobj.hidden.attr(o.pool, o.oid), slog.String("user", id.String()))
			return fmt.Errorf("the email of user %s is an account's: %w", id, op.ErrEmailExists)
		}
		v = hv
	}
	return fmt.Errorf("the email of user %s: its index keeps changing: %w", id, op.ErrConcurrentModification)
}

// createEmailIndex creates the index object o naming user id exclusively,
// with a version of its own.
func (s *Store) createEmailIndex(ctx context.Context, o sysObj, id meta.UserID) error {
	_, err := s.sysobj.write(ctx, o, encodeAt(meta.UID(id.String()), s.release), nil, true, time.Time{}, &objv{write: newWriteVersion()})
	return err
}

// readEmailIndex is the owner the index object o names, read past the
// cache, and the version it was read at. A missing index is ErrNoSuchUser.
func (s *Store) readEmailIndex(ctx context.Context, o sysObj) (meta.Owner, meta.ObjVersion, error) {
	s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	v := &objv{}
	res, err := s.sysobj.read(ctx, o, readParams{data: true, objv: v})
	if err != nil {
		return meta.Owner{}, meta.ObjVersion{}, mapUserErr(err)
	}
	d := denc.NewDecoder(res.data)
	uid := meta.DecodeUID(d)
	if err := d.Err(); err != nil {
		return meta.Owner{}, meta.ObjVersion{}, fmt.Errorf("%w: decoding index %s: %w", op.ErrInternalError, s.sysobj.hidden.name(o.pool, o.oid), err)
	}
	return meta.ParseOwner(string(uid)), v.read, nil
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
			return fmt.Errorf("a key of user %s belongs to user %s: %w", id, holder.Info.UserID, op.ErrKeyExists)
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

// releaseEmail removes email's index object while it names user id, under
// the version read, so an index another user sharing the email, or an
// account, holds, or one written over since the read, stays. An index
// without a version, which radosgw writes, is removed once it is read to
// name the user, as radosgw removes it. A missing index is no failure.
// radosgw removes the index whoever it names (remove_old_indexes, :415-417;
// remove_user_info, :584-590).
func (s *Store) releaseEmail(ctx context.Context, id meta.UserID, email string) error {
	o := s.emailIndexObj(email)
	v := &objv{}
	res, err := s.sysobj.read(ctx, o, readParams{data: true, objv: v})
	if err != nil {
		if err = mapUserErr(err); errors.Is(err, op.ErrNoSuchUser) {
			return nil
		}
		return err
	}
	d := denc.NewDecoder(res.data)
	uid := meta.DecodeUID(d)
	if d.Err() != nil {
		return fmt.Errorf("%w: decoding index %s: %w", op.ErrInternalError, s.sysobj.hidden.name(o.pool, o.oid), d.Err())
	}
	if owner := meta.ParseOwner(string(uid)); owner.User == nil || *owner.User != id {
		return nil
	}
	err = s.sysobj.remove(ctx, o, &objv{read: v.read})
	switch {
	case err == nil, errors.Is(err, radosclient.ErrNotFound), errors.Is(err, radosclient.ErrCanceled):
		return nil
	}
	return mapUserErr(err)
}

// removeOldIndexes is PutOperation::remove_old_indexes (:399-443): the
// indexes of the old record that the new one no longer has, the email's
// through releaseEmail. radosgw names an access key's index by the key's own
// id when it removes one (remove_key_index, :513-520), and by its map key
// when it writes one.
func (s *Store) removeOldIndexes(ctx context.Context, old, cur meta.UserInfo) error {
	if old.Email != "" && lowerASCII(old.Email) != lowerASCII(cur.Email) {
		if err := s.releaseEmail(ctx, old.UserID, old.Email); err != nil {
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
// rec's version when its Ver is not 0, and last the user's entries in the
// key holder index, which hold the keys until the user is gone. The account
// and group unlinks it also makes (:601-622; v20.2.4 :575-596) are the
// admin API's.
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
		if err := s.releaseEmail(ctx, info.UserID, info.Email); err != nil {
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
	if err != nil {
		return mapUserErr(err)
	}
	for _, key := range slices.Sorted(maps.Keys(info.AccessKeys)) {
		if err := s.releaseKey(ctx, info.UserID, key); err != nil {
			return err
		}
	}
	return nil
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
