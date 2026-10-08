package frontend

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// DrainTimeout bounds Serve's graceful shutdown. Rook sets no termination
// grace period on a radosgw pod, so Kubernetes' default of 30 s applies, and
// the driver's workers stop only after the drain, the usage log's last
// flush taking up to 10 s more: the two together fit inside the grace.
const DrainTimeout = 20 * time.Second

// Server is every listener one beast spec asks for, serving one handler.
type Server struct {
	spec      Spec
	handler   http.Handler
	tlsConfig *tls.Config
	listeners []net.Listener
	drain     time.Duration
}

// New builds the server for spec around h. It loads the certificate when
// spec has TLS listeners and fails on an unusable one, as ssl_init does.
// Without a TLS listener the ssl_* keys do nothing, as they do in radosgw,
// which never refuses them there: rgw_frontend_defaults gives every beast
// entry a certificate (rgw_appmain.cc:462-465 at v19.2.6, :470-472 at
// v20.2.4). It logs once each key it does not apply.
func New(spec Spec, h http.Handler) (*Server, error) {
	s := &Server{spec: spec, handler: h, drain: DrainTimeout}
	if hasTLSListener(spec) {
		cfg, err := TLSConfig(spec)
		if err != nil {
			return nil, err
		}
		s.tlsConfig = cfg
	}
	logIgnored(spec)
	return s, nil
}

func hasTLSListener(spec Spec) bool {
	return len(spec.SSLPorts) > 0 || len(spec.SSLEndpoints) > 0
}

// logIgnored logs each key of spec the frontend does not apply.
func logIgnored(spec Spec) {
	for _, key := range slices.Sorted(maps.Keys(spec.Unknown)) {
		if key == "ssl_ciphersuites" {
			continue
		}
		slog.Warn("ignoring unknown frontend key", slog.String("key", key), slog.Any("values", spec.Unknown[key]))
	}
	if v, ok := spec.Unknown["ssl_ciphersuites"]; ok {
		slog.Warn("ssl_ciphersuites is not configurable in Go, its TLS 1.3 cipher suites apply", slog.Any("values", v))
	}
	if spec.MaxConnectionBacklog != 0 {
		slog.Warn("max_connection_backlog is not configurable in Go, the kernel backlog applies",
			slog.Int("max_connection_backlog", spec.MaxConnectionBacklog))
	}
	if hasTLSListener(spec) {
		return
	}
	var unused []string
	for _, k := range []struct {
		key string
		set bool
	}{
		{"ssl_certificate", spec.SSLCertificate != ""},
		{"ssl_private_key", spec.SSLPrivateKey != ""},
		{"ssl_options", spec.SSLOptions != nil},
		{"ssl_ciphers", spec.SSLCiphers != nil},
	} {
		if k.set {
			unused = append(unused, k.key)
		}
	}
	if len(unused) > 0 {
		slog.Warn("ignoring ssl keys without a TLS listener", slog.Any("keys", unused))
	}
}

// listenAddr is one socket to bind: the spec key that asked for it, and its
// network and address for net.Listen.
type listenAddr struct {
	key, network, address string
	tls                   bool
}

// addrs lists the sockets spec asks for in the order AsioFrontend::init
// binds them, TLS first since ssl_init adds those listeners before the
// others (rgw_asio_frontend.cc:612-642 at v20.2.4). A port gets one socket
// per address family and an endpoint one in its own; beast opens an IPv6
// socket IPv6-only (v6_only, :671-678), where Go's "tcp" network listens on
// both families for any wildcard address, 0.0.0.0 included.
func (s *Server) addrs() []listenAddr {
	var addrs []listenAddr
	ports := func(key string, ports []int, secure bool) {
		for _, p := range ports {
			port := strconv.Itoa(p)
			addrs = append(addrs,
				listenAddr{key + "=" + port, "tcp4", net.JoinHostPort("0.0.0.0", port), secure},
				listenAddr{key + "=" + port, "tcp6", net.JoinHostPort("::", port), secure})
		}
	}
	endpoints := func(key string, endpoints []string, secure bool) {
		for _, ep := range endpoints {
			network := "tcp4"
			if ap, err := netip.ParseAddrPort(ep); err == nil && ap.Addr().Is6() {
				network = "tcp6"
			}
			addrs = append(addrs, listenAddr{key + "=" + ep, network, ep, secure})
		}
	}
	ports("ssl_port", s.spec.SSLPorts, true)
	endpoints("ssl_endpoint", s.spec.SSLEndpoints, true)
	ports("port", s.spec.Ports, false)
	endpoints("endpoint", s.spec.Endpoints, false)
	return addrs
}

