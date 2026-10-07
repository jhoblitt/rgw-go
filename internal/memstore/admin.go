package memstore

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

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

// IndexStats implements op.BucketAdminStore; not implemented yet.
func (s *Store) IndexStats(context.Context, *op.BucketRecord) (op.BucketIndexStats, error) {
	return op.BucketIndexStats{}, op.ErrNotImplemented
}

// ChangeBucketOwner implements op.BucketAdminStore; not implemented yet.
func (s *Store) ChangeBucketOwner(context.Context, *op.BucketRecord, meta.Owner, string, *meta.BucketID) error {
	return op.ErrNotImplemented
}

// UnlinkBucketOwner implements op.BucketAdminStore; not implemented yet.
func (s *Store) UnlinkBucketOwner(context.Context, *op.BucketRecord, meta.Owner) error {
	return op.ErrNotImplemented
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

// SyncOwnerStats implements op.BucketAdminStore; not implemented yet.
func (s *Store) SyncOwnerStats(context.Context, meta.Owner) error {
	return op.ErrNotImplemented
}

// PurgeBypassGC implements op.BucketAdminStore; not implemented yet.
func (s *Store) PurgeBypassGC(context.Context, *op.BucketRecord) error {
	return op.ErrNotImplemented
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
