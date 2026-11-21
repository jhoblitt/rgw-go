package s3_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// A Deny on s3:x-amz-content-sha256 reads the payload hash as the SigV4
// verifier acts on it: UNSIGNED-PAYLOAD for every payload it reads with no
// signature, presigned and aws-chunked ones included, and beside it absent
// for a presigned upload and as sent for an unsigned form other than
// UNSIGNED-PAYLOAD (docs/differences.md, "A Deny reads the condition keys
// Amazon S3 sets").
var _ = Describe("a Deny of unsigned payloads behind the SigV4 verifier", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			const body = "hello"
			var (
				h     *s3.Handler
				store *memstore.Store
				creds aws.Credentials
			)
			newSigner := func() *v4.Signer {
				return v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
			}
			serve := func(req *http.Request) int {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				return rec.Code
			}

			BeforeEach(func(ctx SpecContext) {
				creds = aws.Credentials{AccessKeyID: "AKALICE", SecretAccessKey: "secret-alice"}
				store = memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return unverifiedNow }})
				u := meta.NewUserInfo()
				u.UserID = meta.UserID{ID: "alice"}
				u.OpMask = op.OpTypeAll
				u.AccessKeys = map[string]meta.AccessKey{"AKALICE": {ID: "AKALICE", Secret: "secret-alice", Active: true}}
				store.AddUser(u)
				owner := meta.UserOwner(u.UserID)
				deny := `{"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObject",` +
					` "Resource": "arn:aws:s3:::plain/*", "Condition": {"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}}]}`
				_, err := store.CreateBucket(ctx, op.CreateBucketParams{
					Name: "plain", Owner: owner, Placement: meta.PlacementRule{Name: "default-placement"},
					Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "")), op.AttrIAMPolicy: []byte(deny)},
				})
				Expect(err).NotTo(HaveOccurred())
				env := testEnv(store)
				env.Authz = authz.New(authz.DefaultConfig(rel))
				cfg := auth.DefaultConfig()
				cfg.Now = func() time.Time { return unverifiedNow }
				h = s3.NewHandler(env, auth.New(cfg, store, store), testConfig(s3.Config{}))
			})

			// put is alice's PutObject of body to plain/k, signed in its
			// Authorization header with hash as x-amz-content-sha256.
			put := func(ctx SpecContext, hash string) int {
				GinkgoHelper()
				req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/plain/k", strings.NewReader(body))
				req.Header.Set("Content-Length", strconv.Itoa(len(body)))
				req.Header.Set("X-Amz-Content-Sha256", hash)
				Expect(newSigner().SignHTTP(ctx, creds, req, hash, "s3", "us-east-1", unverifiedNow)).To(Succeed())
				return serve(req)
			}

			DescribeTable("refuses every upload whose body the verifier leaves unverified",
				func(ctx SpecContext, hash string, want int) {
					Expect(put(ctx, hash)).To(Equal(want))
				},
				Entry("UNSIGNED-PAYLOAD", "UNSIGNED-PAYLOAD", http.StatusForbidden),
				Entry("a prefix before it", "XUNSIGNED-PAYLOAD", http.StatusForbidden),
				Entry("a suffix after it", "UNSIGNED-PAYLOAD-X", http.StatusForbidden),
				Entry("it twice in one value", "UNSIGNED-PAYLOAD,UNSIGNED-PAYLOAD", http.StatusForbidden),
				Entry("the body's hash, which the verifier checks", func() string {
					sum := sha256.Sum256([]byte(body))
					return hex.EncodeToString(sum[:])
				}(), http.StatusOK),
				Entry("a lower-case spelling, which the verifier checks as the body's hash", "unsigned-payload", http.StatusBadRequest),
			)

			// unsignedChunked is alice's aws-chunked PutObject of body whose
			// chunks carry no signature, STREAMING-UNSIGNED-PAYLOAD-TRAILER.
			unsignedChunked := func(ctx SpecContext) *http.Request {
				GinkgoHelper()
				const form = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
				chunked := "5\r\nhello\r\n0\r\n\r\n"
				req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/plain/k", strings.NewReader(chunked))
				req.Header.Set("Content-Length", strconv.Itoa(len(chunked)))
				req.Header.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(body)))
				req.Header.Set("X-Amz-Content-Sha256", form)
				Expect(newSigner().SignHTTP(ctx, creds, req, form, "s3", "us-east-1", unverifiedNow)).To(Succeed())
				return req
			}
			// signedChunked is alice's aws-chunked PutObject of body whose
			// chunks it signs, STREAMING-AWS4-HMAC-SHA256-PAYLOAD.
			signedChunked := func(ctx SpecContext) *http.Request {
				GinkgoHelper()
				const form = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
				chunks := [][]byte{[]byte(body), nil}
				encoded := 0
				for _, c := range chunks {
					encoded += len(fmt.Sprintf("%x;chunk-signature=%064d\r\n%s\r\n", len(c), 0, c))
				}
				req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/plain/k", nil)
				req.ContentLength = int64(encoded)
				req.Header.Set("Content-Length", strconv.Itoa(encoded))
				req.Header.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(body)))
				req.Header.Set("X-Amz-Content-Sha256", form)
				Expect(newSigner().SignHTTP(ctx, creds, req, form, "s3", "us-east-1", unverifiedNow)).To(Succeed())
				_, sig, ok := strings.Cut(req.Header.Get("Authorization"), "Signature=")
				Expect(ok).To(BeTrue())
				seed, err := hex.DecodeString(sig)
				Expect(err).NotTo(HaveOccurred())
				stream := v4.NewStreamSigner(creds, "s3", "us-east-1", seed)
				var b bytes.Buffer
				for _, c := range chunks {
					chunkSig, err := stream.GetSignature(ctx, nil, c, unverifiedNow)
					Expect(err).NotTo(HaveOccurred())
					fmt.Fprintf(&b, "%x;chunk-signature=%x\r\n%s\r\n", len(c), chunkSig, c)
				}
				Expect(b.Len()).To(Equal(encoded))
				req.Body = io.NopCloser(&b)
				return req
			}
			// presigned is alice's presigned PutObject of body, which sends no
			// x-amz-content-sha256.
			presigned := func(ctx SpecContext) *http.Request {
				GinkgoHelper()
				unsigned := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/plain/k?X-Amz-Expires=300", nil)
				u, _, err := newSigner().PresignHTTP(ctx, creds, unsigned, "UNSIGNED-PAYLOAD", "s3", "us-east-1", unverifiedNow)
				Expect(err).NotTo(HaveOccurred())
				parsed, err := url.Parse(u)
				Expect(err).NotTo(HaveOccurred())
				req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com"+parsed.RequestURI(), strings.NewReader(body))
				req.Header.Set("Content-Length", strconv.Itoa(len(body)))
				return req
			}
			// setDeny replaces plain's policy with a Deny of s3:PutObject under
			// cond, or removes it when cond is empty.
			setDeny := func(ctx SpecContext, cond string) {
				GinkgoHelper()
				rec, err := store.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				if cond == "" {
					Expect(store.PutBucketAttrs(ctx, rec, nil, []string{op.AttrIAMPolicy})).To(Succeed())
					return
				}
				deny := `{"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObject",` +
					` "Resource": "arn:aws:s3:::plain/*", "Condition": ` + cond + `}]}`
				Expect(store.PutBucketAttrs(ctx, rec, map[string][]byte{op.AttrIAMPolicy: []byte(deny)}, nil)).To(Succeed())
			}

			// A presigned upload's payload hash reads as UNSIGNED-PAYLOAD or
			// absent, and an unsigned aws-chunked upload's as UNSIGNED-PAYLOAD
			// or as sent; the Deny refuses where it holds on any reading.
			DescribeTable("refuses an upload where the Deny holds on any reading of its payload hash",
				func(ctx SpecContext, cond string, onPresigned, onUnsignedChunked, onSignedChunked int) {
					setDeny(ctx, cond)
					Expect(serve(presigned(ctx))).To(Equal(onPresigned), "a presigned upload")
					Expect(serve(unsignedChunked(ctx))).To(Equal(onUnsignedChunked), "an aws-chunked upload with unsigned chunks")
					Expect(serve(signedChunked(ctx))).To(Equal(onSignedChunked), "an aws-chunked upload with signed chunks")
				},
				Entry("a plain operator on UNSIGNED-PAYLOAD",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					http.StatusForbidden, http.StatusForbidden, http.StatusOK),
				Entry("a negated operator on UNSIGNED-PAYLOAD, which refuses every signed payload",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					http.StatusForbidden, http.StatusForbidden, http.StatusForbidden),
				Entry("a plain operator on the unsigned aws-chunked form",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`,
					http.StatusOK, http.StatusForbidden, http.StatusOK),
				Entry("a pattern on every aws-chunked form",
					`{"StringLike": {"s3:x-amz-content-sha256": "STREAMING-*"}}`,
					http.StatusOK, http.StatusForbidden, http.StatusForbidden),
				Entry("no Deny", "", http.StatusOK, http.StatusOK, http.StatusOK),
			)
		})
	}
})
