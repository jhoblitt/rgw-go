package driver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The OLH index operations are the OLH protocol's calls on the bucket index
// shard of a versioned key's name, which RGWRados makes beside the OLH head
// operations (olh_head.go): the link and unlink of a version under the
// name's OLH entry, the read, trim and clear of that entry's pending log,
// and bi_get of one entry. Link, unlink, trim and clear are guarded writes,
// the read-log and bi_get unguarded reads.

// bucketOLHIndex is the bucket index side of the OLH protocol for one name
// of one bucket: the shard holding the name, found from the bucket record and
// found again when a reshard moves the name, and the request fields every
// call carries. Each call takes the name's OLH head as get_obj_state read it,
// and sends the head's olh.idtag whole as its olh_tag, as radosgw copies the
// tag's bufferlist (cls_rgw_bucket_link_olh's to_str,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L513-L534 at v19.2.7)
// or a string of its whole length into each request
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8806 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9552 at v20.2.4).
// A call whose head or version names another name is refused before it is
// sent: its request would land on a shard that name's listing never reads.
// Like indexOp, it changes its shard and bucket record without a lock, so it
// serves one request and is never shared between goroutines.
type bucketOLHIndex struct {
	x *indexOp
}

// newOLHIndex returns the OLH index of key's name in rec's bucket. The shard
// is the one the name hashes to, whatever the version, as get_hash_object
// hashes the name alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_obj_types.h#L540-L542 at v19.2.7).
func (s *Store) newOLHIndex(rec *op.BucketRecord, key meta.ObjKey) *bucketOLHIndex {
	key.Instance = ""
	return &bucketOLHIndex{x: &indexOp{s: s, rec: rec, obj: meta.Obj{Bucket: rec.Info.Bucket, Key: key}}}
}

// olhLinkFields are the fields of a link its caller chooses: whether the
// version is a delete marker, the meta a marker's entry is written with, the
// zero meta of a null pointer otherwise, the epoch, 0 letting the class take
// the OLH entry's next, and unmod_since, compared in whole seconds unless
// highPrecision.
type olhLinkFields struct {
	deleteMarker  bool
	meta          rgw.DirEntryMeta
	epoch         uint64
	unmodSince    time.Time
	highPrecision bool
}

// olhUnlinkFields are the fields of an unlink its caller chooses: the epoch,
// as for a link, and nullVersion for a request that named the null version.
type olhUnlinkFields struct {
	epoch       uint64
	nullVersion bool
}

// headOLHTag is head's olh.idtag as the index calls send it: its bytes whole,
// absent read as empty.
func headOLHTag(head *op.ObjectState) string { return string(head.Attrs[meta.AttrOLHIDTag]) }

// olhKey is the index key of the name with no instance, the key the log
// calls and bi_get of the OLH entry send.
func (b *bucketOLHIndex) olhKey() rgw.ObjKey { return rgw.ObjKey{Name: b.x.obj.Key.IndexKeyName()} }

// ofName refuses key, a call's head or version, when it is not of the name
// whose shard b holds.
func (b *bucketOLHIndex) ofName(key meta.ObjKey) error {
	if key.Name == b.x.obj.Key.Name && key.NS == b.x.obj.Key.NS {
		return nil
	}
	return fmt.Errorf("%w: the olh index of %q in bucket %s was given key %q", op.ErrUnknown, b.x.obj.Key.Name, b.x.obj.Bucket.Name, key.Name)
}

// versionKey is the index key of a version, rgw_obj_key::get_index_key.
func versionKey(key meta.ObjKey) rgw.ObjKey {
	return rgw.ObjKey{Name: key.IndexKeyName(), Instance: key.Instance}
}

// linkOLH is bucket_index_link_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8130-L8176 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8876-L8922 at v20.2.4):
// bucket_link_olh of key, a version of the name, under opTag, with
// the zone's log_op and zones_trace. radosgw then adds a data log entry when
// it logs the op, which a single zone never does.
func (b *bucketOLHIndex) linkOLH(ctx context.Context, head *op.ObjectState, key meta.ObjKey, opTag string, f olhLinkFields) error {
	if err := b.ofName(key); err != nil {
		return err
	}
	l := rgw.LinkOLHOp{
		Key: versionKey(key), OLHTag: headOLHTag(head), DeleteMarker: f.deleteMarker, OpTag: opTag,
		Meta: f.meta, OLHEpoch: f.epoch, LogOp: b.x.logOp(), UnmodSince: f.unmodSince,
		HighPrecisionTime: f.highPrecision, ZonesTrace: b.x.zonesTrace(),
	}
	return b.x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.LinkOLH(w, l, b.x.s.release) })
}

