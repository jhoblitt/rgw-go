package driver

import (
	"cmp"
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Bucket and owner stats and their quota caches are rgw_quota.cc's
// RGWQuotaHandlerImpl, RGWBucketStatsCache and RGWOwnerStatsCache. A bare
// line number is rgw_quota.cc's at v19.2.6; the code is the same at v20.2.4
// unless noted.

// roundedObjSize is rgw_rounded_objsize (rgw_common.h:1635-1638 at v19.2.6,
// :1637-1640 at v20.2.4): b rounded up to 4 KiB.
func roundedObjSize(b uint64) uint64 { return (b + 4095) &^ 4095 }

// quotaEntry is RGWQuotaCacheStats (:41-45). A zero asyncRefresh means a
// refresh is under way, or failed and left the entry to expire.
type quotaEntry[K comparable] struct {
	key          K
	stats        op.Stats
	expiration   time.Time
	asyncRefresh time.Time
	elem         *list.Element
}

// quotaCache is RGWQuotaCache<T> over lru_map (:47-223,
// common/lru_map.h): at most size entries, a negative size meaning no limit
// as lru_map's size_t reads it, each trusted for ttl after it is set and
// refreshed in the background once half the ttl has passed. A find moves an
// entry to the front, and setting one evicts from the back.
type quotaCache[K comparable] struct {
	mu        sync.Mutex
	entries   map[K]*quotaEntry[K]
	lru       *list.List
	size      int
	ttl, half time.Duration
	now       func() time.Time
	refreshes sync.WaitGroup
	// closed closes at close, canceling the refreshes under way.
	closed chan struct{}
}

// newQuotaCache returns a cache of size entries trusted for ttl. radosgw
// halves rgw_bucket_quota_ttl as an integer count of seconds (:139).
func newQuotaCache[K comparable](size int, ttl time.Duration, now func() time.Time) *quotaCache[K] {
	return &quotaCache[K]{
		entries: map[K]*quotaEntry[K]{}, lru: list.New(), size: size,
		ttl: ttl, half: ttl / time.Second / 2 * time.Second, now: now,
		closed: make(chan struct{}),
	}
}

// get is get_stats (:145-171): a held entry whose refresh time has passed
// starts a refresh in the background, one per entry until it sets the entry
// again; a held entry before its expiration is served; otherwise fetch runs
// now, ENOENT being zero stats, and its stats are set. One call can both
// start a refresh and fetch.
func (c *quotaCache[K]) get(ctx context.Context, key K, fetch func(context.Context) (op.Stats, error)) (op.Stats, error) {
	c.mu.Lock()
	now := c.now()
	if e, ok := c.entries[key]; ok {
		c.lru.MoveToFront(e.elem)
		if !e.asyncRefresh.IsZero() && !now.Before(e.asyncRefresh) && !c.isClosed() {
			e.asyncRefresh = time.Time{}
			c.refreshes.Add(1)
			// The refresh outlives the request that started it, and stops
			// when its fetch returns or close cancels it.
			go c.refresh(context.WithoutCancel(ctx), key, fetch)
		}
		if e.expiration.After(now) {
			st := e.stats
			c.mu.Unlock()
			return st, nil
		}
	}
	c.mu.Unlock()
	st, err := fetch(ctx)
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		st = op.Stats{}
	case err != nil:
		return op.Stats{}, err
	}
	c.set(key, st)
	return st, nil
}

