package cli_test

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cli"
	"github.com/jhoblitt/rgw-go/internal/frontend"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
)

var (
	smZone      = meta.Zone{ID: "6e6b5c86-0f6c-4a1b-9e0c-2f4b6f1c9a01", Name: "ceph-objectstore"}
	smZoneGroup = meta.ZoneGroup{ID: "0b1c2d3e-4f50-4617-8293-a4b5c6d7e8f9", Name: "ceph-objectstore"}
	smRealm     = meta.Realm{ID: "9a8b7c6d-5e4f-4321-8fed-cba987654321", Name: "ceph-objectstore"}
)

// emptySync is the status of a gateway running no sync.
var emptySync = map[string]string{"current_sync": "[]"}

// frontendKeys are md's frontend_type#N and frontend_config#N keys.
func frontendKeys(md map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range md {
		if strings.HasPrefix(k, "frontend_") {
			out[k] = v
		}
	}
	return out
}

// captureLogs makes the default logger write JSON at debug level to the
// returned buffer until the spec ends.
func captureLogs() *gbytes.Buffer {
	old := slog.Default()
	DeferCleanup(func() { slog.SetDefault(old) })
	buf := gbytes.NewBuffer()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// registrarCluster is a cluster that can register in the service map.
type registrarCluster struct {
	*radosclientfakes.FakeCluster
	*radosclientfakes.FakeServiceRegistrar
}

func newRegistrarCluster() registrarCluster {
	return registrarCluster{new(radosclientfakes.FakeCluster), new(radosclientfakes.FakeServiceRegistrar)}
}

var _ = Describe("the service map metadata", func() {
	It("is the frontends' keys and pid from init_frontends, then register_to_service_map's", func() {
		Expect(cli.ServiceMetadata(cli.ServiceIdentity{
			Name: "client.rgw.rgw-go", ServiceUniqueID: "rgw-go-a", Zone: smZone, ZoneGroup: smZoneGroup, Realm: smRealm,
			Frontends: "beast endpoint=127.0.0.1:7481", PID: 4242,
		})).To(Equal(map[string]string{
			"frontend_type#0":   "beast",
			"frontend_config#0": "beast endpoint=127.0.0.1:7481",
			"pid":               "4242",
			"num_handles":       "1",
			"zonegroup_id":      smZoneGroup.ID,
			"zonegroup_name":    smZoneGroup.Name,
			"zone_name":         smZone.Name,
			"zone_id":           smZone.ID,
			"realm_name":        smRealm.Name,
			"realm_id":          smRealm.ID,
			"id":                "rgw-go",
			"service_unique_id": "rgw-go-a",
		}))
	})

	DescribeTable("takes the id from the entity name past its type, a leading rgw. removed",
		func(name, want string) {
			Expect(cli.ServiceMetadata(cli.ServiceIdentity{Name: name})).To(HaveKeyWithValue("id", want))
		},
		Entry("the harness's rgw-go", "client.rgw.rgw-go", "rgw-go"),
		Entry("Rook's gateway, whose id holds dots", "client.rgw.my.store.a", "my.store.a"),
		Entry("an id with no rgw. to remove", "client.admin", "admin"),
		Entry("an id starting rgwx., which is not rgw.", "client.rgwx.a", "rgwx.a"),
	)

	It("leaves service_unique_id out when it is empty, and gives a zone without a realm empty realm keys", func() {
		md := cli.ServiceMetadata(cli.ServiceIdentity{Name: "client.admin", Zone: smZone, ZoneGroup: smZoneGroup})
		Expect(md).NotTo(HaveKey("service_unique_id"))
		Expect(md).To(HaveKeyWithValue("realm_name", ""))
		Expect(md).To(HaveKeyWithValue("realm_id", ""))
	})

	It("carries the service_unique_id Rook puts on radosgw's command line, as librados parses it for serve", func(ctx SpecContext) {
		unsetenv("CEPH_ARGS")
		unsetenv("CEPH_CONF")
		// Rook v1.21.1's radosgw arguments, as internal/cephconf/early_test.go
		// holds them, read with no config file.
		early, err := cephconf.ParseEarly([]string{
			"--fsid=b3f3b4a4-9d6e-4a36-8f30-1f6b0c8e2d11",
			"--keyring=/etc/ceph/keyring-store/keyring",
			"--default-log-to-stderr=true",
			"--default-err-to-stderr=true",
			"--default-mon-cluster-log-to-stderr=true",
			"--default-log-stderr-prefix=debug ",
			"--default-log-to-file=false",
			"--default-mon-cluster-log-to-file=false",
			"--mon-host=[v2:10.96.0.10:3300,v1:10.96.0.10:6789]",
			"--mon-initial-members=a",
			"--id=rgw.ceph.objectstore.a",
			"--setuser=ceph",
			"--setgroup=ceph",
			"--foreground",
			"--rgw-frontends=beast port=8080",
			"--rgw-mime-types-file=/etc/ceph/rgw/mime.types",
			"--rgw-realm=ceph-objectstore",
			"--rgw-zonegroup=ceph-objectstore",
			"--rgw-zone=ceph-objectstore",
			"--service-unique-id=rook-ceph-rgw-ceph-objectstore-a-6d8f9c7b5-x2k4p",
			"--no-config-file",
		})
		Expect(err).NotTo(HaveOccurred())
		opts, err := goceph.ConfiguredOptions(ctx, cli.RadosConfig(early, goceph.ModeSync), "service_unique_id")
		Expect(err).NotTo(HaveOccurred())
		md := cli.ServiceMetadata(cli.ServiceIdentity{Name: early.Name, ServiceUniqueID: opts["service_unique_id"]})
		Expect(md).To(HaveKeyWithValue("service_unique_id", "rook-ceph-rgw-ceph-objectstore-a-6d8f9c7b5-x2k4p"))
		Expect(md).To(HaveKeyWithValue("id", "ceph.objectstore.a"))
	})

	DescribeTable("numbers rgw_frontends' entries as radosgw walks them, sorted by framework",
		func(value string, want map[string]string) {
			Expect(frontendKeys(cli.ServiceMetadata(cli.ServiceIdentity{Name: "client.admin", Frontends: value}))).To(Equal(want))
		},
		Entry("one entry", "beast port=8080",
			map[string]string{"frontend_type#0": "beast", "frontend_config#0": "beast port=8080"}),
		Entry("none, which is beast", "",
			map[string]string{"frontend_type#0": "beast", "frontend_config#0": "beast"}),
		Entry("two, given out of order", "loadgen,beast port=80", map[string]string{
			"frontend_type#0": "beast", "frontend_config#0": "beast port=80",
			"frontend_type#1": "loadgen", "frontend_config#1": "loadgen",
		}),
		Entry("an entry after a comma and a space, which keeps the space", "beast port=80, loadgen", map[string]string{
			"frontend_type#0": "beast", "frontend_config#0": "beast port=80",
			"frontend_type#1": "loadgen", "frontend_config#1": " loadgen",
		}),
		Entry("an entry of one space, whose framework is empty and sorts first", "beast port=80, ", map[string]string{
			"frontend_type#0": "", "frontend_config#0": " ",
			"frontend_type#1": "beast", "frontend_config#1": "beast port=80",
		}),
		Entry("arrow_flight, which takes its number and writes no keys", "beast port=80,arrow_flight,loadgen", map[string]string{
			"frontend_type#1": "beast", "frontend_config#1": "beast port=80",
			"frontend_type#2": "loadgen", "frontend_config#2": "loadgen",
		}),
		Entry("two of one framework, which keep the order given", "beast port=81,beast port=80", map[string]string{
			"frontend_type#0": "beast", "frontend_config#0": "beast port=81",
			"frontend_type#1": "beast", "frontend_config#1": "beast port=80",
		}),
		Entry("empty pieces around one entry, which are dropped", ",beast port=80,,",
			map[string]string{"frontend_type#0": "beast", "frontend_config#0": "beast port=80"}),
	)
})

var _ = Describe("the service map registration serve makes", func() {
	var (
		conf *cephconf.Options
		zi   *opfakes.FakeZoneInfo
	)

	BeforeEach(func() {
		conf = cephconf.NewOptions(cephconf.MapGetter{"service_unique_id": "rgw-go-a"})
		zi = new(opfakes.FakeZoneInfo)
		zi.ZoneReturns(smZone)
		zi.ZoneGroupReturns(smZoneGroup)
		zi.RealmReturns(smRealm)
	})

	It("is the RADOS client's instance id in decimal, as radosgw's daemon name is", func() {
		c := new(radosclientfakes.FakeCluster)
		c.InstanceIDReturns(4721)
		Expect(cli.ServiceDaemon(c)).To(Equal("4721"))
	})

	It("registers through the cluster, with the metadata of the process, its zone and its entity", func(ctx SpecContext) {
		c := newRegistrarCluster()
		r, md, err := cli.ServiceRegistration(ctx, true, c, conf, zi, "client.rgw.rgw-go", "beast port=8080")
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(Equal(c))
		Expect(md).To(Equal(cli.ServiceMetadata(cli.ServiceIdentity{
			Name: "client.rgw.rgw-go", ServiceUniqueID: "rgw-go-a", Zone: smZone, ZoneGroup: smZoneGroup, Realm: smRealm,
			Frontends: "beast port=8080", PID: os.Getpid(),
		})))
	})

	It("registers nothing when --service-map is false", func(ctx SpecContext) {
		r, md, err := cli.ServiceRegistration(ctx, false, newRegistrarCluster(), conf, zi, "client.rgw.rgw-go", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(BeNil())
		Expect(md).To(BeNil())
	})

	It("registers nothing, saying so, through a cluster that cannot register", func(ctx SpecContext) {
		logs := captureLogs()
		r, _, err := cli.ServiceRegistration(ctx, true, new(radosclientfakes.FakeCluster), conf, zi, "client.admin", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(r).To(BeNil())
		Expect(logs).To(gbytes.Say(`"level":"WARN","msg":"the rados client cannot register in the manager's service map"`))
	})
})

// steps records the steps of serve's startup in the order they run.
type steps struct {
	mu   sync.Mutex
	list []string
}

func (s *steps) add(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, step)
}

func (s *steps) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.list)
}

