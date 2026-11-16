package s3_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// putLifecycle puts a configuration of rules on plain through
// PutBucketLifecycleConfiguration, as an s3-test does.
func (w *writeWorld) putLifecycle(rules ...string) {
	GinkgoHelper()
	rec := w.send(w.alice, http.MethodPut, "/plain?lifecycle", lcDocument(rules...))
	Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
}

// storeLifecycle stores rules on plain under user.rgw.lc, for a
// configuration PutBucketLifecycleConfiguration refuses but a radosgw or a
// bucket.instance metadata put can leave.
func (w *writeWorld) storeLifecycle(ctx context.Context, rules ...lifecycle.Rule) {
	GinkgoHelper()
	w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcEncoded(lifecycle.Configuration{Rules: rules})})
}

// exactValues are h's values under name as spelled, the spelling radosgw's
// dump_header sends and addHeader keeps.
func exactValues(h http.Header, name string) []string { return h[name] }

// expiryPattern is the form check_lifecycle_expiration_header reads.
var expiryPattern = regexp.MustCompile(`expiry-date="(.+)", rule-id="(.+)"`)

// expiryMatches is s3-tests' check_lifecycle_expiration_header
// (https://github.com/ceph/ceph/blob/358477718367e91a9959ebdf33edfe527ab1f107/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L10954-L10967):
// h holds x-amz-expiration, its rule is ruleID and its date is days whole
// days after start.
func expiryMatches(h http.Header, start time.Time, ruleID string, days int) bool {
	m := expiryPattern.FindStringSubmatch(exactHeader(h, "x-amz-expiration"))
	if m == nil {
		return false
	}
	date, err := time.Parse(http.TimeFormat, m[1])
	if err != nil {
		return false
	}
	return int(date.Sub(start)/(24*time.Hour)) == days && m[2] == ruleID
}

// The rules of the four s3-tests, as botocore writes them.
const (
	days1Rule = `<Rule><Expiration><Days>1</Days></Expiration><ID>rule1</ID><Prefix>days1/</Prefix><Status>Enabled</Status></Rule>`
	tag1Rule  = `<Rule><Expiration><Days>1</Days></Expiration><ID>rule1</ID><Filter><Tag><Key>key1</Key><Value>tag1</Value></Tag></Filter>` +
		`<Status>Enabled</Status></Rule>`
	tag2Rule = `<Rule><Expiration><Days>1</Days></Expiration><ID>rule1</ID><Filter><Tag><Key>key2</Key><Value>tag1</Value></Tag></Filter>` +
		`<Status>Enabled</Status></Rule>`
	andTagsRule = `<Rule><Expiration><Days>1</Days></Expiration><ID>rule1</ID><Filter><And><Tag><Key>key1</Key><Value>tag1</Value></Tag>` +
		`<Tag><Key>key5</Key><Value>tag6</Value></Tag></And></Filter><Status>Enabled</Status></Rule>`
	// tags1 is the tag set the tag tests put on obj_key1.
	tags1 = `<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>key1</Key><Value>tag1</Value></Tag>` +
		`<Tag><Key>key5</Key><Value>tag5</Value></Tag></TagSet></Tagging>`
	everyRule = `<Rule><Expiration><Days>1</Days></Expiration><ID>every</ID><Prefix></Prefix><Status>Enabled</Status></Rule>`
	ncRule    = `<Rule><ID>nc</ID><Prefix></Prefix><Status>Enabled</Status>` +
		`<NoncurrentVersionExpiration><NoncurrentDays>1</NoncurrentDays></NoncurrentVersionExpiration></Rule>`
)

// abortRule is a rule of id aborting uploads under prefix days after they
// begin.
func abortRule(id, prefix, days string) string {
	return `<Rule><ID>` + id + `</ID><Prefix>` + prefix + `</Prefix><Status>Enabled</Status>` +
		`<AbortIncompleteMultipartUpload><DaysAfterInitiation>` + days + `</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>`
}

// day1Expiry is the x-amz-expiration of rule1's Days 1 for an object written
// at writeMtime, Monday 2026-09-28T12:00:00Z: the first midnight UTC a day
// after it.
const day1Expiry = `expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="rule1"`

