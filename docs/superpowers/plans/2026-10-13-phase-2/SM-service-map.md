# Unit SM: rgw-go registers in the manager's service map

Threat model: honest mistakes only (PP-14). The service map is not a boundary the spec names, and
rgw-go is one more librados client registering itself. What can go wrong is a wrong or missing
metadata key, a harness reader that loses the cluster's radosgw, or a host rgw-go that adds a
second Ceph build to `ceph versions`:
- an unparsable `frontend_config#0` takes down the dashboard's gateway discovery for every gateway;
- a registration without `id` is undefined behaviour in the active manager
  (`docs/ceph-upstream-bugs.md`, "The manager's config show reads an rgw service daemon's id
  without checking it exists").

Facts are cited at v19.2.7 and v20.2.4, Rook's at v1.21.1, and rgw-go's paths at origin/main
997cdf1. No task reads the dashboard itself: the rooket clusters run with it off
(`hack/rooket/<release>/values/rook-ceph-cluster.yaml`).

## Scope

radosgw registers its librados instance in the manager's service map once its zone is loaded and
its frontends run, as daemon `<global id>` of service `rgw`
([rgw_rados.cc:1155-1184](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1155-L1184),
[rgw_rados.cc:1192-1221](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1192-L1221);
[rgw_appmain.cc:516-521](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L516-L521),
[rgw_appmain.cc:524-529](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L524-L529)).
rgw-go registers nothing, so the dashboard's Object Gateway pages, `ceph -s`, `ceph versions`, the
prometheus module's `ceph_rgw_metadata`, telemetry and `ceph config show client.rgw.<id>` do not see
it (`docs/exclusions.md`, "rgw-go does not register in the manager's service map").

| Item | Tasks |
|---|---|
| The C calls bound in the jhoblitt/go-ceph fork, and rgw-go's pin bumped | SM1 |
| The harness's radosgw readers skip any rgw-go entry | SM2 |
| The registration seam in `radosclient`, implemented by goceph | SM3 |
| The gateway registers with radosgw's metadata and reports radosgw's status; the harness's host rgw-go stays out | SM4 |
| A cluster spec comparing rgw-go's entry with radosgw's, key by key, on both releases | SM5 |

Not in this unit:
- the manager's defect a registration without `id` meets, which the registry records;
- radosgw's `rgw` perf counters through the manager, which librados's C API cannot add
  (`docs/cgo-limitations.md`, "librados' C API cannot add perf counters to the manager report");
- re-registration after a realm reload, since rgw-go has none, and `rgw-nfs`, since NFS is
  excluded.

## Decisions

| Id | Decision | Reason | Tasks |
|---|---|---|---|
| SM-D1 | Register, rather than record an exclusion. | In production rgw-go replaces radosgw in the derived image, and the dashboard finds gateways only in the map: with none it says "No RGW service is running." ([rgw_client.py:74-98](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/services/rgw_client.py#L74-L98), [rgw_client.py:82-106](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/services/rgw_client.py#L82-L106)), which breaks `docs/exclusions.md`'s promise of the dashboard's admin endpoints. librados does the rest: beacons, failover, its system metadata, the config report and the unregistering close. | SM1 to SM5 |
| SM-D2 | The harness's host rgw-go does not register. An rgw-go-only serve flag, `--service-map` (`RGW_GO_SERVICE_MAP`), defaults to true; every rgw-go the harness starts on the host runs with `RGW_GO_SERVICE_MAP=false`, which the Makefile exports to every target that runs one, each language's one start sets again, and a guard over every reference to the binary holds to those starts (SM4); SM5 alone turns it on, for its own short run. The flag is a Command-line difference in `docs/exclusions.md`. | librados writes `ceph_version` from the library the process links before the caller's keys, and `insert` keeps it ([RadosClient.cc:1089-1095](https://github.com/ceph/ceph/blob/v19.2.7/src/librados/RadosClient.cc#L1089-L1095), [RadosClient.cc:1091-1097](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/RadosClient.cc#L1091-L1097)). The host links Fedora's 19.2.6 locally and download.ceph.com's 20.2.4 in CI for both releases, so a registering host rgw-go adds a second build to `ceph versions` on both clusters locally and on Squid's at least in CI. Rook then takes its upgrade path on every CephCluster reconcile and fails it while health is not OK (pkg/operator/ceph/cluster/version.go:80-91, :178-210), and the harness's one-build checks fail (`hack/rooket/up.sh:42-47`, `test/gate/phase0_test.go`'s `expectSameBuild`). Building against the cluster's librados, as `hack/image/cgo-build.sh` does, is not enough alone: the binary must also run in that image, which moves every harness rgw-go into a container, a larger change than this unit. | SM4, SM5 |
| SM-D3 | The harness's radosgw entries are the rgw entries whose metadata carries `pod_name`. | Rook sets `POD_NAME` on every daemon it runs (pkg/operator/k8sutil/pod.go:223-228, the rgw container at pkg/operator/ceph/object/spec.go:466) and librados reports it ([util.cc:288-291](https://github.com/ceph/ceph/blob/v19.2.7/src/common/util.cc#L288-L291), [util.cc:288-291](https://github.com/ceph/ceph/blob/v20.2.4/src/common/util.cc#L288-L291)); every rgw-go the harness starts runs on the host. The `id` does not tell them apart: the harness runs rgw-go as `client.rgw.rgw-go` (`hack/rooket/rgw-go-up.sh`) and as `client.admin` (`test/integration/phase2_website_test.go`, `internal/cli/serve_integration_test.go:192`). A key of rgw-go's own would make its entry differ from radosgw's. The decision assumes that no rgw-go the harness reads the map beside runs in a pod: at origin/main no tracked file sets `POD_NAME` for one, and the only pod-run rgw-go, `.github/workflows/rook.yml`'s, reads no map through the harness. A task that runs rgw-go in a rooket pod revisits the discriminator. | SM2, SM5 |
| SM-D4 | rgw-go registers radosgw's keys and values, and adds none. | The dashboard and telemetry index the keys without a default, and the manager's `config show` reads `id` unchecked (Threat model). | SM4 |
| SM-D5 | rgw-go registers once, after its listeners are bound and its privileges dropped and before it serves, and logs a failure at error level and serves on. | radosgw registers after `init_storage` and after every frontend runs, and ignores a failure (Scope's appmain links). rgw-go has no realm reload, after which radosgw registers again. | SM4 |
| SM-D6 | rgw-go sends `{"current_sync": "[]"}` at once and every `rgw_sync_trace_servicemap_update_interval` seconds. | Every radosgw runs the sync trace's status thread, single-site included ([rgw_rados.cc:1302-1304](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1302-L1304), [rgw_rados.cc:1357-1359](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1357-L1359)), and rgw-go is single-zone (`docs/exclusions.md`, "Multisite"), where its value is the empty JSON array. | SM4 |
| SM-D7 | The binding lands on the fork's `rgw-go` branch first, by PR merged on green fork CI, and rgw-go's `replace` is bumped by hand after. | The project's go-ceph rule; Dependabot does not follow a `replace`. No go-ceph issue or PR names either call, so the same change can go to ceph/go-ceph too, and then merges into `rgw-go` first, unmodified. | SM1 |
| SM-D8 | No s3-tests test. | Nothing here is visible through S3. | all |

## Seams

**Consumed**
- `radosclient.Cluster.InstanceID()`, rados_get_instance_id cached at connect
  (`internal/radosclient/cluster.go`, `internal/radosclient/goceph/cluster.go`), the daemon name.
- goceph's `begin`/`end` accounting and `Close` (`internal/radosclient/goceph/cluster.go`), whose
  `rados_shutdown` sends the manager its close
  ([MgrClient.cc:82-94](https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/MgrClient.cc#L82-L94),
  [MgrClient.cc:83-95](https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/MgrClient.cc#L83-L95)).
- `driver.Store`'s `Zone()`, `ZoneGroup()` and `Realm()`; `cephconf.EarlyArgs.Name`; the raw
  `rgw_frontends` value serve reads.
- The harness's `wait_rgw` (`hack/rooket/lib.sh`) and `harness.RadosgwEntries`
  (`hack/internal/harness/radosgw.go`), through which every radosgw reader goes: `rgw_daemon`,
  `use_site`, populate.sh's `wait_reload`, `OneRadosgw` (hack/bench's site, and clusterconf's,
  on which T-2's LC switch waits) and clusterconf's `waitReload`.

**Produced**

| Seam | Task | Consumers |
|---|---|---|
| `func (c *Conn) ServiceRegister(service, daemon string, metadata map[string]string) error`, `func (c *Conn) ServiceUpdateStatus(status map[string]string) error` in the fork's `rados` package, `ceph_preview` | SM1 | SM3 |
| `radosclient.ServiceRegistrar`, below | SM3 | SM4 |
| radosgw entries are the `pod_name` entries (SM-D3) | SM2 | every harness reader, SM5 |
| `--service-map` / `RGW_GO_SERVICE_MAP` | SM4 | `hack/rooket/rgw-go-up.sh`, the integration specs, SM5 |
| the Go specs' one start helper for rgw-go, in `internal/testutil/cephtest`, which returns the running process, and the guard over every reference to the binary | SM4 | every spec that starts rgw-go on the host, SM5 |
| the Makefile's `RGW_GO_SERVICE_MAP=false` export on every target that runs a host rgw-go | SM4 | every such target, and any target added later that runs one |

```go
// ServiceRegistrar is satisfied by a Cluster that can register in the
// manager's service map; serve type-asserts it.
type ServiceRegistrar interface {
	ServiceRegister(service, daemon string, metadata map[string]string) error
	ServiceUpdateStatus(status map[string]string) error
}
```

## Hot files

| File | Tasks | Other units | Rule |
|---|---|---|---|
| `go.mod`, `go.sum` (the go-ceph `replace`) | SM1 | any task that bumps the fork | one bump at a time; every pinned ref is on `rgw-go`, so a later bump carries the earlier commits |
| `hack/rooket/lib.sh` | SM2 (`wait_rgw`) | none open | single owner here |
| `hack/rooket/lib_test.go` | SM2 (`wait_rgw`'s specs), SM4 (the serve-line spec) | none open | separate specs; whichever lands first, the later rebases |
| `hack/rooket/rgw-go-up.sh` (the serve line) | SM4 | none open | single owner here |
| `internal/cli/serve.go` | SM4 | any task wiring serve | SM4 adds a flag, a call and a goroutine; the index's hot-file rule |
| `Makefile` | SM4 (one target-specific export) | any task adding a cluster target | a target that runs a host rgw-go joins the export's list in the same change |
| `docs/exclusions.md` | SM4 | most tasks | SM4 edits only "Manager differences" and its Command-line bullet; announced to rgw-rs |
| `docs/cgo-limitations.md` | SM1 | any binding task | SM1 edits only its Binding row |

## Tasks

### Task SM1: go-ceph fork, ServiceRegister and ServiceUpdateStatus

- **Layer:** the jhoblitt/go-ceph fork, then rgw-go's `go.mod`. **Cluster:** no; the fork's CI
  runs its micro-osd cluster, which starts a manager.
- **Files:** in the fork, `rados/conn_service.go` and `rados/conn_service_test.go`, both
  `//go:build ceph_preview`, and `docs/api-status.json` through `make api-update`; in rgw-go,
  `go.mod`, `go.sum` and `docs/cgo-limitations.md`'s Binding row.
- **Produces:** the two methods (Seams).
- **Behaviour:**
  1. Each implements its C call, `rados_service_register` and `rados_service_update_status`
     ([librados.h:4091-4129](https://github.com/ceph/ceph/blob/v19.2.7/src/include/rados/librados.h#L4091-L4129),
     [librados.h:4092-4130](https://github.com/ceph/ceph/blob/v20.2.4/src/include/rados/librados.h#L4092-L4130)),
     with the fork's "Implements:" doc comment and a `preview_api` entry, as its rados `Checksum`
     binding has them.
  2. A map is encoded as `key\0value\0` per pair and a closing `\0`, in C memory the call frees
     after it returns: librados copies the dict into a `std::map` first
     ([librados_c.cc:799-837](https://github.com/ceph/ceph/blob/v19.2.7/src/librados/librados_c.cc#L799-L837),
     [librados_c.cc:799-837](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L799-L837)).
  3. An empty key, or a NUL in any key or value, is refused before C is called. `dict_to_map`
     stops at the first empty key and splits at every NUL, so either would cut or reshape the
     dict without an error.
  4. librados's own refusals come back as errors: EEXIST for a second registration on one
     connection, EINVAL for `osd`, `mds`, `client`, `mon` or `mgr` or an empty name, EBUSY while
     connecting, and ENOTCONN from the status update before connect
     ([RadosClient.cc:1069-1115](https://github.com/ceph/ceph/blob/v19.2.7/src/librados/RadosClient.cc#L1069-L1115),
     [RadosClient.cc:1071-1117](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/RadosClient.cc#L1071-L1117)).
- **Specs (fork):**
  - a registration under a service name unique to the run, then `MgrCommand` `service dump`
    polled until the daemon's metadata holds the given pairs and librados's `ceph_version` and
    `hostname`;
  - a pair whose value is empty comes back with its empty value, as rgw-go's `realm_name` and
    `realm_id` are on a zone without a realm;
  - a second registration is EEXIST, `osd` is EINVAL, and an empty key or a NUL in a key or a
    value is an error with nothing registered;
  - a status update on a connection not yet connected is ENOTCONN, as librados answers it before
    its state is connected (behaviour 4's RadosClient.cc links);
  - a status update shows under `service status`, which alone carries status;
  - `Shutdown` removes the entry from `service dump` within a bound.
- **PRs:** the fork's, merged on green fork CI; then rgw-go's, which bumps `replace` to the merge
  and marks the Binding row fixed.
- **Exclusions and registry:** the Binding row above.
- **Depends on:** nothing. **Brief:** about 90 lines.

### Task SM2: harness, the radosgw readers skip any rgw-go

- **Layer:** harness. **Cluster:** no; the cluster tasks' runs exercise it.
- **Files:** `hack/rooket/lib.sh` (`wait_rgw`), `hack/internal/harness/radosgw.go`
  (`RadosgwEntries`), and the specs and fixtures that feed them a service map:
  `hack/rooket/lib_test.go`, `hack/internal/harness/radosgw_test.go`,
  `hack/bench/gateway/internal/gateway/{wait_test.go,world_test.go,testdata/}`,
  `hack/s3tests/internal/clusterconf/{configurer_test.go,world_test.go}` and
  `hack/s3tests/internal/selection/lcmode_test.go`.
- **Produces:** SM-D3's selection.
- **Behaviour:**
  1. `wait_rgw` and `RadosgwEntries` keep only the rgw entries whose metadata carries `pod_name`,
     so every reader in Seams skips an rgw-go entry: it is not counted, not listed in a timeout's
     message, and its new gid does not end a reload wait.
  2. `RadosgwEntity` and `diag.sh`'s dump of the whole map are unchanged.
  3. Until SM4 the change selects what it selects today: no rgw-go registers.
- **Specs:**
  - `RadosgwEntries` over a dump holding a radosgw entry and two rgw-go entries, `id` `rgw-go`
    and `id` `admin`, neither with `pod_name`, returns the radosgw alone;
  - `OneRadosgw` settles on the radosgw while an rgw-go entry stays listed;
  - `waitReload` goes on waiting when only an rgw-go entry has a new gid, and ends on a radosgw
    entry with one;
  - `lib_test.go`, with `ceph_cmd` stubbed to print such a dump: `rgw_daemon` prints the radosgw
    entry, and populate.sh's reload filter is not satisfied by the rgw-go entry;
  - every existing fixture's radosgw entry gains a `pod_name`, so each spec passes unchanged.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 70 lines.

### Task SM3: radosclient, the service-map capability

- **Layer:** radosclient. **Cluster:** yes, for the goceph spec.
- **Files:** `internal/radosclient/cluster.go`, `internal/radosclient/goceph/cluster.go`, the
  counterfeiter fakes in `internal/radosclient/radosclientfakes`, and
  `internal/radosclient/goceph/goceph_integration_test.go`.
- **Consumes:** SM1's methods. **Produces:** `ServiceRegistrar` (Seams).
- **Behaviour:**
  1. `ServiceRegistrar` is an optional capability, as `StatsReporter` and `InflightLimiter` are;
     the counterfeiter fake is generated for it.
  2. goceph's cluster implements it over SM1's methods inside `begin`/`end`, so a call after
     `Close` starts is refused as the other calls outside a Pool are, and `Close` waits for one in
     flight. Errors are mapped as `ConfigGet`'s are.
  3. `Close` unregisters: its `rados_shutdown` sends the manager's close (Seams).
- **Specs:** the goceph integration spec, on both releases: a fresh connection registers under a
  service name of the spec's own, not `rgw`, so no harness reader or dashboard sees it; `service
  dump` shows it, a status update shows under `service status`, a call after `Close` is refused,
  and after `Close` the entry leaves `service dump` within a bound. For those seconds `ceph
  versions` lists the host's librados build (SM-D2), so the spec closes the connection in a
  `DeferCleanup`.
- **Exclusions and registry:** none.
- **Depends on:** SM1's bump. **PR:** one. **Brief:** about 60 lines.

### Task SM4: cli, the gateway registers and reports its status

- **Layer:** cli. **Cluster:** no; SM5 is the cluster spec.
- **Files:** `internal/cli/servicemap.go` and `internal/cli/servicemap_test.go` (new),
  `internal/cli/serve.go` (the flag, its usage line, the wiring); the harness's host starts:
  the `Makefile`'s export, `hack/rooket/rgw-go-up.sh` and `hack/rooket/lib_test.go`'s serve-line
  spec, `hack/rooket/README.md`, a start helper and its guard spec in
  `internal/testutil/cephtest`, `test/integration/phase2_website_test.go` (`startWebsiteRGWGo`)
  and `internal/cli/serve_integration_test.go` (its `start`); `docs/exclusions.md`.
- **Consumes:** SM3's `ServiceRegistrar`; SM2 landed.
- **Behaviour:**
  1. **The metadata** (SM-D4) is a pure function of the entity name, `service_unique_id`, the zone,
     zonegroup and realm, the raw `rgw_frontends` value and the pid:
     - `id`: the entity name past its `TYPE.`, a leading `rgw.` removed; `num_handles` `1`;
       `zonegroup_id`, `zonegroup_name`, `zone_name`, `zone_id`, `realm_name`, `realm_id`, empty
       without a realm; `service_unique_id` only when it is not empty (Scope's rgw_rados links);
     - `pid` ([rgw_appmain.cc:421](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L421),
       [rgw_appmain.cc:429](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L429));
     - `frontend_type#N` and `frontend_config#N` for every entry, in radosgw's order. It splits the
       value at commas, dropping empty pieces but keeping a piece of spaces alone, with `beast` for
       none
       ([rgw_appmain.cc:96-148](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L96-L148),
       [rgw_appmain.cc:98-150](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L98-L150)).
       No entry is dropped there: `RGWFrontendConfig::parse_config` takes the first space-separated
       word as the framework and parses a key only from a word holding `=`, which cannot fail, so a
       piece of spaces alone has the empty framework
       ([rgw_frontend.cc:17-48](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_frontend.cc#L17-L48),
       [rgw_frontend.cc:17-48](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_frontend.cc#L17-L48)).
       It then walks a `std::multimap` keyed by the framework, so the entries come sorted by
       framework, the empty one first, and in the order given within one; N counts every entry
       walked, and the type is the framework and the config the entry as split, spaces kept
       ([rgw_appmain.cc:456-514](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L456-L514),
       [rgw_appmain.cc:464-522](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L464-L522)).
       An `arrow_flight` entry takes its N and writes no keys, as a build without Arrow Flight
       skips it there; the release builds leave it out
       ([CMakeLists.txt:492](https://github.com/ceph/ceph/blob/v19.2.7/CMakeLists.txt#L492)).
     - librados adds the system keys itself.
  2. **Registration** (SM-D5): with `--service-map` true and a cluster that implements
     `ServiceRegistrar`, serve calls `ServiceRegister("rgw", <InstanceID in decimal>, metadata)`
     after `DropPrivileges` and before `serveAndRun`. An error is logged at error level with the
     error and the daemon name, and serving goes on without the status loop. The steps after the
     store opens, the frontend's and the metrics listener's `Listen`, `DropPrivileges`, the
     registration and `serveAndRun`, run in one function to which serve's `run` passes them, so a
     spec can drive them with a real frontend and recording fakes (its names are the
     implementer's).
  3. **Status** (SM-D6): after a registration, one errgroup goroutine on serve's context sends
     the status at once, then waits `rgw_sync_trace_servicemap_update_interval` seconds, read again
     each round, and sends it again. An interval of 0 or below sends no more, as radosgw's thread
     then waits for its shutdown
     ([rgw_rados.cc:327-361](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L327-L361),
     [rgw_rados.cc:362-396](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L362-L396);
     [rgw_sync_trace.cc:74-83](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_trace.cc#L74-L83),
     [rgw_sync_trace.cc:74-83](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sync_trace.cc#L74-L83)).
     A failed send is logged and the loop goes on; it returns nil when the context ends, so it
     never stops the gateway.
  4. **The flag** (SM-D2): `--service-map=false`, or `RGW_GO_SERVICE_MAP=false`, registers nothing
     and sends nothing. The README and serve's usage list it. Every rgw-go the harness starts on
     the host runs with `RGW_GO_SERVICE_MAP=false` in its environment, which works in both of
     rgw-go's command-line forms (serve's flags, and radosgw's argv, under which rgw-go's own
     settings come from `RGW_GO_*` alone), and which a `--service-map=true` flag overrides
     (`internal/cli/root.go`: flags, then the environment). Three layers keep it there:
     - **The inherited default.** The Makefile exports `RGW_GO_SERVICE_MAP=false` as a
       target-specific variable on every target that runs a host rgw-go against a cluster, so
       every process the target starts, a recursive `$(MAKE)` included, inherits it: `integration`
       (internal/cli's and test/integration's specs start rgw-go), `admin-gate` (the website gate
       starts one), `gate` (its specs need none today; the export covers a later one),
       `read-gate` and `rgw-go-up` (rgw-go-up.sh), `lc-mode` (it restarts rgw-go through
       `make rgw-go-up`), `s3tests` and `admin-suite` (both run `make lc-mode`), `s3tests-parity`,
       `admin-parity`, `admin-record`, `s3tests-record` and `s3tests-record-tests` (each runs
       `s3tests` or `admin-suite`), and `bench-gateway` (hack/bench's gateway command runs
       rgw-go-up.sh). `bench-seam`, `populate` and the cluster targets start no rgw-go. A spec
       that reads the variable sets it itself, so `make test` stays independent of it.
     - **One start per language, each setting it again** for a run outside make: rgw-go-up.sh's
       serve line, which `make rgw-go-up`, read-gate.sh, `make lc-mode` and hack/bench's gateway
       command all go through, sets it beside `RGW_GO_LOG_FORMAT`; and the Go specs start rgw-go
       only through one helper in `internal/testutil/cephtest`, which builds the binary itself and
       returns the running process (a `*gexec.Session` it stops when the spec or container ends),
       never a path, in either form: `serve` with flags, or a copy named `radosgw` given Rook's
       argv. Today's two Go start sites move to it: serve_integration_test.go's `start`
       (radosgw's argv, as `client.admin`) and phase2_website_test.go's `startWebsiteRGWGo`
       (`serve`, as `client.admin`).
     - **A guard over every reference to the binary.** No tracked file outside `docs/`, Markdown
       and a named allow-list mentions `cmd/rgw-go` (which `./cmd/rgw-go` and the import path
       `github.com/jhoblitt/rgw-go/cmd/rgw-go` contain) or `bin/rgw-go`, or runs a `go build` of
       it. The allow-list, each entry with its reason, is:
       - the helper's own file, and the guard's spec and its `testdata` fixtures, which hold the
         names as data;
       - `hack/rooket/rgw-go-up.sh`, the shell's one start, which sets the variable on its serve
         line, and `hack/rooket/lib_test.go`, whose spec pins that line;
       - `hack/bench/gateway/internal/gateway/gateways.go`, which builds `bin/rgw-go` under the
         bench's tags only for rgw-go-up.sh to start, and its specs and command-trace fixtures,
         `cli_test.go`, `world_test.go`, `testdata/run-rgw-go.txt` and
         `testdata/run-rgw-go-smoke.txt`, which run against a fake execer and start nothing;
       - `hack/image/build.sh` and `hack/image/Containerfile`, which build the derived image's
         binary in the release's builder and run it only inside that image (`rgw-go version`,
         `ldd`), never on the host;
       - `.goreleaser.yaml` and `.github/workflows/release.yml`, the release builds, the latter
         building a binary only to read its version stamp;
       - `internal/cli/root_test.go`, whose `/usr/bin/rgw-go` is an argv string for `cli.Argv`.

       A new file that names the binary fails the guard until it is allow-listed with a reason,
       so a start site that bypasses the helper fails `make test` wherever its build and its
       start sit. What the guard cannot see is a start that reaches the binary by a path it
       computes without naming it; layer one's inherited default still covers that start under
       make.
- **Specs** (Ginkgo, over the counterfeiter `ServiceRegistrar` and a fake clock):
  - the metadata, a table: `client.rgw.rgw-go` gives `id` `rgw-go`, `client.rgw.my.store.a`
    gives `my.store.a`, `client.admin` gives `admin`, `client.rgwx.a` gives `rgwx.a`;
    `service_unique_id` only when set; the zero realm gives empty `realm_name` and `realm_id`;
    `num_handles` `1`; `pid`;
  - `service_unique_id` from the command line: radosgw's argv with Rook's
    `--service-unique-id=rook-ceph-rgw-ceph-objectstore-a-6d8f9c7b5-x2k4p`
    (`internal/cephconf/early_test.go`'s fixture) reads back through librados's parse, as serve
    reads its options (`goceph.ConfiguredOptions`, as serve_test.go's module-default specs read
    theirs), and the metadata built from it carries that value;
  - the frontends, a table: `beast port=8080` gives `beast` and `beast port=8080`; an empty value
    gives `beast` and `beast`; `loadgen,beast port=80` gives #0 `beast` `beast port=80` and #1
    `loadgen` `loadgen`; `beast port=80, loadgen` gives #1 `loadgen` with config ` loadgen`;
    `beast port=80, ` (a comma, then one space) gives #0 with an empty type and a config of one
    space, and #1 `beast` `beast port=80`; `beast port=80,arrow_flight,loadgen` gives no #0
    keys, #1 `beast` and #2 `loadgen`;
  - the order, driven through behaviour 2's function with a real frontend on `127.0.0.1:0`: the
    recorded steps run listen, drop privileges, register, serve; and when the fake registrar's
    `ServiceRegister` runs, a TCP dial to the frontend's bound address connects while the serve
    step has not been entered;
  - one registration, service `rgw` and daemon `4721` for instance id 4721, with that metadata;
    an error is logged at error level, serving goes on, and no status is sent;
  - the loop: a send at once and after 10 s; after 5 s once the option reads 5; none after it
    reads 0; a failed send logged and the next one made; nil on cancel;
  - `--service-map=false` and `RGW_GO_SERVICE_MAP=false` make no call, and `--service-map=true`
    wins over `RGW_GO_SERVICE_MAP=false`;
  - `lib_test.go`'s serve line sets `RGW_GO_SERVICE_MAP=false` beside `RGW_GO_LOG_FORMAT=json`;
  - the Makefile, read through `make -pn`'s database: each target layer one lists exports
    `RGW_GO_SERVICE_MAP = false`, and `test` and `check` do not;
  - the helper returns a running process whose environment carries `RGW_GO_SERVICE_MAP=false`
    in both forms, and its API exposes no path to the binary;
  - the guard, in `cephtest`'s suite, over the tracked file list: it passes at the task's
    commit, and fails, naming the file, for fixtures holding each bypass:
    - the split-file bypass: one file builds `github.com/jhoblitt/rgw-go/cmd/rgw-go` into a
      package variable in a `BeforeSuite`, another starts that variable with `exec.Command`;
      the building file fails, whatever the starting file holds;
    - the `bin/rgw-go` bypass: a spec starts `filepath.Join(repo, "bin/rgw-go")`, which
      rgw-go-up.sh's build left behind;
    - a `go build -o x ./cmd/rgw-go` in a script under `hack/`;
    - an allow-listed name used from a file that is not on the list.
- **Exclusions and registry:** in `docs/exclusions.md`, "rgw-go does not register in the
  manager's service map" goes, the Command-line differences gain the flag ("no Ceph option turns
  radosgw's registration off", Scope's appmain links), and "The manager gets none of radosgw's
  rgw perf counters from rgw-go" says that the report now carries librados's own counters.
  Announced to rgw-rs.
- **Depends on:** SM2, SM3. **PRs:** two. (a) The harness's off-switch, behaviour 4's three
  layers and the start sites' move, with their specs; it changes nothing until (b) reads the
  variable, so it lands first and no harness rgw-go can register in between. (b) The flag, the
  metadata, the registration and the status loop, with their specs and the exclusions edits.
  **Brief:** about 250 lines across the two.

### Task SM5: integration, rgw-go's entry against radosgw's, on both releases

- **Layer:** integration specs. **Cluster:** yes.
- **Files:** `test/integration/servicemap_test.go` (new).
- **Behaviour:** the spec, `Label("integration")`, `Ordered`:
  1. Starts rgw-go built from the tree through SM4's start helper in the `serve` form, the only
     form that takes rgw-go's own flags: `serve --service-map=true -- <radosgw's options>`. The
     flag overrides the inherited and the helper's `RGW_GO_SERVICE_MAP=false`. Radosgw's options
     after `--` put it on the populated site, as `startWebsiteRGWGo` does, on
     `beast endpoint=127.0.0.1:<free port>`, with `--service-unique-id=rgw-go-servicemap-spec`,
     as an entity of its own, `client.rgw.rgw-go-servicemap`. The spec gets or creates that
     entity through the toolbox with radosgw's caps under Rook, `mon 'allow rw' osd 'allow rwx'`,
     and removes it when it ends.
     - `config show` reaches an rgw daemon only by a name holding `rgw` after its type
       ([DaemonServer.cc:2093-2133](https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L2093-L2133),
       [DaemonServer.cc:2572-2612](https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L2572-L2612)).
     - `client.rgw.rgw-go`'s config section is rgw-go-up's.
  2. Finds rgw-go's entry in `ceph service dump -f json` as the rgw entry whose `pid` is the
     process's, and the cluster's radosgw as SM-D3 picks it. Like radosgw's, rgw-go's entry is
     keyed by its librados instance id: its key, read as a number, equals its `gid`, the global id
     of the session that registered it
     ([rgw_rados.cc:1174-1177](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1174-L1177),
     [rgw_rados.cc:1211-1214](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1211-L1214);
     [DaemonServer.cc:607-614](https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L607-L614),
     [DaemonServer.cc:611-618](https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L611-L618));
     the spec asserts it for both entries.
  3. Asserts, key by key, each gateway running one frontend:
     - the two key sets are equal once `pod_name`, `pod_namespace`, `container_name`,
       `container_image`, `container_hostname` and `mem_cgroup_limit`, which the pod supplies, are
       left out of both;
     - `realm_*`, `zonegroup_*`, `zone_*`, `num_handles` and `frontend_type#0` equal radosgw's;
     - `id` is `rgw-go-servicemap`, `service_unique_id` is `rgw-go-servicemap-spec`,
       `frontend_config#0` is the beast entry it was given, and `ceph_version` names a build.
  4. `ceph service status -f json` shows `current_sync` `[]` for rgw-go, as for the radosgw.
  5. `ceph config show client.rgw.rgw-go-servicemap rgw_frontends` answers the beast entry.
  6. After SIGTERM the process exits 0 and its entry leaves `service dump` within 10 s. That is
     well under `mgr_service_beacon_grace`, 60 s
     ([mgr.yaml.in:113-118](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/mgr.yaml.in#L113-L118),
     [mgr.yaml.in:113-118](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/mgr.yaml.in#L113-L118)),
     after which the manager drops a silent daemon anyway, so only the close librados sends at
     shutdown removes it that soon
     ([DaemonServer.cc:686-710](https://github.com/ceph/ceph/blob/v19.2.7/src/mgr/DaemonServer.cc#L686-L710),
     [DaemonServer.cc:690-714](https://github.com/ceph/ceph/blob/v20.2.4/src/mgr/DaemonServer.cc#L690-L714)).
- **Cost:** while the spec runs, `ceph versions` lists the host's librados build too (SM-D2). It
  runs under `make integration`, which CI runs before `make gate` and never beside it.
- **Exclusions and registry:** none.
- **Depends on:** SM4. **PR:** one. **Brief:** about 80 lines.

## Order

| Wave | Tasks | Waits for |
|---|---|---|
| 1, in parallel | SM1 (the fork's PR, then the bump), SM2 | nothing |
| 2 | SM3 | SM1's bump |
| 3 | SM4 | SM2, SM3 |
| 4 | SM5 | SM4 |

The unit runs beside phase 2's units and waits for none of them. Its cluster tasks, SM3 and SM5,
run one at a time with every other unit's.

## Defects

- "The manager's config show reads an rgw service daemon's id without checking it exists"
  (`docs/ceph-upstream-bugs.md`): SM4's metadata always carries `id`.
