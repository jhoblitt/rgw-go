package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The metadata sections are the RGWMetadataManager handlers the rados
// driver registers for users and buckets (driver/rados/rgw_user.cc and
// rgw_bucket.cc) over the metadata backend's objects
// (services/svc_meta_be_sobj.cc, svc_user_rados.cc and svc_bucket_sobj.cc).
// A bare line number is v19.2.6's.
const (
	sectionUser           = "user"
	sectionBucket         = "bucket"
	sectionBucketInstance = "bucket.instance"
)

// bucketInstancePrefix is RGW_BUCKET_INSTANCE_MD_PREFIX
// (svc_bucket_sobj.cc:20; instance_oid_prefix, v20.2.4 :23).
const bucketInstancePrefix = ".bucket.meta."

// instanceOIDToKey is oid_to_key (svc_bucket_sobj.cc:103-122;
// instance_oid_to_meta_key, v20.2.4 :44-62): the oid past the prefix, its
// first ':' made a '/' when a second follows, as then the first ends the
// tenant.
func instanceOIDToKey(oid string) string {
	key, ok := strings.CutPrefix(oid, bucketInstancePrefix)
	if !ok {
		return ""
	}
	if c := strings.IndexByte(key, ':'); c >= 0 && strings.IndexByte(key[c+1:], ':') >= 0 {
		key = key[:c] + "/" + key[c+1:]
	}
	return key
}

// parseInstanceKey is parse_bucket (driver/rados/rgw_bucket.cc:50-86;
// v20.2.4 :51-87), which the instance handler reads a key with:
// "[tenant/]name:id", or "tenant:name:id". A key naming no instance id is
// ErrInvalidArgument.
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

// entryPointKey splits a bucket section key, "tenant/name" or "name", the
// entry point's oid (svc_bucket_sobj.cc:49-57). A key whose split names
// another oid, as an empty tenant before a '/' would, names no entry point.
func entryPointKey(key string) (tenant, name string, ok bool) {
	if t, n, found := strings.Cut(key, "/"); found {
		tenant, name = t, n
	} else {
		name = key
	}
	return tenant, name, meta.BucketID{Tenant: tenant, Name: name}.EntryPointOID() == key && name != ""
}

// Get implements op.MetadataStore as RGWMetadataManager::get's handler read
// (rgw_metadata.cc:469-499): each section's document, its JSON in Data and
// the value in Doc, at the version and mtime its object was read at. An
// unknown section and a missing entry are ErrNoSuchKey, find_handler's and
// the read's ENOENT. A bucket instance whose website configuration or sync
// policy is carried opaque is meta.ErrOpaqueJSON.
func (s *Store) Get(ctx context.Context, section, key string) (op.MetadataEntry, error) {
	if err := op.RefuseNULKey(key); err != nil {
		return op.MetadataEntry{}, err
	}
	switch section {
	case sectionUser:
		return op.GetMetadataUser(ctx, s, key)
	case sectionBucket:
		return s.getEntryPoint(ctx, key)
	case sectionBucketInstance:
		return s.getInstance(ctx, key)
	}
	return op.MetadataEntry{}, fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
}

// noSuchKey maps a missing bucket to the section's ENOENT.
func noSuchKey(err error) error {
	if errors.Is(err, op.ErrNoSuchBucket) {
		return fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	return err
}

// getEntryPoint is RGWBucketMetadataHandler::do_get (rgw_bucket.cc:2158-2176;
// get, v20.2.4 :2336-2353).
func (s *Store) getEntryPoint(ctx context.Context, key string) (op.MetadataEntry, error) {
	tenant, name, ok := entryPointKey(key)
	if !ok {
		return op.MetadataEntry{}, fmt.Errorf("bucket %q: %w", key, op.ErrNoSuchKey)
	}
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return op.MetadataEntry{}, noSuchKey(err)
	}
	data, err := json.Marshal(e.ep)
	if err != nil {
		return op.MetadataEntry{}, err
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: e.ep, Version: e.v.read, Mtime: e.mtime}, nil
}

