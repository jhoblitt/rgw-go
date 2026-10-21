package driver

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The admin bucket ops' storage steps are RGWBucketAdminOp's and
// RGWBucketCtl's (driver/rados/rgw_bucket.cc) and RadosBucket's
// (driver/rados/rgw_sal_rados.cc). Each fact a v19.2.6 link cites holds at
// v20.2.4 too.

// categoryName is to_string(RGWObjCategory)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.cc#L133-L143 at v19.2.6
// and v20.2.4).
func categoryName(c uint8) string {
	switch c {
	case rgwcls.CategoryNone:
		return "rgw.none"
	case rgwcls.CategoryMain:
		return "rgw.main"
	case rgwcls.CategoryShadow:
		return "rgw.shadow"
	case rgwcls.CategoryMultiMeta:
		return "rgw.multimeta"
	case rgwcls.CategoryCloudTiered:
		return "rgw.cloudtiered"
	}
	return "unknown"
}

// shardString is BucketIndexShardsManager::to_string (cls/rgw/cls_rgw_client.h
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.h#L190-L207 at v19.2.6
// and v20.2.4): "<shard>#<value>" for shards 0 on, joined by ",".
func shardString(values []string) string {
	items := make([]string, len(values))
	for i, v := range values {
		items[i] = strconv.Itoa(i) + "#" + v
	}
	return strings.Join(items, ",")
}

// IndexStats implements op.BucketAdminStore as RGWRados::get_bucket_stats
// over every shard
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8872-L8914 at v19.2.6):
// each header's categories summed as accumulate_raw_stats sums them
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5464-L5479),
// and the headers' versions, master versions and max markers in shard
// order. An unsharded index is its one header at shard 0.
// Categories radosgw keys apart but names alike, every value past
// CloudTiered, are summed under "unknown". An indexless bucket has no
// headers to read.
func (s *Store) IndexStats(ctx context.Context, rec *op.BucketRecord) (op.BucketIndexStats, error) {
	st := op.BucketIndexStats{Categories: map[string]op.CategoryStats{}}
	if rec.Info.Layout.Current.Layout.Type != meta.IndexNormal {
		return st, nil
	}
	headers, err := s.readShardHeaders(ctx, &rec.Info)
	if err != nil {
		return op.BucketIndexStats{}, err
	}
	vers, masters, markers := make([]string, len(headers)), make([]string, len(headers)), make([]string, len(headers))
	for i, h := range headers {
		accumulateStats(st.Categories, h)
		vers[i] = strconv.FormatUint(h.Ver, 10)
		masters[i] = strconv.FormatUint(h.MasterVer, 10)
		markers[i] = h.MaxMarker
	}
	st.Ver, st.MasterVer, st.MaxMarker = shardString(vers), shardString(masters), shardString(markers)
	return st, nil
}

// accumulateStats is accumulate_raw_stats
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5464-L5479 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6196-L6211 at v20.2.4):
// h's categories added into cats.
func accumulateStats(cats map[string]op.CategoryStats, h rgwcls.DirHeader) {
	for c, hs := range h.Stats {
		name := categoryName(c)
		cs := cats[name]
		cs.Size += hs.TotalSize
		cs.SizeRounded += hs.TotalSizeRounded
		cs.SizeUtilized += hs.ActualSize
		cs.NumObjects += hs.NumEntries
		cats[name] = cs
	}
}

// eachShard runs fn on every shard object of rec's current index
// generation, at most rgw_bucket_index_max_aio at once, as radosgw's
// CLSRGWConcurrentIO does (cls_rgw_client.cc at v19.2.6; shard_io's
// async_reads and async_writes at v20.2.4). radosgw opens the index without
// looking at its layout, so an indexless bucket's calls go to the
// ".dir.<id>" object it never created.
func (s *Store) eachShard(ctx context.Context, rec *op.BucketRecord, fn func(ctx context.Context, pool radosclient.Pool, i int, oid string) error) error {
	pool, oids, err := s.indexShards(ctx, rec)
	if err != nil {
		return err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(s.opts.bucketIndexMaxAIO, 1))
	for i, oid := range oids {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			return fn(gctx, pool, i, oid)
		})
	}
	return g.Wait()
}

// CheckIndex implements op.BucketAdminStore as RGWRados::bucket_check_index
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5481-L5514 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6213-L6237
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L851-L882 at v20.2.4):
// one bucket_check_index on every shard, both headers each returns summed
// per category. A shard the OSD
// does not find is radosgw's -ENOENT, NoSuchKey, and a reply that does not
// decode its -EIO.
func (s *Store) CheckIndex(ctx context.Context, rec *op.BucketRecord) (existing, calculated map[string]op.CategoryStats, err error) {
	rets := make([]rgwcls.CheckIndexRet, len(shardOIDs(&rec.Info, rec.Info.Layout.Current)))
	err = s.eachShard(ctx, rec, func(ctx context.Context, pool radosclient.Pool, i int, oid string) error {
		rop := radosclient.NewReadOp()
		res := rgwcls.BucketCheckIndex(rop, s.release)
		if _, rerr := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); rerr != nil {
			return fmt.Errorf("checking index shard %s: %w", oid, op.FromRADOS(rerr, op.ScopeObject))
		}
		ret, derr := res.Result()
		if derr != nil {
			return fmt.Errorf("%w: checking index shard %s: %w", op.ErrUnknown, oid, derr)
		}
		rets[i] = ret
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	existing, calculated = map[string]op.CategoryStats{}, map[string]op.CategoryStats{}
	for _, r := range rets {
		accumulateStats(existing, r.ExistingHeader)
		accumulateStats(calculated, r.CalculatedHeader)
	}
	return existing, calculated, nil
}

// RebuildIndex implements op.BucketAdminStore as
// RGWRados::bucket_rebuild_index
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5516-L5527 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L894-L925 at v20.2.4):
// one bucket_rebuild_index on every shard, which writes the header its
// entries make in place of the stored one.
func (s *Store) RebuildIndex(ctx context.Context, rec *op.BucketRecord) error {
	return s.eachShard(ctx, rec, func(ctx context.Context, pool radosclient.Pool, _ int, oid string) error {
		wop := radosclient.NewWriteOp()
		rgwcls.BucketRebuildIndex(wop)
		if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
			return fmt.Errorf("rebuilding index shard %s: %w", oid, op.FromRADOS(err, op.ScopeObject))
		}
		return nil
	})
}

