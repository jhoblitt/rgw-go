package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

// Config is what the handler needs of the process.
type Config struct {
	// Prefix is rgw_admin_entry, "admin" by default.
	Prefix        string
	TransIDSuffix string
	ServerHeader  string
	// MaxConcurrent is rgw_max_concurrent_requests, which the handler counts
	// against admin requests alone; 0 means unlimited.
	MaxConcurrent int
	// DNSNames are the hostnames a Host names a bucket under, as s3.Config's:
	// the handler parses the path with that bucket in front, as radosgw's
	// manager lookup does.
	DNSNames []string
}

// HandlerFunc serves one route: it runs the op and renders the response. It
// returns an error only before anything was written.
type HandlerFunc func(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error

// Handler is the admin API as one http.Handler, the RGWRESTMgr_Admin tree.
type Handler struct {
	env     *op.Env
	auth    s3.Authenticator
	cfg     Config
	metrics op.Metrics
	routes  map[string]HandlerFunc
	// inflight is the admin requests counted against MaxConcurrent.
	inflight atomic.Int64
}

// NewHandler builds the handler with every route Dispatch yields running
// unregistered until a handler is registered for it, then the routes this
// package serves registered. It panics on a route without its cap pairs.
func NewHandler(env *op.Env, auth s3.Authenticator, cfg Config) *Handler {
	h := &Handler{env: env, auth: auth, cfg: cfg, metrics: env.Metrics, routes: map[string]HandlerFunc{}}
	if h.metrics == nil {
		h.metrics = op.NopMetrics{}
	}
	for _, name := range routeNames {
		if len(routeCaps[name]) == 0 {
			panic(fmt.Sprintf("admin: route %q has no cap pairs", name))
		}
		h.routes[name] = unregistered(name)
	}
	for _, routes := range []map[string]HandlerFunc{infoHandlers(), newUserHandlers(), newUserSubHandlers(), newBucketHandlers()} {
		for name, fn := range routes {
			h.Register(name, fn)
		}
	}
	return h
}

// unregistered serves a route no handler is registered for: radosgw's
// permission check for the op, so a caller radosgw refuses is refused, then
// NotImplemented.
func unregistered(name string) HandlerFunc {
	return func(ctx context.Context, _ http.ResponseWriter, r *op.Request, _ Request) error {
		return op.Run(ctx, &unregisteredOp{name: name}, r)
	}
}

// unregisteredOp is an admin op with its check_caps and no execution.
type unregisteredOp struct {
	op.AdminOp
	name string
}

func (o *unregisteredOp) Name() string { return o.name }

// VerifyPermission passes when the identity holds any of the route's cap
// pairs, which Run lets an admin identity through.
func (o *unregisteredOp) VerifyPermission(_ context.Context, r *op.Request) error {
	for _, c := range accountCaps(o.name, r.Env.Zone.Release()) {
		if op.CheckCaps(r, c.typ, c.perm) == nil {
			return nil
		}
	}
	return op.ErrAccessDenied
}

func (o *unregisteredOp) Execute(context.Context, *op.Request) error { return op.ErrNotImplemented }

// Register installs fn for the route named name, replacing any handler
// there, before the handler serves. It panics on a name Dispatch never
// yields.
func (h *Handler) Register(name string, fn HandlerFunc) {
	if !slices.Contains(routeNames, name) {
		panic(fmt.Sprintf("admin: no route is named %q", name))
	}
	h.routes[name] = fn
}

// InFlight returns the admin requests counted against MaxConcurrent: those
// dispatched and not yet answered.
func (h *Handler) InFlight() int64 { return h.inflight.Load() }

// ServeHTTP runs radosgw's process_request for an admin request
// (rgw_process.cc:298-463 at v19.2.6, :280-470 at v20.2.4). It gives the
// request its transaction id, drawn at random as radosgw draws it
// (StoreDriver::get_new_req_id, rgw_sal_store.h:27-29 at v19.2.6), and sends
// x-amz-request-id and Server on every response. Once the response is
// written it observes the request; it logs no usage, as radosgw registers
// the admin managers without set_logging (rgw_appmain.cc:354-361 at
// v19.2.6). A panic before anything was written is answered 500; one after
// it ends the connection. Every line logged under the request's context
// carries its transaction id.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	h.metrics.InFlight(1)
	defer h.metrics.InFlight(-1)

	now := h.env.Clock()
	id := op.TransID(rand.Uint64(), now, h.cfg.TransIDSuffix) //nolint:gosec // a request id must be unique, not unpredictable
	ctx := op.WithRequestID(req.Context(), id)
	w.Header().Set("x-amz-request-id", id)
	if h.cfg.ServerHeader != "" {
		w.Header().Set("Server", h.cfg.ServerHeader)
	}
	r := &op.Request{
		ID:            id,
		Time:          now,
		Method:        req.Method,
		Host:          strings.ToLower(vhost.Hostname(req.Host)),
		Path:          req.URL.Path,
		RawPath:       req.URL.EscapedPath(),
		RawQuery:      req.URL.RawQuery,
		Header:        req.Header,
		Body:          req.Body,
		ContentLength: req.ContentLength,
		RemoteAddr:    req.RemoteAddr,
		Referer:       req.Referer(),
		TLS:           req.TLS != nil,
		Env:           h.env,
	}
	rw := &responseWriter{ResponseWriter: w, r: r}
	st := &served{route: "unknown"}

	abort := false
	defer func() {
		if r.Status == 0 {
			r.Status = http.StatusOK
		}
		elapsed := time.Since(start)
		h.metrics.Observe(st.route, r.Status, elapsed, r.BytesIn, r.BytesOut)
		slog.DebugContext(ctx, "admin request done", slog.String("op", st.route),
			slog.Int("status", r.Status), slog.Duration("elapsed", elapsed))
		if abort {
			panic(http.ErrAbortHandler)
		}
	}()
	defer func() {
		v := recover()
		if v == nil {
			return
		}
		if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
			abort = true
			return
		}
		slog.ErrorContext(ctx, "admin request handler panicked", slog.String("op", st.route),
			slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
		if rw.wrote {
			abort = true
			return
		}
		WriteError(ctx, rw, r, st.q, op.ErrInternalError)
	}()
	h.serve(ctx, rw, req, r, st)
}

