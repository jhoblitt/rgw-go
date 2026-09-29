package memstore

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// multipartNS is the bucket index namespace of upload meta objects (RGW_OBJ_NS_MULTIPART).
const multipartNS = "multipart"

func bucketKey(tenant, name string) string { return tenant + "/" + name }

// GetBucket implements op.BucketStore.
func (s *Store) GetBucket(_ context.Context, tenant, name string) (*op.BucketRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.buckets[bucketKey(tenant, name)]
	if !ok {
		return nil, fmt.Errorf("bucket %s: %w", bucketKey(tenant, name), op.ErrNoSuchBucket)
	}
	return copyBucket(&b.rec), nil
}

// GetBucketInstance implements op.BucketStore.
func (s *Store) GetBucketInstance(_ context.Context, id meta.BucketID) (*op.BucketRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.instances[id.ID]
	if !ok {
		return nil, fmt.Errorf("bucket instance %s: %w", id.ID, op.ErrNoSuchBucket)
	}
	return copyBucket(&b.rec), nil
}

// CreateBucket implements op.BucketStore. The instance id has radosgw's
// three-part shape, "<zone id>.<counter>.1", and is the marker too. An
// existing name, Exclusive or not, answers with a copy of its record and
// ErrBucketAlreadyExists.
func (s *Store) CreateBucket(_ context.Context, p op.CreateBucketParams) (*op.BucketRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := bucketKey(p.Tenant, p.Name)
	if b, ok := s.buckets[key]; ok {
		return copyBucket(&b.rec), fmt.Errorf("bucket %s: %w", key, op.ErrBucketAlreadyExists)
	}
	s.bucketSeq++
	id := fmt.Sprintf("%s.%d.1", s.cfg.Params.ID, s.bucketSeq)
	bid := meta.BucketID{Tenant: p.Tenant, Name: p.Name, Marker: id, ID: id}
	now := s.now()

	info := meta.NewBucketInfo()
	info.Bucket = bid
	info.Owner = cloneOwner(p.Owner)
	info.Zonegroup = p.Zonegroup
	info.CreationTime = meta.Time{Time: now}
	info.PlacementRule = p.Placement
	info.HasInstanceObj = true
	info.Quota = p.Quota

	ep := meta.NewBucketEntryPoint()
	ep.Bucket = bid
	ep.Owner = cloneOwner(p.Owner)
	ep.CreationTime = info.CreationTime
	ep.Linked = true

	b := &bucket{
		rec: op.BucketRecord{
			EntryPoint: ep,
			Info:       info,
			Attrs:      cloneAttrs(p.Attrs),
			Version:    meta.ObjVersion{Ver: 1, Tag: s.newTag()},
			EPVersion:  meta.ObjVersion{Ver: 1, Tag: s.newTag()},
			Mtime:      now,
		},
		objects: map[meta.ObjKey]*object{},
	}
	s.buckets[key] = b
	s.instances[id] = b
	return copyBucket(&b.rec), nil
}

// DeleteBucket implements op.BucketStore.
func (s *Store) DeleteBucket(_ context.Context, rec *op.BucketRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.instance(rec)
	if err != nil {
		return err
	}
	if len(b.objects) > 0 {
		return fmt.Errorf("bucket %s: %w", rec.Info.Bucket.Name, op.ErrBucketNotEmpty)
	}
	delete(s.instances, rec.Info.Bucket.ID)
	key := bucketKey(rec.Info.Bucket.Tenant, rec.Info.Bucket.Name)
	if s.buckets[key] == b {
		delete(s.buckets, key)
	}
	return nil
}

// PutBucketInfo implements op.BucketStore, leaving rec at the new version.
func (s *Store) PutBucketInfo(_ context.Context, rec *op.BucketRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.guardInstance(rec)
	if err != nil {
		return err
	}
	b.rec.Info = cloneBucketInfo(rec.Info)
	s.bumpInstance(b, rec)
	return nil
}

// PutBucketAttrs implements op.BucketStore: set, then rm. It leaves rec with
// the resulting attrs at the new version.
func (s *Store) PutBucketAttrs(_ context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.guardInstance(rec)
	if err != nil {
		return err
	}
	b.rec.Attrs = applyAttrs(b.rec.Attrs, set, rm)
	s.bumpInstance(b, rec)
	rec.Attrs = cloneAttrs(b.rec.Attrs)
	return nil
}

// guardInstance finds rec's instance, which must still be at rec.Version.
func (s *Store) guardInstance(rec *op.BucketRecord) (*bucket, error) {
	b, err := s.instance(rec)
	if err != nil {
		return nil, err
	}
	if b.rec.Version != rec.Version {
		return nil, fmt.Errorf("bucket %s: %w", rec.Info.Bucket.Name, op.ErrConcurrentModification)
	}
	return b, nil
}

