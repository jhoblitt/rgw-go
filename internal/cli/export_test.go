package cli

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/memlimit"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/metrics"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// serve's pieces that need no cluster.
var (
	EnvToVec        = envToVec
	RadosgwDefaults = radosgwDefaults
	UnservedAPIs    = unservedAPIs
	ResourcePaths   = resourcePaths
	S3Config        = s3Config
	GatewayConfigs  = gatewayConfigs
	ServeMetrics    = serveMetrics
	ServeAndRun     = serveAndRun
	WatchRADOS      = watchRADOS
	WatchStore      = watchStore
	ErrNoStats      = errNoStats
	WatchSignals    = watchSignals
	RadosConfig     = radosConfig
	ServiceDaemon   = serviceDaemon
)

// NewRouter is the handler serve puts behind the frontend: s3h, nil when S3
// is off, for every path but the unserved ones, which get radosgw's answer
// to a request no manager takes.
func NewRouter(s3h http.Handler, env *op.Env, cfg s3.Config, paths []string) http.Handler {
	return newRouter(s3h, nil, "", newUnservedHandler(env, cfg), cfg, paths)
}

// testGatewayStore is a gatewayStore over a zone fake and a request cap.
type testGatewayStore struct {
	op.ZoneInfo
	maxConcurrent int64
}

func (s testGatewayStore) MaxConcurrentRequests() int64 { return s.maxConcurrent }

// GatewayStoreForTest is what S3Config takes from a store: zi's zone and
// the request cap maxConcurrent.
func GatewayStoreForTest(zi op.ZoneInfo, maxConcurrent int64) gatewayStore {
	return testGatewayStore{ZoneInfo: zi, maxConcurrent: maxConcurrent}
}

// NewGatewayForTest is the gateway NewGateway builds, over env, a and cfg
// rather than a store's.
func NewGatewayForTest(env *op.Env, a s3.Authenticator, cfg s3.Config) *Gateway {
	return newGateway(env, a, cfg)
}

// SetMemorySizer makes serve size the Go memory limit with s until the
// returned restore runs.
func SetMemorySizer(s memlimit.Sizer) (restore func()) {
	old := processSizer
	processSizer = func() memlimit.Sizer { return s }
	return func() { processSizer = old }
}

// NewAdminRouter is NewRouter with adminH at adminPath.
func NewAdminRouter(s3h, adminH http.Handler, adminPath string, env *op.Env, cfg s3.Config, paths []string) http.Handler {
	return newRouter(s3h, adminH, adminPath, newUnservedHandler(env, cfg), cfg, paths)
}

// ServiceIdentity is what ServiceMetadata builds radosgw's metadata from.
type ServiceIdentity struct {
	Name            string
	ServiceUniqueID string
	Zone            meta.Zone
	ZoneGroup       meta.ZoneGroup
	Realm           meta.Realm
	Frontends       string
	PID             int
}

// ServiceMetadata is the metadata serve registers for id.
func ServiceMetadata(id ServiceIdentity) map[string]string {
	return serviceMetadata(serviceIdentity{
		name: id.Name, serviceUniqueID: id.ServiceUniqueID, zone: id.Zone, zoneGroup: id.ZoneGroup, realm: id.Realm,
		frontends: id.Frontends, pid: id.PID,
	})
}

// ServiceZone is the zone, zonegroup and realm ServiceRegistration
// registers with.
type ServiceZone interface {
	Zone() meta.Zone
	ZoneGroup() meta.ZoneGroup
	Realm() meta.Realm
}

// ServiceRegistration is what serve registers when on: c as a
// ServiceRegistrar, nil when off or when c cannot register, and the
// metadata.
func ServiceRegistration(ctx context.Context, on bool, c radosclient.Cluster, conf *cephconf.Options, zi ServiceZone,
	name, frontends string,
) (radosclient.ServiceRegistrar, map[string]string, error) {
	return serviceRegistration(ctx, on, c, conf, zi, name, frontends)
}

// ServiceMap is serve's registration: Registrar nil registers nothing, and
// After stands in for time.After.
type ServiceMap struct {
	Registrar radosclient.ServiceRegistrar
	Daemon    string
	Metadata  map[string]string
	Interval  func() (int64, error)
	After     func(time.Duration) <-chan time.Time
}

func (m ServiceMap) serviceMap() serviceMap {
	return serviceMap{registrar: m.Registrar, daemon: m.Daemon, metadata: m.Metadata, interval: m.Interval, after: m.After}
}

// ReportStatus is the status loop serve runs after a registration.
func ReportStatus(ctx context.Context, m ServiceMap) error {
	return m.serviceMap().report(ctx)
}

// Frontend is what StartGateway binds and serves.
type Frontend interface {
	Listen() error
	Serve(ctx context.Context) error
	Addrs() []net.Addr
}

// StartGateway runs serve's steps once the store is open: it binds fe,
// drops privileges with drop, registers as m says, then serves fe and runs
// run until ctx ends, with no metrics listener.
func StartGateway(ctx context.Context, fe Frontend, drop func() error, run func(context.Context) error, m ServiceMap) error {
	return startup{frontend: fe, registry: metrics.New(), dropPrivileges: drop, serviceMap: m.serviceMap(), run: run}.start(ctx)
}
