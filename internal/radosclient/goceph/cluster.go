package goceph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/ceph/go-ceph/rados"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Mode selects how a completion wakes the waiting goroutine.
type Mode string

// The completion modes.
const (
	ModeSync     Mode = "sync"     // stock blocking Operate, one OS thread parked per operation
	ModeCallback Mode = "callback" // AioCompletion with the C-to-Go callback
	ModePipe     Mode = "pipe"     // AioCompletion with the pipe notifier
)

// Config is how Connect reaches a cluster.
type Config struct {
	// Cluster is the ceph cluster name. "" is "ceph" without a ConfigFile;
	// with one, librados names the cluster after the file it reads, as
	// radosgw does.
	Cluster string
	Name    string // entity name, e.g. "client.rgw.a"; default "client.admin"
	// ConfigFile is -c, "" to read the files CEPH_CONF names or else the
	// default ones.
	ConfigFile string
	// NoConfigFile is --no-config-file: skip the default files, and take
	// a ConfigFile or CEPH_CONF none of whose files can be read as no file.
	NoConfigFile     bool
	Args             []string // ceph-style argv handed to librados after the early arguments
	Mode             Mode     // default ModeCallback
	MaxInflightOps   int      // 0 derives from objecter_inflight_ops after connect
	MaxInflightBytes int64    // 0 derives from objecter_inflight_op_bytes after connect
}

func (c Config) withDefaults() Config {
	// radosgw's parse_config_files, which runs whether or not a file is
	// read, names the cluster "ceph" when no -c was given, and otherwise
	// after the first file of the list it reads, leaving it empty when it
	// reads none (config.cc:355-357 and :382-384 at v19.2.6, :356-358 and
	// :383-385 at v20.2.4). librados names a cluster it was created without
	// only when it reads a file, so the first case is set here.
	if c.Cluster == "" && c.ConfigFile == "" {
		c.Cluster = "ceph"
	}
	if c.Name == "" {
		c.Name = "client.admin"
	}
	if c.Mode == "" {
		c.Mode = ModeCallback
	}
	return c
}

// cluster is the seam's Cluster over one go-ceph connection.
type cluster struct {
	conn *rados.Conn
	mode Mode
	// instanceID is read once at connect, where librados caches it
	// (RadosClient::connect); rados_get_instance_id after Close would get
	// the nil handle go-ceph's Shutdown leaves.
	instanceID    uint64
	limit         *limiter
	reads, writes counter
	reaper        reaper
	close         sync.Once

	// Every field below mu is guarded by it.
	mu      sync.Mutex
	drained *sync.Cond // broadcast when ops drops to zero
	closed  bool
	// ops counts the calls using the connection outside a Pool (MonCommand,
	// ConfigGet, opening a Pool); Close shuts down only once it is zero.
	ops      int
	pools    map[*poolState]struct{}
	closeErr error
}

var (
	_ radosclient.Cluster       = (*cluster)(nil)
	_ radosclient.StatsReporter = (*cluster)(nil)
)

