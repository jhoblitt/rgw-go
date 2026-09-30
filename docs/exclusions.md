# Excluded features

Status: draft, 2026-09-25, revised the same day after review by the rgw-rs
session and further decisions (D3N excluded; bucket notifications kept
with Kafka and AMQP delivery excluded, sized against Rook's integration
tests). Companion to the design spec, which is not yet written. Everything
under "Excluded" was decided in the rgw-go design session. Every claim
about C++ behavior below was checked against ceph/ceph main at
e234256339f and, where a release is named, against the v19.2.3 tag.

Since 2026-09-25 this document is also the canonical feature set for
rgw-rs, which tracks it instead of keeping its own list. A material change
here is announced to the rgw-rs session when it is made.

## The goal this list serves

rgw-go is a Go reimplementation of Ceph RGW. Its goal is to be a drop-in
replacement for C++ RGW in a Rook cluster running Squid or Tentacle:
bit-compatible with RGW's RADOS layout, able to coexist with radosgw on the
same zone during a rolling replacement or rollback, administered by the
unmodified radosgw-admin, and benchmarked against C++ RGW and rgw-rs.
Priorities, in order: performance, then scalability, then a compact and
maintainable code base. The Ceph version floor is Squid 19.2.6 or Tentacle
20.2.4 for every daemon, OSDs and any radosgw on the zone included; data
written by any earlier release is still decoded (design spec, section 3).
The feature level is Tentacle radosgw wherever a feature is gateway-side.
That principle has a precise form, decided 2026-09-25 and refined the same
day with the rgw-rs session, which splits by layer:

- Driver-level persistent types, the ones the gateway itself reads and
  writes as object data, xattrs or omap values, such as bucket info, user
  info, manifests and ACLs: rgw-go decodes every struct version the C++
  decoders accept, since never-rewritten metadata sits at its original
  encoding, and encodes at the version the cluster's own radosgw release
  writes, Squid on a Squid cluster and Tentacle on a Tentacle cluster,
  detected from the cluster's required OSD release and overridable for
  testing. Coexistence is byte-exact by construction rather than
  dependent on the compat tolerance of the decoders.
- Object-class requests: encoded at the same cluster-release version, so
  that request shapes added in Tentacle are reachable on Tentacle OSDs.
- Object-class replies: decoded from the Squid version up to the newest
  main writes. Stored index, OLH, GC, usage and lifecycle records are
  written and re-encoded by the OSD's class at that OSD's version, so a
  reply never carries an older stored encoding. The one exception, the
  `bi_*` entries that return raw stored bytes, is out of scope.

A Tentacle gateway-side feature is therefore available on a Squid cluster
when its persistent form is a separate attribute, as S3 checksums are,
and unavailable when it is a field of a struct that the cluster's radosgw
would re-encode without it, as bucket logging is.

The acceptance criterion for "drop-in" is explicit: rgw-go, installed in
the derived Ceph image, passes Rook's object integration suite,
`TestCephObjectSuite`, in both of its passes, with and without TLS,
against the current Rook main. That suite runs one shared store of Rook's
"zoned" shape, a single zone declared through CephObjectRealm,
CephObjectZoneGroup and CephObjectZone resources with shared pools mapped
as RADOS namespaces, plus one classic store for the dependents test. Its
packages are: bucket lifecycle, owner, policy, quota and read-write
through ObjectBucketClaims; COSI provisioning; store deletion blocked by
dependents; bucket notifications to an HTTP endpoint; Kafka topic
configuration; user capabilities, keys and op mask; and the zone-pool
canary. Excluded from the criterion: any multi-zone sync test, of which
the current suite has none, and the keystone suite, since Keystone is
excluded. The helm and upgrade suites test Rook rather than the gateway
and are not part of the gate.

## How an exclusion is decided

Three tests, applied in order. A feature is excluded when it passes the
first two and the third can be satisfied.

1. It is off the hot path and does not change what the benchmark measures.
2. It either has a supported external substitute, or it is an integration
   with an external system that brings its own client library and auth
   model.
