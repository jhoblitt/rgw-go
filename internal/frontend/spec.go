package frontend

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// DefaultRequestTimeout is beast's REQUEST_TIMEOUT (rgw_asio_frontend.h:11).
const DefaultRequestTimeout = 65 * time.Second

// DefaultMaxHeaderSize and MaxHeaderSizeCap are beast's header_limit default
// and its parse-buffer ceiling (rgw_asio_frontend.cc:420, rgw_asio_frontend_connection.h:17).
const (
	DefaultMaxHeaderSize = 16384
	MaxHeaderSizeCap     = 65536
)

// The default ports of an endpoint and an ssl_endpoint without one
// (AsioFrontend::init and ssl_init).
const (
	defaultPort    = 80
	defaultSSLPort = 443
)

// ErrNotBeast is returned for a frontend entry whose framework is not beast.
var ErrNotBeast = errors.New("frontend: not a beast frontend")

// Spec is one beast frontend's configuration.
type Spec struct {
	// Ports and SSLPorts listen on every address; Endpoints and SSLEndpoints
	// are "host:port" strings, the port defaulting to 80 and 443.
	Ports, SSLPorts         []int
	Endpoints, SSLEndpoints []string
	// SSLCertificate is a PEM file path holding the chain and, when
	// SSLPrivateKey is empty, the key too.
	SSLCertificate, SSLPrivateKey string
	// SSLOptions are the colon-separated ssl_options items; nil means radosgw's
	// default "no_sslv2:no_sslv3:no_tlsv1:no_tlsv1_1" when a certificate is set.
	SSLOptions []string
	// SSLCiphers is the colon-separated OpenSSL cipher list for TLS 1.2 and below.
	SSLCiphers []string
	// TCPNoDelay is nil when the key is absent; radosgw enables it only for "1".
	TCPNoDelay     *bool
	RequestTimeout time.Duration
	// MaxConnectionBacklog is parsed and reported; Go's listener takes the
	// kernel's backlog, so frontend.New logs it as ignored.
	MaxConnectionBacklog int
	MaxHeaderSize        int
	// Unknown holds, for the caller to log, every key the parser did not
	// recognize and every recognized key whose value it could not use and
	// replaced with the default, as radosgw warns and keeps it.
	Unknown map[string][]string
}

// ParseBeast parses one frontend entry ("beast port=80 ssl_port=443 ...") as
// RGWFrontendConfig::parse_config splits it (rgw_frontend.cc:17-48 at
// v19.2.6): space-separated tokens, the first the framework name, each other
// "key=value" split at its first '=' with both sides trimmed, or a bare key
// with an empty value; a key may repeat. The keys are AsioFrontend::init's
// and ssl_init's (rgw_asio_frontend.cc:592-740, 916-1045). A framework other
// than beast is ErrNotBeast, and an unparsable port or endpoint is an error
// naming the key, as radosgw refuses them; an unusable request_timeout_ms,
// max_header_size or max_connection_backlog keeps its default and is reported
// in Unknown. Where a key radosgw reads once repeats, the first value wins, as
// std::multimap::find returns it.
func ParseBeast(entry string) (Spec, error) {
	framework, kvs := parseConfig(entry)
	if framework != "beast" {
		return Spec{}, fmt.Errorf("%w: %q", ErrNotBeast, framework)
	}
	s := Spec{RequestTimeout: DefaultRequestTimeout, MaxHeaderSize: DefaultMaxHeaderSize}
	seen := map[string]bool{}
	for _, kv := range kvs {
		first := !seen[kv.key]
		seen[kv.key] = true
		if err := s.set(kv, first); err != nil {
			return Spec{}, fmt.Errorf("parsing %s=%s: %w", kv.key, kv.val, err)
		}
	}
	return s, nil
}

// set applies one key; first is false for a repeat.
func (s *Spec) set(kv keyValue, first bool) error {
	switch kv.key {
	case "port", "ssl_port":
		p, err := parsePort(kv.val)
		if err != nil {
			return err
		}
		if kv.key == "port" {
			s.Ports = append(s.Ports, p)
		} else {
			s.SSLPorts = append(s.SSLPorts, p)
		}
	case "endpoint":
		ep, err := parseEndpoint(kv.val, defaultPort)
		if err != nil {
			return err
		}
		s.Endpoints = append(s.Endpoints, ep)
	case "ssl_endpoint":
		ep, err := parseEndpoint(kv.val, defaultSSLPort)
		if err != nil {
			return err
		}
		s.SSLEndpoints = append(s.SSLEndpoints, ep)
	case "ssl_certificate", "ssl_private_key", "ssl_options", "ssl_ciphers",
		"tcp_nodelay", "request_timeout_ms", "max_header_size", "max_connection_backlog":
		if first {
			s.setOnce(kv)
		}
	default:
		s.unknown(kv)
	}
	return nil
}

