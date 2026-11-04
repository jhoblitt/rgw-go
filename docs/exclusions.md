# Excluded features

rgw-go will not implement the features below. Each links to its entry,
which says what the exclusion covers, why, and what rgw-go must still do
so that a zone it shares with radosgw stays safe. How rgw-go behaves
differently from radosgw within the features it does implement is in
[docs/differences.md](differences.md).

| Feature | Reason |
|---|---|
| [Multisite](#multisite) | Zone-to-zone sync needs two or more zones and does nothing on a single-zone data path, and its async model does not port straightforwardly. |
| [Every storage driver except RADOS](#every-storage-driver-except-rados) | The goal is bit-compatibility with RGW's RADOS layout, which is one driver. |
| [Swift API and Swift authentication](#swift-api-and-swift-authentication) | A separate protocol off the S3 path, which Rook's probes do not depend on. |
| [Lua scripting](#lua-scripting) | Excluded in the project brief; a bounded addition later if ever wanted. |
| [S3 Select](#s3-select) | A large SQL query engine kept as its own component, off the data path. |
| [S3 Vectors and object dedup](#s3-vectors-and-object-dedup) | Both are newer than Squid and experimental, and S3 Vectors is Rust in the Ceph tree. |
| [Cloud transition and restore](#cloud-transition-and-restore) | It needs a remote cloud endpoint; storage-class transitions inside the cluster are not excluded. |
| [Keystone and LDAP authentication](#keystone-and-ldap-authentication) | External identity systems with their own clients and token caches; local users, accounts, roles and OIDC providers are in scope. |
| [Key management: SSE-KMS and SSE-S3 with every backend](#key-management-sse-kms-and-sse-s3-with-every-backend) | Every backend is an external key server, and RGW's SSE-S3 requires Vault; SSE-C is in scope. |
| [Dynamic resharding worker](#dynamic-resharding-worker) | Off the hot path and fully substituted by `radosgw-admin bucket reshard` and `radosgw-admin reshard process`; the reshard protocol on the data path is kept. |
| [librgw, NFS and SMB](#librgw-nfs-and-smb) | Not the S3 daemon. |
| [QAT and UADK hardware offload](#qat-and-uadk-hardware-offload) | Hardware-specific; the software codecs and crypto are in scope. |
| [D3N local read cache](#d3n-local-read-cache) | A cache between the gateway and RADOS changes what the benchmark measures, and upstream marks it experimental. |
| [Kafka and AMQP delivery](#kafka-and-amqp-delivery) | External message brokers with their own client libraries and auth models; the rest of bucket notifications is in scope. |
| [The radosgw-admin CLI](#the-radosgw-admin-cli) | A scope statement rather than an exclusion: the unmodified C++ tool administers all of rgw-go's metadata. |

Status: draft, 2026-09-25, revised the same day after review by the rgw-rs
session and further decisions (D3N excluded; bucket notifications kept
with Kafka and AMQP delivery excluded, sized against Rook's integration
tests). Companion to the design spec, which is not yet written. Everything
under "Excluded" was decided in the rgw-go design session. Every claim
about C++ behavior below was checked against ceph/ceph main at
e234256339f, and its claims about main again at 8aa24c51928 (2026-10-30),
and, where a release is named, against the v19.2.3 tag.

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
maintainable code base. The Ceph version floor is Squid 19.2.7 or Tentacle
20.2.4 for every daemon, OSDs and any radosgw on the zone included; data
written by any earlier release is still decoded (design spec, section 3).
The feature level is Tentacle radosgw wherever a feature is gateway-side.
That principle has a precise form, which splits by layer and is recorded
in docs/differences.md, "The feature level".

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
So rgw-go's lifecycle transition does not reproduce radosgw's choice to
keep a transition out of the bucket-index log where every zone of the
zonegroup runs lifecycle itself, `zonegroup_lc_check`
([rgw_lc.cc:579-599](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L579-L599),
[rgw_lc.cc:587-606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L587-L606)),
which decides its `FLAG_LOG_OP`
([rgw_lc.cc:1513-1514](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1513-L1514),
[rgw_lc.cc:1559-1560](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1559-L1560)):
its index change is logged as any write's is, which in a single zone is not
at all.

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

Objects written through Swift as dynamic or static large objects carry
`user.rgw.user_manifest` or `user.rgw.slo_manifest`, and radosgw composes
them from their segments even on an S3 GET or HEAD. rgw-go does not: it
answers 501 NotImplemented on GET and HEAD rather than serve the manifest
object itself as the data. Such objects exist only where a client wrote
them through Swift.

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

What: lifecycle transitions to a cloud tier, RestoreObject,
`/admin/restore`, and the expiry of restored copies that v20.2.4's
lifecycle worker runs after each pass
([rgw_lc.cc:2079-2083](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2079-L2083)).

Why: needs a remote cloud endpoint. Storage-class transitions inside the
cluster are not excluded.

Still required: an object whose manifest says its data lives in a cloud
tier must be reported as such and never served as if it were local.

rgw-go does so as radosgw does: a cloud-tiered object answers 403
InvalidObjectState on GET, with the headers alone on HEAD, and on Tentacle
a restored copy is served and a restore in progress answers 400
RequestTimeout. One case differs: where a Tentacle tier allows
read-through, radosgw starts a restore from the cloud and answers 400
RequestTimeout ("restore is still in progress"), while rgw-go, which runs
no restore, answers the 403 (docs/differences.md, "Object read differences", "Cloud
read-through").

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

1. SSE-C is implemented in phase 4, byte-compatible with RGW's scheme,
   so that objects radosgw wrote with a customer key decrypt. Until then
   rgw-go answers SSE-C reads and writes with 501, so it neither stores in
   the clear what a client asked to have encrypted nor serves ciphertext.
2. The three bucket-encryption configuration ops behave as a radosgw
   with no backend configured, and any use of `aws:kms` or `AES256`
   fails with radosgw's error. A configuration is stored in radosgw's
   `RGWBucketEncryptionConfig` bytes, and one radosgw stored is read as
   stored, but not every document round-trips: PutBucketEncryption
   refuses with 400 MalformedXML a document outside S3's schema, an
   `SSEAlgorithm` outside AWS's list, and a `KMSMasterKeyID` beside an
   algorithm other than `aws:kms` or `aws:kms:dsse`, all of which
   radosgw stores (docs/differences.md, "A PutBucketEncryption document is held to S3's
   schema and AWS's values"), and GetBucketEncryption writes a bucket
   key as `true` where radosgw writes `True` (docs/differences.md, "GetBucketEncryption
   writes BucketKeyEnabled as true").
3. Objects radosgw encrypted with a key server carry a crypt-mode xattr.
   rgw-go reads it and refuses with the error radosgw gives when its
   backend is unavailable. Ciphertext is never served as plaintext.

Costs accepted: the s3-tests encryption groups that use the testing
backend are skipped, and Rook's `security.kms` and `security.s3` object
store settings have no effect.

### Dynamic resharding worker

What: the background worker that scans bucket stats, decides a bucket
needs resharding, and executes the reshard.

Why: off the hot path; roughly 1.4k lines in Squid and 2k in main at
8aa24c51928; fully substitutable by `radosgw-admin bucket reshard` and
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
class lacks the newer index methods that main's radosgw (8aa24c51928)
probes for (`bucket_init_index2`, `bucket_refresh_instance`,
`bi_put_entries`, `reshard_log_trim`), so rgw-go uses the original index-init method and
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

An index call that finds its shard gone, because a reshard committed after
the shard was found and removed the old index, is retried on both releases.
rgw-go reads the bucket instance again and, when the instance now puts the
object on another shard, sends the call there within the same ten calls,
without starting the count over. A v19.2.7 radosgw retries that ENOENT too,
but sends the call again whatever shard the instance names, up to ten times
([rgw_rados.cc:7225-7230](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7225-L7230),
[rgw_rados.cc:7929-7933](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7929-L7933), the bound at
[rgw_rados.cc:7216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7216)); rgw-go answers the
ENOENT at once when the instance names the same shard, so such a write
answers 404 NoSuchKey after one call where v19.2.7's answers it after ten.
A v20.2.4 radosgw does not retry, and the write answers 404 NoSuchKey
([rgw_rados.cc:7888-7891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7888-L7891);
`docs/ceph-upstream-bugs.md`, "radosgw fails a write whose bucket was
resharded since the request read it"); on Tentacle rgw-go takes v19.2.7's
fix all the same (decided 2026-10-13). The retry covers the prepare and the
completion worker's re-send of a completion or cancel that a reshard
refused, as v19.2.7's does. The first send of a completion or cancel, which
the write does not wait for, is dropped when it meets a missing shard, as
radosgw of either release drops it
([rgw_rados.cc:1011-1017](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1011-L1017),
[rgw_rados.cc:1052-1058](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1052-L1058)), and its pending
entry is left for listing to reconcile. rgw-go reads the instance again
from RADOS, past its metadata cache, after a missing shard and after a
reshard wait alike, where radosgw answers the read from its bucket info
cache when that holds the instance
([svc_bucket_sobj.cc:281-299](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_bucket_sobj.cc#L281-L299),
[svc_bucket_sobj.cc:232-250](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L232-L250)), which the
reshard's notify may not have reached.

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
`rgw_bucket_persistent_notif_num_shards`, default 11 in main at
8aa24c51928, and each event routed to the shard radosgw's hash of bucket
and key selects; a topic record without the field has one shard. rgw-go follows the same
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
19.2.7 included (Tentacle fixed it in 20.2.3), lists the queue registry
without ever advancing its paging marker, so a registry holding more than
1024 queue objects hangs that radosgw, which a newer release's sharding can
reach from roughly a hundred topics on a shared registry. And OSDs below
the floor, Squid before 19.2.4 and Tentacle before 20.2.3, added a ten-byte
overhead per entry to reserved capacity on every reserve but took it back
on no commit, abort or expiry, so a 128 MB queue reached permanent SlowDown
after roughly 12.8 million events. OSDs at the floor no longer add the
overhead, but drift accrued before an upgrade can survive it, and the
reserved size is not exact even at the floor; see docs/differences.md,
"Queue initialization and reservation accounting".

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

## Pending

None. D3N was excluded on 2026-09-25. Bucket notifications were first
excluded the same day, then sized against Rook's integration tests and
kept at the scope described under "Explicitly not excluded", with only
Kafka and AMQP delivery excluded. That is the shape the rgw-rs review
recommended.

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
