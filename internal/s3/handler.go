package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// HandlerFunc serves one route: it fills the op from r, runs it and renders
// the response. It returns an error only before anything was written.
type HandlerFunc func(ctx context.Context, w http.ResponseWriter, r *op.Request) error

// Handler is the S3 protocol layer as one http.Handler.
type Handler struct {
	env     *op.Env
	auth    Authenticator
	cfg     Config
	metrics op.Metrics
	routes  map[routeKey]HandlerFunc
	// inflight is SimpleThrottler's outstanding_requests.
	inflight atomic.Int64
}

// routeKey binds a handler: a route name can be routed in more than one
// scope, get_acls in the bucket's and the object's, each with its own handler.
type routeKey struct {
	scope op.Scope
	name  string
}

// unitHandlers is the units' route maps, by the scope their entries bind to.
type unitHandlers struct {
	service, bucket, object, multipart map[string]HandlerFunc
}

var scopeNames = [...]string{op.ScopeService: "service", op.ScopeBucket: "bucket", op.ScopeObject: "object"}

// NewHandler builds the handler with every implemented route registered and
// every other route answering NotImplemented. It binds each entry of
// serviceHandlers, bucketHandlers and objectHandlers to its map's scope, and
// each entry of multipartHandlers to the one scope Dispatch routes its name
// in. It panics on an entry whose scope Dispatch never routes its name to, a
// multipartHandlers entry routed in more than one scope, and a scope and
// name bound twice.
func NewHandler(env *op.Env, auth Authenticator, cfg Config) *Handler {
	return buildHandler(env, auth, cfg, unitHandlers{
		service:   serviceHandlers(),
		bucket:    bucketHandlers(),
		object:    objectHandlers(env),
		multipart: multipartHandlers(),
	})
}

func buildHandler(env *op.Env, auth Authenticator, cfg Config, u unitHandlers) *Handler {
	h := &Handler{env: env, auth: auth, cfg: cfg, metrics: env.Metrics, routes: map[routeKey]HandlerFunc{}}
	if h.metrics == nil {
		h.metrics = op.NopMetrics{}
	}
	for scope, fns := range map[op.Scope]map[string]HandlerFunc{
		op.ScopeService: u.service,
		op.ScopeBucket:  u.bucket,
		op.ScopeObject:  u.object,
	} {
		for name, fn := range fns {
			h.bind(scope, name, fn)
		}
	}
	for name, fn := range u.multipart {
		scopes := routedScopes(name)
		if len(scopes) != 1 {
			panic(fmt.Sprintf("s3: multipart route %q is routed in %d scopes, not one", name, len(scopes)))
		}
		h.bind(scopes[0], name, fn)
	}
	return h
}

func (h *Handler) bind(scope op.Scope, name string, fn HandlerFunc) {
	if !routedAt(scope, name) {
		panic(fmt.Sprintf("s3: no %s route is named %q", scopeNames[scope], name))
	}
	k := routeKey{scope, name}
	if _, dup := h.routes[k]; dup {
		panic(fmt.Sprintf("s3: %s route %q is bound twice", scopeNames[scope], name))
	}
	h.routes[k] = fn
}

// Register installs fn for the route named name under every scope that
// routes it, replacing any handler there, before the handler serves. The
// units bind their routes through their files' maps, which NewHandler
// binds; Register replaces a route for a spec. It panics on a name no scope
// routes.
func (h *Handler) Register(name string, fn HandlerFunc) {
	scopes := routedScopes(name)
	if len(scopes) == 0 {
		panic(fmt.Sprintf("s3: no route is named %q", name))
	}
	for _, scope := range scopes {
		h.routes[routeKey{scope, name}] = fn
	}
}

// InFlight returns the requests counted against rgw_max_concurrent_requests:
// those dispatched and not yet answered.
func (h *Handler) InFlight() int64 { return h.inflight.Load() }

