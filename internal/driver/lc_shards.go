package driver

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"syscall"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The lifecycle index is radosgw's: rgw_lc_max_objs shard objects lc.0,
// lc.1 and on in the zone's lc pool, each holding in its omap the entry of
// every bucket with a lifecycle configuration whose key hashes to it, and in
// its omap header the head the lifecycle workers walk it from. Every write
// to a shard is made under its lc_process lock
// (RGWLC::initialize,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L233-L254 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L233-L254 at v20.2.4;
// lc_oid_prefix and lc_index_lock_name,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L27-L30 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L27-L30 at v20.2.4).
const (
	lcOIDPrefix = "lc"
	lcLockName  = "lc_process"
	// lcCookieLen is the length of the cookie a gateway takes the lock
	// under to change a bucket's entry: 15, as RGWLC::initialize passes
	// COOKIE_LEN, 16, to gen_rand_alphanumeric as a size that counts the
	// NUL
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L45-L51 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L45-L51 at v20.2.4;
	// docs/ceph-upstream-bugs.md).
	lcCookieLen = 15
	// lcModifyRetries and lcModifyRetryWait are guard_lc_modify's: a shard
	// already locked is tried again 100 ms later, up to 500 times.
	lcModifyRetries   = 500
	lcModifyRetryWait = 100 * time.Millisecond
	// lcUnlockTimeout bounds a shard unlock, which runs past the caller's
	// context: a lock left behind would hold every gateway's lifecycle
	// writes on the shard off for rgw_lc_lock_max_time.
	lcUnlockTimeout = 30 * time.Second
)

// lcIndex is what the gateway keeps for its writes to the lifecycle index:
// RGWLC's cookie, drawn once for the life of the Store as RGWLC::initialize
// draws it for the process, and the wait between tries of a locked shard.
type lcIndex struct {
	cookie string
	sleep  func(ctx context.Context, d time.Duration) error
}

func newLCIndex() lcIndex {
	return lcIndex{cookie: randAlnum(lcCookieLen), sleep: sleepCtx}
}

var _ op.LifecycleStore = (*Store)(nil)

// lcBucketKey is get_bucket_lc_key, the key of a bucket's entry:
// "tenant:name:marker", the separators kept for an empty tenant
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1990-L1992 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2047-L2049 at v20.2.4).
func lcBucketKey(b meta.BucketID) string {
	return b.Tenant + ":" + b.Name + ":" + b.Marker
}

// lcShardIndex is get_lc_index: the shard of shards that holds key's entry,
// ceph_str_hash_linux(key) % HASH_PRIME % shards
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1966-L1976 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2023-L2033 at v20.2.4).
func lcShardIndex(key string, shards uint32) uint32 {
	return shardMod(meta.StrHashLinux(key)%lcHashPrime, shards)
}

// lcShardOID is the name of shard i, get_lc_oid's "lc.<i>".
func lcShardOID(i uint32) string {
	return lcOIDPrefix + "." + strconv.FormatUint(uint64(i), 10)
}

// lcPool is the zone's lc pool, radosgw's lc_pool_ctx.
func (s *Store) lcPool(ctx context.Context) (radosclient.Pool, error) {
	return s.pools.get(ctx, s.zone.Params.LCPool)
}

// SetEntry implements op.LifecycleStore: set_bucket_config's
// guard_lc_modify with set_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2659-L2663 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2726-L2730 at v20.2.4).
func (s *Store) SetEntry(ctx context.Context, rec *op.BucketRecord) error {
	return s.guardLCModify(ctx, rec.Info.Bucket, lcSetEntry)
}

// RemoveEntry implements op.LifecycleStore: remove_bucket_config's
// guard_lc_modify with rm_entry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2686-L2690 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2753-L2757 at v20.2.4).
func (s *Store) RemoveEntry(ctx context.Context, rec *op.BucketRecord) error {
	return s.guardLCModify(ctx, rec.Info.Bucket, lcRmEntry)
}

// guardLCModify is guard_lc_modify
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2584-L2634 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2651-L2701 at v20.2.4):
// under the lc_process lock of b's shard, taken with the gateway's cookie
// for rgw_lc_lock_max_time, modify is given the entry {key, 0, uninitial},
// and the lock is released. A shard already locked, EBUSY by another holder
// or EEXIST by this gateway's own cookie, is tried again after 100 ms, up to
// 500 times, and then the lock's error is returned; any other failure to
// lock is returned at once. radosgw also unlocks after a lock it did not
// take, which releases this gateway's cookie's hold on the shard when one
// holds it (docs/ceph-upstream-bugs.md); here only a lock taken is
// released.
func (s *Store) guardLCModify(ctx context.Context, b meta.BucketID, modify func(context.Context, radosclient.Pool, string, rgwcls.LCEntry) error) error {
	key := lcBucketKey(b)
	oid := lcShardOID(lcShardIndex(key, s.opts.lcMaxObjs))
	pool, err := s.lcPool(ctx)
	if err != nil {
		return err
	}
	if err := s.lockLCModify(ctx, pool, oid); err != nil {
		return err
	}
	defer unlockLC(ctx, pool, oid, s.lc.cookie)
	if err := modify(ctx, pool, oid, rgwcls.LCEntry{Bucket: key, Status: rgwcls.LCStatusUninitial}); err != nil {
		return fmt.Errorf("writing the lifecycle entry %s: %w", key, err)
	}
	return nil
}

