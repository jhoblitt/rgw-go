package op

import (
	"context"
	"crypto/rand"
	"errors"

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

// addKey is RGWAccessKeyPool::add with the user write deferred
// (driver/rados/rgw_user.cc:457-777 at v19.2.6) for S3 keys: check_op's
// type and its demand for an access key unless one is generated, then
// modify_key for a key info holds and generate_key otherwise. It changes
// info; the caller writes it. Swift keys and subuser keys are
// ErrNotImplemented.
func addKey(ctx context.Context, env *Env, info *meta.UserInfo, p UserKeyParams) error {
	typ := p.Type
	if typ == KeyTypeUndefined {
		typ = KeyTypeS3
		if p.Subuser != "" {
			typ = KeyTypeSwift
		}
	}
	if typ != KeyTypeS3 || p.Subuser != "" {
		return ErrNotImplemented
	}
	if !p.genAccess() && p.AccessKey == "" {
		return ErrInvalidAccessKeyID
	}
	if k, ok := info.AccessKeys[p.AccessKey]; ok && p.AccessKey != "" {
		secret := p.SecretKey
		if p.genSecret() {
			secret = genSecretKey()
		}
		if secret != "" {
			k.Secret = secret
		}
		if p.Active != nil {
			k.Active = *p.Active
		}
		info.AccessKeys[p.AccessKey] = k
		return nil
	}
	id := p.AccessKey
	if id != "" {
		if _, err := env.Users.GetUserByAccessKey(ctx, id); err == nil {
			return ErrKeyExists
		}
	}
	secret := p.SecretKey
	if p.genSecret() {
		secret = genSecretKey()
	} else if secret == "" {
		return ErrInvalidSecretKey
	}
	if p.genAccess() {
		var err error
		if id, err = genAccessKey(ctx, env); err != nil {
			return err
		}
	}
	k := meta.NewAccessKey()
	k.ID, k.Secret, k.CreatedAt = id, secret, meta.Time{Time: env.Clock()}
	if info.AccessKeys == nil {
		info.AccessKeys = map[string]meta.AccessKey{}
	}
	info.AccessKeys[id] = k
	return nil
}
