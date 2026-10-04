package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing/iotest"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var streamTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

const (
	streamDate  = "20130524T000000Z"
	streamScope = "20130524/us-east-1/s3/aws4_request"
	streamSeed  = "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9"
)

// signedChunks frames data as signed aws-chunked chunks of at most size bytes,
// then the final chunk and an empty trailer section, using the reference
// StreamSigner: its event-stream string to sign with empty headers is the S3
// chunk's, sha256("") standing in the headers' slot. The returned signatures
// are hex, one per chunk, the final chunk's last.
func signedChunks(ctx context.Context, data []byte, size int, seed string) (stream []byte, sigs []string) {
	seedRaw, err := hex.DecodeString(seed)
	Expect(err).NotTo(HaveOccurred())
	signer := v4.NewStreamSigner(testCredentials(), "s3", "us-east-1", seedRaw)
	frame := func(chunk []byte) {
		sig, err := signer.GetSignature(ctx, nil, chunk, streamTime)
		Expect(err).NotTo(HaveOccurred())
		sigs = append(sigs, hex.EncodeToString(sig))
		stream = append(stream, chunkFrame(chunk, sigs[len(sigs)-1])...)
	}
	for chunk := range slices.Chunk(data, size) {
		frame(chunk)
	}
	frame(nil)
	return stream, sigs
}

// chunkFrame is one signed aws-chunked chunk: its header line, its data and
// the CRLF that ends it, which for the final chunk is the empty trailer
// section.
func chunkFrame(data []byte, sig string) []byte {
	return fmt.Appendf(nil, "%x%s%s\r\n%s\r\n", len(data), chunkSigPrefix, sig, data)
}

// expectEOF asserts that r, read to its end, keeps answering io.EOF.
func expectEOF(r io.Reader) {
	n, err := r.Read(make([]byte, 1))
	Expect(n).To(BeZero())
	Expect(err).To(Equal(io.EOF))
}

// unsignedChunks frames data as unsigned aws-chunked chunks of at most size
// bytes, then the final chunk and an empty trailer section.
func unsignedChunks(data []byte, size int) []byte {
	var b bytes.Buffer
	for chunk := range slices.Chunk(data, size) {
		fmt.Fprintf(&b, "%x\r\n%s\r\n", len(chunk), chunk)
	}
	b.WriteString("0\r\n\r\n")
	return b.Bytes()
}

// withTrailer replaces the empty trailer section of a signedChunks stream
// (the final "\r\n" after the 0 chunk line) with section.
func withTrailer(stream []byte, section string) []byte {
	out := make([]byte, 0, len(stream)-2+len(section))
	out = append(out, stream[:len(stream)-2]...)
	return append(out, section...)
}

// dataIndex returns where the data of the chunk that carries exactly chunk
// starts in stream; the CRLFs around it keep a match out of the hex.
func dataIndex(stream []byte, chunk string) int {
	i := bytes.Index(stream, []byte("\r\n"+chunk+"\r\n"))
	Expect(i).To(BeNumerically(">=", 0), "chunk %q in the stream", chunk)
	return i + len("\r\n")
}

func chunkedFor(src io.Reader, class payloadClass, decoded uint64, trailer string) *chunkedReader {
	p := chunkedParams{
		date: streamDate, scope: streamScope, seedSignature: streamSeed,
		signingKey: signingKeyV4(testSecret, streamScope), class: class, decoded: decoded,
	}
	if trailer != "" {
		p.trailerNames = strings.Split(trailer, ",")
	}
	return newChunkedReader(src, p)
}

// lastWithEOF returns io.EOF together with its last bytes, as a net/http
// request body does on the read that meets its Content-Length.
type lastWithEOF struct{ rest []byte }

func (r *lastWithEOF) Read(p []byte) (int, error) {
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	if len(r.rest) == 0 {
		return n, io.EOF
	}
	return n, nil
}

