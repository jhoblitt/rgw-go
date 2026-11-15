package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// sizedRule is rule1's Days 1 for objects over gt bytes and under lt, either
// "" for no bound, as botocore writes it.
func sizedRule(gt, lt string) string {
	var b strings.Builder
	if gt != "" {
		b.WriteString("<ObjectSizeGreaterThan>" + gt + "</ObjectSizeGreaterThan>")
	}
	if lt != "" {
		b.WriteString("<ObjectSizeLessThan>" + lt + "</ObjectSizeLessThan>")
	}
	return `<Rule><Expiration><Days>1</Days></Expiration><ID>rule1</ID><Filter><Prefix></Prefix>` + b.String() +
		`</Filter><Status>Enabled</Status></Rule>`
}

// initiationReads is a multipart store that reads an upload's initiation as
// the driver's GetUpload does, the meta object's own mtime, which a part
// has moved to moved, and counts the reads of UploadInitiated, answering
// answer when it is set.
type initiationReads struct {
	op.MultipartStore
	moved  time.Time
	answer *time.Time
	reads  *int
}

func (s initiationReads) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, id string) (*op.Upload, error) {
	up, err := s.MultipartStore.GetUpload(ctx, rec, key, id)
	if err == nil {
		up.Initiated = s.moved
	}
	return up, err
}

func (s initiationReads) UploadInitiated(ctx context.Context, up *op.Upload) (time.Time, error) {
	*s.reads++
	if s.answer != nil {
		return *s.answer, nil
	}
	return s.MultipartStore.UploadInitiated(ctx, up)
}