// routedAt reports whether Dispatch routes name at scope at some release:
// through a row or a method's default in its table, or through one of the
// two choices Dispatch makes past the table, list_bucket_v2 and copy_obj.
func routedAt(scope op.Scope, name string) bool {
	if name == "" {
		return false
	}
	if scope == op.ScopeBucket && name == "list_bucket_v2" || scope == op.ScopeObject && name == "copy_obj" {
		return true
	}
	for k, m := range table {
		if k.scope != scope {
			continue
		}
		if m.def == name {
			return true
		}
		for _, rw := range m.rows {
			if rw.name == name {
				return true
			}
		}
	}
	return false
}

func routedScopes(name string) []op.Scope {
	var scopes []op.Scope
	for _, scope := range [...]op.Scope{op.ScopeService, op.ScopeBucket, op.ScopeObject} {
		if routedAt(scope, name) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// radosgwNames maps each route whose name is rgw-go's own to its op's
// RGWOp::name() (rgw_op.h:916 and :970 at v19.2.6, :988 and :1042 at
// v20.2.4; rgw_rest_pubsub.cc:1197, :1452 and :1556 at v19.2.6, :1208, :1464
// and :1569 at v20.2.4). S3 Select's op is an RGWGetObj.
var radosgwNames = map[string]string{
	"get_usage":                  "get_self_usage",
	"list_bucket_v2":             "list_bucket",
	"select_obj":                 "get_obj",
	"put_bucket_notification":    "pubsub_notification_create_s3",
	"delete_bucket_notification": "pubsub_notification_delete_s3",
	"get_bucket_notification":    "pubsub_notifications_get_s3",
}

// usageName is the RGWOp::name() of the op radosgw runs for route at rel,
// the category the usage log files the request under (rgw_log.cc:245 and
// :556 at v19.2.6 and v20.2.4). Squid's radosgw runs ?attributes as
// GetObject (rgw_rest_s3.cc:4805-4821 at v19.2.6).
func usageName(route string, rel denc.Release) string {
	if route == "get_obj_attrs" && rel == denc.Squid {
		return "get_obj"
	}
	if name, ok := radosgwNames[route]; ok {
		return name
	}
	return route
}

// served is how far the request got: the route's name and radosgw's op name,
// both "unknown" until a route is dispatched, as rgw_log_op names a request
// no op serves (rgw_log.cc:556), and whether postauth_init has named its
// bucket.
type served struct {
	route, usage string
	named        bool
}

// ServeHTTP runs radosgw's process_request (rgw_process.cc:298-412 at
// v19.2.6, :280-419 at v20.2.4) for req. It gives the request its
// transaction id and sends x-amz-request-id and Server on every response,
// as end_header does (rgw_rest.cc:589-651 at v19.2.6, :594-656 at v20.2.4).
// Once the response is written it observes the request and logs its usage,
// refused requests included, as process_request's tail does (:454-462 at
// v19.2.6, :461-469 at v20.2.4). A panic anywhere before anything was
// written is answered 500, observed and usage-logged; one after it ends the
// connection, so a truncated body is never framed as complete.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	ctx := req.Context()
	h.metrics.InFlight(1)
	defer h.metrics.InFlight(-1)

	now := h.env.Clock()
	// radosgw draws each request's number at random (StoreDriver::get_new_req_id,
	// rgw_sal_store.h:27-29 at v19.2.6, :101-103 at v20.2.4).
	id := op.TransID(rand.Uint64(), now, h.cfg.TransIDSuffix) //nolint:gosec // a request id must be unique, not unpredictable
	w.Header().Set("x-amz-request-id", id)
	if h.cfg.ServerHeader != "" {
		w.Header().Set("Server", h.cfg.ServerHeader)
	}
	// rw.r is the request the observation reports: this stand-in until the
	// parse yields the request itself.
	rw := &responseWriter{ResponseWriter: w, r: &op.Request{ID: id, Env: h.env, Time: now, Method: req.Method}}
	st := &served{route: "unknown", usage: "unknown"}

	abort := false
	defer func() {
		r := rw.r
		// radosgw's status defaults to 200, as net/http's does for a
		// handler that writes nothing.
		if r.Status == 0 {
			r.Status = http.StatusOK
		}
		elapsed := time.Since(start)
		h.metrics.Observe(st.route, r.Status, elapsed, r.BytesIn, r.BytesOut)
		op.LogUsage(ctx, r, st.usage)
		slog.DebugContext(ctx, "request done", slog.String("request_id", r.ID), slog.String("op", st.route),
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
		slog.ErrorContext(ctx, "request handler panicked", slog.String("request_id", id), slog.String("op", st.route),
			slog.Any("panic", v), slog.String("stack", string(debug.Stack())))
		switch {
		case rw.wrote:
			abort = true
		case st.named:
			WriteError(ctx, rw, rw.r, op.ErrInternalError)
		default:
			refuse(ctx, rw, rw.r, op.ErrInternalError)
		}
	}()
	p, err := h.parse(req, now)
	if p.Req != nil {
		p.Req.ID, p.Req.Env = id, h.env
		rw.r = p.Req
	}
	if err != nil {
		refuse(ctx, rw, rw.r, err)
		return
	}
	h.serve(ctx, rw, req, p, st)
}

// parse is RGWREST::get_handler (rgw_rest.cc:2276-2318 at v19.2.6,
// :2298-2340 at v20.2.4): preprocess's refusal of a NUL in the URL,
// RGWRESTMgr_S3::get_handler's refusal of an object's subresource at bucket
// scope, then RGWHandler_REST_S3::init's refusal of a copy source
// parse_copy_location cannot parse (rgw_rest_s3.cc:5026-5041 at v19.2.6,
// :5586-5601 at v20.2.4), whatever the method and scope. radosgw refuses a
// method no S3 op serves only later, in get_op (rgw_process.cc:325-329 at
// v19.2.6, :327-331 at v20.2.4); the parse does not depend on the method,
// so a request with such a method is parsed as a GET to reach the earlier
// refusals first.
func (h *Handler) parse(req *http.Request, now time.Time) (Parsed, error) {
	p, err := ParseRequest(req, h.cfg, now)
	var methodErr error
	if errors.Is(err, op.ErrMethodNotAllowed) {
		probe := req.WithContext(req.Context())
		probe.Method = http.MethodGet
		if p, err = ParseRequest(probe, h.cfg, now); err == nil {
			p.Req.Method = req.Method
		}
		methodErr = op.ErrMethodNotAllowed
	}
	if err != nil {
		return p, err
	}
	r := p.Req
	if r.Scope() == op.ScopeBucket && hasObjectSubresource(r.Query) {
		return p, op.ErrMethodNotAllowed
	}
	if v, ok := copySource(r); ok {
		if _, parsed := copySourceBucket(v); !parsed {
			return p, op.ErrInvalidArgument // parse_copy_location's false is -EINVAL
		}
	}
	return p, methodErr
}

// serve dispatches p, caps the requests in flight, authenticates and runs
// the route, in process_request's order: get_op, schedule_request, the
// op_type set from the dispatched op, verify_requester, postauth_init, the
// suspended-user check, then the op (rgw_process.cc:325-405 at v19.2.6,
// :327-412 at v20.2.4). It records the dispatched route in st.
func (h *Handler) serve(ctx context.Context, w *responseWriter, req *http.Request, p Parsed, st *served) {
	r := p.Req
	rel := h.env.Zone.Release()
	route, err := Dispatch(r, rel)
	if err != nil {
		refuse(ctx, w, r, err)
		return
	}
	st.route, st.usage = route.Name, usageName(route.Name, rel)

	// SimpleThrottler counts a request it refuses until that request is
	// answered too (rgw_dmclock_async_scheduler.h:184-207 at v19.2.6), and
	// answers -EAGAIN, which process_request sends as ERR_RATE_LIMITED.
	n := h.inflight.Add(1)
	defer h.inflight.Add(-1)
	if h.cfg.MaxConcurrent > 0 && n > int64(h.cfg.MaxConcurrent) {
		refuse(ctx, w, r, op.ErrSlowDown)
		return
	}

	res, err := h.auth.Authenticate(ctx, req, route.Payloads)
	if err != nil {
		refuseAuth(w, r, err)
		return
	}
	ApplyAuth(r, res)
	if route.Name == "put_obj" && r.Body != nil {
		r.Body = &countingReader{r: r.Body, n: &r.BytesIn}
	}
	err = PostAuthInit(p, r.Identity.Tenant)
	st.named = true
	if err != nil {
		if !p.ExplicitTenant {
			// rgw_parse_url_bucket names no bucket for a token that ends at
			// its first colon, the one that stays unsplit.
			if _, bucket, ok := strings.Cut(r.Bucket, ":"); ok {
				r.Bucket = bucket
			}
		}
		WriteError(ctx, w, r, err)
		return
	}
	if u := r.Identity.User; u != nil && u.Suspended != 0 {
		WriteError(ctx, w, r, op.ErrUserSuspended)
		return
	}

	fn := h.routes[routeKey{route.Scope, route.Name}]
	if fn == nil {
		WriteError(ctx, w, r, op.ErrNotImplemented)
		return
	}
	if err := fn(ctx, w, r); err != nil {
		if w.wrote {
			slog.WarnContext(ctx, "route failed after its response started", slog.String("request_id", r.ID),
				slog.String("op", route.Name), slog.Any("error", err))
			return
		}
		WriteError(ctx, w, r, err)
	}
}

// refuse answers a request refused before postauth_init has named its
// bucket, so neither the error document nor the usage log names one (dump,
// rgw_common.cc:397-398 at v19.2.6, :410-411 at v20.2.4).
func refuse(ctx context.Context, w http.ResponseWriter, r *op.Request, err error) {
	r.Bucket = ""
	WriteError(ctx, w, r, err)
}

// refuseAuth answers a failed authentication as refuse does and logs
// nothing: an authenticator's error can carry what the request's
// credentials hold, and the authenticator logs its own failures.
func refuseAuth(w http.ResponseWriter, r *op.Request, err error) {
	r.Bucket = ""
	writeErrorDocument(w, r, op.AsError(err))
}

// ApplyAuth gives r the authenticated identity and, when the authenticator
// replaces the request body with a reader that verifies the payload as it is
// read, that reader and its length. Nothing reads the request body before
// this. The admin handler applies its AuthResult through it too.
func ApplyAuth(r *op.Request, res *op.AuthResult) {
	r.Identity = res.Identity
	if res.Body != nil {
		r.Body = res.Body
		r.ContentLength = res.ContentLength
	}
}

// countingReader counts what the op reads into n. radosgw's accounting
// filter sits above the payload decoders (rgw_client_io.h:334-349 at
// v19.2.6 and v20.2.4) and counts a request body only inside PutObject's
// data reads (rgw_rest.cc:1082-1093 at v19.2.6, :1087-1098 at v20.2.4).
type countingReader struct {
	r io.Reader
	n *int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// responseWriter records the status and body bytes the metrics and the
// usage log observe, and lets http.ResponseController reach the connection
// through FlushError and Unwrap. It adds no header: Accept-Ranges goes out
// only where a route calls SetContentLength or WriteError, as radosgw sends
// it only where dump_content_length runs. It is not an op.Sink, whose
// WriteHeader takes the headers too; sinkOf adapts it.
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

// sink is the op.Sink sinkOf returns.
type sink struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

// sinkOf adapts w to the op.Sink a streaming op writes to. The writer a route
// receives cannot be that sink itself: http.ResponseWriter.WriteHeader(int)
// and op.Sink.WriteHeader(int, http.Header) share a name.
func sinkOf(w http.ResponseWriter) op.Sink {
	return &sink{w: w, rc: http.NewResponseController(w)}
}

// WriteHeader merges h into the response's headers and then sends the
// status; a route that names the length puts it in h through
// SetContentLength.
func (s *sink) WriteHeader(status int, h http.Header) {
	maps.Copy(s.w.Header(), h)
	s.w.WriteHeader(status)
}

func (s *sink) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *sink) Flush() error { return s.rc.Flush() }