// recordingFrontend is a real frontend whose Listen and Serve record
// themselves as they are entered; Serve also closes serveEntered, so it is
// called once.
type recordingFrontend struct {
	*frontend.Server
	steps        *steps
	serveEntered chan struct{}
}

func (f recordingFrontend) Listen() error {
	f.steps.add("listen")
	return f.Server.Listen()
}

func (f recordingFrontend) Serve(ctx context.Context) error {
	f.steps.add("serve")
	close(f.serveEntered)
	return f.Server.Serve(ctx)
}

var _ = Describe("serve's startup once the store is open", func() {
	var (
		st  *steps
		fe  recordingFrontend
		reg *radosclientfakes.FakeServiceRegistrar
		md  map[string]string
	)

	BeforeEach(func() {
		st = &steps{}
		srv, err := frontend.New(frontend.Spec{Endpoints: []string{"127.0.0.1:0"}}, http.NotFoundHandler())
		Expect(err).NotTo(HaveOccurred())
		fe = recordingFrontend{Server: srv, steps: st, serveEntered: make(chan struct{})}
		reg = new(radosclientfakes.FakeServiceRegistrar)
		md = cli.ServiceMetadata(cli.ServiceIdentity{Name: "client.rgw.rgw-go", Zone: smZone, ZoneGroup: smZoneGroup})
	})

	// start runs the startup over fe and reg until the frontend serves, and
	// returns a stop that ends it and requires it to return nil.
	start := func(ctx context.Context, m cli.ServiceMap) (stop func()) {
		GinkgoHelper()
		sctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- cli.StartGateway(sctx, fe, func() error { st.add("drop privileges"); return nil },
				func(ctx context.Context) error { <-ctx.Done(); return nil }, m)
		}()
		Eventually(st.get).WithTimeout(10 * time.Second).WithPolling(10 * time.Millisecond).Should(ContainElement("serve"))
		stopped := false
		stop = func() {
			if stopped {
				return
			}
			stopped = true
			cancel()
			Eventually(done).WithTimeout(frontend.DrainTimeout).WithPolling(10 * time.Millisecond).Should(Receive(Succeed()))
		}
		DeferCleanup(stop)
		return stop
	}

	It("listens, drops privileges, registers, then serves, the frontend accepting before the registration", func(ctx SpecContext) {
		var dialErr error
		var servedDuringRegister bool
		reg.ServiceRegisterCalls(func(string, string, map[string]string) error {
			st.add("register")
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", fe.Addrs()[0].String())
			if dialErr = err; err == nil {
				dialErr = conn.Close()
			}
			// A serve started before the registration enters within this
			// wait; one started after it cannot.
			select {
			case <-fe.serveEntered:
				servedDuringRegister = true
			case <-time.After(500 * time.Millisecond):
			}
			return nil
		})
		start(ctx, cli.ServiceMap{Registrar: reg, Daemon: "4721", Metadata: md, Interval: func() (int64, error) { return 0, nil }})()
		Expect(servedDuringRegister).To(BeFalse(), "the frontend served while the registration ran")
		Expect(st.get()).To(Equal([]string{"listen", "drop privileges", "register", "serve"}))
		Expect(dialErr).NotTo(HaveOccurred(), "a TCP dial to the frontend while the registration runs")
	})

	It("registers once, as daemon 4721 of service rgw, with the metadata, and sends its status", func(ctx SpecContext) {
		start(ctx, cli.ServiceMap{Registrar: reg, Daemon: "4721", Metadata: md, Interval: func() (int64, error) { return 0, nil }})
		Expect(reg.ServiceRegisterCallCount()).To(Equal(1))
		service, daemon, got := reg.ServiceRegisterArgsForCall(0)
		Expect(service).To(Equal("rgw"))
		Expect(daemon).To(Equal("4721"))
		Expect(got).To(Equal(md))
		Eventually(reg.ServiceUpdateStatusCallCount).WithTimeout(10 * time.Second).WithPolling(10 * time.Millisecond).Should(Equal(1))
	})

	It("logs a refused registration at error level with the daemon, serves on, and sends no status", func(ctx SpecContext) {
		logs := captureLogs()
		reg.ServiceRegisterReturns(errors.New("rados: ret=-16, Device or resource busy"))
		start(ctx, cli.ServiceMap{Registrar: reg, Daemon: "4721", Metadata: md, Interval: func() (int64, error) { return 10, nil }})
		Expect(logs).To(gbytes.Say(`"level":"ERROR","msg":"registering in the manager's service map; serving without it",` +
			`"service":"rgw","daemon":"4721","error":"rados: ret=-16, Device or resource busy"`))
		Expect(fe.Addrs()).NotTo(BeEmpty())
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", fe.Addrs()[0].String())
		Expect(err).NotTo(HaveOccurred(), "the frontend serves on")
		Expect(conn.Close()).To(Succeed())
		Consistently(reg.ServiceUpdateStatusCallCount).WithTimeout(200 * time.Millisecond).WithPolling(10 * time.Millisecond).Should(BeZero())
	})

	It("registers nothing and sends nothing without a registrar, as --service-map=false leaves it", func(ctx SpecContext) {
		start(ctx, cli.ServiceMap{Daemon: "4721", Interval: func() (int64, error) { return 10, nil }})()
		Expect(st.get()).To(Equal([]string{"listen", "drop privileges", "serve"}))
	})
})

