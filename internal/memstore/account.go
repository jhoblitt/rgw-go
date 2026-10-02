package memstore

import (
	"context"
	"fmt"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// AddAccount stores info as an account with a fresh version and its name and
// email index entries, for test setup.
func (s *Store) AddAccount(info meta.AccountInfo) *op.AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &op.AccountRecord{
		Info:    info,
		Attrs:   map[string][]byte{},
		Version: meta.ObjVersion{Ver: 1, Tag: s.newTag()},
		Mtime:   s.now(),
	}
	if old, ok := s.accounts[info.ID]; ok {
		s.unindexAccount(old.Info)
	}
	s.accounts[info.ID] = rec
	if info.Name != "" {
		s.accountsByName[accountNameKey(info.Tenant, info.Name)] = info.ID
	}
	if info.Email != "" {
		s.emails[lowerASCII(info.Email)] = info.ID
	}
	return copyAccount(rec)
}

// GetAccount implements op.AccountStore.
func (s *Store) GetAccount(_ context.Context, id string) (*op.AccountRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.account(id)
}

// GetAccountByName implements op.AccountStore through the "name.<tenant>$<name>"
// index (account.cc:92-100 at v19.2.6).
func (s *Store) GetAccountByName(_ context.Context, tenant, name string) (*op.AccountRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.accountsByName[accountNameKey(tenant, name)]
	if !ok {
		return nil, fmt.Errorf("account name %s$%s: %w", tenant, name, op.ErrNoSuchEntity)
	}
	return s.account(id)
}

// GetAccountByEmail implements op.AccountStore through the email index users
// share. An entry a user holds names no account, as read_by_email answers
// ENOENT for an id that is not an account's (account.cc:219-240 at v19.2.6).
func (s *Store) GetAccountByEmail(_ context.Context, email string) (*op.AccountRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.emails[lowerASCII(email)]
	if !ok {
		return nil, fmt.Errorf("account email %s: %w", email, op.ErrNoSuchEntity)
	}
	return s.account(id)
}

// PutAccount implements op.AccountStore as rgwrados::account::write
// (account.cc:242-392 at v19.2.6). A name or email that differs from old's
// and that any index entry holds, this account's included, is
// ErrAccountAlreadyExists before anything is written. A write over a stored
// account bumps its version count under its tag and a create starts at 1
// with a fresh tag, as cls_version_inc does; a non-zero rec.Version must
// match the stored one. Old's index entries are dropped only where they
// still name this account.
func (s *Store) PutAccount(_ context.Context, rec *op.AccountRecord, old *meta.AccountInfo, opts op.PutAccountOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := rec.Info
	id := info.ID
	if old != nil && old.ID != id {
		return fmt.Errorf("account %s: changing the id to %s: %w", old.ID, id, op.ErrInvalidArgument)
	}
	sameName := old != nil && old.Tenant == info.Tenant && old.Name == info.Name
	sameEmail := old != nil && lowerASCII(old.Email) == lowerASCII(info.Email)
	if !sameName && info.Name != "" {
		if _, taken := s.accountsByName[accountNameKey(info.Tenant, info.Name)]; taken {
			return fmt.Errorf("account name %s$%s: %w", info.Tenant, info.Name, op.ErrAccountAlreadyExists)
		}
	}
	if !sameEmail && info.Email != "" {
		if _, taken := s.emails[lowerASCII(info.Email)]; taken {
			return fmt.Errorf("account email %s: %w", info.Email, op.ErrAccountAlreadyExists)
		}
	}
	stored, exists := s.accounts[id]
	if exists && opts.Exclusive {
		return fmt.Errorf("account %s: %w", id, op.ErrAccountAlreadyExists)
	}
	var cur meta.ObjVersion
	if exists {
		cur = stored.Version
	}
	if rec.Version.Ver != 0 && rec.Version != cur {
		return fmt.Errorf("account %s: %w", id, op.ErrConcurrentModification)
	}
	next := meta.ObjVersion{Ver: 1, Tag: s.newTag()}
	if exists {
		next = meta.ObjVersion{Ver: cur.Ver + 1, Tag: cur.Tag}
	}
	rec.Version = next
	rec.Mtime = s.now()
	s.accounts[id] = copyAccount(rec)
	if !sameName {
		if old != nil && old.Name != "" {
			if key := accountNameKey(old.Tenant, old.Name); s.accountsByName[key] == id {
				delete(s.accountsByName, key)
			}
		}
		if info.Name != "" {
			s.accountsByName[accountNameKey(info.Tenant, info.Name)] = id
		}
	}
	if !sameEmail {
		if old != nil && old.Email != "" {
			if key := lowerASCII(old.Email); s.emails[key] == id {
				delete(s.emails, key)
			}
		}
		if info.Email != "" {
			s.emails[lowerASCII(info.Email)] = id
		}
	}
	return nil
}

