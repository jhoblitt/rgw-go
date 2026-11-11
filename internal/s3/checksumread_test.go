package s3_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// setChecksumAttr stores b as key's user.rgw.cksum, as another writer of the
// attr would leave it.
func (w *writeWorld) setChecksumAttr(ctx context.Context, key string, b []byte) {
	GinkgoHelper()
	Expect(w.store.SetObjectAttrs(ctx, w.stat(ctx, key), map[string][]byte{meta.AttrCksum: b}, nil)).To(Succeed())
}

// rawCksumRecord is a user.rgw.cksum record of type t and digest as
// Cksum::encode frames one, whatever the type and the digest's length, which
// encodeCksum's Encode refuses to write.
func rawCksumRecord(t uint16, digest []byte) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(2, 1)
	e.U16(t)
	e.U16(uint16(len(digest))) //nolint:gosec // the specs' digests are short
	e.Raw(digest)
	e.U16(0)
	e.EndStruct(f)
	return e.Bytes()
}

// multipartChecksums is multipartObjects with head as the object's record
// and parts[n] as part n's head's, n the part head key's last dotted field.
func multipartChecksums(head *cksum.Cksum, parts map[int]*cksum.Cksum) *opfakes.FakeObjectStore {
	f := multipartObjects()
	stat := f.StatObjectStub
	withChecksum := func(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
		st, err := stat(ctx, rec, key)
		if err != nil {
			return nil, err
		}
		if key.Name == "k" {
			st.Checksum = head
			return st, nil
		}
		n, err := strconv.Atoi(key.Name[strings.LastIndexByte(key.Name, '.')+1:])
		if err != nil {
			return nil, err
		}
		st.Checksum = parts[n]
		return st, nil
	}
	f.StatObjectStub = withChecksum
	f.PrefetchObjectStub = withChecksum
	return f
}

// partDigest is part n's 32-byte digest in the multipart specs, which
// partArmor armors.
func partDigest(n int) []byte { return bytes.Repeat([]byte{byte(n)}, 32) }

func partArmor(n int) string { return base64.StdEncoding.EncodeToString(partDigest(n)) }

// compositeRecord is a completed digest upload's record, which partRecords'
// parts 1 and 2 combine into; part 3 has none.
func compositeRecord() *cksum.Cksum {
	return &cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest(), Flags: cksum.FlagV2 | cksum.FlagCombined | cksum.FlagComposite}
}

func partRecords() map[int]*cksum.Cksum {
	return map[int]*cksum.Cksum{
		1: {Type: cksum.SHA256, Digest: partDigest(1), Flags: cksum.FlagComposite},
		2: {Type: cksum.SHA256, Digest: partDigest(2), Flags: cksum.FlagComposite},
	}
}

// encryptedUploadKey is the SSE-C key encryptedUpload's upload names.
func encryptedUploadKey() []byte { return bytes.Repeat([]byte{7}, 32) }

// overTLS is the origin of a request httptest sends over TLS.
const overTLS = "https://s3.example.com"

// sseCRequest is the SSE-C headers that name key, as a ListParts sends them.
func sseCRequest(key []byte) []string {
	sum := md5.Sum(key) //nolint:gosec // SSE-C names its key by the key's MD5
	return []string{
		"X-Amz-Server-Side-Encryption-Customer-Algorithm", "AES256",
		"X-Amz-Server-Side-Encryption-Customer-Key", base64.StdEncoding.EncodeToString(key),
		"X-Amz-Server-Side-Encryption-Customer-Key-Md5", base64.StdEncoding.EncodeToString(sum[:]),
	}
}