// served is how far the request got: its parse, JSON until the handler has
// selected the format, and its route, "unknown" until one is dispatched.
type served struct {
	q     Request
	route string
}

// serve is process_request's order for an admin request: preprocess's
// refusal of a NUL in the decoded URI and the manager lookup, both answered
// in JSON because no handler has allocated a formatter yet (abort_early,
// rgw_rest.cc:676-683 at v19.2.6); the handler's format; get_op;
// schedule_request; verify_requester; the suspended-user check; then the op
// (rgw_process.cc:310-405 at v19.2.6). The admin handlers' postauth_init,
// RGWHandler_Auth_S3's, does nothing (rgw_rest_s3.h:646 at v19.2.6).
func (h *Handler) serve(ctx context.Context, w *responseWriter, req *http.Request, r *op.Request, st *served) {
	uri := s3.DecodedURI(req, s3.Config{DNSNames: h.cfg.DNSNames})
	if strings.IndexByte(uri, 0) >= 0 {
		WriteError(ctx, w, r, st.q, op.ErrInvalidRequest)
		return
	}
	q, err := ParseRequest(uri, h.cfg.Prefix, req.URL.RawQuery)
	r.Query = q.Args.values()
	if err != nil {
		WriteError(ctx, w, r, st.q, err)
		return
	}
	accept, _ := op.HeaderValue(req.Header, "Accept")
	q.Format = SelectFormat(q.Args, accept)
	st.q = q
	route, err := Dispatch(req.Method, q, h.env.Zone.Release())
	if err != nil {
		WriteError(ctx, w, r, q, err)
		return
	}
	st.route = route.Name

	// radosgw's scheduler counts a request it refuses until that request is
	// answered, and answers -EAGAIN, which process_request sends as
	// ERR_RATE_LIMITED (rgw_process.cc:330-338 at v19.2.6).
	n := h.inflight.Add(1)
	defer h.inflight.Add(-1)
	if h.cfg.MaxConcurrent > 0 && n > int64(h.cfg.MaxConcurrent) {
		WriteError(ctx, w, r, q, op.ErrSlowDown)
		return
	}

	res, err := h.auth.Authenticate(ctx, req, route.Payloads)
	if err != nil {
		refuseAuth(w, r, q, err)
		return
	}
	s3.ApplyAuth(r, res)
	r.Tenant = r.Identity.Tenant
	if u := r.Identity.User; u != nil && u.Suspended != 0 {
		WriteError(ctx, w, r, q, op.ErrUserSuspended)
		return
	}

	if err := h.routes[route.Name](ctx, w, r, q); err != nil {
		if w.wrote {
			slog.WarnContext(ctx, "admin route failed after its response started",
				slog.String("op", route.Name), slog.Any("error", err))
			return
		}
		WriteError(ctx, w, r, q, err)
	}
}

// values is the arguments as op.Request's Query holds them, the "rgwx-"
// names included.
func (a Args) values() url.Values {
	v := make(url.Values, len(a.vals)+len(a.sys))
	for _, m := range []map[string]string{a.vals, a.sys} {
		for name, val := range m {
			v[name] = []string{val}
		}
	}
	return v
}

// responseWriter records the status and body bytes the metrics observe, and
// lets http.ResponseController reach the connection through FlushError and
// Unwrap. It adds no header of its own: Accept-Ranges goes out only where
// render.go names the length, as radosgw sends it only from
// dump_content_length.
type responseWriter struct {
	http.ResponseWriter
	r     *op.Request
	wrote bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.r.Status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write counts nothing for a HEAD, whose body net/http discards and radosgw
// never sends (rgw_flush_formatter, rgw_rest.cc:316-324 at v19.2.6).
func (w *responseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	n, err := w.ResponseWriter.Write(p)
	if w.r.Method != http.MethodHead {
		w.r.BytesOut += int64(n)
	}
	return n, err
}

// FlushError flushes the response; http.ResponseController.Flush calls it.
func (w *responseWriter) FlushError() error {
	w.WriteHeader(http.StatusOK)
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Flush is FlushError for a caller holding an http.Flusher.
func (w *responseWriter) Flush() {
	_ = w.FlushError() //nolint:errcheck // http.Flusher has no error to return, and the next write fails the same way
}

// Unwrap lets http.ResponseController reach the connection's deadlines.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