// RemoveAccount implements op.AccountStore as rgwrados::account::remove
// (account.cc:394-438 at v19.2.6): the account, guarded by a non-zero
// rec.Version, then the name and email entries of rec.Info and the users
// index, whoever those entries name.
func (s *Store) RemoveAccount(_ context.Context, rec *op.AccountRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rec.Info.ID
	stored, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("account %s: %w", id, op.ErrNoSuchEntity)
	}
	if rec.Version.Ver != 0 && rec.Version != stored.Version {
		return fmt.Errorf("account %s: %w", id, op.ErrConcurrentModification)
	}
	delete(s.accounts, id)
	if rec.Info.Name != "" {
		delete(s.accountsByName, accountNameKey(rec.Info.Tenant, rec.Info.Name))
	}
	if rec.Info.Email != "" {
		delete(s.emails, lowerASCII(rec.Info.Email))
	}
	delete(s.accountUsers, id)
	return nil
}

// AddAccountUser implements op.AccountStore: cls_user's account_resource_add
// without exclusive or limit (cls_user.cc:520-579 at v19.2.6), which
// overwrites an entry under the same key.
func (s *Store) AddAccountUser(_ context.Context, accountID string, info meta.UserInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	users := s.accountUsers[accountID]
	if users == nil {
		users = map[string]string{}
		s.accountUsers[accountID] = users
	}
	users[lowerASCII(info.DisplayName)] = info.UserID.ID
	return nil
}

// RemoveAccountUser implements op.AccountStore: account_resource_rm, which
// answers ENOENT for a key the index lacks (cls_user.cc:616-661 at v19.2.6).
func (s *Store) RemoveAccountUser(_ context.Context, accountID, displayName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := lowerASCII(displayName)
	if _, ok := s.accountUsers[accountID][key]; !ok {
		return fmt.Errorf("account %s user %s: %w", accountID, displayName, op.ErrNoSuchKey)
	}
	delete(s.accountUsers[accountID], key)
	return nil
}

// maxAccountResources is account_resource_list's cap on one call's entries
// (cls_user.cc:678 at v19.2.6).
const maxAccountResources = 1000

// ListAccountUsers implements op.AccountStore: account_resource_list over the
// keys after marker in order, at most maxIDs of them and never more than
// 1000, with users::list's next marker, the last key returned when more
// remain.
func (s *Store) ListAccountUsers(_ context.Context, accountID, marker string, maxIDs uint32) (ids []string, next string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := s.accountUsers[accountID]
	var keys []string
	for k := range users {
		if k > marker {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	n := min(int(maxIDs), maxAccountResources, len(keys))
	for _, k := range keys[:n] {
		ids = append(ids, users[k])
	}
	if n < len(keys) && n > 0 {
		next = keys[n-1]
	}
	return ids, next, nil
}

// AccountName implements op.AccountStore.
func (s *Store) AccountName(ctx context.Context, id string) (string, error) {
	rec, err := s.GetAccount(ctx, id)
	if err != nil {
		return "", err
	}
	return rec.Info.Name, nil
}

// account returns a copy of the account stored under id.
func (s *Store) account(id string) (*op.AccountRecord, error) {
	rec, ok := s.accounts[id]
	if !ok {
		return nil, fmt.Errorf("account %s: %w", id, op.ErrNoSuchEntity)
	}
	return copyAccount(rec), nil
}

// unindexAccount drops the name and email entries that name info's account.
func (s *Store) unindexAccount(info meta.AccountInfo) {
	if key := accountNameKey(info.Tenant, info.Name); s.accountsByName[key] == info.ID {
		delete(s.accountsByName, key)
	}
	if key := lowerASCII(info.Email); s.emails[key] == info.ID {
		delete(s.emails, key)
	}
}

// accountNameKey is get_name_key without its "name." prefix.
func accountNameKey(tenant, name string) string { return tenant + "$" + name }

func copyAccount(rec *op.AccountRecord) *op.AccountRecord {
	c := *rec
	c.Attrs = cloneAttrs(rec.Attrs)
	return &c
}
