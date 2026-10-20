package auth

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const (
	// payloadChunkAlgorithm is AWS4_HMAC_SHA256_PAYLOAD_STR.
	payloadChunkAlgorithm  = "AWS4-HMAC-SHA256-PAYLOAD"
	trailerAlgorithm       = "AWS4-HMAC-SHA256-TRAILER"
	trailerSignatureHeader = "x-amz-trailer-signature"
	chunkSigPrefix         = ";chunk-signature="
	// chunkSigLen is ChunkMeta::SIG_SIZE.
	chunkSigLen = 64
	// maxChunkSizeDigits holds 2^64-1.
	maxChunkSizeDigits = 16
	// maxChunkHeader is ChunkMeta::META_MAX_SIZE, 101
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.h#L315-L316 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.h#L317-L318 at v20.2.4):
	// the buffer radosgw parses a chunk header from holds the CRLF that ends
	// the previous chunk's data, sixteen hex digits, the signature and the
	// header's own CRLF.
	maxChunkHeader = len("\r\n") + maxChunkSizeDigits + len(chunkSigPrefix) + chunkSigLen + len("\r\n")
	// maxTrailerSection bounds what follows the last data chunk, counted as
	// radosgw's trailer buffer counts it; see finish.
	maxTrailerSection = 1024
	// chunkedBufferSize is the bufio buffer; bulk reads bypass it.
	chunkedBufferSize = 4096
)

// chunkedParams is what AWSv4ComplMulti::create receives
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1696-L1720 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1673-L1697 at v20.2.4),
// and the decoded length PutObject reads.
type chunkedParams struct {
	date, scope string // the request's, as sent
	// seedSignature is the request signature, the chain's first previous
	// signature.
	seedSignature string
	signingKey    []byte // signingKeyV4(secret, scope)
	class         payloadClass
	// trailerNames is x-amz-trailer split on ","; only these headers enter
	// the trailer map.
	trailerNames []string
	// decoded is x-amz-decoded-content-length, the payload's length, as
	// RGWPutObj_ObjStore::get_data takes it
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1068-L1101 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1073-L1106 at v20.2.4).
	decoded uint64
}

// chunkedReader is AWSv4ComplMulti as an io.Reader, read as PutObject reads
// it. It strips the aws-chunked framing and delivers each chunk's data as it
// arrives, hashing it, up to the decoded length and no further. A signed
// chunk is verified when data past it is asked for, before the CRLF that ends
// it, as radosgw verifies the previous chunk before it parses the next header
// (recv_chunk,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1284-L1291 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1261-L1268 at v20.2.4);
// a mismatch there is SignatureDoesNotMatch. The chunk in progress when the
// decoded length is reached is verified by the next Read, as complete()
// verifies it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1555-L1564,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1532-L1541),
// and a mismatch is XAmzContentSHA256Mismatch, the error
// do_aws4_auth_completion makes of complete()'s false
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1370-L1371 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1607-L1608 at v20.2.4).
// That Read then reads what follows, as complete describes, and reports the
// verdict in place of io.EOF; only a Read that returns io.EOF makes the
// trailers it kept answer through Trailer. Every error is sticky. Memory is
// O(1): a fixed bufio buffer, chunk headers sliced from it, one SHA-256 state
// and, once the final chunk line is read, a maxTrailerSection+1-byte array
// for the trailer section, from a copy of which the kept trailer values are
// sliced; chunk sizes and the decoded length are counters, never allocation
// sizes.
type chunkedReader struct {
	br      *bufio.Reader
	p       chunkedParams
	prevSig string
	// declared is the signature the chunk in progress declares.
	declared string
	// remaining counts the chunk in progress's bytes not yet delivered, and
	// left those of the decoded length.
	remaining, left uint64
	// pending marks a signed chunk whose data is hashed but not yet verified.
	pending bool
	// first holds until the first chunk header is read: no CRLF precedes it.
	first bool
	done  bool
	h     hash.Hash
	err   error
	// values is what trailers returned, set together with done.
	values map[string]string
}

func newChunkedReader(src io.Reader, p chunkedParams) *chunkedReader {
	return &chunkedReader{
		br: bufio.NewReaderSize(src, chunkedBufferSize), p: p, prevSig: p.seedSignature,
		left: p.decoded, first: true, h: sha256.New(),
	}
}

// chunkStringToSign is calc_chunk_signature's string to sign
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1210-L1228 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1187-L1205 at v20.2.4).
func chunkStringToSign(date, scope, prevSig, dataHashHex string) string {
	return strings.Join([]string{payloadChunkAlgorithm, date, scope, prevSig, emptyPayloadHash, dataHashHex}, "\n")
}

