package op_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// uploadPart stores body as part n of key's upload id and returns its ETag.
func (f *writeFixture) uploadPart(ctx context.Context, key, id string, n int, body []byte) string {
	GinkgoHelper()
	o := f.partOf(id, n, "")
	o.Body, o.Size = bytes.NewReader(body), int64(len(body))
	Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", key))).To(Succeed())
	return o.ETag
}

var _ = Describe("the multipart ops", func() {
	It("run a whole upload, a copied part included, and log no usage", func(ctx SpecContext) {
		f := newMPFixture(ctx, denc.Squid)
		init := &op.InitMultipart{Attrs: map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")}, ACL: acl.DefaultPolicy(f.alice.Owner, "Alice")}
		Expect(op.Run(ctx, init, f.req(http.MethodPost, "plain", "k"))).To(Succeed())

		p1 := bytes.Repeat([]byte("a"), 5<<20)
		etag1 := f.uploadPart(ctx, "k", init.UploadID, 1, p1)
		Expect(etag1).To(Equal(md5Hex(p1)))

		f.put(ctx, "src", bytes.Repeat([]byte("s"), 8<<20))
		up2 := f.copyPartOf(init.UploadID, 2, "src", "bytes=1048576-3145727")
		Expect(op.Run(ctx, up2, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(up2.ETag).To(Equal(md5Hex(bytes.Repeat([]byte("s"), 2<<20))))

		lp := &op.ListParts{UploadID: init.UploadID, MaxParts: 1000}
		Expect(op.Run(ctx, lp, f.req(http.MethodGet, "plain", "k"))).To(Succeed())
		Expect(lp.Result.Parts).To(HaveLen(2))
		Expect(lp.Result.NextMarker).To(Equal(2))
		Expect(lp.Owner.ID).To(Equal("alice"))
		Expect(lp.StorageClass).To(Equal(meta.StorageClassStandard))

		lu := &op.ListMultipartUploads{MaxUploads: 1000}
		Expect(op.Run(ctx, lu, f.req(http.MethodGet, "plain", ""))).To(Succeed())
		Expect(lu.Result.Uploads).To(HaveLen(1))
		Expect(lu.Result.Uploads[0].ID).To(Equal(init.UploadID))

		doc := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>2</PartNumber><ETag>"%s"</ETag></Part><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, up2.ETag, etag1)
		cm := &op.CompleteMultipart{UploadID: init.UploadID}
		Expect(op.Run(ctx, cm, f.bodyReq(http.MethodPost, "k", doc))).To(Succeed())
		Expect(cm.Parts).To(Equal([]op.CompletePart{{Number: 1, ETag: etag1}, {Number: 2, ETag: `"` + up2.ETag + `"`}}))
		Expect(cm.ETag).To(HaveSuffix("-2"))
		Expect(cm.Size).To(BeEquivalentTo(7 << 20))
		st := f.stat(ctx, "k")
		Expect(st.Exists).To(BeTrue())
		Expect(st.ETag).To(Equal(cm.ETag))
		Expect(st.WriteTag).To(Equal(writeReqID), "the request id is the completed head's tag")
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")), "the upload's attrs")

		lu = &op.ListMultipartUploads{MaxUploads: 1000}
		Expect(op.Run(ctx, lu, f.req(http.MethodGet, "plain", ""))).To(Succeed())
		Expect(lu.Result.Uploads).To(BeEmpty())

		init2 := &op.InitMultipart{ACL: acl.DefaultPolicy(f.alice.Owner, "Alice")}
		Expect(op.Run(ctx, init2, f.req(http.MethodPost, "plain", "k2"))).To(Succeed())
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: init2.UploadID}, f.req(http.MethodDelete, "plain", "k2"))).To(Succeed())
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the ops")
	})
})

var _ = Describe("the multipart writes for a requester refused permission", func() {
	DescribeTable("answer AccessDenied whether or not the upload exists, so its existence does not leak",
		func(ctx SpecContext, build func(id string) (op.Op, string)) {
			f := newMPFixture(ctx, denc.Squid)
			id := f.initUpload(ctx, "k")
			for _, uploadID := range []string{id, "2~nope"} {
				o, method := build(uploadID)
				r := f.bodyReq(method, "k", completeDoc(xmlPart("1", "x")))
				r.Identity = f.bob
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied), "upload %q", uploadID)
			}
		},
		Entry("UploadPart", func(id string) (op.Op, string) {
			return &op.UploadPart{UploadID: id, PartNumber: 1, Body: failingReader{}, Size: 1}, http.MethodPut
		}),
		Entry("CompleteMultipartUpload", func(id string) (op.Op, string) { return &op.CompleteMultipart{UploadID: id}, http.MethodPost }),
		Entry("AbortMultipartUpload", func(id string) (op.Op, string) { return &op.AbortMultipart{UploadID: id}, http.MethodDelete }),
	)
})

