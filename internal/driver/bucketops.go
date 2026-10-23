package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/sync/errgroup"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Bucket creation and removal are RGWRados::create_bucket and
// RGWRados::delete_bucket (driver/rados/rgw_rados.cc) under
// RadosBucket::create and RadosBucket::remove (driver/rados/rgw_sal_rados.cc).

// maxCreateRetries is MAX_CREATE_RETRIES
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2371;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2476).
const maxCreateRetries = 20

// bucketID is create_bucket_id
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2344-L2352;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2447-L2455):
// "<zone id>.<instance id>.<n>", n counting from 1 per process as
// next_bucket_id does.
func (s *Store) bucketID() string {
	return fmt.Sprintf("%s.%d.%d", s.zone.Params.ID, s.cluster.InstanceID(), s.nextBucketID.Add(1))
}

// defaultLayout is init_default_bucket_layout
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2781-L2801;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L2895-L2919)
// on a fresh layout: generation 0 of indexType, hashed by Mod, with
// rgw_override_bucket_index_max_shards shards when it is set and the
// zone's bucket_index_max_shards otherwise, neither bounded
// (docs/ceph-upstream-bugs.md, "radosgw clamps a copy of
// rgw_override_bucket_index_max_shards that bucket creation never reads"),
// and the in-index log of a Normal index.
func (s *Store) defaultLayout(indexType meta.IndexType) meta.BucketLayout {
	l := meta.NewBucketLayout()
	l.Current.Gen = 0
	l.Current.Layout.Normal.HashType = meta.HashMod
	l.Current.Layout.Type = indexType
	l.Current.Layout.Normal.NumShards = s.zone.Zone.BucketIndexMaxShards
	if s.opts.overrideIndexMaxShards > 0 {
		l.Current.Layout.Normal.NumShards = s.opts.overrideIndexMaxShards
	}
	if indexType == meta.IndexNormal {
		l.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, l.Current)}
	}
	return l
}

// hasIndexObjects reports whether init_index and clean_index act on info's
// current index: Squid's create and remove its shard objects whatever the
// index type, and Tentacle's only for a Normal index
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bi_rados.cc#L354-L393 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L456-L458
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L521-L523 at v20.2.4).
func (s *Store) hasIndexObjects(info *meta.BucketInfo) bool {
	return s.release < denc.Tentacle || info.Layout.Current.Layout.Type == meta.IndexNormal
}

// initIndex is init_index for the current index: an exclusive create and
// bucket_init_index on every shard, at most rgw_bucket_index_max_aio in
// flight, a shard already there counting as initialized
// (CLSRGWIssueBucketIndexInit,
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L196-L242
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.h#L305-L314 at v19.2.6;
// IndexInitWriter,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L411-L492 at v20.2.4).
// The first failure stops further shards, and every shard this call created is
// removed again before its error is returned.
func (s *Store) initIndex(ctx context.Context, info *meta.BucketInfo) error {
	if !s.hasIndexObjects(info) {
		return nil
	}
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return err
	}
	var (
		mu      sync.Mutex
		created []string
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(s.opts.bucketIndexMaxAIO, 1))
	for _, oid := range shardOIDs(info, info.Layout.Current) {
		g.Go(func() error {
			if cerr := gctx.Err(); cerr != nil {
				return cerr
			}
			wop := radosclient.NewWriteOp()
			wop.Create(true)
			rgwcls.BucketInitIndex(wop)
			_, werr := pool.Write(gctx, oid, wop, radosclient.OpFlagNone)
			switch {
			case errors.Is(werr, radosclient.ErrExists):
				return nil
			case werr != nil:
				return fmt.Errorf("initializing index shard %s: %w", oid, op.FromRADOS(werr, op.ScopeBucket))
			}
			mu.Lock()
			created = append(created, oid)
			mu.Unlock()
			return nil
		})
	}
	err = g.Wait()
	if err == nil {
		return nil
	}
	for _, oid := range created {
		wop := radosclient.NewWriteOp()
		wop.Remove()
		if _, rerr := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); rerr != nil {
			slog.WarnContext(ctx, "could not remove an index shard after a failed initialization", slog.String("oid", oid), slog.Any("error", rerr))
		}
	}
	return err
}