// RemoveIndexEntries implements op.BucketAdminStore as
// RGWRados::remove_objs_from_index
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10247-L10321 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11171-L11245 at v20.2.4),
// which removes the omap keys directly, without cls_rgw, so the shard
// headers keep counting what it removes. An indexless layout is EINVAL.
// Each key goes to the shard bucket_shard_index names
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bi_rados.h#L109-L122 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.h#L110-L123 at v20.2.4):
// by the object name for a multipart entry, the name with its last two
// "."-components removed as RGWMPObj::from_meta splits it, and the empty
// key when it cannot; by the name itself otherwise. The key removed is
// rgw_obj_key(name, instance, ns).get_oid(); the shard's keys go in one
// omap_rm_keys, in order, the shards one after another.
func (s *Store) RemoveIndexEntries(ctx context.Context, rec *op.BucketRecord, keys []meta.ObjKey) error {
	gen := rec.Info.Layout.Current
	if gen.Layout.Type == meta.IndexIndexless {
		return op.ErrInvalidArgument
	}
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		return err
	}
	if rec.Info.Bucket.ID == "" {
		return fmt.Errorf("%w: bucket %s has no bucket id", op.ErrUnknown, rec.Info.Bucket.Name)
	}
	byShard := map[uint32][]string{}
	for _, k := range keys {
		hashKey := k.Name
		if k.NS == meta.NSMultipart {
			hashKey, _, _ = meta.ParseMultipartMeta(k.Name)
		}
		shard, _ := meta.IndexShard(hashKey, gen.Layout.Normal.NumShards)
		byShard[shard] = append(byShard[shard], k.OID())
	}
	for _, shard := range slices.Sorted(maps.Keys(byShard)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		oid := rec.Info.IndexShardOID(gen, shard)
		names := slices.Compact(slices.Sorted(slices.Values(byShard[shard])))
		wop := radosclient.NewWriteOp()
		wop.OmapRmKeys(names)
		if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
			return fmt.Errorf("removing %d entries from index shard %s: %w", len(names), oid, op.FromRADOS(err, op.ScopeObject))
		}
	}
	return nil
}

// ownerOf is the owner an ACL's owner id names, nothing for an empty id.
func ownerOf(id string) (meta.Owner, bool) {
	if id == "" {
		return meta.Owner{}, false
	}
	return meta.ParseOwner(id), true
}

// ChangeBucketOwner implements op.BucketAdminStore as RGWBucketAdminOp::link's
// storage steps
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1093-L1184;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1244-L1335).
// A bucket without an ACL is radosgw's EINVAL, and one whose ACL does not
// decode its -EIO, UnknownError. Before anything is written, the entry point
// of the name the bucket ends under is read: one naming another bucket is
// BucketAlreadyExists, whether the link renames the bucket or loaded it by
// its id, where radosgw overwrites it (docs/ceph-upstream-bugs.md, "radosgw's
// bucket link overwrites the entry point of the bucket it renames onto" and
// "radosgw's bucket link by bucket id takes the name from the live bucket").
//
// Without a rename the writes are radosgw's, in its order: the owners'
// list entries for the bucket removed, the instance written with a default
// ACL for owner and owner in it, under the version rec holds, the owner's
// list linked, and the entry point written naming owner, under the version
// read, or created when there was none. A failed entry point write unlinks
// the owner's list again, as do_link_bucket's done_err does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3397-L3401).
//
// A rename runs renameBucket. A bucket an unfinished rename marks
// (op.RenameIntentAttr) is refused with ConcurrentModification unless the
// link is that rename's retry, which completes it.
func (s *Store) ChangeBucketOwner(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string, newName *meta.BucketID) error {
	raw, ok := rec.Attrs[meta.AttrACL]
	if !ok {
		return op.ErrInvalidArgument
	}
	d := denc.NewDecoder(raw)
	pol := acl.DecodePolicy(d)
	if d.Err() != nil {
		return op.ErrUnknown
	}
	src := rec.Info.Bucket
	dst := src
	if newName != nil {
		dst.Tenant, dst.Name = newName.Tenant, newName.Name
	}
	intent, live, err := s.liveRename(ctx, rec)
	if err != nil {
		return err
	}
	if live {
		if intent.DstTenant != dst.Tenant || intent.DstName != dst.Name || intent.Owner != owner.String() {
			return op.ErrConcurrentModification
		}
		src.Tenant, src.Name = intent.SrcTenant, intent.SrcName
	}
	if src.Tenant != dst.Tenant || src.Name != dst.Name {
		if _, removing := rec.Attrs[op.RemovingAttr]; removing {
			return op.ErrConcurrentModification
		}
		from := intent.From
		if !live {
			from = previousOwners(rec, pol)
		}
		return s.renameBucket(ctx, rec, pol, rename{
			src: src, dst: dst, owner: owner, displayName: displayName, retry: live,
			intent: op.RenameIntent{SrcTenant: src.Tenant, SrcName: src.Name, DstTenant: dst.Tenant, DstName: dst.Name, Owner: owner.String(), From: from},
		})
	}

	epv, exclusive, err := s.claimName(ctx, src)
	if err != nil {
		return err
	}
	if err := s.unlinkOwners(ctx, rec, pol, src); err != nil {
		return err
	}
	cur := withOwner(rec, owner, displayName, s.release)
	delete(cur.Attrs, op.RenameIntentAttr)
	if err := s.PutBucketInfo(ctx, &cur); err != nil {
		return err
	}
	if err := s.linkEntryPoint(ctx, &cur, owner, epv, exclusive); err != nil {
		return err
	}
	*rec = cur
	return nil
}

