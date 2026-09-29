package goceph

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	Cluster          string   // ceph cluster name, default "ceph"
	Name             string   // entity name, e.g. "client.rgw.a"; default "client.admin"
	ConfigFile       string   // "" searches CEPH_CONF or the default config files
	NoConfigFile     bool     // --no-config-file: skip that search; a named ConfigFile is still read
	Args             []string // ceph-style argv handed to librados after the early arguments
	Mode             Mode     // default ModeCallback
	MaxInflightOps   int      // 0 derives from objecter_inflight_ops after connect
	MaxInflightBytes int64    // 0 derives from objecter_inflight_op_bytes after connect
}

func (c Config) withDefaults() Config {
	if c.Cluster == "" {
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
	if err := configure(ctx, conn, cfg); err != nil {
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

// configure reads the config file, CEPH_ARGS and Args into conn. Given no
// path, rados_conf_read_file searches CEPH_CONF or the default list, and
// finding no file there is not an error: ceph's global_pre_init only warns
// "did not load config file, using default settings" and goes on.
func configure(ctx context.Context, conn *rados.Conn, cfg Config) error {
	var err error
	switch {
	case cfg.ConfigFile != "":
		err = conn.ReadConfigFile(cfg.ConfigFile)
	case cfg.NoConfigFile:
	default:
		err = conn.ReadDefaultConfigFile()
		if errors.Is(toSeamError("read config file", err), radosclient.ErrNotFound) {
			slog.WarnContext(ctx, "no ceph.conf found, continuing with defaults")
			err = nil
		}
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
