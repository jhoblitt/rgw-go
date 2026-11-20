//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"

	"github.com/jhoblitt/rgw-go/internal/frontend"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

const (
	// serviceMapEntity is the cephx entity the service map spec's rgw-go
	// runs as. The manager's config show finds an rgw daemon only by a name
	// that holds "rgw" past its type, matching the rest past the first dot
	// to the id the daemon registered
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L2093-L2133 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L2572-L2612 at v20.2.4),
	// and client.rgw.rgw-go's config section is rgw-go-up's.
	serviceMapEntity = "client.rgw.rgw-go-servicemap"
	// serviceMapID is the id radosgw registers for serviceMapEntity: its
	// name's id less a leading "rgw."
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1155-L1184 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1192-L1221 at v20.2.4).
	serviceMapID       = "rgw-go-servicemap"
	serviceMapUniqueID = "rgw-go-servicemap-spec"

	// serviceCloseTimeout bounds the wait, from rgw-go's exit, for its
	// registration to leave the map. The close librados sends as it shuts
	// down removes the entry from the manager's pending map at once
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/MgrClient.cc#L67-L101 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/MgrClient.cc#L68-L102 at v20.2.4;
	// https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L686-L710 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L690-L714 at v20.2.4),
	// and the map service dump reads follows on the manager's next report
	// to the mon, each mgr_tick_period, 2 s
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/mgr.yaml.in#L410-L419 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/mgr.yaml.in#L439-L448 at v20.2.4),
	// and the mon's commit. It is goceph's bound for the same close, and under
	// mgr_service_beacon_grace, 60 s
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/mgr.yaml.in#L113-L118 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/mgr.yaml.in#L113-L118 at v20.2.4),
	// after which the manager drops a silent daemon anyway, so only the close
	// removes the registration within it.
	serviceCloseTimeout = 30 * time.Second
	// serviceReregisteredTimeout bounds the wait, from rgw-go's exit, for an
	// entry librados registered again as it shut down to leave the map
	// (docs/ceph-upstream-bugs.md, "librados registers a service daemon
	// again while it shuts down"). No close follows that registration and it
	// sends no beacon, so the manager's first prune after it stamps its
	// beacon and a prune mgr_service_beacon_grace, 60 s, later drops it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L2677-L2711 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L3156-L3190 at v20.2.4),
	// both on the manager's 2 s tick, then the mon publishes the map: about
	// 65 s, and 90 s leaves margin.
	serviceReregisteredTimeout = 90 * time.Second
	// serviceSettle is how long the map must list nothing under rgw-go's key
	// before the spec takes the entry as gone. The close and the
	// registration librados can send after it reach the manager up to 1 s
	// apart, so a map published between them lists neither, and the next,
	// one 2 s tick later, lists the new one; five ticks leave margin.
	serviceSettle = 10 * time.Second
)

// podKeys are the metadata keys librados takes from the container a daemon
// runs in
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/util.cc#L285-L308 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/util.cc#L285-L308 at v20.2.4)
// and from its cgroup's memory limit
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/util.cc#L354-L358 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/util.cc#L354-L358 at v20.2.4):
// Rook's radosgw has them and a host rgw-go need not.
var podKeys = []string{"pod_name", "pod_namespace", "container_name", "container_image", "container_hostname", "mem_cgroup_limit"}

// sameAsRadosgw are the metadata keys of the site and the frontend whose
// values a gateway on the radosgw's site with one beast frontend shares with
// it.
var sameAsRadosgw = []string{
	"realm_name", "realm_id", "zonegroup_name", "zonegroup_id", "zone_name", "zone_id",
	"num_handles", "frontend_type#0",
}

// cephBuild is the form of the ceph_version librados registers.
//
// It is pretty_version_to_str's: the version, the commit, then the release's
// name and type
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/version.cc#L46-L54 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/version.cc#L46-L59 at v20.2.4).
var cephBuild = regexp.MustCompile(`^ceph version \S+ \([0-9a-f]{40}\) [a-z]+ \(.+\)$`)

// frontendKey is a key radosgw registers for one of its frontends.
var frontendKey = regexp.MustCompile(`^frontend_(type|config)#[0-9]+$`)

// serviceMapEntry is one daemon of the rgw service in ceph service dump -f
// json's output.
type serviceMapEntry struct {
	// Key is the daemon's name in the map.
	Key      string            `json:"-"`
	GID      uint64            `json:"gid"`
	Metadata map[string]string `json:"metadata"`
	// StartEpoch and StartStamp are the map's epoch and the manager's clock
	// when it last registered the daemon.
	StartEpoch uint64 `json:"start_epoch"`
	StartStamp string `json:"start_stamp"`
	// Raw is the entry as the map gives it, compacted, for messages.
	Raw string `json:"-"`
}

