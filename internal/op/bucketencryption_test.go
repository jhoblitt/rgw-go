package op_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/sse"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// kmsEncryptionDoc is a PutBucketEncryption body naming aws:kms with key id
// k and a bucket key.
const kmsEncryptionDoc = `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>aws:kms</SSEAlgorithm><KMSMasterKeyID>k</KMSMasterKeyID>` +
	`</ApplyServerSideEncryptionByDefault><BucketKeyEnabled>true</BucketKeyEnabled></Rule></ServerSideEncryptionConfiguration>`

// aesEncryptionDoc is a PutBucketEncryption body naming AES256 alone.
const aesEncryptionDoc = `<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault>` +
	`<SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`

// kmsEncryption is kmsEncryptionDoc's configuration.
func kmsEncryption() sse.BucketEncryptionConfig {
	return sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{
		Apply:            sse.ApplyServerSideEncryptionByDefault{KMSMasterKeyID: "k", SSEAlgorithm: "aws:kms"},
		BucketKeyEnabled: true,
	}}
}

// aesEncryption is aesEncryptionDoc's configuration.
func aesEncryption() sse.BucketEncryptionConfig {
	return sse.BucketEncryptionConfig{Rule: &sse.ServerSideEncryptionConfiguration{
		Apply: sse.ApplyServerSideEncryptionByDefault{SSEAlgorithm: "AES256"},
	}}
}

func encryptionAttr(c sse.BucketEncryptionConfig) []byte {
	e := denc.NewEncoder()
	c.Encode(e, denc.Squid)
	return e.Bytes()
}

// blockedRuleAttr is an AES256 default as ceph main 96ed8b8c780 stores it,
// its rule at ENCODE_START(2, 1) with BlockedEncryptionTypes after the bucket
// key
// (https://github.com/ceph/ceph/blob/96ed8b8c780/src/rgw/rgw_bucket_encryption.h#L91-L107 at 96ed8b8c780).
func blockedRuleAttr() []byte {
	e := denc.NewEncoder()
	cfg := e.BeginStruct(1, 1)
	e.Bool(true)
	rule := e.BeginStruct(2, 1)
	apply := e.BeginStruct(1, 1)
	e.String("")
	e.String("AES256")
	e.EndStruct(apply)
	e.Bool(false)
	e.U32(1)
	e.String("SSE-C")
	e.EndStruct(rule)
	e.EndStruct(cfg)
	return e.Bytes()
}

// stepReader appends "read" to seen at its first Read.
type stepReader struct {
	r    io.Reader
	seen *[]string
	read bool
}

func (s *stepReader) Read(p []byte) (int, error) {
	if !s.read {
		s.read = true
		*s.seen = append(*s.seen, "read")
	}
	return s.r.Read(p)
}