// refresh is the async refresh's completion (:114-130): a fetched result
// sets the entry, held or not, and a failure is only logged, leaving the
// entry without a refresh time until it expires.
func (c *quotaCache[K]) refresh(ctx context.Context, key K, fetch func(context.Context) (op.Stats, error)) {
	defer c.refreshes.Done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Stops when the refresh returns, which cancels ctx.
	go func() {
		select {
		case <-c.closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	st, err := fetch(ctx)
	if err != nil {
		slog.DebugContext(ctx, "quota stats refresh failed", slog.Any("key", key), slog.Any("error", err))
		return
	}
	c.set(key, st)
}

// set is set_stats (:133-142) and lru_map::add.
func (c *quotaCache[K]) set(key K, st op.Stats) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	e, ok := c.entries[key]
	if ok {
		c.lru.MoveToFront(e.elem)
	} else {
		e = &quotaEntry[K]{key: key}
		e.elem = c.lru.PushFront(e)
		c.entries[key] = e
	}
	e.stats, e.expiration, e.asyncRefresh = st, now.Add(c.ttl), now.Add(c.half)
	for c.size >= 0 && c.lru.Len() > c.size {
		if back, ok := c.lru.Remove(c.lru.Back()).(*quotaEntry[K]); ok {
			delete(c.entries, back.key)
		}
	}
}

// adjust is adjust_stats with RGWQuotaStatsUpdate (:174-223): a held
// entry's fields move by the deltas, each computed in 64-bit unsigned
// arithmetic and floored at zero when it reads as negative. It reports
// whether the cache held the entry.
func (c *quotaCache[K]) adjust(key K, objs, added, removed int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return false
	}
	c.lru.MoveToFront(e.elem)
	// radosgw's byte counts are unsigned, its object delta a signed int.
	add, rm := uint64(added), uint64(removed) //nolint:gosec // two's complement, as radosgw's conversions
	st := &e.stats
	st.Size = flooredSum(st.Size, add, rm)
	st.SizeRounded = flooredSum(st.SizeRounded, roundedObjSize(add), roundedObjSize(rm))
	st.NumObjects = flooredSum(st.NumObjects, uint64(objs), 0) //nolint:gosec // two's complement, as radosgw's conversion
	return true
}

// flooredSum is v + add - sub in wrapping 64-bit arithmetic, or 0 when the
// result read as a signed integer is negative.
func flooredSum(v, add, sub uint64) uint64 {
	if n := v + add - sub; int64(n) >= 0 { //nolint:gosec // the sign test radosgw makes
		return n
	}
	return 0
}

// wait waits for the refreshes under way.
func (c *quotaCache[K]) wait() { c.refreshes.Wait() }

// close cancels the refreshes under way and waits for them, so that none
// outlives the pools it reads, as ~RGWQuotaCache waits for its own (:82-84);
// radosgw lets them finish where rgw-go cancels them. No refresh starts
// after it.
func (c *quotaCache[K]) close() {
	c.mu.Lock()
	if !c.isClosed() {
		close(c.closed)
	}
	c.mu.Unlock()
	c.refreshes.Wait()
}

// isClosed reports whether close has run.
func (c *quotaCache[K]) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// peek returns key's stats without touching the entry's place or times.
func (c *quotaCache[K]) peek(key K) (op.Stats, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return op.Stats{}, false
	}
	return e.stats, true
}

// bucketKey is the part of rgw_bucket its ordering compares
// (rgw_bucket_types.h:168-183 at both tags).
type bucketKey struct{ tenant, name, id string }

func bucketStatsKey(b meta.BucketID) bucketKey { return bucketKey{b.Tenant, b.Name, b.ID} }

func ownerStatsKey(o meta.Owner) string {
	if o.User != nil {
		return "user:" + o.User.String()
	}
	return "account:" + o.Account
}

// modifiedBucket is an entry of the owner cache's modified_buckets.
type modifiedBucket struct {
	owner  meta.Owner
	bucket meta.BucketID
}

// statsCaches is RGWQuotaHandlerImpl's two caches with the owner cache's
// modified buckets, which the bucket sync worker writes to their owners.
type statsCaches struct {
	bucket *quotaCache[bucketKey]
	owner  *quotaCache[string]

	mu       sync.Mutex
	modified map[bucketKey]modifiedBucket

	// bucketSyncEvery and ownerSyncEvery are the sync workers' intervals.
	bucketSyncEvery, ownerSyncEvery time.Duration
}

// newStatsCaches returns the caches, both sized and timed by the bucket
// options as radosgw's are (:242, :486).
func newStatsCaches(o options, now func() time.Time) statsCaches {
	return statsCaches{
		bucket:   newQuotaCache[bucketKey](o.bucketQuotaCacheSize, o.bucketQuotaTTL, now),
		owner:    newQuotaCache[string](o.bucketQuotaCacheSize, o.bucketQuotaTTL, now),
		modified: map[bucketKey]modifiedBucket{},
	}
}

