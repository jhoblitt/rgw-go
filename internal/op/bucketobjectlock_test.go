package op_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The messages PutObjectLockConfiguration sets in s->err.message
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8243-L8280 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9178-L9215 at v20.2.4).
const (
	msgLockNeedsVersioning = "Object lock cannot be enabled unless the bucket has versioning enabled"
	msgRetentionPeriod     = "retention period must be a positive integer value"
)

// lockNow is the clock the object-lock specs run on.
var lockNow = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

// lockDoc is a PutObjectLockConfiguration body holding retention, the
// DefaultRetention's elements, in a Rule, or no Rule when retention is "".
func lockDoc(retention string) string {
	rule := ""
	if retention != "" {
		rule = "<Rule><DefaultRetention>" + retention + "</DefaultRetention></Rule>"
	}
	return `<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<ObjectLockEnabled>Enabled</ObjectLockEnabled>` + rule + `</ObjectLockConfiguration>`
}

// lockRule is an enabled lock whose rule is mode for days or years.
func lockRule(mode string, days, years int32) meta.ObjectLock {
	return meta.ObjectLock{Enabled: true, Rule: &meta.ObjectLockRule{DefaultRetention: meta.DefaultRetention{Mode: mode, Days: days, Years: years}}}
}

// lockBytes is l as RGWObjectLock::encode writes it.
func lockBytes(l meta.ObjectLock) []byte {
	e := denc.NewEncoder()
	l.Encode(e, denc.Squid)
	return e.Bytes()
}

var governanceDayDoc = lockDoc("<Mode>GOVERNANCE</Mode><Days>1</Days>")