var _ = Describe("bucket encryption ops", func() {
	var f *writeFixture
	BeforeEach(func(ctx SpecContext) { f = newWriteFixture(ctx, denc.Squid) })

	bucket := func(ctx context.Context) *op.BucketRecord {
		GinkgoHelper()
		rec, err := f.store.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	putReq := func(body string) *op.Request { return f.bodyReq(http.MethodPut, "", body) }
	// teamTag is the tag set team=v.
	teamTag := func(v string) []byte {
		GinkgoHelper()
		var s tags.Set
		Expect(s.Add("team", v, tags.MaxBucketTags)).To(Succeed())
		return encodeTags(s)
	}

	DescribeTable("is radosgw's op, authorized as its action under its op mask",
		func(o op.Op, name string, a policy.Action, mask uint32) {
			Expect(o.Name()).To(Equal(name))
			Expect(o.Action()).To(Equal(a))
			Expect(o.OpMask()).To(Equal(mask))
		},
		Entry("GetBucketEncryption, rgw_op.h:1782-1794 at v19.2.7", &op.GetBucketEncryption{}, "get_bucket_encryption", policy.S3GetBucketEncryption, op.OpTypeRead),
		Entry("PutBucketEncryption, rgw_op.h:1766-1780 at v19.2.7", &op.PutBucketEncryption{}, "put_bucket_encryption", policy.S3PutBucketEncryption, op.OpTypeWrite),
		Entry("DeleteBucketEncryption, a write authorized as s3:PutBucketEncryption, rgw_op.h:1796-1808 and rgw_op.cc:8843-8849 at v19.2.7",
			&op.DeleteBucketEncryption{}, "delete_bucket_encryption", policy.S3PutBucketEncryption, op.OpTypeWrite),
	)

	It("names the key id attr radosgw's SSE-S3 backend writes", func() {
		Expect(op.AttrBucketEncryptionKeyID).To(Equal("user.rgw.sse-s3.key-id"), "rgw_common.h:173-175 at v19.2.7")
	})

	Describe("error order", func() {
		var (
			seen    []string
			buckets *opfakes.FakeBucketStore
			authzr  *opfakes.FakeAuthorizer
		)
		// Each fake records its step in seen and passes the call to the
		// memstore, or allows it; the body records "read" when it is first
		// read.
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
			f.env.Buckets, f.env.Authz = buckets, authzr
		})
		// request is alice's request on plain whose body is doc with its
		// Content-Length, as stepReader records its read.
		request := func(doc string) *op.Request {
			r := putReq(doc)
			r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
			return r
		}

		get := func() op.Op { return &op.GetBucketEncryption{} }
		put := func() op.Op { return &op.PutBucketEncryption{} }
		del := func() op.Op { return &op.DeleteBucketEncryption{} }

		none := func(context.Context, *op.Request) {}
		missing := func(_ context.Context, r *op.Request) { r.Bucket = "missing" }
		refused := func(context.Context, *op.Request) {
			authzr.VerifyBucketStub = func(context.Context, *op.Request, policy.Action, acl.Permission) error {
				seen = append(seen, "verify")
				return op.ErrAccessDenied
			}
		}
		stored := func(b []byte) func(context.Context, *op.Request) {
			return func(ctx context.Context, _ *op.Request) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: b})
			}
		}
		noLength := func(_ context.Context, r *op.Request) {
			r.ContentLength = 0
			r.Header.Del("Content-Length")
		}
		overLimit := func(context.Context, *op.Request) {
			f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(kmsEncryptionDoc) - 1)
		}
		body := func(doc string) func(context.Context, *op.Request) {
			return func(_ context.Context, r *op.Request) {
				r.Body = &stepReader{r: strings.NewReader(doc), seen: &seen}
				r.ContentLength = int64(len(doc))
				r.Header.Set("Content-Length", strconv.Itoa(len(doc)))
			}
		}
		writeFails := func(context.Context, *op.Request) {
			buckets.PutBucketAttrsStub = func(context.Context, *op.BucketRecord, map[string][]byte, []string) error {
				seen = append(seen, "write")
				return op.ErrInternalError
			}
		}

		DescribeTable("stops at the first step that fails, in radosgw's order",
			func(ctx SpecContext, mk func() op.Op, fail func(context.Context, *op.Request), want error, steps []string) {
				r := request(kmsEncryptionDoc)
				fail(ctx, r)
				Expect(op.Run(ctx, mk(), r)).To(MatchError(want))
				Expect(seen).To(Equal(steps))
			},
			Entry("GetBucketEncryption on a missing bucket, before any permission check", get, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("GetBucketEncryption for a refused requester, before it looks for the attr", get, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("GetBucketEncryption on a bucket without a configuration", get, none, op.ErrNoSuchBucketEncryption, []string{"load", "verify"}),
			Entry("GetBucketEncryption on a configuration that does not decode", get, stored([]byte{0xff}), op.ErrUnknown, []string{"load", "verify"}),
			Entry("PutBucketEncryption on a missing bucket, its body unread", put, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("PutBucketEncryption for a refused requester, its body unread, rgw_op.cc:8780 at v19.2.7", put, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("PutBucketEncryption without a Content-Length, 411 before the body is read or parsed, rgw_rest.cc:1571-1574 at v19.2.7 and v20.2.4",
				put, noLength, op.ErrMissingContentLength, []string{"load", "verify"}),
			Entry("PutBucketEncryption whose Content-Length is over rgw_max_put_param_size, 416 before the body is read or parsed, rgw_rest.cc:1556-1558 at v19.2.7",
				put, overLimit, op.ErrInvalidRange, []string{"load", "verify"}),
			Entry("PutBucketEncryption whose body does not parse, writing nothing", put, body("<ServerSideEncryptionConfiguration>"),
				op.ErrMalformedXML, []string{"load", "verify", "read"}),
			Entry("PutBucketEncryption whose configuration Validate refuses, writing nothing", put,
				body(strings.Replace(aesEncryptionDoc, "AES256", "AES-256", 1)), op.ErrMalformedXML, []string{"load", "verify", "read"}),
			Entry("PutBucketEncryption whose write fails otherwise than by a race, retrying nothing", put, writeFails,
				op.ErrInternalError, []string{"load", "verify", "read", "write"}),
			Entry("DeleteBucketEncryption on a missing bucket", del, missing, op.ErrNoSuchBucket, []string{"load"}),
			Entry("DeleteBucketEncryption for a refused requester", del, refused, op.ErrAccessDenied, []string{"load", "verify"}),
			Entry("DeleteBucketEncryption whose write fails otherwise than by a race", del, writeFails, op.ErrInternalError, []string{"load", "verify", "write"}),
		)
		DescribeTable("refuses an op mask without the op's bit before it reads the body or writes",
			func(ctx SpecContext, mk func() op.Op) {
				o := mk()
				r := request(kmsEncryptionDoc)
				r.Identity.OpMask = op.OpTypeAll &^ o.OpMask()
				Expect(op.Run(ctx, o, r)).To(MatchError(op.ErrAccessDenied))
				Expect(seen).To(Equal([]string{"load", "verify"}))
			},
			Entry("GetBucketEncryption, without read", get),
			Entry("PutBucketEncryption, without write", put),
			Entry("DeleteBucketEncryption, without write", del),
		)
		DescribeTable("authorizes its action against the bucket with the ACL permission op_to_perm gives it",
			func(ctx SpecContext, mk func() op.Op, a policy.Action, perm acl.Permission, steps []string) {
				stored(encryptionAttr(aesEncryption()))(ctx, nil)
				Expect(op.Run(ctx, mk(), request(kmsEncryptionDoc))).To(Succeed())
				Expect(seen).To(Equal(steps))
				Expect(authzr.VerifyBucketCallCount()).To(Equal(1))
				_, r, gotA, gotPerm := authzr.VerifyBucketArgsForCall(0)
				Expect(r.BucketRec.Info.Bucket.Name).To(Equal("plain"))
				Expect(gotA).To(Equal(a))
				Expect(gotPerm).To(Equal(perm))
			},
			Entry("GetBucketEncryption, READ_ACP, rgw_iam_policy.h:273-289 at v19.2.7", get, policy.S3GetBucketEncryption, acl.PermReadACP,
				[]string{"load", "verify"}),
			Entry("PutBucketEncryption, WRITE_ACP, rgw_iam_policy.h:297-311 at v19.2.7", put, policy.S3PutBucketEncryption, acl.PermWriteACP,
				[]string{"load", "verify", "read", "write"}),
			Entry("DeleteBucketEncryption, WRITE_ACP as s3:PutBucketEncryption", del, policy.S3PutBucketEncryption, acl.PermWriteACP,
				[]string{"load", "verify", "write"}),
		)
	})

	DescribeTable("gives a policy none of the bucket's tags, as no encryption op calls rgw_iam_add_buckettags",
		func(ctx SpecContext, mk func() op.Op, req func() *op.Request, action string) {
			f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encryptionAttr(aesEncryption()), tags.Attr: teamTag("a")})
			deny := func(condition string) []byte {
				return fmt.Appendf(nil,
					`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":%q,"Resource":"arn:aws:s3:::plain"%s}]}`,
					action, condition)
			}
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: deny("")})
			Expect(op.Run(ctx, mk(), req())).To(MatchError(op.ErrAccessDenied), "the policy names the op's action")
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: deny(`,"Condition":{"StringEquals":{"s3:ResourceTag/team":"a"}}`)})
			Expect(op.Run(ctx, mk(), req())).To(Succeed(), "a tagged bucket the tag-conditioned Deny does not reach")
		},
		Entry("GetBucketEncryption, rgw_op.cc:8814-8820 at v19.2.7", func() op.Op { return &op.GetBucketEncryption{} },
			func() *op.Request { return f.req(http.MethodGet, "plain", "") }, "s3:GetBucketEncryption"),
		Entry("PutBucketEncryption, rgw_op.cc:8764-8770 at v19.2.7", func() op.Op { return &op.PutBucketEncryption{} },
			func() *op.Request { return putReq(aesEncryptionDoc) }, "s3:PutBucketEncryption"),
		Entry("DeleteBucketEncryption, rgw_op.cc:8843-8849 at v19.2.7", func() op.Op { return &op.DeleteBucketEncryption{} },
			func() *op.Request { return f.req(http.MethodDelete, "plain", "") }, "s3:PutBucketEncryption"),
	)

	Describe("GetBucketEncryption", func() {
		run := func(ctx context.Context) (*op.GetBucketEncryption, error) {
			o := &op.GetBucketEncryption{}
			return o, op.Run(ctx, o, f.req(http.MethodGet, "plain", ""))
		}

		It("answers a bucket without the attr with 404 and execute's message", func(ctx SpecContext) {
			_, err := run(ctx)
			Expect(err).To(MatchError(op.ErrNoSuchBucketEncryption), "-ENOENT, which send_response maps, rgw_rest_s3.cc:3888-3893 at v19.2.7")
			e := op.AsError(err)
			Expect(e.Status).To(Equal(404))
			Expect(e.Code).To(Equal("ServerSideEncryptionConfigurationNotFoundError"))
			Expect(e.Message).To(Equal("The server side encryption configuration was not found"), "rgw_op.cc:8828-8829 at v19.2.7")
		})
		It("takes the stored configuration as it decodes", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encryptionAttr(kmsEncryption())})
			o, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Config).To(Equal(kmsEncryption()))
		})
		It("takes a configuration without a rule, which a document without one stores", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encryptionAttr(sse.BucketEncryptionConfig{})})
			o, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Config).To(Equal(sse.BucketEncryptionConfig{}))
		})
		It("ignores the bytes after the configuration, as decode reads no further", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: append(encryptionAttr(kmsEncryption()), 'x', 'y', 'z')})
			o, err := run(ctx)
			Expect(err).NotTo(HaveOccurred(), "rgw_op.cc:8832-8839 at v19.2.7")
			Expect(o.Config).To(Equal(kmsEncryption()))
		})
		DescribeTable("answers an attr that does not decode with 500 UnknownError, radosgw's -EIO, which no error row holds",
			func(ctx SpecContext, b []byte) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: b})
				_, err := run(ctx)
				Expect(err).To(MatchError(op.ErrUnknown), "rgw_op.cc:8835-8839 and rgw_common.cc:352-353 at v19.2.7")
				Expect(op.AsError(err).Status).To(Equal(500))
			},
			Entry("a truncated one", []byte{0xff}),
			Entry("an empty one, which is present, so not 404", []byte{}),
			Entry("one whose compat version is above 1", []byte{2, 2, 1, 0, 0, 0, 0}),
		)
		It("reads a rule stored at (2,1) and leaves its bytes as they are", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: blockedRuleAttr()})
			before := bucket(ctx)
			o, err := run(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Config).To(Equal(aesEncryption()), "BlockedEncryptionTypes skipped, as DECODE_START(1) skips it")
			Expect(bucket(ctx)).To(Equal(before), "nothing is written")
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, blockedRuleAttr()))
		})
	})

	Describe("PutBucketEncryption", func() {
		It("stores the configuration in the policy attr alone, which GetBucketEncryption reads back", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag("a"), op.AttrBucketEncryptionKeyID: []byte("kek")})
			before := bucket(ctx).Attrs
			Expect(op.Run(ctx, &op.PutBucketEncryption{}, putReq(kmsEncryptionDoc))).To(Succeed())
			want := maps.Clone(before)
			want[op.AttrBucketEncryption] = encryptionAttr(kmsEncryption())
			Expect(bucket(ctx).Attrs).To(Equal(want), "ENCODE_START(1, 1), rgw_op.cc:8805-8811 at v19.2.7")
			g := &op.GetBucketEncryption{}
			Expect(op.Run(ctx, g, f.req(http.MethodGet, "plain", ""))).To(Succeed())
			Expect(g.Config).To(Equal(kmsEncryption()))
		})
		It("writes the same bytes on Tentacle", func(ctx SpecContext) {
			f = newWriteFixture(ctx, denc.Tentacle)
			Expect(op.Run(ctx, &op.PutBucketEncryption{}, putReq(kmsEncryptionDoc))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encryptionAttr(kmsEncryption())))
		})
		It("refuses a requester before it reads the body", func(ctx SpecContext) {
			r := putReq(kmsEncryptionDoc)
			r.Identity, r.Body = f.bob, failingReader{}
			Expect(op.Run(ctx, &op.PutBucketEncryption{}, r)).To(MatchError(op.ErrAccessDenied), "get_params runs in execute, rgw_op.cc:8780 at v19.2.7")
		})

		Describe("the body, as read_all_input reads it under rgw_max_put_param_size", func() {
			It("is MissingContentLength unread without a Content-Length or a chunked encoding", func(ctx SpecContext) {
				r := f.req(http.MethodPut, "plain", "")
				r.Body, r.ContentLength = failingReader{}, 0
				Expect(op.Run(ctx, &op.PutBucketEncryption{}, r)).To(MatchError(op.ErrMissingContentLength), "rgw_rest.cc:1571-1574 and rgw_common.cc:87 at v19.2.7, :1571-1574 and :88 at v20.2.4")
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
			})
			It("is InvalidRange unread when its Content-Length is over rgw_max_put_param_size", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(kmsEncryptionDoc) - 1)
				r := putReq(kmsEncryptionDoc)
				r.Body = failingReader{}
				Expect(op.Run(ctx, &op.PutBucketEncryption{}, r)).To(MatchError(op.ErrInvalidRange), "rgw_rest.cc:1556-1558 and rgw_common.cc:128 at v19.2.7")
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
			})
			It("is read at rgw_max_put_param_size", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(len(kmsEncryptionDoc))
				Expect(op.Run(ctx, &op.PutBucketEncryption{}, putReq(kmsEncryptionDoc))).To(Succeed())
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encryptionAttr(kmsEncryption())))
			})
			It("is read when chunked, as RGWOp::read_all_input drops the allow_chunked get_params passes", func(ctx SpecContext) {
				r := f.req(http.MethodPut, "plain", "")
				r.Body, r.ContentLength = strings.NewReader(aesEncryptionDoc), -1
				Expect(op.Run(ctx, &op.PutBucketEncryption{}, r)).To(Succeed(), "rgw_op.h:216-228 at v19.2.7")
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encryptionAttr(aesEncryption())))
			})
			It("is read past sse.MaxConfigXML under a raised option, which the parser then refuses", func(ctx SpecContext) {
				f.conf["rgw_max_put_param_size"] = strconv.Itoa(2 * sse.MaxConfigXML)
				doc := aesEncryptionDoc + strings.Repeat(" ", sse.MaxConfigXML+1-len(aesEncryptionDoc))
				err := op.Run(ctx, &op.PutBucketEncryption{}, putReq(doc))
				Expect(err).To(MatchError(op.ErrMalformedXML))
				Expect(err).NotTo(MatchError(op.ErrInvalidRange))
			})
			It("returns the body's verification verdict from its final Read, before parsing", func(ctx SpecContext) {
				r := putReq(aesEncryptionDoc)
				r.Body = io.MultiReader(strings.NewReader(aesEncryptionDoc), errReader{op.ErrContentSHA256Mismatch})
				Expect(op.Run(ctx, &op.PutBucketEncryption{}, r)).To(MatchError(op.ErrContentSHA256Mismatch))
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
			})
		})

		DescribeTable("answers a body sse.ParseXML refuses with 400 MalformedXML and no message, storing nothing",
			func(ctx SpecContext, doc string) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: encryptionAttr(kmsEncryption())})
				err := op.Run(ctx, &op.PutBucketEncryption{}, putReq(doc))
				Expect(err).To(MatchError(op.ErrMalformedXML))
				Expect(op.AsError(err)).To(Equal(op.ErrMalformedXML))
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encryptionAttr(kmsEncryption())))
			},
			Entry("an empty body", ""),
			Entry("an unclosed root", "<ServerSideEncryptionConfiguration>"),
			Entry("another root", "<Tagging/>"),
			Entry("an SSEAlgorithm outside AWS's list, which Validate refuses", strings.Replace(aesEncryptionDoc, "AES256", "AES-256", 1)),
			Entry("a KMSMasterKeyID beside AES256, which Validate refuses",
				strings.Replace(aesEncryptionDoc, "</SSEAlgorithm>", "</SSEAlgorithm><KMSMasterKeyID>k</KMSMasterKeyID>", 1)),
		)
		DescribeTable("replaces the stored attr whole without decoding it",
			func(ctx SpecContext, stored []byte) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: stored})
				Expect(op.Run(ctx, &op.PutBucketEncryption{}, putReq(kmsEncryptionDoc))).To(Succeed())
				Expect(bucket(ctx).Attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encryptionAttr(kmsEncryption())))
			},
			Entry("a rule stored at (2,1)", blockedRuleAttr()),
			Entry("an attr that does not decode", []byte{0xff}),
		)
	})

	Describe("DeleteBucketEncryption", func() {
		It("removes the policy and key id attrs and nothing else, after which GetBucketEncryption finds none", func(ctx SpecContext) {
			f.setBucketAttrs(ctx, map[string][]byte{tags.Attr: teamTag("a")})
			before := bucket(ctx).Attrs
			f.setBucketAttrs(ctx, map[string][]byte{
				op.AttrBucketEncryption: encryptionAttr(kmsEncryption()), op.AttrBucketEncryptionKeyID: []byte("kek"),
			})
			Expect(op.Run(ctx, &op.DeleteBucketEncryption{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			Expect(bucket(ctx).Attrs).To(Equal(before), "rgw_op.cc:8860-8866 at v19.2.7")
			Expect(op.Run(ctx, &op.GetBucketEncryption{}, f.req(http.MethodGet, "plain", ""))).To(MatchError(op.ErrNoSuchBucketEncryption))
		})
		DescribeTable("removes a stored attr without decoding it",
			func(ctx SpecContext, stored []byte) {
				f.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: stored})
				Expect(op.Run(ctx, &op.DeleteBucketEncryption{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
				Expect(bucket(ctx).Attrs).NotTo(HaveKey(op.AttrBucketEncryption))
			},
			Entry("a rule stored at (2,1)", blockedRuleAttr()),
			Entry("an attr that does not decode", []byte{0xff}),
		)
		It("succeeds on a bucket without either attr, rewriting the instance as radosgw does", func(ctx SpecContext) {
			before := bucket(ctx)
			Expect(op.Run(ctx, &op.DeleteBucketEncryption{}, f.req(http.MethodDelete, "plain", ""))).To(Succeed())
			after := bucket(ctx)
			Expect(after.Version.Ver).To(Equal(before.Version.Ver+1), "no presence check, rgw_op.cc:8851-8867 at v19.2.7")
			Expect(after.Attrs).To(Equal(before.Attrs))
		})
	})

	Describe("an encryption write that loses a race", func() {
		It("is retried fifteen times, reading the bucket again before each, then answers ConcurrentModification", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			buckets.GetBucketStub = f.store.GetBucket
			buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
			f.env.Buckets = buckets
			Expect(op.Run(ctx, &op.PutBucketEncryption{}, putReq(kmsEncryptionDoc))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(16), "rgw_op.h:189-195 at v19.2.7")
			Expect(buckets.GetBucketCallCount()).To(Equal(16), "the load, then one read before each retry")
			_, _, set, rm := buckets.PutBucketAttrsArgsForCall(15)
			Expect(set).To(Equal(map[string][]byte{op.AttrBucketEncryption: encryptionAttr(kmsEncryption())}), "the body read once, its configuration alone on each try")
			Expect(rm).To(BeEmpty())
			Expect(op.Run(ctx, &op.DeleteBucketEncryption{}, f.req(http.MethodDelete, "plain", ""))).To(MatchError(op.ErrConcurrentModification))
			Expect(buckets.PutBucketAttrsCallCount()).To(Equal(32))
			_, _, set, rm = buckets.PutBucketAttrsArgsForCall(31)
			Expect(set).To(BeEmpty())
			Expect(rm).To(ConsistOf(op.AttrBucketEncryption, op.AttrBucketEncryptionKeyID))
		})
		DescribeTable("stops without writing when the name now names another bucket",
			func(ctx SpecContext, mk func() op.Op, req func() *op.Request) {
				other := bucket(ctx)
				other.Info.Bucket.ID = "another-instance"
				buckets := &opfakes.FakeBucketStore{}
				buckets.GetBucketStub = func(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
					if buckets.GetBucketCallCount() == 1 {
						return f.store.GetBucket(ctx, tenant, name)
					}
					return other, nil
				}
				buckets.PutBucketAttrsReturns(op.ErrConcurrentModification)
				f.env.Buckets = buckets
				before := bucket(ctx)
				Expect(op.Run(ctx, mk(), req())).To(MatchError(op.ErrConcurrentModification))
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "radosgw writes into the bucket re-created under the name, rgw_op.cc:8807-8811 at v19.2.7")
				Expect(bucket(ctx)).To(Equal(before))
			},
			Entry("PutBucketEncryption", func() op.Op { return &op.PutBucketEncryption{} }, func() *op.Request { return putReq(kmsEncryptionDoc) }),
			Entry("DeleteBucketEncryption, rgw_op.cc:8860-8866 at v19.2.7", func() op.Op { return &op.DeleteBucketEncryption{} },
				func() *op.Request { return f.req(http.MethodDelete, "plain", "") }),
		)

		Describe("to another write of the bucket", func() {
			// bobCanWriteACP is alice's ACL with WRITE_ACP, the permission
			// both encryption writes map to, granted to bob.
			bobCanWriteACP := func() []byte {
				p := acl.DefaultPolicy(f.alice.Owner, "Alice")
				p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: acl.PermWriteACP})
				return encodedPolicy(p)
			}
			revokeByACL := func() map[string][]byte { return map[string][]byte{meta.AttrACL: f.aliceACL} }
			denyBobOn := func(actions ...string) func() map[string][]byte {
				return func() map[string][]byte {
					names, err := json.Marshal(actions)
					Expect(err).NotTo(HaveOccurred())
					return map[string][]byte{op.AttrIAMPolicy: fmt.Appendf(nil,
						`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/bob"]},"Action":%s,"Resource":"arn:aws:s3:::plain"}]}`,
						names)}
				}
			}
			// retagged is another request's change of an attr the bucket
			// already holds.
			retagged := func() map[string][]byte { return map[string][]byte{tags.Attr: teamTag("b")} }

			var (
				buckets *opfakes.FakeBucketStore
				// raced is the bucket's attrs as the write the first try
				// lost to left them.
				raced map[string][]byte
			)
			// race gives plain a stored AES256 default, a key id, the tag set
			// team=a and bob WRITE_ACP, then makes the first attr write lose
			// to concurrent, another request's change of the bucket's attrs,
			// and lets every retry through to the store.
			race := func(ctx context.Context, concurrent map[string][]byte) {
				GinkgoHelper()
				f.env.Authz = authz.New(authz.DefaultConfig(denc.Squid))
				f.setBucketAttrs(ctx, map[string][]byte{
					meta.AttrACL: bobCanWriteACP(), op.AttrBucketEncryption: encryptionAttr(aesEncryption()),
					op.AttrBucketEncryptionKeyID: []byte("kek"), tags.Attr: teamTag("a"),
				})
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
			// write is an encryption write as bob asks for it, and what it
			// leaves in the bucket's attrs.
			type write struct {
				op    op.Op
				req   *op.Request
				check func(attrs map[string][]byte)
			}
			put := func() write {
				return write{
					op: &op.PutBucketEncryption{}, req: asBob(putReq(kmsEncryptionDoc)),
					check: func(attrs map[string][]byte) {
						Expect(attrs).To(HaveKeyWithValue(op.AttrBucketEncryption, encryptionAttr(kmsEncryption())))
						Expect(attrs).To(HaveKeyWithValue(op.AttrBucketEncryptionKeyID, []byte("kek")))
					},
				}
			}
			del := func() write {
				return write{
					op: &op.DeleteBucketEncryption{}, req: asBob(f.req(http.MethodDelete, "plain", "")),
					check: func(attrs map[string][]byte) {
						Expect(attrs).NotTo(HaveKey(op.AttrBucketEncryption))
						Expect(attrs).NotTo(HaveKey(op.AttrBucketEncryptionKeyID))
					},
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
					Expect(attrs).To(HaveKeyWithValue(tags.Attr, teamTag("b")), "s->bucket->get_attrs() inside the retry, rgw_op.cc:8808 and :8861 at v19.2.7")
				},
				Entry("PutBucketEncryption", put),
				Entry("DeleteBucketEncryption", del),
			)
			DescribeTable("refuses the retry once the write it lost to revoked the requester's permission, leaving the revoking bucket",
				func(ctx SpecContext, mk func() write, revoke func() map[string][]byte) {
					race(ctx, revoke())
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(MatchError(op.ErrAccessDenied))
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(1), "the retry writes nothing")
					Expect(bucket(ctx).Attrs).To(Equal(raced))
				},
				Entry("PutBucketEncryption under an ACL that drops the grant", put, revokeByACL),
				Entry("PutBucketEncryption under a policy that denies s3:PutBucketEncryption alone", put, denyBobOn("s3:PutBucketEncryption")),
				Entry("DeleteBucketEncryption under an ACL that drops the grant", del, revokeByACL),
				Entry("DeleteBucketEncryption under a policy that denies s3:PutBucketEncryption alone, the action it authorizes",
					del, denyBobOn("s3:PutBucketEncryption")),
			)
			DescribeTable("writes on a retry whose requester still holds the permission",
				func(ctx SpecContext, mk func() write) {
					race(ctx, denyBobOn("s3:GetBucketEncryption", "s3:PutBucketPolicy", "s3:PutBucketTagging")())
					w := mk()
					Expect(op.Run(ctx, w.op, w.req)).To(Succeed())
					Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
					w.check(bucket(ctx).Attrs)
				},
				Entry("PutBucketEncryption", put),
				Entry("DeleteBucketEncryption", del),
			)
			It("lets an admin through a retry's refusal, as Run lets one through the first check", func(ctx SpecContext) {
				race(ctx, revokeByACL())
				w := put()
				w.req.Identity.Admin = true
				Expect(op.Run(ctx, w.op, w.req)).To(Succeed())
				Expect(buckets.PutBucketAttrsCallCount()).To(Equal(2))
				w.check(bucket(ctx).Attrs)
			})
		})
	})
})
