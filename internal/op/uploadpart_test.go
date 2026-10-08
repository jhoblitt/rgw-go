package op_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // a Content-MD5 is an MD5
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// verdictAfter delivers data whole, with no error, and fails the Read after
// it with err, as a single-chunk payload's verifier gives its verdict only
// once the body has ended.
type verdictAfter struct {
	data []byte
	err  error
}

func (v *verdictAfter) Read(p []byte) (int, error) {
	if len(v.data) == 0 {
		return 0, v.err
	}
	n := copy(p, v.data)
	v.data = v.data[n:]
	return n, nil
}

// partOf is alice's part n of upload id carrying body.
func (f *writeFixture) partOf(id string, n int, body string) *op.UploadPart {
	return &op.UploadPart{
		UploadID: id, PartNumber: n, Body: strings.NewReader(body), Size: int64(len(body)),
		ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"),
	}
}

// copyPartOf is alice's part n of upload id copied from plain's key.
func (f *writeFixture) copyPartOf(id string, n int, key, rng string) *op.UploadPart {
	o := &op.UploadPart{
		UploadID: id, PartNumber: n, CopySource: true, SrcBucket: "plain", SrcKey: meta.ObjKey{Name: key},
		ACL: acl.DefaultPolicy(f.alice.Owner, "Alice"),
	}
	if rng != "" {
		o.Range = new(rng)
	}
	return o
}

// parts are the parts the store holds for key's upload id in plain.
func (f *writeFixture) parts(ctx context.Context, key, id string) []op.Part {
	GinkgoHelper()
	up, err := f.upload(ctx, key, id)
	Expect(err).NotTo(HaveOccurred())
	res, err := f.store.ListParts(ctx, up, 0, 1000)
	Expect(err).NotTo(HaveOccurred())
	return res.Parts
}

