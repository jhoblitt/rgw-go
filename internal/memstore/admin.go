package memstore

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// AddUsage seeds the usage log ReadUsage and TrimUsage work on; Log feeds a
// separate log. A record without a User is filed under its payer, else its
// owner, as rgw_user_usage_log_add files it (cls_rgw.cc:3583 at v19.2.6).
func (s *Store) AddUsage(rec op.UsageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.User = cmp.Or(rec.User, rec.Payer, rec.Owner)
	rec.Categories = maps.Clone(rec.Categories)
	s.usageRecords = append(s.usageRecords, rec)
}

// usageMatches is usage_iterate_range's filter: the user unless it is "",
// the bucket unless it is "", and an epoch in [start, end).
func usageMatches(rec *op.UsageRecord, user, bucket string, start, end uint64) bool {
	return (user == "" || rec.User == user) && (bucket == "" || rec.Bucket == bucket) &&
		rec.Epoch >= start && rec.Epoch < end
}

// maxUsageEntries is user_usage_log_read's limit when the caller gives none
// (cls_rgw.cc:3739-3740 at v19.2.6, :4141-4142 at v20.2.4).
const maxUsageEntries = 1000

// ReadUsage implements op.UsageReader over the seeded records in (User,
// Bucket, Epoch) order, it.Index counting the records earlier calls
// returned. A page of at most maxRecs records, 1000 when it is 0, is
// aggregated per (User, Bucket) as rgw_usage_log_entry::aggregate does: the
// first record's owner, payer and epoch, and a Total that is the sum of the
// categories, whatever Total the records held.
func (s *Store) ReadUsage(_ context.Context, user, bucket string, start, end uint64, maxRecs uint32, it *op.UsageIter) (recs []op.UsageRecord, truncated bool, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched []op.UsageRecord
	for i := range s.usageRecords {
		if usageMatches(&s.usageRecords[i], user, bucket, start, end) {
			matched = append(matched, s.usageRecords[i])
		}
	}
	slices.SortStableFunc(matched, func(a, b op.UsageRecord) int {
		return cmp.Or(cmp.Compare(a.User, b.User), cmp.Compare(a.Bucket, b.Bucket), cmp.Compare(a.Epoch, b.Epoch))
	})
	if maxRecs == 0 {
		maxRecs = maxUsageEntries
	}
	from := min(int(it.Index), len(matched))
	to := min(from+int(maxRecs), len(matched))
	for i := from; i < to; i++ {
		r := &matched[i]
		if n := len(recs); n == 0 || recs[n-1].User != r.User || recs[n-1].Bucket != r.Bucket {
			recs = append(recs, op.UsageRecord{User: r.User, Owner: r.Owner, Payer: r.Payer, Bucket: r.Bucket, Epoch: r.Epoch})
		}
		aggregateUsage(&recs[len(recs)-1], r)
	}
	it.Index = uint32(to) //nolint:gosec // to is at most len(s.usageRecords), far below 2^32
	return recs, to < len(matched), nil
}

// aggregateUsage is rgw_usage_log_entry::aggregate's sums: each category
// added into into's category and into its total, and the s3select counters.
func aggregateUsage(into, r *op.UsageRecord) {
	for cat, d := range r.Categories {
		if into.Categories == nil {
			into.Categories = map[string]op.UsageData{}
		}
		into.Categories[cat] = addUsage(into.Categories[cat], d)
		into.Total = addUsage(into.Total, d)
	}
	into.S3Select.BytesProcessed += r.S3Select.BytesProcessed
	into.S3Select.BytesReturned += r.S3Select.BytesReturned
}

func addUsage(a, b op.UsageData) op.UsageData {
	return op.UsageData{
		BytesSent:     a.BytesSent + b.BytesSent,
		BytesReceived: a.BytesReceived + b.BytesReceived,
		Ops:           a.Ops + b.Ops,
		SuccessfulOps: a.SuccessfulOps + b.SuccessfulOps,
	}
}