// close closes both caches.
func (q *statsCaches) close() {
	q.bucket.close()
	q.owner.close()
}

// openQuota makes the stats caches and, with rgw_enable_quota_threads, adds
// the three RGWOwnerStatsCache workers (:488-495). A sync interval that is
// not positive is used as one second (positiveInterval): radosgw's sync
// threads then loop without pause, except that v20.2.4's bucket sync waits
// a second on 0.
func (s *Store) openQuota(ctx context.Context) {
	s.quota = newStatsCaches(s.opts, s.clock)
	if !s.opts.quotaThreads {
		return
	}
	s.quota.bucketSyncEvery = positiveInterval(ctx, "rgw_user_quota_bucket_sync_interval", s.opts.bucketSyncInterval)
	s.quota.ownerSyncEvery = positiveInterval(ctx, "rgw_user_quota_sync_interval", s.opts.ownerSyncInterval)
	s.AddWorker("quota-bucket-sync", s.runBucketsSync)
	s.AddWorker("quota-user-sync", func(ctx context.Context) error { return s.runOwnerSync(ctx, userOwners) })
	s.AddWorker("quota-account-sync", func(ctx context.Context) error { return s.runOwnerSync(ctx, accountOwners) })
}

// clock is the gateway's time.
func (s *Store) clock() time.Time { return s.sysobj.now() }

// effectiveQuotas is RGWOp::init_quota over get_owner_quota_info
// (rgw_op.cc:1383-1449 at v19.2.6, :1620-1686 at v20.2.4): the period's
// quotas (RadosStore::get_quota, svc_quota.cc:9-17), then the bucket's own
// bucket quota or else the owner's when enabled, and the owner's user quota
// when enabled. An owner that does not load fails it, a missing one with
// radosgw's bare ENOENT, NoSuchKey.
func (s *Store) effectiveQuotas(ctx context.Context, rec *op.BucketRecord, owner meta.Owner) (bucket, user meta.Quota, err error) {
	var ownerBucket, ownerUser meta.Quota
	if owner.User != nil {
		u, err := s.GetUser(ctx, *owner.User)
		if err != nil {
			if errors.Is(err, op.ErrNoSuchUser) {
				err = fmt.Errorf("%w: loading the bucket owner's quotas: %w", op.ErrNoSuchKey, err)
			}
			return meta.Quota{}, meta.Quota{}, err
		}
		ownerBucket, ownerUser = u.Info.BucketQuota, u.Info.UserQuota
	} else {
		a, err := s.readAccount(ctx, owner.Account)
		if err != nil {
			return meta.Quota{}, meta.Quota{}, err
		}
		ownerBucket, ownerUser = a.BucketQuota, a.Quota
	}
	bucket, user = s.zone.PeriodConfig.BucketQuota, s.zone.PeriodConfig.UserQuota
	switch {
	case rec.Info.Quota.Enabled:
		bucket = rec.Info.Quota
	case ownerBucket.Enabled:
		bucket = ownerBucket
	}
	if ownerUser.Enabled {
		user = ownerUser
	}
	return bucket, user, nil
}

// exceeded is check_quota for one quota (:877-906) through the applier it
// selects (:769-869): the objects first, then the size, which the default
// applier takes as size_rounded plus the rounded addition and check_on_raw's
// as the raw size plus the raw addition. A negative limit is none.
// radosgw's additions are unsigned, so a negative one counts as none.
func exceeded(q meta.Quota, st op.Stats, addBytes, addObjs int64) bool {
	if !q.Enabled {
		return false
	}
	objs, bytes := uint64(max(addObjs, 0)), uint64(max(addBytes, 0))
	if q.MaxObjects >= 0 && st.NumObjects+objs > uint64(q.MaxObjects) {
		return true
	}
	if q.MaxSize < 0 {
		return false
	}
	if q.CheckOnRaw {
		return st.Size+bytes > uint64(q.MaxSize)
	}
	return st.SizeRounded+roundedObjSize(bytes) > uint64(q.MaxSize)
}

