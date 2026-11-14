package s3_test

import (
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const (
	sseHeaderName     = "X-Amz-Server-Side-Encryption"
	sseKeyHeader      = "X-Amz-Server-Side-Encryption-Customer-Key"
	sseKeyMD5Header   = "X-Amz-Server-Side-Encryption-Customer-Key-Md5"
	sseKMSKeyHeader   = "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"
	sseBucketKeyHdr   = "X-Amz-Server-Side-Encryption-Bucket-Key-Enabled"
	sseContextHeader  = "X-Amz-Server-Side-Encryption-Context"
	sseTwentyBytesHdr = "X-Amz-Server-Side-Enx"

	sseCopyKeyHeader = "X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key"

	mixedGroups   = "do sse-c &amp; sse-kms|sse-s3"
	incompleteKMS = "incomplete rest sse parms: x-amz-server-side-encryption-aws-kms-key-id not valid without kms"
	incompleteBKE = "incomplete rest sse parms: x-amz-server-side-encryption-bucket-key-enabled not valid without kms"
	incompleteCtx = "incomplete rest sse parms: x-amz-server-side-encryption-context not valid without kms"

	// emptySSEMsg is v20.2.5's message for an x-amz-server-side-encryption
	// that is neither aws:kms nor AES256, and emptySSECMsg rgw-go's for an
	// SSE-C name sent empty.
	emptySSEMsg  = "requires HTTP header x-amz-server-side-encryption : aws:kms or AES256"
	emptySSECMsg = "must not send a server-side-encryption-customer header with no value"
	emptyKMSMsg  = "requires HTTP header x-amz-server-side-encryption-aws-kms-key-id"

	algorithmMissingMsg = "must provide a valid encryption algorithm"
)

// emptyEncryption is one request whose encryption headers are all empty, or
// that holds a value among them, with the answer of a write that carries it:
// a 200 that stores the object unencrypted and the attrs named, or a refusal.
type emptyEncryption struct {
	hdr    []string
	status int
	code   string
	msg    string
	stored []string
}

// refused is the 400 InvalidArgument a write answers hdr with, msg in its
// message.
func refused(msg string, hdr ...string) emptyEncryption {
	return emptyEncryption{hdr: hdr, status: 400, code: "InvalidArgument", msg: msg}
}

// emptyEncryptionEntries are the requests the writes that run both
// get_encryption_defaults and rgw_s3_prepare_encrypt (PutObject and
// CreateMultipartUpload) and the parts of an upload without a crypt mode
// answer alike. Each header is sent with no value unless an entry says
// otherwise.
func emptyEncryptionEntries() []TableEntry {
	return []TableEntry{
		Entry("the algorithm", refused(emptySSECMsg, sseCAlgorithmHeader, "")),
		Entry("the key", refused(emptySSECMsg, sseKeyHeader, "")),
		Entry("the key MD5", refused(emptySSECMsg, sseKeyMD5Header, "")),
		Entry("the algorithm, the key and the key MD5",
			refused(emptySSECMsg, sseCAlgorithmHeader, "", sseKeyHeader, "", sseKeyMD5Header, "")),
		Entry("any other name under the SSE-C prefix", refused(emptySSECMsg, "X-Amz-Server-Side-Encryption-Customer-Foo", "")),
		Entry("x-amz-server-side-encryption, which v20.2.5 refuses", refused(emptySSEMsg, sseHeaderName, "")),
		Entry("x-amz-server-side-encryption and the KMS key id", refused(emptySSEMsg, sseHeaderName, "", sseKMSKeyHeader, "")),
		Entry("x-amz-server-side-encryption, the bucket key and the context",
			refused(emptySSEMsg, sseHeaderName, "", sseBucketKeyHdr, "", sseContextHeader, "")),
		Entry("a name only the first 20 bytes of the prefix match, in the map and in neither group: stored as radosgw stores it",
			emptyEncryption{hdr: []string{sseTwentyBytesHdr, ""}, status: 200, stored: []string{meta.AttrPrefix + "x-amz-server-side-enx"}}),
		Entry("the algorithm beside such a name", refused(emptySSECMsg, sseCAlgorithmHeader, "", sseTwentyBytesHdr, "")),
		Entry("the KMS key id alone: get_encryption_defaults refuses it without x-amz-server-side-encryption",
			refused(incompleteKMS, sseKMSKeyHeader, "")),
		Entry("the bucket key alone", refused(incompleteBKE, sseBucketKeyHdr, "")),
		Entry("the context alone", refused(incompleteCtx, sseContextHeader, "")),
		Entry("the KMS key id and the context, the key id named first", refused(incompleteKMS, sseContextHeader, "", sseKMSKeyHeader, "")),
		Entry("the algorithm and x-amz-server-side-encryption: both groups", refused(mixedGroups, sseCAlgorithmHeader, "", sseHeaderName, "")),
		Entry("the key and the KMS key id: both groups", refused(mixedGroups, sseKeyHeader, "", sseKMSKeyHeader, "")),
		Entry("x-amz-server-side-encryption beside a KMS key id that has a value: 501, as every request that asks for encryption",
			emptyEncryption{hdr: []string{sseHeaderName, "", sseKMSKeyHeader, "k"}, status: 501, code: "NotImplemented"}),
		Entry("the algorithm beside a key that has a value: 501",
			emptyEncryption{hdr: []string{sseCAlgorithmHeader, "", sseKeyHeader, "a2V5"}, status: 501, code: "NotImplemented"}),
	}
}

// expectUnencrypted checks that attrs, a stored object's or an upload's,
// show nothing encrypted, hold no SSE-C name, and hold of the other
// encryption names exactly the empty ones stored lists.
func expectUnencrypted(attrs map[string][]byte, stored []string) {
	GinkgoHelper()
	Expect(attrs).NotTo(HaveKey(meta.AttrCryptMode), "nothing is encrypted")
	Expect(attrs).NotTo(HaveKey(HavePrefix(meta.AttrPrefix+"x-amz-server-side-encryption-customer-")), "the SSE-C names are not stored")
	for _, a := range stored {
		Expect(attrs).To(HaveKeyWithValue(a, []byte{0}), "an empty name radosgw stores")
	}
	for a := range attrs {
		if strings.HasPrefix(a, meta.AttrPrefix+"x-amz-server-side-en") {
			Expect(stored).To(ContainElement(a), "no other encryption name is stored")
		}
	}
}

// sendAll is send with each pair of hdr added, so that a name it repeats is
// sent as many times, in order.
func (w *writeWorld) sendAll(who *op.UserRecord, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	return w.sendAllOn(op.RouteUnknown, who, method, target, body, hdr...)
}

// sendAllOn is sendAll authenticated on route, which decides whether the
// policy's environment reads the query's encryption parameters.
func (w *writeWorld) sendAllOn(route op.AuthRoute, who *op.UserRecord, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	var b io.Reader
	if body != "" {
		b = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(GinkgoT().Context(), method, target, b)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s3.NewHandler(w.env, authAsOn(who, route), testConfig(s3.Config{})).ServeHTTP(rec, req)
	return rec
}

// withQuery is target with query added, which may be empty.
func withQuery(target, query string) string {
	switch {
	case query == "":
		return target
	case strings.Contains(target, "?"):
		return target + "&" + query
	}
	return target + "?" + query
}

// writeAttempt is one of the five writes that take encryption headers, sent
// to a target of its own, with the query it adds.
type writeAttempt struct {
	name string
	// query is whether map_qs_metadata reads the query's encryption
	// parameters for the write, as it does for PutObject and its parts.
	query bool
	send  func(w *writeWorld, id, query string, hdr ...string) *httptest.ResponseRecorder
}

// writeAttempts are PutObject, CreateMultipartUpload, UploadPart,
// UploadPartCopy and CopyObject, each sent as alice to a bucket whose upload
// id names a plain upload of k.
var writeAttempts = []writeAttempt{
	{"PutObject", true, func(w *writeWorld, _, query string, hdr ...string) *httptest.ResponseRecorder {
		return w.sendAll(w.alice, http.MethodPut, overTLS+withQuery("/plain/j", query), "x", hdr...)
	}},
	{"CreateMultipartUpload", false, func(w *writeWorld, _, query string, hdr ...string) *httptest.ResponseRecorder {
		return w.sendAll(w.alice, http.MethodPost, overTLS+withQuery("/plain/m?uploads", query), "", hdr...)
	}},
	{"UploadPart", true, func(w *writeWorld, id, query string, hdr ...string) *httptest.ResponseRecorder {
		return w.sendAll(w.alice, http.MethodPut, overTLS+withQuery("/plain/k?uploadId="+id+"&partNumber=1", query), "x", hdr...)
	}},
	{"UploadPartCopy", true, func(w *writeWorld, id, query string, hdr ...string) *httptest.ResponseRecorder {
		return w.sendAll(w.alice, http.MethodPut, overTLS+withQuery("/plain/k?uploadId="+id+"&partNumber=2", query), "",
			append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"}, hdr...)...)
	}},
	{"CopyObject", false, func(w *writeWorld, _, query string, hdr ...string) *httptest.ResponseRecorder {
		return w.sendAll(w.alice, http.MethodPut, overTLS+withQuery("/plain/dst", query), "",
			append([]string{"X-Amz-Copy-Source", "/plain/src"}, hdr...)...)
	}},
}

// expectNothingWritten checks that none of writeAttempts stored anything:
// no object, no part, and no upload but the one id names.
func expectNothingWritten(ctx SpecContext, w *writeWorld, id string, rel denc.Release) {
	GinkgoHelper()
	Expect(w.stat(ctx, "j").Exists).To(BeFalse(), "release %d", rel)
	Expect(w.stat(ctx, "dst").Exists).To(BeFalse(), "release %d", rel)
	Expect(w.uploads(ctx)).To(HaveLen(1), "release %d: only the upload this spec began", rel)
	Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d", rel)
}

var _ = Describe("an encryption header sent empty on a write", func() {
	// A PutObject runs get_encryption_defaults in get_params, before the
	// requester is authorized, and rgw_s3_prepare_encrypt when it writes;
	// rgw-go refuses an empty x-amz-server-side-encryption and an empty SSE-C
	// name where radosgw stores the object in the clear (docs/differences.md).
	DescribeTable("is answered by PutObject, per release",
		func(ctx SpecContext, c emptyEncryption) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				rec := w.send(w.alice, http.MethodPut, overTLS+"/plain/k", "x", c.hdr...)
				if c.status == 200 {
					Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
					st := w.stat(ctx, "k")
					Expect(st.Exists).To(BeTrue(), "release %d", rel)
					expectUnencrypted(st.Attrs, c.stored)
					continue
				}
				expectError(rec, c.status, c.code)
				Expect(rec.Body.String()).To(ContainSubstring(c.msg), "release %d", rel)
				Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d: nothing is stored", rel)
			}
		},
		emptyEncryptionEntries(),
	)

	// A CreateMultipartUpload runs both once the requester is authorized.
	DescribeTable("is answered by CreateMultipartUpload, per release",
		func(ctx SpecContext, c emptyEncryption) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				rec := w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploads", "", c.hdr...)
				if c.status == 200 {
					Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
					ups := w.uploads(ctx)
					Expect(ups).To(HaveLen(1), "release %d", rel)
					up, err := w.upload(ctx, "k", ups[0].ID)
					Expect(err).NotTo(HaveOccurred())
					expectUnencrypted(up.Attrs, c.stored)
					continue
				}
				expectError(rec, c.status, c.code)
				Expect(rec.Body.String()).To(ContainSubstring(c.msg), "release %d", rel)
				Expect(w.uploads(ctx)).To(BeEmpty(), "release %d: no upload", rel)
			}
		},
		emptyEncryptionEntries(),
	)

	// A CopyObject runs neither function on Squid and rgw_s3_prepare_encrypt
	// alone on Tentacle, so names that do not go together are not refused, nor
	// are the KMS names get_encryption_defaults reads, but for an empty KMS key
	// id, which sets a policy condition key as the SSE name does, and the
	// copy source's names, which no write reads, are not either.
	DescribeTable("is answered by CopyObject, per release",
		func(ctx SpecContext, c emptyEncryption) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				rec := w.send(w.alice, http.MethodPut, overTLS+"/plain/dst", "", append([]string{"X-Amz-Copy-Source", "/plain/src"}, c.hdr...)...)
				if c.status == 200 {
					Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
					Expect(w.stat(ctx, "dst").Attrs).NotTo(HaveKey(meta.AttrCryptMode), "release %d", rel)
					continue
				}
				expectError(rec, c.status, c.code)
				Expect(rec.Body.String()).To(ContainSubstring(c.msg), "release %d", rel)
				Expect(w.stat(ctx, "dst").Exists).To(BeFalse(), "release %d: nothing is stored", rel)
			}
		},
		Entry("the algorithm", refused(emptySSECMsg, sseCAlgorithmHeader, "")),
		Entry("the key MD5", refused(emptySSECMsg, sseKeyMD5Header, "")),
		Entry("x-amz-server-side-encryption", refused(emptySSEMsg, sseHeaderName, "")),
		Entry("the algorithm and x-amz-server-side-encryption: the groups are not told apart for a copy",
			refused(emptySSEMsg, sseCAlgorithmHeader, "", sseHeaderName, "")),
		Entry("the KMS key id alone, which sets a policy condition key", refused(emptyKMSMsg, sseKMSKeyHeader, "")),
		Entry("the KMS key id and the bucket key", refused(emptyKMSMsg, sseKMSKeyHeader, "", sseBucketKeyHdr, "")),
		Entry("x-amz-server-side-encryption and the KMS key id: the SSE name first",
			refused(emptySSEMsg, sseHeaderName, "", sseKMSKeyHeader, "")),
		Entry("the bucket key alone, which sets no condition key", emptyEncryption{hdr: []string{sseBucketKeyHdr, ""}, status: 200}),
		Entry("the context alone, which sets no condition key", emptyEncryption{hdr: []string{sseContextHeader, ""}, status: 200}),
		Entry("the copy source's algorithm", emptyEncryption{hdr: []string{sseCCopyAlgorithm, ""}, status: 200}),
		Entry("x-amz-server-side-encryption beside a KMS key id that has a value: 501",
			emptyEncryption{hdr: []string{sseHeaderName, "", sseKMSKeyHeader, "k"}, status: 501, code: "NotImplemented"}),
	)

	It("answers PutObject's refusal before the permission check and CreateMultipartUpload's after it", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		expectError(w.send(w.bob, http.MethodPut, overTLS+"/plain/k", "x", sseKMSKeyHeader, ""), 400, "InvalidArgument")
		expectError(w.send(w.bob, http.MethodPost, overTLS+"/plain/k?uploads", "", sseKMSKeyHeader, ""), 403, "AccessDenied")
	})

	// A request that asks for encryption is refused 501 before the
	// permission check for PutObject and UploadPart, where radosgw's
	// rgw_s3_prepare_encrypt runs in execute: it shows a requester only that
	// no encryption is served, which docs/exclusions.md publishes. An empty
	// header is PutObject's before it too, and the part's once its upload is
	// read, which only an authorized requester reaches.
	It("answers the refusal of a header before the permission check for PutObject, and after it for the rest", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			id := w.initUpload(w.alice, "/plain/k")
			part := overTLS + "/plain/k?uploadId=" + id + "&partNumber=1"
			expectError(w.send(w.bob, http.MethodPut, overTLS+"/plain/j", "x", sseHeaderName, "AES256"), 501, "NotImplemented")
			expectError(w.send(w.bob, http.MethodPut, part, "x", sseHeaderName, "AES256"), 501, "NotImplemented")
			expectError(w.send(w.bob, http.MethodPost, overTLS+"/plain/m?uploads", "", sseHeaderName, "AES256"), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodPut, overTLS+"/plain/j", "x", sseHeaderName, ""), 400, "InvalidArgument")
			expectError(w.send(w.bob, http.MethodPut, part, "x", sseHeaderName, ""), 403, "AccessDenied")
			expectError(w.send(w.bob, http.MethodPost, overTLS+"/plain/m?uploads", "", sseHeaderName, ""), 403, "AccessDenied")
			Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d", rel)
		}
	})

	// get_encryption_defaults judges the names alone, before anything reads a
	// value, so a request whose names do not go together is 400 whatever they
	// hold; one whose names go together and hold a value is 501.
	DescribeTable("answers encryption names that do not go together 400 whatever their values, for PutObject, CreateMultipartUpload and UploadPart, per release",
		func(ctx SpecContext, hdr []string, msg string) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				for name, rec := range map[string]*httptest.ResponseRecorder{
					"PutObject":             w.send(w.alice, http.MethodPut, overTLS+"/plain/j", "x", hdr...),
					"CreateMultipartUpload": w.send(w.alice, http.MethodPost, overTLS+"/plain/j?uploads", "", hdr...),
					"UploadPart":            w.send(w.alice, http.MethodPut, overTLS+"/plain/k?uploadId="+id+"&partNumber=1", "x", hdr...),
				} {
					expectError(rec, 400, "InvalidArgument")
					Expect(rec.Body.String()).To(ContainSubstring(msg), "release %d, %s", rel, name)
				}
				Expect(w.stat(ctx, "j").Exists).To(BeFalse(), "release %d", rel)
				Expect(w.uploads(ctx)).To(HaveLen(1), "release %d: only the one this spec began", rel)
				Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d", rel)
			}
		},
		Entry("the algorithm sent empty beside x-amz-server-side-encryption: AES256",
			[]string{sseCAlgorithmHeader, "", sseHeaderName, "AES256"}, mixedGroups),
		Entry("an SSE-C request beside x-amz-server-side-encryption: aws:kms",
			append(sseCRequest(encryptedUploadKey()), sseHeaderName, "aws:kms"), mixedGroups),
		Entry("a key with a value beside an empty KMS key id", []string{sseKeyHeader, "a2V5", sseKMSKeyHeader, ""}, mixedGroups),
		Entry("a KMS key id with a value, alone", []string{sseKMSKeyHeader, "k"}, incompleteKMS),
		Entry("a bucket key with a value, alone", []string{sseBucketKeyHdr, "true"}, incompleteBKE),
		Entry("a context with a value, alone", []string{sseContextHeader, "{}"}, incompleteCtx),
	)

	DescribeTable("keeps 501 for a request whose names go together and that holds a value, for every write, per release",
		func(ctx SpecContext, hdr []string) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				for _, a := range writeAttempts {
					rec := a.send(w, id, "", hdr...)
					Expect(rec.Code).To(Equal(501), "release %d, %s: %s", rel, a.name, rec.Body.String())
					expectError(rec, 501, "NotImplemented")
				}
				expectNothingWritten(ctx, w, id, rel)
			}
		},
		Entry("x-amz-server-side-encryption: AES256", []string{sseHeaderName, "AES256"}),
		Entry("x-amz-server-side-encryption: aws:kms and a KMS key id", []string{sseHeaderName, "aws:kms", sseKMSKeyHeader, "k"}),
		Entry("SSE-C, its three headers", sseCRequest(encryptedUploadKey())),
		Entry("an empty algorithm beside a key with a value, which radosgw's rgw_s3_prepare_encrypt refuses 400 and phase 4 validates",
			[]string{sseCAlgorithmHeader, "", sseKeyHeader, "a2V5"}),
	)

	It("answers names that do not go together before a bucket's default encryption, for PutObject and CreateMultipartUpload, and the default's 501 once they do", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			conflict := []string{sseCAlgorithmHeader, "", sseHeaderName, ""}
			expectError(w.send(w.alice, http.MethodPut, overTLS+"/plain/k", "x", conflict...), 400, "InvalidArgument")
			expectError(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploads", "", conflict...), 400, "InvalidArgument")
			expectError(w.send(w.alice, http.MethodPut, overTLS+"/plain/k", "x"), 501, "NotImplemented")
			expectError(w.send(w.alice, http.MethodPost, overTLS+"/plain/k?uploads", ""), 501, "NotImplemented")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d", rel)
			Expect(w.uploads(ctx)).To(BeEmpty(), "release %d", rel)
		}
	})

	It("answers an empty header 400 before a bucket's default encryption's 501, which radosgw skips for a request with an SSE-C name or x-amz-server-side-encryption", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
			for _, h := range []string{sseCAlgorithmHeader, sseHeaderName} {
				expectError(w.send(w.alice, http.MethodPut, overTLS+"/plain/k", "x", h, ""), 400, "InvalidArgument")
				Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d, %s", rel, h)
			}
		}
	})

	// radosgw's environment keeps the last value of a header sent twice, so a
	// value followed by an empty one is read as empty and the object is stored
	// in the clear. rgw-go judges every instance: any value asks for
	// encryption, and an empty instance of x-amz-server-side-encryption or an
	// SSE-C name is refused unless a value is present, which wins.
	DescribeTable("judges every instance of an encryption header sent more than once, for every write, per release",
		func(ctx SpecContext, hdr []string, status int, code, msg string) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				for _, a := range writeAttempts {
					rec := a.send(w, id, "", hdr...)
					Expect(rec.Code).To(Equal(status), "release %d, %s: %s", rel, a.name, rec.Body.String())
					expectError(rec, status, code)
					Expect(rec.Body.String()).To(ContainSubstring(msg), "release %d, %s", rel, a.name)
				}
				expectNothingWritten(ctx, w, id, rel)
			}
		},
		Entry("x-amz-server-side-encryption: AES256, then empty",
			[]string{sseHeaderName, "AES256", sseHeaderName, ""}, 501, "NotImplemented", ""),
		Entry("x-amz-server-side-encryption: empty, then AES256",
			[]string{sseHeaderName, "", sseHeaderName, "AES256"}, 501, "NotImplemented", ""),
		Entry("x-amz-server-side-encryption: empty twice",
			[]string{sseHeaderName, "", sseHeaderName, ""}, 400, "InvalidArgument", emptySSEMsg),
		Entry("the algorithm: AES256, then empty",
			[]string{sseCAlgorithmHeader, "AES256", sseCAlgorithmHeader, ""}, 501, "NotImplemented", ""),
		Entry("the algorithm: empty, then AES256",
			[]string{sseCAlgorithmHeader, "", sseCAlgorithmHeader, "AES256"}, 501, "NotImplemented", ""),
		Entry("the algorithm: empty twice",
			[]string{sseCAlgorithmHeader, "", sseCAlgorithmHeader, ""}, 400, "InvalidArgument", emptySSECMsg),
		Entry("the key: a value, then empty, beside an empty algorithm",
			[]string{sseCAlgorithmHeader, "", sseKeyHeader, "a2V5", sseKeyHeader, ""}, 501, "NotImplemented", ""),
		Entry("each of the three SSE-C headers: a value, then empty",
			[]string{
				sseCAlgorithmHeader, "AES256", sseCAlgorithmHeader, "", sseKeyHeader, "a2V5", sseKeyHeader, "",
				sseKeyMD5Header, "bQ==", sseKeyMD5Header, "",
			}, 501, "NotImplemented", ""),
		Entry("an empty x-amz-server-side-encryption beside a KMS key id that has a value",
			[]string{sseHeaderName, "", sseKMSKeyHeader, "k"}, 501, "NotImplemented", ""),
		Entry("the copy source's key: a value, then empty",
			[]string{sseCopyKeyHeader, "a2V5", sseCopyKeyHeader, ""}, 501, "NotImplemented", ""),
		Entry("the KMS key id: a value, then empty, beside x-amz-server-side-encryption: aws:kms",
			[]string{sseHeaderName, "aws:kms", sseKMSKeyHeader, "k", sseKMSKeyHeader, ""}, 501, "NotImplemented", ""),
		Entry("an empty KMS key id beside x-amz-server-side-encryption: aws:kms",
			[]string{sseHeaderName, "aws:kms", sseKMSKeyHeader, ""}, 501, "NotImplemented", ""),
	)

	// An empty KMS key id sets s3:x-amz-server-side-encryption-aws-kms-key-id
	// as the SSE name sets its key, so it is refused 400: by get_encryption_defaults'
	// names for the writes that run it, and by its value for a CopyObject.
	It("refuses an empty KMS key id on every write, as a header and as a query parameter, per release", func(ctx SpecContext) {
		const query = "x-amz-server-side-encryption-aws-kms-key-id="
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			id := w.initUpload(w.alice, "/plain/k")
			for _, a := range writeAttempts {
				want := incompleteKMS
				if a.name == "CopyObject" {
					want = emptyKMSMsg
				}
				rec := a.send(w, id, "", sseKMSKeyHeader, "")
				Expect(rec.Code).To(Equal(400), "release %d, %s, header: %s", rel, a.name, rec.Body.String())
				expectError(rec, 400, "InvalidArgument")
				Expect(rec.Body.String()).To(ContainSubstring(want), "release %d, %s, header", rel, a.name)
				if !a.query {
					continue
				}
				rec = a.send(w, id, query)
				Expect(rec.Code).To(Equal(400), "release %d, %s, query: %s", rel, a.name, rec.Body.String())
				Expect(rec.Body.String()).To(ContainSubstring(incompleteKMS), "release %d, %s, query", rel, a.name)
			}
			expectNothingWritten(ctx, w, id, rel)
		}
	})

	It("keeps accepting an empty bucket key or context, which set no condition key, on a CopyObject", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			for _, h := range []string{sseBucketKeyHdr, sseContextHeader} {
				rec := w.send(w.alice, http.MethodPut, overTLS+"/plain/dst", "", "X-Amz-Copy-Source", "/plain/src", h, "")
				Expect(rec.Code).To(Equal(200), "release %d, %s: %s", rel, h, rec.Body.String())
			}
		}
	})

	// map_qs_metadata files each query parameter whose lowercased name starts
	// x-amz-server-side-encryption into the same map as the headers, for
	// PutObject and so for a part, and overrides what it holds.
	DescribeTable("takes a query parameter as it takes the header, for PutObject and its parts, per release",
		func(ctx SpecContext, query string, hdr []string, status int, code, msg string) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				for _, a := range writeAttempts {
					if !a.query {
						continue
					}
					rec := a.send(w, id, query, hdr...)
					Expect(rec.Code).To(Equal(status), "release %d, %s: %s", rel, a.name, rec.Body.String())
					expectError(rec, status, code)
					Expect(rec.Body.String()).To(ContainSubstring(msg), "release %d, %s", rel, a.name)
				}
				Expect(w.stat(ctx, "j").Exists).To(BeFalse(), "release %d", rel)
				Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d", rel)
			}
		},
		Entry("the algorithm, empty", "x-amz-server-side-encryption-customer-algorithm=", nil, 400, "InvalidArgument", emptySSECMsg),
		Entry("the algorithm, empty, in another case, as map_qs_metadata lowercases the name",
			"X-Amz-Server-Side-Encryption-Customer-Algorithm", nil, 400, "InvalidArgument", emptySSECMsg),
		Entry("x-amz-server-side-encryption, empty", "x-amz-server-side-encryption=", nil, 400, "InvalidArgument", emptySSEMsg),
		Entry("x-amz-server-side-encryption with the algorithm header: both groups", "x-amz-server-side-encryption=",
			[]string{sseCAlgorithmHeader, ""}, 400, "InvalidArgument", mixedGroups),
		Entry("x-amz-server-side-encryption with a value in the header and none in the query: radosgw lets the query's empty value override it, and rgw-go refuses the value",
			"x-amz-server-side-encryption=", []string{sseHeaderName, "AES256"}, 501, "NotImplemented", ""),
		Entry("x-amz-server-side-encryption with a value in the query and none in the header",
			"x-amz-server-side-encryption=AES256", []string{sseHeaderName, ""}, 501, "NotImplemented", ""),
		Entry("the algorithm with a value", "x-amz-server-side-encryption-customer-algorithm=AES256", nil, 501, "NotImplemented", ""),
		Entry("the algorithm with a value in the query beside x-amz-server-side-encryption in the header: both groups",
			"x-amz-server-side-encryption-customer-algorithm=AES256", []string{sseHeaderName, "AES256"}, 400, "InvalidArgument", mixedGroups),
		Entry("x-amz-server-side-encryption: AES256, then empty",
			"x-amz-server-side-encryption=AES256&x-amz-server-side-encryption=", nil, 501, "NotImplemented", ""),
		Entry("x-amz-server-side-encryption: empty, then AES256",
			"x-amz-server-side-encryption=&x-amz-server-side-encryption=AES256", nil, 501, "NotImplemented", ""),
		Entry("x-amz-server-side-encryption: empty twice",
			"x-amz-server-side-encryption=&x-amz-server-side-encryption=", nil, 400, "InvalidArgument", emptySSEMsg),
		Entry("x-amz-server-side-encryption: AES256, then empty, in another case",
			"X-Amz-Server-Side-Encryption=AES256&X-Amz-Server-Side-Encryption=", nil, 501, "NotImplemented", ""),
		Entry("the algorithm: AES256, then empty",
			"x-amz-server-side-encryption-customer-algorithm=AES256&x-amz-server-side-encryption-customer-algorithm=", nil, 501, "NotImplemented", ""),
		Entry("the algorithm: empty, then AES256",
			"x-amz-server-side-encryption-customer-algorithm=&x-amz-server-side-encryption-customer-algorithm=AES256", nil, 501, "NotImplemented", ""),
		Entry("the algorithm: empty twice",
			"x-amz-server-side-encryption-customer-algorithm=&x-amz-server-side-encryption-customer-algorithm=", nil, 400, "InvalidArgument", emptySSECMsg),
		Entry("the KMS key id, empty, without x-amz-server-side-encryption",
			"x-amz-server-side-encryption-aws-kms-key-id=", nil, 400, "InvalidArgument", incompleteKMS),
		Entry("the KMS key id: a value, then empty, without x-amz-server-side-encryption",
			"x-amz-server-side-encryption-aws-kms-key-id=k&x-amz-server-side-encryption-aws-kms-key-id=", nil, 400, "InvalidArgument", incompleteKMS),
		Entry("the KMS key id, empty, beside x-amz-server-side-encryption: aws:kms in the header: a value wins",
			"x-amz-server-side-encryption-aws-kms-key-id=", []string{sseHeaderName, "aws:kms"}, 501, "NotImplemented", ""),
	)

	It("leaves the query alone for CreateMultipartUpload and CopyObject, as map_qs_metadata does not run for them", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			id := w.initUpload(w.alice, "/plain/k")
			for _, a := range writeAttempts {
				if a.query {
					continue
				}
				for _, q := range []string{
					"x-amz-server-side-encryption-customer-algorithm=AES256", "x-amz-server-side-encryption-customer-algorithm=",
					"x-amz-server-side-encryption=AES256&x-amz-server-side-encryption=", "x-amz-server-side-encryption-aws-kms-key-id=",
				} {
					rec := a.send(w, id, q)
					Expect(rec.Code).To(Equal(200), "release %d, %s, %s: %s", rel, a.name, q, rec.Body.String())
				}
			}
		}
	})

	// rgw_iam_add_crypt_attrs adds s3:x-amz-server-side-encryption to the
	// policy's environment for a header that is present, empty or not, so a
	// Deny on its Null condition does not apply to a write that sends it
	// empty. rgw-go refuses the empty header before the object is stored.
	Describe("under a bucket policy that denies a write without s3:x-amz-server-side-encryption", func() {
		nullDeny := func(key string) []byte {
			return []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":["arn:aws:iam:::user/alice"]},` +
				`"Action":["s3:PutObject"],"Resource":["arn:aws:s3:::plain/*"],"Condition":{"Null":{"` + key + `":"true"}}}]}`)
		}

		It("answers a write without the header 403, with it empty 400 and with AES256 501, for every write, per release", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: nullDeny("s3:x-amz-server-side-encryption")})
				for _, a := range writeAttempts {
					none := a.send(w, id, "")
					Expect(none.Code).To(Equal(403), "release %d, %s, no header: %s", rel, a.name, none.Body.String())
					empty := a.send(w, id, "", sseHeaderName, "")
					Expect(empty.Code).To(Equal(400), "release %d, %s, empty header: %s", rel, a.name, empty.Body.String())
					Expect(empty.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"), "release %d, %s", rel, a.name)
					valued := a.send(w, id, "", sseHeaderName, "AES256")
					Expect(valued.Code).To(Equal(501), "release %d, %s, AES256: %s", rel, a.name, valued.Body.String())
				}
				expectNothingWritten(ctx, w, id, rel)
			}
		})

		It("answers a write that repeats the header, a value and an empty one, 501 whichever comes last", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: nullDeny("s3:x-amz-server-side-encryption")})
				for _, a := range writeAttempts {
					for _, hdr := range [][]string{
						{sseHeaderName, "AES256", sseHeaderName, ""}, {sseHeaderName, "", sseHeaderName, "AES256"},
					} {
						rec := a.send(w, id, "", hdr...)
						Expect(rec.Code).To(Equal(501), "release %d, %s, %v: %s", rel, a.name, hdr, rec.Body.String())
					}
				}
				expectNothingWritten(ctx, w, id, rel)
			}
		})

		It("answers a write without the KMS key id 403, with it empty 400 and with a value 501, for every write, per release", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: nullDeny("s3:x-amz-server-side-encryption-aws-kms-key-id")})
				for _, a := range writeAttempts {
					none := a.send(w, id, "")
					Expect(none.Code).To(Equal(403), "release %d, %s, no header: %s", rel, a.name, none.Body.String())
					empty := a.send(w, id, "", sseKMSKeyHeader, "")
					Expect(empty.Code).To(Equal(400), "release %d, %s, empty header: %s", rel, a.name, empty.Body.String())
					Expect(empty.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"), "release %d, %s", rel, a.name)
					valued := a.send(w, id, "", sseHeaderName, "aws:kms", sseKMSKeyHeader, "k")
					Expect(valued.Code).To(Equal(501), "release %d, %s, aws:kms: %s", rel, a.name, valued.Body.String())
				}
				expectNothingWritten(ctx, w, id, rel)
			}
		})

		// The specs above run on no named route, whose policy environment never
		// reads the query's encryption parameters. A SigV4 route reads them, so
		// the name sent empty in the query is present to the Null condition there.
		It("answers an encryption name sent empty in the query 400 on a SigV4 route under a Null Deny on its key, for PutObject", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				names := []string{"x-amz-server-side-encryption", "x-amz-server-side-encryption-aws-kms-key-id"}
				if rel >= denc.Tentacle {
					names = append(names, "x-amz-server-side-encryption-customer-algorithm")
				}
				for name, route := range map[string]op.AuthRoute{"SigV4Header": op.RouteSigV4Header, "SigV4Query": op.RouteSigV4Query} {
					for _, key := range names {
						w := newWriteWorld(ctx, rel)
						w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: nullDeny("s3:" + key)})
						none := w.sendAllOn(route, w.alice, http.MethodPut, overTLS+"/plain/j", "x")
						Expect(none.Code).To(Equal(403), "release %d, %s, %s, no parameter: %s", rel, name, key, none.Body.String())
						empty := w.sendAllOn(route, w.alice, http.MethodPut, overTLS+"/plain/j?"+key+"=", "x")
						Expect(empty.Code).To(Equal(400), "release %d, %s, %s, empty parameter: %s", rel, name, key, empty.Body.String())
						Expect(empty.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"), "release %d, %s, %s", rel, name, key)
						Expect(w.stat(ctx, "j").Exists).To(BeFalse(), "release %d, %s, %s", rel, name, key)
					}
				}
			}
		})

		It("answers an empty SSE-C algorithm 400 before a Null condition on its key is evaluated, for PutObject", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: nullDeny("s3:x-amz-server-side-encryption-customer-algorithm")})
				expectError(w.send(w.alice, http.MethodPut, overTLS+"/plain/k", "x"), 403, "AccessDenied")
				expectError(w.send(w.alice, http.MethodPut, overTLS+"/plain/k", "x", sseCAlgorithmHeader, ""), 400, "InvalidArgument")
				Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d", rel)
			}
		})
	})

	// An UploadPart runs get_encryption_defaults in get_params, and
	// rgw_s3_prepare_decrypt over the upload's attrs when it writes, which
	// reads the algorithm only for an SSE-C upload. An upload that stores no
	// crypt mode has nothing for it to read, and rgw-go refuses a part that
	// sends an empty header all the same, once it has read the upload.
	Describe("a part", func() {
		part := func(w *writeWorld, id string, copied bool, hdr ...string) *httptest.ResponseRecorder {
			target := overTLS + "/plain/k?uploadId=" + id + "&partNumber=1"
			if copied {
				return w.sendAll(w.alice, http.MethodPut, target, "", append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/src"}, hdr...)...)
			}
			return w.sendAll(w.alice, http.MethodPut, target, "x", hdr...)
		}

		DescribeTable("of an upload without a crypt mode is stored or refused, per release, uploaded or copied",
			func(ctx SpecContext, c emptyEncryption) {
				for _, rel := range sseReleases {
					for _, copied := range []bool{false, true} {
						w := newWriteWorld(ctx, rel)
						id := w.initUpload(w.alice, "/plain/k")
						rec := part(w, id, copied, c.hdr...)
						if c.status == 200 {
							Expect(rec.Code).To(Equal(200), "release %d, copied %t: %s", rel, copied, rec.Body.String())
							ps := w.parts(ctx, "k", id)
							Expect(ps).To(HaveLen(1), "release %d, copied %t", rel, copied)
							continue
						}
						expectError(rec, c.status, c.code)
						Expect(rec.Body.String()).To(ContainSubstring(c.msg), "release %d, copied %t", rel, copied)
						Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d, copied %t", rel, copied)
					}
				}
			},
			emptyEncryptionEntries(),
		)

		It("of an upload that does not exist is NoSuchUpload, not the refusal of an empty header, which needs the upload", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				expectError(w.send(w.alice, http.MethodPut, overTLS+"/plain/k?uploadId=2~nope&partNumber=1", "x", sseHeaderName, ""), 404, "NoSuchUpload")
			}
		})

		DescribeTable("of an SSE-C upload is decided by the algorithm header as sent, per release, uploaded or copied",
			func(ctx SpecContext, hdr []string, status int, code, msg string) {
				for _, rel := range sseReleases {
					for _, copied := range []bool{false, true} {
						w := newWriteWorld(ctx, rel)
						id := w.beganEncrypted(ctx, "SSE-C-AES256")
						rec := part(w, id, copied, hdr...)
						expectError(rec, status, code)
						Expect(rec.Body.String()).To(ContainSubstring(msg), "release %d, copied %t", rel, copied)
						Expect(w.parts(ctx, "k", id)).To(BeEmpty(), "release %d, copied %t", rel, copied)
					}
				}
			},
			Entry("the header not sent", nil, 400, "InvalidArgument", algorithmMissingMsg),
			Entry("the algorithm sent empty: no AES256", []string{sseCAlgorithmHeader, ""}, 400, "InvalidEncryptionAlgorithmError", ""),
			Entry("the algorithm, the key and the key MD5 sent empty",
				[]string{sseCAlgorithmHeader, "", sseKeyHeader, "", sseKeyMD5Header, ""}, 400, "InvalidEncryptionAlgorithmError", ""),
			Entry("x-amz-server-side-encryption sent empty: the algorithm is still missing", []string{sseHeaderName, ""}, 400, "InvalidArgument", algorithmMissingMsg),
			Entry("the algorithm and x-amz-server-side-encryption sent empty: both groups",
				[]string{sseCAlgorithmHeader, "", sseHeaderName, ""}, 400, "InvalidArgument", mixedGroups),
			Entry("the algorithm sent empty beside a key with a value: 501", []string{sseCAlgorithmHeader, "", sseKeyHeader, "a2V5"}, 501, "NotImplemented", ""),
			Entry("the algorithm sent twice, AES256 then empty: 501", []string{sseCAlgorithmHeader, "AES256", sseCAlgorithmHeader, ""}, 501, "NotImplemented", ""),
		)

		It("never reads the algorithm for an upload of another mode: its own refusal answers", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				for _, copied := range []bool{false, true} {
					w := newWriteWorld(ctx, rel)
					id := w.beganEncrypted(ctx, "SSE-KMS")
					rec := part(w, id, copied, sseCAlgorithmHeader, "")
					expectError(rec, 400, "InvalidArgument")
					Expect(rec.Body.String()).To(ContainSubstring("Failed to retrieve the actual key, kms-keyid: kid"), "release %d, copied %t", rel, copied)
				}
			}
		})

		// A copy's SSE-C source is decided by the copy-source algorithm
		// header on Tentacle and the request's own on Squid.
		DescribeTable("copied from an SSE-C source is decided by the header the release reads",
			func(ctx SpecContext, rel denc.Release, hdr []string, status int, code string) {
				w := newWriteWorld(ctx, rel)
				sum := md5.Sum(encryptedUploadKey()) //nolint:gosec // SSE-C names its key by the key's MD5
				w.put(ctx, "enc", "hello", map[string][]byte{meta.AttrCryptMode: []byte("SSE-C-AES256"), meta.AttrCryptKeyMD5: sum[:]})
				id := w.initUpload(w.alice, "/plain/k")
				rec := w.send(w.alice, http.MethodPut, overTLS+"/plain/k?uploadId="+id+"&partNumber=1", "",
					append([]string{"Content-Length", "0", "X-Amz-Copy-Source", "/plain/enc"}, hdr...)...)
				expectError(rec, status, code)
				Expect(w.parts(ctx, "k", id)).To(BeEmpty())
			},
			Entry("Tentacle, the copy-source algorithm sent empty", denc.Tentacle, []string{sseCCopyAlgorithm, ""}, 400, "InvalidEncryptionAlgorithmError"),
			Entry("Tentacle, the request's own sent empty and not read: the copy-source one is missing", denc.Tentacle,
				[]string{sseCAlgorithmHeader, ""}, 400, "InvalidArgument"),
			Entry("Tentacle, neither sent", denc.Tentacle, nil, 400, "InvalidArgument"),
			Entry("Squid, the request's own sent empty", denc.Squid, []string{sseCAlgorithmHeader, ""}, 400, "InvalidEncryptionAlgorithmError"),
			Entry("Squid, the copy-source one sent empty and not read: the algorithm is missing", denc.Squid,
				[]string{sseCCopyAlgorithm, ""}, 400, "InvalidArgument"),
			Entry("Squid, neither sent", denc.Squid, nil, 400, "InvalidArgument"),
		)

		It("copies an unencrypted source whatever the copy-source headers say, sent empty", func(ctx SpecContext) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := w.initUpload(w.alice, "/plain/k")
				rec := part(w, id, true, sseCCopyAlgorithm, "", sseCopyKeyHeader, "",
					"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key-Md5", "")
				Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
				Expect(w.parts(ctx, "k", id)).To(HaveLen(1), "release %d", rel)
			}
		})

		It("keeps a copy-source header with a value 501", func(ctx SpecContext) {
			w := newWriteWorld(ctx, denc.Tentacle)
			id := w.initUpload(w.alice, "/plain/k")
			expectError(part(w, id, true, sseCCopyAlgorithm, "AES256"), 501, "NotImplemented")
			Expect(w.parts(ctx, "k", id)).To(BeEmpty())
		})
	})
})

