package clusterconf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

const (
	// namespace is where Rook runs the cluster.
	namespace = "rook-ceph"
	// objectStore is the CephObjectStore the chart values name, whose
	// rgwConfig Rook writes for its radosgw.
	objectStore = "ceph-objectstore"
	// rgwSelector selects the radosgw's deployment and pod.
	rgwSelector = "app=rook-ceph-rgw"
	// configLogWindow is how many of the mons' config changes are read.
	configLogWindow = 1000
)

// radosgw is the cluster's one radosgw: its service map entry, the site it
// serves, its config entity, and, once setRadosgw has read it, the
// rgw_enable_lc_threads its running daemon reports.
type radosgw struct {
	harness.Radosgw
	realm, zonegroup, zone string
	entity                 string
	lc                     string
}

// radosgw waits for the service map to list one radosgw, and returns it with
// its site and its config entity checked.
func (c *Configurer) radosgw(ctx context.Context) (radosgw, error) {
	entry, err := c.Cluster.OneRadosgw(ctx, harness.Poll{Now: c.Now, Sleep: c.Sleep, Wait: c.SiteWait, Every: c.SitePoll})
	if err != nil {
		return radosgw{}, err
	}
	rgw := radosgw{Radosgw: entry}
	if rgw.realm, rgw.zonegroup, rgw.zone, err = entry.Site(); err != nil {
		return radosgw{}, err
	}
	if rgw.StartStamp == "" {
		return radosgw{}, fmt.Errorf("the radosgw's service map entry has no start_stamp: %s", rgw.Raw)
	}
	if rgw.entity, err = c.Cluster.RadosgwEntity(ctx, entry); err != nil {
		return radosgw{}, err
	}
	return rgw, nil
}

// storeState is what setRadosgw reads of the object store.
type storeState struct {
	Metadata struct {
		Generation int64 `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Gateway struct {
			RgwConfig map[string]string `json:"rgwConfig"`
		} `json:"gateway"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration int64  `json:"observedGeneration"`
		Phase              string `json:"phase"`
		Message            string `json:"message"`
	} `json:"status"`
}

func (c *Configurer) store(ctx context.Context) (storeState, error) {
	out, err := c.Cluster.Kubectl(ctx, "-n", namespace, "get", "cephobjectstore", objectStore, "-o", "json")
	if err != nil {
		return storeState{}, fmt.Errorf("reading the object store %s: %w", objectStore, err)
	}
	var s storeState
	if err := json.Unmarshal(out, &s); err != nil {
		return storeState{}, fmt.Errorf("decoding the object store %s: %w", objectStore, err)
	}
	return s, nil
}

// setRadosgw runs radosgw's lifecycle worker when on, and stops it
// otherwise. rgwConfig is the option's one writer: rgw-go-up.sh leaves it
// to Rook, and Rook writes it at each reconcile, so a mode set otherwise
// would not hold. When the running radosgw started before the option last
// changed, the radosgw is restarted, as rgw-go-up.sh restarts it for the
// parity options, and the switch waits until the service map lists one
// started after the change.
func (c *Configurer) setRadosgw(ctx context.Context, rgw *radosgw, on bool) error {
	want := strconv.FormatBool(on)
	s, err := c.store(ctx)
	if err != nil {
		return err
	}
	if s.Spec.Gateway.RgwConfig[lcOption] != want {
		err = c.setRgwConfig(ctx, want)
		if err != nil {
			return err
		}
		if *rgw, err = c.radosgw(ctx); err != nil {
			return err
		}
	}
	got, err := c.cephValue(ctx, "config", "get", rgw.entity, lcOption)
	if err != nil {
		return fmt.Errorf("reading %s's %s: %w", rgw.entity, lcOption, err)
	}
	if got != want {
		// rgwConfig holds want already, so that no reconcile is due to
		// write it: the entity was changed by hand. Setting it agrees with
		// the next reconcile.
		slog.InfoContext(ctx, "setting the radosgw's option to the object store's rgwConfig",
			slog.String("entity", rgw.entity), slog.String(lcOption, want), slog.String("mons", got))
		if _, err = c.Cluster.Ceph(ctx, "config", "set", rgw.entity, lcOption, want); err != nil {
			return fmt.Errorf("setting %s's %s to %s: %w", rgw.entity, lcOption, want, err)
		}
	}
	stale, err := c.stale(ctx, *rgw)
	if err != nil {
		return err
	}
	if stale {
		err = c.restartRadosgw(ctx, rgw)
		if err != nil {
			return err
		}
	}
	if rgw.lc, err = c.cephValue(ctx, "config", "show", rgw.entity, lcOption); err != nil {
		return fmt.Errorf("reading the running radosgw's %s: %w", lcOption, err)
	}
	if rgw.lc != want {
		return fmt.Errorf("the radosgw %s runs with %s %s, not %s", rgw.entity, lcOption, rgw.lc, want)
	}
	return nil
}

