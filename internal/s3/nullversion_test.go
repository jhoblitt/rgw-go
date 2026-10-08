package s3_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// nullVersionOf is target naming the null version.
func nullVersionOf(target string) string {
	if strings.Contains(target, "?") {
		return target + "&versionId=null"
	}
	return target + "?versionId=null"
}

// comparableHeaders is rec's headers without those that name the request.
func comparableHeaders(rec *httptest.ResponseRecorder) http.Header {
	h := rec.Header().Clone()
	delete(h, "x-amz-request-id")
	for _, k := range []string{"X-Amz-Id-2", "Date"} {
		h.Del(k)
	}
	return h
}

// objectView is what a client can learn of st: whether it exists, its size,
// its mtime and its attrs. The bucket record st carries differs between two
// stores in its version tags.
func objectView(st *op.ObjectState) op.ObjectState {
	return op.ObjectState{Exists: st.Exists, Size: st.Size, Mtime: st.Mtime, Attrs: st.Attrs}
}

var _ = Describe("a request naming the null version", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			tagged := func(ctx SpecContext, w *writeWorld) {
				set := tags.Set{}
				Expect(set.Add("a", "1", tags.MaxObjectTags)).To(Succeed())
				w.put(ctx, "src", "hello", map[string][]byte{tags.Attr: encodeTags(set)})
			}

			DescribeTable("on a bucket whose versioning was never enabled is answered as the same request naming no version",
				func(ctx SpecContext, method, target, body string, hdr []string, version string) {
					plain, null := newWriteWorld(ctx, rel), newWriteWorld(ctx, rel)
					tagged(ctx, plain)
					tagged(ctx, null)
					before := objectView(plain.stat(ctx, "src"))
					want := plain.send(plain.alice, method, target, body, hdr...)
					got := null.send(null.alice, method, nullVersionOf(target), body, hdr...)
					Expect(want.Code).To(BeNumerically("<", 300), want.Body.String())
					Expect(got.Code).To(Equal(want.Code), got.Body.String())
					Expect(got.Body.String()).To(Equal(want.Body.String()))
					if version == "" {
						Expect(got.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"))
					} else {
						Expect(got.Header()).To(HaveKeyWithValue("x-amz-version-id", []string{version}))
					}
					delete(got.Header(), "x-amz-version-id")
					Expect(comparableHeaders(got)).To(Equal(comparableHeaders(want)))
					after := objectView(plain.stat(ctx, "src"))
					Expect(objectView(null.stat(ctx, "src"))).To(Equal(after))
					if method != http.MethodGet && method != http.MethodHead {
						Expect(after).NotTo(Equal(before), "the write changes the object")
					}
				},
				Entry("GetObject, which names the version it read", http.MethodGet, "/plain/src", "", nil, "null"),
				Entry("HeadObject, which names the version it read", http.MethodHead, "/plain/src", "", nil, "null"),
				Entry("GetObjectAcl", http.MethodGet, "/plain/src?acl", "", nil, ""),
				Entry("PutObjectAcl", http.MethodPut, "/plain/src?acl", "", []string{"x-amz-acl", "public-read", "Content-Length", "0"}, ""),
				Entry("GetObjectTagging", http.MethodGet, "/plain/src?tagging", "", nil, ""),
				Entry("PutObjectTagging", http.MethodPut, "/plain/src?tagging",
					"<Tagging><TagSet><Tag><Key>b</Key><Value>2</Value></Tag></TagSet></Tagging>", nil, ""),
				Entry("DeleteObjectTagging", http.MethodDelete, "/plain/src?tagging", "", nil, ""),
				Entry("DeleteObject, whose unversioned delete names no version", http.MethodDelete, "/plain/src", "", nil, ""),
			)
			It("on a bucket whose versioning was never enabled deletes the plain key in a DeleteObjects, naming the version it was asked for", func(ctx SpecContext) {
				w := newWriteWorld(ctx, rel)
				rec := w.send(w.alice, http.MethodPost, "/plain?delete", deleteDoc("<Key>src</Key><VersionId>null</VersionId>"))
				Expect(rec.Code).To(Equal(200))
				Expect(rec.Body.String()).To(Equal(deleteResultOpen+"<Deleted><Key>src</Key><VersionId>null</VersionId></Deleted></DeleteResult>"),
					"send_partial_response names the key's instance, rgw_rest_s3.cc:4293-4295 at v19.2.6")
				Expect(w.stat(ctx, "src").Exists).To(BeFalse())
			})
			DescribeTable("on a bucket whose versioning was never enabled authorizes a DeleteObjects key for s3:DeleteObjectVersion",
				func(ctx SpecContext, action string, deleted bool) {
					w := newWriteWorld(ctx, rel)
					policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},` +
						`"Action":"` + action + `","Resource":"arn:aws:s3:::plain/*"}]}`
					Expect(w.send(w.alice, http.MethodPut, "/plain?policy", policy).Code).To(Equal(204))
					rec := w.send(w.bob, http.MethodPost, "/plain?delete", deleteDoc("<Key>src</Key><VersionId>null</VersionId>"))
					Expect(rec.Code).To(Equal(200), rec.Body.String())
					if deleted {
						Expect(rec.Body.String()).To(Equal(deleteResultOpen + "<Deleted><Key>src</Key><VersionId>null</VersionId></Deleted></DeleteResult>"))
					} else {
						Expect(rec.Body.String()).To(ContainSubstring("<Key>src</Key><VersionId>null</VersionId><Code>AccessDenied</Code>"))
					}
					Expect(w.stat(ctx, "src").Exists).To(Equal(!deleted))
				},
				Entry("refusing a requester allowed s3:DeleteObject alone, as rgw_op.cc:6829-6831 at v19.2.6 does", "s3:DeleteObject", false),
				Entry("deleting for a requester allowed s3:DeleteObjectVersion alone", "s3:DeleteObjectVersion", true),
			)
			DescribeTable("on a bucket whose versioning is or was enabled is answered 501 and changes nothing",
				func(ctx SpecContext, flags uint32) {
					w := newWriteWorld(ctx, rel)
					w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= flags })
					expectStatusLine(w.send(w.alice, http.MethodPost, "/plain?delete", deleteDoc("<Key>src</Key><VersionId>null</VersionId>")), 501)
					Expect(w.send(w.alice, http.MethodDelete, "/plain/src?versionId=null", "").Code).To(Equal(501))
					Expect(w.stat(ctx, "src").Exists).To(BeTrue())
				},
				Entry("enabled", uint32(meta.BucketVersioned)),
				Entry("suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
			)
		})
	}
})
