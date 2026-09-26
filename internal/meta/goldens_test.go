package meta_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"

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
})
