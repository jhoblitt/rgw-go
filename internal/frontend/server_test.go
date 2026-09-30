package frontend_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/frontend"
)

// poll is how often each Eventually in the suite checks again.
const poll = 10 * time.Millisecond

// echoMethod answers each request with its method.
var echoMethod = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, r.Method)
})

// start builds a Server for spec around h and binds it.
func start(spec frontend.Spec, h http.Handler) *frontend.Server {
	GinkgoHelper()
	s, err := frontend.New(spec, h)
	Expect(err).NotTo(HaveOccurred())
	Expect(s.Listen()).To(Succeed())
	return s
}

// serve runs s until the spec ends, then expects a clean drain.
func serve(ctx context.Context, s *frontend.Server) {
	ctx, cancel := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()
	DeferCleanup(func() {
		cancel()
		Eventually(served).WithTimeout(frontend.DrainTimeout).WithPolling(poll).Should(Receive(BeNil()))
	})
}

// newClient returns a client with a transport of its own, which offers h2 to
// a TLS server and closes its idle connections when the spec ends.
func newClient(tlsConfig *tls.Config) *http.Client {
	tr := &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true}
	DeferCleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// get sends a GET for url and closes the response's body when the spec ends.
func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	Expect(err).NotTo(HaveOccurred())
	res, err := client.Do(req)
	if err == nil {
		DeferCleanup(res.Body.Close)
	}
	return res, err
}

// dial connects to addr until the spec ends; every read and write on the
// connection fails after ten seconds, so no spec waits forever on a server.
func dial(ctx context.Context, addr string) *net.TCPConn {
	GinkgoHelper()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(c.Close()).To(Or(Succeed(), MatchError(net.ErrClosed))) })
	Expect(c.SetDeadline(time.Now().Add(10 * time.Second))).To(Succeed())
	tc, ok := c.(*net.TCPConn)
	Expect(ok).To(BeTrue(), "a tcp dial returns %T", c)
	return tc
}

// send writes s to c.
func send(c net.Conn, s string) {
	GinkgoHelper()
	_, err := io.WriteString(c, s)
	Expect(err).NotTo(HaveOccurred())
}

// listen binds address on network until the spec ends.
func listen(ctx context.Context, network, address string) net.Listener {
	GinkgoHelper()
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, network, address)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(l.Close()).To(Succeed()) })
	return l
}

// freePort returns a port no socket holds, in IPv4 and, where the host has
// it, IPv6.
func freePort(ctx context.Context) int {
	GinkgoHelper()
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp4", "0.0.0.0:0")
	Expect(err).NotTo(HaveOccurred())
	addr, ok := l.Addr().(*net.TCPAddr)
	Expect(ok).To(BeTrue(), "a tcp4 listener's address is %T", l.Addr())
	if l6, err := lc.Listen(ctx, "tcp6", net.JoinHostPort("::", strconv.Itoa(addr.Port))); err == nil {
		Expect(l6.Close()).To(Succeed())
	} else {
		Expect(err).To(MatchError(syscall.EAFNOSUPPORT), "port %d is taken in IPv6", addr.Port)
	}
	Expect(l.Close()).To(Succeed())
	return addr.Port
}

// requireIPv6 skips the spec on a host without IPv6 loopback.
func requireIPv6(ctx context.Context) {
	GinkgoHelper()
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp6", "[::1]:0")
	if err != nil {
		Skip("the host has no IPv6 loopback: " + err.Error())
	}
	Expect(l.Close()).To(Succeed())
}

// addrStrings renders each address.
func addrStrings(addrs []net.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// logBuffer holds what slog writes, from the server's goroutines as from the
// spec's own.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines decodes the JSON lines written so far.
func (b *logBuffer) lines() []map[string]any {
	GinkgoHelper()
	b.mu.Lock()
	data := slices.Clone(b.buf.Bytes())
	b.mu.Unlock()
	lines := []map[string]any{}
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var line map[string]any
		Expect(dec.Decode(&line)).To(Succeed())
		lines = append(lines, line)
	}
	return lines
}

// captureLogs sends slog's default logger to a buffer until the spec ends.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so the cleanup puts
// log's writer and flags back too.
func captureLogs() *logBuffer {
	buf := &logBuffer{}
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return buf
}