// encryptedUpload stores an upload of k in plain, of type SHA256, as a
// Tentacle radosgw stores one created with mode, encryptedUploadKey's MD5
// for SSE-C, and two parts of "hello" with their checksums, and returns its
// id. rgw-go answers an SSE-C or SSE-KMS write 501, so only the store makes
// one.
func (w *writeWorld) encryptedUpload(ctx context.Context, mode string) string {
	GinkgoHelper()
	keyMD5 := md5.Sum(encryptedUploadKey()) //nolint:gosec // SSE-C names its key by the key's MD5
	owner := meta.UserOwner(w.alice.Info.UserID)
	uc := op.UploadChecksum{Type: cksum.SHA256, Flags: cksum.FlagComposite}
	up, err := w.store.CreateUpload(ctx, w.bucket(ctx), meta.ObjKey{Name: "k"}, op.UploadParams{
		Owner: owner, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Checksum: uc,
		Attrs: map[string][]byte{
			meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Alice")), meta.AttrCryptMode: []byte(mode), meta.AttrCryptKeyMD5: keyMD5[:],
		},
	})
	Expect(err).NotTo(HaveOccurred())
	for n := 1; n <= 2; n++ {
		_, err = w.store.PutPart(ctx, up, n, strings.NewReader("hello"), op.PutParams{Size: 5, Checksum: &op.ChecksumRequest{Type: uc.Type, Flags: uc.Flags}})
		Expect(err).NotTo(HaveOccurred())
	}
	return up.ID
}

// checksumModeOn is the header that asks a GET or HEAD for its checksum.
var checksumModeOn = []string{"X-Amz-Checksum-Mode", "enabled"}

// listPartsDoc is the ListPartsResult of upload id of k in plain listing
// parts, a whole listing of up to 1000, extra following the Owner.
func listPartsDoc(id, extra string, parts ...string) string {
	return xmlDecl + `<ListPartsResult ` + xmlnsS3 + `><Bucket>plain</Bucket><Key>k</Key><UploadId>` + id +
		`</UploadId><StorageClass>STANDARD</StorageClass><PartNumberMarker>0</PartNumberMarker><NextPartNumberMarker>` +
		strconv.Itoa(len(parts)) + `</NextPartNumberMarker><MaxParts>1000</MaxParts><IsTruncated>false</IsTruncated>` +
		`<Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner>` + extra + strings.Join(parts, "") + `</ListPartsResult>`
}

// helloPart is part n of "hello" as ListPartsResult lists it, extra
// following its Size.
func helloPart(n int, extra string) string {
	return `<Part><LastModified>` + writeMtimeMs + `</LastModified><PartNumber>` + strconv.Itoa(n) +
		`</PartNumber><ETag>&quot;` + md5Hex("hello") + `&quot;</ETag><Size>5</Size>` + extra + `</Part>`
}

