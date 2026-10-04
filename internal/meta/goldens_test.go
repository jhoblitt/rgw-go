package meta_test

import (
	"bytes"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// ceph-dencoder dumps some types through their own dump() method, whose shape
// differs from the encode_json overload RGW uses when the type is a field of a
// larger struct. The Go types marshal in the field form; these wrappers render
// the dump() form so the corpus JSON is still compared.

// userDump is rgw_user::dump: the string form under "user".
type userDump struct {
	User meta.UserID `json:"user"`
}

// poolDump is rgw_pool::dump: name and namespace as separate fields.
type poolDump struct{ meta.Pool }

func (p poolDump) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"name": p.Name, "ns": p.NS})
}

// placementDump is rgw_placement_rule::dump: the name and the canonical storage class.
type placementDump struct{ meta.PlacementRule }

func (p placementDump) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"name": p.Name, "storage_class": p.CanonicalStorageClass()})
}

// capsDump is RGWUserCaps::dump: the cap list under "caps".
type capsDump struct {
	Caps meta.Caps `json:"caps"`
}

// uidDump is RGWUID::dump: the id under "user_id".
type uidDump struct {
	UserID meta.UID `json:"user_id"`
}

// JSON policy (see the package doc): where a type's dump differs between
// releases the Go JSON follows v20.2.4's, and a field only main has appears
// only when set, which the corpus never does. The goldens come from the v19
// dencoder, so squidView drops the keys v20.2.4 added before comparing;
// every other key, main-only ones included, must match.
var postSquidKeys = map[string]bool{
	// RGWZoneParams.
	"restore_pool":        true,
	"dedup_pool":          true,
	"bucket_logging_pool": true,
	// RGWZoneGroupPlacementTier.
	"allow_read_through":        true,
	"read_through_restore_days": true,
	"restore_storage_class":     true,
	"s3-glacier":                true,
}

// squidView renders v's JSON without postSquidKeys, at any depth.
type squidView[T any] struct{ v T }

func (s squidView[T]) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(s.v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var x any
	if err := d.Decode(&x); err != nil {
		return nil, err
	}
	return json.Marshal(dropKeys(x))
}

func dropKeys(x any) any {
	switch v := x.(type) {
	case map[string]any:
		for k, e := range v {
			if postSquidKeys[k] {
				delete(v, k)
				continue
			}
			v[k] = dropKeys(e)
		}
	case []any:
		for i, e := range v {
			v[i] = dropKeys(e)
		}
	}
	return x
}

// squidRoundTrip is goldentest.RoundTrip at Squid through squidView.
func squidRoundTrip[T any](typ string, decode func(*denc.Decoder) T, encode func(*denc.Encoder, T, denc.Release)) {
	GinkgoHelper()
	goldentest.RoundTrip("testdata", typ, squid,
		func(d *denc.Decoder) squidView[T] { return squidView[T]{decode(d)} },
		func(e *denc.Encoder, v squidView[T], r denc.Release) { encode(e, v.v, r) })
}

// formattableDump is JSONFormattable::dump, which ceph-dencoder prints: each
// node is wrapped as {"value": ...}, {"array": [...]} or {"object": {...}},
// and inside an array the wrapper's name is dropped, as JSONFormatter drops
// names there. The embedded form, encode_json, has no wrappers.
type formattableDump struct{ meta.JSONFormattable }

func (f formattableDump) MarshalJSON() ([]byte, error) {
	v, _ := dumpFormattable(f.JSONFormattable)
	return json.Marshal(v)
}

// dumpFormattable returns the section JSONFormattable::dump writes and its
// name; an empty name means the node writes nothing.
func dumpFormattable(j meta.JSONFormattable) (map[string]any, string) {
	switch j.Type {
	case meta.FormattableValue:
		var v any = j.Value
		if !j.Quoted {
			v = json.RawMessage(j.Value)
		}
		return map[string]any{"value": v}, "value"
	case meta.FormattableArray:
		arr := []any{}
		for _, e := range j.Array {
			if inner, name := dumpFormattable(e); name != "" {
				arr = append(arr, inner[name])
			}
		}
		return map[string]any{"array": arr}, "array"
	case meta.FormattableObject:
		obj := map[string]any{}
		for k, e := range j.Object {
			obj[k], _ = dumpFormattable(e)
		}
		return map[string]any{"object": obj}, "object"
	default:
		return map[string]any{}, ""
	}
}