// hijackNoDelay answers a request by reading TCP_NODELAY off its connection
// and sending it on got.
func hijackNoDelay(got chan<- int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		defer GinkgoRecover()
		conn, _, err := http.NewResponseController(w).Hijack()
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(conn.Close()).To(Succeed()) }()
		tc, ok := conn.(*net.TCPConn)
		Expect(ok).To(BeTrue(), "the server's connection is %T", conn)
		raw, err := tc.SyscallConn()
		Expect(err).NotTo(HaveOccurred())
		var (
			v      int
			optErr error
		)
		Expect(raw.Control(func(fd uintptr) {
			v, optErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY)
		})).To(Succeed())
		Expect(optErr).NotTo(HaveOccurred())
		got <- v
	}
}

var _ = Describe("Server", func() {
	It("listens on each endpoint and serves its handler", func(ctx SpecContext) {
		s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}}, echoMethod)
		serve(ctx, s)
		Expect(s.Addrs()).To(HaveLen(1))
		Expect(get(ctx, newClient(nil), "http://"+s.Addrs()[0].String()+"/")).
			To(And(HaveHTTPStatus(http.StatusOK), HaveHTTPBody("GET")))
	})

	It("hands its handler the query as sent, as radosgw keeps a ';' inside a value", func(ctx SpecContext) {
		queries := make(chan string, 1)
		s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}}, frontend.Deadlines(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			queries <- r.URL.RawQuery
		}), time.Hour))
		serve(ctx, s)
		send(dial(ctx, s.Addrs()[0].String()), "GET /b?prefix=a;b&max-keys=1 HTTP/1.1\r\nHost: s3\r\n\r\n")
		Eventually(queries).WithTimeout(time.Second).WithPolling(poll).Should(Receive(Equal("prefix=a;b&max-keys=1")))
	})

	It("bounds a request header by its size alone, as beast does, past net/http's default of 500 values", func(ctx SpecContext) {
		s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}, MaxHeaderSize: frontend.DefaultMaxHeaderSize}, echoMethod)
		serve(ctx, s)
		c := dial(ctx, s.Addrs()[0].String())
		var req strings.Builder
		req.WriteString("GET /b HTTP/1.1\r\nHost: s3\r\n")
		for i := range 600 {
			fmt.Fprintf(&req, "X-Amz-Meta-N: %d\r\n", i)
		}
		req.WriteString("\r\n")
		send(c, req.String())
		Expect(readResponse(bufio.NewReader(c))).To(HaveHTTPStatus(http.StatusOK))
	})

	It("listens on a port in both address families, as beast opens a listener for each", func(ctx SpecContext) {
		requireIPv6(ctx)
		port := freePort(ctx)
		p := strconv.Itoa(port)
		s := start(frontend.Spec{Ports: []int{port}}, echoMethod)
		serve(ctx, s)
		Expect(addrStrings(s.Addrs())).To(Equal([]string{"0.0.0.0:" + p, "[::]:" + p}))
		client := newClient(nil)
		for _, host := range []string{"127.0.0.1", "::1"} {
			Expect(get(ctx, client, "http://"+net.JoinHostPort(host, p)+"/")).To(HaveHTTPStatus(http.StatusOK), host)
		}
	})

	It("opens each endpoint in its own address family only, as beast sets v6_only, so an IPv4 and an IPv6 endpoint share a port", func(ctx SpecContext) {
		requireIPv6(ctx)
		p := strconv.Itoa(freePort(ctx))
		s := start(frontend.Spec{Endpoints: []string{"0.0.0.0:" + p, "[::]:" + p}}, echoMethod)
		serve(ctx, s)
		Expect(addrStrings(s.Addrs())).To(Equal([]string{"0.0.0.0:" + p, "[::]:" + p}))
	})

	It("fails to bind an address in use, naming its key, and closes what it had bound", func(ctx SpecContext) {
		taken := listen(ctx, "tcp4", "127.0.0.1:0").Addr().String()
		free := net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort(ctx)))
		s, err := frontend.New(frontend.Spec{Endpoints: []string{free, taken}}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
		err = s.Listen()
		Expect(err).To(MatchError(syscall.EADDRINUSE))
		Expect(err).To(MatchError(ContainSubstring("endpoint=" + taken)))
		listen(ctx, "tcp4", free)
	})

	It("fails to bind a privileged port as an unprivileged user, naming it", func(ctx SpecContext) {
		var lc net.ListenConfig
		if l, err := lc.Listen(ctx, "tcp4", "0.0.0.0:1"); err == nil {
			Expect(l.Close()).To(Succeed())
			Skip("this process may bind port 1")
		}
		s, err := frontend.New(frontend.Spec{Ports: []int{1}}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
		err = s.Listen()
		Expect(err).To(MatchError(os.ErrPermission))
		Expect(err).To(MatchError(ContainSubstring("port=1")))
	})

	It("refuses a spec with no listener, as radosgw refuses to start", func() {
		s, err := frontend.New(frontend.Spec{}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Listen()).To(MatchError("unable to listen at any endpoints"))
	})

	It("refuses to serve before Listen has bound a listener", func(ctx SpecContext) {
		s, err := frontend.New(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Serve(ctx)).To(MatchError(ContainSubstring("Listen")))
	})

	It("stops every listener and returns the error when one fails", func(ctx SpecContext) {
		s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0", "127.0.0.1:0"}}, echoMethod)
		Expect(frontend.CloseListener(s, 0)).To(Succeed())
		served := make(chan error, 1)
		go func() { served <- s.Serve(ctx) }()
		var err error
		Eventually(served).WithTimeout(frontend.DrainTimeout).WithPolling(poll).Should(Receive(&err))
		Expect(err).To(MatchError(net.ErrClosed))
		Expect(err).To(MatchError(ContainSubstring("serving on " + s.Addrs()[0].String())))
		var d net.Dialer
		_, err = d.DialContext(ctx, "tcp", s.Addrs()[1].String())
		Expect(err).To(MatchError(syscall.ECONNREFUSED), "the drain the failure started closes the other listener")
	})

	DescribeTable("sets TCP_NODELAY from tcp_nodelay, keeping Go's default on without it",
		func(ctx SpecContext, noDelay *bool, want int) {
			got := make(chan int, 1)
			s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}, TCPNoDelay: noDelay}, hijackNoDelay(got))
			serve(ctx, s)
			send(dial(ctx, s.Addrs()[0].String()), "GET / HTTP/1.1\r\nHost: s3\r\n\r\n")
			Eventually(got).WithTimeout(time.Second).WithPolling(poll).Should(Receive(Equal(want)))
		},
		Entry("absent", nil, 1),
		Entry("tcp_nodelay=1", new(true), 1),
		Entry("any other value, which beast reads as off", new(false), 0),
	)

	Context("when its context ends", func() {
		// inFlight is a Server with one request blocked in its handler until
		// release closes, served under a context cancel ends. Its handler and
		// goroutines hold only its own values, since the spec that lets a
		// drain expire ends before the client goroutine does.
		type inFlight struct {
			s         *frontend.Server
			cancel    context.CancelFunc
			release   chan struct{}
			served    chan error
			responses chan *http.Response
			failures  chan error
		}

		begin := func(ctx SpecContext, drain time.Duration) *inFlight {
			entered, release := make(chan struct{}), make(chan struct{})
			s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
					fmt.Fprint(w, "drained")
				case <-r.Context().Done():
				}
			}))
			if drain > 0 {
				frontend.SetDrainTimeout(s, drain)
			}
			serveCtx, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			f := &inFlight{
				s: s, cancel: cancel, release: release,
				served: make(chan error, 1), responses: make(chan *http.Response, 1), failures: make(chan error, 1),
			}
			served, responses, failures := f.served, f.responses, f.failures
			go func() { served <- s.Serve(serveCtx) }()

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.Addrs()[0].String()+"/", http.NoBody)
			Expect(err).NotTo(HaveOccurred())
			client := newClient(nil)
			go func() {
				res, err := client.Do(req)
				if err != nil {
					failures <- err
					return
				}
				responses <- res
			}()
			Eventually(entered).WithTimeout(time.Second).WithPolling(poll).Should(BeClosed())
			return f
		}

		It("drains an in-flight request and returns nil", func(ctx SpecContext) {
			f := begin(ctx, 0)
			f.cancel()
			// Shutdown refuses new connections before it waits.
			var d net.Dialer
			Eventually(func(g Gomega) {
				c, err := d.DialContext(ctx, "tcp", f.s.Addrs()[0].String())
				if err == nil {
					g.Expect(c.Close()).To(Succeed())
				}
				g.Expect(err).To(HaveOccurred())
			}).WithTimeout(time.Second).WithPolling(poll).Should(Succeed())
			close(f.release)
			var res *http.Response
			Eventually(f.responses).WithTimeout(time.Second).WithPolling(poll).Should(Receive(&res))
			DeferCleanup(res.Body.Close)
			Expect(res).To(And(HaveHTTPStatus(http.StatusOK), HaveHTTPBody("drained")))
			Eventually(f.served).WithTimeout(frontend.DrainTimeout).WithPolling(poll).Should(Receive(BeNil()))
		})

		It("closes a request the drain outlasts and reports the unclean drain", func(ctx SpecContext) {
			f := begin(ctx, 100*time.Millisecond)
			f.cancel()
			Eventually(f.served).WithTimeout(time.Second).WithPolling(poll).Should(Receive(MatchError(context.DeadlineExceeded)))
			Eventually(f.failures).WithTimeout(time.Second).WithPolling(poll).Should(Receive())
		})

		It("ends the requests an expired drain outlasts and waits for their handlers, as radosgw joins every request", func(ctx SpecContext) {
			entered, returned := make(chan struct{}), make(chan struct{})
			s := start(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}}, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-r.Context().Done():
					// Work after the cancellation, which Serve must wait out.
					<-time.After(100 * time.Millisecond)
					close(returned)
				case <-time.After(5 * time.Second):
				}
			}))
			frontend.SetDrainTimeout(s, 100*time.Millisecond)
			serveCtx, cancel := context.WithCancel(ctx)
			DeferCleanup(cancel)
			served := make(chan error, 1)
			go func() { served <- s.Serve(serveCtx) }()
			// The handler leaves the body unread, so no background read runs
			// to end its context when the connection closes.
			send(dial(ctx, s.Addrs()[0].String()), "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: 100\r\n\r\n")
			Eventually(entered).WithTimeout(time.Second).WithPolling(poll).Should(BeClosed())
			cancel()
			var err error
			Eventually(served).WithTimeout(5 * time.Second).WithPolling(poll).Should(Receive(&err))
			Expect(returned).To(BeClosed(), "Serve returned before its handler did")
			Expect(err).To(MatchError(context.DeadlineExceeded))
		})
	})

	It("logs once each frontend key it does not apply", func() {
		logs := captureLogs()
		_, err := frontend.New(frontend.Spec{
			Endpoints:            []string{"127.0.0.1:0"},
			SSLCertificate:       "/nonexistent/rgw-cert.pem",
			MaxConnectionBacklog: 512,
			Unknown: map[string][]string{
				"prefix":           {"/s3", "/b"},
				"so_reuseport":     {"1"},
				"ssl_ciphersuites": {"TLS_AES_128_GCM_SHA256"},
			},
		}, echoMethod)
		Expect(err).NotTo(HaveOccurred())
		Expect(logs.lines()).To(ConsistOf(
			And(HaveKeyWithValue("msg", "ignoring unknown frontend key"), HaveKeyWithValue("key", "prefix"),
				HaveKeyWithValue("values", Equal([]any{"/s3", "/b"}))),
			And(HaveKeyWithValue("msg", "ignoring unknown frontend key"), HaveKeyWithValue("key", "so_reuseport"),
				HaveKeyWithValue("values", Equal([]any{"1"}))),
			And(HaveKeyWithValue("msg", "ssl_ciphersuites is not configurable in Go, its TLS 1.3 cipher suites apply"),
				HaveKeyWithValue("values", Equal([]any{"TLS_AES_128_GCM_SHA256"}))),
			And(HaveKeyWithValue("msg", "max_connection_backlog is not configurable in Go, the kernel backlog applies"),
				HaveKeyWithValue("max_connection_backlog", BeEquivalentTo(512))),
			And(HaveKeyWithValue("msg", "ignoring ssl keys without a TLS listener"),
				HaveKeyWithValue("keys", Equal([]any{"ssl_certificate"}))),
		))
	})

	It("logs net/http's own errors through slog under one message", func(ctx SpecContext) {
		logs := captureLogs()
		cert, _, _ := writeCertificate(GinkgoT().TempDir(), true)
		s := start(frontend.Spec{SSLEndpoints: []string{"127.0.0.1:0"}, SSLCertificate: cert}, echoMethod)
		serve(ctx, s)
		send(dial(ctx, s.Addrs()[0].String()), "GET / HTTP/1.1\r\nHost: s3\r\n\r\n")
		Eventually(logs.lines).WithTimeout(time.Second).WithPolling(poll).Should(ContainElement(And(
			HaveKeyWithValue("msg", "http server error"),
			HaveKeyWithValue("error", ContainSubstring("TLS handshake error")),
		)))
	})
})
