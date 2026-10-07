package s3

import "github.com/jhoblitt/rgw-go/internal/op"

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

// UnitHandlersForTest is the four unit maps NewHandler binds, by the scope
// each map's entries bind to.
type UnitHandlersForTest struct {
	Service, Bucket, Object, Multipart map[string]HandlerFunc
}

// NewHandlerWithForTest is NewHandler over u in place of the units' maps.
func NewHandlerWithForTest(env *op.Env, auth Authenticator, cfg Config, u UnitHandlersForTest) *Handler {
	return buildHandler(env, auth, cfg, unitHandlers{service: u.Service, bucket: u.Bucket, object: u.Object, multipart: u.Multipart})
}
