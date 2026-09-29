package frontend

import (
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Deadlines wraps h so each read of the request body and each write or flush
// of the response must finish within timeout, beast's per-operation
// request_timeout_ms, while the http.Server itself keeps zero timeouts. As
// with beast's timer (rgw_asio_frontend_timer.h:45-56 at v19.2.6 and
// v20.2.4), a deadline is armed when an operation starts and cleared when it
// returns, a timeout of zero arms nothing, and an operation that times out
// ends the connection, which timeout_handler shuts down (:20-26).
//
// Whatever the timeout, the response goes out before net/http reads what h
// left of the body, as beast answers before it discards the rest
// (rgw_asio_frontend.cc:362-389 at v19.2.6, :372-399 at v20.2.4). The
// deadlines bound that read and net/http's final flush too.
func Deadlines(h http.Handler, timeout time.Duration) http.Handler {
	return deadlines{h: h, timeout: timeout}
}

type deadlines struct {
	h       http.Handler
	timeout time.Duration
}

func (d deadlines) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// Without full duplex net/http reads the rest of the body before it sends
	// the response header (chunkWriter.writeHeader); with it, finishRequest
	// reads it after the response.
	_ = rc.EnableFullDuplex() //nolint:errcheck // only a writer without full duplex refuses it, and HTTP/1 net/http has it
	if d.timeout <= 0 {
		d.h.ServeHTTP(w, r)
		return
	}
	c := &connDeadlines{rc: rc, timeout: d.timeout}
	if r.Body != nil && r.Body != http.NoBody {
		c.unread.Store(true)
		r2 := new(http.Request)
		*r2 = *r
		r2.Body = &deadlineBody{ReadCloser: r.Body, c: c}
		r = r2
	}
	d.h.ServeHTTP(&deadlineWriter{ResponseWriter: w, c: c}, r)
	// After h returns net/http flushes what h left buffered, then reads up to
	// 256 KiB of the body h left unread (response.finishRequest), operations
	// beast times too. It clears the write deadline once the response is out
	// and sets the read deadline for the idle wait (conn.serve).
	c.arm(c.unread.Load(), true)
}

// connDeadlines is one request's hold on its connection's two deadlines. The
// handler may read in one goroutine while it writes in another, but net/http
// allows neither two reads of the body nor two writes at once.
type connDeadlines struct {
	rc      *http.ResponseController
	timeout time.Duration
	// unread is the handler's view of the body: set until a read of it
	// returns EOF or the handler closes it. Past that point a read is
	// answered from net/http's state, not from the connection.
	unread atomic.Bool

	mu      sync.Mutex
	expired bool
}

// aLongTimeAgo is a deadline already passed, which fails every read and
// write at once.
var aLongTimeAgo = time.Unix(1, 0)

// arm starts the deadlines an operation needs.
func (c *connDeadlines) arm(read, write bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := time.Now().Add(c.timeout)
	if read {
		c.set(c.rc.SetReadDeadline, t)
	}
	if write {
		c.set(c.rc.SetWriteDeadline, t)
	}
}

// disarm clears the deadlines an operation that returned err needed, or,
// when it timed out, expires both so the connection ends without another
// read or write, as beast's shut-down socket does.
func (c *connDeadlines) disarm(read, write bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if errors.Is(err, os.ErrDeadlineExceeded) {
		c.set(c.rc.SetReadDeadline, aLongTimeAgo)
		c.set(c.rc.SetWriteDeadline, aLongTimeAgo)
		c.expired = true
		return
	}
	if read {
		c.set(c.rc.SetReadDeadline, time.Time{})
	}
	if write {
		c.set(c.rc.SetWriteDeadline, time.Time{})
	}
}

// set applies a deadline unless the connection has expired. net/http fails
// it only for a closed connection, which ends the request as a timeout does,
// and a writer without a connection answers http.ErrNotSupported and has no
// deadline to set.
func (c *connDeadlines) set(apply func(time.Time) error, t time.Time) {
	if c.expired {
		return
	}
	if err := apply(t); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.expired = true
	}
}

// deadlineBody times each read of the request body.
type deadlineBody struct {
	io.ReadCloser
	c *connDeadlines
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	if !b.c.unread.Load() {
		// net/http's background read, which starts at EOF, must not inherit
		// a deadline.
		return b.ReadCloser.Read(p)
	}
	b.c.arm(true, false)
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.c.unread.Store(false)
	}
	b.c.disarm(true, false, err)
	return n, err
}

// Close times the read net/http makes of an unread body, up to 256 KiB, to
// reuse the connection (body.Close).
func (b *deadlineBody) Close() error {
	if !b.c.unread.Swap(false) {
		return b.ReadCloser.Close()
	}
	b.c.arm(true, false)
	err := b.ReadCloser.Close()
	b.c.disarm(true, false, err)
	return err
}

// deadlineWriter times each write and flush of the response. It has no
// ReadFrom, which would make a whole copy one operation.
type deadlineWriter struct {
	http.ResponseWriter
	c *connDeadlines
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	w.c.arm(false, true)
	n, err := w.ResponseWriter.Write(p)
	w.c.disarm(false, true, err)
	return n, err
}

// FlushError flushes the response; http.ResponseController.Flush calls it.
func (w *deadlineWriter) FlushError() error {
	w.c.arm(false, true)
	err := w.c.rc.Flush()
	w.c.disarm(false, true, err)
	return err
}

// Flush is FlushError for a caller holding an http.Flusher.
func (w *deadlineWriter) Flush() {
	_ = w.FlushError() //nolint:errcheck // http.Flusher has no error to return, and the next write fails the same way
}

// Unwrap lets http.ResponseController reach the connection's other controls.
func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
