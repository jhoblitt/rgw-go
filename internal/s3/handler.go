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
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/vhost"
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
	// inflight is SimpleThrottler's outstanding_requests: cfg.InFlight, or a
	// count of the handler's own.
	inflight *atomic.Int64
}

// routeKey binds a handler: a route name can be routed in more than one
// scope, get_acls in the bucket's and the object's, each with its own handler.
type routeKey struct {
	scope op.Scope
	name  string
}

// unitHandlers is the units' route maps, by the scope their entries bind to,
// and the features' route sets.
type unitHandlers struct {
	service, bucket, object, multipart map[string]HandlerFunc
	sets                               []routeSet
}

var scopeNames = [...]string{op.ScopeService: "service", op.ScopeBucket: "bucket", op.ScopeObject: "object"}

// NewHandler builds the handler with every implemented route registered and
// every other route answering NotImplemented. It binds each entry of
// serviceHandlers, bucketHandlers and objectHandlers to its map's scope, each
// entry of a featureRoutes set to that set's scope, and each entry of
// multipartHandlers to the one scope Dispatch routes its name in. It panics
// on a scope Dispatch never routes, an entry whose scope Dispatch never
// routes its name to, a multipartHandlers entry routed in more than one
// scope, and a scope and name bound twice, across the maps and sets alike.
func NewHandler(env *op.Env, auth Authenticator, cfg Config) *Handler {
	return buildHandler(env, auth, cfg, unitHandlers{
		service:   serviceHandlers(),
		bucket:    bucketHandlers(),
		object:    objectHandlers(env),
		multipart: multipartHandlers(env),
		sets:      featureRoutes(env, auth),
	})
}

func buildHandler(env *op.Env, auth Authenticator, cfg Config, u unitHandlers) *Handler {
	h := &Handler{env: env, auth: auth, cfg: cfg, metrics: env.Metrics, routes: map[routeKey]HandlerFunc{}, inflight: cfg.InFlight}
	if h.metrics == nil {
		h.metrics = op.NopMetrics{}
	}
	if h.inflight == nil {
		h.inflight = new(atomic.Int64)
	}
	sets := append([]routeSet{
		{op.ScopeService, u.service},
		{op.ScopeBucket, u.bucket},
		{op.ScopeObject, u.object},
	}, u.sets...)
	for _, s := range sets {
		for name, fn := range s.handlers {
			h.bind(s.scope, name, fn)
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
	if int(scope) >= len(scopeNames) {
		panic(fmt.Sprintf("s3: route %q names scope %d, which Dispatch never routes", name, scope))
	}
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
// units bind their routes through their files' maps and the features' route
// sets, which NewHandler binds; Register replaces a route for a spec. It
// panics on a name no scope routes.
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
// those dispatched and not yet answered, by every handler sharing the count.
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
// RGWOp::name()
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L916 and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L970 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L988 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1042 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_pubsub.cc#L1197,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_pubsub.cc#L1452
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_pubsub.cc#L1556 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_pubsub.cc#L1208,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_pubsub.cc#L1464
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_pubsub.cc#L1569 at v20.2.4).
// S3 Select's op is an RGWGetObj.
var radosgwNames = map[string]string{
	"get_usage":                  "get_self_usage",
	"list_bucket_v2":             "list_bucket",
	"select_obj":                 "get_obj",
	"put_bucket_notification":    "pubsub_notification_create_s3",
	"delete_bucket_notification": "pubsub_notification_delete_s3",
	"get_bucket_notification":    "pubsub_notifications_get_s3",
}

// usageName is the RGWOp::name() of the op radosgw runs for route at rel,
// the category the usage log files the request under
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L245 and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L556 at v19.2.6
// and v20.2.4). Squid's radosgw runs ?attributes as GetObject
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4805-L4821 at v19.2.6).
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
// no op serves
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L556),
// whether postauth_init has named its bucket, and whether the client went
// away.
type served struct {
	route, usage string
	named, gone  bool
}

// ServeHTTP runs radosgw's process_request
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L298-L412 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L280-L419 at v20.2.4)
// for req. It gives the request its transaction id and sends
// x-amz-request-id and Server on every response, as end_header does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L589-L651 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L594-L656 at v20.2.4).
// Once the response is written it observes the request and logs its usage,
// refused requests included, as process_request's tail does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L454-L462 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L461-L469 at v20.2.4).
// A panic anywhere before anything was written is answered 500, observed
// and usage-logged; one after it ends the connection, so a truncated body
// is never framed as complete. A request whose authentication or route
// fails because its client went away (op.EndForGoneClient) is observed and
// usage-logged, and then its connection ends with nothing more written.
// Every line logged under the request's context carries its transaction id.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	h.metrics.InFlight(1)
	defer h.metrics.InFlight(-1)

	now := h.env.Clock()
	// radosgw draws each request's number at random (StoreDriver::get_new_req_id,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_sal_store.h#L27-L29 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_sal_store.h#L101-L103 at v20.2.4).
	id := op.TransID(rand.Uint64(), now, h.cfg.TransIDSuffix) //nolint:gosec // a request id must be unique, not unpredictable
	ctx := op.WithRequestID(req.Context(), id)
	SetHeader(w.Header(), "x-amz-request-id", id)
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
		slog.DebugContext(ctx, "request done", slog.String("op", st.route),
			slog.Int("status", r.Status), slog.Duration("elapsed", elapsed))
		if abort || st.gone {
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
		slog.ErrorContext(ctx, "request handler panicked", slog.String("op", st.route),
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

// parse is RGWREST::get_handler
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2276-L2318 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2298-L2340 at v20.2.4):
// preprocess's refusal of a NUL in the URL, RGWRESTMgr_S3::get_handler's
// refusal of an object's subresource at bucket scope, then
// RGWHandler_REST_S3::init's refusal of a copy source parse_copy_location
// cannot parse
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5026-L5041 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5586-L5601 at v20.2.4),
// whatever the method and scope. radosgw refuses a method no S3 op serves
// only later, in get_op
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L325-L329 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L331 at v20.2.4);
// the parse does not depend on the method, so a request with such a method
// is parsed as a GET to reach the earlier refusals first. A request with a
// method an S3 op serves is then refused for a NUL in an argument that
// names a RADOS object (refuseNUL), which radosgw does not check, before
// authentication reads any object.
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
	if methodErr != nil {
		return p, methodErr
	}
	return p, refuseNUL(r)
}

