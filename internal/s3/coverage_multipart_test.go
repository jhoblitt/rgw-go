package s3_test

import (
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// docs/differences.md, "The multipart ops after UploadPart check no storage
// class": radosgw refuses a storage class the bucket's placement lacks with
// 400 InvalidArgument on every S3 request; rgw-go checks it where the
// upload or the part keeps the class.
var _ = Describe("the multipart ops after UploadPart, as docs/differences.md records them", func() {
	var (
		w  *writeWorld
		id string
	)
	const nope = "NOPE"
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, denc.Squid)
		id = w.initUpload(w.alice, "/plain/k")
		rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x")
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
	})
	It("serve ListParts naming a storage class the placement lacks", func() {
		rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "", "X-Amz-Storage-Class", nope)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
	})
	It("serve ListMultipartUploads naming a storage class the placement lacks", func() {
		rec := w.send(w.alice, http.MethodGet, "/plain?uploads", "", "X-Amz-Storage-Class", nope)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
	})
	It("serve CompleteMultipartUpload naming a storage class the placement lacks", func() {
		rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")), "X-Amz-Storage-Class", nope)
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
	})
	It("serve AbortMultipartUpload naming a storage class the placement lacks", func() {
		rec := w.send(w.alice, http.MethodDelete, "/plain/k?uploadId="+id, "", "X-Amz-Storage-Class", nope)
		Expect(rec.Code).To(Equal(http.StatusNoContent), rec.Body.String())
	})
	It("refuse UploadPart naming it, which keeps the upload's class, with InvalidArgument", func() {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=2", "y", "X-Amz-Storage-Class", nope), 400, "InvalidArgument")
	})
})
