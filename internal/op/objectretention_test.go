package op_test

import (
	"context"
	"errors"
	"fmt"
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

// The messages the retention and legal-hold ops set in s->err.message
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8358-L8588 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9293-L9556 at v20.2.4).
const (
	msgRetentionNoLock = "object retention can't be set if bucket object lock not configured"
	msgLegalHoldNoLock = "object legal hold can't be set if bucket object lock not enabled"
	msgLockNotSet      = "bucket object lock not configured"
	msgRetainUntilPast = "the retain-until date must be in the future"
	msgShortens        = "proposed retain-until date shortens an existing retention period and governance bypass check failed"
	msgToGovernance    = "can't change retention mode from COMPLIANCE to GOVERNANCE"
	msgFromGovernance  = "can't change retention mode from GOVERNANCE without governance bypass"
)

// retentionDoc is a PutObjectRetention body in S3's namespace.
func retentionDoc(mode, until string) string {
	return `<Retention xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Mode>` + mode +
		`</Mode><RetainUntilDate>` + until + `</RetainUntilDate></Retention>`
}

// legalHoldDoc is a PutObjectLegalHold body in S3's namespace.
func legalHoldDoc(status string) string {
	return `<LegalHold xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>` + status + `</Status></LegalHold>`
}

// retentionBytes is the retention as RGWObjectRetention::encode writes it.
func retentionBytes(mode string, until time.Time) []byte {
	e := denc.NewEncoder()
	meta.ObjectRetention{Mode: mode, RetainUntil: until}.Encode(e, denc.Squid)
	return e.Bytes()
}

// legalHoldBytes is the legal hold as RGWObjectLegalHold::encode writes it.
func legalHoldBytes(status string) []byte {
	e := denc.NewEncoder()
	meta.ObjectLegalHold{Status: status}.Encode(e, denc.Squid)
	return e.Bytes()
}

// day is midnight UTC of the day in 2030, for retain-until dates.
func day(d int) time.Time { return time.Date(2030, 1, d, 0, 0, 0, 0, time.UTC) }

// The specs' fixed values: the two versions of k they write, the current one,
// named without an instance, and the noncurrent v1; retentions to
// 2030-01-03; and the documents they send.
var (
	lockCurrent      = meta.ObjKey{Name: "k"}
	lockNoncurrent   = meta.ObjKey{Name: "k", Instance: "v1"}
	governanceTo3    = meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: day(3)}
	complianceTo3    = meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: day(3)}
	governanceTo1Doc = retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z")
	legalHoldOnDoc   = legalHoldDoc("ON")
)

// markerHeads is an object store whose StatObject reads the key named name,
// named without an instance, as a current delete marker, whose state does not
// exist.
type markerHeads struct {
	op.ObjectStore
	name string
}

func (m markerHeads) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	if key.Name == m.name && key.Instance == "" {
		return &op.ObjectState{Bucket: rec, Key: meta.ObjKey{Name: key.Name, Instance: "dm"}, DeleteMarker: true}, nil
	}
	return m.ObjectStore.StatObject(ctx, rec, key)
}

