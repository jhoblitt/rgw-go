# Disposable clusters

Single-node Ceph clusters for integration tests and the phase 0 gate: a mon, a mgr,
one memstore OSD and a radosgw serving S3 on `127.0.0.1:7480`, all from
`quay.io/ceph/ceph` on the host network. Nothing here touches any other cluster.

```sh
make cluster-up-squid          # quay.io/ceph/ceph:v19.2.6
make populate RELEASE=squid
make gate RELEASE=squid        # the phase 0 gate, test/gate/
make cluster-down

make cluster-up-tentacle       # newest v20.2.* tag, resolved at startup
make populate RELEASE=tentacle
make gate RELEASE=tentacle     # the phase 0 gate, test/gate/
make cluster-down
```

One release runs at a time, since both use the same ports and container names;
`up.sh` refuses to start while any cluster it started is still present.

## Layout

| Container    | Role                                                              |
|--------------|-------------------------------------------------------------------|
| `rgw-go-mon` | mon `a` on `v2:127.0.0.1:3300`; writes `ceph.conf` and the keyrings |
| `rgw-go-mgr` | mgr `x`                                                           |
| `rgw-go-osd` | `osd.0`, memstore, so the data lives in memory                    |
| `rgw-go-rgw` | `radosgw` as `client.rgw.a`, `--rgw-frontends="beast endpoint=127.0.0.1:7480"` |

The ports stay clear of the rados-rs cluster (`docker/docker-compose.ceph.yml`
there), which uses 6789 and 6000-7000: this mon listens on 3300 and the daemons bind
7100-7199. The containers use the `rgw-go-` prefix for the same reason, so run the
Ceph tools as `podman exec rgw-go-mon ceph -s`.

`entrypoint.sh` is the whole daemon setup, one function per role. `up.sh` runs it
with `podman run` and is the only way to start a cluster. Every container and volume
carries the label `rgw-go.release=<release>`, which is what `down.sh` removes. If
`up.sh` fails after its preflight check, it removes the containers, volumes and
`out/<release>/` directory it created and exits non-zero.

`up.sh` also creates the `rgw-go-test` pool for the goceph integration suite.

## Output

`hack/cluster/out/<release>/` is ignored by git and removed by `make cluster-down`:

- `ceph.conf` and `ceph.client.admin.keyring`, for host processes. The copied
  `ceph.conf` names the copied keyring for `client.admin`, so
  `CEPH_CONF=hack/cluster/out/squid/ceph.conf` is all a host client needs.
  Host-side clients need librados >= 19.2.6 (Squid) or >= 20.2.4 (Tentacle), because
  those clusters create AES256KRB5 cephx keys by default.
- `image`, the image the cluster runs.
- `manifest.json`, written by `populate.sh`: the pools, the users `alice` and
  `t1$bob` with their S3 keys, the buckets `plain` and `t1/tenanted` with the id,
  marker and index shard count `radosgw-admin bucket stats` reports, and the objects
  in `plain`.

## Population

`populate.sh` writes everything through the radosgw: users with `radosgw-admin`
inside `rgw-go-rgw`, buckets and objects with the aws CLI against
`http://127.0.0.1:7480`. `multipart.bin` is 20 MiB and goes up with `aws s3 cp`,
which switches to multipart at 8 MiB. `_underscore.bin` exercises radosgw's escaping
of a leading underscore in RADOS object names. Object contents are pseudo-random
bytes seeded by the key, so a rerun writes the same data. The script is safe to
rerun against a populated cluster.

Checks after populating:

```sh
podman exec rgw-go-mon rados -p default.rgw.meta -N users.uid ls
podman exec rgw-go-mon rados -p default.rgw.buckets.index ls
```

`users.uid` holds `alice`, `alice.buckets`, `t1$bob` and `t1$bob.buckets`; the index
pool holds eleven `.dir.<bucket id>.<n>` shards per bucket.
