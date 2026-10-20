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

// sectionBucketInstance names the section of
// RGWBucketInstanceMetadataHandler (driver/rados/rgw_bucket.cc), the bucket
// instances in the zone's domain root (services/svc_bucket_sobj.cc).
const sectionBucketInstance = "bucket.instance"

type instanceSection struct{}

func (instanceSection) get(ctx context.Context, s *Store, key string) (op.MetadataEntry, error) {
	return s.getInstance(ctx, key)
}

func (instanceSection) put(ctx context.Context, s *Store, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	return s.putInstance(ctx, key, e, opts)
}

func (instanceSection) remove(ctx context.Context, s *Store, key string) error {
	return s.removeInstanceEntry(ctx, key)
}

func (instanceSection) pool(s *Store) meta.Pool { return s.zone.Params.DomainRoot }

// oidKey is RGWSI_BucketInstance_SObj_Module's is_valid_oid and oid_to_key
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L81-L122;
// BucketInstanceLister, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L130-L157).
func (instanceSection) oidKey(oid string) (string, bool) {
	if !strings.HasPrefix(oid, bucketInstancePrefix) {
		return "", false
	}
	return instanceOIDToKey(oid), true
}

// bucketInstancePrefix is RGW_BUCKET_INSTANCE_MD_PREFIX
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L20;
// instance_oid_prefix, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L23).
const bucketInstancePrefix = ".bucket.meta."

// instanceOIDToKey is oid_to_key
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L103-L122;
// instance_oid_to_meta_key, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L44-L62):
// the oid past the prefix, its first ':' made a '/' when a second
// follows, as then the first ends the tenant.
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

// parseInstanceKey is parse_bucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L50-L86;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L51-L87),
// which the instance handler reads a key with: "[tenant/]name:id", or
// "tenant:name:id". A key naming no instance id is ErrInvalidArgument.
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

// getInstance is RGWBucketInstanceMetadataHandler::do_get
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2685-L2700;
// get, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2843-L2856).
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

// putInstance is RGWMetadataHandlerPut_BucketInstance's put_check,
// put_checked and put_post
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2803-L2946;
// v20.2.4 RGWBucketInstanceMetadataHandler::put, put_prepare and put_post,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2858-L3060):
// the bucket's tenant, name and id come from the key; a new instance's index
// type from its placement rule, which the zone must have, as
// select_bucket_location_by_rule requires
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L853-L886;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_zone.cc#L639-L672);
// a stored instance keeps its explicit placement and placement rule, as
// radosgw's does; then the instance is written with the document's attrs and
// its index initialized, a shard already there counting as initialized. The
// object lock configuration a document carries is stored, as v19.2.7's put
// stores the instance it decodes whole
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L2866-L2887 at v19.2.7),
// on Tentacle too, whose decode reads no obj_lock
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2606-L2647 at v20.2.4).
// The lifecycle configuration and topic mappings radosgw updates after the
// write are not kept by rgw-go yet.
//
// Each refusal comes before anything is written:
//   - an attr outside user.rgw. is ErrInvalidArgument (op.RefuseRawAttrs),
//     and so is a NUL byte in the marker, an explicit placement pool
//     (refuseNULPlacement) or the owner, which names the owner's bucket
//     list;
//   - a stored instance keeps its marker, layout and the attrs rgw-go keeps
//     for itself, op.RenameIntentAttr and op.RemovingAttr, and its object
//     lock configuration when the document carries none; a document
//     changing the marker, the shard count, hash type or index type, or
//     turning object lock off, is ErrInvalidArgument, where radosgw writes
//     it, which re-shards the index under its entries or drops the bucket's
//     object lock;
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
// refusals for one: info takes what the stored instance old keeps, and old's
// object lock configuration when the document carries none, where v19.2.7
// resets it to RGWObjectLock()
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346 at v19.2.7).
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
	if info.ObjLock == nil {
		info.ObjLock = old.info.ObjLock
	}
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

// removeInstanceEntry is RGWBucketInstanceMetadataHandler::do_remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2707-L2725):
// the instance read and removed under the version read; a missing one is
// ErrNoSuchKey. rgw-go removes only an instance no entry point names, a
// stale one, as removing the instance a bucket's name loads leaves that
// name loading nothing and the bucket's index and objects reachable by no
// request: ErrConcurrentModification. A bucket a live rename or a removal
// holds is refused too. The index is left, as radosgw leaves it.
// Tentacle's handler reads the instance and removes nothing, leaving
// instances to each zone's trimming
// (RGWBucketInstanceMetadataHandler::remove, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L3062-L3080),
// and so does this on Tentacle.
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