// cleanIndex is clean_index for the current index: every shard removed, at
// most rgw_bucket_index_max_aio in flight, a missing one being no failure
// (CLSRGWIssueBucketIndexClean at v19.2.6; IndexCleanWriter,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L494-L558 at v20.2.4).
// It returns the first failure.
func (s *Store) cleanIndex(ctx context.Context, info *meta.BucketInfo) error {
	if !s.hasIndexObjects(info) {
		return nil
	}
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(s.opts.bucketIndexMaxAIO, 1))
	for _, oid := range shardOIDs(info, info.Layout.Current) {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			wop := radosclient.NewWriteOp()
			wop.Remove()
			if _, err := pool.Write(gctx, oid, wop, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
				return fmt.Errorf("removing index shard %s: %w", oid, err)
			}
			return nil
		})
	}
	return g.Wait()
}

// newBucketInfo is the bucket info one try of create_bucket writes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2372-L2408;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2477-L2516):
// a new bucket id as marker and id, the owner, zonegroup and placement
// asked for, requester-pays off, created now, and the default layout. A
// zero p.Quota is S3's quota that is not given, which keeps RGWBucketInfo's
// unlimited default. A bucket with object lock gets BUCKET_VERSIONED and
// BUCKET_OBJ_LOCK_ENABLED, its obj_lock left RGWObjectLock()
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2404-L2406 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2496-L2498 at v20.2.4).
func (s *Store) newBucketInfo(p op.CreateBucketParams, indexType meta.IndexType) meta.BucketInfo {
	id := s.bucketID()
	info := meta.NewBucketInfo()
	info.Bucket = meta.BucketID{Tenant: p.Tenant, Name: p.Name, Marker: id, ID: id}
	info.Owner, info.Zonegroup, info.PlacementRule = p.Owner, p.Zonegroup, p.Placement
	if p.ObjectLock {
		info.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled
	}
	info.CreationTime = meta.Time{Time: s.sysobj.now()}
	if p.Quota != (meta.Quota{}) {
		info.Quota = p.Quota
	}
	info.Layout = s.defaultLayout(indexType)
	return info
}

// discardInstance removes the instance and index of a try that lost, logging
// what it cannot remove
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2434-L2447;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2542-L2555).
func (s *Store) discardInstance(ctx context.Context, info *meta.BucketInfo) {
	if err := s.cleanIndex(ctx, info); err != nil {
		slog.WarnContext(ctx, "could not remove bucket index", slog.String("bucket", info.Bucket.Name), slog.Any("error", err))
	}
	if err := s.removeInstance(ctx, info.Bucket, nil); err != nil {
		slog.WarnContext(ctx, "failed to remove bucket instance info", slog.String("instance", info.Bucket.InstanceOID()), slog.Any("error", err))
	}
}