// liveRename is the rename rec's instance records and whether it is live:
// the new name has no entry point, or one naming the bucket. One naming
// another bucket means the rename lost the name, and its record is void.
func (s *Store) liveRename(ctx context.Context, rec *op.BucketRecord) (op.RenameIntent, bool, error) {
	i, ok := op.DecodeRenameIntent(rec.Attrs)
	if !ok {
		return op.RenameIntent{}, false, nil
	}
	e, err := s.readEntryPoint(ctx, i.DstTenant, i.DstName)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return i, true, nil
	case err != nil:
		return op.RenameIntent{}, false, err
	}
	return i, e.ep.Bucket.ID == rec.Info.Bucket.ID, nil
}

// claimName reads the entry point of b's name: one naming another bucket is
// BucketAlreadyExists; one naming b is written under the version read, and
// a missing one is created exclusively.
func (s *Store) claimName(ctx context.Context, b meta.BucketID) (epv *objv, exclusive bool, err error) {
	e, err := s.readEntryPoint(ctx, b.Tenant, b.Name)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return &objv{write: newWriteVersion()}, true, nil
	case err != nil:
		return nil, false, err
	case e.ep.Bucket.ID != b.ID:
		return nil, false, op.ErrBucketAlreadyExists
	}
	return &objv{read: e.v.read}, false, nil
}

// unlinkOwners removes, under name b, the list entries naming rec's
// instance of its ACL owner and of its instance owner when they differ.
// radosgw removes the ACL owner's entry by name and logs a failure
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1118-L1122);
// rgw-go returns it.
func (s *Store) unlinkOwners(ctx context.Context, rec *op.BucketRecord, pol acl.Policy, b meta.BucketID) error {
	owners := []meta.Owner{rec.Info.Owner}
	if o, ok := ownerOf(pol.Owner.ID); ok && o.String() != rec.Info.Owner.String() {
		owners = append(owners, o)
	}
	for _, o := range owners {
		if err := s.unlinkInstance(ctx, o, b); err != nil {
			return op.FromRADOS(err, op.ScopeBucket)
		}
	}
	return nil
}

// previousOwners are the owners a rename takes the bucket from: the
// instance's, and the ACL's when it differs.
func previousOwners(rec *op.BucketRecord, pol acl.Policy) []string {
	from := []string{rec.Info.Owner.String()}
	if o, ok := ownerOf(pol.Owner.ID); ok && o.String() != rec.Info.Owner.String() {
		from = append(from, o.String())
	}
	return from
}

// unlinkFrom removes, under name b, the list entries naming the bucket of
// each owner in from and of rec's instance and ACL owners: a retry of a
// rename finds the instance already given to the new owner, so the owners
// it takes the bucket from come from the rename's record.
func (s *Store) unlinkFrom(ctx context.Context, from []string, rec *op.BucketRecord, pol acl.Policy, b meta.BucketID) error {
	owners := slices.Concat(from, previousOwners(rec, pol))
	slices.Sort(owners)
	for _, o := range slices.Compact(owners) {
		if err := s.unlinkInstance(ctx, meta.ParseOwner(o), b); err != nil {
			return op.FromRADOS(err, op.ScopeBucket)
		}
	}
	return nil
}

// withOwner is rec with owner in its instance and a default ACL for owner.
func withOwner(rec *op.BucketRecord, owner meta.Owner, displayName string, rel denc.Release) op.BucketRecord {
	e := denc.NewEncoder()
	acl.DefaultPolicy(owner, displayName).Encode(e, rel)
	cur := *rec
	cur.Attrs = maps.Clone(rec.Attrs)
	if cur.Attrs == nil {
		cur.Attrs = map[string][]byte{}
	}
	cur.Attrs[meta.AttrACL] = e.Bytes()
	cur.Info.Owner = owner
	return cur
}

// linkEntryPoint is link_bucket with update_entrypoint: owner's list gains
// cur's bucket, then the entry point names it under owner, linked, written
// under epv, exclusively when exclusive. A failed entry point write unlinks
// the list again (done_err,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3397-L3401).
func (s *Store) linkEntryPoint(ctx context.Context, cur *op.BucketRecord, owner meta.Owner, epv *objv, exclusive bool) error {
	b := cur.Info.Bucket
	if err := s.linkBucket(ctx, owner, b, cur.Info.CreationTime.Time); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	ep := meta.NewBucketEntryPoint()
	ep.Bucket, ep.Owner, ep.CreationTime, ep.Linked = b, owner, cur.Info.CreationTime, true
	if err := s.writeEntryPoint(ctx, ep, nil, exclusive, s.sysobj.now(), epv); err != nil {
		if uerr := s.unlinkInstance(ctx, owner, b); uerr != nil {
			slog.WarnContext(ctx, "failed to unlink bucket", slog.String("bucket", b.Name), slog.Any("error", uerr))
		}
		return err
	}
	cur.EntryPoint, cur.EPVersion = ep, epv.read
	return nil
}

