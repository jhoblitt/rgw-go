package conformance

import (
	"context"
	"maps"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // Ginkgo's DSL is meant to be dot-imported
	. "github.com/onsi/gomega"    //nolint:revive // Gomega's DSL is meant to be dot-imported

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// RunLifecycleInputs declares the store-conformance specs of what a copy, a
// completion and an upload report for the lifecycle response headers,
// against Run's kind of Env factory: the tags attr and the size before
// compression of the object a copy or a completion wrote, and the initiation
// of an upload, which no part moves.
func RunLifecycleInputs(newEnv func(ctx context.Context) (*op.Env, func())) {
	Describe("lifecycle header inputs conformance", func() {
		const tagged = "key1=tag1&key5=tag5\x00"
		var f checksumFixture
		f.beforeEach(newEnv)

		DescribeTable("reports the tags attr and the size of the object a copy wrote: the source's under COPY and the request's under REPLACE",
			func(ctx SpecContext, replace bool, request map[string][]byte, want []byte) {
				const body = "a source to copy"
				_, err := f.env.Objects.PutObject(ctx, f.rec, meta.ObjKey{Name: "src"}, strings.NewReader(body),
					op.PutParams{Size: int64(len(body)), Attrs: map[string][]byte{meta.AttrACL: f.acl(), tags.Attr: []byte(tagged)}})
				Expect(err).NotTo(HaveOccurred(), "putting the source")
				src, err := f.env.Objects.PrefetchObject(ctx, f.rec, meta.ObjKey{Name: "src"})
				Expect(err).NotTo(HaveOccurred())
				attrs := map[string][]byte{meta.AttrACL: f.acl()}
				maps.Copy(attrs, request)
				res, err := f.env.Objects.CopyObject(ctx, src, f.rec, meta.ObjKey{Name: "dst"}, op.CopyParams{Attrs: attrs, ReplaceAttrs: replace})
				src.ReleaseHead()
				Expect(err).NotTo(HaveOccurred(), "copying")
				Expect(res.Tags).To(Equal(want), "the result")
				Expect(res.OrigSize).To(BeEquivalentTo(len(body)), "the result's size before compression")
				Expect(f.stat(ctx, "dst").Attrs[tags.Attr]).To(Equal(want), "what the head stores")
			},
			Entry("COPY", false, nil, []byte(tagged)),
			Entry("REPLACE without tags", true, nil, nil),
			Entry("REPLACE with the request's tags", true, map[string][]byte{tags.Attr: []byte("a=b\x00")}, []byte("a=b\x00")),
		)

		It("reports the tags attr and the size of the object a completion wrote, and of the object a completion retried after it succeeded finds", func(ctx SpecContext) {
			up, err := f.env.Multipart.CreateUpload(ctx, f.rec, meta.ObjKey{Name: "mp"}, op.UploadParams{
				Owner: f.owner(), OwnerName: f.u.Info.DisplayName, Placement: f.rec.Info.PlacementRule,
				Attrs: map[string][]byte{meta.AttrACL: f.acl(), tags.Attr: []byte(tagged)},
			})
			Expect(err).NotTo(HaveOccurred(), "creating the upload")
			DeferCleanup(func(ctx context.Context) {
				if cur, gerr := f.env.Multipart.GetUpload(ctx, f.rec, up.Key, up.ID); gerr == nil {
					Expect(f.env.Multipart.Abort(ctx, cur)).To(Succeed(), "aborting the upload")
				}
			})
			part := []byte("the one part")
			r := f.putPart(ctx, up, 1, part, nil)
			parts := []op.CompletePart{{Number: 1, ETag: r.ETag}}
			first, err := f.env.Multipart.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred(), "completing")
			Expect(first.Tags).To(Equal([]byte(tagged)), "the completion")
			Expect(first.OrigSize).To(BeEquivalentTo(len(part)), "the completion's size before compression")
			Expect(f.stat(ctx, "mp").Attrs[tags.Attr]).To(Equal(first.Tags), "what the head stores")

			retried, err := f.env.Multipart.Complete(ctx, up, parts)
			Expect(err).NotTo(HaveOccurred(), "completing again")
			Expect([]any{retried.Tags, retried.OrigSize}).To(Equal([]any{first.Tags, first.OrigSize}), "the retry")
		})

		It("reports an upload's initiation as the time CreateUpload reported, which a part does not move, and the zero time once the upload is gone", func(ctx SpecContext) {
			up := f.newUpload(ctx, op.UploadChecksum{})
			f.putPart(ctx, up, 1, []byte("a part"), nil)
			got, err := f.env.Multipart.GetUpload(ctx, f.rec, up.Key, up.ID)
			Expect(err).NotTo(HaveOccurred())
			initiated, err := f.env.Multipart.UploadInitiated(ctx, got)
			Expect(err).NotTo(HaveOccurred())
			Expect(initiated).To(BeTemporally("~", up.Initiated, time.Millisecond), "the initiation the create reported")

			Expect(f.env.Multipart.Abort(ctx, got)).To(Succeed())
			Eventually(func(g Gomega) {
				gone, ierr := f.env.Multipart.UploadInitiated(ctx, got)
				g.Expect(ierr).NotTo(HaveOccurred())
				g.Expect(gone).To(BeZero())
			}).WithContext(ctx).WithTimeout(30*time.Second).WithPolling(100*time.Millisecond).Should(Succeed(), "an upload that is gone has no entry")
		})
	})
}