// bigData is four bufio buffers of payload.
func bigData() []byte { return bytes.Repeat([]byte("abcdefgh"), 2048) }

// readLarge reads r to its end in 64 KiB reads, so that a chunk larger than
// the bufio buffer is read straight from the body.
func readLarge(r io.Reader) ([]byte, error) {
	var got []byte
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		switch {
		case errors.Is(err, io.EOF):
			return got, nil
		case err != nil:
			return got, err
		}
	}
}

var (
	signedClass        = payloadClass{kind: payloadChunked}
	unsignedClass      = payloadClass{kind: payloadChunked, unsignedPayload: true, trailingChecksum: true, unsignedChunked: true}
	trailerSignedClass = payloadClass{kind: payloadChunked, trailingChecksum: true, trailerSignature: true}
	zeroSig            = strings.Repeat("0", chunkSigLen)
)

var _ = Describe("chunkedReader, signed chunks", func() {
	It("decodes a stream the reference signer produced and ends with EOF", func(ctx SpecContext) {
		data := bytes.Repeat([]byte("abcdefgh"), 3000) // 24000 bytes, two 16 KiB chunks
		stream, _ := signedChunks(ctx, data, 16384, streamSeed)
		r := chunkedFor(bytes.NewReader(stream), signedClass, uint64(len(data)), "")
		got, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(data))
		expectEOF(r)
	})

	It("delivers a chunk's bytes before the rest of the stream arrives", func(ctx SpecContext) {
		pr, pw := io.Pipe()
		// Closing the read side ends a write the spec leaves blocked.
		DeferCleanup(pr.Close)
		r := chunkedFor(pr, signedClass, 11, "")
		stream, _ := signedChunks(ctx, []byte("hello world"), 5, streamSeed)
		firstChunkEnd := bytes.Index(stream, []byte("\r\n")) + 2 + 5 // header line + 5 data bytes
		written := make(chan error, 2)
		go func() {
			_, werr := pw.Write(stream[:firstChunkEnd])
			written <- werr
		}()
		buf := make([]byte, 64)
		n, err := r.Read(buf)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(buf[:n])).To(Equal("hello"), "no whole-body buffering")
		Eventually(written).WithTimeout(5*time.Second).WithPolling(10*time.Millisecond).
			Should(Receive(BeNil()), "the first chunk was taken whole")
		go func() {
			_, werr := pw.Write(stream[firstChunkEnd:])
			written <- errors.Join(werr, pw.Close())
		}()
		rest, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rest)).To(Equal(" world"))
		Eventually(written).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(BeNil()))
	})

	It("reports a chunk that fails before the decoded length as SignatureDoesNotMatch, after delivering it", func(ctx SpecContext) {
		stream, _ := signedChunks(ctx, []byte("hello world"), 5, streamSeed)
		stream[dataIndex(stream, "hello")] = 'j'
		r := chunkedFor(bytes.NewReader(stream), signedClass, 11, "")
		got, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		Expect(string(got)).To(Equal("jello"), "the bad chunk was delivered, the next was not")
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch), "sticky")
	})

	DescribeTable("reports the chunk that reaches the decoded length as XAmzContentSHA256Mismatch, after delivering it",
		func(ctx SpecContext, data, corrupt, want string) {
			stream, _ := signedChunks(ctx, []byte(data), 5, streamSeed)
			stream[dataIndex(stream, corrupt)] ^= 0x01
			r := chunkedFor(bytes.NewReader(stream), signedClass, uint64(len(data)), "")
			got, err := io.ReadAll(r)
			Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
			Expect(string(got)).To(Equal(want))
			_, err = r.Read(make([]byte, 1))
			Expect(err).To(MatchError(op.ErrContentSHA256Mismatch), "sticky")
		},
		Entry("a one-chunk stream", "hello", "hello", "iello"),
		Entry("the last of three chunks", "hello world", "d", "hello worle"),
	)

	DescribeTable("rejects a stream chained from a different seed signature",
		func(ctx SpecContext, data string, wantErr error) {
			stream, _ := signedChunks(ctx, []byte(data), 5, strings.Repeat("0", 64))
			_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, uint64(len(data)), ""))
			Expect(err).To(MatchError(wantErr))
		},
		Entry("in its only chunk, checked when the decoded length is reached", "hello", op.ErrContentSHA256Mismatch),
		Entry("in its first chunk, checked before the decoded length", "hello world", op.ErrSignatureDoesNotMatch),
	)

	It("does not compare the final chunk's declared signature once the decoded length is delivered", func(ctx SpecContext) {
		stream, sigs := signedChunks(ctx, []byte("hello"), 5, streamSeed)
		final := sigs[len(sigs)-1]
		stream = bytes.Replace(stream, []byte("0"+chunkSigPrefix+final), []byte("0"+chunkSigPrefix+strings.Repeat("f", 64)), 1)
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 5, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("accepts an empty payload as a lone final chunk", func(ctx SpecContext) {
		stream, sigs := signedChunks(ctx, nil, 5, streamSeed)
		Expect(sigs).To(HaveLen(1))
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 0, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	DescribeTable("rejects malformed framing with InvalidArgument",
		func(stream string) {
			got, err := io.ReadAll(chunkedFor(strings.NewReader(stream), signedClass, 5, ""))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(got).To(BeEmpty(), "nothing of the bad chunk is delivered")
		},
		Entry("no chunk-signature in signed mode", "5\r\nhello\r\n0\r\n\r\n"),
		Entry("signature not 64 long", "5;chunk-signature=abc\r\nhello\r\n"),
		Entry("signature 65 long", "5;chunk-signature="+zeroSig+"0\r\nhello\r\n"),
		Entry("wrong key", "5;chunk-sig="+zeroSig+"\r\nhello\r\n"),
		Entry("a 15-byte key other than chunk-signature", "5;chunk-signaturx="+zeroSig+"\r\nhello\r\n"),
		Entry("size not hex", "zz;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("a negative size, which strtoull wraps to 2^64-1", "-1;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("seventeen hex digits, which strtoull saturates", "1ffffffffffffffff;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("seventeen digits even when the value fits", "00000000000000005;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("an empty size", ";chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("a 0x prefix", "0x5;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("a leading blank", " 5;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("a plus sign", "+5;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("a byte after the digits", "5z;chunk-signature="+zeroSig+"\r\nhello\r\n"),
		Entry("a header line ending in a bare newline", "5;chunk-signature="+zeroSig+"\nhello\r\n"),
		Entry("header over 101 bytes", strings.Repeat("f", 40)+";chunk-signature="+zeroSig+"\r\n"),
		Entry("header over 101 bytes cut short by the end of the body", strings.Repeat("f", 102)),
		Entry("header without a newline in 4 KiB", strings.Repeat("f", 5000)),
	)

	DescribeTable("requires the CRLF that ends a chunk's data, which radosgw does not",
		func(ctx SpecContext, data string) {
			stream, _ := signedChunks(ctx, []byte(data), 5, streamSeed)
			end := dataIndex(stream, "hello") + len("hello")
			stream = slices.Delete(stream, end, end+len("\r\n"))
			got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, uint64(len(data)), ""))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(string(got)).To(Equal("hello"), "the chunk before it verified and was delivered")
		},
		Entry("after a chunk before the decoded length", "hello world"),
		Entry("after the chunk that reaches the decoded length", "hello"),
	)

	It("verifies a chunk before it reads the CRLF that ends it", func(ctx SpecContext) {
		stream, _ := signedChunks(ctx, []byte("hello world"), 5, streamSeed)
		end := dataIndex(stream, "hello") + len("hello")
		stream[end-1] = 'p'
		stream = slices.Delete(stream, end, end+len("\r\n"))
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 11, ""))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch), "not the missing CRLF's InvalidArgument")
	})

	It("verifies the chunk that reaches the decoded length before it reads anything after it", func(ctx SpecContext) {
		stream, _ := signedChunks(ctx, []byte("hello"), 5, streamSeed)
		end := dataIndex(stream, "hello") + len("hello")
		stream[end-5] = 'j'
		stream = slices.Delete(stream, end, end+len("\r\n"))
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 5, ""))
		Expect(string(got)).To(Equal("jello"))
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch), "not the missing CRLF's InvalidArgument")
	})

	DescribeTable("rejects a malformed size in the unsigned form, where radosgw stores a corrupted multi-chunk object",
		func(stream string) {
			got, err := io.ReadAll(chunkedFor(strings.NewReader(stream), unsignedClass, 11, ""))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(got).To(BeEmpty(), "nothing of the bad chunk is delivered")
		},
		Entry("a negative first size", "-1\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"),
		Entry("seventeen hex digits", "1ffffffffffffffff\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"),
		Entry("an empty size", "\r\nhello\r\n0\r\n\r\n"),
		Entry("a 0x prefix", "0x5\r\nhello\r\n0\r\n\r\n"),
	)

	It("ignores what follows a ';' in the unsigned form, within radosgw's 101-byte header buffer counted with the CRLF before it", func() {
		// The second header line is "6;" and its extension and CRLF; with the
		// CRLF that ends "hello" it fills the buffer exactly, or overflows it
		// by one byte.
		stream := func(ext int) string {
			return "5;x\r\nhello\r\n6;" + strings.Repeat("x", ext) + "\r\n world\r\n0\r\n\r\n"
		}
		fits := 101 - len("\r\n6;\r\n")
		got, err := io.ReadAll(chunkedFor(strings.NewReader(stream(fits)), unsignedClass, 11, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello world"))
		got, err = io.ReadAll(chunkedFor(strings.NewReader(stream(fits+1)), unsignedClass, 11, ""))
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		Expect(string(got)).To(Equal("hello"))
	})

	It("reports a stream that ends inside a chunk, or inside what follows the decoded length, as an unexpected EOF, keeping memory flat", func(ctx SpecContext) {
		huge := "ffffffffffffffff;chunk-signature=" + zeroSig + "\r\n0123456789"
		r := chunkedFor(strings.NewReader(huge), signedClass, math.MaxInt64, "")
		got, err := io.ReadAll(r)
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		Expect(string(got)).To(Equal("0123456789"), "the declared and decoded sizes are counters, not allocations")
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF), "sticky")
		stream, _ := signedChunks(ctx, []byte("hello"), 5, streamSeed)
		final := bytes.LastIndex(stream, []byte("\r\n0"))
		for _, cut := range [][]byte{stream[:final+1], stream[:final+len("\r\n")], stream[:final+len("\r\n0;chunk")]} {
			got, err = io.ReadAll(chunkedFor(bytes.NewReader(cut), signedClass, 5, ""))
			Expect(string(got)).To(Equal("hello"))
			Expect(err).To(MatchError(io.ErrUnexpectedEOF), "cut after %q", cut[final:])
		}
	})

	It("keeps an error the body returns past the final chunk, so a body cut short gets no verdict", func(ctx SpecContext) {
		stream, _ := signedChunks(ctx, []byte("hello"), 5, streamSeed)
		r := chunkedFor(io.MultiReader(bytes.NewReader(stream[:len(stream)-1]), iotest.ErrReader(io.ErrUnexpectedEOF)), signedClass, 5, "")
		got, err := io.ReadAll(r)
		Expect(string(got)).To(Equal("hello"))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF), "sticky")
	})
})

