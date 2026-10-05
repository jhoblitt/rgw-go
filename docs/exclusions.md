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

radosgw also keeps an in-memory tombstone cache of deleted objects' mtimes,
zone short ids and placement group versions (`rgw_obj_tombstone_cache_size`)
for multi-zone data sync, which reads it through the state
`get_obj_state_impl` gives a missing object
(`driver/rados/rgw_rados.cc:6153-6165` at v19.2.6, `:6911` at v20.2.4), and
builds it only when another zone syncs from its zone (`driver/rados/rgw_rados.cc:1343-1346` at v19.2.6,
`:1332-1335` at v20.2.4). rgw-go
never builds it, which is what radosgw does on every single-zone
deployment.

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

When an interrupted reshard has left a bucket index flagged as resharding,
radosgw's write path takes the bucket's reshard lock and, finding no
reshard running, clears the flag itself. rgw-go does not take that lock:
its writes wait and retry, and answer 500 once their retries run out,
until an operator runs `radosgw-admin reshard cancel`. While it waits,
radosgw also reads the bucket again after each failed attempt on that lock
and polls the shard the bucket then names; rgw-go polls the shard that
refused the write until that shard is gone or no longer reads as
resharding. A Tentacle radosgw also runs that recovery from a guarded
index write while a reshard is in its log-record phase, once the bucket
layout's `judge_reshard_lock_time` is older than
`rgw_reshard_progress_judge_interval`, an interval it stretches at random
by up to `rgw_reshard_progress_judge_ratio`
(`check_reshard_logrecord_status`, `driver/rados/rgw_rados.cc:8631-8655`
at v20.2.4); rgw-go does not, so such a bucket's writes go on until its
reshard log reaches `rgw_reshardlog_threshold` and then wait as above.

