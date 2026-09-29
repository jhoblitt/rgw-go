package frontend_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/frontend"
)

// opTimeout is the per-operation timeout most connection specs run under.
const opTimeout = 200 * time.Millisecond

// deadlineServer serves Deadlines(h, timeout) until the spec ends and returns
// its address.
func deadlineServer(h http.HandlerFunc, timeout time.Duration) string {
	srv := httptest.NewUnstartedServer(frontend.Deadlines(h, timeout))
	srv.Start()
	DeferCleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// awaitDone closes done once ctx ends, giving up after a few seconds so that
// a handler whose context never ends still returns.
func awaitDone(ctx context.Context, done chan<- struct{}) {
	select {
	case <-ctx.Done():
		close(done)
	case <-time.After(5 * time.Second):
	}
}

// readResponse reads a response from r and closes its body when the spec
// ends.
func readResponse(r *bufio.Reader) *http.Response {
	GinkgoHelper()
	res, err := http.ReadResponse(r, nil)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(res.Body.Close)
	return res
}

// closedWithin expects the server to close the connection r reads within d,
// sending nothing more.
func closedWithin(c *net.TCPConn, r *bufio.Reader, d time.Duration) {
	GinkgoHelper()
	Expect(c.SetReadDeadline(time.Now().Add(d))).To(Succeed())
	n, err := r.Read(make([]byte, 1))
	Expect(n).To(BeZero())
	Expect(err).To(MatchError(io.EOF))
}

var _ = Describe("Deadlines on a connection", func() {
	It("fails a body read the client stalls, and ends the request's context", func(ctx SpecContext) {
		readErr, canceled := make(chan error, 1), make(chan struct{})
		addr := deadlineServer(func(_ http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			readErr <- err
			awaitDone(r.Context(), canceled)
		}, opTimeout)
		send(dial(ctx, addr), "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: 100\r\n\r\npartial")
		Eventually(readErr).WithTimeout(time.Second).WithPolling(poll).Should(Receive(MatchError(os.ErrDeadlineExceeded)))
		Eventually(canceled).WithTimeout(time.Second).WithPolling(poll).Should(BeClosed())
	})

	It("fails a response write the client stops reading, and ends the request's context", func(ctx SpecContext) {
		writeErr, canceled := make(chan error, 1), make(chan struct{})
		addr := deadlineServer(func(w http.ResponseWriter, r *http.Request) {
			chunk := make([]byte, 64<<10)
			var err error
			// A gibibyte at most, far past any socket buffer.
			for range 1 << 14 {
				if _, err = w.Write(chunk); err != nil {
					break
				}
			}
			writeErr <- err
			awaitDone(r.Context(), canceled)
		}, opTimeout)
		c := dial(ctx, addr)
		Expect(c.SetReadBuffer(1024)).To(Succeed())
		send(c, "GET /b/o HTTP/1.1\r\nHost: s3\r\n\r\n")
		Eventually(writeErr).WithTimeout(5 * time.Second).WithPolling(poll).Should(Receive(MatchError(os.ErrDeadlineExceeded)))
		Eventually(canceled).WithTimeout(time.Second).WithPolling(poll).Should(BeClosed())
	})

	It("lets a request outlast the timeout when each read and write beats it", func(ctx SpecContext) {
		const pieces = 5
		addr := deadlineServer(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			rc := http.NewResponseController(w)
			tick := time.NewTicker(opTimeout / 2)
			defer tick.Stop()
			for _, b := range body {
				<-tick.C
				fmt.Fprintf(w, "%c", b)
				if rc.Flush() != nil {
					return
				}
			}
		}, opTimeout)
		c := dial(ctx, addr)
		began := time.Now()
		send(c, "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: "+strconv.Itoa(pieces)+"\r\n\r\n")
		tick := time.NewTicker(opTimeout / 2)
		defer tick.Stop()
		for i := range pieces {
			<-tick.C
			send(c, strconv.Itoa(i))
		}
		Expect(readResponse(bufio.NewReader(c))).To(And(HaveHTTPStatus(http.StatusOK), HaveHTTPBody("01234")))
		Expect(time.Since(began)).To(BeNumerically(">", 4*opTimeout))
	})

	It("ends the request's context when the client disconnects", func(ctx SpecContext) {
		entered, canceled := make(chan struct{}), make(chan struct{})
		addr := deadlineServer(func(_ http.ResponseWriter, r *http.Request) {
			close(entered)
			awaitDone(r.Context(), canceled)
		}, opTimeout)
		c := dial(ctx, addr)
		send(c, "GET /b/o HTTP/1.1\r\nHost: s3\r\n\r\n")
		Eventually(entered).WithTimeout(time.Second).WithPolling(poll).Should(BeClosed())
		Expect(c.Close()).To(Succeed())
		Eventually(canceled).WithTimeout(time.Second).WithPolling(poll).Should(BeClosed())
	})

	It("keeps the request's context while the handler works on after reading the whole body", func(ctx SpecContext) {
		addr := deadlineServer(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			// A decoder checking for trailing data reads again at EOF.
			if n, err := r.Body.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
				http.Error(w, fmt.Sprintf("read %d past EOF: %v", n, err), http.StatusInternalServerError)
				return
			}
			select {
			case <-r.Context().Done():
				http.Error(w, context.Cause(r.Context()).Error(), http.StatusInternalServerError)
			case <-time.After(3 * opTimeout):
				fmt.Fprint(w, "live")
			}
		}, opTimeout)
		c := dial(ctx, addr)
		send(c, "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: 4\r\n\r\ndata")
		Expect(readResponse(bufio.NewReader(c))).To(And(HaveHTTPStatus(http.StatusOK), HaveHTTPBody("live")))
	})

	It("answers a stalled upload the handler refuses at once, then bounds the read of the rest, as beast does", func(ctx SpecContext) {
		const timeout = 2 * time.Second
		addr := deadlineServer(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "AccessDenied", http.StatusForbidden)
		}, timeout)
		c := dial(ctx, addr)
		began := time.Now()
		send(c, "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: 100\r\n\r\n")
		r := bufio.NewReader(c)
		Expect(readResponse(r)).To(And(HaveHTTPStatus(http.StatusForbidden), HaveHTTPBody("AccessDenied\n")))
		Expect(time.Since(began)).To(BeNumerically("<", timeout/2), "the response waited on the unread body")
		closedWithin(c, r, 2*timeout)
	})

	It("does not hold a handler's write on a body it left unread", func(ctx SpecContext) {
		wrote := make(chan error, 1)
		addr := deadlineServer(func(w http.ResponseWriter, _ *http.Request) {
			// More than net/http buffers, so the write sends the header.
			_, err := w.Write(make([]byte, 4<<10))
			wrote <- err
		}, opTimeout)
		c := dial(ctx, addr)
		send(c, "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: 100\r\n\r\n")
		// Held for the unread body, the write would fail with its deadline.
		Eventually(wrote).WithTimeout(time.Second).WithPolling(poll).Should(Receive(BeNil()))
		r := bufio.NewReader(c)
		Expect(readResponse(r)).To(And(HaveHTTPStatus(http.StatusOK), HaveHTTPBody(HaveLen(4<<10))))
		closedWithin(c, r, time.Second)
	})

	It("closes the connection without a response once an operation times out, as beast's timer shuts the socket down", func(ctx SpecContext) {
		addr := deadlineServer(func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.ReadAll(r.Body); err != nil {
				http.Error(w, "request timed out", http.StatusBadRequest)
			}
		}, opTimeout)
		c := dial(ctx, addr)
		send(c, "PUT /b/o HTTP/1.1\r\nHost: s3\r\nContent-Length: 100\r\n\r\n")
		closedWithin(c, bufio.NewReader(c), time.Second)
	})
})

