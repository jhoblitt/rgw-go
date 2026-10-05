package auth_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ s3.Authenticator = (*auth.Verifier)(nil)

const (
	helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")

	streamingSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"

	// helloWorldTrailerBody is "hello world" as one unsigned aws-chunked
	// chunk with its CRC32 trailer.
	helloWorldTrailerBody = "b\r\nhello world\r\n0\r\nx-amz-checksum-crc32:DUoRhQ==\r\n\r\n"
)

// newBodyRequest is a server-side request carrying body. The SDK signs
// content-length from req.ContentLength, and net/http's server keeps the
// Content-Length header, which httptest leaves out, so it is set here.
func newBodyRequest(ctx context.Context, method, target, body string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return req
}

// withPayloadHash sets x-amz-content-sha256, which signWith then signs over.
func withPayloadHash(req *http.Request, hash string) *http.Request {
	req.Header.Set("X-Amz-Content-Sha256", hash)
	return req
}

// signatureOf is the hex signature in req's SigV4 Authorization header.
func signatureOf(req *http.Request) string {
	_, sig, ok := strings.Cut(req.Header.Get("Authorization"), "Signature=")
	Expect(ok).To(BeTrue(), "Authorization %q", req.Header.Get("Authorization"))
	return sig
}

// signedChunkedPut is a PUT of data to target as aws-chunked chunks of at
// most size bytes, signed as id at t: the request signature seeds the chain
// of chunk signatures, which the reference StreamSigner computes, since its
// event-stream string to sign with empty headers is the S3 chunk's. newReq
// builds the request without a body; the body is attached once the request
// signature is known.
func signedChunkedPut(ctx context.Context, newReq func() *http.Request, id string, data []byte, size int, t time.Time) *http.Request {
	chunks := slices.Collect(slices.Chunk(data, size))
	chunks = append(chunks, nil)
	encoded := 0
	for _, c := range chunks {
		encoded += len(fmt.Sprintf("%x;chunk-signature=%064d\r\n%s\r\n", len(c), 0, c))
	}
	req := newReq()
	req.ContentLength = int64(encoded)
	req.Header.Set("Content-Length", strconv.Itoa(encoded))
	req.Header.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(data)))
	signedAt(ctx, withPayloadHash(req, streamingSigned), id, t)
	seed, err := hex.DecodeString(signatureOf(req))
	Expect(err).NotTo(HaveOccurred())
	signer := v4.NewStreamSigner(aws.Credentials{AccessKeyID: accessKeyOf(id), SecretAccessKey: secretOf(id)}, "s3", "us-east-1", seed)
	var stream bytes.Buffer
	for _, c := range chunks {
		sig, err := signer.GetSignature(ctx, nil, c, t)
		Expect(err).NotTo(HaveOccurred())
		fmt.Fprintf(&stream, "%x;chunk-signature=%x\r\n%s\r\n", len(c), sig, c)
	}
	Expect(stream.Len()).To(Equal(encoded))
	req.Body = io.NopCloser(&stream)
	return req
}

