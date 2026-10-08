package op

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The generated key lengths, PUBLIC_ID_LEN and SECRET_KEY_LEN
// (driver/rados/rgw_user.h:23-24 at v19.2.6, :22-23 at v20.2.4).
const (
	accessKeyLen = 20
	secretKeyLen = 40
)

// The tables gen_rand_alphanumeric_upper and gen_rand_alphanumeric_plain
// draw from (src/common/random_string.cc at v19.2.6 and v20.2.4).
const (
	upperAlnum = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	plainAlnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// randomFrom is choose_from: n random bytes, each taken as the char it is,
// signed on the platforms radosgw ships on, then cast to unsigned and
// reduced into table.
func randomFrom(table string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	for i, c := range b {
		pos := uint32(int32(int8(c)))        //nolint:gosec // static_cast<unsigned>(char) sign-extends as this does
		b[i] = table[pos%uint32(len(table))] //nolint:gosec // the tables are 36 and 62 bytes long
	}
	return string(b)
}

// genSecretKey is rgw_generate_secret_key (driver/rados/rgw_user.cc:500-506
// at v19.2.6).
func genSecretKey() string { return randomFrom(plainAlnum, secretKeyLen) }

// genAccessKey is rgw_generate_access_key (:508-533): ids are drawn until
// one no user holds. A lookup that fails otherwise is generate_key's
// ERR_INVALID_ACCESS_KEY.
func genAccessKey(ctx context.Context, env *Env) (string, error) {
	for {
		id := randomFrom(upperAlnum, accessKeyLen)
		_, err := env.Users.GetUserByAccessKey(ctx, id)
		if errors.Is(err, ErrNoSuchUser) {
			return id, nil
		}
		if err != nil {
			return "", ErrInvalidAccessKeyID
		}
	}
}

// keyType is the type RGWAccessKeyPool::check_op settles on
// (driver/rados/rgw_user.cc:474-483 at v19.2.6): an undefined type is Swift
// when a subuser is named, S3 otherwise.
func (p UserKeyParams) keyType() KeyType {
	if p.Type != KeyTypeUndefined {
		return p.Type
	}
	if p.hasSubuser() {
		return KeyTypeSwift
	}
	return KeyTypeS3
}

// swiftKeyID is build_default_swift_kid (:352-363): "<uid>:<subuser>", or ""
// without either.
func swiftKeyID(uid meta.UserID, p UserKeyParams) string {
	if uid.ID == "" || p.Subuser == "" {
		return ""
	}
	return uid.String() + ":" + p.Subuser
}

// keyCheck is the rest of check_op, for a key of type typ: an S3 key needs
// an access key unless one is generated. It then is check_existing_key
// (:394-455), which finds a Swift key by "<uid>:<subuser>" alone and an S3
// key by the access key, both among info's own keys, and returns the id it
// found.
func keyCheck(info *meta.UserInfo, p UserKeyParams, typ KeyType) (id string, existing bool, err error) {
	if typ == KeyTypeS3 && !p.genAccess() && p.AccessKey == "" {
		return "", false, ErrInvalidAccessKeyID
	}
	swiftID := swiftKeyID(info.UserID, p)
	if p.AccessKey == "" && swiftID == "" {
		return "", false, nil
	}
	switch typ {
	case KeyTypeSwift:
		_, existing = info.SwiftKeys[swiftID]
		return swiftID, existing, nil
	case KeyTypeS3:
		_, existing = info.AccessKeys[p.AccessKey]
		return p.AccessKey, existing, nil
	}
	return "", false, nil
}

// addKey is RGWAccessKeyPool::add with the user write deferred
// (driver/rados/rgw_user.cc:457-777 at v19.2.6, :462-782 at v20.2.4):
// check_op, then modify_key for a key info holds and generate_key
// otherwise. A modify that leaves an S3 key active refuses an id another
// user also holds, active or not (refuseHeldKey); one that leaves it
// inactive goes through, as a removal does, since neither can put a second
// user on the id. It changes info; the caller writes it.
func addKey(ctx context.Context, env *Env, info *meta.UserInfo, p UserKeyParams) error {
	typ := p.keyType()
	id, existing, err := keyCheck(info, p, typ)
	if err != nil {
		return err
	}
	if existing {
		active := info.AccessKeys[id].Active
		if p.Active != nil {
			active = *p.Active
		}
		if typ == KeyTypeS3 && active {
			if err := refuseHeldKey(ctx, env, info.UserID, id); err != nil {
				return err
			}
		}
		return modifyKey(info, p, typ, id)
	}
	return generateKey(ctx, env, info, p, typ)
}

// generateKey is generate_key (:536-639). An explicit S3 id another user
// holds is ErrKeyExists, active or not (refuseHeldKey), and a failed read
// refuses the key too, where radosgw goes on. The Swift index is no UserStore read, so the
// explicit id of a Swift request, which generate_key checks and then
// replaces, goes unchecked, and "<uid>:<subuser>" is checked by PutUser.
// The key records the subuser named. A request that names active=false
// gets an inactive key; radosgw's generate_key never reads
// access_key_active and makes it active (docs/exclusions.md).
func generateKey(ctx context.Context, env *Env, info *meta.UserInfo, p UserKeyParams, typ KeyType) error {
	var id string
	if !p.genAccess() {
		id = p.AccessKey
	}
	if id != "" && typ == KeyTypeS3 {
		_, err := env.Users.GetUserByAccessKey(ctx, id)
		switch {
		case err == nil:
			return ErrKeyExists
		case !errors.Is(err, ErrNoSuchUser):
			return storeErr(err)
		}
		if err := refuseHeldKey(ctx, env, info.UserID, id); err != nil {
			return err
		}
	}
	k := meta.NewAccessKey()
	if p.hasSubuser() {
		k.Subuser = p.Subuser
	}
	if p.genSecret() {
		k.Secret = genSecretKey()
	} else if k.Secret = p.SecretKey; k.Secret == "" {
		return ErrInvalidSecretKey
	}
	if typ == KeyTypeS3 && p.genAccess() {
		var err error
		if id, err = genAccessKey(ctx, env); err != nil {
			return err
		}
	}
	if typ == KeyTypeSwift {
		if id = swiftKeyID(info.UserID, p); id == "" {
			return ErrInvalidAccessKeyID
		}
	}
	k.ID, k.CreatedAt = id, meta.Time{Time: env.Clock()}
	if p.Active != nil {
		k.Active = *p.Active
	}
	switch typ {
	case KeyTypeS3:
		info.AccessKeys = emplace(info.AccessKeys, id, k)
	case KeyTypeSwift:
		info.SwiftKeys = emplace(info.SwiftKeys, id, k)
	}
	return nil
}

// refuseHeldKey is ErrKeyExists when a user other than self holds id among
// its access keys, active or not. The key index names active keys only
// (svc_user_rados.cc:329-339 and :424-432 at v19.2.6, :309-319 and
// :404-412 at v20.2.4), so radosgw lets a second user take a deactivated
// key's id, and the index then authenticates whichever user it names
// (docs/ceph-upstream-bugs.md, "radosgw lets another user take an inactive
// access key's id"). No user store read finds a key by id outside the
// index, so the users are read one by one through the metadata listing,
// every page of it: one GetUser per stored user. The index check stands
// alone where the users cannot be listed: an Env without a MetadataStore,
// a listing that answers ErrNotImplemented, or ErrNotFound, a listing with
// no user section. A
// user gone between the listing and its read, ErrNoSuchUser, is skipped;
// any other failure refuses the key, as storeErr's error.
func refuseHeldKey(ctx context.Context, env *Env, self meta.UserID, id string) error {
	if env.Metadata == nil {
		return nil
	}
	marker := ""
	for {
		keys, next, more, err := env.Metadata.List(ctx, "user", marker, maxListUsers)
		switch {
		case errors.Is(err, ErrNotImplemented), errors.Is(err, ErrNotFound):
			return nil
		case err != nil:
			return storeErr(err)
		}
		for _, k := range keys {
			uid := meta.ParseUserID(k)
			if uid == self {
				continue
			}
			rec, err := env.Users.GetUser(ctx, uid)
			if errors.Is(err, ErrNoSuchUser) {
				continue
			}
			if err != nil {
				return storeErr(err)
			}
			if _, held := rec.Info.AccessKeys[id]; held {
				return ErrKeyExists
			}
		}
		if !more || len(keys) == 0 {
			return nil
		}
		marker = next
	}
}

// emplace is std::map::emplace: k goes in under id unless id is there.
func emplace(m map[string]meta.AccessKey, id string, k meta.AccessKey) map[string]meta.AccessKey {
	if m == nil {
		m = map[string]meta.AccessKey{}
	}
	if _, ok := m[id]; !ok {
		m[id] = k
	}
	return m
}

// modifyKey is modify_key (:642-710) for the key id info holds, changed in
// place: a new secret when one is named or generated, the active flag only
// when the request names it, and the creation date kept. radosgw rebuilds a
// Swift key from its id and subuser alone, which reactivates a deactivated
// key, drops its creation date and can leave it without a secret
// (docs/ceph-upstream-bugs.md, "radosgw's Swift key modify rebuilds the
// key"); rgw-go keeps the stored key instead, and refuses a change that
// would leave any key without a secret as generate_key refuses an empty
// one, ERR_INVALID_SECRET_KEY (:589-592).
func modifyKey(info *meta.UserInfo, p UserKeyParams, typ KeyType, id string) error {
	var k meta.AccessKey
	switch typ {
	case KeyTypeS3:
		k = info.AccessKeys[id]
	case KeyTypeSwift:
		k = info.SwiftKeys[id]
	default:
		return ErrInvalidKeyType
	}
	switch {
	case p.genSecret():
		k.Secret = genSecretKey()
	case p.SecretKey != "":
		k.Secret = p.SecretKey
	}
	if k.Secret == "" {
		return ErrInvalidSecretKey
	}
	if p.Active != nil {
		k.Active = *p.Active
	}
	if typ == KeyTypeS3 {
		info.AccessKeys[id] = k
	} else {
		info.SwiftKeys[id] = k
	}
	return nil
}

// removeKey is RGWAccessKeyPool::remove with the write deferred (:779-853):
// check_op, then the key info holds is removed; one it does not hold is
// ErrInvalidAccessKeyID. Only info's own keys are looked at.
func removeKey(info *meta.UserInfo, p UserKeyParams) error {
	typ := p.keyType()
	id, existing, err := keyCheck(info, p, typ)
	if err != nil {
		return err
	}
	if !existing {
		return ErrInvalidAccessKeyID
	}
	if typ == KeyTypeS3 {
		delete(info.AccessKeys, id)
	} else {
		delete(info.SwiftKeys, id)
	}
	return nil
}

// subuserOf is RGWUserAdminOpState::set_subuser (rgw_user.cc:281-299 at
// v19.2.6, :286-304 at v20.2.4) over the op's uid: a subuser
// "<user>:<name>" names its user too, whose id replaces uid's, or all of
// uid when it carries a tenant. set is subuser_specified, which a name
// left empty past the ':' still sets.
func subuserOf(uid meta.UserID, s string) (_ meta.UserID, name string, set bool) {
	if s == "" {
		return uid, "", false
	}
	user, name, ok := strings.Cut(s, ":")
	if !ok {
		return uid, s, true
	}
	if id := meta.ParseUserID(user); id.Tenant == "" {
		uid.ID = id.ID
	} else {
		uid = id
	}
	return uid, name, true
}

// subuserKey is the key part of a key or subuser op with its subuser, as
// set_subuser leaves the op state: the uid it names and the parameters.
func subuserKey(uid meta.UserID, subuser string, p UserKeyParams) (meta.UserID, UserKeyParams) {
	uid, p.Subuser, p.subuserSet = subuserOf(opUserID(uid), subuser)
	return uid, p
}

// keyOpUser is RGWUser::init for the key, subuser, caps and quota ops,
// then init_members and their has_existing_user check: the anonymous uid
// with no user found is keys.init's EINVAL, which comes first
// (driver/rados/rgw_user.cc:247-264 and :1461-1478 at v19.2.6), and any
// other uid with none found is ERR_NO_SUCH_USER.
func keyOpUser(ctx context.Context, env *Env, uid meta.UserID, accessKey string, swift bool) (*UserRecord, error) {
	l, err := lookupUser(ctx, env, uid, "", accessKey, swift)
	switch {
	case err != nil:
		return nil, err
	case l.rec != nil:
		return l.rec, nil
	case uid == anonymousUser:
		return nil, ErrInvalidArgument
	}
	return nil, ErrNoSuchUser
}

// putUser is RGWUser::update for an op that changes neither the account,
// the path nor the display name: rec written against the version it was
// read at, every error storeErr's.
func putUser(ctx context.Context, env *Env, rec *UserRecord) error {
	old := rec.Info
	return writeUser(ctx, env, rec, &old, ErrBucketAlreadyExists)
}

// CreateKey is RGWOp_Key_Create and RGWUserAdminOp_Key::create
// (driver/rados/rgw_rest_user.cc:666-726, driver/rados/rgw_user.cc:2586-2625
// at v19.2.6). Cap users=write.
type CreateKey struct {
	AdminOp
	UID meta.UserID
	// Key.Subuser is the subuser argument as the request names it,
	// "<uid>:<name>" included.
	Key    UserKeyParams
	Result meta.UserInfo
	// Type is the type check_op settled on, which picks the keys the
	// response lists.
	Type KeyType
}

// NewCreateKey returns a CreateKey with generate-key's default, true, and
// no key type named.
func NewCreateKey() *CreateKey {
	return &CreateKey{Key: UserKeyParams{Type: KeyTypeUndefined, GenerateKey: true}}
}

// Name is create_access_key.
func (o *CreateKey) Name() string { return "create_access_key" }

// VerifyPermission is RGWOp_Key_Create::check_caps.
func (o *CreateKey) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init, which with no user by uid finds one by the
// access key, then keys.add and the user's write.
func (o *CreateKey) Execute(ctx context.Context, r *Request) error {
	uid, key := subuserKey(o.UID, o.Key.Subuser, o.Key)
	rec, err := keyOpUser(ctx, r.Env, uid, key.AccessKey, key.Type == KeyTypeSwift)
	if err != nil {
		return err
	}
	o.Type = key.keyType()
	if err := addKey(ctx, r.Env, &rec.Info, key); err != nil {
		return err
	}
	if err := putUser(ctx, r.Env, rec); err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// RemoveKey is RGWOp_Key_Remove and RGWUserAdminOp_Key::remove
// (driver/rados/rgw_rest_user.cc:728-773, driver/rados/rgw_user.cc:2627-2648
// at v19.2.6). Cap users=write.
type RemoveKey struct {
	AdminOp
	UID meta.UserID
	// Key.Subuser is the subuser argument as the request names it.
	Key UserKeyParams
}

// NewRemoveKey returns a RemoveKey with no key type named.
func NewRemoveKey() *RemoveKey { return &RemoveKey{Key: UserKeyParams{Type: KeyTypeUndefined}} }

// Name is remove_access_key.
func (o *RemoveKey) Name() string { return "remove_access_key" }

// VerifyPermission is RGWOp_Key_Remove::check_caps.
func (o *RemoveKey) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init, keys.remove and the user's write.
func (o *RemoveKey) Execute(ctx context.Context, r *Request) error {
	uid, key := subuserKey(o.UID, o.Key.Subuser, o.Key)
	rec, err := keyOpUser(ctx, r.Env, uid, key.AccessKey, key.Type == KeyTypeSwift)
	if err != nil {
		return err
	}
	if err := removeKey(&rec.Info, key); err != nil {
		return err
	}
	return putUser(ctx, r.Env, rec)
}

// subuserCheck is RGWSubUserPool::check_op (driver/rados/rgw_user.cc:963-1002
// at v19.2.6): a name, a valid permission, and a key type, Swift unless one
// is named. It reports whether info holds the subuser.
func subuserCheck(info *meta.UserInfo, key *UserKeyParams, perm uint32) (existing bool, err error) {
	if key.Subuser == "" {
		return false, ErrInvalidArgument
	}
	if perm == meta.PermInvalid {
		return false, ErrInvalidArgument
	}
	if key.Type == KeyTypeUndefined {
		key.Type = KeyTypeSwift
	}
	_, existing = info.SubUsers[key.Subuser]
	return existing, nil
}

// CreateSubuser is RGWOp_Subuser_Create and RGWUserAdminOp_Subuser::create
// (driver/rados/rgw_rest_user.cc:481-554, driver/rados/rgw_user.cc:2495-2528
// at v19.2.6) over RGWSubUserPool::add (:1004-1085). Cap users=write.
type CreateSubuser struct {
	AdminOp
	UID meta.UserID
	// Subuser is the argument as the request names it, "<uid>:<name>"
	// included.
	Subuser string
	// Perm is the access argument: "", read, write, readwrite or full.
	Perm string
	// Key carries access-key, secret-key, key-type (Swift by default),
	// gen-access-key as GenAccess and generate-secret as GenSecret.
	Key    UserKeyParams
	Result meta.UserInfo
}

// NewCreateSubuser returns a CreateSubuser with key-type's default, Swift.
func NewCreateSubuser() *CreateSubuser { return &CreateSubuser{Key: UserKeyParams{Type: KeyTypeSwift}} }

// Name is create_subuser.
func (o *CreateSubuser) Name() string { return "create_subuser" }

// VerifyPermission is RGWOp_Subuser_Create::check_caps.
func (o *CreateSubuser) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init, then RGWSubUserPool::add: check_op; an S3 key
// with no access key and a key with no secret are generated, so a key is
// always added or modified; then the subuser goes in as std::map::insert
// puts it, so a subuser that exists keeps its permission; then the write.
func (o *CreateSubuser) Execute(ctx context.Context, r *Request) error {
	uid, key := subuserKey(o.UID, o.Subuser, o.Key)
	rec, err := keyOpUser(ctx, r.Env, uid, key.AccessKey, key.Type == KeyTypeSwift)
	if err != nil {
		return err
	}
	info := &rec.Info
	perm := meta.ParseSubuserPerm(o.Perm)
	if _, err := subuserCheck(info, &key, perm); err != nil {
		return err
	}
	if key.Type == KeyTypeS3 && key.AccessKey == "" {
		key.GenAccess = true
	}
	if key.SecretKey == "" {
		key.GenSecret = true
	}
	if err := addKey(ctx, r.Env, info, key); err != nil {
		return err
	}
	if _, ok := info.SubUsers[key.Subuser]; !ok {
		if info.SubUsers == nil {
			info.SubUsers = map[string]meta.SubUser{}
		}
		info.SubUsers[key.Subuser] = meta.SubUser{Name: key.Subuser, Perm: perm}
	}
	if err := putUser(ctx, r.Env, rec); err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// ModifySubuser is RGWOp_Subuser_Modify and RGWUserAdminOp_Subuser::modify
// (driver/rados/rgw_rest_user.cc:556-621, driver/rados/rgw_user.cc:2530-2561
// at v19.2.6) over RGWSubUserPool::modify (:1151-1222). Cap users=write.
type ModifySubuser struct {
	AdminOp
	UID     meta.UserID
	Subuser string
	// Perm is the access argument, nil to leave the permission. radosgw's
	// body always sets it, an absent access as no permission.
	Perm *string
	// Key carries secret-key, key-type (Swift by default) and
	// generate-secret as GenSecret.
	Key    UserKeyParams
	Result meta.UserInfo
}

// NewModifySubuser returns a ModifySubuser with key-type's default, Swift.
func NewModifySubuser() *ModifySubuser { return &ModifySubuser{Key: UserKeyParams{Type: KeyTypeSwift}} }

// Name is modify_subuser.
func (o *ModifySubuser) Name() string { return "modify_subuser" }

// VerifyPermission is RGWOp_Subuser_Modify::check_caps.
func (o *ModifySubuser) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init, then check_op and execute_modify: a subuser info
// lacks is ErrNoSuchSubUser; the key is added or modified when a secret or
// generate-secret is named; the permission is set when given; then the
// write.
func (o *ModifySubuser) Execute(ctx context.Context, r *Request) error {
	uid, key := subuserKey(o.UID, o.Subuser, o.Key)
	rec, err := keyOpUser(ctx, r.Env, uid, key.AccessKey, key.Type == KeyTypeSwift)
	if err != nil {
		return err
	}
	info := &rec.Info
	perm := uint32(0)
	if o.Perm != nil {
		perm = meta.ParseSubuserPerm(*o.Perm)
	}
	existing, err := subuserCheck(info, &key, perm)
	if err != nil {
		return err
	}
	if !existing {
		return ErrNoSuchSubUser
	}
	su := info.SubUsers[key.Subuser]
	if key.hasKeyOp() {
		if err := addKey(ctx, r.Env, info, key); err != nil {
			return err
		}
	}
	if o.Perm != nil {
		su.Perm = perm
	}
	info.SubUsers[key.Subuser] = su
	if err := putUser(ctx, r.Env, rec); err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// RemoveSubuser is RGWOp_Subuser_Remove and RGWUserAdminOp_Subuser::remove
// (driver/rados/rgw_rest_user.cc:623-664, driver/rados/rgw_user.cc:2563-2584
// at v19.2.6) over RGWSubUserPool::remove (:1087-1149). Cap users=write.
type RemoveSubuser struct {
	AdminOp
	UID     meta.UserID
	Subuser string
	// PurgeKeys is purge-keys, true by default; execute_remove purges the
	// subuser's keys whatever it says.
	PurgeKeys bool
}

// NewRemoveSubuser returns a RemoveSubuser with purge-keys' default.
func NewRemoveSubuser() *RemoveSubuser { return &RemoveSubuser{PurgeKeys: true} }

// Name is remove_subuser.
func (o *RemoveSubuser) Name() string { return "remove_subuser" }

// VerifyPermission is RGWOp_Subuser_Remove::check_caps.
func (o *RemoveSubuser) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init, check_op and execute_remove: a subuser info
// lacks is ErrNoSuchSubUser; remove_subuser_keys (:856-923) removes the
// Swift key "<uid>:<name>" and every S3 key recorded for the subuser, the
// user's own keys and other subusers' untouched; then the subuser goes and
// the user is written.
func (o *RemoveSubuser) Execute(ctx context.Context, r *Request) error {
	uid, key := subuserKey(o.UID, o.Subuser, UserKeyParams{Type: KeyTypeUndefined})
	rec, err := keyOpUser(ctx, r.Env, uid, "", false)
	if err != nil {
		return err
	}
	info := &rec.Info
	existing, err := subuserCheck(info, &key, 0)
	if err != nil {
		return err
	}
	if !existing {
		return ErrNoSuchSubUser
	}
	delete(info.SwiftKeys, swiftKeyID(info.UserID, key))
	for id, k := range info.AccessKeys {
		if k.Subuser == key.Subuser {
			delete(info.AccessKeys, id)
		}
	}
	delete(info.SubUsers, key.Subuser)
	return putUser(ctx, r.Env, rec)
}

// AddCaps is RGWOp_Caps_Add and RGWUserAdminOp_Caps::add
// (driver/rados/rgw_rest_user.cc:775-811, driver/rados/rgw_user.cc:2650-2683
// at v19.2.6) over RGWUserCapPool::add (:1263-1297). Cap users=write, and
// nothing more: radosgw lets a users=write caller grant any cap, to any
// user and to itself, which makes users=write a superuser cap; rgw-go
// keeps that design.
type AddCaps struct {
	AdminOp
	UID meta.UserID
	// Caps is the user-caps argument, ';'-separated.
	Caps   string
	Result meta.Caps
}

// NewAddCaps returns an AddCaps.
func NewAddCaps() *AddCaps { return &AddCaps{} }

// Name is add_user_caps.
func (o *AddCaps) Name() string { return "add_user_caps" }

// VerifyPermission is RGWOp_Caps_Add::check_caps.
func (o *AddCaps) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWUser::init by uid, then RGWUserCapPool::add: an empty list
// or one add_from_string refuses is ErrInvalidCapability and writes
// nothing; then the write.
func (o *AddCaps) Execute(ctx context.Context, r *Request) error {
	return o.change(ctx, r, func(c *meta.Caps) error { return c.AddString(o.Caps) })
}

// change runs one caps change on the user o.UID names and writes it.
func (o *AddCaps) change(ctx context.Context, r *Request, fn func(*meta.Caps) error) error {
	rec, err := keyOpUser(ctx, r.Env, opUserID(o.UID), "", false)
	if err != nil {
		return err
	}
	if o.Caps == "" {
		return ErrInvalidCapability
	}
	if err := fn(&rec.Info.Caps); err != nil {
		return ErrInvalidCapability
	}
	if err := putUser(ctx, r.Env, rec); err != nil {
		return err
	}
	o.Result = rec.Info.Caps
	return nil
}

// RemoveCaps is RGWOp_Caps_Remove and RGWUserAdminOp_Caps::remove
// (driver/rados/rgw_rest_user.cc:813-849, driver/rados/rgw_user.cc:2685-2718
// at v19.2.6) over RGWUserCapPool::remove (:1305-1339). Cap users=write.
type RemoveCaps struct{ AddCaps }

// NewRemoveCaps returns a RemoveCaps.
func NewRemoveCaps() *RemoveCaps { return &RemoveCaps{} }

// Name is remove_user_caps.
func (o *RemoveCaps) Name() string { return "remove_user_caps" }

// Execute is RGWUserCapPool::remove's counterpart of AddCaps.Execute.
func (o *RemoveCaps) Execute(ctx context.Context, r *Request) error {
	return o.change(ctx, r, func(c *meta.Caps) error { return c.RemoveString(o.Caps) })
}

// validQuotaType is the quota-type check RGWOp_Quota_Info::execute and
// RGWOp_Quota_Set::execute make (driver/rados/rgw_rest_user.cc at v19.2.6
// and v20.2.4).
func validQuotaType(t string) bool { return t == "" || t == "user" || t == "bucket" }

// GetUserQuota is RGWOp_Quota_Info (driver/rados/rgw_rest_user.cc:871-941 at
// v19.2.6). Cap users=read.
type GetUserQuota struct {
	AdminOp
	UID meta.UserID
	// QuotaType is "", "user" or "bucket".
	QuotaType string
	Result    meta.UserInfo
}

// NewGetUserQuota returns a GetUserQuota.
func NewGetUserQuota() *GetUserQuota { return &GetUserQuota{} }

// Name is get_quota_info.
func (o *GetUserQuota) Name() string { return "get_quota_info" }

// VerifyPermission is RGWOp_Quota_Info::check_caps.
func (o *GetUserQuota) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapRead)
}

// Execute refuses an empty uid and an unknown quota type with EINVAL, then
// is RGWUser::init by uid.
func (o *GetUserQuota) Execute(ctx context.Context, r *Request) error {
	if o.UID == (meta.UserID{}) || !validQuotaType(o.QuotaType) {
		return ErrInvalidArgument
	}
	rec, err := keyOpUser(ctx, r.Env, opUserID(o.UID), "", false)
	if err != nil {
		return err
	}
	o.Result = rec.Info
	return nil
}

// QuotaParams are the arguments RGWOp_Quota_Set reads when the request has
// no body (driver/rados/rgw_rest_user.cc:1103-1111 at v19.2.6), each nil
// when the request does not name it.
type QuotaParams struct {
	MaxObjects, MaxSize *int64
	// MaxSizeKB is max-size-kb, which replaces MaxSize with itself * 1024.
	MaxSizeKB *int64
	Enabled   *bool
}

// quotaInputMaxLen is QUOTA_INPUT_MAX_LEN, the longest body a quota set
// reads.
const quotaInputMaxLen = 1024

// SetUserQuota is RGWOp_Quota_Set (driver/rados/rgw_rest_user.cc:943-1127 at
// v19.2.6). Cap users=write.
type SetUserQuota struct {
	AdminOp
	UID meta.UserID
	// QuotaType is "", both quotas, "user" or "bucket".
	QuotaType string
	// Params are used when the request carries no body, or, for one quota
	// type, an empty chunked one.
	Params QuotaParams
	// UserQuota and BucketQuota are the quotas written, nil for one left
	// alone.
	UserQuota, BucketQuota *meta.Quota
}

// NewSetUserQuota returns a SetUserQuota.
func NewSetUserQuota() *SetUserQuota { return &SetUserQuota{} }

// Name is set_quota_info.
func (o *SetUserQuota) Name() string { return "set_quota_info" }

// VerifyPermission is RGWOp_Quota_Set::check_caps.
func (o *SetUserQuota) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "users", meta.CapWrite)
}

