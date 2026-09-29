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
make cluster-down RELEASE=squid

make cluster-up RELEASE=tentacle  # quay.io/ceph/ceph:v20.2.4 under Rook v1.20.7
make populate RELEASE=tentacle
make integration RELEASE=tentacle
make gate RELEASE=tentacle
make cluster-down RELEASE=tentacle
```

Each release is its own rooket cluster, `rgw-go-<release>`, so both can run at
once and neither collides with another rooket cluster on the host. Reach one
with `ROOKET_NAME=rgw-go-<release> rooket k ...`, never with the ambient
`kubectl`; `ROOKET=<path>` makes the targets run a rooket other than the one
on `PATH`.

## Prerequisites

- rooket at the commit `.github/workflows/integration.yml` pins, with its own
  prerequisites: a container engine, `kind`, `kubectl`, `helm`, and the iSCSI
  tooling. Every `cluster-up` creates the OSD's iSCSI target and every
  `cluster-down` removes it with the disk image, so both need root;
  `rooket sudoers install` removes the prompt.
- The AWS CLI v2, `jq` and `python3`, for `populate.sh`.
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
| `lib.sh` | the Rook release, cluster naming and toolbox helpers the scripts share |

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