var _ = Describe("UploadPart", func() {
	var (
		f  *writeFixture
		id string
	)
	BeforeEach(func(ctx SpecContext) {
		f = newMPFixture(ctx, denc.Squid)
		id = f.initUpload(ctx, "k")
	})

	It("is radosgw's put_obj, a write of s3:PutObject, for both forms", func() {
		for _, o := range []*op.UploadPart{{}, {CopySource: true}} {
			Expect(o.Name()).To(Equal("put_obj"))
			Expect(o.Action()).To(Equal(policy.S3PutObject))
			Expect(o.OpMask()).To(Equal(op.OpTypeWrite))
		}
	})
	It("stores the body as the part and reports its ETag", func(ctx SpecContext) {
		o := f.partOf(id, 3, "hello")
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(o.ETag).To(Equal(md5Hex([]byte("hello"))))
		Expect(o.Mtime).To(Equal(smallMtime()))
		Expect(f.parts(ctx, "k", id)).To(ConsistOf(HaveField("Number", 3)))
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("passes the request's attrs with the ACL, the length, the Content-MD5 and the request id to the store", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.PutPartReturns(&op.PartResult{ETag: "e"}, nil)
		f.env.Multipart = stub
		o := f.partOf(id, 2, "hello")
		o.Attrs = map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")}
		o.ContentMD5 = []byte("0123456789abcdef")
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		_, up, n, body, p := stub.PutPartArgsForCall(0)
		Expect([]any{up.ID, up.Bucket.Info.Bucket.Name, up.Key, n}).To(Equal([]any{id, "plain", meta.ObjKey{Name: "k"}, 2}))
		Expect(body).To(BeIdenticalTo(o.Body))
		Expect(p.Attrs).To(Equal(map[string][]byte{meta.AttrContentType: []byte("text/plain\x00"), meta.AttrACL: f.aliceACL}))
		Expect(up.Attrs).To(Equal(p.Attrs))
		Expect([]any{p.Size, p.ContentMD5, p.Tag}).To(Equal([]any{int64(5), []byte("0123456789abcdef"), writeReqID}))
		Expect(o.Attrs).To(HaveLen(1), "the request's attrs stay as the handler gave them")
	})
	It("reports BadDigest for a body whose MD5 is not the Content-MD5, storing nothing", func(ctx SpecContext) {
		sum := md5.Sum([]byte("other")) //nolint:gosec // a Content-MD5 is an MD5
		o := f.partOf(id, 1, "hello")
		o.ContentMD5 = sum[:]
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrBadDigest))
		Expect(f.parts(ctx, "k", id)).To(BeEmpty())
	})
	Describe("the body's verification verdict", func() {
		It("is surfaced from a body that fails as it ends, and stores nothing", func(ctx SpecContext) {
			o := f.partOf(id, 1, "")
			o.Body, o.Size = io.MultiReader(strings.NewReader("hel"), errReader{op.ErrContentSHA256Mismatch}), 3
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrContentSHA256Mismatch))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("is surfaced by the Read after the Content-Length bytes, for a single chunk, and stores nothing", func(ctx SpecContext) {
			o := f.partOf(id, 1, "")
			o.Body, o.Size = &verdictAfter{data: []byte("hello"), err: op.ErrContentSHA256Mismatch}, 5
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrContentSHA256Mismatch))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("is surfaced for a body of unknown length", func(ctx SpecContext) {
			o := f.partOf(id, 1, "")
			o.Body, o.Size = &verdictAfter{data: []byte("hello"), err: op.ErrContentSHA256Mismatch}, -1
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrContentSHA256Mismatch))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
	})
	It("answers NoSuchUpload for an upload that does not exist, the body unread", func(ctx SpecContext) {
		o := f.partOf("2~nope", 1, "")
		o.Body, o.Size = failingReader{}, 1
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchUpload))
	})
	It("refuses a key-less request as InvalidArgument, the body unread", func(ctx SpecContext) {
		o := f.partOf(id, 1, "")
		o.Body, o.Size = failingReader{}, 1
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInvalidArgument))
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		o := f.partOf(id, 1, "")
		o.Body = failingReader{}
		Expect(op.Run(ctx, o, f.req(http.MethodPut, "nope", "k"))).To(MatchError(op.ErrNoSuchBucket))
		Expect(authz.Invocations()).To(BeEmpty())
	})
	It("authorizes s3:PutObject on the bucket with its ACL permission", func(ctx SpecContext) {
		authz := &opfakes.FakeAuthorizer{}
		f.env.Authz = authz
		Expect(op.Run(ctx, f.partOf(id, 1, "x"), f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		Expect(authz.VerifyBucketCallCount()).To(Equal(1))
		_, _, a, perm := authz.VerifyBucketArgsForCall(0)
		Expect(a).To(Equal(policy.S3PutObject))
		Expect(perm).To(Equal(acl.PermFor(policy.S3PutObject)))
		Expect(authz.VerifyBucketInCallCount()+authz.VerifyObjectInCallCount()).To(BeZero(), "no source to check")
	})
	It("writes nothing for a requester refused write permission", func(ctx SpecContext) {
		stub := &opfakes.FakeMultipartStore{}
		stub.PutPartReturns(&op.PartResult{}, nil)
		f.env.Multipart = stub
		r := f.req(http.MethodPut, "plain", "k")
		r.Identity = f.bob
		o := f.partOf(id, 1, "")
		o.Body, o.Size = failingReader{}, 1
		Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
		Expect(stub.Invocations()).To(BeEmpty())
	})
	Describe("a Content-Length over rgw_max_put_size", func() {
		BeforeEach(func() { f.conf["rgw_max_put_size"] = "10" })

		It("is EntityTooLarge once permitted, before the body is read", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := f.partOf(id, 1, "")
			o.Body, o.Size = failingReader{}, 11
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrEntityTooLarge))
			Expect(authz.VerifyBucketCallCount()).To(Equal(1), "verify_params runs after verify_permission")
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("is EntityTooLarge for a copy whose request declares it, as verify_params reads only the length", func(ctx SpecContext) {
			f.put(ctx, "src", []byte("hello"))
			o := f.copyPartOf(id, 1, "src", "")
			o.Size = 11
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrEntityTooLarge))
		})
		It("passes a length at the limit", func(ctx SpecContext) {
			Expect(op.Run(ctx, f.partOf(id, 1, "0123456789"), f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		})
	})
	Describe("the quota", func() {
		var stats *opfakes.FakeStatsStore
		BeforeEach(func() {
			stats = &opfakes.FakeStatsStore{}
			f.env.Stats = stats
		})

		It("is checked against the announced size, one object, for the bucket's owner, before the body", func(ctx SpecContext) {
			stats.CheckQuotaReturns(op.ErrQuotaExceeded)
			o := f.partOf(id, 1, "")
			o.Body, o.Size = failingReader{}, 5
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrQuotaExceeded))
			_, rec, owner, n, objs := stats.CheckQuotaArgsForCall(0)
			Expect([]any{rec.Info.Bucket.Name, owner, n, objs}).To(Equal([]any{"plain", f.alice.Owner, int64(5), int64(1)}))
		})
		It("is checked for a copy against the request's own length, as radosgw's first check reads it", func(ctx SpecContext) {
			f.put(ctx, "src", []byte("hello"))
			stats.CheckQuotaReturns(op.ErrQuotaExceeded)
			stub := &opfakes.FakeMultipartStore{}
			f.env.Multipart = stub
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrQuotaExceeded))
			_, _, _, n, _ := stats.CheckQuotaArgsForCall(0)
			Expect(n).To(BeZero())
			Expect(stub.CopyPartCallCount()).To(BeZero())
		})
		It("is not checked for a body of unknown length", func(ctx SpecContext) {
			o := f.partOf(id, 1, "x")
			o.Size = -1
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(stats.CheckQuotaCallCount()).To(BeZero())
		})
		It("is not checked for a system request", func(ctx SpecContext) {
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity.System = true
			Expect(op.Run(ctx, f.partOf(id, 1, "x"), r)).To(Succeed())
			Expect(stats.CheckQuotaCallCount()).To(BeZero())
		})
	})
	Describe("a public ACL under a block of public ACLs", func() {
		BeforeEach(func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
		})

		It("is refused once the requester is authorized, before the body is read", func(ctx SpecContext) {
			o := f.partOf(id, 1, "")
			o.Body, o.ACL = failingReader{}, publicReadOf(f.alice.Owner)
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("lets a private ACL through", func(ctx SpecContext) {
			Expect(op.Run(ctx, f.partOf(id, 1, "x"), f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		})
	})

	Describe("with a copy source", func() {
		var src []byte
		BeforeEach(func(ctx SpecContext) {
			src = bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
			f.put(ctx, "src", src)
		})

		It("copies the range into the part", func(ctx SpecContext) {
			o := f.copyPartOf(id, 2, "src", "bytes=100-299")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(o.ETag).To(Equal(md5Hex(src[100:300])))
			Expect(f.parts(ctx, "k", id)).To(ConsistOf(And(HaveField("Number", 2), HaveField("Size", uint64(200)))))
			Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
		})
		It("copies the whole source without a range", func(ctx SpecContext) {
			o := f.copyPartOf(id, 1, "src", "")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(o.ETag).To(Equal(md5Hex(src)))
		})
		It("copies a range ending on the source's last byte", func(ctx SpecContext) {
			o := f.copyPartOf(id, 1, "src", "bytes=65535-65535")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(o.ETag).To(Equal(md5Hex(src[65535:])))
		})
		DescribeTable("answers InvalidRange for a range past the source, writing nothing",
			func(ctx SpecContext, rng string) {
				stub := &opfakes.FakeMultipartStore{}
				stub.CopyPartReturns(&op.PartResult{}, nil)
				f.env.Multipart = stub
				Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", rng), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidRange))
				Expect(stub.CopyPartCallCount()).To(BeZero())
			},
			Entry("one starting at its end", "bytes=65536-70000"),
			Entry("one ending past its end, whose next piece range_to_ofs refuses", "bytes=0-65536"),
			Entry("one far past it", "bytes=0-99999999"),
		)
		It("copies nothing from an empty source, whatever the range", func(ctx SpecContext) {
			f.put(ctx, "empty", nil)
			o := f.copyPartOf(id, 1, "empty", "bytes=10-20")
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(o.ETag).To(Equal(md5Hex(nil)))
		})
		It("hands the store the source state it authorized, the range and the part's attrs", func(ctx SpecContext) {
			stub := &opfakes.FakeMultipartStore{}
			stub.CopyPartReturns(&op.PartResult{ETag: "e"}, nil)
			f.env.Multipart = stub
			o := f.copyPartOf(id, 4, "src", "bytes=10-19")
			o.Attrs = map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")}
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			_, up, n, st, rng := stub.CopyPartArgsForCall(0)
			Expect([]any{up.ID, up.Key, n, rng}).To(Equal([]any{id, meta.ObjKey{Name: "k"}, 4, op.ByteRange{Offset: 10, Length: 10}}))
			Expect(up.Attrs).To(Equal(map[string][]byte{meta.AttrContentType: []byte("text/plain\x00"), meta.AttrACL: f.aliceACL}))
			Expect([]any{st.Key.Name, st.Exists, st.Size}).To(Equal([]any{"src", true, uint64(len(src))}))
			Expect(stub.PutPartCallCount()).To(BeZero())
		})
		It("sizes the range by the source's uncompressed length", func(ctx SpecContext) {
			objs := &opfakes.FakeObjectStore{}
			objs.PrefetchObjectReturns(&op.ObjectState{
				Exists: true, Key: meta.ObjKey{Name: "src"}, Size: 10, Compression: &meta.CompressionInfo{OrigSize: 1000},
				Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrETag: []byte("e")},
			}, nil)
			f.env.Objects = objs
			stub := &opfakes.FakeMultipartStore{}
			stub.CopyPartReturns(&op.PartResult{}, nil)
			f.env.Multipart = stub
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", ""), f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			_, _, _, _, rng := stub.CopyPartArgsForCall(0)
			Expect(rng).To(Equal(op.ByteRange{Offset: 0, Length: 1000}))
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", "bytes=500-999"), f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			_, _, _, _, rng = stub.CopyPartArgsForCall(1)
			Expect(rng).To(Equal(op.ByteRange{Offset: 500, Length: 500}))
		})
		DescribeTable("refuses a range radosgw's parse refuses before checking permissions",
			func(ctx SpecContext, rng string, want error) {
				authz := &opfakes.FakeAuthorizer{}
				f.env.Authz = authz
				o := f.copyPartOf(id, 1, "src", "")
				o.Range = new(rng)
				Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(want))
				Expect(authz.Invocations()).To(BeEmpty())
			},
			Entry("a malformed one", "5-10", op.ErrInvalidArgument),
			Entry("a header present and empty, whose value has no bytes= at its start", "", op.ErrInvalidArgument),
			Entry("a reversed one", "bytes=10-5", op.ErrInvalidRange),
			Entry("one past 2^63, which radosgw's comparison passes", "bytes=9223372036854775808-18446744073709551615", op.ErrInvalidRange),
		)
		It("refuses a source bucket that does not exist once the destination allows the request", func(ctx SpecContext) {
			o := f.copyPartOf(id, 1, "src", "")
			o.SrcBucket = "nope"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchBucket))
		})
		DescribeTable("authorizes the source in its bucket, by both ACLs, then s3:PutObject on the destination",
			func(ctx SpecContext, instance string, want policy.Action) {
				fake := &opfakes.FakeAuthorizer{}
				f.env.Authz = fake
				o := f.copyPartOf(id, 1, "src", "")
				o.SrcKey.Instance = instance
				err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
				if instance == "" {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(op.ErrNotImplemented), "a version is not served yet")
				}
				Expect(fake.VerifyBucketInCallCount()).To(Equal(1))
				_, _, a, perm, bucket, key := fake.VerifyBucketInArgsForCall(0)
				Expect([]any{a, perm, bucket.Info.Bucket.Name, key}).To(Equal([]any{want, acl.PermFor(want), "plain", o.SrcKey}))
				Expect(fake.VerifyObjectInCallCount()).To(Equal(1))
				_, _, a, perm, bucket, obj := fake.VerifyObjectInArgsForCall(0)
				Expect([]any{a, perm, bucket.Info.Bucket.Name, obj.Key.Name}).To(Equal([]any{want, acl.PermFor(want), "plain", "src"}))
				Expect(fake.VerifyBucketCallCount()).To(Equal(1))
				_, _, a, perm = fake.VerifyBucketArgsForCall(0)
				Expect([]any{a, perm}).To(Equal([]any{policy.S3PutObject, acl.PermFor(policy.S3PutObject)}))
			},
			Entry("no version", "", policy.S3GetObject),
			Entry("a version", "null", policy.S3GetObjectVersion),
		)
		DescribeTable("copies nothing when a source check refuses",
			func(ctx SpecContext, refuse func(*opfakes.FakeAuthorizer)) {
				fake := &opfakes.FakeAuthorizer{}
				refuse(fake)
				f.env.Authz = fake
				stub := &opfakes.FakeMultipartStore{}
				stub.CopyPartReturns(&op.PartResult{}, nil)
				f.env.Multipart = stub
				Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
				Expect(stub.Invocations()).To(BeEmpty())
			},
			Entry("the source bucket's", func(a *opfakes.FakeAuthorizer) { a.VerifyBucketInReturns(op.ErrAccessDenied) }),
			Entry("the source object's", func(a *opfakes.FakeAuthorizer) { a.VerifyObjectInReturns(op.ErrAccessDenied) }),
			Entry("the destination's", func(a *opfakes.FakeAuthorizer) { a.VerifyBucketReturns(op.ErrAccessDenied) }),
		)
		It("answers a missing source with NoSuchKey", func(ctx SpecContext) {
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "missing", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchKey))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("takes a missing source's answer from read_obj_policy's rule", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyObjectInReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "missing", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			_, _, _, _, _, obj := fake.VerifyObjectInArgsForCall(0)
			Expect(obj.Exists).To(BeFalse())
			Expect(fake.VerifyBucketInCallCount()).To(BeZero())
		})
		It("refuses a missing source itself when the authorizer lets it through, reaching no store", func(ctx SpecContext) {
			f.env.Authz = &opfakes.FakeAuthorizer{}
			stub := &opfakes.FakeMultipartStore{}
			stub.CopyPartReturns(&op.PartResult{}, nil)
			f.env.Multipart = stub
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "missing", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchKey))
			Expect(stub.Invocations()).To(BeEmpty())
		})
		It("does not copy a missing source for an admin let through a refusal", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyObjectInReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			stub := &opfakes.FakeMultipartStore{}
			f.env.Multipart = stub
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity.Admin = true
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "missing", ""), r)).To(MatchError(op.ErrNoSuchKey))
			Expect(stub.CopyPartCallCount()).To(BeZero())
		})
		It("refuses a store that reports no source state rather than failing open", func(ctx SpecContext) {
			objs := &opfakes.FakeObjectStore{}
			objs.PrefetchObjectReturns(nil, nil)
			f.env.Objects = objs
			stub := &opfakes.FakeMultipartStore{}
			f.env.Multipart = stub
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "src", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchKey))
			Expect(stub.CopyPartCallCount()).To(BeZero())
		})
		It("refuses a source whose ACL does not decode before checking its permission", func(ctx SpecContext) {
			f.putAttrs(ctx, "bad", "x", map[string][]byte{meta.AttrACL: {0xff}})
			fake := &opfakes.FakeAuthorizer{}
			f.env.Authz = fake
			Expect(op.Run(ctx, f.copyPartOf(id, 1, "bad", ""), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrUnknown))
			Expect(fake.VerifyBucketInCallCount()).To(BeZero())
		})
		It("answers NoSuchUpload for an upload that does not exist ahead of the source's state", func(ctx SpecContext) {
			Expect(op.Run(ctx, f.copyPartOf("2~nope", 1, "src", "bytes=99999-99999"), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNoSuchUpload))
		})
		DescribeTable("refuses a source transitioned to a cloud tier, with a range or not",
			func(ctx SpecContext, release denc.Release, tier, rng string, refused bool) {
				f = newMPFixture(ctx, release)
				id = f.initUpload(ctx, "k")
				objs := &opfakes.FakeObjectStore{}
				m := meta.NewManifest()
				m.TierType = tier
				objs.PrefetchObjectReturns(&op.ObjectState{Exists: true, Key: meta.ObjKey{Name: "src"}, Size: 10, Manifest: &m, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}}, nil)
				f.env.Objects = objs
				stub := &opfakes.FakeMultipartStore{}
				stub.CopyPartReturns(&op.PartResult{}, nil)
				f.env.Multipart = stub
				err := op.Run(ctx, f.copyPartOf(id, 1, "src", rng), f.req(http.MethodPut, "plain", "k"))
				if !refused {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(MatchError(op.ErrInvalidObjectState))
				Expect(messageOf(err)).To(Equal("This object was transitioned to cloud-s3"))
				Expect(stub.CopyPartCallCount()).To(BeZero())
			},
			Entry("cloud-s3 on Squid", denc.Squid, meta.TierTypeCloudS3, "", true),
			Entry("cloud-s3 on Squid with a range, which radosgw does not check", denc.Squid, meta.TierTypeCloudS3, "bytes=0-1", true),
			Entry("cloud-s3-glacier on Squid, which Squid does not know", denc.Squid, meta.TierTypeCloudS3Glacier, "", false),
			Entry("cloud-s3-glacier on Tentacle", denc.Tentacle, meta.TierTypeCloudS3Glacier, "", true),
			Entry("cloud-s3 on Tentacle, whatever its size", denc.Tentacle, meta.TierTypeCloudS3, "", true),
			Entry("no tier", denc.Tentacle, "", "", false),
		)
		DescribeTable("checks the x-amz-copy-source-if-* conditions against the state it copies, which radosgw ignores",
			func(ctx SpecContext, set func(o *op.UploadPart), want error) {
				stub := &opfakes.FakeMultipartStore{}
				stub.CopyPartReturns(&op.PartResult{}, nil)
				f.env.Multipart = stub
				o := f.copyPartOf(id, 1, "src", "")
				set(o)
				err := op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
					Expect(stub.CopyPartCallCount()).To(Equal(1))
					return
				}
				Expect(err).To(MatchError(want))
				Expect(stub.CopyPartCallCount()).To(BeZero())
			},
			Entry("If-Match naming the source's ETag, quoted, copies", func(o *op.UploadPart) { o.IfMatch = new(`"` + md5Hex(src) + `"`) }, nil),
			Entry("If-Match naming another ETag fails", func(o *op.UploadPart) { o.IfMatch = new(md5Hex([]byte("other"))) }, op.ErrPreconditionFailed),
			Entry("an empty If-Match fails", func(o *op.UploadPart) { o.IfMatch = new("") }, op.ErrPreconditionFailed),
			Entry("If-None-Match naming the source's ETag is NotModified", func(o *op.UploadPart) { o.IfNoneMatch = new(md5Hex(src)) }, op.ErrNotModified),
			Entry("If-None-Match naming another ETag copies", func(o *op.UploadPart) { o.IfNoneMatch = new("other") }, nil),
			Entry("If-Modified-Since at the source's mtime is NotModified", func(o *op.UploadPart) {
				o.IfModifiedSince = new(smallMtime().Format(http.TimeFormat))
			}, op.ErrNotModified),
			Entry("If-Unmodified-Since before the source's mtime fails", func(o *op.UploadPart) {
				o.IfUnmodifiedSince = new(smallMtime().Add(-48 * time.Hour).Format(http.TimeFormat))
			}, op.ErrPreconditionFailed),
			Entry("a date radosgw does not parse is InvalidArgument", func(o *op.UploadPart) { o.IfModifiedSince = new("yesterday") }, op.ErrInvalidArgument),
		)
		It("refuses a source version with 501 until versioning is served", func(ctx SpecContext) {
			objs := &opfakes.FakeObjectStore{}
			objs.PrefetchObjectReturns(&op.ObjectState{Exists: true, Key: meta.ObjKey{Name: "src", Instance: "v1"}, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}}, nil)
			f.env.Objects = objs
			stub := &opfakes.FakeMultipartStore{}
			f.env.Multipart = stub
			o := f.copyPartOf(id, 1, "src", "")
			o.SrcKey.Instance = "v1"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNotImplemented))
			Expect(stub.Invocations()).To(BeEmpty())
		})

		Describe("the source's ACLs, when no policy decides", func() {
			var (
				bob   op.Identity
				bobID string
			)
			BeforeEach(func(ctx SpecContext) {
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				u := meta.NewUserInfo()
				u.UserID, u.DisplayName, u.Type = meta.UserID{ID: "bob"}, "Bob", meta.IdentityRGW
				bob = op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, AccessKey: "bob"}
				_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
					Name: "bobs", Owner: bob.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
					Attrs: map[string][]byte{meta.AttrACL: encodedPolicy(acl.DefaultPolicy(bob.Owner, "Bob"))},
				})
				Expect(err).NotTo(HaveOccurred())
				r := f.req(http.MethodPost, "bobs", "dst")
				r.Identity = bob
				init := &op.InitMultipart{ACL: acl.DefaultPolicy(bob.Owner, "Bob")}
				Expect(op.Run(ctx, init, r)).To(Succeed())
				bobID = init.UploadID
			})
			readBy := func(id string) acl.Policy {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: id, Name: id, Permission: acl.PermRead})
				return p
			}
			DescribeTable("needs READ on both the bucket's and the object's",
				func(ctx SpecContext, bucketRead, objectRead, allowed bool) {
					if bucketRead {
						f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(readBy("bob"))})
					}
					objACL := f.aliceACL
					if objectRead {
						objACL = encodedPolicy(readBy("bob"))
					}
					f.putAttrs(ctx, "src", "hello", map[string][]byte{meta.AttrACL: objACL})
					r := f.req(http.MethodPut, "bobs", "dst")
					r.Identity = bob
					o := &op.UploadPart{UploadID: bobID, PartNumber: 1, CopySource: true, SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "src"}, ACL: acl.DefaultPolicy(bob.Owner, "Bob")}
					err := op.Run(ctx, o, r)
					if allowed {
						Expect(err).NotTo(HaveOccurred())
						Expect(o.ETag).To(Equal(md5Hex([]byte("hello"))))
						return
					}
					Expect(err).To(MatchError(op.ErrAccessDenied))
				},
				Entry("a grantee of the object alone is refused, where radosgw copies", false, true, false),
				Entry("a grantee of the bucket alone is refused, as by radosgw", true, false, false),
				Entry("a grantee of both copies", true, true, true),
				Entry("a grantee of neither is refused", false, false, false),
			)
		})
	})
	Describe("the protocol's hooks and the checks of stored state", func() {
		It("refuses a storage class the zone lacks once the requester is authorized", func(ctx SpecContext) {
			o := f.partOf(id, 1, "x")
			o.StorageClass = "NOPE"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			o = f.partOf(id, 1, "x")
			o.StorageClass = "NOPE"
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
			o = f.partOf(id, 1, "x")
			o.StorageClass = meta.StorageClassStandard
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
		})
		It("refuses a storage class the zone lacks ahead of a bucket's default encryption, as init_permissions comes first", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			o := f.partOf(id, 1, "x")
			o.StorageClass = "NOPE"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
			Expect(op.Run(ctx, f.partOf(id, 1, "x"), f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNotImplemented))
		})
		It("returns SourceErr, a parse of the request, before the range and Params, and before checking permissions", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := f.partOf(id, 1, "")
			o.SourceErr, o.CopySource, o.Range = op.ErrInvalidArgument, true, new("bytes=3-1")
			o.Params = func(context.Context, *op.UploadPart) error { return op.ErrMissingContentLength }
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
			Expect(authz.Invocations()).To(BeEmpty())
		})
		It("runs Params, which reads the request alone, before checking permissions", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := f.partOf(id, 1, "")
			o.Params = func(context.Context, *op.UploadPart) error { return op.ErrMissingContentLength }
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrMissingContentLength))
			Expect(authz.Invocations()).To(BeEmpty())
		})
		It("refuses a public canned ACL under the block once authorized, before Authorized", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			calls := 0
			o := f.partOf(id, 1, "")
			o.Body, o.CannedACL = failingReader{}, "public-read"
			o.Authorized = func(context.Context, *op.UploadPart) error { calls++; return nil }
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero())
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			o = f.partOf(id, 1, "")
			o.Body, o.CannedACL = failingReader{}, "public-read"
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
		})
		It("refuses the public ACL Authorized builds under the block, storing nothing", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrPublicAccess: publicAccessAttr(acl.PublicAccessBlock{BlockPublicACLs: true})})
			o := f.partOf(id, 1, "")
			o.Body = failingReader{}
			o.Authorized = func(_ context.Context, o *op.UploadPart) error { o.ACL = publicReadOf(f.alice.Owner); return nil }
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrAccessDenied))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("refuses a bucket with a default encryption once the requester is authorized, before Authorized, storing nothing", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			calls := 0
			o := f.partOf(id, 1, "x")
			o.Authorized = func(context.Context, *op.UploadPart) error { calls++; return nil }
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNotImplemented))
			Expect(calls).To(BeZero())
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			Expect(op.Run(ctx, f.partOf(id, 1, "x"), r)).To(MatchError(op.ErrAccessDenied))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("runs Authorized only for an authorized requester, before the body is read", func(ctx SpecContext) {
			calls := 0
			authorized := func(context.Context, *op.UploadPart) error { calls++; return op.ErrInvalidDigest }
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			o := f.partOf(id, 1, "")
			o.Body, o.Authorized = failingReader{}, authorized
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
			Expect(calls).To(BeZero())
			o = f.partOf(id, 1, "")
			o.Body, o.Authorized = failingReader{}, authorized
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidDigest))
			Expect(calls).To(Equal(1))
			Expect(f.parts(ctx, "k", id)).To(BeEmpty())
		})
		It("refuses a copy's storage class the zone lacks before reading the source, as init_permissions checks it", func(ctx SpecContext) {
			objects := &opfakes.FakeObjectStore{}
			f.env.Objects = objects
			o := f.copyPartOf(id, 1, "src", "")
			o.SrcBucket, o.StorageClass = "nope", "NOPE"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
			Expect(objects.PrefetchObjectCallCount()).To(BeZero())
		})
		DescribeTable("answers a requester whose op mask refuses the write an identical 403 before reading anything of the source, as verify_op_mask precedes read_obj_policy",
			func(ctx SpecContext, srcBucket, srcKey string) {
				r, loaded, objects := f.opMaskRefusesSources(ctx, "k")
				o := f.copyPartOf(id, 1, srcKey, "")
				o.SrcBucket = srcBucket
				Expect(op.AsError(op.Run(ctx, o, r))).To(Equal(op.ErrAccessDenied))
				Expect(*loaded).To(Equal([]string{"plain"}), "only the destination is loaded")
				Expect(objects.PrefetchObjectCallCount()).To(BeZero())
			},
			Entry("a source bucket that does not exist", "nope", "src"),
			Entry("a source key that does not exist", "plain", "nope"),
			Entry("a source that exists", "plain", "src"),
			Entry("a source whose bucket policy does not parse", "badpol", "src"),
		)
		DescribeTable("answers a requester the destination refuses 403 before reading anything of the source",
			func(ctx SpecContext, srcBucket, srcKey string) {
				objects := &opfakes.FakeObjectStore{}
				f.env.Objects = objects
				r := f.req(http.MethodPut, "plain", "k")
				r.Identity = f.bob
				o := &op.UploadPart{UploadID: id, PartNumber: 1, CopySource: true, SrcBucket: srcBucket, SrcKey: meta.ObjKey{Name: srcKey}}
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(objects.PrefetchObjectCallCount()).To(BeZero())
			},
			Entry("a source bucket that does not exist", "nope", "src"),
			Entry("a source key that does not exist", "plain", "nope"),
			Entry("a source that exists", "plain", "src"),
		)
	})
})