// renameBucket moves the bucket rec holds, its instance under src or dst,
// from src to dst under owner, as r describes it, each step done again or
// found done by a retry:
//  1. the entry point of dst is read: one naming another bucket is
//     BucketAlreadyExists; then the instances under src and dst are read
//     past the cache, rec, one of them, refused when RADOS holds another
//     version (readCopies), and a retry is refused when either instance
//     has changed since the rename's last attempt left it (retryable), all
//     before anything is written;
//  2. the entry point of dst is created naming the bucket, or rewritten
//     under the version step 1 read, which secures the name before the
//     bucket changes: a rename that loses the name to a bucket created
//     since step 1 has written nothing. It carries the rename's record
//     too, so RemoveDanglingEntryPoint knows the rename that reserved it;
//  3. the instance under src, while there is one, gets owner, a default
//     ACL, or on a retry the ACL it holds, and the rename's record
//     (op.RenameIntentAttr), under the version read: a rename that loses
//     this write to a removal's claim, to RemoveDanglingEntryPoint's
//     rewrite or to a write made through src since has changed nothing of
//     the bucket;
//  4. the list entries naming the bucket under src go, those of the owners
//     the record names it was taken from and of its current owners; a
//     retry, which only the rename's owner can make, finds its own entry
//     under dst, which step 6 writes again;
//  5. the instance under dst is written the same, under the version read,
//     or created when there was none (writeCopy);
//  6. owner's list gains dst;
//  7. the entry point of src is read, and the instance under src removed
//     under the version step 3 wrote, step 5 done again from it while
//     another write has changed it since (dropSource);
//  8. that entry point is removed while it names the bucket, under the
//     version step 7 read;
//  9. the record is removed from the instance under dst.
//
// Each record the rename writes onto an instance holds the versions its
// attempt leaves on both instances (op.RenameLeft), which the next retry
// compares (retryable).
//
// Before step 2 the bucket's metadata claim is moved to dst, keeping src
// as its old name, and after step 9 the old name is dropped from it
// (moveClaim and settleClaim, bucketclaim.go), so the metadata sections
// answer for the bucket under either name while the rename runs.
//
// Every instance carrying the bucket's id gets the new owner before a
// second one exists, and each is named by an entry point naming the id
// while it exists, so no two owners reach the bucket's data, and radosgw's
// stale-instance cleanup, which purges the index of an instance its name
// no longer loads (docs/ceph-upstream-bugs.md, "radosgw's stale-instance
// cleanup purges a live bucket's index"), never meets one. radosgw writes
// the instance under dst before its entry point, writes the new owner into
// that instance alone, and removes the old entry point before the old
// instance, so a failure leaves the old name to the old owner and an
// instance only the retry removes (docs/ceph-upstream-bugs.md, "radosgw's
// failed bucket rename leaves the old name to the old owner").
func (s *Store) renameBucket(ctx context.Context, rec *op.BucketRecord, pol acl.Policy, r rename) error {
	epv, exclusive, err := s.claimName(ctx, r.dst)
	if err != nil {
		return err
	}
	return s.moveBucket(ctx, rec, pol, r, epv, exclusive)
}

// rename is one rename of a bucket: from src to dst for owner, as intent
// records it, and whether it is the retry of an unfinished one.
type rename struct {
	src, dst    meta.BucketID
	intent      op.RenameIntent
	owner       meta.Owner
	displayName string
	retry       bool
}

// moveBucket is renameBucket past the entry point read of step 1, which
// read dst's entry point version into epv, or found none, exclusive.
func (s *Store) moveBucket(ctx context.Context, rec *op.BucketRecord, pol acl.Policy, r rename, epv *objv, exclusive bool) error {
	old, prev, rerr := s.readCopies(ctx, rec, r.src, r.dst)
	if rerr != nil {
		return rerr
	}
	if r.retry {
		if rerr := retryable(old, prev); rerr != nil {
			return rerr
		}
	}
	content := old
	if content == nil {
		content = prev
	}
	cur := withOwner(content, r.owner, r.displayName, s.release)
	if a := content.Attrs[meta.AttrACL]; r.retry && len(a) > 0 {
		cur.Attrs[meta.AttrACL] = a
	}
	cur.Attrs[op.RenameIntentAttr] = r.intent.Encode()

	if err := s.moveClaim(ctx, r.src, r.dst); err != nil {
		return err
	}
	ep := meta.NewBucketEntryPoint()
	ep.Bucket, ep.Owner, ep.CreationTime, ep.Linked = r.dst, r.owner, cur.Info.CreationTime, true
	claim := map[string][]byte{op.RenameIntentAttr: r.intent.Encode()}
	if err := s.writeEntryPoint(ctx, ep, claim, exclusive, s.sysobj.now(), epv); err != nil {
		return err
	}
	cur.EntryPoint, cur.EPVersion = ep, epv.read

	srcv, err := s.markSource(ctx, &cur, old, prev, r)
	if err != nil {
		return err
	}
	if err := s.unlinkFrom(ctx, r.intent.From, rec, pol, r.src); err != nil {
		return err
	}

	cur.Info.Bucket = r.dst
	if err := s.writeCopy(ctx, &cur, prev, srcv, r.intent); err != nil {
		return err
	}
	if err := s.linkBucket(ctx, r.owner, r.dst, cur.Info.CreationTime.Time); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}

	if err := s.removeSource(ctx, &cur, r.src, srcv, r.intent); err != nil {
		return err
	}

	delete(cur.Attrs, op.RenameIntentAttr)
	if err := s.PutBucketInfo(ctx, &cur); err != nil {
		return err
	}
	s.settleClaim(ctx, r.dst)
	s.moveLCEntry(ctx, &cur, r.src)
	*rec = cur
	return nil
}