// fetchBucketQuotaStats is RGWBucketStatsCache::fetch_stats_from_storage
// (:249-287) and init_refresh (:311-340): the bucket instance read again,
// then every category of its current index summed, zero for an indexless
// bucket. A bucket that is gone is ENOENT, which the cache takes as zero
// stats.
func (s *Store) fetchBucketQuotaStats(b meta.BucketID) func(context.Context) (op.Stats, error) {
	return func(ctx context.Context) (op.Stats, error) {
		rec, err := s.loadBucket(ctx, b)
		if err != nil {
			return op.Stats{}, err
		}
		if rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless {
			return op.Stats{}, nil
		}
		headers, err := s.readShardHeaders(ctx, &rec.Info)
		if err != nil {
			return op.Stats{}, err
		}
		var st op.Stats
		for _, h := range headers {
			for _, cs := range h.Stats {
				st.Size += cs.TotalSize
				st.SizeRounded += cs.TotalSizeRounded
				st.NumObjects += cs.NumEntries
			}
		}
		return st, nil
	}
}

// fetchOwnerQuotaStats is RGWOwnerStatsCache::fetch_stats_from_storage
// (:561-576): load_stats.
func (s *Store) fetchOwnerQuotaStats(owner meta.Owner) func(context.Context) (op.Stats, error) {
	return func(ctx context.Context) (op.Stats, error) {
		st, _, _, err := s.readOwnerStats(ctx, owner)
		return st, err
	}
}

// BucketStats implements op.StatsStore as load_bucket_stats
// (rgw_op.cc:3003-3016 at v19.2.6, :3235-3248 at v20.2.4): the Main
// category of the current index, zero for an indexless bucket.
func (s *Store) BucketStats(ctx context.Context, rec *op.BucketRecord) (op.Stats, error) {
	if rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless {
		return op.Stats{}, nil
	}
	ent, err := s.readIndexStats(ctx, &rec.Info)
	if err != nil {
		return op.Stats{}, op.FromRADOS(err, op.ScopeBucket)
	}
	return op.Stats{Size: ent.Size, SizeRounded: ent.SizeRounded, NumObjects: ent.Count}, nil
}

// UserStats implements op.StatsStore as load_stats: the header of the
// owner's buckets object.
func (s *Store) UserStats(ctx context.Context, owner meta.Owner) (op.Stats, error) {
	st, _, _, err := s.readOwnerStats(ctx, owner)
	if err != nil {
		return op.Stats{}, op.FromRADOS(err, op.ScopeUser)
	}
	return st, nil
}

// CheckQuota implements op.StatsStore as init_quota followed by
// RGWQuotaHandlerImpl::check_quota (:912-955): nothing is read when neither
// quota is enabled; the bucket quota is checked against the bucket cache,
// then the user quota against the owner cache. The error names the quota
// exceeded.
func (s *Store) CheckQuota(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, addBytes, addObjs int64) error {
	bq, uq, err := s.effectiveQuotas(ctx, rec, owner)
	if err != nil {
		return err
	}
	if bq.Enabled {
		st, err := s.quota.bucket.get(ctx, bucketStatsKey(rec.Info.Bucket), s.fetchBucketQuotaStats(rec.Info.Bucket))
		if err != nil {
			return op.FromRADOS(err, op.ScopeBucket)
		}
		if exceeded(bq, st, addBytes, addObjs) {
			return fmt.Errorf("bucket quota of %s: %w", rec.Info.Bucket.Name, op.ErrQuotaExceeded)
		}
	}
	if uq.Enabled {
		st, err := s.quota.owner.get(ctx, ownerStatsKey(owner), s.fetchOwnerQuotaStats(owner))
		if err != nil {
			return op.FromRADOS(err, op.ScopeUser)
		}
		if exceeded(uq, st, addBytes, addObjs) {
			return fmt.Errorf("user quota of %s: %w", owner, op.ErrQuotaExceeded)
		}
	}
	return nil
}