// getInstance is RGWBucketInstanceMetadataHandler::do_get
// (rgw_bucket.cc:2685-2700; get, v20.2.4 :2843-2856).
func (s *Store) getInstance(ctx context.Context, key string) (op.MetadataEntry, error) {
	b, err := parseInstanceKey(key)
	if err != nil {
		return op.MetadataEntry{}, fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	in, err := s.readInstance(ctx, b)
	if err != nil {
		return op.MetadataEntry{}, noSuchKey(err)
	}
	doc := meta.BucketCompleteInfo{Info: in.info, Attrs: in.attrs}
	data, err := json.Marshal(doc)
	if err != nil {
		return op.MetadataEntry{}, fmt.Errorf("bucket instance %s: %w", key, err)
	}
	return op.MetadataEntry{Key: key, Data: data, Doc: doc, Version: in.v.read, Mtime: in.mtime}, nil
}

// Put implements op.MetadataStore as RGWMetadataManager::put's handler put
// (rgw_metadata.cc:501-552, :129-147): e.Data decoded as the section's
// document, a document that does not decode being ErrInvalidArgument and
// an opaque one meta.ErrOpaqueJSON. Each section reads its object first, as
// put_pre does (:255-278), checks opts.IfVersion against the version read,
// and writes under that version, or exclusively when it read nothing; the
// version written is e.Version's when it has a tag, and the mtime
// e.Mtime's. Whether the put is applied at all, check_versions, is the
// op's. A key holding a NUL byte is ErrInvalidRequest in every section, on
// Get and Remove too (op.RefuseNULKey).
func (s *Store) Put(ctx context.Context, section, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	if err := op.RefuseNULKey(key); err != nil {
		return err
	}
	switch section {
	case sectionUser:
		return op.PutMetadataUser(ctx, s, s, key, e, opts)
	case sectionBucket:
		return s.putEntryPoint(ctx, key, e, opts)
	case sectionBucketInstance:
		return s.putInstance(ctx, key, e, opts)
	}
	return fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
}

// decodeDoc decodes a put's document into v.
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

// putEntryPoint is RGWMetadataHandlerPut_Bucket's put_checked and put_post
// (rgw_bucket.cc:2265-2317; v20.2.4 RGWBucketMetadataHandler::put,
// :2355-2401). The entry point is written with the old entry point's attrs,
// and the owner lists follow the entry point written: its owner's list
// names the bucket while it is linked and not while it is unlinked, and an
// old owner's list entry naming the bucket goes. The new owner's list is
// set from the entry point after it is written, so a put that stops between
// is completed there by its retry; the old owner's entry is not, as the
// retry reads the new entry point as the old one, and stays in that list.
//
// The bucket's claim (bucketclaim.go) is read before the instance is
// checked and, for a claim that does not exist yet, created after; it is
// marked with the entry point's owner before the entry point is written and
// cleared after, so an instance put changing the owner meanwhile fails one
// of the two.
//
// Each refusal comes before anything is written. radosgw writes the entry
// point the document gives under the key, whatever bucket, instance and
// owner it names; rgw-go refuses:
//   - a document naming another bucket than the key, or one from before
//     version 8, which embeds its bucket info, and a NUL byte in the
//     bucket id, marker or owner it names: ErrInvalidArgument;
//   - over a stored entry point, another bucket id: ErrBucketAlreadyExists,
//     as the name is held by the bucket it names, which would be left
//     without it;
//   - an instance the document names that does not exist, and an owner
//     other than that instance's: ErrInvalidArgument, as an entry point
//     owner apart from the instance's is a second owner to every later
//     link, unlink and chown;
//   - for a new entry point, an instance whose id another entry point
//     loads: ErrBucketAlreadyExists, as two names would reach one bucket's
//     index and data;
//   - a bucket a live rename or a removal holds, as the bucket admin store
//     refuses its other writes.
func (s *Store) putEntryPoint(ctx context.Context, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
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
	old, err := s.readEntryPoint(ctx, tenant, name)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		old = nil
	case err != nil:
		return err
	}
	var read meta.ObjVersion
	if old != nil {
		read = old.v.read
	}
	err = guardVersion("bucket "+key, old != nil, read, opts)
	if err != nil {
		return err
	}
	if old != nil && old.ep.Bucket.ID != ep.Bucket.ID {
		return fmt.Errorf("bucket %s names instance %s: %w", key, old.ep.Bucket.ID, op.ErrBucketAlreadyExists)
	}
	claim, err := s.readClaim(ctx, ep.Bucket)
	if err != nil {
		return err
	}
	in, err := s.readInstance(ctx, ep.Bucket)
	if errors.Is(err, op.ErrNoSuchBucket) {
		return fmt.Errorf("%w: bucket %s: the instance %s does not exist", op.ErrInvalidArgument, key, ep.Bucket.ID)
	}
	if err != nil {
		return err
	}
	err = s.busyBucket(ctx, in.info, in.attrs)
	if err != nil {
		return err
	}
	if ep.Owner.String() != in.info.Owner.String() {
		return fmt.Errorf("%w: bucket %s: the entry point's owner is not its instance's", op.ErrInvalidArgument, key)
	}
	if old == nil {
		err = s.refuseNamedElsewhere(ctx, ep.Bucket)
		if err != nil {
			return err
		}
	}
	if claim == nil {
		// The instance was read before the claim existed, so a claim
		// another put creates meanwhile may follow a write this did not see.
		claim, err = s.createClaim(ctx, ep.Bucket)
		if errors.Is(err, radosclient.ErrExists) {
			return fmt.Errorf("bucket %s: %w", key, op.ErrConcurrentModification)
		}
		if err != nil {
			return mapBucketErr(err)
		}
	}
	err = s.markClaim(ctx, claim, ep.Owner)
	if err != nil {
		return err
	}
	var attrs map[string][]byte
	if old != nil {
		attrs = old.attrs
	}
	err = s.writeEntryPoint(ctx, ep, attrs, old == nil, e.Mtime, writeVersion(read, old != nil, e))
	if err != nil {
		s.abandonMark(ctx, claim, err)
		return err
	}
	err = s.clearClaim(ctx, claim)
	if err != nil {
		return err
	}
	if old != nil && old.ep.Owner.String() != ep.Owner.String() {
		err = s.unlinkInstance(ctx, old.ep.Owner, ep.Bucket)
		if err != nil {
			return op.FromRADOS(err, op.ScopeBucket)
		}
	}
	if ep.Linked {
		err = s.linkBucket(ctx, ep.Owner, ep.Bucket, ep.CreationTime.Time)
	} else {
		err = s.unlinkInstance(ctx, ep.Owner, ep.Bucket)
	}
	return op.FromRADOS(err, op.ScopeBucket)
}

