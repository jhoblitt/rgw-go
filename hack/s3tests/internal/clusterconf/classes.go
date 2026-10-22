package clusterconf

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// Classes are the storage classes the s3-tests conf names. s3-tests adds
// STANDARD to them itself, and its transition tests skip below three
// classes. Ceph's lifecycle suite names the same two
// (https://github.com/ceph/ceph/blob/v19.2.7/qa/suites/rgw/lifecycle/overrides.yaml#L13-L15 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/qa/suites/rgw/lifecycle/overrides.yaml#L13-L17 at v20.2.4).
var Classes = []string{"LUKEWARM", "FROZEN"}

// ConfValue is the classes as the conf's storage_classes takes them, which
// s3-tests splits at anything but a word character.
func ConfValue() string { return strings.Join(Classes, ", ") }

// placementTarget is a zonegroup placement target, as radosgw-admin prints
// it.
type placementTarget struct {
	Name           string   `json:"name"`
	StorageClasses []string `json:"storage_classes"`
}

// zonegroupDoc is what ensureClasses reads of a zonegroup.
type zonegroupDoc struct {
	Name             string            `json:"name"`
	DefaultPlacement string            `json:"default_placement"`
	PlacementTargets []placementTarget `json:"placement_targets"`
}

// lists reports whether zg's placement target named placement lists class.
func (zg zonegroupDoc) lists(placement, class string) bool {
	return slices.ContainsFunc(zg.PlacementTargets, func(t placementTarget) bool {
		return t.Name == placement && slices.Contains(t.StorageClasses, class)
	})
}

// zoneClass is a storage class of a zone's placement.
type zoneClass struct {
	DataPool        string  `json:"data_pool"`
	CompressionType *string `json:"compression_type"`
}

// zoneDoc is what ensureClasses reads of a zone.
type zoneDoc struct {
	PlacementPools []struct {
		Key string `json:"key"`
		Val struct {
			StorageClasses map[string]zoneClass `json:"storage_classes"`
		} `json:"val"`
	} `json:"placement_pools"`
}

// classes are the storage classes of the zone's placement, nil when it has
// none by that name.
func (z zoneDoc) classes(placement string) map[string]zoneClass {
	for _, p := range z.PlacementPools {
		if p.Key == placement {
			return p.Val.StorageClasses
		}
	}
	return nil
}

// periodDoc is what ensureClasses reads of the current period.
type periodDoc struct {
	PeriodMap struct {
		ZoneGroups []zonegroupDoc `json:"zonegroups"`
	} `json:"period_map"`
}

// ensureClasses adds Classes to the default placement of the radosgw's
// zonegroup and zone, each on the STANDARD class's data pool, uncompressed,
// as populate.sh adds its compressing classes: with the Rook operator's
// radosgw-admin, so that the zonegroup and zone stay in the encoding Rook
// writes them in, and the zonegroup's placement target first, since
// radosgw-admin adds a class to the zone only once the target lists it. It
// commits the realm's period when it added one, or when the period lacks
// one, as a run that stopped before its commit leaves it, and waits for the
// radosgw to reload it. It returns the placement, the data pool, and whether
// it changed the period.
func (c *Configurer) ensureClasses(ctx context.Context, rgw *radosgw) (placement, pool string, added bool, _ error) {
	var zg zonegroupDoc
	if err := c.adminJSON(ctx, *rgw, &zg, "zonegroup", "get"); err != nil {
		return "", "", false, err
	}
	if placement = zg.DefaultPlacement; placement == "" {
		return "", "", false, fmt.Errorf("zonegroup %s has no default placement", rgw.zonegroup)
	}
	var zone zoneDoc
	if err := c.adminJSON(ctx, *rgw, &zone, "zone", "get"); err != nil {
		return "", "", false, err
	}
	have := zone.classes(placement)
	if pool = have["STANDARD"].DataPool; pool == "" {
		return "", "", false, fmt.Errorf("zone %s has no STANDARD data pool in placement %s", rgw.zone, placement)
	}
	var changed bool
	for _, class := range Classes {
		if !zg.lists(placement, class) {
			if err := c.operatorAdmin(ctx, *rgw, "zonegroup", "placement", "add", "--placement-id", placement,
				"--storage-class", class); err != nil {
				return "", "", false, err
			}
			changed = true
		}
		if _, ok := have[class]; !ok {
			if err := c.operatorAdmin(ctx, *rgw, "zone", "placement", "add", "--placement-id", placement,
				"--storage-class", class, "--data-pool", pool); err != nil {
				return "", "", false, err
			}
			changed = true
		}
	}
	listed, err := c.periodLists(ctx, *rgw, placement)
	if err != nil {
		return "", "", false, err
	}
	if changed || !listed {
		slog.InfoContext(ctx, "committing the period with the storage classes", slog.String("classes", ConfValue()),
			slog.String("placement", placement), slog.String("data_pool", pool))
		err = c.operatorAdmin(ctx, *rgw, "period", "update", "--commit")
		if err != nil {
			return "", "", false, err
		}
		err = c.waitReload(ctx, rgw)
		if err != nil {
			return "", "", false, err
		}
		added = true
		if listed, err = c.periodLists(ctx, *rgw, placement); err != nil {
			return "", "", false, err
		}
		if !listed {
			return "", "", false, fmt.Errorf("the current period's zonegroup %s does not list the storage classes %s",
				rgw.zonegroup, ConfValue())
		}
	}
	if err := c.adminJSON(ctx, *rgw, &zone, "zone", "get"); err != nil {
		return "", "", false, err
	}
	have = zone.classes(placement)
	for _, class := range Classes {
		got, ok := have[class]
		if !ok || got.DataPool != pool || (got.CompressionType != nil && *got.CompressionType != "") {
			return "", "", false, fmt.Errorf("zone %s does not keep the storage class %s uncompressed on %s: %+v",
				rgw.zone, class, pool, have[class])
		}
	}
	return placement, pool, added, nil
}

