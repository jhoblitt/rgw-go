package frontend

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// openSSLSuites maps the OpenSSL name of each TLS 1.2 AEAD suite Go ships,
// the ECDHE suites with AES-GCM or ChaCha20-Poly1305, to the suite; each is
// in tls.CipherSuites(), and its IANA name is tls.CipherSuiteName's.
var openSSLSuites = map[string]uint16{
	"ECDHE-ECDSA-AES128-GCM-SHA256": tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	"ECDHE-RSA-AES128-GCM-SHA256":   tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	"ECDHE-ECDSA-AES256-GCM-SHA384": tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	"ECDHE-RSA-AES256-GCM-SHA384":   tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	"ECDHE-ECDSA-CHACHA20-POLY1305": tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	"ECDHE-RSA-CHACHA20-POLY1305":   tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// configKeyPrefix marks a certificate radosgw reads from the monitors'
// config-key store instead of a file.
const configKeyPrefix = "config://"

// TLSConfig builds crypto/tls's configuration from the spec's ssl_* keys, as
// AsioFrontend::init_ssl builds its ssl context (rgw_asio_frontend.cc:889-1048
// at v20.2.4, ssl_reload :981-1094 at v19.2.6), and loads the certificate.
// It needs ssl_certificate to name a file: radosgw, given none, reads the
// config:// certificate rgw_frontend_defaults names, which rgw-go does not.
func TLSConfig(spec Spec) (*tls.Config, error) {
	if spec.SSLCertificate == "" {
		return nil, errNoCertificate(spec)
	}
	// radosgw's default ssl_options, no_sslv2:no_sslv3:no_tlsv1:no_tlsv1_1,
	// make TLS 1.2 its floor. rgw-go keeps that floor whatever ssl_options
	// says, though crypto/tls implements TLS 1.0 and 1.1.
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	for _, option := range spec.SSLOptions {
		switch option {
		case "no_tlsv1_2":
			cfg.MinVersion = tls.VersionTLS13
		case "no_sslv2", "no_sslv3", "no_tlsv1", "no_tlsv1_1", "no_compression", "single_dh_use":
			// crypto/tls has no SSL, the floor leaves out TLS 1.0 and 1.1,
			// and it never compresses or reuses an ECDHE key.
		case "default_workarounds":
			// OpenSSL's workarounds for broken peers have no crypto/tls
			// counterpart.
		default:
			slog.Warn("ignoring unknown ssl option", slog.String("option", option))
		}
	}
	if spec.SSLCiphers != nil {
		for _, name := range spec.SSLCiphers {
			if id, ok := cipherSuite(name); ok {
				cfg.CipherSuites = append(cfg.CipherSuites, id)
			} else {
				slog.Warn("skipping unknown ssl cipher", slog.String("cipher", name))
			}
		}
		if len(cfg.CipherSuites) == 0 {
			return nil, fmt.Errorf("no cipher could be selected from ssl_ciphers=%s", strings.Join(spec.SSLCiphers, ":"))
		}
	}
	cert, err := loadCertificate(spec.SSLCertificate, spec.SSLPrivateKey)
	if err != nil {
		return nil, err
	}
	cfg.Certificates = []tls.Certificate{cert}
	return cfg, nil
}

// errNoCertificate names the TLS listener that has no certificate, as
// init_ssl does (:1014-1037 at v20.2.4).
func errNoCertificate(spec Spec) error {
	var key string
	switch {
	case len(spec.SSLPorts) > 0:
		key = "ssl_port"
	case len(spec.SSLEndpoints) > 0:
		key = "ssl_endpoint"
	default:
		return errors.New("no ssl_certificate configured")
	}
	return fmt.Errorf("no ssl_certificate configured for %s: rgw-go does not read the %s certificate rgw_frontend_defaults names", key, configKeyPrefix)
}

// cipherSuite looks name up as an OpenSSL name or an IANA name.
func cipherSuite(name string) (uint16, bool) {
	if id, ok := openSSLSuites[name]; ok {
		return id, true
	}
	for _, id := range openSSLSuites {
		if tls.CipherSuiteName(id) == name {
			return id, true
		}
	}
	return 0, false
}

// loadCertificate loads the chain from certFile and the key from keyFile,
// or from certFile when keyFile is empty or holds no usable key, as radosgw
// retries the certificate's file for the key (:985-1011 at v20.2.4).
func loadCertificate(certFile, keyFile string) (tls.Certificate, error) {
	for _, kv := range []struct{ key, val string }{{"ssl_certificate", certFile}, {"ssl_private_key", keyFile}} {
		if strings.HasPrefix(kv.val, configKeyPrefix) {
			return tls.Certificate{}, fmt.Errorf("%s=%s: %s sources are not supported", kv.key, kv.val, configKeyPrefix)
		}
	}
	if keyFile == "" {
		cert, err := tls.LoadX509KeyPair(certFile, certFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("loading ssl_certificate=%s: %w", certFile, err)
		}
		return cert, nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err == nil {
		return cert, nil
	}
	if cert, certErr := tls.LoadX509KeyPair(certFile, certFile); certErr == nil {
		return cert, nil
	}
	return tls.Certificate{}, fmt.Errorf("loading ssl_certificate=%s ssl_private_key=%s: %w", certFile, keyFile, err)
}
