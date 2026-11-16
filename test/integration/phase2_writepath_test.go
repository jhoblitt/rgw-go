//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// writepathUser owns the write-path gate's buckets. Its name and keys are
// fixed, so a run that stopped partway leaves it for the next run to remove
// first.
const writepathUser = "writepath-gate"

var writepathKey = keyPair{access: "WPGATEEXAMPLEKEY0001", secret: "wpGateExampleSecret" + strings.Repeat("0", 20) + "1"}

// writepathChunk is rgw_max_chunk_size's default, 4 MiB
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L84-L95 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L84-L98 at v20.2.4),
// which neither the chart values nor the parity options change, so both
// gateways cut a write's plaintext into compressor blocks of it.
const writepathChunk = 4 << 20

// writepathCodecs are the codecs populate.sh gives a storage class each on
// STANDARD's data pool.
var writepathCodecs = []string{"zlib", "snappy", "zstd", "lz4"}

// codecClass is the storage class populate.sh compresses with codec.
func codecClass(codec string) string { return "COMP_" + strings.ToUpper(codec) }

// The payload hashes the gate signs a request over: UNSIGNED-PAYLOAD for a
// body sent as it is, STREAMING-UNSIGNED-PAYLOAD-TRAILER for an
// aws-chunked one. Every request that carries a checksum is framed by hand:
// the SDK sends a checksum as a trailer over HTTPS alone, and both
// gateways' endpoints are HTTP.
const (
	unsignedPayloadHash = "UNSIGNED-PAYLOAD"
	unsignedTrailerHash = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
)

// wpGateway is one gateway as the write-path gate reaches it: its S3
// endpoint and an S3 client of writepathUser's against it.
type wpGateway struct {
	name, url string
	s3        *awss3.Client
}

func (g wpGateway) radosgw() bool { return g.name == "radosgw" }

// wpResponse is a response as the wire carried it.
type wpResponse struct {
	status int
	header http.Header
	body   []byte
}

// code is the error code the response's body carries, "" for none.
func (r wpResponse) code() string { return xmlElement(r.body, "Code") }

// wpSend sends method on path, which may carry a query, to g as
// writepathUser with the headers h and body, signed with SigV4 over
// payloadHash.
func wpSend(ctx context.Context, g wpGateway, method, path string, h http.Header, body []byte, payloadHash string) wpResponse {
	GinkgoHelper()
	var rd io.Reader = http.NoBody
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.url+path, rd)
	Expect(err).NotTo(HaveOccurred())
	for k, vs := range h {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	Expect(v4.NewSigner().SignHTTP(ctx, writepathKey.creds(), req, payloadHash, "s3", "us-east-1", time.Now())).To(Succeed())
	resp, err := rawClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s on %s", method, path, g.name)
	b, err := io.ReadAll(resp.Body)
	Expect(errors.Join(err, resp.Body.Close())).NotTo(HaveOccurred(), "reading %s %s on %s", method, path, g.name)
	return wpResponse{status: resp.StatusCode, header: resp.Header, body: b}
}

func wpObjectPath(bucket, key string) string { return "/" + bucket + "/" + key }

func wpPartPath(bucket, key, id string, n int) string {
	return wpObjectPath(bucket, key) + "?partNumber=" + strconv.Itoa(n) + "&uploadId=" + url.QueryEscape(id)
}

func wpCompletePath(bucket, key, id string) string {
	return wpObjectPath(bucket, key) + "?uploadId=" + url.QueryEscape(id)
}

// wpOtherGateway is the gateway that is not name.
func wpOtherGateway(name string) string {
	if name == "radosgw" {
		return "rgw-go"
	}
	return "radosgw"
}

// checksumPut is a PutObject or an UploadPart of body on path through g
// with alg's checksum named by x-amz-sdk-checksum-algorithm, as an SDK sends
// it, and its value in alg's header or, with trailer, in the trailer of an
// unsigned aws-chunked payload that x-amz-trailer lists. radosgw computes a
// checksum only for an algorithm a header names
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73 at v20.2.4),
// and reads the trailers x-amz-trailer lists alone
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1459-L1487 at v20.2.4).
func checksumPut(ctx context.Context, g wpGateway, path string, alg cksumAlgorithm, trailer bool, body []byte) wpResponse {
	GinkgoHelper()
	h := http.Header{"X-Amz-Sdk-Checksum-Algorithm": {alg.name}}
	if !trailer {
		h.Set(alg.valueHeader(), alg.armor(body))
		return wpSend(ctx, g, http.MethodPut, path, h, body, unsignedPayloadHash)
	}
	h.Set("X-Amz-Trailer", alg.valueHeader())
	h.Set("X-Amz-Decoded-Content-Length", strconv.Itoa(len(body)))
	return wpSend(ctx, g, http.MethodPut, path, h, awsChunked(body, 64<<10, alg.valueHeader(), alg.armor(body)), unsignedTrailerHash)
}

// expectWriteChecksum requires w's answer to a write of alg's checksum
// value: x-amz-checksum-type FULL_OBJECT and the value under alg's header.
// radosgw sends them for an AWS type alone
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2914-L2917 at v20.2.4),
// and rgw-go for every type it computes (docs/differences.md, "Writes
// answer a SHA512 checksum under AWS's name").
func expectWriteChecksum(what string, w wpGateway, alg cksumAlgorithm, value string, resp wpResponse) {
	GinkgoHelper()
	if w.radosgw() && !alg.aws {
		Expect(resp.header.Values(alg.valueHeader())).To(BeEmpty(), "%s: radosgw's %s", what, alg.valueHeader())
		Expect(resp.header.Values("X-Amz-Checksum-Type")).To(BeEmpty(), "%s: radosgw's x-amz-checksum-type", what)
		return
	}
	Expect(resp.header.Get(alg.valueHeader())).To(Equal(value), "%s: %s's %s", what, w.name, alg.valueHeader())
	Expect(resp.header.Get("X-Amz-Checksum-Type")).To(Equal("FULL_OBJECT"), "%s: %s's x-amz-checksum-type", what, w.name)
}

