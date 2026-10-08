package s3_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = Describe("WriteError", func() {
	const header = `<?xml version="1.0" encoding="UTF-8"?>`
	var env *op.Env
	BeforeEach(func() { env = &op.Env{HostID: "4155-z-zg"} })
	write := func(ctx context.Context, r *op.Request, err error) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s3.WriteError(ctx, rec, r, err)
		return rec
	}
	It("renders rgw_err's dump with every field the request carries", func(ctx SpecContext) {
		rec := write(ctx, &op.Request{ID: "tx1", Bucket: "b", Env: env}, op.ErrNoSuchBucket)
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(Equal(header + `<Error><Code>NoSuchBucket</Code><Message></Message>` +
			`<BucketName>b</BucketName><RequestId>tx1</RequestId><HostId>4155-z-zg</HostId></Error>`))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(exactHeader(rec.Header(), "x-amz-request-id")).To(Equal("tx1"))
	})
	It("leaves out an empty code, bucket and request id but always writes the message and host id", func(ctx SpecContext) {
		rec := write(ctx, &op.Request{Env: env}, &op.Error{Status: 400})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(Equal(header + `<Error><Message></Message><HostId>4155-z-zg</HostId></Error>`))
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-request-id"), "dump_trans_id skips an empty id")
	})
	It("escapes every field as dump_string does", func(ctx SpecContext) {
		rec := write(ctx, &op.Request{ID: "tx'1", Bucket: "a&b", Env: &op.Env{HostID: `h"1`}}, op.ErrNoSuchKey.WithMessage("k\x01<"))
		Expect(rec.Body.String()).To(Equal(header + `<Error><Code>NoSuchKey</Code><Message>k&#x01;&lt;</Message>` +
			`<BucketName>a&amp;b</BucketName><RequestId>tx&apos;1</RequestId><HostId>h&quot;1</HostId></Error>`))
	})
	DescribeTable("renders an error without an op.Error as AsError maps it",
		func(ctx context.Context, err error, status int, code string) {
			rec := write(ctx, &op.Request{ID: "tx1", Env: env}, err)
			Expect(rec.Code).To(Equal(status))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>" + code + "</Code>"))
		},
		Entry("a plain error, an op's bug", errors.New("boom"), 500, "InternalError"),
		Entry("a canceled request", context.Canceled, 408, "RequestTimeout"),
		Entry("a wrapped op.Error", errors.Join(errors.New("cause"), op.ErrNoSuchUpload), 404, "NoSuchUpload"),
	)
	It("sends a status radosgw does not count as an error without a document", func(ctx SpecContext) {
		rec := write(ctx, &op.Request{ID: "tx1", Env: env}, op.ErrNotModified)
		Expect(rec.Code).To(Equal(http.StatusNotModified))
		Expect(rec.Body.Len()).To(BeZero(), "rgw_err::is_err is false for 200-399, so end_header dumps no error")
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"))
		Expect(exactHeader(rec.Header(), "x-amz-request-id")).To(Equal("tx1"))
	})
})
