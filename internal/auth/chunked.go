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
	// maxChunkHeader is ChunkMeta::META_MAX_SIZE, 101 (rgw_auth_s3.h:315-316
	// at v19.2.6, :317-318 at v20.2.4): the buffer radosgw parses a chunk
	// header from holds the CRLF that ends the previous chunk's data, sixteen
	// hex digits, the signature and the header's own CRLF.
	maxChunkHeader = len("\r\n") + maxChunkSizeDigits + len(chunkSigPrefix) + chunkSigLen + len("\r\n")
	// maxTrailerSection bounds what follows the last data chunk, counted as
	// radosgw's trailer buffer counts it; see finish.
	maxTrailerSection = 1024
	// chunkedBufferSize is the bufio buffer; bulk reads bypass it.
	chunkedBufferSize = 4096
)

// chunkedParams is what AWSv4ComplMulti::create receives
// (rgw_auth_s3.cc:1696-1720 at v19.2.6, :1673-1697 at v20.2.4), and the
// decoded length PutObject reads.
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
	// RGWPutObj_ObjStore::get_data takes it (rgw_rest.cc:1068-1101 at
	// v19.2.6, :1073-1106 at v20.2.4).
	decoded uint64
}

// chunkedReader is AWSv4ComplMulti as an io.Reader, read as PutObject reads
// it. It strips the aws-chunked framing and delivers each chunk's data as it
// arrives, hashing it, up to the decoded length and no further. A signed
// chunk is verified when data past it is asked for, before the CRLF that ends
// it, as radosgw verifies the previous chunk before it parses the next header
// (recv_chunk, rgw_auth_s3.cc:1284-1291 at v19.2.6, :1261-1268 at v20.2.4); a
// mismatch there is SignatureDoesNotMatch. The chunk in progress when the
// decoded length is reached is verified by the next Read, as complete()
// verifies it (:1555-1564, :1532-1541), and a mismatch is
// XAmzContentSHA256Mismatch, the error do_aws4_auth_completion makes of
// complete()'s false (rgw_op.cc:1370-1371 at v19.2.6, :1607-1608 at
// v20.2.4). That Read then reads what follows, as complete describes, and
// reports the verdict in place of io.EOF.
// Every error is sticky. Memory is O(1): a fixed bufio buffer, chunk headers
// sliced from it, one SHA-256 state and, once the final chunk line is read, a
// maxTrailerSection+1-byte array for the trailer section; chunk sizes and the
// decoded length are counters, never allocation sizes.
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
}

func newChunkedReader(src io.Reader, p chunkedParams) *chunkedReader {
	return &chunkedReader{
		br: bufio.NewReaderSize(src, chunkedBufferSize), p: p, prevSig: p.seedSignature,
		left: p.decoded, first: true, h: sha256.New(),
	}
}

// chunkStringToSign is calc_chunk_signature's string to sign
// (rgw_auth_s3.cc:1210-1228 at v19.2.6, :1187-1205 at v20.2.4).
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
		if err := r.complete(); err != nil {
			return r.fail(err)
		}
		r.done = true
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
// create_next, rgw_auth_s3.cc:1284-1336 and :1111-1208 at v19.2.6,
// :1261-1313 and :1088-1185 at v20.2.4).
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
// decoded length is delivered (rgw_auth_s3.cc:1555-1694 at v19.2.6,
// :1532-1671 at v20.2.4): the chunk in progress is verified first. What
// follows is either more data or the end of the body, which endAtLength
// answers, or the CRLF and the final chunk line, parsed as strictly as any
// chunk header and followed by the trailer section. The final chunk's
// declared signature is not compared.
func (r *chunkedReader) complete() error {
	if !r.verifyPending() {
		return fmt.Errorf("%w: aws-chunked last chunk signature mismatch", op.ErrContentSHA256Mismatch)
	}
	if r.remaining > 0 {
		return r.endAtLength()
	}
	size, _, consumed, err := r.nextHeader()
	switch {
	case errors.Is(err, io.EOF):
		return r.endAtLength()
	case err != nil:
		return unexpected(err)
	case size != 0:
		return r.endAtLength()
	}
	return r.finish(consumed)
}