var _ = Describe("Verifier", func() {
	var (
		store *memstore.Store
		cfg   auth.Config
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", nil))
		cfg = auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	It("authenticates an unsigned request as anonymous, with an unsigned payload", func(ctx SpecContext) {
		req := newBodyRequest(ctx, http.MethodPut, "http://s3.example.com/b/k", "hello")
		res, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity).To(Equal(op.Anonymous()))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		Expect(res.Body).To(BeNil())
		Expect(res.ContentLength).To(BeEquivalentTo(5))
		Expect(res.Presigned).To(BeFalse())
	})

	It("authenticates an OPTIONS request without a known signature scheme as anonymous", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodOptions, "http://s3.example.com/b/k", nil)
		req.Header.Set("Authorization", "Bearer abc")
		res, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeTrue())
	})

	It("treats presigned parameters not spelled X-Amz- as an unsigned request, as radosgw does", func(ctx SpecContext) {
		req := newGet(ctx, "http://s3.example.com/?X-AMZ-Algorithm=AWS4-HMAC-SHA256&X-AMZ-Credential=AKALICE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-AMZ-Signature=00")
		res, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeTrue())
	})

	DescribeTable("maps radosgw's engine errors",
		func(ctx SpecContext, mutate func(*http.Request), want error) {
			req := signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice")
			mutate(req)
			_, err := v.Authenticate(ctx, req, anyPayload)
			Expect(err).To(MatchError(want))
		},
		Entry("wrong signature", func(r *http.Request) {
			a := r.Header.Get("Authorization")
			r.Header.Set("Authorization", a[:len(a)-1]+map[bool]string{true: "0", false: "1"}[a[len(a)-1] != '0'])
		}, op.ErrSignatureDoesNotMatch),
		Entry("unknown scheme", func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc") }, op.ErrInvalidArgument),
		Entry("v2 header without a colon", func(r *http.Request) { r.Header.Set("Authorization", "AWS junk") }, op.ErrInvalidArgument),
		Entry("v4 credential with an empty access key", func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Credential=AKALICE/", "Credential=/", 1))
		}, op.ErrInvalidArgument),
		Entry("v4 header with an empty signature", func(r *http.Request) {
			a, _, _ := strings.Cut(r.Header.Get("Authorization"), "Signature=")
			r.Header.Set("Authorization", a+"Signature=")
		}, op.ErrInvalidArgument),
		Entry("skewed", func(r *http.Request) { r.Header.Set("X-Amz-Date", "20150830T100000Z") }, op.ErrRequestTimeTooSkewed),
		Entry("unsigned x-amz header", func(r *http.Request) { r.Header.Set("X-Amz-Meta-Late", "x") }, op.ErrAccessDenied),
	)

	It("denies everything without a backend, and every signed request when presigned URLs are disabled", func(ctx SpecContext) {
		off := cfg
		off.UseRados = false
		none := auth.New(off, store, store)
		_, err := none.Authenticate(ctx, newGet(ctx, "http://s3.example.com/"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		_, err = none.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))

		noPresign := cfg
		noPresign.DisablePresignedURLs = true
		strict := auth.New(noPresign, store, store)
		for _, req := range []*http.Request{
			signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice"),
			newGet(ctx, "http://s3.example.com/?AWSAccessKeyId=AKALICE&Expires=1441000000&Signature=x"),
			func() *http.Request {
				r := newGet(ctx, "http://s3.example.com/")
				r.Header.Set("Authorization", "Bearer abc")
				return r
			}(),
		} {
			_, err = strict.Authenticate(ctx, req, anyPayload)
			opErr, ok := errors.AsType[*op.Error](err)
			Expect(ok).To(BeTrue(), "%s %s: %v", req.Header.Get("Authorization"), req.URL.RawQuery, err)
			Expect(opErr.Code).To(Equal("AccessDenied"))
			Expect(opErr.Message).To(Equal("Presigned URLs are disabled by admin"))
		}
		res, err := strict.Authenticate(ctx, newGet(ctx, "http://s3.example.com/"), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeTrue())
	})

	It("passes the expired presigned URL's message through", func(ctx SpecContext) {
		req := newGet(ctx, "http://s3.example.com/?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKALICE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20150830T120000Z&X-Amz-Expires=1&X-Amz-SignedHeaders=host&X-Amz-Signature=00")
		_, err := v.Authenticate(ctx, req, anyPayload)
		opErr, ok := errors.AsType[*op.Error](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(opErr.Code).To(Equal("AccessDenied"))
		Expect(opErr.Message).To(Equal("The pre-signed URL has expired"))
	})

	It("verifies a SigV2 header and a SigV2 presigned request", func(ctx SpecContext) {
		req := newGet(ctx, "http://s3.example.com/b/k")
		req.Header.Set("Date", "Sun, 30 Aug 2015 12:36:00 GMT")
		h := hmac.New(sha1.New, []byte("secret-alice"))
		h.Write([]byte("GET\n\n\nSun, 30 Aug 2015 12:36:00 GMT\n/b/k"))
		req.Header.Set("Authorization", "AWS AKALICE:"+base64.StdEncoding.EncodeToString(h.Sum(nil)))
		res, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.Presigned).To(BeFalse())
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		Expect(res.Body).To(BeNil())

		h = hmac.New(sha1.New, []byte("secret-alice"))
		h.Write([]byte("GET\n\n\n1441000000\n/b/k"))
		pre := newGet(ctx, "http://s3.example.com/b/k?AWSAccessKeyId=AKALICE&Expires=1441000000&Signature="+url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil))))
		res, err = v.Authenticate(ctx, pre, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.Presigned).To(BeTrue())

		h = hmac.New(sha1.New, []byte("not-the-secret"))
		h.Write([]byte("GET\n\n\n1441000000\n/b/k"))
		bad := newGet(ctx, "http://s3.example.com/b/k?AWSAccessKeyId=AKALICE&Expires=1441000000&Signature="+url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil))))
		_, err = v.Authenticate(ctx, bad, anyPayload)
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
	})

	It("answers a SigV2 request InvalidArgument when its key's secret is empty, as get_v2_signature refuses one", func(ctx SpecContext) {
		store.AddUser(user("ivan", func(u *meta.UserInfo) {
			k := u.AccessKeys["AKIVAN"]
			k.Secret = ""
			u.AccessKeys["AKIVAN"] = k
		}))
		req := newGet(ctx, "http://s3.example.com/b/k")
		req.Header.Set("Date", "Sun, 30 Aug 2015 12:36:00 GMT")
		req.Header.Set("Authorization", "AWS AKIVAN:c2lnbmF0dXJl")
		_, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).To(MatchError(op.ErrInvalidArgument))
	})

	It("authenticates the request go-ceph's admin client sends", func(ctx SpecContext) {
		req := newGet(ctx, "http://s3.example.com/admin/user?uid=alice&format=json")
		creds := aws.Credentials{AccessKeyID: "AKALICE", SecretAccessKey: "secret-alice"}
		Expect(v4.NewSigner().SignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "default", time.Now())).To(Succeed())
		Expect(req.Header).NotTo(HaveKey("X-Amz-Content-Sha256"), "go-ceph's SignHTTP call never sends it")
		live := cfg
		live.Now = nil
		res, err := auth.New(live, store, store).Authenticate(ctx, req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
	})

	// The key is unknown in every entry: a refused form is answered before
	// the lookup, as get_auth_data_v4 throws before LocalEngine runs
	// (rgw_rest_s3.cc:5899-5969 and :6325-6329 at v19.2.6, :6466-6540 and
	// :6896-6900 at v20.2.4); an accepted one reaches it.
	DescribeTable("refuses a payload form the dispatched op does not take, before the access key is looked up",
		func(ctx SpecContext, hash, body string, forms op.PayloadForms, want error) {
			req := withPayloadHash(newBodyRequest(ctx, http.MethodPut, "http://s3.example.com/plain/k", body), hash)
			if strings.HasPrefix(hash, "STREAMING-") {
				req.Header.Set("X-Amz-Decoded-Content-Length", "5")
			}
			_, err := v.Authenticate(ctx, signWith(ctx, req, "AKNOBODY", "x", fixedNow()), forms)
			Expect(err).To(MatchError(want))
		},
		Entry("a signed single chunk on an op that takes none", helloSHA256, "hello", op.PayloadForms(0), op.ErrNotImplemented),
		Entry("a signed single chunk on an op in the whitelist", helloSHA256, "hello", op.PayloadSigned, op.ErrInvalidAccessKeyID),
		Entry("aws-chunked on an op that takes a single chunk only", streamingSigned,
			"5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n", op.PayloadSigned, op.ErrNotImplemented),
		Entry("unsigned aws-chunked with a trailer on an op that takes a single chunk only", streamingUnsignedTrailer,
			"5\r\nhello\r\n0\r\n\r\n", op.PayloadSigned, op.ErrNotImplemented),
		Entry("aws-chunked on put_obj", streamingSigned,
			"5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n", anyPayload, op.ErrInvalidAccessKeyID),
		Entry("UNSIGNED-PAYLOAD with a body needs no completer", "UNSIGNED-PAYLOAD", "hello", op.PayloadForms(0), op.ErrInvalidAccessKeyID),
		Entry("the empty hash on an empty body needs no completer", emptySHA256, "", op.PayloadForms(0), op.ErrInvalidAccessKeyID),
	)

	Describe("the body it hands the op", func() {
		unsignedPut := func(ctx context.Context, hash, body string) *http.Request {
			return withPayloadHash(newBodyRequest(ctx, http.MethodPut, "http://s3.example.com/b/k", body), hash)
		}
		put := func(ctx context.Context, hash, body string) *http.Request {
			return signedAs(ctx, unsignedPut(ctx, hash, body), "alice")
		}

		It("verifies a single chunk as it is read, with the request's length", func(ctx SpecContext) {
			res, err := v.Authenticate(ctx, put(ctx, helloSHA256, "hello"), anyPayload)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.PayloadSHA256).To(Equal(helloSHA256))
			Expect(res.Body).NotTo(BeNil())
			Expect(res.ContentLength).To(BeEquivalentTo(5))
			Expect(io.ReadAll(res.Body)).To(Equal([]byte("hello")))

			res, err = v.Authenticate(ctx, put(ctx, helloSHA256, "jello"), anyPayload)
			Expect(err).NotTo(HaveOccurred(), "the signature covers the hash, not the body")
			_, err = io.ReadAll(res.Body)
			Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
		})

		It("decodes an aws-chunked payload, with its decoded length", func(ctx SpecContext) {
			req := unsignedPut(ctx, streamingUnsignedTrailer, helloWorldTrailerBody)
			req.Header.Set("X-Amz-Decoded-Content-Length", "11")
			res, err := v.Authenticate(ctx, signedAs(ctx, req, "alice"), anyPayload)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.PayloadSHA256).To(Equal(streamingUnsignedTrailer))
			Expect(res.ContentLength).To(BeEquivalentTo(11))
			Expect(io.ReadAll(res.Body)).To(Equal([]byte("hello world")))

			signed := signedChunkedPut(ctx, func() *http.Request {
				return httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/b/k", nil)
			}, "alice", []byte("hello world"), 4, fixedNow())
			res, err = v.Authenticate(ctx, signed, anyPayload)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.ContentLength).To(BeEquivalentTo(11))
			Expect(io.ReadAll(res.Body)).To(Equal([]byte("hello world")))
		})

		It("hands over no body for an unsigned payload", func(ctx SpecContext) {
			res, err := v.Authenticate(ctx, put(ctx, "UNSIGNED-PAYLOAD", "hello"), anyPayload)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Body).To(BeNil())
			Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		})

		It("reports a missing x-amz-decoded-content-length only once the signature has verified", func(ctx SpecContext) {
			_, err := v.Authenticate(ctx, put(ctx, streamingUnsignedTrailer, helloWorldTrailerBody), anyPayload)
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			req := unsignedPut(ctx, streamingUnsignedTrailer, helloWorldTrailerBody)
			_, err = v.Authenticate(ctx, signWith(ctx, req, "AKALICE", "not-the-secret", fixedNow()), anyPayload)
			Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		})
	})
})

