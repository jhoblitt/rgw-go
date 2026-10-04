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
    blanks in its place: radosgw accepts it, and rgw-go refuses it with 400
    InvalidArgument. rgw-go also refuses with 400 InvalidArgument a
    malformed final chunk line and a signed chunk header in any form but
    `<size>;chunk-signature=<64 bytes>`, the key included.
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
  fails.** rgw-go walks a manifest's stripes from its start in two ways:
  `meta.Manifest.Stripes`, the whole layout, which it builds in memory, and
  `meta.PartWalk`, the part walk behind `meta.Manifest.PartBounds`, the
  part lookup of GET and HEAD with `partNumber`, and behind
  GetObjectAttributes's ObjectParts listing. Each fails with
  `denc.ErrMalformed` at the first step that does not move past the
  previous offset, and with `meta.ErrTooManyStripes` past its bound,
  `meta.MaxStripes` (2^21 stripes) for `Stripes` and `meta.MaxWalkStripes`
  (2^26) for the part walk; each refuses at once a tail that needs more
  stripes than its bound. The operation that needs the walk fails instead
  of hanging. radosgw's walks check neither
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
  stat ignores a data pool it cannot resolve").
- **No bootstrap.** rgw-go writes nothing at startup but the control
  objects, which radosgw creates too. When no zone or zonegroup resolves,
  it refuses to start with `driver.ErrNoZone`, naming the object it looked
  for, and it refuses to start on a root pool or a control pool it cannot
  open. A metadata pool found missing later fails the request that needs
  it, naming the pool. A data pool found missing is not created either:
  rgw-go stats an object in it as absent, and a read or write that needs
  the pool fails, naming it. radosgw creates what is missing instead, a
  data pool on the first stat, read or write that opens it.
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
  v19.2.6, `:1718-1731` at v20.2.4) and
  `rgw_override_bucket_index_max_shards` at each bucket creation
  (`init_default_bucket_layout`, `driver/rados/rgw_bucket.cc:2790-2796` at
  v19.2.6, `:2908-2910` at v20.2.4).
- **Shard counts and the bucket-index AIO limit that are not positive.** A
  zero or negative `rgw_usage_max_shards`, `rgw_usage_max_user_shards` or
  `rgw_lc_max_objs`, and a zero `rgw_bucket_index_max_aio`, is used as 1,
  and rgw-go logs an error naming the option and the value it read. With
  `rgw_usage_max_shards` at 1, rgw-go names every usage object `usage.0`.
  Ceph's config refuses an `rgw_usage_max_user_shards` below its minimum of
  1 before either gateway reads it (`Option::validate`,
  `common/options.cc:99-109` at v19.2.6, `:100-110` at v20.2.4). On a zero
  AIO limit radosgw skips or never finishes every batch of bucket-index
  operations ("radosgw skips or never finishes every bucket-index batch on
  a zero rgw_bucket_index_max_aio"). radosgw accepts a zero or negative
  `rgw_usage_max_shards` or `rgw_lc_max_objs`:
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
- **Counts too large for radosgw's integers.** rgw-go holds a shard count in
  32 bits, using a larger one as 2^32-1, and the bucket-index AIO limit in
  63, using a larger one as 2^63-1. It caps `rgw_lc_max_objs` at 7877.
  radosgw caps it at 7877 too, but only after narrowing it to a 32-bit
  `int` (`rgw_lc.cc:237-239` at v19.2.6 and v20.2.4): 2^31 becomes INT_MIN
  and throws at startup, as a negative value does, and 2^32 becomes 0.
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
- **GetObjectTagging of a tag set that does not decode.** rgw-go answers 500
  UnknownError when an object's `user.rgw.x-amz-tagging` decodes neither as
  a tag set nor as the URL-encoded text older objects store. radosgw, by
  code reading, answers 200 with no body (`rgw_rest_s3.cc:746-773` at
  v19.2.6, `:829-856` at v20.2.4).
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
