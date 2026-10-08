package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// The metadata sections, as the RADOS driver serves them: user, bucket and
// bucket.instance over the store's users and buckets, with the driver's
// keys, documents, versions and refusals. A bucket here is its entry point
// and instance together, and an instance a put creates has no entry point
// until a bucket put names it. The store's writes cannot stop halfway or
// race, so it never holds a bucket an unfinished rename or removal marks.

// metadataKey is the bucket section's key of a bucket: its entry point's
// oid, "tenant/name" or "name".
func metadataKey(id meta.BucketID) string {
	return meta.BucketID{Tenant: id.Tenant, Name: id.Name}.EntryPointOID()
}

// entryPointKey splits a bucket section key as the driver does.
func entryPointKey(key string) (tenant, name string, ok bool) {
	if t, n, found := strings.Cut(key, "/"); found {
		tenant, name = t, n
	} else {
		name = key
	}
	return tenant, name, metadataKey(meta.BucketID{Tenant: tenant, Name: name}) == key && name != ""
}

// parseInstanceKey is the driver's reading of a bucket instance key,
// "[tenant/]name:id" or "tenant:name:id".
func parseInstanceKey(key string) (meta.BucketID, error) {
	var b meta.BucketID
	rest := key
	if t, r, ok := strings.Cut(key, "/"); ok {
		b.Tenant, rest = t, r
	}
	name, id, ok := strings.Cut(rest, ":")
	if !ok {
		return meta.BucketID{}, fmt.Errorf("%w: bucket instance key %q names no instance", op.ErrInvalidArgument, key)
	}
	b.Name, b.ID = name, id
	if b.Tenant == "" {
		if n, i, ok := strings.Cut(id, ":"); ok {
			b.Tenant, b.Name, b.ID = name, n, i
		}
	}
	if b.ID == "" {
		return meta.BucketID{}, fmt.Errorf("%w: bucket instance key %q names no instance", op.ErrInvalidArgument, key)
	}
	return b, nil
}

// instanceKey is the bucket instance section's key of an instance.
func instanceKey(id meta.BucketID) string {
	if id.Tenant != "" {
		return id.Tenant + "/" + id.Name + ":" + id.ID
	}
	return id.Name + ":" + id.ID
}

// Get implements op.MetadataStore with the RADOS driver's sections.
func (s *Store) Get(ctx context.Context, section, key string) (op.MetadataEntry, error) {
	if err := op.RefuseNULKey(key); err != nil {
		return op.MetadataEntry{}, err
	}
	switch section {
	case "user":
		return op.GetMetadataUser(ctx, s, key)
	case "bucket":
		return s.getEntryPoint(key)
	case "bucket.instance":
		return s.getInstance(key)
	}
	return op.MetadataEntry{}, fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
}

func (s *Store) getEntryPoint(key string) (op.MetadataEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := s.named(key)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	rec := copyBucket(&b.rec)
	data, err := json.Marshal(rec.EntryPoint)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: rec.EntryPoint, Version: rec.EPVersion, Mtime: rec.Mtime}, nil
}

// named is the bucket whose entry point key is key.
func (s *Store) named(key string) (*bucket, error) {
	tenant, name, ok := entryPointKey(key)
	if !ok {
		return nil, fmt.Errorf("bucket %q: %w", key, op.ErrNoSuchKey)
	}
	b, ok := s.buckets[bucketKey(tenant, name)]
	if !ok {
		return nil, fmt.Errorf("bucket %q: %w", key, op.ErrNoSuchKey)
	}
	return b, nil
}

// instanceOf is the instance the key names, which must carry the key's
// tenant and name.
func (s *Store) instanceOf(id meta.BucketID) (*bucket, bool) {
	b, ok := s.instances[id.ID]
	if !ok || b.rec.Info.Bucket.Tenant != id.Tenant || b.rec.Info.Bucket.Name != id.Name {
		return nil, false
	}
	return b, true
}

