package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// serviceMapService is the service radosgw registers its gateway under; an
// NFS gateway's, rgw-nfs, is not rgw-go's.
const serviceMapService = "rgw"

// serviceIdentity is what radosgw builds its service map metadata from.
type serviceIdentity struct {
	// name is the entity name, TYPE.ID.
	name            string
	serviceUniqueID string
	zone            meta.Zone
	zoneGroup       meta.ZoneGroup
	realm           meta.Realm
	// frontends is rgw_frontends as serve read it.
	frontends string
	pid       int
}

// serviceMetadata is the metadata radosgw registers in the manager's
// service map, beside the keys librados adds itself: its pid and its
// frontends from init_frontends2
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L421 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L456-L514 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L429 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L464-L522 at v20.2.4),
// then the keys register_to_service_map adds: the entity's id less a leading
// "rgw.", num_handles 1, the zonegroup's, zone's and realm's ids and names,
// empty without a realm, and service_unique_id only when it is set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1155-L1184 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1192-L1221 at v20.2.4).
func serviceMetadata(id serviceIdentity) map[string]string {
	md := map[string]string{
		"pid":            strconv.Itoa(id.pid),
		"num_handles":    "1",
		"zonegroup_id":   id.zoneGroup.ID,
		"zonegroup_name": id.zoneGroup.Name,
		"zone_name":      id.zone.Name,
		"zone_id":        id.zone.ID,
		"realm_name":     id.realm.Name,
		"realm_id":       id.realm.ID,
		"id":             daemonID(id.name),
	}
	if id.serviceUniqueID != "" {
		md["service_unique_id"] = id.serviceUniqueID
	}
	for n, fe := range frontendEntries(id.frontends) {
		// A build without Arrow Flight skips the entry after counting it,
		// and the release builds leave Arrow Flight out
		// (https://github.com/ceph/ceph/blob/v19.2.7/CMakeLists.txt#L492 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/CMakeLists.txt#L535 at v20.2.4).
		if fe.framework == "arrow_flight" {
			continue
		}
		md["frontend_type#"+strconv.Itoa(n)] = fe.framework
		md["frontend_config#"+strconv.Itoa(n)] = fe.config
	}
	return md
}

// daemonID is the ID of the entity name TYPE.ID, less a leading "rgw.".
func daemonID(name string) string {
	_, id, ok := strings.Cut(name, ".")
	if !ok {
		id = name
	}
	return strings.TrimPrefix(id, "rgw.")
}

// frontendEntry is one entry of rgw_frontends: its framework, and the entry
// as given.
type frontendEntry struct {
	framework, config string
}

// frontendEntries are rgw_frontends' entries in the order radosgw walks
// them. It splits the value at commas, dropping empty pieces, as get_str_vec
// does, with "beast" for none; an entry's framework is its first
// space-separated word, empty for an entry of spaces alone, as
// RGWFrontendConfig::parse_config takes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_frontend.cc#L17-L48 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_frontend.cc#L17-L48 at v20.2.4).
// radosgw keeps them in a std::multimap keyed by the framework, which sorts
// them bytewise by framework and keeps the given order within one
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L96-L148 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L98-L150 at v20.2.4).
func frontendEntries(value string) []frontendEntry {
	var out []frontendEntry
	for piece := range strings.SplitSeq(value, ",") {
		if piece == "" {
			continue
		}
		fe := frontendEntry{config: piece}
		for word := range strings.SplitSeq(piece, " ") {
			if word != "" {
				fe.framework = word
				break
			}
		}
		out = append(out, fe)
	}
	if len(out) == 0 {
		out = []frontendEntry{{framework: "beast", config: "beast"}}
	}
	slices.SortStableFunc(out, func(a, b frontendEntry) int { return strings.Compare(a.framework, b.framework) })
	return out
}

// serviceDaemon is the daemon name radosgw registers under: its RADOS
// client's instance id in decimal.
func serviceDaemon(c radosclient.Cluster) string {
	return strconv.FormatUint(c.InstanceID(), 10)
}

