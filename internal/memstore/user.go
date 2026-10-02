package memstore

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// AddUser stores info as a user with a fresh version and index entries, for
// test setup; an access key's empty ID is filled from its map key.
func (s *Store) AddUser(info meta.UserInfo) *op.UserRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	info = cloneUserInfo(info)
	for id, k := range info.AccessKeys {
		if k.ID == "" {
			k.ID = id
			info.AccessKeys[id] = k
		}
	}
	rec := &op.UserRecord{
		Info:    info,
		Attrs:   map[string][]byte{},
		Version: meta.ObjVersion{Ver: 1, Tag: s.newTag()},
		Mtime:   s.now(),
	}
	s.storeUser(rec)
	return copyUser(rec)
}

// GetUser implements op.UserStore.
func (s *Store) GetUser(_ context.Context, id meta.UserID) (*op.UserRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.user(id.String())
}

// GetUserByAccessKey implements op.UserStore.
func (s *Store) GetUserByAccessKey(_ context.Context, key string) (*op.UserRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.keys[key]
	if !ok {
		return nil, fmt.Errorf("access key %s: %w", key, op.ErrNoSuchUser)
	}
	return s.user(id)
}

// GetUserByEmail implements op.UserStore. radosgw keys its email index by
// the lowercased email and lowercases the one it looks up
// (svc_user_rados.cc:318-320, :737-738 at v19.2.6).
func (s *Store) GetUserByEmail(_ context.Context, email string) (*op.UserRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.emails[lowerASCII(email)]
	if !ok {
		return nil, fmt.Errorf("email %s: %w", email, op.ErrNoSuchUser)
	}
	return s.user(id)
}

// PutUser implements op.UserStore as RGWSI_User_RADOS's PutOperation writes
// a user (svc_user_rados.cc:229-351 at v19.2.6). An active access key that
// another user's index holds, and that the stored record (read unless
// Exclusive) did not already hold active, is ErrKeyExists before anything is
// written (:257-270). IfVersion is checked unless its Ver is 0, and the
// version written is IfVersion's plus one under its tag, or a fresh one
// without IfVersion or its tag (:234-241; rgw_common.h:950-955). The mtime
// stored is opts.Mtime, or the clock's time when that is zero.
func (s *Store) PutUser(_ context.Context, rec *op.UserRecord, opts op.PutUserOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rec.Info.UserID.String()
	old, exists := s.users[id]
	var stored *meta.UserInfo
	if exists && !opts.Exclusive {
		stored = &old.Info
	}
	for key, k := range rec.Info.AccessKeys {
		if !k.Active || (stored != nil && stored.AccessKeys[key].Active) {
			continue
		}
		if holder, ok := s.keys[key]; ok && holder != id {
			return fmt.Errorf("access key %s of user %s: %w", key, id, op.ErrKeyExists)
		}
	}
	if exists && opts.Exclusive {
		return fmt.Errorf("user %s: %w", id, op.ErrUserAlreadyExists)
	}
	var cur meta.ObjVersion
	if exists {
		cur = old.Version
	}
	if opts.IfVersion != nil && opts.IfVersion.Ver != 0 && *opts.IfVersion != cur {
		return fmt.Errorf("user %s: %w", id, op.ErrConcurrentModification)
	}
	next := meta.ObjVersion{Ver: 1, Tag: s.newTag()}
	if opts.IfVersion != nil && opts.IfVersion.Tag != "" {
		next = meta.ObjVersion{Ver: opts.IfVersion.Ver + 1, Tag: opts.IfVersion.Tag}
	}
	rec.Version = next
	rec.Mtime = opts.Mtime
	if rec.Mtime.IsZero() {
		rec.Mtime = s.now()
	}
	s.storeUser(copyUser(rec))
	return nil
}

// RemoveUser implements op.UserStore. It checks rec.Version unless its Ver
// is 0, and removes nothing when the user is missing or changed; the
// contract allows a partial removal, which radosgw's order leaves.
func (s *Store) RemoveUser(_ context.Context, rec *op.UserRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rec.Info.UserID.String()
	cur, ok := s.users[id]
	if !ok {
		return fmt.Errorf("user %s: %w", id, op.ErrNoSuchUser)
	}
	if rec.Version.Ver != 0 && rec.Version != cur.Version {
		return fmt.Errorf("user %s: %w", id, op.ErrConcurrentModification)
	}
	s.unindexUser(id)
	delete(s.users, id)
	return nil
}

// ListUserBuckets implements op.UserStore over the owner's buckets in name
// order, as cls_user keys a user's bucket list by name (cls_user.cc:45-48 at
// v19.2.6). Each entry carries the bucket's creation time, placement and
// current totals.
func (s *Store) ListUserBuckets(_ context.Context, owner meta.Owner, marker string, maxEntries int) (ents []meta.BucketEnt, next string, more bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var owned []*bucket
	for _, b := range s.buckets {
		if b.rec.Info.Owner.String() == owner.String() && b.rec.Info.Bucket.Name > marker {
			owned = append(owned, b)
		}
	}
	slices.SortFunc(owned, func(a, b *bucket) int { return strings.Compare(a.rec.Info.Bucket.Name, b.rec.Info.Bucket.Name) })
	next = marker
	for _, b := range owned[:min(max(maxEntries, 0), len(owned))] {
		st := stats(b)
		ents = append(ents, meta.BucketEnt{
			Bucket:        b.rec.Info.Bucket,
			Size:          st.Size,
			SizeRounded:   st.SizeRounded,
			CreationTime:  b.rec.Info.CreationTime,
			Count:         st.NumObjects,
			PlacementRule: b.rec.Info.PlacementRule,
		})
		next = b.rec.Info.Bucket.Name
	}
	return ents, next, len(owned) > len(ents), nil
}

// user returns a copy of the user stored under id.
func (s *Store) user(id string) (*op.UserRecord, error) {
	rec, ok := s.users[id]
	if !ok {
		return nil, fmt.Errorf("user %s: %w", id, op.ErrNoSuchUser)
	}
	return copyUser(rec), nil
}

// storeUser stores rec, which the store now owns, and rewrites its index
// entries: the email, and the access keys that are active, as PutOperation
// indexes only those (svc_user_rados.cc:328-339, :424-432 at v19.2.6).
func (s *Store) storeUser(rec *op.UserRecord) {
	id := rec.Info.UserID.String()
	s.unindexUser(id)
	s.users[id] = rec
	for key, k := range rec.Info.AccessKeys {
		if k.Active {
			s.keys[key] = id
		}
	}
	if rec.Info.Email != "" {
		s.emails[lowerASCII(rec.Info.Email)] = id
	}
}

// unindexUser drops the index entries that name the user stored under id.
func (s *Store) unindexUser(id string) {
	old, ok := s.users[id]
	if !ok {
		return
	}
	for key := range old.Info.AccessKeys {
		if s.keys[key] == id {
			delete(s.keys, key)
		}
	}
	if email := lowerASCII(old.Info.Email); s.emails[email] == id {
		delete(s.emails, email)
	}
}

// lowerASCII is boost::to_lower in the C locale radosgw runs in: ASCII
// letters only, every other byte kept.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
