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
// A bare line number is v19.2.6's.

// maxCreateRetries is MAX_CREATE_RETRIES (rgw_rados.cc:2371; v20.2.4 :2476).
const maxCreateRetries = 20

// bucketID is create_bucket_id (rgw_rados.cc:2344-2352; v20.2.4
// :2447-2455): "<zone id>.<instance id>.<n>", n counting from 1 per process
// as next_bucket_id does.
func (s *Store) bucketID() string {
	return fmt.Sprintf("%s.%d.%d", s.zone.Params.ID, s.cluster.InstanceID(), s.nextBucketID.Add(1))
}

// defaultLayout is init_default_bucket_layout (rgw_bucket.cc:2781-2801;
// v20.2.4 :2895-2919) on a fresh layout: generation 0 of indexType, hashed
// by Mod, with rgw_override_bucket_index_max_shards shards when it is set
// and the zone's bucket_index_max_shards otherwise, neither bounded
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
// index type, and Tentacle's only for a Normal index (svc_bi_rados.cc:354-393
// at v19.2.6; :456-458 and :521-523 at v20.2.4).
func (s *Store) hasIndexObjects(info *meta.BucketInfo) bool {
	return s.release < denc.Tentacle || info.Layout.Current.Layout.Type == meta.IndexNormal
}

// initIndex is init_index for the current index: an exclusive create and
// bucket_init_index on every shard, at most rgw_bucket_index_max_aio in
// flight, a shard already there counting as initialized
// (CLSRGWIssueBucketIndexInit, cls_rgw_client.cc:196-242 and
// cls_rgw_client.h:305-314 at v19.2.6; IndexInitWriter, svc_bi_rados.cc:411-492
// at v20.2.4). The first failure stops further shards, and every shard this
// call created is removed again before its error is returned.
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
// svc_bi_rados.cc:494-558 at v20.2.4). It returns the first failure.
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
// (rgw_rados.cc:2372-2408; v20.2.4 :2477-2516): a new bucket id as marker
// and id, the owner, zonegroup and placement asked for, requester-pays off,
// created now, and the default layout. A zero p.Quota is S3's quota that is
// not given, which keeps RGWBucketInfo's unlimited default.
func (s *Store) newBucketInfo(p op.CreateBucketParams, indexType meta.IndexType) meta.BucketInfo {
	id := s.bucketID()
	info := meta.NewBucketInfo()
	info.Bucket = meta.BucketID{Tenant: p.Tenant, Name: p.Name, Marker: id, ID: id}
	info.Owner, info.Zonegroup, info.PlacementRule = p.Owner, p.Zonegroup, p.Placement
	info.CreationTime = meta.Time{Time: s.sysobj.now()}
	if p.Quota != (meta.Quota{}) {
		info.Quota = p.Quota
	}
	info.Layout = s.defaultLayout(indexType)
	return info
}

// discardInstance removes the instance and index of a try that lost, logging
// what it cannot remove (rgw_rados.cc:2434-2447; v20.2.4 :2542-2555).
func (s *Store) discardInstance(ctx context.Context, info *meta.BucketInfo) {
	if err := s.cleanIndex(ctx, info); err != nil {
		slog.WarnContext(ctx, "could not remove bucket index", slog.String("bucket", info.Bucket.Name), slog.Any("error", err))
	}
	if err := s.removeInstance(ctx, info.Bucket, nil); err != nil {
		slog.WarnContext(ctx, "failed to remove bucket instance info", slog.String("instance", info.Bucket.InstanceOID()), slog.Any("error", err))
	}
}

// CreateBucket implements op.BucketStore as RGWRados::create_bucket
// (rgw_rados.cc:2354-2458; v20.2.4 :2457-2566) with put_linked_bucket_info
// (:9039-9075) under RadosBucket::create (rgw_sal_rados.cc:159-231; v20.2.4
// :168-248). Each try initializes the index, writes the instance exclusively
// and then the entry point exclusively, each under a fresh version. When the
// entry point exists, the bucket is read by name: when that finds none, the
// try is abandoned and another made with a new id, and after twenty tries
// the create fails with radosgw's -ENOENT, NoSuchKey with no message
// (rgw_rados.cc:2455-2457; v20.2.4 :2563-2565); otherwise this try's instance
// and index are removed and the existing record is returned with
// ErrBucketAlreadyExists. That existing bucket is linked to p.Owner when it
// is p.Owner's, repairing a creation that died before its link, and never
// when it is another owner's. A new bucket is linked to its owner, and a
// link that fails unlinks it again and is returned. After a link the entry
// point is read again, and when a concurrent delete has removed it, or it
// names another instance, the bucket is unlinked once more. Every unlink
// removes only an owner's entry naming the bucket's own instance.
//
// radosgw leaves an abandoned try's instance and index behind and carries
// its layout's log into the next try, and it reports a new bucket whose link
// failed as created; rgw-go removes the abandoned try's objects and returns
// the link's failure (docs/ceph-upstream-bugs.md, "radosgw's bucket creation
// retry leaks the abandoned instance" and "radosgw reports a bucket whose
// owner link failed as created").
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
// (rgw_sal_rados.cc:173-228; v20.2.4 :183-245): another owner's bucket as it
// is; p.Owner's own linked to it, which a link the class refuses as existing
// leaves alone, then unlinked again when its entry point has since gone.
// Either way the record comes back with ErrBucketAlreadyExists, which the op
// decides on by owner.
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
// failure (rgw_sal_rados.cc:200-227; v20.2.4 :217-244). The read goes
// through the metadata cache, as radosgw's does, and the unlink removes
// only an entry naming b. An entry point naming another instance is a
// bucket created under the name after b was deleted. Another owner's
// bucket leaves b's entry stale, so b is unlinked, where radosgw keeps the
// entry. The same owner's bucket shares b's entry, which this link may have
// pointed back at b after that bucket's own link, so the entry point's
// instance is linked again and the owner's list names the live bucket.
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