func (s *Store) getInstance(key string) (op.MetadataEntry, error) {
	id, err := parseInstanceKey(key)
	if err != nil {
		return op.MetadataEntry{}, fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.instanceOf(id)
	if !ok {
		return op.MetadataEntry{}, fmt.Errorf("bucket instance %q: %w", key, op.ErrNoSuchKey)
	}
	rec := copyBucket(&b.rec)
	doc := meta.BucketCompleteInfo{Info: rec.Info, Attrs: rec.Attrs}
	data, err := json.Marshal(doc)
	if err != nil {
		return op.MetadataEntry{}, fmt.Errorf("bucket instance %s: %w", key, err)
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: doc, Version: rec.Version, Mtime: rec.Mtime}, nil
}

// Put implements op.MetadataStore with the RADOS driver's sections and
// refusals: the same documents, a put guarded by the version stored, a
// stored instance's layout, marker, placement, object lock and rgw-go
// attrs kept. The memstore's lists follow its buckets' instance owners, so
// an entry point's linked flag changes no list, as for UnlinkBucketOwner.
func (s *Store) Put(ctx context.Context, section, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	if err := op.RefuseNULKey(key); err != nil {
		return err
	}
	switch section {
	case "user":
		return op.PutMetadataUser(ctx, s, s, key, e, opts)
	case "bucket":
		return s.putEntryPoint(key, e, opts)
	case "bucket.instance":
		return s.putInstance(key, e, opts)
	}
	return fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
}

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

// nextVersion is the version a put writes over cur: e's when it has a tag,
// else cur's next, or a fresh one over nothing.
func (s *Store) nextVersion(cur meta.ObjVersion, exists bool, e op.MetadataEntry) meta.ObjVersion {
	switch {
	case e.Version.Tag != "":
		return e.Version
	case exists:
		return meta.ObjVersion{Ver: cur.Ver + 1, Tag: cur.Tag}
	}
	return meta.ObjVersion{Ver: 1, Tag: s.newTag()}
}

func ifVersion(what string, cur meta.ObjVersion, opts op.PutMetadataOptions) error {
	if opts.IfVersion != nil && *opts.IfVersion != cur {
		return fmt.Errorf("%s: %w", what, op.ErrConcurrentModification)
	}
	return nil
}

func (s *Store) putEntryPoint(key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	var ep meta.BucketEntryPoint
	if err := decodeDoc(e.Data, &ep); err != nil {
		return err
	}
	tenant, name, ok := entryPointKey(key)
	if !ok || ep.Bucket.Tenant != tenant || ep.Bucket.Name != name {
		return fmt.Errorf("%w: the document names bucket %s under key %s", op.ErrInvalidArgument, ep.Bucket.Key(), key)
	}
	if ep.HasBucketInfo {
		return fmt.Errorf("%w: an entry point from before version 8 is not written", op.ErrInvalidArgument)
	}
	if err := op.RefuseNUL("the entry point", ep.Bucket.ID, ep.Bucket.Marker, ep.Owner.String()); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := bucketKey(tenant, name)
	old, exists := s.buckets[k]
	var cur meta.ObjVersion
	if exists {
		cur = old.rec.EPVersion
	}
	if err := ifVersion("bucket "+key, cur, opts); err != nil {
		return err
	}
	if exists && old.rec.Info.Bucket.ID != ep.Bucket.ID {
		return fmt.Errorf("bucket %s names instance %s: %w", key, old.rec.Info.Bucket.ID, op.ErrBucketAlreadyExists)
	}
	b, ok := s.instanceOf(meta.BucketID{Tenant: tenant, Name: name, ID: ep.Bucket.ID})
	if !ok {
		return fmt.Errorf("%w: bucket %s: the instance %s does not exist", op.ErrInvalidArgument, key, ep.Bucket.ID)
	}
	if ep.Owner.String() != b.rec.Info.Owner.String() {
		return fmt.Errorf("%w: bucket %s: the entry point's owner is not its instance's", op.ErrInvalidArgument, key)
	}
	if !exists {
		for other, ob := range s.buckets {
			if ob == b && other != k {
				return fmt.Errorf("bucket %s loads instance %s: %w", other, ep.Bucket.ID, op.ErrBucketAlreadyExists)
			}
		}
	}
	ep.Owner = cloneOwner(ep.Owner)
	ep.Bucket = b.rec.Info.Bucket
	b.rec.EntryPoint = ep
	b.rec.EPVersion = s.nextVersion(cur, exists, e)
	s.buckets[k] = b
	return nil
}

func (s *Store) putInstance(key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	id, err := parseInstanceKey(key)
	if err != nil {
		return err
	}
	var doc meta.BucketCompleteInfo
	if err := decodeDoc(e.Data, &doc); err != nil {
		return err
	}
	if err := op.RefuseRawAttrs(doc.Attrs); err != nil {
		return err
	}
	p := doc.Info.Bucket.ExplicitPlacement
	if err := op.RefuseNUL("the bucket instance", doc.Info.Bucket.Marker, doc.Info.Owner.String(),
		p.DataPool.Name, p.DataPool.NS, p.DataExtraPool.Name, p.DataExtraPool.NS, p.IndexPool.Name, p.IndexPool.NS); err != nil {
		return err
	}
	info := cloneBucketInfo(doc.Info)
	info.Bucket.Tenant, info.Bucket.Name, info.Bucket.ID = id.Tenant, id.Name, id.ID
	attrs := cloneAttrs(doc.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	delete(attrs, op.RenameIntentAttr)
	delete(attrs, op.RemovingAttr)
	s.mu.Lock()
	defer s.mu.Unlock()
	old, exists := s.instanceOf(id)
	if !exists {
		if _, taken := s.instances[id.ID]; taken {
			return fmt.Errorf("bucket instance id %s: %w", id.ID, op.ErrBucketAlreadyExists)
		}
		for _, other := range s.instances {
			if other.rec.Info.Bucket.Marker == id.ID {
				return fmt.Errorf("bucket instance id %s is another instance's marker: %w", id.ID, op.ErrBucketAlreadyExists)
			}
		}
	}
	var cur meta.ObjVersion
	if exists {
		cur = old.rec.Version
	}
	if err := ifVersion("bucket instance "+key, cur, opts); err != nil {
		return err
	}
	if exists {
		if err := keepInstance(&info, attrs, old); err != nil {
			return err
		}
		if s.buckets[bucketKey(id.Tenant, id.Name)] == old && old.rec.EntryPoint.Owner.String() != info.Owner.String() {
			return fmt.Errorf("%w: bucket instance %s: the owner is not its entry point's", op.ErrInvalidArgument, key)
		}
	} else if err := s.newInstance(&info); err != nil {
		return err
	}
	b := old
	if !exists {
		ep := meta.NewBucketEntryPoint()
		ep.Bucket = info.Bucket
		b = &bucket{rec: op.BucketRecord{EntryPoint: ep}, objects: map[meta.ObjKey]*object{}}
	}
	b.rec.Info = info
	b.rec.Attrs = attrs
	b.rec.Version = s.nextVersion(cur, exists, e)
	b.rec.Mtime = e.Mtime
	if b.rec.Mtime.IsZero() {
		b.rec.Mtime = s.now()
	}
	s.instances[id.ID] = b
	return nil
}

// keepInstance is the driver's keepInstance over the stored bucket old.
func keepInstance(info *meta.BucketInfo, attrs map[string][]byte, old *bucket) error {
	stored := old.rec.Info
	cur, next := stored.Layout.Current.Layout, info.Layout.Current.Layout
	switch {
	case info.Bucket.Marker != stored.Bucket.Marker:
		return fmt.Errorf("%w: bucket instance %s: the marker changes", op.ErrInvalidArgument, stored.Bucket.Key())
	case next.Type != cur.Type || next.Normal.NumShards != cur.Normal.NumShards || next.Normal.HashType != cur.Normal.HashType:
		return fmt.Errorf("%w: bucket instance %s: the index layout changes", op.ErrInvalidArgument, stored.Bucket.Key())
	case stored.ObjLockEnabled() && !info.ObjLockEnabled():
		return fmt.Errorf("%w: bucket instance %s: object lock cannot be turned off", op.ErrInvalidArgument, stored.Bucket.Key())
	}
	keep := cloneBucketInfo(stored)
	info.Bucket.ExplicitPlacement = keep.Bucket.ExplicitPlacement
	info.PlacementRule = keep.PlacementRule
	info.Layout = keep.Layout
	info.ObjLock = keep.ObjLock
	for _, k := range []string{op.RenameIntentAttr, op.RemovingAttr} {
		if v, ok := old.rec.Attrs[k]; ok {
			attrs[k] = v
		}
	}
	return nil
}

// newInstance is the driver's newInstance over the store's zone.
func (s *Store) newInstance(info *meta.BucketInfo) error {
	if info.Bucket.Marker != info.Bucket.ID {
		return fmt.Errorf("%w: bucket instance %s: the marker of a new instance must be its id", op.ErrInvalidArgument, info.Bucket.Key())
	}
	pi, ok := s.cfg.Params.PlacementPools[info.PlacementRule.Name]
	if info.PlacementRule.Name == "" || !ok {
		return fmt.Errorf("%w: bucket instance %s: no placement rule %q in the zone", op.ErrInvalidArgument, info.Bucket.Key(), info.PlacementRule.Name)
	}
	if _, ok := pi.StorageClasses[info.PlacementRule.CanonicalStorageClass()]; !ok && info.PlacementRule.CanonicalStorageClass() != meta.StorageClassStandard {
		return fmt.Errorf("%w: bucket instance %s: no storage class %q", op.ErrInvalidArgument, info.Bucket.Key(), info.PlacementRule.StorageClass)
	}
	current := &info.Layout.Current
	current.Layout.Type = meta.IndexType(pi.IndexType)
	info.Layout.Logs = nil
	if current.Layout.Type == meta.IndexNormal {
		info.Layout.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, *current)}
	}
	info.ObjLock = nil
	return nil
}

