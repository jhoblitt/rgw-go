package memstore_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("the zone", func() {
	It("is a Squid zone named default in a master zonegroup named default by default", func() {
		store, _ := newStore()
		Expect(store.Release()).To(Equal(denc.Squid), "release")
		Expect([]string{store.Zone().Name, store.Zone().ID}).To(Equal([]string{"default", "default"}), "zone")
		Expect([]string{store.ZoneParams().Name, store.ZoneParams().ID}).To(Equal([]string{"default", "default"}), "zone params")
		zg := store.ZoneGroup()
		Expect([]any{zg.Name, zg.ID, zg.APIName, zg.IsMaster, zg.MasterZone}).To(Equal([]any{"default", "default", "default", true, "default"}), "zonegroup")
		Expect(zg.Zones).To(HaveKeyWithValue("default", store.Zone()), "the zonegroup's zones")
		Expect(zg.DefaultPlacement).To(Equal(meta.PlacementRule{Name: "default-placement"}), "default placement")
		Expect(zg.PlacementTargets).To(Equal(map[string]meta.ZoneGroupPlacementTarget{
			"default-placement": {Name: "default-placement", StorageClasses: []string{meta.StorageClassStandard}},
		}), "placement targets")
	})
	It("resolves the zero rule to the default placement's default.rgw.buckets pools", func() {
		store, _ := newStore()
		p, err := store.Placement(meta.PlacementRule{})
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(Equal(op.Placement{
			Rule:          meta.PlacementRule{Name: "default-placement"},
			DataPool:      meta.Pool{Name: "default.rgw.buckets.data"},
			IndexPool:     meta.Pool{Name: "default.rgw.buckets.index"},
			DataExtraPool: meta.Pool{Name: "default.rgw.buckets.non-ec"},
			InlineData:    true,
		}))
	})
	Context("with its own placement", func() {
		var store *memstore.Store
		BeforeEach(func() {
			cold, lz4 := meta.Pool{Name: "cold.data"}, "lz4"
			std := meta.Pool{Name: "fast.data"}
			store = memstore.New(memstore.Config{
				Release: denc.Tentacle,
				Zone:    meta.Zone{Name: "ro", ReadOnly: true},
				Params: meta.ZoneParams{PlacementPools: map[string]meta.ZonePlacementInfo{
					"fast": {
						IndexPool: meta.Pool{Name: "fast.index"},
						StorageClasses: meta.ZoneStorageClasses{
							meta.StorageClassStandard: {DataPool: &std},
							"COLD":                    {DataPool: &cold, CompressionType: &lz4},
						},
					},
				}},
				ZoneGroup: meta.ZoneGroup{Name: "zg", DefaultPlacement: meta.PlacementRule{Name: "fast"}},
			})
		})
		It("keeps what the config sets", func() {
			Expect(store.Release()).To(Equal(denc.Tentacle), "release")
			Expect([]any{store.Zone().Name, store.Zone().ID, store.Zone().ReadOnly}).To(Equal([]any{"ro", "ro", true}), "zone")
			Expect(store.ZoneGroup().IsMaster).To(BeFalse(), "a configured zonegroup keeps its own master flag")
		})
		It("resolves a storage class to its own pool and compression", func() {
			p, err := store.Placement(meta.PlacementRule{Name: "fast", StorageClass: "COLD"})
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{p.DataPool, p.Compression}).To(Equal([]any{meta.Pool{Name: "cold.data"}, "lz4"}))
		})
		It("resolves a storage class the target lacks to STANDARD's pool, as get_data_pool does", func() {
			p, err := store.Placement(meta.PlacementRule{Name: "fast", StorageClass: "GLACIER"})
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{p.DataPool, p.Compression}).To(Equal([]any{meta.Pool{Name: "fast.data"}, ""}))
		})
		It("puts extra data in the STANDARD pool when the target has no extra pool, as get_data_extra_pool does", func() {
			p, err := store.Placement(meta.PlacementRule{})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Rule).To(Equal(meta.PlacementRule{Name: "fast"}), "the zonegroup's default")
			Expect(p.DataExtraPool).To(Equal(meta.Pool{Name: "fast.data"}))
		})
		It("refuses an unknown rule with InvalidLocationConstraint", func() {
			_, err := store.Placement(meta.PlacementRule{Name: "nope"})
			Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
		})
	})
	It("returns the configured realm and period", func() {
		realm := meta.Realm{ID: "r", Name: "realm"}
		period := meta.Period{ID: "p", Epoch: 2}
		store := memstore.New(memstore.Config{Realm: realm, Period: period})
		Expect(store.Realm()).To(Equal(realm), "realm")
		Expect(store.Period()).To(Equal(period), "period")
	})
})