// lockLCModify is guard_lc_modify's loop: every try that finds the shard
// locked waits 100 ms before the next, the last one included, as radosgw
// waits before it counts the try.
func (s *Store) lockLCModify(ctx context.Context, pool radosclient.Pool, oid string) error {
	for retries := 0; ; retries++ {
		err := pool.LockExclusive(ctx, oid, lcLockName, s.lc.cookie, "", s.opts.lcLockMaxTime, 0)
		if err == nil {
			return nil
		}
		if e := errno(err); e != syscall.EBUSY && e != syscall.EEXIST {
			return fmt.Errorf("locking lifecycle shard %s: %w", oid, err)
		}
		if serr := s.lc.sleep(ctx, lcModifyRetryWait); serr != nil {
			return fmt.Errorf("waiting for lifecycle shard %s: %w", oid, serr)
		}
		if retries == lcModifyRetries {
			return fmt.Errorf("locking lifecycle shard %s: %w", oid, err)
		}
	}
}

// unlockLC releases the lc_process lock held on oid under cookie, past ctx's
// end, logging a failure: the lock then lapses after rgw_lc_lock_max_time.
func unlockLC(ctx context.Context, pool radosclient.Pool, oid, cookie string) {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lcUnlockTimeout)
	defer cancel()
	if err := pool.Unlock(uctx, oid, lcLockName, cookie); err != nil {
		slog.DebugContext(ctx, "lifecycle shard unlock failed", slog.String("shard", oid), slog.Any("error", err))
	}
}

// The shard calls below are RadosLifecycle's, each one op on the shard
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3780-L3882 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4640-L4791 at v20.2.4).

// lcGetHead reads the shard's head, ENOENT for a shard that does not exist.
func lcGetHead(ctx context.Context, pool radosclient.Pool, oid string) (rgwcls.LCHead, error) {
	r := radosclient.NewReadOp()
	res := rgwcls.LCGetHead(r)
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return rgwcls.LCHead{}, err
	}
	return res.Result()
}

// lcPutHead writes h as the shard's head.
func lcPutHead(ctx context.Context, pool radosclient.Pool, oid string, h rgwcls.LCHead) error {
	w := radosclient.NewWriteOp()
	rgwcls.LCPutHead(w, h)
	_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
	return err
}

// lcGetEntry reads the shard's entry under key, ENOENT when it holds none.
func lcGetEntry(ctx context.Context, pool radosclient.Pool, oid, key string) (rgwcls.LCEntry, error) {
	r := radosclient.NewReadOp()
	res := rgwcls.LCGetEntry(r, key)
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return rgwcls.LCEntry{}, err
	}
	return res.Result()
}

// lcGetNextEntry reads the shard's first entry after marker, the empty entry
// past its last.
func lcGetNextEntry(ctx context.Context, pool radosclient.Pool, oid, marker string) (rgwcls.LCEntry, error) {
	r := radosclient.NewReadOp()
	res := rgwcls.LCGetNextEntry(r, marker)
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return rgwcls.LCEntry{}, err
	}
	return res.Result()
}

// lcSetEntry stores e under its bucket.
func lcSetEntry(ctx context.Context, pool radosclient.Pool, oid string, e rgwcls.LCEntry) error {
	w := radosclient.NewWriteOp()
	rgwcls.LCSetEntry(w, e)
	_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
	return err
}

// lcRmEntry removes the entry under e's bucket.
func lcRmEntry(ctx context.Context, pool radosclient.Pool, oid string, e rgwcls.LCEntry) error {
	w := radosclient.NewWriteOp()
	rgwcls.LCRmEntry(w, e)
	_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
	return err
}

// lcListEntries lists up to maxEntries of the shard's entries after marker,
// with rgwcls.LCListEntries's paging rule.
func lcListEntries(ctx context.Context, pool radosclient.Pool, oid, marker string, maxEntries uint32) (rgwcls.LCListEntriesRet, error) {
	r := radosclient.NewReadOp()
	res := rgwcls.LCListEntries(r, marker, maxEntries)
	if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil {
		return rgwcls.LCListEntriesRet{}, err
	}
	return res.Result()
}
