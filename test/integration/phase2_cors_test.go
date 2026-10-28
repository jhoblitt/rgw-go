//go:build integration

package integration_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// corsUser owns the CORS gate's buckets. Its name and keys are fixed, so a
// run that stopped partway leaves it for the next run to remove first.
const corsUser = "cors-gate"

var corsKey = keyPair{access: "CORSGATEEXAMPLEKEY01", secret: "corsGateExampleSecret" + strings.Repeat("0", 18) + "1"}

// corsObject is the public-read object of each gate bucket that the
// object-scope requests name; corsPutKey is the key the signed PUT writes.
const (
	corsObject = "page.html"
	corsPutKey = "upload.bin"
)

// corsGateConfig is internal/cors/testdata/vectors/two-rules.xml, the
// document whose stored form internal/cors's hand-built vector is: a rule
// with two origins, one of them a wildcard, an ID, two methods, one in lower
// case, two allowed headers, a max age and two expose headers, and a rule
// that allows every origin.
func corsGateConfig() *types.CORSConfiguration {
	return &types.CORSConfiguration{CORSRules: []types.CORSRule{
		{
			ID:             aws.String("web"),
			AllowedOrigins: []string{"https://www.example.com", "https://*.example.com"},
			AllowedMethods: []string{"PUT", "get"},
			AllowedHeaders: []string{"X-Amz-*", "Content-Type"},
			MaxAgeSeconds:  aws.Int32(3000),
			ExposeHeaders:  []string{"x-amz-request-id", "ETag"},
		},
		{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET"}},
	}}
}

// corsGateDocument is the GetBucketCors body radosgw renders for
// corsGateConfig: no XML declaration, the methods by its fixed order, the
// origins and allowed headers as their sets sort them, and the expose
// headers in document order (RGWCORSRule_S3::to_xml and
// RGWCORSConfiguration_S3::to_xml,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L32-L74 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors_s3.cc#L166-L175 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L32-L74 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors_s3.cc#L166-L175 at v20.2.4).
const corsGateDocument = `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<CORSRule><ID>web</ID><AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod>` +
	`<AllowedOrigin>https://*.example.com</AllowedOrigin><AllowedOrigin>https://www.example.com</AllowedOrigin>` +
	`<AllowedHeader>Content-Type</AllowedHeader><AllowedHeader>X-Amz-*</AllowedHeader>` +
	`<MaxAgeSeconds>3000</MaxAgeSeconds><ExposeHeader>x-amz-request-id</ExposeHeader><ExposeHeader>ETag</ExposeHeader>` +
	`</CORSRule><CORSRule><AllowedMethod>GET</AllowedMethod><AllowedOrigin>*</AllowedOrigin></CORSRule></CORSConfiguration>`

// corsHeaderNames are the response headers a CORS answer is compared by.
// radosgw never sends Access-Control-Allow-Credentials, only the other five
// (dump_access_control,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L526-L554 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L531-L559 at v20.2.4),
// so it is compared too: no case wants it, and a gateway that sent it fails.
var corsHeaderNames = []string{
	"Access-Control-Allow-Origin", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers",
	"Access-Control-Expose-Headers", "Access-Control-Max-Age", "Access-Control-Allow-Credentials", "Vary",
}

// corsAnswer is a response's status, the error code its body carries when
// the status is an error's, and its CORS headers, each with every value it
// was sent with.
type corsAnswer struct {
	Status  int
	Code    string
	Headers map[string][]string
}

// corsHeaders is the CORS headers of an answer, by name.
type corsHeaders map[string]string

// answer is the corsAnswer of status with h, each header sent once.
func (h corsHeaders) answer(status int) corsAnswer {
	a := corsAnswer{Status: status, Headers: map[string][]string{}}
	for k, v := range h {
		a.Headers[k] = []string{v}
	}
	return a
}

// corsRefused is the answer to a preflight no rule allows: 403 AccessDenied,
// with no CORS header, as radosgw sends the -ENOENT of the rule match as
// -EACCES
// (RGWOptionsCORS_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3861-L3867 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4312-L4318 at v20.2.4).
func corsRefused() corsAnswer {
	a := corsHeaders{}.answer(http.StatusForbidden)
	a.Code = "AccessDenied"
	return a
}

// corsGateway is one gateway as the CORS gate reaches it: its S3 endpoint
// and an S3 client of corsUser's against it.
type corsGateway struct {
	name, base string
	s3         *awss3.Client
}

// corsCase is a request to a gate bucket and the answer both gateways must
// give it.
type corsCase struct {
	what, method string
	// key is the object the request names, "" for the bucket.
	key    string
	header map[string]string
	// signed signs the request as corsUser; it is anonymous otherwise.
	signed bool
	want   corsAnswer
}

// corsExposed is the first rule's expose headers as both gateways send
// them, in document order.
const corsExposed = "x-amz-request-id,ETag"

// corsGateCases are preflights and requests other than a preflight that
// each gateway answers from corsGateConfig as radosgw's floors do. The
// first rule allows its origins' GET and PUT, the requested headers it
// covers, its expose headers and its max age; the second allows any
// origin's GET, which an anonymous request is sent * for. No case needs a
// later rule for its origin, and a rule allows every case's origin, so
// neither "CORS rules are matched as Amazon S3 matches them" nor "A request
// whose Origin no CORS rule allows is refused" applies.
func corsGateCases() []corsCase {
	return []corsCase{
		{
			what: "a preflight from an origin the first rule names, for a method and headers it allows", method: http.MethodOptions, key: corsObject,
			header: map[string]string{
				"Origin": "https://www.example.com", "Access-Control-Request-Method": "PUT",
				"Access-Control-Request-Headers": "x-amz-meta-a, content-type",
			},
			want: corsHeaders{
				"Access-Control-Allow-Origin": "https://www.example.com", "Access-Control-Allow-Methods": "PUT",
				"Access-Control-Allow-Headers": "x-amz-meta-a,content-type", "Access-Control-Expose-Headers": corsExposed,
				"Access-Control-Max-Age": "3000", "Vary": "Origin",
			}.answer(http.StatusOK),
		},
		{
			what: "a preflight from an origin the first rule's wildcard admits", method: http.MethodOptions, key: corsObject,
			header: map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": "GET"},
			want: corsHeaders{
				"Access-Control-Allow-Origin": "https://app.example.com", "Access-Control-Allow-Methods": "GET",
				"Access-Control-Expose-Headers": corsExposed, "Access-Control-Max-Age": "3000", "Vary": "Origin",
			}.answer(http.StatusOK),
		},
		{
			what: "a bucket preflight from an origin only the second rule admits", method: http.MethodOptions,
			header: map[string]string{"Origin": "https://other.test", "Access-Control-Request-Method": "GET"},
			want: corsHeaders{
				"Access-Control-Allow-Origin": "https://other.test", "Access-Control-Allow-Methods": "GET", "Vary": "Origin",
			}.answer(http.StatusOK),
		},
		{
			what: "a preflight for a method its origin's rule does not allow", method: http.MethodOptions, key: corsObject,
			header: map[string]string{"Origin": "https://other.test", "Access-Control-Request-Method": "PUT"},
			want:   corsRefused(),
		},
		{
			what: "a preflight for a header its origin's rule does not allow", method: http.MethodOptions, key: corsObject,
			header: map[string]string{
				"Origin": "https://www.example.com", "Access-Control-Request-Method": "GET",
				"Access-Control-Request-Headers": "x-custom",
			},
			want: corsRefused(),
		},
		{
			what: "an anonymous GET from an origin only the second rule admits", method: http.MethodGet, key: corsObject,
			header: map[string]string{"Origin": "https://other.test"},
			want:   corsHeaders{"Access-Control-Allow-Origin": "*", "Access-Control-Allow-Methods": "GET"}.answer(http.StatusOK),
		},
		{
			what: "a signed GET from an origin the first rule's wildcard admits", method: http.MethodGet, key: corsObject, signed: true,
			header: map[string]string{"Origin": "https://app.example.com"},
			want: corsHeaders{
				"Access-Control-Allow-Origin": "https://app.example.com", "Access-Control-Allow-Methods": "GET",
				"Access-Control-Expose-Headers": corsExposed, "Access-Control-Max-Age": "3000", "Vary": "Origin",
			}.answer(http.StatusOK),
		},
		{
			what: "a signed PUT from an origin the first rule names", method: http.MethodPut, key: corsPutKey, signed: true,
			header: map[string]string{"Origin": "https://www.example.com"},
			want: corsHeaders{
				"Access-Control-Allow-Origin": "https://www.example.com", "Access-Control-Allow-Methods": "PUT",
				"Access-Control-Expose-Headers": corsExposed, "Access-Control-Max-Age": "3000", "Vary": "Origin",
			}.answer(http.StatusOK),
		},
	}
}

// corsSign signs req as corsUser with SigV4 over an unsigned payload.
func corsSign(ctx context.Context, req *http.Request) error {
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	return v4.NewSigner().SignHTTP(ctx, corsKey.creds(), req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now())
}

// corsPutBody is what g's signed PUT writes, so that the object read back
// tells which gateway's write it holds.
func corsPutBody(g corsGateway) string {
	return "written through " + g.name + " with an Origin"
}

// corsSend sends c to g for bucket and returns its answer and its body.
func corsSend(ctx context.Context, g corsGateway, bucket string, c corsCase) (corsAnswer, []byte, error) {
	path := "/" + bucket
	if c.key != "" {
		path += "/" + c.key
	}
	var body io.Reader = http.NoBody
	if c.method == http.MethodPut {
		body = strings.NewReader(corsPutBody(g))
	}
	req, err := http.NewRequestWithContext(ctx, c.method, g.base+path, body)
	if err != nil {
		return corsAnswer{}, nil, fmt.Errorf("building %s %s for %s: %w", c.method, path, g.name, err)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	if c.signed {
		if err = corsSign(ctx, req); err != nil {
			return corsAnswer{}, nil, fmt.Errorf("signing %s %s for %s: %w", c.method, path, g.name, err)
		}
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		return corsAnswer{}, nil, fmt.Errorf("%s %s on %s: %w", c.method, path, g.name, err)
	}
	b, err := io.ReadAll(resp.Body)
	err = errors.Join(err, resp.Body.Close())
	if err != nil {
		return corsAnswer{}, nil, fmt.Errorf("reading %s %s on %s: %w", c.method, path, g.name, err)
	}
	a := corsAnswer{Status: resp.StatusCode, Headers: map[string][]string{}}
	if resp.StatusCode >= http.StatusBadRequest {
		var e struct{ Code string }
		if err = xml.Unmarshal(b, &e); err != nil {
			return corsAnswer{}, nil, fmt.Errorf("the error body of %s %s on %s: %w", c.method, path, g.name, err)
		}
		a.Code = e.Code
	}
	for _, name := range corsHeaderNames {
		if v := resp.Header.Values(name); len(v) > 0 {
			a.Headers[name] = v
		}
	}
	return a, b, nil
}

// expectPutWritten requires bucket's corsPutKey, read through radosgw, to
// hold what g's signed PUT wrote.
func expectPutWritten(ctx context.Context, radosgw, g corsGateway, bucket string) {
	GinkgoHelper()
	out, err := radosgw.s3.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(corsPutKey)})
	Expect(err).NotTo(HaveOccurred(), "reading %s/%s through radosgw after %s's PUT", bucket, corsPutKey, g.name)
	b, err := io.ReadAll(out.Body)
	Expect(errors.Join(err, out.Body.Close())).NotTo(HaveOccurred())
	Expect(string(b)).To(Equal(corsPutBody(g)), "%s/%s after %s's PUT", bucket, corsPutKey, g.name)
}