var _ = Describe("checksums on reads", func() {
	const objectAttrsHead = xmlDecl + `<GetObjectAttributes>`
	var w *writeWorld

	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Context("GET and HEAD on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })

			// send_response_data
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L551-L586 at v20.2.4);
			// Squid's radosgw shows none, and rgw-go what it stores there
			// (docs/differences.md, "Squid clusters show the checksums they
			// store").
			DescribeTable("send the stored checksum under its type's header and x-amz-checksum-type under checksum mode",
				func(algorithm, header, value string) {
					Expect(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", algorithm).Code).To(Equal(200))
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						rec := w.send(w.alice, method, "/plain/k", "", "X-Amz-Checksum-Mode", "ENABLED")
						Expect(rec.Code).To(Equal(200), method)
						Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{header: value, "x-amz-checksum-type": "FULL_OBJECT"}), method)
					}
				},
				Entry("CRC32", "CRC32", "x-amz-checksum-crc32", helloCRC32),
				Entry("CRC32C", "CRC32C", "x-amz-checksum-crc32c", helloCRC32C),
				Entry("CRC64NVME", "CRC64NVME", "x-amz-checksum-crc64nvme", helloCRC64NVME),
				Entry("SHA1", "SHA1", "x-amz-checksum-sha1", helloSHA1),
				Entry("SHA256", "SHA256", "x-amz-checksum-sha256", helloSHA256),
				Entry("SHA512 under AWS's x-amz-checksum-sha512, not Tentacle's x-rgw-checksum-sha512", "SHA512", "x-amz-checksum-sha512", helloSHA512),
			)
			DescribeTable("names a stored record's type as AWS does, and one AWS does not define under x-rgw-checksum-",
				func(ctx SpecContext, c cksum.Cksum, header string) {
					w.setChecksumAttr(ctx, "src", encodeCksum(c))
					rec := w.send(w.alice, http.MethodGet, "/plain/src", "", checksumModeOn...)
					Expect(rec.Code).To(Equal(200))
					Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{header: c.Armor(), "x-amz-checksum-type": "FULL_OBJECT"}))
				},
				Entry("XXH3 as x-amz-checksum-xxhash3, where v20.2.4 sends x-rgw-checksum-xxh3",
					cksum.Cksum{Type: cksum.XXH3, Digest: storedDigest()[:8]}, "x-amz-checksum-xxhash3"),
				Entry("BLAKE3 as Tentacle's x-rgw-checksum-blake3", cksum.Cksum{Type: cksum.BLAKE3, Digest: storedDigest()}, "x-rgw-checksum-blake3"),
			)
			DescribeTable("sends no checksum header",
				func(hdr ...string) {
					Expect(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", "SHA256").Code).To(Equal(200))
					rec := w.send(w.alice, http.MethodGet, "/plain/k", "", hdr...)
					Expect(rec.Code).To(BeElementOf(200, 206))
					Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				},
				Entry("without checksum mode"),
				Entry("under another mode", "X-Amz-Checksum-Mode", "disabled"),
				Entry("for a Range, which radosgw's checksum mode leaves out", "X-Amz-Checksum-Mode", "enabled", "Range", "bytes=0-4"),
			)
			It("sends none for an object without a record", func() {
				rec := w.send(w.alice, http.MethodGet, "/plain/src", "", checksumModeOn...)
				Expect(rec.Code).To(Equal(200))
				Expect(checksumHeaders(rec.Header())).To(BeEmpty())
			})
			// Tentacle's radosgw decodes the first four unchecked: it aborts at
			// its type table on the two types with no id, and shows the
			// type's digest size of its buffer for the two digests of another
			// size. It aborts on the record with bytes past it, whose
			// whole-buffer decode asserts, and, as rgw-go does, sends none
			// for the truncated one, whose decode throws
			// (docs/ceph-upstream-bugs.md, "radosgw's checksum decode overruns
			// its digest buffer and its type table" and "radosgw aborts on a
			// checksum-mode GET of an object whose checksum attr has bytes
			// past its record"). Squid's radosgw shows none for any.
			DescribeTable("serves an object whose record does not decode whole with no checksum header",
				func(ctx SpecContext, b []byte) {
					w.setChecksumAttr(ctx, "src", b)
					rec := w.send(w.alice, http.MethodGet, "/plain/src", "", checksumModeOn...)
					Expect(rec.Code).To(Equal(200))
					Expect(rec.Body.String()).To(Equal("hello"))
					Expect(checksumHeaders(rec.Header())).To(BeEmpty())
				},
				Entry("type 9, which Tentacle's table has no row for", rawCksumRecord(9, storedDigest()[:16])),
				Entry("type 65535", rawCksumRecord(65535, storedDigest()[:8])),
				Entry("a CRC32 whose digest is longer than a CRC32's", rawCksumRecord(uint16(cksum.CRC32), storedDigest()[:8])),
				Entry("a SHA256 whose digest is shorter than a SHA256's", rawCksumRecord(uint16(cksum.SHA256), storedDigest()[:4])),
				Entry("a record with bytes past it", append(encodeCksum(cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest()}), 0x0d, 0x0a)),
				Entry("a truncated record", encodeCksum(cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest()})[:9]),
			)
			It("sends a digest holding CR, LF and NUL as its base64, never as raw bytes", func(ctx SpecContext) {
				digest := []byte("\r\nX-Injected: yes\r\n\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")
				Expect(digest).To(HaveLen(32))
				w.setChecksumAttr(ctx, "src", encodeCksum(cksum.Cksum{Type: cksum.SHA256, Digest: digest}))
				rec := w.send(w.alice, http.MethodGet, "/plain/src", "", checksumModeOn...)
				Expect(rec.Code).To(Equal(200))
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{
					"x-amz-checksum-sha256": base64.StdEncoding.EncodeToString(digest), "x-amz-checksum-type": "FULL_OBJECT",
				}))
				Expect(rec.Header()).NotTo(haveHeaderAnyCase("X-Injected"))
			})
		})

		Context("a multipart object on "+rel.String(), func() {
			var rw *readWorld
			BeforeEach(func(ctx SpecContext) { rw = newReadWorld(ctx, rel) })
			read := func(objects op.ObjectStore, target string, hdr map[string]string) *httptest.ResponseRecorder {
				return send(rw.handler(rw.env(objects), rw.alice), http.MethodGet, target, hdr)
			}

			It("sends a composite with the object's part count", func() {
				rec := read(multipartChecksums(compositeRecord(), partRecords()), "/plain/k", map[string]string{"x-amz-checksum-mode": "enabled"})
				Expect(rec.Code).To(Equal(200))
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-sha256": digestArmor + "-3", "x-amz-checksum-type": "COMPOSITE"}))
			})
			// main's rule (docs/differences.md, "A partNumber read shows its
			// part's checksum bare").
			It("sends a partNumber read's part checksum bare, typed by its flags", func() {
				rec := read(multipartChecksums(compositeRecord(), partRecords()), "/plain/k?partNumber=2", map[string]string{"x-amz-checksum-mode": "enabled"})
				Expect(rec.Code).To(Equal(200))
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-sha256": partArmor(2), "x-amz-checksum-type": "COMPOSITE"}))
				crc := map[int]*cksum.Cksum{2: {Type: cksum.CRC32, Digest: partDigest(2)[:4], Flags: cksum.FlagFullObject}}
				rec = read(multipartChecksums(nil, crc), "/plain/k?partNumber=2", map[string]string{"x-amz-checksum-mode": "enabled"})
				Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-crc32": "AgICAg==", "x-amz-checksum-type": "FULL_OBJECT"}))
				rec = read(multipartChecksums(compositeRecord(), partRecords()), "/plain/k?partNumber=3", map[string]string{"x-amz-checksum-mode": "enabled"})
				Expect(checksumHeaders(rec.Header())).To(BeEmpty(), "a part head without a record, never the object's")
			})
			// RGWGetObjAttrs_ObjStore_S3::send_response
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4029-L4057 and
			// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4088-L4096 at v20.2.4).
			It("renders GetObjectAttributes' Checksum and each part's checksum", func() {
				rec := read(multipartChecksums(compositeRecord(), partRecords()), "/plain/k?attributes", map[string]string{"x-amz-object-attributes": "Checksum,ObjectParts"})
				Expect(rec.Code).To(Equal(200))
				size3 := strconv.FormatUint(goldenManifest("squid-multipart").ObjSize-16<<20, 10)
				Expect(rec.Body.String()).To(Equal(objectAttrsHead +
					`<Checksum><ChecksumSHA256>` + digestArmor + `-3</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType></Checksum>` +
					`<ObjectParts><Part><PartNumber>1</PartNumber><Size>8388608</Size><ChecksumSHA256>` + partArmor(1) + `</ChecksumSHA256></Part>` +
					`<Part><PartNumber>2</PartNumber><Size>8388608</Size><ChecksumSHA256>` + partArmor(2) + `</ChecksumSHA256></Part>` +
					`<Part><PartNumber>3</PartNumber><Size>` + size3 + `</Size></Part>` +
					`<PartsCount>3</PartsCount><TotalPartsCount>3</TotalPartsCount><IsTruncated>false</IsTruncated></ObjectParts></GetObjectAttributes>`))
			})
		})

		Context("GetObjectAttributes on "+rel.String(), func() {
			BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, rel) })
			attrs := func(key string) string {
				GinkgoHelper()
				rec := w.send(w.alice, http.MethodGet, "/plain/"+key+"?attributes", "", "X-Amz-Object-Attributes", "Checksum,ObjectSize")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				return rec.Body.String()
			}

			It("renders the stored checksum's element and ChecksumType", func() {
				Expect(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", "SHA512").Code).To(Equal(200))
				Expect(attrs("k")).To(Equal(objectAttrsHead + `<Checksum><ChecksumSHA512>` + helloSHA512 +
					`</ChecksumSHA512><ChecksumType>FULL_OBJECT</ChecksumType></Checksum><ObjectSize>5</ObjectSize></GetObjectAttributes>`))
			})
			It("names a stored xxh3 record ChecksumXXHASH3, where v20.2.4 renders ChecksumXXH3", func(ctx SpecContext) {
				w.setChecksumAttr(ctx, "src", encodeCksum(cksum.Cksum{Type: cksum.XXH3, Digest: storedDigest()[:8]}))
				Expect(attrs("src")).To(Equal(objectAttrsHead +
					`<Checksum><ChecksumXXHASH3>q6urq6urq6s=</ChecksumXXHASH3><ChecksumType>FULL_OBJECT</ChecksumType></Checksum><ObjectSize>5</ObjectSize></GetObjectAttributes>`))
			})
			emptyChecksum := func(ctx SpecContext, b []byte) {
				if b != nil {
					w.setChecksumAttr(ctx, "src", b)
				}
				Expect(attrs("src")).To(Equal(objectAttrsHead + `<Checksum></Checksum><ObjectSize>5</ObjectSize></GetObjectAttributes>`))
			}
			// send_response leaves the section empty for no record and for
			// one whose decode throws
			// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4029-L4057 at v20.2.4).
			DescribeTable("renders an empty Checksum, as radosgw does, for no record and one whose decode throws", emptyChecksum,
				Entry("no record", nil),
				Entry("a truncated record", encodeCksum(cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest()})[:9]),
			)
			// Tentacle's radosgw decodes these unchecked: it aborts at its
			// type table on a type with no id, and renders the SHA1's first
			// 20 bytes (docs/differences.md, "A stored checksum rgw-go cannot
			// read whole is not shown").
			DescribeTable("renders an empty Checksum for a record radosgw decodes unchecked", emptyChecksum,
				Entry("type 9", rawCksumRecord(9, storedDigest()[:16])),
				Entry("a SHA1 whose digest is a SHA256's", rawCksumRecord(uint16(cksum.SHA1), storedDigest())),
			)
		})
	}

	// RGWListMultipart_ObjStore_S3::send_response
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4695-L4713 at v20.2.4),
	// with the upload's type (docs/differences.md, "ListParts reports its
	// upload's checksum algorithm and type").
	Context("ListParts", func() {
		list := func(id string) string {
			GinkgoHelper()
			rec := w.send(w.alice, http.MethodGet, "/plain/k?uploadId="+id, "")
			expectChunkedXML(rec)
			return rec.Body.String()
		}
		upload := func(create []string, part ...string) string {
			GinkgoHelper()
			id := w.initUpload(w.alice, "/plain/k", create...)
			for n := 1; n <= 2; n++ {
				rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber="+strconv.Itoa(n), "hello", part...)
				Expect(rec.Code).To(Equal(200), rec.Body.String())
			}
			return id
		}

		DescribeTable("renders the upload's ChecksumAlgorithm and ChecksumType after the Owner, and each part's checksum, on Tentacle",
			func(ctx SpecContext, create []string, alg, typ, element, value string) {
				w = newWriteWorld(ctx, denc.Tentacle)
				id := upload(create)
				part := "<" + element + ">" + value + "</" + element + ">"
				Expect(list(id)).To(Equal(listPartsDoc(id, "<ChecksumAlgorithm>"+alg+"</ChecksumAlgorithm><ChecksumType>"+typ+"</ChecksumType>",
					helloPart(1, part), helloPart(2, part))))
			},
			Entry("SHA256, COMPOSITE", []string{"X-Amz-Checksum-Algorithm", "sha256"}, "SHA256", "COMPOSITE", "ChecksumSHA256", helloSHA256),
			Entry("SHA512 under AWS's names", []string{"X-Amz-Checksum-Algorithm", "SHA512"}, "SHA512", "COMPOSITE", "ChecksumSHA512", helloSHA512),
			Entry("CRC32, FULL_OBJECT", []string{"X-Amz-Checksum-Algorithm", "CRC32"}, "CRC32", "FULL_OBJECT", "ChecksumCRC32", helloCRC32),
			Entry("CRC32 created COMPOSITE", []string{"X-Amz-Checksum-Algorithm", "CRC32", "X-Amz-Checksum-Type", "COMPOSITE"},
				"CRC32", "COMPOSITE", "ChecksumCRC32", helloCRC32),
		)
		It("renders a part's own checksum, and no algorithm, for an upload without a type on Tentacle", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Tentacle)
			id := upload(nil, "X-Amz-Checksum-Algorithm", "CRC32C")
			part := "<ChecksumCRC32C>" + helloCRC32C + "</ChecksumCRC32C>"
			Expect(list(id)).To(Equal(listPartsDoc(id, "", helloPart(1, part), helloPart(2, part))))
		})
		// Amazon S3 takes ListParts' SSE-C headers for an upload created with
		// a checksum algorithm, and needs kms:Decrypt for an SSE-KMS one
		// (aws-sdk-go-v2 service/s3 v1.114.6, ListParts and ListPartsInput);
		// radosgw reads neither and renders them all
		// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1602-L1627 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7593-L7633 at v20.2.4;
		// docs/differences.md, "ListParts shows an encrypted upload's checksums
		// only to a requester who could read its data").
		DescribeTable("renders an encrypted upload's checksums only to a requester who could read its data, on Tentacle",
			func(ctx SpecContext, mode, origin string, hdr []string, shown bool) {
				w = newWriteWorld(ctx, denc.Tentacle)
				id := w.encryptedUpload(ctx, mode)
				rec := w.send(w.alice, http.MethodGet, origin+"/plain/k?uploadId="+id, "", hdr...)
				expectChunkedXML(rec)
				if !shown {
					Expect(rec.Body.String()).To(Equal(listPartsDoc(id, "", helloPart(1, ""), helloPart(2, ""))))
					return
				}
				part := "<ChecksumSHA256>" + helloSHA256 + "</ChecksumSHA256>"
				Expect(rec.Body.String()).To(Equal(listPartsDoc(id, "<ChecksumAlgorithm>SHA256</ChecksumAlgorithm><ChecksumType>COMPOSITE</ChecksumType>",
					helloPart(1, part), helloPart(2, part))))
			},
			Entry("SSE-C, to a request naming its key over TLS", "SSE-C-AES256", overTLS, sseCRequest(encryptedUploadKey()), true),
			Entry("SSE-C, to a request naming its key over plain HTTP, as rgw_crypt_require_ssl refuses a GET of its data",
				"SSE-C-AES256", "", sseCRequest(encryptedUploadKey()), false),
			Entry("SSE-C, to a request without the SSE-C headers", "SSE-C-AES256", overTLS, nil, false),
			Entry("SSE-C, to a request naming another key", "SSE-C-AES256", overTLS, sseCRequest(bytes.Repeat([]byte{9}, 32)), false),
			Entry("SSE-KMS, SSE-C headers or not", "SSE-KMS", overTLS, sseCRequest(encryptedUploadKey()), false),
			Entry("SSE-S3, whose key no requester holds", "AES256", "", nil, true),
		)
		It("renders no checksum on Squid, whose uploads and part records hold none", func(ctx SpecContext) {
			w = newWriteWorld(ctx, denc.Squid)
			id := upload([]string{"X-Amz-Checksum-Algorithm", "SHA256"}, "X-Amz-Checksum-Algorithm", "SHA256")
			Expect(list(id)).To(Equal(listPartsDoc(id, "", helloPart(1, ""), helloPart(2, ""))))
		})
	})

	// The op builds every checksum a read reports from a record
	// cksum.DecodeAttr accepted; the handler still refuses to render one that
	// is not well-formed, so no byte the store holds reaches a header raw.
	DescribeTable("renders no read checksum it is handed that is not well-formed",
		func(c *op.ReadChecksum) {
			h := http.Header{}
			s3.SetReadChecksumForTest(h, c)
			Expect(h).To(BeEmpty())
			Expect(s3.ObjectAttrsChecksumForTest(c)).To(Equal("<Checksum></Checksum>"))
		},
		Entry("nil", nil),
		Entry("a CR and LF inside the armor, which encoding/base64 skips",
			&op.ReadChecksum{Type: cksum.SHA256, Value: digestArmor[:20] + "\r\n" + digestArmor[20:], ChecksumType: cksum.FullObjectType}),
		Entry("a NUL after the armor", &op.ReadChecksum{Type: cksum.SHA256, Value: digestArmor + "\x00", ChecksumType: cksum.FullObjectType}),
		Entry("a header after the part count", &op.ReadChecksum{Type: cksum.SHA256, Value: digestArmor + "-2\r\nX-Injected: yes", ChecksumType: cksum.CompositeType}),
		Entry("an empty part count", &op.ReadChecksum{Type: cksum.SHA256, Value: digestArmor + "-", ChecksumType: cksum.CompositeType}),
		Entry("a digest shorter than the type's", &op.ReadChecksum{Type: cksum.SHA256, Value: "q6urqw==", ChecksumType: cksum.FullObjectType}),
		Entry("the armor without its padding", &op.ReadChecksum{Type: cksum.SHA256, Value: strings.TrimRight(digestArmor, "="), ChecksumType: cksum.FullObjectType}),
		Entry("type none", &op.ReadChecksum{Type: cksum.None, ChecksumType: cksum.FullObjectType}),
		Entry("a type with no id", &op.ReadChecksum{Type: 9, Value: "q6urq6urq6urq6urq6urqw==", ChecksumType: cksum.FullObjectType}),
		Entry("another checksum type", &op.ReadChecksum{Type: cksum.SHA256, Value: digestArmor, ChecksumType: "FULL_OBJECT\r\nX-Injected: yes"}),
	)
	It("renders a well-formed read checksum, a composite's part count included", func() {
		c := &op.ReadChecksum{Type: cksum.SHA256, Value: digestArmor + "-10000", ChecksumType: cksum.CompositeType}
		h := http.Header{}
		s3.SetReadChecksumForTest(h, c)
		Expect(checksumHeaders(h)).To(Equal(map[string]string{"x-amz-checksum-sha256": digestArmor + "-10000", "x-amz-checksum-type": "COMPOSITE"}))
		Expect(s3.ObjectAttrsChecksumForTest(c)).To(Equal("<Checksum><ChecksumSHA256>" + digestArmor + "-10000</ChecksumSHA256><ChecksumType>COMPOSITE</ChecksumType></Checksum>"))
	})
	DescribeTable("renders a listed part's checksum only for a record Valid accepts",
		func(c *cksum.Cksum, want string) {
			Expect(s3.PartChecksumForTest(c)).To(Equal(want))
		},
		Entry("a SHA256", &cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest()}, "<ChecksumSHA256>"+digestArmor+"</ChecksumSHA256>"),
		Entry("a BLAKE3, whatever AWS defines", &cksum.Cksum{Type: cksum.BLAKE3, Digest: storedDigest()}, "<ChecksumBLAKE3>"+digestArmor+"</ChecksumBLAKE3>"),
		Entry("nil", (*cksum.Cksum)(nil), ""),
		Entry("type none", &cksum.Cksum{}, ""),
		Entry("a digest shorter than the type's", &cksum.Cksum{Type: cksum.SHA256, Digest: storedDigest()[:4]}, ""),
		Entry("a type with no id", &cksum.Cksum{Type: 9, Digest: storedDigest()[:16]}, ""),
	)
})
