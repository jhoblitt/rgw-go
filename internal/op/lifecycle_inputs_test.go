package op_test

import (
	"errors"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var errUploadInitiated = errors.New("the store could not read the index entry")

// lifecycleAttr is a lifecycle configuration holding rules, as the bucket
// stores it.
func lifecycleAttr(rules ...lifecycle.Rule) map[string][]byte {
	c := lifecycle.Configuration{Rules: rules}
	e := denc.NewEncoder()
	c.Encode(e, denc.Squid)
	return map[string][]byte{op.AttrLC: e.Bytes()}
}

var _ = Describe("what the ops report for the lifecycle headers", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newMPFixture(ctx, denc.Squid) })

	Describe("ObjectState.OrigSize", func() {
		It("is a compressed object's uncompressed size, as the worker's size filter reads it", func() {
			st := &op.ObjectState{Size: 10, Compression: &meta.CompressionInfo{Type: "zlib", OrigSize: 4096}}
			Expect(st.OrigSize()).To(Equal(uint64(4096)))
		})
		It("is any other object's size", func() {
			Expect((&op.ObjectState{Size: 10}).OrigSize()).To(Equal(uint64(10)))
		})
	})

	Describe("CopyObject", func() {
		It("reports the destination's tags and size before compression as the store gives them", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			stub.PrefetchObjectReturns(&op.ObjectState{Exists: true, Size: 5, Attrs: map[string][]byte{meta.AttrETag: []byte("e")}}, nil)
			stub.CopyObjectReturns(&op.PutResult{ETag: "e", Size: 5, Mtime: smallMtime(), Tags: []byte("k=v\x00"), OrigSize: 9}, nil)
			f.env.Objects = stub
			o := f.copyOf("src")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "dst"))).To(Succeed())
			Expect([]any{o.Tags, o.OrigSize}).To(Equal([]any{[]byte("k=v\x00"), uint64(9)}))
		})
	})

	Describe("CompleteMultipart", func() {
		It("reports the completed object's tags and size before compression as the store gives them", func(ctx SpecContext) {
			stub := &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(nil, op.ErrNoSuchUpload)
			stub.CompleteReturns(&op.PutResult{ETag: "e-1", Size: 5, Mtime: smallMtime(), Tags: []byte("k=v\x00"), OrigSize: 9}, nil)
			f.env.Multipart = stub
			body := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"` + md5Hex([]byte("x")) + `"</ETag></Part></CompleteMultipartUpload>`
			o := &op.CompleteMultipart{UploadID: "2~u"}
			Expect(op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", body))).To(Succeed())
			Expect([]any{o.Tags, o.OrigSize}).To(Equal([]any{[]byte("k=v\x00"), uint64(9)}))
		})
	})

	Describe("ListParts", func() {
		var initiated time.Time
		aborting := func(id, prefix string) lifecycle.Rule {
			return lifecycle.Rule{ID: id, Prefix: prefix, Status: "Enabled", MPExpiration: lifecycle.Expiration{Days: "1"}}
		}
		var (
			stub *opfakes.FakeMultipartStore
			id   string
		)
		BeforeEach(func() {
			initiated = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
			id = "2~u"
			stub = &opfakes.FakeMultipartStore{}
			stub.GetUploadReturns(&op.Upload{ID: id, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}}, nil)
			stub.UploadInitiatedReturns(initiated, nil)
			f.env.Multipart = stub
		})
		list := func(ctx SpecContext) (*op.ListParts, error) {
			GinkgoHelper()
			o := &op.ListParts{UploadID: id, MaxParts: 10}
			return o, op.Run(ctx, o, f.req(http.MethodGet, "plain", "k"))
		}

		It("reads the upload's initiation for a bucket whose lifecycle aborts uploads of the key", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, lifecycleAttr(aborting("a", "")))
			o, err := list(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Initiated).To(Equal(initiated))
			Expect(stub.UploadInitiatedCallCount()).To(Equal(1))
			_, up := stub.UploadInitiatedArgsForCall(0)
			Expect(up.ID).To(Equal(id), "the upload GetUpload read")
		})

		DescribeTable("reads no initiation where no abort header could follow",
			func(ctx SpecContext, set func() map[string][]byte) {
				if attrs := set(); attrs != nil {
					f.setBucketAttrs(ctx, attrs)
				}
				o, err := list(ctx)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.Initiated).To(BeZero())
				Expect(stub.UploadInitiatedCallCount()).To(BeZero())
			},
			Entry("a bucket without a lifecycle configuration", func() map[string][]byte { return nil }),
			Entry("a configuration whose rules abort no upload", func() map[string][]byte {
				return lifecycleAttr(lifecycle.Rule{ID: "e", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"}})
			}),
			Entry("an abort rule of another prefix", func() map[string][]byte { return lifecycleAttr(aborting("a", "logs/")) }),
			Entry("a disabled abort rule", func() map[string][]byte {
				r := aborting("a", "")
				r.Status = "Disabled"
				return lifecycleAttr(r)
			}),
			Entry("a configuration that does not decode", func() map[string][]byte { return map[string][]byte{op.AttrLC: {1}} }),
		)

		It("is the zero time for an upload whose index entry the store does not find", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, lifecycleAttr(aborting("a", "")))
			stub.UploadInitiatedReturns(time.Time{}, nil)
			o, err := list(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Initiated).To(BeZero())
			Expect(stub.UploadInitiatedCallCount()).To(Equal(1))
		})

		It("fails with the store's error when the initiation cannot be read", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, lifecycleAttr(aborting("a", "")))
			stub.UploadInitiatedReturns(time.Time{}, errUploadInitiated)
			_, err := list(ctx)
			Expect(err).To(MatchError(errUploadInitiated))
		})
	})
})
