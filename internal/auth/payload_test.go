package auth

import (
	"bufio"
	"cmp"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing/iotest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// absent stands for a header the request does not carry, so that "" can be
// one sent empty.
const absent = "-"

func viewWithBody(ctx context.Context, hash string, contentLength int64, chunkedTE bool) *requestView {
	req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://h/b/k", nil)
	if hash != absent {
		req.Header.Set("X-Amz-Content-Sha256", hash)
	}
	req.ContentLength = contentLength
	if chunkedTE {
		req.TransferEncoding = []string{"chunked"}
	}
	return newRequestView(req)
}

var _ = Describe("payload classification", func() {
	DescribeTable("classifyPayload is get_auth_data_v4's completer choice",
		func(ctx SpecContext, hash string, cl int64, te bool, want payloadClass, wantErr error) {
			rv := viewWithBody(ctx, hash, cl, te)
			got, err := classifyPayload(rv, expectedPayloadHash(rv))
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("absent header with a body is unsigned", absent, int64(5), false, payloadClass{kind: payloadNone}, nil),
		Entry("empty header with a body is a single completer, unlike an absent one", "", int64(5), false,
			payloadClass{kind: payloadSingle}, nil),
		Entry("empty header with no body is a mismatch now", "", int64(0), false, payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("UNSIGNED-PAYLOAD", unsignedPayload, int64(5), false, payloadClass{kind: payloadNone}, nil),
		Entry("hex with a body is a single completer", helloSHA256, int64(5), false, payloadClass{kind: payloadSingle}, nil),
		Entry("hex with a chunked transfer encoding is not empty", helloSHA256, int64(-1), true, payloadClass{kind: payloadSingle}, nil),
		Entry("empty hash with no body", emptyPayloadHash, int64(0), false, payloadClass{kind: payloadNone}, nil),
		Entry("non-empty hash with no body is a mismatch now", helloSHA256, int64(0), false, payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("garbage hash with no body is a mismatch now", "not-a-hash", int64(0), false, payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("upper-case empty hash with no body is a mismatch now", strings.ToUpper(emptyPayloadHash), int64(0), false,
			payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("streaming signed chunks", streamingPayload, int64(100), false, payloadClass{kind: payloadChunked}, nil),
		Entry("streaming signed chunks with a signed trailer", streamingPayloadTrailer, int64(100), false,
			payloadClass{kind: payloadChunked, trailingChecksum: true, trailerSignature: true}, nil),
		Entry("streaming unsigned chunks with a trailer", streamingUnsignedTrailer, int64(100), false,
			payloadClass{kind: payloadChunked, unsignedPayload: true, trailingChecksum: true, unsignedChunked: true}, nil),
		Entry("signed streaming form with no body is a mismatch now, being neither unsigned nor the empty hash",
			streamingPayload, int64(0), false, payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("unsigned streaming form with no body is empty", streamingUnsignedTrailer, int64(0), false, payloadClass{kind: payloadNone}, nil),
		Entry("UNSIGNED-PAYLOAD-TRAILER without STREAMING falls to a single completer", "UNSIGNED-PAYLOAD-TRAILER", int64(5), false,
			payloadClass{kind: payloadSingle, unsignedPayload: true, trailingChecksum: true}, nil),
	)

	DescribeTable("acceptedBy is get_auth_data_v4's per-op whitelist",
		func(kind payloadKind, forms op.PayloadForms, wantErr error) {
			err := payloadClass{kind: kind}.acceptedBy(forms)
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(wantErr))
		},
		Entry("no completer passes an op that takes nothing", payloadNone, op.PayloadForms(0), nil),
		Entry("a single chunk needs an op in the single-chunk whitelist", payloadSingle, op.PayloadForms(0), op.ErrNotImplemented),
		Entry("a single chunk on a whitelisted op", payloadSingle, op.PayloadSigned, nil),
		Entry("a single chunk on put_obj", payloadSingle, op.PayloadSigned|op.PayloadChunked, nil),
		Entry("aws-chunked on an op that takes only a single chunk", payloadChunked, op.PayloadSigned, op.ErrNotImplemented),
		Entry("aws-chunked on an op that takes nothing", payloadChunked, op.PayloadForms(0), op.ErrNotImplemented),
		Entry("aws-chunked on put_obj", payloadChunked, op.PayloadSigned|op.PayloadChunked, nil),
	)
})

var _ = Describe("hashReader", func() {
	It("passes a matching body through and ends with EOF", func() {
		r := newHashReader(strings.NewReader("hello"), helloSHA256)
		got, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
		n, err := r.Read(make([]byte, 1))
		Expect(n).To(BeZero())
		Expect(err).To(Equal(io.EOF))
	})

	It("reports a mismatch on the read that reaches EOF, and keeps reporting it", func() {
		r := newHashReader(iotest.OneByteReader(strings.NewReader("hellp")), helloSHA256)
		got, err := io.ReadAll(r)
		Expect(string(got)).To(Equal("hellp"), "the bytes are delivered before the verdict, as radosgw's completer runs after the body")
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	It("returns the bytes of the read that carries both the last data and EOF", func() {
		r := newHashReader(iotest.DataErrReader(strings.NewReader("hellp")), helloSHA256)
		buf := make([]byte, 16)
		n, err := r.Read(buf)
		Expect(string(buf[:n])).To(Equal("hellp"))
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	It("compares the hex byte for byte, so an uppercase expected value never matches", func() {
		r := newHashReader(strings.NewReader("hello"), strings.ToUpper(helloSHA256))
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	It("keeps a truncated body's error for every later read, so a body cut short gets no verdict", func() {
		// net/http answers the read after a truncation with io.EOF, and the
		// three bytes that came are the ones this hash was taken over.
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("PUT /b/k HTTP/1.1\r\nHost: h\r\nContent-Length: 10\r\n\r\nhel")))
		Expect(err).NotTo(HaveOccurred())
		r := newHashReader(req.Body, sha256Hex("hel"))
		got, err := io.ReadAll(r)
		Expect(string(got)).To(Equal("hel"))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		n, err := r.Read(make([]byte, 1))
		Expect(n).To(BeZero())
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
	})
})

var _ = Describe("parseContentLength", func() {
	DescribeTable("is parse_content_length over strict_strtoll",
		func(s string, want int64) {
			Expect(parseContentLength(s)).To(Equal(want), "%q", s)
		},
		Entry("an empty value is zero", "", int64(0)),
		Entry("digits", "5", int64(5)),
		Entry("a plus sign", "+5", int64(5)),
		Entry("leading whitespace", " \t5", int64(5)),
		Entry("negative zero", "-0", int64(0)),
		Entry("the largest int64", "9223372036854775807", int64(9223372036854775807)),
		Entry("negative", "-1", int64(-1)),
		Entry("trailing whitespace", "5 ", int64(-1)),
		Entry("trailing text", "5x", int64(-1)),
		Entry("no digits", "abc", int64(-1)),
		Entry("a sign alone", "+", int64(-1)),
		Entry("whitespace alone", " ", int64(-1)),
		Entry("hex", "0x10", int64(-1)),
		Entry("overflow", "9223372036854775808", int64(-1)),
	)
})

var _ = Describe("authDataV4's payload classification", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	It("refuses an empty body whose hash is not the empty hash", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/b/k", nil)
		sdkSign(ctx, req, helloSHA256)
		_, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	It("refuses an unsigned x-amz header before the empty-payload rule", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/b/k", nil)
		sdkSign(ctx, req, helloSHA256)
		req.Header.Set("X-Amz-Meta-Injected", "after signing")
		_, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	It("keeps the class it chose", func(ctx SpecContext) {
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/b/k", strings.NewReader("hello"))
		sdkSign(ctx, req, streamingUnsignedTrailer)
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.payload).To(Equal(payloadClass{kind: payloadChunked, unsignedPayload: true, trailingChecksum: true, unsignedChunked: true}))
	})
})

var _ = Describe("completer", func() {
	var cfg Config

	BeforeEach(func() {
		cfg = DefaultConfig()
	})

	signedPut := func(ctx context.Context, body, hash string, extra map[string]string) *requestView {
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/b/k", strings.NewReader(body))
		// The SDK signs content-length from req.ContentLength; net/http's
		// server keeps the header that carried it, which httptest leaves out.
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		sdkSign(ctx, req, cmp.Or(hash, unsignedPayload))
		return newRequestView(req)
	}

	authDataFor := func(rv *requestView) *authData {
		d, err := authDataV4(rv, false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.verify(testSecret)).To(BeTrue(), "the signature the reference client made")
		return d
	}

	It("returns no reader and the request length for an unsigned payload", func(ctx SpecContext) {
		rv := signedPut(ctx, "hello", "", nil)
		body, n, err := authDataFor(rv).completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(body).To(BeNil())
		Expect(n).To(Equal(int64(5)))
	})

	It("returns no reader for an empty body signed with the empty hash", func(ctx SpecContext) {
		rv := signedPut(ctx, "", emptyPayloadHash, nil)
		body, n, err := authDataFor(rv).completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(body).To(BeNil())
		Expect(n).To(BeZero())
	})

	It("returns a hash reader over the request body for a signed single chunk", func(ctx SpecContext) {
		rv := signedPut(ctx, "hello", helloSHA256, nil)
		body, n, err := authDataFor(rv).completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(5)))
		got, err := io.ReadAll(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("holds a signed single chunk to the hash the request carried", func(ctx SpecContext) {
		rv := signedPut(ctx, "hellp", helloSHA256, nil)
		body, _, err := authDataFor(rv).completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		_, err = io.ReadAll(body)
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	DescribeTable("requires a usable x-amz-decoded-content-length for a chunked payload",
		func(ctx SpecContext, decoded string, wantErr error) {
			extra := map[string]string{}
			if decoded != absent {
				extra["X-Amz-Decoded-Content-Length"] = decoded
			}
			rv := signedPut(ctx, "0;chunk-signature=x\r\n\r\n", streamingPayload, extra)
			_, _, err := authDataFor(rv).completer(rv, testSecret)
			Expect(err).To(MatchError(wantErr))
		},
		Entry("missing", absent, op.ErrInvalidArgument),
		Entry("negative", "-1", op.ErrInvalidArgument),
		Entry("not a number", "abc", op.ErrInvalidArgument),
	)
})