// serve dispatches p, caps the requests in flight, authenticates and runs
// the route, in process_request's order: get_op, schedule_request, the
// op_type set from the dispatched op, verify_requester, postauth_init, the
// suspended-user check, then the op
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L325-L405 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L412 at v20.2.4).
// It records the dispatched route in st.
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
	// answered too
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_dmclock_async_scheduler.h#L184-L207 at v19.2.6),
	// and answers -EAGAIN, which process_request sends as
	// ERR_RATE_LIMITED.
	n := h.inflight.Add(1)
	defer h.inflight.Add(-1)
	if h.cfg.MaxConcurrent > 0 && n > int64(h.cfg.MaxConcurrent) {
		refuse(ctx, w, r, op.ErrSlowDown)
		return
	}

	res, err := h.auth.Authenticate(ctx, req, route.Payloads)
	if err != nil {
		r.Bucket = ""
		if op.EndForGoneClient(ctx, r, w.wrote, route.Name, err) {
			st.gone = true
			return
		}
		refuseAuth(ctx, w, r, err)
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
		if op.EndForGoneClient(ctx, r, w.wrote, route.Name, err) {
			st.gone = true
			return
		}
		if w.wrote {
			slog.WarnContext(ctx, "route failed after its response started",
				slog.String("op", route.Name), slog.Any("error", err))
			return
		}
		setMissingDeleteMarker(ctx, w.Header(), r, err)
		setRedirectLocation(w.Header(), req, h.cfg, r, err)
		WriteError(ctx, w, r, err)
	}
}

