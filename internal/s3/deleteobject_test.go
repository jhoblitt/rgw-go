package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// deleteResults is an object store whose DeleteObject answers what answer
// gives for each key, and records the keys it was handed; memstore serves
// no versioned delete.
type deleteResults struct {
	op.ObjectStore
	answer func(key meta.ObjKey) (*op.DeleteObjectResult, error)
	mu     sync.Mutex
	keys   []meta.ObjKey
}

func (d *deleteResults) DeleteObject(_ context.Context, _ *op.BucketRecord, key meta.ObjKey, _ op.DeleteParams) (*op.DeleteObjectResult, error) {
	d.mu.Lock()
	d.keys = append(d.keys, key)
	d.mu.Unlock()
	return d.answer(key)
}

// answering is a deleteResults over store that answers res and err for
// every key.
func answering(store op.ObjectStore, res *op.DeleteObjectResult, err error) *deleteResults {
	return &deleteResults{ObjectStore: store, answer: func(meta.ObjKey) (*op.DeleteObjectResult, error) { return res, err }}
}

// expectNoContent checks rec is a 204 with no body and no type, as
// end_header sends a DeleteObject's
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3455-L3470).
func expectNoContent(rec *httptest.ResponseRecorder) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(204), rec.Body.String())
	Expect(rec.Body.Len()).To(BeZero())
	Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
	Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
}

