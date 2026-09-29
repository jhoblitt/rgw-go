package frontend_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/frontend"
)

// spec is frontend.Spec with the parser's defaults and fill applied.
func spec(fill func(*frontend.Spec)) frontend.Spec {
	s := frontend.Spec{RequestTimeout: frontend.DefaultRequestTimeout, MaxHeaderSize: frontend.DefaultMaxHeaderSize}
	fill(&s)
	return s
}

var _ = Describe("ParseBeast", func() {
	DescribeTable("parses Rook's exact frontend strings",
		func(entry string, want frontend.Spec) {
			Expect(frontend.ParseBeast(entry)).To(Equal(want))
		},
		Entry("plain", "beast port=80", spec(func(s *frontend.Spec) { s.Ports = []int{80} })),
		Entry("plain and TLS with a separate key",
			"beast port=8080 ssl_port=443 ssl_certificate=/etc/ceph/private/rgw-cert.pem ssl_private_key=/etc/ceph/private/rgw-key.pem",
			spec(func(s *frontend.Spec) {
				s.Ports = []int{8080}
				s.SSLPorts = []int{443}
				s.SSLCertificate = "/etc/ceph/private/rgw-cert.pem"
				s.SSLPrivateKey = "/etc/ceph/private/rgw-key.pem"
			})),
		Entry("TLS only with ssl_options",
			"beast ssl_port=443 ssl_certificate=/etc/ceph/private/rgw-cert.pem ssl_options=no_compression:no_tlsv1_2",
			spec(func(s *frontend.Spec) {
				s.SSLPorts = []int{443}
				s.SSLCertificate = "/etc/ceph/private/rgw-cert.pem"
				s.SSLOptions = []string{"no_compression", "no_tlsv1_2"}
			})),
	)
	DescribeTable("parses each key as AsioFrontend::init and ssl_init read it",
		func(entry string, want frontend.Spec) {
			Expect(frontend.ParseBeast(entry)).To(Equal(want))
		},
		Entry("a repeated port listens on each", "beast port=80 port=8080", spec(func(s *frontend.Spec) { s.Ports = []int{80, 8080} })),
		Entry("the highest port", "beast ssl_port=65535 ssl_certificate=c", spec(func(s *frontend.Spec) {
			s.SSLPorts = []int{65535}
			s.SSLCertificate = "c"
		})),
		Entry("an IPv4 endpoint with its port", "beast endpoint=10.0.0.1:8080", spec(func(s *frontend.Spec) { s.Endpoints = []string{"10.0.0.1:8080"} })),
		Entry("an IPv4 endpoint defaults to port 80", "beast endpoint=10.0.0.1", spec(func(s *frontend.Spec) { s.Endpoints = []string{"10.0.0.1:80"} })),
		Entry("an IPv6 endpoint with its port", "beast endpoint=[::1]:8080", spec(func(s *frontend.Spec) { s.Endpoints = []string{"[::1]:8080"} })),
		Entry("an IPv6 endpoint defaults to port 80", "beast endpoint=[fd00::7]", spec(func(s *frontend.Spec) { s.Endpoints = []string{"[fd00::7]:80"} })),
		Entry("an ssl_endpoint defaults to port 443", "beast ssl_endpoint=0.0.0.0 ssl_endpoint=[::] ssl_certificate=c", spec(func(s *frontend.Spec) {
			s.SSLEndpoints = []string{"0.0.0.0:443", "[::]:443"}
			s.SSLCertificate = "c"
		})),
		Entry("a request timeout in milliseconds", "beast port=80 request_timeout_ms=1500", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.RequestTimeout = 1500 * time.Millisecond
		})),
		Entry("a request timeout of zero, which beast takes as none", "beast port=80 request_timeout_ms=0", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.RequestTimeout = 0
		})),
		Entry("an invalid request timeout keeps the default and is reported, as radosgw warns and keeps it",
			"beast port=80 request_timeout_ms=soon", spec(func(s *frontend.Spec) {
				s.Ports = []int{80}
				s.Unknown = map[string][]string{"request_timeout_ms": {"soon"}}
			})),
		Entry("a header size within the parse buffer", "beast port=80 max_header_size=32768", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.MaxHeaderSize = 32768
		})),
		Entry("a header size above the parse buffer is capped", "beast port=80 max_header_size=1048576", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.MaxHeaderSize = frontend.MaxHeaderSizeCap
		})),
		Entry("an invalid header size keeps the default and is reported", "beast port=80 max_header_size=-1", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.Unknown = map[string][]string{"max_header_size": {"-1"}}
		})),
		Entry("a connection backlog", "beast port=80 max_connection_backlog=512", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.MaxConnectionBacklog = 512
		})),
		Entry("an invalid connection backlog is reported", "beast port=80 max_connection_backlog=lots", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.Unknown = map[string][]string{"max_connection_backlog": {"lots"}}
		})),
		Entry("tcp_nodelay=1 enables it", "beast port=80 tcp_nodelay=1", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.TCPNoDelay = new(true)
		})),
		Entry("any other tcp_nodelay disables it", "beast port=80 tcp_nodelay=true", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.TCPNoDelay = new(false)
		})),
		Entry("a bare key has an empty value", "beast port=80 tcp_nodelay", spec(func(s *frontend.Spec) {
			s.Ports = []int{80}
			s.TCPNoDelay = new(false)
		})),
		Entry("the first of a repeated single-valued key wins, as std::multimap::find returns it",
			"beast port=80 tcp_nodelay=1 tcp_nodelay=0", spec(func(s *frontend.Spec) {
				s.Ports = []int{80}
				s.TCPNoDelay = new(true)
			})),
		Entry("ssl_options and ssl_ciphers split on colons, dropping empty items",
			"beast ssl_port=443 ssl_certificate=c ssl_options=no_sslv3::no_tlsv1: ssl_ciphers=ECDHE-RSA-AES128-GCM-SHA256:AES256-SHA",
			spec(func(s *frontend.Spec) {
				s.SSLPorts = []int{443}
				s.SSLCertificate = "c"
				s.SSLOptions = []string{"no_sslv3", "no_tlsv1"}
				s.SSLCiphers = []string{"ECDHE-RSA-AES128-GCM-SHA256", "AES256-SHA"}
			})),
		Entry("an empty ssl_options is no options, not radosgw's default", "beast ssl_port=443 ssl_certificate=c ssl_options=",
			spec(func(s *frontend.Spec) {
				s.SSLPorts = []int{443}
				s.SSLCertificate = "c"
				s.SSLOptions = []string{}
			})),
		Entry("whitespace around a value is trimmed, as parse_key_value does", "beast port=80\t", spec(func(s *frontend.Spec) { s.Ports = []int{80} })),
		Entry("keys rgw-go does not implement are reported with every value",
			"beast port=80 prefix=/s3 so_reuseport=1 ssl_ciphersuites=TLS_AES_128_GCM_SHA256 tls_groups=X25519 ssl_reload=60 prefix=/b bogus",
			spec(func(s *frontend.Spec) {
				s.Ports = []int{80}
				s.Unknown = map[string][]string{
					"prefix":           {"/s3", "/b"},
					"so_reuseport":     {"1"},
					"ssl_ciphersuites": {"TLS_AES_128_GCM_SHA256"},
					"tls_groups":       {"X25519"},
					"ssl_reload":       {"60"},
					"bogus":            {""},
				}
			})),
		Entry("a bare beast has the defaults and no listener", "beast", spec(func(*frontend.Spec) {})),
	)
	DescribeTable("refuses what radosgw refuses, naming the key",
		func(entry, key string) {
			_, err := frontend.ParseBeast(entry)
			Expect(err).To(MatchError(ContainSubstring(key)), "%q", entry)
		},
		Entry("a non-numeric port", "beast port=http", "port=http"),
		Entry("port zero", "beast port=0", "port=0"),
		Entry("a port past 65535", "beast port=65536", "port=65536"),
		Entry("a negative ssl_port", "beast ssl_port=-443 ssl_certificate=c", "ssl_port=-443"),
		Entry("an empty port", "beast port", "port="),
		Entry("an endpoint with a bad port", "beast endpoint=10.0.0.1:http", "endpoint=10.0.0.1:http"),
		Entry("an endpoint naming a host, which make_address_v4 refuses", "beast endpoint=localhost:8080", "endpoint=localhost:8080"),
		Entry("an IPv6 endpoint without brackets", "beast endpoint=::1", "endpoint=::1"),
		Entry("an IPv4 address in brackets", "beast endpoint=[10.0.0.1]:80", "endpoint=[10.0.0.1]:80"),
		Entry("an unclosed bracket", "beast endpoint=[::1:80", "endpoint=[::1:80"),
		Entry("text after the bracket that is not :port", "beast endpoint=[::1]80", "endpoint=[::1]80"),
		Entry("an empty ssl_endpoint", "beast ssl_endpoint= ssl_certificate=c", "ssl_endpoint="),
		Entry("a private key without a certificate on a TLS listener", "beast ssl_port=443 ssl_private_key=/k", "ssl_certificate"),
		Entry("a private key without a certificate and no TLS listener, as Tentacle's init_ssl refuses it", "beast port=80 ssl_private_key=/k", "ssl_certificate"),
		Entry("an empty private key without a certificate, which radosgw tests for presence", "beast port=80 ssl_private_key=", "ssl_certificate"),
	)
	It("refuses a framework other than beast", func() {
		_, err := frontend.ParseBeast("civetweb port=80")
		Expect(err).To(MatchError(frontend.ErrNotBeast))
	})
})

