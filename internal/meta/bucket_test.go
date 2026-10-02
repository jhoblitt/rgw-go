package meta_test

import (
	"encoding/hex"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

func loadGoldens(typ string) ([]goldentest.Case, error) { return goldentest.Load("testdata", typ) }

func encodeWith(r denc.Release, enc func(*denc.Encoder, denc.Release)) []byte {
	e := denc.NewEncoder()
	enc(e, r)
	return e.Bytes()
}

func decodeErr[T any](b []byte, decode func(*denc.Decoder) T) error {
	d := denc.NewDecoder(b)
	decode(d)
	return d.Err()
}

var alice = meta.UserID{Tenant: "t1", ID: "alice"}

var _ = Describe("Owner", func() {
	var userBytes []byte
	BeforeEach(func() { userBytes = encodeWith(denc.Squid, alice.Encode) })

	It("writes a user in the converted form as the bare rgw_user", func() {
		o := meta.UserOwner(alice)
		Expect(encodeWith(denc.Squid, o.EncodeConverted)).To(Equal(userBytes))
		Expect(decodeWhole(userBytes, meta.DecodeOwnerConverted)).To(Equal(o))
	})
	It("writes an account in the converted form under a version 129 header", func() {
		o := meta.AccountOwner("RGW12345678901234567")
		e := denc.NewEncoder()
		f := e.BeginStruct(129, 129)
		e.String("RGW12345678901234567")
		e.EndStruct(f)
		Expect(encodeWith(denc.Squid, o.EncodeConverted)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), meta.DecodeOwnerConverted)).To(Equal(o))
	})
	It("writes a user in the versioned form under a version 0 header", func() {
		o := meta.UserOwner(alice)
		e := denc.NewEncoder()
		f := e.BeginStruct(0, 0)
		e.Raw(userBytes)
		e.EndStruct(f)
		Expect(encodeWith(denc.Squid, o.EncodeVersioned)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), meta.DecodeOwnerVersioned)).To(Equal(o))
	})
	It("writes an account in the versioned form under a version 1 header", func() {
		o := meta.AccountOwner("RGW12345678901234567")
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("RGW12345678901234567")
		e.EndStruct(f)
		Expect(encodeWith(denc.Squid, o.EncodeVersioned)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), meta.DecodeOwnerVersioned)).To(Equal(o))
	})
	It("reads a version 1 rgw_user, which had no namespace, in the converted form", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("t1")
		e.String("alice")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), meta.DecodeOwnerConverted)).To(Equal(meta.UserOwner(alice)))
	})
	It("applies rgw_user's compat check to a converted user", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(3, 3)
		e.String("")
		e.String("alice")
		e.String("")
		e.EndStruct(f)
		Expect(decodeErr(e.Bytes(), meta.DecodeOwnerConverted)).To(MatchError(denc.ErrIncompatible))
	})
	It("rejects a converted variant index with no alternative", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(130, 129)
		e.String("x")
		e.EndStruct(f)
		Expect(decodeErr(e.Bytes(), meta.DecodeOwnerConverted)).To(MatchError(denc.ErrMalformed))
	})
	It("rejects a versioned variant index with no alternative", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		e.String("x")
		e.EndStruct(f)
		Expect(decodeErr(e.Bytes(), meta.DecodeOwnerVersioned)).To(MatchError(denc.ErrMalformed))
	})
	It("marshals as its string form and parses account ids as parse_owner does", func() {
		Expect(mustMarshal(meta.UserOwner(alice))).To(Equal(`"t1$alice"`))
		Expect(mustMarshal(meta.AccountOwner("RGW12345678901234567"))).To(Equal(`"RGW12345678901234567"`))
		Expect(meta.ParseOwner("RGW12345678901234567")).To(Equal(meta.AccountOwner("RGW12345678901234567")))
		Expect(meta.ParseOwner("RGW1234567890123456x")).To(Equal(meta.UserOwner(meta.UserID{ID: "RGW1234567890123456x"})))
		Expect(meta.ParseOwner("RGW1234")).To(Equal(meta.UserOwner(meta.UserID{ID: "RGW1234"})))
		var o meta.Owner
		Expect(o.UnmarshalJSON([]byte(`"t1$alice"`))).To(Succeed())
		Expect(o).To(Equal(meta.UserOwner(alice)))
	})
})

