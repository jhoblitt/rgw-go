package cli_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cli"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/metrics"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = DescribeTable("EnvToVec joins CEPH_ARGS to the command line as env_to_vec does",
	func(env string, args, want []string) {
		Expect(cli.EnvToVec(env, args)).To(Equal(want))
	},
	Entry("CEPH_ARGS's words first", "--id=a --keyring=k", []string{"--foreground"}, []string{"--id=a", "--keyring=k", "--foreground"}),
	Entry("split on spaces alone, empty words dropped", "  a\tb  c ", []string{"d"}, []string{"a\tb", "c", "d"}),
	Entry("each list's arguments after a single --", "x -- y", []string{"a", "--", "b"}, []string{"x", "a", "--", "y", "b"}),
	Entry("a -- for CEPH_ARGS's arguments alone", "-- y", []string{"a"}, []string{"a", "--", "y"}),
	Entry("no -- when neither side has arguments", "", []string{"a", "--"}, []string{"a"}),
)

var _ = DescribeTable("ResourcePaths are where register_resource puts a manager",
	func(entry string, want []string) {
		Expect(cli.ResourcePaths(entry)).To(Equal(want))
	},
	Entry("one segment", "admin", []string{"/admin"}),
	Entry("a nested entry adds its parent", "api/swift", []string{"/api/swift", "/api"}),
	Entry("a trailing slash adds nothing for itself", "a/b/", []string{"/a/b/", "/a"}),
	Entry("a leading slash adds the root", "/admin", []string{"//admin", "/"}),
	Entry("an empty entry is the root", "", []string{"/"}),
)

var _ = Describe("UnservedAPIs", func() {
	options := func(apis, swiftPrefix, adminEntry string) *cephconf.Options {
		return cephconf.NewOptions(cephconf.MapGetter{
			"rgw_enable_apis":      apis,
			"rgw_swift_url_prefix": swiftPrefix,
			"rgw_swift_auth_entry": "auth",
			"rgw_admin_entry":      adminEntry,
		})
	}

	DescribeTable("lays out the managers radosgw registers and whether S3 is the default",
		func(enabled, swiftPrefix, adminEntry string, wantPaths []string, wantAdmin string, wantS3 bool) {
			conf := options(enabled, swiftPrefix, adminEntry)
			apis, err := cephconf.EnabledAPIs(conf)
			Expect(err).NotTo(HaveOccurred())
			paths, adminPath, s3On, err := cli.UnservedAPIs(conf, apis)
			Expect(err).NotTo(HaveOccurred())
			Expect(paths).To(ConsistOf(wantPaths))
			Expect(adminPath).To(Equal(wantAdmin))
			Expect(s3On).To(Equal(wantS3))
		},
		Entry("radosgw's default", "s3, s3website, swift, swift_auth, admin, sts, iam, notifications", "swift", "admin",
			[]string{"/swift", "/auth"}, "/admin", true),
		Entry("S3 alone", "s3", "swift", "admin", []string{}, "", true),
		Entry("the zero API at its fixed path", "s3, zero", "swift", "admin", []string{"/zero"}, "", true),
		Entry("no S3", "swift,admin", "swift", "admin", []string{"/swift"}, "/admin", false),
		Entry("Swift at the root, which leaves S3 off", "s3, swift, swift_auth", "/", "admin", []string{"/auth"}, "", false),
		Entry("a nested Swift prefix and its parent", "s3, swift", "api/swift", "admin", []string{"/api/swift", "/api"}, "", true),
		Entry("a nested admin entry keeps its parent, which has no handler", "s3, admin", "swift", "a/b", []string{"/a"}, "/a/b", true),
		Entry("the admin API replaces Swift registered at its path", "s3, swift, admin", "admin", "admin", []string{}, "/admin", true),
		Entry("the zero API replaces the admin API at its path", "s3, admin, zero", "swift", "zero", []string{"/zero"}, "", true),
		Entry("a nested admin entry never replaces a parent Swift holds", "s3, swift, admin", "a", "a/b", []string{"/a"}, "/a/b", true),
	)

	It("names an option it cannot read", func() {
		conf := cephconf.NewOptions(cephconf.MapGetter{"rgw_swift_url_prefix": "swift", "rgw_swift_auth_entry": "auth"})
		_, _, _, err := cli.UnservedAPIs(conf, cephconf.APIs{S3: true, Admin: true})
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
		Expect(err).To(MatchError(HavePrefix("reading rgw_admin_entry: ")))
	})
})