3. Its absence is safe on a shared zone. Every exclusion below carries a
   "Still required" line: what rgw-go must do so that data radosgw wrote
   using the feature is never misread, served incorrectly, or corrupted.

Multisite, the drivers, Swift and Lua were excluded in the project brief.
The rest were proposed during design and approved.

## Excluded

### Multisite

What: zone-to-zone sync, the sync coroutine framework, sync modules
(elasticsearch metasearch, cloud sync, archive), realm and period push,
`/admin/log`, `/admin/realm` period POST, the admin Sync_Bucket op, and the
S3 bucket replication configuration.

Why: it needs two or more zones; rgw-rs's findings sized it at roughly
41k lines across the RGW root and the RADOS driver and identified it as
the one part of RGW whose async model does not map onto a straightforward
port. It has no effect on a single-zone data path.

Still required: read the realm, zonegroup, zone and period metadata,
because Rook creates all of them for every object store, and serve a
single zone that Rook declared through its realm, zonegroup and zone
resources exactly as one it declared in the store spec, since the
integration suite's shared store has that shape; honor
`rgw_run_sync_thread`, which Rook sets to true by default and to false
only when the store sets `disableMultisiteSyncTraffic` (rook config.go:256-258),
though a single zone has nothing to sync either way; serve the read-only realm
and period getters and `/admin/config?type=zone`, because the Ceph
dashboard calls them; write none of the sync logs. That last point is
safe because radosgw itself does not write them in a Rook cluster: the
data and bucket-index logs are gated on the zone's `log_data` flag, which
zone creation sets only when the zonegroup has more than one zone, and
the metadata log is gated on being the metadata master with bucket
metadata sync enabled. The replication subresource answers with the
not-found error radosgw returns when no configuration exists, and rejects
writes. Adding a second zone to a cluster served by rgw-go is unsupported.

### Every storage driver except RADOS

What: D4N, POSIX, and by extension dbstore, DAOS and Motr.

Why: the goal is bit-compatibility with RGW's RADOS layout, which is one
driver. A storage abstraction exists only so the op layer can be tested
without a cluster.

Still required: nothing; no data is affected.

### Swift API and Swift authentication

What: the Swift protocol, Swift auth, and the Swift entries in
`rgw_enable_apis`.

Why: a separate protocol, sized at 5.7k lines by rgw-rs, off the S3 path.
Rook's probes are HTTP requests to the S3 endpoint root whenever S3 is
enabled and do not depend on it.

Still required: accept `rgw_enable_apis` values containing swift and
swift_auth -- radosgw's own default enables them, and Rook writes the
option only when the store sets `protocols.enableAPIs` or disables S3
(rook spec.go:1162-1183) -- and ignore them with one log
line at startup. Subusers and their Swift keys remain in the user model
and the admin API because they are part of the user metadata, not
because any Swift request is served. One piece of Swift state outlives
the API: objects written through Swift with `X-Delete-At` carry a
delete-at xattr that only the Swift path sets, and radosgw's object
expirer worker deletes them on schedule from hint shards in the log pool.
rgw-go does not run that worker. It would need the timeindex object
class, which nothing else in scope uses, and the pass has an external
substitute in `radosgw-admin objects expire`. rgw-go treats the delete-at
xattr as opaque metadata: preserved on copy, never acted on.

### Lua scripting

What: request and background Lua scripts.

Why: excluded in the brief; a bounded addition later if ever wanted.

Still required: nothing.

### S3 Select

What: the `?select` object subresource and the SQL engine behind it.

Why: a SQL query engine maintained as its own component; off the data
path; large.

Still required: return the error radosgw returns when the feature is
disabled.

### S3 Vectors and object dedup

What: the S3 Vectors API and the dedup subsystem with `/admin/dedup`.

Why: both are newer than Squid, experimental, and S3 Vectors is Rust in
the Ceph tree.

Still required: nothing on Squid. On Tentacle, dedup-managed objects are
ordinary RADOS objects with shared tails; the refcount protocol already
required for copy handles them.

### Cloud transition and restore

What: lifecycle transitions to a cloud tier, RestoreObject, and
`/admin/restore`.