// inPod tells whether e is a daemon Rook runs: Rook sets POD_NAME on every
// one (https://github.com/rook/rook/blob/v1.21.1/pkg/operator/k8sutil/pod.go#L223-L226),
// librados reports it as pod_name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/util.cc#L288-L291 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/util.cc#L288-L291 at v20.2.4),
// and every rgw-go the specs start runs outside a pod. It is the test the
// harness's radosgw readers make (harness.RadosgwEntries), which this
// package cannot import.
func (e serviceMapEntry) inPod() bool {
	_, ok := e.Metadata["pod_name"]
	return ok
}

// rgwServiceEntries are the rgw daemons the service map lists, in the order
// of their keys; the service's summary is none.
func rgwServiceEntries(ctx context.Context, g Gomega) []serviceMapEntry {
	out, err := cephOut(ctx, "service", "dump", "-f", "json")
	g.Expect(err).NotTo(HaveOccurred())
	var sm struct {
		Services struct {
			RGW struct {
				Daemons map[string]json.RawMessage `json:"daemons"`
			} `json:"rgw"`
		} `json:"services"`
	}
	g.Expect(json.Unmarshal([]byte(out), &sm)).To(Succeed(), "decoding ceph service dump: %s", out)
	var entries []serviceMapEntry
	for _, key := range slices.Sorted(maps.Keys(sm.Services.RGW.Daemons)) {
		if key == "summary" {
			continue
		}
		var compact bytes.Buffer
		g.Expect(json.Compact(&compact, sm.Services.RGW.Daemons[key])).To(Succeed(), "the rgw entry %s", key)
		e := serviceMapEntry{Key: key, Raw: compact.String()}
		g.Expect(json.Unmarshal(compact.Bytes(), &e)).To(Succeed(), "the rgw entry %s", key)
		entries = append(entries, e)
	}
	return entries
}

// onHost picks the entry of the host process with pid pid. It must lie
// outside a pod too: a radosgw's pid is its container's, which can equal any
// host process's.
func onHost(pid int) func(serviceMapEntry) bool {
	return func(e serviceMapEntry) bool {
		return !e.inPod() && e.Metadata["pid"] == strconv.Itoa(pid)
	}
}

// rawEntries are the raw entries of entries that match picks.
func rawEntries(entries []serviceMapEntry, match func(serviceMapEntry) bool) []string {
	var raws []string
	for _, e := range entries {
		if match(e) {
			raws = append(raws, e.Raw)
		}
	}
	return raws
}

// entryUnder is the entry under key, nil when the map lists none.
func entryUnder(entries []serviceMapEntry, key string) *serviceMapEntry {
	for _, e := range entries {
		if e.Key == key {
			return &e
		}
	}
	return nil
}

// registration names e's registration, or none.
func registration(e *serviceMapEntry) string {
	if e == nil {
		return "no entry"
	}
	return fmt.Sprintf("start_epoch %d, start_stamp %s", e.StartEpoch, e.StartStamp)
}

// sameRegistration tells whether a and b are one registration: the manager
// stamps an entry anew whenever it adds it.
func sameRegistration(a, b serviceMapEntry) bool {
	return a.StartEpoch == b.StartEpoch && a.StartStamp == b.StartStamp
}

// rawOf is e as the map gave it, or none.
func rawOf(e *serviceMapEntry) string {
	if e == nil {
		return "no entry"
	}
	return e.Raw
}

// onlyEntry is the one entry of entries that match picks; what names it.
func onlyEntry(g Gomega, entries []serviceMapEntry, what string, match func(serviceMapEntry) bool) serviceMapEntry {
	var picked []serviceMapEntry
	for _, e := range entries {
		if match(e) {
			picked = append(picked, e)
		}
	}
	all := rawEntries(entries, func(serviceMapEntry) bool { return true })
	g.Expect(picked).To(HaveLen(1), "%s among the rgw entries:\n%s", what, strings.Join(all, "\n"))
	return picked[0]
}