// CreateBucket implements op.BucketStore as RGWRados::create_bucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2354-L2458;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2457-L2566)
// with put_linked_bucket_info
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9039-L9075)
// under RadosBucket::create
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L159-L231;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L168-L248).
// Each try initializes the index, writes the instance exclusively and then
// the entry point exclusively, each under a fresh version. When the entry
// point exists, the bucket is read by name: when that finds none, the try is
// abandoned and another made with a new id, and after twenty tries the create
// fails with radosgw's -ENOENT, NoSuchKey with no message
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2455-L2457 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2563-L2565 at v20.2.4);
// otherwise this try's instance and index are removed and the existing record
// is returned with ErrBucketAlreadyExists. That existing bucket is linked to
// p.Owner when it is p.Owner's, repairing a creation that died before its
// link, and never when it is another owner's. A new bucket is linked to its
// owner, and a link that fails unlinks it again and is returned. After a link
// the entry point is read again, and when a concurrent delete has removed it,
// or it names another instance, the bucket is unlinked once more. Every
// unlink removes only an owner's entry naming the bucket's own instance.
//
// radosgw leaves an abandoned try's instance and index behind and carries
// its layout's log into the next try, and it reports a new bucket whose link
// failed as created; rgw-go removes the abandoned try's objects and returns
// the link's failure (docs/ceph-upstream-bugs.md, "radosgw's bucket creation
// retry leaks the abandoned instance and repeats its log layout" and
// "radosgw reports a bucket whose owner link failed as created").
func (s *Store) CreateBucket(ctx context.Context, p op.CreateBucketParams) (*op.BucketRecord, error) {
	pl, err := s.Placement(p.Placement)
	if err != nil {
		return nil, err
	}
	indexType := meta.IndexType(s.zone.Params.PlacementPools[pl.Rule.Name].IndexType)
	for range maxCreateRetries {
		info := s.newBucketInfo(p, indexType)
		if err := s.initIndex(ctx, &info); err != nil {
			return nil, err
		}
		mtime := info.CreationTime.Time
		v := &objv{write: newWriteVersion()}
		if err := s.writeInstance(ctx, &info, p.Attrs, true, mtime, v); err != nil {
			return nil, err
		}
		ep := meta.NewBucketEntryPoint()
		ep.Bucket, ep.Owner, ep.CreationTime, ep.Linked = info.Bucket, p.Owner, info.CreationTime, true
		epv := &objv{write: newWriteVersion()}
		err := s.writeEntryPoint(ctx, ep, nil, true, mtime, epv)
		if errors.Is(err, op.ErrBucketAlreadyExists) || errors.Is(err, op.ErrConcurrentModification) {
			existing, rerr := s.GetBucket(ctx, p.Tenant, p.Name)
			if errors.Is(rerr, op.ErrNoSuchBucket) {
				s.discardInstance(ctx, &info)
				continue
			}
			if rerr != nil {
				s.discardInstance(ctx, &info)
				return nil, rerr
			}
			if existing.Info.Bucket.ID != info.Bucket.ID {
				s.discardInstance(ctx, &info)
			}
			return s.existingBucket(ctx, p, existing)
		}
		if err != nil {
			return nil, err
		}
		rec := &op.BucketRecord{EntryPoint: ep, Info: info, Attrs: p.Attrs, Version: v.read, EPVersion: epv.read, Mtime: mtime}
		if err := s.linkBucket(ctx, p.Owner, info.Bucket, info.CreationTime.Time); err != nil {
			if uerr := s.unlinkInstance(ctx, p.Owner, info.Bucket); uerr != nil {
				slog.WarnContext(ctx, "failed to unlink bucket", slog.String("bucket", p.Name), slog.Any("error", uerr))
			}
			return nil, fmt.Errorf("linking bucket %s to %s: %w", p.Name, p.Owner, op.FromRADOS(err, op.ScopeBucket))
		}
		s.unlinkIfRemoved(ctx, p.Owner, info.Bucket)
		return rec, nil
	}
	slog.ErrorContext(ctx, "could not create bucket, continuously raced with bucket creation and removal", slog.String("bucket", p.Name))
	return nil, fmt.Errorf("%w: bucket %s: every try lost to a creation and removal", op.ErrNoSuchKey, p.Name)
}

// existingBucket is RadosBucket::create's answer for a name that exists
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L173-L228;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L183-L245):
// another owner's bucket as it is; p.Owner's own linked to it, which a link
// the class refuses as existing leaves alone, then unlinked again when its
// entry point has since gone. Either way the record comes back with
// ErrBucketAlreadyExists, which the op decides on by owner.
func (s *Store) existingBucket(ctx context.Context, p op.CreateBucketParams, existing *op.BucketRecord) (*op.BucketRecord, error) {
	exists := fmt.Errorf("bucket %s: %w", p.Name, op.ErrBucketAlreadyExists)
	if existing.Info.Owner.String() != p.Owner.String() {
		return existing, exists
	}
	b := existing.Info.Bucket
	if err := s.linkBucket(ctx, p.Owner, b, existing.Info.CreationTime.Time); err != nil {
		if errors.Is(err, radosclient.ErrExists) {
			return existing, exists
		}
		return nil, fmt.Errorf("linking bucket %s to %s: %w", p.Name, p.Owner, op.FromRADOS(err, op.ScopeBucket))
	}
	s.unlinkIfRemoved(ctx, p.Owner, b)
	return existing, exists
}

