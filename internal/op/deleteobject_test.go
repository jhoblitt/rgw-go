package op_test

import (
	"context"
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
		func(ctx SpecContext, instance string, want policy.Action, wantErr error) {
			authz := &opfakes.FakeAuthorizer{}
			f.env.Authz = authz
			o := &op.DeleteObject{}
			r := f.req(http.MethodDelete, "plain", "small")
			r.Object.Instance = instance
			err := op.Run(ctx, o, r)
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(wantErr))
			}
			Expect(o.Name()).To(Equal("delete_obj"))
			Expect(o.OpMask()).To(Equal(op.OpTypeDelete))
			Expect(o.Action()).To(Equal(want))
			Expect(authz.VerifyBucketCallCount()).To(Equal(1))
			_, _, a, perm := authz.VerifyBucketArgsForCall(0)
			Expect(a).To(Equal(want))
			Expect(perm).To(Equal(acl.PermFor(want)))
		},
		Entry("no instance", "", policy.S3DeleteObject, nil),
		Entry("the null instance, the plain object on a bucket whose versioning was never enabled", "null", policy.S3DeleteObjectVersion, nil),
		Entry("a version, which a bucket never versioned does not hold: radosgw's 204", "v1", policy.S3DeleteObjectVersion, nil),
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
	It("passes the parsed conditions to the store, an absent one as unset", func(ctx SpecContext) {
		stub := &opfakes.FakeObjectStore{}
		stub.StatObjectReturns(&op.ObjectState{Exists: true}, nil)
		f.env.Objects = stub
		Expect(op.Run(ctx, &op.DeleteObject{}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
		_, _, _, p := stub.DeleteObjectArgsForCall(0)
		Expect(p).To(Equal(op.DeleteParams{MarkerOwner: f.alice.Owner, MarkerOwnerDisplayName: "Alice"}))

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
	DescribeTable("reads x-amz-delete-if-unmodified-since as utime_t::parse_date does",
		func(ctx SpecContext, value string, want time.Time) {
			stub := &opfakes.FakeObjectStore{}
			stub.StatObjectReturns(&op.ObjectState{Exists: true}, nil)
			f.env.Objects = stub
			Expect(op.Run(ctx, &op.DeleteObject{UnmodifiedSince: new(value)}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
			_, _, _, p := stub.DeleteObjectArgsForCall(0)
			Expect(p.UnmodifiedSince).To(Equal(want), "%q", value)
		},
		Entry("a date alone", "2026-09-27", time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)),
		Entry("bytes after the date other than a space or T, ignored", "2026-09-27x01:02:03", time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)),
		Entry("a fraction of up to nine digits", "2026-09-27T01:02:03.123456789123", time.Date(2026, 9, 27, 1, 2, 3, 123456789, time.UTC)),
		Entry("a short fraction padded with zeros", "2026-09-27 01:02:03.5", time.Date(2026, 9, 27, 1, 2, 3, 500000000, time.UTC)),
		Entry("an offset with a colon", "2026-09-27T01:02:03.5-01:30", time.Date(2026, 9, 27, 2, 32, 3, 500000000, time.UTC)),
		Entry("a time whose sixth byte ends the format", "2026-09-27T1:2:3", time.Date(2026, 9, 27, 1, 2, 0, 0, time.UTC)),
		Entry("numbers in the time skipping white space", "2026-09-27T 1: 2: 3", time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)),
		Entry("a day past the month's end runs on", "2026-02-31", time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)),
		Entry("a NUL ends the value", "2026-09-27T01:02:03\x00+0100", time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)),
		Entry("seconds and microseconds after white space and signs", " +1790470923. -5", time.Unix(1790470927, 294962296).UTC()),
		Entry("negative seconds, through utime_t's 32 bits", "-1.0", time.Unix(math.MaxUint32, 0).UTC()),
		Entry("seconds past a long, saturated then cut to an int", "99999999999999999999.0", time.Unix(math.MaxUint32, 0).UTC()),
	)
	DescribeTable("refuses x-amz-delete-if-unmodified-since that parse_date refuses",
		func(ctx SpecContext, value string) {
			err := op.Run(ctx, &op.DeleteObject{UnmodifiedSince: new(value)}, f.req(http.MethodDelete, "plain", "small"))
			Expect(err).To(MatchError(op.ErrInvalidArgument), "%q", value)
		},
		Entry("seconds alone", "1790470923"),
		Entry("seconds and a dot", "1790470923."),
		Entry("seconds, a dot and a sign", "1790470923.-"),
		Entry("a dot and microseconds", ".5"),
		Entry("an hour alone", "2026-09-27T01"),
		Entry("a time whose separators are not colons", "2026-09-27T01-02-03"),
		Entry("a conversion after the seconds", "2026-09-27T01:02:03%d"),
	)
	DescribeTable("reads x-amz-if-match-size as strict_strtoll does",
		func(ctx SpecContext, value string, want uint64) {
			stub := &opfakes.FakeObjectStore{}
			stub.StatObjectReturns(&op.ObjectState{Exists: true}, nil)
			f.env.Objects = stub
			Expect(op.Run(ctx, &op.DeleteObject{IfMatchSize: new(value)}, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
			_, _, _, p := stub.DeleteObjectArgsForCall(0)
			Expect(p.IfMatchSize).To(Equal(new(want)), "%q", value)
		},
		Entry("LLONG_MAX", "9223372036854775807", uint64(math.MaxInt64)),
		Entry("LLONG_MIN, cast to uint64_t", "-9223372036854775808", uint64(1)<<63),
		Entry("white space and a sign", "\t-0", uint64(0)),
		Entry("bytes after a NUL", "12\x00x", uint64(12)),
	)
	DescribeTable("refuses x-amz-if-match-size that strict_strtoll refuses",
		func(ctx SpecContext, value string) {
			err := op.Run(ctx, &op.DeleteObject{IfMatchSize: new(value)}, f.req(http.MethodDelete, "plain", "small"))
			Expect(err).To(MatchError(op.ErrInvalidArgument), "%q", value)
		},
		Entry("below LLONG_MIN", "-9223372036854775809"),
		Entry("a sign alone", "+"),
		Entry("white space alone", " "),
		Entry("trailing white space", "1 "),
	)
	DescribeTable("maps the store's outcome as RGWDeleteObj::execute does",
		func(ctx SpecContext, storeErr, want error) {
			stub := &opfakes.FakeObjectStore{}
			stub.DeleteObjectReturns(nil, storeErr)
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
		var stub *opfakes.FakeObjectStore
		BeforeEach(func(ctx SpecContext) {
			f.setBucketFlags(ctx, meta.BucketVersioned|meta.BucketMFAEnabled)
			stub = &opfakes.FakeObjectStore{}
			stub.DeleteObjectReturns(&op.DeleteObjectResult{}, nil)
			f.env.Objects = stub
		})
		// run deletes small at instance as id, whose x-amz-mfa verified a
		// device of id's user when verified.
		run := func(ctx context.Context, id op.Identity, instance string, verified bool) error {
			r := f.req(http.MethodDelete, "plain", "small")
			r.Identity, r.Object.Instance, r.MFAVerified = id, instance, verified
			return op.Run(ctx, &op.DeleteObject{}, r)
		}
		// expectRefused checks err is ERR_MFA_REQUIRED, which shares
		// AccessDenied and 403 with EACCES and differs in its errno.
		expectRefused := func(err error) {
			GinkgoHelper()
			Expect(err).To(MatchError(op.ErrMFARequired))
			Expect(op.AsError(err).Errno).To(Equal(op.ErrMFARequired.Errno), "ERR_MFA_REQUIRED, not the permission's EACCES")
			Expect(stub.DeleteObjectCallCount()).To(BeZero(), "nothing reaches the store")
		}

		DescribeTable("deletes a version only under the owner's verified x-amz-mfa, and writes a delete marker without one",
			func(ctx SpecContext, instance string, verified, refused bool) {
				err := run(ctx, f.alice, instance, verified)
				if refused {
					expectRefused(err)
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(stub.DeleteObjectCallCount()).To(Equal(1))
			},
			Entry("a version, unverified", "v1", false, true),
			Entry("the null version, unverified, a version as radosgw counts it", "null", false, true),
			Entry("a version, verified", "v1", true, false),
			Entry("the null version, verified", "null", true, false),
			Entry("no version, unverified: the delete marker removes nothing", "", false, false),
		)
		It("counts no requester's code but the owner's, even one the permission lets delete", func(ctx SpecContext) {
			f.env.Authz = &opfakes.FakeAuthorizer{}
			expectRefused(run(ctx, f.bob, "v1", true))
		})
		DescribeTable("checks an admin whom the permission refuses, where radosgw's admin override skips the MFA check",
			func(ctx SpecContext, verified bool) {
				admin := f.bob
				admin.Admin = true
				expectRefused(run(ctx, admin, "v1", verified))
			},
			Entry("unverified", false),
			Entry("with the admin's own code verified, which is not the owner's", true),
		)
		It("lets an admin the permission refuses delete a version under the owner's verified code", func(ctx SpecContext) {
			admin := f.alice
			admin.Admin = true
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyBucketReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			Expect(run(ctx, admin, "v1", true)).To(Succeed())
			Expect(stub.DeleteObjectCallCount()).To(Equal(1))
		})
		It("returns the permission's own refusal once the owner's code verified", func(ctx SpecContext) {
			fake := &opfakes.FakeAuthorizer{}
			fake.VerifyBucketReturns(op.ErrAccessDenied)
			f.env.Authz = fake
			err := run(ctx, f.alice, "v1", true)
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(op.AsError(err).Errno).To(Equal(op.ErrAccessDenied.Errno), "the permission's EACCES, not ERR_MFA_REQUIRED")
			Expect(stub.DeleteObjectCallCount()).To(BeZero())
		})
		DescribeTable("returns a refusal op.Run never overrides as it is, ahead of the MFA check",
			func(ctx SpecContext, refusal, want error) {
				fake := &opfakes.FakeAuthorizer{}
				fake.VerifyBucketReturns(refusal)
				f.env.Authz = fake
				admin := f.alice
				admin.Admin = true
				Expect(run(ctx, admin, "v1", false)).To(MatchError(want))
				Expect(stub.DeleteObjectCallCount()).To(BeZero())
			},
			Entry("one the authorizer marks first", op.BeforeVerify(op.ErrUserSuspended), op.ErrUserSuspended),
			Entry("one that is no access denial, a tag read's failure", op.ErrInternalError, op.ErrInternalError),
		)
	})
	Describe("on a versioned bucket", func() {
		var stub *opfakes.FakeObjectStore
		BeforeEach(func() {
			stub = &opfakes.FakeObjectStore{}
			stub.DeleteObjectReturns(&op.DeleteObjectResult{}, nil)
			f.env.Objects = stub
		})
		DescribeTable("hands the store the key, the requester as a delete marker's owner and no expected state, with the key's own update",
			func(ctx SpecContext, flags uint32, instance string) {
				f.setBucketFlags(ctx, flags)
				r := f.req(http.MethodDelete, "plain", "small")
				r.Object.Instance = instance
				Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(Succeed())
				Expect(stub.DeleteObjectCallCount()).To(Equal(1))
				_, rec, key, p := stub.DeleteObjectArgsForCall(0)
				Expect(rec.Info.Flags).To(Equal(flags))
				Expect(key).To(Equal(meta.ObjKey{Name: "small", Instance: instance}))
				Expect(p.MarkerOwner).To(Equal(f.alice.Owner))
				Expect(p.MarkerOwnerDisplayName).To(Equal("Alice"))
				Expect(p.SkipOLHUpdate).To(BeFalse())
				Expect(p.Expect).To(BeNil())
			},
			Entry("enabled, a delete marker", uint32(meta.BucketVersioned), ""),
			Entry("enabled, a version", uint32(meta.BucketVersioned), "v1"),
			Entry("suspended, the null delete marker", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), ""),
			Entry("suspended, the null version", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended), "null"),
		)
		It("names an account's user's delete marker for the account, under the account's name, as s->owner does", func(ctx SpecContext) {
			f.setBucketFlags(ctx, meta.BucketVersioned)
			f.env.Authz = &opfakes.FakeAuthorizer{}
			const acct = "RGW12345678901234567"
			u := meta.UserInfo{UserID: meta.UserID{ID: "carol"}, DisplayName: "Carol", AccountID: acct}
			r := f.req(http.MethodDelete, "plain", "small")
			r.Identity = op.Identity{User: &u, Owner: meta.AccountOwner(acct), Account: &meta.AccountInfo{ID: acct, Name: "acme"}, OpMask: op.OpTypeAll}
			Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(Succeed())
			_, _, _, p := stub.DeleteObjectArgsForCall(0)
			Expect(p.MarkerOwner).To(Equal(meta.AccountOwner(acct)))
			Expect(p.MarkerOwnerDisplayName).To(Equal("acme"))
		})
		DescribeTable("answers the version and the delete marker the store reports, and none for a delete that answers success without one",
			func(ctx SpecContext, res *op.DeleteObjectResult, storeErr error, wantVersion string, wantMarker bool) {
				f.setBucketFlags(ctx, meta.BucketVersioned)
				stub.DeleteObjectReturns(res, storeErr)
				o := &op.DeleteObject{}
				Expect(op.Run(ctx, o, f.req(http.MethodDelete, "plain", "small"))).To(Succeed())
				Expect(o.VersionID).To(Equal(wantVersion))
				Expect(o.DeleteMarker).To(Equal(wantMarker))
			},
			Entry("a delete marker written", &op.DeleteObjectResult{VersionID: "m1", DeleteMarker: true}, nil, "m1", true),
			Entry("a version removed", &op.DeleteObjectResult{VersionID: "v1"}, nil, "v1", false),
			Entry("an unversioned delete", &op.DeleteObjectResult{}, nil, "", false),
			Entry("a missing version, radosgw's 204 with neither header", nil, op.ErrNoSuchKey, "", false),
			Entry("a lost race, radosgw's 204 with neither header", nil, op.ErrConcurrentModification, "", false),
		)
		DescribeTable("answers 501 on a bucket with object lock, a delete marker's included, before the store",
			func(ctx SpecContext, instance string) {
				f.setBucketFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
				r := f.req(http.MethodDelete, "plain", "small")
				r.Object.Instance = instance
				Expect(op.Run(ctx, &op.DeleteObject{}, r)).To(MatchError(op.ErrNotImplemented))
				Expect(stub.DeleteObjectCallCount()).To(BeZero())
			},
			Entry("no version", ""),
			Entry("a version", "v1"),
			Entry("the null version", "null"),
		)
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