// refuseNamedElsewhere is ErrBucketAlreadyExists when an entry point under
// another name than b's loads b's id, read from every page of the bucket
// section's listing. One that cannot be read refuses too, as what it names
// cannot be told; one gone since the listing is skipped.
func (s *Store) refuseNamedElsewhere(ctx context.Context, b meta.BucketID) error {
	marker := ""
	for {
		keys, next, more, err := s.List(ctx, sectionBucket, marker, listPage)
		if err != nil {
			return err
		}
		for _, k := range keys {
			tenant, name, _ := entryPointKey(k)
			if tenant == b.Tenant && name == b.Name {
				continue
			}
			e, err := s.readEntryPoint(ctx, tenant, name)
			switch {
			case errors.Is(err, op.ErrNoSuchBucket):
				continue
			case err != nil:
				return err
			case e.ep.Bucket.ID == b.ID:
				return fmt.Errorf("bucket %s loads instance %s: %w", k, b.ID, op.ErrBucketAlreadyExists)
			}
		}
		if !more {
			return nil
		}
		marker = next
	}
}

// refuseIDElsewhere is ErrBucketAlreadyExists when another instance object
// carries b's id or marker as its id or marker, from every page of the
// bucket instance section's listing: two instances with one id share its
// index, and two with one marker each other's objects, whose names the
// marker prefixes. The listing's keys carry each instance's id; its marker
// takes a read of the instance, one per instance. An instance that cannot
// be read refuses too, as its marker cannot be told; one gone since the
// listing is skipped.
func (s *Store) refuseIDElsewhere(ctx context.Context, b meta.BucketID) error {
	marker := ""
	for {
		keys, next, more, err := s.List(ctx, sectionBucketInstance, marker, listPage)
		if err != nil {
			return err
		}
		for _, k := range keys {
			other, err := parseInstanceKey(k)
			if err != nil || other.Tenant == b.Tenant && other.Name == b.Name && other.ID == b.ID {
				continue
			}
			if other.ID == b.ID || other.ID == b.Marker {
				return fmt.Errorf("bucket instance %s carries the id or marker of %s: %w", k, b.Key(), op.ErrBucketAlreadyExists)
			}
			in, err := s.readInstance(ctx, other)
			switch {
			case errors.Is(err, op.ErrNoSuchBucket):
				continue
			case err != nil:
				return err
			case in.info.Bucket.Marker == b.ID || in.info.Bucket.Marker == b.Marker:
				return fmt.Errorf("bucket instance %s carries the id or marker of %s: %w", k, b.Key(), op.ErrBucketAlreadyExists)
			}
		}
		if !more {
			return nil
		}
		marker = next
	}
}