// The payload's length is x-amz-decoded-content-length, as
// RGWPutObj_ObjStore::get_data takes it (rgw_rest.cc:1068-1101 at v19.2.6).
var _ = Describe("chunkedReader, framing that disagrees with x-amz-decoded-content-length", func() {
	Context("chunks that carry more data", func() {
		It("delivers the decoded length and checks the chunk it ends in, as radosgw's complete() does", func(ctx SpecContext) {
			stream, _ := signedChunks(ctx, []byte("hello world"), 11, streamSeed)
			got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 5, ""))
			Expect(string(got)).To(Equal("hello"))
			Expect(err).To(MatchError(op.ErrContentSHA256Mismatch), "the chunk's signature covers bytes the decoder never read")
		})

		// radosgw's complete() accepts a payload without a trailer signature
		// whose decoded length is followed by more data, or by the end of the
		// body (rgw_auth_s3.cc:1555-1694 at v19.2.6).
		helloWorldIn5 := func(ctx context.Context) []byte {
			stream, _ := signedChunks(ctx, []byte("hello world"), 5, streamSeed)
			return stream
		}
		hello := func(ctx context.Context) []byte {
			stream, _ := signedChunks(ctx, []byte("hello"), 5, streamSeed)
			return stream
		}
		helloThenEnd := func(ctx context.Context) []byte {
			stream := hello(ctx)
			return stream[:dataIndex(stream, "hello")+len("hello")]
		}
		unsignedHelloWorld := func(context.Context) []byte { return unsignedChunks([]byte("hello world"), 11) }
		empty := func(context.Context) []byte { return nil }

		DescribeTable("end the payload at the decoded length when no final chunk follows it",
			func(ctx SpecContext, stream func(context.Context) []byte, class payloadClass, decoded uint64, want string) {
				r := chunkedFor(bytes.NewReader(stream(ctx)), class, decoded, "")
				got, err := io.ReadAll(r)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(got)).To(Equal(want))
				expectEOF(r)
			},
			Entry("a data chunk after the chunk that reaches it", helloWorldIn5, signedClass, uint64(5), "hello"),
			Entry("an unsigned chunk that runs past it", unsignedHelloWorld, unsignedClass, uint64(5), "hello"),
			Entry("a data chunk where a zero length expects the final chunk", hello, signedClass, uint64(0), ""),
			Entry("the end of the body right after the chunk that reaches it", helloThenEnd, signedClass, uint64(5), "hello"),
			Entry("an empty body for a zero length", empty, signedClass, uint64(0), ""),
		)

		DescribeTable("refuse such a payload with SignatureDoesNotMatch when a trailer signature is expected",
			func(ctx SpecContext, stream func(context.Context) []byte, decoded uint64, want string) {
				got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream(ctx)), trailerSignedClass, decoded, ""))
				Expect(string(got)).To(Equal(want))
				Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
			},
			Entry("a data chunk after the chunk that reaches it", helloWorldIn5, uint64(5), "hello"),
			Entry("a data chunk where a zero length expects the final chunk", hello, uint64(0), ""),
			Entry("the end of the body right after the chunk that reaches it", helloThenEnd, uint64(5), "hello"),
		)

		It("leave the rest of the body unread", func(ctx SpecContext) {
			stream := helloWorldIn5(ctx)
			body := io.MultiReader(bytes.NewReader(stream[:dataIndex(stream, " worl")]), iotest.ErrReader(errors.New("read past the next chunk header")))
			got, err := io.ReadAll(chunkedFor(body, signedClass, 5, ""))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal("hello"))
		})

		It("keep a body's own error right after the chunk that reaches the length", func(ctx SpecContext) {
			body := io.MultiReader(bytes.NewReader(helloThenEnd(ctx)), iotest.ErrReader(io.ErrUnexpectedEOF))
			got, err := io.ReadAll(chunkedFor(body, signedClass, 5, ""))
			Expect(string(got)).To(Equal("hello"))
			Expect(err).To(MatchError(io.ErrUnexpectedEOF), "a truncated body is not an end of body")
		})

		// A chunk of bigData is read past bufio's buffer, so its last read
		// meets the body's io.EOF together with the last bytes, as a net/http
		// request body delivers them.
		cutAt := func(stream []byte, n int) []byte { return stream[:dataIndex(stream, string(bigData()))+n] }
		signedBigThenEnd := func(ctx context.Context) []byte {
			stream, _ := signedChunks(ctx, bigData(), len(bigData()), streamSeed)
			return cutAt(stream, len(bigData()))
		}
		unsignedBigThenEnd := func(context.Context) []byte {
			return cutAt(unsignedChunks(bigData(), len(bigData())), len(bigData()))
		}
		unsignedBigCut := func(context.Context) []byte { return cutAt(unsignedChunks(bigData(), len(bigData())), 12288) }

		DescribeTable("take the body's last bytes arriving with io.EOF for the end of the body",
			func(ctx SpecContext, stream func(context.Context) []byte, class payloadClass, decoded int, wantErr error) {
				got, err := readLarge(chunkedFor(&lastWithEOF{rest: stream(ctx)}, class, uint64(decoded), ""))
				Expect(got).To(Equal(bigData()[:decoded]))
				if wantErr == nil {
					Expect(err).NotTo(HaveOccurred())
					return
				}
				Expect(err).To(MatchError(wantErr))
			},
			Entry("a signed chunk that reaches the length", signedBigThenEnd, signedClass, len(bigData()), nil),
			Entry("an unsigned chunk that reaches the length", unsignedBigThenEnd, unsignedClass, len(bigData()), nil),
			Entry("an unsigned chunk that runs past the length", unsignedBigCut, unsignedClass, 12288, nil),
			Entry("a signed chunk that reaches the length, with a trailer signature expected", signedBigThenEnd, trailerSignedClass, len(bigData()), op.ErrSignatureDoesNotMatch),
		)
	})

	Context("chunks that carry less data", func() {
		// When the op reads on, radosgw answers a final chunk before the
		// decoded length with SignatureDoesNotMatch when its signature is
		// wrong, else InvalidArgument (recv_chunk and create_next,
		// rgw_auth_s3.cc:1284-1336 and :1111-1208 at v19.2.6).
		DescribeTable("refuse the final chunk with InvalidArgument once its signature verifies",
			func(ctx SpecContext, data string, decoded uint64) {
				stream, _ := signedChunks(ctx, []byte(data), 5, streamSeed)
				got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, decoded, ""))
				Expect(string(got)).To(Equal(data))
				Expect(err).To(MatchError(op.ErrInvalidArgument))
			},
			Entry("after one data chunk", "hello", uint64(11)),
			Entry("with no data at all", "", uint64(5)),
		)

		It("compare the final chunk's signature, unlike a final chunk after the decoded length", func(ctx SpecContext) {
			stream, sigs := signedChunks(ctx, []byte("hello"), 5, streamSeed)
			stream = bytes.Replace(stream, []byte(sigs[len(sigs)-1]), []byte(strings.Repeat("f", 64)), 1)
			got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 11, ""))
			Expect(string(got)).To(Equal("hello"))
			Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		})

		It("check the last data chunk first, as a chunk before the decoded length", func(ctx SpecContext) {
			stream, _ := signedChunks(ctx, []byte("hello world"), 5, streamSeed)
			stream[dataIndex(stream, "d")] = 'e'
			got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedClass, 12, ""))
			Expect(string(got)).To(Equal("hello worle"))
			Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		})

		It("refuse an unsigned final chunk with InvalidArgument", func() {
			got, err := io.ReadAll(chunkedFor(bytes.NewReader(unsignedChunks([]byte("hello"), 5)), unsignedClass, 11, ""))
			Expect(string(got)).To(Equal("hello"))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})
	})
})

