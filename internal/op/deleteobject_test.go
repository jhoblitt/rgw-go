package op_test

import (
	"math"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var _ = Describe("DeleteObject", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	DescribeTable("is radosgw's delete_obj, a delete of the action the instance selects",
		func(ctx SpecContext, instance string, want policy.Action) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := &op.DeleteObject{}
			r := f.req(http.MethodDelete, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if instance == "" {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(op.ErrNotImplemented), "a version is not served yet")
			}
			Expect(o.Name()).To(Equal("delete_obj"))
			Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
			Expect(o.Action()).To(Equal(want))
			Expect(authz.VerifyBucketCallCount()).To(Equal(1))
			_, _, a, perm := authz.VerifyBucketArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3DeleteObject),
		Entry("the null instance", "null", policy.S3DeleteObjectVersion),
	)
	It("deletes the object, answering nil for radosgw's 204, and logs no usage", func(ctx SpecContext) {
		o := &op.DeleteObject{}
		Expect(op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
		Expect(f.stat(ctx, "small").Exists).To(BeFalse())
		Expect(o.DeleteMarker).To(BeFalse())
		Expect(o.VersionID).To(BeEmpty())
		Expect(f.store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("answers a missing key with nil, radosgw's 204", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.DeleteObject{}, f.req(http.MethodDelete, "plain", "missing"))).To(Succeed())
	})
	It("refuses a bucket that does not exist", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.DeleteObject{}, f.req(http.MethodDelete, "plain-not", "small"))).To(MatchError(op.ErrNoSuchBucket))
	})
	It("refuses a requester the authorizer refuses and keeps the object", func(ctx SpecContext) {
		r := f.req(http.MethodDelete, "plain", "small")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(MatchError(op.ErrAccessDenied))
		Expect(f.stat(ctx, "small").Exists).To(BeTrue())
	})
	Describe("x-amz-delete-if-unmodified-since", func() {
		It("refuses a value parse_date does not take as InvalidArgument, before permissions", func(ctx SpecContext) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := &op.DeleteObject{UnmodifiedSince: new("Sun, 27 Sep 2026 01:02:03 GMT")}
			Expect(op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))).To(MatchError(op.ErrInvalidArgument))
			Expect(authz.Invocations()).To(BeEmpty(), "get_params runs in init_processing")
			Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		})
		DescribeTable("deletes when the object is no newer, in whole seconds, and otherwise answers 412",
			func(ctx SpecContext, value string, want error) {
				err := op.Run(ctx, &op.DeleteObject{UnmodifiedSince: new(value)}, f.req(http.MethodDelete, "plain", "small"))
				if want != nil {
					Expect(err).To(MatchError(want))
					Expect(f.stat(ctx, "small").Exists).To(BeTrue())
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(f.stat(ctx, "small").Exists).To(BeFalse())
			},
			Entry("a second before the mtime", "2026-09-27T01:02:02Z", op.ErrPreconditionFailed),
			Entry("the mtime's second", "2026-09-27T01:02:03Z", nil),
			Entry("a fraction within the mtime's second", "2026-09-27 01:02:03.000000001", nil),
			Entry("a later date alone", "2026-09-28", nil),
			Entry("an earlier date alone, its midnight", "2026-09-27", op.ErrPreconditionFailed),
			Entry("epoch seconds and a fraction", "1790470923.0", nil),
			Entry("an offset east of UTC, applied", "2026-09-27T02:02:03+0100", nil),
			Entry("an offset west of UTC, applied", "2026-09-27T00:02:03-0100", nil),
			Entry("an offset that puts the date a second before the mtime", "2026-09-27T02:02:02+01:00", op.ErrPreconditionFailed),
			Entry("epoch seconds a second before the mtime", "1790470922.999999", op.ErrPreconditionFailed),
			Entry("trailing bytes past the parsed time, which strptime leaves", "2026-09-27T01:02:03Zjunk", nil),
		)
	})
	DescribeTable("checks Tentacle's three match conditions on both releases",
		func(ctx SpecContext, release denc.Release, o *op.DeleteObject, want error) {
			f = newWriteFixture(ctx, release)
			err := op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))
			if want != nil {
				Expect(err).To(MatchError(want))
				Expect(f.stat(ctx, "small").Exists).To(BeTrue())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(f.stat(ctx, "small").Exists).To(BeFalse())
		},
		Entry("If-Match naming another ETag on Squid", denc.Squid, &op.DeleteObject{IfMatch: new(md5Hex([]byte("other")))}, op.ErrPreconditionFailed),
		Entry("If-Match naming another ETag on Tentacle", denc.Tentacle, &op.DeleteObject{IfMatch: new(md5Hex([]byte("other")))}, op.ErrPreconditionFailed),
		Entry("If-Match naming the ETag", denc.Squid, &op.DeleteObject{IfMatch: new(md5Hex(payload(1024)))}, nil),
		Entry("x-amz-if-match-size of another size on Squid", denc.Squid, &op.DeleteObject{IfMatchSize: new("1023")}, op.ErrPreconditionFailed),
		Entry("x-amz-if-match-size of the size", denc.Tentacle, &op.DeleteObject{IfMatchSize: new("1024")}, nil),
		Entry("x-amz-if-match-size after whitespace and a plus, as strtoll reads it", denc.Squid, &op.DeleteObject{IfMatchSize: new(" +1024")}, nil),
		Entry("x-amz-if-match-size negative, wrapped as radosgw casts it", denc.Squid, &op.DeleteObject{IfMatchSize: new("-1")}, op.ErrPreconditionFailed),
		Entry("x-amz-if-match-last-modified-time of another second on Squid", denc.Squid,
			&op.DeleteObject{IfMatchLastModified: new("Sun, 27 Sep 2026 01:02:04 GMT")}, op.ErrPreconditionFailed),
		Entry("x-amz-if-match-last-modified-time of the mtime's second", denc.Tentacle,
			&op.DeleteObject{IfMatchLastModified: new("Sun, 27 Sep 2026 01:02:03 GMT")}, nil),
	)
	DescribeTable("refuses a match condition that does not parse as InvalidArgument, before permissions",
		func(ctx SpecContext, o *op.DeleteObject) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			Expect(op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))).To(MatchError(op.ErrInvalidArgument))
			Expect(authz.Invocations()).To(BeEmpty())
		},
		Entry("a size that is no number", &op.DeleteObject{IfMatchSize: new("abc")}),
		Entry("a size with bytes after its digits", &op.DeleteObject{IfMatchSize: new("1024x")}),
		Entry("a size past int64", &op.DeleteObject{IfMatchSize: new("9223372036854775808")}),
		Entry("a last-modified time that is no date", &op.DeleteObject{IfMatchLastModified: new("yesterday")}),
	)
	It("passes the parsed conditions to the store, an empty one as unset", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.StatObjectReturns(&op.ObjectState{Exists: true}, nil)
		f.env.Objects = stub
		Expect(op.Run(ctx, &op.DeleteObject{}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
		_, _, _, p := stub.DeleteObjectArgsForCall(0)
		Expect(p).To(Equal(op.DeleteParams{}))

		o := &op.DeleteObject{
			IfMatch: new("*"), IfMatchSize: new("-1"),
			UnmodifiedSince: new("2026-09-27T01:02:03Z"), IfMatchLastModified: new("Sun, 27 Sep 2026 01:02:03 GMT"),
		}
		Expect(op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
		_, _, _, p = stub.DeleteObjectArgsForCall(1)
		Expect(p.IfMatch).To(Equal("*"))
		Expect(p.IfMatchSize).To(Equal(new(uint64(math.MaxUint64))))
		Expect(p.UnmodifiedSince).To(Equal(time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)))
		Expect(p.IfMatchLastModified).To(Equal(time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)))
	})
	DescribeTable("maps the store's outcome as RGWDeleteObj::execute does",
		func(ctx SpecContext, storeErr, want error) {
			stub := &opfakes.FakeObjectStore{}
			stub.DeleteObjectReturns(storeErr)
			f.env.Objects = stub
			err := op.Run(ctx, &op.DeleteObject{}, f.req(http.MethodDelete, "plain", "small"))
			if want == nil {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(want))
		},
		Entry("a lost race is success", op.ErrConcurrentModification, nil),
		Entry("a missing key is success", op.ErrNoSuchKey, nil),
		Entry("a failed condition is 412", op.ErrPreconditionFailed, op.ErrPreconditionFailed),
		Entry("any other failure passes", op.ErrServiceUnavailable, op.ErrServiceUnavailable),
	)
	Describe("on a bucket with MFA delete", func() {
		BeforeEach(func(ctx SpecContext) { f.setBucketFlags(ctx, meta.BucketMFAEnabled) })

		It("refuses a delete of a version, whose MFA rgw-go never verifies", func(ctx SpecContext) {
			r := f.req(http.MethodDelete, "plain", "small")
			r.Object.Instance = "null"
			Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(MatchError(op.ErrMFARequired))
			Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		})
		It("deletes without a version", func(ctx SpecContext) {
			Expect(op.Run(ctx, &op.DeleteObject{}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
		})
		It("refuses an admin refused the permission too, where radosgw's override skips MFA", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			f.env.Objects = stub
			r := f.req(http.MethodDelete, "plain", "small")
			r.Identity = f.bob
			r.Identity.Admin = true
			r.Object.Instance = "null"
			Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(MatchError(op.ErrMFARequired))
			Expect(stub.DeleteObjectCallCount()).To(BeZero())
		})
		It("still returns a refusal the authorizer marks first", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyBucketReturns(op.BeforeVerify(op.ErrUserSuspended))
			f.env.Authz = fake
			r := f.req(http.MethodDelete, "plain", "small")
			r.Object.Instance = "null"
			Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(MatchError(op.ErrUserSuspended))
		})
	})
	DescribeTable("takes a condition header present with an empty value as radosgw does, and keeps the object",
		func(ctx SpecContext, o *op.DeleteObject, want error) {
			Expect(op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))).To(MatchError(want))
			Expect(f.stat(ctx, "small").Exists).To(BeTrue())
		},
		Entry("If-Match, which no ETag is a prefix of", &op.DeleteObject{IfMatch: new("")}, op.ErrPreconditionFailed),
		Entry("x-amz-delete-if-unmodified-since, which parse_date refuses", &op.DeleteObject{UnmodifiedSince: new("")}, op.ErrInvalidArgument),
		Entry("x-amz-if-match-size, which strict_strtoll refuses", &op.DeleteObject{IfMatchSize: new("")}, op.ErrInvalidArgument),
		Entry("x-amz-if-match-last-modified-time, which parse_time refuses", &op.DeleteObject{IfMatchLastModified: new("")}, op.ErrInvalidArgument),
	)
})
