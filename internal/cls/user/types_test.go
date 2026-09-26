package user_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

func decodeWhole[T any](b []byte, decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

func encodeSquid(enc func(*denc.Encoder, denc.Release)) []byte {
	e := denc.NewEncoder()
	enc(e, denc.Squid)
	return e.Bytes()
}

// The corpus holds cls_user_bucket only at (7,3), so every other version is
// hand-built field by field.
var _ = Describe("Bucket", func() {
	It("writes (9,8) with the placement id, and no pools, when it has one", func() {
		b := user.Bucket{Name: "n", Marker: "m", BucketID: "id", PlacementID: "p", DataPool: "ignored"}
		e := denc.NewEncoder()
		f := e.BeginStruct(9, 8)
		e.String("n")
		e.String("m")
		e.String("id")
		e.String("p")
		e.EndStruct(f)
		Expect(encodeSquid(b.Encode)).To(Equal(e.Bytes()))
		b.DataPool = ""
		Expect(decodeWhole(e.Bytes(), user.DecodeBucket)).To(Equal(b))
	})
	It("writes (7,3) with the explicit pools when it has no placement id", func() {
		b := user.Bucket{Name: "n", Marker: "m", BucketID: "id", DataPool: "d", IndexPool: "i", DataExtraPool: "x"}
		e := denc.NewEncoder()
		f := e.BeginStruct(7, 3)
		e.String("n")
		e.String("d")
		e.String("m")
		e.String("id")
		e.String("i")
		e.String("x")
		e.EndStruct(f)
		Expect(encodeSquid(b.Encode)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeBucket)).To(Equal(b))
	})
	It("reads version 8 with an empty placement id followed by the pools", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(8, 8)
		e.String("n")
		e.String("m")
		e.String("id")
		e.String("")
		e.String("d")
		e.String("i")
		e.String("x")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), user.DecodeBucket)).To(Equal(user.Bucket{
			Name: "n", Marker: "m", BucketID: "id", DataPool: "d", IndexPool: "i", DataExtraPool: "x",
		}))
	})
	It("reads version 8 with a placement id and no pools", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(8, 8)
		e.String("n")
		e.String("m")
		e.String("id")
		e.String("p")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), user.DecodeBucket)).To(Equal(user.Bucket{
			Name: "n", Marker: "m", BucketID: "id", PlacementID: "p",
		}))
	})
	It("reads version 4, which had no index or extra pool, taking the data pool as the index pool", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(4, 3)
		e.String("n")
		e.String("d")
		e.String("m")
		e.String("id")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), user.DecodeBucket)).To(Equal(user.Bucket{
			Name: "n", Marker: "m", BucketID: "id", DataPool: "d", IndexPool: "d",
		}))
	})
	It("reads version 6, which had an index pool but no extra pool", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(6, 3)
		e.String("n")
		e.String("d")
		e.String("m")
		e.String("id")
		e.String("i")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), user.DecodeBucket)).To(Equal(user.Bucket{
			Name: "n", Marker: "m", BucketID: "id", DataPool: "d", IndexPool: "i",
		}))
	})
	It("reads version 3's numeric bucket id as snprintf(\"%llu\", (long long)id) into 16 bytes", func() {
		bucket := func(id uint64) user.Bucket {
			e := denc.NewEncoder()
			f := e.BeginStruct(3, 3)
			e.String("n")
			e.String("d")
			e.String("m")
			e.U64(id)
			e.EndStruct(f)
			return decodeWhole(e.Bytes(), user.DecodeBucket)
		}
		Expect(bucket(4156).BucketID).To(Equal("4156"))
		// A signed long long printed with %llu is its unsigned value; the
		// 16-byte buffer keeps 15 digits.
		Expect(bucket(1<<63 + 5).BucketID).To(Equal("922337203685477"))
		Expect(bucket(123456789012345).BucketID).To(Equal("123456789012345"))
	})
	It("reads version 2, which had no compat byte or length", func() {
		b := []byte{2}
		e := denc.NewEncoder()
		e.String("n")
		e.String("d")
		e.String("m")
		e.U64(7)
		b = append(b, e.Bytes()...)
		Expect(decodeWhole(b, user.DecodeBucket)).To(Equal(user.Bucket{
			Name: "n", Marker: "m", BucketID: "7", DataPool: "d", IndexPool: "d",
		}))
	})
	It("reads version 1, which had only the name and data pool", func() {
		b := []byte{1}
		e := denc.NewEncoder()
		e.String("n")
		e.String("d")
		b = append(b, e.Bytes()...)
		Expect(decodeWhole(b, user.DecodeBucket)).To(Equal(user.Bucket{Name: "n", DataPool: "d", IndexPool: "d"}))
	})
	It("rejects a compat version above 8", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(10, 9)
		e.EndStruct(f)
		d := denc.NewDecoder(e.Bytes())
		user.DecodeBucket(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})