var _ = Describe("the lifecycle headers on CopyObject, CompleteMultipartUpload and ListParts", func() {
	copyFrom := func(src string, hdr ...string) []string { return append([]string{"X-Amz-Copy-Source", src}, hdr...) }

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *writeWorld
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.env.Lifecycle = &opfakes.FakeLifecycleStore{}
			})

			// completeOnePart uploads body as the one part of an upload of
			// key, created with the create headers, and completes it.
			completeOnePart := func(key, body string, create ...string) *httptest.ResponseRecorder {
				GinkgoHelper()
				id, doc := w.partsUpload(key, create, nil, []byte(body))
				return w.send(w.alice, http.MethodPost, "/plain/"+key+"?uploadId="+id, doc)
			}

			Describe("CopyObject's x-amz-expiration", func() {
				It("is sent for the destination under a matching rule", func() {
					w.putLifecycle(days1Rule)
					rec := w.send(w.alice, http.MethodPut, "/plain/days1/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))
					Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), "the headers precede the chunked document")
				})

				It("is not sent for a destination no rule matches, without a configuration or for one that does not decode", func(ctx SpecContext) {
					rec := w.send(w.alice, http.MethodPut, "/plain/days1/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "no configuration")

					w.putLifecycle(days1Rule)
					rec = w.send(w.alice, http.MethodPut, "/plain/other/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "days1/ does not begin other/dst")

					w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {1}})
					rec = w.send(w.alice, http.MethodPut, "/plain/days1/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "the configuration does not decode")
				})

				It("is not sent on a copy that is refused", func() {
					w.putLifecycle(everyRule)
					missing := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/missing")...)
					expectError(missing, http.StatusNotFound, "NoSuchKey")
					Expect(missing.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
					rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src", "X-Amz-Copy-Source-If-Match", `"wrong"`)...)
					expectError(rec, http.StatusPreconditionFailed, "PreconditionFailed")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
				})

				It("matches the tags the destination holds: the source's under COPY, the request's under REPLACE", func() {
					w.putLifecycle(tag1Rule)
					Expect(w.send(w.alice, http.MethodPut, "/plain/tagged", "x", "X-Amz-Tagging", "key1=tag1&key5=tag5").Code).To(Equal(http.StatusOK))

					rec := w.send(w.alice, http.MethodPut, "/plain/copied", "", copyFrom("/plain/tagged")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}), "COPY keeps the source's tags")

					rec = w.send(w.alice, http.MethodPut, "/plain/replaced", "", copyFrom("/plain/tagged", "X-Amz-Metadata-Directive", "REPLACE")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "REPLACE gives the destination the request's tags, none")

					rec = w.send(w.alice, http.MethodPut, "/plain/retagged", "",
						copyFrom("/plain/src", "X-Amz-Metadata-Directive", "REPLACE", "X-Amz-Tagging", "key1=tag1&key5=tag5")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}), "REPLACE stores the request's x-amz-tagging as the destination's tags")
				})

				It("is not sent for a tag rule when neither the source nor the request gives the destination tags", func() {
					w.putLifecycle(tag1Rule)
					rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
				})

				DescribeTable("counts a size rule only for a destination whose size it admits, strictly, as the worker does",
					func(gt, lt string, counted bool) {
						w.putLifecycle(sizedRule(gt, lt))
						Expect(w.send(w.alice, http.MethodPut, "/plain/four", "abcd").Code).To(Equal(http.StatusOK))
						rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/four")...)
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						if counted {
							Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))
						} else {
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "radosgw names rule1 for any size")
						}
					},
					Entry("4 bytes over an ObjectSizeGreaterThan of 3", "3", "", true),
					Entry("4 bytes at an ObjectSizeGreaterThan of 4", "4", "", false),
					Entry("4 bytes under an ObjectSizeLessThan of 5", "", "5", true),
					Entry("4 bytes at an ObjectSizeLessThan of 4", "", "4", false),
				)

				It("is not sent for a request that names a versionId", func() {
					w.putLifecycle(everyRule)
					rec := w.send(w.alice, http.MethodPut, "/plain/dst?versionId=v1", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "rgw_rest_s3.cc:123-125 at v19.2.7, as PutObject")
				})

				It("takes the Expiration rule for the version written to a versioning-enabled bucket, where Tentacle's radosgw takes NoncurrentVersionExpiration", func(ctx SpecContext) {
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
					w.putLifecycle(everyRule, ncRule)
					rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(MatchRegexp(newInstance), "the copy made a new version")
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{`expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="every"`}))

					w.putLifecycle(ncRule)
					rec = w.send(w.alice, http.MethodPut, "/plain/dst", "", copyFrom("/plain/src")...)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "a noncurrent-only rule names no date for the version written")
				})
			})

			Describe("CompleteMultipartUpload's x-amz-expiration", func() {
				It("is sent for the completed object under a matching rule", func() {
					w.putLifecycle(days1Rule)
					rec := completeOnePart("days1/k", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))
				})

				It("is not sent for an object no rule matches, without a configuration or for one that does not decode", func(ctx SpecContext) {
					rec := completeOnePart("days1/k", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "no configuration")

					w.putLifecycle(days1Rule)
					rec = completeOnePart("other/k", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "days1/ does not begin other/k")

					w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {1}})
					rec = completeOnePart("days1/k", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "the configuration does not decode")
				})

				It("is not sent on a completion that is refused", func() {
					w.putLifecycle(everyRule)
					id, _ := w.partsUpload("k", nil, nil, []byte("part"))
					rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("other")))
					expectError(rec, http.StatusBadRequest, "InvalidPart")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
				})

				It("matches the tags the upload was created with", func() {
					w.putLifecycle(tag1Rule)
					rec := completeOnePart("tagged", "part", "X-Amz-Tagging", "key1=tag1&key5=tag5")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))

					rec = completeOnePart("untagged", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "an upload without tags matches no tag rule")
				})

				DescribeTable("counts a size rule only for an object whose size it admits, strictly, as the worker does",
					func(gt, lt string, counted bool) {
						w.putLifecycle(sizedRule(gt, lt))
						rec := completeOnePart("k", "abcd")
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						if counted {
							Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))
						} else {
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "radosgw names rule1 for any size")
						}
					},
					Entry("4 bytes over an ObjectSizeGreaterThan of 3", "3", "", true),
					Entry("4 bytes at an ObjectSizeGreaterThan of 4", "4", "", false),
					Entry("4 bytes under an ObjectSizeLessThan of 5", "", "5", true),
					Entry("4 bytes at an ObjectSizeLessThan of 4", "", "4", false),
				)

				It("is sent again for a completion retried after it succeeded, for the object it wrote", func() {
					w.putLifecycle(tag1Rule)
					id, doc := w.partsUpload("tagged", []string{"X-Amz-Tagging", "key1=tag1&key5=tag5"}, nil, []byte("part"))
					first := w.send(w.alice, http.MethodPost, "/plain/tagged?uploadId="+id, doc)
					Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
					retried := w.send(w.alice, http.MethodPost, "/plain/tagged?uploadId="+id, doc)
					Expect(retried.Code).To(Equal(http.StatusOK), retried.Body.String())
					Expect(exactValues(retried.Header(), "x-amz-expiration")).To(Equal(exactValues(first.Header(), "x-amz-expiration")))
					Expect(exactValues(retried.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))
				})

				It("is not sent for a request that names a versionId", func() {
					w.putLifecycle(everyRule)
					id, doc := w.partsUpload("k", nil, nil, []byte("part"))
					rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id+"&versionId=v1", doc)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "rgw_rest_s3.cc:123-125 at v19.2.7, as PutObject")
				})

				It("takes the Expiration rule for the version written to a versioning-enabled bucket", func(ctx SpecContext) {
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
					w.putLifecycle(everyRule, ncRule)
					rec := completeOnePart("k", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(MatchRegexp(newInstance), "the completion made a new version")
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{`expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="every"`}))

					w.putLifecycle(ncRule)
					rec = completeOnePart("k", "part")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "a noncurrent-only rule names no date for the version written")
				})
			})

			Describe("ListParts' x-amz-abort-date and x-amz-abort-rule-id", func() {
				var id string
				BeforeEach(func() {
					id = w.initUpload(w.alice, "/plain/k")
					Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "part").Code).To(Equal(http.StatusOK))
				})

				DescribeTable("name the earliest DaysAfterInitiation date from the upload's initiation, and its rule",
					func(method string) {
						w.putLifecycle(abortRule("a", "", "7"), abortRule("b", "", "3"))
						rec := w.send(w.alice, method, "/plain/k?uploadId="+id, "")
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						Expect(exactValues(rec.Header(), "x-amz-abort-date")).To(Equal([]string{"Fri, 02 Oct 2026 00:00:00 GMT"}),
							"3 days after writeMtime, the next midnight UTC; radosgw sends none")
						Expect(exactValues(rec.Header(), "x-amz-abort-rule-id")).To(Equal([]string{"b"}))
					},
					Entry("GET", http.MethodGet),
					Entry("HEAD", http.MethodHead),
				)

				It("take the rule whose prefix begins the key", func() {
					logs := w.initUpload(w.alice, "/plain/logs/a")
					w.putLifecycle(abortRule("logs", "logs/", "1"))
					rec := w.send(w.alice, http.MethodGet, "/plain/logs/a?uploadId="+logs, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-abort-date")).To(Equal([]string{"Wed, 30 Sep 2026 00:00:00 GMT"}))
					Expect(exactValues(rec.Header(), "x-amz-abort-rule-id")).To(Equal([]string{"logs"}))

					rec = w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"), "logs/ does not begin k")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
				})

				It("match the prefix alone, whatever tags the rule filters by", func(ctx SpecContext) {
					tagged := lifecycle.Rule{ID: "tagged", Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "2"}}
					tagged.Filter.Tags = tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}
					w.storeLifecycle(ctx, tagged)
					rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-abort-rule-id")).To(Equal([]string{"tagged"}))
				})

				It("are not sent without a rule that aborts uploads, or for a configuration that does not decode", func(ctx SpecContext) {
					rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"), "no configuration")

					w.putLifecycle(everyRule)
					rec = w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"), "an Expiration rule aborts no upload")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))

					w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {1}})
					rec = w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"), "the configuration does not decode")
				})

				It("leave out an empty rule ID, as dump_header_if_nonempty does", func(ctx SpecContext) {
					w.storeLifecycle(ctx, lifecycle.Rule{Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "1"}})
					rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-abort-date")).To(Equal([]string{"Wed, 30 Sep 2026 00:00:00 GMT"}))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
				})

				It("are not sent for an upload that does not exist, which read_obj_policy answers NoSuchKey", func() {
					w.putLifecycle(abortRule("a", "", "1"))
					rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId=2~"+strings.Repeat("x", 31), "")
					expectError(rec, http.StatusNotFound, "NoSuchKey")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
				})

				It("count from the initiation the store reads for the upload, not the meta object's mtime GetUpload reports, which a part moves", func() {
					var reads int
					w.env.Multipart = initiationReads{MultipartStore: w.store, moved: writeMtime.Add(5 * 24 * time.Hour), reads: &reads}
					w.putLifecycle(abortRule("b", "", "3"))
					rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-abort-date")).To(Equal([]string{"Fri, 02 Oct 2026 00:00:00 GMT"}), "3 days from the initiation, not from 5 days on")
					Expect(reads).To(Equal(1))
				})

				It("reads the initiation only for a bucket whose lifecycle aborts uploads of the key", func() {
					var reads int
					w.env.Multipart = initiationReads{MultipartStore: w.store, moved: writeMtime, reads: &reads}
					Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "").Code).To(Equal(http.StatusOK))
					w.putLifecycle(everyRule)
					Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "").Code).To(Equal(http.StatusOK))
					w.putLifecycle(abortRule("logs", "logs/", "1"))
					Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "").Code).To(Equal(http.StatusOK))
					Expect(reads).To(BeZero(), "no configuration, an Expiration rule and an abort rule of another prefix")
					w.putLifecycle(abortRule("every", "", "1"))
					Expect(w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "").Code).To(Equal(http.StatusOK))
					Expect(reads).To(Equal(1))
				})

				It("are not sent when the store finds no index entry for the upload, which counts from no time", func() {
					var reads int
					var none time.Time
					w.env.Multipart = initiationReads{MultipartStore: w.store, moved: writeMtime, answer: &none, reads: &reads}
					w.putLifecycle(abortRule("a", "", "1"))
					rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(reads).To(Equal(1))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"), "a date from the epoch would name 1970")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
				})
			})
		})
	}
})