// expectSameCORS sends each case to both gateways and requires the same
// answer from both, and the one the case wants, and each PUT's object.
func expectSameCORS(ctx context.Context, radosgw, rgwgo corsGateway, bucket string) {
	GinkgoHelper()
	for _, c := range corsGateCases() {
		want, wantBody, err := corsSend(ctx, radosgw, bucket, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(want).To(Equal(c.want), "%s: radosgw's answer; body %s", c.what, wantBody)
		// rgw-go's PUT writes the same key, so radosgw's is read back first.
		if c.method == http.MethodPut {
			expectPutWritten(ctx, radosgw, radosgw, bucket)
		}
		got, gotBody, err := corsSend(ctx, rgwgo, bucket, c)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want), "%s: rgw-go's answer against radosgw's; radosgw's body %s, rgw-go's %s", c.what, wantBody, gotBody)
		if c.method == http.MethodPut {
			expectPutWritten(ctx, radosgw, rgwgo, bucket)
		}
	}
}

// corsDocument is g's GetBucketCors of bucket: its status and its body as
// sent.
func corsDocument(ctx context.Context, g corsGateway, bucket string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+"/"+bucket+"?cors", http.NoBody)
	if err != nil {
		return 0, "", fmt.Errorf("building GetBucketCors of %s for %s: %w", bucket, g.name, err)
	}
	if err = corsSign(ctx, req); err != nil {
		return 0, "", fmt.Errorf("signing GetBucketCors of %s for %s: %w", bucket, g.name, err)
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("GetBucketCors of %s on %s: %w", bucket, g.name, err)
	}
	b, err := io.ReadAll(resp.Body)
	err = errors.Join(err, resp.Body.Close())
	if err != nil {
		return 0, "", fmt.Errorf("reading GetBucketCors of %s on %s: %w", bucket, g.name, err)
	}
	return resp.StatusCode, string(b), nil
}