// Execute is RGWOp_Quota_Set::execute: an empty uid, an unknown quota type,
// and both quotas asked for without a body, a request whose Content-Length
// is 0 and that is not chunked, are EINVAL before RGWUser::init by uid. Then
// the body, read only once the user is found: UserQuotas for both quotas,
// one RGWQuotaInfo otherwise, each at most 1024 bytes. For one quota an
// empty chunked body falls back to the arguments, which start from
// RGWQuotaInfo's defaults, so check_on_raw is false, and default to the
// current values. A maximum size below 0 other than -1 is refused with
// ErrInvalidArgument, which radosgw stores. RGWUser::modify writes the
// user, refusing an account user whose display name is no IAM user name,
// as execute_modify does.
func (o *SetUserQuota) Execute(ctx context.Context, r *Request) error {
	env := r.Env
	if o.UID == (meta.UserID{}) || !validQuotaType(o.QuotaType) {
		return ErrInvalidArgument
	}
	useParams := r.ContentLength == 0
	if useParams && o.QuotaType == "" {
		return ErrInvalidArgument
	}
	uid := opUserID(o.UID)
	rec, err := keyOpUser(ctx, env, uid, "", false)
	if err != nil {
		return err
	}
	info := &rec.Info
	o.UserQuota, o.BucketQuota = nil, nil
	var body []byte
	if !useParams {
		if body, err = ReadParamBody(r, quotaInputMaxLen); err != nil {
			return err
		}
		useParams = len(body) == 0 && o.QuotaType != ""
	}
	switch {
	case o.QuotaType == "":
		if o.UserQuota, o.BucketQuota, err = decodeUserQuotas(body); err != nil {
			return err
		}
	case useParams:
		cur := info.UserQuota
		if o.QuotaType == "bucket" {
			cur = info.BucketQuota
		}
		q, err := o.Params.apply(cur)
		if err != nil {
			return err
		}
		o.setOne(q)
	default:
		var q meta.Quota
		if err := decodeQuotaBody(body, &q); err != nil {
			return err
		}
		o.setOne(q)
	}
	for _, q := range []*meta.Quota{o.UserQuota, o.BucketQuota} {
		if q != nil && !validSizeLimit(q.MaxSize) {
			return ErrInvalidArgument
		}
	}
	if err := checkFound(uid, userLookup{rec: rec}); err != nil {
		return err
	}
	if o.UserQuota != nil {
		info.UserQuota = *o.UserQuota
	}
	if o.BucketQuota != nil {
		info.BucketQuota = *o.BucketQuota
	}
	if info.AccountID != "" && !validIAMUserName(info.DisplayName) {
		return ErrInvalidArgument
	}
	return putUser(ctx, env, rec)
}