// setRgwConfig sets the object store's rgwConfig option to want and waits
// until Rook has reconciled it, which it marks in the status's
// observedGeneration once it has written the options and updated the
// radosgw's deployment
// (https://github.com/rook/rook/blob/v1.21.1/pkg/operator/ceph/object/controller.go#L523-L527),
// and then for any rollout that update started.
func (c *Configurer) setRgwConfig(ctx context.Context, want string) error {
	slog.InfoContext(ctx, "setting the object store's rgwConfig", slog.String("object_store", objectStore), slog.String(lcOption, want))
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"gateway": map[string]any{
		"rgwConfig": map[string]string{lcOption: want},
	}}})
	if err != nil {
		return fmt.Errorf("encoding the patch: %w", err)
	}
	if _, err = c.Cluster.Kubectl(ctx, "-n", namespace, "patch", "cephobjectstore", objectStore,
		"--type", "merge", "-p", string(patch)); err != nil {
		return fmt.Errorf("patching the object store %s: %w", objectStore, err)
	}
	var s storeState
	err = c.poll(ctx, c.RookWait, func() (bool, error) {
		var serr error
		if s, serr = c.store(ctx); serr != nil {
			return false, serr
		}
		return s.Spec.Gateway.RgwConfig[lcOption] == want && s.Status.ObservedGeneration >= s.Metadata.Generation, nil
	}, func(last error) error {
		if last != nil {
			return fmt.Errorf("the Rook operator did not reconcile the object store %s in %ds: %w", objectStore,
				seconds(c.RookWait), last)
		}
		return fmt.Errorf("the Rook operator did not reconcile the object store %s in %ds: generation %d, observed %d, phase %q: %s",
			objectStore, seconds(c.RookWait), s.Metadata.Generation, s.Status.ObservedGeneration, s.Status.Phase, s.Status.Message)
	})
	if err != nil {
		return err
	}
	if err := c.kubectlLong(ctx, "-n", namespace, "rollout", "status", "deploy", "-l", rgwSelector, "--timeout=5m"); err != nil {
		return fmt.Errorf("waiting for the radosgw's rollout: %w", err)
	}
	return nil
}

// restartRadosgw restarts the radosgw's deployment and waits until the
// service map lists one radosgw that started after the option last changed.
func (c *Configurer) restartRadosgw(ctx context.Context, rgw *radosgw) error {
	slog.InfoContext(ctx, "restarting the radosgw, which started before its option changed",
		slog.String("entity", rgw.entity), slog.String("start_stamp", rgw.StartStamp))
	if err := c.kubectlLong(ctx, "-n", namespace, "rollout", "restart", "deploy", "-l", rgwSelector); err != nil {
		return fmt.Errorf("restarting the radosgw: %w", err)
	}
	if err := c.kubectlLong(ctx, "-n", namespace, "rollout", "status", "deploy", "-l", rgwSelector, "--timeout=5m"); err != nil {
		return fmt.Errorf("waiting for the radosgw's restart: %w", err)
	}
	return c.poll(ctx, c.SiteWait, func() (bool, error) {
		next, err := c.radosgw(ctx)
		if err != nil {
			return true, err
		}
		*rgw = next
		stale, err := c.stale(ctx, next)
		return err != nil || !stale, err
	}, func(error) error {
		return fmt.Errorf("the radosgw in the service map started before its %s changed, %ds after its restart: %s",
			lcOption, seconds(c.SiteWait), rgw.Raw)
	})
}

