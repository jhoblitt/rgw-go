package op_test

import (
	"context"
	"crypto/md5" //nolint:gosec // a Content-MD5 is an MD5
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// lcBody is a PutBucketLifecycleConfiguration body holding rules.
func lcBody(rules ...string) string {
	return `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` + strings.Join(rules, "") + `</LifecycleConfiguration>`
}

// lcExpireRule is a rule ID that expires objects under prefix after days,
// its members in the order botocore 1.43.90 writes them.
func lcExpireRule(id, prefix string, days int) string {
	return `<Rule><Expiration><Days>` + strconv.Itoa(days) + `</Days></Expiration><ID>` + id + `</ID><Prefix>` + prefix +
		`</Prefix><Status>Enabled</Status></Rule>`
}

// lcExpireDoc is the body boto3 writes for the first rule of s3-tests'
// test_lifecycle_set alone.
var lcExpireDoc = lcBody(lcExpireRule("rule1", "test1/", 1))

// lcExpireConfig is lcExpireDoc's configuration.
func lcExpireConfig() lifecycle.Configuration {
	return lifecycle.Configuration{Rules: []lifecycle.Rule{{
		ID: "rule1", Prefix: "test1/", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "1"},
	}}}
}

// lcAttr is c as radosgw stores it under user.rgw.lc.
func lcAttr(c lifecycle.Configuration) []byte {
	e := denc.NewEncoder()
	c.Encode(e, denc.Squid)
	return e.Bytes()
}

// contentMD5 is the Content-MD5 of body.
func contentMD5(body string) string {
	sum := md5.Sum([]byte(body)) //nolint:gosec // a Content-MD5 is an MD5
	return base64.StdEncoding.EncodeToString(sum[:])
}

// lcUpdatedDoc is another one-rule configuration, a client's update of
// lcExpireDoc.
var lcUpdatedDoc = lcBody(lcExpireRule("rule2", "logs/", 7))

// lcUpdatedConfig is lcUpdatedDoc's configuration.
func lcUpdatedConfig() lifecycle.Configuration {
	return lifecycle.Configuration{Rules: []lifecycle.Rule{{
		ID: "rule2", Prefix: "logs/", Status: "Enabled", Expiration: lifecycle.Expiration{Days: "7"},
	}}}
}

// errLockBusy is the error guard_lc_modify returns once its 500 retries find
// the shard still locked by another holder.
var errLockBusy = fmt.Errorf("locking lifecycle shard lc.17: %w", &radosclient.Error{Errno: -int32(syscall.EBUSY), Op: "lock"})