// listPage is the page the store's own scans of a section list in.
const listPage = 1000

// putInstance is RGWMetadataHandlerPut_BucketInstance's put_check,
// put_checked and put_post (rgw_bucket.cc:2803-2946; v20.2.4
// RGWBucketInstanceMetadataHandler::put, put_prepare and put_post,
// :2858-3060): the bucket's tenant,
// name and id come from the key; a new instance's index type from its
// placement rule, which the zone must have, as select_bucket_location_by_rule
// requires (services/svc_zone.cc:853-886; v20.2.4 :639-672); a stored
// instance keeps its explicit placement and placement rule, as radosgw's
// does; then the instance is written with the document's attrs and its
// index initialized, a shard already there counting as initialized. The
// lifecycle configuration and topic mappings radosgw updates after the
// write are not kept by rgw-go yet.
//
// Each refusal comes before anything is written:
//   - an attr outside user.rgw. is ErrInvalidArgument (op.RefuseRawAttrs),
//     and so is a NUL byte in the marker, an explicit placement pool
//     (refuseNULPlacement) or the owner, which names the owner's bucket
//     list;
//   - a stored instance keeps its marker, layout, object lock configuration
//     and the attrs rgw-go keeps for itself, op.RenameIntentAttr and
//     op.RemovingAttr; a document changing the marker, the shard count, hash
//     type or index type, or turning object lock off, is ErrInvalidArgument,
//     where radosgw writes it, which re-shards the index under its entries
//     or drops the bucket's object lock;
//   - an owner other than that of an entry point naming the instance is
//     ErrInvalidArgument, a second owner as for putEntryPoint;
//   - a new instance whose marker is not its id is ErrInvalidArgument, and
//     one whose id another instance carries as its id or marker
//     ErrBucketAlreadyExists (refuseIDElsewhere), as either shares another
//     bucket's objects or index;
//   - an id another bucket's claim holds is ErrBucketAlreadyExists, and a
//     claim another put marked with another owner, or changed since it was
//     read, ErrConcurrentModification;
//   - a bucket a live rename or a removal holds.
//
// The bucket's claim (bucketclaim.go), created for a new instance after the
// scan, is read before the entry point is checked, marked with the
// instance's owner before the instance is written and cleared after.
func (s *Store) putInstance(ctx context.Context, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	b, err := parseInstanceKey(key)
	if err != nil {
		return err
	}
	var doc meta.BucketCompleteInfo
	err = decodeDoc(e.Data, &doc)
	if err != nil {
		return err
	}
	err = op.RefuseRawAttrs(doc.Attrs)
	if err != nil {
		return err
	}
	err = refuseNULPlacement(doc.Info.Bucket)
	if err != nil {
		return err
	}
	err = op.RefuseNUL("the bucket instance's owner", doc.Info.Owner.String())
	if err != nil {
		return err
	}
	info := doc.Info
	info.Bucket.Tenant, info.Bucket.Name, info.Bucket.ID = b.Tenant, b.Name, b.ID
	attrs := maps.Clone(map[string][]byte(doc.Attrs))
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	delete(attrs, op.RenameIntentAttr)
	delete(attrs, op.RemovingAttr)
	old, err := s.readInstance(ctx, b)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		old = nil
	case err != nil:
		return err
	}
	var read meta.ObjVersion
	if old != nil {
		read = old.v.read
	}
	err = guardVersion("bucket instance "+key, old != nil, read, opts)
	if err != nil {
		return err
	}
	if old != nil {
		err = s.keepInstance(ctx, &info, attrs, old)
	} else {
		err = s.newInstance(ctx, &info)
	}
	if err != nil {
		return err
	}
	claim, err := s.claimBucket(ctx, info.Bucket)
	if err != nil {
		return err
	}
	err = s.instanceOwner(ctx, &info)
	if err != nil {
		return err
	}
	err = s.markClaim(ctx, claim, info.Owner)
	if err != nil {
		return err
	}
	v := writeVersion(read, old != nil, e)
	_, err = s.sysobj.write(ctx, s.instanceObj(info.Bucket), encodeAt(info, s.release), attrs, old == nil, e.Mtime, v)
	if err != nil {
		s.abandonMark(ctx, claim, err)
	}
	if errors.Is(err, radosclient.ErrExists) {
		return fmt.Errorf("bucket instance %s: %w", key, op.ErrConcurrentModification)
	}
	if err != nil {
		return mapBucketErr(err)
	}
	err = s.clearClaim(ctx, claim)
	if err != nil {
		return err
	}
	err = s.initIndex(ctx, &info)
	if err != nil {
		return err
	}
	slog.DebugContext(ctx, "not updating the lifecycle configuration or topic mappings of a put bucket instance (rgw_bucket.cc:2900-2944 at v19.2.6)",
		slog.String("bucket", info.Bucket.Name))
	return nil
}

