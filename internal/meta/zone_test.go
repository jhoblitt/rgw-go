package meta_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = DescribeTable("root pool object names",
	func(got, want string) { Expect(got).To(Equal(want)) },
	Entry("zone info", meta.ZoneInfoOID("z1"), "zone_info.z1"),
	Entry("zone name", meta.ZoneNameOID("us-east"), "zone_names.us-east"),
	Entry("default zone", meta.DefaultZoneOID("r1"), "default.zone.r1"),
	Entry("default zone without a realm keeps the trailing dot", meta.DefaultZoneOID(""), "default.zone."),
	Entry("zonegroup info", meta.ZoneGroupInfoOID("g1"), "zonegroup_info.g1"),
	Entry("zonegroup name", meta.ZoneGroupNameOID("us"), "zonegroups_names.us"),
	Entry("default zonegroup", meta.DefaultZoneGroupOID("r1"), "default.zonegroup.r1"),
	Entry("default zonegroup without a realm keeps the trailing dot", meta.DefaultZoneGroupOID(""), "default.zonegroup."),
	Entry("realm", meta.RealmOID("r1"), "realms.r1"),
	Entry("realm name", meta.RealmNameOID("gold"), "realms_names.gold"),
	Entry("realm control", meta.RealmControlOID("r1"), "realms.r1.control"),
	Entry("default realm", meta.DefaultRealmOID(), "default.realm"),
	Entry("period", meta.PeriodOID("p1", 7), "periods.p1.7"),
	Entry("staging period omits the epoch", meta.PeriodOID("r1:staging", 7), "periods.r1:staging"),
	Entry("period latest epoch", meta.PeriodLatestEpochOID("p1"), "periods.p1.latest_epoch"),
	Entry("period config", meta.PeriodConfigOID("r1"), "period_config.r1"),
	Entry("period config without a realm", meta.PeriodConfigOID(""), "period_config.default"),
	Entry("root pool", meta.RootPool, ".rgw.root"),
)

// goldenBin reads one corpus object's bytes from the goldens.
func goldenBin(typ, archive, name string) []byte {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("testdata", "goldens", typ, archive, name+".bin"))
	Expect(err).NotTo(HaveOccurred())
	return b
}

// encoded returns the bytes enc writes.
func encoded(enc func(*denc.Encoder)) []byte {
	e := denc.NewEncoder()
	enc(e)
	return e.Bytes()
}

// reframe replaces a struct's header with version v and appends extra to its
// payload, fixing the length.
func reframe(b []byte, v uint8, extra []byte) []byte {
	return encoded(func(e *denc.Encoder) {
		f := e.BeginStruct(v, b[1])
		e.Raw(b[6:])
		e.Raw(extra)
		e.EndStruct(f)
	})
}

func pool(name, ns string) meta.Pool { return meta.Pool{Name: name, NS: ns} }

func encPool(e *denc.Encoder, name, ns string) { pool(name, ns).Encode(e, denc.Squid) }

const squidZoneArchive = "19.2.0-404-g78ddc7f9027"

// zoneParamsV11 builds an RGWZoneParams at version 11, the last to store the
// tier config as old_tier_config, a string map, holding kv.
func zoneParamsV11(kv [][2]string) []byte {
	return encoded(func(e *denc.Encoder) {
		f := e.BeginStruct(11, 1)
		for range 10 {
			encPool(e, "p", "")
		}
		sys := e.BeginStruct(1, 1)
		e.String("id")
		e.String("name")
		e.EndStruct(sys)
		meta.NewAccessKey().Encode(e, denc.Squid)
		e.U32(0)
		encPool(e, "", "")
		e.String("realm")
		encPool(e, "lc", "")
		e.U32(uint32(len(kv)))
		for _, p := range kv {
			e.String(p[0])
			e.String(p[1])
		}
		encPool(e, "roles", "")
		encPool(e, "reshard", "")
		encPool(e, "otp", "")
		e.EndStruct(f)
	})
}