// unlinkIfRemoved reads the entry point of b once its link is written and,
// when a concurrent DELETE has removed it, unlinks b from owner, logging a
// failure
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L200-L227;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L217-L244).
// The read goes through the metadata cache, as radosgw's does, and the
// unlink removes only an entry naming b. An entry point naming another
// instance is a bucket created under the name after b was deleted. Another
// owner's bucket leaves b's entry stale, so b is unlinked, where radosgw
// keeps the entry. The same owner's bucket shares b's entry, which this
// link may have pointed back at b after that bucket's own link, so the
// entry point's instance is linked again and the owner's list names the
// live bucket.
func (s *Store) unlinkIfRemoved(ctx context.Context, owner meta.Owner, b meta.BucketID) {
	e, err := s.readEntryPoint(ctx, b.Tenant, b.Name)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		slog.WarnContext(ctx, "the bucket entry point has been deleted by a concurrent delete bucket request; unlinking the bucket", slog.String("bucket", b.Name))
	case err != nil || e.ep.Bucket.ID == b.ID:
		return
	case e.ep.Owner.String() == owner.String():
		slog.WarnContext(ctx, "the bucket entry point names the owner's newer instance; linking it again", slog.String("bucket", b.Name),
			slog.String("instance", b.ID), slog.String("entry_point", e.ep.Bucket.ID))
		if lerr := s.linkBucket(ctx, owner, e.ep.Bucket, e.ep.CreationTime.Time); lerr != nil {
			slog.WarnContext(ctx, "failed to link bucket", slog.String("bucket", b.Name), slog.Any("error", lerr))
		}
		return
	default:
		slog.WarnContext(ctx, "the bucket entry point names another owner's instance; unlinking this one", slog.String("bucket", b.Name),
			slog.String("instance", b.ID), slog.String("entry_point", e.ep.Bucket.ID))
	}
	if err := s.unlinkInstance(ctx, owner, b); err != nil {
		slog.WarnContext(ctx, "failed to unlink bucket", slog.String("bucket", b.Name), slog.Any("error", err))
	}
}

// emptinessPage is check_bucket_empty's NUM_ENTRIES.
const emptinessPage = 1000

// checkBucketEmpty is RGWRados::check_bucket_empty
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5186-L5231;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5723-L5768):
// the unordered listing, versions included, from the start, pages of a
// thousand, until an entry parses into the empty namespace, which is
// BucketNotEmpty. radosgw checks nothing for a bucket whose zonegroup id is
// not this one's; DeleteBucket, which calls this, refuses such a bucket
// first, and checks one whose empty id RGWZoneGroup::equals takes for this
// zonegroup.
func (s *Store) checkBucketEmpty(ctx context.Context, rec *op.BucketRecord) error {
	var marker listPos
	for {
		entries, truncated, last, err := s.listUnordered(ctx, rec, marker, "", emptinessPage, true)
		if err != nil {
			return err
		}
		for i := range entries {
			if k, ok := meta.ParseIndexKeyName(entries[i].Key.Name); ok && k.NS == "" {
				return fmt.Errorf("bucket %s: %w", rec.Info.Bucket.Name, op.ErrBucketNotEmpty)
			}
		}
		if !truncated || len(entries) == 0 {
			return nil
		}
		marker = last
	}
}

// maxRemoveRetries bounds the instance removals a delete or a rename
// retries after another write changed the instance.
const maxRemoveRetries = 20