// metadataDiffs are the differences between the metadata rgw-go registered
// and radosgw's, one a line, empty when there are none. The radosgw serves
// site, the populated realm, zonegroup and zone by their name keys. Once the
// keys the pod supplies are left out of both, the two hold the same keys,
// the site's and the frontend's values are radosgw's, and rgw-go's id,
// unique id and frontend are the ones it was started with. Each gateway runs
// one frontend, so each registers frontend_type#0 and frontend_config#0 and
// no other frontend key.
func metadataDiffs(radosgw, rgwgo map[string]string, beast string, site map[string]string) []string {
	var diffs []string
	for _, k := range slices.Sorted(maps.Keys(site)) {
		if v, ok := radosgw[k]; !ok || v != site[k] {
			diffs = append(diffs, fmt.Sprintf("%s: radosgw registers %q, where the populated site's is %q", k, v, site[k]))
		}
	}
	theirs, ours := maps.Clone(radosgw), maps.Clone(rgwgo)
	for _, k := range podKeys {
		delete(theirs, k)
		delete(ours, k)
	}
	for _, k := range slices.Sorted(maps.Keys(theirs)) {
		if _, ok := ours[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s: radosgw registers it as %q, rgw-go does not register it", k, theirs[k]))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(ours)) {
		if _, ok := theirs[k]; !ok {
			diffs = append(diffs, fmt.Sprintf("%s: rgw-go registers it as %q, radosgw does not register it", k, ours[k]))
		}
	}
	shown := func(md map[string]string, k string) string {
		if v, ok := md[k]; ok {
			return strconv.Quote(v)
		}
		return "nothing"
	}
	for _, k := range sameAsRadosgw {
		if v, ok := theirs[k]; !ok || ours[k] != v {
			diffs = append(diffs, fmt.Sprintf("%s: rgw-go registers %s, radosgw %s", k, shown(ours, k), shown(theirs, k)))
		}
	}
	want := map[string]string{"id": serviceMapID, "service_unique_id": serviceMapUniqueID, "frontend_config#0": beast}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if v, ok := ours[k]; !ok || v != want[k] {
			diffs = append(diffs, fmt.Sprintf("%s: rgw-go registers %s, where it was started with %q", k, shown(ours, k), want[k]))
		}
	}
	if !cephBuild.MatchString(ours["ceph_version"]) {
		diffs = append(diffs, fmt.Sprintf("ceph_version: rgw-go registers %s, which names no Ceph build", shown(ours, "ceph_version")))
	}
	for _, gw := range []struct {
		name string
		md   map[string]string
	}{{"radosgw", theirs}, {"rgw-go", ours}} {
		var keys []string
		for k := range gw.md {
			if frontendKey.MatchString(k) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"frontend_config#0", "frontend_type#0"}) {
			diffs = append(diffs, fmt.Sprintf("%s registers the frontend keys %q, where one frontend's are frontend_config#0 and frontend_type#0", gw.name, keys))
		}
	}
	return diffs
}

// siteRGWGo is an rgw-go startSiteRGWGo started.
type siteRGWGo struct {
	session *gexec.Session
	// frontend is its rgw_frontends, a beast entry; base is its URL.
	frontend, base string
}