// legacyTier decodes a version 11 zone whose old tier config maps "k" to
// val, and returns what JSONFormattable::set stored under "k".
func legacyTier(val string) meta.JSONFormattable {
	GinkgoHelper()
	z := decodeWhole(zoneParamsV11([][2]string{{"k", val}}), meta.DecodeZoneParams)
	return z.TierConfig.Object["k"]
}

func fvalue(s string, quoted bool) meta.JSONFormattable {
	return meta.JSONFormattable{Type: meta.FormattableValue, Value: s, Quoted: quoted}
}

func farray(xs ...meta.JSONFormattable) meta.JSONFormattable {
	return meta.JSONFormattable{Type: meta.FormattableArray, Array: xs}
}

// The pre-v12 tier config reaches JSONFormattable::set, which parses each
// value with json_spirit and stores a nested scalar as
// json_spirit::write_string prints it.
var _ = Describe("legacy tier config conversion", func() {
	DescribeTable("stores values as json_spirit reads and writes them",
		func(val string, want meta.JSONFormattable) {
			Expect(legacyTier(val)).To(Equal(want))
		},
		Entry("a nested real prints with showpoint at precision 17", "[1.5]", farray(fvalue("1.5000000000000000", false))),
		Entry("a nested real keeps the parser's rounding: 3 * pow(10, -1)", "[0.3]", farray(fvalue("0.30000000000000004", false))),
		Entry("a nested exponent-only number is a real", "[1e2]", farray(fvalue("100.00000000000000", false))),
		Entry("glibc's pow(10, 23) is one ulp above 1e23", "[1e23]", farray(fvalue("1.0000000000000001e+23", false))),
		Entry("a nested -0 is an int64 and prints as 0", "[-0]", farray(fvalue("0", false))),
		Entry("int64_p takes leading zeros and a plus sign", "[007,+5]", farray(fvalue("7", false), fvalue("5", false))),
		Entry("a large integer beyond int64 is a uint64", "[18446744073709551615]", farray(fvalue("18446744073709551615", false))),
		Entry("an integer beyond both fails the parse and is stored whole", "[-9223372036854775809]",
			fvalue("[-9223372036854775809]", true)),
		Entry("a top-level real's printed form is longer than the text, so the text is stored as numeric", "1.5", fvalue("1.5", false)),
		Entry("a top-level -0 likewise", "-0", fvalue("-0", false)),
		Entry("a top-level integer is stored as printed", "42", fvalue("42", false)),
		Entry("a top-level string", `"a\tb"`, fvalue("a\tb", true)),
		Entry("text that is not JSON is quoted", "http://host:80", fvalue("http://host:80", true)),
		Entry("stray commas are accepted", "[1,,2,]", farray(fvalue("1", false), fvalue("2", false))),
		Entry("trailing text after an array is ignored", "[true] trailing", farray(fvalue("true", false))),
		Entry("string bytes are kept whether or not they are UTF-8", "[\"caf\xe9\"]", farray(fvalue("caf\xe9", true))),
		Entry("\\u escapes are encoded one by one, surrogates included", `["\ud83dé"]`, farray(fvalue("\xed\xa0\xbd\xc3\xa9", true))),
		Entry("an unknown escape is dropped with its backslash", `["a\qb"]`, farray(fvalue("ab", true))),
		Entry("nested objects and arrays", `{"b": [1, "x", {"c": null}], "a": false}`, meta.JSONFormattable{
			Type: meta.FormattableObject,
			Object: map[string]meta.JSONFormattable{
				"a": fvalue("false", false),
				"b": farray(fvalue("1", false), fvalue("x", true), meta.JSONFormattable{
					Type:   meta.FormattableObject,
					Object: map[string]meta.JSONFormattable{"c": fvalue("null", false)},
				}),
			},
		}),
	)
	DescribeTable("rejects the forms whose C++ result is not transcribed",
		func(val string) {
			d := denc.NewDecoder(zoneParamsV11([][2]string{{"k", val}}))
			meta.DecodeZoneParams(d)
			Expect(d.Err()).To(MatchError(denc.ErrMalformed))
			Expect(d.Err()).To(MatchError(ContainSubstring("legacy tier config")))
		},
		Entry("a \\x escape", `["\x41"]`),
		Entry("an octal escape", `["\101"]`),
		Entry("a digit run over 300 digits", "["+strings.Repeat("1", 301)+".0]"),
		Entry("an exponent beyond 300", "[1e301]"),
		Entry("an exponent beyond an int, which C++ reads as a double and stores inf", "[1e99999999999]"),
		Entry("an exponent just beyond an int", "[1e2147483648]"),
		Entry("a negative exponent beyond an int, which C++ stores as 0", "[1e-99999999999]"),
		Entry("a negative exponent just beyond an int", "[1e-2147483649]"),
		Entry("exponent 126, whose glibc pow depends on FMA", "[1e126]"),
		Entry("a non-finite result", "[1000000000.0e300]"),
	)
})

