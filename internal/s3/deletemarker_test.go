package s3_test

import (
	"context"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// failingStats is an object store whose StatObject of key k fails.
type failingStats struct {
	op.ObjectStore
	k string
}

func (o failingStats) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	if key.Name == o.k {
		return nil, op.ErrServiceUnavailable
	}
	return o.ObjectStore.StatObject(ctx, rec, key)
}

// Tentacle's read_permissions adds x-amz-delete-marker to a GET or HEAD whose
// object policies were refused with -ENOENT, naming whether the key's current
// version is a delete marker
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1945 at v20.2.4);
// Squid's adds nothing
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1893-L1933 at v19.2.6).
var _ = Describe("x-amz-delete-marker on a missing object", func() {
	Context("on Tentacle", func() {
		var w *subresWorld
		BeforeEach(func(ctx SpecContext) {
			w = newSubresWorld(ctx, denc.Tentacle)
			_, err := w.store.PutObject(ctx, w.bucket(ctx), meta.ObjKey{Name: "here"}, strings.NewReader("x"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
		})
		DescribeTable("sends false where read_permissions finds no object",
			func(method, target string, hdr ...string) {
				rec := w.send(w.alice, method, target, "", hdr...)
				Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
				var lines []string
				for k, vs := range rec.Header() {
					if strings.EqualFold(k, "x-amz-delete-marker") {
						for _, v := range vs {
							lines = append(lines, k+": "+v)
						}
					}
				}
				Expect(lines).To(Equal([]string{"x-amz-delete-marker: false"}), "radosgw's spelling, once")
			},
			Entry("GET", http.MethodGet, "/plain/nope"),
			Entry("HEAD", http.MethodHead, "/plain/nope"),
			Entry("GET ?acl", http.MethodGet, "/plain/nope?acl"),
			Entry("GET ?tagging", http.MethodGet, "/plain/nope?tagging"),
			Entry("GET naming the null version", http.MethodGet, "/plain/nope?versionId=null"),
			Entry("HEAD naming a version", http.MethodHead, "/plain/nope?versionId=v1"),
			Entry("GET ?attributes, Tentacle's GetObjectAttributes", http.MethodGet, "/plain/nope?attributes", "X-Amz-Object-Attributes", "ETag"),
			Entry("ListParts of a missing upload, whose key does not exist", http.MethodGet, "/plain/nope?uploadId=2~nope"),
			Entry("ListParts of a missing upload, whose key exists", http.MethodGet, "/plain/here?uploadId=2~nope"),
		)
		It("sends false when the stat of ListParts' key fails, as radosgw ignores the load's failure", func(ctx SpecContext) {
			w.env.Objects = failingStats{ObjectStore: w.env.Objects, k: "unread"}
			rec := w.send(w.alice, http.MethodGet, "/plain/unread?uploadId=2~nope", "")
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(exactHeader(rec.Header(), "x-amz-delete-marker")).To(Equal("false"))
		})
		It("sends none to a requester read_obj_policy refuses as one who may not list", func() {
			rec := w.send(w.bob, http.MethodGet, "/plain/nope", "")
			Expect(rec.Code).To(Equal(http.StatusForbidden), rec.Body.String())
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
		})
		It("sends none for an object that exists, a missing bucket, or a method other than GET and HEAD", func() {
			Expect(w.send(w.alice, http.MethodGet, "/plain/here", "").Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
			rec := w.send(w.alice, http.MethodGet, "/nobucket/nope", "")
			Expect(rec.Code).To(Equal(http.StatusNotFound))
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), "-ERR_NO_SUCH_BUCKET is not -ENOENT")
			rec = w.send(w.alice, http.MethodPut, "/plain/nope?tagging", "<Tagging><TagSet></TagSet></Tagging>")
			Expect(rec.Code).To(Equal(http.StatusNotFound), rec.Body.String())
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
		})
		It("sends none for a NoSuchKey execute finds, after read_permissions", func(ctx SpecContext) {
			rw := newReadWorld(ctx, denc.Tentacle)
			rec := send(rw.handler(rw.env(nil), rw.alice), http.MethodGet, "/plain/nope", nil)
			Expect(rec.Code).To(Equal(http.StatusNotFound), "OwnerOnly authorizes, and RGWGetObj::execute finds no object")
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
		})
	})
	It("is not sent on Squid", func(ctx SpecContext) {
		w := newSubresWorld(ctx, denc.Squid)
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			rec := w.send(w.alice, m, "/plain/nope", "")
			Expect(rec.Code).To(Equal(http.StatusNotFound))
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"), m)
		}
		rec := w.send(w.alice, http.MethodGet, "/plain/nope?uploadId=2~nope", "")
		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
	})
})