// setOne records q as the quota QuotaType names.
func (o *SetUserQuota) setOne(q meta.Quota) {
	if o.QuotaType == "bucket" {
		o.BucketQuota = &q
	} else {
		o.UserQuota = &q
	}
}

// validSizeLimit reports whether n may be stored as a maximum size: not
// negative, or -1, the "no limit" radosgw writes. radosgw stores any value,
// so a negative size from a wrapped product or a stray sign would lift the
// limit (docs/exclusions.md).
func validSizeLimit(n int64) bool { return n >= 0 || n == -1 }

// apply is the arguments over RGWQuotaInfo's defaults, each absent one
// cur's value. A max-size-kb whose size in bytes overflows int64, which is
// undefined behavior in radosgw's multiplication, is ErrInvalidArgument.
func (p QuotaParams) apply(cur meta.Quota) (meta.Quota, error) {
	q := meta.Quota{MaxObjects: cur.MaxObjects, MaxSize: cur.MaxSize, Enabled: cur.Enabled}
	if p.MaxObjects != nil {
		q.MaxObjects = *p.MaxObjects
	}
	if p.MaxSize != nil {
		q.MaxSize = *p.MaxSize
	}
	if p.MaxSizeKB != nil {
		kb := *p.MaxSizeKB
		if kb > math.MaxInt64/1024 || kb < math.MinInt64/1024 {
			return meta.Quota{}, ErrInvalidArgument
		}
		q.MaxSize = kb * 1024
	}
	if p.Enabled != nil {
		q.Enabled = *p.Enabled
	}
	return q, nil
}

