package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/locktest"
)

// capturedUploads is a multipart store that records the UploadParams of each
// upload it creates.
type capturedUploads struct {
	op.MultipartStore
	params *[]op.UploadParams
}

func (c capturedUploads) CreateUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error) {
	*c.params = append(*c.params, p)
	return c.MultipartStore.CreateUpload(ctx, rec, key, p)
}

// The retention the lock headers name, to the nanosecond, after the clock of
// testEnv, 2025-09-27T08:34:58Z.
const lockUntilHeader = "2030-06-15T12:30:45.123456789Z"

var lockUntil = time.Date(2030, 6, 15, 12, 30, 45, 123456789, time.UTC)

// lockHeaders name a COMPLIANCE retention to lockUntil and a legal hold ON.
var lockHeaders = []string{
	"X-Amz-Object-Lock-Mode", "COMPLIANCE", "X-Amz-Object-Lock-Retain-Until-Date", lockUntilHeader,
	"X-Amz-Object-Lock-Legal-Hold", "ON",
}

// oneDay is a GOVERNANCE default retention of a day, which a write at
// writeMtime takes to lockDefaultUntil, the next day's noon.
var (
	oneDay           = &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: "GOVERNANCE", Days: 1}}
	lockDefaultUntil = writeMtime.Add(24 * time.Hour)
)

// lockSourceUntil is a copy source's own retention, which no copy takes.
var lockSourceUntil = time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

// expectLock checks attrs hold the retention and the legal hold the lock
// headers name, in radosgw's bytes, the retention's date to the nanosecond.
func expectLock(attrs map[string][]byte) {
	GinkgoHelper()
	Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, locktest.RetentionAttr("COMPLIANCE", lockUntil)))
	Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, locktest.LegalHoldAttr("ON")))
	r, err := objectlock.DecodeRetention(attrs[meta.AttrObjectRetention])
	Expect(err).NotTo(HaveOccurred())
	Expect(r.RetainUntil.Equal(lockUntil)).To(BeTrue(), "the date keeps its nanoseconds, %s", r.RetainUntil)
}

// lockPlain gives plain object lock and enabled versioning, with rule as its
// default retention when it is not nil.
func (w *writeWorld) lockPlain(ctx context.Context, rule *meta.ObjectLockRule) {
	GinkgoHelper()
	w.setBucketInfo(ctx, func(i *meta.BucketInfo) {
		i.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled
		i.SetObjectLock(meta.ObjectLock{Enabled: true, Rule: rule})
	})
}

// written is the attrs of the version of key in plain that rec, a write's
// 200, names.
func (w *writeWorld) written(ctx context.Context, rec *httptest.ResponseRecorder, key string) map[string][]byte {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(200), rec.Body.String())
	id := exactHeader(rec.Header(), "x-amz-version-id")
	Expect(id).NotTo(BeEmpty())
	st, err := w.store.StatObject(ctx, w.bucket(ctx), meta.ObjKey{Name: key, Instance: id})
	Expect(err).NotTo(HaveOccurred())
	Expect(st.Exists).To(BeTrue())
	return st.Attrs
}

// versionsOf is every version plain holds of key.
func (w *writeWorld) versionsOf(ctx context.Context, key string) []meta.ObjKey {
	GinkgoHelper()
	res, err := w.store.ListObjects(ctx, w.bucket(ctx), op.ListObjectsParams{Prefix: key, MaxKeys: 1000, ListVersions: true})
	Expect(err).NotTo(HaveOccurred())
	var keys []meta.ObjKey
	for _, e := range res.Entries {
		if e.Key.Name == key {
			keys = append(keys, e.Key)
		}
	}
	return keys
}

