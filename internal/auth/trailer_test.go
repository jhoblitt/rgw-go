package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing/iotest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// trailerSig computes the trailer signature the way the AWS documentation
// describes it, independently of chunked.go's helpers.
func trailerSig(finalChunkSig, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	sts := "AWS4-HMAC-SHA256-TRAILER\n" + streamDate + "\n" + streamScope + "\n" + finalChunkSig + "\n" + hex.EncodeToString(sum[:])
	return hex.EncodeToString(hmacSHA256(signingKeyV4(testSecret, streamScope), []byte(sts)))
}

var _ = Describe("chunkedReader, trailers", func() {
	const crc = "x-amz-checksum-crc32c:sOO8/Q==\r\n"

	// signedHello is a signed one-chunk stream of "hello" and its final chunk
	// signature.
	signedHello := func(ctx context.Context) (stream []byte, final string) {
		stream, sigs := signedChunks(ctx, []byte("hello"), 5, streamSeed)
		return stream, sigs[len(sigs)-1]
	}

	DescribeTable("verifies a signed trailer chained from the final chunk signature and delivers the payload",
		func(ctx SpecContext, data string) {
			stream, sigs := signedChunks(ctx, []byte(data), 5, streamSeed)
			stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(sigs[len(sigs)-1], "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
			r := chunkedFor(bytes.NewReader(stream), trailerSignedClass, uint64(len(data)), "x-amz-checksum-crc32c")
			got, err := io.ReadAll(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(data))
			expectEOF(r)
		},
		Entry("after one data chunk", "hello"),
		Entry("after three data chunks", "hello world"),
		Entry("after no data at all", ""),
	)

	It("rejects a wrong or missing trailer signature when the form requires one", func(ctx SpecContext) {
		stream, _ := signedHello(ctx)
		bad := withTrailer(stream, crc+"x-amz-trailer-signature:"+zeroSig+"\r\n\r\n")
		r := chunkedFor(bytes.NewReader(bad), trailerSignedClass, 5, "x-amz-checksum-crc32c")
		got, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		Expect(string(got)).To(Equal("hello"))
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch), "sticky")
		missing := withTrailer(stream, crc+"\r\n")
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(missing), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		empty := withTrailer(stream, crc+"x-amz-trailer-signature:\r\n\r\n")
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(empty), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
	})

	It("signs only the trailers x-amz-trailer announced, in name order", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		section := "x-amz-checksum-sha256:LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=\r\n" + crc + "x-amz-unlisted:ignored\r\n"
		canonical := "x-amz-checksum-crc32c:sOO8/Q==\nx-amz-checksum-sha256:LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=\n"
		stream = withTrailer(stream, section+"x-amz-trailer-signature:"+trailerSig(final, canonical)+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, "x-amz-checksum-sha256,x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("signs an empty header set when x-amz-trailer is absent, as radosgw does", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(final, "")+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, ""))
		Expect(err).NotTo(HaveOccurred())
	})

	It("signs the value after the first colon of a trailer line, untrimmed", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		const line = "x-amz-checksum-crc32c: sOO8:/Q== \r\n"
		stream = withTrailer(stream, line+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c: sOO8:/Q== \n")+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("takes the first of repeated lines", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		sig := trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")
		section := crc + "x-amz-checksum-crc32c:AAAAAA==\r\n" + "x-amz-trailer-signature:" + sig + "\r\nx-amz-trailer-signature:" + zeroSig + "\r\n\r\n"
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(withTrailer(stream, section)), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts the signature line before the trailers it covers", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		stream = withTrailer(stream, "x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n"+crc+"\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("signs each trailer line once when x-amz-trailer repeats or overlaps names",
		func(ctx SpecContext, announced string) {
			stream, final := signedHello(ctx)
			stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
			_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, announced))
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("a name listed twice", "x-amz-checksum-crc32c,x-amz-checksum-crc32c"),
		Entry("one name and its tail", "x-amz-checksum-crc32c,crc32c"),
	)

	It("keeps an error the body returns inside the section, with no trailer verdict", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
		cut := bytes.Index(stream, []byte("x-amz-trailer-signature:")) + len("x-amz-trailer-signature:")
		body := io.MultiReader(bytes.NewReader(stream[:cut]), iotest.ErrReader(io.ErrUnexpectedEOF))
		got, err := io.ReadAll(chunkedFor(body, trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(string(got)).To(Equal("hello"))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF), "not SignatureDoesNotMatch")
	})

	It("chains the trailer signature from the final chunk signature it computes, the declared one not compared", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		forged := strings.Repeat("f", chunkSigLen)
		stream = bytes.Replace(stream, []byte("0"+chunkSigPrefix+final), []byte("0"+chunkSigPrefix+forged), 1)
		computed := withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(computed), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
		declared := withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(forged, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(declared), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
	})

	It("checks the last data chunk before the trailer", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
		stream[dataIndex(stream, "hello")] = 'j'
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(string(got)).To(Equal("jello"))
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	DescribeTable("takes the trailer signature only from a line of its own",
		func(ctx SpecContext, section func(sig string) string) {
			stream, final := signedHello(ctx)
			stream = withTrailer(stream, section(trailerSig(final, "")))
			_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, ""))
			Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		},
		Entry("not from another line's value", func(sig string) string {
			return "x-amz-meta-note:x-amz-trailer-signature:" + sig + "\r\n\r\n"
		}),
		Entry("not from a line whose name only ends in it", func(sig string) string {
			return "xx-amz-trailer-signature:" + sig + "\r\n\r\n"
		}),
		Entry("not from a line the body ends inside, before its CRLF", func(sig string) string {
			return "x-amz-trailer-signature:" + sig
		}),
	)

	DescribeTable("takes an announced trailer only from a line that bears exactly its name",
		func(ctx SpecContext, announced, line string) {
			stream, final := signedHello(ctx)
			stream = withTrailer(stream, line+"x-amz-trailer-signature:"+trailerSig(final, "")+"\r\n\r\n")
			_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, announced))
			Expect(err).NotTo(HaveOccurred(), "the signature covers no trailer")
		},
		Entry("not from a longer name", "x-amz-checksum-crc32c", "x-amz-checksum-crc32c-extra:sOO8/Q==\r\n"),
		Entry("not from a name it is the tail of", "checksum-crc32c", crc),
		Entry("not from a line without a colon", "x-amz-checksum-crc32c", "x-amz-checksum-crc32c sOO8/Q==\r\n"),
		Entry("not from a nameless line for an empty name in the list", "x-amz-checksum-crc32c,", ":sOO8/Q==\r\n"),
	)

	It("does not validate the checksum value and ignores a trailer on the plain streaming form", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		const wrongCRC = "x-amz-checksum-crc32c:AAAAAA==\r\n"
		signed := withTrailer(stream, wrongCRC+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:AAAAAA==\n")+"\r\n\r\n")
		r := chunkedFor(bytes.NewReader(signed), trailerSignedClass, 5, "x-amz-checksum-crc32c")
		_, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(ask(r, "x-amz-checksum-crc32c")).To(Equal(trailerAnswer{"AAAAAA==", true}), "kept unvalidated")
		unrequired := withTrailer(stream, wrongCRC+"x-amz-trailer-signature:"+zeroSig+"\r\n\r\n")
		r = chunkedFor(bytes.NewReader(unrequired), signedClass, 5, "x-amz-checksum-crc32c")
		_, err = io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(ask(r, "x-amz-checksum-crc32c")).To(BeZero(), "the plain streaming form offers no trailer")
	})

	It("decodes the unsigned aws-chunked form the Go SDK sends and requires no signatures", func() {
		stream := "5\r\nhello\r\n6\r\n world\r\n0\r\n" + crc + "\r\n"
		got, err := io.ReadAll(chunkedFor(strings.NewReader(stream), unsignedClass, 11, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello world"))
		withExt := "5;ext=1\r\nhello\r\n0\r\n\r\n"
		got, err = io.ReadAll(chunkedFor(strings.NewReader(withExt), unsignedClass, 5, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("accepts a 290-byte signed SHA512 trailer section, which radosgw's 256-byte buffer cuts off", func(ctx SpecContext) {
		stream, final := signedHello(ctx)
		const sha512 = "x-amz-checksum-sha512:m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="
		stream = withTrailer(stream, sha512+"\r\nx-amz-trailer-signature:"+trailerSig(final, sha512+"\n")+"\r\n\r\n")
		dataEnd := bytes.Index(stream, []byte("hello")) + len("hello")
		Expect(len(stream)-dataEnd).To(Equal(290), "the CRLF, the final chunk line and the section, as radosgw counts them")
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), trailerSignedClass, 5, "x-amz-checksum-sha512"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})
})

// trailerAnswer is what Trailer returns, so that a failure prints both.
type trailerAnswer struct {
	value string
	found bool
}

func ask(t op.Trailers, name string) trailerAnswer {
	v, ok := t.Trailer(name)
	return trailerAnswer{v, ok}
}

// The CRC32 trailer of "hello": its name, value, section line and
// canonical form, and what Trailer answers for it.
const (
	crc32Name  = "x-amz-checksum-crc32"
	helloCRC32 = "NhCmhg=="
	crc32Line  = crc32Name + ":" + helloCRC32 + "\r\n"
	crc32Canon = crc32Name + ":" + helloCRC32 + "\n"
	// fakeLine is chunk data that reads as a trailer line.
	fakeLine = crc32Name + ":AAAAAA==\r\n"
)

var helloCRC = trailerAnswer{helloCRC32, true}

var _ = Describe("chunkedReader, trailer values", func() {
	// signedWith is a signed stream of data in chunks of at most size bytes
	// whose trailer section is lines, a trailer signature over canonical and
	// the closing CRLF.
	signedWith := func(ctx context.Context, data string, size int, lines, canonical string) []byte {
		stream, sigs := signedChunks(ctx, []byte(data), size, streamSeed)
		return withTrailer(stream, lines+"x-amz-trailer-signature:"+trailerSig(sigs[len(sigs)-1], canonical)+"\r\n\r\n")
	}
	// unsignedWith is an unsigned stream of data in chunks of at most size
	// bytes whose trailer section is lines and the closing CRLF.
	unsignedWith := func(data string, size int, lines string) []byte {
		return withTrailer(unsignedChunks([]byte(data), size), lines+"\r\n")
	}
	helloWith := func(ctx context.Context, class payloadClass, lines, canonical string) []byte {
		if class.trailerSignature {
			return signedWith(ctx, "hello", 5, lines, canonical)
		}
		return unsignedWith("hello", 5, lines)
	}
	readHello := func(r io.Reader) {
		got, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	}

	DescribeTable("offers a listed CRC32 trailer once the body has ended",
		func(ctx SpecContext, class payloadClass) {
			r := chunkedFor(bytes.NewReader(helloWith(ctx, class, crc32Line, crc32Canon)), class, 5, crc32Name)
			readHello(r)
			Expect(ask(r, crc32Name)).To(Equal(helloCRC))
		},
		Entry("the signed-trailer payload", trailerSignedClass),
		Entry("the unsigned-trailer payload", unsignedClass),
	)

	DescribeTable("answers not found until the Read that returns io.EOF, and never from the data",
		func(ctx SpecContext, class payloadClass) {
			data := fakeLine + "hello"
			var stream []byte
			if class.trailerSignature {
				stream = signedWith(ctx, data, len(fakeLine), crc32Line, crc32Canon)
			} else {
				stream = unsignedWith(data, len(fakeLine), crc32Line)
			}
			r := chunkedFor(bytes.NewReader(stream), class, uint64(len(data)), crc32Name)
			Expect(ask(r, crc32Name)).To(BeZero(), "before any Read")
			buf := make([]byte, len(data))
			_, err := io.ReadFull(r, buf[:len(fakeLine)])
			Expect(err).NotTo(HaveOccurred())
			Expect(ask(r, crc32Name)).To(BeZero(), "mid-body, after a chunk that reads as a trailer line")
			_, err = io.ReadFull(r, buf[len(fakeLine):])
			Expect(err).NotTo(HaveOccurred())
			Expect(string(buf)).To(Equal(data))
			Expect(ask(r, crc32Name)).To(BeZero(), "every data byte delivered, before io.EOF")
			expectEOF(r)
			Expect(ask(r, crc32Name)).To(Equal(helloCRC), "the section's value, not the data's")
		},
		Entry("the signed-trailer payload", trailerSignedClass),
		Entry("the unsigned-trailer payload", unsignedClass),
	)

	// atBound returns a stream of "hello" whose trailer count, from the CRLF
	// that ends the data through the closing CRLF, is total, with the CRC32
	// line the section's last.
	atBound := func(ctx context.Context, class payloadClass, total int) []byte {
		stream := helloWith(ctx, class, "", crc32Canon)
		dataEnd := dataIndex(stream, "hello") + len("hello")
		// The CRLF that ends the data and the final chunk line.
		head := len("\r\n") + bytes.Index(stream[dataEnd+len("\r\n"):], []byte("\r\n")) + len("\r\n")
		sigLine := string(stream[dataEnd+head : len(stream)-len("\r\n")])
		pad := total - head - len("x:\r\n") - len(sigLine) - len(crc32Line) - len("\r\n")
		stream = append(stream[:dataEnd+head:dataEnd+head], "x:"+strings.Repeat("A", pad)+"\r\n"+sigLine+crc32Line+"\r\n"...)
		Expect(len(stream) - dataEnd).To(Equal(total))
		return stream
	}

	DescribeTable("bounds the section at 1 KiB",
		func(ctx SpecContext, class payloadClass, total int, want trailerAnswer, wantErr error) {
			r := chunkedFor(bytes.NewReader(atBound(ctx, class, total)), class, 5, crc32Name)
			got, err := io.ReadAll(r)
			Expect(string(got)).To(Equal("hello"))
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(MatchError(wantErr))
			}
			Expect(ask(r, crc32Name)).To(Equal(want))
		},
		Entry("keeping a signed value whose line ends the section at the bound", trailerSignedClass, 1024, helloCRC, nil),
		Entry("keeping an unsigned value whose line ends the section at the bound", unsignedClass, 1024, helloCRC, nil),
		Entry("answering a signed section one byte past it with LimitExceeded and nothing offered",
			trailerSignedClass, 1025, trailerAnswer{}, op.ErrLimitExceeded),
		Entry("answering an unsigned section one byte past it with LimitExceeded and nothing offered",
			unsignedClass, 1025, trailerAnswer{}, op.ErrLimitExceeded),
	)

	DescribeTable("offers nothing when the trailer signature fails, which stays SignatureDoesNotMatch",
		func(ctx SpecContext, sigLine func(final string) string) {
			stream, sigs := signedChunks(ctx, []byte("hello"), 5, streamSeed)
			r := chunkedFor(bytes.NewReader(withTrailer(stream, crc32Line+sigLine(sigs[len(sigs)-1])+"\r\n")), trailerSignedClass, 5, crc32Name)
			got, err := io.ReadAll(r)
			Expect(string(got)).To(Equal("hello"))
			Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
			Expect(ask(r, crc32Name)).To(BeZero())
			_, err = r.Read(make([]byte, 1))
			Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch), "sticky")
			Expect(ask(r, crc32Name)).To(BeZero(), "after the sticky error")
		},
		Entry("a signature over another value", func(final string) string {
			return "x-amz-trailer-signature:" + trailerSig(final, crc32Name+":AAAAAA==\n") + "\r\n"
		}),
		Entry("a wrong signature", func(string) string { return "x-amz-trailer-signature:" + zeroSig + "\r\n" }),
		Entry("no signature line", func(string) string { return "" }),
	)

	DescribeTable("offers nothing for a payload that fails before its section is checked",
		func(ctx SpecContext, body func(context.Context) io.Reader, class payloadClass, wantErr error) {
			r := chunkedFor(body(ctx), class, 5, crc32Name)
			_, err := io.ReadAll(r)
			Expect(err).To(MatchError(wantErr))
			Expect(ask(r, crc32Name)).To(BeZero())
		},
		Entry("a last data chunk that fails its signature", func(ctx context.Context) io.Reader {
			stream := helloWith(ctx, trailerSignedClass, crc32Line, crc32Canon)
			stream[dataIndex(stream, "hello")] = 'j'
			return bytes.NewReader(stream)
		}, trailerSignedClass, op.ErrContentSHA256Mismatch),
		Entry("a malformed final chunk line", func(context.Context) io.Reader {
			return strings.NewReader("5\r\nhello\r\n0x\r\n" + crc32Line + "\r\n")
		}, unsignedClass, op.ErrInvalidArgument),
		Entry("a body that fails after the trailer line, inside the section", func(context.Context) io.Reader {
			stream := unsignedWith("hello", 5, crc32Line)
			return io.MultiReader(bytes.NewReader(stream[:len(stream)-len("\r\n")]), iotest.ErrReader(io.ErrUnexpectedEOF))
		}, unsignedClass, io.ErrUnexpectedEOF),
	)

	DescribeTable("answers not found for a listed name with no line of its own in the section",
		func(ctx SpecContext, stream func(context.Context) []byte, class payloadClass, listed string) {
			r := chunkedFor(bytes.NewReader(stream(ctx)), class, 5, listed)
			readHello(r)
			Expect(ask(r, crc32Name)).To(BeZero())
		},
		Entry("a signed section without it", func(ctx context.Context) []byte {
			return helloWith(ctx, trailerSignedClass, "", "")
		}, trailerSignedClass, crc32Name),
		Entry("an unsigned section without it", func(ctx context.Context) []byte {
			return helloWith(ctx, unsignedClass, "", "")
		}, unsignedClass, crc32Name),
		Entry("its line cut off by the end of the body, before its CRLF", func(context.Context) []byte {
			return []byte("5\r\nhello\r\n0\r\n" + crc32Name + ":" + helloCRC32)
		}, unsignedClass, crc32Name),
		Entry("a section after chunks that run past the decoded length, which is never read", func(context.Context) []byte {
			return unsignedWith("hello world", 11, crc32Line)
		}, unsignedClass, crc32Name),
		Entry("its line, when x-amz-trailer lists another name", func(ctx context.Context) []byte {
			return helloWith(ctx, unsignedClass, crc32Line, "")
		}, unsignedClass, "x-amz-checksum-sha256"),
		Entry("its line, when x-amz-trailer is absent", func(ctx context.Context) []byte {
			return helloWith(ctx, unsignedClass, crc32Line, "")
		}, unsignedClass, ""),
	)

	It("never offers the trailer signature's line as a trailer, even when x-amz-trailer lists its name", func(ctx SpecContext) {
		r := chunkedFor(bytes.NewReader(helloWith(ctx, trailerSignedClass, "", "")), trailerSignedClass, 5, trailerSignatureHeader)
		readHello(r)
		Expect(ask(r, trailerSignatureHeader)).To(BeZero())
	})

	DescribeTable("takes the value of the first line that bears exactly the listed name, untrimmed",
		func(ctx SpecContext, class payloadClass, lines, canonical string, want trailerAnswer) {
			r := chunkedFor(bytes.NewReader(helloWith(ctx, class, lines, canonical)), class, 5, crc32Name)
			readHello(r)
			Expect(ask(r, crc32Name)).To(Equal(want))
		},
		Entry("the first of repeated lines, the one the trailer signature covers", trailerSignedClass,
			crc32Line+fakeLine, crc32Canon, helloCRC),
		Entry("the first of repeated unsigned lines", unsignedClass, crc32Line+fakeLine, "", helloCRC),
		Entry("the whole value after the first colon, blanks and colons kept", unsignedClass,
			crc32Name+": Nh:Cmhg== \r\n", "", trailerAnswer{" Nh:Cmhg== ", true}),
		Entry("an empty value", unsignedClass, crc32Name+":\r\n", "", trailerAnswer{"", true}),
		Entry("past a longer name, a name that ends in it and a line without a colon", unsignedClass,
			crc32Name+"c:AAAAAA==\r\nx"+fakeLine+crc32Name+" AAAAAA==\r\n"+crc32Line, "", helloCRC),
		Entry("past a line whose value holds the name", unsignedClass, "x-amz-meta-a:"+fakeLine+crc32Line, "", helloCRC),
	)

	DescribeTable("looks a name up in any ASCII case, the first listed name with a line answering",
		func(listed, lines, name string, want trailerAnswer) {
			r := chunkedFor(bytes.NewReader(unsignedWith("hello", 5, lines)), unsignedClass, 5, listed)
			readHello(r)
			Expect(ask(r, name)).To(Equal(want))
		},
		Entry("a name listed in mixed case", "X-Amz-Checksum-CRC32", "X-Amz-Checksum-CRC32:"+helloCRC32+"\r\n", crc32Name, helloCRC),
		Entry("a name asked in upper case", crc32Name, crc32Line, "X-AMZ-CHECKSUM-CRC32", helloCRC),
		Entry("two listed spellings, the first listed answering whatever the line order", crc32Name+",X-Amz-Checksum-CRC32",
			"X-Amz-Checksum-CRC32:AAAAAA==\r\n"+crc32Line, crc32Name, helloCRC),
		Entry("two listed spellings, the second answering when the first has no line", crc32Name+",X-Amz-Checksum-CRC32",
			"X-Amz-Checksum-CRC32:AAAAAA==\r\n", crc32Name, trailerAnswer{"AAAAAA==", true}),
		Entry("not across a Unicode case fold", "x-amz-chec\u212asum-crc32", "x-amz-chec\u212asum-crc32:"+helloCRC32+"\r\n",
			crc32Name, trailerAnswer{}),
		Entry("not across '-' and '_'", "x_amz_checksum_crc32", "x_amz_checksum_crc32:"+helloCRC32+"\r\n", crc32Name, trailerAnswer{}),
		Entry("not for an empty name an empty part of x-amz-trailer gives", crc32Name+",", ":"+helloCRC32+"\r\n", "", trailerAnswer{}),
	)
})
