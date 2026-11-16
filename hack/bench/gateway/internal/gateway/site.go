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
	"syscall"
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

// emptyBuckets removes every bench bucket's objects through the radosgw: those
// of the plain buckets with elbencho's multi-object delete, a thousand keys a
// request, and the versioned bucket with its objects, versions and delete
// markers, which a delete naming no version leaves behind. Both run whatever
// the other gives. A bucket is first looked up, and one that is not there is
// skipped; one whose lookup tells nothing, a timeout or a toolbox that could
// not be reached among them, is skipped too, since nothing is known of it,
// and the failure is returned, so that its objects are not left for the next
// profile unnoticed.
func (r *Runner) emptyBuckets(ctx context.Context, st *state) error {
	var plain []string
	var versioned bool
	var errs []error
	for _, b := range buckets {
		exists, err := st.bucketExists(ctx, b)
		if err != nil {
			st.log.ErrorContext(ctx, "could not tell whether a bench bucket is there, so its objects stay",
				slog.String("bucket", b), slog.Any("error", err))
			errs = append(errs, fmt.Errorf("looking up %s before removing its objects: %w", b, err))
			continue
		}
		if !exists {
			continue
		}
		if b == verBucket {
			versioned = true
		} else {
			plain = append(plain, b)
		}
	}
	if len(plain) > 0 {
		st.log.InfoContext(ctx, "removing the objects of the bench buckets", slog.String("buckets", strings.Join(plain, " ")))
		errs = append(errs, r.logged(ctx, st, r.PrepTimeout,
			st.el(st.radosgwEndpoint, append([]string{"--s3multidel", "1000", "-t", strconv.Itoa(len(plain))}, plain...)...)))
	}
	switch {
	case versioned && st.purgeFailed:
		st.log.WarnContext(ctx, "not removing the versioned bench bucket again: its removal failed, and remove-user.sh takes it with the user",
			slog.String("bucket", verBucket))
	case versioned:
		errs = append(errs, r.purgeBucket(ctx, st, verBucket))
	}
	return errors.Join(errs...)
}

// purgeBucket removes bucket with every version in it: radosgw-admin lists
// them all, as no client's delete does without a version id for each. The
// bucket is gone after it, so the next profile's preparation makes it again,
// unversioned. It has a bound of its own, PurgeTimeout, since a bucket of
// hundreds of thousands of versions takes far longer than a cluster call or
// a preparation is allowed, and it logs its start, its end and how long it
// took, which the next run's bound is chosen from.
//
// radosgw-admin bucket rm exits 0 when it removes nothing (the registry's
// "radosgw-admin bucket rm exits 0 when it removes nothing", in
// docs/ceph-upstream-bugs.md), so the bucket is read back, and a bucket still
// there fails the run, as does a read-back that tells nothing.
func (r *Runner) purgeBucket(ctx context.Context, st *state, bucket string) (err error) {
	defer func() {
		if err != nil {
			st.purgeFailed = true
		}
	}()
	args := []string{"bucket", "rm", "--bucket", bucket, "--purge-objects"}
	started := r.Now()
	st.log.InfoContext(ctx, "removing the versioned bench bucket with its versions",
		slog.String("bucket", bucket), slog.Int64("limit_seconds", seconds(r.PurgeTimeout)))
	out, err := st.adminWithin(ctx, r.PurgeTimeout, args...)
	st.ran(ctx, "radosgw-admin "+strings.Join(args, " "), out, err)
	took := seconds(r.Now().Sub(started))
	if err != nil {
		st.log.ErrorContext(ctx, "removing the versioned bench bucket failed",
			slog.String("bucket", bucket), slog.Int64("seconds", took), slog.Int64("limit_seconds", seconds(r.PurgeTimeout)), slog.Any("error", err))
		return fmt.Errorf("removing %s with its versions failed after %d s of the %d s allowed: %w", bucket, took, seconds(r.PurgeTimeout), err)
	}
	switch exists, readErr := st.bucketExists(ctx, bucket); {
	case readErr != nil:
		st.log.ErrorContext(ctx, "the versioned bench bucket could not be read back",
			slog.String("bucket", bucket), slog.Int64("seconds", took), slog.Any("error", readErr))
		return fmt.Errorf("reading back %s after radosgw-admin bucket rm exited 0, %d s into the %d s allowed: %w", bucket, took, seconds(r.PurgeTimeout), readErr)
	case exists:
		st.log.ErrorContext(ctx, "the versioned bench bucket is still there", slog.String("bucket", bucket), slog.Int64("seconds", took))
		return fmt.Errorf("%s still exists after radosgw-admin bucket rm exited 0, %d s into the %d s allowed", bucket, took, seconds(r.PurgeTimeout))
	}
	st.log.InfoContext(ctx, "removed the versioned bench bucket", slog.String("bucket", bucket), slog.Int64("seconds", took))
	return nil
}