// TrimUsage implements op.UsageReader by dropping every record ReadUsage
// would return for the same filters.
func (s *Store) TrimUsage(_ context.Context, user, bucket string, start, end uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageRecords = slices.DeleteFunc(s.usageRecords, func(r op.UsageRecord) bool {
		return usageMatches(&r, user, bucket, start, end)
	})
	return nil
}

// IndexStats implements op.BucketAdminStore over the bucket's objects, all
// in the Main category, sized as the bucket index sizes them; the store
// keeps no index header, so shard 0's version is the bucket's newest object
// epoch and every other value is zero or empty, one entry per shard.
func (s *Store) IndexStats(_ context.Context, rec *op.BucketRecord) (op.BucketIndexStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := s.instance(rec)
	if err != nil {
		return op.BucketIndexStats{}, err
	}
	st := op.BucketIndexStats{Categories: map[string]op.CategoryStats{}}
	var ver uint64
	if len(b.objects) > 0 {
		var main op.CategoryStats
		for _, o := range b.objects {
			main.Size += o.state.Size
			main.SizeRounded += roundedObjSize(o.state.Size)
			main.NumObjects++
			ver = max(ver, o.state.Epoch)
		}
		main.SizeUtilized = main.Size
		st.Categories["rgw.main"] = main
	}
	shards := max(rec.Info.Layout.Current.Layout.Normal.NumShards, 1)
	vers, masters, markers := make([]string, shards), make([]string, shards), make([]string, shards)
	for i := range shards {
		vers[i], masters[i], markers[i] = shardValue(i, 0), shardValue(i, 0), strconv.FormatUint(uint64(i), 10)+"#"
	}
	vers[0] = shardValue(0, ver)
	st.Ver, st.MasterVer, st.MaxMarker = strings.Join(vers, ","), strings.Join(masters, ","), strings.Join(markers, ",")
	return st, nil
}

// shardValue is one "<shard>#<value>" item of BucketIndexShardsManager::to_string.
func shardValue(shard uint32, v uint64) string {
	return strconv.FormatUint(uint64(shard), 10) + "#" + strconv.FormatUint(v, 10)
}

// ChangeBucketOwner implements op.BucketAdminStore with the driver's
// checks and ACL edit: a bucket without an ACL is ErrInvalidArgument, one
// whose ACL does not decode ErrUnknown, and a new name another bucket holds
// ErrBucketAlreadyExists, each before anything changes; so is the bucket's
// own name when another bucket holds it, as for an instance loaded by its id
// after its name was re-created. The bucket then gets
// a default ACL for owner, owner in its instance and entry point, linked,
// and under newName its new key, with its id and marker; the owners' lists
// follow the instance's owner.
func (s *Store) ChangeBucketOwner(_ context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string, newName *meta.BucketID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.instance(rec)
	if err != nil {
		return err
	}
	raw, ok := b.rec.Attrs[meta.AttrACL]
	if !ok {
		return op.ErrInvalidArgument
	}
	d := denc.NewDecoder(raw)
	acl.DecodePolicy(d)
	if d.Err() != nil {
		return op.ErrUnknown
	}
	oldKey := bucketKey(b.rec.Info.Bucket.Tenant, b.rec.Info.Bucket.Name)
	newKey := oldKey
	if newName != nil {
		newKey = bucketKey(newName.Tenant, newName.Name)
	}
	if other, ok := s.buckets[newKey]; ok && other != b {
		return fmt.Errorf("bucket %s: %w", newKey, op.ErrBucketAlreadyExists)
	}
	e := denc.NewEncoder()
	acl.DefaultPolicy(owner, displayName).Encode(e, s.cfg.Release)
	b.rec.Attrs = applyAttrs(b.rec.Attrs, map[string][]byte{meta.AttrACL: e.Bytes()}, []string{op.RenameIntentAttr})
	b.rec.Info.Owner = cloneOwner(owner)
	if newKey != oldKey {
		if s.buckets[oldKey] == b {
			delete(s.buckets, oldKey)
		}
		b.rec.Info.Bucket.Tenant, b.rec.Info.Bucket.Name = newName.Tenant, newName.Name
	}
	s.buckets[newKey] = b
	b.rec.EntryPoint.Bucket = b.rec.Info.Bucket
	b.rec.EntryPoint.Owner = cloneOwner(owner)
	b.rec.EntryPoint.Linked = true
	b.rec.Version.Ver++
	b.rec.EPVersion.Ver++
	b.rec.Mtime = s.now()
	*rec = *copyBucket(&b.rec)
	return nil
}