// deadlineLog is an http.ResponseWriter with a connection's deadline setters,
// logging each deadline set and each write, flush and body read or close in
// the order they happen.
type deadlineLog struct {
	*httptest.ResponseRecorder
	timeout time.Duration

	mu     sync.Mutex
	events []string
}

func newDeadlineLog(timeout time.Duration) *deadlineLog {
	return &deadlineLog{ResponseRecorder: httptest.NewRecorder(), timeout: timeout}
}

func (l *deadlineLog) log(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *deadlineLog) Events() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// deadline logs how t sets direction's deadline: armed with the timeout,
// cleared, or expired.
func (l *deadlineLog) deadline(direction string, t time.Time) {
	left := time.Until(t)
	switch {
	case t.IsZero():
		l.log(direction + " clear")
	case left < 0:
		l.log(direction + " expire")
	case left <= l.timeout && left > l.timeout-time.Minute:
		l.log(direction + " arm")
	default:
		l.log(fmt.Sprintf("%s deadline in %s", direction, left))
	}
}

func (l *deadlineLog) SetReadDeadline(t time.Time) error {
	l.deadline("read", t)
	return nil
}

func (l *deadlineLog) SetWriteDeadline(t time.Time) error {
	l.deadline("write", t)
	return nil
}

func (l *deadlineLog) Write(p []byte) (int, error) {
	l.log("write")
	return l.ResponseRecorder.Write(p)
}

func (l *deadlineLog) Flush() {
	l.log("flush")
	l.ResponseRecorder.Flush()
}