// metaNamed is a multipart store that finds an upload by its meta object's
// name, "<key>.<id>.meta", as radosgw's RGWMPObj names it, so an id holding
// a dot reaches another key's upload: key "a" with id "b.2~X" is key "a.b"'s
// upload "2~X".
type metaNamed struct{ op.MultipartStore }

func (m metaNamed) name(key meta.ObjKey, id string) (meta.ObjKey, string) {
	if i := strings.LastIndex(id, "."); i >= 0 {
		key.Name, id = key.Name+"."+id[:i], id[i+1:]
	}
	return key, id
}

func (m metaNamed) up(up *op.Upload) *op.Upload {
	c := *up
	c.Key, c.ID = m.name(up.Key, up.ID)
	return &c
}

func (m metaNamed) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, id string) (*op.Upload, error) {
	key, id = m.name(key, id)
	return m.MultipartStore.GetUpload(ctx, rec, key, id)
}

func (m metaNamed) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	return m.MultipartStore.PutPart(ctx, m.up(up), n, body, p)
}

func (m metaNamed) CopyPart(ctx context.Context, up *op.Upload, n int, src *op.ObjectState, rng op.ByteRange) (*op.PartResult, error) {
	return m.MultipartStore.CopyPart(ctx, m.up(up), n, src, rng)
}

func (m metaNamed) ListParts(ctx context.Context, up *op.Upload, marker, maxParts int) (op.ListPartsResult, error) {
	return m.MultipartStore.ListParts(ctx, m.up(up), marker, maxParts)
}

func (m metaNamed) Complete(ctx context.Context, up *op.Upload, parts []op.CompletePart) (*op.PutResult, error) {
	return m.MultipartStore.Complete(ctx, m.up(up), parts)
}

func (m metaNamed) Abort(ctx context.Context, up *op.Upload) error {
	return m.MultipartStore.Abort(ctx, m.up(up))
}