// UnlinkBucketOwner implements op.BucketAdminStore as do_unlink_bucket's
// entry point step: an entry point that is gone or already unlinked is
// done, one another owner holds is ErrInvalidArgument, and otherwise it is
// unlinked. The owners' lists follow the instance's owner, so the bucket
// stays in its owner's.
func (s *Store) UnlinkBucketOwner(_ context.Context, rec *op.BucketRecord, owner meta.Owner) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[bucketKey(rec.Info.Bucket.Tenant, rec.Info.Bucket.Name)]
	switch {
	case !ok || !b.rec.EntryPoint.Linked:
		return nil
	case b.rec.EntryPoint.Owner.String() != owner.String():
		return op.ErrInvalidArgument
	}
	b.rec.EntryPoint.Linked = false
	b.rec.EPVersion.Ver++
	return nil
}

// CheckIndex implements op.BucketAdminStore; not implemented yet.
func (s *Store) CheckIndex(context.Context, *op.BucketRecord) (existing, calculated map[string]op.CategoryStats, err error) {
	return nil, nil, op.ErrNotImplemented
}

// RebuildIndex implements op.BucketAdminStore; not implemented yet.
func (s *Store) RebuildIndex(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
}

// RemoveIndexEntries implements op.BucketAdminStore; not implemented yet.
func (s *Store) RemoveIndexEntries(context.Context, *op.BucketRecord, []meta.ObjKey) error {
	return op.ErrNotImplemented
}

// ChownBucket implements op.BucketAdminStore as RadosBucket::chown
// (rgw_sal_rados.cc:699-751 at v19.2.6): the bucket's instance and entry
// point move to owner, so the old owner's list drops it and the new
// owner's lists it, and its ACL, when it has one that decodes, loses every
// grant keyed by the old owner and gains FULL_CONTROL for the new one, who
// becomes its owner. A memstore write cannot race, so the stored instance
// is rewritten whatever version rec holds, as adopt_user_bucket's retries
// end; rec is left at the stored state.
func (s *Store) ChownBucket(_ context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.instance(rec)
	if err != nil {
		return err
	}
	if raw, ok := b.rec.Attrs[meta.AttrACL]; ok {
		d := denc.NewDecoder(raw)
		pol := acl.DecodePolicy(d)
		if d.Err() == nil {
			pol.ACL.RemoveCanonUserGrant(pol.Owner.ID)
			pol.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: owner.String(), Name: displayName, Permission: acl.PermFullControl})
			pol.Owner = acl.Owner{ID: owner.String(), DisplayName: displayName}
			e := denc.NewEncoder()
			pol.Encode(e, s.cfg.Release)
			b.rec.Attrs = applyAttrs(b.rec.Attrs, map[string][]byte{meta.AttrACL: e.Bytes()}, nil)
		}
	}
	b.rec.Info.Owner = cloneOwner(owner)
	b.rec.EntryPoint.Owner = cloneOwner(owner)
	b.rec.EntryPoint.Linked = true
	b.rec.Version.Ver++
	b.rec.Mtime = s.now()
	*rec = *copyBucket(&b.rec)
	return nil
}

