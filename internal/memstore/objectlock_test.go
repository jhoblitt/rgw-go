package memstore_test

import (
	"bytes"
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// The lock fixture is a bucket CreateBucket makes with object lock, its rule
// a day of GOVERNANCE, on a clock with nanoseconds.
var _ = Describe("a create write to a bucket with object lock", func() {
	const day, year = 24 * time.Hour, 31556952 * time.Second
	var (
		s   *memstore.Store
		clk *clock
		rec *op.BucketRecord
	)
	setRule := func(r meta.DefaultRetention) {
		rec.Info.SetObjectLock(meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: r}})
	}
	BeforeEach(func(ctx SpecContext) {
		s, clk = newStore()
		clk.t = start.Add(123456789)
		var err error
		rec, err = s.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: owner("alice"), Placement: meta.PlacementRule{Name: "default-placement"}, ObjectLock: true})
		Expect(err).NotTo(HaveOccurred())
		setRule(meta.DefaultRetention{Mode: "GOVERNANCE", Days: 1})
	})
	put := func(ctx context.Context, key string, p op.PutParams) error {
		p.Size = 4
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader("data"), p)
		return err
	}
	mustPut := func(ctx context.Context, key string, p op.PutParams) {
		GinkgoHelper()
		Expect(put(ctx, key, p)).To(Succeed())
	}
	stat := func(ctx context.Context, key string) *op.ObjectState {
		GinkgoHelper()
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue(), "%s exists", key)
		return st
	}
	retention := func(ctx context.Context, key string) []byte {
		GinkgoHelper()
		st := stat(ctx, key)
		Expect(st.Attrs).To(HaveKey(meta.AttrObjectRetention), "%s", key)
		return st.Attrs[meta.AttrObjectRetention]
	}
	// upload creates an upload of mp with p's lock settings and one part.
	upload := func(ctx context.Context, p op.UploadParams) (*op.Upload, []op.CompletePart) {
		GinkgoHelper()
		up, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "mp"}, p)
		Expect(err).NotTo(HaveOccurred())
		r, err := s.PutPart(ctx, up, 1, bytes.NewReader([]byte("part")), op.PutParams{Size: 4})
		Expect(err).NotTo(HaveOccurred())
		return up, []op.CompletePart{{Number: 1, ETag: r.ETag}}
	}
	complete := func(ctx context.Context, p op.UploadParams) {
		GinkgoHelper()
		up, parts := upload(ctx, p)
		_, err := s.Complete(ctx, up, parts)
		Expect(err).NotTo(HaveOccurred())
	}

	DescribeTable("takes the rule's mode retained until the stamped mtime plus the rule's period",
		func(ctx SpecContext, write func(context.Context) string) {
			later := clk.t.Add(90 * time.Minute)
			key := write(ctx)
			Expect(stat(ctx, key).Mtime).To(Equal(later), "the object's mtime is the clock's")
			Expect(retention(ctx, key)).To(Equal(locktest.RetentionAttr("GOVERNANCE", later.Add(day))))
		},
		Entry("PutObject", func(ctx context.Context) string {
			clk.t = clk.t.Add(90 * time.Minute)
			mustPut(ctx, "k", op.PutParams{})
			return "k"
		}),
		Entry("CopyObject, which drops its source's retention", func(ctx context.Context) string {
			mustPut(ctx, "src", op.PutParams{})
			clk.t = clk.t.Add(90 * time.Minute)
			_, err := s.CopyObject(ctx, stat(ctx, "src"), rec, meta.ObjKey{Name: "k"}, op.CopyParams{})
			Expect(err).NotTo(HaveOccurred())
			return "k"
		}),
		Entry("CompleteMultipartUpload", func(ctx context.Context) string {
			clk.t = clk.t.Add(90 * time.Minute)
			complete(ctx, op.UploadParams{})
			return "mp"
		}),
	)

	It("takes the date from the mtime the write is given, not the clock", func(ctx SpecContext) {
		given := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
		mustPut(ctx, "k", op.PutParams{Mtime: given})
		Expect(retention(ctx, "k")).To(Equal(locktest.RetentionAttr("GOVERNANCE", given.Add(day))))
	})

	DescribeTable("adds the rule's period in its unit, the release's encoding alike",
		func(ctx SpecContext, rel denc.Release, r meta.DefaultRetention, want time.Time) {
			s = memstore.New(memstore.Config{Release: rel, Now: clk.now})
			var err error
			rec, err = s.CreateBucket(ctx, op.CreateBucketParams{Name: "b", Owner: owner("alice"), Placement: meta.PlacementRule{Name: "default-placement"}, ObjectLock: true})
			Expect(err).NotTo(HaveOccurred())
			setRule(r)
			mustPut(ctx, "k", op.PutParams{})
			Expect(retention(ctx, "k")).To(Equal(locktest.RetentionAttr(r.Mode, want)))
		},
		Entry("30 days of COMPLIANCE on Tentacle", denc.Tentacle, meta.DefaultRetention{Mode: "COMPLIANCE", Days: 30},
			start.Add(123456789).Add(30*day)),
		Entry("2 years of COMPLIANCE, std::chrono::years of 365.2425 days", denc.Squid, meta.DefaultRetention{Mode: "COMPLIANCE", Years: 2},
			start.Add(123456789).Add(2*year)),
		Entry("600 years, clamped to real_time's last instant", denc.Squid, meta.DefaultRetention{Mode: "GOVERNANCE", Years: 600},
			meta.MaxRealTime),
	)

	Describe("writes no retention", func() {
		It("for a lock without a rule, RGWObjectLock()", func(ctx SpecContext) {
			rec.Info.ObjLock = nil
			mustPut(ctx, "k", op.PutParams{})
			Expect(stat(ctx, "k").Attrs).NotTo(HaveKey(meta.AttrObjectRetention))
		})
		It("for a rule on a record whose flags do not enable object lock", func(ctx SpecContext) {
			rec.Info.Flags &^= meta.BucketObjLockEnabled
			mustPut(ctx, "k", op.PutParams{})
			Expect(stat(ctx, "k").Attrs).NotTo(HaveKey(meta.AttrObjectRetention))
		})
	})

	Describe("keeps a retention the write already carries", func() {
		until := func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
		header := func() []byte { return locktest.RetentionAttr("COMPLIANCE", until()) }
		It("from PutObject's request", func(ctx SpecContext) {
			mustPut(ctx, "k", op.PutParams{Attrs: map[string][]byte{meta.AttrObjectRetention: header()}})
			Expect(retention(ctx, "k")).To(Equal(header()))
		})
		It("from CopyObject's request, with COPY and with REPLACE", func(ctx SpecContext) {
			mustPut(ctx, "src", op.PutParams{})
			for _, replace := range []bool{false, true} {
				_, err := s.CopyObject(ctx, stat(ctx, "src"), rec, meta.ObjKey{Name: "k"}, op.CopyParams{
					Attrs: map[string][]byte{meta.AttrObjectRetention: header()}, ReplaceAttrs: replace,
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(retention(ctx, "k")).To(Equal(header()), "replace %t", replace)
			}
		})
		It("from the upload a completion completes", func(ctx SpecContext) {
			complete(ctx, op.UploadParams{Retention: &meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: until()}})
			Expect(retention(ctx, "mp")).To(Equal(header()))
		})
		It("but takes the default over an empty one", func(ctx SpecContext) {
			mustPut(ctx, "k", op.PutParams{Attrs: map[string][]byte{meta.AttrObjectRetention: {}}})
			Expect(retention(ctx, "k")).To(Equal(locktest.RetentionAttr("GOVERNANCE", clk.t.Add(day))))
		})
		It("and takes the default beside an upload's legal hold alone", func(ctx SpecContext) {
			complete(ctx, op.UploadParams{LegalHold: &meta.ObjectLegalHold{Status: "ON"}})
			Expect(stat(ctx, "mp").Attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, locktest.LegalHoldAttr("ON")))
			Expect(retention(ctx, "mp")).To(Equal(locktest.RetentionAttr("GOVERNANCE", clk.t.Add(day))))
		})
	})

	It("drops a copy source's legal hold, as copy_obj does", func(ctx SpecContext) {
		mustPut(ctx, "src", op.PutParams{Attrs: map[string][]byte{meta.AttrObjectLegalHold: locktest.LegalHoldAttr("ON")}})
		_, err := s.CopyObject(ctx, stat(ctx, "src"), rec, meta.ObjKey{Name: "k"}, op.CopyParams{})
		Expect(err).NotTo(HaveOccurred())
		Expect(stat(ctx, "k").Attrs).NotTo(HaveKey(meta.AttrObjectLegalHold))
	})

	DescribeTable("fails with UnknownError and stores nothing when the lock configuration does not decode",
		func(ctx SpecContext, write func(context.Context) (string, error)) {
			key, err := write(ctx)
			Expect(err).To(MatchError(op.ErrUnknown))
			st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Exists).To(BeFalse(), "%s", key)
		},
		Entry("for PutObject", func(ctx context.Context) (string, error) {
			rec.Info.ObjLock = meta.RawStruct{1, 1, 1, 0, 0, 0, 1}
			return "k", put(ctx, "k", op.PutParams{})
		}),
		Entry("for CopyObject", func(ctx context.Context) (string, error) {
			mustPut(ctx, "src", op.PutParams{})
			src := stat(ctx, "src")
			rec.Info.ObjLock = meta.RawStruct{1, 1, 1, 0, 0, 0, 1}
			_, err := s.CopyObject(ctx, src, rec, meta.ObjKey{Name: "k"}, op.CopyParams{})
			return "k", err
		}),
		Entry("for CompleteMultipartUpload, which keeps the upload", func(ctx context.Context) (string, error) {
			up, parts := upload(ctx, op.UploadParams{})
			rec.Info.ObjLock = meta.RawStruct{1, 1, 1, 0, 0, 0, 1}
			_, err := s.Complete(ctx, up, parts)
			Expect(s.GetUpload(ctx, rec, meta.ObjKey{Name: "mp"}, up.ID)).Error().NotTo(HaveOccurred(), "the upload stays for a retry")
			return "mp", err
		}),
	)
})