var _ = Describe("bucket lifecycle ops", func() {
	var (
		f  *writeFixture
		lc *opfakes.FakeLifecycleStore
	)
	// useFixture runs the specs on a fresh fixture at rel, with a lifecycle
	// store that succeeds.
	useFixture := func(ctx context.Context, rel denc.Release) {
		f = newWriteFixture(ctx, rel)
		lc = &opfakes.FakeLifecycleStore{}
		f.env.Lifecycle = lc
	}
	BeforeEach(func(ctx SpecContext) { useFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	putReq := func(body string) *op.Request { return f.bodyReq(http.MethodPut, "", body) }
	delReq := func() *op.Request { return f.req(http.MethodDelete, "plain", "") }
	put := func(ctx context.Context, r *op.Request) error { return op.Run(ctx, &op.PutBucketLifecycle{}, r) }
	setIndexless := func(ctx context.Context) {
		GinkgoHelper()
		rec := bucket(ctx)
		rec.Info.Layout.Current.Layout.Type = meta.IndexIndexless
		Expect(f.store.PutBucketInfo(ctx, rec)).To(Succeed())
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("PutBucketLifecycle, rgw_op.h:1652-1682 at v19.2.7", &op.PutBucketLifecycle{}, "put_lifecycle",
			policy.S3PutLifecycleConfiguration, op.OpTypeWrite),
		Entry("DeleteBucketLifecycle, a write authorized as s3:PutLifecycleConfiguration, rgw_op.h:1684-1695 and rgw_op.cc:5790-5802 at v19.2.7",
			&op.DeleteBucketLifecycle{}, "delete_lifecycle", policy.S3PutLifecycleConfiguration, op.OpTypeWrite),
	)

	Describe("error order", func() {
		var (
			seen    []string
			buckets *opfakes.FakeBucketStore
			authzr  *opfakes.FakeAuthorizer
		)
		// Each fake records its step in seen and passes the call to the
		// memstore, or allows it; the body records "read" when it is first
		// read, and the lifecycle store "entry".
		BeforeEach(func() {
			seen = nil
			buckets = &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
				seen = append(seen, "load")
				return f.store.GetBucket(ctx, tenant, name)
			}
			buckets.PutBucketAttrsStub = func(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
				seen = append(seen, "write")
				return f.store.PutBucketAttrs(ctx, rec, set, rm)
			}
			authzr = &opfakes.FakeAuthorizer{}
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return nil
			}
			entry := func(context.Context, *op.BucketRecord) error {
				seen = append(seen, "entry")
				return nil
			}
			lc.SetEntryStub, lc.RemoveEntryStub = entry, entry
			f.env.Buckets, f.env.Authz = buckets, authzr
		})
		request := func(doc string) *op.Request {
			r := putReq(doc)
			r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
			return r
		}

		putOp := func() op.Op { return &op.PutBucketLifecycle{} }
		delOp := func() op.Op { return &op.DeleteBucketLifecycle{} }

		none := func(context.Context, *op.Request) {}
		missing := func(_ context.Context, r *op.Request) { r.Bucket = "missing" }
		refused := func(context.Context, *op.Request) {
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		indexless := func(ctx context.Context, _ *op.Request) { setIndexless(ctx) }
		md5Header := func(v string) func(context.Context, *op.Request) {
			return func(_ context.Context, r *op.Request) { r.Header.Set("Content-MD5", v) }
		}
		both := func(steps ...func(context.Context, *op.Request)) func(context.Context, *op.Request) {
			return func(ctx context.Context, r *op.Request) {
				for _, s := range steps {
					s(ctx, r)
				}
			}
		}
		noLength := func(_ context.Context, r *op.Request) {
			r.ContentLength = 0
			r.Header.Del("Content-Length")
		}
		overLimit := func(context.Context, *op.Request) {
			f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(lcExpireDoc) - 1)
		}
		body := func(doc string) func(context.Context, *op.Request) {
			return func(_ context.Context, r *op.Request) {
				r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
				r.ContentLength = int64(len(doc))
				r.Header.Set("Content-Length", strconv.Itoa(len(doc)))
			}
		}
		stored := func(ctx context.Context, _ *op.Request) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcAttr(lcExpireConfig())})
		}
		writeFails := func(context.Context, *op.Request) {
			buckets.PutBucketAttrsStub = func(context.Context, *op.BucketRecord, map[string][]byte, []string) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}
		entryBusy := func(context.Context, *op.Request) {
			fail := func(context.Context, *op.BucketRecord) error {
				seen = append(seen, "entry")
				return errLockBusy
			}
			lc.SetEntryStub, lc.RemoveEntryStub = fail, fail
		}

		DescribeTable("stops at the first step that fails, in radosgw's order",
			func(ctx SpecContext, mk func() op.Op, fail func(context.Context, *op.Request), want error, steps []string) {
				r := request(lcExpireDoc)
				fail(ctx, r)
				Expect(op.Run(ctx, mk(), r)).To(MatchError(want))
				Expect(seen).To(Equal(steps))
			},
			Entry("PutBucketLifecycle on a missing bucket, its body unread", putOp, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("PutBucketLifecycle for a refused requester, its body unread", putOp, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("PutBucketLifecycle on an indexless bucket, before its Content-MD5, v20.2.4 rgw_op.cc:6594-6599",
				putOp, both(indexless, md5Header("!")), op.ErrMethodNotAllowed, []string{"load", "verify"}),
			Entry("PutBucketLifecycle whose Content-MD5 does not decode, InvalidDigest before the body is read, rgw_op.cc:5947-5958 at v19.2.7",
				putOp, both(md5Header("!"), noLength), op.ErrInvalidDigest, []string{"load", "verify"}),
			Entry("PutBucketLifecycle whose Content-MD5 decodes to fewer than 16 bytes, InvalidDigest before the body is read",
				putOp, both(md5Header("AAAA"), noLength), op.ErrInvalidDigest, []string{"load", "verify"}),
			Entry("PutBucketLifecycle whose Content-MD5 decodes to more than 16 bytes, InvalidDigest before the body is read",
				putOp, both(md5Header(base64.StdEncoding.EncodeToString(make([]byte, 17))), noLength), op.ErrInvalidDigest, []string{"load", "verify"}),
			Entry("PutBucketLifecycle without a Content-Length, 411 before the body is read, rgw_rest.cc:1571-1574 at v19.2.7",
				putOp, noLength, op.ErrMissingContentLength, []string{"load", "verify"}),
			Entry("PutBucketLifecycle whose Content-Length is over rgw_max_put_param_size, 416 unread, rgw_rest.cc:1556-1558 at v19.2.7",
				putOp, overLimit, op.ErrInvalidRange, []string{"load", "verify"}),
			Entry("PutBucketLifecycle whose body's MD5 is not its Content-MD5, before the parse, rgw_op.cc:5972-5989 at v19.2.7",
				putOp, both(md5Header(contentMD5("other")), body("<LifecycleConfiguration>")), op.ErrBadDigest, []string{"load", "verify", "read"}),
			Entry("PutBucketLifecycle whose body does not parse, writing nothing", putOp, body("<LifecycleConfiguration>"),
				op.ErrMalformedXML, []string{"load", "verify", "read"}),
			Entry("PutBucketLifecycle whose rule LCRule::valid refuses, InvalidArgument, writing nothing", putOp,
				body(lcBody(lcExpireRule("r", "", 0))), op.ErrInvalidArgument, []string{"load", "verify", "read"}),
			Entry("PutBucketLifecycle whose tag filter beside ExpiredObjectDeleteMarker check_and_add_rule refuses, InvalidRequest, writing nothing",
				putOp, body(lcBody(`<Rule><Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter><Status>Enabled</Status>`+
					`<Expiration><ExpiredObjectDeleteMarker>true</ExpiredObjectDeleteMarker></Expiration></Rule>`)),
				op.ErrInvalidRequest, []string{"load", "verify", "read"}),
			Entry("PutBucketLifecycle whose attr write fails otherwise than by a race, setting no entry", putOp, writeFails,
				op.ErrInternalError, []string{"load", "verify", "read", "write"}),
			Entry("PutBucketLifecycle whose entry's shard stays locked, 503 for EBUSY after the attr is written, rgw_lc.cc:2642-2663 at v19.2.7",
				putOp, entryBusy, op.ErrServiceUnavailable, []string{"load", "verify", "read", "write", "entry"}),
			Entry("DeleteBucketLifecycle on a missing bucket", delOp, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("DeleteBucketLifecycle for a refused requester", delOp, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("DeleteBucketLifecycle whose attr erase fails otherwise than by a race, removing no entry", delOp, both(stored, writeFails),
				op.ErrInternalError, []string{"load", "verify", "write"}),
			Entry("DeleteBucketLifecycle whose entry's shard stays locked, after the attr is erased", delOp, both(stored, entryBusy),
				op.ErrServiceUnavailable, []string{"load", "verify", "write", "entry"}),
			Entry("DeleteBucketLifecycle on a bucket without the attr, which writes nothing and removes the entry, rgw_lc.cc:2686-2690 at v19.2.7",
				delOp, both(none, entryBusy), op.ErrServiceUnavailable, []string{"load", "verify", "entry"}),
		)
		It("answers an EEXIST from the lock, this gateway's own hold, as radosgw's EEXIST row, 409 BucketAlreadyExists", func(ctx SpecContext) {
			lc.SetEntryReturns(&radosclient.Error{Errno: -int32(syscall.EEXIST)})
			err := op.Run(ctx, &op.PutBucketLifecycle{}, request(lcExpireDoc))
			Expect(err).To(MatchError(op.ErrBucketAlreadyExists), "rgw_common.cc:113 at v19.2.7")
			Expect(op.AsError(err).Status).To(Equal(http.StatusConflict))
		})
		DescribeTable("refuses an op mask without the op's bit before it reads the body or writes",
			func(ctx SpecContext, mk func() op.Op) {
				o := mk()
				r := request(lcExpireDoc)
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).To(Equal([]string{"load", "verify"}))
				Expect(lc.Invocations()).To(BeEmpty())
			},
			Entry("PutBucketLifecycle, without write", putOp),
			Entry("DeleteBucketLifecycle, without write", delOp),
		)
		DescribeTable("authorizes its action against the bucket with the ACL permission op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, steps []string) {
				stored(ctx, nil)
				Expect(op.Run(ctx, mk(), request(lcExpireDoc))).To(Succeed())
				Expect(seen).To(Equal(steps))
				Expect(authzr.VerifyBucketCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(policy.S3PutLifecycleConfiguration))
				Expect(gotPerm).To(Equal(acl.PermWriteACP))
			},
			Entry("PutBucketLifecycle, WRITE_ACP", putOp, []string{"load", "verify", "read", "write", "entry"}),
			Entry("DeleteBucketLifecycle, WRITE_ACP as s3:PutLifecycleConfiguration", delOp, []string{"load", "verify", "write", "entry"}),
		)
	})

	DescribeTable("refuses an indexless bucket with 405 and Tentacle's message on both releases, storing nothing",
		func(ctx SpecContext, rel denc.Release) {
			useFixture(ctx, rel)
			setIndexless(ctx)
			err := put(ctx, putReq(lcExpireDoc))
			Expect(err).To(MatchError(op.ErrMethodNotAllowed))
			e := op.AsError(err)
			Expect(e.Status).To(Equal(http.StatusMethodNotAllowed))
			Expect(e.Message).To(Equal("Indexless buckets do not support lifecycle policy"), "rgw_op.cc:6594-6599 at v20.2.4")
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
			Expect(lc.SetEntryCallCount()).To(BeZero())
		},
		Entry("Squid, whose radosgw stores it (docs/exclusions.md)", denc.Squid),
		Entry("Tentacle", denc.Tentacle),
	)

	Describe("PutBucketLifecycle", func() {
		It("stores the configuration in the lifecycle attr alone, then sets the bucket's entry", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(tags.Set{})})
			before := bucket(ctx).Attrs
			Expect(put(ctx, putReq(lcExpireDoc))).To(Succeed())
			want := maps.Clone(before)
			want[op.AttrLC] = lcAttr(lcExpireConfig())
			Expect(bucket(ctx).Attrs).To(Equal(want), "set_bucket_config, rgw_lc.cc:2642-2654 at v19.2.7")
			Expect(lc.SetEntryCallCount()).To(Equal(1))
			_, rec := lc.SetEntryArgsForCall(0)
			Expect(rec.Info.Bucket).To(Equal(bucket(ctx).Info.Bucket))
			Expect(rec.Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())), "the entry is set for the bucket as written")
			Expect(lc.RemoveEntryCallCount()).To(BeZero())
		})
		It("writes the same bytes on Tentacle", func(ctx SpecContext) {
			useFixture(ctx, denc.Tentacle)
			Expect(put(ctx, putReq(lcExpireDoc))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())))
		})
		It("replaces a stored configuration whole", func(ctx SpecContext) {
			Expect(put(ctx, putReq(lcBody(lcExpireRule("a", "x/", 3), lcExpireRule("b", "y/", 4))))).To(Succeed())
			Expect(put(ctx, putReq(lcExpireDoc))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())))
			Expect(lc.SetEntryCallCount()).To(Equal(2), "set_entry on every put")
		})
		It("keeps the attr when the entry's shard stays locked, as radosgw does, and fails with the lock's 503", func(ctx SpecContext) {
			lc.SetEntryReturns(errLockBusy)
			err := put(ctx, putReq(lcExpireDoc))
			Expect(err).To(MatchError(op.ErrServiceUnavailable), "-EBUSY, rgw_common.cc:134 at v19.2.7")
			Expect(err).To(MatchError(ContainSubstring("lc.17")), "the lock's error is kept for the log")
			Expect(op.AsError(err).Status).To(Equal(http.StatusServiceUnavailable))
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())), "rgw_lc.cc:2642-2654, then :2659-2663, at v19.2.7")
		})
		It("refuses a requester before it reads the body", func(ctx SpecContext) {
			r := putReq(lcExpireDoc)
			r.Identity, r.Body = f.bob, failingReader{}
			Expect(put(ctx, r)).To(MatchError(op.ErrAccessDenied), "get_params runs in execute, rgw_op.cc:5965 at v19.2.7")
		})

		Describe("its Content-MD5, decoded by from_base64", func() {
			DescribeTable("checks the body's MD5 against a 16-byte one, BadDigest with radosgw's message when they differ, and refuses any other as InvalidDigest",
				func(ctx SpecContext, value func() []string, wantErr error, message string) {
					r := putReq(lcExpireDoc)
					for _, v := range value() {
						r.Header.Add("Content-MD5", v)
					}
					err := put(ctx, r)
					if wantErr == nil {
						Expect(err).NotTo(HaveOccurred())
						Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())))
						return
					}
					Expect(err).To(MatchError(wantErr))
					Expect(op.AsError(err).Status).To(Equal(http.StatusBadRequest))
					Expect(op.AsError(err).Message).To(Equal(message))
					Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
					Expect(lc.SetEntryCallCount()).To(BeZero())
				},
				Entry("absent, which radosgw no longer requires, rgw_op.cc:5941-5944 at v19.2.7", func() []string { return nil }, nil, ""),
				Entry("the body's", func() []string { return []string{contentMD5(lcExpireDoc)} }, nil, ""),
				Entry("the body's after another, the last of a repeated header, as radosgw's env keeps it",
					func() []string { return []string{contentMD5("other"), contentMD5(lcExpireDoc)} }, nil, ""),
				Entry("another body's, rgw_op.cc:5980-5988 at v19.2.7", func() []string { return []string{contentMD5("other")} }, op.ErrBadDigest,
					"The Content-MD5 you specified did not match what we received."),
				Entry("one that does not decode, InvalidDigest with the message radosgw's BadDigest carries, rgw_op.cc:5947-5958 at v19.2.7",
					func() []string { return []string{"*" + contentMD5(lcExpireDoc)} },
					op.ErrInvalidDigest, "Request header Content-MD5 contains character that is not base64 encoded."),
				Entry("one that decodes short, of which radosgw compares 16 bytes all the same", func() []string {
					return []string{base64.StdEncoding.EncodeToString([]byte("0123456789abcde"))}
				}, op.ErrInvalidDigest, ""),
				Entry("one that decodes long, though its first 16 bytes are the body's MD5, which radosgw takes", func() []string {
					sum := md5.Sum([]byte(lcExpireDoc)) //nolint:gosec // a Content-MD5 is an MD5
					return []string{base64.StdEncoding.EncodeToString(append(sum[:], 'x'))}
				}, op.ErrInvalidDigest, ""),
				Entry("an empty one, which is present and decodes to no bytes", func() []string { return []string{""} }, op.ErrInvalidDigest, ""),
				Entry("all '=', which ends radosgw's from_base64 out of bounds (docs/ceph-upstream-bugs.md)", func() []string { return []string{"===="} },
					op.ErrInvalidDigest, ""),
			)
		})

		Describe("the body, as read_all_input reads it under rgw_max_put_param_size", func() {
			It("is MissingContentLength unread without a Content-Length or a chunked encoding", func(ctx SpecContext) {
				r := f.req(http.MethodPut, "plain", "")
				r.Body, r.ContentLength = failingReader{}, 0
				Expect(put(ctx, r)).To(MatchError(op.ErrMissingContentLength))
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
			})
			It("is InvalidRange unread when its Content-Length is over rgw_max_put_param_size", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(lcExpireDoc) - 1)
				r := putReq(lcExpireDoc)
				r.Body = failingReader{}
				Expect(put(ctx, r)).To(MatchError(op.ErrInvalidRange), "rgw_rest.cc:1556-1558 at v19.2.7")
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
			})
			It("is read at rgw_max_put_param_size", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(lcExpireDoc))
				Expect(put(ctx, putReq(lcExpireDoc))).To(Succeed())
			})
			It("is read when chunked, as RGWOp::read_all_input drops the allow_chunked get_params passes", func(ctx SpecContext) {
				r := f.req(http.MethodPut, "plain", "")
				r.Body, r.ContentLength = strings.NewReader(lcExpireDoc), -1
				Expect(put(ctx, r)).To(Succeed(), "rgw_rest.cc:1487 at v19.2.7")
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())))
			})
			It("returns the body's verification verdict from its final Read, before parsing", func(ctx SpecContext) {
				r := putReq(lcExpireDoc)
				r.Body = io.MultiReader(strings.NewReader(lcExpireDoc), errReader{op.ErrContentSHA256Mismatch})
				Expect(put(ctx, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
			})
		})

		DescribeTable("answers a body lifecycle.Parse refuses with its 400 and no message, storing nothing",
			func(ctx SpecContext, doc string, want *op.Error) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcAttr(lcExpireConfig())})
				err := put(ctx, putReq(doc))
				Expect(err).To(MatchError(want))
				Expect(op.AsError(err)).To(Equal(want), "RGWPutLC::execute sets no message, rgw_op.cc:5991-6006 at v19.2.7")
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcExpireConfig())))
				Expect(lc.SetEntryCallCount()).To(BeZero())
			},
			Entry("an empty body", "", op.ErrMalformedXML),
			Entry("an unclosed root", "<LifecycleConfiguration>", op.ErrMalformedXML),
			Entry("a root other than LifecycleConfiguration, which radosgw stores as no rules (docs/ceph-upstream-bugs.md)",
				`<Tagging>`+lcExpireRule("rule1", "", 1)+`</Tagging>`, op.ErrMalformedXML),
			Entry("a configuration without a Rule", lcBody(), op.ErrMalformedXML),
			Entry("a rule without Status", lcBody(`<Rule><ID>r</ID><Prefix></Prefix><Expiration><Days>1</Days></Expiration></Rule>`), op.ErrMalformedXML),
			Entry("a rule whose Expiration Days is 0, which LCExpiration::valid refuses", lcBody(lcExpireRule("r", "", 0)), op.ErrInvalidArgument),
			Entry("two rules under one ID", lcBody(lcExpireRule("r", "a/", 1), lcExpireRule("r", "b/", 1)), op.ErrInvalidArgument),
			Entry("a tag filter beside AbortIncompleteMultipartUpload", lcBody(`<Rule><Filter><Tag><Key>k</Key><Value>v</Value></Tag></Filter>`+
				`<Status>Enabled</Status><AbortIncompleteMultipartUpload><DaysAfterInitiation>1</DaysAfterInitiation></AbortIncompleteMultipartUpload></Rule>`),
				op.ErrInvalidRequest),
		)

		Describe("the rule count, as rgw_lc_max_rules bounds it", func() {
			It("reads the option at each request", func(ctx SpecContext) {
				doc := lcBody(lcExpireRule("a", "x/", 1), lcExpireRule("b", "y/", 1))
				f.conf["rgw_lc_max_rules"] = "1"
				err := put(ctx, putReq(doc))
				Expect(err).To(MatchError(op.ErrMalformedXML), "rgw_lc_s3.cc:109-114 at v19.2.7")
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
				f.conf["rgw_lc_max_rules"] = "2"
				Expect(put(ctx, putReq(doc))).To(Succeed())
			})
			It("takes its default, 1000, when the option cannot be read", func(ctx SpecContext) {
				rules := make([]string, lifecycle.DefaultMaxRules+1)
				for i := range rules {
					rules[i] = lcExpireRule(fmt.Sprintf("r%04d", i), "", 1)
				}
				Expect(put(ctx, putReq(lcBody(rules[1:]...)))).To(Succeed())
				Expect(put(ctx, putReq(lcBody(rules...)))).To(MatchError(op.ErrMalformedXML))
			})
			It("takes a count past an int's range as the most an int holds", func(ctx SpecContext) {
				f.conf["rgw_lc_max_rules"] = "18446744073709551615"
				Expect(put(ctx, putReq(lcExpireDoc))).To(Succeed())
			})
		})
	})

	Describe("DeleteBucketLifecycle", func() {
		It("removes the lifecycle attr and nothing else, then the bucket's entry", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: encodeTags(tags.Set{})})
			before := bucket(ctx).Attrs
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcAttr(lcExpireConfig())})
			Expect(op.Run(ctx, &op.DeleteBucketLifecycle{}, delReq())).To(Succeed())
			Expect(bucket(ctx).Attrs).To(Equal(before), "rgw_lc.cc:2676-2684 at v19.2.7")
			Expect(lc.RemoveEntryCallCount()).To(Equal(1))
			_, rec := lc.RemoveEntryArgsForCall(0)
			Expect(rec.Info.Bucket).To(Equal(bucket(ctx).Info.Bucket))
		})
		It("removes an attr that does not decode", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: {0xff}})
			Expect(op.Run(ctx, &op.DeleteBucketLifecycle{}, delReq())).To(Succeed())
			Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrLC))
		})
		It("writes nothing for a bucket without the attr and removes its entry all the same", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.DeleteBucketLifecycle{}, delReq())).To(Succeed())
			Expect(bucket(ctx)).To(Equal(before), "put_info only when attrs.erase finds the attr, rgw_lc.cc:2677-2678 at v19.2.7")
			Expect(lc.RemoveEntryCallCount()).To(Equal(1), "rm_entry whether or not, rgw_lc.cc:2686-2690 at v19.2.7")
		})
	})

	Describe("a lifecycle write that loses a race", func() {
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcAttr(lcExpireConfig())})
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			Expect(put(ctx, putReq(lcExpireDoc))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16), "where radosgw makes one attempt (docs/exclusions.md)")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			_, _, set, rm := buckets.PutBucketAttrsArgsForCall(15)
			Expect(set).To(Equal(map[string][]byte{op.AttrLC: lcAttr(lcExpireConfig())}), "the body read once, its configuration alone on each try")
			Expect(rm).To(BeEmpty())
			Expect(lc.SetEntryCallCount()).To(BeZero(), "no entry for a configuration not stored")
			Expect(op.Run(ctx, &op.DeleteBucketLifecycle{}, delReq())).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(32))
			_, _, set, rm = buckets.PutBucketAttrsArgsForCall(31)
			Expect(set).To(BeEmpty())
			Expect(rm).To(Equal([]string{op.AttrLC}))
			Expect(lc.RemoveEntryCallCount()).To(BeZero())
		})
		It("lets a delete's retry write nothing once the write it lost to removed the attr", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrLC: lcAttr(lcExpireConfig())})
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketAttrsStub = func(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
				Expect(f.store.PutBucketAttrs(ctx, bucket(ctx), nil, []string{op.AttrLC})).To(Succeed())
				return op.ErrConcurrentModification
			}
			f.env.Buckets = buckets
			Expect(op.Run(ctx, &op.DeleteBucketLifecycle{}, delReq())).To(Succeed())
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry finds no attr to erase")
			Expect(lc.RemoveEntryCallCount()).To(Equal(1))
		})

		Describe("to another write of the bucket", func() {
			// bobCanWriteACP is alice's ACL with WRITE_ACP, the permission
			// both lifecycle writes map to, granted to bob.
			bobCanWriteACP := func() []byte {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				return encodedPolicy(p)
			}
			revokeByACL := func() map[string][]byte { return map[string][]byte{meta.AttrACL: f.aliceACL} }
			denyBob := func() map[string][]byte {
				names, err := json.Marshal([]string{"s3:PutLifecycleConfiguration"})
				Expect(err).NotTo(HaveOccurred())
				return map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
					`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":%s,"Resource":"arn:aws:s3:::plain"}]}`,
					names)}
			}
			retagged := func() map[string][]byte {
				var s tags.Set
				Expect(s.Add("team", "b", tags.MaxBucketTags)).To(Succeed())
				return map[string][]byte{tags.Attr: encodeTags(s)}
			}
			var (
				buckets *opfakes.FakeBucketStore
				raced   map[string][]byte
			)
			// race gives plain a stored configuration and bob WRITE_ACP,
			// then makes the first attr write lose to concurrent, another
			// request's change of the bucket's attrs, and lets every retry
			// through to the store.
			race := func(ctx context.Context, concurrent map[string][]byte) {
				GinkgoHelper()
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				f.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: bobCanWriteACP(), op.AttrLC: lcAttr(lcExpireConfig())})
				buckets = &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = f.store.GetBucket
				buckets.PutBucketAttrsStub = func(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
					if buckets.PutBucketAttrsCallCount() == 1 {
						f.setBucketAttrs(ctx, concurrent)
						raced = maps.Clone(bucket(ctx).Attrs)
					}
					return f.store.PutBucketAttrs(ctx, rec, set, rm)
				}
				f.env.Buckets = buckets
			}
			asBob := func(r *op.Request) *op.Request {
				r.Identity = f.bob
				return r
			}
			type write struct {
				op    op.Op
				req   *op.Request
				check func(attrs map[string][]byte)
				entry func() int
			}
			putWrite := func() write {
				return write{
					op: &op.PutBucketLifecycle{}, req: asBob(putReq(lcUpdatedDoc)),
					check: func(attrs map[string][]byte) {
						Expect(attrs).To(HaveKeyWithValue(op.AttrLC, lcAttr(lcUpdatedConfig())))
					},
					entry: lc.SetEntryCallCount,
				}
			}
			delWrite := func() write {
				return write{
					op: &op.DeleteBucketLifecycle{}, req: asBob(delReq()),
					check: func(attrs map[string][]byte) { Expect(attrs).NotTo(HaveKey(op.AttrLC)) },
					entry: lc.RemoveEntryCallCount,
				}
			}

			DescribeTable("writes over the bucket as the retry read it, keeping the other write's change",
				func(ctx SpecContext, mk func() write) {
					race(ctx, retagged())
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2), "the first try lost, the retry wrote")
					attrs := bucket(ctx).Attrs
					w.check(attrs)
					Expect(attrs).To(HaveKeyWithValue(tags.Attr, retagged()[tags.Attr]))
					Expect(w.entry()).To(Equal(1))
				},
				Entry("PutBucketLifecycle", putWrite),
				Entry("DeleteBucketLifecycle", delWrite),
			)
			DescribeTable("refuses the retry once the write it lost to revoked the requester's permission, leaving the revoking bucket",
				func(ctx SpecContext, mk func() write, revoke func() map[string][]byte) {
					race(ctx, revoke())
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
					Expect(bucket(ctx).Attrs).To(Equal(raced))
					Expect(w.entry()).To(BeZero())
				},
				Entry("PutBucketLifecycle under an ACL that drops the grant", putWrite, revokeByACL),
				Entry("PutBucketLifecycle under a policy that denies s3:PutLifecycleConfiguration", putWrite, denyBob),
				Entry("DeleteBucketLifecycle under an ACL that drops the grant", delWrite, revokeByACL),
				Entry("DeleteBucketLifecycle under a policy that denies s3:PutLifecycleConfiguration, the action it authorizes", delWrite, denyBob),
			)
		})
	})
})
