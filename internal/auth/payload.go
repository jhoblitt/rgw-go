package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// The x-amz-content-sha256 values radosgw tells apart besides
// UNSIGNED-PAYLOAD (rgw_auth_s3.h:524-537 at v19.2.6, :527-540 at v20.2.4).
const (
	emptyPayloadHash         = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	streamingPayload         = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	streamingPayloadTrailer  = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
)

// decodedContentLengthHeader carries an aws-chunked payload's length once
// its framing is removed.
const decodedContentLengthHeader = "x-amz-decoded-content-length"

// payloadKind is the completer get_auth_data_v4 builds.
type payloadKind uint8

const (
	payloadNone    payloadKind = iota // null_completer_factory: the body is not verified
	payloadSingle                     // AWSv4ComplSingle: the body's SHA-256 must equal the header
	payloadChunked                    // AWSv4ComplMulti: aws-chunked framing
)

// payloadClass is the completer get_auth_data_v4 chooses and the
// AWSv4ComplMulti flags it computes from x-amz-content-sha256; only
// AWSv4ComplMulti reads the flags.
type payloadClass struct {
	kind             payloadKind
	unsignedPayload  bool // FLAG_UNSIGNED_PAYLOAD: the hash contains UNSIGNED-PAYLOAD
	trailingChecksum bool // FLAG_TRAILING_CHECKSUM: the hash ends in TRAILER
	unsignedChunked  bool // FLAG_UNSIGNED_CHUNKED: STREAMING-UNSIGNED-PAYLOAD-TRAILER
	trailerSignature bool // FLAG_TRAILER_SIGNATURE: STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER
}

// classifyPayload is get_auth_data_v4's choice of completer
// (rgw_rest_s3.cc:5855-6000 at v19.2.6, :6422-6571 at v20.2.4) over the
// predicates of rgw_auth_s3.h:663-702 at v19.2.6, :666-705 at v20.2.4. A
// request has no body when its Content-Length is 0 and it has no
// Transfer-Encoding. The ops that take no body never complete the payload, so
// radosgw refuses here a request without a body whose hash is neither an
// unsigned form nor the empty string's, with XAmzContentSHA256Mismatch.
func classifyPayload(rv *requestView, payloadHash string) (payloadClass, error) {
	empty := rv.contentLength() == 0 && !rv.chunkedTE()
	unsigned := strings.Contains(payloadHash, unsignedPayload)
	if empty && !unsigned && payloadHash != emptyPayloadHash {
		return payloadClass{}, fmt.Errorf("%w: empty payload with a non-empty checksum", op.ErrContentSHA256Mismatch)
	}
	pc := payloadClass{
		unsignedPayload:  unsigned,
		trailingChecksum: strings.HasSuffix(payloadHash, "TRAILER"),
		unsignedChunked:  payloadHash == streamingUnsignedTrailer,
		trailerSignature: payloadHash == streamingPayloadTrailer,
	}
	switch {
	case payloadHash == unsignedPayload, unsigned && !pc.trailingChecksum, empty:
		return payloadClass{kind: payloadNone}, nil
	case !strings.HasPrefix(payloadHash, "STREAMING-"):
		pc.kind = payloadSingle
	default:
		pc.kind = payloadChunked
	}
	return pc, nil
}

// acceptedBy refuses a payload form the op does not take, as radosgw's
// abstractor does once the empty-payload rule has passed and before the
// access key is looked up: a single chunk only for the op types its switch
// lists (rgw_rest_s3.cc:5903-5944 at v19.2.6, :6470-6515 at v20.2.4), an
// aws-chunked payload only for RGW_OP_PUT_OBJ (:5962-5969, :6533-6540). The
// refusal is ERR_NOT_IMPLEMENTED.
func (pc payloadClass) acceptedBy(forms op.PayloadForms) error {
	switch {
	case pc.kind == payloadSingle && forms&op.PayloadSigned == 0:
		return fmt.Errorf("%w: aws4 completion for this operation", op.ErrNotImplemented)
	case pc.kind == payloadChunked && forms&op.PayloadChunked == 0:
		return fmt.Errorf("%w: aws4 completion for this operation in streaming mode", op.ErrNotImplemented)
	}
	return nil
}

