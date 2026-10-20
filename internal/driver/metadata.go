package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// A metadataSection is one section of the admin metadata API, the handler
// RGWMetadataManager registers under the section's name and finds a key's
// section by
// (register_handler and find_handler,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_metadata.cc#L416-L466 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L211-L261 at v20.2.4).
// A section is served once it has a row in metadataSections, which lands
// with its row in op's put rules, the gate of a put. Get, Put and Remove
// refuse a key holding a NUL byte before a section sees it, and every verb
// answers a name without a row ErrNoSuchKey.
type metadataSection interface {
	// get is the entry key names: its document as JSON in Data and, in Doc,
	// as RGWMetadataObject::dump writes it, at the version and mtime its
	// object was read at. A missing entry is op.ErrNoSuchKey.
	get(ctx context.Context, s *Store, key string) (op.MetadataEntry, error)
	// put writes e.Data, decoded as the section's document, as the entry key
	// names, its version e.Version when that has a tag and its mtime
	// e.Mtime. opts.IfVersion is the version the op's get read: a section
	// that reads its object first, as put_pre does, refuses another with
	// op.ErrConcurrentModification and writes under the version read, or
	// exclusively when it read none. Each refusal comes before anything is
	// written, a document that does not decode being op.ErrInvalidArgument.
	// Whether a put applies at all, check_versions, is the op's.
	put(ctx context.Context, s *Store, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error
	// remove removes the entry key names, read first and removed under the
	// version read, as RGWMetadataManager::remove does. A missing entry is
	// op.ErrNoSuchKey.
	remove(ctx context.Context, s *Store, key string) error
	// pool is the pool, and its namespace, whose objects List lists.
	pool(s *Store) meta.Pool
	// oidKey is the key of the entry an object of pool holds, or false for
	// an object that holds none.
	oidKey(oid string) (key string, ok bool)
}

// metadataSections are the sections the store serves, by name.
var metadataSections = map[string]metadataSection{
	sectionUser:           userSection{},
	sectionBucket:         bucketSection{},
	sectionBucketInstance: instanceSection{},
}

// Sections implements op.MetadataStore: the name of each section
// metadataSections holds, as get_sections names the registered handlers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_metadata.cc#L687-L692 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_metadata.cc#L482-L487 at v20.2.4).
func (s *Store) Sections() []string { return slices.Sorted(maps.Keys(metadataSections)) }

// findSection is find_handler's lookup: the section named name, or
// ErrNoSuchKey, its ENOENT.
func findSection(name string) (metadataSection, error) {
	if sec, ok := metadataSections[name]; ok {
		return sec, nil
	}
	return nil, fmt.Errorf("metadata section %q: %w", name, op.ErrNoSuchKey)
}

// Get implements op.MetadataStore as RGWMetadataManager::get's handler read
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L469-L499):
// each section's document, its JSON in Data and the value in Doc, at the
// version and mtime its object was read at. An unknown section and a
// missing entry are ErrNoSuchKey, find_handler's and the read's ENOENT. A
// bucket instance whose sync policy with groups is carried opaque is
// meta.ErrOpaqueJSON.
func (s *Store) Get(ctx context.Context, section, key string) (op.MetadataEntry, error) {
	if err := op.RefuseNULKey(key); err != nil {
		return op.MetadataEntry{}, err
	}
	sec, err := findSection(section)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	return sec.get(ctx, s, key)
}

// noSuchKey maps a missing bucket to the section's ENOENT.
func noSuchKey(err error) error {
	if errors.Is(err, op.ErrNoSuchBucket) {
		return fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	return err
}

// Put implements op.MetadataStore as RGWMetadataManager::put's handler put
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L501-L552,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L129-L147):
// a key holding a NUL byte is ErrInvalidArgument in every section, on Get
// and Remove too (op.RefuseNULKey), and an unknown section is ErrNoSuchKey,
// find_handler's ENOENT. How the document is decoded, guarded and written
// is the section's put (metadataSection.put).
func (s *Store) Put(ctx context.Context, section, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	if err := op.RefuseNULKey(key); err != nil {
		return err
	}
	sec, err := findSection(section)
	if err != nil {
		return err
	}
	return sec.put(ctx, s, key, e, opts)
}

// decodeDoc decodes a put's document into v: one that does not decode is
// ErrInvalidArgument, and an opaque one ErrNotImplemented with
// meta.ErrOpaqueJSON.
func decodeDoc(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, meta.ErrOpaqueJSON):
		return fmt.Errorf("%w: %w", op.ErrNotImplemented, err)
	}
	return fmt.Errorf("%w: %w", op.ErrInvalidArgument, err)
}