// checksumHead is a HEAD of path through g with checksum mode on.
func checksumHead(ctx context.Context, g wpGateway, path string) wpResponse {
	GinkgoHelper()
	r := wpSend(ctx, g, http.MethodHead, path, http.Header{"X-Amz-Checksum-Mode": {"ENABLED"}}, nil, unsignedPayloadHash)
	Expect(r.status).To(Equal(http.StatusOK), "a checksum-mode HEAD of %s through %s", path, g.name)
	return r
}

// attributesChecksum is the Checksum section of a GetObjectAttributes of
// path through g.
func attributesChecksum(ctx context.Context, g wpGateway, path string) string {
	GinkgoHelper()
	r := wpSend(ctx, g, http.MethodGet, path+"?attributes", http.Header{"X-Amz-Object-Attributes": {"Checksum"}}, nil, unsignedPayloadHash)
	Expect(r.status).To(Equal(http.StatusOK), "GetObjectAttributes of %s through %s: %s", path, g.name, r.body)
	return xmlSection(r.body, "Checksum")
}

// checksumSection is GetObjectAttributes' Checksum section for alg's value
// of checksum type typ, as radosgw renders it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4029-L4057 at v20.2.4).
func checksumSection(alg cksumAlgorithm, value, typ string) string {
	return "<Checksum><" + alg.element() + ">" + value + "</" + alg.element() + "><ChecksumType>" + typ + "</ChecksumType></Checksum>"
}

// expectShownChecksum requires g to show alg's value of checksum type typ
// for path, through a checksum-mode HEAD and GetObjectAttributes.
func expectShownChecksum(ctx context.Context, g wpGateway, path string, alg cksumAlgorithm, value, typ string) {
	GinkgoHelper()
	head := checksumHead(ctx, g, path)
	Expect(head.header.Get(alg.readHeader(g.radosgw()))).To(Equal(value), "%s's checksum-mode HEAD of %s", g.name, path)
	Expect(head.header.Get("X-Amz-Checksum-Type")).To(Equal(typ), "%s's checksum-mode HEAD of %s", g.name, path)
	Expect(attributesChecksum(ctx, g, path)).To(Equal(checksumSection(alg, value, typ)), "%s's GetObjectAttributes of %s", g.name, path)
}

// wpCreateUpload begins an upload of key in bucket through g with the
// headers h and returns its id. The upload is aborted through g when the
// spec ends, which a completed upload answers 404.
func wpCreateUpload(ctx context.Context, g wpGateway, bucket, key string, h http.Header) string {
	GinkgoHelper()
	r := wpSend(ctx, g, http.MethodPost, wpObjectPath(bucket, key)+"?uploads", h, nil, unsignedPayloadHash)
	Expect(r.status).To(Equal(http.StatusOK), "CreateMultipartUpload of %s through %s: %s", key, g.name, r.body)
	id := xmlElement(r.body, "UploadId")
	Expect(id).To(MatchRegexp(uploadIDShape), "the upload id CreateMultipartUpload of %s through %s answers: %s", key, g.name, r.body)
	DeferCleanup(func(ctx context.Context) {
		wpSend(ctx, g, http.MethodDelete, wpCompletePath(bucket, key, id), nil, nil, unsignedPayloadHash)
	})
	return id
}

// checksumPart uploads body as part n of upload id through g with alg's
// checksum in a header, which must succeed, and returns its ETag.
func checksumPart(ctx context.Context, g wpGateway, bucket, key, id string, n int, alg cksumAlgorithm, body []byte) string {
	GinkgoHelper()
	r := checksumPut(ctx, g, wpPartPath(bucket, key, id, n), alg, false, body)
	Expect(r.status).To(Equal(http.StatusOK), "UploadPart %d of %s through %s: %s", n, key, g.name, r.body)
	Expect(r.header.Get("ETag")).To(Equal(`"`+md5Hex(body)+`"`), "part %d of %s through %s", n, key, g.name)
	Expect(r.header.Get(alg.valueHeader())).To(Equal(alg.armor(body)), "part %d of %s through %s", n, key, g.name)
	return r.header.Get("ETag")
}

// wpUpload is a multipart upload the gate drives by hand: the gateway that
// began it, its id and the ETags of its parts in order.
type wpUpload struct {
	begin           wpGateway
	bucket, key, id string
	etags           []string
}

// wpComplete sends CompleteMultipartUpload of u through g, naming its parts
// in order, with the headers h.
func wpComplete(ctx context.Context, g wpGateway, u wpUpload, h http.Header) wpResponse {
	GinkgoHelper()
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for i, e := range u.etags {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, e)
	}
	b.WriteString("</CompleteMultipartUpload>")
	return wpSend(ctx, g, http.MethodPost, wpCompletePath(u.bucket, u.key, u.id), h, []byte(b.String()), unsignedPayloadHash)
}

// wpUploadAcross begins the upload "by-<completer>" through the gateway that
// is not completer, with the headers h, and uploads parts with alg's checksum
// in a header: the odd-numbered through the gateway that began it and the
// even-numbered through completer, so that each gateway writes parts of an
// upload the other completes. The completion is the caller's.
func wpUploadAcross(ctx context.Context, gateways map[string]wpGateway, completer, bucket string, h http.Header, alg cksumAlgorithm, parts ...[]byte) wpUpload {
	GinkgoHelper()
	begin := gateways[wpOtherGateway(completer)]
	u := wpUpload{begin: begin, bucket: bucket, key: "by-" + completer}
	u.id = wpCreateUpload(ctx, begin, bucket, u.key, h)
	for i, body := range parts {
		via := begin
		if i%2 == 1 {
			via = gateways[completer]
		}
		u.etags = append(u.etags, checksumPart(ctx, via, bucket, u.key, u.id, i+1, alg, body))
	}
	return u
}

// expectCompleted requires r to be a completion's answer for an object of
// parts with alg's value of checksum type typ.
func expectCompleted(what string, r wpResponse, parts [][]byte, alg cksumAlgorithm, value, typ string) {
	GinkgoHelper()
	Expect(r.status).To(Equal(http.StatusOK), "%s: %s", what, r.body)
	Expect(xmlElement(r.body, "ETag")).To(Equal(`"`+mpETag(parts...)+`"`), "%s: ETag", what)
	Expect(xmlElement(r.body, alg.element())).To(Equal(value), "%s: %s", what, alg.element())
	Expect(xmlElement(r.body, "ChecksumType")).To(Equal(typ), "%s: ChecksumType", what)
}