var shardedInfo = meta.BucketInfo{Bucket: meta.BucketID{Name: "plain", ID: "zone.4156.1"}}

var _ = Describe("BucketInfo index shards", func() {
	gen := func(g uint64, shards uint32) meta.IndexLayoutGen {
		l := meta.NewIndexLayoutGen()
		l.Gen = g
		l.Layout.Normal.NumShards = shards
		return l
	}
	It("names the single object of an unsharded index after the bucket id", func() {
		Expect(shardedInfo.IndexShardOID(gen(0, 0), 0)).To(Equal(".dir.zone.4156.1"))
		Expect(shardedInfo.IndexShardOID(gen(3, 0), 5)).To(Equal(".dir.zone.4156.1"))
	})
	It("leaves generation 0 out of shard names", func() {
		Expect(shardedInfo.IndexShardOID(gen(0, 11), 3)).To(Equal(".dir.zone.4156.1.3"))
	})
	It("puts a later generation before the shard", func() {
		Expect(shardedInfo.IndexShardOID(gen(2, 11), 3)).To(Equal(".dir.zone.4156.1.2.3"))
	})

	// Recorded from the populated Squid cluster of the retired podman harness
	// against bucket "plain", 11 shards, by listing every shard's omap keys,
	// and re-checked on the populated Tentacle rooket cluster by listing every
	// shard in its Rook toolbox:
	//   rooket k -n rook-ceph exec deploy/rook-ceph-tools -- rados \
	//     -p <index pool> listomapkeys .dir.<bucket id>.<shard>   # for shard in 0..10
	// small.bin was in shard 3; _underscore.bin, listed under its index key
	// __underscore.bin, was in shard 6, which shows the name, not the index
	// key, is what gets hashed.
	DescribeTable("selects the shard radosgw placed each object in",
		func(name string, want uint32) {
			shard, ok := meta.IndexShard(name, 11)
			Expect(ok).To(BeTrue())
			Expect(shard).To(Equal(want))
		},
		Entry(nil, "small.bin", uint32(3)),
		Entry(nil, "large.bin", uint32(1)),
		Entry(nil, "meta.bin", uint32(1)),
		Entry(nil, "empty.bin", uint32(5)),
		Entry(nil, "_underscore.bin", uint32(6)),
		Entry(nil, "multipart.bin", uint32(7)),
		Entry(nil, "head-full.bin", uint32(8)),
	)
	// ceph_str_hash_linux("a") is (97<<4 + 97>>4) * 11 = 17138 (0x42f2); the
	// fold gives 0xf20042f2 = 4060103410, which is 6161 mod 7877 and 29124
	// mod 65521.
	DescribeTable("switches primes above 7877 shards",
		func(shards, want uint32) {
			shard, ok := meta.IndexShard("a", shards)
			Expect(ok).To(BeTrue())
			Expect(shard).To(Equal(want))
		},
		Entry("11 shards", uint32(11), uint32(6161%11)),
		Entry("7877 shards", uint32(7877), uint32(6161)),
		Entry("7878 shards", uint32(7878), uint32(29124%7878)),
		Entry("10000 shards", uint32(10000), uint32(9124)),
	)
	It("has no shard for an unsharded index, where C++ returns RGW_NO_SHARD", func() {
		_, ok := meta.IndexShard("small.bin", 0)
		Expect(ok).To(BeFalse())
	})
	It("hashes as ceph_str_hash_linux", func() {
		Expect(meta.StrHashLinux("a")).To(BeEquivalentTo(17138))
	})
})

// indexNormalBytes is bucket_index_normal_layout as Squid writes it (v1) or
// Tentacle (v2, with min_num_shards).
func indexNormalBytes(r denc.Release, shards, minShards uint32) []byte {
	e := denc.NewEncoder()
	if r == denc.Squid {
		f := e.BeginStruct(1, 1)
		e.U32(shards)
		e.U8(0)
		e.EndStruct(f)
		return e.Bytes()
	}
	f := e.BeginStruct(2, 1)
	e.U32(shards)
	e.U8(0)
	e.U32(minShards)
	e.EndStruct(f)
	return e.Bytes()
}