var _ = Describe("the status serve reports after registering", func() {
	var (
		reg      *radosclientfakes.FakeServiceRegistrar
		sent     chan map[string]string
		asked    chan time.Duration
		fire     chan time.Time
		interval atomic.Int64
		readErr  atomic.Pointer[error]
	)

	BeforeEach(func() {
		reg = new(radosclientfakes.FakeServiceRegistrar)
		sent = make(chan map[string]string, 10)
		reg.ServiceUpdateStatusCalls(func(status map[string]string) error {
			sent <- maps.Clone(status)
			return nil
		})
		asked = make(chan time.Duration, 10)
		fire = make(chan time.Time)
		interval.Store(10)
		readErr.Store(nil)
	})

	// report runs the loop on a clock the spec moves: each wait it asks for
	// goes on asked, and passes when the spec sends on fire. The stop it
	// returns ends it and requires it to return nil.
	report := func(ctx context.Context) (stop func()) {
		sctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- cli.ReportStatus(sctx, cli.ServiceMap{
				Registrar: reg, Daemon: "4721",
				Interval: func() (int64, error) {
					if err := readErr.Load(); err != nil {
						return 0, *err
					}
					return interval.Load(), nil
				},
				After: func(d time.Duration) <-chan time.Time {
					asked <- d
					return fire
				},
			})
		}()
		return func() {
			cancel()
			Eventually(done).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(BeNil()))
		}
	}

	const short = 100 * time.Millisecond

	It("sends the empty sync list at once and after 10 s, after 5 s once the option reads 5, none after it reads 0, and returns nil on cancel",
		func(ctx SpecContext) {
			stop := report(ctx)
			Eventually(sent).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(emptySync)))
			Eventually(asked).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(10 * time.Second)))
			Consistently(sent).WithTimeout(short).WithPolling(10 * time.Millisecond).ShouldNot(Receive())

			interval.Store(5)
			fire <- time.Time{}
			Eventually(sent).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(emptySync)))
			Eventually(asked).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(5 * time.Second)))

			interval.Store(0)
			fire <- time.Time{}
			Eventually(sent).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(emptySync)))
			Consistently(asked).WithTimeout(short).WithPolling(10 * time.Millisecond).ShouldNot(Receive())
			Consistently(sent).WithTimeout(short).WithPolling(10 * time.Millisecond).ShouldNot(Receive())
			stop()
		})

	It("logs a failed send and makes the next one", func(ctx SpecContext) {
		logs := captureLogs()
		calls := 0
		reg.ServiceUpdateStatusCalls(func(status map[string]string) error {
			calls++
			sent <- maps.Clone(status)
			if calls == 1 {
				return errors.New("rados: ret=-107, Transport endpoint is not connected")
			}
			return nil
		})
		stop := report(ctx)
		Eventually(sent).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive())
		Eventually(logs).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(gbytes.Say(
			`"level":"ERROR","msg":"updating the gateway's status in the manager's service map","daemon":"4721",` +
				`"error":"rados: ret=-107, Transport endpoint is not connected"`))
		Eventually(asked).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(10 * time.Second)))
		fire <- time.Time{}
		Eventually(sent).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(emptySync)))
		stop()
	})

	It("logs an interval it cannot read, sends no more and returns nil on cancel", func(ctx SpecContext) {
		logs := captureLogs()
		err := errors.New("rados: ret=-108, Cannot send after transport endpoint shutdown")
		readErr.Store(&err)
		stop := report(ctx)
		Eventually(sent).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive(Equal(emptySync)))
		Eventually(logs).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(gbytes.Say(
			`"level":"ERROR","msg":"reading rgw_sync_trace_servicemap_update_interval; sending no more status"`))
		Consistently(asked).WithTimeout(short).WithPolling(10 * time.Millisecond).ShouldNot(Receive())
		stop()
	})
})

