package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/pprof" //nolint:gosec // its handlers are registered on the --metrics-addr listener's own mux; nothing serves http.DefaultServeMux
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/frontend"
	"github.com/jhoblitt/rgw-go/internal/metrics"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/version"
)

func newServeCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve [flags] -- [radosgw options...]",
		Short: "Run the gateway with radosgw's command line",
		Long: "serve runs the gateway. Everything after -- is radosgw's command line, which\n" +
			"rgw-go also takes whole when it is invoked as radosgw.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return serve(cmd.Context(), v, cmd.OutOrStdout(), args)
		},
	}
	f := cmd.Flags()
	f.String("metrics-addr", "", "HOST:PORT serving /metrics and /debug/pprof/, empty for none (RGW_GO_METRICS_ADDR)")
	f.String("rados-completions", string(goceph.ModeCallback),
		"how a RADOS completion wakes its request: sync, callback, or pipe (RGW_GO_RADOS_COMPLETIONS)")
	return cmd
}

// serveSettings are rgw-go's own serve flags.
type serveSettings struct {
	metricsAddr string
	mode        goceph.Mode
}

func (s serveSettings) validate() error {
	switch s.mode {
	case goceph.ModeSync, goceph.ModeCallback, goceph.ModePipe:
		return nil
	}
	return fmt.Errorf("--rados-completions %q is not sync, callback or pipe", s.mode)
}

// errNoArguments is radosgw's refusal of an empty command line, on which it
// exits 1 (rgw_main.cc:90-93 at v19.2.6 and v20.2.4).
var errNoArguments = errors.New("-h or --help for usage")

// serve is radosgw's main: the command-line checks rgw_main.cc makes itself
// (:90-97 at v19.2.6 and v20.2.4), then global_pre_init's early arguments,
// CEPH_ARGS's included (global_init.cc:110-114), which may print the version
// and exit, then the gateway.
func serve(ctx context.Context, v *viper.Viper, stdout io.Writer, args []string) error {
	s := serveSettings{metricsAddr: v.GetString("metrics-addr"), mode: goceph.Mode(v.GetString("rados-completions"))}
	slog.DebugContext(ctx, "serve settings",
		slog.String("metrics_addr", s.metricsAddr), slog.String("rados_completions", string(s.mode)))
	if len(args) == 0 {
		return errNoArguments
	}
	if needUsage(args) {
		fmt.Fprint(stdout, usage)
		return nil
	}
	if env, ok := os.LookupEnv("CEPH_ARGS"); ok {
		args = envToVec(env, args)
	}
	early, err := cephconf.ParseEarly(args)
	if err != nil {
		return err
	}
	if early.Version {
		fmt.Fprintln(stdout, version.String())
		return nil
	}
	if err := s.validate(); err != nil {
		return err
	}
	return s.run(ctx, early)
}

// needUsage is ceph_argparse_need_usage, which radosgw asks of its command
// line alone, before CEPH_ARGS joins it: -h or --help anywhere, past a "--"
// too (ceph_argparse.cc:575-587 at v19.2.6, :579-591 at v20.2.4).
func needUsage(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return a == "-h" || a == "--help" })
}

// usage is radosgw's usage (rgw_main.cc:42-56 at v19.2.6 and v20.2.4, with
// generic_usage, ceph_argparse.cc:550-573 at v19.2.6, :554-577 at v20.2.4),
// then rgw-go's own settings.
const usage = `usage: radosgw [options...]
options:
  --rgw-region=<region>     region in which radosgw runs
  --rgw-zone=<zone>         zone in which radosgw runs
  --rgw-socket-path=<path>  specify a unix domain socket path
  -m monaddress[:port]      connect to specified monitor
  --keyring=<path>          path to radosgw keyring
  --logfile=<logfile>       file to log debug output
  --debug-rgw=<log-level>/<memory-level>  set radosgw debug level
  --conf/-c FILE    read configuration from the given configuration file
  --id/-i ID        set ID portion of my name
  --name/-n TYPE.ID set name
  --cluster NAME    set cluster name (default: ceph)
  --setuser USER    set uid to user or uid (and gid to user's gid)
  --setgroup GROUP  set gid to group or gid
  --version         show version and quit

  -d                run in foreground, log to stderr
  -f                run in foreground, log to usual location

  --debug_ms N      set message debug level (e.g. 1)

rgw-go logs to stderr and takes its own settings from the environment:
  RGW_GO_LOG_LEVEL          debug, info, warn or error (default info)
  RGW_GO_LOG_FORMAT         json or text (default json)
  RGW_GO_METRICS_ADDR       HOST:PORT serving /metrics and /debug/pprof/ (default none)
  RGW_GO_RADOS_COMPLETIONS  sync, callback or pipe (default callback)
`