func (r *chunkedReader) chunkSignature(dataHashHex string) string {
	return signatureV4(r.p.signingKey, chunkStringToSign(r.p.date, r.p.scope, r.prevSig, dataHashHex))
}

func (r *chunkedReader) fail(err error) (int, error) {
	r.err = err
	return 0, err
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 && r.left > 0 {
		if err := r.nextChunk(); err != nil {
			return r.fail(err)
		}
	}
	if r.left == 0 {
		values, err := r.complete()
		if err != nil {
			return r.fail(err)
		}
		r.values, r.done = values, true
		return 0, io.EOF
	}
	if want := min(r.remaining, r.left); uint64(len(p)) > want {
		p = p[:want]
	}
	n, err := r.br.Read(p)
	if r.pending {
		r.h.Write(p[:n])
	}
	delivered := uint64(n) //nolint:gosec // bufio.Reader panics rather than return a negative count
	r.remaining -= delivered
	r.left -= delivered
	// A net/http request body returns its last bytes together with io.EOF,
	// and answers io.EOF again on the next Read, which then judges the end.
	if err != nil && (n == 0 || !errors.Is(err, io.EOF)) {
		r.err = unexpected(err)
		return n, r.err
	}
	return n, nil
}

// nextChunk ends the chunk just delivered, short of the decoded length: it
// verifies the chunk and parses the next header. A zero-size chunk is the
// final one, and a payload that ends with it before the decoded length is
// refused as radosgw refuses it when the op reads on: SignatureDoesNotMatch
// when the chunk's signature is wrong, else InvalidArgument (recv_chunk and
// create_next,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1284-L1336
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1111-L1208 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1261-L1313
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1088-L1185 at v20.2.4).
func (r *chunkedReader) nextChunk() error {
	if !r.verifyPending() {
		return fmt.Errorf("%w: aws-chunked chunk signature mismatch", op.ErrSignatureDoesNotMatch)
	}
	size, declared, _, err := r.nextHeader()
	if err != nil {
		return unexpected(err)
	}
	if size == 0 {
		if !r.p.class.unsignedChunked && !sigEqual(declared, r.chunkSignature(emptyPayloadHash)) {
			return fmt.Errorf("%w: aws-chunked final chunk signature mismatch", op.ErrSignatureDoesNotMatch)
		}
		return fmt.Errorf("%w: aws-chunked payload ends before %s", op.ErrInvalidArgument, decodedContentLengthHeader)
	}
	r.remaining, r.declared, r.pending = size, declared, !r.p.class.unsignedChunked
	return nil
}

// complete is AWSv4ComplMulti::complete as PutObject reaches it, once the
// decoded length is delivered
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1555-L1694 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1532-L1671 at v20.2.4):
// the chunk in progress is verified first. What follows is either more
// data or the end of the body, which endAtLength answers, or the CRLF and
// the final chunk line, parsed as strictly as any chunk header and
// followed by the trailer section, whose trailers it returns. The final
// chunk's declared signature is not compared.
func (r *chunkedReader) complete() (map[string]string, error) {
	if !r.verifyPending() {
		return nil, fmt.Errorf("%w: aws-chunked last chunk signature mismatch", op.ErrContentSHA256Mismatch)
	}
	if r.remaining > 0 {
		return nil, r.endAtLength()
	}
	size, _, consumed, err := r.nextHeader()
	switch {
	case errors.Is(err, io.EOF):
		return nil, r.endAtLength()
	case err != nil:
		return nil, unexpected(err)
	case size != 0:
		return nil, r.endAtLength()
	}
	return r.finish(consumed)
}

// endAtLength answers a payload whose decoded length is followed by more
// data, or by the end of the body, rather than by the final chunk. One that
// expects a trailer signature is refused. Any other ends at the decoded
// length, as it does in radosgw's complete()
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1555-L1694 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1532-L1671 at v20.2.4),
// and the rest of the body is left unread.
func (r *chunkedReader) endAtLength() error {
	if r.p.class.trailerSignature {
		return fmt.Errorf("%w: no aws-chunked trailer signature follows %s", op.ErrSignatureDoesNotMatch, decodedContentLengthHeader)
	}
	return nil
}

// verifyPending compares the signature the chunk in progress declares with
// the one its data gives, and a match chains it (is_signature_mismatched,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1230-L1273 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1207-L1250 at v20.2.4).
// An unsigned chunk has nothing to verify.
func (r *chunkedReader) verifyPending() bool {
	if !r.pending {
		return true
	}
	r.pending = false
	want := r.chunkSignature(hex.EncodeToString(r.h.Sum(nil)))
	r.h.Reset()
	if !sigEqual(want, r.declared) {
		return false
	}
	r.prevSig = r.declared
	return true
}

func sigEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// nextHeader consumes the CRLF that ends the previous chunk's data, which
// radosgw does not require, and reads the next chunk header line; consumed
// counts both. Together they fit maxChunkHeader, radosgw's parse buffer, and
// the line ends in CRLF. The signed form is exactly
// <size>;chunk-signature=<64 bytes> (create_next,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1134-L1183 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1111-L1160 at v20.2.4);
// the unsigned form is <size>, anything after a ';' ignored
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1184-L1207,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1161-L1184).
// A body that ends before a byte of either is io.EOF, and one that ends
// inside them io.ErrUnexpectedEOF.
func (r *chunkedReader) nextHeader() (size uint64, declared string, consumed int, err error) {
	if !r.first {
		var crlf [2]byte
		if _, err = io.ReadFull(r.br, crlf[:]); err != nil {
			return 0, "", 0, err
		}
		if crlf != [2]byte{'\r', '\n'} {
			return 0, "", 0, fmt.Errorf("%w: aws-chunked data not followed by CRLF", op.ErrInvalidArgument)
		}
		consumed = len(crlf)
	}
	r.first = false
	line, err := r.br.ReadSlice('\n')
	consumed += len(line)
	switch {
	case consumed > maxChunkHeader:
		return 0, "", 0, fmt.Errorf("%w: aws-chunked header too long", op.ErrInvalidArgument)
	case err != nil && consumed == 0:
		return 0, "", 0, err
	case err != nil:
		return 0, "", 0, unexpected(err)
	}
	meta, ok := bytes.CutSuffix(line, []byte("\r\n"))
	if !ok {
		return 0, "", 0, fmt.Errorf("%w: malformed aws-chunked header", op.ErrInvalidArgument)
	}
	sizeField, ext := meta, []byte(nil)
	if i := bytes.IndexByte(meta, ';'); i >= 0 {
		sizeField, ext = meta[:i], meta[i:]
	}
	if !r.p.class.unsignedChunked {
		sig, found := bytes.CutPrefix(ext, []byte(chunkSigPrefix))
		if !found || len(sig) != chunkSigLen {
			return 0, "", 0, fmt.Errorf("%w: malformed aws-chunked chunk signature", op.ErrInvalidArgument)
		}
		declared = string(sig)
	}
	size, ok = parseChunkSize(sizeField)
	if !ok {
		return 0, "", 0, fmt.Errorf("%w: malformed aws-chunked chunk size", op.ErrInvalidArgument)
	}
	return size, declared, consumed, nil
}

// unexpected is a stream that ends early.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// parseChunkSize reads a chunk size as one to sixteen hex digits and nothing
// else. radosgw uses strtoull
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1127-L1132 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1104-L1109 at v20.2.4),
// which skips blanks, takes a sign, a 0x prefix and trailing bytes, and
// turns "-1" or a seventeen-digit size into 2^64-1, so the next chunk's
// framing is read as data (tracker #81123).
func parseChunkSize(s []byte) (uint64, bool) {
	if len(s) == 0 || len(s) > maxChunkSizeDigits {
		return 0, false
	}
	var n uint64
	for _, c := range s {
		var d byte
		switch {
		case '0' <= c && c <= '9':
			d = c - '0'
		case 'a' <= c && c <= 'f':
			d = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		n = n<<4 | uint64(d)
	}
	return n, true
}

// finish reads the trailer section, the rest of the body, after the final
// chunk line, and checks it. radosgw reads the section into a 256-byte buffer
// but asks for at most 256-pos-1 bytes per read, so its size check never
// fires and a longer section is cut short: a signed one then fails its
// trailer signature
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1583-L1614 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1560-L1591 at v20.2.4;
// tracker #81122). The bound here is maxTrailerSection bytes counted the same
// way, from the CRLF that ends the last data chunk, so the consumed bytes of
// that CRLF and the final chunk line count against it; a longer section is
// the LimitExceeded that radosgw's dead check names. The trailer signature
// chains from the final chunk signature computed from prevSig, the last
// verified chunk's or the seed, as complete() computes it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1565-L1575,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1542-L1552).
func (r *chunkedReader) finish(consumed int) (map[string]string, error) {
	budget := maxTrailerSection - consumed
	var buf [maxTrailerSection + 1]byte
	n, err := readToEnd(r.br, buf[:budget+1])
	switch {
	case n > budget:
		return nil, fmt.Errorf("%w: aws-chunked trailer section exceeds %d bytes", op.ErrLimitExceeded, maxTrailerSection)
	case err != nil:
		return nil, err
	}
	return r.trailers(buf[:n], r.chunkSignature(emptyPayloadHash))
}

// readToEnd reads into buf until it is full or src ends. Unlike io.ReadFull
// it answers the end of src with nil, so an io.ErrUnexpectedEOF it returns is
// the body's own, a body cut short.
func readToEnd(src io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := src.Read(buf[n:])
		n += m
		switch {
		case errors.Is(err, io.EOF):
			return n, nil
		case err != nil:
			return n, err
		}
	}
	return n, nil
}

