package op_test

import (
	"context"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// instanceForm is gen_rand_obj_instance_name's: 31 characters of
// gen_rand_alphanumeric_no_underscore's table.
const instanceForm = `^[A-Za-z0-9.-]{31}$`

const (
	flagsEnabled   = uint32(meta.BucketVersioned)
	flagsSuspended = uint32(meta.BucketVersioned | meta.BucketVersionsSuspended)
	flagsLocked    = uint32(meta.BucketVersioned | meta.BucketObjLockEnabled)
)

var _ = Describe("VersionForWrite", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	// loaded is alice's request for k in plain, its flags set, naming
	// instance, with the bucket loaded as Init loads it.
	loaded := func(ctx context.Context, flags uint32, instance string) *op.Request {
		GinkgoHelper()
		if flags != 0 {
			f.setBucketFlags(ctx, flags)
		}
		r := f.req(http.MethodPut, "plain", "k")
		r.Object.Instance = instance
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		r.BucketRec = rec
		return r
	}

	DescribeTable("is empty on a bucket whose versioning is not enabled, whatever versionId the request names",
		func(ctx SpecContext, flags uint32, instance string) {
			Expect(op.VersionForWrite(loaded(ctx, flags, instance))).To(BeEmpty())
		},
		Entry("never versioned", uint32(0), ""),
		Entry("never versioned, naming the null version", uint32(0), "null"),
		Entry("never versioned, naming a version", uint32(0), "v1"),
		Entry("suspended", flagsSuspended, ""),
		Entry("suspended, naming the null version", flagsSuspended, "null"),
		Entry("suspended, naming a version", flagsSuspended, "v1"),
		Entry("the suspended flag alone, which radosgw never writes", uint32(meta.BucketVersionsSuspended), ""),
	)
	DescribeTable("is a new instance on each call on a bucket whose versioning is enabled, whatever versionId the request names",
		func(ctx SpecContext, instance string) {
			r := loaded(ctx, flagsEnabled, instance)
			first, second := op.VersionForWrite(r), op.VersionForWrite(r)
			Expect(first).To(MatchRegexp(instanceForm))
			Expect(second).To(MatchRegexp(instanceForm))
			Expect(second).NotTo(Equal(first))
		},
		Entry("naming none", ""),
		Entry("naming the null version", "null"),
		Entry("naming a version", "v1"),
	)
})

var _ = Describe("ResponseVersionID", func() {
	DescribeTable("names a version by its instance and the null version null, on a bucket whose versioning was ever enabled",
		func(flags uint32, instance, want string) {
			rec := &op.BucketRecord{Info: meta.BucketInfo{Flags: flags}}
			Expect(op.ResponseVersionID(rec, instance)).To(Equal(want))
		},
		Entry("an instance on an enabled bucket", flagsEnabled, "v1", "v1"),
		Entry("an instance on a suspended bucket", flagsSuspended, "v1", "v1"),
		Entry("the empty instance on an enabled bucket", flagsEnabled, "", "null"),
		Entry("the empty instance on a suspended bucket", flagsSuspended, "", "null"),
		Entry("the null instance on a suspended bucket", flagsSuspended, "null", "null"),
		Entry("the empty instance on a bucket never versioned", uint32(0), "", ""),
		Entry("the null instance on a bucket never versioned", uint32(0), "null", ""),
		Entry("the empty instance under the suspended flag alone, which radosgw never writes", uint32(meta.BucketVersionsSuspended), "", ""),
	)
})