var _ = Describe("ParseFrontends", func() {
	It("returns the beast entry and names the others", func() {
		got, others, err := frontend.ParseFrontends("beast port=8080, civetweb port=80,rgw-nfs")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(spec(func(s *frontend.Spec) { s.Ports = []int{8080} })))
		Expect(others).To(Equal([]string{"civetweb", "rgw-nfs"}))
	})
	It("skips empty entries, as get_str_vec does", func() {
		got, others, err := frontend.ParseFrontends(",beast port=80,,")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Ports).To(Equal([]int{80}))
		Expect(others).To(BeEmpty())
	})
	It("takes an empty value as a bare beast, as init_frontends1 does, which then has no listener", func() {
		got, others, err := frontend.ParseFrontends("")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(spec(func(*frontend.Spec) {})))
		Expect(others).To(BeEmpty())
	})
	It("refuses a value with no beast entry, still naming the others", func() {
		_, others, err := frontend.ParseFrontends("civetweb port=80")
		Expect(err).To(MatchError(frontend.ErrNotBeast))
		Expect(others).To(Equal([]string{"civetweb"}))
	})
	It("refuses a second beast entry, which one Spec cannot hold", func() {
		_, _, err := frontend.ParseFrontends("beast port=80,beast port=81")
		Expect(err).To(MatchError(ContainSubstring("more than one beast frontend")))
	})
	It("passes a beast entry's parse error through", func() {
		_, _, err := frontend.ParseFrontends("beast port=x")
		Expect(err).To(MatchError(ContainSubstring("port=x")))
	})
})