var created = time.Date(2026, 9, 26, 1, 2, 3, 456789000, time.UTC)

var _ = Describe("BucketEntry", func() {
	bucketBytes := func() []byte {
		return encodeSquid(user.Bucket{Name: "n", Marker: "m", BucketID: "id"}.Encode)
	}

	It("writes creation_time twice: as whole u32 seconds, then as real_time", func() {
		be := user.BucketEntry{
			Bucket: user.Bucket{Name: "n", Marker: "m", BucketID: "id"},
			Size:   1, SizeRounded: 4096, Count: 2, UserStatsSync: true, CreationTime: created,
		}
		e := denc.NewEncoder()
		f := e.BeginStruct(9, 5)
		e.String("")
		e.U64(1)
		e.U32(uint32(created.Unix()))
		e.U64(2)
		e.Raw(bucketBytes())
		e.U64(4096)
		e.Bool(true)
		e.Time(created)
		e.EndStruct(f)
		Expect(encodeSquid(be.Encode)).To(Equal(e.Bytes()))
		Expect(decodeWhole(e.Bytes(), user.DecodeBucketEntry)).To(Equal(be))
	})
	It("reads version 8, discarding its placement rule", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(8, 5)
		e.String("")
		e.U64(1)
		e.U32(99) // ignored from version 7
		e.U64(2)
		e.Raw(bucketBytes())
		e.U64(3)
		e.Bool(false)
		e.Time(created)
		e.String("default-placement")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), user.DecodeBucketEntry)).To(Equal(user.BucketEntry{
			Bucket: user.Bucket{Name: "n", Marker: "m", BucketID: "id"},
			Size:   1, SizeRounded: 3, Count: 2, CreationTime: created,
		}))
	})
	It("reads version 5, taking the creation time from the u32 seconds", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(5, 5)
		e.String("ignored")
		e.U64(1)
		e.U32(1700000000)
		e.U64(2)
		e.Raw(bucketBytes())
		e.U64(3)
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), user.DecodeBucketEntry)).To(Equal(user.BucketEntry{
			Bucket: user.Bucket{Name: "n", Marker: "m", BucketID: "id"},
			Size:   1, SizeRounded: 3, Count: 2, CreationTime: time.Unix(1700000000, 0).UTC(),
		}))
	})
	It("reads version 3, which had no rounded size, taking the size as the rounded size", func() {
		b := []byte{3}
		e := denc.NewEncoder()
		e.String("")
		e.U64(10)
		e.U32(0)
		e.U64(2)
		e.Raw(bucketBytes())
		b = append(b, e.Bytes()...)
		Expect(decodeWhole(b, user.DecodeBucketEntry)).To(Equal(user.BucketEntry{
			Bucket: user.Bucket{Name: "n", Marker: "m", BucketID: "id"},
			Size:   10, SizeRounded: 10, Count: 2,
		}))
	})
	It("reads version 1, which had only the size and time", func() {
		b := []byte{1}
		e := denc.NewEncoder()
		e.String("old")
		e.U64(10)
		e.U32(5)
		b = append(b, e.Bytes()...)
		Expect(decodeWhole(b, user.DecodeBucketEntry)).To(Equal(user.BucketEntry{
			Size: 10, SizeRounded: 10, CreationTime: time.Unix(5, 0).UTC(),
		}))
	})
	It("marshals as cls_user_bucket_entry::dump, with small times as raw seconds", func() {
		be := user.BucketEntry{
			Bucket: user.Bucket{Name: "n", Marker: "m", BucketID: "id", PlacementID: "p"},
			Size:   1, SizeRounded: 2, Count: 3, UserStatsSync: true, CreationTime: created,
		}
		b, err := json.Marshal(be)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"bucket":{"name":"n","marker":"m","bucket_id":"id"},"size":1,"size_rounded":2,` +
			`"creation_time":"2026-09-26T01:02:03.456789Z","count":3,"user_stats_sync":true}`))
		be.CreationTime = time.Unix(3, 1000).UTC()
		b, err = json.Marshal(be)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring(`"creation_time":"3.000001"`))
	})
})

var _ = Describe("Header", func() {
	It("marshals the stats as signed integers, as dump_int writes them", func() {
		h := user.Header{Stats: user.Stats{TotalEntries: 1<<64 - 1, TotalBytes: 2, TotalBytesRounded: 3}}
		b, err := json.Marshal(h)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"stats":{"total_entries":-1,"total_bytes":2,"total_bytes_rounded":3},` +
			`"last_stats_sync":"0.000000","last_stats_update":"0.000000"}`))
	})
})