// envToVec is ceph's env_to_vec for a set CEPH_ARGS (ceph_argparse.cc:91-127
// at v19.2.6, :95-131 at v20.2.4): env split on spaces, empty words dropped,
// then env's options, args's options, and, when either names any, a "--"
// followed by env's arguments and args's, each list split at its first
// "--".
func envToVec(env string, args []string) []string {
	var words []string
	for w := range strings.SplitSeq(env, " ") {
		if w != "" {
			words = append(words, w)
		}
	}
	envOpts, envArgs := splitDashDash(words)
	opts, rest := splitDashDash(args)
	out := slices.Concat(envOpts, opts)
	if len(envArgs) == 0 && len(rest) == 0 {
		return out
	}
	return slices.Concat(out, []string{"--"}, envArgs, rest)
}

// splitDashDash is ceph's split_dashdash: what precedes the first "--", and
// what follows it.
func splitDashDash(args []string) (opts, rest []string) {
	i := slices.Index(args, "--")
	if i < 0 {
		return args, nil
	}
	return args[:i], args[i+1:]
}

// radosgwDefaults are the defaults radosgw gives its own process before it
// reads any configuration (rgw_main.cc:79-87 at v19.2.6 and v20.2.4), as
// librados arguments that set the same default level (config.cc:780-791 at
// v19.2.6, :781-792 at v20.2.4), so the config file, the mon config store,
// CEPH_ARGS and the command line each still override them.
var radosgwDefaults = []string{
	"--default-debug_rgw=1/5",
	"--default-keyring=$rgw_data/keyring",
	"--default-objecter_inflight_ops=24576",
	"--default-ms_mon_client_mode=secure",
	"--default-auth_client_required=cephx",
}

// privilegeOptions are what global_init resolves the ids it drops to from,
// before it fetches the mon config.
var privilegeOptions = []string{"setuser", "setgroup", "setuser_match_path"}

