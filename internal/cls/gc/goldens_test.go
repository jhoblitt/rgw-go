package gc_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// squid checks re-encodings against goldens from the v19 dencoder image. No
// gc type prints in radosgw-admin output, so none is compared as JSON.
//
// No Tentacle goldens are committed: src/cls/rgw_gc and the cls_rgw gc structs
// are identical at v19.2.6 and v20.2.4, and running
// IMAGE=quay.io/ceph/ceph:v20.2.4 hack/goldens/gen.sh over the gc types
// regenerates every gc golden byte for byte.
var squid = goldentest.Options{Release: denc.Squid, SkipJSON: true}

// rawCase is a corpus object of a type ceph-dencoder does not register.
type rawCase struct {
	id  string
	bin []byte
}

// loadRaw reads testdata/corpus/<typ>/<archive>/<object>, copied verbatim
// from the ceph-object-corpus. No dencoder re-encoding exists for these
// types, so a (1,1) struct's corpus bytes are themselves the expected encoding.
func loadRaw(typ string) []rawCase {
	GinkgoHelper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", typ, "*", "*"))
	Expect(err).NotTo(HaveOccurred())
	Expect(paths).NotTo(BeEmpty(), "no corpus objects for %s", typ)
	cs := make([]rawCase, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p) //nolint:gosec // paths come from the test's own testdata
		Expect(err).NotTo(HaveOccurred())
		cs = append(cs, rawCase{id: p, bin: b})
	}
	return cs
}