// firstJSONValue is JSONParser::parse over a body (src/common/ceph_json.cc
// at v19.2.6 and v20.2.4): the first JSON value, json_spirit leaving any
// text after an object, an array or a string unread, while a lone number,
// true, false or null must be the whole body. An empty body or one that is
// not JSON is get_json_input's EINVAL.
func firstJSONValue(body []byte) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&raw); err != nil {
		return nil, ErrInvalidArgument
	}
	if c := raw[0]; c != '{' && c != '[' && c != '"' && !bytes.Equal(raw, body) {
		return nil, ErrInvalidArgument
	}
	return raw, nil
}

// decodeQuotaBody is get_json_input into one RGWQuotaInfo.
func decodeQuotaBody(body []byte, q *meta.Quota) error {
	raw, err := firstJSONValue(body)
	if err != nil {
		return err
	}
	if err := q.UnmarshalJSON(raw); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

// decodeUserQuotas is get_json_input into UserQuotas
// (driver/rados/rgw_rest_user.cc:851-869 at v19.2.6): bucket_quota and
// user_quota, each RGWQuotaInfo's defaults when absent, as decode_json
// resets an absent member.
func decodeUserQuotas(body []byte) (user, bucket *meta.Quota, err error) {
	raw, err := firstJSONValue(body)
	if err != nil {
		return nil, nil, err
	}
	m, err := meta.JSONMembers(raw)
	if err != nil {
		return nil, nil, ErrInvalidArgument
	}
	quota := func(name string) (*meta.Quota, error) {
		q := meta.Quota{MaxSize: -1, MaxObjects: -1}
		if v, ok := m[name]; ok && q.UnmarshalJSON(v.Raw) != nil {
			return nil, ErrInvalidArgument
		}
		return &q, nil
	}
	if bucket, err = quota("bucket_quota"); err != nil {
		return nil, nil, err
	}
	if user, err = quota("user_quota"); err != nil {
		return nil, nil, err
	}
	return user, bucket, nil
}
