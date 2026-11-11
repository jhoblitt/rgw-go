package op_test

import (
	"context"
	"maps"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("CompleteMultipart of an upload radosgw began encrypted", func() {
	// withPart stores part 1, "x", of key's upload id in plain as a gateway
	// that takes the upload's mode for none stores it, and returns the
	// completion document naming it.
	withPart := func(ctx context.Context, f *writeFixture, key, id string) string {
		GinkgoHelper()
		up, err := f.upload(ctx, key, id)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.store.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Size: 1})
		Expect(err).NotTo(HaveOccurred())
		return completeDoc(xmlPart("1", md5Hex([]byte("x"))))
	}

	DescribeTable("refuses an upload of a crypt mode rgw-go does not know with NotImplemented on both releases, completing nothing",
		func(ctx SpecContext, mode string) {
			for _, rel := range bothReleases {
				f := newMPFixture(ctx, rel)
				id := f.encryptedUpload(ctx, "k", mode)
				doc := withPart(ctx, f, "k", id)
				err := op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", doc))
				Expect(err).To(MatchError(op.ErrNotImplemented), "release %d", rel)
				_, err = f.upload(ctx, "k", id)
				Expect(err).NotTo(HaveOccurred(), "release %d: the upload is kept", rel)
				st, err := f.store.StatObject(ctx, f.rec, meta.ObjKey{Name: "k"})
				Expect(err).NotTo(HaveOccurred(), "release %d", rel)
				Expect(st.Exists).To(BeFalse(), "release %d: no object is written", rel)
			}
		},
		Entry("a GCM mode v20.2.5 writes", "SSE-KMS-GCM"),
		Entry("a mode a NUL ends, which names no mode", "AES256\x00"),
	)

	It("asks the store to complete nothing, refusing before any write", func(ctx SpecContext) {
		for _, rel := range bothReleases {
			f := newMPFixture(ctx, rel)
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(&op.Upload{ID: "2~x", Attrs: radosgwCryptAttrs("RGW-AUTO-GCM")}, nil)
			f.env.Multipart = stub
			err := op.Run(ctx, &op.CompleteMultipart{UploadID: "2~x"}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", md5Hex([]byte("x"))))))
			Expect(err).To(MatchError(op.ErrNotImplemented), "release %d", rel)
			Expect(stub.GetUploadCallCount()).To(Equal(1), "release %d", rel)
			Expect(stub.CompleteCallCount()).To(BeZero(), "release %d", rel)
		}
	})

	DescribeTable("completes an upload without a crypt mode on both releases, as before",
		func(ctx SpecContext, attrs map[string][]byte) {
			for _, rel := range bothReleases {
				f := newMPFixture(ctx, rel)
				all := map[string][]byte{meta.AttrACL: f.aliceACL}
				maps.Copy(all, attrs)
				up, err := f.store.CreateUpload(ctx, f.rec, meta.ObjKey{Name: "k"}, op.UploadParams{
					Owner: f.alice.Owner, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: all,
				})
				Expect(err).NotTo(HaveOccurred())
				doc := withPart(ctx, f, "k", up.ID)
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: up.ID}, f.bodyReq(http.MethodPost, "k", doc))).To(Succeed(), "release %d", rel)
			}
		},
		Entry("no crypt attr", map[string][]byte(nil)),
		Entry("an empty mode, which radosgw takes for none", map[string][]byte{meta.AttrCryptMode: {}}),
	)

	It("leaves a completion retried after it succeeded to the store, which finds no upload", func(ctx SpecContext) {
		f := newMPFixture(ctx, bothReleases[1])
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
		stub.CompleteReturns(&op.PutResult{ETag: "e"}, nil)
		f.env.Multipart = stub
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: "2~x"}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "e"))))).To(Succeed())
		Expect(stub.CompleteCallCount()).To(Equal(1))
	})
})
