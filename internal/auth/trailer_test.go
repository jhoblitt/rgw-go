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
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(signed), trailerSignedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
		unrequired := withTrailer(stream, wrongCRC+"x-amz-trailer-signature:"+zeroSig+"\r\n\r\n")
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(unrequired), signedClass, 5, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
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