Why: needs a remote cloud endpoint. Storage-class transitions inside the
cluster are not excluded.

Still required: an object whose manifest says its data lives in a cloud
tier must be reported as such and never served as if it were local.

### Keystone and LDAP authentication

What: the Keystone and LDAP auth engines, and by extension the STS Lite
variants of GetSessionToken that rely on them.

Why: external identity systems with their own clients and token caches.

Still required: nothing; local users, accounts, roles and OIDC providers
are in scope.

### Key management: SSE-KMS and SSE-S3 with every backend

What: SSE-KMS and SSE-S3 with the Vault, Barbican, KMIP and testing
backends, and the `rgw_crypt_default_encryption_key` option.

Why: every backend is an external key server. In RGW, SSE-S3 is not a
local mode: the code requires the SSE-S3 backend to be Vault, so excluding
key servers necessarily excludes SSE-S3. The encryption that is on the hot
path, AES-256-CBC in RGW's chunked format, stays with SSE-C.

Still required, and these three hold the exclusion together:

1. SSE-C is implemented, byte-compatible with RGW's scheme, so that
   objects radosgw wrote with a customer key decrypt.
2. The three bucket-encryption configuration ops behave exactly as a
   radosgw with no backend configured: the configuration round-trips,
   and any use of `aws:kms` or `AES256` fails with radosgw's error.
3. Objects radosgw encrypted with a key server carry a crypt-mode xattr.
   rgw-go reads it and refuses with the error radosgw gives when its
   backend is unavailable. Ciphertext is never served as plaintext.

Costs accepted: the s3-tests encryption groups that use the testing
backend are skipped, and Rook's `security.kms` and `security.s3` object
store settings have no effect.

### Dynamic resharding worker

What: the background worker that scans bucket stats, decides a bucket
needs resharding, and executes the reshard.

Why: off the hot path; roughly 1.4k lines in Squid and 2k in main; fully
substitutable by `radosgw-admin bucket reshard` and
`radosgw-admin reshard process`, which Rook's toolbox already ships.

Still required, and this is not optional: the reshard protocol on the
data path. The OSD does not enforce it by itself on Squid. radosgw
prepends a guard call to bucket_prepare_op and bucket_complete_op,
including the index completion manager's retry of a failed completion,
and to the OLH ops link_olh, unlink_instance, trim_olh_log and clear_olh,
on every release. A Tentacle radosgw also guards the dir_suggest_changes
it sends while listing; a Squid radosgw sends that op unguarded. No
release guards bucket_set_tag_timeout, index init, or index check and
rebuild. On Squid the guard fails with the busy-resharding error whenever
the shard header's reshard status is anything other than not-resharding.
rgw-go prepends the guard exactly where radosgw of the same release does,
and on the busy-resharding error re-reads the bucket instance's index
layout and retries. From v20.2.0 the object class additionally enforces
the guard on its own index writes and can answer busy-resharding even
when the client sends no guard op; rgw-go still sends the guard and
decodes the added log-record status value and the header's log-entry
count. Squid's
class lacks the newer index methods that main's radosgw probes for
(`bucket_init_index2`, `bucket_refresh_instance`, `bi_put_entries`,
`reshard_log_trim`), so rgw-go uses the original index-init method and
never calls the other three on Squid. rgw-go logs a warning at startup
when dynamic resharding is enabled in config.

Cost accepted: buckets that grow past the per-shard threshold list more
slowly until an operator reshards them.

### librgw, NFS and SMB

What: the library form of RGW and the file gateways built on it.

Why: not the S3 daemon.

Still required: nothing.

### QAT and UADK hardware offload

What: compression and crypto offload plugins.

Why: hardware-specific; the software codecs and crypto are in scope.

Still required: nothing; on-disk formats do not depend on the accelerator.

### D3N local read cache

What: the per-gateway cache of RADOS reads on local storage.

Why: a cache between the gateway and RADOS changes what the benchmark
measures unless it is disabled on both sides, and upstream marks it
experimental. It carries no on-disk state.

Still required: nothing. Benchmarks run radosgw with
`rgw_d3n_l1_local_datacache_enabled = false`.

