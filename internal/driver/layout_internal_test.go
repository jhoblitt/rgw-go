package driver

import (
	"bytes"
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// writeSizes counts the bytes written to it and keeps the sizes of the
// first and of the largest write.
type writeSizes struct{ n, first, largest int }

func (w *writeSizes) Write(p []byte) (int, error) {
	if w.n == 0 {
		w.first = len(p)
	}
	w.n += len(p)
	w.largest = max(w.largest, len(p))
	return len(p), nil
}

var _ = Describe("the layout's placement and stripe bound", func() {
	var (
		s  *Store
		st *op.ObjectState
	)
	render := func(ctx context.Context, limit uint64) (string, error) {
		GinkgoHelper()
		d, err := s.layout(ctx, st, limit)
		if err != nil {
			return "", err
		}
		var out bytes.Buffer
		_, err = d.WriteTo(&out)
		Expect(err).NotTo(HaveOccurred())
		Expect(int64(out.Len())).To(Equal(d.Len()))
		return out.String(), nil
	}
	// fine lays st out as size bytes in one-byte stripes after an empty
	// head, so it has size stripes.
	fine := func(size uint64) {
		m := st.Manifest
		m.ObjSize, m.HeadSize, m.MaxHeadSize = size, 0, 0
		m.Rules = map[uint64]meta.ManifestRule{0: {StripeMaxSize: 1}}
		st.Size = size
	}
	BeforeEach(func() {
		rule := meta.PlacementRule{Name: "default-placement"}
		s = &Store{release: denc.Squid, zone: &zoneConfig{
			Params: meta.ZoneParams{PlacementPools: map[string]meta.ZonePlacementInfo{
				"default-placement": {StorageClasses: meta.ZoneStorageClasses{meta.StorageClassStandard: {DataPool: new(meta.ParsePool("data"))}}},
				"fast":              {StorageClasses: meta.ZoneStorageClasses{meta.StorageClassStandard: {DataPool: new(meta.ParsePool("fast.data"))}}},
			}},
			ZoneGroup: meta.ZoneGroup{DefaultPlacement: rule},
		}}
		rec := &op.BucketRecord{Info: meta.BucketInfo{Bucket: meta.BucketID{Name: "b", Marker: "m", ID: "m"}, PlacementRule: rule}}
		// A 4 MiB head and 6 MiB in two shadow stripes, the last cut short.
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 10<<20, 4<<20, 4<<20
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}
		m.HeadPlacementRule = rule
		m.TailPlacement = meta.BucketPlacement{Bucket: rec.Info.Bucket, PlacementRule: rule}
		st = &op.ObjectState{Bucket: rec, Key: m.Obj.Key, Exists: true, Size: m.ObjSize, Manifest: &m}
	})

	It("places the head by the bucket's placement rule and the tail stripes by the manifest's tail rule", func(ctx SpecContext) {
		fast := meta.PlacementRule{Name: "fast"}
		st.Bucket.Info.PlacementRule, st.Manifest.HeadPlacementRule = fast, fast
		doc, err := render(ctx, meta.MaxStripes)
		Expect(err).NotTo(HaveOccurred())
		Expect(doc).To(HavePrefix(`{"head":{"pool":"fast.data","oid":"m_k","loc":""},"manifest":`))
		Expect(doc).To(HaveSuffix(`"data_location":[{"ofs":0,"loc":{"pool":"fast.data","oid":"m_k","loc":""},"loc_ofs":0,"loc_size":4194304},` +
			`{"ofs":4194304,"loc":{"pool":"data","oid":"m__shadow_1","loc":""},"loc_ofs":0,"loc_size":4194304},` +
			`{"ofs":8388608,"loc":{"pool":"data","oid":"m__shadow_2","loc":""},"loc_ofs":0,"loc_size":2097152}]}`))
	})

	It("lays out an empty object as no stripes, whatever its head holds, as obj_begin and obj_end meet at 0", func(ctx SpecContext) {
		st.Manifest.ObjSize, st.Size = 0, 0
		doc, err := render(ctx, meta.MaxStripes)
		Expect(err).NotTo(HaveOccurred())
		Expect(doc).To(HaveSuffix(`"data_location":[]}`))
	})

	It("lays out an object of as many stripes as the bound", func(ctx SpecContext) {
		doc, err := render(ctx, 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(doc).To(ContainSubstring(`"ofs":8388608,`))
	})

	It("refuses at once a tail that needs more stripes than the bound", func(ctx SpecContext) {
		_, err := render(ctx, 1)
		Expect(err).To(MatchError(meta.ErrTooManyStripes))
		Expect(err).To(MatchError(ContainSubstring("at least 2, more than 1")))
	})

	It("fails a walk past the bound that the tail alone does not reach", func(ctx SpecContext) {
		_, err := render(ctx, 2)
		Expect(err).To(MatchError(meta.ErrTooManyStripes))
		Expect(err).To(MatchError(ContainSubstring("more than 2 before offset 8388608")))
	})

	It("writes a document of as many stripes as its bound in pieces no larger than its head and manifest", func(ctx SpecContext) {
		fine(4096)
		d, err := s.layout(ctx, st, 4096)
		Expect(err).NotTo(HaveOccurred())
		var w writeSizes
		n, err := d.WriteTo(&w)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(d.Len()))
		Expect(int64(w.n)).To(Equal(d.Len()))
		Expect(d.Len()).To(BeNumerically(">", 90*4096), "over 90 bytes for each of 4096 stripes")
		Expect(w.largest).To(Equal(w.first), "no write is larger than the head and manifest, the first")
		Expect(w.first).To(BeNumerically("<", 4096))
	})

	It("refuses an object of one stripe more than meta.MaxStripes", func(ctx SpecContext) {
		fine(meta.MaxStripes + 1)
		doc, err := s.ObjectLayout(ctx, st)
		Expect(err).To(MatchError(meta.ErrTooManyStripes))
		Expect(err).To(MatchError(op.ErrUnknown))
		Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("more than %d", meta.MaxStripes))))
		Expect(fmt.Sprintf("%T", doc)).To(Equal("<nil>"), "a nil interface, not a typed nil")
	})

	It("stops measuring with the context's error once the context ends", func(ctx SpecContext) {
		fine(layoutCheckStripes)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		doc, err := s.ObjectLayout(cctx, st)
		Expect(err).To(MatchError(context.Canceled))
		Expect(fmt.Sprintf("%T", doc)).To(Equal("<nil>"), "a nil interface, not a typed nil")
	})

	It("stops writing with the context's error once the context ends", func(ctx SpecContext) {
		fine(layoutCheckStripes)
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		d, err := s.layout(cctx, st, meta.MaxStripes)
		Expect(err).NotTo(HaveOccurred())
		cancel()
		var w writeSizes
		n, err := d.WriteTo(&w)
		Expect(err).To(MatchError(context.Canceled))
		Expect(n).To(BeNumerically("<", d.Len()), "WriteTo stopped before the end")
	})
})