// checkBucketEmpty is RGWRados::check_bucket_empty (rgw_rados.cc:5186-5231;
// v20.2.4 :5723-5768): nothing to check for another zonegroup's bucket; else
// the unordered listing, versions included, from the start, pages of a
// thousand, until an entry parses into the empty namespace, which is
// BucketNotEmpty.
func (s *Store) checkBucketEmpty(ctx context.Context, rec *op.BucketRecord) error {
	if rec.Info.Zonegroup != s.zone.ZoneGroup.ID {
		return nil
	}
	var marker rgwcls.ObjKey
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

// maxRemoveRetries bounds the instance removals a delete retries after
// another write changed the instance.
const maxRemoveRetries = 20

// DeleteBucket implements op.BucketStore as RGWDeleteBucket's own-bucket
// steps (rgw_op.cc:3759-3772; v20.2.4 :3986-3999) and RadosBucket::remove
// with delete_children false (rgw_sal_rados.cc:350-468; v20.2.4 :367-489)
// through RGWRados::delete_bucket (rgw_rados.cc:5238-5302; v20.2.4
// :5936-6018). It first reads the instance again by its id, as remove's
// load_bucket does (rgw_sal_rados.cc:356-360; v20.2.4 :373-377): one already
// gone is radosgw's -ENOENT, NoSuchKey, and the delete stops there. For a
// bucket of this zonegroup it syncs the owner's stats, logging a failure,
// refuses a bucket with objects, and aborts the bucket's multipart uploads
// (abortMultiparts, rgw_sal_rados.cc:395-400; v20.2.4 :412-417), stopping at
// an abort that fails. It then reads the entry point afresh,
// as remove's new tracker does, and leaves it when the read fails or it
// names another instance; otherwise it removes it under the version read,
// and a failure there, a lost race included, is returned before anything
// else is removed; the op answers that race. The instance follows, under
// the version read, then the index of the generation that read names, whose
// failure is logged, and last the owner's list entry, whose failure is
// returned.
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
	if cur.Info.Zonegroup == s.zone.ZoneGroup.ID {
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
	}
	e, err := s.readEntryPoint(ctx, b.Tenant, b.Name)
	switch {
	case err != nil:
		if !errors.Is(err, op.ErrNoSuchBucket) {
			slog.ErrorContext(ctx, "could not read the bucket entry point; leaving it", slog.String("bucket", b.Name), slog.Any("error", err))
		}
	case b.ID != "" && e.ep.Bucket.ID != b.ID:
	default:
		if rerr := s.removeEntryPoint(ctx, b.Tenant, b.Name, &objv{read: e.v.read}); rerr != nil {
			return rerr
		}
	}
	if cur, err = s.removeCurrentInstance(ctx, cur); err != nil {
		return err
	}
	if err := s.cleanIndex(ctx, &cur.Info); err != nil {
		slog.WarnContext(ctx, "could not remove bucket index", slog.String("bucket", b.Name), slog.Any("error", err))
	}
	if err := s.unlinkInstance(ctx, cur.Info.Owner, b); err != nil {
		return fmt.Errorf("unlinking bucket %s from %s: %w", b.Name, cur.Info.Owner, op.FromRADOS(err, op.ScopeBucket))
	}
	return nil
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
// (cls_user_remove_bucket, cls/user/cls_user.cc:239-276 at both tags). An
// entry that keeps changing is left after maxUnlinkRetries reads, with a
// warning (docs/exclusions.md).
func (s *Store) unlinkInstance(ctx context.Context, owner meta.Owner, b meta.BucketID) error {
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
		if ent.Bucket.BucketID != b.ID {
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