// A CompleteMultipartUpload adds the encryption condition keys to the policy's
// environment from its headers, as a PutObject does, and reads no encryption
// header but an SSE-C upload's key. rgw-go refuses a header sent empty that
// emptyMessage covers, once the upload's decision has passed, so that a Null
// Deny written to require encryption does not complete an upload in the
// clear; a header with a value is not asked of a completion (docs/differences.md).
var _ = Describe("an encryption header sent empty on a CompleteMultipartUpload", func() {
	// withPart begins alice's upload of k in plain, with no crypt mode and a
	// part of "x", and returns its id.
	withPart := func(ctx SpecContext, w *writeWorld) string {
		GinkgoHelper()
		id := w.initUpload(w.alice, "/plain/k")
		Expect(w.send(w.alice, http.MethodPut, overTLS+"/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))
		return id
	}
	complete := func(w *writeWorld, id, query string, hdr ...string) *httptest.ResponseRecorder {
		return w.sendAll(w.alice, http.MethodPost, overTLS+withQuery("/plain/k?uploadId="+id, query), completion(md5Hex("x")), hdr...)
	}
	expectNotCompleted := func(ctx SpecContext, w *writeWorld, id string, rel denc.Release) {
		GinkgoHelper()
		Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d: nothing is completed", rel)
		Expect(w.uploads(ctx)).To(HaveLen(1), "release %d: the upload stays", rel)
		Expect(w.parts(ctx, "k", id)).To(HaveLen(1), "release %d: its part stays", rel)
	}

	DescribeTable("is refused or accepted by the rule, for an upload without a crypt mode, per release",
		func(ctx SpecContext, c emptyEncryption) {
			for _, rel := range sseReleases {
				w := newWriteWorld(ctx, rel)
				id := withPart(ctx, w)
				rec := complete(w, id, "", c.hdr...)
				if c.status == 200 {
					Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
					Expect(w.stat(ctx, "k").Attrs).NotTo(HaveKey(meta.AttrCryptMode), "release %d", rel)
					continue
				}
				expectError(rec, c.status, c.code)
				Expect(rec.Body.String()).To(ContainSubstring(c.msg), "release %d", rel)
				Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-server-side-encryption"), "release %d: a refusal echoes nothing", rel)
				expectNotCompleted(ctx, w, id, rel)
			}
		},
		Entry("x-amz-server-side-encryption", refused(emptySSEMsg, sseHeaderName, "")),
		Entry("the KMS key id", refused(emptyKMSMsg, sseKMSKeyHeader, "")),
		Entry("the algorithm", refused(emptySSECMsg, sseCAlgorithmHeader, "")),
		Entry("the key", refused(emptySSECMsg, sseKeyHeader, "")),
		Entry("the key MD5", refused(emptySSECMsg, sseKeyMD5Header, "")),
		Entry("x-amz-server-side-encryption and the KMS key id: the SSE name first", refused(emptySSEMsg, sseHeaderName, "", sseKMSKeyHeader, "")),
		Entry("x-amz-server-side-encryption sent twice, a value then empty: the empty instance is refused, a value being no refusal here",
			refused(emptySSEMsg, sseHeaderName, "AES256", sseHeaderName, "")),
		Entry("the bucket key, which sets no condition key", emptyEncryption{hdr: []string{sseBucketKeyHdr, ""}, status: 200}),
		Entry("the context, which sets no condition key", emptyEncryption{hdr: []string{sseContextHeader, ""}, status: 200}),
		Entry("a name only the first 20 bytes of the prefix match", emptyEncryption{hdr: []string{sseTwentyBytesHdr, ""}, status: 200}),
		Entry("the copy source's algorithm", emptyEncryption{hdr: []string{sseCCopyAlgorithm, ""}, status: 200}),
	)

	It("leaves the query alone, as the policy's environment and map_qs_metadata do not read it for a completion", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			id := withPart(ctx, w)
			rec := complete(w, id, "x-amz-server-side-encryption=&x-amz-server-side-encryption-aws-kms-key-id=")
			Expect(rec.Code).To(Equal(200), "release %d: %s", rel, rec.Body.String())
		}
	})

	It("answers a completion without the header 403 and with it empty 400, under a Null Deny on each key the release sets", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			keys := map[string]string{
				"s3:x-amz-server-side-encryption":                sseHeaderName,
				"s3:x-amz-server-side-encryption-aws-kms-key-id": sseKMSKeyHeader,
			}
			if rel >= denc.Tentacle {
				keys["s3:x-amz-server-side-encryption-customer-algorithm"] = sseCAlgorithmHeader
			}
			for key, header := range keys {
				w := newWriteWorld(ctx, rel)
				id := withPart(ctx, w)
				w.setBucketAttrs(ctx, map[string][]byte{op.AttrIAMPolicy: []byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny",` +
					`"Principal":{"AWS":["arn:aws:iam:::user/alice"]},"Action":["s3:PutObject"],"Resource":["arn:aws:s3:::plain/*"],` +
					`"Condition":{"Null":{"` + key + `":"true"}}}]}`)})
				none := complete(w, id, "")
				Expect(none.Code).To(Equal(403), "release %d, %s, no header: %s", rel, key, none.Body.String())
				empty := complete(w, id, "", header, "")
				Expect(empty.Code).To(Equal(400), "release %d, %s, empty header: %s", rel, key, empty.Body.String())
				Expect(empty.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"), "release %d, %s", rel, key)
				expectNotCompleted(ctx, w, id, rel)
			}
		}
	})

	It("keeps an SSE-C upload's own answer to its key headers, and refuses a header sent empty beside the right key", func(ctx SpecContext) {
		for _, rel := range sseReleases {
			w := newWriteWorld(ctx, rel)
			id := w.beganEncrypted(ctx, "SSE-C-AES256")
			up, err := w.upload(ctx, "k", id)
			Expect(err).NotTo(HaveOccurred())
			_, err = w.store.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Size: 1})
			Expect(err).NotTo(HaveOccurred())
			rec := complete(w, id, "", sseCAlgorithmHeader, "")
			expectError(rec, 400, "InvalidEncryptionAlgorithmError")
			rec = complete(w, id, "", append(sseCRequest(encryptedUploadKey()), sseKMSKeyHeader, "")...)
			expectError(rec, 400, "InvalidArgument")
			Expect(rec.Body.String()).To(ContainSubstring(emptyKMSMsg), "release %d", rel)
			Expect(w.stat(ctx, "k").Exists).To(BeFalse(), "release %d", rel)
		}
	})

	It("echoes nothing when it refuses a completion of an SSE-S3 upload on Tentacle", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		id := w.beganEncrypted(ctx, "AES256")
		up, err := w.upload(ctx, "k", id)
		Expect(err).NotTo(HaveOccurred())
		_, err = w.store.PutPart(ctx, up, 1, strings.NewReader("x"), op.PutParams{Size: 1})
		Expect(err).NotTo(HaveOccurred())
		rec := complete(w, id, "", sseKMSKeyHeader, "")
		expectError(rec, 400, "InvalidArgument")
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-server-side-encryption"))
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})
})