// guardVersion checks a put's opts.IfVersion against read, the version of
// what the put read, zero for nothing, and refuses an object read without
// a version, which no write could be guarded by.
func guardVersion(what string, exists bool, read meta.ObjVersion, opts op.PutMetadataOptions) error {
	if exists && read.Ver == 0 {
		return fmt.Errorf("%w: %s carries no version to write it under", op.ErrInternalError, what)
	}
	if opts.IfVersion != nil && *opts.IfVersion != read {
		return fmt.Errorf("%s: %w", what, op.ErrConcurrentModification)
	}
	return nil
}

// writeVersion is the tracker a put writes under: the version read, or
// none for an exclusive create, and e's version as the write version when
// it has a tag, else an increment of the version read, or a fresh one.
func writeVersion(read meta.ObjVersion, exists bool, e op.MetadataEntry) *objv {
	v := &objv{}
	if exists {
		v.read = read
	} else {
		v.write = newWriteVersion()
	}
	if e.Version.Tag != "" {
		v.write = e.Version
	}
	return v
}

// busyBucket is ErrRenamePending for a bucket a live rename record marks,
// and ErrConcurrentModification for one a removal has claimed, whose
// other writes the bucket admin store refuses the same way.
func (s *Store) busyBucket(ctx context.Context, info meta.BucketInfo, attrs map[string][]byte) error {
	if _, removing := attrs[op.RemovingAttr]; removing {
		return fmt.Errorf("bucket %s is being removed: %w", info.Bucket.Name, op.ErrConcurrentModification)
	}
	_, live, err := s.liveRename(ctx, &op.BucketRecord{Info: info, Attrs: attrs})
	if err != nil {
		return err
	}
	if live {
		return op.ErrRenamePending
	}
	return nil
}

// listPage is the page the store's own scans of a section list in.
const listPage = 1000

// Remove implements op.MetadataStore as RGWMetadataManager::remove's
// handler remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L554-L574):
// each section's object read and removed under the version read. An
// unknown section and a missing entry are ErrNoSuchKey.
func (s *Store) Remove(ctx context.Context, section, key string) error {
	if err := op.RefuseNULKey(key); err != nil {
		return err
	}
	sec, err := findSection(section)
	if err != nil {
		return err
	}
	return sec.remove(ctx, s, key)
}

// errStopListing ends a listing's callback early.
var errStopListing = errors.New("listing stopped")

// List implements op.MetadataStore as list_keys_next over the section's
// pool (rgw_list_pool,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L346-L395 at v19.2.6):
// the keys of the objects the section keeps after marker, a token an
// earlier List returned, until maxEntries keys are listed; next resumes
// after the last object read, and more says whether the pool holds more
// objects. A token from elsewhere, radosgw's cursors among them, is
// ErrInvalidArgument. A maxEntries of 0 lists nothing and tells only
// whether a key follows. An unknown section is ErrNoSuchKey, and a pool
// that does not exist lists nothing, as the handler takes its ENOENT
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L211-L227).
func (s *Store) List(ctx context.Context, section, marker string, maxEntries int) (keys []string, next string, more bool, err error) {
	sec, err := findSection(section)
	if err != nil {
		return nil, "", false, err
	}
	pool, err := s.pools.get(ctx, sec.pool(s))
	if errors.Is(err, radosclient.ErrNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	token := marker
	if maxEntries <= 0 {
		_, _, err = pool.ListObjectsFrom(ctx, token, 0, func(oid, _ string) error {
			if _, ok := sec.oidKey(oid); ok {
				return errStopListing
			}
			return nil
		})
		switch {
		case errors.Is(err, errStopListing):
			return nil, marker, true, nil
		case err != nil:
			return nil, "", false, listErr(err)
		}
		return nil, marker, false, nil
	}
	for {
		next, more, err = pool.ListObjectsFrom(ctx, token, maxEntries-len(keys), func(oid, _ string) error {
			if k, ok := sec.oidKey(oid); ok {
				keys = append(keys, k)
			}
			return nil
		})
		if err != nil {
			return nil, "", false, listErr(err)
		}
		if !more || len(keys) >= maxEntries {
			return keys, next, more, nil
		}
		token = next
	}
}

// listErr maps a listing's failure: a token the seam did not issue is
// ErrInvalidArgument.
func listErr(err error) error {
	if errors.Is(err, radosclient.ErrBadOp) {
		return fmt.Errorf("%w: the listing marker is not one this gateway issued", op.ErrInvalidArgument)
	}
	return err
}