// moveLCEntry files the lifecycle entry of cur, a renamed bucket carrying
// user.rgw.lc, under its new name, and removes the one under src, its old
// name, logging a failure of either: the rename is done by then. An entry's
// key holds the bucket's name, and radosgw's rename moves no entry, so the
// renamed bucket's rules stop running (docs/ceph-upstream-bugs.md,
// "radosgw's bucket rename leaves the renamed bucket out of the lifecycle
// index").
func (s *Store) moveLCEntry(ctx context.Context, cur *op.BucketRecord, src meta.BucketID) {
	if _, ok := cur.Attrs[op.AttrLC]; !ok {
		return
	}
	if err := s.SetEntry(ctx, cur); err != nil {
		slog.WarnContext(ctx, "could not file the renamed bucket's lifecycle entry", slog.String("bucket", cur.Info.Bucket.Name), slog.Any("error", err))
	}
	if err := s.RemoveEntry(ctx, &op.BucketRecord{Info: meta.BucketInfo{Bucket: src}}); err != nil {
		slog.WarnContext(ctx, "could not remove the renamed bucket's old lifecycle entry", slog.String("bucket", src.Name), slog.Any("error", err))
	}
}

// removeSource is renameBucket's steps 7 and 8: src's entry point read,
// the instance under src removed by dropSource, then that entry point
// removed while it named the bucket, under the version read.
func (s *Store) removeSource(ctx context.Context, cur *op.BucketRecord, src meta.BucketID, srcv meta.ObjVersion, intent op.RenameIntent) error {
	oldEP, err := s.readEntryPoint(ctx, src.Tenant, src.Name)
	if err != nil && !errors.Is(err, op.ErrNoSuchBucket) {
		return err
	}
	if rerr := s.dropSource(ctx, cur, src, srcv, intent); rerr != nil {
		return rerr
	}
	if oldEP == nil || oldEP.ep.Bucket.ID != src.ID {
		return nil
	}
	rerr := s.removeEntryPoint(ctx, src.Tenant, src.Name, &objv{read: oldEP.v.read})
	if rerr != nil && !errors.Is(rerr, op.ErrNoSuchBucket) {
		return rerr
	}
	return nil
}

// dropSource removes the instance under src under srcv, the version
// markSource wrote. The old name loads that instance until it goes, so a
// bucket config write made through it since is acknowledged and fails the
// removal: the instance is then read again and written whole over cur, the
// instance under dst, under the version cur holds, with a record of the
// versions read and written, and its removal tried again under the version
// read. A write through the new name since this attempt last wrote cur
// fails that copy, and maxRemoveRetries lost removals fail the rename, both
// with ConcurrentModification and both instances left, each holding what
// was written through its name; the retry then refuses until both names
// hold the same configuration (retryable). An instance already gone when it
// is read again counts as removed. radosgw removes the old instance
// unchecked
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1175-L1179 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1326-L1330 at v20.2.4),
// losing such a write (docs/ceph-upstream-bugs.md, "radosgw's bucket rename
// loses a bucket config write made through the old name").
func (s *Store) dropSource(ctx context.Context, cur *op.BucketRecord, src meta.BucketID, srcv meta.ObjVersion, intent op.RenameIntent) error {
	for range maxRemoveRetries {
		err := s.removeInstance(ctx, src, &objv{read: srcv})
		if !errors.Is(err, op.ErrConcurrentModification) {
			return err
		}
		old, err := s.rereadInstance(ctx, src)
		if errors.Is(err, op.ErrNoSuchBucket) {
			return nil
		}
		if err != nil {
			return err
		}
		next := *cur
		next.Info, next.Attrs = old.Info, maps.Clone(old.Attrs)
		next.Info.Bucket = cur.Info.Bucket
		record(&next, intent, &old.Version, afterWrite(cur.Version))
		if err := s.PutBucketInfo(ctx, &next); err != nil {
			return err
		}
		*cur, srcv = next, old.Version
	}
	return fmt.Errorf("%w: bucket instance %s kept changing as the rename removed it", op.ErrConcurrentModification, src.InstanceOID())
}

// markSource writes cur, the bucket's content with the rename's owner, ACL
// and record, into old, the instance under src, under the version old was
// read at, recording the version the mark leaves on old and the one prev,
// the instance read under dst, holds, and returns the version it wrote,
// none when no instance is left under src.
func (s *Store) markSource(ctx context.Context, cur, old, prev *op.BucketRecord, r rename) (meta.ObjVersion, error) {
	if old == nil {
		return meta.ObjVersion{}, nil
	}
	marked := *cur
	marked.Attrs = maps.Clone(cur.Attrs)
	marked.Info.Bucket = r.src
	marked.Version = old.Version
	record(&marked, r.intent, afterWrite(old.Version), versionOf(prev))
	if err := s.PutBucketInfo(ctx, &marked); err != nil {
		return meta.ObjVersion{}, err
	}
	return marked.Version, nil
}

// writeCopy writes cur, the bucket under dst, over prev, the instance read
// under dst, under the version prev was read at, or creates it when prev is
// nil, recording srcv, the version the mark left on the instance under src,
// none when there is none, and the version the write leaves: a write made
// through the new name since that read fails it rather than being
// overwritten, and so does an instance another attempt of the rename
// created since, where radosgw's exclusive write of the new instance takes
// that instance as its own
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L548-L557 at v19.2.6;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L488-L497).
func (s *Store) writeCopy(ctx context.Context, cur, prev *op.BucketRecord, srcv meta.ObjVersion, intent op.RenameIntent) error {
	var oldv *meta.ObjVersion
	if srcv.Ver != 0 {
		oldv = &srcv
	}
	if prev != nil {
		cur.Version = prev.Version
		record(cur, intent, oldv, afterWrite(prev.Version))
		return s.PutBucketInfo(ctx, cur)
	}
	now := s.sysobj.now()
	v := &objv{write: newWriteVersion()}
	record(cur, intent, oldv, &v.write)
	_, err := s.sysobj.write(ctx, s.instanceObj(cur.Info.Bucket), encodeAt(cur.Info, s.release), cur.Attrs, true, now, v)
	switch {
	case errors.Is(err, radosclient.ErrExists):
		return fmt.Errorf("%w: bucket instance %s was created since the rename read it", op.ErrConcurrentModification, cur.Info.Bucket.InstanceOID())
	case err != nil:
		return mapBucketErr(err)
	}
	cur.Version, cur.Mtime = v.read, now
	return nil
}

