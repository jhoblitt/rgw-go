package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// rgwDaemon is the service map's entry of the cluster's one radosgw: the
// realm, zonegroup and zone it serves, its id and its frontends are in its
// metadata.
type rgwDaemon struct {
	Metadata map[string]string `json:"metadata"`
	// raw is the entry as the service map gives it, for messages.
	raw string
}

var (
	podIPPattern   = regexp.MustCompile(`^[0-9.]+$`)
	podNamePattern = regexp.MustCompile(`^[a-z0-9.-]+$`)
	portPattern    = regexp.MustCompile(`port=(\d+)`)
)

// useSite reads the cluster's one radosgw from the service map, which names
// the site radosgw-admin works in from then on, and finds the radosgw's
// endpoint, config entity and pod.
func (r *Runner) useSite(ctx context.Context, st *state) error {
	rgw, err := r.rgwDaemon(ctx, st)
	if err != nil {
		return err
	}
	md := rgw.Metadata
	if md["realm_name"] == "" || md["zonegroup_name"] == "" || md["zone_name"] == "" {
		return fmt.Errorf("the radosgw reports no realm, zonegroup or zone: %s", rgw.raw)
	}
	st.site = site{realm: md["realm_name"], zonegroup: md["zonegroup_name"], zone: md["zone_name"]}
	st.radosgwFrontends = md["frontend_config#0"]

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
		return fmt.Errorf("no port in the radosgw's frontend config: %s", rgw.raw)
	}
	st.radosgwEndpoint = "http://" + ip + ":" + port[1]

	// Rook runs the radosgw as client.rgw. and its deployment's name past
	// rook-ceph-rgw, dashes mapped to dots (generateCephXUser in rook
	// pkg/operator/ceph/object/config.go), and the service map's id is that
	// name past client.rgw. config show answers only for a section a running
	// daemon reads, with the frontends that daemon reports, so a wrong name
	// stops here rather than checking options nothing reads.
	id := md["id"]
	if id == "" {
		return fmt.Errorf("the radosgw reports no id: %s", rgw.raw)
	}
	entity := "client.rgw." + id
	out, err = st.cl.Ceph(ctx, "config", "show", entity, "rgw_frontends")
	if err != nil {
		return fmt.Errorf("no running daemon reads the config section %s: %w", entity, err)
	}
	if frontends := strings.TrimRight(string(out), "\n"); frontends == "" || frontends != st.radosgwFrontends {
		return fmt.Errorf("%s reports the frontends '%s', the radosgw '%s'", entity, frontends, st.radosgwFrontends)
	}
	st.radosgwEntity = entity

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

// rgwDaemon polls the service map until it lists exactly one radosgw, and
// returns its entry. A radosgw registers under a new gid each time it starts
// or reloads its realm, and the manager can list the old one beside it for
// up to mgr_service_beacon_grace, 60 s, after a restart. After SiteWait it
// fails, never restarting a radosgw itself.
func (r *Runner) rgwDaemon(ctx context.Context, st *state) (rgwDaemon, error) {
	deadline := r.Now().Add(r.SiteWait)
	for {
		out, err := st.cl.Ceph(ctx, "service", "dump", "-f", "json")
		var entries []json.RawMessage
		if err == nil {
			entries, err = rgwEntries(out)
		}
		if err == nil && len(entries) == 1 {
			var rgw rgwDaemon
			if err = json.Unmarshal(entries[0], &rgw); err != nil {
				return rgwDaemon{}, fmt.Errorf("decoding the radosgw's service map entry: %w", err)
			}
			rgw.raw = string(entries[0])
			return rgw, nil
		}
		if !r.Now().Before(deadline) {
			if err != nil {
				return rgwDaemon{}, fmt.Errorf("the service map did not settle to one radosgw in %ds: "+
					"ceph service dump failed, so the service map could not be read: %w", seconds(r.SiteWait), err)
			}
			listed, err := json.Marshal(entries)
			if err != nil {
				return rgwDaemon{}, fmt.Errorf("encoding the radosgw entries: %w", err)
			}
			return rgwDaemon{}, fmt.Errorf("the service map did not settle to one radosgw in %ds (radosgw entries: %s); "+
				"the manager drops a stopped radosgw within about a minute, so none is running or more than one is",
				seconds(r.SiteWait), listed)
		}
		if err := r.Sleep(ctx, r.SitePoll); err != nil {
			return rgwDaemon{}, err
		}
	}
}

// rgwEntries are the service map's radosgw entries, compacted, in the order
// of their gids; its summary is none.
func rgwEntries(dump []byte) ([]json.RawMessage, error) {
	var sm struct {
		Services struct {
			RGW struct {
				Daemons map[string]json.RawMessage `json:"daemons"`
			} `json:"rgw"`
		} `json:"services"`
	}
	if err := json.Unmarshal(dump, &sm); err != nil {
		return nil, fmt.Errorf("decoding ceph service dump: %w", err)
	}
	entries := []json.RawMessage{}
	for _, gid := range slices.Sorted(maps.Keys(sm.Services.RGW.Daemons)) {
		if gid == "summary" {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, sm.Services.RGW.Daemons[gid]); err != nil {
			return nil, fmt.Errorf("decoding the service map entry %s: %w", gid, err)
		}
		entries = append(entries, compact.Bytes())
	}
	return entries, nil
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
// no daemon with the manager, as rgw-go does.
func (r *Runner) parityCheck(ctx context.Context, st *state, entity, mode string) error {
	for _, o := range st.parity {
		out, err := st.cl.Ceph(ctx, "config", mode, entity, o.name)
		if err != nil {
			return fmt.Errorf("ceph config %s %s %s failed: %w", mode, entity, o.name, err)
		}
		if have := strings.TrimRight(string(out), "\n"); have != o.value {
			return fmt.Errorf("%s holds %s=%s, not the parity setting %s: run make rgw-go-up RELEASE=%s first",
				entity, o.name, have, o.value, st.cfg.Release)
		}
	}
	return nil
}

// parityOptions are the options both gateways run with when they are
// compared, in the order the release's chart values give them: the rgwConfig
// of the values' object store, which make parity-options-check holds equal
// to hack/rooket/lib.sh's parity_options, the one list rgw-go-up.sh sets.
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
