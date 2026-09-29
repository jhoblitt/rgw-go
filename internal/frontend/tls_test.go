package frontend_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/frontend"
)

// writeCertificate writes a self-signed ECDSA certificate for the loopback
// addresses into dir: with its key in the same PEM when together is set, as
// Rook's rgw-cert.pem holds both, or else in a PEM of its own. It returns the
// paths, the key's empty when together, and a pool trusting the certificate.
func writeCertificate(dir string, together bool) (certFile, keyFile string, roots *x509.CertPool) {
	GinkgoHelper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rgw-go frontend suite"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	leaf, err := x509.ParseCertificate(der)
	Expect(err).NotTo(HaveOccurred())
	roots = x509.NewCertPool()
	roots.AddCert(leaf)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	Expect(err).NotTo(HaveOccurred())
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	certFile = filepath.Join(dir, "rgw-cert.pem")
	if together {
		Expect(os.WriteFile(certFile, append(certPEM, keyPEM...), 0o600)).To(Succeed())
		return certFile, "", roots
	}
	keyFile = filepath.Join(dir, "rgw-key.pem")
	Expect(os.WriteFile(certFile, certPEM, 0o600)).To(Succeed())
	Expect(os.WriteFile(keyFile, keyPEM, 0o600)).To(Succeed())
	return certFile, keyFile, roots
}

// loaded matches a configuration holding the suite's certificate.
func loaded() OmegaMatcher {
	return HaveField("Certificates", ConsistOf(HaveField("Leaf.Subject.CommonName", "rgw-go frontend suite")))
}

