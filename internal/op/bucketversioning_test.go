package op_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The bucket flags a versioning change reads and writes.
const (
	verFlagEnabled   = meta.BucketVersioned
	verFlagSuspended = meta.BucketVersioned | meta.BucketVersionsSuspended
	verFlagMFA       = meta.BucketMFAEnabled
	verFlagLock      = meta.BucketObjLockEnabled
)

var _ = Describe("bucket versioning ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	// setFlags replaces plain's flags.
	setFlags := func(ctx context.Context, flags uint32) {
		GinkgoHelper()
		rec := bucket(ctx)
		rec.Info.Flags = flags
		Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	put := func(status op.VersioningStatus, mfaDelete *bool) *op.PutBucketVersioning {
		return &op.PutBucketVersioning{Status: status, MFADelete: mfaDelete}
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetBucketVersioning", &op.GetBucketVersioning{}, "get_bucket_versioning", policy.S3GetBucketVersioning, op.OpTypeRead),
		Entry("PutBucketVersioning", &op.PutBucketVersioning{}, "set_bucket_versioning", policy.S3PutBucketVersioning, op.OpTypeWrite),
	)

	DescribeTable("answers NoSuchBucket before any permission check",
		func(ctx SpecContext, o op.Op) {
			r := f.req(http.MethodGet, "missing", "")
			r.Identity = f.bob
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrNoSuchBucket))
		},
		Entry("GetBucketVersioning", &op.GetBucketVersioning{}),
		Entry("PutBucketVersioning", put(op.VersioningEnabled, nil)),
	)

	DescribeTable("refuses a requester the authorizer refuses",
		func(ctx SpecContext, o op.Op) {
			r := f.req(http.MethodPut, "plain", "")
			r.Identity = f.bob
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
			Expect(bucket(ctx).Info.Flags).To(BeZero())
		},
		Entry("GetBucketVersioning", &op.GetBucketVersioning{}),
		Entry("PutBucketVersioning", put(op.VersioningEnabled, nil)),
	)

	Describe("GetBucketVersioning", func() {
		DescribeTable("reads versioned(), versioning_enabled() and mfa_enabled() from the bucket's flags",
			func(ctx SpecContext, flags uint32, want op.GetBucketVersioning) {
				setFlags(ctx, flags)
				o := &op.GetBucketVersioning{}
				Expect(op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))).To(Succeed())
				Expect(*o).To(Equal(want), "flags %#x, rgw_op.cc:2749-2756 at v19.2.7", flags)
			},
			Entry("never versioned", uint32(0), op.GetBucketVersioning{}),
			Entry("enabled", uint32(verFlagEnabled), op.GetBucketVersioning{Versioned: true, Enabled: true}),
			Entry("suspended", uint32(verFlagSuspended), op.GetBucketVersioning{Versioned: true}),
			Entry("enabled with MFA delete", uint32(verFlagEnabled|verFlagMFA), op.GetBucketVersioning{Versioned: true, Enabled: true, MFAEnabled: true}),
			Entry("suspended with MFA delete", uint32(verFlagSuspended|verFlagMFA), op.GetBucketVersioning{Versioned: true, MFAEnabled: true}),
		)
	})

	Describe("PutBucketVersioning", func() {
		It("calls Params only once the requester is authorized, and stores what it fills in", func(ctx SpecContext) {
			called := false
			params := func(_ context.Context, o *op.PutBucketVersioning) error {
				called = true
				o.Status = op.VersioningSuspended
				return nil
			}
			r := f.req(http.MethodPut, "plain", "")
			r.Identity = f.bob
			Expect(op.Run(ctx, &op.PutBucketVersioning{Params: params}, r)).To(MatchError(op.ErrAccessDenied))
			Expect(called).To(BeFalse(), "get_params runs in execute, after verify_permission, rgw_op.cc:2779 at v19.2.7")
			Expect(op.Run(ctx, &op.PutBucketVersioning{Params: params}, f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Info.Flags).To(Equal(uint32(verFlagSuspended)))
		})
		It("returns Params' error and writes nothing", func(ctx SpecContext) {
			o := &op.PutBucketVersioning{Params: func(context.Context, *op.PutBucketVersioning) error { return op.ErrMalformedXML }}
			before := bucket(ctx).Version
			Expect(op.Run(ctx, o, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrMalformedXML))
			Expect(bucket(ctx).Version).To(Equal(before))
		})
		It("refuses a request without a status and writes nothing", func(ctx SpecContext) {
			before := bucket(ctx).Version
			Expect(op.Run(ctx, &op.PutBucketVersioning{}, f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrInternalError))
			Expect(bucket(ctx).Version).To(Equal(before))
		})

		DescribeTable("sets the versioning flags from each state, keeping every other flag",
			func(ctx SpecContext, from uint32, status op.VersioningStatus, want uint32) {
				setFlags(ctx, from|meta.BucketDatasyncDisabled)
				Expect(op.Run(ctx, put(status, nil), f.req(http.MethodPut, "plain", ""))).To(Succeed())
				Expect(bucket(ctx).Info.Flags).To(Equal(want|meta.BucketDatasyncDisabled), "from %#x, rgw_op.cc:2837-2843 at v19.2.7", from)
			},
			Entry("Enabled on a bucket never versioned", uint32(0), op.VersioningEnabled, uint32(verFlagEnabled)),
			Entry("Suspended on a bucket never versioned", uint32(0), op.VersioningSuspended, uint32(verFlagSuspended)),
			Entry("Enabled on an enabled bucket", uint32(verFlagEnabled), op.VersioningEnabled, uint32(verFlagEnabled)),
			Entry("Suspended on an enabled bucket", uint32(verFlagEnabled), op.VersioningSuspended, uint32(verFlagSuspended)),
			Entry("Enabled on a suspended bucket", uint32(verFlagSuspended), op.VersioningEnabled, uint32(verFlagEnabled)),
			Entry("Suspended on a suspended bucket", uint32(verFlagSuspended), op.VersioningSuspended, uint32(verFlagSuspended)),
			Entry("Enabled on an object-lock bucket", uint32(verFlagEnabled|verFlagLock), op.VersioningEnabled, uint32(verFlagEnabled|verFlagLock)),
		)

		It("rewrites the instance through PutBucketInfo, which sends its cache notify, even when no flag changes", func(ctx SpecContext) {
			setFlags(ctx, verFlagEnabled)
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketInfoStub = f.store.PutBucketInfo
			f.env.Buckets = buckets
			before := bucket(ctx).Version
			Expect(op.Run(ctx, put(op.VersioningEnabled, nil), f.req(http.MethodPut, "plain", ""))).To(Succeed())
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "put_info, rgw_op.cc:2848 at v19.2.7")
			Expect(buckets.PutBucketAttrsCallCount()).To(BeZero())
			_, rec := buckets.PutBucketInfoArgsForCall(0)
			Expect(rec.Info.Flags).To(Equal(uint32(verFlagEnabled)))
			Expect(bucket(ctx).Version.Ver).To(Equal(before.Ver+1), "modified is set for every valid status, rgw_op.cc:2837-2846 at v19.2.7")
		})

		DescribeTable("refuses any status but Enabled on an object-lock bucket with 409 InvalidBucketState and radosgw's message",
			func(ctx SpecContext, from uint32, mfaDelete *bool, verified bool) {
				setFlags(ctx, from|verFlagLock)
				before := bucket(ctx).Version
				r := f.req(http.MethodPut, "plain", "")
				r.MFAVerified = verified
				err := op.Run(ctx, put(op.VersioningSuspended, mfaDelete), r)
				Expect(err).To(MatchError(op.ErrInvalidBucketState), "rgw_op.cc:2788-2793 at v19.2.7")
				Expect(op.AsError(err).Message).To(Equal("bucket versioning cannot be disabled on buckets with object lock enabled"))
				Expect(bucket(ctx).Version).To(Equal(before))
			},
			Entry("an enabled lock bucket", uint32(verFlagEnabled), nil, false),
			Entry("a lock bucket radosgw left suspended", uint32(verFlagSuspended), nil, false),
			Entry("an enabled lock bucket, to a verified request, as radosgw reads no mfa_verified before the lock check",
				uint32(verFlagEnabled), nil, true),
			Entry("a lock bucket radosgw left suspended, to a verified request", uint32(verFlagSuspended), nil, true),
			Entry("an enabled lock bucket with MFA delete, to a verified request that also clears it",
				uint32(verFlagEnabled|verFlagMFA), new(false), true),
		)
		It("refuses the lock change before the MFA check", func(ctx SpecContext) {
			setFlags(ctx, verFlagEnabled|verFlagLock)
			err := op.Run(ctx, put(op.VersioningSuspended, new(true)), f.req(http.MethodPut, "plain", ""))
			Expect(err).To(MatchError(op.ErrInvalidBucketState), "rgw_op.cc:2788-2793 precede :2795-2817 at v19.2.7")
		})

		DescribeTable("refuses an unverified request that changes MFA delete, or flips the versioning state while it is on, with 403 AccessDenied",
			func(ctx SpecContext, from uint32, status op.VersioningStatus, mfaDelete *bool) {
				setFlags(ctx, from)
				before := bucket(ctx).Version
				err := op.Run(ctx, put(status, mfaDelete), f.req(http.MethodPut, "plain", ""))
				Expect(err).To(MatchError(op.ErrMFARequired), "rgw_op.cc:2795-2817 at v19.2.7")
				Expect(op.AsError(err).Errno).To(Equal(op.ErrMFARequired.Errno), "ERR_MFA_REQUIRED, rgw_common.cc:96 at v19.2.7")
				Expect(bucket(ctx).Version).To(Equal(before), "nothing written")
			},
			Entry("MfaDelete Enabled on a bucket without it", uint32(verFlagEnabled), op.VersioningEnabled, new(true)),
			Entry("MfaDelete Enabled on a bucket never versioned", uint32(0), op.VersioningEnabled, new(true)),
			Entry("MfaDelete Disabled on a bucket with it", uint32(verFlagEnabled|verFlagMFA), op.VersioningEnabled, new(false)),
			Entry("Suspended on an enabled bucket with MFA delete", uint32(verFlagEnabled|verFlagMFA), op.VersioningSuspended, nil),
			Entry("Enabled on a suspended bucket with MFA delete", uint32(verFlagSuspended|verFlagMFA), op.VersioningEnabled, nil),
			Entry("Suspended on a bucket with MFA delete never versioned, whose BUCKET_VERSIONS_SUSPENDED is unset",
				uint32(verFlagMFA), op.VersioningSuspended, nil),
		)
		DescribeTable("accepts an MfaDelete equal to the bucket's, a request that flips nothing, and any verified request",
			func(ctx SpecContext, from uint32, status op.VersioningStatus, mfaDelete *bool, verified bool, want uint32) {
				setFlags(ctx, from)
				r := f.req(http.MethodPut, "plain", "")
				r.MFAVerified = verified
				Expect(op.Run(ctx, put(status, mfaDelete), r)).To(Succeed())
				Expect(bucket(ctx).Info.Flags).To(Equal(want), "rgw_op.cc:2797 and :2828-2843 at v19.2.7")
			},
			Entry("an unverified MfaDelete Disabled on a bucket without it is dropped",
				uint32(verFlagEnabled), op.VersioningSuspended, new(false), false, uint32(verFlagSuspended)),
			Entry("an unverified MfaDelete Enabled on a bucket with it is dropped",
				uint32(verFlagEnabled|verFlagMFA), op.VersioningEnabled, new(true), false, uint32(verFlagEnabled|verFlagMFA)),
			Entry("an unverified Enabled on a bucket with MFA delete never versioned",
				uint32(verFlagMFA), op.VersioningEnabled, nil, false, uint32(verFlagEnabled|verFlagMFA)),
			Entry("an unverified Enabled on an enabled bucket with MFA delete",
				uint32(verFlagEnabled|verFlagMFA), op.VersioningEnabled, nil, false, uint32(verFlagEnabled|verFlagMFA)),
			Entry("an unverified Suspended on a suspended bucket with MFA delete",
				uint32(verFlagSuspended|verFlagMFA), op.VersioningSuspended, nil, false, uint32(verFlagSuspended|verFlagMFA)),
			Entry("a verified MfaDelete Enabled, which sets the flag",
				uint32(0), op.VersioningSuspended, new(true), true, uint32(verFlagSuspended|verFlagMFA)),
			Entry("a verified MfaDelete Disabled, which clears it as the state flips",
				uint32(verFlagEnabled|verFlagMFA), op.VersioningSuspended, new(false), true, uint32(verFlagSuspended)),
			Entry("a verified flip with MFA delete on, which keeps it",
				uint32(verFlagSuspended|verFlagMFA), op.VersioningEnabled, nil, true, uint32(verFlagEnabled|verFlagMFA)),
		)
		It("refuses an admin an MFA change it has not verified, as the check is execute's", func(ctx SpecContext) {
			r := f.req(http.MethodPut, "plain", "")
			r.Identity = f.bob
			r.Identity.Admin = true
			Expect(op.Run(ctx, put(op.VersioningEnabled, new(true)), r)).To(MatchError(op.ErrMFARequired))
			Expect(bucket(ctx).Info.Flags).To(BeZero())
		})

		Describe("a write that loses a race", func() {
			var buckets *opfakes.FakeBucketStore
			// raceWith makes the first write of plain lose to concurrent,
			// another request's change of the bucket as stored, and lets every
			// retry through to the store.
			raceWith := func(concurrent func(rec *op.BucketRecord)) {
				buckets = &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = f.store.GetBucket
				buckets.PutBucketInfoStub = func(ctx context.Context, rec *op.BucketRecord) error {
					if buckets.PutBucketInfoCallCount() == 1 {
						other := bucket(ctx)
						concurrent(other)
						Expect(f.store.PutBucketInfo(ctx, other)).To(Succeed())
					}
					return f.store.PutBucketInfo(ctx, rec)
				}
				f.env.Buckets = buckets
			}

			It("refuses the retry once the write it lost to enabled MFA delete, writing nothing more", func(ctx SpecContext) {
				setFlags(ctx, verFlagEnabled)
				raceWith(func(rec *op.BucketRecord) { rec.Info.Flags |= verFlagMFA })
				err := op.Run(ctx, put(op.VersioningSuspended, new(false)), f.req(http.MethodPut, "plain", ""))
				Expect(err).To(MatchError(op.ErrMFARequired))
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(bucket(ctx).Info.Flags).To(Equal(uint32(verFlagEnabled | verFlagMFA)))
			})
			It("refuses the retry once the write it lost to enabled object lock, writing nothing more", func(ctx SpecContext) {
				setFlags(ctx, verFlagEnabled)
				raceWith(func(rec *op.BucketRecord) { rec.Info.Flags |= verFlagLock })
				err := op.Run(ctx, put(op.VersioningSuspended, nil), f.req(http.MethodPut, "plain", ""))
				Expect(err).To(MatchError(op.ErrInvalidBucketState))
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(bucket(ctx).Info.Flags).To(Equal(uint32(verFlagEnabled | verFlagLock)))
			})
			It("keeps the attrs the write it lost to changed", func(ctx SpecContext) {
				var set tags.Set
				Expect(set.Add("k", "v", tags.MaxBucketTags)).To(Succeed())
				raceWith(func(rec *op.BucketRecord) { rec.Attrs[tags.Attr] = encodeTags(set) })
				Expect(op.Run(ctx, put(op.VersioningEnabled, nil), f.req(http.MethodPut, "plain", ""))).To(Succeed())
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(2))
				got := bucket(ctx)
				Expect(got.Info.Flags).To(Equal(uint32(verFlagEnabled)))
				Expect(got.Attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(set)),
					"radosgw's retry writes s->bucket_attrs back, rgw_op.cc:2847 at v19.2.7")
			})
			It("authorizes the retry again against the ACL the write it lost to stored", func(ctx SpecContext) {
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodedPolicy(p)})
				raceWith(func(rec *op.BucketRecord) {
					rec.Attrs[meta.AttrACL] = encodedPolicy(acl.DefaultPolicy(f.alice.Owner, "Alice"))
				})
				r := f.req(http.MethodPut, "plain", "")
				r.Identity = f.bob
				Expect(op.Run(ctx, put(op.VersioningEnabled, nil), r)).To(MatchError(op.ErrAccessDenied))
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "the retry writes nothing")
				Expect(bucket(ctx).Info.Flags).To(BeZero())
			})
		})
	})

	Describe("through the policy evaluator", func() {
		BeforeEach(func() { f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid)) })
		bobOn := func(effect, action, cond string) []byte {
			c := ""
			if cond != "" {
				c = `,"Condition":` + cond
			}
			return []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"` + effect +
				`","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":"` + action +
				`","Resource":"arn:aws:s3:::plain"` + c + `}]}`)
		}
		asBob := func(method string) *op.Request {
			r := f.req(method, "plain", "")
			r.Identity = f.bob
			return r
		}

		It("authorizes GetBucketVersioning as s3:GetBucketVersioning", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bobOn("Allow", "s3:GetBucketVersioning", "")})
			Expect(op.Run(ctx, &op.GetBucketVersioning{}, asBob(http.MethodGet))).To(Succeed())
			Expect(op.Run(ctx, put(op.VersioningEnabled, nil), asBob(http.MethodPut))).To(MatchError(op.ErrAccessDenied))
		})
		It("authorizes PutBucketVersioning as s3:PutBucketVersioning", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bobOn("Allow", "s3:PutBucketVersioning", "")})
			Expect(op.Run(ctx, put(op.VersioningEnabled, nil), asBob(http.MethodPut))).To(Succeed())
			Expect(bucket(ctx).Info.Flags).To(Equal(uint32(verFlagEnabled)))
			Expect(op.Run(ctx, &op.GetBucketVersioning{}, asBob(http.MethodGet))).To(MatchError(op.ErrAccessDenied))
		})
		It("refuses the owner a PutBucketVersioning a policy denies", func(ctx SpecContext) {
			deny := []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:PutBucketVersioning","Resource":"arn:aws:s3:::plain"}]}`)
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: deny})
			Expect(op.Run(ctx, put(op.VersioningEnabled, nil), f.req(http.MethodPut, "plain", ""))).To(MatchError(op.ErrAccessDenied))
			Expect(bucket(ctx).Info.Flags).To(BeZero())
		})
		DescribeTable("adds the bucket's tags to the environment for a policy that asks, rgw_op.cc:2731-2733 and :2761-2763 at v19.2.7",
			func(ctx SpecContext, method string, o func() op.Op, action string) {
				cond := `{"StringEquals":{"s3:ResourceTag/team":"a"}}`
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bobOn("Allow", action, cond)})
				Expect(op.Run(ctx, o(), asBob(method))).To(MatchError(op.ErrAccessDenied), "untagged")
				var set tags.Set
				Expect(set.Add("team", "a", tags.MaxBucketTags)).To(Succeed())
				f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(set)})
				Expect(op.Run(ctx, o(), asBob(method))).To(Succeed(), "tagged")
			},
			Entry("GetBucketVersioning", http.MethodGet, func() op.Op { return &op.GetBucketVersioning{} }, "s3:GetBucketVersioning"),
			Entry("PutBucketVersioning", http.MethodPut, func() op.Op { return put(op.VersioningEnabled, nil) }, "s3:PutBucketVersioning"),
		)
	})

	DescribeTable("needs its op mask",
		func(ctx SpecContext, method string, o op.Op, mask uint32) {
			r := f.req(method, "plain", "")
			r.Identity.OpMask = mask
			Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
		},
		Entry("GetBucketVersioning, RGW_OP_TYPE_READ, rgw_op.h:1017 at v19.2.7", http.MethodGet, &op.GetBucketVersioning{}, op.OpTypeModify),
		Entry("PutBucketVersioning, RGW_OP_TYPE_WRITE, rgw_op.h:1045 at v19.2.7", http.MethodPut, put(op.VersioningEnabled, nil), op.OpTypeRead|op.OpTypeDelete),
	)
})