// setRedirectLocation sends Location with the PermanentRedirect of a bucket
// that loaded, as abort_early does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L716-L727 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L721-L732 at v20.2.4):
// build_redirect_url over s->zonegroup_endpoint, which
// rgw_build_bucket_policies sets from this zonegroup once the bucket loads
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L555 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L585 at v20.2.4).
// The URL is that endpoint less one trailing slash, then request_uri, then
// '?' and request_params when there are any, both as the client sent them;
// without an endpoint none is sent.
func setRedirectLocation(h http.Header, req *http.Request, cfg Config, r *op.Request, err error) {
	if r.BucketRec == nil || !errors.Is(err, op.ErrPermanentRedirect) {
		return
	}
	base := zoneGroupEndpoint(r.Env.Zone.ZoneGroup())
	if base == "" {
		return
	}
	loc := strings.TrimSuffix(base, "/") + requestURI(req, cfg)
	if _, query := vhost.RequestTarget(req); query != "" {
		loc += "?" + query
	}
	SetHeader(h, "Location", loc)
}

// zoneGroupEndpoint is get_zonegroup_endpoint
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L1277-L1288 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L1320-L1331 at v20.2.4):
// the zonegroup's first endpoint, else its master zone's first, else "".
func zoneGroupEndpoint(zg meta.ZoneGroup) string {
	if len(zg.Endpoints) > 0 {
		return zg.Endpoints[0]
	}
	if z, ok := zg.Zones[zg.MasterZone]; ok && len(z.Endpoints) > 0 {
		return z.Endpoints[0]
	}
	return ""
}

// refuse answers a request refused before postauth_init has named its
// bucket, so neither the error document nor the usage log names one (dump,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L397-L398 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L410-L411 at v20.2.4).
func refuse(ctx context.Context, w http.ResponseWriter, r *op.Request, err error) {
	r.Bucket = ""
	WriteError(ctx, w, r, err)
}

// refuseAuth answers a failed authentication as refuse does, after
// LogAuthError.
func refuseAuth(ctx context.Context, w http.ResponseWriter, r *op.Request, err error) {
	r.Bucket = ""
	LogAuthError(ctx, err)
	writeErrorDocument(w, r, op.AsError(err))
}

// LogAuthError logs a failed authentication's server error, a store's
// failure rather than a refusal, by its code alone, as an authenticator's
// error can carry what the request's credentials hold. It logs no refusal:
// the verifier logs the failed lookup behind one itself. The admin handler
// logs its authentication failures through it too.
func LogAuthError(ctx context.Context, err error) {
	if op.AsError(err).Status >= http.StatusInternalServerError {
		slog.ErrorContext(ctx, "authentication failed", slog.String("code", op.ErrorCode(err)))
	}
}

// ApplyAuth gives r the authenticated identity and the route it
// authenticated on (op.AuthResult.Route); when the authenticator replaces
// the request body with a reader that verifies the payload as it is read,
// that reader and its length; and a SigV2 presigned request's signed query
// meta (op.AuthResult.QueryMeta) as headers of a copy of r's, with every
// query parameter it was read from gone, so that each read of the request,
// its stored attrs and sts:authentication among them, takes it as if sent as
// a header. Nothing reads the request body before this. The admin handler
// applies its AuthResult through it too.
func ApplyAuth(r *op.Request, res *op.AuthResult) {
	r.Identity = res.Identity
	r.AuthRoute = res.Route
	if res.Body != nil {
		r.Body = res.Body
		r.ContentLength = res.ContentLength
	}
	if len(res.QueryMeta) > 0 {
		h := r.Header.Clone()
		if h == nil {
			h = http.Header{}
		}
		for name, v := range res.QueryMeta {
			h.Set(name, v)
		}
		r.Header = h
		maps.DeleteFunc(r.Query, func(k string, _ []string) bool {
			_, folded := res.QueryMeta[lowerASCIIString(k)]
			return folded
		})
	}
}

// countingReader counts what the op reads into n. radosgw's accounting
// filter sits above the payload decoders
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io.h#L334-L349 at v19.2.6
// and v20.2.4) and counts a request body only inside PutObject's data
// reads
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1082-L1093 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1087-L1098 at v20.2.4).
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
// never sends (rgw_flush_formatter,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L316-L324 at v19.2.6).
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
