package driver_test

import (
	"context"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// realmListTimeout bounds a spec that lists the root pool.
const realmListTimeout = 10 * time.Second

var _ = Describe("realm and period reads", func() {
	const (
		store   = "ceph-objectstore"
		otherID = "realm-other"
	)
	var (
		c   *fakerados.Cluster
		ids rookZone
	)
	BeforeEach(func() {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		ids = seedRookZone(c, store, true)
		// The realm's period at a second epoch, its latest.
		d := denc.NewDecoder(rootData(c, meta.PeriodOID(ids.periodID, 1)))
		p := meta.DecodePeriod(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		p.Epoch = 2
		c.Put(meta.RootPool, "", meta.PeriodOID(ids.periodID, 2), encode(p))
		c.Put(meta.RootPool, "", meta.PeriodLatestEpochOID(ids.periodID), encode(meta.PeriodLatestEpochInfo{Epoch: 2}))
		c.Put(meta.RootPool, "", meta.RealmNameOID("other"), encode(meta.NameToID{ObjID: otherID}))
		c.Put(meta.RootPool, "", meta.RealmOID(otherID), encode(meta.Realm{ID: otherID, Name: "other"}))
	})
	openWith := func(ctx context.Context, kv map[string]string) *driver.Store {
		GinkgoHelper()
		s, err := driver.Open(ctx, c, conf(kv), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		return s
	}
	open := func(ctx context.Context) *driver.Store {
		GinkgoHelper()
		return openWith(ctx, map[string]string{"rgw_zone": store})
	}

	It("resolves the realm by id, by name and by default", func(ctx SpecContext) {
		s := open(ctx)
		for _, tc := range []struct{ id, name string }{{ids.realmID, ""}, {"", store}, {"", ""}, {ids.realmID, "other"}} {
			r, err := s.GetRealm(ctx, tc.id, tc.name)
			Expect(err).NotTo(HaveOccurred(), "id %q name %q", tc.id, tc.name)
			Expect(r).To(Equal(meta.Realm{ID: ids.realmID, Name: store, CurrentPeriod: ids.periodID, Epoch: 1}), "id %q name %q", tc.id, tc.name)
		}
		r, err := s.GetRealm(ctx, "", "other")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.ID).To(Equal(otherID))
	})
	It("answers NoSuchKey for a realm, name or default it cannot find", func(ctx SpecContext) {
		s := open(ctx)
		_, err := s.GetRealm(ctx, "", "nosuch")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, err = s.GetRealm(ctx, "nosuch", "")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		removeRoot(ctx, c, meta.DefaultRealmOID())
		_, err = open(ctx).GetRealm(ctx, "", "")
		Expect(err).To(MatchError(op.ErrNoSuchKey), "no default realm")
	})
	It("answers UnknownError, radosgw's -EIO, for a realm or name object that does not decode", func(ctx SpecContext) {
		c.Put(meta.RootPool, "", meta.RealmNameOID("broken"), []byte{1})
		c.Put(meta.RootPool, "", meta.RealmOID("broken"), []byte{1, 1, 9})
		s := open(ctx)
		_, err := s.GetRealm(ctx, "", "broken")
		Expect(err).To(MatchError(op.ErrUnknown))
		_, err = s.GetRealm(ctx, "broken", "")
		Expect(err).To(MatchError(op.ErrUnknown))
	})
	It("takes rgw_realm_id, then rgw_realm, for a realm the request does not name by id", func(ctx SpecContext) {
		c.Put(meta.RootPool, "", meta.DefaultRealmOID(), encode(meta.DefaultSystemMetaObjInfo{DefaultID: otherID}))
		s := openWith(ctx, map[string]string{"rgw_realm_id": ids.realmID, "rgw_zone": store})
		r, err := s.GetRealm(ctx, "", "other")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.ID).To(Equal(ids.realmID), "the configured id wins over the name, as RGWSystemMetaObj::init takes it first")
		s = openWith(ctx, map[string]string{"rgw_realm": store, "rgw_zone": store})
		r, err = s.GetRealm(ctx, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.ID).To(Equal(ids.realmID), "the configured name wins over default.realm")
		r, err = s.GetRealm(ctx, "", "other")
		Expect(err).NotTo(HaveOccurred())
		Expect(r.ID).To(Equal(otherID), "a requested name wins over the configured one")
	})
	It("lists the realm names in the pool's order with the default id", func(ctx SpecContext) {
		for _, n := range []string{"zeta", "alpha", "mid", "beta"} {
			c.Put(meta.RootPool, "", meta.RealmNameOID(n), encode(meta.NameToID{ObjID: "id-" + n}))
		}
		c.Put(meta.RootPool, "", meta.RealmNamesPrefix, encode(meta.NameToID{ObjID: "bare"}))
		pool, err := c.Pool(ctx, meta.RootPool, "")
		Expect(err).NotTo(HaveOccurred())
		var want []string
		Expect(pool.ListObjects(ctx, func(oid, _ string) error {
			if n, ok := strings.CutPrefix(oid, meta.RealmNamesPrefix); ok && n != "" {
				want = append(want, n)
			}
			return nil
		})).To(Succeed())
		Expect(want).NotTo(Equal(slices.Sorted(slices.Values(want))), "the pool's order is not the names' order")
		def, names, err := open(ctx).ListRealms(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(def).To(Equal(ids.realmID))
		Expect(names).To(Equal(want))
		Expect(names).To(ConsistOf(store, "other", "zeta", "alpha", "mid", "beta"), "the bare prefix is no name")
	}, SpecTimeout(realmListTimeout))
	It("lists the realms with an empty default when default.realm is missing or does not decode", func(ctx SpecContext) {
		removeRoot(ctx, c, meta.DefaultRealmOID())
		def, names, err := open(ctx).ListRealms(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(def).To(BeEmpty())
		Expect(names).To(ConsistOf(store, "other"))
		s := open(ctx)
		c.Put(meta.RootPool, "", meta.DefaultRealmOID(), []byte{1})
		def, _, err = s.ListRealms(ctx)
		Expect(err).NotTo(HaveOccurred(), "radosgw ignores read_default_id's failure")
		Expect(def).To(BeEmpty())
	}, SpecTimeout(realmListTimeout))
	It("stops listing when its context ends", func(ctx SpecContext) {
		s := open(ctx)
		done, cancel := context.WithCancel(ctx)
		cancel()
		_, _, err := s.ListRealms(done)
		Expect(err).To(HaveOccurred())
	}, SpecTimeout(realmListTimeout))
	It("reads the current period at its latest epoch, and a named one at a named epoch", func(ctx SpecContext) {
		s := open(ctx)
		for _, realm := range []string{"", ids.realmID} {
			p, err := s.GetPeriod(ctx, realm, "", 0)
			Expect(err).NotTo(HaveOccurred(), "realm %q", realm)
			Expect(p.ID).To(Equal(ids.periodID))
			Expect(p.Epoch).To(Equal(uint32(2)))
		}
		p, err := s.GetPeriod(ctx, "", ids.periodID, 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Epoch).To(Equal(uint32(1)))
		p, err = s.GetPeriod(ctx, "", ids.periodID, 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Epoch).To(Equal(uint32(2)))
		_, err = s.GetPeriod(ctx, "", ids.periodID, 9)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		_, err = s.GetPeriod(ctx, "", "nosuch", 0)
		Expect(err).To(MatchError(op.ErrNoSuchKey), "no latest epoch")
		_, err = s.GetPeriod(ctx, "nosuch", "", 0)
		Expect(err).To(MatchError(op.ErrNoSuchKey), "no such realm")
	})
	It("names a staging period without its epoch only for the realm read", func(ctx SpecContext) {
		staging := ids.realmID + ":staging"
		c.Put(meta.RootPool, "", "periods."+staging, encode(meta.Period{ID: staging, Epoch: 5}))
		c.Put(meta.RootPool, "", "periods."+staging+".5", encode(meta.Period{ID: staging, Epoch: 6}))
		s := open(ctx)
		p, err := s.GetPeriod(ctx, ids.realmID, staging, 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Epoch).To(Equal(uint32(5)), "the realm's own staging period has no epoch in its name")
		p, err = s.GetPeriod(ctx, "", staging, 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Epoch).To(Equal(uint32(6)), "with no realm id the staging id is another realm's, and named with its epoch")
	})
	It("answers UnknownError for a period or latest epoch that does not decode", func(ctx SpecContext) {
		c.Put(meta.RootPool, "", meta.PeriodLatestEpochOID("bad"), []byte{1})
		c.Put(meta.RootPool, "", meta.PeriodOID(ids.periodID, 3), []byte{1, 1, 2})
		s := open(ctx)
		_, err := s.GetPeriod(ctx, "", "bad", 0)
		Expect(err).To(MatchError(op.ErrUnknown))
		_, err = s.GetPeriod(ctx, "", ids.periodID, 3)
		Expect(err).To(MatchError(op.ErrUnknown))
	})
	It("round-trips the period config under period_config.<realm id>, and NotFound before", func(ctx SpecContext) {
		s := open(ctx)
		_, err := s.GetPeriodConfig(ctx, ids.realmID)
		Expect(err).To(MatchError(op.ErrNotFound))
		cfg := meta.PeriodConfig{UserRateLimit: meta.RateLimitInfo{MaxReadOps: 5, Enabled: true}, AnonRateLimit: meta.RateLimitInfo{MaxWriteBytes: 9}}
		Expect(s.PutPeriodConfig(ctx, ids.realmID, cfg)).To(Succeed())
		got, err := s.GetPeriodConfig(ctx, ids.realmID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(cfg))
		Expect(rootData(c, meta.PeriodConfigOID(ids.realmID))).To(Equal(encode(cfg)))
		cfg.UserRateLimit.MaxReadOps = 6
		Expect(s.PutPeriodConfig(ctx, ids.realmID, cfg)).To(Succeed(), "a non-exclusive write replaces the object")
		got, err = open(ctx).GetPeriodConfig(ctx, ids.realmID)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.UserRateLimit.MaxReadOps).To(BeEquivalentTo(6), "a fresh store reads what was written")
		Expect(s.PutPeriodConfig(ctx, "", cfg)).To(Succeed())
		Expect(c.Object(meta.RootPool, "", "period_config.default")).NotTo(BeNil(), "no realm is period_config.default")
	})
	It("answers UnknownError for a period config that does not decode", func(ctx SpecContext) {
		c.Put(meta.RootPool, "", meta.PeriodConfigOID(ids.realmID), []byte{2})
		_, err := open(ctx).GetPeriodConfig(ctx, ids.realmID)
		Expect(err).To(MatchError(op.ErrUnknown))
	})
})