// trailerStringToSign is calc_v4_trailer_signature's string to sign
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1391-L1418 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1368-L1395 at v20.2.4):
// the final chunk signature and the hex SHA-256 of the canonical trailer
// headers, which are "name:value\n" for each signed trailer in name order
// (get_canon_amz_hdrs,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L66-L86,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L67-L87).
func trailerStringToSign(date, scope, finalChunkSig, canonicalTrailers string) string {
	return strings.Join([]string{trailerAlgorithm, date, scope, finalChunkSig, sha256Hex(canonicalTrailers)}, "\n")
}

// trailers reads the trailer section of a payload whose x-amz-content-sha256
// ends in TRAILER and returns the trailers x-amz-trailer lists, as radosgw
// puts each into the request's environment
// (extract_trailing_headers,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L1504-L1532 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1459-L1487 at v20.2.4).
// It reads no other payload's section, of which radosgw requires nothing
// (complete()'s expect_trailer_signature,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L1708-L1712 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1663-L1667 at v20.2.4),
// though radosgw takes the listed trailers from any payload's section when
// x_amz_trailer is set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L1681-L1683 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1636-L1638 at v20.2.4).
// The section is read as lines that end in CRLF, each split at its first
// colon into a name and an untrimmed value. The first line named
// x-amz-trailer-signature declares the signature, and the first line bearing
// each name x-amz-trailer lists is that trailer, which the trailer signature
// covers when the payload has one; every other line, and any bytes after the
// last CRLF, are ignored.
// radosgw instead finds the signature and each listed name anywhere in the
// section (docs/exclusions.md, "aws-chunked trailer sections are read line by
// line"). For a payload that expects a trailer signature, a declared
// signature that is missing or differs from the one computed over the
// trailers is SignatureDoesNotMatch, and no trailer is returned. The checksum
// values are kept, not validated.
func (r *chunkedReader) trailers(section []byte, finalChunkSig string) (map[string]string, error) {
	if !r.p.class.trailingChecksum && !r.p.class.trailerSignature {
		return nil, nil
	}
	var declared string
	haveDeclared := false
	listed := map[string]string{}
	rest := string(section)
	for {
		line, after, ok := strings.Cut(rest, "\r\n")
		if !ok {
			break
		}
		rest = after
		name, value, ok := strings.Cut(line, ":")
		switch {
		case !ok:
		case name == trailerSignatureHeader:
			if !haveDeclared {
				declared, haveDeclared = value, true
			}
		// trailerNames holds an empty name for each empty part of
		// x-amz-trailer, which ceph::split drops (common/split.h at both
		// tags).
		case name != "" && slices.Contains(r.p.trailerNames, name):
			if _, seen := listed[name]; !seen {
				listed[name] = value
			}
		}
	}
	if r.p.class.trailerSignature {
		var canonical strings.Builder
		for _, name := range slices.Sorted(maps.Keys(listed)) {
			canonical.WriteString(name + ":" + listed[name] + "\n")
		}
		want := signatureV4(r.p.signingKey, trailerStringToSign(r.p.date, r.p.scope, finalChunkSig, canonical.String()))
		if !sigEqual(declared, want) {
			return nil, fmt.Errorf("%w: aws-chunked trailer signature mismatch", op.ErrSignatureDoesNotMatch)
		}
	}
	return foldTrailers(r.p.trailerNames, listed), nil
}

// foldTrailers keys each listed trailer's value by its name in lower case,
// the first name x-amz-trailer lists winning among those that fold together.
// radosgw keys its environment by the name upper-cased (ys_header_mangle,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L2086-L2096 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L2080-L2090 at v20.2.4),
// and inserts the listed names in order, keeping the first.
func foldTrailers(names []string, listed map[string]string) map[string]string {
	folded := make(map[string]string, len(listed))
	for _, name := range names {
		v, ok := listed[name]
		if !ok {
			continue
		}
		key := asciiLower(name)
		if _, taken := folded[key]; !taken {
			folded[key] = v
		}
	}
	return folded
}

var _ op.Trailers = (*chunkedReader)(nil)

// Trailer answers op.Trailers from the trailers the Read that returned
// io.EOF kept, matching name in any ASCII case.
func (r *chunkedReader) Trailer(name string) (string, bool) {
	v, ok := r.values[asciiLower(name)]
	return v, ok
}