// periodLists reports whether the current period's map gives the radosgw's
// zonegroup's placement every class of Classes.
func (c *Configurer) periodLists(ctx context.Context, rgw radosgw, placement string) (bool, error) {
	var p periodDoc
	if err := c.adminJSON(ctx, rgw, &p, "period", "get"); err != nil {
		return false, err
	}
	for _, zg := range p.PeriodMap.ZoneGroups {
		if zg.Name == rgw.zonegroup {
			return !slices.ContainsFunc(Classes, func(class string) bool { return !zg.lists(placement, class) }), nil
		}
	}
	return false, nil
}

// waitReload waits for the radosgw to reload the period just committed: it
// pauses its frontends, replaces its store, and registers it with the
// manager under a new gid, as populate.sh waits for it. A reload that has
// not finished by SiteWait has hung, and a restart of the radosgw's pod has
// ended such a hang (docs/ceph-upstream-bugs.md).
func (c *Configurer) waitReload(ctx context.Context, rgw *radosgw) error {
	err := c.poll(ctx, c.SiteWait, func() (bool, error) {
		out, err := c.Cluster.Ceph(ctx, "service", "dump", "-f", "json")
		if err != nil {
			return false, err
		}
		entries, err := harness.RadosgwEntries(out)
		if err != nil {
			return false, err
		}
		return slices.ContainsFunc(entries, func(e harness.Radosgw) bool { return e.GID != rgw.GID }), nil
	}, func(last error) error {
		if last != nil {
			return fmt.Errorf("the radosgw's reload of the committed period could not be seen in %ds: "+
				"ceph service dump failed, so the service map could not be read: %w", seconds(c.SiteWait), last)
		}
		return fmt.Errorf("the radosgw did not finish reloading the committed period in %ds; restart it with "+
			"'ROOKET_NAME=rgw-go-%s %s kubectl -n %s delete pod -l %s' and rerun once the service map lists it alone",
			seconds(c.SiteWait), c.Cluster.Release, c.Cluster.Rooket, namespace, rgwSelector)
	})
	if err != nil {
		return err
	}
	next, err := c.radosgw(ctx)
	if err != nil {
		return err
	}
	*rgw = next
	return nil
}

// siteArgs name the radosgw's site: without them radosgw-admin works in a
// zone named default, which it creates on first use and the radosgw never
// serves.
func (rgw radosgw) siteArgs() []string {
	return []string{"--rgw-realm=" + rgw.realm, "--rgw-zonegroup=" + rgw.zonegroup, "--rgw-zone=" + rgw.zone}
}

// adminJSON runs radosgw-admin in the toolbox on the radosgw's site and
// decodes its output into v.
func (c *Configurer) adminJSON(ctx context.Context, rgw radosgw, v any, args ...string) error {
	out, err := c.Cluster.Toolbox(ctx, slices.Concat([]string{"radosgw-admin"}, args, rgw.siteArgs())...)
	if err != nil {
		return fmt.Errorf("radosgw-admin %s: %w", strings.Join(args, " "), err)
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("decoding radosgw-admin %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// operatorAdmin runs radosgw-admin as Rook's operator does, in the
// operator's pod with the config and keyring it keeps for the cluster.
// Rook's operator writes the realm, zonegroup, zone and periods with the
// radosgw-admin of its own image, so changing them with it leaves them in
// the encoding Rook would.
func (c *Configurer) operatorAdmin(ctx context.Context, rgw radosgw, args ...string) error {
	_, err := c.Cluster.Kubectl(ctx, slices.Concat(
		[]string{"-n", namespace, "exec", "deploy/rook-ceph-operator", "--", "radosgw-admin"}, args, rgw.siteArgs(),
		[]string{
			"--cluster=rook-ceph", "--conf=/var/lib/rook/rook-ceph/rook-ceph.config",
			"--name=client.admin", "--keyring=/var/lib/rook/rook-ceph/client.admin.keyring",
		})...)
	if err != nil {
		return fmt.Errorf("the operator's radosgw-admin %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