### Kafka and AMQP delivery

What: delivering notification events to Kafka and AMQP endpoints. The
rest of the notification subsystem is in scope; see "Explicitly not
excluded".

Why: external message brokers with their own client libraries and auth
models, the same test as key servers, and roughly 2.1k lines of C++
between them. Rook's own Kafka integration test stops at the RGW
boundary: it configures topics with a Kafka URI and basic-auth
attributes, reads them back, updates and deletes them, and never runs a
broker.

Still required: topics with Kafka and AMQP endpoint URIs are accepted,
validated and stored exactly as radosgw stores them, with their endpoint
attributes, so that CreateTopic, GetTopicAttributes, SetTopicAttributes
and DeleteTopic round-trip and radosgw-admin's topic commands read them.
Non-persistent Kafka and AMQP topics drop their events with a log line,
which is radosgw's behavior when delivery fails. Persistent Kafka and
AMQP topics are created and enqueued exactly as any other persistent
topic, so that radosgw's deliverer and radosgw-admin's topic removal treat
the queue as theirs. The queue object is named
`<account or tenant>:<topic>` in the zone's notification pool, created
exclusively and initialized through the 2pc queue class at radosgw's
fixed capacity of 128 MB, and registered as an omap key on the queue
registry object; removal deletes both. On releases whose topic record
carries a shard count, radosgw creates one queue object per shard, the
first named as above and the rest with a numeric suffix, each initialized
and registered, with the count taken at creation from
`rgw_bucket_persistent_notif_num_shards`, default 11 in main, and each
event routed to the shard radosgw's hash of bucket and key selects; a
topic record without the field has one shard. rgw-go follows the same
contract by reading the count from the same option through librados and
from the topic record. A topic named like the registry object is
rejected. rgw-go runs no deliverer for these queues, but it does run the
stale-reservation expiry for every persistent queue whether or not it
delivers to it, without taking the queue lock: radosgw runs expiry only
from the deliverer that owns the lock, asserting the lock rather than
taking it, so a foreign gateway that took the lock would block that
deliverer, and the expiry call is a single atomic class operation that is
safe to run unlocked.

Two hazards rgw-go cannot fix, only avoid triggering. A Squid radosgw,
19.2.6 included (Tentacle fixed it in 20.2.3), lists the queue registry
without ever advancing its paging marker, so a registry holding more than
1024 queue objects hangs that radosgw, which a newer release's sharding can
reach from roughly a hundred topics on a shared registry. And OSDs below
the floor, Squid before 19.2.4 and Tentacle before 20.2.3, added a ten-byte
overhead per entry to reserved capacity on every reserve but took it back
on no commit, abort or expiry, so a 128 MB queue reached permanent SlowDown
after roughly 12.8 million events. OSDs at the floor no longer add the
overhead, but drift accrued before an upgrade can survive it, and the
reserved size is not exact even at the floor; see "Queue initialization and
reservation accounting" below.

Costs accepted: Rook CephBucketTopic resources with Kafka or AMQP
endpoints reconcile, but rgw-go delivers nothing to them. On a zone
rgw-go serves alone, a persistent Kafka or AMQP topic's queue fills, and
once reservation fails radosgw's own behavior applies: writes to buckets
notifying that topic fail with SlowDown, which is also what radosgw does
when its broker is unreachable long enough. Entries sit in the queue until
an operator removes the topic or a radosgw returns; nothing corrupts.

### The radosgw-admin CLI

Not an excluded feature but a scope statement: rgw-go does not
reimplement radosgw-admin. Bit-compatibility means the C++ tool already
administers every piece of rgw-go's metadata. rgw-go ships one binary
with `serve` and `version`.

## Coexistence obligations independent of any exclusion

These are not tied to a single exclusion. They are what sharing a zone
with radosgw and radosgw-admin demands of every feature that is in scope,
and they belong here because a reviewer of the exclusions needs to see
them next to the "Still required" lines. Most were raised by the rgw-rs
review and verified against the tree.