func decodeWhole[T any](b []byte, decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

func decodeErr[T any](b []byte, decode func(*denc.Decoder) T) error {
	d := denc.NewDecoder(b)
	decode(d)
	return d.Err()
}

func encodeWith(r denc.Release, enc func(*denc.Encoder, denc.Release)) []byte {
	e := denc.NewEncoder()
	enc(e, r)
	return e.Bytes()
}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("cls_rgw_gc_queue_init_op", func() {
		goldentest.RoundTrip(dir, "cls_rgw_gc_queue_init_op", squid, gc.DecodeQueueInitOp,
			func(e *denc.Encoder, v gc.QueueInitOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_rgw_gc_urgent_data, whose map is a std::unordered_map", func() {
		o := squid
		o.Nondeterministic = true
		goldentest.RoundTrip(dir, "cls_rgw_gc_urgent_data", o, gc.DecodeUrgentData,
			func(e *denc.Encoder, v gc.UrgentData, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_rgw_gc_list_op", func() {
		goldentest.RoundTrip(dir, "cls_rgw_gc_list_op", squid, gc.DecodeListOp,
			func(e *denc.Encoder, v gc.ListOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_rgw_gc_list_ret", func() {
		goldentest.RoundTrip(dir, "cls_rgw_gc_list_ret", squid, gc.DecodeListRet,
			func(e *denc.Encoder, v gc.ListRet, r denc.Release) { v.Encode(e, r) })
	})
})

var _ = Describe("corpus objects ceph-dencoder does not register", func() {
	It("cls_rgw_gc_queue_remove_entries_op round-trips byte for byte", func() {
		for _, c := range loadRaw("cls_rgw_gc_queue_remove_entries_op") {
			v := decodeWhole(c.bin, gc.DecodeQueueRemoveEntriesOp)
			Expect(v.NumEntries).To(BeNumerically(">", 0), c.id)
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				Expect(encodeWith(r, v.Encode)).To(Equal(c.bin), c.id)
			}
		}
	})
	It("cls_rgw_gc_queue_defer_entry_op round-trips byte for byte", func() {
		for _, c := range loadRaw("cls_rgw_gc_queue_defer_entry_op") {
			v := decodeWhole(c.bin, gc.DecodeQueueDeferEntryOp)
			Expect(v.ExpirationSecs).To(BeEquivalentTo(10), c.id)
			Expect(v.Info.Chain).To(HaveLen(2), c.id)
			for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
				Expect(encodeWith(r, v.Encode)).To(Equal(c.bin), c.id)
			}
		}
	})
})

// fixtureTime has a nonzero nanosecond part, which no gc corpus object's
// defer-op or urgent-data time carries.
var fixtureTime = time.Unix(1_700_000_000, 123_456_789).UTC()

// putTime writes a real_time field by field: u32 seconds, u32 nanoseconds.
func putTime(e *denc.Encoder, t time.Time) {
	e.U32(uint32(t.Unix()))       //nolint:gosec // fixture times fit a u32
	e.U32(uint32(t.Nanosecond())) //nolint:gosec // within [0, 1e9)
}

// struct1 wraps body in a hand-written ENCODE_START(v, compat) header.
func struct1(e *denc.Encoder, v, compat uint8, body func(*denc.Encoder)) {
	inner := denc.NewEncoder()
	body(inner)
	e.U8(v)
	e.U8(compat)
	e.U32(uint32(inner.Len())) //nolint:gosec // fixture bodies are small
	e.Raw(inner.Bytes())
}

// urgentDataFixture holds two urgent-data map entries, a field every corpus
// object leaves empty. The keys are written in sorted order, the order Encode
// emits; C++ writes them in hash order.
func urgentDataFixture() ([]byte, gc.UrgentData) {
	e := denc.NewEncoder()
	struct1(e, 1, 1, func(e *denc.Encoder) {
		e.U32(2)
		e.String("tag-a")
		putTime(e, fixtureTime)
		e.String("tag-b")
		putTime(e, fixtureTime.Add(time.Hour))
		e.U32(50) // num_urgent_data_entries
		e.U32(2)  // num_head_urgent_entries
		e.U32(0)  // num_xattr_urgent_entries
	})
	return e.Bytes(), gc.UrgentData{
		UrgentDataMap: map[string]time.Time{
			"tag-a": fixtureTime,
			"tag-b": fixtureTime.Add(time.Hour),
		},
		NumUrgentDataEntries: 50,
		NumHeadUrgentEntries: 2,
	}
}

// deferFixture is a cls_rgw_gc_queue_defer_entry_op whose info carries a
// nonzero time and an object instance, both at their defaults in the corpus.
func deferFixture() ([]byte, gc.QueueDeferEntryOp) {
	e := denc.NewEncoder()
	struct1(e, 1, 1, func(e *denc.Encoder) {
		e.U32(3600)                              // expiration_secs
		struct1(e, 1, 1, func(e *denc.Encoder) { // cls_rgw_gc_obj_info
			e.String("tag\x00")
			struct1(e, 1, 1, func(e *denc.Encoder) { // cls_rgw_obj_chain
				e.U32(1)
				struct1(e, 2, 1, func(e *denc.Encoder) { // cls_rgw_obj
					e.String("default.rgw.buckets.data")
					e.String("obj")
					e.String("loc")
					struct1(e, 1, 1, func(e *denc.Encoder) { // cls_rgw_obj_key
						e.String("obj")
						e.String("inst")
					})
				})
			})
			putTime(e, fixtureTime)
		})
	})
	return e.Bytes(), gc.QueueDeferEntryOp{
		ExpirationSecs: 3600,
		Info: rgw.GCObjInfo{
			Tag: "tag\x00",
			Chain: []rgw.GCObj{{
				Pool: "default.rgw.buckets.data",
				Key:  rgw.ObjKey{Name: "obj", Instance: "inst"},
				Loc:  "loc",
			}},
			Time: fixtureTime,
		},
	}
}

var _ = Describe("hand-built fixtures", func() {
	It("decodes urgent data with map entries field by field and re-encodes it exactly", func() {
		b, want := urgentDataFixture()
		Expect(decodeWhole(b, gc.DecodeUrgentData)).To(Equal(want))
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			Expect(encodeWith(r, want.Encode)).To(Equal(b))
		}
	})
	It("keeps the first value for a key the urgent-data map repeats, as emplace does", func() {
		e := denc.NewEncoder()
		struct1(e, 1, 1, func(e *denc.Encoder) {
			e.U32(2)
			e.String("dup")
			putTime(e, fixtureTime)
			e.String("dup")
			putTime(e, fixtureTime.Add(time.Hour))
			e.U32(0)
			e.U32(0)
			e.U32(0)
		})
		got := decodeWhole(e.Bytes(), gc.DecodeUrgentData)
		Expect(got.UrgentDataMap).To(Equal(map[string]time.Time{"dup": fixtureTime}))
	})
	It("decodes a defer op with a timed, versioned info field by field and re-encodes it exactly", func() {
		b, want := deferFixture()
		Expect(decodeWhole(b, gc.DecodeQueueDeferEntryOp)).To(Equal(want))
		Expect(encodeWith(denc.Squid, want.Encode)).To(Equal(b))
	})
	It("defaults expired_only to true when a version 1 list op omits it", func() {
		e := denc.NewEncoder()
		struct1(e, 1, 1, func(e *denc.Encoder) {
			e.String("m")
			e.U32(7)
		})
		Expect(decodeWhole(e.Bytes(), gc.DecodeListOp)).To(Equal(gc.ListOp{Marker: "m", Max: 7, ExpiredOnly: true}))
	})
	It("decodes a version 1 list reply, which has no next marker", func() {
		e := denc.NewEncoder()
		struct1(e, 1, 1, func(e *denc.Encoder) {
			e.U32(0)
			e.Bool(true)
		})
		Expect(decodeWhole(e.Bytes(), gc.DecodeListRet)).To(Equal(gc.ListRet{Truncated: true}))
	})
	It("rejects a list reply whose compat version is newer than 2", func() {
		e := denc.NewEncoder()
		struct1(e, 3, 3, func(e *denc.Encoder) {})
		Expect(decodeErr(e.Bytes(), gc.DecodeListRet)).To(MatchError(denc.ErrIncompatible))
	})
})
