package seam

//go:generate go tool counterfeiter -generate

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// cluster reaches a release's rooket cluster through its Rook toolbox, and
// the pool the benchmark writes to there.
type cluster struct {
	harness.Cluster
}

// poolAvail returns the bytes ceph df says the pool can still take.
func (c cluster) poolAvail(ctx context.Context) (int64, error) {
	return c.PoolAvail(ctx, pool)
}

// objecter returns the client's value of the named objecter option, or
// unknown when the cluster cannot say.
func (c cluster) objecter(ctx context.Context, name string) string {
	out, err := c.Ceph(ctx, "config", "get", "client.admin", name)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// rados runs the rados CLI in the toolbox against the pool, failing any OSD
// or monitor operation that takes over two minutes, as the sweep's floor runs
// do, rather than waiting on a stuck placement group. Killing kubectl at a
// call's deadline ends the call here, but the rados it started in the
// toolbox's pod can outlive the exec session; these timeouts end it there.
func (c cluster) rados(ctx context.Context, args ...string) ([]byte, error) {
	return c.Toolbox(ctx, append([]string{"rados", "--rados-osd-op-timeout=120", "--rados-mon-op-timeout=120", "-p", pool}, args...)...)
}

// objects lists the pool's objects in namespace ns, or in every namespace
// when ns is empty.
func (c cluster) objects(ctx context.Context, ns string) ([]string, error) {
	args := []string{"ls", "--all"}
	if ns != "" {
		args = []string{"-N", ns, "ls"}
	}
	out, err := c.rados(ctx, args...)
	if err != nil {
		if ns != "" {
			return nil, fmt.Errorf("listing the objects in %s of %s: %w", ns, pool, err)
		}
		return nil, fmt.Errorf("listing the objects in %s: %w", pool, err)
	}
	var names []string
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimRight(line, "\n"); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// purgeBatch is how many names one rados rm takes, and purgeAtOnce how many
// rados rm run at once: a large write4k cell leaves hundreds of thousands of
// objects, which one rm at a time takes most of an hour to remove.
const (
	purgeBatch  = 1024
	purgeAtOnce = 8
)

// floorNamespace is the prefix of the namespaces the floor's rados bench
// writes in, bench-floor-<object size>.
const floorNamespace = "bench-floor-"

// purge removes every object left in namespace ns: in a floor namespace, rados
// bench's data objects with one concurrent cleanup, then whatever is left by
// name, a run's metadata or the objects of a benchmark process that did not
// end cleanly; one that does removes its own.
//
// The names go on stdin to one toolbox session whose xargs runs rados rm
// purgeAtOnce at a time, purgeBatch names each. xargs keeps starting batches
// after one fails and exits non-zero once all have run, so a failed batch
// loses only its own names, and one session spares a kubectl exec a batch.
func (c cluster) purge(ctx context.Context, log *slog.Logger, ns string) error {
	if strings.HasPrefix(ns, floorNamespace) {
		if _, err := c.rados(ctx, "-N", ns, "cleanup", "--prefix", "benchmark_data", "-t", "64"); err != nil {
			return fmt.Errorf("removing rados bench's objects in %s: %w", ns, err)
		}
	}
	names, err := c.objects(ctx, ns)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	log.InfoContext(ctx, "removing the objects left", slog.String("namespace", ns), slog.Int("objects", len(names)))
	if _, err := c.ToolboxIn(ctx, []byte(strings.Join(names, "\n")+"\n"),
		"xargs", "-r", "-d", `\n`, "-n", strconv.Itoa(purgeBatch), "-P", strconv.Itoa(purgeAtOnce),
		"rados", "-p", pool, "-N", ns, "rm"); err != nil {
		return fmt.Errorf("removing the objects left in %s: %w", ns, err)
	}
	log.InfoContext(ctx, "removed the objects left", slog.String("namespace", ns))
	return nil
}