func beginEnd(e *denc.Encoder, v, c uint8, body func()) {
	f := e.BeginStruct(v, c)
	body()
	e.EndStruct(f)
}

var normal = meta.IndexNormalLayout{NumShards: 11, MinNumShards: 3}

var _ = Describe("bucket index layout", func() {
	It("writes bucket_index_normal_layout v1 on Squid and v2 with min_num_shards on Tentacle", func() {
		Expect(encodeWith(denc.Squid, normal.Encode)).To(Equal(indexNormalBytes(denc.Squid, 11, 3)))
		Expect(encodeWith(denc.Tentacle, normal.Encode)).To(Equal(indexNormalBytes(denc.Tentacle, 11, 3)))
	})
	It("reads a v1 normal layout with the default min_num_shards of 1", func() {
		Expect(decodeWhole(indexNormalBytes(denc.Squid, 11, 0), meta.DecodeIndexNormalLayout)).To(Equal(
			meta.IndexNormalLayout{NumShards: 11, MinNumShards: 1}))
		Expect(decodeWhole(indexNormalBytes(denc.Tentacle, 11, 3), meta.DecodeIndexNormalLayout)).To(Equal(normal))
	})
	It("writes no normal layout for an indexless bucket and keeps the defaults on decode", func() {
		l := meta.IndexLayout{Type: meta.IndexIndexless, Normal: meta.NewIndexNormalLayout()}
		want := []byte{1, 1, 1, 0, 0, 0, 1}
		Expect(encodeWith(denc.Tentacle, l.Encode)).To(Equal(want))
		Expect(decodeWhole(want, meta.DecodeIndexLayout)).To(Equal(l))
	})

	layout := func() meta.BucketLayout {
		l := meta.NewBucketLayout()
		l.Resharding = meta.ReshardInProgress
		l.Current.Gen = 1
		l.Current.Layout.Normal = normal
		target := meta.NewIndexLayoutGen()
		target.Gen = 2
		target.Layout.Normal.NumShards = 23
		l.Target = &target
		l.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(1, l.Current)}
		return l
	}
	// layoutBytes builds rgw::BucketLayout field by field for r: v2 on Squid,
	// v3 with judge_reshard_lock_time on Tentacle.
	layoutBytes := func(r denc.Release, judge time.Time) []byte {
		e := denc.NewEncoder()
		v := uint8(3)
		if r == denc.Squid {
			v = 2
		}
		indexGen := func(gen uint64, shards, minShards uint32) {
			beginEnd(e, 1, 1, func() {
				e.U64(gen)
				beginEnd(e, 1, 1, func() {
					e.U8(0)
					e.Raw(indexNormalBytes(r, shards, minShards))
				})
			})
		}
		beginEnd(e, v, 1, func() {
			e.U8(1)
			indexGen(1, 11, 3)
			e.Bool(true)
			indexGen(2, 23, 1)
			e.U32(1)
			beginEnd(e, 1, 1, func() {
				e.U64(1)
				beginEnd(e, 1, 1, func() {
					e.U8(0)
					beginEnd(e, 1, 1, func() {
						e.U64(1)
						e.Raw(indexNormalBytes(r, 11, 3))
					})
				})
			})
			if r != denc.Squid {
				e.Time(judge)
			}
		})
		return e.Bytes()
	}

	It("writes rgw::BucketLayout v2 on Squid and v3 with the reshard lock time on Tentacle", func() {
		judge := time.Unix(1_700_000_000, 5000).UTC()
		l := layout()
		l.JudgeReshardLockTime = meta.Time{Time: judge}
		Expect(encodeWith(denc.Squid, l.Encode)).To(Equal(layoutBytes(denc.Squid, judge)))
		Expect(encodeWith(denc.Tentacle, l.Encode)).To(Equal(layoutBytes(denc.Tentacle, judge)))
		Expect(decodeWhole(layoutBytes(denc.Tentacle, judge), meta.DecodeBucketLayout)).To(Equal(l))
	})
	It("reads v1, which had no logs, deriving one from the current index", func() {
		e := denc.NewEncoder()
		beginEnd(e, 1, 1, func() {
			e.U8(0)
			beginEnd(e, 1, 1, func() {
				e.U64(4)
				beginEnd(e, 1, 1, func() {
					e.U8(0)
					e.Raw(indexNormalBytes(denc.Squid, 7, 0))
				})
			})
			e.Bool(false)
		})
		want := meta.NewBucketLayout()
		want.Current.Gen = 4
		want.Current.Layout.Normal.NumShards = 7
		want.Logs = []meta.LogLayoutGen{{Gen: 0, Layout: meta.LogLayout{
			Type:    meta.LogInIndex,
			InIndex: meta.IndexLogLayout{Gen: 4, Layout: want.Current.Layout.Normal},
			FIFO:    meta.FIFOLogLayout{NumShards: 7},
		}}}
		Expect(decodeWhole(e.Bytes(), meta.DecodeBucketLayout)).To(Equal(want))
	})
	It("reads main's FIFO log layout and writes only its type, as Squid and Tentacle do", func() {
		e := denc.NewEncoder()
		beginEnd(e, 1, 1, func() {
			e.U8(2)
			beginEnd(e, 1, 1, func() {
				e.U32(13)
				e.U8(0)
			})
		})
		got := decodeWhole(e.Bytes(), meta.DecodeLogLayout)
		Expect(got.Type).To(Equal(meta.LogFIFO))
		Expect(got.FIFO).To(Equal(meta.FIFOLogLayout{NumShards: 13}))
		Expect(encodeWith(denc.Tentacle, got.Encode)).To(Equal([]byte{1, 1, 1, 0, 0, 0, 2}))
	})
	It("reads and writes Tentacle's Deleted log layout as its type alone", func() {
		b := []byte{1, 1, 1, 0, 0, 0, 1}
		got := decodeWhole(b, meta.DecodeLogLayout)
		Expect(got.Type).To(Equal(meta.LogDeleted))
		Expect(encodeWith(denc.Squid, got.Encode)).To(Equal(b))
	})
	It("marshals as encode_json_impl does on Tentacle", func() {
		l := layout()
		Expect(mustMarshal(l)).To(MatchJSON(`{
			"resharding": "InProgress",
			"current_index": {"gen": 1, "layout": {"type": "Normal",
				"normal": {"num_shards": 11, "hash_type": "Mod", "min_num_shards": 3}}},
			"target_index": {"gen": 2, "layout": {"type": "Normal",
				"normal": {"num_shards": 23, "hash_type": "Mod", "min_num_shards": 1}}},
			"logs": [{"gen": 1, "layout": {"type": "InIndex", "in_index": {"gen": 1,
				"layout": {"num_shards": 11, "hash_type": "Mod", "min_num_shards": 3}}}}],
			"judge_reshard_lock_time": "0.000000"
		}`))
		l.Target = nil
		l.Logs = []meta.LogLayoutGen{{Layout: meta.LogLayout{Type: meta.LogDeleted}}}
		Expect(mustMarshal(l)).NotTo(ContainSubstring("target_index"))
		Expect(mustMarshal(l)).To(ContainSubstring(`"logs":[{"gen":0,"layout":{"type":"Deleted"}}]`))
	})
})