var _ = Describe("the object-lock headers of a write", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })
	copyFrom := func(src string, hdr ...string) []string { return append([]string{"X-Amz-Copy-Source", src}, hdr...) }

	Describe("PutObject", func() {
		It("stores the retention, its date to the nanosecond, and the legal hold, as RGWPutObj::execute does", func(ctx SpecContext) {
			w.lockPlain(ctx, nil)
			expectLock(w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/k", "x", lockHeaders...), "k"))
		})
		DescribeTable("stores each header alone, and a write naming none on a bucket without a rule carries neither",
			func(ctx SpecContext, hdr []string, retention, hold []byte) {
				w.lockPlain(ctx, nil)
				attrs := w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/k", "x", hdr...), "k")
				for name, want := range map[string][]byte{meta.AttrObjectRetention: retention, meta.AttrObjectLegalHold: hold} {
					if want == nil {
						Expect(attrs).NotTo(HaveKey(name))
					} else {
						Expect(attrs).To(HaveKeyWithValue(name, want))
					}
				}
			},
			Entry("a GOVERNANCE retention", []string{"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"},
				locktest.RetentionAttr("GOVERNANCE", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)), nil),
			Entry("a legal hold OFF", []string{"X-Amz-Object-Lock-Legal-Hold", "OFF"}, nil, locktest.LegalHoldAttr("OFF")),
			Entry("a date ended by white space, read up to it", []string{"X-Amz-Object-Lock-Mode", "COMPLIANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-06-15 trailing"},
				locktest.RetentionAttr("COMPLIANCE", time.Date(2030, 6, 15, 0, 0, 0, 0, time.UTC)), nil),
			Entry("no header", nil, nil, nil),
		)
		It("takes the header's retention over the bucket's default, and the default where the request names none", func(ctx SpecContext) {
			w.lockPlain(ctx, oneDay)
			expectLock(w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/k", "x", lockHeaders...), "k"))
			attrs := w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/j", "x", "X-Amz-Object-Lock-Legal-Hold", "ON"), "j")
			Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, locktest.RetentionAttr("GOVERNANCE", lockDefaultUntil)), "a legal hold alone leaves the default")
			Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, locktest.LegalHoldAttr("ON")))
			Expect(w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/i", "x"), "i")).To(HaveKeyWithValue(meta.AttrObjectRetention, locktest.RetentionAttr("GOVERNANCE", lockDefaultUntil)))
		})
		DescribeTable("reads the retain-until date to real_time's last nanosecond and refuses one past it as InvalidArgument, storing nothing",
			func(ctx SpecContext, date string, want *time.Time) {
				w.lockPlain(ctx, nil)
				rec := w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", date)
				if want == nil {
					expectError(rec, 400, "InvalidArgument")
					Expect(w.versionsOf(ctx, "k")).To(BeEmpty())
					return
				}
				Expect(w.written(ctx, rec, "k")).To(HaveKeyWithValue(meta.AttrObjectRetention, locktest.RetentionAttr("GOVERNANCE", *want)))
			},
			Entry("real_time's last nanosecond", "2554-07-21T23:34:33.709551615Z", &meta.MaxRealTime),
			Entry("a nanosecond past it", "2554-07-21T23:34:33.709551616Z", nil),
			Entry("a date radosgw wraps to a later one", "3000-01-01T00:00:00Z", nil),
		)
	})

	Describe("CopyObject", func() {
		BeforeEach(func(ctx SpecContext) {
			w.put(ctx, "lsrc", "data", map[string][]byte{
				meta.AttrObjectRetention: locktest.RetentionAttr("GOVERNANCE", lockSourceUntil),
				meta.AttrObjectLegalHold: locktest.LegalHoldAttr("OFF"),
			})
		})

		DescribeTable("stores the request's retention and legal hold in place of the source's, as copy_obj does",
			func(ctx SpecContext, directive string) {
				w.lockPlain(ctx, nil)
				rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/lsrc", append([]string{"X-Amz-Metadata-Directive", directive}, lockHeaders...)...)...)
				expectLock(w.written(ctx, rec, "dst"))
			},
			Entry("COPY", "COPY"),
			Entry("REPLACE", "REPLACE"),
		)
		DescribeTable("drops the source's retention and legal hold where the request names none",
			func(ctx SpecContext, directive string) {
				w.lockPlain(ctx, nil)
				attrs := w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/lsrc", "X-Amz-Metadata-Directive", directive)...), "dst")
				Expect(attrs).NotTo(HaveKey(meta.AttrObjectRetention))
				Expect(attrs).NotTo(HaveKey(meta.AttrObjectLegalHold))
			},
			Entry("COPY", "COPY"),
			Entry("REPLACE", "REPLACE"),
		)
		It("takes the destination's default retention, not the source's, where the request names none", func(ctx SpecContext) {
			w.lockPlain(ctx, oneDay)
			attrs := w.written(ctx, w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/lsrc", "X-Amz-Object-Lock-Legal-Hold", "ON")...), "dst")
			Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, locktest.RetentionAttr("GOVERNANCE", lockDefaultUntil)))
			Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, locktest.LegalHoldAttr("ON")))
		})
		It("refuses a date past real_time's range as InvalidArgument with get_params' message", func(ctx SpecContext) {
			w.lockPlain(ctx, nil)
			rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/lsrc",
				"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2554-07-21T23:34:33.709551616Z")...)
			expectError(rec, 400, "InvalidArgument")
			Expect(rec.Body.String()).To(ContainSubstring("<Message>invalid x-amz-object-lock-retain-until-date value</Message>"))
			Expect(w.versionsOf(ctx, "dst")).To(BeEmpty())
		})
	})

	Describe("a multipart upload", func() {
		var created []op.UploadParams
		var parts []op.PutParams
		BeforeEach(func() {
			created, parts = nil, nil
			w.env.Multipart = capturedUploads{MultipartStore: capturedParts{MultipartStore: w.store, params: &parts}, params: &created}
		})

		It("gives CreateMultipartUpload's retention and legal hold to the upload, whose completion writes them as the object's", func(ctx SpecContext) {
			w.lockPlain(ctx, oneDay)
			id := w.initUpload(w.alice, "/plain/k", lockHeaders...)
			Expect(created).To(HaveLen(1))
			Expect(created[0].Retention).NotTo(BeNil())
			Expect(created[0].Retention.Mode).To(Equal("COMPLIANCE"))
			Expect(created[0].Retention.RetainUntil.Equal(lockUntil)).To(BeTrue(), "the date keeps its nanoseconds")
			Expect(created[0].LegalHold).To(Equal(&meta.ObjectLegalHold{Status: "ON"}))
			Expect(created[0].Attrs).NotTo(HaveKey(meta.AttrObjectRetention), "the meta object holds no lock attr")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			expectLock(w.written(ctx, w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x"))), "k"))
		})
		It("gives an upload naming none neither, and its completion the bucket's default retention", func(ctx SpecContext) {
			w.lockPlain(ctx, oneDay)
			id := w.initUpload(w.alice, "/plain/k")
			Expect(created).To(HaveLen(1))
			Expect(created[0].Retention).To(BeNil())
			Expect(created[0].LegalHold).To(BeNil())
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			attrs := w.written(ctx, w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x"))), "k")
			Expect(attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, locktest.RetentionAttr("GOVERNANCE", lockDefaultUntil)))
			Expect(attrs).NotTo(HaveKey(meta.AttrObjectLegalHold))
		})
		It("refuses a CreateMultipartUpload date past real_time's range as InvalidArgument, creating no upload", func(ctx SpecContext) {
			w.lockPlain(ctx, nil)
			expectError(w.send(w.alice, http.MethodPost, "/plain/k?uploads", "",
				"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "3000-01-01T00:00:00Z"), 400, "InvalidArgument")
			Expect(created).To(BeEmpty())
		})
		It("stores an UploadPart's retention and legal hold on the part's head, as RGWPutObj::execute does, and the completion does not take them", func(ctx SpecContext) {
			w.lockPlain(ctx, nil)
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x", lockHeaders...).Code).To(Equal(200))
			Expect(parts).To(HaveLen(1))
			expectLock(parts[0].Attrs)
			attrs := w.written(ctx, w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x"))), "k")
			Expect(attrs).NotTo(HaveKey(meta.AttrObjectRetention))
			Expect(attrs).NotTo(HaveKey(meta.AttrObjectLegalHold))
		})
	})
})