var _ = Describe("the router", func() {
	var (
		env     *op.Env
		metrics *opfakes.FakeMetrics
		cfg     s3.Config
		s3h     http.Handler
	)

	BeforeEach(func() {
		metrics = new(opfakes.FakeMetrics)
		env = &op.Env{
			HostID:  "4107-zone-a-zonegroup",
			Metrics: metrics,
			Now:     func() time.Time { return time.Unix(0x66f00000, 0) },
		}
		cfg = s3.Config{TransIDSuffix: op.TransIDSuffix(4107, "zone-a"), ServerHeader: "Ceph Object Gateway (squid)"}
		s3h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	})

	serve := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1/", nil)
		req.URL.Path = path
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// With a hostname configured, radosgw puts a virtual-hosted bucket in
	// front of the path before it picks an API.
	Describe("behind a hostname", func() {
		var h http.Handler

		BeforeEach(func() {
			cfg.DNSNames = []string{"s3.example.com"}
			h = cli.NewRouter(s3h, env, cfg, []string{"/admin", "/swift", "/auth", "/zero"})
		})

		serveHost := func(host, path string) int {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
			req.Host, req.URL.Path = host, path
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Code
		}

		DescribeTable("sends a virtual-hosted key under an unserved API's path to S3",
			func(path string) { Expect(serveHost("b.s3.example.com", path)).To(Equal(http.StatusTeapot)) },
			Entry("admin", "/admin/x"),
			Entry("swift", "/swift/v1/k"),
			Entry("swift auth", "/auth/x"),
			Entry("zero", "/zero/x"),
		)

		DescribeTable("answers a path-style request for an unserved API's path itself",
			func(path string) { Expect(serveHost("s3.example.com", path)).To(Equal(http.StatusMethodNotAllowed)) },
			Entry("admin", "/admin"),
			Entry("swift", "/swift"),
			Entry("swift auth", "/auth"),
			Entry("zero", "/zero"),
		)

		It("takes a Host that names no hostname as the bucket, as radosgw's fallback does", func() {
			Expect(serveHost("bucket.example.org:8080", "/admin/x")).To(Equal(http.StatusTeapot))
		})

		It("sends a virtual-hosted bucket named after an unserved API where radosgw sends it", func() {
			Expect(serveHost("admin.s3.example.com", "/")).To(Equal(http.StatusMethodNotAllowed))
		})

		It("answers 400 InvalidRequest for a NUL in a virtual-hosted request to an unserved API", func() {
			Expect(serveHost("admin.s3.example.com", "/k\x00")).To(Equal(http.StatusBadRequest))
		})

		// preprocess decodes the Host-named bucket together with the path, so
		// a %00 in the bucket is a NUL in the URI the router matched.
		It("looks for a NUL in the path it routed by, the Host-named bucket included", func() {
			h = cli.NewRouter(nil, env, cfg, nil)
			Expect(serveHost("b%00.s3.example.com", "/k")).To(Equal(http.StatusBadRequest))
		})
	})

	DescribeTable("sends a path where radosgw's manager lookup sends it",
		func(path string, want int) {
			h := cli.NewRouter(s3h, env, cfg, []string{"/admin", "/swift", "/api/swift", "/api"})
			Expect(serve(h, http.MethodGet, path).Code).To(Equal(want))
		},
		Entry("an unserved path", "/admin", http.StatusMethodNotAllowed),
		Entry("its directory", "/admin/", http.StatusMethodNotAllowed),
		Entry("a path under it", "/admin/info", http.StatusMethodNotAllowed),
		Entry("a nested entry's parent", "/api/other", http.StatusMethodNotAllowed),
		Entry("a longer first segment, to S3", "/administrator", http.StatusTeapot),
		Entry("an unserved name past the first segment, to S3", "/bucket/admin", http.StatusTeapot),
		Entry("the root, to S3", "/", http.StatusTeapot),
	)

	It("answers 405 MethodNotAllowed with the error document and headers radosgw sends", func() {
		rec := serve(cli.NewRouter(s3h, env, cfg, []string{"/swift"}), http.MethodGet, "/swift/v1")
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
		id := rec.Header().Get("x-amz-request-id")
		Expect(id).To(MatchRegexp(`^tx[0-9a-f]{21}-0066f00000-4107-zone-a$`))
		Expect(rec.Header().Get("Server")).To(Equal("Ceph Object Gateway (squid)"))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>MethodNotAllowed</Code>` +
			`<Message></Message><RequestId>` + id + `</RequestId><HostId>4107-zone-a-zonegroup</HostId></Error>`))
	})

	It("answers 400 InvalidRequest for a NUL in the path, which radosgw refuses before the lookup", func() {
		rec := serve(cli.NewRouter(s3h, env, cfg, []string{"/admin"}), http.MethodGet, "/admin/\x00")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"))
	})

	It("answers every request 405 without S3, as radosgw does with no default manager", func() {
		rec := serve(cli.NewRouter(nil, env, cfg, nil), http.MethodGet, "/")
		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
	})

	It("draws each answer's request id at random, as radosgw does", func() {
		h := cli.NewRouter(nil, env, cfg, nil)
		first := serve(h, http.MethodGet, "/").Header().Get("x-amz-request-id")
		Expect(serve(h, http.MethodGet, "/").Header().Get("x-amz-request-id")).NotTo(Equal(first))
	})

	It("observes the answer under the op radosgw's log names unknown", func() {
		rec := serve(cli.NewRouter(nil, env, cfg, nil), http.MethodGet, "/")
		Expect(metrics.InFlightCallCount()).To(Equal(2))
		Expect(metrics.InFlightArgsForCall(0)).To(Equal(1))
		Expect(metrics.InFlightArgsForCall(1)).To(Equal(-1))
		Expect(metrics.ObserveCallCount()).To(Equal(1))
		name, status, _, in, out := metrics.ObserveArgsForCall(0)
		Expect([]any{name, status, in, out}).To(Equal([]any{"unknown", http.StatusMethodNotAllowed, int64(0), int64(rec.Body.Len())}))
	})

	It("counts no body bytes for a HEAD", func() {
		serve(cli.NewRouter(nil, env, cfg, nil), http.MethodHead, "/")
		_, _, _, _, out := metrics.ObserveArgsForCall(0)
		Expect(out).To(BeZero())
	})
})

var _ = Describe("the router with the admin API", func() {
	const adminStatus = http.StatusAccepted
	var (
		env    *op.Env
		cfg    s3.Config
		s3h    http.Handler
		adminH http.Handler
	)

	BeforeEach(func() {
		env = &op.Env{HostID: "4107-zone-a-zonegroup", Metrics: op.NopMetrics{}}
		cfg = s3.Config{DNSNames: []string{"s3.example.com"}, TransIDSuffix: op.TransIDSuffix(4107, "zone-a")}
		s3h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
		adminH = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(adminStatus) })
	})

	serve := func(h http.Handler, host, path string) int {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		req.Host, req.URL.Path = host, path
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	DescribeTable("sends the admin entry and what lies under it to the admin handler, as get_resource_mgr matches it",
		func(path string, want int) {
			h := cli.NewAdminRouter(s3h, adminH, "/admin", env, cfg, []string{"/swift"})
			Expect(serve(h, "127.0.0.1", path)).To(Equal(want))
		},
		Entry("the entry", "/admin", adminStatus),
		Entry("its directory", "/admin/", adminStatus),
		Entry("a resource", "/admin/info", adminStatus),
		Entry("a resource no manager takes, which the admin handler refuses", "/admin/nosuch", adminStatus),
		Entry("a longer first segment, to S3", "/adminx", http.StatusTeapot),
		Entry("the entry past the first segment, to S3", "/plain/admin", http.StatusTeapot),
		Entry("the entry in another case, to S3", "/Admin/info", http.StatusTeapot),
		Entry("the root, to S3", "/", http.StatusTeapot),
		Entry("an unserved API beside it", "/swift/v1", http.StatusMethodNotAllowed),
	)

	It("sends a virtual-hosted request for the key admin/x to S3", func() {
		h := cli.NewAdminRouter(s3h, adminH, "/admin", env, cfg, nil)
		Expect(serve(h, "b.s3.example.com", "/admin/x")).To(Equal(http.StatusTeapot))
	})

	It("sends a Host naming the bucket admin, with the path /info, to the admin API", func() {
		h := cli.NewAdminRouter(s3h, adminH, "/admin", env, cfg, nil)
		Expect(serve(h, "admin.s3.example.com", "/info")).To(Equal(adminStatus))
	})

	It("serves a nested rgw_admin_entry and answers 405 at its parent, which has no handler", func() {
		conf := cephconf.NewOptions(cephconf.MapGetter{
			"rgw_enable_apis": "s3, admin", "rgw_swift_url_prefix": "swift",
			"rgw_swift_auth_entry": "auth", "rgw_admin_entry": "a/b",
		})
		apis, err := cephconf.EnabledAPIs(conf)
		Expect(err).NotTo(HaveOccurred())
		paths, adminPath, _, err := cli.UnservedAPIs(conf, apis)
		Expect(err).NotTo(HaveOccurred())
		h := cli.NewAdminRouter(s3h, adminH, adminPath, env, cfg, paths)
		Expect(serve(h, "127.0.0.1", "/a/x")).To(Equal(http.StatusMethodNotAllowed))
		Expect(serve(h, "127.0.0.1", "/a")).To(Equal(http.StatusMethodNotAllowed))
		Expect(serve(h, "127.0.0.1", "/a/b/info")).To(Equal(adminStatus))
		Expect(serve(h, "127.0.0.1", "/ab")).To(Equal(http.StatusTeapot))
	})

	It("serves the admin API with S3 off, and answers 405 everywhere else", func() {
		h := cli.NewAdminRouter(nil, adminH, "/admin", env, cfg, []string{"/swift"})
		Expect(serve(h, "127.0.0.1", "/admin/info")).To(Equal(adminStatus))
		Expect(serve(h, "127.0.0.1", "/")).To(Equal(http.StatusMethodNotAllowed))
		Expect(serve(h, "127.0.0.1", "/bucket/key")).To(Equal(http.StatusMethodNotAllowed))
	})

	It("lets a longer unserved path under the admin entry win, as the longest resource does", func() {
		h := cli.NewAdminRouter(s3h, adminH, "/admin", env, cfg, []string{"/admin/swift"})
		Expect(serve(h, "127.0.0.1", "/admin/swift/x")).To(Equal(http.StatusMethodNotAllowed))
		Expect(serve(h, "127.0.0.1", "/admin/info")).To(Equal(adminStatus))
	})
})

var _ = Describe("S3Config", func() {
	var (
		opts cephconf.MapGetter
		zi   *opfakes.FakeZoneInfo
	)

	BeforeEach(func() {
		opts = cephconf.MapGetter{
			"rgw_dns_name":                "s3.example.com, s3.internal",
			"rgw_max_concurrent_requests": "1024",
			"rgw_service_provider_name":   "",
		}
		zi = new(opfakes.FakeZoneInfo)
		zi.ReleaseReturns(denc.Squid)
		zi.ZoneReturns(meta.Zone{Name: "zone-a"})
		zi.ZoneGroupReturns(meta.ZoneGroup{Name: "zonegroup", Hostnames: []string{"zg.example.com"}})
	})

	It("reads the options the S3 handler takes, rgw_dns_name joined with the zonegroup's hostnames", func() {
		Expect(cli.S3Config(cephconf.NewOptions(opts), zi, 4107)).To(Equal(s3.Config{
			DNSNames:      []string{"s3.example.com", "s3.internal", "zg.example.com"},
			MaxConcurrent: 1024,
			TransIDSuffix: "-4107-zone-a",
			ServerHeader:  "Ceph Object Gateway (squid)",
		}))
	})

	It("names the cluster's release in the Server header", func() {
		zi.ReleaseReturns(denc.Tentacle)
		cfg, err := cli.S3Config(cephconf.NewOptions(opts), zi, 4107)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ServerHeader).To(Equal("Ceph Object Gateway (tentacle)"))
	})

	It("sends rgw_service_provider_name as the Server header when it is set, as end_header does", func() {
		opts["rgw_service_provider_name"] = "Example Storage"
		cfg, err := cli.S3Config(cephconf.NewOptions(opts), zi, 4107)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.ServerHeader).To(Equal("Example Storage"))
	})

	It("names an option it cannot read", func() {
		delete(opts, "rgw_service_provider_name")
		_, err := cli.S3Config(cephconf.NewOptions(opts), zi, 4107)
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
		Expect(err).To(MatchError(HavePrefix("reading rgw_service_provider_name: ")))
	})
})

var _ = Describe("the metrics listener", func() {
	It("serves the registry at /metrics and net/http/pprof at /debug/pprof/ until its context ends", func(ctx SpecContext) {
		ln, lnErr := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		Expect(lnErr).NotTo(HaveOccurred())
		reg := metrics.New()
		reg.Observe("list_buckets", http.StatusOK, time.Millisecond, 0, 10)
		sctx, cancel := context.WithCancel(ctx)
		defer cancel()
		g, gctx := errgroup.WithContext(sctx)
		cli.ServeMetrics(gctx, g, ln, reg)

		get := func(path string) (int, string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+path, nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			body, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
			return resp.StatusCode, string(body)
		}
		code, body := get("/metrics")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`rgw_go_requests_total{op="list_buckets",status="2xx"} 1`))
		code, body = get("/debug/pprof/heap?debug=1")
		Expect(code).To(Equal(http.StatusOK))
		Expect(body).To(HavePrefix("heap profile:"))
		for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
			code, _ = get(path)
			Expect(code).To(Equal(http.StatusOK), "%s", path)
		}
		code, _ = get("/")
		Expect(code).To(Equal(http.StatusNotFound), "a path the mux does not register")

		cancel()
		done := make(chan error, 1)
		go func() { done <- g.Wait() }()
		Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Succeed()))
		_, dialErr := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		Expect(dialErr).To(HaveOccurred(), "the listener is closed")
	})
})

// statsCluster is a cluster that reports RADOS stats.
type statsCluster struct {
	*radosclientfakes.FakeCluster
	stats radosclient.Stats
}

func (c statsCluster) Stats() radosclient.Stats { return c.stats }

var _ = Describe("WatchRADOS", func() {
	It("reports the cluster's RADOS stats under the completion mode", func(ctx SpecContext) {
		reg := metrics.New()
		c := statsCluster{FakeCluster: new(radosclientfakes.FakeCluster), stats: radosclient.Stats{InflightOps: 3}}
		Expect(cli.WatchRADOS(reg, goceph.ModePipe, c)).To(Succeed())
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", nil))
		Expect(rec.Body.String()).To(ContainSubstring(`rgw_go_rados_ops_in_flight{mode="pipe"} 3`))
	})

	It("fails startup for a cluster that reports no stats", func() {
		err := cli.WatchRADOS(metrics.New(), goceph.ModeCallback, new(radosclientfakes.FakeCluster))
		Expect(err).To(MatchError(cli.ErrNoStats))
		Expect(err).To(MatchError(ContainSubstring("FakeCluster")))
	})
})

var _ = Describe("radosgw's module defaults", func() {
	var conf string

	BeforeEach(func() {
		unsetenv("CEPH_ARGS")
		unsetenv("CEPH_CONF")
		conf = filepath.Join(GinkgoT().TempDir(), "ceph.conf")
		Expect(os.WriteFile(conf, []byte("[global]\nobjecter_inflight_ops = 77\n"), 0o600)).To(Succeed())
	})

	It("are defaults the config file overrides", func(ctx SpecContext) {
		cfg := goceph.Config{Name: "client.rgw.a", ConfigFile: conf, Args: cli.RadosgwDefaults}
		Expect(goceph.ConfiguredOptions(ctx, cfg,
			"objecter_inflight_ops", "keyring", "ms_mon_client_mode", "auth_client_required", "debug_rgw")).
			To(Equal(map[string]string{
				"objecter_inflight_ops": "77",
				"keyring":               "/var/lib/ceph/radosgw/ceph-rgw.a/keyring",
				"ms_mon_client_mode":    "secure",
				"auth_client_required":  "cephx",
				"debug_rgw":             "1/5",
			}))
	})

	It("are defaults the command line overrides", func(ctx SpecContext) {
		cfg := goceph.Config{NoConfigFile: true, Args: slices.Concat(cli.RadosgwDefaults, []string{"--ms_mon_client_mode=crc"})}
		Expect(goceph.ConfiguredOptions(ctx, cfg, "objecter_inflight_ops", "ms_mon_client_mode")).To(Equal(map[string]string{
			"objecter_inflight_ops": "24576",
			"ms_mon_client_mode":    "crc",
		}))
	})
})