var _ = Describe("chunkedReader, the trailer bound", func() {
	// streamWithSection returns a signed one-chunk stream of "hello" whose
	// trailer count is total: from the CRLF that ends the data through the
	// closing CRLF, the span radosgw's 256-byte trailer buffer holds
	// (rgw_auth_s3.cc:1583-1606 at v19.2.6). The section is one unannounced
	// header line, which no trailer map takes and no trailer signature covers.
	streamWithSection := func(ctx context.Context, total int) []byte {
		stream, _ := signedChunks(ctx, []byte("hello"), 5, streamSeed)
		dataEnd := bytes.Index(stream, []byte("hello")) + len("hello")
		head := len(stream) - 2 - dataEnd // the CRLF and the final chunk line
		pad := total - head - len("x:\r\n\r\n")
		out := withTrailer(stream, "x:"+strings.Repeat("A", pad)+"\r\n\r\n")
		Expect(len(out) - dataEnd).To(Equal(total))
		return out
	}

	It("accepts a section of 1024 bytes, four times what radosgw's buffer holds", func(ctx SpecContext) {
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(streamWithSection(ctx, 1024)), signedClass, 5, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("answers LimitExceeded at 1025 bytes, where radosgw truncates the section silently", func(ctx SpecContext) {
		r := chunkedFor(bytes.NewReader(streamWithSection(ctx, 1025)), signedClass, 5, "")
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrLimitExceeded))
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(op.ErrLimitExceeded), "sticky")
	})
})