func (l *deadlineLog) EnableFullDuplex() error {
	l.log("full duplex")
	return nil
}

// logBody is a request body of left bytes that logs each read and its close;
// every read fails with fail when that is set.
type logBody struct {
	log  *deadlineLog
	left int
	fail error
}

func (b *logBody) Read(p []byte) (int, error) {
	b.log.log("body read")
	if b.fail != nil {
		return 0, b.fail
	}
	n := min(len(p), b.left)
	b.left -= n
	if b.left == 0 {
		return n, io.EOF
	}
	return n, nil
}

func (b *logBody) Close() error {
	b.log.log("body close")
	return nil
}

var _ = Describe("Deadlines", func() {
	const timeout = time.Hour

	var (
		l   *deadlineLog
		req *http.Request
	)

	BeforeEach(func(ctx SpecContext) {
		l = newDeadlineLog(timeout)
		req = httptest.NewRequestWithContext(ctx, http.MethodPut, "/b/o", http.NoBody)
		req.Body = &logBody{log: l, left: 6}
	})

	It("arms each operation's deadline as it starts and clears it when it returns", func() {
		frontend.Deadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			buf := make([]byte, 4)
			Expect(r.Body.Read(buf)).To(Equal(4))
			n, err := r.Body.Read(buf)
			Expect(n).To(Equal(2))
			Expect(err).To(MatchError(io.EOF))
			n, err = r.Body.Read(buf)
			Expect(n).To(BeZero())
			Expect(err).To(MatchError(io.EOF))
			fmt.Fprint(w, "ok")
			Expect(http.NewResponseController(w).Flush()).To(Succeed())
		}), timeout).ServeHTTP(l, req)
		Expect(l.Events()).To(Equal([]string{
			"full duplex",
			"read arm", "body read", "read clear",
			"read arm", "body read", "read clear",
			"body read",
			"write arm", "write", "write clear",
			"write arm", "flush", "write clear",
			"write arm",
		}), "a read at EOF leaves the connection alone, and the write deadline stays armed for net/http's final flush")
	})

	It("arms the read deadline after the handler while the body is unread, for net/http's read of the rest", func() {
		frontend.Deadlines(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "ok")
		}), timeout).ServeHTTP(l, req)
		Expect(l.Events()).To(Equal([]string{
			"full duplex",
			"write arm", "write", "write clear",
			"read arm", "write arm",
		}))
	})

	It("arms the read deadline while the handler closes an unread body, which net/http reads on", func() {
		frontend.Deadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(r.Body.Close()).To(Succeed())
			fmt.Fprint(w, "ok")
		}), timeout).ServeHTTP(l, req)
		Expect(l.Events()).To(Equal([]string{
			"full duplex",
			"read arm", "body close", "read clear",
			"write arm", "write", "write clear",
			"write arm",
		}))
	})

	It("expires both deadlines when an operation times out and arms none after, as beast's timer shuts the socket down", func() {
		req.Body = &logBody{log: l, fail: fmt.Errorf("reading: %w", os.ErrDeadlineExceeded)}
		frontend.Deadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := r.Body.Read(make([]byte, 4))
			Expect(err).To(MatchError(os.ErrDeadlineExceeded))
			fmt.Fprint(w, "late")
		}), timeout).ServeHTTP(l, req)
		Expect(l.Events()).To(Equal([]string{
			"full duplex",
			"read arm", "body read", "read expire", "write expire",
			"write",
		}))
	})

	It("arms nothing for a timeout of zero, as beast's timer does not start, but still answers before the rest of the body", func() {
		frontend.Deadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Expect(io.ReadAll(r.Body)).To(HaveLen(6))
			fmt.Fprint(w, "ok")
			Expect(http.NewResponseController(w).Flush()).To(Succeed())
		}), 0).ServeHTTP(l, req)
		Expect(l.Events()).To(Equal([]string{"full duplex", "body read", "write", "flush"}))
	})

	It("leaves a request without a body its http.NoBody", func(ctx SpecContext) {
		var body io.ReadCloser
		frontend.Deadlines(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			body = r.Body
		}), timeout).ServeHTTP(l, httptest.NewRequestWithContext(ctx, http.MethodGet, "/b/o", http.NoBody))
		Expect(body).To(BeIdenticalTo(http.NoBody))
	})

	It("reaches the connection's other controls through Unwrap", func() {
		frontend.Deadlines(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			Expect(http.NewResponseController(w).EnableFullDuplex()).To(Succeed())
		}), timeout).ServeHTTP(l, req)
		Expect(l.Events()).To(HaveExactElements("full duplex", "full duplex", "read arm", "write arm"),
			"Deadlines enables full duplex itself, then the handler's call reaches the writer through Unwrap")
	})
})