// serviceZone is the zone, zonegroup and realm a gateway registers with.
type serviceZone interface {
	Zone() meta.Zone
	ZoneGroup() meta.ZoneGroup
	Realm() meta.Realm
}

// serviceRegistration is what serve registers in the manager's service map:
// when on, c as a ServiceRegistrar and the metadata of the gateway that runs
// as entity name in zi's zone with frontends, rgw_frontends as serve read
// it. When off, or when c cannot register, which it logs, the registrar is
// nil and the gateway serves without one.
func serviceRegistration(ctx context.Context, on bool, c radosclient.Cluster, conf *cephconf.Options, zi serviceZone,
	name, frontends string,
) (radosclient.ServiceRegistrar, map[string]string, error) {
	if !on {
		return nil, nil, nil
	}
	r, ok := c.(radosclient.ServiceRegistrar)
	if !ok {
		slog.WarnContext(ctx, "the rados client cannot register in the manager's service map", slog.String("client", fmt.Sprintf("%T", c)))
		return nil, nil, nil
	}
	uniqueID, err := conf.String("service_unique_id")
	if err != nil {
		return nil, nil, fmt.Errorf("reading service_unique_id: %w", err)
	}
	return r, serviceMetadata(serviceIdentity{
		name:            name,
		serviceUniqueID: uniqueID,
		zone:            zi.Zone(),
		zoneGroup:       zi.ZoneGroup(),
		realm:           zi.Realm(),
		frontends:       frontends,
		pid:             os.Getpid(),
	}), nil
}

// serviceMap is serve's registration in the manager's service map.
type serviceMap struct {
	// registrar is nil when serve does not register.
	registrar radosclient.ServiceRegistrar
	// daemon is the RADOS client's instance id in decimal, the daemon name
	// radosgw registers under.
	daemon   string
	metadata map[string]string
	// interval reads rgw_sync_trace_servicemap_update_interval.
	interval func() (int64, error)
	// after is time.After, which specs replace.
	after func(time.Duration) <-chan time.Time
}

// register registers the gateway, and reports whether it did. A failure is
// logged and the gateway serves on, as radosgw ignores it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L516-L521 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L524-L529 at v20.2.4).
func (m serviceMap) register(ctx context.Context) bool {
	if m.registrar == nil {
		return false
	}
	if err := m.registrar.ServiceRegister(serviceMapService, m.daemon, m.metadata); err != nil {
		slog.ErrorContext(ctx, "registering in the manager's service map; serving without it",
			slog.String("service", serviceMapService), slog.String("daemon", m.daemon), slog.Any("error", err))
		return false
	}
	slog.InfoContext(ctx, "registered in the manager's service map",
		slog.String("service", serviceMapService), slog.String("daemon", m.daemon))
	return true
}

// syncStatus is the status radosgw's sync trace reports: the names of its
// active syncs, as a JSON array, which a single zone, all rgw-go serves, has
// none of.
var syncStatus = map[string]string{"current_sync": "[]"}

// report sends the status at once, then again each time
// rgw_sync_trace_servicemap_update_interval seconds pass, reading the
// option again each round, until ctx ends, as radosgw's sync trace thread
// does; an interval of 0 or less sends no more, as that thread then waits
// for its shutdown
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L327-L361 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L362-L396 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_trace.cc#L74-L83 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sync_trace.cc#L74-L83 at v20.2.4).
// A failed send is logged, as the thread logs it, and the next is made. It
// returns nil, so it never stops the gateway: when ctx ends, or after
// logging an interval it cannot read, which leaves it waiting for ctx.
func (m serviceMap) report(ctx context.Context) error {
	for {
		if err := m.registrar.ServiceUpdateStatus(syncStatus); err != nil {
			slog.ErrorContext(ctx, "updating the gateway's status in the manager's service map",
				slog.String("daemon", m.daemon), slog.Any("error", err))
		}
		secs, err := m.interval()
		if err != nil {
			slog.ErrorContext(ctx, "reading rgw_sync_trace_servicemap_update_interval; sending no more status",
				slog.Any("error", err))
			secs = 0
		}
		if secs <= 0 {
			<-ctx.Done()
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.after(time.Duration(secs) * time.Second):
		}
	}
}