// wpBucket creates a bucket of writepathUser's through radosgw.
// writepathUser's removal removes it.
func wpBucket(ctx context.Context, radosgw wpGateway) string {
	GinkgoHelper()
	name := randName("wp")
	_, err := radosgw.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	return name
}

// wpHeadOf is the head object of key in bucket.
func wpHeadOf(ctx context.Context, bucket, key string) string {
	GinkgoHelper()
	rec, err := store.GetBucket(ctx, "", bucket)
	Expect(err).NotTo(HaveOccurred(), "GetBucket %s", bucket)
	return keyHead(rec, key)
}

// wpPartHeadOf is the head object of part n of upload id of key in bucket,
// its name in the multipart namespace (RGWMPObj::get_part).
func wpPartHeadOf(ctx context.Context, bucket, key, id string, n uint32) string {
	GinkgoHelper()
	rec, err := store.GetBucket(ctx, "", bucket)
	Expect(err).NotTo(HaveOccurred(), "GetBucket %s", bucket)
	name := meta.MultipartPartName(meta.MultipartPrefix(key, id), n)
	return meta.Stripe{Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: name, NS: meta.NSMultipart}}}.OID()
}

// cksumAttr is the user.rgw.cksum oid carries, as RADOS holds it.
func cksumAttr(ctx context.Context, oid string) []byte {
	GinkgoHelper()
	attrs := rawXattrs(ctx, oid)
	Expect(attrs).To(HaveKey(meta.AttrCksum), "%s's checksum attr", oid)
	return attrs[meta.AttrCksum]
}

// expectCksumRecord requires attr to be the record of alg's digest with
// flags.
func expectCksumRecord(what string, attr []byte, alg cksumAlgorithm, digest []byte, flags uint16) {
	GinkgoHelper()
	rec, err := parseCksumRecord(attr)
	Expect(err).NotTo(HaveOccurred(), what)
	Expect(rec).To(Equal(cksumRecord{Type: alg.id, Digest: digest, Flags: flags}), what)
}

// wpPart is one part of an upload the gate writes and the gateway it goes
// through.
type wpPart struct {
	via  wpGateway
	body []byte
}

// wpPutIn writes body as key into class through g's PutObject.
func wpPutIn(ctx context.Context, g wpGateway, bucket, key, class string, body []byte) {
	GinkgoHelper()
	_, err := g.s3.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), StorageClass: s3types.StorageClass(class),
	})
	Expect(err).NotTo(HaveOccurred(), "PutObject %s/%s into %s through %s", bucket, key, class, g.name)
}

// wpUploadIn writes key into class as an upload begun through begin, each
// part through its gateway and completed through complete, and returns the
// parts' bodies. The upload is aborted through begin when the spec ends,
// which a completed upload answers 404.
func wpUploadIn(ctx context.Context, begin wpGateway, bucket, key, class string, parts []wpPart, complete wpGateway) [][]byte {
	GinkgoHelper()
	id := createUpload(ctx, begin.s3, bucket, key, s3types.StorageClass(class))
	DeferCleanup(func(ctx context.Context) {
		wpSend(ctx, begin, http.MethodDelete, wpCompletePath(bucket, key, id), nil, nil, unsignedPayloadHash)
	})
	var completed []s3types.CompletedPart
	var bodies [][]byte
	for i, p := range parts {
		By(fmt.Sprintf("uploading part %d of %s through %s", i+1, key, p.via.name))
		completed = append(completed, uploadPart(ctx, p.via.s3, bucket, key, id, int32(i+1), p.body)) //nolint:gosec // a handful of parts
		bodies = append(bodies, p.body)
	}
	By(fmt.Sprintf("completing %s through %s", key, complete.name))
	mustComplete(ctx, complete.s3, bucket, key, id, completed, bodies...)
	return bodies
}

// wpExpectRange requires a GET of the range rng of key through g to answer
// those bytes of body.
func wpExpectRange(ctx context.Context, g wpGateway, bucket, key, rng string, body []byte) {
	GinkgoHelper()
	out, err := g.s3.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Range: aws.String(rng)})
	Expect(err).NotTo(HaveOccurred(), "GetObject %s of %s/%s through %s", rng, bucket, key, g.name)
	got, err := io.ReadAll(out.Body)
	Expect(errors.Join(err, out.Body.Close())).NotTo(HaveOccurred(), "reading %s of %s through %s", rng, key, g.name)
	first, last := rangeBounds(rng, len(body))
	Expect(aws.ToString(out.ContentRange)).To(Equal(fmt.Sprintf("bytes %d-%d/%d", first, last, len(body))), "%s's Content-Range for %s", g.name, rng)
	Expect(got).To(HaveLen(last-first+1), "the bytes %s read of %s", g.name, rng)
	Expect(md5Hex(got)).To(Equal(md5Hex(body[first:last+1])), "the MD5 of what %s read of %s", g.name, rng)
}

// tenMiBRanges are ranges of a 10 MiB object of 4 MiB blocks: inside the
// first block and inside the second, across the first boundary, across
// both, a suffix inside the last block and an open end across the second
// boundary.
var tenMiBRanges = []string{
	"bytes=1000-1999",
	"bytes=5242880-5246975",
	"bytes=4194294-4194313",
	"bytes=1048576-9437183",
	"bytes=-100",
	"bytes=8388600-",
}

// wpOldOffsets is the plaintext offset of each of c's blocks.
func wpOldOffsets(c meta.CompressionInfo) []uint64 {
	out := make([]uint64, len(c.Blocks))
	for i, b := range c.Blocks {
		out[i] = b.OldOfs
	}
	return out
}

// expectBlockMap requires c to compress with codec an object of parts of
// the given plaintext sizes, stored in stored bytes, as radosgw lays a
// block map out: a block for each chunk of each part, back to back.
func expectBlockMap(what string, c meta.CompressionInfo, codec string, stored uint64, parts ...uint64) {
	GinkgoHelper()
	var total uint64
	for _, p := range parts {
		total += p
	}
	Expect(c.Type).To(Equal(codec), "%s: compression_type", what)
	Expect(c.OrigSize).To(Equal(total), "%s: orig_size", what)
	Expect(wpOldOffsets(c)).To(Equal(blockOffsets(writepathChunk, parts...)), "%s: the blocks' plaintext offsets, a block a chunk", what)
	expectBlocksTile(what, c, stored)
}

