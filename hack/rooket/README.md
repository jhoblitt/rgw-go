# Disposable Rook clusters

One-worker Rook clusters on kind for the integration specs and the phase 0
gate, one per Ceph release, stood up by
[rooket](https://github.com/jhoblitt/rooket) from a released Rook with the
release's Ceph image pinned. Each runs the chart's `ceph-objectstore` object
store on one OSD, with host networking so that librados and S3 clients on the
host reach its mon, OSD and radosgw. Nothing here touches any other cluster.

```sh
make cluster-up RELEASE=squid     # quay.io/ceph/ceph:v19.2.7 under Rook v1.20.7
make populate RELEASE=squid
make integration RELEASE=squid    # the integration specs
make gate RELEASE=squid           # the phase 0 gate, test/gate/
make read-gate RELEASE=squid      # the corpus read through rgw-go and the radosgw, compared
make s3tests RELEASE=squid        # s3-tests against the radosgw, after the gate
make admin-suite RELEASE=squid    # go-ceph's rgw/admin suite against the radosgw
make rgw-go-up RELEASE=squid      # rgw-go on the host at http://127.0.0.1:7481, under the parity settings
make s3tests-parity RELEASE=squid # s3-tests against rgw-go, compared with radosgw's baseline
make admin-parity RELEASE=squid   # go-ceph's rgw/admin suite against rgw-go, compared with radosgw's baseline
make admin-gate RELEASE=squid     # rgw-go's admin API compared with radosgw's, byte for byte
make rgw-go-down RELEASE=squid
make cluster-down RELEASE=squid

make cluster-up RELEASE=tentacle  # quay.io/ceph/ceph:v20.2.4 under Rook v1.20.7
make populate RELEASE=tentacle
make integration RELEASE=tentacle
make gate RELEASE=tentacle
make read-gate RELEASE=tentacle
make s3tests RELEASE=tentacle
make admin-suite RELEASE=tentacle
make rgw-go-up RELEASE=tentacle
make s3tests-parity RELEASE=tentacle
make admin-parity RELEASE=tentacle
make admin-gate RELEASE=tentacle
make rgw-go-down RELEASE=tentacle
make cluster-down RELEASE=tentacle
```

Each release is its own rooket cluster, `rgw-go-<release>`, so both can run at
once and neither collides with another rooket cluster on the host. Reach one
with `ROOKET_NAME=rgw-go-<release> rooket k ...`, never with the ambient
`kubectl`; `ROOKET=<path>` makes the targets run a rooket other than the one
on `PATH`.

`make integration` runs the write path's oracle, `test/integration/write_test.go`,
which removes the buckets it writes and the garbage they leave, so `make gate`
still passes after it.

It also runs the multipart oracle, `test/integration/multipart_test.go`,
which serves rgw-go's S3 handler from the spec process over a RADOS
connection of its own, beside the cluster's radosgw, and drives alice's
uploads in the bucket `mp-oracle` through both gateways: each reads back what
the other wrote, completes an upload the other started, and lists its parts
and uploads with the same documents; a re-uploaded part's first upload and an
aborted upload's parts reach the GC under the upload id; the
`RGWCompleteMultipart` lock held from the spec refuses either gateway's
completion with radosgw's 500; `radosgw-admin object stat` shows the manifest
rgw-go decodes; and `bucket check` stays clean, but for a live upload's
re-uploaded part head, which it reports for radosgw's upload too. It counts
the RADOS operations a 5 MiB UploadPart costs rgw-go, and removes `mp-oracle`
and its garbage when it ends. To run it alone against a release's cluster:

```sh
RGW_GO_TEST_CEPH_CONF=$PWD/hack/rooket/out/squid/ceph.conf \
  go test -tags=ceph_preview,integration -race -count=1 ./test/integration \
  -ginkgo.focus='multipart oracle'
```

`make cluster-up` rewrites `out/<release>/` without the `manifest.json` the
gate reads, so `make populate` runs before `make gate` after every
`make cluster-up`, in a fresh worktree too. Against a populated cluster it
writes the same data and manifest again.

## Prerequisites

- rooket at the commit `.github/actions/rooket-cluster/action.yml` pins, the
  setup CI's cluster workflows share, with its own prerequisites: a container
  engine, `kind`, `kubectl`, `helm`, and the iSCSI tooling. Every `cluster-up` creates the OSD's iSCSI target and every
  `cluster-down` removes it with the disk image, so both need root;
  `rooket sudoers install` removes the prompt.
- The AWS CLI v2, `jq` and `python3`, for `populate.sh`.
- `jq`, for `rgw-go-up.sh` and `rgw-go-down.sh`, and for `make test`, whose
  specs run their reads of `rgw-go.log`.
- `git`, `curl` and `python3` with `venv`, with access to GitHub and PyPI,
  for `hack/s3tests/run.sh`.
- `git`, `curl`, `jq` and Go with cgo and the librados headers below, with
  access to GitHub and the Go module proxy, for `hack/admin/run.sh`.
- librados 19.2.6 or later with its headers: both releases' admin keys are
  AES256KRB5, which older librados cannot parse.

## Layout

| Path | Role |
|---|---|
| `squid/`, `tentacle/` | rooket configuration homes, recorded with the cluster: the `host-network` profile and the chart values |
| `up.sh` | `rooket up --wait`, then checks every Ceph pod runs the pinned image, creates the `rgw-go-test` pool the integration specs use, runs `rooket ceph-config`, and checks the host reaches the radosgw |
| `populate.sh` | writes the data set through the radosgw and records it in `manifest.json` |
| `down.sh` | stops the release's rgw-go, then `rooket down --delete-disks`, and removes `out/<release>/`, even when rooket fails |
| `diag.sh` | collects pod logs, Rook status and Ceph's view for CI, with no admin keyring or S3 secret key, each call bounded by a timeout |
| `admin.sh`, `endpoint.sh` | `admin.sh <release> <args...>` runs `radosgw-admin` in the toolbox on the site `manifest.json` records and `endpoint.sh <release>` prints the radosgw's URL, for the integration specs (`internal/testutil/cephtest`) |
| `rgw-go-up.sh`, `rgw-go-down.sh` | build rgw-go, set the parity options for both gateways and run rgw-go on the host against the cluster; stop it |
| `read-gate.sh` | `make read-gate`: starts rgw-go, runs the gate's read specs against it and the radosgw, and stops it |
| `lib.sh` | the Rook release, cluster naming, toolbox and radosgw lookup helpers, and the parity options, that the scripts share |
| `parity-options-check.sh` | `make parity-options-check`: fails when a release's chart values give the radosgw other options than `parity_options` |

The chart values pin `cephImage.tag`, a plain `vX.Y.Z` that is the one place
a release's Ceph version is set: `up.sh` checks every daemon runs it and
`populate.sh` records it for the gate. They also turn the dashboard off (with
it on, Rook creates a `dashboard-admin` user in the zone) and drop the
chart's block pool and filesystem, which rgw-go does not use. rooket's
one-worker base already fits the chart's `ceph-objectstore` object store to a
single OSD; the values' entry for it, which rooket merges onto the base's by
name, adds to its gateway the parity options as `rgwConfig` (see "Running
rgw-go against the cluster") and, as `rgwCommandFlags`, a frontend with
`tcp_nodelay=1` (see "Running the gateway comparison"). rooket's `rgw` profile is not
used: its CephObjectStoreUser and bucket claim would add a user and a bucket
to the zone the gate counts. `host-network` sits in `config.yaml` rather than
on the command line because Rook cannot move a running cluster onto host
networking, so every later `up` must keep it.

## Finding the radosgw

The scripts find the radosgw, and the realm, zonegroup and zone it serves, in
Ceph's service map. A radosgw registers there under a new gid each time it
starts or reloads its realm. The manager drops the old gid as soon as that
instance closes its session, but one that ends without closing it stays until
it has sent no beacon for a minute (`mgr_service_beacon_grace`), so after a
restart or a reload the map can list both for about that long. `make
cluster-up` against a running cluster can restart the radosgw through its
helm upgrade, and `populate.sh` reloads it. The scripts therefore wait up to
three minutes for the map to settle, to one radosgw or, after a reload, to
the reloaded one, and then stop with the entries they saw. A reload that has
not finished by then has hung; a restart of the radosgw's pod has ended such
a hang, and `docs/ceph-upstream-bugs.md` records its probable causes. That
stop names the restart command, with `ROOKET_NAME` set; the scripts never
restart the radosgw themselves. A map that has not settled to one radosgw by
then lists none that is running or more than one, since the manager has
long dropped any that stopped.

## Output

`hack/rooket/out/<release>/` is ignored by git and removed by `make cluster-down`:

- `ceph.conf` and `ceph.client.admin.keyring`, from `rooket ceph-config`; the
  conf names the keyring, so `CEPH_CONF=hack/rooket/out/squid/ceph.conf` is
  all a host client needs.
- `image`, the Ceph image every daemon and the toolbox run.
- `manifest.json`, written by `populate.sh`: the pinned Ceph version, the
  rooket cluster, the realm, zonegroup and zone, the pools, the compressing
  storage classes with their codecs (`storage_classes`), the users `alice`
  and `t1$bob` with their S3 keys, the buckets `plain` and `t1/tenanted` with
  the id, marker and index shard count `radosgw-admin bucket stats` reports,
  and the objects in `plain`, a compressed one with its `storage_class` and
  `compression`.
- `ceph.client.rgw.rgw-go.keyring`, `rgw-go.log`, `rgw-go.pid`,
  `rgw-go.endpoint` and `rgw-go.metrics`, from `rgw-go-up.sh`: rgw-go's key,
  its JSON log, its process, its S3 URL and its metrics address.
  `rgw-go-down.sh` removes the last three.

## Population

`populate.sh` writes everything through the radosgw: users with
`radosgw-admin` in the Rook toolbox, buckets and objects with the AWS CLI
against the radosgw's node IP. Rook names the realm, zonegroup, zone and pools
after the object store, so nothing is hard-coded: the site is the one the
radosgw reports in Ceph's service map, and the pools and placement come from
the zone and zonegroup. Every `radosgw-admin` call names the realm, zonegroup
and zone; without them it creates, and works in, a `default` zone the radosgw
never serves.

The zonegroup gets a cloud-s3 storage class, so that a placement tier is
encoded. The default placement gets a storage class per codec radosgw
compresses with, `COMP_ZLIB`, `COMP_SNAPPY`, `COMP_ZSTD` and `COMP_LZ4`, each
on the STANDARD class's data pool: radosgw compresses by the storage class an
object is written to, and `radosgw-admin` adds a class to the zone only once
the zonegroup's placement target lists it. The realm's period is committed
once with all of them, as Rook would otherwise commit it at a later
reconcile; these changes and the commit are made with the Rook operator's
`radosgw-admin`, as below. The script then waits for the radosgw to reload
its zonegroup and zone from the period, which took 3 to 28 s in the reloads
seen. One 19.2.6 radosgw was still paused minutes into that reload; the
script stops after three minutes, and deleting the radosgw's pod recovers
it. `docs/ceph-upstream-bugs.md` records the probable causes of the delay and
of the hang.

`multipart.bin` is 20 MiB and goes up with `aws s3 cp`, which switches to
multipart at 8 MiB. `_underscore.bin` exercises radosgw's escaping of a
leading underscore in RADOS object names. `comp-<codec>.bin` is 1 MiB of its
key repeated, written to its codec's storage class; radosgw stores an object
as written when it cannot load the class's compressor, so the script stops
unless `radosgw-admin object stat` shows the object compressed with the
codec. The other objects hold pseudo-random bytes seeded by the key; either
way a rerun writes the same data. The script is safe to rerun against a
populated cluster.

Rook's operator writes the realm, zonegroup, zone and periods with the
`radosgw-admin` of its own image, Ceph 20.2.4 in Rook v1.20.7. `populate.sh`
changes them with that same `radosgw-admin`, run in the operator's pod with
the config and keyring the operator uses, so on the Squid cluster they stay in
Tentacle's encoding, as Rook leaves them. The gate accepts a root pool object
that is not the cluster release's encoding only when it is the operator
release's encoding byte for byte, and when rgw-go's re-encoding of it for the
cluster's release equals what the cluster's own `ceph-dencoder` writes after
decoding it, which is what the cluster's radosgw would write back. When the
two releases differ, the gate fails unless some root pool object is in the
operator release's encoding.

Checks after populating:

```sh
export ROOKET_NAME=rgw-go-squid
rooket k -n rook-ceph exec deploy/rook-ceph-tools -- rados -p ceph-objectstore.rgw.meta -N users.uid ls
rooket k -n rook-ceph exec deploy/rook-ceph-tools -- rados -p ceph-objectstore.rgw.buckets.index ls
```

`users.uid` holds `alice`, `alice.buckets`, `t1$bob` and `t1$bob.buckets`; the
index pool holds eleven `.dir.<bucket id>.<n>` shards per bucket.

## Running rgw-go against the cluster

```sh
make rgw-go-up RELEASE=squid      # build bin/rgw-go and start it
make rgw-go-up RELEASE=squid RGW_GO_FLAGS=--rados-completions=pipe
make s3tests RELEASE=squid GATEWAY=rgw-go RUN=try1
make rgw-go-down RELEASE=squid
```

`rgw-go-up.sh <release> [flags...]` builds `bin/rgw-go` with cgo, taking
`CGO_CFLAGS` and `CGO_LDFLAGS` from the environment, and runs it on the host,
beside the cluster's radosgw and on the site that radosgw serves, as
`rgw-go serve --metrics-addr=127.0.0.1:9481 [flags...] -- <radosgw's options>`.
radosgw's options name rgw-go's entity, keyring and `ceph.conf`, the realm,
zonegroup and zone, and `--rgw-frontends="beast endpoint=127.0.0.1:7481"`,
so that rgw-go listens on the loopback address only. Each release has its
own ports, so both clusters can have an rgw-go at once: 7481 and 9481 for
Squid, 7482 and 9482 for Tentacle. `RGW_GO_PORT` and `RGW_GO_METRICS_PORT`
override them. It restarts an rgw-go it started, and stops before changing
anything when something else answers on the port. Once rgw-go answers the
anonymous `GET /` with 200, as Rook's probe expects of radosgw, the script
writes `out/<release>/rgw-go.endpoint`, which `make s3tests` and `make
admin-suite` read for `GATEWAY=rgw-go`.

rgw-go runs as its own cephx entity, `client.rgw.rgw-go`, with radosgw's caps
under Rook, `mon 'allow rw' osd 'allow rwx'`. The script creates it on its
first run and fetches its key into `ceph.client.rgw.rgw-go.keyring` on each
run. It then sets the parity options in the monitors' config store, for that
entity and for the radosgw's (`client.rgw.ceph.objectstore.a`, checked as
under "go-ceph's rgw/admin suite" below), so that both gateways run alike:
`rgw_dynamic_resharding`, `rgw_enable_gc_threads`, `rgw_enable_lc_threads`
and `rgw_run_sync_thread` false, so that no background worker runs beside
the requests; `rgw_d3n_l1_local_datacache_enabled` false, so that no read
cache sits between the gateway and RADOS; and the three usage options the
rgw/admin suite needs. `parity_options` in `lib.sh` lists them.

None of them is a startup-only option to the monitors, so `ceph config set`
reaches the running radosgw at once and `ceph config show` reports the new
value straight away. But radosgw reads the GC, LC, sync and reshard settings
only when it builds its store, at startup and at each realm reload, and
`config show` cannot tell whether it has done so since. The script therefore
compares the time each parity option last changed, from `ceph config log`,
in any section the radosgw reads (`global`, `client`, `client.rgw` and on to
its own, masked or not), with the `start_stamp` of the radosgw's service map
entry, which the manager stamps when that instance registers, after it has
built its store. When an option changed later, this run or an earlier one
that stopped before restarting the radosgw, it restarts the radosgw's
deployment, which interrupts any other client of the radosgw, and waits
until the service map lists a radosgw started after every change. Otherwise
it leaves the radosgw alone.

The check has three limits. A change made by hand while the radosgw starts,
after it has read its options but before the manager stamps it, looks older
than the start and is missed. A manager that loses its service map, or
stamps the radosgw afresh, makes a stale radosgw look new. And it compares
the monitors' clock with the manager's, which agree here because every kind
node shares the host's kernel and its clock.

Each time it reconciles the object store, `make cluster-up` against a
running cluster among the times, Rook writes its own options for the
radosgw's entity, `rgw_run_sync_thread=true` among them, before it creates
or updates the radosgw's deployment. The chart values therefore give the
object store's gateway the parity options as `rgwConfig`, which Rook writes
in place of its own (`generateMonConfigOptions` in rook
pkg/operator/ceph/object/config.go). The radosgw of a fresh cluster starts
under them, a reconcile leaves them as they are, and `make rgw-go-up`
leaves the radosgw's options, and the radosgw, alone unless they were
changed by hand. `make
parity-options-check`, part of `make check`, fails when the two releases'
`rgwConfig` and `parity_options` differ. The entity and the options stay
when rgw-go stops; the gate counts neither.

`rgw-go-down.sh` sends rgw-go `TERM`, under which it drains its requests as
radosgw does, waits up to 30 s for it to exit and kills it after that, and
fails when rgw-go was killed or ended with an error. It keeps `rgw-go.log`,
which `diag.sh` collects with the rest of `out/<release>/`. `make
cluster-down` runs it first, and goes on when no rgw-go is running.

### By hand

rgw-go runs on the host against a populated cluster with the command line Rook
gives radosgw. Named `radosgw`, the binary takes its whole command line as
ceph's; rgw-go's own settings come from `RGW_GO_*` variables:

```sh
out=hack/rooket/out/squid
go build -tags=ceph_preview -o "${TMPDIR}/radosgw" ./cmd/rgw-go
RGW_GO_METRICS_ADDR=127.0.0.1:9283 RGW_GO_LOG_LEVEL=debug \
"${TMPDIR}/radosgw" --foreground --name=client.admin -c "${out}/ceph.conf" \
	--default-log-to-stderr=true --default-err-to-stderr=true \
	'--rgw-frontends=beast endpoint=127.0.0.1:8080' \
	--rgw-realm="$(jq -r .realm "${out}/manifest.json")" \
	--rgw-zonegroup="$(jq -r .zonegroup "${out}/manifest.json")" \
	--rgw-zone="$(jq -r .zone "${out}/manifest.json")"
```

Rook's probe, an anonymous `curl -i http://127.0.0.1:8080/`, answers 200 with
`Server: Ceph Object Gateway (squid)` and the empty listing:

```xml
<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>anonymous</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>
```

`http://127.0.0.1:9283/metrics` and `/debug/pprof/` serve the metrics and
profiles. `serve_integration_test.go` in `internal/cli` runs this command
under `make integration`, over TLS too, and stops it with SIGTERM.

## The read gate

```sh
make read-gate RELEASE=squid
```

`read-gate.sh` starts rgw-go with `rgw-go-up.sh`, runs the specs in
`test/gate` labelled `read` against it and the cluster's radosgw, and stops
rgw-go however they end; `make gate` leaves those specs out. Each spec sends
one signed request to both gateways and requires rgw-go's status, headers and
body to be radosgw's, an error document's request and host ids aside, since
they end in each process's librados instance id: every populated object read
whole and by HEAD, through a raw client and through
aws-sdk-go-v2, ranges across the head, tail-stripe, part and compression
boundaries, the conditional headers, `partNumber`, the `response-*`
overrides, `?tagging` and `?acl`. GetObjectAttributes is compared on
Tentacle; on Squid, whose radosgw answers it as a GetObject, rgw-go's answer
is checked on its own. One spec counts the RADOS read ops of a request from
`rgw_go_rados_ops_total` at the address `rgw-go.metrics` names: one for a GET
of an object its head holds whole. That counter is the whole process's, so
each count is taken between two one-second windows in which it holds still,
and only a count two such measurements agree on is judged. The gate needs a
populated cluster and is no benchmark.

## The seam microbenchmark

`make bench-seam RELEASE=squid` runs `hack/bench/seam`, a Go command, which sweeps the
seam microbenchmark in `test/bench/seam` over the `rgw-go-test` pool, each
cell in each completion mode in a benchmark process of its own, so that a
cell's thread growth is its own, measures its floor with `rados bench`,
run on the host through `ROOKET_ENGINE` (podman by default), and renders
`hack/bench/out/<release>/seam-<UTC time>/REPORT.md` with
`hack/bench/report`. Each cell the throughput and latency criteria compare
runs alone, between a floor run just before its three modes and one just
after them, and its ratios divide by the two runs' mean; any other mirror
cell has one floor run. The floor's `rados` comes from
`quay.io/ceph/ceph:v<version>` for the version of the librados the benchmark
links (`BENCH_FLOOR_IMAGE` overrides the name, not the check), so that both
sides use one client release; the sweep stops when that image cannot be
found or runs another release. Where the cluster's own image is another
release, its `rados bench` also runs once at each judged cell, reported
beside the floor and not judged. It measures the host as much as the seam, so run it
with nothing else busy; `noise.log` in the run directory samples the load and
the busiest processes every 15 seconds. Each 4 MiB write waits for 5 GiB free
in the pool, writes about 3.9 GiB and is removed before the next, and the
sweep leaves the pool as it found it; one that fails partway removes the
floor's objects and those of the benchmark process that was running, then
exits with the failure's status. A watchdog stops a benchmark process or a
floor run that outlives its limit, since go test's `-timeout` does not apply
to benchmarks; `BENCH_TIMEOUT` and `BENCH_FLOOR_TIMEOUT` override the limits.
The Go cells take their client config from
the release's rooket output, as the floor does, and the sweep refuses an
`RGW_GO_TEST_CEPH_CONF` naming another file. A full sweep took about an hour per
release on a 32-thread host. `BENCH_QUICK=1` runs a short sweep; any other
value of it is refused. Every setting is checked before anything touches the
cluster, and `BENCH_PLAN=1` prints the sweep's schedule, each benchmark
process with its watchdog limit, each floor run and each wait for the pool's
space, then exits. The command's package doc lists its settings, and
`go run ./hack/bench/seam --help` every flag and its environment name.

`make bench-seam RELEASE=squid BENCH_LIMITER=1` re-runs only the sweep's
over-budget cell, write4m at 64 in flight past the byte budget, under the
in-flight limiter. The cell runs in sync, callback and pipe mode, each in a
benchmark process of its own, with the limiter's byte bound at 100 MiB,
librados's default `objecter_inflight_op_bytes`, and its operation bound
derived, as rgw-go runs. `BENCH_INFLIGHT_BYTES` sets another bound. The run
writes `overbudget-<mode>.jsonl`, `env.json`, `sweep.log`, `noise.log` and
`bench-<mode>.log` into `hack/bench/out/<release>/seam-<UTC time>-limited/`,
or `seam-<UTC time>-limited-<bytes>/` for another bound. It runs no floor and
renders no `REPORT.md`, since `hack/bench/report` renders only a whole sweep.
`BENCH_PLAN=1` prints its schedule. `BENCH_QUICK=1` runs 128 objects a
process instead of 640, and `BENCH_OVER_OBJECTS`, `BENCH_TIMEOUT` and
`BENCH_MIN_AVAIL_GIB` work as they do for the sweep; the settings only the
sweep reads are refused. Setting `BENCH_INFLIGHT_BYTES` without
`BENCH_LIMITER=1` is refused rather than starting the sweep.

## Running the gateway comparison

`make bench-gateway RELEASE=<release> GATEWAY=radosgw|rgw-go` runs
`hack/bench/gateway`, a Go command: elbencho against the gateway for the S3
profiles, `hack/bench/httpload` for the anonymous `GET /` baseline, a sampler
of the gateway's process once a second, and for rgw-go a CPU profile of each
profile's last phase and its GC trace. It writes
`hack/bench/out/<release>/gateway-<gateway>-<UTC time>/` and its `REPORT.md`;
the command's package doc lists the profiles, the files and the settings, and
`go run ./hack/bench/gateway --help` every flag and its environment name.
`PROFILES="small-get noop"` runs some profiles only, and `BENCH_SMOKE=1` runs
every profile with 5 s phases at one concurrency, which proves the tooling and
measures nothing; any other value of it is refused. Every setting is checked
before anything touches the cluster, and every command the run starts is
killed past a bound of its own. A run that fails or is interrupted still
removes the bench user, stops the rgw-go it started and compacts the OSD.

`make elbencho` builds the pinned [jhoblitt/elbencho](https://github.com/jhoblitt/elbencho)
fork statically in an Alpine container (`RGW_GO_ENGINE` picks the engine) into
`hack/bench/out/elbencho`, and `bench-gateway` builds it when it is missing.
The build takes about 8 minutes and keeps about 5 GiB of source and external
libraries in `hack/bench/out/elbencho-src`, which `ELBENCHO_SRC` moves to a
filesystem with room. The Alpine image is pinned by digest and its packages
are not upgraded, but Alpine keeps only each package's latest release, so a
later build can link a newer libcurl; `hack/bench/out/elbencho.commit` records
the image and the toolchain and library versions a binary was built with,
and every run copies them into its `env.json`.

elbencho signs as the user `bench`, created with fixed keys. A radosgw run
refuses to start while an rgw-go runs against the cluster, and both refuse a
gateway whose entity lacks the parity options, so `make rgw-go-up` runs once
first; an rgw-go run starts its own rgw-go and stops it when it ends.

The clusters' one OSD holds 10 GiB, and the parity options leave no garbage
collector running. A run first collects any garbage left queued and reads the
data pool's free space, and refuses to start when a profile's dataset, the
most object data the profile holds at once, is more than half of it; the
other half is room for the bucket index and the OSD's own metadata. The
defaults hold at most 3 GiB at once, small-put's 3 000 objects of 4 KiB per
thread at 256 threads, and 2 GiB in the large profiles, which write one
64 MiB object per thread at 32 threads and remove it before the next write.
After each profile the run removes every bench object and collects the
garbage, and when it ends it removes the user with its buckets, collects the
garbage and compacts the OSD, so that `make gate` passes after it and the
next run does not start on the tombstones its deletes left in the OSD's
RocksDB. `BENCH_PLAN=1 BENCH_IDLE_AVAIL=<bytes>` prints each profile's
dataset against that much free space without touching a cluster, and `make
bench-plan-check`, part of `make check`, fails when the defaults need more
than 8 220 119 040 B, the least the data pool could take on an idle cluster
in the seam sweeps. `BENCH_IDLE_AVAIL` without `BENCH_PLAN=1` is refused
rather than starting a run.

Both gateways answer with TCP_NODELAY on. Rook starts the radosgw with the
frontend `beast port=80`, and beast turns TCP_NODELAY on only for
`tcp_nodelay=1`; the chart values' `rgwCommandFlags` give the radosgw `beast
port=80 tcp_nodelay=1`, which Rook passes after its own frontend, so the
radosgw keeps it. rgw-go keeps Go's TCP_NODELAY unless its frontend sets
`tcp_nodelay` to anything else (`docs/exclusions.md`, "TCP options"). A run
refuses a gateway that answers with it off and records the gateway's
frontends and their TCP_NODELAY in `env.json`; `report gateway` names them
in its header and refuses to compare runs that differ in it, or in their
profiles or settings. Under Rook's own frontend the Tentacle radosgw answered
each anonymous `GET /` after a connection's first in about 41 ms, the first
in half a millisecond; with `tcp_nodelay=1`, every one in 0.2 to 0.5 ms.

A cluster brought up before the chart values carried `rgwCommandFlags` takes
them with `make cluster-up`, whose helm upgrade restarts the radosgw, then
`make populate`, since `cluster-up` rewrites `out/<release>/` and so drops
`manifest.json`. The measured runs, Squid first and each release's radosgw
before its rgw-go, on a quiet host:

```sh
export CGO_CFLAGS=... CGO_LDFLAGS=...   # as for make rgw-go-up; leave GOMAXPROCS unset
make elbencho
make cluster-up RELEASE=squid && make populate RELEASE=squid  # a cluster without rgwCommandFlags only
make rgw-go-up RELEASE=squid && make rgw-go-down RELEASE=squid
make bench-gateway RELEASE=squid GATEWAY=radosgw     # about 40 minutes
make bench-gateway RELEASE=squid GATEWAY=rgw-go      # about 40 minutes
make gate RELEASE=squid
make cluster-up RELEASE=tentacle && make populate RELEASE=tentacle  # likewise
make rgw-go-up RELEASE=tentacle && make rgw-go-down RELEASE=tentacle
make bench-gateway RELEASE=tentacle GATEWAY=radosgw  # about 40 minutes
make bench-gateway RELEASE=tentacle GATEWAY=rgw-go   # about 40 minutes
make gate RELEASE=tentacle
go run ./hack/bench/report gateway --dir hack/bench/out/squid/gateway-radosgw-<time> \
  --dir hack/bench/out/squid/gateway-rgw-go-<time> --out hack/bench/out/squid/gateway.md
go run ./hack/bench/report gateway --dir hack/bench/out/tentacle/gateway-radosgw-<time> \
  --dir hack/bench/out/tentacle/gateway-rgw-go-<time> --out hack/bench/out/tentacle/gateway.md
```

The durations scale the `BENCH_SMOKE=1` runs on Tentacle to the full phases,
and are estimates: small-put, small-get and noop run six 60 s phases each,
about 6.5 minutes apiece with small-get's read set of 102 400 objects written
first; large-put and large-get about 2 and 3 minutes; list about 4.5 minutes,
most of it writing its 102 400 keys; 2 to 3 minutes to start, render, remove
the user and compact the OSD, a minute more for rgw-go, which `rgw-go-up.sh`
builds and starts; and the removals after each profile, which the smoke ran
at about 1 600 objects a second: about a minute each for small-get's and
list's 102 400, and for small-put's however many its phases wrote, up to
768 000. A phase that finishes its objects first, as small-put's lower
concurrencies do, is shorter.

What shapes the numbers before they are read:

- The gateways sit in different places. radosgw runs host-networked in the
  rooket node, a container behind the engine's bridge network, under its
  pod's 2 GiB memory limit and no CPU limit; rgw-go runs on the host and
  listens on loopback, and every RADOS operation it sends crosses the bridge.
  elbencho and httpload run on the host for both.
- Only the radosgw run pays for its samples on the same host: each is a
  `rooket kubectl exec`, where rgw-go's are read from `/proc`. Only rgw-go
  runs with `GODEBUG=gctrace=1`, and under its CPU profiler during each
  profile's last phase.
- A 2 s gap after every phase and every preparation keeps their CPU out of
  the next phase's, but not their threads out of rgw-go's thread high-water:
  Go parks an idle OS thread rather than ending it, so the column is the most
  threads the run has needed by then, not that phase's own.
- The GC figures cover the whole profile, its preparations and removals
  included.
- A multipart PUT's row counts parts: elbencho counts one IO per UploadPart
  and none for the upload's Create and Complete.

## The derived image

`make image RELEASE=<release>` builds the image Rook runs rgw-go in and prints
its reference, `ghcr.io/jhoblitt/rgw-go:local-<tag>` for the release's pinned
`cephImage.tag`. That name is never pushed, so a kind node with the image
loaded never tries to pull it. The image is the release's Ceph image with
`/usr/bin/rgw-go` added, `/usr/bin/radosgw` a symlink to it and Ceph's own
radosgw kept as `/usr/bin/radosgw.ceph`; every other Ceph binary is the
base's, because under Rook this image is every daemon's and the toolbox's.

```sh
img=$(make -s image RELEASE=squid)
RGW_GO_IMAGE_TEST=1 make -s image RELEASE=tentacle   # and check what it built
```

rgw-go is built with cgo in a builder made from the same Ceph image
(`hack/image/Containerfile.builder`), so it links against the librados and
glibc it runs with. `hack/image/cgo-build.sh` runs any `go` command in that
builder for the Ceph tag in `RGW_GO_CEPH_TAG`, which a release's chart values
must pin, since they name the image's repository too; it takes `GOOS`, `GOARCH`,
`GOAMD64`, `CGO_ENABLED` and `GOFLAGS` from its environment as a goreleaser
build's `tool` must. The first build of a release's builder pulls from
quay.io and docker.io and installs `librados-devel` from download.ceph.com;
the builder is then reused until `Containerfile.builder` or the release's Ceph
image changes. `RGW_GO_ENGINE` picks the container engine, podman when
installed, else docker; rootless Docker is not supported.

`RGW_GO_IMAGE_TEST=1` checks that rgw-go carries the checkout's version stamp,
that `/usr/bin/radosgw` resolves to it, that every library it links is in the
image, that `radosgw.ceph`, `ceph`, `radosgw-admin` and `rados` print the base
image's versions, and that the image names its base. `hack/image/out/`,
ignored by git, holds each release's binary and each builder's Go build cache.

## s3-tests

```sh
make gate RELEASE=squid                                # first: the gate counts the zone's users
make s3tests RELEASE=squid GATEWAY=radosgw RUN=try1    # GATEWAY=rgw-go runs the same set against rgw-go
make s3tests RELEASE=squid GATEWAY=radosgw SET=gate    # the gate set, gate-only tests included
```

`hack/s3tests/run.sh` runs s3-tests, the suite ceph/ceph carries in
`src/test/rgw/s3-tests` now that ceph/s3-tests takes no changes, at the commit
`hack/s3tests/PIN` pins (`run.sh --print-commit`) on the `rgw-go-s3tests`
branch of [jhoblitt/ceph](https://github.com/jhoblitt/ceph): ceph/ceph `main`
with the suite fixes rgw-go needs on top. `PIN` is the single place the pin is
set: exactly 40 lowercase hex digits and a newline, which `run.sh` and the
recorder, `hack/parity`, read the same way, by that one rule.
A commit is merged into that branch before the change that pins it, and the
branch only ever gains commits, so no rewrite can take a pinned commit away. It runs
against the release's radosgw, or against the rgw-go whose URL
`out/<release>/rgw-go.endpoint` holds. The set
is `test_s3.py` and `test_headers.py` minus the tests of the s3-tests markers
`hack/s3tests/excluded-markers.txt` lists, of features not implemented yet and
of `docs/exclusions.md`, plus the tests `hack/s3tests/include-phase2.txt`
names, and minus the tests `hack/s3tests/deselect-phase2.txt` and
`deselect-phase3.txt` name. A marker stays listed while it also marks tests
of a feature not served yet, and the include list names the tests it removes
that phase 2 serves; a deselect list names tests that call a later phase's
feature but carry none of those markers. That is the gate set, which a phase
gate and every recording run. The pr set, which `SET=pr`, the default, runs,
and with it the `s3tests` workflow on every pull request, is the gate set
less the tests `hack/s3tests/gate-only.txt` names: tests only a gate and a
run on demand afford, such as the time-driven lifecycle tests, which sleep
while their rules come due. `SET=gate` runs the gate set. Both gateways run
the same set, `fails_on_rgw` tests included, since both must fail those
alike. The selection is a Go command, `hack/s3tests`, which `run.sh` builds
into `bin/s3tests` from its own tree each time it runs: `run.sh` collects the
tests with pytest twice, under the expression the command makes of the
markers and without it, which alone holds the include list's tests; the
command picks the set's node ids from the two collections and the lists, and
pytest runs exactly those ids, in collection order (`go doc ./hack/s3tests`).
`run.sh` gives the command every flag, so that neither an `S3TESTS_` variable
the command would read nor a config file one names changes a run.

`run.sh` creates the s3-tests users with `radosgw-admin`, with fixed keys so
that every run writes the same conf: `s3tests-main`, `s3tests-alt`,
`s3tt$s3tests-tenant`, `s3tests-iam` with the `user-policy`, `roles` and
`oidc-provider` caps, and `s3tests-root1` and `s3tests-root2`, the roots of
the accounts `RGW11111111111111111` and `RGW22222222222222222`. They stay for
later runs, and the gate fails on a zone that holds users the manifest does
not list, so `make gate` runs before `make s3tests`, never after. Before and
after each run `run.sh` removes the buckets the three s3 users own: s3-tests
removes only those of its own run, and `test_list_buckets_paginated` wants
the main user to own none. It then collects the tails of the objects the run
deleted, several hundred megabytes, with `radosgw-admin gc process
--include-all`: radosgw's own collector waits two hours, and the parity
settings turn it off.

For `make gate` to pass again, remove the users with their buckets, then
the accounts, then the tails the removal leaves for the collector, which
the gate's data pool check would count, and last the account roots'
`<uid>.buckets` objects in `users.uid`, which the gate counts too. Either
gateway's quota user sync writes that object for every user when the
gateway starts, and radosgw's user removal leaves it for a user in an
account (`docs/ceph-upstream-bugs.md`, "radosgw's removal of an account
user leaves the bucket list its quota sync wrote"), so it is there only
when a gateway started while the roots existed. `run.sh` creates the users
and accounts again on its next run.

```sh
export ROOKET_NAME=rgw-go-squid
admin() {
	rooket k -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin "$@" \
		--rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore
}
for uid in s3tests-main s3tests-alt 's3tt$s3tests-tenant' s3tests-iam s3tests-root1 s3tests-root2; do
	admin user rm --uid "${uid}" --purge-data
done
admin account rm --account-id RGW11111111111111111
admin account rm --account-id RGW22222222222222222
admin gc process --include-all
uids() {
	rooket k -n rook-ceph exec deploy/rook-ceph-tools -- rados -p ceph-objectstore.rgw.meta -N users.uid "$@"
}
for uid in s3tests-root1 s3tests-root2; do
	if uids stat "${uid}.buckets" >/dev/null 2>&1; then uids rm "${uid}.buckets"; fi
done
```

Each run leaves `hack/s3tests/out/<release>-<gateway>-<run>.xml`, the junit
report `hack/parity` reads, with `.log` (pytest's output), `.conf` (the
s3-tests conf), `.freeze` (the Python version and the packages the venv
held; s3-tests' `requirements.txt` pins none), `.ids` (the pytest node ids
the run selected, one a line, which `hack/parity diff --selection` reads)
and `.gate-only.ids` (those of the gate set's tests `gate-only.txt` names,
whichever set ran, which `--gate-only` reads) beside it. `out/src` holds the
pinned commit alone, fetched without file contents and checked out sparsely,
so of all of Ceph only the suite in `src/test/rgw/s3-tests` is on disk, with
the files directly in its parent directories and at the top level, which
cone mode always keeps: a few megabytes. pytest runs from the suite's
directory. It and the venv
in `out/venv` are rebuilt when the pin changes, which "Moving the s3-tests
pin" below covers. `run.sh` exits 0 when pytest ran the whole set, whatever
the tests' outcomes, and nonzero when it could not.

`run.sh`'s fourth argument is the set, `pr` or `gate`, which `make s3tests
SET=` passes. A fifth, a file of pytest node ids
(`s3tests/functional/test_s3.py::test_x`) one a line, `#` starting a comment,
runs only those tests, and a test's id without its parametrization runs each
of them; `make s3tests TESTS=FILE` passes it. `run.sh` stops before any test
runs when an id is not in the set: no such test at the pin, the markers or
a deselect list remove it, or, for the pr set, `gate-only.txt` names it.
Such a run's `.ids` holds only those tests, so a `hack/parity diff
--selection` of it by hand lists every other ledger test as dropped from the
pr set, as expected.
`make s3tests-record` and `make s3tests-record-tests` run the gate set
whatever `SET` says: a full record retires every ledger test it did not run,
which would retire the gate-only tests, and a TESTS file of a record may
name one.

To try a change to s3-tests before it is pinned, `S3TESTS_REPO` and
`S3TESTS_COMMIT` in the environment make `run.sh`, and every target that
calls it, run another clone of Ceph, a local clone's path among them (a
relative path is taken from the directory `run.sh` starts in, which is the
repository root under `make`), at another commit, which must be a full
commit id. `run.sh --print-commit`
prints that commit, and on stderr that it came from the environment, not
`PIN`, so a result recorded from such a run carries it; `make s3tests-parity`
refuses to compare it with the ledger, whose chain ends at the pin, and
`make s3tests-record` refuses to record it. `bin/parity diff
--allow-meta-drift` compares the outcomes anyway. A
local clone serves the fetch without file contents only with
`uploadpack.allowFilter` set: without it git fetches every file of the
commit, which a clone made without file contents cannot serve.

```sh
git -C "$HOME/src/ceph" config uploadpack.allowFilter true
S3TESTS_REPO=$HOME/src/ceph S3TESTS_COMMIT=$(git -C "$HOME/src/ceph" rev-parse HEAD) \
	make s3tests RELEASE=squid GATEWAY=radosgw RUN=try1
```

Each line of `excluded-markers.txt` is a marker, under the comment naming
its feature's phase or its exclusion. Each line of a deselect list is a pytest
node id and, after `#`, the calls that need the feature; it removes every
parametrization of that test. Each line of an include list is a pytest node
id, or a test's id without its parametrization for every parametrization, and
after `#` why phase 2 runs it, under its area's comment; it adds the tests it
names. `run.sh` stops before any test runs when a deselect line names no test
the markers leave, because it was renamed, removed or marked at the pinned
commit, or when a deselect line would also remove a test of the set it does
not name were it given to pytest's `--deselect`, which removes every node id
that starts with its argument. It stops too when an include line names no
collected test, because it was renamed or removed, or names a test the
markers leave, and when an include line and a deselect line name the same
test. Each line of `gate-only.txt` is a pytest node id, or a test's id
without its parametrization for each parametrization the gate set holds, and
after `#` why it waits for the gate, under its area's comment; `run.sh` stops
when one names no test of the gate set, for either set. The command names
every such line before it stops.
`run.sh --print-deselect` prints a digest of the listed ids, each include
line's after a `+`, which each recorded entry carries in its meta; with no
include line it is the deselect lists' digest alone, and the comparison no
longer checks it. The gate-only lines stay out of it, so a run of either set
carries the same digest. The PR that lands a later phase's feature deletes
its lines and its markers' lines, adds include lines for its tests that a
marker still listed removes, and gate-only lines for those the pr set cannot
afford, and records the tests they free in both ledgers, as "Adding tests"
below does.

## Parity baselines

`test/s3tests/baseline/<release>.json` is radosgw's baseline on that
release's cluster: a ledger, which only ever grows. Its chain names each
s3-tests commit the suite has been pinned at, and each test holds a list of
entries, each an outcome radosgw gave, stamped with the commit, the Ceph
version and the time it was recorded at, the number of runs, whether the
runs disagreed, which makes the test `unstable`, and why the entry was
needed. A test's last entry is its baseline. rgw-go's run is compared with
it, and must match every outcome but an unstable one's, failures included.
`hack/parity`'s package doc holds the ledger's schema and the comparison's
rules.

Nothing in a ledger is edited, removed or reordered. A record appends an
entry only for a test whose outcome, unstable flag or Ceph version changed,
or whose entry an s3-tests change made stale, and an entry that changes the
outcome or the unstable flag, or retires a test the set no longer runs,
needs a reason. That rule is the guard against a radosgw regression
entering the baseline unremarked: a failure recorded as the baseline lets
the same failure on rgw-go pass. A reason that starts `unreviewed:` marks a
draft, which the comparison accepts but nothing lets into the repository.

A reason must say something, a letter or a digit at least, and no ledger text
may hold a control or format character, a zero-width space or a bidi override
among them.

`make check` validates the four ledgers. The `ledgers` workflow runs
`hack/parity check --base --head` on every pull request, a required check that
reports even when no ledger changes; the ledger directories it reads and the
`run.sh` a pin's proof reads come from the default branch's build and the base
branch's history. It fails a pull request that removes a ledger, changes
what one holds other than by appending, replaces one with a link, or adds one
anywhere else; that leaves an appended change or base pin without its reason
or with a draft's; or that appends an additive or reworded s3-tests pin which
`hack/parity pin`'s proof, run again against a fetch of `jhoblitt/ceph`
without file contents, does not admit as it says. Its summary lists each
changed ledger's appended pins, each base pin with its reason and every
outcome it re-stamps, counting those it carries forward unchanged, and every
appended entry that changes an outcome or an unstable flag, with its reason.
Everything `hack/parity` prints escapes control characters and holds no GitHub
Actions workflow command, so text in a ledger reads back in the job's log as it
was written.

These are a regression test's integrity checks against honest mistakes: they
keep the baseline append-only, require a reason for a changed outcome, keep a
pin bump that is not purely additive from carrying old outcomes over, and keep
pins on `rgw-go-s3tests`'s history. Every s3-tests pin the pull request brings
in, in a ledger or in `hack/s3tests/PIN`, must be on that history, as "Moving
the s3-tests pin" describes, and `PIN` must hold it by the rule.

The workflow runs on `pull_request_target`, so GitHub runs it as the default
branch holds it: every pull request is checked by the rules already merged,
and a change to the workflow takes effect once it merges. The job builds the
checker from the default branch, with `setup-go`'s cache off and module and
build caches of its own, then fetches the pull request's head by its commit
id and reads that commit's ledgers from git, against the merge base of the
head and the base branch. `pull_request_target` runs with the repository's
token, so the job reads the pull request and never checks it out or runs
it.

### Recording

```sh
make gate RELEASE=squid            # first: the gate counts the zone's users
make rgw-go-up RELEASE=squid       # sets the parity options on radosgw's entity too
make s3tests-record RELEASE=squid  # REASONS=FILE for changed outcomes; OUT=FILE appends to another ledger
```

`make s3tests-record` runs the gate set, whatever `SET` says, against radosgw
twice, as `RUN=base1` and `RUN=base2`, and appends both runs to `OUT`, by
default `test/s3tests/baseline/<release>.json`, with the meta `release`,
`ceph_version` from `manifest.json`, `gateway=radosgw`, `s3tests_commit`,
`deselect` and `pytest_freeze`. It refuses to start before `make rgw-go-up`,
so that radosgw records under the parity options rgw-go is compared under.
A record that would change an outcome or an unstable flag, or retire a test,
stops and names each one; nothing is written. Each needs a line in
`REASONS`, a file of a test id, blanks and the reason, one a line:

```text
s3tests.functional.test_s3::test_x radosgw 19.2.7 answers 400 where 19.2.6 answered 200
```

`pytest_freeze` is the first twelve hex digits of the SHA-256 of the
`.freeze` file beside the last run, a record of the venv rather than
something the comparison checks.

The `s3tests` workflow's `record-baseline` input runs the same target on the
runner with `DRAFT`, appending to a copy of the committed ledger as a draft:
each change that needs a reason gets `unreviewed: recorded by <run URL>`.
The job summary lists those entries, the workflow compares rgw-go with the
copy, and uploads it as the artifact `s3tests-baseline-<release>`. A record
there and one here differ only in `recorded`, in `pytest_freeze` when the
hosts' Python or packages differ, and in any outcome the two hosts' radosgw
give differently, which the summary shows rather than letting it in
silently. `make check` and the `ledgers` job refuse the draft until each
`unreviewed:` reason is replaced by the change's own, and a reasons file
cannot give one. Every reader holds a ledger to the exact form
`hack/parity` writes, one pin or entry a line, so that its diff reads as the
tools read it; `go run ./hack/parity fmt FILE` rewrites a hand-edited ledger
into that form.

### A full record

A full record, at the pin, is due when the Ceph pin moves: every entry
under the old version is stale, and the comparison names each test so
until the record appends one under the new version. It also enters the tests
a deselect list stops removing and retires those one starts removing. When
the s3-tests pin moves to a commit `make s3tests-pin` cannot prove keeps the
outcomes, write the new commit to `hack/s3tests/PIN` by hand and record in full with
`PIN_REASON`, a line saying why the move cannot be proven: the record appends
a base pin carrying it, after which every earlier entry is stale until the
record replaces it. Such a pin re-stamps every outcome at a source no proof
ties to the one before, so a record that would append one without
`PIN_REASON` stops, and `DRAFT` gives it the draft's reason, which `make
check` refuses like any other.

### Moving the s3-tests pin

```sh
make s3tests-pin CLONE="$HOME/src/ceph" NEW="$(git -C "$HOME/src/ceph" rev-parse rgw-go-s3tests)" TESTS=added.txt
```

Every s3-tests pin, in `hack/s3tests/PIN` or a ledger, must be the tip of
`jhoblitt/ceph`'s `rgw-go-s3tests` or an ancestor of it, so that pins stay on
the branch's history: a commit of a fork, of another branch or of none is
refused as a likely mistake, by `make s3tests-pin` and by the `ledgers`
workflow, each asking a treeless fetch of the branch. The proof below is a
regression test's check against honest mistakes: it keeps a pin bump that is
not purely additive from carrying the old outcomes over, and stays
conservative, so whatever it does not recognize needs a full record.

`make s3tests-pin` runs `hack/parity pin`, which moves the pin in `PIN` and
appends it to both ledgers when `NEW` is on `rgw-go-s3tests` and a clone of
Ceph that holds both the pin and `NEW` proves the move keeps every outcome
they hold:

- additive, when `NEW` descends from the pin and the suite's directory only
  gains tests: no line changed or removed, no file added or removed, and
  nothing changed but test modules (`test_*.py`) and `pytest.ini`. A test
  module may gain only whole new tests between its existing statements: a
  one-line `def test_<name>(<bare names>):`, a name the module did not hold,
  with its body, every line of it indented, under any number of bare
  `@pytest.mark.<name>` decorators, each followed by a comment at most, as
  the suite writes them, and each naming a marker `pytest.ini` registers at
  `NEW`, so that no built-in such as `parametrize` or `skipif` passes. A
  mark's arguments refuse, literal or not: Python evaluates them when pytest
  imports the module, and pytest judges them when it collects it, where a
  `parametrize` whose names and values disagree fails the module and every
  test in it. Any other top-level line, a fixture, an
  import, an assignment, a class, a comment, refuses, as does any line added
  inside an existing test or statement. An added line must be printable ASCII
  ending at a line feed, with no tab, no `;` and no backslash at its end, and
  no string or continuation may carry it across lines: the proof does not
  parse Python, and these keep its reading of each line Python's; a test
  they refuse needs a full record. `pytest.ini` may gain only markers, with
  no control character or line separator, which pytest's ini reader takes
  for a line break. It writes the node ids of the tests it adds to `run.sh`'s
  test files to `TESTS`. The proof cannot judge what it would have to run
  Python to see: what the new tests do when they run.
- reworded, when the commits on the two lines since they met are as many,
  with the same tree at each, as rewording commits, for example to sign them
  before they are pushed, leaves them.

Anything else it refuses, naming each line and file that keeps the move from
being additive: that needs a full record. Merge `NEW` into `rgw-go-s3tests`
before the change that pins it: `hack/parity pin` and the `ledgers` job both
refuse a commit the branch does not hold, and the job proves the pin again
from there.

### Adding tests

A test added to s3-tests needs only its own record, not the whole set
again. Push it to `rgw-go-s3tests`, then:

```sh
make s3tests-pin CLONE="$HOME/src/ceph" NEW=<commit> TESTS=added.txt
make gate RELEASE=squid && make rgw-go-up RELEASE=squid
make s3tests-record-tests RELEASE=squid TESTS=added.txt
```

and the last two lines again with `RELEASE=tentacle`. Delete from
`added.txt` any test the set's markers remove, which `run.sh` refuses.
`make s3tests-record-tests` runs the listed tests against radosgw twice, as
`RUN=tests1` and `RUN=tests2`, and appends them alone; every other test
keeps its entries. A new test's first entry needs no reason. Commit
`run.sh` and both ledgers together.

## Comparing rgw-go with the baseline

```sh
make gate RELEASE=squid           # first: the gate counts the zone's users
make rgw-go-up RELEASE=squid
make s3tests-parity RELEASE=squid
make s3tests-parity RELEASE=squid SET=gate        # the gate set, at a phase gate
make s3tests-parity RELEASE=squid BASELINE=path/to/just-recorded.json
make rgw-go-down RELEASE=squid
```

`make s3tests-parity` runs the pr set, or with `SET=gate` the gate set,
against the rgw-go `make rgw-go-up` started, as `make s3tests GATEWAY=rgw-go
RUN=parity`, records the run in `out/<release>/s3tests-rgw-go.json` with the
release, the Ceph version from `manifest.json`, the s3-tests commit and the
deselect digest, and compares it with `BASELINE`, by default
`test/s3tests/baseline/<release>.json`. It needs a populated cluster, for
`manifest.json`. A run of the pr set is compared over the tests it selected
alone, `bin/parity diff --selection` reading the run's `.ids` and
`--gate-only` its `.gate-only.ids`: a ledger test outside the selection that
the run lacks is not compared, so it does not fail the run. A gate-only test
passes silently; any other such test, unless retired, is a test the pull
request's selection no longer runs, such as one a new deselect or marker
line, or a deleted include line, removes, and the comparison lists it after
the differences, under `dropped from the pr set: <n> ledger tests ...`, as
an indented `<test>: baseline <outcome>`, and counts it in its summary when
it fails. With `SUMMARY=FILE` it also appends that list, when it is not
empty, to FILE as markdown under the same heading; the `s3tests` workflow
points it at the job summary, so a pull request's reviewer sees the tests it
drops on a green run. A run of the gate set is compared over every test of
the ledger, so it fails on a dropped test as missing, or as stale when its
last entry is, until a record retires it; it skips one whose last entry is
current and unstable, which the next full record of the gate set retires,
stopping for its reason. A selected test the run lacks is still missing, and
one the ledger lacks has no baseline entry. It prints one line per test
whose outcome differs, and `<test>: baseline stale, re-record` for each
whose last entry was recorded under another Ceph version or before the
chain's last base pin, and fails unless there is none. `hack/parity` runs
as a built binary, `bin/parity`, so make's error line carries the
comparison's own status: 1 for a test that differs or is stale, 2 for a run
at another s3-tests commit than the ledger's head, or for another release.
A stale test and a moved pin are recorded as under "Parity baselines". make
itself exits 2 for either status.

The comparison skips the tests `test/s3tests/known-differences-<release>.txt`
lists: one regular expression per line over `hack/parity`'s test ids
(`s3tests.functional.test_s3::<test>`), each under a comment naming the
`docs/exclusions.md` entry that decides the difference. It fails on a line
that matches no test, and on one whose test no longer differs, so the list
cannot outlive its differences; a pr set's comparison takes a line that
matches only a ledger test outside the run's selection, a gate-only or a
dropped test's, as matching, unless a comparison of the gate set would skip
that test too: its last entry retired, or current and unstable. A line
starting with `~ ` is the other kind of entry, for a test whose outcome on
rgw-go depends on timing, as its exclusion says: its test is skipped
whether it differs or agrees, and only an entry that matches no test fails.
No entry on either list uses that form at present; every entry is plain.
Only a recorded exclusion goes there. A difference that is not one is a bug
in rgw-go, and a test that needs a later phase's feature goes on that
phase's deselect list, which removes it on both gateways. Squid's list is
mostly the Tentacle feature level rgw-go serves on Squid too, which Squid's
radosgw fails.

The `s3tests` workflow (`.github/workflows/s3tests.yml`) runs `make
rgw-go-up` and `make s3tests-parity`, the pr set, against a Squid cluster on
every pull request that changes the S3 path, and against either release on
demand, so CI and a local run judge alike; its `s3-tests` job is the check
to require.
It runs no gate, and needs none: nothing after it counts the users.

### A unit's tests

`test/s3tests/<unit>-groups.txt` names the tests that judge one area of
the S3 path, one extended regular expression per line over `hack/parity`'s
test ids, so that `grep -E -f` keeps a diff's lines for that area alone.
The write path's, `w-groups.txt`, selects PUT, DELETE, COPY, DeleteObjects,
the conditional writes and the object ACL and tagging subresources; the
multipart uploads', `p-groups.txt`, selects CreateMultipartUpload,
UploadPart, UploadPartCopy, CompleteMultipartUpload, AbortMultipartUpload
and ListMultipartUploads, and partNumber reads of the result. After
`make s3tests-parity`:

```sh
go run ./hack/parity diff --baseline test/s3tests/baseline/squid.json \
	--candidate hack/rooket/out/squid/s3tests-rgw-go.json \
	--selection hack/s3tests/out/squid-rgw-go-parity.ids \
	--gate-only hack/s3tests/out/squid-rgw-go-parity.gate-only.ids | grep -E -f test/s3tests/w-groups.txt
```

The diff runs without `--known`, so it prints the recorded differences
too: every line the filter keeps is a known-differences entry, whose
exclusion decides it, an upstream Ceph defect, which goes in
`docs/ceph-upstream-bugs.md`, or a bug in rgw-go, or a test dropped from the
pr set. `--selection` and `--gate-only` keep the gate-only tests a run of the
pr set leaves out from showing as missing; a run of the gate set needs
neither.

## go-ceph's rgw/admin suite

```sh
make admin-suite RELEASE=squid GATEWAY=radosgw RUN=try1       # GATEWAY=rgw-go runs it against rgw-go
```

`hack/admin/run.sh` runs [go-ceph](https://github.com/ceph/go-ceph)'s admin
ops suite, `TestRadosGWTestSuite` in `rgw/admin`, at the tag it pins
(`GO_CEPH_TAG`), against the release's radosgw or against the rgw-go whose URL
`out/<release>/rgw-go.endpoint` holds. It fetches the tag into
`hack/admin/_out/.go-ceph`, which git ignores; `golangci-lint fmt` walks a
directory whose name starts with `_`, unlike go's `./...`, but skips one
that starts with `.`, so `make check` never formats the checkout. Before
each run it resets the checkout to the tag and applies two patches:

- `hack/admin/endpoint.patch`: upstream hard-codes the suite's endpoint, and
  the patch makes `SetupConnection` read `RGW_GO_ADMIN_ENDPOINT`.
- `hack/admin/subtest-t.patch`: upstream's subtests assert on their method's
  test, `suite.T()`, so a subtest passes whatever it checks and a failure
  shows only on its method. The patch makes every subtest assert on its own
  `t`, so `hack/parity` compares each subtest.

Bumping the tag regenerates both patches with `git diff` in a checkout of the
new tag and records both ledgers in full, with `PIN_REASON` naming the bump,
which appends a base pin at the new tag, carrying it, to each.

`go test` skips, on both gateways, the subtests
`hack/admin/deselect-phase2.txt` names: subtests that set up what they read
through the admin API with a phase 2 S3 feature, `PutBucketVersioning` or a
CreateBucket with object lock, which rgw-go answers 501 until phase 2. Each
line is a test id as `go test -json` reports it; `run.sh` turns the lines
into one `-skip` pattern, stops before the run when a line names no method
or subtest of the checkout, and after it when the run still reports one.
`run.sh --print-deselect` prints a digest of the listed ids, which each
recorded entry carries in its meta. The PR that lands a feature deletes its
lines and records both ledgers, which enters the subtests it frees.

`go test` runs the suite with `-tags ceph_preview`, which the account tests
need, and with `CEPH_CONF` naming `out/<release>/ceph.conf`, since
`TestGetInfo` connects through librados and compares the cluster's fsid with
the one the gateway reports; `run.sh` refuses to run without that conf.
go-ceph picks its expectations for a release from `CEPH_VERSION`, as its CI
sets it. At this tag the only ones that differ between `squid`, `tentacle`
and `main` are `TestAccount`'s skips of GET and DELETE on `/admin/account`,
which it takes for every release up to `tentacle`:

- Squid runs as `squid` and keeps the skips: v19.2.7's radosgw checks a cap
  named `account` for both calls, which no caps command grants, so they
  answer 403 to the `admin` user.
- Tentacle runs as `main`: v20.2.4's radosgw checks `accounts`, which the
  `admin` user holds, so both calls run and are compared.

The suite's packages are cgo, so `CGO_CFLAGS` and `CGO_LDFLAGS` come from the
caller's environment where the librados headers are not installed.

The suite signs as the user `admin`, with the example keys go-ceph's CI gives
it (access key `AKIAIOSFODNN7EXAMPLE`) and the caps
`buckets=*;users=*;usage=read;metadata=read;info=read;accounts=*`. `run.sh`
creates it, or adds the caps when it exists, and stops when it exists without
the key. Before and after each run it removes what the suite creates and
removes again, which a run that stops partway leaves behind: the users
`leseb`, `test`, `test-user1`, `test-user2` and `test-user-bucket-rename`, the
buckets `test`, `bucket-object-lock`, `initial-name` and `renamed-name`, the
account `RGW12345678901234567`, and every key of `admin` but its own. The
`admin` user signs with AWS's documented example keys and holds the users,
buckets and accounts caps, so a trap removes it with `hack/admin/remove-user.sh`,
which also collects the garbage the removal leaves, whenever `run.sh` exits,
passed, failed or stopped partway. `make gate` passes after a run.

For `GATEWAY=radosgw` the script sets, on the radosgw's own config entity, the
usage-log options go-ceph's CI sets and `TestUsage` needs:

| Option | Set to | Default |
|---|---|---|
| `rgw_enable_usage_log` | `true` | `false`, but Rook already sets `true` |
| `rgw_usage_log_tick_interval` | `1` | `30` |
| `rgw_usage_log_flush_threshold` | `1` | `1024` |

The entity is `client.rgw.` and the radosgw's id in the service map,
`client.rgw.ceph.objectstore.a` on these clusters: Rook names it after the
radosgw's deployment, `rook-ceph-rgw-ceph-objectstore-a`, with the dashes
after `rook-ceph-rgw` mapped to dots. The script stops unless `ceph config
show` reports that entity running with the frontends the radosgw reports, so
the options cannot land in a section no daemon reads. They persist in the
monitors' config store. When the script changes one, it restarts the
radosgw's deployment, which interrupts any other client of the radosgw, and
it then checks that the running radosgw reports all three. For
`GATEWAY=rgw-go`, `make rgw-go-up` sets them, for both gateways.

Each run leaves `hack/admin/_out/<release>-<gateway>-<run>.jsonl`, the
`go test -json` stream `hack/parity` reads, with `.log`, go test's stderr,
beside it. `run.sh` exits 0 when the suite ran to its end, whatever the tests'
outcomes, and nonzero when it did not: the package failed to build, the test
binary panicked, or the gateway could not be reached.

`test/admin/baseline/<release>.json` is radosgw's ledger for the suite, as
"Parity baselines" describes for s3-tests, its chain the go-ceph tags it has
been pinned at:

```sh
make rgw-go-up RELEASE=squid     # sets the parity options on radosgw's entity too
make admin-record RELEASE=squid  # REASONS=FILE for changed outcomes; OUT=FILE appends to another ledger
```

`make admin-record` runs the suite against radosgw twice, as `RUN=base1` and
`RUN=base2`, and appends both runs to `OUT`, by default
`test/admin/baseline/<release>.json`, with the meta `release`, `ceph_version`
from `manifest.json`, `gateway=radosgw`, `go_ceph_tag` and `deselect`. Like
`make s3tests-record` it refuses to start before `make rgw-go-up`, so that
radosgw records under the parity options rgw-go is compared under, and it
stops on a changed outcome or unstable flag, or a retired subtest, that
`REASONS` gives no reason for. A subtest whose outcome differed between the
runs is `unstable`, and a comparison skips it. The comparison refuses a run
at another go-ceph tag than the ledger's head or for another release, and
names each subtest stale under another Ceph version, so bumping
`GO_CEPH_TAG` or a Ceph pin records both releases' ledgers in the same
change.

Each baseline holds the suite's 82 subtests less the two the deselect list
skips. On both releases radosgw fails
four of them, each because it lists the whole zone and expects only the
suite's own buckets and users, while the populated zone also holds `plain`,
`t1/tenanted`, `alice` and `t1$bob`:

- `TestBucket/list_buckets` expects 1 bucket and `TestBucket/list_bucket_is_now_zero`
  0; the zone lists 3, then 2.
- `TestListBucketsWithStat/list_buckets_with_stat` expects 1 bucket, `test`
  owned by `admin`; the zone lists 3, the first `tenanted` owned by `t1$bob`.
- `TestUser/get_users` expects 2 users; a populated zone lists 4 with the
  suite's `leseb`, and more once other harnesses have added theirs, such as
  the six s3-tests users.

rgw-go must fail them alike. A method whose subtests fail is left to them;
the other methods pass. Squid skips `TestAccount`'s get and delete subtests,
as above, and Tentacle passes them.

### Comparing rgw-go with the baseline

```sh
make rgw-go-up RELEASE=squid
make admin-parity RELEASE=squid
make admin-parity RELEASE=squid ADMIN_BASELINE=path/to/just-recorded.json
make rgw-go-down RELEASE=squid
```

`make admin-parity` runs the suite against the rgw-go `make rgw-go-up`
started, as `make admin-suite GATEWAY=rgw-go RUN=parity`, records the run in
`out/<release>/admin-rgw-go.json` with the release, the Ceph version from
`manifest.json`, the go-ceph tag and the deselect digest, and compares it
with `ADMIN_BASELINE`, by default `test/admin/baseline/<release>.json`. It
prints one line per subtest whose outcome differs and fails unless there is
none; as for `make s3tests-parity`, `bin/parity`'s status is 1 for a
subtest that differs or is stale and 2 for a run at another go-ceph tag than
the ledger's head, or for another release. No subtest differs for a recorded
exclusion at this tag, so there is no known-differences list: a subtest that
needs a later phase's feature goes on that phase's deselect list, and any
other difference is a bug in rgw-go.

## Running the admin gate

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid && make rgw-go-up RELEASE=squid
make admin-suite RELEASE=squid GATEWAY=rgw-go RUN=n1  # go-ceph's suite against rgw-go
make admin-parity RELEASE=squid                       # the same, compared with radosgw's baseline
make admin-gate RELEASE=squid                         # rgw-go's admin API compared with radosgw's
```

and the same with `RELEASE=tentacle`. go-ceph's admin client reaches rgw-go
through `RGW_GO_ADMIN_ENDPOINT`: `hack/admin/run.sh` sets it to the URL in
`out/<release>/rgw-go.endpoint` for `GATEWAY=rgw-go` (`hack/admin/endpoint.patch`
makes the suite read it), and `make admin-gate` reads that file unless the
variable names another rgw-go.

`make admin-gate` runs the specs of `test/integration` labelled `rgw-go`,
`admin_test.go`, which `make integration` leaves out because they need the
rgw-go `make rgw-go-up` started. They sign as the user `rgwgo-admin`, which
holds Rook's admin-ops caps and `info`, `ratelimit` and the write caps the
trim, index check and metadata requests need, and they:

- compare `GetInfo`'s `cluster_id` on both gateways with `ceph fsid`;
- make the admin calls Rook makes, in Rook's order, through go-ceph's client
  against rgw-go, asserting the fields Rook reads;
- send every read the admin API serves, each gateway's own user and account
  creates and changes, an AccessDenied, a NoSuchKey, a NoSuchBucket and a
  405 of each path to both gateways in JSON and XML, and the info and an
  error in HTML, and require the bodies to be equal byte for byte once the
  values two runs of one gateway differ in are masked, keyed on their names:
  the request and host ids, and for the fixtures each gateway creates their
  ids, markers, versions, tags, times and keys. Each request also checks
  `Content-Length`, `Content-Type` and `Accept-Ranges` on both gateways as
  `docs/exclusions.md`, "Admin responses carry a Content-Type on every body",
  says. A request whose answer a `docs/exclusions.md` entry records as
  different names that entry and must differ;
- page a metadata listing through rgw-go to the keys radosgw lists whole;
- read a write rgw-go logged back from its usage log and trim it within 5 s;
- remove buckets with tail stripes through each gateway with
  `purge-objects`, whose tails go to the GC, and with `bypass-gc`, which
  rgw-go removes whole and radosgw leaks one object's tail
  (`docs/ceph-upstream-bugs.md`, "radosgw's admin API bypass-gc removal
  leaks a tail and runs unbounded"); the spec removes the leaked stripe.

Its fixtures are `rgwgo-admin` and users, buckets and accounts named
`admin-gate-*`. The specs remove them, their usage records and the garbage
their removal leaves before and after they run, so `make gate` passes after
them. `rgwgo-admin` holds every admin cap with keys committed in the spec, so
`make admin-gate` also removes it with `hack/admin/remove-user.sh` in a trap
when `go test` ends, however it ends, as `run.sh` does for its `admin`. Neither
user is in an account, so its removal also removes the `<uid>.buckets` object
the gateways' quota sync wrote.
