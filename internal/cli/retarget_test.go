package cli_test

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/cli"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// A signature covers the path and query req_info splits from the target.
// These specs take a request alice signed for GetObject on bucket/key, put
// the same path and query in an absolute-form target whose query holds the
// path, and send it through the router, the S3 handler and the verifier: it
// must be served as that GetObject or refused, never as another op.
var _ = Describe("a signed request moved into an absolute-form target", func() {
	const (
		accessKey = "AKALICE"
		secret    = "secret-alice"
		dnsName   = "s3.example.com"
	)
	type served struct{ route, bucket, object string }
	var (
		h           http.Handler
		seen        chan served
		signingTime time.Time
		forms       []string
	)

	BeforeEach(func() {
		signingTime = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
		forms = []string{"SigV4 presigned", "SigV2 presigned", "SigV4 header", "SigV2 header"}
		store := memstore.New(memstore.Config{Release: denc.Squid})
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "alice"}
		info.AccessKeys = map[string]meta.AccessKey{accessKey: {ID: accessKey, Secret: secret, Active: true}}
		store.AddUser(info)
		env := &op.Env{
			Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store,
			Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
		}
		cfg := s3.Config{DNSNames: []string{dnsName}}
		acfg := auth.DefaultConfig()
		acfg.HostNames = cfg.HostNames()
		acfg.Now = func() time.Time { return signingTime }
		s3h := s3.NewHandler(env, auth.New(acfg, store, store), cfg)
		seen = make(chan served, 8)
		for _, route := range []string{"get_obj", "get_acls", "get_obj_tags", "list_buckets", "get_usage", "list_bucket", "get_bucket_policy"} {
			s3h.Register(route, func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
				seen <- served{route, r.Bucket, r.Object.Name}
				w.WriteHeader(http.StatusOK)
				return nil
			})
		}
		h = cli.NewRouter(s3h, env, cfg, nil)
	})

	// sign returns the query and headers alice's client sends for GET path
	// on host, in the given form.
	sign := func(ctx context.Context, form, host, path string) (string, http.Header) {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+path, nil)
		creds := aws.Credentials{AccessKeyID: accessKey, SecretAccessKey: secret}
		signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
		resource := path
		if b, ok := strings.CutSuffix(host, "."+dnsName); ok {
			resource = "/" + b + path
		}
		v2 := func(date string) string {
			mac := hmac.New(sha1.New, []byte(secret))
			mac.Write([]byte("GET\n\n\n" + date + "\n" + resource))
			return base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		switch form {
		case "SigV4 presigned":
			req.URL.RawQuery = "X-Amz-Expires=300"
			u, _, err := signer.PresignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", signingTime)
			Expect(err).NotTo(HaveOccurred())
			parsed, err := url.Parse(u)
			Expect(err).NotTo(HaveOccurred())
			return parsed.RawQuery, http.Header{}
		case "SigV2 presigned":
			expires := strconv.FormatInt(signingTime.Unix()+300, 10)
			return "AWSAccessKeyId=" + accessKey + "&Expires=" + expires + "&Signature=" + url.QueryEscape(v2(expires)), http.Header{}
		case "SigV4 header":
			req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
			Expect(signer.SignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", signingTime)).To(Succeed())
			return "", req.Header
		case "SigV2 header":
			date := signingTime.Format(http.TimeFormat)
			return "", http.Header{"Date": {date}, "Authorization": {"AWS " + accessKey + ":" + v2(date)}}
		}
		Fail("no form " + form)
		return "", nil
	}

	// send reads the request line and headers as the server does, serves
	// them through the router, and returns the route that served them, or
	// the status and body when none did.
	send := func(target, host string, hdr http.Header) served {
		var b strings.Builder
		fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\n", target, host)
		for k, vs := range hdr {
			for _, v := range vs {
				fmt.Fprintf(&b, "%s: %s\r\n", k, v)
			}
		}
		b.WriteString("\r\n")
		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(b.String())))
		Expect(err).NotTo(HaveOccurred(), "net/http reads %q", target)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		select {
		case s := <-seen:
			return s
		default:
			return served{route: fmt.Sprintf("%d %s", rec.Code, strings.TrimSpace(rec.Body.String()))}
		}
	}

	// Each target is a format string for the signed query. Every form is
	// sent before the comparison, so a failure shows what served each.
	DescribeTable("is served as the GetObject it was signed for",
		func(ctx SpecContext, host, path, target string) {
			got, want := map[string]served{}, map[string]served{}
			for _, form := range forms {
				query, hdr := sign(ctx, form, host, path)
				got[form] = send(fmt.Sprintf(target, query), host, hdr)
				want[form] = served{"get_obj", "bucket", "key"}
			}
			Expect(got).To(Equal(want))
		},
		Entry("as it was signed", dnsName, "/bucket/key", "/bucket/key?%s"),
		Entry("its path in the query, which net/http reads as no path", dnsName, "/bucket/key", "http://s3.example.com?/bucket/key?%s"),
		Entry("its path in the query after an unsigned usage argument", dnsName, "/bucket/key", "http://s3.example.com?usage&x/bucket/key?%s"),
		Entry("virtual-hosted, its path in the query", "bucket.s3.example.com", "/key", "http://bucket.s3.example.com?x/key?%s"),
		Entry("virtual-hosted, its path in the query after an unsigned policy argument", "bucket.s3.example.com", "/key",
			"http://bucket.s3.example.com?policy&x/key?%s"),
		// An object's sub-resource ahead of the path selects an object op if
		// the arguments are read from anything but the query auth signs.
		Entry("its path in the query after an unsigned acl argument", dnsName, "/bucket/key", "http://s3.example.com?acl&x/bucket/key?%s"),
		Entry("virtual-hosted, its path in the query after an unsigned tagging argument", "bucket.s3.example.com", "/key",
			"http://bucket.s3.example.com?tagging&x/key?%s"),
	)

	// SigV4 signs a bad hex digit's empty decode as "/", the same as a
	// ListObjects on the bucket, which radosgw would serve as ListBuckets.
	It("is refused when its path holds a bad hex digit", func(ctx SpecContext) {
		got, want := map[string]served{}, map[string]served{}
		for _, form := range forms {
			query, hdr := sign(ctx, form, "bucket.s3.example.com", "/")
			got[form] = send("http://bucket.s3.example.com?x/%zz?"+query, "bucket.s3.example.com", hdr)
			want[form] = served{route: "400 400 Bad Request"}
		}
		Expect(got).To(Equal(want))
	})
})