// record sets rec's rename record to intent with the versions an attempt
// leaves on the instance under the old name, oldv, and under the new, newv,
// each nil for a name without one.
func record(rec *op.BucketRecord, intent op.RenameIntent, oldv, newv *meta.ObjVersion) {
	intent.Left = &op.RenameLeft{Old: oldv, New: newv}
	rec.Attrs[op.RenameIntentAttr] = intent.Encode()
}

// afterWrite is the version a write guarded by v leaves: cls_version's inc
// keeps the tag and advances the counter
// (https://github.com/ceph/ceph/blob/v19.2.6/src/cls/version/cls_version.cc#L148-L170 at v19.2.6
// and v20.2.4; obj_version::inc,
// https://github.com/ceph/ceph/blob/v19.2.6/src/cls/version/cls_version_types.h#L33-L34 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/version/cls_version_types.h#L41-L42 at v20.2.4),
// as objv.applyWrite takes it. The zero version guards nothing, and what
// such a write leaves is not known: nil.
func afterWrite(v meta.ObjVersion) *meta.ObjVersion {
	if v.Ver == 0 {
		return nil
	}
	return &meta.ObjVersion{Ver: v.Ver + 1, Tag: v.Tag}
}

// versionOf is the version rec was read at, nil for no instance.
func versionOf(rec *op.BucketRecord) *meta.ObjVersion {
	if rec == nil {
		return nil
	}
	return &rec.Version
}

// readCopies are the bucket's instances under src, old, and under dst,
// prev, each nil when there is none, read past the metadata cache, which
// keeps a copy another gateway has changed until that gateway's notify
// arrives: rec where it is the one, else the one read, whose version
// guards the rename's write of it. rec is what the request loaded, through
// the cache, and checked, so one RADOS no longer holds is
// ConcurrentModification before anything is written; the read drops the
// cached copy, so a retry loads the instance afresh. radosgw's link loads
// the bucket through its cache and writes nothing under its version
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1044
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1138-L1144 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1195
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1289-L1295 at v20.2.4).
func (s *Store) readCopies(ctx context.Context, rec *op.BucketRecord, src, dst meta.BucketID) (old, prev *op.BucketRecord, err error) {
	read := func(b meta.BucketID) (*op.BucketRecord, error) {
		r, rerr := s.rereadInstance(ctx, b)
		if rec.Info.Bucket.Tenant != b.Tenant || rec.Info.Bucket.Name != b.Name {
			if errors.Is(rerr, op.ErrNoSuchBucket) {
				return nil, nil
			}
			return r, rerr
		}
		switch {
		case errors.Is(rerr, op.ErrNoSuchBucket), rerr == nil && r.Version != rec.Version:
			return nil, fmt.Errorf("%w: bucket instance %s changed since the request loaded it", op.ErrConcurrentModification, b.InstanceOID())
		case rerr != nil:
			return nil, rerr
		}
		return rec, nil
	}
	if old, err = read(src); err != nil {
		return nil, nil, err
	}
	if prev, err = read(dst); err != nil {
		return nil, nil, err
	}
	return old, prev, nil
}

// retryable is nil when a retry of the rename may copy old, the bucket's
// instance under the old name, onto prev, the one under the new name, and
// ConcurrentModification otherwise, before anything is written. The retry
// merges nothing: it goes on when
//   - one record either instance carries names the versions both hold now,
//     so neither has changed since that attempt left it;
//   - only one instance is there, so nothing can be lost or reconciled; or
//   - both have been written since every record and hold the same
//     configuration (sameConfig), which is how an operator reconciles the
//     two after a refusal: the configuration they want, written through
//     each name.
//
// Any other change to either instance, a config write, an ACL or an attr
// through either name or a concurrent retry's write, is refused, so no
// write acknowledged through one name is overwritten by the other's. radosgw
// checks nothing: its retry writes the new instance exclusively and, finding
// it, leaves it as it is
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1138-L1144,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L548-L557 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1289-L1295
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L488-L497 at v20.2.4),
// then removes the old one unchecked
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1175-L1179 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1326-L1330 at v20.2.4),
// losing a write made through the old name between the attempts
// (docs/ceph-upstream-bugs.md, "radosgw's bucket rename loses a bucket config
// write made through the old name").
func retryable(old, prev *op.BucketRecord) error {
	if old == nil || prev == nil {
		return nil
	}
	oldSeen, prevSeen := false, false
	for _, rec := range []*op.BucketRecord{old, prev} {
		i, ok := op.DecodeRenameIntent(rec.Attrs)
		if !ok || i.Left == nil {
			continue
		}
		o, p := sameVersion(i.Left.Old, old), sameVersion(i.Left.New, prev)
		if o && p {
			return nil
		}
		oldSeen, prevSeen = oldSeen || o, prevSeen || p
	}
	if !oldSeen && !prevSeen && sameConfig(old, prev) {
		return nil
	}
	return fmt.Errorf("%w: bucket %s changed under its old or new name since the rename's last attempt; write the configuration it should keep through both names, then retry",
		op.ErrConcurrentModification, old.Info.Bucket.Key())
}

// sameVersion reports whether v is the version rec holds.
func sameVersion(v *meta.ObjVersion, rec *op.BucketRecord) bool {
	return v != nil && *v == rec.Version
}