// setOnce applies a key radosgw reads once.
func (s *Spec) setOnce(kv keyValue) {
	switch kv.key {
	case "ssl_certificate":
		s.SSLCertificate = kv.val
	case "ssl_private_key":
		s.SSLPrivateKey = kv.val
	case "ssl_options":
		s.SSLOptions = splitColons(kv.val)
	case "ssl_ciphers":
		s.SSLCiphers = splitColons(kv.val)
	case "tcp_nodelay":
		s.TCPNoDelay = new(kv.val == "1")
	case "request_timeout_ms":
		// ceph::parse<uint64_t>: digits only, the whole value.
		ms, err := strconv.ParseUint(kv.val, 10, 64)
		if err != nil || ms > math.MaxInt64/uint64(time.Millisecond) {
			s.unknown(kv)
			return
		}
		s.RequestTimeout = time.Duration(ms) * time.Millisecond
	case "max_header_size":
		limit, err := strconv.ParseUint(kv.val, 10, 64)
		if err != nil {
			s.unknown(kv)
			return
		}
		s.MaxHeaderSize = MaxHeaderSizeCap
		if limit < MaxHeaderSizeCap {
			s.MaxHeaderSize = int(limit)
		}
	case "max_connection_backlog":
		// strict_strtol: a whole base-10 value within int.
		n, err := strconv.ParseInt(kv.val, 10, 32)
		if err != nil {
			s.unknown(kv)
			return
		}
		s.MaxConnectionBacklog = int(n)
	}
}

func (s *Spec) unknown(kv keyValue) {
	if s.Unknown == nil {
		s.Unknown = map[string][]string{}
	}
	s.Unknown[kv.key] = append(s.Unknown[kv.key], kv.val)
}

// ParseFrontends parses an rgw_frontends value, split on commas into entries
// as rgw::AppMain::init_frontends1 splits it (rgw_appmain.cc:195 at main): the
// beast entry is returned and every other entry's framework name is listed in
// others for the caller to log. An empty value is a bare beast, as
// init_frontends1 pushes when the list is empty, with no listener. A second
// beast entry is an error, since one Spec cannot hold two frontends.
func ParseFrontends(value string) (spec Spec, others []string, err error) {
	entries := strings.FieldsFunc(value, func(r rune) bool { return r == ',' })
	if len(entries) == 0 {
		entries = []string{"beast"}
	}
	beast := false
	for _, entry := range entries {
		framework, _ := parseConfig(entry)
		if framework == "" {
			continue
		}
		if framework != "beast" {
			others = append(others, framework)
			continue
		}
		if beast {
			return Spec{}, others, errors.New("frontend: more than one beast frontend")
		}
		beast = true
		if spec, err = ParseBeast(entry); err != nil {
			return Spec{}, others, err
		}
	}
	if !beast {
		return Spec{}, others, fmt.Errorf("rgw_frontends %q: %w", value, ErrNotBeast)
	}
	return spec, others, nil
}

type keyValue struct{ key, val string }

// asciiSpace is what rgw_trim_whitespace trims: C's isspace.
const asciiSpace = " \t\n\v\f\r"

// parseConfig is RGWFrontendConfig::parse_config.
func parseConfig(entry string) (framework string, kvs []keyValue) {
	for tok := range strings.SplitSeq(entry, " ") {
		switch {
		case tok == "":
		case framework == "":
			framework = tok
		default:
			key, val, ok := strings.Cut(tok, "=")
			if !ok {
				kvs = append(kvs, keyValue{key: tok})
				continue
			}
			kvs = append(kvs, keyValue{strings.Trim(key, asciiSpace), strings.Trim(val, asciiSpace)})
		}
	}
	return framework, kvs
}

// parsePort reads a port, 1 to 65535.
func parsePort(s string) (int, error) {
	p, err := strconv.ParseUint(s, 10, 16)
	if err != nil || p == 0 {
		return 0, fmt.Errorf("port %q is not between 1 and 65535", s)
	}
	return int(p), nil
}

// parseEndpoint is parse_endpoint: "[ipv6]" or "ipv4", each with an optional
// ":port", rendered as "host:port" with the default port filled in. A host
// name is refused, as make_address_v4 refuses it.
func parseEndpoint(s string, port int) (string, error) {
	var (
		text, family string
		want         func(netip.Addr) bool
	)
	if rest, ok := strings.CutPrefix(s, "["); ok {
		addr, after, ok := strings.Cut(rest, "]")
		if !ok {
			return "", errors.New("no closing bracket")
		}
		if after != "" {
			p, ok := strings.CutPrefix(after, ":")
			if !ok {
				return "", errors.New("the address is not followed by :port")
			}
			n, err := parsePort(p)
			if err != nil {
				return "", err
			}
			port = n
		}
		text, family, want = addr, "IPv6", netip.Addr.Is6
	} else {
		addr, p, ok := strings.Cut(s, ":")
		if ok {
			n, err := parsePort(p)
			if err != nil {
				return "", err
			}
			port = n
		}
		text, family, want = addr, "IPv4", netip.Addr.Is4
	}
	a, err := netip.ParseAddr(text)
	if err != nil || !want(a) {
		return "", fmt.Errorf("%q is not an %s address", text, family)
	}
	return net.JoinHostPort(a.String(), strconv.Itoa(port)), nil
}

// splitColons is ceph::split(value, ":"), which drops empty items.
func splitColons(value string) []string {
	items := []string{}
	for item := range strings.SplitSeq(value, ":") {
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}
