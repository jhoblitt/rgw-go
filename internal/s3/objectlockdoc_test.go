package s3_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// lockDocTargets are a lock document's targets in plain under subresource
// sub: k, which exists, a key that does not, and a version of k that does
// not.
func lockDocTargets(sub string) map[string]string {
	return map[string]string{
		"an existing key":   "/plain/k?" + sub,
		"a missing key":     "/plain/nope?" + sub,
		"a missing version": "/plain/k?" + sub + "&versionId=nope",
	}
}

// firstResponseLine sends a PUT of target to srv over a raw connection,
// declaring a 200-byte body it never sends and expecting 100 Continue, and
// returns the first line the server answers.
func firstResponseLine(ctx context.Context, srv *httptest.Server, target string) string {
	GinkgoHelper()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(c.Close()).To(Succeed()) }()
	_, err = c.Write([]byte("PUT " + target + " HTTP/1.1\r\nHost: localhost\r\n" +
		"Content-Length: 200\r\nExpect: 100-continue\r\n\r\n"))
	Expect(err).NotTo(HaveOccurred())
	Expect(c.SetReadDeadline(time.Now().Add(5 * time.Second))).To(Succeed())
	line, err := bufio.NewReader(c).ReadString('\n')
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(line)
}

var _ = Describe("a refused requester's PutObjectRetention and PutObjectLegalHold", func() {
	for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
		Describe("on "+rel.String(), func() {
			var (
				w *subresWorld
				// requesters are the two whom plain refuses both actions: bob,
				// who holds no grant, and the anonymous user.
				requesters map[string]s3.Authenticator
				docs       map[string]string
			)
			BeforeEach(func(ctx SpecContext) {
				w = newSubresWorld(ctx, rel)
				lockBucket(ctx, w)
				requesters = map[string]s3.Authenticator{
					"bob": authAs(w.bob),
					"the anonymous user": s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
						return &op.AuthResult{Identity: op.Anonymous(), Route: op.RouteAnonymous}, nil
					}),
				}
				docs = map[string]string{"retention": retentionInForce, "legal-hold": legalHoldBody("ON")}
			})
			expectNoLock := func(ctx context.Context) {
				GinkgoHelper()
				for _, key := range []meta.ObjKey{{Name: "k"}, {Name: "k", Instance: "v1"}} {
					st, err := w.store.StatObject(ctx, w.bucket(ctx), key)
					Expect(err).NotTo(HaveOccurred())
					Expect(st.Attrs).NotTo(Or(HaveKey(meta.AttrObjectRetention), HaveKey(meta.AttrObjectLegalHold)), key.Instance)
				}
			}

			It("reads the whole document for an existing key, a missing key and a missing version alike, answering each 403", func(ctx SpecContext) {
				for who, auth := range requesters {
					h := s3.NewHandler(w.env, auth, testConfig(s3.Config{}))
					for sub, doc := range docs {
						for name, target := range lockDocTargets(sub) {
							counted := &countingBody{r: strings.NewReader(doc)}
							rec := serveReq(h, http.MethodPut, target, counted)
							Expect(rec.Code).To(Equal(http.StatusForbidden), "%s, %s, %s: %s", who, sub, name, rec.Body.String())
							Expect(counted.n).To(Equal(len(doc)), "%s, %s, %s", who, sub, name)
						}
					}
				}
				expectNoLock(ctx)
			})
			It("sends an existing key, a missing key and a missing version the same first line under Expect: 100-continue", func(ctx SpecContext) {
				for who, auth := range requesters {
					srv := httptest.NewServer(s3.NewHandler(w.env, auth, testConfig(s3.Config{})))
					DeferCleanup(srv.Close)
					for sub := range docs {
						for name, target := range lockDocTargets(sub) {
							Expect(firstResponseLine(ctx, srv, target)).To(Equal("HTTP/1.1 100 Continue"), "%s, %s, %s", who, sub, name)
						}
					}
				}
			})
		})
	}
})