- **Metadata cache invalidation, both directions.** radosgw and
  radosgw-admin invalidate every gateway's metadata cache by notifying on
  the `notify.N` objects in the control pool with a cache-notify record.
  rgw-go watches those objects and honors every notify, and sends its own
  on every metadata write. Without this, an administrator's bucket
  removal, user change or quota change is invisible to rgw-go for the
  cache lifetime, and rgw-go's writes are invisible to a coexisting
  radosgw. The only alternative is running with the cache disabled.
- **GC shard formats.** Each GC shard is in either the omap-era format or
  the queue-era format. radosgw reads the object version of each shard
  through the version class to decide which, tries the queue class behind
  a version check, and falls back to the omap set-entry method when the
  shard answers that it has not transitioned. rgw-go does the same per
  shard and does not assume a cluster has transitioned. There is no
  read-path obligation: radosgw's GC defer on read has been disabled since
  before Squid.
- **Queue initialization and reservation accounting.** The queue, GC queue
  and 2pc queue classes share one init, which reads the queue head and
  treats a missing or empty object, or a head that fails to decode, as
  uninitialized; so an init on a missing object creates it, an init on an
  initialized queue answers EEXIST, and an init on an object that is not a
  queue overwrites its start. radosgw's GC puts a non-exclusive create, a
  version check, the GC queue init and a version set in one op. Only the
  notification queue's creation uses an exclusive create, which adds EEXIST
  for an existing object that is not a queue. rgw-go issues the same ops.
  The 2pc queue's reserved size, kept in the head, is not guaranteed exact
  on any release. Before 19.2.4 and 20.2.3, below the floor, a reserve adds
  the size plus ten bytes per entry, while commit, abort and expiry take
  back only the size. From those releases they take back both, and a
  reserve recomputes the reserved size from the outstanding reservations,
  but only for a head decoded at version 2 or lower. Those releases
  re-encode the head at version 3 on every write, so a drifted head first
  rewritten by a commit, abort, expiry or entry removal keeps its drift for
  good. rgw-go never assumes the reserved size equals the sum of the
  outstanding reservations.
- **Class methods that both modify and return data.** Such methods, for
  example the user-stats reset in the user class and on Tentacle the OLH
  link and instance-unlink methods, return their data only when the
  operation carries the return-vector flag, which the librados C API
  exposes for read-op operate but not for write-op exec, whose signature
  has no output buffer. The OSD classifies a class call as a write from
  the method's own registration flags, not from the request, which is
  why a read op can carry such a call. This is a go-ceph fork item, and
  it gates persistent notifications rather than a corner case: the 2pc
  queue reserve returns the reservation id in a write op's reply, and
  radosgw runs it with the return-vector flag on every persistent event.
- **OLH epochs differ by OSD release.** With an epoch of zero in the
  request, the Squid class assigns the next epoch itself and returns
  nothing, while main uses the current time in nanoseconds and returns
  the committed epoch. A gateway at Tentacle feature level on a Squid
  OSD handles both. This lands with versioning in phase 2.
- **Compression: decode all four codecs on read.** radosgw may have
  written zlib, snappy, zstd or lz4 depending on the zone's placement
  configuration. rgw-go reads all four from the first release, whatever
  it chooses to write.
- **User stats and quota bookkeeping stay exact.** The user-class calls
  that set bucket stats and synchronize user stats keep radosgw's
  semantics, or `radosgw-admin user stats` and quota enforcement drift
  between the two implementations.