// stale tells whether rgw_enable_lc_threads changed, in a section rgw's
// daemon reads, after that daemon started.
func (c *Configurer) stale(ctx context.Context, rgw radosgw) (bool, error) {
	start, err := stampTime(rgw.StartStamp)
	if err != nil {
		return false, fmt.Errorf("the radosgw's start_stamp: %w", err)
	}
	out, err := c.Cluster.Ceph(ctx, "config", "log", strconv.Itoa(configLogWindow), "-f", "json")
	if err != nil {
		return false, fmt.Errorf("reading the config log: %w", err)
	}
	var log []changeSet
	if err := json.Unmarshal(out, &log); err != nil {
		return false, fmt.Errorf("decoding the config log: %w", err)
	}
	return changedSince(log, configLogWindow, lcOption, rgw.entity, start)
}

// changeSet is an entry of ceph config log -f json: when the mons made it,
// and the config keys it changed.
type changeSet struct {
	Timestamp string   `json:"timestamp"`
	Changes   []change `json:"changes"`
}

// change is a config key a changeset changed.
type change struct {
	Name string `json:"name"`
}

// changedSince reports whether log, the last window entries of the mons'
// config log, holds a change of option in a section entity reads after
// start. A log that holds window entries, every one after start, counts as
// such a change too: one older than the log reaches back to may follow
// start.
func changedSince(log []changeSet, window int, option, entity string, start time.Time) (bool, error) {
	after := 0
	for _, cs := range log {
		at, err := stampTime(cs.Timestamp)
		if err != nil {
			return false, fmt.Errorf("the config log: %w", err)
		}
		if !at.After(start) {
			continue
		}
		after++
		if slices.ContainsFunc(cs.Changes, func(ch change) bool { return reads(ch.Name, option, entity) }) {
			return true, nil
		}
	}
	return len(log) >= window && after == len(log), nil
}

// reads reports whether the config key name sets option for entity. A key
// is its sections and masks, then the option, joined by "/"
// (https://github.com/ceph/ceph/blob/v19.2.7/src/mon/ConfigMap.cc#L200-L238 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/mon/ConfigMap.cc#L203-L242 at v20.2.4),
// and a daemon reads global, its type, and each dotted prefix of its name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/mon/ConfigMap.cc#L137-L198 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/mon/ConfigMap.cc#L140-L201 at v20.2.4).
// A masked key counts whatever its mask, which may match the daemon.
func reads(name, option, entity string) bool {
	parts := strings.Split(name, "/")
	if parts[len(parts)-1] != option {
		return false
	}
	section := "global"
	for _, p := range parts[:len(parts)-1] {
		switch {
		case p == "global":
			section = "global"
		case strings.Contains(p, ":"):
		default:
			section = p
		}
	}
	return section == "global" || section == entity || strings.HasPrefix(entity, section+".")
}

// stampPattern is a utime_t as the mons and the manager print a time: the
// local time with its offset, which is UTC in Ceph's images.
var stampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{4})$`)

// stampTime parses a utime_t as the mons and the manager print one: the
// local time with its %z offset, as 2026-10-08T21:58:20.402329+0000, or, for
// a time within ten years of the epoch, which the mons' first changeset
// carries, as seconds, 0.000000
// (https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L334-L368 at v19.2.7
// and v20.2.4).
func stampTime(s string) (time.Time, error) {
	if secs, frac, ok := strings.Cut(s, "."); ok && isDigits(secs) && isDigits(frac) && len(frac) <= 9 {
		sec, serr := strconv.ParseInt(secs, 10, 64)
		nsec, nerr := strconv.ParseInt(frac+strings.Repeat("0", 9-len(frac)), 10, 64)
		if err := errors.Join(serr, nerr); err != nil {
			return time.Time{}, fmt.Errorf("unparsed timestamp %q: %w", s, err)
		}
		return time.Unix(sec, nsec).UTC(), nil
	}
	if !stampPattern.MatchString(s) {
		return time.Time{}, fmt.Errorf("unparsed timestamp %q", s)
	}
	t, err := time.Parse("2006-01-02T15:04:05.999999999Z0700", s)
	if err != nil {
		return time.Time{}, errors.Join(fmt.Errorf("unparsed timestamp %q", s), err)
	}
	return t, nil
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