// DeleteBucket implements op.BucketStore as RGWDeleteBucket's own-bucket
// steps
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3759-L3772;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3968-L3981)
// and RadosBucket::remove with delete_children false
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L350-L468;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L367-L489)
// through RGWRados::delete_bucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5238-L5302;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5936-L6018).
// It first reads the instance again by its id, as remove's load_bucket does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L356-L360;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L373-L377):
// one already gone is radosgw's -ENOENT, NoSuchKey, and the delete stops
// there. A bucket of another zonegroup, as RGWZoneGroup::equals tells it, is
// PermanentRedirect before the index is read or anything removed. radosgw
// reaches its delete with another zonegroup's bucket only for a system
// request to the master zonegroup
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L565 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L595 at v20.2.4):
// the delete the owning zonegroup's zone forwarded to the metadata master
// after checking emptiness itself
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3759-L3783
// and rgw_forward_request_to_master,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L126-L134;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3968-L3992
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L161-L169).
// rgw-go forwards nothing (docs/exclusions.md). For a bucket of this
// zonegroup it syncs the owner's stats, logging a failure, refuses a bucket
// with objects, and aborts the bucket's multipart uploads (abortMultiparts,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L395-L400;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L412-L417),
// stopping at an abort that fails; radosgw compares the raw id there and so
// skips all three for a bucket with an empty zonegroup id on the master
// zonegroup (docs/ceph-upstream-bugs.md). It then reads the entry point
// afresh, as remove's new tracker does, and leaves it when the read fails or
// it names another instance; otherwise it removes it under the version read,
// and a failure there is returned before anything else is removed, a lost
// race as op.ErrRemovalRaced once the removal's claim is cleared
// (releaseRemoval). The instance follows, under
// the version read, then the index of the generation that read names, whose
// failure is logged, the owner's list entry, whose failure is returned, and
// last the bucket's lifecycle entry (removeLCEntry). The steps from the
// entry point's read through the owner's list entry run past the end of
// ctx; the lifecycle entry's removal does not.
//
// radosgw answers a delete whose instance removal lost to another write
// with success, leaving the instance, the index and the owner's entry
// (docs/ceph-upstream-bugs.md, "radosgw's bucket delete answers success when
// its instance removal loses a race"); rgw-go reads the instance again and
// removes it under that version. radosgw unlinks the owner's entry by the
// bucket's name, the entry of a bucket re-created under it included
// (docs/ceph-upstream-bugs.md, "radosgw's bucket delete unlinks a bucket
// re-created under the same name from its owner"); rgw-go removes only an
// entry naming this instance.
func (s *Store) DeleteBucket(ctx context.Context, rec *op.BucketRecord) error {
	cur, lerr := s.GetBucketInstance(ctx, rec.Info.Bucket)
	if errors.Is(lerr, op.ErrNoSuchBucket) {
		return fmt.Errorf("%w: %w", op.ErrNoSuchKey, lerr)
	}
	if lerr != nil {
		return lerr
	}
	b := cur.Info.Bucket
	if !s.zone.ZoneGroup.Equals(cur.Info.Zonegroup) {
		return fmt.Errorf("%w: bucket %s belongs to zonegroup %q", op.ErrPermanentRedirect, b.Name, cur.Info.Zonegroup)
	}
	if _, err := s.syncOwnerStats(ctx, cur.Info.Owner, &cur.Info); err != nil {
		slog.WarnContext(ctx, "failed to sync user stats before bucket delete", slog.String("bucket", b.Name), slog.Any("error", err))
	}
	if err := s.checkBucketEmpty(ctx, cur); err != nil {
		return err
	}
	n, err := s.abortMultiparts(ctx, cur)
	if err != nil {
		return err
	}
	if n > 0 {
		slog.WarnContext(ctx, "aborted incomplete multipart uploads", slog.String("bucket", b.Name), slog.Int("count", n))
	}
	if cur, lerr = s.claimRemoval(ctx, cur); lerr != nil {
		return lerr
	}
	// From the entry point's read on, the removal runs to its end, as
	// radosgw's does: a request that ended between its steps would leave the
	// owner's list naming a bucket that is gone, and its index, and a read
	// failing for an ended request would leave the entry point while the
	// instance went.
	rctx := context.WithoutCancel(ctx)
	e, err := s.readEntryPoint(rctx, b.Tenant, b.Name)
	switch {
	case err != nil:
		if !errors.Is(err, op.ErrNoSuchBucket) {
			slog.ErrorContext(rctx, "could not read the bucket entry point; leaving it", slog.String("bucket", b.Name), slog.Any("error", err))
		}
	case b.ID != "" && e.ep.Bucket.ID != b.ID:
	default:
		if rerr := s.removeEntryPoint(rctx, b.Tenant, b.Name, &objv{read: e.v.read}); rerr != nil {
			return s.releaseRemoval(rctx, cur, rerr)
		}
	}
	if cur, err = s.removeCurrentInstance(rctx, cur); err != nil {
		return err
	}
	if err := s.cleanIndex(rctx, &cur.Info); err != nil {
		slog.WarnContext(rctx, "could not remove bucket index", slog.String("bucket", b.Name), slog.Any("error", err))
	}
	if err := s.unlinkInstance(rctx, cur.Info.Owner, b); err != nil {
		return fmt.Errorf("unlinking bucket %s from %s: %w", b.Name, cur.Info.Owner, op.FromRADOS(err, op.ScopeBucket))
	}
	s.removeLCEntry(ctx, cur)
	return nil
}