- **Lock names are radosgw's; cookies are not shared.** Two
  implementations exclude one another only if they take the same locks
  by name. On the S3 data path that is the exclusive lock named
  `RGWCompleteMultipart` on the multipart meta object, held for
  `rgw_mp_lock_max_time` and renewed on main, which is what stops two
  gateways completing the same upload at once. The meta object lives in
  the placement's data-extra pool (normally `<zone>.rgw.buckets.non-ec`;
  the data pool only when none is configured), so a lock taken on that
  name in the data pool excludes nothing. The workers take
  `gc_process`, `lc_process` and `reshard_process`, plus one lock per
  persistent notification queue named after the queue with a `_lock`
  suffix, each with radosgw's duration. `bucket_instance_lock` is named
  in the reshard code but never taken, and radosgw takes no lock on the
  queue registry object. Cookies do not need reproducing: the class
  identifies a holder by client identity plus cookie, so a cookie only
  has to be unique within one client, and radosgw's are random strings.
  Two consequences follow. A restarted gateway cannot renew or release
  its predecessor's lock even with the same cookie, so recovery is by
  expiry, as radosgw's is. And a lock on a missing object creates it, so
  wherever the object's absence matters radosgw prepends an existence
  assertion, and rgw-go does the same.
  The class's failure modes matter for renewal: a renew with
  `LOCK_FLAG_MUST_RENEW` by a client that does not hold the lock fails
  with `ENOENT`, not `EBUSY` (`MAY_RENEW` gets `EBUSY`), which radosgw's
  reshard renewal reads as expiry; and an expired ephemeral lock's object is
  removed only by a lock call that succeeds, because unlock, break,
  get_info and assert_locked calls that fail drop their transaction and
  leave it in place.
- **Signature quirks are radosgw's, not AWS's.** When a request carries
  no `x-amz-content-sha256` header, radosgw treats the payload as
  unsigned rather than rejecting the request as AWS does. This is a hard
  requirement, not a preference: Rook's own admin client, go-ceph's
  rgw/admin package, signs with the unsigned-payload hash and never sends
  the header, so it works against radosgw only because of that fallback.
  rgw-rs's spike chose AWS's behavior; rgw-go must not.
- **Some bucket sub-records are carried opaque in phase 0.**
  RGWBucketInfo's website configuration, object-lock configuration and
  sync policy are kept as the encoded bytes radosgw wrote and written back
  unchanged; the compat check of each is still applied. This keeps them
  byte-exact by construction, with one known exception until they are
  modelled: radosgw re-encodes an old v1 website configuration as v2 when
  it rewrites the bucket, while rgw-go writes the v1 bytes back. The phase
  that serves website, object lock or bucket sync policy models them.
- **Zonegroup and period JSON with sync policy groups is not rendered.** The
  zonegroup's and period's sync policy is also kept opaque, and rgw-go
  refuses to render their JSON when a sync policy holds groups. Rook creates
  no sync policies on a single-site store, and multisite is excluded, so a
  Rook-managed cluster is not affected; an operator who ran
  `radosgw-admin sync group create` would be.
- **radosgw does not key class request shapes on the cluster release;
  rgw-go does, and that is stricter.** Request-struct bumps keep their
  compat version at 1, so a newer radosgw sends the newest shape
  unconditionally and an older OSD ignores the tail, and radosgw probes
  for new methods and falls back on not-supported. During a rolling OSD
  upgrade a Tentacle radosgw therefore already sends new shapes while the
  OSD map's required release still says squid, whereas rgw-go, keyed on
  that map, keeps sending the older shape until the operator raises the
  release. Nothing stored differs, because the OSD writes the records;
  the cost is only that a Tentacle-only request feature waits for the
  release to be raised, which is the trade the encoding rule accepts.
- **`setuser` and `setgroup` numbers are read strictly.** radosgw reads
  either value with `atoi`, takes a non-zero result as the id, and looks
  anything else up as a name (`global_init.cc` at v19.2.6 and v20.2.4). So
  it takes any leading number: `12abc`, ` 12` and `+12` are id 12 and a
  user named `1password` is uid 1, while `0` is looked up as a name and
  radosgw fails to start unless a user or group is named `0`. On glibc
  x86_64 the id is the number modulo 2^32, as the v19.2.6 and v20.2.4
  binaries show: 2147483648 is id 2147483648, 4294967295 is `(uid_t)-1` or
  `(gid_t)-1`, which `setuid` and `setgid` refuse with EINVAL at the drop,
  4294967296 is 0 and so a name, and 4294967297 is id 1. rgw-go takes
  only a whole decimal string from 0 to 2147483647 as the id and looks up
  anything else, so `12abc`, `1password` and 2147483648 are names, and
  `0` is root's id, which asks for no change.