An index write that a reshard keeps refusing is bounded in rgw-go: it sends
the bucket-index prepare or completion at most ten times while the shard
refuses it as resharding, and starts that count over only when the bucket,
read again after a wait, names another shard object. When one shard refuses
the operation ten times although its reshard status reads as finished after
each refusal, rgw-go answers 500 UnknownError. A Squid radosgw starts the
count over after each such wait and re-sends with no bound
(`docs/ceph-upstream-bugs.md`, "Squid's cls_rgw reshard guard and Squid's
radosgw reshard wait disagree, so guard_reshard spins without waiting").

After a reshard wait rgw-go reads the bucket instance again by its id, where
radosgw reads the bucket again by tenant and name. A bucket removed while the
write waits is 404 NoSuchKey from both. When a bucket of the same name is
created in that time, rgw-go answers 404 NoSuchKey, since the old instance is
gone, and radosgw writes into the new bucket's index
(`docs/ceph-upstream-bugs.md`, "radosgw re-reads a resharding bucket by name,
so a write can land in a bucket recreated under that name").

radosgw's quota sync threads also check each bucket whose stats they write
to its owner, and with `rgw_dynamic_resharding` set queue one past the
per-shard threshold for resharding (`check_bucket_shards`,
`rgw_quota.cc:596` at v19.2.6 and `:617` at v20.2.4, `rgw_user.cc:44` at
both; `driver/rados/rgw_rados.cc:10523-10582` at v19.2.6). rgw-go's sync
workers write the same stats and check nothing, so a bucket that only
rgw-go writes to is queued by a coexisting radosgw's checks or an
operator's `radosgw-admin bucket reshard`.

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
  rgw-go honors a notify by invalidating the named entry and re-reading
  it on the next access, whichever op the record carries, because
  radosgw applies an `UPDATE_OBJ` payload unchecked
  (`docs/ceph-upstream-bugs.md`, "radosgw caches any control-pool
  UPDATE_OBJ notify payload unchecked"). It sends `UPDATE_OBJ` with the
  written record after its own writes, so that a coexisting radosgw need
  not re-read. A control watch that cannot be registered, at startup or
  after it is lost, is retried after a pause that doubles from 1 s to
  30 s, for ever, with the cache disabled meanwhile. radosgw instead
  fails to start when a first registration fails (`init_watch`,
  `services/svc_notify.cc:243-261` at v19.2.6, `:207-221` at v20.2.4),
  and later re-registers a lost watch at once and can abort the process
  (`docs/ceph-upstream-bugs.md`, "radosgw aborts the process after 100
  failed control-watch re-registrations"). radosgw abandons a watch
  whose control object was deleted: the OSD answers ENOENT to any watch
  op on a missing object (`osd/PrimaryLogPG.cc:6967-6969` at v19.2.6,
  `:7036-7038` at v20.2.4), the unwatch that `RGWWatcher::reinit` sends
  first among them, and `reinit` takes that for a shutdown and returns
  without re-registering or rescheduling
  (`services/svc_notify.cc:95-101` at v19.2.6, `:93-99` at v20.2.4).
  That watch stays down, and radosgw's cache off, until radosgw
  restarts, even if the object is created again
  (`docs/ceph-upstream-bugs.md`, "radosgw abandons a control watch whose
  control object is deleted"); radosgw itself creates control objects
  only at startup (`init_watch`, `services/svc_notify.cc:231-238` at
  v19.2.6, `:198-205` at v20.2.4). rgw-go instead creates a missing
  control object again, with the `create(false)` it runs at startup,
  when a registration fails with ENOENT, and its watch comes back.
  rgw-go creates, watches and sends on the control objects
  whatever `rgw_cache_enabled` says. With it false radosgw sends no
  notify, because its only sender is the cache
  (`RGWSI_SysObj_Cache::distribute_cache`,
  `services/svc_sys_obj_cache.cc:452-463` at both releases), which it
  builds only when the option is true: its startup hands the option down
  as `have_cache` (`rgw_appmain.cc:255` at v19.2.6, `:264` at v20.2.4),
  and the cache is built only under it
  (`driver/rados/rgw_service.cc:88-90` at v19.2.6, `:79-81` at v20.2.4).
  Squid still creates and watches the control objects (`:75`,
  `:113-114`, `:139-145`), and Tentacle does neither (`:66-68`,
  `:98-100`, `:120-126`). rgw-go does not read
  `rgw_inject_notify_timeout_probability`, a `level: dev` fault-injection
  option (`rgw.yaml.in:3342` at v19.2.6, `:3527` at v20.2.4) with which
  radosgw drops that share of the notifies it receives, unacked
  (`RGWWatcher::handle_notify`, `services/svc_notify.cc:66-75` at
  v19.2.6, `:64-73` at v20.2.4); at its default of 0 both drop none.
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
  it chooses to write. It fails a read on every stored block radosgw's
  decompressor refuses, and also on damaged blocks that radosgw serves
  short. radosgw's zstd decompressor ignores `ZSTD_decompressStream`'s
  result, so a frame that fails, or that decodes to another length than
  the block's length header, yields whatever it decoded up to that
  length (`ZstdCompressor.h:69-102` at v19.2.6 and v20.2.4;
  `docs/ceph-upstream-bugs.md`, "radosgw's zstd decompress reports
  success on a frame that fails or decodes short"). Its zlib
  decompressor returns what a truncated deflate, zlib or gzip stream
  decoded, without error (`ZlibCompressor.cc:214-278` at v19.2.6,
  `:230-298` at v20.2.4; `docs/ceph-upstream-bugs.md`, "radosgw's zlib
  decompress reports success on a truncated stream"). rgw-go also fails
  a read whose blocks decode to fewer bytes than the block map gives the
  range, and a read on any block that decodes to more or fewer bytes than
  the block map gives it: the distance to the next block's `old_ofs`, and
  to `orig_size` for the last block, as `RGWPutObj_Compress::process` and
  `RGWPutObj::execute` record them for the bytes they took in
  (`rgw_compression.cc:44-87` at v19.2.6 and v20.2.4; `rgw_op.cc:4459` at
  v19.2.6, `:4691` at v20.2.4). It refuses such a block before writing any
  of it, and a block whose header claims more before allocating for it,
  where radosgw allocates whatever the block's header claims and passes on
  whatever the block decodes to. On an lz4 block too short for its own
  count and pair table, radosgw throws an exception nothing catches and the
  process ends (`docs/ceph-upstream-bugs.md`, "radosgw terminates on a
  stored lz4 block too short for its pair table"); rgw-go fails the read.
  It also refuses a block whose block map gives it more than 1 GiB,
  decoded or stored (`compression.MaxBlockLen`), before buffering or
  decoding it, where radosgw buffers and decodes it whole. radosgw makes a
  block from one read of at most `rgw_max_chunk_size` (`rgw_rest.cc:1071-1078`
  at v19.2.6, `:1076-1083` at v20.2.4), an option with no minimum or
  maximum (`rgw.yaml.in:84-98`, `:84-101`) and a default of 4 MiB, and
  writes in chunks of that size, which an OSD refuses past
  `osd_max_write_size`, 90 MiB by default (`PrimaryLogPG.cc:2139-2147` at
  v19.2.6, `:2186-2194` at v20.2.4); multisite sync makes blocks of 512
  KiB. Every refusal in this entry answers 500 UnknownError, except a block
  its codec refuses, as radosgw's decompress does, once the block has passed
  rgw-go's own length checks; that refusal keeps the decompress's return for
  the handler. The length checks come first: the block map's lengths and
  `compression.MaxBlockLen` before the codec runs, then the length a
  snappy, zstd or lz4 header claims before the codec decodes, and zlib's
  output while it inflates. A block that fails one of them answers 500
  UnknownError even where radosgw's decompress would refuse its body too
  and radosgw, before any of the body has gone out, answers 403
  AccessDenied or 404 NoSuchKey.
  radosgw's compressors write none of these blocks, so only a
  damaged object reads differently: where radosgw sends fewer or wrong
  bytes, rgw-go fails the GET. The other way round, rgw-go's inflater
  keeps a 32 KiB window whatever the stored window bits say, so it may
  accept a back-reference that radosgw's inflate refuses. radosgw writes
  none of those either, because it stores the window bits it deflates
  with; where zlib widens 8 bits to 9, the stream's header says 9, which
  both gateways refuse (`docs/ceph-upstream-bugs.md`,
  "compressor_zlib_winsize 8 writes zlib blocks radosgw cannot read
  back").
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
- **aws-chunked trailer sections are accepted up to 1 KiB, and a longer
  one is refused with 409.** radosgw reads an aws-chunked upload's trailer
  section, counted from the CRLF that ends the last data chunk through the
  closing CRLF, into a 256-byte buffer whose reads stop one byte short of
  it, so its own size check, which would answer 409 LimitExceeded, never
  fires and a longer section is cut off silently (`rgw_auth_s3.cc:1583-1614`
  at v19.2.6). On both floors a signed section longer than 257 bytes then
  fails with 403 SignatureDoesNotMatch, every signed SHA512 trailer
  (290 bytes) included, and on Tentacle an unsigned section whose checksum
  line falls past the cut is accepted without that checksum being compared
  (tracker #81122). rgw-go counts the section the same way, accepts up to
  1024 bytes, which holds a signed SHA512 trailer with room to spare, and
  answers 409 LimitExceeded above that. A signed trailer that radosgw
  refuses with 403 therefore succeeds through rgw-go.
- **aws-chunked chunk sizes are one to sixteen hex digits.** radosgw parses
  a chunk size with an unchecked `strtoull` (`rgw_auth_s3.cc:1127-1132` at
  v19.2.6), which skips blanks and accepts a sign, a `0x` prefix, trailing
  bytes and more than sixteen digits, turning `-1` or seventeen hex digits
  into 2^64-1. On both floors an unsigned multi-chunk upload whose first
  chunk carries such a size is then stored corrupted with 200, because the
  bad chunk swallows the next chunk's framing; a signed multi-chunk one
  fails with 400 XAmzContentSHA256Mismatch and a single-chunk one is stored
  intact (tracker #81123). rgw-go answers any other size with 400
  InvalidArgument and stores nothing.
- **aws-chunked framing is read strictly.** radosgw's PutObject and
  UploadPart take an aws-chunked payload's length from
  `x-amz-decoded-content-length` (`RGWPutObj_ObjStore::get_data`,
  `rgw_rest.cc:1068-1101` at v19.2.6, `:1073-1106` at v20.2.4), and rgw-go
  reads the same length. Like radosgw, rgw-go answers 400
  XAmzContentSHA256Mismatch when the chunk in progress at that length
  fails its signature, and ends a payload that expects no trailer
  signature at that length when more data, or the end of the body,
  follows the chunk there (`complete()`, `rgw_auth_s3.cc:1555-1694` at
  v19.2.6, `:1532-1671` at v20.2.4). rgw-go refuses such a payload that
  expects a trailer signature with 403 SignatureDoesNotMatch. They differ
  as follows.
  - Chunks that carry more data than the length: with a Content-MD5,
    radosgw answers 400 BadDigest on both floors (`rgw_op.cc:4483-4486` at
    v19.2.6, `:4715-4718` at v20.2.4), and on Tentacle it answers 400
    BadDigest for a trailing checksum it finds after the length
    (`rgw_op.cc:4757-4790` at v20.2.4). The Content-MD5 is rgw-go's op's to
    compare, and phase 1 compares no trailing checksum, so rgw-go accepts
    the payload where Tentacle answers BadDigest for a checksum.
  - Chunks that carry less data than the length: radosgw answers 403
    SignatureDoesNotMatch when the final chunk's signature is wrong and 400
    InvalidArgument otherwise, as rgw-go does, except when the payload's
    data length is a multiple of `rgw_max_chunk_size`, zero included, and
    fewer than 101 bytes follow the data. radosgw then answers 400
    XAmzContentSHA256Mismatch for a wrong signature and 400 RequestTimeout
    otherwise, where rgw-go answers 403 SignatureDoesNotMatch and 400
    InvalidArgument. A payload with an empty chunk followed by more data:
    radosgw accepts it, and rgw-go refuses it with those answers.
  - A stream that ends early, in a body whose Content-Length is met:
    radosgw answers 400 XAmzContentSHA256Mismatch when it ends inside a
    signed chunk and 400 InvalidArgument when it ends where another chunk
    header is due, and rgw-go's reader reports an unexpected EOF, which the
    op answers as a body cut short. Once the length is delivered, a body
    that ends inside the CRLF after the chunk there, right after its `\r`,
    or right after that CRLF: radosgw accepts the payload, or answers 403
    SignatureDoesNotMatch when a signed trailer is expected, and rgw-go
    reports an unexpected EOF. rgw-go also reports an unexpected EOF for a
    body that ends inside the final chunk line.
  - A stream without the CRLF that ends a chunk's data, or with other
    blanks in its place: radosgw accepts it, its size parse skipping the
    CRLF as leading whitespace (`docs/ceph-upstream-bugs.md`, "radosgw
    accepts a negative or overflowing aws-chunked chunk size"), and rgw-go
    refuses it with 400 InvalidArgument. rgw-go also refuses with 400
    InvalidArgument a malformed final chunk line and a signed chunk header
    in any form but `<size>;chunk-signature=<64 bytes>`, the key included.
  - A signed chunk header with a key other than `chunk-signature`: radosgw
    never checks the key's name, so with a 15-byte key it frames the chunk
    and verifies it, and with a key of another length it answers a signature
    error (403 SignatureDoesNotMatch, or 400 XAmzContentSHA256Mismatch for
    the chunk at the decoded length) (`create_next`,
    `rgw_auth_s3.cc:1140-1183` at v19.2.6, `:1117-1160` at v20.2.4;
    `docs/ceph-upstream-bugs.md`, "radosgw's signed aws-chunked header parse
    ignores the key and misframes one not 15 bytes long"). rgw-go refuses
    both with 400 InvalidArgument.
  - The final chunk once the decoded length is delivered: radosgw accepts a
    malformed final chunk line, or a body that ends inside it, and does not
    compare the final chunk's declared signature (`rgw_auth_s3.cc:1557-1575`
    and `:1624-1655` at v19.2.6, `:1534-1552` and `:1601-1632` at v20.2.4;
    `docs/ceph-upstream-bugs.md`, "radosgw never compares the final
    aws-chunked chunk's signature and parses its line loosely"). rgw-go
    refuses a malformed line with 400 InvalidArgument, reports an unexpected
    EOF for a body that ends inside it, and does not compare the signature
    either.
- **aws-chunked trailer sections are read line by line.** For a payload
  that expects a trailer signature, rgw-go reads the trailer section as
  lines that end in CRLF, in any order, each split at its first colon into
  a name and an untrimmed value. The declared signature is the value of
  the first line named `x-amz-trailer-signature`, and the signature rgw-go
  computes covers, for each name `x-amz-trailer` lists, the first line
  bearing exactly that name; a declared signature that is missing or
  differs is 403 SignatureDoesNotMatch. radosgw instead searches the up to
  255 bytes it reads after the chunk at the decoded length for
  `x-amz-trailer-signature:` and for each listed name anywhere, takes the
  text from the first match of each to the next CRLF as its line, and
  splits that line at every colon (`extract_helper`, `split_header`,
  `extract_trailing_headers` and `complete()`, `rgw_auth_s3.cc:1436-1510`
  and `:1583-1690` at v19.2.6, `:1413-1487` and `:1560-1667` at v20.2.4).
  A section as the AWS SDKs write it, one line per listed trailer and the
  signature line last, with `x-amz-trailer` naming each trailer once, gets
  the same answer from both when it fits radosgw's buffer. They differ as
  follows.
  - A signature whose first occurrence is not on a line of its own but in
    another line's value, at the end of a longer name, or in a following
    chunk's data when the chunks run past `x-amz-decoded-content-length`:
    radosgw takes that occurrence as the declared signature, even when a
    line of its own follows, and accepts the payload when it matches.
    rgw-go takes the declared signature only from a line of its own, and
    answers 403 SignatureDoesNotMatch to a payload whose chunks run past
    the length.
  - A listed name whose first occurrence is inside another line, in its
    value or as the start or the end of a longer name: radosgw's trailer
    signature covers the text from that occurrence to the line's CRLF,
    split at its colons, even when the name also has a line of its own
    further on. rgw-go's covers the first line that bears exactly the
    name, or no line for it.
  - The bytes radosgw drops before it looks for the signature: it drops
    from the start of the section the summed length of the lines it found,
    CRLFs included, counting a line once for each listed name that finds
    it, and looks for the signature only in what is left
    (`rgw_auth_s3.cc:1505-1509` at v19.2.6, `:1482-1486` at v20.2.4). When
    the drop reaches into the signature line, radosgw answers 403
    SignatureDoesNotMatch to a payload that expects a trailer signature.
    That happens when the signature line comes before a listed trailer's
    line with fewer bytes before it than the drop, and when `x-amz-trailer`
    lists a name the section holds twice, or lists overlapping names such
    as `x-amz-checksum-crc32c,crc32c`, even with the trailers first and the
    signature line last. When the drop exceeds the section, as it does once
    a name is listed often enough for the section's length, radosgw's
    behaviour is undefined, on any aws-chunked upload that carries
    `x-amz-trailer`
    (`docs/ceph-upstream-bugs.md`, "radosgw cuts an aws-chunked trailer
    section by its trailers' length, not their position"). rgw-go answers
    each such section as any other, and accepts the payload when the
    declared signature matches.
  - A line whose value holds a second colon, or is empty: radosgw takes
    only the text between the first two colons as the value, skipping
    empty fields, and leaves a trailer line with an empty value out of its
    trailer signature; rgw-go takes the whole value after the first colon,
    an empty one included, both for the declared signature and for the
    trailers its signature covers (`docs/ceph-upstream-bugs.md`, "radosgw
    reads an aws-chunked trailer line's value only up to a second colon,
    and drops an empty one").
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
- **ACL documents escape what radosgw writes raw.** radosgw writes the
  owner's and each grantee's ID and display name, and an email grantee's
  address, into the GetBucketAcl and GetObjectAcl documents without XML
  escaping (`rgw_acl_s3.cc:172-180` and `:246-274` at v19.2.6 and
  v20.2.4). A display name holding markup
  therefore yields either malformed XML, as `A&B` and `a<b` do, or
  well-formed XML that parses to another name or carries extra markup, as
  `&amp;` (read back as `&`) and `<b/>` (an empty `b` element) do. rgw-go
  escapes that text as radosgw's XML formatter escapes the text of its
  other documents (`xml_stream_escaper`, `src/common/escape.cc:134-169`).
  The stored ACL is the same either way; only the rendered document
  differs, and only for an ID, display name or email address holding `&`,
  `<`, `>`, `'`, `"` or a control byte.
- **A manifest walk that stops moving, or runs past its stripe bound,
  fails.** rgw-go walks a manifest's stripes from its start in three ways:
  `meta.Manifest.Stripes`, the whole layout, which it builds in memory;
  `meta.PartWalk`, the part walk behind `meta.Manifest.PartBounds`, the
  part lookup of GET and HEAD with `partNumber`, and behind
  GetObjectAttributes's ObjectParts listing; and the listing's sweep of a
  reconciled head's multipart parts from the index (`check_disk_state`,
  `driver/rados/rgw_rados.cc:10413-10429` at v19.2.6, `:11337-11353` at
  v20.2.4). Each fails with `denc.ErrMalformed` at the first step that does
  not move past the previous offset, and with `meta.ErrTooManyStripes` past
  its bound, `meta.MaxStripes` (2^21 stripes) for `Stripes` and
  `meta.MaxWalkStripes` (2^26) for the part walk and the sweep; the first
  two refuse at once a tail that needs more stripes than their bound. The
  operation that needs the walk fails instead of hanging; the sweep logs a
  warning and the listing goes on, with the parts after that step left in
  the index. radosgw's walks check neither
  (`docs/ceph-upstream-bugs.md`, "[radosgw hangs or faults walking a
  manifest whose rule has a stripe size of
  0](ceph-upstream-bugs.md#radosgw-hangs-or-faults-walking-a-manifest-whose-rule-has-a-stripe-size-of-0)").
  - The ObjectParts listing walks the parts once, where radosgw's
    `get_part_obj_state` looks each one up again from the start with
    `obj_find_part` (`driver/rados/rgw_rados.cc:7617-7618` at v20.2.4). The
    two agree on every manifest whose part ids rise, as every manifest
    radosgw writes does: `RadosMultipartUpload::complete` appends the
    upload's parts in part-number order, from the `std::map` that
    `list_parts` fills (`rgw_sal.h:1422` and
    `driver/rados/rgw_sal_rados.cc:3463`, `:3522` at v19.2.6;
    `rgw_sal.h:1534` and `:4311`, `:4370` at v20.2.4).
    Where a part id recurs after another part, which no radosgw write
    produces, radosgw sizes the repeated part by its first region and
    rgw-go by the region it lists.
  - `operator++` stops moving on a rule whose stripe size is 0, adding that
    size to the stripe offset without ever reaching the part's end, and on
    a manifest without rules that is not explicit
    (`rgw_obj_manifest.cc:34-36` and `:55-91` at v19.2.6 and v20.2.4).
  - radosgw's part lookup then loops forever in `obj_find_part` before part
    n (`driver/rados/rgw_obj_manifest.cc:210-217`). For a part whose first
    stripe is on such a rule, `get_part_obj_state` divides by its stripe
    size of 0, which on x86-64 raises SIGFPE and ends the process; AArch64's
    UDIV yields 0 instead, and the lookup would loop (not exercised).
  - For a part that starts in the head, the object itself holds its first
    stripe and carries the manifest, so radosgw's lookup returns at once
    with the whole object's manifest (`driver/rados/rgw_rados.cc:6810-6813`
    at v19.2.6, `:7646-7649` at v20.2.4). A HEAD with that `partNumber` then
    answers 200, as `RGWGetObj` returns after the read's `prepare` for a stat
    (`rgw_op.cc:2267-2269` at v19.2.6, `:2503-2506` at v20.2.4); a GET never
    ends in `iterate_obj`, whose step stays put on a stripe of size 0
    (`driver/rados/rgw_rados.cc:7497-7519` at v19.2.6, `:8352-8374` at
    v20.2.4); and at v20.2.4 ObjectParts never ends in `list_parts`' own walk
    (`driver/rados/rgw_sal_rados.cc:2880-2886`). rgw-go refuses all three, so
    for that HEAD it fails where radosgw answers.
  - A manifest without rules never reaches the part lookup, as its end part
    id is 0. radosgw's whole-manifest walk in `update_gc_chain`, which an
    overwrite or delete runs on the replaced object's manifest unless it
    keeps the tail (`driver/rados/rgw_rados.cc:5414` at v19.2.6, `:6137` at
    v20.2.4), loops forever on such a rule once the object passes its head,
    and without rules whenever the object is not empty. Each pass whose
    stripe is not the head appends that stripe's object to the chain
    (`cls/rgw/cls_rgw_types.h:1161-1172` at v19.2.6, `:1200-1209` at
    v20.2.4), so the stuck walk also grows memory without bound. `Stripes`
    instead lays a manifest without rules whose head holds the whole object
    out as the head alone.
  - The progress check also refuses a step that goes backward, which only
    an offset wrapping past 2^64 produces and which radosgw follows.
  - No manifest radosgw writes trips the progress check, so it costs
    nothing on data radosgw or rgw-go wrote. `generator::create_begin`
    refuses a manifest without a rule
    (`driver/rados/rgw_obj_manifest.cc:250-254`). A rule's stripe size of 0
    comes from an `rgw_obj_stripe_size` of 0, and then `create_next` divides
    by it at the first offset at or past the head's end (`:22-28`), so no
    such manifest with data past its head is stored
    (`docs/ceph-upstream-bugs.md`, "[radosgw divides by zero writing with an
    rgw_obj_stripe_size of
    0](ceph-upstream-bugs.md#radosgw-divides-by-zero-writing-with-an-rgw_obj_stripe_size-of-0)").
    A PUT smaller than the inline head stores one, whose walk ends in the
    head. Only a corrupted manifest, or one planted by a writer with access
    to the data pool, trips the check.
  - radosgw does not cap an object at S3's 5 TiB. CompleteMultipartUpload
    checks only the part count against `rgw_multipart_part_upload_limit`
    (`rgw_op.cc:6410-6414` at v19.2.6, `:7205-7209` at v20.2.4), and
    `rgw_max_put_size` caps each part (`RGWPutObj_ObjStore::verify_params`,
    `rgw_rest.cc:1049-1059` at v19.2.6, `:1054-1064` at v20.2.4). Their
    defaults, 10,000 and 5 GiB (`common/options/rgw.yaml.in:2367-2373` and
    `:127-137` at v19.2.6, `:2467-2473` and `:130-140` at v20.2.4, where the
    option text names the product), allow a multipart object of about
    48.8 TiB, 12,800,000 stripes of the default 4 MiB. An appendable object
    has no total cap: `rgw_max_put_size` bounds each append's request
    (`RGWPutObj_ObjStore::get_data`, `rgw_rest.cc:1096` at v19.2.6, `:1101`
    at v20.2.4), each non-empty append adds a part of at least one stripe
    (`AppendObjectProcessor::prepare`,
    `driver/rados/rgw_putobj_processor.cc:662-667` at v19.2.6, `:701-706` at
    v20.2.4), and no option caps their number at either tag.
    `rgw_obj_stripe_size` has no lower limit
    (`common/options/rgw.yaml.in:1860-1872` at v19.2.6, `:1948-1960` at
    v20.2.4), and radosgw at most rounds it to the data pool's required
    alignment (`get_max_aligned_size`, `driver/rados/rgw_rados.cc:695-708`
    at v19.2.6, `:730-743` at v20.2.4, called at
    `driver/rados/rgw_putobj_processor.cc:317` and `:453` at v19.2.6, `:345`
    and `:487` at v20.2.4).
  - `PartBounds`' bound, which keeps nothing per stripe and caps a walk at
    about 6 s, covers that largest multipart object five times over. It
    refuses data radosgw wrote past 2^26 stripes:
    - a multipart object of 10,000 parts of 5 GiB in stripes of at most
      800,105 bytes, the size at which its walk to the last part passes the
      bound, or in stripes under 800,000 bytes (about 781 KiB), which the
      up-front check refuses outright;
    - an appendable object past 2^26 stripes, which takes far fewer appends
      than that: about 52,429 of 5 GiB in the default 4 MiB stripes, some
      256 TiB.
  - `Stripes`' bound keeps the list it builds to a few hundred MB, and
    refuses data radosgw writes at its defaults: an object over about 8 TiB
    in the default 4 MiB stripes, or one appended to more than 2^21 times at
    any stripe size. A caller that walks whole objects that large iterates
    instead, under `PartBounds`' bound.
  - The driver's object read walks the stripes of the range it reads, from
    the one holding the range's first byte, as `iterate_obj` does
    (`driver/rados/rgw_rados.cc:7466-7536` at v19.2.6, `:8321-8391` at
    v20.2.4). It fails with 500 UnknownError at the first step that does not
    move past the previous offset, at a stripe that starts after the offset
    it needs, where the manifest ends before the range does, and past
    `meta.MaxWalkStripes` stripes; it refuses at once a range whose bytes
    past the head need more stripes than that in the largest stripes the
    rules allow. The bytes of the stripes before the failure are already
    written. radosgw's walk loops on the first, on the second reads the
    next stripe's object at an offset and length computed from a negative
    difference, and on the third ends the response short of its
    Content-Length without an error; it has no stripe bound.
- **A stripe shorter than its manifest says fails the read.** radosgw hands
  on the bytes such a read returned and then waits for an offset no later
  read starts at, so the GET ends without an error, its body short of its
  Content-Length (`get_obj_data::flush` and `drain`,
  `driver/rados/rgw_rados.cc:7345-7379` and `rgw_rados.h:1730-1741` at
  v19.2.6, `:8196-8230` and `rgw_rados.h:1810-1821` at v20.2.4). rgw-go
  hands on the same bytes, then fails the read with 500 InternalError. It
  fails a compressed object's read with 500 UnknownError before reading
  anything when the blocks the range needs reach past the object's stored
  bytes; radosgw reads what there is and ends the response short. Only a
  damaged object reads differently.
- **An explicit part manifest is not appended.** radosgw's
  `RGWObjManifest::append`, which completing a multipart upload runs for
  each part, converts both manifests to explicit ones when either is
  explicit and appends the part's pieces (`append_explicit`,
  `driver/rados/rgw_obj_manifest.cc:47-49` and `:165-182` at v19.2.6 and
  v20.2.4). rgw-go's `meta.Manifest.Append` refuses such a part, and a
  later part without rules, with `meta.ErrExplicitManifest`, leaving the
  object's manifest unchanged. radosgw refuses a part without rules before
  it appends, as InvalidPart (`obj_part.manifest.empty()`,
  `driver/rados/rgw_sal_rados.cc:3516-3520` at v19.2.6, `:4364-4368` at
  v20.2.4), and writes every part manifest with a part rule
  (`set_multipart_part_rule`, `driver/rados/rgw_putobj_processor.cc:455`
  at v19.2.6, `:489` at v20.2.4). Only a part whose manifest an older
  radosgw stored below manifest version 3, which decodes as explicit
  (`driver/rados/rgw_obj_manifest.h:303-318` at both tags), differs. No
  request reaches the refusal yet: the driver's multipart completion,
  which will append the parts, still answers NotImplemented.
- **Client checksums are ignored until phase 2.** rgw-go ignores the
  `x-amz-checksum-*` headers and aws-chunked trailers of PutObject,
  UploadPart and CompleteMultipartUpload, and `x-amz-checksum-algorithm` on
  CreateMultipartUpload, on both releases, and stores no checksum. A Squid
  radosgw ignores them too, so on Squid nothing differs. A Tentacle radosgw
  validates a supplied checksum, answering 400 BadDigest on a mismatch, and
  stores the object's checksum in `user.rgw.cksum` (`rgw_op.cc:4757-4789`
  for PutObject and UploadPart, `:7295-7341` for CompleteMultipartUpload,
  at v20.2.4); it also keeps the algorithm CreateMultipartUpload names in
  the upload's `multipart_upload_info` (`:7006-7010`), where rgw-go writes
  no checksum type. On a Tentacle cluster an object rgw-go wrote therefore
  carries no `user.rgw.cksum`: GetObjectAttributes and a checksum-mode GET
  through radosgw show no checksum for it, and rgw-go accepts a checksum
  that does not match the data. An upload a Tentacle radosgw created with a
  checksum algorithm cannot be completed through radosgw once any of its
  parts went through rgw-go: completion sums the parts' checksums
  (`try_sum_part_cksums`, called at `:7274-7281`, with the algorithm
  `get_info` read back, `driver/rados/rgw_sal_rados.cc:4573-4574`) and
  answers 400 InvalidRequest for a part that has none (`:7108-7114`). rgw-go
  also accepts what a Tentacle radosgw refuses: an
  `x-amz-checksum-algorithm` or `x-amz-sdk-checksum-algorithm` that names
  no algorithm it knows, which fails PutObject and UploadPart with EINVAL,
  400 InvalidArgument (`RGWPutObj_Cksum::Factory`,
  `rgw_cksum_pipe.cc:40-62`, caught at `rgw_op.cc:4602-4611`), and an
  `x-amz-checksum-type` that does not suit CreateMultipartUpload's
  algorithm, which fails it with 400 InvalidRequest
  (`rgw_rest_s3.cc:4508-4520`, `permitted_cksum_algo_and_type` in
  `rgw_cksum.h`). CreateMultipartUpload takes an algorithm Tentacle does
  not know as none, as rgw-go does. Phase 2 adds Tentacle-level checksums.
- **Members of IAM groups are refused until phase 3 evaluates group
  policies.** radosgw loads, with a user's own identity policies, the
  inline and managed policies of every IAM group the user's record lists
  (`load_account_and_policies`, `rgw_auth.cc:170-184` at v19.2.6 and
  v20.2.4) and evaluates them on every request. rgw-go phase 1 cannot read
  groups, which only the IAM API creates and rgw-go serves from phase 3, so
  it answers every request of a user whose record lists a group with 403
  AccessDenied rather than evaluate the request without the group's
  policies, which would allow what a group's Deny forbids. A missing key
  is AccessDenied to such a user, never NoSuchKey, since a group's policy
  could decide whether the user may list the bucket. An admin or system
  user still passes, as radosgw's own policy denials let one, except in
  DeleteObjects' per-key checks, which radosgw also makes without its
  override. Account users in IAM groups therefore work through radosgw and
  not through rgw-go until phase 3.
- **A copy that streams its data copies the stored bytes.** Where a copy
  cannot share the source's tails (another pool, placement or storage
  class, or a source held in its head), rgw-go reads the source's stored
  bytes and writes them unchanged, keeping a compressed source's
  compression attribute and its accounted size, as a Squid radosgw does
  (`copy_obj_data`, `driver/rados/rgw_rados.cc:5034-5115` at v19.2.6). A
  Tentacle radosgw decodes such a source instead and re-encodes it with
  the destination placement's compression type
  (`RGWCopyObjDPF::set_writer`, `rgw_op.cc:5770-5844` at v20.2.4), so the
  two gateways' copies can differ in codec and block layout; each reads
  back identically through either gateway. For the same reason a Tentacle
  radosgw compresses an uncompressed source that a copy streams into a
  placement with a compression type, which rgw-go, like Squid, stores
  uncompressed. rgw-go refuses an encrypted copy source with 501
  NotImplemented on both releases, as a Squid radosgw does
  (`driver/rados/rgw_rados.cc:4755-4763` at v19.2.6); a Tentacle radosgw
  decrypts it and re-encrypts as the request asks (`rgw_op.cc:5793-5805`
  and `:5846-5866` at v20.2.4), which needs SSE-C decryption, not yet
  implemented, or a key server, which is excluded.

### Command-line differences

rgw-go takes radosgw's command line, which is how Rook starts it, and
reads its own settings from `RGW_GO_*` variables. Where the two differ,
checked against the v19.2.6 and v20.2.4 tags, rgw-go does the following.

- **`--version` names rgw-go's build.** `-v` and `--version` print
  rgw-go's module version, revision, build time and Go version, such as
  `v1.0.0 (16c59f7d80f3) built 2026-10-01T15:27:32Z go1.27.1`. radosgw
  prints `ceph version 19.2.6 (<sha1>) squid (stable)`, and at v20.2.4
  `ceph version 20.2.4 (<sha1>) tentacle (stable - <build type>)`
  (`pretty_version_to_str`, `version.cc:46-54` at v19.2.6, `:46-59` at
  v20.2.4, printed from `ceph_argparse.cc:505-508` and `:509-512`). Rook
  reads the Ceph version from `ceph --version`
  (`pkg/operator/ceph/controller/version.go:71-73` at rook f09547c14),
  which the derived image keeps as Ceph's.
- **The usage adds rgw-go's settings.** `-h` and `--help` print radosgw's
  usage (`rgw_main.cc:42-56` at both tags, `ceph_argparse.cc:550-573` at
  v19.2.6, `:554-577` at v20.2.4), then the `RGW_GO_*` variables rgw-go
  reads.
- **Errors and logs go to stderr.** radosgw points its stderr at its
  stdout before anything else (`rgw_main.cc:69-77` at both tags), so its
  refusal of an empty command line, `<argv[0]>: -h or --help for usage`,
  and each log line it writes to stderr reach stdout. rgw-go writes its
  logs, and that refusal as `rgw-go: -h or --help for usage`, to stderr.
  Both exit 1 on an empty command line.
- **SIGHUP reopens nothing, and rgw-go never daemonizes.** radosgw reopens
  its log file and ops log file on SIGHUP (`rgw_signal.cc:40-45`,
  registered at `rgw_main.cc:127`, at both tags), as a log rotation's
  postrotate asks it to. rgw-go has no log file to reopen: it logs SIGHUP at
  debug level and keeps serving. Both stop on SIGINT and SIGTERM
  (`:134-135`) and keep serving on SIGUSR1, which radosgw hands to its
  SIGTERM handler (`:136`) only to skip the shutdown for it
  (`rgw_signal.cc:84-85`); rgw-go logs it at debug level. radosgw forks
  into the background unless it runs in the
  foreground (`:113-114`): the `daemonize` option is on for a daemon, and
  `-f` or `--foreground` turns it off, as does `-d`, which also sends its
  log to stderr (`config.cc:674-684` at v19.2.6, `:675-685` at v20.2.4).
  rgw-go always runs in the foreground and logs to stderr; it hands both
  flags to librados, which applies `-d`'s log settings to its own logging,
  and neither changes how rgw-go runs. Rook passes `--foreground`
  (`pkg/operator/ceph/object/spec.go:454` at rook f09547c14).

### Request parsing and dispatch differences

rgw-go parses an S3 request's Host, path and query, picks the op that
serves it, and reads its XML body, as radosgw's `RGWREST::preprocess`,
`init_from_header` and `op_*` methods and its `RGWXMLParser` do. Where the
two differ, checked against the v19.2.6 and v20.2.4 tags, rgw-go does the
following.

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
- **The admin, Swift and zero APIs answer 405.** When `rgw_enable_apis`
  names `admin`, `swift`, `swift_auth` or `zero`, radosgw serves that API
  at `rgw_admin_entry`, `rgw_swift_url_prefix`, `rgw_swift_auth_entry` or
  `zero` (`rgw_appmain.cc:307-366` at v19.2.6, `:316-375` at v20.2.4).
  rgw-go serves none of them. It keeps every path at or under each of
  those entries, and under each parent `register_resource` adds for a
  nested entry, from the S3 handler (`rgw_rest.cc:1935-1966` at v19.2.6,
  `:1952-1983` at v20.2.4), matching them as radosgw does against the path
  with a virtual-hosted bucket in front (`:2154-2160` and `:2182` at
  v19.2.6, `:2171-2177` and `:2204` at v20.2.4), so a virtual-hosted key
  such as `admin/x` stays S3's. It answers each request there as radosgw
  answers one no handler takes: 405 MethodNotAllowed, or 400
  InvalidRequest for a NUL in the path (`:2182-2186` and `:2290-2304` at
  v19.2.6, `:2204-2208` and `:2312-2326` at v20.2.4). With
  `rgw_swift_url_prefix` set to `/`, radosgw serves no S3 and Swift at
  every path; rgw-go answers every request 405, as it does when
  `rgw_enable_apis` names neither `s3` nor `s3website`.
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
  SigV2 signs that bucket in front of the path, as radosgw signs the URI
  `RGWREST::preprocess` rewrote (`rgw_auth_s3.cc:249-254` at v19.2.6,
  `:251-256` at v20.2.4), so a SigV2 request on a Host the two read
  differently is signed over another resource in rgw-go and fails there
  with 403 SignatureDoesNotMatch.
- **On Squid, `?attributes` is GetObjectAttributes.** The object
  `op_get` of Squid's radosgw has no attributes branch
  (`rgw_rest_s3.cc:4805-4821` at v19.2.6), so `GET /b/k?attributes` is
  GetObject there and returns the object's body; Tentacle's runs
  GetObjectAttributes (`:5370-5371` at v20.2.4). rgw-go routes the
  request to GetObjectAttributes on both releases, the Tentacle feature
  level. On Squid it still accepts the signed payload forms of the
  GetObject a Squid radosgw would run, so authentication refuses no
  request radosgw takes.
- **S3 XML request bodies are read with Go's `encoding/xml`, not expat.**
  radosgw reads them with expat, without namespace processing and without
  an unknown-encoding handler (`RGWXMLParser`, `rgw_xml.cc:158` and
  `:219-221` at v19.2.6 and v20.2.4). rgw-go reads them through one
  reader, `xmltext.Parse`, which keeps names as written, prefix included,
  as radosgw does, and adds the well-formedness checks expat makes and Go's
  decoder skips. It still refuses with 400 some bodies expat reads: one
  declaring ISO-8859-1 or US-ASCII, or written in UTF-16, the encodings
  expat knows besides UTF-8; one whose XML declaration names a version
  other than 1.0; one with a name holding more than one colon; and one that
  references an entity declared in its DTD, or an undeclared entity where
  the DTD has an external subset or a parameter-entity reference. rgw-go
  ignores a DTD's attribute declarations, which expat applies: both the
  defaults and the trimming and collapsing of whitespace in a value whose
  declared type is tokenized, such as NMTOKEN or ID. So an ACL grantee
  whose `xsi:type` comes only from such a default, or holds whitespace
  such a declaration would remove, is refused.
  rgw-go reads some bodies expat refuses: attributes not separated by
  whitespace; a malformed document type declaration, which Go's decoder
  passes through unparsed; and a character reference to a surrogate code
  point, which Go's decoder reads as U+FFFD.

### Frontend differences

rgw-go serves radosgw's beast frontend configuration on net/http. Where
the two differ, checked against the v19.2.6 and v20.2.4 tags, rgw-go does
the following; a difference only one release shows names it. The
`rgw_asio_frontend.cc` lines cited are those tags', and the net/http,
crypto/tls and net/url ones go1.27.1's.

- **TCP options.** TCP_NODELAY is on, Go's default, unless `tcp_nodelay`
  is set to anything but `1`; beast turns it off unless `tcp_nodelay=1`
  (`rgw_asio_frontend.cc:1165` at v19.2.6, `:1083` at v20.2.4). TCP
  keepalive is on too, Go's default of probes after 15 s idle, where
  beast sets none.
- **No `Connection: Keep-Alive` header.** beast writes `Connection:
  Keep-Alive` or `Connection: close` on every response
  ([`rgw_asio_client.cc:152-158`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L152-L158)
  at both tags); net/http writes the header only to close the connection
  or to keep an HTTP/1.0 one open, which HTTP/1.1 clients treat alike.
  A response whose connection then closes over a body the handler left
  unread carries no `Connection: close` either: under the full duplex
  rgw-go serves each request in, net/http decides to close only after the
  header is out (`server.go:1447-1473` and `:1552-1560`).
- **Keys accepted and ignored**, each logged once at startup:
  `max_connection_backlog`, since Go listens with the kernel's backlog;
  `so_reuseport`, which on Tentacle sets `SO_REUSEPORT` (`:651-693` at
  v20.2.4); `ssl_ciphersuites`, since TLS 1.3 cipher suites are Go's, so
  even a value naming no suite, which radosgw refuses, is accepted;
  `prefix`, which radosgw prepends to the request path when it picks the
  API to serve it (`rgw_rest.h:610-617` at v19.2.6, `:622-629` at
  v20.2.4); and, on Squid, `ssl_reload`, which re-reads the certificate
  on an interval (`:1096-1130` at v19.2.6), where rgw-go reads it once.
- **TLS versions and suites.** rgw-go's `MinVersion` makes TLS 1.2 the
  floor whatever `ssl_options` says. crypto/tls implements TLS 1.0 and 1.1
  (`crypto/tls/common.go:1219-1224`); radosgw leaves them out through its
  default `no_sslv2:no_sslv3:no_tlsv1:no_tlsv1_1`, which it applies only
  when `ssl_options` is absent. `no_tlsv1_2` raises the floor to 1.3.
  `no_sslv2`, `no_sslv3`, `no_tlsv1`, `no_tlsv1_1`, `no_compression` and
  `single_dh_use` change nothing, since crypto/tls has no SSL, the floor
  leaves out TLS 1.0 and 1.1, and it never compresses or reuses an ECDHE
  key. `default_workarounds`, OpenSSL's workarounds for broken peers, has
  no crypto/tls counterpart and is accepted without effect. Any other
  item, `no_tlsv1_3` included, is logged and ignored, as radosgw does
  (`:1019-1021` at v19.2.6, `:937-939` at v20.2.4). `ssl_ciphers` selects
  by name only, the OpenSSL or IANA name of one of Go's six ECDHE AEAD
  suites for TLS 1.2; any other item, an OpenSSL cipher expression such
  as `HIGH:!aNULL` among them, is logged and skipped, and a list that
  selects no suite is refused, where OpenSSL would evaluate the
  expression.
- **ssl keys without a TLS listener.** rgw-go accepts `ssl_private_key`,
  `ssl_options`, `ssl_ciphers` and `ssl_ciphersuites` without a TLS
  listener and logs them as unused. Squid never reads them then, since
  `ssl_init` returns first (`:941-944` at v19.2.6). Tentacle applies them
  to an SSL context no listener uses, so it refuses an `ssl_ciphers` or
  `ssl_ciphersuites` that selects nothing (`:943-972` at v20.2.4), where
  rgw-go accepts it.
- **Certificates come from files named in the entry.** A `config://`
  source, which radosgw reads from the monitors' config-key store, is
  refused naming its key, and the `$realm`, `$zone` and other variables
  radosgw expands in the paths (`ExpandMetaVar`, `:1063-1065` at v19.2.6,
  `:993-996` at v20.2.4) are not expanded. rgw-go also does not read
  `rgw_frontend_defaults`. Its default at both tags,
  `beast ssl_certificate=config://rgw/cert/$realm/$zone.crt ssl_private_key=config://rgw/cert/$realm/$zone.key`,
  adds each of the two keys to every beast entry that does not name it
  (`set_default_config`, `rgw_frontend.cc:50-59`, applied at
  `rgw_appmain.cc:462-465` at v19.2.6 and `:470-472` at v20.2.4), and
  radosgw loads the certificate and key only for a TLS listener
  (`:941-944` at v19.2.6, `:974-983` at v20.2.4). So on radosgw a TLS
  listener without `ssl_certificate` serves the config-key certificate
  when one is stored, where rgw-go refuses the listener, naming its key;
  and an entry naming only `ssl_certificate` gets
  `ssl_private_key=config://…`, which radosgw tries before it falls back
  to the certificate's file (`:1067-1074` at v19.2.6, `:998-1005` at
  v20.2.4), where rgw-go reads the key from the certificate's file at
  once. Rook names its certificate file in the entry, and its key file
  for some secret types (`pkg/operator/ceph/object/config.go:77-92` at
  rook f09547c14).
- **Timeouts per operation.** `request_timeout_ms` bounds each read of
  the request body and each write of the response, as beast's timer
  does, and an operation that times out ends the connection
  (`rgw_asio_frontend_timer.h:20-56` at both tags). For a connection's
  first request one deadline covers the wait and the header, as beast's
  timer does (`server.go:2038-2040`); on a kept-alive connection net/http
  times the wait for the next request and the read of its header
  separately, the header from its first byte (`:2163-2181`), where
  beast's one timer covers both. After the response, net/http reads what
  the handler left of the body only up to 256 KiB and otherwise closes
  the connection (`transfer.go:999-1019`), where beast discards all of it
  to keep the connection (`:362-389` at v19.2.6, `:372-399` at v20.2.4).
- **Header limit and malformed requests.** net/http enforces
  `max_header_size` with 4096 bytes of slack for its read buffer and
  answers a longer header with `431 Request Header Fields Too Large` and
  a text body; beast answers a header over the limit, as any request it
  cannot parse, with an empty `400 Bad Request` (`:275-289` at both
  tags), where net/http's 400 carries a text body. `max_header_size=0`
  sets beast's limit to 0, so radosgw answers every request with that
  empty 400 (`:643-657` at v19.2.6, `:595-609` at v20.2.4), where rgw-go
  takes 0 as net/http's 1 MiB default.
- **Host header refusals.** net/http answers `400 Bad Request`, with a
  text body and before any handler runs, an HTTP/1.1 request without a
  Host header (`server.go:1069-1073`), one with more than one
  (`request.go:1161-1163`), and one whose Host holds a byte its host
  validator refuses (`server.go:1074-1076`). beast answers 400 only to a
  request it cannot parse (`:275-289` at both tags) and files each header
  it reads, so a repeated Host's last value wins (`rgw_asio_client.cc:36-66`
  at v19.2.6 and v20.2.4); radosgw serves all three.
- **Malformed percent-escapes in the path.** net/http answers `400 Bad
  Request`, with a text body and before any handler runs, a request whose
  path holds a malformed percent-escape such as `%zz` or a lone `%`: the
  server parses the request-target with `url.ParseRequestURI`
  (`request.go:1142-1144`), which refuses such an escape in the path
  (`net/url/url.go:112-119`, through `setPath`, `:506` and `:660-664`).
  radosgw serves such a request for another path: its `url_decode`
  empties the path at a bad hex digit and cuts it at an escape the path
  ends in, so `GET /b/%zz` is served as `GET /`, a ListBuckets, and a
  request for the key `k%` acts on the key `k` (`docs/ceph-upstream-bugs.md`,
  "A malformed percent-escape in the path makes radosgw serve another
  path").
- **Shutdown drains.** On SIGTERM rgw-go stops accepting and lets the
  requests in flight finish for up to 30 s, Rook's default termination
  grace period, before it closes every connection and ends the requests
  still running. radosgw closes every connection at once
  (`AsioFrontend::stop`, `:1226-1245` at v19.2.6); Tentacle waits for the
  requests in flight first only when `rgw_graceful_stop` is set, and it
  defaults to false (`:1139-1171` at v20.2.4). rgw-go reads neither
  `rgw_graceful_stop` nor `rgw_exit_timeout_secs`.
- **Usage logged at shutdown.** With `rgw_enable_usage_log` set, rgw-go's
  usage-log worker makes its last flush as soon as shutdown begins, while
  the frontend is still draining. Until the shutdown order is fixed, so that
  the drain ends before the driver's workers stop, the usage of requests
  that finish during the drain is lost, unless they push the log past
  `rgw_usage_log_flush_threshold`. Tentacle's radosgw with
  `rgw_graceful_stop` set waits for the requests in flight before it
  finalizes its usage logger (`AsioFrontend::stop`,
  `rgw_asio_frontend.cc:1139-1171`; `rgw_appmain.cc:624` and `:628` at
  v20.2.4), so it logs them. Without it, and at v19.2.6
  (`rgw_appmain.cc:589` and `:593`), radosgw closes the connections first,
  and a request that reaches `log_usage` after the finalize finds no usage
  logger and is not logged (`rgw_log.cc:189-201`). rgw-go's last flush also
  gives up 10 s after shutdown begins, as does a tick flush still writing
  then, and their entries are lost; radosgw's `~UsageLogger` waits for its
  flush without bound (`rgw_log.cc:128-133`).
- **Ports and endpoints parse strictly.** rgw-go refuses port 0 and a
  port with anything but digits. radosgw reads a port with `strtoul` and
  refuses it only above 65535 or when `strtoul` reads no digits at all
  (`parse_port`, `:537-547` at v19.2.6, `:489-499` at v20.2.4), so
  `port=0` listens on an ephemeral port in each address family, and
  `port=80abc` and `port=+80` on port 80. For an IPv6 endpoint radosgw
  ignores even those errors (`docs/ceph-upstream-bugs.md`, "radosgw
  ignores a bad port in an IPv6 endpoint"), so `endpoint=[::1]:http`
  listens on an ephemeral port and `endpoint=[::1]:70000` on port 4464.
- **One beast frontend.** A second `beast` entry in `rgw_frontends` is
  refused; radosgw starts a frontend for each entry, with its own
  listeners and settings (`init_frontends2`, `rgw_appmain.cc:457-514` at
  v19.2.6, `:465-522` at v20.2.4).
- **`Server` header.** It names the release rgw-go detects for the
  cluster, `Ceph Object Gateway (squid)` or `(tentacle)`, where radosgw's
  names the release it was built as (`rgw_rest.cc:641` at v19.2.6, `:646`
  at v20.2.4); the two agree once every gateway runs the cluster's
  release. Both send `rgw_service_provider_name` instead when it is set
  (`:637-642` at v19.2.6, `:642-647` at v20.2.4), but rgw-go reads it once
  at startup, where radosgw reads it for every response, so a change made
  while the gateway runs takes effect at rgw-go's next start.

### Zone and placement differences

rgw-go resolves its zone and its placements from the root pool as radosgw's
startup does at the v19.2.6 and v20.2.4 tags, and only reads. It reads the
options its driver uses at startup as well. Where the result differs, rgw-go
does the following.

- **A storage class without a data pool.** A storage class that a zone
  placement lists without a data pool resolves to the STANDARD class's
  data pool in rgw-go's placement resolution (`op.ZoneInfo.Placement`) and
  in the driver's object pool resolution, so rgw-go stats and reads the
  objects that class places from STANDARD's pool. radosgw's
  `RGWZonePlacementInfo::get_data_pool` returns an empty pool for it
  (`rgw_zone_types.h:281-290` at v19.2.6 and v20.2.4). A stat or read
  through that pool opens a pool with an empty name, which librados refuses
  with EINVAL (`librados/RadosClient.cc:686-687` at v19.2.6, `:688-689` at
  v20.2.4), so by code reading radosgw answers 400 InvalidArgument. Where
  the STANDARD class has no data pool either, rgw-go answers 400
  InvalidArgument as well.
- **A bucket whose placement resolves to no data pool.** When the bucket has
  no explicit data pool, which both gateways take first
  (`RGWZoneParams::get_head_data_pool`, `driver/rados/rgw_zone.h:287-294` at
  v19.2.6, `:309-316` at v20.2.4), and neither the bucket's placement rule
  nor the zonegroup's default placement names a placement of the zone,
  rgw-go answers a stat of the bucket's objects with 500 UnknownError,
  naming the placement: the -EIO, "probably
  misconfiguration", that radosgw's head-object helpers answer for the same
  zone (`get_obj_head_ref`, `driver/rados/rgw_rados.cc:2508-2512` at
  v19.2.6, `:2616-2620` at v20.2.4). radosgw's object stat drops that
  failure and opens a pool with an empty name, so it answers 400
  InvalidArgument instead (`docs/ceph-upstream-bugs.md`, "radosgw's object
  stat ignores a data pool it cannot resolve"). The same holds for a
  multipart upload's meta object, whose data-extra pool both gateways
  resolve from the same two placements: rgw-go answers a read of it with
  500 UnknownError, where radosgw's `RadosMultipartUpload::get_info`
  reaches that stat and answers 400 (`driver/rados/rgw_sal_rados.cc:3672`
  at v19.2.6, `:4531` at v20.2.4). Both answer the meta object's write
  with 500 UnknownError.
- **No bootstrap.** rgw-go writes nothing at startup but the control
  objects and the gc shards' queue initialization, which radosgw writes
  too (`RGWGC::initialize`, `driver/rados/rgw_gc.cc:31-56` at v19.2.6 and
  v20.2.4). When no zone or zonegroup resolves, it refuses to start with
  `driver.ErrNoZone`, naming the object it looked for, and it refuses to
  start on a root pool, a control pool or a gc pool it cannot open. radosgw
  creates a missing gc pool and refuses to start only when it cannot open
  it after that (`open_gc_pool_ctx`, `driver/rados/rgw_rados.cc:1201-1203`
  and `:1441-1443` at v19.2.6, `:1251-1253` and `:1516-1518` at v20.2.4).
  Without its gc pool rgw-go would delete every overwritten tail inline,
  under readers still fetching it, so it does not start. A metadata pool found missing later fails the request that needs
  it, naming the pool. A data pool found missing is not created either:
  rgw-go stats an object in it as absent, and a read or write that needs
  the pool fails, naming it. radosgw creates what is missing instead, a
  data pool on the first stat, read or write that opens it. With the
  data-extra pool missing, rgw-go reads a multipart upload's meta object as
  absent, NoSuchUpload, which radosgw answers too once it has created the
  pool, but it fails CreateMultipartUpload with 404 NoSuchUpload, naming
  the pool, where radosgw creates the pool and then the upload.
  SiteConfig::load creates the default zone and zonegroup
  (read_or_create_default_zone and read_or_create_default_zonegroup,
  `driver/rados/rgw_zone.cc:1214`, `:1222` and `:1312` at v19.2.6, `:1208`,
  `:1216` and `:1306` at v20.2.4), Squid's zone service creates the default
  zonegroup (create_default_zg, `services/svc_zone.cc:214` at v19.2.6), and
  the config store creates a missing root pool when it reads from it
  (`rgw_init_ioctx` with create set, `driver/rados/config/impl.cc:53` at
  v19.2.6 and v20.2.4), as the notify service and the system-object layer
  create the control pool and any metadata pool they open, and the object
  stat, read and write paths any data pool they open
  (`rgw_get_rados_ref`, `driver/rados/rgw_tools.cc:107-108` at v19.2.6,
  `:108-109` at v20.2.4; the stat reaches it through `get_raw_obj_ref`,
  `driver/rados/rgw_rados.cc:2530-2543` at v19.2.6, `:2638-2650` at
  v20.2.4).
- **A zonegroup without a master zone.** A zonegroup with one zone and no
  master zone gets that zone as its master in rgw-go's memory, and rgw-go
  writes nothing back. radosgw makes the zone the master and writes the
  zonegroup back (`init_zg_from_period` and `init_zg_from_local`,
  `services/svc_zone.cc:513-533` and `:595-601` at v19.2.6, `:349-369` and
  `:399-405` at v20.2.4). Both refuse to start on a zonegroup whose master
  zone is none of its zones.
- **No search of other realms.** When the realm's current period does not
  hold the zone, Squid's radosgw searches every realm for it
  (`search_realm_with_zone`, `services/svc_zone.cc:83-126` and `:178-192`
  at v19.2.6). rgw-go takes the local zonegroup instead, as
  SiteConfig::load does on both releases (`driver/rados/rgw_zone.cc:1239-1250`
  at v19.2.6, `:1233-1244` at v20.2.4) and as Tentacle's radosgw, which has
  no such search, then serves. A realm whose period cannot be read, such as
  one with no current period, therefore stops Squid's radosgw from starting
  but not rgw-go (`docs/ceph-upstream-bugs.md`, "Squid's radosgw fails to
  start when its realm search meets a realm whose period cannot be read").
  radosgw-admin writes an initial period with every realm it creates
  (`driver/rados/rgw_zone.cc:513-522` at both tags), so that shape needs
  writes made outside it.
- **A default realm without a default zone.** When neither rgw_realm nor
  rgw_realm_id is set and the default realm has no default zone, rgw-go
  serves the zone named `default` and the realm that zone names, as
  SiteConfig::load does on both releases (`driver/rados/rgw_zone.cc:1206-1216`
  and `:1229-1237` at v19.2.6, `:1200-1210` and `:1223-1231` at v20.2.4) and
  as Tentacle's radosgw, which takes its zone from SiteConfig
  (`services/svc_zone.cc:98-103` at v20.2.4), then serves. Squid's zone
  service looks the zone up again through the default realm
  (`services/svc_zone.cc:150-155` at v19.2.6), does not find it, and stops
  radosgw with ENOENT unless the zonegroup is named `default` (`:237-248`).
- **rgw_realm_id, rgw_zonegroup_id and rgw_zone_id.** rgw-go honors them on
  both releases as Squid's zone service does: an id takes precedence over
  the name and the default object, and the name is then not read
  (`RGWSystemMetaObj::init`, `rgw_zone.cc:106-145` at v19.2.6). Tentacle's
  radosgw resolves the zonegroup and zone by name or default object alone
  (SiteConfig::load, which reads no id, `driver/rados/rgw_zone.cc:1184-1227`
  at v19.2.6, `:1178-1221` at v20.2.4) and uses rgw_realm_id only for the
  realm it reports (`services/svc_zone.cc:93` at v20.2.4). On either
  release, SiteConfig::load still reads a configured realm or zone name and
  refuses one that does not resolve, even when an id is set.
- **The default-object and latest-epoch names.** rgw-go reads the default
  realm, zonegroup and zone objects and a period's latest epoch under their
  default names, `default.realm`, `default.zonegroup.<realm id>`,
  `default.zone.<realm id>` and `periods.<period id>.latest_epoch`.
  radosgw takes those names from rgw_default_realm_info_oid,
  rgw_default_zonegroup_info_oid, rgw_default_zone_info_oid and
  rgw_period_latest_epoch_info_oid, whose defaults they are
  (`driver/rados/config/realm.cc:44-48`, `zonegroup.cc:37-42`,
  `zone.cc:36-40` and `period.cc:38-45` at v19.2.6 and v20.2.4).
- **Options read once.** The driver reads the `rgw_*` options it uses once,
  at startup, so a change made with `ceph config set` while rgw-go runs takes
  effect at its next start. None of those options carries Ceph's `startup`
  flag, and radosgw reads several where it uses them, so there the change
  takes effect at the next use: the usage-log shard counts each time it names
  a usage object (`usage_log_hash`, `driver/rados/rgw_rados.cc:1615-1628` at
  v19.2.6, `:1718-1731` at v20.2.4); `rgw_enable_usage_log` at each request
  (`RGWConf::init`, `rgw_env.cc:150` at both tags, which each request's
  `ClientIO::init_env` calls, `rgw_asio_client.cc:29`);
  `rgw_usage_log_flush_threshold` each time the usage log takes an entry and
  `rgw_usage_log_tick_interval` each time it re-arms its flush timer
  (`rgw_log.cc:151` and `:116` at both tags); and
  `rgw_override_bucket_index_max_shards` at each bucket creation
  (`init_default_bucket_layout`, `driver/rados/rgw_bucket.cc:2790-2796` at
  v19.2.6, `:2908-2910` at v20.2.4).
- **Shard counts and the bucket-index AIO limit that are not positive.** A
  zero or negative `rgw_usage_max_shards`, `rgw_usage_max_user_shards`,
  `rgw_lc_max_objs` or `rgw_gc_max_objs`, and a zero
  `rgw_bucket_index_max_aio` or `rgw_max_copy_obj_concurrent_io`, is used
  as 1,
  and rgw-go logs an error naming the option and the value it read. With a
  zero `rgw_max_copy_obj_concurrent_io` radosgw fails every copy that
  shares its source's tails with 500 UnknownError ("radosgw fails every
  tail-sharing copy on a zero rgw_max_copy_obj_concurrent_io"); a negative
  one limits nothing in either gateway. With
  `rgw_usage_max_shards` at 1, rgw-go names every usage object `usage.0`.
  Ceph's config refuses an `rgw_usage_max_user_shards` below its minimum of
  1 before either gateway reads it (`Option::validate`,
  `common/options.cc:99-109` at v19.2.6, `:100-110` at v20.2.4). On a zero
  AIO limit radosgw skips or never finishes every batch of bucket-index
  operations ("radosgw skips or never finishes every bucket-index batch on
  a zero rgw_bucket_index_max_aio"). radosgw accepts a zero or negative
  `rgw_usage_max_shards`, `rgw_lc_max_objs` or `rgw_gc_max_objs`:
  - A zero faults on the first request that shards by it
    (`docs/ceph-upstream-bugs.md`, "radosgw faults on a zero
    rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards").
  - A negative `rgw_usage_max_shards` does not fault. `usage_log_hash` holds
    it in an `int`, and taking the unsigned hash modulo it converts it to a
    huge unsigned divisor (`driver/rados/rgw_rados.cc:1625-1626` at v19.2.6,
    `:1728-1729` at v20.2.4), so radosgw names each usage object by the
    hash itself, `usage.<hash>`, where rgw-go writes `usage.0`.
  - A negative `rgw_lc_max_objs` makes `RGWLC::initialize` allocate an
    array of that many shard names (`rgw_lc.cc:237-241` at v19.2.6 and
    v20.2.4), which throws at radosgw's startup
    (`driver/rados/rgw_rados.cc:1326-1327` at v19.2.6, `:1310-1311` at
    v20.2.4). A negative `rgw_gc_max_objs` does the same in
    `RGWGC::initialize` (`driver/rados/rgw_gc.cc:35-37` at v19.2.6 and
    v20.2.4), which radosgw runs at startup
    (`driver/rados/rgw_rados.cc:1223-1225` at v19.2.6, `:1285-1287` at
    v20.2.4).
- **Read sizes that cannot read.** rgw-go refuses to start on a zero
  `rgw_max_chunk_size` or `rgw_get_obj_max_req_size`, or on an
  `rgw_get_obj_window_size` below `rgw_get_obj_max_req_size`, naming the
  three values. Ceph's config sets no minimum on any of them
  (`common/options/rgw.yaml.in:84-98` and `:1899-1918` at v19.2.6,
  `:84-101` and `:1999-2018` at v20.2.4), and radosgw starts with them.
  With a window below the request size, radosgw fails every RADOS read of
  a GET that is longer than the window with EDEADLK (`BlockingAioThrottle::get`
  and `YieldingAioThrottle::get`, `rgw_aio_throttle.cc:40-42` and
  `:131-132` at v19.2.6 and v20.2.4), so the GET fails; a GET that the
  prefetched head serves alone still succeeds. With a zero request size, a
  GET of a non-empty object never finishes (`docs/ceph-upstream-bugs.md`,
  "radosgw never finishes a GET on a zero rgw_get_obj_max_req_size").
- **Write sizes that cannot write.** rgw-go also refuses to start on a zero
  `rgw_obj_stripe_size`, or on an `rgw_put_obj_min_window_size` below
  `rgw_max_chunk_size`, naming the three values. Ceph's config sets no
  minimum on either (`common/options/rgw.yaml.in:99-113` and `:1860-1873`
  at v19.2.6, `:102-116` and `:1948-1961` at v20.2.4), and radosgw starts
  with them. A zero stripe size divides by zero at radosgw's first write
  that passes the head (`docs/ceph-upstream-bugs.md`, "radosgw divides by
  zero writing with an rgw_obj_stripe_size of 0"). The atomic, append and
  multipart writers take their throttle from the window
  (`driver/rados/rgw_sal_rados.cc:2265`, `:2284` and `:3730` at v19.2.6,
  `:2751`, `:2770` and `:4593` at v20.2.4), so with a window below the
  chunk size radosgw fails every write of a chunk longer than the window
  with EDEADLK (`rgw_aio_throttle.cc:40-42` and `:131-132` at v19.2.6 and
  v20.2.4), and every PUT whose body is longer than the window fails.
  When a pool's required alignment raises the aligned chunk above the
  window, rgw-go refuses every PUT and UploadPart to that pool with 500
  UnknownError, radosgw's answer to EDEADLK (`rgw_common.cc:346-353` at
  v19.2.6, `:359-366` at v20.2.4); radosgw refuses only a PUT that sends a
  tail piece longer than the window, and stores one that fits in its head,
  and only an UploadPart whose first stripe or a later piece is longer
  than the window.
- **Counts too large for radosgw's integers.** rgw-go holds a shard count in
  32 bits, using a larger one as 2^32-1, and the bucket-index AIO limit in
  63, using a larger one as 2^63-1. It caps `rgw_lc_max_objs` at 7877.
  radosgw caps it at 7877 too, but only after narrowing it to a 32-bit
  `int` (`rgw_lc.cc:237-239` at v19.2.6 and v20.2.4): 2^31 becomes INT_MIN
  and throws at startup, as a negative value does, and 2^32 becomes 0. The
  same holds for `rgw_gc_max_objs`, which rgw-go and radosgw cap at 65521
  (`rgw_shards_max`; `driver/rados/rgw_gc.cc:35` at v19.2.6 and v20.2.4).
  radosgw narrows a shard count above 2^32-1 into a 32-bit integer
  elsewhere too: the usage-log shard counts in `usage_log_hash`, as above,
  and a new bucket's index shard count (`rgw_bucket_layout.h:55` at v19.2.6
  and v20.2.4). At v19.2.6 it narrows an AIO limit above 2^32-1 the same
  way (`cls/rgw/cls_rgw_client.h:277`).
- **A cache expiry interval too large for radosgw's clock.** rgw-go caps
  `rgw_cache_expiry_interval` at 9223372036 s, about 292 years, the longest
  interval its clock holds, so with a larger value an entry of its metadata
  cache never expires in practice; 0 turns expiry off for both gateways.
  radosgw counts the interval in nanoseconds in an unsigned 64-bit integer,
  so it uses a value up to 18446744073 s, about 584 years, as given and
  wraps a larger one: 18446744074 s expires an entry 0.29 s after it is
  cached, and a multiple of 2^55 s turns expiry off
  (`docs/ceph-upstream-bugs.md`, "[radosgw wraps an rgw_cache_expiry_interval
  above 18446744073 seconds](ceph-upstream-bugs.md#radosgw-wraps-an-rgw_cache_expiry_interval-above-18446744073-seconds)").
- **A usage-log tick interval that is not positive.** With
  `rgw_enable_usage_log` set and `rgw_usage_log_tick_interval` 0 or
  negative, rgw-go flushes its usage log every second and logs an error
  naming the option and the value it read. Ceph's config sets no minimum on
  the option (`common/options/rgw.yaml.in:1651-1665` at v19.2.6,
  `:1734-1748` at v20.2.4). radosgw re-arms its flush timer that many
  seconds after each flush (`rgw_log.cc:109-117` at both tags), so it
  flushes without pause, spinning between requests
  (`docs/ceph-upstream-bugs.md`, "[radosgw spins or stops caching on a
  negative usage-log or quota interval](ceph-upstream-bugs.md#radosgw-spins-or-stops-caching-on-a-negative-usage-log-or-quota-interval)").
  Its usage entries reach the usage log at once; rgw-go's within a second.
- **Quota sync intervals that are not positive.** With
  `rgw_enable_quota_threads` set and `rgw_user_quota_bucket_sync_interval`
  or `rgw_user_quota_sync_interval` 0 or negative, rgw-go runs that sync
  every second and logs an error naming the option and the value it read.
  radosgw's owner-sync threads then loop without pause at both tags, and so
  does its bucket-sync thread at v19.2.6; at v20.2.4 the bucket-sync thread
  waits a second for 0 through -1024 and loops without pause below that
  (`rgw_quota.cc:378-381` and `:427-428` at v19.2.6, `:394-401` and
  `:448-449` at v20.2.4; `docs/ceph-upstream-bugs.md`, "[radosgw spins or
  stops caching on a negative usage-log or quota interval](ceph-upstream-bugs.md#radosgw-spins-or-stops-caching-on-a-negative-usage-log-or-quota-interval)").
  rgw-go does not read the developer option `rgw_reshard_debug_interval`,
  by which a Tentacle radosgw scales its bucket-sync interval. Each sync
  runs on a ticker, so a pass that takes longer than its interval is
  followed by the next at once, where radosgw waits the interval after
  every pass. A zero or negative `rgw_user_quota_sync_wait_time` gives
  every owner the pass does not skip as idle a full sync on every pass in
  both gateways.
- **A gc processor period that is not positive.** With
  `rgw_enable_gc_threads` set and `rgw_gc_processor_period` 0 or negative,
  rgw-go's gc worker waits a second after each pass that took less, and
  logs an error naming the option and the value it read. radosgw's
  `GCWorker::entry` waits the period less the pass's whole seconds and
  skips the wait when that is not positive (`driver/rados/rgw_gc.cc:795-805`
  at v19.2.6, `:807-817` at v20.2.4), so it runs its passes without pause,
  locking and listing every gc shard each time
  (`docs/ceph-upstream-bugs.md`, "[radosgw's gc worker runs its passes
  without pause on a non-positive rgw_gc_processor_period](ceph-upstream-bugs.md#radosgws-gc-worker-runs-its-passes-without-pause-on-a-non-positive-rgw_gc_processor_period)").
  A period of a second or more is waited as radosgw waits it, in whole
  seconds, and both gateways narrow it and `rgw_gc_processor_max_time` to
  32 bits as radosgw's `int` reads them.
- **A quota stats TTL below about -1.79e9 seconds.** A zero or negative
  `rgw_bucket_quota_ttl` makes both gateways read a bucket's or owner's
  stats from RADOS on every quota check, and start a background refresh
  too once an entry is cached. Below about -1.79e9 s, the current Unix
  time, radosgw's expiration wraps below zero and is pinned in 2106
  (`rgw_quota.cc:136-139`; `include/utime.h:528-535` at both tags), so it
  serves cached stats; rgw-go keeps reading them on every check. Down to
  about -3.58e9 s, twice the current Unix time, the refresh time
  (now plus half the TTL) stays positive and past, so each check
  starts a background refresh whose stats `set_stats` pins again
  (`rgw_quota.cc:149-156`): radosgw serves stats one refresh old. Below
  that the refresh time is pinned in 2106 too, and radosgw serves the
  first stats it cached for good.
- **The read window counts buffers and holds them until written.** rgw-go
  keeps a GET's reads within `rgw_get_obj_window_size`, but a piece's share
  of the window is the buffer it reads into, a pooled
  `rgw_get_obj_max_req_size` for a piece of more than half that, and it
  holds the share until its bytes are written to the client. radosgw counts
  the bytes a read asks for (`get_obj_iterate_cb`,
  `driver/rados/rgw_rados.cc:7436` at v19.2.6, `:8287` at v20.2.4), and its
  throttles give the share back when the read completes
  (`BlockingAioThrottle::put` and `YieldingAioThrottle::put`,
  `rgw_aio_throttle.cc:65-79` and `:155-170` at v19.2.6 and v20.2.4),
  keeping the completed pieces that wait behind a slower earlier one outside
  the window. So rgw-go has fewer reads in flight for pieces of just over
  half a request, and while a slow read or a slow client holds a GET back;
  in exchange the buffers a GET holds stay within the window.

### S3 handler differences

rgw-go runs an S3 request through radosgw's `process_request` steps and
renders its responses and error documents as radosgw's `end_header` and
`abort_early` do. Where the two differ, checked against the v19.2.6 and
v20.2.4 tags, rgw-go does the following.

- **Every document is XML.** radosgw's S3 handler chooses its formatter
  per request: the `format` query parameter when it is `xml`, `json` or
  `html`, and otherwise the `Accept` header's type when it is `text/xml`,
  `application/xml`, `application/json` or `text/html`, so `format=json` or
  `Accept: application/json` turns error documents and the listings into
  JSON (`allocate_formatter`, `rgw_rest.cc:1732-1764` at v19.2.6,
  `:1736-1768` at v20.2.4, which `RGWRESTMgr_S3::get_handler` calls with
  the format configurable, `rgw_rest_s3.cc:5262-5264` and `:5822-5824`).
  A NUL in the URL, and a request no API's handler takes, are refused
  before any formatter exists, so `abort_early` answers each with a JSON
  document, such as
  `{"Code":"InvalidRequest","Message":"","RequestId":"…","HostId":"…"}`
  or its 405 MethodNotAllowed, and `Content-Type: application/json`
  (`rgw_rest.cc:2182-2186`, `:2290-2304` and `:676-681` at v19.2.6,
  `:2204-2208`, `:2312-2326` and `:681-686` at v20.2.4). rgw-go renders
  every S3 document and error document as XML, those included.
- **No `Bucket` header.** With `rgw_expose_bucket` set, which defaults to
  false, radosgw names the bucket in a `Bucket` header on an error it
  answers through `abort_early`, one refused before the op executes
  (`dump_bucket_from_state`, `rgw_rest.cc:428-438`, called at `:715` at
  v19.2.6 and `:720` at v20.2.4). rgw-go never sends the header.
- **The request cap is fixed at startup.** rgw-go refuses a request past
  `rgw_max_concurrent_requests` as radosgw's default `throttler`
  scheduler does, with 503 SlowDown, and counts the refused request until
  it is answered, as that scheduler does. It applies the value it read at
  startup, where radosgw's scheduler follows a runtime change
  (`SimpleThrottler::handle_conf_change`,
  `rgw_dmclock_async_scheduler.h:176-183` at v19.2.6, `:175-182` at
  v20.2.4), and it has no counterpart to the experimental `dmclock`
  scheduler that `rgw_scheduler_type` selects.
- **Go's reason phrases.** net/http writes Go's reason phrase after every
  status code. radosgw writes the phrase its own `http_codes` table gives
  (`rgw_rest.cc:44-88` at v19.2.6 and v20.2.4), so a status line differs
  wherever the two phrases differ: a SlowDown or ServiceUnavailable error
  is `503 Service Unavailable` from rgw-go and `503 Slow Down` from radosgw
  (`:85`). net/http cannot send another phrase without taking over the
  connection, and clients act on the code.
- **Canonical header names.** net/http sends every header name rgw-go
  writes in its canonical case, such as `X-Amz-Request-Id` and `Etag`.
  radosgw sends each name as its source spells it, such as
  `x-amz-request-id` and `x-amz-request-charged` (`rgw_rest.cc:585` and
  `:600` at v19.2.6, `:590` and `:605` at v20.2.4) and `ETag` (`dump_etag`).
  Header names are case-insensitive (RFC 9110, section 5.1).

### Request authentication differences

rgw-go authenticates an S3 request from the headers and target radosgw's
`req_info` reads, which beast files as the client sent them. Go's
net/http server hands rgw-go some of them in another form. Where the two
differ, checked against the v19.2.6 and v20.2.4 tags and Go 1.27.1, rgw-go
does the following.

- **A byte above 0x7f in a percent-escape is a bad hex digit.** radosgw's
  `url_decode` looks such a byte up outside its hex table and decodes
  whatever it finds there, which differs by build: in the 19.2.6 and
  20.2.4 packages most such bytes decode as the digit 0 (`hex_to_num`,
  `rgw_common.cc:1690-1692` at v19.2.6, `:1753-1755` at v20.2.4; the
  entry on `url_decode`'s hex table in `docs/ceph-upstream-bugs.md`).
  rgw-go takes such a byte as no hex digit, so the decode is empty, as for
  any other bad digit. It reaches three places: the query, which net/http
  does not check; the `x-amz-tagging` header (`tags.ParseHeader`, radosgw's
  `set_from_string`); and a stored tag set in the legacy URL-encoded text
  form, which `tags.Decode` reads when the binary form does not decode, as
  `RGWObjTags::decode` does. net/http refuses such an escape in the path.
- **The Host of an absolute-form request is the target's authority.** For
  a request line such as `GET http://b.example.com/k HTTP/1.1`, net/http
  takes the target's authority as the request's host and drops the Host
  header (`net/http/request.go:1172-1175` and `server.go:1087`). beast
  files the Host header as `HTTP_HOST` (`rgw_asio_client.cc:36-66` at
  v19.2.6 and v20.2.4), which `req_info` reads (`rgw_common.cc:246` at
  v19.2.6, `:256` at v20.2.4). When the header names another host than
  the target, rgw-go reads a signed `host` header, and the host that
  names a virtual-hosted bucket, from the target's authority, where
  radosgw reads them from the header. HTTP/1.1 clients send the absolute
  form to proxies.
- **A chunked request's Transfer-Encoding reads as `chunked`, and its
  Trailer header is not seen.** net/http accepts a single Transfer-Encoding
  header whose value is `chunked` in any case, removes the header and
  records the encoding as `chunked` (`net/http/transfer.go:639-664` and
  `:598-600`); it ignores the header on an HTTP/1.0 request (`:647-649`).
  Under chunked encoding it also removes the Trailer header, keeping only
  the field names it declares (`:775-790`). beast files both headers with
  their values as sent (`rgw_asio_client.cc:36-66` at v19.2.6 and
  v20.2.4), and radosgw looks a signed header up among them
  (`rgw_auth_s3.cc:749-759` at v19.2.6, `:726-736` at v20.2.4). So in
  rgw-go a signed `transfer-encoding` header reads `chunked` whatever its
  case was, and is absent on HTTP/1.0, and a signed `trailer` header on a
  chunked request is absent; an absent signed header is left out of the
  canonical headers, as radosgw leaves out one the request lacks. radosgw
  signs the values the client sent. A request that signs
  `Transfer-Encoding: chunked` in lower case over HTTP/1.1, and signs no
  Trailer header, is authenticated alike.
- **An empty Host header on HTTP/1.0 is no Host header.** HTTP/1.1
  requires a Host header, so rgw-go takes an empty host on an HTTP/1.1
  request as a Host header sent empty, as beast files it: `HTTP_HOST` set
  to an empty value. HTTP/1.0 does not, and net/http removes the header
  either way (`net/http/server.go:1069-1087`), so on an HTTP/1.0 request
  rgw-go cannot tell an empty Host header from none and takes it as none.
  For radosgw a signed `host` on such a request is present and empty, and
  passes its check that `host` is signed (`rgw_auth_s3.cc:793` at v19.2.6,
  `:770` at v20.2.4); for rgw-go it is absent.
- **A secret key signs with its own bytes.** radosgw builds the SigV4
  signing key through `transform_secret_key`, which re-encodes each byte of
  the secret as a UTF-8 code point (`rgw_auth_s3.cc:984-1004` at v19.2.6,
  `:961-981` at v20.2.4). That leaves ASCII as it is, and radosgw generates
  ASCII secrets, but it accepts any non-empty secret an administrator
  supplies, and for a byte above 0x7f the conversion is undefined behaviour
  (`docs/ceph-upstream-bugs.md`, "radosgw's SigV4 signing key is undefined
  for a secret byte above 0x7f"). rgw-go keys the HMAC with the secret's
  bytes, as AWS clients do, so a request signed with such a secret verifies
  where radosgw's outcome is undefined.
- **A SigV2 Date header far in the future is skewed.** radosgw keeps the
  request time of a header-signed SigV2 request in `utime_t`'s 32-bit
  seconds (`rgw_auth_s3.cc:241-242` at v19.2.6, `:243-244` at v20.2.4), so
  a Date a multiple of 2^32 seconds, about 136 years, ahead of its clock
  passes its 15-minute skew check (`docs/ceph-upstream-bugs.md`, "radosgw's
  SigV2 date check wraps the request time to 32 bits"). rgw-go compares the
  full time and answers such a request with 403 RequestTimeTooSkewed.
- **A signed OPTIONS request is verified over OPTIONS and answered 501
  until CORS lands.** radosgw serves an OPTIONS request on a bucket or an
  object as its CORS preflight op. It builds the canonical request over
  the request's `Access-Control-Request-Method` instead of `OPTIONS`, and
  answers 400 InvalidArgument, before it looks the key up, when that header
  is missing or names no CORS method: for SigV4 on both floors, for SigV2
  on Tentacle only (`get_v4_canonical_method`, `rgw_auth_s3.cc:705-732`,
  called at `rgw_rest_s3.cc:5827` at v19.2.6; `get_canonical_method`,
  `rgw_auth_s3.cc:1749-1776`, called at `:258` and at `rgw_rest_s3.cc:6394`
  at v20.2.4). For a key the index finds, once the user's account loads and
  the key is in the user's record, it then skips the signature compare,
  SigV2 and SigV4 alike, and runs the preflight (`rgw_rest_s3.cc:6325-6360`
  at v19.2.6, `:6896-6931` at v20.2.4; `docs/ceph-upstream-bugs.md`,
  "radosgw grants a signed CORS preflight without comparing its
  signature"). rgw-go serves no CORS preflight in phase 1: it verifies a
  signed OPTIONS request as any other, over the method `OPTIONS`, and
  answers one that verifies with 501 NotImplemented. Where radosgw runs the
  preflight whatever the signature, rgw-go answers 403
  SignatureDoesNotMatch unless the request was signed over `OPTIONS`, a
  request signed as radosgw expects included. Where radosgw answers 400
  InvalidArgument for a missing or unknown method, rgw-go goes on to the
  key: an unknown one is 403 InvalidAccessKeyId, and a known one is
  answered as its signature decides. Otherwise an unknown key is 403
  InvalidAccessKeyId from both. rgw-go does not give that 400 on its own:
  it is the first step of radosgw's preflight signing, which phase 2's
  CORS work takes whole, and alone it would refuse requests as radosgw
  does while verifying the rest over another method. An unsigned OPTIONS
  request is anonymous in both, which rgw-go answers with 501
  NotImplemented.
- **A system request's rgwx-uid names its owner, not its user record.**
  Only radosgw's own multisite connection sends a system request with
  `rgwx-uid` or `rgwx-perm-check-uid` (`RGWRESTConn`,
  `rgw_rest_conn.h:234-237` and `rgw_rest_conn.cc:330-331` at v20.2.4),
  and rgw-go answers one on both floors as Squid does. With a non-empty
  `rgwx-uid` naming a user, Tentacle makes that user the request's user
  record (`SysReqApplier::load_acct_info` returns it,
  `rgw_auth_filters.h:324-344`, and `Strategy::apply` stores it,
  `rgw_auth.cc:534`), so everything that reads the request's user record
  reads the named user, such as the suspended check (`rgw_process.cc:374`),
  the op mask (`rgw_op.cc:1223`), the MODIFY op-mask check (`:1655`) and
  the placement chosen for a new bucket (`:3753`, `:8319`). Squid keeps the
  system user's record (`rgw_auth.cc:519`, `rgw_auth_filters.h:283-317` at
  v19.2.6), as rgw-go does on both floors, with the owner and the tenant the
  named user's on every floor. Tentacle also adds impersonation of the user
  a system request names in `rgwx-perm-check-uid`
  (`rgw_rest_s3.cc:6950-6978` at v20.2.4), but reads that parameter before
  system parameters are visible: `args.get` sees only ordinary parameters
  (`:6953`; `rgw_common.cc:991-1000`), every `rgwx-` name is filed as a
  system parameter (`:931-937`), and system parameters become visible only
  once authentication has granted (`set_system`, `rgw_common.h:462-467`,
  called at `rgw_auth_filters.h:355` from `rgw_auth.cc:540`). The
  impersonation therefore never applies, and both releases and rgw-go serve
  such a request as the system user (`docs/ceph-upstream-bugs.md`,
  "Tentacle's radosgw never applies rgwx-perm-check-uid, so a user-mode sync
  pipe's source read goes unchecked"). Multisite is excluded, so a
  single-zone rgw-go receives neither parameter from radosgw.

### Authorization differences

rgw-go evaluates bucket and identity policies as radosgw's `rgw::IAM` code
does. Where the two differ, checked against the v19.2.6 and v20.2.4 tags,
rgw-go does the following.

- **ARN patterns match component by component.** rgw-go splits an ARN
  pattern, such as an `ArnEquals`, `ArnLike`, `ArnNotEquals` or `ArnNotLike`
  condition's, and the ARN it is matched against at every colon, and matches
  each component on its own with fnmatch's rules (`policy.MatchPolicy`).
  radosgw's `match_policy` hands `substr` the next colon's position where it
  expects a length, so it compares every component after the first together
  with the text after it (`rgw_common.cc:2178-2179` at v19.2.6,
  `:2241-2242` at v20.2.4, which `arn_like` calls,
  `rgw_iam_policy.cc:845-852` and `:864-871`). A pattern such as
  `arn:aws:sns:*:123456789012:topic` therefore matches
  `arn:aws:sns:us-east-1:123456789012:topic` in rgw-go and not in radosgw.
  A statement whose `ArnLike` or `ArnEquals` condition names such a pattern
  takes effect in rgw-go where radosgw's is skipped, whether it allows or
  denies; under `ArnNotLike` or `ArnNotEquals` the reverse holds, so rgw-go
  can skip a Deny that radosgw applies (`rgw_iam_policy.cc:1001` at
  v19.2.6, `:1005` at v20.2.4). Action names have one colon and match
  alike. `docs/ceph-upstream-bugs.md` records the defect.
- **A runtime condition whose last value is shorter than three bytes reads
  no values.** radosgw takes a runtime condition's key from its last value
  by erasing that value's first two bytes and its last, and for a value
  shorter than three bytes the second erase throws `std::out_of_range`,
  which ends the radosgw process (`rgw_iam_policy.cc:871-879` at v19.2.6,
  `:891-899` at v20.2.4; `docs/ceph-upstream-bugs.md`, "radosgw reads a
  runtime condition's key from its last value, and terminates when that
  value is short"). rgw-go reads the empty key instead, which no request
  sets, so the condition's string and ARN operators compare the key's
  values with none: the positive and `ForAllValues` operators do not hold,
  and a negated one holds on Tentacle and not on Squid, each release's rule
  for no values. A request that stops radosgw gets an answer from rgw-go.
- **Out-of-range epoch seconds in a date condition convert as on x86-64.**
  `as_date` casts a count's whole seconds and its fraction to `uint64_t`
  and sums them in signed int64 nanoseconds. The seconds cast (a count of
  -1 or less, NaN, or 2^64 and more), the fraction cast (any negative
  count, NaN, or 2^64 and more) and the signed multiply and sum (a count
  from about 9.2e9 seconds up to 2^64, and on x86-64 NaN and a count of -1
  or less) are all undefined in C++; a count between -1 and 0 reaches only
  the fraction cast (`rgw_iam_policy.h:390-394` at v19.2.6, `:409-413` at
  v20.2.4; `docs/ceph-upstream-bugs.md`, "radosgw's date conditions wrap
  past 2554 and before 1970"). rgw-go computes in uint64 what radosgw's
  x86-64 build computes, on whatever architecture rgw-go runs; only the
  casts could make a radosgw built for another architecture answer
  otherwise.
- **A policy whose Statement is a string is refused on Tentacle too.**
  v20.2.4's policy parser asserts that a statement exists before it reads
  any string but a Version or Id, and a string under the top-level
  Statement key, as in `{"Statement": "x"}` or `{"Statement": ["x"]}`,
  comes before any statement, so radosgw aborts (`rgw_iam_policy.cc:608-609`
  at v20.2.4; `docs/ceph-upstream-bugs.md`, "Tentacle's radosgw aborts on a
  policy whose Statement is a string"). rgw-go refuses the document on both
  releases as v19.2.6 does, "At character offset N, `x` is not valid in the
  context of `Statement`." (`policy.Parse`), so a bucket, IAM or session
  policy carrying it fails as any policy that does not parse fails.
  Otherwise the parser accepts what radosgw's accepts, its defects
  included, such as a statement that names both Resource and NotResource,
  whose NotResource evaluation then ignores (`docs/ceph-upstream-bugs.md`,
  "radosgw ignores NotResource in a statement that also names Resource").
- **A stored IAM policy attr with bytes past its encoding fails the
  request.** radosgw decodes a user's or group's inline and managed policy
  attrs with the full-bufferlist decode, which aborts the process unless the
  decode consumed every byte (`include/encoding.h:632-637` at v19.2.6,
  `:633-638` at v20.2.4; `rgw_auth.cc:91` and `:102` at both;
  `docs/ceph-upstream-bugs.md`, "radosgw aborts on a stored IAM policy attr
  with bytes past its encoding"). rgw-go's `policy.DecodeUserPolicies` and
  `policy.DecodeManagedPolicies` fail on such an attr, so the request is
  refused as for an attr that does not decode, which radosgw's
  authentication answers with -EPERM (`rgw_auth.cc:548-554` at v19.2.6,
  `:563-569` at v20.2.4). radosgw writes these attrs cleanly; only
  corruption or another writer leaves such bytes.
- **A PUT's encryption condition keys leave out the bucket's default
  encryption.** radosgw's PutObject, UploadPart and UploadPartCopy read
  their parameters before authorizing, and `get_encryption_defaults` fills
  in the bucket's stored default algorithm and KMS key id when the request
  names none (`rgw_rest_s3.cc:146-264` and `:2612` at v19.2.6, `:151-269`
  and `:2773` at v20.2.4), so `s3:x-amz-server-side-encryption` and
  `s3:x-amz-server-side-encryption-aws-kms-key-id` carry the bucket's
  default in the policy environment. rgw-go adds those keys from the
  request's headers and query string alone. A bucket default names SSE-S3
  or SSE-KMS, which rgw-go refuses ("Key management: SSE-KMS and SSE-S3
  with every backend"), so such a write fails in rgw-go either way; a
  policy condition on these keys can make it fail with 403 AccessDenied
  where radosgw's refusal, if it has no key server, comes from encryption.
- **`aws:SourceIp` from a variable that is not a request header is
  absent.** `rgw_remote_addr_param` may name any variable of radosgw's
  request environment, and radosgw sets `aws:SourceIp` to its value
  (`rgw_op.cc:869-886` at v19.2.6, `:905-922` at v20.2.4). rgw-go reads
  `REMOTE_ADDR`, `CONTENT_TYPE`, `CONTENT_LENGTH` and `HTTP_` followed by a
  header's name, from the headers Go's net/http leaves in a server request
  (Go 1.27): it takes out Host and Transfer-Encoding always, and Trailer
  and Content-Length from a chunked request (`net/http/server.go:1087`,
  `net/http/transfer.go:644`, `:727`, `:790`). So for `HTTP_HOST`,
  `HTTP_TRANSFER_ENCODING`, and on a chunked request `HTTP_TRAILER` and
  `CONTENT_LENGTH`, and for the request line's and the listener's variables
  (`REQUEST_METHOD`, `REQUEST_URI`, `SCRIPT_URI`, `QUERY_STRING`,
  `HTTP_VERSION`, `SERVER_PORT`, `SERVER_PORT_SECURE`), rgw-go adds no
  `aws:SourceIp`, so neither an `IpAddress` nor a `NotIpAddress` condition
  holds unless it is IfExists. None of them carries a client address.
- **A typed condition on a repeated key reads the value upstream's builds
  read.** Which of a key's values a Numeric, Date, Bool, BinaryEquals,
  IpAddress or NotIpAddress condition reads depends on the libstdc++ that
  built radosgw, and on Tentacle on how many pairs the environment held as
  each value was added (`policy.Env`). rgw-go follows upstream's el9
  builds by release, GCC 11 for v19.2.6 and GCC 13.3 for v20.2.4, and
  adds the environment's keys in radosgw's order, its tag keys at each
  op's own point. A radosgw built with another libstdc++, such as a Squid
  built with GCC 12 or later, which reads as Tentacle does, can read
  another value. Only tag keys repeat, from a tag set or an
  `x-amz-tagging` header that names a key twice.
- **A public-access block that does not decode refuses the request.**
  radosgw takes a bucket's `user.rgw.public-access` attr that does not
  decode for no block at all (`get_public_access_conf_from_attr`,
  `rgw_op.cc:340-355` at v19.2.6, `:370-385` at v20.2.4;
  `docs/ceph-upstream-bugs.md`, "radosgw ignores a public-access block that
  does not decode"), so IgnorePublicAcls, BlockPublicPolicy and
  RestrictPublicBuckets stop applying and the bucket's public grants and
  public policy take effect. rgw-go answers a request on such a bucket with
  403 AccessDenied instead, as radosgw answers one whose stored bucket
  policy does not parse (`rgw_op.cc:598-615` at v19.2.6, `:628-645` at
  v20.2.4). radosgw writes the attr cleanly; only corruption or another
  writer leaves one that does not decode.
- **A bucket policy that does not parse refuses the request rather than
  stopping radosgw.** radosgw parses a copy source's stored policy with no
  handler, and its process terminates when the policy does not parse. It
  does the same for an object request by a user whom its handler for the
  request's own bucket lets through, a system user and on v20.2.4 an admin
  user (`docs/ceph-upstream-bugs.md`, "radosgw terminates on a copy source
  or system request whose bucket policy does not parse"). rgw-go refuses a
  copy whose source bucket's policy does not parse with 403 AccessDenied,
  for every requester and ahead of the op mask. For the request's own
  bucket it refuses such a policy with 403 unless the requester is an
  admin, whose request, an object request included, is evaluated without
  the policy, as radosgw evaluates an admin's bucket request. DeleteObjects
  checks each key as it checks a copy source, so an admin's DeleteObjects
  on such a bucket is refused for every key, where radosgw evaluates the
  keys without the policy for a system user, and on v20.2.4 for an admin
  user too; v19.2.6 refuses a non-system admin's whole request in
  `init_permissions` (`rgw_op.cc:612` at v19.2.6).
- **A stored identity policy that does not decode is refused at the
  permission check, not at authentication.** radosgw loads a user's
  `user.rgw.user-policy` and `user.rgw.managed-policy` while it
  authenticates the request, and a policy that does not decode or parse
  fails the authentication with 403 AccessDenied (`rgw_auth.cc:548-554` at
  v19.2.6, `:563-569` at v20.2.4). rgw-go decodes them when it authorizes
  and refuses there with the same 403, ahead of the op mask and never
  overridden for an admin. What rgw-go answers before it authorizes is
  answered first: a request naming a missing bucket answers 404
  NoSuchBucket, and an admin API request, which rgw-go authorizes by the
  user's caps alone, is served.
- **An op's own early errors can come before radosgw's early refusals.**
  radosgw refuses a request on a suspended bucket, on a bucket whose stored
  policy does not parse or whose ACL does not decode, and a missing
  object's request from a requester who may not list its bucket, while it
  reads the bucket and the object, before an op reads its parameters
  (`rgw_process.cc:173-211` at v19.2.6 and v20.2.4). rgw-go makes those
  checks when the op authorizes, after its `Init` and after what it parses
  first, as radosgw's `verify_permission` parses some parameters first. An
  error the op finds before it authorizes is answered first: a ListObjects
  with `max-keys=x` on a suspended bucket answers 400 InvalidArgument where
  radosgw answers 403 UserSuspended. Both refuse the request; only which
  refusal differs, and the op's error depends on the request alone.
- **Squid clusters get Tentacle's checks outside the policy language.** On
  both releases rgw-go refuses a request whose `x-amz-expected-bucket-owner`
  header names another owner than the request's bucket's
  (`rgw_common.cc:1407-1412` and `:1575-1580` at v20.2.4), and a requester
  outside the bucket owner's account when the bucket's public-access block
  sets RestrictPublicBuckets and its policy is public (`:1374-1380`,
  `:1541-1547`), judging the policy public as v20.2.4's `is_public` does.
  Squid's radosgw ignores both (`docs/ceph-upstream-bugs.md`, "Squid
  accepts RestrictPublicBuckets but never enforces it"). rgw-go also lets an
  admin user that is not a system user through a suspended bucket's
  refusal and an unparsable bucket policy's, as v20.2.4 does
  (`rgw_op.cc:396`, `:433` and `:642` at v20.2.4); Squid's radosgw exempts
  only system users (`:366`, `:403` and `:612` at v19.2.6). The policy
  language itself, its actions, operators and principals, follows the
  cluster's release.
- **A copy source is checked against its own bucket.** radosgw checks
  CopyObject's and UploadPartCopy's source through the request's state,
  whose bucket is the destination, and takes three inputs from the
  destination (`docs/ceph-upstream-bugs.md`, "radosgw checks a copy source
  with inputs from the destination bucket"): the owner that decides an
  account user's cross-account check and owns a source object's default
  ACL, the tenant the source's policy is parsed with, and the public-access
  block. rgw-go takes every input that describes the source from the
  source's bucket: its owner, its policy parsed with its own tenant, and
  its public-access block, which applies in addition to the destination's,
  so IgnorePublicAcls holds when either block sets it and each block's
  RestrictPublicBuckets is judged against its own bucket's owner. The
  destination supplies requester pays and the `x-amz-expected-bucket-owner`
  comparison, as in radosgw. A copy radosgw allows can therefore be
  refused: an account user's copy from another account's bucket that
  grants it nothing, a cross-tenant copy a Deny in the source's policy
  covers, and a copy of a public source whose owner blocked public access.
- **Tags that cannot be read refuse the request.** When a bucket policy or
  an identity policy names an `s3:ExistingObjectTag` or `s3:ResourceTag`
  key, radosgw reads the object's or bucket's tags into the policy
  environment before it authorizes, and ignores a read that fails or a tag
  set that does not decode, evaluating without the tags (`rgw_op.cc:987`,
  `:1045`, `:1085`, `:1126` and `:5704` at v19.2.6;
  `docs/ceph-upstream-bugs.md`, "radosgw evaluates a tag-conditioned policy
  without the tags it fails to read"), so a Deny conditioned on a tag is
  skipped. rgw-go answers such a request with 500 InternalError instead,
  which op.Run's admin override does not pass. An object that does not
  exist still adds no tags, as in radosgw, and the ACL ops, which add tags
  whatever the policies name, refuse nothing when no policy needs them.
  Only a failed read of the object's head, or a tag attr that neither
  radosgw's binary form nor its text form decodes, differs, and the
  refusal lasts while the attr stays undecodable:
  - When a bucket policy names `s3:ResourceTag` and the bucket's tag attr
    does not decode, every bucket op that reads the bucket's tags, listing
    the bucket and Get, Put and DeleteBucketPolicy and Put and
    DeleteBucketTagging among them, answers 500 to every requester, admins
    and system users included. An object whose tag attr does not decode is
    refused the same way for every object op that reads its tags.
  - Multisite sync, which reads through a system user, stalls on such a
    bucket or object for as long.
  - The repair is outside rgw-go's S3 surface: radosgw-admin, or a radosgw
    gateway serving the same cluster, which ignores the attr, can rewrite or
    remove it, or remove the policy that names the tag.

### Bucket metadata differences

rgw-go reads and writes a bucket's entry point and instance objects as
radosgw's RADOS driver does at the v19.2.6 and v20.2.4 tags. Where the two
differ, rgw-go does the following.

- **An entry point from before version 8 is not converted.** radosgw wrote
  such an entry point before Dumpling (0.67). It is itself an
  `RGWBucketInfo` and names no instance (`RGWBucketEntryPoint::decode`,
  `rgw_common.h:1134-1142` at v19.2.6, `:1177-1185` at v20.2.4). Before it
  sets attrs on an instance without `has_instance_obj`, which no bucket
  created since Octopus carries, radosgw reads the bucket's entry point,
  and on finding one of these converts the bucket: it writes an instance
  from the embedded info and a current entry point that names it
  (`merge_and_store_attrs`, `set_bucket_instance_attrs` and
  `convert_old_bucket_info`, `driver/rados/rgw_sal_rados.cc:771-778` and
  `driver/rados/rgw_bucket.cc:3237-3304` at v19.2.6, `:789-796` and
  `:3359-3421` at v20.2.4). It removes attrs through `put_info`, which
  reads no entry point (`rgw_op.cc:1232-1235` at v19.2.6, `:1469-1472` at
  v20.2.4). rgw-go reads the entry point where radosgw does, before it sets
  attrs, and refuses that write with 500 InternalError, naming the bucket.
  Only a record read by its instance id reaches that write, because neither
  gateway loads such a bucket by name (`docs/ceph-upstream-bugs.md`,
  "[radosgw cannot load a bucket whose entry point is from before version
  8](ceph-upstream-bugs.md#radosgw-cannot-load-a-bucket-whose-entry-point-is-from-before-version-8)").

### Object read differences

rgw-go serves GetObject and HeadObject as radosgw's `RGWGetObj` does: its
range and conditional parsing, the part redirect, and every refusal before
the data is read. It serves GetObjectAttributes, GetObjectTagging and
GetObjectAcl as `RGWGetObjAttrs`, `RGWGetObjTags` and `RGWGetACLs` do.
Where the two differ, checked against the v19.2.6 and v20.2.4 tags, rgw-go
does the following.

- **Torrents.** `?torrent` on an object with a stored torrent answers 501
  NotImplemented, where radosgw serves the torrent. When the
  `user.rgw.torrent` attr is absent, radosgw also looks for the
  `rgw.torrent` omap key that older releases wrote
  (`RadosObject::get_torrent_info`, `driver/rados/rgw_sal_rados.cc:2465-2499`
  at v19.2.6, `:3058-3092` at v20.2.4); rgw-go does not, so it answers 404
  NoSuchKey for an object whose torrent lives only there. An SSE-C object
  and an object with no torrent answer as on radosgw.
- **Swift large objects.** An S3 GET, HEAD or GetObjectAttributes of an
  object carrying `user.rgw.user_manifest` or `user.rgw.slo_manifest`
  answers 501 NotImplemented, where radosgw composes the object from its
  segments (`rgw_op.cc:2373-2394` at v19.2.6, `:2606-2627` at v20.2.4).
  Only radosgw's Swift API, which is excluded, writes such objects.
- **Cloud read-through.** On Tentacle, a GET of an object transitioned to a
  cloud tier that allows read-through answers 403 InvalidObjectState,
  "This object was transitioned to cloud-s3", and logs an error. radosgw
  starts a restore and answers 400 RequestTimeout, "restore is still in
  progress" (`rgw_op.cc:1083-1111` at v20.2.4). Every other answer of the
  cloud-tier check, a restore in progress or done, a tier without
  read-through, a tier that is not an S3 tier and a failed tier lookup, is
  radosgw's.
- **SSE-C reads.** A GET or HEAD of an SSE-C object whose customer-key
  headers pass every check radosgw makes answers 501 NotImplemented until
  rgw-go decrypts SSE-C, which "Key management" above requires; radosgw
  decrypts it. GetObjectAttributes, which makes GetObject's checks, answers
  the same 501, where radosgw makes them without setting up a decryption,
  because it reads no data, and answers the attributes
  (`RGWGetObjAttrs_ObjStore_S3::get_decrypt_filter`, `rgw_rest_s3.cc:3985-3997`
  at v20.2.4). Every refusal before that is radosgw's.
- **SSE-C key headers that are all '='.** rgw-go decodes the SSE-C customer
  key and its MD5 from base64 with a reader that returns an empty result for
  an input of only '=' (`internal/op/readconds.go`, `fromBase64`), so such a
  key or key-MD5 is 400 InvalidArgument, its length being neither the key
  nor the digest size. radosgw's `rgw::from_base64` reads before the start
  of such an input, which ends the gateway (`docs/ceph-upstream-bugs.md`,
  "[radosgw's from_base64 reads before an all-'=' input](ceph-upstream-bugs.md#radosgws-from_base64-reads-before-an-all--input)").
- **A ranged GET of a compressed object does not spin.** rgw-go writes the
  part of each decoded block that lies in the range and stops, whatever
  `rgw_max_chunk_size` is (`internal/compression`, `Stream`). radosgw loops
  forever on a Range that ends at least `rgw_max_chunk_size` bytes before
  the end of a block larger than the serving gateway's `rgw_max_chunk_size`
  (`docs/ceph-upstream-bugs.md`, "[radosgw spins on a ranged GET of a
  compressed block larger than rgw_max_chunk_size](ceph-upstream-bugs.md#radosgw-spins-on-a-ranged-get-of-a-compressed-block-larger-than-rgw_max_chunk_size)").
  When rgw-go writes compressed objects, blocks larger than a coexisting
  radosgw's `rgw_max_chunk_size` would expose that radosgw to the loop.
- **GetObjectTagging of a tag set that does not decode.** rgw-go answers 500
  UnknownError when an object's `user.rgw.x-amz-tagging` decodes neither as
  a tag set nor as the URL-encoded text older objects store. radosgw, by
  code reading, answers 200 with no body (`rgw_rest_s3.cc:746-773` at
  v19.2.6, `:829-856` at v20.2.4; `docs/ceph-upstream-bugs.md`, "[radosgw
  answers GetObjectTagging with 200 and no body when the tags do not
  decode](ceph-upstream-bugs.md#radosgw-answers-getobjecttagging-with-200-and-no-body-when-the-tags-do-not-decode)").
- **NextPartNumberMarker when max-parts is below 1.** For a
  GetObjectAttributes request for ObjectParts whose `x-amz-max-parts` is 0
  or negative, rgw-go answers IsTruncated true and the request's
  `x-amz-part-number-marker`, 0 when absent, as NextPartNumberMarker.
  radosgw answers IsTruncated true with a NextPartNumberMarker it never sets
  (`rgw_rest_s3.cc:4077` and `:4111-4113` at v20.2.4;
  `docs/ceph-upstream-bugs.md`, "[radosgw renders GetObjectAttributes's
  NextPartNumberMarker from an unset variable when max-parts is below
  1](ceph-upstream-bugs.md#radosgw-renders-getobjectattributess-nextpartnumbermarker-from-an-unset-variable-when-max-parts-is-below-1)").
- **An empty header reads as no header.** rgw-go takes `Range`,
  `If-Match`, `If-None-Match`, `If-Modified-Since`, `If-Unmodified-Since`
  and `x-amz-server-side-encryption-customer-algorithm` as absent when they
  are sent with an empty value. radosgw takes each of them as present: an
  empty `Range` turns the head's prefetch off, and on an empty object is 416
  InvalidRange (`rgw_op.cc:2176-2191` and `:2396-2401` at v19.2.6,
  `:2411-2426` and `:2629-2634` at v20.2.4); an empty `If-Match` is 412
  PreconditionFailed, as no ETag starts the empty string, and turns
  `If-Unmodified-Since` off; an empty `If-None-Match` turns
  `If-Modified-Since` off; an empty date is 400 InvalidArgument; and an
  empty algorithm is 400 InvalidEncryptionAlgorithmError rather than
  InvalidArgument. radosgw reads the customer key and key-MD5 headers with
  an empty default (`rgw_crypt.cc:1341` and `:1359` at v19.2.6, `:1360` and
  `:1379` at v20.2.4), so for those two an empty value is an absent one on
  both gateways.
- **X-Rgw-Auth.** radosgw answers a GET or HEAD carrying an `X-Rgw-Auth`
  header, for its cache server's authentication check, with the status and
  headers alone and a Content-Length of 0, and makes none of the torrent,
  compression, cloud-tier, Swift large object, range and SSE checks that
  follow the conditionals; a GET carrying it does not prefetch
  (`rgw_op.cc:2179` and `:2271-2274` at v19.2.6, `:2414` and `:2507-2510`
  at v20.2.4). rgw-go ignores the header: it makes those checks, which can
  refuse the request, and serves a GET's body.
- **A refused response-* parameter.** rgw-go answers 400 InvalidRequest to
  a request whose `response-*` parameter radosgw refuses, because the
  request is anonymous or the value holds a control character. radosgw
  makes that check while it writes the response headers
  (`rgw_rest_s3.cc:513-533` at v19.2.6, `:630-650` at v20.2.4). For a HEAD,
  and for a GET with nothing to read, it ignores the refusal
  (`rgw_op.cc:2436-2439` at v19.2.6, `:2668-2671` at v20.2.4) and, by code
  reading, sends no response at all; for any other GET it sends its
  success status line, 200, or 206 for a Range GET (`rgw_rest_s3.cc:398` at
  v19.2.6, `:409` at v20.2.4), and then the 400 response
  (`docs/ceph-upstream-bugs.md`,
  "[radosgw sends no response, or two status lines, when it refuses a
  response-* parameter](ceph-upstream-bugs.md#radosgw-sends-no-response-or-two-status-lines-when-it-refuses-a-response--parameter)").
- **Conditionals use the object's mtime alone.** Tentacle compares
  `If-Modified-Since` and `If-Unmodified-Since` with the later of the
  object's mtime and its `user.rgw.rgw-internal-mtime` attr
  (`obj_time_weight::init`, `driver/rados/rgw_rados.cc:4211-4227` at
  v20.2.4), which a cloud transition writes and a restore rewrites
  (`driver/rados/rgw_sal_rados.cc:3487` and `:3567`, and
  `driver/rados/rgw_rados.cc:5642`, at v20.2.4). rgw-go compares the mtime
  alone, so where that attr is later, it answers 304 Not Modified where
  Tentacle serves the object, and serves it where Tentacle answers 412
  PreconditionFailed. Squid has no such attr.

### Object write differences

rgw-go writes an object's tails, head and bucket index entry as radosgw's
`AtomicObjectProcessor` and `RGWRados::Object::Write::write_meta` do at the
v19.2.6 and v20.2.4 tags, and queues an overwritten object's tails for the
GC as `complete_atomic_modification` does. Where the two differ, rgw-go
does the following.

- **A GC chain object too large for one entry is queued alone.** rgw-go
  splits an overwritten object's tails into GC entries whose estimated
  encoding stays within `rgw_max_chunk_size`, as `send_split_chain` does
  (`driver/rados/rgw_gc.cc:68-118` at v19.2.6 and v20.2.4), but an object
  whose estimate alone passes that size goes into an entry of its own.
  radosgw then never finishes the write: it decrements its iterator past
  the object, sends an empty entry and comes back to the same object
  (`docs/ceph-upstream-bugs.md`, "[radosgw's send_split_chain repeats an
  object in its remainder and loops on an object too large for an
  entry](ceph-upstream-bugs.md#radosgws-send_split_chain-repeats-an-object-in-its-remainder-and-loops-on-an-object-too-large-for-an-entry)").
  Only an `rgw_max_chunk_size` smaller than one tail's estimate, a few
  hundred bytes, reaches it.
- **A negative or overlong gc object wait.** Ceph's config sets no limit on
  `rgw_gc_obj_min_wait` (`common/options/rgw.yaml.in:1711` at v19.2.6,
  `:1799` at v20.2.4). radosgw narrows it to the `uint32_t` expiration every
  gc enqueue takes (`driver/rados/rgw_gc_log.cc:29-30`;
  `cls/rgw_gc/cls_rgw_gc_client.cc:50` at both tags), and the class adds
  that to the OSD's clock and stores the due time in 32-bit seconds. So a
  value from -1 down to about -1.79e9, or one whose narrowed value takes the
  due time past 2106, wraps to a time already gone, and the next gc pass
  frees the tails under any reader still fetching them
  (`docs/ceph-upstream-bugs.md`, "[A negative or overlong
  rgw_gc_obj_min_wait makes radosgw's gc free overwritten tails at its next
  pass](ceph-upstream-bugs.md#a-negative-or-overlong-rgw_gc_obj_min_wait-makes-radosgws-gc-free-overwritten-tails-at-its-next-pass)").
  rgw-go never sends such a wait.
  - It uses the option's default, 7200 s, for a negative value, logging an
    error naming the option and the value it read.
  - It sends a positive value whole while the due time it gives stays a day
    short of 2106 by the gateway's clock, and that bound otherwise: the
    tails are then due shortly before 2106, never at once, whatever the
    value, where radosgw keeps only its low 32 bits. The day covers an
    OSD clock running ahead of the gateway's. rgw-go logs an error at
    startup for a value past that bound.
- **The gc worker opens a chain object's pool even when it has none.**
  rgw-go collects the gc shards as radosgw's `RGWGC::process` does
  (`driver/rados/rgw_gc.cc:550-727` at v19.2.6, `:565-739` at v20.2.4), but
  it opens the pool of a page's first object, and of an object after one
  whose pool failed to open, whatever the pool's name. radosgw opens a pool
  only when the name differs from the last one it opened, starting each page
  from the empty name but keeping the pass's I/O context, so an object whose
  chain records no pool, as `update_gc_chain` records for a stripe whose
  placement resolves none, keeps whatever context the pass holds
  (`:633` and `:660-677` at v19.2.6, `:648` and `:672-689` at v20.2.4). On a
  shard's first page, or after a failed open, that context was never opened
  and the process faults; on a later page the put goes to the previous
  page's last pool, usually answers ENOENT, and the entry is removed with
  its tail leaked (`docs/ceph-upstream-bugs.md`, "[radosgw's gc processor
  faults on a chain object without a
  pool](ceph-upstream-bugs.md#radosgws-gc-processor-faults-on-a-chain-object-without-a-pool)").
  In rgw-go the empty name fails to open on every page, as any missing pool
  does: an omap-era entry skips the object and keeps its tag, and a
  queue-era entry ends the shard's pass and stays queued.
- **Inline tail deletes run in parallel on Squid too.** When the GC shard
  refuses an overwritten object's tails, rgw-go deletes them inline with
  up to `rgw_multi_obj_del_max_aio` refcount puts in flight, as
  `delete_objs_inline` does at v20.2.4 (`driver/rados/rgw_rados.cc:6155-6194`).
  At v19.2.6 radosgw sends them one at a time (`:5432-5462`). rgw-go puts
  each object's ref through its own pool and locator, as v19.2.6 does,
  where v20.2.4 sends every put through the first object's pool and drops
  the locator (`driver/rados/rgw_rados.cc:6162-6185`; `rgw_aio.cc:61-63` and
  `:100` at v20.2.4; `docs/ceph-upstream-bugs.md`, "[Tentacle's
  delete_objs_inline puts every ref through the first object's pool,
  without its
  locator](ceph-upstream-bugs.md#tentacles-delete_objs_inline-puts-every-ref-through-the-first-objects-pool-without-its-locator)").
  For every chain radosgw builds, the requests match v20.2.4's: its
  objects share one tail pool and carry no locator, whether they are
  shadow-namespace tails or, for an overwritten multipart object,
  multipart-namespace parts. Only their order and overlap differ from
  v19.2.6's.
- **Write conditions are Tentacle's on Squid too.** rgw-go checks a PUT's
  If-Match and If-None-Match as v20.2.4's `check_preconditions` does
  (`driver/rados/rgw_rados.cc:7286-7328` at v20.2.4), on both releases,
  keeping its comparison of a condition's start over the ETag's length. A
  client of a Squid zone sees these differences from v19.2.6
  (`prepare_atomic_modification`, `:6493-6544` at v19.2.6):
  - The conditions of a head with a manifest and no write tag, whose tag
    radosgw fakes, are checked, where v19.2.6 skips them and overwrites.
  - A quoted If-Match naming the current ETag is accepted, where v19.2.6
    answers 412 PreconditionFailed.
  - A quoted If-None-Match naming the current ETag answers 412
    PreconditionFailed, where v19.2.6 overwrites.
  - If-None-Match naming an ETag succeeds on a missing key and on an
    existing object with no ETag, where v19.2.6 answers 412
    PreconditionFailed.
  - If-Match, `*` or an ETag, on a missing key answers 404 NoSuchKey, where
    v19.2.6 answers 412 PreconditionFailed.

  v19.2.6 skips the check on a head with a fake tag (`need_guard`,
  `:6493-6495` at v19.2.6), which lets a conditional write overwrite an
  update it was meant to protect; [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348)
  fixed it for Tentacle, and its Squid backport,
  [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932), merged after
  v19.2.6 (`docs/ceph-upstream-bugs.md`, "[Squid's write conditions fail an
  If-None-Match ETag on a missing key and skip a head without a write
  tag](ceph-upstream-bugs.md#squids-write-conditions-fail-an-if-none-match-etag-on-a-missing-key-and-skip-a-head-without-a-write-tag)").
- **Delete conditions are Tentacle's, and a delete is guarded, on both
  releases.** rgw-go checks a DeleteObject's If-Match,
  `x-amz-if-match-size` and `x-amz-if-match-last-modified-time` as
  v20.2.4's `Delete::delete_obj` does, with `check_preconditions` and an
  `obj_check_mtime` EQ in the removal op
  (`driver/rados/rgw_rados.cc:6663-6671` and `:7260-7329` at v20.2.4), on
  both releases. On both it also sends the head's removal behind v19.2.6's
  `cmpxattr` on the write tag the delete read (`prepare_atomic_modification`,
  `:5914` and `:6493-6513` at v19.2.6). A client sees these differences:
  - On a Squid zone, a delete that carries one of the three conditions and
    fails it answers 412 PreconditionFailed, where v19.2.6, which does not
    implement them, deletes: its S3 handler reads only
    `x-amz-delete-if-unmodified-since` (`rgw_rest_s3.cc:3427-3453` at
    v19.2.6) and `RGWDeleteObj` has no member for the others
    (`rgw_op.h:1461-1495` at v19.2.6). Conditional delete came upstream with
    [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348) (tracker
    [#68183](https://tracker.ceph.com/issues/68183)), in v20.2.4 through
    [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949); it reaches
    Squid at 19.2.7 through
    [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932), merged after
    v19.2.6.
  - On a Tentacle zone, a delete whose object another writer replaced
    between the delete's read and its removal fails the removal and answers
    204 with the replacement standing, as on Squid. v20.2.4 removes the
    replacement, even under an If-Match naming the replaced object's ETag,
    and leaves the replacement's tails unreferenced
    (`docs/ceph-upstream-bugs.md`, "[Tentacle's DeleteObject removes the
    head without checking its write
    tag](ceph-upstream-bugs.md#tentacles-deleteobject-removes-the-head-without-checking-its-write-tag)").
    The removal op carries the `cmpxattr` step v20.2.4 does not send.
- **An attribute change adds no expirer hint.** rgw-go changes an object's
  attrs as radosgw's `RGWRados::set_attrs` does
  (`driver/rados/rgw_rados.cc:6593-6757` at v19.2.6, `:7393-7593` at
  v20.2.4), except that a change setting the Swift delete-at attr adds no
  object expirer hint, where radosgw adds one (`:6643-6655` at v19.2.6,
  `:7443-7455` at v20.2.4). rgw-go runs no expirer ("Swift API and Swift
  authentication").
- **A failed part upload does not shrink the quota cache.** When an
  UploadPart fails after its part head was created, rgw-go removes the
  part as radosgw's `~RadosWriter` does, the head through the bucket index
  when it sits in the pool the bucket's placement names
  (`driver/rados/rgw_putobj_processor.cc:184-229` at v19.2.6, `:212-257`
  at v20.2.4). radosgw's removal, `RGWRados::delete_obj`, then subtracts
  one object and the head's size from the bucket's and the owner's cached
  stats (`driver/rados/rgw_rados.cc:5984` at v19.2.6, `:6738` at v20.2.4)
  even when the part failed before its head was counted: a short body, a
  failed quota check, a wrong Content-MD5, a failed payload signature or a
  body past `rgw_max_put_size`. rgw-go subtracts the head only when its
  write had counted the part, as it has for a part that fails while it is
  registered, so after a failure before that its cache keeps one object
  and up to one stripe more than radosgw's until the entry is fetched
  again (`docs/ceph-upstream-bugs.md`, "[A failed UploadPart shrinks
  radosgw's quota
  cache](ceph-upstream-bugs.md#a-failed-uploadpart-shrinks-radosgws-quota-cache)").
- **A part whose head write lost a race is not registered.** radosgw
  answers a part head write that fails with ECANCELED, ENOENT or EEXIST
  as a lost race and success (`driver/rados/rgw_rados.cc:3388-3391` at
  v19.2.6, `:3541-3544` at v20.2.4), registers the part on the upload's
  meta object, and then, because the write was canceled, removes the
  part's objects (`driver/rados/rgw_putobj_processor.cc:604-606` at
  v19.2.6, `:643-645` at v20.2.4), leaving a registered part whose data is
  gone (`docs/ceph-upstream-bugs.md`, "[radosgw registers a part whose
  head write lost a race and then removes its
  data](ceph-upstream-bugs.md#radosgw-registers-a-part-whose-head-write-lost-a-race-and-then-removes-its-data)").
  rgw-go removes the objects and fails the UploadPart with 500
  InternalError without registering it. The part head write is a
  non-atomic one without conditions or a create, so no reply from an OSD
  is known to reach this path.
- **UploadPart ignores If-Match and If-None-Match.** rgw-go writes a part
  head without the request's write conditions on both releases, as
  v19.2.6's `MultipartObjectProcessor::complete` does
  (`driver/rados/rgw_putobj_processor.cc:491-531` at v19.2.6). v20.2.4's
  passes them to the part head's write (`:563-564`), where they are
  checked against the part head the request has just created: If-None-Match:
  `*`, and If-Match naming an ETag, then fail with 412 PreconditionFailed for
  a part in the bucket placement's pool, and any If-Match with 404
  NoSuchKey for a part whose storage class lives in another pool, after
  the body was received (`docs/ceph-upstream-bugs.md`, "[Tentacle fails an
  UploadPart that sends If-None-Match: * for a part in the bucket
  placement's pool](ceph-upstream-bugs.md#tentacle-fails-an-uploadpart-that-sends-if-none-match--for-a-part-in-the-bucket-placements-pool)").
  Where a Tentacle radosgw answers those, rgw-go stores the part.
- **UploadPartCopy reads one version of its source.** rgw-go copies a
  part's source range from the one state the request read: the head bytes
  that read prefetched, the rest of the head through reads guarded by its
  write tag, and the tails its manifest names, so the part holds that
  version or the copy fails. radosgw reads the source's state
  again for each `rgw_max_chunk_size` piece of the range (`RGWPutObj::get_data`,
  `rgw_op.cc:4018-4085` at v19.2.6, `:4227-4294` at v20.2.4) and copies the
  rest of the range from the new version; a shorter new version fails the
  copy with 416 InvalidRange after the leading pieces were written, and an
  emptied one ends the part short without an error
  (`docs/ceph-upstream-bugs.md`, "[radosgw's
  UploadPartCopy can assemble a part from two versions of its
  source](ceph-upstream-bugs.md#radosgws-uploadpartcopy-can-assemble-a-part-from-two-versions-of-its-source)").
- **An upload without a v2 id keys a part by its canonical number.**
  radosgw registers a part of an upload whose id lacks the `2~` or `2/`
  prefix under `part.` and the part number as the request spelled it
  (`part_num_str`, `driver/rados/rgw_putobj_processor.cc:534-543` at
  v19.2.6, `:572-581` at v20.2.4). The driver receives the number as an
  integer and writes it in decimal without leading zeros, so a request
  spelling it `07` registers `part.7` where radosgw registers `part.07`.
  Only an upload that a radosgw older than the v2 ids created reaches
  this; rgw-go and every current radosgw make v2 ids.
- **A copy keeps its tail references when its head write timed out.** A
  copy that shares its source's tails takes a refcount reference on each
  under its tag before it writes its head, as radosgw's `copy_obj` does.
  When the head write fails, radosgw drops the references again
  (`done_ret`, `driver/rados/rgw_rados.cc:4983-4986` and `:4990-5031` at
  v19.2.6, `:5243-5246` and `:5250-5290` at v20.2.4), even after
  ETIMEDOUT, when the head may yet land; the copy's head then names tails
  that hold no reference for it, and they are removed with their source
  (`docs/ceph-upstream-bugs.md`, "[radosgw's copy drops its tail
  references after a head write that timed
  out](ceph-upstream-bugs.md#radosgws-copy-drops-its-tail-references-after-a-head-write-that-timed-out)").
  rgw-go keeps them after a timeout, as radosgw keeps a PUT's tails then
  (`AtomicObjectProcessor::complete`, `rgw_putobj_processor.cc:395-401` at
  v19.2.6), so a copy whose head never lands leaks its references instead.
  Only a gateway with `rados_osd_op_timeout` set sees a timeout.
- **A copy drops its tail references when its head write loses a race.**
  When a copy's head write meets a head another writer replaced, removed
  or created, radosgw answers success, as for any unconditional write,
  and keeps the references it took, which no head names: the tails then
  outlive their source for good (`write_meta` returning 0 with
  `meta.canceled`, `driver/rados/rgw_rados.cc:3369-3391` at v19.2.6,
  `:3522-3544` at v20.2.4, and `copy_obj` returning at `:4988`, `:5248`;
  `docs/ceph-upstream-bugs.md`, "[radosgw's copy keeps its tail references
  when its head write loses a
  race](ceph-upstream-bugs.md#radosgws-copy-keeps-its-tail-references-when-its-head-write-loses-a-race)").
  rgw-go also answers success and drops them, as `done_ret` would.
- **A copy onto itself is guarded on the head it read.** A copy of an
  object onto itself keeps the object's tails and writes its manifest back
  under a new head. radosgw writes that head as any other: an exclusive
  create, and on EEXIST a guarded write on the head as it reads it then
  (`write_meta`, `driver/rados/rgw_rados.cc:3419-3440` at v19.2.6), not on
  the head the copy was read from (`RGWCopyObj::execute`, `rgw_op.cc:5599`
  at v19.2.6, `:6164` at v20.2.4). A PUT or DELETE of the key in between
  has already queued those tails for the GC, and the copy then lands the
  old manifest on them, over the PUT's head or in place of the deleted
  one, so the object loses its tails once the GC runs
  (`docs/ceph-upstream-bugs.md`, "[radosgw's copy of an object onto itself
  can land its old manifest on tails queued for the
  GC](ceph-upstream-bugs.md#radosgws-copy-of-an-object-onto-itself-can-land-its-old-manifest-on-tails-queued-for-the-gc)").
  rgw-go writes a copy onto itself in one guarded write, compared with the
  write tag the copy read, or with its stored manifest when it has none,
  and without the exclusive create. A head replaced since answers 409
  ConcurrentModification, as radosgw answers a read whose head guard fails
  (ECANCELED, `rgw_common.cc:141` at v19.2.6, `:143` at v20.2.4); a head
  removed since answers 404 NoSuchKey, as a copy of a missing source does.
  Either way the newer object, or the deletion, stands, where radosgw
  answers 200.

### Bucket index differences

rgw-go reads a bucket's index and its owner's bucket list as radosgw's
RADOS driver does at the v19.2.6 and v20.2.4 tags. Where the two differ,
rgw-go does the following.

- **A missing index pool is not created.** rgw-go opens a bucket's index
  pool and the pool of its owner's bucket list without creating either, and
  a request that needs one found missing fails, naming it. radosgw creates
  the index pool when it opens it (`open_pool`, which calls
  `rgw_init_ioctx` with create set, `services/svc_bi_rados.cc:35-41` at
  v19.2.6, `:43-49` at v20.2.4), and the bucket list's pool through
  `rgw_get_rados_ref` (`driver/rados/rgw_tools.cc:107-108` at v19.2.6,
  `:108-109` at v20.2.4), as "No bootstrap" describes for the other pools.
- **Index shard headers are read as Squid reads them.** On both releases
  rgw-go reads each index shard's header through cls_rgw's `bucket_list` of
  no entries, as Squid's radosgw does (`cls_bucket_head`,
  `services/svc_bi_rados.cc:323-352` at v19.2.6). A missing shard fails the
  read with ENOENT, and a shard object without a header reads as an empty
  header (`read_bucket_header`, `cls/rgw/cls_rgw.cc:464-485` at v19.2.6,
  `:514-535` at v20.2.4). Tentacle's radosgw reads the omap headers
  directly and answers -EIO for both (`services/svc_bi_rados.cc:331-409`
  at v20.2.4; `docs/ceph-upstream-bugs.md`, "[Tentacle's radosgw answers
  EIO for an index shard its header read means to
  skip](ceph-upstream-bugs.md#tentacles-radosgw-answers-eio-for-an-index-shard-its-header-read-means-to-skip)").
  On a Tentacle cluster, a read of such a bucket's index stats therefore
  fails with ENOENT in rgw-go where radosgw fails with -EIO, or succeeds in
  rgw-go, counting the header-less shard as empty, where radosgw fails.

### Bucket listing differences

rgw-go lists a bucket, in ListObjects v1 and v2 and in the versions
listing, as radosgw's `RGWListBucket` and `RGWRados::Bucket::List` do at
the v19.2.6 and v20.2.4 tags. Where the two differ, rgw-go does the
following.

- **The versions of a bucket whose versioning was ever enabled are not
  listed.** `GET /<bucket>?versions` on a bucket that has never had
  versioning enabled lists every object as its "null" version, as radosgw
  does. On a bucket whose versioning is enabled or suspended rgw-go answers
  501 NotImplemented until versioning is served (phase 2); radosgw lists
  its versions and delete markers.
- **A pending multipart upload's index entry is not reconciled by a
  listing.** A listing checks an index entry that has a pending operation,
  or that is not there, against the object's head and suggests the repair
  to the index shard. For a multipart upload's `.meta` entry, one in the
  `multipart` namespace, radosgw makes that check in the bucket's
  data-extra pool (`check_disk_state`,
  `driver/rados/rgw_rados.cc:10341-10344` at v19.2.6, `:11266-11268` at
  v20.2.4); rgw-go leaves such an entry as it is and suggests nothing, so
  the upload listing shows it until a write completes it or
  `radosgw-admin bucket check --fix` repairs it.
- **A plain object whose name ends in .meta is checked where it is
  stored.** radosgw chooses the data-extra pool by the raw index name
  alone (`MultipartMetaFilter`, at the lines above), so it also checks a
  plain object named like `notes.v1.meta` there. It does not find it, hides
  it from the listing and suggests removing its index entry, so after an
  interrupted write such an object can vanish from listings
  (`docs/ceph-upstream-bugs.md`, "radosgw's listing checks a plain object
  named like a multipart meta object in the wrong pool"). rgw-go checks
  every entry outside the `multipart` namespace in the data pool, and lists
  and repairs such an object as any other.
- **Listing-time suggestions are bounded.** radosgw sends each shard's
  `dir_suggest_changes` with `aio_operate` and never waits for it
  (`:9909` and `:10144` at v19.2.6, `:10831` and `:11068` at v20.2.4).
  rgw-go sends them in the background too and never waits either, but
  keeps at most `rgw_bucket_index_max_aio` in flight, each for at most 30
  seconds; a shard's suggestions that find no free slot are dropped, and the
  next listing of the same entries suggests them again.
- **A shard that answers advance-and-retry fails an ordered listing.**
  cls_rgw's `bucket_list` answers `RGWBIAdvanceAndRetryError` (-EFBIG) with
  a marker when eight omap reads find nothing visible, and radosgw's
  ordered listing lists that shard again from the marker
  (`cls/rgw/cls_rgw_client.cc:99-110` and `:161-166` at v19.2.6;
  `ListReader`, `rgw/services/svc_bi_rados.cc:948-965` at v20.2.4).
  rgw-go's seam keeps no output for a class call that fails, so its
  ordered listing fails with 500 UnknownError instead. The unordered
  listing is the same in both: radosgw's reads the shard with
  `rgw_rados_operate` directly and fails with 500 too (`:10071` at v19.2.6,
  `:10993` at v20.2.4). Only a run of delete markers or noncurrent
  versions longer than eight reads makes a shard answer so, and versioning
  is not served yet.
- **A system user's listing carries no sync fields.** For a system user's
  request radosgw adds `RgwxTag` to each entry, `VersionedEpoch` and
  `RgwxMtime` to each version, reads `objs-container` and the
  `HTTP_RGWX_SHARD_ID` header, and can list a single index shard
  (`rgw_rest_s3.cc:1724-1737`, `:1838-1845` and `:1947-1949` at v19.2.6,
  `:1835-1848`, `:1949-1956` and `:2058-2060` at v20.2.4). These serve
  multisite sync, which rgw-go excludes, and rgw-go renders a system
  user's listing as any other user's.

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
- Part lookups in an object whose manifest lays out more than 2^26
  stripes are refused past that bound, or outright when its tail alone
  needs more. radosgw writes such a multipart object at its default size
  limits only with an `rgw_obj_stripe_size` of about 800,000 bytes or less,
  and such an appendable object with enough appends, about 52,429 of 5 GiB
  at the default stripe size. `Stripes` lays out no object over 2^21
  stripes, about 8 TiB at the default stripe size, so whole-object walks of
  larger objects iterate.