// AdjustStats implements op.StatsStore as update_stats (:957-960): both
// caches' entries move by the deltas, and the bucket joins the modified set
// with its owner unless already there (data_modified, :704-715). radosgw
// fills that set without rgw_enable_quota_threads too, though only the
// bucket sync thread ever empties it; rgw-go keeps it only for its worker
// (docs/ceph-upstream-bugs.md, "radosgw keeps every modified bucket for
// good when its quota threads are off").
func (s *Store) AdjustStats(_ context.Context, rec *op.BucketRecord, owner meta.Owner, objs, addBytes, removedBytes int64) error {
	key := bucketStatsKey(rec.Info.Bucket)
	s.quota.bucket.adjust(key, objs, addBytes, removedBytes)
	s.quota.owner.adjust(ownerStatsKey(owner), objs, addBytes, removedBytes)
	if !s.opts.quotaThreads {
		return nil
	}
	s.quota.mu.Lock()
	defer s.quota.mu.Unlock()
	if _, ok := s.quota.modified[key]; !ok {
		s.quota.modified[key] = modifiedBucket{owner: owner, bucket: rec.Info.Bucket}
	}
	return nil
}

// runBucketsSync is BucketsSyncThread (:349-392): at start and then every
// sync interval it takes the modified buckets and writes each one's index
// stats to its owner, logging a bucket that fails. radosgw's sync_bucket
// then checks the bucket for dynamic resharding, which rgw-go does not run
// (docs/exclusions.md).
func (s *Store) runBucketsSync(ctx context.Context) error {
	s.syncModifiedBuckets(ctx)
	t := s.newTicker(s.quota.bucketSyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C():
			s.syncModifiedBuckets(ctx)
		}
	}
}

func (s *Store) syncModifiedBuckets(ctx context.Context) {
	s.quota.mu.Lock()
	modified := s.quota.modified
	s.quota.modified = map[bucketKey]modifiedBucket{}
	s.quota.mu.Unlock()
	keys := slices.SortedFunc(maps.Keys(modified), func(a, b bucketKey) int {
		return cmp.Or(strings.Compare(a.tenant, b.tenant), strings.Compare(a.name, b.name), strings.Compare(a.id, b.id))
	})
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		mb := modified[k]
		if err := s.syncBucket(ctx, mb.bucket); err != nil {
			slog.WarnContext(ctx, "quota bucket sync failed",
				slog.String("owner", mb.owner.String()), slog.String("bucket", mb.bucket.Name), slog.Any("error", err))
		}
	}
}

// syncBucket is sync_bucket (:578-597) without check_bucket_shards: the
// bucket as load_bucket reads it, then its stats written to the owner its
// info names (RadosBucket::sync_owner_stats, rgw_sal_rados.cc:655-660 at
// v19.2.6, :673-678 at v20.2.4).
func (s *Store) syncBucket(ctx context.Context, b meta.BucketID) error {
	rec, err := s.loadBucket(ctx, b)
	if err != nil {
		return err
	}
	_, err = s.syncOwnerStats(ctx, rec.Info.Owner, &rec.Info)
	return err
}

// loadBucket is RadosStore::load_bucket (rgw_sal_rados.cc:1595-1600 and
// :610-637 at v19.2.6, :2135-2140 and :631-655 at v20.2.4): the instance b
// names, or, without a bucket id, the one its entry point names.
func (s *Store) loadBucket(ctx context.Context, b meta.BucketID) (*op.BucketRecord, error) {
	if b.ID == "" {
		return s.GetBucket(ctx, b.Tenant, b.Name)
	}
	return s.GetBucketInstance(ctx, b)
}

// ownerSection is a metadata section whose owners an owner sync worker
// walks.
type ownerSection int

const (
	userOwners ownerSection = iota
	accountOwners
)

// ownerListPage is the page sync_all_owners lists keys in (:676).
const ownerListPage = 1000

// runOwnerSync is OwnerSyncThread (:401-439): a full pass over the
// section's owners at start and then every rgw_user_quota_sync_interval.
func (s *Store) runOwnerSync(ctx context.Context, section ownerSection) error {
	s.syncAllOwners(ctx, section)
	t := s.newTicker(s.quota.ownerSyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C():
			s.syncAllOwners(ctx, section)
		}
	}
}

