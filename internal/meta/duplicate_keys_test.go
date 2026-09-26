package meta_test

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// dupKey1 and dupKey2 differ only in their last byte, so patching every
// dupKey2 in an encoding to dupKey1 repeats a map key without moving a byte.
const (
	dupKey1 = "dupkey-1"
	dupKey2 = "dupkey-2"
)

// repeatKey encodes v, whose map holds dupKey1 then dupKey2, rewrites dupKey2
// to dupKey1 wherever it appears, and decodes the result.
func repeatKey[T any](v T, encode func(T, *denc.Encoder, denc.Release), decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	e := denc.NewEncoder()
	encode(v, e, denc.Squid)
	b := bytes.ReplaceAll(bytes.Clone(e.Bytes()), []byte(dupKey2), []byte(dupKey1))
	Expect(bytes.Contains(b, []byte(dupKey2))).To(BeFalse())
	return decodeWhole(b, decode)
}

// Every std::map in these types whose key or value lacks denc traits decodes
// through C++'s operator[], so a repeated key keeps its last value.
// ceph-dencoder v19.2.6 and v20.2.4 confirm it: an RGWUserInfo whose
// access_keys repeats an id with secrets FIRSTSECRET then SECONDSECRET dumps
// SECONDSECRET, while an RGWUserCaps repeating "users" with perms 1 then 3
// dumps "read", the first, since both its key and value have denc traits.
var _ = Describe("repeated map keys", func() {
	It("keep the last RGWUserInfo access key", func() {
		got := repeatKey(meta.UserInfo{AccessKeys: map[string]meta.AccessKey{
			dupKey1: {ID: dupKey1, Secret: "FIRSTSECRET", Active: true},
			dupKey2: {ID: dupKey2, Secret: "SECONDSECRET", Active: true},
		}}, meta.UserInfo.Encode, meta.DecodeUserInfo)
		Expect(got.AccessKeys).To(HaveLen(1))
		Expect(got.AccessKeys[dupKey1].Secret).To(Equal("SECONDSECRET"))
	})

	It("keep the last RGWUserInfo swift key", func() {
		got := repeatKey(meta.UserInfo{SwiftKeys: map[string]meta.AccessKey{
			dupKey1: {ID: dupKey1, Secret: "first", Active: true},
			dupKey2: {ID: dupKey2, Secret: "second", Active: true},
		}}, meta.UserInfo.Encode, meta.DecodeUserInfo)
		Expect(got.SwiftKeys).To(HaveLen(1))
		Expect(got.SwiftKeys[dupKey1].Secret).To(Equal("second"))
	})

	It("keep the last RGWUserInfo subuser", func() {
		got := repeatKey(meta.UserInfo{SubUsers: map[string]meta.SubUser{
			dupKey1: {Name: dupKey1, Perm: 1},
			dupKey2: {Name: dupKey2, Perm: 2},
		}}, meta.UserInfo.Encode, meta.DecodeUserInfo)
		Expect(got.SubUsers).To(HaveLen(1))
		Expect(got.SubUsers[dupKey1].Perm).To(Equal(uint32(2)))
	})

	It("keep the last RGWZoneParams placement pool", func() {
		got := repeatKey(meta.ZoneParams{PlacementPools: map[string]meta.ZonePlacementInfo{
			dupKey1: {IndexPool: meta.ParsePool("first")},
			dupKey2: {IndexPool: meta.ParsePool("second")},
		}}, meta.ZoneParams.Encode, meta.DecodeZoneParams)
		Expect(got.PlacementPools).To(HaveLen(1))
		Expect(got.PlacementPools[dupKey1].IndexPool).To(Equal(meta.ParsePool("second")))
	})

	It("keep the last RGWZoneStorageClasses class", func() {
		first, second := meta.ParsePool("first"), meta.ParsePool("second")
		got := repeatKey(meta.ZoneStorageClasses{
			dupKey1: {DataPool: &first},
			dupKey2: {DataPool: &second},
		}, meta.ZoneStorageClasses.Encode, meta.DecodeZoneStorageClasses)
		Expect(got[dupKey1].DataPool).To(Equal(&second))
	})

	It("keep the last RGWZoneGroup zone", func() {
		got := repeatKey(meta.ZoneGroup{Zones: map[string]meta.Zone{
			dupKey1: {ID: dupKey1, BucketIndexMaxShards: 1},
			dupKey2: {ID: dupKey2, BucketIndexMaxShards: 2},
		}}, meta.ZoneGroup.Encode, meta.DecodeZoneGroup)
		Expect(got.Zones).To(HaveLen(1))
		Expect(got.Zones[dupKey1].BucketIndexMaxShards).To(Equal(uint32(2)))
	})

	It("keep the last RGWZoneGroup placement target", func() {
		got := repeatKey(meta.ZoneGroup{PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{
			dupKey1: {Name: dupKey1, Tags: []string{"first"}},
			dupKey2: {Name: dupKey2, Tags: []string{"second"}},
		}}, meta.ZoneGroup.Encode, meta.DecodeZoneGroup)
		Expect(got.PlacementTargets).To(HaveLen(1))
		Expect(got.PlacementTargets[dupKey1].Tags).To(Equal([]string{"second"}))
	})

	It("keep the last RGWZoneGroupPlacementTarget tier target", func() {
		got := repeatKey(meta.ZoneGroupPlacementTarget{TierTargets: map[string]meta.ZoneGroupPlacementTier{
			dupKey1: {StorageClass: dupKey1, TierType: "first"},
			dupKey2: {StorageClass: dupKey2, TierType: "second"},
		}}, meta.ZoneGroupPlacementTarget.Encode, meta.DecodeZoneGroupPlacementTarget)
		Expect(got.TierTargets).To(HaveLen(1))
		Expect(got.TierTargets[dupKey1].TierType).To(Equal("second"))
	})

	It("keep the last RGWZoneGroupPlacementTierS3 ACL mapping", func() {
		got := repeatKey(meta.ZoneGroupPlacementTierS3{ACLMappings: map[string]meta.TierACLMapping{
			dupKey1: {SourceID: dupKey1, DestID: "first"},
			dupKey2: {SourceID: dupKey2, DestID: "second"},
		}}, meta.ZoneGroupPlacementTierS3.Encode, meta.DecodeZoneGroupPlacementTierS3)
		Expect(got.ACLMappings).To(HaveLen(1))
		Expect(got.ACLMappings[dupKey1].DestID).To(Equal("second"))
	})

	It("keep the last RGWPeriodMap zonegroup", func() {
		got := repeatKey(meta.PeriodMap{ZoneGroups: map[string]meta.ZoneGroup{
			dupKey1: {ID: dupKey1, APIName: "first"},
			dupKey2: {ID: dupKey2, APIName: "second"},
		}}, meta.PeriodMap.Encode, meta.DecodePeriodMap)
		Expect(got.ZoneGroups).To(HaveLen(1))
		Expect(got.ZoneGroups[dupKey1].APIName).To(Equal("second"))
	})

	It("keep the last JSONFormattable object member", func() {
		got := repeatKey(meta.JSONFormattable{Type: meta.FormattableObject, Object: map[string]meta.JSONFormattable{
			dupKey1: fvalue("first", true),
			dupKey2: fvalue("second", true),
		}}, meta.JSONFormattable.Encode, meta.DecodeJSONFormattable)
		Expect(got.Object).To(HaveLen(1))
		Expect(got.Object[dupKey1].Value).To(Equal("second"))
	})

	It("keep the first RGWUserCaps entry, whose key and value have denc traits", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U32(2)
			e.String("users")
			e.U32(1)
			e.String("users")
			e.U32(3)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeCaps)).To(Equal(meta.Caps{"users": 1}))
	})
})