// unlinkInstance is bucket_index_unlink_instance
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8184-L8223 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8930-L8969 at v20.2.4)
// with the bilog flags unlink_obj_instance gives it: VERSIONED_OP, and
// NULL_VERSION for a request that named the null version
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8814-L8818 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9560-L9564 at v20.2.4).
func (b *bucketOLHIndex) unlinkInstance(ctx context.Context, head *op.ObjectState, key meta.ObjKey, opTag string, f olhUnlinkFields) error {
	if err := b.ofName(key); err != nil {
		return err
	}
	flags := rgw.BILogFlagVersionedOp
	if f.nullVersion {
		flags |= rgw.BILogNullVersion
	}
	u := rgw.UnlinkInstanceOp{
		Key: versionKey(key), OpTag: opTag, OLHEpoch: f.epoch, LogOp: b.x.logOp(), BILogFlags: flags,
		OLHTag: headOLHTag(head), ZonesTrace: b.x.zonesTrace(),
	}
	return b.x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.UnlinkInstance(w, u, b.x.s.release) })
}

// readLog is bucket_index_read_olh_log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8225-L8269 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8971-L9015 at v20.2.4):
// the name's pending log after marker, read without the reshard guard. A
// reply that does not decode is the -EIO cls_rgw_get_olh_log's callback
// answers, UnknownError.
func (b *bucketOLHIndex) readLog(ctx context.Context, head *op.ObjectState, marker uint64) (rgw.ReadOLHLogRet, error) {
	if err := b.ofName(head.Key); err != nil {
		return rgw.ReadOLHLogRet{}, err
	}
	q := rgw.ReadOLHLogOp{OLH: b.olhKey(), VerMarker: marker, OLHTag: headOLHTag(head)}
	var ret rgw.ReadOLHLogRet
	err := b.x.readShard(ctx, func(sh indexShard) error {
		rop := radosclient.NewReadOp()
		res := rgw.ReadOLHLog(rop, q, b.x.s.release)
		if _, err := sh.pool.Read(ctx, sh.oid, rop, radosclient.OpFlagNone); err != nil {
			return err
		}
		var err error
		if ret, err = res.Result(); err != nil {
			return fmt.Errorf("%w: the olh log of %s on index shard %s: %w", op.ErrUnknown, b.x.obj.Key.Name, sh.oid, err)
		}
		return nil
	})
	return ret, err
}

// trimLog is bucket_index_trim_olh_log
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8326-L8363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9072-L9109 at v20.2.4):
// bucket_trim_olh_log of the name's epochs up to ver.
func (b *bucketOLHIndex) trimLog(ctx context.Context, head *op.ObjectState, ver uint64) error {
	if err := b.ofName(head.Key); err != nil {
		return err
	}
	t := rgw.TrimOLHLogOp{OLH: b.olhKey(), Ver: ver, OLHTag: headOLHTag(head)}
	return b.x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.TrimOLHLog(w, t, b.x.s.release) })
}

// clear is bucket_index_clear_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8365-L8395 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9111-L9141 at v20.2.4):
// bucket_clear_olh of the name's OLH entry.
func (b *bucketOLHIndex) clear(ctx context.Context, head *op.ObjectState) error {
	if err := b.ofName(head.Key); err != nil {
		return err
	}
	c := rgw.ClearOLHOp{Key: b.olhKey(), OLHTag: headOLHTag(head)}
	return b.x.guardedWrite(ctx, func(w *radosclient.WriteOp) { rgw.ClearOLH(w, c, b.x.s.release) })
}