var _ = Describe("ZoneParams", func() {
	var z meta.ZoneParams
	BeforeEach(func() {
		z = decodeWhole(goldenBin("RGWZoneParams", squidZoneArchive, "d3cf39ab99a1c7c2fb50f63d7164098d"), meta.DecodeZoneParams)
	})

	It("fills the pools Squid's version 15 predates as RGWZoneParams::decode does", func() {
		Expect(z.RestorePool).To(Equal(pool("default.rgw.log", "restore")))
		Expect(z.DedupPool).To(Equal(pool("default.rgw.dedup", "")))
		Expect(z.BucketLoggingPool).To(Equal(pool("default.rgw.log", "logging")))
		Expect(z.VectorPool).To(Equal(pool("default.rgw.meta", "vector")))
	})

	It("encodes Tentacle's version 18 by appending the restore, dedup and bucket logging pools", func() {
		sq := encoded(func(e *denc.Encoder) { z.Encode(e, denc.Squid) })
		tn := encoded(func(e *denc.Encoder) { z.Encode(e, denc.Tentacle) })
		Expect(sq[:2]).To(Equal([]byte{15, 1}))
		extra := encoded(func(e *denc.Encoder) {
			z.RestorePool.Encode(e, denc.Tentacle)
			z.DedupPool.Encode(e, denc.Tentacle)
			z.BucketLoggingPool.Encode(e, denc.Tentacle)
		})
		Expect(tn).To(Equal(reframe(sq, 18, extra)))
		Expect(decodeWhole(tn, meta.DecodeZoneParams)).To(Equal(z))
	})

	It("decodes main's version 19 vector pool", func() {
		z.VectorPool = pool("vec", "ns")
		tn := encoded(func(e *denc.Encoder) { z.Encode(e, denc.Tentacle) })
		v19 := reframe(tn, 19, encoded(func(e *denc.Encoder) { encPool(e, "vec", "ns") }))
		Expect(decodeWhole(v19, meta.DecodeZoneParams)).To(Equal(z))
		out, err := json.Marshal(z)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(ContainSubstring(`"vector_pool":"vec:ns"`))
	})

	It("fills the pools version 12 predates", func() {
		old := decodeWhole(goldenBin("RGWZoneParams", "15.2.1-40-ga838bb1aae", "03964bc6558314c15355eb0b4b9b8ffe"), meta.DecodeZoneParams)
		Expect(old.OIDCPool).To(Equal(pool(old.Name+".rgw.meta", "oidc")))
		Expect(old.NotifPool).To(Equal(pool(old.LogPool.Name, "notif")))
		Expect(old.TopicsPool).To(Equal(pool(old.Name+".rgw.meta", "topics")))
		Expect(old.AccountPool).To(Equal(pool(old.Name+".rgw.meta", "accounts")))
		Expect(old.GroupPool).To(Equal(pool(old.Name+".rgw.meta", "groups")))
	})

	It("reads version 5, which stored only the name, and the pools it predates", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(5, 1)
			for _, n := range []string{"root", "control", "gc", "log", "intent", "usage", "keys", "email", "swift", "uid"} {
				encPool(e, n, "")
			}
			e.String("zname")
			meta.AccessKey{ID: "AK", Secret: "SK", Active: true}.Encode(e, denc.Squid)
			e.U32(0)           // placement_pools
			encPool(e, "", "") // unused metadata heap
			e.EndStruct(f)
		})
		got := decodeWhole(b, meta.DecodeZoneParams)
		Expect(got.ID).To(Equal("zname"))
		Expect(got.Name).To(Equal("zname"))
		Expect(got.SystemKey).To(Equal(meta.AccessKey{ID: "AK", Secret: "SK", Active: true}))
		Expect(got.UserUIDPool).To(Equal(pool("uid", "")))
		Expect(got.LCPool).To(Equal(pool("log", "lc")))
		Expect(got.RolesPool).To(Equal(pool("zname.rgw.meta", "roles")))
		Expect(got.ReshardPool).To(Equal(pool("log", "reshard")))
		Expect(got.OTPPool).To(Equal(pool("zname.rgw.otp", "")))
		Expect(got.TierConfig.IsZero()).To(BeTrue())
	})

	It("converts version 11's old tier config through JSONFormattable::set", func() {
		// map<string, string, ltstr_nocase>: case-insensitive order, and a
		// key equal but for case to an earlier one is dropped.
		b := zoneParamsV11([][2]string{
			{"a.b", "c"},
			{"Endpoint", "http://x"},
			{"endpoint", "dropped"},
			{"list[]", "1"},
			{"retries", "3"},
		})
		got := decodeWhole(b, meta.DecodeZoneParams)
		Expect(got.RealmID).To(Equal("realm"))
		Expect(got.TierConfig).To(Equal(meta.JSONFormattable{
			Type: meta.FormattableObject,
			Object: map[string]meta.JSONFormattable{
				"a": {Type: meta.FormattableObject, Object: map[string]meta.JSONFormattable{
					"b": {Type: meta.FormattableValue, Value: "c", Quoted: true},
				}},
				"Endpoint": {Type: meta.FormattableValue, Value: "http://x", Quoted: true},
				"list": {Type: meta.FormattableArray, Array: []meta.JSONFormattable{
					{Type: meta.FormattableValue, Value: "1"},
				}},
				"retries": {Type: meta.FormattableValue, Value: "3"},
			},
		}))
		out, err := json.Marshal(got)
		Expect(err).NotTo(HaveOccurred())
		var m map[string]json.RawMessage
		Expect(json.Unmarshal(out, &m)).To(Succeed())
		Expect(m["tier_config"]).To(MatchJSON(`{"a":{"b":"c"},"Endpoint":"http://x","list":[1],"retries":3}`))
	})

	It("omits tier_config from JSON when it holds nothing, as encode_json does", func() {
		out, err := json.Marshal(z)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).NotTo(ContainSubstring("tier_config"))
		Expect(string(out)).NotTo(ContainSubstring("vector_pool"), "main's vector pool at its default")
		Expect(string(out)).To(ContainSubstring(`"restore_pool":"default.rgw.log:restore"`))
	})

	It("returns a pool by its field name", func() {
		p, ok := z.PoolFor(meta.ZonePoolUserUID)
		Expect(ok).To(BeTrue())
		Expect(p).To(Equal(pool("default.rgw.meta", "users.uid")))
		p, ok = z.PoolFor(meta.ZonePoolOIDC)
		Expect(ok).To(BeTrue())
		Expect(p).To(Equal(z.OIDCPool))
		_, ok = z.PoolFor("no_such_pool")
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("Zone", func() {
	It("takes the id from the name below version 4 and keeps the defaults of later fields", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(3, 1)
			e.String("z")
			e.U32(1)
			e.String("http://a")
			e.Bool(true)
			e.Bool(false)
			e.U32(5)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeZone)).To(Equal(meta.Zone{
			ID: "z", Name: "z", Endpoints: []string{"http://a"}, LogMeta: true,
			BucketIndexMaxShards: 5, SyncFromAll: true,
		}))
	})
	It("encodes sets sorted and without duplicates", func() {
		z := meta.Zone{SyncFrom: []string{"b", "a", "b"}, SupportedFeatures: []string{"resharding", "notification_v2"}}
		got := decodeWhole(encoded(func(e *denc.Encoder) { z.Encode(e, denc.Squid) }), meta.DecodeZone)
		Expect(got.SyncFrom).To(Equal([]string{"a", "b"}))
		Expect(got.SupportedFeatures).To(Equal([]string{"notification_v2", "resharding"}))
	})
})