// sameConfig reports whether a and b, the bucket's instances under its two
// names, hold the same configuration: the info but for the name, encoded at
// the newest release, whose encoding carries every field, and the attrs but
// for the rename's record.
func sameConfig(a, b *op.BucketRecord) bool {
	ai, bi := a.Info, b.Info
	ai.Bucket.Tenant, ai.Bucket.Name = "", ""
	bi.Bucket.Tenant, bi.Bucket.Name = "", ""
	if !bytes.Equal(encodeAt(ai, denc.Tentacle), encodeAt(bi, denc.Tentacle)) {
		return false
	}
	without := func(attrs map[string][]byte) map[string][]byte {
		m := maps.Clone(attrs)
		delete(m, op.RenameIntentAttr)
		return m
	}
	return maps.EqualFunc(without(a.Attrs), without(b.Attrs), bytes.Equal)
}

// RemoveDanglingEntryPoint implements op.BucketAdminStore. The entry point
// of tenant/name is read; when it records the rename that reserved it, the
// instance under that rename's old name is read: one carrying a live record
// is the rename's, ErrRenamePending, and any other is rewritten under the
// version read, so a resumed rename that read it before fails its guarded
// mark, and one that marks it first fails this rewrite. Then the instance
// the entry point names must not exist, and the entry point is removed
// under the version read, which a rename claiming the name after the read
// fails. radosgw frees no such name (docs/exclusions.md, "Bucket link and
// unlink write differently from radosgw").
func (s *Store) RemoveDanglingEntryPoint(ctx context.Context, tenant, name string) error {
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return err
	}
	if i, ok := op.DecodeRenameIntent(e.attrs); ok {
		src := meta.BucketID{Tenant: i.SrcTenant, Name: i.SrcName, ID: e.ep.Bucket.ID}
		old, gerr := s.GetBucketInstance(ctx, src)
		switch {
		case gerr == nil:
			if _, live, lerr := s.liveRename(ctx, old); lerr != nil || live {
				return cmp.Or(lerr, op.ErrRenamePending)
			}
			if perr := s.PutBucketInfo(ctx, old); perr != nil {
				return perr
			}
		case !errors.Is(gerr, op.ErrNoSuchBucket):
			return gerr
		}
	}
	_, err = s.GetBucketInstance(ctx, e.ep.Bucket)
	switch {
	case err == nil:
		return op.ErrConcurrentModification
	case !errors.Is(err, op.ErrNoSuchBucket):
		return err
	}
	return s.removeEntryPoint(ctx, tenant, name, &objv{read: e.v.read})
}

// claimRemoval is a removal's first write: the bucket's instance, which
// must carry no live rename record, rewritten with op.RemovingAttr under
// the version read, read again past another writer's change. A rename that
// read the instance before fails its guarded write of the record, and one
// that reads it after refuses the bucket, so a removal and a rename never
// both change it; a rename's record written first fails this write and is
// then found. The attr's value is new to each removal, so releaseRemoval
// tells its own claim from a later removal's. It returns cur at the
// version written.
func (s *Store) claimRemoval(ctx context.Context, cur *op.BucketRecord) (*op.BucketRecord, error) {
	claim := []byte(randAlnum(writeTagLen))
	for range maxRemoveRetries {
		if _, live, err := s.liveRename(ctx, cur); err != nil || live {
			return nil, cmp.Or(err, op.ErrRenamePending)
		}
		marked := *cur
		marked.Attrs = maps.Clone(cur.Attrs)
		if marked.Attrs == nil {
			marked.Attrs = map[string][]byte{}
		}
		marked.Attrs[op.RemovingAttr] = claim
		err := s.PutBucketInfo(ctx, &marked)
		if err == nil {
			return &marked, nil
		}
		if !errors.Is(err, op.ErrConcurrentModification) {
			return nil, err
		}
		next, err := s.rereadInstance(ctx, cur.Info.Bucket)
		if errors.Is(err, op.ErrNoSuchBucket) {
			return nil, fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
		}
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return nil, fmt.Errorf("%w: bucket instance %s kept changing as its removal began", op.ErrInternalError, cur.Info.Bucket.InstanceOID())
}

// rereadInstance is the instance of b read past the cache, after a guarded
// write of it lost to another writer.
func (s *Store) rereadInstance(ctx context.Context, b meta.BucketID) (*op.BucketRecord, error) {
	o := s.instanceObj(b)
	s.sysobj.cache.invalidateRemove(normalName(o.pool, o.oid))
	return s.GetBucketInstance(ctx, b)
}

// releaseRemoval is a removal that failed after claimRemoval at err: one
// that lost its entry point to another write clears its claim from the
// instance and answers op.ErrRemovalRaced, as the bucket stays; any other
// failure leaves the claim for the removal's retry. The claim is removed
// under the version read, starting from the one the claim wrote, so the
// winner's own write of the instance, a link's or a chown's new owner, is
// kept; past such a write the instance is read again, and its claim removed
// while it is still this removal's, and left when a later removal's or
// none is there.
func (s *Store) releaseRemoval(ctx context.Context, cur *op.BucketRecord, err error) error {
	if !errors.Is(err, op.ErrConcurrentModification) {
		return err
	}
	claim := cur.Attrs[op.RemovingAttr]
	for range maxRemoveRetries {
		released := *cur
		released.Attrs = maps.Clone(cur.Attrs)
		delete(released.Attrs, op.RemovingAttr)
		perr := s.PutBucketInfo(ctx, &released)
		if perr == nil {
			return op.ErrRemovalRaced
		}
		if !errors.Is(perr, op.ErrConcurrentModification) {
			slog.WarnContext(ctx, "could not clear a lost removal's claim", slog.String("bucket", cur.Info.Bucket.Name), slog.Any("error", perr))
			return op.ErrRemovalRaced
		}
		next, gerr := s.rereadInstance(ctx, cur.Info.Bucket)
		if gerr != nil {
			if !errors.Is(gerr, op.ErrNoSuchBucket) {
				slog.WarnContext(ctx, "could not read a lost removal's claim", slog.String("bucket", cur.Info.Bucket.Name), slog.Any("error", gerr))
			}
			return op.ErrRemovalRaced
		}
		if !bytes.Equal(next.Attrs[op.RemovingAttr], claim) {
			return op.ErrRemovalRaced
		}
		cur = next
	}
	slog.WarnContext(ctx, "a lost removal's claim kept changing; leaving it", slog.String("bucket", cur.Info.Bucket.Name))
	return op.ErrRemovalRaced
}

// UnlinkBucketOwner implements op.BucketAdminStore as
// RGWBucketCtl::unlink_bucket with update_entrypoint (do_unlink_bucket,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3416-L3460;
// v20.2.4 unlink_bucket
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L3506-L3540):
// owner's list entry for the bucket's name removed, then the entry point
// read, a missing or already unlinked one being done, one another owner
// holds EINVAL, and otherwise written with linked false under the version
// read. The entry is removed by name, as radosgw removes it, so an unlink
// also clears an entry a re-created bucket of the name left behind; radosgw
// logs a failure to remove it and goes on, where rgw-go returns it. A
// bucket an unfinished rename marks is ConcurrentModification until a retry
// completes it.
func (s *Store) UnlinkBucketOwner(ctx context.Context, rec *op.BucketRecord, owner meta.Owner) error {
	if _, live, err := s.liveRename(ctx, rec); err != nil || live {
		return cmp.Or(err, op.ErrRenamePending)
	}
	b := rec.Info.Bucket
	if err := s.unlinkBucket(ctx, owner, b); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	e, err := s.readEntryPoint(ctx, b.Tenant, b.Name)
	switch {
	case errors.Is(err, op.ErrNoSuchBucket):
		return nil
	case err != nil:
		return err
	case !e.ep.Linked:
		return nil
	case e.ep.Owner.String() != owner.String():
		return op.ErrInvalidArgument
	}
	ep := e.ep
	ep.Linked = false
	return s.writeEntryPoint(ctx, ep, e.attrs, false, s.sysobj.now(), &objv{read: e.v.read})
}

