package s3_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// forgedPreflight is OPTIONS target signed for alice's key id with a secret
// that is not hers, on one of the four signed routes.
type forgedPreflight struct {
	name  string
	build func(ctx context.Context, target string) *http.Request
}

const (
	unverifiedKey    = "AKALICE"
	unverifiedSecret = "not-the-secret"
	v2Expiry         = "1441000000"
)

var unverifiedNow = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

func preflightRequest(ctx context.Context, target string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, http.MethodOptions, "http://s3.example.com"+target, nil)
	req.Header.Set("Origin", "https://example.org")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	return req
}

func forgedV2Signature(sts string) string {
	mac := hmac.New(sha1.New, []byte(unverifiedSecret))
	mac.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

var forgedPreflights = []forgedPreflight{
	{"SigV4 header", func(ctx context.Context, target string) *http.Request {
		req := preflightRequest(ctx, target)
		req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
		creds := aws.Credentials{AccessKeyID: unverifiedKey, SecretAccessKey: unverifiedSecret}
		Expect(v4.NewSigner().SignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", unverifiedNow)).To(Succeed())
		return req
	}},
	{"SigV4 presigned", func(ctx context.Context, target string) *http.Request {
		creds := aws.Credentials{AccessKeyID: unverifiedKey, SecretAccessKey: unverifiedSecret}
		u, _, err := v4.NewSigner().PresignHTTP(ctx, creds, preflightRequest(ctx, target+"&X-Amz-Expires=300"),
			"UNSIGNED-PAYLOAD", "s3", "us-east-1", unverifiedNow)
		Expect(err).NotTo(HaveOccurred())
		parsed, err := url.Parse(u)
		Expect(err).NotTo(HaveOccurred())
		return preflightRequest(ctx, parsed.RequestURI())
	}},
	{"SigV2 header", func(ctx context.Context, target string) *http.Request {
		req := preflightRequest(ctx, target)
		req.Header.Set("Date", "Sun, 30 Aug 2015 12:36:00 GMT")
		req.Header.Set("Authorization", "AWS "+unverifiedKey+":"+forgedV2Signature("GET"))
		return req
	}},
	{"SigV2 presigned", func(ctx context.Context, target string) *http.Request {
		q := "&AWSAccessKeyId=" + unverifiedKey + "&Expires=" + v2Expiry + "&Signature=" + url.QueryEscape(forgedV2Signature("GET"))
		return preflightRequest(ctx, target+q)
	}},
}

// captureErrors sends slog's default logger to a buffer as JSON until the
// spec ends.
func captureErrors() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

// A forged preflight is granted on its key's id alone, so dispatch sending
// it to any route but the preflight would be a full authentication bypass:
// a stray OPTIONS row is the dispatch fault the guard is there for.
var _ = Describe("An unverified identity a stray dispatch row sends past the preflight", func() {
	newHandler := func(ctx context.Context, rel denc.Release) *s3.Handler {
		store := memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return unverifiedNow }})
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "alice"}
		info.AccessKeys = map[string]meta.AccessKey{unverifiedKey: {ID: unverifiedKey, Secret: "secret-alice", Active: true}}
		store.AddUser(info)
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		cfg := auth.DefaultConfig()
		cfg.Now = func() time.Time { return unverifiedNow }
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		return s3.NewHandler(env, auth.New(cfg, store, store), s3.Config{})
	}
	serve := func(h http.Handler, req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	DescribeTable("is refused with 500 InternalError on every signed route, logged with the route and no credentials",
		func(ctx SpecContext, rel denc.Release, ownRoute bool) {
			DeferCleanup(s3.AddOptionsRowForTest(op.ScopeBucket, "ownershipControls", "get_acls"))
			h := newHandler(ctx, rel)
			ran := 0
			if ownRoute {
				// A route that does not run through op.Run: the handler's
				// guard alone must stop it.
				h.Register("get_acls", func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
					ran++
					w.WriteHeader(http.StatusOK)
					return nil
				})
			}
			for _, f := range forgedPreflights {
				logs := captureErrors()
				code, body := serve(h, f.build(ctx, "/plain?ownershipControls"))
				Expect(code).To(Equal(http.StatusInternalServerError), "%s: %s", f.name, body)
				Expect(body).To(ContainSubstring("<Code>InternalError</Code>"), f.name)
				Expect(body).NotTo(ContainSubstring("AccessControlPolicy"), f.name)
				Expect(logs.String()).To(ContainSubstring(`"level":"ERROR"`), f.name)
				Expect(logs.String()).To(ContainSubstring("get_acls"), f.name)
				Expect(logs.String()).NotTo(ContainSubstring(unverifiedKey), f.name)
				Expect(logs.String()).NotTo(ContainSubstring(unverifiedSecret), f.name)
			}
			Expect(ran).To(BeZero())

			code, body := serve(h, forgedPreflights[0].build(ctx, "/plain?acl"))
			Expect(code).To(Equal(http.StatusNotImplemented), "the preflight itself still runs: %s", body)
		},
		Entry("on Squid, through the ACL op", denc.Squid, false),
		Entry("on Tentacle, through the ACL op", denc.Tentacle, false),
		Entry("on Squid, through a route outside op.Run", denc.Squid, true),
		Entry("on Tentacle, through a route outside op.Run", denc.Tentacle, true),
	)
})