// squid checks re-encodings against goldens from the v19 dencoder image.
var squid = goldentest.Options{Release: denc.Squid}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("rgw_user", func() {
		goldentest.RoundTrip(dir, "rgw_user", squid,
			func(d *denc.Decoder) userDump { return userDump{meta.DecodeUserID(d)} },
			func(e *denc.Encoder, v userDump, r denc.Release) { v.User.Encode(e, r) })
	})
	It("rgw_bucket", func() {
		goldentest.RoundTrip(dir, "rgw_bucket", squid, meta.DecodeBucketID,
			func(e *denc.Encoder, v meta.BucketID, r denc.Release) { v.Encode(e, r) })
	})
	It("rgw_pool", func() {
		goldentest.RoundTrip(dir, "rgw_pool", squid,
			func(d *denc.Decoder) poolDump { return poolDump{meta.DecodePool(d)} },
			func(e *denc.Encoder, v poolDump, r denc.Release) { v.Encode(e, r) })
	})
	It("rgw_placement_rule", func() {
		goldentest.RoundTrip(dir, "rgw_placement_rule", squid,
			func(d *denc.Decoder) placementDump { return placementDump{meta.DecodePlacementRule(d)} },
			func(e *denc.Encoder, v placementDump, r denc.Release) { v.Encode(e, r) })
	})
	It("rgw_obj", func() {
		goldentest.RoundTrip(dir, "rgw_obj", squid, meta.DecodeObj,
			func(e *denc.Encoder, v meta.Obj, r denc.Release) { v.Encode(e, r) })
	})
	It("rgw_raw_obj", func() {
		goldentest.RoundTrip(dir, "rgw_raw_obj", squid, meta.DecodeRawObj,
			func(e *denc.Encoder, v meta.RawObj, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWQuotaInfo", func() {
		goldentest.RoundTrip(dir, "RGWQuotaInfo", squid, meta.DecodeQuota,
			func(e *denc.Encoder, v meta.Quota, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWAccessKey", func() {
		goldentest.RoundTrip(dir, "RGWAccessKey", squid, meta.DecodeAccessKey,
			func(e *denc.Encoder, v meta.AccessKey, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWSubUser", func() {
		goldentest.RoundTrip(dir, "RGWSubUser", squid, meta.DecodeSubUser,
			func(e *denc.Encoder, v meta.SubUser, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWUserCaps", func() {
		goldentest.RoundTrip(dir, "RGWUserCaps", squid,
			func(d *denc.Decoder) capsDump { return capsDump{meta.DecodeCaps(d)} },
			func(e *denc.Encoder, v capsDump, r denc.Release) { v.Caps.Encode(e, r) })
	})
	It("RGWUserInfo", func() {
		goldentest.RoundTrip(dir, "RGWUserInfo", squid, meta.DecodeUserInfo,
			func(e *denc.Encoder, v meta.UserInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWUID", func() {
		goldentest.RoundTrip(dir, "RGWUID", squid,
			func(d *denc.Decoder) uidDump { return uidDump{meta.DecodeUID(d)} },
			func(e *denc.Encoder, v uidDump, r denc.Release) { v.UserID.Encode(e, r) })
	})
	It("RGWAccountInfo", func() {
		goldentest.RoundTrip(dir, "RGWAccountInfo", squid, meta.DecodeAccountInfo,
			func(e *denc.Encoder, v meta.AccountInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWBucketEntryPoint", func() {
		goldentest.RoundTrip(dir, "RGWBucketEntryPoint", squid, meta.DecodeBucketEntryPoint,
			func(e *denc.Encoder, v meta.BucketEntryPoint, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWBucketInfo", func() {
		goldentest.RoundTrip(dir, "RGWBucketInfo", squid, meta.DecodeBucketInfo,
			func(e *denc.Encoder, v meta.BucketInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWBucketEnt", func() {
		goldentest.RoundTrip(dir, "RGWBucketEnt", squid, meta.DecodeBucketEnt,
			func(e *denc.Encoder, v meta.BucketEnt, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZoneParams", func() {
		squidRoundTrip("RGWZoneParams", meta.DecodeZoneParams,
			func(e *denc.Encoder, v meta.ZoneParams, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZoneGroup", func() {
		squidRoundTrip("RGWZoneGroup", meta.DecodeZoneGroup,
			func(e *denc.Encoder, v meta.ZoneGroup, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZone", func() {
		goldentest.RoundTrip(dir, "RGWZone", squid, meta.DecodeZone,
			func(e *denc.Encoder, v meta.Zone, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZonePlacementInfo", func() {
		goldentest.RoundTrip(dir, "RGWZonePlacementInfo", squid, meta.DecodeZonePlacementInfo,
			func(e *denc.Encoder, v meta.ZonePlacementInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZoneGroupPlacementTarget", func() {
		squidRoundTrip("RGWZoneGroupPlacementTarget", meta.DecodeZoneGroupPlacementTarget,
			func(e *denc.Encoder, v meta.ZoneGroupPlacementTarget, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZoneStorageClasses", func() {
		goldentest.RoundTrip(dir, "RGWZoneStorageClasses", squid, meta.DecodeZoneStorageClasses,
			func(e *denc.Encoder, v meta.ZoneStorageClasses, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWZoneStorageClass", func() {
		goldentest.RoundTrip(dir, "RGWZoneStorageClass", squid, meta.DecodeZoneStorageClass,
			func(e *denc.Encoder, v meta.ZoneStorageClass, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWRealm", func() {
		goldentest.RoundTrip(dir, "RGWRealm", squid, meta.DecodeRealm,
			func(e *denc.Encoder, v meta.Realm, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWPeriod", func() {
		squidRoundTrip("RGWPeriod", meta.DecodePeriod,
			func(e *denc.Encoder, v meta.Period, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWPeriodLatestEpochInfo", func() {
		goldentest.RoundTrip(dir, "RGWPeriodLatestEpochInfo", squid, meta.DecodePeriodLatestEpochInfo,
			func(e *denc.Encoder, v meta.PeriodLatestEpochInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWNameToId", func() {
		goldentest.RoundTrip(dir, "RGWNameToId", squid, meta.DecodeNameToID,
			func(e *denc.Encoder, v meta.NameToID, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWObjManifest", func() {
		goldentest.RoundTrip(dir, "RGWObjManifest", squid, meta.DecodeManifest,
			func(e *denc.Encoder, v meta.Manifest, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWObjManifestPart", func() {
		goldentest.RoundTrip(dir, "RGWObjManifestPart", squid, meta.DecodeManifestPart,
			func(e *denc.Encoder, v meta.ManifestPart, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWObjManifestRule", func() {
		goldentest.RoundTrip(dir, "RGWObjManifestRule", squid, meta.DecodeManifestRule,
			func(e *denc.Encoder, v meta.ManifestRule, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWObjTier", func() {
		squidRoundTrip("RGWObjTier", meta.DecodeObjTier,
			func(e *denc.Encoder, v meta.ObjTier, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWCompressionInfo", func() {
		goldentest.RoundTrip(dir, "RGWCompressionInfo", squid, meta.DecodeCompressionInfo,
			func(e *denc.Encoder, v meta.CompressionInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("RGWCacheNotifyInfo", func() {
		goldentest.RoundTrip(dir, "RGWCacheNotifyInfo", squid, meta.DecodeCacheNotifyInfo,
			func(e *denc.Encoder, v meta.CacheNotifyInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("ObjectCacheInfo", func() {
		goldentest.RoundTrip(dir, "ObjectCacheInfo", squid, meta.DecodeObjectCacheInfo,
			func(e *denc.Encoder, v meta.ObjectCacheInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("ObjectMetaInfo", func() {
		goldentest.RoundTrip(dir, "ObjectMetaInfo", squid, meta.DecodeObjectMetaInfo,
			func(e *denc.Encoder, v meta.ObjectMetaInfo, r denc.Release) { v.Encode(e, r) })
	})
	// One corpus object holds an unquoted non-numeric value, which
	// JSONFormattable::dump prints bare, so its dencoder JSON does not parse:
	// bytes are compared here and JSON in the next spec.
	It("JSONFormattable", func() {
		goldentest.RoundTrip(dir, "JSONFormattable", goldentest.Options{Release: denc.Squid, SkipJSON: true},
			meta.DecodeJSONFormattable,
			func(e *denc.Encoder, v meta.JSONFormattable, r denc.Release) { v.Encode(e, r) })
	})
	It("JSONFormattable JSON", func() {
		cs, err := goldentest.Load(dir, "JSONFormattable")
		Expect(err).NotTo(HaveOccurred())
		Expect(cs).NotTo(BeEmpty())
		invalid := 0
		for _, c := range cs {
			v := decodeWhole(c.Bin, meta.DecodeJSONFormattable)
			if !json.Valid(c.JSON) {
				invalid++
				Expect(v.Quoted).To(BeFalse(), c.Name)
				Expect(string(c.JSON)).To(ContainSubstring(`"value": `+v.Value), c.Name)
				continue
			}
			got, err := json.Marshal(formattableDump{v})
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(MatchJSON(c.JSON), c.Name)
		}
		Expect(invalid).To(Equal(1))
	})
	// Neither type has a Go JSON form to compare: RGWUploadPartInfo::dump
	// prints only num, size, etag, modified and past_prefixes (rgw_multi.cc:94-102
	// at v19.2.6), and multipart_upload_info::dump only the placement's dump.
	It("RGWUploadPartInfo", func() {
		goldentest.RoundTrip(dir, "RGWUploadPartInfo", goldentest.Options{Release: denc.Squid, SkipJSON: true}, meta.DecodeUploadPartInfo,
			func(e *denc.Encoder, v meta.UploadPartInfo, r denc.Release) { v.Encode(e, r) })
	})
	It("multipart_upload_info", func() {
		goldentest.RoundTrip(dir, "multipart_upload_info", goldentest.Options{Release: denc.Squid, SkipJSON: true}, meta.DecodeMultipartUploadInfo,
			func(e *denc.Encoder, v meta.MultipartUploadInfo, r denc.Release) { v.Encode(e, r) })
	})
})
