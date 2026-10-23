package memstore_test

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

var _ = Describe("the otp metadata section", func() {
	const key = "user:alice"
	var (
		store *memstore.Store
		clk   *clock
	)
	BeforeEach(func() {
		otptest.RedactDevices()
		store, clk = newStore()
	})
	device := func(id string, window uint32) otp.OTPInfo {
		return otp.OTPInfo{
			Type: otp.TypeTOTP, ID: id, Seed: "3132333435363738393031323334353637383930", SeedType: otp.SeedHex,
			StepSize: 30, Window: window,
		}
	}
	document := func(devs ...otp.OTPInfo) json.RawMessage {
		GinkgoHelper()
		b, err := json.Marshal(op.OTPDocument{Devices: devs})
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	put := func(ctx SpecContext, e op.MetadataEntry) error {
		return store.Put(ctx, "otp", key, e, op.PutMetadataOptions{})
	}
	get := func(ctx SpecContext) op.MetadataEntry {
		GinkgoHelper()
		e, err := store.Get(ctx, "otp", key)
		Expect(err).NotTo(HaveOccurred())
		return e
	}

	It("is one of the sections the store serves", func() {
		Expect(store.Sections()).To(ConsistOf("bucket", "bucket.instance", "otp", "user"))
	})

	It("stores a document's devices in the header's order, the last of a repeated id winning, and answers them", func(ctx SpecContext) {
		Expect(put(ctx, op.MetadataEntry{Data: document(device("b", 2), device("a", 2), device("b", 5)), Mtime: start})).To(Succeed())
		e := get(ctx)
		want := op.OTPDocument{Devices: []otp.OTPInfo{device("a", 2), device("b", 5)}}
		Expect(docOf[op.OTPDocument](e)).To(Equal(want))
		got, err := op.DecodeOTPDocument(e.Data)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want), "the JSON is the document")
		Expect(e.Mtime).To(BeTemporally("==", start))
		Expect(e.Key).To(Equal(key))
	})

	DescribeTable("writes the version the RADOS driver's put writes",
		func(ctx SpecContext, stored *meta.ObjVersion, ver meta.ObjVersion, want func(meta.ObjVersion) bool) {
			if stored != nil {
				Expect(put(ctx, op.MetadataEntry{Data: document(device("a", 2)), Version: *stored})).To(Succeed())
			}
			Expect(put(ctx, op.MetadataEntry{Data: document(device("a", 2)), Version: ver})).To(Succeed())
			v := get(ctx).Version
			Expect(want(v)).To(BeTrue(), "stored %+v", v)
		},
		Entry("a fresh tag at version 1 for a ver without a tag, over a stored entry too",
			&meta.ObjVersion{Ver: 7, Tag: "old"}, meta.ObjVersion{Ver: 9}, func(v meta.ObjVersion) bool {
				return v.Ver == 1 && len(v.Tag) == 24 && v.Tag[0] == '_'
			}),
		Entry("the document's ver when it has a tag", &meta.ObjVersion{Ver: 7, Tag: "old"}, meta.ObjVersion{Ver: 3, Tag: "doc"},
			func(v meta.ObjVersion) bool { return v == meta.ObjVersion{Ver: 3, Tag: "doc"} }),
		Entry("an increment of the stored version for a tag at version 0", &meta.ObjVersion{Ver: 7, Tag: "old"}, meta.ObjVersion{Tag: "doc"},
			func(v meta.ObjVersion) bool { return v == meta.ObjVersion{Ver: 8, Tag: "old"} }),
		Entry("version 2 under a fresh tag for a tag at version 0 over nothing, as cls_version_inc initializes then increments",
			nil, meta.ObjVersion{Tag: "doc"}, func(v meta.ObjVersion) bool {
				return v.Ver == 2 && v.Tag != "" && v.Tag != "doc"
			}),
	)

	It("keeps the stored mtime for a document without one, as the OSD keeps it for a write of mtime zero", func(ctx SpecContext) {
		Expect(put(ctx, op.MetadataEntry{Data: document(device("a", 2)), Mtime: start})).To(Succeed())
		clk.t = start.Add(3600e9)
		Expect(put(ctx, op.MetadataEntry{Data: document(device("a", 3))})).To(Succeed())
		Expect(get(ctx).Mtime).To(BeTemporally("==", start))
	})

	DescribeTable("refuses with InvalidArgument what the RADOS driver refuses, storing nothing",
		func(ctx SpecContext, k, data string) {
			Expect(store.Put(ctx, "otp", k, op.MetadataEntry{Data: json.RawMessage(data)}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
			keys, _, _, err := store.List(ctx, "otp", "", 10)
			Expect(err).NotTo(HaveOccurred())
			Expect(keys).To(BeEmpty())
		},
		Entry("a step_size of 0", key, `{"devices":[{"type":2,"id":"a","seed":"3132","seed_type":"hex","step_size":0}]}`),
		Entry("a seed of 1025 bytes", key, `{"devices":[{"type":2,"id":"a","seed":"`+strings.Repeat("a", 1025)+`","seed_type":"hex","step_size":30}]}`),
		Entry("a seed given an object", key, `{"devices":[{"type":2,"id":"a","seed":{},"seed_type":"hex","step_size":30}]}`),
		Entry("a key holding a NUL byte", "user:al\x00ice", `{"devices":[]}`),
	)

	It("lists its entries' keys in order across pages, and removes an entry", func(ctx SpecContext) {
		for i := range 5 {
			Expect(store.Put(ctx, "otp", fmt.Sprintf("user:u%d", i), op.MetadataEntry{Data: document(device("a", 2))}, op.PutMetadataOptions{})).To(Succeed())
		}
		keys, next, more, err := store.List(ctx, "otp", "", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{keys, next, more}).To(Equal([]any{[]string{"user:u0", "user:u1"}, "user:u1", true}))
		keys, _, more, err = store.List(ctx, "otp", next, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{keys, more}).To(Equal([]any{[]string{"user:u2", "user:u3", "user:u4"}, false}))
		Expect(store.Remove(ctx, "otp", "user:u3")).To(Succeed())
		_, err = store.Get(ctx, "otp", "user:u3")
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(store.Remove(ctx, "otp", "user:u3")).To(MatchError(op.ErrNoSuchKey))
	})
})