var _ = Describe("an upload id holding a dot", func() {
	var (
		f        *writeFixture
		id, etag string
	)
	BeforeEach(func(ctx SpecContext) {
		f = newMPFixture(ctx, denc.Squid)
		f.env.Multipart = metaNamed{f.store}
		id = f.initUpload(ctx, "a.b")
		etag = f.uploadPart(ctx, "a.b", id, 1, []byte("secret"))
	})
	// untouched checks that key a.b's upload still holds its one part.
	untouched := func(ctx context.Context) {
		GinkgoHelper()
		Expect(f.parts(ctx, "a.b", id)).To(ConsistOf(HaveField("ETag", etag)))
		Expect(f.stat(ctx, "a").Exists).To(BeFalse())
	}

	It("cannot replace another key's part through UploadPart", func(ctx SpecContext) {
		o := f.partOf("b."+id, 1, "")
		o.Body, o.Size = strings.NewReader("x"), 1
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		untouched(ctx)
	})
	It("cannot replace another key's part through UploadPartCopy", func(ctx SpecContext) {
		f.put(ctx, "src", []byte("other"))
		Expect(op.Run(ctx, f.copyPartOf("b."+id, 1, "src", ""), f.req(http.MethodPut, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		untouched(ctx)
	})
	It("cannot complete another key's upload into the request's key", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: "b." + id}, f.bodyReq(http.MethodPost, "a", completeDoc(xmlPart("1", etag))))).To(MatchError(op.ErrNoSuchUpload))
		untouched(ctx)
	})
	It("cannot abort another key's upload", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: "b." + id}, f.req(http.MethodDelete, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		untouched(ctx)
	})
	It("cannot list another key's parts, and answers as for a missing upload", func(ctx SpecContext) {
		o := &op.ListParts{UploadID: "b." + id, MaxParts: 10}
		Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(o.Result.Parts).To(BeEmpty())
		Expect(o.Upload).To(BeNil())
		missing := op.Run(ctx, &op.ListParts{UploadID: "2~nope", MaxParts: 10}, f.req(http.MethodGet, "plain", "a"))
		Expect(missing).To(MatchError(op.ErrNoSuchUpload))
	})
	It("reaches no store for any op", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.GetUploadReturns(&op.Upload{ID: "x"}, nil)
		stub.PutPartReturns(&op.PartResult{}, nil)
		stub.CopyPartReturns(&op.PartResult{}, nil)
		stub.CompleteReturns(&op.PutResult{}, nil)
		f.env.Multipart = stub
		f.put(ctx, "src", []byte("other"))
		Expect(op.Run(ctx, f.partOf("b."+id, 1, "x"), f.req(http.MethodPut, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(op.Run(ctx, f.copyPartOf("b."+id, 1, "src", ""), f.req(http.MethodPut, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: "b." + id}, f.bodyReq(http.MethodPost, "a", completeDoc(xmlPart("1", etag))))).To(MatchError(op.ErrNoSuchUpload))
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: "b." + id}, f.req(http.MethodDelete, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(op.Run(ctx, &op.ListParts{UploadID: "b." + id, MaxParts: 10}, f.req(http.MethodGet, "plain", "a"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(stub.Invocations()).To(BeEmpty())
	})
})

var _ = Describe("CompleteMultipart", func() {
	var (
		f  *writeFixture
		id string
	)
	BeforeEach(func(ctx SpecContext) {
		f = newMPFixture(ctx, denc.Squid)
		id = f.initUpload(ctx, "k")
	})
	// onePart uploads part 1 and returns the document completing it.
	onePart := func(ctx context.Context) string {
		etag := f.uploadPart(ctx, "k", id, 1, []byte("hello"))
		return completeDoc(xmlPart("1", `"`+etag+`"`))
	}
	stillThere := func(ctx context.Context) {
		GinkgoHelper()
		_, err := f.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred(), "the upload is still in place")
		Expect(f.stat(ctx, "k").Exists).To(BeFalse(), "nothing was completed")
	}

	It("is radosgw's complete_multipart, a write of s3:PutObject", func() {
		o := &op.CompleteMultipart{}
		Expect(o.Name()).To(Equal("complete_multipart"))
		Expect(o.Action()).To(Equal(policy.S3PutObject))
		Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
	})
	It("authorizes s3:PutObject on the bucket with its ACL permission", func(ctx SpecContext) {
		doc := onePart(ctx)
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", doc))).To(Succeed())
		Expect(authz.VerifyBucketCallCount()).To(Equal(1))
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect([]any{a, perm}).To(Equal([]any{policy.S3PutObject, acl.PermFor(policy.S3PutObject)}))
	})
	It("hands the store the sorted parts with the request id and the conditions, and reports the object", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.CompleteReturns(&op.PutResult{ETag: "e-2", Size: 7, Mtime: smallMtime()}, nil)
		f.env.Multipart = stub
		o := &op.CompleteMultipart{UploadID: id, IfMatch: new(""), IfNoneMatch: new("*")}
		Expect(op.Run(ctx, o, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("2", "b"), xmlPart("1", "a"))))).To(Succeed())
		_, up, parts := stub.CompleteArgsForCall(0)
		Expect([]any{up.ID, up.Bucket.Info.Bucket.Name, up.Key, up.WriteTag}).To(Equal([]any{id, "plain", meta.ObjKey{Name: "k"}, writeReqID}))
		Expect([]string{up.IfMatch, up.IfNoneMatch}).To(Equal([]string{"\x00", "*"}), "an empty If-Match is a condition")
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: "a"}, {Number: 2, ETag: "b"}}))
		Expect([]any{o.ETag, o.Size, o.Mtime}).To(Equal([]any{"e-2", uint64(7), smallMtime()}))
	})
	It("passes no conditions when the headers are absent", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.CompleteReturns(&op.PutResult{}, nil)
		f.env.Multipart = stub
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "a"))))).To(Succeed())
		_, up, _ := stub.CompleteArgsForCall(0)
		Expect([]string{up.IfMatch, up.IfNoneMatch}).To(Equal([]string{"", ""}))
	})
	It("answers an empty upload id with 500 UnknownError, radosgw's ENOTSUP, once permitted and before the body", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		r := f.req(http.MethodPost, "plain", "k")
		r.Body, r.ContentLength = failingReader{}, 1
		Expect(op.Run(ctx, &op.CompleteMultipart{}, r)).To(MatchError(op.ErrUnknown))
		Expect(authz.VerifyBucketCallCount()).To(Equal(1), "get_params runs in execute")
	})
	It("reads the body only once permitted", func(ctx SpecContext) {
		r := f.req(http.MethodPost, "plain", "k")
		r.Identity = f.bob
		r.Body, r.ContentLength = failingReader{}, 1
		r.Header.Set("Content-Length", "1")
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrAccessDenied))
		stillThere(ctx)
	})
	DescribeTable("refuses",
		func(ctx SpecContext, body string, want error) {
			Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", body))).To(MatchError(want))
			stillThere(ctx)
		},
		Entry("an empty body", "", op.ErrMalformedXML),
		Entry("no parts", "<CompleteMultipartUpload/>", op.ErrMalformedXML),
		Entry("a MultipartUpload root", "<MultipartUpload>"+xmlPart("1", "x")+"</MultipartUpload>", op.ErrMalformedXML),
		Entry("more parts than rgw_multipart_part_upload_limit", tooManyParts(10001), op.ErrInvalidRange),
		Entry("a part that was not uploaded", completeDoc(xmlPart("1", `"x"`)), op.ErrInvalidPart),
	)
	It("counts the parts once their repeats collapse, as the limit counts std::map's entries", func(ctx SpecContext) {
		f.conf["rgw_multipart_part_upload_limit"] = "1"
		doc := onePart(ctx)
		repeated := completeDoc(xmlPart("1", "x"), strings.TrimSuffix(strings.TrimPrefix(doc, "<CompleteMultipartUpload>"), "</CompleteMultipartUpload>"))
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", repeated))).To(Succeed())
	})
	It("answers a completion in progress for an upload that does not exist, as radosgw's lock does", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: "2~nope"}, f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", "x"))))).To(MatchError(op.ErrCompletionInProgress))
	})

	Describe("the body, as read_all_input reads it", func() {
		It("is InvalidRange unread when its Content-Length is over rgw_max_put_param_size", func(ctx SpecContext) {
			f.conf["rgw_max_put_param_size"] = "4"
			r := f.req(http.MethodPost, "plain", "k")
			r.Body, r.ContentLength = failingReader{}, 5
			r.Header.Set("Content-Length", "5")
			Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrInvalidRange))
			stillThere(ctx)
		})
		It("is read at rgw_max_put_param_size", func(ctx SpecContext) {
			doc := onePart(ctx)
			f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(doc))
			Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, f.bodyReq(http.MethodPost, "k", doc))).To(Succeed())
		})
		It("is MissingContentLength without a Content-Length or a chunked encoding", func(ctx SpecContext) {
			r := f.req(http.MethodPost, "plain", "k")
			r.Body, r.ContentLength = failingReader{}, 0
			Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrMissingContentLength))
		})
		Describe("chunked", func() {
			chunked := func(body string) *op.Request {
				r := f.req(http.MethodPost, "plain", "k")
				r.Body, r.ContentLength = strings.NewReader(body), -1
				return r
			}
			BeforeEach(func() { f.conf["rgw_max_put_param_size"] = "4096" })

			It("is read to its end while the reads read_all_chunked_input makes stay within the bound", func(ctx SpecContext) {
				doc := onePart(ctx)
				padded := doc + strings.Repeat(" ", 4096+8192-1-len(doc))
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, chunked(padded))).To(Succeed())
			})
			It("is InvalidRange once a full read leaves the total past the bound", func(ctx SpecContext) {
				doc := onePart(ctx)
				padded := doc + strings.Repeat(" ", 4096+8192-len(doc))
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, chunked(padded))).To(MatchError(op.ErrInvalidRange))
				stillThere(ctx)
			})
			It("surfaces the verdict the body ends with", func(ctx SpecContext) {
				doc := onePart(ctx)
				r := f.req(http.MethodPost, "plain", "k")
				r.Body, r.ContentLength = &verdictAfter{data: []byte(doc), err: op.ErrContentSHA256Mismatch}, -1
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				stillThere(ctx)
			})
		})
		Describe("its verification verdict", func() {
			It("is returned for a body that fails as it ends, and nothing is completed", func(ctx SpecContext) {
				doc := onePart(ctx)
				r := f.bodyReq(http.MethodPost, "k", doc)
				r.Body = io.MultiReader(strings.NewReader(doc), errReader{op.ErrContentSHA256Mismatch})
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				stillThere(ctx)
			})
			It("is returned by the Read after the Content-Length bytes, for a single chunk", func(ctx SpecContext) {
				doc := onePart(ctx)
				r := f.bodyReq(http.MethodPost, "k", doc)
				r.Body = &verdictAfter{data: []byte(doc), err: op.ErrContentSHA256Mismatch}
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				stillThere(ctx)
			})
			It("is returned for an empty body too, read to its final Read though it holds nothing", func(ctx SpecContext) {
				r := f.bodyReq(http.MethodPost, "k", "")
				r.Body = &verdictAfter{err: op.ErrContentSHA256Mismatch}
				Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				stillThere(ctx)
			})
		})
		It("is RequestTimeout when it ends short of its length", func(ctx SpecContext) {
			r := f.bodyReq(http.MethodPost, "k", "<CompleteMultipartUpload>")
			r.Body = io.MultiReader(strings.NewReader("<Comp"), errReader{io.ErrUnexpectedEOF})
			Expect(op.Run(ctx, &op.CompleteMultipart{UploadID: id}, r)).To(MatchError(op.ErrRequestTimeout))
			stillThere(ctx)
		})
	})
})