// Connect creates the librados connection, reads the config file and CEPH_ARGS, parses Args,
// connects, and returns the seam's Cluster. It refuses a librados older than
// Squid with ErrLibradosTooOld. When the connect fails and librados is too
// old for the AES256KRB5 cephx keys Squid 19.2.6 and Tentacle 20.2.4 create
// by default, it adds that cause, also as ErrLibradosTooOld, to what would
// otherwise be a bare EIO.
//
// Once connected it sizes the Cluster's in-flight limiter, which every Pool
// derived from it shares, from cfg or from librados's objecter throttle.
//
// librados cannot abandon a connection attempt; when ctx ends first, Connect
// returns ctx.Err() and a goroutine shuts the connection down once the
// attempt returns, which client_mount_timeout bounds.
func Connect(ctx context.Context, cfg Config) (radosclient.Cluster, error) {
	cfg = cfg.withDefaults()
	switch cfg.Mode {
	case ModeSync, ModeCallback, ModePipe:
	default:
		return nil, fmt.Errorf("goceph: completion mode %q: %w", cfg.Mode, radosclient.ErrBadOp)
	}
	if cfg.MaxInflightOps < 0 || cfg.MaxInflightBytes < 0 {
		return nil, fmt.Errorf("goceph: in-flight limits of %d ops and %d bytes: %w",
			cfg.MaxInflightOps, cfg.MaxInflightBytes, radosclient.ErrBadOp)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	version := libradosVersion()
	if _, ok := parseVersion(version); !ok {
		slog.WarnContext(ctx, "cannot read the librados version, so a failed connect will not be checked against the AES256KRB5 key floor",
			slog.String("version", version))
	}
	if err := checkLibradosLine(version); err != nil {
		return nil, err
	}

	conn, err := rados.NewConnWithClusterAndUser(cfg.Cluster, cfg.Name)
	if err != nil {
		return nil, toSeamError("create connection", err)
	}
	if err := configure(ctx, conn, cfg, slog.Default()); err != nil {
		conn.Shutdown()
		return nil, err
	}
	if err := acquireAioMode(cfg.Mode); err != nil {
		conn.Shutdown()
		return nil, err
	}

	connected := make(chan error, 1)
	go func() { connected <- conn.Connect() }()
	select {
	case err := <-connected:
		if err != nil {
			releaseAioMode(cfg.Mode)
			conn.Shutdown()
			return nil, explainConnect(version, toSeamError("connect", err))
		}
	case <-ctx.Done():
		go func() {
			<-connected
			conn.Shutdown()
			releaseAioMode(cfg.Mode)
		}()
		return nil, ctx.Err()
	}

	maxOps, maxBytes := cfg.MaxInflightOps, cfg.MaxInflightBytes
	if maxOps == 0 || maxBytes == 0 {
		// The objecter sized its throttle when rados_connect built it, from
		// the configuration the mons had just handed librados.
		derivedOps, derivedBytes := deriveLimits(ctx, conn.GetConfigOption)
		if maxOps == 0 {
			maxOps = derivedOps
		}
		if maxBytes == 0 {
			maxBytes = derivedBytes
		}
	}
	c := &cluster{
		conn:       conn,
		mode:       cfg.Mode,
		instanceID: conn.GetInstanceID(),
		limit:      newLimiter(maxOps, maxBytes),
		pools:      map[*poolState]struct{}{},
	}
	c.drained = sync.NewCond(&c.mu)
	return c, nil
}

// configure reads the config file, CEPH_ARGS and Args into conn in
// global_pre_init's order (global_init.cc:133-168 at v19.2.6 and v20.2.4).
// Given no path, rados_conf_read_file reads the files CEPH_CONF names, or
// else the default list, which --no-config-file skips (config.cc:428-435 at
// v19.2.6, :429-436 at v20.2.4). A list none of whose files can be read is
// an error only for -c without --no-config-file: without -c,
// global_pre_init warns "did not load config file, using default settings"
// and goes on, and under --no-config-file it goes on silently. configure
// logs to log.
func configure(ctx context.Context, conn *rados.Conn, cfg Config, log *slog.Logger) error {
	var err error
	switch {
	case cfg.ConfigFile != "":
		err = conn.ReadConfigFile(cfg.ConfigFile)
	case !cfg.NoConfigFile:
		err = conn.ReadDefaultConfigFile()
		if notFound(err) {
			log.WarnContext(ctx, "no ceph.conf found, continuing with defaults")
			err = nil
		}
	default:
		if _, ok := os.LookupEnv("CEPH_CONF"); ok {
			err = conn.ReadDefaultConfigFile()
		}
	}
	if cfg.NoConfigFile && notFound(err) {
		log.DebugContext(ctx, "no config file read under --no-config-file", slog.String("conf", cfg.ConfigFile))
		err = nil
	}
	if err != nil {
		return toSeamError("read config file", err)
	}
	if err := conn.ParseDefaultConfigEnv(); err != nil {
		return toSeamError("parse CEPH_ARGS", err)
	}
	if len(cfg.Args) > 0 {
		if err := conn.ParseCmdLineArgs(cfg.Args); err != nil {
			return toSeamError("parse args", err)
		}
	}
	return nil
}

// notFound reports whether a config file read failed because no file of its
// list could be read, which parse_config_files reports as ENOENT whatever
// kept each file from being read.
func notFound(err error) bool {
	return errors.Is(toSeamError("read config file", err), radosclient.ErrNotFound)
}

// ConfiguredOptions configures a librados handle from cfg as Connect does,
// without connecting, and returns the named options' values from it: what
// the config file, CEPH_ARGS and cfg.Args set, before connect applies the
// mon config store. radosgw fixes the ids it drops privileges to from that
// view (global_init.cc:227-318 at v19.2.6 and v20.2.4), before it fetches
// the mon config (:369-381). It logs nothing: Connect reads the same files
// and logs what it finds once.
func ConfiguredOptions(ctx context.Context, cfg Config, names ...string) (map[string]string, error) {
	cfg = cfg.withDefaults()
	conn, err := rados.NewConnWithClusterAndUser(cfg.Cluster, cfg.Name)
	if err != nil {
		return nil, toSeamError("create connection", err)
	}
	defer conn.Shutdown()
	if err := configure(ctx, conn, cfg, slog.New(slog.DiscardHandler)); err != nil {
		return nil, err
	}
	vals := make(map[string]string, len(names))
	for _, name := range names {
		v, err := conn.GetConfigOption(name)
		if err != nil {
			return nil, toSeamError("config get "+name, err)
		}
		vals[name] = v
	}
	return vals, nil
}

// begin counts a call that uses the connection outside a Pool, refusing it
// once Close has started; end finishes it.
func (c *cluster) begin(op string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return closedError(op, "cluster")
	}
	c.ops++
	return nil
}

