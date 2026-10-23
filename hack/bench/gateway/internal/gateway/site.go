package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

var (
	podIPPattern   = regexp.MustCompile(`^[0-9.]+$`)
	podNamePattern = regexp.MustCompile(`^[a-z0-9.-]+$`)
	portPattern    = regexp.MustCompile(`port=(\d+)`)
)

// useSite reads the cluster's one radosgw from the service map, which names
// the site radosgw-admin works in from then on, and finds the radosgw's
// endpoint, config entity and pod.
func (r *Runner) useSite(ctx context.Context, st *state) error {
	rgw, err := st.cl.OneRadosgw(ctx, harness.Poll{Now: r.Now, Sleep: r.Sleep, Wait: r.SiteWait, Every: r.SitePoll})
	if err != nil {
		return err
	}
	if st.site.realm, st.site.zonegroup, st.site.zone, err = rgw.Site(); err != nil {
		return err
	}
	st.radosgwFrontends = rgw.Frontends()

	// The radosgw is host-networked, so its pod's IP is its node's.
	out, err := st.cl.Kubectl(ctx, "-n", "rook-ceph", "get", "pods", "-l", "app=rook-ceph-rgw",
		"-o", "jsonpath={.items[*].status.podIP}")
	if err != nil {
		return fmt.Errorf("reading the radosgw pod's IP: %w", err)
	}
	ip := strings.TrimRight(string(out), "\n")
	if !podIPPattern.MatchString(ip) {
		return fmt.Errorf("want one radosgw pod IP, have '%s'", ip)
	}
	port := portPattern.FindStringSubmatch(st.radosgwFrontends)
	if port == nil {
		return fmt.Errorf("no port in the radosgw's frontend config: %s", rgw.Raw)
	}
	st.radosgwEndpoint = "http://" + ip + ":" + port[1]

	if st.radosgwEntity, err = st.cl.RadosgwEntity(ctx, rgw); err != nil {
		return err
	}

	out, err = st.cl.Kubectl(ctx, "-n", "rook-ceph", "get", "pods", "-l", "app=rook-ceph-rgw",
		"-o", "jsonpath={.items[*].metadata.name}")
	if err != nil {
		return fmt.Errorf("reading the radosgw pod's name: %w", err)
	}
	pod := strings.TrimRight(string(out), "\n")
	if !podNamePattern.MatchString(pod) {
		return fmt.Errorf("want one radosgw pod, have '%s'", pod)
	}
	st.radosgwPod = pod
	return nil
}

// drainGC runs every entry of the garbage collection queue, expired or not,
// until the queue is empty, for up to GCWait: the parity options leave no
// collector thread to run them.
func (r *Runner) drainGC(ctx context.Context, st *state) error {
	deadline := r.Now().Add(r.GCWait)
	for {
		if err := r.gcProcess(ctx, st, nil); err != nil {
			return err
		}
		out, err := st.admin(ctx, "gc", "list", "--include-all")
		if err != nil {
			return err
		}
		var queued []json.RawMessage
		if err := json.Unmarshal(out, &queued); err != nil {
			return fmt.Errorf("decoding radosgw-admin gc list: %w", err)
		}
		if len(queued) == 0 {
			return nil
		}
		if !r.Now().Before(deadline) {
			return fmt.Errorf("the queue still holds %d entries", len(queued))
		}
		if err := r.Sleep(ctx, r.GCPoll); err != nil {
			return err
		}
	}
}

// gcProcess runs radosgw-admin gc process --include-all, its output going
// to w, or run.log when w is nil.
func (r *Runner) gcProcess(ctx context.Context, st *state, w io.Writer) error {
	args := []string{"gc", "process", "--include-all"}
	out, err := st.admin(ctx, args...)
	if w == nil {
		st.ran(ctx, "radosgw-admin "+strings.Join(args, " "), out, err)
		return err
	}
	if _, werr := w.Write(out); werr != nil {
		return errors.Join(err, werr)
	}
	return err
}

// needSpace waits up to SpaceWait, since the space a removal frees reaches
// ceph df a few seconds after it, for the data pool to take twice the
// dataset of profile, the run's plan, and fails otherwise. Each read of ceph
// df gets no more than the wait has left, but at least a second.
func (r *Runner) needSpace(ctx context.Context, st *state, profile string) error {
	dataset := st.cfg.Dataset(profile)
	deadline := r.Now().Add(r.SpaceWait)
	for {
		poll, cancel := context.WithTimeout(ctx, max(deadline.Sub(r.Now()), time.Second))
		avail, err := st.cl.PoolAvail(poll, st.dataPool)
		cancel()
		if err == nil && avail >= 2*dataset {
			return nil
		}
		if !r.Now().Before(deadline) {
			if err != nil {
				return fmt.Errorf("%s: the data pool can take unknown B, under twice the %d B its dataset holds: %w", profile, dataset, err)
			}
			return fmt.Errorf("%s: the data pool can take %d B, under twice the %d B its dataset holds", profile, avail, dataset)
		}
		if err := r.Sleep(ctx, r.SpacePoll); err != nil {
			return err
		}
	}
}

// emptyBuckets removes every bench bucket's objects through the radosgw with
// elbencho's multi-object delete, a thousand keys a request.
func (r *Runner) emptyBuckets(ctx context.Context, st *state) error {
	var have []string
	for _, b := range buckets {
		if _, err := st.admin(ctx, "bucket", "stats", "--bucket", b); err == nil {
			have = append(have, b)
		}
	}
	if len(have) == 0 {
		return nil
	}
	st.log.InfoContext(ctx, "removing the objects of the bench buckets", slog.String("buckets", strings.Join(have, " ")))
	return r.logged(ctx, st, r.PrepTimeout,
		st.el(st.radosgwEndpoint, append([]string{"--s3multidel", "1000", "-t", strconv.Itoa(len(have))}, have...)...))
}

