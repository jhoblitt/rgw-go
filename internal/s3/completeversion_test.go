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
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// radosgw's completion sends x-amz-version-id when version_id is not empty
// (rgw_rest_s3.cc:4081 at v19.2.6, :4609 at v20.2.4): the instance it
// generates on a bucket whose versioning is enabled, which rgw-go refuses
// with 501, or a system request's rgwx-version-id, which rgw-go does not
// read (docs/exclusions.md).
var _ = Describe("x-amz-version-id on CompleteMultipartUpload", func() {
	DescribeTable("is not sent for a completion on an unversioned bucket",
		func(ctx SpecContext, rel denc.Release) {
			w := newWriteWorld(ctx, rel)
			id := w.initUpload(w.alice, "/plain/k")
			Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
			rec := w.send(w.alice, http.MethodPost, "/plain/k?uploadId="+id, completion(md5Hex("x")))
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"))

			system := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return &op.AuthResult{Identity: op.Identity{
					User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: op.OpTypeAll, System: true, Admin: true,
				}}, nil
			})
			h := s3.NewHandler(w.env, system, testConfig(s3.Config{}))
			id = w.initUpload(w.alice, "/plain/j")
			Expect(serveReq(h, http.MethodPut, "/plain/j?uploadId="+id+"&partNumber=1", strings.NewReader("x")).Code).To(Equal(200))
			rec = serveReq(h, http.MethodPost, "/plain/j?uploadId="+id+"&rgwx-version-id=v1", strings.NewReader(completion(md5Hex("x"))))
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"), "radosgw echoes it; rgw-go reads no versioning system parameter")
		},
		Entry("Squid", denc.Squid),
		Entry("Tentacle", denc.Tentacle),
	)
})