- **`-i` names the gateway and `--show_args` prints nothing.** radosgw
  starts as a client, and ceph's early-argument parser takes `-i` only
  for other daemon types (`ceph_argparse_early_args` in
  `ceph_argparse.cc` at v19.2.6 and v20.2.4), so `radosgw -i rgw.a` keeps
  the name `client.admin` and ceph's option parser skips both words.
  rgw-go takes `-i` as it takes `--id` and runs as `client.rgw.a`.
  radosgw prints its arguments on `--show_args` and goes on; rgw-go drops
  the flag. Rook passes `--id` and neither of these, so a Rook-managed
  gateway is unaffected.
- **An admin is served past a failed permission check only on an access
  denial.** When the permission check refuses a request from an admin or
  system user, rgw-go serves it anyway only if the refusal is an access
  denial (AccessDenied from `EACCES` or `EPERM`, or AuthorizationError),
  on both releases, as Tentacle's radosgw does. Squid's radosgw serves
  such a user past any error from that check, invalid parameters it
  parses there included, such as a listing's `max-keys`; on a Squid
  cluster rgw-go returns every error but an access denial instead.
  Tentacle's radosgw narrowed the override because the other errors may
  be invalid input.

### Request parsing and dispatch differences

rgw-go parses an S3 request's Host, path and query, and picks the op that
serves it, as radosgw's `RGWREST::preprocess`, `init_from_header` and
`op_*` methods do. Where the two differ, checked against the v19.2.6 and
v20.2.4 tags, rgw-go does the following.

- **`?notification` ignores `rgw_enable_apis`.** radosgw routes a bucket's
  `?notification` to its notification ops only when `rgw_enable_apis`
  names `notifications` or `pubsub` (`is_notification_op`,
  `rgw_rest_s3.h:710-715` at v19.2.6 and `:737-742` at v20.2.4, fed by
  `rgw_appmain.cc:304-305` and `:313-314`); the default names
  `notifications`. With neither named, the key selects nothing and the
  request falls through the method's later branches to `list_bucket`,
  `create_bucket` or `delete_bucket`, so `DELETE /b?notification`
  deletes the bucket. rgw-go answers `?notification` with the
  notification op whatever `rgw_enable_apis` names.
- **The CNAME fallback resolves no CNAME and knows no s3website
  hostnames.** When the Host matches none of its hostnames and
  `rgw_resolve_cname` is set (it defaults to false), radosgw looks up the
  Host's CNAME and matches that instead (`rgw_rest.cc:2086` at v19.2.6,
  `:2103` at v20.2.4). A Host that matches nothing, looks like no IP
  address and passes `validate_bucket_name` is the bucket when any
  hostname is configured, the s3website ones included:
  `rgw_dns_s3website_name` and the zonegroup's s3website hostnames
  (`:235-238`, and `:2136-2143` at v19.2.6, `:2153-2160` at v20.2.4).
  rgw-go matches the Host itself, never its CNAME, and takes it as the
  bucket only when `rgw_dns_name` or the zonegroup's hostnames name one,
  so with s3website hostnames alone the Host never names the bucket.
- **On Squid, `?attributes` is GetObjectAttributes.** The object
  `op_get` of Squid's radosgw has no attributes branch
  (`rgw_rest_s3.cc:4805-4821` at v19.2.6), so `GET /b/k?attributes` is
  GetObject there and returns the object's body; Tentacle's runs
  GetObjectAttributes (`:5370-5371` at v20.2.4). rgw-go routes the
  request to GetObjectAttributes on both releases, the Tentacle feature
  level. On Squid it still accepts the signed payload forms of the
  GetObject a Squid radosgw would run, so authentication refuses no
  request radosgw takes.

## Pending

None. D3N was excluded on 2026-09-25. Bucket notifications were first
excluded the same day, then sized against Rook's integration tests and
kept at the scope described under "Explicitly not excluded", with only
Kafka and AMQP delivery excluded. That is the shape the rgw-rs review
recommended.

## Benchmark parity with radosgw and rgw-rs