var _ = Describe("ZonePlacementInfo", func() {
	It("moves version 6's data pool and compression into the STANDARD storage class", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(6, 1)
			e.String("idx:ns")
			e.String("data")
			e.String("extra")
			e.U32(0x101) // truncated to BucketIndexType's u8
			e.String("zlib")
			e.EndStruct(f)
		})
		zlib := "zlib"
		want := meta.ZonePlacementInfo{
			IndexPool:      pool("idx", "ns"),
			StorageClasses: meta.ZoneStorageClasses{"STANDARD": {DataPool: &meta.Pool{Name: "data"}, CompressionType: &zlib}},
			DataExtraPool:  pool("extra", ""),
			IndexType:      1,
			InlineData:     true,
		}
		got := decodeWhole(b, meta.DecodeZonePlacementInfo)
		Expect(got).To(Equal(want))
		again := decodeWhole(encoded(func(e *denc.Encoder) { got.Encode(e, denc.Squid) }), meta.DecodeZonePlacementInfo)
		Expect(again).To(Equal(want))
	})
	It("adds an empty STANDARD class to decoded storage classes", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U32(0)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeZoneStorageClasses)).To(Equal(meta.ZoneStorageClasses{"STANDARD": {}}))
	})
})

var _ = Describe("ZoneGroupPlacementTarget", func() {
	It("defaults version 1's storage classes to STANDARD", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.String("default-placement")
			e.U32(1)
			e.String("t")
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeZoneGroupPlacementTarget)).To(Equal(meta.ZoneGroupPlacementTarget{
			Name: "default-placement", Tags: []string{"t"}, StorageClasses: []string{"STANDARD"},
		}))
	})
})