// run starts the gateway in radosgw's order: connect to RADOS as the
// launching user, open the store, bind every listener, then drop
// privileges and serve until ctx ends.
func (s serveSettings) run(ctx context.Context, early cephconf.EarlyArgs) (err error) {
	cfg := goceph.Config{
		Cluster:      early.Cluster,
		Name:         early.Name,
		ConfigFile:   early.ConfFile,
		NoConfigFile: early.NoConfigFile,
		Args:         slices.Concat(radosgwDefaults, early.Rest),
		Mode:         s.mode,
	}
	ids, err := goceph.ConfiguredOptions(ctx, cfg, privilegeOptions...)
	if err != nil {
		return fmt.Errorf("reading the ceph configuration: %w", err)
	}
	cluster, err := goceph.Connect(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connecting to rados: %w", err)
	}
	defer func() { err = errors.Join(err, cluster.Close()) }()

	conf := cephconf.NewOptions(cluster)
	apis, err := cephconf.EnabledAPIs(conf)
	if err != nil {
		return fmt.Errorf("reading rgw_enable_apis: %w", err)
	}
	if len(apis.Ignored) > 0 {
		slog.InfoContext(ctx, "ignoring apis", slog.Any("apis", apis.Ignored))
	}
	unserved, s3On, err := unservedAPIs(conf, apis)
	if err != nil {
		return err
	}
	if apis.S3 && !s3On {
		slog.WarnContext(ctx, "not serving s3, rgw_swift_url_prefix puts swift at the root")
	}
	frontends, err := conf.String("rgw_frontends")
	if err != nil {
		return fmt.Errorf("reading rgw_frontends: %w", err)
	}
	spec, others, err := frontend.ParseFrontends(frontends)
	if err != nil {
		return fmt.Errorf("parsing rgw_frontends: %w", err)
	}
	if len(others) > 0 {
		slog.WarnContext(ctx, "ignoring frontends", slog.Any("frontends", others))
	}

	store, err := driver.Open(ctx, cluster, conf, driver.Options{})
	if err != nil {
		return fmt.Errorf("opening the driver: %w", err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	reg := metrics.New()
	if err = watchRADOS(reg, s.mode, cluster); err != nil {
		return fmt.Errorf("watching rados metrics: %w", err)
	}
	zone, zonegroup := store.Zone(), store.ZoneGroup()
	env := store.Env()
	env.Authz = op.OwnerOnly{}
	env.Metrics = reg
	env.HostID = op.HostID(cluster.InstanceID(), zone.Name, zonegroup.Name)
	s3cfg, err := s3Config(conf, store, cluster.InstanceID())
	if err != nil {
		return err
	}
	authCfg, err := auth.ConfigFrom(conf, s3cfg.DNSNames)
	if err != nil {
		return fmt.Errorf("reading auth options: %w", err)
	}
	verifier := auth.New(authCfg, store, store)
	var s3h http.Handler
	if s3On {
		s3h = s3.NewHandler(env, verifier, s3cfg)
	}
	root := newRouter(s3h, newUnservedHandler(env, s3cfg), s3cfg, unserved)

	fe, err := frontend.New(spec, frontend.Deadlines(root, spec.RequestTimeout))
	if err != nil {
		return fmt.Errorf("configuring the frontend: %w", err)
	}
	if err = fe.Listen(); err != nil {
		return fmt.Errorf("starting the frontend: %w", err)
	}
	var metricsLn net.Listener
	if s.metricsAddr != "" {
		if metricsLn, err = (&net.ListenConfig{}).Listen(ctx, "tcp", s.metricsAddr); err != nil {
			return fmt.Errorf("listening on --metrics-addr %s: %w", s.metricsAddr, err)
		}
	}
	if err = cephconf.DropPrivileges(cephconf.NewOptions(cephconf.MapGetter(ids))); err != nil {
		return fmt.Errorf("dropping privileges: %w", err)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return fe.Serve(gctx) })
	g.Go(func() error { return store.Run(gctx) })
	if metricsLn != nil {
		serveMetrics(gctx, g, metricsLn, reg)
	}
	addrs := make([]string, 0, len(fe.Addrs()))
	for _, a := range fe.Addrs() {
		addrs = append(addrs, a.String())
	}
	var metricsAddr string
	if metricsLn != nil {
		metricsAddr = metricsLn.Addr().String()
	}
	slog.InfoContext(ctx, "serving", slog.Any("addrs", addrs), slog.String("metrics_addr", metricsAddr),
		slog.String("zone", zone.Name), slog.String("release", store.Release().String()))
	return g.Wait()
}

// errNoStats is a RADOS client without the stats the rgw_go_rados_* metrics
// report.
var errNoStats = errors.New("the rados client reports no stats")

// watchRADOS reports c's RADOS stats in reg under mode.
func watchRADOS(reg *metrics.Registry, mode goceph.Mode, c radosclient.Cluster) error {
	sr, ok := c.(radosclient.StatsReporter)
	if !ok {
		return fmt.Errorf("%T: %w", c, errNoStats)
	}
	reg.WatchRADOS(string(mode), sr)
	return nil
}

// s3Config reads the options the S3 handler takes: rgw_dns_name joined with
// the zonegroup's hostnames as rgw_rest_init joins them (rgw_rest.cc:215-223
// at v19.2.6 and v20.2.4), and the Server header end_header sends,
// rgw_service_provider_name when it is set (:637-642 at v19.2.6, :642-647
// at v20.2.4).
func s3Config(conf *cephconf.Options, zi op.ZoneInfo, instanceID uint64) (s3.Config, error) {
	dns, err := conf.List("rgw_dns_name")
	if err != nil {
		return s3.Config{}, fmt.Errorf("reading rgw_dns_name: %w", err)
	}
	maxConcurrent, err := conf.Int64("rgw_max_concurrent_requests")
	if err != nil {
		return s3.Config{}, fmt.Errorf("reading rgw_max_concurrent_requests: %w", err)
	}
	server, err := conf.String("rgw_service_provider_name")
	if err != nil {
		return s3.Config{}, fmt.Errorf("reading rgw_service_provider_name: %w", err)
	}
	if server == "" {
		server = "Ceph Object Gateway (" + zi.Release().String() + ")"
	}
	return s3.Config{
		DNSNames:      append(dns, zi.ZoneGroup().Hostnames...),
		MaxConcurrent: int(maxConcurrent),
		TransIDSuffix: op.TransIDSuffix(instanceID, zi.Zone().Name),
		ServerHeader:  server,
	}, nil
}

// unservedAPIs lists the paths of the REST managers radosgw registers for an
// enabled API rgw-go does not serve: Swift at rgw_swift_url_prefix, Swift
// auth at rgw_swift_auth_entry, the zero API, and the admin API at
// rgw_admin_entry (rgw_appmain.cc:307-366 at v19.2.6, :316-375 at v20.2.4).
// s3On reports whether S3 is the default manager: radosgw leaves it out when
// rgw_swift_url_prefix is "/", which puts Swift at the root.
func unservedAPIs(conf *cephconf.Options, apis cephconf.APIs) (paths []string, s3On bool, err error) {
	var swift, swiftAuth, admin string
	for _, o := range []struct {
		name string
		v    *string
	}{{"rgw_swift_url_prefix", &swift}, {"rgw_swift_auth_entry", &swiftAuth}, {"rgw_admin_entry", &admin}} {
		if *o.v, err = conf.String(o.name); err != nil {
			return nil, false, fmt.Errorf("reading %s: %w", o.name, err)
		}
	}
	swiftAtRoot := swift == "/"
	var entries []string
	if slices.Contains(apis.Ignored, "swift") && !swiftAtRoot {
		entries = append(entries, swift)
	}
	if slices.Contains(apis.Ignored, "swift_auth") {
		entries = append(entries, swiftAuth)
	}
	if apis.Admin {
		entries = append(entries, admin)
	}
	if slices.Contains(apis.Ignored, "zero") {
		entries = append(entries, "zero")
	}
	for _, e := range entries {
		paths = append(paths, resourcePaths(e)...)
	}
	return paths, apis.S3 && !swiftAtRoot, nil
}

// resourcePaths are the paths RGWRESTMgr::register_resource puts a manager
// at for entry: r = "/"+entry, and r up to each "/" in it past the first and
// before the last byte, where it adds a manager with no handler
// (rgw_rest.cc:1935-1966 at v19.2.6, :1952-1983 at v20.2.4).
func resourcePaths(entry string) []string {
	r := "/" + entry
	paths := []string{r}
	for i := 1; ; i++ {
		j := strings.IndexByte(r[i:], '/')
		if j < 0 || i+j == len(r)-1 {
			return paths
		}
		i += j
		paths = append(paths, r[:i])
	}
}

// router is RGWREST::get_handler's manager lookup (rgw_rest.cc:2276-2304 at
// v19.2.6, :2298-2326 at v20.2.4): a request whose decoded URI, the path
// with a virtual-hosted bucket in front, is at or under one of the unserved
// paths, as RGWRESTMgr::get_resource_mgr matches them (:1974-1997 at
// v19.2.6, :1991-2014 at v20.2.4), or any request when S3 is off, goes to
// unserved, and every other one to S3, the default manager.
type router struct {
	s3, unserved http.Handler
	cfg          s3.Config
	paths        []string
}

func newRouter(s3h, unserved http.Handler, cfg s3.Config, paths []string) http.Handler {
	return router{s3: s3h, unserved: unserved, cfg: cfg, paths: paths}
}

func (rt router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if rt.s3 == nil || rt.unservedPath(s3.DecodedURI(req, rt.cfg)) {
		rt.unserved.ServeHTTP(w, req)
		return
	}
	rt.s3.ServeHTTP(w, req)
}

func (rt router) unservedPath(path string) bool {
	for _, p := range rt.paths {
		if rest, ok := strings.CutPrefix(path, p); ok && (rest == "" || rest[0] == '/') {
			return true
		}
	}
	return false
}

// unservedHandler answers a request no manager rgw-go serves takes as
// radosgw answers one it finds no handler for: 405 MethodNotAllowed, or 400
// InvalidRequest when the decoded URI the router matched holds a NUL, which
// preprocess refuses before the lookup (rgw_rest.cc:2182-2186 at v19.2.6,
// :2204-2208 at v20.2.4;
// abort_early from process_request, rgw_process.cc:316-318 at v19.2.6,
// :318-320 at v20.2.4). process_request logs no usage for such a request,
// since it sets should_log only past that point (:322 at v19.2.6, :324 at
// v20.2.4), and draws each request's id at random
// (StoreDriver::get_new_req_id, rgw_sal_store.h:27-29 at v19.2.6, :101-103
// at v20.2.4).
type unservedHandler struct {
	env     *op.Env
	metrics op.Metrics
	cfg     s3.Config
}

func newUnservedHandler(env *op.Env, cfg s3.Config) http.Handler {
	h := unservedHandler{env: env, metrics: env.Metrics, cfg: cfg}
	if h.metrics == nil {
		h.metrics = op.NopMetrics{}
	}
	return h
}

func (h unservedHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	h.metrics.InFlight(1)
	defer h.metrics.InFlight(-1)
	now := h.env.Clock()
	r := &op.Request{ID: op.TransID(rand.Uint64(), now, h.cfg.TransIDSuffix), Env: h.env, Time: now, Method: req.Method} //nolint:gosec // a request id must be unique, not unpredictable
	if h.cfg.ServerHeader != "" {
		w.Header().Set("Server", h.cfg.ServerHeader)
	}
	refusal := op.ErrMethodNotAllowed
	if strings.IndexByte(s3.DecodedURI(req, h.cfg), 0) >= 0 {
		refusal = op.ErrInvalidRequest
	}
	cw := &countingWriter{ResponseWriter: w, head: req.Method == http.MethodHead}
	s3.WriteError(req.Context(), cw, r, refusal)
	h.metrics.Observe("unknown", refusal.Status, time.Since(start), 0, cw.n)
}

// countingWriter counts the body bytes written, none for a HEAD, whose body
// net/http discards.
type countingWriter struct {
	http.ResponseWriter
	head bool
	n    int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if !w.head {
		w.n += int64(n)
	}
	return n, err
}

// serveMetrics serves reg at /metrics and net/http/pprof at /debug/pprof/ on
// ln until ctx ends, then drains for up to the frontend's DrainTimeout.
func serveMetrics(ctx context.Context, g *errgroup.Group, ln net.Listener, reg *metrics.Registry) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// No write timeout: a CPU profile or a trace streams for as long as its
	// seconds parameter asks.
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	g.Go(func() error {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serving metrics on %s: %w", ln.Addr(), err)
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), frontend.DrainTimeout)
		defer cancel()
		if err := srv.Shutdown(drainCtx); err != nil {
			slog.WarnContext(ctx, "metrics listener closed with requests running", slog.Any("error", err))
		}
		return srv.Close()
	})
}
