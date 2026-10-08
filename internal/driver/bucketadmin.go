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

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// The admin bucket ops' storage steps are RGWBucketAdminOp's and
// RGWBucketCtl's (driver/rados/rgw_bucket.cc) and RadosBucket's
// (driver/rados/rgw_sal_rados.cc). A bare rgw_bucket.cc line is v19.2.6's;
// each fact cited holds at v20.2.4 too.

// categoryName is to_string(RGWObjCategory) (cls/rgw/cls_rgw_types.cc:133-143
// at v19.2.6 and v20.2.4).
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
// :190-207 at v19.2.6 and v20.2.4): "<shard>#<value>" for shards 0 on,
// joined by ",".
func shardString(values []string) string {
	items := make([]string, len(values))
	for i, v := range values {
		items[i] = strconv.Itoa(i) + "#" + v
	}
	return strings.Join(items, ",")
}

// IndexStats implements op.BucketAdminStore as RGWRados::get_bucket_stats
// over every shard (rgw_rados.cc:8872-8914 at v19.2.6): each header's
// categories summed as accumulate_raw_stats sums them (:5464-5479), and the
// headers' versions, master versions and max markers in shard order. An
// unsharded index is its one header at shard 0. Categories radosgw keys
// apart but names alike, every value past CloudTiered, are summed under
// "unknown". An indexless bucket has no headers to read.
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
		for c, hs := range h.Stats {
			name := categoryName(c)
			cs := st.Categories[name]
			cs.Size += hs.TotalSize
			cs.SizeRounded += hs.TotalSizeRounded
			cs.SizeUtilized += hs.ActualSize
			cs.NumObjects += hs.NumEntries
			st.Categories[name] = cs
		}
		vers[i] = strconv.FormatUint(h.Ver, 10)
		masters[i] = strconv.FormatUint(h.MasterVer, 10)
		markers[i] = h.MaxMarker
	}
	st.Ver, st.MasterVer, st.MaxMarker = shardString(vers), shardString(masters), shardString(markers)
	return st, nil
}

// ownerOf is the owner an ACL's owner id names, nothing for an empty id.
func ownerOf(id string) (meta.Owner, bool) {
	if id == "" {
		return meta.Owner{}, false
	}
	return meta.ParseOwner(id), true
}

// ChangeBucketOwner implements op.BucketAdminStore as RGWBucketAdminOp::link's
// storage steps (rgw_bucket.cc:1093-1184; v20.2.4 :1244-1335). A bucket
// without an ACL is radosgw's EINVAL, and one whose ACL does not decode its
// -EIO, UnknownError. Before anything is written, the entry point of the
// name the bucket ends under is read: one naming another bucket is
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
// (rgw_bucket.cc:3397-3401).
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
		return s.renameBucket(ctx, rec, pol, src, dst, owner, displayName, from)
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
// (rgw_bucket.cc:1118-1122); rgw-go returns it.
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
// the list again (done_err, rgw_bucket.cc:3397-3401).
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
// from src to dst under owner, each step done again or found done by a
// retry:
//  1. the entry point of dst is read: one naming another bucket is
//     BucketAlreadyExists, before anything is written;
//  2. the entry point of dst is created naming the bucket, or rewritten
//     under the version step 1 read, which secures the name before the
//     bucket changes: a rename that loses the name to a bucket created
//     since step 1 has written nothing. It carries the rename's record
//     too, so RemoveDanglingEntryPoint knows the rename that reserved it;
//  3. the instance under src, while there is one, gets owner, a default
//     ACL and the rename's record (op.RenameIntentAttr), under the version
//     read: a rename that loses this write to a removal's claim or to
//     RemoveDanglingEntryPoint's rewrite has changed nothing of the bucket;
//  4. the list entries naming the bucket under src go, those of the owners
//     the record names it was taken from and of its current owners; a
//     retry, which only the rename's owner can make, finds its own entry
//     under dst, which step 6 writes again;
//  5. the instance under dst is written, with the bucket's id and marker,
//     owner, the ACL and the record;
//  6. owner's list gains dst;
//  7. the entry point of src is read, and the instance under src removed;
//  8. that entry point is removed while it names the bucket, under the
//     version step 7 read;
//  9. the record is removed from the instance under dst.
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
func (s *Store) renameBucket(ctx context.Context, rec *op.BucketRecord, pol acl.Policy, src, dst meta.BucketID, owner meta.Owner, displayName string, from []string) error {
	epv, exclusive, err := s.claimName(ctx, dst)
	if err != nil {
		return err
	}
	intent := op.RenameIntent{SrcTenant: src.Tenant, SrcName: src.Name, DstTenant: dst.Tenant, DstName: dst.Name, Owner: owner.String(), From: from}
	return s.moveBucket(ctx, rec, pol, intent, owner, displayName, epv, exclusive)
}