// tierV1 is the corpus's RGWZoneGroupPlacementTier, identical in the 18.2.0
// and 19.2.0 archives; ceph-dencoder has no RGWZoneGroupPlacementTier, so
// there are no goldens for it.
var tierV1 = []byte{1, 1, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

func s3Tier() meta.ZoneGroupPlacementTierS3 {
	return meta.ZoneGroupPlacementTierS3{
		Endpoint: "http://s3", Key: meta.AccessKey{ID: "AK", Secret: "SK", Active: true}, Region: "r",
		HostStyle: 1, TargetStorageClass: "COLD", TargetPath: "p",
		ACLMappings: map[string]meta.TierACLMapping{
			"u1": {Type: 1, SourceID: "u1", DestID: "d1"},
		},
		MultipartSyncThreshold: 7, MultipartMinPartSize: 8,
	}
}

// encS3V1 transcribes RGWZoneGroupPlacementTierS3::encode at version 1.
func encS3V1(e *denc.Encoder, s meta.ZoneGroupPlacementTierS3) {
	f := e.BeginStruct(1, 1)
	e.String(s.Endpoint)
	s.Key.Encode(e, denc.Squid)
	e.String(s.Region)
	e.U32(s.HostStyle)
	e.String(s.TargetStorageClass)
	e.String(s.TargetPath)
	e.U32(uint32(len(s.ACLMappings)))
	for k, m := range s.ACLMappings {
		e.String(k)
		mf := e.BeginStruct(1, 1)
		e.U32(m.Type)
		e.String(m.SourceID)
		e.String(m.DestID)
		e.EndStruct(mf)
	}
	e.U64(s.MultipartSyncThreshold)
	e.U64(s.MultipartMinPartSize)
	e.EndStruct(f)
}

var _ = Describe("ZoneGroupPlacementTier", func() {
	It("round-trips the corpus's version 1 tier and keeps the defaults of later fields", func() {
		got := decodeWhole(tierV1, meta.DecodeZoneGroupPlacementTier)
		Expect(got.ReadThroughRestoreDays).To(Equal(uint64(1)))
		Expect(got.RestoreStorageClass).To(Equal("STANDARD"))
		Expect(got.S3.MultipartSyncThreshold).To(Equal(uint64(32 << 20)))
		Expect(got.S3Glacier).To(Equal(meta.ZoneGroupTierS3Glacier{RestoreDays: 1}))
		Expect(encoded(func(e *denc.Encoder) { got.Encode(e, denc.Squid) })).To(Equal(tierV1))
	})

	var glacier meta.ZoneGroupPlacementTier
	BeforeEach(func() {
		glacier = meta.ZoneGroupPlacementTier{
			TierType: "cloud-s3-glacier", StorageClass: "GLACIER", RetainHeadObject: true,
			S3: s3Tier(), AllowReadThrough: true, ReadThroughRestoreDays: 4, RestoreStorageClass: "STANDARD",
			S3Glacier: meta.ZoneGroupTierS3Glacier{RestoreDays: 3, RestoreTierType: 1},
		}
	})

	It("encodes Tentacle's version 4 with the glacier config", func() {
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(4, 1)
			e.String("cloud-s3-glacier")
			e.String("GLACIER")
			e.Bool(true)
			encS3V1(e, s3Tier())
			e.Bool(true)
			e.U64(4)
			e.String("STANDARD")
			g := e.BeginStruct(1, 1)
			e.U64(3)
			e.U8(1)
			e.EndStruct(g)
			e.EndStruct(f)
		})
		Expect(encoded(func(e *denc.Encoder) { glacier.Encode(e, denc.Tentacle) })).To(Equal(want))
		Expect(decodeWhole(want, meta.DecodeZoneGroupPlacementTier)).To(Equal(glacier))
	})

	It("encodes Squid's version 1, which writes the S3 config only for cloud-s3", func() {
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.String("cloud-s3-glacier")
			e.String("GLACIER")
			e.Bool(true)
			e.EndStruct(f)
		})
		Expect(encoded(func(e *denc.Encoder) { glacier.Encode(e, denc.Squid) })).To(Equal(want))

		s3 := glacier
		s3.TierType = "cloud-s3"
		want = encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.String("cloud-s3")
			e.String("GLACIER")
			e.Bool(true)
			encS3V1(e, s3Tier())
			e.EndStruct(f)
		})
		Expect(encoded(func(e *denc.Encoder) { s3.Encode(e, denc.Squid) })).To(Equal(want))
	})

	It("reads version 2, which put the read-through fields before the S3 config", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			e.String("cloud-s3")
			e.String("COLD")
			e.Bool(false)
			e.Bool(true)
			e.U64(9)
			encS3V1(e, s3Tier())
			e.EndStruct(f)
		})
		got := decodeWhole(b, meta.DecodeZoneGroupPlacementTier)
		Expect(got.AllowReadThrough).To(BeTrue())
		Expect(got.ReadThroughRestoreDays).To(Equal(uint64(9)))
		Expect(got.S3).To(Equal(s3Tier()))
	})

	It("reads main's version 5 and its S3 config's version 3", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(5, 1)
			e.String("cloud-s3")
			e.String("COLD")
			e.Bool(false)
			s3 := encoded(func(e *denc.Encoder) { encS3V1(e, s3Tier()) })
			e.Raw(reframe(s3, 3, encoded(func(e *denc.Encoder) {
				e.String("eu")
				e.Bool(true)
				e.String("pre")
			})))
			e.Bool(false)
			e.U64(1)
			e.String("STANDARD")
			e.Bool(true)
			e.EndStruct(f)
		})
		got := decodeWhole(b, meta.DecodeZoneGroupPlacementTier)
		Expect(got.RetainCurrentVersion).To(BeTrue())
		Expect(got.S3.LocationConstraint).To(Equal("eu"))
		Expect(got.S3.TargetByBucket).To(BeTrue())
		Expect(got.S3.TargetByBucketPrefix).To(Equal("pre"))
	})
})