// expectIndexed requires bucket list to account key at its plaintext size
// and to store it at the manifest's size.
func expectIndexed(ctx context.Context, bucket, key string, plaintext int, o adminObject) {
	GinkgoHelper()
	Expect(bucketList(ctx, bucket)).To(HaveKeyWithValue(key, And(
		HaveField("Meta.AccountedSize", BeEquivalentTo(plaintext)),
		HaveField("Meta.Size", Equal(o.manifest().ObjSize)),
	)), "bucket list's entry of %s", key)
}

// skipOnSquid skips a spec of Tentacle's checksums on a Squid cluster.
func skipOnSquid() {
	if store.Release() < denc.Tentacle {
		Skip("Squid's radosgw has no checksum code: v19.2.7 has no src/rgw/rgw_cksum.h")
	}
}

// The write-path gate writes compressed and checksummed objects through each
// gateway and reads them through the other, and holds what each stores to
// what radosgw stores for the same request, as radosgw-admin object stat,
// bucket stats and bucket check show it and as RADOS holds it.
var _ = Describe("the write-path gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "writepath"), func() {
	var gateways map[string]wpGateway

	BeforeAll(func(ctx SpecContext) {
		removeUserFixtures(ctx, writepathUser)
		DeferCleanup(removeUserFixtures, writepathUser)
		createUser(ctx, writepathUser, writepathKey)
		for _, codec := range writepathCodecs {
			Expect(manifest.StorageClasses).To(HaveKeyWithValue(codecClass(codec), codec), "populate.sh's storage class for %s", codec)
		}
		base := rgwgoAdminEndpoint()
		gateways = map[string]wpGateway{
			"radosgw": {name: "radosgw", url: endpoint, s3: s3At(endpoint, writepathKey)},
			"rgw-go":  {name: "rgw-go", url: base, s3: s3At(base, writepathKey)},
		}
	})

	Describe("compression", func() {
		DescribeTable("serves a 10 MiB PUT into a codec's class through the other gateway, a block a chunk",
			func(ctx SpecContext, codec, writer string) {
				w, rd := gateways[writer], gateways[wpOtherGateway(writer)]
				bucket, class := wpBucket(ctx, gateways["radosgw"]), codecClass(codec)
				body := compressibleBody(10<<20, 11)
				wpPutIn(ctx, w, bucket, "obj", class, body)

				expectReads(ctx, rd.s3, rd.name, bucket, "obj", md5Hex(body), body)
				for _, rng := range tenMiBRanges {
					wpExpectRange(ctx, rd, bucket, "obj", rng, body)
				}
				o := objectStat(ctx, bucket, "obj")
				Expect(o.Attrs).To(HaveKeyWithValue(meta.AttrStorageClass, class), "object stat's storage class")
				expectBlockMap(writer+"'s object", o.compression(), codec, o.manifest().ObjSize, 10<<20)
				Expect(o.manifest().ObjSize).To(BeNumerically("<", len(body)), "the manifest's size, the stored size")
				expectIndexed(ctx, bucket, "obj", len(body), o)
				expectBucketCheckClean(ctx, bucket)
			},
			Entry("zlib, written by rgw-go", "zlib", "rgw-go"),
			Entry("zlib, written by radosgw", "zlib", "radosgw"),
			Entry("snappy, written by rgw-go", "snappy", "rgw-go"),
			Entry("snappy, written by radosgw", "snappy", "radosgw"),
			Entry("zstd, written by rgw-go", "zstd", "rgw-go"),
			Entry("zstd, written by radosgw", "zstd", "radosgw"),
			Entry("lz4, written by rgw-go", "lz4", "rgw-go"),
			Entry("lz4, written by radosgw", "lz4", "radosgw"),
		)

		// zlib, as its compressor_message is the one a completion holds every
		// part to.
		DescribeTable("completes an upload into a compressing class with parts from both gateways, read through both",
			func(ctx SpecContext, completer string) {
				c, s := gateways[completer], gateways[wpOtherGateway(completer)]
				bucket, class := wpBucket(ctx, gateways["radosgw"]), codecClass("zlib")
				parts := []wpPart{
					{via: s, body: compressibleBody(5<<20, 21)},
					{via: c, body: compressibleBody(5<<20, 22)},
					{via: s, body: compressibleBody(1<<20, 23)},
				}
				bodies := wpUploadIn(ctx, s, bucket, "mp", class, parts, c)
				whole := bytes.Join(bodies, nil)

				for _, g := range gateways {
					expectReads(ctx, g.s3, g.name, bucket, "mp", mpETag(bodies...), whole)
					wpExpectRange(ctx, g, bucket, "mp", "bytes=5242870-5242889", whole)
					wpExpectRange(ctx, g, bucket, "mp", "bytes=9437180-9437199", whole)
				}
				o := objectStat(ctx, bucket, "mp")
				expectBlockMap("the upload "+completer+" completed", o.compression(), "zlib", o.manifest().ObjSize, 5<<20, 5<<20, 1<<20)
				expectIndexed(ctx, bucket, "mp", len(whole), o)
				expectBucketCheckClean(ctx, bucket)
			},
			Entry("completed by rgw-go", "rgw-go"),
			Entry("completed by radosgw", "radosgw"),
		)
	})

	Describe("Tentacle's checksums", func() {
		BeforeEach(skipOnSquid)

		DescribeTable("shows a checksummed PutObject through the other gateway, from the record radosgw stores",
			func(ctx SpecContext, name string, trailer bool) {
				alg := wpAlgorithm(name)
				bucket := wpBucket(ctx, gateways["radosgw"])
				body := randBytes(1<<20 + 333)
				value := alg.armor(body)
				attrs := map[string][]byte{}
				for writer, w := range gateways {
					path := wpObjectPath(bucket, "by-"+writer)
					resp := checksumPut(ctx, w, path, alg, trailer, body)
					Expect(resp.status).To(Equal(http.StatusOK), "PutObject through %s: %s", writer, resp.body)
					expectWriteChecksum("PutObject through "+writer, w, alg, value, resp)
					expectShownChecksum(ctx, gateways[wpOtherGateway(writer)], path, alg, value, "FULL_OBJECT")

					attrs[writer] = cksumAttr(ctx, wpHeadOf(ctx, bucket, "by-"+writer))
					Expect(objectStat(ctx, bucket, "by-"+writer).Attrs).To(HaveKeyWithValue(meta.AttrCksum, cstring(attrs[writer])),
						"object stat's %s of the object %s wrote", meta.AttrCksum, writer)
				}
				// A PutObject's record keeps the put filter's flags, 0
				// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L187-L191 at v20.2.4).
				expectCksumRecord("radosgw's record", attrs["radosgw"], alg, alg.hash(body), 0)
				Expect(attrs["rgw-go"]).To(Equal(attrs["radosgw"]), "the %s rgw-go stores against radosgw's", meta.AttrCksum)
				expectBucketCheckClean(ctx, bucket)
			},
			Entry("CRC32 in a header", "CRC32", false),
			Entry("CRC32 in a trailer", "CRC32", true),
			Entry("CRC32C in a header", "CRC32C", false),
			Entry("CRC32C in a trailer", "CRC32C", true),
			Entry("CRC64NVME in a header", "CRC64NVME", false),
			Entry("CRC64NVME in a trailer", "CRC64NVME", true),
			Entry("SHA1 in a header", "SHA1", false),
			Entry("SHA1 in a trailer", "SHA1", true),
			Entry("SHA256 in a header", "SHA256", false),
			Entry("SHA256 in a trailer", "SHA256", true),
			Entry("SHA512 in a header", "SHA512", false),
			Entry("SHA512 in a trailer", "SHA512", true),
		)

		// Each upload is begun on one gateway, its first part goes through it
		// and its second through the other, which completes it; the gateway
		// that began it is then sent the completion again. Both uploads of an
		// entry carry the same parts, so the combined records compare whole.
		DescribeTable("combines an upload one gateway began as the other completes it, and answers its retried completion",
			func(ctx SpecContext, name, checksumType string) {
				alg := wpAlgorithm(name)
				bucket := wpBucket(ctx, gateways["radosgw"])
				parts := [][]byte{randBytes(5 << 20), randBytes(1<<20 + 7)}
				whole := bytes.Join(parts, nil)
				value, digest, flags := alg.armor(whole), alg.hash(whole), uint16(cksumFlagV2|cksumFlagCombined|cksumFlagFullObject)
				partFlags := uint16(cksumFlagFullObject)
				supplied := http.Header{alg.valueHeader(): {value}}
				if checksumType == "COMPOSITE" {
					composite, err := base64.StdEncoding.DecodeString(alg.composite(parts...))
					Expect(err).NotTo(HaveOccurred())
					value, digest, flags = alg.composite(parts...)+"-2", composite, cksumFlagV2|cksumFlagCombined|cksumFlagComposite
					partFlags, supplied = cksumFlagComposite, http.Header{}
				}

				records := map[string][]byte{}
				partRecords := map[string][2][]byte{}
				for completer, c := range gateways {
					u := wpUploadAcross(ctx, gateways, completer, bucket, http.Header{"X-Amz-Checksum-Algorithm": {name}, "X-Amz-Checksum-Type": {checksumType}}, alg, parts...)
					expectCompleted(completer+"'s completion", wpComplete(ctx, c, u, supplied), parts, alg, value, checksumType)
					expectCompleted(u.begin.name+"'s answer to the retried completion", wpComplete(ctx, u.begin, u, supplied), parts, alg, value, checksumType)
					for _, g := range gateways {
						expectShownChecksum(ctx, g, wpObjectPath(bucket, u.key), alg, value, checksumType)
					}
					records[completer] = cksumAttr(ctx, wpHeadOf(ctx, bucket, u.key))
					partRecords[completer] = [2][]byte{
						cksumAttr(ctx, wpPartHeadOf(ctx, bucket, u.key, u.id, 1)),
						cksumAttr(ctx, wpPartHeadOf(ctx, bucket, u.key, u.id, 2)),
					}
				}
				expectCksumRecord("radosgw's combined record", records["radosgw"], alg, digest, flags)
				Expect(records["rgw-go"]).To(Equal(records["radosgw"]), "the combined %s rgw-go stores against radosgw's", meta.AttrCksum)
				// A part's record keeps its upload's flags
				// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472 at v20.2.4).
				// Part n of one upload went through the gateway part n of the other
				// did not.
				for i := range parts {
					expectCksumRecord(fmt.Sprintf("part %d's record in the upload radosgw completed", i+1), partRecords["radosgw"][i], alg, alg.hash(parts[i]), partFlags)
					Expect(partRecords["rgw-go"][i]).To(Equal(partRecords["radosgw"][i]), "part %d's %s, written by each gateway", i+1, meta.AttrCksum)
				}
				expectBucketCheckClean(ctx, bucket)
			},
			Entry("a COMPOSITE SHA256 upload", "SHA256", "COMPOSITE"),
			Entry("a FULL_OBJECT CRC32 upload", "CRC32", "FULL_OBJECT"),
		)

		// The checksums docs/ceph-upstream-bugs.md records radosgw as getting
		// wrong, which rgw-go does not reproduce, pinned on each gateway. Each
		// upload is begun on one gateway and completed by the other.
		Describe("where radosgw's combination is wrong", func() {
			It("stores a COMPOSITE CRC32 upload's composite through rgw-go and a full-object CRC through radosgw", func(ctx SpecContext) {
				alg := wpAlgorithm("CRC32")
				bucket := wpBucket(ctx, gateways["radosgw"])
				parts := [][]byte{randBytes(5 << 20), randBytes(1 << 20)}
				composite := alg.composite(parts...)
				shown := map[string]struct {
					value, typ string
					flags      uint16
				}{
					// docs/differences.md, "A multipart upload's combined CRC
					// follows its checksum type".
					"rgw-go": {composite + "-2", "COMPOSITE", cksumFlagV2 | cksumFlagCombined | cksumFlagComposite},
					// docs/ceph-upstream-bugs.md, "radosgw stores a COMPOSITE
					// CRC upload's checksum as a full-object CRC".
					"radosgw": {alg.armor(bytes.Join(parts, nil)), "FULL_OBJECT", cksumFlagV2 | cksumFlagCombined | cksumFlagFullObject},
				}
				for completer, c := range gateways {
					u := wpUploadAcross(ctx, gateways, completer, bucket, http.Header{"X-Amz-Checksum-Algorithm": {"CRC32"}, "X-Amz-Checksum-Type": {"COMPOSITE"}}, alg, parts...)
					want := shown[completer]
					expectCompleted(completer+"'s completion", wpComplete(ctx, c, u, nil), parts, alg, want.value, want.typ)
					for _, g := range gateways {
						expectShownChecksum(ctx, g, wpObjectPath(bucket, u.key), alg, want.value, want.typ)
					}
					digest, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(want.value, "-2"))
					Expect(err).NotTo(HaveOccurred())
					expectCksumRecord(completer+"'s combined record", cksumAttr(ctx, wpHeadOf(ctx, bucket, u.key)), alg, digest, want.flags)
				}
				expectBucketCheckClean(ctx, bucket)
			})

			It("stores a one-part CRC32 upload as FULL_OBJECT through rgw-go and as a composite through radosgw", func(ctx SpecContext) {
				alg := wpAlgorithm("CRC32")
				bucket := wpBucket(ctx, gateways["radosgw"])
				part := randBytes(1<<20 + 3)
				crc := alg.armor(part)
				shown := map[string]struct {
					answered, value, typ string
					flags                uint16
				}{
					"rgw-go": {crc, crc, "FULL_OBJECT", cksumFlagV2 | cksumFlagCombined | cksumFlagFullObject},
					// docs/ceph-upstream-bugs.md, "radosgw reports a single-part
					// CRC upload's full-object checksum as COMPOSITE": the
					// completion answers the bare CRC, typed COMPOSITE, and the
					// reads add the part count.
					"radosgw": {crc, crc + "-1", "COMPOSITE", cksumFlagCombined | cksumFlagFullObject},
				}
				for completer, c := range gateways {
					u := wpUploadAcross(ctx, gateways, completer, bucket, http.Header{"X-Amz-Checksum-Algorithm": {"CRC32"}}, alg, part)
					want := shown[completer]
					expectCompleted(completer+"'s completion", wpComplete(ctx, c, u, nil), [][]byte{part}, alg, want.answered, want.typ)
					for _, g := range gateways {
						expectShownChecksum(ctx, g, wpObjectPath(bucket, u.key), alg, want.value, want.typ)
					}
					expectCksumRecord(completer+"'s combined record", cksumAttr(ctx, wpHeadOf(ctx, bucket, u.key)), alg, alg.hash(part), want.flags)
				}
				expectBucketCheckClean(ctx, bucket)
			})

			// docs/ceph-upstream-bugs.md, "radosgw does not verify a CRC64NVME
			// or xxh3 upload's part sent without an algorithm header" and
			// "radosgw's CompleteMultipartUpload never compares a supplied
			// CRC64NVME checksum".
			It("verifies the CRC64NVME values radosgw takes unverified, a part's lone value and a completion's", func(ctx SpecContext) {
				alg := wpAlgorithm("CRC64NVME")
				radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
				bucket := wpBucket(ctx, radosgw)
				parts := [][]byte{randBytes(5 << 20), randBytes(1 << 20)}
				wrong := alg.armor([]byte("not the part"))
				id := wpCreateUpload(ctx, radosgw, bucket, "obj", http.Header{"X-Amz-Checksum-Algorithm": {"CRC64NVME"}})
				lone := func(g wpGateway, n int, value string) wpResponse {
					GinkgoHelper()
					return wpSend(ctx, g, http.MethodPut, wpPartPath(bucket, "obj", id, n), http.Header{alg.valueHeader(): {value}}, parts[n-1], unsignedPayloadHash)
				}

				r := lone(rgwgo, 1, wrong)
				Expect([]any{r.status, r.code()}).To(Equal([]any{http.StatusBadRequest, "BadDigest"}), "rgw-go's part 1 under a wrong lone value: %s", r.body)
				r = lone(radosgw, 1, wrong)
				Expect(r.status).To(Equal(http.StatusOK), "radosgw's part 1 under a wrong lone value: %s", r.body)
				r = lone(rgwgo, 2, alg.armor(parts[1]))
				Expect(r.status).To(Equal(http.StatusOK), "rgw-go's part 2 under its own value: %s", r.body)
				u := wpUpload{begin: radosgw, bucket: bucket, key: "obj", id: id, etags: []string{`"` + md5Hex(parts[0]) + `"`, `"` + md5Hex(parts[1]) + `"`}}

				supplied := http.Header{alg.valueHeader(): {wrong}}
				r = wpComplete(ctx, rgwgo, u, supplied)
				Expect([]any{r.status, r.code()}).To(Equal([]any{http.StatusBadRequest, "BadDigest"}), "rgw-go's completion under a wrong value: %s", r.body)
				value := alg.armor(bytes.Join(parts, nil))
				expectCompleted("radosgw's completion under a wrong value", wpComplete(ctx, radosgw, u, supplied), parts, alg, value, "FULL_OBJECT")
				for _, g := range gateways {
					expectShownChecksum(ctx, g, wpObjectPath(bucket, "obj"), alg, value, "FULL_OBJECT")
				}
				expectBucketCheckClean(ctx, bucket)
			})
		})
	})

	// Squid's radosgw has no checksum code and ignores user.rgw.cksum, and on
	// either release its COPY directive carries the attr to the copy
	// (set_copy_attrs,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3705-L3732 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3858-L3885 at v20.2.4).
	It("serves rgw-go's checksummed PutObject whole through radosgw, whose copy keeps its record", func(ctx SpecContext) {
		alg := wpAlgorithm("SHA256")
		radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
		bucket := wpBucket(ctx, radosgw)
		body := randBytes(1<<20 + 5)
		value := alg.armor(body)
		resp := checksumPut(ctx, rgwgo, wpObjectPath(bucket, "src"), alg, false, body)
		Expect(resp.status).To(Equal(http.StatusOK), "rgw-go's PutObject: %s", resp.body)
		expectWriteChecksum("rgw-go's PutObject", rgwgo, alg, value, resp)
		src := cksumAttr(ctx, wpHeadOf(ctx, bucket, "src"))
		expectCksumRecord("rgw-go's record", src, alg, alg.hash(body), 0)

		expectReads(ctx, radosgw.s3, radosgw.name, bucket, "src", md5Hex(body), body)
		_, err := radosgw.s3.CopyObject(ctx, &awss3.CopyObjectInput{
			Bucket: aws.String(bucket), Key: aws.String("copy"), CopySource: aws.String(bucket + "/src"),
		})
		Expect(err).NotTo(HaveOccurred(), "radosgw's CopyObject")
		Expect(cksumAttr(ctx, wpHeadOf(ctx, bucket, "copy"))).To(Equal(src), "the %s of radosgw's copy", meta.AttrCksum)
		expectReads(ctx, rgwgo.s3, rgwgo.name, bucket, "copy", md5Hex(body), body)
		expectShownChecksum(ctx, rgwgo, wpObjectPath(bucket, "copy"), alg, value, "FULL_OBJECT")
		if store.Release() < denc.Tentacle {
			head := checksumHead(ctx, radosgw, wpObjectPath(bucket, "copy"))
			Expect(head.header.Values(alg.valueHeader())).To(BeEmpty(), "Squid's radosgw shows no checksum")
			Expect(head.header.Values("X-Amz-Checksum-Type")).To(BeEmpty(), "Squid's radosgw shows no checksum type")
		} else {
			expectShownChecksum(ctx, radosgw, wpObjectPath(bucket, "copy"), alg, value, "FULL_OBJECT")
		}
		expectBucketCheckClean(ctx, bucket)
	})

	Describe("copies", func() {
		It("serves through both gateways rgw-go's streamed copy of a radosgw-compressed source, its stored bytes kept", func(ctx SpecContext) {
			radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
			bucket := wpBucket(ctx, radosgw)
			body := compressibleBody(10<<20, 51)
			wpPutIn(ctx, radosgw, bucket, "src", codecClass("zstd"), body)
			// Another storage class than the source's, so the copy cannot
			// share its tails and streams.
			_, err := rgwgo.s3.CopyObject(ctx, &awss3.CopyObjectInput{
				Bucket: aws.String(bucket), Key: aws.String("copy"), CopySource: aws.String(bucket + "/src"),
				StorageClass: s3types.StorageClassStandard,
			})
			Expect(err).NotTo(HaveOccurred(), "rgw-go's CopyObject")

			so, co := objectStat(ctx, bucket, "src"), objectStat(ctx, bucket, "copy")
			Expect(co.manifest().Prefix).NotTo(Equal(so.manifest().Prefix), "the copy's tails are its own")
			Expect(canonical(co.Compression)).To(Equal(canonical(so.Compression)), "the copy's compression info against the source's")
			Expect(co.manifest().ObjSize).To(Equal(so.manifest().ObjSize), "the copy's stored size against the source's")
			for _, g := range gateways {
				expectReads(ctx, g.s3, g.name, bucket, "copy", md5Hex(body), body)
				wpExpectRange(ctx, g, bucket, "copy", "bytes=4194294-4194313", body)
			}
			expectIndexed(ctx, bucket, "copy", len(body), co)
			expectBucketCheckClean(ctx, bucket)
		})

		// Into another compressing class: a Tentacle radosgw's copy into a
		// class that does not compress accounts the source's compressed size
		// (docs/ceph-upstream-bugs.md, "Tentacle's copy of a compressed source
		// into a class that does not compress it accounts the compressed
		// size").
		It("serves through both gateways radosgw's copy of an rgw-go-compressed source", func(ctx SpecContext) {
			radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
			bucket := wpBucket(ctx, radosgw)
			body := compressibleBody(10<<20, 52)
			wpPutIn(ctx, rgwgo, bucket, "src", codecClass("zlib"), body)
			_, err := radosgw.s3.CopyObject(ctx, &awss3.CopyObjectInput{
				Bucket: aws.String(bucket), Key: aws.String("copy"), CopySource: aws.String(bucket + "/src"),
				StorageClass: s3types.StorageClass(codecClass("lz4")),
			})
			Expect(err).NotTo(HaveOccurred(), "radosgw's CopyObject")

			so, co := objectStat(ctx, bucket, "src"), objectStat(ctx, bucket, "copy")
			if store.Release() < denc.Tentacle {
				// Squid's copy_obj_data copies the stored bytes and keeps the
				// source's compression info
				// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4823-L4825 at v19.2.7).
				Expect(canonical(co.Compression)).To(Equal(canonical(so.Compression)), "Squid's copy's compression info against the source's")
			} else {
				// Tentacle's RGWCopyObjDPF decodes the source and encodes the
				// copy with the destination's codec
				// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5770-L5844 at v20.2.4),
				// in blocks its read sizes, which need not be chunks.
				c := co.compression()
				Expect(c.Type).To(Equal("lz4"), "Tentacle's copy's codec")
				Expect(c.OrigSize).To(BeEquivalentTo(len(body)), "Tentacle's copy's orig_size")
				expectBlocksTile("Tentacle's copy", c, co.manifest().ObjSize)
			}
			for _, g := range gateways {
				expectReads(ctx, g.s3, g.name, bucket, "copy", md5Hex(body), body)
				wpExpectRange(ctx, g, bucket, "copy", "bytes=4194294-4194313", body)
			}
			expectIndexed(ctx, bucket, "copy", len(body), co)
			expectBucketCheckClean(ctx, bucket)
		})
	})

	// The types main added past v20.2.4's table, MD5 9, XXHASH64 10 and
	// XXHASH128 11 (https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L41-L56
	// at main), which a v20.2.4 radosgw would index its nine-entry table past
	// its end to read
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L147-L149 at v20.2.4).
	DescribeTable("refuses a checksum algorithm a v20.2.4 radosgw has no type for, and stores nothing",
		func(ctx SpecContext, name string, digestSize int, form string) {
			radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
			bucket := wpBucket(ctx, radosgw)
			body := randBytes(1 << 10)
			header := "x-amz-checksum-" + strings.ToLower(name)
			value := base64.StdEncoding.EncodeToString(make([]byte, digestSize))
			path := wpObjectPath(bucket, "refused")
			var resp wpResponse
			switch form {
			case "algorithm":
				resp = wpSend(ctx, rgwgo, http.MethodPut, path, http.Header{"X-Amz-Sdk-Checksum-Algorithm": {name}, header: {value}}, body, unsignedPayloadHash)
			case "value":
				resp = wpSend(ctx, rgwgo, http.MethodPut, path, http.Header{header: {value}}, body, unsignedPayloadHash)
			case "trailer":
				h := http.Header{"X-Amz-Trailer": {header}, "X-Amz-Decoded-Content-Length": {strconv.Itoa(len(body))}}
				resp = wpSend(ctx, rgwgo, http.MethodPut, path, h, awsChunked(body, 64<<10, header, value), unsignedTrailerHash)
			}
			Expect([]any{resp.status, resp.code()}).To(Equal([]any{http.StatusBadRequest, "InvalidArgument"}), "rgw-go's answer: %s", resp.body)

			head := wpSend(ctx, radosgw, http.MethodHead, path, nil, nil, unsignedPayloadHash)
			Expect(head.status).To(Equal(http.StatusNotFound), "radosgw's HEAD of the refused key")
			_, err := rawStat(ctx, wpHeadOf(ctx, bucket, "refused"))
			Expect(err).To(MatchError(radosclient.ErrNotFound), "the refused key's head in RADOS")
			rec, err := store.GetBucket(ctx, "", bucket)
			Expect(err).NotTo(HaveOccurred())
			Expect(markerOIDs(ctx, rec.Info.Bucket.Marker)).To(BeEmpty(), "the bucket's data after the refusal")
			Expect(bucketList(ctx, bucket)).To(BeEmpty(), "the bucket's index after the refusal")
			expectBucketCheckClean(ctx, bucket)
		},
		Entry("MD5 named by the algorithm header", "MD5", 16, "algorithm"),
		Entry("XXHASH64 named by the algorithm header", "XXHASH64", 8, "algorithm"),
		Entry("XXHASH128 named by the algorithm header", "XXHASH128", 16, "algorithm"),
		Entry("MD5 named by its value header alone", "MD5", 16, "value"),
		Entry("XXHASH64 named by the trailer x-amz-trailer lists", "XXHASH64", 8, "trailer"),
	)

	// S3 shows no trace of compression, so bucket stats and object stat are
	// the only witnesses a client has. The same body goes through each
	// gateway into a bucket of its own; the codecs' encoders differ, so the
	// stored bytes may too.
	DescribeTable("accounts each gateway's compressed object alike, as bucket stats and object stat show them",
		func(ctx SpecContext, codec string, parts []uint64) {
			class := codecClass(codec)
			var size uint64
			for _, p := range parts {
				size += p
			}
			Expect(roundedSize(size)).To(BeNumerically(">", size), "a body off a 4 KiB multiple, for size_actual's rounding")
			body := compressibleBody(int(size), 71)
			type written struct {
				stat    adminObject
				storage adminUsage
			}
			got := map[string]written{}
			for writer, w := range gateways {
				bucket := wpBucket(ctx, gateways["radosgw"])
				etag := md5Hex(body)
				if len(parts) == 1 {
					wpPutIn(ctx, w, bucket, "obj", class, body)
				} else {
					split := []wpPart{{via: w, body: body[:parts[0]]}, {via: w, body: body[parts[0]:]}}
					etag = mpETag(wpUploadIn(ctx, w, bucket, "obj", class, split, w)...)
				}
				for _, g := range gateways {
					head, err := g.s3.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("obj")})
					Expect(err).NotTo(HaveOccurred(), "HeadObject through %s", g.name)
					Expect(aws.ToInt64(head.ContentLength)).To(BeEquivalentTo(len(body)), "%s's HEAD of %s's object", g.name, writer)
					expectReads(ctx, g.s3, g.name, bucket, "obj", etag, body)
				}
				o, s := objectStat(ctx, bucket, "obj"), bucketUsage(ctx, bucket)
				c := o.compression()
				expectBlockMap(writer+"'s object", c, codec, o.manifest().ObjSize, parts...)
				Expect(s).To(HaveField("NumObjects", BeEquivalentTo(1)), "%s's bucket's object count", writer)
				Expect(s.Size).To(BeEquivalentTo(len(body)), "%s's bucket's size, the plaintext's", writer)
				Expect(s.SizeActual).To(Equal(roundedSize(uint64(len(body)))), "%s's bucket's size_actual, the plaintext's rounded to 4 KiB", writer)
				last := c.Blocks[len(c.Blocks)-1]
				Expect(s.SizeUtilized).To(Equal(last.NewOfs+last.Len), "%s's bucket's size_utilized, the end of its block map", writer)
				Expect(s.SizeUtilized).To(BeNumerically("<", s.Size), "%s's bucket's size_utilized below its size", writer)
				expectBucketCheckClean(ctx, bucket)
				got[writer] = written{stat: o, storage: s}
			}

			r, m := got["radosgw"], got["rgw-go"]
			Expect(m.storage.Size).To(Equal(r.storage.Size), "rgw-go's bucket's size against radosgw's")
			Expect(m.storage.SizeActual).To(Equal(r.storage.SizeActual), "rgw-go's bucket's size_actual against radosgw's")
			rc, mc := r.stat.compression(), m.stat.compression()
			Expect(mc.Type).To(Equal(rc.Type), "rgw-go's compression_type against radosgw's")
			Expect(mc.OrigSize).To(Equal(rc.OrigSize), "rgw-go's orig_size against radosgw's")
			Expect(mc.CompressorMessage).To(Equal(rc.CompressorMessage), "rgw-go's compressor_message against radosgw's")
			Expect(wpOldOffsets(mc)).To(Equal(wpOldOffsets(rc)), "rgw-go's blocks' old_ofs against radosgw's")
		},
		Entry("zlib, by PutObject", "zlib", []uint64{6<<20 + 333}),
		Entry("zlib, as a two-part upload", "zlib", []uint64{5 << 20, 1<<20 + 333}),
		Entry("snappy, by PutObject", "snappy", []uint64{6<<20 + 333}),
		Entry("snappy, as a two-part upload", "snappy", []uint64{5 << 20, 1<<20 + 333}),
		Entry("zstd, by PutObject", "zstd", []uint64{6<<20 + 333}),
		Entry("zstd, as a two-part upload", "zstd", []uint64{5 << 20, 1<<20 + 333}),
		Entry("lz4, by PutObject", "lz4", []uint64{6<<20 + 333}),
		Entry("lz4, as a two-part upload", "lz4", []uint64{5 << 20, 1<<20 + 333}),
	)
})