// tentacleBucketInfo is ceph-dencoder v20.2.4's re-encoding of the corpus
// object RGWBucketInfo/0.61.4-60-g24c59be/25f5ff183bba6da708243a0bbbe40a2e, a
// version 4 bucket info:
//
//	podman run --rm -v <corpus>/archive/0.61.4-60-g24c59be/objects/RGWBucketInfo:/in:ro \
//	  quay.io/ceph/ceph:v20.2.4 ceph-dencoder type RGWBucketInfo \
//	  import /in/25f5ff183bba6da708243a0bbbe40a2e decode encode export /dev/stdout | xxd -p
const tentacleBucketInfo = "1804ef0000000a0a110000000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000003011a000000ffffffffffffffffffffffffffffffff" +
	"00ffffffffffffffff0000000000000000000000000000000000000000000000000000030164" +
	"0000000001011e00000000000000000000000101100000000002010900000001000000000100" +
	"0000000100000001012c000000000000000000000001011e0000000001011700000000000000" +
	"0000000002010900000001000000000100000000000000000000000000000000001200000002" +
	"010c000000000000000000000000000000"

var _ = Describe("BucketInfo", func() {
	It("re-encodes a legacy bucket info as the Tentacle dencoder does", func() {
		cs, err := loadGoldens("RGWBucketInfo")
		Expect(err).NotTo(HaveOccurred())
		var bin []byte
		for _, c := range cs {
			if c.Archive == "0.61.4-60-g24c59be" && c.Name == "25f5ff183bba6da708243a0bbbe40a2e" {
				bin = c.Bin
			}
		}
		Expect(bin).NotTo(BeNil())
		want, err := hex.DecodeString(tentacleBucketInfo)
		Expect(err).NotTo(HaveOccurred())
		info := decodeWhole(bin, meta.DecodeBucketInfo)
		Expect(encodeWith(denc.Tentacle, info.Encode)).To(Equal(want))
	})

	// v24 with an account owner, object lock, website and sync policy, none of
	// which the corpus holds.
	full := func() meta.BucketInfo {
		b := meta.NewBucketInfo()
		b.Bucket = meta.BucketID{Name: "b", Marker: "m", ID: "id"}
		b.Owner = meta.AccountOwner("RGW12345678901234567")
		b.Flags = meta.BucketObjLockEnabled | meta.BucketVersioned
		b.Website = rawStruct(2, 1, "website")
		b.ObjLock = rawStruct(1, 1, "lock")
		b.SyncPolicy = rawStruct(1, 1, "\x01\x00\x00\x00policy")
		b.SwiftVersioning = true
		b.SwiftVerLocation = "archive"
		b.MDSearchConfig = map[string]uint32{"x-amz-meta-a": 1}
		b.Layout.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, b.Layout.Current)}
		return b
	}
	It("carries website, object lock and sync policy encodings through unchanged", func() {
		b := full()
		enc := encodeWith(denc.Squid, b.Encode)
		Expect(decodeWhole(enc, meta.DecodeBucketInfo)).To(Equal(b))
	})
	It("writes an empty sync policy as absent, as empty_sync_policy() decides", func() {
		b := full()
		b.SyncPolicy = rawStruct(1, 1, "\x00\x00\x00\x00")
		got := decodeWhole(encodeWith(denc.Squid, b.Encode), meta.DecodeBucketInfo)
		Expect(got.SyncPolicy).To(BeNil())
	})
	// RGWObjectLock() is enabled(true), rule_exist(false); ceph-dencoder
	// v19.2.6 and v20.2.4 both encode a default-constructed one as
	// 01 01 02000000 01 00.
	It("writes a default object lock when the flag is set without one", func() {
		b := full()
		b.ObjLock = nil
		got := decodeWhole(encodeWith(denc.Squid, b.Encode), meta.DecodeBucketInfo)
		Expect(got.ObjLock).To(Equal(meta.RawStruct{1, 1, 2, 0, 0, 0, 1, 0}))
	})
	It("drops an object lock when the flag is clear", func() {
		b := full()
		b.Flags = 0
		got := decodeWhole(encodeWith(denc.Squid, b.Encode), meta.DecodeBucketInfo)
		Expect(got.ObjLock).To(BeNil())
	})
	It("rejects a website encoding newer than RGWBucketWebsiteConf::decode accepts", func() {
		b := full()
		b.Website = rawStruct(3, 3, "website")
		Expect(decodeErr(encodeWith(denc.Squid, b.Encode), meta.DecodeBucketInfo)).To(MatchError(denc.ErrIncompatible))
	})
	It("refuses to marshal JSON for a website or sync policy it holds opaque", func() {
		b := full()
		_, err := b.MarshalJSON()
		Expect(err).To(MatchError(meta.ErrOpaqueJSON))
		b.Website = nil
		_, err = b.MarshalJSON()
		Expect(err).To(MatchError(meta.ErrOpaqueJSON))
		b.SyncPolicy = nil
		Expect(mustMarshal(b)).To(ContainSubstring(`"owner":"RGW12345678901234567"`))
	})

	// The corpus starts at version 4; these are built from the C++ decode body.
	It("reads version 1, which held only the bucket, with the constructor defaults", func() {
		e := denc.NewEncoder()
		legacyHeader(e, 1)
		meta.BucketID{Name: "b", ID: "1"}.Encode(e, denc.Squid)
		want := meta.NewBucketInfo()
		want.Bucket = meta.BucketID{Name: "b", ID: "1"}
		want.Layout.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, want.Layout.Current)}
		Expect(decodeWhole(e.Bytes(), meta.DecodeBucketInfo)).To(Equal(want))
	})
})

