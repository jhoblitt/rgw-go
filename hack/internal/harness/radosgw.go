package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Poll bounds a wait on a cluster: it tries at once, then every Every,
// until Wait has passed on Now's clock, waiting through Sleep.
type Poll struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Wait  time.Duration
	Every time.Duration
}

// Until calls try until it reports done, and returns its error then. Once
// Wait has passed it returns what timeout makes of try's last error, which
// is nil when try last failed without one.
func (p Poll) Until(ctx context.Context, try func() (bool, error), timeout func(last error) error) error {
	deadline := p.Now().Add(p.Wait)
	for {
		done, err := try()
		if done {
			return err
		}
		if !p.Now().Before(deadline) {
			return timeout(err)
		}
		if err := p.Sleep(ctx, p.Every); err != nil {
			return err
		}
	}
}

// Radosgw is a radosgw's entry in Ceph's service map: the realm, zonegroup
// and zone it serves, its id and its frontends are in its metadata. A
// radosgw registers under a new gid, its librados instance id, with a new
// start_stamp, each time it starts or reloads its realm, once it has built
// its store.
type Radosgw struct {
	GID        int64             `json:"gid"`
	StartStamp string            `json:"start_stamp"`
	Metadata   map[string]string `json:"metadata"`
	// Raw is the entry as the service map gives it, compacted, for messages.
	Raw string `json:"-"`
}

// RadosgwEntries are the radosgw entries of ceph service dump -f json's
// output, in the order of their gids; its summary is none.
func RadosgwEntries(dump []byte) ([]Radosgw, error) {
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
	entries := []Radosgw{}
	for _, gid := range slices.Sorted(maps.Keys(sm.Services.RGW.Daemons)) {
		if gid == "summary" {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, sm.Services.RGW.Daemons[gid]); err != nil {
			return nil, fmt.Errorf("decoding the service map entry %s: %w", gid, err)
		}
		var rgw Radosgw
		if err := json.Unmarshal(compact.Bytes(), &rgw); err != nil {
			return nil, fmt.Errorf("decoding the service map entry %s: %w", gid, err)
		}
		rgw.Raw = compact.String()
		entries = append(entries, rgw)
	}
	return entries, nil
}

// OneRadosgw polls the service map until it lists exactly one radosgw, and
// returns it. The manager drops a radosgw's old gid as soon as that
// instance closes its session, but lists one that ends without closing it
// until it has sent no beacon for mgr_service_beacon_grace, 60 s, so after a
// restart or a reload the map can list both, or the old one alone, for
// about a minute. Once p's Wait has passed it fails, never restarting a
// radosgw itself.
func (c Cluster) OneRadosgw(ctx context.Context, p Poll) (Radosgw, error) {
	var entries []Radosgw
	err := p.Until(ctx, func() (bool, error) {
		out, err := c.Ceph(ctx, "service", "dump", "-f", "json")
		if err == nil {
			entries, err = RadosgwEntries(out)
		}
		return err == nil && len(entries) == 1, err
	}, func(last error) error {
		if last != nil {
			return fmt.Errorf("the service map did not settle to one radosgw in %ds: "+
				"ceph service dump failed, so the service map could not be read: %w", seconds(p.Wait), last)
		}
		raws := make([]json.RawMessage, 0, len(entries))
		for _, e := range entries {
			raws = append(raws, json.RawMessage(e.Raw))
		}
		listed, err := json.Marshal(raws)
		if err != nil {
			return fmt.Errorf("encoding the radosgw entries: %w", err)
		}
		return fmt.Errorf("the service map did not settle to one radosgw in %ds (radosgw entries: %s); "+
			"the manager drops a stopped radosgw within about a minute, so none is running or more than one is",
			seconds(p.Wait), listed)
	})
	if err != nil {
		return Radosgw{}, err
	}
	return entries[0], nil
}

// Site is the realm, zonegroup and zone the radosgw serves, which
// radosgw-admin must name: without them it works in a zone named default,
// which it creates on first use and the radosgw never serves.
func (r Radosgw) Site() (realm, zonegroup, zone string, err error) {
	realm, zonegroup, zone = r.Metadata["realm_name"], r.Metadata["zonegroup_name"], r.Metadata["zone_name"]
	if realm == "" || zonegroup == "" || zone == "" {
		return "", "", "", fmt.Errorf("the radosgw reports no realm, zonegroup or zone: %s", r.Raw)
	}
	return realm, zonegroup, zone, nil
}

// Frontends is the frontend the radosgw reports serving.
func (r Radosgw) Frontends() string { return r.Metadata["frontend_config#0"] }

// RadosgwEntity is rgw's config entity. Rook runs the radosgw as client.rgw.
// and its deployment's name past rook-ceph-rgw, dashes mapped to dots
// (generateCephXUser in
// https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/config.go#L161-L164),
// and the service map's id is that name past client.rgw. config show
// answers only for a section a running daemon reads, with the frontends
// that daemon reports, so a wrong name stops here rather than reading or
// setting options nothing reads.
func (c Cluster) RadosgwEntity(ctx context.Context, rgw Radosgw) (string, error) {
	id := rgw.Metadata["id"]
	if id == "" {
		return "", fmt.Errorf("the radosgw reports no id: %s", rgw.Raw)
	}
	entity := "client.rgw." + id
	out, err := c.Ceph(ctx, "config", "show", entity, "rgw_frontends")
	if err != nil {
		return "", fmt.Errorf("no running daemon reads the config section %s: %w", entity, err)
	}
	if frontends := strings.TrimRight(string(out), "\n"); frontends == "" || frontends != rgw.Frontends() {
		return "", fmt.Errorf("%s reports the frontends '%s', the radosgw '%s'", entity, frontends, rgw.Frontends())
	}
	return entity, nil
}

var pidPattern = regexp.MustCompile(`^\d+$`)

// RgwGoPID is the pid pidFile names when its process is rgw-go, as
// hack/rooket/rgw-go-up.sh writes it, and empty otherwise: a pid outlives
// its process and can be reused, so only one whose process, read from proc,
// is named rgw-go counts.
func RgwGoPID(proc fs.FS, pidFile string) string {
	data, err := os.ReadFile(pidFile) //nolint:gosec // the release's rooket output
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(string(data))
	if !pidPattern.MatchString(pid) {
		return ""
	}
	comm, err := fs.ReadFile(proc, pid+"/comm")
	if err != nil || strings.TrimRight(string(comm), "\n") != "rgw-go" {
		return ""
	}
	return pid
}

// seconds is d in whole seconds, for messages.
func seconds(d time.Duration) int64 { return int64(d / time.Second) }