var _ = Describe("RateLimitInfo", func() {
	var rl meta.RateLimitInfo
	var v1 []byte
	BeforeEach(func() {
		rl = meta.RateLimitInfo{MaxReadOps: 1, MaxWriteOps: 2, MaxListOps: 3, MaxDeleteOps: 4, MaxReadBytes: 5, MaxWriteBytes: 6, Enabled: true}
		v1 = encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.I64(2)
			e.I64(1)
			e.I64(6)
			e.I64(5)
			e.Bool(true)
			e.EndStruct(f)
		})
	})
	It("encodes version 1 at both releases, without the list and delete limits", func() {
		Expect(encoded(func(e *denc.Encoder) { rl.Encode(e, denc.Squid) })).To(Equal(v1))
		Expect(encoded(func(e *denc.Encoder) { rl.Encode(e, denc.Tentacle) })).To(Equal(v1))
	})
	It("reads main's version 2", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			for _, v := range []int64{2, 1, 3, 4, 6, 5} {
				e.I64(v)
			}
			e.Bool(true)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeRateLimitInfo)).To(Equal(rl))
	})
})

// Every rate limit and quota in the corpus is the default, so the order of
// RGWPeriodConfig's fields is pinned here with distinct values.
var _ = Describe("PeriodConfig", func() {
	var c meta.PeriodConfig
	BeforeEach(func() {
		c = meta.PeriodConfig{
			BucketQuota:     meta.Quota{MaxSize: 1024, MaxObjects: 1},
			UserQuota:       meta.Quota{MaxSize: 2048, MaxObjects: 2},
			UserRateLimit:   meta.RateLimitInfo{MaxReadOps: 10},
			BucketRateLimit: meta.RateLimitInfo{MaxReadOps: 20},
			AnonRateLimit:   meta.RateLimitInfo{MaxReadOps: 30},
		}
	})
	It("encodes the bucket quota and rate limit before the user's", func() {
		want := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			c.BucketQuota.Encode(e, denc.Squid)
			c.UserQuota.Encode(e, denc.Squid)
			c.BucketRateLimit.Encode(e, denc.Squid)
			c.UserRateLimit.Encode(e, denc.Squid)
			c.AnonRateLimit.Encode(e, denc.Squid)
			e.EndStruct(f)
		})
		Expect(encoded(func(e *denc.Encoder) { c.Encode(e, denc.Squid) })).To(Equal(want))
		Expect(decodeWhole(want, meta.DecodePeriodConfig)).To(Equal(c))
	})
})