// rawStruct builds an opaque struct encoding with the given header and body.
func rawStruct(v, c uint8, body string) meta.RawStruct {
	e := denc.NewEncoder()
	beginEnd(e, v, c, func() { e.Raw([]byte(body)) })
	return meta.RawStruct(e.Bytes())
}

var _ = Describe("BucketEntryPoint legacy decoding", func() {
	It("reads version 7 and below as an embedded bucket info", func() {
		inner := meta.NewBucketInfo()
		inner.Bucket = meta.BucketID{Name: "b", ID: "1"}
		inner.Owner = meta.UserOwner(meta.UserID{ID: "alice"})
		inner.Flags = 1
		inner.Layout.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, inner.Layout.Current)}
		e := denc.NewEncoder()
		beginEnd(e, 7, 4, func() {
			inner.Bucket.Encode(e, denc.Squid)
			e.String("alice")
			e.U32(1)
			e.String("") // zonegroup, v5
			e.U64(0)     // creation time, v6
			e.String("") // placement rule, v7
		})
		got := decodeWhole(e.Bytes(), meta.DecodeBucketEntryPoint)
		want := meta.NewBucketEntryPoint()
		want.HasBucketInfo = true
		want.OldBucketInfo = &inner
		Expect(got).To(Equal(want))
		Expect(got.Owner).To(Equal(meta.UserOwner(meta.UserID{})))
		Expect(mustMarshal(got)).To(ContainSubstring(`"old_bucket_info":{"bucket"`))

		// RGWBucketEntryPoint::encode writes the defaults the embedded form
		// left: an empty bucket and the empty user.
		reenc := denc.NewEncoder()
		beginEnd(reenc, 10, 8, func() {
			beginEnd(reenc, 10, 10, func() {
				for range 4 {
					reenc.String("")
				}
				reenc.Bool(false)
			})
			reenc.String("")
			reenc.Bool(false)
			reenc.U64(0)
			beginEnd(reenc, 2, 1, func() {
				for range 3 {
					reenc.String("")
				}
			})
			reenc.Time(time.Time{})
		})
		Expect(encodeWith(denc.Squid, got.Encode)).To(Equal(reenc.Bytes()))
	})
	It("reads version 8, whose owner is a bare user id and whose time is in seconds", func() {
		e := denc.NewEncoder()
		b := meta.BucketID{Name: "b", ID: "1"}
		beginEnd(e, 8, 8, func() {
			b.Encode(e, denc.Squid)
			e.String("alice")
			e.Bool(true)
			e.U64(1_600_000_000)
		})
		got := decodeWhole(e.Bytes(), meta.DecodeBucketEntryPoint)
		Expect(got).To(Equal(meta.BucketEntryPoint{
			Bucket:       b,
			Owner:        meta.UserOwner(meta.UserID{ID: "alice"}),
			CreationTime: meta.Time{Time: time.Unix(1_600_000_000, 0).UTC()},
			Linked:       true,
		}))

		ct := time.Unix(1_600_000_000, 0).UTC()
		reenc := denc.NewEncoder()
		beginEnd(reenc, 10, 8, func() {
			beginEnd(reenc, 10, 10, func() {
				reenc.String("b")
				reenc.String("")
				reenc.String("1")
				reenc.String("")
				reenc.Bool(false)
			})
			reenc.String("alice")
			reenc.Bool(true)
			reenc.U64(uint64(ct.Unix()))
			beginEnd(reenc, 2, 1, func() {
				reenc.String("")
				reenc.String("alice")
				reenc.String("")
			})
			reenc.Time(ct)
		})
		Expect(encodeWith(denc.Squid, got.Encode)).To(Equal(reenc.Bytes()))
	})
})