var _ = Describe("parseChunkSize", func() {
	DescribeTable("takes one to sixteen hex digits",
		func(s string, want uint64) {
			got, ok := parseChunkSize([]byte(s))
			Expect(ok).To(BeTrue(), "%q", s)
			Expect(got).To(Equal(want), "%q", s)
		},
		Entry("one digit", "5", uint64(5)),
		Entry("either case", "aB", uint64(0xab)),
		Entry("sixteen digits hold 2^64-1", "ffffffffffffffff", uint64(math.MaxUint64)),
		Entry("leading zeros within sixteen digits", "0000000000000400", uint64(1024)),
	)
})

var _ = Describe("authData.completer for an aws-chunked payload", func() {
	// chunkedPut signs a PUT of stream as the reference client does and
	// returns its view and authData, chained as the reference stream signer
	// chained stream: from streamSeed, at streamTime.
	chunkedPut := func(ctx context.Context, stream []byte, decoded string) (*requestView, *authData) {
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "http://s3.example.com/b/k", bytes.NewReader(stream))
		req.Header.Set("Content-Length", strconv.Itoa(len(stream)))
		req.Header.Set("X-Amz-Decoded-Content-Length", decoded)
		sdkSign(ctx, req, streamingPayload)
		cfg := DefaultConfig()
		rv := newRequestView(req)
		d, err := authDataV4(rv, false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.verify(testSecret)).To(BeTrue(), "the signature the reference client made")
		d.signature = streamSeed
		d.v4.date, d.v4.scope = streamDate, streamScope
		return rv, d
	}

	It("returns a reader that decodes the payload, and the decoded length", func(ctx SpecContext) {
		data := []byte("hello world")
		stream, _ := signedChunks(ctx, data, 5, streamSeed)
		rv, d := chunkedPut(ctx, stream, "11")
		body, n, err := d.completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(11)))
		got, err := io.ReadAll(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(data))
	})

	It("checks the chunk signatures with the secret it is given", func(ctx SpecContext) {
		stream, _ := signedChunks(ctx, []byte("hello world"), 5, streamSeed)
		rv, d := chunkedPut(ctx, stream, "11")
		body, _, err := d.completer(rv, "not-the-secret")
		Expect(err).NotTo(HaveOccurred())
		got, err := io.ReadAll(body)
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		Expect(string(got)).To(Equal("hello"))
	})

	It("takes an empty x-amz-decoded-content-length for a zero-length payload, as parse_content_length does", func(ctx SpecContext) {
		stream, _ := signedChunks(ctx, nil, 5, streamSeed)
		rv, d := chunkedPut(ctx, stream, "")
		body, n, err := d.completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeZero())
		got, err := io.ReadAll(body)
		Expect(err).NotTo(HaveOccurred(), "any decoded length but 0 would refuse the lone final chunk")
		Expect(got).To(BeEmpty())
	})
})