// Listen binds every port and endpoint. It runs before privileges drop, as
// AsioFrontend::init binds and then drop_privileges runs. A port listens on
// both address families; a family the host lacks is skipped with a warning,
// and no listener at all is an error, as radosgw refuses to start
// (rgw_asio_frontend.cc:656-729 at v20.2.4). A failed Listen closes what it
// bound.
func (s *Server) Listen() error {
	var listeners []net.Listener
	for _, a := range s.addrs() {
		l, err := net.Listen(a.network, a.address) //nolint:noctx // an IP literal needs no resolution for a context to cancel, and Listen takes none
		if errors.Is(err, syscall.EAFNOSUPPORT) {
			slog.Warn("cannot open socket for endpoint", slog.String("endpoint", a.address), slog.Any("error", err))
			continue
		}
		if err != nil {
			return errors.Join(fmt.Errorf("listening on %s: %w", a.key, err), closeAll(listeners))
		}
		if nd := s.spec.TCPNoDelay; nd != nil && !*nd {
			l = noDelayOff{l}
		}
		if a.tls {
			l = tls.NewListener(l, s.tlsConfig)
		}
		listeners = append(listeners, l)
	}
	if len(listeners) == 0 {
		return errors.New("unable to listen at any endpoints")
	}
	s.listeners = listeners
	return nil
}

func closeAll(listeners []net.Listener) error {
	var errs []error
	for _, l := range listeners {
		errs = append(errs, l.Close())
	}
	return errors.Join(errs...)
}

// noDelayOff turns TCP_NODELAY off on each connection it accepts, where Go
// turns it on; beast sets it from tcp_nodelay (rgw_asio_frontend.cc:1165 at
// v19.2.6, :1083 at v20.2.4).
type noDelayOff struct{ net.Listener }

func (l noDelayOff) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(false) //nolint:errcheck // it fails only on a dead socket, whose first read reports it, and Go's own accept ignores it too
	}
	return c, err
}

// Addrs returns the bound addresses, for tests and the startup log.
func (s *Server) Addrs() []net.Addr {
	addrs := make([]net.Addr, 0, len(s.listeners))
	for _, l := range s.listeners {
		addrs = append(addrs, l.Addr())
	}
	return addrs
}

// Serve accepts on every listener until ctx ends, then drains: Shutdown with
// DrainTimeout, then Close. It returns nil on a clean drain. Requests do not
// see ctx end, but a drain that expires closes their connections and ends
// their contexts, and Serve returns only once every handler has: radosgw
// joins every request before it closes its store (rgw_appmain.cc:588-604 at
// v19.2.6, :624-644 at v20.2.4).
func (s *Server) Serve(ctx context.Context) error {
	if len(s.listeners) == 0 {
		return errors.New("serving with no listener: Listen has not bound one")
	}
	base, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	// A connection's goroutine runs its handlers, so its end is theirs; a
	// hijacked connection's handler is the hijacker's to finish.
	var conns sync.WaitGroup
	// beast speaks HTTP/1.x alone.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: s.spec.RequestTimeout,
		IdleTimeout:       s.spec.RequestTimeout,
		MaxHeaderBytes:    s.spec.MaxHeaderSize,
		// beast bounds a request header by its size alone.
		MaxHeaderValueCount: math.MaxInt,
		Protocols:           &protocols,
		BaseContext:         func(net.Listener) context.Context { return base },
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				conns.Add(1)
			case http.StateHijacked, http.StateClosed:
				conns.Done()
			}
		},
		ErrorLog: slog.NewLogLogger(serverErrors{}, slog.LevelWarn),
	}
	g, gctx := errgroup.WithContext(ctx)
	for _, l := range s.listeners {
		g.Go(func() error {
			if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serving on %s: %w", l.Addr(), err)
			}
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.drain)
		defer cancel()
		var err error
		if shutdownErr := srv.Shutdown(drainCtx); shutdownErr != nil {
			err = fmt.Errorf("draining: %w", shutdownErr)
		}
		err = errors.Join(err, srv.Close())
		stop()
		return err
	})
	err := g.Wait()
	// Each connection was counted in an accept loop, and every accept loop
	// has returned.
	conns.Wait()
	return err
}

// serverErrors files each line net/http logs, TLS handshake failures and
// recovered handler panics among them, under one static message.
type serverErrors struct{}

func (serverErrors) Enabled(context.Context, slog.Level) bool { return true }

func (serverErrors) Handle(ctx context.Context, r slog.Record) error {
	slog.WarnContext(ctx, "http server error", slog.String("error", r.Message))
	return nil
}

func (h serverErrors) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h serverErrors) WithGroup(string) slog.Handler { return h }