var _ = Describe("the --service-map setting", func() {
	var missing string

	BeforeEach(func() {
		for _, key := range []string{"CEPH_ARGS", "CEPH_CONF", "RGW_GO_SERVICE_MAP"} {
			unsetenv(key)
		}
		missing = filepath.Join(GinkgoT().TempDir(), "missing.conf")
	})

	DescribeTable("is on by default, and --service-map wins over RGW_GO_SERVICE_MAP",
		func(ctx SpecContext, env string, flags []string, want bool) {
			if env != "" {
				GinkgoT().Setenv("RGW_GO_SERVICE_MAP", env)
			}
			_, logs, err := run(ctx, slices.Concat([]string{"--log-level=debug", "serve"}, flags, []string{"--", "-c", missing, "-v"})...)
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).To(ContainSubstring(`"service_map":"` + strconv.FormatBool(want) + `"`))
		},
		Entry("on with neither", "", nil, true),
		Entry("off with --service-map=false", "", []string{"--service-map=false"}, false),
		Entry("off with RGW_GO_SERVICE_MAP=false, as when invoked as radosgw", "false", nil, false),
		Entry("on with --service-map=true over RGW_GO_SERVICE_MAP=false", "false", []string{"--service-map=true"}, true),
	)

	It("refuses an RGW_GO_SERVICE_MAP that is not true or false before reading any configuration", func(ctx SpecContext) {
		GinkgoT().Setenv("RGW_GO_SERVICE_MAP", "maybe")
		_, _, err := run(ctx, "serve", "--", "-c", missing, "--id=x")
		Expect(err).To(MatchError(`--service-map "maybe" is not true or false`))
	})

	It("is in radosgw's usage among rgw-go's own settings", func(ctx SpecContext) {
		out, _, err := run(ctx, "serve", "--", "-c", missing, "-h")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(ContainSubstring("  RGW_GO_SERVICE_MAP         true or false: register in the manager's service map\n"))
	})
})