// hashReader is AWSv4ComplSingle (rgw_auth_s3.cc:1722-1770 at v19.2.6,
// :1699-1747 at v20.2.4): it hashes the body as it passes and compares the
// hex digest with x-amz-content-sha256 byte for byte, so upper-case hex never
// matches. radosgw compares when the op calls complete() after reading the
// body and reports a mismatch as XAmzContentSHA256Mismatch
// (do_aws4_auth_completion, rgw_op.cc:1360-1381 at v19.2.6, :1597-1618 at
// v20.2.4); here the Read that reaches the end of the body compares and
// returns that error in place of io.EOF, then and on every later Read. Any
// other error the body returns, a truncation's among them, is kept the same
// way: net/http answers the Read after a truncation with io.EOF, which would
// otherwise give a verdict over a body shorter than the one sent.
type hashReader struct {
	src      io.Reader
	h        hash.Hash
	expected string
	err      error // the first error, the verdict included, once there is one
}

func newHashReader(src io.Reader, expected string) *hashReader {
	return &hashReader{src: src, h: sha256.New(), expected: expected}
}

func (r *hashReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.src.Read(p)
	r.h.Write(p[:n])
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) && hex.EncodeToString(r.h.Sum(nil)) != r.expected {
		err = fmt.Errorf("%w: payload sha256 differs from x-amz-content-sha256", op.ErrContentSHA256Mismatch)
	}
	r.err = err
	return n, err
}

// completer builds the reader an op reads the body through once the
// signature has verified, and the length the op is to take for it. A payload
// without a completer is read as it came, with the request's length, -1 for
// a chunked transfer encoding; a single chunk is read through a hashReader.
// An aws-chunked payload's length is x-amz-decoded-content-length, which
// radosgw requires and refuses with EINVAL when it is missing or
// parse_content_length makes it negative (AWSv4ComplMulti::modify_request_state,
// rgw_auth_s3.cc:1532-1548 at v19.2.6, :1509-1525 at v20.2.4); its decoder,
// which checks the chunk signatures with the secret, is not implemented yet.
func (d *authData) completer(rv *requestView, _ string) (body io.Reader, contentLength int64, err error) {
	switch d.payload.kind {
	case payloadSingle:
		return newHashReader(rv.req.Body, d.payloadHash), rv.contentLength(), nil
	case payloadChunked:
		v, ok := rv.header(decodedContentLengthHeader)
		if !ok {
			return nil, 0, fmt.Errorf("%w: aws-chunked payload without %s", op.ErrInvalidArgument, decodedContentLengthHeader)
		}
		if parseContentLength(v) < 0 {
			return nil, 0, fmt.Errorf("%w: malformed %s", op.ErrInvalidArgument, decodedContentLengthHeader)
		}
		return nil, 0, op.ErrNotImplemented
	default:
		return nil, rv.contentLength(), nil
	}
}

// parseContentLength is parse_content_length (rgw_rest.h:788-803 at v19.2.6,
// :800-815 at v20.2.4): an empty value is 0, and any other is strict_strtoll
// in base 10 (common/strtol.cc:42-59 at both tags), which takes leading
// whitespace and a sign but requires digits that run to the end and fit an
// int64. A value it refuses is -1.
func parseContentLength(s string) int64 {
	if s == "" {
		return 0
	}
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	// n accumulates the negated value, so math.MinInt64 is reachable.
	var n int64
	for ; i < len(s) && isDigit(s[i]); i++ {
		d := int64(s[i] - '0')
		if n < (math.MinInt64+d)/10 {
			return -1
		}
		n = n*10 - d
	}
	switch {
	case i == start, i != len(s), !neg && n == math.MinInt64:
		return -1
	case neg:
		return n
	}
	return -n
}