// startSiteRGWGo starts rgw-go serve with serveFlags, then radosgw's options
// args, on the populated site beside the radosgw, on a beast frontend of its
// own on loopback, until the node that started it ends. Its background
// workers are off on its command line, as the parity options hold them for
// rgw-go-up's. Once it answers Rook's probe, an anonymous GET of the root
// with 200, it returns.
func startSiteRGWGo(ctx context.Context, serveFlags, args []string) siteRGWGo {
	GinkgoHelper()
	addr := loopbackAddr(ctx)
	beast := "beast endpoint=" + addr
	s := cephtest.StartRGWGo(cephtest.Serve, slices.Concat(serveFlags, []string{"--", "-c", conf}, manifest.AdminFlags(), args, []string{
		"--rgw-frontends=" + beast,
		"--rgw-enable-gc-threads=false", "--rgw-enable-lc-threads=false",
		"--rgw-dynamic-resharding=false", "--rgw-run-sync-thread=false",
	}))
	base := "http://" + addr
	Eventually(func(g Gomega) int {
		if code := s.ExitCode(); code != -1 {
			StopTrying(fmt.Sprintf("rgw-go exited %d before it answered at %s", code, base)).Now()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", http.NoBody)
		g.Expect(err).NotTo(HaveOccurred())
		resp, err := webClient.Do(req)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(resp.Body.Close()).To(Succeed())
		return resp.StatusCode
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(250*time.Millisecond).Should(Equal(http.StatusOK), "rgw-go at %s", base)
	return siteRGWGo{session: s, frontend: beast, base: base}
}

// The spec turns rgw-go's registration back on for its own rgw-go alone,
// which then registers the host's librados build: while it runs, ceph
// versions lists that build beside the cluster's. Its entry is keyed, as
// radosgw's is, by the daemon's librados instance id
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1174-L1177 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1211-L1214 at v20.2.4),
// the global id of the session the manager takes as the entry's gid
// (https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L607-L614 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L611-L618 at v20.2.4).
var _ = Describe("rgw-go's service map entry against the radosgw's", Ordered, ContinueOnFailure, Label("integration", "servicemap"), func() {
	var (
		rgwgo *gexec.Session
		// beast is rgw-go's frontend, as it was started with it.
		beast string
		// ours and theirs are rgw-go's entry and the cluster's radosgw's.
		ours, theirs serviceMapEntry
	)

	BeforeAll(func(ctx SpecContext) {
		// get-or-create answers the entity's keyring when it exists with
		// these caps, radosgw's under Rook, and fails when it exists with
		// others.
		keyring := cephCmd(ctx, "auth", "get-or-create", serviceMapEntity, "mon", "allow rw", "osd", "allow rwx")
		DeferCleanup(func(ctx context.Context) { tryCeph(ctx, "auth", "rm", serviceMapEntity) })
		keyringFile := filepath.Join(GinkgoT().TempDir(), "keyring")
		Expect(os.WriteFile(keyringFile, []byte(keyring+"\n"), 0o600)).To(Succeed())

		// The flag overrides the RGW_GO_SERVICE_MAP=false the rgw-go
		// StartRGWGo starts inherits.
		started := startSiteRGWGo(ctx, []string{"--service-map=true"},
			[]string{"--name=" + serviceMapEntity, "--keyring=" + keyringFile, "--service-unique-id=" + serviceMapUniqueID})
		rgwgo, beast = started.session, started.frontend
		pid := rgwgo.Command.Process.Pid
		Eventually(func(g Gomega) {
			entries := rgwServiceEntries(ctx, g)
			ours = onlyEntry(g, entries, fmt.Sprintf("rgw-go's entry, the one outside a pod with pid %d", pid), onHost(pid))
			theirs = onlyEntry(g, entries, "the cluster's radosgw, the one entry from a pod", serviceMapEntry.inPod)
		}).WithContext(ctx).WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(Succeed(), "rgw-go's and the radosgw's service map entries")
	})

	It("keys its entry, as radosgw's, by the global id of the session that registered it", func() {
		for _, e := range []serviceMapEntry{theirs, ours} {
			gid, err := strconv.ParseUint(e.Key, 10, 64)
			Expect(err).NotTo(HaveOccurred(), "the key of the entry %s", e.Raw)
			Expect(gid).To(Equal(e.GID), "the key of the entry %s against its gid", e.Raw)
		}
	})

	It("registers radosgw's metadata, key by key", func() {
		site := map[string]string{"realm_name": manifest.Realm, "zonegroup_name": manifest.ZoneGroup, "zone_name": manifest.Zone}
		diffs := metadataDiffs(theirs.Metadata, ours.Metadata, beast, site)
		// Gomega cuts a long actual short, and never the description.
		Expect(diffs).To(BeEmpty(),
			"rgw-go's service map metadata against radosgw's, less the keys the pod supplies, %q:\n%s\nradosgw: %s\nrgw-go:  %s",
			podKeys, strings.Join(diffs, "\n"), theirs.Raw, ours.Raw)
	})

	// radosgw's sync trace sends its active syncs, none on a single site,
	// as its status
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_trace.cc#L74-L83 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sync_trace.cc#L74-L83 at v20.2.4).
	It("reports radosgw's status", func(ctx SpecContext) {
		Eventually(func(g Gomega) map[string]string {
			out, err := cephOut(ctx, "service", "status", "-f", "json")
			g.Expect(err).NotTo(HaveOccurred())
			var status map[string]map[string]struct {
				Status map[string]string `json:"status"`
			}
			g.Expect(json.Unmarshal([]byte(out), &status)).To(Succeed(), "decoding ceph service status: %s", out)
			got := map[string]string{}
			for gw, e := range map[string]serviceMapEntry{"radosgw": theirs, "rgw-go": ours} {
				d, ok := status["rgw"][e.Key]
				switch sync, synced := d.Status["current_sync"]; {
				case !ok:
					got[gw] = "no status for " + e.Key
				case !synced:
					got[gw] = fmt.Sprintf("no current_sync in %v", d.Status)
				default:
					got[gw] = sync
				}
			}
			return got
		}).WithContext(ctx).WithTimeout(30*time.Second).WithPolling(2*time.Second).
			Should(Equal(map[string]string{"radosgw": "[]", "rgw-go": "[]"}), "each gateway's current_sync in ceph service status")
	})

	It("answers config show for its entity with its frontend", func(ctx SpecContext) {
		Eventually(func(g Gomega) string {
			out, err := cephOut(ctx, "config", "show", serviceMapEntity, "rgw_frontends")
			g.Expect(err).NotTo(HaveOccurred())
			return out
		}).WithContext(ctx).WithTimeout(30*time.Second).WithPolling(2*time.Second).
			Should(Equal(beast), "ceph config show %s rgw_frontends", serviceMapEntity)
	})

	// The one check of serve's own wiring of the setting, where run hands
	// the resolved --service-map to the registration: no spec without a
	// cluster reaches run past its connect.
	It("keeps an rgw-go started without the flag out of the map", func(ctx SpecContext) {
		pid := startSiteRGWGo(ctx, nil, []string{"--name=client.admin"}).session.Command.Process.Pid
		Consistently(func(g Gomega) []string {
			return rawEntries(rgwServiceEntries(ctx, g), onHost(pid))
		}).WithContext(ctx).WithTimeout(30*time.Second).WithPolling(2*time.Second).
			Should(BeEmpty(), "the rgw entries of the rgw-go with pid %d, started under %s", pid, cephtest.ServiceMapOff)
	})

	// librados's close removes rgw-go's registration within
	// serviceCloseTimeout of its exit. While it shuts down librados can also
	// register the daemon again under the same key, with a later start_stamp,
	// and that entry leaves only within serviceReregisteredTimeout of the
	// exit. The spec takes either and records which held: a registration
	// other than the one it found, seen under the key at any poll, is the
	// second. A re-registered entry stays listed for about a minute, so the
	// 1 s polls cannot miss it; only one the map first listed more than
	// serviceSettle after it stopped listing the key would go unrecorded, and
	// the spec would pass on the close.
	It("leaves the map once it stops on SIGTERM", func(ctx SpecContext) {
		rgwgo.Terminate()
		Eventually(rgwgo).WithTimeout(frontend.DrainTimeout+10*time.Second).WithPolling(100*time.Millisecond).
			Should(gexec.Exit(0), "rgw-go after SIGTERM")
		exited := time.Now()
		var (
			// last is the entry under rgw-go's key the last poll found.
			last *serviceMapEntry
			// again is the first entry under the key with another
			// registration than the one BeforeAll found.
			again *serviceMapEntry
			// absent is when the polls began to find no entry under the key.
			absent time.Time
		)
		since := func() time.Duration { return time.Since(exited).Round(100 * time.Millisecond) }
		Eventually(func(g Gomega) bool {
			last = entryUnder(rgwServiceEntries(ctx, g), ours.Key)
			if last == nil {
				if absent.IsZero() {
					absent = time.Now()
				}
				return time.Since(absent) >= serviceSettle
			}
			absent = time.Time{}
			switch {
			case sameRegistration(*last, ours):
				if time.Since(exited) > serviceCloseTimeout {
					StopTrying(fmt.Sprintf("rgw-go's registration under %s (%s) %s after it exited, past %s: %s",
						ours.Key, registration(&ours), since(), serviceCloseTimeout, last.Raw)).Now()
				}
			case time.Since(exited) > serviceReregisteredTimeout:
				StopTrying(fmt.Sprintf("rgw-go's entry under %s, registered again (%s), %s after it exited, past %s: %s",
					ours.Key, registration(last), since(), serviceReregisteredTimeout, last.Raw)).Now()
			case again == nil:
				e := *last
				again = &e
			}
			return false
		}).WithContext(ctx).WithTimeout(serviceReregisteredTimeout+serviceSettle+5*time.Second).WithPolling(time.Second).
			Should(BeTrue(), func() string {
				return fmt.Sprintf("rgw-go's entry under %s %s after it exited, the last poll finding %s: %s",
					ours.Key, since(), registration(last), rawOf(last))
			})
		left := absent.Sub(exited).Round(100 * time.Millisecond)
		if again != nil {
			AddReportEntry("rgw-go's entry was registered again as it stopped", fmt.Sprintf(
				"%s, where it was %s; the entry left the map %s after rgw-go exited: %s",
				registration(again), registration(&ours), left, again.Raw))
		} else {
			AddReportEntry("rgw-go's registration left the map by its close", fmt.Sprintf(
				"%s after rgw-go exited, and no other registration under %s in the %s after", left, ours.Key, serviceSettle))
		}
	})
})