// expectCORSDocument waits for g's GetBucketCors of bucket to answer
// corsGateDocument: the other gateway's write reaches g's cache through its
// notify.
func expectCORSDocument(ctx context.Context, g corsGateway, bucket string) {
	GinkgoHelper()
	Eventually(func(g2 Gomega) string {
		status, body, err := corsDocument(ctx, g, bucket)
		g2.Expect(err).NotTo(HaveOccurred())
		g2.Expect(status).To(Equal(http.StatusOK), "body %s", body)
		return body
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal(corsGateDocument),
		"GetBucketCors of %s on %s", bucket, g.name)
}

// expectNoCORS waits for g's GetBucketCors of bucket to answer 404
// NoSuchCORSConfiguration.
func expectNoCORS(ctx context.Context, g corsGateway, bucket string) {
	GinkgoHelper()
	Eventually(func(g2 Gomega) int {
		status, body, err := corsDocument(ctx, g, bucket)
		g2.Expect(err).NotTo(HaveOccurred())
		g2.Expect(body).To(ContainSubstring("<Code>NoSuchCORSConfiguration</Code>"))
		return status
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal(http.StatusNotFound),
		"GetBucketCors of %s on %s", bucket, g.name)
}

// bucketAttrs is the attrs of bucket's instance as RADOS holds them.
func bucketAttrs(ctx context.Context, bucket string) map[string][]byte {
	GinkgoHelper()
	rec, err := store.GetBucket(ctx, "", bucket)
	Expect(err).NotTo(HaveOccurred())
	oid := rec.Info.Bucket.InstanceOID()
	rop := radosclient.NewReadOp()
	x := rop.GetXattrs()
	_, err = pool(ctx, store.ZoneParams().DomainRoot).Read(ctx, oid, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s's xattrs", oid)
	Expect(x.Err).NotTo(HaveOccurred())
	return x.Xattrs
}

// corsBucket creates a public-read bucket of corsUser's through radosgw
// holding the public-read corsObject. corsUser's removal removes it.
func corsBucket(ctx context.Context, radosgw corsGateway) string {
	GinkgoHelper()
	name := randName("cors")
	_, err := radosgw.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name), ACL: types.BucketCannedACLPublicRead})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	_, err = radosgw.s3.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(name), Key: aws.String(corsObject), Body: strings.NewReader("<html>a page</html>"),
		ACL: types.ObjectCannedACLPublicRead,
	})
	Expect(err).NotTo(HaveOccurred(), "writing %s/%s through radosgw", name, corsObject)
	return name
}