var _ = It("BucketEnt reads version 1, with neither compat byte nor length, count, bucket or rounded size", func() {
	e := denc.NewEncoder()
	e.U8(1)
	e.String("old-name")
	e.U64(4096)
	e.U32(1_600_000_000)
	Expect(decodeWhole(e.Bytes(), meta.DecodeBucketEnt)).To(Equal(meta.BucketEnt{
		Size:         4096,
		SizeRounded:  4096,
		CreationTime: meta.Time{Time: time.Unix(1_600_000_000, 0).UTC()},
	}))
})

// The fixtures below are built field by field from the C++ encode bodies,
// every field distinct and away from its default, so a transposed pair or a
// dropped field shows. They are the Tentacle forms: layout v3, normal layout v2.

var (
	fixtureCTime = time.Unix(1_650_000_000, 123_000).UTC()
	fixtureOwner = meta.UserID{Tenant: "otn", ID: "uid", NS: "ons"}
	fixtureID    = meta.BucketID{Tenant: "tn", Name: "bkt", Marker: "mk", ID: "bid"}
)

// fixtureBucketID writes fixtureID as rgw_bucket::encode does.
func fixtureBucketID(e *denc.Encoder) {
	beginEnd(e, 10, 10, func() {
		e.String("bkt")
		e.String("mk")
		e.String("bid")
		e.String("tn")
		e.Bool(false)
	})
}

