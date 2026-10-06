//go:build integration

package cli_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/frontend"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// serveManifest is the part of populate.sh's manifest.json these specs read:
// the realm, zonegroup and zone Rook created.
type serveManifest struct{ Realm, Zonegroup, Zone string }

// emptyListing is radosgw's answer to an anonymous GET /, Rook's probe.
const emptyListing = `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
	`<Owner><ID>anonymous</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`

// freeAddr is a loopback address whose port was free a moment ago.
func freeAddr(ctx context.Context) string {
	GinkgoHelper()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	addr := ln.Addr().String()
	Expect(ln.Close()).To(Succeed())
	return addr
}

// fetch sends method to url and returns the response with its body read.
func fetch(ctx context.Context, c *http.Client, method, url string, h http.Header) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, "", err
	}
	maps.Copy(req.Header, h)
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	body, err := io.ReadAll(resp.Body)
	return resp, string(body), errors.Join(err, resp.Body.Close())
}

// writeSelfSigned writes a self-signed certificate for 127.0.0.1 and its key
// to one PEM file, as Rook lays a TLS secret out for radosgw, and returns
// the certificate.
func writeSelfSigned(path string) *x509.Certificate {
	GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rgw-go"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	Expect(err).NotTo(HaveOccurred())
	out := slices.Concat(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	Expect(os.WriteFile(path, out, 0o600)).To(Succeed())
	cert, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())
	return cert
}

var _ = Describe("serve against a cluster", Label("integration"), Ordered, func() {
	var (
		bin     string
		conf    string
		m       serveManifest
		release string
		client  *http.Client
	)

	BeforeAll(func(ctx SpecContext) {
		conf = cephtest.Conf()
		cephtest.ReadManifest(conf, &m)
		Expect(m.Zone).NotTo(BeEmpty(), "manifest.json names the zone")

		built, err := gexec.Build("github.com/jhoblitt/rgw-go/cmd/rgw-go", "-tags=ceph_preview")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(gexec.CleanupBuildArtifacts)
		// Named radosgw, the binary takes its whole command line as ceph's,
		// as Rook launches it.
		b, err := os.ReadFile(built)
		Expect(err).NotTo(HaveOccurred())
		bin = filepath.Join(GinkgoT().TempDir(), "radosgw")
		Expect(os.WriteFile(bin, b, 0o755)).To(Succeed())

		cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
		name, err := cluster.RequiredOSDRelease(ctx)
		Expect(err).NotTo(HaveOccurred())
		rel, ok := denc.ClusterRelease(ctx, name)
		Expect(ok).To(BeTrue(), "required osd release %q", name)
		release = rel.String()

		client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	})

	// start runs the binary with Rook's argv shape, frontends as its
	// rgw_frontends and metricsAddr as RGW_GO_METRICS_ADDR, and kills it when
	// the spec or container that started it ends, unless it has exited.
	start := func(frontends, metricsAddr string, extra ...string) *gexec.Session {
		GinkgoHelper()
		args := slices.Concat([]string{
			"--foreground", "--name=client.admin", "-c", conf,
			"--default-log-to-stderr=true", "--default-err-to-stderr=true",
			"--rgw-frontends=" + frontends,
			"--rgw-realm=" + m.Realm, "--rgw-zonegroup=" + m.Zonegroup, "--rgw-zone=" + m.Zone,
		}, extra)
		// Not exec.CommandContext: a process a BeforeAll starts outlives
		// that node's context, and DeferCleanup ends it.
		cmd := exec.Command(bin, args...) //nolint:noctx // see above
		cmd.Env = append(os.Environ(), "RGW_GO_METRICS_ADDR="+metricsAddr, "RGW_GO_LOG_LEVEL=debug")
		s, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { s.Kill().Wait(10 * time.Second) })
		return s
	}

	// probe waits for base to answer Rook's probe.
	probe := func(ctx context.Context, c *http.Client, base string) {
		GinkgoHelper()
		Eventually(func(g Gomega) {
			resp, body, err := fetch(ctx, c, http.MethodGet, base+"/", nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(resp.StatusCode).To(Equal(http.StatusOK))
			g.Expect(body).To(Equal(emptyListing))
		}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())
	}

	Context("on a plain endpoint with the metrics listener", Ordered, func() {
		var base, metricsBase string

		BeforeAll(func(ctx SpecContext) {
			addr, maddr := freeAddr(ctx), freeAddr(ctx)
			start("beast endpoint="+addr, maddr)
			base, metricsBase = "http://"+addr, "http://"+maddr
			probe(ctx, client, base)
		})

		It("answers Rook's probe with the empty listing, its request id and Server header", func(ctx SpecContext) {
			resp, body, err := fetch(ctx, client, http.MethodGet, base+"/", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(body).To(Equal(emptyListing))
			Expect(resp.Header.Get("x-amz-request-id")).To(MatchRegexp(`^tx[0-9a-f]{21}-[0-9a-f]{10}-[0-9]+-%s$`, regexp.QuoteMeta(m.Zone)))
			Expect(resp.Header.Get("Server")).To(Equal("Ceph Object Gateway (" + release + ")"))
		})

		It("answers 501 NotImplemented for an op it does not serve yet, a bucket's CORS preflight", func(ctx SpecContext) {
			resp, body, err := fetch(ctx, client, http.MethodOptions, base+"/plain", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusNotImplemented))
			Expect(body).To(ContainSubstring("<Code>NotImplemented</Code>"))
			Expect(body).To(ContainSubstring("<BucketName>plain</BucketName>"))
			Expect(body).To(MatchRegexp(`<HostId>[0-9]+-%s-%s</HostId>`, regexp.QuoteMeta(m.Zone), regexp.QuoteMeta(m.Zonegroup)))
		})

		It("answers 403 InvalidAccessKeyId for a signed request with a key the cluster has never seen", func(ctx SpecContext) {
			h := http.Header{
				"Authorization": {"AWS4-HMAC-SHA256 Credential=AKNOBODY/20150830/us-east-1/s3/aws4_request, " +
					"SignedHeaders=host;x-amz-date, Signature=" + strings.Repeat("0", 64)},
				"X-Amz-Date": {time.Now().UTC().Format("20060102T150405Z")},
			}
			resp, body, err := fetch(ctx, client, http.MethodGet, base+"/", h)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
			Expect(body).To(ContainSubstring("<Code>InvalidAccessKeyId</Code>"))
		})

		It("answers 405 for POST /", func(ctx SpecContext) {
			resp, _, err := fetch(ctx, client, http.MethodPost, base+"/", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
		})

		It("serves the request and RADOS metrics and the heap profile", func(ctx SpecContext) {
			resp, body, err := fetch(ctx, client, http.MethodGet, metricsBase+"/metrics", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(body).To(ContainSubstring(`rgw_go_requests_total{op="list_buckets",status="2xx"}`))
			Expect(body).To(ContainSubstring(`rgw_go_rados_ops_in_flight{mode="callback"} 0`))

			resp, body, err = fetch(ctx, client, http.MethodGet, metricsBase+"/debug/pprof/heap?debug=1", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(body).To(HavePrefix("heap profile:"))
		})
	})

	It("answers Rook's probe over TLS with the certificate and key in one file", func(ctx SpecContext) {
		cert := filepath.Join(GinkgoT().TempDir(), "rgw-cert.pem")
		roots := x509.NewCertPool()
		roots.AddCert(writeSelfSigned(cert))
		addr := freeAddr(ctx)
		start("beast ssl_endpoint="+addr+" ssl_certificate="+cert, "")
		// rgw-probe.sh skips verification; trusting the one certificate
		// written also shows it is the one served.
		tlsClient := &http.Client{Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		}}
		probe(ctx, tlsClient, "https://"+addr)
	})

	It("finishes a request inside its handler when SIGTERM arrives, then exits 0 within the drain", func(ctx SpecContext) {
		addr, maddr := freeAddr(ctx), freeAddr(ctx)
		s := start("beast endpoint="+addr, maddr)
		probe(ctx, client, "http://"+addr)

		// A CPU profile holds its request in the handler for the seconds it
		// asks for.
		type answer struct {
			resp *http.Response
			body string
			err  error
		}
		held := make(chan answer, 1)
		go func() {
			resp, body, err := fetch(ctx, client, http.MethodGet, "http://"+maddr+"/debug/pprof/profile?seconds=3", nil)
			held <- answer{resp, body, err}
		}()
		Eventually(func(g Gomega) {
			_, stacks, err := fetch(ctx, client, http.MethodGet, "http://"+maddr+"/debug/pprof/goroutine?debug=2", nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(stacks).To(ContainSubstring("net/http/pprof.Profile("))
		}).WithContext(ctx).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())

		s.Signal(syscall.SIGTERM)
		var a answer
		Eventually(held).WithTimeout(frontend.DrainTimeout).WithPolling(100 * time.Millisecond).Should(Receive(&a))
		Expect(a.err).NotTo(HaveOccurred())
		Expect(a.resp.StatusCode).To(Equal(http.StatusOK))
		Expect(a.body).NotTo(BeEmpty(), "the profile")
		Eventually(s).WithTimeout(frontend.DrainTimeout).WithPolling(100 * time.Millisecond).Should(gexec.Exit(0))
	})

	It("keeps serving on SIGHUP and SIGUSR1, as radosgw does, and stops on SIGTERM", func(ctx SpecContext) {
		addr := freeAddr(ctx)
		s := start("beast endpoint="+addr, "")
		probe(ctx, client, "http://"+addr)

		s.Signal(syscall.SIGHUP)
		Eventually(s.Err).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).
			Should(gbytes.Say(`"msg":"ignoring signal","signal":"hangup"`))
		s.Signal(syscall.SIGUSR1)
		Eventually(s.Err).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).
			Should(gbytes.Say(`"msg":"ignoring signal","signal":"user defined signal 1"`))
		probe(ctx, client, "http://"+addr)
		Expect(s.ExitCode()).To(Equal(-1), "still running")

		s.Signal(syscall.SIGTERM)
		Eventually(s).WithTimeout(frontend.DrainTimeout).WithPolling(100 * time.Millisecond).Should(gexec.Exit(0))
	})

	It("answers 405 on / and serves the admin API when rgw_enable_apis leaves S3 out", func(ctx SpecContext) {
		addr := freeAddr(ctx)
		start("beast endpoint="+addr, "", "--rgw-enable-apis=swift,admin")
		Eventually(func(g Gomega) {
			resp, body, err := fetch(ctx, client, http.MethodGet, "http://"+addr+"/", nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(resp.StatusCode).To(Equal(http.StatusMethodNotAllowed))
			g.Expect(body).To(ContainSubstring("<Code>MethodNotAllowed</Code>"))
		}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(250 * time.Millisecond).Should(Succeed())

		// An anonymous identity holds no info cap: AccessDenied, in the
		// admin handlers' default format.
		resp, body, err := fetch(ctx, client, http.MethodGet, "http://"+addr+"/admin/info", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(body).To(MatchRegexp(`^\{"Code":"AccessDenied","Message":"","RequestId":"tx[0-9a-f]{21}-[0-9a-f]{10}-[0-9]+-%s","HostId":"[0-9]+-%s-%s"\}$`,
			regexp.QuoteMeta(m.Zone), regexp.QuoteMeta(m.Zone), regexp.QuoteMeta(m.Zonegroup)))
	})
})
