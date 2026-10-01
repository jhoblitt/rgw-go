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
make cluster-down RELEASE=squid

make cluster-up RELEASE=tentacle  # quay.io/ceph/ceph:v20.2.4 under Rook v1.20.7
make populate RELEASE=tentacle
make integration RELEASE=tentacle
make gate RELEASE=tentacle
make s3tests RELEASE=tentacle
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
- librados 19.2.6 or later with its headers: both releases' admin keys are
  AES256KRB5, which older librados cannot parse.

## Layout

| Path | Role |
|---|---|
| `squid/`, `tentacle/` | rooket configuration homes, recorded with the cluster: the `host-network` profile and the chart values |
| `up.sh` | `rooket up --wait`, then checks every Ceph pod runs the pinned image, creates the `rgw-go-test` pool the integration specs use, runs `rooket ceph-config`, and checks the host reaches the radosgw |
| `populate.sh` | writes the data set through the radosgw and records it in `manifest.json` |
| `down.sh` | `rooket down --delete-disks` and removes `out/<release>/`, even when rooket fails |
| `diag.sh` | collects pod logs, Rook status and Ceph's view for CI, with no admin keyring or S3 secret key, each call bounded by a timeout |
| `lib.sh` | the Rook release, cluster naming, toolbox and radosgw lookup helpers the scripts share |

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
seam microbenchmark in `test/bench/seam` over the `rgw-go-test` pool, one
process per completion mode, then measures its floor with `rados bench` from
the cluster's own image, run on the host through `ROOKET_ENGINE` (podman by
default), and renders `hack/bench/out/<release>/seam-<UTC time>/REPORT.md`
with `hack/bench/report`. It measures the host as much as the seam, so run it
with nothing else busy; `noise.log` in the run directory samples the load and
the busiest processes every 15 seconds. Each 4 MiB write waits for 5 GiB free
in the pool, writes about 3.9 GiB and is removed before the next, and the
sweep leaves the pool as it found it. `BENCH_QUICK=1` runs a short sweep, and
the script's header lists its other settings.

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
builder for the Ceph tag in `RGW_GO_CEPH_TAG`, taking `GOOS`, `GOARCH`,
`GOAMD64`, `CGO_ENABLED` and `GOFLAGS` from its environment as a goreleaser
build's `tool` must. The first build of a release's builder pulls from
quay.io and docker.io and installs `librados-devel` from download.ceph.com;
the builder is then reused until `Containerfile.builder` changes.
`RGW_GO_ENGINE` picks the container engine, podman when installed, else
docker; rootless Docker is not supported.

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