Facts from the rgw-rs session on 2026-09-25: rgw-rs is also targeting
bit-compatibility with radosgw's layout, through the rados-rs pure-Rust
client rather than librados, so the two reimplementations share the
layout and differ in transport. Since this document became canonical for
both, the surfaces match: rgw-rs has added the lock, 2pc queue and OTP
classes to its roadmap, so persistent notifications and MFA are in scope
on both sides. The bucket-index log and the reshard queue are out on both
sides through the multisite and dynamic-resharding entries. The `bi_*`
index-entry methods back resharding and the instance and OLH index
readers used by index repair, not the S3 versioning path, which uses the
dedicated OLH methods; they are out on both sides unless an index-repair
path in scope turns out to need them. Feature level: both decode class
replies up to what main writes. For driver-level types both gateways
follow the same rule, decode every version and encode at the cluster
release's version, adopted for rgw-rs on 2026-09-25, so on Tentacle as on
Squid they write identical bytes. Both detect the release from the OSD
map's required-OSD-release field, so they agree even during a rolling OSD
upgrade, when that field lags the newest daemon until the operator raises
it. For class requests rgw-rs sends Squid-version shapes today and adds
release-selected newer shapes in a later package. The newer shapes are
fewer than first thought, verified against the release tags on
2026-09-26: only the bucket update-stats request's version 2 is Tentacle,
first in v20.2.0; the OLH log read's version 2 and the version 8 index
entry metadata carried inside the complete and link-OLH requests are
Umbrella, first in the v21.1.0 release candidate; and the OLH epoch reply
on link and unlink exists only on unreleased main and feeds only the
multisite log. So on a Tentacle cluster the two gateways differ in one
request struct until rgw-rs adds it, and the stored records the OSD
writes are identical for both regardless. The benchmark plan records
transport as a variable and runs all three gateways with the same parity
settings: notifications off, MFA off, SSE off, D3N off, dynamic
resharding off, the same cache setting, the same shard count and stripe
and chunk sizes, and the sync, GC and LC workers off during timed runs.

Server-side copy, since it matters for the benchmark: rgw-go copies as
radosgw does. When the manifest can be shared, every tail object gains a
reference through the refcount class under the new object's tag and only
the head is read and rewritten by the gateway; when placement, storage
class, encryption or head geometry forbid sharing, the data is streamed
through the gateway. radosgw never uses RADOS's object-to-object copy
operation, which the librados C API does not expose. The rgw-rs session
confirmed on 2026-09-25 that rgw-rs shares tails through the refcount
class the same way, including the NUL-terminated reference tags radosgw
writes, so CopyObject parity holds across all three gateways.


## Explicitly not excluded

To prevent this list being read too broadly: IAM and STS with local
users, roles, groups and OIDC providers; bucket notifications in full
except Kafka and AMQP delivery, meaning the topic actions, the bucket
notification subresource with its filter grammar, event generation on
the data path, synchronous HTTP and HTTPS delivery, and persistent topics
over the 2pc queue class with an HTTP deliverer, sized at roughly four
thousand lines of Go and gated in phase 3 by Rook's notification and
Kafka integration tests; bucket logging; versioning, object lock,
lifecycle, static website, CORS; the four compression codecs; SSE-C; MFA;
accounts; tenants; quotas; the usage log; the GC and lifecycle workers;
the ops log to a file, which Rook's sidecar tails; admin-socket counters
in the format ceph-exporter scrapes; and every admin endpoint the Rook
operator and the Ceph dashboard call.

## Costs accepted, collected

- s3-tests groups skipped: SSE-KMS and SSE-S3 encryption, S3 Select,
  replication, and anything requiring Keystone or LDAP.
- Rook CephObjectStore fields with no effect: `security.kms`,
  `security.s3`, `auth.keystone`, `protocols.swift`, and `zone`. Rook
  CephBucketTopic resources with Kafka or AMQP endpoints reconcile but
  receive no events from rgw-go.
- The Ceph dashboard's bucket replication tab errors.
- Large-bucket listing degrades until an operator reshards.
- Objects given a Swift delete-at time before the switch are not expired
  by rgw-go; `radosgw-admin objects expire` does it on demand.
- A go-ceph fork item beyond the planned bindings: an exec step that
  returns data from a modifying class method.