var _ = Describe("TLSConfig", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("loads the certificate and key from one PEM, as Rook writes rgw-cert.pem", func() {
		cert, _, _ := writeCertificate(dir, true)
		Expect(frontend.TLSConfig(frontend.Spec{SSLCertificate: cert})).To(loaded())
	})

	It("loads the key from ssl_private_key's PEM", func() {
		cert, key, _ := writeCertificate(dir, false)
		Expect(frontend.TLSConfig(frontend.Spec{SSLCertificate: cert, SSLPrivateKey: key})).To(loaded())
	})

	It("takes the key from the certificate's PEM when ssl_private_key cannot be loaded, as radosgw falls back", func() {
		cert, _, _ := writeCertificate(dir, true)
		Expect(frontend.TLSConfig(frontend.Spec{SSLCertificate: cert, SSLPrivateKey: filepath.Join(dir, "missing.pem")})).
			To(loaded())
	})

	DescribeTable("refuses a certificate it cannot load, naming the key",
		func(spec func(dir string) frontend.Spec, key string) {
			_, err := frontend.TLSConfig(spec(dir))
			Expect(err).To(MatchError(ContainSubstring(key)))
		},
		Entry("a missing file", func(dir string) frontend.Spec {
			return frontend.Spec{SSLCertificate: filepath.Join(dir, "missing.pem")}
		}, "ssl_certificate="),
		Entry("a certificate with no key in its PEM or ssl_private_key's", func(dir string) frontend.Spec {
			cert, _, _ := writeCertificate(dir, false)
			return frontend.Spec{SSLCertificate: cert, SSLPrivateKey: filepath.Join(dir, "missing.pem")}
		}, "ssl_private_key="),
		Entry("a config:// certificate, which radosgw reads from the monitors' config-key store", func(string) frontend.Spec {
			return frontend.Spec{SSLCertificate: "config://rgw/cert/realm/zone.crt"}
		}, "ssl_certificate=config://"),
		Entry("a config:// key", func(dir string) frontend.Spec {
			cert, _, _ := writeCertificate(dir, true)
			return frontend.Spec{SSLCertificate: cert, SSLPrivateKey: "config://rgw/cert/realm/zone.key"}
		}, "ssl_private_key=config://"),
	)

	DescribeTable("needs ssl_certificate to name a file, naming the TLS listener that has none",
		func(spec frontend.Spec, want string) {
			_, err := frontend.TLSConfig(spec)
			Expect(err).To(MatchError(want))
		},
		Entry("an ssl_port, whose certificate radosgw would read from rgw_frontend_defaults' config:// source",
			frontend.Spec{SSLPorts: []int{443}, SSLPrivateKey: "/k.pem"},
			"no ssl_certificate configured for ssl_port: rgw-go does not read the config:// certificate rgw_frontend_defaults names"),
		Entry("an ssl_endpoint",
			frontend.Spec{SSLEndpoints: []string{"127.0.0.1:443"}},
			"no ssl_certificate configured for ssl_endpoint: rgw-go does not read the config:// certificate rgw_frontend_defaults names"),
		Entry("no TLS listener", frontend.Spec{SSLOptions: []string{"no_tlsv1_2"}}, "no ssl_certificate configured"),
	)

	Context("with a certificate", func() {
		var cert string

		BeforeEach(func() {
			cert, _, _ = writeCertificate(dir, true)
		})

		DescribeTable("applies ssl_options as radosgw sets its ssl context's options",
			func(options []string, minVersion uint16, ignored []string) {
				logs := captureLogs()
				cfg, err := frontend.TLSConfig(frontend.Spec{SSLCertificate: cert, SSLOptions: options})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.MinVersion).To(Equal(minVersion))
				Expect(cfg.MaxVersion).To(BeZero(), "TLS 1.3 stays on")
				var logged []any
				for _, line := range logs.lines() {
					Expect(line).To(HaveKeyWithValue("msg", "ignoring unknown ssl option"))
					logged = append(logged, line["option"])
				}
				Expect(logged).To(ConsistOf(ignored))
			},
			Entry("absent, radosgw's default of no_sslv2:no_sslv3:no_tlsv1:no_tlsv1_1", nil, uint16(tls.VersionTLS12), []string{}),
			Entry("no_tlsv1_2, which raises the floor to TLS 1.3", []string{"no_tlsv1_2"}, uint16(tls.VersionTLS13), []string{}),
			Entry("every other option radosgw knows, which changes nothing under rgw-go's TLS 1.2 floor",
				[]string{"default_workarounds", "no_compression", "no_sslv2", "no_sslv3", "no_tlsv1", "no_tlsv1_1", "single_dh_use"},
				uint16(tls.VersionTLS12), []string{}),
			Entry("an unknown option, which radosgw logs and ignores", []string{"bogus", "no_tlsv1_2"}, uint16(tls.VersionTLS13), []string{"bogus"}),
			Entry("no_tlsv1_3, which radosgw does not know either", []string{"no_tlsv1_3"}, uint16(tls.VersionTLS12), []string{"no_tlsv1_3"}),
		)

		DescribeTable("selects each of Go's TLS 1.2 AEAD suites by its OpenSSL or IANA name",
			func(name string, id uint16) {
				cfg, err := frontend.TLSConfig(frontend.Spec{SSLCertificate: cert, SSLCiphers: []string{name}})
				Expect(err).NotTo(HaveOccurred())
				Expect(cfg.CipherSuites).To(Equal([]uint16{id}))
			},
			EntryDescription("%[1]s"),
			Entry(nil, "ECDHE-ECDSA-AES128-GCM-SHA256", tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
			Entry(nil, "ECDHE-RSA-AES128-GCM-SHA256", tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256),
			Entry(nil, "ECDHE-ECDSA-AES256-GCM-SHA384", tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384),
			Entry(nil, "ECDHE-RSA-AES256-GCM-SHA384", tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384),
			Entry(nil, "ECDHE-ECDSA-CHACHA20-POLY1305", tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256),
			Entry(nil, "ECDHE-RSA-CHACHA20-POLY1305", tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256),
			Entry(nil, "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384", tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384),
			Entry(nil, "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256", tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256),
		)

		It("skips and logs a cipher it does not know, as SSL_CTX_set_cipher_list skips one", func() {
			logs := captureLogs()
			cfg, err := frontend.TLSConfig(frontend.Spec{SSLCertificate: cert, SSLCiphers: []string{"ECDHE-RSA-AES128-GCM-SHA256", "UNKNOWN"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(cfg.CipherSuites).To(Equal([]uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}))
			Expect(logs.lines()).To(ConsistOf(
				And(HaveKeyWithValue("msg", "skipping unknown ssl cipher"), HaveKeyWithValue("cipher", "UNKNOWN")),
			))
		})

		DescribeTable("refuses ssl_ciphers that select no suite, as SSL_CTX_set_cipher_list does",
			func(ciphers []string) {
				captureLogs()
				_, err := frontend.TLSConfig(frontend.Spec{SSLCertificate: cert, SSLCiphers: ciphers})
				Expect(err).To(MatchError(ContainSubstring("no cipher could be selected from ssl_ciphers")))
			},
			Entry("an unknown name", []string{"UNKNOWN"}),
			Entry("an empty list", []string{}),
			Entry("an OpenSSL cipher expression, which Go cannot evaluate", []string{"HIGH", "!aNULL", "!MD5"}),
			Entry("the RSA key exchange suites Go lists as insecure", []string{"AES128-GCM-SHA256", "AES256-GCM-SHA384"}),
			Entry("a CBC suite, outside Go's AEAD suites", []string{"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA"}),
		)
	})
})

var _ = Describe("New", func() {
	DescribeTable("refuses a TLS listener without ssl_certificate, naming it",
		func(spec frontend.Spec, key string) {
			_, err := frontend.New(spec, echoMethod)
			Expect(err).To(MatchError(ContainSubstring("no ssl_certificate configured for " + key + ":")))
		},
		Entry("an ssl_port", frontend.Spec{Ports: []int{80}, SSLPorts: []int{443}}, "ssl_port"),
		Entry("an ssl_endpoint", frontend.Spec{SSLEndpoints: []string{"127.0.0.1:443"}}, "ssl_endpoint"),
	)

	It("does not load the certificate without a TLS listener, as init_ssl does not", func() {
		captureLogs()
		_, err := frontend.New(frontend.Spec{Ports: []int{80}, SSLCertificate: "/nonexistent/rgw-cert.pem"}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts ssl_* keys without a TLS listener, where they do nothing, and logs them as unused", func() {
		logs := captureLogs()
		_, err := frontend.New(frontend.Spec{
			Ports:         []int{80},
			SSLPrivateKey: "/k.pem",
			SSLOptions:    []string{"bogus"},
			SSLCiphers:    []string{"HIGH", "!aNULL"},
			Unknown:       map[string][]string{"ssl_ciphersuites": {"TLS_AES_128_GCM_SHA256"}},
		}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
		Expect(logs.lines()).To(ConsistOf(
			And(HaveKeyWithValue("msg", "ignoring ssl keys without a TLS listener"),
				HaveKeyWithValue("keys", Equal([]any{"ssl_private_key", "ssl_options", "ssl_ciphers"}))),
			HaveKeyWithValue("msg", "ssl_ciphersuites is not configurable in Go, its TLS 1.3 cipher suites apply"),
		))
	})
})

var _ = Describe("Server over TLS", func() {
	var (
		cert  string
		roots *x509.CertPool
	)

	BeforeEach(func() {
		cert, _, roots = writeCertificate(GinkgoT().TempDir(), true)
	})

	It("serves https to a client trusting the certificate", func(ctx SpecContext) {
		s := start(frontend.Spec{SSLEndpoints: []string{"127.0.0.1:0"}, SSLCertificate: cert}, echoMethod)
		serve(ctx, s)
		Expect(get(ctx, newClient(&tls.Config{RootCAs: roots}), "https://"+s.Addrs()[0].String()+"/")).
			To(And(HaveHTTPStatus(http.StatusOK), HaveHTTPBody("GET")))
	})

	It("answers HTTP/1.1 to a client offering h2, as beast speaks nothing else", func(ctx SpecContext) {
		s := start(frontend.Spec{SSLEndpoints: []string{"127.0.0.1:0"}, SSLCertificate: cert}, echoMethod)
		serve(ctx, s)
		res, err := get(ctx, newClient(&tls.Config{RootCAs: roots}), "https://"+s.Addrs()[0].String()+"/")
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Proto).To(Equal("HTTP/1.1"))
	})

	It("refuses a TLS 1.2 client under ssl_options=no_tlsv1_2", func(ctx SpecContext) {
		s := start(frontend.Spec{
			SSLEndpoints:   []string{"127.0.0.1:0"},
			SSLCertificate: cert,
			SSLOptions:     []string{"no_tlsv1_2"},
		}, echoMethod)
		serve(ctx, s)
		client := newClient(&tls.Config{RootCAs: roots, MaxVersion: tls.VersionTLS12})
		_, err := get(ctx, client, "https://"+s.Addrs()[0].String()+"/")
		Expect(err).To(MatchError(ContainSubstring("protocol version")))
	})
})
