package s3_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// docs/differences.md, "A system user's listing carries no sync fields":
// radosgw adds RgwxTag to each entry, and VersionedEpoch and RgwxMtime to
// each version, of a system user's listing, for multisite sync.
var _ = Describe("a system user's listing, as docs/differences.md records it", func() {
	DescribeTable("renders no sync field, as any other user's listing",
		func(ctx SpecContext, target string) {
			w := newWriteWorld(ctx, denc.Squid)
			system := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				return &op.AuthResult{Identity: op.Identity{
					User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: op.OpTypeAll, System: true,
				}}, nil
			})
			rec := serveReq(s3.NewHandler(w.env, system, testConfig(s3.Config{})), http.MethodGet, target, nil)
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Key>src</Key>"))
			for _, field := range []string{"RgwxTag", "VersionedEpoch", "RgwxMtime"} {
				Expect(rec.Body.String()).NotTo(ContainSubstring(field), target)
			}
			plain := w.send(w.alice, http.MethodGet, target, "")
			Expect(rec.Body.String()).To(Equal(plain.Body.String()), "the same document a non-system user gets")
		},
		Entry("ListObjects", "/plain"),
		Entry("ListObjectsV2", "/plain?list-type=2"),
		Entry("ListObjectVersions", "/plain?versions"),
	)
})