// Remove implements op.MetadataStore with the RADOS driver's sections and
// refusals: a bucket's entry point goes only once its instance has, which
// a bucket here never outlives, and an instance only while no entry point
// names it; on Tentacle an instance removal removes nothing.
func (s *Store) Remove(ctx context.Context, section, key string) error {
	if err := op.RefuseNULKey(key); err != nil {
		return err
	}
	switch section {
	case "user":
		return op.RemoveMetadataUser(ctx, s, s, key)
	case "bucket":
		s.mu.RLock()
		defer s.mu.RUnlock()
		if _, err := s.named(key); err != nil {
			return err
		}
		return fmt.Errorf("bucket %s: its instance exists, so it is removed with the bucket: %w", key, op.ErrConcurrentModification)
	case "bucket.instance":
		return s.removeInstance(key)
	}
	return fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
}

func (s *Store) removeInstance(key string) error {
	id, err := parseInstanceKey(key)
	if err != nil {
		return fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.instanceOf(id)
	switch {
	case !ok:
		return fmt.Errorf("bucket instance %q: %w", key, op.ErrNoSuchKey)
	case s.cfg.Release >= denc.Tentacle:
		return nil
	case s.buckets[bucketKey(id.Tenant, id.Name)] == b:
		return fmt.Errorf("bucket instance %s: its bucket's entry point names it: %w", key, op.ErrConcurrentModification)
	}
	delete(s.instances, id.ID)
	return nil
}

// List implements op.MetadataStore: the section's keys past marker, sorted,
// at most maxEntries of them; next is the last key returned. The user
// section is the stored users, each keyed by its users.uid object's name,
// the user id's string form, as RGWSI_User_Module lists them
// (svc_user_rados.cc:32-66 at v19.2.6); the bucket section is the stored
// buckets, each keyed by its entry point's metadata key, the name or
// "tenant/name" (RGWSI_Bucket::get_entrypoint_meta_key, svc_bucket.cc:9-19
// at both tags); the bucket.instance section is the stored instances,
// "[tenant/]name:id". Another section is ErrNoSuchKey.
func (s *Store) List(_ context.Context, section, marker string, maxEntries int) (keys []string, next string, more bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []string
	switch section {
	case "user":
		all = slices.Collect(maps.Keys(s.users))
	case "bucket":
		for _, b := range s.buckets {
			all = append(all, metadataKey(b.rec.Info.Bucket))
		}
	case "bucket.instance":
		for _, b := range s.instances {
			all = append(all, instanceKey(b.rec.Info.Bucket))
		}
	default:
		return nil, "", false, fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
	}
	var after []string
	for _, k := range all {
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
