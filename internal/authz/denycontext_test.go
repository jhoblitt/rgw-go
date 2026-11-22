package authz_test

import (
	"net/http"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// contextRequest is a request on priv/o for action a, authenticated on
// route, over TLS when secure, with query and header name, value pairs, sent
// with method, GET when empty. A check on in, when set, is on that key of
// priv, as a copy's source or a DeleteObjects key is, rather than on priv/o.
type contextRequest struct {
	a      policy.Action
	route  op.AuthRoute
	secure bool
	query  string
	hdr    []string
	method string
	in     *meta.ObjKey
	// onState, when set, has the check on priv/o's state read at
	// stateInstance, as a copy's or UploadPartCopy's source object check is.
	onState       bool
	stateInstance string
	// formSigV4 marks a browser upload's form signed by Signature Version 4.
	formSigV4 bool
}

// The aws-chunked forms of x-amz-content-sha256 whose chunks carry no
// signature and whose chunks are signed.
const (
	streamingUnsigned = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	streamingSigned   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
)

// A bucket policy's Deny reads the keys Amazon S3's request context holds
// where radosgw's holds none or another value, and refuses where its
// condition holds on either; an Allow reads radosgw's alone
// (docs/differences.md, "A Deny reads the condition keys Amazon S3 sets").
var _ = Describe("a policy on the keys Amazon S3's request context sets", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var f *fixture

			// The clock stands at noon on 2026-11-20, 1795089600 seconds.
			BeforeEach(func() {
				f = newFixture(rel)
				cfg := evalConfig(rel)
				cfg.Now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
				f.eval = authz.New(cfg)
				f.env.Authz = f.eval
			})

			// send checks q for id, as the op for its action does.
			send := func(id op.Identity, q contextRequest) error {
				GinkgoHelper()
				r := f.req(id, "priv", "o", q.hdr...)
				r.AuthRoute, r.TLS, r.FormSigV4 = q.route, q.secure, q.formSigV4
				vals, err := url.ParseQuery(q.query)
				Expect(err).NotTo(HaveOccurred())
				r.Query = vals
				if q.method != "" {
					r.Method = q.method
				}
				switch {
				case q.in != nil:
					return f.eval.VerifyBucketIn(f.ctx, r, q.a, acl.PermFor(q.a), r.BucketRec, *q.in)
				case q.onState:
					st := f.state(r.BucketRec, "o")
					st.Key.Instance = q.stateInstance
					return f.eval.VerifyObjectIn(f.ctx, r, q.a, acl.PermFor(q.a), r.BucketRec, st)
				case q.a == policy.S3PutObject:
					r.Method = http.MethodPut
					return f.eval.VerifyBucket(f.ctx, r, q.a, acl.PermWrite)
				}
				return f.eval.VerifyObject(f.ctx, r, q.a, acl.PermRead)
			}

			// Alice owns priv, and its ACL lets her in unless the Deny
			// refuses her; nothing but the Allow lets bob in.
			DescribeTable("refuses where Amazon S3's request context or radosgw's meets the Deny, and grants where radosgw's meets the Allow",
				func(cond string, q contextRequest, refused, granted bool) {
					on := `"arn:aws:s3:::priv/*"`
					deny := `{"Effect": "Deny", "Principal": "*", "Action": "` + q.a.String() + `", "Resource": ` + on +
						`, "Condition": ` + cond + `}`
					f.setPolicy("priv", doc(deny))
					if refused {
						Expect(send(alice(), q)).To(MatchError(op.ErrAccessDenied), "under the Deny")
					} else {
						Expect(send(alice(), q)).To(Succeed(), "under the Deny")
					}
					allow := `{"Effect": "Allow", "Principal": {"AWS": "arn:aws:iam:::user/bob"}, "Action": "` +
						q.a.String() + `", "Resource": ` + on + `, "Condition": ` + cond + `}`
					f.setPolicy("priv", doc(allow))
					if granted {
						Expect(send(bob(), q)).To(Succeed(), "under the Allow")
					} else {
						Expect(send(bob(), q)).To(MatchError(op.ErrAccessDenied), "under the Allow")
					}
				},
				Entry("aws:SecureTransport false, on a plain request",
					`{"Bool": {"aws:SecureTransport": "false"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, true, false),
				Entry("aws:SecureTransport false, on a secure request",
					`{"Bool": {"aws:SecureTransport": "false"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header, secure: true}, false, false),
				Entry("aws:SecureTransport true, on a plain request",
					`{"Bool": {"aws:SecureTransport": "true"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, false, false),
				Entry("aws:SecureTransport true, on a secure request",
					`{"Bool": {"aws:SecureTransport": "true"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header, secure: true}, true, true),

				Entry("a Numeric operator on aws:EpochTime that the request's seconds meet",
					`{"NumericGreaterThan": {"aws:EpochTime": "1700000000"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, true, false),
				Entry("a Numeric operator on aws:EpochTime that the request's seconds fail",
					`{"NumericLessThan": {"aws:EpochTime": "1700000000"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, false, false),
				Entry("a Numeric operator on aws:CurrentTime, which radosgw sets in seconds",
					`{"NumericGreaterThan": {"aws:CurrentTime": "1700000000"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, true, true),
				Entry("a Date operator on aws:CurrentTime that the request's time meets",
					`{"DateGreaterThan": {"aws:CurrentTime": "2026-01-01T00:00:00Z"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, true, true),
				Entry("a Date operator on aws:EpochTime that the request's time meets",
					`{"DateGreaterThan": {"aws:EpochTime": "2026-01-01T00:00:00Z"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, true, true),
				Entry("a Date operator on aws:EpochTime that the request's time fails",
					`{"DateLessThan": {"aws:EpochTime": "2026-01-01T00:00:00Z"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, false, false),
				Entry("a pattern on aws:CurrentTime that Amazon S3's date and time meets",
					`{"StringLike": {"aws:CurrentTime": "2026-11-20T*"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, true, false),

				Entry("a negated operator on s3:authType, on the route it names",
					`{"StringNotEquals": {"s3:authType": "REST-HEADER"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, false, false),
				Entry("a negated operator on s3:authType, on another route",
					`{"StringNotEquals": {"s3:authType": "REST-HEADER"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Query}, true, false),
				Entry("a plain operator on s3:authType, on the route it names",
					`{"StringEquals": {"s3:authType": "REST-QUERY-STRING"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV2Query}, true, false),
				Entry("a plain operator on s3:authType, on another route",
					`{"StringEquals": {"s3:authType": "REST-QUERY-STRING"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV2Header}, false, false),

				Entry("a plain operator on the unsigned payload, for a header-signed upload that does not sign it",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD"}},
					true, false),
				Entry("a plain operator on the unsigned payload, for an upload whose hash holds it among other text",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"}},
					true, false),
				Entry("a plain operator on the unsigned payload, for a presigned upload",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query}, true, false),
				Entry("a plain operator on the unsigned payload, for an aws-chunked upload with unsigned chunks",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{
						a: policy.S3PutObject, route: op.RouteSigV4Header,
						hdr: []string{"X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER"},
					}, true, false),
				Entry("a plain operator on the unsigned payload, for an aws-chunked upload with signed chunks",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{
						a: policy.S3PutObject, route: op.RouteSigV4Header,
						hdr: []string{"X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"},
					}, false, false),
				Entry("a plain operator on the unsigned payload, for a signed one",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "e3b0c442"}},
					false, false),
				Entry("a negated operator on the unsigned payload, for a signed one",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "e3b0c442"}},
					true, false),
				Entry("a negated operator on the unsigned payload, for a header-signed upload that does not sign it",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD"}},
					false, false),
				Entry("a negated operator on the unsigned payload, for a presigned upload, read without the key too",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query}, true, false),
				Entry("a negated operator on the payload hash, for a SigV2 upload, which has none",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV2Header}, true, false),

				// The open readings: a presigned upload's payload hash is
				// UNSIGNED-PAYLOAD or absent, and an unsigned form's other than
				// UNSIGNED-PAYLOAD is UNSIGNED-PAYLOAD or as sent.
				Entry("a negated operator on the unsigned payload, for an aws-chunked upload with unsigned chunks, read as sent too",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", streamingUnsigned}},
					true, false),
				Entry("a negated operator on the unsigned payload, for an aws-chunked upload with signed chunks",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", streamingSigned}},
					true, false),
				Entry("a negated operator on the unsigned payload, for a header-signed upload whose hash holds it among other text",
					`{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"}},
					true, false),
				Entry("a plain operator on the unsigned aws-chunked form, for an upload sending it",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", streamingUnsigned}},
					true, false),
				Entry("a plain operator on the unsigned aws-chunked form, for a presigned upload sending it",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query, hdr: []string{"X-Amz-Content-Sha256", streamingUnsigned}},
					true, false),
				Entry("a plain operator on the unsigned aws-chunked form, for a presigned upload sending no payload hash",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query}, false, false),
				Entry("a plain operator on the unsigned aws-chunked form, for an aws-chunked upload with signed chunks",
					`{"StringEquals": {"s3:x-amz-content-sha256": "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", streamingSigned}},
					false, false),
				Entry("a plain operator on a prefixed spelling, for an upload sending it",
					`{"StringEquals": {"s3:x-amz-content-sha256": "XUNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"}},
					true, false),
				Entry("a pattern on every aws-chunked form, for an upload with unsigned chunks",
					`{"StringLike": {"s3:x-amz-content-sha256": "STREAMING-*"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", streamingUnsigned}},
					true, false),
				Entry("a pattern on every aws-chunked form, for an upload with signed chunks",
					`{"StringLike": {"s3:x-amz-content-sha256": "STREAMING-*"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", streamingSigned}},
					true, false),
				Entry("a pattern on every aws-chunked form, for a presigned upload sending no payload hash",
					`{"StringLike": {"s3:x-amz-content-sha256": "STREAMING-*"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query}, false, false),
				Entry("a plain operator on UNSIGNED-PAYLOAD, for a presigned upload whose header holds the body's hash",
					`{"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query, hdr: []string{"X-Amz-Content-Sha256", "e3b0c442"}},
					false, false),

				Entry("a plain operator on the storage class, for an upload asking for it",
					`{"StringEquals": {"s3:x-amz-storage-class": "STANDARD_IA"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Storage-Class", "STANDARD_IA"}},
					true, false),
				Entry("a plain operator on the storage class, for an upload asking for another",
					`{"StringEquals": {"s3:x-amz-storage-class": "STANDARD_IA"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Storage-Class", "STANDARD"}},
					false, false),
				Entry("a negated operator on the storage class, for an upload asking for it",
					`{"StringNotEquals": {"s3:x-amz-storage-class": "STANDARD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Storage-Class", "STANDARD"}},
					false, false),
				Entry("a negated operator on the storage class, for an upload asking for another",
					`{"StringNotEquals": {"s3:x-amz-storage-class": "STANDARD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Storage-Class", "STANDARD_IA"}},
					true, false),
				Entry("a negated operator on the storage class, for an upload asking for none",
					`{"StringNotEquals": {"s3:x-amz-storage-class": "STANDARD"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header}, true, false),

				Entry("a plain operator on the version, for the version it names",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, query: "versionId=v1"}, true, false),
				Entry("a plain operator on the version, for another",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, query: "versionId=v2"}, false, false),
				Entry("a negated operator on the version, for the version it names",
					`{"StringNotEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, query: "versionId=v1"}, false, false),
				Entry("a negated operator on the version, for another",
					`{"StringNotEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, query: "versionId=v2"}, true, false),
				Entry("a plain operator on the version, for a GET naming it beside a copy source header it has no use for",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, query: "versionId=v1",
						hdr: []string{"X-Amz-Copy-Source", "/priv/o"},
					}, true, false),
				Entry("a plain operator on the version, for a HEAD naming it beside a copy source header it has no use for",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, query: "versionId=v1", method: http.MethodHead,
						hdr: []string{"X-Amz-Copy-Source", "/priv/o"},
					}, true, false),
				Entry("a plain operator on the version, for a tagging PUT naming it beside a copy source header it has no use for",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3PutObjectVersionTagging, route: op.RouteSigV4Header, query: "tagging&versionId=v1", method: http.MethodPut,
						hdr: []string{"X-Amz-Copy-Source", "/priv/o"},
					}, true, false),
				Entry("a plain operator on the version, for a copy of it",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, method: http.MethodPut,
						hdr: []string{"X-Amz-Copy-Source", "/priv/o?versionId=v1"}, in: &meta.ObjKey{Name: "o", Instance: "v1"},
					}, true, false),
				Entry("a plain operator on the version, for a copy of another, whatever version the query names",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, method: http.MethodPut, query: "versionId=v1",
						hdr: []string{"X-Amz-Copy-Source", "/priv/o?versionId=v2"}, in: &meta.ObjKey{Name: "o", Instance: "v2"},
					}, false, false),
				Entry("a negated operator on the version, for a copy of another, whatever version the query names",
					`{"StringNotEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, method: http.MethodPut, query: "versionId=v1",
						hdr: []string{"X-Amz-Copy-Source", "/priv/o?versionId=v2"}, in: &meta.ObjKey{Name: "o", Instance: "v2"},
					}, true, false),
				Entry("a plain operator on the version, for a copy source's state read at it",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, method: http.MethodPut,
						hdr: []string{"X-Amz-Copy-Source", "/priv/o?versionId=v1"}, onState: true, stateInstance: "v1",
					}, true, false),
				Entry("a plain operator on the null version, for a copy source's state read at it, whose id is empty",
					`{"StringEquals": {"s3:VersionId": "null"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, method: http.MethodPut,
						hdr: []string{"X-Amz-Copy-Source", "/priv/o?versionId=null"}, onState: true,
					}, true, false),
				Entry("a plain operator on the version, for a copy source's state read at another",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3GetObjectVersion, route: op.RouteSigV4Header, method: http.MethodPut, query: "versionId=v1",
						hdr: []string{"X-Amz-Copy-Source", "/priv/o?versionId=v2"}, onState: true, stateInstance: "v2",
					}, false, false),
				Entry("a plain operator on the version, for a DeleteObjects key naming it",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3DeleteObjectVersion, route: op.RouteSigV4Header, method: http.MethodPost, query: "delete",
						in: &meta.ObjKey{Name: "o", Instance: "v1"},
					}, true, false),
				Entry("a plain operator on the version, for a DeleteObjects key naming another, whatever version the query names",
					`{"StringEquals": {"s3:VersionId": "v1"}}`,
					contextRequest{
						a: policy.S3DeleteObjectVersion, route: op.RouteSigV4Header, method: http.MethodPost, query: "delete&versionId=v1",
						in: &meta.ObjKey{Name: "o", Instance: "v2"},
					}, false, false),

				Entry("a plain operator on s3:signatureversion, for SigV2",
					`{"StringEquals": {"s3:signatureversion": "AWS"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV2Header}, true, false),
				Entry("a plain operator on s3:signatureversion, for SigV4",
					`{"StringEquals": {"s3:signatureversion": "AWS"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, false, false),
				Entry("a negated operator on s3:signatureversion, for a SigV2 presigned URL",
					`{"StringNotEquals": {"s3:signatureversion": "AWS4-HMAC-SHA256"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV2Query}, true, false),
				Entry("a negated operator on s3:signatureversion, for a SigV4 presigned URL",
					`{"StringNotEquals": {"s3:signatureversion": "AWS4-HMAC-SHA256"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Query}, false, false),
				Entry("a plain operator on s3:signatureversion, for a browser upload whose form SigV2 signs",
					`{"StringEquals": {"s3:signatureversion": "AWS"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload}, true, false),
				Entry("a plain operator on s3:signatureversion, for a browser upload whose form SigV4 signs",
					`{"StringEquals": {"s3:signatureversion": "AWS"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload, formSigV4: true}, false, false),
				Entry("a negated operator on s3:signatureversion, for a browser upload whose form SigV4 signs",
					`{"StringNotEquals": {"s3:signatureversion": "AWS4-HMAC-SHA256"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload, formSigV4: true}, false, false),
				Entry("a negated operator on s3:signatureversion, for a browser upload whose form SigV2 signs",
					`{"StringNotEquals": {"s3:signatureversion": "AWS4-HMAC-SHA256"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload}, true, false),
				Entry("a negated operator on s3:signatureversion, for an anonymous request, which has none",
					`{"StringNotEquals": {"s3:signatureversion": "AWS4-HMAC-SHA256"}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteAnonymous}, true, false),

				Entry("a plain operator on s3:authType, for a browser upload",
					`{"StringEquals": {"s3:authType": "POST"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload}, true, false),
				Entry("a negated operator on s3:authType, for a browser upload",
					`{"StringNotEquals": {"s3:authType": "POST"}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload}, false, false),

				Entry("a negated operator on s3:authType over two values, for a route it names",
					`{"StringNotEquals": {"s3:authType": ["REST-HEADER", "REST-QUERY-STRING"]}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV4Header}, false, false),
				Entry("a negated operator on s3:authType over two values, for the other route it names",
					`{"StringNotEquals": {"s3:authType": ["REST-HEADER", "REST-QUERY-STRING"]}}`,
					contextRequest{a: policy.S3GetObject, route: op.RouteSigV2Query}, false, false),
				Entry("a negated operator on s3:authType over two values, for a route it does not name",
					`{"StringNotEquals": {"s3:authType": ["REST-HEADER", "REST-QUERY-STRING"]}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload}, true, false),
				Entry("a negated operator on the storage class over two values, for one it names",
					`{"StringNotEquals": {"s3:x-amz-storage-class": ["STANDARD", "STANDARD_IA"]}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Storage-Class", "STANDARD"}},
					false, false),
				Entry("a negated operator on the storage class over two values, for one it does not name",
					`{"StringNotEquals": {"s3:x-amz-storage-class": ["STANDARD", "STANDARD_IA"]}}`,
					contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Storage-Class", "GLACIER"}},
					true, false),
			)

			// The unsigned-payload Deny reaches every SigV4 upload whose payload
			// no signature covers, presigned ones included; a browser upload has
			// no payload hash, and needs a Deny on POST beside it.
			It("refuses presigned uploads under the unsigned-payload Deny, and browser uploads only with a Deny on POST beside it", func() {
				unsigned := `{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::priv/*",` +
					` "Condition": {"StringEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}}`
				byForm := `{"Effect": "Deny", "Principal": "*", "Action": "s3:PutObject", "Resource": "arn:aws:s3:::priv/*",` +
					` "Condition": {"StringEquals": {"s3:authType": "POST"}}}`
				presigned := contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Query}
				browser := contextRequest{a: policy.S3PutObject, route: op.RouteBrowserUpload}
				signed := contextRequest{a: policy.S3PutObject, route: op.RouteSigV4Header, hdr: []string{"X-Amz-Content-Sha256", "e3b0c442"}}
				f.setPolicy("priv", doc(unsigned))
				Expect(send(alice(), presigned)).To(MatchError(op.ErrAccessDenied), "a presigned upload under the payload's Deny alone")
				Expect(send(alice(), browser)).To(Succeed(), "a browser upload under the payload's Deny alone")
				f.setPolicy("priv", doc(unsigned, byForm))
				Expect(send(alice(), browser)).To(MatchError(op.ErrAccessDenied), "a browser upload")
				Expect(send(alice(), signed)).To(Succeed(), "an upload signed in the header with its payload")
			})
		})
	}
})