// moveBucket is renameBucket past step 1, which read dst's entry point
// version into epv, or found none, exclusive.
func (s *Store) moveBucket(ctx context.Context, rec *op.BucketRecord, pol acl.Policy, intent op.RenameIntent, owner meta.Owner, displayName string, epv *objv, exclusive bool) error {
	src := rec.Info.Bucket
	src.Tenant, src.Name = intent.SrcTenant, intent.SrcName
	dst := rec.Info.Bucket
	dst.Tenant, dst.Name = intent.DstTenant, intent.DstName
	cur := withOwner(rec, owner, displayName, s.release)
	cur.Attrs[op.RenameIntentAttr] = intent.Encode()

	if err := s.moveClaim(ctx, src, dst); err != nil {
		return err
	}
	ep := meta.NewBucketEntryPoint()
	ep.Bucket, ep.Owner, ep.CreationTime, ep.Linked = dst, owner, cur.Info.CreationTime, true
	claim := map[string][]byte{op.RenameIntentAttr: intent.Encode()}
	if err := s.writeEntryPoint(ctx, ep, claim, exclusive, s.sysobj.now(), epv); err != nil {
		return err
	}
	cur.EntryPoint, cur.EPVersion = ep, epv.read

	if err := s.markSource(ctx, rec, &cur, src, owner, displayName, intent); err != nil {
		return err
	}
	if err := s.unlinkFrom(ctx, intent.From, rec, pol, src); err != nil {
		return err
	}

	cur.Info.Bucket = dst
	now := s.sysobj.now()
	v := &objv{write: newWriteVersion()}
	if err := s.writeInstance(ctx, &cur.Info, cur.Attrs, false, now, v); err != nil {
		return err
	}
	cur.Version, cur.Mtime = v.read, now
	if err := s.linkBucket(ctx, owner, dst, cur.Info.CreationTime.Time); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}

	if err := s.removeSource(ctx, src); err != nil {
		return err
	}

	delete(cur.Attrs, op.RenameIntentAttr)
	if err := s.PutBucketInfo(ctx, &cur); err != nil {
		return err
	}
	s.settleClaim(ctx, dst)
	*rec = cur
	return nil
}

// removeSource is renameBucket's steps 7 and 8: src's entry point read,
// the instance under src removed, then that entry point removed while it
// named the bucket, under the version read.
func (s *Store) removeSource(ctx context.Context, src meta.BucketID) error {
	oldEP, err := s.readEntryPoint(ctx, src.Tenant, src.Name)
	if err != nil && !errors.Is(err, op.ErrNoSuchBucket) {
		return err
	}
	if rerr := s.removeInstance(ctx, src, &objv{}); rerr != nil {
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

// markSource writes the rename's owner, ACL and record into the instance
// under src: cur itself when rec is that instance, else the instance read
// under src, when one is left.
func (s *Store) markSource(ctx context.Context, rec, cur *op.BucketRecord, src meta.BucketID, owner meta.Owner, displayName string, intent op.RenameIntent) error {
	if rec.Info.Bucket.Tenant == src.Tenant && rec.Info.Bucket.Name == src.Name {
		return s.PutBucketInfo(ctx, cur)
	}
	old, err := s.GetBucketInstance(ctx, src)
	if errors.Is(err, op.ErrNoSuchBucket) {
		return nil
	}
	if err != nil {
		return err
	}
	marked := withOwner(old, owner, displayName, s.release)
	marked.Attrs[op.RenameIntentAttr] = intent.Encode()
	return s.PutBucketInfo(ctx, &marked)
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
// rgw_bucket.cc:3416-3460; v20.2.4 unlink_bucket :3506-3540): owner's list
// entry for the bucket's name removed, then the entry point read, a missing
// or already unlinked one being done, one another owner holds EINVAL, and
// otherwise written with linked false under the version read. The entry is
// removed by name, as radosgw removes it, so an unlink also clears an entry
// a re-created bucket of the name left behind; radosgw logs a failure to
// remove it and goes on, where rgw-go returns it. A bucket an unfinished
// rename marks is ConcurrentModification until a retry completes it.
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
// (driver/rados/rgw_user.cc:1681-1712 at v19.2.6).
const maxChownRounds = 10

// ChownBucket implements op.BucketAdminStore as RadosBucket::chown
// (rgw_sal_rados.cc:699-751; v20.2.4 :717-769) under adopt_user_bucket's
// retries: each round loads the bucket by name, unlinks its owner, links
// owner and writes the entry point naming it, then the instance with owner
// and an ACL whose old owner's grant becomes owner's FULL_CONTROL grant,
// other grants kept; an ACL that does not decode is left as it is. A round
// that loses a version race starts over. The round loads the bucket by its
// name and refuses with ErrNoSuchBucket a name that now loads another
// instance; radosgw's adopt_user_buckets loads the instance the owner's
// list entry names by its id and points the name at it
// (driver/rados/buckets.cc:119-130 and rgw_user.cc:1714-1741 at v19.2.6).
// The entry point is written under the version the round read, where
// radosgw writes it unchecked, and a bucket an unfinished rename marks is
// ConcurrentModification, without another round.
func (s *Store) ChownBucket(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string) error {
	id := rec.Info.Bucket
	for range maxChownRounds {
		cur, err := s.GetBucket(ctx, id.Tenant, id.Name)
		if err != nil {
			return err
		}
		if cur.Info.Bucket.ID != id.ID {
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
// bucket as written.
func (s *Store) chownRound(ctx context.Context, cur *op.BucketRecord, owner meta.Owner, displayName string) error {
	b := cur.Info.Bucket
	if err := s.unlinkInstance(ctx, cur.Info.Owner, b); err != nil {
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