// maxChownRounds is adopt_user_bucket's retry count
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1681-L1712 at v19.2.6).
const maxChownRounds = 10

// ChownBucket implements op.BucketAdminStore as RadosBucket::chown
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L699-L751 at v19.2.6;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L717-L769)
// under adopt_user_bucket's retries: each round loads the bucket by name,
// unlinks its owner, links owner and writes the entry point naming it, then
// the instance with owner and an ACL whose old owner's grant becomes
// owner's FULL_CONTROL grant, other grants kept; an ACL that does not
// decode is left as it is. A round that loses a version race starts over.
// rec names the instance to chown and the owner it is taken from, for an
// adoption the instance the owner's list entry names by its id. Every round
// loads the bucket by its name and refuses with ErrNoSuchBucket a name that
// now loads an instance other than rec's, or rec's instance under an owner
// other than rec's; radosgw's adopt_user_bucket loads that instance by its
// id in each round and chowns it from whoever holds it, pointing the name
// at it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L119-L130
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1681-L1742 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_user.cc#L1687-L1748 at v20.2.4).
// The entry point is written under the version the round read, where
// radosgw writes it unchecked, and a bucket an unfinished rename marks is
// ConcurrentModification, without another round.
func (s *Store) ChownBucket(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string) error {
	id, from := rec.Info.Bucket, rec.Info.Owner.String()
	for range maxChownRounds {
		cur, err := s.GetBucket(ctx, id.Tenant, id.Name)
		if err != nil {
			return err
		}
		if cur.Info.Bucket.ID != id.ID || cur.Info.Owner.String() != from {
			return op.ErrNoSuchBucket
		}
		if _, live, lerr := s.liveRename(ctx, cur); lerr != nil || live {
			return cmp.Or(lerr, op.ErrRenamePending)
		}
		err = s.chownRound(ctx, cur, owner, displayName)
		if errors.Is(err, op.ErrConcurrentModification) {
			continue
		}
		if err != nil {
			return err
		}
		*rec = *cur
		return nil
	}
	return op.ErrConcurrentModification
}

// chownRound is one RadosBucket::chown over cur, which it leaves at the
// bucket as written. cur was loaded by its name, so the old owner's entry
// without an id names it too.
func (s *Store) chownRound(ctx context.Context, cur *op.BucketRecord, owner meta.Owner, displayName string) error {
	b := cur.Info.Bucket
	if err := s.unlinkEntry(ctx, cur.Info.Owner, b, true); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	if err := s.linkBucket(ctx, owner, b, cur.Info.CreationTime.Time); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	ep := meta.NewBucketEntryPoint()
	ep.Bucket, ep.Owner, ep.CreationTime, ep.Linked = b, owner, cur.Info.CreationTime, true
	epv := &objv{read: cur.EPVersion}
	if err := s.writeEntryPoint(ctx, ep, nil, false, s.sysobj.now(), epv); err != nil {
		return err
	}
	cur.EntryPoint, cur.EPVersion = ep, epv.read
	cur.Info.Owner = owner
	if raw, ok := cur.Attrs[meta.AttrACL]; ok {
		d := denc.NewDecoder(raw)
		pol := acl.DecodePolicy(d)
		if d.Err() == nil {
			pol.ACL.RemoveCanonUserGrant(pol.Owner.ID)
			pol.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: owner.String(), Name: displayName, Permission: acl.PermFullControl})
			pol.Owner = acl.Owner{ID: owner.String(), DisplayName: displayName}
			e := denc.NewEncoder()
			pol.Encode(e, s.release)
			cur.Attrs = maps.Clone(cur.Attrs)
			cur.Attrs[meta.AttrACL] = e.Bytes()
		}
	}
	return s.PutBucketInfo(ctx, cur)
}

// SyncOwnerStats implements op.BucketAdminStore as rgw_sync_all_stats.
func (s *Store) SyncOwnerStats(ctx context.Context, owner meta.Owner) error {
	return s.syncAllStats(ctx, owner)
}