func (c *cluster) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ops--
	if c.ops == 0 {
		c.drained.Broadcast()
	}
}

// Pool opens an I/O context on pool within namespace.
func (c *cluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.begin("open pool " + pool); err != nil {
		return nil, err
	}
	defer c.end()
	return openPool(c, pool, namespace)
}

// MonCommand runs a JSON mon command. The command blocks in librados; ctx is
// checked before it starts.
func (c *cluster) MonCommand(ctx context.Context, cmd []byte) (out []byte, status string, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, "", ctxErr
	}
	if beginErr := c.begin("mon command"); beginErr != nil {
		return nil, "", beginErr
	}
	defer c.end()
	out, status, err = c.conn.MonCommand(cmd)
	return out, status, toSeamError("mon command", err)
}

// ConfigGet reads a Ceph configuration option through librados.
func (c *cluster) ConfigGet(name string) (string, error) {
	op := "config get " + name
	if err := c.begin(op); err != nil {
		return "", err
	}
	defer c.end()
	v, err := c.conn.GetConfigOption(name)
	return v, toSeamError(op, err)
}

// InstanceID returns the client's global id, which the connection keeps for
// its lifetime; it stays readable after Close.
func (c *cluster) InstanceID() uint64 { return c.instanceID }

// FSID returns the fsid of the monitor map the connection holds
// (RadosClient::get_fsid).
func (c *cluster) FSID() (string, error) {
	if err := c.begin("fsid"); err != nil {
		return "", err
	}
	defer c.end()
	fsid, err := c.conn.GetFSID()
	return fsid, toSeamError("fsid", err)
}

// Stats reports the operations Read and Write submitted on every Pool of the
// Cluster, and what its in-flight limiter holds.
func (c *cluster) Stats() radosclient.Stats {
	s := c.limit.stats()
	s.ReadOps, s.ReadBytes = c.reads.ops.Load(), c.reads.bytes.Load()
	s.WriteOps, s.WriteBytes = c.writes.ops.Load(), c.writes.bytes.Load()
	return s
}

// track registers an open pool so Close can close it.
func (c *cluster) track(s *poolState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return closedError("open pool "+s.name, "cluster")
	}
	c.pools[s] = struct{}{}
	return nil
}

func (c *cluster) untrack(s *poolState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pools, s)
}

// Close refuses new calls and closes every Pool still open, which refuses
// new operations, closes their watches, waits for their operations,
// abandoned ones included, and destroys their I/O contexts; a Pool some other
// caller is already closing is waited for. It then waits for the calls in
// flight and the reaper, and shuts the connection down. A concurrent or later
// Close waits for the first and returns its result.
func (c *cluster) Close() error {
	c.close.Do(func() {
		c.mu.Lock()
		c.closed = true
		pools := make([]*poolState, 0, len(c.pools))
		for s := range c.pools {
			pools = append(pools, s)
		}
		c.mu.Unlock()

		errs := make([]error, 0, len(pools))
		for _, s := range pools {
			errs = append(errs, s.close())
		}

		c.mu.Lock()
		for c.ops > 0 {
			c.drained.Wait()
		}
		c.mu.Unlock()

		c.reaper.wait()
		c.conn.Shutdown()
		releaseAioMode(c.mode)
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}