var _ = Describe("object retention and legal hold ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) {
		f = newWriteFixture(ctx, denc.Squid)
		f.env.Now = func() time.Time { return lockNow }
		f.setBucketFlags(ctx, meta.BucketVersioned|meta.BucketObjLockEnabled)
		f.put(ctx, "k", []byte("current"))
		_, err := f.store.PutObject(ctx, f.rec, lockNoncurrent, strings.NewReader("old"),
			op.PutParams{Size: 3, Attrs: map[string][]byte{meta.AttrACL: f.aliceACL}})
		Expect(err).NotTo(HaveOccurred())
	})

	stat := func(ctx context.Context, key meta.ObjKey) *op.ObjectState {
		GinkgoHelper()
		st, err := f.store.StatObject(ctx, f.rec, key)
		Expect(err).NotTo(HaveOccurred())
		return st
	}
	setAttr := func(ctx context.Context, key meta.ObjKey, name string, v []byte) {
		GinkgoHelper()
		Expect(f.store.SetObjectAttrs(ctx, stat(ctx, key), map[string][]byte{name: v}, nil)).To(Succeed())
	}
	setFlags := func(ctx context.Context, flags uint32) {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		rec.Info.Flags = flags
		Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}
	// req is alice's request for key, whose instance is the versionId, with
	// body and its Content-Length.
	req := func(method string, key meta.ObjKey, body string) *op.Request {
		r := f.bodyReq(method, key.Name, body)
		r.Object = key
		return r
	}
	putRetention := func(ctx context.Context, key meta.ObjKey, doc string, bypassHeader bool) error {
		return op.Run(ctx, &op.PutObjectRetention{BypassHeader: bypassHeader}, req(http.MethodPut, key, doc))
	}
	getRetention := func(ctx context.Context, key meta.ObjKey) (meta.ObjectRetention, error) {
		o := &op.GetObjectRetention{}
		err := op.Run(ctx, o, req(http.MethodGet, key, ""))
		return o.Retention, err
	}
	putLegalHold := func(ctx context.Context, key meta.ObjKey, doc string) error {
		return op.Run(ctx, &op.PutObjectLegalHold{}, req(http.MethodPut, key, doc))
	}
	getLegalHold := func(ctx context.Context, key meta.ObjKey) (meta.ObjectLegalHold, error) {
		o := &op.GetObjectLegalHold{}
		err := op.Run(ctx, o, req(http.MethodGet, key, ""))
		return o.LegalHold, err
	}
	expectError := func(err, want error, message string) {
		GinkgoHelper()
		Expect(err).To(MatchError(want))
		Expect(op.AsError(err).Message).To(Equal(message))
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("PutObjectRetention, rgw_op.h:2465-2481 at v19.2.7", &op.PutObjectRetention{}, "put_obj_retention",
			policy.S3PutObjectRetention, op.OpTypeWrite),
		Entry("GetObjectRetention, rgw_op.h:2483-2494 at v19.2.7", &op.GetObjectRetention{}, "get_obj_retention",
			policy.S3GetObjectRetention, op.OpTypeRead),
		Entry("PutObjectLegalHold, rgw_op.h:2496-2509 at v19.2.7", &op.PutObjectLegalHold{}, "put_obj_legal_hold",
			policy.S3PutObjectLegalHold, op.OpTypeWrite),
		Entry("GetObjectLegalHold, rgw_op.h:2511-2522 at v19.2.7", &op.GetObjectLegalHold{}, "get_obj_legal_hold",
			policy.S3GetObjectLegalHold, op.OpTypeRead),
	)

	DescribeTable("BypassGovernanceHeader reads x-amz-bypass-governance-retention url-decoded and \"true\" in any case, rgw_rest_s3.cc:4523-4527 at v19.2.7",
		func(v string, want bool) { Expect(op.BypassGovernanceHeader(v)).To(Equal(want)) },
		Entry("true", "true", true),
		Entry("TRUE, iequals", "TRUE", true),
		Entry("%74rue, url_decode first", "%74rue", true),
		Entry("yes, which is not true", "yes", false),
		Entry("an absent header, read as empty", "", false),
		Entry("true with a space after it, which nothing trims", "true ", false),
		Entry("true with an encoded NUL after it, which iequals compares", "true%00", false),
		Entry("a bad escape, which url_decode empties", "%zztrue", false),
	)

	Describe("PutObjectRetention", func() {
		It("stores the retention on the current version as RGWObjectRetention version 2, its nanoseconds kept, which GetObjectRetention reads back", func(ctx SpecContext) {
			until := time.Date(2030, 1, 1, 0, 0, 0, 123456789, time.UTC)
			Expect(putRetention(ctx, lockCurrent, retentionDoc("GOVERNANCE", "2030-01-01T00:00:00.123456789Z"), false)).To(Succeed())
			Expect(stat(ctx, lockCurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, retentionBytes("GOVERNANCE", until)))
			Expect(stat(ctx, lockNoncurrent).Attrs).NotTo(HaveKey(meta.AttrObjectRetention), "the noncurrent version is left alone")
			got, err := getRetention(ctx, lockCurrent)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: until}))
		})
		It("writes every attr the version carried back with the retention, as modify_obj_attrs does", func(ctx SpecContext) {
			var set tags.Set
			Expect(set.Add("team", "a", tags.MaxObjectTags)).To(Succeed())
			setAttr(ctx, lockCurrent, tags.Attr, encodeTags(set))
			before := stat(ctx, lockCurrent).Attrs
			Expect(putRetention(ctx, lockCurrent, retentionDoc("COMPLIANCE", "2030-01-01T00:00:00Z"), false)).To(Succeed())
			after := stat(ctx, lockCurrent).Attrs
			for name, v := range before {
				Expect(after).To(HaveKeyWithValue(name, v))
			}
			Expect(after).To(HaveLen(len(before) + 1))
		})
		It("writes a noncurrent version named by versionId, leaving the current alone, and GetObjectRetention reads it by versionId", func(ctx SpecContext) {
			Expect(putRetention(ctx, lockNoncurrent, retentionDoc("COMPLIANCE", "2030-01-02T00:00:00Z"), false)).To(Succeed())
			Expect(stat(ctx, lockNoncurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, retentionBytes("COMPLIANCE", day(2))))
			Expect(stat(ctx, lockCurrent).Attrs).NotTo(HaveKey(meta.AttrObjectRetention))
			got, err := getRetention(ctx, lockNoncurrent)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: day(2)}))
			_, err = getRetention(ctx, lockCurrent)
			Expect(err).To(MatchError(op.ErrNoSuchObjectLockConfig), "the current version has none")
		})
		It("writes the null version named by versionId=null", func(ctx SpecContext) {
			null := meta.ObjKey{Name: "k", Instance: meta.NullInstance}
			Expect(putRetention(ctx, null, retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false)).To(Succeed())
			got, err := getRetention(ctx, null)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.RetainUntil).To(Equal(day(1)))
		})
		It("hands SetObjectAttrs the state Init read, whose mtime the store moves on by a nanosecond and whose write tag it replaces", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			st := &op.ObjectState{
				Bucket: f.rec, Key: lockNoncurrent, Exists: true, Mtime: smallMtime(), WriteTag: "tx-put",
				Attrs: map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrIDTag: []byte("tx-put\x00")},
			}
			stub.StatObjectReturns(st, nil)
			f.env.Objects = stub
			Expect(putRetention(ctx, lockNoncurrent, retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false)).To(Succeed())
			Expect(stub.SetObjectAttrsCallCount()).To(Equal(1))
			_, gotSt, set, rm := stub.SetObjectAttrsArgsForCall(0)
			Expect(gotSt).To(BeIdenticalTo(st), "the driver writes st.Mtime plus 1 ns under a fresh tag, guarded on st's, driver/attrs.go")
			Expect(set).To(Equal(map[string][]byte{
				meta.AttrACL: f.aliceACL, meta.AttrIDTag: []byte("tx-put\x00"),
				meta.AttrObjectRetention: retentionBytes("GOVERNANCE", day(1)),
			}))
			Expect(rm).To(BeNil())
			_, _, key := stub.StatObjectArgsForCall(0)
			Expect(key).To(Equal(lockNoncurrent))
		})
		DescribeTable("writes nothing to a state that does not exist, answering NoSuchKey",
			func(ctx SpecContext, run func(context.Context) error) {
				stub := &opfakes.FakeObjectStore{}
				stub.StatObjectReturns(&op.ObjectState{Bucket: f.rec, Key: meta.ObjKey{Name: "k", Instance: "dm"}, DeleteMarker: true}, nil)
				f.env.Objects = stub
				Expect(run(ctx)).To(MatchError(op.ErrNoSuchKey))
				Expect(stub.SetObjectAttrsCallCount()).To(BeZero())
			},
			Entry("PutObjectRetention", func(ctx context.Context) error {
				return putRetention(ctx, lockCurrent, retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false)
			}),
			Entry("PutObjectLegalHold", func(ctx context.Context) error { return putLegalHold(ctx, lockCurrent, legalHoldDoc("ON")) }),
		)
		It("passes a lost race on as 409 ConcurrentModification, radosgw's -ECANCELED", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			stub.StatObjectReturns(&op.ObjectState{Bucket: f.rec, Key: lockCurrent, Exists: true}, nil)
			stub.SetObjectAttrsReturns(op.ErrConcurrentModification)
			f.env.Objects = stub
			err := putRetention(ctx, lockCurrent, retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false)
			Expect(err).To(MatchError(op.ErrConcurrentModification))
			Expect(op.AsError(err).Status).To(Equal(http.StatusConflict))
		})

		Describe("in radosgw's order", func() {
			var (
				seen   []string
				authzr *opfakes.FakeAuthorizer
				refuse map[policy.Action]bool
			)
			BeforeEach(func() {
				seen, refuse = nil, map[policy.Action]bool{}
				authzr = &opfakes.FakeAuthorizer{}
				authzr.VerifyObjectStub = func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
					seen = append(seen, "verify "+a.String())
					if refuse[a] {
						return op.ErrAccessDenied
					}
					return nil
				}
				f.env.Authz = authzr
			})
			request := func(key meta.ObjKey, doc string) *op.Request {
				r := req(http.MethodPut, key, doc)
				r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
				return r
			}
			const (
				put    = "verify s3:PutObjectRetention"
				bypass = "verify s3:BypassGovernanceRetention"
			)

			refused := func(context.Context, *op.Request) { refuse[policy.S3PutObjectRetention] = true }
			noWrite := func(_ context.Context, r *op.Request) { r.Identity.OpMask = op.OpTypeAll &^ op.OpTypeWrite }
			overLimit := func(context.Context, *op.Request) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(governanceTo1Doc) - 1)
			}
			noLength := func(_ context.Context, r *op.Request) {
				r.ContentLength = 0
				r.Header.Del("Content-Length")
			}
			body := func(doc string) func(context.Context, *op.Request) {
				return func(_ context.Context, r *op.Request) {
					r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
					r.ContentLength = int64(len(doc))
					r.Header.Set("Content-Length", strconv.Itoa(len(doc)))
				}
			}
			noLock := func(ctx context.Context, r *op.Request) {
				setFlags(ctx, meta.BucketVersioned)
				body("<Retention>")(ctx, r)
			}
			missing := func(_ context.Context, r *op.Request) { r.Object = meta.ObjKey{Name: "k", Instance: "nope"} }
			undecodable := func(ctx context.Context, _ *op.Request) {
				setAttr(ctx, lockCurrent, meta.AttrObjectRetention, []byte{2, 1, 3, 0, 0, 0})
			}
			bytesPast := func(ctx context.Context, _ *op.Request) {
				setAttr(ctx, lockCurrent, meta.AttrObjectRetention, append(retentionBytes("GOVERNANCE", day(9)), 0))
			}

			DescribeTable("stops at the first step that fails",
				func(ctx SpecContext, header bool, fail func(context.Context, *op.Request), want error, message string, steps []string) {
					r := request(lockCurrent, governanceTo1Doc)
					fail(ctx, r)
					before := stat(ctx, lockCurrent).Epoch
					err := op.Run(ctx, &op.PutObjectRetention{BypassHeader: header}, r)
					expectError(err, want, message)
					Expect(seen).To(Equal(steps))
					Expect(stat(ctx, lockCurrent).Epoch).To(Equal(before), "nothing written")
				},
				Entry("a refused requester, whose body is unread and whose bypass is not evaluated, rgw_op.cc:8340-8342 at v19.2.7",
					true, refused, op.ErrAccessDenied, "", []string{put}),
				Entry("an op mask without write, which verify_op_mask refuses before verify_permission reads the body, rgw_process.cc:207-211 at v19.2.7",
					true, noWrite, op.ErrAccessDenied, "", []string{put}),
				Entry("a Content-Length over rgw_max_put_param_size, InvalidRange unread, before the bypass, rgw_op.cc:8343-8346 at v19.2.7",
					true, overLimit, op.ErrInvalidRange, "", []string{put}),
				Entry("no Content-Length, 411", true, noLength, op.ErrMissingContentLength, "", []string{put}),
				Entry("a bucket without object lock, after the body is read and before it is parsed, rgw_op.cc:8360-8365 at v19.2.7",
					false, noLock, op.ErrInvalidRequest, msgRetentionNoLock, []string{put, "read"}),
				Entry("a bucket without object lock, after the bypass is evaluated",
					true, noLock, op.ErrInvalidRequest, msgRetentionNoLock, []string{put, "read", bypass}),
				Entry("a document that does not parse, rgw_op.cc:8374-8385 at v19.2.7",
					false, body("<Retention>"), op.ErrMalformedXML, "", []string{put, "read"}),
				Entry("a Mode outside GOVERNANCE and COMPLIANCE", false, body(retentionDoc("governance", "2030-01-01T00:00:00Z")),
					op.ErrMalformedXML, "", []string{put, "read"}),
				Entry("a retain-until date past real_time's range, which the parser refuses where radosgw wraps it",
					false, body(retentionDoc("GOVERNANCE", "2554-07-21T23:34:34Z")), op.ErrMalformedXML, "", []string{put, "read"}),
				Entry("a retain-until date before the clock, rgw_op.cc:8387-8392 at v19.2.7",
					false, body(retentionDoc("GOVERNANCE", "2026-10-20T23:59:59Z")), op.ErrInvalidArgument, msgRetainUntilPast, []string{put, "read"}),
				Entry("a version that does not exist, which the authorizer passes, rgw_op.cc:8397-8401 at v19.2.7",
					false, missing, op.ErrNoSuchKey, "", []string{put, "read"}),
				Entry("an existing retention too short to decode, -EIO, rgw_op.cc:8405-8412 at v19.2.7",
					false, undecodable, op.ErrUnknown, "", []string{put, "read"}),
				Entry("an existing retention with bytes past its encoding, which decode(T&, const bufferlist&) aborts on",
					false, bytesPast, op.ErrUnknown, "", []string{put, "read"}),
			)
			It("answers a request that passes every check with nil after one check of each, the bypass last", func(ctx SpecContext) {
				Expect(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, request(lockCurrent, governanceTo1Doc))).To(Succeed())
				Expect(seen).To(Equal([]string{put, "read", bypass}))
			})
			It("does not evaluate s3:BypassGovernanceRetention without the header, rgw_op.cc:8347-8349 at v19.2.7", func(ctx SpecContext) {
				Expect(op.Run(ctx, &op.PutObjectRetention{}, request(lockCurrent, governanceTo1Doc))).To(Succeed())
				Expect(authzr.VerifyObjectCallCount()).To(Equal(1))
				_, _, a, _ := authzr.VerifyObjectArgsForCall(0)
				Expect(a).To(Equal(policy.S3PutObjectRetention))
			})
			It("evaluates s3:BypassGovernanceRetention on the version the request names, with WRITE as its ACL permission", func(ctx SpecContext) {
				Expect(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, request(lockNoncurrent, governanceTo1Doc))).To(Succeed())
				Expect(authzr.VerifyObjectCallCount()).To(Equal(2))
				for i, want := range []policy.Action{policy.S3PutObjectRetention, policy.S3BypassGovernanceRetention} {
					_, r, a, perm := authzr.VerifyObjectArgsForCall(i)
					Expect(a).To(Equal(want))
					Expect(perm).To(Equal(acl.PermWrite), "rgw_iam_policy.h op_to_perm")
					Expect(r.ObjState.Key).To(Equal(lockNoncurrent))
				}
			})
			DescribeTable("takes the bypass check's error as a refusal of the bypass only when it is an access denial",
				func(ctx SpecContext, bypassErr, want error) {
					setAttr(ctx, lockCurrent, meta.AttrObjectRetention, retentionBytes("GOVERNANCE", day(3)))
					authzr.VerifyObjectStub = func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
						if a == policy.S3BypassGovernanceRetention {
							return bypassErr
						}
						return nil
					}
					err := op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, request(lockCurrent, governanceTo1Doc))
					Expect(err).To(MatchError(want))
					if errors.Is(want, op.ErrAccessDenied) {
						Expect(op.AsError(err).Message).To(Equal(msgShortens), "the change's refusal, not the bypass check's")
					}
				},
				Entry("an access denial, which leaves the bypass off", op.ErrAccessDenied, op.ErrAccessDenied),
				Entry("an evaluator fault, which is the request's error", fmt.Errorf("%w: tags unreadable", op.ErrInternalError), op.ErrInternalError),
			)
			It("lets an admin a refusal of s3:PutObjectRetention lets through reach execute with no document, MalformedXML, as radosgw's override skips get_params",
				func(ctx SpecContext) {
					refuse[policy.S3PutObjectRetention] = true
					r := request(lockCurrent, governanceTo1Doc)
					r.Identity.Admin = true
					r.Body = failingReader{}
					Expect(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, r)).To(MatchError(op.ErrMalformedXML))
					Expect(seen).To(Equal([]string{put}), "neither the body nor the bypass")
				})
		})

		Describe("the change from an existing retention", func() {
			var refuseBypass bool
			BeforeEach(func() {
				refuseBypass = false
				authzr := &opfakes.FakeAuthorizer{}
				authzr.VerifyObjectStub = func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
					if a == policy.S3BypassGovernanceRetention && refuseBypass {
						return op.ErrAccessDenied
					}
					return nil
				}
				f.env.Authz = authzr
			})
			// change puts proposed over old as stored, with the header and the
			// permission as given, and reports the retention then stored.
			change := func(ctx context.Context, old meta.ObjectRetention, proposed string, header, perm bool) (meta.ObjectRetention, error) {
				setAttr(ctx, lockCurrent, meta.AttrObjectRetention, retentionBytes(old.Mode, old.RetainUntil))
				refuseBypass = !perm
				err := putRetention(ctx, lockCurrent, proposed, header)
				got, gerr := getRetention(ctx, lockCurrent)
				Expect(gerr).NotTo(HaveOccurred())
				return got, err
			}

			DescribeTable("takes the bypass from the header and the permission both",
				func(ctx SpecContext, old meta.ObjectRetention, proposed string, header, perm bool, message string) {
					got, err := change(ctx, old, proposed, header, perm)
					if message == "" {
						Expect(err).NotTo(HaveOccurred())
						Expect(got).NotTo(Equal(old))
						return
					}
					expectError(err, op.ErrAccessDenied, message)
					Expect(got).To(Equal(old), "nothing written")
				},
				Entry("shortening GOVERNANCE with the header and the permission", governanceTo3,
					retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), true, true, ""),
				Entry("shortening GOVERNANCE with the header alone, rgw_op.cc:8413-8418 at v19.2.7", governanceTo3,
					retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), true, false, msgShortens),
				Entry("shortening GOVERNANCE with the permission alone", governanceTo3,
					retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false, true, msgShortens),
				Entry("shortening GOVERNANCE with neither", governanceTo3,
					retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false, false, msgShortens),
				Entry("GOVERNANCE to COMPLIANCE with the header and the permission", governanceTo3,
					retentionDoc("COMPLIANCE", "2030-01-03T00:00:00Z"), true, true, ""),
				Entry("GOVERNANCE to COMPLIANCE with the header alone, rgw_op.cc:8425-8429 at v19.2.7", governanceTo3,
					retentionDoc("COMPLIANCE", "2030-01-03T00:00:00Z"), true, false, msgFromGovernance),
				Entry("GOVERNANCE to COMPLIANCE with the permission alone", governanceTo3,
					retentionDoc("COMPLIANCE", "2030-01-03T00:00:00Z"), false, true, msgFromGovernance),
				Entry("GOVERNANCE to COMPLIANCE with neither", governanceTo3,
					retentionDoc("COMPLIANCE", "2030-01-03T00:00:00Z"), false, false, msgFromGovernance),
				Entry("COMPLIANCE to GOVERNANCE, which no bypass passes, rgw_op.cc:8421-8424 at v19.2.7", complianceTo3,
					retentionDoc("GOVERNANCE", "2030-01-04T00:00:00Z"), true, true, msgToGovernance),
				Entry("shortening COMPLIANCE, which no bypass passes", complianceTo3,
					retentionDoc("COMPLIANCE", "2030-01-01T00:00:00Z"), true, true, msgShortens),
				Entry("extending GOVERNANCE, which needs no bypass", governanceTo3,
					retentionDoc("GOVERNANCE", "2030-01-05T00:00:00Z"), false, false, ""),
				Entry("extending COMPLIANCE, which needs no bypass", complianceTo3,
					retentionDoc("COMPLIANCE", "2030-01-05T00:00:00Z"), false, false, ""),
				Entry("shortening GOVERNANCE by less than a second, which compares whole seconds", governanceTo3,
					retentionDoc("GOVERNANCE", "2030-01-02T23:59:59.999999999Z"), false, false, msgShortens),
			)
			It("keeps a same-second date's nanoseconds as the document gives them, with no bypass", func(ctx SpecContext) {
				got, err := change(ctx, governanceTo3, retentionDoc("GOVERNANCE", "2030-01-03T00:00:00.5Z"), false, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.RetainUntil).To(Equal(day(3).Add(500 * time.Millisecond)))
			})
			DescribeTable("binds no change to a lapsed retention, whose second is not after the clock's",
				func(ctx SpecContext, old meta.ObjectRetention, proposed string) {
					got, err := change(ctx, old, proposed, false, false)
					Expect(err).NotTo(HaveOccurred(), "docs/exclusions.md, \"A lapsed retention binds no retention change\"")
					Expect(got.Mode).To(Equal("GOVERNANCE"))
				},
				Entry("COMPLIANCE, lapsed a second ago, to GOVERNANCE",
					meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: lockNow.Add(-time.Second)}, retentionDoc("GOVERNANCE", "2027-01-01T00:00:00Z")),
				Entry("COMPLIANCE ending at the clock's second",
					meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: lockNow}, retentionDoc("GOVERNANCE", "2027-01-01T00:00:00Z")),
				Entry("GOVERNANCE that ran past a shorter date, without the bypass",
					meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: lockNow.Add(-time.Hour)}, retentionDoc("GOVERNANCE", "2026-10-21T00:00:00Z")),
			)
			It("holds a retention whose second is after the clock's", func(ctx SpecContext) {
				_, err := change(ctx, meta.ObjectRetention{Mode: "COMPLIANCE", RetainUntil: lockNow.Add(time.Second)},
					retentionDoc("GOVERNANCE", "2027-01-01T00:00:00Z"), true, true)
				expectError(err, op.ErrAccessDenied, msgToGovernance)
			})
		})

		DescribeTable("compares the retain-until second with the clock exactly",
			func(ctx SpecContext, clock time.Time, until string, refused bool) {
				f.env.Now = func() time.Time { return clock }
				err := putRetention(ctx, lockCurrent, retentionDoc("GOVERNANCE", until), false)
				if refused {
					expectError(err, op.ErrInvalidArgument, msgRetainUntilPast)
					return
				}
				Expect(err).NotTo(HaveOccurred())
			},
			Entry("the clock's own second, on the second", lockNow, "2026-10-21T00:00:00Z", false),
			Entry("the clock's own second, 50 ns past it, which radosgw's double reads as on it", lockNow.Add(50), "2026-10-21T00:00:00Z", true),
			Entry("the second after the clock's, 50 ns before it", lockNow.Add(time.Second-50), "2026-10-21T00:00:01Z", false),
			Entry("a date later than the clock within the clock's second, whose second is before it",
				lockNow.Add(500*time.Millisecond), "2026-10-21T00:00:00.9Z", true),
		)

		It("answers a current delete marker 404 NoSuchKey, writing nothing", func(ctx SpecContext) {
			f.env.Objects = markerHeads{ObjectStore: f.store, name: "k"}
			before := stat(ctx, lockCurrent).Epoch
			Expect(putRetention(ctx, lockCurrent, retentionDoc("GOVERNANCE", "2030-01-01T00:00:00Z"), false)).To(MatchError(op.ErrNoSuchKey))
			Expect(stat(ctx, lockCurrent).Epoch).To(Equal(before))
		})
	})

	Describe("GetObjectRetention", func() {
		It("answers a bucket without object lock 400 InvalidRequest with radosgw's message, rgw_op.cc:8456-8461 at v19.2.7", func(ctx SpecContext) {
			setFlags(ctx, meta.BucketVersioned)
			setAttr(ctx, lockCurrent, meta.AttrObjectRetention, retentionBytes("GOVERNANCE", day(1)))
			_, err := getRetention(ctx, lockCurrent)
			expectError(err, op.ErrInvalidRequest, msgLockNotSet)
		})
		It("answers a version without a retention 404 ObjectLockConfigurationNotFoundError with no message, rgw_op.cc:8469-8473 at v19.2.7", func(ctx SpecContext) {
			_, err := getRetention(ctx, lockCurrent)
			expectError(err, op.ErrNoSuchObjectLockConfig, "")
			Expect(op.AsError(err).Status).To(Equal(http.StatusNotFound))
		})
		DescribeTable("reads a retention attr through an iterator, rgw_op.cc:8475-8482 at v19.2.7",
			func(ctx SpecContext, attr []byte, want error) {
				setAttr(ctx, lockCurrent, meta.AttrObjectRetention, attr)
				got, err := getRetention(ctx, lockCurrent)
				if want != nil {
					Expect(err).To(MatchError(want))
					Expect(op.AsError(err).Status).To(Equal(http.StatusInternalServerError))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: day(1)}))
			},
			Entry("too short to decode: -EIO, 500 UnknownError", []byte{2, 1, 3, 0, 0, 0}, op.ErrUnknown),
			Entry("bytes past its encoding, left unread", append(retentionBytes("GOVERNANCE", day(1)), 0xff), nil),
		)
		DescribeTable("answers a version that does not exist 404 NoSuchKey",
			func(ctx SpecContext, key meta.ObjKey, marker bool) {
				if marker {
					f.env.Objects = markerHeads{ObjectStore: f.store, name: "k"}
				}
				_, err := getRetention(ctx, key)
				Expect(err).To(MatchError(op.ErrNoSuchKey))
			},
			Entry("a missing version", meta.ObjKey{Name: "k", Instance: "nope"}, false),
			Entry("a current delete marker", lockCurrent, true),
		)
		It("writes nothing", func(ctx SpecContext) {
			setAttr(ctx, lockCurrent, meta.AttrObjectRetention, retentionBytes("GOVERNANCE", day(1)))
			before := stat(ctx, lockCurrent).Epoch
			_, err := getRetention(ctx, lockCurrent)
			Expect(err).NotTo(HaveOccurred())
			Expect(stat(ctx, lockCurrent).Epoch).To(Equal(before))
		})
	})

	Describe("PutObjectLegalHold", func() {
		It("stores ON and then OFF as RGWObjectLegalHold, which GetObjectLegalHold reads back", func(ctx SpecContext) {
			Expect(putLegalHold(ctx, lockCurrent, legalHoldDoc("ON"))).To(Succeed())
			Expect(stat(ctx, lockCurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, legalHoldBytes("ON")))
			got, err := getLegalHold(ctx, lockCurrent)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(meta.ObjectLegalHold{Status: "ON"}))
			Expect(putLegalHold(ctx, lockCurrent, legalHoldDoc("OFF"))).To(Succeed())
			Expect(stat(ctx, lockCurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, legalHoldBytes("OFF")))
		})
		It("writes a noncurrent version named by versionId, leaving the current alone", func(ctx SpecContext) {
			Expect(putLegalHold(ctx, lockNoncurrent, legalHoldDoc("ON"))).To(Succeed())
			Expect(stat(ctx, lockNoncurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, legalHoldBytes("ON")))
			Expect(stat(ctx, lockCurrent).Attrs).NotTo(HaveKey(meta.AttrObjectLegalHold))
			got, err := getLegalHold(ctx, lockNoncurrent)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Status).To(Equal("ON"))
		})
		It("hands SetObjectAttrs the state Init read, with every attr it carried and the legal hold", func(ctx SpecContext) {
			stub := &opfakes.FakeObjectStore{}
			st := &op.ObjectState{
				Bucket: f.rec, Key: lockCurrent, Exists: true, Mtime: smallMtime(), WriteTag: "tx-put",
				Attrs: map[string][]byte{meta.AttrACL: f.aliceACL},
			}
			stub.StatObjectReturns(st, nil)
			f.env.Objects = stub
			Expect(putLegalHold(ctx, lockCurrent, legalHoldDoc("ON"))).To(Succeed())
			_, gotSt, set, rm := stub.SetObjectAttrsArgsForCall(0)
			Expect(gotSt).To(BeIdenticalTo(st))
			Expect(set).To(Equal(map[string][]byte{meta.AttrACL: f.aliceACL, meta.AttrObjectLegalHold: legalHoldBytes("ON")}))
			Expect(rm).To(BeNil())
		})

		Describe("in radosgw's order", func() {
			var (
				seen   []string
				refuse bool
			)
			BeforeEach(func() {
				seen, refuse = nil, false
				authzr := &opfakes.FakeAuthorizer{}
				authzr.VerifyObjectStub = func(_ context.Context, _ *op.Request, a policy.Action, _ acl.Permission) error {
					seen = append(seen, "verify "+a.String())
					if refuse {
						return op.ErrAccessDenied
					}
					return nil
				}
				f.env.Authz = authzr
			})
			const verify = "verify s3:PutObjectLegalHold"
			body := func(doc string) func(context.Context, *op.Request) {
				return func(_ context.Context, r *op.Request) {
					r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
					r.ContentLength = int64(len(doc))
					r.Header.Set("Content-Length", strconv.Itoa(len(doc)))
				}
			}

			DescribeTable("stops at the first step that fails",
				func(ctx SpecContext, fail func(context.Context, *op.Request), want error, message string, steps []string) {
					r := req(http.MethodPut, lockCurrent, legalHoldOnDoc)
					body(legalHoldOnDoc)(ctx, r)
					fail(ctx, r)
					before := stat(ctx, lockCurrent).Epoch
					expectError(op.Run(ctx, &op.PutObjectLegalHold{}, r), want, message)
					Expect(seen).To(Equal(steps))
					Expect(stat(ctx, lockCurrent).Epoch).To(Equal(before), "nothing written")
				},
				Entry("a refused requester", func(context.Context, *op.Request) { refuse = true }, op.ErrAccessDenied, "", []string{verify}),
				Entry("an op mask without write", func(_ context.Context, r *op.Request) { r.Identity.OpMask = op.OpTypeRead },
					op.ErrAccessDenied, "", []string{verify}),
				Entry("a bucket without object lock, before the body is read, rgw_op.cc:8504-8520 at v19.2.7",
					func(ctx context.Context, _ *op.Request) { setFlags(ctx, meta.BucketVersioned) },
					op.ErrInvalidRequest, msgLegalHoldNoLock, []string{verify}),
				Entry("a Content-Length over rgw_max_put_param_size, InvalidRange unread",
					func(context.Context, *op.Request) {
						f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(legalHoldOnDoc) - 1)
					},
					op.ErrInvalidRange, "", []string{verify}),
				Entry("a document that does not parse", body("<LegalHold>"), op.ErrMalformedXML, "", []string{verify, "read"}),
				Entry("a Status outside ON and OFF", body(legalHoldDoc("on")), op.ErrMalformedXML, "", []string{verify, "read"}),
				Entry("a version that does not exist, which both releases' attr reads answer before the write",
					func(_ context.Context, r *op.Request) { r.Object = meta.ObjKey{Name: "k", Instance: "nope"} },
					op.ErrNoSuchKey, "", []string{verify, "read"}),
			)
			It("reads an admin's body in execute once its override lets a refusal through, and writes it", func(ctx SpecContext) {
				refuse = true
				r := req(http.MethodPut, lockCurrent, legalHoldOnDoc)
				r.Identity.Admin = true
				Expect(op.Run(ctx, &op.PutObjectLegalHold{}, r)).To(Succeed())
				Expect(stat(ctx, lockCurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectLegalHold, legalHoldBytes("ON")))
			})
		})

		It("answers a current delete marker 404 NoSuchKey", func(ctx SpecContext) {
			f.env.Objects = markerHeads{ObjectStore: f.store, name: "k"}
			Expect(putLegalHold(ctx, lockCurrent, legalHoldDoc("ON"))).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("GetObjectLegalHold", func() {
		It("answers a bucket without object lock 400 InvalidRequest with radosgw's message, rgw_op.cc:8560-8565 at v19.2.7", func(ctx SpecContext) {
			setFlags(ctx, 0)
			_, err := getLegalHold(ctx, lockCurrent)
			expectError(err, op.ErrInvalidRequest, msgLockNotSet)
		})
		It("answers a version without a legal hold 404 ObjectLockConfigurationNotFoundError, rgw_op.cc:8573-8577 at v19.2.7", func(ctx SpecContext) {
			setAttr(ctx, lockCurrent, meta.AttrObjectRetention, retentionBytes("GOVERNANCE", day(1)))
			_, err := getLegalHold(ctx, lockCurrent)
			expectError(err, op.ErrNoSuchObjectLockConfig, "")
		})
		DescribeTable("reads a legal-hold attr through an iterator, rgw_op.cc:8579-8586 at v19.2.7",
			func(ctx SpecContext, attr []byte, want error) {
				setAttr(ctx, lockCurrent, meta.AttrObjectLegalHold, attr)
				got, err := getLegalHold(ctx, lockCurrent)
				if want != nil {
					Expect(err).To(MatchError(want))
					return
				}
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(meta.ObjectLegalHold{Status: "ON"}))
			},
			Entry("too short to decode: -EIO, 500 UnknownError", []byte{1, 1, 6, 0, 0, 0}, op.ErrUnknown),
			Entry("bytes past its encoding, left unread", append(legalHoldBytes("ON"), 0xff), nil),
		)
		It("answers a current delete marker 404 NoSuchKey", func(ctx SpecContext) {
			f.env.Objects = markerHeads{ObjectStore: f.store, name: "k"}
			_, err := getLegalHold(ctx, lockCurrent)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
	})

	Describe("through the policy evaluator", func() {
		BeforeEach(func() { f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid)) })
		statement := func(effect, principal, action, resource, cond string) string {
			c := ""
			if cond != "" {
				c = `,"Condition":` + cond
			}
			return fmt.Sprintf(`{"Effect":%q,"Principal":{"AWS":[%q]},"Action":%q,"Resource":%q%s}`, effect, principal, action, resource, c)
		}
		setPolicy := func(ctx context.Context, statements ...string) {
			GinkgoHelper()
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[` +
				strings.Join(statements, ",") + `]}`)})
		}
		const (
			bob   = "arn:aws:iam:::user/bob"
			alice = "arn:aws:iam:::user/alice"
		)
		asBob := func(r *op.Request) *op.Request {
			r.Identity = f.bob
			return r
		}
		// k holds a GOVERNANCE retention to 2030-01-03 and the tag team=a.
		BeforeEach(func(ctx SpecContext) {
			setAttr(ctx, lockCurrent, meta.AttrObjectRetention, retentionBytes("GOVERNANCE", day(3)))
			var set tags.Set
			Expect(set.Add("team", "a", tags.MaxObjectTags)).To(Succeed())
			setAttr(ctx, lockCurrent, tags.Attr, encodeTags(set))
		})
		stored := func(ctx context.Context) meta.ObjectRetention {
			GinkgoHelper()
			got, err := getRetention(ctx, lockCurrent)
			Expect(err).NotTo(HaveOccurred())
			return got
		}

		It("lets bob shorten a GOVERNANCE retention with the header and both actions on the object", func(ctx SpecContext) {
			setPolicy(ctx, statement("Allow", bob, "s3:PutObjectRetention", "arn:aws:s3:::plain/k", ""),
				statement("Allow", bob, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/*", ""))
			Expect(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, asBob(req(http.MethodPut, lockCurrent, governanceTo1Doc)))).To(Succeed())
			Expect(stored(ctx).RetainUntil).To(Equal(day(1)))
		})
		DescribeTable("refuses the bypass that the policies do not grant on the object",
			func(ctx SpecContext, asAlice bool, statements ...string) {
				setPolicy(ctx, statements...)
				r := req(http.MethodPut, lockCurrent, governanceTo1Doc)
				if !asAlice {
					r = asBob(r)
				}
				expectError(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, r), op.ErrAccessDenied, msgShortens)
				Expect(stored(ctx).RetainUntil).To(Equal(day(3)))
			},
			Entry("bob, granted s3:PutObjectRetention alone", false,
				statement("Allow", bob, "s3:PutObjectRetention", "arn:aws:s3:::plain/*", "")),
			Entry("bob, granted the bypass on the bucket's ARN, which is not the object's", false,
				statement("Allow", bob, "s3:PutObjectRetention", "arn:aws:s3:::plain/*", ""),
				statement("Allow", bob, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain", "")),
			Entry("alice, the owner, under a Deny of the bypass on the object", true,
				statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/k", "")),
			Entry("alice under a Deny of the bypass conditioned on a tag the object carries", true,
				statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/*",
					`{"StringEquals":{"s3:ExistingObjectTag/team":"a"}}`)),
		)
		It("lets alice bypass under a Deny conditioned on a tag the object does not carry", func(ctx SpecContext) {
			setPolicy(ctx, statement("Deny", alice, "s3:BypassGovernanceRetention", "arn:aws:s3:::plain/*",
				`{"StringEquals":{"s3:ExistingObjectTag/team":"b"}}`))
			Expect(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, req(http.MethodPut, lockCurrent, governanceTo1Doc))).To(Succeed())
		})
		It("authorizes the reads as s3:GetObjectRetention and s3:GetObjectLegalHold alone", func(ctx SpecContext) {
			setAttr(ctx, lockCurrent, meta.AttrObjectLegalHold, legalHoldBytes("ON"))
			setPolicy(ctx, statement("Allow", bob, "s3:GetObjectRetention", "arn:aws:s3:::plain/*", ""))
			Expect(op.Run(ctx, &op.GetObjectRetention{}, asBob(req(http.MethodGet, lockCurrent, "")))).To(Succeed())
			Expect(op.Run(ctx, &op.GetObjectLegalHold{}, asBob(req(http.MethodGet, lockCurrent, "")))).To(MatchError(op.ErrAccessDenied))
			setPolicy(ctx, statement("Allow", bob, "s3:GetObjectLegalHold", "arn:aws:s3:::plain/*", ""))
			Expect(op.Run(ctx, &op.GetObjectLegalHold{}, asBob(req(http.MethodGet, lockCurrent, "")))).To(Succeed())
			Expect(op.Run(ctx, &op.GetObjectRetention{}, asBob(req(http.MethodGet, lockCurrent, "")))).To(MatchError(op.ErrAccessDenied))
		})
		It("authorizes PutObjectLegalHold as s3:PutObjectLegalHold alone", func(ctx SpecContext) {
			setPolicy(ctx, statement("Allow", bob, "s3:PutObjectRetention", "arn:aws:s3:::plain/*", ""))
			Expect(op.Run(ctx, &op.PutObjectLegalHold{}, asBob(req(http.MethodPut, lockCurrent, legalHoldDoc("ON"))))).To(MatchError(op.ErrAccessDenied))
			setPolicy(ctx, statement("Allow", bob, "s3:PutObjectLegalHold", "arn:aws:s3:::plain/*", ""))
			Expect(op.Run(ctx, &op.PutObjectLegalHold{}, asBob(req(http.MethodPut, lockCurrent, legalHoldDoc("ON"))))).To(Succeed())
		})
		DescribeTable("refuses a missing version before the permission check and the body, as read_obj_policy does",
			func(ctx SpecContext, asBobToo bool, want error) {
				r := req(http.MethodPut, meta.ObjKey{Name: "k", Instance: "nope"}, governanceTo1Doc)
				r.Body = failingReader{}
				if asBobToo {
					r = asBob(r)
				}
				err := op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, r)
				Expect(err).To(MatchError(want))
				Expect(op.IsBeforeVerify(err)).To(BeTrue())
			},
			Entry("NoSuchKey to alice, who may list", false, op.ErrNoSuchKey),
			Entry("AccessDenied to bob, who may not", true, op.ErrAccessDenied),
		)
		It("refuses bob a retention he may not put, reading no body", func(ctx SpecContext) {
			r := asBob(req(http.MethodPut, lockCurrent, governanceTo1Doc))
			r.Body = failingReader{}
			Expect(op.Run(ctx, &op.PutObjectRetention{BypassHeader: true}, r)).To(MatchError(op.ErrAccessDenied))
			Expect(stat(ctx, lockCurrent).Attrs).To(HaveKeyWithValue(meta.AttrObjectRetention, retentionBytes("GOVERNANCE", day(3))))
		})
	})
})