// removeLCEntry is remove's removal of the bucket's lifecycle entry, made
// when rec, the instance the delete removed, carries user.rgw.lc. A failure
// is logged and ignored, as remove casts it to void
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L402-L407 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L419-L424 at v20.2.4):
// the entry left names a bucket its name no longer loads, or loads with
// another marker, which a lifecycle pass drops as radosgw's
// bucket_lc_process does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1697-L1718 at v19.2.7
// and https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2430-L2440,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1753-L1774 at v20.2.4
// and https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2497-L2506),
// and rgw-go's lifecycle worker drops it too (lcLoadBucket and
// lcShard.postState, internal/driver/lc_worker.go).
//
// radosgw removes the entry before delete_bucket, which can still fail and
// leave the bucket with its configuration and without its entry
// (docs/ceph-upstream-bugs.md, "radosgw's bucket delete drops the
// lifecycle entry of a bucket it then fails to remove"), so DeleteBucket
// removes it as its last step, once the bucket is gone, and on the
// request's context, unlike the removal before it: its wait for a shard
// another holder locked lasts up to 50 s, which a client that leaves or a
// drain that expires must be able to end, and ending it leaves only the
// entry.
func (s *Store) removeLCEntry(ctx context.Context, rec *op.BucketRecord) {
	if _, ok := rec.Attrs[op.AttrLC]; !ok {
		return
	}
	if err := s.RemoveEntry(ctx, rec); err != nil {
		slog.WarnContext(ctx, "could not remove the removed bucket's lifecycle entry", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
}

// removeCurrentInstance removes cur's instance under the version cur was
// read with and, when another write changed it since, reads it again from
// RADOS and retries, returning the record it removed under. The read skips
// the metadata cache, which keeps the version the removal lost to until the
// writer's notify arrives. An instance already gone is no failure.
func (s *Store) removeCurrentInstance(ctx context.Context, cur *op.BucketRecord) (*op.BucketRecord, error) {
	for range maxRemoveRetries {
		err := s.removeInstance(ctx, cur.Info.Bucket, &objv{read: cur.Version})
		if !errors.Is(err, op.ErrConcurrentModification) {
			return cur, err
		}
		o := s.instanceObj(cur.Info.Bucket)
		s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
		next, rerr := s.GetBucketInstance(ctx, cur.Info.Bucket)
		if errors.Is(rerr, op.ErrNoSuchBucket) {
			return cur, nil
		}
		if rerr != nil {
			return nil, rerr
		}
		cur = next
	}
	return nil, fmt.Errorf("%w: bucket instance %s kept changing during its removal", op.ErrInternalError, cur.Info.Bucket.InstanceOID())
}

// maxUnlinkRetries bounds the unlinks unlinkInstance retries after the
// owner's entry changed between its read and its removal.
const maxUnlinkRetries = 5

// unlinkInstance removes owner's list entry for b's name only while that
// entry names b's bucket id: it reads the entry, and removes it in an op
// that first compares the entry with what was read, reading again when it
// changed in between. An entry naming another instance, a bucket re-created
// under the name, stays. radosgw removes the entry by name alone
// (cls_user_remove_bucket,
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/user/cls_user.cc#L239-L276 at both tags).
// An entry that keeps changing is left after maxUnlinkRetries reads, with a
// warning (docs/exclusions.md).
func (s *Store) unlinkInstance(ctx context.Context, owner meta.Owner, b meta.BucketID) error {
	return s.unlinkEntry(ctx, owner, b, false)
}

// unlinkEntry is unlinkInstance that, with byName, also removes an entry
// carrying no bucket id, which names whatever bucket its name loads, as
// load_bucket reads an rgw_bucket without an id by its name
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L616-L622 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L636-L641 at v20.2.4).
// A caller passes byName only when the name loads b.
func (s *Store) unlinkEntry(ctx context.Context, owner meta.Owner, b meta.BucketID, byName bool) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	for range maxUnlinkRetries {
		rop := radosclient.NewReadOp()
		res := rop.OmapGetValsByKeys([]string{b.Name})
		if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
			if errors.Is(err, radosclient.ErrNotFound) {
				return nil
			}
			return err
		}
		val, ok := res.Values[b.Name]
		if !ok {
			return nil
		}
		d := denc.NewDecoder(val)
		ent := user.DecodeBucketEntry(d)
		if err := d.Err(); err != nil {
			return fmt.Errorf("%w: the entry of bucket %s in %s: %w", op.ErrUnknown, b.Name, oid, err)
		}
		if ent.Bucket.BucketID != b.ID && (!byName || ent.Bucket.BucketID != "") {
			slog.InfoContext(ctx, "leaving the owner's entry of another instance of the bucket",
				slog.String("bucket", b.Name), slog.String("instance", b.ID), slog.String("entry", ent.Bucket.BucketID))
			return nil
		}
		wop := radosclient.NewWriteOp()
		wop.OmapCmp(b.Name, radosclient.CmpEQ, val)
		user.RemoveBucket(wop, clsBucket(b), s.release)
		_, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			// The owner's object went since the read, and its entries with it.
			return nil
		case !errors.Is(err, radosclient.ErrCanceled):
			return err
		}
	}
	slog.WarnContext(ctx, "the owner's entry of the bucket kept changing; leaving it", slog.String("bucket", b.Name), slog.String("instance", b.ID))
	return nil
}