var _ = Describe("the lifecycle response headers", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var w *writeWorld
			BeforeEach(func(ctx SpecContext) {
				w = newWriteWorld(ctx, rel)
				w.env.Lifecycle = &opfakes.FakeLifecycleStore{}
			})

			Describe("x-amz-expiration", func() {
				It("is sent on PutObject, test_lifecycle_expiration_header_put", func() {
					w.putLifecycle(days1Rule)
					rec := w.send(w.alice, http.MethodPut, "/plain/days1/foo", "bar")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(expiryMatches(rec.Header(), writeMtime, "rule1", 1)).To(BeTrue(), exactHeader(rec.Header(), "x-amz-expiration"))
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}), "dump_header_if_nonempty, rgw_rest_s3.cc:2757 at v19.2.7")
				})

				DescribeTable("is sent on a read, test_lifecycle_expiration_header_head",
					func(method string) {
						w.putLifecycle(days1Rule)
						Expect(w.send(w.alice, http.MethodPut, "/plain/days1/foo", "bar").Code).To(Equal(http.StatusOK))
						rec := w.send(w.alice, method, "/plain/days1/foo", "")
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						Expect(expiryMatches(rec.Header(), writeMtime, "rule1", 1)).To(BeTrue(), exactHeader(rec.Header(), "x-amz-expiration"))
						Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}), "rgw_rest_s3.cc:470 at v19.2.7, :498 at v20.2.4")
					},
					Entry("HEAD", http.MethodHead),
					Entry("GET", http.MethodGet),
				)

				It("matches the tags the object holds, test_lifecycle_expiration_header_tags_head", func() {
					w.putLifecycle(tag1Rule)
					Expect(w.send(w.alice, http.MethodPut, "/plain/obj_key1", "obj_key1_body").Code).To(Equal(http.StatusOK))
					Expect(w.send(w.alice, http.MethodPut, "/plain/obj_key1?tagging", tags1).Code).To(Equal(http.StatusOK))
					rec := w.send(w.alice, http.MethodHead, "/plain/obj_key1", "")
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(expiryMatches(rec.Header(), writeMtime, "rule1", 1)).To(BeTrue(), exactHeader(rec.Header(), "x-amz-expiration"))

					w.putLifecycle(tag2Rule)
					rec = w.send(w.alice, http.MethodHead, "/plain/obj_key1", "")
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(expiryMatches(rec.Header(), writeMtime, "rule1", 1)).To(BeFalse())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
				})

				It("needs every tag of an And, test_lifecycle_expiration_header_and_tags_head", func() {
					w.putLifecycle(andTagsRule)
					Expect(w.send(w.alice, http.MethodPut, "/plain/obj_key1", "obj_key1_body").Code).To(Equal(http.StatusOK))
					Expect(w.send(w.alice, http.MethodPut, "/plain/obj_key1?tagging", tags1).Code).To(Equal(http.StatusOK))
					rec := w.send(w.alice, http.MethodHead, "/plain/obj_key1", "")
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(expiryMatches(rec.Header(), writeMtime, "rule1", 1)).To(BeFalse())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
				})

				It("matches a PutObject's own x-amz-tagging, the set rgw_cond_decode_objtags reads back from the attrs it writes", func() {
					w.putLifecycle(tag1Rule)
					rec := w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Tagging", "key1=tag1&key5=tag5")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}))

					rec = w.send(w.alice, http.MethodPut, "/plain/k", "x")
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "a PutObject without tags matches no tag rule")
				})

				It("reads a stored text tag set as RGWObjTags::decode leaves it, the tags before the item set_from_string refuses", func(ctx SpecContext) {
					w.putLifecycle(tag1Rule)
					w.put(ctx, "k", "x", map[string][]byte{tags.Attr: []byte("key1=tag1&\x00")})
					rec := w.send(w.alice, http.MethodHead, "/plain/k", "")
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}),
						"the empty item is refused and key1=tag1 kept, rgw_tag.h:32-62 and rgw_tag.cc:36-57 at v19.2.7")
				})

				It("is not sent on a 304, which send_response_data answers before its headers", func() {
					w.putLifecycle(everyRule)
					put := w.send(w.alice, http.MethodPut, "/plain/k", "x")
					Expect(put.Header()).To(HaveKey("x-amz-expiration"))
					rec := w.send(w.alice, http.MethodGet, "/plain/k", "", "If-None-Match", exactHeader(put.Header(), "ETag"))
					Expect(rec.Code).To(Equal(http.StatusNotModified))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "if (op_ret) goto done, rgw_rest_s3.cc:411-412 at v19.2.7")
				})

				It("is not sent on a PutObject naming a versionId, which get_s3_expiration_header passes on", func() {
					w.putLifecycle(everyRule)
					rec := w.send(w.alice, http.MethodPut, "/plain/k?versionId=v1", "x")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "rgw_rest_s3.cc:123-125 at v19.2.7")
				})

				It("takes the Expiration rule for the version a PutObject writes on a versioning-enabled bucket, where Tentacle's radosgw takes its NoncurrentVersionExpiration", func(ctx SpecContext) {
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
					w.putLifecycle(everyRule, ncRule)
					rec := w.send(w.alice, http.MethodPut, "/plain/k", "x")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(MatchRegexp(newInstance), "the write made a new version")
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{`expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="every"`}),
						"v20.2.4 passes the written instance, rgw_op.cc:4491-4497 and rgw_rest_s3.cc:117-123, and names nc")

					w.putLifecycle(ncRule)
					rec = w.send(w.alice, http.MethodPut, "/plain/k", "x")
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "a noncurrent-only rule names no date for the version written")
				})

				Describe("on a versioned bucket, whose read resolves the current version's instance", func() {
					BeforeEach(func(ctx SpecContext) {
						w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketVersioned })
						w.put(ctx, "k", "null version", nil)
						w.putVersion(ctx, "k", "v1", "current version")
						w.env.Objects = versions{ObjectStore: w.store, current: map[string]string{"k": "v1"}, markers: map[meta.ObjKey]bool{}}
					})

					DescribeTable("takes the request's versionId, not the instance the read resolves",
						func(method string) {
							w.putLifecycle(everyRule)
							rec := w.send(w.alice, method, "/plain/k", "")
							Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
							Expect(exactHeader(rec.Header(), "x-amz-version-id")).To(Equal("v1"), "the read resolved the current version")
							Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{`expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="every"`}))
						},
						Entry("HEAD", http.MethodHead),
						Entry("GET", http.MethodGet),
					)

					DescribeTable("is not sent for a request that names a version, v19.2.7's form on both releases",
						func(method, version string) {
							w.putLifecycle(everyRule)
							rec := w.send(w.alice, method, "/plain/k?versionId="+version, "")
							Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "rgw_lc.cc:2766-2767 at v19.2.7")
						},
						Entry("HEAD of v1", http.MethodHead, "v1"),
						Entry("GET of v1", http.MethodGet, "v1"),
						Entry("GET of the null version", http.MethodGet, "null"),
					)

					It("names no NoncurrentVersionExpiration rule for the current version, where Tentacle's radosgw does", func() {
						w.putLifecycle(ncRule)
						for _, target := range []string{"/plain/k", "/plain/k?versionId=v1"} {
							rec := w.send(w.alice, http.MethodHead, target, "")
							Expect(rec.Code).To(Equal(http.StatusOK), target)
							Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), target)
						}
					})
				})

				It("gives a stored Date real_time cannot hold no date, so another rule's Days give the header", func(ctx SpecContext) {
					wrapped := lifecycle.Rule{ID: "a", Status: "Enabled", Expiration: lifecycle.Expiration{Date: "2600-01-01T23:34:33.709551616Z"}}
					w.put(ctx, "k", "x", nil)
					w.storeLifecycle(ctx, wrapped)
					rec := w.send(w.alice, http.MethodHead, "/plain/k", "")
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "radosgw names the wrapped 2015-06-14")

					w.storeLifecycle(ctx, wrapped, lifecycle.Rule{ID: "b", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "5"}})
					rec = w.send(w.alice, http.MethodHead, "/plain/k", "")
					Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{`expiry-date="Sun, 04 Oct 2026 00:00:00 GMT", rule-id="b"`}))
				})

				It("is not sent when the stored configuration does not decode, and the request succeeds", func(ctx SpecContext) {
					w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {1}})
					put := w.send(w.alice, http.MethodPut, "/plain/k", "x")
					Expect(put.Code).To(Equal(http.StatusOK), put.Body.String())
					Expect(put.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
					head := w.send(w.alice, http.MethodHead, "/plain/k", "")
					Expect(head.Code).To(Equal(http.StatusOK))
					Expect(head.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "rgw_lc.cc:2773-2781 at v19.2.7")
				})

				Describe("under a rule with an object-size filter", func() {
					// sizeRule is rule1's Days 1 for objects whose size is above
					// gt and below lt, either "" for no bound, as botocore writes it.
					sizeRule := func(gt, lt string) string {
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
					// written answers each of PutObject, GET and HEAD for an
					// object k holding body.
					written := func(body string) map[string]*httptest.ResponseRecorder {
						GinkgoHelper()
						put := w.send(w.alice, http.MethodPut, "/plain/k", body)
						Expect(put.Code).To(Equal(http.StatusOK), put.Body.String())
						return map[string]*httptest.ResponseRecorder{
							"PutObject": put,
							"GET":       w.send(w.alice, http.MethodGet, "/plain/k", ""),
							"HEAD":      w.send(w.alice, http.MethodHead, "/plain/k", ""),
						}
					}

					DescribeTable("counts the rule only for an object whose size it admits, strictly, as the worker does",
						func(gt, lt, body string, counted bool) {
							w.putLifecycle(sizeRule(gt, lt))
							for name, rec := range written(body) {
								Expect(rec.Code).To(Equal(http.StatusOK), name)
								if counted {
									Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{day1Expiry}), name)
								} else {
									Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"), "%s: radosgw names rule1 for any size", name)
								}
							}
						},
						Entry("4 bytes, over an ObjectSizeGreaterThan of 3", "3", "", "abcd", true),
						Entry("3 bytes, at it", "3", "", "abc", false),
						Entry("2 bytes, under it", "3", "", "ab", false),
						Entry("2 bytes, under an ObjectSizeLessThan of 3", "", "3", "ab", true),
						Entry("3 bytes, at it", "", "3", "abc", false),
						Entry("4 bytes, over it", "", "3", "abcd", false),
						Entry("3 bytes, between 2 and 4", "2", "4", "abc", true),
					)

					It("names the unfiltered rule for an object the filtered rule beside it excludes", func() {
						month := `<Rule><Expiration><Days>30</Days></Expiration><ID>month</ID><Prefix></Prefix><Status>Enabled</Status></Rule>`
						w.putLifecycle(sizeRule("1000", ""), month)
						for name, rec := range written("small") {
							Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{`expiry-date="Thu, 29 Oct 2026 00:00:00 GMT", rule-id="month"`}),
								"%s: radosgw names rule1's day, and its worker removes the object on day 30", name)
						}
					})
				})

				It("is not sent on UploadPart or UploadPartCopy, as S3 sends none there", func() {
					w.putLifecycle(everyRule)
					id := w.initUpload(w.alice, "/plain/k")
					part := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x")
					Expect(part.Code).To(Equal(http.StatusOK), part.Body.String())
					Expect(part.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
					copied := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "",
						"X-Amz-Copy-Source", "/plain/src", "Content-Length", "0")
					Expect(copied.Code).To(Equal(http.StatusOK), copied.Body.String())
					Expect(copied.Header()).NotTo(haveHeaderAnyCase("x-amz-expiration"))
				})
			})

			Describe("x-amz-abort-date and x-amz-abort-rule-id", func() {
				It("name the earliest DaysAfterInitiation date from the upload's initiation, and its rule", func() {
					w.putLifecycle(abortRule("a", "", "7"), abortRule("b", "", "3"))
					rec := serveReq(w.handler(w.alice), http.MethodPost, "/plain/k?uploads", nil)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-abort-date")).To(Equal([]string{"Fri, 02 Oct 2026 00:00:00 GMT"}),
						"3 days after writeMtime, the next midnight UTC; radosgw counts from the epoch and names Mon, 05 Jan 1970")
					Expect(exactValues(rec.Header(), "x-amz-abort-rule-id")).To(Equal([]string{"b"}))
				})

				It("match the prefix alone, whatever tags the rule filters by", func(ctx SpecContext) {
					tagged := lifecycle.Rule{ID: "tagged", Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "2"}}
					tagged.Filter.Tags = tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}}
					elsewhere := lifecycle.Rule{ID: "elsewhere", Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "1"}}
					elsewhere.Filter.Prefix = "data/"
					w.storeLifecycle(ctx, tagged, elsewhere)
					for target, want := range map[string]string{"/plain/logs/a": "tagged", "/plain/data/a": "elsewhere"} {
						rec := serveReq(w.handler(w.alice), http.MethodPost, target+"?uploads", nil)
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						Expect(exactValues(rec.Header(), "x-amz-abort-rule-id")).To(Equal([]string{want}), target)
					}

					w.storeLifecycle(ctx, elsewhere)
					rec := serveReq(w.handler(w.alice), http.MethodPost, "/plain/logs/a?uploads", nil)
					Expect(rec.Code).To(Equal(http.StatusOK))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"), "no rule's prefix begins the key")
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
				})

				It("are not sent without a rule that aborts uploads, or for a configuration that does not decode", func(ctx SpecContext) {
					w.putLifecycle(everyRule)
					for _, stored := range []bool{false, true} {
						if stored {
							w.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {1}})
						}
						rec := serveReq(w.handler(w.alice), http.MethodPost, "/plain/k?uploads", nil)
						Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"))
						Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
					}
				})

				It("are not sent on a refusal, which radosgw's send_response sends them with", func() {
					w.putLifecycle(abortRule("a", "", "1"))
					rec := serveReq(w.handler(w.alice), http.MethodPost, "/plain/k?uploads", nil, "X-Amz-Tagging", "=")
					Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-date"))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"))
				})

				It("leave out an empty rule ID, as dump_header_if_nonempty does", func(ctx SpecContext) {
					w.storeLifecycle(ctx, lifecycle.Rule{Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "1"}})
					rec := serveReq(w.handler(w.alice), http.MethodPost, "/plain/k?uploads", nil)
					Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
					Expect(exactValues(rec.Header(), "x-amz-abort-date")).To(Equal([]string{"Wed, 30 Sep 2026 00:00:00 GMT"}))
					Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-abort-rule-id"), "rgw_rest_s3.cc:4071 at v19.2.7")
				})
			})

			DescribeTable("sends a stored rule ID URL-encoded in x-amz-expiration and x-amz-abort-rule-id, on every response that carries them",
				func(ctx SpecContext, id, wantID string) {
					w.put(ctx, "k", "x", nil)
					w.storeLifecycle(ctx, lifecycle.Rule{
						ID: id, Status: "Enabled",
						Expiration: lifecycle.Expiration{Days: "1"}, MPExpiration: lifecycle.Expiration{Days: "1"},
					})
					wantExpiry := `expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="` + wantID + `"`
					for _, call := range []struct{ name, method string }{{"PutObject", http.MethodPut}, {"GET", http.MethodGet}, {"HEAD", http.MethodHead}} {
						body := ""
						if call.method == http.MethodPut {
							body = "x"
						}
						rec := w.send(w.alice, call.method, "/plain/k", body)
						Expect(rec.Code).To(Equal(http.StatusOK), call.name)
						Expect(exactValues(rec.Header(), "x-amz-expiration")).To(Equal([]string{wantExpiry}), call.name)
						Expect(strings.Count(exactHeader(rec.Header(), "x-amz-expiration"), "expiry-date=")).To(Equal(1), "%s: the rule's own date alone", call.name)
					}
					copied := w.send(w.alice, http.MethodPut, "/plain/copied", "", "X-Amz-Copy-Source", "/plain/k")
					Expect(copied.Code).To(Equal(http.StatusOK), copied.Body.String())
					Expect(exactValues(copied.Header(), "x-amz-expiration")).To(Equal([]string{wantExpiry}), "CopyObject")
					upload, doc := w.partsUpload("m", nil, nil, []byte("x"))
					completed := w.send(w.alice, http.MethodPost, "/plain/m?uploadId="+upload, doc)
					Expect(completed.Code).To(Equal(http.StatusOK), completed.Body.String())
					Expect(exactValues(completed.Header(), "x-amz-expiration")).To(Equal([]string{wantExpiry}), "CompleteMultipartUpload")
					create := serveReq(w.handler(w.alice), http.MethodPost, "/plain/k?uploads", nil)
					Expect(create.Code).To(Equal(http.StatusOK), create.Body.String())
					Expect(exactValues(create.Header(), "x-amz-abort-rule-id")).To(Equal([]string{wantID}), "CreateMultipartUpload")
					listed := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+uploadIDPattern.FindStringSubmatch(create.Body.String())[1], "")
					Expect(listed.Code).To(Equal(http.StatusOK), listed.Body.String())
					Expect(exactValues(listed.Header(), "x-amz-abort-rule-id")).To(Equal([]string{wantID}), "ListParts")
				},
				Entry("a plain ID, unchanged", "rule1", "rule1"),
				Entry("spaces", "my rule  1", "my%20rule%20%201"),
				Entry("quotes", `say "hi"`, "say%20%22hi%22"),
				Entry("commas and equals signs", "a,b=c", "a%2Cb%3Dc"),
				Entry("a percent sign", "100%", "100%25"),
				Entry("non-ASCII", "caf\u00e9", "caf%C3%A9"),
				Entry("a byte that is not UTF-8", "a\xffb", "a%FFb"),
				Entry("NUL, tab, CR and LF", "a\x00\t\r\nb", "a%00%09%0D%0Ab"),
				Entry("DEL", "a\x7fb", "a%7Fb"),
				Entry("an ID that forges an expiry-date", `x", expiry-date="Fri, 01 Jan 2100 00:00:00 GMT`,
					"x%22%2C%20expiry-date%3D%22Fri%2C%2001%20Jan%202100%2000%3A00%3A00%20GMT"),
			)

			It("sends a stored rule ID's control bytes as %XX in both headers, which a strict client reads, starting no header of its own", func(ctx SpecContext) {
				const id = "a\r\nX-Injected: 1\x00\x7f"
				w.put(ctx, "k", "x", nil)
				w.storeLifecycle(ctx, lifecycle.Rule{
					ID: id, Status: "Enabled",
					Expiration: lifecycle.Expiration{Days: "1"}, MPExpiration: lifecycle.Expiration{Days: "1"},
				})
				srv := httptest.NewServer(w.handler(w.alice))
				DeferCleanup(srv.Close)
				do := func(method, target string) *http.Response {
					GinkgoHelper()
					req, err := http.NewRequestWithContext(ctx, method, srv.URL+target, nil)
					Expect(err).NotTo(HaveOccurred())
					resp, err := srv.Client().Do(req)
					Expect(err).NotTo(HaveOccurred())
					DeferCleanup(resp.Body.Close)
					Expect(resp.StatusCode).To(Equal(http.StatusOK))
					return resp
				}
				const wantID = "a%0D%0AX-Injected%3A%201%00%7F"
				head := do(http.MethodHead, "/plain/k")
				Expect(head.Header.Values("X-Amz-Expiration")).To(Equal([]string{`expiry-date="Wed, 30 Sep 2026 00:00:00 GMT", rule-id="` + wantID + `"`}))
				Expect(head.Header).NotTo(HaveKey("X-Injected"))
				create := do(http.MethodPost, "/plain/k?uploads")
				Expect(create.Header.Values("X-Amz-Abort-Rule-Id")).To(Equal([]string{wantID}))
				Expect(create.Header).NotTo(HaveKey("X-Injected"))
			})
		})
	}
})
