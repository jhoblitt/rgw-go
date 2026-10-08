package memstore

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// Config seeds a Store.
type Config struct {
	Release   denc.Release
	Zone      meta.Zone
	ZoneGroup meta.ZoneGroup
	Params    meta.ZoneParams
	Realm     meta.Realm
	Period    meta.Period
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Store is an in-memory implementation of every store interface in op. It
// is safe for concurrent use, stores copies of what it is given and hands
// out copies of what it holds.
type Store struct {
	cfg Config

	mu        sync.RWMutex
	epoch     uint64                    // the last object epoch handed out
	bucketSeq uint64                    // the last bucket instance number handed out
	users     map[string]*op.UserRecord // by meta.UserID.String()
	keys      map[string]string         // access key -> user id string
	// emails maps a lowercased email to the user id string or account id
	// holding it: radosgw keeps both in one object per email, so an email is
	// unique across users and accounts (account.cc:102-114 at v19.2.6).
	emails         map[string]string
	buckets        map[string]*bucket // "tenant/name" -> bucket
	instances      map[string]*bucket // bucket id -> bucket
	uploads        map[string]*upload // uploadKey -> upload
	accounts       map[string]*op.AccountRecord
	accountsByName map[string]string            // "tenant$name" -> account id
	accountUsers   map[string]map[string]string // account id -> lowercased display name -> user id
	usage          []op.UsageEntry
	usageRecords   []op.UsageRecord             // the usage log ReadUsage reads, seeded by AddUsage
	realms         map[string]meta.Realm        // by id
	periods        map[string]meta.Period       // "id.epoch" -> period
	periodConfigs  map[string]meta.PeriodConfig // realm id -> config
}

type bucket struct {
	rec     op.BucketRecord
	objects map[meta.ObjKey]*object
}

type object struct {
	state op.ObjectState
	// data is never changed in place once stored, so a reader may keep a
	// slice of it past the lock.
	data []byte
}

type upload struct {
	up       op.Upload
	bucketID string
	parts    map[int]*part
}

type part struct {
	op.Part
	data []byte
}

var (
	_ op.ZoneInfo         = (*Store)(nil)
	_ op.UserStore        = (*Store)(nil)
	_ op.AccountStore     = (*Store)(nil)
	_ op.BucketStore      = (*Store)(nil)
	_ op.ObjectStore      = (*Store)(nil)
	_ op.MultipartStore   = (*Store)(nil)
	_ op.StatsStore       = (*Store)(nil)
	_ op.UsageLogger      = (*Store)(nil)
	_ op.MetadataStore    = (*Store)(nil)
	_ op.UsageReader      = (*Store)(nil)
	_ op.BucketAdminStore = (*Store)(nil)
	_ op.RealmStore       = (*Store)(nil)
)

// defaultPlacement is the placement target radosgw creates a zonegroup with.
const defaultPlacement = "default-placement"

// New returns an empty store. A zero Config is a Squid zone named "default"
// in a master zonegroup named "default" whose one placement target,
// "default-placement", maps to default.rgw.buckets.{data,index,non-ec}. A
// Config that sets part of that keeps what it sets, and New fills in the
// rest: an ID is its name, the zone parameters name the zone, and the pools
// of the default placement are named after the zone.
func New(cfg Config) *Store {
	if cfg.Zone.ID == "" && cfg.Zone.Name == "" {
		cfg.Zone = meta.NewZone()
	}
	name := cmp.Or(cfg.Zone.Name, cfg.Params.Name, "default")
	id := cmp.Or(cfg.Zone.ID, cfg.Params.ID, name)
	cfg.Zone.Name, cfg.Zone.ID = name, id
	cfg.Params.Name, cfg.Params.ID = cmp.Or(cfg.Params.Name, name), cmp.Or(cfg.Params.ID, id)

	zg := &cfg.ZoneGroup
	if zg.ID == "" && zg.Name == "" {
		zg.IsMaster = true
	}
	zg.Name = cmp.Or(zg.Name, "default")
	zg.ID = cmp.Or(zg.ID, zg.Name)
	zg.APIName = cmp.Or(zg.APIName, zg.Name)
	zg.MasterZone = cmp.Or(zg.MasterZone, cfg.Zone.ID)
	if zg.Zones == nil {
		zg.Zones = map[string]meta.Zone{cfg.Zone.ID: cfg.Zone}
	}
	if zg.DefaultPlacement.Name == "" {
		zg.DefaultPlacement = meta.PlacementRule{Name: defaultPlacement}
	}
	target := zg.DefaultPlacement.Name
	if zg.PlacementTargets == nil {
		zg.PlacementTargets = map[string]meta.ZoneGroupPlacementTarget{
			target: {Name: target, StorageClasses: []string{meta.StorageClassStandard}},
		}
	}
	if cfg.Params.PlacementPools == nil {
		prefix := cfg.Params.Name + ".rgw.buckets."
		pools := meta.NewZonePlacementInfo()
		pools.IndexPool = meta.Pool{Name: prefix + "index"}
		pools.DataExtraPool = meta.Pool{Name: prefix + "non-ec"}
		pools.StorageClasses = meta.ZoneStorageClasses{meta.StorageClassStandard: {DataPool: &meta.Pool{Name: prefix + "data"}}}
		cfg.Params.PlacementPools = map[string]meta.ZonePlacementInfo{target: pools}
	}
	s := &Store{
		cfg:            cfg,
		users:          map[string]*op.UserRecord{},
		accounts:       map[string]*op.AccountRecord{},
		accountsByName: map[string]string{},
		accountUsers:   map[string]map[string]string{},
		keys:           map[string]string{},
		emails:         map[string]string{},
		buckets:        map[string]*bucket{},
		instances:      map[string]*bucket{},
		uploads:        map[string]*upload{},
		realms:         map[string]meta.Realm{},
		periods:        map[string]meta.Period{},
		periodConfigs:  map[string]meta.PeriodConfig{},
	}
	if cfg.Realm.ID != "" {
		s.realms[cfg.Realm.ID] = cfg.Realm
	}
	if cfg.Period.ID != "" {
		s.periods[periodKey(cfg.Period.ID, cfg.Period.Epoch)] = cfg.Period
	}
	return s
}

// Release implements op.ZoneInfo.
func (s *Store) Release() denc.Release { return s.cfg.Release }

// Zone implements op.ZoneInfo.
func (s *Store) Zone() meta.Zone { return s.cfg.Zone }

// ZoneGroup implements op.ZoneInfo.
func (s *Store) ZoneGroup() meta.ZoneGroup { return s.cfg.ZoneGroup }

// ZoneParams implements op.ZoneInfo.
func (s *Store) ZoneParams() meta.ZoneParams { return s.cfg.Params }

// Realm implements op.ZoneInfo.
func (s *Store) Realm() meta.Realm { return s.cfg.Realm }

// Period implements op.ZoneInfo.
func (s *Store) Period() meta.Period { return s.cfg.Period }

// Placement implements op.ZoneInfo through the zone's placement pools. A
// rule without a name takes the zonegroup's default placement, and its
// storage class when the rule has none either. The rule must then name a
// placement of the zone and a storage class it has, as radosgw's
// find_zone_placement requires, the empty class being STANDARD. The pools
// are RGWZonePlacementInfo's: the class's data pool, else STANDARD's; the
// data-extra pool, else STANDARD's data pool (get_data_extra_pool).
func (s *Store) Placement(rule meta.PlacementRule) (op.Placement, error) {
	if rule.Name == "" {
		def := s.cfg.ZoneGroup.DefaultPlacement
		rule.Name = def.Name
		rule.StorageClass = cmp.Or(rule.StorageClass, def.StorageClass)
	}
	info, ok := s.cfg.Params.PlacementPools[rule.Name]
	if !ok {
		return op.Placement{}, fmt.Errorf("placement %s: %w", rule, op.ErrInvalidLocationConstraint)
	}
	sc := rule.CanonicalStorageClass()
	class, ok := info.StorageClasses[sc]
	if !ok && sc != meta.StorageClassStandard {
		return op.Placement{}, fmt.Errorf("placement %s: storage class %s: %w", rule.Name, sc, op.ErrInvalidLocationConstraint)
	}
	standard := deref(info.StorageClasses[meta.StorageClassStandard].DataPool)
	data := standard
	if class.DataPool != nil {
		data = *class.DataPool
	}
	extra := info.DataExtraPool
	if extra.Name == "" {
		extra = standard
	}
	return op.Placement{
		Rule:          meta.PlacementRule{Name: rule.Name, StorageClass: sc},
		DataPool:      data,
		IndexPool:     info.IndexPool,
		DataExtraPool: extra,
		Compression:   deref(class.CompressionType),
		InlineData:    info.InlineData,
	}, nil
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func (s *Store) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

// nextEpoch hands out the version a head write leaves.
func (s *Store) nextEpoch() uint64 {
	s.epoch++
	return s.epoch
}

// newTag is RGWObjVersionTracker::generate_new_write_ver's tag:
// append_rand_alpha with TAG_LEN 24 writes an underscore, then the 23
// characters gen_rand_alphanumeric fills a 24-byte buffer with before its
// terminator (rgw_common.cc:3204-3211, rgw_common.h:1621-1628 at v19.2.6;
// :3266-3273, :1623-1630 at v20.2.4).
func (s *Store) newTag() string { return "_" + randomAlphanumeric(23) }

// alphanumeric is gen_rand_alphanumeric's table (src/common/random_string.cc),
// a URL-safe base64 alphabet despite the name.
const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// randomAlphanumeric is gen_rand_alphanumeric: n characters, each a random
// byte reduced into the table.
func randomAlphanumeric(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	for i, c := range b {
		b[i] = alphanumeric[int(c)%len(alphanumeric)]
	}
	return string(b)
}

// instance finds rec's bucket instance.
func (s *Store) instance(rec *op.BucketRecord) (*bucket, error) {
	if rec != nil {
		if b, ok := s.instances[rec.Info.Bucket.ID]; ok {
			return b, nil
		}
	}
	return nil, op.ErrNoSuchBucket
}

func cloneAttrs(m map[string][]byte) map[string][]byte {
	if m == nil {
		return nil
	}
	c := make(map[string][]byte, len(m))
	for k, v := range m {
		c[k] = bytes.Clone(v)
	}
	return c
}

func cloneOwner(o meta.Owner) meta.Owner {
	if o.User != nil {
		u := *o.User
		o.User = &u
	}
	return o
}

func cloneUserInfo(u meta.UserInfo) meta.UserInfo {
	u.AccessKeys = maps.Clone(u.AccessKeys)
	u.SwiftKeys = maps.Clone(u.SwiftKeys)
	u.SubUsers = maps.Clone(u.SubUsers)
	u.Caps = maps.Clone(u.Caps)
	u.TempURLKeys = maps.Clone(u.TempURLKeys)
	u.PlacementTags = slices.Clone(u.PlacementTags)
	u.MFAIDs = slices.Clone(u.MFAIDs)
	u.Tags = slices.Clone(u.Tags)
	u.GroupIDs = slices.Clone(u.GroupIDs)
	return u
}

func copyUser(rec *op.UserRecord) *op.UserRecord {
	c := *rec
	c.Info = cloneUserInfo(rec.Info)
	c.Attrs = cloneAttrs(rec.Attrs)
	return &c
}

func cloneBucketInfo(i meta.BucketInfo) meta.BucketInfo {
	i.Owner = cloneOwner(i.Owner)
	if i.Layout.Target != nil {
		t := *i.Layout.Target
		i.Layout.Target = &t
	}
	i.Layout.Logs = slices.Clone(i.Layout.Logs)
	i.Website = slices.Clone(i.Website)
	i.MDSearchConfig = maps.Clone(i.MDSearchConfig)
	i.ObjLock = slices.Clone(i.ObjLock)
	i.SyncPolicy = slices.Clone(i.SyncPolicy)
	return i
}

func copyBucket(rec *op.BucketRecord) *op.BucketRecord {
	c := *rec
	c.EntryPoint.Owner = cloneOwner(rec.EntryPoint.Owner)
	if rec.EntryPoint.OldBucketInfo != nil {
		old := cloneBucketInfo(*rec.EntryPoint.OldBucketInfo)
		c.EntryPoint.OldBucketInfo = &old
	}
	c.Info = cloneBucketInfo(rec.Info)
	c.Attrs = cloneAttrs(rec.Attrs)
	return &c
}