// apiName is the zonegroup's api_name, the region elbencho signs for.
func (r *Runner) apiName(ctx context.Context, st *state) (string, error) {
	out, err := st.admin(ctx, "zonegroup", "get")
	if err != nil {
		return "", fmt.Errorf("reading the zonegroup: %w", err)
	}
	var zg struct {
		APIName string `json:"api_name"`
	}
	if err := json.Unmarshal(out, &zg); err != nil {
		return "", fmt.Errorf("decoding radosgw-admin zonegroup get: %w", err)
	}
	return zg.APIName, nil
}

// ensureUser creates the user bench with its fixed keys unless it exists,
// and checks that it holds them; from then on the run removes it when it
// ends. A bench user without those keys is someone else's, and is left
// alone.
func (r *Runner) ensureUser(ctx context.Context, st *state) error {
	out, err := st.admin(ctx, "user", "info", "--uid", benchUID)
	if err != nil {
		out, err = st.admin(ctx, "user", "create", "--uid", benchUID, "--display-name", benchUID,
			"--access-key", benchKey, "--secret-key", benchSecret)
		if err != nil {
			return fmt.Errorf("creating the user %s failed: %w", benchUID, err)
		}
	}
	var user struct {
		Keys []userKey `json:"keys"`
	}
	if err := json.Unmarshal(out, &user); err != nil {
		return fmt.Errorf("decoding the user %s: %w", benchUID, err)
	}
	if !slices.Contains(user.Keys, userKey{AccessKey: benchKey, SecretKey: benchSecret}) {
		return fmt.Errorf("the user %s exists without the key %s; remove it and rerun", benchUID, benchKey)
	}
	st.userCreated = true
	return nil
}

// userKey is one of a user's S3 keys, as radosgw-admin user info gives it.
type userKey struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// parityCheck fails unless entity holds every parity option: show reads what
// a running radosgw uses, get what the monitors give a client that registers
// no daemon with the manager, as rgw-go does. Its message names what sets
// the option: rgw-go-up.sh, but for radosgw's rgw_enable_lc_threads, which
// the LC mode holds in the object store's rgwConfig and make lc-mode LC=off
// restores.
func (r *Runner) parityCheck(ctx context.Context, st *state, entity, mode string) error {
	for _, o := range st.parity {
		out, err := st.cl.Ceph(ctx, "config", mode, entity, o.name)
		if err != nil {
			return fmt.Errorf("ceph config %s %s %s failed: %w", mode, entity, o.name, err)
		}
		if have := strings.TrimRight(string(out), "\n"); have != o.value {
			fix := "make rgw-go-up RELEASE=" + st.cfg.Release
			if entity == st.radosgwEntity && o.name == "rgw_enable_lc_threads" {
				fix = "make lc-mode LC=off RELEASE=" + st.cfg.Release
			}
			return fmt.Errorf("%s holds %s=%s, not the parity setting %s: run %s first", entity, o.name, have, o.value, fix)
		}
	}
	return nil
}

// parityOptions are the options both gateways run with when they are
// compared, in the order the release's chart values give them: the rgwConfig
// of the values' object store, which make parity-options-check holds equal
// to hack/rooket/lib.sh's parity_options, the list rgw-go-up.sh sets on both
// entities but for radosgw's rgw_enable_lc_threads, which the LC mode holds.
func parityOptions(repo, release string) ([]option, error) {
	path := filepath.Join(repo, "hack", "rooket", release, "values", "rook-ceph-cluster.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // the path is the release's own chart values
	if err != nil {
		return nil, fmt.Errorf("reading the chart values: %w", err)
	}
	var values struct {
		CephObjectStores []struct {
			Name string `yaml:"name"`
			Spec struct {
				Gateway struct {
					RgwConfig yaml.Node `yaml:"rgwConfig"`
				} `yaml:"gateway"`
			} `yaml:"spec"`
		} `yaml:"cephObjectStores"`
	}
	if err := yaml.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for i := range values.CephObjectStores {
		store := &values.CephObjectStores[i]
		if store.Name != "ceph-objectstore" {
			continue
		}
		node := store.Spec.Gateway.RgwConfig
		if node.Kind != yaml.MappingNode || len(node.Content) == 0 {
			break
		}
		options := make([]option, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			options = append(options, option{name: node.Content[i].Value, value: node.Content[i+1].Value})
		}
		return options, nil
	}
	return nil, fmt.Errorf("%s has no rgwConfig at cephObjectStores[name=ceph-objectstore].spec.gateway", path)
}

// noDelay is on or off, the TCP_NODELAY gateway's frontends give its
// connections from their first tcp_nodelay: radosgw's beast turns it on only
// for 1, and rgw-go keeps Go's, on, unless tcp_nodelay names anything else.
func noDelay(gateway, frontends string) string {
	for w := range strings.FieldsSeq(frontends) {
		if w == "tcp_nodelay" || strings.HasPrefix(w, "tcp_nodelay=") {
			if strings.TrimPrefix(strings.TrimPrefix(w, "tcp_nodelay"), "=") == "1" {
				return "on"
			}
			return "off"
		}
	}
	if gateway == "rgw-go" {
		return "on"
	}
	return "off"
}