// refuseNULPlacement is op.RefuseNUL over the names an instance document
// gives that name objects or pools: the marker, which prefixes the
// bucket's object names, and the explicit placement's pools.
func refuseNULPlacement(b meta.BucketID) error {
	p := b.ExplicitPlacement
	return op.RefuseNUL("the bucket instance", b.Marker,
		p.DataPool.Name, p.DataPool.NS, p.DataExtraPool.Name, p.DataExtraPool.NS, p.IndexPool.Name, p.IndexPool.NS)
}

// keepInstance is put_check's existing-instance branch and rgw-go's
// refusals for one: info takes what the stored instance old keeps.
func (s *Store) keepInstance(ctx context.Context, info *meta.BucketInfo, attrs map[string][]byte, old *instance) error {
	if err := s.busyBucket(ctx, old.info, old.attrs); err != nil {
		return err
	}
	cur, next := old.info.Layout.Current.Layout, info.Layout.Current.Layout
	switch {
	case info.Bucket.Marker != old.info.Bucket.Marker:
		return fmt.Errorf("%w: bucket instance %s: the marker changes", op.ErrInvalidArgument, old.info.Bucket.Key())
	case next.Type != cur.Type || next.Normal.NumShards != cur.Normal.NumShards || next.Normal.HashType != cur.Normal.HashType:
		return fmt.Errorf("%w: bucket instance %s: the index layout changes", op.ErrInvalidArgument, old.info.Bucket.Key())
	case old.info.ObjLockEnabled() && !info.ObjLockEnabled():
		return fmt.Errorf("%w: bucket instance %s: object lock cannot be turned off", op.ErrInvalidArgument, old.info.Bucket.Key())
	}
	info.Bucket.ExplicitPlacement = old.info.Bucket.ExplicitPlacement
	info.PlacementRule = old.info.PlacementRule
	info.Layout = old.info.Layout
	info.ObjLock = old.info.ObjLock
	for _, k := range []string{op.RenameIntentAttr, op.RemovingAttr} {
		if v, ok := old.attrs[k]; ok {
			attrs[k] = v
		}
	}
	return nil
}