var _ = Describe("delete_obj", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	It("answers 204 with no body for an existing key, and removes it", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodDelete, "/plain/src", "")
		expectNoContent(rec)
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"))
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
		Expect(w.stat(ctx, "src").Exists).To(BeFalse())
	})
	It("answers 204 for a missing key, as send_response turns -ENOENT into success", func() {
		expectNoContent(w.send(w.alice, http.MethodDelete, "/plain/missing", ""))
	})
	It("answers a missing bucket 404 and a requester the bucket refuses 403", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodDelete, "/nope/k", ""), 404, "NoSuchBucket")
		expectError(w.send(w.bob, http.MethodDelete, "/plain/src", ""), 403, "AccessDenied")
		Expect(w.stat(ctx, "src").Exists).To(BeTrue())
	})
	It("marks a requester-pays delete by a non-owner", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.RequesterPays = true })
		rec := w.send(w.bob, http.MethodDelete, "/plain/src", "", "X-Amz-Request-Payer", "requester")
		expectNoContent(rec)
		Expect(exactHeader(rec.Header(), "x-amz-request-charged")).To(Equal("requester"))
	})
	DescribeTable("refuses a condition that does not parse with InvalidArgument and keeps the object",
		func(ctx SpecContext, hdr ...string) {
			expectError(w.send(w.alice, http.MethodDelete, "/plain/src", "", hdr...), 400, "InvalidArgument")
			Expect(w.stat(ctx, "src").Exists).To(BeTrue())
		},
		Entry("x-amz-delete-if-unmodified-since that parse_date refuses", "X-Amz-Delete-If-Unmodified-Since", "garbage"),
		Entry("x-amz-delete-if-unmodified-since sent empty", "X-Amz-Delete-If-Unmodified-Since", ""),
		Entry("x-amz-if-match-size that is no number", "X-Amz-If-Match-Size", "1x"),
		Entry("x-amz-if-match-last-modified-time that is no date", "X-Amz-If-Match-Last-Modified-Time", "yesterday"),
	)
	It("url-decodes the date conditions as get_params does", func(ctx SpecContext) {
		expectNoContent(w.send(w.alice, http.MethodDelete, "/plain/src", "", "X-Amz-Delete-If-Unmodified-Since", "2026-09-28T12%3A00%3A00Z"))
		Expect(w.stat(ctx, "src").Exists).To(BeFalse())
	})
	It("keeps an object changed after x-amz-delete-if-unmodified-since, with 412", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodDelete, "/plain/src", "", "X-Amz-Delete-If-Unmodified-Since", "2026-09-28T11:00:00Z"),
			412, "PreconditionFailed")
		Expect(w.stat(ctx, "src").Exists).To(BeTrue())
	})
	DescribeTable("names the version and the delete marker the delete reports, as send_response sends del_op->result",
		func(ctx SpecContext, flags uint32, query string, res *op.DeleteObjectResult, storeErr error, version string, marker bool) {
			w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
			store := answering(w.store, res, storeErr)
			w.env.Objects = store
			rec := w.send(w.alice, http.MethodDelete, "/plain/src"+query, "")
			expectNoContent(rec)
			if version == "" {
				Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"))
			} else {
				Expect(rec.Header()).To(HaveKeyWithValue("x-amz-version-id", []string{version}))
			}
			if marker {
				Expect(rec.Header()).To(HaveKeyWithValue("x-amz-delete-marker", []string{"true"}))
			} else {
				Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), "dump_header only when delete_marker")
			}
			Expect(store.keys).To(HaveLen(1))
		},
		Entry("a delete marker on an enabled bucket", uint32(meta.BucketVersioned), "",
			&op.DeleteObjectResult{VersionID: "m1", DeleteMarker: true}, nil, "m1", true),
		Entry("the null delete marker on a suspended bucket", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "",
			&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}, nil, "null", true),
		Entry("a version removed", uint32(meta.BucketVersioned), "?versionId=v1",
			&op.DeleteObjectResult{VersionID: "v1"}, nil, "v1", false),
		Entry("a delete marker removed by its version", uint32(meta.BucketVersioned), "?versionId=m1",
			&op.DeleteObjectResult{VersionID: "m1", DeleteMarker: true}, nil, "m1", true),
		Entry("the null delete marker removed by versionId=null", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "?versionId=null",
			&op.DeleteObjectResult{VersionID: "null", DeleteMarker: true}, nil, "null", true),
		Entry("a missing version, whose -ENOENT is the 204", uint32(meta.BucketVersioned), "?versionId=v9",
			nil, op.ErrNoSuchKey, "", false),
	)
	It("hands the store the version the query names", func(ctx SpecContext) {
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
		store := answering(w.store, &op.DeleteObjectResult{VersionID: "v1"}, nil)
		w.env.Objects = store
		expectNoContent(w.send(w.alice, http.MethodDelete, "/plain/src?versionId=v1", ""))
		Expect(store.keys).To(Equal([]meta.ObjKey{{Name: "src", Instance: "v1"}}))
	})
	It("answers 501 on a bucket with object lock and deletes nothing", func(ctx SpecContext) {
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned | meta.BucketObjLockEnabled })
		store := answering(w.store, &op.DeleteObjectResult{}, nil)
		w.env.Objects = store
		expectError(w.send(w.alice, http.MethodDelete, "/plain/src?versionId=v1", ""), 501, "NotImplemented")
		expectError(w.send(w.alice, http.MethodDelete, "/plain/src", ""), 501, "NotImplemented")
		Expect(store.keys).To(BeEmpty())
	})
	Describe("on a bucket with MFA delete", func() {
		var store *deleteResults
		BeforeEach(func(ctx SpecContext) {
			w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned | meta.BucketMFAEnabled })
			w.alice.Info.MFAIDs = []string{mfaSerial}
			w.env.MFA = deviceVerifier()
			store = answering(w.store, &op.DeleteObjectResult{VersionID: "v1"}, nil)
			w.env.Objects = store
		})
		DescribeTable("deletes a version only under the owner's verified x-amz-mfa, and a delete marker needs none",
			func(target string, deleted bool, hdr ...string) {
				rec := w.send(w.alice, http.MethodDelete, target, "", hdr...)
				if deleted {
					expectNoContent(rec)
					Expect(store.keys).To(HaveLen(1))
					return
				}
				expectError(rec, 403, "AccessDenied")
				Expect(store.keys).To(BeEmpty())
			},
			Entry("a version without x-amz-mfa", "/plain/src?versionId=v1", false),
			Entry("a version under a code that does not verify", "/plain/src?versionId=v1", false, "x-amz-mfa", "SERIAL 654321"),
			Entry("a version under the owner's verified code", "/plain/src?versionId=v1", true, "x-amz-mfa", "SERIAL 123456"),
			Entry("the null version without x-amz-mfa", "/plain/src?versionId=null", false),
			Entry("no version without x-amz-mfa", "/plain/src", true),
		)
		It("refuses a version to another requester's own verified code", func(ctx SpecContext) {
			w.grantBob(ctx, acl.PermWrite)
			w.bob.Info.MFAIDs = []string{mfaSerial}
			expectError(w.send(w.bob, http.MethodDelete, "/plain/src?versionId=v1", "", "x-amz-mfa", "SERIAL 123456"), 403, "AccessDenied")
			Expect(store.keys).To(BeEmpty())
		})
	})
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })
			DescribeTable("checks the match conditions as v19.2.7 and v20.2.4 do",
				func(ctx SpecContext, deleted bool, hdr ...string) {
					rec := w.send(w.alice, http.MethodDelete, "/plain/src", "", hdr...)
					if deleted {
						expectNoContent(rec)
					} else {
						expectError(rec, 412, "PreconditionFailed")
					}
					Expect(w.stat(ctx, "src").Exists).To(Equal(!deleted))
				},
				Entry("If-Match naming another ETag", false, "If-Match", `"wrong"`),
				Entry("If-Match sent empty, a condition no ETag meets", false, "If-Match", ""),
				Entry("If-Match naming the ETag", true, "If-Match", `"`+md5Hex("hello")+`"`),
				Entry("x-amz-if-match-size of another size", false, "X-Amz-If-Match-Size", "4"),
				Entry("x-amz-if-match-size of the size", true, "X-Amz-If-Match-Size", "5"),
				Entry("x-amz-if-match-last-modified-time of another time", false, "X-Amz-If-Match-Last-Modified-Time", "Mon, 28 Sep 2026 11:00:00 GMT"),
				Entry("x-amz-if-match-last-modified-time of the mtime", true, "X-Amz-If-Match-Last-Modified-Time", "Mon, 28 Sep 2026 12:00:00 GMT"),
			)
		})
	}
})