// RemoveDanglingEntryPoint implements op.BucketAdminStore: the store keeps
// an entry point only with its bucket, so no name is dangling.
func (s *Store) RemoveDanglingEntryPoint(_ context.Context, tenant, name string) error {
	return fmt.Errorf("bucket %s: %w", bucketKey(tenant, name), op.ErrNoSuchBucket)
}

// SyncOwnerStats implements op.BucketAdminStore: the store computes an
// owner's stats from its buckets on every read, so there is nothing to
// sync.
func (s *Store) SyncOwnerStats(context.Context, meta.Owner) error { return nil }

// PurgeBypassGC implements op.BucketAdminStore as remove_bypass_gc's data
// pass: every object of the bucket and every upload in flight goes, there
// being no GC to bypass, and the bucket stays for the purge that follows.
func (s *Store) PurgeBypassGC(_ context.Context, rec *op.BucketRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.instance(rec)
	if err != nil {
		return err
	}
	clear(b.objects)
	for k, u := range s.uploads {
		if u.bucketID == rec.Info.Bucket.ID {
			delete(s.uploads, k)
		}
	}
	return nil
}

// GetRealm implements op.RealmStore over the seeded realm: by id, else by
// name, else Config.Realm as the default realm.
func (s *Store) GetRealm(_ context.Context, id, name string) (meta.Realm, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realm(id, name)
}

func (s *Store) realm(id, name string) (meta.Realm, error) {
	if id == "" && name != "" {
		for _, r := range s.realms {
			if r.Name == name {
				return r, nil
			}
		}
		return meta.Realm{}, fmt.Errorf("realm name %q: %w", name, op.ErrNoSuchKey)
	}
	if id == "" {
		id = s.cfg.Realm.ID
	}
	r, ok := s.realms[id]
	if !ok {
		return meta.Realm{}, fmt.Errorf("realm %q: %w", id, op.ErrNoSuchKey)
	}
	return r, nil
}

// ListRealms implements op.RealmStore: the realm names sorted, and
// Config.Realm's id as the default when one was seeded.
func (s *Store) ListRealms(context.Context) (defaultID string, names []string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.realms {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	return s.cfg.Realm.ID, names, nil
}

// GetPeriod implements op.RealmStore over the seeded period as RGWPeriod::init
// resolves one: an empty periodID is the realm's current period and a zero
// epoch the highest epoch held for the period.
func (s *Store) GetPeriod(_ context.Context, realmID, periodID string, epoch uint32) (meta.Period, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if periodID == "" {
		r, err := s.realm(realmID, "")
		if err != nil {
			return meta.Period{}, err
		}
		periodID = r.CurrentPeriod
	}
	if epoch == 0 {
		for key := range s.periods {
			if p := s.periods[key]; p.ID == periodID {
				epoch = max(epoch, p.Epoch)
			}
		}
	}
	p, ok := s.periods[periodKey(periodID, epoch)]
	if !ok {
		return meta.Period{}, fmt.Errorf("period %s epoch %d: %w", periodID, epoch, op.ErrNoSuchKey)
	}
	return p, nil
}

// GetPeriodConfig implements op.RealmStore.
func (s *Store) GetPeriodConfig(_ context.Context, realmID string) (meta.PeriodConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, ok := s.periodConfigs[realmID]
	if !ok {
		return meta.PeriodConfig{}, fmt.Errorf("period config of realm %q: %w", realmID, op.ErrNotFound)
	}
	return cfg, nil
}

// PutPeriodConfig implements op.RealmStore.
func (s *Store) PutPeriodConfig(_ context.Context, realmID string, cfg meta.PeriodConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.periodConfigs[realmID] = cfg
	return nil
}

// periodKey is the "<id>.<epoch>" suffix of a period's object name.
func periodKey(id string, epoch uint32) string {
	return id + "." + strconv.FormatUint(uint64(epoch), 10)
}