// bumpInstance increments the instance version and stamps its mtime, in the
// store and in the caller's rec.
func (s *Store) bumpInstance(b *bucket, rec *op.BucketRecord) {
	b.rec.Version.Ver++
	b.rec.Mtime = s.now()
	rec.Version, rec.Mtime = b.rec.Version, b.rec.Mtime
}

// applyAttrs returns attrs with set applied, then rm removed, leaving attrs
// itself unchanged.
func applyAttrs(attrs, set map[string][]byte, rm []string) map[string][]byte {
	out := cloneAttrs(attrs)
	if out == nil {
		out = map[string][]byte{}
	}
	maps.Copy(out, cloneAttrs(set))
	for _, k := range rm {
		delete(out, k)
	}
	return out
}

// ListObjects implements op.BucketStore in index order: Marker and
// NextMarker are key names in p.NS, and the order of names is the order of
// the index keys radosgw escapes them to.
func (s *Store) ListObjects(_ context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := s.instance(rec)
	if err != nil {
		return op.ListObjectsResult{}, err
	}
	var entries []op.ObjectEntry
	for key, o := range b.objects {
		if key.NS == p.NS {
			entries = append(entries, op.ObjectEntry{
				Key:          key,
				Size:         o.state.Size,
				Mtime:        o.state.Mtime,
				ETag:         o.state.ETag,
				StorageClass: cmp.Or(o.state.StorageClass, meta.StorageClassStandard),
				Exists:       true,
			})
		}
	}
	if p.NS == multipartNS {
		for _, u := range s.uploads {
			if u.bucketID == rec.Info.Bucket.ID {
				entries = append(entries, op.ObjectEntry{
					Key:              meta.ObjKey{Name: u.metaName(), NS: multipartNS},
					Mtime:            u.up.Initiated,
					Owner:            cloneOwner(u.up.Owner),
					OwnerDisplayName: u.up.OwnerName,
					StorageClass:     meta.StorageClassStandard,
					Exists:           true,
				})
			}
		}
	}
	slices.SortFunc(entries, func(a, b op.ObjectEntry) int {
		return cmp.Or(strings.Compare(a.Key.Name, b.Key.Name), strings.Compare(a.Key.Instance, b.Key.Instance))
	})

	var res op.ListObjectsResult
	pg := newPager(p.Prefix, p.Delimiter, p.Marker, p.MaxKeys)
	for i := range entries {
		name := entries[i].Key.Name
		if name <= p.Marker {
			continue
		}
		switch pg.next(name) {
		case pagePrefix:
			res.CommonPrefixes = append(res.CommonPrefixes, pg.last)
		case pageEntry:
			res.Entries = append(res.Entries, entries[i])
		case pageFull:
			res.Truncated, res.NextMarker = true, pg.last
			return res, nil
		case pageSkip:
		}
	}
	return res, nil
}

// pager is the per-name logic of list_objects_ordered once a name is past
// the marker (rgw_rados.cc:1802-2160 at v19.2.6): the prefix filter, the
// rollup of names holding the delimiter after the prefix into one common
// prefix each, and the count of names and prefixes against the page size.
type pager struct {
	prefix, delim string
	// skip is the common prefix the marker falls inside, which a previous
	// page returned; list_objects_ordered fast-forwards the marker past it
	// (:1844-1854).
	skip       string
	room       int
	count      int
	last       string // the last name or common prefix listed, the marker to go on from
	lastPrefix string // the last common prefix listed
}

type pageStep int

const (
	pageSkip   pageStep = iota // not listed
	pagePrefix                 // a new common prefix, now pager.last
	pageEntry                  // a name, now pager.last
	pageFull                   // the page is full and another item remains
)

func newPager(prefix, delim, marker string, maxKeys int) *pager {
	g := &pager{prefix: prefix, delim: delim, room: max(maxKeys, 0), last: marker}
	if delim != "" && len(marker) >= len(prefix) {
		if i := strings.Index(marker[len(prefix):], delim); i >= 0 {
			g.skip = marker[:len(prefix)+i+len(delim)]
		}
	}
	return g
}

func (g *pager) next(name string) pageStep {
	if (g.skip != "" && strings.HasPrefix(name, g.skip)) || !strings.HasPrefix(name, g.prefix) {
		return pageSkip
	}
	step, item := pageEntry, name
	if g.delim != "" {
		if i := strings.Index(name[len(g.prefix):], g.delim); i >= 0 {
			step, item = pagePrefix, name[:len(g.prefix)+i+len(g.delim)]
			if item == g.lastPrefix {
				return pageSkip
			}
		}
	}
	if g.count >= g.room {
		return pageFull
	}
	g.count++
	g.last = item
	if step == pagePrefix {
		g.lastPrefix = item
	}
	return step
}