var _ = Describe("the writes that create a head on a versioned bucket", func() {
	// writeFunc runs one of the writes as requester's request for k in
	// plain, naming instance, and returns the key it handed its store and
	// the version id it reported.
	type writeFunc func(ctx context.Context, instance string) (meta.ObjKey, string, error)
	var (
		f         *writeFixture
		stub      *opfakes.FakeObjectStore
		mp        *opfakes.FakeMultipartStore
		requester op.Identity
		writes    map[string]writeFunc
	)
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		stub = &opfakes.FakeObjectStore{}
		stub.PutObjectReturns(&op.PutResult{ETag: "e"}, nil)
		stub.CopyObjectReturns(&op.PutResult{ETag: "e"}, nil)
		stub.PrefetchObjectReturns(&op.ObjectState{
			Exists: true, Key: meta.ObjKey{Name: "small"}, Size: 1,
			Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrETag: []byte("e")},
		}, nil)
		f.env.Objects = stub
		mp = &opfakes.FakeMultipartStore{}
		mp.CompleteStub = func(_ context.Context, up *op.Upload, _ []op.CompletePart) (*op.PutResult, error) {
			return &op.PutResult{ETag: "e-1", Version: up.Key.Instance}, nil
		}
		f.env.Multipart = mp
		requester = f.alice
		writes = map[string]writeFunc{"PutObject": func(ctx context.Context, instance string) (meta.ObjKey, string, error) {
			r := f.req(http.MethodPut, "plain", "k")
			r.Object.Instance, r.Identity = instance, requester
			o := &op.PutObject{Body: strings.NewReader("x"), Size: 1}
			if err := op.Run(ctx, o, r); err != nil {
				return meta.ObjKey{}, "", err
			}
			_, _, key, _, _ := stub.PutObjectArgsForCall(stub.PutObjectCallCount() - 1)
			return key, o.VersionID, nil
		}, "CopyObject": func(ctx context.Context, instance string) (meta.ObjKey, string, error) {
			r := f.req(http.MethodPut, "plain", "k")
			r.Object.Instance, r.Identity = instance, requester
			o := f.copyOf("small")
			if err := op.Run(ctx, o, r); err != nil {
				return meta.ObjKey{}, "", err
			}
			_, _, _, key, _ := stub.CopyObjectArgsForCall(stub.CopyObjectCallCount() - 1)
			return key, o.VersionID, nil
		}, "CompleteMultipartUpload": func(ctx context.Context, instance string) (meta.ObjKey, string, error) {
			r := f.bodyReq(http.MethodPost, "k", completeDoc(xmlPart("1", `"x"`)))
			r.Object.Instance, r.Identity = instance, requester
			o := &op.CompleteMultipart{UploadID: "2~u"}
			if err := op.Run(ctx, o, r); err != nil {
				return meta.ObjKey{}, "", err
			}
			_, up, _ := mp.CompleteArgsForCall(mp.CompleteCallCount() - 1)
			return up.Key, o.VersionID, nil
		}}
	})

	DescribeTable("write the plain object, or the null version, on a bucket whose versioning is not enabled, ignoring a versionId",
		func(ctx SpecContext, flags uint32, wantID string) {
			if flags != 0 {
				f.setBucketFlags(ctx, flags)
			}
			for name, w := range writes {
				for _, instance := range []string{"", "null", "v1"} {
					key, id, err := w(ctx, instance)
					Expect(err).NotTo(HaveOccurred(), "%s naming %q", name, instance)
					Expect(key).To(Equal(meta.ObjKey{Name: "k"}), "%s naming %q", name, instance)
					Expect(id).To(Equal(wantID), "%s naming %q", name, instance)
				}
			}
		},
		Entry("never versioned, where the object has no version id", uint32(0), ""),
		Entry("suspended, where the null version's id is null", flagsSuspended, "null"),
	)
	It("write a new version on a bucket whose versioning is enabled, ignoring a versionId, and report its id", func(ctx SpecContext) {
		f.setBucketFlags(ctx, flagsEnabled)
		for name, w := range writes {
			seen := map[string]bool{}
			for _, instance := range []string{"", "null", "v1"} {
				key, id, err := w(ctx, instance)
				Expect(err).NotTo(HaveOccurred(), "%s naming %q", name, instance)
				Expect(key.Name).To(Equal("k"))
				Expect(key.Instance).To(MatchRegexp(instanceForm), "%s naming %q", name, instance)
				Expect(id).To(Equal(key.Instance), "%s naming %q", name, instance)
				Expect(seen).NotTo(HaveKey(key.Instance), "%s writes a new instance each time", name)
				seen[key.Instance] = true
			}
		}
	})
	DescribeTable("answer 501 NotImplemented on a bucket with object lock and write nothing",
		func(ctx SpecContext, flags uint32) {
			f.setBucketFlags(ctx, flags)
			for name, w := range writes {
				_, _, err := w(ctx, "")
				Expect(err).To(MatchError(op.ErrNotImplemented), name)
			}
			Expect(stub.PutObjectCallCount()).To(BeZero())
			Expect(stub.CopyObjectCallCount()).To(BeZero())
			Expect(mp.CompleteCallCount()).To(BeZero())
		},
		Entry("versioned with object lock", flagsLocked),
		Entry("suspended with object lock", flagsLocked|uint32(meta.BucketVersionsSuspended)),
		Entry("the object-lock flag alone, which radosgw never writes", uint32(meta.BucketObjLockEnabled)),
	)
	It("refuse an object-lock bucket only once the requester is authorized", func(ctx SpecContext) {
		f.setBucketFlags(ctx, flagsLocked)
		requester = f.bob
		for name, w := range writes {
			_, _, err := w(ctx, "")
			Expect(err).To(MatchError(op.ErrAccessDenied), name)
		}
		r := f.req(http.MethodPut, "plain", "k")
		r.Identity = f.bob
		Expect(op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 1}, r)).To(MatchError(op.ErrAccessDenied), "before the body is read")
	})
	It("report the version a completion's store answers with, which for a completion it finishes is the version that completion wrote", func(ctx SpecContext) {
		f.setBucketFlags(ctx, flagsEnabled)
		mp.CompleteStub = nil
		mp.CompleteReturns(&op.PutResult{ETag: "e-1", Version: "recorded"}, nil)
		_, id, err := writes["CompleteMultipartUpload"](ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(id).To(Equal("recorded"))
		mp.CompleteReturns(&op.PutResult{ETag: "e-1"}, nil)
		_, id, err = writes["CompleteMultipartUpload"](ctx, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(id).To(Equal("null"), "a recorded null version")
	})

	Describe("CopyObject's source", func() {
		// copyReading is alice's copy of plain's small, naming instance, whose
		// read resolves to resolved.
		copyReading := func(ctx context.Context, instance, resolved string) *op.CopyObject {
			GinkgoHelper()
			stub.PrefetchObjectReturns(&op.ObjectState{
				Exists: true, Key: meta.ObjKey{Name: "small", Instance: resolved}, Size: 1,
				Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrETag: []byte("e")},
			}, nil)
			o := f.copyOf("small")
			o.SrcKey.Instance = instance
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			_, _, key := stub.PrefetchObjectArgsForCall(stub.PrefetchObjectCallCount() - 1)
			Expect(key).To(Equal(meta.ObjKey{Name: "small", Instance: instance}), "the source is read as named")
			_, src, _, _, _ := stub.CopyObjectArgsForCall(stub.CopyObjectCallCount() - 1)
			Expect(src.Key.Instance).To(Equal(resolved), "the copy is of the version read")
			return o
		}

		DescribeTable("is the version read, named by its bucket's versioning",
			func(ctx SpecContext, flags uint32, instance, resolved, want string) {
				if flags != 0 {
					f.setBucketFlags(ctx, flags)
				}
				Expect(copyReading(ctx, instance, resolved).SrcVersionID).To(Equal(want))
			},
			Entry("a version named", flagsEnabled, "v1", "v1", "v1"),
			Entry("the version current on the key, which the read followed to", flagsEnabled, "", "v2", "v2"),
			Entry("the null version current on the key", flagsEnabled, "", "", "null"),
			Entry("the null version named on a suspended bucket", flagsSuspended, "null", "null", "null"),
			Entry("an object of a bucket never versioned", uint32(0), "", "", ""),
			Entry("the null version named on a bucket never versioned", uint32(0), "null", "null", ""),
		)
		It("is named by the source bucket's versioning, not the destination's", func(ctx SpecContext) {
			_, err := f.store.CreateBucket(ctx, op.CreateBucketParams{
				Name: "versioned", Owner: f.alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"},
				Attrs: map[string][]byte{meta.AttrACL: f.aliceACL},
			})
			Expect(err).NotTo(HaveOccurred())
			rec, err := f.store.GetBucket(ctx, "", "versioned")
			Expect(err).NotTo(HaveOccurred())
			rec.Info.Flags |= flagsSuspended
			Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
			o := f.copyOf("small")
			o.SrcBucket = "versioned"
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", "k"))).To(Succeed())
			Expect(o.SrcVersionID).To(Equal("null"))
			Expect(o.VersionID).To(BeEmpty(), "plain was never versioned")
		})
	})

	Describe("an append", func() {
		appendTo := func(ctx context.Context, r *op.Request) error {
			return op.Run(ctx, &op.PutObject{Body: failingReader{}, Size: 1, Append: true}, r)
		}
		DescribeTable("is 409 InvalidBucketState on a bucket whose versioning was ever enabled, as RGWPutObj::execute answers, before the body is read",
			func(ctx SpecContext, flags uint32) {
				f.setBucketFlags(ctx, flags)
				Expect(appendTo(ctx, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidBucketState))
				Expect(stub.PutObjectCallCount()).To(BeZero())
			},
			Entry("enabled", flagsEnabled),
			Entry("suspended", flagsSuspended),
			Entry("with object lock, which radosgw refuses so ahead of any lock", flagsLocked),
		)
		It("is 409 on a versioned bucket with a default encryption, which radosgw's append never applies", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			f.setBucketFlags(ctx, flagsEnabled)
			Expect(appendTo(ctx, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrInvalidBucketState))
		})
		DescribeTable("is 501 NotImplemented on a bucket whose versioning was never enabled, where rgw-go does not append",
			func(ctx SpecContext, flags uint32) {
				if flags != 0 {
					f.setBucketFlags(ctx, flags)
				}
				Expect(appendTo(ctx, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrNotImplemented))
				Expect(stub.PutObjectCallCount()).To(BeZero())
			},
			Entry("no flags", uint32(0)),
			Entry("the suspended flag alone, which radosgw takes for an unversioned bucket", uint32(meta.BucketVersionsSuspended)),
		)
		It("is refused only once the requester is authorized", func(ctx SpecContext) {
			f.setBucketFlags(ctx, flagsEnabled)
			r := f.req(http.MethodPut, "plain", "k")
			r.Identity = f.bob
			Expect(appendTo(ctx, r)).To(MatchError(op.ErrAccessDenied))
		})
		It("is refused after the quota, which execute checks first", func(ctx SpecContext) {
			f.setBucketFlags(ctx, flagsEnabled)
			stats := &opfakes.FakeStatsStore{}
			stats.CheckQuotaReturns(op.ErrQuotaExceeded)
			f.env.Stats = stats
			Expect(appendTo(ctx, f.req(http.MethodPut, "plain", "k"))).To(MatchError(op.ErrQuotaExceeded))
		})
	})
})
