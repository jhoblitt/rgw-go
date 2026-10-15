package s3

import (
	"slices"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var (
	SinkOfForTest              = sinkOf
	WriteChunkedXMLForTest     = writeChunkedXML
	DeleteResultElementForTest = deleteResultElement
	ParseCopySourceForTest     = parseCopySource
	ParseDeleteForTest         = parseDelete
	FormatXattrForTest         = formatXattr
)

// RequestAttrsForTest is requestAttrs with the generic attrs r's
// configuration names.
func RequestAttrsForTest(r *op.Request, query bool) (map[string][]byte, error) {
	return requestAttrs(r, genericAttrsFor(r.Env.Conf), query)
}

// RouteSetForTest is a routeSet a spec builds.
type RouteSetForTest struct {
	Scope    op.Scope
	Handlers map[string]HandlerFunc
}

// UnitHandlersForTest is the four unit maps NewHandler binds, by the scope
// each map's entries bind to, and the feature route sets.
type UnitHandlersForTest struct {
	Service, Bucket, Object, Multipart map[string]HandlerFunc
	Sets                               []RouteSetForTest
}

// NewHandlerWithForTest is NewHandler over u in place of the units' maps and
// the features' route sets.
func NewHandlerWithForTest(env *op.Env, auth Authenticator, cfg Config, u UnitHandlersForTest) *Handler {
	sets := make([]routeSet, 0, len(u.Sets))
	for _, s := range u.Sets {
		sets = append(sets, routeSet{scope: s.Scope, handlers: s.Handlers})
	}
	return buildHandler(env, auth, cfg, unitHandlers{service: u.Service, bucket: u.Bucket, object: u.Object, multipart: u.Multipart, sets: sets})
}

// BoundRoutesForTest is every route h has a handler for, as scope/name,
// sorted.
func BoundRoutesForTest(h *Handler) []string {
	out := make([]string, 0, len(h.routes))
	for k := range h.routes {
		out = append(out, scopeNames[k.scope]+"/"+k.name)
	}
	slices.Sort(out)
	return out
}