// endAtLength answers a payload whose decoded length is followed by more
// data, or by the end of the body, rather than by the final chunk. One that
// expects a trailer signature is refused. Any other ends at the decoded
// length, as it does in radosgw's complete() (rgw_auth_s3.cc:1555-1694 at
// v19.2.6, :1532-1671 at v20.2.4), and the rest of the body is left unread.
func (r *chunkedReader) endAtLength() error {
	if r.p.class.trailerSignature {
		return fmt.Errorf("%w: no aws-chunked trailer signature follows %s", op.ErrSignatureDoesNotMatch, decodedContentLengthHeader)
	}
	return nil
}

// verifyPending compares the signature the chunk in progress declares with
// the one its data gives, and a match chains it (is_signature_mismatched,
// rgw_auth_s3.cc:1230-1273 at v19.2.6, :1207-1250 at v20.2.4). An unsigned
// chunk has nothing to verify.
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
// <size>;chunk-signature=<64 bytes> (create_next, rgw_auth_s3.cc:1134-1183
// at v19.2.6, :1111-1160 at v20.2.4); the unsigned form is <size>, anything
// after a ';' ignored (:1184-1207, :1161-1184). A body that ends before a
// byte of either is io.EOF, and one that ends inside them
// io.ErrUnexpectedEOF.
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
// else. radosgw uses strtoull (rgw_auth_s3.cc:1127-1132 at v19.2.6,
// :1104-1109 at v20.2.4), which skips blanks, takes a sign, a 0x prefix and
// trailing bytes, and turns "-1" or a seventeen-digit size into 2^64-1, so
// the next chunk's framing is read as data (tracker #81123).
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
// trailer signature (rgw_auth_s3.cc:1583-1614 at v19.2.6, :1560-1591 at
// v20.2.4; tracker #81122). The bound here is maxTrailerSection bytes counted
// the same way, from the CRLF that ends the last data chunk, so the consumed
// bytes of that CRLF and the final chunk line count against it; a longer
// section is the LimitExceeded that radosgw's dead check names. The trailer
// signature chains from the final chunk signature computed from prevSig, the
// last verified chunk's or the seed, as complete() computes it (:1565-1575,
// :1542-1552).
func (r *chunkedReader) finish(consumed int) error {
	budget := maxTrailerSection - consumed
	var buf [maxTrailerSection + 1]byte
	n, err := readToEnd(r.br, buf[:budget+1])
	switch {
	case n > budget:
		return fmt.Errorf("%w: aws-chunked trailer section exceeds %d bytes", op.ErrLimitExceeded, maxTrailerSection)
	case err != nil:
		return err
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
// (rgw_auth_s3.cc:1391-1418 at v19.2.6, :1368-1395 at v20.2.4): the final
// chunk signature and the hex SHA-256 of the canonical trailer headers, which
// are "name:value\n" for each signed trailer in name order
// (get_canon_amz_hdrs, :66-86, :67-87).
func trailerStringToSign(date, scope, finalChunkSig, canonicalTrailers string) string {
	return strings.Join([]string{trailerAlgorithm, date, scope, finalChunkSig, sha256Hex(canonicalTrailers)}, "\n")
}

// trailers checks the trailer section of a payload that expects a trailer
// signature; radosgw requires nothing of any other payload's section
// (complete(), rgw_auth_s3.cc:1686-1690 at v19.2.6, :1663-1667 at v20.2.4).
// The section is read as lines that end in CRLF, each split at its first
// colon into a name and an untrimmed value. The first line named
// x-amz-trailer-signature declares the signature, and the first line bearing
// each name x-amz-trailer lists is a signed trailer; every other line, and
// any bytes after the last CRLF, are ignored. radosgw instead finds the
// signature and each listed name anywhere in the section
// (docs/exclusions.md, "aws-chunked trailer sections are read line by
// line"). A declared signature that is missing or differs from the one
// computed over the signed trailers is SignatureDoesNotMatch. The checksum
// values are neither validated nor kept.
func (r *chunkedReader) trailers(section []byte, finalChunkSig string) error {
	if !r.p.class.trailerSignature {
		return nil
	}
	var declared string
	haveDeclared := false
	signed := map[string]string{}
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
			if _, seen := signed[name]; !seen {
				signed[name] = value
			}
		}
	}
	var canonical strings.Builder
	for _, name := range slices.Sorted(maps.Keys(signed)) {
		canonical.WriteString(name + ":" + signed[name] + "\n")
	}
	want := signatureV4(r.p.signingKey, trailerStringToSign(r.p.date, r.p.scope, finalChunkSig, canonical.String()))
	if !sigEqual(declared, want) {
		return fmt.Errorf("%w: aws-chunked trailer signature mismatch", op.ErrSignatureDoesNotMatch)
	}
	return nil
}