// opView is what a registered route saw of its request.
type opView struct {
	body          string
	contentLength int64
	payloadHash   string
	err           error
}

var _ = Describe("Verifier through the s3 handler", func() {
	var (
		store *memstore.Store
		h     *s3.Handler
		srv   *httptest.Server
		seen  chan opView
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid})
		store.AddUser(user("alice", nil))
		store.AddUser(user("sue", func(u *meta.UserInfo) { u.Suspended = 1 }))
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		h = s3.NewHandler(env, auth.New(auth.DefaultConfig(), store, store), s3.Config{})
		seen = make(chan opView, 4)
		// record reads the body to its end, where the verdict arrives, and
		// returns the verdict as the op would.
		record := func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			body, err := io.ReadAll(r.Body)
			seen <- opView{body: string(body), contentLength: r.ContentLength, payloadHash: r.Header.Get("X-Amz-Content-Sha256"), err: err}
			if err != nil {
				return err
			}
			w.WriteHeader(http.StatusOK)
			return nil
		}
		h.Register("put_obj", record)
		h.Register("multi_object_delete", record)
		srv = httptest.NewTLSServer(h)
		DeferCleanup(srv.Close)
	})

	// client is the SDK's S3 client as config.LoadDefaultConfig configures
	// it, which computes a request checksum whenever the operation supports
	// one; over TLS it sends that checksum as an aws-chunked trailer.
	client := func(secret string) *awss3.Client {
		return awss3.New(awss3.Options{
			Region:                     "us-east-1",
			BaseEndpoint:               aws.String(srv.URL),
			UsePathStyle:               true,
			Credentials:                credentials.NewStaticCredentialsProvider("AKALICE", secret, ""),
			HTTPClient:                 srv.Client(),
			RetryMaxAttempts:           1,
			RequestChecksumCalculation: aws.RequestChecksumCalculationWhenSupported,
		})
	}
	newClientRequest := func(ctx context.Context, method, path, body string) *http.Request {
		req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		return req
	}
	send := func(req *http.Request) (int, string) {
		resp, err := srv.Client().Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(resp.Body.Close()).To(Succeed()) }()
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, string(body)
	}
	serve := func(req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	It("serves ListBuckets to the reference client and refuses a wrong secret with radosgw's error document", func(ctx SpecContext) {
		out, err := client("secret-alice").ListBuckets(ctx, &awss3.ListBucketsInput{})
		Expect(err).NotTo(HaveOccurred())
		Expect(aws.ToString(out.Owner.ID)).To(Equal("alice"))

		_, err = client("wrong").ListBuckets(ctx, &awss3.ListBucketsInput{})
		apiErr, ok := errors.AsType[smithy.APIError](err)
		Expect(ok).To(BeTrue(), "%v", err)
		Expect(apiErr.ErrorCode()).To(Equal("SignatureDoesNotMatch"))
	})

	It("decodes the aws-chunked trailer upload the reference client sends and hands the op the decoded length", func(ctx SpecContext) {
		_, err := client("secret-alice").PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("plain"), Key: aws.String("k"), Body: strings.NewReader("hello world"), ContentLength: aws.Int64(11)})
		Expect(err).NotTo(HaveOccurred())
		var got opView
		Expect(seen).To(Receive(&got))
		Expect(got).To(Equal(opView{body: "hello world", contentLength: 11, payloadHash: streamingUnsignedTrailer}))

		req := newClientRequest(ctx, http.MethodPut, "/plain/k2", helloWorldTrailerBody)
		req.Header.Set("X-Amz-Decoded-Content-Length", "11")
		req.Header.Set("X-Amz-Trailer", "x-amz-checksum-crc32")
		code, body := send(signedAt(ctx, withPayloadHash(req, streamingUnsignedTrailer), "alice", time.Now()))
		Expect(code).To(Equal(http.StatusOK), body)
		Expect(seen).To(Receive(&got))
		Expect(got).To(Equal(opView{body: "hello world", contentLength: 11, payloadHash: streamingUnsignedTrailer}))
	})

	// One op of each payload form: what the op reads is the verifying body,
	// and its verdict arrives with the read that ends the payload.
	It("hands a signed aws-chunked payload's verdict to the op that reads it", func(ctx SpecContext) {
		newReq := func() *http.Request { return newClientRequest(ctx, http.MethodPut, "/plain/k", "") }
		code, body := send(signedChunkedPut(ctx, newReq, "alice", []byte("hello world"), 4, time.Now()))
		Expect(code).To(Equal(http.StatusOK), body)
		var got opView
		Expect(seen).To(Receive(&got))
		Expect(got).To(Equal(opView{body: "hello world", contentLength: 11, payloadHash: streamingSigned}))

		req := signedChunkedPut(ctx, newReq, "alice", []byte("hello world"), 4, time.Now())
		stream, err := io.ReadAll(req.Body)
		Expect(err).NotTo(HaveOccurred())
		// The first chunk's signature: the chunk after it is pending, so the
		// mismatch is found mid-payload.
		i := bytes.Index(stream, []byte(";chunk-signature=")) + len(";chunk-signature=")
		stream[i] = map[bool]byte{true: '1', false: '0'}[stream[i] == '0']
		req.Body = io.NopCloser(bytes.NewReader(stream))
		code, body = send(req)
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>SignatureDoesNotMatch</Code>"))
		Expect(seen).To(Receive(&got))
		Expect(got.err).To(MatchError(op.ErrSignatureDoesNotMatch))
	})

	It("hands a signed single chunk's verdict to the op that reads it", func(ctx SpecContext) {
		const del = `<Delete><Object><Key>k</Key></Object></Delete>`
		digest := sha256.Sum256([]byte(del))
		sum := hex.EncodeToString(digest[:])
		code, body := send(signedAt(ctx, withPayloadHash(newClientRequest(ctx, http.MethodPost, "/plain?delete", del), sum), "alice", time.Now()))
		Expect(code).To(Equal(http.StatusOK), body)
		var got opView
		Expect(seen).To(Receive(&got))
		Expect(got).To(Equal(opView{body: del, contentLength: int64(len(del)), payloadHash: sum}))

		tampered := strings.Replace(del, "<Key>k</Key>", "<Key>K</Key>", 1)
		req := signedAt(ctx, withPayloadHash(newClientRequest(ctx, http.MethodPost, "/plain?delete", del), sum), "alice", time.Now())
		req.Body = io.NopCloser(strings.NewReader(tampered))
		code, body = send(req)
		Expect(code).To(Equal(http.StatusBadRequest))
		Expect(body).To(ContainSubstring("<Code>XAmzContentSHA256Mismatch</Code>"))
		Expect(seen).To(Receive(&got))
		Expect(got.body).To(Equal(tampered), "the op reads every byte before the verdict")
		Expect(got.err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	It("renders auth failures and a suspended user as radosgw's error documents", func(ctx SpecContext) {
		code, body := serve(signedAt(ctx, newGet(ctx, "http://s3.example.com/"), "sue", time.Now()))
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>UserSuspended</Code>"))
		req := signedAt(ctx, newGet(ctx, "http://s3.example.com/"), "alice", time.Now())
		req.Header.Set("X-Amz-Date", "20150830T100000Z")
		code, body = serve(req)
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>RequestTimeTooSkewed</Code>"))
		code, body = serve(newGet(ctx, "http://s3.example.com/?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKALICE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20150830T123600Z&X-Amz-Expires=1&X-Amz-SignedHeaders=host&X-Amz-Signature=00"))
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>AccessDenied</Code><Message>The pre-signed URL has expired</Message>"))
	})

	// rgw-go serves no CORS preflight yet (docs/exclusions.md): it verifies a
	// signed OPTIONS request over its own method, where radosgw signs it over
	// Access-Control-Request-Method and skips the compare.
	It("verifies a signed OPTIONS request over OPTIONS and answers it NotImplemented", func(ctx SpecContext) {
		preflight := func(method string) *http.Request {
			req := httptest.NewRequestWithContext(ctx, method, "http://s3.example.com/plain/k", nil)
			req.Header.Set("Origin", "https://example.org")
			req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			return req
		}
		code, body := serve(signedAt(ctx, preflight(http.MethodOptions), "alice", time.Now()))
		Expect(code).To(Equal(http.StatusNotImplemented), body)
		Expect(body).To(ContainSubstring("<Code>NotImplemented</Code>"))

		asRadosgw := signedAt(ctx, preflight(http.MethodGet), "alice", time.Now())
		asRadosgw.Method = http.MethodOptions
		code, body = serve(asRadosgw)
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>SignatureDoesNotMatch</Code>"))

		noMethod := preflight(http.MethodOptions)
		noMethod.Header.Del("Access-Control-Request-Method")
		code, body = serve(signedAt(ctx, noMethod, "alice", time.Now()))
		Expect(code).To(Equal(http.StatusNotImplemented), "radosgw answers 400 InvalidArgument: %s", body)

		unknownKey := preflight(http.MethodOptions)
		unknownKey.Header.Del("Access-Control-Request-Method")
		code, body = serve(signWith(ctx, unknownKey, "AKNOBODY", "x", time.Now()))
		Expect(code).To(Equal(http.StatusForbidden), "radosgw answers 400 InvalidArgument before the lookup")
		Expect(body).To(ContainSubstring("<Code>InvalidAccessKeyId</Code>"))
	})

	// The key is unknown: a form the route refuses is 501 from authentication,
	// a form it takes goes on to the key lookup and is 403 InvalidAccessKeyId,
	// so the status shows which side of the whitelist the route sits on at
	// that release (rgw_rest_s3.cc:5899-5969 at v19.2.6, :6466-6540 at v20.2.4).
	DescribeTable("answers the dispatched route's payload forms at the cluster's release",
		func(ctx SpecContext, rel denc.Release, method, target, hash string, wantStatus int, wantCode string) {
			rs := memstore.New(memstore.Config{Release: rel})
			env := &op.Env{Zone: rs, Users: rs, Accounts: rs, Buckets: rs, Objects: rs, Multipart: rs, Stats: rs, Usage: rs, Metadata: rs, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
			rh := s3.NewHandler(env, auth.New(auth.DefaultConfig(), rs, rs), s3.Config{})
			req := withPayloadHash(newBodyRequest(ctx, method, "http://s3.example.com"+target, "hello"), hash)
			if strings.HasPrefix(hash, "STREAMING-") {
				req.Header.Set("X-Amz-Decoded-Content-Length", "5")
			}
			rec := httptest.NewRecorder()
			rh.ServeHTTP(rec, signWith(ctx, req, "AKNOBODY", "x", time.Now()))
			Expect(rec.Code).To(Equal(wantStatus), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>" + wantCode + "</Code>"))
		},
		Entry("DELETE object takes no signed body on Squid", denc.Squid, http.MethodDelete, "/plain/k", helloSHA256, 501, "NotImplemented"),
		Entry("DELETE object takes no signed body on Tentacle", denc.Tentacle, http.MethodDelete, "/plain/k", helloSHA256, 501, "NotImplemented"),
		Entry("GET ?logging takes no signed body on Squid", denc.Squid, http.MethodGet, "/plain?logging", helloSHA256, 501, "NotImplemented"),
		Entry("GET ?logging takes one on Tentacle", denc.Tentacle, http.MethodGet, "/plain?logging", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("POST ?restore is a form upload on Squid and takes no signed body", denc.Squid, http.MethodPost, "/plain/k?restore", helloSHA256, 501, "NotImplemented"),
		Entry("POST ?restore is restore_obj on Tentacle and takes one", denc.Tentacle, http.MethodPost, "/plain/k?restore", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("DELETE ?website takes one, radosgw typing it as SET_BUCKET_WEBSITE", denc.Squid, http.MethodDelete, "/plain?website", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("POST ?delete takes one", denc.Squid, http.MethodPost, "/plain?delete", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("CompleteMultipartUpload takes no aws-chunked body", denc.Squid, http.MethodPost, "/plain/k?uploadId=u", streamingSigned, 501, "NotImplemented"),
		Entry("UploadPart takes an aws-chunked body", denc.Tentacle, http.MethodPut, "/plain/k?uploadId=u&partNumber=1", streamingSigned, 403, "InvalidAccessKeyId"),
	)
})
