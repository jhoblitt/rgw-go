package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Cluster reaches a release's rooket cluster through its Rook toolbox, which
// runs the cluster's Ceph image with the admin keyring.
type Cluster struct {
	Exec    Execer
	Rooket  string
	Release string
	// CallTimeout bounds each toolbox call, so that a wedged cluster or
	// kubectl session fails the call rather than hanging the run.
	CallTimeout time.Duration
}

// Env is the environment every rooket call takes: ROOKET_NAME names its
// cluster, without which rooket falls back to a name derived from the working
// directory.
func (c Cluster) Env() []string {
	return []string{"ROOKET_NAME=rgw-go-" + c.Release}
}

// Kubectl runs rooket's kubectl with args against the cluster, within
// CallTimeout.
func (c Cluster) Kubectl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.CallTimeout)
	defer cancel()
	return c.Exec.Output(ctx, Cmd{Name: c.Rooket, Args: append([]string{"kubectl"}, args...), Env: c.Env()})
}

// Toolbox runs args in the toolbox, within CallTimeout.
func (c Cluster) Toolbox(ctx context.Context, args ...string) ([]byte, error) {
	return c.ToolboxIn(ctx, nil, args...)
}

// ToolboxIn is Toolbox with stdin passed to args, which kubectl exec forwards
// with -i; rooket hands kubectl its own stdin (rooket cmd/kubectl.go).
func (c Cluster) ToolboxIn(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.CallTimeout)
	defer cancel()
	prefix := []string{"kubectl", "-n", "rook-ceph", "exec", "deploy/rook-ceph-tools", "--"}
	if stdin != nil {
		prefix = []string{"kubectl", "-n", "rook-ceph", "exec", "-i", "deploy/rook-ceph-tools", "--"}
	}
	return c.Exec.Output(ctx, Cmd{
		Name:  c.Rooket,
		Args:  append(prefix, args...),
		Env:   c.Env(),
		Stdin: stdin,
	})
}

// Ceph runs the ceph CLI in the toolbox, bounding both the connect and each
// mon operation, so a wedged mon fails the command rather than hanging it.
func (c Cluster) Ceph(ctx context.Context, args ...string) ([]byte, error) {
	return c.Toolbox(ctx, append([]string{"ceph", "--connect-timeout=20", "--rados-mon-op-timeout=20"}, args...)...)
}

// PoolAvail returns the bytes ceph df says the pool can still take.
func (c Cluster) PoolAvail(ctx context.Context, pool string) (int64, error) {
	out, err := c.Ceph(ctx, "df", "-f", "json")
	if err != nil {
		return 0, fmt.Errorf("reading ceph df: %w", err)
	}
	return DecodePoolAvail(out, pool)
}

// DecodePoolAvail reads the named pool's max_avail from ceph df -f json.
func DecodePoolAvail(data []byte, name string) (int64, error) {
	var df struct {
		Pools []struct {
			Name  string `json:"name"`
			Stats struct {
				MaxAvail *int64 `json:"max_avail"`
			} `json:"stats"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(data, &df); err != nil {
		return 0, fmt.Errorf("decoding ceph df: %w", err)
	}
	for _, p := range df.Pools {
		if p.Name != name {
			continue
		}
		if p.Stats.MaxAvail == nil {
			return 0, fmt.Errorf("ceph df gives pool %s no max_avail", name)
		}
		return *p.Stats.MaxAvail, nil
	}
	return 0, fmt.Errorf("ceph df lists no pool %s", name)
}
