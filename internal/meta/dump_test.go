package meta_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// These pin what the corpus cannot: the keys RGWZoneParams::dump adds at
// v20.2.4 (rgw_zone.cc:312-340) and the embedded tier config.
var _ = Describe("Dump", func() {
	render := func(v meta.Dumper, rel denc.Release) string {
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		v.Dump(f, rel)
		f.CloseSection()
		return string(f.Bytes())
	}
	It("writes the zone params keys Tentacle adds only on Tentacle", func() {
		z := meta.ZoneParams{ID: "zid", Name: "z1", DedupPool: meta.ParsePool("z1.rgw.dedup"), RestorePool: meta.ParsePool("z1.rgw.restore")}
		squid := render(z, denc.Squid)
		Expect(squid).NotTo(ContainSubstring("dedup_pool"))
		Expect(squid).NotTo(ContainSubstring("bucket_logging_pool"))
		Expect(squid).NotTo(ContainSubstring("restore_pool"))
		tentacle := render(z, denc.Tentacle)
		Expect(tentacle).To(ContainSubstring(`"control_pool":"","dedup_pool":"z1.rgw.dedup","gc_pool":""`))
		Expect(tentacle).To(ContainSubstring(`"group_pool":"","bucket_logging_pool":"","system_key"`))
		Expect(tentacle).To(HaveSuffix(`"realm_id":"","restore_pool":"z1.rgw.restore"}`))
	})
	It("never writes main's vector pool, which neither release dumps", func() {
		z := meta.ZoneParams{Name: "z1", VectorPool: meta.ParsePool("elsewhere")}
		Expect(render(z, denc.Tentacle)).NotTo(ContainSubstring("vector_pool"))
	})
	It("writes a tier config value unquoted when it was stored unquoted, as radosgw does", func() {
		var tc meta.JSONFormattable
		tc.Type = meta.FormattableObject
		tc.Object = map[string]meta.JSONFormattable{
			"retain": {Type: meta.FormattableValue, Value: "+5"},
			"target": {Type: meta.FormattableValue, Value: "s3", Quoted: true},
			"hosts": {Type: meta.FormattableArray, Array: []meta.JSONFormattable{
				{Type: meta.FormattableValue, Value: "a", Quoted: true},
				{},
			}},
		}
		z := meta.ZoneParams{TierConfig: tc}
		Expect(render(z, denc.Squid)).To(ContainSubstring(`"tier_config":{"hosts":["a"],"retain":+5,"target":"s3"}`))
	})
	It("writes the system key as dump_plain and the placement pools as std::map entries", func() {
		std := meta.ParsePool("z1.rgw.buckets.data")
		z := meta.ZoneParams{
			SystemKey: meta.AccessKey{ID: "AK", Secret: "SK"},
			PlacementPools: map[string]meta.ZonePlacementInfo{
				"p2": meta.NewZonePlacementInfo(),
				"p1": {IndexType: 1, StorageClasses: meta.ZoneStorageClasses{"STANDARD": {DataPool: &std}}},
			},
		}
		Expect(render(z, denc.Squid)).To(ContainSubstring(`"system_key":{"access_key":"AK","secret_key":"SK"},` +
			`"placement_pools":[{"key":"p1","val":{"index_pool":"","storage_classes":{"STANDARD":{"data_pool":"z1.rgw.buckets.data"}},"data_extra_pool":"","index_type":1,"inline_data":false}},` +
			`{"key":"p2","val":{"index_pool":"","storage_classes":{"STANDARD":{}},"data_extra_pool":"","index_type":0,"inline_data":true}}],` +
			`"realm_id":""`))
	})
	It("renders Time.Gmtime as utime_t::gmtime does", func() {
		Expect(meta.Time{}.Gmtime()).To(Equal("0.000000"))
		Expect(meta.Time{Time: time.Unix(1348588800, 123456000)}.Gmtime()).To(Equal("2012-09-25T16:00:00.123456Z"))
		Expect(meta.Time{Time: time.Unix(5, 7000)}.Gmtime()).To(Equal("5.000007"), "a time within ten years of the epoch is a duration")
	})
})