// getOLH is bi_get_olh
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9465-L9485 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10214-L10234 at v20.2.4):
// the name's OLH entry, or radosclient.ErrNotFound when the name has none,
// the class's ENOENT from cls_cxx_map_get_val
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2655-L2659 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2823-L2827 at v20.2.4),
// once the bucket, read again, names the shard that answered it. An entry
// that does not decode is bi_get_olh's -EIO, UnknownError.
func (b *bucketOLHIndex) getOLH(ctx context.Context, head *op.ObjectState) (rgw.OLHEntry, error) {
	if err := b.ofName(head.Key); err != nil {
		return rgw.OLHEntry{}, err
	}
	var e rgw.OLHEntry
	err := b.x.readShard(ctx, func(sh indexShard) error {
		res, err := b.x.biGet(ctx, sh, b.olhKey(), rgw.BIIndexOLH)
		if err != nil {
			return err
		}
		if e, err = res.OLHEntry(); err != nil {
			return fmt.Errorf("%w: the olh entry of %s on index shard %s: %w", op.ErrUnknown, b.x.obj.Key.Name, sh.oid, err)
		}
		return nil
	})
	return e, err
}

// getInstanceEntry is the index entry of key's version as the class's
// read_key_entry resolves it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 at v20.2.4),
// through bi_get, which reads one entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2785-L2832 at v20.2.4).
// A named instance is one Instance read of its entry. The null version, the
// empty instance, is first the Instance read of the plain key, which bi_get
// answers for an empty instance; when that entry is a version marker, the
// version's entries are behind it, and Plain reads, which take the key as
// given, read the null delete marker's entry and, when it is absent, the
// null data entry. The last read's ENOENT, radosclient.ErrNotFound, is the
// answer once the bucket, read again, names the shard that gave it; on a
// shard a reshard moved the name to, the resolution starts again from the
// plain key (docs/exclusions.md, "An index read of a versioned key that finds
// its shard gone reads the shard the bucket now names"). radosgw's
// bi_get_instance reads the plain key alone, which on a converted key is the
// version marker, and reads once through its caller's bucket info. An entry
// that does not decode is bi_get_instance's -EIO, UnknownError.
func (s *Store) getInstanceEntry(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (rgw.DirEntry, error) {
	x := s.newOLHIndex(rec, key).x
	k := versionKey(key)
	var en rgw.DirEntry
	err := x.readShard(ctx, func(sh indexShard) error {
		var err error
		en, err = x.readKeyEntry(ctx, sh, k)
		return err
	})
	return en, err
}

// readKeyEntry is getInstanceEntry's one to three reads of k on sh.
func (x *indexOp) readKeyEntry(ctx context.Context, sh indexShard, k rgw.ObjKey) (rgw.DirEntry, error) {
	en, err := x.dirEntry(ctx, sh, k, rgw.BIIndexInstance)
	if err != nil || k.Instance != "" || en.Flags&rgw.FlagVerMarker == 0 {
		return en, err
	}
	en, err = x.dirEntry(ctx, sh, rgw.ObjKey{Name: rgw.InstanceIndexKey(k, true)}, rgw.BIIndexPlain)
	if !errors.Is(err, radosclient.ErrNotFound) {
		return en, err
	}
	return x.dirEntry(ctx, sh, rgw.ObjKey{Name: rgw.InstanceIndexKey(k, false)}, rgw.BIIndexPlain)
}

// biGet runs bi_get of key's entry of type typ on sh as a read op of its own.
func (x *indexOp) biGet(ctx context.Context, sh indexShard, key rgw.ObjKey, typ rgw.BIIndexType) (*rgw.BIGetResult, error) {
	rop := radosclient.NewReadOp()
	res := rgw.BIGet(rop, key, typ, x.s.release)
	if _, err := sh.pool.Read(ctx, sh.oid, rop, radosclient.OpFlagNone); err != nil {
		return nil, err
	}
	return res, nil
}

// dirEntry is the index entry bi_get of key and typ reads on sh, decoded.
func (x *indexOp) dirEntry(ctx context.Context, sh indexShard, key rgw.ObjKey, typ rgw.BIIndexType) (rgw.DirEntry, error) {
	res, err := x.biGet(ctx, sh, key, typ)
	if err != nil {
		return rgw.DirEntry{}, err
	}
	en, err := res.DirEntry()
	if err != nil {
		return rgw.DirEntry{}, fmt.Errorf("%w: the index entry %q on index shard %s: %w", op.ErrUnknown, key.Name, sh.oid, err)
	}
	return en, nil
}