// statusPrefix starts the line lookupScript ends its output with.
const statusPrefix = "radosgw-admin-status="

// lookupScript is the bucket lookup the toolbox's sh runs: radosgw-admin
// bucket stats of the bucket its first argument names, in the site its others
// name, the stats discarded and the error output kept, then its exit status on
// a line of its own. rooket exits 1 for any command that fails (rooket
// cmd/kubectl.go and cmd/root.go), so the status reaches the run only as
// output. The bucket is an argument, never part of the script.
const lookupScript = `bucket=$1; shift; radosgw-admin bucket stats --bucket "$bucket" "$@" 2>&1 >/dev/null; printf '\n` +
	statusPrefix + `%s\n' "$?"`

// bucketExists reports whether bucket exists, from the exit status of
// radosgw-admin bucket stats, which lookupScript prints: 0 for a bucket that
// exists, and 2 for one that does not. bucket stats exits with
// posix_errortrans of its failure, at v20.2.4
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L7655-L7659)
// and at v19.2.7
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L7408-L7412),
// which turns ERR_NO_SUCH_BUCKET into ENOENT
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L132-L135,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L120-L130).
// Any other status, an output that ends in none, and a toolbox that could not
// run the script are errors, since none of them tells whether the bucket is
// there.
func (st *state) bucketExists(ctx context.Context, bucket string) (bool, error) {
	out, err := st.cl.Toolbox(ctx, append([]string{"sh", "-c", lookupScript, "sh", bucket}, st.siteOptions()...)...)
	st.ran(ctx, "radosgw-admin bucket stats --bucket "+bucket, out, err)
	if err != nil {
		return false, err
	}
	text := strings.TrimRight(string(out), "\n")
	before, last, found := strings.CutLast(text, "\n")
	if !found {
		before, last = "", text
	}
	field, ok := strings.CutPrefix(last, statusPrefix)
	status, convErr := strconv.Atoi(field)
	switch {
	case !ok || convErr != nil:
		return false, fmt.Errorf("radosgw-admin bucket stats --bucket %s ended in no exit status: %q", bucket, out)
	case status == 0:
		return true, nil
	case status == int(syscall.ENOENT):
		return false, nil
	}
	return false, fmt.Errorf("radosgw-admin bucket stats --bucket %s exited %d: %s", bucket, status, strings.TrimSpace(before))
}

// zonegroup is the zonegroup's api_name, the region elbencho signs for, and
// its default placement.
func (r *Runner) zonegroup(ctx context.Context, st *state) (apiName, placement string, err error) {
	out, err := st.admin(ctx, "zonegroup", "get")
	if err != nil {
		return "", "", fmt.Errorf("reading the zonegroup: %w", err)
	}
	var zg struct {
		APIName          string `json:"api_name"`
		DefaultPlacement string `json:"default_placement"`
	}
	if err := json.Unmarshal(out, &zg); err != nil {
		return "", "", fmt.Errorf("decoding radosgw-admin zonegroup get: %w", err)
	}
	return zg.APIName, zg.DefaultPlacement, nil
}

// checkClass fails unless the zone's default placement holds the class the
// comp profiles write through, and gives it a compressor: without one the
// profiles would measure plain writes under a compressing profile's name.
// make populate makes the class on every cluster it populates.
func (r *Runner) checkClass(ctx context.Context, st *state) error {
	out, err := st.admin(ctx, "zone", "get")
	if err != nil {
		return fmt.Errorf("reading the zone: %w", err)
	}
	var zone struct {
		PlacementPools []struct {
			Key string `json:"key"`
			Val struct {
				StorageClasses map[string]struct {
					CompressionType string `json:"compression_type"`
				} `json:"storage_classes"`
			} `json:"val"`
		} `json:"placement_pools"`
	}
	if err := json.Unmarshal(out, &zone); err != nil {
		return fmt.Errorf("decoding radosgw-admin zone get: %w", err)
	}
	for _, pool := range zone.PlacementPools {
		if pool.Key != st.defaultPlacement {
			continue
		}
		class, ok := pool.Val.StorageClasses[compClass]
		if !ok {
			return fmt.Errorf("the placement %s has no storage class %s: run make populate RELEASE=%s first",
				st.defaultPlacement, compClass, st.cfg.Release)
		}
		if class.CompressionType == "" || class.CompressionType == "none" {
			return fmt.Errorf("the storage class %s of the placement %s has no compressor, so the comp profiles would write uncompressed: "+
				"run make populate RELEASE=%s first", compClass, st.defaultPlacement, st.cfg.Release)
		}
		st.log.InfoContext(ctx, "compressing class", slog.String("class", compClass), slog.String("compression_type", class.CompressionType))
		return nil
	}
	return fmt.Errorf("the zone has no placement %s, the zonegroup's default", st.defaultPlacement)
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