var _ = Describe("PeriodMap", func() {
	It("takes the master zonegroup from the zonegroup marked master, as RGWPeriodMap::decode does", func() {
		pm := meta.PeriodMap{
			ID: "p",
			ZoneGroups: map[string]meta.ZoneGroup{
				"g1": {ID: "g1", Name: "one"},
				"g2": {ID: "g2", Name: "two", IsMaster: true},
			},
			MasterZoneGroup: "stale",
			ShortZoneIDs:    map[string]uint32{"z1": 42},
		}
		got := decodeWhole(encoded(func(e *denc.Encoder) { pm.Encode(e, denc.Squid) }), meta.DecodePeriodMap)
		Expect(got.MasterZoneGroup).To(Equal("g2"))
		Expect(got.ShortZoneIDs).To(Equal(pm.ShortZoneIDs))
	})
})

// emptySyncPolicy is rgw_sync_policy_info with no groups, as the corpus holds it.
var emptySyncPolicy = []byte{1, 1, 4, 0, 0, 0, 0, 0, 0, 0}

var _ = Describe("SyncPolicy", func() {
	It("encodes the zero value as an empty rgw_sync_policy_info", func() {
		Expect(encoded(func(e *denc.Encoder) { meta.SyncPolicy{}.Encode(e, denc.Squid) })).To(Equal(emptySyncPolicy))
		Expect(decodeWhole(emptySyncPolicy, meta.DecodeSyncPolicy)).To(Equal(meta.SyncPolicy{}))
		out, err := json.Marshal(meta.SyncPolicy{})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(MatchJSON(`{"groups":[]}`))
	})
	It("keeps a non-empty policy's bytes verbatim and refuses to render it", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U32(1)
			e.String("group")
			e.Raw([]byte{1, 2, 3})
			e.EndStruct(f)
		})
		p := decodeWhole(b, meta.DecodeSyncPolicy)
		Expect(encoded(func(e *denc.Encoder) { p.Encode(e, denc.Tentacle) })).To(Equal(b))
		_, err := json.Marshal(p)
		Expect(err).To(HaveOccurred())
	})
	It("rejects a compat version above 1", func() {
		d := denc.NewDecoder([]byte{2, 2, 4, 0, 0, 0, 0, 0, 0, 0})
		meta.DecodeSyncPolicy(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})

var _ = Describe("ZoneGroup", func() {
	It("takes the id from the name below version 4", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(3, 1)
			e.String("zg")
			e.String("api")
			e.Bool(true)
			e.U32(0)       // endpoints
			e.String("z1") // master_zone
			e.U32(0)       // zones
			e.U32(0)       // placement_targets
			e.String("default-placement")
			e.U32(0) // hostnames
			e.U32(1) // hostnames_s3website
			e.String("web")
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeZoneGroup)).To(Equal(meta.ZoneGroup{
			ID: "zg", Name: "zg", APIName: "api", IsMaster: true, MasterZone: "z1",
			DefaultPlacement:   meta.PlacementRule{Name: "default-placement"},
			HostnamesS3Website: []string{"web"},
		}))
	})
})