var _ = Describe("bucket object-lock ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		f.env.Now = func() time.Time { return lockNow }
	})

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
	// setLock gives plain the flags and the encoded lock raw, nil for none.
	setLock := func(ctx context.Context, flags uint32, raw []byte) {
		GinkgoHelper()
		rec := bucket(ctx)
		rec.Info.Flags, rec.Info.ObjLock = flags, raw
		Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	storedLock := func(ctx context.Context) meta.ObjectLock {
		GinkgoHelper()
		l, err := bucket(ctx).Info.ObjectLock()
		Expect(err).NotTo(HaveOccurred())
		return l
	}
	putReq := func(body string) *op.Request { return f.bodyReq(http.MethodPut, "", body) }
	getReq := func() *op.Request { return f.req(http.MethodGet, "plain", "") }
	get := func() op.Op { return &op.GetObjectLockConfiguration{} }
	put := func() op.Op { return &op.PutObjectLockConfiguration{} }

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetObjectLockConfiguration, rgw_op.h:2454-2463 at v19.2.7", get(), "get_bucket_object_lock",
			policy.S3GetBucketObjectLockConfiguration, op.OpTypeRead),
		Entry("PutObjectLockConfiguration, rgw_op.h:2436-2452 at v19.2.7", put(), "put_bucket_object_lock",
			policy.S3PutBucketObjectLockConfiguration, op.OpTypeWrite),
	)

	Describe("error order", func() {
		var (
			seen    []string
			buckets *opfakes.FakeBucketStore
			authzr  *opfakes.FakeAuthorizer
		)
		// Each fake records its step in seen and passes the call to the
		// memstore, or allows it; the body records "read" when it is first
		// read.
		BeforeEach(func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned)
			seen = nil
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
				seen = append(seen, "load")
				return f.store.GetBucket(ctx, tenant, name)
			}
			buckets.PutBucketInfoStub = func(ctx context.Context, rec *op.BucketRecord) error {
				seen = append(seen, "write")
				return f.store.PutBucketInfo(ctx, rec)
			}
			authzr = &opfakes.FakeAuthorizer{}
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return nil
			}
			f.env.Buckets, f.env.Authz = buckets, authzr
		})
		request := func(doc string) *op.Request {
			r := putReq(doc)
			r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
			return r
		}

		none := func(context.Context, *op.Request) {}
		missing := func(_ context.Context, r *op.Request) { r.Bucket = "missing" }
		refused := func(context.Context, *op.Request) {
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		flags := func(fl uint32) func(context.Context, *op.Request) {
			return func(ctx context.Context, _ *op.Request) { setFlags(ctx, fl) }
		}
		body := func(doc string) func(context.Context, *op.Request) {
			return func(_ context.Context, r *op.Request) {
				r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
				r.ContentLength = int64(len(doc))
				r.Header.Set("Content-Length", strconv.Itoa(len(doc)))
			}
		}
		unversionedMalformed := func(ctx context.Context, r *op.Request) {
			setFlags(ctx, 0)
			body("<ObjectLockConfiguration>")(ctx, r)
		}
		noLength := func(_ context.Context, r *op.Request) {
			r.ContentLength = 0
			r.Header.Del("Content-Length")
		}
		overLimit := func(context.Context, *op.Request) {
			f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(governanceDayDoc) - 1)
		}
		writeFails := func(context.Context, *op.Request) {
			buckets.PutBucketInfoStub = func(context.Context, *op.BucketRecord) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}

		DescribeTable("stops at the first step that fails, in radosgw's order",
			func(ctx SpecContext, mk func() op.Op, fail func(context.Context, *op.Request), want error, steps []string) {
				r := request(governanceDayDoc)
				fail(ctx, r)
				Expect(op.Run(ctx, mk(), r)).To(MatchError(want))
				Expect(seen).To(Equal(steps))
			},
			Entry("GetObjectLockConfiguration on a missing bucket, before any permission check", get, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("GetObjectLockConfiguration for a refused requester, before it looks at the flags", get, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("GetObjectLockConfiguration on a bucket without object lock, rgw_op.cc:8326-8332 at v19.2.7", get, none,
				op.ErrNoSuchObjectLockConfig, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration on a missing bucket, its body unread", put, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("PutObjectLockConfiguration for a refused requester, its body unread", put, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration on a bucket never versioned, 409 before a malformed body is read, rgw_op.cc:8245-8251 at v19.2.7",
				put, unversionedMalformed, op.ErrInvalidBucketState, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration on a suspended bucket, 409 before the body is read", put,
				flags(meta.BucketVersioned|meta.BucketVersionsSuspended), op.ErrInvalidBucketState, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration without a Content-Length, 411 before the body is read, rgw_rest.cc:1491-1496 at v19.2.7",
				put, noLength, op.ErrMissingContentLength, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration whose Content-Length is over rgw_max_put_param_size, 416 unread",
				put, overLimit, op.ErrInvalidRange, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration whose body does not parse, writing nothing, rgw_op.cc:8263-8266 at v19.2.7",
				put, body("<ObjectLockConfiguration>"), op.ErrMalformedXML, []string{"load", "verify", "read"}),
			Entry("PutObjectLockConfiguration whose rule's Days are 0, writing nothing, rgw_op.cc:8275-8280 at v19.2.7",
				put, body(lockDoc("<Mode>GOVERNANCE</Mode><Days>0</Days>")), op.ErrInvalidRetentionPeriod, []string{"load", "verify", "read"}),
			Entry("PutObjectLockConfiguration whose rule's Years run past real_time's range, writing nothing",
				put, body(lockDoc("<Mode>GOVERNANCE</Mode><Years>600</Years>")), op.ErrInvalidRetentionPeriod, []string{"load", "verify", "read"}),
			Entry("PutObjectLockConfiguration whose write fails otherwise than by a race, retrying nothing", put, writeFails,
				op.ErrInternalError, []string{"load", "verify", "read", "write"}),
		)
		DescribeTable("refuses an op mask without the op's bit before it reads the body or writes",
			func(ctx SpecContext, mk func() op.Op) {
				o := mk()
				r := request(governanceDayDoc)
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).To(Equal([]string{"load", "verify"}))
			},
			Entry("GetObjectLockConfiguration, without read", get),
			Entry("PutObjectLockConfiguration, without write", put),
		)
		DescribeTable("authorizes its action against the bucket with the ACL permission op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, a policy.Action, perm acl.Permission, steps []string) {
				setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, nil)
				Expect(op.Run(ctx, mk(), request(governanceDayDoc))).To(Succeed())
				Expect(seen).To(Equal(steps))
				Expect(authzr.VerifyBucketCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(a))
				Expect(gotPerm).To(Equal(perm))
			},
			Entry("GetObjectLockConfiguration, READ_ACP, rgw_iam_policy.h:270-289 at v19.2.7", get,
				policy.S3GetBucketObjectLockConfiguration, acl.PermReadACP, []string{"load", "verify"}),
			Entry("PutObjectLockConfiguration, WRITE_ACP, rgw_iam_policy.h:291-311 at v19.2.7", put,
				policy.S3PutBucketObjectLockConfiguration, acl.PermWriteACP, []string{"load", "verify", "read", "write"}),
		)
	})

	Describe("GetObjectLockConfiguration", func() {
		run := func(ctx context.Context) (*op.GetObjectLockConfiguration, error) {
			o := &op.GetObjectLockConfiguration{}
			return o, op.Run(ctx, o, getReq())
		}

		It("answers a bucket without object lock with 404 ObjectLockConfigurationNotFoundError and no message", func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned)
			_, err := run(ctx)
			Expect(err).To(MatchError(op.ErrNoSuchObjectLockConfig), "-ERR_NO_SUCH_OBJECT_LOCK_CONFIGURATION, rgw_op.cc:8328-8331 at v19.2.7")
			e := op.AsError(err)
			Expect(e.Status).To(Equal(http.StatusNotFound))
			Expect(e.Code).To(Equal("ObjectLockConfigurationNotFoundError"))
			Expect(e.Message).To(BeEmpty())
		})
		It("answers a bucket whose flags drop object lock 404 whatever lock its instance last carried", func(ctx SpecContext) {
			setLock(ctx, meta.BucketVersioned, lockBytes(lockRule("COMPLIANCE", 3, 0)))
			_, err := run(ctx)
			Expect(err).To(MatchError(op.ErrNoSuchObjectLockConfig), "obj_lock_enabled() reads the flags alone")
		})
		It("takes RGWObjectLock(), enabled with no rule, for a lock bucket that stores none, as create_bucket leaves it", func(ctx SpecContext) {
			setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, nil)
			o, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Lock).To(Equal(meta.ObjectLock{Enabled: true}), "rgw_object_lock.h:102 at v19.2.7")
		})
		It("takes the stored rule", func(ctx SpecContext) {
			setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, lockBytes(lockRule("COMPLIANCE", 0, 2)))
			o, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Lock).To(Equal(lockRule("COMPLIANCE", 0, 2)))
		})
		It("answers a lock whose encoding does not decode with 500 UnknownError, radosgw's -EIO", func(ctx SpecContext) {
			setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, []byte{1, 1, 1, 0, 0, 0, 1})
			_, err := run(ctx)
			Expect(err).To(MatchError(op.ErrUnknown))
			Expect(op.AsError(err).Status).To(Equal(http.StatusInternalServerError))
		})
		It("writes nothing", func(ctx SpecContext) {
			setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, nil)
			before := bucket(ctx).Version
			_, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(bucket(ctx).Version).To(Equal(before))
		})
	})

	Describe("PutObjectLockConfiguration", func() {
		BeforeEach(func(ctx SpecContext) { setFlags(ctx, meta.BucketVersioned) })
		run := func(ctx context.Context, doc string) error {
			return op.Run(ctx, &op.PutObjectLockConfiguration{}, putReq(doc))
		}

		It("enables object lock on a versioning-enabled bucket and stores the rule, which GetObjectLockConfiguration reads back", func(ctx SpecContext) {
			Expect(run(ctx, governanceDayDoc)).To(Succeed())
			got := bucket(ctx)
			Expect(got.Info.Flags).To(Equal(uint32(meta.BucketVersioned|meta.BucketObjLockEnabled)), "rgw_op.cc:8290-8299 at v19.2.7")
			Expect([]byte(got.Info.ObjLock)).To(Equal(lockBytes(lockRule("GOVERNANCE", 1, 0))), "RGWObjectLock::encode, rgw_object_lock.h:127-135 at v19.2.7")
			g := &op.GetObjectLockConfiguration{}
			Expect(op.Run(ctx, g, getReq())).To(Succeed())
			Expect(g.Lock).To(Equal(lockRule("GOVERNANCE", 1, 0)))
		})
		It("stores a document without a rule as an enabled lock with none", func(ctx SpecContext) {
			Expect(run(ctx, lockDoc(""))).To(Succeed())
			Expect(storedLock(ctx)).To(Equal(meta.ObjectLock{Enabled: true}))
			Expect([]byte(bucket(ctx).Info.ObjLock)).To(Equal([]byte{1, 1, 2, 0, 0, 0, 1, 0}))
		})
		It("sets the flag only when it is missing, keeping every other flag", func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned|meta.BucketDatasyncDisabled)
			Expect(run(ctx, governanceDayDoc)).To(Succeed())
			Expect(bucket(ctx).Info.Flags).To(Equal(uint32(meta.BucketVersioned | meta.BucketDatasyncDisabled | meta.BucketObjLockEnabled)))
			Expect(run(ctx, lockDoc("<Mode>COMPLIANCE</Mode><Days>2</Days>"))).To(Succeed())
			Expect(bucket(ctx).Info.Flags).To(Equal(uint32(meta.BucketVersioned|meta.BucketDatasyncDisabled|meta.BucketObjLockEnabled)),
				"a lock bucket's flags are left as they are")
		})
		It("replaces a rule and then removes it, leaving object lock enabled", func(ctx SpecContext) {
			Expect(run(ctx, governanceDayDoc)).To(Succeed())
			Expect(run(ctx, lockDoc("<Mode>COMPLIANCE</Mode><Years>2</Years>"))).To(Succeed())
			Expect(storedLock(ctx)).To(Equal(lockRule("COMPLIANCE", 0, 2)), "obj_lock = obj_lock, rgw_op.cc:8301 at v19.2.7")
			Expect(run(ctx, lockDoc(""))).To(Succeed())
			Expect(storedLock(ctx)).To(Equal(meta.ObjectLock{Enabled: true}))
			Expect(bucket(ctx).Info.ObjLockEnabled()).To(BeTrue(), "no document turns object lock off")
		})
		It("replaces a stored lock that does not decode", func(ctx SpecContext) {
			setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, []byte{1, 1, 1, 0, 0, 0, 1})
			Expect(run(ctx, governanceDayDoc)).To(Succeed())
			Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
		})
		It("writes the instance through PutBucketInfo, keeping the bucket's attrs", func(ctx SpecContext) {
			var set tags.Set
			Expect(set.Add("team", "a", tags.MaxBucketTags)).To(Succeed())
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(set)})
			before := bucket(ctx)
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketInfoStub = f.store.PutBucketInfo
			f.env.Buckets = buckets
			Expect(run(ctx, governanceDayDoc)).To(Succeed())
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "put_info, rgw_op.cc:8302 at v19.2.7")
			Expect(buckets.PutBucketAttrsCallCount()).To(BeZero())
			got := bucket(ctx)
			Expect(got.Attrs).To(Equal(before.Attrs))
			Expect(got.Version.Ver).To(Equal(before.Version.Ver + 1))
		})
		It("writes the same bytes on Tentacle", func(ctx SpecContext) {
			f = newWriteFixture(ctx, denc.Tentacle)
			f.env.Now = func() time.Time { return lockNow }
			setFlags(ctx, meta.BucketVersioned)
			Expect(run(ctx, governanceDayDoc)).To(Succeed())
			Expect([]byte(bucket(ctx).Info.ObjLock)).To(Equal(lockBytes(lockRule("GOVERNANCE", 1, 0))))
		})
		It("refuses a requester before it reads the body", func(ctx SpecContext) {
			r := putReq(governanceDayDoc)
			r.Identity, r.Body = f.bob, failingReader{}
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, r)).To(MatchError(op.ErrAccessDenied), "get_params runs in execute, rgw_op.cc:8259 at v19.2.7")
		})

		DescribeTable("refuses a bucket whose versioning is not enabled with 409 InvalidBucketState and radosgw's message, writing nothing",
			func(ctx SpecContext, flags uint32) {
				setFlags(ctx, flags)
				before := bucket(ctx).Version
				err := run(ctx, governanceDayDoc)
				Expect(err).To(MatchError(op.ErrInvalidBucketState), "rgw_op.cc:8245-8251 at v19.2.7, :9180-9186 at v20.2.4")
				e := op.AsError(err)
				Expect(e.Status).To(Equal(http.StatusConflict))
				Expect(e.Message).To(Equal(msgLockNeedsVersioning))
				Expect(bucket(ctx).Version).To(Equal(before))
			},
			Entry("never versioned", uint32(0)),
			Entry("suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended)),
			Entry("a lock bucket radosgw left suspended", uint32(meta.BucketVersioned|meta.BucketVersionsSuspended|meta.BucketObjLockEnabled)),
		)

		DescribeTable("answers a document that does not parse or decode with 400 MalformedXML and no message, storing nothing",
			func(ctx SpecContext, doc string) {
				setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, lockBytes(lockRule("COMPLIANCE", 7, 0)))
				before := bucket(ctx).Version
				err := run(ctx, doc)
				Expect(err).To(MatchError(op.ErrMalformedXML))
				Expect(op.AsError(err)).To(Equal(op.ErrMalformedXML))
				Expect(bucket(ctx).Version).To(Equal(before))
				Expect(storedLock(ctx)).To(Equal(lockRule("COMPLIANCE", 7, 0)))
			},
			Entry("an empty body, which expat refuses", ""),
			Entry("an unclosed root", "<ObjectLockConfiguration>"),
			Entry("another root", "<VersioningConfiguration/>"),
			Entry("no ObjectLockEnabled, rgw_object_lock.cc:75 at v19.2.7",
				`<ObjectLockConfiguration><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`),
			Entry("an ObjectLockEnabled other than Enabled, rgw_object_lock.cc:76-77 at v19.2.7",
				strings.Replace(governanceDayDoc, ">Enabled<", ">Disabled<", 1)),
			Entry("a Rule without a DefaultRetention, rgw_object_lock.cc:49 at v19.2.7",
				`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule/></ObjectLockConfiguration>`),
			Entry("a Mode outside GOVERNANCE and COMPLIANCE, rgw_object_lock.cc:15-18 at v19.2.7",
				lockDoc("<Mode>governance</Mode><Days>1</Days>")),
			Entry("both Days and Years, which decode_xml refuses before retention_period_valid, rgw_object_lock.cc:19-23 at v19.2.7",
				lockDoc("<Mode>GOVERNANCE</Mode><Days>1</Days><Years>1</Years>")),
			Entry("neither Days nor Years", lockDoc("<Mode>GOVERNANCE</Mode>")),
			Entry("Days that are no number", lockDoc("<Mode>GOVERNANCE</Mode><Days>one</Days>")),
		)

		DescribeTable("answers a rule whose period is no positive one, or runs past real_time's range, with 400 InvalidRetentionPeriod and radosgw's message, storing nothing",
			func(ctx SpecContext, retention string) {
				before := bucket(ctx)
				err := run(ctx, lockDoc(retention))
				Expect(err).To(MatchError(op.ErrInvalidRetentionPeriod))
				e := op.AsError(err)
				Expect(e.Status).To(Equal(http.StatusBadRequest))
				Expect(e.Message).To(Equal(msgRetentionPeriod), "rgw_op.cc:8275-8280 at v19.2.7, :9210-9215 at v20.2.4")
				got := bucket(ctx)
				Expect(got.Version).To(Equal(before.Version))
				Expect(got.Info.ObjLockEnabled()).To(BeFalse())
			},
			Entry("Days of 0", "<Mode>GOVERNANCE</Mode><Days>0</Days>"),
			Entry("negative Days", "<Mode>COMPLIANCE</Mode><Days>-1</Days>"),
			Entry("Years of 0", "<Mode>GOVERNANCE</Mode><Years>0</Years>"),
			Entry("Years 600 from now, which radosgw's u64 nanoseconds wrap to 2042", "<Mode>GOVERNANCE</Mode><Years>600</Years>"),
			Entry("Years 528 from now, past 2554-07-21", "<Mode>COMPLIANCE</Mode><Years>528</Years>"),
			Entry("Days 2147483647 from now", "<Mode>COMPLIANCE</Mode><Days>2147483647</Days>"),
		)
		It("stores a rule whose period from now ends inside real_time's range", func(ctx SpecContext) {
			Expect(run(ctx, lockDoc("<Mode>COMPLIANCE</Mode><Years>527</Years>"))).To(Succeed())
			Expect(storedLock(ctx)).To(Equal(lockRule("COMPLIANCE", 0, 527)))
		})
		It("judges the period from the env's clock", func(ctx SpecContext) {
			f.env.Now = func() time.Time { return lockNow.AddDate(-1, 0, 0) }
			Expect(run(ctx, lockDoc("<Mode>COMPLIANCE</Mode><Years>528</Years>"))).To(Succeed(), "from 2025-10-21, 528 years end in 2553")
		})

		Describe("the body, as read_all_input reads it under rgw_max_put_param_size", func() {
			It("is read at rgw_max_put_param_size", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(governanceDayDoc))
				Expect(run(ctx, governanceDayDoc)).To(Succeed())
				Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
			})
			It("is InvalidRange unread past rgw_max_put_param_size", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(governanceDayDoc) - 1)
				r := putReq(governanceDayDoc)
				r.Body = failingReader{}
				Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, r)).To(MatchError(op.ErrInvalidRange))
				Expect(bucket(ctx).Info.ObjLockEnabled()).To(BeFalse())
			})
			It("is read when chunked, as RGWOp::read_all_input drops the allow_chunked get_params passes", func(ctx SpecContext) {
				r := f.req(http.MethodPut, "plain", "")
				r.Body, r.ContentLength = strings.NewReader(governanceDayDoc), -1
				Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, r)).To(Succeed(), "rgw_rest.cc:1491-1496 at v19.2.7")
				Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
			})
			It("returns the body's verification verdict from its final Read, before parsing", func(ctx SpecContext) {
				r := putReq(governanceDayDoc)
				r.Body = io.MultiReader(strings.NewReader(governanceDayDoc), errReader{op.ErrContentSHA256Mismatch})
				Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				Expect(bucket(ctx).Info.ObjLockEnabled()).To(BeFalse())
			})
		})
	})

	Describe("a configuration write that loses a race", func() {
		var buckets *opfakes.FakeBucketStore
		// raceWith makes the first write of plain lose to concurrent, another
		// request's change of the bucket as stored, and lets every retry
		// through to the store.
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
		BeforeEach(func(ctx SpecContext) { setFlags(ctx, meta.BucketVersioned) })

		It("is retried over the bucket as the retry read it, keeping the other write's change", func(ctx SpecContext) {
			var set tags.Set
			Expect(set.Add("team", "b", tags.MaxBucketTags)).To(Succeed())
			raceWith(func(rec *op.BucketRecord) { rec.Attrs[tags.Attr] = encodeTags(set) })
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, putReq(governanceDayDoc))).To(Succeed())
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(2), "the first try lost, the retry wrote, rgw_op.cc:8289-8304 at v19.2.7")
			got := bucket(ctx)
			Expect(got.Attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(set)))
			Expect(got.Info.Flags).To(Equal(uint32(meta.BucketVersioned | meta.BucketObjLockEnabled)))
			Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
		})
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketInfoReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, putReq(governanceDayDoc))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(16), "rgw_op.h:184-197 at v19.2.7")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			_, rec := buckets.PutBucketInfoArgsForCall(15)
			Expect([]byte(rec.Info.ObjLock)).To(Equal(lockBytes(lockRule("GOVERNANCE", 1, 0))), "the body read once")
		})
		It("checks versioning again when the flag is still missing, refusing a bucket the write it lost to suspended", func(ctx SpecContext) {
			raceWith(func(rec *op.BucketRecord) { rec.Info.Flags |= meta.BucketVersionsSuspended })
			err := op.Run(ctx, &op.PutObjectLockConfiguration{}, putReq(governanceDayDoc))
			Expect(err).To(MatchError(op.ErrInvalidBucketState), "rgw_op.cc:8290-8297 at v19.2.7")
			Expect(op.AsError(err).Message).To(Equal(msgLockNeedsVersioning))
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "the retry writes nothing")
			Expect(bucket(ctx).Info.Flags).To(Equal(uint32(meta.BucketVersioned | meta.BucketVersionsSuspended)))
		})
		It("replaces the rule of a bucket the write it lost to locked, without checking versioning again", func(ctx SpecContext) {
			raceWith(func(rec *op.BucketRecord) {
				rec.Info.Flags |= meta.BucketObjLockEnabled | meta.BucketVersionsSuspended
				rec.Info.SetObjectLock(lockRule("COMPLIANCE", 9, 0))
			})
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, putReq(governanceDayDoc))).To(Succeed(),
				"the flag is set, so the retry makes no versioning check, rgw_op.cc:8290-8301 at v19.2.7")
			Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
		})
		It("stops without writing when the name now names another bucket", func(ctx SpecContext) {
			other := bucket(ctx)
			other.Info.Bucket.ID = "another-instance"
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
				if buckets.GetBucketCallCount() == 1 {
					return f.store.GetBucket(ctx, tenant, name)
				}
				return other, nil
			}
			buckets.PutBucketInfoReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, putReq(governanceDayDoc))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "radosgw writes into the bucket re-created under the name")
			Expect(bucket(ctx)).To(Equal(before))
		})

		Describe("to a write that changes the requester's permission", func() {
			// bobCanWriteACP is alice's ACL with WRITE_ACP, the permission
			// PutObjectLockConfiguration maps to, granted to bob.
			bobCanWriteACP := func() []byte {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				return encodedPolicy(p)
			}
			denyBob := func(action string) []byte {
				return fmt.Appendf(nil,
					`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":%q,"Resource":"arn:aws:s3:::plain"}]}`,
					action)
			}
			asBob := func() *op.Request {
				r := putReq(governanceDayDoc)
				r.Identity = f.bob
				return r
			}
			BeforeEach(func(ctx SpecContext) {
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: bobCanWriteACP()})
			})

			DescribeTable("refuses the retry once the write it lost to revoked it, leaving the revoking bucket",
				func(ctx SpecContext, revoke func(rec *op.BucketRecord)) {
					raceWith(revoke)
					Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, asBob())).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketInfoCallCount()).To(Equal(1), "the retry writes nothing")
					got := bucket(ctx)
					Expect(got.Info.ObjLockEnabled()).To(BeFalse())
				},
				Entry("an ACL that drops the grant", func(rec *op.BucketRecord) { rec.Attrs[meta.AttrACL] = f.aliceACL }),
				Entry("a policy that denies s3:PutBucketObjectLockConfiguration", func(rec *op.BucketRecord) {
					rec.Attrs[op.AttrIAMPolicy] = denyBob("s3:PutBucketObjectLockConfiguration")
				}),
			)
			It("writes on a retry whose requester still holds it", func(ctx SpecContext) {
				raceWith(func(rec *op.BucketRecord) {
					rec.Attrs[op.AttrIAMPolicy] = denyBob("s3:GetBucketObjectLockConfiguration")
				})
				Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, asBob())).To(Succeed())
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(2))
				Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
			})
			It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
				raceWith(func(rec *op.BucketRecord) { rec.Attrs[meta.AttrACL] = f.aliceACL })
				r := asBob()
				r.Identity.Admin = true
				Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, r)).To(Succeed())
				Expect(buckets.PutBucketInfoCallCount()).To(Equal(2))
				Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
			})
		})
	})

	Describe("through the policy evaluator", func() {
		BeforeEach(func(ctx SpecContext) {
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			setLock(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled, nil)
		})
		bobOn := func(action, cond string) []byte {
			c := ""
			if cond != "" {
				c = `,"Condition":` + cond
			}
			return []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":"` +
				action + `","Resource":"arn:aws:s3:::plain"` + c + `}]}`)
		}
		asBob := func(r *op.Request) *op.Request {
			r.Identity = f.bob
			return r
		}

		It("authorizes GetObjectLockConfiguration as s3:GetBucketObjectLockConfiguration alone", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bobOn("s3:GetBucketObjectLockConfiguration", "")})
			Expect(op.Run(ctx, &op.GetObjectLockConfiguration{}, asBob(getReq()))).To(Succeed())
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, asBob(putReq(governanceDayDoc)))).To(MatchError(op.ErrAccessDenied))
		})
		It("authorizes PutObjectLockConfiguration as s3:PutBucketObjectLockConfiguration alone", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bobOn("s3:PutBucketObjectLockConfiguration", "")})
			Expect(op.Run(ctx, &op.PutObjectLockConfiguration{}, asBob(putReq(governanceDayDoc)))).To(Succeed())
			Expect(storedLock(ctx)).To(Equal(lockRule("GOVERNANCE", 1, 0)))
			Expect(op.Run(ctx, &op.GetObjectLockConfiguration{}, asBob(getReq()))).To(MatchError(op.ErrAccessDenied))
		})
		DescribeTable("adds the bucket's tags to the environment for a policy that asks",
			func(ctx SpecContext, o func() op.Op, req func() *op.Request, action string) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: bobOn(action, `{"StringEquals":{"s3:ResourceTag/team":"a"}}`)})
				Expect(op.Run(ctx, o(), asBob(req()))).To(MatchError(op.ErrAccessDenied), "untagged")
				var set tags.Set
				Expect(set.Add("team", "a", tags.MaxBucketTags)).To(Succeed())
				f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(set)})
				Expect(op.Run(ctx, o(), asBob(req()))).To(Succeed(), "tagged")
			},
			Entry("GetObjectLockConfiguration, rgw_op.cc:8313-8317 at v19.2.7", get, getReq, "s3:GetBucketObjectLockConfiguration"),
			Entry("PutObjectLockConfiguration, rgw_op.cc:8230-8234 at v19.2.7", put, func() *op.Request { return putReq(governanceDayDoc) },
				"s3:PutBucketObjectLockConfiguration"),
		)
	})
})
