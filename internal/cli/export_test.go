package cli

import (
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// serve's pieces that need no cluster.
var (
	EnvToVec        = envToVec
	RadosgwDefaults = radosgwDefaults
	UnservedAPIs    = unservedAPIs
	ResourcePaths   = resourcePaths
	S3Config        = s3Config
	ServeMetrics    = serveMetrics
	WatchRADOS      = watchRADOS
	ErrNoStats      = errNoStats
	WatchSignals    = watchSignals
)

// NewRouter is the handler serve puts behind the frontend: s3h, nil when S3
// is off, for every path but the unserved ones, which get radosgw's answer
// to a request no manager takes.
func NewRouter(s3h http.Handler, env *op.Env, cfg s3.Config, paths []string) http.Handler {
	return newRouter(s3h, nil, "", newUnservedHandler(env, cfg), cfg, paths)
}

// NewAdminRouter is NewRouter with adminH at adminPath.
func NewAdminRouter(s3h, adminH http.Handler, adminPath string, env *op.Env, cfg s3.Config, paths []string) http.Handler {
	return newRouter(s3h, adminH, adminPath, newUnservedHandler(env, cfg), cfg, paths)
}