// removeCORSUser removes corsUser with its buckets and objects.
func removeCORSUser(ctx context.Context) {
	if _, err := radosgwAdmin(ctx, "user", "info", "--uid", corsUser); err == nil {
		tryAdmin(ctx, "user", "rm", "--uid", corsUser, "--purge-data")
	}
}

// trimCORSUsage removes corsUser's usage records.
func trimCORSUsage(ctx context.Context) {
	tryAdmin(ctx, "usage", "trim", "--uid", corsUser, "--yes-i-really-mean-it")
}

// The CORS gate writes one configuration through each gateway and requires
// the other to read it, both to answer the same preflights and requests
// from it, and the attr each writes to be the one radosgw writes, byte for
// byte.
var _ = Describe("the CORS gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "cors"), func() {
	var (
		gateways map[string]corsGateway
		// vector is internal/cors's hand-built stored form of
		// corsGateConfig.
		vector []byte
	)

	BeforeAll(func(ctx SpecContext) {
		removeCORSUser(ctx)
		trimCORSUsage(ctx)
		gcProcess(ctx)
		// Cleanups run last registered first, each whatever the others did,
		// so the garbage is collected once the user and its usage are gone.
		DeferCleanup(gcProcess)
		DeferCleanup(trimCORSUsage)
		DeferCleanup(removeCORSUser)
		createUser(ctx, corsUser, corsKey)
		base := rgwgoAdminEndpoint()
		gateways = map[string]corsGateway{
			"radosgw": {name: "radosgw", base: endpoint, s3: s3At(endpoint, corsKey)},
			"rgw-go":  {name: "rgw-go", base: base, s3: s3At(base, corsKey)},
		}
		var err error
		vector, err = os.ReadFile(filepath.Join("..", "..", "internal", "cors", "testdata", "vectors", "two-rules.bin"))
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("serves one gateway's CORS configuration from the other's endpoint, from one attr",
		func(ctx SpecContext, writer, reader string) {
			w, rd := gateways[writer], gateways[reader]
			radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
			bucket := corsBucket(ctx, radosgw)
			// Each gateway caches the bucket here, so the other's write reaches
			// it only through that gateway's notify.
			expectNoCORS(ctx, rd, bucket)
			expectNoCORS(ctx, w, bucket)

			By("writing the configuration through " + writer)
			_, err := w.s3.PutBucketCors(ctx, &awss3.PutBucketCorsInput{Bucket: aws.String(bucket), CORSConfiguration: corsGateConfig()})
			Expect(err).NotTo(HaveOccurred())
			Expect(bucketAttrs(ctx, bucket)).To(HaveKeyWithValue(op.AttrCORS, vector),
				"the %s %s wrote, against the stored form radosgw writes", op.AttrCORS, writer)
			expectCORSDocument(ctx, rd, bucket)
			expectCORSDocument(ctx, w, bucket)
			expectSameCORS(ctx, radosgw, rgwgo, bucket)

			By("writing it again through " + reader)
			_, err = rd.s3.PutBucketCors(ctx, &awss3.PutBucketCorsInput{Bucket: aws.String(bucket), CORSConfiguration: corsGateConfig()})
			Expect(err).NotTo(HaveOccurred())
			Expect(bucketAttrs(ctx, bucket)).To(HaveKeyWithValue(op.AttrCORS, vector),
				"the %s %s wrote over %s's", op.AttrCORS, reader, writer)

			By("removing it through " + reader)
			_, err = rd.s3.DeleteBucketCors(ctx, &awss3.DeleteBucketCorsInput{Bucket: aws.String(bucket)})
			Expect(err).NotTo(HaveOccurred())
			Expect(bucketAttrs(ctx, bucket)).NotTo(HaveKey(op.AttrCORS), "after %s's DeleteBucketCors", reader)
			expectNoCORS(ctx, w, bucket)
			expectNoCORS(ctx, rd, bucket)
		},
		Entry("written through radosgw", "radosgw", "rgw-go"),
		Entry("written through rgw-go", "rgw-go", "radosgw"),
	)
})