// dsmoV1 is a corpus RGWDefaultSystemMetaObjInfo from the 19.2.0 archive;
// ceph-dencoder has no RGWDefaultSystemMetaObjInfo, so there are no goldens.
var dsmoV1 = append([]byte{1, 1, 40, 0, 0, 0, 36, 0, 0, 0}, "b9d33d85-8234-4f36-92ce-fe80e2223e0d"...)

var _ = Describe("DefaultSystemMetaObjInfo", func() {
	It("round-trips the corpus object", func() {
		got := decodeWhole(dsmoV1, meta.DecodeDefaultSystemMetaObjInfo)
		Expect(got).To(Equal(meta.DefaultSystemMetaObjInfo{DefaultID: "b9d33d85-8234-4f36-92ce-fe80e2223e0d"}))
		Expect(encoded(func(e *denc.Encoder) { got.Encode(e, denc.Squid) })).To(Equal(dsmoV1))
		out, err := json.Marshal(got)
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(MatchJSON(`{"default_id":"b9d33d85-8234-4f36-92ce-fe80e2223e0d"}`))
	})
})

var _ = Describe("JSONFormattable", func() {
	value := func(s string, quoted bool) meta.JSONFormattable {
		return meta.JSONFormattable{Type: meta.FormattableValue, Value: s, Quoted: quoted}
	}
	DescribeTable("marshals as encode_json does",
		func(j meta.JSONFormattable, want string) {
			out, err := json.Marshal(j)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(MatchJSON(want))
		},
		Entry("quoted value", value("foo", true), `"foo"`),
		Entry("unquoted number", value("12", false), `12`),
		Entry("unquoted text that is not JSON falls back to a string", value("foo", false), `"foo"`),
		Entry("array without its empty elements",
			meta.JSONFormattable{Type: meta.FormattableArray, Array: []meta.JSONFormattable{value("a", true), {}, value("true", false)}},
			`["a", true]`),
		Entry("object without its empty members",
			meta.JSONFormattable{Type: meta.FormattableObject, Object: map[string]meta.JSONFormattable{"a": value("b", true), "n": {}}},
			`{"a": "b"}`),
	)
	It("reads version 1 values as quoted", func() {
		b := encoded(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U8(uint8(meta.FormattableValue))
			e.String("v")
			e.U32(0)
			e.U32(0)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, meta.DecodeJSONFormattable)).To(Equal(value("v", true)))
	})
	It("round-trips nested arrays and objects", func() {
		j := meta.JSONFormattable{Type: meta.FormattableObject, Object: map[string]meta.JSONFormattable{
			"b": {Type: meta.FormattableArray, Array: []meta.JSONFormattable{value("1", false), value("x", true)}},
			"a": value("y", true),
		}}
		Expect(decodeWhole(encoded(func(e *denc.Encoder) { j.Encode(e, denc.Squid) }), meta.DecodeJSONFormattable)).To(Equal(j))
	})
})
