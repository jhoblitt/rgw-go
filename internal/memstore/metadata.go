package memstore

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// Get implements op.MetadataStore.
func (s *Store) Get(_ context.Context, section, key string) (op.MetadataEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.metadata[section][key]
	if !ok {
		return op.MetadataEntry{}, fmt.Errorf("metadata %s:%s: %w", section, key, op.ErrNotFound)
	}
	e.Data = bytes.Clone(e.Data)
	return e, nil
}

// Put implements op.MetadataStore. A new entry starts at version 1 with a
// fresh tag and a put over a stored one increments its count; a zero Mtime
// is now.
func (s *Store) Put(_ context.Context, section, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.metadata[section][key]
	var cur meta.ObjVersion
	if exists {
		cur = old.Version
	}
	if opts.IfVersion != nil && *opts.IfVersion != cur {
		return fmt.Errorf("metadata %s:%s: %w", section, key, op.ErrConcurrentModification)
	}
	e.Key = key
	e.Data = bytes.Clone(e.Data)
	e.Version = meta.ObjVersion{Ver: 1, Tag: s.newTag()}
	if exists {
		e.Version = meta.ObjVersion{Ver: cur.Ver + 1, Tag: cur.Tag}
	}
	if e.Mtime.IsZero() {
		e.Mtime = s.now()
	}
	if s.metadata[section] == nil {
		s.metadata[section] = map[string]op.MetadataEntry{}
	}
	s.metadata[section][key] = e
	return nil
}

// Remove implements op.MetadataStore.
func (s *Store) Remove(_ context.Context, section, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.metadata[section][key]; !ok {
		return fmt.Errorf("metadata %s:%s: %w", section, key, op.ErrNotFound)
	}
	delete(s.metadata[section], key)
	return nil
}

// List implements op.MetadataStore: the section's keys past marker, sorted,
// at most maxEntries of them; next is the last key returned. The user
// section is the stored users, each keyed by its users.uid object's name,
// the user id's string form, as RGWSI_User_Module lists them
// (svc_user_rados.cc:32-66 at v19.2.6); the bucket section is the stored
// buckets, each keyed by its entry point's metadata key, the name or
// "tenant/name" (RGWSI_Bucket::get_entrypoint_meta_key, svc_bucket.cc:9-19
// at both tags); the other sections are what Put stored.
func (s *Store) List(_ context.Context, section, marker string, maxEntries int) (keys []string, next string, more bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := maps.Keys(s.metadata[section])
	switch section {
	case "user":
		all = maps.Keys(s.users)
	case "bucket":
		all = s.bucketMetadataKeys()
	}
	var after []string
	for k := range all {
		if k > marker {
			after = append(after, k)
		}
	}
	slices.Sort(after)
	keys = after[:min(max(maxEntries, 0), len(after))]
	if len(keys) == 0 {
		return nil, marker, len(after) > 0, nil
	}
	return slices.Clip(keys), keys[len(keys)-1], len(after) > len(keys), nil
}

// bucketMetadataKeys are the stored buckets' entry point metadata keys.
func (s *Store) bucketMetadataKeys() iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, b := range s.buckets {
			id := b.rec.Info.Bucket
			k := id.Name
			if id.Tenant != "" {
				k = id.Tenant + "/" + id.Name
			}
			if !yield(k) {
				return
			}
		}
	}
}