// fixtureNormal writes bucket_index_normal_layout v2.
func fixtureNormal(e *denc.Encoder, shards uint32, hash uint8, minShards uint32) {
	beginEnd(e, 2, 1, func() {
		e.U32(shards)
		e.U8(hash)
		e.U32(minShards)
	})
}

func bucketInfoFixture() ([]byte, meta.BucketInfo) {
	website := rawStruct(2, 1, "web")
	objLock := rawStruct(1, 1, "\x00\x00")
	syncPolicy := rawStruct(1, 1, "\x01\x00\x00\x00grp")
	judge := time.Unix(1_660_000_000, 7_000).UTC()

	e := denc.NewEncoder()
	beginEnd(e, 24, 4, func() {
		fixtureBucketID(e)
		e.String("uid")
		e.U32(meta.BucketVersioned | meta.BucketObjLockEnabled)
		e.String("zg")
		e.U64(uint64(fixtureCTime.Unix()))
		e.String("pr/COLD")
		e.Bool(true) // has_instance_obj
		beginEnd(e, 3, 1, func() {
			e.I64(5) // max_size_kb
			e.I64(7) // max_objects
			e.Bool(true)
			e.I64(5000) // max_size
			e.Bool(true)
		})
		e.Bool(true) // requester_pays
		e.String("otn")
		e.Bool(true) // has_website
		e.Raw(website)
		e.Bool(true) // swift_versioning
		e.String("verloc")
		e.Time(fixtureCTime)
		e.U32(2) // mdsearch_config
		e.String("k1")
		e.U32(11)
		e.String("k2")
		e.U32(22)
		e.U8(2) // reshard_status DONE
		e.String("nbi")
		e.Raw(objLock)
		e.Bool(true) // has_sync_policy
		e.Raw(syncPolicy)
		beginEnd(e, 3, 1, func() { // rgw::BucketLayout
			e.U8(1) // InProgress
			beginEnd(e, 1, 1, func() {
				e.U64(3)
				beginEnd(e, 1, 1, func() {
					e.U8(0)
					fixtureNormal(e, 13, 1, 4)
				})
			})
			e.Bool(true) // target_index
			beginEnd(e, 1, 1, func() {
				e.U64(4)
				beginEnd(e, 1, 1, func() { e.U8(1) }) // Indexless
			})
			e.U32(2) // logs
			beginEnd(e, 1, 1, func() {
				e.U64(5)
				beginEnd(e, 1, 1, func() { e.U8(1) }) // Deleted
			})
			beginEnd(e, 1, 1, func() {
				e.U64(6)
				beginEnd(e, 1, 1, func() {
					e.U8(0)
					beginEnd(e, 1, 1, func() {
						e.U64(3)
						fixtureNormal(e, 13, 1, 4)
					})
				})
			})
			e.Time(judge)
		})
		e.String("ons")
		beginEnd(e, 0, 0, func() {
			beginEnd(e, 2, 1, func() {
				e.String("otn")
				e.String("uid")
				e.String("ons")
			})
		})
	})

	normal := meta.IndexNormalLayout{NumShards: 13, HashType: 1, MinNumShards: 4}
	target := meta.IndexLayoutGen{Gen: 4, Layout: meta.IndexLayout{Type: meta.IndexIndexless, Normal: meta.NewIndexNormalLayout()}}
	deleted := meta.LogLayoutGen{Gen: 5, Layout: meta.LogLayout{
		Type:    meta.LogDeleted,
		InIndex: meta.IndexLogLayout{Layout: meta.NewIndexNormalLayout()},
		FIFO:    meta.FIFOLogLayout{NumShards: 7},
	}}
	inIndex := meta.LogLayoutGen{Gen: 6, Layout: meta.LogLayout{
		Type:    meta.LogInIndex,
		InIndex: meta.IndexLogLayout{Gen: 3, Layout: normal},
		FIFO:    meta.FIFOLogLayout{NumShards: 7},
	}}
	return e.Bytes(), meta.BucketInfo{
		Bucket:         fixtureID,
		Owner:          meta.UserOwner(fixtureOwner),
		Flags:          meta.BucketVersioned | meta.BucketObjLockEnabled,
		Zonegroup:      "zg",
		CreationTime:   meta.Time{Time: fixtureCTime},
		PlacementRule:  meta.PlacementRule{Name: "pr", StorageClass: "COLD"},
		HasInstanceObj: true,
		Quota:          meta.Quota{MaxSize: 5000, MaxObjects: 7, Enabled: true, CheckOnRaw: true},
		Layout: meta.BucketLayout{
			Resharding:           meta.ReshardInProgress,
			Current:              meta.IndexLayoutGen{Gen: 3, Layout: meta.IndexLayout{Type: meta.IndexNormal, Normal: normal}},
			Target:               &target,
			Logs:                 []meta.LogLayoutGen{deleted, inIndex},
			JudgeReshardLockTime: meta.Time{Time: judge},
		},
		RequesterPays:       true,
		Website:             website,
		SwiftVersioning:     true,
		SwiftVerLocation:    "verloc",
		MDSearchConfig:      map[string]uint32{"k1": 11, "k2": 22},
		ReshardStatus:       meta.ReshardStatusDone,
		NewBucketInstanceID: "nbi",
		ObjLock:             objLock,
		SyncPolicy:          syncPolicy,
	}
}

