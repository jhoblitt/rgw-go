package goceph

import (
	"context"
	"errors"
	"fmt"
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
	Cluster    string   // ceph cluster name, default "ceph"
	Name       string   // entity name, e.g. "client.rgw.a"; default "client.admin"
	ConfigFile string   // "" reads the default config file
	Args       []string // ceph-style argv handed to librados after the early arguments
	Mode       Mode     // default ModeCallback
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
	conn   *rados.Conn
	mode   Mode
	reaper reaper
	close  sync.Once

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

var _ radosclient.Cluster = (*cluster)(nil)

// Connect creates the librados connection, reads the config file and CEPH_ARGS, parses Args,
// connects, and returns the seam's Cluster.
//
// librados cannot abandon a connection attempt; when ctx ends first, Connect
// returns ctx.Err() and a goroutine shuts the connection down once the
// attempt returns, which client_mount_timeout bounds.
func Connect(ctx context.Context, cfg Config) (radosclient.Cluster, error) {
	cfg = cfg.withDefaults()
	switch cfg.Mode {
	case ModeSync, ModeCallback, ModePipe:
	default:
		return nil, fmt.Errorf("goceph: completion mode %q: %w", cfg.Mode, radosclient.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conn, err := rados.NewConnWithClusterAndUser(cfg.Cluster, cfg.Name)
	if err != nil {
		return nil, toSeamError("create connection", err)
	}
	if err := configure(conn, cfg); err != nil {
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
			return nil, toSeamError("connect", err)
		}
	case <-ctx.Done():
		go func() {
			<-connected
			conn.Shutdown()
			releaseAioMode(cfg.Mode)
		}()
		return nil, ctx.Err()
	}
	c := &cluster{conn: conn, mode: cfg.Mode, pools: map[*poolState]struct{}{}}
	c.drained = sync.NewCond(&c.mu)
	return c, nil
}

func configure(conn *rados.Conn, cfg Config) error {
	var err error
	if cfg.ConfigFile == "" {
		err = conn.ReadDefaultConfigFile()
	} else {
		err = conn.ReadConfigFile(cfg.ConfigFile)
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
		return closedError(op)
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

// track registers an open pool so Close can close it.
func (c *cluster) track(s *poolState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return closedError("open pool " + s.name)
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
