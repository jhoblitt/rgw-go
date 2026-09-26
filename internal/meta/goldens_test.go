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
})