// newInstance is put_check's new-instance branch and rgw-go's refusals for
// one: info's index type is its placement rule's.
func (s *Store) newInstance(ctx context.Context, info *meta.BucketInfo) error {
	if info.Bucket.Marker != info.Bucket.ID {
		return fmt.Errorf("%w: bucket instance %s: the marker of a new instance must be its id", op.ErrInvalidArgument, info.Bucket.Key())
	}
	if info.PlacementRule.Name == "" {
		return fmt.Errorf("%w: bucket instance %s names no placement rule", op.ErrInvalidArgument, info.Bucket.Key())
	}
	if _, err := s.Placement(info.PlacementRule); err != nil {
		return fmt.Errorf("%w: bucket instance %s: %w", op.ErrInvalidArgument, info.Bucket.Key(), err)
	}
	if err := s.refuseIDElsewhere(ctx, info.Bucket); err != nil {
		return err
	}
	current := &info.Layout.Current
	current.Layout.Type = meta.IndexType(s.zone.Params.PlacementPools[info.PlacementRule.Name].IndexType)
	info.Layout.Logs = nil
	if current.Layout.Type == meta.IndexNormal {
		info.Layout.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, *current)}
	}
	info.ObjLock = nil
	return nil
}

// instanceOwner refuses an owner for info other than that of the entry
// point naming its instance, when one does.
func (s *Store) instanceOwner(ctx context.Context, info *meta.BucketInfo) error {
	e, err := s.readEntryPoint(ctx, info.Bucket.Tenant, info.Bucket.Name)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return nil
	case err != nil:
		return err
	case e.ep.Bucket.ID == info.Bucket.ID && e.ep.Owner.String() != info.Owner.String():
		return fmt.Errorf("%w: bucket instance %s: the owner is not its entry point's", op.ErrInvalidArgument, info.Bucket.Key())
	}
	return nil
}

// Remove implements op.MetadataStore as RGWMetadataManager::remove's
// handler remove (rgw_metadata.cc:554-574): each section's object read and
// removed under the version read. An unknown section and a missing entry
// are ErrNoSuchKey.
func (s *Store) Remove(ctx context.Context, section, key string) error {
	if err := op.RefuseNULKey(key); err != nil {
		return err
	}
	switch section {
	case sectionUser:
		return op.RemoveMetadataUser(ctx, s, s, key)
	case sectionBucket:
		return s.removeEntryPointEntry(ctx, key)
	case sectionBucketInstance:
		return s.removeInstanceEntry(ctx, key)
	}
	return fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
}

// removeEntryPointEntry is RGWBucketMetadataHandler::do_remove
// (rgw_bucket.cc:2185-2213; remove, v20.2.4 :2489-2511), which unlinks the bucket
// from its owner and removes its entry point, logging either failure and
// answering success. rgw-go removes only an entry point whose instance is
// gone, a name that loads nothing: radosgw's removal of a live bucket's
// entry point leaves the bucket without its name, its index and objects
// reachable by no request, which is ErrConcurrentModification here. The
// owner's list entry naming the instance goes first, so a removal that
// stops between is completed by its retry, then the entry point, through
// RemoveDanglingEntryPoint, under the version read; each failure is
// returned.
func (s *Store) removeEntryPointEntry(ctx context.Context, key string) error {
	tenant, name, ok := entryPointKey(key)
	if !ok {
		return fmt.Errorf("bucket %q: %w", key, op.ErrNoSuchKey)
	}
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return noSuchKey(err)
	}
	_, err = s.readInstance(ctx, e.ep.Bucket)
	switch {
	case err == nil:
		return fmt.Errorf("bucket %s: its instance exists, so it is removed with the bucket: %w", key, op.ErrConcurrentModification)
	case !errors.Is(err, op.ErrNoSuchBucket):
		return err
	}
	if err := s.unlinkInstance(ctx, e.ep.Owner, e.ep.Bucket); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	return noSuchKey(s.RemoveDanglingEntryPoint(ctx, tenant, name))
}

