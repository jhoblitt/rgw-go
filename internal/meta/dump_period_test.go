package meta_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("MaskPeriodTierSecrets", func() {
	tier := func(typ, id, secret string) meta.ZoneGroupPlacementTier {
		t := meta.NewZoneGroupPlacementTier()
		t.TierType, t.StorageClass = typ, "CLOUD"
		t.S3.Key.ID, t.S3.Key.Secret = id, secret
		return t
	}
	// period has two zonegroups, each with a cloud-s3 and a cloud-s3-glacier
	// tier, and G1 a second placement target with a tier of its own; key ids
	// and secrets name the zonegroup and the tier's storage class.
	period := func() meta.Period {
		p := meta.NewPeriod()
		p.ID, p.RealmID = "P", "R"
		p.PeriodMap = meta.PeriodMap{ID: "P", ZoneGroups: map[string]meta.ZoneGroup{}}
		for _, g := range []string{"G1", "G2"} {
			p.PeriodMap.ZoneGroups[g] = meta.ZoneGroup{ID: g, Name: g, PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{
				"default-placement": {Name: "default-placement", TierTargets: map[string]meta.ZoneGroupPlacementTier{
					"CLOUD":   tier(meta.TierTypeCloudS3, "AK-"+g+"-CLOUD", "secret-"+g+"-CLOUD"),
					"GLACIER": tier(meta.TierTypeCloudS3Glacier, "AK-"+g+"-GLACIER", "secret-"+g+"-GLACIER"),
				}},
			}}
		}
		p.PeriodMap.ZoneGroups["G1"].PlacementTargets["fast-placement"] = meta.ZoneGroupPlacementTarget{
			Name: "fast-placement", TierTargets: map[string]meta.ZoneGroupPlacementTier{
				"FASTCLOUD": tier(meta.TierTypeCloudS3, "AK-G1-FASTCLOUD", "secret-G1-FASTCLOUD"),
			},
		}
		return p
	}
	// keys is each tier's S3 key, by "<zonegroup>-<storage class>".
	keys := func(p meta.Period) map[string]meta.AccessKey {
		out := map[string]meta.AccessKey{}
		for g, zg := range p.PeriodMap.ZoneGroups {
			for _, t := range zg.PlacementTargets {
				for class, x := range t.TierTargets {
					out[g+"-"+class] = x.S3.Key
				}
			}
		}
		return out
	}
	render := func(p meta.Period, rel denc.Release) string {
		GinkgoHelper()
		f := formatter.NewJSON(false)
		f.OpenObjectSection("period")
		p.Dump(f, rel)
		f.CloseSection()
		Expect(f.Err()).NotTo(HaveOccurred())
		return string(f.Bytes())
	}

	It("masks every tier's secret in a copy and keeps each access key, leaving the caller's period and Dump unmasked", func() {
		p := period()
		masked := meta.MaskPeriodTierSecrets(p)
		Expect(keys(masked)).To(HaveLen(5))
		for name, k := range keys(masked) {
			Expect(k.ID).To(Equal("AK-"+name), "tier %s", name)
			Expect(k.Secret).To(Equal(meta.MaskedTierSecret), "tier %s", name)
		}
		for name, k := range keys(p) {
			Expect(k.Secret).To(Equal("secret-"+name), "the caller's tier %s", name)
		}
		// RGWZoneGroupPlacementTier::dump writes cloud-s3-glacier's s3 on
		// Tentacle alone, so Tentacle's document carries all five secrets.
		want := render(p, denc.Tentacle)
		for name := range keys(p) {
			secret := `"secret":"secret-` + name + `"`
			Expect(want).To(ContainSubstring(secret), "Dump of the caller's period writes tier %s's secret", name)
			want = strings.ReplaceAll(want, secret, `"secret":"`+meta.MaskedTierSecret+`"`)
		}
		Expect(render(masked, denc.Tentacle)).To(Equal(want), "only the secrets differ")
	})

	DescribeTable("writes a set secret as MaskedTierSecret and an empty one empty, keeping the access key",
		func(secret, want string) {
			p := period()
			zg := p.PeriodMap.ZoneGroups["G1"]
			zg.PlacementTargets["default-placement"].TierTargets["CLOUD"] = tier(meta.TierTypeCloudS3, "AKCLOUD", secret)
			Expect(render(meta.MaskPeriodTierSecrets(p), denc.Squid)).To(ContainSubstring(`"access_key":"AKCLOUD","secret":"` + want + `",`))
		},
		Entry("a set secret", "cloud-secret", meta.MaskedTierSecret),
		Entry("an empty secret", "", ""),
	)
})
