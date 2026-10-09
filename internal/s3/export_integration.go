//go:build integration

package s3

import "github.com/jhoblitt/rgw-go/internal/op"

// RequestAttrsForIntegration is the attrs a PutObject of r stores, through
// requestAttrs with the generic attrs r's configuration names, for
// test/integration's specs, which compare them with what radosgw stores for
// the same request.
func RequestAttrsForIntegration(r *op.Request, query bool) (map[string][]byte, error) {
	return requestAttrs(r, genericAttrsFor(r.Env.Conf), query)
}