// removeInstanceEntry is RGWBucketInstanceMetadataHandler::do_remove
// (rgw_bucket.cc:2707-2725): the instance read and removed under the
// version read; a missing one is ErrNoSuchKey. rgw-go removes only an
// instance no entry point names, a stale one, as removing the instance a
// bucket's name loads leaves that name loading nothing and the bucket's
// index and objects reachable by no request: ErrConcurrentModification. A
// bucket a live rename or a removal holds is refused too. The index is
// left, as radosgw leaves it. Tentacle's handler reads the instance and
// removes nothing, leaving instances to each zone's trimming
// (RGWBucketInstanceMetadataHandler::remove, v20.2.4 :3062-3080), and so
// does this on Tentacle.
func (s *Store) removeInstanceEntry(ctx context.Context, key string) error {
	b, err := parseInstanceKey(key)
	if err != nil {
		return fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	in, err := s.readInstance(ctx, b)
	if err != nil {
		return noSuchKey(err)
	}
	if s.release >= denc.Tentacle {
		return nil
	}
	err = s.busyBucket(ctx, in.info, in.attrs)
	if err != nil {
		return err
	}
	e, err := s.readEntryPoint(ctx, b.Tenant, b.Name)
	switch {
	case err == nil && e.ep.Bucket.ID == b.ID:
		return fmt.Errorf("bucket instance %s: its bucket's entry point names it: %w", key, op.ErrConcurrentModification)
	case err != nil && !errors.Is(err, op.ErrNoSuchBucket):
		return err
	}
	return s.removeInstance(ctx, b, &objv{read: in.v.read})
}

// sectionListing is a section's pool and its key for an oid, or false for
// an oid the section skips.
func (s *Store) sectionListing(section string) (meta.Pool, func(oid string) (string, bool), bool) {
	switch section {
	case sectionUser:
		// RGWSI_User_Module::is_valid_oid (svc_user_rados.cc:54-57;
		// UserLister, v20.2.4 :65-81): not a user's bucket list.
		return s.zone.Params.UserUIDPool, func(oid string) (string, bool) {
			return oid, !strings.HasSuffix(oid, bucketsSuffix)
		}, true
	case sectionBucket:
		// RGWSI_Bucket_SObj_Module::is_valid_oid (svc_bucket_sobj.cc:45-47;
		// BucketEntrypointLister, v20.2.4 :95-112): not behind a '.'.
		return s.zone.Params.DomainRoot, func(oid string) (string, bool) {
			return oid, oid != "" && oid[0] != '.'
		}, true
	case sectionBucketInstance:
		// is_valid_oid and oid_to_key (svc_bucket_sobj.cc:81-122;
		// BucketInstanceLister, v20.2.4 :130-157).
		return s.zone.Params.DomainRoot, func(oid string) (string, bool) {
			if !strings.HasPrefix(oid, bucketInstancePrefix) {
				return "", false
			}
			return instanceOIDToKey(oid), true
		}, true
	}
	return meta.Pool{}, nil, false
}

// errStopListing ends a listing's callback early.
var errStopListing = errors.New("listing stopped")

// List implements op.MetadataStore as list_keys_next over the section's
// pool (rgw_list_pool, driver/rados/rgw_tools.cc:346-395 at v19.2.6): the
// keys of the objects the section keeps after marker, a
// token an earlier List returned, until maxEntries keys are listed; next
// resumes after the last object read, and more says whether the pool holds
// more objects. A token from elsewhere, radosgw's cursors among them, is
// ErrInvalidArgument. A maxEntries of 0 lists nothing and tells only
// whether a key follows. An unknown section is ErrNoSuchKey, and a pool
// that does not exist lists nothing, as the handler takes its ENOENT
// (rgw_metadata.cc:211-227).
func (s *Store) List(ctx context.Context, section, marker string, maxEntries int) (keys []string, next string, more bool, err error) {
	poolName, keyOf, ok := s.sectionListing(section)
	if !ok {
		return nil, "", false, fmt.Errorf("metadata section %q: %w", section, op.ErrNoSuchKey)
	}
	pool, err := s.pools.get(ctx, poolName)
	if errors.Is(err, radosclient.ErrNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	token := marker
	if maxEntries <= 0 {
		_, _, err = pool.ListObjectsFrom(ctx, token, 0, func(oid, _ string) error {
			if _, ok := keyOf(oid); ok {
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
			if k, ok := keyOf(oid); ok {
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
