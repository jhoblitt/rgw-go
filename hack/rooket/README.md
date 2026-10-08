# Disposable Rook clusters

One-worker Rook clusters on kind for the integration specs and the phase 0
gate, one per Ceph release, stood up by
[rooket](https://github.com/jhoblitt/rooket) from a released Rook with the
release's Ceph image pinned. Each runs the chart's `ceph-objectstore` object
store on one OSD, with host networking so that librados and S3 clients on the
host reach its mon, OSD and radosgw. Nothing here touches any other cluster.

```sh
make cluster-up RELEASE=squid     # quay.io/ceph/ceph:v19.2.6 under Rook v1.20.7
make populate RELEASE=squid
make integration RELEASE=squid    # the integration specs
make gate RELEASE=squid           # the phase 0 gate, test/gate/
make s3tests RELEASE=squid        # s3-tests against the radosgw, after the gate
make admin-suite RELEASE=squid    # go-ceph's rgw/admin suite against the radosgw, after the gate
make rgw-go-up RELEASE=squid      # rgw-go on the host at http://127.0.0.1:7481, under the parity settings
make rgw-go-down RELEASE=squid
make cluster-down RELEASE=squid

make cluster-up RELEASE=tentacle  # quay.io/ceph/ceph:v20.2.4 under Rook v1.20.7
make populate RELEASE=tentacle
make integration RELEASE=tentacle
make gate RELEASE=tentacle
make s3tests RELEASE=tentacle
make admin-suite RELEASE=tentacle
make rgw-go-up RELEASE=tentacle
make rgw-go-down RELEASE=tentacle
make cluster-down RELEASE=tentacle
```

Each release is its own rooket cluster, `rgw-go-<release>`, so both can run at
once and neither collides with another rooket cluster on the host. Reach one
with `ROOKET_NAME=rgw-go-<release> rooket k ...`, never with the ambient
`kubectl`; `ROOKET=<path>` makes the targets run a rooket other than the one
on `PATH`.

`make cluster-up` rewrites `out/<release>/` without the `manifest.json` the
gate reads, so `make populate` runs before `make gate` after every
`make cluster-up`, in a fresh worktree too. Against a populated cluster it
writes the same data and manifest again.

## Prerequisites

- rooket at the commit `.github/workflows/integration.yml` pins, with its own
  prerequisites: a container engine, `kind`, `kubectl`, `helm`, and the iSCSI
  tooling. Every `cluster-up` creates the OSD's iSCSI target and every
  `cluster-down` removes it with the disk image, so both need root;
  `rooket sudoers install` removes the prompt.
- The AWS CLI v2, `jq` and `python3`, for `populate.sh`.
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
| `lib.sh` | the Rook release, cluster naming, toolbox and radosgw lookup helpers, and the parity options, that the scripts share |

The chart values pin `cephImage.tag`, a plain `vX.Y.Z` that is the one place
a release's Ceph version is set: `up.sh` checks every daemon runs it and
`populate.sh` records it for the gate. They also turn the dashboard off (with
it on, Rook creates a `dashboard-admin` user in the zone) and drop the
chart's block pool and filesystem, which rgw-go does not use. rooket's
one-worker base already fits the object store's pools to a single OSD. rooket's `rgw` profile is not
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

Rook sets `rgw_run_sync_thread=true` for the radosgw each time it reconciles
the object store, `make cluster-up` against a running cluster among the
times, so after a reconcile the next `make rgw-go-up` sets it and restarts
the radosgw again. The entity and the options stay when rgw-go stops; the
gate counts neither.

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

## The seam microbenchmark

`make bench-seam RELEASE=squid` runs `hack/bench/seam.sh`, which sweeps the
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
release on a 32-thread host. `BENCH_QUICK=1` runs a short sweep, and the
script's header lists its other settings.

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
```

`hack/s3tests/run.sh` runs [ceph/s3-tests](https://github.com/ceph/s3-tests)
at the commit it pins (`run.sh --print-commit`) against the release's radosgw,
or against the rgw-go whose URL `out/<release>/rgw-go.endpoint` holds. The set
is `test_s3.py` and `test_headers.py` minus the s3-tests markers `run.sh`
excludes, of features not implemented yet and of `docs/exclusions.md`, and
minus the tests `hack/s3tests/deselect-phase2.txt` and `deselect-phase3.txt`
name: tests that call a later phase's feature but carry none of those
markers. Both gateways run the same set, `fails_on_rgw` tests included, since
both must fail those alike.

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
the gate's data pool check would count. `run.sh` creates them again on its
next run.

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
```

Each run leaves `hack/s3tests/out/<release>-<gateway>-<run>.xml`, the junit
report `hack/parity` reads, with `.log` (pytest's output), `.conf` (the
s3-tests conf) and `.freeze` (the Python version and the packages the venv
held; s3-tests' `requirements.txt` pins none) beside it. The checkout and the
venv stay in `out/src` and `out/venv` and are rebuilt when the pin changes;
the pin is bumped by hand, and the baselines re-recorded with it. `run.sh`
exits 0 when pytest ran the whole set, whatever the tests' outcomes, and
nonzero when it could not.

Each line of a deselect list is a pytest node id and, after `#`, the calls
that need the feature. `run.sh` stops before any test runs when a line names
no test the markers leave, because it was renamed, removed or marked at the
pinned commit, or when a line would also remove a test the lists do not name,
since pytest's `--deselect` removes every node id that starts with its
argument. `run.sh --print-deselect` prints a digest of the listed ids, which a
recorded baseline carries. The PR that lands a later phase's feature deletes
its lines and re-records the radosgw baselines.

## Parity baselines

`test/s3tests/baseline/<release>.json` is radosgw's outcome for every test of
the set on that release's cluster, recorded by `hack/parity` from two runs in a
row; a test whose outcome differed between them is listed as `unstable`, and a
comparison skips it. rgw-go's run is compared with it, and must match every
other outcome, failures included. `hack/parity`'s package doc holds the file's
schema and the comparison's rules.

```sh
make gate RELEASE=squid
make s3tests RELEASE=squid GATEWAY=radosgw RUN=base1
make s3tests RELEASE=squid GATEWAY=radosgw RUN=base2
make parity-record SUITE=s3tests FORMAT=junit OUT=test/s3tests/baseline/squid.json \
	META="release=squid ceph_version=$(jq -r .ceph_version hack/rooket/out/squid/manifest.json) gateway=radosgw s3tests_commit=$(hack/s3tests/run.sh --print-commit) deselect=$(hack/s3tests/run.sh --print-deselect) pytest_freeze=$(sha256sum hack/s3tests/out/squid-radosgw-base2.freeze | cut -c1-12)" \
	FILES="hack/s3tests/out/squid-radosgw-base1.xml hack/s3tests/out/squid-radosgw-base2.xml"
```

A baseline carries the s3-tests commit, the deselect lists' digest, the
release and its Ceph version, and `make parity-check` refuses to compare
results that differ in any of them: bumping the s3-tests pin or the Ceph pin,
or changing a deselect list, re-records both releases' baselines in the same
change. `pytest_freeze` is the first twelve hex digits of the SHA-256 of the
`.freeze` file beside the last run, a record of the venv rather than a key
the comparison checks.

## go-ceph's rgw/admin suite

```sh
make gate RELEASE=squid                                       # first: the gate counts the zone's users
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
new tag and re-records both baselines.

`go test` runs the suite with `-tags ceph_preview`, which the account tests
need, and with `CEPH_CONF` naming `out/<release>/ceph.conf`, since
`TestGetInfo` connects through librados and compares the cluster's fsid with
the one the gateway reports; `run.sh` refuses to run without that conf.
go-ceph picks its expectations for a release from `CEPH_VERSION`, as its CI
sets it. At this tag the only ones that differ between `squid`, `tentacle`
and `main` are `TestAccount`'s skips of GET and DELETE on `/admin/account`,
which it takes for every release up to `tentacle`:

- Squid runs as `squid` and keeps the skips: v19.2.6's radosgw checks a cap
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
`admin` user stays, like the s3-tests users, so `make gate` runs before
`make admin-suite`, never after.

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

`test/admin/baseline/<release>.json` is radosgw's outcome for each test,
recorded from two runs in a row:

```sh
make admin-suite RELEASE=squid GATEWAY=radosgw RUN=base1
make admin-suite RELEASE=squid GATEWAY=radosgw RUN=base2
make parity-record SUITE=admin FORMAT=gotest OUT=test/admin/baseline/squid.json \
	META="release=squid ceph_version=$(jq -r .ceph_version hack/rooket/out/squid/manifest.json) gateway=radosgw go_ceph_tag=v0.39.0" \
	FILES="hack/admin/_out/squid-radosgw-base1.jsonl hack/admin/_out/squid-radosgw-base2.jsonl"
```

A test whose outcome differed between the runs is listed as `unstable`, and a
comparison skips it. `make parity-check` refuses to compare results whose
go-ceph tag, release or Ceph version differ, so bumping `GO_CEPH_TAG` or a
Ceph pin re-records both releases' baselines in the same change.

Each baseline holds the suite's 82 subtests. On both releases radosgw fails
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