func bucketEntFixture() ([]byte, meta.BucketEnt) {
	e := denc.NewEncoder()
	beginEnd(e, 7, 5, func() {
		e.String("")
		e.U64(1234) // size
		e.U32(uint32(fixtureCTime.Unix()))
		e.U64(9) // count
		fixtureBucketID(e)
		e.U64(4096) // size_rounded
		e.Time(fixtureCTime)
		e.String("pr/COLD")
	})
	return e.Bytes(), meta.BucketEnt{
		Bucket:        fixtureID,
		Size:          1234,
		SizeRounded:   4096,
		CreationTime:  meta.Time{Time: fixtureCTime},
		Count:         9,
		PlacementRule: meta.PlacementRule{Name: "pr", StorageClass: "COLD"},
	}
}

var _ = Describe("hand-built fixtures", func() {
	It("decodes BucketInfo v24 field by field and re-encodes it exactly", func() {
		b, want := bucketInfoFixture()
		Expect(decodeWhole(b, meta.DecodeBucketInfo)).To(Equal(want))
		Expect(encodeWith(denc.Tentacle, want.Encode)).To(Equal(b))
	})
	It("decodes BucketEnt v7 field by field and re-encodes it exactly", func() {
		b, want := bucketEntFixture()
		Expect(decodeWhole(b, meta.DecodeBucketEnt)).To(Equal(want))
		Expect(encodeWith(denc.Squid, want.Encode)).To(Equal(b))
	})
})

var _ = DescribeTable("BucketID.Key is rgw_bucket::get_key at its default delimiters",
	func(b meta.BucketID, want string) {
		Expect(b.Key()).To(Equal(want))
	},
	Entry("a name alone", meta.BucketID{Name: "b"}, "b"),
	Entry("a tenant and an id", meta.BucketID{Tenant: "t", Name: "b", ID: "id"}, "t/b:id"),
	Entry("an id without a tenant", meta.BucketID{Name: "b", ID: "id"}, "b:id"),
	Entry("a marker, which get_key leaves out", meta.BucketID{Name: "b", Marker: "m"}, "b"),
)