// syncAllOwners is sync_all_owners (:665-702): every owner forEachOwner
// lists goes through syncOwner, whose failure is logged and skipped; a
// listing failure ends the pass.
func (s *Store) syncAllOwners(ctx context.Context, section ownerSection) {
	err := s.forEachOwner(ctx, section, func(owner meta.Owner) {
		if err := s.syncOwner(ctx, owner); err != nil {
			slog.WarnContext(ctx, "owner stats sync failed", slog.String("owner", owner.String()), slog.Any("error", err))
		}
	})
	if err != nil && ctx.Err() == nil {
		slog.ErrorContext(ctx, "listing owners for the stats sync failed", slog.Any("error", err))
	}
}

// forEachOwner lists a section's metadata keys as radosgw's listers do, a
// page of ownerListPage objects at a time, and calls fn with each key's
// owner (parse_owner) once its page is listed, until ctx ends. The user
// section is the users.uid pool without the ".buckets" objects
// (svc_user_rados.cc:54-57 at v19.2.6, :71-80 at v20.2.4); the account
// section is the account pool's "account." objects (account.cc:518-533 and
// :636-645 at both tags).
func (s *Store) forEachOwner(ctx context.Context, section ownerSection, fn func(meta.Owner)) error {
	pool := s.zone.Params.UserUIDPool
	if section == accountOwners {
		pool = s.zone.Params.AccountPool
	}
	p, err := s.pools.get(ctx, pool)
	if err != nil {
		return err
	}
	token := ""
	for {
		var keys []string
		next, more, err := p.ListObjectsFrom(ctx, token, ownerListPage, func(oid, _ string) error {
			switch section {
			case userOwners:
				if !strings.HasSuffix(oid, bucketsSuffix) {
					keys = append(keys, oid)
				}
			case accountOwners:
				if id, ok := strings.CutPrefix(oid, accountOIDPrefix); ok {
					keys = append(keys, id)
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("listing %s: %w", pool, err)
		}
		for _, k := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			fn(meta.ParseOwner(k))
		}
		if !more {
			return nil
		}
		token = next
	}
}

// accountOIDPrefix is account_oid_prefix (account.cc:40 at both tags).
const accountOIDPrefix = "account."

// syncOwner is sync_owner (:623-663): nothing for an owner idle since its
// last full sync, unless rgw_user_quota_sync_idle_users, nor for one synced
// within rgw_user_quota_sync_wait_time; otherwise the owner's tenant, which
// an account owner's must load for, and a full sync.
func (s *Store) syncOwner(ctx context.Context, owner meta.Owner) error {
	_, lastSync, lastUpdate, err := s.readOwnerStats(ctx, owner)
	if err != nil {
		return err
	}
	if !s.opts.ownerSyncIdle && lastUpdate.Before(lastSync) {
		return nil
	}
	if lastSync.Add(s.opts.ownerSyncWait).After(s.clock()) {
		return nil
	}
	if _, err := s.ownerTenant(ctx, owner); err != nil {
		return err
	}
	return s.syncAllStats(ctx, owner)
}

// syncAllStats is rgw_sync_all_stats (rgw_user.cc:16-55 at both tags): the
// owner's buckets rgw_list_buckets_max_chunk at a time, each written to its
// owner's entry, a bucket that does not load being skipped and a write that
// fails ending the sync; then the owner's last full sync is stamped. As in
// syncBucket, check_bucket_shards is not run.
func (s *Store) syncAllStats(ctx context.Context, owner meta.Owner) error {
	chunk := s.opts.listBucketsChunk
	marker := ""
	for {
		ents, next, more, err := s.ListUserBuckets(ctx, owner, marker, chunk)
		if err != nil {
			return err
		}
		for i := range ents {
			rec, err := s.loadBucket(ctx, ents[i].Bucket)
			if err != nil {
				slog.WarnContext(ctx, "could not read bucket info for the owner stats sync",
					slog.String("owner", owner.String()), slog.String("bucket", ents[i].Bucket.Name), slog.Any("error", err))
				continue
			}
			if _, err := s.syncOwnerStats(ctx, rec.Info.Owner, &rec.Info); err != nil {
				return fmt.Errorf("syncing the stats of bucket %s: %w", rec.Info.Bucket.Name, err)
			}
		}
		if !more {
			break
		}
		marker = next
	}
	return s.completeOwnerStatsSync(ctx, owner)
}
